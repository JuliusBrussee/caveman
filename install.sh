#!/usr/bin/env bash
set -euo pipefail

# One-liner install for Codex Desktop local plugin discovery.
#
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/yibie/caveman-codex/main/install.sh | bash
#   curl -fsSL https://raw.githubusercontent.com/yibie/caveman-codex/main/install.sh | bash -s -- --project "$PWD"
#
# What it does:
# - Vendors plugin into <project>/.codex-plugins/caveman
# - Upserts <project>/.agents/plugins/marketplace.json to reference it (relative path)
# - Prints next steps (restart Codex Desktop, install from Plugins UI)

REPO_DEFAULT="yibie/caveman-codex"
REF_DEFAULT="main"

PROJECT=""
REPO="$REPO_DEFAULT"
REF="$REF_DEFAULT"
PLUGIN_NAME="caveman"
PLUGIN_SUBDIR="plugins/caveman"
VENDOR_REL=".codex-plugins/caveman"
FORCE="0"
NO_PROMPT="0"
RAW_BASE_OVERRIDE=""

die() { echo "ERROR: $*" >&2; exit 1; }

usage() {
  cat <<'EOF'
Install caveman Codex plugin into a project.

Options:
  --project <path>   Target project root (default: current dir; if empty and macOS, prompt)
  --repo <owner/repo> GitHub repo (default: yibie/caveman-codex)
  --ref <ref>        Git ref (default: main)
  --force            Overwrite existing vendored plugin dir
  --no-prompt        Do not open folder picker when --project is missing
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --project) PROJECT="${2:-}"; shift 2 ;;
    --repo) REPO="${2:-}"; shift 2 ;;
    --ref) REF="${2:-}"; shift 2 ;;
    --raw-base) RAW_BASE_OVERRIDE="${2:-}"; shift 2 ;;
    --force) FORCE="1"; shift 1 ;;
    --no-prompt) NO_PROMPT="1"; shift 1 ;;
    -h|--help) usage; exit 0 ;;
    *) die "Unknown arg: $1 (use --help)" ;;
  esac
done

command -v curl >/dev/null 2>&1 || die "curl not found"
command -v python3 >/dev/null 2>&1 || die "python3 not found"

if [[ -z "$PROJECT" ]]; then
  # If run inside a project folder, default to PWD.
  if [[ -d .git || -f AGENTS.md || -d .agents ]]; then
    PROJECT="$PWD"
  elif [[ "$NO_PROMPT" == "1" ]]; then
    die "Missing --project"
  elif command -v osascript >/dev/null 2>&1; then
    PROJECT="$(/usr/bin/osascript -e 'POSIX path of (choose folder with prompt "Select the project folder to install the Caveman Codex plugin into:")' 2>/dev/null || true)"
  fi
fi

[[ -n "$PROJECT" ]] || die "No project selected"
PROJECT="$(cd "$PROJECT" && pwd)"

VENDOR_DIR="$PROJECT/$VENDOR_REL"
MARKETPLACE="$PROJECT/.agents/plugins/marketplace.json"
REL_SOURCE_PATH="./${VENDOR_REL}"

if [[ -e "$VENDOR_DIR" ]]; then
  if [[ "$FORCE" == "1" ]]; then
    rm -rf "$VENDOR_DIR"
  else
    die "Vendor dir exists: $VENDOR_DIR (use --force)"
  fi
fi

mkdir -p "$VENDOR_DIR/.codex-plugin" "$VENDOR_DIR/skills/$PLUGIN_NAME"

RAW_BASE="https://raw.githubusercontent.com/${REPO}/${REF}/${PLUGIN_SUBDIR}"
if [[ -n "$RAW_BASE_OVERRIDE" ]]; then
  RAW_BASE="$RAW_BASE_OVERRIDE"
fi

# If this script is executed from a local checkout (not piped from curl),
# prefer copying the plugin directory instead of downloading.
LOCAL_PLUGIN_DIR=""
if [[ -n "${BASH_SOURCE[0]:-}" && -f "${BASH_SOURCE[0]}" ]]; then
  SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
  if [[ -f "$SCRIPT_DIR/$PLUGIN_SUBDIR/.codex-plugin/plugin.json" ]]; then
    LOCAL_PLUGIN_DIR="$SCRIPT_DIR/$PLUGIN_SUBDIR"
  fi
fi

echo "Project: $PROJECT"
echo "Repo:    $REPO@$REF"
echo "Vendor:  $VENDOR_DIR"
echo ""

if [[ -n "$LOCAL_PLUGIN_DIR" ]]; then
  echo "Source:  local ($LOCAL_PLUGIN_DIR)"
  cp -R "$LOCAL_PLUGIN_DIR"/. "$VENDOR_DIR"/
else
  echo "Source:  download ($RAW_BASE)"
  curl -fsSL "$RAW_BASE/.codex-plugin/plugin.json" -o "$VENDOR_DIR/.codex-plugin/plugin.json"
  curl -fsSL "$RAW_BASE/skills/${PLUGIN_NAME}/SKILL.md" -o "$VENDOR_DIR/skills/${PLUGIN_NAME}/SKILL.md"
fi

python3 - "$MARKETPLACE" "$PLUGIN_NAME" "$REL_SOURCE_PATH" <<'PY'
import json
import os
import sys
from pathlib import Path

marketplace_path = Path(sys.argv[1])
plugin_name = sys.argv[2]
source_path = sys.argv[3]

marketplace_path.parent.mkdir(parents=True, exist_ok=True)

if marketplace_path.exists():
    try:
        data = json.loads(marketplace_path.read_text(encoding="utf-8"))
    except Exception as exc:
        raise SystemExit(f"Failed to parse {marketplace_path}: {exc}")
    if not isinstance(data, dict):
        raise SystemExit(f"Invalid marketplace root (expected object): {marketplace_path}")
else:
    data = {"name": "local-plugins", "interface": {"displayName": "Local Plugins"}, "plugins": []}

plugins = data.get("plugins")
if plugins is None:
    plugins = []
    data["plugins"] = plugins
if not isinstance(plugins, list):
    raise SystemExit("Invalid marketplace: `plugins` must be a list")

entry = {
    "name": plugin_name,
    "source": {"source": "local", "path": source_path},
    "policy": {"installation": "AVAILABLE", "authentication": "ON_INSTALL"},
    "category": "Productivity",
}

for i, p in enumerate(plugins):
    if isinstance(p, dict) and p.get("name") == plugin_name:
        plugins[i] = entry
        break
else:
    plugins.append(entry)

marketplace_path.write_text(json.dumps(data, indent=2, ensure_ascii=False) + "\n", encoding="utf-8")
print(f"Updated: {marketplace_path}")
PY

echo ""
echo "Done."
echo "Next: restart Codex Desktop, then install \"${PLUGIN_NAME}\" from the Plugins UI."
