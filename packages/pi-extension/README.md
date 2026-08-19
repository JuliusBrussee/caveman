# @caveman-ai/pi

Native [Caveman](https://getcaveman.dev) extension for the [Pi coding agent](https://pi.dev).

One extension, four jobs:

- **Proxy routing** — points the selected model's provider at your local Caveman
  proxy (`/w/pi/...`) with a baseUrl-only override. Pi keeps owning auth, model
  names, pricing, and `models.json`; nothing is copied or rewritten.
- **Exact recovery** — registers a single model-visible tool, `caveman_retrieve`,
  backed by the local `caveman-mcp` binary and the shared CCR store. Compressed
  bytes are always recoverable, byte-exact.
- **Native lifecycle** — bridges Pi session/turn/tool events into the Caveman
  native runtime (Core injection, per-turn context, tool-output shrinking).
- **Honest fallback** — routing activates only after the recovery gate holds
  (proxy alive, recovery contract matched, MCP child initialized). Anything else
  is a visible pass-through: direct provider, one notice, no savings claims.
  OAuth/subscription-authenticated models are never routed.

## Install

Through the Caveman CLI (recommended — journaled, reversible):

```bash
caveman wrap pi      # this session only
caveman enable pi    # persistent; plain `pi` stays routed until `caveman disable pi`
```

Or as a plain Pi package:

```bash
pi install npm:@caveman-ai/pi
```

Requires the Caveman CLI (`npm i -g @caveman-ai/cli`) plus the local
`caveman-proxy` / `caveman-mcp` binaries (`caveman setup`). Without them the
extension loads, says so once, and stays out of the way.

Pinned against `@earendil-works/pi-coding-agent` 0.84.2.
