🪵

# Layman

**Make AI coding output instantly understandable.**

**🪵 Built on Caveman speed. Upgraded for real-world clarity.**  
Turn giant technical agent dumps into short, plain-English handoffs people actually read.



[Before / After](#before--after) · [Install](#install-in-60-seconds) · [Results](#results-you-can-expect) · [Modes](#modes) · [Commands](#commands)

---

## Why people install this fast

AI agents can ship code now. The bottleneck is understanding what they just did.

Layman makes the handoff clear in seconds:

- **Done**: what actually shipped
- **Why it matters**: user/product impact
- **What changed**: key technical change in simple words
- **Check this**: exact next verification step
- **Warning**: only when there is a real risk or missing validation

And yes, you still get Caveman-style brevity modes for token savings.

## Best use cases

Layman is strongest when code is done but communication is weak:

- **Solo builder to users**: turn technical changelogs into user-facing release notes
- **Founder to team**: explain AI agent work to non-engineers fast
- **Engineer handoff**: make PR or task completion updates easier to scan
- **Support/internal ops**: convert bug-fix details into safe customer messages
- **Agency/freelance delivery**: send clear client updates without jargon

## Before / After


### 🔴 Before (noise overload)

```txt
Refactored auth flow by introducing schema-level validation and
response normalization in `authSubmitHandler()`. Added defensive null
guards for `payload.user` and `payload.session`, split error branches
by transport vs domain failure, and rewired render path to consume
mapped error tokens. Updated fixtures + snapshots to align with new
error-state contract and touched retry semantics in edge path.
```

```txt
Status:
Clarity       ▒▒▒▒▒▒▒▒  20%
Actionability ▒▒▒▒▒▒    30%
Readability   ▒▒▒▒▒     25%
```



### 🟢 After (Layman Summary)

```txt
Layman Summary

Done:
- Fixed the login form when credentials are wrong
- Prevented crashes when the server sends bad data
- Added clear error messages people can act on

Why it matters:
Users get a calm, clear message instead of a confusing broken screen.

What changed:
The form now checks input before sending it, then safely handles bad
server responses so the page stays stable.

Check this:
Try one bad login and one valid login. You should see clear feedback
both times, with no crash.
```

```txt
Status:
Clarity       ██████████  95%
Actionability ██████████  95%
Readability   ██████████  90%
```



### 🟠 Before (engineer-only billing note)

```txt
Patched billing retry pipeline by propagating idempotency key through
webhook fan-out, hardening null-state hydration in subscription merge,
and recalibrating queue backoff for transient PSP 5xx. Added replay
guard in event consumer to prevent duplicate mutation on at-least-once
delivery and updated telemetry labels for capture-failure branch.
```



### 🔵 After (founder-friendly billing note)

```txt
Done:
- Made failed billing retries much more reliable
- Protected the system from duplicate webhook side effects

Why it matters:
Failed payments are far less likely to get stuck silently or run twice.

Check this:
Trigger one failed payment and replay the same webhook event. You
should still see one safe billing outcome.
```



**Same code changes. Way better understanding.**

## Install in 60 seconds

Pick your agent. Run one command. Start using immediately.

### Quick install

```bash
# Claude Code plugin
claude plugin marketplace add vamsi920/layman
claude plugin install layman@layman

# Claude Code standalone hooks
bash <(curl -s https://raw.githubusercontent.com/vamsi920/layman/main/hooks/install.sh)

# Windows PowerShell
irm https://raw.githubusercontent.com/vamsi920/layman/main/hooks/install.ps1 | iex

# Gemini CLI
gemini extensions install https://github.com/vamsi920/layman

# Other agents
npx skills add vamsi920/layman
npx skills add vamsi920/layman -a cursor
npx skills add vamsi920/layman -a windsurf
```


| Agent                 | Install                                                                                   |
| --------------------- | ----------------------------------------------------------------------------------------- |
| **Claude Code**       | `claude plugin marketplace add vamsi920/layman && claude plugin install layman@layman`    |
| **Claude Code hooks** | `bash <(curl -s https://raw.githubusercontent.com/vamsi920/layman/main/hooks/install.sh)` |
| **Codex**             | Clone repo -> `/plugins` -> Search `Layman` -> Install                                    |
| **Gemini CLI**        | `gemini extensions install https://github.com/vamsi920/layman`                            |
| **Cursor**            | `npx skills add vamsi920/layman -a cursor`                                                |
| **Windsurf**          | `npx skills add vamsi920/layman -a windsurf`                                              |
| **Copilot**           | `npx skills add vamsi920/layman -a github-copilot`                                        |
| **Cline**             | `npx skills add vamsi920/layman -a cline`                                                 |
| **Any other agent**   | `npx skills add vamsi920/layman`                                                          |


## Results you can expect

- Understand completed AI work in seconds, not minutes
- Fewer "wait, what changed?" follow-up questions
- Better communication with non-technical teammates
- Cleaner next-step verification after each task
- Optional token-saving mode when you want maximum compression

## Modes

### 1) Summary (default)

Use after coding tasks are complete.

```txt
Layman Summary

Done:
- Fixed the login bug
- Cleaned the signup form
- Added better error messages

Why it matters:
Users now understand what failed instead of seeing a broken flow.

What changed:
The form validates bad input and safely handles malformed responses.

Check this:
Try signup with an invalid email and a weak password.
```

Use `Warning:` only when there is real risk, missing test, migration, or manual follow-up.

### 2) Explain

Same plain-English style, with more context and terms.

```txt
Layman Explain

Short version:
Signup now catches invalid input earlier.

What happened:
Validation moved closer to submit so users get immediate feedback.

Why it matters:
This reduces failed signups caused by confusing errors.

Terms:
- Validation: checking input format before sending data.

Check this:
Test bad email, weak password, then normal valid signup.
```

### 3) Brief modes (Caveman power included)

Use when you want fewer tokens, faster scanning.

```txt
/layman brief
/layman lite
/layman full
/layman ultra
/layman wenyan
```


#### Lite

`Expiry check used "<" instead of "<=".`



#### Full

`Expiry check wrong. Use "<=".`



#### Ultra

`Expiry "<=" bug. Fix.`



#### Wenyan

`验期判失，当用 <=。`



Use `normal mode` or `stop layman` to turn it off.

## Use it in a real workflow

1. Let your coding agent finish implementation.
2. Ask for `/layman` (or set it as default once).
3. Paste the handoff directly into Slack, Linear, Notion, or email.
4. Validate the single `Check this` step before merge/release.

The goal is not prettier text. The goal is faster alignment with fewer follow-up questions.

## Commands


| Command                   | What it does                                               |
| ------------------------- | ---------------------------------------------------------- |
| `/layman`                 | Turn on Layman Summary mode                                |
| `/layman summary`         | Use the default summary handoff                            |
| `/layman explain`         | Use more detailed plain-English explanation                |
| `/layman brief`           | Turn on short technical responses                          |
| `/layman lite`            | Turn on light brevity                                      |
| `/layman full`            | Turn on stronger brevity                                   |
| `/layman ultra`           | Turn on maximum compression                                |
| `/layman wenyan`          | Turn on classical Chinese terse mode                       |
| `/layman-commit`          | Generate clear Conventional Commit messages                |
| `/layman-review`          | Generate clear actionable review comments                  |
| `/layman:compress <file>` | Simplify long Markdown memory files while preserving facts |


## Keep it always on

Add this to your agent rules/system prompt:

```txt
When you finish coding work, respond with a Layman Summary.

Include:
- Done
- Why it matters
- What changed
- Check this
- Warning, only if needed

Use simple plain English. Keep important technical terms when needed.
Do not be vague, insulting, or overly long.

If the user asks for brevity, switch to Layman Brief mode.
```

## Development

Edit source files, then verify:

```bash
python3 tests/verify_repo.py
python3 -m unittest tests/test_hooks.py
```

Main files:

- `skills/layman/SKILL.md` - core behavior
- `rules/layman-activate.md` - always-on rule text
- `hooks/` - activation, mode tracking, statusline hooks
- `plugins/layman/` - Codex plugin package

## License

MIT