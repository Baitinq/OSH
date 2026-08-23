# osh

A terminal-based OpenAI agent with shell access.

## Configuration

The endpoint and model are intentionally configured as constants near the top of `agent.go`:

```go
const baseURL = "https://api.openai.com/v1/"
const modelName = "gpt-5.6-sol"
```

Edit these values to use another provider. `OPENAI_API_KEY` remains an environment variable so credentials are not committed to source control. For local servers that ignore authentication, use any non-empty value.

OpenAI:

```sh
export OPENAI_API_KEY='...'
./osh
```

For an OpenAI-compatible server, first update the constants, for example:

```go
const baseURL = "http://localhost:11434/v1/"
const modelName = "your-model-name"
```

Then run:

```sh
export OPENAI_API_KEY='not-used'
./osh
```

The configured server must implement the OpenAI **Responses API** (`POST /responses`), including function tool calls. Compatibility limited to the Chat Completions API is not sufficient for this application.

## Controls

- `Enter` — send a message; while responding, steer after the active turn finishes
- `Shift+Enter` — queue a message behind the active response
- `Escape` — cancel the active response
- `Mouse wheel` / `Page Up` / `Page Down` — scroll conversation history
- `Ctrl/Alt+←/→` or `Alt+B/F` — move through input word by word
- `Ctrl+W`, `Alt+D`, `Ctrl+U`, `Ctrl+K` — delete words or text around the cursor
- `Ctrl+C` — quit
