# Optional TokenSave cooperation

Caveman and TokenSave remain independently installed and managed. Caveman never
installs TokenSave, edits its configuration, or changes models, providers, or
endpoints because it is present. Removing TokenSave leaves Caveman usable.

The Caveman voice skills and investigation guidance prefer suitable targeted
semantic queries only when tools are available in the session and the project
is indexed. `tokensave_status` can establish readiness; an installed executable,
MCP configuration, or skill file cannot. Sufficient findings should be reused
before broad reads or repeated Cavecrew research. User instructions take priority.

## Preserve model-bound tool results

These controls require a proxy built with this change. They do not activate voice
skills or prove TokenSave availability. In the existing proxy `caveman.yaml`:

```yaml
preserve_tools:
  - 'mcp__tokensave__*'
  - 'tokensave_*'
```

Alternatively, set the environment of the existing Caveman proxy process:

```bash
export CAVEMAN_PRESERVE_TOOLS='["mcp__tokensave__*","tokensave_*"]'
```

The JSON array replaces YAML rules; `[]` disables preservation. An invalid array,
empty identity, whitespace, or wildcard other than one final `*` refuses startup.
Exact names and nonempty prefixes are case-sensitive. There is no automatic
server discovery: prefixes describe observed model-visible names, not a universal
MCP server identity. For renamed servers, configure the names your agent sends.
TokenSave's `src/agents/mod.rs` uses `mcp__tokensave__...` permission names; its
MCP definitions expose bare `tokensave_...` names. Other naming conventions need
explicit rules and verification, not a prose heuristic.

Rules exclude complete matching results from adapter rewrite candidates before
compression, TOON conversion, elision, and cached replacement substitution. The
exclusion preserves original serialized content spans, including nested content;
it does not depend on a recovery handle. Call IDs associate interleaved results
with the originating calls. With rules enabled, orphaned results and results
with missing call names stay raw. A duplicate ID is protected if any associated
call is protected. Unrelated results and user text remain eligible for normal
compression. This policy must be set before a fresh session: changing policy
for an existing compressed history can recache that history in its raw form.

| Model protocol | Result identification |
| --- | --- |
| Anthropic Messages | Assistant `tool_use.id/name` to `tool_result.tool_use_id` |
| OpenAI Chat Completions | Assistant `tool_calls[].id/function.name` to `tool_call_id` |
| OpenAI Responses, including decoded Codex `/chatgpt/responses` | `function_call`/`custom_tool_call` `call_id/name` to the corresponding output `call_id` |
| Gemini generateContent | Protocol `functionResponse.name` on the result itself |
| Azure/OpenAI-compatible mounts using these grammars | Inherit the matching OpenAI or Anthropic adapter grammar |
| Bedrock/Vertex | Existing result compression stays unavailable; no new exclusion support is claimed |

Unrecognized envelopes and protocols do not acquire an exact-preservation
contract. A raw Engine/CLI compression call has no originating MCP identity;
this policy cannot protect arbitrary bytes submitted directly to such a call.
The CLI shrink hook rewrites supported shell commands before execution, rather
than compressing arbitrary MCP responses. Standalone memory-file compression is
also outside this protocol policy. Do not send protected results to either
manual compression path. No MCP server is expected to set model-request headers.

Proxy startup logs list configured rules, supported grammars, and conservative
fallbacks. Per-request logs list matched tool names, protected and unresolved
result counts, ambiguous IDs, and unsupported protocols without result payloads. Rules describe the protected identities; they do not prove any tool
is installed, active, or called. Pixel rendering is disabled for the whole
request while preservation is configured, since its scanner lacks general tool
correlation. Proxy catalog annotation stripping is also disabled while rules
are configured. These conservative conflicts are explicit in startup diagnostics.

## Preserve catalog descriptions separately

For the dedicated catalog shrinker, set `CAVEMAN_PRESERVE_TOOLS` in the shrinker's
process environment, using names actually present in its input catalog:

```bash
CAVEMAN_PRESERVE_TOOLS='["tokensave_*"]' caveman-shrink < tools.json > tools.shrunk.json
CAVEMAN_PRESERVE_TOOLS='["tokensave_*"]' caveman-shrink lint tools.json
```

Malformed shrinker policy forwards the original catalog unchanged with a
diagnostic; lint rejects it. Matching MCP and OpenAI tool envelopes remain whole,
including descriptions,
parameter guidance, constraints, defaults, and schemas. JSON serialization can
change formatting; decoded values remain equal. Unrelated tools still shrink.
Unsupported Gemini catalog envelopes currently remain uncompressed. The Go
library exposes `shrink.WithPreservedTools(toolpolicy.Policy{...})` for both
`Shrink` and `Lint`; library callers explicitly pass policy rather than relying
on ambient environment. The pure compressor constructor is
`compressors.NewToolSchemaWithPreservedTools`.

Preserving schemas alone does not prove equivalent tool selection. Recovery of
the original catalog does not prove the model saw unchanged descriptions.

## Reproducible correctness checks

The secure SQLite tests require private temporary directories; use a restrictive
umask on hosts whose default umask permits group writes:

```bash
umask 077
go test ./shared/toolpolicy ./proxy/providers/... ./engine/compressors ./shrink/... ./proxy/internal/config ./proxy/internal/gateway ./proxy/internal/standalone
go vet ./shared/toolpolicy ./proxy/providers/... ./engine/compressors ./shrink/... ./proxy/internal/config ./proxy/internal/gateway ./proxy/internal/standalone
node packages/cli/scripts/compile-registries.mjs
python3 tests/verify_repo.py
git diff --check
```

Provider tests mutate every offered candidate and assert the original serialized
semantic payload still occurs exactly. Fixtures cover JSON, code-style
signatures, paths, identifiers, errors, numbers, relationships, interleaving,
and mixed results. Gateway checks include actual upstream delivery and pixel
pass-through. Catalog tests assert full decoded equality for protected tools.

## Performance evidence

Correctness tests do not measure savings. TokenSave's existing
`scripts/caveman_bench` runner on its compatibility branch provides paired fresh
checkouts, task verification, and a real-agent telemetry adapter contract. Run
its documented protocol with the same pinned task, project index, agent, model,
provider, and pricing in both arms: TokenSave alone and TokenSave plus Caveman.
Record input/output tokens, MCP calls, redundant reads, independently checked
task success, latency, and cost per successful task. Keep raw traces private.
No live agent adapter, paid model trial, or billing evidence has been supplied
for this change. No measured performance improvement is claimed. Python runner
accounting fixtures are synthetic and must never be reported as savings.
