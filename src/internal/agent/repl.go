package agent

import (
	"bufio"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
)

//go:embed repl.py
var pythonREPLScript string

type pythonREPL struct {
	mu      sync.Mutex
	stdinMu sync.Mutex
	llm     func(context.Context, string) (string, error)
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	stdout  *bufio.Reader
	stderr  strings.Builder
	started bool
}

type replResult struct {
	Output   string `json:"output"`
	Error    bool   `json:"error"`
	HostCall string `json:"host_call"`
	ID       int    `json:"id"`
	Prompt   string `json:"prompt"`
}

func newPythonREPL(llm func(context.Context, string) (string, error)) *pythonREPL {
	return &pythonREPL{llm: llm}
}

func (r *pythonREPL) start() error {
	r.stderr.Reset()
	r.cmd = exec.Command("python3", "-u", "-c", pythonREPLScript)
	stdin, err := r.cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := r.cmd.StdoutPipe()
	if err != nil {
		return err
	}
	r.cmd.Stderr = &r.stderr
	if err := r.cmd.Start(); err != nil {
		return fmt.Errorf("start Python REPL: %w", err)
	}
	r.stdin = stdin
	r.stdout = bufio.NewReader(stdout)
	r.started = true
	return nil
}

func (r *pythonREPL) stop() {
	if r.cmd == nil {
		return
	}
	cmd := r.cmd
	r.stdinMu.Lock()
	_ = r.stdin.Close()
	r.stdinMu.Unlock()
	_ = cmd.Process.Signal(syscall.SIGTERM)
	done := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		_ = cmd.Process.Kill()
		<-done
	}
	r.cmd, r.stdin, r.stdout = nil, nil, nil
}

func (r *pythonREPL) close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.stop()
}

func (r *pythonREPL) execute(ctx context.Context, code string) (string, bool, error) {
	if err := ctx.Err(); err != nil {
		return "", true, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cmd == nil {
		if err := r.start(); err != nil {
			return "", true, err
		}
	}
	data, err := json.Marshal(map[string]string{"code": code})
	if err != nil {
		return "", true, err
	}
	if _, err := r.write(append(data, '\n')); err != nil {
		r.stop()
		return "", true, err
	}
	return r.readResult(ctx)
}

func (r *pythonREPL) snapshot(path, objectsPath string) error {
	tmp := path + ".tmp"
	if err := r.operation(map[string]string{"op": "snapshot", "path": tmp, "objects_path": objectsPath}); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (r *pythonREPL) restore(path, objectsPath string) error {
	return r.operation(map[string]string{"op": "restore", "path": path, "objects_path": objectsPath})
}

func (r *pythonREPL) operation(request map[string]string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cmd == nil {
		if r.started {
			return errors.New("Python REPL is stopped")
		}
		if err := r.start(); err != nil {
			return err
		}
	}
	data, err := json.Marshal(request)
	if err != nil {
		return err
	}
	if _, err := r.write(append(data, '\n')); err != nil {
		r.stop()
		return err
	}
	_, _, err = r.readResult(context.Background())
	return err
}

func (r *pythonREPL) write(data []byte) (int, error) {
	return r.writeTo(r.stdin, data)
}

func (r *pythonREPL) writeTo(stdin io.Writer, data []byte) (int, error) {
	r.stdinMu.Lock()
	defer r.stdinMu.Unlock()
	return stdin.Write(data)
}

func (r *pythonREPL) readResult(ctx context.Context) (string, bool, error) {
	type readResult struct {
		line []byte
		err  error
	}
	hostCallError := make(chan error, 1)
	cancel := ctx.Done()
	var canceled error
	for {
		read := make(chan readResult, 1)
		go func() {
			line, err := r.stdout.ReadBytes('\n')
			read <- readResult{line: line, err: err}
		}()

		var result readResult
		for {
			select {
			case <-cancel:
				canceled = ctx.Err()
				cancel = nil
				_ = r.cmd.Process.Signal(syscall.SIGINT)
			case err := <-hostCallError:
				r.stop()
				return "", true, err
			case result = <-read:
				goto received
			}
		}

	received:
		if result.err != nil {
			r.stop()
			if stderr := strings.TrimSpace(r.stderr.String()); stderr != "" {
				return "", true, fmt.Errorf("Python REPL stopped: %s", stderr)
			}
			return "", true, fmt.Errorf("Python REPL stopped: %w", result.err)
		}
		var response replResult
		if err := json.Unmarshal(result.line, &response); err != nil {
			r.stop()
			return "", true, fmt.Errorf("invalid Python REPL response: %w", err)
		}
		if response.HostCall == "llm" {
			go r.runLLMHostCall(ctx, r.stdin, response.ID, response.Prompt, hostCallError)
			continue
		}
		if canceled != nil {
			return "", true, canceled
		}
		return response.Output, response.Error, nil
	}
}

func (r *pythonREPL) runLLMHostCall(ctx context.Context, stdin io.Writer, id int, prompt string, hostCallError chan<- error) {
	text, callErr := r.llm(ctx, prompt)
	hostResponse := map[string]any{"id": id, "result": text}
	if callErr != nil {
		hostResponse = map[string]any{"id": id, "error": callErr.Error()}
	}
	data, _ := json.Marshal(hostResponse)
	if _, err := r.writeTo(stdin, append(data, '\n')); err != nil {
		select {
		case hostCallError <- err:
		default:
		}
	}
}

func formatREPLError(err error) string {
	if err == context.Canceled {
		return ""
	}
	return "REPL error: " + err.Error()
}
