#!/usr/bin/env bash

set -euo pipefail

PLUGIN_NAME="caveman"
TARGET_PLUGIN_DIR="${HOME}/.codex/plugins/${PLUGIN_NAME}"
CACHE_ROOT_DIR="${HOME}/.codex/plugins/cache"
MARKETPLACE_DIR="${HOME}/.agents/plugins"
MARKETPLACE_FILE="${MARKETPLACE_DIR}/marketplace.json"
CODEX_CONFIG_FILE="${HOME}/.codex/config.toml"
ASSUME_YES=0

fail() {
  echo "Error: $*" >&2
  exit 1
}

need_cmd() {
  command -v "$1" >/dev/null 2>&1 || fail "missing required command: $1"
}

confirm() {
  if [ "${ASSUME_YES}" -eq 1 ]; then
    return 0
  fi

  printf "Remove Caveman plugin from Codex? [y/N] "
  read -r reply
  case "${reply}" in
    y|Y|yes|YES)
      return 0
      ;;
    *)
      echo "Cancelled."
      exit 0
      ;;
  esac
}

for arg in "$@"; do
  case "${arg}" in
    --yes)
      ASSUME_YES=1
      ;;
    *)
      fail "unknown argument: ${arg}"
      ;;
  esac
done

need_cmd python3
need_cmd rm
need_cmd rmdir

shopt -s nullglob

PLUGIN_EXISTS=0
MARKETPLACE_HAS_ENTRY=0
CONFIG_HAS_ENTRY=0
CACHED_PLUGIN_EXISTS=0
CACHED_PLUGIN_DIRS=( "${CACHE_ROOT_DIR}"/*/"${PLUGIN_NAME}" )

if [ -e "${TARGET_PLUGIN_DIR}" ]; then
  PLUGIN_EXISTS=1
fi

if [ "${#CACHED_PLUGIN_DIRS[@]}" -gt 0 ]; then
  CACHED_PLUGIN_EXISTS=1
fi

if [ -f "${MARKETPLACE_FILE}" ]; then
  if python3 - "${MARKETPLACE_FILE}" <<'PY'
import json
import sys

path = sys.argv[1]
try:
    data = json.load(open(path))
except Exception:
    raise SystemExit(2)

for plugin in data.get("plugins", []):
    if plugin.get("name") == "caveman":
        raise SystemExit(0)

raise SystemExit(1)
PY
  then
    MARKETPLACE_HAS_ENTRY=1
  else
    status=$?
    if [ "${status}" -eq 2 ]; then
      fail "marketplace file is not valid JSON: ${MARKETPLACE_FILE}"
    fi
  fi
fi

if [ -f "${CODEX_CONFIG_FILE}" ]; then
  if python3 - "${CODEX_CONFIG_FILE}" <<'PY'
import re
import sys

path = sys.argv[1]
pattern = re.compile(r'^\[plugins\."caveman@[^"]+"\]$')

with open(path, "r", encoding="utf-8") as f:
    for line in f:
        if pattern.match(line.strip()):
            raise SystemExit(0)

raise SystemExit(1)
PY
  then
    CONFIG_HAS_ENTRY=1
  fi
fi

if [ "${PLUGIN_EXISTS}" -eq 0 ] && [ "${MARKETPLACE_HAS_ENTRY}" -eq 0 ] && [ "${CONFIG_HAS_ENTRY}" -eq 0 ] && [ "${CACHED_PLUGIN_EXISTS}" -eq 0 ]; then
  echo "Caveman not installed. Nothing to do."
  exit 0
fi

confirm

if [ -f "${MARKETPLACE_FILE}" ]; then
  echo "Update marketplace..."
  python3 - "${MARKETPLACE_FILE}" <<'PY'
import json
import pathlib
import sys

marketplace_path = pathlib.Path(sys.argv[1])
data = json.loads(marketplace_path.read_text())
plugins = [plugin for plugin in data.get("plugins", []) if plugin.get("name") != "caveman"]
data["plugins"] = plugins

delete_file = (
    not plugins
    and data.get("name") == "local-plugins"
    and data.get("interface", {}).get("displayName") == "Local Plugins"
)

if delete_file:
    marketplace_path.unlink()
else:
    marketplace_path.write_text(json.dumps(data, indent=2) + "\n")
PY
fi

if [ -f "${CODEX_CONFIG_FILE}" ]; then
  echo "Update Codex config..."
  python3 - "${CODEX_CONFIG_FILE}" <<'PY'
import pathlib
import re
import sys

config_path = pathlib.Path(sys.argv[1])
pattern = re.compile(r'^\[plugins\."caveman@[^"]+"\]$')
lines = config_path.read_text().splitlines(keepends=True)

result = []
skip = False
for line in lines:
    stripped = line.strip()
    if not skip and pattern.match(stripped):
        skip = True
        continue
    if skip and stripped.startswith("["):
        skip = False
    if not skip:
        result.append(line)

config_path.write_text("".join(result))
PY
fi

if [ -e "${TARGET_PLUGIN_DIR}" ]; then
  echo "Remove plugin directory..."
  rm -rf "${TARGET_PLUGIN_DIR}"
fi

for cached_plugin_dir in "${CACHED_PLUGIN_DIRS[@]}"; do
  if [ -e "${cached_plugin_dir}" ]; then
    echo "Remove installed plugin cache ${cached_plugin_dir}..."
    rm -rf "${cached_plugin_dir}"
  fi
done

if [ -d "${MARKETPLACE_DIR}" ] && [ -z "$(ls -A "${MARKETPLACE_DIR}")" ]; then
  rmdir "${MARKETPLACE_DIR}"
fi

for cached_plugin_dir in "${CACHED_PLUGIN_DIRS[@]}"; do
  cached_marketplace_dir="$(dirname "${cached_plugin_dir}")"
  if [ -d "${cached_marketplace_dir}" ] && [ -z "$(ls -A "${cached_marketplace_dir}")" ]; then
    rmdir "${cached_marketplace_dir}"
  fi
done

if [ -d "${HOME}/.agents" ] && [ -z "$(ls -A "${HOME}/.agents")" ]; then
  rmdir "${HOME}/.agents"
fi

if [ -e "${TARGET_PLUGIN_DIR}" ]; then
  fail "plugin directory still exists: ${TARGET_PLUGIN_DIR}"
fi

for cached_plugin_dir in "${CACHED_PLUGIN_DIRS[@]}"; do
  if [ -e "${cached_plugin_dir}" ]; then
    fail "installed cache still exists: ${cached_plugin_dir}"
  fi
done

if [ -f "${MARKETPLACE_FILE}" ]; then
  if python3 - "${MARKETPLACE_FILE}" <<'PY'
import json
import sys

data = json.load(open(sys.argv[1]))
for plugin in data.get("plugins", []):
    if plugin.get("name") == "caveman":
        raise SystemExit(1)
raise SystemExit(0)
PY
  then
    :
  else
    fail "marketplace still contains caveman entry"
  fi
fi

if [ -f "${CODEX_CONFIG_FILE}" ]; then
  if python3 - "${CODEX_CONFIG_FILE}" <<'PY'
import re
import sys

path = sys.argv[1]
pattern = re.compile(r'^\[plugins\."caveman@[^"]+"\]$')

with open(path, "r", encoding="utf-8") as f:
    for line in f:
        if pattern.match(line.strip()):
            raise SystemExit(1)

raise SystemExit(0)
PY
  then
    :
  else
    fail "Codex config still contains caveman plugin entry"
  fi
fi

echo
echo "Uninstall complete."
echo "Restart Codex if plugin still appears in current session."
