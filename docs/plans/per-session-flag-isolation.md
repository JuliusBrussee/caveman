---
work-ids: []
---

# Per-Session Caveman Flag Isolation

## Origin

**Seed artifact:** a 2026-07-06 debugging conversation (session `fdbef973`,
ai-coding-agents repo) traced why `/caveman off` kept "coming back on its own."
Root causes found in the then-current hook code: (1) the `SessionStart` hook
(`caveman-activate.js`) unconditionally rewrote the flag from the configured
default on every session start anywhere on the machine, and (2) the flag file
is a single global path (`$CLAUDE_CONFIG_DIR/.caveman-active`), so every
Claude Code session on the machine shares one on/off/level state. The user's
explicit ask that session: *"well, I'd love to have it be on or off per
session."*

**Backlog lineage.** No formal backlog item — this is a personal fork
(`git@github.com:JuliusBrussee/caveman`) of an installed plugin, tracked via
a dedicated feature branch (`feat/per-session-flag-isolation`) rather than a
governed work-item ID. A prior implementation attempt that same session
hand-patched the hook files directly (uncommitted) and was abandoned when
local `main` was later fast-forwarded to upstream; a plan for the same intent
was drafted and sent for Tier-2 cross-agent review on 2026-07-10 (packet
`CAPGATE_20260710_104530_ADHOC_caveman-session-isolation-plan-v1`, target the
now-overwritten plan-mode scratch file `~/.claude/plans/bubbly-foraging-ripple.md`).
That review returned 1 High + 3 Medium findings (non-TTY stdin timeout could
still fall back to the legacy global path; the installer's uninstall used
exact-path removal instead of enumerating scoped variants; statusline
sanitizer-parity testing covered only the JS helpers, not the Bash/PowerShell
scripts; docs describing the flag topology would go stale). No v2 was ever
produced and no code from that attempt survived — `origin/main` fast-forwarded
past it and the feature branch sat at 0 commits.

**Reference context.** The upstream repo has moved substantially since that
review (`0d95a81` → `ec83e5b`, 11 commits, tag `v1.10.0`): `bin/install.js`
renamed to `cli/install.js`, a shared `caveman-parse.js` module extracted from
the tracker hook (#602), and — most relevant here — `caveman-activate.js`
already gained a **synchronous** stdin read for a `source` field (#691,
distinguishing real `startup` from `resume`/`clear`/`compact` re-fires) since
that review ran. That single change removes the exact async-timeout hazard
the v1 review's High finding was about: this plan's stdin handling in
`caveman-activate.js` reuses that already-accepted synchronous, EOF-driven
read rather than introducing a new timer-based one. `caveman-mode-tracker.js`
also gained a `.caveman-active.prev` restore mechanism (#599) and a
mode-transition log `.caveman-mode-log.jsonl` (#601, feeds
`caveman-stats.js` attribution) — both new state files this plan must scope
consistently with the primary flag, or leave deliberately unscoped with a
stated reason (see Non-Goals).

**v2 revision note.** A v1 draft of this plan was sent for Tier-1 headless
review (`docs/plans/per-session-flag-isolation-plan-t1-v1` chain, target
`a521bc7899c083ab3b61d8f8f8ae83ed4b56f5db`) and returned 3 Critical + 4 High
findings, all real design gaps, not false positives:

1. **Critical** — the v1 spec's shell-sanitizer sketch stripped invalid
   characters and truncated instead of rejecting the whole value on any
   mismatch (`../../etc/passwd` would have sanitized to `etcpasswd` and been
   accepted). Fixed below: strict whole-string match, reject on any failure.
2. **Critical** — v1's "absence of the scoped file falls back to legacy" rule
   collided with `/caveman off` deleting the scoped file to represent "off."
   After turning caveman off in one session, that session's next read would
   have found no scoped file and silently fallen back to whatever the legacy
   global file said — possibly another session's active mode. Fixed below:
   "off" is now **written**, not deleted, so a scoped file's mere existence
   (any content) means "this session has touched its own state," and only
   genuine absence (`ENOENT`) triggers fallback.
3. **Critical** — `caveman-stats.js` would have derived a session id from the
   transcript filename unconditionally, even when the hook that invoked it
   had no valid `session_id` and was itself falling back to the legacy path
   — a split-brain where the hook writes legacy but stats reads scoped.
   Fixed below: the tracker threads its own already-sanitized session id
   into the stats subprocess explicitly; stats only derives its own id as a
   fallback for truly session-context-free manual/lifetime invocations.
4. **High** — `readFlag` collapses "missing," "symlink," "oversized," and
   "invalid content" into the same `null` return, so a naive
   `readFlag(scoped) || readFlag(legacy)` would fall back to the legacy file
   even when the scoped file exists but is corrupted or attacker-planted —
   exposing another session's mode instead of failing closed. Fixed below:
   a single shared resolver distinguishes `ENOENT` (genuine fallback case)
   from "exists but rejected" (fail closed, no fallback).
5. **High** — `recordModeChange`'s `current`/`prev` lookup read the
   hardcoded legacy path regardless of which path the caller actually
   writes to, corrupting mode-log `prev` values once flags are scoped. Fixed
   below: it resolves through the same shared resolver.
6. **High** — the mode-log filter (`session_id == null || session_id ===
   thisSessionId`) let *any* concurrent legacy/no-session writer's rows
   bleed into a scoped session's attribution indefinitely, not just
   historical pre-migration rows. Fixed below: only rows with the
   `session_id` key entirely *absent* (true pre-migration history) are
   always included; rows with an explicit `session_id: null` (written by
   this change's own legacy-fallback path) only join a reader that is
   itself currently on the legacy path.
7. **High** — every checklist item named `bin/install.js`, which no longer
   exists (renamed to `cli/install.js` upstream, see Reference context
   above) — a plan-authoring artifact of reading the installer before
   fast-forwarding local `main`. Fixed by replacing every reference.

Two Medium findings (byte-for-byte legacy-compat gate contradicted the new
additive mode-log field; `caveman-stats.js`'s `flagMtimeMs` must stat the
same resolved path attribution reads mode from) and one Nit (an uninstall
near-miss test fixture that actually matched the intended charset) are also
folded into this v2.

**v3 revision note.** v2 (target `82f37cc8790f2df1da3b6416fabe9d99b8e5ccd9`)
cleared all Criticals but returned 5 more High + 2 Medium — precision gaps
in the checklist, not new design flaws:

1. **High** — the checklist told `activate.js`/`mode-tracker.js` to compute
   their READ of current state via the same `flagBaseName` helper used for
   WRITES, which never falls back — a valid session id with no scoped file
   yet, plus an active legacy flag, would read as "inactive" instead of
   inheriting the legacy state. Fixed: reads go through
   `resolveFlag`/`resolvePrev`; writes use `flagBaseName`/`prevBaseName`
   directly. Stated explicitly per call site.
2. **High** — only the reinforcement-emit check was updated to
   `isActiveMode()`; `mode-tracker.js`'s independent-mode `current` capture
   and `prev` restore-decision still treated literal `'off'` as an active
   mode to save/restore, corrupting one-shot state and mode-log entries
   after `/caveman off` → `/caveman-commit` → an ordinary prompt. Fixed:
   both gates use `isActiveMode()` too; a restored `'off'` normalizes to
   `null` in the mode-log, never logged as `'off'` itself.
3. **High** — the mode-log filter's `(row.session_id || null) === (sessionId
   || null)` coerced any falsy-but-present value (`""`, `0`, `false`) to
   `null` via `||`, letting a corrupted row masquerade as a legitimate
   legacy-fallback row. Fixed: strict validation in `readModeLog` — a
   present `session_id` must be `null` or pass `sanitizeSessionId`;
   anything else is `malformed` and excluded from every reader.
4. **High** — the plan only touched `cli/install.js`'s uninstall function;
   two standalone entry points, `src/hooks/uninstall.sh` and
   `uninstall.ps1`, also remove only the exact legacy flag path and were
   missed entirely. Fixed: both gain the same enumeration.
5. **High** — the Acceptance Gates section claimed byte-identical legacy
   flag content while the Phase 2 checklist separately required legacy
   `/caveman off` to write `'off'` instead of unlinking — a direct
   contradiction. Fixed: the gate now states path selection is unchanged
   but the "off" representation change is universal, on the legacy path
   too.

Two Medium findings (the docs task promised `/caveman-stats` reports the
resolved flag path, but the implementation task never required exposing
it; the statusline test plan mostly proved sanitizer decisions, not the
full ENOENT-vs-rejected file-state matrix) are also folded into this v3.

**v4 revision note.** v3 (target `710fff42c49cafef5fdfe6a69bfab6271aa59a92`)
cleared all Criticals AND all prior Highs but returned 3 new High + 2
Medium — narrower precision gaps than v3's, all confirming the deeper
design fixes held:

1. **High** — `caveman-activate.js`'s checklist said `writeFlagPath` is
   used for "every write in this file," but that file also writes
   `.caveman-nudge-shown` (required to stay global per Non-Goals) — the
   wording was ambiguous enough to scope the nudge marker too. Fixed:
   narrowed to "every write to the active-mode flag specifically," with an
   explicit "do not touch `nudgeMarkerPath`" callout and a matching
   acceptance check.
2. **High** — `recordModeChange` still compared the raw (non-normalized)
   `current`/`newMode` values, so a resolved `current === 'off'` compared
   against `next === null` would still log a spurious `{mode: null, prev:
   'off'}` transition, even with every call-site-level `isActiveMode` fix
   from v3 in place. Fixed: `isActiveMode` normalization moved INSIDE
   `recordModeChange` itself — single source of truth, no call site can
   get it wrong, and callers no longer need to pre-normalize before calling
   it.
3. **High** — the plan only named `tests/test_mode_tracker_stdin.js` for
   new coverage, missing that pre-existing tracked tests
   (`tests/test_mode_tracker.py`, twelve assertions; `tests/verify_repo.py`,
   three assertions) assert the OLD unlink-on-off behavior this plan
   intentionally changes — they'd fail under the whole-plan "full test
   suite passes" gate. Fixed: added an explicit checklist item enumerating
   every assertion that needs updating.
4. **Medium** — Non-Goals claimed `.caveman-active.prev` (legacy name) is
   an untouched, deferred uninstall gap, directly contradicting Phase 5's
   own enumeration regex (which matches it) and test fixture (which
   requires its removal). Fixed: narrowed the deferred list to the
   genuinely untouched files (mode-log, history, statusline-suffix).
5. **Medium** — the stats section claimed manual/lifetime invocations are
   preserved "byte-for-byte," directly contradicted by the same plan
   requiring a new `Flag file:` output line universally. Fixed: the claim
   is now scoped to path selection and attribution semantics, not literal
   output bytes.

**v5 revision note.** v4 (target `32005d901a0707ff575b0553d40bcaf441c38679`)
cleared every prior finding at every severity but returned 3 new High —
this round found genuinely subtle, distinct issues, not repeats:

1. **High** — a v3 draft defined `resolvePrev` as a bare
   `resolveState(..., prevBaseName)` alias, symmetric with `resolveFlag` —
   but WRONG for `.prev` specifically: a session that already has scoped
   ACTIVE state (e.g. explicitly turned off before its first
   `/caveman-commit`) but no scoped `.prev` yet would still `ENOENT` on the
   scoped `.prev` and fall back to a stale LEGACY `.prev`, leaking another
   session's or caller's previous mode. Fixed: `resolvePrev` now gates its
   own legacy fallback on whether the session's scoped ACTIVE flag exists
   at all — once it does, `.prev` absence means "no prev," never "check
   legacy."
2. **High** — `resolveState`/`resolveFlag` returned `mode: null` for BOTH
   "genuinely never touched" (ENOENT) and "touched but rejected"
   (symlink/oversized/corrupted), and `caveman-activate.js`'s
   resume-preserve check couldn't tell them apart — a rejected scoped file
   on resume fell through to `getDefaultMode()` instead of staying
   inactive, contradicting the plan's own fail-closed principle. Fixed:
   `resolveState` now also returns `rejected: true/false`; resume
   explicitly resolves to `'off'` when `rejected` is true, never the
   configured default.
3. **High** — the v3/v4 test-update checklist claimed "twelve"
   `assertIsNone(self.flag_value())` assertions in `test_mode_tracker.py`
   all need to change to expect `off`, but three of the actual thirteen
   occurrences (lines 107, 109, 113) assert "never activated," not
   "deactivated," and must stay asserting absence — following the v4
   wording literally would have broken those three tests. Fixed: every one
   of the thirteen occurrences is now individually classified by line
   number, verified against the actual file content.

## Problem

Every Claude Code session on the machine reads and writes the same handful of
global files under `$CLAUDE_CONFIG_DIR`:

| File | Written by | Read by |
|---|---|---|
| `.caveman-active` | `caveman-activate.js`, `caveman-mode-tracker.js` | both, `caveman-stats.js`, `caveman-statusline.sh`/`.ps1` |
| `.caveman-active.prev` | `caveman-mode-tracker.js` | `caveman-mode-tracker.js` |
| `.caveman-mode-log.jsonl` | `caveman-config.js:recordModeChange` (called from both hooks) | `caveman-stats.js` |

A user running two agent sessions concurrently (the common case for this
user — confirmed during the 2026-07-06 session, where a second agent's
`/caveman:caveman stop` cleared the flag for a session that never asked to be
turned off) cannot have caveman on in one and off in another. Worse, once
this plan scopes the *toggle* per session, the *stats attribution* log
becomes actively wrong more often than today: `.caveman-mode-log.jsonl` has
no `session_id` field, so `caveman-stats.js`'s timestamp-window join
(`attributeByMode`) can pull in a mode-change row from a concurrent session
that happens to fall inside the current session's message time range.

## Design

### Session-id resolution

Claude Code hook payloads (`SessionStart`, `UserPromptSubmit`, and the
`statusLine` command's stdin) all carry a `session_id` field. This plan takes
that hook-payload field as the sole authoritative source in every consumer —
including `caveman-stats.js`, which now receives it explicitly from the
tracker rather than re-deriving anything (see v2 finding 3 above).

A `session_id` value is untrusted external input feeding a filesystem path
(same class of concern the existing symlink hardening in `caveman-config.js`
already treats seriously). Add a strict allowlist sanitizer to
`caveman-config.js` that **rejects the whole value on any mismatch** — no
character stripping, no truncation:

```js
const SESSION_ID_RE = /^[A-Za-z0-9_-]{1,128}$/;
function sanitizeSessionId(raw) {
  return typeof raw === 'string' && SESSION_ID_RE.test(raw) ? raw : null;
}
```

`SESSION_ID_RE.test()` requires the **entire** string to match (JS regex
`test()` without anchors would allow a substring match, but `^...$` anchors
already present here force a whole-string match — the v1 mistake was in the
Bash/PowerShell ports, not this JS version, and is fixed in those sections
below by using the equivalent whole-string match idiom in each language,
never strip-then-truncate).

Any session id that fails this check (missing field, wrong type, empty
string, path-traversal characters, oversized) is treated identically to "no
session id" — see fallback below. This also naturally covers every caller
that never sends `session_id` at all: `opencode`'s plugin, the Gemini CLI
extension, and a manual `node caveman-activate.js < /dev/null` invocation
that pipes no JSON. None of those are in scope for this change (see
Non-Goals) and all continue to get the pre-existing global-file behavior
unmodified.

### Scoped file naming

```js
function flagBaseName(sessionId) {
  return sessionId ? `.caveman-active-${sessionId}` : '.caveman-active';
}
function prevBaseName(sessionId) {
  return sessionId ? `.caveman-active-${sessionId}.prev` : '.caveman-active.prev';
}
```

### "Off" is written, never deleted (fixes v2 finding 2)

Today, "off" is represented by the flag file's *absence* — every "clear"
path (`caveman-activate.js`'s `mode === 'off'` branch,
`caveman-mode-tracker.js`'s `wantsOff`/`change.action === 'clear'` branches)
calls `fs.unlinkSync(flagPath)`. Once the flag is scoped, "absence" becomes
ambiguous between "this session has never touched its own state yet" (should
fall back to legacy) and "this session explicitly turned off" (should NOT
fall back — off means off). Both cases need the same on-disk signal to
disambiguate, so this plan changes the representation: **every "clear" path
now writes the literal string `off` via `safeWriteFlag(flagPath, 'off')`
instead of unlinking.** `'off'` is already a member of `VALID_MODES` in
`caveman-config.js` (used today to validate the *config file's* `defaultMode`
field, never previously written to the flag itself), so `readFlag` already
accepts it without any whitelist change.

This changes what "truthy flag content" means everywhere the flag is read
for an active-mode check — every such check must now explicitly exclude
`'off'`, not just check truthiness. Add one helper for this and use it
everywhere instead of ad-hoc `if (activeMode)`:

```js
function isActiveMode(mode) {
  return !!mode && mode !== 'off';
}
```

Consumers needing this exclusion: `caveman-mode-tracker.js`'s reinforcement
emit check, and the statusline scripts' render logic (both must render
nothing — no `[CAVEMAN:OFF]` badge — for a resolved mode of `'off'`, matching
today's behavior for an absent flag).

### Shared resolver — ENOENT vs rejected content, generalized to flag AND prev (fixes v2 findings 2 + 4; fixes v3 finding 1)

One pair of functions in `caveman-config.js`, used by every consumer that
**reads** current state (`activate.js`'s resume-preserve check,
`mode-tracker.js`'s `current`/`activeMode`/`prev` reads, `stats.js`, and
reimplemented with equivalent semantics in both statusline scripts). A v2
draft specified this resolver but the Implementation-Ready Checklist still
told `activate.js`/`mode-tracker.js` to compute their READ path via the same
`flagBaseName` helper used for WRITES — which never falls back at all
(Tier-1 v2 High finding: a valid session id with no scoped file yet and an
active legacy flag would read as "inactive" instead of correctly inheriting
the legacy state). This v3 makes the read/write split explicit and names
which helper each call site uses:

```js
function resolveState(claudeDir, sessionId, baseNameFn) {
  const legacyPath = path.join(claudeDir, baseNameFn(null));
  if (!sessionId) {
    return { path: legacyPath, mode: readFlag(legacyPath), rejected: false };
  }
  const scopedPath = path.join(claudeDir, baseNameFn(sessionId));
  let st;
  try {
    st = fs.lstatSync(scopedPath);
  } catch (e) {
    if (e.code === 'ENOENT') {
      // Never touched by scoped logic yet -> legacy fallback.
      return { path: legacyPath, mode: readFlag(legacyPath), rejected: false };
    }
    // Any other stat error (permission, etc.) -> fail closed. Never fall back.
    return { path: scopedPath, mode: null, rejected: true };
  }
  // Scoped path exists in SOME form. readFlag re-validates symlink/size/
  // whitelist and returns null for anything unsafe or invalid -- that null
  // is a fail-closed "inactive," NOT a signal to fall back to legacy. Track
  // this distinctly as `rejected` (v4 finding 2 -- see below) so a caller
  // can tell "genuinely never touched, use the default" apart from
  // "something's here and it's bad, stay inactive."
  const mode = readFlag(scopedPath);
  return { path: scopedPath, mode, rejected: mode === null };
}
function resolveFlag(claudeDir, sessionId) { return resolveState(claudeDir, sessionId, flagBaseName); }
```

`mode` is `null` for "inactive" (including the fail-closed cases above) or a
`VALID_MODES` string including `'off'` (see `isActiveMode` above to collapse
`'off'` into "inactive" for callers that only care about active/inactive).
Returning `{ path, mode, rejected }` (not just `mode`) lets
`caveman-stats.js` stat the *same* path it read mode from (fixes the Medium
finding about `flagMtimeMs` following a different path than `mode`) and lets
resume logic distinguish "never touched" from "touched but rejected" (v4
finding 2, below).

Because "off" is now written rather than deleted, a session that has
explicitly turned off has a scoped file that exists with content `'off'` —
`lstatSync` succeeds, `readFlag` returns `'off'`, and the function correctly
returns the scoped path with mode `'off'`, never falling through to legacy.
Only a session that has *never* written to its scoped path at all (true
`ENOENT`) falls back — resolving the v1 ambiguity precisely.

**`resolvePrev` is state-aware, not a bare alias for `resolveState` with
`prevBaseName` (fixes v4 finding 1).** A v3 draft defined `resolvePrev` as
`resolveState(claudeDir, sessionId, prevBaseName)` — symmetric with
`resolveFlag`, but WRONG: `.prev`'s legacy-fallback should only apply while
this session has never scoped ANYTHING, not independently of whether the
session already has scoped identity. Tier-1 v4 review found the reachable
leak: a session that is already scoped (has a scoped ACTIVE flag — e.g. it
was just turned explicitly off before its first `/caveman-commit`, or it
simply never had a prior prose mode) but has no scoped `.prev` file yet
would still `ENOENT` on the SCOPED `.prev` path and fall back to the LEGACY
`.prev` — restoring a stale, unrelated previous mode from another session or
a legacy caller into a session that has its own, already-established
identity. The fix: gate `.prev`'s legacy fallback on whether the session's
scoped ACTIVE flag exists at all — if it does, `.prev` ENOENT means "this
session genuinely has no prev," full stop, never fall back:

```js
function resolvePrev(claudeDir, sessionId) {
  if (!sessionId) {
    const legacyPrevPath = path.join(claudeDir, prevBaseName(null));
    return { path: legacyPrevPath, mode: readFlag(legacyPrevPath), rejected: false };
  }
  const scopedActivePath = path.join(claudeDir, flagBaseName(sessionId));
  const sessionHasScopedIdentity = fs.existsSync(scopedActivePath);
  if (!sessionHasScopedIdentity) {
    // This session has never scoped anything (including its own active
    // flag) -- consistent with resolveFlag's own legacy fallback story.
    return resolveState(claudeDir, sessionId, prevBaseName);
  }
  // Session already has scoped identity -- .prev is scoped-only from here
  // on. ENOENT means "no prev for this session," never a signal to
  // consult a legacy .prev that belongs to a different session/caller.
  const scopedPrevPath = path.join(claudeDir, prevBaseName(sessionId));
  let st;
  try {
    st = fs.lstatSync(scopedPrevPath);
  } catch (e) {
    return { path: scopedPrevPath, mode: null, rejected: false };
  }
  const mode = readFlag(scopedPrevPath);
  return { path: scopedPrevPath, mode, rejected: mode === null };
}
```

**The read/write split, stated explicitly (this is the exact fix for v3
finding 1):**

- **Writes always target `path.join(claudeDir, flagBaseName(sessionId))`
  (or `prevBaseName(sessionId)` for `.prev`) directly — no resolver call.**
  A write establishes THIS session's own state; it never needs to know
  where the legacy file is.
- **Reads of "what is the currently active mode/prev for this session"
  always go through `resolveFlag`/`resolvePrev`.** This includes:
  `caveman-activate.js`'s resume-preserve check (`source !== 'startup'`
  branch); `caveman-mode-tracker.js`'s `current` capture before an
  independent-mode override, its `activeMode` read for the reinforcement
  decision, and its `prev` read during one-shot independent-mode restore;
  and `caveman-stats.js`'s flag read.

### "Off" truthiness — every call site, not just reinforcement (fixes v3 finding 2)

A v2 draft updated only `caveman-mode-tracker.js`'s reinforcement-emit check
to use `isActiveMode()`. Tier-1 v3 review found two more truthiness checks on
mode-like values in the same file that also treat literal `'off'` as if it
were an active prose mode to save/restore:

1. **Current-mode capture before an independent-mode override**
   (`if (current && !INDEPENDENT_MODES.has(current)) { safeWriteFlag(prevPath, current); }`).
   With `current === 'off'` this saves `'off'` into `.prev` as if it were a
   real mode to restore later. Fix: gate on `isActiveMode(current)` instead
   of bare truthiness — an off/inactive current mode has nothing worth
   saving.
2. **One-shot independent-mode restore decision**
   (`if (prev && !INDEPENDENT_MODES.has(prev)) { ...restore prev as active... }
   else { ...deactivate... }`). With `prev === 'off'` this takes the
   "restore as active" branch and calls `recordModeChange(claudeDir, prev,
   sessionId)` — logging `'off'` as if it were a distinct mode value
   instead of the same thing as `null`/deactivated. Fix: gate on
   `isActiveMode(prev)`; when it's false (including `prev === 'off'`), take
   the SAME branch as "no prev to restore" — `recordModeChange(claudeDir,
   null, sessionId)` and write `'off'` (not unlink) to the flag, so the
   mode-log always records `null` for an inactive/off state regardless of
   which code path produced it.

The one truthiness check that does NOT need this treatment:
`if (activeMode && INDEPENDENT_MODES.has(activeMode))` (deciding whether
THIS turn is a one-shot independent-mode turn) — `INDEPENDENT_MODES.has('off')`
is already `false`, so this condition is correct as bare truthiness
regardless of `isActiveMode`.

### Mode-transition log — add `session_id`, keep one shared file (fixes v2 findings 5 + 6; fixes v3 finding 2)

`recordModeChange(claudeDir, newMode, sessionId)` (new third parameter)
resolves `current` via `resolveFlag(claudeDir, sessionId).mode` — the SAME
path/logic every other consumer uses — instead of the current hardcoded
`readFlag(path.join(claudeDir, '.caveman-active'))`.

**`isActiveMode` normalization lives INSIDE `recordModeChange`, once, not at
every call site (fixes v3 finding 2).** A v3 draft's "off truthiness" fixes
normalized `'off'` to `null` at each call site in `caveman-mode-tracker.js`
before calling `recordModeChange`, but `recordModeChange` itself still
compared the raw resolved `current` (which can legitimately be `'off'`, not
normalized, when read from disk) against the raw `newMode` argument. Tier-1
v3 review found the gap this leaves: a call site that resolves `current` as
`'off'` and passes `newMode: null` (e.g. a resume/re-fire path, or any
future call site that doesn't happen to pre-normalize) sees
`(current || null) === next` evaluate `'off' !== null` (since `'off'` is
truthy) and logs a spurious `{ mode: null, prev: 'off' }` transition row for
what is semantically a no-op (off → off). Fixed: both `current` and
`newMode` are normalized through `isActiveMode` INSIDE `recordModeChange`,
so callers never need to think about it and no call site can get it wrong:

```js
function recordModeChange(claudeDir, newMode, sessionId) {
  const rawCurrent = resolveFlag(claudeDir, sessionId).mode;
  const current = isActiveMode(rawCurrent) ? rawCurrent : null;
  const next = isActiveMode(newMode) ? newMode : null;
  if (current === next) return; // semantically unchanged (off/null collapse to the same state)
  appendFlag(logPath, JSON.stringify({ ts: Date.now(), mode: next, prev: current, session_id: sessionId || null }));
}
```

This also simplifies every caller in `caveman-activate.js` and
`caveman-mode-tracker.js`: they can pass whatever mode value they actually
resolved (including a literal `'off'`) into `recordModeChange` without
pre-normalizing it themselves — the function's own internal normalization
is the single source of truth, so a future call site added without knowing
about `isActiveMode` still can't log a spurious `'off'`-vs-`null`
transition.

It appends `{ ts, mode: next, prev: current, session_id: sessionId || null
}`. The log stays a single shared append-only file (`appendFlag`'s
`O_APPEND` already makes concurrent writers safe); fragmenting it per
session would complicate `aggregateHistory`'s cross-session lifetime
rollup for no benefit.

`readModeLog` in `caveman-stats.js` gains a `session_id` field on each
returned row, additionally tracking whether the **key was present at all**
in the source JSON (not just its value) — this distinguishes two cases that
must be handled differently:

- **Key entirely absent** (every row written before this change shipped) —
  true historical data with no session information available. Always
  included for every reader, exactly matching today's behavior (nothing
  regresses for existing history).
- **Key present with value `null`** (written by this change's own
  legacy-fallback path, i.e. a hook that itself had no valid `session_id`)
  — only included for a reader that is *itself* currently resolving to the
  legacy path (its own `sessionId` is also falsy). A properly scoped reader
  (valid `sessionId`) excludes these, since they represent other
  concurrent/legacy activity that isn't this session's.
- **Key present with any other value** (empty string, a number, `false`, or
  a string that fails `sanitizeSessionId`) — a v2 draft's `(row.session_id
  || null) === (sessionId || null)` comparison silently coerced any of
  these falsy-but-not-null values (`""`, `0`, `false`) to `null` via `||`,
  letting a corrupted row masquerade as a legitimate legacy-fallback row.
  Tier-1 v3 review flagged this as High. Fixed: validate every present
  `session_id` value strictly in `readModeLog` itself — it must be either
  JSON `null` or a string that passes `sanitizeSessionId`; anything else is
  a malformed row and is **excluded from every reader's attribution**
  (never silently coerced into matching), while still being counted so a
  future debugging pass can find it.

```js
// In readModeLog, per parsed row `e`:
const hasSessionIdKey = Object.prototype.hasOwnProperty.call(e, 'session_id');
let sessionIdValue;   // sanitized string, null (valid legacy marker), or undefined (malformed)
if (!hasSessionIdKey) {
  sessionIdValue = undefined; // handled as "no key" below, not "malformed"
} else if (e.session_id === null) {
  sessionIdValue = null;
} else {
  sessionIdValue = sanitizeSessionId(e.session_id); // null return here means MALFORMED, not "legacy" -- see filter below
}
// row = { ts, mode, prev, hasSessionIdKey, session_id: sessionIdValue, malformed: hasSessionIdKey && e.session_id !== null && sessionIdValue === null }

function relevantModeLogRows(modeLog, sessionId) {
  return modeLog.filter(row => {
    if (row.malformed) return false;             // corrupted session_id value -- never join anything
    if (!row.hasSessionIdKey) return true;        // pre-migration historical row
    return row.session_id === (sessionId || null);
  });
}
```

### Stats — session id comes from the hook, not re-derived (fixes v2 finding 3)

`caveman-mode-tracker.js`'s `execFileSync` call to `caveman-stats.js` (for
`/caveman-stats`) now passes `--session-id <id>` alongside the existing
`--session-file <transcript_path>`, using the SAME `sessionId` the tracker
already resolved (and sanitized) from its own hook payload — not a fresh
derivation.

In `caveman-stats.js`'s `main()`: when `--session-id` is supplied, sanitize
it again (defense in depth — trust nothing across a process boundary) and
use it for `resolveFlag` and the mode-log filter. When `--session-id` is
**not** supplied (a direct `node caveman-stats.js` run, `--all`/`--since`
lifetime aggregation, or any older caller that only passes
`--session-file`), `sessionId` is `null` — `resolveFlag` reads the legacy
path exactly as every pre-this-change invocation always has: **path
selection and attribution semantics for the no-session case are
unchanged.** (A v3 draft over-claimed this as byte-for-byte OUTPUT
identity, which directly contradicted the new `Flag file:` output line
required just below — every invocation's rendered text changes now,
scoped or not. Fixed: the compatibility claim is scoped to path
selection/attribution, not literal output bytes.) This fixes the
split-brain the Critical finding identified: a hook falling back to legacy
(invalid or missing `session_id`) now passes an empty/omitted
`--session-id`, so stats falls back to legacy too, matching the hook
exactly.

The pre-existing `sessionId = path.basename(sessionFile, '.jsonl')`
computation at the lifetime-history append site is UNCHANGED and serves a
different purpose (a stable per-session key for
`.caveman-history.jsonl`'s cross-session rollup) — this plan does not touch
that line or that file.

**Stats now exposes the resolved flag path (fixes v3 Medium finding).** The
docs task below promises "`/caveman-stats` reports the resolved session
file," but a v2 draft's `caveman-stats.js` task only required using
`resolveFlag`'s path for `flagMtimeMs` — it never actually threaded that path
into the user-visible output, so the docs promise wasn't backed by an
implementation requirement. Fixed: `resolveFlag`'s `path` is passed into
`formatStats`/`formatShare` and rendered in the output (e.g. a `Flag file:`
line), for all three outcomes — scoped, legacy-fallback, and fail-closed
(where it still shows the scoped path even though `mode` is `null`, so a
user debugging a rejected/corrupted scoped file can see exactly which path
was rejected).

### Statusline scripts

Both `caveman-statusline.sh` and `caveman-statusline.ps1` currently read no
stdin at all — they only ever look at the fixed global flag path. Claude Code
invokes a `statusLine` command with the same JSON-on-stdin contract as other
hooks (confirmed via the sibling `ai-coding-agents/claude/statusline.sh`,
which already reads `.session_id` from this exact payload shape). Both
scripts gain: read stdin, extract `session_id` with a plain string match (no
`jq` dependency — matches the existing dependency-free posture of
`caveman-statusline.sh`), validate it with a **whole-string anchored match —
reject entirely on any non-match, never strip characters or truncate** (this
is the v1 Critical fix — see Invariant Matrix), and resolve the same
ENOENT-vs-rejected fallback semantics as `resolveFlag` above. A resolved mode
of `'off'` renders nothing, matching `isActiveMode`.

**Test coverage must exercise the full file-state matrix, not just the
sanitizer (fixes v3 Medium finding).** A v2 draft's statusline test plan
mostly proved sanitizer accept/reject decisions. Tier-1 v3 review pointed out
this doesn't prove the ENOENT-vs-rejected fallback logic itself is correctly
ported to Bash/PowerShell: a naive "scoped-file-exists ? read scoped :
read legacy" implementation (missing the reject-vs-ENOENT distinction) would
pass every sanitizer test while still leaking a legacy mode through a
corrupted scoped file. Phase 4's shared test vectors are extended with a
**file-state matrix**, each case run with an active, distinguishable legacy
sentinel value present so a wrong fallback is observable:
`scoped-ENOENT` (must read legacy sentinel), `scoped-valid-content`
(must read scoped, ignore legacy sentinel), `scoped-off-content` (must
render nothing), `scoped-invalid-content` (garbage bytes — must render
nothing, must NOT read legacy sentinel), `scoped-oversized` (>64 bytes —
same), `scoped-symlink` (points at an arbitrary file — same), and, where the
platform allows constructing it, a non-`ENOENT` stat failure (e.g. a
permission-denied scoped path) — same fail-closed expectation.

### Uninstall enumeration

Three separate uninstall entry points exist and all currently unlink only
the exact literal `.caveman-active` path: `cli/install.js`'s uninstall
function (`configDir` case; the opencode `ocFlag` case is out of scope, see
Non-Goals), and the two standalone hook uninstallers
`src/hooks/uninstall.sh` and `src/hooks/uninstall.ps1` (used for a
standalone, non-plugin hook-only install — a v2 draft covered only the
first and missed these two, which Tier-1 v3 review flagged as High: without
fixing them too, a standalone install's uninstall leaves every scoped flag
and `.prev` file behind, and a later reinstall can resurrect a stale
per-session mode).

All three gain the same enumeration: list the config directory's entries
once and remove every name matching
`^\.caveman-active(-[A-Za-z0-9_-]{1,128})?(\.prev)?$` — `fs.readdirSync` +
a JS regex test in `cli/install.js` (real directory enumeration, not a shell
glob — Node's `fs` APIs don't expand `*`); `find "$CLAUDE_DIR" -maxdepth 1
-name '.caveman-active*'` filtered through an equivalent shell pattern
test (not a bare glob, to enforce the same charset/length bound) in
`uninstall.sh`; `Get-ChildItem` + a PowerShell regex `-match` filter in
`uninstall.ps1`. **This regex's optional `(-[A-Za-z0-9_-]{1,128})?` group
means it matches the LEGACY (non-scoped) `.caveman-active.prev` too** — a v3
draft's Non-Goals section claimed that file was an untouched pre-existing
gap, which directly contradicted this enumeration and Phase 5's own test
fixture (which requires `.prev` removal). Fixed: the legacy `.prev` file IS
now cleaned up as a natural consequence of this enumeration; only
`.caveman-mode-log.jsonl`, `.caveman-history.jsonl`, and
`.caveman-statusline-suffix` remain the genuinely deferred, unrelated
uninstall gap (see Non-Goals).

## Non-Goals (explicit scope boundaries)

- **`opencode` native plugin** (`src/plugins/opencode/plugin.js`) — different
  runtime, own `session.created` lifecycle hook, no `CLAUDE_CONFIG_DIR`
  contract. Left on today's global-flag behavior. Follow-up, not this PR.
- **`.caveman-statusline-suffix`** (lifetime savings display string) — stays
  global. It's an aggregate informational display, not mode-toggle behavior;
  scoping it per-session would show "no savings yet" on every fresh session,
  which is a UX regression for a purely cosmetic figure.
- **`.caveman-nudge-shown`** (one-shot statusline-setup nudge marker) — stays
  global by design; it's meant to fire once ever, not once per session.
- **Regenerating `src/hooks/checksums.sha256`** — that manifest verifies
  files downloaded from the pinned release tag (`PINNED_REF` in
  `cli/install.js`, currently `v1.10.0`) for the curl\|bash / `npx` remote
  install path. Per the existing top-of-file comment, it's regenerated at
  release-cut time, after this change is merged and tagged — not part of
  this PR's diff.
- **Fixing the pre-existing incomplete uninstall of UNRELATED state files**
  — `.caveman-mode-log.jsonl`, `.caveman-history.jsonl`, and
  `.caveman-statusline-suffix` are never removed by any of the three
  uninstall entry points today, and this plan does not add that. (The
  legacy `.caveman-active.prev` is NOT in this deferred list — the Phase 5
  enumeration's regex naturally matches and removes it, since its charset
  after `.caveman-active` is identical to the scoped-`.prev` pattern; a v3
  draft incorrectly listed it here, contradicting the enumeration and
  Phase 5's own test fixture. Fixed.)

## Invariant Matrix

**Invariant:** every reader of the caveman mode state resolves the SAME flag
path and mode value (via `resolveFlag`'s ENOENT-vs-rejected semantics,
scoped-if-valid-session-id else legacy global) as every writer for that same
session — no split-brain where a hook writes to one path/representation and
a statusline or stats reader looks at a different one or a different
fallback rule.

| Path | Role | Conformance |
|---|---|---|
| `src/hooks/caveman-config.js` | defines `sanitizeSessionId`, `flagBaseName`, `prevBaseName`, `isActiveMode`, `resolveFlag`; `recordModeChange` gains `sessionId` param and resolves `current` via `resolveFlag` | needs-change |
| `src/hooks/caveman-activate.js` | writer (SessionStart, incl. "off" write instead of unlink) + reader (resume-preserve check) | needs-change |
| `src/hooks/caveman-mode-tracker.js` | writer (incl. "off" write instead of unlink) + reader (reinforcement, one-shot independent-mode restore) + passes `--session-id` to the stats subprocess | needs-change |
| `src/hooks/caveman-stats.js` | reader (flag + mode-log join) via explicit `--session-id`, falls back to legacy when absent; `flagMtimeMs` stats the SAME path `resolveFlag` returned | needs-change |
| `src/hooks/caveman-statusline.sh` | reader, same ENOENT-vs-rejected + `isActiveMode`("off" -> nothing) semantics | needs-change |
| `src/hooks/caveman-statusline.ps1` | reader, same semantics | needs-change |
| `cli/install.js` (uninstall, `configDir` case) | deletes state files | needs-change (enumerate) |
| `cli/install.js` (uninstall, opencode `ocFlag` case) | deletes state files | conforms (opencode explicitly out of scope, unlink stays exact-path) |
| `src/hooks/uninstall.sh` | standalone hook-only uninstaller, deletes state files | needs-change (enumerate) — missed in v2, found by Tier-1 v3 review |
| `src/hooks/uninstall.ps1` | standalone hook-only uninstaller (Windows), deletes state files | needs-change (enumerate) — same v3 finding |
| `src/plugins/opencode/plugin.js` | writer + reader (opencode) | out of scope — legacy/alternate path, left conforming to its OWN existing (global) contract, tested only to confirm this PR doesn't touch it |
| `CLAUDE.md`, `src/hooks/README.md`, `INSTALL.md` | documentation of the flag topology | needs-change |
| `tests/test_symlink_flag.js`, `tests/test_repo_local_config.js` | exercise `caveman-config.js` helpers | test-only — extend for new helpers |

Legacy/alternate-path test required by the matrix: a hook invocation with NO
`session_id` (or an invalid one) must produce the SAME files and paths as
today — writes and reads the legacy global `.caveman-active` — proving the
fallback isn't just documented but actually exercised. Two intentional,
documented deviations from strict byte-identity apply on this path too (not
just the scoped path): the mode-log row gains an additive `session_id: null`
field, and an "off" resolution now writes literal `off` content instead of
unlinking the file (see the Acceptance Gates wording below — a v2 draft's
gate contradicted this second point by claiming full byte-identity while
also requiring the legacy "off" behavior change; fixed).

## Implementation-Ready Checklist

### Phase 1 — `caveman-config.js` foundation

- [ ] `src/hooks/caveman-config.js`
  - **Task:** add `SESSION_ID_RE`, `sanitizeSessionId(raw)`, `flagBaseName(sessionId)`,
    `prevBaseName(sessionId)`, `isActiveMode(mode)`, `resolveState(claudeDir,
    sessionId, baseNameFn)` (returns `{ path, mode, rejected }`),
    `resolveFlag(claudeDir, sessionId)` (= `resolveState(..., flagBaseName)`).
    **`resolvePrev` is NOT a bare `resolveState(..., prevBaseName)` alias —
    it additionally gates legacy fallback on whether the session's scoped
    ACTIVE flag already exists** (per Design above — fixes v4 finding 1:
    a v3 draft's symmetric alias let a session with scoped active state but
    no scoped `.prev` fall back to a stale legacy `.prev`, leaking another
    session's/caller's previous mode). Change `recordModeChange(claudeDir,
    newMode)` to `recordModeChange(claudeDir, newMode, sessionId)`: resolve
    `rawCurrent` via `resolveFlag(claudeDir, sessionId).mode`, normalize both
    `rawCurrent` and `newMode` through `isActiveMode` before comparing/
    logging (Design above — fixes v3 finding 2/v4 finding 2's normalization
    gap), append `session_id: sessionId || null` to the JSON row. Export all
    new functions.
  - **Acceptance:** existing `tests/test_symlink_flag.js` and
    `tests/test_repo_local_config.js` still pass unmodified (backward-compatible
    signature — `sessionId` is a new optional third arg). New unit coverage
    (`tests/test_session_scoping.js`, Phase 4) proves: `sanitizeSessionId`
    accepts a UUID-shaped string and rejects `../etc`, `""`, `null`, a
    200-char string, and a non-string, with NO partial/stripped acceptance;
    `flagBaseName`/`prevBaseName` return the legacy name for `null`/`undefined`
    and the scoped name otherwise; `resolveFlag` returns legacy on ENOENT,
    fails closed (`mode: null`, `rejected: true`, scoped path) on a
    symlinked or oversized scoped file, and returns the scoped path+mode
    (including `'off'`) once that scoped file exists in any form;
    `resolvePrev` specifically — with a scoped ACTIVE flag present but no
    scoped `.prev` file, and a legacy `.prev` present with a DIFFERENT
    value — returns `{ mode: null, rejected: false }` for the scoped `.prev`
    path, NEVER the legacy value (this is the exact v4 finding 1 regression
    test); `isActiveMode` returns `false` for `null`, `undefined`, and
    `'off'`, and `true` for every other `VALID_MODES` string;
    `recordModeChange` writes a row containing the exact `session_id` value
    passed (or `null` when omitted), computes `prev` from the resolved path
    (not the legacy one), and is a no-op (no row appended) when the
    normalized current and normalized next are the same value (e.g.
    resolved `current: 'off'` and called with `newMode: null`).

### Phase 2 — hooks

- [ ] `src/hooks/caveman-activate.js`
  - **Task:** in the existing synchronous stdin-read block (already parses
    `data.source`), also extract and sanitize `data.session_id`. Compute
    `writeFlagPath = path.join(claudeDir, flagBaseName(sessionId))` — used
    for every write **to the active-mode flag specifically**, unconditionally,
    never through the resolver. **This file also writes
    `.caveman-nudge-shown` (the one-shot statusline-setup nudge marker) —
    `nudgeMarkerPath` is UNCHANGED and stays global per Non-Goals; a v3
    draft's "every write in this file" wording was ambiguous enough that a
    literal reading could scope that marker too, which Tier-1 v3 review
    flagged as High. This v4 wording is scoped to the active-mode flag
    only — do not touch `nudgeMarkerPath`.** For the `source !== 'startup'`
    resume-preserve check, replace the current direct `readFlag(flagPath)`
    with the full `resolveFlag(claudeDir, sessionId)` result (not just
    `.mode`) and branch on `rejected` explicitly (fixes v4 finding 2 — a
    resolution can be `{ mode: null, rejected: true }` for an EXISTING
    symlinked/oversized/corrupted scoped file, which is a different case
    from "genuinely nothing here yet" and must not silently fall through to
    the configured default):
    ```js
    let mode = getDefaultMode();
    if (source !== 'startup') {
      const resolved = resolveFlag(claudeDir, sessionId);
      if (resolved.rejected) {
        mode = 'off'; // fail closed -- an existing-but-rejected scoped
                       // file must NOT silently reactivate at the default
      } else if (resolved.mode) {
        mode = resolved.mode; // genuinely resolved (scoped or legacy-fallback)
      }
      // else: truly nothing resolved (fresh session, no legacy either) --
      // `mode` stays at getDefaultMode() from above, which is correct.
    }
    ```
    Replace the `mode === 'off'` branch's `fs.unlinkSync(flagPath)` with
    `safeWriteFlag(writeFlagPath, 'off')`. Thread `sessionId` into every
    `recordModeChange` call in this file — pass whatever mode value was
    actually resolved (including a literal `'off'`); `recordModeChange`'s
    own internal `isActiveMode` normalization (Design above) handles
    collapsing `'off'` to `null` for logging, so this file does not need to
    pre-normalize.
  - **Acceptance:** `node caveman-activate.js < /dev/null` (no session_id)
    still writes `.caveman-active` (legacy path) — proves the fallback.
    Piping `{"source":"startup","session_id":"abc-123"}` writes
    `.caveman-active-abc-123` and NOT the legacy path, and leaves
    `.caveman-nudge-shown` at its existing global path (not
    `.caveman-nudge-shown-abc-123`) — proving the narrowed write scope.
    Piping a `resume` source for a session id with NO scoped file yet,
    while an active legacy flag exists, preserves the LEGACY mode (not
    "inactive") — this is the exact case v3 review's finding 1 caught
    missing. Piping a `resume` source with a pre-existing scoped flag for
    that session id preserves IT instead (extends the existing #691
    resume-preserve test to the scoped path). **New (v4 finding 2): piping
    a `resume` source where the scoped flag exists but is a symlink, is
    oversized, or contains garbage content, with an active DEFAULT mode
    configured — must resolve to `'off'` (fail closed), NOT the configured
    default.** A `mode === 'off'` resolution writes literal `off` content
    to the scoped path (verify via direct file read), not an unlink. A
    `resume` re-fire where the resolved mode is already `'off'` (i.e. no
    actual change) appends NO new mode-log row — proves
    `recordModeChange`'s internal normalization prevents the spurious
    off→null transition v3 finding 2 named.

- [ ] `src/hooks/caveman-mode-tracker.js`
  - **Task:** extract + sanitize `data.session_id` in the `end` handler
    (alongside the existing `data.prompt`/`data.cwd`/`data.transcript_path`
    reads). Compute `writeFlagPath`/`writePrevPath` via
    `flagBaseName`/`prevBaseName` — used for every write. For every READ of
    current state, use the resolver instead: replace
    `const current = readFlag(flagPath)` (independent-mode capture) with
    `const { mode: current } = resolveFlag(claudeDir, sessionId)`; replace
    `let activeMode = readFlag(flagPath)` with
    `let { mode: activeMode } = resolveFlag(claudeDir, sessionId)`; replace
    `const prev = readFlag(prevPath)` with
    `const { mode: prev } = resolvePrev(claudeDir, sessionId)`. Change the
    independent-mode-capture gate from `if (current && !INDEPENDENT_MODES.has(current))`
    to `if (isActiveMode(current) && !INDEPENDENT_MODES.has(current))`
    (v3 finding 2, item 1). Change the one-shot-restore gate from
    `if (prev && !INDEPENDENT_MODES.has(prev))` to
    `if (isActiveMode(prev) && !INDEPENDENT_MODES.has(prev))`; the `else`
    branch (deactivate) now also runs when `prev === 'off'`, writing `off`
    to `writeFlagPath` (not unlinking) and calling
    `recordModeChange(claudeDir, null, sessionId)` — never logging `'off'`
    itself as a mode value (v3 finding 2, item 2). Leave the
    `activeMode && INDEPENDENT_MODES.has(activeMode)` gate as bare
    truthiness (`INDEPENDENT_MODES.has('off')` is already false, so
    `isActiveMode` would be redundant there). Replace every remaining
    `fs.unlinkSync(writeFlagPath)` used to represent "off" with
    `safeWriteFlag(writeFlagPath, 'off')` (the `.prev` unlinks stay unlinks
    — that's genuinely transient one-shot-restore state, not the on/off
    sentinel). Thread `sessionId` into every `recordModeChange` call. Use
    `isActiveMode(activeMode)` instead of bare truthiness in the
    reinforcement-emit check. Pass `--session-id <sessionId>` (only when a
    valid sessionId was resolved — omit the flag entirely otherwise) to the
    `caveman-stats.js` `execFileSync` invocation, alongside the existing
    `--session-file`.
  - **Acceptance:** extend `tests/test_mode_tracker_stdin.js` with cases:
    two payloads with different `session_id`s and the same prompt
    (`"/caveman ultra"` then `"/caveman off"`) produce two independent scoped
    flag files, neither affecting the other's state or the legacy global
    file; the "off" payload leaves the scoped file present with content
    `off`, not deleted. `/caveman off` then `/caveman-commit` then an
    ordinary prompt does NOT save `off` into `.prev` and does NOT restore
    `off` as a distinct logged mode — the mode-log's row for the restore
    step must show `mode: null`, not `mode: "off"` (this is the exact
    sequence v3 finding 2 named). A payload with no `session_id` behaves
    exactly as today (legacy path only, matching the pre-change test
    expectations verbatim, including "off" still writing `off` rather than
    unlinking — note this IS a behavior change from today's unlink-on-off
    even for the legacy path; call this out explicitly in the test as an
    intentional, documented change, not a silent regression).

- [ ] `tests/test_mode_tracker.py`, `tests/verify_repo.py` (existing tests —
  update, don't just extend)
  - **Task:** Tier-1 v3 review found these pre-existing tracked tests
    assert the OLD unlink-on-off behavior this plan intentionally changes,
    and a v3 draft's checklist only named `tests/test_mode_tracker_stdin.js`
    for NEW coverage, never touching these. **Tier-1 v4 review then found
    the v3 fix over-broad and under-specified — it claimed "twelve"
    `assertIsNone(self.flag_value())` assertions all need to change, but
    three of the thirteen actual occurrences are NOT deactivation
    assertions at all; they check that caveman was never activated in the
    first place, and must stay asserting absence.** The precise,
    line-verified split in `tests/test_mode_tracker.py` (13 total
    `assertIsNone(self.flag_value())` occurrences):
    - **Change to expect content `off`** (10 lines — genuine deactivation
      outcomes): lines 63, 68, 73, 79, 84, 89 (`turn caveman mode off` /
      `turn caveman off` / `turn off caveman` / `stop\ncaveman` / `normal
      mode` / `back to normal mode please` — all explicit deactivations),
      144 (`/caveman off`), 160 (`/caveman-commit` with no prior mode,
      restored to nothing on the next prompt), 189 and 192
      (`test_deactivation_clears_saved_prev` — the deactivation itself and
      the subsequent prompt proving it stays deactivated).
    - **Leave asserting absence, unchanged** (3 lines — the flag was never
      activated in the first place, not deactivated): lines 107 and 109
      (`test_question_does_not_activate` — a bare question about caveman
      must never activate it, so the flag is correctly absent both times)
      and 113 (`test_scoped_brevity_does_not_activate` — a section-scoped
      brevity request must not activate caveman either). Line 190's
      `assertFalse(self.prev.exists(), ...)` also stays unchanged — `.prev`
      clearing remains an unlink in this plan, only the main flag's "off"
      representation changes.
    - `tests/verify_repo.py` has three matching assertions to flip:
      `"off mode should remove flag file"` (line ~314), `"/caveman with off
      default should not write flag"` (line ~327 — under this plan, a bare
      `/caveman` with a configured-off default now DOES write `off`
      content, so this assertion's message and check both need to flip),
      and `"normal mode should remove flag file"` (line ~364) — all three
      become "...flag file contains `off`" instead of "...removes flag
      file." Its `uninstall.sh` check (line ~379, "uninstall.sh should
      remove flag file") stays correct as-is — enumeration still removes
      the flag, it's the ON/OFF representation that changed, not the
      uninstall behavior.
  - **Acceptance:** exactly the 10 line-verified `test_mode_tracker.py`
    deactivation assertions change to expect `off` content; the 3
    never-activated assertions (lines 107, 109, 113) and the `.prev`
    absence check (line 190) are verified UNCHANGED in the diff (a reviewer
    or implementer checking the diff should see these four lines untouched
    — treat any incidental change to them as a self-check failure, not a
    stylistic choice); `verify_repo.py`'s three named assertions are
    updated to match the new `off`-is-written semantics. The full existing
    test suite (per the whole-plan Acceptance Gates) passes with these
    updates, proving no other tracked test still encodes the old
    unlink-on-off assumption.

### Phase 3 — stats + statuslines

- [ ] `src/hooks/caveman-stats.js`
  - **Task:** add `--session-id` argv parsing. When present, sanitize via
    `sanitizeSessionId` (defense in depth); when absent or invalid, treat as
    `null`. Use `resolveFlag(claudeDir, sessionId)` for the flag this script
    reads — `flagMtimeMs` must `fs.statSync` the exact `path` `resolveFlag`
    returned, not a separately-hardcoded path. Thread that same `path` into
    `formatStats`/`formatShare` and render it in the output (a `Flag file:`
    line) for all three outcomes (scoped, legacy-fallback, fail-closed) —
    this is what backs the docs task's "reports the resolved session file"
    claim (v3 Medium finding: a v2 draft made that promise in the docs task
    without a matching implementation requirement here). Do NOT change the
    existing `sessionId = path.basename(sessionFile, '.jsonl')` computation
    used for the `.caveman-history.jsonl` append — that stays as-is,
    unrelated to this flag/mode-log resolution. In `readModeLog`, also
    parse and return `session_id`, `hasSessionIdKey`
    (`Object.prototype.hasOwnProperty` check on the raw parsed row, before
    any defaulting), and `malformed` per row (Design above — a present,
    non-null `session_id` that fails `sanitizeSessionId` is `malformed:
    true` and must be excluded from every reader, never coerced to `null`
    via `||`). In `main()`, filter `modeLog` via the `relevantModeLogRows`
    logic (Design above) using the `--session-id` argument (not the
    transcript-derived id) before calling `attributeByMode`.
  - **Acceptance:** extend `tests/test_caveman_stats.js` with cases: (a)
    `.caveman-mode-log.jsonl` seeded with interleaved rows for two different
    explicit `session_id`s — attribution for session A's `--session-id`
    only reflects session A's rows; (b) a mix of true pre-migration rows
    (no `session_id` key at all) and new legacy-fallback rows
    (`session_id: null`) — the former join a scoped reader too, the latter
    do not; (c) a malformed row (`session_id: ""`, `session_id: 0`,
    `session_id: false`, or a path-traversal string) is excluded from
    EVERY reader's attribution, scoped or legacy — proves the `||`
    coercion bug from v2 doesn't recur; (d) invoking with no `--session-id`
    (manual/lifetime path) reads the legacy flag and joins legacy-or-keyless
    rows exactly as before this change (regression check); (e) stats output
    includes the resolved flag path in all three of scoped/legacy/fail-closed
    outcomes.

- [ ] `src/hooks/caveman-statusline.sh`
  - **Task:** read stdin, extract `session_id` via a plain string match (no
    `jq`), validate with a **whole-string anchored match** (e.g. `case
    "$SESSION_ID" in [A-Za-z0-9_-]*) ...whole-string test via parameter
    expansion or a strict regex match, never `tr -cd` character stripping
    followed by truncation`) and a length cap, rejecting the entire value
    (falling back to no-session behavior) on any failure — never a
    stripped/truncated partial value. Resolve the ENOENT-vs-rejected
    fallback semantics equivalent to `resolveFlag`. Render nothing for a
    resolved mode of `off` (matching `isActiveMode`).
  - **Acceptance:** manual + scripted invocation
    (`echo '{"session_id":"abc-123"}' | bash caveman-statusline.sh`) reads
    the scoped file when present; with no stdin JSON or an invalid
    `session_id` (including a path-traversal or oversized value) it reads
    the legacy path exactly as today, with NO partial/stripped id ever used
    to construct a path. A scoped file containing `off` renders nothing.
    Additionally covers the full file-state matrix from Phase 4 (ENOENT,
    valid content, `off` content, invalid content, oversized, symlink, and
    a non-`ENOENT` stat failure where constructible), each run against a
    distinguishable active legacy sentinel to prove no wrong fallback.

- [ ] `src/hooks/caveman-statusline.ps1`
  - **Task:** same behavior as the Bash script (whole-string anchored
    match, reject-not-strip, ENOENT-vs-rejected fallback, `off` renders
    nothing), PowerShell idiom (`ConvertFrom-Json` on stdin is fine here —
    it's a first-party PowerShell idiom, not an external dependency the way
    `jq` would be for Bash).
  - **Acceptance:** same test vectors AND the same file-state matrix as the
    Bash script produce the same resolved path (manual `pwsh` check if
    `pwsh` is unavailable in CI, documented explicitly — not a silent
    "looks right" visual check).

### Phase 4 — shared sanitizer test vectors

- [ ] `tests/test_session_scoping.js` (new)
  - **Task:** define one shared table of `{ input, expectedSanitized }`
    vectors covering: valid UUID, valid short alnum id, empty string,
    `../../etc/passwd`, a string with an embedded path separator, a 200-char
    string, `null`, a number. Exercise `sanitizeSessionId` from
    `caveman-config.js` directly, AND pipe the same raw `session_id` values
    through `caveman-statusline.sh` (via
    `child_process.execFileSync('bash', [...])`) and, if `pwsh`/`powershell`
    is available, `caveman-statusline.ps1`, asserting all three resolve to
    the identical scoped-vs-fallback decision — critically, that an invalid
    value NEVER produces a stripped/truncated scoped path in any of the
    three implementations. Additionally define the file-state matrix (v3
    Medium finding): for a valid session id, construct each of `ENOENT`
    (no scoped file), valid mode content, `off` content, invalid/garbage
    content, oversized (>64 bytes) content, a symlink pointing elsewhere,
    and (where constructible) a non-`ENOENT` stat failure — each case paired
    with a distinguishable active legacy sentinel value present at the
    legacy path — and assert the resolved path/mode/render decision for
    each, in both `caveman-config.js`'s `resolveFlag` directly and through
    the Bash/PowerShell statuslines.
  - **Acceptance:** test passes for the JS + Bash pair unconditionally;
    the PowerShell leg runs when `pwsh`/`powershell` is resolvable on PATH
    and is explicitly skipped (with a printed reason, not a silent no-op)
    otherwise. Every file-state matrix case proves the legacy sentinel is
    read ONLY for `ENOENT` and never for any other scoped-file state.

### Phase 5 — installer + docs

- [ ] `cli/install.js`
  - **Task:** in the uninstall path, replace the two exact-path
    `.caveman-active` unlinks (`configDir` case only — leave the opencode
    `ocFlag` case as exact-path, out of scope) with `fs.readdirSync(configDir)`
    enumeration, unlinking every entry matching
    `^\.caveman-active(-[A-Za-z0-9_-]{1,128})?(\.prev)?$`.
  - **Acceptance:** extend the installer e2e uninstall test
    (`tests/installer/e2e.freshinstall.test.mjs` or a new case) to seed a
    legacy flag, two scoped flags for different session ids (including one
    containing literal `off` content), a `.prev` file, and a genuine
    near-miss name that must NOT match the regex (e.g.
    `.caveman-active-backup.2026` — a literal dot, which the
    `[A-Za-z0-9_-]` charset rejects) — assert the real ones are removed and
    the near-miss survives.

- [ ] `src/hooks/uninstall.sh`
  - **Task:** replace the exact-path `$FLAG_FILE` removal (currently
    `if [ -f "$FLAG_FILE" ]; then rm "$FLAG_FILE"; ...`) with an enumeration
    of `$CLAUDE_DIR`'s entries, removing every name matching the same
    `^\.caveman-active(-[A-Za-z0-9_-]{1,128})?(\.prev)?$` pattern via a
    real directory listing (`find "$CLAUDE_DIR" -maxdepth 1
    -name '.caveman-active*'` piped through a per-name pattern test — not a
    bare glob expansion, which would accept a near-miss name the regex
    should reject).
  - **Acceptance:** a standalone-install-style test (new, or extend an
    existing installer test if one already drives `uninstall.sh`) seeds the
    same fixture set as the `cli/install.js` uninstall test (legacy flag,
    two scoped flags including one with `off` content, a `.prev` file, a
    near-miss name) and asserts the same removal/survival split.

- [ ] `src/hooks/uninstall.ps1`
  - **Task:** same fix, PowerShell idiom: `Get-ChildItem $ClaudeDir` +
    a `-match` filter against the equivalent anchored pattern, replacing
    the exact-path `$FlagFile` removal.
  - **Acceptance:** same fixture set and split as `uninstall.sh` (manual
    `pwsh` check if unavailable in CI, documented explicitly).

- [ ] `CLAUDE.md`, `src/hooks/README.md`, `INSTALL.md`
  - **Task:** update every prose/ASCII-diagram description of
    `$CLAUDE_CONFIG_DIR/.caveman-active` as "the" flag file to describe the
    per-session naming (`.caveman-active-<session_id>`, falling back to the
    legacy global name when no session id is available or its scoped file
    has never been written), note that "off" is now represented as written
    content rather than file absence, and add a short "how do I check my
    current session's mode" note (run `/caveman-stats` inside the session,
    which now reports its exact resolved flag path; or find the
    most-recently-modified `.caveman-active-*` file).
  - **Acceptance:** grep for `\.caveman-active` across these three files
    turns up no sentence that still describes a single global flag as the
    only state or describes "off" as file-absence; the troubleshooting
    sections (`INSTALL.md`'s `cat .../.caveman-active` debug step) show the
    scoped-name pattern too.

## Acceptance Gates (whole plan)

- Full existing test suite (`npm test`, `for f in tests/test_*.js; do node
  "$f"; done`, `python -m unittest discover -s tests`) passes with zero
  regressions.
- Two concurrent simulated sessions (distinct `session_id`s) toggle caveman
  independently, including one turning off while the other stays on —
  proven by an automated test, not manual inspection.
- A session with no `session_id` selects the same legacy `.caveman-active`
  path as before this change (path selection is unchanged) — with two
  explicit, documented representation changes that apply UNIVERSALLY,
  including on this legacy path, not just the scoped one: (1) the mode-log
  row it appends additionally carries `session_id: null` (an additive JSON
  field), and (2) an "off" resolution now writes literal `off` content to
  the flag file instead of unlinking it. A v2 draft of this gate claimed
  full byte-identical flag content on the legacy path, which directly
  contradicted requirement (2) elsewhere in this same plan — this wording
  is the fix (Tier-1 v3 High finding).
- `caveman-stats.js` attribution for a session with concurrent-session
  mode-log rows interleaved in time only reflects that session's own rows
  (plus genuinely historical pre-migration rows, which have no
  `session_id` key at all) — and never a malformed row's value, regardless
  of which session is reading.
- All three uninstall entry points (`cli/install.js`, `uninstall.sh`,
  `uninstall.ps1`) remove every scoped flag/`.prev` file they can enumerate,
  not just the legacy name.
