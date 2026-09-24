# Middleware conformance kit

A black-box check of any Caveman middleware runtime against the protocol in
[`docs/technical/middleware-protocol.md`](../../../../docs/technical/middleware-protocol.md).
It speaks only HTTP, so it tests `caveman-proxy`, a Postgres-backed replica set, or
another implementation the same way.

```sh
pnpm install --filter @caveman-ai/contracts   # ajv is its only dependency
node packages/shared/contracts/conformance/run.mjs http://127.0.0.1:8787 \
  --token "$CAVEMAN_AUTH_TOKEN" [--namespace N] [--foreign-token T2] [--legacy-only]
```

Output is TAP. Exit 0 means conformant, 1 a failed check, 2 bad usage, 3 ajv missing.

| Flag | Meaning |
|---|---|
| `--token` | Runtime credential sent as `Authorization: Bearer`. Without it the runtime is assumed open and the `unauthorized` checks skip. |
| `--namespace` | Namespace for every scope the kit creates (default `conformance`). Session ids are random per run. |
| `--foreign-token` | A second principal that may **not** use `--namespace`. Enables the cross-namespace `403 forbidden_namespace` checks, and proves a refused delete left the owner's data intact. |
| `--legacy-only` | Behave as a protocol 1.0 client only (never send `Caveman-Middleware-Features`) and hold the runtime to 1.0 obligations: `Retry-After` is not required and the stalled-body check skips, because 1.0 never delivered that answer. Use it against a 1.0 runtime such as `bin-v1.1.8`. |

## What it checks

Every response must be JSON, match its contract schema (route schema on 200,
`middleware-error` otherwise), carry `Cache-Control: no-store` and
`X-Content-Type-Options: nosniff`, and send `Retry-After` on each 429/503.

Run once per view (the 1.0 view without the features header, the 1.1 view with it):

- capabilities in both views (legacy filtering, `protocol`, all four features);
- optimize → retrieve round trip, with the original verified by sha256;
- paging, excerpt queries, `invalid_range` past the end;
- receipts and their fixed response;
- `sessions/delete` → `originals_deleted: true` and counts → `410 deleted`;
- each `server_error_codes` / `legacy_conditions` entry a client can trigger
  from outside, at the status its view requires: malformed and incomplete
  bodies, unknown fields (tolerated with the header, rejected without),
  `unsupported_version`, `unknown_capability` and the stale-revision rule,
  `not_found`, the request and receipt payload limits, `forbidden_origin`,
  `unauthorized`, a malformed recovery binding, `not_smaller`,
  `epoch_changed`, and a stalled request body;
- `quota_exceeded` + `Retry-After`, when capabilities advertise
  `quota_requests_per_minute` (≤ 2000). This runs last because it spends the
  principal's quota for the rest of the minute.

Entries that need an internal fault, a clock, or a race (`capacity`,
`deadline`, `expired`, `identity_conflict`, storage failures) are listed as
`# SKIP` with the reason, so the report always accounts for the whole catalog.

## Versioning

`KIT_VERSION` in `run.mjs` is `<protocol major>.<protocol minor>.<kit revision>`.
Expected statuses and codes come from
`packages/sdk/parity/middleware-v1_1.fixtures.json`, so the kit and the SDK
vectors cannot drift apart. A new protocol minor bumps the kit to match.
