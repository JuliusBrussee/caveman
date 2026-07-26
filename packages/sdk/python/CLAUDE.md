# packages/sdk-python — Python SDK for the Caveman Cloud gateway

Stdlib-only (`urllib.request`, no third-party deps) Python package. Provides `Cave` (config +
entrypoint), `CaveTool` (tool descriptor), and `ToolSearchResult`. All HTTP calls POST to the
gateway with `x-cave-agent` / `x-cave-workflow` / `x-cave-retention` headers set from `Cave` fields.

## Layout

- `caveman_cloud/__init__.py` — re-exports `Cave`, `CaveTool`, `CompressResult`, `ToolSearchResult`, …
- `caveman_cloud/core.py` — all implementation: `Cave`, `Trace`, `Provider`, `_Create`, `ToolSearchResult`, `CompressResult`, `CaveTool`, `headers()`
- `tests/test_sdk.py` — pytest tests; mock `urllib.request.urlopen` with `patch()`
- `tests/test_parity.py` — cross-language conformance suite; drives `../../parity/fixtures.json` (shared with sdk-ts). Same fixtures, two languages → a field in one SDK and not the other fails CI.
- `pyproject.toml` — distribution name `caveman` (PyPI public name; import package stays `caveman_cloud`), `requires-python = ">=3.13"`, no runtime dependencies

## Key API surface (`core.py`)

- `Cave.trace(workflow, tags)` → context manager yielding `Trace`; call `.model.openai.responses.create(body)` inside
- `Cave.tools(catalog, *, strategy="all", initial_tool_count=8)` → builder handle with `.strategy`, `.initial` (`list[CaveTool]`), `.search(query, *, max_tools, context, workflow, ranker, session_id)`. `strategy="deferred"` → `initial` = `always_load` tools + first `initial_tool_count` of the catalog; `.search()` always hits the gateway with the FULL catalog. MIRRORS the TS `cave.tools({catalog, strategy})`
- `Cave.tool_search(tools, query, *, context, max_tools, workflow, ranker, session_id)` → flat variant: POSTs `[tools, query]` to `/sdk/v1/tool-search`; returns `ToolSearchResult` with `.saved_tokens` / `.reduction_pct` / `.session_id`. Schema-token counters are estimates; `.token_basis` discloses the counter and `.basis` is always `"inferred"`. `ranker` (`"bm25"`|`"embeddings"`) is passed through verbatim — the SDK never computes similarity
- `Cave.prompts.internal_brevity(*, style, preserve_errors_verbatim=False, preserve_code_verbatim=False)` → output-style snippet (`"none"` → `""`); booleans render lowercase to match the TS `cave.prompts.internalBrevity`
- `Cave.compress(payload, *, content_type=None)` → `CompressResult`; POSTs `/sdk/v1/compress`, maps the Engine report. **Byte-safe pass-through** on any transport/parse problem (original input, `ratio=0.0`, no handle); `.token_count_basis` discloses the counter and `basis` is always `"inferred"`. The SDK delegates — it never reimplements a compressor
- `Trace.expand(source_ref)` — the GET half of `checkpoint()`; `GET /sdk/v1/checkpoints/{ref}/expand` returns the stored `{source_ref, version, messages, checkpoint}`
- `Cave.openai/anthropic/gemini/vertex(upstream_key)` → `Provider` that proxies through gateway; `Provider.raw(path, body)` is the escape hatch (mirrors the TS provider-client `raw`)
- `Cave.bedrock(region, endpoint="runtime")` → no-network first-party route descriptor; Runtime defaults to `/bedrock`, explicit Mantle returns `/bedrock/anthropic`, and `sdk_only=False` mirrors TS `sdkOnly`
- `Trace.tool(name, options, fn)` — calls `fn()` then POSTs a `tool.call` event
- `Trace.page_artifact(value, options)` / `Trace.artifacts.page(value, options)` — store value to `/sdk/v1/artifacts` (body `{value, options, workflow}`, incl. `workflow` to mirror TS) or return a cave-artifact stub. `artifacts.page` is the mirror-named entry point; `page_artifact` stays as a backwards-compat alias
- `Trace.model["openai"].responses.create(body, *, latency_class=None, tool_session_id=None)` — when `latency_class` is set, sends the `x-cave-async` header (`"true"` unless `"interactive"`); when `tool_session_id` is set, sends `x-cave-tool-session`, mirroring the TS `trace.model.openai.responses.create(body, {cave:{latencyClass, toolSessionId}})`
- `Trace.checkpoint(messages, options)` — POSTs to `/sdk/v1/checkpoints`; the gateway persists it (Valkey) and returns a reversible `source_ref` you can later expand via `GET /sdk/v1/checkpoints/{ref}/expand`
- `Cave.exporter(service_name=None)` → `OTelExporter`; `record_span(...)` maps GenAI fields to `gen_ai.*`, `export()` POSTs the OTLP/JSON batch to `/otlp/v1/traces` (headers via `otlp_headers()`)
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
- `sdk-python` and `sdk-ts` mirror the same field names and `/sdk/v1/*` contract — enforced by the shared parity suite (`tests/test_parity.py` + `../../parity/fixtures.json`), not just convention. A divergence is a CI failure. Change one SDK, change both **and** the fixtures

See ../../../CLAUDE.md (root) · ../../../docs/design.md
