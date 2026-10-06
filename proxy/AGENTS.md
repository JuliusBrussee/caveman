# proxy — the byte-safe standalone `caveman` proxy (commercial, binary-distributed)

A base-URL-swap reverse proxy: match → authenticate → inspect → byte-safe transform → upstream
→ meter. Single-operator, BYOK, **zero cloud dependencies**. It shares its provider adapters with
the managed gateway (the managed gateway imports them from here). `caveman start` launches the
`caveman-proxy` binary.

## Layout
- `providers/` — the shared, public byte-safe adapter set: `Adapter` interface + `Base` embed + `UsageScanner`/`ParseUsageBytes` (`adapter.go`), and `anthropic`/`openai`/`gemini`/`azureopenai`/`bedrock`/`vertex`/`openaicompat`. `ResolveUpstreamURL` takes `providers.RouteContext` (no control-plane coupling). Anthropic + OpenAI carry both prefixed and bare routes (`/v1/messages`, `/v1/chat/completions`). `vertex` preserves caller OAuth bearer or Express-mode Google API-key credentials for Gemini + Claude on Vertex AI (no signing, no environment-key fallback, no custom usage parser).
- `internal/gateway/` — the request lifecycle (`server.go` + `proxy.go`) behind three injected seams: `Authenticator`, `CredentialResolver`, `TelemetrySink`. Ports the managed loop with the fail-open fix.
- `internal/config/` — `caveman.yaml` loader + BYOK env-key resolution; unknown mode fails closed to `record`.
- `internal/store/` — `~/.caveman/caveman.db` SQLite spend store (`modernc.org/sqlite`, cgo-free); implements `TelemetrySink`.
- `internal/standalone/` — wiring: static `Auth`, BYOK `Creds`, adapter set, and the always-on SSRF-guarded client.
- `internal/identity/` — who calls the framework middleware routes (legacy token, token map, OIDC/JWT, mTLS) and the reloadable TLS listener config.
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

- **listener lifetime is not session lifetime**: `serve` never exits because a
  wrapper, native session, heartbeat, or idle timer expires. Legacy
  `CAVEMAN_NATIVE_IDLE_TIMEOUT` does not arm shutdown. The CLI never restarts a
  shared listener to change mode/recovery; an incompatible new wrap runs direct.
  Failed local startup also runs direct. Native session correlation entries may
  age out without affecting API traffic.
- **no default generation deadline**: `CAVE_GATEWAY_UPSTREAM_TIMEOUT_MS` defaults
  to `0` (no total request deadline). A positive value is an explicit operator
  cap and includes response streaming. Client cancellation still cancels upstream;
  connection setup, inbound header/upload limits, and idle keep-alive socket
  cleanup remain bounded separately. The replacement bound for an upstream that
  connects and then goes silent is the transport's response-header deadline
  (`CAVE_GATEWAY_RESPONSE_HEADER_TIMEOUT_MS`, default 900000, `0` disables) —
  headers only, so it never truncates a live stream.
- **response protocol controls streaming**: SSE/event-stream responses flush
  headers and chunks even without a JSON `stream` flag. Encoded requests keep
  `Content-Encoding` and bypass transforms. Interrupted response copies record
  an error and abort HTTP framing; they never become a clean successful EOF.
- **replay only when non-delivery is proven**: proxy transport retries are
  limited to connection-setup failures — `*net.OpError` with `Op` `dial` or
  `proxyconnect` (a proxy that is down fails as the latter, never as a dial). A failed upload/header read or truncated response
  does not prove an inference was unprocessed; do not automatically replay it.
  An explicit transformed-request 4xx still retries once with original bytes.
  See `docs/technical/proxy-reliability.md` at the repository root.
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
- **SSRF always on**: `standalone.StandaloneHTTPClient` guards every upstream dial (not gated on `CAVE_ENV`) using `ssrf.SelfHostedConfig` — NOT ManagedConfig, which ignores the allowlist and would make the escape hatch a silent no-op. `CAVE_SSRF_ALLOWLIST` opts loopback/private hosts back in (local model servers like Ollama; `localhost` as an entry covers 127.0.0.0/8 + ::1); metadata/link-local stay blocked in every mode. Provider traffic honours `HTTPS_PROXY`/`NO_PROXY` by default (#1001) — `upstream_proxy: env|off|<url>` / `CAVE_UPSTREAM_PROXY` — via `ssrf.Config.Proxy`, which validates IP-literal/localhost destinations before proxy selection and dials the (operator-configured) proxy address unguarded; `ssrf.NewHTTPClient` itself stays direct unless a caller sets `Proxy`, and a managed-mode Config carrying one is refused loudly (every request fails with `ssrf.ErrProxyInManagedMode`) rather than silently dialing direct. Adapters that pre-flight a resolved endpoint (bedrock, vertex) read the selector off the request context (`providers.WithUpstreamProxy`, published by `gateway.Handler` from the upstream transport) and use `ssrf.ValidateURLNoResolve` for proxied destinations: a proxy-only network has no outbound DNS, so resolving there failed the request before the proxy was ever consulted. `ca_bundle` / `CAVE_CA_BUNDLE` plus inherited `SSL_CERT_FILE`/`REQUESTS_CA_BUNDLE`/`NODE_EXTRA_CA_CERTS` append private roots for TLS inspection through `shared/platform/cabundle` (fail-closed parser shared with chhttp).
- **inbound token is consumed, never forwarded**: `CAVEMAN_AUTH_TOKEN` gates non-loopback listens (a middleware token map, OIDC issuer or TLS client CA also makes one legal; provider routes still accept only the token, and without it `Auth.closed` refuses every provider request off loopback); `standalone.Auth` deletes the matching `x-cave-api-key`/`Authorization` header before `Creds.Resolve` so the shared token can never be forwarded to a provider or classified as a provider credential; a bearer that is not the token survives untouched (Claude OAuth, `/chatgpt/`). `Authenticate` runs BEFORE `matchAdapter` so the 401/404 split cannot be used to enumerate routes, and every 401 goes through `Server.rejectUnauthorized`, which counts `cave_proxy_unauthorized_total` and warns with the path and remote host only. Health and the no-op `/caveman/keepalive` beacon stay unauthenticated, `/metrics` too unless `CAVEMAN_METRICS_TOKEN` is set; the `X-Caveman-Instance` header on `/health/live` is published only on a loopback bind. A token on a LOOPBACK listener is honored too (the local `caveman wrap` path sends none, so `runServe` warns). Bedrock credentials then come from `shared/platform/awscreds` (env → web identity → container → IMDSv2), never from the request; a PARTIAL env pair fails closed instead of falling through to an ambient role, and the winning source is logged once.
- **middleware identity is server-side and per route**: `middleware.Config.Identify` resolves an `identity.Principal` from the credential alone (bearer decides when present, else a verified client certificate); `ServeHTTP`'s `decode` checks `Principal.Allows(scope.Namespace)` on EVERY scoped route, so a new route that skips `decode` skips authorization. The legacy token stays `single_operator` with every namespace; JWT and certificate principals may never be named `single_operator`. Token and hash comparisons are constant-time over every entry. A token map or TLS file that fails to reload keeps the previous one. Tests: `identity/*_test.go` (negative JWT and mTLS cases), `middleware/identity_test.go`.
- **Postgres middleware store keeps SQLite's single-writer semantics per authority**: `store.PostgresMiddleware` serializes writers of one authority with a transaction-scoped advisory lock taken by `Scope` (then re-read), `SaveScope`, `Revoke`, `PurgeBatch`, `Receipt` and `SaveOriginal`; the sweep is one-replica (try-lock) and skips busy authorities and row-locked scopes. Counters are statement-level transition-table triggers striped by txid; a per-row trigger goes quadratic on bulk statements. Lock keys include `current_schema()`. Run the store suite against both backends with `CAVEMAN_TEST_POSTGRES_URL`; `middleware_concurrency_test.go` fails without the locks.
- **Auth scheme is preserved**: a key from an inbound `Authorization: Bearer` keeps `Scheme:"bearer"` on the `providers.Credential`; Anthropic and Gemini forward bearer credentials as bearer credentials (Claude/Gemini OAuth breaks if remapped to an API-key header). BYOK env keys and inbound `x-api-key` keep provider API-key mapping.
  On a named compat mount the header follows the wire protocol of the path:
  `/compat/<name>/v1/messages` gets `x-api-key` plus a default
  `anthropic-version`, every other path gets Bearer. A real inbound Bearer stays
  Bearer on every path. OpenCode Go rejects Bearer
  on `/v1/messages` (401 `Missing API key.`, 2026-09-03). The gateway env
  fallback puts the key in the header that the adapter emitted, so the upstream
  never gets two credentials.
- **fail-closed**: unknown route → 404; unknown mode → `record`.
- **subscription AND oauth compression is NOT account-gated**: non-PAYG sessions from Claude Code, Codex ChatGPT, Gemini CLI, and other routed clients take live-zone compression with no Caveman account, entitlement, or seat. `CAVEMAN_WRAP_ENTITLED` and every `WrapEntitled` field are **deleted**, not merely ignored — do not reintroduce them. Exactly four conditions remain, all technical and all fail-closed (`liveZoneCompressionAllowed`): the operator `subscription_compress` switch (empty/`live_zone` allow, `off` and any unknown value close it), the adapter must implement schema-aware `PrefixStabilizer` zones, recovery must run through the agent's own MCP `caveman_retrieve`, and a durable prefix cache must be wired. The dedicated Codex `/chatgpt/responses` route uses the OpenAI Responses stabilizer while preserving OAuth and `ChatGPT-Account-ID` headers; transformed 4xx responses retry once with exact original bytes. Legacy savings fields remain **tokens-only** — `compression_tokens_before/after` + `estimated_engine_o200k`, never booked compression dollars. New request-comparison and price-snapshot fields support separately labeled API equivalents in local stats; those values never enter subscription spend or legacy saved-dollar fields. LOCAL wrap only; managed gateway non-PAYG behavior is unchanged.
- **cache safety is byte-stable replacement**: a compressed live-zone turn becomes prefix on next request, so same logical message must re-serialize to deterministically identical bytes every time. Replacement is pure function of segment content (deterministic compressor + content-hash CCR marker), held in durable replacement cache and re-substituted below cache floor; new compression stays live-zone-only. Cache miss/write failure forwards original bytes. Cross-turn stability covers **anthropic, openai, azureopenai/openaicompat, and gemini** through `ExtractStabilizable`; `bedrock` and `vertex` expose no compressible blocks and stay pass-through. Anthropic uses declared `cache_control`; OpenAI/Gemini use latest-user/latest-tool zones against implicit provider caches. Replacement cache is SQLite spend store and must retain `journal_mode(WAL)` + `busy_timeout`.
- **pixel mode**: S4 lossy text→PNG (`pxpipe` port). Default allowlist is `claude-fable-5,gpt-5.6` via `CAVE_PIXEL_MODELS`; original request is always in CCR before transformed bytes are sent; savings stay inferred-only; any error is byte-identical pass-through.
- **route stage is optional and fails open** (`internal/cloudlink`, `internal/gateway/route.go`):
  it asks Cloud `POST /v1/route` only while the CLI is signed in with `modules.routing: true` in
  `$CAVEMAN_HOME/cloud.json`, for API-key Anthropic Messages / OpenAI chat or responses requests
  whose model is in the same-provider pool (subscription traffic never routes, ADR 0083 §7). The
  ask starts before compression (parse, ask, compress, route), waits at most 800 ms, carries the
  caller's models, counts, what the request declares (the raw values of eleven allowlisted agent
  headers, never cut: a value over 256 bytes, 16 KiB for Codex's turn metadata, which also comes
  from the body's `client_metadata`, is left out; a forked Claude Code child's spawn-call
  `subagent_type` as `x-caveman-agent`; its tool names, effort and thinking type), what the
  session's previous request ran (the served model, the effort in force, the provider's input and
  cache token counts, its age) and Cloud's opaque per-session state, plus, on a turn's first ask,
  the raw text of the latest human turn, the one before it and the end of the agent's last reply
  (contracts `route-ask-v1`; Cloud picks the model and the effort and no routing logic lives
  here). Its answer is cached per ask (session, provider, model, turn number, latest human text;
  an input group carrying a tool result is no human turn, text riding along included) so a tool
  loop never switches model or effort mid-turn; a request the agent labels compaction or auxiliary
  (Claude Code's request class and compaction flag, OpenCode's title/summary/compaction agent,
  Codex's subagent kind and turn-metadata request kind) is asked on its own, without the text. The
  session is `x-cave-session`, else the agent's session header (a Codex thread, `thread-id`, is
  its own, its parent `x-codex-parent-thread-id`); a Claude Code child (`x-claude-code-agent-id`)
  is a session of its own and sends its parent's state; a session id guessed from timing is not
  used. State, previous-request facts and marks live in bounded in-memory LRUs (1024), never on
  disk. The answer sets Responses `reasoning.effort`, chat `reasoning_effort` or Anthropic
  `output_config.effort`; Anthropic `effort_mode: message` instead inserts the byte-identical mark
  `{"role":"system","content":[],"output_config":{"effort":…}}` (beta
  `mid-conversation-output-config-2026-07-01` appended to `anthropic-beta`) before the last user
  turn, or at the end after a tool result. Only `effort_mode: message` ever starts marks; a "top"
  answer on history carrying the session's marks goes in as one more mark (never wiping them; for
  a compaction or side request, that request's only), and on any other history sets the top-level
  field. Marks are remembered per session with a salted hash of the message before each
  (`cache_control` and thinking blocks left out, so a heal's strip keeps them) and replayed at the
  same places on every later request to a model that already took them (the agent resends history
  without them), count_tokens of a known session or a forked child's parent included (it adds
  nothing to a history without marks). Without an effort from Cloud (a failure, routing off,
  effort "") a history without the session's marks goes as the agent sent it, top-level field
  included (one cache restart where Cloud's was, nothing inserted, no beta), except that a
  remembered heal (drop_block with its beta, or a strip) keeps applying. One with marks runs at
  the request's own top-level effort, or, when it sets none, at the model's default effort
  (Cloud's `default_effort`, kept per session, a forked child taking its parent's), marked when it
  differs from the one in force; only with no default known does such a request go as the agent
  sent it, without the marks, which the session then forgets (its later thinking blocks then lose
  their binding) unless the request went to another model. The top-level field is fixed at the
  session's first per-message request: the routed effort on a fresh conversation whose request
  sets one (kept there, also for later requests that set none), else the request's own; a routed
  effort for a request that sets none goes in as a mark, first on a fresh conversation. An anchor
  that no longer matches drops that mark and every later one; compaction and side requests never
  change them, and a body matching none of them (an unlabeled side request, compacted history)
  gets marks of its own, kept for up to four request shapes (first message, length, last message),
  which take the session over only from a longer request continuing that conversation (same first
  message, its last one where it was); the session's marks and fixed top-level field then wait
  there in turn. A thinking-binding 400 on the asked model of a `/messages` request retries once
  only on a session the route stage changed (it was on for the request, the session carries its
  marks or heal, or another model served one of its requests) whose body sets no
  `thinking.block_binding`: with `thinking.block_binding.prefix_mismatch_behavior: "drop_block"`
  (beta `thinking-binding-controls-2026-08-01`) when the block is bound to a different
  conversation and the request's thinking is adaptive or enabled, then sent on every later request
  (count_tokens and forked children included); else (Sonnet 5.5 `between_tools`, no thinking
  field, a tampered signature) without thinking blocks from the failing message on, and later
  requests on the same history (same first message, same message at its end) strip the same range
  up front (a second strip widens it; the strip stays, the history as served, even if the agent
  later sets its own `block_binding`). A drop_block retry refused with a 400 naming the binding,
  `block_binding` or its beta moves the session to the strip path, a strip refused that way ends
  its heals (the first lasts for the session, as it depends on the model and thinking type; the
  second goes with the marks, cleared when they are forgotten; a side request never clears either,
  nor the strip; a 429, 5xx or other 400 on the retry teaches nothing). Routing switched off after
  a restart surfaces that 400. A refused mark retries once with top-level effort only and, once
  served, latches per-message effort off for the session (a model that refused takes Cloud's
  effort top-level); compressed 400s are decoded first. A Cloud error, timeout, 401/403,
  `allowance` or `billing_limit` answer keeps the asked model and pauses new asks (1 min; 10 min
  for 401/403 and billing_limit; until the 1st for allowance). Refusals and limits land in
  `$CAVEMAN_HOME/route-state.json` (with Cloud's notice) for `caveman status`; a new login lifts
  the pause. A provider 4xx on the routed model replays the original bytes on the asked model; a
  429 on the asked model of a request whose bytes the route stage changed (effort, marks, strip,
  drop_block, a heal retry) is returned as is. When the model moved, the agent's copy of the
  answer names the model it asked for (Claude Code drops its thinking on another name): a JSON
  answer's top-level `model`, and in a stream every `"model":"<sent>"` pair, rewritten
  incrementally across reads; the upstream is asked for an identity answer, a compressed one is
  left as it is, and usage, stats and `last` read the provider's bytes. The pass-through header
  and encoded bodies never route, nor does any origin but the provider's own API (Azure,
  OpenRouter, LiteLLM, a custom base URL); record mode does (routing is its own module). A limit
  pause asks `/me` at most every 15 min and lifts once routing is no longer limited or the plan
  changed. runtime/v1 events go whenever signed in, routing on or not, unless the CLI telemetry
  opt-out is set (ADR 0085 §5; `route.outcome: off` when no route stage ran): in-memory batches of
  at most 500, dropped on failure, at `/me`'s `data.level`. The runtime never invents a level:
  none in `/me` means `counts`, an unreadable `/me` lowers it to `counts`, `off` or no level sends
  nothing. Model ids from other origins go as `custom`. Signing in or out never changes another
  stage.
- **boundary**: this is public code — it must never import the managed-cloud lane. `make check-boundaries` enforces it.

See ../../CLAUDE.md (root)
