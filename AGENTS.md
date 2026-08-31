# fn agent

Priorities: simplicity > speed > features.

- Make the smallest change that solves the task.
- Prefer direct, readable Go.
- Keep the agent loop explicit and the UI responsive.
- Preserve the Python REPL process and in-memory state across execution failures and cancellation.
- Drain every emitted host call before an execution returns; never leave a protocol response for the next execution.
- Do not add functionality unless the task requires it.
- Do not preserve backwards compatibility.
- Run `gofmt` and `go test ./...` from `src`; also run `./testdata/tmux-e2e.sh` for terminal behavior changes.
- Commit directly to `master`; do not create feature branches.
