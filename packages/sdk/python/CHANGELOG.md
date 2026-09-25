# Changelog

## Unreleased

- The transport bounds the whole exchange (connect, proxy tunnel, TLS, headers
  and body) by the deadline, and never reuses connections the server closed
  while idle.
- `aclose()` never blocks on a stuck worker.
- The first capabilities fetch is single-flight. An unusable capabilities view
  is refreshed once. Server-advertised deadlines are capped.
- JSON is parsed strictly (no NaN; integer-valued floats become ints), and
  endpoint parsing is stricter.
- Warn-once rules, the decision event's `runtime_build`, and `adapter_error`
  for an unserializable manifest now match TypeScript.
- New `unsupported_provider` and `unsupported_request` reason codes.
- Exporter: cost is also emitted as `caveman.usage.cost_usd`.
- The warn-once line now reads `Caveman middleware passed content through
  unchanged: adapter=… reason=…`, the same as TypeScript. Log filters that
  match the old `Caveman middleware decision:` prefix need updating.
- Strict `ready()`/`preflight()` now report the first `decline()`.
- `decline()` accepts any catalog reason and an optional adapter id, which the
  warn-once line names. It never raises in strict mode.
- Python floor lowered from 3.13 to 3.11.
- `caveman_cloud.middleware` implements middleware protocol 1.1 and is marked
  experimental. Requests carry `Caveman-Middleware-Features`,
  `Caveman-Middleware-Client`, and `traceparent`/`tracestate`.
- Capabilities are parsed tolerantly and cached for 300 s with single-flight
  refresh. A new policy revision or transform version no longer rejects a plan.
- Only `exact_ccr` replacements that carry the recovery marker and handle and are
  shorter in UTF-8 bytes are applied. `recovery: "none"` is never applied, which
  closes a text-injection path.
- New circuit breaker: opens after 5 consecutive or 10 of 20 failures, stays
  open 30 s, then allows one probe. Deadlines count; local and 4xx errors don't.
  Ill-formed Unicode is reported as `unsupported_shape`. `Retry-After` is honored.
- The default deadline comes from capabilities (500 ms before the first fetch).
  Retrieve has its own 5 s deadline and its own pool.
- Async calls respect their deadline even while queued. Pools are rebuilt after
  `fork()`. `aclose()` resolves in-flight calls as `closed` instead of raising
  `CancelledError`.
- Transport: keep-alive reuse; DNS, connect and TLS inside the deadline;
  `HTTPS_PROXY`/`HTTP_PROXY`/`NO_PROXY`; `ssl_context`; a pluggable `transport`;
  endpoint path prefixes; `allow_insecure_transport`; `max_concurrency`.
- Per-candidate budgets replace the whole-call bypass at 256 candidates or 4096
  manifest items.
- Endpoint problems no longer raise at construction. `recovery()` returns `None`
  for an invalid scope. Scopes are normalized, so emails and spaces are hashed.
  `delete_session()` returns the result.
- New: warn-once logging on `caveman.middleware`, `on_decision` events
  (including `no_candidate`), opt-in OTel `tracer`/`meter`, and `as_sync()`,
  `ensure_sync()`, `ensure_async()`.
- The credential is kept out of `vars()`, `repr()` and pickling.
- `OTelExporter.record_span(cache_creation_tokens=)` emits
  `gen_ai.usage.cache_creation.input_tokens`. `gen_ai.usage.cost_usd` is
  deprecated; it stays through 1.x.
- Release process: each release gets a GitHub Release with these notes and a
  CycloneDX SBOM of its dependency graph.

## 1.1.0 — 2026-09-15

- Added `caveman_cloud.middleware`: the dependency-free protocol client, validation, deadlines and receipts that `caveman-middleware` adapters build on.

## 1.0.0 — 2026-07-26

- Recorded stable Python SDK API and `/sdk/v1/*` wire baseline.
- Pinned coordinated-major and parity rules with TypeScript SDK.
