# cavemem

Durable, compression-native agent memory: `remember`, `recall`, `supersede`,
`history`, `forget`.
Memories live in a local SQLite store; recall ranks them with BM25 behind a
conservative threshold and compresses each hit through the
[Caveman engine](../engine), so the inferred token cost is honest and the dropped
detail stays recoverable. Counts are inferred; component never claims `verified`
savings.

Go core and binary ship under BSL 1.1. Thin JS/Python clients remain MIT.
BSL runtime is source-available, not OSI Open Source before Change Date. See
`LICENSE` and `../LICENSING.md`.

## CLI

```bash
go build -o cavemem ./mem/cmd/cavemem
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

- Byte-safe write: raw text is stored before anything else, so a memory is never lost.
- Off-topic query recalls nothing rather than guessing.
- Recall packs at most 2000 inferred tokens by default. CLI, MCP,
  JS, and Python callers may explicitly request `token_budget: 0` for unlimited
  recall; omitting it never disables the cap.
- Recall excludes superseded facts by default; history stays local and auditable.
- Every compressed recall hit carries a `recovery_handle`.
