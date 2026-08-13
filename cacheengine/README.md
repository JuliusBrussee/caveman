# cacheengine

Standalone prompt-cache planner and provider-native wire engine. Import path:

```go
github.com/JuliusBrussee/caveman/cacheengine
```

## License

Source ships under Business Source License 1.1 (`BSL-1.1`). It is
source-available, not OSI Open Source before Change Date. First-party
self-hosted production is permitted; third-party hosted, managed, or embedded
service use requires commercial license. See `LICENSE` and `../LICENSING.md`.

Core planner knows capabilities, not provider names. Give it ordered stable
segments plus cache economics; it selects positive-break-even prefix points,
guards epoch bytes, detects volatile data and drift, and creates tenant-opaque,
load-sharded affinity keys. `Driver` and profile resolver seams support arbitrary
providers and wire formats. Production constructors should use `NewChecked`;
legacy `New` also stores configuration errors and makes every operation fail
closed. Resolver and driver callbacks may run concurrently.
Provider-native bodies and framed stable prefixes default to separate 64 MiB
limits, configurable through `MaxRequestBytes` and `MaxStablePrefixBytes`; both
reject before copy or concatenation. Explicit limits must remain between one
byte and 1 GiB. Request identities, segment names, profile IDs, and routing
metadata are length-bounded and reject control characters. Custom-driver output
cannot exceed configured request-body limit, and optimizer identities receive
same strict validation.

Native bridge ships grounded built-in strategies:

| Surface | Behavior | Attribution ceiling |
|---|---|---|
| Anthropic | Reuses existing stable tool/system breakpoint; adds rolling top-level automatic caching | causal provider observation; standalone dollars stay zero |
| OpenAI GPT-5.6 family | Scoped affinity key plus one stable and latest three explicit breakpoints; affinity-only fallback when body has no safe markable block | causal provider observation; repo verified ledger extension remains unbuilt |
| Earlier OpenAI | Scoped affinity key over provider automatic caching | affinity only |
| Bedrock Anthropic Claude | Reuses catalog-gated stable point and adds rolling message checkpoint | causal provider observation; standalone dollars stay zero |
| Gemini | Observes implicit provider-managed caching without rewriting body | organic, never attributed to engine |
| Unknown | Exact pass-through | unavailable |

Runtime needs no gateway process, network, database, or control plane;
provider-native compilers live in this package. Core production graph reuses
only Caveman JSON splice, cache guard, catalog/cost, and YAML packages (six
non-stdlib packages total). Parity tests lock Anthropic and Bedrock behavior to
existing gateway transforms without importing gateway runtime in production.
`Optimize` makes no provider call. It accepts and returns wire bytes, so proxy,
SDK, sidecar, or local process can embed engine directly:

```go
result, err := engine.Optimize(ctx, cacheengine.NativeRequest{
    Scope: "org/project", Epoch: "conversation-42",
    Provider: "openai", Model: "gpt-5.6", Endpoint: "/v1/responses",
    Body: requestBody, PrefixTokens: providerCount,
    ExpectedCalls: 8, RuntimeMode: "optimize", AuthMode: "payg",
})
upstreamBody := result.Body // original bytes on every unsafe/unsupported path
```

“Always cached” is impossible as a literal guarantee: provider minimums, TTL,
concurrency, capacity, exact-prefix changes, unsupported models, and organic
caches can still miss. Engine maximizes eligible stable prefixes and returns
explicit reason when it cannot act. Caller cache fields always win. Malformed or
ambiguous JSON (including duplicate keys), unsupported built-in model/endpoint,
body/metadata model mismatch, record mode, non-PAYG mode, volatile stable slots,
and prefix drift preserve original bytes.

## Generic planner

```go
engine := cacheengine.New(cacheengine.Config{})
plan, err := engine.Plan(cacheengine.PlanRequest{
    Scope:         "org/project",
    Epoch:         "conversation-42",
    ExpectedCalls: 8,
    Profile: cacheengine.Profile{
        ID: "provider-cache-v1", Mode: cacheengine.ModeExplicit,
        MinPrefixTokens: 1024, MaxBreakpoints: 4,
        EconomicsKnown: true,
        WriteMultiplier: 1.25, ReadMultiplier: 0.10,
        RoutingKey: true,
    },
    Segments: []cacheengine.Segment{
        {Name: "tools", Content: toolBytes, Tokens: 1800, Stable: true, Cacheable: true},
        {Name: "live", Content: userBytes, Stable: false},
    },
})
```

`ExpectedCalls` means calls expected to share prefix while provider entry stays
warm; do not feed total lifetime calls across cache expiry gaps. Economics use
input-rate units, never guessed dollars. Unknown token count keeps
safe transformation available but reports economics unavailable. `Observe`
accepts normalized provider usage and distinguishes hit/write/miss/unavailable;
`ObserveRawCacheUsage` also maps official raw cache counters, including OpenAI
`cache_write_tokens`. Neither path mints verified savings.

## Product boundary

This module plans provider-native prompt-prefix caching for hosted APIs. It does
not store or replay model responses, and it does not manage self-hosted KV
memory. Those are separate products with different correctness boundaries:

| Category | Examples | Difference |
|---|---|---|
| Provider prompt-prefix planner | cacheengine | Metadata-only request transform; provider still runs model and reports cache counters |
| Exact/semantic response cache | [Helicone](https://docs.helicone.ai/features/advanced-usage/caching), [Portkey](https://portkey.ai/docs/virtual_key_old/product/ai-gateway/cache-simple-and-semantic), [GPTCache](https://github.com/zilliztech/GPTCache) | Replays stored outputs; semantic modes add answer-equivalence risk |
| Self-hosted KV cache | [vLLM APC](https://docs.vllm.ai/en/v0.15.0/features/automatic_prefix_caching/), [LMCache](https://docs.lmcache.ai/) | Controls inference memory; requires serving infrastructure |

No best-in-market claim exists yet. It requires live, same-population provider
counters, task-quality verification, latency, and competitor comparison. Current
public-corpus artifact is conservative simulation and fails strict 97% gates.

`cache-replay` closes external-runner glue without weakening evidence: exact v3
trace reconstruction, opt-in authenticated calls, no automatic retries,
provider-counted usage, external task grading, private retained artifacts, and
exact-population observation v3. Full trace optimization/equivalence completes
before first call; bounded concurrent workers use absolute trace timing and
fail on excess schedule drift. Caller-declared optimized-wire input ceilings
plus provider-native maximum output fields form preflight billed-token ceiling;
provider-counted basis remains caller-attested, and ceiling is not guaranteed
actual-token or dollar cap. Synthetic/session-local timing and estimated token
budgets fail live defaults. See
[`cachebench/REPLAY_PROTOCOL.md`](cachebench/REPLAY_PROTOCOL.md).

## New provider

Supply capability profile plus `Driver`; planner stays unchanged. Native
profiles must bind `Provider` explicitly. Driver receives selected breakpoints
and must return original bytes with no optimizer IDs when safe compilation is
impossible.

```go
engine := cacheengine.New(cacheengine.Config{
    ResolveProfile: func(r cacheengine.NativeRequest) (cacheengine.Profile, bool) {
        return acmeProfile, r.Provider == "acme"
    },
    Drivers: map[string]cacheengine.Driver{
        "acme": acmeWireDriver,
    },
})
```

## Proof

```bash
cd public
go test -race ./cacheengine/...
go vet ./cacheengine/...
go test -run '^$' -bench BenchmarkOptimizeOpenAIExplicit -benchmem ./cacheengine
go run ./cacheengine/cmd/cache-experiment
go run ./cacheengine/cmd/cachebench
go run ./cacheengine/cmd/cache-replay -help
```

Experiment makes zero provider calls. Fixture token counts and break-even output
are modeled evidence, not live cache-hit evidence.

`cachebench` adds strict 97% request-hit and eligible-token-hit gates over
synthetic and public agent traces, planned compaction, provider-specific wire
transforms, model-visible request equivalence, TTL failure drills, and
provider-observation JSONL replay. It imports the CC-BY-4.0 LMCache Agentic
Traces corpus with pinned source hashes and bounded retained memory. See
[`cachebench/README.md`](cachebench/README.md). Simulation and provider-observed
reports never blend.

Built-in capability behavior checked against official provider docs on
2026-08-10:

- https://platform.claude.com/docs/en/build-with-claude/prompt-caching
- https://developers.openai.com/api/docs/guides/prompt-caching
- https://ai.google.dev/gemini-api/docs/caching
- https://docs.aws.amazon.com/bedrock/latest/userguide/prompt-caching.html
