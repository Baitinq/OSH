#!/usr/bin/env bash
set -euo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
tmp=$(mktemp -d "${TMPDIR:-/tmp}/fn-mcp-e2e.XXXXXX")
cleanup() { [[ -n "${mock_pid:-}" ]] && kill "$mock_pid" 2>/dev/null || true; rm -rf "$tmp"; }
trap cleanup EXIT

uv venv -q --python 3.13 "$tmp/venv"
uv pip install -q --python "$tmp/venv/bin/python" 'fastmcp-slim[client]>=3.4,<4' 'websockets>=15'
cat > "$tmp/mcp_server.py" <<'PY'
from mcp.server.fastmcp import FastMCP
server = FastMCP("e2e")
calls = 0
@server.tool(description="Return a label and this server process call count")
def sequence(label: str):
    global calls
    calls += 1
    return {"label": label, "call": calls}
server.run()
PY
cat > "$tmp/mcp.json" <<JSON
{"mcpServers":{"e2e":{"command":"$tmp/venv/bin/python","args":["$tmp/mcp_server.py"]}}}
JSON
cat > "$tmp/model.py" <<'PY'
import json, os
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
class Handler(BaseHTTPRequestHandler):
    calls = 0
    def do_POST(self):
        request = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
        Handler.calls += 1
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.end_headers()
        if Handler.calls == 1:
            assert request["tools"][0]["function"]["name"] == "repl"
            code = "servers = await mcp.servers()\nmatches = await mcp.search('sequence call', 'e2e')\nfirst = await mcp.call('e2e.sequence', label='first')\nsecond = await mcp.call('e2e.sequence', label='second')\n(servers, matches[0]['name'], first, second)"
            arguments = json.dumps({"code": code})
            chunks = [
                {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"mcp_e2e","type":"function","function":{"name":"repl","arguments":arguments}}]}}]},
                {"usage":{"prompt_tokens":100,"completion_tokens":20,"total_tokens":120},"choices":[]},
            ]
        else:
            tool_results = [message["content"] for message in request["messages"] if message["role"] == "tool"]
            expected = "(['e2e'], 'sequence', {'label': 'first', 'call': 1}, {'label': 'second', 'call': 2})"
            message = "MCP E2E passed: discovery, persistent stdio calls, and model round-trip." if tool_results == [expected] else "MCP E2E failed: " + repr(tool_results)
            chunks = [
                {"choices":[{"delta":{"content":message}}]},
                {"usage":{"prompt_tokens":150,"completion_tokens":15,"total_tokens":165},"choices":[]},
            ]
        for chunk in chunks:
            self.wfile.write(b"data: " + json.dumps(chunk).encode() + b"\n\n")
        self.wfile.write(b"data: [DONE]\n\n")
        self.wfile.flush()
    def log_message(self, *args): pass
server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
open(os.environ["PORT_FILE"], "w").write(str(server.server_port))
server.serve_forever()
PY
PORT_FILE="$tmp/port" "$tmp/venv/bin/python" "$tmp/model.py" >"$tmp/model.log" 2>&1 &
mock_pid=$!
for _ in $(seq 1 100); do [[ -s "$tmp/port" ]] && break; sleep .02; done
[[ -s "$tmp/port" ]]
port=$(cat "$tmp/port")
cd "$root"
go build -o "$tmp/fn" ./cmd/fn
output=$(PATH="$tmp/venv/bin:$PATH" FN_PROVIDER=openai-completions FN_API_KEY=test FN_MODEL=e2e FN_BASE_URL="http://127.0.0.1:$port/v1" FN_MCP_CONFIG="$tmp/mcp.json" HOME="$tmp/home" "$tmp/fn" -p "Exercise the configured MCP server end to end")
expected='MCP E2E passed: discovery, persistent stdio calls, and model round-trip.'
[[ "$output" == "$expected" ]] || { printf 'unexpected output: %s\n' "$output" >&2; exit 1; }
printf '%s\n' "$output"
