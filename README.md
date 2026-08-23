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
go run .
```

To build a local binary:

```sh
cd src
go build -o osh .
./osh
```

## Configuration

The endpoint and model are intentionally configured as constants near the top of
`src/agent.go`:

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

## Controls

- `Enter` — send a message; while responding, steer after the active turn finishes
- `Shift+Enter` — queue a message behind the active response
- `Escape` — cancel the active response
- `Page Up` / `Page Down` — scroll conversation history
- Mouse drag — select terminal text for copying
- `Ctrl/Alt+←/→` or `Alt+B/F` — move through input word by word
- `Ctrl+W`, `Alt+D`, `Ctrl+U`, `Ctrl+K` — delete words or text around the cursor
- `Ctrl+C` — quit

Distinguishing `Shift+Enter` requires terminal keyboard-enhancement support. On
legacy terminals that report it as plain Enter, it behaves as a steer.

## Test

```sh
cd src
go test ./...
go test -race ./...
go vet ./...
```

## Safety

OSH allows the model to execute shell commands with the permissions of the user
running it. Run it only in environments where that access is appropriate.
