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

## Controls

- `Enter` sends a message while idle. While the agent is working, it steers by interrupting the current response and injecting the message into the ongoing conversation.
- `Shift+Enter` queues a message while the agent is working. Queued messages run in FIFO order after the current response finishes.
- `Esc` cancels the current response.
- `Ctrl+C` exits.

Distinguishing `Shift+Enter` requires terminal keyboard-enhancement support. On legacy terminals that report it as plain Enter, it behaves as a steer.

## Test

```sh
cd src
go test ./...
```
