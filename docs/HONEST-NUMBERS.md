# Honest Numbers

Caveman save tokens sometimes. Caveman cost tokens sometimes. This page say which is which, with the real numbers. No marketing. If caveman lose for your workload, this page tell you to turn it off.

## What the response skill does

The Caveman response skill makes model write shorter output. Skill itself
does not compress input, context, files, or model thinking tokens. Caveman local
Engine and proxy are separate components that can compress recoverable input;
see [Product model](technical/product-model.md).

## The measured numbers

| What | Number | How measured | Source |
|---|---|---|---|
| Output reduction vs a plain `Answer concisely.` control | caveman 3%, ultracave 35%, megacave 9% at the median (n=10, single run, output length only, tiktoken o200k approximation; measured on the 3.1.0 skill text, not re-run since the 3.2.0 rule changes) | `evals/llm_run.py` on claude-opus-5-5 with host settings isolated; `evals/measure.py` | [`evals/snapshots/results.json`](../evals/snapshots/results.json) |
| Output reduction vs default verbose replies (no benchmark harness run) | Not published | `benchmarks/` harness exists, but the repository has no committed reviewed raw result | [`benchmarks/`](../benchmarks/) |
| Input reduction from the skill | 0% | It's an output-style instruction | Not applicable |
| Input cost the skill *adds* | Not measured yet | `evals/llm_run.py` now stores the usage Claude Code reports for every call, and `evals/measure.py` reports the median input tokens the skill adds against the terse control. The committed snapshot predates usage capture. File size is not a billed token count, and real agents differ in when they inject rules and how they cache | [`evals/README.md`](../evals/README.md) |
| `/caveman-compress` on memory files | 33.2% total input reduction across five listed fixtures (22.8–49.1% per file, tiktoken o200k) | `skills/caveman-compress/scripts/benchmark.py` over `tests/caveman-compress/`, plus structural checks; no general quality-equivalence claim | [caveman-compress fixtures](../skills/caveman-compress/README.md#benchmarks) |

Token-count runs measure output length only. They do not prove semantic or technical equivalence. Publish a reduction only with committed raw pairs and separate quality review. A fidelity eval now exists (`evals/score_fidelity.py`: fixed cases, regex checks for kept numbers, negations, language and safety wording, scored for every arm), but no reviewed fidelity snapshot is committed, so no quality claim is published. The full eval harness and its correction history are documented in [`evals/README.md`](../evals/README.md).

## When caveman wins

- Long chatty outputs give terse style more removable prose. Measure your own A/B; no aggregate reduction is currently published.
- Longer sessions can accumulate output reduction; measure the rules' input cost and cache behavior in the same comparison.
- Shorter replies can finish sooner and take less time to read.

## When caveman loses (net-negative)

The rules can add input tokens. Whether shorter output offsets that cost
depends on the agent, cache behavior, workload, and billing model. Comparing
raw input and output token counts alone does not establish a monetary net result.

- Terse coding Q&A ([#145](https://github.com/JuliusBrussee/caveman/issues/145)): fixed prompt overhead can exceed any output reduction. User in #145 measured a net loss.
- Agents billed by request or credit ([#506](https://github.com/JuliusBrussee/caveman/issues/506)): GitHub Copilot charges premium *requests*. A shorter answer is same request, so Caveman cannot lower Copilot credit use. Same applies to other per-message pricing.
- Session totals can differ sharply from output-only changes because prompts, context, files, and injected rules consume tokens. Provider-billed A/B totals outrank output-only estimates.
- Tool-side counters can go wrong direction ([#550](https://github.com/JuliusBrussee/caveman/issues/550)). One Cursor A/B showed 4.3M tokens with caveman versus 1M without and twice wall-clock time. Exact run was not reproducible, so only safe conclusion is that rule re-injection, retries, and cache or context accounting can overwhelm output savings. Turn Caveman off if your A/B is net-negative.

## Outside A/Bs

Other people's runs, not ours. Each names the skill version it tested. Caveman 3 rewrote the rules after these runs, so treat them as evidence about the approach, not about today's exact file.

- **LemonCrow** ([#727](https://github.com/JuliusBrussee/caveman/issues/727)): 20 engineering prompts × 5 reps × 3 runs (300 runs per arm), `claude-opus-4-8`, July 2026 `skills/caveman/SKILL.md` as the only extra system instruction. Billed output −44.1%. Total cost +0.06%. Initial cached context +1,532 tokens per run. [Method](https://github.com/lemoncrow-lab/lemoncrow/blob/main/BENCHMARKS.md#telegraphic-qa-benchmark) · [raw results](https://github.com/lemoncrow-lab/lemoncrow/tree/main/benchmarks/codebench/results/telegraphic_2026_07_17)
- **edubraqd** ([#733 comment, 2026-09-14](https://github.com/JuliusBrussee/caveman/issues/733#issuecomment-5657222190)): 10 dev questions in pt-BR, `haiku-4-5`, 20 replies per arm, pre-3.0 SKILL.md at the `full` level. On top of a CLAUDE.md that already asks for terse replies, caveman cut output 8% (median −5%, stdev 25%). Separately, across 662 Claude Code sessions, output was 0.3% of all tokens and caveman's net effect on the bill was within ±0.2%.
- **JetBrains** ([blog, July 2026](https://blog.jetbrains.com/ai/2026/07/speak-to-ai-agents-like-cavemen-tosave-tokens/)): 86 real coding tasks, paired A/B. 8.5% fewer output tokens, no measurable quality loss (p = 0.82).

Common thread: output shrink, but an agent run's bill is mostly input and cache re-reads, which an output-style rule cannot touch. The [proxy](../README.md#big-rock-the-proxy) goes after that input instead.

## Measure it yourself

1. `/caveman-stats` (Claude Code) reads the session log and prints recorded output/cache-read counts and mode attribution. It reports savings as unknown because that transcript has no measured comparison without Caveman. A benchmark on other tasks would not verify savings for this session.
2. Run same task with and without Caveman, then compare provider usage or billing page. That A/B outranks repository estimates.
3. Reproduce repository numbers with `benchmarks/run.py` (Anthropic key required) and `evals/measure.py` (offline committed snapshot).

## Rule of thumb

> Compare provider-billed totals on the same task with and without Caveman.
> If Caveman increases billed cost for the same task, turn it off for that workload.

Earlier stats releases applied a fixed 65% output ratio without a committed
reviewed result. Current reports ignore those historical `est_saved_*` fields
while preserving the original history rows. Lifetime and shared reports do
not convert those estimates into verified savings, and the statusline no
longer displays the numeric savings suffix. Memory-file comparisons report
original and current bytes; they do not prove either file was sent to a provider.

If your A/B contradicts these numbers, [open an issue](https://github.com/JuliusBrussee/caveman/issues).
We will add result to this page.
