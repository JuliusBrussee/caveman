# mcp — the Caveman MCP server (commercial Go core + MIT launcher)

A thin **stdio JSON-RPC** adapter exposing the compression [engine](../engine/CLAUDE.md) as
five MCP tools to any host (Claude Code, Cursor, …). It owns only the MCP framing; all
compression is the engine's, linked **in-process** (no subprocess, no drift). Local-only — it
opens no network connection — and everything it reports is `inferred`, never `verified`.

## Layout
- `server.go` — the `Server`: JSON-RPC loop, dispatch, the five tool handlers. Takes an injectable `Engine` interface so the framing is testable without the real compressors.
- `protocol.go` — JSON-RPC + MCP tool-result types, `toolText`/`toolError` helpers, the exact `tools/list` definitions.
- `cmd/caveman-mcp/` — binary: opens shared file CCR store (`CAVEMAN_CCR_DB`,
  else `CAVEMAN_HOME/ccr.db`/`~/.caveman/ccr.db`) so proxy handles resolve across
  processes, then serves stdin↔stdout. `CAVEMAN_MCP_EPHEMERAL=1` opts into an
  isolated in-memory store for tests/sessions that do not need proxy recovery.
- `bin/caveman-mcp.mjs` + `package.json` — the `npx caveman-mcp` launcher that execs the prebuilt Go binary.

## The five tools (exact names, case-sensitive)
- `caveman_compress(input)` → compressed text + inferred ratio + `recovery_handle`. Lossy (S4), recoverable, and fail-closed: incompressible/malformed/not-smaller input returns unchanged, `ratio:0`, `recovery_handle:null` — never an error.
- `caveman_retrieve(recovery_handle)` → the byte-exact original. Unknown handle → `isError:true` + a `cave_snake_code`, never a fabricated payload.
- `caveman_stats()` → `basis:"inferred"`, `scope:"session"`; the string `verified` never appears.
- `caveman_toon_encode(input)` → explicit JSON→TOON re-encoding with input/output sizes; returns pass-through plus note when encoding fails.
- `caveman_toon_decode(input)` → TOON→JSON; invalid TOON returns `isError:true`, never raw input as JSON.

## Conventions
- Build/test: `make product-build PRODUCT=mcp` / `make product-test PRODUCT=mcp`.
- **stdout is the protocol channel** — logs go to stderr only (a dedicated test guards this).

## Gotchas (honesty invariants)
- **un-killable transport** — the stdio server survives everything short of EOF (issue #139). Framing is line-delimited: a malformed line is answered `-32700` and the loop RESYNCHRONIZES to the next newline (never `return`); a handler panic is contained by `recover()` → `cave_tool_panicked` (dispatch panics → `cave_internal_error`); JSON-RPC batch arrays are handled per spec (one array response); id-less/`"id":null` requests are notifications and get no reply; and both inbound lines and generated tool output (compress/toon) are size-capped (`cave_payload_too_large`, `maxInboundBytes`/`maxResultBytes`, 16 MiB default) — but `caveman_retrieve` is exempt (`Tool.ExemptResultCap`): recovery returns the byte-exact original and must never fail closed on size, since the shared gateway store has no matching ceiling. A dead server is worse than a slow one — the proxy keeps eliding content that no longer has a `caveman_retrieve` to expand it.
- **fail-open** — engine error or malformed input → byte-identical pass-through, never a protocol error.
- **fail-closed** — unknown tool/handle → `isError` + cave_snake_code; unknown JSON-RPC method → `-32601`.
- **zero-egress** — the adapter imports no `net`/`net/http`/`os/exec`; a test parses the source to enforce it.
- v1 is **stdio-only**, **string payloads only** (the engine detects type); HTTP transport + `caveman mcp` subcommand are v2.
- **protocol negotiation must never error.** The adapter implements the 2024-11-05 contract and echoes that version back; a client asking for a newer one gets 2024-11-05 in the initialize result and decides for itself, per the MCP lifecycle. It previously answered `-32602: unsupported protocol version`, which made Claude Code (and every other current client) drop the server — and because `caveman wrap` reads recovery availability from an install-time marker rather than from the live agent, the proxy kept eliding content that no longer had a `caveman_retrieve` to expand it. Declining to echo an unimplemented version is right; refusing to speak is not.

See ../../CLAUDE.md (root) · ../engine/CLAUDE.md
