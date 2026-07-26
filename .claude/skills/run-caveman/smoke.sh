#!/usr/bin/env bash
# caveman smoke driver — launches every user-facing surface in a sandboxed
# CLAUDE_CONFIG_DIR and asserts the full hook lifecycle end-to-end.
#
# Usage (from repo root):
#   bash .claude/skills/run-caveman/smoke.sh
#
# Exits 0 when every assertion passes. No writes outside mktemp dirs
# (except caveman-init's ~/.openclaw probe, which read-only skips when the
# workspace is absent).
set -u
cd "$(dirname "$0")/../../.."   # repo root

FAIL=0
note() { printf '  %s\n' "$1"; }
check() { # check <desc> <expected> <actual>
  if [ "$2" = "$3" ]; then note "✓ $1"; else note "✗ $1 — expected [$2] got [$3]"; FAIL=1; fi
}

SB=$(mktemp -d)
export CLAUDE_CONFIG_DIR="$SB"
trap 'rm -rf "$SB"' EXIT

echo "== 1. SessionStart hook (caveman-activate.js) =="
OUT=$(node src/hooks/caveman-activate.js)
case "$OUT" in "CAVEMAN MODE ACTIVE — level: full"*) note "✓ ruleset emitted";; *) note "✗ ruleset missing"; FAIL=1;; esac
check "flag file written" "full" "$(cat "$SB/.caveman-active" 2>/dev/null)"

echo "== 2. Mode switch via UserPromptSubmit (/caveman ultra) =="
OUT=$(echo '{"prompt":"/caveman ultra"}' | node src/hooks/caveman-mode-tracker.js)
check "flag switched" "ultra" "$(cat "$SB/.caveman-active" 2>/dev/null)"
case "$OUT" in *'CAVEMAN MODE ACTIVE (ultra)'*) note "✓ reinforcement JSON emitted";; *) note "✗ no reinforcement output"; FAIL=1;; esac

echo "== 3. Statusline badge =="
BADGE=$(bash src/hooks/caveman-statusline.sh)
case "$BADGE" in *'[CAVEMAN:ULTRA]'*) note "✓ badge renders [CAVEMAN:ULTRA]";; *) note "✗ badge wrong: $BADGE"; FAIL=1;; esac

echo "== 4. Natural-language deactivate =="
echo '{"prompt":"stop caveman"}' | node src/hooks/caveman-mode-tracker.js >/dev/null
check "flag deleted" "no" "$(test -f "$SB/.caveman-active" && echo yes || echo no)"
check "statusline silent when off" "" "$(bash src/hooks/caveman-statusline.sh)"

echo "== 5. Intent classifier (direct invocation — the layer most PRs touch) =="
node -e '
const { classifyPrompt } = require("./src/hooks/caveman-config");
const cases = [
  ["talk like caveman",                       {on: true,  off: false}],
  ["dont use caveman",                        {on: false, off: false}],
  ["what is caveman mode vs normal mode?",    {on: false, off: false}],
  ["stop caveman",                            {on: false, off: true }],
];
let fail = 0;
for (const [p, want] of cases) {
  const got = classifyPrompt(p);
  const ok = got.wantsOn === want.on && got.wantsOff === want.off;
  console.log(`  ${ok ? "✓" : "✗"} classifyPrompt(${JSON.stringify(p)}) → on=${got.wantsOn} off=${got.wantsOff}`);
  if (!ok) fail = 1;
}
process.exit(fail);
' || FAIL=1

echo "== 6. Installer CLI (read-only paths) =="
node bin/install.js --list >/dev/null 2>&1 && note "✓ --list exits 0" || { note "✗ --list failed"; FAIL=1; }
DRY=$(node bin/install.js --dry-run --non-interactive --config-dir "$SB" 2>&1)
case "$DRY" in *'dry run — nothing will be written'*) note "✓ --dry-run runs, writes nothing";; *) note "✗ --dry-run output unexpected"; FAIL=1;; esac

echo "== 7. caveman-init in throwaway repo =="
T=$(mktemp -d); REPO=$PWD
( cd "$T" && git init -q . && node "$REPO/src/tools/caveman-init.js" >/dev/null 2>&1 )
check "cursor rule written" "yes" "$(test -f "$T/.cursor/rules/caveman.mdc" && echo yes || echo no)"
check "AGENTS.md written"   "yes" "$(test -f "$T/AGENTS.md" && echo yes || echo no)"
rm -rf "$T"

echo
if [ "$FAIL" = 0 ]; then echo "SMOKE PASS"; else echo "SMOKE FAIL"; fi
exit "$FAIL"
