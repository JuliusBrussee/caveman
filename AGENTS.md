@./skills/caveman/SKILL.md
@./skills/caveman-commit/SKILL.md
@./skills/caveman-review/SKILL.md
@./caveman-compress/SKILL.md

<!-- multiagent-safety:START -->
## Multi-Agent Execution Contract (GX)

**Repo toggle.** Guardex is enabled by default. If the repo root `.env` sets `GUARDEX_ON=0`, `false`, `no`, or `off`, treat this entire Guardex contract as disabled for the repo and do not require Guardex worktrees, lock claims, completion flow, or OpenSpec workflow until `GUARDEX_ON` is re-enabled.

**Repo toggle examples.** Add one of these lines to the repo-root `.env` file:
`GUARDEX_ON=0` disables Guardex for that repo.
`GUARDEX_ON=1` explicitly enables Guardex for that repo again.

**Task-size routing.** Small tasks stay in direct caveman-only mode. For typos, single-file tweaks, one-liners, version bumps, or similarly bounded asks, solve directly and do not escalate into heavy OMX orchestration just because a keyword appears. Treat `quick:`, `simple:`, `tiny:`, `minor:`, `small:`, `just:`, and `only:` as explicit lightweight escape hatches.
Promote to OMX orchestration only when the task is medium/large: multi-file behavior changes, API/schema work, refactors, migrations, architecture, cross-cutting scope, or long prompts. Heavy OMX modes (`ralph`, `autopilot`, `team`, `ultrawork`, `swarm`, `ralplan`) are for that larger scope. If the task grows while working, upgrade then.

## Token / Context Budget

Default: less word, same proof.

- For prompts about `token inefficiency`, `reviewer mode`, `minimal token overhead`, or session waste patterns, switch into low-overhead mode: plan in at most 4 bullets, execute by phase, batch related reads/commands, avoid duplicate reads and interactive loops, keep outputs compact, and verify once per phase.
- Low output alone is not a defect. A bounded run that finishes in roughly <=10 steps is usually fine; low output spread across 20+ steps with rising per-turn input is fragmentation and should be treated as context growth first.
- Startup / resume summaries stay tiny: `branch`, `task`, `blocker`, `next step`, and `evidence`.
- Memory-driven starts stay ordered: read active `.omx/state` first, then one live `.omx/notepad.md` handoff, then external memory only when the task depends on prior repo decisions, a previous lane, or ambiguous continuity. Stop after the first 1-2 relevant hits.
- Front-load scaffold/path discovery into one grouped inspection pass. Avoid serial `ls` / `find` / `rg` / `cat` retries that only rediscover the same path state.
- Treat repeated `write_stdin`, repeated `sed` / `cat` peeks, and tiny diagnostic follow-up checks as strong negative signals. If they appear alongside climbing input cost, stop the probe loop and batch the next phase.
- Tool / hook summaries stay tiny: command, status, last meaningful lines only. Drop routine hook boilerplate.
- Treat local edit/commit, remote publish/PR, CI diagnosis, and cleanup as bounded phases. Do not spend fresh narration or approval turns on obvious safe follow-ons inside an already authorized phase unless the risk changes.
- When a session turns fragmented, collapse back to inspect once, patch once, verify once, and summarize once.
- Keep `.omx/notepad.md` lean: live handoffs only. Use exactly `branch`, `task`, `blocker`, `next step`, and `evidence`; move narrative proof into OpenSpec artifacts, PRs, or command output.

## OMX Caveman Style

- Commentary and progress updates use smart-caveman `ultra` by default: drop articles, filler, pleasantries, and hedging. Fragments are fine when they stay clear.
- Answer order stays fixed: answer first, cause next, fix or next step last. If yes/no fits, say yes/no first.
- Keep literals exact: code, commands, file paths, flags, env vars, URLs, numbers, timestamps, and error text are never caveman-compressed.
- Auto-clarity wins: switch back to `lite` or normal wording for security warnings, irreversible actions, privacy/compliance notes, ordered instructions where fragments may confuse, or when the user is confused and needs more detail.
- Boundaries stay normal/exact for code, commits, PR text, specs, logs, and blocker evidence.

**Isolation.** Every task runs on a dedicated `agent/*` branch + worktree. Start with `gx branch start "<task>" "<agent-name>"`. Treat the base branch (`main`/`dev`) as read-only while an agent branch is active. Never `git checkout <branch>` on a primary working tree (including nested repos); use `git worktree add` instead. The `.githooks/post-checkout` hook auto-reverts primary-branch switches during agent sessions - bypass only with `GUARDEX_ALLOW_PRIMARY_BRANCH_SWITCH=1`.
For every new task, including follow-up work in the same chat/session, if an assigned agent sub-branch/worktree is already open, continue in that sub-branch instead of creating a fresh lane unless the user explicitly redirects scope.
Never implement directly on the local/base branch checkout; keep it unchanged and perform all edits in the agent sub-branch/worktree.

**Ownership.** Before editing, claim files: `gx locks claim --branch "<agent-branch>" <file...>`. Before deleting, confirm the path is in your claim. Don't edit outside your scope unless reassigned.

**Handoff gate.** Post a one-line handoff note (plan/change, owned scope, intended action) before editing. Re-read the latest handoffs before replacing others' code.

**Completion.** Finish with `gx branch finish --branch "<agent-branch>" --via-pr --wait-for-merge --cleanup` (or `gx finish --all`). Task is only complete when: commit pushed, PR URL recorded, state = `MERGED`, sandbox worktree pruned. If anything blocks, append a `BLOCKED:` note and stop - don't half-finish.
OMX completion policy: when a task is done, the agent must commit the task changes, push the agent branch, and create/update a PR before considering the branch complete.

**Parallel safety.** Assume other agents edit nearby. Never revert unrelated changes. Report conflicts in the handoff.

**Reporting.** Every completion handoff includes: files changed, behavior touched, verification commands + results, risks/follow-ups.

**Open questions.** If Codex/Claude hits an unresolved question, branching decision, or blocker that should survive chat, record it in `openspec/plan/<plan-slug>/open-questions.md` as an unchecked `- [ ]` item. Resolve it in-place when answered instead of burying it in chat-only notes.

**OpenSpec (when change-driven).** Keep `openspec/changes/<slug>/tasks.md` checkboxes current during work, not batched at the end. Task scaffolds and manual task edits must include an explicit final completion/cleanup section that ends with PR merge + sandbox cleanup (`gx finish --via-pr --wait-for-merge --cleanup` or `gx branch finish ... --cleanup`) and records PR URL + final `MERGED` evidence. Verify specs with `openspec validate --specs` before archive. Don't archive unverified.

**Version bumps.** If a change bumps a published version, the same PR updates release notes/changelog.
<!-- multiagent-safety:END -->
