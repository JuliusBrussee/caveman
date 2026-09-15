# Local tools

Caveman ships local tools for recovery, memory, browser context, and large
command output. They work without a Caveman account.

## MCP server

Register recovery server with detected agents:

```bash
caveman tools mcp install
```

Run `caveman tools mcp uninstall` to remove registration. Direct server binary,
`caveman-mcp`, communicates over standard input and output.

It exposes five tool classes:

| Tool | Purpose |
|---|---|
| Compress | Reduce eligible context and return recovery reference |
| Retrieve | Fetch exact source for a recovery handle |
| Stats | Read local compression statistics |
| TOON encode | Encode supported structured input |
| TOON decode | Decode and validate TOON input |

MCP transport is local process I/O, and host agent decides when tools can be
called. Configure its tool permissions as narrowly as possible.

## Cavemem

Cavemem stores durable project facts in SQLite and retrieves them with local
BM25 text ranking.

Main operations:

```bash
caveman tools mem remember "fact"
caveman tools mem recall "query"
caveman tools mem supersede <id> "replacement"
caveman tools mem history <id>
caveman tools mem forget <id>
```

Recall returns current facts by default and has a default inferred-token budget
of 2,000. Superseded history remains available for inspection. Recalled compact
records can include recovery references to exact stored source.

Memory is local persistence, not model training. A remembered statement can be
wrong or stale; source and supersession metadata help callers judge it.

Build standalone binary:

```bash
go build -o cavemem ./mem/cmd/cavemem
```

## Browser bridge

Browser bridge attaches to Chrome through Chrome DevTools Protocol. It captures
an accessibility-tree representation, filters it by query, compresses it, and
returns actionable element references.

```bash
caveman tools browse https://example.com "pricing"
caveman tools browse act '<reference>' click
caveman tools browse eval 'document.title'
caveman tools browse recover '<handle>'
caveman tools browse close
```

Snapshots avoid screenshots when semantic structure is enough. Recovery keeps
exact captured source available. Browser actions and JavaScript evaluation can
change page or account state; host permission policy should distinguish read
and write operations.

Build bridge:

```bash
go build ./browse/cmd/caveman-browse
```

## Output shrinker

Output shrinker parses large command and tool results, keeps high-signal
structure, and stores full source in CCR. It is useful for compiler logs, test
output, diffs, search results, and terminal transcripts.

```bash
some-command | caveman tools shrink
caveman tools shrink -- go test ./...
```

Structural shrink input is capped at 32 MiB. CLI command capture uses a separate
bounded limit, 8 MiB by default. Oversized input is rejected or handled by
caller policy; it is not silently treated as complete.

Shrinker must preserve error class, exit status, relevant paths, and recovery
handle. A short summary without those fields is not a safe replacement for
debugging output.

## Tool-catalog shrinker

`caveman-shrink` specializes in MCP and OpenAI-style tool catalogs. It keeps
tool names, parameters, enums, required fields, defaults, constants, and `$ref`
targets while removing annotations and shortening long descriptions. Description
reduction remains lossy and can change tool selection.

```bash
cat tools.json | caveman-shrink > tools.min.json
caveman-shrink lint tools.json
caveman-shrink recover <handle> > tools.original.json
```

Input cap is 32 MiB. Malformed or non-winning input passes through. Exact source
must be committed to CCR before compact catalog and handle are emitted.

## Local storage

Local proxy and tools commonly use:

```text
~/.caveman/caveman.db
~/.caveman/ccr.db
~/.caveman/mem/mem.db
~/.caveman/mem/ccr.db
~/.caveman/caveman.yaml
~/.caveman-cloud/config.json
```

Project overlays use `./.caveman/config.json`. Back up or delete these files only
with awareness that recovery handles and learned observations may stop resolving,
along with local memory.
