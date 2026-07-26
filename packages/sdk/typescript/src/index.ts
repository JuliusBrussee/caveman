/** `user` is an opaque end-user identifier sent as x-cave-user-hash (never a
 *  Caveman member id). The SDK forwards it verbatim — hash it yourself before
 *  passing if the raw value is PII. Mirrors the Python `Cave.user`. */
export type CaveOptions = { apiKey: string; baseURL: string; agent: string; defaultWorkflow?: string; retention?: "metadata" | "zdr" | "configured"; verifyOnInit?: boolean; controlURL?: string; user?: string };
export type TraceOptions = { workflow?: string; tags?: Record<string, string> };
export type ToolOptions = { readOnly?: boolean; idempotent?: boolean; artifactEligible?: boolean };

/** A tool descriptor for inclusion in the tool catalog sent to the gateway. */
export type CaveTool = { name: string; description: string; inputSchema: unknown; handler: (...args: unknown[]) => unknown; tags?: string[]; readOnly: boolean; idempotent: boolean; alwaysLoad?: boolean };

/** Response from a server-side tool-search call. */
export type ToolSearchResult = {
  /** Server-side tool-session id for later provider-request reinjection. */
  sessionId?: string;
  /** Reduced tool list returned by the gateway for this query. */
  tools: CaveTool[];
  /** Estimated tokens for the schemas the gateway actually sent (reduced set). */
  sentSchemaTokens: number;
  /** Estimated tokens if ALL tool schemas had been sent (full catalog). */
  fullSchemaTokens: number;
  /** Number of tools whose schemas were deferred (not sent). */
  deferredCount: number;
  /** Search method used by the gateway ("bm25" by default, "embeddings" if wired). */
  method: string;
  /** Counting method for both schema-token estimates. */
  tokenBasis: string;
  /** Always inferred: the full-catalog counterfactual is not provider-observed. */
  basis: "inferred";
  /** Estimated tokens avoided vs. sending the full catalog. */
  readonly savedTokens: number;
  /** Estimated percentage avoided (0–100). */
  readonly reductionPct: number;
};

/** Options for {@link Cave.compress}. */
export type CompressOptions = {
  /** Optional content-type hint for the engine's detector, including forced "toon". */
  contentType?: "json" | "toon" | "log" | "code" | "diff" | "search-result" | "text" | "toolschema" | string;
};

/**
 * Result of a {@link Cave.compress} call — the Engine's compression report.
 *
 * `basis` is always `"inferred"`: the SDK never emits `verified` (that is earned
 * only by the Cloud `active` path). On any transport/parse problem the call is a
 * fail-closed pass-through (`output` is the original input, `ratio` is `0`, and
 * `recoveryHandle` is absent).
 */
export type CompressResult = {
  /** The compressed payload, or the original input verbatim on pass-through. */
  output: string;
  /** The detected/declared content type ("json" | "log" | "code" | "text" | "unknown"). */
  contentType: string;
  /** Estimated tokens for the original input (0 when not computed / pass-through). */
  tokensBefore: number;
  /** Estimated tokens for the compressed output (0 when not computed / pass-through). */
  tokensAfter: number;
  /** Fraction of tokens removed (`(tokensBefore - tokensAfter) / tokensBefore`); `0` = unchanged. */
  ratio: number;
  /** Always `"inferred"` — the SDK never reports `verified`. */
  basis: "inferred";
  /** Token counter used by the Engine (e.g. `o200k_base` or `approx_chars_div_4`). */
  tokenCountBasis: string;
  /** Handle to recover the byte-exact original; absent when nothing was stored. */
  recoveryHandle?: string;
  /** Concrete compression method chosen by the engine, e.g. "toon" or "elision". */
  method?: string;
  /** True when model-visible output kept the full value with no data dropped. */
  losslessToModel?: boolean;
};

// ─── Cave Plan (agent-readable) types ─────────────────────────────────────────
// Field names are the snake_case wire names — identical to the control-api JSON
// and the Python `cave_plan()`. Every dollar figure is `inferred` and a PER-DAY
// rate; the SDK passes them through verbatim and never re-projects them.

/** One safety-class band of the plan's headroom (inferred, per-day USD). */
export interface CavePlanHeadroomClass {
  safety_class: string;
  low: number;
  base: number;
  high: number;
  move_count: number;
}

/** The plan headline: summed inferred per-day headroom across all moves. */
export interface CavePlanHeadline {
  low: number;
  base: number;
  high: number;
  /** Always `"inferred"` — the plan headline is never a verified figure. */
  basis: string;
  move_count: number;
}

/** One (agent/workflow/provider/model) scope's share of a move's base headroom. */
export interface CavePlanScopeShare {
  agent_id?: string;
  workflow_id?: string;
  agent_name?: string;
  workflow_name?: string;
  provider?: string;
  model?: string;
  savings_usd_base: number;
  /** Share of the move's base headroom, 0–100. */
  share_pct: number;
}

/** One recommended optimizer move. Dollar figures are inferred, per-day. */
export interface CavePlanMove {
  optimizer_id: string;
  title: string;
  family: string;
  safety_class: string;
  basis: string;
  confidence: string;
  quality_risk: string;
  implementation_effort: string;
  requires_eval_gate: boolean;
  scope_count: number;
  sample_size: number;
  savings_usd_low: number;
  savings_usd_base: number;
  savings_usd_high: number;
  share_of_headline_pct: number;
  next_action: string;
  summary: string;
  /** Present when the move's gateway optimizer is already active (residual signal). */
  already_active_note?: string;
  /** Up to 3 driving scopes, by `savings_usd_base` desc. */
  top_scopes?: CavePlanScopeShare[];
}

/** An optimizer with no current signal, plus the plain-language reason why. */
export interface CavePlanNoSignal {
  optimizer_id: string;
  title: string;
  reason_code: string;
  reason: string;
}

/**
 * The project-scope Cave Plan, read machine-readably via `GET /sdk/v1/cave-plan`
 * (see {@link Cave.cavePlan}). Passed through verbatim from control-api: field
 * names are the snake_case wire names, identical to the Python `cave_plan()`.
 * Every dollar figure is `inferred` and a PER-DAY rate — never `verified`,
 * never re-projected to a month.
 */
export interface CavePlan {
  headline: CavePlanHeadline;
  headroom_by_class: CavePlanHeadroomClass[];
  moves: CavePlanMove[];
  no_signal: CavePlanNoSignal[];
  methodology: string;
  as_of?: string;
  detectors_last_ran?: string;
  diagnostics?: string[];
  mode_note?: string;
  /** Always `"project"` on this surface (the SDK reads one project's plan). */
  scope: string;
  project_id: string;
}

/**
 * The single human-editable object controlling cave-auto routing for a workflow
 * (spec R14). Field names are the snake_case wire names — identical to the Go
 * `policy.TaskProfile` JSON tags and the sdk-python `TaskProfile` dataclass.
 * Editing one is a policy publish; there is no ML in the loop.
 *
 * `alpha` is the 0–10 cost/quality dial (0 = most capable in the passing set,
 * 10 = cheapest). Every field is optional; an absent profile means baseline
 * pass-through.
 */
export interface TaskProfile {
  /** Minimum grader pass rate (LCB) a candidate must hold to be eligible. */
  quality_floor?: number;
  /** Cost/quality dial, 0 (most capable) – 10 (cheapest). */
  alpha?: number;
  /** Allowlist patterns ("provider/*", "*:model", "provider:model", "model"). */
  candidate_allowlist?: string[];
  /** Denylist patterns (same grammar as the allowlist). */
  candidate_denylist?: string[];
  /** Max tolerated p95 latency increase vs baseline, in milliseconds. */
  max_p95_latency_delta_ms?: number;
  /** Max tolerated error-rate increase vs baseline. */
  max_error_delta?: number;
  /** Max tolerated cost ratio (routed / baseline). */
  max_cost_ratio?: number;
  /** Enable FrugalGPT-style cascade escalation. */
  cascade_enabled?: boolean;
  /** Confidence threshold below which the cascade escalates a rung. */
  cascade_tau?: number;
  /** Max tolerated cascade escalation rate. */
  max_escalation_rate?: number;
  /** Session stickiness mode ("conversation" | "none" | "key"). */
  stickiness?: string;
  /** Allow routing across providers (not just within the request's provider). */
  cross_provider?: boolean;
  /** Data-residency region tags a candidate must satisfy. */
  data_residency?: string[];
  /** Trusted request hint sources permitted to influence the route. */
  trusted_route_hints?: string[];
}

/** Fields for a span recorded via the SDK-bundled OTel exporter. */
export type SpanOptions = {
  traceId?: string;
  spanId?: string;
  parentSpanId?: string;
  kind?: number;
  provider?: string;
  model?: string;
  operation?: string;
  toolName?: string;
  /** Provider-reported total input tokens, inclusive of cached input. */
  inputTokens?: number;
  /** Provider-reported total output tokens, inclusive of reasoning output. */
  outputTokens?: number;
  /** Provider-reported cached-input subset; never added to inputTokens. */
  cachedTokens?: number;
  /** Provider-reported/request-attributed cost in USD. */
  costUsd?: number;
  workflow?: string;
  status?: "unset" | "ok" | "error";
  startTimeNs?: number;
  endTimeNs?: number;
  attributes?: Record<string, string | number | boolean>;
};

/** A span buffered by the OTel exporter (mirrors the OTLP/JSON span shape). */
export type OTelSpan = {
  name: string;
  traceId: string;
  spanId: string;
  parentSpanId: string;
  kind: number;
  startTimeNs: number;
  endTimeNs: number;
  statusCode: number; // 0=unset, 1=ok, 2=error
  attributes: Record<string, string | number | boolean>;
};

export class Cave {
  readonly options: CaveOptions;

  constructor(options: CaveOptions) {
    if (!options.apiKey || !options.baseURL || !options.agent) throw new Error("apiKey, baseURL, and agent are required");
    // CAVE_WORKFLOW lets a wrapper (`cave wrap --workflow x`) label every request
    // from an SDK app without a code change. An explicit option always wins.
    // globalThis lookup keeps the SDK browser-neutral (no node type dependency).
    // The env value is normalized to the gateway's label rule (lowercase
    // [a-z0-9_-], max 96) — an invalid ambient value is ignored rather than
    // 400-ing every request. Mirrors caveman_cloud (Python).
    const envRaw = (globalThis as { process?: { env?: Record<string, string | undefined> } }).process?.env?.["CAVE_WORKFLOW"]?.toLowerCase();
    const envWorkflow = envRaw && /^[a-z0-9_-]{1,96}$/.test(envRaw) ? envRaw : undefined;
    this.options = !options.defaultWorkflow && envWorkflow ? { ...options, defaultWorkflow: envWorkflow } : options;
  }

  async trace<T>(options: TraceOptions, fn: (trace: CaveTrace) => Promise<T>): Promise<T> {
    return fn(new CaveTrace(this, options.workflow ?? this.options.defaultWorkflow ?? "unlabeled-workflow", options.tags ?? {}));
  }

  openai(config: { upstreamKey?: string } = {}) {
    return providerClient(this, "openai", "/openai/v1", config.upstreamKey);
  }

  anthropic(config: { upstreamKey?: string } = {}) {
    return providerClient(this, "anthropic", "/anthropic", config.upstreamKey);
  }

  gemini(config: { upstreamKey?: string } = {}) {
    return providerClient(this, "gemini", "/gemini", config.upstreamKey);
  }

  /**
   * Describe the first-party Bedrock gateway surface for an AWS SDK or agent
   * wrapper. Runtime is the default; Mantle remains an explicit opt-in lane.
   *
   * This descriptor performs no network request and carries no AWS secret.
   */
  bedrock(config: { region: string; endpoint?: "runtime" | "mantle" }) {
    const endpoint = config.endpoint ?? "runtime";
    if (endpoint !== "runtime" && endpoint !== "mantle") {
      throw new Error("bedrock endpoint must be runtime or mantle");
    }
    return {
      region: config.region,
      endpoint,
      gatewayPrefix: endpoint === "mantle" ? "/bedrock/anthropic" : "/bedrock",
      instrumented: true,
      sdkOnly: false,
    };
  }

  /**
   * Vertex AI client proxied through the gateway (`/vertex` prefix). Routes
   * Google Gemini and Anthropic Claude calls through Caveman for metering.
   *
   * `upstreamKey` is a Google OAuth2 access token (e.g. from
   * `gcloud auth print-access-token` or Application Default Credentials); the
   * gateway forwards it as `Authorization: Bearer …`. Use the returned client's
   * `raw` fetch for native Vertex paths
   * (`/vertex/v1/projects/.../models/{model}:generateContent`).
   */
  vertex(config: { upstreamKey?: string } = {}) {
    return providerClient(this, "vertex", "/vertex", config.upstreamKey);
  }

  /**
   * Create a one-call OTel exporter bound to this Cave's gateway config.
   *
   * The exporter ships spans to the gateway's OTLP endpoint
   * (`POST {baseURL}/otlp/v1/traces`) with the Caveman headers
   * (`x-cave-api-key` / `x-cave-agent` / `x-cave-workflow`) so that
   * `recordSpan()` + `export()` land rows in `caveman.spans` without any
   * external OpenTelemetry wiring.
   *
   * @param config.serviceName `service.name` resource attribute; defaults to
   *        the Cave agent slug (the gateway falls back to it for the agent label).
   */
  exporter(config: { serviceName?: string } = {}): OTelExporter {
    return new OTelExporter(this, config.serviceName ?? this.options.agent);
  }

  /**
   * Build a tool-catalog handle with server-side search via /sdk/v1/tool-search.
   *
   * strategy "all"      → initial = whole catalog; search() still calls the server.
   * strategy "deferred" → initial = alwaysLoad tools (subset sent on first turn);
   *                       search() calls the server to load relevant tools on demand.
   *
   * Breaking change from 1.0: search() is now async and returns ToolSearchResult
   * (previously sync, returning CaveTool[]). Callers must await search().
   */
  tools(config: { catalog: CaveTool[]; strategy?: "all" | "deferred"; initialToolCount?: number; maxLoadedTools?: number }) {
    const strategy = config.strategy ?? "all";
    const initial = strategy === "all" ? config.catalog : config.catalog.filter((tool) => tool.alwaysLoad).concat(config.catalog.slice(0, config.initialToolCount ?? 8));
    const cave = this;

    return {
      strategy,
      initial,
      /**
       * Search the full catalog via the gateway's /sdk/v1/tool-search endpoint.
       * Returns a ToolSearchResult with the reduced tool list and token counts.
       *
       * @param query   The user's current intent / task description.
       * @param options.maxTools Optional cap on returned tools.
       * @param options.context  Optional extra context for the gateway ranker.
       * @param options.workflow Override workflow label.
       * @param options.ranker   Optional ranking algorithm passed through to the
       *        gateway ("bm25" default, or "embeddings" when the gateway has an
       *        embedding provider wired). The SDK passes it through; it never
       *        computes similarity itself.
       */
      search: async (
        query: string,
        options?: { maxTools?: number; context?: string; workflow?: string; ranker?: "bm25" | "embeddings"; toolSessionId?: string }
      ): Promise<ToolSearchResult> => {
        return toolSearch(cave, config.catalog, query, options);
      }
    };
  }

  /**
   * Directly call the gateway's /sdk/v1/tool-search endpoint with the given catalog.
   * Use this when you manage the catalog outside of a tools() handle.
   */
  async toolSearch(
    catalog: CaveTool[],
    query: string,
    options?: { maxTools?: number; context?: string; workflow?: string; ranker?: "bm25" | "embeddings"; toolSessionId?: string }
  ): Promise<ToolSearchResult> {
    return toolSearch(this, catalog, query, options);
  }

  /**
   * Compress a payload through the Engine (`POST /sdk/v1/compress`), returning
   * the Engine's report. The SDK is **not** the compressor — it delegates and
   * maps the result; it never reimplements a compressor (that would fork
   * behavior across surfaces).
   *
   * **Byte-safe.** On any transport or parse problem the call passes through:
   * `output` is the original `payload` unchanged, `ratio` is `0`, and there is
   * no `recoveryHandle`. The SDK never rewrites bytes itself and never claims a
   * saving it did not get back from the Engine. `basis` is always `"inferred"`.
   *
   * Mirrors the Python `Cave.compress`.
   */
  async compress(payload: string, options?: CompressOptions): Promise<CompressResult> {
    return compress(this, payload, options);
  }

  /**
   * Read this project's Cave Plan machine-readably (`GET /sdk/v1/cave-plan`),
   * authed by the project key's `plan:read` scope. Returns the project-scope
   * plan verbatim — snake_case wire fields identical to control-api and the
   * Python `cave_plan()`. Every dollar figure is `inferred` and a PER-DAY rate;
   * the SDK never re-derives or re-projects them (no monthly, no `verified`).
   *
   * This is a control-plane read served by control-api, so it targets
   * `controlURL` (falling back to `baseURL`) and carries the `x-cave-api-key`
   * project key — not the gateway `authorization: Bearer` header. On a non-200
   * it throws (mirrors {@link Cave.toolSearch}); there is no byte-safe
   * pass-through here (this reads state, it never touches request bytes).
   *
   * Mirrors the Python `Cave.cave_plan`.
   */
  async cavePlan(): Promise<CavePlan> {
    return cavePlan(this);
  }

  prompts = {
    internalBrevity: ({ style, preserveErrorsVerbatim, preserveCodeVerbatim }: { style: "technical-concise" | "caveman" | "none"; preserveErrorsVerbatim?: boolean; preserveCodeVerbatim?: boolean }) =>
      style === "none" ? "" : `Internal output style: ${style}. Preserve errors verbatim: ${Boolean(preserveErrorsVerbatim)}. Preserve code verbatim: ${Boolean(preserveCodeVerbatim)}.`
  };

  /**
   * Session-keyed multi-agent shared context. One agent `put`s the full handoff
   * context under a session key (`POST /sdk/v1/shared-context`); a peer agent in the
   * same project `get`s it back byte-exact (`GET /sdk/v1/shared-context/{key}`). The
   * gateway is tenant-scoped — the project namespaces the key, so a peer in another
   * project cannot read it. Mirrors the Python `cave.shared_context`.
   */
  sharedContext = {
    put: async (sessionKey: string, content: string): Promise<Record<string, unknown>> =>
      request(this, "/sdk/v1/shared-context", { session_key: sessionKey, content }),
    get: async (sessionKey: string): Promise<Record<string, unknown>> =>
      request(this, `/sdk/v1/shared-context/${encodeURIComponent(sessionKey)}`)
  };

  /** Reserved async-job surface; methods fail locally without network I/O. */
  jobs = new JobsClient(this);

  /**
   * A fresh retry-loop breaker that interrupts a repeated identical tool-call
   * loop after `threshold` consecutive repeats. Mirrors the Python
   * `Cave.retry_loop_breaker`.
   */
  retryLoopBreaker(threshold = 3): RetryLoopBreaker {
    return new RetryLoopBreaker(threshold);
  }
}

// ─── Retry-loop breaker ───────────────────────────────────────────────────────

/** Thrown by {@link RetryLoopBreaker} when an identical tool call repeats past the threshold. */
export class RetryLoopError extends Error {
  constructor(readonly signature: string, readonly repeats: number, readonly threshold: number) {
    super(`retry loop interrupted: tool call ${JSON.stringify(signature)} repeated ${repeats} times (threshold ${threshold})`);
    this.name = "RetryLoopError";
  }
}

/**
 * Detects and interrupts a repeated identical tool-call loop.
 *
 * Call `record()` (or `guard()`) before each tool invocation with the tool name
 * and arguments. When the SAME (name, arguments) signature repeats consecutively
 * more than `threshold` times, `record()` throws {@link RetryLoopError}. Any
 * different call resets the streak. Mirrors the Python `RetryLoopBreaker`
 * (same field names + threshold semantics: fires on the `threshold + 1`-th
 * consecutive identical call).
 */
export class RetryLoopBreaker {
  private lastSignature: string | null = null;
  private repeats = 0;

  constructor(readonly threshold = 3) {}

  /** Canonical signature for a tool call (name + sorted-key JSON args). */
  signature(name: string, args: unknown): string {
    let serialized: string;
    try {
      serialized = stableStringify(args);
    } catch {
      serialized = String(args);
    }
    return `${name}(${serialized})`;
  }

  /**
   * Record a tool call. Throws {@link RetryLoopError} once an identical call has
   * repeated past the threshold. A different call resets the streak.
   */
  record(name: string, args: unknown): void {
    const sig = this.signature(name, args);
    if (sig === this.lastSignature) {
      this.repeats += 1;
    } else {
      this.lastSignature = sig;
      this.repeats = 1;
    }
    if (this.repeats > this.threshold) {
      throw new RetryLoopError(sig, this.repeats, this.threshold);
    }
  }

  /** Record the call (may throw) then invoke `fn`. */
  async guard<T>(name: string, args: unknown, fn: () => Promise<T> | T): Promise<T> {
    this.record(name, args);
    return fn();
  }

  /** Clear the streak (e.g. when starting a new task). */
  reset(): void {
    this.lastSignature = null;
    this.repeats = 0;
  }
}

// ─── Async job client ─────────────────────────────────────────────────────────

/** Scheduling hint for an async job. */
export type LatencyClass = "interactive" | "background" | "offline";

/** Reserved async-job result shape for future durable execution support. */
export type Job = { id: string; state: string; raw: Record<string, unknown> };

const ASYNC_JOBS_UNAVAILABLE = "Async job execution is unavailable: durable encrypted request storage, provider credential custody, and a draining worker are not wired. No job was submitted.";

export class AsyncJobsUnavailableError extends Error {
  readonly code = "cave_async_jobs_unavailable";

  constructor() {
    super(ASYNC_JOBS_UNAVAILABLE);
    this.name = "AsyncJobsUnavailableError";
  }
}

/**
 * Reserved async-job surface. Every method fails locally before network I/O
 * until delayed requests have durable payload, credential, and worker support.
 * Mirrors Python `JobsClient` and prevents fake queued/completed work.
 */
export class JobsClient {
  constructor(_cave: Cave) {}

  async submit(_body: Record<string, unknown>, _options?: { latencyClass?: LatencyClass }): Promise<Job> {
    throw new AsyncJobsUnavailableError();
  }

  async status(_id: string): Promise<Job> {
    throw new AsyncJobsUnavailableError();
  }

  async cancel(_id: string): Promise<Record<string, unknown>> {
    throw new AsyncJobsUnavailableError();
  }

  async wait(_id: string, _options?: { intervalMs?: number; timeoutMs?: number }): Promise<Job> {
    throw new AsyncJobsUnavailableError();
  }

  async submitAndWait(
    _body: Record<string, unknown>,
    _options?: { latencyClass?: LatencyClass; intervalMs?: number; timeoutMs?: number }
  ): Promise<Job> {
    throw new AsyncJobsUnavailableError();
  }
}

export class CaveTrace {
  constructor(private cave: Cave, private workflow: string, private tags: Record<string, string>) {}

  async tool<T>(name: string, options: ToolOptions, fn: () => Promise<T> | T): Promise<T> {
    const start = Date.now();
    try {
      return await fn();
    } finally {
      await request(this.cave, "/sdk/v1/events", { span_type: "tool.call", name, workflow: this.workflow, options, duration_ms: Date.now() - start, tags: this.tags }).catch(() => undefined);
    }
  }

  model = {
    openai: {
      responses: { create: (body: unknown, init?: { cave?: { latencyClass?: string } }) => providerFetch(this.cave, "/openai/v1/responses", body, this.workflow, init?.cave) },
      chat: { completions: { create: (body: unknown) => providerFetch(this.cave, "/openai/v1/chat/completions", body, this.workflow) } }
    }
  };

  artifacts = {
    page: async (value: unknown, options: { source: string; contentType?: string; strategy: "verbatim" | "json-index" | "text-chunks" | "table-index" | "llm-summary"; maxInlineTokens?: number }) => {
      if (options.strategy === "verbatim") return value;
      const response = await request(this.cave, "/sdk/v1/artifacts", { value, options, workflow: this.workflow });
      if (response.stored === false) return value;
      return `[cave-artifact id=${response.artifact_id} source=${options.source} type=${options.contentType ?? "application/json"}]\nsummary: artifact stored.\nretrieve: call cave_expand_artifact with id and json_pointer or range.\n[/cave-artifact]`;
    }
  };

  context = {
    checkpoint: async (messages: unknown[], options: Record<string, unknown>) => request(this.cave, "/sdk/v1/checkpoints", { messages, options, workflow: this.workflow }),
    /**
     * Reverse a checkpoint `source_ref` back into the original context.
     *
     * GETs `/sdk/v1/checkpoints/{sourceRef}/expand`; the gateway returns the
     * stored `{ source_ref, version, messages, checkpoint }`. This is the other
     * half of `checkpoint()` — reversibility is mandatory: a checkpoint that
     * cannot be expanded is a bug.
     */
    expand: async (sourceRef: string) => request(this.cave, `/sdk/v1/checkpoints/${encodeURIComponent(sourceRef)}/expand`)
  };
}

async function toolSearch(
  cave: Cave,
  catalog: CaveTool[],
  query: string,
  options?: { maxTools?: number; context?: string; workflow?: string; ranker?: "bm25" | "embeddings"; toolSessionId?: string }
): Promise<ToolSearchResult> {
  const body: Record<string, unknown> = {
    tools: catalog.map((t) => ({
      name: t.name,
      description: t.description,
      input_schema: t.inputSchema,
      read_only: t.readOnly,
      always_load: t.alwaysLoad ?? false
    })),
    query
  };
  if (options?.context !== undefined) body["context"] = options.context;
  if (options?.maxTools !== undefined) body["max_tools"] = options.maxTools;
  if (options?.toolSessionId !== undefined) body["session_id"] = options.toolSessionId;
  // Embedding-similarity is a pure passthrough: the SDK forwards the ranker
  // choice and the gateway honors "embeddings" only when it has an embedding
  // provider wired. The SDK never computes similarity itself (byte-safe, no
  // extra deps).
  if (options?.ranker !== undefined) body["ranker"] = options.ranker;

  const workflow = options?.workflow ?? cave.options.defaultWorkflow ?? "unlabeled-workflow";
  const response = await fetch(`${cave.options.baseURL}/sdk/v1/tool-search`, {
    method: "POST",
    headers: headers(cave, workflow),
    body: JSON.stringify(body)
  });
	if (!response.ok) throw new Error(`tool search failed with HTTP ${response.status}`);
  const data = await response.json();

  // Map snake_case response to camelCase
  const rawSent = strictNonNegativeInt(data.sent_schema_tokens);
  const rawFull = strictNonNegativeInt(data.full_schema_tokens);
  const validCounts = rawSent !== null && rawFull !== null && rawSent <= rawFull;
  const sentSchemaTokens = validCounts ? rawSent : 0;
  const fullSchemaTokens = validCounts ? rawFull : 0;
  const savedTokens = fullSchemaTokens - sentSchemaTokens;
  const reductionPct = fullSchemaTokens === 0 ? 0 : Math.round((savedTokens / fullSchemaTokens) * 1000) / 10;

  return {
    sessionId: typeof data.session_id === "string" ? data.session_id : undefined,
    tools: data.tools ?? [],
    sentSchemaTokens,
    fullSchemaTokens,
    deferredCount: strictNonNegativeInt(data.deferred_count) ?? 0,
    method: data.method ?? "unknown",
    tokenBasis: typeof data.token_basis === "string" && data.token_basis.trim() ? data.token_basis : "unavailable",
    basis: "inferred",
    savedTokens,
    reductionPct
  };
}

async function compress(cave: Cave, payload: string, options?: CompressOptions): Promise<CompressResult> {
  // Fail-closed pass-through: the original bytes unchanged, no saving claimed.
  const passthrough = (): CompressResult => ({
    output: payload,
    contentType: options?.contentType ?? "unknown",
    tokensBefore: 0,
    tokensAfter: 0,
    ratio: 0,
    basis: "inferred",
    tokenCountBasis: "unavailable"
  });

  const body: Record<string, unknown> = { input: payload };
  if (options?.contentType !== undefined) body["content_type"] = options.contentType;
  const workflow = cave.options.defaultWorkflow ?? "unlabeled-workflow";

  let data: Record<string, unknown>;
  try {
    const response = await fetch(`${cave.options.baseURL}/sdk/v1/compress`, {
      method: "POST",
      headers: headers(cave, workflow),
      body: JSON.stringify(body)
    });
    if (!response.ok) return passthrough();
    data = await response.json();
  } catch {
    return passthrough();
  }

  // Anything other than a well-formed report with a string `output` is a parse
  // problem → pass through. Never trust a partial response.
  if (!data || typeof data["output"] !== "string") return passthrough();

  const tokensBefore = strictNonNegativeInt(data["tokens_before"]);
  const tokensAfter = strictNonNegativeInt(data["tokens_after"]);
  if (tokensBefore === null || tokensAfter === null || tokensAfter > tokensBefore) return passthrough();
  const derivedRatio = tokensBefore === 0 ? 0 : (tokensBefore - tokensAfter) / tokensBefore;
  // Ratio is a mathematical derivative of the validated counters. Recompute it
  // rather than preserving an inconsistent/optimistic server field.
  const ratio = derivedRatio;
  if (data["output"] === payload && tokensAfter !== tokensBefore) return passthrough();
  const result: CompressResult = {
    output: data["output"] as string,
    contentType: typeof data["content_type"] === "string" ? (data["content_type"] as string) : options?.contentType ?? "unknown",
    tokensBefore,
    tokensAfter,
    ratio,
    basis: "inferred", // honesty: the SDK never emits `verified`, whatever the Engine says.
    tokenCountBasis: typeof data["token_count_basis"] === "string" && (data["token_count_basis"] as string).trim()
      ? (data["token_count_basis"] as string)
      : "unavailable"
  };
  if (typeof data["recovery_handle"] === "string") result.recoveryHandle = data["recovery_handle"] as string;
  if (typeof data["method"] === "string") result.method = data["method"] as string;
  if (typeof data["lossless_to_model"] === "boolean") result.losslessToModel = data["lossless_to_model"] as boolean;
  return result;
}

async function cavePlan(cave: Cave): Promise<CavePlan> {
  // Control-plane read served by control-api: target `controlURL` (falling back
  // to `baseURL`) and authenticate with the project key via `x-cave-api-key`
  // (the OTLP/key-auth header set, `otlpHeaders`), never the gateway
  // `authorization: Bearer` header. The `plan:read` scope gates it server-side.
  const base = cave.options.controlURL ?? cave.options.baseURL;
  const response = await fetch(`${base}/sdk/v1/cave-plan`, {
    method: "GET",
    headers: otlpHeaders(cave)
  });
  if (!response.ok) throw new Error(`cave plan fetch failed with HTTP ${response.status}`);
  // Passed through verbatim: snake_case wire fields, every figure inferred/per-day.
  return (await response.json()) as CavePlan;
}

function strictNonNegativeInt(value: unknown): number | null {
  return typeof value === "number" && Number.isSafeInteger(value) && value >= 0 ? value : null;
}

function providerClient(cave: Cave, provider: string, prefix: string, upstreamKey?: string) {
  return {
    provider,
    responses: { create: (body: unknown, init?: { cave?: { latencyClass?: string; toolSessionId?: string } }) => providerFetch(cave, `${prefix}/responses`, body, cave.options.defaultWorkflow ?? "unlabeled-workflow", init?.cave, upstreamKey) },
    chat: { completions: { create: (body: unknown, init?: { cave?: { toolSessionId?: string } }) => providerFetch(cave, `${prefix}/chat/completions`, body, cave.options.defaultWorkflow ?? "unlabeled-workflow", init?.cave, upstreamKey) } },
    raw: fetch
  };
}

async function providerFetch(cave: Cave, path: string, body: unknown, workflow: string, hint?: Record<string, unknown>, upstreamKey?: string) {
  const response = await fetch(`${cave.options.baseURL}${path}`, {
    method: "POST",
    headers: headers(cave, workflow, upstreamKey, hint),
    body: JSON.stringify(body)
  });
  if (!response.ok) throw new Error(await response.text());
  return response.json();
}

async function request(cave: Cave, path: string, body?: unknown) {
  const init: RequestInit = {
    method: body === undefined ? "GET" : "POST",
    headers: headers(cave, cave.options.defaultWorkflow ?? "unlabeled-workflow")
  };
  if (body !== undefined) init.body = JSON.stringify(body);
  const response = await fetch(`${cave.options.baseURL}${path}`, init);
  return response.json();
}

function headers(cave: Cave, workflow: string, upstreamKey?: string, hint?: Record<string, unknown>) {
  return {
    "content-type": "application/json",
    authorization: `Bearer ${cave.options.apiKey}`,
    "x-cave-agent": cave.options.agent,
    "x-cave-workflow": workflow,
    "x-cave-retention": cave.options.retention ?? "metadata",
    ...(cave.options.user ? { "x-cave-user-hash": cave.options.user } : {}),
    ...(upstreamKey ? { "x-cave-upstream-key": upstreamKey } : {}),
    ...(hint?.["latencyClass"] !== undefined ? { "x-cave-async": String(hint["latencyClass"] !== "interactive") } : {}),
    ...(typeof hint?.["toolSessionId"] === "string" ? { "x-cave-tool-session": hint["toolSessionId"] as string } : {})
  };
}

// ─── OTel exporter (OTLP/JSON over fetch, no runtime deps) ────────────────────

/**
 * One-call OTel exporter that ships spans to the gateway OTLP endpoint.
 *
 * Build with `cave.exporter()`. Record spans with `recordSpan()` (GenAI fields
 * are mapped to `gen_ai.*` semantic-convention attributes), then `export()`
 * POSTs the buffered batch to `{baseURL}/otlp/v1/traces` and clears the buffer.
 * No runtime deps: the OTLP/JSON payload is built by hand and sent with `fetch`.
 */
export class OTelExporter {
  private spans: OTelSpan[] = [];

  constructor(private cave: Cave, readonly serviceName: string) {}

  /** Number of spans buffered and not yet exported. */
  get pending(): number {
    return this.spans.length;
  }

  /** A fresh 16-byte (32-hex) trace id. */
  newTraceId(): string {
    return randomHex(16);
  }

  /** A fresh 8-byte (16-hex) span id. */
  newSpanId(): string {
    return randomHex(8);
  }

  /**
   * Buffer a span, mapping GenAI fields to `gen_ai.*` attributes.
   * Returns the OTelSpan (with generated ids) so callers can chain child spans
   * via `parentSpanId: span.spanId`.
   */
  recordSpan(name: string, options: SpanOptions = {}): OTelSpan {
    const nowNs = Date.now() * 1_000_000;
    const attrs: Record<string, string | number | boolean> = { ...(options.attributes ?? {}) };
    // These counters feed spend and savings reports. Only the typed, validated
    // fields may set them; arbitrary attributes cannot overwrite trusted values.
    for (const key of [
      "gen_ai.usage.input_tokens",
      "gen_ai.usage.output_tokens",
      "gen_ai.usage.cached_tokens",
      "gen_ai.usage.cost_usd",
      "cave.agent",
      "cave.workflow",
    ]) delete attrs[key];
    if (options.operation !== undefined) attrs["gen_ai.operation.name"] = options.operation;
    if (options.provider !== undefined) attrs["gen_ai.system"] = options.provider;
    if (options.model !== undefined) {
      attrs["gen_ai.request.model"] = options.model;
      attrs["gen_ai.response.model"] = options.model;
    }
    if (options.toolName !== undefined) attrs["gen_ai.tool.name"] = options.toolName;
    const inputTokens = strictNonNegativeInt(options.inputTokens);
    const outputTokens = strictNonNegativeInt(options.outputTokens);
    const cachedTokens = strictNonNegativeInt(options.cachedTokens);
    if (inputTokens !== null) attrs["gen_ai.usage.input_tokens"] = inputTokens;
    if (outputTokens !== null) attrs["gen_ai.usage.output_tokens"] = outputTokens;
    if (cachedTokens !== null && (inputTokens === null || cachedTokens <= inputTokens)) attrs["gen_ai.usage.cached_tokens"] = cachedTokens;
    if (typeof options.costUsd === "number" && Number.isFinite(options.costUsd) && options.costUsd >= 0) attrs["gen_ai.usage.cost_usd"] = options.costUsd;
    attrs["cave.agent"] = this.cave.options.agent;
    attrs["cave.workflow"] = options.workflow ?? this.cave.options.defaultWorkflow ?? "unlabeled-workflow";

    const statusCode = options.status === "error" ? 2 : options.status === "unset" ? 0 : 1;
    const span: OTelSpan = {
      name,
      traceId: options.traceId ?? this.newTraceId(),
      spanId: options.spanId ?? this.newSpanId(),
      parentSpanId: options.parentSpanId ?? "",
      kind: options.kind ?? 3,
      startTimeNs: options.startTimeNs ?? nowNs,
      endTimeNs: options.endTimeNs ?? nowNs,
      statusCode,
      attributes: attrs
    };
    this.spans.push(span);
    return span;
  }

  /** Build the OTLP/JSON payload for the buffered spans (no network). */
  buildPayload(): Record<string, unknown> {
    return {
      resourceSpans: [
        {
          resource: {
            attributes: [otlpKV("service.name", this.serviceName), otlpKV("cave.agent", this.cave.options.agent)]
          },
          scopeSpans: [
            {
              scope: { name: "caveman-cloud", version: "1.0.0" },
              spans: this.spans.map(spanToOtlp)
            }
          ]
        }
      ]
    };
  }

  /**
   * POST the buffered spans to `{baseURL}/otlp/v1/traces` and clear them.
   * Returns the gateway's JSON response (`ok` / `spans_accepted` /
   * `spans_total` / `otel_schema_version`). No-op when nothing is buffered.
   */
  async export(): Promise<Record<string, unknown>> {
    if (this.spans.length === 0) return { ok: true, spans_accepted: 0, spans_total: 0 };
    const payload = this.buildPayload();
    const response = await fetch(`${this.cave.options.baseURL}/otlp/v1/traces`, {
      method: "POST",
      headers: otlpHeaders(this.cave),
      body: JSON.stringify(payload)
    });
    if (!response.ok) throw new Error(await response.text());
    const data = await response.json();
    this.spans = [];
    return data;
  }

  /** Alias for `export()`, matching common OTel exporter naming. */
  flush(): Promise<Record<string, unknown>> {
    return this.export();
  }
}

function spanToOtlp(sp: OTelSpan): Record<string, unknown> {
  return {
    traceId: sp.traceId,
    spanId: sp.spanId,
    parentSpanId: sp.parentSpanId,
    name: sp.name,
    kind: sp.kind,
    startTimeUnixNano: String(sp.startTimeNs),
    endTimeUnixNano: String(sp.endTimeNs),
    attributes: Object.entries(sp.attributes).map(([k, v]) => otlpKV(k, v)),
    status: { code: sp.statusCode }
  };
}

/**
 * Encode one attribute as an OTLP/JSON KeyValue.
 * Ints → `intValue` (proto3 int64 → JSON string), bools → `boolValue`,
 * non-integer numbers → `doubleValue`; everything else → `stringValue`.
 */
function otlpKV(key: string, value: string | number | boolean): Record<string, unknown> {
  if (typeof value === "boolean") return { key, value: { boolValue: value } };
  if (typeof value === "number") {
    return Number.isInteger(value) ? { key, value: { intValue: String(value) } } : { key, value: { doubleValue: value } };
  }
  return { key, value: { stringValue: String(value) } };
}

function otlpHeaders(cave: Cave) {
  return {
    "content-type": "application/json",
    "x-cave-api-key": cave.options.apiKey,
    "x-cave-agent": cave.options.agent,
    "x-cave-workflow": cave.options.defaultWorkflow ?? "unlabeled-workflow",
    "x-cave-retention": cave.options.retention ?? "metadata",
    ...(cave.options.user ? { "x-cave-user-hash": cave.options.user } : {})
  };
}

function randomHex(nBytes: number): string {
  const bytes = new Uint8Array(nBytes);
  globalThis.crypto.getRandomValues(bytes);
  return Array.from(bytes, (b) => b.toString(16).padStart(2, "0")).join("");
}

/**
 * JSON.stringify with object keys sorted recursively, so two semantically equal
 * argument objects produce the same string regardless of key order. Mirrors the
 * Python breaker's `json.dumps(..., sort_keys=True)`.
 */
function stableStringify(value: unknown): string {
  if (value === null || typeof value !== "object") return JSON.stringify(value);
  if (Array.isArray(value)) return `[${value.map(stableStringify).join(",")}]`;
  const obj = value as Record<string, unknown>;
  const entries = Object.keys(obj)
    .sort()
    .map((k) => `${JSON.stringify(k)}:${stableStringify(obj[k])}`);
  return `{${entries.join(",")}}`;
}
