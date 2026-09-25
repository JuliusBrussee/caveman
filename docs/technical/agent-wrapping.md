# Agent wrapping

Agent wrapping starts an existing coding agent with local Caveman endpoints,
hooks, skills, and recovery tools. Caveman does not replace the agent. The agent
still owns its model calls, user interface, permissions, and project workflow.

## Supported agents

| Agent | Wire protocol | Configuration method | Native extension |
|---|---|---|---|
| Aider | OpenAI Chat Completions | Environment | None |
| Claude Code | Anthropic Messages | Environment | Command and memory hooks, skills |
| Codex | OpenAI Responses | Environment | Command hook, skills |
| Gemini CLI | Gemini GenerateContent | Environment | Before-tool hook |
| Hermes Agent | OpenAI Chat Completions | Environment | Plugin |
| Kilo Code | OpenAI Chat Completions | Inline configuration via environment | None |
| OpenClaw | OpenAI Chat Completions | Configuration file | Plugin |
| OpenCode | OpenAI Chat Completions | Configuration plus environment | Plugin |
| Pi | OpenAI Chat Completions | Native extension | Native extension, skills |
| Qwen Code | OpenAI Chat Completions | Temporary system-settings overlay | None |

Profiles record tested upstream versions, but upstream CLIs change independently.
Run `caveman setup` to inspect installed support before relying on a profile.

## Start an agent

```bash
caveman claude
caveman codex
caveman gemini
caveman aider
caveman hermes
caveman kilo
caveman qwen
caveman openclaw
caveman opencode
caveman pi
```

`caveman kilocode` is an alias for the second binary published by Kilo's CLI
package. Both launch the same `kilo` profile. CLI wrapping does not reconfigure
an already-running Kilo editor extension.

Install the verified Kilo CLI version before using either shortcut:

```bash
npm install -g @kilocode/cli@7.5.6
caveman kilo
```

The wrapper confines Kilo's active providers to the injected `caveman` provider.
An explicit non-Caveman `--model`/`--m`/`-m`, top-level `attach`, `cloud`, or
`roll-call`, or `run --attach` launches directly with a warning. Attached and cloud
sessions execute outside Kilo's local routed process; `roll-call` deliberately spans
models instead of honoring one routed model.

Kilo organization and enterprise-managed config loads after inline environment
config. When either higher-priority source is active—or account state cannot be
verified safely—the wrapper launches Kilo directly instead of claiming proxy
confinement or MCP recovery that later policy could override.

Install Qwen Code's CLI before using its shortcut:

```bash
npm i -g @qwen-code/qwen-code@0.22.3
caveman qwen
```

The Qwen profile reads the existing system-settings source named by
`QWEN_CODE_SYSTEM_SETTINGS_PATH`, or the platform enterprise default when the
variable is unset: `/Library/Application Support/QwenCode/settings.json` on
macOS, `/etc/qwen-code/settings.json` on Linux, and
`%ProgramData%\qwen-code\settings.json` on Windows. It deep-merges the selected
local or managed OpenAI-compatible route into a temporary file and points Qwen
at that overlay. Enterprise policy and unrelated provider settings survive the
merge; the source system settings and `~/.qwen/settings.json` are not rewritten.
The pinned real-CLI route smoke covers Qwen Code 0.22.3 in both local and
managed modes.

Arguments after the shortcut are passed through:

```bash
caveman codex --full-auto
```

Equivalent explicit form:

```bash
caveman wrap codex --full-auto
```

For an unlisted command, use:

```bash
caveman run -- my-agent --flag value
```

Generic wrapping supplies proxy environment but cannot infer every agent's
native hook or plugin format.

## What a profile can change

Profiles are data files compiled by the CLI. A profile can declare:

- executable name and wire protocol;
- environment or configuration-file injection;
- local proxy endpoint templates;
- supported command and memory hooks;
- skills to install;
- native plugins;
- version and capability notes.

The profile compiler rejects unknown keys, unsafe paths, unsupported injection
types, reserved command collisions, and unapproved environment templates.
Supported protocols are Anthropic Messages, OpenAI Chat Completions, OpenAI
Responses, and Gemini GenerateContent.

## Modes

### Compress

Default wrapping mode compresses eligible context locally and can use TOON for
smaller structured data. Supported command output may be shrunk; lossy
transformations require recovery storage or an equivalent recovery path.

### Record

```bash
caveman wrap --off claude
```

Record mode observes local traffic without changing model-visible request
bytes. Use it to establish a baseline or troubleshoot an integration.

### Pixel

```bash
caveman wrap --pixel gemini
```

Pixel mode can encode text as an image for configured vision-capable models. It
is lossy and model-dependent. The selected model must appear in
`think.pixel.models`; Caveman does not assume image compatibility from a model
name.

## Agent-native setup

Claude Code and Codex can use explicit native setup:

```bash
caveman setup --agent-native claude
caveman setup --agent-native codex
```

Remove it with:

```bash
caveman setup --agent-native claude --remove
```

Native setup installs only files needed by that agent. Caveman hooks and plugins
keep local state under Caveman directories or agent-owned configuration paths.
Review changes before committing dotfiles or project configuration.

## Recovery during an agent run

Compressed context includes `ccr_...` handles or typed `ccr://...` pointers.
Agents with the MCP integration can retrieve exact source through a tool call.
Operators can retrieve the same source from a terminal:

```bash
caveman tools retrieve <handle>
```

Install or remove the recovery MCP registration for Qwen with:

```bash
caveman tools mcp install qwen --server caveman
caveman tools mcp uninstall qwen --server caveman
```

The installer preserves sibling entries and refuses to overwrite or remove a
Qwen MCP entry it does not own. Wrapped Qwen 0.22 sessions with that owned
registration use blocking MCP discovery so recovery is available before the
first request.

Recovery is local by default. A handle is useful only while its backing store is
available.

## Skill and hook interaction

Response skills change how an agent writes; Engine compression changes context
sent to a model. They are separate controls. Hooks can add reminders, expose
recovery tools, or compact command output. An installed skill does not prove
that request compression is active, and proxy traffic does not prove that a
response skill is active.

See [Skills, hooks, and plugins](skills-hooks-and-plugins.md) for lifecycle and
trust boundaries.

## Troubleshooting

1. Run `caveman status` and confirm selected mode.
2. Run `caveman setup` and confirm runtime binaries.
3. Start in `--off` mode. If failure remains, problem is outside request
   transformation.
4. Inspect provider credential variables without printing secret values.
5. Confirm agent uses local endpoint emitted by profile.
6. Use installed-version help because upstream profile requirements can change.

If a transform cannot parse input, cannot store recovery data, or cannot produce
smaller safe output, Caveman sends original input.

### Claude Code Remote Control

Claude Code 2.1.196 and later only allows Remote Control when
`ANTHROPIC_BASE_URL` points at `api.anthropic.com`; its first-party escape
hatch does not cover this check. A proxied session therefore cannot start
Remote Control. `caveman claude remote-control` and
`caveman wrap claude remote-control` detect the subcommand and launch Claude
Code directly, uncompressed. If `caveman enable claude` has written the route
into `settings.json`, run `caveman disable claude` before starting a Remote
Control session from a plain `claude` command, then `caveman enable claude`
again afterwards.
