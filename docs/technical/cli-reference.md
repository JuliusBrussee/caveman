# CLI reference

`caveman` and `cave` invoke the same command-line program. The short alias is
useful in terminals; scripts should prefer `caveman` because its meaning is
clearer to readers.

Run `caveman help <command>` for installed-version help. This page explains the
command groups and important behavior; command help remains the exact source
for accepted flags.

## Main commands

| Command | Purpose | Account needed |
|---|---|---:|
| `caveman <agent>` | Run a supported agent through the local layer | No |
| `caveman run -- <command>` | Run an arbitrary command through the local layer | No |
| `caveman learn` | Rank locally observed improvements | No |
| `caveman status` | Show local runtime, mode, and connection state | No |
| `caveman login` | Connect the installation to Caveman Cloud | Yes |
| `caveman tools` | Open the local tool namespace | No |
| `caveman cloud` | Open the connected-service namespace | Yes |

Supported agent shortcuts are `aider`, `claude`, `codex`, `gemini`, `hermes`,
`openclaw`, and `opencode`.

```bash
caveman claude
caveman codex --full-auto
caveman run -- my-agent --project .
```

Arguments after an agent name are passed to that agent. Arguments after `--`
in `caveman run` are passed to the selected command.

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
```

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
caveman learn --since 7d --sources proxy,agent
caveman learn --json
caveman learn implement <finding-id>
caveman learn apply <report-path>
```

Output formats include plain text, JSON and Markdown. A recommendation remains
an inferred opportunity until stronger evidence exists.

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
