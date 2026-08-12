# shrink — caveman-shrink, the tool-catalog compressor (commercial Go core + MIT launcher)

Compress MCP/OpenAI **tool definitions** before they fill the context window. A thin Go wrapper
over the [engine](../engine/CLAUDE.md)'s `toolschema` compressor: it drops annotation metadata
(examples, titles, `$comment`, `$schema`) and reduces long descriptions to their lead plus every
constraint-bearing sentence. It keeps every **selection token** (tool/parameter names, types,
enums, required) and argument-construction value (`default`, `const`, `$ref` targets)
byte-for-byte. Everything is `inferred`.

## Layout
- `shrink.go` — the library: `Shrink` (catalog → compressed, fail-open S4 transform, durable-CCR-backed), `Recover` (handle → original bytes), `Lint` (per-tool inferred token reductions), `SelectionProfile` (the structural surface — what must survive). `WithStore`/`WithStorePath` select recovery storage; default is `CAVEMAN_CCR_DB` / `~/.caveman/ccr.db`.
- `cmd/caveman-shrink/` — the CLI: `caveman-shrink` / `shrink` (stdin→stdout), `lint <file>`, and `recover <handle>`.
- `bin/caveman-shrink.mjs` + `package.json` — the `npx caveman-shrink` launcher around the prebuilt binary.

## Two correctness boundaries
`SelectionProfile(input)` == `SelectionProfile(Shrink(input).Output)` always. This proves the
structural selection surface (names/params/enums/required) survives byte-for-byte. It does **not**
prove the model selects the same tool: descriptions are model-visible and long descriptions are
lossy. Behavioral selection equivalence requires model-eval fixtures; keep results `inferred`.

Selection alone does not guarantee valid arguments. Short descriptions stay whole; long ones
keep the lead plus every recognised constraint sentence (must/cannot/required/bounds/format/
ISO/RFC/absolute/etc.). `default`, `const`, and internal `$ref` targets survive. Golden and
adversarial conformance tests lock these construction surfaces; over-keep is safer than a retry.

## Conventions
- Build/test: `make product-build PRODUCT=shrink` / `make product-test PRODUCT=shrink`.
- The structural compressor lives in the **engine** (`compressors/toolschema.go`); shrink reuses it, never forks it.
- **This is the dedicated product surface for the `toolschema` compressor.** Engine API/CLI callers
  can also force it locally. Managed-gateway adapters keep tool arrays in the frozen prompt-cache
  prefix and never hand them to it; gateway's separate S2 tool-search/deferral path is not
  compression. Local reductions stay `inferred`. A future billed route for this transform needs
  cache-versus-schema cost proof, stable byte-identical prefix output, and an eval gate.

## Gotchas (honesty invariants)
- **S4 lossy, fail-open** — successful compression changes model-visible bytes; malformed/incompressible catalogs pass through unchanged (`ratio:0`, no handle).
- **inferred-only** — token counts are estimates via the engine's offline counter; never `verified`, never re-projected.
- **reversible for real** — a compressing shrink commits the exact original to the durable shared
  CCR store before returning a handle. A separate process resolves it through `Recover` /
  `caveman-shrink recover`; failure to open storage makes library `Shrink` return an error, while
  the CLI catches that error and forwards the original bytes with no handle.

See ../../CLAUDE.md (root) · ../engine/CLAUDE.md
