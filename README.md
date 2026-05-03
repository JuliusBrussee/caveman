<p align="center">
  <img src="https://em-content.zobj.net/source/apple/391/rock_1faa8.png" width="120" />
</p>

<h1 align="center">caveman</h1>

<p align="center">
  <strong>why use many token when few do trick</strong>
</p>

<p align="center">
  <a href="https://github.com/JuliusBrussee/caveman/stargazers"><img src="https://img.shields.io/github/stars/JuliusBrussee/caveman?style=flat&color=yellow" alt="Stars"></a>
  <a href="https://github.com/JuliusBrussee/caveman/commits/main"><img src="https://img.shields.io/github/last-commit/JuliusBrussee/caveman?style=flat" alt="Last Commit"></a>
  <a href="LICENSE"><img src="https://img.shields.io/github/license/JuliusBrussee/caveman?style=flat" alt="License"></a>
</p>

<p align="center">
  <a href="#before--after">Before/After</a> •
  <a href="#install">Install</a> •
  <a href="#kiro">Kiro</a> •
  <a href="#intensity-levels">Levels</a> •
  <a href="#caveman-skills">Skills</a> •
  <a href="#benchmarks">Benchmarks</a> •
  <a href="#evals">Evals</a>
</p>

<p align="center">
  <strong>🪨 Caveman Ecosystem</strong> &nbsp;·&nbsp;
  <strong>caveman</strong> <em>talk less</em> <sub>(you are here)</sub> &nbsp;·&nbsp;
  <a href="https://github.com/JuliusBrussee/cavemem">cavemem</a> <em>remember more</em> &nbsp;·&nbsp;
  <a href="https://github.com/JuliusBrussee/cavekit">cavekit</a> <em>build better</em>
</p>

---

A [Claude Code](https://docs.anthropic.com/en/docs/claude-code) skill/plugin and Codex plugin that makes agent talk like caveman — cutting **~75% of output tokens** while keeping full technical accuracy. Now with [文言文 mode](#文言文-wenyan-mode), [terse commits / one-line reviews / lifetime stats](#caveman-skills), and a [compression tool](#caveman-compress-receipts) that cuts **~46% of input tokens** every session.

Based on the viral observation that caveman-speak dramatically reduces LLM token usage without losing technical substance. So we made it a one-line install.

## Before / After

<table>
<tr>
<td width="50%">

### 🗣️ Normal Claude (69 tokens)

> "The reason your React component is re-rendering is likely because you're creating a new object reference on each render cycle. When you pass an inline object as a prop, React's shallow comparison sees it as a different object every time, which triggers a re-render. I'd recommend using useMemo to memoize the object."

</td>
<td width="50%">

### 🪨 Caveman Claude (19 tokens)

> "New object ref each render. Inline object prop = new ref = re-render. Wrap in `useMemo`."

</td>
</tr>
<tr>
<td>

### 🗣️ Normal Claude

> "Sure! I'd be happy to help you with that. The issue you're experiencing is most likely caused by your authentication middleware not properly validating the token expiry. Let me take a look and suggest a fix."

</td>
<td>

### 🪨 Caveman Claude

> "Bug in auth middleware. Token expiry check use `<` not `<=`. Fix:"

</td>
</tr>
</table>

**Same fix. 75% less word. Brain still big.**

**Pick your level of grunt:**

<table>
<tr>
<td width="25%">

#### 🪶 Lite

> "Your component re-renders because you create a new object reference each render. Inline object props fail shallow comparison every time. Wrap it in `useMemo`."

</td>
<td width="25%">

#### 🪨 Full

> "New object ref each render. Inline object prop = new ref = re-render. Wrap in `useMemo`."

</td>
<td width="25%">

#### 🔥 Ultra

> "Inline obj prop → new ref → re-render. `useMemo`."

</td>
<td width="25%">

#### 📜 文言文

> "物出新參照，致重繪。useMemo Wrap之。"

</td>
</tr>
</table>

**Same answer. You pick how many word.**

```
┌─────────────────────────────────────┐
│  TOKENS SAVED          ████████ 75% │
│  TECHNICAL ACCURACY    ████████ 100%│
│  SPEED INCREASE        ████████ ~3x │
│  VIBES                 ████████ OOG │
└─────────────────────────────────────┘
```

- **Faster response** — less token to generate = speed go brrr
- **Easier to read** — no wall of text, just answer
- **Same accuracy** — all technical info kept, only fluff dropped ([science say so](https://arxiv.org/abs/2604.00025))
- **Save money** — 65% mean output reduction across [our benchmarks](#benchmarks) (range 22-87%)
- **Fun** — every code review become comedy

## Install

**One line. Detect every agent. Install for each.**

```bash
# macOS / Linux / WSL / Git Bash
curl -fsSL https://raw.githubusercontent.com/JuliusBrussee/caveman/main/install.sh | bash

# Windows (PowerShell)
irm https://raw.githubusercontent.com/JuliusBrussee/caveman/main/install.ps1 | iex
```

Detects 30+ agents (Claude Code, Gemini CLI, Codex, Cursor, Windsurf, Cline, Copilot, Continue, Kilo, Roo, Augment, Aider Desk, Amp, Bob, Crush, Devin, Droid, ForgeCode, Goose, iFlow, JetBrains Junie, Kiro CLI, Mistral Vibe, OpenHands, opencode, Qwen Code, Qoder, Rovo Dev, Tabnine, Trae, Warp, Replit Agent, Antigravity, …). Runs each one's native install. Skips what you not have. Safe to re-run.

By default the installer wires Claude Code's hooks + statusline + stats badge and registers the [`caveman-shrink`](#caveman-shrink-mcp-middleware) MCP proxy on top of the plugin install. Pass `--minimal` to skip the extras and just install the plugin/extension. Pass `--all` to also drop per-repo rule files into the current directory.

| Flag | What |
|---|---|
| `--all` | Plugin + hooks + statusline + MCP shrink + per-repo rule files in `$PWD`. The full ride. |
| `--minimal` | Plugin/extension only. No hooks, no MCP shrink, no per-repo rules. |
| `--dry-run` | Preview, write nothing |
| `--only <agent>` | One target only (repeatable) |
| `--with-hooks` | Claude Code: also wire standalone hooks + statusline + stats badge. **On by default.** |
| `--with-mcp-shrink` | Claude Code: register the [caveman-shrink](#caveman-shrink-mcp-middleware) MCP proxy via `npx caveman-shrink`. **On by default.** |
| `--with-init` | Drop always-on rule files into the current repo (Cursor / Windsurf / Cline / Copilot / AGENTS.md). Off by default; turned on by `--all`. |
| `--list` | Print full agent matrix and exit |
| `--force` | Re-run even if already installed |

`install.sh --help` for full reference.

**Manual install per agent:**

</details>

<details>
<summary><strong>Any other agent (opencode, Roo, Amp, Goose, Kiro, and 40+ more)</strong></summary>

[npx skills](https://github.com/vercel-labs/skills) supports 40+ agents:

```bash
npx skills add JuliusBrussee/caveman           # auto-detect agent
npx skills add JuliusBrussee/caveman -a amp
npx skills add JuliusBrussee/caveman -a augment
npx skills add JuliusBrussee/caveman -a goose
npx skills add JuliusBrussee/caveman -a kiro-cli
npx skills add JuliusBrussee/caveman -a roo
# ... and many more
```

### Kiro

Install caveman as a power from GitHub:

1. Open the Powers panel → **Add Power**
2. Select **From GitHub repo**
3. Enter `JuliusBrussee/caveman`
4. Done. Trigger with `/caveman`, "caveman mode", or "less tokens please"

> [!NOTE]
> Kiro reads the `POWER.md` at the repo root. No clone, no build step. Add from GitHub and go.

### Optional: Statusline Badge
Uninstall: `npx skills remove caveman`
| Agent | Command |
|---|---|
| **Claude Code** | `claude plugin marketplace add JuliusBrussee/caveman && claude plugin install caveman@caveman` |
| **Gemini CLI** | `gemini extensions install https://github.com/JuliusBrussee/caveman` |
| **Cursor / Windsurf / Cline / Copilot** | `npx skills add JuliusBrussee/caveman -a <cursor\|windsurf\|cline\|github-copilot>` |
| **Codex / opencode / Roo / Amp / Goose / Kiro / Augment / Aider Desk / Continue / Kilo / Junie / Trae / Warp / Tabnine / Mistral / Qwen / Devin / Droid / ForgeCode / Bob / Crush / iFlow / OpenHands / Qoder / Rovo Dev / Replit / Antigravity** | `npx skills add JuliusBrussee/caveman -a <profile>` (see `install.sh --list` for the full slug list) |
| **Anything else (40+ agents)** | `npx skills add JuliusBrussee/caveman` (auto-detect) |

Standalone Claude Code hooks (without plugin): `bash <(curl -s https://raw.githubusercontent.com/JuliusBrussee/caveman/main/hooks/install.sh)`. Windows: `irm https://raw.githubusercontent.com/JuliusBrussee/caveman/main/hooks/install.ps1 | iex`. Manual fallback for stubborn Windows envs lives in [`docs/install-windows.md`](docs/install-windows.md).

Uninstall: disable the Claude plugin, `gemini extensions uninstall caveman`, or `npx skills remove caveman`.

### What You Get

| Feature | Claude Code | Codex | Gemini CLI | Cursor / Windsurf | Cline / Copilot | Others* |
|---|:-:|:-:|:-:|:-:|:-:|:-:|
| Caveman mode | Y | Y | Y | Y | Y | Y |
| Auto-activate every session | Y | Y¹ | Y | with `--with-init` | with `--with-init` | with `--with-init` |
| `/caveman` command | Y | Y¹ | Y | — | — | — |
| Mode switching (lite/full/ultra) | Y | Y¹ | Y | Y² | — | — |
| Statusline badge | Y | — | — | — | — | — |
| caveman-commit / caveman-review | Y | — | Y | Y | Y | Y |
| caveman-compress / caveman-help | Y | Y³ | Y | Y | Y | Y |
| caveman-stats | Y | — | — | — | — | — |
| cavecrew (subagents) | Y | — | — | — | — | — |

\* opencode, Roo, Amp, Goose, Kiro CLI, Augment, Aider Desk, Continue, Kilo, Junie (JetBrains), Trae, Warp, Tabnine, Mistral, Qwen, Devin, Droid, ForgeCode, Bob, Crush, iFlow, OpenHands, Qoder, Rovo Dev, Replit, Antigravity, and more via `npx skills`. AGENTS.md / IDE rule files reach Zed, generic agents, etc. via `--with-init`.
¹ Codex uses `$caveman` instead of `/caveman`. Auto-start ships when you run Codex inside this repo (via `.codex/hooks.json`); for other repos, copy the hook or use `$caveman` manually. ² Mode switching is on-demand via the skill, no slash command. ³ Compress only.

`--with-init` writes `.cursor/rules/caveman.mdc`, `.windsurf/rules/caveman.md`, `.clinerules/caveman.md`, `.github/copilot-instructions.md`, and `AGENTS.md` into the current repo so caveman auto-starts there.

## Usage

Trigger with:
- `/caveman` or Codex `$caveman`
- "talk like caveman"
- "caveman mode"
- "less tokens please"

Stop with: "stop caveman" or "normal mode"

### Intensity Levels

| Level | Trigger | What it do |
|-------|---------|------------|
| **Lite** | `/caveman lite` | Drop filler, keep grammar. Professional but no fluff |
| **Full** | `/caveman full` | Default caveman. Drop articles, fragments, full grunt |
| **Ultra** | `/caveman ultra` | Maximum compression. Telegraphic. Abbreviate everything |

### 文言文 (Wenyan) Mode

Classical Chinese literary compression — same technical accuracy, but in the most token-efficient written language humans ever invented.

| Level | Trigger | What it do |
|-------|---------|------------|
| **Wenyan-Lite** | `/caveman wenyan-lite` | Semi-classical. Grammar intact, filler gone |
| **Wenyan-Full** | `/caveman wenyan` | Full 文言文. Maximum classical terseness |
| **Wenyan-Ultra** | `/caveman wenyan-ultra` | Extreme. Ancient scholar on a budget |

Level stick until you change it or session end.

## Caveman Skills

| Skill | What |
|---|---|
| `/caveman-commit` | Terse commit messages. Conventional Commits, ≤50 char subject. Why over what. |
| `/caveman-review` | One-line PR comments: `L42: 🔴 bug: user null. Add guard.` No throat-clearing. |
| `/caveman-help` | Quick-reference card. All modes, skills, commands. |
| `/caveman-stats` | Real session token usage + estimated savings + USD. Lifetime aggregation via `--all`, time window via `--since 7d`, tweetable line via `--share`. Reads the Claude Code session JSONL directly, no model-side guessing. Claude Code only. |
| `/caveman:compress <file>` | Rewrites a memory file (e.g. `CLAUDE.md`) into caveman-speak. Saves backup as `<file>.original.md`. Cuts ~46% of *input* tokens every session start. Code/URLs/paths preserved byte-for-byte. |
| `cavecrew-investigator/builder/reviewer` | Caveman subagents for Claude Code. Subagent tool-output gets injected back into main context — these emit ~60% fewer tokens than vanilla `Explore` / reviewer agents, so main context lasts longer across long sessions. Investigator (read-only locator, haiku), builder (1-2 file surgical edit, refuses 3+), reviewer (one-line findings, haiku). |

**Statusline savings badge** — on by default. After your first `/caveman-stats` run the statusline appends `[CAVEMAN] ⛏ 12.4k` (lifetime tokens saved) and updates every time `/caveman-stats` runs. Don't want it? Set `CAVEMAN_STATUSLINE_SAVINGS=0` to silence.

### caveman-compress receipts

| File | Original | Compressed | Saved |
|---|---:|---:|---:|
| `claude-md-preferences.md` | 706 | 285 | **59.6%** |
| `project-notes.md` | 1145 | 535 | **53.3%** |
| `claude-md-project.md` | 1122 | 636 | **43.3%** |
| `todo-list.md` | 627 | 388 | **38.1%** |
| `mixed-with-code.md` | 888 | 560 | **36.9%** |
| **Average** | **898** | **481** | **46%** |

Full docs: [caveman-compress README](caveman-compress/README.md). [Snyk false-positive note](./caveman-compress/SECURITY.md).

## caveman-shrink (MCP middleware)

Stdio proxy that wraps any MCP server, intercepts `tools/list` / `prompts/list` / `resources/list` responses, and compresses the `description` fields. Code, URLs, paths, identifiers stay byte-for-byte identical.

```jsonc
{
  "mcpServers": {
    "fs-shrunk": {
      "command": "npx",
      "args": ["caveman-shrink", "npx", "@modelcontextprotocol/server-filesystem", "/path/to/dir"]
    }
  }
}
```

Published on npm as [`caveman-shrink`](https://www.npmjs.com/package/caveman-shrink). V1 does not touch tool-call response bodies or request payloads. Auto-registered by `install.sh` (use `--minimal` to skip). Full docs: [`mcp-servers/caveman-shrink/`](mcp-servers/caveman-shrink).

## Benchmarks

Real token counts from the Claude API ([reproduce it yourself](benchmarks/)):

<!-- BENCHMARK-TABLE-START -->
| Task | Normal (tokens) | Caveman (tokens) | Saved |
|------|---------------:|----------------:|------:|
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

*Range: 22%–87% savings across prompts.*
<!-- BENCHMARK-TABLE-END -->

> [!IMPORTANT]
> Caveman only affects output tokens — thinking/reasoning tokens are untouched. Caveman no make brain smaller. Caveman make *mouth* smaller. Biggest win is **readability and speed**, cost savings are a bonus.

A March 2026 paper ["Brevity Constraints Reverse Performance Hierarchies in Language Models"](https://arxiv.org/abs/2604.00025) found that constraining large models to brief responses **improved accuracy by 26 percentage points** on certain benchmarks and completely reversed performance hierarchies. Verbose not always better. Sometimes less word = more correct.

## Evals

Caveman not just claim 75%. Caveman **prove** it.

The `evals/` directory has a three-arm eval harness that measures real token compression against a proper control — not just "verbose vs skill" but "terse vs skill". Because comparing caveman to verbose Claude conflate the skill with generic terseness. That cheating. Caveman not cheat.

```bash
# Run the eval (needs claude CLI)
uv run python evals/llm_run.py

# Read results (no API key, runs offline)
uv run --with tiktoken python evals/measure.py
```

## Star This Repo

If caveman save you mass token, mass money — leave mass star. ⭐

[![Star History Chart](https://api.star-history.com/svg?repos=JuliusBrussee/caveman&type=Date)](https://star-history.com/#JuliusBrussee/caveman&Date)

## 🪨 The Caveman Ecosystem

Three tools. One philosophy: **agent do more with less**.

| Repo | What | One-liner |
|------|------|-----------|
| [**caveman**](https://github.com/JuliusBrussee/caveman) *(you are here)* | Output compression skill | *why use many token when few do trick* — ~75% fewer output tokens across Claude Code, Cursor, Gemini, Codex |
| [**cavemem**](https://github.com/JuliusBrussee/cavemem) | Cross-agent persistent memory | *why agent forget when agent can remember* — compressed SQLite + MCP, local by default |
| [**cavekit**](https://github.com/JuliusBrussee/cavekit) | Spec-driven autonomous build loop | *why agent guess when agent can know* — natural language → kits → parallel build → verified |

They compose: **cavekit** orchestrates the build, **caveman** compresses what the agent *says*, **cavemem** compresses what the agent *remembers*. Install one, some, or all — each stands alone.

## Also by Julius Brussee

- **[Revu](https://github.com/JuliusBrussee/revu-swift)** — local-first macOS study app with FSRS spaced repetition, decks, exams, and study guides. [revu.cards](https://revu.cards)

## License

MIT — free like mass mammoth on open plain.
