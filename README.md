# OSH

**Overly Simple Harness** — a small terminal-based OpenAI agent with shell access.

OSH accepts messages, preserves the conversation, and exposes a shell tool to the
model without hiding the agent control loop behind a framework.

## Requirements

- Go 1.26 or newer
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

## Configuration

The endpoint and model are intentionally configured as constants near the top of
`src/internal/agent/agent.go`:

```go
const baseURL = "https://api.openai.com/v1/"
const modelName = "gpt-5.6-sol"
```

Edit these values to use another provider. `OPENAI_API_KEY` remains an environment
variable so credentials are not committed to source control. For local servers
that ignore authentication, use any non-empty value.

For example:

```go
const baseURL = "http://localhost:11434/v1/"
const modelName = "your-model-name"
```

The configured server must implement the OpenAI **Responses API** (`POST
/responses`), including function tool calls. Compatibility limited to the Chat
Completions API is not sufficient.

## MCP

OSH keeps MCP out of its core, following the same CLI-first approach as Pi. The
agent knows it can use [MCPorter](https://mcporter.sh) through its shell tool to
discover and invoke MCP servers:

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

## Controls

- `Enter` — send a message; while responding, steer the active agent after its current tool-call batch
- `Shift+Enter` — queue a follow-up until the active agent finishes
- Reasoning summaries stream as italic gray text and remain in the transcript
- Shell calls stream live in Pi-style cards with elapsed time; cards preview the last five lines and model-visible output is capped at 2,000 lines or 50KB
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

OSH allows the model to execute shell commands with the permissions of the user
running it. Run it only in environments where that access is appropriate.
