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
| Output reduction vs default verbose replies | Not published | Harness exists, but repository has no committed reviewed raw result | [`benchmarks/`](../benchmarks/) |
| Input reduction from the skill | 0% | It's an output-style instruction | Not applicable |
| Input cost the skill *adds* | ~1–1.5k tokens per turn | SKILL.md rules (~5 KB) injected into context, plus skill-list entries | [`skills/caveman/SKILL.md`](../skills/caveman/SKILL.md) |
| `/caveman-compress` on memory files | ~46% average input reduction across five listed fixtures | Fixture token counts plus structural checks; no general quality-equivalence claim | [caveman-compress fixtures](../skills/caveman-compress/README.md#benchmarks) |

Token-count runs measure output length only. They do not prove semantic or technical equivalence. Publish a reduction only with committed raw pairs and separate quality review. The full eval harness and its correction history are documented in [`evals/README.md`](../evals/README.md).

## When caveman wins

- Long chatty outputs give terse style more removable prose. Measure your own A/B; no aggregate reduction is currently published.
- Long sessions with verbose agents can accumulate per-reply output reduction while fixed rule cost repeats each turn.
- Shorter replies can finish sooner and take less time to read.

## When caveman loses (net-negative)

Skill costs about 1–1.5k input tokens every turn. If it saves less output than
that, you are paying to use it.

- Terse coding Q&A ([#145](https://github.com/JuliusBrussee/caveman/issues/145)): fixed prompt overhead can exceed any output reduction. User in #145 measured a net loss.
- Agents billed by request or credit ([#506](https://github.com/JuliusBrussee/caveman/issues/506)): GitHub Copilot charges premium *requests*. A shorter answer is same request, so Caveman cannot lower Copilot credit use. Same applies to other per-message pricing.
- Session totals can differ sharply from output-only changes because prompts, context, files, and injected rules consume tokens. Provider-billed A/B totals outrank output-only estimates.
- Tool-side counters can go wrong direction ([#550](https://github.com/JuliusBrussee/caveman/issues/550)). One Cursor A/B showed 4.3M tokens with caveman versus 1M without and twice wall-clock time. Exact run was not reproducible, so only safe conclusion is that rule re-injection, retries, and cache or context accounting can overwhelm output savings. Turn Caveman off if your A/B is net-negative.

## Measure it yourself

1. `/caveman-stats` (Claude Code) reads session log and prints actual output/cache counts. It publishes no counterfactual savings until a reviewed benchmark result is committed.
2. Run same task with and without Caveman, then compare provider usage or billing page. That A/B outranks repository estimates.
3. Reproduce repository numbers with `benchmarks/run.py` (Anthropic key required) and `evals/measure.py` (offline committed snapshot).

## Rule of thumb

> Compare provider-billed totals on the same task with and without Caveman.
> If fixed prompt overhead exceeds output reduction, turn Caveman off for that workload.

If your A/B contradicts these numbers, [open an issue](https://github.com/JuliusBrussee/caveman/issues).
We will add result to this page.
