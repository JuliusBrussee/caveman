# Live replay protocol

`cache-replay` turns a cachebench trace into retained provider and task-quality
evidence. It is deliberately harder to execute than to simulate: live inference
costs money, may expose trace content to a provider, and cannot be made
scientifically valid by hiding weak timestamps or estimated token budgets.

## Evidence pipeline

```text
trace.v3
  -> exact NativeRequest reconstruction
  -> cacheengine metadata-only optimization
  -> model-visible equivalence check
  -> authenticated provider request
  -> retained full response + provider usage extraction
  -> external task verifier
  -> observation.v3 + replay-evidence.v1
  -> exact-population 97% report
```

No retry occurs after a request begins. A connection failure can be ambiguous:
provider may have processed and billed request even when client saw no response.
Automatic retry could double bill and corrupt cache chronology.

Before first provider call, runner optimizes every request in trace order and
proves each wire body model-visible equivalent to captured body. Scheduling then
uses absolute offsets from first trace timestamp, so provider latency is not
added to historical start-to-start gaps. `-max-concurrency` bounds active replay
request lifecycles (provider call through task verification) from 1 through
1024; default is 1. Each worker measures drift immediately before transport.
Grounded replay emits `schedule_drift` and sends nothing for request when drift
exceeds `-max-schedule-drift`. Evidence retains target time, measured drift, and
tolerance. Size concurrency for real global overlap and verifier latency;
insufficient capacity fails evidence instead of silently stretching schedule.

## Trace v3

Generated trace records bind original body and every optimizer input:

- request ID, provider, model, region, endpoint, scope, epoch, partition key;
- expected requests/minute and expected calls inside cache TTL;
- runtime/auth mode;
- original body and SHA-256;
- declared cacheable-prefix tokens for planning;
- declared ceiling for total optimized-wire input tokens, exact provider-native
  maximum output tokens, and caller-supplied token-count basis;
- timestamp and timing basis.

Reader retains `trace.v1` for old observation joins and `trace.v2` for exact
optimizer reconstruction. Live replay requires v3: v2 omitted total-input and
output ceilings, so its old prefix-token limit could not bound billed volume.
V3 validates output ceiling against provider request body and rejects streaming,
model mismatch, ambiguous OpenAI ceiling fields, missing ceiling, and nonpositive
values.

Three timing bases exist:

- `grounded_global_timestamps` — real global request ordering; required by
  default for live execution.
- `per_partition_timestamps_only` — LMCache public corpus supplies session-local
  gaps but no global chronology.
- `synthetic_schedule` — deterministic generated workload.

Traces claiming provider-counted input use token basis
`provider_counted_input_tokens`; declared total input must cover optimized wire
body, not original cacheable prefix. Runner binds claim into trace hash but
cannot independently prove caller's prior count operation. Local tokenizer or
fixture counts require explicit downgrade flag and cannot establish a
provider-grounded input ceiling.
Preflight sums declared total input plus exact maximum output for every request
into `declared_billed_token_ceiling` and rejects population above operator cap.
Provider response whose total input or output exceeds request declaration fails
run after that request. Complete usage extraction requires provider-native input
and output counters; missing, ambiguous, fractional, negative, or overflowing
counters fail closed. Declared ceiling is not guaranteed actual-token cap,
dollar cap, or provider invoice because provider processing happens before
response counters can be checked.

## Preflight: zero provider calls

Every run requires hard request, declared billed-token, trace-size,
response-size, concurrency, drift, and inter-request-gap limits. Without
`-execute`, command only validates and prints trace digest plus schedule:

```bash
cd public
go run ./cacheengine/cmd/cache-replay \
  -trace /secure/grounded-trace.jsonl \
  -max-requests 500 \
  -max-declared-billed-tokens 5000000
```

Synthetic/public traces fail grounded defaults. Mechanism testing may opt into
weaker evidence without sending traffic:

```bash
go run ./cacheengine/cmd/cache-replay \
  -trace /tmp/cachebench-openai.jsonl \
  -max-requests 128 \
  -max-declared-billed-tokens 5000000 \
  -allow-ungrounded-timing \
  -allow-estimated-token-budget
```

Preflight prints declared total input, declared maximum output,
`declared_billed_token_ceiling`, `timing_grounded`,
`input_budget_claimed_provider_counted`, and
`max_concurrency`; flags never rewrite evidence bases.
Trace path must be absolute. CLI accepts at most 512 MiB trace input and 100,000
paid requests. Verifier input and aggregate retained artifacts serialize as
bounded streams rather than full-population buffers. Library
trace decoding defaults to 96 MiB per JSONL record, 100,000 records, and 64 MiB
request bodies. Observation decoding defaults to 8 MiB per record and 100,000
records. `ReadTraceJSONLWithLimits` and `ReadObservationJSONLWithLimits` let
embedding callers tighten those bounds.

## Live execution

Live execution additionally requires explicit cost acceptance, new private
output directory, credentials, and task verifier:

```bash
go run ./cacheengine/cmd/cache-replay \
  -trace /secure/grounded-trace.jsonl \
  -max-requests 500 \
  -max-declared-billed-tokens 5000000 \
  -max-concurrency 8 \
  -provider-timeout 2m \
  -execute \
  -accept-live-cost \
  -output /secure/cache-replay-2026-08-10 \
  -verifier-command /absolute/path/to/task-grader \
  -verifier-arg --suite \
  -verifier-arg swebench-pinned
```

Built-in HTTP transport supports:

| Provider | Credential environment | Endpoint |
|---|---|---|
| OpenAI | `OPENAI_API_KEY` | Chat Completions or Responses |
| Anthropic | `ANTHROPIC_API_KEY` | Messages |
| Gemini | `GEMINI_API_KEY` | `generateContent` |
| Bedrock | `AWS_BEARER_TOKEN_BEDROCK`, or access key + secret + optional session token | Converse, SigV4 when using IAM |

Redirects are rejected. Default client ignores environment proxies, requires
TLS 1.2+, applies hard per-request timeout, bounds response size, and never
writes credentials into evidence or errors. Transport also rejects outbound
bodies above 64 MiB by default (`HTTPReplayConfig.MaxRequestBytes` may tighten
or raise this to 256 MiB). Custom base URLs require
`-allow-custom-base-url`; plaintext HTTP also requires explicit loopback-only
test flag. Aggregate configured response plus verifier buffers across workers
cannot exceed 1 GiB. This bounds retained buffers, not provider SDK, kernel,
JSON decoder, or verifier-process memory.

Pre-send runner checks target sample floor against raw and engine-eligible
population. Known non-cacheable engine decisions fail before network, except
provider-minimum misses: those remain honest ineligible samples in report.
Caller-supplied custom transports and verifiers remain responsible for their
own connection pools, process resources, and any stronger policy limits.

## Task verifier

Verifier is executed directly, never through shell. It receives one JSON object
on stdin per provider response:

```json
{
  "schema": "caveman.cachebench.verification.v1",
  "request_id": "openai/session/0002",
  "provider": "openai",
  "model": "gpt-5.6",
  "trace_body_sha256": "<sha256>",
  "wire_body_sha256": "<sha256>",
  "original_request": {},
  "optimized_request": {},
  "provider_response": {}
}
```

It must emit exactly one object:

```json
{
  "schema": "caveman.cachebench.verification.v1",
  "request_id": "openai/session/0002",
  "passed": true,
  "verifier": "swebench-harness@immutable-revision",
  "evidence": {"instance_id":"fixture","resolved":true}
}
```

Unknown fields, duplicate JSON keys, mismatched request IDs, control characters,
empty verifier identity/evidence, oversized output, timeout, or nonzero exit fail replay.
Verifier receives only `PATH`, locale, and temporary-directory environment by
default. Provider credential variables are blocked even when requested through
`-verifier-env`.

Task verifier owns task semantics. Request equivalence alone cannot substitute
for outcome quality.

## Retained output

Output directory must not exist. Command creates it mode `0700`; files are
atomic, synced, and mode `0600`:

```text
manifest.json
report.json
replay-summary.json
evidence.jsonl
observations.jsonl
responses/<sha256(request_id)>.json
quality/<sha256(request_id)>.json
evidence/<sha256(request_id)>.json
observations/<sha256(request_id)>.json
```

Per-request filenames never expose request IDs. Digests bind trace body, exact
wire body, retained provider response, usage object, and grader artifact.
Replay summary reports overall/per-provider p50, p95, p99, and max latency over
same retained evidence population, plus provider-reported input/output token
totals bound to retained usage objects.
`observation.v3` keeps below-minimum requests in population while excluding
them from eligible hit denominators.

Interrupted/failed directory remains evidence. It is never resumed into same
population: elapsed wall time and provider cache state have changed. Start new
run directory and replay full population. Partial runs retain both per-request
artifacts and aggregate JSONL written before failure manifest finalization;
`completed_requests` counts emitted evidence records, including failed ones.
Partial observations cannot pass exact trace join.

Provider-observed pass proves supplied population only. It still does not prove
production prevalence, omitted tails, invoice spend, or Caveman verified
savings. `publishable` therefore remains false.
