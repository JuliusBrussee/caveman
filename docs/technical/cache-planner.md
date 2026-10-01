# Prompt-cache planner

Cache planner adds provider-native prompt-cache controls where economics and
request shape support them. It never caches model responses.

Cache planner inspects a request, identifies stable prefix boundaries, and adds
provider-native cache hints. Planning and request optimization make no network
calls.

Built-in planners cover Anthropic, OpenAI, Amazon Bedrock, and Google Gemini.
Unknown providers pass through unchanged.

## Fail-safe cases

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

## Provider observations

A cache hint does not prove a cache hit; provider response usage determines
whether a cache read or write occurred. Local planning records remain inferred,
and provider observations retain their own evidence basis.

## Benchmark boundary

Repository cache corpus tests measure safety gates and planner behavior. They do
not establish a universal provider savings rate or market ranking. Provider
features and prices change; recheck capability data and public documentation
before publishing current claims.
