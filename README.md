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
  <a href="#intensity-levels">Levels</a> •
  <a href="#caveman-skills">Skills</a> •
  <a href="#benchmarks">Benchmarks</a> •
  <a href="#evals">Evals</a>
</p>

---

A [Claude Code](https://docs.anthropic.com/en/docs/claude-code) skill/plugin and Codex plugin that makes agent talk like caveman — cutting **~75% of output tokens** while keeping full technical accuracy. Now with [文言文 mode](#文言文-wenyan-mode), [terse commits](#caveman-commit), [one-line code reviews](#caveman-review), and a [compression tool](#caveman-compress) that cuts **~45% of input tokens** every session.

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
- **Easier to read** — no wall of text, just the answer
- **Same accuracy** — all technical info kept, only fluff removed ([science say so](https://arxiv.org/abs/2604.00025))
- **Save money** — ~71% less output token = less cost
- **Fun** — every code review become comedy

## Install

### Claude Code (recommended)

Install as a plugin — includes skills + auto-loading hooks (caveman activates every session, mode badge tracks `/caveman ultra` etc.):

```bash
claude plugin marketplace add JuliusBrussee/caveman
claude plugin install caveman@caveman
```

### Any agent (Claude Code, Cursor, Copilot, Windsurf, Cline, Codex)

```bash
npx skills add JuliusBrussee/caveman
```

For a specific agent: `npx skills add JuliusBrussee/caveman -a cursor`

> [!NOTE]
> `npx skills` installs skills only (no hooks). For Claude Code auto-loading hooks, use the plugin install above or run `bash hooks/install.sh`.

### Codex

1. Clone repo → Open Codex in repo → `/plugins` → Search `Caveman` → Install

> [!NOTE]
> **Windows Codex users:** Clone repo → VS Code → Codex Settings → Plugins → find `Caveman` under local marketplace → Install → Reload Window. Also enable `git config core.symlinks true` before cloning (requires developer mode or admin).

Install once. Use in all sessions after that. One rock. That it.

### Warp (manual)

Warp local agents discover skills from `.agents/skills/` (project) or `~/.agents/skills/` (global).

Project-local install:

```bash
mkdir -p .agents/skills/caveman .agents/skills/caveman-commit .agents/skills/caveman-review .agents/skills/compress
cp skills/caveman/SKILL.md .agents/skills/caveman/SKILL.md
cp skills/caveman-commit/SKILL.md .agents/skills/caveman-commit/SKILL.md
cp skills/caveman-review/SKILL.md .agents/skills/caveman-review/SKILL.md
cp caveman-compress/SKILL.md .agents/skills/compress/SKILL.md
rm -rf .agents/skills/compress/scripts
cp -R caveman-compress/scripts .agents/skills/compress/scripts
```
Global install from a local checkout (first time):

```bash
git clone git@github.com:JuliusBrussee/caveman.git ~/caveman
cd ~/caveman
git pull --rebase origin main
mkdir -p ~/.agents/skills/caveman ~/.agents/skills/caveman-commit ~/.agents/skills/caveman-review ~/.agents/skills/compress
cp skills/caveman/SKILL.md ~/.agents/skills/caveman/SKILL.md
cp skills/caveman-commit/SKILL.md ~/.agents/skills/caveman-commit/SKILL.md
cp skills/caveman-review/SKILL.md ~/.agents/skills/caveman-review/SKILL.md
cp caveman-compress/SKILL.md ~/.agents/skills/compress/SKILL.md
rm -rf ~/.agents/skills/compress/scripts
cp -R caveman-compress/scripts ~/.agents/skills/compress/scripts
```

Update global install after upstream changes:

```bash
cd ~/caveman
git pull --rebase origin main
mkdir -p ~/.agents/skills/caveman ~/.agents/skills/caveman-commit ~/.agents/skills/caveman-review ~/.agents/skills/compress
cp skills/caveman/SKILL.md ~/.agents/skills/caveman/SKILL.md
cp skills/caveman-commit/SKILL.md ~/.agents/skills/caveman-commit/SKILL.md
cp skills/caveman-review/SKILL.md ~/.agents/skills/caveman-review/SKILL.md
cp caveman-compress/SKILL.md ~/.agents/skills/compress/SKILL.md
rm -rf ~/.agents/skills/compress/scripts
cp -R caveman-compress/scripts ~/.agents/skills/compress/scripts
```

Use `/caveman`, `/caveman-commit`, `/caveman-review`, or natural-language prompts like "caveman mode".

### Optional: Statusline Badge

Add a `[CAVEMAN:ULTRA]` badge to your statusline showing which mode is active. See [`hooks/README.md`](hooks/README.md) for the snippet.

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

| Skill | What it do | Trigger |
|-------|-----------|---------|
| **caveman-commit** | Terse commit messages. Conventional Commits. ≤50 char subject. Why over what. | `/caveman-commit` |
| **caveman-review** | One-line PR comments: `L42: 🔴 bug: user null. Add guard.` No throat-clearing. | `/caveman-review` |

### caveman-compress

Caveman make Claude *speak* with fewer tokens. **Compress** make Claude *read* fewer tokens.

Your `CLAUDE.md` loads on **every session start**. Caveman Compress rewrites memory files into caveman-speak so Claude reads less — without you losing the human-readable original.

```
/caveman:compress CLAUDE.md
```

```
CLAUDE.md          ← compressed (Claude reads this every session — fewer tokens)
CLAUDE.original.md ← human-readable backup (you read and edit this)
```

| File | Original | Compressed | Saved |
|------|----------:|----------:|------:|
| `claude-md-preferences.md` | 706 | 285 | **59.6%** |
| `project-notes.md` | 1145 | 535 | **53.3%** |
| `claude-md-project.md` | 1122 | 687 | **38.8%** |
| `todo-list.md` | 627 | 388 | **38.1%** |
| `mixed-with-code.md` | 888 | 574 | **35.4%** |
| **Average** | **898** | **494** | **45%** |

Code blocks, URLs, file paths, commands, headings, dates, version numbers — anything technical passes through untouched. Only prose gets compressed. See the full [caveman-compress README](caveman-compress/README.md) for details. [Security note](./caveman-compress/SECURITY.md): Snyk flags this as High Risk due to subprocess/file patterns — it's a false positive.

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

Snapshots committed to git. CI runs free. Every number change reviewable as diff. Add a skill, add a prompt — harness pick it up automatically.

## Star This Repo

If caveman save you mass token, mass money — leave mass star. ⭐

[![Star History Chart](https://api.star-history.com/svg?repos=JuliusBrussee/caveman&type=Date)](https://star-history.com/#JuliusBrussee/caveman&Date)

## Also by Julius Brussee

- **[Cavekit](https://github.com/JuliusBrussee/cavekit)** — specification-driven development for Claude Code. Caveman language → specs → parallel builds → working software.
- **[Revu](https://github.com/JuliusBrussee/revu-swift)** — local-first macOS study app with FSRS spaced repetition, decks, exams, and study guides. [revu.cards](https://revu.cards)

## License

MIT — free like mass mammoth on open plain.
