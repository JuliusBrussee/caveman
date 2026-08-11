# cavemem

Durable, compression-native agent memory: `remember`, `recall`, `supersede`,
`history`, `forget`.
Memories live in a local SQLite store; recall ranks them with BM25 behind a
conservative threshold and compresses each hit through the
[Caveman engine](../engine), so the inferred token cost is honest and the dropped
detail stays recoverable. **Inferred-only** — it never claims `verified` savings.

## CLI

```bash
go build -o cavemem ./public/mem/cmd/cavemem
./cavemem remember "the deploy key lives in vault under ops/deploy"
./cavemem recall   "where is the deploy key"      # JSON: { hits: [...], basis: "inferred" }
./cavemem recall   "full migration context" 5 0   # explicit 0 token budget = unlimited
./cavemem supersede mem_xxxxxxxx "deploy key moved to vault ops/deploy-v2"
./cavemem history   mem_yyyyyyyy                  # oldest → current
./cavemem forget   mem_xxxxxxxx
./cavemem          # runs the MCP server over stdio
```

## MCP

```jsonc
{ "mcpServers": { "cavemem": { "command": "cavemem" } } }
```

## Libraries

Thin clients shell text to the binary over stdin (avoiding OS argument-size limits;
set `CAVEMEM_BIN` if it is not on PATH):
an oversized `remember` exits with code 65; both clients export
`MEMORY_TOO_LARGE_EXIT_CODE` so callers can branch without parsing stderr.

```js
import { remember, recall, supersede, history, forget } from "cavemem"; // js/index.mjs
await remember("…"); await recall("…", 5, 2000); await recall("…", 5, 0); // unlimited
```
```python
import cavemem                                            # py/cavemem.py
cavemem.remember("…"); cavemem.recall("…", limit=5, token_budget=2000)
```

## Guarantees

- **Byte-safe write** — raw text is stored before anything else; a memory is never lost.
- **Fails toward nothing** — an off-topic query recalls nothing rather than guessing.
- **Bounded by default** — recall packs at most 2000 inferred tokens. CLI, MCP,
  JS, and Python callers may explicitly request `token_budget: 0` for unlimited
  recall; omitting it never disables the cap.
- **Current by default** — recall excludes superseded facts; history stays local and auditable.
- **Reversible** — every compressed recall hit carries a `recovery_handle`.
