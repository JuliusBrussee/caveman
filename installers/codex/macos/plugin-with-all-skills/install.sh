#!/usr/bin/env bash

set -euo pipefail

REPO_URL="https://github.com/JuliusBrussee/caveman"
PLUGIN_NAME="caveman"
TARGET_PLUGIN_DIR="${HOME}/.codex/plugins/${PLUGIN_NAME}"
EXTRA_SKILLS=("caveman-commit" "caveman-help" "caveman-review")
MARKETPLACE_DIR="${HOME}/.agents/plugins"
MARKETPLACE_FILE="${MARKETPLACE_DIR}/marketplace.json"
TMP_DIR=""
INSTALL_COMPLETE=0
PLUGIN_INSTALLED=0

cleanup() {
  if [ "${INSTALL_COMPLETE}" -eq 0 ] && [ "${PLUGIN_INSTALLED}" -eq 1 ] && [ -e "${TARGET_PLUGIN_DIR}" ]; then
    rm -rf "${TARGET_PLUGIN_DIR}"
  fi

  if [ -n "${TMP_DIR}" ]; then
    rm -rf "${TMP_DIR}"
  fi
}

trap cleanup EXIT

fail() {
  echo "Error: $*" >&2
  exit 1
}

need_cmd() {
  command -v "$1" >/dev/null 2>&1 || fail "missing required command: $1"
}

need_cmd git
need_cmd python3
need_cmd mv
need_cmd mkdir
need_cmd mktemp
need_cmd rm

if [ -e "${TARGET_PLUGIN_DIR}" ]; then
  fail "${TARGET_PLUGIN_DIR} already exists. Stop before overwrite."
fi

echo "Clone caveman plugin and standalone skill subtrees..."
TMP_DIR="$(mktemp -d "${TMPDIR:-/tmp}/caveman-install.XXXXXX")"
git clone --depth 1 --filter=blob:none --sparse "${REPO_URL}" "${TMP_DIR}/repo"
git -C "${TMP_DIR}/repo" sparse-checkout set \
  "plugins/${PLUGIN_NAME}" \
  "skills/caveman-commit" \
  "skills/caveman-help" \
  "skills/caveman-review"

PLUGIN_SRC_DIR="${TMP_DIR}/repo/plugins/${PLUGIN_NAME}"

[ -f "${PLUGIN_SRC_DIR}/.codex-plugin/plugin.json" ] || fail "missing plugin manifest"
[ -f "${PLUGIN_SRC_DIR}/skills/caveman/SKILL.md" ] || fail "missing caveman skill"
[ -f "${PLUGIN_SRC_DIR}/skills/compress/SKILL.md" ] || fail "missing compress skill"
for skill in "${EXTRA_SKILLS[@]}"; do
  [ -f "${TMP_DIR}/repo/skills/${skill}/SKILL.md" ] || fail "missing standalone skill: ${skill}"
done

echo "Ensure local plugin directories exist..."
mkdir -p "${HOME}/.codex/plugins" "${MARKETPLACE_DIR}"

echo "Move plugin into Codex plugin directory..."
mv "${PLUGIN_SRC_DIR}" "${TARGET_PLUGIN_DIR}"
PLUGIN_INSTALLED=1

echo "Move companion skills into plugin skills directory..."
for skill in "${EXTRA_SKILLS[@]}"; do
  mv "${TMP_DIR}/repo/skills/${skill}" "${TARGET_PLUGIN_DIR}/skills/${skill}"
done

[ -f "${TARGET_PLUGIN_DIR}/.codex-plugin/plugin.json" ] || fail "installed manifest missing"
[ -f "${TARGET_PLUGIN_DIR}/skills/caveman/SKILL.md" ] || fail "installed caveman skill missing"
[ -f "${TARGET_PLUGIN_DIR}/skills/compress/SKILL.md" ] || fail "installed compress skill missing"
for skill in "${EXTRA_SKILLS[@]}"; do
  [ -f "${TARGET_PLUGIN_DIR}/skills/${skill}/SKILL.md" ] || fail "installed companion skill missing: ${skill}"
done

echo "Update local marketplace..."
python3 - "${MARKETPLACE_FILE}" <<'PY'
import json
import pathlib
import sys

marketplace_path = pathlib.Path(sys.argv[1])
entry = {
    "name": "caveman",
    "source": {
        "source": "local",
        "path": "./.codex/plugins/caveman",
    },
    "policy": {
        "installation": "AVAILABLE",
        "authentication": "ON_INSTALL",
    },
    "category": "Productivity",
}

if marketplace_path.exists():
    try:
        data = json.loads(marketplace_path.read_text())
    except (OSError, json.JSONDecodeError):
        raise SystemExit(f"marketplace file is not valid JSON: {marketplace_path}")
    if not isinstance(data, dict):
        raise SystemExit(f"marketplace file is not valid JSON: {marketplace_path}")
else:
    data = {
        "name": "local-plugins",
        "interface": {
            "displayName": "Local Plugins",
        },
        "plugins": [],
    }

if not data.get("name"):
    data["name"] = "local-plugins"

interface = data.setdefault("interface", {})
if not isinstance(interface, dict):
    interface = {}
    data["interface"] = interface
if not interface.get("displayName"):
    interface["displayName"] = "Local Plugins"

plugins = data.setdefault("plugins", [])
if not isinstance(plugins, list):
    raise SystemExit(f"marketplace file is not valid JSON: {marketplace_path}")
plugins = [plugin for plugin in plugins if isinstance(plugin, dict) and plugin.get("name") != "caveman"]
plugins.append(entry)
data["plugins"] = plugins

marketplace_path.write_text(json.dumps(data, indent=2) + "\n")
PY

[ -f "${MARKETPLACE_FILE}" ] || fail "marketplace file missing"

INSTALL_COMPLETE=1

echo
echo "Install complete."
echo "Next:"
echo "1. Restart Codex."
echo "2. Open plugin marketplace."
echo "3. Install 'Caveman' from 'Local Plugins'."
