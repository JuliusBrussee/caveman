# CaveBench Wrap benchmark

## Result

On six deterministic, agent-shaped tool-output workloads, Caveman-wrapped
Claude Code used **33.2% fewer provider-reported input tokens** than direct
Claude Code: **591,673 vs 885,793 tokens** across 18 paired runs. Caveman passed
all **18/18 exact-answer checks**. The case-clustered 95% interval was
**14.6% to 48.5%**.

Claim basis: `benchmark_counterfactual`. This is controlled benchmark evidence,
not production traffic, customer spend, a provider invoice, or Caveman
`verified_savings`.

| Arm | Exact quality | Provider input on held pairs | Reduction vs direct | Case-clustered 95% interval |
|---|---:|---:|---:|---:|
| Direct Claude Code | 18/18 | 885,793 | baseline | n/a |
| Caveman wrap + skill | 18/18 | 591,673 | **33.2%** | **14.6% to 48.5%** |
| Headroom wrap | 15/18 | 703,202 vs matched direct | 6.7% | -0.7% to 17.9% |

Caveman won 15/18 pairs. Headroom's three YAML runs failed the exact-answer
gate and remain visible rather than counting toward savings at held quality.

## Per-case results

Each case ran three times per arm.

| Case | Shape | Direct input | Caveman input | Reduction | Caveman quality |
|---|---|---:|---:|---:|---:|
| `sre-log-needle` | log | 148,807 | 74,068 | 50.2% | 3/3 |
| `deployment-json-drift` | JSON | 147,975 | 108,939 | 26.4% | 3/3 |
| `fraud-csv-outlier` | CSV | 165,823 | 74,484 | 55.1% | 3/3 |
| `test-output-failure` | test output | 150,377 | 108,514 | 27.8% | 3/3 |
| `config-yaml-drift` | YAML | 132,124 | 71,027 | 46.2% | 3/3 |
| `dashboard-html-alert` | HTML | 140,687 | 154,641 | **-9.9%** | 3/3 |

Unsupported and no-op inputs stay in the aggregate. HTML regressed because no
compression transform applied while full Caveman skill overhead remained
counted.

## Method

- Six immutable MCP fixtures, each 60–95 KB: logs; deployment JSON; fraud CSV;
  test output; configuration YAML; dashboard HTML.
- Three rotated repetitions for direct Claude Code, Caveman, and Headroom: 54
  total agent runs and 18 direct/Caveman pairs.
- Claude Code `2.1.223`, model `claude-sonnet-5`.
- Every arm called the same fixture exactly once and returned a structured answer
  checked by an exact semantic JSON oracle.
- Every arm used Claude Code's `modelUsage` counters. Primary input metric:
  `input_tokens + cache_read_input_tokens + cache_creation_input_tokens`.
- Cache buckets were summed without price weighting. Wrapper-native tokenizer
  estimates did not drive the comparison.
- Recovery was available only when required answer data was absent from visible
  compressed content. Recovery calls and follow-up provider input remained
  counted.
- Full Caveman skill prompt overhead remained counted from the first request.
- Aggregate reduction compares summed Caveman input with summed paired direct
  input. The interval uses a deterministic 10,000-resample percentile bootstrap
  clustered by case.

## Verification and provenance

- Generated: `2026-08-06T14:31:43Z`
- Publication gate: passed
- Same provider-usage source: 54/54 runs
- Fixture called exactly once: 54/54 runs
- Caveman skill installed, loaded, and applied: 18/18 runs
- Positive proxy compression observed: 15/18 Caveman runs; no-op runs remained
  included
- Permission denials: 0
- Corpus SHA-256:
  `9a400a6dc38591dc3ce59bc2e3fa6fc59d99e211dc9185b430979de78991760a`
- Caveman skill SHA-256:
  `5e30bb56afbd0b01bd736f2da84180e76f18db4a64de8e124525d5c8dc2e8605`
- Harness source SHA-256:
  `e6322a3a55cfdfa6c7942022a1be9adb49fa30c380352c819c99bbd4bff30cc4`
- Fixture MCP binary SHA-256:
  `5c71768780582708c00fa1d6862a6a5b93fc33fc23655ff430b0edb8fee1e790`
- Claude binary SHA-256:
  `4163c57c719e27680336f323ebdcd2ba8aa48a683fdfa427240ec4b506a21e45`
- Harness Git commit: `630e157246b68b63559fb8baab29b87042db996b`
- Dirty worktree at execution: `false`

## Reproduction availability

This repository contains published report and provenance hashes, but not raw
harness or run artifacts for this result. It cannot be independently reproduced
from this checkout. Treat result as pinned report, not reproducible public
benchmark, until harness and raw artifacts are published here.

Publication requires at least six cases and three repetitions; exact direct and
Caveman quality on every run; one fixture call per run; the same provider usage
source; active Caveman skill; at least one proven compression; positive aggregate
reduction; and a 95% interval entirely above zero. Negative and no-op cases may
not be removed.

## Boundaries

- Workloads are deterministic large tool outputs, not open-ended coding tasks or
  customer traffic.
- Result applies to this pinned suite and runtime. It is not a universal savings
  promise.
- Local host isolation reset a dedicated Claude configuration before every arm.
  This was not a sealed container, and no exact filesystem or egress isolation is
  claimed.
- Cost, latency, and output-token deltas are secondary because agent trajectories
  can differ. Primary claim is paired provider input tokens at held quality.
