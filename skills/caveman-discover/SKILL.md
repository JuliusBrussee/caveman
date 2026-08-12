---
name: caveman-discover
description: >
  Find every LLM workflow in the current repository and label it, so Caveman
  Cloud groups spend by what the code actually does (support-reply,
  nightly-digest) instead of one anonymous bucket. Use when the user pastes
  the Caveman discovery prompt, says "discover workflows", or asks to break
  LLM spend down by workflow. The repo should already route through the
  Caveman gateway (the caveman-setup skill does that part).
---

You are labeling this repository's LLM workflows for Caveman Cloud. A
*workflow* is a job the code performs — "answer a support ticket", "build the
nightly digest", "run the eval suite" — not a technology. Every gateway
request can carry a workflow label; unlabeled traffic all lands in one
`unlabeled-workflow` bucket. Your job: find the workflows, name them well,
wire the labels, and verify nothing broke.

This changes code, so it goes through the user's normal review: **propose the
table first, apply after the user agrees.** Re-running on an already-labeled
repo must change nothing (idempotent).

This skill is operator-invoked. An `unlabeled-traffic` Cave Plan observation is
review-only and does not create an advisory file, proposal, or Draft PR. Do not
infer that telemetry selected a callsite or authorized an edit. Independently
inventory the repository, present the labeling table, and wait for the user's
approval before changing code.

## Step 1 — Inventory the workflows

Walk the repo from its entry points, not from its imports:

- HTTP/RPC handlers that call an LLM (directly or through layers)
- Scheduled jobs: cron definitions, queue consumers, workers, GitHub Actions
  that invoke LLM code
- CLI commands and scripts (`scripts/`, `bin/`, package.json scripts)
- Eval / test harnesses that burn real tokens
- Distinct agents or chains inside a framework (each LangGraph graph, each
  crew, each agent definition is usually its own workflow)

One workflow = one job a human would name. Ten callsites inside the same
request handler are one workflow; one shared `llm.ts` helper used by three
jobs is three workflows (label at the callers, never the shared helper).

## Step 2 — Name them

Slug grammar (the gateway enforces this): lowercase `[a-z0-9_-]`, 1–96 chars.
Name the job, not the tech:

- Good: `support-reply`, `nightly-digest`, `pr-review`, `eval-suite`,
  `onboarding-email`
- Bad: `openai-calls` (tech), `main` (says nothing), `SupportReply` (invalid),
  `johns-test-3` (won't age)

Names are forever-ish — renaming later splits the spend history. When a job's
purpose isn't clear from the code, derive the slug from the file name and mark
it `review` in the table rather than inventing a purpose.

## Step 3 — Propose, then apply

Present this table and ask to proceed:

```
| workflow | job | where | how it gets labeled |
|---|---|---|---|
| support-reply | answers inbound tickets | src/bot/reply.ts:41 | defaultHeaders on the reply client |
| nightly-digest | 02:00 summary job | jobs/digest.ts:12 | header on the digest client |
| eval-suite (review) | scripts/eval.ts:8 — purpose inferred from filename | scripts/eval.ts:8 | env override at invocation |
```

Then wire each label with the lightest mechanism available at that callsite:

- **@caveman-ai/sdk / caveman_cloud SDK**: per-trace `workflow` option, or
  `defaultWorkflow` on the client a single-job service constructs.
- **Raw provider SDKs** (OpenAI/Anthropic/LangChain/LiteLLM/Vercel): add
  `"x-cave-workflow": "<slug>"` to the same `defaultHeaders` /
  `default_headers` / `extra_headers` block that already carries
  `x-cave-api-key`. Shared client used by several jobs → pass the header per
  call (every SDK above accepts per-request header overrides), or give each
  job its own thin client.
- **Wrapped coding agents** (`caveman wrap`): `--workflow <slug>` flag or
  `CAVE_WORKFLOW=<slug>` env at the invocation site (cron line, CI step).
- **Raw HTTP**: add the `x-cave-workflow` header to the request.

Label the callers, keep the diff minimal, match the repo's style. If a
callsite is not routed through the Caveman gateway at all, don't label it —
list it under "not wired" in the report (labels only travel on gateway
traffic; wiring is the caveman-setup skill's job).

## Step 4 — Verify

Run whatever the repo already uses to exercise one labeled path (a test, a
dev script, one curl). Then confirm: the request still succeeds (the gateway
rejects an invalid label with 400 `cave_invalid_request_header` — fix the slug
if so). Labeled spend appears on the dashboard at `/activity?tab=workflows` as
each workflow next runs; jobs on a schedule show up when the schedule fires,
and that's worth saying in the report rather than pretending they're live.

## Step 5 — Report

```
## Workflows labeled

| workflow | job | where |
|---|---|---|
| support-reply | answers inbound tickets | src/bot/reply.ts:41 |
| nightly-digest | 02:00 summary job | jobs/digest.ts:12 |

Verified: <the labeled path you actually exercised, and what you observed>
Lands at: <DASHBOARD>/activity?tab=workflows — each row appears as that workflow
next runs. Anything still unlabeled shows as `unlabeled-workflow`.
Not wired (no gateway routing, so no label): <list or "none">
Marked review: <slugs whose purpose was inferred from filenames, or "none">
```

If you found no LLM entry points at all: say exactly that, and point at the
setup skill (`<docs origin>/docs/agent-setup.md`) instead of manufacturing a
table.
