# GitGuardex Active Agents

Local VS Code companion for Guardex-managed repos.

## Quick Start

Use the welcome view in Source Control to create or inspect Guardex sandboxes quickly.

1. Install from a Guardex-wired repo:

```sh
node scripts/install-vscode-active-agents-extension.js
```

2. Reload the VS Code window.
3. In Source Control -> `Active Agents`, use `Start agent` to enter a task + agent name and launch the repo Guardex agent runner. The companion prefers `bash scripts/codex-agent.sh` when present, falls back to `npm run agent:codex --`, and only uses `gx branch start` as a last resort.

What it does:

- Bundles a local GitGuardex icon so repo installs show branded extension metadata inside VS Code.
- Adds an `Active Agents` view to the Source Control container.
- Renders one repo node per live Guardex workspace with grouped `ACTIVE AGENTS` and `CHANGES` sections.
- Splits live sessions inside `ACTIVE AGENTS` into `BLOCKED`, `WORKING NOW`, `THINKING`, `STALLED`, and `DEAD` groups so stuck, active, and inactive lanes stand out immediately.
- Mirrors the same live state in the VS Code status bar so the selected session or active-agent count stays visible outside the tree.
- Shows one row per live Guardex sandbox session inside those activity groups, with changed-file rows nested under sessions that are touching files.
- Shows repo-root git changes in a sibling `CHANGES` section when the guarded repo itself is dirty.
- Derives session state from dirty worktree status, git conflict markers, heartbeat freshness, PID liveness, and recent file mtimes, surfaces working/dead/conflict counts in the repo/header summary, and shows changed-file counts for active edits.
- Uses distinct VS Code codicons for each session state, including animated `loading~spin` for `WORKING NOW`.
- Reads repo-local presence files from `.omx/state/active-sessions/`, expects `lastHeartbeatAt` freshness, and falls back to managed worktree-root `AGENT.lock` telemetry when the launcher session file is absent.
- Publishes `guardex.hasAgents` and `guardex.hasConflicts` context keys for other VS Code contributions.
