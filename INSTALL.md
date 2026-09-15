# Install caveman

One install. Works for every AI coding agent on your machine.

If just want it to work, run the one-liner. If want to know what gets touched, scroll down.

## One-liner

**macOS / Linux / WSL / Git Bash**

```bash
curl -fsSL https://raw.githubusercontent.com/JuliusBrussee/caveman/v2.7.0/install.sh | bash
```

**Windows (PowerShell 5.1+)**

```powershell
irm https://raw.githubusercontent.com/JuliusBrussee/caveman/v2.7.0/install.ps1 | iex
```

> Piping a script straight into a shell runs it sight-unseen. If you'd rather read it first, download then run: `curl -fsSL https://raw.githubusercontent.com/JuliusBrussee/caveman/v2.7.0/install.sh -o install.sh` (review it) `&& bash install.sh`. Bootstrap, package, and hook downloads stay pinned to that immutable release. Set `CAVEMAN_REF` only when intentionally testing another ref.

What it does:

- Auto-detects every supported agent installed on your machine (Claude Code, Cursor, Codex, etc.).
- For each one, runs that agent's native install path (plugin / extension / rule file / `npx skills add`).
- Installs Cavecrew investigator, builder, and reviewer presets where the host supports native subagents.
- Wires Claude Code hooks and statusline badge on top. (`caveman-shrink` MCP middleware is opt-in via `--with-mcp-shrink` — see flag table below.)
- Skips anything you don't have. Safe to re-run. ~30 seconds end-to-end.

Want to preview before installing? Use `--dry-run`:

```bash
curl -fsSL https://raw.githubusercontent.com/JuliusBrussee/caveman/v2.7.0/install.sh | bash -s -- --dry-run
```

## Per-agent install

If you want to install for one agent (or want to know exactly what command runs under the hood), use the table below. Every row also works as `--only <id>` to the unified installer.

> **Choose the install scope your agent reads.** `-g` installs into the agent's user skill directory. Without it, skills belong to the current project. The unified installer uses user scope except for Replit, whose documented filesystem location is the project's `.agents/skills`. Run Replit's command from that project's Shell. Replit workspace-wide skills are managed in Workspace Settings.

| Agent | Install command | Auto-activates? |
|---|---|:-:|
| **Claude Code** | `claude plugin marketplace add JuliusBrussee/caveman && claude plugin install caveman@caveman` | Yes |
| **Gemini CLI** | `gemini extensions install https://github.com/JuliusBrussee/caveman` | Yes |
| **opencode** | `node bin/install.js --only opencode` *(or `npx -y github:JuliusBrussee/caveman -- --only opencode`)* | Yes (plugin + AGENTS.md) |
| **OpenClaw** | `npx -y github:JuliusBrussee/caveman -- --only openclaw` | Yes (workspace skill + SOUL.md) |
| **Hermes Agent** | `npx -y github:JuliusBrussee/caveman -- --only hermes` *(or `node bin/install.js --only hermes` from a clone)* | Yes (native skills, enabled on load) |
| **Codex CLI** | `npx skills add JuliusBrussee/caveman -a codex -g` | Per-session: `/caveman` |
| **Cursor** | `npx skills add JuliusBrussee/caveman -a cursor -g` | Per-session by default; `--with-init` for an always-on rule file |
| **Windsurf** | `npx skills add JuliusBrussee/caveman -a windsurf -g` | Per-session by default; `--with-init` for an always-on rule file |
| **Cline** | `npx skills add JuliusBrussee/caveman -a cline -g` | Per-session by default; `--with-init` for an always-on rule file |
| **GitHub Copilot** | `npx -y github:JuliusBrussee/caveman -- --only copilot --with-init` | Repo-wide instructions via `--with-init` |
| **Continue** | `npx -y github:JuliusBrussee/caveman -- --only continue` | No — invoke the Caveman skill |
| **Kilo Code** | `npx skills add JuliusBrussee/caveman -a kilo -g` | No |
| **Roo Code** | `npx skills add JuliusBrussee/caveman -a roo -g` | No |
| **Augment Code** | `npx skills add JuliusBrussee/caveman -a augment -g` | No |
| **AiderDesk** | `npx -y github:JuliusBrussee/caveman -- --only aider-desk` | No — enable Skills Tools |
| **Sourcegraph Amp** | `npx skills add JuliusBrussee/caveman -a amp -g` | No |
| **IBM Bob** | `npx skills add JuliusBrussee/caveman -a bob -g` | No |
| **Crush** | `npx -y github:JuliusBrussee/caveman -- --only crush` | No |
| **Devin (terminal)** | `npx skills add JuliusBrussee/caveman -a devin -g` | No |
| **Droid (Factory)** | `npx skills add JuliusBrussee/caveman -a droid -g` | No |
| **ForgeCode** | `npx skills add JuliusBrussee/caveman -a forgecode -g` | No |
| **Block Goose** | `npx skills add JuliusBrussee/caveman -a goose -g` | No |
| **iFlow CLI** | `npx -y github:JuliusBrussee/caveman -- --only iflow` | No |
| **Kiro CLI** | `npx skills add JuliusBrussee/caveman -a kiro-cli -g` | No |
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

"Soft probe" = installer won't auto-detect these without `--only <id>` because there's no reliable always-on signal (no CLI / config-dir-only). Pass the flag when you want them.

For "auto-activates? No" agents, invoke the Caveman skill using the host's skill menu, `/caveman` where supported, or a prompt naming the skill. Enable skills first if your host requires it: Augment has a Skills beta setting; AiderDesk requires Skills Tools in the active agent profile; custom Kiro agents need skill resources.

Continue needs physical skill directories because its current loader skips per-skill symlinks. The unified installer copies into `CONTINUE_GLOBAL_DIR/skills` (default `~/.continue/skills`) and follows AiderDesk's `AIDER_DESK_HOME_DIR` / `AIDER_DESK_DIR` overrides. It also honors `IFLOW_HOME` and Crush's exact `CRUSH_SKILLS_DIR`. Use the same environment when uninstalling. Existing unowned skill directories or symlinks produce a conflict rather than being silently replaced. See the [vendor discovery matrix](docs/technical/installer-provider-discovery.md) for sources and product limits.

Antigravity IDE reads `~/.gemini/antigravity/skills`; Antigravity 2.0 reads `~/.gemini/config/skills`. Select the matching product. Each command copies only into that product's directory.

**Finding a profile slug for `npx skills add ... -a <profile>`?** Either read the table above, or print the live matrix from the installer:

```bash
# Either of these works (install.sh / install.ps1 are thin shims that
# forward all flags to bin/install.js):
bash install.sh --list             # macOS / Linux / WSL, from a local clone
pwsh install.ps1 --list            # Windows / PowerShell, from a local clone
node bin/install.js --list         # any platform, from a local clone
npx -y github:JuliusBrussee/caveman -- --list   # no clone needed
```

Each row prints the agent id, profile slug (where applicable), and whether it was auto-detected on your machine. Full agent matrix (with detection rules) is also defined in `bin/install.js` under the `PROVIDERS` array.

## Manual install (no `curl | bash`)

If you'd rather see exactly what runs:

```bash
# Clone the repo
git clone https://github.com/JuliusBrussee/caveman.git
cd caveman

# Preview every command the installer would run
node bin/install.js --dry-run --all

# Inspect the agent matrix
node bin/install.js --list

# Install for everything detected
node bin/install.js --all
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
| `--with-hooks` / `--no-hooks` | Force-on or force-off the Claude Code hook installer. (Default: on.) |
| `--config-dir <path>` | Claude Code config dir for hook files + `settings.json`. **Does NOT scope** `claude plugin install`, `gemini extensions install`, opencode (`XDG_CONFIG_HOME`), or openclaw (`OPENCLAW_WORKSPACE`) — those use their own paths. Default: `$CLAUDE_CONFIG_DIR` or `~/.claude`. `~` is expanded. |
| `--non-interactive` | Never prompt; use defaults. (Auto when stdin is not a TTY.) |
| `--no-color` | Disable ANSI colors. |
| `--list` | Print full agent matrix and exit. |
| `--force` | Re-run even if already installed. |
| `--uninstall` | Remove everything. See below. |

For Windows paths containing spaces, pass a single quoted value from PowerShell:

```powershell
node bin/install.js --only opencode --with-mcp-shrink "'C:\Program Files\nodejs\node.exe' 'C:\MCP servers\server.js' 'C:\data folder\'"
```

The installer preserves each quoted path as one argument. For arguments containing quote characters, use a JSON array such as `--with-mcp-shrink='["node","server.js","path with spaces"]'`.

## Always-on rules

For agents without a hook system (Cursor, Windsurf, Cline, Copilot, and friends), the always-on path is a static rule file. Two ways:

```bash
# Drop rule files into the current repo
node bin/install.js --with-init

# Or pull the rule body straight in (manual)
curl -fsSL https://raw.githubusercontent.com/JuliusBrussee/caveman/main/src/rules/caveman-activate.md \
  > .cursor/rules/caveman.mdc   # or .windsurf/rules/caveman.md, .clinerules/caveman.md, .github/copilot-instructions.md
```

`--with-init` writes the rule into every supported per-agent location it can detect (`.cursor/rules/`, `.windsurf/rules/`, `.clinerules/`, `.github/copilot-instructions.md`, `.opencode/AGENTS.md`, `AGENTS.md`). It also installs the OpenClaw workspace bootstrap (skill folder + SOUL.md marker block) when `~/.openclaw/workspace/` exists. Single source: [`src/rules/caveman-activate.md`](src/rules/caveman-activate.md).

## Verify

After install, three quick checks:

**1. See what got installed.**

```bash
node bin/install.js --list
```

You should see ~30 rows. Detected agents are marked. Anything you wanted but isn't marked → not detected (likely the binary isn't on `PATH`).

**2. Talk to Claude Code.**

Open Claude Code, type `/caveman`. Response should be terse fragments — "Got it. Caveman mode on." or similar. Try a real question: "What is closures in JS?" — answer should drop articles and read like grunts.

**3. Check the flag file.**

```bash
cat "${CLAUDE_CONFIG_DIR:-$HOME/.claude}/.caveman-active"
# expected output: full
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

```bash
# Re-run the installer (safe to repeat — overwrites only installer-owned files)
npx -y github:JuliusBrussee/caveman
```

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
- The OpenClaw workspace skill folder and the marker-fenced block from `~/.openclaw/workspace/SOUL.md` (when present).
- All mode state in `$CLAUDE_CONFIG_DIR`: the `.caveman-sessions/` directory (one file per window), `.caveman-active`, `.caveman-active.prev`, `.caveman-mode-log.jsonl`, `.caveman-statusline-suffix`, and `.caveman-nudge-shown`.

What it does **not** remove:

- Skills installed via `npx skills add` — the `skills` CLI manages those. Run `npx skills remove caveman` (or use your IDE's skill manager).
- Per-repo rule files written by `--with-init` (`.cursor/rules/`, `.windsurf/rules/`, `.clinerules/`, `.github/copilot-instructions.md`, `.opencode/AGENTS.md`, `AGENTS.md`). Delete by hand if you want.
- `$CLAUDE_CONFIG_DIR/.caveman-history.jsonl`, which keeps lifetime stats. Delete it manually if you want history removed too.

## Troubleshooting

**"Install script broke. What now?"**

Open your agent in this repo and say:

> "Read CLAUDE.md and INSTALL.md. Install caveman for me."

Agent read repo. Agent run install. Caveman make agent talk less — agent first job is install caveman to talk less. Snake eat tail.

Still broken? [Open an issue](https://github.com/JuliusBrussee/caveman/issues).

**"I ran the installer but Claude Code isn't talking caveman."**

1. Run `node bin/install.js --list` — confirm `claude` is on the detected list. If not, `claude` isn't on `PATH`. Fix that first.
2. Open `$CLAUDE_CONFIG_DIR/settings.json` (default `~/.claude/settings.json`) and look for `"hooks"` containing `caveman-activate.js` and `caveman-mode-tracker.js`. If missing, re-run with `--force`.
3. Check `$CLAUDE_CONFIG_DIR/.caveman-active` exists with content `full`. If not, the SessionStart hook silent-failed — check `$CLAUDE_CONFIG_DIR/hooks/` for the JS files and try `node $CLAUDE_CONFIG_DIR/hooks/caveman-activate.js < /dev/null` to see if it errors. Keep the `< /dev/null`: the hook reads its payload from stdin, and a pipe that never closes makes it wait out its 3s watchdog.
4. Restart Claude Code. The SessionStart hook only fires on session start, not mid-session.

**"One window is caveman, another isn't."**

That's intended. Mode is per window. Say `/caveman` in the window you want it in.
`${CLAUDE_CONFIG_DIR:-$HOME/.claude}/.caveman-sessions/` has one file per session
if you want to see the current state of each.

**"I said 'stop caveman' and it came back on its own."**

Fixed. This used to happen when a long conversation hit automatic context
compaction: the SessionStart hook re-ran and re-applied your configured default.
Deactivation is now stored as a durable value that survives compaction and
resume. If you still see it, check whether `CAVEMAN_DEFAULT_MODE` or a repo-local
`.caveman.json` is re-arming it on a genuinely new session, or whether you ran
`/clear` — that is a deliberate reset, and intended.

**"Hooks failing on Windows."**

- Use `install.ps1`, not `install.sh`. Git Bash works for the shell version, but the hook side wires PowerShell counterparts (`caveman-statusline.ps1`).
- PowerShell 5.1 minimum. Check with `$PSVersionTable.PSVersion`.
- If `irm | iex` blocks on execution policy: `Set-ExecutionPolicy -Scope Process -ExecutionPolicy Bypass` for the install session, then re-run.
- Long-running issues: see `docs/install-windows.md` in the repo for manual fallback.

**"My `settings.json` got mangled."**

The installer uses a JSONC-tolerant parser (`bin/lib/settings.js`) so comments and trailing commas don't crash the merge. It also runs `validateHookFields()` before every write so a malformed hook can't poison the file. If something still went wrong:

1. Check for a backup at `$CLAUDE_CONFIG_DIR/settings.json.bak` (installer writes one before any merge).
2. If no backup, restore from your shell history or version control.
3. File an issue with the broken `settings.json` content (redacted) — that file passing validation but breaking Claude Code is a bug we want to fix.

**"I'm in a managed env where I can't install hooks."**

Use the rule-file-only path. Hooks are Claude Code-specific; everything else works via static rule files:

```bash
# Just install for one agent, no Claude hooks
node bin/install.js --only cursor

# Or write rule files into the current repo only (no global state)
node bin/install.js --with-init --only cursor --only windsurf
```

This drops `.cursor/rules/caveman.mdc` (and friends) into your repo. No hooks, no global config, nothing outside the repo.

**"`npx skills add` errored on a profile slug."**

The profile slug must exist in [vercel-labs/skills](https://github.com/vercel-labs/skills). If a row in the table above 404s, the upstream profile was renamed or removed — open an issue, we'll update.

## Privacy

The installer doesn't phone home. It writes to:

- `$CLAUDE_CONFIG_DIR` (default `~/.claude/`) — hooks, flag file, `settings.json` merge.
- Each agent's own config location — Cursor's `.cursor/rules/`, Windsurf's `.windsurf/rules/`, opencode's `~/.config/opencode/`, etc.
- Your current working directory (only with `--with-init`) — repo-local rule files.
- `~/.openclaw/workspace/` (only with `--only openclaw` or `--with-init` when OpenClaw is detected) — the one `--with-init` side-effect outside the cwd.

Installer sends no Caveman telemetry or analytics. Run from a clone or via npx, its own code copies files locally. One exception: run detached from any checkout (the rare curl-fallback path), it downloads hook files from raw.githubusercontent.com pinned to an immutable release tag and verifies each against a SHA-256 manifest before wiring anything. Network requests also happen indirectly through per-agent CLIs it shells out to — `claude plugin marketplace add`, `claude plugin install`, `gemini extensions install`, `npm view caveman-shrink`, and `npx -y skills add`. Each fetches from its own registry (Anthropic / GitHub / npm). Source: [`bin/install.js`](bin/install.js).

After install, classic skill and output hooks stay local. CLI telemetry is off by default and sends content-free events only after explicit opt-in. Proxy, SDK, provider, authenticated sync, and managed gateway commands use network according to their configured purpose. Full data-flow statement: [SECURITY.md](./SECURITY.md#privacy--telemetry).

---

Stuck? Open an issue: <https://github.com/JuliusBrussee/caveman/issues>
