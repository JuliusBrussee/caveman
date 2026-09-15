# Local stats and savings accounting

`caveman stats` reports the requests routed through this machine's Caveman
proxy. It prints a terminal summary and writes an offline dashboard. In an
interactive terminal the dashboard opens automatically; `--plain` keeps the
command in the terminal. `--json` produces the complete report without opening
a browser or writing HTML.

```sh
caveman stats
caveman stats --days 7
caveman stats --all-time --provider anthropic
caveman stats --model gpt-5.6 --auth subscription
caveman stats --json
caveman stats --out ./savings.html --open
```

The default window is 30 UTC calendar days. Provider, model, agent and auth
filters use the exact values captured on each request. A current model
selection never changes the price or identity of older requests. The report
does not scan prompts from other agents or assume their traffic used Caveman.

## What a token delta means

For an eligible request, the proxy normalizes original and final request JSON
and counts both with the same offline tokenizer. Key order and equivalent
escaping cannot manufacture a reduction. The difference includes replacement text,
recovery markers and request overhead. Negative differences remain negative.
Original-request fallbacks earn no compression savings. Failed and incomplete
requests do not earn savings merely because a compressor produced output.

These are local estimates, not exact provider token counts. The provider can
use a different tokenizer, add hidden framing, or bill image and audio content
separately. The report keeps provider-reported usage apart from the local
before/after pair. Multimodal requests, payloads over 4 MiB, and unsupported comparisons remain
unavailable instead of counting base64 image bytes as text tokens.

The baseline is the request at the proxy boundary. It answers what this
request would contain without the proxy's transforms. It does not reconstruct
an entire alternate agent run. Native masking or MCP compression performed
before that boundary, extra agent turns, recovery behavior, output verbosity,
and task quality need their own paired evidence. A file is only represented
when its read output reaches this boundary; a file's size on disk alone earns
no savings. Repeated requests are repeated observations, not unique files.

## Three separate money questions

* **API spend:** provider-counted billing buckets at the captured published
  rates. Complete reported usage remains eligible when a stream later fails;
  that failure still earns no counterfactual savings. It is a catalog estimate,
  not an invoice reconciliation.
* **Estimated API input reduction:** the signed input delta valued using the
  request's observed mix of uncached input, cache reads and cache writes. The
  original request's cache behavior is unknown, so this remains an estimate.
* **Subscription API equivalent:** the same arithmetic for subscription or
  non-billable OAuth traffic. It illustrates the API value of that context;
  it does not reduce a subscription bill or prove additional quota.

OAuth is an authentication mechanism, not a billing plan. Paid cloud OAuth
routes such as Vertex stay in the API category when their billing provenance
supports it. Unknown billing modes stay separate. Unknown models never borrow
a sibling model's rate or a blended provider price.

Rates and catalog provenance are captured with new requests. Prices are not
recomputed from the model selected when the report is opened. Unsupported
prices and absent measurements are `null`/unavailable; a displayed subtotal
only covers the priced rows. Legacy compressed-segment measurements remain
separate from the stronger request comparison. The report never promotes
local evidence to `verified` savings or projects a window into a monthly claim.

## Research and design references

[Headroom's savings command](https://github.com/headroomlabs-ai/headroom/blob/main/docs/content/docs/savings.mdx)
uses a durable event ledger and groups savings by time, model and client.
[Its metrics documentation](https://headroomlabs-ai.github.io/headroom/metrics/)
also describes historical series and usage visibility. Those are useful
product patterns. Caveman keeps unpriced usage explicit rather than assigning
a blended rate to an unknown model.

Provider billing categories come from the published
[Anthropic pricing documentation](https://platform.claude.com/docs/en/about-claude/pricing),
[OpenAI prompt caching documentation](https://openai.com/index/api-prompt-caching/),
and [Gemini caching documentation](https://ai.google.dev/gemini-api/docs/caching).
The dated repository catalog remains the source of the actual captured rates.
The dashboard follows the typography, white page, subdued borders, and muted
green accents of `caveman learn`.
