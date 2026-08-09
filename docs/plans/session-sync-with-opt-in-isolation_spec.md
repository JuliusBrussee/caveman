# Spec: caveman sync-by-default + opt-in session isolation

Companion to `session-sync-with-opt-in-isolation.md`. Behavior details, edge
cases, and design rationale that don't belong in the lean plan.

## Why this design and not "live re-poll `getDefaultMode()` on every hook fire"

An earlier framing considered having every hook invocation (not just true
startup) re-resolve `getDefaultMode()` and refresh the shared legacy file,
so synced sessions would pick up config changes mid-session-lifetime. This
was rejected as over-engineering for two reasons:

1. `getDefaultMode(startDir)` walks up from a `cwd` looking for repo-local
   `.caveman/config.json` / `.caveman.json`. Two synced sessions open in
   different repositories, with different repo-local overrides, would both
   try to write their own resolved value into the SAME single shared file —
   a genuine collision/ping-pong risk that doesn't exist in upstream's
   current design (upstream only ever resolves+writes once, at session
   creation).
2. It isn't actually what upstream does today. Upstream's own resume path
   (`caveman-activate.js`, `source !== 'startup'`) reads the flag file's
   *stored* content, not a fresh config resolution. Making our synced case
   MORE dynamic than upstream's own mechanism would be a behavior change
   nobody asked for, and would need its own review scrutiny for the
   multi-repo collision case above.

The chosen design (refresh from config only at true session startup, exactly
matching upstream) is strictly the smaller, more conservative change: it
fixes the actual reported bug (every session isolates on creation) without
inventing new propagation semantics upstream never had.

## Why `/caveman default` deletes rather than writes a sentinel

`off` is a written value (`'off'`), deliberately distinct from file absence,
because a *rejected* scoped file (symlink, oversized, corrupt) must fail
closed rather than silently falling back to legacy — the existing
`resolveFlag`/`resolveState` contract depends on absence meaning exactly one
thing ("never touched"). `/caveman default` is semantically "un-touch this
session" — the correct representation is deletion, restoring the session to
the exact state it was in before any `/caveman` action, not a new written
value that would need its own case in every consumer (`resolveFlag`,
`resolvePrev`, the Bash/PowerShell mirrors, `isActiveMode`, etc.).

## Interaction with `.prev` (independent-mode restore)

`.prev` exists to restore the prose mode active before a one-shot
independent mode (`/caveman-commit`, `/caveman-review`, `/caveman-compress`)
so the next ordinary prompt can bring it back. If a session isolates, uses
an independent mode (setting scoped `.prev`), then runs `/caveman default`
before the independent mode's one-shot restore has fired, the scoped `.prev`
must be deleted alongside the scoped active flag — leaving it behind would
be a dangling reference to a session identity that's no longer scoped, and
`resolvePrev`'s existing `sessionHasScopedIdentity` gate (comparing
`resolveFlag(...).path` against the scoped active path) would already
correctly treat it as no-longer-relevant once the active flag is gone. Delete
explicitly anyway for cleanliness (matches the existing `clear` action's
unlink-on-deactivate pattern) rather than relying on it becoming
unreachable.

## Why `/caveman default` is command-only, not natural-language

The plan does not add natural-language triggers ("sync caveman", "reset
caveman to default") for this action. Scope discipline: the user's own
description named the explicit command form only, and the existing NL
triggers (`stop caveman`, `activate caveman`, brevity phrases) are all
well-established, high-confidence phrasings that took multiple review
rounds each to get right (case sensitivity, question-vs-command
disambiguation, phrase boundary false positives) in the original PR. Adding
NL detection for a brand-new, less-obvious phrase risks the same class of
false-positive/false-negative bugs for comparatively low value — a user who
wants their session back in sync can type the explicit command. Revisit only
if requested.

## Backward compatibility for existing installs

An install that already has a real per-session scoped file on disk (e.g.
someone manually tested the closed PR's build before it was closed) is
unaffected: this plan doesn't touch the shape or naming of scoped files,
only which code paths create them. A stray leftover scoped file from manual
testing continues to behave exactly as documented (isolated), and
`/caveman default` cleanly reverts it like any other isolated session.

## Review Decisions

(populated as review rounds land — mirrors the plan doc's own section;
kept here too since Medium/Low/Nit dispositions and spec-gap downgrades
land in the spec per doctrine, not the lean plan)
