# Changelog

## Unreleased

- `@caveman-ai/sdk/middleware` implements middleware protocol 1.1 and is marked
  `@experimental`:
  - capabilities are parsed tolerantly, cached for 300 s and refreshed
    single-flight
  - plans survive a policy-revision change
  - errors are handled as the protocol specifies, including `Retry-After`
  - a new circuit breaker opens after 5 consecutive or 10 of 20 failures and
    counts deadlines
  - per-candidate budgets replace the whole-call bypass
- Security: replacements must be `exact_ccr`, carry the recovery marker and
  handle, and be strictly shorter in UTF-8 bytes. `recovery: "none"` output is
  never applied.
- Fixed a per-request memory leak (`AbortSignal.any`). The runtime token no
  longer appears in `JSON.stringify` or `util.inspect`.
- Local data errors no longer count as runtime outages, and 4xx responses no
  longer clear cached capabilities.
- New options: `maxConcurrency`, `allowInsecureTransport`, `onDecision`,
  `tracer`, `meter`. Endpoint path prefixes work, and `deadlineMs` defaults to
  the runtime's advertised value.
- The constructor never throws: endpoint and option errors warn once and show
  up in `ready()`/`preflight()`. `decline()` no longer throws in strict mode.
- **Breaking (experimental subpath):**
  - `recovery()` returns `null` for an invalid scope.
  - `deleteSession()` returns a `SessionDeleteResult`.
  - `validateCapabilities` is removed.
  - Scopes are normalized: values that aren't valid tokens are hashed to
    `h-…`.
- Requests send `Caveman-Middleware-Features`, `Caveman-Middleware-Client` and
  W3C trace context.
- Node floor lowered to `>=22.12`. CommonJS `require()` works through
  require(esm).
- OTel exporter: `cacheCreationTokens` is emitted as
  `gen_ai.usage.cache_creation.input_tokens`.
- Release process: each release gets a GitHub Release with these notes and a
  CycloneDX SBOM of its dependency graph.

## 1.1.0 — 2026-09-15

- Added the `@caveman-ai/sdk/middleware` subpath: the dependency-free protocol client, validation, deadlines and receipts that `@caveman-ai/middleware` adapters build on.

## 1.0.0 — 2026-07-26

- Recorded stable TypeScript SDK API and `/sdk/v1/*` wire baseline.
- Pinned coordinated-major and parity rules with Python SDK.
