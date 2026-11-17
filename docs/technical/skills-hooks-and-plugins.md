# Skills, hooks, and plugins

Caveman reaches an agent through skills (Markdown instructions), lifecycle hooks
(small Node scripts), and agent-specific plugins. Each has different
permissions and failure behavior. This page is the maintainer reference for how
they fit together; `src/hooks/README.md` is the user-facing hook guide.

## Response skills and modes

Three self-contained skills set the voice, each mapped 1:1 to a stored mode:
`caveman` (the voice), `ultracave` (grammar stripped), `megacave` (classical
Chinese). The SessionStart hook injects `skills/<mode>/SKILL.md` whole; there is
no per-level filtering. Auto-clarity, defined in each skill, drops to normal
prose for security warnings, irreversible actions, ambiguous multi-step
sequences, confused users, and questions the agent asks. Keep it in any edit.

Stored modes (`VALID_MODES` in `src/hooks/caveman-config.js`): `off`,
`caveman`, `ultracave`, `megacave`, and the one-shot modes `commit`, `review`,
`compress`. Legacy values map forward on every read (`lite`, `full` to
`caveman`; `ultra` to `ultracave`; every `wenyan*` to `megacave`); writes emit
new ids only.

`defaultMode: "manual"` is a Claude Code startup policy, not a mode. It starts
inactive, stores only `off`, and explicit activation resolves to `caveman`. It
stays out of `VALID_MODES`; only configuration accepts it. `/caveman status` is
read-only and returns before any mode-state write.

The default mode resolves in this order: `CAVEMAN_DEFAULT_MODE`, then the
nearest repo-local `.caveman/config.json` or `.caveman.json` that names a
`defaultMode` (walking up from the working directory), then the user config
(`$XDG_CONFIG_HOME/caveman/config.json`, `~/.config/caveman/config.json`, or
`%APPDATA%\caveman\config.json` on Windows), then `caveman`. `reinforce` and
`CAVEMAN_REINFORCE` resolve the same way and only switch off the per-prompt
reminder.

Install the skills alone with:

```bash
npx skills add JuliusBrussee/caveman
```

The response skill shortens generated prose. It does not compress request
context or prove a token-reduction percentage. See
[`HONEST-NUMBERS.md`](../HONEST-NUMBERS.md).

## Focused skills

| Skill | Output contract | Side effects |
|---|---|---|
| `caveman-commit` | Terse Conventional Commit message, subject 50 characters or less | Does not stage, commit, or amend |
| `caveman-review` | One line per finding: `L<line>: <severity> <problem>. <fix>.` | Does not approve, request changes, or run linters |
| `caveman-help` | One-shot command card | Does not change active mode |
| `caveman-stats` | Session output and cache-read counts with mode attribution | Reads the transcript the host names; appends to local history |
| `caveman-compress` | Shortened memory file plus backup | Sends the file to the configured model provider; refuses files holding credentials |
| `caveman-learn` | Consent-gated review of local findings | Applies only operator-approved edits |
| `caveman-explore` | Read-only `path:line` citations | Installs a restricted explorer agent |

`caveman-compress` (Python 3.10 or newer) writes the compressed file to the
original path and keeps a backup. It validates headings, code blocks, URLs,
paths, and list structure and retries at most twice with targeted patches.
Those checks do not prove semantic equivalence.

The published CLI ships no sibling assets, so it embeds the skills it installs
(`caveman tools skills install <name>`) in
`packages/cli/src/agent-skills.generated.ts`. `skills/compile.mjs` generates
that file from `skills/*/SKILL.md` and `skills/registry.json`; run
`node packages/cli/scripts/compile-registries.mjs` after editing a skill.
`packages/cli/tests/skills.runtime.mjs` fails when an installed copy differs
from its `SKILL.md`.

See [Exploration and delegation](exploration-and-delegation.md) for the
explorer and Cavecrew boundaries.

## Plugin layout

`.claude-plugin/marketplace.json` sets `"source": "./"`, so the Claude Code
plugin root is the repository root. That has three consequences:

- **Every `skills/*/SKILL.md` ships.** There is no allowlist in `plugin.json`.
  A new directory under `skills/` installs into every plugin user's agent and
  competes for activation. Code that should not reach users belongs elsewhere.
  `tests/verify_repo.py` fails when a shipped skill is not named in a code span
  in `README.md` or `AGENTS.md`, and when a loose `skills/*.md` file exists
  (Pi's package scan would read it as a skill).
- **Every `.md` under `agents/` becomes a subagent.** `plugin.json` must not
  carry an `agents` key: on Claude Code 2.1.235 an explicit list loaded zero
  agents, and a directory string is rejected, so the default `agents/` scan is
  the only path that works. Only the three `cavecrew-*.md` files may live
  there; `tests/verify_repo.py` enforces it.
- **`commands/*.md` load as skills too.** A command stub that shares a skill's
  name registers that name twice, so `commands/` keeps one `.md`
  (`caveman-init.md`). The `.toml` stubs are for Codex and Gemini and are not
  scanned.

Cursor's plugin (`.cursor-plugin/plugin.json`) reads `skills/` and
`hooks/hooks-cursor.json`. Never add `hooks/hooks.json`: Claude Code and Gemini
load it automatically from the plugin or extension root.

`plugins/caveman/` is the CI-mirrored Claude Code plugin copy.
`.github/workflows/sync-skill.yml` runs on pushes to `main` that touch the
`caveman`, `ultracave`, `megacave`, `cavecrew`, or `caveman-compress` skills or
`agents/cavecrew-*.md`. It copies them into `plugins/caveman/`, rebuilds
`dist/caveman.skill` (a ZIP of `skills/caveman/`), and pushes a `[skip ci]`
commit as `github-actions[bot]`. `plugins/caveman/skills/caveman-stats/` is a
hand-kept copy the workflow does not touch.

`src/rules/caveman-activate.md` is generated: `skills/compile.mjs` derives it
from `skills/caveman/SKILL.md` plus the fixed tail in
`skills/activation-rule.mjs`, and rewrites the `RULE_BODY` fallback in
`src/tools/caveman-init.js` to match. Run
`node packages/cli/scripts/compile-registries.mjs` after editing either source;
`tests/installer/rule-copies.test.mjs` fails on drift.

## Claude Code hooks

| Script | Event | Job |
|---|---|---|
| `caveman-activate.js` | SessionStart | Resolve and store the session's mode, emit that skill as hidden context, offer statusline setup once |
| `caveman-activate.js --subagent` | SubagentStart | Give a subagent its parent session's mode (read-only) |
| `caveman-mode-tracker.js` | UserPromptSubmit | Slash commands and natural-language on/off, per-prompt reminder |
| `caveman-stats.js --record` | SessionEnd | Append one snapshot of the named transcript to `.caveman-history.jsonl` |
| `caveman-statusline.sh` / `.ps1` | statusline | `[CAVEMAN]`, `[ULTRACAVE]`, `[MEGACAVE]`, or `[CAVEMAN:<ONE-SHOT>]`; nothing when off |
| `caveman-host-session-start.js <host>` | Cursor and Copilot CLI sessionStart | Emit the resolved mode's skill under the host's key; writes no state |

Shared modules: `caveman-config.js` (config resolution and every mode-state
read and write) and `caveman-parse.js` (mode-change parsing).
`src/hooks/package.json` pins `"type": "commonjs"` so the hooks still load
when an ancestor `package.json` declares ES modules.

### Mode state

Each session's mode lives in
`$CLAUDE_CONFIG_DIR/.caveman-sessions/<session_id>.mode` (default
`~/.claude`). `$CLAUDE_CONFIG_DIR/.caveman-active` is a last-write-wins mirror
kept for older hooks and statuslines.

- The mirror never holds the literal `off`; deactivation deletes it. An older
  tracker or statusline would read `off` as an active mode. Mixed-version
  installs are real (plugin and standalone hooks can both be registered).
- Both spellings of off are read everywhere: a missing file and a literal `off`.
- `recordModeChange` compares against the session's own stored value
  (`readSessionModeRaw`), never the mirror, which another session may have
  written.
- Every helper accepts a missing or malformed session id and falls back to the
  machine-wide behavior. That fallback is the backward-compatibility path.
- `gcSessionStore` sweeps session files older than 14 days, and runs on new
  sessions only, never on `compact`.

### SessionStart behavior

- Only `startup` and `clear` re-derive the default mode. `compact`, `resume`,
  `fork`, unknown sources, and the stdin watchdog's `unknown` keep the stored
  mode, so "stop caveman" survives auto-compaction. A stored `off` emits
  nothing; an active mode is re-emitted because compaction prunes the rules.
- Headless sessions (`CLAUDE_CODE_ENTRYPOINT` starting with `sdk-`, such as
  `claude -p` and the Agent SDK) turn any non-`off` default into `manual`, so
  tools that parse the reply get no ruleset. An explicit `CAVEMAN_DEFAULT_MODE`
  opts back in. Hook tests drop `CLAUDE_CODE_ENTRYPOINT` from the environment.
- The SubagentStart path prints nothing for a stored `off`, a one-shot mode,
  `cavecrew-*` agents (they carry their own voice), or a repo
  `defaultMode: "off"`.

### Rules for hook code

- Return on the first complete JSON object on stdin, never at EOF, with the
  2000 ms `PAYLOAD_WATCHDOG_MS` backstop and `process.stdin.unref()` when done.
  Windows pipe close can lag past the host's whole hook budget. The readers that
  follow this: `caveman-activate.js`, `caveman-mode-tracker.js`,
  `caveman-stats.js --record`, `caveman-host-session-start.js`,
  `packages/cli/src/native-hook-fast.ts`, `readHookStdin()` in
  `packages/cli/src/index.ts`, and `readNativeHookPayload` in
  `proxy/cmd/caveman-proxy/main.go`. A new reader uses one of them and gets a
  keep-the-writer-open regression test.
- Silent-fail on every filesystem error. A hook must never block session start.
- Write flag files only through `safeWriteFlag()` (refuses symlinks, opens with
  `O_NOFOLLOW` where supported, atomic rename, mode `0600`).
- Pass every `session_id` through `validateSessionId()` before it reaches a
  path; the symlink checks do not cover traversal.
- Keep mode-state logic in `caveman-config.js`. The opencode plugin is a
  compiled Bun module that evaluates this file's source through
  `new Function(...)` with only Node built-ins, so a second home for the state
  primitives would drift.
- Honor `CLAUDE_CONFIG_DIR`; never hardcode `~/.claude`.
- The statusline scripts re-implement path resolution in shell.
  `tests/verify_repo.py` greps both for the `SESSIONS_DIRNAME` and
  `SESSION_ID_RE` constants; there is no behavioral `.ps1` test on POSIX
  runners. `read -t` takes an integer because macOS ships bash 3.2.
- Any edit under `src/hooks/` means regenerating `src/hooks/checksums.sha256`.
  `tests/verify_repo.py` checks it, and the installer verifies downloaded hooks
  against it for the pinned ref.
- `.codex/codex-sessionstart.js` carries a hand-copied `FALLBACK_VALID_MODES`;
  `tests/hooks/codex-sessionstart.test.mjs` fails if it drifts from
  `VALID_MODES`.

## Installer

`installer/install.js` is the only installer. `install.sh` and `install.ps1`
are short shims that pick a runtime (Node first, Bun only when Node is absent)
and delegate to it; never put per-OS install logic back in them. Under Bun the
installer is skills-only and refuses `--with-hooks`, `--with-mcp-shrink`, and
`--only claude`, because every hook runs Node. `install.js` runs `main()` only
when it is the entry module, so tests can `require()` it.

The standalone Claude Code install copies the hooks into
`$CLAUDE_CONFIG_DIR/hooks/` and merges SessionStart, SubagentStart,
UserPromptSubmit, SessionEnd, and the statusline into `settings.json`. Every
`settings.json` read goes through `readSettings()` in `installer/lib/settings.js`
(JSONC-tolerant), and every write runs `validateHookFields()` first: Claude Code
discards the whole file on a schema mismatch. Uninstall removes entries by the
`caveman` marker and deletes owned files; native skill copies use ownership
journals in `installer/lib/provider-skills.js`, so edited or foreign files stay.

To add an agent, add an entry to the `PROVIDERS` array in `installer/install.js`
(`id`, `label`, `mech`, `detect`, optional `profile`, optional `soft: true`).
`detect` is a clause spec such as `command:foo||dir:$HOME/x`, with `&&` for
terms that must all hold. The `profile` slug must exist in upstream
[vercel-labs/skills](https://github.com/vercel-labs/skills); a wrong slug fails
only at install time. Check the row with `node installer/install.js --list`.

## How each agent gets caveman

| Agent | Mechanism | Starts on its own |
|---|---|---|
| Claude Code | Plugin, or standalone hooks from the installer | Yes: SessionStart; SubagentStart for subagents |
| Codex | `npx skills add -a codex` plus an owned SessionStart hook in `$CODEX_HOME/hooks.json` (`.codex/codex-sessionstart.js`) | Yes, once the user trusts the hook in `/hooks` (never pre-trust it) |
| Gemini CLI | Extension with `GEMINI.md` context file | Yes |
| opencode | Native plugin in `$OPENCODE_CONFIG_DIR/plugins/caveman/` plus a fenced `AGENTS.md` block | Yes; no statusline |
| OpenClaw | Workspace skill plus a fenced block in `~/.openclaw/workspace/SOUL.md` (`installer/lib/openclaw.js`) | Yes |
| Cursor | Plugin, or `--only cursor` (skills, cavecrew agents, one sessionStart hook). Pick one, not both | Yes |
| Copilot CLI | Skills plus `$COPILOT_HOME/hooks/caveman.json` sessionStart hook | Yes |
| Grok Build | Owned skill copies plus a fenced block in `$GROK_HOME/AGENTS.md` | Per xAI docs; not run live |
| Antigravity CLI | `agy plugin install` of a staged plugin with `rules/AGENTS.md` | Yes |
| Windsurf, Cline, others | `npx skills add -a <profile>`; per-repo rules via `--with-init` | Host skill invocation or repo rule |

opencode resolves `OPENCODE_CONFIG_DIR`, then `$XDG_CONFIG_HOME/opencode`, then
`~/.config/opencode` in three separate places (CLI, installer, copied plugin);
a test pins the order in each.

`src/rules/caveman-openclaw-bootstrap.md` must keep the
`<!-- caveman-begin -->` / `<!-- caveman-end -->` markers and the
`Respond terse like smart caveman` sentinel, which `installer/lib/openclaw.js`
keys idempotency on, and stay well under OpenClaw's 12K-per-file bootstrap cap.
The fallback copy embedded in `openclaw.js` must stay byte-equivalent.

## Native plugins

Hermes Agent, OpenClaw, and OpenCode use native plugins where they lack
equivalent hooks. The OpenCode plugin (`src/plugins/opencode/plugin.js`)
listens to `session.created` and `tui.prompt.append`. Plugins remain subject to
the host agent's plugin permissions and version compatibility.

## Browser extension

The MV3 extension adds a visible Caveman Mode directive to outgoing messages on
ChatGPT, Claude, and Gemini web. It runs locally and sends no analytics or
prompt content. It never rewrites an already-sent message. Site markup changes
can break selectors, so the tests cover each supported host and fail without
submitting content.

```bash
npm --prefix extension test
npm --prefix extension run package
```

Load the unpacked extension through the browser's developer mode.

## Trust checklist

Before installing a skill, hook, or plugin:

1. read the source and the permissions it asks for;
2. confirm the repository and the commit or release;
3. check which files it will modify;
4. know the removal command or manual rollback;
5. try it in a disposable project.

Skills are instructions and can influence model behavior. Hooks and plugins are
code and act on local files and commands within the host's permissions.

## Removing integrations

```bash
npx -y github:JuliusBrussee/caveman -- --uninstall
caveman setup --agent-native claude --remove
```

Use the same vendor home overrides for install and uninstall. Remove skill
packages through the installer or the agent's skill manager, and the browser
extension through browser settings. Check agent config for stale endpoint
variables afterward.
