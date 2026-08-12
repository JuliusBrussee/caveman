# cachebench

`cachebench` answers one question: can cacheengine sustain at least 97% cache
hits on a growing tool-using agent, without hiding cold starts, compactions,
semantic mutations, invalid usage, or unsupported provider behavior?

## Golden path

```bash
cd public
go run ./cacheengine/cmd/cachebench
```

Zero flags run 128 requests/provider across Anthropic, OpenAI, Bedrock, and
Gemini. Workload carries 8,192 declared stable system/tool tokens, growing user,
assistant tool-call, and tool-result history, plus planned compaction every 64
turns. Compaction starts a new epoch and its cold write remains in denominator.

Expected readout:

```text
CACHEBENCH agent-cache evaluation: PASS
Evidence: benchmark_simulated | publishable: false | quality: model_visible_request_equivalence
Target: request hits >= 97.00%, eligible-token hits >= 97.00%, >= 100 eligible requests/provider

provider   mode       rolling  eligible  inelig  req-hit  token-hit  attributed  cold  invalid  safety  gate
anthropic  explicit   true          128       0   99.22%     97.79%      97.79%     1        0       0  PASS
openai     explicit   true          128       0   99.22%     97.79%      97.79%     1        0       0  PASS
bedrock    explicit   true          128       0   99.22%     97.79%      97.79%     1        0       0  PASS
gemini     implicit   true          128       0   99.22%     97.79%       0.00%     1        0       0  PASS
```

This proves local mechanism against deterministic provider-cache simulation.
It makes zero provider calls and is never production or verified-savings
evidence.

## Metric contract

Primary gates include cold writes:

```text
request_hit_rate = requests with cache_read_tokens > 0
                   / all cache-eligible requests

token_hit_rate   = sum(cache_read_tokens)
                   / sum(cache-eligible prompt-prefix tokens)
```

Both must be at least target, every provider must have minimum sample count,
quality pass rate must equal 100%, model-visible equivalence failures must be
zero, and invalid samples must be zero. Rates never exclude planned cold starts,
TTL expiry, compaction, or prefix invalidation.

`opportunity_*_capture_rate` is diagnostic, never a substitute gate. It divides
actual reads by exact reusable prefixes previously seen in same evidence-backed
partition, ignoring TTL. This separates engine/provider realization from cold
starts and novel suffix tokens while keeping raw 97% targets unchanged.

Requests shorter than provider minimum are reported as `inelig`; they are not
cache misses and are not invalid samples. Unknown providers, malformed bodies,
silent stable-prefix drift, unsafe transforms, or missing usage remain invalid
and fail gate.

`attributed_token_hit_rate` is separate. Gemini implicit hits remain organic;
they help cache performance but never become engine-causal.

## Public agent corpus

[LMCache Agentic Traces](https://huggingface.co/datasets/sammshen/lmcache-agentic-traces)
contains recorded SWE-bench, GAIA, and WildClaw agent request histories and is
licensed CC-BY-4.0. Fetch immutable revision and verify every LFS object:

```bash
cd public
sh cacheengine/cachebench/scripts/fetch_lmcache_agentic_traces.sh /tmp/lmcache-agentic-traces
python3 -m pip install pyarrow
```

Replay all five shards as one continuous corpus. Full normalized input is about
2.43 GB, so command raises default 1 GiB retained-input guard explicitly:

```bash
cachebench_data_dir=/tmp/lmcache-agentic-traces
for cachebench_shard in "$cachebench_data_dir"/*.parquet; do
  python3 cacheengine/cachebench/scripts/lmcache_parquet_to_jsonl.py "$cachebench_shard"
done | GOMEMLIMIT=12GiB go run ./cacheengine/cmd/cachebench \
  -corpus - \
  -corpus-name lmcache-agentic-traces/full-train \
  -corpus-license CC-BY-4.0 \
  -corpus-revision hf:6e043b9e89865df3aec19fd5679286b683bfd70e \
  -corpus-max-bytes 3221225472 \
  -providers openai \
  -target .97 \
  -format json
```

[Machine-readable pinned result](results/lmcache-agentic-traces-2026-08-10.json).
Measured 2026-08-10: 24,880 requests across 767 sessions; 24,706
OpenAI-cache-eligible requests; 96.89% request-hit rate; 95.76% estimated-token
hit rate; 174 below provider minimum; zero invalid samples; zero model-visible
equivalence failures. Overall strict gate is **FAIL** on both metrics. Corpus has
no global timeline, so benchmark isolates sessions and credits no cross-session
reuse. One cold epoch per session caps request hits at 96.92% before other
losses: 97% is unattainable on this population without separately measured
cross-session reuse. Engine captured 100% of eligible within-session reusable
opportunities: 23,938 requests and 665,558,422 estimated tokens. Opportunity
capture is diagnostic; it does not turn failed raw gates into a pass. Result
remains deterministic simulation using o200k estimates, not provider counters
or publishable savings evidence.
Dataset exposes normalized messages, model labels, output lengths, and
per-session gaps—not full original provider envelopes or tool schemas.
Corpus decoding bounds source bytes, retained bytes, rows, sessions, row size,
message count, and message size. Default source/retained limits are each 1 GiB;
explicit limits above 16 GiB fail closed.

Add `-trace-out /tmp/lmcache-openai-trace.jsonl` to corpus command to export
same provider-native request population as trace v3. Public corpus timestamps
remain session-local and token counts remain o200k estimates, so live runner
rejects them by default. Explicit downgrade flags preserve those limitations in
preflight; they do not manufacture grounded evidence. Corpus simulation and
live evidence remain separate reports.

Cross-provider replay on pinned shard `train-00000-of-00005` exercised 4,976
requests through each built-in adapter. Request-hit rates were 97.01%
Anthropic, 97.63% OpenAI, 97.01% Bedrock, and 97.01% Gemini. Estimated-token hit
rates were 95.69%, 96.43%, 95.69%, and 95.69%; every strict gate failed on token
rate, with zero invalid samples and zero equivalence failures. OpenAI captured
100% of reusable request/token opportunities; other three captured 99.36% and
99.23% because 5-minute TTL assumptions expired 31 otherwise reusable requests.
OpenAI benefits from guaranteed 30-minute GPT-5.6 retention; direct Anthropic
and selected Bedrock profile use guaranteed 5-minute retention. Gemini value is
organic simulation with 5-minute fallback, never causal engine attribution.

## Real provider replay

Generate replayable real-agent-shaped request bodies:

```bash
go run ./cacheengine/cmd/cachebench \
  -providers openai \
  -trace-out /tmp/cachebench-openai.jsonl
```

First-class `cache-replay` performs exact optimizer reconstruction, authenticated
provider calls, retained response capture, external task verification, and
bounded absolute-time concurrent dispatch. Entire trace passes optimization and
model-visible equivalence before first live call. Start with zero-network
preflight:

```bash
go run ./cacheengine/cmd/cache-replay \
  -trace /tmp/cachebench-openai.jsonl \
  -max-requests 128 \
  -max-declared-billed-tokens 5000000 \
  -allow-ungrounded-timing \
  -allow-estimated-token-budget
```

Grounded production traces omit downgrade flags. Live mode additionally requires
`-execute -accept-live-cost -output <new-private-dir> -verifier-command <path>`.
Trace v3 binds caller-declared optimized-wire input ceilings plus exact
provider-native output caps; preflight rejects declared sum above operator
limit. Provider-counted basis is caller-attested, then checked against actual
response input and output totals. Prefix tokens remain separate cache-planning
input.
Set `-max-concurrency` high enough for captured global overlap; request-level
drift fails closed when capacity or verifier latency cannot preserve schedule.
CLI caps trace input at 512 MiB, aggregate configured response/verifier buffers
at 1 GiB, paid trace population at 100,000 requests, and provider requests at
configurable `-provider-timeout` (2 minutes default). Verifier input and aggregate
artifacts stream instead of duplicating complete populations in serialization
buffers. Trace, output, and verifier paths are absolute to avoid working-directory
ambiguity. Library trace decoding defaults to 96 MiB per JSONL record, 100,000
records, and 64 MiB request bodies; observation decoding defaults to 8 MiB per
record and 100,000 records. Bounded reader variants let embedders tighten these
limits.
See [`REPLAY_PROTOCOL.md`](REPLAY_PROTOCOL.md) for provider credentials, grader
wire contract, failure semantics, and retained artifact layout.

Runner emits observation v3 records:

```json
{"schema":"caveman.cachebench.observation.v3","request_id":"request-001","request_body_sha256":"<sha256-from-trace>","provider_evidence_sha256":"<sha256-of-retained-provider-response>","provider":"openai","epoch":"agent-epoch-1","eligible_input_tokens":10000,"cache_eligible":true,"applied":true,"engine_decision":"apply","engine_reason":"applied","profile_id":"openai-gpt-5.6-explicit-v1","attribution":"causal","optimizer_ids":["openai-prompt-cache-key","cave-cache-openai-explicit-v1"],"quality_passed":true,"quality_verifier":"swebench-harness@pinned-revision","quality_evidence_sha256":"<sha256-of-retained-grader-artifact>","usage":{"input_tokens_details":{"cached_tokens":9900,"cache_write_tokens":100}}}
```

Evaluate provider counters:

```bash
go run ./cacheengine/cmd/cachebench \
  -observations /tmp/cachebench-observations.jsonl \
  -trace-in /tmp/cachebench-openai.jsonl
```

Embedding code can still construct records with `NewObservationRecord`; it binds
actual `NativeResult` attribution and optimizer IDs plus retained provider
response, verifier identity, and quality-artifact SHA-256. Observed evaluator rejects
duplicate IDs/JSON keys, unknown fields, malformed/negative counters, counters above
eligible input, causal labels without exact optimizer evidence, any failed task
verification, and fewer than required samples.

Provider-observed pass still does not prove omitted tails, traffic prevalence,
provider invoice spend, or verified savings. Those remain managed-ledger work.

Custom providers build `Trace` values and call `EvaluateTrace`; same custom
`Profile`/`Driver` used by cacheengine remains source of cache semantics.

## Failure drills

```bash
# Every Anthropic request exceeds 5-minute TTL: expected FAIL.
go run ./cacheengine/cmd/cachebench \
  -providers anthropic \
  -turns 128 \
  -step 6m \
  -target 0.97
```

Program exits `1` when gate fails, `2` for invalid input/configuration, `0` only
when every selected provider clears gate.
