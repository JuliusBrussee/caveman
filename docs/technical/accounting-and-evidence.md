# Accounting and evidence

Caveman labels numbers by how they were produced. Token estimate and provider
usage record answer different questions from benchmark comparison or verified
savings record; they must not share one label.

## Evidence bases

| Basis | What it means | What it does not mean |
|---|---|---|
| `measured` | Directly counted by named local or provider mechanism | Automatically billable or causal |
| `inferred` | Estimated by local model, tokenizer, or assumption | Provider-confirmed usage |
| `provider_reported` | Returned by provider usage fields | Independent invoice reconciliation |
| `benchmark_counterfactual` | Difference between controlled fixture variants | Production savings |
| `observed` | Before/after correlation in live observations | Proof that change caused difference |
| `verified` | Meets a named enforced verification method | Universal quality or future savings |
| `unpriced` | No supported public price is known | Zero real-world cost |

UI and reports should show basis next to value. A value cannot silently change
basis while being aggregated.

## Token counts

Engine uses offline `o200k_base` counting where available and character estimate
fallback. Those counts are inferred for provider billing purposes. Providers
may tokenize same text differently and may count cache or image inputs under
separate units.

Provider-reported token fields keep provider basis. Do not replace them with a
local estimate when field is missing.

## Cost calculation

Provider catalog contains dated public list prices. Local cost display is:

```text
provider-reported or explicitly selected token units
× dated public catalog rate
= list-price subtotal
```

This is not an invoice. It can differ because of negotiated terms, subscriptions,
regional prices, batch rates, caching rules, credits, taxes, or provider billing
adjustments.

Unknown provider or model prices produce zero plus `unpriced`. Zero prevents
invented cost from entering totals; `unpriced` prevents zero from being mistaken
for free use.

## Inferred headroom

Local compression and learning reports can estimate avoidable context, reported
as headroom under stated assumptions. Keep its time basis unchanged: a per-day
estimate must not become a monthly claim unless method measures each day and
states that result is a projection.

## Benchmark evidence

A useful benchmark report states:

- exact fixtures and their hashes or versions;
- command and code revision;
- counter or provider usage source;
- baseline and variant definitions;
- quality or invariant checks;
- failures and excluded cases;
- scope of conclusion.

One fixture result supports only that fixture and method. Average reduction does
not prove equal task quality, and recovery checks establish source availability
rather than model comprehension.

## Verified values

Verified is reserved for methods whose prerequisites are enforced and whose
records identify method. Public local runtime should not mark Engine estimates,
skill output, pixel conversion, TOON output, cache plans, or merged code as
verified by themselves.

When evidence is incomplete, use inferred, observed, provider-reported, or
honest zero.

## Negative and failed results

Keep negative deltas and failed transformations. Dropping regressions biases a
result. A rejected optimization can still incur provider usage; account for that
usage even though compact output was not served.

## Publication checklist

Before publishing a number:

1. name exact evidence basis;
2. link committed fixture or source record;
3. disclose counter and pricing date;
4. state quality test and failure count;
5. distinguish list price from invoice;
6. avoid extrapolating across model, provider, task, or time;
7. publish zero or `unpriced` when support is absent.

See [`HONEST-NUMBERS.md`](../HONEST-NUMBERS.md) for currently supported public
claims.
