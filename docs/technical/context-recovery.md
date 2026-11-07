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

Recovery storage is bounded, with a default payload capacity of 512 MiB. The
SQLite page cap counts every page (indexes, overflow, slack), so the file can
fill before the payload does. A write the store refuses for capacity first
frees room and is tried once more:

1. Duplicate full copies of one repository map become references to a single
   copy whose bytes verify. No object is deleted.
2. Typed objects past the retention policy are deleted, oldest first, until the
   store is at 80% of its limits: archived objects of a session whose last write
   is 14+ days old, and every object of a session idle 30+ days. A row that a
   kept row reads through `data_ref` stays. Each expiry is logged, and the id
   answers "expired" (`cave_expired_recovery`) for 180 days rather than
   "unknown".

Blob recovery handles (`ccr_…`) are never expired, because an emitted request
may reference any of them. If nothing can be freed the write is refused and
Engine keeps original input. `caveman-proxy recovery-store status` reports a
full store and what the next write can expire.

## Integrity

A store damaged before an upgrade stays damaged across it. The proxy runs
`PRAGMA quick_check` once per release, on its own read-only connection, and a
damaged store (or SQLite reporting a malformed page mid-operation) refuses
every write: a write into a damaged b-tree can spread the damage. Reads go on
and are hash-checked. `caveman-proxy recovery-store check` runs the check on
demand; `caveman-proxy recovery-store repair` takes an exclusive lock (so it
refuses while any other process has the store open), copies every row whose
bytes still match its handle or content hash into a new file, moves the damaged
database and its journals aside as a backup, and lists every row it could not
carry over.

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

Never delete, copy over, rename, or replace an open recovery database or its
`-wal` / `-shm` sidecars. Use SQLite-aware backups, or stop all processes using
that store before moving its files. A WAL may contain committed data that has
not yet reached the main database; removing it can lose existing handles.

The native store checks database and journal file identities before and after
each operation. Normal writes and checkpoints preserve these identities. Any
observed removal or replacement is terminal for that Store, including a complete,
valid database replacement. Reads, writes, typed-object operations, and
`Store.Close` then return `recovery storage changed`. No replacement connection is
opened, and a change observed during a write suppresses its returned handle so the
Engine keeps the original input. Restoring the previous files does not revive an
invalidated Store.

Stop the affected process, restore a consistent database with its available
journal files if restoration is needed, and start a fresh process. It can read
existing handles and create new ones normally. The store cannot reconstruct
records from deleted journals. Do not delete more files to clear this error, or
restart a proxy shared by other users without coordinating that interruption.

The invalidated Store retains its one connection until process exit. SQLite close
can checkpoint an obsolete WAL into the current database; retaining the
connection avoids that cleanup write. The operating system releases its
descriptors and locks on exit. A failed disk-store initialization also retains
any connection it already opened, because a later path snapshot cannot establish
which journal files that connection holds. Fix the opening error and restart the
process before retrying. The store does not stop or restart a shared proxy
automatically.

This behavior addresses the orphaned-descriptor failure reported in
[issue #1008](https://github.com/JuliusBrussee/caveman/issues/1008): an existing
process must report a storage failure instead of acknowledging writes through
retired files. Recovery is explicit process restart, not transparent replacement
adoption. Tests use separate real writer/reader processes, verify that rejected
operations preserve replacement database/WAL/SHM bytes, and confirm retrieval
and new writes after restart.

These checks are not an atomic lock against external file replacement. The
public native driver does not expose the identity of its open database and
journal descriptors. In particular, successful initial opening cannot reliably
detect a journal replacement that occurs before its first complete file snapshot,
or replacement followed by restoration of the same file identities during open.
Do not move or replace files while a store is opening or executing SQLite calls;
stop all users first. Normal concurrent SQLite transactions and checkpoints are
supported. The supported guarantee is terminal failure after an observed
identity change, not safety under arbitrary concurrent filesystem manipulation.

The native driver exposes `SQLITE_FCNTL_PERSIST_WAL`, which keeps valid WAL/SHM
files after ordinary close, so their presence after exit is expected. It does
**not** suppress checkpointing and does not make an invalidated connection safe
to close. See
[SQLite's persistent WAL control](https://www.sqlite.org/c3ref/c_fcntl_begin_atomic_write.html#sqlitefcntlpersistwal)
and [SQLite's warnings about unlinking open databases and journals](https://www.sqlite.org/howtocorrupt.html).
