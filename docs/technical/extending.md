# Extending Caveman

Extensions should preserve original input on uncertainty, keep recovery exact,
and label measurements by evidence source. This page maps common extension
points and required proof.

## Add an Engine compressor

Implement compressor under `engine/compressors` and register it only after
selection behavior is defined.

Required properties:

- bounded parser and deterministic output;
- declared safety class;
- explicit automatic or forced-only selection;
- original-byte fallback for malformed or unsupported input;
- smaller-output gate;
- CCR integration for lossy output;
- tests for recovery, boundary, failure, and non-winning input;
- fixture-backed performance claim, if one is published.

Update registry count and technical docs when default registry changes.

## Add a provider adapter

Provider adapter belongs in local proxy provider package. Keep transport shape,
authentication, streaming, usage parsing, and capability rules inside adapter
instead of scattering provider-name branches through runtime.

Adapter needs:

1. documented routes and default public endpoint;
2. credential precedence without secret logging;
3. streaming and error pass-through tests;
4. SSRF validation for configurable endpoints;
5. provider usage parsing with unknown-field tolerance;
6. explicit cache, count, image, and tool capability data;
7. unknown pricing fallback to zero plus `unpriced`;
8. record-mode byte-equivalence test.

An OpenAI-compatible provider can use named compatibility mount when protocol
matches. Do not claim full provider support from one successful chat request.

## Add an agent profile

Profiles live under `agents` and compile into CLI artifacts.

Choose one supported wire protocol and injection method, then declare executable,
endpoint templates, extensions and tested upstream version. Compiler rejects
unsafe paths and unknown fields; reserved commands and unapproved environment
templates are also rejected.

Profile proof should include:

- compiled artifact matches source;
- CLI launches exact executable and preserves arguments;
- record mode request bytes match unwrapped request;
- configured provider route receives expected protocol;
- hook or plugin install and removal are reversible;
- unsupported upstream version produces useful error.

## Add an SDK recipe

Recipes live under `integrations`. Keep them small and executable. A recipe
should show endpoint, provider credential behavior, recovery integration where
available, and record-mode escape hatch.

Do not copy SDK source into recipe. Link public SDK documentation for provider
semantics that change frequently.

## Add a schema or SDK field

Contract changes begin in `packages/shared/contracts`. Regenerate derived artifacts,
then update TypeScript and Python SDKs together. Add round-trip and rejection
tests for both languages.

Use additive fields when compatible. Do not make clients accept unknown enum as
successful state when safe behavior is rejection.

## Add a grader

Add type, schema, TypeScript implementation, fixtures, and fail-closed unknown
handling. Output must identify method and reproducible inputs; grader pass is
scoped evidence rather than a savings label.

## Update provider catalog

Catalog change needs authoritative public source, effective date, unit
normalization, generator run, and fixture updates. Preserve old dated records
when needed for historical calculations. Never infer a missing price from a
nearby model name.

## Add a skill, hook, or plugin

- Skill: state behavior in plain language; avoid unsupported reduction or
  quality claims.
- Hook: document event, changed files, permissions, failure behavior, and
  removal.
- Plugin: minimize host permissions and pin tested host version.

Installation preview should show exact source. State whether integration sends
network traffic and where it stores local state.

## Documentation required with code

Update:

- nearest package README;
- relevant page in this manual;
- CLI help for public commands;
- configuration reference for new keys;
- security or evidence page when trust boundary changes;
- changelog or release note according to package process.

Run commands in [Testing and benchmarks](testing-and-benchmarks.md). Report
targeted proof separately from gates not run.
