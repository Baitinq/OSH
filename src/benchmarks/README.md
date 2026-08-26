# Benchmarks

This directory contains reproducible evaluations for fn agent and other coding harnesses.

## HarnessBench

[HarnessBench](harnessbench/README.md) is the maintained lightweight comparison suite. It uses the deterministic task fixtures and programmatic oracles from Qihoo360 HarnessBench, pinned to commit `1025086a446653702b80cfb48babbeec35db6b2c`.

### Set up

From `src/benchmarks/harnessbench`:

```sh
make setup
```

This creates an ignored Python virtual environment and upstream checkout, installs the pinned dependencies, and builds the current fn agent source into the benchmark cache.

Run `make setup` again after changing fn agent so the benchmark uses a fresh binary.

### Run

The quick suite contains 15 tasks: every odd-numbered task from 027 through 055.

```sh
make quick LABEL=my-fn-run HARNESS=fn
make quick LABEL=my-pi-run HARNESS=pi
make quick LABEL=my-codex-run HARNESS=codex
```

Labels must be unique. A run will not overwrite an existing result directory.

Run one task to validate an adapter:

```sh
make smoke LABEL=my-smoke
```

Run the complete 30-task suite:

```sh
make run LABEL=my-full-run
```

See [the HarnessBench README](harnessbench/README.md) for controlled variables, custom task ranges, timeouts, and interpretation guidance.

### Results

Each run is written to the ignored directory `harnessbench/results/<label>/`. Important files are:

- `summary.json`: aggregate pass count, mean outcome, failures, and runtime
- `records.jsonl`: one task-level record per harness run
- `metadata.json`: model, versions, source revision, and binary hash
- `logs/`: adapter logs
- `work/`: retained task workspaces

Reprint a run summary with:

```sh
.venv/bin/python benchmark.py report results/<label>
```

Open [harnessbench/results.html](harnessbench/results.html) in a browser for the current comparison report.

Generated environments, upstream checkouts, workspaces, and result data are ignored by Git.
