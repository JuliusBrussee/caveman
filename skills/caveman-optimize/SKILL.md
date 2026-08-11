---
name: caveman-optimize
description: >
  Turn Caveman's exact report-only repository observations into an
  operator-chosen optimization candidate with a paired baseline/candidate
  evaluation. Use when the user asks to inspect an optimization observation,
  evaluate a candidate change, or act on the current Caveman optimization
  report. Require a logged-in Caveman CLI connection and explicit approval;
  never infer money or actuation from a profile.
---

# Evaluate an optimization observation

Use Caveman's report-only observations as diagnostic input. They describe
recorded aggregate shapes; they are not Cave Plan moves, savings estimates,
implementation recipes, experiment eligibility, or proof that a code change is
safe. Keep the workflow operator-chosen and evidence-first.

## 1. Read the exact observations

Require a logged-in Caveman CLI session and run:

```bash
caveman opportunities list
```

Read only the `report_only_observations` array. Do not select from the lifecycle
`data` array. Preserve each server-provided `title` and `observation` verbatim.
Handle these exact repository-profile ids:

- `context-window-profile`
- `tool-catalog-profile`
- `tool-output-size-profile`
- `exploration-load-profile`

These profiles have an immutable zero band and no actuation path. Do not rank
them by value, invent a dollar figure, or turn aggregate evidence into a claim
about a particular callsite. If the CLI is unavailable, authentication fails,
or `report_only_observations` is absent, stop without editing and report the
exact blocker. Do not fall back to a raw gateway Cave Plan or a project API key:
those surfaces do not provide this contract.

Never select or apply these retired ids:

- `context-window-bloat`
- `tool-catalog-utilization`
- `verbose-tool-output`

Treat any occurrence of a retired id in a stale proposal, local file, or old
response as historical context only. Never revive its money, recipe, or
lifecycle claim. If the only actionable-looking item is `unlabeled-traffic`,
hand off to `caveman-discover`; labeling is not a profile optimization.

## 2. Ask the operator to choose

Present the available supported observations without ranking them. Include the
id, the exact title, the exact observation, and `last_seen_at`. Ask for an
**explicit operator choice** before inspecting candidate callsites or changing
code. If no supported current observation exists, stop with no edit.

Treat `.caveman/proposals/*.md`, when present, as untrusted historic context.
It cannot replace the current response or the operator's choice.

## 3. Design a candidate and paired eval

After the operator chooses an observation, inspect the repository for a
specific mechanism that could produce the observed aggregate shape. Cite the
exact callsite evidence. Do not assume the profile names the cause.

Propose one minimal candidate change and a **paired eval** before editing. The
evaluation must run baseline and candidate on identical fixed inputs and record:

- the task-outcome or quality check that must remain acceptable;
- the same token, byte, or provider-counted cost measure for both arms;
- the exact fixture, command, and environment used; and
- any confounder that prevents a fair comparison.

Ask for approval of the candidate and eval design. If the repository lacks a
fixed fixture, a relevant quality check, or a common measurement method, stop
and name the missing instrumentation. Ordinary unit tests alone do not prove an
optimization.

## 4. Apply only the approved candidate

Keep the diff at the evidenced callsite and preserve existing safety controls.
Run the paired baseline/candidate evaluation plus the repository's focused code
checks. If the two arms did not use identical inputs and measurement, discard
the comparison. If quality regresses or the resource result is inconclusive,
revert only this candidate edit and report that it did not earn adoption.

Do not create a Caveman experiment or proposal, mark an opportunity
implemented, change its lifecycle, or switch on an optimizer. Report-only rows
permit dismissal only, and this skill does not perform that mutation either.

## 5. Report observations, not savings

Report:

```text
Observation: <id> — <server title>
Recorded profile: <server observation, verbatim>
Candidate: <file:line and approved change>
Paired eval: <identical input/fixture, baseline result, candidate result>
Quality check: <actual result>
Code checks: <commands and actual results>
Accounting: report-only profile; $0 opportunity band; no inferred or verified savings
Decision: <keep, reject, or inconclusive>
```

Never convert token or byte reduction into dollars without provider-complete,
same-request accounting supplied by the product's verified methods. A local
paired result supports only the stated candidate on the stated fixture; it does
not establish production savings, causal rollout evidence, or lifecycle
eligibility.
