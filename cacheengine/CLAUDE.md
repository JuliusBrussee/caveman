# cacheengine — standalone prompt-cache engine

Capability-driven planner plus provider-native wire compiler. Runtime needs no
gateway process, network, database, or control plane. `engine.go` is
provider/model agnostic; standalone wire compilers and `profiles.go` bind
current provider contracts while
`Driver`/profile resolver seams add providers without planner changes.
`cachebench` alone owns optional live replay I/O.

## Layout

- `types.go` — public capability, plan, result, driver, observation contracts.
- `engine.go` — stable-prefix frontier, break-even math, volatile/drift guard,
  scoped affinity-key sharding.
- `native.go`, `openai.go`, `wire_anthropic_bedrock.go` — strict built-in JSON
  extraction and wire edits. Production graph imports no gateway adapter;
  parity tests compare standalone Anthropic/Bedrock behavior to those adapters.
- `profiles.go` — built-in model thresholds, TTLs, economics, attribution.
- `raw_usage.go` — provider cache-counter normalization; no dollar minting.
- `cmd/cache-experiment` — offline transformation/economics fixtures only.
- `cachebench/` + `cmd/cachebench` — strict 97% request/token gates,
  semantic-equivalence check, bounded public-corpus import, trace export, and
  provider-observation replay.
- `cmd/cache-replay` — opt-in live replay: exact trace reconstruction, provider
  HTTP auth/SigV4, pre-send full-trace equivalence, bounded absolute-time
  concurrency, task-verifier protocol, private retained evidence, no retry.
- `cachebench/scripts/` — hash-pinned LMCache download and parquet-to-JSONL
  streaming helpers; PyArrow is optional benchmark tooling, never runtime code.

## Correctness invariants

- Original bytes survive malformed, unsupported, caller-managed, record,
  non-PAYG, volatile, drifting, or untransformable requests.
- Never reorder or rewrite semantic prompt content. Add provider-native cache
  controls only; OpenAI string-to-content-block conversion is wire-equivalent
  and only used where explicit breakpoint grammar requires it.
- `Scope` is required and hashed before provider-visible routing keys. Native
  profiles bind one provider explicitly.
- `Epoch` means one frozen stable prefix. Changed bytes require `StartEpoch`;
  silent prefix drift passes through.
- `ExpectedCalls` means reuse inside provider TTL. Unknown token counts or cache
  economics remain zero/unavailable, never guessed.
- Standalone applied results are `inferred`; pass-through/observe-only results
  are `none`; `VerifiedSavingsUSD` is always zero. Managed gateway owns any
  verified accounting method.
- Custom drivers return original bytes and zero optimizer IDs on uncertainty.
- Public boundary: never import `cloud/...`.

## Proof

```bash
cd public
go test -race ./cacheengine/...
go vet ./cacheengine/...
go test ./cacheengine -run '^$' -fuzz '^FuzzOptimizeMalformedBuiltinsPassThrough$' -fuzztime=5s
go test ./cacheengine/cachebench -run '^$' -fuzz '^FuzzAgentCorpusJSONLFailClosed$' -fuzztime=5s
go test ./cacheengine/cachebench -run '^$' -fuzz '^FuzzObservationJSONLFailClosed$' -fuzztime=5s
go test ./cacheengine/cachebench -run '^$' -fuzz '^FuzzTraceJSONLFailClosed$' -fuzztime=5s
go test ./cacheengine/cachebench -run '^$' -fuzz '^FuzzVerificationCommandOutputFailClosed$' -fuzztime=5s
go test -run '^$' -bench BenchmarkOptimizeOpenAIExplicit -benchmem ./cacheengine
go run ./cacheengine/cmd/cache-experiment
go run ./cacheengine/cmd/cachebench
go run ./cacheengine/cmd/cache-replay -help
```

See `README.md` for embedding and custom-driver examples.
