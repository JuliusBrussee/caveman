---
name: cavecrew
description: >
  Decision guide for delegating compressed subagent work in Hermes. Tells the main thread
  WHEN to spawn a caveman-style worker via delegate_task — investigator (locate code),
  builder (1-2 file edit), or reviewer (diff audit) — instead of doing the work inline.
  Subagent summaries are caveman-compressed, so the result injected back into main context
  is ~60% smaller and the session lasts longer. Trigger: "use cavecrew", "delegate
  compressed", "spawn investigator/builder/reviewer", "save context".
  Redrafted for Hermes from github.com/JuliusBrussee/caveman (MIT).
metadata:
  hermes:
    tags: [delegation, token-efficiency, subagents, context-management]
    source: https://github.com/JuliusBrussee/caveman
    related_skills: [caveman]
---

# cavecrew — compressed delegation (Hermes redraft)

Cavecrew = three `delegate_task` presets whose **final summary is caveman-compressed**.
Hermes injects only a subagent's final summary into main context (intermediate tool calls
never enter your window), so compressing that summary directly shrinks the per-delegation
context cost. Same jobs as normal delegation; difference is the returned summary is terse.

> **Status: experimental. Use only when context-bound.** Each preset costs a full
> delegate_task round-trip (subagent latency + its own token spend) to save ~1–1.5k tokens
> of *return* summary. That trade only wins in long sessions near context exhaustion — for a
> quick lookup, do it inline. Delegation latency depends on your provider; on slow/aux-model
> setups a crew task can take minutes or time out. Prompt-only compression also means a model
> under load may ignore the output contract — verify the shape before relying on it.

> Hermes mechanics: there is no Claude-Code `cavecrew-investigator` agent file. You create
> the preset inline by passing the role contract below as the `context` to `delegate_task`,
> and instructing the worker to "return your final summary in caveman-ultra style per the
> output contract." Leaf subagents cannot delegate further — keep crew tasks single-level.

## When to use cavecrew vs alternatives

| Task | Use |
|---|---|
| "Where is X defined / what calls Y / list uses of Z / map this dir" | investigator preset |
| Same but you also want architecture commentary / suggestions | normal delegate_task (prose) |
| Surgical edit, ≤2 files, scope obvious | builder preset |
| New feature / 3+ files / cross-cutting refactor | main thread or a normal subagent |
| Review a diff / branch / file for bugs | reviewer preset |
| Deep review with rationale + alternatives | normal delegate_task (prose) |
| One-line answer you already know | main thread, no subagent |

Rule of thumb: **want the result in 1/3 the tokens → cavecrew. Want prose → normal delegate_task.**

## Why this exists

A normal research subagent that returns 2k tokens of prose costs 2k tokens of main-context
budget on return. The same finding compressed returns ~700 tokens. Across 20 delegations in
one long session that's the difference between context exhaustion and finishing.

## Role presets (pass as `context` to delegate_task)

### investigator — read-only locator
Toolsets: `["file", "terminal"]` (read_file / search_files live in the **file** toolset;
terminal adds git grep / find). Goal verb: locate, report, stop.
Never edits, never proposes a fix. Output contract:
```
<path:line> — `<symbol>` — <≤6 word note>
```
Group with a one-word header at 3+ rows: `Defs:` / `Refs:` / `Callers:` / `Tests:` / `Sites:`.
Single hit → one line. Zero hits → `No match.` Last line → `totals: 2 defs, 5 refs.` (omit if ≤1).
Asked to fix → return `Read-only. Spawn builder.`

### builder — surgical 1-2 file edit
Toolsets: `["file", "terminal"]`. Scope: 1 file ideal, 2 OK, **3+ → refuse**. Edit existing
only (new file iff user asked). No new abstractions, no drive-by refactors. Workflow:
read target → smallest diff → re-read to verify → return receipt. Output contract:
```
<path:line-range> — <change ≤10 words>.
verified: <re-read OK | mismatch @ path:line>.
```
Terminal refusal first-token: `too-big. split: <n one-line tasks>.` / `needs-confirm. op: <cmd>.`
/ `ambiguous. ask: <one question>.` / `regressed. revert path:line. cause: <fragment>.`

### reviewer — diff/branch/file audit
Toolsets: `["file", "terminal"]` (read_file for sources, terminal for git diff / log — no
mutating commands). Findings only — no
praise, no scope creep, skip formatting nits unless they change meaning. Output contract:
```
path:line: <emoji> <severity>: <problem>. <fix>.
totals: N🔴 N🟡 N🔵 N❓
```
Severity: 🔴 bug (wrong output/crash/security/data loss) · 🟡 risk (edge/race/leak/perf/missing guard)
· 🔵 nit (style/naming — only if asked thorough) · ❓ question (need author intent).
Zero findings → `No issues.` File order, ascending lines within file.

## Chaining patterns

**Locate → fix → verify** (most common): investigator returns site list → main thread picks
1-2 sites, hands exact paths to builder → reviewer audits the diff.

**Parallel scout**: spawn 2-3 investigator tasks in one delegate_task batch (different angles:
defs vs callers vs tests). Aggregate in main thread. (Hermes caps concurrent children — batch
of up to 3.)

**Single-shot edit**: site already known → skip investigator, hand exact path:line to builder.

## What NOT to do

- Don't use builder when you don't already know the file — spawn investigator first or you'll
  burn tokens passing context.
- Don't chain investigator → builder for a 5-file refactor. Builder returns `too-big.` —
  wasted turn.
- Don't ask reviewer for "general feedback" — it returns findings only. Use a prose subagent.
- Don't expect prose. Cavecrew output is structured, sometimes cryptic. If a human reads it
  directly, paraphrase first.

## Auto-clarity (inherited)

Subagents drop caveman → normal English for security warnings, irreversible-action
confirmations, and any output where fragment ambiguity could be misread. Resume caveman after.
