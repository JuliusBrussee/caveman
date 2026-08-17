# Install and update

Caveman ships a response-skill installer and a separate local-runtime CLI.
Choose either one or install both.

## Requirements

| Surface | Minimum runtime |
|---|---|
| Skill installer | Node.js 18 |
| `@caveman-ai/cli` | Node.js 22.13 |
| `@caveman-ai/agent` | Node.js 22.19 |
| Source build | Go version in [`go.mod`](../../go.mod), Node.js, and pnpm |
| Browser extension development | Node.js plus pinned Playwright dependencies |

Prebuilt companion binaries cover macOS, Linux, and Windows on amd64 and arm64.
Run `caveman setup` to inspect current machine support.

## Skill-only install

Run installer directly from GitHub:

```bash
npx -y github:JuliusBrussee/caveman
```

It detects installed agents and installs only matching integrations. List every
known target:

```bash
npx -y github:JuliusBrussee/caveman -- --list
```

Install one target:

```bash
npx -y github:JuliusBrussee/caveman -- --only claude
npx -y github:JuliusBrussee/caveman -- --only codex
```

Claude Code uses its plugin system, while Gemini CLI uses its extension system.
opencode, OpenClaw, and Hermes have native installers; skills-compatible hosts
use external Skills CLI, and targets labeled `soft` require `--only` because
local detection is unreliable.

Useful installer controls:

| Flag | Effect |
|---|---|
| `--dry-run` | Print commands and writes without changing state |
| `--force` | Reinstall an existing target |
| `--minimal` | Install plugin or extension without hooks or project rules |
| `--with-hooks` / `--no-hooks` | Control Claude Code hooks |
| `--with-init` | Write current-project rule files |
| `--with-mcp-shrink="<command>"` | Wrap one upstream MCP server |
| `--non-interactive` | Disable prompts |
| `--config-dir <path>` | Select Claude Code hook/config directory |

Full command reference lives in [`INSTALL.md`](../../INSTALL.md).

## Local runtime install

```bash
npm install -g @caveman-ai/cli
caveman setup --install
```

`setup --install` downloads the release checksum manifest and signature. It
verifies the pinned public key, checks each artifact SHA-256, and installs by
atomic rename under `~/.caveman/bin`. A failed verification leaves the
candidate binary uninstalled.

Inspect result:

```bash
caveman setup
caveman setup --json
```

Required binaries power proxy, Engine, MCP recovery, and memory. Browser and
tool-catalog shrink binaries are optional.

## First run

```bash
caveman claude
```

The command is shorthand for `caveman wrap claude`. An interactive first run
can install missing signed binaries and continue. Non-interactive runs never
silently download a runtime; prepare them with `caveman setup --install`.

Check direct pass-through before enabling transforms:

```bash
caveman wrap --off claude
caveman status
```

Then use default recoverable compression:

```bash
caveman claude
```

No Caveman account is required for either local mode.

## Build from source

Clone repository and run platform installer:

```bash
git clone https://github.com/JuliusBrussee/caveman.git
cd caveman
./scripts/install-local-cli.sh
```

Windows PowerShell:

```powershell
git clone https://github.com/JuliusBrussee/caveman.git
Set-Location caveman
pwsh -File scripts/install-local-cli.ps1
```

Source build uses versions pinned by repository manifests. Do not mix an old
CLI with newer companion binaries; `caveman setup` reports version drift.

## Existing reviewed binaries

Every companion accepts an environment override:

```bash
export CAVEMAN_PROXY_BIN=/reviewed/path/caveman-proxy
export CAVEMAN_ENGINE_BIN=/reviewed/path/caveman-engine
export CAVEMAN_MCP_BIN=/reviewed/path/caveman-mcp
export CAVEMAN_BROWSE_BIN=/reviewed/path/caveman-browse
export CAVEMAN_SHRINK_BIN=/reviewed/path/caveman-shrink
```

This bypasses download lookup and is useful for source builds or controlled
deployment images.

## Update

Update JavaScript CLI through npm, then repair binary bundle:

```bash
npm install -g @caveman-ai/cli@latest
caveman setup --install
caveman setup
```

Skill/plugin updates follow host's plugin or Skills CLI update flow. Re-running
GitHub installer is idempotent for installer-owned files.

## Uninstall

Remove installer-managed skill integrations:

```bash
npx -y github:JuliusBrussee/caveman -- --uninstall
```

Remove global CLI through npm:

```bash
npm uninstall -g @caveman-ai/cli
```

These commands do not delete local usage, CCR, or account state. Review
[`SECURITY.md`](../../SECURITY.md#local-storage) before deleting
`~/.caveman` or `~/.caveman-cloud`.

## Windows notes

PowerShell 5.1+ can run root installer shim. Skills CLI may need
`--copy` when symlink creation is unavailable. Native Windows supports local
runtime binaries; Agent SDK tools requiring OS-level network isolation should
run in WSL2. See [Windows fallback](../install-windows.md) for manual plugin
recovery.
