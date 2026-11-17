# engine: the Caveman Engine

Go library that detects a payload's type, routes it to a safety-classed compressor, counts the
token reduction and stores the original for recovery (CCR). The proxy, `caveman-engine` CLI, MCP
server, cavemem, shrink and browse all call the same four entry points: `Compress`, `Retrieve`,
`Detect`, `Stats`. Every ratio it reports is `inferred`, never `verified`. Apache-2.0
(`../LICENSING.md`); `pixel/` is an MIT port (see `pixel/NOTICE`).

## Layout
- `engine.go`: the core. Detect, route, compress, count, store. `record` mode, a miss, a parse failure, a not-smaller result and a missing store all pass the input through.
- `result.go`: `Result`, `Options`, `Mode`. An unknown mode falls back to `record`.
- `detect.go`: content router (`json`, `diff`, `code`, `log`, `test-report`, `search-result`, `html`, `tabular`, `config`, `terminal`, `text`). Low confidence means `text`.
- `listing.go`, `filewrap.go`, `codexexec.go`: strip presentation that agents wrap around content (a read tool's line-number gutter, `<file name=...>` wrappers, Codex exec JSON lines) before Detect, then restore it. A restored gutter keeps each surviving line's original number; transforms that restructure (re-encoded JSON) get no gutter back.
- `retrieve_query.go`: query-narrowed recovery views.
- `compressors/`: the `Compressor` interface and registry. `Default()` registers 16. `toolschema`, `toolschema-annotations`, `toon`, `a11y` and `repetition` are forced-only (`Options.Type`) and never returned by `Detect`.
- `safety/`: S0 to S4 classes. S4 is lossy and `RequiresCCR`; an unknown class fails closed.
- `tokens/`: offline o200k_base BPE counter with an embedded vocab.
- `contextwindow/`: deterministic BM25 context packer with token budgets (cavemem's recall uses it).
- `ccr/`: SQLite recovery store at `~/.caveman/ccr.db` (blob handles plus the native runtime's typed objects). One serialized connection per store.
- `pixel/`: text-to-PNG request compression for the proxy's `pixel` mode. S4, gated by `CAVE_PIXEL_MODELS` (default `claude-fable-5,gpt-5.6`). Never wired into `Detect`; the WASM build must not import it (about 4 MB of assets).
- `image/`: offline image-token reducer. No production path calls it yet.
- `evals/`: local eval harness, fail-closed graders, embedded fixtures. `evals.Run()` is the quality gate.
- `cmd/caveman-engine/`: CLI (`compress`, `detect`, `retrieve`, `stats`, `registry`, `toon encode|decode`, `evals run`, `pixel render|simulate`, `version`). `cmd/caveman-wasm/`: the WASM build.

## Build and test (from the repo root)
- `go build ./engine/...` and `go test ./engine/...`. CI (`engine-ci.yml`) runs `go vet` and `go test ./...`.
- `go run ./engine/cmd/caveman-engine evals run` runs the embedded evals.
- Release binaries are cgo builds through a pinned zig: `ZIG=/path/to/zig node scripts/build-release-binaries.mjs --test --out <dir>` (the `release-shape` CI lane).

## Invariants
- **Fail closed.** A compressor returns the original unchanged on any parse problem, and the engine passes through when the result is not smaller in tokens. Unknown mode means `record`, unknown type means `text`, unknown grader means `passed:false`.
- **CCR or pass-through.** An S4 result is emitted only after its original is stored. No store, no lossy output.
- **Inferred only.** Ratios are token estimates labelled `inferred`. Call a class byte-safe only when `safety.Info.ByteSafe` is true.
- **Compressors are pure byte transforms.** They never count tokens, touch CCR or open the network; the core does that around them. A new compressor is one file plus tests in `compressors/`, registered in `Default()`.
- **Elide repetition, never a document.** Every unit-dropping compressor (text, log, json, tabular, config, searchresult, terminal) calls `keepNonRedundant` (`compressors/redundancy.go`), which keeps one unit per distinct content class. A bibliography or a table of measurements survives whole. This is a correctness rule, not a tuning knob: eliding a list the agent had to enumerate cost the agent bench 5.5x its baseline.
- **An elision marker states only what it verified** (`compressors/invariants.go`). Facts come from the exact units replaced: constants, full enumerations whose counts sum to the elided count, ranges built from original value strings, identifier coverage (`25 distinct, a..b`, or `all N present` only when density is counted). Anything that cannot be stated in full (credential-shaped names, long values, more than 5 distinct values) is withheld entirely, because a partial list reads as complete. A summary that sheds facts for its byte budget says `+N omitted`. A run under 3 units is not elided unless collapsing still halves its bytes. Agents that cannot tell what was elided loop on `caveman_retrieve` (46 calls in one measured task).
- **A recovery view is made of whole units** (`retrieve_query.go`): a complete JSON record, a whole CSV row with the header, a whole log line, a whole paragraph. Never a field line. Content it cannot split returns in full, and every gap prints `… [caveman: non-adjacent] …` so adjacency in the view never implies adjacency in the original. A fragment once made an agent attribute one order's status to its neighbour.
- **Two handle id spaces, one recovery surface.** `Engine.Retrieve` resolves a blob handle (`ccr_…`, `store.Get`) first, then a typed object (`store.GetObject`, shown to agents as `ccr://<id>`). The MCP adapter normalizes `ccr_…`, `<<ccr:…>>`, `ccr:…` and `ccr://…`. Agents copy whatever reference they see; a form that does not resolve makes them loop.
- **The CCR store reclaims room and refuses damage.** A write refused for capacity runs one reclaim (`ccr/store_reclaim.go`): dedupe unreferenced RepositoryMap copies, then expire typed objects past retention down to 80% of the limits. Expired ids keep a tombstone, so `GetObject` returns `ErrExpired` and MCP answers `cave_expired_recovery`. Blob recoveries never expire. A store that fails `PRAGMA quick_check` never reclaims and refuses every write with `ErrStoreDamaged`; `caveman-proxy recovery-store status|check|repair` reports and rebuilds it. Keep `data_ref != ''` in every `data_ref` lookup so the partial index applies.
- **`toolschema` is forced-only.** `caveman-shrink` is its product surface. Provider adapters keep tool arrays in the prompt-cache prefix; the proxy's only catalog rewrite is the default-off `toolschema_strip: annotations` lever. A new proxy route for the lossy transform needs cache-versus-schema cost arithmetic, byte-stable output and an eval gate first.
- **Manifest exclusions are deliberate.** `test-report` and `toolschema-annotations` are registered but left out of `Capabilities()`: no compiled plan can route to them, and advertising one rotates `RegistrySHA256` and breaks existing Cave Build locks.
- **cgo matters.** Python, JS and TS code compression needs tree-sitter (cgo); `CGO_ENABLED=0` parses Go only. `TestReleaseShape*` carry no build tag on purpose and fail without cgo.
- **Dependency direction.** The engine imports no other package from this repo. Proxy, mcp, mem, shrink and browse import the engine, never the reverse.

See the root [AGENTS.md](../AGENTS.md).
