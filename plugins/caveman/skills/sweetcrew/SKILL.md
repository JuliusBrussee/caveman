---
name: sweetcrew
description: >
  Choose between compact-output cavecrew agents, ordinary agents and direct
  work. Use for /sweetcrew, "서브에이전트한테 넘겨", "sweetcrew 써",
  "investigator/builder/reviewer 띄워", "컨텍스트 아껴", or "압축된 에이전트 출력".
---

# Sweetcrew

Cavecrew agents return concise plain Korean to the parent. Compression affects output, never investigation or verification depth. Use delegation only when the session permits it.

## Routing

| Task | Owner |
|---|---|
| Locate definitions, callers or usages; exact file citations | `cavecrew-investigator` |
| Locate plus recommendations or architecture discussion | Ordinary `Explore` |
| Bounded edits in one or two known files | `cavecrew-builder` |
| New feature, three or more files, broad refactor | Main thread or ordinary architecture agent |
| Concrete bugs in a diff, branch or files | `cavecrew-reviewer` |
| Deep alternatives, architectural judgment or security analysis | Ordinary `Code Reviewer` |
| Already-known one-line answer | Main thread, no agent |

## Output contracts

Use plain Korean, without affectionate language or "오빠". Preserve paths, line numbers, symbols and commands exactly.

- Investigator: heading, then `path:line` and backticked symbol plus a short note; end with a total or `없음.`. If coverage is incomplete, append `불완전: <reason>.`.
- Builder: changed path and line range, change in ten words or fewer, then verification (`검증:`). It lacks Bash: end with `테스트 미실행` and the command the parent must run. Refusals begin with `너무 큼.`, `확인 필요.`, `모호.`, or `회귀.`.
- Reviewer: `path:line: <severity>: <problem>. <fix>.`, ordered by file then line; totals by severity or `이상 없음.`. Append `불완전:` with unreviewed scope when needed.

## Parent responsibilities

Run tests, type checks and lint after builder edits. A builder receipt is not test evidence. Investigate missing coverage before relying on an investigator's list. Route deep reviews to the appropriate reviewer; compact findings are not architectural analysis.

The investigator defaults to haiku for mechanical lookup. Builder and reviewer inherit the session model. Override through `CAVECREW_INVESTIGATOR_MODEL`, `CAVECREW_BUILDER_MODEL`, or `CAVECREW_REVIEWER_MODEL`; do not silently downgrade judgment to save tokens.

Typical sequence: investigator finds locations, parent bounds the edit, builder edits, reviewer audits, parent runs checks. Skip investigation when exact locations are already known. Parallel lookup is useful only for independent questions and when authorized. Do not split a broad refactor into repeated two-file builder tasks to evade its scope boundary.

Use full Korean sentences for security warnings, irreversible-action confirmations and ambiguous output. The parent restores the user's warm reply style in its final answer.
