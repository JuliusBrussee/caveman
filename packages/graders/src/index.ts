import { Worker } from "node:worker_threads";

export type Grader =
  | {
      type: "exact_match";
      expected: unknown;
      /** Default false: compare case-insensitively (today's behaviour). */
      case_sensitive?: boolean;
      /** Default false: strip ASCII punctuation before comparing. */
      remove_punctuation?: boolean;
    }
  | { type: "contains"; fragments: string[] }
  | { type: "not_contains"; fragments: string[] }
  | { type: "regex"; pattern: string }
  | { type: "not_regex"; pattern: string }
  | { type: "blocklist"; terms: string[] }
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
  | { type: "bleu_score"; reference: string; threshold?: number }
  | {
      type: "rouge_score";
      reference: string;
      rouge_type?: "rouge_1" | "rouge_l";
      measure?: "precision" | "recall" | "fmeasure";
      threshold?: number;
    }
  | {
      type: "context_f1";
      retrieved: string[];
      expected: string[];
      measure?: "precision" | "recall" | "f1";
      similarity_threshold?: number;
      threshold?: number;
    }
  | { type: "no_pii"; entities?: string[] }
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
    }
  // The four langevals judge types share `llm_judge`'s transport options verbatim
  // (gateway_url / model / api_key / upstream_key) — see JudgeTransport.
  | ({ type: "llm_score"; rubric: string; min_score: number } & JudgeTransport)
  | ({
      type: "llm_category";
      prompt: string;
      categories: string[];
      passing_categories: string[];
    } & JudgeTransport)
  | ({ type: "llm_pairwise"; baseline: string; criteria: string } & JudgeTransport)
  | ({ type: "llm_answer_match"; expected: string } & JudgeTransport);

/** Public taxonomy used by build/evidence validators outside this package. */
export const SUPPORTED_GRADER_TYPES = new Set<Grader["type"]>([
  "exact_match",
  "contains",
  "not_contains",
  "regex",
  "not_regex",
  "blocklist",
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
  "bleu_score",
  "rouge_score",
  "context_f1",
  "no_pii",
  "custom_webhook",
  "localization_f1",
  "llm_judge",
  "llm_score",
  "llm_category",
  "llm_pairwise",
  "llm_answer_match",
]);

export interface GradeResult {
  passed: boolean;
  reason: string;
}

export interface GradeDeps {
  /** Override for IP-literal network calls. Hostnames never use this capability. */
  fetch?: typeof fetch;
  /**
   * Transport that pins SSRF-approved DNS results through socket connect.
   * Required for hostname targets; naming the capability separately prevents
   * an ordinary fetch wrapper from accidentally claiming rebinding safety.
   */
  resolutionPinnedFetch?: typeof fetch;
  /** Override the SSRF guard. Defaults to an IP-literal classifier (see notes). */
  ssrfCheck?: (url: string) => Promise<{ allowed: boolean; reason: string }>;
  /** One deadline for network headers plus body parsing. Default 10 seconds. */
  networkTimeoutMs?: number;
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

// ASCII_PUNCTUATION is the full 32-character ASCII punctuation set, used by the
// optional exact_match `remove_punctuation` knob.
const ASCII_PUNCTUATION = /[!"#$%&'()*+,\-./:;<=>?@[\\\]^_`{|}~]/g;

// normaliseExact mirrors the Python `_normalise_str` used by exact_match: a string
// is trimmed + lowercased; any other value is serialised with sorted keys then
// lowercased. (toLowerCase ≈ Python casefold for ASCII; Unicode-casefold-only
// edge cases like ß→ss are not normalised, an accepted minor residual.)
// The two knobs are additive and DEFAULT TO TODAY'S BEHAVIOUR: case_sensitive
// skips the lowercase step only, remove_punctuation deletes ASCII punctuation
// after it. Order is pinned (trim/serialise → lowercase → punctuation) so both
// languages produce the same string. remove_punctuation reaches this function only
// for string inputs — the dispatch rejects it for structured values, because
// stripping punctuation from a serialised object deletes the delimiters that carry
// its shape.
function normaliseExact(value: unknown, caseSensitive = false, removePunctuation = false): string {
  let text = typeof value === "string" ? value.trim() : stableStringify(value);
  if (!caseSensitive) text = text.toLowerCase();
  if (removePunctuation) text = text.replace(ASCII_PUNCTUATION, "");
  return text;
}

const MAX_REGEX_PATTERN_CHARS = 1_024;
const MAX_REGEX_INPUT_CHARS = 64 * 1_024;

// JavaScript RegExp has no execution deadline. Reject backreferences and any
// quantified group containing a nested quantifier or alternation. Scanner
// propagates risk through redundant parentheses, unlike a flat regex check.
function hasUnsafeQuantifiedGroup(pattern: string): boolean {
  const stack: Array<{ risky: boolean }> = [];
  let escaped = false;
  let inClass = false;
  for (let i = 0; i < pattern.length; i++) {
    const ch = pattern[i]!;
    if (escaped) { escaped = false; continue; }
    if (ch === "\\") { escaped = true; continue; }
    if (inClass) { if (ch === "]") inClass = false; continue; }
    if (ch === "[") { inClass = true; continue; }
    if (ch === "(") { stack.push({ risky: false }); continue; }
    if (ch === "|") { for (const frame of stack) frame.risky = true; continue; }
    if (ch === ")") {
      const frame = stack.pop();
      if (!frame) continue;
      const next = pattern[i + 1];
      const quantified = next === "+" || next === "*" || next === "?" || next === "{";
      if (quantified && frame.risky) return true;
      if (quantified) for (const parent of stack) parent.risky = true;
      continue;
    }
    const groupPrefix = ch === "?" && pattern[i - 1] === "(";
    if (!groupPrefix && (ch === "+" || ch === "*" || ch === "?" || ch === "{")) {
      for (const frame of stack) frame.risky = true;
    }
  }
  return false;
}

function safeRegex(pattern: unknown, value: string): { regex?: RegExp; error?: string } {
  if (typeof pattern !== "string" || pattern.length > MAX_REGEX_PATTERN_CHARS) return { error: "regex pattern exceeds safety cap" };
  if (value.length > MAX_REGEX_INPUT_CHARS) return { error: "regex input exceeds safety cap" };
  if (/\\[1-9]/.test(pattern)) return { error: "regex backreferences are not supported safely" };
  if (hasUnsafeQuantifiedGroup(pattern)) return { error: "regex contains a potentially catastrophic quantified group" };
  try {
    return { regex: new RegExp(pattern) };
  } catch (error) {
    return { error: `invalid regex: ${String(error)}` };
  }
}

const REGEX_EXECUTION_TIMEOUT_MS = 100;

async function boundedRegexTest(regex: RegExp, value: string): Promise<{ matched?: boolean; error?: string }> {
  const worker = new Worker(`
    const { parentPort, workerData } = require("node:worker_threads");
    try {
      parentPort.postMessage({ matched: new RegExp(workerData.pattern).test(workerData.value) });
    } catch (error) {
      parentPort.postMessage({ error: String(error) });
    }
  `, { eval: true, workerData: { pattern: regex.source, value } });
  return new Promise((resolve) => {
    let settled = false;
    const finish = (result: { matched?: boolean; error?: string }) => {
      if (settled) return;
      settled = true;
      clearTimeout(timer);
      void worker.terminate();
      resolve(result);
    };
    // The execution deadline starts when the worker comes online, not at
    // spawn: cold worker startup can exceed the whole regex budget on a
    // loaded CI runner and would fail-close valid patterns. A separate,
    // generous guard still bounds a worker that never starts.
    let timer = setTimeout(
      () => finish({ error: "regex worker startup deadline exceeded" }),
      5_000,
    );
    worker.once("online", () => {
      if (settled) return;
      clearTimeout(timer);
      timer = setTimeout(
        () => finish({ error: "regex execution deadline exceeded" }),
        REGEX_EXECUTION_TIMEOUT_MS,
      );
    });
    worker.once("message", (message: unknown) => {
      const record = asRecord(message);
      finish(typeof record.matched === "boolean"
        ? { matched: record.matched }
        : { error: String(record.error ?? "regex worker failed") });
    });
    worker.once("error", (error: Error) => finish({ error: `regex worker failed: ${String(error)}` }));
    worker.once("exit", (code: number) => {
      if (code !== 0) finish({ error: `regex worker exited with code ${code}` });
    });
  });
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
    if (cur && typeof cur === "object" && !Array.isArray(cur)) {
      const rec = cur as Record<string, unknown>;
      if (!Object.hasOwn(rec, part)) return { found: false, value: undefined };
      cur = rec[part];
    } else if (Array.isArray(cur)) {
      const idx = Number(part);
      if (!Number.isInteger(idx) || idx < 0 || idx >= cur.length) return { found: false, value: undefined };
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
      return typeof value === "number";
    case "integer":
      return typeof value === "number" && Number.isInteger(value);
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
  if (!schema || typeof schema !== "object") return [`${path}: schema is not an object`];
  const s = schema as Record<string, unknown>;
  const errors: string[] = [];
  if (Object.hasOwn(s, "type")) {
    const types = Array.isArray(s.type) ? (s.type as string[]) : [String(s.type)];
    if (!types.some((t) => typeOk(value, t))) {
      return [`${path}: expected type ${JSON.stringify(s.type)}`];
    }
  }
  if (Array.isArray(s.enum) && !s.enum.some((e) => JSON.stringify(e) === JSON.stringify(value))) {
    errors.push(`${path}: value not in enum`);
  }
  if (value && typeof value === "object" && !Array.isArray(value)) {
    const rec = value as Record<string, unknown>;
    for (const req of Array.isArray(s.required) ? (s.required as string[]) : []) {
      if (!Object.hasOwn(rec, req)) errors.push(`${path}: missing required key ${JSON.stringify(req)}`);
    }
    const props = s.properties && typeof s.properties === "object" ? (s.properties as Record<string, unknown>) : {};
    for (const [key, sub] of Object.entries(props)) {
      if (Object.hasOwn(rec, key)) errors.push(...validateSchema(rec[key], sub, `${path}.${key}`));
    }
  }
  if (Array.isArray(value) && Object.hasOwn(s, "items")) {
    value.forEach((item, i) => errors.push(...validateSchema(item, s.items, `${path}[${i}]`)));
  }
  return errors;
}

// ─── SSRF guard (default: IP-literal classifier) ─────────────────────────────
// Without node DNS types in this lib, the default guard classifies IP literals
// (catching 169.254.169.254 and other private/loopback/link-local ranges) and
// fails CLOSED on bare hostnames so a hostname cannot smuggle a private address.
// Hostname targets additionally require an injected fetch transport that pins
// the checked resolution through connect; a resolver-only check is vulnerable
// to DNS rebinding between check and fetch.

function targetNeedsPinnedTransport(url: string): boolean {
  try {
    return !isIpLiteral(new URL(url).hostname.replace(/^\[/, "").replace(/\]$/, ""));
  } catch {
    return true;
  }
}

function parseIPv4(ip: string): number[] | null {
  const parts = ip.split(".");
  if (parts.length !== 4) return null;
  const bytes = parts.map((part) => /^\d{1,3}$/.test(part) ? Number(part) : -1);
  return bytes.every((part) => Number.isInteger(part) && part >= 0 && part <= 255) ? bytes : null;
}

function ipv4Blocked(bytes: number[]): boolean {
  const [a = -1, b = -1, c = -1] = bytes;
  if (a === 0 || a === 10 || a === 127 || a >= 224) return true;
  if (a === 100 && b >= 64 && b <= 127) return true;
  if (a === 169 && b === 254) return true;
  if (a === 172 && b >= 16 && b <= 31) return true;
  if (a === 192 && (b === 168 || (b === 0 && (c === 0 || c === 2)))) return true;
  if (a === 198 && (b === 18 || b === 19 || (b === 51 && c === 100))) return true;
  if (a === 203 && b === 0 && c === 113) return true;
  return false;
}

function parseIPv6(ip: string): number[] | null {
  let value = (ip.toLowerCase().split("%")[0] ?? "");
  const lastColon = value.lastIndexOf(":");
  const dotted = lastColon >= 0 ? value.slice(lastColon + 1) : "";
  if (dotted.includes(".")) {
    const v4 = parseIPv4(dotted);
    if (v4 === null) return null;
    value = value.slice(0, lastColon + 1) +
      (((v4[0] ?? 0) << 8) | (v4[1] ?? 0)).toString(16) + ":" +
      (((v4[2] ?? 0) << 8) | (v4[3] ?? 0)).toString(16);
  }
  if ((value.match(/::/g) ?? []).length > 1) return null;
  const halves = value.split("::");
  const left = halves[0] ? halves[0].split(":") : [];
  const right = halves.length === 2 && halves[1] ? halves[1].split(":") : [];
  const missing = 8 - left.length - right.length;
  if ((halves.length === 1 && missing !== 0) || (halves.length === 2 && missing < 1)) return null;
  const words = [...left, ...Array(missing).fill("0"), ...right];
  if (words.length !== 8 || words.some((word) => !/^[0-9a-f]{1,4}$/.test(word))) return null;
  return words.map((word) => Number.parseInt(word, 16));
}

function classifyIp(ip: string): "blocked" | "ok" {
  const v4 = parseIPv4(ip);
  if (v4 !== null) return ipv4Blocked(v4) ? "blocked" : "ok";
  const words = parseIPv6(ip);
  if (words === null) return "blocked";
  const bytes = words.flatMap((word) => [word >> 8, word & 0xff]);
  const mapped = bytes.slice(0, 10).every((b) => b === 0) && bytes[10] === 0xff && bytes[11] === 0xff;
  if (mapped) return ipv4Blocked(bytes.slice(12)) ? "blocked" : "ok";
  // Allow only global-unicast 2000::/3, excluding documentation/benchmark ranges.
  if (((words[0] ?? 0) & 0xe000) !== 0x2000) return "blocked";
  if (words[0] === 0x2002) return "blocked"; // 6to4 can encode private/loopback IPv4
  if (words[0] === 0x2001 && (
    words[1] === 0x0000 ||
    ((words[1] ?? 0) >= 0x0010 && (words[1] ?? 0) <= 0x001f) ||
    words[1] === 0x0002 ||
    words[1] === 0x0db8
  )) return "blocked";
  if (((words[0] ?? 0) & 0xfff0) === 0x3ff0) return "blocked";
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

// isRedirect reports whether a response status is a 3xx. Every outbound grader
// call sets `redirect: "manual"` and fails closed here rather than following it:
// the SSRF guard only ever validated the URL the caller supplied, so a redirect
// moves the request to a host nothing checked. Mirrors the Python side's
// _NoRedirectHandler.
function isRedirect(status: unknown): boolean {
  return typeof status === "number" && status >= 300 && status < 400;
}

const DEFAULT_NETWORK_GRADER_TIMEOUT_MS = 10_000;

async function withNetworkDeadline<T>(deps: GradeDeps, operation: (signal: AbortSignal) => Promise<T>): Promise<T> {
  const configured = deps.networkTimeoutMs;
  const timeoutMs = typeof configured === "number" && Number.isSafeInteger(configured) && configured > 0
    ? configured
    : DEFAULT_NETWORK_GRADER_TIMEOUT_MS;
  const controller = new AbortController();
  let timer: ReturnType<typeof setTimeout> | undefined;
  const deadline = new Promise<never>((_resolve, reject) => {
    timer = setTimeout(() => {
      controller.abort(new Error("network grader timeout"));
      reject(new Error("network grader timeout"));
    }, timeoutMs);
  });
  try {
    return await Promise.race([operation(controller.signal), deadline]);
  } finally {
    if (timer !== undefined) clearTimeout(timer);
  }
}

async function gradeCustomWebhook(url: string, value: unknown, deps: GradeDeps): Promise<GradeResult> {
  if (!url) return fail("custom_webhook requires url");
  const needsPinnedTransport = targetNeedsPinnedTransport(url);
  if (needsPinnedTransport && deps.resolutionPinnedFetch === undefined) {
    return fail("url blocked by SSRF guard: hostname targets require an injected resolution-pinned fetch transport");
  }
  const ssrf = deps.ssrfCheck ?? defaultSsrfCheck;
  const doFetch = needsPinnedTransport
    ? deps.resolutionPinnedFetch!
    : deps.fetch ?? deps.resolutionPinnedFetch ?? fetch;
  let body: unknown;
  try {
    const outcome = await withNetworkDeadline(deps, async (signal) => {
      const verdict = await ssrf(url);
      if (!verdict.allowed) return { failure: `url blocked by SSRF guard: ${verdict.reason}` };
      const resp = await doFetch(url, {
        method: "POST",
        headers: { "content-type": "application/json" },
        body: JSON.stringify({ candidate: value }),
        redirect: "manual",
        signal,
      });
      if (isRedirect(resp.status)) {
        return { failure: `webhook redirect refused (${resp.status}); the SSRF guard only saw the url you supplied` };
      }
      if (resp.ok === false) return { failure: `webhook returned non-2xx (${resp.status ?? "error"})` };
      return { responseBody: await resp.json() };
    });
    if (outcome.failure !== undefined) return fail(outcome.failure);
    body = outcome.responseBody;
  } catch (e) {
    return fail(`webhook call failed: ${String(e)}`);
  }
  const rec = asRecord(body);
  if (!Object.hasOwn(rec, "passed")) return fail("webhook response missing 'passed'");
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

// ─── shared LLM-judge transport ───────────────────────────────────────────────
// Every judge grader (`llm_judge` plus the four langevals judge types) posts through
// this one path: default judge model → judge-bias guard → SSRF guard → POST
// `<gateway_url>/openai/v1/responses` → model text. Callers own their own option
// validation, their own prompt, and their own verdict parsing, and they validate
// BEFORE building a prompt so an invalid grader never spends a judge call.

/** The transport options every judge grader shares, with `llm_judge`'s names. */
interface JudgeTransport {
  gateway_url?: string;
  model?: string;
  api_key?: string;
  upstream_key?: string;
}

interface JudgeRequest {
  /** Grader type name; used verbatim in the transport's failure reasons. */
  type: string;
  /** Already-validated gateway base URL (see judgeGatewayUrl). */
  gatewayUrl: string;
  config: JudgeTransport;
  prompt: string;
  /**
   * Fail closed on a non-2xx gateway reply. True for the four langevals judge
   * types; FALSE only for `llm_judge`, whose behaviour is frozen (it parses
   * whatever body came back — still fail-closed on an unparseable one — so no
   * existing verdict or reason string changes).
   */
  rejectNonOk: boolean;
}

type JudgeOutcome = { ok: true; model: string; text: string } | { ok: false; model: string; reason: string };

const DEFAULT_JUDGE_MODEL = "gpt-5.5";

// judgeGatewayUrl validates the one transport option that is required. Callers run it
// with the rest of their option validation, before any candidate is serialised.
function judgeGatewayUrl(raw: unknown): string | null {
  return typeof raw === "string" && raw !== "" ? raw : null;
}

async function callJudge(call: JudgeRequest, deps: GradeDeps): Promise<JudgeOutcome> {
  const judgeModel = call.config.model ?? DEFAULT_JUDGE_MODEL;
  // Judge-bias guard: a model tends to favour its own outputs, so the judge must
  // be a different family than the system-under-test. Fail closed when they match.
  if (deps.subjectModel && deps.subjectModel.trim()) {
    const judgeFamily = modelFamily(judgeModel);
    const subjectFamily = modelFamily(deps.subjectModel);
    if (judgeFamily && judgeFamily === subjectFamily) {
      return {
        ok: false,
        model: judgeModel,
        reason: `${call.type} bias guard: judge model "${judgeModel}" shares family "${judgeFamily}" with system-under-test "${deps.subjectModel}"; use a different model family as judge`,
      };
    }
  }
  const needsPinnedTransport = targetNeedsPinnedTransport(call.gatewayUrl);
  if (needsPinnedTransport && deps.resolutionPinnedFetch === undefined) {
    return {
      ok: false,
      model: judgeModel,
      reason: "gateway url blocked by SSRF guard: hostname targets require an injected resolution-pinned fetch transport",
    };
  }
  const ssrf = deps.ssrfCheck ?? defaultSsrfCheck;
  const doFetch = needsPinnedTransport
    ? deps.resolutionPinnedFetch!
    : deps.fetch ?? deps.resolutionPinnedFetch ?? fetch;
  try {
    const outcome = await withNetworkDeadline(deps, async (signal) => {
      const verdict = await ssrf(call.gatewayUrl);
      if (!verdict.allowed) {
        return { failure: `gateway url blocked by SSRF guard: ${verdict.reason}` };
      }
      const resp = await doFetch(`${call.gatewayUrl.replace(/\/$/, "")}/openai/v1/responses`, {
        method: "POST",
        headers: {
          authorization: `Bearer ${call.config.api_key ?? ""}`,
          "x-cave-upstream-key": call.config.upstream_key ?? "stub",
          "content-type": "application/json",
        },
        body: JSON.stringify({ model: judgeModel, input: call.prompt }),
        redirect: "manual",
        signal,
      });
      if (isRedirect(resp.status)) {
        return { failure: `${call.type}: judge redirect refused (${resp.status}); the SSRF guard only saw the gateway url you supplied` };
      }
      if (call.rejectNonOk && resp.ok === false) {
        return { failure: `${call.type}: judge returned non-2xx (${resp.status ?? "error"})` };
      }
      return { responseBody: await resp.json() };
    });
    if (outcome.failure !== undefined) return { ok: false, model: judgeModel, reason: outcome.failure };
    return { ok: true, model: judgeModel, text: extractModelText(outcome.responseBody) };
  } catch (e) {
    return { ok: false, model: judgeModel, reason: `judge call failed: ${String(e)}` };
  }
}

async function gradeLlmJudge(
  grader: Extract<Grader, { type: "llm_judge" }>,
  value: unknown,
  deps: GradeDeps,
): Promise<GradeResult> {
  if (!grader.rubric || !grader.rubric.trim()) return fail("llm_judge requires rubric");
  const gatewayUrl = judgeGatewayUrl(grader.gateway_url);
  if (gatewayUrl === null) return fail("llm_judge requires gateway_url");
  const outcome = await callJudge(
    {
      type: "llm_judge",
      gatewayUrl,
      config: grader,
      prompt: `${grader.rubric}\n\nOutput under test:\n${asText(value)}\n\nRespond with exactly PASS or FAIL.`,
      rejectNonOk: false,
    },
    deps,
  );
  if (!outcome.ok) return fail(outcome.reason);
  const result = parseVerdict(outcome.text);
  // Record the judge model+version in the verdict reason so the signed report
  // shows who judged (spec §5.3: report judge model + version).
  if (result === null) return fail(`judge(${outcome.model}) returned no PASS/FAIL verdict`);
  return result ? pass(`judge(${outcome.model}) returned PASS`) : fail(`judge(${outcome.model}) returned FAIL`);
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

  // Zero-quality candidates never satisfy a quality gate, including an explicit
  // zero threshold. Exact matches still pass at the zero boundary.
  const passed = fileF1 > 0 && lineF1 > 0 && fileF1 >= fileThr && lineF1 >= lineThr;
  const detail = `file_f1=${fileF1.toFixed(4)} line_f1=${lineF1.toFixed(4)} (thresholds file=${fileThr} line=${lineThr})`;
  return passed ? pass(`localization_f1 ${detail}`) : fail(`localization_f1 ${detail}`);
}

// ─── langevals port: option validation ────────────────────────────────────────
// Behavioral reference: langevals (MIT, (c) 2024 Reasoning Engine B.V.), reimplemented.
// Caveman INVERTS langevals' skip-on-missing-input: upstream returns a skipped or
// error status, we return `{passed:false}` with a reason. Graders arrive as JSON at
// runtime, so the compile-time option types are not a guarantee — every helper here
// returns null on an invalid option and the caller fails CLOSED.

// candidateText is `asText` with a string guarantee: JSON.stringify(undefined)
// returns undefined, and the string graders below must never throw on it.
function candidateText(value: unknown): string {
  const text = asText(value);
  return typeof text === "string" ? text : "";
}

// PINNED_WHITESPACE is the explicit ASCII whitespace class every new grader uses
// instead of `\s`. The two engines disagree on `\s`: JS includes U+0085, U+FEFF,
// U+00A0, U+2028/29 and the rest of the Unicode space separators, Python's
// (non-ASCII) `\s` includes U+001C-U+001F and U+0085 but not U+FEFF. Pinning the
// class makes every such character an ordinary, counted character on both sides.
const PINNED_WHITESPACE = /[ \t\n\r\f\v]+/g;

// normaliseLineEndings pins what the `not_regex` engines see at line boundaries:
// CRLF and a lone CR become LF, then exactly one trailing LF is removed, so `$`
// and `.` behave the same for a body that arrived with Windows line endings or a
// single trailing newline. Two residual divergences stay documented, not fixed
// (rewriting a caller's pattern would be worse than naming the limit):
//   * JS `.` excludes U+2028/U+2029; Python's does not.
//   * Python `$` also matches just before a FINAL newline, JS `$` only at the
//     absolute end — so a candidate with two or more trailing newlines can differ
//     between the two sides for a `$`-anchored pattern.
function normaliseLineEndings(text: string): string {
  const unified = text.replace(/\r\n|\r/g, "\n");
  return unified.endsWith("\n") ? unified.slice(0, -1) : unified;
}

// Size caps (pinned identical on both sides). A grader that would otherwise run an
// O(n·m) DP over an unbounded input fails CLOSED — an eval gate must never hang,
// and a silent truncation would change the verdict it reports.
const SCORE_TOKEN_CAP = 4096;
const CONTEXT_MEMBER_CAP = 64;
const CONTEXT_CHAR_CAP = 8192;
const SIZE_CAP_REASON = "input exceeds grader size cap";

// requireStringList validates a required option that must be a non-empty list of
// strings. `allowBlank` keeps empty-string members (context_f1 defines a similarity
// for them); fragment/term lists reject a blank member as an unmatchable option.
function requireStringList(raw: unknown, allowBlank = false): string[] | null {
  if (!Array.isArray(raw) || raw.length === 0) return null;
  for (const item of raw) {
    if (typeof item !== "string") return null;
    if (!allowBlank && item === "") return null;
  }
  return raw as string[];
}

// requireText validates a required string option that must be non-empty after trim.
function requireText(raw: unknown): string | null {
  if (typeof raw !== "string" || raw.trim() === "") return null;
  return raw;
}

// unitThreshold resolves an optional threshold. Only an ABSENT option (undefined —
// JSON has no undefined, so an absent key) takes the default; an explicit null, a
// boolean, a numeric string, NaN or an out-of-range number is an invalid option, so
// it returns null and the grader fails. This matches the Python side exactly, where
// `opts.get(key, default)` only defaults on a missing key.
function unitThreshold(raw: unknown, fallback: number): number | null {
  if (raw === undefined) return fallback;
  if (typeof raw !== "number" || !Number.isFinite(raw) || raw < 0 || raw > 1) return null;
  return raw;
}

// enumOption resolves an optional enum option. An absent option takes the default;
// any other unknown value (including an explicit null) returns null so the grader
// fails closed — the same rule the `default` dispatch branch enforces.
function enumOption<T extends string>(raw: unknown, allowed: readonly T[], fallback: T): T | null {
  if (raw === undefined) return fallback;
  if (typeof raw !== "string") return null;
  return (allowed as readonly string[]).includes(raw) ? (raw as T) : null;
}

// escapeRegex mirrors Python's re.escape for the characters that matter, so a
// blocklist term is matched literally instead of being read as a pattern.
function escapeRegex(text: string): string {
  return text.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
}

// ─── blocklist (case-insensitive whole-word terms) ────────────────────────────
// Behavioral reference: langevals (MIT, (c) 2024 Reasoning Engine B.V.), reimplemented.
// Word boundaries use pinned lookarounds instead of `\b`: `\b` depends on each
// engine's word-character definition and would drift between JS and Python. Both
// sides lowercase the candidate and each term, then search for
// `(?<![a-z0-9])<escaped term>(?![a-z0-9])`.

function gradeBlocklist(grader: Extract<Grader, { type: "blocklist" }>, value: unknown): GradeResult {
  const terms = requireStringList(grader.terms);
  if (terms === null) return fail("blocklist requires at least one non-empty term");
  const haystack = candidateText(value).toLowerCase();
  const hits = terms.filter((term) => new RegExp(`(?<![a-z0-9])${escapeRegex(term.toLowerCase())}(?![a-z0-9])`).test(haystack));
  return hits.length === 0
    ? pass("no blocklisted terms mentioned")
    : fail(`blocklisted terms mentioned: ${JSON.stringify([...new Set(hits)])}`);
}

// ─── shared scoring tokenizer + n-gram helpers (bleu_score, rouge_score) ──────
// Behavioral reference: langevals (MIT, (c) 2024 Reasoning Engine B.V.), reimplemented.
// The tokenizer is PINNED to be byte-identical with the Python side: lowercase, then
// every run of [a-z0-9] is one token and every other non-whitespace character is its
// own single-character token. No `\w`, no locale, no Unicode classes — those diverge
// between JS and Python. Scores are therefore deterministic and identical across the
// two languages but are NOT comparable to published sacrebleu / rouge-score numbers.

// The `u` flag is load-bearing for parity: without it the class matches UTF-16 code
// UNITS, so an astral symbol (emoji) would split into two surrogate-half tokens and
// inflate every denominator. Python's `re` counts code points, so `u` is what makes
// "one non-space symbol = one token" true on both sides. The whitespace class is the
// pinned ASCII set, never `\s` — see PINNED_WHITESPACE for why.
function scoreTokens(text: string): string[] {
  return text.toLowerCase().match(/[a-z0-9]+|[^a-z0-9 \t\n\r\f\v]/gu) ?? [];
}

// ngramCounts counts contiguous n-grams. Keys are JSON-encoded token slices, which
// is unambiguous for any token content (a token may itself be a punctuation char).
function ngramCounts(tokens: string[], n: number): Map<string, number> {
  const counts = new Map<string, number>();
  for (let i = 0; i + n <= tokens.length; i++) {
    const key = JSON.stringify(tokens.slice(i, i + n));
    counts.set(key, (counts.get(key) ?? 0) + 1);
  }
  return counts;
}

// clippedMatches is the standard modified precision numerator: per distinct
// candidate n-gram, min(count in candidate, count in reference).
function clippedMatches(cand: string[], ref: string[], n: number): number {
  const refNgrams = ngramCounts(ref, n);
  let clipped = 0;
  for (const [key, count] of ngramCounts(cand, n)) clipped += Math.min(count, refNgrams.get(key) ?? 0);
  return clipped;
}

function fMeasure(precision: number, recall: number): number {
  if (precision + recall === 0) return 0;
  return (2 * precision * recall) / (precision + recall);
}

// bleuScore: BP * exp(mean(ln p_n)) over n = 1..min(4, |cand|, |ref|). A zero
// clipped count is smoothed to 1/(2 * candidate n-gram count) so one missing order
// does not collapse the whole score to 0. Callers guarantee both sides non-empty.
function bleuScore(cand: string[], ref: string[]): number {
  const order = Math.min(4, cand.length, ref.length);
  let logSum = 0;
  for (let n = 1; n <= order; n++) {
    const total = cand.length - n + 1;
    const clipped = clippedMatches(cand, ref, n);
    logSum += Math.log(clipped > 0 ? clipped / total : 1 / (2 * total));
  }
  const brevity = cand.length >= ref.length ? 1 : Math.exp(1 - ref.length / cand.length);
  return brevity * Math.exp(logSum / order);
}

// lcsLength is the classic O(n·m) longest-common-subsequence DP over token
// sequences, used by rouge_l.
function lcsLength(a: string[], b: string[]): number {
  let prev = new Array<number>(b.length + 1).fill(0);
  let cur = new Array<number>(b.length + 1).fill(0);
  for (let i = 1; i <= a.length; i++) {
    for (let j = 1; j <= b.length; j++) {
      cur[j] = a[i - 1] === b[j - 1] ? (prev[j - 1] ?? 0) + 1 : Math.max(prev[j] ?? 0, cur[j - 1] ?? 0);
    }
    const done = cur;
    cur = prev;
    prev = done;
    cur.fill(0);
  }
  return prev[b.length] ?? 0;
}

function gradeBleuScore(grader: Extract<Grader, { type: "bleu_score" }>, value: unknown): GradeResult {
  const reference = requireText(grader.reference);
  if (reference === null) return fail("bleu_score requires a non-empty reference");
  const threshold = unitThreshold(grader.threshold, 0.5);
  if (threshold === null) return fail("bleu_score threshold must be a number in [0,1]");
  const cand = scoreTokens(candidateText(value));
  const ref = scoreTokens(reference);
  if (cand.length === 0 || ref.length === 0) return fail("bleu_score: empty after tokenization");
  if (cand.length > SCORE_TOKEN_CAP || ref.length > SCORE_TOKEN_CAP) return fail(`bleu_score: ${SIZE_CAP_REASON}`);
  const score = bleuScore(cand, ref);
  const detail = `bleu_score score=${score.toFixed(4)} threshold=${threshold}`;
  return score >= threshold ? pass(detail) : fail(detail);
}

const ROUGE_TYPES = ["rouge_1", "rouge_l"] as const;
const ROUGE_MEASURES = ["precision", "recall", "fmeasure"] as const;

function gradeRougeScore(grader: Extract<Grader, { type: "rouge_score" }>, value: unknown): GradeResult {
  const reference = requireText(grader.reference);
  if (reference === null) return fail("rouge_score requires a non-empty reference");
  const rougeType = enumOption(grader.rouge_type, ROUGE_TYPES, "rouge_1");
  if (rougeType === null) return fail(`rouge_score: unknown rouge_type ${JSON.stringify(grader.rouge_type)}`);
  const measure = enumOption(grader.measure, ROUGE_MEASURES, "fmeasure");
  if (measure === null) return fail(`rouge_score: unknown measure ${JSON.stringify(grader.measure)}`);
  const threshold = unitThreshold(grader.threshold, 0.5);
  if (threshold === null) return fail("rouge_score threshold must be a number in [0,1]");
  const cand = scoreTokens(candidateText(value));
  const ref = scoreTokens(reference);
  if (cand.length === 0 || ref.length === 0) return fail("rouge_score: empty after tokenization");
  if (cand.length > SCORE_TOKEN_CAP || ref.length > SCORE_TOKEN_CAP) return fail(`rouge_score: ${SIZE_CAP_REASON}`);
  const overlap = rougeType === "rouge_l" ? lcsLength(cand, ref) : clippedMatches(cand, ref, 1);
  const precision = overlap / cand.length;
  const recall = overlap / ref.length;
  const score = measure === "precision" ? precision : measure === "recall" ? recall : fMeasure(precision, recall);
  const detail = `rouge_score ${rougeType} ${measure} score=${score.toFixed(4)} threshold=${threshold}`;
  return score >= threshold ? pass(detail) : fail(detail);
}

// ─── context_f1 (retrieved ⇄ expected context lists) ──────────────────────────
// Behavioral reference: langevals (MIT, (c) 2024 Reasoning Engine B.V.), reimplemented.
// Upstream's non-LLM context precision/recall uses rapidfuzz; we pin a normalized
// Levenshtein similarity instead so no dependency is needed and both languages agree
// exactly. Wire shape: BOTH lists arrive on the grader options (`retrieved` /
// `expected`) and the candidate value is unused for this type — identical in meaning
// on the Python side.

const CONTEXT_MEASURES = ["precision", "recall", "f1"] as const;

// normaliseContext: lowercase, collapse PINNED_WHITESPACE runs to one space, then
// drop the single leading/trailing space that survives the collapse. `String.trim()`
// is deliberately NOT used: it strips the whole Unicode whitespace set (including
// U+FEFF and U+00A0), which Python's str.strip() does not, so the two sides would
// disagree on where a member starts.
function normaliseContext(text: string): string {
  return text.toLowerCase().replace(PINNED_WHITESPACE, " ").replace(/^ | $/g, "");
}

// contextCharCount counts CODE POINTS (Python's len()) across a list, short-circuiting
// once the cap is exceeded so an oversized input is rejected without a full scan.
function contextCharCount(list: string[], cap: number): number {
  let total = 0;
  for (const member of list) {
    for (const _codePoint of member) {
      total++;
      if (total > cap) return total;
    }
  }
  return total;
}

// levenshtein over arrays of code points (Array.from, not .length) so JS matches
// Python's per-character distance for astral characters such as emoji.
function levenshtein(a: string[], b: string[]): number {
  if (a.length === 0) return b.length;
  if (b.length === 0) return a.length;
  let prev = Array.from({ length: b.length + 1 }, (_unused, i) => i);
  const cur = new Array<number>(b.length + 1).fill(0);
  for (let i = 1; i <= a.length; i++) {
    cur[0] = i;
    for (let j = 1; j <= b.length; j++) {
      const cost = a[i - 1] === b[j - 1] ? 0 : 1;
      cur[j] = Math.min((cur[j - 1] ?? 0) + 1, (prev[j] ?? 0) + 1, (prev[j - 1] ?? 0) + cost);
    }
    prev = [...cur];
  }
  return prev[b.length] ?? 0;
}

// contextSimilarity is 1 - normalised Levenshtein distance over ALREADY-normalised
// code-point arrays; two empty members are identical (similarity 1), and an empty vs
// non-empty pair scores 0. Callers normalise once and reuse, so the similarity matrix
// is computed exactly once per grade call.
function contextSimilarity(left: string[], right: string[]): number {
  const longest = Math.max(left.length, right.length);
  if (longest === 0) return 1;
  return 1 - levenshtein(left, right) / longest;
}

function gradeContextF1(grader: Extract<Grader, { type: "context_f1" }>): GradeResult {
  const retrieved = requireStringList(grader.retrieved, true);
  if (retrieved === null) return fail("context_f1 requires a non-empty retrieved list of strings");
  const expected = requireStringList(grader.expected, true);
  if (expected === null) return fail("context_f1 requires a non-empty expected list of strings");
  const measure = enumOption(grader.measure, CONTEXT_MEASURES, "f1");
  if (measure === null) return fail(`context_f1: unknown measure ${JSON.stringify(grader.measure)}`);
  const simThreshold = unitThreshold(grader.similarity_threshold, 0.7);
  if (simThreshold === null) return fail("context_f1 similarity_threshold must be a number in [0,1]");
  const threshold = unitThreshold(grader.threshold, 0.5);
  if (threshold === null) return fail("context_f1 threshold must be a number in [0,1]");
  if (retrieved.length > CONTEXT_MEMBER_CAP || expected.length > CONTEXT_MEMBER_CAP) {
    return fail(`context_f1: ${SIZE_CAP_REASON}`);
  }
  if (
    contextCharCount(retrieved, CONTEXT_CHAR_CAP) > CONTEXT_CHAR_CAP ||
    contextCharCount(expected, CONTEXT_CHAR_CAP) > CONTEXT_CHAR_CAP
  ) {
    return fail(`context_f1: ${SIZE_CAP_REASON}`);
  }

  // Normalise once, then reuse: the similarity matrix below is the only place the
  // O(n·m) Levenshtein runs, so precision and recall read the SAME numbers.
  const retrievedNorm = retrieved.map((r) => Array.from(normaliseContext(r)));
  const expectedNorm = expected.map((e) => Array.from(normaliseContext(e)));
  // A retrieval gate must never pass on nothing: if every member of either list
  // normalises away, there is nothing to judge, so fail CLOSED.
  if (retrievedNorm.every((r) => r.length === 0)) return fail("context_f1: every retrieved member normalises to empty");
  if (expectedNorm.every((e) => e.length === 0)) return fail("context_f1: every expected member normalises to empty");

  const similarity = retrievedNorm.map((r) => expectedNorm.map((e) => contextSimilarity(r, e)));
  const matched = similarity.filter((row) => row.some((s) => s >= simThreshold)).length;
  const covered = expectedNorm.filter((_e, j) => similarity.some((row) => (row[j] ?? 0) >= simThreshold)).length;
  const precision = matched / retrieved.length;
  const recall = covered / expected.length;
  const score = measure === "precision" ? precision : measure === "recall" ? recall : fMeasure(precision, recall);
  const detail = `context_f1 ${measure} score=${score.toFixed(4)} threshold=${threshold} (similarity>=${simThreshold})`;
  return score >= threshold ? pass(detail) : fail(detail);
}

// ─── no_pii (pinned pure-regex PII detectors) ─────────────────────────────────
// Behavioral reference: langevals (MIT, (c) 2024 Reasoning Engine B.V.), reimplemented.
// Only the ENTITY SET comes from upstream's Presidio evaluator; detection here is a
// deliberately conservative pure-regex subset — no ML, no spaCy, no dependency. It is
// NOT Presidio-equivalent: fewer entity kinds, checksum-valid credit cards / IBANs
// only, and international `+`-prefixed phone numbers only, so IDs and timestamps do
// not read as phone numbers. Verdict reasons name entity KINDS only, never the
// matched text — PII must not leak into a reason, a report, or a log line.

const PII_ENTITIES = ["email", "credit_card", "iban", "ipv4", "ipv6", "phone", "crypto"] as const;
type PiiEntity = (typeof PII_ENTITIES)[number];

const EMAIL_RE = /[a-zA-Z0-9._%+-]+@[a-zA-Z0-9.-]+\.[a-zA-Z]{2,}/;
const IPV4_RE = /(?<!\d)(?:25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)(?:\.(?:25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)){3}(?!\d)/;
const IPV6_FULL_RE = /(?<![A-Fa-f0-9:])(?:[A-Fa-f0-9]{1,4}:){7}[A-Fa-f0-9]{1,4}(?![A-Fa-f0-9:])/;
const IPV6_COMPRESSED_RE = /(?<![A-Fa-f0-9:])(?:[A-Fa-f0-9]{1,4}:)*[A-Fa-f0-9]{0,4}::(?:[A-Fa-f0-9]{1,4}:)*[A-Fa-f0-9]{0,4}(?![A-Fa-f0-9:])/g;
const BTC_LEGACY_RE = /(?<![a-km-zA-HJ-NP-Z1-9])[13][a-km-zA-HJ-NP-Z1-9]{25,34}(?![a-km-zA-HJ-NP-Z1-9])/;
const BECH32_RE = /(?<![a-z0-9])bc1[a-z0-9]{25,60}(?![a-z0-9])/;
const CARD_CANDIDATE_RE = /(?<!\d)(?:\d[ -]?){13,19}(?!\d)/g;
const IBAN_CANDIDATE_RE = /(?<![A-Za-z0-9])[A-Za-z]{2}\d{2}[A-Za-z0-9]{11,30}(?![A-Za-z0-9])/g;
// The phone runs use the pinned ASCII whitespace class, never `\s` (see
// PINNED_WHITESPACE): JS `\s` would let a U+00A0 or U+FEFF sit inside a candidate run
// that Python's ASCII `\s` would break.
const PHONE_CANDIDATE_RE = /[+()\d][\d \t\n\r\f\v().-]{6,}\d/g;
const PHONE_STRIP_RE = /[ \t\n\r\f\v().-]/g;
// ASCII_DIGIT_RE gates the compressed-IPv6 detector: a compressed match must contain
// at least one 0-9 digit to count as an address.
const ASCII_DIGIT_RE = /[0-9]/;

// luhnValid is the standard mod-10 checksum: only a checksum-valid digit string is
// reported as a credit card, so order numbers and IDs are not false positives.
function luhnValid(digits: string): boolean {
  let sum = 0;
  let double = false;
  for (let i = digits.length - 1; i >= 0; i--) {
    let d = digits.charCodeAt(i) - 48;
    if (double) {
      d *= 2;
      if (d > 9) d -= 9;
    }
    sum += d;
    double = !double;
  }
  return sum % 10 === 0;
}

function hasCreditCard(text: string): boolean {
  for (const match of text.matchAll(CARD_CANDIDATE_RE)) {
    const digits = match[0].replace(/[ -]/g, "");
    if (digits.length >= 13 && digits.length <= 19 && luhnValid(digits)) return true;
  }
  return false;
}

// ibanValid: move the first four characters to the end, map A=10..Z=35, then require
// mod 97 == 1 (ISO 13616). Computed incrementally to stay inside safe integers.
function ibanValid(iban: string): boolean {
  const rearranged = iban.slice(4) + iban.slice(0, 4);
  let remainder = 0;
  for (const ch of rearranged) {
    const code = ch.charCodeAt(0);
    const part = code >= 65 && code <= 90 ? String(code - 55) : ch;
    for (const digit of part) remainder = (remainder * 10 + (digit.charCodeAt(0) - 48)) % 97;
  }
  return remainder === 1;
}

function hasIban(text: string): boolean {
  for (const match of text.matchAll(IBAN_CANDIDATE_RE)) {
    if (ibanValid(match[0].toUpperCase())) return true;
  }
  return false;
}

// hasIpv6 accepts the full 8-group form, or a `::`-compressed form that contains at
// least one ASCII DIGIT. The digit requirement — not merely "a hex character" — is what
// keeps source code out of the detector: `std::vector` and `a::b` are hex-letter-only
// and are ignored, while `::1` and `fe80::` still match. Documented deviation: a
// digitless compressed address such as `ab::cd` is therefore not detected, which is
// conservative by design (a false negative here is a missed redaction hint, a false
// positive would block every C++ or Rust snippet an eval ever grades).
function hasIpv6(text: string): boolean {
  if (IPV6_FULL_RE.test(text)) return true;
  for (const match of text.matchAll(IPV6_COMPRESSED_RE)) {
    if (ASCII_DIGIT_RE.test(match[0])) return true;
  }
  return false;
}

// hasPhone strips spaces, dots, dashes and parens from each candidate run and only
// accepts a full international number: `+` plus 7-15 digits (E.164 range).
function hasPhone(text: string): boolean {
  for (const match of text.matchAll(PHONE_CANDIDATE_RE)) {
    if (/^\+\d{7,15}$/.test(match[0].replace(PHONE_STRIP_RE, ""))) return true;
  }
  return false;
}

const PII_DETECTORS: Record<PiiEntity, (text: string) => boolean> = {
  email: (text) => EMAIL_RE.test(text),
  credit_card: hasCreditCard,
  iban: hasIban,
  ipv4: (text) => IPV4_RE.test(text),
  ipv6: hasIpv6,
  phone: hasPhone,
  crypto: (text) => BTC_LEGACY_RE.test(text) || BECH32_RE.test(text),
};

function gradeNoPii(grader: Extract<Grader, { type: "no_pii" }>, value: unknown): GradeResult {
  // Only an absent `entities` key selects every entity; an explicit null is an
  // invalid option (mirrors the Python `opts.get("entities", ALL)` behaviour).
  let selected: readonly PiiEntity[] = PII_ENTITIES;
  if (grader.entities !== undefined) {
    const names = requireStringList(grader.entities);
    if (names === null) return fail("no_pii entities must be a non-empty list of entity names");
    for (const name of names) {
      if (!(PII_ENTITIES as readonly string[]).includes(name)) return fail(`no_pii: unknown entity ${JSON.stringify(name)}`);
    }
    // Canonical order + de-duplicated, so the reason text is stable.
    selected = PII_ENTITIES.filter((entity) => names.includes(entity));
  }
  const text = candidateText(value);
  const found = selected.filter((entity) => PII_DETECTORS[entity](text));
  return found.length === 0
    ? pass(`no_pii: no matches for ${selected.join(", ")}`)
    : fail(`no_pii: detected ${found.join(", ")}`);
}

// ─── langevals judge family (llm_score · llm_category · llm_pairwise · llm_answer_match) ──
// Behavioral reference: langevals (MIT, (c) 2024 Reasoning Engine B.V.), reimplemented.
// Upstream drives these four through litellm structured outputs / dspy signatures. We pin
// a plain-text ONE-LINE contract over the same gateway path `llm_judge` already uses, so
// this package keeps zero runtime dependencies.
//
// HONESTY NOTE: an LLM judge is non-deterministic in production — a verdict is only as
// reliable as the judge model, so an eval gate should pin that model. Every parse failure,
// refusal or ordering disagreement fails CLOSED. A judge score is a grader verdict, never
// a `measured` number.

// The prompt templates below are BYTE CONTRACTS shared with the Python mirror in
// cloud/optimizer: for identical inputs both languages must emit identical prompt strings
// (asserted by the `expected_prompts` field of tests/langevals-judge.vectors.json). Do not
// reflow, re-punctuate or re-wrap them, and note that none ends with a trailing newline.
// Interpolation is STRING-ONLY: every option is validated as a string and the candidate
// goes through the same candidateText path as the other langevals-ported graders, so an
// invalid grader fails before a judge call is ever made.
//
// A NON-STRING candidate is serialised by `JSON.stringify` (via candidateText), which is
// already the compact form — no space after `,` or `:`. That compactness is part of the byte
// contract, not an accident: the Python mirror serialises the judge family with
// `json.dumps(..., separators=(",", ":"), ensure_ascii=False)` specifically to match these
// bytes, so `{"b":2,"a":[1,2]}` reaches the judge identically from both languages (pinned by
// the object-candidate vector's `expected_prompts`). Python's DEFAULT `json.dumps` spacing
// residual documented for `exact_match` therefore does not apply to this family.

function scorePrompt(rubric: string, candidate: string): string {
  return `You are a strict evaluation judge. ${rubric}\n\nScore the RESPONSE from 0.00 to 1.00 against the rubric.\nReply with exactly one line: SCORE: <number>\n\nRESPONSE:\n${candidate}`;
}

function categoryPrompt(prompt: string, categories: string[], candidate: string): string {
  return `You are a strict classification judge. ${prompt}\n\nClassify the RESPONSE into exactly one of these categories: ${categories.join(", ")}.\nReply with exactly one line: CATEGORY: <category>\n\nRESPONSE:\n${candidate}`;
}

function pairwisePrompt(criteria: string, first: string, second: string): string {
  return `You are a strict evaluation judge comparing two responses. Criteria: ${criteria}\n\nReply with exactly one line: WINNER: A or WINNER: B or WINNER: TIE\n\nRESPONSE A:\n${first}\n\nRESPONSE B:\n${second}`;
}

function answerMatchPrompt(expected: string, candidate: string): string {
  return `You are a strict evaluation judge. Decide whether the RESPONSE conveys the same answer as the EXPECTED answer, ignoring differences in style, wording, or formatting.\nReply with exactly one line: MATCH: YES or MATCH: NO\n\nEXPECTED:\n${expected}\n\nRESPONSE:\n${candidate}`;
}

// Parse patterns use the pinned ASCII whitespace class, never `\s` (see PINNED_WHITESPACE),
// and no shorthand beyond `\d` — which JS already restricts to 0-9 and the Python side pins
// with re.ASCII. All four KEYWORDS (SCORE:/CATEGORY:/WINNER:/MATCH:) are matched
// case-SENSITIVELY; only the captured A/B/TIE and YES/NO tokens accept either case, and they
// spell that out PER CHARACTER. No engine-wide `i` flag is used anywhere in this family: `i`
// would also lowercase the keyword, so `winner: a` — which is not the pinned reply contract —
// parsed here and failed closed in Python (which has always been per-character), i.e. the
// same judge reply produced two different verdicts. The keyword is the contract; a model that
// cannot reproduce it has not answered the question that was asked.
const SCORE_LINE_RE = /^[ \t\n\r\f\v]*SCORE:[ \t\n\r\f\v]*(\d+(?:\.\d+)?)[ \t\n\r\f\v]*$/m;
const CATEGORY_LINE_RE = /^[ \t\n\r\f\v]*CATEGORY:[ \t\n\r\f\v]*(.+?)[ \t\n\r\f\v]*$/m;
const WINNER_LINE_RE = /^[ \t\n\r\f\v]*WINNER:[ \t\n\r\f\v]*([AaBb]|[Tt][Ii][Ee])[ \t\n\r\f\v]*$/m;
const MATCH_LINE_RE = /^[ \t\n\r\f\v]*MATCH:[ \t\n\r\f\v]*([Yy][Ee][Ss]|[Nn][Oo])[ \t\n\r\f\v]*$/m;

// normaliseJudgeText pins the LINE STRUCTURE both engines see in a judge reply: CRLF and a
// lone CR become LF, then U+2028 (LINE SEPARATOR) and U+2029 (PARAGRAPH SEPARATOR) become LF
// too. Both passes exist because the engines disagree about what ends a line: JS `^`/`$` under
// `m` anchor at CR, U+2028 and U+2029, while Python's re.MULTILINE anchors only at `\n`. Left
// unnormalised, a reply whose verdict line is terminated by a U+2028 rather than a newline
// parsed here and failed closed in Python — one reply, two verdicts. The fix lands in Python;
// for the four patterns below the JS fold is a NO-OP (this engine already anchors at all four
// characters) and is kept anyway, so both mirrors state the same rule in code instead of one
// of them leaning on an implicit engine behaviour a later flag or pattern change could quietly
// withdraw. This is model text ONLY — never a user pattern and never the candidate, whose bytes
// are the operator's to define (there is also no trailing-newline strip here, unlike
// normaliseLineEndings).
function normaliseJudgeText(text: string): string {
  return text.replace(/\r\n|\r/g, "\n").replace(/[\u2028\u2029]/g, "\n");
}

// judgeToken normalises the reply's line structure and returns the captured token of the
// FIRST matching line. Empty model text and a garbage reply are the same miss: null. Only the
// captured token ever reaches a verdict reason — never the full model output.
function judgeToken(text: string, re: RegExp): string | null {
  const match = re.exec(normaliseJudgeText(text));
  return match && typeof match[1] === "string" ? match[1] : null;
}

// JUDGE_QUOTE_CAP bounds any MODEL-CONTROLLED text that reaches a verdict reason. A judge can
// reply with a megabyte-long "category", and a reason string is copied into reports, logs and
// eval-gate output — so the quote is truncated to 160 code points plus a single ellipsis.
// Counting is by code point (Array.from), not UTF-16 units, so the cut lands in the same place
// as the Python mirror's `_cap_judge_quote`. The cap applies to the REASON only: matching and
// comparison always run on the full captured token.
const JUDGE_QUOTE_CAP = 160;
function capJudgeQuote(text: string): string {
  const points = Array.from(text);
  return points.length <= JUDGE_QUOTE_CAP ? text : `${points.slice(0, JUDGE_QUOTE_CAP).join("")}…`;
}

// requireUnitNumber validates a REQUIRED number option in [0,1]. Unlike unitThreshold there
// is no default: missing, NaN, non-numeric or out-of-range all fail closed.
function requireUnitNumber(raw: unknown): number | null {
  if (typeof raw !== "number" || !Number.isFinite(raw) || raw < 0 || raw > 1) return null;
  return raw;
}

// pinnedTrim strips exactly the pinned ASCII whitespace class from both ends, mirroring the
// Python `_pinned_trim`. `String.prototype.trim()` is deliberately NOT used in this family:
// it also strips NBSP, U+FEFF and the Unicode space separators, which the pinned class
// treats as ORDINARY CHARACTERS (amendment J1). With `.trim()` a category label of
// "safe " matched here and failed as unknown in Python, and a rubric of a single NBSP
// failed here but was judged there — same input, two verdicts.
const PINNED_TRIM_RE = /^[ \t\n\r\f\v]+|[ \t\n\r\f\v]+$/g;
function pinnedTrim(text: string): string {
  return text.replace(PINNED_TRIM_RE, "");
}

// requireJudgeText is requireText on the pinned class. The pre-existing requireText keeps
// String.trim() for bleu_score/rouge_score, whose Python counterpart validates with
// str.strip() — aligning THAT pair on the pinned class would create the very divergence
// this helper removes for the judge family.
function requireJudgeText(raw: unknown): string | null {
  if (typeof raw !== "string" || pinnedTrim(raw) === "") return null;
  return raw;
}

async function gradeLlmScore(
  grader: Extract<Grader, { type: "llm_score" }>,
  value: unknown,
  deps: GradeDeps,
): Promise<GradeResult> {
  const rubric = requireJudgeText(grader.rubric);
  if (rubric === null) return fail("llm_score requires a non-empty rubric");
  const minScore = requireUnitNumber(grader.min_score);
  if (minScore === null) return fail("llm_score requires min_score to be a number in [0,1]");
  const gatewayUrl = judgeGatewayUrl(grader.gateway_url);
  if (gatewayUrl === null) return fail("llm_score requires gateway_url");
  const outcome = await callJudge(
    { type: "llm_score", gatewayUrl, config: grader, prompt: scorePrompt(rubric, candidateText(value)), rejectNonOk: true },
    deps,
  );
  if (!outcome.ok) return fail(outcome.reason);
  const token = judgeToken(outcome.text, SCORE_LINE_RE);
  const score = token === null ? Number.NaN : Number(token);
  // A score outside [0,1] is as unusable as no score at all: the judge did not answer the
  // question that was asked, so it fails closed rather than being clamped into a verdict.
  if (!Number.isFinite(score) || score < 0 || score > 1) {
    return fail(`llm_score judge(${outcome.model}): unparseable judge score`);
  }
  // The comparison runs on the UNROUNDED parsed value and is inclusive at the boundary, so
  // a judge score exactly equal to min_score passes.
  const detail = `llm_score judge(${outcome.model}) score=${score.toFixed(4)} min_score=${minScore}`;
  return score >= minScore ? pass(detail) : fail(detail);
}

async function gradeLlmCategory(
  grader: Extract<Grader, { type: "llm_category" }>,
  value: unknown,
  deps: GradeDeps,
): Promise<GradeResult> {
  const promptText = requireJudgeText(grader.prompt);
  if (promptText === null) return fail("llm_category requires a non-empty prompt");
  const categories = requireStringList(grader.categories);
  if (categories === null || categories.length < 2 || categories.some((c) => pinnedTrim(c) === "")) {
    return fail("llm_category requires at least two non-empty string categories");
  }
  // Category identity is case-insensitive on the label the judge returns, so two labels that
  // differ only by case are the SAME category — an ambiguous option, not two choices.
  const keys = categories.map((c) => pinnedTrim(c).toLowerCase());
  if (new Set(keys).size !== keys.length) return fail("llm_category categories must be unique case-insensitively");
  const passing = requireStringList(grader.passing_categories);
  if (passing === null) return fail("llm_category requires a non-empty passing_categories list");
  const passingKeys = passing.map((p) => pinnedTrim(p).toLowerCase());
  for (const entry of passing) {
    if (!keys.includes(pinnedTrim(entry).toLowerCase())) {
      return fail(`llm_category: passing_categories entry ${JSON.stringify(entry)} is not one of categories`);
    }
  }
  const gatewayUrl = judgeGatewayUrl(grader.gateway_url);
  if (gatewayUrl === null) return fail("llm_category requires gateway_url");
  const outcome = await callJudge(
    {
      type: "llm_category",
      gatewayUrl,
      config: grader,
      // The prompt lists the categories AS GIVEN (joined with ", "), not the trimmed keys —
      // the operator's wording is what the judge should see.
      prompt: categoryPrompt(promptText, categories, candidateText(value)),
      rejectNonOk: true,
    },
    deps,
  );
  if (!outcome.ok) return fail(outcome.reason);
  const label = judgeToken(outcome.text, CATEGORY_LINE_RE);
  if (label === null) return fail(`llm_category judge(${outcome.model}): unparseable judge category`);
  const labelKey = pinnedTrim(label).toLowerCase();
  const index = keys.indexOf(labelKey);
  if (index < 0) {
    // The label is MODEL-controlled, so the quote is capped (J8) before it reaches a reason.
    return fail(
      `llm_category judge(${outcome.model}): judge returned unknown category ${JSON.stringify(capJudgeQuote(label))}`,
    );
  }
  const canonical = categories[index] === undefined ? pinnedTrim(label) : pinnedTrim(categories[index]);
  const detail = `llm_category judge(${outcome.model}) category=${JSON.stringify(canonical)}`;
  return passingKeys.includes(labelKey) ? pass(detail) : fail(detail);
}

// PairwiseSide is which of the two compared texts the judge picked, resolved back out of the
// A/B slot it was placed in for that call.
type PairwiseSide = "candidate" | "baseline" | "tie";

function resolvePairwise(token: string, candidateIsA: boolean): PairwiseSide {
  const upper = token.toUpperCase();
  if (upper === "TIE") return "tie";
  return (upper === "A") === candidateIsA ? "candidate" : "baseline";
}

async function gradeLlmPairwise(
  grader: Extract<Grader, { type: "llm_pairwise" }>,
  value: unknown,
  deps: GradeDeps,
): Promise<GradeResult> {
  const baseline = requireJudgeText(grader.baseline);
  if (baseline === null) return fail("llm_pairwise requires a non-empty baseline");
  const criteria = requireJudgeText(grader.criteria);
  if (criteria === null) return fail("llm_pairwise requires non-empty criteria");
  const gatewayUrl = judgeGatewayUrl(grader.gateway_url);
  if (gatewayUrl === null) return fail("llm_pairwise requires gateway_url");
  const candidate = candidateText(value);

  // COST NOTE: this grader makes TWO judge calls per grade — the same pair judged once with
  // the candidate in slot A and once in slot B. LLM judges carry a measurable position bias
  // (they favour whichever text they read first), so a single call would score the slot as
  // much as the text. Budget roughly double an llm_judge/llm_score call for this type.
  const ab = await callJudge(
    {
      type: "llm_pairwise",
      gatewayUrl,
      config: grader,
      prompt: pairwisePrompt(criteria, candidate, baseline),
      rejectNonOk: true,
    },
    deps,
  );
  if (!ab.ok) return fail(ab.reason);
  const ba = await callJudge(
    {
      type: "llm_pairwise",
      gatewayUrl,
      config: grader,
      prompt: pairwisePrompt(criteria, baseline, candidate),
      rejectNonOk: true,
    },
    deps,
  );
  if (!ba.ok) return fail(ba.reason);
  // Both orderings are asked BEFORE either reply is parsed, so a successful transport
  // always emits the same fixed TWO-prompt shape — the shape the shared parity vectors
  // pin in `expected_prompts`, and the shape the Python mirror emits. Only a transport
  // failure exits early: there is then nothing left to compare the other ordering against.
  const abToken = judgeToken(ab.text, WINNER_LINE_RE);
  if (abToken === null) return fail(`llm_pairwise judge(${ab.model}): unparseable judge winner (candidate as A)`);
  const baToken = judgeToken(ba.text, WINNER_LINE_RE);
  if (baToken === null) return fail(`llm_pairwise judge(${ba.model}): unparseable judge winner (candidate as B)`);

  const abSide = resolvePairwise(abToken, true);
  const baSide = resolvePairwise(baToken, false);
  const detail = `llm_pairwise judge(${ab.model}) AB=${abSide} BA=${baSide}`;
  // Pinned verdict: pass only when NEITHER ordering resolves to the baseline. A candidate
  // that loses once and wins once told us the slot decided it, not the text — that is an
  // undecided comparison, and an undecided gate fails CLOSED.
  if (abSide === "baseline" && baSide === "baseline") return fail(`${detail}: baseline won both orderings`);
  if (abSide === "baseline" || baSide === "baseline") return fail(`${detail}: orderings disagreed: ${abSide}/${baSide}`);
  return pass(
    abSide === "candidate" && baSide === "candidate"
      ? `${detail}: candidate won both orderings`
      : `${detail}: candidate was not beaten under either ordering`,
  );
}

async function gradeLlmAnswerMatch(
  grader: Extract<Grader, { type: "llm_answer_match" }>,
  value: unknown,
  deps: GradeDeps,
): Promise<GradeResult> {
  const expected = requireJudgeText(grader.expected);
  if (expected === null) return fail("llm_answer_match requires a non-empty expected answer");
  const gatewayUrl = judgeGatewayUrl(grader.gateway_url);
  if (gatewayUrl === null) return fail("llm_answer_match requires gateway_url");
  const outcome = await callJudge(
    {
      type: "llm_answer_match",
      gatewayUrl,
      config: grader,
      prompt: answerMatchPrompt(expected, candidateText(value)),
      rejectNonOk: true,
    },
    deps,
  );
  if (!outcome.ok) return fail(outcome.reason);
  const token = judgeToken(outcome.text, MATCH_LINE_RE);
  if (token === null) return fail(`llm_answer_match judge(${outcome.model}): unparseable judge match verdict`);
  return token.toUpperCase() === "YES"
    ? pass(`llm_answer_match judge(${outcome.model}) returned MATCH: YES`)
    : fail(`llm_answer_match judge(${outcome.model}) returned MATCH: NO`);
}

// ─── dispatch ──────────────────────────────────────────────────────────────────

export async function grade(grader: Grader, value: unknown, deps: GradeDeps = {}): Promise<GradeResult> {
  switch (grader.type) {
    case "exact_match": {
      // Mirror the Python grader (the live /v1/grade service): normalise both
      // sides — case-insensitive, and key-order-insensitive for structured values
      // — so the same input yields the same verdict in both languages. Previously
      // raw JSON.stringify made TS case/order-sensitive (diverged from Python).
      // Both knobs are optional booleans; only an ABSENT key takes the default, and
      // any non-boolean value is an INVALID OPTION that fails CLOSED rather than
      // being silently read as false (the Python grader validates identically).
      const caseSensitive = grader.case_sensitive === undefined ? false : grader.case_sensitive;
      const removePunctuation = grader.remove_punctuation === undefined ? false : grader.remove_punctuation;
      if (typeof caseSensitive !== "boolean") return fail("exact_match option case_sensitive must be a boolean");
      if (typeof removePunctuation !== "boolean") return fail("exact_match option remove_punctuation must be a boolean");
      // remove_punctuation is a TEXT knob and is string-only. Applied to a serialised
      // structured value it would delete the braces, brackets, quotes and colons that
      // carry the shape, so `{"a":1}` and `["a",1]` collapse to the same string — a
      // false equal — and the two languages' serialisers space differently anyway.
      // Fail CLOSED instead of comparing shapes that were flattened away.
      if (removePunctuation && (typeof value !== "string" || typeof grader.expected !== "string")) {
        return fail("remove_punctuation requires string candidate and expected");
      }
      return normaliseExact(value, caseSensitive, removePunctuation) ===
        normaliseExact(grader.expected, caseSensitive, removePunctuation)
        ? pass("normalised values equal")
        : fail("normalised values differ");
    }
    case "contains": {
      const text = asText(value);
      const missing = grader.fragments.filter((f) => !text.includes(f));
      return missing.length === 0 ? pass("all fragments present") : fail(`missing fragments: ${JSON.stringify(missing)}`);
    }
    case "not_contains": {
      // Same normalisation and substring matching as `contains`, inverted quantifier:
      // fail if ANY fragment is present, pass only when none are. Unlike `contains`
      // (whose String(f) coercion is frozen for backward compatibility), a non-string
      // member here is an INVALID OPTION: `42` and `"42"` coerce differently across the
      // two languages, and a forbidden-fragment list that quietly changes shape is a
      // gate that quietly stops matching. Fail CLOSED.
      const fragments = grader.fragments;
      if (!Array.isArray(fragments) || fragments.length === 0) return fail("not_contains requires at least one fragment");
      if (fragments.some((f) => typeof f !== "string")) return fail("not_contains fragments must be strings");
      const text = asText(value);
      const present = fragments.filter((f) => text.includes(f));
      return present.length === 0
        ? pass("no forbidden fragments present")
        : fail(`forbidden fragments present: ${JSON.stringify(present)}`);
    }
    case "regex": {
      const text = String(value);
      const checked = safeRegex(grader.pattern, text);
      if (!checked.regex) return fail(checked.error ?? "unsafe regex");
      const executed = await boundedRegexTest(checked.regex, text);
      if (executed.matched === undefined) return fail(executed.error ?? "regex execution failed");
      return executed.matched ? pass("regex matched") : fail("regex did not match");
    }
    case "not_regex": {
      // Same engine semantics as `regex` (JS RegExp, no flags), inverted verdict.
      // An empty or invalid pattern is an invalid option, so it fails CLOSED rather
      // than "matching nothing" and passing.
      // The candidate goes through the same asText path as every other new grader —
      // NOT String(value), which renders an object as "[object Object]" and would make
      // a content pattern silently stop matching structured output. KNOWN ASYMMETRY,
      // by design: the pre-existing `regex` grader keeps String(value) (backward
      // compatibility), so `regex` and `not_regex` read a non-string candidate
      // differently — `not_regex` is the one that matches the Python side.
      if (typeof grader.pattern !== "string" || grader.pattern === "") return fail("not_regex requires a non-empty pattern");
      const text = normaliseLineEndings(candidateText(value));
      const checked = safeRegex(grader.pattern, text);
      if (!checked.regex) return fail(checked.error ?? "unsafe regex");
      const executed = await boundedRegexTest(checked.regex, text);
      if (executed.matched === undefined) return fail(executed.error ?? "regex execution failed");
      return executed.matched ? fail("regex matched") : pass("regex did not match");
    }
    case "blocklist":
      return gradeBlocklist(grader, value);
    case "json_schema": {
      let v = value;
      if (typeof value === "string") {
        try {
          v = JSON.parse(value);
        } catch {
          return fail("candidate is not valid JSON");
        }
      }
      const errors = validateSchema(v, grader.schema);
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
      const { found, value: resolved } = jsonPathGet(v, grader.path);
      if (grader.exists === true) return found ? pass(`path ${grader.path} exists`) : fail(`path ${grader.path} not found`);
      if (grader.exists === false) return !found ? pass(`path ${grader.path} absent`) : fail(`path ${grader.path} present`);
      if (!found) return fail(`path ${grader.path} not found`);
      if (Object.hasOwn(grader, "equals")) {
        return stableStringify(resolved) === stableStringify(grader.equals)
          ? pass(`path ${grader.path} equals expected`)
          : fail(`path ${grader.path} value ${JSON.stringify(resolved)} != expected`);
      }
      return pass(`path ${grader.path} exists`);
    }
    case "tool_called": {
      const called = new Set(extractToolCalls(value).map((c) => c.name));
      const missing = grader.tools.filter((t) => !called.has(t));
      return missing.length === 0 ? pass("all required tools called") : fail(`tools not called: ${JSON.stringify(missing)}`);
    }
    case "tool_not_called": {
      const called = new Set(extractToolCalls(value).map((c) => c.name));
      const present = grader.tools.filter((t) => called.has(t));
      return present.length === 0 ? pass("no forbidden tools called") : fail(`forbidden tools called: ${JSON.stringify(present)}`);
    }
    case "tool_sequence": {
      const called = extractToolCalls(value).map((c) => c.name);
      let i = 0;
      for (const name of called) {
        if (grader.tools[i] === name) i++;
      }
      return i === grader.tools.length
        ? pass("tools called in order")
        : fail(`expected ordered subsequence ${JSON.stringify(grader.tools)}`);
    }
    case "tool_argument_assertion": {
      for (const call of extractToolCalls(value)) {
        if (call.name !== grader.tool) continue;
        const { found, value: arg } = jsonPathGet(call.arguments, grader.path);
        if (found && stableStringify(arg) === stableStringify(grader.equals)) {
          return pass(`${grader.tool}.${grader.path} equals expected`);
        }
      }
      return fail(`no call to ${grader.tool} with ${grader.path} == expected`);
    }
    case "http_status":
      return Number(asRecord(value).status ?? asRecord(value).status_code) === grader.status
        ? pass(`status ${grader.status}`)
        : fail("status mismatch");
    case "latency_threshold": {
      const v = Number(asRecord(value).p95_ms ?? asRecord(value).latency_ms);
      if (!Number.isFinite(grader.p95_ms) || grader.p95_ms < 0) return fail("latency threshold must be non-negative and finite");
      if (!Number.isFinite(v) || v < 0) return fail("candidate has no non-negative latency");
      return v <= grader.p95_ms ? pass(`p95 ${v}ms <= ${grader.p95_ms}ms`) : fail(`p95 ${v}ms > ${grader.p95_ms}ms`);
    }
    case "cost_threshold": {
      // Fail CLOSED on absent cost: a candidate never measured can't pass a ceiling.
      const v = Number(asRecord(value).cost_usd ?? asRecord(value).total_cost_usd);
      if (!Number.isFinite(grader.max_usd) || grader.max_usd < 0) return fail("cost threshold must be non-negative and finite");
      if (!Number.isFinite(v) || v < 0) return fail("candidate has no non-negative cost");
      return v <= grader.max_usd ? pass(`cost ${v} <= ${grader.max_usd}`) : fail(`cost ${v} > ${grader.max_usd}`);
    }
    case "token_threshold": {
      // Fail CLOSED on absent token count: an unmeasured candidate can't pass.
      const v = Number(asRecord(value).tokens ?? asRecord(value).total_tokens);
      if (!Number.isFinite(grader.max_tokens) || grader.max_tokens < 0) return fail("token threshold must be non-negative and finite");
      if (!Number.isFinite(v) || v < 0) return fail("candidate has no non-negative tokens");
      return v <= grader.max_tokens ? pass(`tokens ${v} <= ${grader.max_tokens}`) : fail(`tokens ${v} > ${grader.max_tokens}`);
    }
    case "bleu_score":
      return gradeBleuScore(grader, value);
    case "rouge_score":
      return gradeRougeScore(grader, value);
    case "context_f1":
      return gradeContextF1(grader);
    case "no_pii":
      return gradeNoPii(grader, value);
    case "custom_webhook":
      return gradeCustomWebhook(grader.url, value, deps);
    case "localization_f1":
      return gradeLocalizationF1(grader, value);
    case "llm_judge":
      return gradeLlmJudge(grader, value, deps);
    case "llm_score":
      return gradeLlmScore(grader, value, deps);
    case "llm_category":
      return gradeLlmCategory(grader, value, deps);
    case "llm_pairwise":
      // TWO judge calls per grade (see gradeLlmPairwise): candidate as A, then as B.
      return gradeLlmPairwise(grader, value, deps);
    case "llm_answer_match":
      return gradeLlmAnswerMatch(grader, value, deps);
    default:
      // Fail CLOSED: an unknown/misspelled grader type never silently passes.
      return fail(`unknown grader type: ${String((grader as { type?: string }).type)}`);
  }
}
