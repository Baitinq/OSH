#!/usr/bin/env bash
set -euo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
tmp=$(mktemp -d "${TMPDIR:-/tmp}/fn-mcp-oauth-e2e.XXXXXX")
cleanup() {
  [[ -n "${server_pid:-}" ]] && kill "$server_pid" 2>/dev/null || true
  [[ -n "${model_pid:-}" ]] && kill "$model_pid" 2>/dev/null || true
  rm -rf "$tmp"
}
trap cleanup EXIT

uv venv -q --python 3.13 "$tmp/venv"
uv pip install -q --python "$tmp/venv/bin/python" 'fastmcp-slim[client]>=3.4,<4' 'websockets>=15'
cat > "$tmp/oauth_mcp.py" <<'PY'
import json, os
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import parse_qs, urlencode, urlparse

class Handler(BaseHTTPRequestHandler):
    registrations = 0
    authorizations = 0
    token_requests = 0
    tool_calls = 0

    @property
    def origin(self):
        return f"http://127.0.0.1:{self.server.server_port}"

    def body(self):
        return self.rfile.read(int(self.headers.get("Content-Length", "0")))

    def json_response(self, value, status=200, headers=None):
        data = json.dumps(value).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(data)))
        for name, value in (headers or {}).items(): self.send_header(name, value)
        self.end_headers()
        self.wfile.write(data)

    def do_GET(self):
        parsed = urlparse(self.path)
        if parsed.path == "/.well-known/oauth-protected-resource":
            self.json_response({"resource": self.origin + "/mcp", "authorization_servers": [self.origin]})
        elif parsed.path == "/.well-known/oauth-authorization-server":
            self.json_response({
                "issuer": self.origin,
                "authorization_endpoint": self.origin + "/authorize",
                "token_endpoint": self.origin + "/token",
                "registration_endpoint": self.origin + "/register",
                "response_types_supported": ["code"],
                "grant_types_supported": ["authorization_code", "refresh_token"],
                "code_challenge_methods_supported": ["S256"],
                "token_endpoint_auth_methods_supported": ["none"],
            })
        elif parsed.path == "/authorize":
            Handler.authorizations += 1
            query = parse_qs(parsed.query)
            location = query["redirect_uri"][0] + "?" + urlencode({"code": "e2e-code", "state": query["state"][0]})
            self.send_response(302)
            self.send_header("Location", location)
            self.end_headers()
        elif parsed.path == "/stats":
            self.json_response({"registrations": Handler.registrations, "authorizations": Handler.authorizations, "token_requests": Handler.token_requests, "tool_calls": Handler.tool_calls})
        else:
            self.send_error(404)

    def do_POST(self):
        parsed = urlparse(self.path)
        if parsed.path == "/register":
            Handler.registrations += 1
            metadata = json.loads(self.body())
            self.json_response({"client_id": "fn-e2e", "redirect_uris": metadata["redirect_uris"], "grant_types": metadata["grant_types"], "response_types": ["code"], "token_endpoint_auth_method": "none"}, 201)
            return
        if parsed.path == "/token":
            Handler.token_requests += 1
            form = parse_qs(self.body().decode())
            assert form["code"][0] == "e2e-code"
            self.json_response({"access_token": "e2e-access-token", "refresh_token": "e2e-refresh-token", "token_type": "Bearer", "expires_in": 3600})
            return
        if parsed.path != "/mcp":
            self.send_error(404)
            return
        if self.headers.get("Authorization") != "Bearer e2e-access-token":
            self.json_response({"error": "unauthorized"}, 401, {"WWW-Authenticate": f'Bearer resource_metadata="{self.origin}/.well-known/oauth-protected-resource"'})
            return
        request = json.loads(self.body())
        if request.get("method") == "notifications/initialized":
            self.send_response(202); self.end_headers(); return
        request_id = request["id"]
        method = request["method"]
        if method == "initialize":
            result = {"protocolVersion": "2025-06-18", "capabilities": {"tools": {}}, "serverInfo": {"name": "oauth-e2e", "version": "1"}}
        elif method == "tools/list":
            result = {"tools": [{"name": "secret", "description": "Return an OAuth-protected value", "inputSchema": {"type": "object", "properties": {}, "additionalProperties": False}}]}
        elif method == "tools/call":
            Handler.tool_calls += 1
            result = {"content": [{"type": "text", "text": json.dumps({"authenticated": True, "call": Handler.tool_calls})}]}
        else:
            raise AssertionError(method)
        self.json_response({"jsonrpc": "2.0", "id": request_id, "result": result})

    def log_message(self, *args): pass

server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
open(os.environ["PORT_FILE"], "w").write(str(server.server_port))
server.serve_forever()
PY
cat > "$tmp/browser" <<'SH'
#!/bin/sh
curl -fsSL "$1" >/dev/null
SH
chmod +x "$tmp/browser"
PORT_FILE="$tmp/server-port" "$tmp/venv/bin/python" "$tmp/oauth_mcp.py" >"$tmp/server.log" 2>&1 &
server_pid=$!
for _ in $(seq 1 100); do [[ -s "$tmp/server-port" ]] && break; sleep .02; done
server_port=$(cat "$tmp/server-port")

cat > "$tmp/mcp.json" <<JSON
{"mcpServers":{"secure":{"url":"http://127.0.0.1:$server_port/mcp","auth":"oauth"}}}
JSON
cat > "$tmp/model.py" <<'PY'
import json, os
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
class Handler(BaseHTTPRequestHandler):
    def do_POST(self):
        request = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
        tools = [m["content"] for m in request["messages"] if m["role"] == "tool"]
        if tools:
            ok = "'authenticated': True" in tools[-1]
            chunks = [{"choices":[{"delta":{"content":"OAuth MCP E2E passed." if ok else "OAuth MCP E2E failed: " + tools[-1]}}]}]
        else:
            arguments = json.dumps({"code": "mcp.call('secure.secret')"})
            chunks = [{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"oauth_e2e","type":"function","function":{"name":"repl","arguments":arguments}}]}}]}]
        self.send_response(200); self.send_header("Content-Type", "text/event-stream"); self.end_headers()
        for chunk in chunks: self.wfile.write(b"data: " + json.dumps(chunk).encode() + b"\n\n")
        self.wfile.write(b"data: [DONE]\n\n"); self.wfile.flush()
    def log_message(self, *args): pass
server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
open(os.environ["PORT_FILE"], "w").write(str(server.server_port))
server.serve_forever()
PY
PORT_FILE="$tmp/model-port" "$tmp/venv/bin/python" "$tmp/model.py" >"$tmp/model.log" 2>&1 &
model_pid=$!
for _ in $(seq 1 100); do [[ -s "$tmp/model-port" ]] && break; sleep .02; done
model_port=$(cat "$tmp/model-port")

cd "$root"
go build -o "$tmp/fn" ./cmd/fn
mkdir "$tmp/home"
run_fn() {
  PATH="$tmp/venv/bin:$PATH" BROWSER="$tmp/browser" FN_PROVIDER=openai-completions FN_API_KEY=test FN_MODEL=e2e FN_BASE_URL="http://127.0.0.1:$model_port/v1" FN_MCP_CONFIG="$tmp/mcp.json" HOME="$tmp/home" "$tmp/fn" -p "Call the protected MCP tool" 2>/dev/null
}
[[ "$(run_fn)" == "OAuth MCP E2E passed." ]]
[[ "$(run_fn)" == "OAuth MCP E2E passed." ]]
stats=$(curl -fsSL "http://127.0.0.1:$server_port/stats")
python="$tmp/venv/bin/python"
HOME="$tmp/home" "$python" - "$stats" <<'PY'
import json, sys
import pathlib
stats = json.loads(sys.argv[1])
assert stats["registrations"] == 1 and stats["authorizations"] >= 1 and stats["token_requests"] == 1 and stats["tool_calls"] == 2, stats
auth_dir = pathlib.Path.home() / ".fn" / "mcp-auth"
token_files = [path for path in auth_dir.rglob("*") if path.is_file()]
assert token_files
assert auth_dir.stat().st_mode & 0o077 == 0
assert all(path.stat().st_mode & 0o077 == 0 for path in token_files)
print("OAuth MCP E2E passed: discovery, PKCE browser callback, secure token persistence, and authenticated HTTP calls.")
PY
