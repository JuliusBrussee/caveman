# proxy — the byte-safe standalone `caveman` proxy (commercial, binary-distributed)

A base-URL-swap reverse proxy: match → authenticate → inspect → byte-safe transform → upstream
→ meter. Single-operator, BYOK, **zero cloud dependencies**. It shares its provider adapters with
the managed gateway (the managed gateway imports them from here). `caveman start` launches the
`caveman-proxy` binary.

## Layout
- `providers/` — the shared, public byte-safe adapter set: `Adapter` interface + `Base` embed + `UsageScanner`/`ParseUsageBytes` (`adapter.go`), and `anthropic`/`openai`/`gemini`/`azureopenai`/`bedrock`/`vertex`/`openaicompat`. `ResolveUpstreamURL` takes `providers.RouteContext` (no control-plane coupling). Anthropic + OpenAI carry both prefixed and bare routes (`/v1/messages`, `/v1/chat/completions`). `vertex` is a bearer pass-through proxy for Gemini + Claude on Vertex AI (no signing, no custom usage parser).
- `internal/gateway/` — the request lifecycle (`server.go` + `proxy.go`) behind three injected seams: `Authenticator`, `CredentialResolver`, `TelemetrySink`. Ports the managed loop with the fail-open fix.
- `GET /health/ready` identifies the runtime and advertises `billing: "byok"` because the standalone proxy forwards the caller's selected provider credential. The managed twin advertises `managed`; SDK dollar budgets fail closed on missing/unknown billing provenance.
- `internal/config/` — `caveman.yaml` loader + BYOK env-key resolution; unknown mode fails closed to `record`.
- `internal/store/` — `~/.caveman/caveman.db` SQLite spend store (`modernc.org/sqlite`, cgo-free); implements `TelemetrySink`.
- `internal/standalone/` — wiring: static `Auth`, BYOK `Creds`, adapter set, and the always-on SSRF-guarded client.
- `internal/nativeruntime/` — normalized local-agent lifecycle, Task Contract,
  Decision Ledger, typed CCR capture/masking, child evidence merge, honest
  receipts, user-only Unix socket / Windows named pipe, and wrap-owned idle exit.
- `internal/repointel/` — deterministic local repository map, task evidence,
  conservative test impact, and optional-Scout recommendation. No model/network.
- `internal/nativepack/` — embedded compiled Core/skill policy; generated from
  `public/skills`, fail-closed on schema/version drift.
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
- **native marker is local-route-only**: HMAC session marker is emitted only
  after local runtime + route proof and stripped before capture/hash/provider.
  Direct, managed, proxy-disabled, invalid, and conflicting paths never correlate.
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
- **OpenAI cache-key affinity is not cache placement or savings proof**:
  `openai-prompt-cache-key` adds routing metadata only. On GPT-5.6, the
  provider's implicit latest-message breakpoint can repeatedly write a changing
  suffix, and writes cost 1.25× uncached input. The usage parser normalizes
  official nested `cache_write_tokens` from both Responses
  `input_tokens_details` and Chat `prompt_tokens_details` into
  `CacheCreationInputTokens`; applying the affinity key never mints verified
  savings. Explicit `prompt_cache_breakpoint` placement is not implemented.
- **SSRF always on**: `standalone.StandaloneHTTPClient` guards every upstream dial (not gated on `CAVE_ENV`) using `ssrf.SelfHostedConfig` — NOT ManagedConfig, which ignores the allowlist and would make the escape hatch a silent no-op. `CAVE_SSRF_ALLOWLIST` opts loopback/private hosts back in (local model servers like Ollama; `localhost` as an entry covers 127.0.0.0/8 + ::1); metadata/link-local stay blocked in every mode.
- **Auth scheme is preserved**: a key from an inbound `Authorization: Bearer` keeps `Scheme:"bearer"` on the `providers.Credential`; Anthropic and Gemini forward bearer credentials as bearer credentials (Claude/Gemini OAuth breaks if remapped to an API-key header). BYOK env keys and inbound `x-api-key` keep provider API-key mapping.
- **fail-closed**: unknown route → 404; unknown mode → `record`.
- **subscription AND oauth compression is NOT account-gated**: non-PAYG sessions from Claude Code, Codex ChatGPT, Gemini CLI, and other routed clients take live-zone compression with no Caveman account, entitlement, or seat. `CAVEMAN_WRAP_ENTITLED` and every `WrapEntitled` field are **deleted**, not merely ignored — do not reintroduce them. Exactly four conditions remain, all technical and all fail-closed (`liveZoneCompressionAllowed`): the operator `subscription_compress` switch (empty/`live_zone` allow, `off` and any unknown value close it), the adapter must implement schema-aware `PrefixStabilizer` zones, recovery must run through the agent's own MCP `caveman_retrieve`, and a durable prefix cache must be wired. The dedicated Codex `/chatgpt/responses` route uses the OpenAI Responses stabilizer while preserving OAuth and `ChatGPT-Account-ID` headers; transformed 4xx responses retry once with exact original bytes. Rows are **tokens-only** — `compression_tokens_before/after` + `estimated_engine_o200k`, never compression dollars. LOCAL wrap only; managed gateway non-PAYG behavior is unchanged.
- **recovered bytes are never a compression candidate**: a `tool_result` answering the agent's own `caveman_retrieve` is excluded by every adapter (`providers.IsRecoveryToolName`; anthropic keys off `tool_use_id`, OpenAI off `tool_call_id`/`call_id`, gemini off the `functionResponse` name). This is not an optimization — the replacement is a deterministic function of block content and is memoised in the prefix cache, so collecting a recovery result substitutes the SAME elision straight back in and the agent has no path to its own data at all. Measured 2026-08-06: the agent retrieved, reported "Same truncation", and re-read one file seven times. Regression test `internal/gateway/recovery_exempt_test.go`.
- **cache safety is byte-stable replacement**: a compressed live-zone turn becomes prefix on next request, so same logical message must re-serialize to deterministically identical bytes every time. Replacement is pure function of segment content (deterministic compressor + content-hash CCR marker), held in durable replacement cache and re-substituted below cache floor; new compression stays live-zone-only. Cache miss/write failure forwards original bytes. Cross-turn stability covers **anthropic, openai, azureopenai/openaicompat, and gemini** through `ExtractStabilizable`; `bedrock` and `vertex` expose no compressible blocks and stay pass-through. Anthropic uses declared `cache_control`; OpenAI/Gemini use latest-user/latest-tool zones against implicit provider caches. Replacement cache is SQLite spend store and must retain `journal_mode(WAL)` + `busy_timeout`.
- **tool-schema annotation strip is DEFAULT OFF and separately opted in**: `toolschema_strip: annotations` / `CAVEMAN_TOOLSCHEMA_STRIP=annotations` (`""`, `off`, and any unrecognized value all mean off, normalized in the config loader AND re-checked at the decision point). It is a SECOND gate ON TOP of the four `liveZoneCompressionAllowed` conditions, which it reuses rather than restates, plus a THIRD: the session ledger's freeze registry (`ledger.LeverAllowed`) — it never runs in `record` mode, under `caveman.pass-through.v1`, under a compiled Cave Build, or in a session whose harm tripwire has frozen it. S4 + CCR (the original catalog is stored before the rewritten bytes ship, disclosed as `x-caveman-toolschema-recovery-handle` and on the row's `RecoveryHandle`), promotable only under the local-wrap clause. It removes `$schema`/`title`/`examples`/`deprecated` ONLY inside schemas reached through `input_schema`/`inputSchema`/`parameters` and the JSON-Schema applicator allowlist — never from a tool envelope, `annotations` (whose `title` is the tool's display NAME), `_meta`, or a vendor extension. `ExtractToolCatalog` covers the **anthropic-messages** shape only; openai/gemini and `count_tokens` fail closed. It mints NOTHING: no tokens, no ratio, no dollars — the replay grid prices it. Its version is folded into the prefix-cache scope key so toggling it is a deliberate epoch rollover, not a silent partial cold write.
- **cache-breakpoint planner is DEFAULT OFF**: `breakpoint_plan: frontier` / `CAVEMAN_BREAKPOINT_PLAN=frontier` (`""`, `off`, and any unrecognized value all mean off, normalized in the config loader AND re-checked at the decision point). It is metadata-only — Anthropic `cache_control` and OpenAI `prompt_cache_key` are provider hints on the UPSTREAM request, never model-visible bytes — so it is byte-safe, but it stays off until the escalation ladder prices it. **It runs from the `default:` transform branch ONLY** (i.e. `recommend`/`shadow`/`canary`/`active`) — never `record`, `compress`, or `pixel` — so a wrapped Claude Code session, which runs in `compress` by default, does not reach it at all. It is not skipped for subscription traffic the way `ApplyProviderNativeTransforms` is, but both live arms are payg-gated, so today it produces nothing for subscription/OAuth; that reach exists for the reworked lookback guard, which must see caching harnesses. Two arms that never mix: with NO `cache_control` anywhere it places its own deterministic set (tools tail → system tail → frontier), **payg only**; with existing `cache_control` it NEVER moves or removes one, and the only thing it may still add is the composition-dead-zone frontier breakpoint — when every existing marker is on the TOOL CATALOG and none on a content block (the shape the sibling `anthropic-cache-breakpoints` optimizer leaves behind), the conversation is uncached, so the frontier goes in, payg-only and budget-capped at Anthropic's max 4. **The 20-block lookback guard is DISABLED** (`lookbackPlan` returns nil, pinned by test): the lookback finds only entries PRIOR REQUESTS WROTE, so an insertion placed relative to the current body's markers moves every turn, lands where nothing was written, and pays the 1.25x write forever without ever reading. The rework is specified in place — cross-request anchor via the session ledger, insert at `lastWrittenIndex+19`, re-emit that ABSOLUTE index every turn. It mints NOTHING — `cache-breakpoint-plan` is deliberately absent from `cacheOptimizerIDs`, so planner-placed Anthropic breakpoints do not attribute `provider_causal_cache` savings; promoting it into the minting set is a separate reviewed change. OpenAI's arm hashes the session id (sha256, 16 hex chars), never forwards the raw `x-cave-session`, and never overwrites an existing key — the prefix-signature optimizer in `cache_key.go` runs earlier and WINS.
- **harm tripwire / session ledger**: `internal/gateway/ledger.go` keys a bounded LRU (1024) on `x-cave-session`; **no session header = no entry and the whole mechanism is inert**. Every lever asks `LeverAllowed` before running. A lever active on request N earns a strike when request N+1's `cache_creation_input_tokens` exceeds BOTH an absolute floor (50k) AND 3x the session's baseline mean; 3 strikes freeze that lever for the session, one-way (never un-freezes), fail-open to pass-through. The baseline EXCLUDES calls already judged anomalous — a plain running mean folds each spike into the bar the next spike is measured against and goes blind to persistent regressions. Disclosure is `x-caveman-tripwire: <lever>=frozen`, and it appears from the request AFTER the one that tripped, because usage is only known once the response is already streaming. Thresholds are conservative by construction, pending replay-derived values.
- **pixel mode**: S4 lossy text→PNG (`pxpipe` port). Default allowlist is `claude-fable-5,gpt-5.6` via `CAVE_PIXEL_MODELS`; original request is always in CCR before transformed bytes are sent; savings stay inferred-only; any error is byte-identical pass-through.
- **mask what cannot be summarized in place; elide what can**: `nativeruntime.afterTool` replaces an over-threshold tool output with a `ccr://` pointer stub, but NOT when `Engine.Detect` classifies it as `json`, `tabular`, or `log` (logfmt + NDJSON) — the classes with a field grammar, which the elision engine compresses in place into rows plus stated invariants (`all state=charged`, `status: delivered×18 attempted×17`, `wh-5000..wh-5059 all 60 present`). Masking those first destroys every fact AND costs more: the agent sees no row, then recovery re-enters the FULL original through the recovery-exempt path — whole page + stub + an extra turn, strictly worse than no wrap. Measured 2026-08-08 on the shipped default (`compress` → native policy `safe` → profile `full-safe` → mask on): inventory-mismatch and webhook-delivery-gaps scored 0/6 with 27–97 recovery calls, while rate-limit-forensics scored 3/3 at ~35% cheaper for the sole reason that its pages sat under the threshold. Capture is unaffected (the object is still stored, recovery still available); only the replacement is skipped, and the size rule for still-maskable classes is unchanged. Fails toward masking: no classifier → mask, so the fallback is bounded context. Tests: `mask_elidable_test.go`.
- **boundary**: this is public code — it must never import the managed-cloud lane. `make check-boundaries` enforces it.

See ../../CLAUDE.md (root)
