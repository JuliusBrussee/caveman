# Definite GitHub bulk-close list — 2026-08-19

Repository: [JuliusBrussee/caveman](https://github.com/JuliusBrussee/caveman)

This is the conservative operational list. It supersedes the 202-item recommendation in `GITHUB_OPEN_TRIAGE_2026-08-19.md`.

Exact review-only closure comments: [GITHUB_BULK_CLOSE_RESPONSES_2026-08-19.md](./GITHUB_BULK_CLOSE_RESPONSES_2026-08-19.md).

## Verdict

- Close now, with comments: **143**
- Issues: **63**
- Pull requests: **80**
- Leave open: **349** of the 492-item snapshot
- Executed 2026-08-19: **143/143 comments posted and items closed**, then independently fetched and verified closed.
- Issue state reasons: **47 completed**, **6 duplicate**, **10 not planned**. All **80 PRs** were closed without merge.
- Before closing #476, its Hermes delegated-agent inheritance acceptance case was copied into #672.

All four sets below are disjoint. Items omitted from this file stay open even if the broader inventory labels them `CLOSE`.

## 1. Released implementation on `main` — 112

Each item is explicitly referenced by a commit reachable from local `main`. Every selected reference commit is contained in at least one release tag. This check intentionally used `git log main`, not `git log --all`; that correction removed issue #113 from the close list.

Issues — **41**:

#234, #347, #383, #386, #421, #451, #464, #506, #528, #537, #538, #539, #550, #565, #571, #588, #597, #601, #602, #603, #617, #618, #635, #652, #655, #676, #677, #679, #680, #686, #701, #711, #713, #714, #723, #823, #825, #834, #835, #851, #861.

Pull requests — **71**:

#38, #226, #230, #238, #239, #243, #248, #250, #258, #260, #261, #262, #314, #322, #326, #376, #380, #387, #388, #393, #395, #396, #398, #419, #424, #429, #434, #437, #438, #446, #459, #466, #469, #483, #501, #511, #532, #534, #549, #562, #578, #582, #590, #619, #622, #623, #632, #634, #645, #646, #654, #657, #658, #660, #661, #664, #667, #670, #674, #678, #683, #684, #685, #691, #695, #702, #715, #717, #718, #720, #726.

Comment format:

> Thanks for reporting this. The fix is on `main` in `<commit>` and shipped in `<release tag>`. Closing as implemented. If the current release still reproduces it, reply with version, platform, and command and I’ll reopen.

For PRs, replace first sentence with “Thanks for doing this work.” Never imply the contributor's branch was merged when an equivalent fix landed through another commit.

## 2. True duplicate issues — 6

| Close | Retain | Required action before close |
|---|---|---|
| #111 | #201 | Link local-backend/Ollama request to broader local-provider tracker. |
| #184 | #334 | Link per-session/tmux isolation report. |
| #233 | #303 and #852 | Link long-session mode drift to current reinforcement/evaluation work. |
| #265 | #242 | Link identical multiple-`SKILL.md` installation error. |
| #412 | #374 and #855 | Link skill/plugin payload-overload report. |
| #476 | #672 | Copy Hermes delegated/subagent inheritance as explicit acceptance case, then close. |

Comment format:

> Thanks — keeping this requirement and tracking it in #X. I copied the unique detail there. Closing this duplicate so discussion stays in one place.

## 3. Answered community cleanup — 16

These are not product defects requiring implementation. Post reply first; then close.

| Issue | Type | Reply/action |
|---|---|---|
| #109 | External Tessl evaluation | Thank author for results. Decline proposed ~80-file PR and workspace transfer for now; canonical benchmark claims must remain in committed, reviewable repo harnesses. Keep external results linked in issue. |
| #259 | External workflow/question | State that applying compression indiscriminately can affect quality; do not claim universal equivalence. Redirect external Jira workflow away from issue tracker. |
| #433 | Showcase/promotion | Thank author; decline integration/promotion request; close as community share. |
| #449 | Companion-tool promotion plus criticism | Thank author; decline README companion-tools section. Explicitly say end-to-end savings criticism remains tracked in #727. |
| #457 | External fork/showcase | Thank author; explain separate fork is not core Caveman work; close. |
| #522 | General project questions | Answer briefly or point to current docs/Discussions; close as answered. |
| #526 | Praise | Reply exactly: “Me thank. Glad rock help.” |
| #531 | Showcase/promotion | Thank author; redirect future showcases to Discussions; close. |
| #557 | Praise | Reply exactly: “Me thank. Glad rock help.” |
| #651 | Off-topic prompt collaboration | Redirect to Discussions; close. |
| #757 | External plugin promotion | Thank author; decline tracker-based promotion/integration; close. |
| #761 | Showcase/promotion | Thank author; redirect to Discussions; close. |
| #769 | Showcase/promotion | Thank author; redirect to Discussions; close. |
| #781 | Showcase/promotion | Thank author; redirect to Discussions; close. |
| #846 | Empty/non-reproducible report | Invite fresh report using template, with version/platform/reproduction; close current empty issue. |
| #854 | Attribution/licensing question | Confirm, based on description: `skills/` is MIT; retained MIT/NOTICE attribution is correct; factual nominative reference is allowed; no Caveman logo/brand endorsement implied. Decline README cross-promotion if desired; close as answered. |

Generic showcase reply:

> Thanks for sharing this. I’m keeping the issue tracker focused on Caveman product work, so I’m closing this thread. Discussions is the better place for future showcases.

## 4. Superseded/current-equivalent PRs — 9

These PRs have narrow behavior already present on committed `main`, or are generated sync output with no source change to merge.

| PR | Evidence on committed `main` |
|---|---|
| #394, #404 | OpenCode `caveman-compress` command shipped through #398 / commit `22f75e3`. |
| #486, #493 | OpenCode installer repair shipped through aggregated installer work and release tags. |
| #615 | `spawn-options.js` is shipped in MCP shrink package; clean-main packaging test passes and names #597. |
| #779 | Shared mode parser implements OpenCode toggle parsing; clean-main parser and OpenCode integration tests pass. |
| #838 | Shared parser handles punctuated levels and blocks quoted prose triggers; clean-main test suite has explicit #838 cases. |
| #849 | Missing-sibling degradation is implemented; clean-main suite passes 18 cases. |
| #857 | Generated `dist/caveman.skill` sync only; no independent source fix to merge. |

Comment format:

> Thanks for doing this work. Equivalent behavior is now on `main` via `<commit/current implementation>` and covered by `<test>`, so I’m closing this as superseded, not rejected.

## Clean-main proof

Baseline: local `main` at `f5104c8b2d96d898418f6ba749f485215f093902`. Dirty worktree files were excluded by exporting `git archive main` to a temporary directory.

Passed from that clean archive:

- `node tests/test_caveman_parse.js` — **49 passed, 0 failed**
- `node tests/test_hook_missing_sibling.js` — **18 passed, 0 failed**
- `node tests/test_mcp_shrink.js` — **18 passed, 0 failed**
- `node --test tests/installer/opencode.test.mjs` — **11 passed, 0 failed**

## Explicitly removed from old recommendation

Do **not** bulk-close these based on current evidence:

- Issue #113: reference exists only outside `main`; not shipped proof.
- Issues #279, #428, #479, #687: contain distinct or current unresolved requirements; not safe duplicates.
- PRs #735, #737: may contain useful regression tests absent from `main`.
- PRs #738, #739, #775: requested behavior is not proven present on committed `main`.
- PRs #824, #826: mixed mega-PRs; full equivalence not proven.
- PR #867: requested last-system-message behavior exists only in uncommitted worktree changes; committed `main` still pushes a separate system entry.
- Previous “verified current implementation” wave of 44 issues: evidence was not strong enough for bulk closure without item-by-item acceptance checks.

## Execution guardrail

Post item-specific comment before closure. Use release/commit evidence for implemented items, canonical link for duplicates, and tailored replies for #109, #259, #449, #522, #846, and #854. Close in small batches and stop if a claimed shipped fix cannot be tied to the issue's actual acceptance criteria.
