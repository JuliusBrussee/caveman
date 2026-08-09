# Plan: caveman sync-by-default + opt-in session isolation

## Origin

**Seed:** closing PR #800 (upstream `JuliusBrussee/caveman`) / #1 (fork
`eggrollofchaos/caveman`), "Per-session mode flag isolation." That PR
(13 Tier-1 rounds, independent Tier-2 confirmation, all clean) correctly
implemented **permanent per-session isolation**: any session that ever
wrote a mode (including implicitly, at its own SessionStart) got its own
`.caveman-active-<session_id>` file forever, with no way back to shared
behavior. Mid-review-completion, the requester realized this silently
changes caveman's **default** behavior for everyone from "all sessions
share one toggle" (today's shipped upstream behavior) to "every session is
permanently isolated" — a much bigger, more disruptive default-behavior
change than intended, and not something a maintainer should have to infer
from "a follow-up PR is coming." Decision: close #800/#1 without merging,
with an explanatory note on both, and design this as its own atomic
follow-up.

**Backlog lineage:** none — this is a personal-fork OSS side project with
no unified-ID governance; tracked only via this plan doc.

**Reference context:** upstream's actual pre-scoping behavior, verified via
`git show ec83e5b:src/hooks/caveman-activate.js` (the commit both closed
PRs forked from) — `mode = getDefaultMode(); ...; safeWriteFlag(flagPath,
mode)` unconditionally on every true session start, to the ONE shared
`.caveman-active` file (no session concept existed). This plan's design is
built to be provably faithful to that exact mechanism for the "untouched
session" case, while adding opt-in isolation on top — not a reinvention.

**Branch basis:** this branch (`feat/session-sync-opt-in-isolation`) is
based on the closed PR's final reviewed head (`ff20fd7`, on
`feat/per-session-flag-isolation`), not vanilla `origin/main`. The closed
PR's cross-implementation isolation machinery (`sanitizeSessionId`,
`resolveFlag`/`resolveState`/`resolvePrev`, `flagBaseName`/`prevBaseName`,
the JS/Bash/PowerShell parity, 71 passing tests) is reused as-is — it was
already reviewed clean across 13 Tier-1 rounds and an independent Tier-2
pass, and nothing in this plan's findings changes it. Only
`caveman-activate.js`'s true-startup branch, `caveman-parse.js`'s grammar,
and `caveman-mode-tracker.js`'s action dispatch are touched by this plan.

## Design

Three states a session's flag can be in:

1. **Synced (default)** — no scoped file exists for this session. Every
   read falls through to the shared legacy file (`resolveFlag`'s existing
   ENOENT-fallback already does this — no change needed there). A brand
   new session start refreshes the SHARED file from `getDefaultMode()`,
   exactly matching upstream's existing single-flag mechanism, just never
   touching a scoped path for an untouched session.
2. **Isolated** — session has its own `.caveman-active-<session_id>` file.
   Reached via `/caveman <level>` (unchanged from the closed PR — this part
   was already correct) or `/caveman off` / natural-language deactivation
   (also unchanged, already isolates). Setting a level that happens to
   equal the current shared value still isolates — no comparison logic,
   isolation is unconditional on any explicit `/caveman <level>` call.
3. **Reverting to synced** — new `/caveman default` command. Deletes the
   session's scoped active flag + its scoped `.prev` file. Next read falls
   through to the shared legacy file again, live, exactly like a session
   that was never touched.

**What does NOT change:** `caveman-mode-tracker.js`'s `action === 'set'`
and `action === 'clear'` handlers (isolation on explicit action) — these
already matched the desired design in the closed PR. `resolveFlag`/
`resolvePrev`'s ENOENT-fallback-to-legacy logic — already correct, that's
what makes "synced" work automatically once a session has no scoped file.

**What changes:** `caveman-activate.js`'s true-startup branch (currently
writes to the SCOPED path unconditionally, isolating every fresh session
immediately — this is the actual bug) must write to the LEGACY path
instead when a session has no scoped identity yet. Plus one new parser
action (`/caveman default`) and its handler (delete scoped state).

## Invariant Matrix

**Invariant:** a session with no explicit `/caveman <level>`/`off`/`default`
action in its own lifetime must always read the live shared/legacy value —
never a private snapshot taken at session creation.

| # | Code path | Current (closed PR) | Fixed |
|---|---|---|---|
| 1 | `caveman-activate.js` true startup (`source === 'startup'`) | Writes `getDefaultMode()` to **scoped** path unconditionally — isolates on creation | Writes `getDefaultMode()` to **legacy** path unconditionally (matches upstream exactly) — stays synced |
| 2 | `caveman-activate.js` resume/clear/compact (`source !== 'startup'`) | `resolveFlag` ENOENT-fallback; writes back to `resolved.path` | Unchanged — already correct; clarified as the single write target |
| 3 | `caveman-mode-tracker.js` `/caveman <level>` (`action: 'set'`) | Writes scoped, unconditional | Unchanged |
| 4 | `caveman-mode-tracker.js` `/caveman off` / NL deactivate (`action: 'clear'`) | Writes scoped `'off'` | Unchanged |
| 5 | `caveman-mode-tracker.js` `/caveman default` | Does not exist | New `action: 'reset'` — unlink scoped active flag + scoped `.prev`, log the transition to whatever the legacy value currently resolves to |
| 6 | `caveman-parse.js` grammar | No `default` keyword | `arg === 'default'` (and `/caveman:caveman default`) → `{action: 'reset'}`; must not collide with `VALID_MODES` (it doesn't — `default` is not a mode) |
| 7 | Uninstall enumeration (`cli/install.js`, `uninstall.sh`, `uninstall.ps1`) | Globs `.caveman-active-*` + validates id segment | No change needed — no new filename pattern introduced |
| 8 | `caveman-stats.js` mode-log attribution | Logs `{mode, prev, session_id}` per transition | No change needed — `/caveman default`'s log entry uses existing `recordModeChange` shape |
| 9 | PowerShell / Bash statusline read side | Reads whichever path the JS write side resolved | No change needed — entirely a write-side fix |

## Open design point to surface in review (not resolved here, deliberately)

A synced session only re-reads `getDefaultMode()` at its own TRUE startup.
An already-running synced session's resume/clear/compact reads the legacy
file's *stored* content, not a fresh config resolution — so a config file
edit mid-session-lifetime only propagates once that session's hook next
fires AND another session's true-startup (or nothing, if no new session
starts) has refreshed the shared file. This exactly matches upstream's
existing propagation lag (verified above) — not a new gap.

## Implementation-Ready Checklist

- [ ] `src/hooks/caveman-activate.js`
  - **Task:** Split the true-startup branch from the resume branch more
    explicitly. True startup: `mode = getDefaultMode()`, write target =
    `path.join(claudeDir, flagBaseName(null))` (legacy), regardless of
    `sessionId`. Resume/clear/compact: keep existing `resolveFlag(claudeDir,
    sessionId)` call, write target = `resolved.path` (already correctly
    scoped-or-legacy). `recordModeChange` call keeps passing the real
    `sessionId` in both branches (log attribution, not write target).
  - **Acceptance:** a `source: 'startup'` invocation with `session_id` set
    never creates `.caveman-active-<id>`; it only ever writes/refreshes the
    legacy `.caveman-active`. A `source: 'resume'` invocation on a session
    that previously isolated itself (scoped file exists) still preserves
    that scoped file, unchanged behavior from the closed PR.
- [ ] `src/hooks/caveman-parse.js`
  - **Task:** In the `/caveman`/`/caveman:caveman` branch, recognize
    `arg === 'default'` before the `VALID_MODES.includes(arg)` check,
    returning `{ action: 'reset' }`.
  - **Acceptance:** `parseModeChange('/caveman default', {...})` returns
    `{ action: 'reset' }`; `/caveman defaultx` or any non-exact match still
    falls through to the existing unknown-arg no-op.
- [ ] `src/hooks/caveman-mode-tracker.js`
  - **Task:** Add an `else if (change && change.action === 'reset')` branch
    alongside the existing `set`/`clear` handling. Only act when `sessionId`
    is truthy (a keyless/legacy caller has no scoped identity to revert —
    must be a no-op, not an accidental unlink of the legacy file itself,
    since `flagBaseName(null) === flagBaseName(sessionId)` when
    `sessionId` is falsy). Resolve the legacy value first (for the log),
    then `recordModeChange`, then `fs.unlinkSync` both the scoped active
    flag and scoped `.prev`, each in its own try/catch (matches existing
    unlink-on-clear pattern at the bottom of the `clear` branch).
  - **Acceptance:** running `/caveman default` on an isolated session
    deletes `.caveman-active-<id>` and `.caveman-active-<id>.prev`; the next
    `resolveFlag(claudeDir, sessionId)` call for that session returns the
    legacy value. Running it on an already-synced session (no scoped file)
    is a silent no-op. Running it with no `session_id` at all never touches
    the legacy file.
- [ ] `tests/test_session_scoping.js` (new cases, alongside existing 71)
  - **Task:** (a) true-startup with no legacy file yet → legacy gets seeded
    from `getDefaultMode()`, no scoped file created. (b) true-startup with
    an existing legacy value → legacy gets refreshed (overwritten) from
    `getDefaultMode()` again, matching upstream's unconditional-refresh
    behavior; still no scoped file. (c) `/caveman <level>` still isolates
    (regression guard against re-breaking this). (d) `/caveman default` on
    an isolated session deletes scoped state and falls back to legacy. (e)
    `/caveman default` on an already-synced session is a no-op. (f)
    `/caveman default` with no `session_id` never touches the legacy file.
    (g) setting `/caveman <level>` to a value equal to the current legacy
    value still isolates (no accidental sync-detection).
  - **Acceptance:** full suite passes; new cases specifically distinguish
    "synced, reading legacy live" from "isolated, at any value."
- [ ] Docs (`skills/caveman/SKILL.md`, `plugins/caveman/skills/caveman/SKILL.md`,
  `README.md`)
  - **Task:** Document the three-state model (synced / isolated / revert),
    `/caveman default`'s behavior, and that setting a level matching the
    current default still isolates. These files have zero prior mentions of
    per-session scoping (the earlier PR never merged), so this is new
    documentation, not an update to stale text.
  - **Acceptance:** a reader can determine, from SKILL.md alone, what
    `/caveman default` does and why setting `/caveman lite` while the
    default is already `lite` still isolates.

## Verification

- `node --test tests/test_session_scoping.js` — full suite, including new
  cases above.
- `node tests/test_caveman_parse.js`, `node tests/test_mode_tracker_stdin.js`
  — regression guard for the parser/tracker changes.
- Manual smoke: two `CLAUDE_CONFIG_DIR` sandboxes representing two
  "sessions" (distinct `session_id`s), same `claudeDir` — start both fresh,
  confirm both read the same legacy value; isolate one via `/caveman ultra`,
  confirm the other is unaffected; run `/caveman default` on the isolated
  one, confirm it reverts to tracking the (possibly since-changed) legacy
  value again.
- Full existing suite (`tests/verify_repo.py`, `tests/test_caveman_stats.js`,
  `tests/test_mode_tracker.py`) stays green — no unrelated regressions.

## Review

Same doctrine as the closed PR: Tier-1 via DeepSeek (V4 Pro, current bridge
default), Tier-2 via Kimi (sufficient alone per current
`resources/review-doctrine.md` routing), Antigravity as a bonus
confirmation lane, not blocking. Round-economics trip-wire applies again if
Tier-1 narrows without converging past ~5 rounds.

## New PR description must include

- The motivation: what the closed PR got wrong (default-behavior change),
  and why this is atomic where that wasn't.
- The three-state model, explicitly.
- A link/reference to the closed PRs (#800, #1) for provenance.

## Review Decisions

(populated as review rounds land)
