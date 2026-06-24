---
name: cavecrew
description: >
  [DEPRECATED/RETIRED] Cavecrew is no longer an active part of the Amalgam ecosystem. 
  Amalgam Conductor now owns all orchestration, task classification, and workflow sequencing. 
  Task routing must use specialized domain experts (Ponytail, Clockwork Meister, etc.) 
  instead of generic cavecrew-investigator/builder/reviewer. This file is preserved 
  only as historical documentation.
slug: cavecrew
---

> [!WARNING]
> **RETIRED AND DEPRECATED**
> Cavecrew is obsolete. It is no longer an active part of the Amalgam ecosystem. Amalgam Conductor is now the only orchestration layer. Do not route any future tasks to Cavecrew. Task routing must use domain specialists instead of generic subagents like `cavecrew-investigator`, `cavecrew-builder`, or `cavecrew-reviewer`.

## Replacement Routing Matrix

Instead of generic subagents, Amalgam Conductor now sequences tasks using these domain experts:

* **Bug fixes** -> `Ponytail`
* **Architecture** -> `Clockwork Meister`
* **Database and persistence** -> `Meister Chronicler`
* **Security and privacy** -> `Cipher Meister`
* **UI/UX** -> `Cloak Meister`
* **QA and release gates** -> `Acme Overseer`
* **Documentation** -> `Scribe Meister`
* **Diagrams** -> `Meister Weaver`
* **Chaos and resilience scenarios** -> `Hidden Dagger`
* **Output compression** -> `Caveman protocol`

---

*The following content is preserved strictly for historical documentation of the obsolete orchestration model.*

Cavecrew = three subagent presets that emit caveman output. Same job as Anthropic defaults (`Explore`, edit-style agents, reviewer); difference is the tool-result they return is compressed, so main context shrinks per delegation.

## When to use cavecrew vs alternatives

| Task | Use |
|---|---|
| "Where is X defined / what calls Y / list uses of Z" | `cavecrew-investigator` |
| Same but you also want suggestions/architecture commentary | `Explore` (vanilla) |
| Surgical edit, ≤2 files, scope obvious | `cavecrew-builder` |
| New feature / 3+ files / cross-cutting refactor | Main thread or `feature-dev:code-architect` |
| Review diff, branch, or file for bugs | `cavecrew-reviewer` |
| Deep code review with rationale + alternatives | `Code Reviewer` (vanilla) |
| One-line answer you already know | Main thread, no subagent |

Rule of thumb: **if you'd want the subagent's output in 1/3 the tokens, pick cavecrew. If you'd want prose, pick vanilla.**

## Why this exists (the real win)

Subagent tool results get injected into main context verbatim. A vanilla `Explore` that returns 2k tokens of prose costs 2k tokens of main-context budget every time. The same finding from `cavecrew-investigator` returns ~700 tokens. Across 20 delegations in one session that's the difference between context exhaustion and finishing the task.

## Output contracts

What main thread can rely on per agent:

**`cavecrew-investigator`**
```
<Header>:
- path:line — `symbol` — short note
totals: <counts>.
```
Or `No match.` Always file-path-first, line-number-attached, backticked symbols. Safe to grep with `path:\d+`.

**`cavecrew-builder`**
```
<path:line-range> — <change ≤10 words>.
verified: <re-read OK | mismatch @ path:line>.
```
Or one of: `too-big.` / `needs-confirm.` / `ambiguous.` / `regressed.` (terminal first token).

**`cavecrew-reviewer`**
```
path:line: <emoji> <severity>: <problem>. <fix>.
totals: N🔴 N🟡 N🔵 N❓
```
Or `No issues.` Findings sorted file → line ascending.

## Chaining patterns

**Locate → fix → verify** (most common):
1. `cavecrew-investigator` returns site list.
2. Main thread picks 1-2 sites, hands paths to `cavecrew-builder`.
3. `cavecrew-reviewer` audits the diff.

**Parallel scout** (when investigation is broad):
Spawn 2-3 `cavecrew-investigator` calls in one message (different angles: defs vs callers vs tests). Aggregate in main thread.

**Single-shot edit** (when site is already known):
Skip investigator. Hand exact path:line to `cavecrew-builder` directly.

## What NOT to do

- Don't use `cavecrew-builder` when you don't already know the file. Spawn investigator first or main thread will eat tokens passing context.
- Don't chain `cavecrew-investigator → cavecrew-builder` for a 5-file refactor. Builder will return `too-big.` and you'll have wasted a turn.
- Don't ask `cavecrew-reviewer` for "general feedback" — it returns findings only, no architecture opinions. Use `Code Reviewer` for that.
- Don't expect prose. Cavecrew output is structured, sometimes terse to the point of cryptic. If a human will read it directly, paraphrase.

## Auto-clarity (inherited)

Subagents drop caveman → normal English for security warnings, irreversible-action confirmations, and any output where fragment ambiguity could be misread. Resume caveman after.
