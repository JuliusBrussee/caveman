# `@caveman/mastra`

The Caveman adapter for [Mastra](https://mastra.ai)'s OpenTelemetry export: it
points Mastra's OTLP exporter at Caveman, stamps the project/agent/task
identifiers Caveman groups by, and labels the Mastra spans that would otherwise
land in Caveman as untyped generic spans.

Zero runtime dependencies. OpenTelemetry is typed **structurally** — nothing here
imports `@opentelemetry/*` or `@mastra/*`, so the package works against whatever
versions your app already runs.

## Licensing note (read before editing)

This package is implemented **against Mastra's public documentation only**. No
Mastra source code was read, vendored, or transcribed. Every Mastra fact it
relies on is cited in `src/index.ts` with the doc URL it came from:

| Fact used | Source |
|---|---|
| OTel exporter config: `provider.custom.{endpoint, protocol, headers}`, `signals`, `timeout`, `batchSize`, `logLevel`; `observability.configs.otel.serviceName`; `resourceAttributes` | https://mastra.ai/docs/observability/tracing/exporters/otel |
| Span names `chat {model}` · `execute_tool {tool_name}` · `invoke_agent {agent_id}` · `invoke_workflow {workflow_id}` | https://mastra.ai/docs/observability/tracing/exporters/otel |
| Span-type names `AGENT_RUN`, `GENERIC`, `LLM_GENERATION`, `LLM_CHUNK`, `TOOL_CALL`, `MCP_TOOL_CALL`, `WORKFLOW_RUN`, `WORKFLOW_STEP` | https://mastra.ai/docs/v0/observability/ai-tracing/overview |
| Span-type names `MODEL_GENERATION`, `MODEL_STEP`, `MODEL_CHUNK`, `WORKFLOW_SLEEP` and the workflow control-flow types | https://mastra.ai/docs/observability/tracing/overview · https://mastra.ai/reference/observability/tracing/span-filtering |
| Studio's default address `http://localhost:4111` | https://mastra.ai/docs/studio/overview |

Mastra's docs point at its **source file** for the complete `SpanType` enum, so
the table above is what is documented in prose and nothing more. Anything outside
it is not guessed: it falls through to the `spanTypeMap` hook and, failing that,
is counted as unmapped.

### What is undocumented, and the hook you get instead

| Undocumented | Hook |
|---|---|
| Which span attribute carries Mastra's span type | `spanTypeAttributes` (defaults to trying `mastra.span.type`, `mastra.spanType`, `mastra.type`; a miss falls through to span-name classification) |
| Attribute keys for token usage / model / tool name on span shapes that predate Mastra's GenAI mapping | `usageAttributes`, `toolNameAttributes` — both empty by default, so nothing is invented |
| The Studio per-trace URL | `mastraStudioLink(traceId, { template })` — the template is **required** |
| Whether your Mastra version accepts an externally registered OTel span processor | register `cavemanSpanProcessor()` on the TracerProvider your app owns (see below) |

## The `gen_ai.*` caveat

Caveman's normalizer derives model, provider, token counts, and operation type
from OpenTelemetry's GenAI semantic conventions (`gen_ai.*`). Two things are true
at once, and this package handles both:

- **Mastra's current OTel exporter documents that it emits `gen_ai.*` itself**
  ("adheres to OpenTelemetry Semantic Conventions for GenAI v1.38.0", incl.
  `gen_ai.operation.name`, `gen_ai.request.model`, `gen_ai.usage.input_tokens`,
  `gen_ai.usage.output_tokens`). Those spans need no mapping — this package
  leaves every existing `gen_ai.*` value **untouched** and only adds Caveman's
  own identifiers. They show up as `alreadyInstrumentedSpans` in diagnostics.
- **Not every Mastra span carries them** — older versions, the legacy telemetry
  path, sub-spans, and custom spans do not. Those get classified from the
  documented span type or the documented span name, and only then labelled.

If your entire span stream reports as `alreadyInstrumentedSpans`, this package is
doing almost nothing beyond identifiers, and that is the honest outcome.

## What maps, what does not

| Mastra span | `gen_ai.operation.name` | Also set |
|---|---|---|
| `AGENT_RUN` / `invoke_agent {id}` | `invoke_agent` | — |
| `WORKFLOW_RUN` / `invoke_workflow {id}` | `invoke_workflow` | — |
| `WORKFLOW_STEP` | `workflow_step` | — |
| `MODEL_GENERATION` / `LLM_GENERATION` / `chat {model}` | `chat` | `gen_ai.request.model` (from the span name only) |
| `TOOL_CALL` / `execute_tool {name}` | `execute_tool` | `gen_ai.tool.name`, `gen_ai.tool.type=function` |
| `MCP_TOOL_CALL` | `execute_tool` | `gen_ai.tool.name`, `gen_ai.tool.type=mcp` |
| `MODEL_STEP`, `MODEL_CHUNK`, `LLM_CHUNK`, `GENERIC`, `WORKFLOW_CONDITIONAL`, `WORKFLOW_CONDITIONAL_EVAL`, `WORKFLOW_PARALLEL`, `WORKFLOW_LOOP`, `WORKFLOW_SLEEP` | **none** | Caveman identifiers only |

Sub-spans and control-flow spans deliberately get **no** operation label: they are
parts of an operation, and labelling them would inflate model-call and step counts
downstream. They are counted as `contextOnlySpans`, not as failures.

Identifiers added to every classified span: `caveman.task.id`,
`caveman.session.id`, `caveman.project.id`, `caveman.environment`,
`caveman.agent.id`, `caveman.agent.version`, `caveman.workflow.version` (each only
when known), plus a `cave.agent` mirror — the gateway reads the agent slug from
that span attribute, not from a resource attribute.

**Unknown span shapes pass through completely unmodified** — no operation label,
no identifiers, nothing. They are counted in `diagnostics().unmappedSpans` with a
capped sample of their names. That counter is the missing-instrumentation signal:
a large number means Mastra is emitting shapes this adapter has never been told
about, and it should be read as "go look", not as an error.

Trace ids, span ids, parent links, and span names are never touched. Existing
attributes are never overwritten.

## Wiring

```bash
npm install @mastra/otel-exporter@latest
# plus the OTLP HTTP/JSON transport your Mastra version asks for
```

```ts
import { Mastra } from "@mastra/core";
import { Observability } from "@mastra/observability";
import { OtelExporter } from "@mastra/otel-exporter";
import { NodeSDK } from "@opentelemetry/sdk-node";
import { cavemanExporterConfig, cavemanSpanProcessor, withTask, recordOutcome } from "@caveman/mastra";

const caveman = cavemanExporterConfig({
  baseUrl: "https://gateway.caveman.so",
  apiKey: process.env.CAVEMAN_API_KEY!,   // cave_live_…
  projectId: process.env.CAVEMAN_PROJECT_ID!,
  environment: process.env.NODE_ENV ?? "development",
  agentId: "support-agent",
  agentVersion: "31",
  workflowVersion: "17",
  serviceVersion: process.env.GIT_SHA ?? "dev",
  serviceName: "support-agent",
});

export const mastra = new Mastra({
  observability: new Observability({
    configs: {
      otel: {
        serviceName: caveman.serviceName,
        exporters: [new OtelExporter({ provider: caveman.provider, resourceAttributes: caveman.resourceAttributes })],
      },
    },
  }),
});

// The processor is a plain OTel SpanProcessor shape ({ onStart, onEnd, shutdown,
// forceFlush }). Register it on the TracerProvider your application owns:
new NodeSDK({
  spanProcessors: [
    cavemanSpanProcessor({
      projectId: process.env.CAVEMAN_PROJECT_ID!,
      environment: process.env.NODE_ENV ?? "development",
      agentId: "support-agent",
      agentVersion: "31",
      workflowVersion: "17",
    }),
  ],
}).start();
```

`protocol` defaults to `"http/json"` and nothing else is accepted: Caveman's
`POST /otlp/v1/traces` decodes protobuf-JSON, so a `http/protobuf` or `grpc`
batch would be rejected at the edge. The config throws rather than letting you
find that out in production.

Auth: the gateway reads `x-cave-api-key` first and falls back to `authorization`
(stripping a `Bearer ` prefix), so both headers are emitted; either alone would
authenticate.

### Binding a task

```ts
await withTask(ticket.id, `session_${ticket.conversationId}`, async () => {
  await agent.generate(ticket.body);
});
```

Every classified span produced inside the callback carries
`caveman.task.id` / `caveman.session.id`. Pass `resolveTask` to the processor if
your task identity comes from somewhere other than the async context.

### Recording the outcome

```ts
await recordOutcome(
  { controlApiUrl: "https://api.caveman.so", token: sessionToken, projectId },
  {
    taskId: ticket.id,
    contract: "resolved_support_ticket",
    values: { ticketResolved: true, customerReopenedWithin72Hours: false, csat: 5 },
    evidence: { ticketId: ticket.id, resolutionEventId: event.id },
  },
);
```

`POST {controlApiUrl}/api/v1/projects/{projectId}/outcomes` with
`correlation_kind: "task"`, `correlation_id: taskId`, and the caller's declared
`source` (`ci | environment | database | business_event | human` — required;
`agent_claim` and `judge` are unstorable server-side, and stating `environment`,
`database`, or `ci` asserts the value was read from that system, not from the
agent's own output).

- `observed_outcome` derivation: `` `${contract} ${stableStringify(values)}` `` —
  the contract name, a space, then the values as JSON with **object keys sorted at
  every level**. Sorting is load-bearing: the server hashes the record for
  idempotency, so the same values in a different key order must produce the same
  bytes and therefore the same record.
- `evidence_refs`: each evidence entry serialized as `"key:value"`. The server
  accepts 1–64 refs; both bounds are enforced here before the call.
- `confidence` is **required** (the server made it mandatory so an omitted value
  is rejected rather than silently ingested as confident — this client does not
  undo that with a default); `occurred_at` defaults to now (ISO-8601).
- **Fails closed before any network call** on a missing task id or contract,
  empty values, values that are not plain JSON (a `Date`, a class instance, a
  non-finite number), empty or oversized evidence, a missing or out-of-range
  confidence, a missing or untrusted `source`, or an invalid date.
- **Exactly one request, no retries.** A non-2xx response throws
  `CavemanOutcomeError` carrying `status` and the response body verbatim.

The control API authenticates with a session access token (`authorization:
Bearer …`), not the project key used for OTLP.

### Deep links

```ts
cavemanTraceLink("https://app.caveman.so", projectId, traceId);
// → https://app.caveman.so/traces/{traceId}?project_id={projectId}
//   (the dashboard resolves the project from the workspace switcher today; the
//    query parameter is carried so the link is unambiguous about which project's
//    trace it means)

mastraStudioLink(traceId, { template: "{baseUrl}/observability?traceId={traceId}" });
// baseUrl defaults to http://localhost:4111 — Studio's documented default address
```

Mastra documents a Studio Observability tab but no per-trace URL, so the template
is required. This package will not invent a link shape that might 404.

## Diagnostics

```ts
processor.diagnostics();
// { totalSpans, mappedSpans, alreadyInstrumentedSpans, contextOnlySpans,
//   unmappedSpans, unmappedSpanNames }
```

## Honesty

Nothing in this package computes, estimates, or claims a saving, and it writes no
money field. It moves identifiers and operation labels onto spans so Caveman can
group them; every number it produces is a count of spans it saw.

## Development

```bash
pnpm build   # tsc → dist/
pnpm test    # tsc + type tests + node --test tests/*.runtime.mjs (no network)
```
