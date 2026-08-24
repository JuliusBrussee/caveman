import { execFileSync, spawnSync } from "node:child_process";
import { appendFileSync, chmodSync, lstatSync, mkdirSync, readFileSync, realpathSync, statSync } from "node:fs";
import { homedir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { connect as netConnect } from "node:net";
import { createHash } from "node:crypto";
import { fileURLToPath } from "node:url";

import { hardenedGitArgs, hardenedGitEnv } from "./git-safe.js";

type NativeAgent = "claude" | "codex" | "hermes" | "gemini" | "opencode" | "pi";
type NativePolicyMode = "record" | "safe" | "max";
type NativeProfile = "record-only" | "core" | "core-lean-build" | "ledger" | "ccr-masking" | "cache-aware" | "full-safe" | "full-max";
type RuntimeResponse = {
  protocol_version: 1;
  policy_mode?: NativePolicyMode;
  profile?: NativeProfile;
  action: "allow" | "observe" | "block" | "ask" | "defer";
  context?: string;
  message?: string;
  output_replacement?: string;
  recovery_ref?: string;
  decision_id?: string;
  visibility: "silent" | "advisory" | "user";
  fail_open: true;
};

const PROFILES = new Set<NativeProfile>(["record-only", "core", "core-lean-build", "ledger", "ccr-masking", "cache-aware", "full-safe", "full-max"]);
const ACTIONS = new Set(["allow", "observe", "block", "ask", "defer"]);
const VISIBILITY = new Set(["silent", "advisory", "user"]);
const EVENTS = new Set(["SessionStart", "UserPromptSubmit", "PreToolUse", "PermissionRequest", "PostToolUse", "PostToolUseFailure", "ModelBefore", "ModelAfter", "PreCompact", "PostCompact", "SubagentStart", "SubagentStop", "Stop", "SessionEnd"]);
const PROTOCOL_EVENTS: Record<string, string> = {
  SessionStart: "session.start", UserPromptSubmit: "prompt.submit", PreToolUse: "tool.before", PermissionRequest: "tool.before",
  PostToolUse: "tool.after", PostToolUseFailure: "tool.failure", ModelBefore: "model.before", ModelAfter: "model.after",
  PreCompact: "context.compact.before", PostCompact: "context.compact.after", SubagentStart: "subagent.start",
  SubagentStop: "subagent.stop", Stop: "turn.stop", SessionEnd: "session.end",
};
const TASK_STOPWORDS = new Set(["about", "after", "again", "agent", "before", "build", "change", "code", "could", "create", "does", "from", "have", "help", "implement", "into", "make", "please", "project", "repository", "should", "spec", "task", "that", "their", "then", "there", "these", "they", "this", "through", "user", "want", "what", "when", "where", "which", "with", "would", "your"]);

function caveHome(): string {
  return process.env.CAVEMAN_HOME ?? join(homedir(), ".caveman");
}

// localGatewayEndpoint resolves the gateway target and returns its host/port
// only when it is a loopback proxy — the only case where a dead port means
// "the local proxy needs restarting". Managed gateways (CAVE_GATEWAY_URL /
// persisted gatewayUrl off-loopback) return undefined and never pay a
// per-prompt delegate spawn. Default mirrors the full CLI's PROXY_ADDR.
function localGatewayEndpoint(): { host: string; port: number } | undefined {
  let gw: unknown = process.env.CAVE_GATEWAY_URL;
  if (typeof gw !== "string" || !gw.trim()) {
    try { gw = object(JSON.parse(readFileSync(join(homedir(), ".caveman-cloud", "config.json"), "utf8"))).gatewayUrl; } catch { /* local default */ }
  }
  if (typeof gw !== "string" || !gw.trim()) return { host: "127.0.0.1", port: 8787 };
  try {
    const url = new URL(gw);
    const host = url.hostname;
    if (host !== "127.0.0.1" && host !== "localhost" && host !== "::1" && host !== "[::1]") return undefined;
    return { host: host === "[::1]" ? "::1" : host, port: url.port ? Number(url.port) : 8787 };
  } catch { return { host: "127.0.0.1", port: 8787 }; }
}

// gatewayListening is the same signal the full CLI's revive block keys on
// (portListening): a loopback TCP probe, ~1ms either way. Gating the delegate
// on the PORT rather than the runtime socket matters — record pass-through,
// noProxy, or a slow runtime all leave the socket dead while the proxy is up,
// and must not pay a full-CLI spawn on every prompt.
function gatewayListening(endpoint: { host: string; port: number }): Promise<boolean> {
  return new Promise((settle) => {
    const socket = netConnect({ host: endpoint.host, port: endpoint.port, timeout: 250 });
    socket.on("connect", () => { socket.destroy(); settle(true); });
    socket.on("error", () => settle(false));
    socket.on("timeout", () => { socket.destroy(); settle(false); });
  });
}

function object(value: unknown): Record<string, unknown> {
  return value && typeof value === "object" && !Array.isArray(value) ? value as Record<string, unknown> : {};
}

function configuredMode(): "compress" | "record" | "pixel" {
  const explicit = process.env.CAVEMAN_WRAP_MODE;
  if (explicit !== undefined) return explicit === "compress" || explicit === "pixel" || explicit === "record" ? explicit : "record";
  try {
    const config = object(JSON.parse(readFileSync(join(homedir(), ".caveman-cloud", "config.json"), "utf8")));
    const legacy = object(config.wrap).mode;
    const current = object(config.think).mode;
    const value = current ?? legacy;
    if (value !== undefined) return value === "compress" || value === "pixel" || value === "record" ? value : "record";
  } catch { /* default below */ }
  return "compress";
}

function configuredCore(): boolean {
  const parse = (value: unknown): boolean | undefined => {
    if (typeof value === "boolean") return value;
    if (typeof value !== "string") return undefined;
    const normalized = value.trim().toLowerCase();
    if (["1", "true", "yes", "on"].includes(normalized)) return true;
    if (["0", "false", "no", "off"].includes(normalized)) return false;
    return undefined;
  };
  let configured = true;
  try {
    const config = object(JSON.parse(readFileSync(join(homedir(), ".caveman-cloud", "config.json"), "utf8")));
    configured = parse(object(config.think).core) ?? true;
  } catch { /* default above */ }
  const env = process.env.CAVEMAN_CORE;
  return env === undefined ? configured : parse(env) ?? configured;
}

function policyMode(): NativePolicyMode {
  const profile = process.env.CAVEMAN_NATIVE_PROFILE?.trim().toLowerCase();
  if (profile && !PROFILES.has(profile as NativeProfile)) return "record";
  if (profile === "record-only") return "record";
  const explicit = process.env.CAVEMAN_NATIVE_MODE?.trim().toLowerCase();
  if (explicit !== undefined && explicit !== "") return explicit === "safe" || explicit === "max" || explicit === "record" ? explicit : "record";
  const mode = configuredMode();
  return mode === "record" ? "record" : mode === "pixel" ? "max" : "safe";
}

function profile(): NativeProfile {
  const explicit = process.env.CAVEMAN_NATIVE_PROFILE?.trim().toLowerCase() as NativeProfile | undefined;
  if (explicit) return PROFILES.has(explicit) ? explicit : "record-only";
  const mode = policyMode();
  return mode === "record" ? "record-only" : mode === "max" ? "full-max" : "full-safe";
}

// Return as soon as the accumulated bytes parse as one complete JSON payload
// instead of waiting for EOF. The host writes one object and closes, but under
// the Windows pipe implementation that close can lag arbitrarily (#729/#833),
// and a hook that blocks on EOF spends the host's whole budget waiting for a
// close it already has all the data for.
async function stdin(): Promise<Buffer> {
  const chunks: Buffer[] = [];
  let bytes = 0;
  for await (const chunk of process.stdin) {
    const value = Buffer.from(chunk);
    bytes += value.length;
    if (bytes > 2 * 1024 * 1024) throw new Error("hook payload too large");
    chunks.push(value);
    const joined = Buffer.concat(chunks);
    // A partial payload throws and we wait for more bytes.
    try { JSON.parse(joined.toString("utf8")); } catch { continue; }
    return joined;
  }
  return Buffer.concat(chunks);
}

function bounded(value: unknown, max = 160): string | undefined {
  if (typeof value !== "string") return undefined;
  const clean = value.replace(/[\r\n\0]/g, " ").trim();
  return clean ? clean.slice(0, max) : undefined;
}

function digestObject(value: unknown): { bytes: number; sha256: string } | undefined {
  if (value && typeof value === "object" && !Array.isArray(value)) {
    const candidate = value as Record<string, unknown>;
    if (typeof candidate.bytes === "number" && candidate.bytes >= 0 && typeof candidate.sha256 === "string" && /^sha256:[0-9a-f]{6,64}$/i.test(candidate.sha256)) {
      return { bytes: Math.floor(candidate.bytes), sha256: candidate.sha256.toLowerCase() };
    }
  }
  return undefined;
}

function digest(value: unknown): { bytes: number; sha256: string } | undefined {
  const existing = digestObject(value);
  if (existing) return existing;
  if (typeof value !== "string") return undefined;
  const bytes = Buffer.from(value, "utf8");
  return { bytes: bytes.length, sha256: `sha256:${createHash("sha256").update(bytes).digest("hex")}` };
}

function taskType(value: unknown): string {
  if (typeof value !== "string") return "general";
  const text = value.toLowerCase();
  if (/\b(migrat(?:e|ion)|schema change|backfill|rollback)\b/.test(text)) return "migration";
  if (/\b(bug|fix|broken|regression|crash|error|fails?|incorrect)\b/.test(text)) return "bugfix";
  if (/\b(investigat(?:e|ion)|diagnos(?:e|is)|root cause|why does|trace)\b/.test(text)) return "investigation";
  if (/\b(refactor|restructure|reorganize|cleanup|clean up)\b/.test(text)) return "refactor";
  if (/\b(review|audit|critique|assess)\b/.test(text)) return "review";
  if (/\b(verif(?:y|ication)|test only|prove|validate|check that)\b/.test(text)) return "verification";
  if (/\b(build|implement|add|create|ship|feature)\b/.test(text)) return "feature";
  return "general";
}

function taskContinuation(value: unknown, claimed: unknown): boolean {
  if (typeof value !== "string") return claimed === true;
  const prompt = value.trim().toLowerCase();
  if (!prompt || prompt.length > 160 || prompt.split(/\s+/).length > 14) return false;
  return /^(?:please\s+)?(?:continue|go ahead|keep going|proceed|do (?:it|that)|fix (?:it|that)|retry|try again|explain (?:it|that)|what do you mean|yes|yep|yeah|why\??|how\??)[.!?\s]*$/.test(prompt);
}

function taskTerms(value: unknown, claimed: unknown): string[] {
  const source = typeof value === "string" ? value.match(/[A-Za-z][A-Za-z0-9_./-]{2,63}/g) ?? [] : Array.isArray(claimed) ? claimed.filter((item): item is string => typeof item === "string") : [];
  const seen = new Set<string>();
  const terms: string[] = [];
  for (const raw of source) {
    const term = raw.toLowerCase().replace(/^[-./]+|[-./]+$/g, "");
    if (!term || term.length < 3 || term.length > 64 || term.includes("..") || term.startsWith("/") || TASK_STOPWORDS.has(term) || seen.has(term)) continue;
    if (/^(?:sk|pk|rk|ghp|github_pat|xox[baprs]|akia)[-_]/i.test(term) || /^[a-z0-9_-]{40,}$/i.test(term)) continue;
    seen.add(term);
    terms.push(term);
    if (terms.length === 12) break;
  }
  return terms;
}

function exactOutput(event: Record<string, unknown>): string | undefined {
  const value = event.tool_output ?? event.tool_response ?? event.output ?? event.result;
  if (value === undefined || digestObject(value)) return undefined;
  if (typeof value === "string") return Buffer.from(value, "utf8").toString("base64");
  try { return Buffer.from(JSON.stringify(value), "utf8").toString("base64"); } catch { return undefined; }
}

function inputState(input: unknown, cwd: string | undefined): string | undefined {
  if (!input || typeof input !== "object" || Array.isArray(input) || !cwd) return undefined;
  const values = input as Record<string, unknown>;
  const raw = [values.path, values.file_path, values.file, values.filename].find((value): value is string => typeof value === "string" && value.trim() !== "");
  if (!raw || raw.includes("\0")) return undefined;
  try {
    const path = resolve(cwd, raw);
    const stat = statSync(path);
    const identity = JSON.stringify({ path: realpathSync(path), size: stat.size, mtime_ms: stat.mtimeMs, mode: stat.mode });
    return `sha256:${createHash("sha256").update(identity).digest("hex")}`;
  } catch { return undefined; }
}

function needsRepositoryState(name: string, input: unknown): boolean {
  const lower = name.toLowerCase();
  if (lower.includes("search") || lower.includes("grep") || lower === "rg" || lower.includes("find")) return true;
  if (!input || typeof input !== "object" || Array.isArray(input)) return false;
  const values = input as Record<string, unknown>;
  const command = typeof values.command === "string" ? values.command : typeof values.cmd === "string" ? values.cmd : "";
  return /(?:^|\s)(?:go\s+test|pytest|test|go\s+build|build)(?:\s|$)/.test(command);
}

function gitDir(cwd: string): string | undefined {
  let current = resolve(cwd);
  for (;;) {
    const candidate = join(current, ".git");
    try {
      const stat = lstatSync(candidate);
      if (stat.isDirectory()) return candidate;
      if (stat.isFile()) {
        const match = readFileSync(candidate, "utf8").match(/^gitdir:\s*(.+)\s*$/m);
        if (match?.[1]) return resolve(current, match[1]);
      }
    } catch { /* ascend */ }
    const parent = dirname(current);
    if (parent === current) return undefined;
    current = parent;
  }
}

function repositoryState(cwd: string | undefined): string | undefined {
  if (!cwd) return undefined;
  try {
    const status = execFileSync("git", hardenedGitArgs(cwd, "status", "--porcelain=v1", "-z", "--branch", "--untracked-files=all"), {
      env: hardenedGitEnv(),
      timeout: 100, maxBuffer: 8 * 1024 * 1024, stdio: ["ignore", "pipe", "ignore"],
    });
    const directory = gitDir(cwd);
    if (!directory) return undefined;
    const hash = createHash("sha256");
    hash.update(realpathSync(cwd));
    hash.update("\0status\0");
    hash.update(status);
    let bytes = 0;
    const include = (label: string, path: string) => {
      hash.update(`\0${label}\0${path}\0`);
      try {
        const stat = lstatSync(path);
        if (!stat.isFile()) {
          hash.update(`mode:${stat.mode}:size:${stat.size}`);
          return true;
        }
        if (bytes + stat.size > 8 * 1024 * 1024) return false;
        const data = readFileSync(path);
        bytes += data.length;
        hash.update(data);
        return true;
      } catch {
        hash.update("missing");
        return true;
      }
    };
    if (!include("index", join(directory, "index"))) return undefined;
    for (const field of status.toString("utf8").split("\0").filter(Boolean)) {
      if (field.startsWith("## ")) continue;
      const relative = field.length >= 4 && field[2] === " " ? field.slice(3) : field;
      if (!relative || !include("changed", resolve(cwd, relative))) return undefined;
    }
    return `git:sha256:${hash.digest("hex")}`;
  } catch { return undefined; }
}

function request(agent: NativeAgent, eventName: string, sessionID: string, event: Record<string, unknown>): Record<string, unknown> {
  const cwd = bounded(event.cwd ?? event.working_directory ?? event.workingDirectory ?? process.cwd(), 4096);
  const claimedRepositoryState = bounded(event.repository_state ?? event.repositoryState, 512);
  const session: Record<string, unknown> = {
    caveman_session_id: `${agent}:${sessionID}`,
    host_session_id: sessionID,
    parent_session_id: bounded(event.parent_session_id ?? event.parentSessionId),
    cwd,
    repository_state: claimedRepositoryState,
  };
  const out: Record<string, unknown> = {
    protocol_version: 1,
    policy_mode: policyMode(),
    profile: profile(),
    agent: { id: agent, version: bounded(event.agent_version ?? event.version), surface: bounded(event.surface ?? event.platform) },
    session,
    event: { type: PROTOCOL_EVENTS[eventName], timestamp_ms: Date.now() },
  };
  const promptValue = event.prompt ?? event.user_prompt ?? event.userMessage;
  const prompt = digest(promptValue);
  if (prompt) {
    out.prompt = prompt;
    out.task_profile = {
      type: typeof promptValue === "string" ? taskType(promptValue) : bounded(event.task_type ?? event.taskType) ?? "general",
      terms: taskTerms(promptValue, event.task_terms ?? event.taskTerms),
      continuation: taskContinuation(promptValue, event.task_continuation ?? event.taskContinuation),
    };
  }
  const toolName = bounded(event.tool_name ?? event.toolName);
  if (toolName) {
    const tool: Record<string, unknown> = { name: toolName };
    const input = event.tool_input ?? event.toolInput ?? event.args;
    if (input !== undefined) {
      tool.input = input;
      const state = inputState(input, cwd);
      if (state) tool.input_state = state;
      if (needsRepositoryState(toolName, input)) session.repository_state = repositoryState(cwd) ?? claimedRepositoryState;
    }
    const output = exactOutput(event);
    if (output) tool.output = output;
    out.tool = tool;
  }
  return out;
}

function validResponse(value: unknown): value is RuntimeResponse {
  if (!value || typeof value !== "object" || Array.isArray(value)) return false;
  const response = value as Record<string, unknown>;
  if (response.protocol_version !== 1 || typeof response.action !== "string" || !ACTIONS.has(response.action)) return false;
  if (typeof response.visibility !== "string" || !VISIBILITY.has(response.visibility) || response.fail_open !== true) return false;
  if (response.policy_mode !== undefined && response.policy_mode !== "record" && response.policy_mode !== "safe" && response.policy_mode !== "max") return false;
  if (response.profile !== undefined && (typeof response.profile !== "string" || !PROFILES.has(response.profile as NativeProfile))) return false;
  return [[response.context, 64 * 1024], [response.message, 4096], [response.output_replacement, 2 * 1024 * 1024], [response.recovery_ref, 1024], [response.decision_id, 256]]
    .every(([field, max]) => field === undefined || (typeof field === "string" && Buffer.byteLength(field) <= (max as number)));
}

function callRuntime(value: Record<string, unknown>): Promise<RuntimeResponse | undefined> {
  return new Promise((settle) => {
    let done = false;
    let bytes = 0;
    const chunks: Buffer[] = [];
    const endpoint = process.platform === "win32"
      ? `\\\\.\\pipe\\caveman-native-${createHash("sha256").update(resolve(caveHome()).replaceAll("/", "\\").toLowerCase()).digest("hex").slice(0, 16)}`
      : join(caveHome(), "run", "native.sock");
    const socket = netConnect({ path: endpoint });
    const finish = (response?: RuntimeResponse) => {
      if (done) return;
      done = true;
      socket.destroy();
      settle(response);
    };
    socket.setTimeout(250);
    socket.on("connect", () => socket.end(`${JSON.stringify(value)}\n`));
    socket.on("data", (chunk: Buffer) => {
      bytes += chunk.length;
      if (bytes > 2 * 1024 * 1024) return finish();
      chunks.push(chunk);
    });
    socket.on("end", () => {
      try {
        const parsed = JSON.parse(Buffer.concat(chunks).toString("utf8"));
        finish(validResponse(parsed) ? parsed : undefined);
      } catch { finish(); }
    });
    socket.on("timeout", () => finish());
    socket.on("error", () => finish());
  });
}

function fallback(entry: Record<string, unknown>): void {
  try {
    const path = join(caveHome(), "runtime", "native-events.jsonl");
    mkdirSync(dirname(path), { recursive: true, mode: 0o700 });
    appendFileSync(path, `${JSON.stringify(entry)}\n`, { mode: 0o600 });
    chmodSync(path, 0o600);
  } catch { /* host remains fail-open */ }
}

// Budget for the delegated child. This runs inside a host hook, so an
// unbounded spawnSync could block the user's turn forever if index.js stalls,
// and on Windows process spawn is ~10x macOS before antivirus (#819) — the
// double-spawn alone can exceed the host's cap. Cap it below the host's and
// fall through to the fail-open path on expiry, the way every other subprocess
// in this package already does (#799).
const DELEGATE_TIMEOUT_MS = 3000;

function delegateToFullCLI(raw: Buffer, agent: NativeAgent): boolean {
  const cli = join(dirname(fileURLToPath(import.meta.url)), "index.js");
  const result = spawnSync(process.execPath, [cli, "native-hook", agent], { input: raw, maxBuffer: 3 * 1024 * 1024, env: process.env, timeout: DELEGATE_TIMEOUT_MS });
  if (!result.error && result.status === 0) {
    if (result.stdout?.length) process.stdout.write(result.stdout);
    if (result.stderr?.length) process.stderr.write(result.stderr);
    return true;
  }
  return false;
}

async function main(): Promise<void> {
  const agentArg = process.argv[2] === "native-hook" ? process.argv[3] : process.argv[2];
  const agent = agentArg === "claude" || agentArg === "codex" || agentArg === "hermes" || agentArg === "gemini" || agentArg === "opencode" || agentArg === "pi" ? agentArg : undefined;
  if (!agent) return;
  let raw: Buffer;
  let event: Record<string, unknown>;
  try {
    raw = await stdin();
    const parsed = JSON.parse(raw.toString("utf8") || "{}");
    if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) return;
    event = parsed as Record<string, unknown>;
  } catch { return; }
  const rawName = bounded(event.hook_event_name ?? event.event_name ?? event.event ?? process.argv[4]);
  const mapped = agent === "gemini" && rawName ? ({ BeforeAgent: "UserPromptSubmit", BeforeTool: "PreToolUse", AfterTool: "PostToolUse", BeforeModel: "ModelBefore", AfterModel: "ModelAfter", PreCompress: "PreCompact", AfterAgent: "Stop" } as Record<string, string>)[rawName] ?? rawName : rawName;
  const eventName = mapped && EVENTS.has(mapped) ? mapped : "Unknown";
  if (eventName === "SessionStart" || eventName === "PostCompact") {
    delegateToFullCLI(raw, agent);
    return;
  }
  const sessionID = bounded(event.session_id ?? event.sessionId);
  const entry: Record<string, unknown> = {
    protocol_version: 1,
    recorded_at: new Date().toISOString(),
    agent,
    event: eventName,
    payload_bytes: raw.length,
    payload_sha256: `sha256:${createHash("sha256").update(raw).digest("hex")}`,
  };
  if (sessionID) entry.host_session_id = sessionID;
  const toolName = bounded(event.tool_name ?? event.toolName);
  if (toolName) entry.tool_name = toolName;
  const cwd = bounded(event.cwd, 4096);
  if (cwd) entry.cwd_sha256 = `sha256:${createHash("sha256").update(cwd).digest("hex")}`;
  const runtimeRequest = sessionID && eventName !== "Unknown" ? request(agent, eventName, sessionID, event) : undefined;
  const response = runtimeRequest ? await callRuntime(runtimeRequest) : undefined;
  if (!response) {
    // A dead loopback gateway PORT at prompt time is the SessionStart revive
    // signal arriving mid-session: native routing still points this session's
    // next request at the local proxy, but a wrap-owned proxy idle-exits after
    // its wrap dies and a plain session has no other way to restart it — the
    // turn hard-fails with ConnectionRefused. Delegate this one event to the
    // full CLI, which revives the proxy exactly like SessionStart (and records
    // the event itself). The port probe — not the dead runtime socket — is the
    // trigger, so proxy-up/socket-down states (record pass-through, noProxy,
    // slow runtime) never pay the spawn; a failed delegate falls through so
    // the event is still recorded.
    if (eventName === "UserPromptSubmit") {
      const endpoint = localGatewayEndpoint();
      if (endpoint && !(await gatewayListening(endpoint)) && delegateToFullCLI(raw, agent)) return;
    }
    fallback(entry);
  }
  if (eventName === "SessionEnd" && response?.message) process.stderr.write(`${response.message}\n`);
  const mode = policyMode();
  const context = mode === "record" || !configuredCore() ? undefined : response?.context;
  if (agent === "hermes" && eventName === "UserPromptSubmit" && context) {
    process.stdout.write(JSON.stringify({ context }));
  } else if (context && eventName === "UserPromptSubmit") {
    process.stdout.write(JSON.stringify({ hookSpecificOutput: { hookEventName: eventName, additionalContext: context } }));
  } else if (context && (eventName === "PreToolUse" || eventName === "PermissionRequest")) {
    process.stdout.write(JSON.stringify({ hookSpecificOutput: { hookEventName: eventName, additionalContext: context } }));
  } else if (mode !== "record" && agent === "claude" && eventName === "PostToolUse" && response?.output_replacement) {
    process.stdout.write(JSON.stringify({ hookSpecificOutput: { hookEventName: "PostToolUse", updatedToolOutput: response.output_replacement } }));
  } else if (mode !== "record" && (agent === "opencode" || agent === "pi") && (eventName === "PostToolUse" || eventName === "PostToolUseFailure") && response) {
    process.stdout.write(JSON.stringify(response));
  }
}

await main();
