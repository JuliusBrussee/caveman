# Install caveman

One install. Works for every AI coding agent on your machine.

If just want it to work, run the one-liner. If want to know what gets touched, scroll down.

## One-liner

**macOS / Linux / WSL / Git Bash**

```bash
curl -fsSL https://raw.githubusercontent.com/JuliusBrussee/caveman/v3.2.0/install.sh | bash
```

**Windows (PowerShell 5.1+)**

```powershell
irm https://raw.githubusercontent.com/JuliusBrussee/caveman/v3.2.0/install.ps1 | iex
```

> Piping a script straight into a shell runs it sight-unseen. If you'd rather read it first, download then run: `curl -fsSL https://raw.githubusercontent.com/JuliusBrussee/caveman/v3.2.0/install.sh -o install.sh` (review it) `&& bash install.sh`. Bootstrap, package, skill, and hook downloads stay pinned to that release tag, never the moving `main` branch. Hook files are checked against a SHA-256 list from the same tag: that catches a broken or partial download, not a tag that was moved. If that list can't be fetched or any file fails it, no hook is installed and your settings stay as they were. Runtime binaries are checked against a checksum list signed with a key built into the CLI. Set `CAVEMAN_REF` only when intentionally testing another ref.

What it does:

- Auto-detects every supported agent installed on your machine (Claude Code, Cursor, Codex, etc.).
- For each one, runs that agent's native install path (plugin / extension / rule file / `npx skills add`).
- Installs Cavecrew investigator, builder, and reviewer presets where the host supports native subagents.
- Wires Claude Code hooks and statusline badge on top, plus a Codex start-of-session hook so Codex talks caveman without asking. (`caveman-shrink` MCP middleware is opt-in via `--with-mcp-shrink` — see flag table below.)
- Skips anything you don't have. Safe to re-run. ~30 seconds end-to-end.

Want to preview before installing? Use `--dry-run`:

```bash
curl -fsSL https://raw.githubusercontent.com/JuliusBrussee/caveman/v3.2.0/install.sh | bash -s -- --dry-run
```

## Per-agent install

If you want to install for one agent (or want to know exactly what command runs under the hood), use the table below. Every row also works as `--only <id>` to the unified installer.

> **On npm 12 or newer, add `--allow-git=root` to any bare `npx -y github:...` command below.** npm 12 turns off git package fetches by default, so a plain `npx -y github:JuliusBrussee/caveman` stops with `npm error code EALLOWGIT`. The flag opts in just the one package you asked for: `npx --allow-git=root -y github:JuliusBrussee/caveman -- --only <id>`. Check with `npx --version`. `install.sh` and `install.ps1` detect the npm major and add the flag themselves starting with `v3.2.0`, the release the one-liners above are pinned to, so those work on npm 12. **One-liners pinned to `v3.1.0` or older download shims that predate that change and keep failing on npm 12**: switch the tag to `v3.2.0`, or install with the flag directly: `npx --allow-git=root -y github:JuliusBrussee/caveman#v3.2.0 -- --only <id>`.

> **Choose the install scope your agent reads.** `-g` installs into the agent's user skill directory. Without it, skills belong to the current project. The unified installer uses user scope except for Replit, whose documented filesystem location is the project's `.agents/skills`. Run Replit's command from that project's Shell. Replit workspace-wide skills are managed in Workspace Settings.

| Agent | Install command | Auto-activates? |
|---|---|:-:|
| **Claude Code** | `claude plugin marketplace add JuliusBrussee/caveman && claude plugin install caveman@caveman` | Yes |
| **Gemini CLI** | `gemini extensions install https://github.com/JuliusBrussee/caveman` | Yes |
| **opencode** | `node installer/install.js --only opencode` *(or `npx -y github:JuliusBrussee/caveman -- --only opencode`)* | Yes (plugin + AGENTS.md) |
| **Oh My Pi (OMP)** | `npx -y github:JuliusBrussee/caveman -- --only omp` *(or `node installer/install.js --only omp` from a clone)* | Yes (native OMP plugin) |
| **OpenClaw** | `npx -y github:JuliusBrussee/caveman -- --only openclaw` | Yes (workspace skill + SOUL.md) |
| **Hermes Agent** | `npx -y github:JuliusBrussee/caveman -- --only hermes` *(or `node installer/install.js --only hermes` from a clone)* | Yes (native skills, enabled on load) |
| **Antigravity CLI** (`agy`) | `npx -y github:JuliusBrussee/caveman -- --only antigravity-cli` | Yes ([agy plugin](#antigravity-cli)) |
| **Codex CLI** | `npx -y github:JuliusBrussee/caveman -- --only codex` *(skills only, no hook: `npx skills add JuliusBrussee/caveman -a codex -g`)* | Yes (SessionStart hook — trust it once with `/hooks`); skills-only: `$caveman` (Codex calls skills with `$`, not `/`) |
| **Cursor** | `npx -y github:JuliusBrussee/caveman -- --only cursor` *(or the [Cursor plugin](#cursor))* | Yes (session hook) |
| **Windsurf** | `npx skills add JuliusBrussee/caveman -a windsurf -g` | Per-session by default; `--with-init` for an always-on rule file |
| **Cline** | `npx skills add JuliusBrussee/caveman -a cline -g` | Per-session by default; `--with-init` for an always-on rule file |
| **GitHub Copilot** | `npx -y github:JuliusBrussee/caveman -- --only copilot --with-init` | Copilot CLI: Yes (session hook). VS Code: repo-wide instructions via `--with-init` |
| **Continue** | `npx -y github:JuliusBrussee/caveman -- --only continue` | No — invoke the Caveman skill |
| **Kilo Code** | `npx skills add JuliusBrussee/caveman -a kilo -g` | No |
| **Roo Code** | `npx skills add JuliusBrussee/caveman -a roo -g` | No |
| **Augment Code** | `npx skills add JuliusBrussee/caveman -a augment -g` | No |
| **AiderDesk** | `npx -y github:JuliusBrussee/caveman -- --only aider-desk` | No — enable Skills Tools |
| **Sourcegraph Amp** | `npx skills add JuliusBrussee/caveman -a amp -g` | No |
| **IBM Bob** | `npx skills add JuliusBrussee/caveman -a bob -g` | No |
| **CodeBuddy Code** | `npx skills add JuliusBrussee/caveman -a codebuddy -g` | No |
| **Crush** | `npx -y github:JuliusBrussee/caveman -- --only crush` | No |
| **Devin (terminal)** | `npx skills add JuliusBrussee/caveman -a devin -g` | No |
| **Droid (Factory)** | `npx skills add JuliusBrussee/caveman -a droid -g` | No |
| **ForgeCode** | `npx skills add JuliusBrussee/caveman -a forgecode -g` | No |
| **Block Goose** | `npx skills add JuliusBrussee/caveman -a goose -g` | No |
| **Grok Build** | `npx -y github:JuliusBrussee/caveman -- --only grok` | Yes, per xAI docs (`~/.grok/AGENTS.md` block; not yet tested live) |
| **iFlow CLI** | `npx -y github:JuliusBrussee/caveman -- --only iflow` | No |
| **Kiro (IDE + CLI)** | `npx skills add JuliusBrussee/caveman -a kiro-cli -g` | No ([always-on steering](#kiro-ide-and-cli)) |
| **Mistral Vibe** | `npx skills add JuliusBrussee/caveman -a mistral-vibe -g` | No |
| **OpenHands** | `npx skills add JuliusBrussee/caveman -a openhands -g` | No |
| **Qwen Code** | `npx skills add JuliusBrussee/caveman -a qwen-code -g` | No |
| **Atlassian Rovo Dev** | `npx skills add JuliusBrussee/caveman -a rovodev -g` | No |
| **Tabnine CLI** | `npx skills add JuliusBrussee/caveman -a tabnine-cli -g` | No |
| **Trae** | `npx skills add JuliusBrussee/caveman -a trae -g` | No |
| **Warp** | `npx skills add JuliusBrussee/caveman -a warp -g` | No |
| **Replit Agent** | From the project Shell: `npx skills add JuliusBrussee/caveman -a replit` | No |
| **JetBrains Junie** *(soft probe)* | `npx skills add JuliusBrussee/caveman -a junie -g` | No |
| **Qoder** *(soft probe)* | `npx skills add JuliusBrussee/caveman -a qoder -g` | No |
| **Antigravity IDE** *(soft probe)* | `npx -y github:JuliusBrussee/caveman -- --only antigravity` | No |
| **Antigravity 2.0** *(explicit selection)* | `npx -y github:JuliusBrussee/caveman -- --only antigravity-2` | No |

Not in the table, with their own steps below: [Claude apps (claude.ai, desktop, mobile, Cowork)](#claude-apps-claudeai-desktop-mobile-cowork), [GitHub Copilot in VS Code](#github-copilot-in-vs-code-agent-plugin), [nanocoder](#nanocoder), and [dev containers](#dev-containers-codespaces-remote-machines).

Already inside a Claude Code session? The same Claude Code install as slash commands:

```text
/plugin marketplace add JuliusBrussee/caveman
/plugin install caveman@caveman
```

"Soft probe" = installer won't auto-detect these without `--only <id>` because there's no reliable always-on signal (no CLI / config-dir-only). Pass the flag when you want them.

For "auto-activates? No" agents, invoke the Caveman skill using the host's skill menu, `/caveman` where supported (`$caveman` in Codex), or a prompt naming the skill. Enable skills first if your host requires it: Augment has a Skills beta setting; AiderDesk requires Skills Tools in the active agent profile; custom Kiro agents need skill resources.

**Pick the mode new sessions start in.** Put `{"defaultMode": "ultracave"}` (or `"caveman"`, `"megacave"`, `"off"`) in `~/.config/caveman/config.json`, or in a `.caveman.json` at a project root for just that project.

Scripted Claude Code runs (`claude -p` and the Agent SDK) start with caveman off whatever that file says, so tools that read Claude's reply get plain text. Type `/caveman` in the prompt, or set `CAVEMAN_DEFAULT_MODE=caveman` (or another mode) in their environment, to turn it on.

**Subagents.** Some agents hand parts of a job to helper agents ("subagents"). Whether the helpers talk caveman depends on the host:

- **Claude Code**: yes. Each subagent starts in the mode of the window that spawned it. Say "stop caveman" and new subagents start normal too. Cavecrew agents keep their own caveman voice.
- **opencode**: subagents get the always-on caveman rules from `AGENTS.md` (checked on opencode 2.0.22). Those rules are fixed text, so "stop caveman" does not switch them off for subagents.
- **Hermes Agent**: not verified. If a delegated task comes back wordy, ask for the caveman skill in that task.

Continue needs physical skill directories because its current loader skips per-skill symlinks. The unified installer copies into `CONTINUE_GLOBAL_DIR/skills` (default `~/.continue/skills`) and follows AiderDesk's `AIDER_DESK_HOME_DIR` / `AIDER_DESK_DIR` overrides. It also honors `IFLOW_HOME`, Crush's exact `CRUSH_SKILLS_DIR`, and `GROK_HOME` (Grok Build reads `GROK_HOME/skills`, default `~/.grok/skills`). For Grok Build it also adds a marker-fenced caveman ruleset block to `GROK_HOME/AGENTS.md`, the global rules file Grok loads every session according to xAI's docs (we have not tested this against a real Grok binary yet); your own text in that file stays. Use the same environment when uninstalling — for a relative override, that means the same working directory too, since the path resolves against `cwd`. Existing unowned skill directories or symlinks produce a conflict rather than being silently replaced. See the [vendor discovery matrix](docs/technical/installer-provider-discovery.md) for sources and product limits.

Antigravity IDE reads `~/.gemini/antigravity/skills`; Antigravity 2.0 reads `~/.gemini/config/skills`. Select the matching product. Each command copies only into that product's directory.

**Finding a profile slug for `npx skills add ... -a <profile>`?** Either read the table above, or print the live matrix from the installer:

```bash
# Either of these works (install.sh / install.ps1 are thin shims that
# forward all flags to installer/install.js):
bash install.sh --list             # macOS / Linux / WSL, from a local clone
pwsh install.ps1 --list            # Windows / PowerShell, from a local clone
node installer/install.js --list         # any platform, from a local clone
npx -y github:JuliusBrussee/caveman -- --list   # no clone needed
```

Each row prints the agent id, profile slug (where applicable), and whether it was auto-detected on your machine. Full agent matrix (with detection rules) is also defined in `installer/install.js` under the `PROVIDERS` array.

### Codex

With `codex` on your PATH, `npx -y github:JuliusBrussee/caveman -- --only codex`
installs the skills and a small start-of-session hook. Every new Codex session
(and every `/clear` or context compaction) then starts in caveman, following
your configured default mode — including `off`. Codex asks you to review new hooks: run `/hooks` once in
Codex and trust the caveman one. Hooks are on by default in current Codex; if
`codex features list` shows `hooks` off, add `[features] hooks = true` to
`~/.codex/config.toml`.

The hook files live in `$CODEX_HOME/caveman/` (default `~/.codex/caveman/`) and
one entry is added to `$CODEX_HOME/hooks.json`. Re-running never duplicates it.
`--no-hooks` keeps the skills-only, per-session behavior: type `$caveman`.
`--uninstall` removes only caveman's entry and files. Use the same `CODEX_HOME`
for install and uninstall.

### Oh My Pi (OMP)

With `omp` on your PATH, run `node installer/install.js --only omp` from this clone,
then restart OMP. The native plugin adds nine skills, eight commands, Cavecrew
presets, a CAVEMAN badge, and Caveman instructions on each agent turn. Commands
such as `/ultracave` and `stop caveman` instruct the model; the badge indicates
that the plugin is loaded. Host lifecycle and prompt delivery were checked with
OMP 18.2.6. This integration does not read Claude Code session statistics.

The installer keeps its package at `~/.omp/caveman-plugin/` and asks OMP to
register it in OMP's own configured plugin directory. Use the same OMP environment
when uninstalling. A plugin already named `caveman` at another location is a
conflict: resolve it through OMP first, even when using `--force`.

Untracked or edited package files are preserved. To replace them intentionally,
use `--only omp --force`; the ownership journal records a backup for restoration
on uninstall. Failed registration retains the owned package, journal, and backups
so OMP cannot be left pointing at deleted files. Fix the reported host error and
rerun the install, or uninstall. Failed deregistration retains those files too.

### Cursor

Two ways in. Pick one: with both, every chat gets the rules twice.

- **Installer.** `npx -y github:JuliusBrussee/caveman -- --only cursor` adds
  the skills, the Cavecrew agents (`~/.cursor/agents/`) and a session hook
  (`~/.cursor/hooks.json` plus `~/.cursor/caveman/`). The Cursor editor, the
  Agents Window and `cursor-agent` all read these. `--no-hooks` installs the
  agents without the hook; it does not remove a hook an earlier install
  added (`--uninstall` does). `--uninstall` removes only caveman's entry from
  `hooks.json`; your other hooks stay.
- **Plugin.** This repo is a Cursor plugin (`.cursor-plugin/plugin.json`: the
  skills, the same session hook, and the Cavecrew agents as-is from `agents/`).
  Cursor skips their Claude-only model pin and runs them on your chat model;
  the installer's copies also mark two of them read-only. Clone it into
  `~/.cursor/plugins/local/caveman`, then run **Developer: Reload Window**. For
  the CLI: `cursor-agent --plugin-dir ~/.cursor/plugins/local/caveman`.

Either way, each new chat starts in your configured default mode
(`CAVEMAN_DEFAULT_MODE`, a repo `.caveman.json`, or your user config);
`"defaultMode": "off"` keeps chats normal. Checked with `cursor-agent`
2026.09.18. Cursor does not read rule files from `~/.cursor/rules/` (user rules
live in Settings), so the per-repo `.cursor/rules/caveman.mdc` from
`--with-init` stays the rule-file option. Not checked: the Cursor editor's
import of Claude Code hooks. If you also have caveman's Claude Code hooks and
see the rules twice, turn one of them off.

### GitHub Copilot CLI

`--only copilot` installs the skills for every Copilot surface. If the Copilot
CLI is on your machine (`copilot` on PATH, or `COPILOT_HOME` set), it also adds
a session hook so every new `copilot` session starts in caveman mode — no
`/caveman` needed. It follows your configured default (`CAVEMAN_DEFAULT_MODE`,
a repo `.caveman.json`, or your user config), so `"defaultMode": "off"` keeps
sessions normal. Checked with Copilot CLI 1.0.92.

Files, all owned by the installer and removed by `--uninstall`:
`$COPILOT_HOME/hooks/caveman.json` (default `~/.copilot/hooks/`) and
`$COPILOT_HOME/caveman/`. Other files in `hooks/` are never touched. Skip the
hook with `--no-hooks` (it skips adding one; `--uninstall` removes one already
there). VS Code reads the same `hooks/` folder, but its
documented session output is shaped differently and this path is untested
there: for VS Code Copilot Chat, use `--with-init` for always-on.

### Antigravity CLI

With `agy` on your PATH, `--only antigravity-cli` builds a small `caveman`
plugin (the skills plus one always-on rule) and installs it with
`agy plugin install`. Every new `agy` session then talks caveman. Checked with
agy 1.2.17.

The rule is fixed text, so `defaultMode` settings do not reach it. To go back
to normal prose, say `stop caveman` in a session, or switch the plugin off for
good with `agy plugin disable caveman` (`enable` turns it back on).
`--uninstall` runs `agy plugin uninstall caveman`. This is separate from the
**Antigravity IDE** and **Antigravity 2.0** rows, which copy skills only.

### Claude apps: claude.ai, desktop, mobile, Cowork

Nothing to install on your phone or inside Cowork. Add caveman to your Claude account once, from a browser or the desktop app, and it follows the account:

1. On [claude.ai](https://claude.ai) or in the Claude desktop app, open **Customize > Plugins**.
2. Pick **Add > Add marketplace** and enter `JuliusBrussee/caveman`.
3. Add **Caveman** from that marketplace.
4. Turn on **Settings > Capabilities > Code execution and file creation**. Claude's skills need it.

Then:

- **Chat** (web, desktop, Android, iOS): chat has no startup hook, so caveman waits until asked. Start a chat with `/caveman` (pick it from the `/` menu) or say "caveman mode". Want it in every chat? Paste the text of [`src/rules/caveman-activate.md`](src/rules/caveman-activate.md) into your personal preferences in Settings.
- **Cowork**: start a new task, then use `/caveman` or say "caveman mode".
- **Claude Code**: the account copy arrives the next time you start Claude Code signed in to that account. If you also installed caveman from the Claude Code command line, check `claude plugin list` and disable one copy, so the rules don't load twice.

Why a command-line install didn't show up: plugins installed from the Claude Code command line stay on that one machine and never reach your Claude account, so the apps and Cowork can't see them.

> Steps follow Claude's [plugin docs](https://claude.com/docs/plugins/overview). We have not yet checked them end to end on a real account: the marketplace add from this repo, the skill appearing on Android, and whether caveman's auto-start runs in Cowork. If a step differs for you, [open an issue](https://github.com/JuliusBrussee/caveman/issues).

### GitHub Copilot in VS Code (agent plugin)

VS Code can install plugins in Claude's format, and this repo is one.

1. Turn on agent plugins with the `chat.plugins.enabled` setting.
2. Open the Command Palette, run **Chat: Install Plugin From Source**, and paste `https://github.com/JuliusBrussee/caveman`.
3. In Copilot Chat, pick `caveman` from the `/` menu or say "caveman mode".

Expect skills only, and no auto-start: VS Code reads a Claude plugin's hooks from a file caveman does not ship. We have not tested this path yet. For caveman on every Copilot reply in one repo, run `npx -y github:JuliusBrussee/caveman -- --only copilot --with-init` at the repo root. It writes Copilot's rule file, `.github/copilot-instructions.md`, and also adds rule files for Cursor, Windsurf, Cline and `AGENTS.md`.

### Kiro (IDE and CLI)

`npx skills add JuliusBrussee/caveman -a kiro-cli -g` puts caveman in `~/.kiro/skills/`. Kiro IDE and kiro-cli both load skills from there, so one install covers both. Say "caveman mode" to start it.

Want it on for every Kiro chat? Add a global steering file with `inclusion: always`:

```bash
mkdir -p ~/.kiro/steering
printf '%s\n' '---' 'inclusion: always' '---' '' > ~/.kiro/steering/caveman.md
curl -fsSL https://raw.githubusercontent.com/JuliusBrussee/caveman/main/src/rules/caveman-activate.md >> ~/.kiro/steering/caveman.md
```

The steering format comes from [Kiro's docs](https://kiro.dev/docs/steering/); we have not tested it in Kiro ourselves. Delete the file to turn it off.

### nanocoder

No native install. nanocoder reads the `AGENTS.md` at your project root into its system prompt (its `nano` profile leaves it out unless you turn on **Include AGENTS.md**). Put caveman's always-on rule there, once per repo:

```bash
curl -fsSL https://raw.githubusercontent.com/JuliusBrussee/caveman/main/src/rules/caveman-activate.md >> AGENTS.md
```

Run it once; a second run adds a second copy. `npx -y github:JuliusBrussee/caveman -- --with-init` is safe to re-run and writes the same rule into `AGENTS.md`, but it also installs caveman into every other agent it finds on your machine and adds rule files for Cursor, Windsurf, Cline and Copilot. Not tested in nanocoder by us yet.

### Dev containers, Codespaces, remote machines

A dev container has its own home folder. Caveman installed on your laptop (`~/.codex/skills`, `~/.claude`, and so on) is not inside it. Two ways that work:

1. **Ship caveman with the repo.** At the repo root, run the skills install *without* `-g`, then commit the folder it creates. Every container and teammate gets caveman. For Codex:

   ```bash
   npx skills add JuliusBrussee/caveman -a codex --copy
   ```

   Codex reads project skills from `.agents/skills/`. Skill files copied anywhere else are not found.
2. **Install when the container is built.** In `.devcontainer/devcontainer.json` (the image needs Node.js):

   ```json
   "postCreateCommand": "npx -y skills add JuliusBrussee/caveman --skill '*' -a codex -g -y"
   ```

Swap `codex` for your agent's profile from the table above. Claude Code inside a container needs its own install too: run the Claude Code plugin commands, or the installer, inside the container.

## Manual install (no `curl | bash`)

If you'd rather see exactly what runs:

```bash
# Clone the repo
git clone https://github.com/JuliusBrussee/caveman.git
cd caveman

# Preview every command the installer would run
node installer/install.js --dry-run --all

# Inspect the agent matrix
node installer/install.js --list

# Install for everything detected
node installer/install.js --all
```

Useful flags:

| Flag | What |
|---|---|
| `--all` | Plugin + hooks + statusline + per-repo rule files in `$PWD`. (MCP shrink is opt-in — see `--with-mcp-shrink` below.) |
| `--minimal` | Plugin / extension only. No hooks, no MCP shrink, no per-repo rules. |
| `--only <id>` | One agent only. Repeatable: `--only claude --only cursor`. |
| `--dry-run` | Print every command. Write nothing. |
| `--with-init` | Drop always-on rule files into the current repo (`.cursor/`, `.windsurf/`, `.clinerules/`, `.github/copilot-instructions.md`, `.opencode/AGENTS.md`, `AGENTS.md`) and, if OpenClaw is on the box, append the bootstrap block to `~/.openclaw/workspace/SOUL.md`. |
| `--with-mcp-shrink="<upstream cmd>"` | Register `caveman-shrink` MCP proxy wrapping the given upstream MCP server. **Off by default.** A value is required — caveman-shrink is a proxy and exits immediately without one. Example: `--with-mcp-shrink="npx @modelcontextprotocol/server-filesystem /tmp"`. Within the value, single or double quotes group paths containing spaces; backslashes stay literal. A JSON array of strings also works when arguments contain quotes. No shell expansion occurs. |
| `--no-mcp-shrink` | Skip MCP-shrink registration. (Default.) |
| `--with-hooks` / `--no-hooks` | Force-on or force-off the Claude Code hook installer, the Codex SessionStart hook, the Cursor sessionStart hook and the Copilot CLI session hook. (Default: on.) |
| `--skip-skills` | Don't run the npx-skills auto-detect fallback when nothing else matched. |
| `--config-dir <path>` | Claude Code config dir for hook files + `settings.json`. **Does NOT scope** `claude plugin install`, `gemini extensions install`, Codex (`CODEX_HOME`), OMP (`~/.omp/`), opencode (`XDG_CONFIG_HOME`), or openclaw (`OPENCLAW_WORKSPACE`) — those use their own paths. Default: `$CLAUDE_CONFIG_DIR` or `~/.claude`. `~` is expanded. |
| `--non-interactive` | Never prompt; use defaults. (Auto when stdin is not a TTY.) |
| `--no-color` | Disable ANSI colors. |
| `--list` | Print full agent matrix and exit. |
| `--force` | Re-run even if already installed. |
| `--uninstall` | Remove everything. See below. |

For Windows paths containing spaces, pass a single quoted value from PowerShell:

```powershell
node installer/install.js --only opencode --with-mcp-shrink "'C:\Program Files\nodejs\node.exe' 'C:\MCP servers\server.js' 'C:\data folder\'"
```

The installer preserves each quoted path as one argument. For arguments containing quote characters, use a JSON array such as `--with-mcp-shrink='["node","server.js","path with spaces"]'`.

## Always-on rules

For agents without a hook system (Windsurf, Cline, Copilot in VS Code, and friends), the always-on path is a static rule file. Two ways:

```bash
# Drop rule files into the current repo
node installer/install.js --with-init

# Or pull the rule body straight in (manual)
curl -fsSL https://raw.githubusercontent.com/JuliusBrussee/caveman/main/src/rules/caveman-activate.md \
  > .cursor/rules/caveman.mdc   # or .windsurf/rules/caveman.md, .clinerules/caveman.md, .github/copilot-instructions.md
```

`--with-init` writes the rule into every supported per-agent location it can detect (`.cursor/rules/`, `.windsurf/rules/`, `.clinerules/`, `.github/copilot-instructions.md`, `.opencode/AGENTS.md`, `AGENTS.md`). It also installs the OpenClaw workspace bootstrap (skill folder + SOUL.md marker block) when `~/.openclaw/workspace/` exists. Single source: [`src/rules/caveman-activate.md`](src/rules/caveman-activate.md).

## Verify

After install, three quick checks:

**1. See what got installed.**

```bash
node installer/install.js --list
```

You should see ~30 rows. Detected agents are marked. Anything you wanted but isn't marked → not detected (likely the binary isn't on `PATH`).

**2. Talk to Claude Code.**

Open Claude Code, type `/caveman`. The reply should answer first, with no greeting or recap. Try a real question: "What is closures in JS?" — the answer should be short, articles optional, every technical term intact.

**3. Check the flag file.**

```bash
cat "${CLAUDE_CONFIG_DIR:-$HOME/.claude}/.caveman-active"
# expected output: caveman
```

If it's missing or empty, the SessionStart hook didn't fire. See troubleshooting below.

Each Claude Code window keeps its own mode in
`${CLAUDE_CONFIG_DIR:-$HOME/.claude}/.caveman-sessions/`, one small file per
session. The `.caveman-active` file above is a mirror of whichever window wrote
most recently — handy for a quick "is caveman on", but with several windows open
it shows one of them, not all. To see them all:

```bash
ls "${CLAUDE_CONFIG_DIR:-$HOME/.claude}/.caveman-sessions/"
```

A window where you said "stop caveman" stores `off` and stays off — including
across the automatic context compaction that happens in long sessions.

Statusline should show `[CAVEMAN]` (orange) at the bottom of Claude Code. `/caveman-stats` reports recorded usage; savings remain unknown without a measured comparison.

## Update

**Claude Code.** The plugin is `caveman@caveman` — plugin name, then the
marketplace it came from. Both are called `caveman`, so the short name looks
right and fails: `claude plugin update caveman` answers *Failed to update
plugin "caveman": Plugin "caveman" not found*. Use the full name:

```bash
claude plugin update caveman@caveman
```

`claude plugin list` shows what you have now. Restart Claude Code after an
update — hooks are read once at session start.

**Everything else:**

| Agent | Update command |
|---|---|
| **Gemini CLI** | The Gemini CLI owns its extensions — see `gemini extensions --help` for its update subcommand |
| **Installed via `npx skills add`** | Re-run the same `npx skills add` command — it overwrites in place |
| **Hooks / opencode / OpenClaw / rule files** | Re-run the installer; it is idempotent for everything it owns |
| **`caveman` CLI** (proxy, learn, shrink, browse) | `npm install -g @caveman-ai/cli@latest` (or `bun add -g @caveman-ai/cli@latest`) |

```bash
# Re-run the installer (safe to repeat — overwrites only installer-owned files)
npx -y github:JuliusBrussee/caveman
```

The `caveman` CLI has its own version number. `caveman --version` shows the CLI release (2.x), not the Caveman release (3.x), and re-running the installer does not update the CLI. Use the npm (or bun) command above for that.

## Uninstall

```bash
npx -y github:JuliusBrussee/caveman -- --uninstall
```

Run this **before** `npm uninstall -g @caveman-ai/cli`. It hands native agent
integrations to `caveman disable --all`, so it needs the `caveman` CLI still on
PATH. If the CLI is already gone, it says which agents are still routed and what
to run; reinstall the CLI, run `caveman disable --all`, then remove it again.

What it removes:

- Native agent routing written by `caveman setup --install` / `caveman enable <agent>` — for Claude Code that is `ANTHROPIC_BASE_URL` and `_CLAUDE_CODE_ASSUME_FIRST_PARTY_BASE_URL` in `~/.claude/settings.json`, which is what makes [Claude Code Remote Control](docs/technical/agent-wrapping.md) unavailable while Caveman is routing. Restored from each agent's integration journal, so your own prior value comes back.
- Caveman hook entries from `$CLAUDE_CONFIG_DIR/settings.json` (default `~/.claude/`; matched by the substring `caveman`).
- Hook files in `$CLAUDE_CONFIG_DIR/hooks/` (`caveman-activate.js`, `caveman-mode-tracker.js`, `caveman-parse.js`, `caveman-stats.js`, `caveman-config.js`, `cavecrew-model-overrides.js`, `caveman-statusline.{sh,ps1}`, plus the dir's `package.json` marker).
- The Claude Code plugin and the Gemini CLI extension (if installed).
- The opencode native plugin (`~/.config/opencode/plugins/caveman/`, the `plugin` and `mcp.caveman-shrink` entries from `opencode.json`, our skill/agent/command files, the caveman block from `AGENTS.md`, and the opencode flag file).
- The Codex SessionStart entry from `$CODEX_HOME/hooks.json` (default `~/.codex/`; other hooks, including the caveman CLI's own, stay) and the hook files under `$CODEX_HOME/caveman/`. The file goes away only if caveman's entry was all it held.
- The Oh My Pi plugin (`omp plugin uninstall caveman`) and Caveman's managed OMP plugin package at `~/.omp/caveman-plugin/`.
- The Antigravity CLI plugin (`agy plugin uninstall caveman`).
- Cursor: the Cavecrew agents in `~/.cursor/agents/`, `~/.cursor/caveman/`, and caveman's entry in `~/.cursor/hooks.json`.
- The Copilot CLI session hook: `$COPILOT_HOME/hooks/caveman.json` and `$COPILOT_HOME/caveman/` (default `~/.copilot/`).
- The OpenClaw workspace skill folder and the marker-fenced block from `~/.openclaw/workspace/SOUL.md` (when present).
- Owned native skill copies (Continue, AiderDesk, Antigravity, Grok Build, and iFlow/Crush with a custom home) and the marker-fenced block from `$GROK_HOME/AGENTS.md` (default `~/.grok/AGENTS.md`). Your own text in that file stays.
- All mode state in `$CLAUDE_CONFIG_DIR`: the `.caveman-sessions/` directory (one file per window), `.caveman-active`, `.caveman-active.prev`, `.caveman-mode-log.jsonl`, `.caveman-statusline-suffix`, `.caveman-nudge-shown`, and `.caveman-statusline-stale`.

What it does **not** remove:

- Skills installed via `npx skills add` — the `skills` CLI manages those. Remove them with `npx skills remove JuliusBrussee/caveman -g`. It lists the skills and asks before deleting. It clears those names from every agent's folder, so if you keep a skill of your own with one of them (such as `migration`), add `-a <agent>` with the name after `-a` in that agent's row above to limit it. Replit: run it inside the project, without `-g`.
- Per-repo rule files written by `--with-init` (`.cursor/rules/`, `.windsurf/rules/`, `.clinerules/`, `.github/copilot-instructions.md`, `.opencode/AGENTS.md`, `AGENTS.md`). Delete by hand if you want.
- `$CLAUDE_CONFIG_DIR/.caveman-history.jsonl`, which keeps lifetime stats. Delete it manually if you want history removed too.

## Troubleshooting

**"Install script broke. What now?"**

Open your agent in this repo and say:

> "Read CLAUDE.md and INSTALL.md. Install caveman for me."

Agent read repo. Agent run install. Caveman make agent talk less — agent first job is install caveman to talk less. Snake eat tail.

Still broken? [Open an issue](https://github.com/JuliusBrussee/caveman/issues).

**"I ran the installer but Claude Code isn't talking caveman."**

1. Run `node installer/install.js --list` — confirm `claude` is on the detected list. If not, `claude` isn't on `PATH`. Fix that first.
2. Open `$CLAUDE_CONFIG_DIR/settings.json` (default `~/.claude/settings.json`) and look for `"hooks"` containing `caveman-activate.js` and `caveman-mode-tracker.js`. If missing, re-run with `--force`.
3. Check `$CLAUDE_CONFIG_DIR/.caveman-active` exists with content `caveman`. If not, the SessionStart hook silent-failed — check `$CLAUDE_CONFIG_DIR/hooks/` for the JS files and try `node $CLAUDE_CONFIG_DIR/hooks/caveman-activate.js < /dev/null` to see if it errors. Keep the `< /dev/null`: the hook reads its payload from stdin, and a pipe that never closes makes it wait out its 2s watchdog.
4. Restart Claude Code. The SessionStart hook only fires on session start, not mid-session.

**"There is no `node` on this machine."**

Caveman turns itself on through small Node.js scripts, so auto-activation needs Node.js 18 or newer on `PATH`. Without it the plugin steps aside quietly (no hook errors) and caveman starts only when you type `/caveman` in a session. Install Node from [nodejs.org](https://nodejs.org) to get auto-activation back. Standalone hooks remember the `node` path they were installed with; if you moved or removed that Node, re-run the installer.

**"One window is caveman, another isn't."**

That's intended. Mode is per window. Say `/caveman` in the window you want it in.
`${CLAUDE_CONFIG_DIR:-$HOME/.claude}/.caveman-sessions/` has one file per session
if you want to see the current state of each.

**"I also run ponytail (or another style plugin). Which one wins?"**

Both apply. Ponytail decides how much code gets written; caveman decides how the reply reads. Ponytail's own instructions say to pair it with caveman. Want only one? Say `stop caveman` in that window, or set `"defaultMode": "manual"` in `~/.config/caveman/config.json` so caveman starts off until you type `/caveman`.

**"I said 'stop caveman' and it came back on its own."**

Fixed. This used to happen when a long conversation hit automatic context
compaction: the SessionStart hook re-ran and re-applied your configured default.
Deactivation is now stored as a durable value that survives compaction and
resume. If you still see it, check whether `CAVEMAN_DEFAULT_MODE` or a repo-local
`.caveman.json` is re-arming it on a genuinely new session, or whether you ran
`/clear` — that is a deliberate reset, and intended.

**"OpenCode works, but Caveman sees no traffic."**

`caveman enable opencode` and `caveman opencode` send only OpenCode's `openai`, `anthropic` and `opencode-go` providers through Caveman. Every other provider talks to its service directly, with nothing compressed or counted. That includes a GitHub Copilot sign-in and OpenCode Zen (`opencode/...` models). After `caveman enable opencode`, `caveman doctor opencode` and `caveman status` warn when the model in your global OpenCode config uses a provider outside that list, or when no model is set there and none of your OpenCode sign-ins is on it. `caveman opencode` alone does not check.

**"Hooks failing on Windows."**

- Use `install.ps1`, not `install.sh`. Git Bash works for the shell version, but the hook side wires PowerShell counterparts (`caveman-statusline.ps1`).
- PowerShell 5.1 minimum. Check with `$PSVersionTable.PSVersion`.
- If `irm | iex` blocks on execution policy: `Set-ExecutionPolicy -Scope Process -ExecutionPolicy Bypass` for the install session, then re-run.
- Long-running issues: see `docs/install-windows.md` in the repo for manual fallback.

**"My `settings.json` got mangled."**

The installer uses a JSONC-tolerant parser (`installer/lib/settings.js`) so comments and trailing commas don't crash the merge. It also runs `validateHookFields()` before every write so a malformed hook can't poison the file. If something still went wrong:

1. Check for a backup at `$CLAUDE_CONFIG_DIR/settings.json.bak` (installer writes one before any merge).
2. If no backup, restore from your shell history or version control.
3. File an issue with the broken `settings.json` content (redacted) — that file passing validation but breaking Claude Code is a bug we want to fix.

**"I'm in a managed env where I can't install hooks."**

Use the rule-file-only path. Static rule files need no hook:

```bash
# Skills for one agent, no hooks (still writes that agent's skills, and for Cursor the Cavecrew agents, in your home folder)
node installer/install.js --only cursor --no-hooks

# Or write rule files into the current repo only (no global state), one agent per run
node src/tools/caveman-init.js --only cursor
node src/tools/caveman-init.js --only windsurf
```

The second form drops `.cursor/rules/caveman.mdc` and `.windsurf/rules/caveman.md` into your repo. No hooks, no global config, nothing outside the repo. (`--with-init` on the installer is not repo-only: it also runs the normal install for every agent.)

**"`npx skills add` errored on a profile slug."**

The profile slug must exist in [vercel-labs/skills](https://github.com/vercel-labs/skills). If a row in the table above 404s, the upstream profile was renamed or removed — open an issue, we'll update.

## Privacy

The installer doesn't phone home. It writes to:

- `$CLAUDE_CONFIG_DIR` (default `~/.claude/`) — hooks, flag file, `settings.json` merge.
- Each agent's own config location — Cursor's `.cursor/rules/`, Windsurf's `.windsurf/rules/`, opencode's `~/.config/opencode/`, etc.
- Your current working directory (only with `--with-init`) — repo-local rule files.
- `$CODEX_HOME` (default `~/.codex/`; only when Codex is detected or selected, and not with `--no-hooks`) — the caveman hook files in `caveman/`, one entry in `hooks.json`, and an ownership record.
- `~/.omp/caveman-plugin/` (only with `--only omp`, or auto-detect when `omp` is on `PATH`) — managed OMP plugin package installed through `omp plugin install`.
- `~/.openclaw/workspace/` (only with `--only openclaw` or `--with-init` when OpenClaw is detected) — the one `--with-init` side-effect outside the cwd.

Installer sends no Caveman telemetry or analytics. Run from a clone or via npx, its own code copies files locally. One exception: run detached from any checkout (the rare curl-fallback path), it downloads hook files from raw.githubusercontent.com pinned to the release tag and checks each against the SHA-256 manifest committed at that same tag before wiring anything. The manifest catches corrupt or partial downloads; because it comes from the same tag, it cannot detect a tag that was moved. Network requests also happen indirectly through per-agent CLIs it shells out to — `claude plugin marketplace add`, `claude plugin install`, `gemini extensions install`, `omp plugin install`, `npm view caveman-shrink`, and `npx -y skills add`. Each fetches from its own registry or local plugin manager (Anthropic / GitHub / OMP / npm). Source: [`installer/install.js`](installer/install.js).

After install, classic skill and output hooks stay local. CLI telemetry is on by default (turn it off with `caveman telemetry off`) and sends content-free usage events, stored with your IP address, including a start event for each agent session launched through the CLI's native install. Proxy, SDK, provider, authenticated sync, and managed gateway commands use network according to their configured purpose. Full data-flow statement: [SECURITY.md](./SECURITY.md#cli-usage-telemetry).

---

Stuck? Open an issue: <https://github.com/JuliusBrussee/caveman/issues>
