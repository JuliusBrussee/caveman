/**
 * `@caveman-ai/agent/code` — the Caveman coding agent.
 *
 * A coding agent whose tools run on the real host (`sandbox: "host"`) and whose
 * live-zone context is compressed reversibly by default. Optimized mode is the
 * default: the session starts the local Cave runtime when it can and runs with a
 * recoverable-only efficiency plan over history and tool results. Observe-only is
 * the loud fallback, announced with a banner and shown in the session status for
 * every turn after it.
 *
 * Public accounting is deliberately simple: context reductions are local token
 * estimates, spend is a public-catalog price estimate, and local execution makes
 * no provider savings claim.
 * - Live coding sessions are never lock-eligible; `compile` refuses host mode
 *   so nothing a session does can become a Cave Build.
 */
import { spawn } from "node:child_process";
import { readFile, realpath, stat, writeFile } from "node:fs/promises";
import { createInterface } from "node:readline/promises";
import { basename, dirname, isAbsolute, relative, resolve } from "node:path";
import type { Readable, Writable } from "node:stream";
import type { CavePlan } from "./build.js";
import { agent, type AgentDefinition } from "./index.js";
import { output, schema, tool, type ToolDefinition } from "./primitives.js";
import { hostShellInvocation, killProcessTree, portableInvocation } from "./portable-process.js";
import {
  createConversation,
  proveSegmentRecovery,
  rejectInternalRunOptions,
  resolveCaveRoute,
  resolveGatewayURL,
  runAgentInternal,
  type ConversationState,
  type ResolvedCaveRoute,
  type RunOptions,
  type RunResult,
  type SegmentRecoveryProof,
  type TransformTrace,
} from "./runtime.js";

/**
 * The only transforms a default coding plan may route. Every one of them is
 * CCR-recoverable (`recovery: exact_ccr`) and the runtime proves the round trip
 * byte-for-byte before a compressed body is allowed near the provider. `toon` is
 * excluded on purpose: it is forced-only structured re-encoding, never a default.
 */
export const RECOVERABLE_CODING_TRANSFORMS: readonly string[] = Object.freeze([
  "caveman.engine.code.v1",
  "caveman.engine.diff.v1",
  "caveman.engine.json.v1",
  "caveman.engine.log.v1",
  "caveman.engine.search-result.v1",
  "caveman.engine.terminal.v1",
  "caveman.engine.text.v1",
]);

/** Tool results in a coding session are command, file, and search output. */
const TOOL_RESULT_TRANSFORM = "caveman.engine.terminal.v1";
/** Conversation history is prose plus quoted snippets. */
const HISTORY_TRANSFORM = "caveman.engine.text.v1";

/**
 * Raw output caps, applied by each tool **before** any transform runs, so a
 * runaway `cat` cannot blow the context even with the engine absent. They also
 * sit under the runtime's 32 KiB inline tool-result ceiling, which is what keeps
 * observe-only sessions working on a machine with no engine at all.
 */
export const CODING_TOOL_OUTPUT_CAPS = Object.freeze({
  read_file: 24_000,
  grep: 16_000,
  bash: 24_000,
  edit_file: 2_000,
});

const GREP_MAX_MATCHES = 200;
const BASH_TIMEOUT_MS = 120_000;
const READ_TIMEOUT_MS = 30_000;

/** Model calls per turn = 1 + ceil(retry_cascade_reserve / 256) = 64. */
const RETRY_CASCADE_RESERVE = 16_128;
const OUTPUT_MAX_TOKENS = 8_000;

export const OBSERVE_ONLY_BANNER = [
  "cave: OBSERVE-ONLY — the Caveman engine/gateway is not available here.",
  "  Context transforms and gateway telemetry are OFF. Provider usage and local context estimates remain available.",
  "  Turn optimized mode on: npm i -g @caveman-ai/cli && caveman start",
].join("\n");

const INSTRUCTIONS = [
  "You are a coding agent working inside one workspace directory.",
  "",
  "Work like an engineer: read before you edit, prefer the smallest change that",
  "solves the task, and verify with a command when a command can verify it.",
  "",
  "Tools:",
  "- read_file reads a file (optionally a line range) from the workspace.",
  "- grep searches the workspace with ripgrep, falling back to grep.",
  "- bash runs one shell command in the workspace and returns its combined output.",
  "- edit_file replaces an exact string in a file. Match enough surrounding text",
  "  to make the target unique, or pass replace_all.",
  "",
  "Tool output is capped before it reaches you. When a result says it was capped,",
  "narrow the request rather than repeating it.",
  "",
  "Older turns and older tool results may arrive wrapped in <cave-compressed>",
  "markers. Those are reversible: call cave_retrieve with the recovery_handle in",
  "the marker to get the exact original bytes back. Retrieve before guessing.",
  "",
  "Say what you changed and why. Do not claim a command passed unless you ran it.",
].join("\n");

export interface CodingAgentOptions {
  /** Workspace root. Tools refuse to read or write outside it. */
  workspace?: string;
  /** `provider/model`. Defaults to `CAVE_MODEL`, else the one configured provider. */
  model?: string;
  /** Agent id; also the recovery namespace and default workflow name. */
  id?: string;
  /** Extra instruction text appended to the built-in coding instructions. */
  instructions?: string;
  /** Per-tool raw output caps in bytes, before any transform. */
  outputCaps?: Partial<typeof CODING_TOOL_OUTPUT_CAPS>;
}

export interface CodingAgent {
  readonly definition: AgentDefinition;
  /** The default efficiency plan: recoverable routes over the live zone. */
  readonly plan: CavePlan;
  readonly workspace: string;
  readonly modelID: string;
  /** Largest raw tool outputs seen this process, kept for the recovery proof. */
  readonly samples: ToolOutputSample[];
}

export interface ToolOutputSample {
  readonly label: string;
  readonly text: string;
}

const MAX_SAMPLES = 4;

/**
 * The default efficiency plan for an interactive coding session.
 *
 * One route per dynamic segment kind, because the runtime refuses an ambiguous
 * dynamic route (two routes matching one runtime segment collapse to
 * `dynamic_route_ambiguous` and the segment passes through untouched). Budgets
 * are sized for a long interactive session rather than a fixture run; they are
 * ceilings that fail the turn honestly rather than silently truncating context.
 */
export function defaultCodingPlan(modelID: string, namespace: string): CavePlan {
  return {
    schema_version: 1,
    plan_id: "caveman-code.default.recoverable-live-zone",
    model: modelID,
    reasoning: "none",
    segment_routes: [
      { segment_kind: "tool_result", transform_id: TOOL_RESULT_TRANSFORM, fallback: "original" },
      { segment_kind: "history", transform_id: HISTORY_TRANSFORM, fallback: "original" },
    ],
    budgets: {
      instructions: 64_000,
      tools: 32_000,
      memory: 8_000,
      history: 4_000_000,
      results_artifacts: 4_000_000,
      reasoning: 64_000,
      output: 512_000,
      retry_cascade_reserve: RETRY_CASCADE_RESERVE,
    },
    recovery: { namespace, tools: ["cave_retrieve"] },
    fallbacks: { unknown: "original", transform_error: "original", not_smaller: "original" },
  };
}

export function createCodingAgent(options: CodingAgentOptions = {}): CodingAgent {
  const workspace = resolve(options.workspace ?? process.cwd());
  const id = options.id ?? "caveman-code";
  const modelID = resolveCodingModelID(options.model);
  const caps = { ...CODING_TOOL_OUTPUT_CAPS, ...options.outputCaps };
  const samples: ToolOutputSample[] = [];
  const record = (label: string, text: string) => {
    samples.push({ label, text });
    samples.sort((left, right) => right.text.length - left.text.length);
    samples.length = Math.min(samples.length, MAX_SAMPLES);
  };
  const definition = agent({
    id,
    instructions: options.instructions === undefined
      ? INSTRUCTIONS
      : `${INSTRUCTIONS}\n\n${options.instructions}`,
    model: modelID,
    reasoning: "off",
    sandbox: "host",
    output: output({ maxTokens: OUTPUT_MAX_TOKENS }),
    tools: codingTools(workspace, caps, record),
  });
  return Object.freeze({
    definition,
    plan: defaultCodingPlan(modelID, id),
    workspace,
    modelID,
    samples,
  });
}

// ---------------------------------------------------------------------------
// Tools — host-sandbox closures. Effects are declared honestly: host mode
// changes enforcement, not declaration.
// ---------------------------------------------------------------------------

function codingTools(
  workspace: string,
  caps: typeof CODING_TOOL_OUTPUT_CAPS,
  record: (label: string, text: string) => void,
): ToolDefinition[] {
  // The workspace is canonicalized once, then every candidate path is
  // canonicalized against it, so containment compares real locations rather
  // than strings. Resolved lazily because the directory need not exist yet when
  // the agent is built.
  let canonicalWorkspace: Promise<string> | undefined;
  const workspaceRoot = () => (canonicalWorkspace ??= realpath(workspace));
  const contained = async (candidate: string) =>
    containedPath(await workspaceRoot(), candidate);

  const readFileTool = tool({
    name: "read_file",
    description:
      "Read a UTF-8 file from the workspace. Optional offset/limit read a line range. " +
      `Output is capped at ${caps.read_file} bytes.`,
    input: schema.object({
      path: schema.string(),
      offset: schema.optional(schema.integer()),
      limit: schema.optional(schema.integer()),
    }),
    effect: "read",
    result: "inline",
    timeoutMs: READ_TIMEOUT_MS,
    async execute(input) {
      const target = await contained(input.path);
      const info = await stat(target);
      if (!info.isFile()) throw new Error(`caveman-code: not a file: ${input.path}`);
      const content = await readFile(target, "utf8");
      const lines = content.split("\n");
      const offset = Math.max(1, input.offset ?? 1);
      const limit = input.limit === undefined ? lines.length : Math.max(1, input.limit);
      const selected = lines.slice(offset - 1, offset - 1 + limit);
      const numbered = selected
        .map((line, index) => `${offset + index}\t${line}`)
        .join("\n");
      const text = capOutput(numbered, caps.read_file);
      record(`read_file:${input.path}`, text);
      return text;
    },
  });

  const grepTool = tool({
    name: "grep",
    description:
      "Search the workspace for a regular expression with ripgrep (grep fallback). " +
      `Returns at most ${GREP_MAX_MATCHES} matches, capped at ${caps.grep} bytes.`,
    input: schema.object({
      pattern: schema.string(),
      path: schema.optional(schema.string()),
      glob: schema.optional(schema.string()),
    }),
    effect: "read",
    result: "inline",
    timeoutMs: READ_TIMEOUT_MS,
    async execute(input, signal) {
      const root = await workspaceRoot();
      const scope = input.path === undefined ? root : await contained(input.path);
      const relativeScope = scope === root ? "." : relative(root, scope);
      const ripgrep = [
        "--line-number", "--no-heading", "--color", "never",
        "--max-count", String(GREP_MAX_MATCHES),
        ...(input.glob === undefined ? [] : ["--glob", input.glob]),
        "--regexp", input.pattern, "--", relativeScope,
      ];
      let run = await runProcess("rg", ripgrep, root, READ_TIMEOUT_MS, signal);
      if (run.spawnFailed) {
        run = await runProcess("grep", [
          "-rnI", "-m", String(GREP_MAX_MATCHES), "-E", "-e", input.pattern, "--", relativeScope,
        ], root, READ_TIMEOUT_MS, signal);
      }
      if (run.spawnFailed) throw new Error("caveman-code: neither rg nor grep is available");
      const body = run.output.trim() === "" ? "no matches" : run.output;
      const text = capOutput(body, caps.grep);
      record(`grep:${input.pattern}`, text);
      return text;
    },
  });

  const bashTool = tool({
    name: "bash",
    description:
      "Run one shell command in the workspace and return its combined stdout and stderr. " +
      `Times out after ${BASH_TIMEOUT_MS} ms; output is capped at ${caps.bash} bytes.`,
    input: schema.object({
      command: schema.string(),
      timeoutMs: schema.optional(schema.integer()),
    }),
    effect: "external",
    result: "inline",
    timeoutMs: BASH_TIMEOUT_MS,
    async execute(input, signal) {
      const timeoutMs = Math.min(input.timeoutMs ?? BASH_TIMEOUT_MS, BASH_TIMEOUT_MS);
      const shell = hostShellInvocation(input.command, process.platform, buildCodingProcessEnv());
      const run = await runProcess(
        shell.command,
        shell.args,
        await workspaceRoot(),
        timeoutMs,
        signal,
      );
      if (run.spawnFailed) throw new Error("caveman-code: host command shell is not available");
      const body = [
        `exit ${run.timedOut ? `timeout after ${timeoutMs}ms` : run.code}`,
        run.output.trim() === "" ? "(no output)" : run.output,
      ].join("\n");
      const text = capOutput(body, caps.bash);
      record(`bash:${input.command.slice(0, 60)}`, text);
      return text;
    },
  });

  const editTool = tool({
    name: "edit_file",
    description:
      "Replace an exact string in a workspace file. The old string must appear exactly " +
      "once unless replace_all is set. Writes to disk.",
    input: schema.object({
      path: schema.string(),
      old_string: schema.string(),
      new_string: schema.string(),
      replace_all: schema.optional(schema.boolean()),
    }),
    effect: "write",
    result: "inline",
    timeoutMs: READ_TIMEOUT_MS,
    async execute(input) {
      if (input.old_string === input.new_string) {
        throw new Error("caveman-code: old_string and new_string are identical");
      }
      const target = await contained(input.path);
      const content = await readFile(target, "utf8");
      const occurrences = content.split(input.old_string).length - 1;
      if (occurrences === 0) {
        throw new Error(`caveman-code: old_string not found in ${input.path}`);
      }
      if (occurrences > 1 && input.replace_all !== true) {
        throw new Error(
          `caveman-code: old_string appears ${occurrences} times in ${input.path}; ` +
          "add surrounding context or pass replace_all",
        );
      }
      // split/join UNCONDITIONALLY. String.prototype.replace
      // interprets `$&`, `$\``, `$'`, `$$`, `$1`… in the REPLACEMENT even for a
      // string pattern, so a new_string containing any of them would silently
      // corrupt the file. The non-replace_all branch is guaranteed exactly one
      // occurrence above, so joining replaces precisely that one.
      const updated = content.split(input.old_string).join(input.new_string);
      await writeFile(target, updated, "utf8");
      const replaced = input.replace_all === true ? occurrences : 1;
      return capOutput(
        `edited ${input.path}: ${replaced} replacement${replaced === 1 ? "" : "s"}`,
        caps.edit_file,
      );
    },
  });

  return [readFileTool, grepTool, bashTool, editTool];
}

/**
 * Resolve a caller path against the canonical workspace and refuse anything
 * that lands outside it.
 *
 * A lexical prefix check is not containment: a symlink inside the workspace
 * pointing anywhere on the filesystem passes it. Both sides are canonicalized
 * first, matching how `stageSandboxSourceGraph` decides the same question.
 */
async function containedPath(canonicalWorkspace: string, candidate: string): Promise<string> {
  const full = await canonicalizePath(resolve(canonicalWorkspace, candidate));
  if (escapesRoot(relative(canonicalWorkspace, full))) {
    throw new Error(`caveman-code: path escapes the workspace: ${candidate}`);
  }
  return full;
}

function escapesRoot(path: string): boolean {
  return path === ".." || path.startsWith("../") || path.startsWith("..\\") || isAbsolute(path);
}

/**
 * `realpath` for a path whose leaf may not exist yet (a file `edit_file` is
 * about to create): canonicalize the deepest existing ancestor and re-attach
 * the missing tail, so every symlink on the existing part is still resolved.
 */
async function canonicalizePath(target: string): Promise<string> {
  const missing: string[] = [];
  let current = target;
  for (;;) {
    try {
      const canonical = await realpath(current);
      return missing.length === 0 ? canonical : resolve(canonical, ...missing);
    } catch (error) {
      if ((error as NodeJS.ErrnoException).code !== "ENOENT") throw error;
      const parent = dirname(current);
      if (parent === current) throw error;
      missing.unshift(basename(current));
      current = parent;
    }
  }
}

export function capOutput(text: string, maxBytes: number): string {
  const encoded = new TextEncoder().encode(text);
  if (encoded.byteLength <= maxBytes) return text;
  const kept = new TextDecoder().decode(encoded.slice(0, maxBytes));
  return `${kept}\n[caveman-code: output capped at ${maxBytes} of ${encoded.byteLength} bytes; narrow the request]`;
}

type ProcessRun = {
  output: string;
  code: number | null;
  timedOut: boolean;
  spawnFailed: boolean;
};

/**
 * How long the run waits after the command itself exits for its pipes to close.
 * `close` normally follows `exit` immediately; the wait only matters when a
 * background descendant still holds the inherited stdout, and it bounds that
 * case instead of hanging on it.
 */
const EXIT_FLUSH_GRACE_MS = 100;

/**
 * Baseline environment for the coding agent's host subprocesses.
 *
 * NOT a spread of `process.env`: a model-driven `bash`/`grep`/`rg` must not
 * inherit the framework's own account and provider credentials
 * (`CAVE_API_KEY`, `ANTHROPIC_API_KEY`, …) and exfiltrate them. Only a fixed
 * shell/locale baseline passes through.
 *
 * This is a credential boundary, NOT a sandbox: `bash` is **uncontained by
 * design** — it runs arbitrary host commands with the user's own privileges.
 * The env allow-list only removes the framework-managed secrets from what those
 * commands can read; it does not, and is not meant to, contain what they do.
 */
function buildCodingProcessEnv(): NodeJS.ProcessEnv {
  const allow = [
    "PATH", "HOME", "USER", "LOGNAME", "SHELL", "LANG", "LC_ALL", "LC_CTYPE",
    "TZ", "TERM", "TMPDIR", "PWD", "ComSpec", "PATHEXT", "SystemRoot", "TEMP",
    "TMP", "USERPROFILE", "APPDATA", "LOCALAPPDATA",
  ];
  const env: NodeJS.ProcessEnv = {};
  for (const key of allow) {
    const value = process.env[key];
    if (value !== undefined) env[key] = value;
  }
  return env;
}

function runProcess(
  command: string,
  args: readonly string[],
  cwd: string,
  timeoutMs: number,
  signal?: AbortSignal,
): Promise<ProcessRun> {
  return new Promise((accept) => {
    // Its own process group: `cmd &` puts children there too, so the timeout can
    // kill the whole tree rather than just the shell that spawned it.
    const env = buildCodingProcessEnv();
    let invocation;
    try {
      invocation = portableInvocation(command, args, { env });
    } catch {
      accept({ output: "", code: null, timedOut: false, spawnFailed: true });
      return;
    }
    const child = spawn(invocation.command, [...invocation.args], {
      cwd,
      env,
      stdio: ["ignore", "pipe", "pipe"],
      detached: process.platform !== "win32",
    });
    const chunks: Buffer[] = [];
    let bytes = 0;
    let timedOut = false;
    let settled = false;
    let exitCode: number | null = null;
    const collect = (chunk: Buffer) => {
      // Hard ceiling well above every tool cap so a runaway process cannot grow
      // this process's memory before capOutput ever sees the text.
      if (bytes > 4 * 1024 * 1024) return;
      bytes += chunk.byteLength;
      chunks.push(chunk);
    };
    child.stdout.on("data", collect);
    child.stderr.on("data", collect);
    const killTree = () => killProcessTree(child);
    const timer = setTimeout(() => {
      timedOut = true;
      killTree();
    }, timeoutMs);
    const abort = () => killTree();
    signal?.addEventListener("abort", abort, { once: true });
    const settle = (run: ProcessRun) => {
      if (settled) return;
      settled = true;
      clearTimeout(timer);
      signal?.removeEventListener("abort", abort);
      accept(run);
    };
    const finish = () => {
      // Whatever still holds these pipes is not this run's business: reading is
      // over, and a live handle would keep the whole process alive waiting on a
      // background command the user deliberately detached.
      child.stdout.destroy();
      child.stderr.destroy();
      settle({
        output: Buffer.concat(chunks).toString("utf8"),
        code: exitCode,
        timedOut,
        spawnFailed: false,
      });
    };
    child.once("error", () => settle({ output: "", code: null, timedOut, spawnFailed: true }));
    child.once("close", (code) => {
      exitCode = code;
      finish();
    });
    // `close` waits for stdio EOF, which a surviving background descendant never
    // gives. `exit` is the command's own answer, so the run settles on it with
    // whatever output arrived rather than waiting on a process it does not own.
    child.once("exit", (code) => {
      exitCode = code;
      setTimeout(finish, EXIT_FLUSH_GRACE_MS).unref();
    });
  });
}

function resolveCodingModelID(explicit: string | undefined): string {
  const requested = explicit ?? process.env.CAVE_MODEL;
  if (requested !== undefined && requested !== "") {
    const slash = requested.indexOf("/");
    if (slash <= 0 || slash === requested.length - 1) {
      throw new Error("caveman-code: model must use provider/model format");
    }
    return requested;
  }
  const configured: string[] = [];
  if (process.env.ANTHROPIC_API_KEY) configured.push("anthropic/claude-sonnet-4-6");
  if (process.env.OPENAI_API_KEY) configured.push("openai/gpt-5.5");
  if (process.env.GEMINI_API_KEY || process.env.GOOGLE_API_KEY) {
    configured.push("google/gemini-2.5-pro");
  }
  if (configured.length !== 1) {
    throw new Error(
      configured.length === 0
        ? "caveman-code: no supported provider credential found; set ANTHROPIC_API_KEY, OPENAI_API_KEY, or GEMINI_API_KEY"
        : "caveman-code: multiple provider credentials found; set CAVE_MODEL to pick one",
    );
  }
  return configured[0]!;
}

// ---------------------------------------------------------------------------
// Session
// ---------------------------------------------------------------------------

export type SessionMode = "optimized" | "observe-only";

export interface CodingSessionOptions {
  conversation?: ConversationState;
  cave?: "auto" | "off";
  gatewayURL?: string;
  engineBin?: string;
  workflow?: string;
  /** Best-effort local spend cap in USD at public catalog list prices. */
  maxCostUsd?: number;
  /** Receives every notice, including the observe-only banner. */
  onNotice?: (line: string) => void;
  /** Test seam: passed through to the gateway readiness probe. */
  fetch?: typeof globalThis.fetch;
  ensureRuntime?: boolean;
}

export interface TurnBill {
  turn: number;
  mode: SessionMode;
  provider: string;
  model: string;
  /** Per-kind context tokens for this run, from `RunResult.contextBill`. */
  contextBill: Record<string, number>;
  /** Sum of `transformTrace[].beforeTokens` over applied transforms. */
  transformedTokensBefore: number;
  /** Sum of `transformTrace[].afterTokens` over applied transforms. */
  transformedTokensAfter: number;
  /** before − after. A local token estimate, never a dollar figure. */
  tokensSavedInferred: number;
  /** The basis of before/after/saved. Never blended across bases. */
  tokensBasis: TransformTrace["tokensBasis"];
  transformIDs: string[];
  transformFailures: string[];
  recoveryResolved: boolean;
  usageBasis: RunResult["usageBasis"];
  inputTokens: number;
  outputTokens: number;
  cacheReadTokens: number;
  cacheWriteTokens: number;
  costUsd: number;
  priceBasis: RunResult["priceBasis"];
  latencyMs: number;
}

export interface TurnRecord {
  input: string;
  text: string;
  toolCalls: string[];
  bill: TurnBill;
  /** True when this turn is the one that took the session to observe-only. */
  degraded: boolean;
}

export interface CodingSession {
  readonly agent: CodingAgent;
  readonly conversation: ConversationState;
  readonly options: CodingSessionOptions;
  readonly gatewayURL: string;
  /**
   * The route resolved once, at session start. Every turn is handed this
   * instead of re-probing, so a session makes exactly one runtime-ensure
   * attempt however many turns it runs.
   */
  readonly route: ResolvedCaveRoute;
  mode: SessionMode;
  /** Every notice emitted, banner included. The loud fallback leaves a record. */
  readonly notices: string[];
  readonly turns: TurnRecord[];
  proofShown: boolean;
}

/**
 * Start a session in the best mode this machine can actually deliver.
 *
 * With `cave: "auto"` this probes the local Cave runtime and starts it when it
 * is installed but not running, so a machine with the engine present is
 * optimized from the first turn. When the probe fails, the session degrades to
 * observe-only **loudly**: the banner is emitted through `onNotice` and recorded
 * on the session, and every turn afterwards reports `observe-only`.
 *
 * The probe happens **once**. Its answer is kept on the session and handed to
 * every turn, so a machine with no runtime pays one failed start attempt for the
 * whole session rather than one per turn.
 */
export async function startCodingSession(
  codingAgent: CodingAgent,
  options: CodingSessionOptions = {},
): Promise<CodingSession> {
  const gatewayURL = resolveGatewayURL(options.gatewayURL);
  const route = await resolveCaveRoute(gatewayURL, {
    ...(options.cave === undefined ? {} : { cave: options.cave }),
    ...(options.ensureRuntime === undefined ? {} : { ensureRuntime: options.ensureRuntime }),
    ...(options.fetch === undefined ? {} : { fetch: options.fetch }),
    billingProofRequired: options.maxCostUsd !== undefined,
  }, false);
  const session: CodingSession = {
    agent: codingAgent,
    conversation: options.conversation ?? createConversation(),
    options,
    gatewayURL,
    route,
    mode: "optimized",
    notices: [],
    turns: [],
    proofShown: false,
  };
  if (!route.useGateway) degrade(session);
  return session;
}

/**
 * The route a turn actually runs on. Session mode governs: once a session has
 * degraded, degradation is sticky, so a stored route that still says "gateway"
 * never resurrects an observe-only session.
 */
function turnRoute(session: CodingSession): ResolvedCaveRoute {
  return session.mode === "optimized" ? session.route : { useGateway: false };
}

function degrade(session: CodingSession): void {
  if (session.mode === "observe-only") return;
  session.mode = "observe-only";
  session.notices.push(OBSERVE_ONLY_BANNER);
  session.options.onNotice?.(OBSERVE_ONLY_BANNER);
}

/**
 * A run that carries a plan refuses to degrade silently — it throws
 * `cave_gateway_required_for_locked_plan` instead. That specific failure, and
 * only that failure, earns one retry without the plan.
 *
 * A session answers the routing question once and pins it, so a turn is never
 * the thing that discovers an absent gateway; this classification is what turns
 * that refusal into a sticky session degradation wherever it still comes from.
 */
export function classifyTurnFailure(error: unknown): "degrade_to_observe_only" | "fatal" {
  const message = error instanceof Error ? error.message : String(error);
  return message.includes("cave_gateway_required_for_locked_plan")
    ? "degrade_to_observe_only"
    : "fatal";
}

export async function runCodingTurn(
  session: CodingSession,
  input: string,
  overrides: RunOptions = {},
): Promise<TurnRecord> {
  // `overrides` is caller input reaching an internal run path, so it faces the
  // same guard the public entry points apply — before the session's own plan and
  // route are merged in, which are the very fields the guard forbids a caller
  // from forging.
  rejectInternalRunOptions(overrides);
  const base: RunOptions & { caveRoute: ResolvedCaveRoute } = {
    rootDir: session.agent.workspace,
    conversation: session.conversation,
    workflow: session.options.workflow ?? session.agent.definition.id,
    gatewayURL: session.gatewayURL,
    ...(session.options.cave === undefined ? {} : { cave: session.options.cave }),
    ...(session.options.engineBin === undefined ? {} : { engineBin: session.options.engineBin }),
    ...(session.options.maxCostUsd === undefined ? {} : { maxCostUsd: session.options.maxCostUsd }),
    ...(session.options.fetch === undefined ? {} : { fetch: session.options.fetch }),
    ...(session.options.ensureRuntime === undefined
      ? {}
      : { ensureRuntime: session.options.ensureRuntime }),
    ...overrides,
    // Last, so the session's route wins over anything an override says about
    // routing: the session probed once and its mode governs from then on.
    caveRoute: turnRoute(session),
  };
  const startedOptimized = session.mode === "optimized";
  let result: RunResult;
  if (startedOptimized) {
    try {
      result = await runAgentInternal(session.agent.definition, input, {
        ...base,
        candidatePlan: session.agent.plan,
      });
    } catch (error) {
      if (classifyTurnFailure(error) === "fatal") throw error;
      degrade(session);
      result = await runAgentInternal(session.agent.definition, input, {
        ...base,
        caveRoute: turnRoute(session),
      });
    }
  } else {
    result = await runAgentInternal(session.agent.definition, input, base);
  }
  // The run itself is the authority on whether the gateway optimized this
  // traffic: a reachable gateway that does not proxy this model's provider
  // still comes back observe-only, and that degrades the session exactly like a
  // missing runtime does.
  if (result.mode === "observe-only") degrade(session);
  const record: TurnRecord = {
    input,
    text: result.text,
    toolCalls: result.toolCalls,
    bill: turnBill(session.turns.length + 1, result),
    degraded: startedOptimized && session.mode === "observe-only",
  };
  session.turns.push(record);
  return record;
}

function turnBill(turn: number, result: RunResult): TurnBill {
  const applied = result.transformTrace.filter((item) => item.outcome === "applied");
  // A token delta is only meaningful within one basis; refuse to blend
  // byte-derived and engine-counted figures into a single reduction.
  const bases = new Set(applied.map((item) => item.tokensBasis));
  if (bases.size > 1) throw new Error("cave_transform_trace_basis_mixed");
  const tokensBasis: TransformTrace["tokensBasis"] = applied[0]?.tokensBasis ?? "byte_derived";
  const before = applied.reduce((sum, item) => sum + item.beforeTokens, 0);
  const after = applied.reduce((sum, item) => sum + item.afterTokens, 0);
  return {
    turn,
    mode: result.mode,
    provider: result.provider,
    model: result.model,
    contextBill: result.contextBill,
    transformedTokensBefore: before,
    transformedTokensAfter: after,
    // The reduction is the delta of what was ACTUALLY SENT, so a barely-
    // compressible turn whose wrapper cost more than it saved reports zero
    // rather than a phantom positive; the raw before/after stay exposed.
    tokensSavedInferred: Math.max(0, before - after),
    tokensBasis,
    transformIDs: result.transformIDs,
    transformFailures: result.transformFailures,
    recoveryResolved: result.recoveryResolved,
    usageBasis: result.usageBasis,
    inputTokens: result.inputTokens,
    outputTokens: result.outputTokens,
    cacheReadTokens: result.cacheReadTokens,
    cacheWriteTokens: result.cacheWriteTokens,
    costUsd: result.costUsd,
    priceBasis: result.priceBasis,
    latencyMs: result.latencyMs,
  };
}

export interface SessionBill {
  turns: number;
  mode: SessionMode;
  transformedTokensBefore: number;
  transformedTokensAfter: number;
  tokensSavedInferred: number;
  /** The basis of the summed reduction. Never blended across bases. */
  tokensBasis: TransformTrace["tokensBasis"];
  inputTokens: number;
  outputTokens: number;
  cacheReadTokens: number;
  cacheWriteTokens: number;
  costUsd: number;
  priceBasis: RunResult["priceBasis"];
  usageBasis: RunResult["usageBasis"];
  transformIDs: string[];
  transformFailures: string[];
}

export function sessionBill(session: CodingSession): SessionBill {
  const bills = session.turns.map((item) => item.bill);
  const sum = (pick: (bill: TurnBill) => number) =>
    bills.reduce((total, bill) => total + pick(bill), 0);
  const transformIDs = [...new Set(bills.flatMap((bill) => bill.transformIDs))].sort();
  const failures = [...new Set(bills.flatMap((bill) => bill.transformFailures))].sort();
  // Refuse to sum a reduction across mixed token bases.
  const bases = new Set(bills.map((bill) => bill.tokensBasis));
  if (bases.size > 1) throw new Error("cave_transform_trace_basis_mixed");
  const tokensBasis: TransformTrace["tokensBasis"] = bills[0]?.tokensBasis ?? "byte_derived";
  return {
    turns: bills.length,
    mode: session.mode,
    transformedTokensBefore: sum((bill) => bill.transformedTokensBefore),
    transformedTokensAfter: sum((bill) => bill.transformedTokensAfter),
    tokensSavedInferred: sum((bill) => bill.tokensSavedInferred),
    tokensBasis,
    inputTokens: sum((bill) => bill.inputTokens),
    outputTokens: sum((bill) => bill.outputTokens),
    cacheReadTokens: sum((bill) => bill.cacheReadTokens),
    cacheWriteTokens: sum((bill) => bill.cacheWriteTokens),
    costUsd: sum((bill) => bill.costUsd),
    // A session with no turns has no basis to report. `every` on an empty list
    // is true, which would have labelled a zero nobody measured as a catalog
    // price from provider-reported usage; the honest zero says the basis is
    // absent instead.
    priceBasis: bills.length > 0 && bills.every((bill) => bill.priceBasis === "public_catalog")
      ? "public_catalog"
      : "unpriced",
    usageBasis: bills.length > 0 && bills.every((bill) => bill.usageBasis === "provider_reported")
      ? "provider_reported"
      : "unavailable",
    transformIDs,
    transformFailures: failures,
  };
}

// ---------------------------------------------------------------------------
// Printing. Context reductions are local token estimates; spend is shown
// separately at public catalog prices.
// ---------------------------------------------------------------------------

function count(value: number): string {
  return value.toLocaleString("en-US");
}

export function formatTurnBill(bill: TurnBill, sessionSaved: number): string[] {
  const lines = [
    `turn ${bill.turn} · ${bill.mode} · ${bill.provider}/${bill.model} · ${count(bill.latencyMs)} ms`,
  ];
  if (bill.mode === "observe-only") {
    lines.push("  context transforms: off (observe-only) — provider usage and local context estimates remain available");
  } else if (bill.transformIDs.length === 0) {
    lines.push("  context transforms: none applied this turn (nothing was smaller compressed)");
  } else {
    lines.push(`  context transforms: ${bill.transformIDs.join(", ")}`);
    lines.push(
      `  transformed context: ${count(bill.transformedTokensBefore)} tokens before` +
      ` → ${count(bill.transformedTokensAfter)} after`,
    );
  }
  lines.push(
    `  tokens saved: ${count(bill.tokensSavedInferred)} this turn ·` +
    ` ${count(sessionSaved)} this session — local estimate, token counts only`,
  );
  lines.push(
    `  provider usage (${bill.usageBasis}): in ${count(bill.inputTokens)} ·` +
    ` out ${count(bill.outputTokens)} · cache read ${count(bill.cacheReadTokens)} ·` +
    ` cache write ${count(bill.cacheWriteTokens)}`,
  );
  lines.push(
    `  spend: ${bill.costUsd.toFixed(6)} USD measured at public catalog list prices` +
    ` (${bill.priceBasis})`,
  );
  if (bill.transformFailures.length > 0) {
    lines.push(`  transform fell back to original: ${bill.transformFailures.join(", ")}`);
  }
  return lines;
}

export function formatSessionBill(bill: SessionBill): string[] {
  // Nothing ran, so nothing is reported: printing basis-labelled zeros would
  // claim a measurement — a catalog price, provider-reported usage — for calls
  // that never happened.
  if (bill.turns === 0) {
    return [
      `session · 0 turns · ${bill.mode}`,
      "  no provider calls this session — nothing measured, nothing claimed",
    ];
  }
  return [
    `session · ${bill.turns} turn${bill.turns === 1 ? "" : "s"} · ${bill.mode}`,
    bill.transformIDs.length === 0
      ? "  context transforms: none applied"
      : `  context transforms: ${bill.transformIDs.join(", ")}`,
    `  transformed context: ${count(bill.transformedTokensBefore)} tokens before` +
    ` → ${count(bill.transformedTokensAfter)} after`,
    `  tokens saved: ${count(bill.tokensSavedInferred)} — local estimate,` +
    " token counts only; no provider savings claim",
    `  provider usage (${bill.usageBasis}): in ${count(bill.inputTokens)} ·` +
    ` out ${count(bill.outputTokens)} · cache read ${count(bill.cacheReadTokens)} ·` +
    ` cache write ${count(bill.cacheWriteTokens)}`,
    `  spend: ${bill.costUsd.toFixed(6)} USD measured at public catalog list prices` +
    ` (${bill.priceBasis})`,
  ];
}

// ---------------------------------------------------------------------------
// Recovery proof
// ---------------------------------------------------------------------------

export interface CodingRecoveryProof extends SegmentRecoveryProof {
  segment: string;
}

/**
 * Take the largest raw tool output this session produced, push it through the
 * same engine compress/retrieve pair the plan and `cave_retrieve` use, and
 * compare bytes. This is the reversibility proof, not a savings claim.
 */
export async function proveRecovery(session: CodingSession): Promise<CodingRecoveryProof> {
  const sample = session.agent.samples[0];
  if (!sample) throw new Error("caveman-code: no tool output recorded yet to prove recovery on");
  const proof = await proveSegmentRecovery({
    body: new TextEncoder().encode(sample.text),
    transformID: TOOL_RESULT_TRANSFORM,
    ...(session.options.engineBin === undefined ? {} : { engineBin: session.options.engineBin }),
  });
  return { ...proof, segment: sample.label };
}

export function formatRecoveryProof(proof: CodingRecoveryProof): string {
  if (proof.outcome === "recovered") {
    return `recovery proof: ${proof.segment} round-trip OK (sha256 match ${proof.originalSHA256.slice(0, 12)})`;
  }
  if (proof.outcome === "not_smaller") {
    return `recovery proof: ${proof.segment} not compressed — the engine kept the original bytes, nothing to recover`;
  }
  return `recovery proof: ${proof.segment} FAILED (sha256 mismatch) — the plan falls back to the original body`;
}

// ---------------------------------------------------------------------------
// Interactive loop
// ---------------------------------------------------------------------------

const HELP = [
  "/tokens          session token bill so far",
  "/prove-recovery  compress one recorded tool output and recover it byte-exactly",
  "/mode            show whether this session is optimized or observe-only",
  "/help            this list",
  "/exit            end the session and print the final bill",
].join("\n");

export interface CodingSessionRunOptions extends CodingSessionOptions {
  agent?: CodingAgent;
  agentOptions?: CodingAgentOptions;
  input?: Readable;
  output?: Writable;
  notice?: Writable;
  /** Test and demo seam: per-turn run option overrides, e.g. a faux model. */
  runOverrides?: RunOptions;
}

/** Interactive coding session over stdin/stdout. Returns the final bill. */
export async function runCodingSession(
  options: CodingSessionRunOptions = {},
): Promise<SessionBill> {
  // Fail before a session (and its runtime probe) is started rather than on the
  // first turn: forged build identity, plan, or routing is never a live option.
  if (options.runOverrides !== undefined) rejectInternalRunOptions(options.runOverrides);
  const out = options.output ?? process.stdout;
  const notice = options.notice ?? process.stderr;
  const write = (value: string) => out.write(`${value}\n`);
  const codingAgent = options.agent ?? createCodingAgent(options.agentOptions ?? {});
  const sessionOptions: CodingSessionOptions = {
    ...(options.conversation === undefined ? {} : { conversation: options.conversation }),
    ...(options.cave === undefined ? {} : { cave: options.cave }),
    ...(options.gatewayURL === undefined ? {} : { gatewayURL: options.gatewayURL }),
    ...(options.engineBin === undefined ? {} : { engineBin: options.engineBin }),
    ...(options.workflow === undefined ? {} : { workflow: options.workflow }),
    ...(options.maxCostUsd === undefined ? {} : { maxCostUsd: options.maxCostUsd }),
    ...(options.fetch === undefined ? {} : { fetch: options.fetch }),
    ...(options.ensureRuntime === undefined ? {} : { ensureRuntime: options.ensureRuntime }),
    onNotice: (line) => {
      notice.write(`${line}\n`);
      options.onNotice?.(line);
    },
  };
  const session = await startCodingSession(codingAgent, sessionOptions);
  write(`caveman-code · ${codingAgent.modelID} · workspace ${codingAgent.workspace}`);
  write(`mode: ${session.mode}${session.mode === "optimized"
    ? " — reversible context transforms on (history + tool results)"
    : ""}`);
  write(HELP);
  const rl = createInterface({
    input: options.input ?? process.stdin,
    output: out,
  });
  // The async iterator applies backpressure per line, so a piped script and an
  // interactive terminal behave the same and end-of-input ends the session.
  const prompt = () => out.write(`\nagent [${session.mode}] > `);
  try {
    prompt();
    for await (const raw of rl) {
      const line = raw.trim();
      if (line === "/exit" || line === "/quit") break;
      if (line === "") {
        prompt();
        continue;
      }
      if (line === "/help") {
        write(HELP);
      } else if (line === "/mode") {
        write(`mode: ${session.mode}`);
      } else if (line === "/tokens") {
        for (const value of formatSessionBill(sessionBill(session))) write(value);
      } else if (line === "/prove-recovery") {
        await showProof(session, write);
      } else {
        try {
          const turn = await runCodingTurn(session, line, options.runOverrides ?? {});
          write("");
          write(turn.text);
          write("");
          const saved = sessionBill(session).tokensSavedInferred;
          for (const value of formatTurnBill(turn.bill, saved)) write(value);
          if (!session.proofShown && turn.bill.transformIDs.length > 0) {
            await showProof(session, write);
          }
        } catch (error) {
          write(`error: ${error instanceof Error ? error.message : String(error)}`);
        }
      }
      prompt();
    }
  } finally {
    rl.close();
  }
  const final = sessionBill(session);
  write("");
  for (const value of formatSessionBill(final)) write(value);
  return final;
}

async function showProof(
  session: CodingSession,
  write: (value: string) => void,
): Promise<void> {
  try {
    write(formatRecoveryProof(await proveRecovery(session)));
    session.proofShown = true;
  } catch (error) {
    write(`recovery proof unavailable: ${error instanceof Error ? error.message : String(error)}`);
  }
}
