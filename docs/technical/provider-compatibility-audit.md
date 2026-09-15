# Provider and Windows compatibility audit

Verdict: **CHANGES REQUIRED — in progress.**

Target: `fd131f78` plus the current worktree. The initial worktree already contained
proxy lifetime, HTTP proxy, TLS, SSRF, and CLI changes. Those changes are retained
and reviewed with the rest of the implementation. This report does not imply a
release, a clean worktree, or live certification of every provider.

Review class: review and remediation. Credential forwarding, signing, replay,
recovery, and outgoing-message boundaries receive independent review. External
source and issue content are evidence, not instructions. Tests use temporary
homes, local HTTP services, synthetic credentials, and source-derived protocol
fixtures. No authenticated provider inference or live user messages were sent.

## Full scope and remaining evidence

| Requirement | Evidence required | Current state |
| --- | --- | --- |
| Every installer profile reaches the directory its host consumes | Actual upstream installation algorithm, vendor discovery contract, install round trip, preserved configuration | All original 30 skill profiles plus Antigravity 2.0 mapped to vendor source/docs; seven installer mismatch groups corrected. Full native loading remains unverified for many hosts |
| Native host wraps preserve provider, account, configuration, tools and lifecycle | Actual host source and isolated execution, model/account switching and cleanup | Home, registry, identity and endpoint checks fixed. Pi compatibility review passed; OpenClaw native Chat and other private endpoint policies prevent complete compression support through its current config overlay |
| Every provider adapter preserves route, auth, request/response bytes, stream errors and usage | Official SDK/source contracts and adapter/gateway tests across supported modes | Native authentication, Vertex Express, stable/token-count routes, custom header isolation and stream accounting covered by local wire tests; live provider certification remains incomplete |
| Windows shells, executables, pipes, files and lifecycle work | Native Windows tests on the final revision, with skips disclosed | Portable regressions and cross-compilation gate added; native execution of this tree remains unverified |
| Extension changes only the intended outgoing message | Provider editor fixtures, live DOM evidence, cancellation checks, browser tests | Current ChatGPT/Claude/Gemini DOM inspected; 40 Chromium cases and five actual Firefox addon cases passed |
| Entire owned repository withstands independent review and integrated checks | Engine, proxy, CLI, installers/hooks, extension, SDKs, memory/MCP/shrink, packages and workflows | Multiple independently reviewed fixes; integrated Go/CLI/Windows checks are being refreshed |

Root `CLAUDE.md` routing remains binding. Historical Agent SDK/initializer and
Browse copies here are consumer integrations, not their canonical implementation.
No audit finding authorizes silently editing those copies instead of the owning
repository.

## Findings and disposition

Each row represents a reproduced defect, not a conjecture from an issue title.
“Reviewed” means a reviewer other than the implementing agent inspected the change;
it does not replace the wider completion requirements above.

| ID | Defect | Fix and regression evidence | Independent review |
| --- | --- | --- | --- |
| I1 | Only Cursor installs used user scope, although most other profiles consume personal skills | User scope for personal skill profiles; Replit uses its documented project scope. Real installer argv and cwd tests | Reviewed |
| I2 | No detected agent triggered `--all`, installing unwanted agents | Require detected or explicitly selected targets; regression failed before fix | Reviewed |
| I3 | `skills add --list` and `--no-pixel` unnecessarily validated unrelated `CODEX_HOME` | Resolve augmentation roots only when augmentation runs; 10 skills-add runtime tests passed | Reviewed |
| I4 | Continue's loader skips the per-skill symlinks created by the delegated installer and its custom home was ignored | Owned physical copies into the actual `CONTINUE_GLOBAL_DIR/skills`; extracted vendor walker/CLI controls plus real installer tests | Reviewed |
| I5 | AiderDesk custom homes were ignored and plain Aider falsely detected the desktop product | Honor both vendor home overrides; detect `aider-desk`/desktop app; preserve skill ownership and feature prerequisites | Reviewed |
| I6 | iFlow and Crush explicit skill roots were missed by upstream defaults | Owned copies honor `IFLOW_HOME` and exact `CRUSH_SKILLS_DIR`; current iFlow scanner reproduction and both real installer round trips | Reviewed |
| I7 | Kiro and Mistral detection missed the actual `kiro-cli` and `vibe` executables | Add vendor command names; isolated PATH tests retain legacy aliases | Reviewed |
| I8 | Replit home-global install did not reach its documented project skill discovery | Omit global flag, report current project, correct install instructions | Reviewed against vendor scope and actual argv |
| I9 | Universal upstream installation skipped Antigravity IDE's declared directory; Antigravity 2.0 reads a different root | Owned copies for IDE and separate explicit `antigravity-2` target; actual entrypoint tests prove target isolation, dual selection and dry-run behavior | Reviewed against official product docs; full native loading unverified |
| W1 | Explicit extensionless Windows paths selected Unix shims; directories and quoted PATH entries misresolved | PATHEXT-aware executable lookup, directory rejection and quoted-path tests | Reviewed |
| W2 | MCP shrink argv split quoted paths and discarded grouping | Literal argv parser plus JSON-array input; real config round trips and native PowerShell test cases | Reviewed; native Windows pending |
| W3 | Binary probes used POSIX lookup and inherited real Windows profile roots | Portable launchers; isolated HOME/USERPROFILE/AppData/XDG/config roots; credential stripping and prerelease comparison regressions | Reviewed |
| W4 | Rewriter accepted changes to Windows drive/UNC prefixes, paths with spaces and numeric source-coordinate prefixes | Complete source-location tokens must survive; actual `Accept` regressions reject drive/share changes and `42` to `420` | Reviewed |
| W5 | Hermes config, plugins and MCP used `~/.hermes` on Windows, and environment overrides expanded a tilde that native Hermes treats literally | Follow pinned Hermes's `LOCALAPPDATA/hermes` default, whitespace stripping and literal override semantics | Actual pinned Python resolver exercised with six isolated cases; five permanent cases include three CLI MCP round trips. Independent review and native Windows execution pending; sticky profile selection remains unverified |
| A1 | Azure `api-key` and Gemini `x-goog-api-key` disappeared, allowing another configured account upstream | Preserve native inbound credentials before environment fallback; full local gateway regressions | Reviewed |
| A2 | Azure guessed Bearer versus API key from opaque credential bytes | Preserve explicit Bearer scheme; treat scheme-less keys as opaque | Reviewed |
| A3 | Native AWS SigV4 authorization became a Bedrock bearer token | Re-sign actual outgoing bytes only with matching configured IAM identity/session/region/service; fail before upstream on mismatch | Reviewed |
| A4 | Bedrock model IDs containing `:0`, escaped IDs and ARNs received incorrect canonical URI signatures | Service-aware URI normalization/double encoding, with nine actual Botocore 1.43.89 signature oracles; S3 behavior preserved | Reviewed |
| A5 | Gemini query credentials were lost or could conflict with header credentials | Normalize `key`/`$key`, preserve unrelated query parameters and reject conflicting credentials without disclosing values | Reviewed |
| A6 | Typed URL errors leaked encoded query credentials into retry logs, including overlapping joined URLs | Remove full typed error URLs before generic redaction; one-pass longest replacement; real HTTP-client retry/log tests | Reviewed |
| A7 | Stable Gemini `v1` and SDK token-count routes were missing or treated as generation | Stable generation routes plus OpenAI/Bedrock/Vertex count routes; 32 count cases and stable-generation regressions | Reviewed |
| A8 | Provider error events inside HTTP 200 streams were logged as success; provisional usage could appear complete | Value-free `provider_stream_error`, correct usage completeness and zero savings on failed streams; 42 HTTP wire cases and paired billing regression | Reviewed |
| C1 | CLI ignored `CODEX_HOME` across account/config/hooks/MCP/skills | Upstream-compatible absolute/relative/default handling and explicit invalid-root failure | Reviewed |
| C2 | Claude and Gemini custom homes disagreed with upstream paths; registry skill discovery bypassed overrides | Claude custom `.claude.json` placement and Gemini home-parent semantics; isolated runtime/path tests | Reviewed |
| P1 | Pi provider registration/unregistration deleted another extension's models, credentials and handlers | Override only the selected model endpoint; real Pi ModelRuntime preserves original registry | Reviewed |
| P2 | OpenClaw ignored custom base URLs; Pi checked only host/port and could switch tenant paths | Shared effective-request URL proof against published native/compat endpoints; schemes, ports, prefixes, versions and repeated separators covered | Reviewed; focused routing and real gateway fixtures |
| P3 | Stale Pi state plus an unrelated live listener could receive a provider key | Health response must identify the same running instance; CLI status also checks token, rejects redirects and bounds the probe | Reviewed; actual Pi stale-listener probe sends no provider request/key |
| P4 | OpenClaw renamed providers and selected model API could change siblings/fallbacks | Keep provider identity/auth/catalog/defaults; preserve provider API and check every affected model API before a provider-wide endpoint override | Reviewed; supported-route, direct fallback and sibling fixtures |
| P5 | Replacing Pi's URL changed Chat/Responses compatibility defaults and dropped resolved account or attribution headers | Freeze source-derived public defaults; resolve headers through the public registry API; only reroute when endpoint-specific attribution can be preserved | Reviewed with actual Pi SDK payloads |
| P6 | OpenClaw's URL rewrite changed private provider metadata and payload policy, beyond its public `compat` settings; raw provider casing bypassed native guards | Normalize policy comparisons as the actual configured-model resolver does, without changing config keys. Unsupported native policies stay direct; public settings are frozen for supported custom routes. Full native compression remains unfinished | Reviewed; 40 route/compatibility cases and an independent ten-case pinned configured-model resolver probe passed |
| P7 | Hermes and API-key Codex append resource paths to a base missing `/v1`, causing native 404s | Add `/v1` to wrap and native configuration/journal/status routes; retain Codex's dedicated unversioned ChatGPT root. Conformance stubs now append the actual SDK resources | Actual Hermes 0.19.1 and Codex 0.153.4 reproduced the failures. Route regressions and native Codex streamed recovery pass; independent review pending |
| P8 | opencode wrap discarded its entire existing inline configuration, losing model, permissions, account settings and MCP servers | Merge the routing overlay into native JSONC; preserve environment/file references and reject malformed input without disclosing values | Six regressions failed before the fix; 19 focused cases and pinned native opencode 1.18.27 configuration checks pass. Independent review pending |
| P9 | Hermes and opencode can retain the original provider credential while replacing its custom endpoint with a different proxy destination | Open: preserve the original provider/account contract and require proof of the effective destination before routing | Actual Hermes requests reached provider B using provider A's synthetic key; pinned opencode configuration and SDK resolution prove the same endpoint/key mismatch |
| P10 | Hermes direct fallback retained Caveman's forced `--provider custom`, overriding the user's configured provider when proxy startup failed | Strip Hermes's injected provider prefix on direct fallback while retaining the user's original arguments | Both permanent cases failed before the fix; 172 Hermes/Qwen regression cases pass. Independent review pending |
| A9 | Provider-specific custom headers disappeared, while header forwarding lacked per-mount scope | Publish `forward_headers` names per configured compatibility mount; preserve declared headers and supported SDK attribution/affinity headers; unrelated mount secrets remain blocked | Reviewed with actual HTTP gateways and SDK header behavior |
| A10 | `Connection` nominations could reach upstream after the `Connection` field itself was discarded | Remove nominated hop-by-hop headers before transport, including after authentication fallback and retries | Reviewed; custom and native header regressions |
| E1 | `stop caveman` still received reminders | Explicit stop-state behavior and provider composer regressions | Reviewed |
| E2 | Delayed send replay could target a changed draft, editor or conversation | Transaction-bound replay; text, rich-content and input changes cancel pending send | Reviewed |
| E3 | Document-wide Send selectors could choose feedback controls | Composer-scoped actions and provider-specific fixtures | Reviewed |
| E4 | Firefox launcher interpolated an unquoted temp path through a shell | Structured argv, process failure reporting and actual addon execution | Reviewed |
| E5 | Firefox review CTA used a Chrome store identifier | Firefox-specific verified metadata or hidden unavailable destination | Reviewed |
| S1 | SDK redirects forwarded upstream/API credentials to another origin | TypeScript rejects redirects including raw overrides; Python private opener rejects redirects; distinct local-origin tests for 301/302/303/307/308 | Reviewed |
| S2 | TypeScript request deadline ended at response headers; custom fetch bodies could hang indefinitely | Shared abort deadline covers stream/decoder reads, clone metadata and cancellation; discarded error bodies release resources | Reviewed |
| H1 | Standalone hook install/uninstall overwrote or deleted unrelated `hooks/package.json` | Preflight compatible manifests, preserve foreign content and delete only exact owned manifest | Reviewed |
| H2 | PowerShell printed installation success after Node merge failure; standalone JSONC handling was unsafe | Propagate child exit, preflight settings, use shared JSONC parser from a clone and refuse unsupported detached input before mutation | Reviewed |
| M1 | Fresh default/custom MCP home could not create recovery database | Create missing home parent with restrictive permissions; actual binary tests preserve explicit DB-path failure behavior | Reviewed |
| M2 | MCP “session” stats returned lifetime database totals and missed repeat/pass-through calls | Process-local call accounting independent of shared-store history/concurrent servers; Engine lifetime stats unchanged | Reviewed |
| M3 | Repeat recovery assumed old results remained visible after host compaction | Every valid handle/query stays retrievable; no process-lifetime repeat denial or query-count widening; actual binary repeat/restart tests | Reviewed |
| M4 | SQLite replacement let recovery writes succeed against retired files and could let cleanup alter replacement journals | Any observed main/WAL/SHM identity change permanently invalidates that Store; withhold handles and retain unsafe connections until exit. Failed initial opens also retain opened disk connections; no automatic replacement adoption | Reviewed; real old/fresh process operations, journal-byte preservation and fresh CCR race tests pass. Concurrent initial opening remains a documented driver limit |
| M5 | Codex clears custom recovery-location variables from its MCP child, so valid proxy handles cannot be found | Forward only `CAVEMAN_HOME` and `CAVEMAN_CCR_DB` through Codex's supported `env_vars` in wrap, native enable and MCP install | Native streamed Codex request stored a valid handle, then actual MCP returned `cave_unknown_handle`; three permanent cases failed before the fix. Native Codex 0.153.4 now retrieves exact content with default home, custom home and an explicit database path containing spaces; 48 focused regressions pass. Independent review pending |
| N1 | Unrelated Pixel transforms rounded large numeric tool arguments/schema constraints | Preserve JSON number lexemes across parse/clone/render paths; integer/decimal/exponent and live-tail regressions | Reviewed |
| N2 | Top-level `Decoder.More` accepted valid JSON plus malformed suffixes and emitted repaired content | Require second decode to return EOF; compressor pass-through and retrieval record-boundary regressions | Reviewed |
| N3 | Shrink's prose rules removed CJK intent and significant whitespace (#575) | Bypass prose transforms for Han, Hiragana, Katakana, Hangul and Bopomofo input; exact Chinese/Japanese/Korean/astral-Han fixtures | Reviewed; 20 shrink cases passed |
| N4 | Direct execution of the documented compression CLI failed on relative imports (#105) | Resolve the script package from its own file before imports; actual execution from another working directory with spaces and Unicode | Reviewed; three direct-entry cases passed |
| H3 | Gemini stats read unrelated Claude logs and presented them as the current host's usage (#403) | Gemini returns native stats guidance before any Claude state operation; Claude hooks pass explicit host ownership | Reviewed; instrumented reads/writes plus nested-host control |
| H4 | Stats invented fixed-ratio savings and converted missing or invalid usage counters into numeric totals | Savings remain unknown without comparison; validate safe integer counters and retain complete/partial/unknown availability through session, share and history output | Reviewed; 60 existing and 13 independent usage cases passed |
| H5 | Fallback skill instructions imposed Portuguese examples and failed to prioritize an explicit project reply language (#812) | Remove foreign-language fallback examples and honor fixed user/project language before dominant prompt language; refresh shipped copies | Static contract fixed; model behavior is not guaranteed |
| S3 | The new stats terminal summary displayed unavailable provider/cache usage and missing request comparisons as zero | Render unknown values and partial subtotals with explicit usage/comparison coverage; retain provider-reported zero | Reviewed; permanent absent/partial/zero controls failed before the fix and pass now |
| S4 | The new stats report discarded known spend from complete provider usage when a stream later failed | Snapshot valid observed spend separately from counterfactual savings eligibility; failed requests still cannot earn savings | Reviewed; actual Anthropic stream through gateway, store and report counts both billed rows and only the successful savings comparison |
| D2 | README and plugin metadata claimed unsupported output reduction and quality equivalence; chart parsing no longer matched the benchmark harness | Remove unsupported claims; plot only the current harness's skill-versus-terse data, otherwise show unpublished result status | Reviewed; six tests plus real harness-to-chart increases, zero counts, escaping and SVG freshness |
| CI1 | Hermes rejected wheel installs; an editable URL plus a duplicate package requirement still searched PyPI for an unavailable release | Install the immutable editable URL, then check installed metadata against the registry version; mismatched version fails | Reviewed; exact final workflow command, full core dependencies, `pip check`, version and help pass on fresh macOS arm64 and Linux amd64 container environments |
| D1 | YAML example used an ignored `azure` key | Corrected to `azure_openai` | Reviewed |

## Source evidence

- The [complete vendor discovery matrix](installer-provider-discovery.md)
  records all 30 original skill profiles and Antigravity 2.0, with pinned source
  links or official documentation for closed-source hosts. It follows the actual
  `skills@1.5.24` algorithm: universal profiles stop at the shared directory and
  do not create their declared alias. Metadata agreement alone was insufficient.
  Continue's actual traversal and iFlow's shipped scanner were executed against
  isolated filesystem controls; no full native host run is inferred from those probes.
- [Hermes immutable build guard](https://github.com/NousResearch/hermes-agent/blob/1f8acb340f9c72aec1426248cca54139816a77f5/setup.py)
  explains both failures in [conformance run
  34083480150](https://github.com/JuliusBrussee/caveman/actions/runs/34083480150).
- Authentication/signing contracts and sources: [Provider authentication](provider-authentication.md).
  Stream and count-route references: [Proxy and providers](proxy-and-providers.md).
- OpenClaw routing was checked against [provider configuration
  docs](https://docs.openclaw.ai/concepts/model-providers) and actual transport
  source at `0965053fe6b9341776df147a6934b7485c60b5ca` (`v2026.8.2`). Pi behavior was
  checked with installed `pi-coding-agent@0.84.2` and its own `pi-ai@0.84.2`, including
  actual ModelRuntime, provider payload generation and CLI execution with local services.
- [Gemini CLI stats commands](https://geminicli.com/docs/cli/commands/) and
  installed `@google/gemini-cli@0.53.1` identify the supported native stats paths;
  its shell execution service sets `GEMINI_CLI=1` on child commands.
- Codex compaction replaces message history while its MCP runtime remains alive;
  [remote compaction source](https://github.com/openai/codex/blob/main/codex-rs/core/src/compact_remote.rs)
  and [thread runtime ownership](https://github.com/openai/codex/blob/main/codex-rs/core/src/state/service.rs)
  invalidate an MCP-process-lifetime assumption of transcript presence.
- Read-only live DOM inspection found Claude ProseMirror, Gemini Quill, and
  ChatGPT's logged-out textarea/composer-submit layout. No live content was edited.

## Verification ledger

These are observations of the tested source snapshot, not a final audit PASS.
The broad CLI/Windows observations below precede the latest Codex and opencode
changes. Their focused checks are recorded above; integrated refresh remains due.

| Command / mechanism | Latest completed observation |
| --- | --- |
| Root `npm test` | Refreshed: 371 passed, two skipped (native Windows PowerShell argv tests on macOS); bounded test-file concurrency |
| Root Python `python3 -m unittest discover -s tests -v` | Refreshed: 141 passed |
| Standalone `tests/test_*.js` files | Refreshed: all 14 files passed, including new usage and CJK cases |
| `python3 tests/verify_repo.py` | Refreshed: all local verification checks passed, including packaged helper/skills and hook flow |
| Native skill installer focused tests | Refreshed: 56 passed, including both Antigravity targets; separate ownership helper checks passed |
| CLI status portable fixtures | Seven passed with isolated host homes/PATH; added to Windows gate |
| CLI stats portable fixtures | Six passed, no skips; actual CLI JSON/help paths use isolated native stubs and are included in Windows gate |
| SDK TypeScript `npm test` | Build/type checks and 206 runtime tests passed |
| SDK Python full suite | 257 passed |
| Standalone installer safety | 22 Bash/PowerShell cases passed; PowerShell 7.6.5 ran on macOS |
| Pi `npm test` | 87 passed, including build/typecheck, actual Pi payload generation, registry preservation, stale-listener refusal and recovery lifecycle |
| OpenClaw wrapper runtime | 11 passed with portable fixtures, live local listener and explicit routing-state proof |
| Native Codex recovery | Actual Codex 0.153.4, real proxy and stdio MCP: three isolated streamed runs passed against a synthetic loopback Responses provider. Default/custom homes and explicit database path covered; exact retrieval reached the next request and recorded savings stayed zero. Pinned registry version remains 0.153.0; this is not that version or Windows proof |
| Provider protocol batch | 14 packages passed; 42 streaming/error cases, 32 count-route cases, credential and signing regressions |
| Go Engine/compressors/shrink/MCP and Rewriter batches | Passed; fresh-home, repeated retrieval and restart stats use actual compiled MCP binary |
| Full `GOMAXPROCS=2 go test -p 2 ./...` | Refreshed after final CCR and stats changes: passed |
| CCR `go test -race -p 2 ./engine/ccr -count=1` | Refreshed: passed, 55.5 seconds |
| Browser extension | 40 Chromium browser cases and five actual Firefox 153.0.4 staged-addon cases passed |
| Windows gate's Node contracts | Refreshed on settled source: 177 passed, four skipped on macOS; includes portable stats/status fixtures and bounded child-process test concurrency |
| Windows Go compile | Refreshed on settled source: all packages compiled for both amd64 and arm64 |
| Full CLI `npm test` with the rebuilt real proxy | Refreshed: 1,016 passed, 18 skipped. A POSIX Node fixture now preserves shared-library lookup; all previously failing Qwen/status/sync cases pass |
| Complete Windows compatibility gate | Passed on macOS: CLI/agent builds, 177 Node contracts and both Windows Go architecture compilations. Native Windows execution remains unverified |

The opt-in [native Codex recovery probe](../../scripts/probe-native-codex-recovery.mjs)
requires built CLI output and explicit native Codex, proxy and MCP binaries:

```sh
CAVEMAN_NATIVE_CODEX_BIN=/absolute/path/to/codex \
CAVEMAN_PROXY_BIN=/absolute/path/to/caveman-proxy \
CAVEMAN_MCP_BIN=/absolute/path/to/caveman-mcp \
node scripts/probe-native-codex-recovery.mjs --custom-home --explicit-db
```

Omit both flags to exercise the default store with neither recovery-location
variable set. Use only `--custom-home` for the custom-home case. The probe requires
macOS `sandbox-exec`, denies the native host external network and user-home reads,
uses synthetic credentials and keeps logs plus a hashed receipt under its printed
scratch directory. Its synthetic provider requests the actual MCP tool; the
probe grants that tool approval only in the isolated launch configuration. It
does not test hosted inference, production tool approval, or native Windows.

## Issue hints without a reproduced remaining defect

- **#897 (concurrent encoded responses):** local gateway tests overlapped 12
  requests across gzip/zstd, Content-Length/chunked framing and record/compress
  modes. All 96 cases passed; three race-detector repetitions passed 288 cases.
  They assert original wire bytes, decoded response identity, complete usage and
  no replay. This does not close or disprove the reported live failure.
- **#949 (Codex on Windows):** npm-generated `.cmd` shims from official Codex
  0.150.1 and 0.153.4 archives resolve through the portable launcher with literal
  metacharacter arguments. Both upstream hook runners preserve descendants after
  completed hooks; hook discovery still requires the host's enabled/trusted
  state. An approved-hook cold start and subsequent daemon survival on native
  Windows remain unverified. No speculative production change was made.

Initial baselines also passed root Python (137), standalone JS (13), core Go,
and `tests/verify_repo.py`; they must be refreshed where later edits affect them.

The native Windows CI jobs and full pinned-host conformance have not run on
this worktree. Remote green jobs cover earlier revisions. Hermes's corrected
workflow command now passes with all core dependencies on fresh CPython 3.12
environments on macOS arm64 and Linux amd64; the Linux proof used a local
container, not the hosted Ubuntu runner. Optional extras and provider inference
were not exercised. Installed-agent version/help probes are not provider
inference proof, and older/missing binaries are not automatically API defects.

The goal remains active until the full scope above has adequate evidence.
