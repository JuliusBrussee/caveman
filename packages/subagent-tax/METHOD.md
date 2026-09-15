# Method — how subagent-tax measures, and what the numbers mean

**Basis: `inferred`, always.** Nothing here is provider-billed usage, spend, or
savings of any kind. This tool captures one thing: the size of the first
agent-turn request each installed coding harness sends — the prefix (system
prompt + tool schemas) that re-ships with every call a subagent makes.

The word *verified* is deliberately absent from this tool's vocabulary: in this
repo it is a reserved savings-accounting term. Recipes and conventions that have been checked
are called **confirmed**.

## What runs

1. A local HTTP **sink** (`lib/sink.mjs`) binds a loopback port and
   impersonates a provider endpoint across four wire protocols
   (anthropic-messages, openai-responses, openai-chat,
   gemini-generatecontent), JSON and SSE. It replies with a minimal, valid
   "DONE" completion so the harness ends its turn instead of retrying.
2. Each harness is launched one-shot (`lib/harnesses.mjs`) with its LLM
   traffic redirected to the sink. Every request it sends is captured to disk,
   redacted at write time. Inherited HTTP proxy variables are neutralized for
   the child and loopback is added to `NO_PROXY`, so a corporate proxy cannot
   receive the prefix.
3. After the first captures land (plus a quiet grace period) the harness's
   process tree is stopped. POSIX uses a detached process group; Windows uses
   `taskkill /t`. Measurement succeeds on capture; the harness
   completing its turn is not required, and a harness that never exits is
   still measured.
4. The analyzer (`lib/analyze.mjs`) splits the **primary** request into
   system chars, per-tool schema chars, message overhead, and — where the
   convention is confirmed — an MCP-vs-builtin tool split.

**Primary selection rule:** the capture carrying the **most tool schemas**,
ties broken toward the earliest. Harnesses interleave small warmup, title, and
router calls with the real agent turn, and some of those carry a tool or two —
"first request with any tools" would hand back a router call's few hundred
bytes as if it were the prefix. If no capture carries tools, the largest body
is used and the row's `pick_rule` says so. All captures ship in the repro pack
either way.

**Malformed captures are skipped, never fatal.** A body whose `tools`,
`messages`, or `input` arrives in an unexpected shape is recorded in
`skipped_captures` with its error rather than silently counted as zero tools,
and the other harnesses' measurements survive.

## Config handling — what "never touches your config" does and doesn't mean

No recipe **modifies** the user's configuration files. That is not the same as
running in isolation:

- **claude** deliberately runs against the **real config**, because the user's
  own plugins and MCP servers are the tax being measured. Launching the real
  binary in the real environment boots those MCP servers, runs their hooks, and
  leaves a session transcript in `~/.claude/projects`. The tool prints this
  before it launches anything.
- Every other harness runs against an **isolated, ephemeral** home/config dir.
- `--isolate` switches claude to an isolated `CLAUDE_CONFIG_DIR` too. Note that
  Claude Code's login lives in that directory, so an isolated run typically
  exits `Not logged in` and reports no capture — the harness's own first line
  of output is surfaced as the reason.

## Network promise

The measurement makes **no provider API calls**. Two explicit exceptions:

- `--count-tokens` (opt-in) posts captured anthropic-protocol bodies — your
  real system prompt — to Anthropic's free `count_tokens` endpoint to upgrade
  those rows from estimated to provider-exact. Requires `ANTHROPIC_API_KEY`;
  warns and stays estimated without one; failures are labeled
  `est (count_tokens failed)` rather than silently downgraded.
- Harnesses may attempt their own side traffic (telemetry, update checks).
  Recipes suppress what can be suppressed; the codex recipe deliberately uses a
  minimal ephemeral home because its real config triggers plugin git clones and
  MCP OAuth handshakes.

## Token accounting — two rungs, never blended

- **`est`** — chars ÷ ratio (default 6.4), printed to 2 significant figures
  with a `~`. Calibrated 2026-08-07 against provider-exact Claude Code prefix
  runs on one machine (observed 5.9–6.9 chars/token, a ±8% band), so further
  digits would be noise. **The calibration is Anthropic-tokenizer-derived and
  is applied to every protocol**, so codex/opencode (o200k) and gemini rows
  carry an extra, unquantified cross-tokenizer error on top of the ±8%. Treat
  cross-harness token comparisons as approximate; the char columns are exact.
- **`exact`** — Anthropic `count_tokens` on the captured body, verbatim.
  Anthropic-protocol rows only; other providers' tokenizers are never
  approximated as exact.

## Variants — rows are labeled, not mixed silently

Per-harness `variant` says what configuration the row reflects:

| harness | variant | why |
|---|---|---|
| claude | real config | the user's actual tax — plugins/MCP included; allow/disallow-tool flags do not strip schemas from the request anyway |
| codex | minimal home (floor) | real-config runs fire background memory-agent calls + side-effect network I/O; floor is the honest zero-touch default |
| gemini | isolated home (api-key mode) | protects the user's OAuth login; `-m` pins a non-gemini-3 model to skip a strict-JSON preflight classifier that retry-loops against a sink |
| opencode | isolated config | the user's real config can narrow the model catalog and break the run; XDG isolation also keeps their auth.json unconsulted |
| pi | isolated home (4 default tools) | pi is the lean-delegate baseline; its default toolset is the comparison point |

A floor row and a real-config row are different constructs. Comparing them
compares a plugin loadout, not a harness — the table prints the variant so
nobody does it by accident, and the honesty block says so in words.

## Column comparability

- `system` / `schemas` / `body` are JSON chars of the captured (scrubbed)
  request. `body` is the analyzed total; `report.json` also keeps the raw
  pre-scrub `body_bytes`, which differs by a few chars.
- `system` is assembled per protocol — top-level `system`/`instructions`
  **plus** system/developer-role entries inside `messages`/`input`, because
  several harnesses put the bulk of their instructions there. Cross-harness
  comparison of this column is approximate.
- `t1st` is wall-clock to the first captured LLM request **including harness
  boot**. It is not a latency benchmark.
- `mcp` is `-` (unknown) unless the harness's MCP naming convention is
  confirmed; today only Claude Code's (`mcp__server__tool`) is. `-` never
  means zero.

## Repetition

A single run is one observation. `--repeat N` runs each harness N times and
reports the **median** trial as the row plus the **observed min–max spread** —
a range across N runs on one machine, explicitly *not* a confidence interval
and not a variance claim. Every trial's captures are kept (`trial-2/`,
`trial-3/`, …) so the spread is auditable.

## Repro pack

`--out` (default `./subagent-tax-report/`) contains: `report.json` (full
per-tool breakdown, calibration, honesty lines), raw captures per harness,
harness output logs, ephemeral configs used, and `manifest.sha256` covering
every artifact. The tool refuses to write into a non-empty directory it did not
create.

**Redaction, at write time:**

- Header values for credentials (authorization, x-api-key, cookies, …) **and**
  account/device/session identifiers (`x-claude-code-session-id`,
  `x-codex-turn-metadata`, `x-gemini-api-privileged-user-id`, `x-session-id`,
  …), replaced with `redacted:sha256:<12>` so equality stays checkable.
- Credential-bearing query params (`?key=`, `access_token`, …).
- Body fields carrying the same class of identifier at any depth
  (`user_id`, `device_id`, `account_uuid`, `prompt_cache_key`,
  `safety_identifier`, `conversation_id`, …).
- Email addresses (Claude Code embeds the account email in its system prompt)
  and credential-shaped strings (`sk-*`, `ghp_*`, `AKIA*`, `xox*`, `AIza*`).

**Still sensitive:** bodies are your harness's real system prompt — local
paths, skill lists, MCP tool names, and any global instruction files. Review
before publishing a repro pack.

**Signing:** the manifest is a hash chain, not a signature. Publishing signed
results goes through the CaveBench Ed25519 receipt path and its governance
(`docs/cavebench/GOVERNANCE.md`), founder-keyed, at publish time only.

## Known limitations

- **cursor-agent** reports `unmeasurable`, and not for lack of a redirect: a
  hidden `-e/--endpoint` flag does point it at a local server (checked
  2026-08-07). What arrives carries no prefix. The client streams
  `agent.v1.AgentRunRequest` protobuf over a bidirectional Connect/HTTP-2 RPC
  whose schema holds conversation turns, a model identifier, and the user's own
  MCP tools — no system-prompt field, no builtin tool schemas — and no LLM
  provider hostname appears anywhere in its bundle. The agent loop runs on
  Cursor's servers, so the prefix never crosses the wire. The one
  client-supplied slice that *would* be measurable is a user's own MCP tool
  schemas.
- opencode's wire protocol here is OpenAI **Responses** (via its builtin
  openai provider), despite ecosystem docs commonly labeling it chat
  completions; with an `@ai-sdk/openai-compatible` provider it speaks chat.
  The analyzer handles both.
- Stopping after the first captures means multi-request boot sequences (codex
  memory agent, opencode title call) are captured but not exhaustively
  explored; they are visible in `all_captures`.
- Harness detection runs `<bin> --version`; a harness available only as a shell
  alias or function will report "not installed".
- Windows npm command shims are resolved through `PATH`/`PATHEXT`, then their
  Node entrypoints are launched directly without a shell; cleanup uses
  `taskkill /t /f`. Native Windows CI covers launch and process-tree contracts.

## Reproducing

```
node run.mjs                  # all installed harnesses
node run.mjs --list           # what's installed + recipe status
node run.mjs --harness claude,pi --repeat 3
node --test tests/*.test.mjs  # harness-free test suite (fake-harness e2e)
```

`fixtures/example-report.json` is a sanitized report from one machine, kept so
the documented shape can be diffed against a fresh run.
