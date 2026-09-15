# Test plan — per-session mode state

How to convince yourself the session-scoped mode patch actually works.

Three layers, cheapest first. Layer 1 catches regressions in logic, layer 2
catches regressions in the wiring between the hooks, and layer 3 catches the
things only a real Claude Code process can show you — the badge, compaction,
and the plugin's own hook registration.

---

## Layer 1 — automated suites

Run everything the CI gate runs:

```bash
npm test                                        # installer + hook unit tests
node --test --test-force-exit tests/*.js        # standalone Node suites
python3 -m unittest discover -s tests           # Python suites
python3 tests/verify_repo.py                    # repo invariants
```

`.github/workflows/ci.yml` runs the same four on ubuntu (Node 18/20/22) and
macOS, plus a `windows-powershell` job that parses every `.ps1` and drives the
standalone install/uninstall round trip through the real Git Bash.

The tests that speak directly to this patch:

| Suite | What it pins down |
|---|---|
| `tests/hooks/caveman-config.test.mjs` | Session-id validation, path containment, durable `off`, prev scoping, mode-log tagging, GC. 25 cases. |
| `tests/test_hooks.py::SessionStartSourceTests` | `source` branching — compaction and resume must not re-derive the default nor resurrect a deactivated session, but must still re-emit when one is active. |
| `tests/test_hooks.py::test_hook_never_blocks_on_stdin_that_never_closes` | The hang guard. Fails by timing out, not by asserting. |
| `tests/test_mode_tracker.py::SessionScopedModeTests` | Two windows, independent modes; the legacy mirror never holding `off`. |
| `tests/test_caveman_stats.js` | Statusline stdin parsing, including traversal ids and malformed JSON. |
| `tests/verify_repo.py::verify_powershell_static` | bash/PowerShell/JS parity, plus the hook checksum manifest. The parity greps are the only guard on the Windows *badge* — the CI Windows job covers install and parse, not badge output. |

Editing anything under `src/hooks/` means regenerating the integrity manifest,
or `verify_repo.py` fails on a checksum mismatch:

```bash
cd src/hooks && awk '{print $2}' checksums.sha256 \
  | while read -r f; do printf '%s  %s\n' "$(shasum -a 256 "$f" | awk '{print $1}')" "$f"; done \
  > /tmp/sums && mv /tmp/sums checksums.sha256
```

---

## Layer 2 — end-to-end smoke test

```bash
bash tests/manual/session-mode-smoke.sh
```

Drives the actual hook binaries with the JSON payloads Claude Code sends, against
a throwaway `CLAUDE_CONFIG_DIR`. Your real `~/.claude` is never touched. Expect
`22 passed, 0 failed`; every check prints its own name, so a failure tells you
which link in the chain broke.

What it covers, in the order a real session would hit it: startup writes a
per-session mode → "stop caveman" stores a durable `off` → compaction does not
undo that → neither does a resume → a second window keeps its own mode → each
badge shows its own window → reinforcement follows the session → compaction still
re-emits when active → traversal ids reach no file → stale files are swept on
startup but not on compact → an old install with only the legacy flag still works,
including across a compaction → a payload-less call still works → stdin without
EOF does not wedge the hook.

To watch a single step by hand, the pattern is the same throughout:

```bash
export CLAUDE_CONFIG_DIR=$(mktemp -d)
echo '{"session_id":"sess-A","source":"startup"}' | node src/hooks/caveman-activate.js
echo '{"session_id":"sess-A","prompt":"stop caveman"}' | node src/hooks/caveman-mode-tracker.js
echo '{"session_id":"sess-A"}' | bash src/hooks/caveman-statusline.sh
cat "$CLAUDE_CONFIG_DIR/.caveman-sessions/sess-A.mode"
```

Always pipe something into `caveman-activate.js`, even `< /dev/null`. Run bare in
a terminal it takes the `isTTY` branch and returns immediately; run with a pipe
that never closes and it waits for the 3000 ms payload watchdog before
activating, which looks like a hang and isn't.

---

## Layer 3 — live Claude Code

The parts no harness can fake. Wire the patched hooks into a scratch config so
your working setup stays intact — by hand, because `--config-dir` scopes the hook
files and `settings.json` but *not* `claude plugin install`, and `--only claude`
would install the plugin into your real setup on the way past:

```bash
TESTDIR=~/.claude-cavemantest
mkdir -p "$TESTDIR/hooks"
cp src/hooks/package.json src/hooks/caveman-*.js src/hooks/caveman-statusline.sh "$TESTDIR/hooks/"
NODE=$(command -v node)
cat > "$TESTDIR/settings.json" <<JSON
{
  "hooks": {
    "SessionStart":     [{ "hooks": [{ "type": "command", "command": "\"$NODE\" \"$TESTDIR/hooks/caveman-activate.js\"",     "timeout": 5 }] }],
    "UserPromptSubmit": [{ "hooks": [{ "type": "command", "command": "\"$NODE\" \"$TESTDIR/hooks/caveman-mode-tracker.js\"", "timeout": 5 }] }]
  },
  "statusLine": { "type": "command", "command": "bash $TESTDIR/hooks/caveman-statusline.sh" }
}
JSON
CLAUDE_CONFIG_DIR="$TESTDIR" claude
```

The `caveman-*.js` glob is doing real work there: it picks up `caveman-config.js`
and `caveman-parse.js`, and the tracker degrades to a silent no-op without either
— which looks like the patch broke rather than the copy being incomplete.
`cavecrew-model-overrides.js` is genuinely optional (loaded in a try/catch).

Disable the caveman plugin first if you have it installed — otherwise its hooks
run alongside these and you cannot tell which copy produced what.

**A. Two windows, two modes.** Open two Claude Code windows. In window 1 say
`/caveman ultra`, in window 2 say `/caveman lite`. Each statusline shows its own
badge, and each keeps it as you keep typing in the other. Before the patch both
badges tracked whichever window spoke last.

**B. Deactivation survives compaction.** In window 1: `stop caveman` — the badge
disappears and replies return to normal prose. Now `/compact`. The badge stays
gone and the replies stay normal. This is the defect the patch exists for.
Upstream `#691` already stopped compaction from clobbering a mid-session *level*
change, but deactivation was spelled "no flag file", so the hook found nothing
stored and re-derived the default anyway.

**C. Caveman survives compaction when it is on.** In window 2, with caveman
active, run `/compact`. Replies stay caveman. Compaction is what prunes the
ruleset out of context, so the hook must re-emit here — "don't fire on compact"
would be the wrong fix and would break exactly this.

**D. Resume.** Quit window 1 and come back with `claude --continue`. It is still
off, because `off` is now a stored value rather than an absent file, and a
`resume` reads stored state instead of re-deriving the default. Note what this
check is *not*: a brand-new window is a new `session_id` with `source: startup`,
so it legitimately starts at the configured default — durable `off` is scoped to
the session that chose it, not to the machine. `/clear` is the other deliberate
reset, since nothing else in the conversation survives it either.

**E. Token attribution.** Switch modes a few times in one window, run
`/caveman-stats`, and check the savings figure is not distorted by what the other
window was doing. `readModeLog` filters the transition log on `session_id`.

**F. Uninstall.**

```bash
node bin/install.js --uninstall --config-dir ~/.claude-cavemantest
ls -a ~/.claude-cavemantest
```

`.caveman-active`, `.caveman-active.prev`, `.caveman-mode-log.jsonl`,
`.caveman-statusline-suffix`, `.caveman-nudge-shown` and the `.caveman-sessions/`
directory are all gone. `.caveman-history.jsonl` remains on purpose — it is the
user's accumulated lifetime savings, not caveman plumbing. Then
`rm -rf ~/.claude-cavemantest`. (The same list lives in `src/hooks/uninstall.sh`
and `.ps1` — worth running one of those too if you touched it, since they are
still shipped for people who installed via the shell script.)

---

## Mixed-version installs

Worth ten minutes, because it is a real configuration: plugin hooks and standalone
hooks can both be registered at once, and `statusLine` holds an absolute path
baked in at install time. So a patched hook and an unpatched one can run against
the same state directory.

**Upgrade.** Put a bare `printf 'lite' > $CLAUDE_CONFIG_DIR/.caveman-active` in a
fresh config, with no `.caveman-sessions/`, and start a session. Caveman comes up
in `lite`, and stays `lite` through a compaction. Covered by step 10 of the smoke
test.

**Downgrade.** Check out the previous hook version over a state directory the
patched hooks wrote, and confirm the old code never sees a mode it cannot parse.
The invariant that makes this safe: the legacy mirror never holds the literal
`off`. Deactivation unlinks it. `off` is in `VALID_MODES`, so an older
`caveman-mode-tracker.js` reading `off` from that path would pass its
`!INDEPENDENT_MODES.has(…)` check and inject "CAVEMAN MODE ACTIVE (off)", and an
older `caveman-statusline.sh` would render `[CAVEMAN:OFF]`. If you ever change
how deactivation writes state, this is the first thing to re-check.

**Stale sibling.** The three hook entrypoints resolve the per-session helpers
individually rather than demanding them in their `requireSibling` shape checks,
so a `caveman-config.js` from before this patch degrades to machine-wide
behaviour instead of turning the hooks into no-ops. `tests/test_hook_missing_sibling.js`
covers the absent-file case; the stale-file case is this paragraph plus the
downgrade run above.

---

## What is not covered

- **PowerShell badge behaviour.** The CI Windows job parses every `.ps1` and
  drives install/uninstall, but nothing renders the badge, so
  `caveman-statusline.ps1` is only checked by the parity greps in
  `verify_repo.py`. Changing it means testing on Windows by hand.
- **opencode.** `src/plugins/opencode/plugin.js` still has the machine-wide
  behaviour and is out of scope: it writes its own flag in the opencode config
  dir and never sees a Claude Code `session_id`.
