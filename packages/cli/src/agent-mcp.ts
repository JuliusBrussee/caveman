import { createInterface } from "node:readline";
import type { Readable, Writable } from "node:stream";

export type JSONValue =
  | null
  | boolean
  | number
  | string
  | JSONValue[]
  | { [key: string]: JSONValue };

export type JSONObject = { [key: string]: JSONValue };

export type AgentMcpClient = {
  request(path: string, options?: { method?: "GET" | "POST"; body?: JSONObject }): Promise<JSONValue>;
  projectId(): Promise<string>;
  recordToolCall?(event: { tool: string; result: "ok" | "error"; durationMs: number }): void;
};

type ToolDefinition = {
  name: string;
  description: string;
  inputSchema: JSONObject;
  outputSchema: JSONObject;
  annotations: {
    title: string;
    readOnlyHint: boolean;
    destructiveHint: boolean;
    idempotentHint: boolean;
    openWorldHint: boolean;
  };
};

type ToolResult = {
  content: Array<{ type: "text"; text: string }>;
  structuredContent: JSONObject;
  isError?: boolean;
};

type McpSessionState = {
  negotiated: boolean;
  initialized: boolean;
};

const LATEST_PROTOCOL_VERSION = "2025-11-25";
const SUPPORTED_PROTOCOL_VERSIONS = new Set([
  "2024-11-05",
  "2025-03-26",
  "2025-06-18",
  LATEST_PROTOCOL_VERSION,
]);

const OBJECT_OUTPUT: JSONObject = {
  type: "object",
  additionalProperties: true,
};

const PROJECT_QUERY_PATHS = new Set([
  "/api/v1/reports/overview",
  "/api/v1/reports/costs",
  "/api/v1/reports/verified-savings",
  "/api/v1/reports/compression",
  "/api/v1/reports/routes",
  "/api/v1/reports/agents",
  "/api/v1/reports/workflows",
  "/api/v1/reports/cave-score",
  "/api/v1/reports/usage",
  "/api/v1/experiments",
]);

const REPORT_PATHS: Record<string, string> = {
  overview: "/api/v1/reports/overview",
  costs: "/api/v1/reports/costs",
  verified_savings: "/api/v1/reports/verified-savings",
  compression: "/api/v1/reports/compression",
  routes: "/api/v1/reports/routes",
  agents: "/api/v1/reports/agents",
  workflows: "/api/v1/reports/workflows",
  score: "/api/v1/reports/cave-score",
  usage: "/api/v1/reports/usage",
};

const TRACE_FILTER_KEYS = new Set([
  "workflow",
  "agent",
  "model",
  "provider",
  "error_code",
  "auth_mode",
  "runtime_mode",
  "cache_status",
  "session_id",
  "client_user_hash",
  "trace_id",
  "member_user_id",
  "api_key_id",
  "optimization_id",
  "status_class",
  "min_cost_usd",
  "max_cost_usd",
  "min_total_tokens",
  "max_total_tokens",
  "min_latency_ms",
  "max_latency_ms",
  "has_error",
  "compressed",
  "monitor",
]);

const TOOL_ARGUMENT_KEYS: Record<string, Set<string>> = {
  caveman_context: new Set(),
  caveman_report: new Set(["report"]),
  caveman_plan: new Set(),
  caveman_trace_search: new Set(["filters", "from", "to", "date_field", "sort", "group_by", "page_size", "cursor"]),
  caveman_trace_get: new Set(["trace_id"]),
  caveman_experiment_get: new Set(["action", "experiment_id"]),
};

// Agent trace reads stay metadata-only even when the underlying dashboard
// endpoint grows new fields. In particular, SDK/OTLP span attributes and events
// may contain prompt, completion, or tool payloads, so this is an allowlist
// projection rather than a denylist.
const AGENT_TRACE_FIELDS = new Set([
  "timestamp",
  "request_id",
  "trace_id",
  "project_id",
  "agent_slug",
  "workflow_slug",
  "provider",
  "provider_connection_slug",
  "model",
  "endpoint",
  "stream",
  "status_code",
  "error_code",
  "latency_ms",
  "ttfb_ms",
  "request_bytes",
  "response_bytes",
  "input_tokens",
  "output_tokens",
  "cached_input_tokens",
  "cache_creation_input_tokens",
  "reasoning_tokens",
  "total_cost_usd",
  "input_cost_usd",
  "output_cost_usd",
  "cached_cost_usd",
  "price_catalog_version",
  "runtime_mode",
  "policy_version",
  "optimization_ids",
  "cache_status",
  "cache_savings_usd",
  "compression_savings_usd",
  "compression_ratio",
  "compression_tokens_before",
  "compression_tokens_after",
  "route_from",
  "route_to",
  "exploration_tool_turns",
  "read_only_tool_result_tokens",
  "auth_mode",
  "route_population",
  "baseline_model",
  "baseline_cost_usd_inferred",
  "cache_tokens_delta",
  "output_tokens_ratio",
  "expansion_unknown",
  "route_net_negative",
  "provider_region",
  "span_id",
  "parent_span_id",
  "session_id",
  "token_usage_basis",
  "token_usage_contract_version",
  "compression_token_count_basis",
]);

const AGENT_SPAN_FIELDS = new Set([
  "span_id",
  "parent_span_id",
  "span_type",
  "span_name",
  "status",
  "duration_ms",
  "provider",
  "model",
  "input_tokens",
  "output_tokens",
  "cached_input_tokens",
  "total_cost_usd",
  "source",
  "request_id",
]);

const AGENT_TIMELINE_FIELDS = new Set(["span_id", "name", "type", "start_ms", "duration_ms"]);

const STRING_LIST_SCHEMA: JSONObject = {
  type: "array",
  items: { type: "string" },
  minItems: 1,
};

const TRACE_FILTER_SCHEMA: JSONObject = {
  type: "object",
  properties: {
    workflow: STRING_LIST_SCHEMA,
    agent: STRING_LIST_SCHEMA,
    model: STRING_LIST_SCHEMA,
    provider: STRING_LIST_SCHEMA,
    error_code: STRING_LIST_SCHEMA,
    auth_mode: STRING_LIST_SCHEMA,
    runtime_mode: STRING_LIST_SCHEMA,
    cache_status: STRING_LIST_SCHEMA,
    session_id: STRING_LIST_SCHEMA,
    client_user_hash: STRING_LIST_SCHEMA,
    trace_id: STRING_LIST_SCHEMA,
    member_user_id: STRING_LIST_SCHEMA,
    api_key_id: STRING_LIST_SCHEMA,
    optimization_id: STRING_LIST_SCHEMA,
    status_class: {
      type: "array",
      items: { type: "string", enum: ["2xx", "4xx", "5xx"] },
      minItems: 1,
    },
    min_cost_usd: { type: "number" },
    max_cost_usd: { type: "number" },
    min_total_tokens: { type: "number" },
    max_total_tokens: { type: "number" },
    min_latency_ms: { type: "number" },
    max_latency_ms: { type: "number" },
    has_error: { type: "boolean" },
    compressed: { type: "boolean" },
    monitor: {
      type: "object",
      properties: {
        id: { type: "string" },
        verdict: { type: "string" },
      },
      required: ["id", "verdict"],
      additionalProperties: false,
    },
  },
  additionalProperties: false,
};

export const AGENT_MCP_TOOLS: ToolDefinition[] = [
  {
    name: "caveman_context",
    description: "Read connected identity, accessible projects, and selected project. Never returns tokens or provider secrets.",
    inputSchema: { type: "object", additionalProperties: false },
    outputSchema: OBJECT_OUTPUT,
    annotations: {
      title: "Read Caveman context",
      readOnlyHint: true,
      destructiveHint: false,
      idempotentHint: true,
      openWorldHint: false,
    },
  },
  {
    name: "caveman_report",
    description: "Read one aggregate report. Keeps provider-list-price cost, inferred headroom, and verified savings separate.",
    inputSchema: {
      type: "object",
      properties: {
        report: { type: "string", enum: Object.keys(REPORT_PATHS) },
      },
      required: ["report"],
      additionalProperties: false,
    },
    outputSchema: OBJECT_OUTPUT,
    annotations: {
      title: "Read Caveman report",
      readOnlyHint: true,
      destructiveHint: false,
      idempotentHint: true,
      openWorldHint: false,
    },
  },
  {
    name: "caveman_plan",
    description: "Read ranked Cave Plan. Headline values are inferred per-day rates, never verified savings.",
    inputSchema: { type: "object", additionalProperties: false },
    outputSchema: OBJECT_OUTPUT,
    annotations: {
      title: "Read Cave Plan",
      readOnlyHint: true,
      destructiveHint: false,
      idempotentHint: true,
      openWorldHint: false,
    },
  },
  {
    name: "caveman_trace_search",
    description: "Search trace metadata and aggregate evidence. Payload content remains excluded.",
    inputSchema: {
      type: "object",
      properties: {
        filters: TRACE_FILTER_SCHEMA,
        from: { type: "string" },
        to: { type: "string" },
        date_field: { type: "string", enum: ["occurred", "updated"] },
        sort: {
          type: "object",
          properties: {
            by: { type: "string", enum: ["timestamp", "total_cost_usd", "latency_ms", "total_tokens"] },
            dir: { type: "string", enum: ["asc", "desc"] },
          },
          additionalProperties: false,
        },
        group_by: { type: "string", enum: ["session", "workflow", "model", "member"] },
        page_size: { type: "integer", minimum: 1, maximum: 500 },
        cursor: {
          type: "object",
          properties: {
            sort_value: { type: "string" },
            request_id: { type: "string" },
            group_key: { type: "string" },
          },
          required: ["sort_value"],
          additionalProperties: false,
        },
      },
      additionalProperties: false,
    },
    outputSchema: OBJECT_OUTPUT,
    annotations: {
      title: "Search Caveman traces",
      readOnlyHint: true,
      destructiveHint: false,
      idempotentHint: true,
      openWorldHint: false,
    },
  },
  {
    name: "caveman_trace_get",
    description: "Read selected-project trace metadata plus payload-stripped spans. Prompt, completion, tool, attribute, and event content is excluded.",
    inputSchema: {
      type: "object",
      properties: { trace_id: { type: "string", minLength: 1 } },
      required: ["trace_id"],
      additionalProperties: false,
    },
    outputSchema: OBJECT_OUTPUT,
    annotations: {
      title: "Read Caveman trace",
      readOnlyHint: true,
      destructiveHint: false,
      idempotentHint: true,
      openWorldHint: false,
    },
  },
  {
    name: "caveman_experiment_get",
    description: "List experiments or read experiment detail/results. Read-only.",
    inputSchema: {
      type: "object",
      properties: {
        action: { type: "string", enum: ["list", "get", "results"] },
        experiment_id: { type: "string", minLength: 1 },
      },
      required: ["action"],
      additionalProperties: false,
    },
    outputSchema: OBJECT_OUTPUT,
    annotations: {
      title: "Read Caveman experiment",
      readOnlyHint: true,
      destructiveHint: false,
      idempotentHint: true,
      openWorldHint: false,
    },
  },
];

function objectArg(value: unknown): JSONObject {
  if (!value || typeof value !== "object" || Array.isArray(value)) return {};
  return value as JSONObject;
}

function allowlistedObject(value: JSONValue, fields: Set<string>): JSONObject {
  if (!value || typeof value !== "object" || Array.isArray(value)) return {};
  const projected: JSONObject = {};
  for (const [key, fieldValue] of Object.entries(value)) {
    if (fields.has(key)) projected[key] = fieldValue;
  }
  return projected;
}

function metadataOnlyTrace(trace: JSONValue): JSONObject {
  return allowlistedObject(trace, AGENT_TRACE_FIELDS);
}

function metadataOnlySpans(value: JSONValue): JSONObject {
  if (!value || typeof value !== "object" || Array.isArray(value)) return { spans: [], timeline: [] };
  const spans = Array.isArray(value.spans)
    ? value.spans.map((span) => allowlistedObject(span, AGENT_SPAN_FIELDS))
    : [];
  const timeline = Array.isArray(value.timeline)
    ? value.timeline.map((event) => allowlistedObject(event, AGENT_TIMELINE_FIELDS))
    : [];
  return { spans, timeline };
}

function stringArg(args: JSONObject, key: string): string {
  const value = args[key];
  if (typeof value !== "string" || value.trim() === "") throw new Error(`${key} is required`);
  return value;
}

function result(value: JSONValue): ToolResult {
  const structuredContent = value && typeof value === "object" && !Array.isArray(value)
    ? value as JSONObject
    : { value };
  return {
    content: [{ type: "text", text: JSON.stringify(structuredContent) }],
    structuredContent,
  };
}

function errorResult(error: unknown): ToolResult {
  const candidate = error as { message?: unknown; code?: unknown; status?: unknown };
  const structuredContent: JSONObject = {
    error: typeof candidate.code === "string" ? candidate.code : "cave_agent_tool_failed",
    message: typeof candidate.message === "string" ? candidate.message : "Caveman tool failed.",
  };
  if (typeof candidate.status === "number") structuredContent.status = candidate.status;
  return {
    content: [{ type: "text", text: JSON.stringify(structuredContent) }],
    structuredContent,
    isError: true,
  };
}

async function withProjectQuery(client: AgentMcpClient, path: string): Promise<string> {
  if (!PROJECT_QUERY_PATHS.has(path)) return path;
  const projectId = await client.projectId();
  const query = new URLSearchParams({ project_id: projectId });
  return `${path}?${query}`;
}

function validateTraceFilters(args: JSONObject): void {
  const filters = args.filters;
  if (filters === undefined) return;
  if (!filters || typeof filters !== "object" || Array.isArray(filters)) throw new Error("filters must be an object");
  const unknown = Object.keys(filters).filter((key) => !TRACE_FILTER_KEYS.has(key));
  if (unknown.length > 0) throw new Error(`unknown trace filter(s): ${unknown.sort().join(", ")}`);
  const stringListKeys = [
    "workflow",
    "agent",
    "model",
    "provider",
    "error_code",
    "auth_mode",
    "runtime_mode",
    "cache_status",
    "session_id",
    "client_user_hash",
    "trace_id",
    "member_user_id",
    "api_key_id",
    "optimization_id",
    "status_class",
  ];
  for (const key of stringListKeys) {
    const value = filters[key];
    if (value === undefined) continue;
    if (!Array.isArray(value) || value.length === 0 || value.some((entry) => typeof entry !== "string" || entry.trim() === "")) {
      throw new Error(`filter ${key} must be a non-empty array of non-empty strings`);
    }
    if (key === "status_class" && value.some((entry) => !["2xx", "4xx", "5xx"].includes(entry as string))) {
      throw new Error("filter status_class values must be 2xx, 4xx, or 5xx");
    }
  }
  for (const key of ["min_cost_usd", "max_cost_usd", "min_total_tokens", "max_total_tokens", "min_latency_ms", "max_latency_ms"]) {
    const value = filters[key];
    if (value !== undefined && (typeof value !== "number" || !Number.isFinite(value))) {
      throw new Error(`filter ${key} must be a finite number`);
    }
  }
  for (const key of ["has_error", "compressed"]) {
    const value = filters[key];
    if (value !== undefined && typeof value !== "boolean") throw new Error(`filter ${key} must be a boolean`);
  }
  const monitor = filters.monitor;
  if (monitor !== undefined) {
    if (!monitor || typeof monitor !== "object" || Array.isArray(monitor)) throw new Error("filter monitor must be an object");
    const monitorUnknown = Object.keys(monitor).filter((key) => key !== "id" && key !== "verdict");
    if (monitorUnknown.length > 0) throw new Error(`filter monitor has unknown key(s): ${monitorUnknown.sort().join(", ")}`);
    if (typeof monitor.id !== "string" || monitor.id.trim() === "") throw new Error("filter monitor.id is required");
    if (typeof monitor.verdict !== "string" || !["pass", "fail", "error"].includes(monitor.verdict)) {
      throw new Error("filter monitor.verdict must be pass, fail, or error");
    }
  }
}

function optionalEnum(args: JSONObject, key: string, values: string[]): void {
  const value = args[key];
  if (value !== undefined && (typeof value !== "string" || !values.includes(value))) {
    throw new Error(`${key} must be one of ${values.join(", ")}`);
  }
}

function validateTraceSearch(args: JSONObject): void {
  validateTraceFilters(args);
  for (const key of ["from", "to"]) {
    const value = args[key];
    if (value !== undefined && typeof value !== "string") throw new Error(`${key} must be a string`);
  }
  optionalEnum(args, "date_field", ["occurred", "updated"]);
  optionalEnum(args, "group_by", ["session", "workflow", "model", "member"]);
  const pageSize = args.page_size;
  if (pageSize !== undefined && (!Number.isSafeInteger(pageSize) || (pageSize as number) < 1 || (pageSize as number) > 500)) {
    throw new Error("page_size must be an integer from 1 to 500");
  }
  const sort = args.sort;
  if (sort !== undefined) {
    if (!sort || typeof sort !== "object" || Array.isArray(sort)) throw new Error("sort must be an object");
    const unknown = Object.keys(sort).filter((key) => key !== "by" && key !== "dir");
    if (unknown.length > 0) throw new Error(`sort has unknown key(s): ${unknown.sort().join(", ")}`);
    optionalEnum(sort, "by", ["timestamp", "total_cost_usd", "latency_ms", "total_tokens"]);
    optionalEnum(sort, "dir", ["asc", "desc"]);
  }
  const cursor = args.cursor;
  if (cursor !== undefined) {
    if (!cursor || typeof cursor !== "object" || Array.isArray(cursor)) throw new Error("cursor must be an object");
    const unknown = Object.keys(cursor).filter((key) => key !== "sort_value" && key !== "request_id" && key !== "group_key");
    if (unknown.length > 0) throw new Error(`cursor has unknown key(s): ${unknown.sort().join(", ")}`);
    if (typeof cursor.sort_value !== "string" || cursor.sort_value === "") throw new Error("cursor.sort_value is required");
    for (const key of ["request_id", "group_key"]) {
      if (cursor[key] !== undefined && typeof cursor[key] !== "string") throw new Error(`cursor.${key} must be a string`);
    }
  }
}

async function callTool(client: AgentMcpClient, name: string, rawArgs: unknown): Promise<ToolResult> {
  const started = Date.now();
  let outcome: "ok" | "error" = "ok";
  try {
    const args = objectArg(rawArgs);
    const allowed = TOOL_ARGUMENT_KEYS[name];
    const unknown = Object.keys(args).filter((key) => !allowed?.has(key));
    if (unknown.length > 0) throw new Error(`unknown argument(s): ${unknown.sort().join(", ")}`);
    let value: JSONValue;
    switch (name) {
      case "caveman_context": {
        const [identity, projects, selectedProjectId] = await Promise.all([
          client.request("/api/v1/auth/me"),
          client.request("/api/v1/projects"),
          client.projectId(),
        ]);
        value = { identity, projects, selected_project_id: selectedProjectId };
        break;
      }
      case "caveman_report": {
        const report = stringArg(args, "report");
        const reportPath = REPORT_PATHS[report];
        if (!reportPath) throw new Error(`unknown report: ${report}`);
        value = await client.request(await withProjectQuery(client, reportPath));
        break;
      }
      case "caveman_plan": {
        value = await client.request(`/api/v1/projects/${encodeURIComponent(await client.projectId())}/cave-plan`);
        break;
      }
      case "caveman_trace_search": {
        validateTraceSearch(args);
        const projectId = await client.projectId();
        value = await client.request(`/api/v1/traces/search?${new URLSearchParams({ project_id: projectId })}`, {
          method: "POST",
          body: args,
        });
        break;
      }
      case "caveman_trace_get": {
        const traceId = encodeURIComponent(stringArg(args, "trace_id"));
        const projectQuery = `?${new URLSearchParams({ project_id: await client.projectId() })}`;
        const [trace, spans] = await Promise.all([
          client.request(`/api/v1/traces/${traceId}${projectQuery}`),
          client.request(`/api/v1/traces/${traceId}/spans${projectQuery}`),
        ]);
        value = { trace: metadataOnlyTrace(trace), spans: metadataOnlySpans(spans) };
        break;
      }
      case "caveman_experiment_get": {
        const action = stringArg(args, "action");
        if (!["list", "get", "results"].includes(action)) throw new Error(`unknown experiment read action: ${action}`);
        if (action === "list") {
          value = await client.request(await withProjectQuery(client, "/api/v1/experiments"));
          break;
        }
        const experimentId = encodeURIComponent(stringArg(args, "experiment_id"));
        const query = new URLSearchParams({ project_id: await client.projectId() });
        value = await client.request(`/api/v1/experiments/${experimentId}${action === "results" ? "/results" : ""}?${query}`);
        break;
      }
      default:
        throw new Error(`unknown tool: ${name}`);
    }
    return result(value);
  } catch (error) {
    outcome = "error";
    return errorResult(error);
  } finally {
    client.recordToolCall?.({ tool: name, result: outcome, durationMs: Date.now() - started });
  }
}

function rpcError(id: JSONValue | undefined, code: number, message: string): JSONObject {
  return { jsonrpc: "2.0", id: id ?? null, error: { code, message } };
}

async function handleMessage(
  client: AgentMcpClient,
  message: unknown,
  session: McpSessionState,
): Promise<JSONObject | undefined> {
  if (!message || typeof message !== "object" || Array.isArray(message)) return rpcError(undefined, -32600, "Invalid Request");
  const request = message as { id?: JSONValue; method?: unknown; params?: unknown };
  if (typeof request.method !== "string") return rpcError(request.id, -32600, "Invalid Request");
  if (request.method === "notifications/initialized") {
    if (session.negotiated) session.initialized = true;
    return undefined;
  }
  if (request.method.startsWith("notifications/")) return undefined;
  if (request.method === "ping") return { jsonrpc: "2.0", id: request.id ?? null, result: {} };
  if (request.method === "initialize") {
    const params = objectArg(request.params);
    const requested = typeof params.protocolVersion === "string" ? params.protocolVersion : "";
    const protocolVersion = SUPPORTED_PROTOCOL_VERSIONS.has(requested)
      ? requested
      : LATEST_PROTOCOL_VERSION;
    session.negotiated = true;
    session.initialized = false;
    return {
      jsonrpc: "2.0",
      id: request.id ?? null,
      result: {
        protocolVersion,
        capabilities: { tools: { listChanged: false } },
        serverInfo: { name: "caveman-cloud", version: "1.0.0" },
        instructions: "Read evidence first. Keep inferred headroom separate from verified savings. This MCP surface is read-only; experiment lifecycle actions require server-authoritative gates before agent exposure.",
      },
    };
  }
  if (!session.initialized) return rpcError(request.id, -32002, "Server not initialized");
  if (request.method === "tools/list") {
    return { jsonrpc: "2.0", id: request.id ?? null, result: { tools: AGENT_MCP_TOOLS } };
  }
  if (request.method === "tools/call") {
    const params = objectArg(request.params);
    if (typeof params.name !== "string") return rpcError(request.id, -32602, "Tool name is required");
    const tool = AGENT_MCP_TOOLS.find((entry) => entry.name === params.name);
    if (!tool) return rpcError(request.id, -32602, `Unknown tool: ${params.name}`);
    return {
      jsonrpc: "2.0",
      id: request.id ?? null,
      result: await callTool(client, params.name, params.arguments),
    };
  }
  return rpcError(request.id, -32601, "Method not found");
}

export async function serveAgentMcp(
  client: AgentMcpClient,
  input: Readable = process.stdin,
  output: Writable = process.stdout,
): Promise<void> {
  const lines = createInterface({ input, crlfDelay: Infinity, terminal: false });
  const session: McpSessionState = { negotiated: false, initialized: false };
  for await (const line of lines) {
    if (line.trim() === "") continue;
    let response: JSONObject | undefined;
    try {
      response = await handleMessage(client, JSON.parse(line), session);
    } catch {
      response = rpcError(undefined, -32700, "Parse error");
    }
    if (response) output.write(`${JSON.stringify(response)}\n`);
  }
}
