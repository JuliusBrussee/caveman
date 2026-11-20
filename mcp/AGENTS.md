# mcp: the Caveman MCP server

A stdio JSON-RPC adapter that exposes the [engine](../engine/AGENTS.md) as five MCP tools to any
host (Claude Code, Cursor, Codex and others). It owns only the MCP framing; compression runs in
process through the engine package. It opens no network connection, and every figure it reports
is `inferred`. cavemem and browse reuse its server through `mcp.NewServer`.

## Layout
- `server.go`: the generic `Server`. JSON-RPC loop, batching, notifications, size caps, panic containment, `initialize`.
- `engine_tools.go`: the five tools (`EngineTools`) and their handlers, behind an injectable `Engine` interface so framing is testable without real compressors.
- `protocol.go`: JSON-RPC and MCP result types, `ToolText`/`ToolError` helpers, error codes.
- `cmd/caveman-mcp/`: the binary. Opens the shared file CCR store (`CAVEMAN_CCR_DB`, else `$CAVEMAN_HOME/ccr.db`, else `~/.caveman/ccr.db`) so proxy handles resolve here. `CAVEMAN_MCP_EPHEMERAL=1` uses an in-memory store instead.
- `bin/caveman-mcp.mjs`, `package.json`: the `npx caveman-mcp` launcher that runs the prebuilt Go binary.

## The five tools (exact, case-sensitive names)
- `caveman_compress(input)`: compressed text, inferred ratio, `recovery_handle`. Incompressible, malformed or not-smaller input comes back unchanged with `ratio:0` and `recovery_handle:null`, never an error.
- `caveman_retrieve(recovery_handle, query?, verify_only?)`: the byte-exact original, or a query-narrowed view of whole records. Every valid call runs, including repeats: hosts compact their context without restarting the MCP process, so the server cannot know what the model still holds. An unknown handle returns `isError:true` with a `cave_` code, never a made-up payload.
- `caveman_stats()`: this process's counts, `basis:"inferred"`, `scope:"session"`.
- `caveman_toon_encode(input)`: JSON to TOON with both sizes; input that cannot round-trip comes back unchanged with a note.
- `caveman_toon_decode(input)`: TOON to JSON; invalid TOON returns `isError:true`, never the raw input as JSON.

## Build and test (from the repo root)
- `go build ./mcp/...` and `go test ./mcp/...`.
- `bun run --filter caveman-mcp test` runs the launcher tests.

## Invariants
- **stdout is the protocol channel.** Logs go to stderr only; a test guards this.
- **The transport cannot be killed short of EOF.** A malformed line gets `-32700` and the loop resyncs at the next newline (never `return`). A handler panic becomes `cave_tool_panicked`; a dispatch panic becomes `cave_internal_error`. Batches get one array response. Requests without an id are notifications and get no reply. A dead server is worse than a slow one: the proxy keeps eliding content that then has no `caveman_retrieve` to expand it.
- **Size caps, except recovery.** Inbound lines and generated tool output are capped at 16 MiB (`cave_payload_too_large`). `caveman_retrieve` sets `ExemptResultCap`: it returns the original bytes and must never fail on size, since the shared store has no matching ceiling.
- **Protocol negotiation never errors.** The server implements 2024-11-05 and answers `initialize` with that version whatever the client asks for; the client decides. Rejecting a newer version made current clients drop the server.
- **Fail open on content, fail closed on names.** Engine errors and malformed input pass through byte-identical. Unknown tools and handles return `isError` with a `cave_` code; unknown methods return `-32601`.
- **Zero egress.** The adapter's own source imports no `net`, `net/http`, `net/rpc` or `os/exec`; `TestZeroEgressNoNetworkImports` parses the source to enforce it.
- **Five tools are a measured prefix cost.** Every registered tool schema rides in the wrapped agent's prompt prefix on every call (about 11k tokens per call for these five). `caveman wrap` gates injection with the `execute.mcp` setting (`auto`, `marker-only`, `off`). Adding a sixth tool raises that cost for every wrapped agent.
- **Stdio only, string payloads only.** The engine detects the type.

See the root [AGENTS.md](../AGENTS.md).
