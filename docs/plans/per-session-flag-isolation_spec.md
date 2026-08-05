---
work-ids: []
---

# Per-Session Caveman Flag Isolation — Spec

Companion to `per-session-flag-isolation.md`. Behavior details, edge cases,
and design rationale that don't belong in the lean plan.

## Why session-id-in-filename over a session-keyed subdirectory or a single JSON map

Three shapes were considered for "many sessions, one config dir":

1. **One file per session, id in the filename** (`.caveman-active-<id>`) —
   chosen. Matches the existing single-file-per-concern pattern
   (`.caveman-active`, `.caveman-active.prev`, `.caveman-statusline-suffix`
   are all flat files today), so every existing symlink-hardening helper
   (`safeWriteFlag`/`readFlag`) works unchanged — they already operate on an
   arbitrary `flagPath` argument, so scoping is purely a caller-side path
   change, zero changes to the read/write primitives themselves.
2. **A subdirectory per session** (`sessions/<id>/active`) — rejected. Adds a
   directory-creation step to every write path and a directory-enumeration
   step to uninstall, for no benefit over (1) given flat files already work.
3. **A single JSON map file** (`{ "<id>": "mode", ... }`) — rejected. Turns
   every read into a JSON-parse of a file that grows without bound (nothing
   currently prunes stale session entries), and turns every write into a
   read-modify-write race between concurrent sessions — exactly the
   concurrency hazard the current flat-file + `O_APPEND` design avoids.

## Why the mode-log stays one shared file with an added field, not one log per session

`caveman-stats.js`'s lifetime aggregation (`aggregateHistory`,
`.caveman-history.jsonl`) already spans sessions by `session_id` inside a
single file — the mode-transition log fragmenting into per-session files
while the history log doesn't would be an inconsistent pattern for no
reason. Filtering rows by `session_id` at read time is a two-line change in
`caveman-stats.js`; migrating to per-session log files would touch the
append path (`recordModeChange`), every future consumer, AND still need a
"which log files exist" enumeration step somewhere. Not worth it for a file
that's already safe for concurrent writers.

## Read-fallback edge cases

**Two sessions, one legacy file, one already-scoped.** Session A predates
this upgrade (its transcript started before the deploy) and has been
running long enough that Claude Code's `SessionStart` hasn't re-fired
(`/resume`, `/clear`, compaction) — call it "mid-flight." Session B starts
fresh after the upgrade. Session A keeps reading/writing the legacy global
path until IT toggles caveman (at which point `caveman-mode-tracker.js`
resolves its own `session_id` from the hook payload — that's independent of
whether `SessionStart` re-ran, since `UserPromptSubmit` fires on every
prompt regardless — and switches A onto its own scoped file from that point
on, writing either an active mode or literal `off`). Session B, from its
very first `SessionStart`, writes only to its own scoped file (whatever the
resolved mode, including `off`) and never touches the legacy path. B never
reads the legacy file after its first `SessionStart` — writes always target
the scoped path unconditionally once a valid session id is resolved. Only
A, which hasn't written anything since the upgrade, keeps reading/writing
legacy until its next toggle. No split second where B's actions are visible
to A or vice versa.

**A session's own scoped file, once created, never falls back again — even
when explicitly turned off.** This is the case the v1 review caught as a
Critical finding: because "off" is now WRITTEN (`safeWriteFlag(flagPath,
'off')`) rather than deleted, a session that ran `/caveman off` still has a
scoped file on disk — `resolveFlag` finds it via `lstatSync` (no `ENOENT`),
`readFlag` returns the valid mode string `'off'`, and every caller correctly
treats that as "inactive" via `isActiveMode` without ever falling through to
the legacy file. Only a session whose scoped file has literally never been
written (true `ENOENT`) falls back — and once ANY write happens for that
session (including turning it off), fallback for that session id is
permanently over for the life of that scoped file.

**Uninstall during an active session.** If `cli/install.js --uninstall`
runs while sessions are live, the readdirSync-based enumeration described in
the plan removes every currently-existing `.caveman-active*` file at that
instant, scoped or legacy — including scoped files whose content is `off`.
A still-running session's next hook invocation simply recreates its own
scoped file on its next toggle (or SessionStart re-fire, which will
`ENOENT` since the file was removed and correctly fall back to the legacy
path — which uninstall also just removed, so the net effect is "no scoped
file exists," `readFlag` returns null, and every consumer treats that as
inactive, matching uninstall's intent) — no crash, no special-cased
"flag missing" error path.

## Why `caveman-stats.js` takes an explicit `--session-id` instead of deriving one from the transcript filename

A v1 draft of this plan reused the pre-existing
`sessionId = path.basename(sessionFile, '.jsonl')` derivation (already used
for the `.caveman-history.jsonl` lifetime-append key) for the flag/mode-log
resolution too — one line, no new CLI flag. Tier-1 review caught the flaw:
that derivation runs **unconditionally** whenever `caveman-stats.js` is
invoked with a transcript file, regardless of whether the *hook* that
invoked it actually had a valid `session_id`. A hook with a missing or
invalid `session_id` correctly falls back to the legacy flag path for its
own reads/writes — but if it still passes `--session-file <transcript_path>`
to the stats subprocess (which it always does, since `transcript_path`
comes from the hook payload independently of `session_id`), stats would
derive its OWN session id from that transcript path and read the SCOPED
flag while the hook itself is on the legacy path. Split-brain: two
processes serving the same `/caveman-stats` invocation, disagreeing about
which flag file is authoritative.

The fix threads the tracker's own already-sanitized `sessionId` (or nothing,
when it resolved to `null`) through explicitly as `--session-id`, so stats
and the hook that invoked it always agree. `caveman-stats.js` still derives
`path.basename(sessionFile, '.jsonl')` for the UNRELATED lifetime-history
key — that line doesn't change, since a direct `node caveman-stats.js` or
`--all`/`--since` invocation has no hook context and no `session_id` to pass,
and lifetime aggregation was never scoped to begin with.

## Why "off" is now written instead of deleted

Today, the flag file's mere *existence* means "caveman is on at whatever
mode the content says," and absence means "off" — a two-state encoding
(present/absent) riding on top of a value encoding (the mode string).
Scoping breaks this because "absent" now has to mean two different things
for the same session id: "never touched" (should fall back to a shared
legacy state) and "explicitly turned off" (should NOT fall back to
anything — this session is off, full stop). Reusing "absent" for both is
exactly the Critical bug the v1 review found. The fix promotes "off" from
an implicit (absence) encoding to an explicit (written value) encoding —
`'off'` was already a valid `VALID_MODES` member (previously used only to
validate the *config file's* `defaultMode` field, never written to the
flag itself), so no whitelist change was needed, only a change to what
"clear" does or write. The one behavior change this introduces for
`readFlag`'s callers is that `activeMode` can now be the truthy string
`'off'` where previously it would have been `null` — every caller that used
to rely on bare truthiness to mean "caveman is active" needs the new
`isActiveMode()` check instead. This is a small, mechanical, one-line
change at each of the (few) call sites that make this check.

## Statusline sanitizer: why no `jq` dependency, and why reject-not-strip

`caveman-statusline.sh` today has zero external dependencies beyond POSIX
shell builtins (`head`, `tr`, `printf`) — deliberately, since it runs on
every keystroke render and the README documents it as dependency-free.
Adding a `jq` dependency just for `session_id` extraction would be a
regression for users who don't have `jq` installed (not universal on stock
macOS or minimal Linux images). A `session_id` value is always a single
scalar string in a flat JSON object (no nesting to worry about for THIS
field specifically) so a `grep -o` + `sed` pair extracting
`"session_id"\s*:\s*"([^"]*)"` is sufficient and keeps the zero-dependency
property.

**The v1 draft got the validation step wrong, and Tier-1 review caught it
as Critical.** The existing MODE sanitization one line below the flag read
(`tr -cd 'A-Za-z0-9_-'` then `head -c 64`) is safe for MODE specifically
because the result is subsequently checked against an exact `case`
whitelist (`off|lite|full|...`) — stripping first doesn't matter, since
anything that doesn't land on an exact whitelisted word is thrown away
regardless. `session_id` has no such whitelist to fall back on; it becomes
a path component directly. Applying the same strip-then-truncate idiom to
it means an attacker-controlled value like `../../etc/passwd` strips to
`etcpasswd` — a syntactically valid-looking id that silently passes as
"sanitized," when the correct behavior is outright rejection. The fix:
validate the RAW extracted value against a whole-string anchored pattern
first (every character in the class, length within bound, nothing
left over); only a value that matches unmodified, in full, is used to build
a scoped path. Any non-match is treated exactly like a missing
`session_id` (legacy fallback), never coerced into "the closest valid-ish
thing." The mode-value sanitizer below it is intentionally left as
strip-then-whitelist-check — that one's fine, because a whitelist check
after stripping is still a full-value validation, just against string
equality instead of a regex.

## Test-vector table (Phase 4)

| Input | Expected `sanitizeSessionId` result | Notes |
|---|---|---|
| `"a1b2c3d4-e5f6-7890-abcd-ef1234567890"` | same string | canonical Claude Code session UUID shape |
| `"test-sess-1"` | same string | short alnum+hyphen id, as used in the 2026-07-06 manual test |
| `""` | `null` | empty string |
| `"../../etc/passwd"` | `null` | path traversal — must reject whole, never sanitize down to `etcpasswd` |
| `"abc/def"` | `null` | embedded path separator — looks alnum-ish at a glance but fails a whole-string match |
| 200-char alnum string | `null` | exceeds 128-char cap |
| `null` | `null` | JSON null (field absent or explicitly null) |
| `12345` (number, not string) | `null` | wrong JSON type — `data.session_id` should always be a string from Claude Code, but a hostile/malformed payload isn't trusted to guarantee that |

## Follow-ups explicitly deferred (not part of this PR)

- Consolidating the three separate uninstall implementations
  (`cli/install.js`'s `STATE_FILES_TO_REMOVE` loop, `uninstall.sh`,
  `uninstall.ps1`) into one shared implementation — this plan brings the
  two standalone scripts up to feature parity with `cli/install.js`'s
  existing state-file coverage (a real gap this plan closes, since
  `cli/install.js` already fixed it upstream under issue #635 but the two
  standalone scripts never got the equivalent fix) and adds the new
  scoped-variant enumeration to all three, but the underlying duplication
  across three near-identical implementations is a pre-existing
  maintenance smell this plan doesn't otherwise address.
- `opencode` native plugin per-session isolation — same problem exists there
  (`session.created` writes one global flag), but it's a different runtime
  contract (Bun plugin, no `CLAUDE_CONFIG_DIR`/hook-JSON-on-stdin shape) and
  deserves its own design pass rather than forcing this plan's Claude-Code-
  specific mechanism onto it.
- Regenerating `src/hooks/checksums.sha256` and bumping `PINNED_REF` in
  `cli/install.js` — release-time chore, happens when this change actually
  ships in a tagged release, not in this PR's diff.
