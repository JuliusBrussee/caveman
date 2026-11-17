# proxy

The standalone Caveman proxy, built as the `caveman-proxy` binary. An agent points its provider base URL at it (default `127.0.0.1:8787`); it matches the route, authenticates, applies byte-safe transforms, forwards upstream with the caller's own credential (BYOK) and records spend in a local SQLite store. It needs no cloud service. `caveman start` and `caveman wrap` launch it.

## Layout

- `providers/`: the byte-safe adapters (`anthropic`, `openai`, `gemini`, `azureopenai`, `bedrock`, `vertex`, `openaicompat`) and, in `adapter.go`, the `Adapter` interface, `Base`, `UsageScanner`, `ParseUsageBytes` and `IsRecoveryToolName`. The managed gateway imports this package too, so a provider fix goes here once. `ResolveUpstreamURL` takes `providers.RouteContext`; keep control-plane types out.
- `internal/gateway/`: the request lifecycle (`server.go`, `proxy.go`) behind three seams, `Authenticator`, `CredentialResolver`, `TelemetrySink`. Also compression, cache safety (`prefix_monitor.go`, `raw_pin.go`), pixel, the harm tripwire (`ledger.go`), cache warming (`cache_warm*.go`) and the route stage (`route*.go`, `chatgpt_auto.go`).
- `internal/config/`: `caveman.yaml` loader and BYOK env-key resolution.
- `internal/store/`: `~/.caveman/caveman.db` (`modernc.org/sqlite`, no cgo): spend rows, the replacement and prefix caches, `caveman learn`, and the Postgres middleware store.
- `internal/standalone/`: wiring: static `Auth`, BYOK `Creds`, the adapter set and the SSRF-guarded client.
- `internal/identity/`, `internal/middleware/`: who may call the framework middleware routes (legacy token, token map, OIDC/JWT, mTLS) and the reloadable TLS listener.
- `internal/nativeruntime/`, `internal/nativehook/`: local agent lifecycle, Task Contract, Decision Ledger, CCR capture and masking, receipts, the user-only Unix socket / Windows named pipe.
- `internal/repointel/`: deterministic repository map and task evidence. No model or network calls.
- `internal/nativepack/`: embeds `native-pack.generated.json`, written by `node skills/compile.mjs`. Do not edit it by hand.
- `internal/gitsafe/`: the only way to run `git` against a user directory.
- `internal/cloudlink/`, `internal/pool/`, `internal/translate/`: optional Auto routing through Caveman Cloud, the provider logins a user adds, and wire translation between Messages, Responses and chat.
- `cmd/caveman-proxy/`: `serve` (default), `stats`, `agent-evidence`, `learn`, `status`, `version` and helper subcommands.

## Build and test

From the repo root:

```bash
go build ./proxy/...
go test ./proxy/...
go test -count=1 -race ./proxy/internal/store/... ./proxy/internal/middleware/...
CAVEMAN_TEST_POSTGRES_URL=postgres://... go test ./proxy/internal/store/...   # Postgres backend too
python tests/verify_repo.py   # git hardening and wrap recovery tool name checks
```

`engine-ci.yml` runs `go build/vet/test ./...` and the race run against Postgres; `ci.yml` runs the Go suite on macOS and builds the Docker image. Tests inject a plain `*http.Client` to reach loopback stubs; the binary always uses the SSRF-guarded client.

## Invariants

Reliability (details in `docs/technical/proxy-and-providers.md#reliability`):
- `serve` never exits because a wrapper, session, heartbeat or idle timer ends; `CAVEMAN_NATIVE_IDLE_TIMEOUT` does not arm shutdown. The CLI never restarts a shared listener to change mode; an incompatible wrap or a failed start runs direct.
- No total request deadline by default: `CAVE_GATEWAY_UPSTREAM_TIMEOUT_MS=0`. A silent upstream is bounded by `CAVE_GATEWAY_RESPONSE_HEADER_TIMEOUT_MS` (default 900000, headers only, so it never cuts a live stream).
- An event-stream response is flushed as it arrives, whatever the request's `stream` flag. An interrupted copy records an error and aborts HTTP framing; it never ends as a clean EOF.
- Retry only when non-delivery is proven: `*net.OpError` with `Op` `dial` or `proxyconnect`. A failed upload or truncated response may already have run the inference; never replay it.
- A transformed request that gets a 4xx retries once with the original bytes, except a 429 with `Retry-After`, which is returned as is. An accepted retry pins the conversation raw.

Byte safety and honesty:
- `record` mode never transforms. A transform error forwards the original bytes (fail-open), never a 400. Unknown route is 404; unknown mode fails closed to `record`.
- `x-cave-transforms: caveman.pass-through.v1` turns off every request transform: compress, pixel and provider-native.
- Encoded request bodies keep `Content-Encoding` and skip transforms (the `/chatgpt` route decodes zstd, transforms and re-encodes).
- Standalone rows are always `Basis: "inferred"`. Nothing here writes `verified` or projects a monthly figure. `GET /health/ready` advertises `billing: "byok"`; SDK dollar budgets fail closed on missing or unknown billing.
- `caveman learn` prices spend only from provider-counted usage at the dated `shared/provider-catalog` rate (basis `provider_counted_x_published_rate`). A model with no catalog row is never priced at a sibling's rate. Savings need an attribution rung (`learn_attribution.go`), and totals are never summed across rungs. Subscription traffic has no marginal cost and every priced surface says so.
- Learn sinks carry `practice_id` through one fail-closed mapping. `subagent_overuse` and the subagent spend sink are count/cost only, with no practice id: spawn count cannot prove a spawn was unnecessary, so they must not revive the retired `context-exploration-offload` opportunity.
- A model name or a positive cost is not evidence for the retired `model-right-sizing` or a cache move. Compression replay reports local `estimated_engine_o200k` with zero dollars.
- `anthropic-automatic-prompt-cache` is a default-off experiment: top-level 5-minute marker on direct Messages requests only, exclusive with `anthropic-cache-breakpoints` and any caller `cache_control`, and it mints no savings. `openai-prompt-cache-key` is routing metadata only, not savings proof.
- The cache-breakpoint planner defaults to `frontier` in optimization modes (`breakpoint_plan: off` / `CAVEMAN_BREAKPOINT_PLAN=off`; unknown values mean off) and never runs in record, compress or pixel mode. It changes no model-visible bytes and `cache-breakpoint-plan` stays out of `cacheOptimizerIDs`.

Cache safety (#1105). Keep `internal/gateway/cache_prefix_invariant_test.go` (with `FuzzCachePrefixInvariant`) green:
- If request R repeats the bytes of an earlier request P's cached prefix, R's forwarded bytes over that range equal P's, and every request caches at least what the client asked for. Exceptions, both re-caching raw: a raw retry, and a compressed PAYG conversation without MCP recovery that streams.
- Replacement is a pure function of block content, memoised in the durable replacement cache, and the first decision for a block (replaced or raw, `RawDecisionHandle`) wins forever. A frozen block with no decision goes out raw. Raw decisions are global and durable; bounding them needs a TTL of at least 24 h (OpenAI extended caching).
- After a raw retry, a later request follows the longest lineage it extends, ties going raw. Pins and lineages live in the PrefixCache (`rawpin`, `lineage`).
- New compression is live-zone only. The tripwire in `prefix_monitor.go` records `requests.cache_bust_cause`; any `caveman` value is a bug.
- Cross-turn stability covers anthropic, openai, azureopenai/openaicompat and gemini through `ExtractStabilizable`; bedrock and vertex stay pass-through.
- The replacement cache must keep `journal_mode(WAL)` and `busy_timeout`.
- A `tool_result` answering the agent's own `caveman_retrieve` is never compressed (`providers.IsRecoveryToolName`): the memoised elision would come straight back and the agent could never read its data. Tests: `internal/gateway/recovery_exempt_test.go`; `verify_repo.py` pins the wrap plugin's tool name.

Compression gates:
- Subscription and OAuth compression is not account-gated. `CAVEMAN_WRAP_ENTITLED` and `WrapEntitled` were deleted; do not bring them back. `liveZoneCompressionAllowed` has exactly four fail-closed conditions: `subscription_compress` allows it (empty or `live_zone`), the adapter implements `PrefixStabilizer`, recovery runs through the agent's MCP `caveman_retrieve`, and a durable prefix cache is wired. Savings fields stay tokens-only.
- The tool-schema strip is default off (`toolschema_strip: annotations` / `CAVEMAN_TOOLSCHEMA_STRIP`). It needs those four conditions plus `ledger.LeverAllowed`, covers the anthropic-messages shape only (`ExtractToolCatalog`), never touches a tool envelope, `annotations`, `_meta` or vendor extensions, and mints no savings.
- Harm tripwire (`ledger.go`): an LRU of 1024 sessions keyed on `x-cave-session`; no header means it is inert. A lever earns a strike when the next request's `cache_creation_input_tokens` exceeds both 50k and 3x the session baseline; 3 strikes freeze it for the session, one way. The baseline excludes anomalous calls, or persistent regressions go unseen.
- Pixel mode: default models `claude-fable-5,gpt-5.6` (`CAVE_PIXEL_MODELS`), savings inferred only, same first-decision rules as compress. At most 20 images per Anthropic request, client images included (`anthropicManyImages`), because past 20 the API rejects images over 2000 px and renders are 2573 px wide.
- `nativeruntime.afterTool` does not mask outputs that `Engine.Detect` classes as `json`, `tabular` or `log`: the elision engine summarizes those in place, and masking them cost every fact plus a full recovery read. No classifier means mask. Tests: `mask_elidable_test.go`.

Repository evidence and git:
- A "Likely implementation path" line is injected only for items whose own file name or symbol contains a task term (`StrengthDirect`). Matching the whole path would let `src` or `internal` mark everything. `StrengthMetadata` items are never shown or stored; `Bundle.DirectOnly()` strips them before the object is written. Terms are at least 3 characters (`NormalizeTerms`). A decline records `repository_evidence_declined_no_direct_match`. `Scout.Status` is advice and must not gate injection (#899).
- `listFiles` prefers `git ls-files`, falls back to a walk on an empty listing, and runs the content-based dependency filter (`directoryFilter`) on both paths. Environments are found by contents (`conda-meta`, `pyvenv.cfg`); `build`, `dist`, `vendor` and similar are excluded only when a sibling manifest proves the ecosystem. Git gets at most half the time budget (`gitListBudget`) so the walk can still finish.
- Every `git` call against a user directory goes through `internal/gitsafe.Command` or `packages/cli/src/git-safe.ts`, which pass `-c` overrides (`core.fsmonitor`, `core.hooksPath`, `credential.helper` and others) and strip `GIT_*`. A cloned repo's `.git/config` can run code on `git status`. `verify_repo.py` fails on a new raw `git -C` call.

Auth, identity and network:
- `standalone.StandaloneHTTPClient` guards every upstream dial with `ssrf.SelfHostedConfig` (not `ManagedConfig`, which ignores the allowlist). `CAVE_SSRF_ALLOWLIST` re-allows loopback/private hosts; metadata and link-local stay blocked. `HTTPS_PROXY`/`NO_PROXY` apply by default (`upstream_proxy` / `CAVE_UPSTREAM_PROXY`); a managed config with a proxy fails with `ssrf.ErrProxyInManagedMode`. Bedrock and vertex use `ssrf.ValidateURLNoResolve` behind a proxy. `ca_bundle` / `CAVE_CA_BUNDLE` add private roots via `shared/platform/cabundle`.
- `CAVEMAN_AUTH_TOKEN` is required for a non-loopback listen (a token map, OIDC issuer or TLS client CA also qualify). `standalone.Auth` deletes the matching header before `Creds.Resolve`, so the token is never forwarded. `Authenticate` runs before `matchAdapter`, so 401 vs 404 cannot enumerate routes. Health, `/caveman/keepalive` and `/metrics` (unless `CAVEMAN_METRICS_TOKEN`) stay open.
- Bedrock credentials come from `shared/platform/awscreds`, never the request; a partial env pair fails closed.
- An inbound `Authorization: Bearer` keeps `Scheme:"bearer"` (Claude and Gemini OAuth break if remapped). On `/compat/<name>/v1/messages` a key goes as `x-api-key`, elsewhere as Bearer. When the same key arrives as both `x-api-key` and an identical Bearer, `openai_compatible` keeps bearer (#1215); differing values resolve `x-api-key` first.
- Middleware routes authorize every scoped route in `decode` via `Principal.Allows`; a route that skips `decode` skips authorization. JWT and certificate principals may never be `single_operator`. A failed token map or TLS reload keeps the previous one.
- `store.PostgresMiddleware` keeps SQLite's single-writer semantics per authority with transaction advisory locks; `middleware_concurrency_test.go` fails without them.

Store:
- `RecordBatch` commits request rows with `synchronous=NORMAL` (WAL); other tables keep FULL. A request's cache component list goes to `request_cache_components` (14-day retention), never into `requests` (#1371: up to 66 KB, slowed every stats read).
- `agent-evidence` is content-blind and always `inferred`. `provider_cache_components_available` is true only when the ordered list is present and valid; an empty list is never evidence of an unchanged prefix.
- With `AsyncRecord`, a reader that must see the request it follows calls `Server.Flush` first. Close the server before the sink.
- The HMAC native session marker is emitted only after local runtime and route proof and is stripped before capture, hashing and the provider.

Cache warming (`cache_warm.go`, `cache_warm_table.go`):
- Only Anthropic Messages on `api.anthropic.com`. The exact upstream bytes are held in memory only (1024 streams, 256 MiB) and replayed with `max_tokens: 0`. Requests that cannot be answered at zero output are never warmed.
- Whether to warm is planned from `cache_warm_table.json` by backward induction at catalog prices; unknown price, class or lifetime means no warm. `TestCacheWarmPlanMatchesTheSimulator` pins the plan to the table; regenerate both together.
- A parent's request does not stop its subagents' warms: a subagent may still run, and on the replay test half that stop cost $565.52 net.
- Stops on any real request of the stream, any warm error, a warm with `cache_read_input_tokens == 0`, or 60 min. A 429 pauses that credential. At most two warms in flight.
- Warm rows are tagged `cache-warm`: spend, never a saving or an agent request, never fed to the tripwire, never sent to Cloud. `CAVEMAN_CACHE_WARM=off` or `modules["waste-fixes"]: false` in `cloud.json` turns it off.

Route stage (`internal/cloudlink`, `internal/gateway/route*.go`; optional, fails open):
- Asks Cloud `POST /v1/route` (contract `route-ask-v1`) only for requests naming `caveman-auto`, while the CLI is signed in with `modules.routing: true`. Cloud picks the model and effort; no routing logic lives here. A named model records `route.outcome: off`.
- Auto goes upstream as `autoFallback` (`route.go`); a transient Cloud failure runs the session's previous model (`heldModel`). Auto on another provider is a 400 `cave_auto_unavailable`. OpenAI Auto runs only `AutoOpenAIModels`.
- The ask waits at most 800 ms. The keychain credential is read in `Link.Start`, never on a request goroutine. The answer is cached per turn so a tool loop never switches model or effort mid-turn.
- Session state, effort marks and heals live in in-memory LRUs (1024), never on disk.
- Effort marks (`effort_mode: message`) are replayed at the same anchors on later requests because the agent resends history without them. No OpenAI mark where OpenAI forbids it (`truncation: "auto"`, `context_management`, non-false `multi_agent`).
- A thinking-binding 400 retries once, only on a session the route stage changed: `drop_block`, else strip thinking blocks from the failing message on.
- Cloud errors, 401/403, `allowance` and `billing_limit` keep the asked model and pause asks; state goes to `$CAVEMAN_HOME/route-state.json`.
- A provider 4xx on a routed model replays the original bytes on the asked model (not a 401). Only a replay that differed in the model alone blames it (`provider_rejected_routed_model`). A rate-limit 429 or 5xx is returned as is.
- When the model moved, the agent's copy names the model it asked for (Claude Code drops its thinking otherwise); usage and stats read the provider's bytes.
- The pass-through header and origins other than the provider's own API never route. Record mode does.
- Telemetry level comes from `/me`; none or unreadable means `counts`, `off` sends nothing. Never invent a level.
- A route warm (`x-cave-warm: 1`, `warm.go`) may be retried on a new connection; an ask never is (Cloud may have counted it).

Pool and translation (`internal/pool`, `internal/translate`, `route_pool.go`, `route_cache.go`):
- Logins from `caveman providers add|login` are listed in `$CAVEMAN_HOME/provider-logins.json` (written under its `.lock`); the secret is in keychain service `caveman-provider` or a 0600 file, never both. At most 64 pool entries.
- `via: cloud` goes through the Cloud gateway on the project key only. Any failure before content reaches the agent falls back to the asked model; the translator holds output up to `gateHold` (10 s). A stream cut after content aborts framing and never closes a half-finished tool call.
- Requests to Anthropic's or OpenAI's own API drop reasoning another host wrote (`translate.AnthropicNative`, `OpenAINative`, `ChatNative`). A part the target cannot take refuses translation.
- Never add Claude Pro/Max, Google or Copilot logins to the pool; every OAuth row in `internal/pool/providers.json` carries a terms note.
- Record mode gets no cache mechanics (`prompt_cache_key`, OpenRouter pin, sibling wait). With routing off or signed out, requests stay byte for byte.
- Live check: `CAVEMAN_LIVE_OPENROUTER_KEY=<key file> go test ./proxy/internal/cloudlink -run TestLiveOpenRouterCache -v` (spends real money, $0.50 cap).

See the root `AGENTS.md` for repo-wide rules.
