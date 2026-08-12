export type Grader =
  | { type: "exact_match"; expected: unknown }
  | { type: "contains"; fragments: string[] }
  | { type: "regex"; pattern: string }
  | { type: "json_schema"; schema: Record<string, unknown> }
  | { type: "json_path_assertion"; path: string; equals?: unknown; exists?: boolean }
  | { type: "tool_called"; tools: string[] }
  | { type: "tool_not_called"; tools: string[] }
  | { type: "tool_sequence"; tools: string[] }
  | { type: "tool_argument_assertion"; tool: string; path: string; equals: unknown }
  | { type: "http_status"; status: number }
  | { type: "latency_threshold"; p95_ms: number }
  | { type: "cost_threshold"; max_usd: number }
  | { type: "token_threshold"; max_tokens: number }
  | { type: "custom_webhook"; url: string }
  | {
      type: "localization_f1";
      reference: unknown;
      file_threshold?: number;
      line_threshold?: number;
      threshold?: number;
    }
  | {
      type: "llm_judge";
      rubric: string;
      gateway_url?: string;
      model?: string;
      api_key?: string;
      upstream_key?: string;
    };

export interface GradeResult {
  passed: boolean;
  reason: string;
}

export interface GradeDeps {
  /** Override for network calls (custom_webhook, llm_judge). Defaults to global fetch. */
  fetch?: typeof fetch;
  /** Override the SSRF guard. Defaults to an IP-literal classifier (see notes). */
  ssrfCheck?: (url: string) => Promise<{ allowed: boolean; reason: string }>;
  /**
   * The model that produced the value under test (the system-under-test). When
   * set, llm_judge fails closed if the judge model shares its family — a known
   * judge-bias pitfall (a model favours its own outputs). Leave unset to skip
   * the check (e.g. deterministic graders that never call a model).
   */
  subjectModel?: string;
}

const pass = (reason: string): GradeResult => ({ passed: true, reason });
const fail = (reason: string): GradeResult => ({ passed: false, reason });

type RuntimeGraderCheck =
  | { valid: true; grader: Grader }
  | { valid: false; reason: string };

// The single source of truth for the supported grader taxonomy. Exported so
// other packages (e.g. @caveman/agent's lock evidence check) reconcile against
// it rather than drifting a hand-maintained copy (issue #143).
export const SUPPORTED_GRADER_TYPES = new Set<Grader["type"]>([
  "exact_match",
  "contains",
  "regex",
  "json_schema",
  "json_path_assertion",
  "tool_called",
  "tool_not_called",
  "tool_sequence",
  "tool_argument_assertion",
  "http_status",
  "latency_threshold",
  "cost_threshold",
  "token_threshold",
  "custom_webhook",
  "localization_f1",
  "llm_judge",
]);

function isRecord(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}

function hasOwn(record: Record<string, unknown>, key: string): boolean {
  return Object.prototype.hasOwnProperty.call(record, key);
}

function readRequiredString(record: Record<string, unknown>, key: string, allowEmpty = false): string | null {
  const value = record[key];
  return typeof value === "string" && (allowEmpty || value.length > 0) ? value : null;
}

function readRequiredStringArray(record: Record<string, unknown>, key: string): string[] | null {
  const value = record[key];
  if (!Array.isArray(value)) return null;
  // Snapshot once before checking element types. Runtime callers can provide
  // accessor-backed arrays; reading each element for validation and then again
  // while cloning could validate one value and dispatch another.
  let snapshot: unknown[];
  try {
    snapshot = Array.from(value);
  } catch {
    return null;
  }
  return snapshot.length > 0 && snapshot.every((item): item is string => typeof item === "string")
    ? snapshot
    : null;
}

function isFiniteNumber(value: unknown): value is number {
  return typeof value === "number" && Number.isFinite(value);
}

function isFiniteInteger(value: unknown): value is number {
  return isFiniteNumber(value) && Number.isInteger(value);
}

function invalidGraderConfig(type: string, field: string): RuntimeGraderCheck {
  return { valid: false, reason: `invalid ${type} grader configuration: ${field}` };
}

const JSON_SCHEMA_TYPE_NAMES = new Set([
  "object",
  "array",
  "string",
  "number",
  "integer",
  "boolean",
  "null",
]);

function isSchemaTypeName(value: unknown): value is string {
  return typeof value === "string" && JSON_SCHEMA_TYPE_NAMES.has(value);
}

// The public grader supports a deliberately small JSON-Schema subset. Validate
// keyword shapes before dispatch so malformed `required`/`properties`/`items`
// values cannot be silently ignored (or turn into a runtime exception). A
// recursion-stack guard also fails closed on cyclic runtime objects, which are
// not representable JSON schemas.
function validateSchemaDefinition(
  schema: unknown,
  path = "$",
  stack = new WeakSet<object>(),
): string[] {
  if (!isRecord(schema)) return [`${path}: schema is not an object`];
  if (stack.has(schema)) return [`${path}: schema contains a cycle`];
  stack.add(schema);
  try {
    const errors: string[] = [];
    if (hasOwn(schema, "type")) {
      const raw = schema.type;
      let types: unknown[];
      try {
        types = Array.isArray(raw) ? Array.from(raw) : [raw];
      } catch {
        types = [];
      }
      if (
        types.length === 0
        || types.some((type) => !isSchemaTypeName(type))
        || new Set(types).size !== types.length
      ) {
        errors.push(`${path}: invalid type keyword`);
      }
    }
    if (hasOwn(schema, "enum")) {
      const enumValues = schema.enum;
      if (!Array.isArray(enumValues) || enumValues.length === 0) {
        errors.push(`${path}: invalid enum keyword`);
      }
    }
    if (hasOwn(schema, "required")) {
      const rawRequired = schema.required;
      let required: unknown[];
      try {
        required = Array.isArray(rawRequired) ? Array.from(rawRequired) : [];
      } catch {
        required = [];
      }
      if (
        !Array.isArray(rawRequired)
        || required.some((key) => typeof key !== "string")
        || new Set(required).size !== required.length
      ) {
        errors.push(`${path}: invalid required keyword`);
      }
    }
    if (hasOwn(schema, "properties")) {
      const properties = schema.properties;
      if (!isRecord(properties)) {
        errors.push(`${path}: invalid properties keyword`);
      } else {
        for (const [key, subschema] of Object.entries(properties)) {
          errors.push(...validateSchemaDefinition(subschema, `${path}.properties.${key}`, stack));
        }
      }
    }
    if (hasOwn(schema, "items")) {
      errors.push(...validateSchemaDefinition(schema.items, `${path}.items`, stack));
    }
    return errors;
  } finally {
    stack.delete(schema);
  }
}

// grade() is a public JavaScript boundary even though its TypeScript signature
// uses the Grader union. Validate every required option before dispatch so
// malformed JSON/runtime callers fail closed without invoking string/array
// methods on null or undefined. Return a plain top-level snapshot so an
// accessor or mutation cannot change a validated option during dispatch. Keep
// diagnostics to fixed field names; do not echo caller-controlled values (which
// may contain secrets).
function validateGraderShape(input: unknown): RuntimeGraderCheck {
  if (!isRecord(input)) return { valid: false, reason: "invalid grader configuration" };
  const type = input.type;
  if (typeof type !== "string") return { valid: false, reason: "grader type must be a string" };
  if (!SUPPORTED_GRADER_TYPES.has(type as Grader["type"])) return { valid: false, reason: "unknown grader type" };

  switch (type) {
    case "exact_match": {
      if (!hasOwn(input, "expected")) return invalidGraderConfig(type, "expected");
      return { valid: true, grader: { type, expected: input.expected } };
    }
    case "contains": {
      const fragments = readRequiredStringArray(input, "fragments");
      return fragments === null
        ? invalidGraderConfig(type, "fragments")
        : { valid: true, grader: { type, fragments } };
    }
    case "regex": {
      const pattern = readRequiredString(input, "pattern", true);
      return pattern === null
        ? invalidGraderConfig(type, "pattern")
        : { valid: true, grader: { type, pattern } };
    }
    case "json_schema": {
      if (!hasOwn(input, "schema")) return invalidGraderConfig(type, "schema");
      const schema = input.schema;
      return validateSchemaDefinition(schema).length === 0
        ? { valid: true, grader: { type, schema: schema as Record<string, unknown> } }
        : invalidGraderConfig(type, "schema");
    }
    case "json_path_assertion": {
      const path = readRequiredString(input, "path");
      if (path === null) return invalidGraderConfig(type, "path");
      const snapshot: Extract<Grader, { type: "json_path_assertion" }> = { type, path };
      if (hasOwn(input, "exists")) {
        const exists = input.exists;
        if (typeof exists !== "boolean") return invalidGraderConfig(type, "exists");
        snapshot.exists = exists;
      }
      if (hasOwn(input, "equals")) snapshot.equals = input.equals;
      return { valid: true, grader: snapshot };
    }
    case "tool_called":
    case "tool_not_called":
    case "tool_sequence": {
      const tools = readRequiredStringArray(input, "tools");
      return tools === null
        ? invalidGraderConfig(type, "tools")
        : { valid: true, grader: { type, tools } };
    }
    case "tool_argument_assertion": {
      const tool = readRequiredString(input, "tool");
      if (tool === null) return invalidGraderConfig(type, "tool");
      const path = readRequiredString(input, "path");
      if (path === null) return invalidGraderConfig(type, "path");
      if (!hasOwn(input, "equals")) return invalidGraderConfig(type, "equals");
      return { valid: true, grader: { type, tool, path, equals: input.equals } };
    }
    case "http_status": {
      const status = input.status;
      return isFiniteInteger(status)
        ? { valid: true, grader: { type, status } }
        : invalidGraderConfig(type, "status");
    }
    case "latency_threshold": {
      const p95_ms = input.p95_ms;
      return isFiniteNumber(p95_ms)
        ? { valid: true, grader: { type, p95_ms } }
        : invalidGraderConfig(type, "p95_ms");
    }
    case "cost_threshold": {
      const max_usd = input.max_usd;
      return isFiniteNumber(max_usd)
        ? { valid: true, grader: { type, max_usd } }
        : invalidGraderConfig(type, "max_usd");
    }
    case "token_threshold": {
      const max_tokens = input.max_tokens;
      return isFiniteNumber(max_tokens)
        ? { valid: true, grader: { type, max_tokens } }
        : invalidGraderConfig(type, "max_tokens");
    }
    case "custom_webhook": {
      const url = readRequiredString(input, "url");
      return url === null
        ? invalidGraderConfig(type, "url")
        : { valid: true, grader: { type, url } };
    }
    case "localization_f1": {
      if (!hasOwn(input, "reference")) return invalidGraderConfig(type, "reference");
      const reference = input.reference;
      const thresholds: Pick<Extract<Grader, { type: "localization_f1" }>, "threshold" | "file_threshold" | "line_threshold"> = {};
      for (const key of ["threshold", "file_threshold", "line_threshold"]) {
        if (!hasOwn(input, key)) continue;
        const value = input[key];
        if (!isFiniteNumber(value) || value < 0 || value > 1) {
          return { valid: false, reason: "localization_f1: invalid threshold" };
        }
        if (key === "threshold") thresholds.threshold = value;
        else if (key === "file_threshold") thresholds.file_threshold = value;
        else thresholds.line_threshold = value;
      }
      return { valid: true, grader: { type, reference, ...thresholds } };
    }
    case "llm_judge": {
      const rubric = readRequiredString(input, "rubric");
      if (rubric === null) return invalidGraderConfig(type, "rubric");
      const gateway_url = readRequiredString(input, "gateway_url");
      if (gateway_url === null) return invalidGraderConfig(type, "gateway_url");
      const snapshot: Extract<Grader, { type: "llm_judge" }> = { type, rubric, gateway_url };
      for (const key of ["model", "api_key", "upstream_key"] as const) {
        if (!hasOwn(input, key)) continue;
        const value = input[key];
        if (typeof value !== "string") return invalidGraderConfig(type, key);
        snapshot[key] = value;
      }
      return { valid: true, grader: snapshot };
    }
    default:
      // Keep this branch for exhaustive/runtime mutation safety. Unknown types
      // are never echoed or dispatched.
      return { valid: false, reason: "unknown grader type" };
  }
}

function asRecord(value: unknown): Record<string, unknown> {
  return value && typeof value === "object" ? (value as Record<string, unknown>) : {};
}

function asText(value: unknown): string {
  return typeof value === "string" ? value : JSON.stringify(value);
}

// stableStringify serialises with object keys sorted recursively, mirroring
// Python's json.dumps(..., sort_keys=True) so structured values compare
// independent of key order.
function stableStringify(value: unknown): string {
  if (value === null || typeof value !== "object") return JSON.stringify(value);
  if (Array.isArray(value)) return "[" + value.map(stableStringify).join(",") + "]";
  const obj = value as Record<string, unknown>;
  return "{" + Object.keys(obj).sort().map((k) => JSON.stringify(k) + ":" + stableStringify(obj[k])).join(",") + "}";
}

// jsonEqual implements JSON Schema's JSON-value equality rather than
// JavaScript's object identity or insertion-order-sensitive serialization:
// booleans never equal numbers, numbers compare by mathematical value (so
// 1 and 1.0 are equal), arrays retain order, and object keys are unordered.
function jsonEqual(left: unknown, right: unknown): boolean {
  if (left === null || right === null) return left === right;
  if (typeof left !== typeof right) return false;
  if (typeof left === "boolean" || typeof left === "string" || typeof left === "number") {
    return left === right;
  }
  if (Array.isArray(left) || Array.isArray(right)) {
    if (!Array.isArray(left) || !Array.isArray(right) || left.length !== right.length) return false;
    return left.every((item, index) => jsonEqual(item, right[index]));
  }
  if (typeof left !== "object" || typeof right !== "object") return false;
  const leftObject = left as Record<string, unknown>;
  const rightObject = right as Record<string, unknown>;
  const leftKeys = Object.keys(leftObject);
  const rightKeys = Object.keys(rightObject);
  if (leftKeys.length !== rightKeys.length) return false;
  return leftKeys.every((key) => Object.prototype.hasOwnProperty.call(rightObject, key)
    && jsonEqual(leftObject[key], rightObject[key]));
}

// normaliseExact mirrors the Python `_normalise_str` used by exact_match: a string
// is trimmed + lowercased; any other value is serialised with sorted keys then
// lowercased. (toLowerCase ≈ Python casefold for ASCII; Unicode-casefold-only
// edge cases like ß→ss are not normalised, an accepted minor residual.)
function normaliseExact(value: unknown): string {
  if (typeof value === "string") return value.trim().toLowerCase();
  return stableStringify(value).toLowerCase();
}

// ─── tool-call extraction ────────────────────────────────────────────────────

interface ToolCall {
  name: string;
  arguments: unknown;
}

function extractToolCalls(value: unknown): ToolCall[] {
  let raw: unknown;
  if (Array.isArray(value)) {
    raw = value;
  } else {
    const rec = asRecord(value);
    raw = rec.tool_calls ?? rec.tools_called ?? rec.tools ?? [];
  }
  if (!Array.isArray(raw)) return [];
  const calls: ToolCall[] = [];
  for (const item of raw) {
    if (typeof item === "string") {
      calls.push({ name: item, arguments: {} });
      continue;
    }
    if (!item || typeof item !== "object") continue;
    const rec = item as Record<string, unknown>;
    const fn = rec.function && typeof rec.function === "object" ? (rec.function as Record<string, unknown>) : {};
    const name = rec.name ?? rec.tool ?? fn.name;
    let args = rec.arguments ?? rec.args ?? fn.arguments;
    if (typeof args === "string") {
      try {
        args = JSON.parse(args);
      } catch {
        /* leave as string */
      }
    }
    if (typeof name === "string" && name) calls.push({ name, arguments: args ?? {} });
  }
  return calls;
}

// ─── json path ───────────────────────────────────────────────────────────────

function jsonPathGet(obj: unknown, path: string): { found: boolean; value: unknown } {
  let cur: unknown = obj;
  for (const part of path.split(".")) {
    if (isRecord(cur)) {
      if (!hasOwn(cur, part)) return { found: false, value: undefined };
      cur = cur[part];
    } else if (Array.isArray(cur)) {
      const idx = Number(part);
      if (!Number.isInteger(idx) || idx < 0 || idx >= cur.length) return { found: false, value: undefined };
      if (!Object.prototype.hasOwnProperty.call(cur, String(idx))) return { found: false, value: undefined };
      cur = cur[idx];
    } else {
      return { found: false, value: undefined };
    }
  }
  return { found: true, value: cur };
}

// ─── minimal JSON-schema subset validator (type/enum/required/properties/items) ──

function typeOk(value: unknown, typeName: string): boolean {
  switch (typeName) {
    case "object":
      return value !== null && typeof value === "object" && !Array.isArray(value);
    case "array":
      return Array.isArray(value);
    case "string":
      return typeof value === "string";
    case "number":
      return typeof value === "number" && Number.isFinite(value);
    case "integer":
      return typeof value === "number" && Number.isFinite(value) && Number.isInteger(value);
    case "boolean":
      return typeof value === "boolean";
    case "null":
      return value === null;
    default:
      // Fail CLOSED: the 7 cases above are the complete JSON-Schema primitive
      // type set, so any other value (e.g. a misspelled "strng") is an invalid
      // schema — treat it as unsatisfied, never silently pass.
      return false;
  }
}

function validateSchema(value: unknown, schema: unknown, path = "$"): string[] {
  if (!isRecord(schema)) return [`${path}: schema is not an object`];
  const s = schema;
  const errors: string[] = [];
  if (hasOwn(s, "type")) {
    const types: unknown[] = Array.isArray(s.type) ? s.type : [s.type];
    if (!types.some((type) => typeof type === "string" && typeOk(value, type))) {
      return [`${path}: expected type ${JSON.stringify(s.type)}`];
    }
  }
  if (hasOwn(s, "enum")) {
    if (!Array.isArray(s.enum) || !s.enum.some((e) => jsonEqual(e, value))) {
      errors.push(`${path}: value not in enum`);
    }
  }
  if (value && typeof value === "object" && !Array.isArray(value)) {
    const rec = value as Record<string, unknown>;
    if (hasOwn(s, "required")) {
      if (!Array.isArray(s.required)) {
        errors.push(`${path}: invalid required keyword`);
      } else {
        for (const req of s.required) {
          if (typeof req === "string" && !hasOwn(rec, req)) {
            errors.push(`${path}: missing required key ${JSON.stringify(req)}`);
          }
        }
      }
    }
    if (hasOwn(s, "properties")) {
      if (!isRecord(s.properties)) {
        errors.push(`${path}: invalid properties keyword`);
      } else {
        for (const [key, sub] of Object.entries(s.properties)) {
          if (hasOwn(rec, key)) errors.push(...validateSchema(rec[key], sub, `${path}.${key}`));
        }
      }
    }
  }
  if (Array.isArray(value) && hasOwn(s, "items")) {
    if (!isRecord(s.items)) {
      errors.push(`${path}: invalid items keyword`);
    } else {
      value.forEach((item, i) => errors.push(...validateSchema(item, s.items, `${path}[${i}]`)));
    }
  }
  return errors;
}

// ─── SSRF guard (default: IP-literal classifier) ─────────────────────────────
// Without node DNS types in this lib, the default guard classifies IP literals
// (catching 169.254.169.254 and other private/loopback/link-local ranges) and
// fails CLOSED on bare hostnames so a hostname cannot smuggle a private address.
// Inject deps.ssrfCheck (resolver-backed) for hostname targets.

function classifyIp(ip: string): "blocked" | "ok" {
  const m = ip.match(/^(\d{1,3})\.(\d{1,3})\.(\d{1,3})\.(\d{1,3})$/);
  if (m) {
    const a = Number(m[1]);
    const b = Number(m[2]);
    if (a === 127 || a === 10 || a === 0 || a >= 224) return "blocked";
    if (a === 172 && b >= 16 && b <= 31) return "blocked";
    if (a === 192 && b === 168) return "blocked";
    if (a === 169 && b === 254) return "blocked"; // link-local incl. cloud metadata
    if (a === 100 && b >= 64 && b <= 127) return "blocked"; // CGNAT
    return "ok";
  }
  const lo = (ip.toLowerCase().split("%")[0] ?? "");
  if (lo === "::1" || lo === "::") return "blocked";
  if (lo.startsWith("fe80") || lo.startsWith("fc") || lo.startsWith("fd") || lo.startsWith("ff")) return "blocked";
  const mapped = lo.match(/^::ffff:(\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3})$/);
  if (mapped && mapped[1]) return classifyIp(mapped[1]);
  return "ok";
}

function isIpLiteral(host: string): boolean {
  return /^\d{1,3}(\.\d{1,3}){3}$/.test(host) || host.includes(":");
}

async function defaultSsrfCheck(url: string): Promise<{ allowed: boolean; reason: string }> {
  let parsed: URL;
  try {
    parsed = new URL(url);
  } catch {
    return { allowed: false, reason: "unparseable url" };
  }
  if (parsed.protocol !== "http:" && parsed.protocol !== "https:") {
    return { allowed: false, reason: `scheme ${parsed.protocol} not allowed` };
  }
  const host = parsed.hostname.replace(/^\[/, "").replace(/\]$/, "");
  if (isIpLiteral(host)) {
    return classifyIp(host) === "blocked"
      ? { allowed: false, reason: `blocked address ${host}` }
      : { allowed: true, reason: "ok" };
  }
  return { allowed: false, reason: `cannot verify hostname ${host}; inject ssrfCheck for hostname targets` };
}

// ─── network graders ──────────────────────────────────────────────────────────

// Fetch resolves HTTP error statuses instead of throwing.  Only an actual
// Response whose status is an integer 2xx may authorize a grader verdict.  The
// explicit status check also keeps injected Response-like test doubles from
// bypassing the fail-closed path with an inconsistent `ok` flag.
function isSuccessfulResponse(resp: Response): boolean {
  return resp.ok === true && Number.isInteger(resp.status) && resp.status >= 200 && resp.status <= 299;
}

async function gradeCustomWebhook(url: string, value: unknown, deps: GradeDeps): Promise<GradeResult> {
  if (!url) return fail("custom_webhook requires url");
  const ssrf = deps.ssrfCheck ?? defaultSsrfCheck;
  const verdict = await ssrf(url);
  if (verdict?.allowed !== true) return fail("url blocked by SSRF guard");
  const doFetch = deps.fetch ?? fetch;
  let body: unknown;
  try {
    const resp = await doFetch(url, {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify({ candidate: value }),
    });
    // Fail CLOSED on a non-2xx webhook: a 500 with {"passed":true} must NOT pass.
    // (Mirrors Python, where urllib raises HTTPError on 4xx/5xx → fail.)
    if (!isSuccessfulResponse(resp)) return fail(`webhook returned non-2xx (${resp.status ?? "error"})`);
    body = await resp.json();
  } catch {
    // Do not reflect transport/provider exception text: it can contain URLs,
    // request material, or injected secret values. The public boundary only
    // needs a deterministic closed verdict.
    return fail("webhook call failed");
  }
  if (!body || typeof body !== "object" || Array.isArray(body)) return fail("webhook response must be an object");
  const rec = body as Record<string, unknown>;
  if (!Object.prototype.hasOwnProperty.call(rec, "passed") || typeof rec.passed !== "boolean") {
    return fail("webhook response 'passed' must be a primitive boolean");
  }
  return rec.passed ? pass(String(rec.reason ?? "webhook passed")) : fail(String(rec.reason ?? "webhook failed"));
}

// modelFamily maps a model id to a coarse provider family so the judge-bias
// guard can tell whether the judge and the system-under-test are the same kind
// of model. Provider prefixes ("anthropic/", "us.anthropic.") are stripped
// first. Unknown ids return themselves, so an identical id still matches itself
// (the most important case: judge == SUT) without over-claiming family ties.
export function modelFamily(model: string): string {
  const id = (model ?? "").trim().toLowerCase().replace(/^[a-z0-9]+[./]/, "");
  if (!id) return "";
  const families: [string, RegExp][] = [
    ["anthropic", /claude/],
    ["openai", /gpt|chatgpt|davinci|^o\d/],
    ["google", /gemini|palm|bison/],
    ["meta", /llama/],
    ["mistral", /mistral|mixtral/],
    ["cohere", /command|cohere/],
    ["deepseek", /deepseek/],
    ["qwen", /qwen/],
  ];
  for (const [fam, re] of families) if (re.test(id)) return fam;
  return id;
}

function parseVerdict(text: string): boolean | null {
  const upper = text.trim().toUpperCase();
  if (upper.startsWith("PASS")) return true;
  if (upper.startsWith("FAIL")) return false;
  const hasPass = upper.includes("PASS");
  const hasFail = upper.includes("FAIL");
  if (hasPass && !hasFail) return true;
  if (hasFail && !hasPass) return false;
  return null;
}

function extractModelText(data: unknown): string {
  if (typeof data === "string") return data;
  const rec = asRecord(data);
  if (typeof rec.output_text === "string") return rec.output_text;
  if (Array.isArray(rec.choices) && rec.choices[0]) {
    const c = asRecord(rec.choices[0]);
    const msg = asRecord(c.message);
    if (typeof msg.content === "string") return msg.content;
    if (typeof c.text === "string") return c.text;
  }
  if (Array.isArray(rec.content)) {
    return rec.content.map((p) => (typeof p === "object" && p ? String((p as Record<string, unknown>).text ?? "") : "")).join(" ");
  }
  if (typeof rec.content === "string") return rec.content;
  if (typeof rec.text === "string") return rec.text;
  return "";
}

async function gradeLlmJudge(
  grader: Extract<Grader, { type: "llm_judge" }>,
  value: unknown,
  deps: GradeDeps,
): Promise<GradeResult> {
  if (!grader.rubric || !grader.rubric.trim()) return fail("llm_judge requires rubric");
  if (!grader.gateway_url) return fail("llm_judge requires gateway_url");
  const judgeModel = grader.model ?? "gpt-5.5";
  // Judge-bias guard: a model tends to favour its own outputs, so the judge must
  // be a different family than the system-under-test. Fail closed when they match.
  if (deps.subjectModel && deps.subjectModel.trim()) {
    const judgeFamily = modelFamily(judgeModel);
    const subjectFamily = modelFamily(deps.subjectModel);
    if (judgeFamily && judgeFamily === subjectFamily) {
      return fail(
        `llm_judge bias guard: judge model "${judgeModel}" shares family "${judgeFamily}" with system-under-test "${deps.subjectModel}"; use a different model family as judge`,
      );
    }
  }
  const ssrf = deps.ssrfCheck ?? defaultSsrfCheck;
  const verdict = await ssrf(grader.gateway_url);
  if (verdict?.allowed !== true) return fail("gateway url blocked by SSRF guard");
  const doFetch = deps.fetch ?? fetch;
  const prompt = `${grader.rubric}\n\nOutput under test:\n${asText(value)}\n\nRespond with exactly PASS or FAIL.`;
  let data: unknown;
  try {
    const resp = await doFetch(`${grader.gateway_url.replace(/\/$/, "")}/openai/v1/responses`, {
      method: "POST",
      headers: {
        authorization: `Bearer ${grader.api_key ?? ""}`,
        "x-cave-upstream-key": grader.upstream_key ?? "stub",
        "content-type": "application/json",
      },
      body: JSON.stringify({ model: judgeModel, input: prompt }),
    });
    // HTTP errors are resolved responses in Fetch, not thrown exceptions. Do
    // not parse an error body: a 4xx/5xx body containing "PASS" is not a judge
    // verdict and must fail closed.
    if (!isSuccessfulResponse(resp)) return fail(`judge gateway returned non-2xx (${resp.status ?? "error"})`);
    data = await resp.json();
  } catch {
    // Keep provider/transport details out of the signed grader result.
    return fail("judge call failed");
  }
  const result = parseVerdict(extractModelText(data));
  // Record the judge model+version in the verdict reason so the signed report
  // shows who judged (spec §5.3: report judge model + version).
  if (result === null) return fail(`judge(${judgeModel}) returned no PASS/FAIL verdict`);
  return result ? pass(`judge(${judgeModel}) returned PASS`) : fail(`judge(${judgeModel}) returned FAIL`);
}

// ─── localization_f1 (explorer evidence ⇄ gold localization) ───────────────────
// Parses an explorer evidence block (a list of {path, lines:[[start,end],...]},
// or compact "path:start-end" text lines) and a gold localization set of the same
// shape, then scores file-level F1 (over the set of cited files) AND line-range F1
// (mean IoU of cited vs gold line ranges per shared file). Fails CLOSED: a missing
// or unparseable candidate/reference returns passed:false, never true. Kept
// byte-identical in verdict with cloud/optimizer grade_localization_f1.

type LineRange = [number, number];
type LocSet = Map<string, LineRange[]>;

// parseOneRange parses "start-end" (inclusive) or a bare "start" (single line).
// Non-integer / unparseable input => null (caller fails closed). Mirrors Python int().
function parseOneRange(text: string): LineRange | null {
  const t = text.trim();
  if (t === "") return null;
  let start: number;
  let end: number;
  const dash = t.indexOf("-");
  if (dash >= 0) {
    start = Number(t.slice(0, dash).trim());
    end = Number(t.slice(dash + 1).trim());
  } else {
    start = Number(t);
    end = start;
  }
  if (!Number.isInteger(start) || !Number.isInteger(end)) return null;
  return start > end ? [end, start] : [start, end];
}

// parseCompactLine parses one "path:start-end" line. A line with no colon is a
// bare path (file-level citation, no ranges). Unparseable range => null.
function parseCompactLine(line: string): { path: string; ranges: LineRange[] } | null {
  const t = line.trim();
  if (t === "") return null;
  const colon = t.lastIndexOf(":");
  if (colon < 0) return { path: t, ranges: [] };
  const path = t.slice(0, colon);
  if (path === "") return null;
  const r = parseOneRange(t.slice(colon + 1));
  if (r === null) return null;
  return { path, ranges: [r] };
}

// parseRanges parses a structured "lines" field: [[start,end], ...]. Absent => [].
// Any malformed pair => null (fail closed).
function parseRanges(raw: unknown): LineRange[] | null {
  if (raw === undefined || raw === null) return [];
  if (!Array.isArray(raw)) return null;
  const out: LineRange[] = [];
  for (const pair of raw) {
    if (!Array.isArray(pair) || pair.length !== 2) return null;
    const start = Number(pair[0]);
    const end = Number(pair[1]);
    if (!Number.isInteger(start) || !Number.isInteger(end)) return null;
    out.push(start > end ? [end, start] : [start, end]);
  }
  return out;
}

// parseLocSet normalises a candidate/reference into Map<path, ranges[]>. Returns
// null when the input is missing, the wrong shape, empty, or any line is
// unparseable — every such case fails the grader CLOSED.
function parseLocSet(value: unknown): LocSet | null {
  if (value === null || value === undefined) return null;
  const result: LocSet = new Map();
  const add = (path: string, ranges: LineRange[]): void => {
    const existing = result.get(path);
    if (existing) existing.push(...ranges);
    else result.set(path, [...ranges]);
  };
  if (typeof value === "string") {
    const lines = value.split(/\r?\n/).map((l) => l.trim()).filter((l) => l !== "");
    if (lines.length === 0) return null;
    for (const line of lines) {
      const parsed = parseCompactLine(line);
      if (!parsed) return null;
      add(parsed.path, parsed.ranges);
    }
    return result;
  }
  let items: unknown[];
  if (Array.isArray(value)) items = value;
  else if (typeof value === "object") items = [value];
  else return null;
  if (items.length === 0) return null;
  for (const item of items) {
    if (typeof item === "string") {
      const parsed = parseCompactLine(item);
      if (!parsed) return null;
      add(parsed.path, parsed.ranges);
    } else if (item && typeof item === "object" && !Array.isArray(item)) {
      const rec = item as Record<string, unknown>;
      if (typeof rec.path !== "string" || rec.path === "") return null;
      const ranges = parseRanges(rec.lines);
      if (ranges === null) return null;
      add(rec.path, ranges);
    } else {
      return null;
    }
  }
  return result;
}

// mergeRanges collapses ranges into sorted, disjoint intervals so coverage length
// is counted without double-counting overlaps (line sets are inclusive integers).
function mergeRanges(ranges: LineRange[]): LineRange[] {
  const sorted = [...ranges].sort((a, b) => a[0] - b[0] || a[1] - b[1]);
  const out: LineRange[] = [];
  for (const [s, e] of sorted) {
    const last = out[out.length - 1];
    if (last && s <= last[1] + 1) last[1] = Math.max(last[1], e);
    else out.push([s, e]);
  }
  return out;
}

function coveredLength(merged: LineRange[]): number {
  let total = 0;
  for (const [s, e] of merged) total += e - s + 1;
  return total;
}

// intersectionLength is the count of integer lines covered by BOTH merged sets.
function intersectionLength(a: LineRange[], b: LineRange[]): number {
  let i = 0;
  let j = 0;
  let total = 0;
  while (i < a.length && j < b.length) {
    const ai = a[i];
    const bj = b[j];
    if (!ai || !bj) break;
    const s = Math.max(ai[0], bj[0]);
    const e = Math.min(ai[1], bj[1]);
    if (e >= s) total += e - s + 1;
    if (ai[1] < bj[1]) i++;
    else j++;
  }
  return total;
}

function f1Score(tp: number, candCount: number, goldCount: number): number {
  const precision = candCount > 0 ? tp / candCount : 0;
  const recall = goldCount > 0 ? tp / goldCount : 0;
  if (precision + recall === 0) return 0;
  return (2 * precision * recall) / (precision + recall);
}

type LocalizationThresholds = {
  defaultThreshold: number;
  fileThreshold: number;
  lineThreshold: number;
};

function localizationThresholds(
  grader: Extract<Grader, { type: "localization_f1" }>,
): LocalizationThresholds | null {
  const hasOwn = (key: string): boolean => Object.prototype.hasOwnProperty.call(grader, key);
  const read = (key: string, fallback: number): number | null => {
    if (!hasOwn(key)) return fallback;
    const value = (grader as Record<string, unknown>)[key];
    if (typeof value !== "number" || !Number.isFinite(value) || value < 0 || value > 1) return null;
    return value;
  };

  const defaultThreshold = read("threshold", 0.5);
  if (defaultThreshold === null) return null;
  const fileThreshold = read("file_threshold", defaultThreshold);
  const lineThreshold = read("line_threshold", defaultThreshold);
  if (fileThreshold === null || lineThreshold === null) return null;
  return { defaultThreshold, fileThreshold, lineThreshold };
}

function gradeLocalizationF1(grader: Extract<Grader, { type: "localization_f1" }>, value: unknown): GradeResult {
  const cand = parseLocSet(value);
  if (!cand) return fail("localization_f1: unparseable or empty candidate");
  const gold = parseLocSet(grader.reference);
  if (!gold) return fail("localization_f1: unparseable or empty reference");

  const thresholds = localizationThresholds(grader);
  if (!thresholds) return fail("localization_f1: invalid threshold");
  const { fileThreshold: fileThr, lineThreshold: lineThr } = thresholds;

  const goldFiles = new Set(gold.keys());
  const shared = [...cand.keys()].filter((f) => goldFiles.has(f)).sort();
  const fileF1 = f1Score(shared.length, cand.size, gold.size);

  let iouSum = 0;
  for (const f of shared) {
    const a = mergeRanges(cand.get(f) ?? []);
    const b = mergeRanges(gold.get(f) ?? []);
    const lenA = coveredLength(a);
    const lenB = coveredLength(b);
    const inter = intersectionLength(a, b);
    const union = lenA + lenB - inter;
    iouSum += union === 0 ? 1 : inter / union;
  }
  const lineF1 = shared.length > 0 ? iouSum / shared.length : 0;

  // A zero-quality candidate cannot satisfy a quality gate even when an
  // author explicitly configures the valid boundary threshold 0. Exact
  // file/line matches with threshold 0 remain valid and pass.
  const passed = fileF1 > 0 && lineF1 > 0 && fileF1 >= fileThr && lineF1 >= lineThr;
  const detail = `file_f1=${fileF1.toFixed(4)} line_f1=${lineF1.toFixed(4)} (thresholds file=${fileThr} line=${lineThr})`;
  return passed ? pass(`localization_f1 ${detail}`) : fail(`localization_f1 ${detail}`);
}

// ─── dispatch ──────────────────────────────────────────────────────────────────

export async function grade(grader: Grader, value: unknown, deps: GradeDeps = {}): Promise<GradeResult> {
  try {
    const checked = validateGraderShape(grader);
    if (!checked.valid) return fail(checked.reason);
    // An omitted candidate is not evidence. Reject it before any helper can
    // stringify it as the literal text "undefined" and accidentally match.
    if (value === undefined) return fail("candidate is missing");
    const validGrader = checked.grader;
    switch (validGrader.type) {
      case "exact_match":
        // Mirror the Python grader (the live /v1/grade service): normalise both
        // sides — case-insensitive, and key-order-insensitive for structured values
        // — so the same input yields the same verdict in both languages. Previously
        // raw JSON.stringify made TS case/order-sensitive (diverged from Python).
        return normaliseExact(value) === normaliseExact(validGrader.expected)
          ? pass("normalised values equal")
          : fail("normalised values differ");
      case "contains": {
        const text = asText(value);
        const missing = validGrader.fragments.filter((f) => !text.includes(f));
        return missing.length === 0 ? pass("all fragments present") : fail(`missing fragments: ${JSON.stringify(missing)}`);
      }
      case "regex": {
        let re: RegExp;
        try {
          re = new RegExp(validGrader.pattern);
        } catch {
          return fail("invalid regex pattern");
        }
        return re.test(String(value)) ? pass("regex matched") : fail("regex did not match");
      }
      case "json_schema": {
        let v = value;
        if (typeof value === "string") {
          try {
            v = JSON.parse(value);
          } catch {
            return fail("candidate is not valid JSON");
          }
        }
        const errors = validateSchema(v, validGrader.schema);
        return errors.length === 0 ? pass("candidate satisfies schema") : fail(errors.slice(0, 5).join("; "));
      }
      case "json_path_assertion": {
        let v = value;
        if (typeof value === "string") {
          try {
            v = JSON.parse(value);
          } catch {
            /* keep string */
          }
        }
        const { found, value: resolved } = jsonPathGet(v, validGrader.path);
        if (validGrader.exists === true) return found ? pass(`path ${validGrader.path} exists`) : fail(`path ${validGrader.path} not found`);
        if (validGrader.exists === false) return !found ? pass(`path ${validGrader.path} absent`) : fail(`path ${validGrader.path} present`);
        if (!found) return fail(`path ${validGrader.path} not found`);
        if (hasOwn(validGrader, "equals")) {
          return stableStringify(resolved) === stableStringify(validGrader.equals)
            ? pass(`path ${validGrader.path} equals expected`)
            : fail(`path ${validGrader.path} value ${JSON.stringify(resolved)} != expected`);
        }
        return pass(`path ${validGrader.path} exists`);
      }
      case "tool_called": {
        const called = new Set(extractToolCalls(value).map((c) => c.name));
        const missing = validGrader.tools.filter((t) => !called.has(t));
        return missing.length === 0 ? pass("all required tools called") : fail(`tools not called: ${JSON.stringify(missing)}`);
      }
      case "tool_not_called": {
        const called = new Set(extractToolCalls(value).map((c) => c.name));
        const present = validGrader.tools.filter((t) => called.has(t));
        return present.length === 0 ? pass("no forbidden tools called") : fail(`forbidden tools called: ${JSON.stringify(present)}`);
      }
      case "tool_sequence": {
        const called = extractToolCalls(value).map((c) => c.name);
        let i = 0;
        for (const name of called) {
          if (validGrader.tools[i] === name) i++;
        }
        return i === validGrader.tools.length
          ? pass("tools called in order")
          : fail(`expected ordered subsequence ${JSON.stringify(validGrader.tools)}`);
      }
      case "tool_argument_assertion": {
        for (const call of extractToolCalls(value)) {
          if (call.name !== validGrader.tool) continue;
          const { found, value: arg } = jsonPathGet(call.arguments, validGrader.path);
          if (found && stableStringify(arg) === stableStringify(validGrader.equals)) {
            return pass(`${validGrader.tool}.${validGrader.path} equals expected`);
          }
        }
        return fail(`no call to ${validGrader.tool} with ${validGrader.path} == expected`);
      }
      case "http_status":
        return Number(asRecord(value).status ?? asRecord(value).status_code) === validGrader.status
          ? pass(`status ${validGrader.status}`)
          : fail("status mismatch");
      case "latency_threshold": {
        const v = Number(asRecord(value).p95_ms ?? asRecord(value).latency_ms);
        if (!Number.isFinite(v)) return fail("candidate has no latency");
        return v <= validGrader.p95_ms ? pass(`p95 ${v}ms <= ${validGrader.p95_ms}ms`) : fail(`p95 ${v}ms > ${validGrader.p95_ms}ms`);
      }
      case "cost_threshold": {
        // Fail CLOSED on absent cost: a candidate never measured can't pass a ceiling.
        const v = Number(asRecord(value).cost_usd ?? asRecord(value).total_cost_usd);
        if (!Number.isFinite(v)) return fail("candidate has no cost");
        return v <= validGrader.max_usd ? pass(`cost ${v} <= ${validGrader.max_usd}`) : fail(`cost ${v} > ${validGrader.max_usd}`);
      }
      case "token_threshold": {
        // Fail CLOSED on absent token count: an unmeasured candidate can't pass.
        const v = Number(asRecord(value).tokens ?? asRecord(value).total_tokens);
        if (!Number.isFinite(v)) return fail("candidate has no tokens");
        return v <= validGrader.max_tokens ? pass(`tokens ${v} <= ${validGrader.max_tokens}`) : fail(`tokens ${v} > ${validGrader.max_tokens}`);
      }
      case "custom_webhook":
        return await gradeCustomWebhook(validGrader.url, value, deps);
      case "localization_f1":
        return gradeLocalizationF1(validGrader, value);
      case "llm_judge":
        return await gradeLlmJudge(validGrader, value, deps);
      default:
        // Keep this branch for exhaustive/runtime mutation safety. Unknown types
        // are never echoed or dispatched.
        return fail("unknown grader type");
    }
  } catch {
    // The eval API is a fail-closed boundary. A malformed candidate, config,
    // dependency, or helper exception must become a deterministic failed
    // GradeResult rather than an unhandled rejection. Keep exception details
    // out of the result to avoid reflecting secrets or unbounded values.
    return fail("grader failed closed");
  }
}
