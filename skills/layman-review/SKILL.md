---
name: layman-review
description: >
  Clear code review comments. Turns PR feedback into actionable plain-English
  findings with location, problem, impact, and fix. Use when user says
  "review this PR", "code review", "review the diff", "/review", or invokes
  /layman-review. Auto-triggers when reviewing pull requests.
---

Write code review comments that are clear, actionable, and respectful. Keep each finding focused: location, problem, impact when needed, fix.

## Rules

**Format:** `L<line>: <severity> <problem>. <fix>.` — or `<file>:L<line>: ...` when reviewing multi-file diffs.

**Severity prefix (optional, when mixed):**
- `🔴 bug:` — broken behavior, will cause incident
- `🟡 risk:` — works but fragile (race, missing null check, swallowed error)
- `🔵 nit:` — style, naming, micro-optim. Author can ignore
- `❓ q:` — genuine question, not a suggestion

**Avoid:**
- "I noticed that...", "It seems like...", "You might want to consider..."
- "This is just a suggestion but..." — use `nit:` instead
- "Great work!", "Looks good overall but..." — say it once at the top, not per comment
- Restating what the line does — the reviewer can read the diff
- Hedging ("perhaps", "maybe", "I think") — if unsure use `q:`

**Keep:**
- Exact line numbers
- Exact symbol/function/variable names in backticks
- Concrete fix, not "consider refactoring this"
- The *why* if the fix isn't obvious from the problem statement

## Examples

❌ "I noticed that on line 42 you're not checking if the user object is null before accessing the email property. This could potentially cause a crash if the user is not found in the database. You might want to add a null check here."

✅ `L42: 🔴 bug: user can be null after .find(). Add guard before .email.`

❌ "It looks like this function is doing a lot of things and might benefit from being broken up into smaller functions for readability."

✅ `L88-140: 🔵 nit: 50-line fn does 4 things. Extract validate/normalize/persist.`

❌ "Have you considered what happens if the API returns a 429? I think we should probably handle that case."

✅ `L23: 🟡 risk: no retry on 429. Wrap in withBackoff(3).`

## Auto-Clarity

Use fuller explanation for: security findings, architectural disagreements, and onboarding contexts where the author needs the "why". Keep it plain-English and specific.

## Boundaries

Reviews only. Does not write the code fix, approve/request changes, or run linters unless the user asks. Output comments ready to paste into the PR. "stop layman-review" or "normal mode": revert to the default review style.
