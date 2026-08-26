# fn agent

Priorities: simplicity > speed > features.

- Make the smallest change that solves the task.
- Prefer direct, readable Go.
- Keep the agent loop explicit and the UI responsive.
- Do not add functionality unless the task requires it.
- Run `gofmt` and `go test ./...` from `src`; also run `./testdata/tmux-e2e.sh` for terminal behavior changes.
- Commit directly to `master`; do not create feature branches.
