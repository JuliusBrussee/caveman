<p align="center">
  <img src="https://em-content.zobj.net/source/apple/391/rock_1faa8.png" width="120" />
</p>

<h1 align="center">caveman</h1>

<p align="center">
  <strong>grug not mass word developer. grug say what need saying.</strong>
</p>

<p align="center">
  <a href="https://github.com/JuliusBrussee/caveman/stargazers"><img src="https://img.shields.io/github/stars/JuliusBrussee/caveman?style=flat&color=yellow" alt="Stars"></a>
  <a href="https://github.com/JuliusBrussee/caveman/commits/main"><img src="https://img.shields.io/github/last-commit/JuliusBrussee/caveman?style=flat" alt="Last Commit"></a>
  <a href="LICENSE"><img src="https://img.shields.io/github/license/JuliusBrussee/caveman?style=flat" alt="License"></a>
</p>

<p align="center">
  <a href="#install">Install</a> •
  <a href="#benchmarks">Benchmarks</a> •
  <a href="#before--after">Before/After</a> •
  <a href="#intensity-levels">Intensity Levels</a> •
  <a href="#why">Why</a>
</p>

---

A [Claude Code](https://docs.anthropic.com/en/docs/claude-code) skill/plugin and Codex plugin that make agent talk like grug — cutting **~75% of tokens** while keeping full technical accuracy.

based on viral observation that grug-speak dramatically reduce LLM token usage without losing technical substance. complexity demon words die, technical substance stay. so grug make it one-line install.

## Before / After

<table>
<tr>
<td width="50%">

### 🗣️ Normal Claude (69 tokens)

> "The reason your React component is re-rendering is likely because you're creating a new object reference on each render cycle. When you pass an inline object as a prop, React's shallow comparison sees it as a different object every time, which triggers a re-render. I'd recommend using useMemo to memoize the object."

</td>
<td width="50%">

### 🪨 Grug Claude (19 tokens)

> "New object ref each render. Inline object prop = new ref = re-render. Wrap in `useMemo`."

</td>
</tr>
<tr>
<td>

### 🗣️ Normal Claude

> "Sure! I'd be happy to help you with that. The issue you're experiencing is most likely caused by your authentication middleware not properly validating the token expiry. Let me take a look and suggest a fix."

</td>
<td>

### 🪨 Grug Claude

> "Bug in auth middleware. Token expiry check use `<` not `<=`. Fix:"

</td>
</tr>
</table>

**Same fix. 75% less word. grug smash complexity demon filler with club.**

**sometimes too much grug. sometimes not enough:**

<table>
<tr>
<td width="33%">

#### 🪶 Lite

> "Your component re-renders because you create a new object reference each render. Inline object props fail shallow comparison every time. Wrap it in `useMemo`."

</td>
<td width="33%">

#### 🪨 Full

> "New object ref each render. Inline object prop = new ref = re-render. Wrap in `useMemo`."

</td>
<td width="33%">

#### 🔥 Ultra

> "Inline obj prop → new ref → re-render. `useMemo`."

</td>
</tr>
</table>

**Same answer. You pick how many grunt.**

## Benchmarks

Real token counts from the Claude API ([reproduce it yourself](benchmarks/)):

<!-- BENCHMARK-TABLE-START -->
| Task | Normal (tokens) | Grug (tokens) | Saved |
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
> grug mode only affect output tokens — thinking/reasoning tokens untouched. grug no make brain smaller. grug make *mouth* smaller. biggest win is **readability and speed**, cost savings are bonus.

### Science back grug up

A March 2026 paper ["Brevity Constraints Reverse Performance Hierarchies in Language Models"](https://arxiv.org/abs/2604.00025) found that constraining large models to brief responses **improved accuracy by 26 percentage points** on certain benchmarks and completely reversed performance hierarchies. verbose not always better. sometimes less word = more correct. grug know this all along.

## Install

```bash
npx skills add JuliusBrussee/caveman
```

Or with Claude Code plugin system:

```bash
claude plugin marketplace add JuliusBrussee/caveman
claude plugin install caveman@caveman
```

Codex:

1. Clone repo
2. Open Codex in repo
3. Run `/plugins`
4. Search `Caveman`
5. Install plugin

Install once. Use in all sessions after that.

One rock. That it.

## Usage

Trigger with:
- `/caveman` or Codex `$caveman`
- "talk like grug"
- "grug mode"
- "caveman mode"
- "less tokens please"

Stop with: "stop grug", "stop caveman", or "normal mode"

### Intensity Levels

sometimes full grug too much. sometimes not enough. now you pick:

| Level | Trigger | What grug do |
|-------|---------|------------|
| **Lite** | `/caveman lite` or `$caveman lite` | grug trim filler, keep grammar. professional for meeting with boss |
| **Full** | `/caveman full` or `$caveman full` | default grug. drop articles, fragments, smash complexity demon with club |
| **Ultra** | `/caveman ultra` or `$caveman ultra` | grug survival mode. abbreviate everything. one grunt when one grunt enough |

Level stick until you change it or session end.

## What Grug Do

| Thing | Grug Do? |
|-------|------------|
| English explanation | 🪨 grug smash complexity demon filler words |
| Code blocks | ✍️ Write normal (grug not touch code with club) |
| Technical terms | 🧠 Keep exact (polymorphism stay polymorphism) |
| Error messages | 📋 Quote exact |
| Git commits & PRs | ✍️ Write normal |
| Articles (a, an, the) | 💀 Gone — complexity demon ceremony words |
| Pleasantries | 💀 "Sure I'd be happy to" is dead. grug not shaman |
| Hedging | 💀 "It might be worth considering" extinct |

## Why

```
┌─────────────────────────────────────┐
│  TOKENS SAVED          ████████ 75% │
│  TECHNICAL ACCURACY    ████████ 100%│
│  SPEED INCREASE        ████████ ~3x │
│  VIBES                 ████████ OOG │
└─────────────────────────────────────┘
```

- **Faster response** — less token to generate = speed go brrr
- **Easier to read** — no wall of text, grug say what need saying
- **Same accuracy** — all technical info kept, only complexity demon fluff removed ([science say so](https://arxiv.org/abs/2604.00025))
- **Save shiney rock** — ~71% less output token = less cost
- **Fun** — every code review become comedy

## How It Work

grug not dumb. grug **efficient**.

Normal LLM waste token on complexity demon ceremony words:
- "I'd be happy to help you with that" (8 wasted tokens)
- "The reason this is happening is because" (7 wasted tokens)
- "I would recommend that you consider" (7 wasted tokens)
- "Sure, let me take a look at that for you" (10 wasted tokens)

grug say what need saying. then stop. is fine.

## Star This Repo

If grug save you mass token, mass shiney rock — leave mass star. ⭐

[![Star History Chart](https://api.star-history.com/svg?repos=JuliusBrussee/caveman&type=Date)](https://star-history.com/#JuliusBrussee/caveman&Date)

## Also by Julius Brussee

- **[Blueprint](https://github.com/JuliusBrussee/blueprint)** — specification-driven development for Claude Code. Natural language → blueprints → parallel builds → working software.
- **[Revu](https://github.com/JuliusBrussee/revu-swift)** — local-first macOS study app with FSRS spaced repetition, decks, exams, and study guides. [revu.cards](https://revu.cards)

## License

MIT — free like mass mammoth on open plain.
