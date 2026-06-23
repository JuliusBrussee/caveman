# Install caveman

One install. Works for every AI coding agent on your machine.

If just want it to work, run the one-liner. If want to know what gets touched, scroll down.

## One-liner

**macOS / Linux / WSL / Git Bash**

```bash
curl -fsSL https://raw.githubusercontent.com/JuliusBrussee/caveman/main/install.sh | bash
```

**Windows (PowerShell 5.1+)**

```powershell
irm https://raw.githubusercontent.com/JuliusBrussee/caveman/main/install.ps1 | iex
```

> Piping a script straight into a shell runs it sight-unseen. If you'd rather read it first, download then run: `curl -fsSL https://raw.githubusercontent.com/JuliusBrussee/caveman/main/install.sh -o install.sh` (review it) `&& bash install.sh`. The installer downloads hook files from a pinned release tag and verifies them against a committed SHA-256 manifest before writing.

What it does:

- Auto-detects every supported agent installed on your machine (Claude Code, Cursor, Codex, etc.).
- For each one, runs that agent's native install path (plugin / extension / rule file / `npx skills add`).
- Wires Claude Code hooks and statusline badge on top. (`caveman-shrink` MCP middleware is opt-in via `--with-mcp-shrink` — see flag table below.)
- Skips anything you don't have. Safe to re-run. ~30 seconds end-to-end.

Want to preview before installing? Use `--dry-run`:

```bash
curl -fsSL https://raw.githubusercontent.com/JuliusBrussee/caveman/main/install.sh | bash -s -- --dry-run
```

## Per-agent install

If you want to install for one agent (or want to know exactly what command runs under the hood), use the table below. Every row also works as `--only <id>` to the unified installer.

| Agent | Install command | Auto-activates? |
|---|---|:-:|
| **Claude Code** | `claude plugin marketplace add JuliusBrussee/caveman && claude plugin install caveman@caveman` | Yes |
| **Gemini CLI** | `gemini extensions install https://github.com/JuliusBrussee/caveman` | Yes |
| **opencode** | `node bin/install.js --only opencode` *(or `npx -y github:JuliusBrussee/caveman -- --only opencode`)* | Yes (plugin + AGENTS.md) |
| **OpenClaw** | `npx -y github:JuliusBrussee/caveman -- --only openclaw` | Yes (workspace skill + SOUL.md) |
| **GitHub Copilot CLI** | `npx -y github:JuliusBrussee/caveman -- --only copilot-cli` | Yes (sessionStart hook in `~/.copilot/hooks/`) |
| **Codex CLI** | `npx skills add JuliusBrussee/caveman -a codex` | Per-session: `/caveman` |
| **Cursor** | `npx skills add JuliusBrussee/caveman -a cursor` | Per-session by default; `--with-init` for an always-on rule file |
| **Windsurf** | `npx skills add JuliusBrussee/caveman -a windsurf` | Per-session by default; `--with-init` for an always-on rule file |
| **Cline** | `npx skills add JuliusBrussee/caveman -a cline` | Per-session by default; `--with-init` for an always-on rule file |
| **GitHub Copilot** *(editor extension, soft probe)* | `npx -y github:JuliusBrussee/caveman -- --only copilot --with-init` | Repo-wide instructions via `--with-init` |
| **Continue** | `npx skills add JuliusBrussee/caveman -a continue` | No — say `/caveman` |
| **Kilo Code** | `npx skills add JuliusBrussee/caveman -a kilo` | No |
| **Roo Code** | `npx skills add JuliusBrussee/caveman -a roo` | No |
| **Augment Code** | `npx skills add JuliusBrussee/caveman -a augment` | No |
| **Aider Desk** | `npx skills add JuliusBrussee/caveman -a aider-desk` | No |
| **Sourcegraph Amp** | `npx skills add JuliusBrussee/caveman -a amp` | No |
| **IBM Bob** | `npx skills add JuliusBrussee/caveman -a bob` | No |
| **Crush** | `npx skills add JuliusBrussee/caveman -a crush` | No |
| **Devin (terminal)** | `npx skills add JuliusBrussee/caveman -a devin` | No |
| **Droid (Factory)** | `npx skills add JuliusBrussee/caveman -a droid` | No |
| **ForgeCode** | `npx skills add JuliusBrussee/caveman -a forgecode` | No |
| **Block Goose** | `npx skills add JuliusBrussee/caveman -a goose` | No |
| **iFlow CLI** | `npx skills add JuliusBrussee/caveman -a iflow-cli` | No |
| **Kiro CLI** | `npx skills add JuliusBrussee/caveman -a kiro-cli` | No |
| **Mistral Vibe** | `npx skills add JuliusBrussee/caveman -a mistral-vibe` | No |
| **OpenHands** | `npx skills add JuliusBrussee/caveman -a openhands` | No |
| **Qwen Code** | `npx skills add JuliusBrussee/caveman -a qwen-code` | No |
| **Atlassian Rovo Dev** | `npx skills add JuliusBrussee/caveman -a rovodev` | No |
| **Tabnine CLI** | `npx skills add JuliusBrussee/caveman -a tabnine-cli` | No |
| **Trae** | `npx skills add JuliusBrussee/caveman -a trae` | No |
| **Warp** | `npx skills add JuliusBrussee/caveman -a warp` | No |
| **Replit Agent** | `npx skills add JuliusBrussee/caveman -a replit` | No |
| **JetBrains Junie** *(soft probe)* | `npx skills add JuliusBrussee/caveman -a junie` | No |
| **Qoder** *(soft probe)* | `npx skills add JuliusBrussee/caveman -a qoder` | No |
| **Google Antigravity** *(soft probe)* | `npx skills add JuliusBrussee/caveman -a antigravity` | No |

"Soft probe" = installer won't auto-detect these without `--only <id>` because there's no reliable always-on signal (Copilot subscription state is auth-gated; the others have no CLI / config-dir-only). Pass the flag when you want them.

**Two GitHub Copilot rows, on purpose.** `copilot-cli` targets the **Copilot CLI** (`copilot` binary) and installs (1) a real hook into `~/.copilot/hooks/caveman.json` — its `sessionStart` hook injects the caveman ruleset every session, so caveman is on automatically (no `/caveman` needed) — and (2) the caveman **Agent Skills** into `~/.copilot/skills/` (caveman, caveman-commit, caveman-review, caveman-compress, caveman-help), which Copilot CLI auto-loads when relevant to your prompt. Mode picks (`/caveman ultra`, "stop caveman") persist across sessions via the flag at `~/.copilot/.caveman-active`. The `copilot` row targets the **editor extension** (VS Code / Cursor), which has no hook system — it gets repo-wide instructions via `--with-init`. Install whichever you use; both is fine.

> **Note on `/caveman` in the CLI.** Copilot CLI doesn't support prompt files (the `/command` mechanism — see GitHub's [customization cheat sheet](https://docs.github.com/en/copilot/reference/customization-cheat-sheet)), but because caveman installs a **skill** named `caveman`, typing `/caveman` (or `/caveman ultra`) invokes that skill. The `userPromptSubmitted` hook reads Copilot's skill-invocation rewrite and persists the chosen mode to the flag, so `/caveman ultra` switches the level. Caveman is also always-on via the `sessionStart` hook, and "stop caveman" / "normal mode" deactivate it. For a `--repo` install the mode flag is stored repo-locally (under `.github/hooks/caveman/`, gitignored), not in your global `~/.copilot`.

**Repo-only (not global) Copilot CLI install.** Add `--repo` to scope the hooks to one project instead of your whole user account:

```bash
cd /path/to/your/project
npx -y github:JuliusBrussee/caveman -- --only copilot-cli --repo
```

This writes a **committable** `<repo>/.github/hooks/caveman.json` plus self-contained scripts under `<repo>/.github/hooks/caveman/` — caveman activates only when you run Copilot CLI inside that repo, and nothing touches `~/.copilot`. Commit `.github/hooks/` to share caveman with your team. Requires `node` on `PATH`. Remove with `--uninstall --repo` from the same repo.

For "auto-activates? No" agents, type `/caveman` once per session (or use natural-language triggers like "talk like caveman", "caveman mode").

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
| `--with-mcp-shrink="<upstream cmd>"` | Register `caveman-shrink` MCP proxy wrapping the given upstream MCP server. **Off by default.** A value is required — caveman-shrink is a proxy and exits immediately without one. Example: `--with-mcp-shrink="npx @modelcontextprotocol/server-filesystem /tmp"`. The value is split on whitespace; for paths-with-spaces, install via `node bin/install.js` from a clone or edit `~/.claude.json` after a stub install. |
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

Statusline should show `[CAVEMAN]` (orange) at the bottom of Claude Code. After your first `/caveman-stats` run it appends a savings counter like `[CAVEMAN] ⛏ 12.4k`.

## Uninstall

```bash
npx -y github:JuliusBrussee/caveman -- --uninstall
```

What it removes:

- Caveman hook entries from `$CLAUDE_CONFIG_DIR/settings.json` (default `~/.claude/`; matched by the substring `caveman`).
- Hook files in `$CLAUDE_CONFIG_DIR/hooks/` (`caveman-activate.js`, `caveman-mode-tracker.js`, `caveman-stats.js`, `caveman-config.js`, `caveman-statusline.{sh,ps1}`, plus the dir's `package.json` marker).
- The Claude Code plugin and the Gemini CLI extension (if installed).
- The opencode native plugin (`~/.config/opencode/plugins/caveman/`, the `plugin` and `mcp.caveman-shrink` entries from `opencode.json`, our skill/agent/command files, the caveman block from `AGENTS.md`, and the opencode flag file).
- The OpenClaw workspace skill folder and the marker-fenced block from `~/.openclaw/workspace/SOUL.md` (when present).
- The `.caveman-active` flag file.

What it does **not** remove:

- Skills installed via `npx skills add` — the `skills` CLI manages those. Run `npx skills remove caveman` (or use your IDE's skill manager).
- Per-repo rule files written by `--with-init` (`.cursor/rules/`, `.windsurf/rules/`, `.clinerules/`, `.github/copilot-instructions.md`, `.opencode/AGENTS.md`, `AGENTS.md`). Delete by hand if you want.

## Troubleshooting

**"Install script broke. What now?"**

Open your agent in this repo and say:

> "Read CLAUDE.md and INSTALL.md. Install caveman for me."

Agent read repo. Agent run install. Caveman make agent talk less — agent first job is install caveman to talk less. Snake eat tail.

Still broken? [Open an issue](https://github.com/JuliusBrussee/caveman/issues).

**"I ran the installer but Claude Code isn't talking caveman."**

1. Run `node bin/install.js --list` — confirm `claude` is on the detected list. If not, `claude` isn't on `PATH`. Fix that first.
2. Open `$CLAUDE_CONFIG_DIR/settings.json` (default `~/.claude/settings.json`) and look for `"hooks"` containing `caveman-activate.js` and `caveman-mode-tracker.js`. If missing, re-run with `--force`.
3. Check `$CLAUDE_CONFIG_DIR/.caveman-active` exists with content `full`. If not, the SessionStart hook silent-failed — check `$CLAUDE_CONFIG_DIR/hooks/` for the JS files and try `node $CLAUDE_CONFIG_DIR/hooks/caveman-activate.js < /dev/null` to see if it errors.
4. Restart Claude Code. The SessionStart hook only fires on session start, not mid-session.

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

No telemetry. No analytics. The installer's own code makes no network calls. Network requests do happen indirectly through the per-agent CLIs it shells out to — `claude plugin marketplace add`, `claude plugin install`, `gemini extensions install`, `npm view caveman-shrink`, and `npx -y skills add`. Each fetches from its own registry (Anthropic / GitHub / npm). Source: [`bin/install.js`](bin/install.js).

---

Stuck? Open an issue: <https://github.com/JuliusBrussee/caveman/issues>
