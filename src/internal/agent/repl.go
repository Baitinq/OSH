package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
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
import re
import signal
import subprocess
import sys
import traceback
import urllib.parse
import urllib.request

_protocol_in = sys.stdin
_protocol_out = sys.stdout
_active_process = None
_web_search_url = "https://html.duckduckgo.com/html/"

@dataclasses.dataclass
class ShellResult:
    stdout: str
    exit_code: int
    error: str | None = None

@dataclasses.dataclass
class SearchResult:
    title: str
    url: str
    snippet: str

def _stop_active_process(*_):
    if _active_process is not None and _active_process.poll() is None:
        try:
            os.killpg(_active_process.pid, signal.SIGKILL)
        except ProcessLookupError:
            pass
    os._exit(143)

signal.signal(signal.SIGTERM, _stop_active_process)

def shell(command, timeout=None):
    """Run a shell command and return ShellResult with combined stdout/stderr."""
    global _active_process
    if timeout is not None and (not isinstance(timeout, (int, float)) or not math.isfinite(timeout) or timeout <= 0):
        raise ValueError("timeout must be a positive finite number of seconds")
    process = subprocess.Popen(
        command,
        shell=True,
        executable="/bin/sh",
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
        error = None if process.returncode == 0 else f"exit status {process.returncode}"
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
        headers={"User-Agent": "Mozilla/5.0 (compatible; OSH/1.0)"},
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

_globals = globals()

def _execute(code):
    output = io.StringIO()
    value = None
    try:
        tree = ast.parse(code, mode="exec")
        with contextlib.redirect_stdout(output), contextlib.redirect_stderr(output):
            if tree.body and isinstance(tree.body[-1], ast.Expr):
                prefix = ast.Module(body=tree.body[:-1], type_ignores=[])
                if prefix.body:
                    exec(compile(prefix, "<osh-repl>", "exec"), _globals)
                expression = ast.Expression(tree.body[-1].value)
                value = eval(compile(expression, "<osh-repl>", "eval"), _globals)
            else:
                exec(compile(tree, "<osh-repl>", "exec"), _globals)
        rendered = output.getvalue()
        if value is not None:
            rendered += repr(value)
        return {"output": rendered}
    except BaseException:
        return {"output": output.getvalue() + traceback.format_exc(), "error": True}

for line in _protocol_in:
    try:
        request = json.loads(line)
        response = _execute(request.get("code", ""))
    except BaseException:
        response = {"output": traceback.format_exc(), "error": True}
    _protocol_out.write(json.dumps(response) + "\n")
    _protocol_out.flush()
`

type pythonREPL struct {
	mu     sync.Mutex
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *bufio.Reader
	stderr strings.Builder
}

type replResult struct {
	Output string `json:"output"`
	Error  bool   `json:"error"`
}

func newPythonREPL() *pythonREPL {
	return &pythonREPL{}
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
	type readResult struct {
		line []byte
		err  error
	}
	read := make(chan readResult, 1)
	go func() {
		line, err := r.stdout.ReadBytes('\n')
		read <- readResult{line: line, err: err}
	}()
	select {
	case <-ctx.Done():
		r.stop()
		return "", true, ctx.Err()
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
		return response.Output, response.Error, nil
	}
}

func formatREPLError(err error) string {
	if err == context.Canceled {
		return ""
	}
	return "REPL error: " + err.Error()
}
