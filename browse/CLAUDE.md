# browse — `caveman-browse` browser driver

Local, agent-facing browser interaction for the open Caveman surface. It serves
stdio MCP tools that read a real Chrome accessibility tree, compress it with the
engine's forced-only `a11y` compressor, act on `uid` handles, and recover the
byte-exact original AX payload through CCR. Every saving here is `inferred`;
Browse never emits `verified`.

## Layout
- `session.go` — MCP tool handlers, engine/CCR integration, UID target cache.
- `cdp.go` — chromedp-backed Mode-A dedicated Chrome driver and bounded
  actionability recipe.
- `cmd/caveman-browse/` — stdio MCP binary.

## Gotchas
- This package may import CDP/network/browser dependencies. `public/mcp` may not.
- `go test ./public/browse/...` is setup-free. `make test-browse` resolves an
  installed Playwright Chromium or system Chrome and runs the `integration`
  build-tagged CDP contract across the package and direct CLI; `make
  test-browser` adds extension tests and `make test-e2e` includes both.
- The actionability layer is intentionally bounded: same-origin dashboards and
  predictable design-system controls, not arbitrary-open-web parity.
- Unknown handles/actions fail closed with `cave_snake_code` errors.
- Snapshot output is compact indented text, not JSON-lines. `query` keeps at most
  12 highest-scoring task matches plus ancestors; recovery metadata exposes only
  UIDs actually shown. Keep `tokens_after` equal to exact agent-visible result
  cost and `view_tokens` equal to serializer output cost.
- Direct CLI Chrome is detached so separate `snapshot`/`act`/`eval` processes
  can share a target. `close` must terminate it. Fresh `CAVEMAN_HOME` must work;
  state writes stay atomic and mode `0600`.
- CDP action acknowledgement is not application settlement. Non-wait actions
  return `settled:false` and require a focused resnapshot for proof.
- Navigation denies `file:`, `javascript:`, and privileged Chrome schemes.
- Benchmark contract and reproducible Playwright comparison live in
  `BENCHMARK.md`; keep token budgets executable in tests.
- **The uid map is `browser_snapshot`'s contract, not a side effect of a
  compression ratio.** On engine pass-through (no recovery handle — a tree that
  did not get smaller, or no CCR store) `snapshotTool` fails closed with
  `cave_browser_snapshot_uncompressed` and returns the prior page's uids intact;
  it MUST NEVER dump the raw AX tree into `uids` (strictly worse than not using
  Browse) nor wipe the target cache. Regressing this reopens issue #140.
- **An `<iframe>` is a leaf, not a broken tree.** `Accessibility.getFullAXTree`
  returns one frame at a time, so an iframe node's `childId` points at a child
  document absent from this payload; the `a11y` compressor
  (`engine/compressors/axtree.go`) treats an unresolvable `childId` as a leaf and
  still curates the frame-visible nodes. Because of that the CDP driver keeps
  Chrome's default Site Isolation (`site-per-process`) — do not disable it to
  "fix" cross-origin iframes.
