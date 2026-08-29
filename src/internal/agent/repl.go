package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
)

const pythonREPLScript = `
import ast
import contextlib
import dataclasses
import html
import io
import json
import math
import os
import pickle
import re
import signal
import subprocess
import sys
import traceback
from typing import Optional
import urllib.parse
import urllib.request

_protocol_in = sys.stdin
_protocol_out = sys.stdout
_active_process = None
_executing = False
_web_search_url = "https://html.duckduckgo.com/html/"

@dataclasses.dataclass
class ShellResult:
    stdout: str
    exit_code: int
    error: Optional[str] = None

@dataclasses.dataclass
class SearchResult:
    title: str
    url: str
    snippet: str

def _kill_active_process():
    if _active_process is not None and _active_process.poll() is None:
        try:
            os.killpg(_active_process.pid, signal.SIGKILL)
        except ProcessLookupError:
            pass

def _stop_repl(*_):
    _kill_active_process()
    os._exit(143)

def _interrupt_execution(*_):
    global _executing
    if not _executing:
        return
    _executing = False
    _kill_active_process()
    raise KeyboardInterrupt

signal.signal(signal.SIGTERM, _stop_repl)
signal.signal(signal.SIGINT, _interrupt_execution)

def shell(command, timeout=None):
    """Run a shell command and return ShellResult with combined stdout/stderr."""
    global _active_process
    if timeout is not None and (not isinstance(timeout, (int, float)) or not math.isfinite(timeout) or timeout <= 0):
        raise ValueError("timeout must be a positive finite number of seconds")
    process = subprocess.Popen(
        command,
        shell=True,
        executable="/bin/sh",
        stdin=subprocess.DEVNULL,
        stdout=subprocess.PIPE,
        stderr=subprocess.STDOUT,
        text=True,
        start_new_session=True,
    )
    _active_process = process
    try:
        try:
            output, _ = process.communicate(timeout=timeout)
        except subprocess.TimeoutExpired:
            os.killpg(process.pid, signal.SIGKILL)
            output, _ = process.communicate()
            return ShellResult(output, -1, f"timeout:{timeout:g}")
        except KeyboardInterrupt:
            process.wait()
            raise
        error: Optional[str] = None if process.returncode == 0 else f"exit status {process.returncode}"
        return ShellResult(output, process.returncode, error)
    finally:
        _active_process = None

_result_link_re = re.compile(r'<a[^>]+class="[^"]*\bresult__a\b[^"]*"[^>]+href="([^"]+)"[^>]*>(.*?)</a>', re.I | re.S)
_snippet_re = re.compile(r'<(?:a|div)[^>]+class="[^"]*\bresult__snippet\b[^"]*"[^>]*>(.*?)</(?:a|div)>', re.I | re.S)
_tag_re = re.compile(r'<[^>]*>', re.S)

def _html_text(value):
    return " ".join(html.unescape(_tag_re.sub(" ", value)).split())

def _result_url(value):
    value = html.unescape(value)
    if value.startswith("//"):
        value = "https:" + value
    parsed = urllib.parse.urlparse(value)
    target = urllib.parse.parse_qs(parsed.query).get("uddg")
    if target:
        parsed = urllib.parse.urlparse(target[0])
    if parsed.scheme not in ("http", "https"):
        return None
    if parsed.hostname and parsed.hostname.lower() == "duckduckgo.com" and parsed.path.startswith("/y.js"):
        return None
    return urllib.parse.urlunparse(parsed)

def llm(prompt: str) -> str:
    """Run one fresh, tool-free model call and return its response as a string."""
    if not isinstance(prompt, str):
        raise TypeError("llm() prompt must be a string")
    _protocol_out.write(json.dumps({"host_call": "llm", "prompt": prompt}) + "\n")
    _protocol_out.flush()
    response = json.loads(_protocol_in.readline())
    if "error" in response:
        raise RuntimeError(response["error"])
    return response["result"]

def web_search(query, max_results=8):
    """Search DuckDuckGo and return a list of SearchResult values."""
    query = query.strip()
    if not query:
        raise ValueError("query must not be empty")
    if not isinstance(max_results, int) or not 1 <= max_results <= 20:
        raise ValueError("max_results must be between 1 and 20")
    data = urllib.parse.urlencode({"q": query}).encode()
    request = urllib.request.Request(
        _web_search_url,
        data=data,
        headers={"User-Agent": "Mozilla/5.0 (compatible; fn-agent/1.0)"},
    )
    with urllib.request.urlopen(request) as response:
        body = response.read().decode("utf-8", errors="replace")
    links = _result_link_re.findall(body)
    snippets = _snippet_re.findall(body)
    results = []
    for index, (href, title) in enumerate(links):
        url = _result_url(href)
        if url is None:
            continue
        snippet = _html_text(snippets[index]) if index < len(snippets) else ""
        results.append(SearchResult(_html_text(title), url, snippet))
        if len(results) == max_results:
            break
    return results

_user_globals = {}
_builtin_names = {"shell", "web_search", "llm", "ShellResult", "SearchResult", "__builtins__"}

def _snapshot(path):
    values = {}
    for name, value in _user_globals.items():
        if name in _builtin_names:
            continue
        try:
            values[name] = pickle.dumps(value)
        except Exception:
            pass
    with open(path, "wb") as state:
        pickle.dump(values, state)
    os.chmod(path, 0o600)
    return {"output": ""}

def _restore(path):
    with open(path, "rb") as state:
        values = pickle.load(state)
    for name, value in values.items():
        try:
            _user_globals[name] = pickle.loads(value)
        except Exception:
            pass
    return {"output": ""}

def _execute(code):
    _user_globals.update(
        shell=shell,
        web_search=web_search,
        llm=llm,
        ShellResult=ShellResult,
        SearchResult=SearchResult,
    )
    output = io.StringIO()
    value = None
    try:
        tree = ast.parse(code, mode="exec")
        with contextlib.redirect_stdout(output), contextlib.redirect_stderr(output):
            if tree.body and isinstance(tree.body[-1], ast.Expr):
                prefix = ast.Module(body=tree.body[:-1], type_ignores=[])
                if prefix.body:
                    exec(compile(prefix, "<fn-repl>", "exec"), _user_globals)
                expression = ast.Expression(tree.body[-1].value)
                value = eval(compile(expression, "<fn-repl>", "eval"), _user_globals)
            else:
                exec(compile(tree, "<fn-repl>", "exec"), _user_globals)
        rendered = output.getvalue()
        if value is not None:
            rendered += repr(value)
        return {"output": rendered}
    except BaseException:
        return {"output": output.getvalue() + traceback.format_exc(), "error": True}

for line in _protocol_in:
    try:
        request = json.loads(line)
        _executing = True
        operation = request.get("op", "execute")
        if operation == "snapshot":
            response = _snapshot(request["path"])
        elif operation == "restore":
            response = _restore(request["path"])
        else:
            response = _execute(request.get("code", ""))
    except BaseException:
        response = {"output": traceback.format_exc(), "error": True}
    finally:
        _executing = False
    _protocol_out.write(json.dumps(response) + "\n")
    _protocol_out.flush()
`

type pythonREPL struct {
	mu     sync.Mutex
	llm    func(context.Context, string) (string, error)
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *bufio.Reader
	stderr strings.Builder
}

type replResult struct {
	Output   string `json:"output"`
	Error    bool   `json:"error"`
	HostCall string `json:"host_call"`
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
	return nil
}

func (r *pythonREPL) stop() {
	if r.cmd == nil {
		return
	}
	cmd := r.cmd
	_ = r.stdin.Close()
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
	if _, err := r.stdin.Write(append(data, '\n')); err != nil {
		r.stop()
		return "", true, err
	}
	return r.readResult(ctx)
}

func (r *pythonREPL) snapshot(path string) error {
	tmp := path + ".tmp"
	if err := r.operation(map[string]string{"op": "snapshot", "path": tmp}); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (r *pythonREPL) restore(path string) error {
	return r.operation(map[string]string{"op": "restore", "path": path})
}

func (r *pythonREPL) operation(request map[string]string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cmd == nil {
		if err := r.start(); err != nil {
			return err
		}
	}
	data, err := json.Marshal(request)
	if err != nil {
		return err
	}
	if _, err := r.stdin.Write(append(data, '\n')); err != nil {
		r.stop()
		return err
	}
	_, _, err = r.readResult(context.Background())
	return err
}

func (r *pythonREPL) readResult(ctx context.Context) (string, bool, error) {
	type readResult struct {
		line []byte
		err  error
	}
	for {
		read := make(chan readResult, 1)
		go func() {
			line, err := r.stdout.ReadBytes('\n')
			read <- readResult{line: line, err: err}
		}()
		select {
		case <-ctx.Done():
			ticker := time.NewTicker(10 * time.Millisecond)
			defer ticker.Stop()
			timeout := time.NewTimer(500 * time.Millisecond)
			defer timeout.Stop()
			for {
				_ = r.cmd.Process.Signal(syscall.SIGINT)
				select {
				case <-read:
					return "", true, ctx.Err()
				case <-ticker.C:
				case <-timeout.C:
					r.stop()
					return "", true, ctx.Err()
				}
			}
		case result := <-read:
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
				text, err := r.llm(ctx, response.Prompt)
				hostResponse := map[string]string{"result": text}
				if err != nil {
					hostResponse = map[string]string{"error": err.Error()}
				}
				data, _ := json.Marshal(hostResponse)
				if _, err := r.stdin.Write(append(data, '\n')); err != nil {
					r.stop()
					return "", true, err
				}
				continue
			}
			return response.Output, response.Error, nil
		}
	}
}

func formatREPLError(err error) string {
	if err == context.Canceled {
		return ""
	}
	return "REPL error: " + err.Error()
}
