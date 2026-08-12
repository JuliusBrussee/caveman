<p align="center">
  <img src="docs/assets/caveman-logo-banner.png" alt="Caveman" width="720">
</p>

<p align="center">
  <strong>why use many token when few do trick</strong>
</p>

<p align="center">
  Original skill made agents say less. Caveman 2 makes them read less too.<br>
  <strong><a href="./docs/WRAP-BENCHMARK.md">33.2% fewer provider-reported input tokens</a> in a pinned Claude Code benchmark.</strong> <code>benchmark_counterfactual</code><br>
  Keep your agent. Brain big. Context small.
</p>

<p align="center">
  <a href="https://trendshift.io/repositories/25391?utm_source=repository-badge&amp;utm_medium=badge&amp;utm_campaign=badge-repository-25391" target="_blank" rel="noopener noreferrer"><img src="https://trendshift.io/api/badge/repositories/25391" alt="JuliusBrussee%2Fcaveman | Trendshift" width="250" height="55"/></a>
</p>

<p align="center">
  <a href="https://github.com/JuliusBrussee/caveman/stargazers"><img src="https://img.shields.io/github/stars/JuliusBrussee/caveman?style=flat&color=yellow" alt="Stars"></a>
  <a href="./INSTALL.md"><img src="https://img.shields.io/badge/skill_works_with-30%2B_agents-orange?style=flat" alt="30+ agents"></a>
  <a href="#wrap-any-agent"><img src="https://img.shields.io/badge/wrap-7_native_agents-blue?style=flat" alt="7 native wrap profiles"></a>
  <a href="#license"><img src="https://img.shields.io/badge/license-MIT_%2B_BSL-green?style=flat" alt="License"></a>
  <a href="https://skills.sh/JuliusBrussee/caveman"><img src="https://skills.sh/b/JuliusBrussee/caveman"></a>
</p>

<p align="center">
  <a href="#install">Install</a> ·
  <a href="#the-skill">See it</a> ·
  <a href="#keep-your-agent">Adoption paths</a> ·
  <a href="#caveman-proxy">Caveman Proxy</a> ·
  <a href="#what-the-engine-does-to-a-payload">Payloads</a> ·
  <a href="#caveman-browse-benchmark">Browse</a> ·
  <a href="#pixel-mode">Pixel</a> ·
  <a href="#the-skill-compressor">Skill→PNG</a> ·
  <a href="#wrap-any-agent">Wrap</a> ·
  <a href="#license">License</a>
</p>

---

## Install

Caveman is three separate installs. Each works alone. Not sure? Start with the skill — it needs no account, proxy, Go toolchain, or code changes.

**Shorter answers — the skill (MIT).** The caveman skill + slash commands in every supported agent found on your machine. Nothing else.

```bash
curl -fsSL https://raw.githubusercontent.com/JuliusBrussee/caveman/v1.10.0/install.sh | bash
```

**Smaller inputs — Caveman Proxy.** The `caveman` CLI plus signed local binaries: proxy, engine, MCP recovery, memory — and the optional browse + shrink tools. Then `caveman claude` wraps your agent.

```bash
npm install -g @caveman-ai/cli && caveman setup --install
```

**Compressed browsing — Caveman Browse.** Included in `caveman setup --install` (needs Chrome) — a local Chrome driver your agent reaches as MCP tools.

```bash
caveman browse <url>
```

**A new agent — Agent SDK.** A TypeScript agent project on the native Caveman runtime. Client SDKs: `npm i @caveman-ai/sdk` · `pip install caveman-sdk`.

```bash
npm create @caveman-ai/agent@latest my-agent
```

On Windows, the skill installs with `irm https://raw.githubusercontent.com/JuliusBrussee/caveman/v1.10.0/install.ps1 | iex` (PowerShell 5.1+).

The skill installer needs Node.js 18+, finds supported agents already on your machine, skips the rest, and is safe to rerun. Prefer one agent only?

```bash
# Claude Code
claude plugin marketplace add JuliusBrussee/caveman && claude plugin install caveman@caveman

# Gemini CLI
gemini extensions install https://github.com/JuliusBrussee/caveman

# Codex, Cursor, Windsurf, Cline, and other skills-compatible agents
npx skills add JuliusBrussee/caveman --skill '*' -a codex --yes  # replace codex with your agent profile
```

See [INSTALL.md](./INSTALL.md) for the full 30+ agent matrix, dry run, flags, verification, and uninstall.

Caveman started as a skill/plugin for [Claude Code](https://docs.anthropic.com/en/docs/claude-code), Codex, Gemini, Cursor, Windsurf, Cline, Copilot, and 30+ other agents. Install once; the agent drops filler and answers in tight caveman-speak while keeping code, commands, and errors byte-for-byte exact.

Agent mouth got smaller. Appetite did not. Tool schemas, files, logs, history, and skill bodies still crossed the provider boundary in full, often on every turn. Caveman 2 shrinks that input before the provider call. Caveman Proxy sits under your existing agent; Caveman Engine powers compression and stores every moved byte for exact recovery.

## The skill

The original. MIT, and it stays MIT.

```bash
curl -fsSL https://raw.githubusercontent.com/JuliusBrussee/caveman/v1.10.0/install.sh | bash
```

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
</table>

Same concrete fix, fewer words.

Type `/caveman` if your agent does not activate it automatically. Switch with `/caveman lite|full|ultra|wenyan-lite|wenyan-full|wenyan-ultra`; turn it off with `/caveman off` or `normal mode`.

One install also brings the small tools:

| Tool / command | What you get |
|---|---|
| `/caveman [lite\|full\|ultra\|wenyan-lite\|wenyan-full\|wenyan-ultra\|off]` | Shorter replies at the intensity you choose. |
| `cavecrew-investigator`, `cavecrew-builder`, `cavecrew-reviewer` | Compressed subagent presets for locating, editing, and reviewing code. |
| `/caveman-commit` | Terse Conventional Commit messages. |
| `/caveman-review` | One-line, actionable review findings. |
| `/caveman-compress <file>` | Smaller Markdown memory files, with the original backed up. |
| `/caveman-stats` | Local session token usage and estimated savings in Claude Code. |

That is the whole adoption path for shorter answers. Stop there if that is all you need. Add the local Proxy later when you want Caveman to shrink what the agent reads too.

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

Use the skill by itself, or keep going when input compression becomes worth the extra setup.

Shorter answers only fixed output. Tool catalogs, MCP results, logs, repeated files, and skill bodies ride upstream again and again. Caveman Proxy catches them before the next model call; Caveman Engine compresses them locally.

## Keep your agent

Caveman works underneath the stack you already use. Adopt only the layer you need.

| Need | Smallest Caveman path |
|---|---|
| Shorter, cleaner answers | Install the MIT skill above. No Caveman account, proxy, or code changes. |
| Less input in Claude Code, Codex, Gemini, Aider, opencode, Hermes, or OpenClaw | `npm install -g @caveman-ai/cli`, then wrap your existing agent with one `caveman` command. |
| Vercel AI SDK | Point the OpenAI-compatible or Anthropic provider `baseURL` at Caveman. Keep the AI SDK loop, tools, and call sites. See the exact [Vercel AI SDK recipe](./integrations/recipes/vercel-ai-sdk.json). |
| LangChain, LiteLLM, OpenAI Agents, CrewAI, PydanticAI, or a provider SDK | Point the existing provider client at Caveman. See [`integrations/recipes/`](./integrations/recipes/). |
| A new TypeScript agent | Run `npm create @caveman-ai/agent@latest my-agent`, powered by [`@caveman-ai/agent`](./packages/agent). |

## Caveman Proxy

One command wraps your agent and routes provider traffic through a local proxy. Caveman Engine powers its compression. In a pinned 54-run Claude Code benchmark, Caveman used **33.2% fewer provider-reported input tokens** than direct Claude Code while passing all 18 exact-answer checks. [Method, per-case results, and limits.](./docs/WRAP-BENCHMARK.md) `benchmark_counterfactual`

No code change. In local mode, Caveman sends no prompts or outputs to a Caveman backend: the proxy forwards each request to your chosen provider, while CCR recovery copies stay on your disk. Claude Pro/Max OAuth credentials pass through to Anthropic as-is.

<p align="center">
  <img src="docs/assets/wrap-stack.svg" alt="coding agent talks to a local caveman proxy that forwards upstream to the provider with auth passed through byte-exact; a CCR store below the proxy keeps the original bytes and returns a recovery handle to the agent; an MCP toolkit side-channel gives the agent caveman_retrieve, toon encode/decode, and browse" width="820">
</p>

### Install the CLI

```bash
npm install -g @caveman-ai/cli
caveman setup --install   # downloads the signed runtime binaries
```

`setup --install` verifies the signed checksum manifest and the SHA-256 of
every binary before an atomic install. Prefer building from source? A clone
plus `scripts/install-local-cli.sh` (macOS/Linux) or
`pwsh -File scripts/install-local-cli.ps1` (Windows) still works — that path
needs Go and `pnpm`. SDK users can point provider base URLs at the local
Proxy directly (`ANTHROPIC_BASE_URL=http://127.0.0.1:8787/anthropic`).

```bash
caveman claude                  # full stack (default): S4 compress + TOON best-of + caveman & browse MCP tools + output shrink
caveman wrap --off codex        # byte-safe pass-through metering only
caveman wrap --pixel claude     # lossy text → PNG pixel mode (model-gated)
```

Subscription logins work — see the [note](#wrap-any-agent) below.

| Mode | What it does | Bytes the model sees |
|---|---|---|
| default stack *(`caveman claude`)* | Structural compression routed per content type (table below), plus uniform JSON tool results re-encoded as TOON only when measured smaller; config `toon: false` turns it off. | Changed, recoverable |
| `--off` | Counts tokens and cost. Changes nothing. | **Byte-identical** |
| `--pixel` | Dense text slabs rendered to PNG pages for vision models. | Changed, recoverable |

Safety gates stay explicit:

- **CCR first.** Before a lossy transform goes upstream, original bytes land in **CCR**, a content-addressed store on your disk. The agent retrieves them through `caveman_retrieve` or `caveman retrieve <handle>`. Parse problem, store failure, or larger result sends original bytes unchanged.
- **Visible declines.** Pixel refuses sparse code. Convert refuses skills when PNG pages do not beat text. TOON runs only when its output measures smaller. Each decline includes its reason.
- **Labeled evidence.** Local results report `inferred`: estimates for choosing what to try. `verified` requires real traffic and eval gates. Offline caveman never reports it.

## What the engine does to a payload

`detect()` types each payload, then routes it to a compressor that keeps what answers depend on:

| Detected type | Keeps | Target |
|---|---|---|
| `json` | keys, structure, error/message subtrees; collapses repetitive arrays | 70–90% |
| `log` | errors, stack traces, first/last lines; drops INFO and progress noise | 85–95% |
| `code` | imports, signatures, types; elides function bodies, syntax stays valid | 40–70% |
| `diff` | file/hunk headers and changed lines; elides repeated context | 60–80% |
| `search-result` | top/bottom hits plus diagnostic/security hits | 80–95% |
| `text` / HTML | headings, opening/closing context, important sections | 50–80% |

All targets `inferred`. The code compressor uses tree-sitter (Go, Python, JS/TS) under cgo, with a pure-Go fallback that handles Go only. `contextwindow.Pack()` additionally fits candidate context into a token budget by BM25 relevance, recency, and error signal, returned in original order so chronology survives.

Beyond the proxy, the same engine powers a set of verbs:

```bash
caveman explore install         # read-only FastContext subagent: finds code as path:line
                                #   cites without burning your solver's context (Claude Code)
caveman shrink -- pnpm test     # compress noisy command output, byte-exact recoverable
caveman browse <url>            # local Chrome driver over a compressed a11y tree
caveman mem remember|recall     # durable memory; `mem recover <handle>` = original bytes
caveman learn                   # scan your real agent history → Cave Score + ranked token sinks
caveman trial -- claude         # A/B a real session, then `trial report`
caveman toon encode|decode      # the TOON re-encoder, standalone
caveman stats                   # what caveman actually did, by content type
```

The MCP server exposes five tools to any MCP host: `caveman_compress`, `caveman_retrieve`, `caveman_stats`, `caveman_toon_encode`, `caveman_toon_decode`.

### Caveman Browse benchmark

```bash
npm install -g @caveman-ai/cli && caveman setup --install   # browse ships with the CLI binaries; needs Chrome
caveman browse <url>
```

Measured 2026-08-10 across five independent Chrome runs with Chrome `151.0.7922.108`, Playwright `1.56.1`, and Caveman's offline `o200k_base` counter. Results are `inferred` token counts, reported as medians.

| Fixture | Raw AX JSON | Playwright ARIA | Caveman full | Caveman focused query |
|---|---:|---:|---:|---:|
| 200-row operations table | 398,494 | 15,704 | 13,368 — 14.88% below Playwright | **121 — 129.8× smaller than Playwright** |
| Small checkout form | 4,186 | **67** | 157 — 2.34× larger | 111 — 1.66× larger |

Large pages reward query-focused disclosure. Tiny pages can lose because Caveman also returns action UIDs, a recovery handle, and exact accounting. Playwright baseline contains only ARIA text, so comparison favors Playwright.

Captured serializer fixture improved from 380 tokens to 58, an 84.7% reduction. Full delivered payload is 126 tokens after CCR and accounting metadata; four-tool MCP catalog costs 287 tokens. Full method, ranges, fixtures, functional gates, and claim boundary live in [`browse/BENCHMARK.md`](./browse/BENCHMARK.md).

## Pixel mode

```bash
npm install -g @caveman-ai/cli && caveman setup --install
caveman wrap --pixel claude
```

The headline trick. A dense wall of text costs a lot of text tokens. Rendered to a PNG for a vision model, the same wall costs image tokens, far fewer for the right content. Pixel mode renders big request slabs (minified JSON tool catalogs, long-line logs, old history) into glyph-rendered PNG pages the model reads as vision input.

<p align="center">
  <img src="docs/assets/pixel-pipeline.svg" alt="agent to caveman proxy (record, compress, pixel modes) to provider, with a CCR store keeping original bytes byte-exact and a recovery handle returned to the agent; the pixel branch renders dense text slabs into PNG pages" width="820">
</p>

<p align="center">
  <img src="docs/assets/pixel-sample.png" alt="A dense block of text rendered to a single grayscale PNG page by caveman pixel mode" width="720">
</p>

<p align="center">
  <sub>Real render, bundled here: 8,622 chars → one 1568×232 PNG, est. <strong>2,597 text → 534 image tokens</strong>, <code>inferred</code>.</sub>
</p>

On a genuinely dense request (a 63.7k-char minified JSON tool-catalog slab plus a 93k-char long-line log, model `claude-fable-5`):

```
55,413 est. text tokens  →  11,402 est. image tokens   ·  −79%  ·  7 PNG pages  ·  inferred
```

The original is stored byte-exact in CCR first; the proxy returns an `X-Caveman-Recovery-Handle` header and the agent pulls the real bytes back via `caveman_retrieve`.

> [!IMPORTANT]
> **Pixel only pays on dense, long-line content.** Sparse code with short lines is honestly *not* profitable: the PNG carries more overhead than the text it replaces, so the profitability gate declines it and the bytes pass through untouched.

Runs only for models with measured render legibility, `claude-fable-5` and `gpt-5.6` by default; override with `pixel_models` config / `CAVE_PIXEL_MODELS`. Pixel ports [pxpipe](https://github.com/teamchong/pxpipe) (MIT); font attribution in the [License](#license).

### The skill compressor

Full circle: the engine now compresses the thing caveman started as. Skills are prompt files, and the fat ones load their whole body on every invocation. `caveman convert` walks your installed skills (Claude Code and Codex folders today) and renders each `SKILL.md` body to PNG pages in place. Frontmatter stays text, so discovery and triggering work exactly as before; the body becomes a two-line stub pointing the agent at the pages.

```bash
caveman convert --dry-run        # every installed skill, with the token math, no writes
caveman convert --agent claude   # convert the profitable ones
caveman convert --revert         # byte-identical restore from SKILL.orig.md
```

Measured on the caveman skill itself: **1,069 → 415 est. tokens, −61%**, `inferred`. Convert only fires when pages + stub beat the text. Any failure (no engine binary, parse error, dropped chars, not smaller) leaves the skill byte-identical and tells you which gate said no. `caveman skills install` auto-pixels new installs by default (`--no-pixel` to opt out).

## Wrap any agent

```bash
npm install -g @caveman-ai/cli && caveman setup --install
caveman claude   # or codex · gemini · aider · opencode · hermes · openclaw
```

`caveman <agent>` wraps seven agents natively. Adding one is a data change (a single JSON profile in [`agents/profiles/`](./agents/profiles/)), no code.

| Agent | Vendor | How it's wrapped |
|---|---|---|
| **Claude Code** | Anthropic | env vars |
| **OpenAI Codex CLI** | OpenAI | env vars (API key) · ephemeral `CODEX_HOME` (ChatGPT login) |
| **Gemini CLI** | Google | env vars |
| **Aider** | OpenAI/Anthropic | env vars |
| **opencode** | sst | inline config via env, your `opencode.json` untouched |
| **Hermes Agent** | Nous Research | `--provider custom` + env |
| **OpenClaw** | OpenClaw | ephemeral merged config, your config read-only |

Wrap never edits your own config files. Real sessions round-trip in record mode, tested against **Hermes v0.18.0** and **OpenClaw 2026.6.11**.

> [!NOTE]
> **Subscription logins work.** Claude Pro/Max OAuth tokens pass through the proxy as-is (`Authorization: Bearer` is preserved), so a wrapped Claude Code on a subscription gets full compression and metering. **Codex ChatGPT logins wrap too**: wrap detects the subscription, builds an ephemeral `CODEX_HOME` (your `~/.codex` is never written) pointing a custom provider at the proxy's `/chatgpt` passthrough, and your OAuth headers ride through byte-exact. That path is metering-only for now — honest token counts, dollars stay zero because subscription traffic has no per-token price — no compression yet. The one remaining exception: a provider pinned inside another agent (e.g. `openai-codex` inside OpenClaw) is left on its own path with a printed note instead of a broken login.

The default hands the agent the whole loadout, each with an honest ceiling:

- **Caveman MCP tools** — `caveman_retrieve` (pull moved bytes back), `caveman_toon_encode` / `caveman_toon_decode`, `caveman_stats`, and `caveman_compress`, because moved bytes are only safe with a recovery path.
- **caveman-browse** — a second MCP server, auto-registered when the `caveman-browse` binary and Chrome resolve and gracefully skipped when they don't (`caveman setup` says what installing it unlocks). It drives a local Chrome over a compressed a11y tree.
- **Command-output shrink** — the agent's shell output reroutes through `caveman shrink`. Hard rewrite via a real hook/plugin on **Claude, opencode, Gemini, Hermes, and OpenClaw**; **Codex** gets an honest soft note in `AGENTS.md` because its runtime rejects the rewrite ([openai/codex#18491](https://github.com/openai/codex/issues/18491)).
- **[The skill compressor](#the-skill-compressor)** — skill installs through the CLI auto-pixel their `SKILL.md` when the pages beat the text.

Turn pieces off in `~/.caveman-cloud/config.json` (wrap section).

## Build natively with `@caveman-ai/agent`

```bash
npm create @caveman-ai/agent@latest my-agent
```

[`@caveman-ai/agent`](./packages/agent) is the native TypeScript runtime for Caveman Engine. Use it when starting a new agent or when Caveman should own the loop, tools, subagents, conversations, provider selection, usage accounting, sandbox behavior, and transform execution.

Provider-wire integrations work independently of `@caveman-ai/agent`. Cave Build adapters add stricter plan, Context IR, usage, and recovery checks; those adapters require exact peer versions. Current locked Vercel adapter targets `ToolLoopAgent` in AI SDK `7.0.43`.

Context IR gives the engine enough information to optimize each context segment safely. It records semantic kind, build/session/turn stability, safety class, priority, recovery policy, cache region, privacy class, provenance digest, token count, and body handle.

Current source includes:

- model and reasoning configuration over an exact-pinned Pi runtime, plus an unlocked Claude Agent SDK lane;
- typed tools with declared side effects, sandbox profiles, local memory, output contracts, and bounded subagent wallets;
- provider-reported usage, context bills, catalog-price spend guards, and run-level model/tool limits that include descendants;
- immutable source snapshots, cache-epoch checks, byte-exact CCR recovery proof, and eval-gated Cave Builds;
- locked adapters for Vercel AI SDK, Eve, and Mastra when exact peer versions and evidence contracts pass;
- [`@caveman-ai/create-agent`](./packages/create-caveman-agent), the zero-runtime-dependency project initializer.

`auto()` is a configuration fallback. It resolves `CAVE_MODEL`, `.caveman/provider.json`, or the baseline model for the sole supported provider credential. It never classifies tasks or routes between models.

The runtime needs no Caveman account. Without local Engine, runs use explicit `observe-only`: direct provider traffic, no transforms, no Caveman gateway telemetry. With Engine running, eligible context can use recoverable local compression. Local results remain `inferred`; the runtime never turns a benchmark, estimate, or local run into verified savings.

Published packages:

```bash
npm create @caveman-ai/agent@latest my-agent
npm install @caveman-ai/agent
npm install @caveman-ai/sdk
python -m pip install caveman-sdk
```

No savings percentage is published for `@caveman-ai/agent`. Current evidence is too small and unstable; verified savings stay `$0`.

## The whole cave

One idea. **Agent do more with less.**

| Repo | What it shrinks |
|------|------|
| [**caveman**](https://github.com/JuliusBrussee/caveman) *(you here)* | What the agent **says**, and now what it **reads** |
| [**@caveman-ai/agent**](./packages/agent) *(also here)* | What your production agent **loads, calls, and spends** |
| [**caveman-code**](https://github.com/JuliusBrussee/caveman-code) | The **whole agent**, end to end |
| [**cavemem**](https://github.com/JuliusBrussee/cavemem) | What the agent **remembers**, across sessions |
| [**cavekit**](https://github.com/JuliusBrussee/cavekit) | The **build loop**, spec-driven |
| [**cavegemma**](https://github.com/JuliusBrussee/finetune-caveman) | The compression **baked into weights** (Gemma fine-tune) |

## From `inferred` to `verified`

**Caveman make token small. Caveman Cloud make it _provable_.**

Local runtime results report `inferred`; controlled benchmark results report `benchmark_counterfactual`. Neither is a provider invoice or production savings claim. Caveman Cloud is where qualifying live evidence can become `verified`. Set a baseline in record mode, try changes behind eval gates, roll back on quality loss, show savings from real traffic with signed receipts. Offline caveman never says `verified`.

[**Join the waitlist → caveman.so**](https://caveman.so)

## Privacy

Your agent still talks to the provider you chose. Local compression needs no Caveman account. The `caveman` CLI sends anonymous usage stats by default — command counts only, never your prompts, code, or file paths. It says so on first run, and one command turns it off forever: `caveman telemetry off` (or `DO_NOT_TRACK=1`). Authenticated dashboard sync runs only with connected credentials. Skill and hooks run locally; local proxy forwards provider traffic; CCR stays in a SQLite file on your disk. Agent SDK `observe-only` mode sends no Caveman gateway telemetry. Exact network, telemetry, storage, and managed-gateway boundaries live in [SECURITY.md](./SECURITY.md).

## License

Split license. The skill half is [MIT](./LICENSE) and stays that way. The engine half is BSL-1.1, more open than the three letters suggest.

**MIT** — the skill, Agent SDK and initializer, the CLI, both client SDKs (TS + Python), kit, evals/graders, contracts, provider catalog, the extension shell, and the thin cavemem clients.

**BSL-1.1** — the engine, proxy, MCP server, `shrink`, the cavemem Go core, and the shared Go platform. Source-available: read it, fork it, self-host it for your own first-party traffic free, production included. Every BSL version auto-converts to **Apache-2.0** on the earlier of `2030-06-21` or four years after that version first ships. The grant withholds one thing: reselling the engine to third parties as a hosted, managed, or embedded service, which needs a commercial license. The BSL text and per-directory map ship with the engine source.

`engine/pixel` embeds [pxpipe](https://github.com/teamchong/pxpipe) (MIT) plus glyph atlases derived from Spleen 5×8 (BSD-2-Clause) and GNU Unifont (dual OFL-1.1 / GPLv2-with-font-exception); its `NOTICE` travels with that source.

"Caveman" and the rock logo are trademarks of Julius Brussee. "Powered by Caveman" is fine when true.

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

[![Star History Chart](./docs/assets/star-history.png)](https://star-history.com/#JuliusBrussee/caveman&Date)

---

<sub>
<strong>Docs:</strong>
<a href="./INSTALL.md">Install matrix</a> ·
<a href="./docs/HONEST-NUMBERS.md">Honest numbers</a> ·
<a href="./LICENSE">License</a> ·
<a href="./CONTRIBUTING.md">Contributing</a> ·
<a href="./CLAUDE.md">Maintainer guide</a> ·
<a href="https://github.com/JuliusBrussee/caveman/issues">Issues</a>
<br>
MIT skill · BSL-1.1 engine — few token. no lie.
</sub>
