# shrink: caveman-shrink, the tool-catalog compressor

Compresses MCP and OpenAI tool definitions before they fill the context window. It is a thin Go
wrapper over the [engine](../engine/AGENTS.md)'s `toolschema` compressor: it drops annotation
metadata (examples, titles, `$comment`, `$schema`) and cuts long descriptions to the lead
sentence plus every constraint-bearing sentence. Tool and parameter names, types, enums,
`required`, `default`, `const` and `$ref` targets survive byte for byte. Every figure is
`inferred`.

## Layout
- `shrink.go`: the library. `Shrink` (catalog to compressed output, S4, backed by the durable CCR store), `Recover` (handle to original bytes), `Lint` (per-tool token reductions), `SelectionProfile` (the structural surface that must survive). `WithStore` and `WithStorePath` pick the store; the default is `CAVEMAN_CCR_DB`, else `$CAVEMAN_HOME/ccr.db`, else `~/.caveman/ccr.db`.
- `cmd/caveman-shrink/`: the CLI. `caveman-shrink` or `caveman-shrink shrink` (stdin to stdout), `lint <file>`, `recover <handle>`, `version [--json]`.
- `bin/caveman-shrink.mjs`, `package.json`: the `npx caveman-shrink` launcher around the prebuilt binary.

## Build and test (from the repo root)
- `go build ./shrink/...` and `go test ./shrink/...`.
- `go test ./engine/compressors/ -run ToolSchema` covers the compressor itself, including the golden constraint fixture (`engine/compressors/testdata/toolschema_constraints_catalog*.json`).
- `pnpm --filter caveman-shrink test` runs the launcher tests.

## Two correctness boundaries
1. **Structural selection surface.** `SelectionProfile(input)` must equal `SelectionProfile(Shrink(input).Output)`. That proves names, parameters, enums and `required` survive. It does not prove the model picks the same tool, because descriptions are model-visible and long ones are lossy. Behavioural equivalence needs model-eval fixtures; until then results stay `inferred`.
2. **Argument validity.** Agents build arguments from descriptions, so keeping selection intact is not enough. A description of at most `maxDescLen*2` bytes is kept whole, so a constraint phrased without a known marker still survives. A longer one keeps its lead sentence plus every sentence with a constraint marker (must, cannot, required, at least, at most, between, format, ISO, RFC and similar). Abbreviations such as "e.g." must not end a sentence early. Over-keep is the rule; never knowingly under-keep, because a dropped bound or format produces invalid calls the structural profile cannot see.

## Invariants
- **The compressor lives in the engine** (`engine/compressors/toolschema.go`). shrink reuses it and never forks it.
- **This is the product surface for `toolschema`.** Engine callers can also force it. Proxy provider adapters keep tool arrays in the prompt-cache prefix and do not run this lossy transform; a billed route for it would need cache-versus-schema cost proof, byte-stable output and an eval gate.
- **S4, fail open.** Successful compression changes model-visible bytes. Malformed or incompressible catalogs pass through unchanged (`ratio:0`, no handle).
- **Reversible for real.** A compressing `Shrink` commits the original to the durable store before it returns a handle, so another process can resolve it with `Recover` or `caveman-shrink recover`. An in-memory store would mint handles that die when `Shrink` returns. If the store cannot be opened, library `Shrink` returns an error and the CLI forwards the original bytes with no handle and no reversibility claim.
- **Inferred only.** Token counts come from the engine's offline counter; never `verified`.

See the root [AGENTS.md](../AGENTS.md).
