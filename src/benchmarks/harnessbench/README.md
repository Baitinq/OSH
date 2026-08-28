# fn agent, Pi, and Codex: HarnessBench

This is a lightweight, repeated comparison of fn agent, Pi, and Codex using
the upstream
[HarnessBench](https://github.com/Qihoo360/harness-bench) task fixtures and
programmatic oracles. It does not use Harbor or rebuild benchmark scoring.

## Published results

The checked-in [60-task, two-repetition results](results.html) contain 360 agent
runs using GPT-5.6 Sol with medium reasoning. The accompanying
[JSON artifact](results-60x2-20260827.json) includes every selected task score,
duration, token count, aggregate statistic, and paired confidence interval. Pi
token counts come from the original runs; fn and Codex counts come from
score-equivalent reruns after comparable token reporting was enabled. All reported
counts are provider total tokens—input plus output—including cached input.

Tasks 078 and 081 required a public tunnel that was unavailable during the run, so
they were replaced with tasks 087 and 089. Task 088 was also attempted as a
replacement but required the same tunnel and was excluded. Each reported
repetition contains the same 60 successfully scored tasks.

The default suite contains tasks 027–056: 30 deterministic office, retrieval,
software-engineering, and data-analysis workspaces. By default, each task runs
once through fn and Pi, for 60 agent runs total. Their order alternates by task
to reduce systematic time-of-run bias. Codex can be run separately on the same
tasks.

## Controlled variables

All harnesses receive the same task prompt, fresh fixture workspace, model,
reasoning level, and 15-minute timeout. The defaults are:

- Model: `openai/gpt-5.6-sol`
- Reasoning: `medium`
- Pi provider: native `openai`
- Outcome: upstream HarnessBench `oracle_result.outcome_score`

Optional HarnessBench LLM process and quality graders are disabled. This keeps
scoring deterministic and avoids judging one agent with another model. A task is
a headline pass only when its oracle outcome score is 1. Partial outcome scores
are retained and included in the mean.

This compares the installed Pi and Codex configurations with the current fn
agent checkout. Each harness uses its normal non-interactive mode with installed
resources enabled.
Pi receives the task prompt on stdin so long prompts are not passed as
command-line arguments.

## Requirements

- macOS with Python, Go, Node.js, and `uv`
- fn agent's configured Responses API available through `FN_BASE_URL`
- Pi configured for the native OpenAI provider
- API credentials for the configured harnesses

No task Docker images are needed. Fixture workspaces and results are small.

## Set up

From `src/benchmarks/harnessbench`:

```sh
make setup
```

Setup pins upstream HarnessBench to commit `1025086a`, installs it in a Python
3.12 virtual environment, and builds the current native fn agent binary. The upstream
checkout and build products stay under the ignored `.cache/` directory.

## Validate the default adapters

Run one task through fn and Pi before starting the default 60-run comparison:

```sh
make smoke LABEL=adapter-smoke
```

## Quick benchmark

For routine iteration, use a systematic 15-task sample: every other task from
027 through 055. It spans office, retrieval, software-engineering, and data
workspaces instead of drawing conclusions from only a few coding tasks.

```sh
make quick LABEL=current
```

It takes about 14 minutes with fn agent on this Mac. Override `HARNESS=codex` for the same slice with Codex. Use `HARNESS=pi` to run the same slice with Pi. Fifteen tasks are still a
screening benchmark; use the 30-task suite and repeated runs before making a
high-confidence decision.

## Run the default 30-task comparison

```sh
make run LABEL=fn-vs-pi
```

Run only fn agent when establishing a standalone baseline:

```sh
.venv/bin/python benchmark.py run --label fn-baseline --harness fn
```

The default fn–Pi comparison can take one to several hours and consumes 60 model
sessions. Run Codex separately with `--harness codex`. It is deliberately
sequential so local proxy load and task ordering stay predictable.

Choose a smaller or different contiguous range when iterating:

```sh
.venv/bin/python benchmark.py run --label code-only --first 39 --last 48
```

Override controlled settings explicitly when needed:

```sh
.venv/bin/python benchmark.py run --label low-reasoning \
  --model openai/gpt-5.6-sol --reasoning-effort low \
  --pi-provider openai --timeout 600
```

## Results

Each run is isolated under `results/<label>/`:

- `metadata.json`: model, harness versions, binary hash, task IDs, and host
- `records.jsonl`: paired outcome, adapter status, latency, and errors
- `summary.json`: pass rates, mean outcome score, and paired results
- `raw/`: untouched upstream HarnessBench result JSON
- `work/`: retained task workspaces for diagnosis
- `logs/`: one runner log per harness and task

Reprint a summary with:

```sh
.venv/bin/python benchmark.py report results/fn-vs-pi
```

## Interpretation

This is a diagnostic harness comparison, not a replacement for SWE-bench. It has
more samples and far lower setup cost, but many tasks create structured artifacts
rather than patches in large repositories. Use paired disagreements to inspect
where one harness behaves better, and keep the two-task SWE-bench smoke result as
a separate real-repository check.
