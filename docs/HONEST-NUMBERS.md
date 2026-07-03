# Honest Numbers

Caveman save tokens sometimes. Caveman cost tokens sometimes. This page say which is which, with the real numbers. No marketing. If caveman lose for your workload, this page tell you to turn it off.

## What caveman actually does

Caveman is a system-prompt skill. It makes the model **write shorter output**. That is the whole mechanism. It does not compress your input, your context, your files, or the model's thinking tokens.

## The measured numbers

| What | Number | How measured | Source |
|---|---|---|---|
| Output reduction vs default verbose replies | **65% average** (range 22–87%) | Real Claude API token counts, 10 prompts | [`benchmarks/`](../benchmarks/) |
| Input reduction from the skill | **0%** | It's an output-style instruction | — |
| Input cost the skill *adds* | **~1–1.5k tokens per turn** | SKILL.md rules (~5 KB) injected into context, plus skill-list entries | [`skills/caveman/SKILL.md`](../skills/caveman/SKILL.md) |
| `/caveman-compress` on memory files | ~46% average input reduction, per session, for those files only | Real files, token counts in README table | [README](../README.md#benchmarks) |

These figures are output tokens only — the skill does not compress your input, your context, your files, or the model's thinking tokens. The full eval harness and its correction history are documented in [`evals/README.md`](../evals/README.md).

## When caveman wins

- **Long chatty outputs.** Explanations, architecture discussions, code review, docs, debugging walkthroughs — anywhere the model would write 1k+ output tokens per reply. This is where the 50–87% cuts happen.
- **Long sessions with verbose agents.** The per-reply savings compound; the fixed ~1–1.5k/turn rule cost stays flat.
- **Reading speed.** Shorter replies finish sooner and you read them faster. For many users this, not cost, is the real win.

## When caveman loses (net-negative)

Plainly: **the skill costs ~1–1.5k input tokens every turn. If it saves less output than that, you are paying to use it.**

- **Terse coding Q&A** ([#145](https://github.com/JuliusBrussee/caveman/issues/145)). If your normal replies are ~150 output tokens, caveman saves maybe 70–100 of them and costs ~1k+ of input overhead per turn. Net loss. The user in #145 measured exactly this. They were right.
- **Agents that bill by request or credit, not tokens** ([#506](https://github.com/JuliusBrussee/caveman/issues/506)). GitHub Copilot charges premium *requests*. A shorter answer is the same request. Caveman cannot lower your Copilot credit use. Same logic for any per-message pricing.
- **Session-level totals** are always smaller than the output-reduction headline, because input tokens (your prompts, your context, your files, the injected rules) dwarf output tokens in agentic coding. Independent session-level measurements land around **14–21% total savings** on output-heavy workloads — and below zero on terse ones.
- **Some tool-side counters go the wrong way** ([#550](https://github.com/JuliusBrussee/caveman/issues/550)). One Cursor A/B showed 4.3M tokens with caveman vs 1M without, and double the wall-clock time. We could not reproduce the exact run, but the honest reading is: rule re-injection, retries, and cache/context accounting can swamp output savings in some agents. If your A/B looks like that, caveman is net-negative for you. Turn it off. Wanting the rock to work does not make the rock work.

## Measure it yourself

1. **`/caveman-stats`** (Claude Code) reads your real session log and prints actual input/output token counts. The "saved" line is an **estimate**: it extrapolates what the output would have been without caveman using the benchmark ratio. Real usage, estimated baseline — the output labels it `est.` for exactly that reason.
2. **The only fully honest test is an A/B**: run the same task with and without caveman and compare your provider's own usage/billing page. That number outranks anything this repo prints.
3. **Reproduce our numbers**: `benchmarks/run.py` (needs an Anthropic key) and `evals/measure.py` (offline, reads the committed snapshot).

## Rule of thumb

> Normal reply longer than ~1.5–2k output tokens → caveman probably saves you money.
> Normal reply shorter than that, or you pay per request → caveman probably costs you money.
> Either way, caveman replies faster to read. That part is free.

Found a workload where our numbers are wrong? [Open an issue](https://github.com/JuliusBrussee/caveman/issues) with the A/B. We will put it on this page.
