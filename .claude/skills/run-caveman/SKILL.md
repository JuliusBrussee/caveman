---
name: run-caveman
description: Run, launch, smoke-test, or drive the caveman plugin locally — exercise the SessionStart/UserPromptSubmit hooks, statusline badge, installer CLI, intent classifier, and test suites in a sandboxed CLAUDE_CONFIG_DIR. Use when asked to "run caveman", "test the hooks", "verify a hook change", or "smoke test the installer".
---

# Run caveman

Caveman is not a server or GUI — it's a Claude Code plugin: two Node hooks
(stdin JSON → stdout), a bash statusline script, an installer CLI, and a
per-repo init tool. "Running the app" = exercising those surfaces against a
throwaway `CLAUDE_CONFIG_DIR`. All paths below are relative to repo root.

## Run (agent path) — the smoke driver

One command exercises every surface end-to-end with assertions:

```bash
bash .claude/skills/run-caveman/smoke.sh
```

Covers: SessionStart hook (ruleset + flag write) → `/caveman ultra` mode
switch → statusline badge → natural-language deactivate → `classifyPrompt`
direct invocation → installer `--list` / `--dry-run` → `caveman-init` in a
throwaway repo. Prints `SMOKE PASS` and exits 0 on success. Writes nothing
outside `mktemp` dirs.

## Driving surfaces individually

Everything sandboxes via `CLAUDE_CONFIG_DIR`:

```bash
SB=$(mktemp -d); export CLAUDE_CONFIG_DIR=$SB

# SessionStart hook — emits full ruleset, writes flag "full"
node src/hooks/caveman-activate.js | head -5
cat $SB/.caveman-active

# UserPromptSubmit hook — stdin is JSON {"prompt": "..."}
echo '{"prompt":"/caveman ultra"}' | node src/hooks/caveman-mode-tracker.js
echo '{"prompt":"stop caveman"}'   | node src/hooks/caveman-mode-tracker.js  # deletes flag

# Statusline badge — reads flag, prints e.g. [CAVEMAN:ULTRA]
bash src/hooks/caveman-statusline.sh

# Installer CLI (read-only paths; --dry-run writes nothing)
node bin/install.js --list
node bin/install.js --dry-run --non-interactive --config-dir "$SB"
```

## Direct invocation — the layer most PRs touch

Classifier/config logic lives in `src/hooks/caveman-config.js` (plain CJS,
importable, no init guard):

```bash
node -e '
const { classifyPrompt, getDefaultMode } = require("./src/hooks/caveman-config");
console.log(classifyPrompt("talk like caveman"));  // { wantsOn: true, wantsOff: false, isQuestion: false }
console.log(getDefaultMode());                     // "full" unless config/env overrides
'
```

## Tests

```bash
npm test                                  # 112 Node installer tests, ~55s
node tests/test_mode_tracker_stdin.js     # standalone Node tests run directly
node tests/test_repo_local_config.js
uv run --with pytest pytest tests/ -q     # 66 Python tests, ~7s
```

Standalone `tests/test_*.js` files are NOT in `npm test` (it only globs
`tests/installer/*.test.mjs`) — run them directly.

## Gotchas

- `caveman-init` (`src/tools/caveman-init.js`) writes rule files into
  **`$PWD`** — always run it from inside a throwaway `git init` dir. It also
  probes the real `~/.openclaw/workspace` regardless of `CLAUDE_CONFIG_DIR`
  (that path is only overridable via `OPENCLAW_WORKSPACE`); absent workspace
  is a harmless "skipped".
- `caveman-stats` exits non-zero with `caveman-stats: no Claude Code session
  found.` outside a real session — expected in a sandbox. Via the
  mode-tracker (`{"prompt":"/caveman-stats"}`) that surfaces as the fallback
  `{"decision":"block","reason":"caveman-stats: could not run stats
  script..."}`.
- Installer without `--dry-run` touches the real machine (runs `claude plugin
  install`, `gemini extensions install`, …). `--config-dir` only scopes hook
  files + settings.json, not those. Sandbox testing → always `--dry-run`.
- Statusline prints nothing (exit 0) when the flag is absent or holds a
  non-whitelisted mode — silence is the correct "off" behavior, not a bug.
- Hooks silent-fail by design: bad stdin JSON, missing dirs, fs errors all
  exit 0. A hook "working" must be asserted via flag-file state or stdout,
  never exit code.

## Troubleshooting

- `No module named pytest` from `python3 -m pytest` → use
  `uv run --with pytest pytest tests/ -q` (uv is installed; pytest is not
  system-wide).
