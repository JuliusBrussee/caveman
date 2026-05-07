#!/usr/bin/env bash
# caveman — smart multi-agent installer.
#
# One line:
#   curl -fsSL https://raw.githubusercontent.com/JuliusBrussee/caveman/main/install.sh | bash
#
# Detects which AI coding agents are on your machine and installs caveman for
# each one using its native distribution (plugin / extension / skill / rule
# file). Skips agents that aren't installed. Safe to re-run — each underlying
# install command is idempotent.
#
# Run `install.sh --help` for the full reference (flags + agent matrix).

set -euo pipefail

# ── Constants ──────────────────────────────────────────────────────────────
REPO="JuliusBrussee/caveman"
RAW_BASE="https://raw.githubusercontent.com/$REPO/main"
HOOKS_INSTALL_URL="$RAW_BASE/hooks/install.sh"
INIT_SCRIPT_URL="$RAW_BASE/tools/caveman-init.js"
MCP_SHRINK_PKG="caveman-shrink"

# ── Flags + state (no associative arrays — bash 3.2 safe) ──────────────────
# WITH_HOOKS / WITH_MCP_SHRINK default to "auto" → ON unless --minimal is set
# or the caller passed an explicit override. WITH_INIT stays opt-in because
# it writes per-repo rule files into $PWD — too surprising for bare curl|bash.
# We still probe `npm view caveman-shrink` before registration so a transient
# npm outage downgrades to a manual-snippet skip instead of a broken config.
DRY=0
FORCE=0
SKIP_SKILLS=0
WITH_HOOKS=auto
WITH_INIT=0
WITH_MCP_SHRINK=auto
ALL=0
MINIMAL=0
LIST_ONLY=0
NO_COLOR=0
ONLY=()

# Result trackers — parallel indexed arrays of agent ids and reasons.
INSTALLED_IDS=()
SKIPPED_IDS=()
SKIPPED_WHY=()
FAILED_IDS=()
FAILED_WHY=()
DETECTED_COUNT=0

# ── Color setup (auto-disable on non-TTY) ──────────────────────────────────
if [ ! -t 1 ]; then NO_COLOR=1; fi

# ── Argument parsing ───────────────────────────────────────────────────────
print_help() {
  cat <<'EOF'
caveman installer — detects your agents and installs caveman for each one.

USAGE
  install.sh [flags]

  curl -fsSL https://raw.githubusercontent.com/JuliusBrussee/caveman/main/install.sh | bash
  curl -fsSL https://raw.githubusercontent.com/JuliusBrussee/caveman/main/install.sh | bash -s -- --with-hooks

FLAGS
  --dry-run             Print what would run, do nothing.
  --force               Re-run even if a target reports "already installed".
  --only <agent>        Install only for the named agent. Repeatable.
  --skip-skills         Don't run the npx-skills auto-detect fallback.
  --all                 Turn on --with-hooks, --with-init, --with-mcp-shrink.
                        Recommended when running from inside a repo you want
                        always-on caveman in.
  --minimal             Just the plugin/extension install. Skips hooks,
                        statusline, MCP shrink, and per-repo rule files.
  --with-hooks          Claude Code: also run the standalone hooks installer
                        (SessionStart/UserPromptSubmit hooks + statusline +
                        stats badge). On by default — pass --minimal to skip.
  --with-init           Also run caveman-init against the current working
                        directory so per-repo IDE rule files are written for
                        Cursor/Windsurf/Cline/Copilot/AGENTS.md. Off by default.
  --with-mcp-shrink     Claude Code: register the caveman-shrink MCP middleware
                        proxy (or print the JSON snippet for manual setup).
                        On by default — pass --minimal to skip.
  --list                Print the full provider matrix and exit.
  --no-color            Disable ANSI color codes (auto-disabled on non-TTY).
  -h, --help            Show this help and exit.

AGENTS DETECTED
  Run with --list for the full table including detection probes. Soft-detected
  agents (config-dir-only probes) are tagged "(soft)" in --list output.

  Native:
    claude       Claude Code           plugin marketplace + plugin install
    gemini       Gemini CLI            gemini extensions install
    codex        Codex CLI             npx skills add (codex)
  IDE / VS Code-family:
    cursor       Cursor IDE            npx skills add (cursor)
    windsurf     Windsurf IDE          npx skills add (windsurf)
    cline        Cline                 npx skills add (cline)
    continue     Continue (VS Code)    npx skills add (continue)
    kilo         Kilo Code             npx skills add (kilo)
    roo          Roo Code              npx skills add (roo)
    augment      Augment Code          npx skills add (augment)
  CLI agents:
    aider-desk   Aider Desk            npx skills add (aider-desk)
    amp          Sourcegraph Amp       npx skills add (amp)
    bob          IBM Bob               npx skills add (bob)
    crush        Crush                 npx skills add (crush)
    devin        Devin (terminal)      npx skills add (devin)
    droid        Droid (Factory)       npx skills add (droid)
    forgecode    ForgeCode             npx skills add (forgecode)
    goose        Block Goose           npx skills add (goose)
    iflow        iFlow CLI             npx skills add (iflow-cli)
    junie        JetBrains Junie       npx skills add (junie)
    kiro         Kiro CLI              npx skills add (kiro-cli)
    mistral      Mistral Vibe          npx skills add (mistral-vibe)
    openhands    OpenHands             npx skills add (openhands)
    opencode     opencode              npx skills add (opencode)
    qwen         Qwen Code             npx skills add (qwen-code)
    qoder        Qoder                 npx skills add (qoder)
    rovodev      Atlassian Rovo Dev    npx skills add (rovodev)
    tabnine      Tabnine CLI           npx skills add (tabnine-cli)
    trae         Trae                  npx skills add (trae)
    warp         Warp                  npx skills add (warp)
    replit       Replit Agent          npx skills add (replit)
    antigravity  Google Antigravity    npx skills add (antigravity)
  Per-repo rule files (via --with-init / --all):
    copilot      GitHub Copilot        .github/copilot-instructions.md
    agents       AGENTS.md (Zed, etc.) AGENTS.md (universal)

URLS THE INSTALLER MAY FETCH FROM
  https://raw.githubusercontent.com/JuliusBrussee/caveman/main/install.sh
  https://raw.githubusercontent.com/JuliusBrussee/caveman/main/hooks/install.sh
  https://raw.githubusercontent.com/JuliusBrussee/caveman/main/tools/caveman-init.js
  https://github.com/JuliusBrussee/caveman   (via gemini extensions install)

EXAMPLES
  install.sh                                    # default: plugin + hooks + MCP shrink
  install.sh --all                              # also drop per-repo rule files
  install.sh --minimal                          # plugin/extension only
  install.sh --dry-run --all
  install.sh --only claude --with-mcp-shrink
  install.sh --only cursor --only windsurf --with-init
  install.sh --list
EOF
}

while [ $# -gt 0 ]; do
  case "$1" in
    --dry-run)         DRY=1 ;;
    --force)           FORCE=1 ;;
    --skip-skills)     SKIP_SKILLS=1 ;;
    --with-hooks)      WITH_HOOKS=1 ;;
    --with-init)       WITH_INIT=1 ;;
    --with-mcp-shrink) WITH_MCP_SHRINK=1 ;;
    --all)             ALL=1 ;;
    --minimal)         MINIMAL=1 ;;
    --list)            LIST_ONLY=1 ;;
    --no-color)        NO_COLOR=1 ;;
    --only)
      shift
      if [ $# -eq 0 ]; then
        echo "error: --only requires an argument" >&2
        exit 2
      fi
      # Backward-compat alias: 'aider' was renamed to 'aider-desk' to match the
      # upstream skills profile slug. Old install.sh --only aider keeps working.
      _only="$1"
      [ "$_only" = "aider" ] && _only="aider-desk"
      ONLY+=("$_only") ;;
    -h|--help)         print_help; exit 0 ;;
    *)
      echo "error: unknown flag: $1" >&2
      echo "run 'install.sh --help' for usage" >&2
      exit 2 ;;
  esac
  shift
done

# Resolve --all / --minimal / "auto" defaults into concrete flag values.
if [ "$ALL" = 1 ] && [ "$MINIMAL" = 1 ]; then
  echo "error: --all and --minimal are mutually exclusive" >&2
  exit 2
fi
if [ "$ALL" = 1 ]; then
  WITH_HOOKS=1
  WITH_INIT=1
  WITH_MCP_SHRINK=1
fi
if [ "$MINIMAL" = 1 ]; then
  WITH_HOOKS=0
  WITH_MCP_SHRINK=0
  WITH_INIT=0
fi
[ "$WITH_HOOKS" = "auto" ] && WITH_HOOKS=1
[ "$WITH_MCP_SHRINK" = "auto" ] && WITH_MCP_SHRINK=1

# ── Color helpers ──────────────────────────────────────────────────────────
if [ "$NO_COLOR" = 1 ]; then
  c_orange=""; c_dim=""; c_red=""; c_green=""; c_reset=""
else
  c_orange=$'\033[38;5;172m'
  c_dim=$'\033[2m'
  c_red=$'\033[31m'
  c_green=$'\033[32m'
  c_reset=$'\033[0m'
fi

say()  { printf '%s%s%s\n' "$c_orange" "$1" "$c_reset"; }
note() { printf '%s%s%s\n' "$c_dim" "$1" "$c_reset"; }
warn() { printf '%s%s%s\n' "$c_red" "$1" "$c_reset" >&2; }
ok()   { printf '%s%s%s\n' "$c_green" "$1" "$c_reset"; }

# ── Helpers ────────────────────────────────────────────────────────────────
want() {
  if [ ${#ONLY[@]} -eq 0 ]; then return 0; fi
  local a
  for a in "${ONLY[@]}"; do [ "$a" = "$1" ] && return 0; done
  return 1
}

run() {
  if [ "$DRY" = 1 ]; then
    note "  would run: $*"
    return 0
  fi
  echo "  $ $*"
  "$@"
}

# Run a command but never let its non-zero exit kill the script (set -e).
try() {
  if [ "$DRY" = 1 ]; then
    note "  would run: $*"
    return 0
  fi
  echo "  $ $*"
  "$@"
}

has() { command -v "$1" >/dev/null 2>&1; }

ensure_node() {
  if has node && has npx; then return 0; fi
  warn "  node + npx required for this target — install Node.js (https://nodejs.org) and re-run."
  return 1
}

# Find the local repo root (the dir containing this script) if we are NOT
# running from a curl-pipe. BASH_SOURCE[0] is unreliable when piped to bash,
# so we double-check the file actually exists and has the expected siblings.
detect_repo_root() {
  local src="${BASH_SOURCE[0]:-}"
  if [ -n "$src" ] && [ -f "$src" ]; then
    local d
    d="$(cd "$(dirname "$src")" 2>/dev/null && pwd)"
    if [ -n "$d" ] && [ -f "$d/install.sh" ] && [ -d "$d/hooks" ] && [ -d "$d/tools" ]; then
      echo "$d"
      return 0
    fi
  fi
  return 1
}

REPO_ROOT="$(detect_repo_root || true)"

# Result recorders (idempotent against double-add).
record_installed() { INSTALLED_IDS+=("$1"); }
record_skipped()   { SKIPPED_IDS+=("$1"); SKIPPED_WHY+=("$2"); }
record_failed()    { FAILED_IDS+=("$1");  FAILED_WHY+=("$2"); }

# ── Provider matrix (parallel arrays — bash 3.2 safe) ──────────────────────
# id | label | install path/notes | detection probe(s) | soft-detection?
#
# When adding a new agent: the profile slug must exist in upstream
# vercel-labs/skills (see https://github.com/vercel-labs/skills). Detection
# probes can be `command:<bin>` (binary on PATH), `dir:<path>` (directory
# exists), `file:<path>` (file exists), `vscode-ext:<needle>`,
# `cursor-ext:<needle>`, `jetbrains-config`, or `jetbrains-plugin:<needle>`.
# Multiple clauses joined by `||` — any match counts. Soft entries (PROVIDER_SOFT=1)
# rely only on dir/file probes — kept in the matrix to maximize reach but
# tagged "(soft)" in --list output so users know detection is best-effort.
PROVIDER_IDS=(
  "claude" "gemini" "codex"
  "cursor" "windsurf" "cline" "copilot" "continue" "kilo" "roo" "augment"
  "aider-desk" "amp" "bob" "crush" "devin" "droid" "forgecode" "goose"
  "iflow" "junie" "kiro" "mistral" "openhands" "opencode" "qwen" "qoder"
  "rovodev" "tabnine" "trae" "warp" "replit" "antigravity"
)
PROVIDER_LABELS=(
  "Claude Code" "Gemini CLI" "Codex CLI"
  "Cursor" "Windsurf" "Cline" "GitHub Copilot" "Continue" "Kilo Code" "Roo Code" "Augment Code"
  "Aider Desk" "Sourcegraph Amp" "IBM Bob" "Crush" "Devin (terminal)" "Droid (Factory)" "ForgeCode" "Block Goose"
  "iFlow CLI" "JetBrains Junie" "Kiro CLI" "Mistral Vibe" "OpenHands" "opencode" "Qwen Code" "Qoder"
  "Atlassian Rovo Dev" "Tabnine CLI" "Trae" "Warp" "Replit Agent" "Google Antigravity"
)
PROVIDER_MECHS=(
  "claude plugin install" "gemini extensions install" "npx skills add (codex)"
  "npx skills add (cursor)" "npx skills add (windsurf)" "npx skills add (cline)"
  "npx skills add (github-copilot)" "npx skills add (continue)" "npx skills add (kilo)"
  "npx skills add (roo)" "npx skills add (augment)"
  "npx skills add (aider-desk)" "npx skills add (amp)" "npx skills add (bob)"
  "npx skills add (crush)" "npx skills add (devin)" "npx skills add (droid)"
  "npx skills add (forgecode)" "npx skills add (goose)" "npx skills add (iflow-cli)"
  "npx skills add (junie)" "npx skills add (kiro-cli)" "npx skills add (mistral-vibe)"
  "npx skills add (openhands)" "npx skills add (opencode)" "npx skills add (qwen-code)"
  "npx skills add (qoder)" "npx skills add (rovodev)" "npx skills add (tabnine-cli)"
  "npx skills add (trae)" "npx skills add (warp)" "npx skills add (replit)"
  "npx skills add (antigravity)"
)
PROVIDER_DETECT=(
  "command:claude" "command:gemini" "command:codex"
  "command:cursor||dir:$HOME/.cursor"
  "command:windsurf||dir:$HOME/.codeium/windsurf||dir:$HOME/.windsurf"
  "vscode-ext:cline"
  "command:gh"
  "vscode-ext:continue.continue||vscode-ext:continue"
  "vscode-ext:kilocode||dir:$HOME/.kilocode"
  "vscode-ext:roo||vscode-ext:rooveterinaryinc.roo-cline||cursor-ext:roo"
  "vscode-ext:augment||jetbrains-plugin:augment"
  "command:aider||dir:$HOME/.aider-desk"
  "command:amp"
  "command:bob||dir:$HOME/.bob"
  "command:crush||dir:$HOME/.config/crush"
  "command:devin||dir:$HOME/.config/devin"
  "command:droid||dir:$HOME/.factory"
  "command:forge||dir:$HOME/.forge"
  "command:goose||dir:$HOME/.config/goose"
  "command:iflow||dir:$HOME/.iflow"
  "dir:$HOME/.junie||jetbrains-plugin:junie"
  "command:kiro||dir:$HOME/.kiro"
  "command:mistral||dir:$HOME/.vibe"
  "command:openhands||dir:$HOME/.openhands"
  "command:opencode||file:$HOME/.config/opencode/AGENTS.md"
  "command:qwen||dir:$HOME/.qwen"
  "dir:$HOME/.qoder"
  "command:rovodev||dir:$HOME/.rovodev"
  "command:tabnine||dir:$HOME/.tabnine"
  "command:trae||dir:$HOME/.trae"
  "command:warp||dir:$HOME/.warp"
  "command:replit||dir:$HOME/.replit"
  "dir:$HOME/.gemini/antigravity"
)
# Soft = no `command:` clause, only dir/file/jetbrains-plugin probes. These
# may false-positive on stale config dirs but greatly widen the reach.
PROVIDER_SOFT=(
  0 0 0
  0 0 0 0 0 0 0 0
  0 0 0 0 0 0 0 0
  0 1 0 0 0 0 0 1
  0 0 0 0 0 1
)

# ── --list output ──────────────────────────────────────────────────────────
if [ "$LIST_ONLY" = 1 ]; then
  say "🪨 caveman provider matrix"
  printf '\n  %-13s %-22s %s\n' "ID" "AGENT" "INSTALL MECHANISM"
  printf '  %-13s %-22s %s\n'   "----" "-----" "-----------------"
  i=0
  total=${#PROVIDER_IDS[@]}
  while [ $i -lt "$total" ]; do
    soft=""
    [ "${PROVIDER_SOFT[$i]:-0}" = "1" ] && soft=" (soft)"
    printf '  %-13s %-22s %s%s\n' "${PROVIDER_IDS[$i]}" "${PROVIDER_LABELS[$i]}" "${PROVIDER_MECHS[$i]}" "$soft"
    i=$((i + 1))
  done
  echo
  note "  Detection probes per agent live in install.sh PROVIDER_DETECT."
  note "  Soft entries detect via config-dir presence only (no CLI on PATH)."
  echo
  note "  Defaults: --with-hooks ON, --with-mcp-shrink ON, --with-init OFF."
  note "  --all turns all three on, --minimal turns all three off."
  exit 0
fi

# ── Detection helpers ──────────────────────────────────────────────────────
vscode_ext_present() {
  # Looks for any extension dir matching the substring across common roots.
  local needle="$1"
  local roots=("$HOME/.vscode/extensions" "$HOME/.vscode-server/extensions" "$HOME/.cursor/extensions" "$HOME/.windsurf/extensions")
  local r
  for r in "${roots[@]}"; do
    if [ -d "$r" ] && ls "$r" 2>/dev/null | grep -qi "$needle"; then
      return 0
    fi
  done
  return 1
}

cursor_ext_present() {
  local needle="$1"
  if [ -d "$HOME/.cursor/extensions" ] && ls "$HOME/.cursor/extensions" 2>/dev/null | grep -qi "$needle"; then
    return 0
  fi
  return 1
}

jetbrains_present() {
  # macOS path + Linux XDG path. Treat presence of a JetBrains config dir as
  # "JetBrains is installed" — the AI Assistant ships in most products now.
  if [ -d "$HOME/Library/Application Support/JetBrains" ]; then return 0; fi
  if [ -d "$HOME/.config/JetBrains" ]; then return 0; fi
  return 1
}

jetbrains_plugin_present() {
  local needle="$1"
  local roots=("$HOME/Library/Application Support/JetBrains" "$HOME/.config/JetBrains")
  local r
  for r in "${roots[@]}"; do
    if [ -d "$r" ] && find "$r" -maxdepth 4 -type d -iname "*${needle}*" 2>/dev/null | grep -q .; then
      return 0
    fi
  done
  return 1
}

# Parse a PROVIDER_DETECT spec like "command:foo||dir:$HOME/x" and return 0
# if any clause matches. Splits on '||' via bash parameter expansion — earlier
# revisions used `awk -v RS='||'` which silently fails on macOS BSD awk
# ("illegal primary in regular expression"), making every compound spec a
# no-op and causing the installer to detect zero of the 28 IDE/CLI agents.
detect_match() {
  local spec="$1"
  local rest="$spec"
  local clause
  while [ -n "$rest" ]; do
    if [ "${rest#*||}" != "$rest" ]; then
      clause="${rest%%||*}"
      rest="${rest#*||}"
    else
      clause="$rest"
      rest=""
    fi
    [ -z "$clause" ] && continue
    case "$clause" in
      command:*)         has "${clause#command:}" && return 0 ;;
      dir:*)             [ -d "${clause#dir:}" ] && return 0 ;;
      file:*)            [ -f "${clause#file:}" ] && return 0 ;;
      vscode-ext:*)      vscode_ext_present "${clause#vscode-ext:}" && return 0 ;;
      cursor-ext:*)      cursor_ext_present "${clause#cursor-ext:}" && return 0 ;;
      jetbrains-config)  jetbrains_present && return 0 ;;
      jetbrains-plugin:*) jetbrains_plugin_present "${clause#jetbrains-plugin:}" && return 0 ;;
    esac
  done
  return 1
}

# ──────────────────────────────────────────────────────────────────────────
say "🪨 caveman installer"
note "  $REPO"
if [ "$DRY" = 1 ]; then note "  (dry run — nothing will be written)"; fi
echo

# ── Per-agent install functions (each returns 0/1) ─────────────────────────

install_claude() {
  DETECTED_COUNT=$((DETECTED_COUNT + 1))
  say "→ Claude Code detected"
  local plugin_done=0

  if [ "$FORCE" = 0 ] && claude plugin list 2>/dev/null | grep -qi caveman; then
    note "  caveman plugin already installed (use --force to reinstall)"
    record_skipped "claude" "plugin already installed"
    plugin_done=1
  else
    if try claude plugin marketplace add "$REPO" && \
       try claude plugin install "caveman@caveman"; then
      record_installed "claude"
      plugin_done=1
    else
      record_failed "claude" "claude plugin install failed"
    fi
  fi

  # --with-hooks: also run the standalone hooks installer.
  if [ "$WITH_HOOKS" = 1 ]; then
    say "  → installing standalone hooks (--with-hooks)"
    local hooks_args=""
    [ "$FORCE" = 1 ] && hooks_args="--force"
    if [ -n "$REPO_ROOT" ] && [ -f "$REPO_ROOT/hooks/install.sh" ]; then
      if [ "$DRY" = 1 ]; then
        note "    would run: bash $REPO_ROOT/hooks/install.sh $hooks_args"
      else
        # shellcheck disable=SC2086
        if bash "$REPO_ROOT/hooks/install.sh" $hooks_args; then
          record_installed "claude-hooks"
        else
          record_failed "claude-hooks" "hooks/install.sh failed"
        fi
      fi
    else
      if ! has curl; then
        warn "    curl required to fetch hooks installer remotely"
        record_failed "claude-hooks" "curl missing"
      elif [ "$DRY" = 1 ]; then
        note "    would run: bash <(curl -fsSL $HOOKS_INSTALL_URL) $hooks_args"
      else
        # shellcheck disable=SC2086
        if bash <(curl -fsSL "$HOOKS_INSTALL_URL") $hooks_args; then
          record_installed "claude-hooks"
        else
          record_failed "claude-hooks" "remote hooks installer failed"
        fi
      fi
    fi
  fi

  # --with-mcp-shrink: register the proxy (or print the snippet). Probe the
  # npm registry first so a transient registry outage degrades to a clean
  # manual-config skip instead of registering an `npx -y caveman-shrink`
  # entry that 404s every time Claude tries to spawn it.
  if [ "$WITH_MCP_SHRINK" = 1 ]; then
    say "  → wiring caveman-shrink MCP proxy (--with-mcp-shrink)"
    if has npm && ! npm view "$MCP_SHRINK_PKG" >/dev/null 2>&1; then
      warn "    'npm view $MCP_SHRINK_PKG' returned no metadata — registry unreachable or package missing."
      note "    Skipping registration. Re-run --with-mcp-shrink when the registry is reachable,"
      note "    or copy the snippet below into your MCP config and point it at a local clone."
      record_skipped "caveman-shrink" "npm registry probe failed"
    elif has claude && claude mcp --help >/dev/null 2>&1; then
      # Newer Claude Code CLIs expose `claude mcp add`. Wrap stdio: proxy
      # spawns the upstream as a child. Without an upstream the proxy is a
      # no-op, so we register the proxy itself with a placeholder upstream
      # and tell the user how to point it at a real server.
      if [ "$DRY" = 1 ]; then
        note "    would run: claude mcp add caveman-shrink -- npx -y $MCP_SHRINK_PKG"
      else
        if try claude mcp add caveman-shrink -- npx -y "$MCP_SHRINK_PKG"; then
          record_installed "caveman-shrink"
          note "    registered. wrap an upstream by editing the mcpServers entry — see:"
          note "    https://github.com/$REPO/tree/main/mcp-servers/caveman-shrink"
        else
          record_failed "caveman-shrink" "claude mcp add failed"
        fi
      fi
    else
      note "    'claude mcp add' not available on this CLI. Add this snippet to your"
      note "    Claude Code MCP config (settings.json or .mcp.json) manually:"
      cat <<'EOF'

    {
      "mcpServers": {
        "fs-shrunk": {
          "command": "npx",
          "args": [
            "caveman-shrink",
            "npx", "@modelcontextprotocol/server-filesystem", "/path/to/dir"
          ]
        }
      }
    }

EOF
      record_skipped "caveman-shrink" "manual config required (snippet printed)"
    fi
  fi

  echo
  return 0
}

install_gemini() {
  DETECTED_COUNT=$((DETECTED_COUNT + 1))
  say "→ Gemini CLI detected"
  if [ "$FORCE" = 0 ] && gemini extensions list 2>/dev/null | grep -qi caveman; then
    note "  caveman extension already installed (use --force to reinstall)"
    record_skipped "gemini" "extension already installed"
  else
    if try gemini extensions install "https://github.com/$REPO"; then
      record_installed "gemini"
    else
      record_failed "gemini" "gemini extensions install failed"
    fi
  fi
  echo
}

install_codex() {
  DETECTED_COUNT=$((DETECTED_COUNT + 1))
  say "→ Codex CLI detected"
  if ! ensure_node; then
    record_failed "codex" "node/npx missing"
    echo
    return 0
  fi
  if try npx -y skills add "$REPO" -a codex; then
    record_installed "codex"
  else
    record_failed "codex" "npx skills add (codex) failed"
  fi
  echo
}

# Generic IDE/skills profile installer used by everything that goes through
# `npx skills add`. Pass an empty profile to use auto-detect.
install_via_skills() {
  local id="$1"
  local label="$2"
  local profile="$3"
  DETECTED_COUNT=$((DETECTED_COUNT + 1))
  say "→ $label detected"
  if ! ensure_node; then
    record_failed "$id" "node/npx missing"
    echo
    return 0
  fi
  local cmd_ok=1
  if [ -n "$profile" ]; then
    if ! try npx -y skills add "$REPO" -a "$profile"; then cmd_ok=0; fi
  else
    if ! try npx -y skills add "$REPO"; then cmd_ok=0; fi
    if [ "$cmd_ok" = 1 ]; then
      note "  used auto-detect — if your agent wasn't picked up, re-run with --only and a profile"
    fi
  fi
  if [ "$cmd_ok" = 1 ]; then
    record_installed "$id"
  else
    record_failed "$id" "npx skills add failed (profile: ${profile:-auto})"
  fi
  echo
}

# ── Run installs in declared order ─────────────────────────────────────────

# Claude: separate function (plugin + optional hooks + optional mcp-shrink).
if want claude && detect_match "command:claude"; then
  install_claude
fi

# Gemini.
if want gemini && detect_match "command:gemini"; then
  install_gemini
fi

# Codex.
if want codex && detect_match "command:codex"; then
  install_codex
fi

# IDE / agent skills targets — id, label, profile, detect spec. Profile slugs
# are validated against upstream vercel-labs/skills (see CLAUDE.md note). Add
# new rows here AND to the PROVIDER_* matrix above so --list stays accurate.
SKILLS_AGENTS=(
  "cursor|Cursor|cursor|command:cursor||dir:$HOME/.cursor"
  "windsurf|Windsurf|windsurf|command:windsurf||dir:$HOME/.codeium/windsurf||dir:$HOME/.windsurf"
  "cline|Cline|cline|vscode-ext:cline"
  "copilot|GitHub Copilot|github-copilot|command:gh"
  "continue|Continue|continue|vscode-ext:continue.continue||vscode-ext:continue"
  "kilo|Kilo Code|kilo|vscode-ext:kilocode||dir:$HOME/.kilocode"
  "roo|Roo Code|roo|vscode-ext:roo||vscode-ext:rooveterinaryinc.roo-cline||cursor-ext:roo"
  "augment|Augment Code|augment|vscode-ext:augment||jetbrains-plugin:augment"
  "aider-desk|Aider Desk|aider-desk|command:aider||dir:$HOME/.aider-desk"
  "amp|Sourcegraph Amp|amp|command:amp"
  "bob|IBM Bob|bob|command:bob||dir:$HOME/.bob"
  "crush|Crush|crush|command:crush||dir:$HOME/.config/crush"
  "devin|Devin (terminal)|devin|command:devin||dir:$HOME/.config/devin"
  "droid|Droid (Factory)|droid|command:droid||dir:$HOME/.factory"
  "forgecode|ForgeCode|forgecode|command:forge||dir:$HOME/.forge"
  "goose|Block Goose|goose|command:goose||dir:$HOME/.config/goose"
  "iflow|iFlow CLI|iflow-cli|command:iflow||dir:$HOME/.iflow"
  "junie|JetBrains Junie|junie|dir:$HOME/.junie||jetbrains-plugin:junie"
  "kiro|Kiro CLI|kiro-cli|command:kiro||dir:$HOME/.kiro"
  "mistral|Mistral Vibe|mistral-vibe|command:mistral||dir:$HOME/.vibe"
  "openhands|OpenHands|openhands|command:openhands||dir:$HOME/.openhands"
  "opencode|opencode|opencode|command:opencode||file:$HOME/.config/opencode/AGENTS.md"
  "qwen|Qwen Code|qwen-code|command:qwen||dir:$HOME/.qwen"
  "qoder|Qoder|qoder|dir:$HOME/.qoder"
  "rovodev|Atlassian Rovo Dev|rovodev|command:rovodev||dir:$HOME/.rovodev"
  "tabnine|Tabnine CLI|tabnine-cli|command:tabnine||dir:$HOME/.tabnine"
  "trae|Trae|trae|command:trae||dir:$HOME/.trae"
  "warp|Warp|warp|command:warp||dir:$HOME/.warp"
  "replit|Replit Agent|replit|command:replit||dir:$HOME/.replit"
  "antigravity|Google Antigravity|antigravity|dir:$HOME/.gemini/antigravity"
)

for spec in "${SKILLS_AGENTS[@]}"; do
  IFS='|' read -r id label profile detect_spec <<EOF
$spec
EOF
  if want "$id" && detect_match "$detect_spec"; then
    install_via_skills "$id" "$label" "$profile"
  fi
done

# ── Generic fallback: npx skills add (auto-detect) ─────────────────────────
# Only fire if (a) no --only filter, (b) skills not disabled, (c) we neither
# installed, skipped, nor failed anything detected.
if [ "$SKIP_SKILLS" = 0 ] && [ ${#ONLY[@]} -eq 0 ] && [ "$DETECTED_COUNT" -eq 0 ]; then
  say "→ no known agents detected — running npx-skills auto-detect fallback"
  if ensure_node; then
    if try npx -y skills add "$REPO"; then
      record_installed "skills-auto"
    else
      record_failed "skills-auto" "npx skills add (auto) failed"
    fi
  fi
  echo
fi

# ── --with-init: drop per-repo rule files into $PWD ────────────────────────
run_init() {
  local args=("$PWD")
  [ "$DRY" = 1 ]   && args+=("--dry-run")
  [ "$FORCE" = 1 ] && args+=("--force")

  if [ -n "$REPO_ROOT" ] && [ -f "$REPO_ROOT/tools/caveman-init.js" ]; then
    if has node; then
      try node "$REPO_ROOT/tools/caveman-init.js" "${args[@]}"
      return $?
    fi
  fi

  # Curl-pipe fallback: stream the init script into `node -`.
  if ! has node; then
    warn "  node required to run caveman-init (install Node.js: https://nodejs.org)"
    return 1
  fi
  if ! has curl; then
    warn "  curl required to fetch caveman-init remotely"
    return 1
  fi
  if [ "$DRY" = 1 ]; then
    note "  would run: curl -fsSL $INIT_SCRIPT_URL | node - ${args[*]}"
    return 0
  fi
  curl -fsSL "$INIT_SCRIPT_URL" | node - "${args[@]}"
}

if [ "$WITH_INIT" = 1 ]; then
  say "→ writing per-repo IDE rule files into $PWD (--with-init)"
  if run_init; then
    record_installed "caveman-init ($PWD)"
  else
    record_failed "caveman-init" "tools/caveman-init.js failed"
  fi
  echo
elif [ ${#INSTALLED_IDS[@]} -gt 0 ] || [ ${#SKIPPED_IDS[@]} -gt 0 ]; then
  # Friendly nudge for the per-repo flow (only when we actually did something).
  note "  tip: re-run inside a repo with --all (or --with-init) to also write per-repo"
  note "       Cursor/Windsurf/Cline/Copilot/AGENTS.md rule files."
fi

# ── Summary ────────────────────────────────────────────────────────────────
echo
say "🪨 done"

if [ ${#INSTALLED_IDS[@]} -gt 0 ]; then
  ok "  installed:"
  for a in "${INSTALLED_IDS[@]}"; do printf '    • %s\n' "$a"; done
fi

if [ ${#SKIPPED_IDS[@]} -gt 0 ]; then
  echo "  skipped:"
  i=0
  while [ $i -lt ${#SKIPPED_IDS[@]} ]; do
    printf '    • %s — %s\n' "${SKIPPED_IDS[$i]}" "${SKIPPED_WHY[$i]}"
    i=$((i + 1))
  done
fi

if [ ${#FAILED_IDS[@]} -gt 0 ]; then
  warn "  failed:"
  i=0
  while [ $i -lt ${#FAILED_IDS[@]} ]; do
    printf '    • %s — %s\n' "${FAILED_IDS[$i]}" "${FAILED_WHY[$i]}" >&2
    i=$((i + 1))
  done
fi

if [ ${#INSTALLED_IDS[@]} -eq 0 ] && [ ${#SKIPPED_IDS[@]} -eq 0 ] && [ ${#FAILED_IDS[@]} -eq 0 ]; then
  echo "  nothing detected. run 'install.sh --list' to see all 30+ supported agents"
  echo "  or pass --only <agent> to force a specific target."
fi

echo
note "  start any session and say 'caveman mode', or run /caveman in Claude Code"
note "  uninstall: see https://github.com/$REPO#install"

# Exit non-zero only when EVERY detected agent failed (and at least one was
# detected). Skips don't count as failure.
if [ "$DETECTED_COUNT" -gt 0 ] && [ ${#INSTALLED_IDS[@]} -eq 0 ] && [ ${#SKIPPED_IDS[@]} -eq 0 ]; then
  exit 1
fi
exit 0
