package agent

import (
	"bufio"
	"context"
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

const pythonREPLPrelude = `
import ast
import asyncio
import contextlib
import dataclasses
import hashlib
from html.parser import HTMLParser
import io
import json
import math
import os
import pickle
import signal
import sys
import traceback
from typing import Optional
import urllib.parse
import urllib.request

_protocol_in = sys.stdin
_protocol_out = sys.stdout
_active_processes = set()
_executing = False
_llm_lock = asyncio.Lock()
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

def _kill_active_processes():
    for process in _active_processes:
        if process.returncode is None:
            try:
                os.killpg(process.pid, signal.SIGKILL)
            except ProcessLookupError:
                pass

def _stop_repl(*_):
    _kill_active_processes()
    os._exit(143)

def _interrupt_execution(*_):
    if not _executing:
        return
    _kill_active_processes()
    raise KeyboardInterrupt

signal.signal(signal.SIGTERM, _stop_repl)
signal.signal(signal.SIGINT, _interrupt_execution)

async def shell(command, timeout=None):
    """Run a shell command and return ShellResult with combined stdout/stderr."""
    if timeout is not None and (not isinstance(timeout, (int, float)) or not math.isfinite(timeout) or timeout <= 0):
        raise ValueError("timeout must be a positive finite number of seconds")
    process = await asyncio.create_subprocess_shell(
        command,
        executable="/bin/sh",
        stdin=asyncio.subprocess.DEVNULL,
        stdout=asyncio.subprocess.PIPE,
        stderr=asyncio.subprocess.STDOUT,
        start_new_session=True,
    )
    _active_processes.add(process)
    try:
        try:
            output, _ = await asyncio.wait_for(process.communicate(), timeout)
        except asyncio.TimeoutError:
            os.killpg(process.pid, signal.SIGKILL)
            output, _ = await process.communicate()
            return ShellResult(output.decode(errors="replace"), -1, f"timeout:{timeout:g}")
        except asyncio.CancelledError:
            if process.returncode is None:
                os.killpg(process.pid, signal.SIGKILL)
                await process.wait()
            raise
        error: Optional[str] = None if process.returncode == 0 else f"exit status {process.returncode}"
        return ShellResult(output.decode(errors="replace"), process.returncode, error)
    finally:
        _active_processes.discard(process)

class _SearchParser(HTMLParser):
    def __init__(self):
        super().__init__()
        self.results = []
        self.result = None
        self.capture = None
        self.capture_tag = None

    def handle_starttag(self, tag, attrs):
        attrs = dict(attrs)
        classes = attrs.get("class", "").split()
        if "result__a" in classes:
            self.result = {"href": attrs.get("href", ""), "title": [], "snippet": []}
            self.results.append(self.result)
            self.capture = self.result["title"]
            self.capture_tag = tag
        elif self.result is not None and "result__snippet" in classes:
            self.capture = self.result["snippet"]
            self.capture_tag = tag

    def handle_endtag(self, tag):
        if tag == self.capture_tag:
            self.capture = None
            self.capture_tag = None

    def handle_data(self, data):
        if self.capture is not None:
            self.capture.append(data)

def _search_text(parts):
    return " ".join("".join(parts).split())

def _result_url(value):
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

async def llm(prompt: str) -> str:
    """Run one fresh, tool-free model call and return its response as a string."""
    if not isinstance(prompt, str):
        raise TypeError("llm() prompt must be a string")
    async with _llm_lock:
        _protocol_out.write(json.dumps({"host_call": "llm", "prompt": prompt}) + "\n")
        _protocol_out.flush()
        response = json.loads(await asyncio.to_thread(_protocol_in.readline))
    if "error" in response:
        raise RuntimeError(response["error"])
    return response["result"]

def _web_search(query, max_results):
    data = urllib.parse.urlencode({"q": query}).encode()
    request = urllib.request.Request(
        _web_search_url,
        data=data,
        headers={"User-Agent": "Mozilla/5.0 (compatible; fn-agent/1.0)"},
    )
    with urllib.request.urlopen(request) as response:
        body = response.read().decode("utf-8", errors="replace")
    parser = _SearchParser()
    parser.feed(body)
    results = []
    for result in parser.results:
        url = _result_url(result["href"])
        if url is None:
            continue
        results.append(SearchResult(_search_text(result["title"]), url, _search_text(result["snippet"])))
        if len(results) == max_results:
            break
    return results

async def web_search(query, max_results=8):
    """Search DuckDuckGo and return a list of SearchResult values."""
    query = query.strip()
    if not query:
        raise ValueError("query must not be empty")
    if not isinstance(max_results, int) or not 1 <= max_results <= 20:
        raise ValueError("max_results must be between 1 and 20")
    return await asyncio.to_thread(_web_search, query, max_results)

_user_globals = {}`

const pythonREPLScript = pythonREPLPrelude + pythonMCPScript + `
_builtin_names = {"shell", "web_search", "llm", "mcp", "ShellResult", "SearchResult", "__builtins__"}

def _snapshot(path, objects_path):
    os.makedirs(objects_path, mode=0o700, exist_ok=True)
    values = {}
    for name, value in _user_globals.items():
        if name in _builtin_names:
            continue
        try:
            data = pickle.dumps(value)
        except Exception:
            continue
        digest = hashlib.sha256(data).hexdigest()
        object_path = os.path.join(objects_path, digest)
        if not os.path.exists(object_path):
            temporary_path = object_path + ".tmp"
            with open(temporary_path, "wb") as object_file:
                object_file.write(data)
            os.chmod(temporary_path, 0o600)
            os.replace(temporary_path, object_path)
        values[name] = digest
    with open(path, "w") as state:
        json.dump(values, state)
    os.chmod(path, 0o600)
    return {"output": ""}

def _restore(path, objects_path):
    with open(path) as state:
        values = json.load(state)
    for name in list(_user_globals):
        if name not in _builtin_names:
            del _user_globals[name]
    for name, digest in values.items():
        try:
            with open(os.path.join(objects_path, digest), "rb") as object_file:
                _user_globals[name] = pickle.load(object_file)
        except Exception:
            pass
    return {"output": ""}

async def _run_code(tree):
    result = eval(compile(tree, "<fn-repl>", "exec", flags=ast.PyCF_ALLOW_TOP_LEVEL_AWAIT), _user_globals)
    if result is not None:
        await result

async def _execute(code):
    global _executing
    _executing = True
    _user_globals.update(
        shell=shell,
        web_search=web_search,
        llm=llm,
        mcp=mcp,
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
                    await _run_code(prefix)
                expression = ast.Expression(tree.body[-1].value)
                value = eval(compile(expression, "<fn-repl>", "eval", flags=ast.PyCF_ALLOW_TOP_LEVEL_AWAIT), _user_globals)
                if asyncio.iscoroutine(value):
                    value = await value
            else:
                await _run_code(tree)
        rendered = output.getvalue()
        if value is not None:
            rendered += repr(value)
        return {"output": rendered}
    except BaseException:
        return {"output": output.getvalue() + traceback.format_exc(), "error": True}
    finally:
        _executing = False

async def _main():
    while line := await asyncio.to_thread(_protocol_in.readline):
        request = json.loads(line)
        operation = request.get("op", "execute")
        if operation == "snapshot":
            response = _snapshot(request["path"], request["objects_path"])
        elif operation == "restore":
            response = _restore(request["path"], request["objects_path"])
        else:
            response = await _execute(request.get("code", ""))
        _protocol_out.write(json.dumps(response) + "\n")
        _protocol_out.flush()
    await mcp.close()

asyncio.run(_main())
`

type pythonREPL struct {
	mu      sync.Mutex
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
	if _, err := r.stdin.Write(append(data, '\n')); err != nil {
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
			_ = r.cmd.Process.Signal(syscall.SIGINT)
			select {
			case <-read:
				return "", true, ctx.Err()
			case <-time.After(500 * time.Millisecond):
				r.stop()
				return "", true, ctx.Err()
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
