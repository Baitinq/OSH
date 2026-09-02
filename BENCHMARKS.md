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

The custom HarnessBench runner has since been removed in favor of using Harbor
for benchmark execution and result collection.

## SWE Atlas Codebase Q&A

[SWE Atlas](https://scale.com/research/swe_atlas) evaluates questions that
require navigating and understanding real repositories. This comparison used a
fixed 40-task subset: eight tasks from each of Kitty, SimpleLogin, Scapy, Maddy,
and TruffleHog. Every harness received the same task and repository in an
isolated Harbor environment.

- Model: `openai/gpt-5.6-sol`
- Reasoning effort: medium
- Harbor: 0.23.0
- Harnesses: fn `7c3886b` plus the Python 3.8 compatibility fix, Pi 0.84.2,
  Codex 0.144.1
- Score: upstream rubric grader using `gpt-4.1-mini`
- Repetitions: one final scored attempt per task and harness

### Results

| Harness | Passed | Pass rate | Input tokens | Output tokens | Total tokens |
| --- | ---: | ---: | ---: | ---: | ---: |
| fn agent | **15/40** | **37.5%** | 24.97M | 0.27M | 25.24M |
| Pi | 14/40 | 35.0% | **21.23M** | **0.26M** | **21.49M** |
| Codex | 12/40 | 30.0% | 81.73M | 0.39M | 82.12M |

The differences were not statistically significant. Paired outcomes had three
fn-only and two Pi-only passes (exact McNemar p=1.00), and eight fn-only and five
Codex-only passes (p=0.58). At this sample size, the result supports approximate
parity rather than a ranking.

The benchmark was run in two balanced 20-task batches. Infrastructure failures
in the first batch were rerun after fixing fn's support for repository images
with Python 3.8 or 3.9; the final attempt for each task is reported. Timeouts,
policy refusals, and agent exits in the final runs count as failures. SWE Atlas
uses an LLM rubric grader, so its scores are less deterministic than test-based
coding benchmarks.

## ContextBench issue resolution

[ContextBench](https://github.com/EuniAI/ContextBench) provides repository-level
issues with human-annotated relevant context. This comparison sampled 20 tasks
from its verified 500-task selection: ten Django and ten scikit-learn issues.
The harnesses solved the issues in Harbor, and the corresponding SWE-bench test
verifiers scored the resulting patches.

- Model: `openai/gpt-5.6-sol`
- Reasoning effort: medium
- Harnesses: the same versions as the SWE Atlas comparison
- Timeout: 25 minutes per agent attempt
- Repetitions: one per task and harness
- Artifact: [fn result record](benchmark-results/contextbench-fn-20-20260901.json)

### Results

| Harness | Passed | Pass rate | Input tokens | Output tokens | Total tokens |
| --- | ---: | ---: | ---: | ---: | ---: |
| fn agent | **18/20** | **90%** | 4.01M | 0.05M | 4.06M |
| Pi | 17/20 | 85% | **3.87M** | 0.06M | **3.93M** |
| Codex | 17/20 | 85% | 7.64M | **0.05M** | 7.69M |

All harnesses completed all 20 tasks without infrastructure exceptions. The
checked-in fn result records 18/20 completed on September 1 with zero harness
errors. Paired outcomes do not show a significant difference: fn had two unique
passes versus one for Pi, and one unique pass versus none for Codex (exact McNemar
p=1.00 for both comparisons).

This run measures end-to-end issue resolution on tasks selected by ContextBench;
it does **not** report ContextBench's official file, symbol, or span retrieval
metrics. Twenty tasks and one attempt per task are enough for a smoke comparison,
not a stable estimate of general issue-resolution ability.

Token totals are provider-reported input plus output tokens; cached input remains
part of the input total. Harbor adapters differ in how they expose cache usage,
so only aggregate input and output are compared. fn used 69% fewer total tokens
than Codex on SWE Atlas and 47% fewer on ContextBench, while Pi used 15% and 12%
fewer than fn, respectively.

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
