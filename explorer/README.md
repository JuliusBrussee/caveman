# @caveman/explorer — the FastContext exploration subagent

A free, BYOK exploration subagent for coding agents, based on **FastContext**
([arXiv 2606.14066](https://arxiv.org/abs/2606.14066)). It separates *finding code*
from *changing code*: a cheap, read-only explorer answers "where does X live?" with a
compact list of `path:line` citations, and its reads/greps never enter your main
agent's context window.

In the paper, repository reading and searching account for **~46% of a coding agent's
tokens**. Offloading that to a small explorer cut end-to-end tokens 14–60% while
resolution went *up*.

## Install

```bash
cave explore install            # writes ./.claude/agents/fastcontext.md (this repo)
cave explore install --user     # writes ~/.claude/agents/fastcontext.md (all repos)
```

Claude Code then delegates exploration to the `fastcontext` subagent automatically
(it runs on Haiku, with only Read/Glob/Grep). The subagent runs in its own isolated
context, so the solver only ever sees the final citations — that separation is the
mechanism, and Claude Code's native subagents give it for free.

> Codex is not wired yet. It needs an MCP shim that must pass a transcript-isolation
> test before it ships, or it would be FastContext-in-name-only.

## What it honestly claims

Caveman does not train the paper's 4B model, so it does **not** inherit the paper's
14–60% headline. What it claims is what it can prove:

- **Local / BYOK:** nothing. The explorer just runs; you keep your own keys.
- **After `caveman login`:** because both the explorer (Haiku) and the solver
  (Sonnet/Opus) route through the gateway, Caveman **measures** the explorer's spend
  against the solver's — a real cost split, not an estimate. The explorer's added
  spend is shown as cost, never netted into a savings number.
- **Savings stay `inferred`** (a per-day rate, never multiplied to a month) until a
  CaveBench A/B proves a net win *and* non-regressed resolution for your specific
  main-model pairing. For some pairings that honest number may be near zero — and
  that is the point.

The funnel: free subagent → `caveman login` → measured explorer cost → (after the
eval gate) an eval-validated delta.

## How it relates to the rest of Caveman

`exploration-offload` is a Cave Plan optimizer in the `input_bloat` mutex family
(S3, eval-gated). The detector that surfaces it from telemetry stays dormant until the
gateway parses tool-call blocks (it shares headroom with `context-compression`, so it
is deduped against it — its distinct value is the resolution/quality axis and the
measured cost split, not new headroom dollars).
