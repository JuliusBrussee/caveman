#!/bin/bash
# caveman — smoke test for per-session mode state.
#
# Exercises the hooks the way Claude Code does: real JSON payloads on stdin,
# against a throwaway CLAUDE_CONFIG_DIR. Nothing touches the user's ~/.claude.
#
# Usage:  bash tests/manual/session-mode-smoke.sh
# Exit:   0 = all checks pass, 1 = at least one failed (each is printed).
#
# This complements the automated suites — it is the end-to-end path no unit
# test covers: hook binary → filesystem → statusline, in one process tree.

cd "$(dirname "$0")/../.." || exit 1

SANDBOX=$(mktemp -d)
trap 'rm -rf "$SANDBOX"' EXIT
export CLAUDE_CONFIG_DIR="$SANDBOX/cfg"
mkdir -p "$CLAUDE_CONFIG_DIR"

PASS=0
FAIL=0

check() { # check <name> <expected> <actual>
  if [ "$2" = "$3" ]; then
    printf '  \033[32mPASS\033[0m  %s\n' "$1"
    PASS=$((PASS + 1))
  else
    printf '  \033[31mFAIL\033[0m  %s\n        expected: %s\n        actual:   %s\n' "$1" "$2" "$3"
    FAIL=$((FAIL + 1))
  fi
}

activate() { printf '%s' "$1" | node src/hooks/caveman-activate.js 2>/dev/null; }
prompt()   { printf '%s' "$1" | node src/hooks/caveman-mode-tracker.js 2>/dev/null; }
badge()    { printf '%s' "$1" | bash src/hooks/caveman-statusline.sh 2>/dev/null | tr -d '\033' | sed 's/\[[0-9;]*m//g'; }
mode_of()  { cat "$CLAUDE_CONFIG_DIR/.caveman-sessions/$1.mode" 2>/dev/null || echo '<absent>'; }
legacy()   { cat "$CLAUDE_CONFIG_DIR/.caveman-active" 2>/dev/null || echo '<absent>'; }

echo
echo "Sandbox: $CLAUDE_CONFIG_DIR"
echo

echo "1. Session start writes a per-session mode"
activate '{"session_id":"sess-A","source":"startup"}' > /dev/null
check "sess-A is full"            "full" "$(mode_of sess-A)"
check "legacy mirror follows"     "full" "$(legacy)"

echo
echo "2. Deactivation is durable, and never leaks 'off' into the legacy mirror"
prompt '{"session_id":"sess-A","prompt":"stop caveman"}' > /dev/null
check "sess-A stores literal off" "off"      "$(mode_of sess-A)"
check "legacy mirror unlinked"    "<absent>" "$(legacy)"

echo
echo "3. Compaction does not resurrect a deactivated session (the original bug)"
OUT=$(activate '{"session_id":"sess-A","source":"compact"}')
check "sess-A still off"          "off" "$(mode_of sess-A)"
check "no ruleset re-emitted"     "OK"  "$OUT"

echo
echo "3b. Same for a resume — 'off' is state, not the absence of state"
OUT=$(activate '{"session_id":"sess-A","source":"resume"}')
check "resume stays off"          "OK"  "$OUT"

echo
echo "4. A second window is independent"
activate '{"session_id":"sess-B","source":"startup"}' > /dev/null
prompt '{"session_id":"sess-B","prompt":"/caveman ultra"}' > /dev/null
check "sess-A untouched"          "off"   "$(mode_of sess-A)"
check "sess-B is ultra"           "ultra" "$(mode_of sess-B)"

echo
echo "5. The statusline badge shows each window its own mode"
check "sess-A renders nothing"    ""                 "$(badge '{"session_id":"sess-A"}')"
check "sess-B renders ultra"      "[CAVEMAN:ULTRA]"  "$(badge '{"session_id":"sess-B"}')"

echo
echo "6. Per-turn reinforcement follows the session, not the machine"
check "sess-A gets none"          "" "$(prompt '{"session_id":"sess-A","prompt":"hi"}')"
case "$(prompt '{"session_id":"sess-B","prompt":"hi"}')" in
  *'CAVEMAN MODE ACTIVE (ultra)'*) check "sess-B reinforced as ultra" "yes" "yes" ;;
  *)                               check "sess-B reinforced as ultra" "yes" "no"  ;;
esac

echo
echo "7. Compaction still re-emits the ruleset when the session IS active"
case "$(activate '{"session_id":"sess-B","source":"compact"}')" in
  *'CAVEMAN MODE ACTIVE'*) check "ruleset survives compaction" "yes" "yes" ;;
  *)                       check "ruleset survives compaction" "yes" "no"  ;;
esac

echo
echo "8. A malformed session id can never reach the filesystem"
prompt '{"session_id":"../../pwned","prompt":"/caveman ultra"}' > /dev/null
check "nothing escapes the store" "0" "$(find "$SANDBOX" -name '*pwned*' | wc -l | tr -d ' ')"

echo
echo "9. Stale session files are swept on startup, not on compaction"
printf 'full' > "$CLAUDE_CONFIG_DIR/.caveman-sessions/sess-STALE.mode"
touch -t 202001010000 "$CLAUDE_CONFIG_DIR/.caveman-sessions/sess-STALE.mode"
activate '{"session_id":"sess-C","source":"compact"}' > /dev/null
check "compact leaves it alone"   "yes" "$([ -f "$CLAUDE_CONFIG_DIR/.caveman-sessions/sess-STALE.mode" ] && echo yes || echo no)"
activate '{"session_id":"sess-C","source":"startup"}' > /dev/null
check "startup sweeps it"         "no"  "$([ -f "$CLAUDE_CONFIG_DIR/.caveman-sessions/sess-STALE.mode" ] && echo yes || echo no)"

echo
echo "10. Upgrade path: an old install has only the legacy flag"
export CLAUDE_CONFIG_DIR="$SANDBOX/old"
mkdir -p "$CLAUDE_CONFIG_DIR"
printf 'lite' > "$CLAUDE_CONFIG_DIR/.caveman-active"
check "badge reads legacy flag"   "[CAVEMAN:LITE]" "$(badge '{"session_id":"sess-X"}')"
case "$(prompt '{"session_id":"sess-X","prompt":"hi"}')" in
  *'(lite)'*) check "reinforcement reads legacy flag" "yes" "yes" ;;
  *)          check "reinforcement reads legacy flag" "yes" "no"  ;;
esac
case "$(CAVEMAN_DEFAULT_MODE=ultra activate '{"session_id":"sess-X","source":"compact"}')" in
  *'level: lite'*) check "compaction keeps the legacy level" "yes" "yes" ;;
  *)               check "compaction keeps the legacy level" "yes" "no"  ;;
esac

echo
echo "11. A payload-less hook call behaves like the old machine-wide version"
export CLAUDE_CONFIG_DIR="$SANDBOX/nopayload"
mkdir -p "$CLAUDE_CONFIG_DIR"
activate '' > /dev/null
check "legacy flag still written" "full" "$(legacy)"

echo
echo "12. The hook cannot wedge on a stdin that never closes"
# The payload watchdog in caveman-activate.js is 2000ms, well inside the host's
# 5s budget: with a write end held open forever the hook must still activate on
# the watchdog and exit, not sit until the host kills it (#729/#833).
ELAPSED=$(node -e '
const { spawn } = require("child_process");
const t0 = Date.now();
const child = spawn(process.execPath, ["src/hooks/caveman-activate.js"], {
  env: { ...process.env, CLAUDE_CONFIG_DIR: process.argv[1] },
  stdio: ["pipe", "ignore", "ignore"],
});
const kill = setTimeout(() => { child.kill(); console.log("HUNG"); }, 15000);
child.on("exit", () => { clearTimeout(kill); console.log(Date.now() - t0); });
' "$CLAUDE_CONFIG_DIR")
if [ "$ELAPSED" != "HUNG" ] && [ "$ELAPSED" -lt 4500 ] 2>/dev/null; then
  check "exits under 4.5s (${ELAPSED}ms)" "yes" "yes"
else
  check "exits under 4.5s (got ${ELAPSED})" "yes" "no"
fi

echo
printf '\033[1m%s passed, %s failed\033[0m\n\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]
