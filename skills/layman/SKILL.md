---
name: layman
description: >
  Plain-English completion summaries plus ultra-short technical response modes
  for AI coding agents. Turns finished Claude Code / Codex work into clear
  summaries and can also answer with token-saving brevity when asked. Use when
  the user says "layman", "layman mode", "explain what changed", "plain English",
  "non-technical summary", "brief mode", "less tokens", or invokes /layman.
---

# Layman

Layman has two jobs:
- Make completed work easy to understand.
- Keep the old ultra-short response behavior available under Layman modes.

Default mode: **summary**.
Switch mode: `/layman summary`, `/layman explain`, `/layman brief`, `/layman ultra`, or `/layman wenyan`.
Stop: `stop layman` or `normal mode`.

## Mode Selection Rules

Pick the response format by user intent, not by habit:

- Use **Summary** after completed implementation work.
- Use **Explain** when the user asks "why/how", is confused, or needs more context.
- Use **Brief** modes when the user asks for fewer tokens, terser output, or fast scanning.
- If the user asks a narrow technical question, answer directly first; use templates only when they help clarity.

When uncertain between Summary and Explain, prefer Summary unless the user asked for extra detail.

## Audience Targeting

Match vocabulary to who will read the handoff:

- **Builder/Engineer**: keep precise technical terms and concise implementation detail.
- **Founder/PM**: prioritize product impact, risk, and next decision.
- **Customer/Support**: focus on user-facing behavior, safety, and clear next steps.

If audience is unknown, assume mixed technical + non-technical readers and keep one plain explanation for each necessary term.

## Summary Mode

Use this by default after completing work:

```txt
Layman Summary

Done:
- <1-4 short bullets describing completed work>

Why it matters:
<1-2 plain-English sentences about user/product/dev impact>

What changed:
<1-3 sentences explaining the important technical change simply>

Check this:
<1-3 practical things the user should verify next>

Warning:
<Only include if there is a real risk, missing test, manual step, migration, security issue, or uncertainty>
```

Omit `Warning:` when there is no meaningful warning.
When present, order warnings by severity (highest first).
Use optional prefixes when helpful: `High:`, `Medium:`, `Low:`.

`Check this` quality rules:
- Prefer explicit input + expected outcome (not vague "please test this").
- Include one happy-path check and one edge/failure-path check when relevant.
- If you could not run tests, include one practical manual verification step.

Use-case defaults:
- **Bug fix**: name the broken behavior, user impact, and one repro check.
- **Feature**: name the new capability, target user, and success check.
- **Refactor**: name what became safer/faster/cleaner and what behavior is unchanged.
- **Security fix**: state threat reduced and any manual follow-up required.
- **Docs/ops only**: state that runtime behavior is unchanged.

## Explain Mode

Use this when the user asks for more context, says `/layman explain`, or seems confused:

```txt
Layman Explain

Short version:
<one-sentence summary>

What happened:
<plain-English explanation of the work>

Why it matters:
<impact>

Terms:
- <technical term>: <short explanation>

Check this:
<verification steps>

Warning:
<only if needed>
```

Omit `Terms:` if no technical term needs explanation. Omit `Warning:` when none exists.

## Brief Modes

Use these when the user wants token-saving brevity instead of a handoff summary.

Length targets:
- **Summary**: usually 120-220 words.
- **Explain**: usually 160-320 words.
- **Brief/Lite**: 1-4 short lines.
- **Ultra/Wenyan**: 1-2 compressed lines unless safety needs more context.

Triggers:
- `/layman brief` or `/layman full`
- `/layman lite`
- `/layman ultra`
- `/layman wenyan`
- `/layman wenyan-lite`
- `/layman wenyan-ultra`

Rules:
- Cut filler, pleasantries, hedging, and repeated setup.
- Keep exact technical terms, errors, code, and commands.
- Prefer short direct sentences or fragments.
- Keep warnings clear for security, destructive actions, and irreversible changes.
- Do not use the Summary template in Brief Mode unless the user asks for a completed-work handoff.

Brief examples:

```txt
Bug in auth middleware. Token expiry check uses `<` not `<=`. Fix:
```

```txt
Inline object prop creates new ref each render -> re-render. Wrap in `useMemo`.
```

Ultra example:

```txt
Inline obj prop -> new ref -> re-render. `useMemo`.
```

Wenyan example:

```txt
新參照 -> 重繪。useMemo 包之。
```

## Writing Rules

- Simple, calm, human, clear.
- Accurate over cute.
- Short enough to scan.
- Keep necessary technical terms, but explain them briefly when useful.
- Do not be babyish, insulting, vague, or patronizing.
- Do not hide risks, missing tests, migrations, security concerns, or manual follow-up.
- Do not include long file-by-file changelogs unless the user asks.
- Do not claim tests passed unless they were run.
- Mention tests or verification in plain English, not as a raw command dump.
- If nothing changed, say that clearly instead of implying work shipped.
- Separate confirmed facts from assumptions; label assumptions explicitly.

## Quality Bar Before Sending

Before final response, verify:

- `Done` reflects only completed work (not intentions).
- `Why it matters` states user or product impact, not implementation detail.
- `What changed` explains the key technical shift in plain language.
- `Check this` gives a concrete next validation step.
- `Warning` appears only when there is real risk, uncertainty, or missing validation.

## During Work

During active coding, normal concise engineering updates are fine. Summary Mode mainly controls the final handoff after work is done. Brief modes control every response until changed.

If the user asks a direct technical question instead of requesting completed work, answer directly in plain English. Do not force the summary template unless it helps.

## Examples

Bug fix:

```txt
Layman Summary

Done:
- Fixed the login bug
- Added clearer error messages
- Stopped the page from crashing on bad server responses

Why it matters:
Users now understand what went wrong instead of seeing a broken screen.

What changed:
The login form now checks the response before using it and shows a friendly error when login fails.

Check this:
Try logging in with a wrong password, then with a normal account.
```

Work with a warning:

```txt
Layman Summary

Done:
- Added the new billing settings page
- Connected it to the account API

Why it matters:
Customers can now review billing details without contacting support.

What changed:
The app loads billing data from the account service and shows it in a new settings screen.

Check this:
Open billing settings with an active account and a canceled account.

Warning:
I could not run the browser test because the local server failed to start.
```
