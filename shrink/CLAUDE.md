# shrink — caveman-shrink, the tool-catalog compressor (commercial Go core + MIT launcher)

Compress MCP/OpenAI **tool definitions** before they fill the context window. A thin Go wrapper
over the [engine](../engine/CLAUDE.md)'s `toolschema` compressor: it drops annotation metadata
(examples, titles, `$comment`, `$schema`) and reduces free-text descriptions to their lead
sentence **plus every constraint-bearing sentence**, while keeping every **selection token** —
tool and parameter names, types, enums, required — and every **argument-construction value** —
`default`, `const`, `$ref` targets — byte-for-byte. Everything is `inferred`.

## Layout
- `shrink.go` — the library: `Shrink` (catalog → compressed, fail-open S4 transform, durable-CCR-backed), `Recover` (handle → original bytes), `Lint` (per-tool inferred token reductions), `SelectionProfile` (the structural surface — what must survive). `WithStore`/`WithStorePath` pick the recovery store; default is the shared `CAVEMAN_CCR_DB` / `~/.caveman/ccr.db`.
- `cmd/caveman-shrink/` — the CLI: `caveman-shrink` / `shrink` (stdin→stdout), `lint <file>`, and `recover <handle>` (prints the original catalog for a handle a prior shrink minted).
- `bin/caveman-shrink.mjs` + `package.json` — the `npx caveman-shrink` launcher around the prebuilt binary.

## Two correctness boundaries
1. **structural selection surface** — `SelectionProfile(input)` ==
   `SelectionProfile(Shrink(input).Output)` always. This proves names/params/enums/required survive
   byte-for-byte. It does **not** prove the model selects the same tool: descriptions are
   model-visible and long descriptions are lossy. Behavioral equivalence requires model-eval
   fixtures; current results stay `inferred`.
2. **argument validity** — agents build tool *arguments* from descriptions, so shrink preserves
   selection but **does not by itself guarantee argument validity**. What it guarantees, biased
   to over-keep: (a) a description that already fits a small budget (≤ `maxDescLen*2` bytes) is
   kept **whole** — no sentence is elided, so a constraint phrased without a recognised marker
   still survives; (b) in a genuinely large description, the lead sentence plus every sentence
   carrying a constraint marker (must / cannot / required / rejected / invalid / not allowed /
   exactly one / at least / at most / max / min / range / between / greater-than / over / above /
   unique / mutually exclusive / format / ISO / RFC / absolute …) is retained in full. Truncating
   an abbreviation ("Target path, e.g. /srv…" → "Target path, e.") or dropping
   `default`/RFC3339/bound rules would produce invalid calls the structural profile cannot see;
   the call-validity conformance test (`engine/compressors`, golden fixture + reproduced
   under-keep cases) locks recognised constraint tokens in place. Over-keep is the rule — never
   knowingly under-keep.

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
- **reversible for real** — a compressing shrink writes the exact original to the **durable** store (`engine.Compress` commits the recovery row before it publishes transformed bytes), so a printed handle resolves later, from a separate process, via `Recover` / `caveman-shrink recover`. It is NOT an in-memory store discarded on return — that minted handles that were unresolvable the instant `Shrink` returned. If the durable store cannot be opened, `Shrink` returns an error and the CLI forwards the original bytes: no handle, no reversibility claim.

See ../../CLAUDE.md (root) · ../engine/CLAUDE.md
