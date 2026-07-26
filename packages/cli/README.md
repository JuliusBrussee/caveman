# @caveman/cli

The `caveman` (alias `cave`) CLI. Wrap any coding agent so its LLM traffic flows
through a local byte-safe compression + truthful-metering proxy:

```sh
caveman claude        # shorthand for `caveman wrap claude`
caveman setup         # show which companion binaries are installed
caveman stats         # local spend, savings labeled `inferred`
```

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

This npm package ships **only the JavaScript front-end** — a single file with
zero runtime dependencies. The heavy lifting (compression, metering, streaming
recovery, browsing) runs in caveman's companion **Go binaries**:

| Binary | Powers |
|---|---|
| `caveman-proxy` | `start` · `wrap` · `stats` — local compression + truthful metering |
| `caveman-engine` | `compress` · `shrink` · `retrieve` · `toon` · `evals` |
| `caveman-mcp` | agent-side recovery so streaming requests can compress |
| `caveman-browse` | compressed page snapshots (optional) |

Without them the CLI still works, but every affected command degrades to a
**loud, byte-safe pass-through**: nothing is compressed, savings honestly report
0, and a warning line tells you so. Run `caveman setup` any time to see exactly
what works, what doesn't, and how to fix it.

## Getting the binaries

One command, from a repo checkout (needs Go):

```sh
git clone https://github.com/JuliusBrussee/caveman
cd caveman && ./scripts/install-local-cli.sh
```

Binaries are built into `~/.caveman/bin`, which the CLI finds automatically
(lookup order: `CAVEMAN_*_BIN` env override → `PATH` → `~/.caveman/bin`).

## Honesty rules

- Local savings are always labeled `inferred`; `verified_savings` stays 0 until
  an optimizer runs in active mode on real traffic.
- Byte-safe: on any problem the CLI/proxy pass bytes through unchanged and
  claim nothing.

Connected verbs (`login`, `plan`, `score`, `costs`, …) talk to Caveman Cloud
over HTTP and need no binaries.
