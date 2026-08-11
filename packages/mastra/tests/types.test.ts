/**
 * Type-level assertions. Compiled by `tsc --project tsconfig.test.json`, never run.
 */
import {
  cavemanExporterConfig,
  cavemanSpanProcessor,
  cavemanTraceLink,
  mastraStudioLink,
  recordOutcome,
  withTask,
  type CavemanProcessorDiagnostics,
  type CavemanSpanKind,
  type CavemanSpanProcessor,
  type MastraSpanLike,
  type RecordOutcomeResult,
} from "../src/index.js";

const config = cavemanExporterConfig({
  baseUrl: "https://gateway.caveman.so",
  apiKey: "cave_live_abcdefghijkl_secret",
  projectId: "project_123",
  environment: "production",
});
const endpoint: string = config.endpoint;
const customEndpoint: string = config.provider.custom.endpoint;

// A structurally-typed span: no @opentelemetry/* import is required to satisfy it.
const span: MastraSpanLike = { name: "chat gpt-4o-mini", attributes: {} };

const processor: CavemanSpanProcessor = cavemanSpanProcessor({
  projectId: "project_123",
  resolveTask: (s) => (s.name === "x" ? { taskId: "t" } : undefined),
  spanTypeMap: { CUSTOM: "tool_call" satisfies CavemanSpanKind },
});
processor.onStart(span);
processor.onEnd(span);
const diagnostics: CavemanProcessorDiagnostics = processor.diagnostics();
const unmapped: number = diagnostics.unmappedSpans;

const outcome: Promise<RecordOutcomeResult> = withTask("ticket_991", "session_552", () =>
  recordOutcome(
    { controlApiUrl: "https://api.caveman.so", token: "jwt", projectId: "project_123" },
    {
      taskId: "ticket_991",
      contract: "resolved_support_ticket",
      values: { ticketResolved: true, csat: 5 },
      evidence: { ticketId: "ticket_991" },
      confidence: 1,
      source: "business_event",
    },
  ),
);

const links: string[] = [
  cavemanTraceLink("https://app.caveman.so", "project_123", "0af7651916cd43dd8448eb211c80319c"),
  mastraStudioLink("0af7651916cd43dd8448eb211c80319c", { template: "{baseUrl}/observability?traceId={traceId}" }),
];

export type _Checked = [typeof endpoint, typeof customEndpoint, typeof unmapped, typeof outcome, typeof links];
