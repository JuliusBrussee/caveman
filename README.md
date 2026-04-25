<p align="center">
  <img src="https://em-content.zobj.net/source/apple/391/brain_1f9e0.png" width="120" />
</p>

<h1 align="center">Layman</h1>

<p align="center">
  <strong>Now anyone can code, for real.</strong>
</p>

<p align="center">
  <strong>🧠 why read big AI dump when simple words do trick — Claude Code / Codex skill that turns agent work into plain-English summaries anyone can understand</strong>
</p>

<p align="center">
  <a href="https://github.com/vamsi920/layman/stargazers"><img src="https://img.shields.io/github/stars/vamsi920/layman?style=flat&color=9b7b34" alt="Stars"></a>
  <a href="https://github.com/vamsi920/layman/commits/main"><img src="https://img.shields.io/github/last-commit/vamsi920/layman?style=flat" alt="Last Commit"></a>
  <a href="LICENSE"><img src="https://img.shields.io/github/license/vamsi920/layman?style=flat" alt="License"></a>
  <a href="https://github.com/JuliusBrussee/caveman"><img src="https://img.shields.io/badge/built%20on-caveman-245a41?style=flat" alt="Built on Caveman"></a>
</p>

<p align="center">
  <a href="#before--after">Before / After</a> ·
  <a href="#install">Install</a> ·
  <a href="#modes">Modes</a> ·
  <a href="#commands">Commands</a> ·
  <a href="#why-layman">Why Layman</a>
</p>

<p align="center">
  <strong>Caveman saves tokens. Layman saves understanding.</strong><br />
  Built on top of <a href="https://github.com/JuliusBrussee/caveman">Caveman</a>, so you still get the brevity modes too.
</p>

---

AI coding agents can now ship real product work. The weak point is the handoff.

Layman takes the giant technical dump at the end of a task and turns it into something a founder, PM, designer, student, or tired engineer can scan in seconds. And when you want pure token savings instead, Layman still gives you the Caveman-style brief modes.

## Before / After

<table>
<tr>
<td width="50%">

### 😵 Raw agent dump

> "Implemented auth form validation by adding schema-level checks, normalizing server responses, refactoring the submit handler, adding guard clauses for undefined API payloads, and updating related fixtures and tests."

</td>
<td width="50%">

### 🧠 Layman Summary

> **Done:** Fixed the login form, added clearer errors, and stopped the page from crashing.
>
> **Why it matters:** Users now understand what went wrong instead of seeing a broken screen.
>
> **What changed:** The form checks bad input before submit and safely handles bad server responses.
>
> **Check this:** Try logging in with a wrong password, then with a normal account.

</td>
</tr>
<tr>
<td width="50%">

### 🤖 Engineer-only handoff

> "Adjusted retry logic in billing webhook processing, patched null-state handling, and updated queue idempotency checks."

</td>
<td width="50%">

### 👀 Founder-friendly handoff

> **Done:** Made failed billing retries more reliable.
>
> **Why it matters:** Failed payments are less likely to get stuck silently.
>
> **Check this:** Trigger a failed payment and confirm the retry shows up once.

</td>
</tr>
</table>

**Same work. Less confusion. Better follow-up.**

### Still want fewer tokens?

Layman is built on top of Caveman, so the brevity side is still here:

```txt
/layman brief
/layman lite
/layman full
/layman ultra
/layman wenyan
```

```txt
┌──────────────────────────────────────────────┐
│  UNDERSTANDING SAVED     ████████ big time   │
│  TOKEN-SAVING MODES      ████████ included   │
│  INSTALL                 █              easy │
│  NON-TECH FRIENDLY       ████████ yes        │
└──────────────────────────────────────────────┘
```

## Install

Pick your agent. One command. Done.

### Quick Install

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

| Agent | Install |
|-------|---------|
| **Claude Code** | `claude plugin marketplace add vamsi920/layman && claude plugin install layman@layman` |
| **Claude Code hooks** | `bash <(curl -s https://raw.githubusercontent.com/vamsi920/layman/main/hooks/install.sh)` |
| **Codex** | Clone repo → `/plugins` → Search `Layman` → Install |
| **Gemini CLI** | `gemini extensions install https://github.com/vamsi920/layman` |
| **Cursor** | `npx skills add vamsi920/layman -a cursor` |
| **Windsurf** | `npx skills add vamsi920/layman -a windsurf` |
| **Copilot** | `npx skills add vamsi920/layman -a github-copilot` |
| **Cline** | `npx skills add vamsi920/layman -a cline` |
| **Any other agent** | `npx skills add vamsi920/layman` |

## What You Get

| Feature | Why it matters |
|---------|----------------|
| **Layman Summary Mode** | Clear final handoff: what got done, why it matters, what changed, and what to check next |
| **Layman Explain Mode** | Slightly deeper plain-English explanation when the user wants more context |
| **Brief / Lite / Full / Ultra / Wenyan** | Caveman-style brevity modes for token savings and faster scanning |
| **layman-commit** | Clear Conventional Commit messages |
| **layman-review** | Clear, actionable review comments |
| **layman:compress** | Compress long Markdown memory files while preserving important facts |

## Modes

### 1. Summary

Default mode. Best for completed work.

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

Use `Warning:` only when there is a real risk, missing test, migration, or manual follow-up.

### 2. Explain

Same plain-English goal, but with a bit more context.

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

### 3. Brief

This is the Caveman side inside Layman. Use it when the goal is fewer tokens, not a founder-friendly handoff.

**Pick your level:**

<table>
<tr>
<td width="25%">

#### 🪶 Lite

> "The login bug came from the expiry check. The code used `<` instead of `<=`."

</td>
<td width="25%">

#### 🪨 Full

> "Expiry check wrong. Use `<=`."

</td>
<td width="25%">

#### 🔥 Ultra

> "Expiry `<=` bug. Fix."

</td>
<td width="25%">

#### 📜 Wenyan

> "驗期判失，當用 `<=`。"

</td>
</tr>
</table>

Use `normal mode` or `stop layman` to turn Layman off.

## Why Layman

AI can write the code now. The real bottleneck is the handoff.

Layman is for:

- founders who need to know what shipped without reading implementation noise
- product and design people reviewing AI work
- developers who want shorter, cleaner summaries after long tasks
- teams that still need warnings, risks, and verification steps kept honest

Layman does not try to make technical work childish. It keeps the important terms when needed. It just removes the dump.

And because it is built on top of Caveman, it does two jobs in one repo:

- **Layman Summary / Explain** when the goal is understanding
- **Layman Brief modes** when the goal is token savings

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

If the user asks for brevity instead, switch to Layman Brief mode.
```

## Development

Edit the source files, then run verification:

```bash
python3 tests/verify_repo.py
python3 -m unittest tests/test_hooks.py
```

Main files:

- `skills/layman/SKILL.md` - core Layman behavior
- `rules/layman-activate.md` - always-on rule text for repo integrations
- `hooks/` - Claude Code activation, mode tracking, and statusline hooks
- `plugins/layman/` - Codex plugin package

## License

MIT
