# Context recovery

Caveman Context Recovery, abbreviated CCR, stores exact original bytes behind a
short reference. Compression can then remove low-value context without making
source unavailable to a user or agent.

## Handles

Byte payloads use content-addressed handles beginning with `ccr_`. Handle body
comes from the first 16 bytes of a SHA-256 digest, encoded as hexadecimal.

```text
ccr_0123456789abcdef0123456789abcdef
```

Identical bytes produce the same handle. A handle is an identifier, not an
encryption mechanism and not an authorization token.

Retrieve source with:

```bash
caveman tools retrieve ccr_0123456789abcdef0123456789abcdef
```

MCP clients can call the recovery tool instead.

## Stored record

A recovery record includes:

- exact original bytes;
- content type and compressor metadata;
- original and compact size information;
- local timestamps and accounting fields needed by the store.

Compression output contains the handle and enough explanation for a tool-aware
agent to request source when needed.

## Typed objects

Structured tools can store typed objects under identifiers beginning with
`ccr_obj_`. References use `ccr://` pointers so a client can select a field or
subtree rather than fetching an unrelated payload.

Typed retrieval validates pointer shape and object type. Invalid selectors fail
instead of returning a guessed object.

## Storage backends

| Runtime | Default recovery store |
|---|---|
| Native Engine and proxy | SQLite-backed local store |
| WebAssembly | In-memory store |
| Tests and embedded callers | Caller-selected store |

Native recovery data normally lives in `~/.caveman/ccr.db`. Proxy measurements
use separate `~/.caveman/caveman.db`. A WebAssembly handle disappears when its
in-memory runtime is discarded unless its host persists record separately.

## Capacity behavior

Recovery storage is bounded, with a default payload capacity of 512 MiB. A new
record that exceeds available capacity is refused without evicting existing
handles, and Engine keeps original input.

This rule protects old compacted context from becoming a dangling reference.

## Security properties

CCR provides availability of exact source; it does not by itself provide:

- encryption at rest;
- remote identity or access control;
- secret redaction;
- permanent archival storage;
- proof that a caller is allowed to see a guessed handle.

Local proxy binds to loopback and assumes one trusted operator account. Protect
local databases like agent transcripts, and do not share handles across trust
boundaries without an authorization layer.

## Recovery and evidence

Recovery proves source remains available. It does not prove compressed context
had equal model quality, and it does not turn inferred token reduction into
verified monetary savings. Those require separate evaluation and evidence.

## Operational checks

When recovery fails:

1. confirm request uses same local runtime and store that created the handle;
2. confirm database file still exists and is readable;
3. confirm handle was copied in full;
4. check storage-capacity errors at compression time;
5. remember that in-memory WebAssembly handles do not survive runtime loss.

Never delete or replace a recovery database during an active agent session
unless losing existing handles is acceptable.
