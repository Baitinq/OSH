#!/bin/sh
set -eu
exec codex exec \
  --json \
  --model "${CODEX_BENCH_MODEL:-gpt-5.6-sol}" \
  -c "model_reasoning_effort=\"${CODEX_BENCH_REASONING:-medium}\"" \
  --sandbox workspace-write \
  --skip-git-repo-check \
  --ephemeral \
  --ignore-user-config \
  --ignore-rules - < "$1"
