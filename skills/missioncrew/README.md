# missioncrew

Decision guide. When to delegate to missionctl subagents instead of doing the work inline.

## What it does

Tells the main thread when to spawn a missionctl-style subagent versus the vanilla equivalent. The win: subagent tool-results inject back into main context verbatim, and missionctl output is roughly 1/3 the size of vanilla prose. Across 20 delegations in one session, that is the difference between context exhaustion and finishing the task.

Three subagents:

| Subagent | Job | Use when |
|----------|-----|----------|
| `missioncrew-investigator` | Locate code (read-only) | "Where is X defined / what calls Y / list uses of Z" |
| `missioncrew-builder` | Surgical edit, 1-2 files | Scope is obvious, ≤2 files. Refuses 3+ file scope. |
| `missioncrew-reviewer` | Diff/file review | One-line findings with severity emoji |

Use vanilla `Explore` or `Code Reviewer` when you want prose, architecture commentary, or rationale. Use main thread directly for one-line answers and 3+ file refactors.

This skill is a decision guide, not a slash command. It activates when the conversation mentions delegation.

## How to invoke

Triggers on phrases like "delegate to subagent", "use missioncrew", "spawn investigator", "save context", "compressed agent output".

## Example chaining

Locate → fix → verify (most common):

1. `missioncrew-investigator` returns site list (`path:line — symbol — note`)
2. Main thread picks 1-2 sites, hands paths to `missioncrew-builder`
3. `missioncrew-reviewer` audits the resulting diff

Parallel scout: spawn 2-3 `missioncrew-investigator` calls in one message with different angles (defs, callers, tests). Aggregate in main.

## See also

- [`SKILL.md`](./SKILL.md) — full decision matrix and output contracts
- [`agents/missioncrew-investigator.md`](../../agents/missioncrew-investigator.md)
- [`agents/missioncrew-builder.md`](../../agents/missioncrew-builder.md)
- [`agents/missioncrew-reviewer.md`](../../agents/missioncrew-reviewer.md)
- [missionctl README](../../README.md) — repo overview
