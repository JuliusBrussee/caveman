# @caveman-ai/pi

Native [Caveman](https://getcaveman.dev) extension for the [Pi coding agent](https://pi.dev)
and [Oh My Pi (OMP)](https://omp.sh).

One extension, four jobs:

- **Proxy routing** — points the selected model's provider at your local Caveman
  proxy (`/w/pi/...`) by updating that model's endpoint and preserving the SDK's
  original URL-dependent compatibility settings. The host keeps owning auth, model
  names, pricing, and `models.json`; the provider registry is unchanged. The
  extension routes only when the running proxy identifies the same destination:
  scheme, host, port, path, and API version must match. Custom providers need a
  published `compat.<provider>.base_url` mount. The proxy's live identity must
  also match its run-state file. Unknown endpoints and older proxies without
  this proof stay direct with a notice. The host keeps its own provider identity,
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
- **Native lifecycle** — bridges Pi or OMP session/turn/tool events into the Caveman
  native runtime (Core injection, per-turn context, tool-output shrinking).
  Successful `read` and `bash` outputs are eligible for shrinking; errors,
  recovery results, mutations, and other tools keep their original output.
- **Honest fallback** — routing activates only after the recovery gate holds
  (proxy alive, recovery contract matched, MCP child initialized). Anything else
  is a visible pass-through: direct provider, one notice, no savings claims.
  OAuth/subscription-authenticated models are never routed.

## Install

### Pi

Through the Caveman CLI (recommended — journaled, reversible):

```bash
caveman wrap pi      # this session only
caveman enable pi    # persistent; plain `pi` stays routed until `caveman disable pi`
```

Or as a plain Pi package:

```bash
pi install npm:@caveman-ai/pi
```

### Oh My Pi

```bash
omp plugin install @caveman-ai/pi
```

OMP loads the package's native `omp.extensions` entry (`dist/omp.mjs`), not the
Pi compatibility entry. Install only one Caveman extension in a host; remove
any earlier local Caveman adapter before enabling this package.

Switching sessions, navigating the session tree, and branching reset Core and
pending context, restore direct provider settings, and start a fresh recovery
client. Shutdown also removes provider overrides and closes recovery. Late
results from a previous session cannot replace the current session's context.
OMP system-prompt blocks are preserved without flattening them into a string.
OMP does not expose a model-selection extension event, so provider eligibility
is rechecked at the start of each user run, not on each provider request.

The native entry shares Caveman's existing `native-hook pi` protocol and
`/w/pi` gateway routes. It does not add a `caveman wrap omp` installer path.

Requires the Caveman CLI (`npm i -g @caveman-ai/cli`) plus the local
`caveman-proxy` / `caveman-mcp` binaries (`caveman setup`). Without them the
extension loads, says so once, and stays out of the way.

Pi is pinned against `@earendil-works/pi-coding-agent` 0.84.2. The OMP entry
targets the native `@oh-my-pi/pi-coding-agent` API, including array-valued
`before_agent_start.systemPrompt`. API reference: OMP integration commit
`6aef0e8ad51b3bc5ea7a5f2a255c3d48e4c5af72` (reports version 18.1.17).
This is not a claim that every published 18.1.17 build includes that commit.

## Development

```bash
npm install
npm test
npm run test:omp       # requires Bun and the native OMP development dependency
npm pack --dry-run
```

`npm test` builds both entries and runs the shared-runtime and Pi regressions.
The OMP regression loads the manifest's built entry and exercises native prompt
blocks, navigation, provider restoration, and real disposable MCP children.
Its hook, gateway, recovery binary, and home directories are isolated fixtures.
The published package contains the two runtime bundles, this README, and the
MIT license; test bundles are not shipped. It does not bundle skills or Caveman
Engine binaries. Engine/proxy/MCP binaries retain their separate BSL-1.1 license.
