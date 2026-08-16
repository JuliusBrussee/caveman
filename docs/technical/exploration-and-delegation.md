# Exploration and delegation

Broad repository search can consume main agent context before editing begins.
Caveman offers read-only explorer and compact delegation guides that keep search
transcripts separate and return only evidence needed by solver.

## FastContext-style explorer

`@caveman/explorer` supplies Claude Code agent file named `fastcontext`. It is
inspired by FastContext research but does not implement paper's trained model or
inherit paper's reported results.

Install for current project:

```bash
cave explore install
```

Install for current user:

```bash
cave explore install --user
```

`explore` is legacy alias for:

```bash
caveman tools skills install caveman-explore --no-pixel
```

Current installer supports Claude Code. Codex integration is withheld until
transcript isolation has dedicated proof.

## Permission boundary

Explorer agent has only Read, Glob, and Grep tools. It cannot edit files or run
commands. Its contract returns one verified citation per line:

```text
path/to/file.ext:START-END  reason location matters
```

Line ranges must come from files explorer read. When nothing relevant exists,
contract requires `no relevant locations found` instead of guessed citation.

## When to use explorer

Use it for cold-start localization, broad cross-file relationships, or search
that would otherwise add many file reads to solver context. Skip it when task
already names exact file or symbol, or earlier evidence provides usable location.

Isolation is mechanism, not outcome guarantee. Explorer call adds model usage.
Compare total task usage and resolution quality before claiming net benefit.

## Cavecrew

`cavecrew` is a decision guide for three compact Claude Code subagents:

| Subagent | Permission and output | Intended scope |
|---|---|---|
| Investigator | Read-only location list | Definitions, callers, tests |
| Builder | Surgical edit | One or two known files |
| Reviewer | Compact findings | Diff or file review |

Builder refuses scopes of three or more files. Main agent should own larger
refactors and cross-component decisions.

Model overrides:

```text
CAVECREW_REVIEWER_MODEL
CAVECREW_BUILDER_MODEL
CAVECREW_INVESTIGATOR_MODEL
```

Overrides patch only installed agent frontmatter model line; empty variables do
nothing. A plugin update or reinstall can replace patch.

## Optional delegate tool

CLI can register `caveman-delegate` MCP server when `execute.delegate` is enabled.
It is opt-in because delegated work consumes separate permissions and model usage.

```bash
caveman tools config set execute.delegate true
caveman tools mcp install claude --server caveman-delegate
```

Host agent and server own exact sandbox and approval behavior. Enabling feature
does not grant broader permission than host configuration allows.

## Evidence rules

- Count explorer and delegated calls in total usage.
- Do not infer complete-task savings from shorter returned prose.
- Validate citations before edits when line drift is possible.
- Keep failure and no-result cases in benchmark.
- Treat paper results as external research, not package performance.
