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

### Shared resolver — ENOENT vs rejected content (fixes v2 findings 2 + 4)

One function in `caveman-config.js`, used by every consumer (`activate.js`,
`mode-tracker.js`, `stats.js`, and reimplemented with equivalent semantics in
both statusline scripts):

```js
function resolveFlag(claudeDir, sessionId) {
  const legacyPath = path.join(claudeDir, '.caveman-active');
  if (!sessionId) {
    return { path: legacyPath, mode: readFlag(legacyPath) };
  }
  const scopedPath = path.join(claudeDir, flagBaseName(sessionId));
  let st;
  try {
    st = fs.lstatSync(scopedPath);
  } catch (e) {
    if (e.code === 'ENOENT') {
      // Never touched by scoped logic yet -> legacy fallback.
      return { path: legacyPath, mode: readFlag(legacyPath) };
    }
    // Any other stat error (permission, etc.) -> fail closed. Never fall back.
    return { path: scopedPath, mode: null };
  }
  // Scoped path exists in SOME form. readFlag re-validates symlink/size/
  // whitelist and returns null for anything unsafe or invalid -- that null
  // is a fail-closed "inactive," NOT a signal to fall back to legacy.
  return { path: scopedPath, mode: readFlag(scopedPath) };
}
```

`mode` is `null` for "inactive" (including the fail-closed cases above) or a
`VALID_MODES` string including `'off'` (see `isActiveMode` above to collapse
`'off'` into "inactive" for callers that only care about active/inactive).
Returning `{ path, mode }` (not just `mode`) lets `caveman-stats.js` stat the
*same* path it read mode from (fixes the Medium finding about `flagMtimeMs`
following a different path than `mode`).

Because "off" is now written rather than deleted, a session that has
explicitly turned off has a scoped file that exists with content `'off'` —
`lstatSync` succeeds, `readFlag` returns `'off'`, and the function correctly
returns the scoped path with mode `'off'`, never falling through to legacy.
Only a session that has *never* written to its scoped path at all (true
`ENOENT`) falls back — resolving the v1 ambiguity precisely.

### Mode-transition log — add `session_id`, keep one shared file (fixes v2 findings 5 + 6)

`recordModeChange(claudeDir, newMode, sessionId)` (new third parameter)
resolves `current` via `resolveFlag(claudeDir, sessionId).mode` — the SAME
path/logic every other consumer uses — instead of the current hardcoded
`readFlag(path.join(claudeDir, '.caveman-active'))`. It appends
`{ ts, mode, prev, session_id: sessionId || null }`. The log stays a single
shared append-only file (`appendFlag`'s `O_APPEND` already makes concurrent
writers safe); fragmenting it per session would complicate
`aggregateHistory`'s cross-session lifetime rollup for no benefit.

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

```js
function relevantModeLogRows(modeLog, sessionId) {
  return modeLog.filter(row => {
    if (!row.hasSessionIdKey) return true;       // pre-migration historical row
    return (row.session_id || null) === (sessionId || null);
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
path exactly as every pre-this-change invocation always has. This preserves
manual/lifetime usage byte-for-byte while fixing the split-brain the
Critical finding identified: a hook falling back to legacy (invalid or
missing `session_id`) now passes an empty/omitted `--session-id`, so stats
falls back to legacy too, matching the hook exactly.

The pre-existing `sessionId = path.basename(sessionFile, '.jsonl')`
computation at the lifetime-history append site is UNCHANGED and serves a
different purpose (a stable per-session key for
`.caveman-history.jsonl`'s cross-session rollup) — this plan does not touch
that line or that file.

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

### Uninstall enumeration

`cli/install.js`'s uninstaller currently unlinks only the exact literal
`.caveman-active` path (`configDir` case) and `.caveman-active` under the
opencode config dir. Neither removes `.caveman-active.prev` (pre-existing
gap, out of scope — see Non-Goals) nor would either remove the new
`.caveman-active-<id>` / `.caveman-active-<id>.prev` scoped variants this
plan introduces. Enumerate `configDir`'s entries once and unlink every name
matching `^\.caveman-active(-[A-Za-z0-9_-]{1,128})?(\.prev)?$` via
`fs.readdirSync` — real directory enumeration, not a shell glob (Node's `fs`
APIs don't expand `*`).

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
- **Fixing the pre-existing incomplete uninstall** (`.caveman-active.prev`,
  `.caveman-mode-log.jsonl`, `.caveman-history.jsonl`, `.caveman-statusline-suffix`
  are never removed by `cli/install.js --uninstall` today, scoped file or
  not). Real gap, predates this change, not touched here.

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
| `src/plugins/opencode/plugin.js` | writer + reader (opencode) | out of scope — legacy/alternate path, left conforming to its OWN existing (global) contract, tested only to confirm this PR doesn't touch it |
| `CLAUDE.md`, `src/hooks/README.md`, `INSTALL.md` | documentation of the flag topology | needs-change |
| `tests/test_symlink_flag.js`, `tests/test_repo_local_config.js` | exercise `caveman-config.js` helpers | test-only — extend for new helpers |

Legacy/alternate-path test required by the matrix: a hook invocation with NO
`session_id` (or an invalid one) must produce the SAME files, paths, and
flag content as today — writes and reads the legacy global `.caveman-active`
— proving the fallback isn't just documented but actually exercised. (The
mode-log row gains an additive `session_id: null` field even on this path —
see the relaxed acceptance-gate wording below; this is the one intentional,
documented deviation from strict byte-identity.)

## Implementation-Ready Checklist

### Phase 1 — `caveman-config.js` foundation

- [ ] `src/hooks/caveman-config.js`
  - **Task:** add `SESSION_ID_RE`, `sanitizeSessionId(raw)`, `flagBaseName(sessionId)`,
    `prevBaseName(sessionId)`, `isActiveMode(mode)`, `resolveFlag(claudeDir, sessionId)`
    (per Design above — returns `{ path, mode }`, ENOENT-vs-rejected fail-closed
    semantics). Change `recordModeChange(claudeDir, newMode)` to
    `recordModeChange(claudeDir, newMode, sessionId)`: resolve `current` via
    `resolveFlag(claudeDir, sessionId).mode` (not the hardcoded legacy path),
    append `session_id: sessionId || null` to the JSON row. Export all new
    functions.
  - **Acceptance:** existing `tests/test_symlink_flag.js` and
    `tests/test_repo_local_config.js` still pass unmodified (backward-compatible
    signature — `sessionId` is a new optional third arg). New unit coverage
    (`tests/test_session_scoping.js`, Phase 4) proves: `sanitizeSessionId`
    accepts a UUID-shaped string and rejects `../etc`, `""`, `null`, a
    200-char string, and a non-string, with NO partial/stripped acceptance;
    `flagBaseName`/`prevBaseName` return the legacy name for `null`/`undefined`
    and the scoped name otherwise; `resolveFlag` returns legacy on ENOENT,
    fails closed (`mode: null`, scoped path) on a symlinked or oversized
    scoped file, and returns the scoped path+mode (including `'off'`) once
    that scoped file exists in any form; `recordModeChange` writes a row
    containing the exact `session_id` value passed (or `null` when omitted)
    and computes `prev` from the resolved path, not the legacy one.

### Phase 2 — hooks

- [ ] `src/hooks/caveman-activate.js`
  - **Task:** in the existing synchronous stdin-read block (already parses
    `data.source`), also extract and sanitize `data.session_id`. Replace the
    `mode === 'off'` branch's `fs.unlinkSync(flagPath)` with
    `safeWriteFlag(flagPath, 'off')`. Compute `flagPath` via `resolveFlag`
    (or `flagBaseName` directly, since activate.js always writes for ITS OWN
    session and doesn't need the legacy-fallback read path for writes) after
    session-id resolution. Thread `sessionId` into every `recordModeChange`
    call in this file.
  - **Acceptance:** `node caveman-activate.js < /dev/null` (no session_id)
    still writes `.caveman-active` (legacy path) — proves the fallback.
    Piping `{"source":"startup","session_id":"abc-123"}` writes
    `.caveman-active-abc-123` and NOT the legacy path. Piping a `resume`
    source with a pre-existing scoped flag for that session id preserves it
    (does not reset to the configured default) — extends the existing #691
    resume-preserve test to the scoped path. A `mode === 'off'` resolution
    writes literal `off` content to the scoped path (verify via direct file
    read), not an unlink.

- [ ] `src/hooks/caveman-mode-tracker.js`
  - **Task:** extract + sanitize `data.session_id` in the `end` handler
    (alongside the existing `data.prompt`/`data.cwd`/`data.transcript_path`
    reads). Compute `flagPath`/`prevPath` via `flagBaseName`/`prevBaseName`
    using that session id. Replace every `fs.unlinkSync(flagPath)` used to
    represent "off" with `safeWriteFlag(flagPath, 'off')` (the `.prev`
    unlinks stay unlinks — that's genuinely transient one-shot-restore
    state, not the on/off sentinel). Thread `sessionId` into every
    `recordModeChange` call. Use `isActiveMode(activeMode)` instead of bare
    truthiness in the reinforcement-emit check. Pass
    `--session-id <sessionId>` (only when a valid sessionId was resolved —
    omit the flag entirely otherwise) to the `caveman-stats.js`
    `execFileSync` invocation, alongside the existing `--session-file`.
  - **Acceptance:** extend `tests/test_mode_tracker_stdin.js` with cases:
    two payloads with different `session_id`s and the same prompt
    (`"/caveman ultra"` then `"/caveman off"`) produce two independent scoped
    flag files, neither affecting the other's state or the legacy global
    file; the "off" payload leaves the scoped file present with content
    `off`, not deleted. A payload with no `session_id` behaves exactly as
    today (legacy path only, matching the pre-change test expectations
    verbatim, including "off" still writing `off` rather than unlinking —
    note this IS a behavior change from today's unlink-on-off even for the
    legacy path; call this out explicitly in the test as an intentional,
    documented change, not a silent regression).

### Phase 3 — stats + statuslines

- [ ] `src/hooks/caveman-stats.js`
  - **Task:** add `--session-id` argv parsing. When present, sanitize via
    `sanitizeSessionId` (defense in depth); when absent or invalid, treat as
    `null`. Use `resolveFlag(claudeDir, sessionId)` for the flag this script
    reads — `flagMtimeMs` must `fs.statSync` the exact `path` `resolveFlag`
    returned, not a separately-hardcoded path. Do NOT change the existing
    `sessionId = path.basename(sessionFile, '.jsonl')` computation used for
    the `.caveman-history.jsonl` append — that stays as-is, unrelated to
    this flag/mode-log resolution. In `readModeLog`, also parse and return
    `session_id` and `hasSessionIdKey` (`Object.prototype.hasOwnProperty`
    check on the raw parsed row, before any defaulting) per row. In
    `main()`, filter `modeLog` via the `relevantModeLogRows` logic (Design
    above) using the `--session-id` argument (not the transcript-derived
    id) before calling `attributeByMode`.
  - **Acceptance:** extend `tests/test_caveman_stats.js` with cases: (a)
    `.caveman-mode-log.jsonl` seeded with interleaved rows for two different
    explicit `session_id`s — attribution for session A's `--session-id`
    only reflects session A's rows; (b) a mix of true pre-migration rows
    (no `session_id` key at all) and new legacy-fallback rows
    (`session_id: null`) — the former join a scoped reader too, the latter
    do not; (c) invoking with no `--session-id` (manual/lifetime path)
    reads the legacy flag and joins legacy-or-keyless rows exactly as
    before this change (regression check).

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
    Shared test vectors (Phase 4) prove the same session id sanitizes
    identically here and in `caveman-config.js`.

- [ ] `src/hooks/caveman-statusline.ps1`
  - **Task:** same behavior as the Bash script (whole-string anchored
    match, reject-not-strip, ENOENT-vs-rejected fallback, `off` renders
    nothing), PowerShell idiom (`ConvertFrom-Json` on stdin is fine here —
    it's a first-party PowerShell idiom, not an external dependency the way
    `jq` would be for Bash).
  - **Acceptance:** same test vectors as the Bash script produce the same
    resolved path (manual `pwsh` check if `pwsh` is unavailable in CI,
    documented explicitly — not a silent "looks right" visual check).

### Phase 4 — shared sanitizer test vectors

- [ ] `tests/test_session_scoping.js` (new)
  - **Task:** define one shared table of `{ input, expectedSanitized }`
    vectors covering: valid UUID, valid short alnum id, empty string,
    `../../etc/passwd`, a string with a null byte, a 200-char string, `null`,
    a number. Exercise `sanitizeSessionId` from `caveman-config.js` directly,
    AND pipe the same raw `session_id` values through
    `caveman-statusline.sh` (via `child_process.execFileSync('bash', [...])`)
    and, if `pwsh`/`powershell` is available, `caveman-statusline.ps1`,
    asserting all three resolve to the identical scoped-vs-fallback
    decision — critically, that an invalid value NEVER produces a
    stripped/truncated scoped path in any of the three implementations.
  - **Acceptance:** test passes for the JS + Bash pair unconditionally;
    the PowerShell leg runs when `pwsh`/`powershell` is resolvable on PATH
    and is explicitly skipped (with a printed reason, not a silent no-op)
    otherwise.

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
- A session with no `session_id` reads and writes the legacy
  `.caveman-active` file exactly as before this change, with one documented,
  intentional exception: the mode-log row it appends now additionally
  carries `session_id: null` (an additive JSON field; the flag file itself
  and every other on-disk path/content stays byte-identical).
- `caveman-stats.js` attribution for a session with concurrent-session
  mode-log rows interleaved in time only reflects that session's own rows
  (plus genuinely historical pre-migration rows, which have no
  `session_id` key at all).
