# Caveman Engine

Local, recoverable context compression for AI agents.

Agents keep sending conversation history and tool results back to model. Large
logs, JSON, tables, code listings, diffs, and terminal output can be reread many
times during one task. Caveman Engine detects those shapes and uses a matching
compressor before next model call.

Compression changes what model sees, so Engine stores original bytes locally
before emitting lossy result. Agent can retrieve exact original later. If input
cannot be parsed, recovery storage is unavailable, or result is not smaller in
tokens, Engine returns original unchanged and claims nothing.

Engine runs locally without Caveman account. Token reductions are local
`o200k_base` estimates labeled `inferred`; they are not provider bills or verified
savings.

## Use it with existing agent

End users install thin CLI, then launch supported agent through local runtime:

```bash
npm i -g @caveman-ai/cli
caveman claude
```

Claude Code, Codex, Gemini, Aider, Hermes, OpenClaw, and opencode have registered
profiles. Exact behavior depends on agent protocol and available recovery path.
When safe compression path is unavailable, run narrows transforms or launches
direct with explicit warning.

## How it works

```text
agent context
    ↓
detect content type
    ↓
matching compressor
    ↓
store original locally before lossy replacement
    ↓
smaller context + recovery handle
```

`Compress`, `Retrieve`, `Detect`, and `Stats` form stable core API used by proxy,
CLI, SDKs, MCP server, and WASM build. Default registry has 14 compressors,
including JSON, logs, code, diffs, search results, HTML, tables, config, terminal
output, tool schemas, TOON, accessibility trees, and repetition.

`record` mode never transforms. Unknown modes fail closed to `record`. Unknown
graders fail with `passed: false`.

## Engine CLI

```text
caveman-engine compress < input
caveman-engine detect < input
caveman-engine retrieve <handle>
caveman-engine stats
caveman-engine registry
caveman-engine toon encode|decode
caveman-engine pixel render|simulate
caveman-engine evals run [--fixtures DIR]
```

Commands reading stdin enforce a 64 MiB input limit and fail with
`cave_input_too_large` above it. Caller-supplied eval fixtures are confined to
`DIR`; traversal and escaping symlinks fail closed.

Persistent CCR storage retains at most 512 MiB of payloads by default. Set
`CAVEMAN_CCR_MAX_BYTES` to a positive byte count before startup to choose a
different cap. At cap, new lossy transforms fail closed to original-byte
pass-through; existing handles are never evicted and remain recoverable.

Engine source ships under BSL 1.1. It is source-available, not OSI Open Source
before Change Date 2030-06-21; first-party self-hosted production is permitted.
Agent SDK, thin CLI, contracts, evals, and other adoption surfaces are MIT.

Build/test inside this repository:
`make product-build PRODUCT=engine` / `make product-test PRODUCT=engine`.

Registry id: `engine`.
