# caveman-shrink

Compress MCP/OpenAI **tool catalogs** so a large tool list costs fewer tokens.
It drops annotation bloat (examples, titles, comments, schema markers), reduces
long descriptions while retaining recognised constraint-bearing sentences, and
keeps the structural selection surface plus argument-construction values
(`default`, `const`, `$ref` targets) byte-for-byte. Description reduction is
model-visible and lossy: structural preservation does not guarantee the model
will pick the same tool. All numbers are `inferred`.

## Use

```bash
# Compress a catalog (stdin → stdout); the inferred ratio report goes to stderr.
cat tools.json | caveman-shrink > tools.min.json

# Recover exact original bytes from a handle printed in shrink's stderr report.
caveman-shrink recover ccr_... > tools.original.json

# Inspect the per-tool reduction without committing to it.
caveman-shrink lint tools.json
```

MIT launcher downloads matching BSL-1.1 binary on first run, verifies
key-signed checksum manifest plus artifact SHA-256, and caches it under
`~/.caveman/bin`. No Go toolchain or global Caveman install is required:

```bash
npx -y caveman-shrink lint tools.json
```

MIT applies to npm launcher. Downloaded binary follows BSL-1.1 terms named
in `BINARY_LICENSE.md`.

## Guarantees

- **Structural selection surface** — the compressed catalog exposes the exact
  same names/params/enums/required as the original. Same-tool behavior needs a
  model eval; this structural check alone cannot prove it.
- **Argument-construction preservation** — short descriptions remain whole;
  long descriptions retain their lead plus recognised constraint sentences.
  Defaults, constants, and internal reference targets survive.
- **Fail-open** — successful compression is S4 and model-visible; malformed or
  incompressible input passes through unchanged.
- **Bounded input** — stdin is capped at 32 MiB. Larger catalogs fail with
  `cave_input_too_large`; they are not buffered without limit.
- **Reversible** — a compressing shrink commits exact original bytes to
  `CAVEMAN_CCR_DB` (or `~/.caveman/ccr.db`) before returning its handle; a later
  process resolves it with `caveman-shrink recover`.
