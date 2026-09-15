# packages/sdk/typescript — TypeScript SDK (`@caveman-ai/sdk`)

Single-file SDK (`src/index.ts`) exported as an ES module. Provides `Cave` (main client),
`CaveTrace` (per-request tracing), and BM25-backed tool-search against the gateway. No runtime
dependencies — only `devDependencies` for TypeScript.

## Layout

- `src/index.ts` — entire SDK; exports `Cave`, `CaveTrace`, `CaveOptions`, `CaveTool`, `ToolSearchResult`, `CompressOptions`, `CompressResult`, and the `ContextPack*` types
- `tests/tool-search.test.ts` — type-level assertions compiled by `tsc --noEmit`
- `tests/tool-search.runtime.mjs` — runtime tests using `node:test` + global fetch mock (imports from `dist/`)
- `tests/runtime-policy.runtime.mjs` + `tests/runtime-policy.test.ts` — the runtime-policy client; drives every section of `../../parity/runtime-policy.fixtures.json` (fetch wire, signature cases, all `assignment_vectors` with exact float equality, all `guard_cases` — the shared operator truth table lives in the fixture, not in this file — and all `decision_cases`). Iterate the arrays; never hard-code their counts
- `tests/parity.runtime.mjs` — cross-language conformance suite; drives `../../parity/fixtures.json` (shared with sdk-python). Same fixtures, two languages → a field in one SDK and not the other fails CI.
- `tests/trace-continuity.runtime.mjs` — trace/span id minting + which requests carry `x-cave-trace-id` / `x-cave-parent-span-id`; mirrors the Python `tests/test_trace_continuity.py`
- `tsconfig.json` / `tsconfig.test.json` — separate configs; test config covers `tests/`. Both extend the **repo-root** `../../../tsconfig.base.json`.

## Key APIs

- `new Cave(options)` — requires `apiKey`, `baseURL`, `agent`
- `cave.trace(opts, fn)` — wraps a callback with a `CaveTrace`; sends tool-call spans to `/sdk/v1/events`. The trace mints `traceId` (32 lowercase hex) + a root `spanId` (16 lowercase hex) with the exporter's RNG; `opts.traceId`/`opts.spanId` continue an inbound trace and a value that isn't the exact hex shape is replaced rather than sent. Every provider call and trace-scoped `/sdk/v1/*` call made **through the trace** carries `x-cave-trace-id` + `x-cave-parent-span-id`; provider clients and SDK calls built directly off the `Cave` carry neither
- `CaveTrace.exporter({serviceName?})` → a per-service memoized `OTelExporter` whose `defaultTraceId` is the trace's, so SDK spans and the gateway's request rows join one trace. Runtime-policy decision spans passed a `CaveTrace` use this same caller-reachable default buffer; call `trace.exporter().flush()` to ship them. MIRRORS the Python `Trace.exporter`
- `cave.tools({ catalog, strategy })` — returns `{ initial, strategy, search(query, opts?) }`. **`search()` is async** (returns `Promise<ToolSearchResult>`); breaking change from 1.0 which was sync. `opts.ranker` (`"bm25"`|`"embeddings"`) is passed through to the gateway verbatim; `opts.toolSessionId` sends `session_id` so provider callbacks can re-inject called deferred tools. The SDK never computes similarity
- `cave.toolSearch(catalog, query, opts?)` — direct variant, same contract (incl. `ranker` / `toolSessionId`). Schema-token counters are estimates; `tokenBasis` discloses the counter and `basis` is always `"inferred"`
- `cave.compress(payload, opts?)` → `Promise<CompressResult>`; POSTs `/sdk/v1/compress`, maps the Engine report. **Byte-safe pass-through** on any transport/parse problem (original input, `ratio:0`, no handle); `tokenCountBasis` discloses the counter and `basis` is always `"inferred"`. The SDK delegates — it never reimplements a compressor
- `cave.context.pack(query, items, options)` → `Promise<ContextPackResult>`; connected-only POST to `/sdk/v1/context/pack`. Lossy selector over caller-owned items, never CCR/ledger; returns exact `deferredIds`. Transport or malformed-report failure returns all original items with zero inferred savings
- `CaveTrace.context.expand(sourceRef)` — the GET half of `checkpoint()`; `GET /sdk/v1/checkpoints/{ref}/expand` returns the stored `{source_ref, version, messages, checkpoint}`
- `cave.openai/anthropic/gemini/vertex()` — thin provider clients; proxied through gateway; each exposes a `.raw` fetch escape hatch (mirrors the Python `Provider.raw`). `cave.bedrock({region, endpoint?})` is a no-network first-party route descriptor: Runtime defaults to `/bedrock`; explicit Mantle returns `/bedrock/anthropic`; `sdkOnly:false` mirrors Python's `sdk_only`
- `cave.prompts.internalBrevity({style, preserveErrorsVerbatim?, preserveCodeVerbatim?})` — output-style snippet (`style:"none"` → `""`); MIRRORS the Python `cave.prompts.internal_brevity`
- `CaveTrace.model.openai.responses.create(body, {cave:{latencyClass, toolSessionId}})` — passing a `latencyClass` hint sets the `x-cave-async` header (`"true"` unless `"interactive"`); passing `toolSessionId` sets `x-cave-tool-session`. Mirrored by the Python `responses.create(body, latency_class=..., tool_session_id=...)`
- `CaveTrace.artifacts.page()` — sends versioned `{value, options, workflow}`; gateway stores only JSON `value`. `artifacts.get(id)` performs authenticated retrieval. `strategy:"verbatim"` bypasses storage. Mirrored by Python.
- `CaveTrace.context.checkpoint()` — POSTs to `/sdk/v1/checkpoints`; gateway persists it (Valkey) + returns a reversible `source_ref` (expand via `GET /sdk/v1/checkpoints/{ref}/expand`)
- `cave.exporter({serviceName?})` → `OTelExporter`; `recordSpan(...)` maps current GenAI fields to `gen_ai.*`, `export()` POSTs OTLP/JSON to standard `/v1/traces` (headers via `otlpHeaders()`; legacy `/otlp/v1/traces` remains server-only compatibility)
- `cave.runtimePolicy({publicKey?, autoRefreshSeconds?, killEnv?})` → `RuntimePolicyClient`. `refresh()` is the only network call (`GET /sdk/v1/runtime-policy`, std headers minus content-type); it Ed25519-verifies the bundle **string's** exact bytes before parsing, TOFU-pins the key **the moment the signature verifies** (before the schema/sequence checks, so a rejected-but-signed bundle cannot open a downgrade window), rejects a regressed `sequence`, and keeps last-known-good on any failure. The fetch carries a 30s `AbortSignal.timeout` (mirrors Python's `timeout=30`) and stops a response after 1 MiB (`oversized_response`); fetch implementations without a bounded readable body fail closed (`bounded_response_unavailable`) instead of calling unbounded `text()`. An `autoRefreshSeconds` tick that lands mid-refresh is skipped, not stacked. `decide(taskFamily, {unitKey, context, trace})` is **synchronous, local-only, and never throws**; holdout suppresses onto the fallback path and a missing unit key or invalid experiment never guesses an arm. `kill()` latches locally, `killEnv` is re-read per decide, `state()` snapshots. Routing only — no savings vocabulary anywhere. MIRRORS the Python `cave.runtime_policy()`
- `cave.retryLoopBreaker(threshold=3)` → `RetryLoopBreaker`; `.record(name, args)` throws `RetryLoopError` after `threshold` consecutive identical tool calls; `.guard(name, args, fn)` records then runs `fn`
- `cave.jobs` → reserved `JobsClient` surface. Every method fails locally with `cave_async_jobs_unavailable`; it performs no network request until durable encrypted request storage, credential custody, and a draining worker exist. MIRRORS the Python `Cave.jobs`

## Conventions

- Tests: type assertions in `.test.ts` (compiled only), runtime in `.runtime.mjs` (run against `dist/`)
- Build before runtime tests: `pnpm build && pnpm test:node`
- Request body keys are `snake_case` to the gateway; response mapped to `camelCase` in `ToolSearchResult`
- `x-cave-workflow` header defaults to `defaultWorkflow ?? "unlabeled-workflow"`; never omit it
- Deferred tool-search session handoff uses request `session_id`, result `sessionId`, and provider header `x-cave-tool-session`; update sdk-python + parity fixtures with any change

## Gotchas

- **byte-safe**: SDK sends request bodies to the gateway verbatim; no rewriting allowed. `compress()` is the one path that yields smaller bytes and it **delegates** to the Engine — on any problem it passes the original through
- **context packing is connected-only and intentionally lossy**: it sends item bytes to gateway, never runs in local wrap, and relies on caller retaining every item named by `deferredIds`. It chooses what enters window; cache-optimal assembly chooses placement
- **mirror sdk-python**: every field/method exists in both, enforced by the shared parity suite — a divergence is a CI failure, not a convention slip. Change one SDK, change both **and** the fixtures
- published as `@caveman-ai/sdk`; the workspace name stays `@caveman-ai/sdk` until the npm redirect plan lands
- `strategy:"deferred"` initial set = `alwaysLoad` tools + up to `initialToolCount` (default 8); never returns the full catalog without a `search()` call
- `reductionPct` rounds to one decimal; `savedTokens` is derived (`full - sent`), not from the gateway response

See ../../../CLAUDE.md (root)
