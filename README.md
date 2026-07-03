<p align="center">
  <img src="docs/assets/caveman-logo-banner.png" alt="Caveman" width="720">
</p>

<p align="center">
  <strong>why use many token when few do trick</strong>
</p>

<p align="center">
  Make your AI coding agent talk like a caveman.<br>
  Same answers, <strong>65% fewer output tokens</strong>. Brain still big. Mouth small.
</p>

<p align="center">
  <a href="https://github.com/JuliusBrussee/caveman/stargazers"><img src="https://img.shields.io/github/stars/JuliusBrussee/caveman?style=flat&color=yellow" alt="Stars"></a>
  <a href="./INSTALL.md"><img src="https://img.shields.io/badge/works_with-30%2B_agents-orange?style=flat" alt="30+ agents"></a>
  <a href="https://github.com/JuliusBrussee/caveman/commits/main"><img src="https://img.shields.io/github/last-commit/JuliusBrussee/caveman?style=flat" alt="Last commit"></a>
  <a href="LICENSE"><img src="https://img.shields.io/github/license/JuliusBrussee/caveman?style=flat" alt="License"></a>
</p>

<p align="center">
  <a href="#before--after">See it</a> ·
  <a href="#install">Install</a> ·
  <a href="#pick-your-grunt">Levels</a> ·
  <a href="#what-you-get">What you get</a> ·
  <a href="#benchmarks">Benchmarks</a> ·
  <a href="#the-whole-cave">Ecosystem</a> ·
  <a href="#caveman-2">Caveman 2</a>
</p>

---

Caveman is a skill/plugin for [Claude Code](https://docs.anthropic.com/en/docs/claude-code), Codex, Gemini, Cursor, Windsurf, Cline, Copilot, and 30+ other agents. Install once. Agent drops the filler and answers in tight caveman-speak, keeping code, commands, and errors byte-for-byte exact. You save output tokens on every reply, forever.

## Before / After

<table>
<tr>
<th width="50%">🗣️ Normal agent — 69 tokens</th>
<th width="50%"><img src="docs/assets/dancing-rock.svg" width="18" height="18" alt=""> Caveman agent — 19 tokens</th>
</tr>
<tr>
<td valign="top">

> The reason your React component is re-rendering is likely because you're creating a new object reference on each render cycle. When you pass an inline object as a prop, React's shallow comparison sees it as a different object every time, which triggers a re-render. I'd recommend using useMemo to memoize the object.

</td>
<td valign="top">

> New object ref each render. Inline object prop = new ref = re-render. Wrap in `useMemo`.

</td>
</tr>
<tr>
<td valign="top">

> Sure! I'd be happy to help you with that. The issue you're experiencing is most likely caused by your authentication middleware not properly validating the token expiry. Let me take a look and suggest a fix.

</td>
<td valign="top">

> Bug in auth middleware. Token expiry check use `<` not `<=`. Fix:

</td>
</tr>
</table>

Same fix. Third of the words. Nothing technical lost.

```
┌────────────────────────────────────────────┐
│   output tokens saved   █████████       65% │
│   input tokens saved    ░░░░░░░░░         0% │
│   technical accuracy    █████████      100% │
│   vibes                 █████████       OOG │
└────────────────────────────────────────────┘
```

Caveman no make brain smaller. Caveman make *mouth* smaller. Shrinks what the agent **says**, not what it knows.

## Install

**One command. Finds every agent on your machine. Installs for each.**

```bash
# macOS · Linux · WSL · Git Bash
curl -fsSL https://raw.githubusercontent.com/JuliusBrussee/caveman/main/install.sh | bash
```

```powershell
# Windows · PowerShell 5.1+
irm https://raw.githubusercontent.com/JuliusBrussee/caveman/main/install.ps1 | iex
```

~30 seconds. Needs Node ≥18. Skips agents you no have. Safe to re-run.

> [!TIP]
> **Turn it on:** type `/caveman` or say *"talk like caveman"*. **Turn it off:** say *"normal mode"*. On Claude Code, Codex, and Gemini it's already on from message one. No command needed.

<details>
<summary><strong>Install for one agent, or any of 30+ others</strong></summary>

<br>

Every agent has its own path (plugin, extension, rule file, or `npx skills add`). The full per-agent matrix, all flags, dry-run, and uninstall live in **[INSTALL.md](./INSTALL.md)**. A few common ones:

```bash
# Claude Code plugin
claude plugin marketplace add JuliusBrussee/caveman && claude plugin install caveman@caveman

# Gemini CLI extension
gemini extensions install https://github.com/JuliusBrussee/caveman

# Cursor / Windsurf / Cline / Codex / 30+ more, via the skills registry
npx skills add JuliusBrussee/caveman -a cursor
```

**Install broke?** Open your agent in this repo and say: *"Read CLAUDE.md and INSTALL.md, install caveman for me."* Agent read repo, agent fix own brain. Snake eat tail.

</details>

## Pick your grunt

Six levels. Switch anytime with `/caveman <level>`. Level sticks until you change it or the session ends.

| Level | Same sentence, shrunk |
|---|---|
| *normal agent* | You should wrap the object in `useMemo`, since a new reference is created on every render. |
| `lite` | Wrap object in `useMemo`. New ref created every render. |
| `full` *(default)* | New ref each render. Wrap object in `useMemo`. |
| `ultra` | New ref/render. `useMemo` it. |
| `wenyan` | New ref every render, so wrap in `useMemo` — rendered in classical Chinese, shorter still. |

> [!NOTE]
> **Speak your tongue.** Caveman keeps your language. Write Portuguese, caveman grunt Portuguese. Spanish, French, same. It compresses the *style*, never translates. `wenyan` mode is the exception on purpose: classical Chinese packs the most meaning per token.

## What you get

| Command | What it does |
|---|---|
| `/caveman [lite\|full\|ultra\|wenyan]` | Compress every reply. Level sticks for the session. |
| `/caveman-commit` | Conventional Commit messages, ≤50-char subject. Why over what. |
| `/caveman-review` | One-line PR comments: `L42: 🔴 bug: user null. Add guard.` |
| `/caveman-stats` | Real session token usage, lifetime savings, USD. Tweetable line with `--share`. |
| `/caveman-compress <file>` | Rewrite a memory file (like `CLAUDE.md`) into caveman-speak. Cuts ~46% input tokens **every session after**. Code, URLs, paths byte-preserved. |
| `caveman-shrink` | MCP middleware. Wraps any MCP server, compresses its tool descriptions. [npm](https://www.npmjs.com/package/caveman-shrink). |
| `cavecrew-*` | Caveman subagents (investigator, builder, reviewer). ~60% fewer tokens than vanilla, so main context lasts longer. |

> [!TIP]
> On Claude Code the statusline shows `[CAVEMAN] ⛏ 12.4k` — that's your lifetime tokens saved, updated on every `/caveman-stats`. Silence it with `CAVEMAN_STATUSLINE_SAVINGS=0`.

## Benchmarks

Real token counts from the Claude API. Average **65% output reduction** across 10 prompts (range 22–87%), measured against default verbose replies. Output tokens only, committed and reproducible in [`benchmarks/`](./benchmarks/) and [`evals/`](./evals/).

<!-- BENCHMARK-TABLE-START -->
| Task | Normal | Caveman | Saved |
|------|-------:|--------:|------:|
| Explain React re-render bug | 1180 | 159 | 87% |
| Fix auth middleware token expiry | 704 | 121 | 83% |
| Set up PostgreSQL connection pool | 2347 | 380 | 84% |
| Explain git rebase vs merge | 702 | 292 | 58% |
| Refactor callback to async/await | 387 | 301 | 22% |
| Architecture: microservices vs monolith | 446 | 310 | 30% |
| Review PR for security issues | 678 | 398 | 41% |
| Docker multi-stage build | 1042 | 290 | 72% |
| Debug PostgreSQL race condition | 1200 | 232 | 81% |
| Implement React error boundary | 3454 | 456 | 87% |
| **Average** | **1214** | **294** | **65%** |
<!-- BENCHMARK-TABLE-END -->

> [!IMPORTANT]
> **Honest number warning.** Caveman only shrinks **output** tokens. Input and reasoning tokens are untouched, and the skill itself adds ~1–1.5k input tokens per turn. So whole-session savings run smaller than the output number, and on already-terse workloads they can go net-negative. The real win is **readability and speed**. Cost savings are the bonus. When caveman wins, when it loses, and how to measure it yourself: **[docs/HONEST-NUMBERS.md](./docs/HONEST-NUMBERS.md)**.

Turns out short isn't just cheaper. A March 2026 paper, [*Brevity Constraints Reverse Performance Hierarchies in Language Models*](https://arxiv.org/abs/2604.00025), tested 31 models and found that constraining large models to brief answers **improved accuracy by ~26 points** on some benchmarks. Sometimes less word = more correct.

<details>
<summary><strong>caveman-compress receipts</strong> — real memory files, cutting input tokens forever</summary>

<br>

| File | Original | Compressed | Saved |
|---|---:|---:|---:|
| `claude-md-preferences.md` | 706 | 285 | **59.6%** |
| `project-notes.md` | 1145 | 535 | **53.3%** |
| `claude-md-project.md` | 1122 | 636 | **43.3%** |
| `todo-list.md` | 627 | 388 | **38.1%** |
| `mixed-with-code.md` | 888 | 560 | **36.9%** |
| **Average** | **898** | **481** | **46%** |

Every session after, that file loads ~46% smaller. Input tokens saved forever, not just one reply.

</details>

## The whole cave

<table>
<tr><td>

### <img src="docs/assets/dancing-rock.svg" width="20" height="20" alt=""> Want the whole agent, not just its mouth? → caveman-code

This skill shrinks what an agent **says**. **[caveman-code](https://github.com/JuliusBrussee/caveman-code)** shrinks **everything** — a full terminal coding agent, caveman top to bottom. **~2× fewer tokens than Codex** on identical tasks. 20+ providers, plan mode, autopilot goal loop, MIT.

```bash
npm install -g @juliusbrussee/caveman-code
```

[**▶ Try caveman-code →**](https://github.com/JuliusBrussee/caveman-code)

</td></tr>
</table>

Five tools, one idea: **agent do more with less.**

| Repo | What it shrinks |
|------|------|
| [**caveman**](https://github.com/JuliusBrussee/caveman) *(you here)* | What the agent **says** |
| [**caveman-code**](https://github.com/JuliusBrussee/caveman-code) | The **whole agent**, end to end |
| [**cavemem**](https://github.com/JuliusBrussee/cavemem) | What the agent **remembers**, across sessions |
| [**cavekit**](https://github.com/JuliusBrussee/cavekit) | The **build loop** — spec-driven, no guessing |
| [**cavegemma**](https://github.com/JuliusBrussee/finetune-caveman) | The compression **baked into weights** (Gemma fine-tune) |

<details>
<summary><strong>Also: five sibling skills, one install</strong></summary>

<br>

[**JuliusBrussee/skills**](https://github.com/JuliusBrussee/skills) — works in Claude Code, Cursor, Gemini, Cline, Copilot, 40+ agents:

| Skill | What |
|------|------|
| [**caveman**](https://github.com/JuliusBrussee/skills/tree/main/skills/caveman) | This one. Speak less, say more. |
| [**grill-me**](https://github.com/JuliusBrussee/skills/tree/main/skills/grill-me) | Agent grills your plan *before* you build the wrong thing. |
| [**interface-kit**](https://github.com/JuliusBrussee/skills/tree/main/skills/interface-kit) | Build UI that looks good, loads fast, works for everyone. |
| [**junior-to-senior**](https://github.com/JuliusBrussee/skills/tree/main/skills/junior-to-senior) | Adversarial review pass. Junior output in, senior output out. |
| [**loop-factory**](https://github.com/JuliusBrussee/skills/tree/main/skills/loop-factory) | Spec-driven task loop — inbox → active → archive. |

```bash
npx skills@latest add JuliusBrussee/skills
```

</details>

<details>
<summary><strong>🦞 Teach the lobster brevity — OpenClaw integration</strong></summary>

<br>

[**OpenClaw**](https://openclaw.ai) is a self-host gateway: one box, many agents inside, wired to Slack / Discord / iMessage / Telegram. Lobster strong. Lobster smart. Lobster also talk a lot.

Same installer, scoped to one agent:

```bash
curl -fsSL https://raw.githubusercontent.com/JuliusBrussee/caveman/main/install.sh | bash -s -- --only openclaw
```

Two things happen, no more: a caveman skill lands in the workspace, and a tiny marker-fenced block is appended to `SOUL.md` (OpenClaw injects it every turn, so the lobster is terse from message one — no `/caveman` per session). Custom path? `OPENCLAW_WORKSPACE=/your/path`. Uninstall with the same line plus `--uninstall`; your other workspace content stays untouched. Lobster claw still sharp. Lobster mouth now small.

</details>

## Caveman 2

**Caveman make token small. Caveman 2 make it _provable_.**

Today's savings numbers (including `/caveman-stats`) are local estimates. Caveman 2 measures and verifies them across a whole team — real receipts, real dashboard, real proof the tokens went down. Building it now.

[**Join the waitlist → caveman.so**](https://caveman.so)

## How it works

1. Install drops a skill file into your agent.
2. Skill tells agent: drop filler, keep substance, use fragments — but never touch code, commands, or errors.
3. On Claude Code, a hook writes a tiny flag file each session, so the agent talks caveman from message one without `/caveman`.
4. `/caveman-stats` reads your session log, counts tokens saved, writes the number to your statusline.
5. `/caveman-compress` rewrites memory files (like `CLAUDE.md`) so every future session starts with a smaller context. Save tokens forever, not just once.

Hook architecture, file ownership, and CI sync are documented for maintainers in [CLAUDE.md](./CLAUDE.md).

## Privacy

Caveman no phone home. No telemetry, no analytics, no accounts, no backend. After install, zero network calls — the skill is a prompt, the hooks are local scripts, and `/caveman-stats` reads a log already on your disk. Install-time fetches (GitHub plus your agents' own registries) are spelled out in [SECURITY.md](./SECURITY.md#privacy--telemetry).

## Sponsors

Caveman free forever. Sponsors keep the rock sharp.

<p align="center">
  <a href="https://www.atlascloud.ai">
    <picture>
      <source media="(prefers-color-scheme: dark)" srcset="docs/assets/atlas-cloud-dark.svg">
      <img src="docs/assets/atlas-cloud.svg" alt="Atlas Cloud" height="32">
    </picture>
  </a>
</p>

<p align="center">
  <a href="https://www.atlascloud.ai"><strong>Atlas Cloud</strong></a> — full-modal AI inference platform, one API.
</p>

<p align="center">
  <a href="https://github.com/sponsors/JuliusBrussee"><strong>Want your rock here? → Sponsor caveman</strong></a>
</p>

## Star this repo

Caveman save you token, save you money. Star cost zero. Fair trade. ⭐

[![Star History Chart](https://api.star-history.com/svg?repos=JuliusBrussee/caveman&type=Date)](https://star-history.com/#JuliusBrussee/caveman&Date)

---

<sub>
<strong>Docs:</strong>
<a href="./INSTALL.md">Install matrix</a> ·
<a href="./docs/HONEST-NUMBERS.md">Honest numbers</a> ·
<a href="./CONTRIBUTING.md">Contributing</a> ·
<a href="./CLAUDE.md">Maintainer guide</a> ·
<a href="https://github.com/JuliusBrussee/caveman/issues">Issues</a>
<br>
<strong>Also by Julius Brussee:</strong>
<a href="https://github.com/JuliusBrussee/revu-swift">Revu</a> — local-first macOS study app with FSRS spaced repetition (<a href="https://revu.cards">revu.cards</a>)
<br><br>
MIT — free like mass mammoth on open plain.
</sub>
