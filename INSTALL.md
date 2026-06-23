# Install missionctl

One install. Works for every AI coding agent on your machine.

If just want it to work, run the one-liner. If want to know what gets touched, scroll down.

## One-liner

**macOS / Linux / WSL / Git Bash**

```bash
curl -fsSL https://raw.githubusercontent.com/dnl-re/missionctl/main/install.sh | bash
```

**Windows (PowerShell 5.1+)**

```powershell
irm https://raw.githubusercontent.com/dnl-re/missionctl/main/install.ps1 | iex
```

> Piping a script straight into a shell runs it sight-unseen. If you'd rather read it first, download then run: `curl -fsSL https://raw.githubusercontent.com/dnl-re/missionctl/main/install.sh -o install.sh` (review it) `&& bash install.sh`. The installer downloads hook files from a pinned release tag and verifies them against a committed SHA-256 manifest before writing.

What it does:

- Auto-detects every supported agent installed on your machine (Claude Code, Cursor, Codex, etc.).
- For each one, runs that agent's native install path (plugin / extension / rule file / `npx skills add`).
- Wires Claude Code hooks and statusline badge on top. (`missionctl-shrink` MCP middleware is opt-in via `--with-mcp-shrink` — see flag table below.)
- Skips anything you don't have. Safe to re-run. ~30 seconds end-to-end.

Want to preview before installing? Use `--dry-run`:

```bash
curl -fsSL https://raw.githubusercontent.com/dnl-re/missionctl/main/install.sh | bash -s -- --dry-run
```

## Per-agent install

If you want to install for one agent (or want to know exactly what command runs under the hood), use the table below. Every row also works as `--only <id>` to the unified installer.

| Agent | Install command | Auto-activates? |
|---|---|:-:|
| **Claude Code** | `claude plugin marketplace add dnl-re/missionctl && claude plugin install missionctl@missionctl` | Yes |
| **Gemini CLI** | `gemini extensions install https://github.com/dnl-re/missionctl` | Yes |
| **opencode** | `node bin/install.js --only opencode` *(or `npx -y github:dnl-re/missionctl -- --only opencode`)* | Yes (plugin + AGENTS.md) |
| **OpenClaw** | `npx -y github:dnl-re/missionctl -- --only openclaw` | Yes (workspace skill + SOUL.md) |
| **Codex CLI** | `npx skills add dnl-re/missionctl -a codex` | Per-session: `/missionctl` |
| **Cursor** | `npx skills add dnl-re/missionctl -a cursor` | Per-session by default; `--with-init` for an always-on rule file |
| **Windsurf** | `npx skills add dnl-re/missionctl -a windsurf` | Per-session by default; `--with-init` for an always-on rule file |
| **Cline** | `npx skills add dnl-re/missionctl -a cline` | Per-session by default; `--with-init` for an always-on rule file |
| **GitHub Copilot** *(soft probe)* | `npx -y github:dnl-re/missionctl -- --only copilot --with-init` | Repo-wide instructions via `--with-init` |
| **Continue** | `npx skills add dnl-re/missionctl -a continue` | No — say `/missionctl` |
| **Kilo Code** | `npx skills add dnl-re/missionctl -a kilo` | No |
| **Roo Code** | `npx skills add dnl-re/missionctl -a roo` | No |
| **Augment Code** | `npx skills add dnl-re/missionctl -a augment` | No |
| **Aider Desk** | `npx skills add dnl-re/missionctl -a aider-desk` | No |
| **Sourcegraph Amp** | `npx skills add dnl-re/missionctl -a amp` | No |
| **IBM Bob** | `npx skills add dnl-re/missionctl -a bob` | No |
| **Crush** | `npx skills add dnl-re/missionctl -a crush` | No |
| **Devin (terminal)** | `npx skills add dnl-re/missionctl -a devin` | No |
| **Droid (Factory)** | `npx skills add dnl-re/missionctl -a droid` | No |
| **ForgeCode** | `npx skills add dnl-re/missionctl -a forgecode` | No |
| **Block Goose** | `npx skills add dnl-re/missionctl -a goose` | No |
| **iFlow CLI** | `npx skills add dnl-re/missionctl -a iflow-cli` | No |
| **Kiro CLI** | `npx skills add dnl-re/missionctl -a kiro-cli` | No |
| **Mistral Vibe** | `npx skills add dnl-re/missionctl -a mistral-vibe` | No |
| **OpenHands** | `npx skills add dnl-re/missionctl -a openhands` | No |
| **Qwen Code** | `npx skills add dnl-re/missionctl -a qwen-code` | No |
| **Atlassian Rovo Dev** | `npx skills add dnl-re/missionctl -a rovodev` | No |
| **Tabnine CLI** | `npx skills add dnl-re/missionctl -a tabnine-cli` | No |
| **Trae** | `npx skills add dnl-re/missionctl -a trae` | No |
| **Warp** | `npx skills add dnl-re/missionctl -a warp` | No |
| **Replit Agent** | `npx skills add dnl-re/missionctl -a replit` | No |
| **JetBrains Junie** *(soft probe)* | `npx skills add dnl-re/missionctl -a junie` | No |
| **Qoder** *(soft probe)* | `npx skills add dnl-re/missionctl -a qoder` | No |
| **Google Antigravity** *(soft probe)* | `npx skills add dnl-re/missionctl -a antigravity` | No |

"Soft probe" = installer won't auto-detect these without `--only <id>` because there's no reliable always-on signal (Copilot subscription state is auth-gated; the others have no CLI / config-dir-only). Pass the flag when you want them.

For "auto-activates? No" agents, type `/missionctl` once per session (or use natural-language triggers like "operational brevity mode", "missionctl mode").

**Finding a profile slug for `npx skills add ... -a <profile>`?** Either read the table above, or print the live matrix from the installer:

```bash
# Either of these works (install.sh / install.ps1 are thin shims that
# forward all flags to bin/install.js):
bash install.sh --list             # macOS / Linux / WSL, from a local clone
pwsh install.ps1 --list            # Windows / PowerShell, from a local clone
node bin/install.js --list         # any platform, from a local clone
npx -y github:dnl-re/missionctl -- --list   # no clone needed
```

Each row prints the agent id, profile slug (where applicable), and whether it was auto-detected on your machine. Full agent matrix (with detection rules) is also defined in `bin/install.js` under the `PROVIDERS` array.

## Manual install (no `curl | bash`)

If you'd rather see exactly what runs:

```bash
# Clone the repo
git clone https://github.com/dnl-re/missionctl.git
cd missionctl

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
| `--with-mcp-shrink="<upstream cmd>"` | Register `missionctl-shrink` MCP proxy wrapping the given upstream MCP server. **Off by default.** A value is required — missionctl-shrink is a proxy and exits immediately without one. Example: `--with-mcp-shrink="npx @modelcontextprotocol/server-filesystem /tmp"`. The value is split on whitespace; for paths-with-spaces, install via `node bin/install.js` from a clone or edit `~/.claude.json` after a stub install. |
| `--no-mcp-shrink` | Skip MCP-shrink registration. (Default.) |
| `--with-hooks` / `--no-hooks` | Force-on or force-off the Claude Code hook installer. (Default: on.) |
| `--skip-skills` | Don't run the npx-skills auto-detect fallback when nothing else matched. |
| `--config-dir <path>` | Claude Code config dir for hook files + `settings.json`. **Does NOT scope** `claude plugin install`, `gemini extensions install`, opencode (`XDG_CONFIG_HOME`), or openclaw (`OPENCLAW_WORKSPACE`) — those use their own paths. Default: `$CLAUDE_CONFIG_DIR` or `~/.claude`. `~` is expanded. |
| `--non-interactive` | Never prompt; use defaults. (Auto when stdin is not a TTY.) |
| `--no-color` | Disable ANSI colors. |
| `--list` | Print full agent matrix and exit. |
| `--force` | Re-run even if already installed. |
| `--uninstall` | Remove everything. See below. |

## Always-on rules

For agents without a hook system (Cursor, Windsurf, Cline, Copilot, and friends), the always-on path is a static rule file. Two ways:

```bash
# Drop rule files into the current repo
node bin/install.js --with-init

# Or pull the rule body straight in (manual)
curl -fsSL https://raw.githubusercontent.com/dnl-re/missionctl/main/src/rules/missionctl-activate.md \
  > .cursor/rules/missionctl.mdc   # or .windsurf/rules/missionctl.md, .clinerules/missionctl.md, .github/copilot-instructions.md
```

`--with-init` writes the rule into every supported per-agent location it can detect (`.cursor/rules/`, `.windsurf/rules/`, `.clinerules/`, `.github/copilot-instructions.md`, `.opencode/AGENTS.md`, `AGENTS.md`). It also installs the OpenClaw workspace bootstrap (skill folder + SOUL.md marker block) when `~/.openclaw/workspace/` exists. Single source: [`src/rules/missionctl-activate.md`](src/rules/missionctl-activate.md).

## Verify

After install, three quick checks:

**1. See what got installed.**

```bash
node bin/install.js --list
```

You should see ~30 rows. Detected agents are marked. Anything you wanted but isn't marked → not detected (likely the binary isn't on `PATH`).

**2. Talk to Claude Code.**

Open Claude Code, type `/missionctl`. Responses should turn terse — short fragments, no filler. Try a real question: "What are closures in JS?" — the answer should drop articles and filler while keeping the technical substance exact.

**3. Check the flag file.**

```bash
cat "${CLAUDE_CONFIG_DIR:-$HOME/.claude}/.missionctl-active"
# expected output: full
```

If it's missing or empty, the SessionStart hook didn't fire. See troubleshooting below.

Statusline should show `[missionctl]` (orange) at the bottom of Claude Code. After your first `/missionctl-stats` run it appends a savings counter like `[missionctl] 📡 12.4k`.

## Uninstall

```bash
npx -y github:dnl-re/missionctl -- --uninstall
```

What it removes:

- missionctl hook entries from `$CLAUDE_CONFIG_DIR/settings.json` (default `~/.claude/`; matched by the substring `missionctl`).
- Hook files in `$CLAUDE_CONFIG_DIR/hooks/` (`missionctl-activate.js`, `missionctl-mode-tracker.js`, `missionctl-stats.js`, `missionctl-config.js`, `missionctl-statusline.{sh,ps1}`, plus the dir's `package.json` marker).
- The Claude Code plugin and the Gemini CLI extension (if installed).
- The opencode native plugin (`~/.config/opencode/plugins/missionctl/`, the `plugin` and `mcp.missionctl-shrink` entries from `opencode.json`, our skill/agent/command files, the missionctl block from `AGENTS.md`, and the opencode flag file).
- The OpenClaw workspace skill folder and the marker-fenced block from `~/.openclaw/workspace/SOUL.md` (when present).
- The `.missionctl-active` flag file.

What it does **not** remove:

- Skills installed via `npx skills add` — the `skills` CLI manages those. Run `npx skills remove missionctl` (or use your IDE's skill manager).
- Per-repo rule files written by `--with-init` (`.cursor/rules/`, `.windsurf/rules/`, `.clinerules/`, `.github/copilot-instructions.md`, `.opencode/AGENTS.md`, `AGENTS.md`). Delete by hand if you want.

## Troubleshooting

**"Install script broke. What now?"**

Open your agent in this repo and say:

> "Read CLAUDE.md and INSTALL.md. Install missionctl for me."

The agent reads the repo and runs the install itself — its first task is installing the very thing that makes it concise.

Still broken? [Open an issue](https://github.com/dnl-re/missionctl/issues).

**"I ran the installer but Claude Code isn't using brevity mode."**

1. Run `node bin/install.js --list` — confirm `claude` is on the detected list. If not, `claude` isn't on `PATH`. Fix that first.
2. Open `$CLAUDE_CONFIG_DIR/settings.json` (default `~/.claude/settings.json`) and look for `"hooks"` containing `missionctl-activate.js` and `missionctl-mode-tracker.js`. If missing, re-run with `--force`.
3. Check `$CLAUDE_CONFIG_DIR/.missionctl-active` exists with content `full`. If not, the SessionStart hook silent-failed — check `$CLAUDE_CONFIG_DIR/hooks/` for the JS files and try `node $CLAUDE_CONFIG_DIR/hooks/missionctl-activate.js < /dev/null` to see if it errors.
4. Restart Claude Code. The SessionStart hook only fires on session start, not mid-session.

**"Hooks failing on Windows."**

- Use `install.ps1`, not `install.sh`. Git Bash works for the shell version, but the hook side wires PowerShell counterparts (`missionctl-statusline.ps1`).
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

This drops `.cursor/rules/missionctl.mdc` (and friends) into your repo. No hooks, no global config, nothing outside the repo.

**"`npx skills add` errored on a profile slug."**

The profile slug must exist in [vercel-labs/skills](https://github.com/vercel-labs/skills). If a row in the table above 404s, the upstream profile was renamed or removed — open an issue, we'll update.

## Privacy

The installer doesn't phone home. It writes to:

- `$CLAUDE_CONFIG_DIR` (default `~/.claude/`) — hooks, flag file, `settings.json` merge.
- Each agent's own config location — Cursor's `.cursor/rules/`, Windsurf's `.windsurf/rules/`, opencode's `~/.config/opencode/`, etc.
- Your current working directory (only with `--with-init`) — repo-local rule files.
- `~/.openclaw/workspace/` (only with `--only openclaw` or `--with-init` when OpenClaw is detected) — the one `--with-init` side-effect outside the cwd.

No telemetry. No analytics. The installer's own code makes no network calls. Network requests do happen indirectly through the per-agent CLIs it shells out to — `claude plugin marketplace add`, `claude plugin install`, `gemini extensions install`, `npm view missionctl-shrink`, and `npx -y skills add`. Each fetches from its own registry (Anthropic / GitHub / npm). Source: [`bin/install.js`](bin/install.js).

---

Stuck? Open an issue: <https://github.com/dnl-re/missionctl/issues>
