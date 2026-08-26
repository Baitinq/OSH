#!/usr/bin/env python3
import argparse
import hashlib
import json
import os
import platform
import re
import subprocess
import sys
import time
from pathlib import Path

ROOT = Path(__file__).resolve().parent
SOURCE_ROOT = ROOT.parents[1]
UPSTREAM = ROOT / ".cache" / "upstream"
FN_BINARY = ROOT / ".cache" / "fn"
VENV_PYTHON = ROOT / ".venv" / "bin" / "python"
RESULTS = ROOT / "results"
UPSTREAM_REVISION = "1025086a446653702b80cfb48babbeec35db6b2c"
DEFAULT_MODEL = "openai/gpt-5.6-sol"


def command(args, *, cwd=None, capture=False, check=True, timeout=None, env=None, stdout=None):
    return subprocess.run(
        [str(value) for value in args],
        cwd=cwd,
        text=True,
        stdout=subprocess.PIPE if capture else stdout,
        stderr=subprocess.PIPE if capture else (subprocess.STDOUT if stdout else None),
        check=check,
        timeout=timeout,
        env=env,
    )


def setup():
    (ROOT / ".cache").mkdir(exist_ok=True)
    if not (UPSTREAM / ".git").is_dir():
        command(["git", "clone", "--filter=blob:none", "https://github.com/Qihoo360/harness-bench.git", UPSTREAM])
    command(["git", "-C", UPSTREAM, "fetch", "--depth", "1", "origin", UPSTREAM_REVISION])
    command(["git", "-C", UPSTREAM, "checkout", "--detach", UPSTREAM_REVISION])
    command(["uv", "venv", "--python", "3.12", ROOT / ".venv"])
    command(["uv", "pip", "install", "--python", VENV_PYTHON, "-r", ROOT / "requirements.lock"])
    command(["uv", "pip", "install", "--python", VENV_PYTHON, "--no-deps", "-e", UPSTREAM])
    command(
        ["go", "build", "-o", FN_BINARY, "./cmd/fn"],
        cwd=SOURCE_ROOT,
        env={**os.environ, "CGO_ENABLED": "0"},
    )
    print(f"HarnessBench {UPSTREAM_REVISION[:8]} and fn agent are ready.")


def task_ids(first, last, step=1):
    result = []
    for number in range(first, last + 1, step):
        matches = list((UPSTREAM / "tasks").glob(f"{number:03d}-*"))
        if len(matches) != 1:
            raise SystemExit(f"expected one upstream task numbered {number}, found {len(matches)}")
        result.append(matches[0].name)
    return result


def runtime_files(run_dir, args):
    raw = run_dir / "raw"
    work = run_dir / "work"
    app = {
        "data_dir": str(run_dir / "data"),
        "tasks_dir": str(UPSTREAM / "tasks"),
        "results_dir": str(raw),
        "work_root": str(work),
        "default_timeout_sec": args.timeout,
        "default_rounds": 1,
    }
    harnesses = {
        "models": {
            "fn": {
                "adapter": "generic_cli",
                "command": "sh",
                "args": [str(ROOT / "scripts" / "run-fn.sh"), "{prompt_file}"],
                "model": args.model,
                "timeout_sec": args.timeout,
                "session_prefix": "harnessbench-fn",
            },
            "pi": {
                "adapter": "generic_cli",
                "command": "sh",
                "args": [str(ROOT / "scripts" / "run-pi.sh"), "{prompt_file}"],
                "model": args.model,
                "timeout_sec": args.timeout,
                "session_prefix": "harnessbench-pi",
            },
            "codex": {
                "adapter": "generic_cli",
                "command": "sh",
                "args": [str(ROOT / "scripts" / "run-codex.sh"), "{prompt_file}"],
                "model": args.model,
                "timeout_sec": args.timeout,
                "session_prefix": "harnessbench-codex",
            },
        }
    }
    app_path, harness_path = run_dir / "app.json", run_dir / "harnesses.json"
    app_path.write_text(json.dumps(app, indent=2) + "\n")
    harness_path.write_text(json.dumps(harnesses, indent=2) + "\n")
    return app_path, harness_path


def version(args):
    result = command(args, capture=True, check=False)
    return (result.stdout or result.stderr).strip().splitlines()[0] if result.stdout or result.stderr else "unknown"


def metadata(args, ids):
    git = command(["git", "rev-parse", "HEAD"], cwd=SOURCE_ROOT, capture=True, check=False)
    dirty = command(["git", "status", "--short"], cwd=SOURCE_ROOT, capture=True, check=False)
    return {
        "label": args.label,
        "created_at": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
        "task_ids": ids,
        "harnesses": [args.harness] if args.harness != "both" else ["fn", "pi"],
        "model": args.model,
        "reasoning_effort": args.reasoning_effort,
        "pi_provider": args.pi_provider,
        "timeout_sec": args.timeout,
        "harnessbench_revision": UPSTREAM_REVISION,
        "fn_binary_sha256": hashlib.sha256(FN_BINARY.read_bytes()).hexdigest(),
        "repository_git_commit": git.stdout.strip() if git.returncode == 0 else None,
        "repository_git_dirty": bool(dirty.stdout.strip()),
        "pi_version": version(["pi", "--version"]),
        "codex_version": version(["codex", "--version"]),
        "host": {"system": platform.system(), "machine": platform.machine()},
    }


def result_file(run_dir, harness, task_id):
    matches = list((run_dir / "raw" / harness).glob(f"*/{task_id}.json"))
    return max(matches, key=lambda path: path.stat().st_mtime) if matches else None


def run_one(run_dir, app_path, harness_path, harness, task_id, args):
    log_dir = run_dir / "logs" / harness
    log_dir.mkdir(parents=True, exist_ok=True)
    env = {
        **os.environ,
        "HARNESSBENCH_APP_CONFIG": str(app_path),
        "HARNESSBENCH_HARNESS_CONFIG": str(harness_path),
        "HARNESSBENCH_SKIP_PROCESS_GRADE": "1",
        "HARNESSBENCH_SKIP_ORACLE_QUALITY_LLM": "1",
        "FN_BENCH_BINARY": str(FN_BINARY),
        "FN_MODEL": args.model,
        "FN_REASONING_EFFORT": args.reasoning_effort,
        "PI_BENCH_PROVIDER": args.pi_provider,
        "PI_BENCH_MODEL": args.model,
        "PI_BENCH_THINKING": args.reasoning_effort,
        "CODEX_BENCH_MODEL": args.model.rsplit("/", 1)[-1],
        "CODEX_BENCH_REASONING": args.reasoning_effort,
    }
    started = time.time()
    with (log_dir / f"{task_id}.log").open("w") as log:
        completed = command(
            [VENV_PYTHON, "-m", "harnessbench.cli", "run-task", "--task", task_id, "--harness", harness, "--mode", "live"],
            cwd=UPSTREAM,
            env=env,
            stdout=log,
            check=False,
        )
    path = result_file(run_dir, harness, task_id)
    if path:
        payload = json.loads(path.read_text())
        oracle = payload.get("oracle_result") or {}
        score = oracle.get("outcome_score")
        adapter_ok = bool((payload.get("adapter_result") or {}).get("ok"))
        error = None
    else:
        score, adapter_ok = None, False
        error = f"runner exit {completed.returncode}; see {log_dir / (task_id + '.log')}"
    return {
        "task_id": task_id,
        "harness": harness,
        "score": score,
        "passed": isinstance(score, (int, float)) and score >= 0.999999,
        "adapter_ok": adapter_ok,
        "seconds": round(time.time() - started, 1),
        "error": error,
    }


def write_summary(run_dir, records):
    by_harness = {}
    harnesses = list(dict.fromkeys(record["harness"] for record in records))
    for harness in harnesses:
        selected = [record for record in records if record["harness"] == harness]
        numeric = [record["score"] for record in selected if isinstance(record["score"], (int, float))]
        by_harness[harness] = {
            "tasks": len(selected),
            "passed": sum(record["passed"] for record in selected),
            "mean_outcome_score": round(sum(numeric) / len(numeric), 4) if numeric else None,
            "adapter_failures": sum(not record["adapter_ok"] for record in selected),
            "total_seconds": round(sum(record["seconds"] for record in selected), 1),
        }
    paired = {}
    for task_id in dict.fromkeys(record["task_id"] for record in records):
        values = {record["harness"]: record for record in records if record["task_id"] == task_id}
        if "fn" in values and "pi" in values:
            paired[task_id] = {name: values[name]["passed"] for name in ("fn", "pi")}
    summary = {"harnesses": by_harness, "paired": paired}
    (run_dir / "summary.json").write_text(json.dumps(summary, indent=2) + "\n")
    print("\nHarness  Passed  Mean score  Failures  Time")
    for harness, value in by_harness.items():
        print(f"{harness:7}  {value['passed']:2}/{value['tasks']:<2}   {str(value['mean_outcome_score']):10}  {value['adapter_failures']:8}  {value['total_seconds']:7.1f}s")
    if paired:
        fn_only = sum(value["fn"] and not value["pi"] for value in paired.values())
        pi_only = sum(value["pi"] and not value["fn"] for value in paired.values())
        print(f"Paired disagreements: fn-only={fn_only}, Pi-only={pi_only}")


def run(args):
    if not VENV_PYTHON.is_file() or not FN_BINARY.is_file() or not (UPSTREAM / ".git").is_dir():
        raise SystemExit("benchmark is not set up; run `make setup`")
    if not re.fullmatch(r"[A-Za-z0-9_.-]+", args.label):
        raise SystemExit("--label may contain only letters, numbers, dot, underscore, and dash")
    run_dir = RESULTS / args.label
    if run_dir.exists():
        raise SystemExit(f"run already exists: {run_dir}")
    run_dir.mkdir(parents=True)
    ids = task_ids(args.first, args.last, args.step)
    (run_dir / "metadata.json").write_text(json.dumps(metadata(args, ids), indent=2) + "\n")
    app_path, harness_path = runtime_files(run_dir, args)

    records = []
    records_path = run_dir / "records.jsonl"
    with records_path.open("w") as output:
        for index, task_id in enumerate(ids):
            if args.harness == "both":
                order = ("fn", "pi") if index % 2 == 0 else ("pi", "fn")
            else:
                order = (args.harness,)
            for harness in order:
                total_runs = len(ids) * (2 if args.harness == "both" else 1)
                print(f"[{len(records) + 1}/{total_runs}] {harness}: {task_id}", flush=True)
                record = run_one(run_dir, app_path, harness_path, harness, task_id, args)
                records.append(record)
                output.write(json.dumps(record) + "\n")
                output.flush()
                print(f"  score={record['score']} adapter_ok={record['adapter_ok']} time={record['seconds']}s", flush=True)
    write_summary(run_dir, records)


def report(path):
    run_dir = Path(path).resolve()
    records = [json.loads(line) for line in (run_dir / "records.jsonl").read_text().splitlines()]
    write_summary(run_dir, records)


def main():
    parser = argparse.ArgumentParser(description="Compare fn agent and Pi on HarnessBench tasks")
    subparsers = parser.add_subparsers(dest="command", required=True)
    subparsers.add_parser("setup")
    run_parser = subparsers.add_parser("run")
    run_parser.add_argument("--label", required=True)
    run_parser.add_argument("--first", type=int, default=27)
    run_parser.add_argument("--last", type=int, default=56)
    run_parser.add_argument("--step", type=int, default=1)
    run_parser.add_argument("--timeout", type=int, default=900)
    run_parser.add_argument("--harness", choices=("fn", "pi", "codex", "both"), default="both")
    run_parser.add_argument("--model", default=os.environ.get("FN_MODEL", DEFAULT_MODEL))
    run_parser.add_argument("--reasoning-effort", default=os.environ.get("FN_REASONING_EFFORT", "medium"))
    run_parser.add_argument("--pi-provider", default="openai")
    report_parser = subparsers.add_parser("report")
    report_parser.add_argument("run_dir")
    args = parser.parse_args()
    if args.command == "setup":
        setup()
    elif args.command == "run":
        run(args)
    else:
        report(args.run_dir)


if __name__ == "__main__":
    main()
