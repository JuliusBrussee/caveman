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

What it does:

- Auto-detects every supported agent installed on your machine (Claude Code, Cursor, Codex, etc.).
- For each one, runs that agent's native install path (plugin / extension / rule file / `npx skills add`).
- Wires Claude Code hooks, statusline badge, and the `caveman-shrink` MCP middleware on top.
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
| **Codex CLI** | `npx skills add JuliusBrussee/caveman -a codex` | Yes (in this repo via `.codex/`) |
| **Cursor** | `npx skills add JuliusBrussee/caveman -a cursor` | Yes (rule file) |
| **Windsurf** | `npx skills add JuliusBrussee/caveman -a windsurf` | Yes (rule file) |
| **Cline** | `npx skills add JuliusBrussee/caveman -a cline` | Yes (rule file) |
| **GitHub Copilot** | `npx skills add JuliusBrussee/caveman -a github-copilot` | Yes (repo-wide instructions) |
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

"Soft probe" = installer won't auto-detect these without `--only <id>` because there's no reliable always-on signal. Pass the flag when you want them.

For "auto-activates? No" agents, type `/caveman` once per session (or use natural-language triggers like "talk like caveman", "caveman mode").

Full agent matrix (with detection rules) is in `bin/install.js` under the `PROVIDERS` array.

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
| `--all` | Plugin + hooks + statusline + MCP shrink + per-repo rule files in `$PWD`. The full ride. |
| `--minimal` | Plugin / extension only. No hooks, no MCP shrink, no per-repo rules. |
| `--only <id>` | One agent only. Repeatable: `--only claude --only cursor`. |
| `--dry-run` | Print every command. Write nothing. |
| `--with-init` | Drop always-on rule files into the current repo (`.cursor/`, `.windsurf/`, `.clinerules/`, `.github/copilot-instructions.md`, `AGENTS.md`). |
| `--with-mcp-shrink` | Register `caveman-shrink` MCP proxy. **On by default.** |
| `--list` | Print full agent matrix and exit. |
| `--force` | Re-run even if already installed. |
| `--uninstall` | Remove everything. See below. |

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
cat ~/.claude/.caveman-active
# expected output: full
```

If it's missing or empty, the SessionStart hook didn't fire. See troubleshooting below.

Statusline should show `[CAVEMAN]` (orange) at the bottom of Claude Code. After your first `/caveman-stats` run it appends a savings counter like `[CAVEMAN] ⛏ 12.4k`.

## Uninstall

```bash
npx -y github:JuliusBrussee/caveman -- --uninstall
```

What it removes:

- Caveman hook entries from `~/.claude/settings.json` (matched by the substring `caveman`).
- Hook files from `~/.claude/hooks/caveman-*`.
- The Claude Code plugin and the Gemini CLI extension (if installed).
- The `.caveman-active` flag file.

What it does **not** remove:

- Skills installed via `npx skills add` — the `skills` CLI manages those. Run `npx skills remove caveman` (or use your IDE's skill manager).
- Per-repo rule files written by `--with-init` (`.cursor/rules/`, `.windsurf/rules/`, `.clinerules/`, `.github/copilot-instructions.md`, `AGENTS.md`). Delete by hand if you want.

## Troubleshooting

**"I ran the installer but Claude Code isn't talking caveman."**

1. Run `node bin/install.js --list` — confirm `claude` is on the detected list. If not, `claude` isn't on `PATH`. Fix that first.
2. Open `~/.claude/settings.json` and look for `"hooks"` containing `caveman-activate.js` and `caveman-mode-tracker.js`. If missing, re-run with `--force`.
3. Check `~/.claude/.caveman-active` exists with content `full`. If not, the SessionStart hook silent-failed — check `~/.claude/hooks/` for the JS files and try `node ~/.claude/hooks/caveman-activate.js < /dev/null` to see if it errors.
4. Restart Claude Code. The SessionStart hook only fires on session start, not mid-session.

**"Hooks failing on Windows."**

- Use `install.ps1`, not `install.sh`. Git Bash works for the shell version, but the hook side wires PowerShell counterparts (`caveman-statusline.ps1`).
- PowerShell 5.1 minimum. Check with `$PSVersionTable.PSVersion`.
- If `irm | iex` blocks on execution policy: `Set-ExecutionPolicy -Scope Process -ExecutionPolicy Bypass` for the install session, then re-run.
- Long-running issues: see `docs/install-windows.md` in the repo for manual fallback.

**"My `settings.json` got mangled."**

The installer uses a JSONC-tolerant parser (`bin/lib/settings.js`) so comments and trailing commas don't crash the merge. It also runs `validateHookFields()` before every write so a malformed hook can't poison the file. If something still went wrong:

1. Check for a backup at `~/.claude/settings.json.bak` (installer writes one before any merge).
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

The installer doesn't phone home. It only writes to:

- `$CLAUDE_CONFIG_DIR` (default `~/.claude/`) — hooks, flag file, settings merge.
- The agent's own config location — Cursor's `.cursor/rules/`, Windsurf's `.windsurf/rules/`, etc.
- Your current working directory (only with `--with-init`) — repo-local rule files.

No telemetry. No analytics. No network calls beyond `npm`/`npx` fetching the `skills` CLI from npm and (during install only) `git`/`curl` fetching this repo. Source: [`bin/install.js`](bin/install.js).

---

Stuck? Open an issue: <https://github.com/JuliusBrussee/caveman/issues>
