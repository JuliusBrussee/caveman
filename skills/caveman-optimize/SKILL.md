---
name: caveman-optimize
description: >
  Read this repository's Cave Plan machine-readably and apply its top
  structural money move in-repo — truncate oversized tool results, defer unused
  tool schemas, compress stable context, or label unlabeled traffic. Use when
  the user pastes the Caveman optimize prompt, says "apply the top move",
  "optimize the plan", or "act on the Cave Plan". The repo should already route
  through the Caveman gateway (the caveman-setup skill does that part).
---

You are applying this repository's Cave Plan — the ranked, dollar-quantified
list of optimizer moves Caveman Cloud inferred from real traffic. Your job:
read the plan over the wire, pick the top **structural (S2)** move whose fix
lives in this repo, implement it minimally, and report honestly. You do not
turn on any optimizer and you never claim a verified saving — the plan's
figures are `inferred` per-day rates, and they stay that way until a change is
deployed, eval-gated, and active on real traffic.

Prefer the logged-in Caveman MCP or CLI connection. If neither is available,
the prompt that sent you here must provide three values. Refer to them as:

- `GATEWAY` — the base URL that serves `/sdk/v1` (the same one this repo is
  already wired to, e.g. `https://gateway.caveman.so` or `http://127.0.0.1:8787`)
- `CAVE_API_KEY` — the project key, scoped `plan:read` (env var only, never
  committed, never printed in full)
- `DASHBOARD` — the dashboard base URL (e.g. `https://app.caveman.so`)

If MCP and CLI are unavailable and any prompt value is missing, stop and ask
for it. Do not guess a URL or mint a key.

This changes code, so it goes through the user's normal review: **propose the
move first, apply after the user agrees.** Re-running on an already-applied
repo must change nothing (idempotent).

First, check for `.caveman/proposals/*.md`. If present, this repo's Cave Plan
already flagged a move and a Cave Agent draft PR sent you here — you are on that
PR's branch to apply it. Cite the proposal file in your report and leave the
advisory file in place (the PR review decides its fate, not you).

## Step 1 — Read the plan

Prefer the read-only MCP tool:

```text
caveman_plan {}
```

CLI fallback:

```bash
caveman cloud plan --json
```

Only when neither connected surface is available, fetch with the project key
provided in the prompt:

Fetch the project-scope Cave Plan (a GET, no body):

```bash
curl -sS "$GATEWAY/sdk/v1/cave-plan" -H "x-cave-api-key: $CAVE_API_KEY"
```

The response is JSON: a `headline` (inferred per-day headroom band),
`headroom_by_class`, a ranked `moves` array, and `no_signal` (optimizers with
no current signal, each with a plain reason). If the repo uses `@caveman/sdk`
or `caveman_cloud`, `cave.cavePlan()` / `cave.cave_plan()` returns the same
object. A non-200 means the key lacks `plan:read` or the gateway is unreachable
— report the matching failure template, never a guessed plan.

## Step 2 — Pick the move

From `moves`, keep only the ones whose fix is in this repo — these four
optimizer ids, all safety class `S2_STRUCTURAL`:

- `context-window-bloat` — the session's full history is re-sent on every turn
  instead of being checkpointed or compacted
- `verbose-tool-output` — oversized tool answers re-enter context every turn
- `tool-catalog-utilization` — the declared tool catalog carries tools the
  window never invoked, re-sent on every request
- `unlabeled-traffic` — spend lands in one anonymous bucket for want of labels

Pick the one with the highest `savings_usd_base`. Ignore any move carrying an
`already_active_note` (its optimizer is already active — nothing to do here) and
anything outside the four above (routing, model right-sizing and cache moves are
policy or dashboard changes, not code you edit from here). Read the move's
`top_scopes` — they name the workflow/model driving the headroom, so you fix the
right callsite, not every callsite.

## Step 3 — Implement it (minimally)

Use the lightest mechanism at the driving callsite; match the repo's style;
keep the diff small.

- **`verbose-tool-output`** — cap tool-result payloads before they go back
  into the model's context. With the Caveman SDK, page large outputs to an
  artifact stub: `trace.artifacts.page(value, { source: "tool-output", strategy:
  "json-index", maxInlineTokens })` (TS) / `trace.artifacts.page(value, {"source":
  "tool-output", "strategy": "json-index"})` (Python). `source` is required — it
  labels where the artifact came from. Without the SDK, truncate or summarize the
  oversized field at the callsite the
  scope names. This changes what the model sees, so it is exactly the kind of
  move that `requires_eval_gate` — propose it, apply after approval.
- **`tool-catalog-utilization`** — send only the relevant tool schemas per
  turn: `cave.tools({ catalog, strategy: "deferred" })` then
  `handle.search(query)` (TS) / `cave.tools(catalog, strategy="deferred")`
  then `handle.search(query)` (Python). The gateway returns the reduced set;
  never-invoked schemas stop riding every request. The move's evidence names
  the never-invoked tools it measured.
- **`context-window-bloat`** — checkpoint or compact the re-sent session
  history at the driving callsite, or run large, stable, read-only context
  blocks through `cave.compress(payload)` (TS) / `cave.compress(payload)`
  (Python) before sending. The SDK is byte-safe — on any problem it passes the
  original through, so this never corrupts a request. The call returns a
  `recoveryHandle` owned by the gateway that issued it. Recover the byte-exact
  original with authenticated `GET $GATEWAY/sdk/v1/ccr/<handle>` using the
  same-project `sdk:write` API key already used by the issuing `cave.compress`
  client (send it through its existing env variable in `x-cave-api-key`; never
  print or hardcode it). The `CAVE_API_KEY` supplied to this skill is only
  `plan:read` and is insufficient unless the operator explicitly issued it with
  both scopes. The SDK itself exposes no recovery method. Do not send this
  handle to local `caveman retrieve` or `caveman_retrieve`: those surfaces
  resolve only handles issued by their local Engine/CCR store.
- **`unlabeled-traffic`** — set the workflow label (`x-cave-workflow` header,
  `defaultWorkflow`, or the per-trace `workflow` option) at the driving
  callsites. This overlaps the caveman-discover skill, which does the full
  inventory — if more than one workflow is unlabeled, hand off: fetch
  `<docs origin>/docs/discover-workflows.md` and follow it instead of labeling
  ad hoc.

Label the callers, not shared helpers. If the driving callsite is not routed
through the Caveman gateway, don't touch it — say so in the report.

## Step 4 — Verify

Run whatever the repo already uses to exercise the changed path (a test, a dev
script, one request). Confirm it still succeeds and behaves the same — a
structural move must not change task outcomes, only the tokens spent getting
there. If a labeled request 400s with `cave_invalid_request_header`, fix the
slug. Report what you actually ran and saw, never an assumed pass.

## Step 5 — Report

```
## Applied a Cave Plan move

Move: <optimizer_id> — <title>
Where: <file:line you changed> (driving scope: <workflow/model from top_scopes>)
Change: <one line — what you wired>
Inferred headroom: $<savings_usd_base>/day (inferred, per-day — the plan's own
figure, not a measured or verified saving).
Verified: HTTP <status> · <what you exercised and observed>
```

Then, verbatim, the honesty close:

```
This is a client-side code change, not a switched-on optimizer, and it cannot
mint `verified_savings` — that ledger comes only from the gateway's
provider-counted methods, which a repo-side structural edit never triggers. The
reduction shows up as lower measured spend; the plan's $<...>/day stays
`inferred`, never relabeled verified and never re-projected to a month.
Watch it land at <DASHBOARD>/cave-plan (this move) and <DASHBOARD>/cave-score.
```

If the user wants to create, approve, or roll back an experiment after the code
change, hand off to `caveman-manage` for read-only evidence review and a
blocked recommendation. Do not execute an experiment lifecycle mutation.

## Failure templates (use verbatim, filled in — never soften)

- **No structural move to apply**: "The Cave Plan has no in-repo structural
  move right now (checked context-window-bloat, verbose-tool-output,
  tool-catalog-utilization, unlabeled-traffic). Top of the plan is <optimizer_id>,
  which is a <policy/dashboard> change — see <DASHBOARD>/cave-plan. Nothing to
  wire from here."
- **Plan unreachable**: "GET $GATEWAY/sdk/v1/cave-plan did not return a plan
  (<error>). Nothing was changed. Check the gateway URL and network, then
  re-run the fetch above."
- **403 / scope**: "The gateway rejected the read — CAVE_API_KEY is missing the
  `plan:read` scope. Grant it (a key-manager can, at <DASHBOARD>/api-keys) or
  mint a plan-readable key, then re-run. No code was changed."
- **Empty plan**: "The plan is empty — either detection has not completed a run
  yet or there is not enough traffic to infer a move. See <DASHBOARD>/cave-plan
  for the reason (`as_of` / diagnostics). Nothing to apply yet."

Never report a saving you did not measure. An applied move is reported as an
`inferred` per-day figure and a $0 verified balance until the eval-gated
rollout earns otherwise.
