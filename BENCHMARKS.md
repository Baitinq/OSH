# Benchmarks

Benchmarks are evidence about a particular model, task set, and harness version;
they are not universal agent rankings. The checked-in artifacts make the results
inspectable and keep the benchmark setups reproducible.

## HarnessBench

[HarnessBench](https://github.com/Qihoo360/harness-bench) evaluates office,
retrieval, software-engineering, and data-analysis tasks with programmatic
oracles. This comparison used 60 successfully scored tasks, two repetitions per
harness, and 360 agent runs in total.

All harnesses received the same task prompt, fresh fixture workspace, model,
reasoning level, and 15-minute timeout:

- Model: `openai/gpt-5.6-sol`
- Reasoning effort: medium
- Score: upstream `oracle_result.outcome_score`
- Repetitions: two per task and harness

Optional LLM process and quality graders were disabled, leaving deterministic
oracle scoring. Partial outcome scores are included in the mean; a headline pass
requires an outcome score of 1.

### Results

| Harness | Combined mean | Mean time per 60 tasks |
| --- | ---: | ---: |
| fn agent | 0.8702 | **45m 39s** |
| Pi | 0.8730 | 62m 01s |
| Codex | 0.8494 | 76m 45s |

The paired fn–Pi difference was −0.0028 (95% CI: −0.0188 to 0.0132), providing
no evidence of a meaningful difference. The paired fn–Codex difference was
0.0208 (95% CI: −0.0053 to 0.0469), whose interval also crossed zero. fn was the
fastest harness and had the smallest difference between its two repetition
means. It processed 55% fewer total tokens than Pi and 74% fewer than Codex.

HarnessBench largely measures structured-artifact work and should not be read as
a comprehensive coding-agent ranking. Pi token counts came from the original
runs. fn and Codex counts came from score-equivalent reruns after comparable
token reporting was enabled.

- [Methodology and reproduction instructions](src/benchmarks/harnessbench/README.md)
- [Detailed report](src/benchmarks/harnessbench/results.html)
- [Machine-readable results](src/benchmarks/harnessbench/results-60x2-20260827.json)

## Aider Polyglot

A separate comparison used a fixed, language-stratified 100-task subset of
[Aider Polyglot](https://github.com/Aider-AI/polyglot-benchmark). It covered
C++, Go, Java, JavaScript, Python, and Rust. Every harness ran the same tasks once
with `gpt-5.6-sol` in isolated [Harbor](https://harborframework.com/)
environments, and Harbor's deterministic test verifier supplied the pass/fail
result.

The runs used Harbor 0.22.0, fn `15d64ff`, Pi 0.73.1, and Codex 0.150.1. fn and
Codex used medium reasoning effort; Pi used its default thinking level. Trials
that failed during environment setup were retried rather than scored as agent
failures.

### Results

| Harness | Passed | Total tokens | Cost | Cost / pass |
| --- | ---: | ---: | ---: | ---: |
| fn agent | **77/100** | 3.68M | $6.13 | $0.08 |
| Codex | 74/100 | 14.80M | $21.08 | $0.28 |
| Pi | 67/100 | **2.39M** | **$4.71** | **$0.07** |

fn and Codex were statistically tied on paired outcomes: five tasks passed only
by fn and two only by Codex (two-sided exact McNemar p=0.45). Against Pi, fn had
11 fn-only passes and one Pi-only pass (p=0.006).

This comparison has only one run per harness and task. It measures performance
on this fixed subset with this model and these harness versions, not general
coding ability.

- [Harbor adapter and methodology](src/benchmarks/harbor/README.md)
- [Task manifest](src/benchmarks/harbor/aider-polyglot-100.txt)
- [Machine-readable results](src/benchmarks/harbor/results-aider-polyglot-100-20260828.json)
