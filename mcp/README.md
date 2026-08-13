# caveman-mcp

The Caveman MCP server: five compression tools — `caveman_compress`,
`caveman_retrieve`, `caveman_stats`, `caveman_toon_encode`, and
`caveman_toon_decode` — over stdio, for any MCP host. It links the
[Caveman Engine](../engine) in-process. **Local-only** (opens no
network connection) and **inferred-only** (it never claims `verified` savings).

## Install (Claude Code)

```jsonc
// .mcp.json / claude mcp config
{
  "mcpServers": {
    "caveman": { "command": "npx", "args": ["-y", "caveman-mcp"] }
  }
}
```

The MIT launcher downloads matching BSL-1.1 binary on first run, verifies
key-signed checksum manifest plus artifact SHA-256, and caches it under
`~/.caveman/bin`. No Go toolchain or global Caveman install is required.
To use an existing reviewed binary instead:

```bash
CAVEMAN_MCP_BIN=/path/to/caveman-mcp npx caveman-mcp
```

MIT applies to npm launcher. Downloaded binary follows BSL-1.1 terms named
in `BINARY_LICENSE.md`.

## Tools

| Tool | Input | Returns |
|---|---|---|
| `caveman_compress` | `input` (string) | compressed text, inferred `ratio`, `recovery_handle` (null on pass-through) |
| `caveman_retrieve` | `recovery_handle` (string) | the byte-exact original; error on unknown handle |
| `caveman_stats` | — | session totals: tokens before/after, `ratio`, `basis:"inferred"`, `scope:"session"` |
| `caveman_toon_encode` | `input` (JSON string) | explicit JSON→TOON result with sizes; pass-through plus note when not encodable |
| `caveman_toon_decode` | `input` (TOON string) | decoded JSON; error on invalid TOON |

Compression is lossy (S4) but **reversible**: every drop is recoverable via
`caveman_retrieve`. Incompressible or malformed input passes through unchanged
with `ratio:0` — never an error.
