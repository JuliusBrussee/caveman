# Cache planner and trajectory rewriter

Caveman includes two independent optimization systems:

- cache planner adds provider-native prompt-cache controls where economics and
  request shape support them;
- trajectory rewriter asks a model to shorten selected historical agent context
  and accepts output only after structural checks.

Neither system caches model responses.

## Prompt-cache planner

Cache planner inspects a request, identifies stable prefix boundaries, and adds
provider-native cache hints. Planning and request optimization make no network
calls.

Built-in planners cover Anthropic, OpenAI, Amazon Bedrock, and Google Gemini.
Unknown providers pass through unchanged.

### Fail-safe cases

Planner keeps original request when it sees:

- record mode;
- unsupported billing tier;
- malformed or ambiguous JSON;
- duplicate JSON keys;
- unsupported provider behavior;
- volatile content at a candidate boundary;
- provider semantics that have drifted from registered capability data.

Provider-specific parsers own exact request shape. Generic planning uses
expected call count and provider rate units. It does not invent dollar savings
when provider pricing evidence is absent.

### Provider observations

A cache hint does not prove a cache hit; provider response usage determines
whether a cache read or write occurred. Local planning records remain inferred,
and provider observations retain their own evidence basis.

## Trajectory rewriter

Trajectory rewriting targets older, lower-value zones in an agent transcript.
It currently supports configured Anthropic and OpenAI rewrite models.

Rewrite path requires:

1. input exceeds configured threshold before any provider call;
2. model and credential are explicitly configured;
3. response passes structural acceptance checks;
4. failure signals and counts remain present along with exit codes and references;
5. output beats configured size threshold;
6. exact original is stored behind a recovery pointer.

Rejected rewrites are never inserted into agent context. Provider usage from a
rejected attempt is still reported because spend occurred.

### Why failures are preserved

Past failures often explain why an agent chose its current approach. Removing
an exit code, error class, or failed command can make a later step appear
unmotivated. Acceptance therefore checks more than output length.

### Recovery and trust

Accepted rewrite is model-generated text, not a lossless encoding. CCR provides
access to original trajectory, but callers still need task evaluation before
claiming equal quality.

## Choosing between systems

| Need | Use |
|---|---|
| Reuse an exact stable provider prefix | Cache planner |
| Shorten old conversational history | Trajectory rewriter |
| Avoid any model-visible byte change | Record mode; cache hints only where request contract treats them as metadata |
| Work offline | Cache planner or Engine; not trajectory rewriter |

Systems can coexist, but evidence must remain separate. Cache usage, Engine
token estimates, rewrite-model spend, and downstream model quality are distinct
measurements.

## Benchmark boundary

Repository cache corpus tests measure safety gates and planner behavior. They do
not establish a universal provider savings rate or market ranking. Provider
features and prices change; recheck capability data and public documentation
before publishing current claims.
