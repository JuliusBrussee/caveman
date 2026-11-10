# sweetcrew

Decision guide. When to delegate to sweet subagents instead of doing the work inline.

## What it does

Tells the main thread when to spawn a sweet-style subagent versus the vanilla equivalent. The win: subagent tool-results inject back into main context verbatim, and compressed output is substantially smaller than vanilla prose. Across 20 delegations in one session, that can be the difference between context exhaustion and finishing the task. (The exact ratio is unmeasured for Korean — see [docs/HONEST-NUMBERS.md](../../docs/HONEST-NUMBERS.md).)

The subagents emit compressed **Korean**, but without 반말/애교: their output is data the main thread reads, not prose for a human. The main thread re-renders the final answer in the sweet voice.

Three subagents:

| Subagent | Job | Use when |
|----------|-----|----------|
| `sweetcrew-investigator` | Locate code (read-only) | "X 어디 정의됐어 / Y 누가 호출해 / Z 쓰는 곳 전부" |
| `sweetcrew-builder` | Surgical edit, 1-2 files | Scope is obvious, ≤2 files. Refuses 3+ file scope. |
| `sweetcrew-reviewer` | Diff/file review | One-line findings with severity emoji (🔴 버그 / 🟡 위험 / 🔵 사소 / ❓ 질문) |

Use vanilla `Explore` or `Code Reviewer` when you want prose, architecture commentary, or rationale. Use main thread directly for one-line answers and 3+ file refactors.

This skill is a decision guide, not a slash command. It activates when the conversation mentions delegation.

## How to invoke

Triggers on phrases like "서브에이전트한테 넘겨", "sweetcrew 써", "investigator 띄워", "컨텍스트 아껴", "압축된 에이전트 출력" (and their English equivalents).

## Example chaining

Locate → fix → verify (most common):

1. `sweetcrew-investigator` returns site list (`path:line — symbol — 메모`, grouped under `정의:` / `호출:` / `테스트:`)
2. Main thread picks 1-2 sites, hands paths to `sweetcrew-builder`
3. `sweetcrew-reviewer` audits the resulting diff

Parallel scout: spawn 2-3 `sweetcrew-investigator` calls in one message with different angles (defs, callers, tests). Aggregate in main.

## Model overrides

By default only `sweetcrew-investigator` pins `model: haiku` — locating a symbol is mechanical and the output contract is strict. `sweetcrew-reviewer` and `sweetcrew-builder` have no `model:` line and inherit the session model: finding bugs and editing code are judgement work, and pinning them to a small model would trade review quality for tokens the compressed output format already saves. Set env vars in your shell before launching Claude Code to override per-agent:

| Env var | Agent |
|---|---|
| `SWEETCREW_REVIEWER_MODEL` | `sweetcrew-reviewer` |
| `SWEETCREW_BUILDER_MODEL` | `sweetcrew-builder` |
| `SWEETCREW_INVESTIGATOR_MODEL` | `sweetcrew-investigator` |

Example — run reviewer on sonnet, keep others on default:

```sh
export SWEETCREW_REVIEWER_MODEL=sonnet
```

Use the same model name strings you'd use in any Claude Code agent frontmatter (e.g. `haiku`, `sonnet`, `opus`).

Overrides patch only the `model:` line in the installed agent's frontmatter; the prompt body is untouched and keeps receiving upstream updates. Plugin installs only — standalone hook installs have no local agent files to patch. Unset or blank = no change. The patch persists in the installed file until the plugin is updated or reinstalled.

## See also

- [`SKILL.md`](./SKILL.md) — full decision matrix and output contracts
- [`agents/sweetcrew-investigator.md`](../../agents/sweetcrew-investigator.md)
- [`agents/sweetcrew-builder.md`](../../agents/sweetcrew-builder.md)
- [`agents/sweetcrew-reviewer.md`](../../agents/sweetcrew-reviewer.md)
- [레포 README](../../README.md) — 개요
