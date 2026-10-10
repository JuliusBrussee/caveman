<div align="center">

<img src="docs/assets/caveman-logo-banner.png" alt="Caveman" width="720">

# why many token when few do trick

**Caveman make your AI agent say less and read less. Code stay exact. Brain still big.**

<a href="https://github.com/JuliusBrussee/caveman/stargazers"><img src="https://img.shields.io/github/stars/JuliusBrussee/caveman?style=flat-square&color=F0A63C&label=stars" alt="GitHub stars"></a>
<a href="https://www.npmjs.com/package/@caveman-ai/cli"><img src="https://img.shields.io/npm/dm/@caveman-ai/cli?style=flat-square&color=F0A63C&label=cli%20downloads" alt="npm downloads"></a>
<a href="./INSTALL.md"><img src="https://img.shields.io/badge/works_with-30%2B_agents-orange?style=flat-square" alt="30+ agents"></a>
<a href="#license"><img src="https://img.shields.io/badge/license-Apache--2.0-green?style=flat-square" alt="License"></a>

<table>
<tr>
<td align="center" width="33%"><h3>33.2% fewer</h3>input tokens through <a href="#the-proxy-332-fewer-input-tokens">the proxy</a><br><sub>54 Claude Code runs, 18 of 18 answers right</sub></td>
<td align="center" width="33%"><h3>129.8× smaller</h3>web pages for the agent<br><sub><a href="./browse/BENCHMARK.md"><code>caveman browse</code></a> vs a Playwright snapshot</sub></td>
<td align="center" width="33%"><h3>1.4 to 2.4× cheaper</h3>with caveman-style output<br><sub><a href="https://arxiv.org/abs/2606.24083">Adobe Research</a>, eight models</sub></td>
</tr>
</table>

Cited by **[Adobe Research](https://arxiv.org/abs/2606.24083)** · A/B tested by **[JetBrains](https://blog.jetbrains.com/ai/2026/07/speak-to-ai-agents-like-cavemen-tosave-tokens/)** · Remade for Elasticsearch on **[Elasticsearch Labs](https://www.elastic.co/search-labs/blog/elastic-caveman-ai-token-reduction)**<br>
**#1** on [Hacker News](https://news.ycombinator.com/item?id=47647455) · **#1** on GitHub Trending · *"No way this actually works."* [ThePrimeagen](https://www.youtube.com/watch?v=L29q2LRiMRc)

**[How it talks](#how-caveman-talks) · [Install](#install) · [The numbers](#the-numbers) · [The proxy](#big-rock-the-proxy) · [The skill](#small-rock-the-skill) · [What you get](#what-you-get) · [In the wild](#in-the-wild)**

</div>

---

<table>
<tr>
<th width="50%">Normal agent · 63 tokens</th>
<th width="50%"><img src="docs/assets/dancing-rock.svg" width="18" height="18" alt=""> Caveman agent · 20 tokens</th>
</tr>
<tr>
<td valign="top">

> The reason your React component is re-rendering is likely because you're creating a new object reference on each render cycle. When you pass an inline object as a prop, React's shallow comparison sees it as a different object every time, which triggers a re-render. I'd recommend using useMemo to memoize the object.

</td>
<td valign="top">

> New object ref each render, so React re-renders. Wrap the prop in `useMemo`.

</td>
</tr>
</table>

**Same fix. 63 token become 20. Brain still big.**

Pick your club:

| Skill | Same answer | Tokens |
|---|---|---:|
| `/caveman` | New object ref each render, so React re-renders. Wrap the prop in `useMemo`. | 20 |
| `/ultracave` | Inline object prop, new ref, re-render. `useMemo`. | 14 |
| `/megacave` | 新參照致重繪。`useMemo`。 | **13** |

<sub>Token counts: tiktoken o200k.</sub>

## How caveman talks

Caveman is a voice, not broken grammar. Every reply follows the same structure:

| Rule | What it means |
|---|---|
| **Answer first** | `[thing] [action] [reason]. [next step].` No greeting, no "let me", no recap, no "hope this helps" |
| **One idea per sentence** | Built on [ASD-STE100](https://www.asd-ste100.org/), the controlled English written for aircraft maintenance manuals: 20 words max, active voice, one term per thing |
| **Meaning never dropped** | Articles can go. *not*, *never*, *no*, *only* never go. Numbers and units stay exact |
| **Payload verbatim** | Code, commands, paths, error messages, and your existing code comments untouched, character for character. Small fix shows the changed lines, not the whole file again |
| **Quiet tool runs** | No chatter between tool calls. One line per phase, one line with the result |
| **Knows when to stop** | Security warnings, irreversible actions, step-by-step orders, questions back to you, and confused users get full sentences. Then grunt resumes |
| **Never performs** | No "me think", no caveman prefix. If caveman phrasing isn't shorter, plain wins |
| **Your prompts stay yours** | Never rewritten. [Research say that backfire](#the-numbers) |

Every reply runs a check before it sends: opener that announces the plan, deleted; closer that recaps, deleted; every negation, path, and number still there.

## Install

```bash
npx @caveman-ai/cli
```

One command, same on Mac, Linux and Windows. Caveman look at your machine, find your agents and your logins, show what it change. Press Enter. It set up, then offer to start Claude Code. Later, `caveman claude` (or codex · gemini · aider · kilo · qwen · opencode · hermes · openclaw · pi).

**This is the proxy, the big rock.** Your agent reads 33.2% fewer input tokens across whole sessions, same answers. It runs on your machine, with your keys and your Claude Pro/Max login. Needs Node.js 22.13+. After the first run, plain `claude` stays caveman'd. One rock. That it.

[What the proxy does](#big-rock-the-proxy) · Only want shorter answers? [Get just the skill](#small-rock-the-skill)

## The numbers

Outside labs first, then ours. Nothing rounded up. Red rows stay red.

| Who | Setup | Result |
|---|---|---|
| **[Adobe Research](https://arxiv.org/abs/2606.24083)**, CAVEWOMAN paper (cites this repo) | Caveman-style output, eight models, five datasets | **Cost cut 1.4 to 2.4× per model, up to 3×** |
| **[Elastic](https://www.elastic.co/search-labs/blog/elastic-caveman-ai-token-reduction)**, Elasticsearch Labs | Caveman mode remade for Elasticsearch, eight live MCP scenarios | **63.6% fewer response tokens.** *"Zero information loss."* |
| **[JetBrains](https://blog.jetbrains.com/ai/2026/07/speak-to-ai-agents-like-cavemen-tosave-tokens/)** | 86 real coding tasks, paired A/B | **No measurable quality loss** (p = 0.82). 8.5% fewer output tokens |

Two findings shaped caveman. Adobe found that cavemanning *your* prompt makes answers longer and worse, so caveman never touches your prompt. JetBrains found that agent sessions are mostly code and tool calls, which the skill leaves alone. So caveman grew a second rock that shrinks what the agent *reads*: [the proxy](#big-rock-the-proxy).

### The proxy: 33.2% fewer input tokens

| File type | The file, through caveman | File saved | Whole Claude Code session, 3 runs | Session saved |
|---|---:|---:|---:|---:|
| CSV | 28,041 → **314** | **98.9%** | 165,823 → 74,484 | **55.1%** |
| Logs | 22,810 → **348** | **98.5%** | 148,807 → 74,068 | **50.2%** |
| YAML | 20,447 → **178** | **99.1%** | 132,124 → 71,027 | **46.2%** |
| Test output | 18,806 → **203** | **98.9%** | 150,377 → 108,514 | **27.8%** |
| JSON | 18,837 → **281** | **98.5%** | 147,975 → 108,939 | **26.4%** |
| HTML | 21,670 → 21,670 | none yet | 140,687 → 154,641 | 9.9% worse |
| **All six** | 130,611 → 22,994 | 82.4% | **885,793 → 591,673** | **33.2%** |

**18 of 18 answers right.** *The file* is each benchmark file run through today's compressor on its own. *The session* is the whole agent run, which also carries the system prompt, tool definitions, conversation, and caveman's own rules, so it moves less than the file. The session run is from August 2026, on an engine that only got the JSON file down to 8,106 tokens. HTML has no compressor yet, so caveman paid its overhead and won nothing back. Each file hides one record in 61 to 72 KB of noise; your files will vary. Headroom on the same suite got 15 of 18 right and used 6.7% fewer tokens on those 15. [Method](./docs/WRAP-BENCHMARK.md#per-file-compression)

### The skill: ten dev questions on claude-opus-5-5

| Instruction | Output tokens |
|---|---:|
| None | 6,983 |
| `Answer concisely.` | 4,334 |
| `/caveman` | 4,119 |
| `/ultracave` | **2,693** |

New models already know "be concise", so that line is the real baseline. On top of it, `/caveman` cuts 3% more at the median and `/ultracave` cuts 35% more. Measured on the 3.1.0 skill text, not yet re-run on this release's. [Harness](./evals/README.md)

<!-- BENCHMARK-TABLE-START -->
<!-- BENCHMARK-TABLE-END -->

### Everything else

| What | Result |
|---|---|
| A web page the agent reads (`caveman browse`, 200-row table) | **121 tokens instead of 15,704** for a Playwright snapshot, 129.8× smaller. Tiny forms lose 2.3×. [Bench](./browse/BENCHMARK.md) |
| Memory files like `CLAUDE.md` (`/caveman-compress`) | **4,138 tokens instead of 6,198** across five fixtures, 22.8% to 49.1% smaller per file, with every heading, code block, and path intact. [Bench](./skills/caveman-compress/README.md#benchmarks) |

The `/caveman` rules are about 1,160 tokens of text (tiktoken count). What that adds to your bill depends on caching; not measured yet. If you pay per request instead of per token (GitHub Copilot premium requests), a shorter answer costs the same, so skip it. Every case where caveman loses: [HONEST-NUMBERS.md](./docs/HONEST-NUMBERS.md).

## Big rock: the proxy

**Logs, CSV, YAML, JSON, and test output come out 98.5% to 99.1% smaller. Whole sessions use 33.2% fewer input tokens, same answers.** ([benchmark](#the-proxy-332-fewer-input-tokens))

The skill shrinks what the agent **says**. The proxy shrinks what it **reads**: logs, test output, JSON, diffs, web pages. It runs on your machine, with your keys and your Claude Pro/Max login. Every original stays on your disk, and the agent can pull it back any time.

Installed it [above](#install)? Then:

```bash
caveman learn                 # rank where your tokens go, from agent history already on disk
caveman learn implement       # apply the fixes one diff at a time, only on your yes
caveman trial -- claude       # A/B a real session on your own work
caveman shrink -- pnpm test   # compress noisy command output
caveman browse <url>          # a compressed web page instead of a 15,000-token dump
caveman convert --dry-run     # pixel mode: skills the model reads as images
caveman stats                 # your token history
```

<p align="center">
  <img src="docs/assets/learn-report.png" alt="Caveman Learn report: a summary and savings cards on the left; the biggest places tokens go, with one fix opened, and a chart of how full sessions get on the right" width="900">
</p>

**Building your own agent?** Same shrinking, as one wrapper around the Vercel AI SDK, LangChain, OpenAI, or Anthropic call you already make:

```bash
npm install @caveman-ai/middleware @caveman-ai/sdk        # TypeScript
pip install 'caveman-middleware[langchain]' caveman-sdk   # Python 3.11+
```

[TypeScript guide](./packages/middleware/typescript/README.md) · [Python guide](./packages/middleware/python/README.md) · [Every framework](https://docs.caveman.so/docs/sdk/middleware/frameworks) · [One container for the whole team](docs/technical/deploy.md)

## Small rock: the skill

**Only want shorter answers? This is output compression alone. No proxy.**

```bash
npx skills add JuliusBrussee/caveman -g
```

<a href="https://skills.sh/JuliusBrussee/caveman"><img src="https://skills.sh/b/JuliusBrussee/caveman" alt="skills.sh"></a>

Works in Claude Code, Codex, Gemini CLI, Cursor, Windsurf, Cline, Copilot, and [30+ more](./INSTALL.md). Type `/caveman` (`$caveman` in Codex) if it doesn't start on its own. Say `stop caveman` to go back.

<details>
<summary><strong>Other ways in</strong>: Claude app and phone, Claude Code plugin, Gemini, every agent at once, Windows, uninstall</summary>

<br>

Claude on the web, the phone app, or Cowork? No terminal. Add caveman to your Claude account once, in Customize > Plugins: [steps](./INSTALL.md#claude-apps-claudeai-desktop-mobile-cowork). Not tested end to end yet.

```bash
# Claude Code plugin, auto-starts every interactive session, subagents too
claude plugin marketplace add JuliusBrussee/caveman && claude plugin install caveman@caveman

# Gemini CLI
gemini extensions install https://github.com/JuliusBrussee/caveman

# Every agent on your machine at once, plus the Claude Code statusline badge (Node.js 22.13+)
curl -fsSL https://raw.githubusercontent.com/JuliusBrussee/caveman/v3.2.0/install.sh | bash
```

Already inside Claude Code? Same rock, slash form. Type these where you talk to Claude:

```text
/plugin marketplace add JuliusBrussee/caveman
/plugin install caveman@caveman
```

Windows, PowerShell 5.1+:

```powershell
irm https://raw.githubusercontent.com/JuliusBrussee/caveman/v3.2.0/install.ps1 | iex
```

On npm 12 or newer, new npm block git install. One-liners above handle it themselves from v3.2.0 on. Old one-liner pinned to v3.1.0 or older still fail there: use one above, or this:
`npx --allow-git=root -y github:JuliusBrussee/caveman#v3.2.0`

Changed your mind: `npx -y github:JuliusBrussee/caveman -- --uninstall` (on npm 12+, add `--allow-git=root` too)

Only grabbed the skill? `npx skills remove JuliusBrussee/caveman -g` take it back out.

Install broke? Open your agent in this repo and say *"Read CLAUDE.md and INSTALL.md, install caveman for me."* Agent fix own brain.

</details>

## What you get

| Command | What it does |
|---|---|
| `/caveman` · `/ultracave` · `/megacave` | The voice, the grunt, the 文言文. `/caveman status` shows the mode, `/caveman off` stops it |
| `/caveman-commit` | One-line Conventional Commit |
| `/caveman-review` | One finding per line: `L42: 🔴 null deref. Guard it.` |
| `/caveman-compress <file>` | Shrinks memory files and backs up the original |
| `/caveman-stats` | Real token usage for this Claude Code session |
| `/caveman-help` | Every mode and command on one screen |
| `cavecrew` | Subagents that find, edit, and review code, then report back in caveman |
| `investigate-first` · `lean-build` · `surgical-patch` · `safe-refactor` · `migration` · `verify-and-stop` | Work patterns that write less code. Your agent picks them up when a task fits |
| `caveman-setup` · `caveman-discover` · `caveman-learn` · `caveman-manage` · `caveman-optimize` · `caveman-explore` · `caveman-evidence-review` | Drive the proxy from inside your agent |

## In the wild

<a href="https://www.youtube.com/watch?v=L29q2LRiMRc"><img src="https://img.youtube.com/vi/L29q2LRiMRc/hqdefault.jpg" alt="ThePrimeagen reacts to Caveman: No way this actually works" width="340" align="right"></a>

**ThePrimeagen**: *"No way this actually works."* [Watch](https://www.youtube.com/watch?v=L29q2LRiMRc)

**Adobe Research**: [CAVEWOMAN](https://arxiv.org/abs/2606.24083), arXiv, June 2026

**JetBrains**: *"It is fun, and it costs you nothing measurable in quality."* [Read](https://blog.jetbrains.com/ai/2026/07/speak-to-ai-agents-like-cavemen-tosave-tokens/)

**Elastic**: [elastic-caveman](https://www.elastic.co/search-labs/blog/elastic-caveman-ai-token-reduction) on Elasticsearch Labs, April 2026

**Hacker News**: [#1, 904 points](https://news.ycombinator.com/item?id=47647455)

**GitHub Trending** #1 overall, July 2026 · **[Trendshift](https://trendshift.io/repositories/25391)** #1 repo of the day, April 2026 · **[Product Hunt](https://www.producthunt.com/products/caveman)** #8 of the day

Started as a joke in April 2026. Now past 100,000 stars. Joke got serious. Voice did not.

<br clear="right">

[![Star History Chart](./docs/assets/star-history.png)](https://star-history.com/#JuliusBrussee/caveman&Date)

## Privacy

The skill runs on your machine and sends nothing. The `caveman` CLI sends usage stats by default: commands run, token counts, a random install ID, OS, and IP. Never your prompts, code, or file paths. One person maintains this for free, and those stats show what to build next. Turn it off for good with `caveman telemetry off` or `DO_NOT_TRACK=1`. Routing, once you sign in, adds Auto to your agent's model picker; pick Auto and each ask sends your latest ask (with whatever your agent attaches to it, such as CLAUDE.md, memory or @-mentioned files), the one before it and the end of the agent's last reply to Caveman Cloud to pick the model and effort, with each request's tool names, effort, a few agent headers and the previous request's token counts; on the Free plan Caveman may keep it to improve routing. Requests go from your machine to your provider, or to another provider you added with `caveman providers add`, on your own key; only a request routed to a Caveman Cloud model passes through Caveman Cloud, whole (system prompt, tool results, code), running on your Caveman project's stored key and billed to that project; `caveman providers cloud off` stops that. `caveman off routing` stops it. The full list, and how to delete sent telemetry: [SECURITY.md](./SECURITY.md).

## License

[Apache-2.0](./LICENSE), whole repo. Read it, fork it, ship it, host it. Free like mammoth on open plain. Older releases and third-party notices: [LICENSING.md](./LICENSING.md). "Caveman" and the rock logo are trademarks of Julius Brussee.

## Cite

```bibtex
@software{brussee2026caveman,
  author = {Brussee, Julius},
  title  = {Caveman: why many token when few do trick},
  year   = {2026},
  url    = {https://github.com/JuliusBrussee/caveman}
}
```

---

<div align="center">

🪨 **Caveman save you token. Star cost zero. Fair trade.**

<sub>
<a href="./docs/README.md">Docs</a> ·
<a href="./INSTALL.md">Install matrix</a> ·
<a href="./docs/HONEST-NUMBERS.md">Honest numbers</a> ·
<a href="./CONTRIBUTING.md">Contributing</a> ·
<a href="https://caveman.so">Caveman Cloud</a> ·
<a href="https://github.com/JuliusBrussee/caveman/issues">Issues</a>
</sub>

</div>
