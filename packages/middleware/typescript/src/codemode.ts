/**
 * @caveman-ai/middleware/codemode — Provider-agnostic, zero-dependency in-process
 * Code-Mode toolset that collapses 20–150+ framework or MCP tools into 3 compact
 * meta-tools (`codemode_search_tools`, `codemode_execute` read-only, and
 * `codemode_mutate` write-capable) so `tools[]` stays static and prefix-cache-safe
 * across Anthropic, OpenAI (Chat + Responses), Gemini, Bedrock, Vertex, and MCP.
 */

export interface CodeModeToolInput {
  /** Tool name (e.g. "pull_request_list"). */
  name: string;
  /** Optional logical server/namespace (default "app"). */
  server?: string;
  /** Human-readable tool description. */
  description: string;
  /** Standard JSON Schema object for the tool's parameters. */
  inputSchema?: Record<string, unknown>;
  /** Explicit read-only hint; when omitted, conservative verb classification applies. */
  readOnly?: boolean;
  /** Optional MCP-style annotations. */
  annotations?: {
    readOnlyHint?: boolean;
    destructiveHint?: boolean;
    idempotentHint?: boolean;
  };
  /** Underlying tool executor. */
  execute: (args: Record<string, unknown>, context?: { signal?: AbortSignal }) => Promise<unknown> | unknown;
}

export interface CodeModePipelineStep {
  id?: string;
  op: "call" | "filter" | "pick" | "sort" | "slice" | "count" | "tail_lines" | "grep_lines";
  tool?: string;
  args?: Record<string, unknown>;
  for_each?: string;
  from?: string;
  where?: {
    field?: string;
    equals?: unknown;
    not_equals?: unknown;
    contains?: string;
    in?: unknown[];
  };
  fields?: string[];
  by?: string;
  desc?: boolean;
  start?: number;
  end?: number;
  lines?: number;
  pattern?: string;
  max_lines?: number;
}

export interface CodeModeToolsetOptions {
  /** Allow mutating tools inside codemode_execute (default false: read/write split). */
  allowWritesInExecute?: boolean;
  /** Max pipeline steps per execution (default 32). */
  maxSteps?: number;
  /** Max items per for_each fan-out (default 50). */
  maxFanOut?: number;
  /** Optional output truncator/compressor callback for large JSON responses. */
  compressOutput?: (encodedJson: string) => Promise<string> | string;
}

export interface CodeModeMetaTool {
  name: "codemode_search_tools" | "codemode_execute" | "codemode_mutate";
  description: string;
  inputSchema: Record<string, unknown>;
  annotations: {
    readOnlyHint: boolean;
    destructiveHint: boolean;
    idempotentHint: boolean;
  };
  execute: (args: Record<string, unknown>, context?: { signal?: AbortSignal }) => Promise<string>;
}

export interface CodeModeToolset {
  readonly tools: readonly CodeModeMetaTool[];
  readonly catalogSize: number;
  search(query: string, opts?: { server?: string; limit?: number }): {
    total_tools: number;
    matches: Array<{ tool: string; access: "read" | "write"; signature: string; description: string }>;
  };
  execute(
    args: { steps?: CodeModePipelineStep[]; tool?: string; args?: Record<string, unknown>; return_step?: string },
    opts?: { allowWrites?: boolean; signal?: AbortSignal },
  ): Promise<{ result: unknown; tool_calls: number }>;
  /** Render Anthropic Messages `tools[]` entries. */
  toAnthropicTools(): Array<{ name: string; description: string; input_schema: Record<string, unknown> }>;
  /** Render OpenAI Chat Completions `tools[]` entries. */
  toOpenAIChatTools(): Array<{ type: "function"; function: { name: string; description: string; parameters: Record<string, unknown> } }>;
  /** Render OpenAI Responses API `tools[]` entries. */
  toOpenAIResponsesTools(): Array<{ type: "function"; name: string; description: string; parameters: Record<string, unknown> }>;
  /** Render Google Gemini `tools` (`functionDeclarations`) wrapper. */
  toGeminiTools(): Array<{ functionDeclarations: Array<{ name: string; description: string; parametersJsonSchema: Record<string, unknown> }> }>;
}

const MUTATING_VERBS = new Set([
  "create", "update", "delete", "remove", "destroy", "drop", "write", "edit",
  "patch", "put", "post", "set", "merge", "close", "reopen", "approve", "cancel",
  "kill", "stop", "start", "launch", "run", "exec", "eval", "act", "click", "type",
  "push", "commit", "send", "respond", "review", "comment", "deploy", "mutate",
  "insert", "upsert", "rename", "archive", "link", "unlink", "schedule", "rerun",
]);

const READONLY_VERBS = new Set([
  "get", "list", "read", "search", "find", "describe", "inspect", "status",
  "logs", "log", "outline", "snapshot", "fetch", "query", "summary", "count",
  "view", "check", "checks", "info", "show", "diff", "recover", "retrieve",
]);

export function isReadOnlyTool(tool: CodeModeToolInput): boolean {
  if (tool.annotations?.destructiveHint === true) return false;
  if (typeof tool.annotations?.readOnlyHint === "boolean") return tool.annotations.readOnlyHint;
  if (typeof tool.readOnly === "boolean") return tool.readOnly;
  const parts = tool.name.toLowerCase().split(/[^a-z0-9]+/).filter(Boolean);
  for (const p of parts) {
    if (MUTATING_VERBS.has(p)) return false;
  }
  for (const p of parts) {
    if (READONLY_VERBS.has(p)) return true;
  }
  return false;
}

function tokenize(text: string): string[] {
  const raw = text.toLowerCase().split(/[^a-z0-9]+/).filter((t) => t.length > 0);
  const out: string[] = [];
  for (const tok of raw) {
    out.push(tok);
    if (tok.length > 3 && tok.endsWith("s") && !tok.endsWith("ss")) {
      out.push(tok.slice(0, -1));
    }
  }
  return out;
}

function schemaTypeSummary(schema: unknown): string {
  if (!schema || typeof schema !== "object") return "any";
  const m = schema as Record<string, unknown>;
  if (Array.isArray(m.enum) && m.enum.length > 0 && m.enum.length <= 5) {
    return m.enum.map((v) => JSON.stringify(String(v))).join("|");
  }
  if (m.type === "array") {
    return `${schemaTypeSummary(m.items)}[]`;
  }
  return typeof m.type === "string" && m.type ? m.type : "any";
}

function compactSignature(server: string, tool: CodeModeToolInput): string {
  const schema = tool.inputSchema ?? {};
  const props = (schema.properties && typeof schema.properties === "object" ? schema.properties : {}) as Record<string, unknown>;
  const req = new Set(Array.isArray(schema.required) ? schema.required.map(String) : []);
  const parts = Object.keys(props)
    .sort()
    .map((k) => `${k}${req.has(k) ? "" : "?"}: ${schemaTypeSummary(props[k])}`);
  return `${server}.${tool.name}({${parts.join(", ")}})`;
}

function firstSentence(text: string, maxLen = 140): string {
  const clean = text.replace(/\s+/g, " ").trim();
  const idx = clean.indexOf(". ");
  if (idx > 0 && idx < maxLen) return clean.slice(0, idx + 1);
  return clean.length > maxLen ? `${clean.slice(0, maxLen - 3)}...` : clean;
}

function splitPath(expr: string): string[] {
  return expr
    .replace(/\[/g, ".")
    .replace(/\]/g, "")
    .split(".")
    .filter(Boolean);
}

function resolvePath(expr: string, env: Record<string, unknown>): unknown {
  const trimmed = expr.trim();
  if (!trimmed.startsWith("$")) throw new Error(`reference "${expr}" must start with '$'`);
  const parts = splitPath(trimmed.slice(1));
  if (parts.length === 0) throw new Error(`empty reference "${expr}"`);
  if (!(parts[0]! in env)) throw new Error(`unknown variable $${parts[0]}`);
  let cur: unknown = env[parts[0]!];
  for (const seg of parts.slice(1)) {
    if (/^\d+$/.test(seg) && Array.isArray(cur)) {
      const idx = Number(seg);
      if (idx < 0 || idx >= cur.length) throw new Error(`index ${idx} out of range in "${expr}"`);
      cur = cur[idx];
      continue;
    }
    if (cur && typeof cur === "object" && !Array.isArray(cur) && seg in (cur as Record<string, unknown>)) {
      cur = (cur as Record<string, unknown>)[seg];
      continue;
    }
    throw new Error(`field "${seg}" missing in "${expr}"`);
  }
  return cur;
}

function resolveValue(val: unknown, env: Record<string, unknown>): unknown {
  if (typeof val === "string" && val.startsWith("$")) return resolvePath(val, env);
  if (Array.isArray(val)) return val.map((item) => resolveValue(item, env));
  if (val && typeof val === "object") {
    const out: Record<string, unknown> = {};
    for (const [k, v] of Object.entries(val as Record<string, unknown>)) {
      out[k] = resolveValue(v, env);
    }
    return out;
  }
  return val;
}

function lookupField(obj: unknown, path: string): unknown {
  let cur = obj;
  for (const seg of splitPath(path)) {
    if (!cur || typeof cur !== "object" || Array.isArray(cur)) return undefined;
    cur = (cur as Record<string, unknown>)[seg];
  }
  return cur;
}

export function createCodeModeToolset(
  inputTools: readonly CodeModeToolInput[],
  options: CodeModeToolsetOptions = {},
): CodeModeToolset {
  const maxSteps = options.maxSteps ?? 32;
  const maxFanOut = options.maxFanOut ?? 50;
  const allowWritesInExecute = options.allowWritesInExecute ?? false;

  const catalog = [...inputTools]
    .map((t) => ({
      server: (t.server?.trim() || "app"),
      tool: t,
      key: `${t.server?.trim() || "app"}.${t.name}`,
      readOnly: isReadOnlyTool(t),
    }))
    .sort((a, b) => a.key.localeCompare(b.key));

  const byKey = new Map(catalog.map((entry) => [entry.key, entry]));
  const docTokens = catalog.map((entry) => {
    const props = entry.tool.inputSchema?.properties;
    const propKeys = props && typeof props === "object" ? Object.keys(props).join(" ") : "";
    return tokenize(`${entry.server} ${entry.tool.name.replace(/_/g, " ")} ${entry.tool.name} ${entry.tool.description} ${propKeys}`);
  });
  const avgLen = docTokens.reduce((sum, d) => sum + d.length, 0) / Math.max(1, docTokens.length);
  const df = new Map<string, number>();
  for (const tokens of docTokens) {
    for (const unique of new Set(tokens)) {
      df.set(unique, (df.get(unique) ?? 0) + 1);
    }
  }

  function search(query: string, opts?: { server?: string; limit?: number }) {
    const limit = Math.max(1, Math.min(25, opts?.limit ?? 8));
    const qTokens = tokenize(query);
    const serverFilter = opts?.server?.trim().toLowerCase() ?? "";
    const N = catalog.length;
    const scored: Array<{ idx: number; score: number }> = [];

    for (let i = 0; i < catalog.length; i++) {
      const entry = catalog[i]!;
      if (serverFilter && entry.server.toLowerCase() !== serverFilter) continue;
      if (qTokens.length === 0) {
        scored.push({ idx: i, score: 1 });
        continue;
      }
      const d = docTokens[i]!;
      const tf = new Map<string, number>();
      for (const t of d) tf.set(t, (tf.get(t) ?? 0) + 1);
      let score = 0;
      for (const qt of qTokens) {
        const f = tf.get(qt) ?? 0;
        if (f === 0) continue;
        const n = df.get(qt) ?? 0;
        const idf = Math.log((N - n + 0.5) / (n + 0.5) + 1);
        score += idf * ((f * 2.5) / (f + 1.5 * (0.25 + 0.75 * (d.length / Math.max(1, avgLen)))));
      }
      if (score > 0) scored.push({ idx: i, score });
    }
    scored.sort((a, b) => (b.score !== a.score ? b.score - a.score : a.idx - b.idx));
    return {
      total_tools: catalog.length,
      matches: scored.slice(0, limit).map(({ idx }) => {
        const entry = catalog[idx]!;
        return {
          tool: entry.key,
          access: (entry.readOnly ? "read" : "write") as "read" | "write",
          signature: compactSignature(entry.server, entry.tool),
          description: firstSentence(entry.tool.description),
        };
      }),
    };
  }

  async function invokeOne(fullTool: string, args: Record<string, unknown>, allowWrites: boolean, signal?: AbortSignal): Promise<unknown> {
    const entry = byKey.get(fullTool);
    if (!entry) throw new Error(`unknown tool "${fullTool}"`);
    if (!allowWrites && !entry.readOnly) {
      throw new Error(`tool "${fullTool}" is state-changing; call codemode_mutate instead of read-only codemode_execute`);
    }
    return entry.tool.execute(args, signal ? { signal } : undefined);
  }

  async function execute(
    input: { steps?: CodeModePipelineStep[]; tool?: string; args?: Record<string, unknown>; return_step?: string },
    opts?: { allowWrites?: boolean; signal?: AbortSignal },
  ): Promise<{ result: unknown; tool_calls: number }> {
    const allowWrites = opts?.allowWrites ?? allowWritesInExecute;
    let steps = input.steps ?? [];
    if (steps.length === 0 && typeof input.tool === "string" && input.tool.trim()) {
      steps = [{ id: "call_1", op: "call", tool: input.tool.trim(), args: input.args ?? {} }];
    }
    if (steps.length === 0) throw new Error("steps or tool must be provided");
    if (steps.length > maxSteps) throw new Error(`max ${maxSteps} steps per execution`);

    const env: Record<string, unknown> = {};
    let toolCalls = 0;
    let lastId = "";

    for (let i = 0; i < steps.length; i++) {
      const step = steps[i]!;
      const id = step.id?.trim() || `step_${i + 1}`;
      lastId = id;
      let val: unknown;

      switch (step.op) {
        case "call": {
          if (!step.tool) throw new Error(`step ${id}: tool is required`);
          if (step.for_each) {
            const src = resolvePath(step.for_each, env);
            if (!Array.isArray(src)) throw new Error(`step ${id}: for_each target is not an array`);
            const items = src.slice(0, maxFanOut);
            const results: unknown[] = [];
            for (const item of items) {
              const resolved = resolveValue(step.args ?? {}, { ...env, item }) as Record<string, unknown>;
              results.push(await invokeOne(step.tool, resolved, allowWrites, opts?.signal));
              toolCalls++;
            }
            val = results;
          } else {
            const resolved = resolveValue(step.args ?? {}, env) as Record<string, unknown>;
            val = await invokeOne(step.tool, resolved, allowWrites, opts?.signal);
            toolCalls++;
          }
          break;
        }
        case "filter": {
          const src = resolvePath(step.from ?? "", env);
          if (!Array.isArray(src)) throw new Error(`step ${id}: filter requires an array`);
          const w = step.where;
          val = !w
            ? src
            : src.filter((item) => {
                const fieldVal = w.field ? lookupField(item, w.field) : item;
                const s = String(fieldVal ?? "");
                if (w.equals !== undefined && String(w.equals) !== s) return false;
                if (w.not_equals !== undefined && String(w.not_equals) === s) return false;
                if (w.contains && !s.toLowerCase().includes(w.contains.toLowerCase())) return false;
                if (w.in && w.in.length > 0 && !w.in.some((c) => String(c) === s)) return false;
                return true;
              });
          break;
        }
        case "pick": {
          const src = resolvePath(step.from ?? "", env);
          const fields = step.fields ?? [];
          const pickOne = (obj: unknown) => {
            if (!obj || typeof obj !== "object" || Array.isArray(obj)) return obj;
            const out: Record<string, unknown> = {};
            for (const f of fields) out[f] = lookupField(obj, f);
            return out;
          };
          val = Array.isArray(src) ? src.map(pickOne) : pickOne(src);
          break;
        }
        case "sort": {
          const src = resolvePath(step.from ?? "", env);
          if (!Array.isArray(src)) throw new Error(`step ${id}: sort requires an array`);
          val = [...src].sort((a, b) => {
            const va = step.by ? lookupField(a, step.by) : a;
            const vb = step.by ? lookupField(b, step.by) : b;
            const cmp = typeof va === "number" && typeof vb === "number"
              ? va - vb
              : String(va ?? "").localeCompare(String(vb ?? ""));
            return step.desc ? -cmp : cmp;
          });
          break;
        }
        case "slice": {
          const src = resolvePath(step.from ?? "", env);
          if (!Array.isArray(src)) throw new Error(`step ${id}: slice requires an array`);
          val = src.slice(step.start ?? 0, step.end);
          break;
        }
        case "count": {
          const src = resolvePath(step.from ?? "", env);
          val = Array.isArray(src) ? src.length : (src && typeof src === "object" ? Object.keys(src).length : 0);
          break;
        }
        case "tail_lines": {
          const src = resolvePath(step.from ?? "", env);
          const text = typeof src === "string" ? src : JSON.stringify(src, null, 2);
          const lines = text.split(/\r?\n/);
          const n = Math.max(1, step.lines ?? 50);
          val = lines.slice(-n).join("\n");
          break;
        }
        case "grep_lines": {
          const src = resolvePath(step.from ?? "", env);
          const re = new RegExp(step.pattern ?? "", "i");
          const maxL = Math.max(1, step.max_lines ?? 100);
          const text = Array.isArray(src)
            ? src.map((x) => (typeof x === "string" ? x : JSON.stringify(x))).join("\n")
            : (typeof src === "string" ? src : JSON.stringify(src));
          val = text.split(/\r?\n/).filter((l) => re.test(l)).slice(0, maxL).join("\n");
          break;
        }
        default:
          throw new Error(`step ${id}: unsupported op "${String(step.op)}"`);
      }
      env[id] = val;
    }

    const retId = input.return_step?.trim() || lastId;
    if (!(retId in env)) throw new Error(`return_step "${retId}" not found`);
    return { result: env[retId], tool_calls: toolCalls };
  }

  const metaTools: CodeModeMetaTool[] = [
    {
      name: "codemode_search_tools",
      description: "Search connected tools by keyword and return compact typed signatures plus read/write access tags.",
      inputSchema: {
        type: "object",
        properties: {
          query: { type: "string", description: "Keywords describing the action or resource." },
          server: { type: "string", description: "Optional server/namespace filter." },
          limit: { type: "integer", description: "Max tools to return (default 8, max 25)." },
        },
        required: ["query"],
      },
      annotations: { readOnlyHint: true, destructiveHint: false, idempotentHint: true },
      async execute(args) {
        return JSON.stringify(search(String(args.query ?? ""), {
          ...(typeof args.server === "string" ? { server: args.server } : {}),
          ...(typeof args.limit === "number" ? { limit: args.limit } : {}),
        }));
      },
    },
    {
      name: "codemode_execute",
      description: "Run a read-only multi-step tool pipeline in one turn (call, for_each, filter, pick, sort, slice, count, tail_lines, grep_lines). Use codemode_mutate for state-changing calls.",
      inputSchema: {
        type: "object",
        properties: {
          steps: { type: "array", items: { type: "object" }, description: "Ordered pipeline steps." },
          return_step: { type: "string", description: "Step id whose value to return (defaults to last step)." },
        },
        required: ["steps"],
      },
      annotations: { readOnlyHint: true, destructiveHint: false, idempotentHint: true },
      async execute(args, ctx) {
        const out = await execute(args as Parameters<typeof execute>[0], { allowWrites: allowWritesInExecute, ...(ctx?.signal ? { signal: ctx.signal } : {}) });
        const encoded = JSON.stringify(out);
        return options.compressOutput ? options.compressOutput(encoded) : encoded;
      },
    },
    {
      name: "codemode_mutate",
      description: "Execute state-changing / write tool call(s) via single `tool`+`args` or multi-step `steps`.",
      inputSchema: {
        type: "object",
        properties: {
          tool: { type: "string", description: "Single mutating tool name (`server.tool_name`)." },
          args: { type: "object", description: "Arguments for single-tool mode." },
          steps: { type: "array", items: { type: "object" }, description: "Optional multi-step pipeline with writes permitted." },
          return_step: { type: "string", description: "Step id to return." },
        },
      },
      annotations: { readOnlyHint: false, destructiveHint: true, idempotentHint: false },
      async execute(args, ctx) {
        const out = await execute(args as Parameters<typeof execute>[0], { allowWrites: true, ...(ctx?.signal ? { signal: ctx.signal } : {}) });
        const encoded = JSON.stringify(out);
        return options.compressOutput ? options.compressOutput(encoded) : encoded;
      },
    },
  ];

  return {
    tools: metaTools,
    catalogSize: catalog.length,
    search,
    execute,
    toAnthropicTools() {
      return metaTools.map((t) => ({ name: t.name, description: t.description, input_schema: t.inputSchema }));
    },
    toOpenAIChatTools() {
      return metaTools.map((t) => ({ type: "function" as const, function: { name: t.name, description: t.description, parameters: t.inputSchema } }));
    },
    toOpenAIResponsesTools() {
      return metaTools.map((t) => ({ type: "function" as const, name: t.name, description: t.description, parameters: t.inputSchema }));
    },
    toGeminiTools() {
      return [{ functionDeclarations: metaTools.map((t) => ({ name: t.name, description: t.description, parametersJsonSchema: t.inputSchema })) }];
    },
  };
}
