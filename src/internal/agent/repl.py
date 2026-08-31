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

_user_globals = {}


class _MCP:
    def __init__(self):
        self._clients = {}
        self._client_locks = {}

    def _config_path(self):
        configured = os.environ.get("FN_MCP_CONFIG")
        if configured:
            return os.path.expanduser(configured)
        return os.path.expanduser("~/.fn/mcp.json")

    def _config(self):
        with open(self._config_path(), encoding="utf-8") as config_file:
            config = json.load(config_file)
        return config.get("mcpServers", config.get("servers", config))

    async def servers(self):
        """Return the names of configured MCP servers."""
        return list(self._config())

    def _expand(self, value):
        if isinstance(value, str):
            return os.path.expandvars(os.path.expanduser(value))
        if isinstance(value, list):
            return [self._expand(item) for item in value]
        if isinstance(value, dict):
            return {key: self._expand(item) for key, item in value.items()}
        return value

    def _oauth(self, config):
        from fastmcp.client.auth import OAuth
        from key_value.aio._utils.sanitization import AlwaysHashStrategy
        from key_value.aio.stores.filetree import FileTreeStore
        from urllib.parse import urlparse

        token_dir = os.path.expanduser("~/.fn/mcp-auth")
        os.makedirs(token_dir, mode=0o700, exist_ok=True)
        os.chmod(token_dir, 0o700)
        redirect = config.get("oauthRedirectUrl")
        callback_port = urlparse(redirect).port if redirect else None
        return OAuth(
            scopes=config.get("oauthScopes"),
            client_name="fn agent",
            client_id=config.get("oauthClientId"),
            client_secret=config.get("oauthClientSecret"),
            callback_port=callback_port,
            token_storage=FileTreeStore(
                data_directory=token_dir,
                key_sanitization_strategy=AlwaysHashStrategy(),
            ),
        )

    def _transport(self, config):
        from fastmcp.client.transports import SSETransport, StdioTransport, StreamableHttpTransport

        config = self._expand(config)
        if "command" in config:
            return StdioTransport(
                command=config["command"],
                args=config.get("args", []),
                env=config.get("env"),
                cwd=config.get("cwd"),
                log_file=sys.__stderr__,
            )

        url = config.get("url", config.get("baseUrl"))
        auth = self._oauth(config) if config.get("auth") == "oauth" else config.get("auth")
        transport = SSETransport if config.get("type") == "sse" else StreamableHttpTransport
        return transport(url=url, headers=config.get("headers"), auth=auth)

    async def _client(self, server):
        if server in self._clients:
            return self._clients[server]
        lock = self._client_locks.setdefault(server, asyncio.Lock())
        async with lock:
            if server in self._clients:
                return self._clients[server]
            try:
                from fastmcp import Client
            except ImportError as error:
                raise RuntimeError(
                    "MCP requires fastmcp: python3 -m pip install 'fastmcp-slim[client]>=3.4,<4' 'websockets>=15'"
                ) from error
            config = self._config()[server]
            client = Client(self._transport(config))
            await client.__aenter__()
            self._clients[server] = client
            return client

    def _plain(self, value):
        if hasattr(value, "model_dump"):
            return self._plain(value.model_dump(by_alias=True, exclude_none=True))
        if dataclasses.is_dataclass(value):
            return self._plain(dataclasses.asdict(value))
        if isinstance(value, dict):
            return {key: self._plain(item) for key, item in value.items()}
        if isinstance(value, (list, tuple)):
            return [self._plain(item) for item in value]
        return value

    async def _tools(self, server):
        client = await self._client(server)
        return [self._plain(tool) for tool in await client.list_tools()]

    async def tools(self, server):
        """Return tool definitions for one MCP server."""
        return await self._tools(server)

    async def search(self, query, server=None):
        """Search tool names and descriptions, optionally within one server."""
        words = query.lower().split()
        servers = [server] if server else await self.servers()
        matches = []
        for server_name in servers:
            for tool in await self.tools(server_name):
                text = (tool.get("name", "") + " " + tool.get("description", "")).lower()
                score = sum(word in text for word in words)
                if score:
                    matches.append((score, server_name, tool))
        matches.sort(key=lambda match: (-match[0], match[1], match[2].get("name", "")))
        return [dict(match[2], server=match[1]) for match in matches]

    def _split_name(self, name):
        for server in self._config():
            prefix = server + "."
            if name.startswith(prefix):
                return server, name[len(prefix):]
        raise ValueError("MCP tool names must be <server>.<tool>")

    async def schema(self, name):
        """Return the definition of an MCP tool named <server>.<tool>."""
        server, tool_name = self._split_name(name)
        for tool in await self.tools(server):
            if tool.get("name") == tool_name:
                return tool
        raise KeyError(name)

    async def _call(self, server, tool, arguments):
        client = await self._client(server)
        result = await client.call_tool(tool, arguments)
        if result.is_error:
            text = "\n".join(block.text for block in result.content if hasattr(block, "text"))
            raise RuntimeError(text or "MCP tool call failed")
        if result.data is not None:
            return self._plain(result.data)
        if result.structured_content is not None:
            return self._plain(result.structured_content)
        if len(result.content) == 1 and hasattr(result.content[0], "text"):
            text = result.content[0].text
            try:
                return json.loads(text)
            except json.JSONDecodeError:
                return text
        return self._plain(result.content)

    async def call(self, name, arguments=None, **kwargs):
        """Call an MCP tool named <server>.<tool>. Pass arguments as a dict or keywords."""
        server, tool = self._split_name(name)
        values = {} if arguments is None else dict(arguments)
        values.update(kwargs)
        return await self._call(server, tool, values)

    async def _close(self):
        for client in reversed(list(self._clients.values())):
            await client.__aexit__(None, None, None)
        self._clients.clear()
        self._client_locks.clear()

    async def close(self):
        """Close all active MCP connections."""
        await self._close()

mcp = _MCP()

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
