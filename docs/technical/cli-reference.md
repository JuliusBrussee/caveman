# CLI reference

`caveman` and `cave` invoke the same command-line program. The short alias is
useful in terminals; scripts should prefer `caveman` because its meaning is
clearer to readers. The npm CLI requires Node.js 22.13 or newer.

Run `caveman help <command>` for installed-version help. This page explains the
command groups and important behavior; command help remains the exact source
for accepted flags.

## Main commands

| Command | Purpose | Account needed |
|---|---|---:|
| `caveman <agent>` | Persistently enable native integration, then run supported agent | No |
| `caveman wrap <agent>` | Run one ephemeral wrapped session without persistent host config | No |
| `caveman run -- <command>` | Run an arbitrary command through the local layer | No |
| `caveman learn` | Rank locally observed improvements | No |
| `caveman status` | Show which modules are on, which agents they reach, and one next step | No |
| `caveman on <module>` / `caveman off <module>` | Turn a module on or off, after showing what will change | No |
| `caveman doctor` | Check this machine: one problem per line, with its fix. Signed in, it also checks Cloud | No |
| `caveman stop` | Stop the local runtime that `caveman start` or `caveman <agent>` started | No |
| `caveman login` | Connect the installation to Caveman Cloud | Yes |
| `caveman tools` | Open the local tool namespace | No |
| `caveman cloud` | Open the connected-service namespace | Yes |

Supported agent shortcuts are `aider`, `claude`, `codex`, `gemini`, `hermes`,
`kilo` (`kilocode` alias), `openclaw`, `opencode`, `pi`, and `qwen`.

```bash
caveman claude
caveman kilo run "review this repository"
caveman qwen -p "review this repository"
caveman codex --full-auto
caveman run -- my-agent --project .
```

Arguments after an agent name are passed to that agent. Arguments after `--`
in `caveman run` are passed to the selected command.

The first `caveman claude` runs setup (what it does, which modules, sign-in)
before anything is written. Once setup has chosen Claude Code, later runs keep
the same native wiring as `caveman enable claude` (`~/.claude/settings.json`,
or `$CLAUDE_CONFIG_DIR`) in place without asking, so the Claude Code IDE
extension and desktop app get the same routing and Auto model as the terminal.
`caveman disable claude` removes it and sticks until `caveman enable claude`.
A leading Caveman flag (`caveman claude --off …`) keeps a run session-only.

Auto is offered only while you are signed in with routing on, and only for a
provider the local runtime sends to its own API. Claude Code set to Bedrock,
Vertex or Foundry (`CLAUDE_CODE_USE_*`) gets no Auto; if you turn one of those
on after Auto was added, pick another model with `/model`.
In Claude Code, Auto has a 1M context window (its id is `caveman-auto[1m]`),
the window of the Claude models it runs on. In OpenCode it is 1M on Anthropic
and 872K on OpenAI; in Codex it is the largest window the ChatGPT model list
gives `gpt-6.1-sol` (872K, where that model's own default is 272K). On an
OpenAI API key, input past 272K tokens is billed at twice the rate.
A saved Codex `model = "caveman-auto"` (top level or under `[profiles.*]`) only
works while Caveman is wired; `caveman disable codex` clears the top-level one,
a profile's is yours to change.

## Modules

Caveman is six modules, each on unless you turn it off:

| Module | What it does |
|---|---|
| `output` | the agent says less |
| `input` | logs, JSON, code and diffs shrink before the model reads them |
| `waste-fixes` | finds your agent's worst waste and fixes it. Also keeps Anthropic's prompt cache warm while your agent pauses: the local proxy re-sends the last request with zero output shortly before the cache expires, only when your own past sessions say the agent is likely back in time for it to pay, for up to an hour |
| `routing` | adds Auto to your agent's model picker while signed in (Claude Code, OpenCode, Codex on a ChatGPT login): on Auto, the right model and effort each turn; any other model runs as picked. Needs a free account. Each request on Auto sends your latest ask (with whatever your agent attaches to it, such as CLAUDE.md, memory or @-mentioned files), the one before it, the end of the agent's last reply and request facts (tool names, effort settings, agent headers, the previous request's token counts) to Caveman Cloud; on the Free plan Caveman may keep it to improve routing |
| `scripts` | reusable scripts your agent keeps (installs `caveman-blocks`) |
| `browse` | compressed pages for browser tools |

```bash
caveman off browse        # shows the plan, asks once, then applies
caveman on input --yes    # no question
caveman on --all --dry-run
```

`on` and `off` change only the modules you name and what those need. Agents
stay wired while any of `output`, `input`, `waste-fixes` or `routing` is on.
Without a terminal, pass `--yes` or nothing changes. Choices live in
`modules` inside the same config file as `caveman tools config`.

## Local tool namespace

`caveman tools` groups less frequent local commands by job.

| Group | Commands |
|---|---|
| Think | `compress`, `shrink`, `toon`, `convert` |
| Remember | `mem`, `retrieve` |
| Execute | `mcp`, `hooks`, `browse`, `skills`, `sdk` |
| Inspect | `stats`, `trial`, `evals`, `config` |

### `compress`

Compress text from standard input or a file. Compression is local. Lossy output
contains a recovery handle when a recovery store is available.

```bash
caveman tools compress < long-context.txt
caveman tools retrieve ccr_0123456789abcdef0123456789abcdef
```

Use `caveman start` when an application needs an HTTP proxy instead of a
one-shot command.

### `shrink`

Reduce large command or tool output while preserving selected structure and a
recovery reference. Input larger than the command limit is rejected rather
than partially processed.

### `toon`

Encode or decode structured data using TOON. Encoding only wins on suitable
data, commonly uniform arrays of objects. It is not a byte-preserving
transformation.

### `convert`

Pack supported installed skills into pixel form for selected agent or project;
flags include `--agent`, `--project`, `--skill`, `--dir`, `--density`,
`--dry-run`, `--revert`, and `--force`. Run a dry run before changing a large
skill tree.

### `mem`

Manage local durable memory. Available operations include `remember`, `recall`,
`supersede`, `history`, and `forget`. See [Local tools](local-tools.md).

### `mcp`

Install or remove local Model Context Protocol registrations for detected
agents. Server choices include recovery, browser, connected read-only evidence,
and opt-in delegation tools.

```bash
caveman tools mcp install claude --server caveman
caveman tools mcp uninstall claude --server caveman
caveman tools mcp install qwen --server caveman
caveman tools mcp uninstall qwen --server caveman
```

Qwen registration is written to `~/.qwen/settings.json`. Caveman preserves
sibling settings and refuses to replace or remove an entry that is not recorded
as Caveman-owned.

`caveman-mcp` binary itself serves compression, recovery, statistics, and TOON
tools over standard input and output.

### `hooks`

Install or inspect supported agent hooks. Hooks add local context and compact
output behavior to agent-native event systems. See
[Skills, hooks, and plugins](skills-hooks-and-plugins.md).

### `browse`

Control a Chrome session through the local browser bridge.

```bash
caveman tools browse https://example.com "main article"
caveman tools browse act '<element-reference>' click
caveman tools browse eval 'document.title'
caveman tools browse recover '<recovery-handle>'
caveman tools browse close
```

Browser evaluation executes JavaScript in the attached page. Treat expressions
as code, and use them only on pages you trust.

### `skills`

List, preview, install, add, or import skills. Preview an external skill before
installation.

```bash
caveman tools skills list
caveman tools skills preview owner/repository
caveman tools skills install owner/repository
```

### `sdk`

Print or apply integration recipes for supported SDKs and agent frameworks.
Recipes cover Anthropic, OpenAI, Google Gen AI, Vercel AI SDK, LangChain,
LiteLLM, CrewAI, Pydantic AI, OpenAI Agents SDK, and direct `curl` use.

### `stats`, `trial`, and `evals`

- `stats` reports local compression and usage records.
- `trial` exercises local features against fixtures or configured providers.
- `evals` runs evaluation-related commands available in the installed build.

Local observations are not verified savings. See
[Accounting and evidence](accounting-and-evidence.md).

### `config`

Read or change local feature configuration.

```bash
caveman tools config path
caveman tools config get think.mode
caveman tools config set think.mode compress
```

See [Configuration](configuration.md) for keys, values and precedence.

## Runtime commands

### `start`

Start the loopback HTTP proxy. Default address is `127.0.0.1:8787`.

```bash
caveman start
```

The proxy uses `~/.caveman/caveman.yaml` unless `CAVEMAN_CONFIG` names another
file. It stores local operational data in `~/.caveman/caveman.db`.

### `setup`

Inspect or install local runtime components.

```bash
caveman setup
caveman setup --install
caveman setup --json
caveman setup --agent-native claude
caveman setup --agent-native codex
```

Add `--remove` to an `--agent-native` command to remove that integration.

### `wrap`

`wrap` is the explicit form used by agent shortcuts.

```bash
caveman wrap claude
caveman wrap --off codex
caveman wrap --pixel gemini
caveman wrap --workflow review opencode
caveman wrap qwen
```

- Default mode enables supported local compression, structured-data encoding,
  recovery, and output shrinking.
- `--off` runs byte-safe pass-through recording.
- `--pixel` enables lossy text-to-image context transport for models listed in
  configuration.
- `--workflow` selects a named workflow when the installed profile supports it.

Record mode never changes model-visible request bytes.

## Learning commands

`caveman learn` reads local observations and ranks possible improvements.

```bash
caveman learn
caveman learn --since 7d --sources claude,codex,gemini,opencode,aider
caveman learn --all
caveman learn --repo my-project
caveman learn --json
caveman learn implement codex --prompt "focus on config fixes"
caveman learn apply claude_md_weight:project --dry-run
caveman learn simulate claude_md_weight:project recurring_context:repaste:<fingerprint>
caveman learn applied claude_md_weight:project --fix-kind claude_md_weight --note "approved and re-measured"
```

Output formats include plain text, JSON, and Markdown. `--all` adds every sink,
confirmed outcomes, per-repository observations, and advanced command hints.
`--repo` filters sessions before analysis. `apply` prepares one candidate;
`simulate` totals counterfactual scale over scanned history; `applied` records a
completed, re-measured fix in Caveman's outcome store. A recommendation remains
an inferred opportunity until stronger evidence exists. Aider scanning remains
opt-in through `CAVEMAN_AIDER_ROOT` because its history is repository-local.
Full reference: [caveman learn](./learn.md).

## Connected namespace

`caveman cloud` is the account and network namespace. Its command groups cover:

- account: `whoami`, `projects`, `keys`, `providers`, `billing`;
- evidence: `score`, `costs`, `plan`, `traces`, `experiments`, `receipts`;
- governance: `audit`, `sync`, `agent`.

These commands require a connected installation and may depend on plan or
organization policy. This repository documents client behavior and public
contracts, not hosted implementation details.

## Exit and failure behavior

Commands use nonzero exit status for invalid arguments, rejected configuration,
missing dependencies, and failed operations. Destructive or ambiguous local
transformations should use `--dry-run` where offered. Compression paths prefer
original input over unsafe or incomplete output.
