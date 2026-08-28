# Harbor

This adapter runs fn against datasets supported by [Harbor](https://harborframework.com/).
Harbor prepares each task environment, invokes `fn -p` in the task workspace, and runs
the dataset verifier.

## Run

Install Harbor:

```sh
uv tool install harbor
```

From `src`, run one Terminal-Bench task:

```sh
export OPENAI_API_KEY=...
PYTHONPATH="$PWD" harbor run \
  --dataset terminal-bench@2.0 \
  --agent benchmarks.harbor.agent:FnAgent \
  --agent-kwarg version="$(git rev-parse HEAD)" \
  --model openai/gpt-5.6-sol \
  --n-tasks 1 \
  --agent-setup-timeout-multiplier 3
```

Scale the run with `--n-concurrent`:

```sh
PYTHONPATH="$PWD" harbor run \
  --dataset terminal-bench@2.0 \
  --agent benchmarks.harbor.agent:FnAgent \
  --agent-kwarg version="$(git rev-parse HEAD)" \
  --agent-kwarg reasoning_effort=medium \
  --model openai/gpt-5.6-sol \
  --n-concurrent 8 \
  --agent-setup-timeout-multiplier 3
```

The `version` argument accepts a release tag or commit available on GitHub. Pin it for
reproducible results. fn's JSONL output, token usage, and session files are
retained under the trial's `agent` logs directory and included in Harbor's aggregate token counts.

fn requires an OpenAI Responses-compatible endpoint. To use a compatible proxy, set
`OPENAI_BASE_URL` or `FN_BASE_URL` in addition to `OPENAI_API_KEY`.

## Published comparison

The root README reports a comparison on a fixed 100-task subset of Aider Polyglot
1.0. The manifest is stratified across C++, Go, Java, JavaScript, Python, and Rust.
Each harness ran each task once with `gpt-5.6-sol`; Harbor's deterministic test
verifier supplied the pass/fail result.

The runs used Harbor 0.22.0, fn `15d64ff`, Pi 0.73.1, and Codex 0.150.1. fn and
Codex used medium reasoning effort; Pi used its default thinking level. Trials that
failed during environment setup were retried and were not scored as agent failures.

See the [task manifest](aider-polyglot-100.txt) and
[machine-readable results](results-aider-polyglot-100-20260828.json), including
versions, token counts, costs, and paired outcomes.
