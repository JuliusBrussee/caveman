# Caveman hooks for Cursor (CLI + IDE)

Cursor runs hooks from `~/.cursor/hooks.json` with a JSON contract that differs slightly from Claude Code's hook stdout format. These adapters bridge the existing caveman hook scripts to Cursor.

## Installed layout

The unified installer (`bin/install.js --only cursor`, or auto-detect when Cursor is present) writes:

```
~/.cursor/
├── hooks.json
└── hooks/
    ├── caveman-activate.js          # from src/hooks/
    ├── caveman-mode-tracker.js
    ├── caveman-config.js
    ├── caveman-stats.js
    ├── caveman-statusline.sh
    ├── caveman-session-start.js     # Cursor adapter (this directory)
    └── caveman-prompt-submit.js
```

Optional CLI badge (when `~/.cursor/cli-config.json` has no custom statusLine):

```json
{
  "statusLine": {
    "type": "command",
    "command": "bash ~/.cursor/hooks/caveman-statusline.sh"
  }
}
```

## Event mapping

| Caveman (Claude Code) | Cursor hook | Adapter output |
| --- | --- | --- |
| `SessionStart` | `sessionStart` | `{ "additional_context": "..." }` |
| `UserPromptSubmit` | `beforeSubmitPrompt` | passthrough JSON (`hookSpecificOutput`, stats block, etc.) |

Shared state uses the same flag file as Claude Code: `$CLAUDE_CONFIG_DIR/.caveman-active` (default `~/.claude/.caveman-active`).

## Manual install

From a repo clone:

```bash
node bin/install.js --only cursor
```

Or copy this directory's adapters plus `src/hooks/caveman-*.js` into `~/.cursor/hooks/` and merge `hooks.json` into `~/.cursor/hooks.json`.

## Uninstall

```bash
npx -y github:JuliusBrussee/caveman -- --uninstall
```

Removes Cursor hook entries when the unified uninstall runs. Or delete the caveman files under `~/.cursor/hooks/` and the `sessionStart` / `beforeSubmitPrompt` entries from `~/.cursor/hooks.json`.

## References

- [Cursor hooks](https://cursor.com/docs/hooks)
- [Third-party / Claude Code hook compatibility](https://cursor.com/docs/reference/third-party-hooks)
