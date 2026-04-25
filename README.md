<h1 align="center">Layman</h1>

<p align="center">
  <strong>🧠 why read big AI dump when simple words do trick — Claude Code / Codex skill that turns agent work into plain-English summaries anyone can understand</strong>
</p>

<p align="center">
  <a href="#why-layman">Why Layman</a> ·
  <a href="#before--after">Before / After</a> ·
  <a href="#modes">Modes</a> ·
  <a href="#install">Install</a>
</p>

---

AI coding agents can now build real software. The hard part is understanding what they just did.

**Layman saves understanding. Brief Mode saves tokens.**

Layman is a Claude Code / Codex skill that turns completed agent work into short, calm, plain-English summaries. It is built for founders, PMs, designers, students, and developers who do not want a giant technical dump after every task.

Layman also includes token-saving brief modes for people who want ultra-short technical responses.

## Quick Install

```bash
# Claude Code plugin
claude plugin marketplace add vamsi920/layman
claude plugin install layman@layman

# Claude Code standalone hooks
bash <(curl -s https://raw.githubusercontent.com/vamsi920/layman/main/hooks/install.sh)

# Gemini CLI
gemini extensions install https://github.com/vamsi920/layman

# Other agents
npx skills add vamsi920/layman
npx skills add vamsi920/layman -a cursor
npx skills add vamsi920/layman -a windsurf
```

## Why Layman

When an AI agent finishes work, the final response often reads like a build log: file names, implementation details, test commands, warnings, and jargon all mixed together.

Layman changes the handoff into something useful:

- **What got done** — the actual outcome
- **Why it matters** — the user or product impact
- **What changed** — the important technical change, in simple words
- **What to check next** — the next practical verification step
- **Warnings** — only when something important needs attention

The goal is not to make answers babyish. The goal is to make coding work understandable.

## Before / After

### Before

```txt
Implemented auth form validation by adding schema-level checks, normalizing server responses,
refactoring the submit handler, and updating the error-state rendering path. Also added
guard clauses for undefined API payloads and adjusted test fixtures.
```

### After

```txt
Layman Summary

Done:
- Fixed the login form
- Added clearer error messages
- Protected the page from bad server responses

Why it matters:
Users now see what went wrong instead of hitting a confusing broken screen.

What changed:
The form checks the email and password before sending them, then shows a clear message if the server rejects the login.

Check this:
Try logging in with a wrong email and a weak password.
```

## Modes

### Layman Summary Mode

Default mode. Use this after completing coding work.

```txt
Layman Summary

Done:
- Fixed the login bug
- Cleaned the signup form
- Added better error messages

Why it matters:
Users now understand what went wrong instead of seeing a broken or confusing screen.

What changed:
The form now checks bad input, shows a clear message, and avoids crashing.

Check this:
Try signing up with a wrong email and a weak password.
```

Add a `Warning:` section only if there is a real risk, missing test, migration step, or manual follow-up.

### Layman Explain Mode

Use this when the user asks for more context. It stays plain-English but adds a little more detail and explains necessary technical terms.

```txt
Layman Explain

Short version:
The signup form now catches bad input before it reaches the server.

What happened:
The form validation was moved closer to the submit button, so users get feedback right away.

Why it matters:
This avoids failed signups caused by unclear errors.

Terms:
- Validation: checking that the email and password look valid before sending them.

Check this:
Try the signup flow with a bad email, a weak password, and a normal valid account.
```

### Layman Brief Mode

Use this when the goal is token-saving brevity instead of plain-English handoff summaries.

```txt
/layman brief
/layman lite
/layman ultra
/layman wenyan
```

Example:

```txt
Bug in auth middleware. Token expiry check uses `<` not `<=`. Fix:
```

Use `normal mode` to turn it off.

## Tone Rules

- Use simple, calm, human language.
- Keep important technical terms when they matter.
- Explain a term briefly when a non-technical reader may need it.
- Do not hide risks, missing tests, migrations, or manual steps.
- Do not over-explain.
- Do not talk down to the reader.
- Keep the summary short enough to scan.

## Install

| Agent | Install |
|-------|---------|
| **Claude Code** | `claude plugin marketplace add vamsi920/layman && claude plugin install layman@layman` |
| **Codex** | Clone repo → `/plugins` → Search `Layman` → Install |
| **Gemini CLI** | `gemini extensions install https://github.com/vamsi920/layman` |
| **Cursor** | `npx skills add vamsi920/layman -a cursor` |
| **Windsurf** | `npx skills add vamsi920/layman -a windsurf` |
| **Copilot** | `npx skills add vamsi920/layman -a github-copilot` |
| **Cline** | `npx skills add vamsi920/layman -a cline` |
| **Any other agent** | `npx skills add vamsi920/layman` |

## Commands

| Command | What it does |
|---------|--------------|
| `/layman` | Turn on Layman Summary Mode |
| `/layman summary` | Use the default summary handoff |
| `/layman explain` | Use the more detailed plain-English explanation |
| `/layman brief` | Turn on short technical responses |
| `/layman lite` | Turn on light brevity |
| `/layman full` | Turn on stronger brevity |
| `/layman ultra` | Turn on maximum compression |
| `/layman wenyan` | Turn on classical Chinese terse mode |
| `/layman-commit` | Generate a clear Conventional Commit message |
| `/layman-review` | Generate clear, actionable review comments |
| `/layman:compress <file>` | Simplify long Markdown memory files while preserving facts |

Say `stop layman` or `normal mode` to turn it off.

## Want It Always On?

Add this to your agent rules or system prompt:

```txt
When you finish coding work, respond with a Layman Summary.

Include:
- Done
- Why it matters
- What changed
- Check this
- Warning, only if needed

Use simple plain English. Keep important technical terms when needed. Do not be babyish, insulting, vague, or overly long.
```

## Who It Helps

- Non-technical founders who need to understand what shipped
- Product teams reviewing AI-generated work
- Developers who want cleaner handoffs
- Students learning from agent output
- Anyone using Claude Code or Codex to build software

## Development

Edit the source files, then run verification:

```bash
python3 tests/verify_repo.py
python3 -m unittest tests/test_hooks.py
```

Main files:

- `skills/layman/SKILL.md` — core Layman behavior
- `rules/layman-activate.md` — always-on rule text for repo integrations
- `hooks/` — Claude Code activation, mode tracking, and statusline hooks
- `plugins/layman/` — Codex plugin package

## License

MIT
