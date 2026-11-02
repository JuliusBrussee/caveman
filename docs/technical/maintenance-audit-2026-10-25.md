# Runtime maintenance audit — 2026-10-25

## Scope and evidence boundary

This audit targets the public Caveman repository's provider proxy, cache behavior, Engine, CLI lifecycle, and release checks. It does not certify every provider, platform, request type, or optimization. Local fixtures prove their exercised contracts; they do not measure provider cache hits or completed-task savings.

The original local checkout was stale and dirty. Its five pre-existing tracked edits were preserved byte-for-byte. Work moved to an isolated checkout of upstream `9b68f7ed`. This matters: upstream already contains the Pi dynamic-context fix from #1376, short-output masking floor from #1378, recovery-description correction from #1377, recovery hash validation from #1373, and stats expression index from #1372. Those are retained, not counted as new fixes here.

## Confirmed fixes in this patch

| Contract | Correction | Regression evidence |
| --- | --- | --- |
| Warm cache decisions survive capacity pressure | Evict only beyond the longest documented provider retention window plus the batched timestamp margin. Raw, compressed, and Pixel decisions remain stable. | Seed over capacity; all warm entries survive; expired entries still evict. |
| Cached decisions survive TOON configuration changes | Discover historical small tool outputs even when new TOON compression is disabled; replay existing decisions without newly transforming those outputs. | Actual SQLite close/reopen; Anthropic, OpenAI Chat, and both Responses forms; live/frozen inputs and raw decisions. |
| Text metadata survives Pixel conversion | Text parts carrying unsupported metadata, citations, or OpenAI cache breakpoints pass through unchanged. | Opaque metadata and cache-breakpoint byte checks. |
| Gemini thought signatures remain attached to original data | Signed/thought parts bypass text compression and memo substitution; opaque Pixel requests pass through intact. | Signed text, tool-response, snake/camel spelling, and Pixel metadata cases. |
| Token exchange reaches Anthropic | Exact POST token routes relay grants after local authentication, without provider-key fallback, transforms, payload capture, or spend rows. | Prefixed/bare/wrapped paths; grants and provider errors; oversized bodies, wrong routes, local-auth failures, and truncation. |
| Workspace selection survives proxying | Forward `anthropic-workspace-id`. | Header mapping contract. |
| Metadata truncation is visible to clients | Abort HTTP framing after interrupted upstream copy. | Real HTTP/1.1 and HTTP/2 incomplete-response tests. |
| Bedrock ARN model IDs keep escaped separators | Preserve escaped paths while routing model actions. | Four inference action shapes plus wrapped gateway forwarding. |
| Native Responses WebSockets reach OpenAI | Relay exact Responses upgrade routes through the existing endpoint, credentials, and transport. Frames remain opaque; browser-origin handshakes cannot use local operator credentials. | Real duplex exchange, provider rejection, authentication, cancellation, shutdown, and stalled-client tests. |
| First stop drains active streams | HTTP shutdown waits for accepted requests; another stop signal forces exit. Persistence closes after drain. | Subprocess SSE request held beyond the former five-second deadline; second-signal and idle-listener tests. |
| Launchers propagate stop signals without duplicate console delivery | Forward supervisor signals, wait for proxy completion, avoid duplicate POSIX terminal signals and Windows forced termination, and clean handlers/timers. | Real POSIX PTY group/PID delivery, Windows dispatch-policy tests, second-stop/timeout cleanup, and signal exit-code checks. Native Windows console execution remains unverified. |
| Mixed OpenCode installations keep both plugin APIs | A binary with an unreadable version cannot be dismissed because another alias reports a known major; select the existing dual API plugin and diagnose stale single-API installations. | Known/unknown combinations, actual generated module loading, and doctor repair. |
| OpenCode guidance does not rewrite the system prefix | Persist per-prompt guidance in admitted user history. Omit ephemeral per-tool reuse/loop reminders and record `reuse_hint_skipped_cache_safety`; retain tool execution, compression, and exact recovery. | Generated V1/V2 hook continuity across turns/reloads, duplicate admission, empty text, tool success/failure, and native-runtime decision ledger/recovery cases. |
| SQLite connection replacement retains storage limits | Apply physical page/WAL limits to every connection, after legacy migrations complete; report physical exhaustion without evicting recovery data. | Forced replacement connections, separate same-DSN clients, different page sizes, and legacy full-store migrations. |
| Failed SQLite migration does not checkpoint stale journals | Use the existing guarded open-failure cleanup for the data-reference migration too. | Deterministic file-generation replacement during a failing migration. |
| Concurrent recovery writers make one deduplication decision | Use `BEGIN IMMEDIATE` around owner lookup, budget checks, and insertion on both write paths. | Twelve independent stores retain one map payload; logical-budget and disk-full refusals release transactions and permit later writes. |
| Usage accounting respects the configured byte limit | Count retained wire bytes, including incomplete UTF-8, instead of estimating four bytes per decoded character. | ASCII, multibyte, invalid UTF-8, exact-limit, overflow, and split-chunk JSON/SSE tests against the installed wheel. |
| Fresh LiteLLM extra installs reject known bad releases | Exclude the documented 1.106.0–1.106.2 tool-call regression and compromised 1.107.0 from wheel dependency metadata. | Installed-wheel requirement checks. This does not remediate an already compromised environment. |
| Hook downloads terminate and preserve old files on failure | Require HTTPS, cap redirects and bytes, enforce one total deadline, and write only complete downloads. | Stalled and truncated streams, relative redirects, downgrade rejection, oversized bodies, and curl option checks. |
| Published installers agree with canonical runtime pin | Regenerate five binary-release mirrors from `packages/cli/BINARY_RELEASE`. | Generation checks and release/installer tests. |
| Release publication depends on platform contracts | Both binary and package release build jobs depend on reusable `proxy-reliability` workflow. | Actionlint plus existing artifact/installer checks; remote jobs must still execute before release readiness. |

The Pi fix already present upstream now has extra checks for context changing and then disappearing, plus a wire assertion that dynamic context remains outside the system prompt.

## Validation of this patch

All runs below use local fixtures. Counts include subtests where the runner reports them.

| Check | Result | Boundary |
| --- | --- | --- |
| Full gateway and provider race suite | 1,682 passed; zero failed or skipped | Includes final WebSocket EOF/backpressure/shutdown changes. |
| Final integrated Engine, gateway, providers, store, native runtime, and proxy command packages | 2,967 passed; 24 skipped | Includes final concurrent-write and OpenCode cache-safety changes. Skips include Postgres-dependent cases, atlas regeneration, and an optional startup diagnostic. |
| CCR race suite | 97 passed; zero failed or skipped | Final production changes; six subsequent focused concurrent-write and refusal cases also passed. |
| Native-runtime race suite | 80 passed; zero failed or skipped | Includes explicit OpenCode advisory skip and exact recovery. |
| Full CLI suite on frozen source | 1,469 passed; 6 skipped | Includes final OpenCode, Windows signal-policy, real POSIX terminal, wrapper, and host-isolated fixture changes. |
| Pi extension | 147 passed; zero failed or skipped | Pinned real Pi host and local wire fixtures. |
| Installed Python middleware wheel | 188 passed; 264 skipped | Optional framework packages unavailable locally. Focused usage/dependency metadata: 37 passed with no skips. |
| Full installer suite | 511 passed; 3 platform/fixture skips | Includes download limits and fresh-install Node-path regression. |
| Go vet | Passed | Engine and proxy packages. |
| Repository verification, generated pins, workflow lint | Passed | No publication or native-platform certification follows from static checks. |

Native Linux/Windows CI and paid-provider cache/savings measurements have not run locally. The signed maintenance branch is prepared for a draft PR; hosted checks must be evaluated separately before merge or release.

## Release and scheduled maintenance

`.github/workflows/proxy-reliability.yml` runs on pull requests, main pushes, daily schedule, manual dispatch, and reusable-workflow calls. It executes provider, gateway, cache store, recovery, native-runtime, CLI and Pi contracts on Ubuntu, macOS, and Windows; Linux also runs race checks. The Pi host must exist before tests, so missing dependencies cannot silently skip integration coverage. Release jobs wait for this workflow on the release revision.

These workflow changes only protect releases after merge. Native Windows/Linux CI outcomes are not inferred from local macOS tests.

A separate Codex heartbeat, **Caveman runtime maintenance**, runs daily at 09:00 America/Los_Angeles. It preserves other work, checks ownership before edits, refreshes GitHub coverage, consults official docs, and fixes one confirmed cause per bounded run. It leaves changes local for review and sends no comments, pushes, merges, releases, deployments, or paid provider calls. Codex and this Mac must be running. The existing broader automation was left unchanged.

## Provider and host sources

Checked 2026-10-25. These define correctness constraints, not evidence of achieved savings.

- [Anthropic prompt caching](https://platform.claude.com/docs/en/build-with-claude/prompt-caching): exact stable prefix and cache-marker semantics.
- [OpenAI prompt caching](https://developers.openai.com/api/docs/guides/prompt-caching): prefix identity, model-specific retention and explicit cache breakpoints.
- [Gemini thought signatures](https://ai.google.dev/gemini-api/docs/generate-content/thought-signatures): preserve signatures and their original parts.
- [Gemini caching](https://ai.google.dev/gemini-api/docs/caching): common-prefix behavior.
- [Anthropic authentication](https://platform.claude.com/docs/en/manage-claude/authentication) and [workload identity contract](https://platform.claude.com/docs/en/manage-claude/wif-reference): workspace selection and token exchange.
- [Bedrock Converse API](https://docs.aws.amazon.com/bedrock/latest/APIReference/API_runtime_Converse.html): ARN model identifiers.
- [Anthropic streaming](https://platform.claude.com/docs/en/build-with-claude/streaming), [Go Shutdown](https://pkg.go.dev/net/http#Server.Shutdown), [Node signals](https://nodejs.org/api/process.html#signal-events), and [Node child termination](https://nodejs.org/api/child_process.html#subprocesskillsignal): stream and lifecycle contracts.
- [Pi extension API](https://github.com/earendil-works/pi/blob/main/packages/coding-agent/docs/extensions.md): per-turn messages versus system-prompt replacement.
- [OpenCode V1/V2 migration contract](https://opencode.ai/v2/docs/build/plugins/migrate-v1): dual `server`/`setup` plugin API, documented for V1 1.18.29 and later; older hosts remain unverified.
- [OpenCode V1 prompt admission](https://github.com/anomalyco/opencode/blob/v1.3.7/packages/opencode/src/session/prompt.ts), [persisted text parts](https://github.com/anomalyco/opencode/blob/v1.3.7/packages/opencode/src/session/message-v2.ts), and [V2 prompt hooks](https://github.com/anomalyco/opencode/blob/dev/packages/plugin/src/promise/session.ts): durable per-turn guidance. Local generated-hook tests follow these host contracts; they do not prove native OpenCode database replay or provider cache hits.
- [OpenAI Responses Connect](https://developers.openai.com/api/reference/resources/responses/methods/connect) and [RFC 6455 origin considerations](https://www.rfc-editor.org/rfc/rfc6455#section-10.2): native WebSocket endpoint/authentication and browser-origin boundaries.
- [SQLite pragmas](https://www.sqlite.org/pragma.html), [transaction behavior](https://www.sqlite.org/lang_transaction.html), and [modernc SQLite connector](https://pkg.go.dev/modernc.org/sqlite#NewConnector): physical-connection configuration and acquiring the write lock before deduplication decisions.
- [LiteLLM incident record](https://docs.litellm.ai/blog/security-incident-oct-21-2026-attack-chain) and [upstream tool-routing fix](https://github.com/BerriAI/litellm/pull/45961): excluded dependency versions.
- [Node HTTP timeouts](https://nodejs.org/api/http.html#requestsettimeouttimeout-callback) and [curl limits](https://curl.se/docs/manpage.html): abort behavior, total duration, redirect, protocol, and byte limits.
- [GitHub reusable workflows](https://docs.github.com/en/actions/how-tos/reuse-automations/reuse-workflows): dependent release jobs.

## GitHub coverage and remaining work

Paginated GitHub REST confirmed **44 open issues and 52 open PRs**. All 96 discussion endpoints were fetched (one transient failure was retried separately), and all 52 PR review-thread endpoints were fetched. Eight unresolved review threads were found. This is complete open-item inventory and targeted code review, not approval of all 52 diffs or an exhaustive review of closed history. No issues or PRs were closed or merged.

Key unresolved leads:

- #1370: connection caps and physical-full reporting are fixed here; genuinely full stores still need a recoverable capacity-management workflow. Never delete data referenced by resumed transcripts or cross-session dedup owners.
- #1375: concurrent deduplication is fixed for updated writers. Existing duplicate payload owners remain, and older simultaneous binaries can still race. A read-snapshot defect was not reproduced through the current immutable-owner APIs; external SQL mutation or future consolidation needs separate validation.
- #1369: hash checks reject wrong recovery bytes, but pre-existing corrupted databases still need explicit diagnosis and a recoverable repair workflow.
- #1371: the new index helps selection, but large cache-component columns and retention still need measured storage/read-cost remediation.
- #1279: latest reporter corrected an earlier success claim; a busy long-lived native runtime still stops answering while fresh homes work. Do not call a release-pin bump a complete fix.
- #1246: OpenCode v2 plugin compatibility needs native-host conformance, not just CLI version probes.
- #1225 / #1311: Pi version drift and OpenCode Go endpoint changes must preserve exact endpoint/credential matching.
- #1312: unresolved Gemini auth repair/shadowing/restoration comments need reproduction against the current merged implementation.
- #1093 / #1323: optimization economics need task-quality, cache-write, recovery, retry and latency evidence. No new savings percentage is justified by this patch.
- #1318 / #1258 / #186: model behavior and host skill discovery reports require host-specific reproduction.
- Unsupported provider endpoints and native Windows console behavior remain explicit compatibility gaps until exercised and verified.
- Switching a conversation between optimized HTTP and opaque WebSocket passthrough can replay different history bytes. Cross-transport cache-prefix equivalence and optimization are unsupported and unverified; WebSocket-only byte relay is the tested contract.

Several full-CLI failures were caused by fixtures inheriting the developer's real Caveman/MCP/OpenCode binaries through PATH. Fixtures now isolate the binaries they intend to exercise; safety and compatibility assertions remain intact.

Cache capacity is a soft target while entries remain warm. This avoids forced provider-cache rebuilds but can increase local disk use under sustained high-cardinality traffic. Signed or unknown metadata may reduce optimization coverage to preserve provider behavior.

OpenCode no longer receives the optional current-observation and repeated-reread reminders before tools. These were advisory, never permission gates or output substitutions. V1 lacks a durable failed-tool guidance hook, so omitting these reminders preserves cache-prefix stability without pretending they delivered an optimization. They need a replay-safe host contract before restoration.

### Open inventory

| Item | Type | Title |
| --- | --- | --- |
| [#1390](https://github.com/JuliusBrussee/caveman/issues/1390) | Issue | Support MiniMax Code CLI (mcode) |
| [#1318](https://github.com/JuliusBrussee/caveman/issues/1318) | Issue | Ultracave ignored on Opus 5.5 despite #929, hooks fire every turn |
| [#1366](https://github.com/JuliusBrussee/caveman/issues/1366) | Issue | Release a CLI that installs bin-v4.0.2: setup --install still gets the bin-v4.0.1 runtime (#1279) |
| [#1370](https://github.com/JuliusBrussee/caveman/issues/1370) | Issue | Input compression stops when ccr.db reaches its page cap below the 512 MiB payload budget, and nothing removes old typed objects |
| [#1371](https://github.com/JuliusBrussee/caveman/issues/1371) | Issue | caveman stats times out because each requests row stores up to 1,024 cache component digests, which every report read must pass over |
| [#1369](https://github.com/JuliusBrussee/caveman/issues/1369) | Issue | Corrupt ccr.db stays corrupt after upgrades: recoveries table damage with the #1149 pattern, and no check or repair |
| [#1368](https://github.com/JuliusBrussee/caveman/issues/1368) | Issue | pi: caveman_retrieve with query returns a silent excerpt, though the handle says one call returns the entire original |
| [#1365](https://github.com/JuliusBrussee/caveman/issues/1365) | Issue | pi: short tool outputs (~1.8 KB) are replaced by a content-free handle, so every needed output costs a caveman_retrieve round trip |
| [#1364](https://github.com/JuliusBrussee/caveman/issues/1364) | Issue | pi: per-prompt runtime context is appended to the system prompt, so the provider prompt cache is re-written at each prompt |
| [#1236](https://github.com/JuliusBrussee/caveman/issues/1236) | Issue | agent-drift: hermes — installed 0.22.5 fails the latest probe (pinned 0.21.5) |
| [#1326](https://github.com/JuliusBrussee/caveman/issues/1326) | Issue | middleware-python nightly canary is failing |
| [#1279](https://github.com/JuliusBrussee/caveman/issues/1279) | Issue | Native runtime closes every native.sock request with an empty reply after a 2.0.1 → 4.0.1 upgrade (pi shrink silently off) |
| [#1093](https://github.com/JuliusBrussee/caveman/issues/1093) | Issue | evals: quality loss not measured; tokenizer mismatch inflates savings numbers |
| [#1350](https://github.com/JuliusBrussee/caveman/issues/1350) | Issue | caveman-compress: Validation checks structure only: a changed number passes, and a prose-only file passes anything shorter |
| [#1352](https://github.com/JuliusBrussee/caveman/issues/1352) | Issue | caveman-compress: A failed fix call leaves the backup and `<name>.caveman-staged`, and every rerun aborts |
| [#1258](https://github.com/JuliusBrussee/caveman/issues/1258) | Issue | Visual Studio Copilot: caveman ultra recognized - not used |
| [#271](https://github.com/JuliusBrussee/caveman/issues/271) | Issue | Add: Plugin for Github Copilot |
| [#1333](https://github.com/JuliusBrussee/caveman/issues/1333) | Issue | Docs: optional "Verified on MCPwell" badge + listing check |
| [#1307](https://github.com/JuliusBrussee/caveman/issues/1307) | Issue | Claude Code skips the caveman dependencies because the root uses a pnpm lockfile |
| [#1225](https://github.com/JuliusBrussee/caveman/issues/1225) | Issue | agent-drift: pi — installed 1.4.0 exceeds pinned 1.0.4 |
| [#1337](https://github.com/JuliusBrussee/caveman/issues/1337) | Issue | docs: Hermes behind a custom OpenAI-compatible gateway needs providers.openai.base_url (otherwise a confusing 401) |
| [#1323](https://github.com/JuliusBrussee/caveman/issues/1323) | Issue | Independent measurement: caveman with Claude Code: no cost change on coding tasks (preliminary) |
| [#1285](https://github.com/JuliusBrussee/caveman/issues/1285) | Issue | Don't work in the latest version of Antigravity CLI. |
| [#1246](https://github.com/JuliusBrussee/caveman/issues/1246) | Issue | opencode plugin targets the v1 plugin API — broken on OpenCode v2.x (verified breakdown + port spec) |
| [#1272](https://github.com/JuliusBrussee/caveman/issues/1272) | Issue | HTML data pages pass through or lose their table structure |
| [#1286](https://github.com/JuliusBrussee/caveman/issues/1286) | Issue | Routing: offer Auto (caveman-auto) in pi |
| [#1278](https://github.com/JuliusBrussee/caveman/issues/1278) | Issue | cavecrew: add brief guidance on working directory, hook triggers and git identity |
| [#1150](https://github.com/JuliusBrussee/caveman/issues/1150) | Issue | Extension v1.2.0: Caveman prompt not injected on claude.ai (works on Gemini) |
| [#1262](https://github.com/JuliusBrussee/caveman/issues/1262) | Issue | cavecrew: tell the orchestrator to spot-check "No match." / "No issues." / "unchanged" claims before acting on them |
| [#1242](https://github.com/JuliusBrussee/caveman/issues/1242) | Issue | Codex marketplace plugin does not register activation hooks |
| [#1240](https://github.com/JuliusBrussee/caveman/issues/1240) | Issue | api.caveman.so returns 503 Service Unavailable |
| [#1217](https://github.com/JuliusBrussee/caveman/issues/1217) | Issue | [Enhancement] Add a unified architecture map with component and data boundaries |
| [#1216](https://github.com/JuliusBrussee/caveman/issues/1216) | Issue | Clarify Pixel model defaults and separate request opt-out from skill conversion |
| [#863](https://github.com/JuliusBrussee/caveman/issues/863) | Issue | support for nix package manager |
| [#1115](https://github.com/JuliusBrussee/caveman/issues/1115) | Issue | Help on setting up this skill for android |
| [#1102](https://github.com/JuliusBrussee/caveman/issues/1102) | Issue | Add official support for the nanocoder agent |
| [#1020](https://github.com/JuliusBrussee/caveman/issues/1020) | Issue | print mode (`claude -p`): eligible subscription requests never reach the engine, while interactive sessions compress normally |
| [#1090](https://github.com/JuliusBrussee/caveman/issues/1090) | Issue | caveman wrap opencode with gateway-routed model: transformed requests 401, fail-open retry, 0 tokens compressed (regression of #946) |
| [#1053](https://github.com/JuliusBrussee/caveman/issues/1053) | Issue | Codex Desktop ChatGPT Responses requests omit tools; standalone subscription compression cannot prove MCP recovery |
| [#1028](https://github.com/JuliusBrussee/caveman/issues/1028) | Issue | Official Firefox installaton link |
| [#374](https://github.com/JuliusBrussee/caveman/issues/374) | Issue | Make bundled skills/agents/commands opt-in (caveman-only install option) |
| [#573](https://github.com/JuliusBrussee/caveman/issues/573) | Issue | Add optional always-on mode for Codex |
| [#570](https://github.com/JuliusBrussee/caveman/issues/570) | Issue | Add Bun support as an alternative installer runtime |
| [#186](https://github.com/JuliusBrussee/caveman/issues/186) | Issue | Caveman not working in Claude Cowork mode. |
| [#1311](https://github.com/JuliusBrussee/caveman/pull/1311) | PR | chore(agents): bump the Pi pin to 1.3.0 |
| [#1387](https://github.com/JuliusBrussee/caveman/pull/1387) | PR | chore(agents): bump the qwen pin to 0.26.0 |
| [#1389](https://github.com/JuliusBrussee/caveman/pull/1389) | PR | chore(agents): bump the opencode pin to 1.19.4 |
| [#1330](https://github.com/JuliusBrussee/caveman/pull/1330) | PR | fix(hooks): uninstall no longer deletes a settings.json.bak it did not make |
| [#1386](https://github.com/JuliusBrussee/caveman/pull/1386) | PR | mem: allow reverting a memory to an earlier value; add supersede --stdin |
| [#1359](https://github.com/JuliusBrussee/caveman/pull/1359) | PR | fix(compress): keep frontmatter local on compress and fix passes |
| [#1393](https://github.com/JuliusBrussee/caveman/pull/1393) | PR | feat(pool): add Runcrate as a provider login |
| [#1388](https://github.com/JuliusBrussee/caveman/pull/1388) | PR | chore(agents): bump the codex pin to 0.164.0 |
| [#1383](https://github.com/JuliusBrussee/caveman/pull/1383) | PR | feat(evals): add Spanish (es) prompt set |
| [#1374](https://github.com/JuliusBrussee/caveman/pull/1374) | PR | fix(ccr): apply the page cap on every store connection |
| [#1372](https://github.com/JuliusBrussee/caveman/pull/1372) | PR | perf(store): index requests by julianday(ts) for stats windows |
| [#1373](https://github.com/JuliusBrussee/caveman/pull/1373) | PR | fix(ccr): refuse recovery bytes that do not match the handle |
| [#1378](https://github.com/JuliusBrussee/caveman/pull/1378) | PR | fix(pi): leave tool outputs under 4096 bytes unmasked |
| [#1377](https://github.com/JuliusBrussee/caveman/pull/1377) | PR | fix(pi): state that caveman_retrieve with a query returns only an excerpt |
| [#1376](https://github.com/JuliusBrussee/caveman/pull/1376) | PR | fix(pi): send per-prompt context as a message, keep the system prompt stable |
| [#1379](https://github.com/JuliusBrussee/caveman/pull/1379) | PR | fix(middleware-python): exclude broken litellm |
| [#1375](https://github.com/JuliusBrussee/caveman/pull/1375) | PR | fix(ccr): serialize writes and pin read snapshots for dedup maps |
| [#1363](https://github.com/JuliusBrussee/caveman/pull/1363) | PR | fix(compress): treat a .env file as config, not prose |
| [#1367](https://github.com/JuliusBrussee/caveman/pull/1367) | PR | fix(middleware): do not mark a response over the limit at a quarter of it |
| [#1344](https://github.com/JuliusBrussee/caveman/pull/1344) | PR | fix(installer): fall back to Bun when Node is absent |
| [#1358](https://github.com/JuliusBrussee/caveman/pull/1358) | PR | fix(compress): match the retry docs and failure message to what the CLI does |
| [#1357](https://github.com/JuliusBrussee/caveman/pull/1357) | PR | fix(compress): refuse credential dotfiles before reading them |
| [#1356](https://github.com/JuliusBrussee/caveman/pull/1356) | PR | fix(compress): ignore trailing punctuation on URLs and paths in validation |
| [#1362](https://github.com/JuliusBrussee/caveman/pull/1362) | PR | fix(cli): default ENABLE_TOOL_SEARCH to "true" to match Claude Code |
| [#1313](https://github.com/JuliusBrussee/caveman/pull/1313) | PR | chore(deps): bump the go group across 1 directory with 5 updates |
| [#1348](https://github.com/JuliusBrussee/caveman/pull/1348) | PR | fix(claude): separate plugin payload from installer dependencies |
| [#1346](https://github.com/JuliusBrussee/caveman/pull/1346) | PR | fix(cavecrew): verify subagent absence claims |
| [#1332](https://github.com/JuliusBrussee/caveman/pull/1332) | PR | fix(engine): keep integers above 2^53 off the numeric range track |
| [#1345](https://github.com/JuliusBrussee/caveman/pull/1345) | PR | fix(proxy): keep the percent-encoded slash of a Bedrock ARN model id upstream |
| [#1338](https://github.com/JuliusBrussee/caveman/pull/1338) | PR | docs(config): document providers.openai.base_url for OpenAI-wire agents behind a gateway |
| [#1343](https://github.com/JuliusBrussee/caveman/pull/1343) | PR | fix(json): stabilize elision note placement |
| [#1341](https://github.com/JuliusBrussee/caveman/pull/1341) | PR | fix(proxy): relay Claude OAuth token refresh |
| [#1340](https://github.com/JuliusBrussee/caveman/pull/1340) | PR | chore(deps): bump the github-actions group with 3 updates |
| [#1314](https://github.com/JuliusBrussee/caveman/pull/1314) | PR | chore(deps): bump the npm group across 3 directories with 4 updates |
| [#1339](https://github.com/JuliusBrussee/caveman/pull/1339) | PR | chore(deps): bump the pip group across 2 directories with 2 updates |
| [#1306](https://github.com/JuliusBrussee/caveman/pull/1306) | PR | fix(proxy): map Kimi K3 chat effort |
| [#1267](https://github.com/JuliusBrussee/caveman/pull/1267) | PR | feat(cursor): dedupe repeated grep, glob, read, and git status |
| [#1289](https://github.com/JuliusBrussee/caveman/pull/1289) | PR | fix(antigravity-cli): declare contextFileName in agy plugin.json |
| [#1331](https://github.com/JuliusBrussee/caveman/pull/1331) | PR | fix(shrink): read the schema of Anthropic input_schema tools |
| [#1329](https://github.com/JuliusBrussee/caveman/pull/1329) | PR | feat(proxy): add Moonshot Responses routing |
| [#1325](https://github.com/JuliusBrussee/caveman/pull/1325) | PR | fix(pi): verify the compat route for an OpenAI base without /v1 |
| [#1319](https://github.com/JuliusBrussee/caveman/pull/1319) | PR | fix(hooks): enforce strict persona override for ultracave mode (#575) |
| [#1317](https://github.com/JuliusBrussee/caveman/pull/1317) | PR | fix(learn): honor Pi's PI_CODING_AGENT_DIR and PI_CODING_AGENT_SESSION_DIR |
| [#1321](https://github.com/JuliusBrussee/caveman/pull/1321) | PR | chore(release): pin binaries to bin-v4.0.2 |
| [#1269](https://github.com/JuliusBrussee/caveman/pull/1269) | PR | fix(installer): bound and pin hook downloads to https |
| [#1243](https://github.com/JuliusBrussee/caveman/pull/1243) | PR | fix(codex): register marketplace plugin lifecycle hooks |
| [#1273](https://github.com/JuliusBrussee/caveman/pull/1273) | PR | feat(engine): keep tables in HTML articles as tables |
| [#1288](https://github.com/JuliusBrussee/caveman/pull/1288) | PR | feat(engine): give HTML data pages a structural view |
| [#1312](https://github.com/JuliusBrussee/caveman/pull/1312) | PR | fix(cli): pin Gemini API-key auth when routed (exit 41) |
| [#1308](https://github.com/JuliusBrussee/caveman/pull/1308) | PR | Suggestion: a short intro reel for the README |
| [#1036](https://github.com/JuliusBrussee/caveman/pull/1036) | PR | feat(omp): add native extension lifecycle and package entry |
| [#1220](https://github.com/JuliusBrussee/caveman/pull/1220) | PR | feat(cli): Cloud commands move to cvm; caveman keeps sign-in |
