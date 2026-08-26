# OSH

**Overly Simple Harness** — a small terminal-based OpenAI agent with a persistent Python control environment.

OSH accepts messages, preserves the conversation, and exposes one model-facing `repl`
tool without hiding the agent control loop behind a framework. Python variables,
imports, and tool results persist across calls, so the model can keep large working
state outside its context and inspect only what it needs.

## Requirements

- Go 1.26 or newer
- Python 3.10 or newer (`python3`)
- An OpenAI API key, or an OpenAI-compatible server implementing the Responses API

## Run

```sh
cd src
export OPENAI_API_KEY='...'
go run ./cmd/osh
```

To build a local binary:

```sh
cd src
go build -o osh ./cmd/osh
./osh
```

For non-interactive use, print only the final response to stdout. Piped input is appended to the prompt:

```sh
osh -p "summarize the changes in this repository"
git diff | osh -p "review this diff"
cat error.log | osh --print "find the root cause"
```

## Configuration

`OPENAI_API_KEY` must be set. OSH also supports these optional environment
overrides:

| Variable | Default |
| --- | --- |
| `OSH_BASE_URL` | `https://api.openai.com/v1/` |
| `OSH_MODEL` | `gpt-5.6-sol` |
| `OSH_REASONING_EFFORT` | `medium` |

When a model rejects a request because its context limit was reached, OSH summarizes older context, preserves approximately 20,000 recent tokens verbatim, and retries the request once.

For local servers that ignore authentication, use any non-empty API key. The
configured server must implement the OpenAI **Responses API** (`POST /responses`),
including function tool calls. Compatibility limited to the Chat Completions API
is not sufficient.

## Persistent REPL

The model works through a persistent Python REPL with two preloaded host functions:

```python
status = shell("git status --short")
status.stdout

hits = web_search("latest Go release")
[(hit.title, hit.url) for hit in hits]
```

`shell()` returns a `ShellResult` with `stdout`, `exit_code`, and `error` fields.
`web_search()` returns `SearchResult` values with `title`, `url`, and `snippet` fields.
Assignments stay in the REPL; only printed output and the final expression are returned
to the model. The Python environment performs command execution and web requests directly.

## MCP

OSH keeps MCP out of its core, following the same CLI-first approach as Pi. The
agent knows it can use [MCPorter](https://mcporter.sh) through `shell()` to discover
and invoke MCP servers:

```sh
npx -y mcporter@latest list
npx -y mcporter@latest call <server>.<tool> key=value
```

MCPorter discovers its own project and user configuration, including supported
configurations imported from other clients. OSH does not preload MCP schemas;
the agent discovers relevant tools only when a task needs them.

`npx -y mcporter@latest` requires Node.js and may download the package on first use. For
frequent use, install MCPorter so the package is cached and immediately
available; MCP configuration and credentials remain managed by MCPorter rather
than OSH.

## Web search

OSH includes a keyless `web_search()` function backed by DuckDuckGo. It returns ranked
result titles, URLs, and snippets so the agent can research current information;
full pages can still be inspected with `shell("curl ...")`.

## Controls

- `Enter` — send a message; while responding, steer the active agent after its current tool-call batch
- `Shift+Enter` — queue a follow-up until the active agent finishes
- Reasoning summaries stream as italic gray text and remain in the transcript
- REPL calls appear as Python code cells; model-visible output is capped at 2,000 lines or 50KB
- `Ctrl+J` — insert a newline in the input editor
- `↑` / `↓` — navigate previously submitted messages and return to the current draft
- `Ctrl+C` — cancel the active response; press twice within one second to quit
- `Escape` — clear the input editor
- Primary-screen rendering keeps terminal scrollback, selection, search, and copying native
- `Ctrl/Alt+←/→` or `Alt+B/F` — move through input word by word
- `Ctrl+W`, `Alt+D`, `Ctrl+U`, `Ctrl+K` — delete words or text around the cursor

Distinguishing `Shift+Enter` requires terminal keyboard-enhancement support. On
legacy terminals that report it as plain Enter, it behaves as a steer.

OSH uses a Pi-style main-screen renderer: the transcript and live controls form one
logical document, appended lines naturally enter terminal history, and only changed
lines still in the visible viewport are rewritten. Terminal resize or a required edit
above that viewport triggers a full transcript replay.

## Test

```sh
cd src
go test ./...
go test -race ./...
go vet ./...
./testdata/tmux-e2e.sh
```

## Safety

OSH allows the model to execute Python and shell commands with the permissions of the
user running it. The REPL process is not a sandbox. Run OSH only in environments where
that access is appropriate.
