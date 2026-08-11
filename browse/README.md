# caveman-browse

`caveman-browse` is a local browser-interaction MCP server. It attaches to
Chrome, reads `Accessibility.getFullAXTree`, compresses the AX payload through
the Caveman engine's forced-only `a11y` compressor, and exposes four tools:

- `browser_snapshot`
- `browser_act`
- `browser_eval`
- `browser_recover`

Savings are always `inferred`. Recovery is CCR-backed: `browser_recover` returns
the byte-exact original AX payload for a snapshot handle.

`browser_snapshot.query` is the token-efficient path on large pages. It keeps
best matching accessible nodes plus their ancestors while CCR retains the full
raw tree. Compact output uses `[uid] role "name"` lines; only actionable or
unknown custom roles spend UID tokens. `tokens_after` counts the exact
agent-visible JSON result; `view_tokens` isolates compact tree cost.

Build:

```bash
go build ./public/browse/cmd/caveman-browse
```

Run as MCP server:

```bash
caveman-browse
```

Direct CLI helper:

```bash
caveman-browse snapshot http://127.0.0.1:3000
caveman-browse snapshot http://127.0.0.1:3000 "save settings"
caveman-browse act <uid> click
caveman-browse recover <handle>
caveman-browse close
```

Direct commands share one detached, isolated Chrome until `close`; first use
creates both profile and CCR directories. Actions report `settled:false` after
CDP dispatch because application state is not proven until another focused
snapshot. Browser navigation allows `http(s)`, `about:blank`, and bounded
`data:text/html`; local files and privileged browser schemes fail closed.

Proof and comparison: [BENCHMARK.md](BENCHMARK.md). Large-page query focus wins
sharply; tiny-page results can exceed bare Playwright ARIA text because Caveman
also returns UIDs, recovery, and exact accounting.
