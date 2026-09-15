---
name: caveman-evidence-review
description: >
  Read-only review of Caveman Cloud evidence: cost, Cave Score, workflows,
  traces, latency, errors, routing, savings. Use when asked what Caveman found
  or where LLM spend goes.
---

# Review Caveman evidence

Act as a read-only operator. Build conclusions from current Caveman data, not
from repository guesses. Never start, approve, cancel, or roll back an
experiment from this skill.

## Hard rules

1. Keep these buckets separate:
   - measured provider-complete list-price cost;
   - `inferred` daily headroom;
   - `verified` ledger savings;
   - evidence cost.
   Never add or relabel them.
2. Do not fetch prompt, completion, tool, or artifact payloads unless the user
   explicitly asks for payload review. Metadata, spans, timing, models, token
   counts, status, and optimizer attribution are enough for the default review.
3. Scope every read to the project selected by Caveman context. Never supply an
   organization id.
4. Empty results are evidence of no current signal, not zero cost or zero risk.
5. Cite trace ids and exact time windows used. Do not claim a cause from an
   aggregate alone.

## Step 1 — Load context

Prefer MCP:

```text
caveman_context {}
```

CLI fallback:

```bash
caveman cloud whoami
caveman cloud projects list
```

Stop if login or project selection is missing. Ask the user to run
`caveman login` or select a project; never guess.

## Step 2 — Establish baseline

Use `caveman_report` for:

- `overview`
- `costs`
- `score`
- `workflows`
- `verified_savings`

Then use `caveman_plan` for ranked daily headroom. If question is narrow, skip
unrelated reports. Read shortest set that can answer it.

CLI fallback:

```bash
caveman cloud costs
caveman cloud score
caveman cloud plan --json
```

State report window and basis before interpreting direction.

## Step 3 — Test the leading explanation with traces

Use `caveman_trace_search`. Choose a bounded window and closed filters:
workflow, agent, model, provider, error code, runtime mode, cache status,
optimization id, status class, token/cost/latency bounds, compression, or
monitor verdict.

Useful groupings:

- `workflow` — find jobs driving cost or failures;
- `model` — compare model mix;
- `session` — isolate retry or loop behavior;
- ungrouped — identify exact traces.

Compare a suspect cohort with a control cohort or earlier bounded window.
Do not infer causality from one expensive trace.

CLI fallback:

```bash
caveman cloud traces search \
  --workflow <slug> \
  --from <RFC3339> \
  --to <RFC3339> \
  --sort total_cost_usd \
  --dir desc \
  --limit 25
```

## Step 4 — Inspect representative traces

Call `caveman_trace_get` for a small number of high-signal trace ids. Inspect
request and span metadata, latency, status, token counts, cache state, applied
optimizers, and model route. Keep payload retrieval off.

CLI fallback:

```bash
caveman cloud traces show <trace-id> --spans
```

## Step 5 — Report

Use this shape:

```text
## Caveman evidence review

Scope: <project> · <from> to <to>
Measured cost: <value and basis>
Verified savings: <ledger value, kept separate>
Inferred headroom: <per-day band, kept separate>

Findings:
1. <finding> — <aggregate evidence> — traces <ids>
2. <finding> — <aggregate evidence> — traces <ids>

Unproven:
- <plausible explanation lacking a control, trace, or eval>

Next read-only check:
- <one bounded query>

Possible action:
- <proposal only; use caveman-manage for read-only lifecycle review and safety gate>
```

If data is missing, name missing signal and stop at strongest supported
statement. Never turn a catalog subtotal into an invoice or an experiment result
into verified savings.
