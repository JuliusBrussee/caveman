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
| OpenClaw | OpenAI Chat Completions | Configuration file | Plugin |
| OpenCode | OpenAI Chat Completions | Configuration plus environment | Plugin |

Profiles record tested upstream versions, but upstream CLIs change independently.
Run `caveman setup` to inspect installed support before relying on a profile.

## Start an agent

```bash
caveman claude
caveman codex
caveman gemini
caveman aider
caveman hermes
caveman openclaw
caveman opencode
```

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
