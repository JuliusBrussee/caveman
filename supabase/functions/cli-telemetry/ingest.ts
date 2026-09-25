// Pure request handling for the cli-telemetry function: bounded body read,
// client IP, and per-event validation. Ported from control-api's
// cli_telemetry.go so the accepted shape stays the one packages/cli emits.
// Invalid events are dropped whole — never partially stored — except the
// environment descriptors (account … locale), which degrade field by field.
import { isIP } from "node:net";

export const MAX_BODY_BYTES = 64 * 1024;
export const MAX_BATCH = 20;

const MAX_COUNT = 2 ** 32;
const MAX_TOKENS = 2 ** 50;
const EVENTS = new Set(["command_run", "consent_granted", "runtime_bootstrap", "agent_tool_call", "first_run", "engine_session", "session_start"]);
// Events that describe an outcome must carry exit_class; the rest must not.
const OUTCOME_EVENTS = new Set(["command_run", "runtime_bootstrap", "agent_tool_call", "engine_session"]);
const ERROR_CLASSES = new Set(["network", "auth", "usage", "unknown_command", "exec_failed", "other"]);
const OS = new Set(["aix", "android", "cygwin", "darwin", "freebsd", "linux", "netbsd", "openbsd", "sunos", "win32"]);
const ARCH = new Set(["arm", "arm64", "ia32", "loong64", "mips", "mipsel", "ppc64", "riscv64", "s390x", "x64"]);
const UUID_RE = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;
const RFC3339_RE = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d+)?(Z|[+-]\d{2}:\d{2})$/;
// Identifier/version charset only: no spaces, no free text, bounded length.
const TOKEN_RE = /^[A-Za-z0-9._+-]{0,64}$/;
const AGENT_TOOL_RE = /^caveman_[a-z0-9_]{1,56}$/;
// The CLI passes the proxy's basis through verbatim (e.g. estimated_request_json_o200k_v1).
const BASIS_RE = /^[a-z0-9_]{1,64}$/;
const ACCOUNTS = new Set(["connected", "none"]);
const INSTALL_CHANNELS = new Set(["npx", "pnpm", "bun", "npm", "source"]);
const TIMEZONE_RE = /^[A-Za-z0-9_+\-/]{1,64}$/;
const LOCALE_RE = /^[A-Za-z0-9-]{2,64}$/;
const SESSION_SOURCES = new Set(["startup", "resume", "clear", "unknown"]);

type Raw = Record<string, unknown>;
export type Row = Record<string, string | number | boolean | null>;

class Invalid extends Error {}

function check(ok: boolean): void {
  if (!ok) throw new Invalid();
}

// Absent and null read as the zero value, matching the Go decoder; a present
// value of the wrong type rejects the event.
function str(raw: Raw, key: string): string {
  const v = raw[key];
  if (v === undefined || v === null) return "";
  check(typeof v === "string");
  return v as string;
}

function int(raw: Raw, key: string, max = MAX_TOKENS): number {
  const v = raw[key];
  if (v === undefined || v === null) return 0;
  check(Number.isSafeInteger(v) && (v as number) >= 0 && (v as number) < max);
  return v as number;
}

function optInt(raw: Raw, key: string, max = MAX_TOKENS): number | null {
  return raw[key] === undefined || raw[key] === null ? null : int(raw, key, max);
}

function bool(raw: Raw, key: string): boolean {
  const v = raw[key];
  if (v === undefined || v === null) return false;
  check(typeof v === "boolean");
  return v as boolean;
}

// An environment descriptor outside its shape is dropped on its own rather
// than costing the whole event.
function descriptor(raw: Raw, key: string, ok: (v: string) => boolean): string | null {
  const v = raw[key];
  return typeof v === "string" && ok(v) ? v : null;
}

function anySet(fields: Record<string, unknown>): boolean {
  return Object.values(fields).some((v) => v !== 0 && v !== false && v !== "" && v !== null);
}

export function validateEvent(input: unknown): Row | null {
  if (!input || typeof input !== "object" || Array.isArray(input)) return null;
  try {
    return toRow(input as Raw);
  } catch (error) {
    if (error instanceof Invalid) return null;
    throw error;
  }
}

function toRow(raw: Raw): Row {
  const anonymousId = str(raw, "anonymous_id");
  const event = str(raw, "event");
  check(str(raw, "schema") === "cli/v1" && UUID_RE.test(anonymousId) && EVENTS.has(event));

  const ts = str(raw, "ts");
  const tsMs = Date.parse(ts);
  check(RFC3339_RE.test(ts) && Number.isFinite(tsMs) && tsMs > Date.parse("0001-01-01T00:00:00Z"));

  const command = str(raw, "command");
  const subcommand = str(raw, "subcommand");
  const agent = str(raw, "agent");
  const cliVersion = str(raw, "cli_version");
  for (const s of [command, subcommand, agent, cliVersion]) check(TOKEN_RE.test(s));
  const os = str(raw, "os");
  const arch = str(raw, "arch");
  check(OS.has(os) && ARCH.has(arch));

  const exitClass = str(raw, "exit_class");
  const errorClass = str(raw, "error_class");
  check(OUTCOME_EVENTS.has(event) ? exitClass === "ok" || exitClass === "error" : exitClass === "");
  check(errorClass === "" || ERROR_CLASSES.has(errorClass));
  if (event === "runtime_bootstrap") check(command === "wrap" && subcommand === "install");
  if (event === "agent_tool_call") check(command === "mcp" && AGENT_TOOL_RE.test(subcommand) && agent === "");
  if (event === "engine_session") check(command === "wrap" && subcommand === "");
  if (event === "session_start") check(command === "" && subcommand === "" && agent !== "");
  const sessionSource = str(raw, "session_source");
  check(event === "session_start" ? sessionSource === "" || SESSION_SOURCES.has(sessionSource) : sessionSource === "");

  const row: Row = {
    ts: new Date(tsMs).toISOString(),
    anonymous_id: anonymousId.toLowerCase(),
    event,
    command: command || null,
    subcommand: subcommand || null,
    agent: agent || null,
    cli_version: cliVersion || null,
    os,
    arch,
    node_major: optInt(raw, "node_major", 256),
    duration_ms: optInt(raw, "duration_ms", MAX_COUNT),
    exit_class: exitClass || null,
    error_class: errorClass || null,
    session_source: sessionSource || null,
    account: descriptor(raw, "account", (v) => ACCOUNTS.has(v)),
    plan: descriptor(raw, "plan", (v) => v !== "" && TOKEN_RE.test(v)),
    install_channel: descriptor(raw, "install_channel", (v) => INSTALL_CHANNELS.has(v)),
    timezone: descriptor(raw, "timezone", (v) => TIMEZONE_RE.test(v)),
    locale: descriptor(raw, "locale", (v) => LOCALE_RE.test(v)),
  };

  // Each aggregate group belongs to specific events; carrying it anywhere else
  // rejects the whole event.
  const tokens = {
    tokens_processed: optInt(raw, "tokens_processed"),
    tokens_saved: optInt(raw, "tokens_saved"),
    tokens_basis: str(raw, "tokens_basis") || null,
  };
  if (anySet(tokens)) {
    check((event === "command_run" || event === "session_start") && tokens.tokens_processed !== null && tokens.tokens_saved !== null
      && tokens.tokens_basis !== null && BASIS_RE.test(tokens.tokens_basis));
    Object.assign(row, tokens);
  }

  const scan = {
    sessions_total: int(raw, "sessions_total", MAX_COUNT),
    sessions_scanned: int(raw, "sessions_scanned", MAX_COUNT),
    tokens_observed: int(raw, "tokens_observed"),
    would_cut_tokens: int(raw, "would_cut_tokens"),
    would_cut_stream_tokens: optInt(raw, "would_cut_stream_tokens"),
    scan_ok: bool(raw, "scan_ok"),
    engine_used: bool(raw, "engine_used"),
    time_boxed: bool(raw, "time_boxed"),
    logged_in: bool(raw, "logged_in"),
  };
  if (event === "first_run") {
    check(scan.sessions_scanned <= scan.sessions_total && scan.would_cut_tokens <= scan.tokens_observed
      && (scan.would_cut_stream_tokens === null || scan.would_cut_stream_tokens <= scan.tokens_observed));
    Object.assign(row, scan);
  } else {
    check(!anySet(scan));
  }

  const engine = {
    measurement_mode: str(raw, "measurement_mode"),
    measurement_ok: bool(raw, "measurement_ok"),
    requests_observed: int(raw, "requests_observed", MAX_COUNT),
    input_tokens_observed: int(raw, "input_tokens_observed"),
    compression_eligible_requests: int(raw, "compression_eligible_requests", MAX_COUNT),
    compression_eligibility_known: bool(raw, "compression_eligibility_known"),
    compression_tokens_before: int(raw, "compression_tokens_before"),
    compression_tokens_after: int(raw, "compression_tokens_after"),
    compression_tokens_saved: int(raw, "compression_tokens_saved"),
    compression_pair_known: bool(raw, "compression_pair_known"),
    estimated_cut_tokens: int(raw, "estimated_cut_tokens"),
    cache_read_tokens: int(raw, "cache_read_tokens"),
    cache_write_tokens: int(raw, "cache_write_tokens"),
    cache_bust_requests: int(raw, "cache_bust_requests", MAX_COUNT),
    headline_suppressed: bool(raw, "headline_suppressed"),
  };
  if (event === "engine_session") {
    check(validEngineSession(engine));
    Object.assign(row, engine);
  } else {
    check(!anySet(engine));
  }
  return row;
}

type Engine = {
  measurement_mode: string; measurement_ok: boolean; requests_observed: number; input_tokens_observed: number;
  compression_eligible_requests: number; compression_eligibility_known: boolean; compression_tokens_before: number;
  compression_tokens_after: number; compression_tokens_saved: number; compression_pair_known: boolean;
  estimated_cut_tokens: number; cache_read_tokens: number; cache_write_tokens: number; cache_bust_requests: number;
  headline_suppressed: boolean;
};

// Mirrors engineSessionTelemetryFields in packages/cli: an unmeasured session
// is all zeros, and a measured one must be internally consistent.
function validEngineSession(e: Engine): boolean {
  if (e.measurement_mode !== "observe" && e.measurement_mode !== "compress") return false;
  if (e.compression_eligible_requests > e.requests_observed || e.cache_bust_requests > e.requests_observed) return false;
  if (!e.measurement_ok) {
    const { measurement_mode: _mode, ...rest } = e;
    return !anySet(rest);
  }
  if (!e.compression_eligibility_known && e.compression_eligible_requests !== 0) return false;
  if (e.compression_pair_known) {
    if (e.compression_tokens_before < e.compression_tokens_after
      || e.compression_tokens_saved !== e.compression_tokens_before - e.compression_tokens_after) return false;
  } else if (e.compression_tokens_before !== 0 || e.compression_tokens_after !== 0) {
    return false;
  }
  if (e.measurement_mode === "observe") {
    return e.compression_tokens_saved === 0 && e.estimated_cut_tokens <= e.input_tokens_observed && !e.headline_suppressed;
  }
  return e.estimated_cut_tokens === 0;
}

// fromCli refuses what a web page can send. A page's no-cors POST with a
// text/plain body skips the CORS preflight, which would let any site record its
// visitors' IPs and spend the global cap. Requiring JSON forces a preflight
// (OPTIONS gets 405, so the browser never sends the POST), and browsers attach
// Origin to every POST and Sec-Fetch-Site to every request. Node's fetch sends
// neither (it does send Sec-Fetch-Mode, so that one cannot be used).
export function fromCli(headers: Headers): boolean {
  const type = (headers.get("content-type") ?? "").split(";")[0]!.trim().toLowerCase();
  return type === "application/json" && !headers.has("origin") && !headers.has("sec-fetch-site");
}

// Hosted Supabase sits behind Cloudflare, which sets cf-connecting-ip itself and
// rejects client-supplied copies. Nothing else is read: x-forwarded-for and
// true-client-ip can carry client-chosen values, so a local `functions serve`
// simply stores no IP.
export function clientIp(headers: Headers): string | null {
  const ip = (headers.get("cf-connecting-ip") ?? "").trim();
  return isIP(ip) ? ip : null;
}

// readBounded returns the body text, or null once it exceeds max bytes. The
// endpoint is unauthenticated, so a chunked body must not be read unbounded.
export async function readBounded(body: ReadableStream<Uint8Array> | null, max: number): Promise<string | null> {
  if (!body) return null;
  const reader = body.getReader();
  const chunks: Uint8Array[] = [];
  let size = 0;
  for (;;) {
    const { done, value } = await reader.read();
    if (done) break;
    size += value.byteLength;
    if (size > max) {
      await reader.cancel();
      return null;
    }
    chunks.push(value);
  }
  const buf = new Uint8Array(size);
  let offset = 0;
  for (const chunk of chunks) {
    buf.set(chunk, offset);
    offset += chunk.byteLength;
  }
  return new TextDecoder().decode(buf);
}
