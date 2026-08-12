# proxy — the byte-safe standalone `caveman` proxy (commercial, binary-distributed)

A base-URL-swap reverse proxy: match → authenticate → inspect → byte-safe transform → upstream
→ meter. Single-operator, BYOK, **zero cloud dependencies**. It shares its provider adapters with
the managed gateway (the managed gateway imports them from here). `caveman start` launches the
`caveman-proxy` binary.

## Layout
- `providers/` — the shared, public byte-safe adapter set: `Adapter` interface + `Base` embed + `UsageScanner`/`ParseUsageBytes` (`adapter.go`), and `anthropic`/`openai`/`gemini`/`azureopenai`/`bedrock`/`vertex`/`openaicompat`. `ResolveUpstreamURL` takes `providers.RouteContext` (no control-plane coupling). Anthropic + OpenAI carry both prefixed and bare routes (`/v1/messages`, `/v1/chat/completions`). `vertex` is a bearer pass-through proxy for Gemini + Claude on Vertex AI (no signing, no custom usage parser).
- `internal/gateway/` — the request lifecycle (`server.go` + `proxy.go`) behind three injected seams: `Authenticator`, `CredentialResolver`, `TelemetrySink`. Ports the managed loop with the fail-open fix.
- `internal/config/` — `caveman.yaml` loader + BYOK env-key resolution; unknown mode fails closed to `record`.
- `internal/store/` — `~/.caveman/caveman.db` SQLite spend store (`modernc.org/sqlite`, cgo-free); implements `TelemetrySink`.
- `internal/standalone/` — wiring: static `Auth`, BYOK `Creds`, adapter set, and the always-on SSRF-guarded client.
- `cmd/caveman-proxy/` — binary: `serve` (default), `stats`, and content-blind
  `agent-evidence --session --build --plan`. Evidence query returns only exact
  provider usage, request hashes, declared context/plan identity, ordered
  provider-prefix component hashes, actual transform IDs/counts, and CCR handle;
  basis is always `inferred`, verified dollars always zero.

## Conventions
- Build/test: `make product-build PRODUCT=proxy` / `make product-test PRODUCT=proxy`.
- Tests inject a plain `*http.Client` to reach loopback stubs; the binary uses the SSRF-guarded client.
- New provider/optimizer work goes in `providers/` (shared) — change it once, both proxies get it.

## Gotchas (honesty invariants — correctness, not style)
- **byte-safe**: `record` mode never transforms; on transform error the ORIGINAL bytes are forwarded (HTTP 200, fail-open) — never a 400.
- **request-wide opt-out**: `x-cave-transforms: caveman.pass-through.v1` suppresses every request transform path — compress, pixel, and provider-native — not only compiled plan routes. Tests cover all three modes.
- **no-fake-savings**: standalone records `Basis: "inferred"` on every row; it never writes `verified` and never re-projects to a monthly figure.
- **practice join**: local learn sinks carry additive `practice_id`; one
  fail-closed mapping table owns sink→practice and unknown sinks keep `""`.
  The historical `subagent_overuse` sink is count-only and deliberately has no
  practice id: spawn count cannot reactivate the retired
  `context-exploration-offload` opportunity or prove any spawn unnecessary.
- **local trial heuristics are not actuation evidence**: a model name never emits
  the retired `model-right-sizing` id, and provider plus positive cost never
  emits a cache move because neither proves stable-prefix eligibility. Legacy
  rows for those identities are hidden at read time. Compression replay reports
  one trial's local engine `estimated_engine_o200k` before/after shape with zero
  dollars and low confidence; it is not provider-counted, a rate, an invoice,
  causal/verified savings, or task-outcome evidence.
- **Anthropic automatic caching is experimental observation only**:
  `anthropic-automatic-prompt-cache` is a typed, default-off manual policy
  experiment and may add only Anthropic's top-level 5-minute marker on direct
  Messages API requests. Managed traffic additionally requires server-attested
  official Anthropic origin; custom or provenance-unknown origins lose the flag
  before the adapter. It is mutually
  exclusive with the explicit `anthropic-cache-breakpoints` transform and any
  caller `cache_control`; Bedrock and count-tokens requests stay byte-identical.
  An applied marker records only its optimizer id plus actual provider usage and
  cost. It has no practice, recipe, generic mode/candidate activation, ledger
  tuple, inferred savings, or verified-savings path (cache-only and forged IDs
  are excluded from the counted-baseline method too); evaluate it by manual
  paired observation because shared provider cache state can contaminate an A/B.
- **SSRF always on**: `standalone.StandaloneHTTPClient` guards every upstream dial (not gated on `CAVE_ENV`) using `ssrf.SelfHostedConfig` — NOT ManagedConfig, which ignores the allowlist and would make the escape hatch a silent no-op. `CAVE_SSRF_ALLOWLIST` opts loopback/private hosts back in (local model servers like Ollama; `localhost` as an entry covers 127.0.0.0/8 + ::1); metadata/link-local stay blocked in every mode.
- **Auth scheme is preserved**: a key from an inbound `Authorization: Bearer` keeps `Scheme:"bearer"` on the `providers.Credential`; Anthropic and Gemini forward bearer credentials as bearer credentials (Claude/Gemini OAuth breaks if remapped to an API-key header). BYOK env keys and inbound `x-api-key` keep provider API-key mapping.
- **fail-closed**: unknown route → 404; unknown mode → `record`.
- **subscription AND oauth compression is NOT account-gated**: non-PAYG sessions from Claude Code, Codex ChatGPT, Gemini CLI, and other routed clients take live-zone compression with no Caveman account, entitlement, or seat. `CAVEMAN_WRAP_ENTITLED` and every `WrapEntitled` field are **deleted**, not merely ignored — do not reintroduce them. Exactly four conditions remain, all technical and all fail-closed (`liveZoneCompressionAllowed`): the operator `subscription_compress` switch (empty/`live_zone` allow, `off` and any unknown value close it), the adapter must implement schema-aware `PrefixStabilizer` zones, recovery must run through the agent's own MCP `caveman_retrieve`, and a durable prefix cache must be wired. The dedicated Codex `/chatgpt/responses` route uses the OpenAI Responses stabilizer while preserving OAuth and `ChatGPT-Account-ID` headers; transformed 4xx responses retry once with exact original bytes. Rows are **tokens-only** — `compression_tokens_before/after` + `estimated_engine_o200k`, never compression dollars. LOCAL wrap only; managed gateway non-PAYG behavior is unchanged.
- **cache safety is byte-stable replacement**: a compressed live-zone turn becomes prefix on next request, so same logical message must re-serialize to deterministically identical bytes every time. Replacement is pure function of segment content (deterministic compressor + content-hash CCR marker), held in durable replacement cache and re-substituted below cache floor; new compression stays live-zone-only. Cache miss/write failure forwards original bytes. Cross-turn stability covers **anthropic, openai, azureopenai/openaicompat, and gemini** through `ExtractStabilizable`; `bedrock` and `vertex` expose no compressible blocks and stay pass-through. Anthropic uses declared `cache_control`; OpenAI/Gemini use latest-user/latest-tool zones against implicit provider caches. Replacement cache is SQLite spend store and must retain `journal_mode(WAL)` + `busy_timeout`.
- **pixel mode**: S4 lossy text→PNG (`pxpipe` port). Default allowlist is `claude-fable-5,gpt-5.6` via `CAVE_PIXEL_MODELS`; original request is always in CCR before transformed bytes are sent; savings stay inferred-only; any error is byte-identical pass-through.
- **boundary**: this is public code — it must never import the managed-cloud lane. `make check-boundaries` enforces it.

See ../../CLAUDE.md (root)
