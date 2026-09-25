# @caveman-ai/pi

Native [Caveman](https://getcaveman.dev) extension for the [Pi coding agent](https://pi.dev).

One extension, four jobs:

- **Proxy routing** — points the selected model's provider at your local Caveman
  proxy (`/w/pi/...`) by updating that model's endpoint and preserving the SDK's
  original URL-dependent compatibility settings. Pi keeps owning auth, model
  names, pricing, and `models.json`; the provider registry is unchanged. The
  extension routes only when the running proxy identifies the same destination:
  scheme, host, port, path, and API version must match. Custom providers need a
  published `compat.<provider>.base_url` mount. The proxy's live identity must
  also match its run-state file. Unknown endpoints and older proxies without
  this proof stay direct with a notice. Pi keeps its own provider identity,
  model catalog, auth registration, and custom stream handlers.
  OpenAI Chat Completions endpoints containing `api.openai.com` stay direct:
  Pi derives their prompt cache key from the URL and exposes no reliable
  override for routed requests. OpenAI Responses routing remains supported.
  Pi also derives attribution/session headers from some URLs. OpenRouter,
  NVIDIA, Cloudflare, and OpenCode aliases stay direct when their original
  headers cannot be preserved; canonical provider IDs remain routable. An alias
  can route when explicit headers make the result identical, or when
  `PI_TELEMETRY=0` disables the affected attribution (OpenCode session headers
  still require preservation). The extension does not reload or guess Pi's
  private active telemetry preference.

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
