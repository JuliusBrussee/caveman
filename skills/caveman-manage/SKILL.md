---
name: caveman-manage
description: >
  Inspect Caveman Cloud's eval-gated experiment lifecycle and block unsafe
  execution. Use when the user asks to start, approve,
  cancel, promote, or roll back a Caveman experiment, or asks what action an
  experiment's evidence supports. Read evidence first; do not execute lifecycle
  mutations until server-authoritative transition and evidence gates ship.
---

# Manage eval-gated experiments

Treat every lifecycle change as a production control action. Read current state
and results, then report one supported recommendation or block.
Current agent MCP is intentionally read-only: control-api does not yet enforce a
complete lifecycle transition table and evidence gate atomically.

## Non-negotiable gates

1. A request to review, inspect, explain, or recommend authorizes reads only.
2. Never approve an experiment whose results are pending, whose required
   guardrails are absent, or whose evidence reports a breach.
3. Never convert experiment lift into `verified_savings`. Only active real
   traffic plus provider-causal, provider-complete ledger evidence can do that.
4. Never supply an organization id. Project and tenant scope come from the
   logged-in Caveman identity and server RBAC.
5. Never execute a lifecycle mutation, even after user approval. Exact
   `<action>:<experiment_id>` strings are agent-generatable and are not proof of
   human intent.
6. Unknown states and server errors fail closed. Report exact
   `cave_snake_code`.

## Step 1 — Load project and experiment

Prefer MCP:

```text
caveman_context {}
caveman_experiment_get {"action":"get","experiment_id":"<id>"}
caveman_experiment_get {"action":"results","experiment_id":"<id>"}
```

Use `{"action":"list"}` when the user has not named an id.

CLI fallback:

```bash
caveman cloud experiments list
caveman cloud experiments show <id>
caveman cloud experiments results <id>
```

Stop if login, project, experiment, or results are unavailable.

## Step 2 — Evaluate evidence

Report:

- current lifecycle state and safety class;
- control and candidate sample sizes;
- quality or eval result;
- latency, error, cost, retry, drop, and escalation guardrails when present;
- evidence cost;
- rollback or hold reason;
- whether result is pending, failed, promotable, or active.

Absence is not a pass. If a required field is absent, state
`evidence incomplete` and do not propose approval.

## Step 3 — Propose one action

Allowed actions:

- `start` — only from a startable draft or queued state with configured graders;
- `approve` — only with complete passing evidence and a safety class the
  current role may approve;
- `cancel` — stop a non-active experiment the user no longer wants;
- `rollback` — revert an active or harmful change through the server's linked
  policy path. Current deployments may reject this honestly with
  `cave_not_implemented`; never describe that response as a rollback.

Show recommendation and id:

```text
Proposed action: approve experiment 7f...
Reason: candidate passed quality and every configured guardrail.
Execution: blocked until server-authoritative lifecycle and evidence gates ship.
```

Do not treat earlier generic statements such as "manage it" or "do what is best"
as mutation approval.

## Step 4 — Block unsafe execution

Do not emit or run an executable lifecycle command. Explain that current server
does not yet enforce every evidence/state transition atomically. CLI and MCP
agent surfaces therefore expose experiment reads only.

## Step 5 — Re-read after external operator action

If operator says they executed command, read detail and results again. Report
server-observed post-state, audit or result response, and any policy-delivery
status returned. Never infer success from operator intent alone.

Use this close:

```text
Action: <action> <experiment-id>
Before: <state>
Server response: <status and cave_snake_code if any>
After: <re-read state>
Basis: experiment evidence only. Verified savings unchanged unless the signed
ledger independently records active, provider-causal real-traffic savings.
```
