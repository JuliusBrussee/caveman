#!/usr/bin/env bash
set -euo pipefail

if [[ $# -eq 0 ]]; then
  printf 'usage: %s <command> [args...]\n' "$0" >&2
  exit 2
fi

ORIGINAL_PATH=${PATH:-/usr/bin:/bin}
REAL_NODE=$(PATH=$ORIGINAL_PATH command -v node || true)
REAL_PYTHON3=$(PATH=$ORIGINAL_PATH command -v python3 || true)
REAL_PYTHON=$(PATH=$ORIGINAL_PATH command -v python || true)
if [[ -z "$REAL_NODE" ]]; then
  printf 'run-hermetic.sh: real node executable not found\n' >&2
  exit 2
fi

command_name=$1
shift
if [[ "$command_name" == */* ]]; then
  if [[ "$command_name" = /* ]]; then
    command_path=$command_name
  else
    command_path=$(cd "$(dirname "$command_name")" && pwd)/$(basename "$command_name")
  fi
else
  command_path=$(PATH=$ORIGINAL_PATH command -v "$command_name" || true)
fi
if [[ -z "${command_path:-}" || ! -e "$command_path" ]]; then
  printf 'run-hermetic.sh: command not found: %s\n' "$command_name" >&2
  exit 2
fi

PROTECT_HOME=${CAVEMAN_HERMETIC_PROTECT_HOME:-${HOME:?HOME is required}}
ROOT=$(mktemp -d "${TMPDIR:-/tmp}/caveman-hermetic.XXXXXX")
cleanup() {
  rm -rf "$ROOT"
}
trap cleanup EXIT INT TERM

HOME="$ROOT/home"
CLAUDE_CONFIG_DIR="$ROOT/claude"
HERMES_HOME="$ROOT/hermes"
XDG_CONFIG_HOME="$ROOT/xdg/config"
XDG_CACHE_HOME="$ROOT/xdg/cache"
XDG_DATA_HOME="$ROOT/xdg/data"
OPENCLAW_WORKSPACE="$ROOT/openclaw/workspace"
TMPDIR="$ROOT/tmp"
TMP="$TMPDIR"
TEMP="$TMPDIR"
APPDATA="$ROOT/appdata"
LOCALAPPDATA="$ROOT/localappdata"
USERPROFILE="$HOME"
CODEX_HOME="$ROOT/codex"
GEMINI_CLI_HOME="$ROOT/gemini"
OPENCODE_CONFIG_DIR="$XDG_CONFIG_HOME/opencode"
NPM_CONFIG_CACHE="$ROOT/npm-cache"
npm_config_cache="$NPM_CONFIG_CACHE"
FAKE_BIN="$ROOT/bin"
CLI_LOG="$ROOT/fake-cli.log"

mkdir -p \
  "$HOME" "$CLAUDE_CONFIG_DIR" "$HERMES_HOME" \
  "$XDG_CONFIG_HOME" "$XDG_CACHE_HOME" "$XDG_DATA_HOME" \
  "$OPENCLAW_WORKSPACE" "$TMPDIR" "$APPDATA" "$LOCALAPPDATA" \
  "$CODEX_HOME" "$GEMINI_CLI_HOME" "$OPENCODE_CONFIG_DIR" \
  "$NPM_CONFIG_CACHE" "$FAKE_BIN"

# Seed unrelated sibling content in every mutable namespace. The post-run
# snapshot proves broad uninstall/install paths did not sweep parent dirs.
printf 'claude sibling\n' > "$CLAUDE_CONFIG_DIR/.hermetic-sibling"
printf 'hermes sibling\n' > "$HERMES_HOME/.hermetic-sibling"
printf 'opencode sibling\n' > "$OPENCODE_CONFIG_DIR/.hermetic-sibling"
printf 'openclaw sibling\n' > "$OPENCLAW_WORKSPACE/.hermetic-sibling"
printf 'home sibling\n' > "$HOME/.hermetic-sibling"

cat > "$FAKE_BIN/caveman-harness-stub" <<'STUB'
#!/bin/sh
name=$(basename "$0")
printf '%s\t%s\n' "$name" "$*" >> "${CAVEMAN_HERMETIC_CLI_LOG:?}"
printf 'caveman-hermetic-stub:%s\n' "$name"
exit 0
STUB
chmod 700 "$FAKE_BIN/caveman-harness-stub"
for name in claude gemini opencode openclaw hermes npm npx; do
  ln -s caveman-harness-stub "$FAKE_BIN/$name"
done
ln -s "$REAL_NODE" "$FAKE_BIN/node"
if [[ -n "$REAL_PYTHON3" ]]; then ln -s "$REAL_PYTHON3" "$FAKE_BIN/python3"; fi
if [[ -n "$REAL_PYTHON" ]]; then ln -s "$REAL_PYTHON" "$FAKE_BIN/python"; fi

export HOME CLAUDE_CONFIG_DIR HERMES_HOME XDG_CONFIG_HOME XDG_CACHE_HOME XDG_DATA_HOME
export OPENCLAW_WORKSPACE TMPDIR TMP TEMP APPDATA LOCALAPPDATA USERPROFILE CODEX_HOME
export GEMINI_CLI_HOME OPENCODE_CONFIG_DIR NPM_CONFIG_CACHE npm_config_cache
export CAVEMAN_HERMETIC_ROOT="$ROOT"
export CAVEMAN_HERMETIC_CLI_LOG="$CLI_LOG"
export CAVEMAN_HERMETIC_PROTECT_HOME="$PROTECT_HOME"
export NO_COLOR=1 CI=1
export PATH="$FAKE_BIN:/usr/bin:/bin:/usr/sbin:/sbin"

snapshot="$ROOT/protected-before.json"
"$REAL_NODE" - "$PROTECT_HOME" "$ROOT" "$snapshot" <<'NODE'
const crypto = require('crypto');
const fs = require('fs');
const path = require('path');
const [protectHome, root, output] = process.argv.slice(2);
const paths = [
  '.claude/settings.json',
  '.hermes/config.yaml',
  '.config/opencode/opencode.json',
  '.openclaw/workspace/SOUL.md',
  '.gemini/settings.json',
  '.codex/config.toml',
].map(rel => path.join(protectHome, rel));
paths.push(
  path.join(root, 'home/.hermetic-sibling'),
  path.join(root, 'claude/.hermetic-sibling'),
  path.join(root, 'hermes/.hermetic-sibling'),
  path.join(root, 'xdg/config/opencode/.hermetic-sibling'),
  path.join(root, 'openclaw/workspace/.hermetic-sibling'),
);
function fingerprint(target) {
  try {
    const lst = fs.lstatSync(target, { bigint: true });
    const record = {
      exists: true,
      kind: lst.isSymbolicLink() ? 'symlink' : lst.isFile() ? 'file' : lst.isDirectory() ? 'dir' : 'other',
      mode: Number(lst.mode),
      mtimeNs: String(lst.mtimeNs),
    };
    if (lst.isSymbolicLink()) record.link = fs.readlinkSync(target);
    try {
      const st = fs.statSync(target);
      if (st.isFile()) {
        record.sha256 = crypto.createHash('sha256').update(fs.readFileSync(target)).digest('hex');
        record.size = st.size;
      }
    } catch (_) {}
    return record;
  } catch (error) {
    if (error && error.code === 'ENOENT') return { exists: false };
    return { exists: 'error', code: error && error.code || 'UNKNOWN' };
  }
}
const manifest = Object.fromEntries(paths.map(target => [target, fingerprint(target)]));
fs.writeFileSync(output, JSON.stringify(manifest));
NODE

set +e
"$command_path" "$@"
command_status=$?
set -e

set +e
"$REAL_NODE" - "$snapshot" <<'NODE'
const crypto = require('crypto');
const fs = require('fs');
const manifestPath = process.argv[2];
const before = JSON.parse(fs.readFileSync(manifestPath, 'utf8'));
function fingerprint(target) {
  try {
    const lst = fs.lstatSync(target, { bigint: true });
    const record = {
      exists: true,
      kind: lst.isSymbolicLink() ? 'symlink' : lst.isFile() ? 'file' : lst.isDirectory() ? 'dir' : 'other',
      mode: Number(lst.mode),
      mtimeNs: String(lst.mtimeNs),
    };
    if (lst.isSymbolicLink()) record.link = fs.readlinkSync(target);
    try {
      const st = fs.statSync(target);
      if (st.isFile()) {
        record.sha256 = crypto.createHash('sha256').update(fs.readFileSync(target)).digest('hex');
        record.size = st.size;
      }
    } catch (_) {}
    return record;
  } catch (error) {
    if (error && error.code === 'ENOENT') return { exists: false };
    return { exists: 'error', code: error && error.code || 'UNKNOWN' };
  }
}
const changed = [];
for (const [target, expected] of Object.entries(before)) {
  const actual = fingerprint(target);
  if (JSON.stringify(actual) !== JSON.stringify(expected)) changed.push(target);
}
if (changed.length) {
  for (const target of changed) console.error(`protected host file changed: ${target}`);
  process.exit(97);
}
console.log('hermetic host invariants: ok');
NODE
invariant_status=$?
set -e

if [[ $invariant_status -ne 0 ]]; then
  exit "$invariant_status"
fi
exit "$command_status"
