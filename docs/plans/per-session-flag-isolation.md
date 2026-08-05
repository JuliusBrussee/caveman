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
still fall back to the legacy global path; `bin/install.js` uninstall used
exact-path removal instead of enumerating scoped variants; statusline
sanitizer-parity testing covered only the JS helpers, not the Bash/PowerShell
scripts; docs describing the flag topology would go stale). No v2 was ever
produced and no code from that attempt survived — `origin/main` fast-forwarded
past it and the feature branch sat at 0 commits.

**Reference context.** The upstream repo has moved substantially since that
review (`0d95a81` → `ec83e5b`, 11 commits, tag `v1.10.0`): `bin/` renamed to
`cli/`, a shared `caveman-parse.js` module extracted from the tracker hook
(#602), and — most relevant here — `caveman-activate.js` already gained a
**synchronous** stdin read for a `source` field (#691, distinguishing real
`startup` from `resume`/`clear`/`compact` re-fires) since that review ran.
That single change removes the exact async-timeout hazard the v1 review's
High finding was about: this plan's stdin handling in `caveman-activate.js`
reuses that already-accepted synchronous, EOF-driven read rather than
introducing a new timer-based one. `caveman-mode-tracker.js` also gained a
`.caveman-active.prev` restore mechanism (#599) and a mode-transition log
`.caveman-mode-log.jsonl` (#601, feeds `caveman-stats.js` attribution) — both
new state files this plan must scope consistently with the primary flag, or
leave deliberately unscoped with a stated reason (see Invariant Matrix).

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
`statusLine` command's stdin) all carry a `session_id` field. `caveman-stats.js`
already derives an equivalent identifier independently — `path.basename(sessionFile,
'.jsonl')` (`caveman-stats.js:553`, pre-existing, used for the lifetime history
log) — because Claude Code names each session's transcript JSONL by session id.
This plan takes the hook-payload field as authoritative in the two hooks that
receive it directly, and continues using the transcript-basename derivation in
`caveman-stats.js`, matching existing precedent instead of threading a new CLI
flag through the `execFileSync` call in `caveman-mode-tracker.js`.

A `session_id` value is untrusted external input feeding a filesystem path
(same class of concern the existing symlink hardening in `caveman-config.js`
already treats seriously). Add a strict allowlist sanitizer to
`caveman-config.js`:

```js
const SESSION_ID_RE = /^[A-Za-z0-9_-]{1,128}$/;
function sanitizeSessionId(raw) {
  return typeof raw === 'string' && SESSION_ID_RE.test(raw) ? raw : null;
}
```

Any session id that fails this check (missing field, wrong type, empty
string, path-traversal characters, oversized) is treated identically to "no
session id" — see fallback below. This also naturally covers every caller
that never sends `session_id` at all: `opencode`'s plugin, the Gemini CLI
extension, and a manual `node caveman-activate.js < /dev/null` invocation
that pipes no JSON. None of those are in scope for this change (see
Non-Goals) and all continue to get the pre-existing global-file behavior
unmodified.

### Scoped file naming + fallback-on-read

```js
function flagBaseName(sessionId) {
  return sessionId ? `.caveman-active-${sessionId}` : '.caveman-active';
}
function prevBaseName(sessionId) {
  return sessionId ? `.caveman-active-${sessionId}.prev` : '.caveman-active.prev';
}
```

**Writes** always target the scoped path when a valid `sessionId` is
resolved. **Reads** (in `caveman-mode-tracker.js`'s reinforcement check,
`caveman-activate.js`'s resume-preserve check, both statuslines, and
`caveman-stats.js`) try the scoped path first; if it does not exist
(`ENOENT`) fall back to the legacy global `.caveman-active`. This is the
explicit compatibility path the v1 review asked for, and it gives a smooth
upgrade: a long-running session that predates this change and never toggles
caveman again keeps showing whatever the legacy global file last held;
the moment any hook writes for that session, it fully switches to its own
scoped file and no longer reads or writes the legacy path.

No migration script, no one-time rewrite of the legacy file — the read
fallback IS the migration path, and it is symmetric with the
already-established `safeWriteFlag`/`readFlag` posture (silent, best-effort,
fail toward the safer/simpler behavior).

### Mode-transition log — add `session_id`, keep one shared file

`recordModeChange(claudeDir, newMode)` gains a third parameter:
`recordModeChange(claudeDir, newMode, sessionId)`, and appends
`{ ts, mode, prev, session_id: sessionId || null }`. The log stays a single
shared append-only file (`appendFlag`'s `O_APPEND` already makes concurrent
writers safe); fragmenting it per session would complicate
`aggregateHistory`'s cross-session lifetime rollup for no benefit.
`readModeLog` in `caveman-stats.js` gains a `session_id` field on each
returned row (additive — the JSON parse already ignores unknown keys, so
this doesn't affect any other reader). `caveman-stats.js`'s `main()` filters
`modeLog` to rows where `session_id == null || session_id === thisSessionId`
before the attribution join — treating a pre-upgrade row with no
`session_id` as "unknown, don't drop it" preserves existing history exactly
as it reads today; only NEW rows recorded after this change actually gain
session isolation in the join.

### Statusline scripts

Both `caveman-statusline.sh` and `caveman-statusline.ps1` currently read no
stdin at all — they only ever look at the fixed global flag path. Claude Code
invokes a `statusLine` command with the same JSON-on-stdin contract as other
hooks (confirmed via the sibling `ai-coding-agents/claude/statusline.sh`,
which already reads `.session_id` from this exact payload shape). Both
scripts gain: read stdin, extract `session_id` with a plain string
match (no `jq` dependency — matches the existing dependency-free posture of
`caveman-statusline.sh`), run it through the same allowlist as the JS
sanitizer, and try the scoped flag path before falling back to the global
one. This is the v1 review's "sanitizer parity" finding: both non-JS
statusline scripts now do the identical validate-then-fallback logic as the
Node side, with shared test vectors (see Invariant Matrix row 4).

### Uninstall enumeration

`bin/install.js`'s uninstaller currently unlinks only the exact literal
`.caveman-active` path (`configDir` case) and `.caveman-active` under the
opencode config dir. Neither removes `.caveman-active.prev` (pre-existing
gap, out of scope — see Non-Goals) nor would either remove the new
`.caveman-active-<id>` / `.caveman-active-<id>.prev` scoped variants this
plan introduces. Enumerate `configDir`'s entries once and unlink every name
matching `^\.caveman-active(-[A-Za-z0-9_-]{1,128})?(\.prev)?$` via
`fs.readdirSync` — real directory enumeration, not a shell glob (Node's `fs`
APIs don't expand `*`), which is exactly the v1 review's Medium finding.

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
  files downloaded from the pinned release tag (`PINPED_REF` in
  `bin/install.js`, currently `v1.10.0`) for the curl\|bash / `npx` remote
  install path. Per the existing top-of-file comment, it's regenerated at
  release-cut time, after this change is merged and tagged — not part of
  this PR's diff.
- **Fixing the pre-existing incomplete uninstall** (`.caveman-active.prev`,
  `.caveman-mode-log.jsonl`, `.caveman-history.jsonl`, `.caveman-statusline-suffix`
  are never removed by `bin/install.js --uninstall` today, scoped file or
  not). Real gap, predates this change, not touched here.

## Invariant Matrix

**Invariant:** every reader of the caveman mode state resolves the SAME flag
path (scoped-if-valid-session-id, else legacy global) as every writer for
that same session — no split-brain where a hook writes to one path and a
statusline or stats reader looks at a different one.

| Path | Role | Conformance |
|---|---|---|
| `src/hooks/caveman-config.js` | defines `sanitizeSessionId`, `flagBaseName`, `prevBaseName`; `recordModeChange` gains `sessionId` param | needs-change |
| `src/hooks/caveman-activate.js` | writer (SessionStart) + reader (resume-preserve check) | needs-change |
| `src/hooks/caveman-mode-tracker.js` | writer + reader (reinforcement, one-shot independent-mode restore) | needs-change |
| `src/hooks/caveman-stats.js` | reader (flag + mode-log join), also derives `sessionId` from transcript basename (pre-existing pattern, reused) | needs-change |
| `src/hooks/caveman-statusline.sh` | reader | needs-change |
| `src/hooks/caveman-statusline.ps1` | reader | needs-change |
| `bin/install.js` (uninstall, `configDir` case) | deletes state files | needs-change (enumerate) |
| `bin/install.js` (uninstall, opencode `ocFlag` case) | deletes state files | conforms (opencode explicitly out of scope, unlink stays exact-path) |
| `src/plugins/opencode/plugin.js` | writer + reader (opencode) | out of scope — legacy/alternate path, left conforming to its OWN existing (global) contract, tested only to confirm this PR doesn't touch it |
| `CLAUDE.md`, `src/hooks/README.md`, `INSTALL.md` | documentation of the flag topology | needs-change |
| `tests/test_symlink_flag.js`, `tests/test_repo_local_config.js` | exercise `caveman-config.js` helpers | test-only — extend for new helpers |

Legacy/alternate-path test required by the matrix: a hook invocation with NO
`session_id` (or an invalid one) must produce byte-identical behavior to
today — writes and reads the legacy global `.caveman-active` — proving the
fallback isn't just documented but actually exercised.

## Implementation-Ready Checklist

### Phase 1 — `caveman-config.js` foundation

- [ ] `src/hooks/caveman-config.js`
  - **Task:** add `SESSION_ID_RE`, `sanitizeSessionId(raw)`, `flagBaseName(sessionId)`,
    `prevBaseName(sessionId)`; change `recordModeChange(claudeDir, newMode)` to
    `recordModeChange(claudeDir, newMode, sessionId)`, adding `session_id: sessionId || null`
    to the appended JSON row. Export the three new functions.
  - **Acceptance:** existing `tests/test_symlink_flag.js` and
    `tests/test_repo_local_config.js` still pass unmodified (backward-compatible
    signature — `sessionId` is a new optional third arg). New unit coverage (in
    this same file's test, or a new `tests/test_session_scoping.js`) proves:
    `sanitizeSessionId` accepts a UUID-shaped string and rejects `../etc`,
    `""`, `null`, a 200-char string, and a non-string; `flagBaseName`/`prevBaseName`
    return the legacy name for `null`/`undefined` and the scoped name otherwise;
    `recordModeChange` writes a row containing the exact `session_id` value passed
    (or `null` when omitted).

### Phase 2 — hooks

- [ ] `src/hooks/caveman-activate.js`
  - **Task:** in the existing synchronous stdin-read block (already parses
    `data.source`), also extract and sanitize `data.session_id`. Compute
    `flagPath` via `flagBaseName` after that resolution (currently computed
    once at module top before stdin is read — move it after). Thread
    `sessionId` into every `recordModeChange` call in this file.
  - **Acceptance:** `node caveman-activate.js < /dev/null` (no session_id)
    still writes `.caveman-active` (legacy path) — proves the fallback.
    Piping `{"source":"startup","session_id":"abc-123"}` writes
    `.caveman-active-abc-123` and NOT the legacy path. Piping a `resume`
    source with a pre-existing scoped flag for that session id preserves it
    (does not reset to the configured default) — extends the existing #691
    resume-preserve test to the scoped path.

- [ ] `src/hooks/caveman-mode-tracker.js`
  - **Task:** extract + sanitize `data.session_id` in the `end` handler
    (alongside the existing `data.prompt`/`data.cwd`/`data.transcript_path`
    reads). Compute `flagPath`/`prevPath` via `flagBaseName`/`prevBaseName`
    using that session id. Thread `sessionId` into every `recordModeChange`
    call. No change needed to the `caveman-stats.js` `execFileSync` invocation
    (stats derives its own session id from `transcript_path`, per existing
    precedent).
  - **Acceptance:** extend `tests/test_mode_tracker_stdin.js` with cases:
    two payloads with different `session_id`s and the same prompt
    (`"/caveman ultra"` then `"/caveman off"`) produce two independent scoped
    flag files, neither affecting the other's state or the legacy global
    file. A payload with no `session_id` behaves exactly as today (legacy
    path only, matching the pre-change test expectations verbatim).

### Phase 3 — stats + statuslines

- [ ] `src/hooks/caveman-stats.js`
  - **Task:** compute `sessionId = path.basename(sessionFile, '.jsonl')`
    once `sessionFile` is resolved (before the existing use at the history
    log at line ~553 — reuse the same value, don't recompute). Use
    `flagBaseName(sessionId)` for the `flagPath` this script reads. In
    `readModeLog`, also parse and return `session_id` per row (`null` if
    absent/invalid). In `main()`, filter `modeLog` to rows where
    `row.session_id == null || row.session_id === sessionId` before calling
    `attributeByMode`.
  - **Acceptance:** extend `tests/test_caveman_stats.js` with a case seeding
    `.caveman-mode-log.jsonl` with interleaved rows for two different
    `session_id`s and asserting `attributeByMode`'s output for session A
    only reflects session A's rows (the pre-existing test's "no session_id"
    rows must still join exactly as before — regression check).

- [ ] `src/hooks/caveman-statusline.sh`
  - **Task:** read stdin, extract `session_id` via a plain string match
    (no `jq`), sanitize through the same allowlist semantics as
    `SESSION_ID_RE` (POSIX character class, not a JS regex), try
    `.caveman-active-<id>` first, fall back to `.caveman-active` if that
    path doesn't exist.
  - **Acceptance:** manual + scripted invocation
    (`echo '{"session_id":"abc-123"}' | bash caveman-statusline.sh`) reads
    the scoped file when present; with no stdin JSON or an invalid
    `session_id` it reads the legacy path exactly as today. Shared test
    vectors (see Phase 4) prove the same session id sanitizes identically
    here and in `caveman-config.js`.

- [ ] `src/hooks/caveman-statusline.ps1`
  - **Task:** same behavior as the Bash script, PowerShell idiom
    (`ConvertFrom-Json` on stdin is fine here — it's a first-party PowerShell
    idiom, not an external dependency the way `jq` would be for Bash).
  - **Acceptance:** same test vectors as the Bash script produce the same
    resolved path (manual `pwsh` check if `pwsh` is unavailable in CI,
    documented explicitly per the v1 review's finding — not a silent
    "looks right" visual check).

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
    decision. This is the exact test gap the v1 review's Medium finding
    named ("verification only calls the JavaScript helpers... PowerShell
    check is a visual comparison").
  - **Acceptance:** test passes for the JS + Bash pair unconditionally;
    the PowerShell leg runs when `pwsh`/`powershell` is resolvable on PATH
    and is explicitly skipped (with a printed reason, not a silent no-op)
    otherwise.

### Phase 5 — installer + docs

- [ ] `bin/install.js`
  - **Task:** in the uninstall path, replace the two exact-path
    `.caveman-active` unlinks (`configDir` case only — leave the opencode
    `ocFlag` case as exact-path, out of scope) with `fs.readdirSync(configDir)`
    enumeration, unlinking every entry matching
    `^\.caveman-active(-[A-Za-z0-9_-]{1,128})?(\.prev)?$`.
  - **Acceptance:** extend the installer e2e uninstall test
    (`tests/installer/e2e.freshinstall.test.mjs` or a new case) to seed a
    legacy flag, two scoped flags for different session ids, a `.prev` file,
    and a near-miss name that must NOT match (e.g. `.caveman-active-backup-2026`
    if it fails the id charset, or an unrelated dotfile) — assert the real
    ones are removed and the near-miss survives.

- [ ] `CLAUDE.md`, `src/hooks/README.md`, `INSTALL.md`
  - **Task:** update every prose/ASCII-diagram description of
    `$CLAUDE_CONFIG_DIR/.caveman-active` as "the" flag file to describe the
    per-session naming (`.caveman-active-<session_id>`, falling back to the
    legacy global name when no session id is available), and add a short
    "how do I check my current session's mode" note (run `/caveman-stats`
    inside the session — it reports the resolved session file; or find the
    most-recently-modified `.caveman-active-*` file).
  - **Acceptance:** grep for `\.caveman-active` across these three files
    turns up no sentence that still describes a single global flag as the
    only state; the troubleshooting sections (`INSTALL.md`'s `cat
    .../.caveman-active` debug step) show the scoped-name pattern too.

## Acceptance Gates (whole plan)

- Full existing test suite (`npm test`, `for f in tests/test_*.js; do node
  "$f"; done`, `python -m unittest discover -s tests`) passes with zero
  regressions.
- Two concurrent simulated sessions (distinct `session_id`s) toggle caveman
  independently — proven by an automated test, not manual inspection.
- A session with no `session_id` (opencode, manual runs, any current
  integration that doesn't send one) is byte-for-byte unaffected — same
  files, same paths, same content as before this change.
- `caveman-stats.js` attribution for a session with concurrent-session
  mode-log rows interleaved in time only reflects that session's own rows.
