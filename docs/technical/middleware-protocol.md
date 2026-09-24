# Caveman middleware protocol 1.1

Status: normative. The key words MUST, MUST NOT, SHOULD, SHOULD NOT and MAY are
used as described in RFC 2119 and RFC 8174.

This document specifies the HTTP protocol between Caveman framework middleware
clients (the `@caveman-ai/sdk/middleware` and `caveman_cloud.middleware` SDKs
and the adapters built on them) and a Caveman middleware runtime
(`caveman-proxy`). Machine-readable artifacts:

| Artifact | Path |
|---|---|
| JSON Schemas | `packages/shared/contracts/schemas/middleware-*.schema.json` |
| OpenAPI 3.1 | `packages/shared/contracts/openapi/middleware.openapi.json` |
| Parity vectors (1.0) | `packages/sdk/parity/middleware.fixtures.json` |
| Parity vectors (1.1) | `packages/sdk/parity/middleware-v1_1.fixtures.json` |

Where this prose and a fixture disagree, the fixture is the bug report: fix one
of them in the same change.

**Versions.** Protocol 1.0 is what shipped in runtime `bin-v1.1.8`, SDK 1.1.0
(TS and Python) and middleware `0.1.0-alpha.2` / `0.1.0a1`. Protocol 1.1 is
additive: `schema_version` stays `1` on every body, and 1.1 behavior is
negotiated per request (§3).

## 1. Routes and transport basics

A client is configured with a base URL `B`: `scheme://host[:port][/prefix]`.
Every route URL is `B` + `/caveman/v1/middleware/` + route. A prefix is
preserved (§15).

| Route | Method | Request schema | 200 body schema |
|---|---|---|---|
| `capabilities` | GET | none | `middleware-capabilities` |
| `optimize` | POST | `middleware-optimize` | `middleware-plan` |
| `retrieve` | POST | `middleware-retrieve` | `middleware-page` |
| `receipts` | POST | `middleware-receipt` | `middleware-receipt-response` |
| `sessions/delete` | POST | `middleware-session-delete` | `middleware-session-delete-response` |

The receipt route is `receipts` (plural) on the wire.

- POST bodies MUST be a single JSON object sent as `Content-Type:
  application/json` in UTF-8. The server answers any other content type with
  400 `invalid_request`.
- Every response body is JSON. Servers MUST send `Cache-Control: no-store` and
  `X-Content-Type-Options: nosniff`.
- Unknown routes, and methods other than those listed, get 404 `not_found`.
- Clients MUST NOT follow redirects (outcome `redirect_refused`) and MUST stop
  reading a response body after 4 MiB (outcome `payload_limit`).

## 2. Authentication and scope model

**Browser refusal.** A request carrying an `Origin` header, or
`Sec-Fetch-Site: cross-site`, MUST get 403 `forbidden_origin` before
authentication runs.

**Authentication.** Every route, `capabilities` included, MUST authenticate.
The credential is `Authorization: Bearer <credential>` (a static token or an
OIDC JWT) or a TLS client certificate. It is a runtime credential. Clients MUST
NOT send a model provider API key. A missing or unknown credential gets 401
`unauthorized`, and servers SHOULD add `WWW-Authenticate: Bearer`. The server
resolves a **principal** from the credential only, never from request JSON. The
legacy single `CAVEMAN_AUTH_TOKEN` maps to one principal, `single_operator`,
which may use every namespace.

**Namespace authorization.** If the principal has an allowed-namespace list and
`scope.namespace` is not in it, the server MUST answer 403
`forbidden_namespace`.

**Scope.** `scope = {namespace, session_id, branch_id, cache_epoch}`. Each
member is a token matching `^[A-Za-z0-9._:/-]{1,256}$`. Define
`H(p1, …, pn)` as the lowercase hex SHA-256 of the UTF-8 JSON array
`[p1,…,pn]`. Then:

- `authority = H(principal, namespace, session_id, branch_id, cache_epoch)`.
  Grants, handles, choices and originals belong to exactly one authority. A
  request can only reach what its own authority owns. An unknown handle, or one
  owned by another authority, gets 404 `not_found`, so the response never
  reveals whether a foreign handle exists.
- `scope_id = H(authority, adapter.id, adapter.serialization_revision)`.
  `scope_id` MUST NOT include the policy revision or the requested transform
  list (§5). Persisted choices therefore survive runtime upgrades and keep
  replacement bytes stable. Protocol 1.0 derived `scope_id` from both, so the
  first 1.1 runtime deployment resets persisted choices once: each live session
  sees one provider-cache miss.
- A persisted choice for `(scope_id, segment.id, segment.source_id,
  segment.sha256)` MUST be reused byte for byte when its `transform_id` is in
  `request.policy.transforms`. Otherwise the segment is skipped with
  `unknown_capability`.
- Recovery handles match `^cmw_[a-f0-9]{48}$` (24 random bytes).

## 3. Negotiation (K2)

**Server → client.** Capabilities MAY carry `protocol: {min, max}`, the
inclusive range of `schema_version` majors the server accepts (1.1: `{1, 1}`),
and `features: [feature]`. A document with no `features` array is a
**protocol 1.0 runtime**. Against such a runtime a client MUST send exact 1.0
shapes only.

**Client → server.** Clients MUST send `Caveman-Middleware-Features` on every
request, `capabilities` included. The value is a comma-separated list of
feature tokens. The 1.1 SDKs send exactly `http_status_v2, revision_tolerant`.
Servers split on `,`, trim optional whitespace, and ignore empty, malformed
(`^[a-z][a-z0-9_]{0,63}$`) and unknown tokens. A request without the header is a
**protocol 1.0 client**.

**Rule.** A server applies the 1.1 semantics of feature F to a request only
when F is in both the request header and the server's own `features`.
Otherwise it MUST behave exactly as protocol 1.0 for that aspect.

| Feature | Direction | Meaning when active |
|---|---|---|
| `tolerant_reader` | server advertises | Server ignores unknown request fields (§4). Future optional request fields are sent only to servers advertising it. |
| `revision_tolerant` | both | Revision acceptance per §5. |
| `http_status_v2` | both | Status mapping per §6 (decisions are 200; 408/429/503 + `Retry-After`). |
| `originals_lifecycle` | server advertises | Delete and retention cover originals, and `max_retention_seconds` is enforced (§12). |

A 1.1 server advertises all four and `protocol: {min: 1, max: 1}`.

**Legacy view of capabilities.** A capabilities request without the features
header MUST get a document that SDK 1.1.0 accepts. Such a document:

- omits every transform whose `recovery` is not `exact_ccr` or `none`, or whose
  `deterministic` is not `true`;
- has only positive safe integers in `limits`;
- carries a `policy_revision` computed over exactly the transforms it lists.

Additive top-level fields (`protocol`, `features`, `max_retention_seconds`) MAY
appear in both views, because 1.0 clients ignore unknown top-level fields.

## 4. Tolerant readers (K1)

**Servers**

- MUST ignore unknown request body fields at any depth and unknown headers.
- MUST still require every required field and validate the known ones.
- MUST NOT give an unknown field any meaning. Anything security-relevant is a
  negotiated feature.
- Runtime `bin-v1.1.8` rejects unknown fields (`DisallowUnknownFields`), which
  is why clients gate new body fields on `tolerant_reader`. Protocol 1.1 adds
  no request body fields.

**Clients** MUST ignore unknown response fields at any depth. They MUST NOT
reject a whole capabilities document over one entry. Parsing capabilities:

1. Reject the document with `unsupported_version` if any of these fails:
   - it is a JSON object;
   - `schema_version == 1`;
   - `protocol`, when present and well-formed, satisfies `min ≤ 1 ≤ max`;
   - `policy_revision` is a token;
   - `runtime_build` is a string;
   - `transforms` is an array;
   - `limits` has `deadline_ms`, `request_bytes`, `segment_bytes` and
     `page_bytes` as positive safe integers;
   - `persistent` and `recovery` are booleans;
   - `retention_seconds` is a safe integer ≥ 0.
2. Treat a malformed `protocol`, `features` or `max_retention_seconds` as
   absent. A non-array `features` therefore means a legacy runtime.
3. `features` = server features ∩ features this client knows, sorted. Ignore
   unknown and malformed entries.
4. Usable transforms: keep only well-formed entries (token `transform_id` and
   `implementation_version`, array `eligible_segment_kinds`) with
   `deterministic === true` and `recovery` in the client allowlist
   (`exact_ccr`). Keep the first entry for each id. An empty result is valid,
   and `optimize` then bypasses locally with `unknown_capability`.
5. `limits`: a known 1.1 key whose value is not a positive safe integer is
   treated as absent. Unknown keys are ignored.
6. An unrecognized `mode` is treated as `record`. `trust_mode` is informational.

Vectors: `capabilities_parsing`.

## 5. Policy revision (K3)

`policy_revision` is an opaque token identifying the transform set of one
capabilities view.

- **Acceptance with `revision_tolerant` active:** accept iff every id in
  `policy.transforms` is unique and currently advertised to this request's
  view. `policy.revision` is ignored.
- **Acceptance otherwise (1.0):** accept iff `policy.revision` equals the
  current revision of the legacy view and every transform is supported.
- A rejection is 400 `unknown_capability`.
- The plan's `policy_revision` MUST be the server's current revision for the
  request's view.

**Client**

- MUST NOT reject a replacement because `transform_version` differs from the
  cached `implementation_version`. Persisted choices keep the version that
  produced them. The client still requires `transform_version` to be a token.
- When `plan.policy_revision` differs from the cached revision, the client
  validates the plan as usual (§7), applies it if valid, and starts a
  **single-flight** capabilities refresh:
  - at most one refresh is in flight per runtime instance;
  - concurrent calls keep the cached document until the refresh resolves;
  - a failed refresh clears the cache.
- Cached capabilities expire after 300 seconds. A client whose cached view has
  no transform it can use also refreshes (single-flight) on the next call,
  instead of waiting for a restart or an error.

## 6. Errors and HTTP semantics (K4)

Every non-2xx body is the error envelope (`middleware-error`):
`{"schema_version": 1, "error": {"code": "<code>"}}`, where the code matches
`^[a-z][a-z0-9_]{0,63}$`. Servers MUST send `Retry-After` (whole seconds, ≥ 1)
on every 429 and 503, in both modes.

| Code | 1.1 status | 1.0 status | Retryable | Meaning |
|---|---|---|---|---|
| `invalid_request` | 400 | 400 | no | Malformed or missing required input |
| `unsupported_version` | 400 | 400 | no | `schema_version` outside `protocol` |
| `unknown_capability` | 400 | 400 | no | Transform not supported / revision rejected (§5) |
| `invalid_range` | 400 | 400 | no | Retrieve offset/limit out of range |
| `unauthorized` | 401 | 401 | no | Missing or unknown credential |
| `forbidden_origin` | 403 | 403 | no | Browser request |
| `forbidden_namespace` | 403 | (new) | no | Principal may not use namespace |
| `not_found` | 404 | 404 | no | Unknown or foreign handle; unknown route. Storage failures are never 404 |
| `request_timeout` | 408 | (new) | yes | Request body not received before the deadline |
| `epoch_changed` | 409 | 409 | no | Manifest is not an extension of the stored one, or sequence went backwards |
| `identity_conflict` | 409 | 409 | yes | Concurrent writer conflict |
| `deleted` | 410 | 410 | no | Scope revoked |
| `expired` | 410 | 410 | no | Scope passed retention or max retention |
| `payload_limit` | 413 | 413 | no | Body exceeds a size limit (actual size only) |
| `capacity` | 429 | 503 | yes | Queue saturated |
| `quota_exceeded` | 429 | (new) | yes | Per-principal quota |
| `runtime_unavailable` | 503 | 503 | yes | Storage or dependency outage |
| `recovery_unavailable` | 503 | 503 | yes | Retrieve cannot read a stored original |
| `deadline` | 504 | 504 | yes | Server work exceeded its deadline |

**Per-condition 1.0 mappings** (`legacy_conditions` in the fixture). When
`http_status_v2` is inactive the server MUST answer these exactly as 1.0 did:

| Condition | 1.1 | 1.0 |
|---|---|---|
| Slow request body | 408 `request_timeout` | 413 `payload_limit` |
| Retrieve `limit` < 4 or > `page_bytes` | 400 `invalid_range` | 413 `payload_limit` |
| No queue slot before the deadline | 429 `capacity` | 504 `deadline` |
| Malformed `recovery_binding` | 400 `invalid_request` | 503 `recovery_unavailable` |
| Optimize: total saving ≤ overhead | 200 plan, reason `not_smaller` | 503 `not_smaller` |
| Optimize: stored plan/choice unreadable | 200 plan, reason `cache_state_unavailable` | 503 same code |
| Optimize: recovery store unusable | 200 plan, reason `recovery_unavailable` | 503 same code |
| Optimize: store capacity reached | 200 plan, reason `capacity` | 503 `capacity` |

**Decisions are 200.** Under `http_status_v2`, an optimize outcome that is a
decision about the content is a 200 plan with `status: "bypassed"`,
`replacements: []`, and every sent segment listed in `skipped`. Segments that
would have been replaced carry the plan's reason, and all other segments keep
their own reason. The plan satisfies `middleware-plan` and every client
invariant.

**For all clients:** a storage or database failure MUST be 503
`runtime_unavailable`, never 404 (1.0 retrieve answered 404).

**Client classification** (vectors: `failure_classification`). Outcomes are
derived from `error.code`, not the status, so a mixed fleet of 1.0 and 1.1
replicas behaves the same. Each outcome is
`{reason, breaker, clear_capabilities, retry_after_ms}`:

1. Transport failure (connect, DNS, TLS, reset, truncated body):
   `runtime_unavailable`, breaker yes, clear yes.
2. Client deadline elapsed: `deadline`, breaker yes, clear no.
3. 3xx: `redirect_refused`, breaker yes, clear yes.
4. Non-2xx whose body is not JSON, has no `error.code`, or whose code violates
   the reason grammar: `runtime_unavailable`, breaker yes, clear yes.
5. `request_timeout`: reason `deadline`, breaker yes, clear no.
6. Otherwise reason = code. Breaker is the code's `breaker` flag in §8, or
   `status ≥ 500` for a code not in §8. Clear is yes for
   `runtime_unavailable`, `unknown_capability` and `unsupported_version`, for a
   code not in §8 with status ≥ 500, and no otherwise.
7. `retry_after_ms`, only for 429/503: `min(delta_seconds × 1000, 30000)` when
   `Retry-After` is a non-negative integer, else `null`. HTTP-date values are
   ignored.

A 2xx capabilities body that fails §4 step 1 is `unsupported_version`,
breaker yes, clear yes: an unusable document from the runtime is an invalid
response, like `invalid_plan`. A 2xx plan that fails §7 is `invalid_plan`,
breaker yes, clear yes.

## 7. Client-enforced plan constraints (K5)

A client MUST validate the whole plan before applying any replacement. Any
violation rejects the whole plan: bypass `invalid_plan`, counted by the breaker.
The 1.0 checks remain (`middleware.fixtures.json` `invalid_plans`). In addition,
for each replacement:

1. `transform_id` is in `request.policy.transforms`, and in the capabilities
   snapshot used to build the request its `recovery` is in the client allowlist
   (1.1: `exact_ccr` only). Transforms with `recovery: "none"` are never applied.
2. `recovery_handle` matches `^cmw_[a-f0-9]{48}$`, and `text` starts with
   `[caveman: shortened; exact original via caveman_retrieve handle=<recovery_handle>]\n`.
3. The UTF-8 byte length of `text` is strictly less than the UTF-8 byte length
   of the original segment content. UTF-16 or code-point length does not count.
4. `sha256 == SHA-256(UTF-8(text))`, and `original_sha256` equals the sent
   segment's `sha256`.

Vectors: `plan_constraints` (patches apply to the `base` fixture documents).

## 8. Reason codes (K7)

Every reason a client reports (decision events, `CallReport.reason`, bypass
`Optimization.reason`, warn-once logs) is in this catalog. `breaker` is whether
the outcome counts as a breaker failure. `warn_once` is whether the first
occurrence per (adapter, reason) logs a warning. `strict` is the behavior in
strict mode:

- `raise`: `optimize()` and the adapter call raise `MiddlewareError(code)`.
- `ready`: the reason is only surfaced by `ready()`, which raises, and by
  `preflight()`, which reports unavailable. The request path passes through.
- `none`: never an error.

Nothing raises at wrap or construction time.

| Code | Meaning | Breaker | Warn | Strict |
|---|---|---|---|---|
| `unsupported_version` | Framework version outside the tested range, or runtime protocol excludes client | no | yes | ready (raise for protocol on request path) |
| `version_unverified` | Framework version unreadable (bundled); features detected, call proceeds | no | yes | none |
| `version_unavailable` | Version unreadable and required hooks missing; pass-through | no | yes | ready |
| `invalid_scope` | Supplied scope not normalizable (§9) | no | yes | raise |
| `payload_budget` | Candidate over budget (per candidate), or no candidate fits (whole call) | no | yes | none |
| `opaque_part` | Image/bytes part: never sent, hashed into manifest; never a whole-call reason | no | no | none |
| `recovery_unbound` | Compress mode but no owned recovery binding; local bypass, no I/O | no | yes | none |
| `recovery_name_conflict` | Host already has a foreign `caveman_retrieve` tool | no | yes | raise |
| `adapter_error` | Exception caught by the fail-open guard | no | yes | raise |
| `provider_state_retained` | Provider would persist the compressed turn (e.g. Responses `store`) | no | yes | none |
| `capacity` | Local concurrency full, or server 429/legacy 503 `capacity` | no | yes | raise |
| `circuit_open` | Breaker open; local bypass | no | yes | raise |
| `deadline` | Client deadline, 504, or 408 | yes | yes | raise |
| `runtime_unavailable` | I/O failure, 5xx, unparseable error | yes | yes | raise |
| `invalid_plan` | Plan failed §7 | yes | yes | raise |
| `unknown_capability` | No usable transform, or server 400 | no | yes | raise |
| `no_candidate` | Nothing eligible to send | no | no | none |
| `closed` | Runtime closed; in-flight calls pass through | no | yes | none |
| `unsupported_shape` | Candidate id invalid/duplicate or content ill-formed Unicode (per candidate) | no | yes | raise |
| `redirect_refused` | Runtime answered 3xx | yes | yes | raise |
| `invalid_endpoint`, `remote_content_not_enabled`, `insecure_transport_not_enabled` | Endpoint refused by §15; every call bypasses, no I/O | no | yes | ready |
| `unauthorized`, `forbidden_origin`, `forbidden_namespace`, `invalid_request`, `payload_limit`, `not_found`, `deleted`, `quota_exceeded`, `recovery_unavailable` | Server code passed through | no | yes | raise |
| `epoch_changed`, `identity_conflict`, `cache_state_unavailable` | Server code passed through | no | yes | none |
| `expired`, `not_smaller` | Server code passed through (natural lifecycle / result) | no | no | none |
| `protected`, `record`, `eligible`, `disabled` | Plan or report outcomes, not failures | no | no | none |

The exact table is `reason_catalog` in the fixture and `REASON_CATALOG` in
each SDK. Warn-once:

- TS logs with `console.warn`; Python logs `WARNING` on
  `logging.getLogger("caveman.middleware")`.
- The line MUST contain `adapter=<id or ->` and `reason=<code>`. It MUST NOT
  contain content, scope values, handles or credentials.
- Deduplication is per process, bounded at 1024 (adapter, reason) pairs.

**Local order.** An `optimize()` call reports the first reason that applies:

1. off
2. `closed`
3. `invalid_scope`
4. `capacity` (local concurrency)
5. only if capabilities are not cached: `circuit_open`, then a `Retry-After`
   suppression, then the capabilities fetch
6. `unknown_capability` (no usable transform, compress mode)
7. `recovery_unbound` (client and runtime both in compress mode, no owned
   binding)
8. candidate filtering and budget: `payload_budget`, `unsupported_shape`,
   `no_candidate`
9. `circuit_open`, then a `Retry-After` suppression
10. send

## 9. Scope normalization (K6)

`normalizeScopeToken(value)` (TS) / `normalize_scope_token(value)` (Python):

1. If `value` is not a string, or is `""`, return null.
2. If `value` is not well-formed Unicode (it has an unpaired surrogate),
   return null.
3. If `value` matches `^[A-Za-z0-9._:/-]{1,256}$` in full (no trailing-newline
   match), return it unchanged.
4. Otherwise return `"h-"` + the first 32 characters of the lowercase hex
   SHA-256 of `UTF-8(value)`. No Unicode normalization is applied.

`normalizeScope(input)` / `normalize_scope(input)`:

- `namespace` and `session_id` are required.
- `branch_id` and `cache_epoch` default to `"main"` and `"0"` when absent,
  `undefined`, or `None`.
- A present member that normalizes to null, or a missing required member,
  makes the whole result null.
- Numbers are not stringified.

SDK entry points that take a scope (`recovery`, `optimize`, `retrieve`,
`observe`, `deleteSession`) MUST apply `normalizeScope`, so every route sees the
same normalized scope. In non-strict mode an invalid scope never raises:

- `optimize` bypasses with `invalid_scope`;
- `recovery()` returns null (TS) / None (Python);
- adapters then make a recovery-free call, reporting `invalid_scope` when a
  scope was supplied and `recovery_unbound` when none was.

Vectors: `scope_tokens`, `scopes`.

## 10. Breaker, deadlines, back-pressure (K8)

**Breaker** (one per runtime instance; guards the optimize path, meaning the
`capabilities` fetch plus `optimize`). Parameters: 5 consecutive failures, a
window of 20 outcomes, 10 failures, 30 000 ms open.

- **closed.** Record each call outcome. A failure is any outcome with
  `breaker = yes`. Every other outcome that involved I/O, 4xx included, counts
  as a success. After a failure, open the breaker when there are ≥ 5
  consecutive failures, or when the last 20 outcomes are all recorded and ≥ 10
  of them failed.
- **open.** Calls bypass `circuit_open` with no I/O and record nothing. Once
  `now − opened_at ≥ 30 000 ms`, the state moves to half-open.
- **half-open.** Exactly one call is sent as the probe, and concurrent calls get
  `circuit_open`. A probe failure reopens the breaker, with `opened_at = now`.
  Any other outcome closes it and resets the counters and the window.
- A call consults the breaker immediately before its first network request and
  records exactly one outcome after its last. A call that bypasses locally
  before any I/O neither consults nor records.
- Retrieve, receipts and delete neither consult nor feed the breaker.

Vectors: `breaker_sequences` (`local` = bypass before I/O).

**Deadlines** (vectors: `deadlines`):

- optimize: `deadlineMs` / `deadline_ms` when configured, else the cached
  `limits.deadline_ms`, else 500 (bootstrap: `ready()`, `preflight()` and a
  first call that must fetch capabilities).
- retrieve and `deleteSession`: `retrieveDeadlineMs` / `retrieve_deadline_ms`
  when configured, else `limits.retrieve_deadline_ms`, else 5000.
- receipts use the optimize deadline.
- DNS, connect, TLS, upload and download all count inside the deadline.

**Server queues.**

- optimize, receipts and sessions/delete share a queue of
  `limits.queue_depth` slots (default 16) and the `deadline_ms` budget.
- retrieve has its own queue, `limits.retrieve_queue_depth` (default 16), and
  its own `retrieve_deadline_ms` (default 5000). It MUST NOT hold the metadata
  writer while reading an original.
- `capabilities` is served from memory and is not queued.

**Retry-After.** After an outcome with `retry_after_ms > 0`, the client MUST
NOT send optimize-path requests until that time has elapsed. Calls in the
window bypass locally with the same reason, do no I/O, and record nothing.

**Concurrency.** Local in-flight optimize calls are bounded by
`maxConcurrency` / `max_concurrency` (integer 1–1024, default 16). The excess
bypasses with `capacity`.

## 11. Budgets (K10)

No whole-call bypass happens while at least one candidate fits.

1. **Local filters** (these candidates are never sent):
   - `protected` → `counts.protected`;
   - `opaque` → `counts.opaque`;
   - invalid or duplicate id, or ill-formed content → `unsupported_shape`,
     `counts.unsupported`;
   - UTF-8 content larger than `limits.segment_bytes` → `payload_budget`,
     `counts.budget_skipped`.
2. **Sizes.** `envelope` = UTF-8 bytes of the exact request serialization with
   `segments: []` (compact JSON, non-ASCII as UTF-8, schema key order).
   `item.bytes` = UTF-8 bytes of the serialized segment + 1.
   `max_bytes = limits.request_bytes − envelope`.
   `max_segments = limits.max_segments` (default 256).
3. **Admission** (`planBudget` / `plan_budget`; vectors: `budgets`):
   - tier A holds items whose `key` (`<original_sha256>:<segment_id>`) was
     replaced before in this scope, in input order;
   - tier B holds the rest, newest first (reverse input order);
   - walk A then B and admit an item iff `admitted < max_segments` and
     `used + bytes ≤ max_bytes`; otherwise skip it (`payload_budget`) and
     continue;
   - admitted items go on the wire in input order.
4. **Replaced memory.** A per-runtime LRU of at most 4096 `(scope, key)`
   entries. Every replacement in an accepted plan is added.
5. **Whole-call reason when nothing is sent:** `payload_budget` if any
   candidate was skipped for budget; else `unsupported_shape` if any was
   unsupported; else `no_candidate`.
6. **Manifest.** The SDK sends the **head** of the manifest, at most
   `limits.max_manifest_items` items (default 4096). Adapters bound hashing
   with `manifestBytes` / `manifest_bytes` (default 2 MiB) using
   `manifestWindow(sizes, maxItems, maxBytes)`, the largest `k` with
   `k ≤ maxItems` and `Σ sizes[0..k) ≤ maxBytes`. A head prefix stays
   prefix-stable as history grows, so `epoch_changed` is not triggered. An
   oversize manifest MUST NOT bypass the call. `sequence` defaults to the
   untruncated history length. Vectors: `manifest_windows`.
7. **Opaque parts** (images, bytes) are serialized in the manifest as
   `{"caveman_opaque": h}`:
   - bytes: `h` is the SHA-256 hex of the bytes;
   - strings (data URLs, base64): `h` is the SHA-256 hex of the UTF-8 string;
   - anything else, or an ill-formed string: `h` is `"unhashable"`.

   They never cause a bypass. Vectors: `opaque_manifest_values`.

All budgets are reported: server budgets in capabilities `limits` (§13) and
client skips in decision-event `counts` (§14).

## 12. Lifecycle (K11) and receipts (K13)

Advertised by `originals_lifecycle`. These are server obligations for every
client.

- **Retention.** A scope has `created_at` and `expires_at`.
  - Creation sets `expires_at = now + retention_seconds`.
  - A successful optimize or retrieve sets
    `expires_at = min(now + retention_seconds, created_at + max_retention_seconds)`.
  - `max_retention_seconds` defaults to 604 800 and MUST be ≥
    `retention_seconds`.
- **Expiry.** An expired scope answers 410 `expired`. Its originals, choices,
  grants and plans become unreadable at once and MUST be physically deleted by
  the next sweeps. Sweeps MUST make progress across all expired scopes, not
  revisit the same batch. Only a content-free tombstone may remain, for the
  grace period, to keep answering 410.
- **Originals** are stored per authority. They are encrypted at rest when a key
  is configured, and they never outlive their scope. An original written by an
  optimize that did not publish a plan (aborted, `not_smaller`, error) MUST NOT
  persist.
- **`sessions/delete`**
  - Synchronously deletes every scope, choice, grant, plan and original of the
    request's authority.
  - Answers 200 `{"schema_version":1,"status":"revoked","originals_deleted":true,"deleted":{"scopes","choices","grants","originals"}}`.
    This shape goes to every client; 1.0 clients ignore the body.
  - Is idempotent: an unknown scope answers the same shape with zero counts.
  - If deletion fails, the answer is 503, never a success.
  - Later use of the scope answers 410 `deleted` for the grace period.
  - Against a 1.0 runtime, clients surface `originals_deleted: false` as
    received.
- **Receipts** MUST NOT create per-call rows that outlive `retention_seconds`.
  Servers SHOULD aggregate into per-(authority, UTC hour, `event_kind`)
  counters with token sums, deduplicating `(logical_call_id, attempt_id,
  event_kind)` within retention. The body limit is `limits.receipt_bytes`
  (413 above it). The response is always
  `{"schema_version":1,"status":"recorded","basis":"client_observed","verified_saved_usd":0}`.

## 13. Capabilities limits (K12)

Every value is a positive safe integer; SDK 1.1.0 rejects the document
otherwise. A key that does not apply is omitted, never sent as 0 or null.

| Key | Default | Client use |
|---|---|---|
| `deadline_ms` | 500 | optimize deadline |
| `request_bytes` | 2 097 152 | budget |
| `segment_bytes` | 524 288 | per-candidate cap |
| `page_bytes` | 262 144 | retrieve page size |
| `retrieve_deadline_ms` | 5000 | retrieve/delete deadline |
| `queue_depth` | 16 | informational |
| `retrieve_queue_depth` | 16 | informational |
| `max_segments` | 256 | budget |
| `max_manifest_items` | 4096 | manifest head |
| `receipt_bytes` | 16 384 | receipt cap |
| `quota_requests_per_minute` | omitted = unlimited | informational, per principal |

Top level: `max_retention_seconds` (§12). Clients fill absent 1.1 keys with the
defaults above (`capabilities_parsing` → `limits`).

## 14. Observability (K9)

**Decision event** (`middleware-decision-event`). One event per `report()`,
delivered to `onDecision` / `on_decision`. Keys are identical in both SDKs:

- `schema_version`
- `status`: `applied|reused|skipped|recorded|disabled`
- `reason`
- `adapter`, `logical_call_id`, `attempt_id`: token or null
- `transform_ids`
- `latency_ms`: optimize duration in integer milliseconds, truncated; 0 when
  there was no optimize
- `counts`: `{candidates, sent, protected, opaque, unsupported, budget_skipped,
  skipped, replaced, reused}`, where
  `candidates = sent + protected + opaque + unsupported + budget_skipped`
- `runtime_build`
- `cache_continuity`: `persistent_choices|unavailable|off`

Events MUST NOT contain content, scope values, handles or credentials. A sink
exception MUST NOT affect the call. `no_candidate` emits an event too.
`CallReport` / `onReport` and `onDiagnostic` keep working unchanged.

**OpenTelemetry** is opt-in. The SDK never imports OTel unless the caller
supplies a tracer and/or meter.

- Spans (kind CLIENT): `caveman.middleware.optimize`,
  `caveman.middleware.retrieve`, `caveman.middleware.receipt`.
- Attributes: `caveman.middleware.{adapter,status,reason,runtime_build,policy_revision,candidates,sent,replaced,reused,skipped,budget_skipped}`,
  plus `server.address`, `server.port`, `http.response.status_code`, and
  `error.type` (= reason) on failures.
- Span status is ERROR only for breaker-counted reasons.
- Receipt spans carry usage as `gen_ai.usage.input_tokens`,
  `gen_ai.usage.output_tokens`, `gen_ai.usage.cache_read.input_tokens` and
  `gen_ai.usage.cache_creation.input_tokens`, taken from `input_tokens`,
  `output_tokens`, `cache_read_tokens` and `cache_write_tokens`.
  `reasoning_tokens` is not exported.
- Metrics:
  - counter `caveman.middleware.decisions` (unit `{decision}`; attributes
    `caveman.middleware.adapter`, `.status`, `.reason`);
  - histogram `caveman.middleware.duration` (unit `s`; attributes
    `caveman.middleware.operation` = `optimize|retrieve|receipt`, plus
    `error.type` on failure).

**Headers (client → server):**

- `Caveman-Middleware-Features` (§3).
- `Caveman-Middleware-Client`: space-separated `product/version` tokens, at
  most 256 chars, e.g. `caveman-sdk-typescript/1.2.0` or
  `caveman-sdk-python/1.2.0`; adapters MAY append their own. Servers log it and
  MUST NOT branch on it.
- `traceparent` and `tracestate` (W3C): the active trace context whenever one
  is available.

**Server metrics** (SHOULD, Prometheus text format with HELP/TYPE):

- `caveman_middleware_requests_total{route,status,code}`
- `caveman_middleware_request_duration_seconds{route}`
- `caveman_middleware_queue_depth{queue}`
- `caveman_middleware_unauthorized_total`

## 15. Transport (K14)

`resolveEndpoint` / `resolve_endpoint`, vectors `endpoints`:

- Scheme is `http` or `https`. No userinfo, query or fragment.
- Path segments are non-empty, not `.` or `..`, from `[A-Za-z0-9._~-]` (no
  percent-encoding).
- A trailing `/` is optional. The prefix is preserved and routes are appended
  after it.
- Loopback means `localhost`, `127.0.0.1` and `::1`.
- A non-loopback endpoint requires `allowRemoteContent` /
  `allow_remote_content` (else `remote_content_not_enabled`).
- Non-loopback `http` additionally requires `allowInsecureTransport` /
  `allow_insecure_transport` (else `insecure_transport_not_enabled`).
- Other violations give `invalid_endpoint`.
- Per decision 3 these surface through `ready()` / `preflight()`, not by
  throwing at construction.

`resolveProxy` / `resolve_proxy`, vectors `proxies`:

- Loopback targets are never proxied.
- `https` targets use `https_proxy`, else `HTTPS_PROXY`. `http` targets use
  `http_proxy`, else `HTTP_PROXY`. Empty values count as unset.
- If `no_proxy` (else `NO_PROXY`) matches the target, no proxy is used. The
  value is a comma list, trimmed and case-insensitive:
  - `*` matches everything;
  - `host` or `.host` matches that host and its subdomains (label boundary
    only);
  - `host:port` matches only that port;
  - IP literals match exactly;
  - CIDR is not supported.

**Other rules**

- Python accepts `ssl_context` and an injectable transport. TS keeps `fetch`
  injection.
- Clients SHOULD reuse connections.
- The credential MUST NOT appear in `JSON.stringify`, `inspect`, `repr` or
  `vars()` output.

## 16. Compatibility and deprecation

- **Additive only within 1.x.** `schema_version` stays 1. A change that could
  break a conforming client or server of the previous minor version MUST be
  negotiated (§3) or wait for a new major version. Such changes include:
  - removing or renaming a field;
  - narrowing a type or enum;
  - adding a required request field;
  - sending 1.0 clients a transform, `limits` value or status they reject;
  - changing an existing code's status without negotiation.
- **N-1 both directions.** A runtime MUST serve clients of the previous minor
  protocol with that protocol's shapes and statuses: today, protocol 1.0
  clients (SDK 1.1.0, middleware `0.1.0-alpha.2` / `0.1.0a1`) via the legacy
  mappings above. A client MUST work against a runtime of the previous minor
  protocol: today, `bin-v1.1.8`.
- **Support window.** Protocol N-1 stays supported for at least 12 months after
  the first release of protocol N. For 1.0 that runs to at least 2027-09-30.
- **Deprecation.** A deprecation is announced in this document and in the
  contracts `CHANGELOG.md` at least 12 months before removal. Removal requires
  a new major (`schema_version` 2, `protocol.min` 2).
- **Exceptions.** Bug fixes that replace an incorrect status with
  `runtime_unavailable` (§6), server-internal identity changes (§2), and
  lifecycle guarantees (§12) apply to every client without negotiation.

## Appendix: SDK helpers that the vectors exercise

| Fixture section | TypeScript | Python |
|---|---|---|
| `scope_tokens`, `scopes` | `normalizeScopeToken`, `normalizeScope` | `normalize_scope_token`, `normalize_scope` |
| `capabilities_parsing` | `parseCapabilities(value): CapabilitiesView` | `parse_capabilities(value) -> CapabilitiesView` |
| `plan_constraints` | `validatePlan(plan, request, inputDigest, caps)` | `validate.plan(plan, request, input_digest, caps)` |
| `failure_classification` | `classifyFailure(input): FailureOutcome` | `classify_failure(input) -> FailureOutcome` |
| `breaker_sequences` | `new CircuitBreaker()`, `.allow(nowMs)`, `.record(result, nowMs)`, `.state` | `CircuitBreaker()`, `.allow(now_ms)`, `.record(result, now_ms)`, `.state` |
| `deadlines` | `resolveDeadlines({deadlineMs, retrieveDeadlineMs}, limits \| null)` | `resolve_deadlines(deadline_ms, retrieve_deadline_ms, limits)` |
| `budgets` | `planBudget(items, {maxSegments, maxBytes}, replaced)` | `plan_budget(items, max_segments=, max_bytes=, replaced=)` |
| `manifest_windows` | `manifestWindow(sizes, maxItems, maxBytes)` | `manifest_window(sizes, max_items, max_bytes)` |
| `opaque_manifest_values` | `opaqueManifestValue(value)` | `opaque_manifest_value(value)` |
| `endpoints` | `resolveEndpoint(endpoint, {allowRemoteContent, allowInsecureTransport})` | `resolve_endpoint(endpoint, allow_remote_content=, allow_insecure_transport=)` |
| `proxies` | `resolveProxy(url, env)` | `resolve_proxy(url, env)` |
| `constants`, `reason_catalog` | constants in `middleware/types.ts` | constants in `middleware/types.py` |
