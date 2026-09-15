# Skills, hooks, and plugins

Caveman integrates through response skills, lifecycle hooks, and agent-specific
plugins. Each has different permissions and failure behavior.

## Response skill

`skills/caveman` asks an agent to communicate with less filler while preserving
technical terms, code, error text, and necessary ordering. Available density
profiles include lite, full, ultra, and Wenyan variants. Auto-clarity relaxes
compression when terse phrasing could make security warnings or irreversible
steps hard to understand.

Install through supported skill installer:

```bash
npx skills add JuliusBrussee/caveman
```

Preview remote skill content before installation when source is unfamiliar.

Response skill affects generated prose. It does not compress request context or
prove a token-reduction percentage. See
[`HONEST-NUMBERS.md`](../HONEST-NUMBERS.md).

## Focused skills

| Skill | Output contract | Side effects |
|---|---|---|
| `caveman-commit` | Terse Conventional Commit message | Does not stage, commit, or amend |
| `caveman-review` | Line-scoped review findings | Does not approve, request changes, or run linters |
| `caveman-help` | One-shot command card | Does not change active mode |
| `caveman-stats` | Session usage plus labeled estimates | Reads local Claude Code session logs and writes status suffix |
| `caveman-compress` | Shortened supported memory file plus backup | Reads, validates, and writes selected local file |
| `caveman-learn` | Consent-gated review of local findings | Applies only operator-approved edits |
| `caveman-explore` | Read-only `path:line` citations | Installs a restricted explorer agent |

`caveman-stats` mixes direct session counts with explicitly estimated baseline
and rule-overhead fields. Estimated net can be negative. See evidence basis on
each line instead of treating whole output as measured.

`caveman-compress` preserves original in data-directory backup and validates
headings, code blocks, URLs, paths, and list structure. Those checks do not prove
semantic equivalence. Review diff before replacing high-stakes instructions.

See [Exploration and delegation](exploration-and-delegation.md) for explorer and
Cavecrew boundaries.

## Hooks

Hook package supports agent lifecycle events including:

- session start;
- user prompt submission;
- command or pre-tool execution;
- status-line updates.

Hooks can add mode reminders, expose recovery context, and compact command
output. They should not change user commands or hide an execution failure.

Hook state uses atomic writes and restrictive file permissions. Symlink checks
prevent a project-controlled link from redirecting trusted state writes.

Install supported native hooks with setup commands documented in
[Agent wrapping](agent-wrapping.md).

## Native plugins

Some agents do not expose equivalent hooks. Their profiles use native plugins:

- Hermes Agent plugin;
- OpenClaw plugin;
- OpenCode plugin.

OpenCode integration listens to native session and prompt events and can use
project `AGENTS.md` instructions. Plugins remain subject to host agent's plugin
permissions and version compatibility.

## Browser extension

MV3 extension adds a visible Caveman Mode directive to outgoing messages on
supported ChatGPT, Claude, and Gemini web interfaces. It runs locally and does
not send analytics or prompt content to Caveman services.

Extension injection is visible before send. It does not silently rewrite an
already-sent message. Browser site markup changes can break selectors, so
extension tests cover supported hosts and fail without submitting content.

Build and test:

```bash
npm --prefix extension run package
npm --prefix extension test
```

Load generated unpacked extension through browser extension developer mode.

## Trust checklist

Before installing a skill, hook, or plugin:

1. read source and requested permissions;
2. confirm repository and commit or release source;
3. check files it will modify;
4. verify removal command or manual rollback path;
5. test in a disposable project;
6. avoid granting shell or browser access beyond feature need.

Skills are instructions and may influence model behavior. Hooks and plugins are
code and can interact with local files or commands according to host
permissions. Treat them accordingly.

## Removing integrations

Use agent-native setup removal where available:

```bash
caveman setup --agent-native claude --remove
```

Remove skill packages through installer or agent skill manager, and remove
browser extension through browser settings. Review agent config for stale
endpoint variables afterward.
