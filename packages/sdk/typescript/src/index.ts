/** `user` is an opaque end-user identifier sent as x-cave-user-hash (never a
 *  Caveman member id). The SDK forwards it verbatim — hash it yourself before
 *  passing if the raw value is PII. Mirrors the Python `Cave.user`. */
export type CaveOptions = { apiKey: string; baseURL: string; agent: string; defaultWorkflow?: string; retention?: "metadata" | "zdr" | "configured"; verifyOnInit?: boolean; controlURL?: string; user?: string; /** Finite deadline for every SDK HTTP request; defaults to 30 seconds. */ timeoutMs?: number; /** Caller cancellation applied to every SDK HTTP request. */ signal?: AbortSignal };
/** `traceId`/`spanId` continue an inbound trace instead of minting fresh ids; a
 *  value that is not the exact hex shape is replaced rather than put on the wire. */
export type TraceOptions = { workflow?: string; tags?: Record<string, string>; traceId?: string; spanId?: string };
export type ToolOptions = { readOnly?: boolean; idempotent?: boolean; artifactEligible?: boolean };

/** Trace-continuity ids a {@link CaveTrace} puts on every provider call it makes. */
type TraceContext = { traceId: string; parentSpanId: string };

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

/** One caller-owned context fragment considered by {@link Cave.context}. */
export type ContextPackItem = {
  /** Stable caller-owned ID, used to report omitted items exactly. */
  id: string;
  /** Model-visible fragment bytes. */
  text: string;
  /** Optional pre-counted token total; gateway counts when omitted or zero. */
  tokens?: number;
  /** Optional RFC 3339 timestamp used by the recency signal. */
  timestamp?: string;
  /** Caller-supplied relevance boost. */
  priority?: number;
  /** Required context. If a pinned item cannot fit, the call returns honest zero. */
  pin?: boolean;
};

/** Budget and scoring options for {@link Cave.context}. */
export type ContextPackOptions = {
  /** Total model-window budget. Must be greater than zero. */
  maxTokens: number;
  /** Tokens reserved for the next response or tool call. */
  reserveTokens?: number;
  /** Optional RFC 3339 scoring clock for deterministic recency tests. */
  now?: string;
  /** Recency half-life in milliseconds. */
  recencyHalfLifeMs?: number;
  /** Optional override for recency score weight. */
  recencyWeight?: number;
  /** Optional override for error-signal score boost. */
  errorBoost?: number;
};

/** Result from connected-only context packing through the gateway. */
export type ContextPackResult = {
  /** Selected caller-owned items, preserved in original chronological order. */
  items: ContextPackItem[];
  tokensUsed: number;
  tokensBefore: number;
  /** Always inferred; this selector never enters the verified-savings ledger. */
  tokensSaved: number;
  deferredCount: number;
  /** Exact request-ID minus selected-ID set, in request order. */
  deferredIds: string[];
  basis: "inferred";
};

export type AssemblyStability = "stable" | "session" | "volatile";
export type EmitCacheHints = "gateway" | "self" | "none";

/** One author-declared request fragment for {@link Cave.assemble}. */
export type AssemblySlot = {
  id: string;
  stability: AssemblyStability;
  content: unknown;
};

/** Input contract for the client-pure cache-optimal request assembler. */
export type AssembleOptions = {
  provider: "anthropic" | "openai" | string;
  model: string;
  sessionId: string;
  slots: AssemblySlot[];
  /** Defaults to "gateway". "self" places hints but mints nothing. */
  emitCacheHints?: EmitCacheHints;
};

/** Result from {@link Cave.assemble}; every token figure is inferred. */
export type AssemblyResult = {
  request: Record<string, unknown>;
  /** Header to send with request; Cave provider clients attach it automatically. */
  headers: { "x-cave-assembly": string };
  prefixHash: string;
  breakpoints: string[];
  stableTokens: number;
  tokenBasis: "estimated_bytes_div_4";
  basis: "inferred";
  volatileBelowBreakpoint: true;
};

/** Stable/session content changed inside its declared lifetime. */
export class AssemblyStabilityError extends Error {
  constructor(readonly slotId: string) {
    super(`assembly slot ${JSON.stringify(slotId)} changed after being declared stable`);
    this.name = "AssemblyStabilityError";
  }
}

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

function normalizedServiceURL(value: string, name: "baseURL" | "controlURL"): string {
  if (value.trim() !== value) throw new Error(`${name} must not contain surrounding whitespace`);
  let parsed: URL;
  try {
    parsed = new URL(value);
  } catch {
    throw new Error(`${name} must be an absolute http(s) URL`);
  }
  if ((parsed.protocol !== "http:" && parsed.protocol !== "https:") || !parsed.hostname || parsed.username || parsed.password) {
    throw new Error(`${name} must be an absolute http(s) URL without credentials`);
  }
  if (parsed.search || parsed.hash) throw new Error(`${name} must not contain a query or fragment`);
  return value.replace(/\/+$/, "");
}

export class Cave {
  readonly options: CaveOptions;

  constructor(options: CaveOptions) {
    if (!options.apiKey || !options.baseURL || !options.agent) throw new Error("apiKey, baseURL, and agent are required");
    if (options.timeoutMs !== undefined && (!Number.isSafeInteger(options.timeoutMs) || options.timeoutMs <= 0)) {
      throw new Error("timeoutMs must be a positive integer");
    }
    // CAVE_WORKFLOW lets a wrapper (`cave wrap --workflow x`) label every request
    // from an SDK app without a code change. An explicit option always wins.
    // globalThis lookup keeps the SDK browser-neutral (no node type dependency).
    // The env value is normalized to the gateway's label rule (lowercase
    // [a-z0-9_-], max 96) — an invalid ambient value is ignored rather than
    // 400-ing every request. Mirrors caveman_cloud (Python).
    const envRaw = (globalThis as { process?: { env?: Record<string, string | undefined> } }).process?.env?.["CAVE_WORKFLOW"]?.toLowerCase();
    const envWorkflow = envRaw && /^[a-z0-9_-]{1,96}$/.test(envRaw) ? envRaw : undefined;
    const normalized: CaveOptions = {
      ...options,
      baseURL: normalizedServiceURL(options.baseURL, "baseURL"),
      ...(options.controlURL === undefined
        ? {}
        : { controlURL: normalizedServiceURL(options.controlURL, "controlURL") }),
    };
    this.options = !normalized.defaultWorkflow && envWorkflow
      ? { ...normalized, defaultWorkflow: envWorkflow }
      : normalized;
  }

  /**
   * Run `fn` inside a trace.
   *
   * The trace allocates a `traceId` (32 lowercase hex) and a root `spanId`
   * (16 lowercase hex) with the same RNG the OTel exporter uses. Every provider
   * call made through the trace carries them as `x-cave-trace-id` +
   * `x-cave-parent-span-id`, and `trace.exporter()` reuses the same trace id —
   * so the gateway's request rows and the SDK's own spans join into one trace.
   *
   * `options.traceId` / `options.spanId` continue an inbound trace; a value that
   * is not the exact hex shape is replaced by a fresh one rather than put on the
   * wire. Mirrors the Python `Cave.trace`.
   */
  async trace<T>(options: TraceOptions, fn: (trace: CaveTrace) => Promise<T>): Promise<T> {
    return fn(new CaveTrace(this, options.workflow ?? this.options.defaultWorkflow ?? "unlabeled-workflow", options.tags ?? {}, options.traceId, options.spanId));
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
   * (`/vertex/v1/projects/.../models/{model}:generateContent`). Raw requests
   * remain constrained to this gateway provider prefix.
   */
  vertex(config: { upstreamKey?: string } = {}) {
    return providerClient(this, "vertex", "/vertex", config.upstreamKey);
  }

  /**
   * Create a one-call OTel exporter bound to this Cave's gateway config.
   *
   * The exporter ships spans to the gateway's OTLP endpoint
   * (`POST {baseURL}/v1/traces`) with Caveman headers
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
    if (strategy !== "all" && strategy !== "deferred") {
      throw new Error("strategy must be all or deferred");
    }
    const initialToolCount = config.initialToolCount ?? 8;
    if (!Number.isSafeInteger(initialToolCount) || initialToolCount < 0) {
      throw new Error("initialToolCount must be a non-negative integer");
    }
    if (config.maxLoadedTools !== undefined && (!Number.isSafeInteger(config.maxLoadedTools) || config.maxLoadedTools < 1)) {
      throw new Error("maxLoadedTools must be a positive integer");
    }
    let initial: CaveTool[];
    if (strategy === "all") {
      initial = config.catalog.slice();
    } else {
      const always = config.catalog.filter((tool) => tool.alwaysLoad);
      if (config.maxLoadedTools !== undefined && always.length > config.maxLoadedTools) {
        throw new Error("maxLoadedTools must be at least the alwaysLoad tool count");
      }
      const lazy = config.catalog.filter((tool) => !tool.alwaysLoad);
      const lazyLimit = config.maxLoadedTools === undefined
        ? initialToolCount
        : Math.min(initialToolCount, config.maxLoadedTools - always.length);
      initial = always.concat(lazy.slice(0, lazyLimit));
    }
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
        const maxTools = options?.maxTools ?? config.maxLoadedTools;
        return toolSearch(cave, config.catalog, query, maxTools === undefined ? options : { ...options, maxTools });
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
   * Connected-only context selector. `pack()` POSTs caller-owned fragments to
   * `/sdk/v1/context/pack`; it never writes CCR and never runs in local wrap.
   * Omitted fragments are named by `deferredIds` so callers can re-supply them.
   *
   * This decides **what** enters the model window. Cache-optimal assembly
   * decides **where** selected content sits relative to a cache breakpoint;
   * they are complementary, not alternatives.
   */
  context = {
    pack: async (query: string, items: ContextPackItem[], options: ContextPackOptions): Promise<ContextPackResult> =>
      contextPack(this, query, items, options)
  };

  /**
   * Build a provider request with stable/session slots above every volatile
   * slot. Pure client-side: no account and no network call.
   *
   * Stability ledger is in-process. Cross-process determinism remains caller's
   * obligation. `emitCacheHints:"gateway"` (default) emits no provider cache
   * hint so gateway optimizer retains causal attribution. `"self"` is for
   * direct provider calls; it places hints but mints nothing.
   */
  assemble(options: AssembleOptions): AssemblyResult {
    return assemble(this, options);
  }

  /**
   * Read this project's Cave Plan machine-readably (`GET /sdk/v1/cave-plan`),
   * authed by the project key's `plan:read` scope. Returns the project-scope
   * plan verbatim — snake_case wire fields identical to control-api and the
   * Python `cave_plan()`. Every dollar figure is `inferred` and a PER-DAY rate;
   * the SDK never re-derives or re-projects them (no monthly, no `verified`).
   *
   * This is a gateway-owned project-key read, so it targets `baseURL` and
   * carries the `x-cave-api-key` project key. `controlURL` is reserved for
   * control-api `/api/v1/*` surfaces. On a non-200
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

  /**
   * A runtime-policy client for this project.
   *
   * Policies come from the project's published policy (delivered overlay), read
   * once per `refresh()` from `GET /sdk/v1/runtime-policy`. `decide()` is
   * synchronous and local-only — it never blocks a caller on the network, and a
   * missing/rejected bundle simply means the caller's own baseline path.
   *
   * This surface measures nothing and claims nothing: it routes work and emits
   * an optional observability span. Mirrors the Python `cave.runtime_policy()`.
   */
  runtimePolicy(options: RuntimePolicyOptions = {}): RuntimePolicyClient {
    return new RuntimePolicyClient(this, options);
  }

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
  /** Trace id shared by every provider call and exported span in this trace (32 lowercase hex). */
  readonly traceId: string;
  /** Root span id of this trace; sent as `x-cave-parent-span-id` (16 lowercase hex). */
  readonly spanId: string;
  /** Publicly reachable exporter buffers, one per service name. */
  private readonly exporters = new Map<string, OTelExporter>();
  /** Per-trace tool start order. Completion order can differ under concurrency. */
  private toolSequence = 0;

  constructor(private cave: Cave, private workflow: string, private tags: Record<string, string>, traceId?: string, spanId?: string) {
    this.traceId = normalizeTraceId(traceId);
    this.spanId = normalizeSpanId(spanId);
  }

  /**
   * An OTel exporter bound to this trace: spans recorded without an explicit
   * `traceId` inherit this trace's, so SDK spans and the gateway's request rows
   * land in the same trace. Repeated calls for one service return the same
   * buffer, including runtime-policy decision spans, so callers can flush them.
   * Mirrors the Python `Trace.exporter`.
   */
  exporter(config: { serviceName?: string } = {}): OTelExporter {
    const serviceName = config.serviceName ?? this.cave.options.agent;
    const existing = this.exporters.get(serviceName);
    if (existing) return existing;
    const exporter = new OTelExporter(this.cave, serviceName, this.traceId);
    this.exporters.set(serviceName, exporter);
    return exporter;
  }

  async tool<T>(name: string, options: ToolOptions, fn: () => Promise<T> | T): Promise<T> {
    const sequence = ++this.toolSequence;
    const start = Date.now();
    let result: T;
    try {
      result = await fn();
    } catch (error) {
      await this.emitToolEvent(name, options, "error", sequence, start);
      throw error;
    }
    await this.emitToolEvent(name, options, "ok", sequence, start);
    return result;
  }

  /** Best-effort metadata only: telemetry must never replace a tool result/error. */
  private async emitToolEvent(name: string, options: ToolOptions, outcome: "ok" | "error", sequence: number, start: number): Promise<void> {
    const durationMs = Math.max(0, Math.min(Number.MAX_SAFE_INTEGER, Math.trunc(Date.now() - start)));
    await request(
      this.cave,
      "/sdk/v1/events",
      { span_type: "tool.call", name, workflow: this.workflow, options, outcome, sequence, duration_ms: durationMs, tags: this.tags },
      this.workflow,
      this.traceContext()
    ).catch(() => undefined);
  }

  /** The continuity ids every provider call in this trace puts on the wire. */
  private traceContext(): TraceContext {
    return { traceId: this.traceId, parentSpanId: this.spanId };
  }

  model = {
    openai: {
      responses: { create: (body: unknown, init?: { cave?: { latencyClass?: string } }) => providerFetch(this.cave, "/openai/v1/responses", body, this.workflow, init?.cave, undefined, this.traceContext()) },
      chat: { completions: { create: (body: unknown) => providerFetch(this.cave, "/openai/v1/chat/completions", body, this.workflow, undefined, undefined, this.traceContext()) } }
    }
  };

  artifacts = {
    page: async (value: unknown, options: { source: string; contentType?: string; strategy: "verbatim" | "json-index" | "text-chunks" | "table-index" | "llm-summary"; maxInlineTokens?: number }) => {
      if (options.strategy === "verbatim") return value;
      const response = await request(this.cave, "/sdk/v1/artifacts", { value, options, workflow: this.workflow }, this.workflow, this.traceContext(), {
        "x-cave-artifact-envelope": "value-v1",
        "x-cave-artifact-source": options.source
      });
      if (response.stored === false) return value;
      if (response.stored !== true || typeof response.artifact_id !== "string" || response.artifact_id.trim() === "") {
        throw new CaveRequestError(200, "/sdk/v1/artifacts", "cave artifact response missing artifact_id");
      }
      return `[cave-artifact id=${response.artifact_id} source=${options.source} type=${options.contentType ?? "application/json"}]\nsummary: artifact stored.\nretrieve: authenticated GET /sdk/v1/artifacts/${response.artifact_id} or trace.artifacts.get("${response.artifact_id}").\n[/cave-artifact]`;
    },
    get: async (artifactId: string): Promise<unknown> => artifactGet(this.cave, artifactId, this.workflow, this.traceContext())
  };

  context = {
    checkpoint: async (messages: unknown[], options: Record<string, unknown>) => request(this.cave, "/sdk/v1/checkpoints", { messages, options, workflow: this.workflow }, this.workflow, this.traceContext()),
    /**
     * Reverse a checkpoint `source_ref` back into the original context.
     *
     * GETs `/sdk/v1/checkpoints/{sourceRef}/expand`; the gateway returns the
     * stored `{ source_ref, version, messages, checkpoint }`. This is the other
     * half of `checkpoint()` — reversibility is mandatory: a checkpoint that
     * cannot be expanded is a bug.
     */
    expand: async (sourceRef: string) => {
      const response = await request(this.cave, `/sdk/v1/checkpoints/${encodeURIComponent(sourceRef)}/expand`, undefined, this.workflow, this.traceContext());
      if (typeof response.source_ref !== "string" || response.source_ref.trim() === "" || !Array.isArray(response.messages)) {
        throw new CaveRequestError(200, "/sdk/v1/checkpoints/{sourceRef}/expand", "cave checkpoint response missing source_ref or messages");
      }
      return response;
    }
  };
}

// ─── Runtime policy client ────────────────────────────────────────────────────

/** Schema version this client accepts; anything else is rejected (fail closed). */
const RUNTIME_POLICY_SCHEMA_VERSION = "caveman.runtime-policy.v1";

/** Runtime-policy envelopes are KB-sized; stop a hostile body after 1 MiB. */
const RUNTIME_POLICY_MAX_RESPONSE_BYTES = 1024 * 1024;

async function readRuntimePolicyResponseText(response: Response): Promise<string> {
  const contentLength = response.headers?.get?.("content-length");
  if (typeof contentLength === "string" && /^[0-9]+$/.test(contentLength) && Number(contentLength) > RUNTIME_POLICY_MAX_RESPONSE_BYTES) {
    await Promise.resolve(response.body?.cancel?.()).catch(() => undefined);
    throw new Error("oversized_response");
  }

  const reader = response.body?.getReader?.();
  if (!reader) throw new Error("bounded_response_unavailable");
  const chunks: Uint8Array[] = [];
  let total = 0;
  try {
    while (true) {
      const { done, value } = await reader.read();
      if (done) break;
      if (!(value instanceof Uint8Array)) throw new Error("runtime policy response stream returned invalid bytes");
      total += value.byteLength;
      if (total > RUNTIME_POLICY_MAX_RESPONSE_BYTES) {
        await Promise.resolve(reader.cancel()).catch(() => undefined);
        throw new Error("oversized_response");
      }
      chunks.push(value);
    }
  } finally {
    try {
      reader.releaseLock();
    } catch {
      // A broken fetch implementation must not mask the bounded-read result.
    }
  }
  const joined = new Uint8Array(total);
  let offset = 0;
  for (const chunk of chunks) {
    joined.set(chunk, offset);
    offset += chunk.byteLength;
  }
  return new TextDecoder().decode(joined);
}

/** One AND-ed guard on a runtime policy (wire shape, verbatim snake_case). */
export type RuntimePolicyGuard = { field: string; op: "eq" | "ne" | "gt" | "gte" | "lt" | "lte" | "in" | string; value?: unknown };

/** One experiment arm. `holdout` is a reserved name and never a declared arm. */
export type RuntimePolicyArm = { name: string; fraction: number };

/** Experiment attached to a runtime policy; absent means the policy applies to 100% post-guards. */
export type RuntimePolicyExperiment = { id: string; holdout_frac?: number; arms: RuntimePolicyArm[] };

/**
 * A published runtime policy, verbatim from the project's delivered policy
 * (snake_case wire fields, like {@link CavePlan}). `verify`, `budget`, and
 * `escalation` are opaque to the SDK and passed straight back to the caller.
 */
export type RuntimePolicyDoc = {
  id: string;
  task_family: string;
  disabled?: boolean;
  applies_when?: RuntimePolicyGuard[];
  execute: { workflow: string };
  fallback?: { workflow?: string | null };
  verify?: string[];
  budget?: Record<string, unknown>;
  escalation?: Array<Record<string, unknown>>;
  experiment?: RuntimePolicyExperiment;
};

/** Why {@link RuntimePolicyClient.decide} landed where it did. Every non-`applied` reason is a fail-closed path. */
export type PolicyDecisionReason =
  | "applied"
  | "holdout"
  | "guards_failed"
  | "disabled"
  | "kill"
  | "local_kill"
  | "policy_unavailable"
  | "no_policy"
  | "no_unit_key"
  | "invalid_policy"
  | "invalid_experiment"
  | "ambiguous_policy";

/**
 * The local routing decision for one task.
 *
 * `baseline` means the caller's own path (`workflow` is null); `fallback` means
 * the policy's declared fallback workflow. Nothing here is a savings claim.
 */
export type PolicyDecision = {
  decision: "execute" | "fallback" | "baseline";
  reason: PolicyDecisionReason;
  policyId: string | null;
  workflow: string | null;
  experimentId?: string;
  arm?: string;
  /** Probability this unit had of receiving the assigned arm (holdout included). */
  propensity?: number;
  budget?: Record<string, unknown>;
  verify?: string[];
  escalation?: Array<Record<string, unknown>>;
  policyVersion?: number;
  sequence?: number;
  /** True when the bundle in force was Ed25519-verified. */
  signed: boolean;
};

/** Options for {@link Cave.runtimePolicy}. */
export type RuntimePolicyOptions = {
  /** Base64 raw 32-byte Ed25519 key to pin. When set, an unsigned or mis-signed bundle is rejected. */
  publicKey?: string;
  /** Background refresh interval in seconds. Default off. */
  autoRefreshSeconds?: number;
  /** Environment variable checked at decide() time as a local kill switch. Default `CAVEMAN_POLICY_KILL`. */
  killEnv?: string;
};

/** Result of {@link RuntimePolicyClient.refresh}; never a thrown error. */
export type RuntimePolicyRefresh = { ok: boolean; signed: boolean; error?: string };

/** Snapshot of what the client currently holds. */
export type RuntimePolicyState = {
  hasBundle: boolean;
  signed: boolean;
  policyVersion?: number;
  sequence?: number;
  /** The bundle's own kill flag (true for pass-through `record` projects). */
  kill: boolean;
  /** True once `kill()` was called locally. */
  killedLocally: boolean;
};

/**
 * Anything that can buffer the decision span — i.e. `cave.exporter()` or
 * `trace.exporter()`. The span rides the exporter's existing batch; `decide()`
 * opens no new network path.
 */
export type PolicySpanRecorder = { recordSpan(name: string, options: SpanOptions): unknown };

/** What `decide()` accepts as a decision-span sink. */
export type PolicySpanSink = PolicySpanRecorder | CaveTrace;

/**
 * Fallback memoization for duck-typed traces from another module/realm. Native
 * CaveTrace instances also memoize exporter() publicly, making this buffer
 * caller-reachable for flush/export.
 */
const policyTraceExporters = new WeakMap<object, PolicySpanRecorder>();

/**
 * Resolve whatever the caller passed into a span sink, or null for no emission.
 *
 * Duck-typed, never `instanceof`: a CaveTrace minted by a duplicated copy of
 * this module (bundler realm, transitive dependency, dual CJS/ESM install) is
 * still a trace, and silently dropping its spans would be an observability
 * hole. Mirrors the Python `_policy_span_sink`.
 */
function policySpanSink(trace: PolicySpanSink | null | undefined): PolicySpanRecorder | null {
  if (!trace) return null;
  const recorder = trace as PolicySpanRecorder;
  if (typeof recorder.recordSpan === "function") return recorder;
  const factory = (trace as { exporter?: unknown }).exporter;
  if (typeof factory !== "function") return null;
  const memoKey = trace as unknown as object;
  const memoized = policyTraceExporters.get(memoKey);
  if (memoized) return memoized;
  const exporter = (factory as () => unknown).call(trace) as PolicySpanRecorder | null;
  if (!exporter || typeof exporter.recordSpan !== "function") return null;
  policyTraceExporters.set(memoKey, exporter);
  return exporter;
}

/** Per-call inputs for {@link RuntimePolicyClient.decide}. */
export type DecideOptions = {
  /** Stable assignment unit (task id, case id, user id…). Required once a policy carries an experiment. */
  unitKey?: string | null;
  /** Values the policy's guards read. Missing field or type mismatch ⇒ that guard is false. */
  context?: Record<string, unknown> | null;
  /**
   * Optional span sink for the decision event (observability only). Prefer an
   * exporter you flush yourself (`cave.exporter()` / `trace.exporter()`); a
   * CaveTrace's default exporter is memoized and caller-reachable.
   */
  trace?: PolicySpanSink | null;
};

/**
 * Client for the project's published runtime policies.
 *
 * `refresh()` is the only network call; it verifies the bundle signature over
 * the exact bytes the server signed and keeps the last-known-good bundle on any
 * failure. `decide()` is synchronous, local-only, and never throws — with no
 * bundle it returns the caller's baseline. Mirrors the Python
 * `RuntimePolicyClient`.
 */
export class RuntimePolicyClient {
  private bundle: Record<string, unknown> | null = null;
  private signedBundle = false;
  private pinnedKey: Uint8Array | null = null;
  private killedLocally = false;
  private timer: ReturnType<typeof setInterval> | null = null;
  /** True while a `refresh()` is in flight; the background timer skips rather than pile on. */
  private refreshing = false;
  private readonly killEnv: string;

  constructor(private cave: Cave, options: RuntimePolicyOptions = {}) {
    this.killEnv = options.killEnv ?? "CAVEMAN_POLICY_KILL";
    if (options.publicKey !== undefined) {
      const pin = base64Bytes(options.publicKey);
      if (pin === null || pin.length !== 32) throw new Error("runtimePolicy publicKey must be a base64-encoded 32-byte Ed25519 key");
      this.pinnedKey = pin;
    }
    const seconds = options.autoRefreshSeconds;
    if (typeof seconds === "number" && Number.isFinite(seconds) && seconds > 0) {
      const timer = setInterval(() => {
        // A slow or hung endpoint must not queue refreshes behind each other:
        // one tick while a refresh is already in flight is skipped, not stacked.
        if (this.refreshing) return;
        void this.refresh();
      }, seconds * 1000);
      (timer as unknown as { unref?: () => void }).unref?.();
      this.timer = timer;
    }
  }

  /**
   * Fetch and verify the current bundle (`GET /sdk/v1/runtime-policy`).
   *
   * Never throws: a transport error, a bad signature, an unknown schema
   * version, or a regressed sequence returns `{ok:false, error}` and leaves the
   * previous bundle in force. The request is capped at 30s (matching the Python
   * `urlopen(..., timeout=30)`) so a hung endpoint cannot pin the caller open.
   */
  async refresh(): Promise<RuntimePolicyRefresh> {
    this.refreshing = true;
    try {
      const requestHeaders: Record<string, string> = { ...headers(this.cave, this.cave.options.defaultWorkflow ?? "unlabeled-workflow") };
      // GET carries no body; the wire contract omits content-type.
      delete requestHeaders["content-type"];
      const init: RequestInit = { method: "GET", headers: requestHeaders };
      const response = await caveFetch(this.cave, `${this.cave.options.baseURL}/sdk/v1/runtime-policy`, init);
      if (!response.ok) return this.refreshFailed(`runtime policy fetch failed (HTTP ${response.status})`);

      let payload: unknown;
      try {
        payload = JSON.parse(await readRuntimePolicyResponseText(response));
      } catch (error) {
        if (error instanceof Error && error.message === "oversized_response") return this.refreshFailed("oversized_response");
        if (error instanceof Error && error.message === "bounded_response_unavailable") return this.refreshFailed("bounded_response_unavailable");
        return this.refreshFailed("runtime policy response was not valid JSON");
      }
      if (!isRecord(payload)) return this.refreshFailed("runtime policy response must be a JSON object");
      const raw = payload["bundle"];
      if (typeof raw !== "string" || raw === "") return this.refreshFailed("runtime policy response missing bundle");

      const verification = await this.verifyBundle(raw, payload["signature"], payload["public_key"]);
      if (!verification.ok) return this.refreshFailed(verification.error);

      let parsed: unknown;
      try {
        parsed = JSON.parse(raw);
      } catch {
        return this.refreshFailed("runtime policy bundle was not valid JSON");
      }
      if (!isRecord(parsed)) return this.refreshFailed("runtime policy bundle must be a JSON object");
      if (parsed["schema_version"] !== RUNTIME_POLICY_SCHEMA_VERSION) {
        return this.refreshFailed(`unknown runtime policy schema_version ${JSON.stringify(parsed["schema_version"])}`);
      }
      const policyVersion = strictNonNegativeInt(parsed["policy_version"]);
      const next = strictNonNegativeInt(parsed["sequence"]);
      if (policyVersion === null) return this.refreshFailed("runtime policy policy_version must be a non-negative safe integer");
      if (next === null) return this.refreshFailed("runtime policy sequence must be a non-negative safe integer");
      const current = this.bundle === null ? null : strictNonNegativeInt(this.bundle["sequence"]);
      if (current !== null && next < current) {
        return this.refreshFailed(`runtime policy sequence regressed (${next} < ${current})`);
      }

      // Only now — after verification, parsing, and the staleness check — does
      // the new bundle replace the last-known-good one.
      this.bundle = parsed;
      this.signedBundle = verification.signed;
      return { ok: true, signed: verification.signed };
    } catch (error) {
      return this.refreshFailed(error instanceof Error ? error.message : String(error));
    } finally {
      this.refreshing = false;
    }
  }

  /**
   * Route one task, locally and synchronously. No network, no throw — a garbage
   * context, a malformed policy, or a missing bundle all resolve to a
   * fail-closed decision.
   */
  decide(taskFamily: string, options: DecideOptions = {}): PolicyDecision {
    let decision: PolicyDecision;
    try {
      decision = this.evaluate(taskFamily, options);
    } catch {
      // decide() is on the caller's hot path: an internal fault must degrade to
      // the customer's own baseline, never surface as an exception.
      decision = { decision: "baseline", reason: "policy_unavailable", policyId: null, workflow: null, signed: this.signedBundle };
    }
    try {
      // Reading `options.trace` is itself caller-controlled (a getter can
      // throw), so the sink lookup lives inside the guard too: decide() never
      // throws, full stop.
      this.emitDecisionSpan(decision, options.trace);
    } catch {
      // Observability must never break routing.
    }
    return decision;
  }

  /** Latch this client off. Every later `decide()` returns the caller's baseline. */
  kill(): void {
    this.killedLocally = true;
  }

  /** What the client currently holds. */
  state(): RuntimePolicyState {
    const bundle = this.bundle;
    const state: RuntimePolicyState = {
      hasBundle: bundle !== null,
      signed: this.signedBundle,
      kill: bundle !== null && bundle["kill"] === true,
      killedLocally: this.killedLocally
    };
    if (bundle !== null) {
      const version = finiteNumber(bundle["policy_version"]);
      const sequence = finiteNumber(bundle["sequence"]);
      if (version !== null) state.policyVersion = version;
      if (sequence !== null) state.sequence = sequence;
    }
    return state;
  }

  /** Release the optional background refresh timer. Mirrors the Python `close()`. */
  close(): void {
    if (this.timer !== null) {
      clearInterval(this.timer);
      this.timer = null;
    }
  }

  private refreshFailed(error: string): RuntimePolicyRefresh {
    return { ok: false, signed: this.signedBundle, error };
  }

  /**
   * Verify the signature over the bundle string's exact UTF-8 bytes, before any
   * parsing. A pinned key (explicit or TOFU) makes a signature mandatory.
   *
   * The TOFU pin is stored the moment a signature cryptographically verifies —
   * BEFORE schema and sequence checks — so a bundle this client rejects for
   * being the wrong shape still establishes which key the project signs with.
   * Pinning after those checks would leave a downgrade window: reject the first
   * signed bundle on schema, then accept a later unsigned one.
   */
  private async verifyBundle(
    raw: string,
    signature: unknown,
    publicKey: unknown
  ): Promise<{ ok: true; signed: boolean } | { ok: false; error: string }> {
    const signatureRecord = isRecord(signature) ? signature : null;
    const rawSig = signatureRecord !== null && typeof signatureRecord["sig"] === "string" ? base64Bytes(signatureRecord["sig"]) : null;
    const signaturePresent = signature !== null && signature !== undefined;
    const publicKeyPresent = publicKey !== null && publicKey !== undefined;
    const message = new TextEncoder().encode(raw);

    if (this.pinnedKey !== null) {
      if (rawSig === null) return { ok: false, error: "runtime policy bundle is unsigned but a public key is pinned" };
      if (!(await ed25519Verify(this.pinnedKey, message, rawSig))) {
        return { ok: false, error: "runtime policy signature did not verify against the pinned key" };
      }
      return { ok: true, signed: true };
    }

    const keyRecord = isRecord(publicKey) ? publicKey : null;
    if (rawSig !== null && keyRecord !== null) {
      const key = typeof keyRecord["key"] === "string" ? base64Bytes(keyRecord["key"]) : null;
      if (key === null || key.length !== 32) return { ok: false, error: "runtime policy public key is not a 32-byte Ed25519 key" };
      if (!(await ed25519Verify(key, message, rawSig))) return { ok: false, error: "runtime policy signature did not verify" };
      // Trust on first use: the key is pinned for this client's lifetime the
      // instant it verifies, so a later unsigned or differently-signed bundle is
      // rejected even if this one goes on to fail a schema or sequence check.
      this.pinnedKey = key;
      return { ok: true, signed: true };
    }
    if (signaturePresent || publicKeyPresent) {
      return { ok: false, error: "runtime policy carried a signature without a usable public key" };
    }
    return { ok: true, signed: false };
  }

  private evaluate(taskFamily: string, options: DecideOptions): PolicyDecision {
    if (this.killedLocally || envKillSet(this.killEnv)) return this.baseline("local_kill");
    const bundle = this.bundle;
    if (bundle === null) return this.baseline("policy_unavailable");
    if (bundle["kill"] === true) return this.baseline("kill");

    const family = typeof taskFamily === "string" ? taskFamily : "";
    const documents = Array.isArray(bundle["runtime_policies"]) ? (bundle["runtime_policies"] as unknown[]) : [];
    const matching = family === ""
      ? []
      : documents.filter((doc): doc is Record<string, unknown> => isRecord(doc) && doc["task_family"] === family);
    if (matching.length === 0) return this.baseline("no_policy");

    const valid = matching.filter(isValidPolicyDoc);
    if (valid.length === 0) return this.baseline("invalid_policy");
    const enabled = valid.filter((doc) => doc["disabled"] !== true);
    if (enabled.length === 0) return this.baseline("disabled");

    const passing = enabled.filter((doc) => guardsPass(doc, options.context));
    // Guard failure is reported against the first enabled candidate so the
    // caller still learns which policy declined the task.
    if (passing.length === 0) return this.forDoc(enabled[0]!, "fallback", "guards_failed");
    // Mirrors the server's ">1 at the winning tier ⇒ none": an ambiguous family
    // is never resolved by guessing.
    if (passing.length > 1) return this.baseline("ambiguous_policy");

    const doc = passing[0]!;
    const experiment = doc["experiment"];
    if (experiment === undefined || experiment === null) return this.forDoc(doc, "execute", "applied");

    const unitKey = typeof options.unitKey === "string" ? options.unitKey : "";
    if (unitKey === "") return this.forDoc(doc, "fallback", "no_unit_key");
    const spec = normalizeExperiment(experiment);
    if (spec === null) return this.forDoc(doc, "fallback", "invalid_experiment");

    const projectId = typeof bundle["project_id"] === "string" ? bundle["project_id"] : "";
    const u = policyUnitFraction(projectId, spec.id, unitKey);
    // The holdout slice is carved first and forced onto the fallback path — it
    // is suppression, never a riskier variant.
    if (u < spec.holdout) {
      const decision = this.forDoc(doc, "fallback", "holdout");
      decision.experimentId = spec.id;
      decision.arm = "holdout";
      decision.propensity = spec.holdout;
      return decision;
    }
    const rescaled = (u - spec.holdout) / (1 - spec.holdout);
    let cumulative = 0;
    let chosen = spec.arms[spec.arms.length - 1]!;
    for (let index = 0; index < spec.arms.length - 1; index++) {
      cumulative += spec.arms[index]!.fraction / spec.total;
      if (rescaled < cumulative) {
        chosen = spec.arms[index]!;
        break;
      }
    }
    const decision = this.forDoc(doc, "execute", "applied");
    decision.experimentId = spec.id;
    decision.arm = chosen.name;
    decision.propensity = (1 - spec.holdout) * (chosen.fraction / spec.total);
    return decision;
  }

  private baseline(reason: PolicyDecisionReason): PolicyDecision {
    return { decision: "baseline", reason, policyId: null, workflow: null, signed: this.signedBundle, ...this.bundleMeta() };
  }

  private forDoc(doc: Record<string, unknown>, outcome: "execute" | "fallback", reason: PolicyDecisionReason): PolicyDecision {
    const execute = isRecord(doc["execute"]) ? doc["execute"] : {};
    const fallback = isRecord(doc["fallback"]) ? doc["fallback"] : {};
    const workflow = outcome === "execute" ? execute["workflow"] : fallback["workflow"];
    const decision: PolicyDecision = {
      decision: outcome,
      reason,
      policyId: typeof doc["id"] === "string" ? doc["id"] : null,
      workflow: typeof workflow === "string" && workflow !== "" ? workflow : null,
      signed: this.signedBundle,
      ...this.bundleMeta()
    };
    // Opaque pass-through: the SDK carries these to the caller, it never reads them.
    if (isRecord(doc["budget"])) decision.budget = doc["budget"];
    if (Array.isArray(doc["verify"])) decision.verify = (doc["verify"] as unknown[]).filter((v): v is string => typeof v === "string");
    if (Array.isArray(doc["escalation"])) decision.escalation = (doc["escalation"] as unknown[]).filter(isRecord);
    return decision;
  }

  private bundleMeta(): { policyVersion?: number; sequence?: number } {
    const bundle = this.bundle;
    if (bundle === null) return {};
    const meta: { policyVersion?: number; sequence?: number } = {};
    const version = finiteNumber(bundle["policy_version"]);
    const sequence = finiteNumber(bundle["sequence"]);
    if (version !== null) meta.policyVersion = version;
    if (sequence !== null) meta.sequence = sequence;
    return meta;
  }

  /** Observability only — one buffered span, no network, never fatal. */
  private emitDecisionSpan(decision: PolicyDecision, trace?: PolicySpanSink | null): void {
    // EVERY interaction with the caller's sink — resolving it, building the
    // exporter, recording — sits inside this guard. A hostile or half-built
    // sink is an observability loss, never a routing failure.
    try {
      const sink = policySpanSink(trace);
      if (sink === null) return;
      const attributes: Record<string, string | number | boolean> = {
        "cave.policy.decision": decision.decision,
        "cave.policy.reason": decision.reason,
        "cave.policy.signed": decision.signed
      };
      if (decision.policyId !== null) attributes["cave.policy.id"] = decision.policyId;
      if (decision.policyVersion !== undefined) attributes["cave.policy.version"] = decision.policyVersion;
      if (decision.experimentId !== undefined) attributes["cave.experiment.id"] = decision.experimentId;
      if (decision.arm !== undefined) attributes["cave.experiment.arm"] = decision.arm;
      if (decision.propensity !== undefined) attributes["cave.experiment.propensity"] = decision.propensity;
      sink.recordSpan("caveman.policy.decision", { attributes });
    } catch {
      // A span sink must never break routing.
    }
  }
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function finiteNumber(value: unknown): number | null {
  return typeof value === "number" && Number.isFinite(value) ? value : null;
}

/** A policy doc the client can act on. Anything else is skipped, never guessed. */
function isValidPolicyDoc(doc: Record<string, unknown>): boolean {
  if (typeof doc["id"] !== "string" || doc["id"].trim() === "") return false;
  if (typeof doc["task_family"] !== "string" || doc["task_family"].trim() === "") return false;
  const execute = doc["execute"];
  if (!isRecord(execute) || typeof execute["workflow"] !== "string" || execute["workflow"].trim() === "") return false;
  const guards = doc["applies_when"];
  if (guards !== undefined && guards !== null) {
    if (!Array.isArray(guards)) return false;
    for (const guard of guards) if (!isRecord(guard)) return false;
  }
  const fallback = doc["fallback"];
  if (fallback !== undefined && fallback !== null && !isRecord(fallback)) return false;
  return true;
}

/** All guards AND-ed. An unreadable guard is false, so the policy declines. */
function guardsPass(doc: Record<string, unknown>, context: Record<string, unknown> | null | undefined): boolean {
  const guards = doc["applies_when"];
  if (guards === undefined || guards === null) return true;
  if (!Array.isArray(guards)) return false;
  const values = isRecord(context) ? context : {};
  for (const guard of guards) if (!guardPasses(guard, values)) return false;
  return true;
}

function guardPasses(guard: unknown, context: Record<string, unknown>): boolean {
  if (!isRecord(guard)) return false;
  const field = guard["field"];
  const op = guard["op"];
  if (typeof field !== "string" || field === "" || typeof op !== "string") return false;
  if (!Object.prototype.hasOwnProperty.call(context, field)) return false;
  const actual = context[field];
  if (actual === undefined) return false;
  const expected = guard["value"];
  switch (op) {
    case "eq":
      return scalarEquals(actual, expected);
    case "ne":
      return sameScalarKind(actual, expected) && !scalarEquals(actual, expected);
    case "gt":
    case "gte":
    case "lt":
    case "lte": {
      const left = finiteNumber(actual);
      const right = finiteNumber(expected);
      if (left === null || right === null) return false;
      if (op === "gt") return left > right;
      if (op === "gte") return left >= right;
      if (op === "lt") return left < right;
      return left <= right;
    }
    case "in":
      return Array.isArray(expected) && expected.some((candidate) => scalarEquals(actual, candidate));
    default:
      // Unknown operator: fail closed rather than guess an intent.
      return false;
  }
}

/** Scalars of the same kind only; a type mismatch is false, never coerced. */
function sameScalarKind(a: unknown, b: unknown): boolean {
  if (typeof a !== typeof b) return false;
  return typeof a === "string" || typeof a === "boolean" || (typeof a === "number" && Number.isFinite(a) && Number.isFinite(b as number));
}

function scalarEquals(a: unknown, b: unknown): boolean {
  return sameScalarKind(a, b) && a === b;
}

type NormalizedExperiment = { id: string; holdout: number; arms: RuntimePolicyArm[]; total: number };

/** A usable experiment, or null — an invalid config never yields a guessed arm. */
function normalizeExperiment(raw: unknown): NormalizedExperiment | null {
  if (!isRecord(raw)) return null;
  const id = raw["id"];
  if (typeof id !== "string" || id.trim() === "") return null;
  let holdout = 0;
  const rawHoldout = raw["holdout_frac"];
  if (rawHoldout !== undefined && rawHoldout !== null) {
    const value = finiteNumber(rawHoldout);
    if (value === null || value < 0 || value >= 1) return null;
    holdout = value;
  }
  const rawArms = raw["arms"];
  if (!Array.isArray(rawArms) || rawArms.length === 0) return null;
  const arms: RuntimePolicyArm[] = [];
  let total = 0;
  for (const entry of rawArms) {
    if (!isRecord(entry)) return null;
    const name = entry["name"];
    if (typeof name !== "string" || name.trim() === "" || name === "holdout") return null;
    const fraction = finiteNumber(entry["fraction"]);
    if (fraction === null || fraction <= 0) return null;
    arms.push({ name, fraction });
    total += fraction;
  }
  if (!(total > 0)) return null;
  return { id, holdout, arms, total };
}

/**
 * The deterministic unit fraction in [0,1) used for experiment assignment,
 * byte-for-byte the Go `shared/platform/sampling.Fraction`: SHA-256 over each
 * key prefixed by its 8-byte big-endian UTF-8 byte length, first 8 digest bytes
 * as a big-endian uint64, shifted right 11 and divided by 2^53.
 *
 * Exported so a caller can reproduce an assignment (and so the cross-language
 * fixtures can pin this port bit-for-bit). Mirrors the Python
 * `policy_unit_fraction`.
 */
export function policyUnitFraction(...keys: string[]): number {
  const encoder = new TextEncoder();
  const parts = keys.map((key) => encoder.encode(key));
  const total = parts.reduce((sum, part) => sum + part.length + 8, 0);
  const buffer = new Uint8Array(total);
  const view = new DataView(buffer.buffer);
  let offset = 0;
  for (const part of parts) {
    view.setBigUint64(offset, BigInt(part.length), false);
    offset += 8;
    buffer.set(part, offset);
    offset += part.length;
  }
  const digest = sha256HexBytes(buffer);
  return Number(BigInt(`0x${digest.slice(0, 16)}`) >> 11n) / 2 ** 53;
}

/** SPKI header for a raw Ed25519 public key (RFC 8410 id-Ed25519, 32-byte BIT STRING). */
const ED25519_SPKI_PREFIX = new Uint8Array([0x30, 0x2a, 0x30, 0x05, 0x06, 0x03, 0x2b, 0x65, 0x70, 0x03, 0x21, 0x00]);

/**
 * Ed25519 verification over the exact message bytes. Any problem — bad lengths,
 * no Ed25519 in the host's WebCrypto, a rejected key — is `false`, never a
 * silent accept.
 */
async function ed25519Verify(publicKey: Uint8Array, message: Uint8Array, signature: Uint8Array): Promise<boolean> {
  if (publicKey.length !== 32 || signature.length !== 64) return false;
  try {
    const spki = new Uint8Array(ED25519_SPKI_PREFIX.length + publicKey.length);
    spki.set(ED25519_SPKI_PREFIX);
    spki.set(publicKey, ED25519_SPKI_PREFIX.length);
    const subtle = globalThis.crypto?.subtle;
    if (!subtle) return false;
    const key = await subtle.importKey("spki", spki as unknown as ArrayBuffer, { name: "Ed25519" }, false, ["verify"]);
    return await subtle.verify({ name: "Ed25519" }, key, signature as unknown as ArrayBuffer, message as unknown as ArrayBuffer);
  } catch {
    return false;
  }
}

/** Standard-alphabet base64 → bytes, or null when the input is not base64. */
function base64Bytes(value: string): Uint8Array | null {
  if (typeof value !== "string" || value.trim() === "") return null;
  try {
    const binary = atob(value.trim());
    const bytes = new Uint8Array(binary.length);
    for (let index = 0; index < binary.length; index++) bytes[index] = binary.charCodeAt(index);
    return bytes;
  } catch {
    return null;
  }
}

/** The local kill switch, read at decide() time so an operator can flip it mid-process. */
function envKillSet(name: string): boolean {
  const raw = (globalThis as { process?: { env?: Record<string, string | undefined> } }).process?.env?.[name];
  if (typeof raw !== "string") return false;
  const value = raw.trim().toLowerCase();
  return value !== "" && value !== "0" && value !== "false" && value !== "no" && value !== "off";
}

async function toolSearch(
  cave: Cave,
  catalog: CaveTool[],
  query: string,
  options?: { maxTools?: number; context?: string; workflow?: string; ranker?: "bm25" | "embeddings"; toolSessionId?: string }
): Promise<ToolSearchResult> {
  if (options?.maxTools !== undefined && (!Number.isSafeInteger(options.maxTools) || options.maxTools < 1)) {
    throw new Error("maxTools must be a positive integer");
  }
  const body: Record<string, unknown> = {
    tools: catalog.map((t) => ({
      name: t.name,
      description: t.description,
      input_schema: t.inputSchema,
      read_only: t.readOnly,
      idempotent: t.idempotent,
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
  const response = await caveFetch(cave, `${cave.options.baseURL}/sdk/v1/tool-search`, {
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

  const catalogByName = new Map<string, CaveTool>();
  for (const tool of catalog) {
    if (typeof tool.name !== "string" || tool.name.trim() === "" || catalogByName.has(tool.name)) {
      throw new Error("tool catalog names must be non-empty and unique");
    }
    catalogByName.set(tool.name, tool);
  }
  if (!Array.isArray(data.tools)) throw new Error("tool search response tools must be an array");
  const returnedNames = new Set<string>();
  const selectedTools = data.tools.map((wire: unknown) => {
    const name = wire !== null && typeof wire === "object" && typeof (wire as Record<string, unknown>)["name"] === "string"
      ? (wire as Record<string, unknown>)["name"] as string
      : "";
    const local = catalogByName.get(name);
    if (!local || returnedNames.has(name)) throw new Error("tool search response contained an unknown or duplicate tool");
    returnedNames.add(name);
    return local;
  });

  return {
    sessionId: typeof data.session_id === "string" ? data.session_id : undefined,
    tools: selectedTools,
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
    const response = await caveFetch(cave, `${cave.options.baseURL}/sdk/v1/compress`, {
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

async function contextPack(
  cave: Cave,
  query: string,
  items: ContextPackItem[],
  options: ContextPackOptions
): Promise<ContextPackResult> {
  const passthrough = (): ContextPackResult => ({
    items: [...items],
    tokensUsed: 0,
    tokensBefore: 0,
    tokensSaved: 0,
    deferredCount: 0,
    deferredIds: [],
    basis: "inferred"
  });

  if (!Number.isSafeInteger(options.maxTokens) || options.maxTokens <= 0) return passthrough();
  const byID = new Map<string, ContextPackItem>();
  for (const item of items) {
    if (!item.id || byID.has(item.id)) return passthrough();
    byID.set(item.id, item);
  }

  const wireItems = items.map((item) => ({
    id: item.id,
    text: item.text,
    ...(item.tokens !== undefined ? { tokens: item.tokens } : {}),
    ...(item.timestamp !== undefined ? { timestamp: item.timestamp } : {}),
    ...(item.priority !== undefined ? { priority: item.priority } : {}),
    ...(item.pin !== undefined ? { pin: item.pin } : {})
  }));
  const wireOptions: Record<string, unknown> = { max_tokens: options.maxTokens };
  if (options.reserveTokens !== undefined) wireOptions["reserve_tokens"] = options.reserveTokens;
  if (options.now !== undefined) wireOptions["now"] = options.now;
  if (options.recencyHalfLifeMs !== undefined) wireOptions["recency_half_life_ms"] = options.recencyHalfLifeMs;
  if (options.recencyWeight !== undefined) wireOptions["recency_weight"] = options.recencyWeight;
  if (options.errorBoost !== undefined) wireOptions["error_boost"] = options.errorBoost;

  let data: Record<string, unknown>;
  try {
    const response = await caveFetch(cave, `${cave.options.baseURL}/sdk/v1/context/pack`, {
      method: "POST",
      headers: headers(cave, cave.options.defaultWorkflow ?? "unlabeled-workflow"),
      body: JSON.stringify({ query, items: wireItems, options: wireOptions })
    });
    if (!response.ok) return passthrough();
    data = await response.json();
  } catch {
    return passthrough();
  }

  if (!data || !Array.isArray(data["items"]) || !Array.isArray(data["deferred_ids"])) return passthrough();
  const selectedIDs: string[] = [];
  const selectedSet = new Set<string>();
  for (const raw of data["items"]) {
    if (!raw || typeof raw !== "object") return passthrough();
    const id = (raw as Record<string, unknown>)["id"];
    if (typeof id !== "string" || !byID.has(id) || selectedSet.has(id)) return passthrough();
    selectedIDs.push(id);
    selectedSet.add(id);
  }
  const expectedDeferred = items.filter((item) => !selectedSet.has(item.id)).map((item) => item.id);
  const deferredIDs = data["deferred_ids"];
  if (!deferredIDs.every((id): id is string => typeof id === "string")) return passthrough();
  if (deferredIDs.length !== expectedDeferred.length || deferredIDs.some((id, i) => id !== expectedDeferred[i])) return passthrough();

  const tokensUsed = strictNonNegativeInt(data["tokens_used"]);
  const tokensBefore = strictNonNegativeInt(data["tokens_before"]);
  const tokensSaved = strictNonNegativeInt(data["tokens_saved"]);
  const deferredCount = strictNonNegativeInt(data["deferred_count"]);
  if (
    tokensUsed === null ||
    tokensBefore === null ||
    tokensSaved === null ||
    deferredCount === null ||
    tokensUsed + tokensSaved !== tokensBefore ||
    deferredCount !== deferredIDs.length
  ) return passthrough();

  return {
    items: selectedIDs.map((id) => byID.get(id)!),
    tokensUsed,
    tokensBefore,
    tokensSaved,
    deferredCount,
    deferredIds: [...deferredIDs],
    basis: "inferred"
  };
}

const assemblyHashLedgers = new WeakMap<Cave, Map<string, string>>();
const assemblyRequestHeaders = new WeakMap<object, string>();

function assemble(cave: Cave, options: AssembleOptions): AssemblyResult {
  const provider = options.provider.trim().toLowerCase();
  const emitCacheHints = options.emitCacheHints ?? "gateway";
  if (!options.model || !options.sessionId) throw new Error("assemble requires model and sessionId");
  if (!["gateway", "self", "none"].includes(emitCacheHints)) {
    throw new Error(`unknown emitCacheHints value ${JSON.stringify(emitCacheHints)}`);
  }

  const ids = new Set<string>();
  let ledger = assemblyHashLedgers.get(cave);
  if (!ledger) {
    ledger = new Map<string, string>();
    assemblyHashLedgers.set(cave, ledger);
  }
  const normalized = options.slots.map((slot) => {
    if (!slot.id || ids.has(slot.id)) throw new Error(`assembly slot id must be non-empty and unique: ${JSON.stringify(slot.id)}`);
    ids.add(slot.id);
    if (!["stable", "session", "volatile"].includes(slot.stability)) {
      throw new Error(`assembly slot ${JSON.stringify(slot.id)} has unknown stability ${JSON.stringify(slot.stability)}`);
    }
    let contentJSON: string;
    try {
      contentJSON = canonicalJSON(slot.content);
    } catch {
      throw new Error(`assembly slot ${JSON.stringify(slot.id)} content is not JSON-serializable`);
    }
    const contentHash = sha256Hex(contentJSON);
    if (slot.stability !== "volatile") {
      const key = `${options.sessionId}\0${slot.id}`;
      const previous = ledger.get(key);
      if (previous !== undefined && previous !== contentHash) throw new AssemblyStabilityError(slot.id);
      ledger.set(key, contentHash);
    }
    return { ...slot, content: JSON.parse(contentJSON) as unknown };
  });

  const ordered = (["stable", "session", "volatile"] as const)
    .flatMap((stability) => normalized.filter((slot) => slot.stability === stability));
  const prefixSlots = ordered.filter((slot) => slot.stability !== "volatile");
  const volatileSlots = ordered.filter((slot) => slot.stability === "volatile");

  let request: Record<string, unknown>;
  let prefixBytes: string;
  let breakpoints: string[] = [];

  if (provider === "anthropic") {
    const toolsSlot = prefixSlots.find((slot) => slot.id === "tools" && Array.isArray(slot.content));
    const systemSlots = prefixSlots.filter((slot) => slot !== toolsSlot);
    const system = systemSlots.map((slot) => ({ type: "text", text: assemblyText(slot.content) }));
    const messages = volatileSlots.length === 0 ? [] : [{
      role: "user",
      content: volatileSlots.map((slot) => ({ type: "text", text: assemblyText(slot.content) }))
    }];
    request = {
      model: options.model,
      ...(toolsSlot ? { tools: cloneCanonical(toolsSlot.content) } : {}),
      ...(system.length > 0 ? { system } : {}),
      messages
    };
    prefixBytes = canonicalJSON({
      model: options.model,
      ...(toolsSlot ? { tools: toolsSlot.content } : {}),
      ...(system.length > 0 ? { system } : {})
    });
    if (emitCacheHints === "self") {
      const tools = request["tools"];
      if (Array.isArray(tools)) {
        for (let i = tools.length - 1; i >= 0; i--) {
          const tool = tools[i];
          if (tool && typeof tool === "object" && !Array.isArray(tool)) {
            (tool as Record<string, unknown>)["cache_control"] = { type: "ephemeral" };
            breakpoints = [`tools[${i}]`];
            break;
          }
        }
      }
      if (breakpoints.length === 0) {
        const blocks = request["system"];
        if (Array.isArray(blocks)) {
          for (let i = blocks.length - 1; i >= 0; i--) {
            const block = blocks[i];
            if (block && typeof block === "object" && !Array.isArray(block)) {
              (block as Record<string, unknown>)["cache_control"] = { type: "ephemeral" };
              breakpoints = [`system[${i}]`];
              break;
            }
          }
        }
      }
    }
  } else if (provider === "openai") {
    const toolsSlot = prefixSlots.find((slot) => slot.id === "tools" && Array.isArray(slot.content));
    const systemSlots = prefixSlots.filter((slot) => slot !== toolsSlot);
    const systemText = systemSlots.map((slot) => assemblyText(slot.content)).join("\n\n");
    const messages: Array<Record<string, unknown>> = [];
    if (systemText) messages.push({ role: "system", content: systemText });
    for (const slot of volatileSlots) messages.push({ role: "user", content: assemblyText(slot.content) });
    request = {
      model: options.model,
      ...(toolsSlot ? { tools: cloneCanonical(toolsSlot.content) } : {}),
      messages
    };
    const signatureParts = [`model=${options.model}`];
    if (toolsSlot) signatureParts.push(canonicalJSON(toolsSlot.content));
    if (systemText) signatureParts.push(canonicalJSON(systemText));
    prefixBytes = signatureParts.join("\0");
    if (emitCacheHints === "self" && (toolsSlot || systemText)) {
      request["prompt_cache_key"] = sha256Hex(prefixBytes).slice(0, 32);
      breakpoints = ["prompt_cache_key"];
    }
  } else {
    request = {
      model: options.model,
      slots: ordered.map((slot) => ({
        id: slot.id,
        stability: slot.stability,
        content: cloneCanonical(slot.content)
      }))
    };
    prefixBytes = canonicalJSON(prefixSlots);
  }

  const prefixHash = sha256Hex(prefixBytes);
  const assemblyHeader = `v1;slots=${ordered.length};prefix=${prefixHash.slice(0, 12)};vbb=1`;
  assemblyRequestHeaders.set(request, assemblyHeader);
  return {
    request,
    headers: { "x-cave-assembly": assemblyHeader },
    prefixHash,
    breakpoints,
    stableTokens: Math.ceil(new TextEncoder().encode(prefixBytes).length / 4),
    tokenBasis: "estimated_bytes_div_4",
    basis: "inferred",
    volatileBelowBreakpoint: true
  };
}

function canonicalJSON(value: unknown): string {
  const raw = JSON.stringify(value);
  if (raw === undefined) throw new Error("value is not JSON-serializable");
  return stableStringify(JSON.parse(raw));
}

function cloneCanonical(value: unknown): unknown {
  return JSON.parse(canonicalJSON(value)) as unknown;
}

function assemblyText(value: unknown): string {
  return typeof value === "string" ? value : canonicalJSON(value);
}

async function cavePlan(cave: Cave): Promise<CavePlan> {
  // `/sdk/v1/cave-plan` is a gateway-owned project-key route. `controlURL` is
  // reserved for control-api `/api/v1/*` surfaces and must never redirect this
  // call to a route that service does not expose.
  const response = await caveFetch(cave, `${cave.options.baseURL}/sdk/v1/cave-plan`, {
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

const DEFAULT_HTTP_TIMEOUT_MS = 30_000;

/**
 * Shared bounded transport for every SDK request.
 *
 * The explicit abort race is intentional: callers still settle on deadline when
 * a custom fetch implementation ignores AbortSignal. Native fetch also receives
 * the combined signal, so response-body reads and sockets are cancelled.
 */
async function caveFetch(cave: Cave, input: RequestInfo | URL, init: RequestInit = {}): Promise<Response> {
  const timeoutMs = cave.options.timeoutMs ?? DEFAULT_HTTP_TIMEOUT_MS;
  const timeout = AbortSignal.timeout(timeoutMs);
  const inputSignal = input instanceof Request ? input.signal : undefined;
  const signals = [timeout, cave.options.signal, inputSignal, init.signal].filter((signal): signal is AbortSignal => signal !== undefined && signal !== null);
  const signal = signals.length === 1 ? signals[0]! : AbortSignal.any(signals);
  const abort = new Promise<never>((_resolve, reject) => {
    const fail = () => {
      const reason = signal.reason;
      reject(reason instanceof DOMException && reason.name === "TimeoutError"
        ? new Error("cave_request_timeout")
        : reason instanceof Error ? reason : new Error("cave_request_aborted"));
    };
    if (signal.aborted) fail();
    else signal.addEventListener("abort", fail, { once: true });
  });
  // AbortSignal.timeout's internal timer is unref'd in Node: with a custom
  // fetch that schedules no work, the event loop can drain before the deadline
  // fires and strand the race. Hold a ref'd timer for the same duration so the
  // deadline always gets its chance to settle.
  const keepAlive = setTimeout(() => {}, timeoutMs);
  try {
    return await Promise.race([fetch(input, { ...init, signal }), abort]);
  } finally {
    clearTimeout(keepAlive);
  }
}

function providerClient(cave: Cave, provider: string, prefix: string, upstreamKey?: string) {
  return {
    provider,
    responses: { create: (body: unknown, init?: { cave?: { latencyClass?: string; toolSessionId?: string } }) => providerFetch(cave, `${prefix}/responses`, body, cave.options.defaultWorkflow ?? "unlabeled-workflow", init?.cave, upstreamKey) },
    chat: { completions: { create: (body: unknown, init?: { cave?: { toolSessionId?: string } }) => providerFetch(cave, `${prefix}/chat/completions`, body, cave.options.defaultWorkflow ?? "unlabeled-workflow", init?.cave, upstreamKey) } },
    raw: (input: RequestInfo | URL, init?: RequestInit) => providerRawFetch(cave, prefix, input, init, upstreamKey)
  };
}

function providerRawFetch(cave: Cave, prefix: string, input: RequestInfo | URL, init?: RequestInit, upstreamKey?: string): Promise<Response> {
  const base = new URL(cave.options.baseURL);
  const request = input instanceof Request
    ? new Request(input, init)
    : new Request(new URL(String(input), base), init);
  const target = new URL(request.url);
  if (target.origin !== base.origin || (target.pathname !== prefix && !target.pathname.startsWith(`${prefix}/`))) {
    throw new Error("cave_provider_raw_path_not_allowed");
  }
  const merged = new Headers(request.headers);
  const gatewayHeaders = headers(cave, cave.options.defaultWorkflow ?? "unlabeled-workflow", upstreamKey);
  for (const [name, value] of Object.entries(gatewayHeaders)) {
    if (name !== "content-type") merged.set(name, value);
  }
  return caveFetch(cave, new Request(request, { headers: merged }));
}

async function providerFetch(cave: Cave, path: string, body: unknown, workflow: string, hint?: Record<string, unknown>, upstreamKey?: string, trace?: TraceContext) {
  const assemblyHeader = body !== null && typeof body === "object" ? assemblyRequestHeaders.get(body as object) : undefined;
  const response = await caveFetch(cave, `${cave.options.baseURL}${path}`, {
    method: "POST",
    headers: headers(cave, workflow, upstreamKey, assemblyHeader ? { ...hint, assemblyHeader } : hint, trace),
    body: JSON.stringify(body)
  });
  if (!response.ok) throw new Error(await response.text());
  return response.json();
}

export class CaveRequestError extends Error {
  constructor(readonly status: number, readonly path: string, message: string) {
    super(message);
    this.name = "CaveRequestError";
  }
}

async function request(cave: Cave, path: string, body?: unknown, workflow?: string, trace?: TraceContext, extraHeaders?: Record<string, string>): Promise<Record<string, unknown>> {
  const init: RequestInit = {
    method: body === undefined ? "GET" : "POST",
    headers: { ...headers(cave, workflow ?? cave.options.defaultWorkflow ?? "unlabeled-workflow", undefined, undefined, trace), ...extraHeaders }
  };
  if (body !== undefined) init.body = JSON.stringify(body);
  const response = await caveFetch(cave, `${cave.options.baseURL}${path}`, init);
  if (!response.ok) throw new CaveRequestError(response.status, path, `cave request failed (${response.status})`);
  let decoded: unknown;
  try {
    if (typeof response.text === "function") {
      decoded = JSON.parse(await response.text());
    } else if (typeof response.json === "function") {
      decoded = await response.json();
    } else {
      throw new Error("missing response decoder");
    }
  } catch {
    throw new CaveRequestError(response.status, path, "cave response was not valid JSON");
  }
  if (decoded === null || typeof decoded !== "object" || Array.isArray(decoded)) {
    throw new CaveRequestError(response.status, path, "cave response must be a JSON object");
  }
  return decoded as Record<string, unknown>;
}

async function artifactGet(cave: Cave, artifactId: string, workflow: string, trace: TraceContext): Promise<unknown> {
  if (typeof artifactId !== "string" || artifactId.trim() === "") throw new Error("artifactId is required");
  const path = `/sdk/v1/artifacts/${encodeURIComponent(artifactId)}`;
  const response = await caveFetch(cave, `${cave.options.baseURL}${path}`, {
    method: "GET",
    headers: headers(cave, workflow, undefined, undefined, trace)
  });
  if (!response.ok) throw new CaveRequestError(response.status, path, `cave request failed (${response.status})`);
  try {
    return JSON.parse(await response.text());
  } catch {
    throw new CaveRequestError(response.status, path, "cave artifact was not valid JSON");
  }
}

function headers(cave: Cave, workflow: string, upstreamKey?: string, hint?: Record<string, unknown>, trace?: TraceContext) {
  return {
    "content-type": "application/json",
    authorization: `Bearer ${cave.options.apiKey}`,
    "x-cave-agent": cave.options.agent,
    "x-cave-workflow": workflow,
    "x-cave-retention": cave.options.retention ?? "metadata",
    ...(cave.options.user ? { "x-cave-user-hash": cave.options.user } : {}),
    ...(upstreamKey ? { "x-cave-upstream-key": upstreamKey } : {}),
    ...(hint?.["latencyClass"] !== undefined ? { "x-cave-async": String(hint["latencyClass"] !== "interactive") } : {}),
    ...(typeof hint?.["toolSessionId"] === "string" ? { "x-cave-tool-session": hint["toolSessionId"] as string } : {}),
    ...(typeof hint?.["assemblyHeader"] === "string" ? { "x-cave-assembly": hint["assemblyHeader"] as string } : {}),
    // Trace continuity: only requests made through a CaveTrace carry these, so
    // the gateway row's span parents onto the SDK's root span.
    ...(trace ? { "x-cave-trace-id": trace.traceId, "x-cave-parent-span-id": trace.parentSpanId } : {})
  };
}

// ─── OTel exporter (OTLP/JSON over fetch, no runtime deps) ────────────────────

/**
 * One-call OTel exporter that ships spans to standard gateway OTLP/HTTP endpoint.
 *
 * Build with `cave.exporter()`. Record spans with `recordSpan()` (GenAI fields
 * are mapped to `gen_ai.*` semantic-convention attributes), then `export()`
 * POSTs the buffered batch to `{baseURL}/v1/traces` and clears the buffer.
 * No runtime deps: the OTLP/JSON payload is built by hand and sent with `fetch`.
 */
export class OTelExporter {
  private spans: OTelSpan[] = [];
  private inflightSpans = 0;
  private exportTail: Promise<void> = Promise.resolve();
  /** Trace id inherited by spans recorded without an explicit `traceId`; set by `trace.exporter()`. */
  readonly defaultTraceId: string | undefined;

  constructor(private cave: Cave, readonly serviceName: string, defaultTraceId?: string) {
    this.defaultTraceId = defaultTraceId;
  }

  /** Number of spans buffered and not yet exported. */
  get pending(): number {
    return this.spans.length + this.inflightSpans;
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
      "gen_ai.usage.cache_read.input_tokens",
      "gen_ai.usage.cost_usd",
      "cave.agent",
      "cave.workflow",
    ]) delete attrs[key];
    if (options.operation !== undefined) attrs["gen_ai.operation.name"] = options.operation;
    if (options.provider !== undefined) attrs["gen_ai.provider.name"] = options.provider;
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
    if (cachedTokens !== null && (inputTokens === null || cachedTokens <= inputTokens)) attrs["gen_ai.usage.cache_read.input_tokens"] = cachedTokens;
    if (typeof options.costUsd === "number" && Number.isFinite(options.costUsd) && options.costUsd >= 0) attrs["gen_ai.usage.cost_usd"] = options.costUsd;
    attrs["cave.agent"] = this.cave.options.agent;
    attrs["cave.workflow"] = options.workflow ?? this.cave.options.defaultWorkflow ?? "unlabeled-workflow";

    const statusCode = options.status === "error" ? 2 : options.status === undefined || options.status === "ok" ? 1 : 0;
    const span: OTelSpan = {
      name,
      traceId: normalizeTraceId(options.traceId ?? this.defaultTraceId),
      spanId: normalizeSpanId(options.spanId),
      parentSpanId: normalizeParentSpanId(options.parentSpanId),
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
    return this.buildPayloadFor(this.spans);
  }

  private buildPayloadFor(spans: readonly OTelSpan[]): Record<string, unknown> {
    return {
      resourceSpans: [
        {
          resource: {
            attributes: [otlpKV("service.name", this.serviceName), otlpKV("cave.agent", this.cave.options.agent)]
          },
          scopeSpans: [
            {
              scope: { name: "caveman-cloud", version: "1.0.0" },
              spans: spans.map(spanToOtlp)
            }
          ]
        }
      ]
    };
  }

  /**
   * POST buffered spans to standard OTLP/HTTP `{baseURL}/v1/traces`.
   * Returns ExportTraceServiceResponse JSON. No-op when nothing is buffered.
   */
  async export(): Promise<Record<string, unknown>> {
    const previous = this.exportTail;
    let release = () => {};
    this.exportTail = new Promise<void>((resolve) => {
      release = resolve;
    });
    await previous;
    try {
      return await this.exportCurrentBatch();
    } finally {
      release();
    }
  }

  private async exportCurrentBatch(): Promise<Record<string, unknown>> {
    if (this.spans.length === 0) return { ok: true, spans_accepted: 0, spans_total: 0 };
    const batch = this.spans;
    this.spans = [];
    this.inflightSpans += batch.length;
    let succeeded = false;
    try {
      const response = await caveFetch(this.cave, `${this.cave.options.baseURL}/v1/traces`, {
        method: "POST",
        headers: otlpHeaders(this.cave),
        body: JSON.stringify(this.buildPayloadFor(batch))
      });
      if (!response.ok) throw new Error(await response.text());
      const data = await response.json();
      succeeded = true;
      return data;
    } finally {
      this.inflightSpans -= batch.length;
      if (!succeeded) this.spans = [...batch, ...this.spans];
    }
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

const TRACE_ID_HEX = /^[0-9a-f]{32}$/;
const SPAN_ID_HEX = /^[0-9a-f]{16}$/;

/**
 * A caller-supplied trace id, canonicalized to lowercase, or a fresh one when
 * it is not exactly 32 hex characters and non-zero. An invalid id is never put on the wire —
 * the gateway would drop it anyway, and a malformed id silently breaks the join.
 */
function normalizeTraceId(value?: string): string {
  const normalized = typeof value === "string" ? value.toLowerCase() : "";
  return TRACE_ID_HEX.test(normalized) && !/^0+$/.test(normalized) ? normalized : randomHex(16);
}

/** A caller-supplied span id, canonicalized to lowercase, or a fresh valid id. */
function normalizeSpanId(value?: string): string {
  const normalized = typeof value === "string" ? value.toLowerCase() : "";
  return SPAN_ID_HEX.test(normalized) && !/^0+$/.test(normalized) ? normalized : randomHex(8);
}

/** Parent ids cannot be regenerated without breaking causality; malformed values become root spans. */
function normalizeParentSpanId(value?: string): string {
  if (value === undefined || value === "") return "";
  const normalized = value.toLowerCase();
  return SPAN_ID_HEX.test(normalized) && !/^0+$/.test(normalized) ? normalized : "";
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

// Synchronous, dependency-free SHA-256 keeps assemble() client-pure and usable
// in browsers where Node crypto is unavailable.
function sha256Hex(input: string): string {
  return sha256HexBytes(new TextEncoder().encode(input));
}

/** SHA-256 over raw bytes; `sha256Hex` is the UTF-8 string wrapper. */
function sha256HexBytes(bytes: Uint8Array): string {
  const bitLength = bytes.length * 8;
  const paddedLength = Math.ceil((bytes.length + 9) / 64) * 64;
  const padded = new Uint8Array(paddedLength);
  padded.set(bytes);
  padded[bytes.length] = 0x80;
  const view = new DataView(padded.buffer);
  const high = Math.floor(bitLength / 0x1_0000_0000);
  const low = bitLength >>> 0;
  view.setUint32(paddedLength - 8, high, false);
  view.setUint32(paddedLength - 4, low, false);

  const k = [
    0x428a2f98, 0x71374491, 0xb5c0fbcf, 0xe9b5dba5, 0x3956c25b, 0x59f111f1, 0x923f82a4, 0xab1c5ed5,
    0xd807aa98, 0x12835b01, 0x243185be, 0x550c7dc3, 0x72be5d74, 0x80deb1fe, 0x9bdc06a7, 0xc19bf174,
    0xe49b69c1, 0xefbe4786, 0x0fc19dc6, 0x240ca1cc, 0x2de92c6f, 0x4a7484aa, 0x5cb0a9dc, 0x76f988da,
    0x983e5152, 0xa831c66d, 0xb00327c8, 0xbf597fc7, 0xc6e00bf3, 0xd5a79147, 0x06ca6351, 0x14292967,
    0x27b70a85, 0x2e1b2138, 0x4d2c6dfc, 0x53380d13, 0x650a7354, 0x766a0abb, 0x81c2c92e, 0x92722c85,
    0xa2bfe8a1, 0xa81a664b, 0xc24b8b70, 0xc76c51a3, 0xd192e819, 0xd6990624, 0xf40e3585, 0x106aa070,
    0x19a4c116, 0x1e376c08, 0x2748774c, 0x34b0bcb5, 0x391c0cb3, 0x4ed8aa4a, 0x5b9cca4f, 0x682e6ff3,
    0x748f82ee, 0x78a5636f, 0x84c87814, 0x8cc70208, 0x90befffa, 0xa4506ceb, 0xbef9a3f7, 0xc67178f2
  ];
  const h = new Uint32Array([
    0x6a09e667, 0xbb67ae85, 0x3c6ef372, 0xa54ff53a,
    0x510e527f, 0x9b05688c, 0x1f83d9ab, 0x5be0cd19
  ]);
  const w = new Uint32Array(64);
  const rotr = (value: number, amount: number) => (value >>> amount) | (value << (32 - amount));

  for (let offset = 0; offset < paddedLength; offset += 64) {
    for (let i = 0; i < 16; i++) w[i] = view.getUint32(offset + i * 4, false);
    for (let i = 16; i < 64; i++) {
      const s0 = rotr(w[i - 15]!, 7) ^ rotr(w[i - 15]!, 18) ^ (w[i - 15]! >>> 3);
      const s1 = rotr(w[i - 2]!, 17) ^ rotr(w[i - 2]!, 19) ^ (w[i - 2]! >>> 10);
      w[i] = (w[i - 16]! + s0 + w[i - 7]! + s1) >>> 0;
    }
    let [a, b, c, d, e, f, g, hh] = h;
    for (let i = 0; i < 64; i++) {
      const s1 = rotr(e!, 6) ^ rotr(e!, 11) ^ rotr(e!, 25);
      const ch = (e! & f!) ^ (~e! & g!);
      const t1 = (hh! + s1 + ch + k[i]! + w[i]!) >>> 0;
      const s0 = rotr(a!, 2) ^ rotr(a!, 13) ^ rotr(a!, 22);
      const maj = (a! & b!) ^ (a! & c!) ^ (b! & c!);
      const t2 = (s0 + maj) >>> 0;
      hh = g;
      g = f;
      f = e;
      e = (d! + t1) >>> 0;
      d = c;
      c = b;
      b = a;
      a = (t1 + t2) >>> 0;
    }
    h[0] = (h[0]! + a!) >>> 0;
    h[1] = (h[1]! + b!) >>> 0;
    h[2] = (h[2]! + c!) >>> 0;
    h[3] = (h[3]! + d!) >>> 0;
    h[4] = (h[4]! + e!) >>> 0;
    h[5] = (h[5]! + f!) >>> 0;
    h[6] = (h[6]! + g!) >>> 0;
    h[7] = (h[7]! + hh!) >>> 0;
  }
  return Array.from(h, (value) => value.toString(16).padStart(8, "0")).join("");
}
