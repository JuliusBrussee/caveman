# packages/sdk/python — Python SDK for the Caveman Cloud gateway

Stdlib-only (`urllib.request`, no third-party deps) Python package. Provides `Cave` (config +
entrypoint), `CaveTool` (tool descriptor), and `ToolSearchResult`. All HTTP calls POST to the
gateway with `x-cave-agent` / `x-cave-workflow` / `x-cave-retention` headers set from `Cave` fields.

## Layout

- `caveman_cloud/__init__.py` — re-exports `Cave`, `CaveTool`, `CompressResult`, `ContextPackItem`, `ContextPackOptions`, `ContextPackResult`, `ToolSearchResult`, …
- `caveman_cloud/core.py` — all implementation: `Cave`, `Trace`, `Provider`, `_Create`, `ToolSearchResult`, `CompressResult`, `ContextPack*`, `CaveTool`, `headers()`
- `tests/test_sdk.py` — pytest tests; mock `urllib.request.urlopen` with `patch()`
- `tests/test_parity.py` — cross-language conformance suite; drives `../../parity/fixtures.json` (shared with sdk-ts). Same fixtures, two languages → a field in one SDK and not the other fails CI.
- `tests/test_trace_continuity.py` — trace/span id minting + which requests carry `x-cave-trace-id` / `x-cave-parent-span-id`; mirrors the TS `tests/trace-continuity.runtime.mjs`
- `pyproject.toml` — distribution name `caveman-sdk` (import package stays `caveman_cloud`), `requires-python = ">=3.13"`, no runtime dependencies

## Key API surface (`core.py`)

- `Cave.trace(workflow, tags, *, trace_id=None, span_id=None)` → context manager yielding `Trace`; call `.model["openai"].responses.create(body)` inside. The trace mints `trace_id` (32 lowercase hex) + a root `span_id` (16 lowercase hex) with the exporter's RNG; `trace_id`/`span_id` continue an inbound trace and a value that isn't the exact hex shape is replaced rather than sent. Every provider call made **through the trace** carries `x-cave-trace-id` + `x-cave-parent-span-id`. Generic `/sdk/v1/*` calls and providers built off the `Cave` carry neither; the sole SDK-endpoint exception is `Trace.tool`, whose `/sdk/v1/events` call carries the trace id and root parent span id
- `Trace.exporter(service_name=None)` → an `OTelExporter` whose `default_trace_id` is the trace's, so SDK spans and the gateway's request rows join one trace. MIRRORS the TS `CaveTrace.exporter`
- `Cave.tools(catalog, *, strategy="all", initial_tool_count=8)` → builder handle with `.strategy`, `.initial` (`list[CaveTool]`), `.search(query, *, max_tools, context, workflow, ranker, session_id)`. `strategy="deferred"` includes every `always_load` tool exactly once, then fills remaining initial slots from non-mandatory tools; a cap below the mandatory count fails locally. `.search()` always hits the gateway with the FULL catalog. MIRRORS the TS `cave.tools({catalog, strategy})`
- `Cave.tool_search(tools, query, *, context, max_tools, workflow, ranker, session_id)` → flat variant: POSTs `[tools, query]` to `/sdk/v1/tool-search`; returns `ToolSearchResult` with `.saved_tokens` / `.reduction_pct` / `.session_id`. Schema-token counters are estimates; `.token_basis` discloses the counter and `.basis` is always `"inferred"`. `ranker` (`"bm25"`|`"embeddings"`) is passed through verbatim — the SDK never computes similarity
- `Cave.prompts.internal_brevity(*, style, preserve_errors_verbatim=False, preserve_code_verbatim=False)` → output-style snippet (`"none"` → `""`); booleans render lowercase to match the TS `cave.prompts.internalBrevity`
- `Cave.compress(payload, *, content_type=None)` → `CompressResult`; POSTs `/sdk/v1/compress`, maps the Engine report. **Byte-safe pass-through** on any transport/parse problem (original input, `ratio=0.0`, no handle); `.token_count_basis` discloses the counter and `basis` is always `"inferred"`. The SDK delegates — it never reimplements a compressor
- `Cave.context.pack(query, items, options)` → `ContextPackResult`; connected-only POST to `/sdk/v1/context/pack`. Lossy selector over caller-owned items, never CCR/ledger; returns exact `deferred_ids`. Transport or malformed-report failure returns all original items with zero inferred savings
- `Trace.expand(source_ref)` — the GET half of `checkpoint()`; `GET /sdk/v1/checkpoints/{ref}/expand` returns the stored `{source_ref, version, messages, checkpoint}`
- `Cave.openai/anthropic/gemini/vertex(upstream_key)` → `Provider` that proxies through gateway; `Provider.raw(path, body)` is the escape hatch (mirrors the TS provider-client `raw`)
- `Cave.bedrock(region, endpoint="runtime")` → no-network first-party route descriptor; Runtime defaults to `/bedrock`, explicit Mantle returns `/bedrock/anthropic`, and `sdk_only=False` mirrors TS `sdkOnly`
- `Trace.tool(name, options, fn)` — calls `fn()` then POSTs a `tool.call` event
- `Trace.page_artifact(value, options)` / `Trace.artifacts.page(value, options)` — send versioned `{value, options, workflow}`; gateway stores only JSON `value`. `artifacts.get(id)` performs authenticated retrieval. `page_artifact` remains backwards-compatible alias.
- `Trace.model["openai"].responses.create(body, *, latency_class=None, tool_session_id=None)` — when `latency_class` is set, sends the `x-cave-async` header (`"true"` unless `"interactive"`); when `tool_session_id` is set, sends `x-cave-tool-session`, mirroring the TS `trace.model.openai.responses.create(body, {cave:{latencyClass, toolSessionId}})`
- `Trace.checkpoint(messages, options)` — POSTs to `/sdk/v1/checkpoints`; the gateway persists it (Valkey) and returns a reversible `source_ref` you can later expand via `GET /sdk/v1/checkpoints/{ref}/expand`
- `Cave.exporter(service_name=None)` → `OTelExporter`; `record_span(...)` maps current GenAI fields to `gen_ai.*`, `export()` POSTs OTLP/JSON to standard `/v1/traces` (headers via `otlp_headers()`; legacy `/otlp/v1/traces` remains server-only compatibility)
- `Cave.retry_loop_breaker(threshold=3)` → `RetryLoopBreaker`; `.record(name, args)` raises `RetryLoopError` after `threshold` consecutive identical tool calls (interrupts a stuck loop). `.guard(name, args, fn)` records then runs `fn`
- `Cave.jobs` → reserved `JobsClient` surface. Every method fails locally with `cave_async_jobs_unavailable`; it performs no network request until durable encrypted request storage, credential custody, and a draining worker exist. MIRRORS the TS `Cave.jobs`

## Conventions

- Tests use `patch("urllib.request.urlopen", side_effect=fake_urlopen)` — never real network
- Add new gateway endpoints via `Trace._request(path, body)` or `Provider.create(path, body)`
- `headers()` is the single source for all outgoing headers; edit there, nowhere else
- Deferred tool-search session handoff uses request/result `session_id` plus provider header `x-cave-tool-session`; update sdk-ts + parity fixtures with any change
- Run tests: `pytest` from this directory (Python ≥ 3.13 required)

## Gotchas

- **No third-party deps** — do not add `requests`, `httpx`, or any library; keep `dependencies = []` in pyproject.toml
- **byte-safe**: SDK sends request bodies to the gateway unmodified; no rewriting. `compress()` delegates to the Engine and passes the original through on any problem
- **context packing is connected-only and intentionally lossy**: it sends item bytes to gateway, never runs in local wrap, and relies on caller retaining every item named by `deferred_ids`. It chooses what enters window; cache-optimal assembly chooses placement
- `sdk-python` and `sdk-ts` mirror the same field names and `/sdk/v1/*` contract — enforced by the shared parity suite (`tests/test_parity.py` + `../../parity/fixtures.json`), not just convention. A divergence is a CI failure. Change one SDK, change both **and** the fixtures

See ../../../CLAUDE.md (root)
