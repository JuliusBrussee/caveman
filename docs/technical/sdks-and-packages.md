# SDKs and public packages

Repository contains standalone packages for application integration, agent
construction, schemas, evaluation, user-interface labels, provider prices, and
framework adapters. Package manifests and generated declarations remain exact
API references; this page explains how pieces fit together.

## TypeScript and Python SDKs

`packages/sdk/typescript` and `packages/sdk/python` implement matching high-level
operations:

- create a Caveman client;
- configure provider calls;
- define, defer, and search tools;
- compress eligible context;
- assemble a request from context parts;
- create and consume context packs;
- emit traces and OpenTelemetry data;
- apply runtime policy.

Field names and `/sdk/v1/*` request contracts should remain aligned across both
languages. A contract change is incomplete until implementations, schemas, and
tests agree.

TypeScript package:

```bash
pnpm --dir packages/sdk/typescript build
pnpm --dir packages/sdk/typescript test
```

Python package:

```bash
python -m pytest -q packages/sdk/python
```

Neither SDK should guess cost for an unknown model. Unknown pricing remains zero
and explicitly unpriced.

## Agent SDK

`packages/agent` is TypeScript runtime for constructing and running tool-using agents. It
exports agent definitions, run and stream interfaces, subagent support, tools,
memory and context assembly plus output handling, evaluation hooks and sandbox
modes.

Agent SDK source includes detailed package README and examples. Its sandbox
selection controls runtime permission policy; it is not a substitute for
operating-system isolation when untrusted code runs.

Build and test:

```bash
pnpm --dir packages/agent build
pnpm --dir packages/agent test
```

### Agent initializer

`packages/create-caveman-agent` creates strict starter project for Agent SDK:

```bash
npm create @caveman-ai/agent@latest my-agent
cd my-agent
npm run doctor
npm run dev
```

Initializer supports Anthropic, OpenAI and Google. Exactly one detected provider
credential selects provider without prompt; zero or multiple credentials prompt
once. Secrets are neither printed nor written. `--no-install` skips dependency
installation. Generated evaluation begins unapproved and needs review before
locked build.

## Shared contracts

`packages/shared/contracts` stores JSON Schema wire contracts. Current schema
set covers:

- adapter conformance and agent-run receipts;
- cache guards and canonical spans;
- Cave builds and Cave Plans;
- context intermediate representation;
- continuous-improvement reports;
- evaluation cases and grader registry;
- harness events and policy;
- practices;
- transform capabilities and traces.

Generate and validate artifacts through package scripts rather than editing
generated outputs independently.

## Provider catalog

`shared/provider-catalog` stores dated public list-price records and generated
catalog outputs for local estimates. Unsupported model returns zero price plus
an `unpriced` marker; catalog does not represent invoice data.

Catalog updates need source date, provider unit semantics, generated-artifact
refresh, and tests. See [Accounting and evidence](accounting-and-evidence.md).

## Benchmark tooling

`packages/subagent-tax` measures local context and delegation fixtures without issuing
provider requests. Its output is benchmark evidence for exact fixtures and
counter implementation, not a general savings claim.

## Package release model

Registry packages release independently through scoped workflow inputs, while
native binaries use a separate signed process. See
[`PACKAGE_RELEASES.md`](../PACKAGE_RELEASES.md) and [Install and
update](install-and-update.md).
