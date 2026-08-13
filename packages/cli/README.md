# @caveman-ai/cli

The `caveman` (alias `cave`) CLI. Wrap supported coding agents so their LLM
traffic can use local metering and recoverable context compression:

```sh
caveman claude        # shorthand for `caveman wrap claude`; bootstraps signed runtime on first TTY run
caveman learn         # interactive local Setup Score + grouped top moves
caveman setup         # show which companion binaries are installed
caveman stats         # local spend, savings labeled `inferred`
```

`caveman learn` shows animated progress, compact result cards, and a keyboard
menu for implementation, full detail, or the visual report. Use `--plain` for
stable compact text, `--all` for every sink and detector id, `--json` for automation, or
`caveman learn implement [claude|codex] --prompt "<focus>"` to open an agent
that reviews fixes one by one. Agent path installs the existing learn guide when
missing, never edits load-bearing findings, and asks before every edit.

Claude Code configured for Bedrock has an explicit native Runtime lane:

```sh
CAVEMAN_WRAP_PROVIDER=bedrock \
AWS_REGION=us-east-1 \
AWS_BEARER_TOKEN_BEDROCK=… \
caveman wrap claude
```

IAM credentials (`AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`, and optional
`AWS_SESSION_TOKEN`) work too. Mantle remains opt-in with
`CAVEMAN_BEDROCK_ENDPOINT=mantle`; it is a distinct Bedrock endpoint contract,
not an alias for normal Anthropic traffic. Runtime uses the attributed
`/bedrock` base; Mantle uses `/bedrock/anthropic` because Claude Code appends
`/v1/messages` to its Mantle override.

When the gateway URL is managed, wrap merges exactly one
`x-cave-api-key: <CAVE_API_KEY>` into Claude Code's documented
`ANTHROPIC_CUSTOM_HEADERS`, preserving unrelated custom headers and replacing
any stale case variant. If `AWS_BEARER_TOKEN_BEDROCK` is present, wrap also
merges it as `x-cave-upstream-key`; otherwise a complete IAM environment is
encoded as
`AWS_ACCESS_KEY_ID:AWS_SECRET_ACCESS_KEY[:AWS_SESSION_TOKEN]`. Bearer wins when
both forms are present. Runtime still uses Claude Code's documented AWS
credential chain; there is no supported Runtime equivalent of
`CLAUDE_CODE_SKIP_MANTLE_AUTH`. With neither explicit environment form, no
upstream header is added, but Claude Code must still resolve a local AWS
credential/profile before the managed gateway can substitute the project's
stored credential. For stored-only, server-injected Claude Code auth, select the
opt-in Mantle lane, whose gateway auth bypass is documented. Stale
`x-cave-upstream-key` variants are removed so they cannot override stored
credentials. Newlines and incomplete IAM pairs fail before launch.

Local Bedrock wrap never adds Caveman gateway headers and keeps the inherited
AWS environment. This header-backed seam is repository-tested; a live Claude
Code → AWS smoke remains a release gate. See the official [custom-header reference](https://code.claude.com/docs/en/env-vars)
and [Bedrock gateway setup](https://code.claude.com/docs/en/bedrock-vertex-proxies).

## What this package is (and is not)

This npm package ships **only the JavaScript front-end** — one CLI entrypoint
plus a lazy 25 KB interactive-learn chunk, with zero runtime dependencies.
The heavy lifting (compression, metering, streaming
recovery, browsing) runs in caveman's companion **Go binaries**:

| Binary | Powers |
|---|---|
| `caveman-proxy` | `start` · `wrap` · `stats` — local compression + truthful metering |
| `caveman-engine` | `compress` · `shrink` · `retrieve` · `toon` · `evals` |
| `caveman-mcp` | agent-side recovery so streaming requests can compress |
| `cavemem` | local remember · recall · learned-context offload |
| `caveman-browse` | compressed page snapshots (optional) |
| `caveman-shrink` | `tools compress catalog` — tool-schema compression, lint, and recovery (optional) |

Two shrink surfaces stay explicit: `caveman tools shrink -- <command>` compresses
command output; `caveman tools compress catalog [lint|recover]` delegates to
`caveman-shrink` for MCP/OpenAI tool catalogs.

On first interactive local wrap, `caveman <agent>` installs signed runtime bundle
and continues same command. Explicit `CAVEMAN_*_BIN` overrides and non-interactive
runs stay unchanged. Without binaries CLI degrades affected commands to explicit
pass-through: nothing is compressed, savings report 0, and warning says why. Run
`caveman setup` to see what works and how to repair missing pieces.

`wrap` is the one exception to "pass-through": it points the agent's
`ANTHROPIC_BASE_URL`/`OPENAI_BASE_URL` at the proxy, so with no proxy listening
the agent's requests **fail to route** rather than passing through. An
interactive wrap catches this and offers to launch the agent directly instead;
a non-interactive wrap prints the warning and still launches, so scripted/CI
callers must ensure the proxy is up (or use `--no-proxy`).

## Getting the binaries

Normal install:

```sh
npm i -g @caveman-ai/cli
caveman claude
```

First interactive wrap downloads key-signed release artifacts, verifies the
checksum manifest plus every SHA-256, installs atomically into
`~/.caveman/bin`, then launches the agent. Manual install/repair is
`caveman setup --install`.

Contributors may still build from source with
`./scripts/install-local-cli.sh` on macOS/Linux or
`pwsh -File scripts/install-local-cli.ps1` on Windows. Lookup order:
`CAVEMAN_*_BIN` env override → `PATH` → `~/.caveman/bin`.

## Honesty rules

- Local savings are always labeled `inferred`; `verified_savings` stays 0 until
  an optimizer runs in active mode on real traffic.
- Recoverable compression: lossy transforms store original before replacement.
  Missing safe recovery path, parse failure, or non-smaller output passes through
  unchanged and claims nothing. `record` mode is byte-preserving.

Connected verbs (`login`, `plan`, `score`, `costs`, …) talk to Caveman Cloud
over HTTP and need no binaries.

Connected telemetry imports stay under existing `cloud audit` governance verb:

```sh
caveman cloud audit import --format langfuse traces.json
caveman cloud audit eval-import evidence.jsonl --dry-run
caveman cloud audit eval-import evidence.jsonl
```

Eval JSONL accepts one canonical external-evidence object per line, at most
1,000 records / 4 MiB per batch. Server stamps org/project, `observed` basis,
and `external_observed` authority; imported evidence cannot approve rollout or
mint verified savings.

## Agent-native setup

Install complete native Caveman integration into Codex or Claude Code: local
routing, lifecycle hooks, architecture-first Core, recovery MCP, project-scoped
read tools, and setup/discovery/review/optimization/management skills:

```sh
caveman setup --agent-native codex
# or
caveman setup --agent-native claude
```

This writes an MCP command, not an access token, into the agent config. Runtime
requests use the existing `caveman login` credential store and still pass through
control-api authentication, project scope, RBAC, audit, and tenant isolation.
Read tools expose structured reports, Cave Plan, trace metadata/spans, and
experiment evidence. Agent MCP intentionally exposes no lifecycle mutation:
control-api needs server-authoritative transition and evidence gates first.
Agent-facing CLI experiment commands are read-only for same reason.

Core changes coding behavior but remains independently controllable. Compression,
recovery, and telemetry continue when Core is off:

```sh
caveman tools config get think.core
caveman tools config set think.core off
caveman tools config set think.core on
```

Run `caveman doctor codex` or `caveman doctor claude` to see active Core state,
source, profile, and pack version. Changes affect later hook events; start a new
agent session to clear Core already delivered into model context. Aider's shallow,
static Core read cannot apply `think.core`; `caveman disable aider` removes it.
`caveman disable <agent>` removes native routing/hooks while preserving unrelated
host edits.

Agent-native setup preflights host/runtime requirements before writes, journals
bundle ownership, validates installed components, and rolls back failed installs.
Rerunning converges journal-owned drift. Remove only bundle-owned pieces and
restore prior skill/MCP bytes with:

```sh
caveman setup --agent-native codex --remove
```

Inspect before install:

```sh
caveman tools skills list --json
caveman tools skills preview caveman-evidence-review
caveman tools skills install --suite agent-native --agent codex --no-pixel
caveman tools mcp install codex --server caveman-cloud
```

Install any third-party skill through the official Skills CLI, then store its
instruction body as profitable pixel pages automatically:

```sh
caveman tools skills add mattpocock/skills --skill tdd --agent codex -y
caveman tools skills add https://github.com/vercel-labs/agent-skills --agent claude-code --global -y
```

GitHub shorthand, Git/GitLab URLs, direct skill URLs, local paths, and upstream
`--skill`/`--agent`/`--global`/`--list`/`--all` flags pass through unchanged.
Caveman forces upstream copy mode when pixelizing, converts only new or changed
Claude Code/Codex skills, keeps `SKILL.orig.md` for byte-exact revert, and
reports measured estimated-token reduction as `inferred` per invocation.
`--no-pixel` keeps upstream plain-text behavior. Third-party instructions and
resources are installed as supplied; Caveman does not security-review them.
Official Skills CLI telemetry behavior also applies; set
`DISABLE_TELEMETRY=1` to opt out upstream.

Dated local smoke on Matt Pocock's 35-skill repository converted 27 profitable
skills and left eight safely as text. Sum across one invocation of each
converted skill: 32,314 → 9,537 estimated tokens (−70% `inferred`). This is
estimator output, not provider-reported billing or a skill-quality benchmark.
