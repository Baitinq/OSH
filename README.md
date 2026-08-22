# OSH

**Overly Simple Harness** — a tiny, minimal agent harness for the terminal.

```text
OSH
──────────────────────────────────────────────




╭────────────────────────────────────────────╮
│ Type a message…                            │
╰────────────────────────────────────────────╯
model gpt-5.6  ·  context 0 tokens
```

OSH is a small foundation for terminal agents: accept messages, preserve the conversation, and connect model or tool logic without hiding the control loop.

## Run

OSH requires an OpenAI API key:

```sh
cd src
OPENAI_API_KEY=your-api-key go run .
```

## Test

```sh
cd src
go test ./...
```
