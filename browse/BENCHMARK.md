# Caveman Browse efficiency benchmark

Measured 2026-08-10 with Google Chrome 151.0.7922.108, locked
`@playwright/test` 1.56.1, and Caveman's offline `o200k_base` counter. Every
number is an `inferred` token count for one snapshot, not provider usage or
billing.

## Results

Five independent Chrome runs; table reports median and `[min–max]`. Random CDP
node ids and CCR handles explain the small Caveman ranges. Playwright was stable
across all five runs.

### Large operations table

| Representation | Tokens | Versus raw AX | Versus Playwright |
|---|---:|---:|---:|
| Raw `Accessibility.getFullAXTree` JSON | 398,494 `[398,493–398,497]` | n/a | n/a |
| Playwright `locator("body").ariaSnapshot()` | 15,704 | 96.06% less | n/a |
| Caveman full agent-visible result | 13,368 `[13,367–13,368]` | 96.65% less | 14.88% less |
| Caveman focused result, query `ORD-0173` | 121 `[121–122]` | 99.97% less | 99.23% less / 129.8× smaller |

Corpus: [`testdata/order_dashboard.html`](testdata/order_dashboard.html), a
200-row operations table with one requested order action. Caveman's full result
contains compact AX text, UIDs, CCR handle, exact agent-visible token count, and
honesty metadata. Playwright baseline is only its ARIA text: no MCP envelope,
action refs, recovery handle, or accounting. That asymmetry favors Playwright.

### Small checkout form

| Representation | Tokens | Versus raw AX | Versus Playwright |
|---|---:|---:|---:|
| Raw `Accessibility.getFullAXTree` JSON | 4,186 `[4,183–4,188]` | n/a | n/a |
| Playwright `locator("body").ariaSnapshot()` | 67 | 98.40% less | n/a |
| Caveman full agent-visible result | 157 `[156–159]` | 96.25% less | 2.34× larger |
| Caveman focused result, query `Email Plan Save order` | 111 `[110–113]` | 97.35% less | 1.66× larger |

This small-page loss is important: Caveman's recovery handle, exact counters,
honesty basis, and action UIDs cost more than bare Playwright ARIA text when the
page itself is tiny. It still saves 97.35% versus raw AX and carries enough
state to type, select, click, verify, and recover bytes. No universal
snapshot-only win is claimed.

Smaller captured fixture also locks serializer regression:

- prior Caveman JSON-lines view: 380 tokens;
- compact indented view: 58 tokens (84.7% less than prior view);
- exact delivered payload including CCR/accounting: 126 tokens;
- raw AX: 5,351 tokens;
- four-tool MCP catalog: 287 tokens.

## Reproduce

Run Caveman live-Chrome benchmark and functional loop:

```bash
CAVEMAN_BROWSE_CHROME="/path/to/Chrome" \
  go test -tags=integration -run 'TestCDPQueryScales|TestCDPFullTokenEfficient' -count=5 -v ./browse
```

Count locked Playwright ARIA baseline with same tokenizer:

```bash
CAVEMAN_BROWSE_CHROME="/path/to/Chrome" \
  node browse/scripts/playwright-aria-baseline.mjs |
  CAVEMAN_CCR_DB=/tmp/caveman-browse-bench.db \
  go run ./engine/cmd/caveman-engine compress --type no-such-type >/dev/null
```

Pass `agent_checkout.html` after the baseline script to reproduce the small-form
row. Four-tool MCP catalog cost is separately locked to 287 tokens.

Integration gates also prove type, select, offscreen auto-scroll click,
post-action focused verification, disabled-control rejection, stale-UID
rejection, byte-exact live recovery, fresh-home startup, cross-process direct
CLI reattachment, and explicit Chrome shutdown.

## Claim boundary

These results apply to this corpus and toolchain. Phase 1 covers same-origin,
predictable controls; OOPIFs, dialogs, downloads, and arbitrary-site
actionability remain deferred. Query-focused progressive disclosure is default
for large pages, with full snapshots available when task intent is unknown.
