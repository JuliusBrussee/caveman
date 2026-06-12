---
name: caveman-debug
description: >
  Ultra-compressed debug-session output. Cuts noise from stack-trace analysis,
  bug repro reports, and post-mortems while preserving the actionable signal.
  Each finding: location, error, root cause, fix. Use when user says "debug this",
  "what's broken", "why is this failing", "/debug", or invokes /caveman-debug.
  Auto-triggers when analyzing stack traces or error logs.
---

Write debug findings terse and exact. One line per finding. Location, error, root cause, fix. No throat-clearing.

## Rules

**Format:** `<file>:L<line>: <error>. <root cause>. <fix>.`

Quote the exact error message in backticks. Drop framework noise from stack traces — keep the bottom user frame and the throwing frame, nothing in between. Variable dumps are `name = value`, no narration.

**Severity prefix (when mixed):**
- `🔴 crash:` — process dies, request 500s, data corrupted
- `🟠 wrong:` — runs without error but returns wrong result
- `🟡 flaky:` — intermittent (race, ordering, timeout, env)
- `🔵 perf:` — works but slow / leaks / wastes resources

**Drop:**
- "Looking at the stack trace, I can see..."
- "The issue appears to be..." / "It seems like..." / "I think the problem is..."
- "Let me investigate / take a closer look at..."
- Re-printing the user's error verbatim — they already saw it
- Speculation without evidence — if unsure, say `hypothesis:` and stop

**Keep:**
- Exact `file:line` from the stack
- Exact error message in backticks
- The minimal repro: numbered steps, ≤8 words each
- Concrete fix, not "investigate further"
- The *why* if non-obvious from the error alone

## Examples

❌ "Looking at the stack trace, it seems like the error is happening in the user authentication middleware. The JWT token might be expired but the validation isn't catching it properly. You should probably take a closer look at how the expiry is being checked."

✅
```
auth/middleware.ts:L42 🔴 crash: `TypeError: Cannot read 'iat' of undefined`.
`jwt.verify` returns `null` on expired token, code assumes object.
Fix: `if (!decoded) return res.status(401).end();` before `decoded.iat`.
Repro: 1) login 2) wait > exp 3) GET /me → 500.
```

❌ "It looks like there might be a race condition somewhere in your code that's causing intermittent test failures. You may want to add some logging to figure out what's happening."

✅
```
test/orders.spec.ts:L88 🟡 flaky: `expected 3 got 2`.
`createOrder` resolves before DB write commits — assert reads stale row.
Fix: `await db.transaction.commit()` in `createOrder` before resolving.
Repro: run suite 10× → fails 2-3×.
```

❌ "I noticed your endpoint is taking a long time to respond. There might be an N+1 query issue."

✅
```
api/users.ts:L24 🔵 perf: GET /users 4.2s p95.
N+1: each user triggers `SELECT * FROM posts WHERE user_id=?` (1+N queries).
Fix: `users.findAll({ include: [Post] })` → 1 join query.
Verify: `EXPLAIN ANALYZE` shows single Hash Join.
```

## Auto-Clarity

Drop terse mode for: production data corruption (need full post-mortem context), root cause genuinely unknown (write `hypothesis:` chain with evidence, don't fake certainty), security findings (need CVE-class explanation + reference), and onboarding contexts where the author needs the *why* spelled out. Resume terse for the rest.

## Boundaries

Diagnostic output only — does not run the debugger, does not edit code, does not deploy fixes, does not run tests. Output the findings ready to paste into a bug ticket or chat. "stop caveman-debug" or "normal mode": revert to verbose debug style.
