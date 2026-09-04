<p align="center">
  <img src="docs/assets/caveman-logo-banner.png" alt="Caveman" width="720">
</p>

<p align="center">
  <strong>why use many token when few do trick</strong>
</p>

<p align="center">
  Your AI coding agent bills by the word and writes like it knows that.<br>
  Caveman make it stop. Brain still big. Mouth small. Bill small.
</p>

<p align="center">
  <a href="https://www.producthunt.com/products/caveman?embed=true&amp;utm_source=badge-featured&amp;utm_medium=badge&amp;utm_campaign=badge-caveman-2" target="_blank" rel="noopener noreferrer"><img src="https://api.producthunt.com/widgets/embed-image/v1/featured.svg?post_id=1220849&amp;theme=light&amp;t=1786634691828" alt="Caveman - why use many token when few do trick | Product Hunt" width="250" height="54"/></a>
  <a href="https://trendshift.io/repositories/25391?utm_source=repository-badge&amp;utm_medium=badge&amp;utm_campaign=badge-repository-25391" target="_blank" rel="noopener noreferrer"><img src="https://trendshift.io/api/badge/repositories/25391" alt="JuliusBrussee%2Fcaveman | Trendshift" width="250" height="55"/></a>
</p>

<p align="center">
  <a href="https://github.com/JuliusBrussee/caveman/stargazers"><img src="https://img.shields.io/github/stars/JuliusBrussee/caveman?style=flat&color=yellow" alt="Stars"></a>
  <a href="./INSTALL.md"><img src="https://img.shields.io/badge/skill_works_with-30%2B_agents-orange?style=flat" alt="30+ agents"></a>
  <a href="#wrap-any-agent"><img src="https://img.shields.io/badge/wrap-10_native_agents-blue?style=flat" alt="10 native wrap profiles"></a>
  <a href="#license"><img src="https://img.shields.io/badge/license-MIT_%2B_BSL-green?style=flat" alt="License"></a>
  <a href="https://skills.sh/JuliusBrussee/caveman"><img src="https://skills.sh/b/JuliusBrussee/caveman"></a>
</p>

<p align="center">
  <a href="#see-it">See it</a> ·
  <a href="#install">Install</a> ·
  <a href="#the-numbers">Numbers</a> ·
  <a href="#the-skill-unpacked">Skill</a> ·
  <a href="#the-proxy-unpacked">Proxy</a> ·
  <a href="#wrap-any-agent">Wrap</a> ·
  <a href="./docs/README.md">Docs</a> ·
  <a href="#privacy-and-a-small-favor">Privacy</a> ·
  <a href="#license">License</a>
</p>

---

## See it

<table>
<tr>
<th width="50%">🗣️ Normal agent · 69 tokens</th>
<th width="50%"><img src="docs/assets/dancing-rock.svg" width="18" height="18" alt=""> Caveman agent · 19 tokens</th>
</tr>
<tr>
<td valign="top">

> The reason your React component is re-rendering is likely because you're creating a new object reference on each render cycle. When you pass an inline object as a prop, React's shallow comparison sees it as a different object every time, which triggers a re-render. I'd recommend using useMemo to memoize the object.

</td>
<td valign="top">

> New object ref each render. Inline object prop = new ref = re-render. Wrap in `useMemo`.

</td>
</tr>
</table>

Same diagnosis. Same fix. Same `useMemo`. The only thing that died was the throat-clearing.

Code, commands, file paths, and exact error messages never get cavemanned. Only the prose around them does.

```
┌──────────────────────────────────────────────────┐
│   output tokens saved (skill)   ██████░░░    65% │
│   input tokens saved  (proxy)   ███░░░░░░    33% │
│   code changed                  ░░░░░░░░░     0% │
│   vibes                         █████████    OOG │
└──────────────────────────────────────────────────┘
```

Caveman no make brain smaller. Caveman make *mouth* smaller.

## Install

Caveman come in two sizes.

**Small rock: the skill.** A rule file that makes your agent answer in caveman. MIT, free forever, works in [30+ agents](./INSTALL.md) (Claude Code, Codex, Gemini, Cursor, Windsurf, Cline, Copilot, more). One command:

```bash
npx skills add JuliusBrussee/caveman
```

Type `/caveman` if your agent doesn't wake up on its own. That the whole install. One rock.

**Big rock: the proxy.** Runs on your machine, between your agent and the AI provider, and shrinks what the agent *reads* before every call. Everything it squeezes gets a backup on your disk, so the agent can always pull the original back. MIT CLI, BSL-1.1 runtime:

```bash
npm install -g @caveman-ai/cli && caveman setup --install
caveman claude        # or codex · gemini · aider · kilo · qwen · opencode · hermes · openclaw · pi
```

They stack. Most people start with the small rock and graduate.

<details>
<summary><strong>More doors into the cave</strong> · full installer, Windows, single agents, uninstall</summary>

<br>

The full installer wires up Claude Code hooks and the statusline badge, finds every supported agent on your machine, and skips agents you no have. Safe to re-run. Needs Node.js 22.13+.

```bash
curl -fsSL https://raw.githubusercontent.com/JuliusBrussee/caveman/v2.6.0/install.sh | bash
```

Windows, PowerShell 5.1+:

```powershell
irm https://raw.githubusercontent.com/JuliusBrussee/caveman/v2.6.0/install.ps1 | iex
```

Just one agent:

```bash
# Claude Code
claude plugin marketplace add JuliusBrussee/caveman && claude plugin install caveman@caveman

# Gemini CLI
gemini extensions install https://github.com/JuliusBrussee/caveman

# Qwen Code CLI, then its Caveman wrapper
npm i -g @qwen-code/qwen-code
caveman qwen

# Codex, Cursor, Windsurf, Cline, and other skills-compatible agents
npx skills add JuliusBrussee/caveman --skill '*' -a codex --yes  # replace codex with your agent profile
```

**Install broke?** Open your agent in this repo and say: *"Read CLAUDE.md and INSTALL.md, install caveman for me."* Agent read repo, agent fix own brain. Snake eat tail.

Changed your mind: `npx -y github:JuliusBrussee/caveman -- --uninstall`

</details>

The full 30+ agent matrix, dry runs, flags, and verification live in [INSTALL.md](./INSTALL.md).

## The numbers

A token is what AI billing counts, roughly three quarters of a word. Your agent pays for every token it writes and every token it reads. Reading is usually the bigger bill. The skill cuts the writing. The proxy cuts the reading.

### Skill: writing less

Ten ordinary coding prompts through the real Claude API, with the skill and without. Same model, same questions. Output tokens per reply:

| Task                               | Normal   | Caveman | Saved   |
| ---------------------------------- | -------: | ------: | ------: |
| Implement React error boundary     | 3454     | 456     | 87%     |
| Set up PostgreSQL connection pool  | 2347     | 380     | 84%     |
| Explain git rebase vs merge        | 702      | 292     | 58%     |
| Refactor callback to async/await   | 387      | 301     | 22%     |
| **Average across all ten prompts** | **1214** | **294** | **65%** |

Best row and worst row both up there on purpose. Caveman wins big when the agent would have written an essay, and barely at all when the answer was already mostly code.

<details>
<summary><strong>All ten prompts</strong> · regenerate with <code>uv run python benchmarks/run.py</code></summary>

<br>

<!-- BENCHMARK-TABLE-START -->
| Task                                    | Normal   | Caveman | Saved   |
| --------------------------------------- | -------- | ------- | ------- |
| Explain React re-render bug             | 1180     | 159     | 87%     |
| Fix auth middleware token expiry        | 704      | 121     | 83%     |
| Set up PostgreSQL connection pool       | 2347     | 380     | 84%     |
| Explain git rebase vs merge             | 702      | 292     | 58%     |
| Refactor callback to async/await        | 387      | 301     | 22%     |
| Architecture: microservices vs monolith | 446      | 310     | 30%     |
| Review PR for security issues           | 678      | 398     | 41%     |
| Docker multi-stage build                | 1042     | 290     | 72%     |
| Debug PostgreSQL race condition         | 1200     | 232     | 81%     |
| Implement React error boundary          | 3454     | 456     | 87%     |
| **Average**                             | **1214** | **294** | **65%** |
<!-- BENCHMARK-TABLE-END -->

</details>

> [!IMPORTANT]
> Before you multiply 65% by your invoice: the skill only shortens **output**. Input and reasoning tokens don't change, and the skill's own rules cost about 1 to 1.5k input tokens every turn. Whole-session savings land lower than the table. On work that was already terse, you can lose money. Speed and readability are the product. The discount is the bonus. Full accounting in [docs/HONEST-NUMBERS.md](./docs/HONEST-NUMBERS.md).

> **Maintainer note.** If you read one linked doc, read that one. I wrote it after [#550](https://github.com/JuliusBrussee/caveman/issues/550), where someone's Cursor A/B went the wrong way and I couldn't reproduce it. Caveman is a shorter agent, not free money. Measure your own setup before you tell your boss anything.

### Proxy: reading less

Your agent rereads logs, test output, diffs, and half your repo all day. The proxy shrinks that stream before it reaches the provider. Pinned 54-run Claude Code benchmark, provider-reported input tokens, three runs per case:

| Case                   | Direct Claude Code | Through caveman | Change     |
| ---------------------- | -----------------: | --------------: | ---------: |
| CSV outlier hunt       | 165,823            | 74,484          | -55.1%     |
| Log needle in haystack | 148,807            | 74,068          | -50.2%     |
| YAML config drift      | 132,124            | 71,027          | -46.2%     |
| Test output failure    | 150,377            | 108,514         | -27.8%     |
| Deployment JSON drift  | 147,975            | 108,939         | -26.4%     |
| Dashboard HTML alert   | 140,687            | 154,641         | **+9.9%**  |
| **Total**              | **885,793**        | **591,673**     | **-33.2%** |

All 18 of 18 exact-answer checks passed, so the squeeze cost nothing in correctness. Method, confidence intervals, and limits: [docs/WRAP-BENCHMARK.md](./docs/WRAP-BENCHMARK.md).

> **Maintainer note.** The HTML row is red and it stays red. That case had no compression transform, so caveman paid its own overhead and won nothing back. The day I hide a red row is the day you should stop trusting the green ones.

Browsing too: a focused question against a 200-row table costs **121 tokens** through caveman's view of the page, against 15,704 for the Playwright ARIA baseline. That's **129.8× smaller** ([`browse/BENCHMARK.md`](./browse/BENCHMARK.md)).

## The skill, unpacked

One rule file, one talking style, plus a small toolbox. `/caveman lite|full|ultra|wenyan-lite|wenyan-full|wenyan-ultra` sets intensity. `/caveman off` or `normal mode` turns it off.

<details>
<summary><strong>Everything in the box</strong> · commit messages, reviews, subagents, work patterns</summary>

<br>

| Tool / command                                                                                                                                  | What you get                                                                                                                |
| ----------------------------------------------------------------------------------------------------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------- |
| `/caveman [lite\|full\|ultra\|wenyan-lite\|wenyan-full\|wenyan-ultra\|off]`                                                                     | Shorter replies at the intensity you choose.                                                                                |
| `cavecrew-investigator`, `cavecrew-builder`, `cavecrew-reviewer`                                                                                | Compressed subagent presets for locating, editing, and reviewing code.                                                      |
| `/caveman-commit`                                                                                                                               | Terse Conventional Commit messages.                                                                                         |
| `/caveman-review`                                                                                                                               | One-line, actionable review findings.                                                                                       |
| `/caveman-compress <file>`                                                                                                                      | Smaller Markdown memory files, with the original backed up.                                                                 |
| `/caveman-stats`                                                                                                                                | Local session token usage and estimated savings in Claude Code.                                                             |
| `/caveman-help`                                                                                                                                 | One-screen reminder of every mode and command.                                                                              |
| `investigate-first`, `lean-build`, `surgical-patch`, `safe-refactor`, `migration`, `verify-and-stop`                                            | Work patterns that write less code, so the agent bills fewer tokens. Your agent picks these up on its own when a task fits. |
| `/caveman-setup`, `/caveman-discover`, `/caveman-learn`, `/caveman-manage`, `/caveman-optimize`, `/caveman-explore`, `/caveman-evidence-review` | Drive the caveman engine and proxy: set it up, find where tokens go, act on what it finds.                                  |

</details>

## The proxy, unpacked

One local process. Your agent talks to it, it talks to your provider. No Caveman server in the path, and your Claude Pro/Max login passes through to Anthropic untouched. Originals of everything it compresses sit in a SQLite file on your machine with a recovery handle, so the agent can always ask for the full version back.

<p align="center">
  <img src="docs/assets/caveman-demo.gif" alt="Terminal demo: caveman compress reads a large JSON payload and emits a much smaller compressed version, byte-exact recoverable">
</p>

<details>
<summary><strong>What the engine keeps, by payload type</strong> · and the wrap stack diagram</summary>

<br>

<p align="center">
  <img src="docs/assets/wrap-stack.svg" alt="coding agent talks to a local caveman proxy that forwards upstream to the provider with auth passed through byte-exact; a CCR store below the proxy keeps the original bytes and returns a recovery handle to the agent; an MCP toolkit side-channel gives the agent caveman_retrieve, toon encode/decode, and browse" width="820">
</p>

`detect()` types each payload and routes it to a compressor that keeps what answers depend on:

| Detected type   | Keeps                                                                  | Target savings |
| --------------- | ---------------------------------------------------------------------- | -------------- |
| `json`          | keys, structure, error/message subtrees; collapses repetitive arrays   | 70-90%         |
| `log`           | errors, stack traces, first/last lines; drops INFO and progress noise  | 85-95%         |
| `code`          | imports, signatures, types; elides function bodies, syntax stays valid | 40-70%         |
| `diff`          | file/hunk headers and changed lines; elides repeated context           | 60-80%         |
| `search-result` | top/bottom hits plus diagnostic/security hits                          | 80-95%         |
| `text` / HTML   | headings, opening/closing context, important sections                  | 50-80%         |

`contextwindow.Pack()` additionally fits candidate context into a token budget by BM25 relevance, recency, and error signal, returned in original order so chronology survives.

Any MCP host gets the same powers through five tools: `caveman_compress`, `caveman_retrieve`, `caveman_stats`, `caveman_toon_encode`, `caveman_toon_decode`.

</details>

### Where your tokens go

Months of your agent history already sit on your disk. `caveman learn` reads it, locally, read-only, no account, and ranks your token sinks worst-first with a one-line fix behind each.

```bash
caveman learn             # Claude Code + Codex + Gemini CLI + opencode; aider via CAVEMAN_AIDER_ROOT
caveman learn implement   # hand the fixes to Claude Code or Codex, one diff at a time, applied only on your yes
```

<p align="center">
  <img src="docs/assets/learn-report.png" alt="Caveman Learn report: TLDR summary and savings cards on the left; ranked token sinks with an expanded fix and a session context depth histogram on the right" width="900">
</p>

`implement` re-measures after every change and reverts anything that didn't lower tokens per turn. Caveman never makes your agent dumber to make it cheaper.

### More verbs

```bash
caveman explore install         # read-only FastContext subagent: finds code as path:line
caveman shrink -- pnpm test     # compress noisy command output, byte-exact recoverable
caveman browse <url>            # local Chrome over a compressed a11y tree
caveman mem remember|recall     # durable memory; `mem recover <handle>` = original bytes
caveman trial -- claude         # A/B a real session, then `trial report`
caveman toon encode|decode      # the TOON re-encoder, standalone
caveman stats                   # what caveman actually did, by content type
```

### Pixel mode

Caveman eating its own tail. Every skill you install is prompt text your agent reloads on every call. `caveman convert` renders the skill body to PNG pages in place, and the model reads it as an image. On the caveman skill itself: **1,069 to 415 estimated tokens, a 61% cut**.

```bash
caveman convert --dry-run        # every installed skill, with the token math, no writes
caveman convert --agent claude   # convert the profitable ones
caveman convert --revert         # byte-identical restore from SKILL.orig.md
```

Convert only fires when pages beat the text. Any failure leaves the skill byte-identical and names the gate that said no.

### Wrap any agent

`caveman <agent>` turns the proxy on for good and launches the agent. `caveman wrap <agent>` runs one session and leaves nothing behind. It never edits your config files.

| Agent                | Vendor           | How it's wrapped                                             |
| -------------------- | ---------------- | ------------------------------------------------------------ |
| **Claude Code**      | Anthropic        | env vars                                                     |
| **OpenAI Codex CLI** | OpenAI           | env vars (API key) · ephemeral `CODEX_HOME` (ChatGPT login)  |
| **Gemini CLI**       | Google           | env vars                                                     |
| **Aider**            | OpenAI/Anthropic | env vars                                                     |
| **Kilo Code**        | Kilo Code        | `KILO_CONFIG_CONTENT`, your `kilo.json` untouched            |
| **Qwen Code**        | QwenLM           | ephemeral system-settings overlay, source settings untouched |
| **opencode**         | sst              | inline config via env, your `opencode.json` untouched        |
| **Hermes Agent**     | Nous Research    | `--provider custom` + env                                    |
| **OpenClaw**         | OpenClaw         | ephemeral merged config, your config read-only               |
| **Pi**               | pi.dev           | bundled native extension, your `~/.pi` config untouched      |

<details>
<summary><strong>Fine print</strong> · tested versions, default loadout, SDK recipes</summary>

<br>

Tested against real sessions on **Hermes v0.18.0**, **OpenClaw 2026.6.11**, **Pi 0.84.2**, **Kilo Code 7.5.6** (the CLI, not the editor extension), and **Qwen Code 0.22.3**. Persistent shortcuts are journaled and reversible with `caveman disable <agent>`.

OpenClaw, for the record, is a lobster. Lobster claw still sharp. Lobster mouth now small.

The default wrap hands the agent the five MCP tools, the browse server when Chrome resolves, command-output shrink on Claude, opencode, Gemini, Hermes, and OpenClaw, and pixel mode on new skill installs. Codex skips the shrink hook because its runtime rejects the rewrite ([openai/codex#18491](https://github.com/openai/codex/issues/18491)). Turn pieces off in `~/.caveman-cloud/config.json`.

Agent not on the list? Point any provider SDK or framework (Vercel AI SDK, LangChain, LiteLLM, OpenAI Agents, CrewAI, PydanticAI) at the local proxy with a `baseURL` swap: [`integrations/recipes/`](./integrations/recipes/). New native agent is usually one JSON profile in [`agents/profiles/`](./agents/profiles/).

</details>

## The whole cave

One idea everywhere: **agent do more with less.**

| Repo                                                                  | What it shrinks                                          | Status            |
| --------------------------------------------------------------------- | -------------------------------------------------------- | ----------------- |
| **[caveman](https://github.com/JuliusBrussee/caveman)** *(you here)*  | What the agent **says** (skill) and **reads** (proxy)    | live              |
| **[caveman-browse](https://github.com/JuliusBrussee/caveman-browse)** | What the agent **sees in the browser**                   | live              |
| **caveman-agent-sdk**                                                 | What your production agent **loads, calls, and spends**  | own repo · in dev |
| **[cavegemma](https://github.com/JuliusBrussee/cavegemma)**           | The compression **baked into weights** (Gemma fine-tune) | labs              |
| **[caveman-code](https://github.com/JuliusBrussee/caveman-code)**     | The **whole agent**, end to end                          | frozen            |
| **[cavemem](https://github.com/JuliusBrussee/cavemem)**               | What the agent **remembers**, across sessions            | frozen            |
| **[cavekit](https://github.com/JuliusBrussee/cavekit)**               | The **build loop**, spec-driven                          | frozen            |

Frozen ones still install and work. Their best ideas moved in here.

**Caveman make token small. Caveman Cloud make it *provable*.** Local numbers are `inferred`, pinned benchmarks `benchmark_counterfactual`, neither is an invoice. Live traffic behind eval gates with signed receipts earns `verified`. That's Cloud. **[Waitlist at caveman.so](https://caveman.so)**

## Privacy, and a small favor

Your agent still talks to the provider you chose. The skill and hooks run entirely on your machine, and nothing here needs an account.

The `caveman` CLI does send anonymous usage stats by default, and here's the honest why: caveman is free, one person maintains it, and those stats are how I find out which commands people actually use and how many tokens caveman actually saves in the wild. That's what keeps this thing free and pointed in the right direction. Fair trade, we think.

What it sends: which commands ran, plus token counts through and cut. What it never sends: your prompts, your code, your file paths, or anything that could identify you. It tells you all this the first time you run it.

Not into it? One command and it's off forever, no hard feelings:

```bash
caveman telemetry off      # or set DO_NOT_TRACK=1
```

Exact network, telemetry, and storage boundaries: [SECURITY.md](./SECURITY.md).

## License

Split license. Skill and adoption surfaces are [MIT](./LICENSE). Engine-linked runtime is BSL-1.1 source-available, not OSI Open Source before Change Date.

**MIT:** the skill, Agent SDK and initializer, the CLI, both client SDKs, contracts, provider catalog, extension shell, and the thin cavemem clients. Free like mammoth on open plain.

**BSL-1.1:** Engine, Proxy, Cache Engine, rewriter, Browse, MCP server, `shrink`, cavemem Go core, and shared Go platform. New Engine-linked runtime modules default to BSL-1.1. Read it, fork it, self-host it for your own first-party traffic free, production included. Each version converts to **Apache-2.0** on the earlier of `2030-06-21` or four years after it ships. Hosting it for third parties needs a commercial license.

`engine/pixel` embeds [pxpipe](https://github.com/teamchong/pxpipe) (MIT) plus glyph atlases derived from Spleen 5×8 (BSD-2-Clause) and GNU Unifont (dual OFL-1.1 / GPLv2-with-font-exception); its `NOTICE` travels with that source.

"Caveman" and the rock logo are trademarks of Julius Brussee. "Powered by Caveman" is fine when true.

## Star this repo

Caveman save you token, save you money. Star cost zero. Fair trade. ⭐

[![Star History Chart](./docs/assets/star-history.png)](https://star-history.com/#JuliusBrussee/caveman&Date)

---

<sub>
<strong>Docs:</strong>
<a href="./docs/README.md">Technical manual</a> ·
<a href="./INSTALL.md">Install matrix</a> ·
<a href="./docs/HONEST-NUMBERS.md">Honest numbers</a> ·
<a href="./docs/WRAP-BENCHMARK.md">Wrap benchmark</a> ·
<a href="./LICENSE">License</a> ·
<a href="./CONTRIBUTING.md">Contributing</a> ·
<a href="./CLAUDE.md">Maintainer guide</a> ·
<a href="https://github.com/JuliusBrussee/caveman/issues">Issues</a>
<br>
MIT skill · BSL-1.1 engine. Few token. No lie.
</sub>
