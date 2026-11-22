# browse: caveman-browse browser driver

`browse/` is a consumer copy of the public
[`JuliusBrussee/caveman-browse`](https://github.com/JuliusBrussee/caveman-browse) repository.
Do Browse product work there. Edit this copy only for pinned integration with this repo or an
explicit sync. Accessibility-compressor work stays in this repo, in
`engine/compressors/axtree.go`, because the engine lives here.

What it is: a local stdio MCP server for agents. It reads a real Chrome accessibility tree,
compresses it with the engine's forced-only `a11y` compressor, acts on `uid` handles and recovers
the byte-exact original AX payload through CCR. Every saving is `inferred`, never `verified`.

## Layout
- `session.go`: MCP tool handlers (`browser_snapshot`, `browser_act`, `browser_eval`, `browser_recover`), engine and CCR integration, the uid target cache.
- `cdp.go`: chromedp-backed dedicated Chrome driver and the bounded actionability recipe.
- `cmd/caveman-browse/`: the stdio MCP binary and the direct CLI (`snapshot`, `act`, `eval`, `recover`, `close`).
- `bin/caveman-browse.mjs`, `package.json`: the npm launcher.
- `BENCHMARK.md`: the benchmark contract and the Playwright comparison.

## Build and test (from the repo root)
- `go build ./browse/cmd/caveman-browse` and `go test ./browse/...` (no browser needed).
- `CAVEMAN_BROWSE_CHROME=/path/to/Chrome go test -tags=integration ./browse/...` runs the CDP contract against a real Chrome.
- `bun run --filter caveman-browse test` runs the launcher tests.

## Invariants
- **The uid map is `browser_snapshot`'s contract.** On engine pass-through (no recovery handle, because the tree did not shrink or there is no store) the tool fails closed with `cave_browser_snapshot_uncompressed` and keeps the prior page's uids. It must never dump the raw AX tree into `uids` or wipe the target cache (issue #140).
- **An `<iframe>` is a leaf.** `Accessibility.getFullAXTree` returns one frame at a time, so an iframe's `childId` points at a document missing from the payload. The `a11y` compressor treats an unresolvable `childId` as a leaf. Keep Chrome's default Site Isolation (`site-per-process`); do not disable it to "fix" cross-origin iframes.
- **Network and browser dependencies stay here.** This package may import CDP and network code; `mcp/` must not.
- **Honest token accounting.** `tokens_after` equals the exact agent-visible result cost and `view_tokens` the serializer output cost. Snapshot output is compact indented text, not JSON lines. A `query` keeps at most 12 top matches plus ancestors, and recovery metadata lists only uids actually shown. Keep the token budgets in `BENCHMARK.md` executable as tests.
- **An action is not a settled page.** CDP acknowledgement is not application settlement: non-wait actions return `settled:false`, and only a focused re-snapshot proves state.
- **Bounded scope.** The actionability layer targets same-origin dashboards and predictable design-system controls, not parity with the open web.
- **Fail closed.** Unknown handles and actions return `cave_` error codes. Navigation allows `http(s)`, `about:blank` and bounded `data:text/html`, and rejects `file:`, `javascript:` and privileged Chrome schemes.
- **Direct CLI state.** Direct CLI Chrome is detached so separate `snapshot`, `act` and `eval` processes share one target; `close` must end it. A fresh `CAVEMAN_HOME` must work, and state writes stay atomic with mode `0600`.

See the root [AGENTS.md](../AGENTS.md).
