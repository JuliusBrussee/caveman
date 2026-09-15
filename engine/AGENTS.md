# engine — the content-aware Caveman Engine (commercial, binary-distributed)

Detect a payload's type → route to a safety-classed compressor → count the token reduction
→ store the original for recovery. The stable four-call API (`Compress`/`Retrieve`/`Detect`/`Stats`)
is what the proxy, CLI, SDKs, MCP, and WASM build all share. Everything it reports is
`inferred`; it never says `verified`.

## Layout
- `engine.go` — the `Engine` core: detect → route → compress → token ratio → CCR. `record` mode + miss + parse-fail + not-smaller + no-store all pass through.
- `result.go` — `Result`/`Options`/`Mode`; unknown mode fails closed to `record`.
- `detect.go` — content router (`json`/`diff`/`code`/`log`/`search-result`/`html`/`tabular`/`config`/`terminal`/`text`); low confidence → `text`.
- `listing.go` — strips a read tool's line-number gutter (`1\t{`, `2\t  "unit"`) before Detect and before the compressor, then restores it after. Agents send file *listings*, not files; the gutter is presentation, and left in place it made every guttered payload detect as `text` and compress ~0%. Restoration keeps each surviving line's ORIGINAL number and is declined entirely for transforms that restructure rather than elide (re-encoded JSON), because numbers that describe nothing are worse than none.
- `safety/` — the S0–S4 registry; S4 is lossy and `RequiresCCR`. Unknown class → fail closed.
- `tokens/` — `Counter` interface; default is an offline, vocab-embedded BPE tokenizer (o200k_base). `inferred` always.
- `contextwindow/` — deterministic BM25 context packer with recency/error/priority signals and token-budget accounting.
- `compressors/` — `Compressor` interface + registry; structural JSON/log/search/diff/text/HTML/table/config/code compressors plus forced-only TOON/tool-schema/accessibility/repetition paths — `Default()` registers 14. A compressor is a pure byte transform.
- `ccr/` — `~/.caveman/ccr.db` SQLite recovery store; content-addressed handles; byte-exact `Get`.
- `pixel/` — pxpipe port (MIT, see its NOTICE): text→PNG request compression. Embedded glyph atlases + renderer + profitability gate + per-wire-format transforms (Anthropic/OpenAI/Gemini). S4-lossy, allowlist-gated (`CAVE_PIXEL_MODELS`, default `claude-fable-5,gpt-5.6`), consumed by the proxy's `pixel` mode; never wired into `Detect` and never imported by the WASM build (≈4 MB assets).
- `evals/` — the local eval harness + fail-closed graders + embedded `fixtures/`. `Run()` is the quality gate.
- `cmd/caveman-engine/` — the binary the CLI shells out to: `compress | detect | retrieve | stats | registry | toon encode|decode | evals run | pixel render|simulate`. `toon` is the stateless (no-CCR) JSON⇄TOON converter; both directions fail closed.

## Conventions
- Build/test: `make product-build PRODUCT=engine` / `make product-test PRODUCT=engine`.
- A new compressor is a self-contained file in `compressors/` + tests, registered in `Default()`. Forced-only compressors such as `toolschema` and `toon` must not be added to `Detect`.
- Compressors never count tokens, touch CCR, or hit the network — the engine core does that around them.
- **The `toolschema` transform is client-side-only today.** Registration makes it forceable by
  engine callers; it does not make it reachable from managed-gateway traffic. Provider adapters
  intentionally keep tool arrays in the frozen prompt-cache prefix, and no billed route invokes
  this compressor. Engine API/CLI callers may force it locally, and `caveman-shrink` is its
  dedicated product surface; those reductions remain local and `inferred`. Managed gateway has a
  separate S2 tool-search/deferral path; do not conflate it with compression. Any future gateway
  route for this transform needs cache-versus-schema cost arithmetic, stable byte-identical prefix
  output, and an eval gate before activation.

## Gotchas (honesty invariants — correctness, not style)
- **fail-closed transforms**: every compressor passes the original through unchanged on any parse problem; the engine also passes through when the result is not smaller in tokens. Reserve **byte-safe** for classes whose `safety.Info.ByteSafe` is true.
- **CCR-or-pass-through**: a lossy (S4) result is only emitted if its original was stored; with no store, the engine fails closed to pass-through.
- **inferred-only**: ratios are token estimates labeled `inferred`; never `verified`, never re-projected.
- **fail-closed**: unknown mode → `record`; unknown content type → `text`; unknown grader → `passed:false`.
- **cgo**: full code compression (Python/JS/TS) needs the tree-sitter build; the cgo-free build compresses Go only. The embedded eval fixtures cover all three under cgo.
- **boundary**: this is `public/` — never import `cloud/…`. `make check-boundaries` enforces it.

See ../../CLAUDE.md (root)
