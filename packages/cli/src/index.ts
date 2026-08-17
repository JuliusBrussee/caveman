#!/usr/bin/env node
import { execFileSync, spawn, spawnSync } from "node:child_process";
import {
  accessSync,
  appendFileSync,
  cpSync,
  existsSync,
  chmodSync,
  constants,
  copyFileSync,
  mkdirSync,
  mkdtempSync,
  lstatSync,
  readdirSync,
  readFileSync,
  realpathSync,
  renameSync,
  rmSync,
  statSync,
  symlinkSync,
  unlinkSync,
  writeFileSync,
} from "node:fs";
import { chmod, mkdir, open, readFile, rename, writeFile } from "node:fs/promises";
import { homedir, hostname, tmpdir } from "node:os";
import { basename, delimiter, dirname, extname, isAbsolute, join, resolve } from "node:path";
import { connect as netConnect, createServer as netCreateServer, isIP, type AddressInfo } from "node:net";
import { request as httpRequest } from "node:http";
import { request as httpsRequest } from "node:https";
import { createHash, createHmac, createPublicKey, randomBytes, randomUUID, verify as edVerify, type KeyObject } from "node:crypto";
import { fileURLToPath } from "node:url";
import { PROFILES, type AgentProfile } from "./agents.generated.js";
import {
  BINARY_RELEASE,
  BINARY_RELEASE_BASE_DEFAULT,
  BINARY_SIGNING_PUBKEY,
} from "./binaries.generated.js";
import { RECIPES, type IntegrationRecipe } from "./recipes.generated.js";
import { PRACTICE_REGISTRY } from "./practices.generated.js";
import { RESERVED_VERBS } from "./reserved-verbs.generated.js";
import { VERIFIED_SAVINGS_METHODS } from "./verified-methods.mirror.js";
import {
  AGENT_SKILLS,
  AGENT_SKILL_METADATA,
  AGENT_SKILL_SUITES,
} from "./agent-skills.generated.js";
import { NATIVE_CORE, NATIVE_PACK, NATIVE_SKILL_INSTRUCTIONS } from "./native-pack.generated.js";
import {
  serveAgentMcp,
  type AgentMcpClient,
  type JSONObject,
  type JSONValue,
} from "./agent-mcp.js";
import { portableInvocation } from "./portable-command.js";

type TokenStore = "keychain" | "file";
type TelemetryConfig = { enabled: boolean; anonymousId?: string; decidedAt: string; promptVersion: number };
// The high-water mark of proxy token totals already reported, so each event
// carries a delta instead of replaying lifetime history on every command.
type TelemetryTokenWatermark = { tokensIn: number; tokensSaved: number; at: string };
type StoredCredentials = {
  access_token: string;
  refresh_token?: string;
  gateway_api_key?: string;
  gateway_key_id?: string;
  project_id?: string;
};
type Config = {
  baseURL: string;
  token: string;
  refreshToken?: string;
  gatewayApiKey?: string;
  gatewayKeyId?: string;
  projectId?: string;
  organizationId?: string;
  tokenStore?: TokenStore;
  gatewayUrl?: string;
  logoutPendingLocalCleanup?: boolean;
  telemetry?: TelemetryConfig;
  telemetryTokens?: TelemetryTokenWatermark;
};
type WrapMode = "local" | "managed";
export type OverlayBuilderContext = { mode: WrapMode; gatewayUrl: string; env: NodeJS.ProcessEnv };
export const overlayBuilders: Record<string, (agent: AgentProfile, baseConfig: unknown, ctx: OverlayBuilderContext) => unknown> = {};
const wrapTempDirs = new Set<string>();

// The standalone proxy listens here; `caveman start` launches it and
// `caveman wrap` points agents at it.
const PROXY_ADDR = "127.0.0.1:8787";
const PROXY_URL = `http://${PROXY_ADDR}`;

// Gateway resolution is dynamic — resolved per invocation, never frozen at module
// load — so `caveman login` persisting a managed gateway URL flips `wrap` to the
// cloud with no env var (SIMPLICITY_SPEC §6.5, audit finding #3). `caveman start`
// always owns the standalone local listener. Precedence:
// explicit CAVE_GATEWAY_URL env > the managed URL persisted by login > local proxy.
function gatewayURL(): string {
  return process.env.CAVE_GATEWAY_URL ?? (gatewayUrlFromConfigFile() || PROXY_URL);
}

// gatewayUrlFromConfigFile reads the persisted managed gateway URL straight from
// config.json (a cheap sync read, like orgIdFromConfigFile) so gateway resolution
// stays dynamic on the hot wrap path. Empty when logged out or local-only.
function gatewayUrlFromConfigFile(): string {
  try {
    const parsed = JSON.parse(readFileSync(configPath(), "utf8")) as { gatewayUrl?: unknown };
    return typeof parsed.gatewayUrl === "string" ? parsed.gatewayUrl : "";
  } catch {
    return "";
  }
}

// Known agents `caveman wrap` can launch by short id come from the agent-profile
// registry (public/agents/profiles/*.json, compiled to agents.generated.ts). The
// data drives detection, the interactive picker, the install hint, and — crucially
// — HOW each agent is pointed at the gateway (env vs inline-config injection). So
// adding an agent is a new profile file, not a code change. Any other command
// still runs verbatim with the generic provider base-URL injection.
const AGENTS: AgentProfile[] = PROFILES;

// binOf is the primary binary name to resolve on PATH for an agent profile.
function binOf(a: AgentProfile): string {
  return a.binary_names[0] ?? a.id;
}

// findAgent resolves a wrap target to a profile by id first, then by any of its
// binary_names — so `caveman wrap opencode` and a renamed binary both match.
function findAgent(requested: string): AgentProfile | undefined {
  return AGENTS.find((a) => a.id === requested || a.binary_names.includes(requested));
}

type CommandGroup = "tools" | "cloud";
type CommandHandler = (argv: string[]) => unknown | Promise<unknown>;
type ResolvedInvocation = {
  verb: string;
  argv: string[];
  group?: CommandGroup;
  handler: CommandHandler;
  agent?: AgentProfile;
};

type DiscoveryGroup = {
  heading: string;
  verbs: { verb: string; description: string; advanced?: boolean; hidden?: boolean }[];
};

const TOOL_DISCOVERY: DiscoveryGroup[] = [
  { heading: "think", verbs: [
    { verb: "compress", description: "compress stdin; use `compress catalog` for tool schemas" },
    { verb: "shrink", description: "run a command and keep output recoverable" },
    { verb: "shrink-hook", description: "internal command-output hook", hidden: true },
    { verb: "toon", description: "convert JSON to or from compact TOON" },
    { verb: "convert", description: "pack installed agent skills into pixels" },
  ] },
  { heading: "remember", verbs: [
    { verb: "mem", description: "remember, recall, or forget durable context" },
    { verb: "retrieve", description: "recover byte-exact compressed content" },
  ] },
  { heading: "execute", verbs: [
    { verb: "mcp", description: "add or remove agent recovery tools" },
    { verb: "hooks", description: "install or remove agent hooks" },
    { verb: "browse", description: "browse through compressed browser tools" },
    { verb: "skills", description: "install agent skills, optionally as pixels" },
    { verb: "practices", description: "render a practice as SKILL.md", hidden: true },
    { verb: "sdk", description: "print copy-ready SDK recipes" },
  ] },
  { heading: "inspect", verbs: [
    { verb: "stats", description: "inspect local proxy measurements" },
    { verb: "trial", description: "measure one local before/after run" },
    { verb: "evals", description: "run local quality gates" },
    { verb: "config", description: "inspect or change what Caveman does" },
    { verb: "check", description: "verify Cave Build lock before model spend", hidden: true },
  ] },
];

const CLOUD_DISCOVERY: DiscoveryGroup[] = [
  { heading: "account", verbs: [
    { verb: "whoami", description: "show connected identity" },
    { verb: "projects", description: "list or create projects" },
    { verb: "keys", description: "create or revoke project keys" },
    { verb: "providers", description: "list or verify providers" },
    { verb: "billing", description: "inspect billing and verified savings" },
  ] },
  { heading: "evidence", verbs: [
    { verb: "score", description: "show scoped Cave Score" },
    { verb: "costs", description: "show provider-complete cost totals" },
    { verb: "plan", description: "show ranked inferred Cave Plan" },
    { verb: "traces", description: "search or export request metadata" },
    { verb: "experiments", description: "inspect or manage eval-gated experiments" },
    { verb: "receipts", description: "verify or export signed receipts" },
  ] },
  { heading: "governance", verbs: [
    { verb: "audit", description: "import or report audit evidence" },
    { verb: "sync", description: "sync local metadata to connected org" },
    { verb: "agent", description: "inspect proposal-only optimization PRs" },
  ] },
];

const TOOL_VERBS = new Set(TOOL_DISCOVERY.flatMap((group) => group.verbs.map((entry) => entry.verb)));
const CLOUD_VERBS = new Set(CLOUD_DISCOVERY.flatMap((group) => group.verbs.map((entry) => entry.verb)));

const TOOL_HANDLERS: Record<string, CommandHandler> = {
  compress,
  shrink,
  "shrink-hook": () => shrinkHook(),
  toon: toonConvert,
  convert,
  mem,
  retrieve,
  mcp: (argv) => {
    if (argv[0] === "install") {
      const target = positionalAfterOptions(argv.slice(1), new Set(["--server"]));
      return mcpInstall(target, flagFrom(argv, "--server", "caveman"));
    }
    if (argv[0] === "uninstall") {
      const target = positionalAfterOptions(argv.slice(1), new Set(["--server"]));
      return mcpUninstall(target, flagFrom(argv, "--server", "caveman"));
    }
    return mcpUsage();
  },
  hooks: hooksCmd,
  browse,
  skills,
  practices: practicesCommand,
  sdk: (argv) => {
    if (argv[0] === "snippet" || argv[0] === "snippets") {
      return argv.length > 1 ? snippets(argv.slice(1)) : sdkSnippet();
    }
    return sdkSnippet();
  },
  stats: (argv) => stats(argv),
  trial,
  evals: (argv) => argv[0] === "run" ? evalsRun(argv.slice(1)) : commandUsage("evals run [--fixtures <dir>]"),
  config: capabilityConfigCommand,
  check: agentBuildCheck,
};

function agentBuildCheck(argv: string[]): void {
  const bin = process.env.CAVEMAN_AGENT_BIN || which("caveman-agent");
  if (!bin) {
    console.error("caveman tools check: caveman-agent is not installed; install @caveman-ai/agent in this project");
    process.exitCode = 1;
    return;
  }
  const invocation = portableInvocation(bin, ["check", ...argv]);
  const result = spawnSync(invocation.command, invocation.args, {
    cwd: process.cwd(),
    env: process.env,
    stdio: "inherit",
  });
  if (result.error) {
    console.error(`caveman tools check: ${result.error.message}`);
    process.exitCode = 1;
    return;
  }
  process.exitCode = result.status ?? 1;
}

const CLOUD_HANDLERS: Record<string, CommandHandler> = {
  whoami: () => get("/api/v1/auth/me").then(print),
  projects: (argv) => {
    if (argv[0] === "list") return get("/api/v1/projects").then(print);
    if (argv[0] === "create") {
      return post("/api/v1/projects", {
        name: flagFrom(argv, "--name", "CLI Project"),
        slug: flagFrom(argv, "--slug", "cli-project"),
      }).then(print);
    }
    return commandUsage("projects list|create");
  },
  keys: async (argv) => {
    if (argv[0] === "create") return createKey(argv);
    if (argv[0] === "revoke") return post(`/api/v1/projects/${await projectId()}/keys/${argv[1] ?? ""}/revoke`, {}).then(print);
    return commandUsage("keys create|revoke <id>");
  },
  providers: async (argv) => {
    if (argv[0] === "list") return get(`/api/v1/projects/${await projectId()}/providers`).then(print);
    if (argv[0] === "verify") return post(`/api/v1/projects/${await projectId()}/providers/${argv[1] ?? ""}/verify`, {}).then(print);
    return commandUsage("providers list|verify <id>");
  },
  billing: (argv) => {
    if (argv[0] === "status") return billingStatus(argv);
    if (argv[0] === "charges") return billingCharges(argv);
    return commandUsage("billing status|charges");
  },
  score: () => get("/api/v1/reports/cave-score").then(print),
  costs: () => get("/api/v1/reports/costs").then(print),
  plan,
  traces: traceCommand,
  experiments: experimentCommand,
  "mcp-serve": () => serveCloudAgentMcp(),
  receipts: (argv) => {
    if (argv[0] === "verify") return receiptsVerify(argv);
    if (argv[0] === "export") return receiptsExport(argv);
    return commandUsage("receipts verify <bundle.json>|export");
  },
  audit,
  sync: () => sync(),
  agent: (argv) => {
    if (argv[0] === "list") return get("/api/v1/optimization-proposals").then(print);
    if (argv[0] === "show") return get(`/api/v1/optimization-proposals/${argv[1] ?? ""}`).then(print);
    if (argv[0] === "run") return post(`/api/v1/optimization-proposals/${argv[1] ?? ""}/run`, {}).then(print);
    return commandUsage("agent list|show <id>|run <id>");
  },
};

const LEGACY_HANDLERS: Record<string, CommandHandler> = {
  help,
  telemetry: telemetryCmd,
  // Unprinted (porcelain caps): replays the first-run 30-day reveal on demand.
  welcome: () => firstRunExperience({ forced: true }),
  login,
  logout: () => logout(),
  init,
  doctor: (argv) => argv[0] ? nativeDoctor(argv) : doctor(),
  enable: (argv) => enableNative(argv),
  disable: (argv) => disableNative(argv),
  inspect: (argv) => nativeInspect(argv),
  why: (argv) => nativeWhy(argv),
  // Host lifecycle callback. Kept outside porcelain/discovery: agents invoke it,
  // humans should not need to.
  "native-hook": (argv) => nativeHook(argv),
  setup: (argv) => setup(argv),
  opportunities: (argv) => argv[0] === "list" ? get("/api/v1/opportunities").then(print) : commandUsage("opportunities list"),
  snippets,
  dev: (argv) => {
    if (argv[0] === "up") return shellHint("make dev");
    if (argv[0] === "down") return shellHint("make down");
    if (argv[0] === "reset") return shellHint("make reset-local");
    return commandUsage("dev up|down|reset");
  },
  deploy: (argv) => {
    if (argv[0] === "aws") return shellHint("make deploy-aws");
    if (argv[0] === "status") return get("/api/v1/system/status").then(print);
    return commandUsage("deploy aws|status");
  },
  start: (argv) => start(argv),
  wrap,
  run: wrap,
  status,
  explore,
  verify: verifyFirstRequest,
  trial,
  usage,
  learn,
  version: () => print({ version: cliVersion(), binary_release: BINARY_RELEASE }),
};

for (const [verb, handler] of Object.entries(TOOL_HANDLERS)) {
  // Practice rendering is intentionally tools-only: no new top-level porcelain.
  if (verb !== "practices") LEGACY_HANDLERS[verb] = handler;
}
for (const [verb, handler] of Object.entries(CLOUD_HANDLERS)) LEGACY_HANDLERS[verb] = handler;

function commandUsage(suffix: string): never {
  const prefix = currentInvocation?.group ? `${invokedAs()} ${currentInvocation.group}` : invokedAs();
  console.error(`usage: ${prefix} ${suffix}`);
  process.exit(2);
}

function printDiscovery(group: CommandGroup, all = false): void {
  const groups = group === "tools" ? TOOL_DISCOVERY : CLOUD_DISCOVERY;
  console.log(`${invokedAs()} ${group} · ${group === "tools" ? "local, no account" : "connected, login required"}`);
  for (const section of groups) {
    const entries = section.verbs.filter((entry) => !entry.hidden && (all || !entry.advanced));
    if (entries.length === 0) continue;
    console.log(`\n${section.heading}`);
    for (const entry of entries) console.log(`  ${entry.verb.padEnd(13)} ${entry.description}`);
  }
  if (group === "cloud") console.log(`\nstart: ${invokedAs()} login`);
}

function resolveInvocation(raw: string[]): ResolvedInvocation {
  const top = raw[0] ?? "help";
  if (top === "--help") return { verb: "help", argv: [], handler: help };
  if ((top === "tools" || top === "cloud") && raw.length === 1) {
    return { verb: top, argv: [], group: top, handler: () => printDiscovery(top) };
  }
  if (top === "help" && (raw[1] === "tools" || raw[1] === "cloud")) {
    const group = raw[1];
    const all = raw.includes("--all");
    return { verb: "help", argv: raw.slice(1), handler: () => printDiscovery(group, all) };
  }
  if (top === "tools" || top === "cloud") {
    const group = top;
    const verb = raw[1] ?? "";
    if (verb === "--help" || verb === "-h") {
      return { verb: "help", argv: raw.slice(1), group, handler: () => printDiscovery(group, raw.includes("--all")) };
    }
    const handlers = group === "tools" ? TOOL_HANDLERS : CLOUD_HANDLERS;
    const handler = handlers[verb];
    if (handler) return { verb, argv: raw.slice(2), group, handler };
    return { verb, argv: raw.slice(2), group, handler: () => unknownInvocation(verb, group) };
  }
  const handler = LEGACY_HANDLERS[top];
  if (handler) return { verb: top, argv: raw.slice(1), handler };
  const agent = RESERVED_VERBS.has(top) ? undefined : findAgent(top);
  if (agent) return {
    verb: "run",
    argv: normalizeAgentShortcutWrapArgs(raw),
    handler: agentShortcut,
    agent,
  };
  return { verb: top, argv: raw.slice(1), handler: () => unknownInvocation(top) };
}

function editDistance(left: string, right: string): number {
  const row = Array.from({ length: right.length + 1 }, (_, index) => index);
  for (let i = 1; i <= left.length; i++) {
    let previous = row[0]!;
    row[0] = i;
    for (let j = 1; j <= right.length; j++) {
      const old = row[j]!;
      row[j] = Math.min(row[j]! + 1, row[j - 1]! + 1, previous + (left[i - 1] === right[j - 1] ? 0 : 1));
      previous = old;
    }
  }
  return row[right.length]!;
}

function suggestedCommand(token: string): string {
  const candidates = [
    ...["run", "learn", "login", "status"],
    ...[...TOOL_VERBS].map((verb) => `tools ${verb}`),
    ...[...CLOUD_VERBS].map((verb) => `cloud ${verb}`),
    ...Object.keys(LEGACY_HANDLERS),
    ...AGENTS.map((agent) => agent.id),
  ];
  return candidates.sort((a, b) => editDistance(token, a.split(" ").at(-1)!) - editDistance(token, b.split(" ").at(-1)!))[0] ?? "run";
}

function unknownInvocation(token: string, group?: CommandGroup): never {
  if (!group && (commandHasPath(token) || token.startsWith(".") || which(token))) {
    console.error(`not a known agent — use \`${invokedAs()} run -- <cmd>\``);
  } else {
    const suggestion = suggestedCommand(token);
    console.error(`unknown command "${token}" — did you mean \`${invokedAs()} ${suggestion}\`?`);
    console.error(`see: ${invokedAs()} --help`);
  }
  process.exit(2);
}

function invokedAs(): "cave" | "caveman" {
  const name = basename(process.argv[1] ?? "");
  return name === "cave" ? "cave" : "caveman";
}

function invokedCommand(legacyVerb: string, groupedTail = ""): string {
  if (currentInvocation?.group) {
    return `${invokedAs()} ${currentInvocation.group} ${currentInvocation.verb}${groupedTail}`;
  }
  return `${invokedAs()} ${legacyVerb}`;
}

let currentInvocation: ResolvedInvocation;
currentInvocation = resolveInvocation(process.argv.slice(2));
// Version 4 = default-on (opt-out) plus token volume: command_run now carries
// the local proxy's processed/saved token deltas, so a v3 "yes" was given for a
// narrower scope and gets the new disclosure reprinted once (never re-asked, and
// never flipped on). A persisted decision from any version — including a "no" to
// the old v1 [y/N] prompt — is honored forever; the default only fills the
// undecided gap, and the first default-on run prints the disclosure line.
const TELEMETRY_PROMPT_VERSION = 4;
const TELEMETRY_URL = "https://api.caveman.so/telemetry/cli";
// The production control-API origin — derived from TELEMETRY_URL (the CLI's
// other hardcoded prod-host literal) so the two can never drift apart.
const PROD_API_URL = new URL(TELEMETRY_URL).origin;
const TELEMETRY_DISCLOSURE_LINE =
  "anonymous usage stats on — command counts and token totals only, never prompts, code, or file paths · caveman telemetry off";
// Reading token totals means spawning caveman-proxy to query the local SQLite
// store. It runs after the command's own work, so the cost lands on process exit;
// a slow or wedged binary drops the token fields rather than holding the CLI.
const TELEMETRY_TOKEN_READ_TIMEOUT_MS = 400;
const SYNC_DISCLOSURE =
  "sync uploads span metadata to your org's dashboard — tokens, cost, latency, model, status. Imported standalone observations never affect managed budgets, verified savings, or billing. Subscription/OAuth sessions carry token counts only — no dollar figure. Never prompt or response bytes.";
const TELEMETRY_COMMAND_ALLOWLIST = [
  "agent", "audit", "billing", "browse", "compress", "convert", "costs", "deploy", "dev", "doctor", "evals", "experiments",
  "explore", "help", "hooks", "init", "keys", "learn", "login", "logout", "mcp", "mem", "opportunities", "plan",
  "projects", "providers", "receipts", "retrieve", "score", "sdk", "setup", "shrink", "shrink-hook", "skills",
  "run", "snippets", "start", "stats", "status", "sync", "telemetry", "toon", "traces", "trial", "unknown", "usage", "verify", "version", "welcome", "whoami", "wrap",
] as const;
const TELEMETRY_COMMANDS = new Set<string>(TELEMETRY_COMMAND_ALLOWLIST);
const TELEMETRY_SUBCOMMANDS = new Set([
  "act", "apply", "aws", "charges", "create", "decode", "down", "encode", "eval", "export", "forget", "import",
  "catalog", "implement", "install", "link", "list", "off", "on", "recall", "recover", "refresh", "remember", "report", "reset", "revoke",
  "run", "show", "snippet", "status", "uninstall", "unlink", "up", "verify",
]);
const TELEMETRY_START_MS = Date.now();
let telemetryCommandSent = false;
let telemetryEphemeralId = "";

async function main() {
  await ensureTelemetryDefault();
  try {
    await dispatch();
    emitCommandRunOnce("ok");
  } catch (error) {
    emitCommandRunOnce("error", classifyTelemetryError(error));
    throw error;
  }
}

async function dispatch() {
  return currentInvocation.handler(currentInvocation.argv);
}

type TelemetryState = "on" | "off";
type TelemetrySource = "env" | "config" | "runtime" | "default";
type TelemetryRuntimeState = { state: TelemetryState; source: TelemetrySource; config: TelemetryConfig | undefined };
type TelemetryExitClass = "ok" | "error";
type TelemetryErrorClass = "network" | "auth" | "usage" | "unknown_command" | "other";

function telemetryState(): TelemetryRuntimeState {
  const dnt = process.env.DO_NOT_TRACK;
  if (dnt !== undefined && dnt !== "" && dnt !== "0") return { state: "off", source: "env", config: telemetryConfigFromDisk() };

  const env = process.env.CAVEMAN_TELEMETRY;
  if (env !== undefined && env !== "") {
    const v = env.trim().toLowerCase();
    if (v === "1" || v === "true" || v === "on") return { state: "on", source: "env", config: telemetryConfigFromDisk() };
    return { state: "off", source: "env", config: telemetryConfigFromDisk() };
  }

  if (envTruthy(process.env.CI) || !interactive()) return { state: "off", source: "runtime", config: telemetryConfigFromDisk() };

  const cfg = telemetryConfigFromDisk();
  if (cfg?.decidedAt) return { state: cfg.enabled ? "on" : "off", source: "config", config: cfg };
  // No decision on disk: default ON (opt-out); only reachable interactively —
  // the CI/non-TTY branch above already returned off. NOTHING may send while
  // source is still "default": telemetrySendable gates every emitter, so the
  // first real send always follows ensureTelemetryDefault's persist (stable
  // anonymous id) + printed disclosure.
  return { state: "on", source: "default", config: undefined };
}

// telemetrySendable is the single choke point every emitter must pass: on, and
// never the un-persisted default (no silent sends, no ephemeral-id retention
// noise from commands that skipped the disclosure).
function telemetrySendable(state: TelemetryRuntimeState): boolean {
  return state.state === "on" && state.source !== "default";
}

function envTruthy(v: string | undefined): boolean {
  return v !== undefined && v !== "" && v !== "0" && v.toLowerCase() !== "false";
}

function telemetryEnvForcesOff(): boolean {
  const dnt = process.env.DO_NOT_TRACK;
  if (dnt !== undefined && dnt !== "" && dnt !== "0") return true;
  const env = process.env.CAVEMAN_TELEMETRY;
  if (env === undefined || env === "") return false;
  const v = env.trim().toLowerCase();
  return !(v === "1" || v === "true" || v === "on");
}

function telemetryConfigFromDisk(): TelemetryConfig | undefined {
  try {
    const parsed = JSON.parse(readFileSync(configPath(), "utf8")) as { telemetry?: unknown };
    return parseTelemetryConfig(parsed.telemetry);
  } catch {
    return undefined;
  }
}

function parseTelemetryConfig(value: unknown): TelemetryConfig | undefined {
  if (!value || typeof value !== "object" || Array.isArray(value)) return undefined;
  const raw = value as Record<string, unknown>;
  if (typeof raw.enabled !== "boolean" || typeof raw.decidedAt !== "string" || typeof raw.promptVersion !== "number") {
    return undefined;
  }
  const out: TelemetryConfig = { enabled: raw.enabled, decidedAt: raw.decidedAt, promptVersion: raw.promptVersion };
  if (typeof raw.anonymousId === "string" && raw.anonymousId) out.anonymousId = raw.anonymousId;
  return out;
}

// ensureTelemetryDefault persists the default-on decision (with a stable
// anonymous id — retention counting is useless on ephemeral ids) the first time
// a real command runs interactively, and prints the one-line disclosure so the
// default is never silent. Env kills (DO_NOT_TRACK / CAVEMAN_TELEMETRY=0) and
// any persisted decision — including a "no" to the old v1 prompt — win.
async function ensureTelemetryDefault() {
  const state = telemetryState();
  if (isHelpLikeInvocation()) return;
  // `caveman telemetry …` manages the decision explicitly — don't pre-mint an
  // "on" for someone whose first-ever command is `telemetry off`.
  if (currentInvocation.verb === "telemetry") return;
  if (state.source === "config") return ensureTelemetryDisclosureVersion(state);
  if (state.source !== "default") return;
  const telemetry: TelemetryConfig = {
    enabled: true,
    anonymousId: randomUUID(),
    decidedAt: new Date().toISOString(),
    promptVersion: TELEMETRY_PROMPT_VERSION,
  };
  // A failed persist must never block the command (read-only home, root-owned
  // config). Nothing sends this run either way: source stays "default" until a
  // persist succeeds, and telemetrySendable refuses un-persisted defaults.
  try {
    await saveTelemetryConfig(telemetry);
  } catch {
    return;
  }
  process.stderr.write(`${dim(TELEMETRY_DISCLOSURE_LINE)}\n`);
}

// ensureTelemetryDisclosureVersion reprints the disclosure once for someone who
// consented under older wording. v4 widened command_run with token volume, and a
// v3 "yes" was given for command counts alone — it stays a yes (re-asking would
// silently reset a decision the user already made), but it is never widened
// silently. Only reached with source "config", which already means interactive,
// not CI, and no env override in play. An opt-out is left completely untouched:
// no write, no line, no version bump.
async function ensureTelemetryDisclosureVersion(state: TelemetryRuntimeState) {
  const cfg = state.config;
  if (!cfg?.enabled || state.state !== "on") return;
  if (cfg.promptVersion >= TELEMETRY_PROMPT_VERSION) return;
  try {
    await saveTelemetryConfig({ ...cfg, promptVersion: TELEMETRY_PROMPT_VERSION });
  } catch {
    return;
  }
  process.stderr.write(`${dim(TELEMETRY_DISCLOSURE_LINE)}\n`);
}

function isHelpLikeInvocation(): boolean {
  return currentInvocation.verb === "help"
    || currentInvocation.verb === "version"
    || currentInvocation.argv[0] === "--help"
    || currentInvocation.argv[0] === "-h";
}

// promptYesNo is the one line-buffered [y/N] reader. Ctrl-D / closed stdin
// resolves as the default No, never hangs the CLI.
function promptYesNo(question: string): Promise<boolean> {
  process.stderr.write(`${question} `);
  return new Promise((resolve) => {
    let answer = "";
    const cleanup = () => {
      process.stdin.pause();
      process.stdin.removeListener("data", onData);
      process.stdin.removeListener("end", onEnd);
      process.stdin.removeListener("error", onEnd);
    };
    const onData = (chunk: Buffer | string) => {
      answer += String(chunk);
      if (answer.includes("\n") || answer.includes("\r")) {
        cleanup();
        resolve(answer.trim() === "y" || answer.trim() === "Y");
      }
    };
    const onEnd = () => {
      cleanup();
      resolve(false);
    };
    process.stdin.setEncoding("utf8");
    process.stdin.resume();
    process.stdin.on("data", onData);
    process.stdin.on("end", onEnd);
    process.stdin.on("error", onEnd);
  });
}

async function saveTelemetryConfig(telemetry: TelemetryConfig) {
  const raw = await readRawConfig();
  raw.telemetry = telemetry;
  await writeRawConfig(raw);
}

async function telemetryCmd(argv: string[]) {
  const sub = argv[0] ?? "status";
  if (sub === "status") return telemetryStatus();
  if (sub === "on") return telemetryOn();
  if (sub === "off") return telemetryOff();
  emitCommandRunOnce("error", "usage");
  console.error(`usage: ${invokedCommand("telemetry")} [status|on|off]`);
  process.exit(2);
}

function telemetryStatus() {
  const state = telemetryState();
  print({
    enabled: state.state === "on",
    state: state.state,
    source: state.source,
    anonymous_id: state.config?.anonymousId ?? "none",
  });
}

async function telemetryOn() {
  const prior = telemetryConfigFromDisk();
  const anonymousId = prior?.enabled && prior.anonymousId ? prior.anonymousId : randomUUID();
  const telemetry: TelemetryConfig = {
    enabled: true,
    anonymousId,
    decidedAt: new Date().toISOString(),
    promptVersion: TELEMETRY_PROMPT_VERSION,
  };
  await saveTelemetryConfig(telemetry);
  if (!(prior?.enabled && prior.anonymousId) && !telemetryEnvForcesOff()) emitConsentGranted(anonymousId);
  if (telemetryEnvForcesOff()) {
    print({ telemetry: "on", anonymous_id: anonymousId, note: "env override active (DO_NOT_TRACK/CAVEMAN_TELEMETRY) — nothing is sent until it is unset" });
    return;
  }
  print({ telemetry: "on", anonymous_id: anonymousId });
}

async function telemetryOff() {
  const telemetry: TelemetryConfig = {
    enabled: false,
    decidedAt: new Date().toISOString(),
    promptVersion: TELEMETRY_PROMPT_VERSION,
  };
  await saveTelemetryConfig(telemetry);
  // Drop the token watermark with the decision. Keeping it would make a later
  // `telemetry on` report every token processed during the opt-out window as one
  // delta — the seeding rule has to hold across the off/on boundary too.
  try {
    mutateRawConfig((out) => {
      delete out.telemetryTokens;
    });
  } catch {
    /* best effort: the decision itself is already persisted */
  }
  print({ telemetry: "off", anonymous_id: "none" });
}

function telemetryCommandName(): string {
  if (currentInvocation.verb === "mcp-serve") return "mcp";
  return TELEMETRY_COMMANDS.has(currentInvocation.verb) ? currentInvocation.verb : "unknown";
}

function telemetrySubcommand(): string | undefined {
  if (currentInvocation.verb === "mcp-serve") return "run";
  if (currentInvocation.verb === "setup" && currentInvocation.argv.includes("--agent-native")) return "install";
  if (currentInvocation.verb === "telemetry" && !currentInvocation.argv[0]) return "status";
  const sub = currentInvocation.argv[0];
  return sub && TELEMETRY_SUBCOMMANDS.has(sub) ? sub : undefined;
}

function telemetryAgent(): string | undefined {
  if (currentInvocation.agent) return currentInvocation.agent.id;
  if (currentInvocation.verb === "setup") {
    const requested = flagFrom(currentInvocation.argv, "--agent-native", "");
    return requested ? findAgent(requested)?.id : undefined;
  }
  if (currentInvocation.verb !== "wrap" && currentInvocation.verb !== "run") return undefined;
  const target = telemetryWrapTarget(currentInvocation.argv);
  const agent = target ? findAgent(target) : undefined;
  return agent?.id;
}

function telemetryWrapTarget(rest: string[]): string | undefined {
  const flags = new Set(["--off", "--pixel"]);
  for (let i = 0; i < rest.length; i++) {
    const item = rest[i]!;
    if (item === "--") return rest[i + 1];
    if (flags.has(item)) continue;
    if (item.startsWith("-")) continue;
    return item;
  }
  return undefined;
}

function telemetryAnonymousId(state: TelemetryRuntimeState): string {
  if (state.config?.anonymousId) return state.config.anonymousId;
  if (!telemetryEphemeralId) telemetryEphemeralId = randomUUID();
  return telemetryEphemeralId;
}

// parseProxyStatsPayload pulls the stats object out of caveman-proxy's stdout.
// The binary points its JSON slog handler at stdout too, so a log line can land
// ahead of the payload and a plain JSON.parse of the whole stream would throw.
// Every candidate must carry a numeric tokens_in: without that check a stray log
// object parses "successfully" as zero tokens, which reads as a rewound store and
// replays the entire lifetime total as one delta.
function parseProxyStatsPayload(out: string): Record<string, unknown> | null {
  const attempt = (text: string): Record<string, unknown> | null => {
    try {
      const parsed = JSON.parse(text) as unknown;
      if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) return null;
      const raw = parsed as Record<string, unknown>;
      return typeof raw.tokens_in === "number" ? raw : null;
    } catch {
      return null;
    }
  };
  const trimmed = out.trim();
  if (!trimmed) return null;
  const whole = attempt(trimmed);
  if (whole) return whole;
  // Log lines can land on either side of the payload (a deferred Close() error
  // prints after it), so scan candidate object bounds from the end rather than
  // assuming the payload runs to EOF. Bounded: this output is a handful of lines,
  // and the budget keeps a pathological one from costing real time.
  const lines = trimmed.split("\n");
  let budget = 64;
  for (let start = lines.length - 1; start >= 0 && budget > 0; start--) {
    if (!lines[start]!.startsWith("{")) continue;
    for (let end = lines.length; end > start && budget > 0; end--) {
      budget--;
      const candidate = attempt(lines.slice(start, end).join("\n"));
      if (candidate) return candidate;
    }
  }
  return null;
}

// readProxyTokenTotals reads the local proxy's lifetime token aggregate. Every
// failure path — no store yet, no proxy binary, slow spawn, unparsable output —
// returns null and the event simply ships without token fields. Numbers only:
// `caveman-proxy stats --json` is an aggregate over the requests table, never a
// row, a prompt, a model name, or a path.
function readProxyTokenTotals(): { tokensIn: number; tokensSaved: number; basis: string } | null {
  try {
    const db = process.env.CAVEMAN_DB || join(cavemanHome(), "caveman.db");
    if (!existsSync(db)) return null;
    const bin = resolveGoBin("caveman-proxy", "CAVEMAN_PROXY_BIN");
    if (!bin) return null;
    const out = execFileSync(bin, ["stats", "--json"], {
      encoding: "utf8",
      timeout: TELEMETRY_TOKEN_READ_TIMEOUT_MS,
      stdio: ["ignore", "pipe", "ignore"],
    });
    const parsed = parseProxyStatsPayload(out);
    if (!parsed) return null;
    const tokensIn = parsed.tokens_in as number;
    const tokensSaved = typeof parsed.compression_tokens_saved === "number" ? parsed.compression_tokens_saved : 0;
    if (!Number.isFinite(tokensIn) || !Number.isFinite(tokensSaved)) return null;
    // Basis rides along because these are tokenizer estimates, not billed counts;
    // the receiving side must never promote them to verified savings.
    const basis = typeof parsed.basis === "string" && parsed.basis ? parsed.basis : "inferred";
    return {
      tokensIn: Math.max(0, Math.trunc(tokensIn)),
      tokensSaved: Math.max(0, Math.trunc(tokensSaved)),
      basis,
    };
  } catch {
    return null;
  }
}

function parseTelemetryTokenWatermark(value: unknown): TelemetryTokenWatermark | null {
  if (!value || typeof value !== "object" || Array.isArray(value)) return null;
  const raw = value as Record<string, unknown>;
  if (typeof raw.tokensIn !== "number" || typeof raw.tokensSaved !== "number") return null;
  if (!Number.isFinite(raw.tokensIn) || !Number.isFinite(raw.tokensSaved)) return null;
  return {
    tokensIn: Math.max(0, Math.trunc(raw.tokensIn)),
    tokensSaved: Math.max(0, Math.trunc(raw.tokensSaved)),
    at: typeof raw.at === "string" ? raw.at : "",
  };
}

function telemetryTokenWatermarkFromDisk(): TelemetryTokenWatermark | null {
  try {
    const parsed = JSON.parse(readFileSync(configPath(), "utf8")) as { telemetryTokens?: unknown };
    return parseTelemetryTokenWatermark(parsed.telemetryTokens);
  } catch {
    return null;
  }
}

// telemetryTokenDelta returns what to report for THIS event and advances the
// watermark to the totals it read. The send is fire-and-forget, so a dropped POST
// loses that delta rather than replaying it — undercounting beats double-counting
// a savings number.
//
// Two cases report nothing and only re-baseline. The first read on a machine
// seeds the watermark: the store can already hold traffic from before the user
// saw this disclosure, and telemetry starts at consent rather than reaching
// backwards through history. A store that rewound (db deleted, restored backup)
// re-baselines for the same reason — never a negative, never a second copy of
// history.
function telemetryTokenDelta(): { processed: number; saved: number; basis: string } | null {
  const totals = readProxyTokenTotals();
  if (!totals) return null;
  const prior = telemetryTokenWatermarkFromDisk();
  const rewound = prior !== null && (totals.tokensIn < prior.tokensIn || totals.tokensSaved < prior.tokensSaved);
  const processed = prior && !rewound ? Math.max(0, totals.tokensIn - prior.tokensIn) : 0;
  const saved = prior && !rewound ? Math.max(0, totals.tokensSaved - prior.tokensSaved) : 0;
  const rebaselining = prior === null || rewound;
  if (processed === 0 && saved === 0 && !rebaselining) return null;
  // Claim the delta optimistically: two CLI processes exiting together would
  // otherwise read the same watermark and both report the same tokens, inflating
  // a savings figure. Whoever writes second sees the watermark already moved and
  // reports nothing.
  let claimed = true;
  try {
    mutateRawConfig((out) => {
      const current = parseTelemetryTokenWatermark(out.telemetryTokens);
      if (current?.tokensIn !== prior?.tokensIn || current?.tokensSaved !== prior?.tokensSaved) {
        claimed = false;
        return;
      }
      out.telemetryTokens = {
        tokensIn: totals.tokensIn,
        tokensSaved: totals.tokensSaved,
        at: new Date().toISOString(),
      } satisfies TelemetryTokenWatermark;
    });
  } catch {
    // Read-only home: report nothing rather than resend the same delta forever.
    return null;
  }
  if (!claimed) return null;
  if (processed === 0 && saved === 0) return null;
  return { processed, saved, basis: totals.basis };
}

function emitCommandRunOnce(exitClass: TelemetryExitClass, errorClass?: TelemetryErrorClass): Promise<void> {
  const event = commandRunEventOnce(exitClass, errorClass);
  return event ? emitTelemetryEvents([event]) : Promise.resolve();
}

function commandRunEventOnce(exitClass: TelemetryExitClass, errorClass?: TelemetryErrorClass): Record<string, unknown> | null {
  if (telemetryCommandSent) return null;
  const state = telemetryState();
  if (!telemetrySendable(state)) return null;
  telemetryCommandSent = true;
  const event: Record<string, unknown> = {
    schema: "cli/v1",
    anonymous_id: telemetryAnonymousId(state),
    event: "command_run",
    command: telemetryCommandName(),
    cli_version: cliVersion(),
    os: process.platform,
    arch: process.arch,
    node_major: Number(process.versions.node.split(".")[0] ?? 0),
    duration_ms: Math.max(0, Date.now() - TELEMETRY_START_MS),
    exit_class: exitClass,
    ts: new Date().toISOString(),
  };
  const sub = telemetrySubcommand();
  const agent = telemetryAgent();
  if (sub) event.subcommand = sub;
  if (agent) event.agent = agent;
  if (exitClass === "error") event.error_class = errorClass ?? "other";
  // Token volume rides on command_run only — runtime_bootstrap shares the same
  // watermark and would race it into a double count.
  const tokens = telemetryTokenDelta();
  if (tokens) {
    event.tokens_processed = tokens.processed;
    event.tokens_saved = tokens.saved;
    event.tokens_basis = tokens.basis;
  }
  return event;
}

function emitConsentGranted(anonymousId: string) {
  emitTelemetryEvents([{
    schema: "cli/v1",
    anonymous_id: anonymousId,
    event: "consent_granted",
    cli_version: cliVersion(),
    os: process.platform,
    arch: process.arch,
    node_major: Number(process.versions.node.split(".")[0] ?? 0),
    ts: new Date().toISOString(),
  }]);
}

function emitRuntimeBootstrap(
  exitClass: TelemetryExitClass,
  durationMS: number,
  errorClass?: TelemetryErrorClass,
) {
  const state = telemetryState();
  if (!telemetrySendable(state)) return;
  const event: Record<string, unknown> = {
    schema: "cli/v1",
    anonymous_id: telemetryAnonymousId(state),
    event: "runtime_bootstrap",
    command: "wrap",
    subcommand: "install",
    cli_version: cliVersion(),
    os: process.platform,
    arch: process.arch,
    node_major: Number(process.versions.node.split(".")[0] ?? 0),
    duration_ms: Math.max(0, durationMS),
    exit_class: exitClass,
    ts: new Date().toISOString(),
  };
  const agent = telemetryAgent();
  if (agent) event.agent = agent;
  if (exitClass === "error") event.error_class = errorClass ?? "other";
  emitTelemetryEvents([event]);
}

function emitTelemetryEvents(events: Record<string, unknown>[]): Promise<void> {
  const url = process.env.CAVEMAN_TELEMETRY_URL || TELEMETRY_URL;
  return fetch(url, {
    method: "POST",
    headers: { "content-type": "application/json" },
    body: JSON.stringify(events),
    signal: AbortSignal.timeout(1500),
  }).then(() => {}).catch(() => {});
}

function classifyTelemetryError(error: unknown): TelemetryErrorClass {
  const message = error instanceof Error ? error.message : "";
  if (telemetryCommandName() === "unknown" || message.startsWith("unknown command:")) return "unknown_command";
  if (/not logged in|unauthorized|forbidden|auth/i.test(message)) return "auth";
  if (/fetch failed|ECONN|ENOTFOUND|ETIMEDOUT|network/i.test(message)) return "network";
  if (/^usage:/i.test(message) || /invalid|unknown .*flag/i.test(message)) return "usage";
  return "other";
}

// browse delegates browser interaction to the standalone Go binary. The CLI
// stays dependency-free and only translates the user-friendly command shape into
// caveman-browse's direct subcommands; MCP hosts should run caveman-browse
// directly as a stdio server.
async function browse(rest: string[]) {
  if (rest.length === 0 || rest[0] === "--help" || rest[0] === "-h") return browseUsage();
  const bin = cavemanBin("caveman-browse", "CAVEMAN_BROWSE_BIN");
  let browserArgs: string[];
  if (rest[0] === "act") {
    if (rest.length < 3) return browseUsage();
    const uid = rest[1]!;
    const action = rest[2]!;
    const text = rest.slice(3).join(" ");
    browserArgs = ["act", uid, action];
    if (text) browserArgs.push(text);
  } else if (rest[0] === "recover") {
    if (rest.length < 2) return browseUsage();
    browserArgs = ["recover", rest[1]!, ...rest.slice(2)];
  } else if (rest[0] === "eval") {
    if (rest.length < 2) return browseUsage();
    browserArgs = ["eval", rest.slice(1).join(" ")];
  } else if (rest[0] === "close") {
    browserArgs = ["close"];
  } else {
    browserArgs = ["snapshot", rest[0]!, ...rest.slice(1)];
  }
  await spawnBrowse(bin, browserArgs);
}

function browseUsage(): never {
  console.error(`usage: ${invokedCommand("browse")} <url> [query]`);
  console.error("       caveman browse act <uid> click|type|select|scroll [text]");
  console.error("       caveman browse recover <handle> [query]");
  console.error("       caveman browse eval <expression>");
  console.error("       caveman browse close");
  process.exit(2);
}

async function spawnBrowse(bin: string, browserArgs: string[]) {
  const code = await new Promise<number>((resolve, reject) => {
    const child = spawn(bin, browserArgs, { stdio: "inherit", env: process.env });
    child.on("error", (error) => reject(new Error(`failed to launch ${bin}: ${error.message} (set CAVEMAN_BROWSE_BIN or build caveman-browse)`)));
    child.on("exit", (code, signal) => resolve(code ?? signalExitCode(signal)));
  });
  process.exit(code);
}

function signalExitCode(signal: NodeJS.Signals | null): number {
  if (!signal) return 1;
  const signals: Partial<Record<NodeJS.Signals, number>> = { SIGHUP: 1, SIGINT: 2, SIGQUIT: 3, SIGTERM: 15 };
  return 128 + (signals[signal] ?? 1);
}

function exploreUsage(): never {
  console.error(`usage: ${invokedCommand("explore")} install [--agent claude] [--user] [--dir <path>]`);
  console.error("  legacy alias for tools skills install caveman-explore");
  console.error("  --agent claude   target Claude Code (default; codex is not wired yet)");
  console.error("  --user           install for all repos (~/.claude/skills) instead of this one");
  console.error("  --dir <path>     write SKILL.md into <path>");
  process.exit(2);
}

function guardExploreAgent(agent: string) {
  if (agent === "claude") return;
  console.error(`caveman explore: only --agent claude is wired today (codex needs verified transcript isolation first). got: ${agent}`);
  process.exit(2);
}

// Unprinted compatibility alias. Canonical surface is:
// caveman tools skills install caveman-explore.
async function explore(rest: string[]) {
  if (rest[0] !== "install") return exploreUsage();
  const agent = flagFrom(rest, "--agent", "claude");
  guardExploreAgent(agent);
  return skills(["install", "caveman-explore", ...rest.slice(1), "--no-pixel"]);
}

const SKILLS: Record<string, string> = AGENT_SKILLS;
const STUB_EST_TOKENS = 120;
const PIXEL_MARKER_PREFIX = "<!-- caveman-pixel v1 sha256:";
const SKILL_MD = "SKILL.md";
const SKILL_ORIG_MD = "SKILL.orig.md";

type PracticeRegistryEntry = (typeof PRACTICE_REGISTRY)[number];

function practicesUsage(): never {
  console.error(`usage: ${invokedAs()} tools practices render <practice_id>`);
  process.exit(2);
}

function renderPracticeSkill(practice: PracticeRegistryEntry): string {
  const harnesses = practice.predicate.harness.join(", ");
  const protocols = practice.predicate.wire_protocol.join(", ");
  return `---
name: ${practice.skill_render.skill_name}
description: >
  Apply ${practice.title} when ${practice.predicate.payload_shape}.
  Use only with an explicit before/after experiment and fail-closed grader.
---

# ${practice.title}

Evidence status: \`${practice.evidence.status}\`

${practice.evidence.status} — verified nowhere yet

## Applies when

- Harness: ${harnesses}
- Wire protocol: ${protocols}
- Payload shape: ${practice.predicate.payload_shape}

## Change

${practice.fix.prose}

Apply verb: \`${practice.fix.apply}\`

## Required gate

- Grader: \`${practice.grader.type}\`
- Fixture: ${practice.grader.fixture_template}
- Experiment: \`${practice.experiment.method}\`
- Metric: \`${practice.experiment.metric}\`

Run the experiment on this workload before promotion. A local result stays
\`observed\` or \`inferred\` according to evidence earned; this skill never
mints \`verified\` savings.

## Never claim

${practice.never_claim}
`;
}

function practicesCommand(rest: string[]) {
  if (rest[0] !== "render" || !rest[1] || rest.length !== 2) return practicesUsage();
  const id = rest[1];
  const practice = PRACTICE_REGISTRY.find((candidate) => candidate.id === id);
  if (!practice) {
    console.error(`caveman practices: unknown practice ${JSON.stringify(id)}`);
    process.exit(2);
  }
  if (!practice.skill_render.eligible) {
    console.error(
      `caveman practices: ${id} is not skill-renderable (skill_render.eligible=false; requires engine or proxy cooperation)`,
    );
    process.exit(2);
  }
  process.stdout.write(renderPracticeSkill(practice));
}

type SkillTarget = { name: string; dir: string; agent?: string };
type ConvertOptions = { dryRun: boolean; force: boolean; revert: boolean; engineBin: string | null; density: string | null };

// PIXEL_DENSITY_LEVELS is the closed set the engine accepts; the CLI validates
// --density/CAVE_PIXEL_DENSITY loudly (a dev surface) before forwarding it, so a
// typo fails here rather than silently falling back inside the engine.
const PIXEL_DENSITY_LEVELS = ["conservative", "balanced", "max"] as const;

// parsePixelDensity validates a density level; "" means "not set" (forward nothing,
// engine default applies). An invalid value exits non-zero with a clear message.
function parsePixelDensity(raw: string, flagName: string): string | null {
  const v = raw.trim().toLowerCase();
  if (v === "") return null;
  if (!(PIXEL_DENSITY_LEVELS as readonly string[]).includes(v)) {
    console.error(`caveman: ${flagName} must be one of ${PIXEL_DENSITY_LEVELS.join("|")} (got ${JSON.stringify(raw)})`);
    process.exit(2);
  }
  return v;
}
type SplitSkill = { frontmatter: string; bodyText: string; bodyBytes: Buffer };
type ConvertResult =
  | { kind: "converted"; name: string; dir: string; textEst: number; imageEst: number; afterEst: number; dryRun: boolean }
  | { kind: "skipped"; name: string; dir: string; reason: string }
  | { kind: "reverted"; name: string; dir: string };

function convertUsage(): never {
  console.error(`usage: ${invokedCommand("convert")} [--agent <id>] [--project] [--skill <name>] [--dir <path>] [--density conservative|balanced|max] [--revert] [--dry-run] [--force]`);
  process.exit(2);
}

async function convert(rest: string[]) {
  if (rest.includes("--help")) return convertUsage();
  const agentID = flagFrom(rest, "--agent", "");
  if (agentID) {
    const profile = PROFILES.find((p) => p.id === agentID);
    if (!profile) {
      console.log(`caveman convert: no agent profile for ${agentID}`);
      return;
    }
    if (!profile.skills) {
      console.log(`caveman convert: no skill surface for ${agentID}`);
      return;
    }
  }
  const targets = discoverSkillTargets(rest);
  const opts: ConvertOptions = {
    dryRun: rest.includes("--dry-run"),
    force: rest.includes("--force"),
    revert: rest.includes("--revert"),
    engineBin: rest.includes("--revert") ? null : resolveEngineBin(),
    density: parsePixelDensity(flagFrom(rest, "--density", ""), "--density"),
  };
  const results: ConvertResult[] = [];
  for (const target of targets) {
    try {
      results.push(convertSkillTarget(target, opts));
    } catch (e) {
      console.error(`caveman convert: cannot write ${target.dir}: ${(e as Error).message}`);
      process.exit(1);
    }
  }
  writeConvertReport(results);
}

function discoverSkillTargets(rest: string[]): SkillTarget[] {
  const skill = flagFrom(rest, "--skill", "");
  const directDir = flagFrom(rest, "--dir", "");
  if (directDir) return discoverTargetsInRoot(resolveSkillRoot(directDir), skill);

  const agentID = flagFrom(rest, "--agent", "");
  const profiles = agentID ? PROFILES.filter((p) => p.id === agentID) : PROFILES;
  if (agentID && profiles.length === 0) {
    console.log(`caveman convert: no agent profile for ${agentID}`);
    return [];
  }
  if (agentID && !profiles[0]?.skills) {
    console.log(`caveman convert: no skill surface for ${agentID}`);
    return [];
  }

  const targets: SkillTarget[] = [];
  for (const profile of profiles) {
    if (!profile.skills) continue;
    if (profile.skills.format !== "skill-md") {
      targets.push({ name: profile.id, dir: "", agent: profile.id });
      continue;
    }
    const roots = [...profile.skills.user_dirs.map(resolveSkillRoot)];
    if (rest.includes("--project")) roots.push(...(profile.skills.project_dirs ?? []).map(resolveSkillRoot));
    for (const root of roots) targets.push(...discoverTargetsInRoot(root, skill, profile.id));
  }
  const seen = new Set<string>();
  return targets.filter((target) => {
    if (!target.dir) return true;
    const key = `${target.agent ?? ""}:${target.dir}`;
    if (seen.has(key)) return false;
    seen.add(key);
    return true;
  });
}

function discoverTargetsInRoot(root: string, skill: string, agent?: string): SkillTarget[] {
  let entries;
  try {
    entries = readdirSync(root, { withFileTypes: true });
  } catch {
    return [];
  }
  const targets: SkillTarget[] = [];
  for (const entry of entries) {
    if (!entry.isDirectory()) continue;
    if (skill && entry.name !== skill) continue;
    const dir = join(root, entry.name);
    if (!hasFile(join(dir, SKILL_MD))) continue;
    targets.push(agent ? { name: entry.name, dir, agent } : { name: entry.name, dir });
  }
  return targets;
}

function convertSkillTarget(target: SkillTarget, opts: ConvertOptions): ConvertResult {
  if (!target.dir) return { kind: "skipped", name: target.name, dir: target.dir, reason: "unknown skill surface format" };
  if (opts.revert) return revertSkillTarget(target);

  const skillPath = join(target.dir, SKILL_MD);
  const origPath = join(target.dir, SKILL_ORIG_MD);
  const currentBytes = readFileSync(skillPath);
  const current = splitSkillMarkdown(currentBytes);
  if (!current) {
    return { kind: "skipped", name: target.name, dir: target.dir, reason: "no frontmatter — discovery needs it as text" };
  }

  if (bodyHasPixelMarker(current.bodyText)) {
    if (!opts.force) return { kind: "skipped", name: target.name, dir: target.dir, reason: "already converted" };
    if (!hasFile(origPath)) {
      return { kind: "skipped", name: target.name, dir: target.dir, reason: "already converted but SKILL.orig.md is missing" };
    }
    const originalBytes = readFileSync(origPath);
    const original = splitSkillMarkdown(originalBytes);
    if (!original) {
      return { kind: "skipped", name: target.name, dir: target.dir, reason: "SKILL.orig.md has no frontmatter" };
    }
    return renderAndMaybeApply(target, originalBytes, original, opts);
  }

  if (hasFile(origPath) && !opts.force) {
    return { kind: "skipped", name: target.name, dir: target.dir, reason: "stale SKILL.orig.md exists — use --force to overwrite" };
  }
  return renderAndMaybeApply(target, currentBytes, current, opts);
}

function revertSkillTarget(target: SkillTarget): ConvertResult {
  const skillPath = join(target.dir, SKILL_MD);
  const origPath = join(target.dir, SKILL_ORIG_MD);
  const currentBytes = readFileSync(skillPath);
  const current = splitSkillMarkdown(currentBytes);
  if (!current || !bodyHasPixelMarker(current.bodyText)) {
    return { kind: "skipped", name: target.name, dir: target.dir, reason: "not converted" };
  }
  if (!hasFile(origPath)) {
    return { kind: "skipped", name: target.name, dir: target.dir, reason: "SKILL.orig.md missing" };
  }
  const originalBytes = readFileSync(origPath);
  writeFileSync(skillPath, originalBytes);
  deleteSkillPixelPages(target.dir);
  unlinkSync(origPath);
  return { kind: "reverted", name: target.name, dir: target.dir };
}

function renderAndMaybeApply(target: SkillTarget, originalBytes: Buffer, split: SplitSkill, opts: ConvertOptions): ConvertResult {
  if (!opts.engineBin) {
    return { kind: "skipped", name: target.name, dir: target.dir, reason: "caveman-engine not found — kept as text; run `caveman setup`" };
  }

  const tempDir = mkdtempSync(join(tmpdir(), "caveman-pixel-"));
  try {
    const tempFile = join(tempDir, "body.md");
    writeFileSync(tempFile, split.bodyBytes);
    // --dense is the pxpipe packing that actually wins: the plain render is
    // never smaller than the text it replaces (measured: 5 KB skill → 1491
    // image vs 1178 text est tokens plain, 313 dense). Line breaks survive as
    // a visible sentinel glyph; droppedChars still gates lossiness below.
    // --density composes with --dense: --dense is the pxpipe pack, --density is the
    // cell geometry it draws with. Forward nothing when unset so the engine default
    // (balanced) applies; page-count math reads the render output, no hardcoded ratio.
    const renderArgs = ["pixel", "render", "--dense", ...(opts.density ? ["--density", opts.density] : []), tempFile];
    const rendered = spawnSync(opts.engineBin, renderArgs, { encoding: "utf8", maxBuffer: 256 * 1024 * 1024 });
    if (rendered.error) {
      const code = (rendered.error as NodeJS.ErrnoException).code;
      const reason = code === "ENOENT"
        ? "caveman-engine not found — kept as text; run `caveman setup`"
        : `render failed: ${rendered.error.message}`;
      return { kind: "skipped", name: target.name, dir: target.dir, reason };
    }
    if ((rendered.status ?? 0) !== 0) {
      return { kind: "skipped", name: target.name, dir: target.dir, reason: `render failed (exit ${rendered.status ?? 1})` };
    }
    const report = parsePixelRenderReport(rendered.stdout);
    if ("reason" in report) return { kind: "skipped", name: target.name, dir: target.dir, reason: report.reason };
    if (report.pages.some((page) => page.droppedChars > 0)) {
      return { kind: "skipped", name: target.name, dir: target.dir, reason: "render dropped chars — kept as text" };
    }
    const afterEst = report.summary.imageEstTokens + STUB_EST_TOKENS;
    if (!(afterEst < report.summary.textEstTokens)) {
      return { kind: "skipped", name: target.name, dir: target.dir, reason: "not smaller — kept as text" };
    }
    const sourcePages = renderedPagePaths(tempFile, report.summary.pages);
    const missingPage = sourcePages.find((page) => !hasFile(page));
    if (missingPage) {
      return { kind: "skipped", name: target.name, dir: target.dir, reason: `render missing ${basename(missingPage)}` };
    }
    if (!opts.dryRun) {
      const destPages = renderedPagePaths(join(resolve(target.dir), "SKILL"), report.summary.pages);
      // Orig is written before anything else is disturbed: a crash anywhere
      // past this line leaves SKILL.orig.md intact, so --revert (or a --force
      // re-run) always recovers, even a --force re-convert that died mid-page-swap.
      writeFileSync(join(target.dir, SKILL_ORIG_MD), originalBytes);
      deleteSkillPixelPages(target.dir);
      for (let i = 0; i < sourcePages.length; i++) {
        copyFileSync(sourcePages[i]!, destPages[i]!);
        unlinkSync(sourcePages[i]!);
      }
      writeFileSync(join(target.dir, SKILL_MD), pixelStubSkill(split.frontmatter, originalBytes, destPages));
    }
    return {
      kind: "converted",
      name: target.name,
      dir: target.dir,
      textEst: report.summary.textEstTokens,
      imageEst: report.summary.imageEstTokens,
      afterEst,
      dryRun: opts.dryRun,
    };
  } finally {
    rmSync(tempDir, { recursive: true, force: true });
  }
}

function parsePixelRenderReport(stdout: string): { summary: { pages: number; textEstTokens: number; imageEstTokens: number }; pages: { droppedChars: number }[] } | { reason: string } {
  const pages: { droppedChars: number }[] = [];
  let summary: { pages: number; textEstTokens: number; imageEstTokens: number } | null = null;
  for (const line of stdout.split(/\r?\n/)) {
    if (!line.trim()) continue;
    let parsed: Record<string, unknown>;
    try {
      parsed = JSON.parse(line) as Record<string, unknown>;
    } catch {
      return { reason: "unparseable render JSONL" };
    }
    if (parsed.summary === true) {
      const pagesN = Number(parsed.pages);
      const textEstTokens = Number(parsed.textEstTokens);
      const imageEstTokens = Number(parsed.imageEstTokens);
      if (!Number.isFinite(pagesN) || !Number.isInteger(pagesN) || pagesN < 1 || !Number.isFinite(textEstTokens) || !Number.isFinite(imageEstTokens)) {
        return { reason: "invalid render summary" };
      }
      summary = { pages: pagesN, textEstTokens, imageEstTokens };
      continue;
    }
    if (typeof parsed.droppedChars !== "number" || !Number.isFinite(parsed.droppedChars)) {
      return { reason: "invalid page render report" };
    }
    pages.push({ droppedChars: parsed.droppedChars });
  }
  if (!summary) return { reason: "missing render summary" };
  if (pages.length < summary.pages) return { reason: "missing page render report" };
  return { summary, pages };
}

function splitSkillMarkdown(bytes: Buffer): SplitSkill | null {
  const text = bytes.toString("utf8");
  const match = text.match(/^---\r?\n[\s\S]*?\r?\n---\r?\n/);
  if (!match) return null;
  const frontmatter = match[0]!;
  const offset = Buffer.byteLength(frontmatter, "utf8");
  return { frontmatter, bodyText: text.slice(frontmatter.length), bodyBytes: bytes.subarray(offset) };
}

function bodyHasPixelMarker(body: string): boolean {
  return body.trimStart().startsWith(PIXEL_MARKER_PREFIX);
}

function pixelStubSkill(frontmatter: string, originalBytes: Buffer, pages: string[]): string {
  const hash = createHash("sha256").update(originalBytes).digest("hex");
  const list = pages.map((page, i) => `${i + 1}. ${page}`).join("\n");
  return `${frontmatter}\n<!-- caveman-pixel v1 sha256:${hash} -->\nThis skill's full instructions are pixel-compressed into image pages to save\ntokens. Read (view) these image files NOW, in order, and follow their contents\nas this skill's complete instructions:\n\n${list}\n\nPlain-text original: SKILL.orig.md in this directory\n(restore with \`caveman convert --revert\`).\n`;
}

function renderedPagePaths(base: string, pages: number): string[] {
  return Array.from({ length: pages }, (_, i) => `${base}.px${i + 1}.png`);
}

function deleteSkillPixelPages(dir: string) {
  for (const entry of readdirSync(dir)) {
    if (/^SKILL\.px\d+\.png$/.test(entry)) unlinkSync(join(dir, entry));
  }
}

function resolveEngineBin(): string | null {
  const bin = cavemanBin("caveman-engine", "CAVEMAN_ENGINE_BIN");
  return commandHasPath(bin) ? (isExecutable(bin) ? bin : null) : which(bin);
}

function hasFile(path: string): boolean {
  try { return statSync(path).isFile(); } catch { return false; }
}

function resolveSkillRoot(path: string): string {
  return resolve(process.cwd(), expandTilde(path));
}

function conversionMath(textEst: number, afterEst: number): string {
  return `${textEst} → ${afterEst} est tokens (−${savingsPct(textEst, afterEst)}% inferred)`;
}

function savingsPct(textEst: number, afterEst: number): number {
  if (textEst <= 0) return 0;
  return Math.max(0, Math.round(((textEst - afterEst) / textEst) * 100));
}

function writeConvertReport(results: ConvertResult[], title = "Caveman convert", preface: string[] = []) {
  const lines = [...preface, ...results.map((result) => {
    if (result.kind === "converted") {
      return `${result.name}: ${conversionMath(result.textEst, result.afterEst)}${result.dryRun ? " (dry-run)" : ""}`;
    }
    if (result.kind === "reverted") return `${result.name}: reverted`;
    return `${result.name}: skipped — ${result.reason}`;
  })];
  const converted = results.filter((r): r is Extract<ConvertResult, { kind: "converted" }> => r.kind === "converted");
  if (converted.length > 0) {
    const before = converted.reduce((sum, r) => sum + r.textEst, 0);
    const after = converted.reduce((sum, r) => sum + r.afterEst, 0);
    lines.push(`total: ${conversionMath(before, after)}`);
  }
  if (lines.length === 0) lines.push("caveman convert: no skills found");
  if (interactive()) panel(title, lines);
  else console.log(lines.join("\n"));
}

function skillsUsage(): never {
  console.error(`usage: ${invokedCommand("skills")} list [--json]`);
  console.error(`       ${invokedCommand("skills")} preview <name>`);
  console.error(`       ${invokedCommand("skills")} install [name] [--suite <name>] [--agent claude|codex] [--user] [--dir <path>] [--density conservative|balanced|max] [--no-pixel]`);
	console.error(`       ${invokedCommand("skills")} add <source> [npx-skills-add-options] [--density conservative|balanced|max] [--no-pixel]`);
	console.error(`       ${invokedCommand("skills")} import <skill-dir|SKILL.md> [--out <dir>] [--dry-run] [--json]`);
  console.error("  install a Caveman agent skill (default name: caveman-learn) or a named suite");
  console.error("  add <source>     install any Git/URL/local source through the official Skills CLI, then pixelize new Claude Code/Codex skills");
  console.error("                   accepts npx skills add flags such as --skill, --agent, --global, --list, --yes, and --all");
  console.error("  --agent claude   Claude Code (default) — writes .claude/skills/<name>/SKILL.md");
  console.error("  --agent codex    Codex — writes ~/.codex/skills/<name>/SKILL.md");
  console.error("  --user           install for all repos (~/.claude/skills) instead of this one");
  console.error("  --dir <path>     single: write SKILL.md there; suite: write <path>/<name>/SKILL.md");
  console.error("  --density LEVEL  pixel pack geometry (default balanced): conservative|balanced|max");
  console.error("  --no-pixel       install plain SKILL.md without pixel conversion");
  process.exit(2);
}

type ImportedSkillManifest = {
	schema: "caveman.skill-import.v1";
	id: string;
	source_sha256: string;
	activation: { status: string; basis: string };
	entry_condition: { status: string; value?: string };
	stop_condition: { status: string; value?: string };
	prompt: { bytes: number; byte_budget: number; status: string };
	resources: { scripts: string[]; references: string[]; assets: string[]; status: string };
	transformations: { exact_duplicate_blocks_removed: number; decorative_separators_removed: number };
	conflicts: { status: "needs_review"; declared: string[] };
	benchmarks: { status: "required"; fixtures: string[] };
	evidence_status: "unevaluated";
	publication: { status: "blocked"; blockers: string[] };
};

function importedSkillSource(input: string): { root: string; file: string } {
	let resolved: string;
	try { resolved = realpathSync(resolve(process.cwd(), expandTilde(input))); } catch {
		throw new Error(`source does not exist: ${input}`);
	}
	const stat = lstatSync(resolved);
	const file = stat.isDirectory() ? join(resolved, SKILL_MD) : resolved;
	if (!stat.isDirectory() && basename(resolved) !== SKILL_MD) throw new Error("source file must be named SKILL.md");
	if (!hasFile(file)) throw new Error(`SKILL.md missing under ${resolved}`);
	return { root: stat.isDirectory() ? resolved : dirname(resolved), file };
}

function cavemannifyImportedSkill(bytes: Buffer): { body: string; removedDuplicates: number; removedSeparators: number } {
	if (bytes.length === 0 || bytes.length > 256 * 1024) throw new Error("SKILL.md must be 1..262144 bytes");
	if (bytes.includes(0)) throw new Error("SKILL.md contains NUL bytes");
	const text = bytes.toString("utf8");
	if (!Buffer.from(text, "utf8").equals(bytes)) throw new Error("SKILL.md must be valid UTF-8");
	if (/-----BEGIN [A-Z ]*PRIVATE KEY-----|\b(?:sk|ghp|github_pat|xox[baprs])[-_][A-Za-z0-9_-]{16,}/i.test(text)) {
		throw new Error("SKILL.md contains credential-shaped material");
	}
	const split = splitSkillMarkdown(bytes);
	if (!split) throw new Error("SKILL.md needs closed YAML frontmatter");
	const blocks = split.bodyText.trim().split(/\r?\n(?:[ \t]*\r?\n)+/);
	const seen = new Set<string>();
	const kept: string[] = [];
	let removedDuplicates = 0;
	let removedSeparators = 0;
	for (const block of blocks) {
		const clean = block.trim();
		if (/^(?:---+|\*\*\*+|___+)$/.test(clean)) {
			removedSeparators++;
			continue;
		}
		const identity = clean.replace(/\s+/g, " ");
		if (seen.has(identity)) {
			removedDuplicates++;
			continue;
		}
		seen.add(identity);
		kept.push(clean);
	}
	return { body: split.frontmatter.replace(/\s+$/, "") + "\n\n" + kept.join("\n\n") + "\n", removedDuplicates, removedSeparators };
}

function skillResourceNames(root: string, directory: string): string[] {
	const path = join(root, directory);
	if (!existsSync(path) || !lstatSync(path).isDirectory()) return [];
	return readdirSync(path).filter((name) => !lstatSync(join(path, name)).isSymbolicLink()).sort();
}

function importedSkillManifest(sourceBytes: Buffer, body: string, root: string, id: string, transformations: { removedDuplicates: number; removedSeparators: number }): ImportedSkillManifest {
	const split = splitSkillMarkdown(Buffer.from(body))!;
	const normalizedFrontmatter = split.frontmatter.replace(/\s+/g, " ");
	const instructionLines = split.bodyText.split(/\r?\n/).map((line) => line.replace(/^\s*(?:[-*]|\d+\.)\s*/, "").trim()).filter(Boolean);
	const entry = instructionLines.find((line) => /^(?:use|apply|start|inspect|gather|define|map)\b/i.test(line));
	const stop = instructionLines.find((line) => /\b(?:stop|finish|done|once .* pass|when .* pass)\b/i.test(line));
	const activationDetected = /\buse (?:when|for)\b|\btrigger/i.test(normalizedFrontmatter);
	const bytes = Buffer.byteLength(split.bodyText.trim());
	const byteBudget = Math.ceil(Math.max(128, bytes) / 128) * 128;
	const resources = {
		scripts: skillResourceNames(root, "scripts"),
		references: skillResourceNames(root, "references"),
		assets: skillResourceNames(root, "assets"),
		status: "copied_unexecuted_deferred",
	};
	const blockers = ["paired or fixture evaluation missing", "conflict review missing"];
	if (!activationDetected) blockers.push("activation condition missing");
	if (!entry) blockers.push("entry condition missing");
	if (!stop) blockers.push("stop condition missing");
	if (bytes > 8192) blockers.push("prompt body exceeds 8192-byte import ceiling; split deferred references");
	return {
		schema: "caveman.skill-import.v1", id,
		source_sha256: `sha256:${createHash("sha256").update(sourceBytes).digest("hex")}`,
		activation: { status: activationDetected ? "detected_needs_review" : "needs_review", basis: activationDetected ? "frontmatter_use_condition" : "none" },
		entry_condition: entry ? { status: "inferred_needs_review", value: entry.slice(0, 240) } : { status: "missing" },
		stop_condition: stop ? { status: "inferred_needs_review", value: stop.slice(0, 240) } : { status: "missing" },
		prompt: { bytes, byte_budget: byteBudget, status: bytes <= 8192 ? "bounded_draft" : "needs_deferred_split" },
		resources,
		transformations: { exact_duplicate_blocks_removed: transformations.removedDuplicates, decorative_separators_removed: transformations.removedSeparators },
		conflicts: { status: "needs_review", declared: [] },
		benchmarks: { status: "required", fixtures: [] },
		evidence_status: "unevaluated",
		publication: { status: "blocked", blockers },
	};
}

function importSkill(rest: string[]) {
	const allowed = new Set(["--out", "--dry-run", "--json"]);
	for (let index = 1; index < rest.length; index++) {
		const value = rest[index]!;
		if (!value.startsWith("--")) continue;
		const flag = value.split("=", 1)[0]!;
		if (!allowed.has(flag)) throw new Error(`unknown import option ${flag}`);
		if (flag === "--out" && !value.includes("=")) index++;
	}
	const input = positionalAfterOptions(rest.slice(1), new Set(["--out"]));
	if (!input) return skillsUsage();
	let source: { root: string; file: string };
	try { source = importedSkillSource(input); } catch (error) {
		console.error(`caveman skills import: ${(error as Error).message}`);
		process.exit(2);
	}
	const sourceBytes = readFileSync(source.file);
	let transformed: ReturnType<typeof cavemannifyImportedSkill>;
	try { transformed = cavemannifyImportedSkill(sourceBytes); } catch (error) {
		console.error(`caveman skills import: ${(error as Error).message}`);
		process.exit(2);
	}
	const name = transformed.body.match(/^name:\s*["']?([a-z0-9-]+)["']?\s*$/m)?.[1];
	if (!name || name.length > 63) {
		console.error("caveman skills import: frontmatter name must be lowercase hyphen-case under 64 characters");
		process.exit(2);
	}
	const manifest = importedSkillManifest(sourceBytes, transformed.body, source.root, name, transformed);
	const target = resolve(process.cwd(), expandTilde(flagFrom(rest, "--out", join(cavemanHome(), "packs", "imports", name))));
	if (!rest.includes("--dry-run")) {
		if (existsSync(target)) {
			console.error(`caveman skills import: target already exists; refusing overwrite: ${target}`);
			process.exit(2);
		}
		mkdirSync(dirname(target), { recursive: true, mode: 0o700 });
		const temporary = mkdtempSync(join(dirname(target), `.import-${name}-`));
		try {
			writeFileSync(join(temporary, SKILL_MD), transformed.body, { mode: 0o600 });
			writeFileSync(join(temporary, "import.json"), JSON.stringify(manifest, null, 2) + "\n", { mode: 0o600 });
			for (const directory of ["scripts", "references", "assets"]) {
				const from = join(source.root, directory);
				if (!existsSync(from) || !lstatSync(from).isDirectory()) continue;
				cpSync(from, join(temporary, directory), { recursive: true, filter: (path) => !lstatSync(path).isSymbolicLink() });
			}
			renameSync(temporary, target);
		} catch (error) {
			rmSync(temporary, { recursive: true, force: true });
			console.error(`caveman skills import: ${(error as Error).message}`);
			process.exit(1);
		}
	}
	if (rest.includes("--json")) return print({ target: rest.includes("--dry-run") ? null : target, manifest });
	console.log(`${rest.includes("--dry-run") ? "Would create" : "Created"} ${target}`);
	console.log(`Draft ${name}: ${manifest.prompt.bytes} prompt bytes; ${manifest.transformations.exact_duplicate_blocks_removed} exact duplicate block(s) removed.`);
	console.log("Publication blocked: evaluation, conflict review, and missing activation/entry/stop metadata must clear first.");
}

function skillsList(json: boolean) {
  if (json) {
    print({ skills: AGENT_SKILL_METADATA, suites: AGENT_SKILL_SUITES });
    return;
  }
  for (const skill of AGENT_SKILL_METADATA) {
    const suites = skill.suites.length > 0 ? ` · ${skill.suites.join(",")}` : "";
    console.log(`${skill.id}\t${skill.summary}${suites}`);
  }
}

function skillInstallNames(rest: string[]): string[] {
  const suite = flagFrom(rest, "--suite", "");
  if (suite) {
    const names = AGENT_SKILL_SUITES[suite];
    if (!names) {
      console.error(`caveman skills: unknown suite ${JSON.stringify(suite)} (known: ${Object.keys(AGENT_SKILL_SUITES).join(", ")})`);
      process.exit(2);
    }
    return names;
  }
  const after = rest.slice(1);
  const optionsWithValues = new Set(["--agent", "--dir", "--density", "--suite"]);
  return [positionalAfterOptions(after, optionsWithValues) ?? "caveman-learn"];
}

function skillDestination(name: string, rest: string[], multiple: boolean, agent: string): string {
  const directDir = flagFrom(rest, "--dir", "");
  if (directDir) return join(directDir, ...(multiple ? [name, "SKILL.md"] : ["SKILL.md"]));
  if (agent === "claude") {
    const root = rest.includes("--user")
      ? join(homedir(), ".claude", "skills")
      : join(process.cwd(), ".claude", "skills");
    return join(root, name, "SKILL.md");
  }
  if (agent === "codex") return join(homedir(), ".codex", "skills", name, "SKILL.md");
  console.error(`caveman skills: --agent must be claude or codex (got ${agent})`);
  process.exit(2);
}

type ExternalSkillRoot = { root: string; agent: "claude" | "codex" };

function externalSkillRoots(args: string[]): ExternalSkillRoot[] {
  const global = args.includes("--global") || args.includes("-g");
  if (global) {
    return [
      { root: join(homedir(), ".claude", "skills"), agent: "claude" },
      { root: join(homedir(), ".agents", "skills"), agent: "codex" },
      { root: join(homedir(), ".codex", "skills"), agent: "codex" },
    ];
  }
  return [
    { root: join(process.cwd(), ".claude", "skills"), agent: "claude" },
    { root: join(process.cwd(), ".agents", "skills"), agent: "codex" },
  ];
}

function externalSkillFingerprint(target: SkillTarget): string {
  const path = join(target.dir, SKILL_MD);
  const stat = statSync(path, { bigint: true });
  const hash = createHash("sha256").update(readFileSync(path)).digest("hex");
  return `${stat.size}:${stat.mtimeNs}:${stat.ctimeNs}:${hash}`;
}

function snapshotExternalSkills(roots: ExternalSkillRoot[]): Map<string, string> {
  const snapshot = new Map<string, string>();
  for (const { root, agent } of roots) {
    for (const target of discoverTargetsInRoot(root, "", agent)) {
      snapshot.set(resolve(target.dir), externalSkillFingerprint(target));
    }
  }
  return snapshot;
}

function stripCavemanSkillAddOptions(rest: string[]): string[] {
  const args: string[] = [];
  for (let index = 1; index < rest.length; index++) {
    const value = rest[index]!;
    if (value === "--no-pixel") continue;
    if (value === "--density") {
      index++;
      continue;
    }
    if (value.startsWith("--density=")) continue;
    args.push(value);
  }
  return args;
}

function changedExternalSkillTargets(
  roots: ExternalSkillRoot[],
  before: Map<string, string>,
): SkillTarget[] {
  const changed: SkillTarget[] = [];
  const seen = new Set<string>();
  for (const { root, agent } of roots) {
    for (const target of discoverTargetsInRoot(root, "", agent)) {
      const key = resolve(target.dir);
      if (seen.has(key)) continue;
      const current = externalSkillFingerprint(target);
      if (before.get(key) === current) continue;
      seen.add(key);
      changed.push(target);
    }
  }
  return changed;
}

function runExternalSkillAdd(rest: string[]) {
  if (rest.length < 2) return skillsUsage();
  const density = parsePixelDensity(flagFrom(rest, "--density", ""), "--density");
  const noPixel = rest.includes("--no-pixel");
  const args = stripCavemanSkillAddOptions(rest);
  const listOnly = args.includes("--list") || args.includes("-l");
  const roots = externalSkillRoots(args);
  const before = noPixel || listOnly ? new Map<string, string>() : snapshotExternalSkills(roots);
  if (!noPixel && !listOnly && !args.includes("--copy")) args.push("--copy");

  const npx = which("npx");
  if (!npx) {
    console.error("caveman skills add: npx not found; install Node.js with npm, then retry");
    process.exit(127);
  }
  const invocation = portableInvocation(npx, ["--yes", "skills", "add", ...args]);
  const result = spawnSync(invocation.command, invocation.args, {
    cwd: process.cwd(),
    env: process.env,
    stdio: "inherit",
  });
  if (result.error) {
    console.error(`caveman skills add: could not run Skills CLI: ${result.error.message}`);
    process.exit(1);
  }
  if ((result.status ?? 1) !== 0) process.exit(result.status ?? 1);
  if (listOnly) return;
  if (noPixel) {
    panel("Third-party skill installed", [
      `${mark("ok")} Official Skills CLI completed.`,
      `${mark("ok")} Installed plain text (--no-pixel).`,
      `${mark("warn")} Third-party instructions and resources were not security-reviewed by Caveman.`,
    ]);
    return;
  }

  const targets = changedExternalSkillTargets(roots, before);
  const opts: ConvertOptions = { dryRun: false, force: true, revert: false, engineBin: resolveEngineBin(), density };
  const results: ConvertResult[] = [];
  for (const target of targets) {
    try {
      results.push(convertSkillTarget(target, opts));
    } catch (error) {
      console.error(`caveman skills add: cannot pixelize ${target.dir}: ${(error as Error).message}`);
      process.exit(1);
    }
  }
  const preface = [
    `${mark("ok")} Official Skills CLI completed in copy mode.`,
    `${mark("warn")} Third-party instructions and resources were not security-reviewed by Caveman.`,
  ];
  if (results.length === 0) {
    preface.push(`${mark("warn")} No new or changed Claude Code/Codex SKILL.md found; nothing pixelized.`);
  }
  writeConvertReport(results, "Third-party skill installed", preface);
}

// skills installs generated byte-identical copies of canonical
// public/skills/<name>/SKILL.md files. Suites are deterministic registry lists.
async function skills(rest: string[]) {
  if (rest[0] === "list") return skillsList(rest.includes("--json"));
	if (rest[0] === "import") return importSkill(rest);
	if (rest[0] === "add") return runExternalSkillAdd(rest);
  if (rest[0] === "preview") {
    const name = rest[1] ?? "";
    const body = SKILLS[name];
    if (!body) {
      console.error(`caveman skills: unknown skill ${JSON.stringify(name)} (known: ${Object.keys(SKILLS).join(", ")})`);
      process.exit(2);
    }
    process.stdout.write(body);
    return;
  }
  if (rest[0] !== "install") return skillsUsage();
  const names = skillInstallNames(rest);
  const agent = flagFrom(rest, "--agent", "claude");
  if (agent !== "claude" && agent !== "codex") {
    console.error(`caveman skills: --agent must be claude or codex (got ${agent})`);
    process.exit(2);
  }
  for (const name of names) {
    const body = SKILLS[name];
    if (!body) {
      console.error(`caveman skills: unknown skill ${JSON.stringify(name)} (known: ${Object.keys(SKILLS).join(", ")})`);
      process.exit(2);
    }
    if (name === "caveman-explore") guardExploreAgent(agent);
  }
  const installed: { name: string; dest: string; pixelResult: ConvertResult | null }[] = [];
  for (const name of names) {
    const dest = skillDestination(name, rest, names.length > 1, agent);
    try {
      mkdirSync(dirname(dest), { recursive: true });
      writeFileSync(dest, SKILLS[name]!);
    } catch (e) {
      console.error(`caveman skills: cannot write ${dest}: ${(e as Error).message}`);
      process.exit(1);
    }
    let pixelResult: ConvertResult | null = null;
    if (!rest.includes("--no-pixel")) {
      try {
        pixelResult = convertSkillTarget(
          { name, dir: dirname(dest), agent },
          { dryRun: false, force: true, revert: false, engineBin: resolveEngineBin(), density: parsePixelDensity(flagFrom(rest, "--density", ""), "--density") },
        );
      } catch (e) {
        console.error(`caveman skills: cannot write ${dirname(dest)}: ${(e as Error).message}`);
        process.exit(1);
      }
    }
    installed.push({ name, dest, pixelResult });
  }
  const note = agent === "codex"
    ? "Codex auto-loads skill directories from ~/.codex/skills."
    : "Claude Code auto-loads this skill when its description matches.";
  const lines = installed.flatMap(({ name, dest, pixelResult }) => [
    `${mark("ok")} ${name}: ${cyan(dest)}`,
    rest.includes("--no-pixel")
      ? `${mark("ok")} Installed plain text (--no-pixel).`
      : pixelInstallLine(pixelResult),
  ]);
  lines.push("", `${installed.length} standard SKILL.md director${installed.length === 1 ? "y" : "ies"} installed.`, "", dim(note));
  panel(installed.length === 1 ? "Caveman skill installed" : "Caveman skill suite installed", lines);
}

function pixelInstallLine(result: ConvertResult | null): string {
  if (!result) return `${mark("warn")} Installed plain text: pixel conversion did not run.`;
  if (result.kind === "converted") {
    return `${mark("ok")} Installed pixel form: ${conversionMath(result.textEst, result.afterEst)} per invocation.`;
  }
  if (result.kind === "skipped") {
    return `${mark("warn")} Installed plain text: ${result.reason}`;
  }
  return `${mark("ok")} Installed plain text.`;
}

// start launches the standalone byte-safe proxy on 127.0.0.1:8787. The Go binary
// is resolved via CAVEMAN_PROXY_BIN (default `caveman-proxy` on PATH); its stdio
// is inherited and its exit code is forwarded. If the port is already served it
// says so; if the binary is missing it renders a panel explaining how to get a
// proxy running instead of a bare spawn error.
type StartOptions = { host: string; port: number; listen: string; config?: string };

function startOptions(argv: string[]): StartOptions {
  const inherited = standaloneProxyEndpoint();
  let host = inherited.host;
  let port = inherited.port;
  let config: string | undefined;
  const seen = new Set<string>();
  for (let index = 0; index < argv.length; index++) {
    const arg = argv[index]!;
    const name = arg.split("=", 1)[0]!;
    if (name !== "--host" && name !== "--port" && name !== "--config") {
      commandUsage("start [--port 8787] [--host 127.0.0.1] [--config caveman.yaml]");
    }
    if (seen.has(name)) commandUsage("start [--port 8787] [--host 127.0.0.1] [--config caveman.yaml]");
    seen.add(name);
    const inline = arg.startsWith(`${name}=`) ? arg.slice(name.length + 1) : undefined;
    const value = inline ?? argv[++index];
    if (!value || value.startsWith("-")) commandUsage("start [--port 8787] [--host 127.0.0.1] [--config caveman.yaml]");
    if (name === "--host") {
      if (value.includes("/") || /\s/.test(value)) commandUsage("start [--port 8787] [--host 127.0.0.1] [--config caveman.yaml]");
      host = value;
    } else if (name === "--port") {
      const parsed = Number(value);
      if (!Number.isInteger(parsed) || parsed < 1 || parsed > 65535) commandUsage("start [--port 8787] [--host 127.0.0.1] [--config caveman.yaml]");
      port = parsed;
    } else {
      config = value;
    }
  }
  const listenHost = host.includes(":") && !host.startsWith("[") ? `[${host}]` : host;
  return { host, port, listen: `${listenHost}:${port}`, ...(config ? { config } : {}) };
}

async function start(argv: string[] = []) {
  const bin = cavemanBin("caveman-proxy", "CAVEMAN_PROXY_BIN");
  const options = startOptions(argv);
  const { host, port, listen } = options;

  if (await portListening(host, port)) {
    panel("Caveman proxy already running", [
      `${mark("ok")} Something is already listening on ${host}:${port}.`,
      "",
      `Route an agent through it:  ${cyan("caveman wrap claude")}`,
      `See local spend:           ${cyan("caveman stats")}`,
    ]);
    return;
  }

  const resolved = which(bin);
  if (!resolved) return startMissingProxyUI(bin, options);

  // `caveman start` is the sibling entry point to `caveman wrap`, so it resolves the
  // SAME mode decision. There is no account condition in it — the
  // entitlement read is a label, not a permission. The mode this proxy actually runs
  // is CAVEMAN_MODE (the proxy itself falls back to record for anything unknown), so
  // a plain `caveman start` resolves to record; record never mutates bytes anyway.
  // A mode set only in the proxy's own caveman.yaml is invisible from here.
  // (honesty rule: byte-safe)
  const runtime = wrapRuntimeConfig({ forStart: true });
  const invalidMode = invalidModeLine(runtime.resolution);
  if (invalidMode) process.stderr.write(`${invalidMode}\n`);
  const modeSource = runtime.resolution.values["think.mode"].source;
  const requestedMode = modeSource === "default" ? "record" : runtime.mode;
  const startGate = resolveWrapGate(readWrapEntitlement(), new Date(), requestedMode);
  const subscriptionCompress = subscriptionCompressEnabled(startGate);
  // A bare proxy has no active-agent identity, so it cannot prove that whichever
  // subscription client arrives owns a compatible recovery tool. Keep the global
  // recovery signal off; only `caveman wrap <agent>` can bind recovery to a checked
  // agent profile. Inherited environment claims never cross this boundary.
  const mcpRecovery = startMcpRecoveryAvailable();
  const env: NodeJS.ProcessEnv = {
    ...process.env,
    CAVEMAN_PROXY_OWNER: "start",
    CAVEMAN_LISTEN: listen,
    CAVEMAN_RECOVERY: mcpRecovery ? "mcp" : "",
  };
  if (options.config) env.CAVEMAN_CONFIG = options.config;
  // The account gate is gone and so is the variable that carried it. Strip
  // any inherited copy so a stray export can never re-enter the contract; a proxy
  // binary old enough to still read it is already reported by the stale-binary state.
  delete env.CAVEMAN_WRAP_ENTITLED;
  if (modeSource === "legacy-wrap" || modeSource === "global" || modeSource === "env") {
    env.CAVEMAN_MODE = runtime.mode;
  } else {
    delete env.CAVEMAN_MODE;
  }
  env.CAVE_ENGINE_TOON = runtime.mode === "compress" && runtime.toon ? "best-of" : "";
  env.CAVE_PIXEL_MODELS = runtime.pixelModels ?? "";
  env.CAVE_PIXEL_DENSITY = runtime.pixelDensity ?? "";
  if (subscriptionCompress && mcpRecovery) {
    process.stderr.write(dim("→ subscription logins (Claude Pro/Max) compress locally too — live zone only; compressed turns are re-sent byte-identically so the provider cache stays warm\n"));
    process.stderr.write(dim(`→ ${SUBSCRIPTION_TOKENS_ONLY_NOTE}\n`));
  } else if (subscriptionCompress) {
    // Deliberately NOT the marker-only variant. That note's remedy is
    // `config set execute.mcp auto`, which only means anything on the wrap door —
    // `start` launches a bare proxy and never installs anything, so flipping the
    // surface back would change nothing here. `mcp install <agent>` is the remedy
    // that actually works at this door, in every surface mode: execute.mcp governs
    // what WRAP injects, and an explicit install is the operator's own act.
    process.stderr.write(dim(`→ ${SUBSCRIPTION_NO_RECOVERY_NOTE}\n`));
  }

  const child = spawn(resolved, [], { stdio: "inherit", env });
  // This handler hard-exits with the child's code and never returns to main(),
  // so the run event goes out now (same pattern as wrap's pre-exec emit).
  emitCommandRunOnce("ok");
  child.on("error", (error) => {
    console.error(`failed to launch ${bin}: ${error.message} (set CAVEMAN_PROXY_BIN or build the proxy)`);
    process.exit(1);
  });
  child.on("exit", (code) => process.exit(code ?? 0));
}

// startMissingProxyUI is the cohesive fallback when `caveman start` can't find a
// proxy binary: one panel with the signed installer and explicit override.
// Exits non-zero so scripts can branch on it.
function startMissingProxyUI(bin: string, options = startOptions([])): never {
  const { host, port } = options;

  panel("Caveman proxy not found", [
    `${mark("bad")} Couldn't find ${cyan(bin)} on your PATH or in ~/.caveman/bin.`,
    "",
    `${bold("caveman start")} runs the local byte-safe proxy on ${host}:${port}, so your`,
    "agents' LLM traffic is metered with no code change. Get one running:",
    "",
    `${cyan("1.")} Install the signed runtime companions:`,
    `   ${dim("caveman setup --install")}`,
    "",
    `${cyan("2.")} Or point at an existing binary:`,
    `   ${dim("export CAVEMAN_PROXY_BIN=/path/to/caveman-proxy")}`,
    "",
    `Check the full install state any time: ${cyan("caveman setup")}`,
  ]);
  process.exit(1);
}

// ── caveman setup ────────────────────────────────────────────────────────────
// setup makes the degraded install state impossible to miss. The npm package
// ships only this JS front-end; compression, metering, recovery, and browsing
// run in caveman's Go binaries (proxy/engine/mcp/mem/browse/shrink). Without them every
// affected command degrades to a LOUD byte-safe pass-through — setup is the one
// place that shows exactly what works, what doesn't, and the one command that
// fixes it. `setup --install` downloads only signed, checksum-verified release
// binaries. Plain setup exits non-zero when a required binary is missing so
// scripts can gate on it. (honesty rule: no-fake-savings —
// a pass-through claims 0% and says so.)
const GO_BINARIES = [
  { name: "caveman-proxy", env: "CAVEMAN_PROXY_BIN", required: true, powers: "start · wrap · stats · verify — local compression + truthful metering", without: "wrap still launches agents, but LLM traffic is NOT compressed or metered" },
  { name: "caveman-engine", env: "CAVEMAN_ENGINE_BIN", required: true, powers: "compress · shrink · retrieve · toon · evals", without: "compress/shrink pass input through unchanged (reported as 0%); toon decode refuses" },
  { name: "caveman-mcp", env: "CAVEMAN_MCP_BIN", required: true, powers: "mcp install — agent-side recovery that lets streaming requests compress", without: "streaming requests pass through uncompressed" },
  { name: "cavemem", env: "CAVEMEM_BIN", required: true, powers: "remember · recall · learn offload", without: "memory and auto-recall are off" },
  { name: "caveman-browse", env: "CAVEMAN_BROWSE_BIN", required: false, powers: "browse + agent-side compressed browsing MCP tools — wrap auto-registers once present", without: "agent-side compressed browsing MCP tools unavailable; wrap auto-registers once installed" },
  { name: "caveman-shrink", env: "CAVEMAN_SHRINK_BIN", required: false, powers: "compress catalog — dedicated tool-schema compression, lint, and recovery", without: "tool-catalog compression is unavailable; command-output shrink is unaffected" },
] as const;

// resolveGoBin is cavemanBin plus an honest "is it actually there" answer: the
// bare-name fallback that keeps the missing-binary panels working is NOT a find.
function resolveGoBin(name: string, envVar: string): string | null {
  const bin = cavemanBin(name, envVar);
  return commandHasPath(bin) ? (isExecutable(bin) ? bin : null) : which(bin);
}

type VersionedBinaryProbe = {
  version: string;
  capabilities: string[];
  current: boolean;
};

const versionedBinaryProbeCache = new Map<string, Omit<VersionedBinaryProbe, "current">>();

function versionedBinaryProbeTimeoutMs(): number {
  const parsed = Number(process.env.CAVE_BINARY_PROBE_TIMEOUT_MS ?? "2000");
  if (!Number.isFinite(parsed)) return 2000;
  return Math.max(100, Math.min(30_000, Math.round(parsed)));
}

// Compatibility probes run on the launch path, so each resolved executable is
// checked at most once per process and file generation. Cache raw capability
// set, then evaluate each required capability independently: one binary can
// support native runtime while lacking newer hook bridge. stdin is closed and
// hard timeout turns old/hung binaries into explicit stale state.
function probeVersionedBinary(binary: string, requiredCapability: string): VersionedBinaryProbe {
  let cacheKey = `${binary}:unstatable`;
  try {
    const stat = statSync(binary);
    cacheKey = `${binary}:${stat.mtimeMs}:${stat.size}`;
  } catch {
    // execFileSync below supplies the fail-closed compatibility result.
  }
  const cached = versionedBinaryProbeCache.get(cacheKey);
  if (cached) return { ...cached, current: cached.capabilities.includes(requiredCapability) };

  let result: Omit<VersionedBinaryProbe, "current"> = { version: "pre-versioned", capabilities: [] };
  try {
    const raw = execFileSync(binary, ["version", "--json"], {
      encoding: "utf8",
      env: process.env,
      timeout: versionedBinaryProbeTimeoutMs(),
      stdio: ["ignore", "pipe", "ignore"],
    });
    const parsed = JSON.parse(raw) as { version?: unknown; capabilities?: unknown };
    const capabilities = Array.isArray(parsed.capabilities)
      ? parsed.capabilities.filter((item): item is string => typeof item === "string")
      : [];
    result = {
      version: typeof parsed.version === "string" && parsed.version ? parsed.version : "unknown",
      capabilities,
    };
  } catch {
    // Pre-versioned binaries exit 2; broken binaries may hang or emit invalid
    // JSON. All are stale, never silently treated as compatible.
  }
  versionedBinaryProbeCache.set(cacheKey, result);
  return { ...result, current: result.capabilities.includes(requiredCapability) };
}

type InstalledBinary = {
  name: string;
  path: string;
  sha256: string;
  status: "installed" | "already installed";
};

type BinaryInstallManifest = {
  release: string;
  artifacts: Record<string, string>;
};

const INSTALL_BINARIES = GO_BINARIES.map((binary) => binary.name);

function setupTimeoutSeconds(): number {
  const raw = process.env.CAVE_SETUP_TIMEOUT ?? "300";
  const parsed = Number(raw);
  if (!Number.isInteger(parsed) || parsed <= 0) {
    throw new Error(`CAVE_SETUP_TIMEOUT must be a positive integer (got ${JSON.stringify(raw)})`);
  }
  return parsed;
}

export function setupPlatform(
  os: NodeJS.Platform = process.platform,
  nodeArch: string = process.arch,
): { os: string; arch: string } {
  const arch = nodeArch === "x64" ? "amd64" : nodeArch;
  if (!(os === "darwin" || os === "linux" || os === "win32") ||
      !(arch === "arm64" || arch === "amd64")) {
    throw new Error(OFF_STATES.unsupportedPlatform(os, arch).line);
  }
  return { os, arch };
}

export function binaryInstallFilename(name: string, os: string = process.platform): string {
  return os === "win32" ? `${name}.exe` : name;
}

function binaryInstallManifestPath(): string {
  return join(cavemanHome(), "bin", ".bin-manifest.json");
}

function sha256File(path: string): string | null {
  try {
    return createHash("sha256").update(readFileSync(path)).digest("hex");
  } catch {
    return null;
  }
}

function readBinaryInstallManifest(): BinaryInstallManifest | null {
  try {
    const parsed = JSON.parse(readFileSync(binaryInstallManifestPath(), "utf8")) as {
      release?: unknown;
      artifacts?: unknown;
    };
    if (typeof parsed.release !== "string" || !parsed.artifacts || typeof parsed.artifacts !== "object") return null;
    const artifacts: Record<string, string> = {};
    for (const [name, digest] of Object.entries(parsed.artifacts)) {
      if (typeof digest !== "string" || !/^[a-f0-9]{64}$/.test(digest)) return null;
      artifacts[name] = digest;
    }
    return { release: parsed.release, artifacts };
  } catch {
    return null;
  }
}

function verifiedLocalInstall(binDir: string): InstalledBinary[] | null {
  const manifest = readBinaryInstallManifest();
  if (!manifest || manifest.release !== BINARY_RELEASE) return null;
  const installed: InstalledBinary[] = [];
  for (const name of INSTALL_BINARIES) {
    const expected = manifest.artifacts[name];
    const path = join(binDir, binaryInstallFilename(name));
    if (!expected || sha256File(path) !== expected) return null;
    installed.push({ name, path, sha256: expected, status: "already installed" });
  }
  return installed;
}

function parseSignedChecksums(raw: string): Map<string, string> {
  const checksums = new Map<string, string>();
  for (const line of raw.split("\n")) {
    if (!line) continue;
    const match = line.match(/^([a-f0-9]{64})  ([A-Za-z0-9._-]+)$/);
    if (!match) throw new Error(`invalid checksum manifest line: ${JSON.stringify(line)}`);
    const filename = match[2]!;
    if (checksums.has(filename)) throw new Error(`duplicate checksum manifest entry: ${filename}`);
    checksums.set(filename, match[1]!);
  }
  return checksums;
}

function verifyChecksumSignature(checksums: string, signature: string): boolean {
  try {
    const bundle = JSON.parse(signature) as {
      mediaType?: unknown;
      messageSignature?: {
        messageDigest?: { algorithm?: unknown; digest?: unknown };
        signature?: unknown;
      };
    };
    if (bundle.mediaType !== "application/vnd.dev.sigstore.bundle.v0.3+json") return false;
    if (bundle.messageSignature?.messageDigest?.algorithm !== "SHA2_256") return false;
    if (typeof bundle.messageSignature.messageDigest.digest !== "string") return false;
    if (typeof bundle.messageSignature.signature !== "string") return false;
    const digest = createHash("sha256").update(checksums).digest();
    const bundledDigest = Buffer.from(bundle.messageSignature.messageDigest.digest, "base64");
    if (digest.length !== bundledDigest.length || !digest.equals(bundledDigest)) return false;
    return edVerify(
      "sha256",
      Buffer.from(checksums),
      createPublicKey(BINARY_SIGNING_PUBKEY),
      Buffer.from(bundle.messageSignature.signature, "base64"),
    );
  } catch {
    return false;
  }
}

class BinaryDownloadError extends Error {
  constructor(readonly kind: "unreachable" | "stalled", message: string) {
    super(message);
  }
}

function isTimeoutError(error: unknown): boolean {
  return error instanceof Error && (error.name === "TimeoutError" || error.name === "AbortError");
}

async function fetchReleaseAsset(url: string, timeoutSeconds: number): Promise<Response> {
  try {
    const response = await fetch(url, { signal: AbortSignal.timeout(timeoutSeconds * 1000) });
    if (!response.ok) throw new BinaryDownloadError("unreachable", `${response.status} ${response.statusText}`);
    return response;
  } catch (error) {
    if (error instanceof BinaryDownloadError) throw error;
    if (isTimeoutError(error)) throw new BinaryDownloadError("stalled", (error as Error).message);
    throw new BinaryDownloadError("unreachable", (error as Error).message);
  }
}

async function downloadReleaseBinary(
  url: string,
  partPath: string,
  timeoutSeconds: number,
): Promise<{ sha256: string; bytes: number }> {
  const response = await fetchReleaseAsset(url, timeoutSeconds);
  if (!response.body) throw new BinaryDownloadError("unreachable", "response body missing");
  const hash = createHash("sha256");
  const file = await open(partPath, constants.O_CREAT | constants.O_EXCL | constants.O_WRONLY, 0o600);
  const reader = response.body.getReader();
  let bytes = 0;
  try {
    while (true) {
      const { done, value } = await reader.read();
      if (done) break;
      const body = Buffer.from(value);
      hash.update(body);
      bytes += body.length;
      await file.write(body);
    }
  } catch (error) {
    if (isTimeoutError(error)) throw new BinaryDownloadError("stalled", (error as Error).message);
    throw error;
  } finally {
    reader.releaseLock();
    await file.close();
  }
  return { sha256: hash.digest("hex"), bytes };
}

function cleanupPartial(path: string) {
  try {
    unlinkSync(path);
  } catch (error) {
    if ((error as NodeJS.ErrnoException).code !== "ENOENT") throw error;
  }
}

function installProgressStart(name: string, platform: { os: string; arch: string }) {
  const line = `${name}  ${platform.os}/${platform.arch}  …`;
  if (interactive()) process.stderr.write(line);
  else console.error(line);
}

function installProgressComplete(
  name: string,
  platform: { os: string; arch: string },
  bytes: number,
) {
  const line = `${name}  ${platform.os}/${platform.arch}  ${(bytes / 1_000_000).toFixed(1)} MB  checksum verified`;
  if (interactive()) process.stderr.write(`\r${line}\n`);
  else console.error(line);
}

function setupInstallFailure(error: unknown, timeoutSeconds: number): never {
  if (interactive()) process.stderr.write("\n");
  if (error instanceof BinaryDownloadError && error.kind === "stalled") {
    throw new Error(`${OFF_STATES.downloadStalled(timeoutSeconds).line}\nfix: ${OFF_STATES.downloadStalled(timeoutSeconds).fix}`);
  }
  throw new Error(`${OFF_STATES.downloadUnreachable.line}\nfix: ${OFF_STATES.downloadUnreachable.fix}`);
}

function printInstallResult(
  installed: InstalledBinary[],
  platform: { os: string; arch: string },
  binDir: string,
  json: boolean,
  continuing = false,
) {
  if (json) {
    print({
      release: BINARY_RELEASE,
      platform: `${platform.os}/${platform.arch}`,
      target: binDir,
      binaries: installed,
      next: "caveman claude",
    });
    return;
  }
  for (const item of installed) {
    const line = `${item.name}  ${platform.os}/${platform.arch}  ${item.path}  ${item.status} · checksum verified`;
    if (continuing) console.error(line);
    else console.log(line);
  }
  if (!continuing) console.log(`next: caveman claude`);
}

async function setupInstall(json: boolean, options: { continuing?: boolean } = {}) {
  const platform = setupPlatform();
  const timeoutSeconds = setupTimeoutSeconds();
  const binDir = join(ensureCavemanHome(), "bin");
  mkdirSync(binDir, { recursive: true, mode: 0o700 });

  const local = verifiedLocalInstall(binDir);
  if (local) {
    printInstallResult(local, platform, binDir, json, options.continuing);
    return;
  }

  const base = (process.env.CAVE_BINARY_RELEASE_BASE ?? BINARY_RELEASE_BASE_DEFAULT).replace(/\/+$/, "");
  const releaseBase = `${base}/${BINARY_RELEASE}`;
  let checksumsRaw: string;
  let signatureRaw: string;
  try {
    const [checksumsResponse, signatureResponse] = await Promise.all([
      fetchReleaseAsset(`${releaseBase}/checksums.txt`, timeoutSeconds),
      fetchReleaseAsset(`${releaseBase}/checksums.txt.keysig`, timeoutSeconds),
    ]);
    [checksumsRaw, signatureRaw] = await Promise.all([checksumsResponse.text(), signatureResponse.text()]);
  } catch (error) {
    setupInstallFailure(error, timeoutSeconds);
  }

  if (!verifyChecksumSignature(checksumsRaw!, signatureRaw!)) {
    throw new Error("signature check failed for checksums.txt — refusing to install; partial download deleted");
  }

  let checksums: Map<string, string>;
  try {
    checksums = parseSignedChecksums(checksumsRaw!);
  } catch {
    throw new Error("signature check failed for checksums.txt — refusing to install; partial download deleted");
  }

  const installed: InstalledBinary[] = [];
  const artifactDigests: Record<string, string> = {};
  for (const name of INSTALL_BINARIES) {
    const artifact = `${name}_${platform.os}_${platform.arch}`;
    const expected = checksums.get(artifact);
    if (!expected) {
      throw new Error(`signature check failed for ${artifact} — refusing to install; partial download deleted`);
    }
    const target = join(binDir, binaryInstallFilename(name, platform.os));
    artifactDigests[name] = expected;
    if (sha256File(target) === expected) {
      installed.push({ name, path: target, sha256: expected, status: "already installed" });
      continue;
    }

    const partial = `${target}.part`;
    cleanupPartial(partial);
    installProgressStart(name, platform);
    let result: { sha256: string; bytes: number };
    try {
      result = await downloadReleaseBinary(`${releaseBase}/${artifact}`, partial, timeoutSeconds);
    } catch (error) {
      cleanupPartial(partial);
      setupInstallFailure(error, timeoutSeconds);
    }
    if (result!.sha256 !== expected) {
      cleanupPartial(partial);
      if (interactive()) process.stderr.write("\n");
      throw new Error(`signature check failed for ${artifact} — refusing to install; partial download deleted`);
    }
    await chmod(partial, 0o755);
    await rename(partial, target);
    installProgressComplete(name, platform, result!.bytes);
    installed.push({ name, path: target, sha256: expected, status: "installed" });
  }

  const manifest: BinaryInstallManifest = { release: BINARY_RELEASE, artifacts: artifactDigests };
  await writeFile(binaryInstallManifestPath(), `${JSON.stringify(manifest, null, 2)}\n`, { mode: 0o600 });
  await chmod(binaryInstallManifestPath(), 0o600);
  printInstallResult(installed, platform, binDir, json, options.continuing);
}

type AgentNativeBundleSkill = {
  file: string;
  before_base64: string | null;
  after_sha256: string;
};

type AgentNativeBundleJournal = {
  schema_version: 1;
  agent: "claude" | "codex";
  pack_version: string;
  native_owned: boolean;
  cloud_mcp: { command: string; args: string[] };
  previous_cloud_mcp: { command: string; args: string[] } | null;
  skills: AgentNativeBundleSkill[];
};

function agentNativeBundleJournalPath(agent: "claude" | "codex", pending = false): string {
  return join(cavemanHome(), "integrations", `${agent}.agent-native-bundle${pending ? ".pending" : ""}.json`);
}

function agentNativeBundleRemovalJournalPath(agent: "claude" | "codex"): string {
  return join(cavemanHome(), "integrations", `${agent}.agent-native-bundle.removing.json`);
}

function readAgentNativeBundleJournal(agent: "claude" | "codex"): AgentNativeBundleJournal | null {
  try {
    const value = JSON.parse(readFileSync(agentNativeBundleJournalPath(agent), "utf8")) as AgentNativeBundleJournal;
    if (value.schema_version !== 1 || value.agent !== agent || !value.cloud_mcp || !Array.isArray(value.skills)) {
      throw new Error("unsupported bundle journal");
    }
    return value;
  } catch (error) {
    if ((error as NodeJS.ErrnoException).code === "ENOENT") return null;
    throw new Error(`cannot read ${agent} agent-native bundle journal: ${(error as Error).message}`);
  }
}

function readPendingAgentNativeBundleJournal(agent: "claude" | "codex"): AgentNativeBundleJournal | null {
  try {
    const value = JSON.parse(readFileSync(agentNativeBundleJournalPath(agent, true), "utf8")) as AgentNativeBundleJournal;
    if (value.schema_version !== 1 || value.agent !== agent || !value.cloud_mcp || !Array.isArray(value.skills)) {
      throw new Error("unsupported pending bundle journal");
    }
    return value;
  } catch (error) {
    if ((error as NodeJS.ErrnoException).code === "ENOENT") return null;
    throw new Error(`cannot read pending ${agent} agent-native bundle journal: ${(error as Error).message}`);
  }
}

function readPendingAgentNativeBundleRemoval(agent: "claude" | "codex"): AgentNativeBundleJournal | null {
  try {
    const value = JSON.parse(readFileSync(agentNativeBundleRemovalJournalPath(agent), "utf8")) as AgentNativeBundleJournal;
    if (value.schema_version !== 1 || value.agent !== agent || !value.cloud_mcp || !Array.isArray(value.skills)) {
      throw new Error("unsupported pending bundle removal journal");
    }
    return value;
  } catch (error) {
    if ((error as NodeJS.ErrnoException).code === "ENOENT") return null;
    throw new Error(`cannot read pending ${agent} agent-native removal journal: ${(error as Error).message}`);
  }
}

function readMcpServerMarker(agent: string, serverName: string): { command: string; args: string[] } | null {
  try {
    const value = JSON.parse(readFileSync(mcpServerMarkerPath(agent, serverName), "utf8")) as Record<string, unknown>;
    if (typeof value.command !== "string" || !Array.isArray(value.args) || !value.args.every((arg) => typeof arg === "string")) return null;
    return { command: value.command, args: value.args as string[] };
  } catch {
    return null;
  }
}

function claudeMcpRegistration(serverName: string): { present: boolean; command: string; args: string[]; exact_shape: boolean } {
  const path = join(homedir(), ".claude.json");
  const bytes = fileBytes(path);
  if (!bytes) return { present: false, command: "", args: [], exact_shape: false };
  const root = parseJsonFileObject(path, bytes);
  const servers = root.mcpServers && typeof root.mcpServers === "object" && !Array.isArray(root.mcpServers)
    ? root.mcpServers as Record<string, unknown>
    : {};
  if (!(serverName in servers)) return { present: false, command: "", args: [], exact_shape: false };
  const raw = servers[serverName];
  if (!raw || typeof raw !== "object" || Array.isArray(raw)) return { present: true, command: "", args: [], exact_shape: false };
  const entry = raw as Record<string, unknown>;
  const keys = Object.keys(entry).sort();
  return {
    present: true,
    command: typeof entry.command === "string" ? entry.command : "",
    args: Array.isArray(entry.args) && entry.args.every((arg) => typeof arg === "string") ? entry.args as string[] : [],
    exact_shape: keys.length === 2 && keys[0] === "args" && keys[1] === "command",
  };
}

function agentNativeCloudMcpMatches(agent: "claude" | "codex", mcp: { command: string; args: string[] }): boolean {
  if (agent === "codex") return codexMcpRegistrationMatches("caveman-cloud", mcp);
  const actual = claudeMcpRegistration("caveman-cloud");
  return actual.present && actual.exact_shape && actual.command === mcp.command && JSON.stringify(actual.args) === JSON.stringify(mcp.args);
}

function installAgentNativeCloudMcp(agent: "claude" | "codex", mcp: { command: string; args: string[] }): void {
  if (agentNativeCloudMcpMatches(agent, mcp)) {
    writeMcpServerMarker(agent, "caveman-cloud", mcp, "caveman_context");
    return;
  }
  const installed = agent === "claude"
    ? installMcpJson(join(homedir(), ".claude.json"), ["mcpServers", "caveman-cloud"], { command: mcp.command, args: mcp.args })
    : installMcpForAgent(findAgent(agent)!, mcp, "caveman-cloud");
  if (!installed) throw new Error(`could not install caveman-cloud MCP for ${agent}`);
  writeMcpServerMarker(agent, "caveman-cloud", mcp, "caveman_context");
}

function uninstallAgentNativeCloudMcp(agent: "claude" | "codex"): void {
  const removed = agent === "claude"
    ? removeMcpJson(join(homedir(), ".claude.json"), ["mcpServers", "caveman-cloud"])
    : uninstallMcpForAgent(findAgent(agent)!, "caveman-cloud");
  if (!removed) throw new Error(`could not remove caveman-cloud MCP for ${agent}`);
  try { unlinkSync(mcpServerMarkerPath(agent, "caveman-cloud")); } catch (error) {
    if ((error as NodeJS.ErrnoException).code !== "ENOENT") throw error;
  }
}

function agentNativeSkillFiles(agent: "claude" | "codex"): Array<{ name: string; file: string; body: string }> {
  const names = AGENT_SKILL_SUITES["agent-native"] ?? [];
  const root = agent === "claude" ? join(homedir(), ".claude", "skills") : join(homedir(), ".codex", "skills");
  return names.map((name) => ({ name, file: join(root, name, "SKILL.md"), body: SKILLS[name]! }));
}

function restoreAgentNativeBundleSkills(skills: AgentNativeBundleSkill[]): void {
  for (const skill of [...skills].reverse()) {
    const before = skill.before_base64 === null ? null : Buffer.from(skill.before_base64, "base64");
    if (before) atomicWriteFile(skill.file, before);
    else {
      try { unlinkSync(skill.file); } catch (error) {
        if ((error as NodeJS.ErrnoException).code !== "ENOENT") throw error;
      }
    }
  }
}

function restoreAgentNativeCloudMcp(agent: "claude" | "codex", previous: { command: string; args: string[] } | null): void {
  if (previous) {
    installAgentNativeCloudMcp(agent, previous);
    return;
  }
  uninstallAgentNativeCloudMcp(agent);
}

function sameMcpCommand(left: { command: string; args: string[] } | null, right: { command: string; args: string[] } | null): boolean {
  return left?.command === right?.command && JSON.stringify(left?.args ?? null) === JSON.stringify(right?.args ?? null);
}

function recoverPendingAgentNativeBundle(agent: "claude" | "codex"): void {
  const pending = readPendingAgentNativeBundleJournal(agent);
  if (!pending) return;
  // During an upgrade, the committed journal describes transaction-start
  // state. Keep accepting that exact state until the pending target commits.
  const committed = readAgentNativeBundleJournal(agent);
  const committedSkills = new Map(committed?.skills.map((skill) => [skill.file, skill.after_sha256]) ?? []);
  for (const skill of pending.skills) {
    const current = fileBytes(skill.file);
    const before = skill.before_base64 === null ? null : Buffer.from(skill.before_base64, "base64");
    const currentMatchesBefore = current === null ? before === null : before !== null && bytesHash(current) === bytesHash(before);
    const currentHash = current ? bytesHash(current) : null;
    const currentMatchesCommitted = currentHash !== null && currentHash === committedSkills.get(skill.file);
    if (!currentMatchesBefore && !currentMatchesCommitted && currentHash !== skill.after_sha256) {
      throw new Error(`${skill.file} changed during interrupted setup; refusing destructive recovery`);
    }
  }
  const marker = readMcpServerMarker(agent, "caveman-cloud");
  const markerKnown = !marker
    || sameMcpCommand(marker, pending.cloud_mcp)
    || sameMcpCommand(marker, pending.previous_cloud_mcp)
    || sameMcpCommand(marker, committed?.cloud_mcp ?? null);
  const hostInstalled = agentNativeCloudMcpMatches(agent, pending.cloud_mcp);
  const hostPrevious = pending.previous_cloud_mcp
    ? agentNativeCloudMcpMatches(agent, pending.previous_cloud_mcp)
    : agentNativeCloudMcpHostAbsent(agent);
  const hostCommitted = committed ? agentNativeCloudMcpMatches(agent, committed.cloud_mcp) : false;
  if (!markerKnown || (!hostInstalled && !hostPrevious && !hostCommitted)) {
    throw new Error(`${agent} caveman-cloud MCP changed during interrupted setup; refusing destructive recovery`);
  }
  restoreInstalledAgentNativeBundle(agent, pending);
  unlinkSync(agentNativeBundleJournalPath(agent, true));
  process.stderr.write(`${mark("warn")} completed interrupted ${agent} agent-native bundle before continuing\n`);
}

function skillMatchesBefore(skill: AgentNativeBundleSkill): boolean {
  const current = fileBytes(skill.file);
  const before = skill.before_base64 === null ? null : Buffer.from(skill.before_base64, "base64");
  return current === null ? before === null : before !== null && current.equals(before);
}

function agentNativeCloudMcpHostAbsent(agent: "claude" | "codex"): boolean {
  if (agent === "claude") return !claudeMcpRegistration("caveman-cloud").present;
  const config = fileBytes(join(homedir(), ".codex", "config.toml"))?.toString("utf8") ?? "";
  return !config.includes("[mcp_servers.caveman-cloud]");
}

function agentNativeCloudMcpAbsent(agent: "claude" | "codex"): boolean {
  return !readMcpServerMarker(agent, "caveman-cloud") && agentNativeCloudMcpHostAbsent(agent);
}

function agentNativeBundleRemovalCompleted(agent: "claude" | "codex", journal: AgentNativeBundleJournal): boolean {
  if (!journal.skills.every(skillMatchesBefore)) return false;
  const cloudRestored = journal.previous_cloud_mcp
    ? sameMcpCommand(readMcpServerMarker(agent, "caveman-cloud"), journal.previous_cloud_mcp)
      && agentNativeCloudMcpMatches(agent, journal.previous_cloud_mcp)
    : agentNativeCloudMcpAbsent(agent);
  if (!cloudRestored) return false;
  return !journal.native_owned || !readNativeJournal(agent);
}

function restoreInstalledAgentNativeBundle(agent: "claude" | "codex", journal: AgentNativeBundleJournal): void {
  ensureAgentNativeIntegration(agent);
  installAgentNativeCloudMcp(agent, journal.cloud_mcp);
  installAgentNativeBundleSkills(agent, journal);
  verifyAgentNativeBundle(agent, journal, journal.cloud_mcp);
  atomicWriteFile(agentNativeBundleJournalPath(agent), Buffer.from(JSON.stringify(journal, null, 2) + "\n"));
}

function recoverPendingAgentNativeRemoval(agent: "claude" | "codex"): void {
  const pending = readPendingAgentNativeBundleRemoval(agent);
  if (!pending) return;
  if (agentNativeBundleRemovalCompleted(agent, pending)) {
    try { unlinkSync(agentNativeBundleJournalPath(agent)); } catch { /* removal already committed */ }
    unlinkSync(agentNativeBundleRemovalJournalPath(agent));
    process.stderr.write(`${mark("warn")} completed interrupted ${agent} agent-native bundle removal\n`);
    return;
  }
  for (const skill of pending.skills) {
    const current = fileBytes(skill.file);
    if (current && bytesHash(current) !== skill.after_sha256 && !skillMatchesBefore(skill)) {
      throw new Error(`${skill.file} changed during interrupted removal; refusing destructive recovery`);
    }
  }
  const marker = readMcpServerMarker(agent, "caveman-cloud");
  const markerKnown = !marker || sameMcpCommand(marker, pending.cloud_mcp) || sameMcpCommand(marker, pending.previous_cloud_mcp);
  const hostIsInstalled = agentNativeCloudMcpMatches(agent, pending.cloud_mcp);
  const hostIsPrevious = pending.previous_cloud_mcp
    ? agentNativeCloudMcpMatches(agent, pending.previous_cloud_mcp)
    : agentNativeCloudMcpHostAbsent(agent);
  if (!markerKnown || (!hostIsInstalled && !hostIsPrevious)) {
    throw new Error(`${agent} caveman-cloud MCP changed during interrupted removal; refusing destructive recovery`);
  }
  restoreInstalledAgentNativeBundle(agent, pending);
  unlinkSync(agentNativeBundleRemovalJournalPath(agent));
  process.stderr.write(`${mark("warn")} rolled back interrupted ${agent} agent-native bundle removal before continuing\n`);
}

function preflightAgentNativeSetup(agent: "claude" | "codex"): void {
  recoverPendingAgentNativeRemoval(agent);
  recoverPendingAgentNativeBundle(agent);
  const profile = findAgent(agent)!;
  if (!which(binOf(profile))) throw new Error(`${profile.display_name} not found on PATH`);
  const mcpBinary = nativeMcpBinaryRequired();
  const gw = gatewayURL();
  nativeProxyBinaryRequired(gw);
  withIntegrationLock(agent, () => recoverPendingNativeInstallUnlocked(agent));
  const status = nativeIntegrationStatus(agent);
  if (!status.installed) nativeMutationsFor(agent, gw, mcpBinary);
}

function ensureAgentNativeIntegration(agent: "claude" | "codex"): void {
  const status = nativeIntegrationStatus(agent);
  if (!status.installed) {
    enableNative([agent]);
    return;
  }
  if (status.state !== "installed") repairNativeAgent(agent);
}

function preflightAgentNativeBundleComponents(
  agent: "claude" | "codex",
  existingBundle: AgentNativeBundleJournal | null,
): void {
  const previousSkills = new Map(existingBundle?.skills.map((skill) => [skill.file, skill]) ?? []);
  for (const { file, body } of agentNativeSkillFiles(agent)) {
    const current = fileBytes(file);
    if (!existingBundle) {
      if (current && !current.equals(Buffer.from(body))) {
        throw new Error(`${file} already exists with non-canonical content; refusing to overwrite an unjournaled skill`);
      }
      continue;
    }
    const previous = previousSkills.get(file);
    if (current && !current.equals(Buffer.from(body)) && (!previous || bytesHash(current) !== previous.after_sha256)) {
      throw new Error(`${file} changed after setup; refusing to overwrite user skill edits`);
    }
  }
  if (agent === "claude") {
    const actual = claudeMcpRegistration("caveman-cloud");
    const marker = readMcpServerMarker("claude", "caveman-cloud");
    if (actual.present && !marker) {
      throw new Error("Claude caveman-cloud MCP exists but is not Caveman-journaled; refusing overwrite");
    }
    if (actual.present && marker && !agentNativeCloudMcpMatches("claude", marker)) {
      throw new Error("Claude caveman-cloud MCP changed after setup; refusing to overwrite user fields");
    }
    return;
  }
  const config = fileBytes(join(homedir(), ".codex", "config.toml"))?.toString("utf8") ?? "";
  if (!config.includes("[mcp_servers.caveman-cloud]")) return;
  if (!readMcpServerMarker("codex", "caveman-cloud")) {
    throw new Error("[mcp_servers.caveman-cloud] exists but is not Caveman-journaled; refusing overwrite");
  }
}

function installAgentNativeBundleSkills(agent: "claude" | "codex", journal: AgentNativeBundleJournal): void {
  const originals = new Map(journal.skills.map((skill) => [skill.file, skill.before_base64]));
  journal.skills = agentNativeSkillFiles(agent).map(({ file, body }) => {
    mkdirSync(dirname(file), { recursive: true });
    atomicWriteFile(file, Buffer.from(body));
    return {
      file,
      before_base64: originals.get(file) ?? null,
      after_sha256: bytesHash(Buffer.from(body)),
    };
  });
}

function verifyAgentNativeBundle(agent: "claude" | "codex", journal: AgentNativeBundleJournal, cloudMcp: { command: string; args: string[] }): void {
  const status = nativeIntegrationStatus(agent);
  if (status.state !== "installed") throw new Error(`${agent} native integration postflight is ${status.state}`);
  const marker = readMcpServerMarker(agent, "caveman-cloud");
  if (!marker || marker.command !== cloudMcp.command || JSON.stringify(marker.args) !== JSON.stringify(cloudMcp.args)) {
    throw new Error(`${agent} caveman-cloud MCP postflight mismatch`);
  }
  if (!agentNativeCloudMcpMatches(agent, cloudMcp)) {
    throw new Error(`${agent} caveman-cloud MCP registration failed exact postflight`);
  }
  for (const skill of journal.skills) {
    const current = fileBytes(skill.file);
    if (!current || bytesHash(current) !== skill.after_sha256) throw new Error(`${skill.file} failed skill postflight`);
  }
}

function removeAgentNativeBundle(agent: "claude" | "codex"): void {
  recoverPendingAgentNativeRemoval(agent);
  recoverPendingAgentNativeBundle(agent);
  const journal = readAgentNativeBundleJournal(agent);
  if (!journal) {
    process.stderr.write(`${mark("warn")} ${agent}: no agent-native bundle journal found\n`);
    return;
  }
  for (const skill of journal.skills) {
    const current = fileBytes(skill.file);
    if (!current || bytesHash(current) !== skill.after_sha256) throw new Error(`${skill.file} changed after setup; refusing destructive bundle removal`);
  }
  const marker = readMcpServerMarker(agent, "caveman-cloud");
  if (!sameMcpCommand(marker, journal.cloud_mcp) || !agentNativeCloudMcpMatches(agent, journal.cloud_mcp)) {
    throw new Error(`${agent} caveman-cloud MCP changed after setup; refusing destructive bundle removal`);
  }
  if (journal.native_owned) {
    const nativeJournal = readNativeJournal(agent);
    if (nativeJournal) {
      for (const operation of nativeJournal.operations) restoreNativeOperation(operation);
    }
  }
  atomicWriteFile(agentNativeBundleRemovalJournalPath(agent), Buffer.from(JSON.stringify(journal, null, 2) + "\n"));
  try {
    restoreAgentNativeBundleSkills(journal.skills);
    restoreAgentNativeCloudMcp(agent, journal.previous_cloud_mcp);
    if (journal.native_owned) disableNativeAgent(agent);
    unlinkSync(agentNativeBundleJournalPath(agent));
    unlinkSync(agentNativeBundleRemovalJournalPath(agent));
  } catch (error) {
    try {
      restoreInstalledAgentNativeBundle(agent, journal);
      unlinkSync(agentNativeBundleRemovalJournalPath(agent));
    } catch (rollback) {
      throw new Error(`agent-native bundle removal failed: ${(error as Error).message}; rollback incomplete: ${(rollback as Error).message}`);
    }
    throw new Error(`agent-native bundle removal failed: ${(error as Error).message}; installed bundle restored`);
  }
  process.stderr.write(`${mark("ok")} ${agent}: agent-native bundle removed; prior skills and cloud MCP restored\n`);
}

async function setup(argv: string[] = []) {
  const json = argv.includes("--json");
  const install = argv.includes("--install");
  const removeBundle = argv.includes("--remove");
  const agentNative = flagFrom(argv, "--agent-native", "");
  const hasAgentNativeFlag = argv.includes("--agent-native") || argv.some((arg) => arg.startsWith("--agent-native="));
  const unknown = argv.filter((arg, index) =>
    arg !== "--json"
    && arg !== "--install"
    && arg !== "--remove"
    && arg !== "--agent-native"
    && !arg.startsWith("--agent-native=")
    && argv[index - 1] !== "--agent-native");
  if (unknown.length > 0 || (hasAgentNativeFlag && !agentNative) || (agentNative && (json || install)) || (removeBundle && !agentNative)) {
    commandUsage("setup [--install] [--json] | setup --agent-native <claude|codex> [--remove]");
  }
  if (agentNative) {
    if (agentNative !== "claude" && agentNative !== "codex") {
      console.error(`caveman setup: --agent-native must be claude or codex (got ${agentNative})`);
      process.exit(2);
    }
    return withIntegrationLock(`agent-native-bundle-${agentNative}`, () => {
      if (removeBundle) {
        removeAgentNativeBundle(agentNative);
        return;
      }
      preflightAgentNativeSetup(agentNative);
      const existingBundle = readAgentNativeBundleJournal(agentNative);
      const cloudMcp = resolveCloudMcpCommand();
      preflightAgentNativeBundleComponents(agentNative, existingBundle);
      const nativeWasInstalled = nativeIntegrationStatus(agentNative).installed;
      const rollbackCloudMcp = readMcpServerMarker(agentNative, "caveman-cloud");
      const rollbackSkills = agentNativeSkillFiles(agentNative).map(({ file, body }) => ({
        file,
        before_base64: fileBytes(file)?.toString("base64") ?? null,
        after_sha256: bytesHash(Buffer.from(body)),
      }));
      const originalSkills = new Map((existingBundle?.skills ?? rollbackSkills).map((skill) => [skill.file, skill.before_base64]));
      const journalSkills = agentNativeSkillFiles(agentNative).map(({ file, body }) => ({
        file,
        before_base64: originalSkills.get(file) ?? null,
        after_sha256: bytesHash(Buffer.from(body)),
      }));
      const journal: AgentNativeBundleJournal = {
        schema_version: 1,
        agent: agentNative,
        pack_version: NATIVE_PACK.version,
        native_owned: existingBundle?.native_owned ?? !nativeWasInstalled,
        cloud_mcp: cloudMcp,
        previous_cloud_mcp: existingBundle ? existingBundle.previous_cloud_mcp : rollbackCloudMcp,
        skills: journalSkills,
      };
      atomicWriteFile(agentNativeBundleJournalPath(agentNative, true), Buffer.from(JSON.stringify(journal, null, 2) + "\n"));
      let cloudMcpApplied = false;
      try {
        ensureAgentNativeIntegration(agentNative);
        installAgentNativeCloudMcp(agentNative, cloudMcp);
        cloudMcpApplied = true;
        installAgentNativeBundleSkills(agentNative, journal);
        verifyAgentNativeBundle(agentNative, journal, cloudMcp);
        atomicWriteFile(agentNativeBundleJournalPath(agentNative), Buffer.from(JSON.stringify(journal, null, 2) + "\n"));
        unlinkSync(agentNativeBundleJournalPath(agentNative, true));
      } catch (error) {
        const rollbackErrors: string[] = [];
        try { restoreAgentNativeBundleSkills(rollbackSkills); } catch (rollback) { rollbackErrors.push((rollback as Error).message); }
        if (cloudMcpApplied) {
          try { restoreAgentNativeCloudMcp(agentNative, rollbackCloudMcp); } catch (rollback) { rollbackErrors.push((rollback as Error).message); }
        }
        if (!nativeWasInstalled) {
          try { disableNativeAgent(agentNative); } catch (rollback) { rollbackErrors.push((rollback as Error).message); }
        }
        try { unlinkSync(agentNativeBundleJournalPath(agentNative, true)); } catch { /* original error remains authority */ }
        throw new Error(`agent-native setup failed: ${(error as Error).message}${rollbackErrors.length ? `; rollback incomplete: ${rollbackErrors.join("; ")}` : "; changes rolled back"}`);
      }
      const coreState = nativeCoreRuntimeState();
      const coreLabel = coreState.active ? "on" : coreState.configured ? "configured on; inactive under record mode/profile" : "off";
      console.error(`${mark("ok")} ${agentNative}: complete agent-native bundle ready`);
      console.error(dim(`→ coding policy: Core ${NATIVE_PACK.version} ${coreLabel}; change with \`caveman tools config set think.core ${coreState.configured ? "off" : "on"}\`; start new session to clear delivered context`));
      console.error(dim(`→ remove complete bundle: \`caveman setup --agent-native ${agentNative} --remove\``));
      console.error(dim("→ log in with `caveman login`; agent reads project context through existing CLI credentials"));
    });
  }
  if (install) return setupInstall(json);

  const rows = GO_BINARIES.map((b) => ({ ...b, resolved: resolveGoBin(b.name, b.env) }));
  const missingRequired = rows.filter((r) => r.required && !r.resolved);

  if (json) {
    print({
      binaries: rows.map((row) => ({
        name: row.name,
        required: row.required,
        path: row.resolved,
        powers: row.powers,
        without: row.resolved ? null : row.without,
      })),
      ready: missingRequired.length === 0,
    });
    if (missingRequired.length > 0) process.exit(1);
    return;
  }

  console.log(bold("caveman setup — Go binary status"));
  console.log(dim("The CLI itself is plain JS; compression/metering run in these binaries."));
  console.log("");
  for (const r of rows) {
    if (r.resolved) {
      console.log(`${mark("ok")} ${r.name.padEnd(15)} ${dim(r.resolved)}`);
      console.log(`    powers: ${r.powers}`);
    } else {
      console.log(`${mark(r.required ? "bad" : "warn")} ${r.name.padEnd(15)} missing${r.required ? "" : dim(" (optional)")}`);
      console.log(`    without it: ${r.without}`);
    }
  }
  console.log("");
  if (missingRequired.length === 0) {
    console.log(`${mark("ok")} All required binaries found. Try: ${cyan("caveman claude")}`);
    return;
  }
  console.log(`${mark("warn")} ${missingRequired.length} of ${rows.filter((r) => r.required).length} required binaries missing — affected commands run as loud, byte-safe`);
  console.log(`   pass-throughs: nothing is compressed, savings honestly report 0.`);
  console.log(`   Connected verbs (login, plan, score, costs, …) work regardless — they only need HTTP.`);
  console.log("");
  console.log(`Get the signed binaries:`);
  console.log(`  ${cyan("caveman setup --install")}`);
  console.log(`Already installed elsewhere? Point at them: ${dim("export CAVEMAN_PROXY_BIN=/path/to/caveman-proxy")} (same for _ENGINE_/_MCP_/_BROWSE_)`);
  console.log(`Lookup order: env override → PATH → ${dim(join(cavemanHome(), "bin"))}`);
  process.exit(1);
}

type WrapRuntimeMode = "compress" | "record" | "pixel";
// How much MCP surface `caveman wrap` injects into the agent — the caveman
// side of the prompt prefix, and a measured tax: the five engine MCP tools cost
// ~11k tokens of tool schema on every single call.
//
//   auto        — install the caveman MCP server when a real binary resolves
//                 (today's behavior); the agent gets caveman_retrieve and the
//                 proxy may compress streams marker-only.
//   marker-only — inject NOTHING. Wrap stops managing the MCP surface: it does
//                 not install, and it does not uninstall what is already there.
//                 Recovery is then answered purely by what the agent actually
//                 has (see wrapMcpRecoveryAvailable), so the proxy is never told
//                 a retrieval tool exists when none does.
//   off         — the pre-existing `execute.mcp: false`; same install behavior
//                 as marker-only, kept as its own name for config compatibility.
type McpSurfaceMode = "auto" | "marker-only" | "off";
type WrapOptions = { mode: WrapRuntimeMode; noProxy: boolean; toon: boolean; pixelModels?: string; pixelDensity?: string; noShrink: boolean; mcpMode: McpSurfaceMode; noBrowse: boolean; delegate: boolean; minimal: boolean; autoRecall?: boolean; workflow?: string; command: string[] };
type CapabilitySource = "default" | "proxy-yaml" | "legacy-wrap" | "global" | "project" | "env";
type CapabilityKey =
  | "think.mode"
  | "think.core"
  | "think.toon"
  | "think.shrink"
  | "think.pixel.models"
  | "think.pixel.density"
  | "remember.mem"
  | "remember.offload"
  | "remember.recall"
  | "execute.mcp"
  | "execute.browse_tool"
  | "execute.browse_cli"
  | "execute.delegate"
  | "execute.proxy";
type CapabilityValue = string | boolean | string[];
type ResolvedCapability = { value: CapabilityValue; source: CapabilitySource; invalid?: string };
type CapabilityResolution = {
  values: Record<CapabilityKey, ResolvedCapability>;
  projectActive: boolean;
  legacyIgnored: CapabilityKey[];
};
type WrapConfig = {
  mode: WrapRuntimeMode;
  core: boolean;
  toon: boolean;
  shrink: boolean;
  mcp: McpSurfaceMode;
  browse: boolean;
  delegate: boolean;
  autoRecall: boolean;
  proxy: boolean;
  pixelModels?: string;
  pixelDensity?: string;
  resolution: CapabilityResolution;
};
type CodexWrapAuthMode = "api-key" | "subscription";

const CAPABILITY_KEYS: CapabilityKey[] = [
  "think.mode",
  "think.core",
  "think.toon",
  "think.shrink",
  "think.pixel.models",
  "think.pixel.density",
  "remember.mem",
  "remember.offload",
  "remember.recall",
  "execute.mcp",
  "execute.browse_tool",
  "execute.browse_cli",
  "execute.delegate",
  "execute.proxy",
];

const CAPABILITY_DEFAULTS: Record<CapabilityKey, CapabilityValue> = {
  "think.mode": "compress",
  "think.core": true,
  "think.toon": true,
  "think.shrink": true,
  "think.pixel.models": [],
  "think.pixel.density": "balanced",
  "remember.mem": true,
  "remember.offload": "auto",
  "remember.recall": false,
  "execute.mcp": "auto",
  "execute.browse_tool": true,
  "execute.browse_cli": false,
  "execute.delegate": false,
  "execute.proxy": true,
};

const LEGACY_CAPABILITY_MAP: Record<string, CapabilityKey> = {
  mode: "think.mode",
  toon: "think.toon",
  shrink: "think.shrink",
  pixel_models: "think.pixel.models",
  pixel_density: "think.pixel.density",
  auto_recall: "remember.recall",
  mcp: "execute.mcp",
  browse: "execute.browse_tool",
  proxy: "execute.proxy",
};

function wrapModeValue(value: unknown): WrapRuntimeMode | undefined {
  return value === "compress" || value === "record" || value === "pixel" ? value : undefined;
}

function wrapBoolValue(value: unknown): boolean | undefined {
  if (typeof value === "boolean") return value;
  if (typeof value !== "string") return undefined;
  const v = value.trim().toLowerCase();
  if (["1", "true", "yes", "on"].includes(v)) return true;
  if (["0", "false", "no", "off"].includes(v)) return false;
  return undefined;
}

function objectValue(value: unknown): Record<string, unknown> {
  return value && typeof value === "object" && !Array.isArray(value) ? value as Record<string, unknown> : {};
}

function globalCapabilityDocument(): Record<string, unknown> {
  try {
    const parsed = JSON.parse(readFileSync(configPath(), "utf8")) as unknown;
    return objectValue(parsed);
  } catch {
    return {};
  }
}

function capabilityInputValue(key: CapabilityKey, value: unknown): CapabilityValue | undefined {
  if (key === "think.mode") return wrapModeValue(value);
  if (key === "think.pixel.models") {
    if (Array.isArray(value) && value.every((entry) => typeof entry === "string")) return value as string[];
    if (typeof value === "string") return value.split(",").map((entry) => entry.trim()).filter(Boolean);
    return undefined;
  }
  if (key === "think.pixel.density") {
    return value === "conservative" || value === "balanced" || value === "max" ? value : undefined;
  }
  if (key === "remember.offload") {
    return value === "auto" || value === "on" || value === "off" ? value : undefined;
  }
  // execute.mcp is the one four-valued knob: two named modes plus the boolean
  // pair. Booleans keep their boolean shape so existing config files and
  // `config get` output are unchanged; anything unrecognized returns undefined,
  // which leaves the key at its previous (default "auto") value rather than
  // silently inventing a surface.
  if (key === "execute.mcp" && (value === "auto" || value === "marker-only")) return value;
  return wrapBoolValue(value);
}

// mcpSurfaceMode collapses the stored execute.mcp value into the three-way mode
// the wrap path acts on. Only the four values capabilityInputValue accepts can
// reach it. It decides how much surface we INJECT — never what we tell the
// proxy about recovery, which is answered from real install evidence.
function mcpSurfaceMode(value: CapabilityValue): McpSurfaceMode {
  if (value === false) return "off";
  if (value === "marker-only") return "marker-only";
  return "auto";
}

const MCP_SURFACE_VALUES = "auto | marker-only | true | false";

function nestedCapabilityValue(doc: Record<string, unknown>, key: CapabilityKey): unknown {
  const [group, first, second] = key.split(".");
  const block = objectValue(doc[group!]);
  if (!second) return block[first!];
  return objectValue(block[first!])[second];
}

function proxyYamlMode(): unknown {
  const path = process.env.CAVEMAN_CONFIG ?? join(cavemanHome(), "caveman.yaml");
  try {
    const raw = readFileSync(path, "utf8");
    const match = raw.match(/^\s*mode\s*:\s*["']?([^#\s"']+)/m);
    return match?.[1];
  } catch {
    return undefined;
  }
}

function projectCapabilityDocument(): Record<string, unknown> {
  try {
    return objectValue(JSON.parse(readFileSync(join(process.cwd(), ".caveman", "config.json"), "utf8")));
  } catch {
    return {};
  }
}

function resolveCapabilities({ forStart = false }: { forStart?: boolean } = {}): CapabilityResolution {
  const values = Object.fromEntries(CAPABILITY_KEYS.map((key) => [
    key,
    { value: CAPABILITY_DEFAULTS[key], source: "default" as CapabilitySource },
  ])) as Record<CapabilityKey, ResolvedCapability>;
  const legacyIgnored: CapabilityKey[] = [];
  const apply = (key: CapabilityKey, raw: unknown, source: CapabilitySource) => {
    if (raw === undefined) return;
    const parsed = capabilityInputValue(key, raw);
    if (parsed !== undefined) {
      values[key] = { value: parsed, source };
      return;
    }
    if (key === "think.mode") {
      values[key] = { value: "record", source, invalid: String(raw) };
    }
  };

  if (forStart) apply("think.mode", proxyYamlMode(), "proxy-yaml");

  const global = globalCapabilityDocument();
  const legacy = objectValue(global.wrap);
  for (const [legacyKey, key] of Object.entries(LEGACY_CAPABILITY_MAP)) {
    apply(key, legacy[legacyKey], "legacy-wrap");
  }
  for (const key of CAPABILITY_KEYS) {
    const raw = nestedCapabilityValue(global, key);
    if (raw !== undefined) {
      if (values[key].source === "legacy-wrap") legacyIgnored.push(key);
      apply(key, raw, "global");
    }
  }

  let projectActive = false;
  if (!forStart) {
    const project = projectCapabilityDocument();
    const allowlisted = CAPABILITY_KEYS.filter((key) =>
      key === "think.toon"
      || key === "think.shrink"
      || key.startsWith("remember.")
      || key.startsWith("execute."));
    for (const key of allowlisted) {
      const raw = nestedCapabilityValue(project, key);
      if (raw === undefined) continue;
      const before = values[key];
      apply(key, raw, "project");
      if (values[key] !== before) projectActive = true;
    }
  }

  const envMode = forStart
    ? process.env.CAVEMAN_MODE ?? process.env.CAVEMAN_WRAP_MODE
    : process.env.CAVEMAN_WRAP_MODE;
  apply("think.mode", envMode, "env");
  apply("think.core", process.env.CAVEMAN_CORE, "env");
  apply("think.toon", process.env.CAVEMAN_TOON, "env");
  apply("think.shrink", process.env.CAVEMAN_SHRINK, "env");
  apply("execute.mcp", process.env.CAVEMAN_MCP, "env");
  apply("think.pixel.models", process.env.CAVE_PIXEL_MODELS, "env");
  apply("think.pixel.density", process.env.CAVE_PIXEL_DENSITY, "env");

  return { values, projectActive, legacyIgnored };
}

function wrapRuntimeConfig(options: { forStart?: boolean } = {}): WrapConfig {
  const resolution = resolveCapabilities(options);
  const value = <T extends CapabilityValue>(key: CapabilityKey) => resolution.values[key].value as T;
  const models = value<string[]>("think.pixel.models");
  return {
    mode: value<WrapRuntimeMode>("think.mode"),
    core: value<boolean>("think.core"),
    toon: value<boolean>("think.toon"),
    shrink: value<boolean>("think.shrink"),
    mcp: mcpSurfaceMode(value<boolean | string>("execute.mcp")),
    browse: value<boolean>("execute.browse_tool"),
    delegate: value<boolean>("execute.delegate"),
    autoRecall: value<boolean>("remember.recall"),
    proxy: value<boolean>("execute.proxy"),
    ...(models.length ? { pixelModels: models.join(",") } : {}),
    pixelDensity: value<string>("think.pixel.density"),
    resolution,
  };
}

function invalidModeLine(resolution: CapabilityResolution): string | undefined {
  const mode = resolution.values["think.mode"];
  return mode.invalid === undefined
    ? undefined
    : OFF_STATES.invalidMode(mode.invalid).line;
}

function capabilityDisplayValue(value: CapabilityValue): string {
  return Array.isArray(value) ? JSON.stringify(value) : String(value);
}

function printCapability(key: CapabilityKey, resolved: ResolvedCapability): void {
  console.log(`${key} = ${capabilityDisplayValue(resolved.value)}  (${resolved.source})`);
}

function setGlobalCapability(key: CapabilityKey, value: CapabilityValue): void {
  mutateRawConfig((out) => {
    const [groupName, first, second] = key.split(".");
    const group = { ...objectValue(out[groupName!]) };
    if (second) {
      const nested = { ...objectValue(group[first!]) };
      nested[second] = value;
      group[first!] = nested;
    } else {
      group[first!] = value;
    }
    out[groupName!] = group;
  });
}

function capabilityConfigCommand(argv: string[]): void {
  const sub = argv[0];
  if (sub === "path") {
    console.log(configPath());
    return;
  }
  if (sub === "get") {
    const requested = argv[1];
    if (requested && !CAPABILITY_KEYS.includes(requested as CapabilityKey)) {
      console.error(`unknown config key: ${requested}`);
      process.exit(2);
    }
    const resolution = resolveCapabilities();
    if (requested) {
      const key = requested as CapabilityKey;
      printCapability(key, resolution.values[key]);
      return;
    }
    for (const key of CAPABILITY_KEYS) printCapability(key, resolution.values[key]);
    return;
  }
  if (sub === "set") {
    const rawKey = argv[1] ?? "";
    const rawValue = argv[2];
    if (!CAPABILITY_KEYS.includes(rawKey as CapabilityKey)) {
      console.error(`config key not settable: ${rawKey}`);
      process.exit(2);
    }
    if (rawValue === undefined) {
      console.error(`usage: ${invokedCommand("config")} set <key> <value>`);
      process.exit(2);
    }
    const key = rawKey as CapabilityKey;
    const parsed = capabilityInputValue(key, rawValue);
    if (parsed === undefined) {
      if (key === "think.mode") {
        console.error(`not a mode: "${rawValue}" — valid: compress | record | pixel`);
      } else if (key === "execute.mcp") {
        console.error(`not an MCP surface: "${rawValue}" — valid: ${MCP_SURFACE_VALUES}`);
      } else {
        console.error(`not a valid value for ${key}: ${rawValue}`);
      }
      process.exit(2);
    }
    setGlobalCapability(key, parsed);
    const resolved = resolveCapabilities().values[key];
    printCapability(key, resolved);
    return;
  }
  console.error(`usage: ${invokedAs()} tools config get|set|path`);
  process.exit(2);
}

function defaultWrapOptions(): WrapOptions {
  const cfg = wrapRuntimeConfig();
  const invalidMode = invalidModeLine(cfg.resolution);
  if (invalidMode) process.stderr.write(`${invalidMode}\n`);
  const opts: WrapOptions = {
    mode: cfg.mode,
    noProxy: !cfg.proxy,
    toon: cfg.toon,
    noShrink: !cfg.shrink,
    mcpMode: cfg.mcp,
    noBrowse: !cfg.browse,
    delegate: cfg.delegate,
    minimal: false,
    autoRecall: cfg.autoRecall,
    command: [],
  };
  if (cfg.pixelModels !== undefined) opts.pixelModels = cfg.pixelModels;
  if (cfg.pixelDensity !== undefined) opts.pixelDensity = cfg.pixelDensity;
  if (opts.mode !== "compress") opts.toon = false;
  return opts;
}

function wrapCompressEnabled(opts: WrapOptions): boolean {
  return opts.mode === "compress";
}

function wrapRecoveryEligible(opts: WrapOptions): boolean {
  return opts.mode === "compress" || opts.mode === "pixel";
}

// ── Wrap entitlement: an ACCOUNT fact, never a compression gate ──────
// Local compression is the free adoption surface: it runs with no Caveman account,
// no entitlement, and no seat. The entitlement read here says what the ACCOUNT
// earns — analytics, team/seats, and cloud sync — and it never decides whether the
// local proxy compresses. The only things that can withhold local compression are
// the user's own `record`/`--off` request and the proxy's technical conditions
// (schema-aware prefix zones, MCP recovery, a durable prefix cache, and the
// operator's `subscription_compress` switch).

export type WrapEntitlement = {
  entitled: boolean;
  plan: string;
  telemetry_level: string;
  seats_used: number;
  seats_limit: number | null;
  devices_used: number;
  devices_limit: number;
  evicted_device_hash: string | null;
  expires_at: string;
  optimized_tokens_week?: number;
  weekly_reset_at?: string;
};

type WrapEntitlementState = {
  kind: "ok" | "seat-wall" | "denied" | "unverified";
  at: string;
  seats_used?: number;
  seats_limit?: number;
};

// The reason is an ACCOUNT label on an already-decided mode, not a permission:
// "unentitled" compresses exactly like "entitled" does.
export type WrapGateReason =
  | "user-record"
  | "entitled"
  | "grace"
  | "unentitled";
export type WrapGate = { mode: WrapRuntimeMode; estimate: boolean; reason: WrapGateReason };

const WRAP_GRACE_MS = 7 * 24 * 60 * 60 * 1000;

type OffStateID =
  | "binary-missing"
  | "foreign-process"
  | "running-mode-mismatch"
  | "running-gate-mismatch"
  | "invalid-mode"
  | "user-record"
  | "weekly-cap"
  | "mcp-missing"
  | "mem-missing"
  | "zdr"
  | "stale-binary"
  | "download-unreachable"
  | "download-stalled"
  | "unsupported-platform"
  | "refresh-offline";

export type OffState = { id: OffStateID; line: string; fix?: string };

// One source of truth for every R-203 line. Run prints the first blocking row;
// status prints every active row in OFF_STATE_PRECEDENCE order.
//
// Every row here is a TECHNICAL reason compression is off or degraded. Account
// state is NOT one of them: being signed out, seat-walled, denied, or
// lapsed never turns local compression off, so none of those may appear here.
const MCP_MARKER_ONLY_LINE =
  "MCP surface marker-only by your config — the engine MCP tools are not injected; streaming turns and Claude Pro/Max sessions pass through uncompressed (non-streaming API-key traffic still compresses)";

export const OFF_STATES = {
  weeklyCap: (used: string, allowance: string): OffState => ({
    id: "weekly-cap",
    line: `weekly plan cap reached — connected traffic returns 429 until Monday 00:00 UTC; local wrap is unaffected (${used} of ${allowance} optimized tokens this week)`,
    fix: "caveman cloud billing",
  }),
  invalidMode: (value: string): OffState => ({
    id: "invalid-mode",
    line: `think.mode "${value}" is not a valid mode — running record (pass-through)`,
    fix: "caveman tools config get think.mode",
  }),
  userRecord: {
    line: "record mode — pass-through by your config, nothing is compressed",
    fix: "caveman tools config set think.mode compress",
  },
  binaryMissing: {
    line: "caveman-proxy not installed — agents still launch, traffic is NOT compressed or metered",
    fix: "caveman setup --install",
  },
  runningModeMismatch: (running: string, resolvedMode: string): OffState => ({
    id: "running-mode-mismatch",
    line: `a caveman proxy is already running in ${running} mode — this session is not compressed; the next run restarts it to pick up ${resolvedMode}`,
    fix: "caveman run -- <your agent>",
  }),
  runningModeHeld: (running: string, resolvedMode: string): OffState => ({
    id: "running-mode-mismatch",
    line: `a caveman proxy is already running in ${running} mode — another live session holds it, so this session keeps that mode instead of restarting to ${resolvedMode}`,
    fix: "caveman run -- <your agent>",
  }),
  runningGateMismatch: {
    id: "running-gate-mismatch",
    line: "running caveman proxy has stale recovery state — launching this agent direct to avoid unsafe compression",
    fix: "stop other wrapped sessions, then run this command again",
  },
  runningGateHeld: {
    id: "running-gate-mismatch",
    line: "another live session holds a caveman proxy with different recovery state — launching this agent direct to avoid unsafe compression",
    fix: "stop other wrapped sessions, then run this command again",
  },
  foreignProcess: (host: string, port: number): OffState => ({
    id: "foreign-process",
    line: `something else is listening on ${host}:${port} — this session is not compressed; caveman will not restart a process it does not own`,
  }),
  downloadUnreachable: {
    line: "binary download unreachable — agents still launch; traffic is NOT compressed or metered",
    fix: "caveman setup --install",
  },
  downloadStalled: (seconds: number): OffState => ({
    id: "download-stalled",
    line: `binary download stalled after ${seconds}s — nothing installed; agents still launch, traffic is NOT compressed or metered`,
    fix: "caveman setup --install",
  }),
  unsupportedPlatform: (os: string, arch: string): OffState => ({
    id: "unsupported-platform",
    line: `no prebuilt binary for ${os}/${arch} — supported: darwin/arm64, darwin/amd64, linux/arm64, linux/amd64, win32/arm64, win32/amd64`,
  }),
  memMissing: {
    line: "cavemem not installed — memory and auto-recall are off",
    fix: "caveman setup --install",
  },
  mcpMissing: {
    line: "MCP recovery missing — streaming turns and Claude Pro/Max sessions pass through uncompressed (non-streaming API-key traffic still compresses)",
    fix: "caveman tools mcp install <agent>",
  },
  // Same technical consequence as mcpMissing, different cause: nothing is
  // missing, the operator asked for the smaller prefix. Two remedies, because the
  // two doors are not the same machine:
  //   - wrap knows the agent and auto-installs, so flipping the surface back to
  //     auto genuinely restores recovery on the next run;
  //   - status/start are agent-less and install nothing, so `config set` alone
  //     would be an inert remedy there — name the install command instead.
  // Both share the mcp-missing id, so OFF_STATE_PRECEDENCE is unchanged.
  mcpMarkerOnly: {
    line: MCP_MARKER_ONLY_LINE,
    fix: "caveman tools config set execute.mcp auto",
  },
  mcpMarkerOnlyStandalone: {
    line: MCP_MARKER_ONLY_LINE,
    fix: "caveman tools mcp install <agent>",
  },
  staleBinary: (binary: string, found: string, expected: string): OffState => ({
    id: "stale-binary",
    line: `${binary} ${found} is older than ${expected} — update before compressing`,
    fix: "caveman setup --install",
  }),
  refreshOffline: {
    line: "account refresh offline — cloud sync and seat state may be stale; local compression is unaffected",
  },
  zdr: {
    line: "ZDR org — wrap telemetry excluded by your data policy; local numbers only",
  },
} as const;

const OFF_STATE_PRECEDENCE: OffStateID[] = [
  "binary-missing",
  "foreign-process",
  "running-mode-mismatch",
  "invalid-mode",
  "user-record",
  "weekly-cap",
  "mcp-missing",
  "mem-missing",
  "zdr",
  "stale-binary",
];

function fixedOffState(id: OffStateID, item: { line: string; fix?: string }): OffState {
  return { id, line: item.line, ...(item.fix ? { fix: item.fix } : {}) };
}

const pendingRunOffStates: OffState[] = [];

function queueRunOffState(state: OffState): void {
  const index = pendingRunOffStates.findIndex((item) => item.id === state.id);
  if (index >= 0) pendingRunOffStates[index] = state;
  else pendingRunOffStates.push(state);
}

function takeRunOffStates(): OffState[] {
  return pendingRunOffStates.splice(0);
}

function capabilityProjectGroups(): string[] {
  const doc = projectCapabilityDocument();
  return ["think", "remember", "execute"].filter((group) => {
    const block = objectValue(doc[group]);
    return Object.keys(block).length > 0;
  });
}

function orderedOffStates(states: OffState[]): OffState[] {
  const rank = new Map(OFF_STATE_PRECEDENCE.map((id, index) => [id, index]));
  return [...states].sort((a, b) => (rank.get(a.id) ?? 999) - (rank.get(b.id) ?? 999));
}

function printRunBanner(options: {
  agent: AgentProfile | undefined;
  binary: string;
  runningMode: string | null;
  states: OffState[];
  projectGroups: string[];
}): void {
  const name = options.agent?.display_name ?? basename(options.binary);
  process.stderr.write(dim(`caveman · ${options.runningMode ?? "owner: unknown"} · ${name}\n`));
  const first = orderedOffStates(options.states)[0];
  if (first) process.stderr.write(dim(`${first.line}\n`));
  if (options.projectGroups.length > 0) {
    process.stderr.write(dim(`project overlay active — ./.caveman/config.json is setting ${options.projectGroups.join(", ")}\n`));
  }
  process.stderr.write(dim("watching…\n"));
}

// resolveWrapGate is the PURE mode decision (no IO) so it is unit-testable.
// `requestedMode` is what the user asked for; the returned `mode` is what the local
// proxy actually runs. The ENTITLEMENT NEVER CHANGES THE MODE — it only
// labels the account state for the messaging layer:
//   - user asked for plain record (--off): stays plain record. `record` mode is
//     always pass-through, so this is the one thing that withholds compression here.
//   - valid entitlement (now < expires_at): compression as requested, "entitled".
//   - lapsed ≤7 days: compression as requested, "grace" (caller kicks a refresh).
//   - no/expired/unreadable entitlement: compression as requested, "unentitled".
// Compression is the free adoption surface; signing in earns analytics, team/seats,
// and cloud sync, never the ability to compress. Downstream technical conditions in
// the proxy (prefix zones, MCP recovery, prefix cache, the operator off-switch) may
// still fail closed to byte-identical pass-through — the gate cannot force them on.
export function resolveWrapGate(
  entitlement: WrapEntitlement | null | undefined,
  now: Date,
  requestedMode: WrapRuntimeMode,
): WrapGate {
  if (requestedMode === "record") return { mode: "record", estimate: false, reason: "user-record" };
  const nowMs = now.getTime();
  if (entitlement && entitlement.entitled) {
    const exp = Date.parse(entitlement.expires_at);
    if (Number.isFinite(exp)) {
      if (nowMs < exp) return { mode: requestedMode, estimate: false, reason: "entitled" };
      if (nowMs < exp + WRAP_GRACE_MS) return { mode: requestedMode, estimate: false, reason: "grace" };
    }
  }
  return { mode: requestedMode, estimate: false, reason: "unentitled" };
}

// subscriptionCompressEnabled is the PURE decision behind subscription/OAuth
// coding-agent compression (Claude Pro/Max, Codex ChatGPT, …). There
// is NO account condition here — a Caveman entitlement is irrelevant. It is on:
//   - LOCALLY — this is the local wrap only; the managed gateway's lossless+stealth
//     rule for non-PAYG traffic is unchanged and is not configured from here;
//   - in compress mode — record never mutates anything, and pixel is not supported
//     for subscription traffic (the proxy passes it through).
// The proxy still fails closed on its own technical conditions.
// Their savings are a TOKEN count only: a seat has no per-token price, so they can
// never mint a dollar figure. (honesty rule: no-fake-savings)
export function subscriptionCompressEnabled(gate: WrapGate | null): boolean {
  return !!gate && gate.mode === "compress";
}

function parseWrapEntitlement(raw: unknown): WrapEntitlement | null {
  if (!raw || typeof raw !== "object" || Array.isArray(raw)) return null;
  const e = raw as Record<string, unknown>;
  if (typeof e.expires_at !== "string" || !e.expires_at) return null;
  return {
    entitled: e.entitled === true,
    plan: typeof e.plan === "string" ? e.plan : "free",
    telemetry_level: typeof e.telemetry_level === "string" ? e.telemetry_level : "metadata",
    seats_used: typeof e.seats_used === "number" ? e.seats_used : 0,
    seats_limit: typeof e.seats_limit === "number" ? e.seats_limit : null,
    devices_used: typeof e.devices_used === "number" ? e.devices_used : 0,
    devices_limit: typeof e.devices_limit === "number" ? e.devices_limit : 3,
    evicted_device_hash: typeof e.evicted_device_hash === "string" ? e.evicted_device_hash : null,
    expires_at: e.expires_at,
    ...(typeof e.optimized_tokens_week === "number" ? { optimized_tokens_week: e.optimized_tokens_week } : {}),
    ...(typeof e.weekly_reset_at === "string" ? { weekly_reset_at: e.weekly_reset_at } : {}),
  };
}

// readWrapEntitlement reads the cached entitlement straight from config.json (a
// cheap sync read on the hot wrap path, like gatewayUrlFromConfigFile). Any
// problem returns null — the gate then degrades to observe-only, never an error.
function readWrapEntitlement(): WrapEntitlement | null {
  try {
    const parsed = JSON.parse(readFileSync(configPath(), "utf8")) as Record<string, unknown>;
    return parseWrapEntitlement(parsed.wrapEntitlement);
  } catch {
    return null;
  }
}

function readWrapEntitlementState(): WrapEntitlementState | null {
  try {
    const parsed = JSON.parse(readFileSync(configPath(), "utf8")) as Record<string, unknown>;
    const value = parsed.wrapEntitlementState;
    if (!value || typeof value !== "object" || Array.isArray(value)) return null;
    const raw = value as Record<string, unknown>;
    if (!["ok", "seat-wall", "denied", "unverified"].includes(String(raw.kind)) || typeof raw.at !== "string") return null;
    return {
      kind: raw.kind as WrapEntitlementState["kind"],
      at: raw.at,
      ...(typeof raw.seats_used === "number" ? { seats_used: raw.seats_used } : {}),
      ...(typeof raw.seats_limit === "number" ? { seats_limit: raw.seats_limit } : {}),
    };
  } catch {
    return null;
  }
}

// mutateRawConfig read-modify-writes config.json preserving every other key, so
// entitlement/deviceId writes never clobber baseURL/gatewayUrl/telemetry etc.
function mutateRawConfig(fn: (out: Record<string, unknown>) => void) {
  let out: Record<string, unknown> = {};
  try {
    const parsed = JSON.parse(readFileSync(configPath(), "utf8")) as unknown;
    if (parsed && typeof parsed === "object" && !Array.isArray(parsed)) out = parsed as Record<string, unknown>;
  } catch {
    /* fresh config */
  }
  fn(out);
  mkdirSync(dirname(configPath()), { recursive: true });
  writeFileSync(configPath(), JSON.stringify(out, null, 2), { mode: 0o600 });
  try {
    chmodSync(configPath(), 0o600);
  } catch {
    /* best effort */
  }
}

// ensureDeviceId returns a stable, opaque per-machine id, generated once and
// persisted in config.json. The entitlement device_hash is sha256(deviceId) — a
// random per-install id, NEVER derived from a hardware serial.
function ensureDeviceId(): string {
  try {
    const parsed = JSON.parse(readFileSync(configPath(), "utf8")) as { deviceId?: unknown };
    if (typeof parsed.deviceId === "string" && parsed.deviceId) return parsed.deviceId;
  } catch {
    /* generate + persist below */
  }
  const id = randomUUID();
  mutateRawConfig((out) => {
    out.deviceId = id;
  });
  return id;
}

function deviceHashFromId(deviceId: string): string {
  return createHash("sha256").update(deviceId).digest("hex");
}

// saveWrapEntitlement stores the server response VERBATIM plus a fetched-at stamp.
function saveWrapEntitlement(entitlement: unknown) {
  mutateRawConfig((out) => {
    out.wrapEntitlement = entitlement;
    out.wrapEntitlementFetchedAt = new Date().toISOString();
    out.wrapEntitlementState = { kind: "ok", at: new Date().toISOString() };
  });
}

function saveWrapEntitlementState(state: Omit<WrapEntitlementState, "at">): void {
  mutateRawConfig((out) => {
    out.wrapEntitlementState = { ...state, at: new Date().toISOString() };
    if (state.kind !== "ok") delete out.wrapEntitlement;
  });
}

function planLabel(plan: string): string {
  switch (plan) {
    case "free":
      return "Free";
    case "indie":
      return "Indie";
    case "team":
      return "Team";
    case "enterprise":
      return "Enterprise";
    default:
      return "Enterprise";
  }
}

// planWeeklyAllowanceText mirrors the web dashboard's weekly-allowance display — the
// parenthetical shows only for the capped tiers (free 5M / indie 50M).
function planWeeklyAllowanceText(plan: string): string | null {
  if (plan === "free") return "5M optimized tokens/week";
  if (plan === "indie") return "50M optimized tokens/week";
  return null;
}

function humanTokens(n: number): string {
  if (!Number.isFinite(n) || n <= 0) return "0";
  if (n >= 1_000_000_000) {
    const b = n / 1_000_000_000;
    return (Number.isInteger(b) ? String(b) : b.toFixed(1).replace(/\.0$/, "")) + "B";
  }
  if (n >= 1_000_000) {
    const m = n / 1_000_000;
    return (Number.isInteger(m) ? String(m) : m.toFixed(1).replace(/\.0$/, "")) + "M";
  }
  if (n >= 1_000) return `${Math.round(n / 1_000)}k`;
  return String(Math.round(n));
}

// ── entitlement handshake (called at login success) ─────────────────────────────

type WrapEntitlementFetch =
  | { kind: "ok"; entitlement: unknown; parsed: WrapEntitlement }
  | { kind: "seatwall"; body: Record<string, unknown> | null }
  | { kind: "unavailable" }
  | { kind: "denied" };

async function requestWrapEntitlement(baseURL: string, accessToken: string, wrappedRun = false): Promise<WrapEntitlementFetch> {
  const deviceId = ensureDeviceId();
  const body = JSON.stringify({
    device_hash: deviceHashFromId(deviceId),
    device_name: hostname(),
    ...(wrappedRun ? { wrapped_run: true } : {}),
  });
  let resp: Response;
  try {
    resp = await fetch(`${baseURL}/api/v1/me/wrap-entitlement`, {
      method: "POST",
      headers: { authorization: `Bearer ${accessToken}`, "content-type": "application/json", "x-cave-csrf": "cli" },
      body,
      signal: AbortSignal.timeout(5000),
    });
  } catch {
    return { kind: "unavailable" }; // network/5xx-shaped: fail open, keep cache
  }
  if (resp.status === 403) {
    const errBody = (await resp.json().catch(() => null)) as Record<string, any> | null;
    const code = errBody?.error?.code ?? errBody?.code;
    if (code === "cave_seats_exhausted") return { kind: "seatwall", body: errBody };
    return { kind: "denied" };
  }
  if (!resp.ok) return { kind: "unavailable" };
  const entitlement = await resp.json().catch(() => null);
  const parsed = parseWrapEntitlement(entitlement);
  if (!parsed) return { kind: "unavailable" };
  return { kind: "ok", entitlement, parsed };
}

// fetchAndStoreWrapEntitlement runs the login-time handshake. Login itself NEVER
// fails for seats or a down entitlement service — and never for compression, which
// does not depend on it. The worst case is no cloud sync.
async function fetchAndStoreWrapEntitlement(baseURL: string, accessToken: string) {
  const result = await requestWrapEntitlement(baseURL, accessToken);
  switch (result.kind) {
    case "ok":
      saveWrapEntitlement(result.entitlement);
      printLoginEntitlement(result.parsed);
      return;
    case "seatwall":
      {
        const err = ((result.body?.error as Record<string, unknown>) ?? result.body ?? {}) as Record<string, unknown>;
        saveWrapEntitlementState({
          kind: "seat-wall",
          ...(typeof err.seats_used === "number" ? { seats_used: err.seats_used } : {}),
          ...(typeof err.seats_limit === "number" ? { seats_limit: err.seats_limit } : {}),
        });
      }
      printSeatWall(result.body);
      return; // continue login WITHOUT an entitlement
    case "denied":
      saveWrapEntitlementState({ kind: "denied" });
      process.stderr.write(dim("  → no wrap entitlement on this account — cloud sync and analytics are off; local compression is unaffected\n"));
      return;
    case "unavailable":
      if (!readWrapEntitlement()) saveWrapEntitlementState({ kind: "unverified" });
      process.stderr.write(dim("  → entitlement check unavailable — keeping any cached entitlement; local compression is unaffected\n"));
      return;
  }
}

function printLoginEntitlement(e: WrapEntitlement) {
  const seatLimit = e.seats_limit == null ? "∞" : String(e.seats_limit);
  const allowance = planWeeklyAllowanceText(e.plan);
  const paren = allowance ? `${planLabel(e.plan)} — ${allowance}` : planLabel(e.plan);
  process.stderr.write(dim(`→ seat ${e.seats_used} of ${seatLimit} active  (${paren})\n`));
  process.stderr.write(dim("→ compression: on locally with or without this account\n"));
  process.stderr.write(dim("→ this account adds: analytics, team/seats, cloud sync\n"));
  process.stderr.write(dim("→ telemetry: token counts only, never your prompts — caveman.so/data-use\n"));
  process.stderr.write(dim("your runs now sync to the dashboard — start one with `caveman claude`\n"));
}

function printSeatWall(body: Record<string, unknown> | null) {
  const err = ((body?.error as Record<string, unknown>) ?? body ?? {}) as Record<string, unknown>;
  const pick = (k: string) => err[k] ?? (body as Record<string, unknown> | null)?.[k];
  const org = String(pick("organization") ?? pick("org") ?? orgIdFromConfigFile() ?? "your org") || "your org";
  const used = pick("seats_used");
  const limit = pick("seats_limit");
  const plan = planLabel(String(pick("plan") ?? "free"));
  const usage = used !== undefined && limit !== undefined ? `${used} of ${limit}` : "all its seats";
  process.stderr.write(`${mark("bad")} no seats left — ${org} is using ${usage} (${plan})\n`);
  process.stderr.write("  Team is $299/mo for 10 seats → app.caveman.so/billing\n");
  process.stderr.write(dim("→ local compression keeps running; only cloud sync and analytics need a seat.\n"));
}

// refreshWrapEntitlementInBackground silently re-fetches during offline grace so a
// renewed plan lifts the notice next session. Fire-and-forget; failure stays in
// grace. Socket is explicitly unrefed so a stalled control plane cannot hold a
// completed wrapped agent process open. A 401 is left for foreground auth paths
// to refresh; credential rotation must not become hidden post-run work.
function isoWeekKey(now = new Date()): string {
  const d = new Date(Date.UTC(now.getUTCFullYear(), now.getUTCMonth(), now.getUTCDate()));
  const day = d.getUTCDay() || 7;
  d.setUTCDate(d.getUTCDate() - day + 1);
  return d.toISOString().slice(0, 10);
}

function claimWeeklyRunRefresh(now = new Date()): boolean {
  const week = isoWeekKey(now);
  let claimed = false;
  mutateRawConfig((out) => {
    if (out.wrapEntitlementRunRefreshWeek === week) return;
    out.wrapEntitlementRunRefreshWeek = week;
    claimed = true;
  });
  return claimed;
}

function backgroundPostJSON(urlString: string, token: string, body: string): Promise<{ ok: boolean; status: number; body: unknown }> {
  return new Promise((resolve, reject) => {
    const url = new URL(urlString);
    const request = (url.protocol === "https:" ? httpsRequest : httpRequest)(url, {
      method: "POST",
      headers: {
        authorization: `Bearer ${token}`,
        "content-type": "application/json",
        "content-length": Buffer.byteLength(body),
        "x-cave-csrf": "cli",
      },
    }, (response) => {
      response.socket?.unref();
      const chunks: Buffer[] = [];
      let bytes = 0;
      response.on("data", (chunk: Buffer) => {
        bytes += chunk.length;
        if (bytes > 1 << 20) {
          request.destroy(new Error("wrap entitlement response exceeds 1 MiB"));
          return;
        }
        chunks.push(chunk);
      });
      response.on("end", () => {
        let parsed: unknown = null;
        try { parsed = JSON.parse(Buffer.concat(chunks).toString("utf8")); } catch { /* invalid response stays null */ }
        const status = response.statusCode ?? 0;
        resolve({ ok: status >= 200 && status < 300, status, body: parsed });
      });
    });
    request.on("socket", (socket) => socket.unref());
    request.on("error", reject);
    request.setTimeout(5000, () => request.destroy(new Error("wrap entitlement refresh timed out")));
    request.end(body);
  });
}

function refreshWrapEntitlementInBackground(options: { wrappedRun?: boolean } = {}) {
  if (wrapExternalWritesDisabled()) return;
  const wrappedRun = options.wrappedRun === true;
  if (wrappedRun && !claimWeeklyRunRefresh()) return;
  void (async () => {
    try {
      const cfg = await config();
      if (!cfg.token) return;
      const deviceId = ensureDeviceId();
      const body = JSON.stringify({
        device_hash: deviceHashFromId(deviceId),
        device_name: hostname(),
        ...(wrappedRun ? { wrapped_run: true } : {}),
      });
      const resp = await backgroundPostJSON(`${cfg.baseURL}/api/v1/me/wrap-entitlement`, cfg.token, body);
      if (!resp.ok) {
        mutateRawConfig((out) => {
          out.wrapEntitlementRefresh = { at: new Date().toISOString(), ok: false };
        });
        return;
      }
      const ent = resp.body;
      if (parseWrapEntitlement(ent)) {
        saveWrapEntitlement(ent);
        mutateRawConfig((out) => {
          out.wrapEntitlementRefresh = { at: new Date().toISOString(), ok: true };
        });
      }
    } catch {
      mutateRawConfig((out) => {
        out.wrapEntitlementRefresh = { at: new Date().toISOString(), ok: false };
      });
    }
  })().catch(() => {
    /* never surface a background rejection */
  });
}

// ── wrap-start + session-end lines (the §8 golden transcripts) ───────────────────

// SUBSCRIPTION_TOKENS_ONLY_NOTE is the single honesty line for locally compressed
// subscription/OAuth sessions. A Claude Pro/Max seat has no per-token price, and an
// OAuth login is list-price-eligible on Vertex alone (which this session view cannot
// confirm), so their savings are only ever a token count — never dollars, never
// `verified`, and the token count itself is the engine's local o200k estimate, not a
// provider figure. (honesty rule: no-fake-savings)
const SUBSCRIPTION_TOKENS_ONLY_NOTE =
  "subscription and OAuth logins are counted in tokens only (local o200k estimate) — a seat has no per-token price, so no dollar figure is claimed for them";

// SUBSCRIPTION_NO_RECOVERY_NOTE is what an entitled session says when the OTHER
// half of the proxy's gate is missing. Compression elides detail behind a
// `<<ccr:handle>>` marker only the agent's own `caveman_retrieve` MCP tool can
// recover, so the proxy stays byte-identical pass-through without it. Say that
// plainly instead of announcing compression that is off. (honesty rule: no-placeholder)
const SUBSCRIPTION_NO_RECOVERY_NOTE =
  "subscription and OAuth logins stay byte-identical pass-through here: local compression needs the caveman MCP retrieve tool to recover elided detail — run `caveman mcp install <agent>` to turn it on";

// Same fact, different remedy: under execute.mcp=marker-only nothing is missing,
// the operator chose the smaller prefix. Telling them to run `mcp install` there
// would contradict their own config, so name the knob instead.
const SUBSCRIPTION_MARKER_ONLY_NOTE =
  "subscription and OAuth logins stay byte-identical pass-through here: execute.mcp=marker-only, so no caveman_retrieve tool is injected and elided detail would be unrecoverable — `caveman tools config set execute.mcp auto` turns it on";

function subscriptionNoRecoveryNote(mode: McpSurfaceMode): string {
  return mode === "marker-only" ? SUBSCRIPTION_MARKER_ONLY_NOTE : SUBSCRIPTION_NO_RECOVERY_NOTE;
}

type ProxyObserveSummary = {
  spans?: number;
  tokens_in?: number;
  would_save_tokens?: number;
  would_save_pct?: number;
  compression_tokens_saved?: number;
  savings_usd?: number;
  would_save_usd?: number | null;
  basis?: string;
  token_accounting?: Record<string, number>;
  mem_blocks?: number;
  // The compressed-parts before/after totals behind compression_tokens_saved. Older
  // proxy binaries predate them; absent → the session line reports the delta alone
  // rather than inventing a before/after pair.
  compression_tokens_before?: number;
  compression_tokens_after?: number;
  // requests_eligible_for_compression counts requests that reached the compression
  // candidate path this session, regardless of bytes saved (proxy issue #133). It
  // disambiguates "routing never applied" (0) from "ran but saved nothing" (>0 with
  // a zero cut). Older proxy binaries omit it → absent means unknown, not zero.
  requests_eligible_for_compression?: number;
  // Provider-reported cache read/write totals across the window, and the proxy's own
  // refusal flag: headline_compression_refused is true when cache writes dominated
  // cache reads, so a small compression cut is not an honest headline saving.
  cached_input_tokens?: number;
  cache_creation_input_tokens?: number;
  headline_compression_refused?: boolean;
  cache_bust_requests?: number;
};

type EngineSessionMeasurementMode = "observe" | "compress";

const TELEMETRY_MAX_COUNT = 2 ** 32 - 1;
const TELEMETRY_MAX_TOKENS = 2 ** 50 - 1;

function boundedTelemetryInt(value: unknown, max: number): number | null {
  return typeof value === "number" && Number.isSafeInteger(value) && value >= 0 && value <= max ? value : null;
}

// Closed, content-free aggregate derived from the same local proxy summary shown
// at session end. Invalid/inconsistent summaries fail closed to measurement_ok=false
// instead of shipping partial numbers that could become a misleading growth claim.
export function engineSessionTelemetryFields(
  measurementMode: EngineSessionMeasurementMode,
  summary: ProxyObserveSummary | null,
): Record<string, unknown> {
  const empty = {
    measurement_mode: measurementMode,
    measurement_ok: false,
    requests_observed: 0,
    input_tokens_observed: 0,
    compression_eligible_requests: 0,
    compression_eligibility_known: false,
    compression_tokens_before: 0,
    compression_tokens_after: 0,
    compression_tokens_saved: 0,
    compression_pair_known: false,
    estimated_cut_tokens: 0,
    cache_read_tokens: 0,
    cache_write_tokens: 0,
    cache_bust_requests: 0,
    headline_suppressed: false,
  };
  if (!summary) return empty;

  const requests = boundedTelemetryInt(summary.spans, TELEMETRY_MAX_COUNT);
  const inputTokens = boundedTelemetryInt(summary.tokens_in, TELEMETRY_MAX_TOKENS);
  const eligibilityKnown = typeof summary.requests_eligible_for_compression === "number";
  const eligible = eligibilityKnown
    ? boundedTelemetryInt(summary.requests_eligible_for_compression, TELEMETRY_MAX_COUNT)
    : 0;
  const pairKnown = typeof summary.compression_tokens_before === "number"
    && typeof summary.compression_tokens_after === "number";
  const before = pairKnown ? boundedTelemetryInt(summary.compression_tokens_before, TELEMETRY_MAX_TOKENS) : 0;
  const after = pairKnown ? boundedTelemetryInt(summary.compression_tokens_after, TELEMETRY_MAX_TOKENS) : 0;
  const saved = measurementMode === "compress"
    ? boundedTelemetryInt(summary.compression_tokens_saved, TELEMETRY_MAX_TOKENS)
    : 0;
  const estimated = measurementMode === "observe"
    ? boundedTelemetryInt(summary.would_save_tokens, TELEMETRY_MAX_TOKENS)
    : 0;
  const cacheRead = boundedTelemetryInt(summary.cached_input_tokens ?? 0, TELEMETRY_MAX_TOKENS);
  const cacheWrite = boundedTelemetryInt(summary.cache_creation_input_tokens ?? 0, TELEMETRY_MAX_TOKENS);
  const cacheBusts = boundedTelemetryInt(summary.cache_bust_requests ?? 0, TELEMETRY_MAX_COUNT);
  const valid = requests !== null && inputTokens !== null && eligible !== null
    && before !== null && after !== null && saved !== null && estimated !== null
    && cacheRead !== null && cacheWrite !== null && cacheBusts !== null
    && (!eligibilityKnown || eligible <= requests)
    && (!pairKnown || (before >= after && saved === before - after))
    && (measurementMode !== "observe" || estimated <= inputTokens);
  if (!valid) return empty;

  return {
    measurement_mode: measurementMode,
    measurement_ok: true,
    requests_observed: requests,
    input_tokens_observed: inputTokens,
    compression_eligible_requests: eligible,
    compression_eligibility_known: eligibilityKnown,
    compression_tokens_before: before,
    compression_tokens_after: after,
    compression_tokens_saved: saved,
    compression_pair_known: pairKnown,
    estimated_cut_tokens: estimated,
    cache_read_tokens: cacheRead,
    cache_write_tokens: cacheWrite,
    cache_bust_requests: cacheBusts,
    headline_suppressed: measurementMode === "compress" && summary.headline_compression_refused === true,
  };
}

function engineSessionEvent(
  measurementMode: EngineSessionMeasurementMode,
  sinceISO: string,
  summary: ProxyObserveSummary | null,
  exitClass: TelemetryExitClass,
  agentId?: string,
): Record<string, unknown> | null {
  const state = telemetryState();
  if (!telemetrySendable(state)) return null;
  const started = Date.parse(sinceISO);
  const event: Record<string, unknown> = {
    schema: "cli/v1",
    anonymous_id: telemetryAnonymousId(state),
    event: "engine_session",
    command: "wrap",
    cli_version: cliVersion(),
    os: process.platform,
    arch: process.arch,
    node_major: Number(process.versions.node.split(".")[0] ?? 0),
    duration_ms: Number.isFinite(started) ? Math.max(0, Date.now() - started) : 0,
    exit_class: exitClass,
    ts: new Date().toISOString(),
    ...engineSessionTelemetryFields(measurementMode, summary),
  };
  if (agentId && findAgent(agentId)) event.agent = agentId;
  if (exitClass === "error") event.error_class = "other";
  return event;
}

// readProxyObserveSummary asks the proxy for the compact stats object filtered to
// the session start. Any problem (binary missing, db busy, non-JSON) returns null:
// the caller then prints NOTHING rather than a wrong number.
function readProxyObserveSummary(sinceISO: string): ProxyObserveSummary | null {
  const bin = cavemanBin("caveman-proxy", "CAVEMAN_PROXY_BIN");
  try {
    const out = execFileSync(bin, ["stats", "--json", "--since", sinceISO], {
      encoding: "utf8",
      env: process.env,
      timeout: 2000,
      stdio: ["ignore", "pipe", "ignore"],
    });
    const parsed = JSON.parse(out) as unknown;
    if (!parsed || typeof parsed !== "object") return null;
    return parsed as ProxyObserveSummary;
  } catch {
    return null;
  }
}

// PROXY_RECENT_ROW_CAP mirrors the store's hard cap on `stats --recent N`: asking
// for more rows than this silently returns this many, so a full page back is
// indistinguishable from a truncated window.
const PROXY_RECENT_ROW_CAP = 500;

// readProxySessionAuthModes returns the distinct auth modes the local store recorded
// at or after the session start, plus whether the window it read was truncated. The
// compact summary does not split its totals by auth mode, and the session line MUST
// know whether tokens-only (subscription/OAuth) rows are in scope: those rows carry
// token counts only, so any dollar figure printed alongside them has to say which
// traffic it covers. A busy session can push those rows past the row cap, so a
// truncated window is reported as such and the caller then qualifies unconditionally
// rather than trusting a partial view. Row timestamps persist as UTC
// "YYYY-MM-DD HH:MM:SS.mmm". Any problem returns an empty set and the caller then
// says nothing extra rather than guessing.
function readProxySessionAuthModes(sinceISO: string): { modes: string[]; truncated: boolean } {
  const bin = cavemanBin("caveman-proxy", "CAVEMAN_PROXY_BIN");
  const modes = new Set<string>();
  let truncated = false;
  try {
    const since = Date.parse(sinceISO);
    if (!Number.isFinite(since)) return { modes: [], truncated: false };
    const out = execFileSync(bin, ["stats", "--recent", String(PROXY_RECENT_ROW_CAP)], {
      encoding: "utf8",
      env: process.env,
      stdio: ["ignore", "pipe", "ignore"],
    });
    const rows = JSON.parse(out) as unknown;
    if (!Array.isArray(rows)) return { modes: [], truncated: false };
    truncated = rows.length >= PROXY_RECENT_ROW_CAP;
    for (const row of rows) {
      if (!row || typeof row !== "object") continue;
      const r = row as Record<string, unknown>;
      const ts = typeof r.ts === "string" ? Date.parse(`${r.ts.replace(" ", "T")}Z`) : NaN;
      if (!Number.isFinite(ts) || ts < since) continue;
      if (typeof r.auth_mode === "string" && r.auth_mode) modes.add(r.auth_mode);
    }
  } catch {
    return { modes: [], truncated: false };
  }
  return { modes: [...modes], truncated };
}

// formatSessionSavings renders the §8.1 end-of-session block (PURE, so it is
// unit-testable). Observe sessions say "would have cut" and nudge to login;
// entitled+compress sessions say "cut". The dollar figure appears only when the
// store produced a price-eligible one — the local store zeroes cost and savings for
// every subscription row, so a subscription session can never reach one. When
// tokens-only rows ARE in scope the block says so explicitly and scopes any dollar
// figure to the API-key traffic it actually came from. Tokens-only means
// subscription OR oauth: an OAuth login is list-price-eligible on Vertex alone, and
// the auth-mode window carries no provider, so oauth is qualified like subscription
// rather than silently folded into the dollar figure. `windowTruncated` says the
// auth-mode window hit the row cap, i.e. it cannot prove which modes were in scope —
// that fails SAFE to qualifying. Unmeasurable sessions still receive one honest
// gate-specific line: silence is indistinguishable from a broken login flip.
export function formatSessionSavings(
  kind: "observe" | "compress",
  s: ProxyObserveSummary | null,
  authModes: readonly string[] = [],
  windowTruncated = false,
  agentId?: string,
): string[] {
  // `caveman doctor` only accepts these targets; a wrappable agent that isn't
  // one (e.g. openclaw) must fall back to `generic` so the remediation hint is a
  // runnable command, not a usage error.
  const doctorTargets = new Set(["claude", "codex", "hermes", "gemini", "opencode", "aider"]);
  const rawTarget = agentId && agentId.trim() ? agentId.trim() : "";
  const doctorTarget = rawTarget ? (doctorTargets.has(rawTarget) ? rawTarget : "generic") : "<agent>";
  // A null summary means the proxy could NOT be read (binary missing, db locked,
  // 2s timeout) — we measured nothing. Naming that is distinct from a real empty
  // measurement: we must not claim savings OR byte-safety we never observed.
  // (honesty rule: no-fake-savings) (issue #129)
  if (!s) {
    return [
      "could not read proxy stats for this session (proxy binary missing, db locked, or timed out) — nothing was measured this session, so nothing is claimed; `caveman status` shows recorded totals",
    ];
  }
  const n = (v: unknown) => (typeof v === "number" && Number.isFinite(v) ? v : 0);
  const spans = n(s.spans);
  const tokensIn = n(s.tokens_in);
  const observe = kind === "observe";
  const emptyLine = observe
    ? "no compressible context in this session — observe mode found nothing to measure"
    : "nothing compressible in this session — the layer stayed byte-safe; `caveman status` shows today's totals";

  // Compress-path routing/actuation disambiguation (issue #129), keyed on the
  // proxy's requests_eligible_for_compression: how many requests reached the
  // compression candidate path this session. Only trusted when the proxy actually
  // reported it. An older binary omits the field entirely (unknown, not zero):
  // when that old summary also reports no cut, identify stale runtime instead of
  // recycling the pre-#129 byte-safe success wording. A real positive delta remains
  // reportable even when the older proxy cannot supply this diagnostic denominator.
  const eligibleKnown = typeof s.requests_eligible_for_compression === "number";
  const eligible = n(s.requests_eligible_for_compression);
  const cut = observe ? n(s.would_save_tokens) : n(s.compression_tokens_saved);
  if (!observe && !eligibleKnown && cut <= 0) {
    return [
      "proxy stats lack compression eligibility, so routing status is unknown — your Caveman runtime is out of date; run `caveman setup --install`",
    ];
  }
  if (!observe && eligibleKnown) {
    if (eligible <= 0) {
      // Nothing reached the compression path: the wrap injection silently no-op'd
      // (stale profile / dead proxy / drift). This is NOT byte-safety success.
      return [
        `your agent never reached the compression layer this session — routing may not have applied; run \`caveman doctor ${doctorTarget}\``,
      ];
    }
    if (cut <= 0) {
      // Compression ran but the net cut was zero — a broken or ineffective setup,
      // not a byte-safe win. Point at the doctor instead of claiming success.
      return [
        `compression ran on ${eligible} request${eligible === 1 ? "" : "s"} this session but saved nothing — run \`caveman doctor ${doctorTarget}\` to check the setup`,
      ];
    }
  }

  if (spans <= 0 || tokensIn <= 0) return [emptyLine];
  if (cut <= 0) return [emptyLine];

  // Cache-write-heavy sessions: the proxy already refuses a compression headline
  // when cache_creation_input_tokens dominate cache reads. Mirror that refusal — a
  // small cut against a large cache write is not an honest headline saving.
  // (honesty rule: under-claim, never over-claim) (issue #129)
  if (!observe && s.headline_compression_refused === true) {
    return [
      `${spans} requests · ${humanTokens(tokensIn)} tokens sent`,
      "this session was cache-write heavy — new context was being written to the provider cache, so there is no compression headline to claim; `caveman status` shows totals",
    ];
  }

  const usd = observe ? (s.would_save_usd == null ? 0 : n(s.would_save_usd)) : n(s.savings_usd);
  const tokensOnly = windowTruncated || authModes.includes("subscription") || authModes.includes("oauth");
  const before = n(s.compression_tokens_before);
  const after = n(s.compression_tokens_after);
  // A percentage may only ratio a SAME-BASIS pair. compression_tokens_saved (before
  // − after, a local compressed-parts estimate) over tokens_in (provider-counted
  // total sent) mixes two bases — the retro path refuses exactly this ratio — so the
  // compress line takes its percentage from before/after when present and otherwise
  // prints none. Observe keeps the proxy's own §8.1 would_save_pct. (no-fake-savings)
  const partsPct = before > 0 && after >= 0 && after <= before
    ? Math.round(((before - after) / before) * 100)
    : null;
  const observePct = observe ? Math.round(n(s.would_save_pct) * 100) : 0;
  const lines = [`${spans} requests · ${humanTokens(tokensIn)} tokens sent`];
  const pctSuffix = observe && observePct > 0 ? ` (${observePct}%)` : "";
  let line = `compression ${observe ? "would have cut" : "cut"} ~${humanTokens(cut)} of those${pctSuffix}`;
  if (before > 0 && after >= 0 && after < before) {
    line += ` · compressed parts ${humanTokens(before)} → ${humanTokens(after)}${partsPct != null ? ` (${partsPct}%)` : ""}`;
  }
  if (usd > 0) {
    // Dollars only ever come from list-price-eligible rows; name that scope whenever
    // unpriceable traffic shared the same session — or whenever we cannot prove it
    // did not. The window is session-start, so the figure is "this session", never "today".
    const scope = tokensOnly ? " on the API-key traffic" : "";
    line += observe
      ? ` — about $${usd.toFixed(2)} this session${scope},\nestimated locally (inferred).`
      : ` — about $${usd.toFixed(2)} this session${scope}, estimated locally (inferred).`;
  } else {
    line += " — estimated locally (inferred).";
  }
  lines.push(line);
  if (tokensOnly) lines.push(SUBSCRIPTION_TOKENS_ONLY_NOTE + ".");
  if (observe) lines.push("apply it:  caveman tools config set think.mode compress");
  return lines;
}

// printSessionSavings reads what the local proxy measured for this session and
// writes the block. A missing summary is itself unmeasurable, so it gets the same
// honest state-specific line rather than silence.
function printSessionSavings(kind: "observe" | "compress", sinceISO: string, agentId?: string): ProxyObserveSummary | null {
  const s = readProxyObserveSummary(sinceISO);
  if (!s) {
    // A missing summary is unmeasurable, not a byte-safe success — render the
    // distinct not-measured line (formatSessionSavings owns the copy).
    for (const line of formatSessionSavings(kind, null, [], false, agentId)) process.stderr.write(dim(line + "\n"));
    return null;
  }
  const window = readProxySessionAuthModes(sinceISO);
  const lines = formatSessionSavings(kind, s, window.modes, window.truncated, agentId);
  for (const line of lines) process.stderr.write(dim(line + "\n"));
  return s;
}

// wrap runs an agent with provider base URLs pointed at the gateway, so the
// agent's LLM traffic flows through Caveman with no code change. Local wrap is
// compression-first: it starts the standalone proxy in `compress` mode unless
// config/env or --off selects record mode. Known agents (claude, codex, …) are launchable by short id
// and get detected; any other command still runs verbatim.
async function wrap(rest: string[]) {
  pendingRunOffStates.length = 0;
  const parsed = parseWrapArgs(rest);
  // Exported (not header-injected): SDK children read CAVE_WORKFLOW as their
  // default x-cave-workflow, and the openclaw overlay reads it at build time.
  // Agents that send no headers stay attributed by the /w/<agent> path only.
  if (parsed.workflow) process.env["CAVE_WORKFLOW"] = parsed.workflow;
  rest = parsed.command;
  if (rest.length === 0) {
    if (interactive()) return wrapInteractive();
    console.error(`usage: ${invokedCommand("wrap")} [--off|--pixel] <agent> [args...]`);
    emitCommandRunOnce("error", "usage");
    process.exit(2);
  }
  const requested = rest[0]!;
  const agent = findAgent(requested);
  const bin = agent ? binOf(agent) : requested;
  const extra = agent ? [...agent.args, ...rest.slice(1)] : rest.slice(1);

  const resolved = which(bin);
  if (!resolved) {
    wrapNotFoundUI(requested, agent);
    emitCommandRunOnce("error", "usage");
    process.exit(127);
  }
  const codexAuthMode = agent?.id === "codex" ? detectCodexWrapAuthMode() : "api-key";
  if (codexAuthMode === "subscription" && parsed.mode === "pixel") {
    console.error("caveman wrap: --pixel not yet supported for codex subscription sessions");
    process.exit(1);
  }
  await bootstrapLocalWrapRuntime(parsed);
  await firstRunExperience();
  // `wrap` is a zero-commit trial. Native hooks/MCP/config live only in a temp
  // host pack for this child; persistent integration requires an explicit install.
  await runWrapped(resolved, extra, agent, parsed, codexAuthMode === "subscription");
}

// ===========================================================================
// First-run experience. One-time (per machine) interactive moment on the first
// real wrap: brand line, a 30-day retrospective scan of local Claude Code/Codex
// session logs ("caveman would have cut ~X of the Y tokens you sent"), the
// telemetry disclosure, and the account question. Everything degrades: non-TTY,
// CAVEMAN_PLAIN, TERM=dumb, or any scan failure skips straight to the agent —
// the experience may never block or break a wrap. Re-run anytime with the
// unprinted `caveman welcome` (porcelain caps: not in help). Honesty rails:
// every figure is a sum over scanned sessions (never extrapolated), labeled
// inferred, tokens not dollars; an absent/failed scan renders nothing rather
// than zeros. (honesty rule: no-fake-savings)
// ===========================================================================

// Base behavior scan and retro pass have independent proxy-side budgets. Child
// timeout is derived from both plus cleanup/serialization margin, so changing a
// pass budget cannot silently make the child kill a valid partial retro result.
// 60s retro covers a full pass over ~1GB / ~2k sessions (measured ~29s); 20s is
// ample for measured ~2.5s base parse while bounding pathological cold trees.
const FIRST_RUN_BEHAVIOR_SCAN_BUDGET_MS = 20_000;
const FIRST_RUN_RETRO_SCAN_BUDGET_MS = 60_000;
const FIRST_RUN_SCAN_TIMEOUT_MARGIN_MS = 10_000;
const FIRST_RUN_SCAN_TIMEOUT_SECONDS = Math.ceil((
  FIRST_RUN_BEHAVIOR_SCAN_BUDGET_MS
  + FIRST_RUN_RETRO_SCAN_BUDGET_MS
  + FIRST_RUN_SCAN_TIMEOUT_MARGIN_MS
) / 1_000);

export function firstRunScanContract(): {
  behaviorBudgetMS: number;
  retroBudgetMS: number;
  timeoutMarginMS: number;
  childTimeoutSeconds: number;
  proxyArgs: string[];
} {
  return {
    behaviorBudgetMS: FIRST_RUN_BEHAVIOR_SCAN_BUDGET_MS,
    retroBudgetMS: FIRST_RUN_RETRO_SCAN_BUDGET_MS,
    timeoutMarginMS: FIRST_RUN_SCAN_TIMEOUT_MARGIN_MS,
    childTimeoutSeconds: FIRST_RUN_SCAN_TIMEOUT_SECONDS,
    proxyArgs: [
      "learn", "scan", "--retro",
      "--behavior-budget-ms", String(FIRST_RUN_BEHAVIOR_SCAN_BUDGET_MS),
      "--retro-budget-ms", String(FIRST_RUN_RETRO_SCAN_BUDGET_MS),
    ],
  };
}

function firstRunUIEligible(): boolean {
  return interactive()
    && !envTruthy(process.env.CAVEMAN_PLAIN)
    && process.env.TERM !== "dumb";
}

async function firstRunPending(): Promise<boolean> {
  const raw = await readRawConfig();
  return typeof raw.firstRunAt !== "string" || !raw.firstRunAt;
}

async function markFirstRunDone(): Promise<void> {
  const raw = await readRawConfig();
  raw.firstRunAt = new Date().toISOString();
  await writeRawConfig(raw);
}

// fitAnimationFrame clips a redraw-in-place frame to the terminal width. A
// frame longer than one row wraps, `\r` then rewinds only to the start of the
// LAST wrapped row, and every subsequent frame leaves the earlier rows behind —
// the animation spews a stack of near-duplicate lines instead of updating one.
function fitAnimationFrame(line: string): string {
  const cols = process.stderr.columns && process.stderr.columns > 0 ? process.stderr.columns : 80;
  return stripAnsi(line).length < cols ? line : truncVisible(line, cols);
}

// startInlineSpinner: zero-dependency braille spinner on stderr. TTY-gated by
// callers; stop() clears the line so the next write starts clean.
function startInlineSpinner(label: string): { update: (msg: string) => void; stop: () => void } {
  const frames = ["⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"];
  let current = label;
  let i = 0;
  const draw = () => process.stderr.write(`\r\x1b[2K${fitAnimationFrame(`${dim(frames[i % frames.length]!)} ${dim(current)}`)}`);
  const timer = setInterval(() => { i++; draw(); }, 80);
  timer.unref();
  draw();
  return {
    update(msg: string) { current = msg; },
    stop() { clearInterval(timer); process.stderr.write("\r\x1b[2K"); },
  };
}

// countUpLine animates a token figure from 0 to its real value (~700ms, eased).
// The animation is presentation only — the final rendered line always shows the
// exact measured figure.
async function countUpLine(render: (value: number) => string, target: number): Promise<void> {
  const steps = 16;
  for (let s = 1; s <= steps; s++) {
    const eased = 1 - Math.pow(1 - s / steps, 3);
    process.stderr.write(`\r\x1b[2K${fitAnimationFrame(render(Math.round(target * eased)))}`);
    await new Promise((resolve) => setTimeout(resolve, 700 / steps));
  }
  // Final line is static (no later redraw), so it may wrap — print it in full.
  process.stderr.write(`\r\x1b[2K${render(target)}\n`);
}

async function firstRunBanner(): Promise<void> {
  const tagline = "  ·  the efficiency layer for AI agents";
  for (const frame of [dim("caveman"), "caveman", bold("caveman")]) {
    process.stderr.write(`\r\x1b[2K${fitAnimationFrame(`  ${frame}${dim(tagline)}`)}`);
    await new Promise((resolve) => setTimeout(resolve, 120));
  }
  process.stderr.write("\n\n");
}

function emitFirstRunEvent(fields: Record<string, unknown>) {
  const state = telemetryState();
  if (!telemetrySendable(state)) return;
  emitTelemetryEvents([{
    schema: "cli/v1",
    anonymous_id: telemetryAnonymousId(state),
    event: "first_run",
    cli_version: cliVersion(),
    os: process.platform,
    arch: process.arch,
    ts: new Date().toISOString(),
    ...fields,
  }]);
}

// renderRetroReveal prints the 30-day retrospective block for a scan that DID
// run (retro present). tokens_observed counts every send including cached
// re-reads; would_cut counts unique tool-output cuts once plus a conservative
// lower bound for observed re-pastes — those carry no ratio. Stream is like-for-like
// against sent: eligible cuts weighted by provider-counted later turns,
// transcript arithmetic, never a projection. (no-fake-savings)
async function renderRetroReveal(retro: LearnRetro): Promise<void> {
  if (retro.sessions_total <= 0) {
    process.stderr.write(dim("  no local Claude Code or Codex sessions from the last 30 days to measure — savings start counting from this one\n\n"));
    return;
  }
  if (retro.sessions_scanned <= 0 || retro.tokens_observed <= 0) {
    process.stderr.write(dim(`  found ${retro.sessions_total} local sessions, but none carried provider-counted usage to measure — savings get measured live from your next sessions\n\n`));
    return;
  }
  // "measured", not "scanned": the gap to sessions_total mixes two causes the
  // proxy does not split — files past the time budget and sessions without
  // provider-counted usage — so the label claims only what both mean.
  const scannedLabel = retro.sessions_scanned < retro.sessions_total
    ? `measured ${retro.sessions_scanned} of ${retro.sessions_total} sessions`
    : `${retro.sessions_scanned} session${retro.sessions_scanned === 1 ? "" : "s"}`;
  process.stderr.write(`  in your last ${retro.window_days} days (${scannedLabel}):\n\n`);
  await countUpLine((v) => `    your agents sent         ${bold(humanTokens(v))} tokens ${dim("(provider-counted, incl. cached re-reads)")}`, retro.tokens_observed);
  if (retro.would_cut_tokens > 0) {
    await countUpLine(
      (v) => `    caveman would have cut   ${green(`~${humanTokens(v)}`)} tokens across unique cuts + observed re-pastes`,
      retro.would_cut_tokens,
    );
    for (const family of retro.families) {
      if (family.tokens > 0) {
        process.stderr.write(dim(`      · ${family.label.padEnd(26)} ${humanTokens(family.tokens)}\n`));
      }
    }
    const streamCut = retro.would_cut_stream_tokens ?? 0;
    if (streamCut > 0) {
      await countUpLine(
        (v) => `    cut context rides every later turn — worth ${green(`~${humanTokens(v)}`)} of the sent total ${dim("(counted from your turns, capped by each turn's provider-counted size)")}`,
        streamCut,
      );
    }
  } else {
    process.stderr.write(`    ${dim("nothing safely compressible found in this history — savings get measured live from your next sessions")}\n`);
  }
  if (!retro.engine_used) {
    process.stderr.write(dim("      (compression engine unavailable — tool-output measurement skipped)\n"));
  }
  const configPerTurn = retro.config_prefix_tokens_per_turn ?? 0;
  if (configPerTurn > 0) {
    process.stderr.write(`\n    plus your config prefix costs ~${humanTokens(configPerTurn)} tokens every turn — \`${invokedAs()} learn\` shows the trim\n`);
  }
  if (retro.sessions_scanned < retro.sessions_total) {
    process.stderr.write(dim("    sessions without provider-counted usage are excluded from every total\n"));
  }
  if (retro.time_boxed) {
    process.stderr.write(dim("    discovery or scan time budget hit; complete discovered paths ran newest-first, figures under-count\n"));
  }
  const sentLabel = retro.tokens_observed_source === "session_usage"
    ? "sent totals are provider-reported in your session logs, counted once per API response · cut estimated locally"
    : "estimated locally";
  process.stderr.write(dim(`    ${sentLabel} · inferred · tokens, not billed spend\n`));
  process.stderr.write(dim("    base cut counts unique tool outputs once + a conservative re-paste lower bound · the rides-every-turn figure uses only later turns observed in your logs — what a re-sent token costs depends on provider caching\n\n"));
}

async function firstRunAccountStep(): Promise<boolean> {
  const cfg = await config();
  if (cfg.token) {
    try {
      const localScan = await syncPendingLocalScan(cfg);
      if (localScan.kind === "synced") process.stderr.write(`  ${mark("ok")} ${localScanSyncLine(localScan)}\n`);
    } catch (error) {
      process.stderr.write(`  ${mark("warn")} local scan sync skipped: ${(error as Error).message} — run \`${invokedAs()} sync\` to retry\n`);
    }
    return true;
  }
  const yes = await promptYesNo(`  do you have a Caveman account? ${dim("[y/N]")}${dim("   (adds the dashboard + auto insights — compression works without one)")}`);
  if (!yes) {
    process.stderr.write(dim(`  when you want the dashboard: ${invokedAs()} login   (free · 1 seat · no card)\n\n`));
    return false;
  }
  try {
    await login([]);
    return true;
  } catch (error) {
    process.stderr.write(`  ${mark("warn")} login failed: ${(error as Error).message} — retry with \`${invokedAs()} login\`\n\n`);
    return false;
  }
}

async function firstRunExperience(opts: { forced?: boolean } = {}): Promise<void> {
  if (!firstRunUIEligible()) return;
  if (!opts.forced && !(await firstRunPending())) return;
  const startedAt = Date.now();
  let retro: LearnRetro | undefined;
  let scanError = "";
  await firstRunBanner();
  const spinner = startInlineSpinner("scanning your last 30 days of local sessions (read-only)…");
  // Both proxy passes are bounded; child cap is derived from those budgets plus
  // margin. An explicit CAVE_LEARN_TIMEOUT remains the operator override.
  const scanContract = firstRunScanContract();
  const priorLearnTimeout = process.env.CAVE_LEARN_TIMEOUT;
  if (priorLearnTimeout === undefined) process.env.CAVE_LEARN_TIMEOUT = String(scanContract.childTimeoutSeconds);
  try {
    const planRaw = await proxyExecLearnAsync(
      scanContract.proxyArgs,
      (message) => spinner.update(message),
    );
    retro = (JSON.parse(planRaw) as LearnPlan).retro;
  } catch (error) {
    scanError = (error as Error).message;
  } finally {
    if (priorLearnTimeout === undefined) delete process.env.CAVE_LEARN_TIMEOUT;
    spinner.stop();
  }
  if (retro) {
    if (!persistPendingLocalScan(retro)) {
      process.stderr.write(dim(`  local scan could not be saved for later dashboard sync — run \`${invokedAs()} welcome\` to retry\n`));
    }
    await renderRetroReveal(retro);
  } else {
    // No retro block and no error means the resolved caveman-proxy predates
    // `learn scan --retro` (older binaries ignore unknown flags). Say that —
    // never blame the user's history for a scan that did not run.
    process.stderr.write(dim(scanError
      ? `  30-day scan skipped (${compactLearnText(scanError, 80)}) — run \`${invokedAs()} learn\` later\n\n`
      : `  30-day scan needs a newer Caveman runtime — update: \`${invokedAs()} setup --install\`\n\n`));
  }
  // Marked after the reveal so a Ctrl-C mid-scan replays the moment next time;
  // only the account question below is one-shot.
  if (!opts.forced) await markFirstRunDone();
  const loggedIn = await firstRunAccountStep();
  emitFirstRunEvent({
    scan_ok: Boolean(retro),
    sessions_total: retro?.sessions_total ?? 0,
    sessions_scanned: retro?.sessions_scanned ?? 0,
    tokens_observed: retro?.tokens_observed ?? 0,
    would_cut_tokens: retro?.would_cut_tokens ?? 0,
    // null (not 0) when the resolved proxy predates the field, so the
    // telemetry stream can tell "old runtime" from "measured nothing".
    would_cut_stream_tokens: retro?.would_cut_stream_tokens ?? null,
    engine_used: retro?.engine_used ?? false,
    time_boxed: retro?.time_boxed ?? false,
    logged_in: loggedIn,
    node_major: Number(process.versions.node.split(".")[0] ?? 0),
    duration_ms: Date.now() - startedAt,
  });
}

export function shouldBootstrapWrapRuntime(input: {
  local: boolean;
  proxyEnabled: boolean;
  interactive: boolean;
  explicitBinaryOverride: boolean;
  runtimeReady: boolean;
}): boolean {
  return input.local
    && input.proxyEnabled
    && input.interactive
    && !input.explicitBinaryOverride
    && !input.runtimeReady;
}

function localWrapRuntimeReady(): boolean {
  const required = GO_BINARIES.filter((binary) => binary.required);
  const resolved = new Map(required.map((binary) => [binary.name, resolveGoBin(binary.name, binary.env)]));
  if ([...resolved.values()].some((binary) => !binary)) return false;
  const proxy = resolved.get("caveman-proxy");
  const recovery = resolved.get("caveman-mcp");
  return Boolean(
    proxy
      && recovery
      && probeVersionedBinary(proxy, "run_state").current
      && probeVersionedBinary(recovery, "mcp_recovery").current,
  );
}

// First interactive wrap is one command. npm package stays tiny; missing
// companion runtime self-installs from signed release assets, then same wrap
// continues. Explicit binary overrides and non-TTY automation are never changed.
// Download failure falls back into existing loud direct/through/cancel menu.
async function bootstrapLocalWrapRuntime(opts: WrapOptions): Promise<void> {
  const requiredBinaryOverride = GO_BINARIES.some(
    (binary) => binary.required && Boolean(process.env[binary.env]?.trim()),
  );
  const local = wrapMode() === "local";
  const interactiveRun = interactive();
  if (!local || opts.noProxy || !interactiveRun || requiredBinaryOverride) return;
  if (!shouldBootstrapWrapRuntime({
    local,
    proxyEnabled: true,
    interactive: interactiveRun,
    explicitBinaryOverride: requiredBinaryOverride,
    runtimeReady: localWrapRuntimeReady(),
  })) {
    return;
  }
  process.stderr.write(dim("→ first run: installing signed Caveman runtime\n"));
  const startedAt = Date.now();
  try {
    await setupInstall(false, { continuing: true });
    emitRuntimeBootstrap("ok", Date.now() - startedAt);
  } catch (error) {
    emitRuntimeBootstrap("error", Date.now() - startedAt, classifyTelemetryError(error));
    const message = error instanceof Error ? error.message : String(error);
    process.stderr.write(`${mark("warn")} automatic runtime install failed: ${message}\n`);
  }
}

function parseWrapArgs(rest: string[]): WrapOptions {
  const out = defaultWrapOptions();
  const cmd = [...rest];
  let explicitOff = false;
  let explicitPixel = false;
  const deletedFlags = new Set(["--compress", "--record", "--toon", "--pixel-models", "--no-shrink", "--no-mcp", "--minimal", "--auto-recall", "--no-proxy"]);
  while (cmd.length > 0) {
    const a = cmd[0];
    if (a === "--") {
      cmd.shift();
      break;
    }
    if (a === "--off") {
      explicitOff = true;
      if (explicitPixel) {
        console.error("caveman wrap: --off and --pixel cannot be used together");
        process.exit(1);
      }
      out.mode = "record";
      cmd.shift();
      continue;
    }
    if (a === "--pixel") {
      explicitPixel = true;
      if (explicitOff) {
        console.error("caveman wrap: --off and --pixel cannot be used together");
        process.exit(1);
      }
      out.mode = "pixel";
      out.toon = false;
      cmd.shift();
      continue;
    }
    if (a === "--workflow") {
      const value = normalizeWorkflowSlug(cmd[1]);
      if (!value) {
        console.error("caveman wrap: --workflow needs a slug (lowercase letters, digits, dashes; max 96 chars)");
        process.exit(2);
      }
      out.workflow = value;
      cmd.shift();
      cmd.shift();
      continue;
    }
    if (deletedFlags.has(a ?? "")) {
      console.error(`caveman wrap: ${a} moved to capability config — inspect with \`${invokedAs()} tools config get\``);
      process.exit(2);
    }
    if (a === "--help" || a === "-h") {
      wrapUsage("stderr");
      process.exit(0);
    }
    break;
  }
  if (out.mode !== "compress") out.toon = false;
  out.command = cmd;
  return out;
}

/** Mirror of the gateway's validLabel (security.go): lowercase [a-z0-9_-],
 *  1-96 chars. Uppercase input is lowercased rather than rejected; anything
 *  else returns undefined so the caller can fail loudly instead of shipping a
 *  header the gateway will 400. */
function normalizeWorkflowSlug(raw: string | undefined): string | undefined {
  if (!raw) return undefined;
  const slug = raw.toLowerCase();
  return /^[a-z0-9_-]{1,96}$/.test(slug) ? slug : undefined;
}

function wrapUsage(stream: "stdout" | "stderr" = "stderr") {
  const write = (line: string) => {
    if (stream === "stdout") console.log(line);
    else console.error(line);
  };
  write(`usage: ${invokedCommand("wrap")} [--off|--pixel] [--workflow <slug>] <agent> [args...]`);
  write("  (default)    ephemeral stack: S4 compression + TOON best-of + recovery/output shrink where host supports a temp pack");
  write("  --off        byte-safe pass-through metering only — nothing is rewritten");
  write("  --pixel      lossy text→PNG pixel mode (model-gated); originals recoverable via caveman_retrieve");
  write("  --workflow   label this session's traffic (x-cave-workflow) so it groups by name in the dashboard");
  write(`Capability groups: think / remember / execute — inspect with \`${invokedAs()} tools config get\`.`);
  write("Auth passes through untouched: API keys AND subscription OAuth logins (Claude Pro/Max) both work.");
  write("Subscription logins compress locally too, no account needed (live zone only — compressed turns are re-sent byte-identically so the provider cache stays warm;");
  write("needs MCP recovery; Claude/Codex wrap provide it ephemerally when caveman-mcp is available). Subscription savings are reported in tokens, never dollars. An account adds the dashboard, not compression.");
}

function normalizeAgentShortcutWrapArgs(input: string[]): string[] {
  const agent = input[0];
  if (!agent) return input;
  const wrapFlags: string[] = [];
  const agentArgs: string[] = [];
  for (let i = 1; i < input.length; i++) {
    const item = input[i]!;
    if (item === "--off" || item === "--pixel") {
      wrapFlags.push(item);
      continue;
    }
    if (item === "--workflow") {
      wrapFlags.push(item);
      const value = input[i + 1];
      if (value !== undefined) {
        wrapFlags.push(value);
        i++;
      }
      continue;
    }
    agentArgs.push(item);
  }
  return [...wrapFlags, agent, ...agentArgs];
}

// Aider is deliberately absent: it has no lifecycle hooks, so nothing on the
// native path would ever start the proxy or apply routedAiderArgs — a direct
// launch would point it at a dead endpoint. Aider keeps the wrap door.
function nativeAgentId(id: string): NativeAgent | undefined {
  return id === "claude" || id === "codex" || id === "hermes" || id === "gemini" || id === "opencode" ? id : undefined;
}

// `caveman <agent>` — the default door. It persistently enables the native
// integration (the same journaled user-scoped writes as `caveman enable <agent>`)
// and then launches the host binary untouched: routing, Core, and proxy
// autostart all live in the installed hooks, so plain `<agent>` stays caveman'd
// in every later session too. Wrap-session flags, unsupported agents, or any
// enable failure fall back to the ephemeral `wrap` door — the shortcut is
// never worse than a session-only wrap.
async function agentShortcut(rest: string[]) {
  // normalizeAgentShortcutWrapArgs hoists --off/--pixel/--workflow to the
  // front, so a leading flag means explicit session-only wrap intent.
  if (rest[0]?.startsWith("--")) return wrap(rest);
  const agent = findAgent(rest[0] ?? "");
  const native = agent ? nativeAgentId(agent.id) : undefined;
  if (!agent || !native) return wrap(rest);
  // A Cave Build lock is enforced at the wrap door (claudeCaveBuildEnv); the
  // native door applies none of its transforms, so a locked project must keep
  // routing through wrap or the lock would be silently unenforced.
  if (existsSync(join(process.cwd(), ".caveman", "agent.lock.json"))) return wrap(rest);
  // First-run disclosure comes before the first persistent write, mirroring wrap.
  await firstRunExperience();
  try {
    // An existing journal means the machine-wide install already owns routing —
    // exactly what plain `<agent>` uses — so launch directly without re-probing
    // (status probes spawn three subprocesses); `caveman doctor <agent>` stays
    // the repair door for drifted installs.
    if (!readNativeJournal(native)) enableNative([native]);
  } catch (error) {
    process.stderr.write(`${mark("warn")} native enable failed: ${(error as Error).message} — using session-only wrap for this run\n`);
    return wrap(rest);
  }
  const bin = which(binOf(agent));
  if (!bin) {
    wrapNotFoundUI(rest[0]!, agent);
    await emitCommandRunOnce("error", "usage");
    process.exit(127);
  }
  // The native SessionStart hook autostarts the proxy, but only after the host
  // has approved the installed hooks — start it here too so the first routed
  // request never hits a dead endpoint. Fail-open, same as the hook.
  try {
    const opts = defaultWrapOptions();
    const gw = gatewayURL();
    const { host, port } = gatewayHostPort(gw);
    if (wrapMode(gw) === "local" && !opts.noProxy && !(await portListening(host, port))) {
      const subscription = native === "codex" && detectCodexWrapAuthMode() === "subscription";
      const mode = subscription && opts.mode === "pixel" ? "record" : opts.mode;
      const recovery = Boolean(probeMcpBinary()?.probe.current);
      await startWrapProxy(
        mode,
        recovery,
        subscription ? false : opts.toon,
        opts.pixelModels,
        opts.pixelDensity,
        gw,
        subscription ? "codex-subscription" : "standard",
        false,
      );
    }
  } catch { /* runtime startup is fail-open; the native hook retries at SessionStart */ }
  if (native === "hermes") maybeWarnHermesMissingKey(agent, gatewayURL());
  const code = await new Promise<number>((resolve, reject) => {
    const invocation = portableInvocation(bin, [...agent.args, ...rest.slice(1)]);
    const child = spawn(invocation.command, invocation.args, { stdio: "inherit" });
    // tty-generated signals (Ctrl+C / Ctrl+\) already reach the child through
    // the shared foreground group — forwarding would double-deliver them. But
    // process-directed SIGTERM/SIGHUP (timeout(1), supervisors, pkill) only hit
    // this launcher, so those must be forwarded. Either way the launcher
    // re-raises on itself after the child exits so callers see a signal death,
    // not a clean exit.
    let fatal: NodeJS.Signals | undefined;
    for (const signal of ["SIGINT", "SIGQUIT"] as NodeJS.Signals[]) {
      process.on(signal, () => { fatal = signal; /* the tty delivered it to the child already */ });
    }
    for (const signal of ["SIGHUP", "SIGTERM"] as NodeJS.Signals[]) {
      process.on(signal, () => {
        fatal = signal;
        child.kill(signal);
        const grace = setTimeout(() => child.kill("SIGKILL"), 10_000);
        grace.unref();
      });
    }
    child.on("error", (error) => reject(new Error(`failed to exec ${bin}: ${error.message}`)));
    child.on("exit", (exitCode, signal) => {
      if (fatal) {
        process.removeAllListeners(fatal);
        process.kill(process.pid, fatal);
        return;
      }
      resolve(exitCode ?? signalExitCode(signal));
    });
  });
  const exitClass: TelemetryExitClass = code === 0 ? "ok" : "error";
  const event = commandRunEventOnce(exitClass, exitClass === "error" ? "other" : undefined);
  if (event) await emitTelemetryEvents([event]);
  process.exit(code);
}

// runWrapped execs the resolved command with the gateway injection applied. It
// auto-starts the local proxy when routing to loopback, so `caveman wrap claude`
// is the one-command compression path. If the proxy can't be reached, a TTY run
// offers to launch the agent directly (no Caveman, no compression this run) rather
// than wire it to a dead endpoint; non-TTY runs warn and route through as before.
async function runWrapped(bin: string, cmdArgs: string[], agent?: AgentProfile, opts: WrapOptions = { mode: "compress", noProxy: false, toon: true, noShrink: false, mcpMode: "auto", noBrowse: false, delegate: false, minimal: false, command: [] }, codexSubscription = false) {
  const result = await spawnWrapped(bin, cmdArgs, agent, opts, gatewayURL(), codexSubscription);
  const summary = result.summaryKind && result.sessionStart
    ? printSessionSavings(result.summaryKind, result.sessionStart, agent?.id)
    : null;
  if (result.proxyStarted) process.stderr.write(dim("→ Caveman proxy left running; inspect with `caveman stats`\n"));
  await syncAfterWrap();
  const exitClass: TelemetryExitClass = result.code === 0 ? "ok" : "error";
  const events: Record<string, unknown>[] = [];
  const commandEvent = commandRunEventOnce(exitClass, exitClass === "error" ? "other" : undefined);
  if (commandEvent) events.push(commandEvent);
  if (result.summaryKind && result.sessionStart) {
    const sessionEvent = engineSessionEvent(result.summaryKind, result.sessionStart, summary, exitClass, agent?.id);
    if (sessionEvent) events.push(sessionEvent);
  }
  if (events.length > 0) await emitTelemetryEvents(events);
  process.exit(result.code);
}

const AIDER_ROUTED_DEFAULT_MODEL = "openai/gpt-5.5";

function routedAiderArgs(args: string[]): string[] {
  let sawModel = false;
  let malformedModel = false;
  let effectiveModel: string | undefined;
  for (let index = 0; index < args.length; index++) {
    const arg = args[index]!;
    if (arg === "--") break;
    if (arg === "--model") {
      sawModel = true;
      const value = args[index + 1];
      if (!value || value === "--" || value.startsWith("-")) {
        malformedModel = true;
      } else {
        effectiveModel = value;
        index++;
      }
    } else if (arg.startsWith("--model=")) {
      sawModel = true;
      const value = arg.slice("--model=".length);
      if (value) effectiveModel = value;
      else malformedModel = true;
    }
  }
  if (sawModel) {
    if (malformedModel) process.stderr.write("caveman: aider --model requires a value; Aider may reject this launch before routing\n");
    if (effectiveModel && !effectiveModel.startsWith("openai/")) {
      process.stderr.write(`caveman: aider model ${JSON.stringify(effectiveModel)} does not select its OpenAI-compatible provider; traffic may bypass Caveman\n`);
    }
    return args;
  }
  process.stderr.write(`caveman: aider model not set; routing with --model ${AIDER_ROUTED_DEFAULT_MODEL}\n`);
  return ["--model", AIDER_ROUTED_DEFAULT_MODEL, ...args];
}

async function spawnWrapped(
  bin: string,
  cmdArgs: string[],
  agent: AgentProfile | undefined,
  opts: WrapOptions,
  gw: string,
  codexSubscription = false,
): Promise<{ code: number; proxyStarted: boolean; sessionStart?: string | undefined; summaryKind?: "observe" | "compress" | undefined }> {
  const { host, port } = gatewayHostPort(gw);
  const local = wrapMode(gw) === "local";
  let proxyStarted = false;
  // Gemini CLI exposes endpoint overrides but no supported custom-header channel.
  // Hosted Caveman needs separate gateway and upstream credentials, so managed
  // Gemini cannot preserve both contracts. Launch direct instead of routing a
  // request that will 401 or misuse one credential as the other.
  const managedGeminiUnsupported = !local && agent?.id === "gemini";
  let direct = managedGeminiUnsupported;
  if (managedGeminiUnsupported) {
    process.stderr.write("caveman: managed Gemini CLI wrap is unsupported because Gemini CLI cannot send separate Caveman and upstream credentials; launching directly\n");
  }
  // The local proxy compresses with no account. When wrap runs the LOCAL
  // proxy path we resolve the mode; managed gateway traffic is governed by the cloud
  // policy engine, so we never resolve it here.
  const gateApplies = local && !opts.noProxy;
  const entitlement = gateApplies ? readWrapEntitlement() : null;
  const entitlementState = gateApplies ? readWrapEntitlementState() : null;
  const gate = gateApplies ? resolveWrapGate(entitlement, new Date(), opts.mode) : null;
  const effectiveMode = gate ? gate.mode : opts.mode;
  const observeEstimate = gate ? gate.estimate : false;
  // Local compress sessions also compress subscription/OAuth logins, live zone only,
  // account or not. Never for managed traffic (gate is null there). Codex ChatGPT
  // login uses the same decision through the local /chatgpt Responses route.
  const subscriptionCompress = subscriptionCompressEnabled(gate);
  if (gate?.reason === "grace" && entitlement) {
    refreshWrapEntitlementInBackground();
  }
  const signedIn = gateApplies
    && Boolean(resolveCredentials(globalCapabilityDocument() as Partial<Config>).access_token);
  if (signedIn && entitlementState?.kind !== "seat-wall" && entitlementState?.kind !== "denied") {
    refreshWrapEntitlementInBackground({ wrappedRun: true });
  }
  // Streaming requests can only be compressed when the agent can recover elided
  // detail itself — i.e. it has the caveman MCP retrieve tool installed (run
  // `caveman mcp install <agent>`). When it does, signal the proxy to use MCP
  // recovery (which lets it compress streams); otherwise streams pass through.
  const ephemeralMcp = agent && (agent.id === "claude" || agent.id === "codex")
    && !opts.minimal && opts.mcpMode === "auto" && wrapRecoveryEligible(opts)
    ? probeMcpBinary()
    : null;
  if (ephemeralMcp && !ephemeralMcp.probe.current) queueStaleMcpBinary(ephemeralMcp.probe);
  const ephemeralMcpBinary = ephemeralMcp?.probe.current ? ephemeralMcp.binary : undefined;
  const mcpRecovery = Boolean(ephemeralMcpBinary) || wrapMcpRecoveryAvailable(agent, opts);
  const delegateAlreadyInstalled = Boolean(agent && mcpServerInstalled(agent.id, "caveman-delegate"));
  const ephemeralDelegateMcp = agent && (agent.id === "claude" || agent.id === "codex")
    && !opts.minimal && opts.delegate && !delegateAlreadyInstalled
    ? resolveDelegateMcpCommand()
    : null;
  if (agent && (agent.id === "claude" || agent.id === "codex") && opts.delegate && !delegateAlreadyInstalled && !ephemeralDelegateMcp) {
    process.stderr.write("caveman: delegate enabled but caveman-delegate server is unavailable; launching without delegate\n");
  }
  const desiredRecoveryViaMCP = observeEstimate ? false : mcpRecovery;
  const sessionMarker = gateApplies ? createProxySessionMarker(port) : null;
  const proxyVersion = gateApplies ? probeProxyVersion() : null;
  let runtime: ProxyRuntimeState = { owner: "unknown" };
  let runtimeState: OffState | null = null;
  let proxyReady = await portListening(host, port);
  if (proxyReady && gateApplies) {
    runtime = readProxyRuntimeState(port, proxyVersion);
    if (runtime.owner === "unknown") {
      runtimeState = proxyVersion?.capabilities.includes("run_state")
        ? OFF_STATES.foreignProcess(host, port)
        : OFF_STATES.staleBinary("caveman-proxy", proxyVersion?.version ?? "unknown", cliVersion());
    } else if (!proxyRuntimeMatches(runtime, effectiveMode, desiredRecoveryViaMCP)) {
      if (countOtherLiveProxySessions(port, sessionMarker) > 0) {
        runtimeState = !proxyRuntimeGateMatches(runtime, desiredRecoveryViaMCP)
          ? OFF_STATES.runningGateHeld
          : OFF_STATES.runningModeHeld(runtime.mode ?? "unknown", effectiveMode);
      } else {
        const beforeSignal = readRawProxyRunState(port);
        const sameGeneration = beforeSignal.owner !== "unknown"
          && beforeSignal.instance_token === runtime.instance_token
          && beforeSignal.pid === runtime.pid;
        if (sameGeneration && typeof runtime.pid === "number") {
          try {
            process.kill(runtime.pid, "SIGTERM");
            const deadline = Date.now() + proxyRestartTimeoutMs();
            let successor = false;
            while (Date.now() < deadline) {
              await sleep(100);
              const generation = readRawProxyRunState(port);
              if (generation.owner !== "unknown" && generation.instance_token !== runtime.instance_token) {
                successor = true;
                break;
              }
              if (!(await portListening(host, port))) break;
            }
            proxyReady = await portListening(host, port);
            if (!proxyReady && !successor) {
              proxyStarted = await startWrapProxy(
                effectiveMode,
                desiredRecoveryViaMCP,
                codexSubscription ? false : observeEstimate ? false : opts.toon,
                opts.pixelModels,
                opts.pixelDensity,
                gw,
                codexSubscription ? "codex-subscription" : "standard",
                observeEstimate,
              );
              proxyReady = await portListening(host, port);
            }
            runtime = proxyReady ? readProxyRuntimeState(port, proxyVersion) : { owner: "unknown" };
            if (runtime.owner === "unknown" || !proxyRuntimeMatches(runtime, effectiveMode, desiredRecoveryViaMCP)) {
              runtimeState = runtime.owner === "unknown"
                ? OFF_STATES.foreignProcess(host, port)
                : !proxyRuntimeGateMatches(runtime, desiredRecoveryViaMCP)
                  ? OFF_STATES.runningGateMismatch
                  : OFF_STATES.runningModeMismatch(runtime.mode ?? "unknown", effectiveMode);
            }
          } catch {
            runtime = readProxyRuntimeState(port, proxyVersion);
            runtimeState = runtime.owner === "unknown"
              ? OFF_STATES.foreignProcess(host, port)
              : !proxyRuntimeMatches(runtime, effectiveMode, desiredRecoveryViaMCP)
                ? !proxyRuntimeGateMatches(runtime, desiredRecoveryViaMCP)
                  ? OFF_STATES.runningGateMismatch
                  : OFF_STATES.runningModeMismatch(runtime.mode ?? "unknown", effectiveMode)
                : null;
          }
        } else {
          runtime = readProxyRuntimeState(port, proxyVersion);
          runtimeState = runtime.owner === "unknown"
            ? OFF_STATES.foreignProcess(host, port)
            : !proxyRuntimeMatches(runtime, effectiveMode, desiredRecoveryViaMCP)
              ? !proxyRuntimeGateMatches(runtime, desiredRecoveryViaMCP)
                ? OFF_STATES.runningGateMismatch
                : OFF_STATES.runningModeMismatch(runtime.mode ?? "unknown", effectiveMode)
              : null;
        }
      }
    }
  }
  if (!proxyReady) {
    if (local && !opts.noProxy) {
      proxyStarted = codexSubscription
        ? await startWrapProxy(effectiveMode, desiredRecoveryViaMCP, false, undefined, undefined, gw, "codex-subscription", observeEstimate)
        : await startWrapProxy(effectiveMode, desiredRecoveryViaMCP, observeEstimate ? false : opts.toon, opts.pixelModels, opts.pixelDensity, gw, "standard", observeEstimate);
    }
    proxyReady = await portListening(host, port);
    if (proxyReady && gateApplies) {
      runtime = readProxyRuntimeState(port, proxyVersion);
      if (runtime.owner === "unknown") {
        runtimeState = proxyVersion?.capabilities.includes("run_state")
          ? OFF_STATES.foreignProcess(host, port)
          : OFF_STATES.staleBinary("caveman-proxy", proxyVersion?.version ?? "unknown", cliVersion());
      }
    }
    if (!proxyReady && !direct) {
      const startHint = codexSubscription || opts.mode === "record" ? "caveman start" : `CAVEMAN_MODE=${opts.mode} caveman start`;
      if (codexSubscription && opts.noProxy) {
        // Explicit proxy:false leaves subscription Codex in pass-through launch mode
        // without extra status noise; useful for tests and managed launchers.
      } else if (interactive()) {
        // The proxy is down and we couldn't bring it up. Launching the agent now
        // would wire it to a dead endpoint (every request fails), so offer to run
        // it straight through to the provider this once instead.
        const target = agent ? agent.display_name : bin;
        const choice = await selectMenu(`Caveman proxy not reachable on ${host}:${port}`, [
          { label: `Launch ${target} directly`, hint: dim("without Caveman · no compression or metering this run") },
          { label: "Launch through Caveman anyway", hint: dim(`requests fail until you run \`${startHint}\``) },
          { label: "Cancel", hint: dim(`I'll run \`${startHint}\` first`) },
        ]);
        if (choice === 0) direct = true;
        else if (choice !== 1) {
          process.stderr.write(`${mark("warn")} cancelled — run ${cyan(startHint)}, then re-run your wrap\n`);
          emitCommandRunOnce("error", "usage");
          removeProxySessionMarker(sessionMarker);
          return { code: 130, proxyStarted };
        }
      } else {
        process.stderr.write(
          `${mark("warn")} Caveman proxy not detected on ${host}:${port} — run ${cyan(startHint)} first ` +
            dim("(requests will fail to route until it's up)") + "\n",
        );
      }
    }
  }
  if (runtimeState?.id === "running-gate-mismatch") {
    process.stderr.write(`${mark("warn")} ${runtimeState.line}\n`);
    direct = true;
  }
  const subscriptionCompressionActive = !direct && proxyReady && subscriptionCompress && desiredRecoveryViaMCP
    && proxyRuntimeMatches(runtime, effectiveMode, desiredRecoveryViaMCP);
  if (direct) {
    process.stderr.write(dim(`→ wrapping ${agent ? agent.display_name : bin} · direct (no Caveman this run) · using your own provider key`) + "\n");
  } else if (codexSubscription) {
    if (subscriptionCompressionActive) {
      process.stderr.write("caveman: codex subscription login detected — routing via ephemeral CODEX_HOME through /chatgpt with live-zone compression\n");
      process.stderr.write(dim(`→ ${SUBSCRIPTION_TOKENS_ONLY_NOTE}\n`));
    } else {
      process.stderr.write("caveman: codex subscription login detected — routing via ephemeral CODEX_HOME through /chatgpt (byte-safe pass-through)\n");
      // KNOWN GAP (pre-existing): this branch never renders off-states, so a
      // stale caveman-mcp binary — which is what really suppressed recovery —
      // is reported as "no retrieve tool, run mcp install". The off-state site
      // below guards that with !staleMcpBinary; there is no equivalent here
      // because takeRunOffStates() is destructive and is consumed in that branch.
      if (subscriptionCompress && !desiredRecoveryViaMCP) process.stderr.write(dim(`→ ${subscriptionNoRecoveryNote(opts.mcpMode)}\n`));
    }
  } else {
    const states: OffState[] = takeRunOffStates();
    const staleMcpBinary = states.some((state) =>
      state.id === "stale-binary" && state.line.startsWith("caveman-mcp "));
    const resolution = wrapRuntimeConfig().resolution;
    const invalidMode = resolution.values["think.mode"].invalid;
    if (!proxyReady && local && !opts.noProxy) states.push(fixedOffState("binary-missing", OFF_STATES.binaryMissing));
    if (runtimeState) states.push(runtimeState);
    if (invalidMode !== undefined) states.push(OFF_STATES.invalidMode(invalidMode));
    if (gate?.reason === "user-record") states.push(fixedOffState("user-record", OFF_STATES.userRecord));
    if (!codexSubscription && wrapRecoveryEligible(opts) && agent && !mcpRecovery && !staleMcpBinary) {
      states.push(fixedOffState(
        "mcp-missing",
        opts.mcpMode === "marker-only" ? OFF_STATES.mcpMarkerOnly : OFF_STATES.mcpMissing,
      ));
    }
    if (!resolveGoBin("cavemem", "CAVEMEM_BIN")) states.push(fixedOffState("mem-missing", OFF_STATES.memMissing));
    if (entitlement?.telemetry_level === "zdr") states.push(fixedOffState("zdr", OFF_STATES.zdr));

    const runningLocalMode = runtime.owner !== "unknown" && runtime.mode ? runtime.mode : null;
    const modeText = local ? runningLocalMode : "managed";
    printRunBanner({
      agent,
      binary: bin,
      runningMode: modeText,
      states,
      projectGroups: resolution.projectActive ? capabilityProjectGroups() : [],
    });
    // One honest line about what subscription compression does and what it can never
    // claim — but only when the proxy will actually take that path. Without MCP
    // recovery the proxy stays byte-identical pass-through, so an unrecoverable
    // session says so instead. (honesty rule: no-placeholder)
    if (subscriptionCompressionActive) {
      process.stderr.write(dim("→ subscription logins (Claude Pro/Max) compress locally too — live zone only; compressed turns are re-sent byte-identically so the provider cache stays warm\n"));
      process.stderr.write(dim(`→ ${SUBSCRIPTION_TOKENS_ONLY_NOTE}\n`));
    } else if (subscriptionCompress && !desiredRecoveryViaMCP && !staleMcpBinary) {
      // !staleMcpBinary for the same reason the off-state above carries it: a
      // stale binary is not a missing tool, and naming the wrong remedy is worse
      // than naming none.
      process.stderr.write(dim(`→ ${subscriptionNoRecoveryNote(opts.mcpMode)}\n`));
    }
  }
  // Direct mode: inherit the shell env with NO profile injection, stripping only
  // our own routing if it leaked in — so the agent talks straight to the provider
  // with its own key. Any unrelated base URL the user set themselves stays put.
  const includeShrink = !opts.noShrink && wrapCompressEnabled(opts);
  let childArgs = cmdArgs;
  let env: NodeJS.ProcessEnv;
  try {
    env = direct
      ? { ...process.env }
      : agent?.id === "codex"
        ? buildCodexEphemeralWrapEnv(gw, codexSubscription, ephemeralMcpBinary, includeShrink, ephemeralDelegateMcp)
        : buildWrapEnv(agent, gw, opts.mcpMode);
    if (!direct && agent?.id === "claude") {
      const pluginDir = buildClaudeEphemeralPlugin(ephemeralMcpBinary, includeShrink, Boolean(opts.autoRecall), ephemeralDelegateMcp);
      childArgs = ["--plugin-dir", pluginDir, ...cmdArgs];
    }
    if (!direct && agent?.id === "aider") childArgs = routedAiderArgs(cmdArgs);
  } catch (error) {
    direct = true;
    env = { ...process.env };
    childArgs = cmdArgs;
    process.stderr.write(`${mark("warn")} temporary native pack unavailable: ${(error as Error).message}; launching ${agent?.display_name ?? bin} directly\n`);
  }
  if (!direct && agent?.id === "claude") Object.assign(env, claudeCaveBuildEnv());
  if (!direct) maybeWarnHermesMissingKey(agent, gw);
  if (direct) {
    for (const k of WRAP_BASE_URL_ENV_VARS) {
      if (env[k] === gw) delete env[k];
    }
  }
  // Only a local proxy session that survived native-pack setup can produce a
  // session-end savings summary. Direct fallback never claims routed work.
  let summaryKind: "observe" | "compress" | undefined;
  if (!direct && local && !opts.noProxy) {
    if (runtime.owner !== "unknown" && runtime.mode === "record" && observeEstimate) summaryKind = "observe";
    if (runtime.owner !== "unknown" && (runtime.mode === "compress" || runtime.mode === "pixel")) summaryKind = "compress";
  }
  const sessionStart = summaryKind ? new Date().toISOString() : undefined;
  let code: number;
  try {
    code = await new Promise<number>((resolve, reject) => {
      const invocation = portableInvocation(bin, childArgs);
      const child = spawn(invocation.command, invocation.args, { stdio: "inherit", env });
      const signalHandlers = new Map<NodeJS.Signals, () => void>();
      const removeSignalHandlers = () => {
        for (const [signal, handler] of signalHandlers) process.removeListener(signal, handler);
      };
      const wrapSignals = ["SIGHUP", "SIGINT", "SIGQUIT", "SIGTERM"] as NodeJS.Signals[];
      for (const signal of wrapSignals) {
        const handler = () => {
          // Forward the signal but defer cleanup and self-termination until the
          // child exits: deleting the wrap temp pack before the agent shuts down
          // races its SessionEnd hooks, which still read the plugin directory.
          removeSignalHandlers();
          const finish = () => {
            cleanupWrapTempDirs();
            removeProxySessionMarker(sessionMarker);
            process.kill(process.pid, signal);
          };
          // Escape hatch: a second signal, or a child that traps the first one,
          // must still clean up — the temp pack can hold copied agent
          // credentials (e.g. the Codex ephemeral home's auth.json), so dying
          // at default disposition without cleanup would strand them on disk.
          const escape = () => {
            removeSignalHandlers();
            child.kill("SIGKILL");
            finish();
          };
          for (const s of wrapSignals) {
            signalHandlers.set(s, escape);
            process.once(s, escape);
          }
          child.kill(signal);
          const grace = setTimeout(escape, 10_000);
          grace.unref();
          child.once("exit", () => {
            clearTimeout(grace);
            removeSignalHandlers();
            finish();
          });
        };
        signalHandlers.set(signal, handler);
        process.once(signal, handler);
      }
      child.on("error", (error) => {
        removeSignalHandlers();
        reject(new Error(`failed to exec ${bin}: ${error.message}`));
      });
      child.on("exit", (code, signal) => {
        removeSignalHandlers();
        resolve(code ?? signalExitCode(signal));
      });
    });
  } finally {
    cleanupWrapTempDirs();
    removeProxySessionMarker(sessionMarker);
  }
  return { code, proxyStarted, sessionStart, summaryKind };
}

export function claudeCaveBuildEnv(): NodeJS.ProcessEnv {
  const lockPath = join(process.cwd(), ".caveman", "agent.lock.json");
  let rawBeforeCheck: string;
  try {
    rawBeforeCheck = readFileSync(lockPath, "utf8");
  } catch (error) {
    if ((error as NodeJS.ErrnoException).code === "ENOENT") return {};
    throw error;
  }
  const checker = process.env.CAVEMAN_AGENT_BIN || which("caveman-agent");
  if (!checker) {
    throw new Error("Cave Build lock exists but caveman-agent checker is unavailable; refusing Claude launch before model spend");
  }
  const invocation = portableInvocation(checker, ["check"]);
  const checked = spawnSync(invocation.command, invocation.args, {
    cwd: process.cwd(),
    env: process.env,
    encoding: "utf8",
  });
  if (checked.error || checked.status !== 0) {
    const detail = String(checked.stderr || checked.error?.message || "lock check failed").trim();
    throw new Error(`Cave Build lock is stale or invalid; refusing Claude launch before model spend: ${detail}`);
  }
  const rawAfterCheck = readFileSync(lockPath, "utf8");
  if (rawAfterCheck !== rawBeforeCheck) {
    throw new Error("Cave Build lock changed during validation; refusing Claude launch before model spend");
  }
  const lock = JSON.parse(rawAfterCheck) as { harness?: { id?: unknown } };
  if (lock.harness?.id !== "claude") {
    throw new Error(
      "Cave Build is Pi-specific; refusing to attach its identity to Claude Code execution",
    );
  }
  throw new Error(
    "Claude-specific Cave Build execution is unavailable until model, reasoning, budget, recovery, and wire selectors are enforced",
  );
}

function firstEnvSecret(env: NodeJS.ProcessEnv, keys: string[]): string | undefined {
  for (const key of keys) {
    const value = env[key];
    if (typeof value === "string" && value.trim()) return value.trim();
  }
  return undefined;
}

function maybeWarnHermesMissingKey(agent: AgentProfile | undefined, gw = gatewayURL()) {
  if (agent?.id !== "hermes") return;
  if (wrapMode(gw) === "local") {
    if (!firstEnvSecret(process.env, HERMES_LOCAL_UPSTREAM_KEY_VARS)) {
      process.stderr.write("caveman: Hermes local wrap has no upstream provider key in env for the local proxy to forward; launching anyway, but provider auth may fail\n");
    }
    return;
  }
	if (!connectedGatewayAPIKey()) {
    process.stderr.write("caveman: Hermes managed wrap has no CAVE_API_KEY; launching anyway, but provider auth may fail\n");
    return;
  }
  if (!hermesHostDerivedApiKeyEnvName(gw)) {
    process.stderr.write("caveman: Hermes cannot derive a managed API-key env var from CAVE_GATEWAY_URL; launching anyway, but provider auth may fail\n");
  }
}

const HERMES_LOCAL_UPSTREAM_KEY_VARS = ["OPENAI_API_KEY", "ANTHROPIC_API_KEY", "GEMINI_API_KEY", "GOOGLE_API_KEY", "OPENROUTER_API_KEY"];
const HERMES_HOST_DERIVED_KEY_DENYLIST = new Set(["OPENAI_API_KEY", "OPENROUTER_API_KEY", "OLLAMA_API_KEY"]);

function hermesHostDerivedApiKeyEnvName(gw: string): string | undefined {
  try {
    const url = new URL(gw);
    let host = url.hostname.trim().toLowerCase();
    if (host.startsWith("[") && host.endsWith("]")) host = host.slice(1, -1);
    if (!host || host === "localhost" || isIP(host)) return undefined;
    const labels = host.split(".").filter(Boolean);
    while (labels[0] === "api" || labels[0] === "www") labels.shift();
    if (labels.length < 2) return undefined;
    // Mirrors Hermes custom-provider key derivation
    // (~/.hermes/hermes-agent/hermes_cli/runtime_provider.py:158-215).
    const vendor = labels[labels.length - 2]!.replace(/[^A-Za-z0-9_]/g, "_").toUpperCase();
    if (!/^[A-Z]/.test(vendor)) return undefined;
    const name = `${vendor}_API_KEY`;
    if (HERMES_HOST_DERIVED_KEY_DENYLIST.has(name)) return undefined;
    return name;
  } catch {
    return undefined;
  }
}

function applyHermesAuthEnv(env: NodeJS.ProcessEnv, gw: string, modeGw = gw) {
  delete env.CUSTOM_API_KEY;
  if (wrapMode(modeGw) !== "managed") return;
	const key = firstEnvSecret(env, ["CAVE_API_KEY"]) ?? connectedGatewayAPIKey();
  const name = hermesHostDerivedApiKeyEnvName(gw);
  if (key && name) env[name] = key;
}

async function startWrapProxy(mode: WrapRuntimeMode, mcpRecovery: boolean, toon: boolean, pixelModels: string | undefined, pixelDensity: string | undefined, gw = gatewayURL(), purpose: "standard" | "codex-subscription" = "standard", observeEstimate = false): Promise<boolean> {
  const bin = cavemanBin("caveman-proxy", "CAVEMAN_PROXY_BIN");
  const resolved = which(bin);
  if (!resolved) {
    if (purpose === "codex-subscription") {
      process.stderr.write(`${mark("warn")} ${bin} not found; codex subscription traffic will not route through /chatgpt — run ${cyan("caveman setup")} to see what's missing\n`);
    } else {
      process.stderr.write(`${mark("warn")} ${bin} not found; wrap will still launch, but no local compression/metering will run — run ${cyan("caveman setup")} to see what's missing\n`);
    }
    return false;
  }
  const { host, port } = gatewayHostPort(gw);
  const env: NodeJS.ProcessEnv = {
    ...process.env,
    CAVEMAN_PROXY_OWNER: purpose === "standard" ? "wrap" : "start",
    CAVEMAN_MODE: mode,
    CAVEMAN_LISTEN: `${host}:${port}`,
    // The recovery half of the proxy's subscription gate, and the switch that lets
    // it compress streams at all. Stamped EXPLICITLY in both directions: wrap
    // derives it from the agent's OWN MCP install (and
    // forces it off for codex-subscription and observe-only runs), so an exported
    // CAVEMAN_RECOVERY=mcp must not survive that answer — the proxy would elide
    // spans behind markers this agent has no caveman_retrieve tool to expand, while
    // the CLI printed that compression was off. (honesty rule: no-placeholder)
    CAVEMAN_RECOVERY: mcpRecovery ? "mcp" : "",
    // best-of JSON routing lets the engine re-encode uniform JSON the model reads
    // (notably tool_result blocks) as TOON whenever that is fewer tokens.
    ...(toon ? { CAVE_ENGINE_TOON: "best-of" } : {}),
    ...(pixelModels ? { CAVE_PIXEL_MODELS: pixelModels } : {}),
    ...(pixelDensity ? { CAVE_PIXEL_DENSITY: pixelDensity } : {}),
    // Mantle is a distinct, opt-in adapter route. Selecting the Claude Mantle
    // endpoint must enable the local proxy side as well as the child env.
    ...(process.env.CAVEMAN_WRAP_PROVIDER?.trim().toLowerCase() === "bedrock"
      && process.env.CAVEMAN_BEDROCK_ENDPOINT?.trim().toLowerCase() === "mantle"
      ? { CAVE_BEDROCK_MANTLE_ENABLED: "1" }
      : {}),
    // Observe-only estimate: record mode measures would-have-saved tokens without
    // ever mutating the forwarded request.
    ...(observeEstimate ? { CAVEMAN_OBSERVE_ESTIMATE: "1" } : {}),
    // Nothing else is stamped for subscription/OAuth compression: it
    // has no account condition. The operator off-switch (`subscription_compress:
    // off`) stays the operator's — we never override it from here.
  };
  // Same reason as `start`: the dead account variable never rides along inherited.
  delete env.CAVEMAN_WRAP_ENTITLED;
  const child = spawn(resolved, [], { stdio: "ignore", env, detached: true, windowsHide: true });
  child.unref();
  for (let i = 0; i < 20; i++) {
    await sleep(100);
    if (await portListening(host, port)) {
      process.stderr.write(dim(`→ started Caveman proxy on ${host}:${port} (${env.CAVEMAN_MODE})\n`));
      return true;
    }
  }
  process.stderr.write(`${mark("warn")} started ${bin}, but proxy did not become ready on ${host}:${port}\n`);
  return false;
}

// wrapMode selects which injection variant to use: "managed" when traffic is aimed
// off-loopback (CAVE_GATEWAY_URL points at a hosted gateway), else "local". Keying
// off where the bytes actually go means `caveman start` (local proxy) and a hosted
// gateway each select the right config with no extra flag.
function wrapMode(gw = gatewayURL()): WrapMode {
  const { host } = gatewayHostPort(gw);
  return host === "127.0.0.1" || host === "localhost" || host === "::1" ? "local" : "managed";
}

// renderTemplate resolves Caveman's {{cave_*}} placeholders to concrete values. It
// deliberately leaves an agent's own {env:VAR} tokens untouched (different syntax),
// so e.g. opencode resolves those at its runtime. Unset values render empty — and
// an env injection omits a var that renders empty, so we never set an empty token.
function renderTemplate(s: string, gw = gatewayURL()): string {
  // A project gateway key authenticates only to the hosted gateway. Never
  // substitute it into local provider-auth variables, where an agent or local
  // proxy could mistake it for an upstream provider credential.
  const caveAPIKey = wrapMode(gw) === "managed" ? connectedGatewayAPIKey() : "";
  return s
    .replaceAll("{{cave_base_url}}", gw)
    .replaceAll("{{cave_proxy_url}}", gw)
    .replaceAll("{{cave_api_key}}", caveAPIKey)
    .replaceAll("{{cave_org_id}}", orgIdFromConfigFile());
}

// renderDeep applies renderTemplate to every string leaf of a JSON value — used to
// render an agent's inline-config template before it is stringified into an env var.
function renderDeep(v: unknown, gw = gatewayURL()): unknown {
  if (typeof v === "string") return renderTemplate(v, gw);
  if (Array.isArray(v)) return v.map((item) => renderDeep(item, gw));
  if (v && typeof v === "object") {
    const out: Record<string, unknown> = {};
    for (const [k, val] of Object.entries(v as Record<string, unknown>)) out[k] = renderDeep(val, gw);
    return out;
  }
  return v;
}

function stripJson5Comments(s: string): string {
  let out = "";
  let inString = false;
  let escaped = false;
  for (let i = 0; i < s.length; i++) {
    const ch = s[i]!;
    const next = s[i + 1];
    if (inString) {
      out += ch;
      if (escaped) escaped = false;
      else if (ch === "\\") escaped = true;
      else if (ch === "\"") inString = false;
      continue;
    }
    if (ch === "\"") {
      inString = true;
      out += ch;
      continue;
    }
    if (ch === "/" && next === "/") {
      i += 2;
      while (i < s.length && s[i] !== "\n" && s[i] !== "\r") i++;
      if (i < s.length) out += s[i]!;
      continue;
    }
    if (ch === "/" && next === "*") {
      i += 2;
      while (i < s.length && !(s[i] === "*" && s[i + 1] === "/")) {
        if (s[i] === "\n" || s[i] === "\r") out += s[i];
        i++;
      }
      if (i < s.length) i++;
      continue;
    }
    out += ch;
  }
  return out;
}

function stripTrailingCommas(s: string): string {
  let out = "";
  let inString = false;
  let escaped = false;
  for (let i = 0; i < s.length; i++) {
    const ch = s[i]!;
    if (inString) {
      out += ch;
      if (escaped) escaped = false;
      else if (ch === "\\") escaped = true;
      else if (ch === "\"") inString = false;
      continue;
    }
    if (ch === "\"") {
      inString = true;
      out += ch;
      continue;
    }
    if (ch === ",") {
      let j = i + 1;
      while (j < s.length && /\s/.test(s[j]!)) j++;
      if (s[j] === "}" || s[j] === "]") continue;
    }
    out += ch;
  }
  return out;
}

export function readJson5Lenient(path: string): unknown {
  const raw = readFileSync(path, "utf8");
  try {
    return JSON.parse(raw);
  } catch {
    return JSON.parse(stripTrailingCommas(stripJson5Comments(raw)));
  }
}

function isPlainObject(v: unknown): v is Record<string, unknown> {
  if (!v || typeof v !== "object" || Array.isArray(v)) return false;
  const proto = Object.getPrototypeOf(v);
  return proto === Object.prototype || proto === null;
}

export function deepMerge(base: unknown, overlay: unknown): unknown {
  if (isPlainObject(base) && isPlainObject(overlay)) {
    const out: Record<string, unknown> = { ...base };
    for (const [k, v] of Object.entries(overlay)) out[k] = deepMerge(out[k], v);
    return out;
  }
  return overlay;
}

type ConfigFileInjection = Extract<AgentProfile["injection"], { method: "config-file" }>;

function baseConfigPath(base: NonNullable<ConfigFileInjection["base_config"]>): string {
  const envPath = base.env_var ? process.env[base.env_var] : undefined;
  if (envPath && envPath.trim()) return expandTilde(envPath);
  const stateDir = base.state_dir?.env_var ? process.env[base.state_dir.env_var] : undefined;
  if (stateDir && stateDir.trim()) return join(expandTilde(stateDir), base.state_dir!.filename);
  return expandTilde(base.path);
}

function readBaseConfig(inj: ConfigFileInjection): unknown {
  if (!inj.base_config) return {};
  const path = baseConfigPath(inj.base_config);
  try {
    return readJson5Lenient(path);
  } catch (e) {
    if ((e as NodeJS.ErrnoException).code === "ENOENT") return {};
    throw e;
  }
}

type JsonObject = Record<string, unknown>;
type OpenClawModelRef = { provider: string; model: string; raw: string };

const OPENCLAW_PLUGIN_ID = "caveman-shrink";
const OPENCLAW_AGENT_HEADER = "x-cave-agent";

// OpenClaw source/docs checked for this route table:
// - openai-completions: OpenAI SDK chat.completions.create => <baseUrl>/chat/completions.
// - openai-responses: OpenAI SDK responses.create => <baseUrl>/responses.
// - anthropic-messages: provider-stream appends /v1/messages unless baseUrl already ends /v1.
// - google-generative-ai: Google transport uses /v1beta/models/<id>:streamGenerateContent?alt=sse when baseUrl includes /v1beta.
// The Caveman proxy exposes provider-native routes under /v1 for OpenAI-compatible
// requests, bare /v1/messages for Anthropic, and /v1beta for Gemini.
const OPENCLAW_API_BASE_PATH: Record<string, string> = {
  "openai-completions": "/v1",
  "openai-responses": "/v1",
  "anthropic-messages": "",
  "google-generative-ai": "/v1beta",
};

const OPENCLAW_WELL_KNOWN_PROVIDERS: Record<string, JsonObject> = {
  openai: { api: "openai-responses", apiKey: "${OPENAI_API_KEY}" },
  anthropic: { api: "anthropic-messages", apiKey: "${ANTHROPIC_API_KEY}" },
  google: { api: "google-generative-ai", apiKey: "${GEMINI_API_KEY}" },
  "openai-codex": { api: "openai-chatgpt-responses", auth: "oauth" },
};

const OPENCLAW_MODEL_DEFAULTS = {
  reasoning: false,
  input: ["text"],
  cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 },
  contextWindow: 200000,
  maxTokens: 8192,
};

const OPENCLAW_FRESH_MODEL = "gpt-5.5";

function asJsonObject(v: unknown): JsonObject | undefined {
  return isPlainObject(v) ? v : undefined;
}

function getObject(root: unknown, path: string[]): JsonObject | undefined {
  let cur: unknown = root;
  for (const key of path) {
    const obj = asJsonObject(cur);
    if (!obj) return undefined;
    cur = obj[key];
  }
  return asJsonObject(cur);
}

function getString(root: unknown, path: string[]): string | undefined {
  let cur: unknown = root;
  for (const key of path) {
    const obj = asJsonObject(cur);
    if (!obj) return undefined;
    cur = obj[key];
  }
  return typeof cur === "string" && cur.trim() ? cur.trim() : undefined;
}

function openClawModelKey(ref: OpenClawModelRef): string {
  return ref.model.toLowerCase().startsWith(`${ref.provider.toLowerCase()}/`) ? ref.model : `${ref.provider}/${ref.model}`;
}

function parseOpenClawModelRef(raw: string | undefined): OpenClawModelRef | undefined {
  if (!raw) return undefined;
  const slash = raw.indexOf("/");
  if (slash <= 0 || slash >= raw.length - 1) return undefined;
  return { provider: raw.slice(0, slash), model: raw.slice(slash + 1), raw };
}

function resolveOpenClawPrimaryRef(config: unknown): OpenClawModelRef | undefined {
  const primary = getString(config, ["agents", "defaults", "model", "primary"]);
  const parsed = parseOpenClawModelRef(primary);
  if (parsed) return parsed;
  const models = getObject(config, ["agents", "defaults", "models"]);
  if (!models) return undefined;
  for (const key of Object.keys(models)) {
    const fallback = parseOpenClawModelRef(key);
    if (fallback) return fallback;
  }
  return undefined;
}

function resolveOpenClawProvider(config: unknown, providerId: string): JsonObject | undefined {
  const configured = getObject(config, ["models", "providers", providerId]);
  if (configured) return configured;
  return OPENCLAW_WELL_KNOWN_PROVIDERS[providerId];
}

function openClawProviderConfigured(config: unknown, providerId: string): boolean {
  return !!getObject(config, ["models", "providers", providerId]);
}

function envTemplateVar(value: unknown): string | undefined {
  if (typeof value !== "string") return undefined;
  const match = value.trim().match(/^\$\{([A-Za-z_][A-Za-z0-9_]*)\}$/);
  return match?.[1];
}

function resolveEnvTemplate(value: string, env: NodeJS.ProcessEnv = process.env): string | undefined {
  const envVar = envTemplateVar(value);
  if (!envVar) return value.trim() || undefined;
  const resolved = env[envVar];
  return typeof resolved === "string" && resolved.trim() ? resolved.trim() : undefined;
}

function openClawProviderUsesOAuth(providerId: string, provider: JsonObject, configuredProvider: boolean): boolean {
  if (providerId === "openai-codex") return true;
  const auth = provider.auth;
  if (typeof auth === "string") return auth.toLowerCase() === "oauth";
  if (asJsonObject(auth) && typeof (auth as JsonObject).mode === "string") {
    return String((auth as JsonObject).mode).toLowerCase() === "oauth";
  }
  if (!configuredProvider && ["openai", "anthropic", "google"].includes(providerId)) {
    const envVar = envTemplateVar(openClawProviderApiKey(providerId, provider));
    if (envVar && !firstEnvSecret(process.env, [envVar])) return true;
  }
  return false;
}

function openClawProviderApi(providerId: string, provider: JsonObject, model?: JsonObject): string | undefined {
  const modelApi = typeof model?.api === "string" ? model.api : undefined;
  const providerApi = typeof provider.api === "string" ? provider.api : undefined;
  const wellKnownApi = typeof OPENCLAW_WELL_KNOWN_PROVIDERS[providerId]?.api === "string"
    ? String(OPENCLAW_WELL_KNOWN_PROVIDERS[providerId]!.api)
    : undefined;
  return modelApi || providerApi || wellKnownApi;
}

function openClawProviderModel(provider: JsonObject, modelId: string): JsonObject | undefined {
  const models = Array.isArray(provider.models) ? provider.models : [];
  for (const item of models) {
    const model = asJsonObject(item);
    if (model && model.id === modelId) return model;
  }
  return undefined;
}

function openClawModelConfig(config: unknown, ref: OpenClawModelRef): JsonObject | undefined {
  const models = getObject(config, ["agents", "defaults", "models"]);
  return asJsonObject(models?.[openClawModelKey(ref)]) ?? asJsonObject(models?.[ref.raw]);
}

function openClawSecretString(v: unknown): string | undefined {
  return typeof v === "string" && v.trim() ? v.trim() : undefined;
}

function openClawDefaultApiKey(providerId: string): string | undefined {
  return openClawSecretString(OPENCLAW_WELL_KNOWN_PROVIDERS[providerId]?.apiKey);
}

function openClawProviderApiKey(providerId: string, provider: JsonObject): string | undefined {
  return openClawSecretString(provider.apiKey) ?? openClawDefaultApiKey(providerId);
}

function openClawResolvedProviderApiKey(providerId: string, provider: JsonObject): string | undefined {
  const key = openClawProviderApiKey(providerId, provider);
  return key ? resolveEnvTemplate(key) : undefined;
}

function freshOpenClawModelRef(ctx: OverlayBuilderContext): OpenClawModelRef {
  const requiredKey = ctx.mode === "managed" ? "CAVE_API_KEY" : "OPENAI_API_KEY";
  if (!firstEnvSecret(ctx.env, [requiredKey])) {
    throw new Error(`openclaw fresh config cannot route through Caveman without ${requiredKey}`);
  }
  return { provider: "openai", model: OPENCLAW_FRESH_MODEL, raw: `openai/${OPENCLAW_FRESH_MODEL}` };
}

function appendUrlPath(base: string, path: string): string {
  return `${base.replace(/\/+$/, "")}${path}`;
}

function codexHomeDir(): string {
  return join(homedir(), ".codex");
}

function codexAuthPath(): string {
  return join(codexHomeDir(), "auth.json");
}

function nonEmptyString(v: unknown): v is string {
  return typeof v === "string" && v.trim().length > 0;
}

function readCodexAuthJson(): JsonObject | undefined {
  const path = codexAuthPath();
  try {
    const stat = statSync(path);
    if (!stat.isFile() || stat.size === 0) return undefined;
    const parsed = JSON.parse(readFileSync(path, "utf8")) as unknown;
    return asJsonObject(parsed);
  } catch {
    return undefined;
  }
}

function codexAuthHasApiKey(auth: JsonObject): boolean {
  return nonEmptyString(auth.OPENAI_API_KEY) || nonEmptyString(process.env.OPENAI_API_KEY);
}

function codexAuthHasChatGptTokens(auth: JsonObject): boolean {
  const tokens = asJsonObject(auth.tokens);
  if (!tokens) return false;
  for (const key of ["account_id", "access_token", "refresh_token", "id_token"]) {
    if (nonEmptyString(tokens[key])) return true;
  }
  return Object.keys(tokens).length > 0;
}

function detectCodexWrapAuthMode(): CodexWrapAuthMode {
  const auth = readCodexAuthJson();
  if (!auth) return "api-key";
  return codexAuthHasChatGptTokens(auth) && !codexAuthHasApiKey(auth) ? "subscription" : "api-key";
}

function codexTomlSectionName(line: string): string | undefined {
  const match = line.match(/^\s*\[([^\]]+)\]\s*(?:#.*)?$/);
  return match?.[1]?.trim();
}

function stripCodexCavemanProviderToml(text: string): string {
  const out: string[] = [];
  let section = "";
  let skippingCavemanProvider = false;
  for (const line of text.replace(/\r\n/g, "\n").split("\n")) {
    const nextSection = codexTomlSectionName(line);
    if (skippingCavemanProvider) {
      if (!nextSection) continue;
      skippingCavemanProvider = false;
    }
    if (nextSection !== undefined) {
      section = nextSection;
      if (section === "model_providers.caveman") {
        skippingCavemanProvider = true;
        continue;
      }
      out.push(line);
      continue;
    }
    if (section === "" && /^\s*model_provider\s*=/.test(line)) continue;
    out.push(line);
  }
  return out.join("\n").trimEnd();
}

function codexCavemanProviderToml(gw: string, subscription = true): string {
  return [
    `model_provider = "caveman"`,
    `[model_providers.caveman]`,
    `name = "Caveman"`,
    `base_url = ${JSON.stringify(subscription ? appendUrlPath(gw, "/chatgpt") : appendUrlPath(gw, "/w/codex"))}`,
    `wire_api = "responses"`,
    `requires_openai_auth = true`,
  ].join("\n");
}

function stripCodexCavemanMcpToml(text: string): string {
  const out: string[] = [];
  let skipping = false;
  for (const line of text.replace(/\r\n/g, "\n").split("\n")) {
    const section = codexTomlSectionName(line);
    if (skipping && section === undefined) continue;
    if (skipping) skipping = false;
    if (section === "mcp_servers.caveman") {
      skipping = true;
      continue;
    }
    out.push(line);
  }
  return out.join("\n").trimEnd();
}

function linkCodexReadOnly(sourceHome: string, outDir: string, name: string) {
  const source = join(sourceHome, name);
  try {
    statSync(source);
    symlinkSync(source, join(outDir, name));
  } catch {
    // Optional context only; auth/config are the required ephemeral inputs.
  }
}

function readJsonObject(path: string): Record<string, unknown> {
  try {
    const value = JSON.parse(readFileSync(path, "utf8"));
    return value && typeof value === "object" && !Array.isArray(value) ? value as Record<string, unknown> : {};
  } catch {
    return {};
  }
}

export function normalizeHookPath(
  path: string,
  platform: NodeJS.Platform = process.platform,
): string {
  return platform === "win32" ? path.replace(/\\/g, "/") : path;
}

export function quoteHookPath(
  path: string,
  platform: NodeJS.Platform = process.platform,
): string {
  const normalized = normalizeHookPath(path, platform);
  if (platform === "win32") return `'${normalized.replace(/'/g, "''")}'`;
  return `'${normalized.replace(/'/g, `'"'"'`)}'`;
}

export function nativeHookInvocation(
  executable: string,
  fastHook: string,
  agentId: string,
  executableIsProxy: boolean,
  platform: NodeJS.Platform = process.platform,
): string {
  const executableInvocation = hookExecutableInvocation(
    executable,
    executableIsProxy ? undefined : fastHook,
    platform,
  );
  const invocation = executableIsProxy
    ? `${executableInvocation} native-hook ${agentId} --adapter ${quoteHookPath(fastHook, platform)}`
    : `${executableInvocation} native-hook ${agentId}`;
  return invocation;
}

export function hookExecutableInvocation(
  executable: string,
  script: string | undefined,
  platform: NodeJS.Platform = process.platform,
): string {
  const invocation = script
    ? `${quoteHookPath(executable, platform)} ${quoteHookPath(script, platform)}`
    : quoteHookPath(executable, platform);
  // Claude/Codex/Gemini dispatch command hooks through PowerShell on native
  // Windows. A quoted executable is only a string literal there; `&` is the
  // required invocation operator. POSIX hook commands keep their exact shape.
  return platform === "win32" ? `& ${invocation}` : invocation;
}

function nativeHookCommand(agentId: string): string {
  const fastHook = join(dirname(fileURLToPath(import.meta.url)), "native-hook-fast.js");
  const explicitProxy = process.env.CAVEMAN_PROXY_BIN;
  const localProxy = join(cavemanHome(), "bin", process.platform === "win32" ? "caveman-proxy.exe" : "caveman-proxy");
  const proxy = explicitProxy || which("caveman-proxy") || (isExecutable(localProxy) ? localProxy : undefined);
  const bridgeCurrent = proxy ? probeVersionedBinary(proxy, "native_hook_bridge_v1").current : false;
  if (proxy && bridgeCurrent && existsSync(fastHook)) {
    return nativeHookInvocation(proxy, fastHook, agentId, true);
  }
  if (existsSync(fastHook)) {
    return nativeHookInvocation(process.execPath, fastHook, agentId, false);
  }
  return `${cavemanBinForHook()} native-hook ${agentId}`;
}

function nativeHookEntry(command: string): Record<string, unknown> {
  return { hooks: [{ type: "command", command }] };
}

function hookEntryCommand(entry: Record<string, unknown>): string | undefined {
  const hooks = Array.isArray(entry.hooks) ? entry.hooks as Array<Record<string, unknown>> : [];
  if (hooks.length !== 1 || hooks[0]?.type !== "command") return undefined;
  return typeof hooks[0].command === "string" ? hooks[0].command : undefined;
}

function nativeHooksDocument(agentId: "claude" | "codex" | "gemini", includeShrink: boolean, base: Record<string, unknown> = {}, includeRecall = false): Record<string, unknown> {
  const root = JSON.parse(JSON.stringify(base)) as Record<string, unknown>;
  const hooks = root.hooks && typeof root.hooks === "object" && !Array.isArray(root.hooks)
    ? root.hooks as Record<string, unknown>
    : {};
  const lifecycle = agentId === "gemini"
    ? ["SessionStart", "BeforeAgent", "BeforeModel", "BeforeTool", "AfterTool", "AfterModel", "PreCompress", "AfterAgent", "SessionEnd"]
    : agentId === "codex"
    ? ["SessionStart", "UserPromptSubmit", "PreToolUse", "PermissionRequest", "PostToolUse", "PostToolUseFailure", "PreCompact", "PostCompact", "SubagentStart", "SubagentStop", "Stop", "SessionEnd"]
    : ["SessionStart", "UserPromptSubmit", "PreToolUse", "PostToolUse", "PostToolUseFailure", "PreCompact", "PostCompact", "SubagentStart", "SubagentStop", "Stop", "SessionEnd"];
  const command = nativeHookCommand(agentId);
  for (const event of lifecycle) {
    const list = Array.isArray(hooks[event]) ? hooks[event] as Array<Record<string, unknown>> : [];
    if (!list.some((entry) => hookEntryCommand(entry) === command)) list.push(nativeHookEntry(command));
    hooks[event] = list;
  }
  if (includeShrink) {
    const shrinkEvent = agentId === "gemini" ? "BeforeTool" : "PreToolUse";
    const list = Array.isArray(hooks[shrinkEvent]) ? hooks[shrinkEvent] as Array<Record<string, unknown>> : [];
    const shrinkCommand = `${cavemanBinForHook()} shrink-hook`;
    if (!list.some((entry) => hookEntryCommand(entry) === shrinkCommand)) {
      list.push(agentId === "gemini"
        ? { matcher: "run_shell_command", ...nativeHookEntry(shrinkCommand) }
        : nativeHookEntry(shrinkCommand));
    }
    hooks[shrinkEvent] = list;
  }
  if (agentId === "claude" && includeRecall) {
    const list = Array.isArray(hooks.UserPromptSubmit) ? hooks.UserPromptSubmit as Array<Record<string, unknown>> : [];
    const recallCommand = `${cavemanBinForHook()} mem recall-hook`;
    if (!list.some((entry) => hookEntryCommand(entry) === recallCommand)) {
      list.push(nativeHookEntry(recallCommand));
    }
    hooks.UserPromptSubmit = list;
  }
  root.hooks = hooks;
  return root;
}

function assertNativeHooksShape(path: string, root: Record<string, unknown>, agentId: "claude" | "codex" | "gemini"): void {
  if (root.hooks !== undefined && (typeof root.hooks !== "object" || root.hooks === null || Array.isArray(root.hooks))) {
    throw new Error(`${path} hooks must be a JSON object; refusing to overwrite it`);
  }
  const hooks = root.hooks as Record<string, unknown> | undefined;
  if (!hooks) return;
  const expected = nativeHooksDocument(agentId, true).hooks as Record<string, unknown>;
  for (const event of Object.keys(expected)) {
    if (hooks[event] !== undefined && !Array.isArray(hooks[event])) {
      throw new Error(`${path} hooks.${event} must be an array; refusing to overwrite it`);
    }
  }
}

function nativeHookEntriesHealthy(root: Record<string, unknown>, agentId: "claude" | "codex" | "gemini"): boolean {
  const hooks = root.hooks && typeof root.hooks === "object" && !Array.isArray(root.hooks)
    ? root.hooks as Record<string, unknown>
    : undefined;
  if (!hooks) return false;
  const expected = nativeHooksDocument(agentId, true).hooks as Record<string, unknown>;
  return Object.entries(expected).every(([event, expectedRaw]) => {
    const actual = Array.isArray(hooks[event]) ? hooks[event] as Array<Record<string, unknown>> : [];
    const actualEntries = new Set(actual.map((entry) => JSON.stringify(entry)));
    return (expectedRaw as Array<Record<string, unknown>>).every((entry) => actualEntries.has(JSON.stringify(entry)));
  });
}

function buildCodexEphemeralHome(
  gw: string,
  subscription: boolean,
  mcpBinary: string | undefined,
  includeShrink: boolean,
  delegateMcp: { command: string; args: string[] } | null,
): string {
  const sourceHome = codexHomeDir();
  const outDir = mkdtempSync(join(tmpdir(), "caveman-wrap-"));
  wrapTempDirs.add(outDir);
  try {
    writeFileSync(join(outDir, "auth.json"), readFileSync(codexAuthPath()), { mode: 0o600 });
  } catch {
    // API-key sessions may have no auth.json; OPENAI_API_KEY stays inherited.
  }

  let sourceConfig = "";
  try {
    sourceConfig = readFileSync(join(sourceHome, "config.toml"), "utf8");
  } catch {
    sourceConfig = "";
  }
  const stripped = stripCodexCavemanMcpToml(stripCodexCavemanProviderToml(sourceConfig));
  const providerLines = codexCavemanProviderToml(gw, subscription).split("\n");
  const providerRoot = providerLines.shift()!;
  const providerTables = providerLines.join("\n");
  const mcp = mcpBinary
    ? `\n\n[mcp_servers.caveman]\ncommand = ${JSON.stringify(mcpBinary)}\n`
    : "\n";
  const delegateArgs = delegateMcp?.args.length
    ? `\nargs = [${delegateMcp.args.map((arg) => JSON.stringify(arg)).join(", ")}]`
    : "";
  const delegate = delegateMcp && !/(^|\n)\s*\[mcp_servers\.caveman-delegate\]\s*(?:\r?\n|$)/m.test(stripped)
    ? `\n[mcp_servers.caveman-delegate]\ncommand = ${JSON.stringify(delegateMcp.command)}${delegateArgs}\n`
    : "";
  writeFileSync(
    join(outDir, "config.toml"),
    `${providerRoot}${stripped ? `\n\n${stripped}` : ""}\n\n${providerTables}${mcp}${delegate}`,
    { mode: 0o600 },
  );

  const hooks = nativeHooksDocument("codex", includeShrink, readJsonObject(join(sourceHome, "hooks.json")));
  writeFileSync(join(outDir, "hooks.json"), JSON.stringify(hooks, null, 2) + "\n", { mode: 0o600 });

  linkCodexReadOnly(sourceHome, outDir, "skills");
  linkCodexReadOnly(sourceHome, outDir, "AGENTS.md");
  return outDir;
}

function buildCodexEphemeralWrapEnv(
  gw: string,
  subscription: boolean,
  mcpBinary: string | undefined,
  includeShrink: boolean,
  delegateMcp: { command: string; args: string[] } | null,
): NodeJS.ProcessEnv {
  const env: NodeJS.ProcessEnv = { ...process.env };
  for (const key of WRAP_BASE_URL_ENV_VARS) delete env[key];
  env.CODEX_HOME = buildCodexEphemeralHome(gw, subscription, mcpBinary, includeShrink, delegateMcp);
  return env;
}

function buildClaudeEphemeralPlugin(
  mcpBinary: string | undefined,
  includeShrink: boolean,
  includeRecall: boolean,
  delegateMcp: { command: string; args: string[] } | null,
): string {
  const dir = mkdtempSync(join(tmpdir(), "caveman-wrap-claude-"));
  wrapTempDirs.add(dir);
  mkdirSync(join(dir, ".claude-plugin"), { recursive: true });
  mkdirSync(join(dir, "hooks"), { recursive: true });
  writeFileSync(join(dir, ".claude-plugin", "plugin.json"), JSON.stringify({
    name: "caveman-wrap",
    version: cliVersion(),
    description: "Ephemeral Caveman local-engine pack",
  }, null, 2) + "\n", { mode: 0o600 });
  writeFileSync(join(dir, "hooks", "hooks.json"), JSON.stringify(nativeHooksDocument("claude", includeShrink, {}, includeRecall), null, 2) + "\n", { mode: 0o600 });
  if (mcpBinary || delegateMcp) {
    const mcpServers: Record<string, { command: string; args: string[] }> = {};
    if (mcpBinary) mcpServers.caveman = { command: mcpBinary, args: [] };
    if (delegateMcp) mcpServers["caveman-delegate"] = delegateMcp;
    writeFileSync(join(dir, ".mcp.json"), JSON.stringify({
      mcpServers,
    }, null, 2) + "\n", { mode: 0o600 });
  }
  return dir;
}

type NativeAgent = "claude" | "codex" | "hermes" | "gemini" | "opencode" | "aider";

const NATIVE_CAPABILITIES = [
  "provider_proxy", "session_start", "prompt_submit", "model_before", "model_after",
  "pre_tool", "post_tool", "post_tool_rewrite", "pre_compact", "post_compact",
  "session_end", "subagents", "mcp", "skills", "native_package",
  "ide_shared_config", "local_runtime_available",
] as const;
type NativeCapability = typeof NATIVE_CAPABILITIES[number];

const NATIVE_CAPABILITY_SUPPORT: Record<NativeAgent, ReadonlySet<NativeCapability>> = {
  claude: new Set(["provider_proxy", "session_start", "prompt_submit", "pre_tool", "post_tool", "post_tool_rewrite", "pre_compact", "post_compact", "session_end", "subagents", "mcp", "skills", "local_runtime_available"]),
  codex: new Set(["provider_proxy", "session_start", "prompt_submit", "pre_tool", "post_tool", "pre_compact", "post_compact", "session_end", "subagents", "mcp", "skills", "local_runtime_available"]),
  hermes: new Set(["provider_proxy", "session_start", "prompt_submit", "model_after", "pre_tool", "post_tool", "session_end", "mcp", "native_package", "local_runtime_available"]),
  gemini: new Set(["provider_proxy", "session_start", "prompt_submit", "model_before", "model_after", "pre_tool", "post_tool", "pre_compact", "session_end", "mcp", "local_runtime_available"]),
  opencode: new Set(["provider_proxy", "session_start", "prompt_submit", "pre_tool", "post_tool", "post_tool_rewrite", "pre_compact", "post_compact", "session_end", "mcp", "native_package", "skills", "local_runtime_available"]),
  aider: new Set(["provider_proxy", "local_runtime_available"]),
};

type NativeComponents = {
  routing: boolean;
  lifecycle_hooks: boolean;
  core: boolean;
  mcp_recovery: boolean;
  tool_rewrite: boolean;
  shared_runtime: boolean;
};

function parsedSemver(value: string | null | undefined): [number, number, number] | undefined {
  const match = value?.match(/(?:^|[^0-9])(\d+)\.(\d+)\.(\d+)(?:[^0-9]|$)/);
  return match ? [Number(match[1]), Number(match[2]), Number(match[3])] : undefined;
}

function compareSemver(left: [number, number, number], right: [number, number, number]): number {
  for (let i = 0; i < 3; i++) {
    if (left[i] !== right[i]) return left[i]! < right[i]! ? -1 : 1;
  }
  return 0;
}

function nativeVersionStatus(observed: string | null, tested: string | undefined): "unreported" | "tested" | "newer_unknown" | "older" | "unknown" {
  const observedSemver = parsedSemver(observed);
  const testedSemver = parsedSemver(tested);
  if (!observedSemver) return "unreported";
  if (!testedSemver) return "unknown";
  const relation = compareSemver(observedSemver, testedSemver);
  return relation === 0 ? "tested" : relation > 0 ? "newer_unknown" : "older";
}

function nativeCapabilityReport(agent: NativeAgent, components: NativeComponents, versionStatus: ReturnType<typeof nativeVersionStatus>) {
  const supported = NATIVE_CAPABILITY_SUPPORT[agent];
  const lifecycleActive = components.lifecycle_hooks && components.shared_runtime;
  const active = (capability: NativeCapability): boolean => {
    if (!supported.has(capability)) return false;
    if (capability === "provider_proxy") return components.routing;
    if (capability === "local_runtime_available") return components.shared_runtime;
    if (capability === "mcp") return components.mcp_recovery;
    if (capability === "skills" || capability === "ide_shared_config") return false;
    if (capability === "native_package") return components.lifecycle_hooks;
    if (capability === "post_tool_rewrite") return lifecycleActive && (agent === "claude" || agent === "opencode");
    return lifecycleActive;
  };
  const basis = versionStatus === "tested" ? "tested_manifest" : "installed_surface_probe_safe_subset";
  return Object.fromEntries(NATIVE_CAPABILITIES.map((capability) => [capability, {
    supported: supported.has(capability),
    active: active(capability),
    basis,
  }])) as Record<NativeCapability, { supported: boolean; active: boolean; basis: string }>;
}

type NativeMutation = {
  file: string;
  before: Buffer | null;
  after: Buffer;
  kind: "claude-settings" | "claude-mcp" | "codex-hooks" | "codex-config" | "hermes-config" | "hermes-plugin-manifest" | "hermes-plugin-init" | "gemini-settings" | "gemini-env" | "opencode-config" | "opencode-plugin" | "aider-config" | "aider-core";
  owned?: Record<string, unknown>;
};

type NativeJournal = {
  schema_version: 1;
  agent: NativeAgent;
  pack_version: string;
  installed_at: string;
  detected_agent_version: string | null;
  operations: Array<{
    file: string;
    kind: NativeMutation["kind"];
    backup: string;
    before_exists: boolean;
    before_sha256: string | null;
    after_sha256: string;
    owned?: Record<string, unknown>;
  }>;
};

const CODEX_NATIVE_ROOT_BEGIN = "# >>> caveman:native-root";
const CODEX_NATIVE_ROOT_END = "# <<< caveman:native-root";
const CODEX_NATIVE_TABLES_BEGIN = "# >>> caveman:native-tables";
const CODEX_NATIVE_TABLES_END = "# <<< caveman:native-tables";
const HERMES_NATIVE_ROUTE_BEGIN = "# >>> caveman:native-hermes-routing";
const HERMES_NATIVE_ROUTE_END = "# <<< caveman:native-hermes-routing";
const HERMES_NATIVE_PLUGIN_BEGIN = "# >>> caveman:native-hermes-plugin";
const HERMES_NATIVE_PLUGIN_END = "# <<< caveman:native-hermes-plugin";
const HERMES_NATIVE_MCP_BEGIN = "# >>> caveman:native-hermes-mcp";
const HERMES_NATIVE_MCP_END = "# <<< caveman:native-hermes-mcp";
const HERMES_NATIVE_PLUGIN_NAME = "caveman_native";
const GEMINI_NATIVE_ENV_BEGIN = "# >>> caveman:native-routing";
const GEMINI_NATIVE_ENV_END = "# <<< caveman:native-routing";
const AIDER_NATIVE_ROUTE_BEGIN = "# >>> caveman:native-aider-routing";
const AIDER_NATIVE_ROUTE_END = "# <<< caveman:native-aider-routing";
const AIDER_NATIVE_READ_BEGIN = "# >>> caveman:native-aider-core-read";
const AIDER_NATIVE_READ_END = "# <<< caveman:native-aider-core-read";
const AIDER_NATIVE_CORE_MARKER = "<!-- caveman:native-aider-core -->";

function nativeJournalPath(agent: string): string {
  return join(cavemanHome(), "integrations", `${agent}.json`);
}

function nativePendingJournalPath(agent: string): string {
  return join(cavemanHome(), "integrations", `.pending-${agent}.json`);
}

function fileBytes(path: string): Buffer | null {
  try { return readFileSync(path); } catch (error) {
    if ((error as NodeJS.ErrnoException).code === "ENOENT") return null;
    throw error;
  }
}

function bytesHash(bytes: Buffer): string {
  return `sha256:${createHash("sha256").update(bytes).digest("hex")}`;
}

function atomicWriteFile(path: string, bytes: Buffer, mode = 0o600): void {
  mkdirSync(dirname(path), { recursive: true, mode: 0o700 });
  const temp = join(dirname(path), `.${basename(path)}.caveman-${process.pid}-${randomUUID()}.tmp`);
  try {
    writeFileSync(temp, bytes, { mode });
    renameSync(temp, path);
    chmodSync(path, mode);
  } catch (error) {
    try { unlinkSync(temp); } catch { /* no partial */ }
    throw error;
  }
}

function parseJsonFileObject(path: string, bytes: Buffer | null): Record<string, unknown> {
  if (!bytes || bytes.length === 0) return {};
  const parsed = JSON.parse(bytes.toString("utf8"));
  if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) throw new Error(`${path} is not a JSON object`);
  return parsed as Record<string, unknown>;
}

function nativeMcpBinaryRequired(): string {
  const compatible = probeMcpBinary();
  if (!compatible) throw new Error("caveman-mcp not found; run `caveman setup --install`");
  if (!compatible.probe.current) throw new Error(`caveman-mcp ${compatible.probe.version} lacks current mcp_recovery capability; run \`caveman setup --install\``);
  return compatible.binary;
}

function nativeProxyBinaryRequired(gw: string): void {
  if (wrapMode(gw) !== "local") return;
  const binary = resolveGoBin("caveman-proxy", "CAVEMAN_PROXY_BIN");
  if (!binary) throw new Error("caveman-proxy not found; run `caveman setup --install`");
  const probe = probeVersionedBinary(binary, "native_runtime_v1");
  if (!probe.current) throw new Error(`caveman-proxy ${probe.version} lacks current native_runtime_v1 capability; run \`caveman setup --install\``);
}

function nativeHostProbe(agent: AgentProfile): { binary: string | null; launchable: boolean; version: string | null; error: string | null } {
  const binary = which(binOf(agent));
  if (!binary) return { binary: null, launchable: false, version: null, error: "binary_not_found" };
  try {
    const invocation = portableInvocation(binary, ["--version"]);
    const out = spawnSync(invocation.command, invocation.args, { encoding: "utf8", timeout: 3000 });
    if (out.error) return { binary, launchable: false, version: null, error: boundedHookString(out.error.message, 240) ?? "version_probe_failed" };
    const value = `${out.stdout ?? ""} ${out.stderr ?? ""}`.trim();
    if (out.status !== 0) return { binary, launchable: false, version: value ? value.slice(0, 160) : null, error: `version_probe_exit_${out.status ?? "unknown"}` };
    return { binary, launchable: true, version: value ? value.slice(0, 160) : null, error: null };
  } catch {
    return { binary, launchable: false, version: null, error: "version_probe_failed" };
  }
}

function detectedAgentVersion(agent: AgentProfile): string | null { return nativeHostProbe(agent).version; }

function claudeNativeMutations(gw: string, mcpBinary: string): NativeMutation[] {
  const settingsPath = claudeSettingsPath();
  const settingsBefore = fileBytes(settingsPath);
  const settings = parseJsonFileObject(settingsPath, settingsBefore);
  if (settings.env !== undefined && (typeof settings.env !== "object" || settings.env === null || Array.isArray(settings.env))) {
    throw new Error(`${settingsPath} env must be a JSON object; refusing to overwrite it`);
  }
  assertNativeHooksShape(settingsPath, settings, "claude");
  const env = settings.env && typeof settings.env === "object" && !Array.isArray(settings.env)
    ? settings.env as Record<string, unknown>
    : {};
  const route = appendUrlPath(gw, "/w/claude");
  const previousRoute = env.ANTHROPIC_BASE_URL;
  env.ANTHROPIC_BASE_URL = route;
  settings.env = env;
  const withHooks = nativeHooksDocument("claude", true, settings);

  const mcpPath = join(homedir(), ".claude.json");
  const mcpBefore = fileBytes(mcpPath);
  const mcpRoot = parseJsonFileObject(mcpPath, mcpBefore);
  if (mcpRoot.mcpServers !== undefined && (typeof mcpRoot.mcpServers !== "object" || mcpRoot.mcpServers === null || Array.isArray(mcpRoot.mcpServers))) {
    throw new Error(`${mcpPath} mcpServers must be a JSON object; refusing to overwrite it`);
  }
  const servers = mcpRoot.mcpServers && typeof mcpRoot.mcpServers === "object" && !Array.isArray(mcpRoot.mcpServers)
    ? mcpRoot.mcpServers as Record<string, unknown>
    : {};
  const previousMcp = servers.caveman;
  const installedMcp = { type: "stdio", command: mcpBinary, args: [], env: {} };
  servers.caveman = installedMcp;
  mcpRoot.mcpServers = servers;

  return [
    {
      file: settingsPath,
      before: settingsBefore,
      after: Buffer.from(JSON.stringify(withHooks, null, 2) + "\n"),
      kind: "claude-settings",
      owned: { route, previous_route: previousRoute ?? null },
    },
    {
      file: mcpPath,
      before: mcpBefore,
      after: Buffer.from(JSON.stringify(mcpRoot, null, 2) + "\n"),
      kind: "claude-mcp",
      owned: { installed_mcp: installedMcp, previous_mcp: previousMcp ?? null },
    },
  ];
}

function geminiNativeEnv(source: string, route: string): { text: string; block: string } {
  let stripped = source;
  const start = stripped.indexOf(GEMINI_NATIVE_ENV_BEGIN);
  const finish = stripped.indexOf(GEMINI_NATIVE_ENV_END);
  if ((start === -1) !== (finish === -1) || (start !== -1 && finish < start)) {
    throw new Error("existing Gemini Caveman routing block is corrupted; run `caveman doctor gemini`");
  }
  if (start !== -1) stripped = `${stripped.slice(0, start)}${stripped.slice(finish + GEMINI_NATIVE_ENV_END.length)}`.trim();
  const block = [
    GEMINI_NATIVE_ENV_BEGIN,
    `GEMINI_BASE_URL=${route}`,
    `GOOGLE_GEMINI_BASE_URL=${route}`,
    `GOOGLE_VERTEX_BASE_URL=${appendUrlPath(route, "/vertex")}`,
    GEMINI_NATIVE_ENV_END,
  ].join("\n");
  return { text: `${stripped}${stripped ? "\n\n" : ""}${block}\n`, block };
}

function geminiNativeMutations(gw: string, mcpBinary: string): NativeMutation[] {
  if (wrapMode(gw) === "managed") {
    throw new Error("managed Gemini CLI routing is unsupported because Gemini CLI cannot send separate Caveman and upstream credentials");
  }
  const settingsPath = geminiSettingsPath();
  const settingsBefore = fileBytes(settingsPath);
  const settings = parseJsonFileObject(settingsPath, settingsBefore);
  assertNativeHooksShape(settingsPath, settings, "gemini");
  if (settings.mcpServers !== undefined && (typeof settings.mcpServers !== "object" || settings.mcpServers === null || Array.isArray(settings.mcpServers))) {
    throw new Error(`${settingsPath} mcpServers must be a JSON object; refusing to overwrite it`);
  }
  const servers = settings.mcpServers && typeof settings.mcpServers === "object" && !Array.isArray(settings.mcpServers)
    ? settings.mcpServers as Record<string, unknown>
    : {};
  const previousMcp = servers.caveman;
  const installedMcp = { command: mcpBinary, args: [] };
  servers.caveman = installedMcp;
  settings.mcpServers = servers;
  const withHooks = nativeHooksDocument("gemini", true, settings);

  const envPath = join(homedir(), ".gemini", ".env");
  const envBefore = fileBytes(envPath);
  const route = appendUrlPath(gw, "/w/gemini");
  const nativeEnv = geminiNativeEnv(envBefore?.toString("utf8") ?? "", route);
  return [
    {
      file: settingsPath,
      before: settingsBefore,
      after: Buffer.from(JSON.stringify(withHooks, null, 2) + "\n"),
      kind: "gemini-settings",
      owned: { installed_mcp: installedMcp, previous_mcp: previousMcp ?? null },
    },
    {
      file: envPath,
      before: envBefore,
      after: Buffer.from(nativeEnv.text),
      kind: "gemini-env",
      owned: { route, route_block: nativeEnv.block },
    },
  ];
}

function opencodeNativePluginPath(): string {
  return join(homedir(), ".config", "opencode", "plugins", "caveman-native.js");
}

function opencodeNativePluginSource(): string {
  const { cmd, pre } = cavemanInvocation();
  return `// caveman:native-opencode — GENERATED by \`caveman enable opencode\`.
// Contract: @opencode-ai/plugin 1.17.8 Hooks (installed local type source).
import { execFileSync } from "node:child_process";
import { createHash } from "node:crypto";

const command = ${JSON.stringify(cmd)};
const prefix = ${JSON.stringify(pre)};
const contexts = new Map();
const pending = new Map();

function call(event, payload = {}) {
  try {
    const raw = execFileSync(command, [...prefix, "native-hook", "opencode", event], {
      input: JSON.stringify({ event_name: event, ...payload }),
      encoding: "utf8",
      timeout: 2000,
      maxBuffer: 2 * 1024 * 1024,
    });
    if (!raw.trim()) return undefined;
    const value = JSON.parse(raw);
    return value && typeof value === "object" ? value : undefined;
  } catch { return undefined; }
}

function digest(value) {
  try {
    const raw = JSON.stringify(value);
    return { bytes: Buffer.byteLength(raw), sha256: "sha256:" + createHash("sha256").update(raw).digest("hex") };
  } catch { return undefined; }
}

function taskType(value) {
  let text;
  try { text = JSON.stringify(value).toLowerCase(); } catch { return "general"; }
  const has = (...terms) => terms.some((term) => text.includes(term));
  if (has("migration", "migrate", "schema change", "backfill", "rollback")) return "migration";
  if (has("bug", "fix", "broken", "regression", "crash", "error", "incorrect")) return "bugfix";
  if (has("investigate", "diagnose", "root cause", "why does", "trace")) return "investigation";
  if (has("refactor", "restructure", "reorganize", "cleanup")) return "refactor";
  if (has("review", "audit", "critique", "assess")) return "review";
  if (has("verify", "verification", "prove", "validate", "check that")) return "verification";
  if (has("build", "implement", "add", "create", "ship", "feature")) return "feature";
  return "general";
}

function taskTerms(value) {
  let text;
  try { text = JSON.stringify(value); } catch { return []; }
  const stop = new Set(["about", "after", "agent", "before", "build", "change", "code", "create", "from", "have", "help", "implement", "into", "make", "please", "project", "repository", "should", "spec", "task", "that", "then", "there", "these", "they", "this", "through", "user", "want", "what", "when", "where", "which", "with", "would", "your"]);
  const out = [];
  const seen = new Set();
  for (const raw of text.match(/[A-Za-z][A-Za-z0-9_./-]{2,63}/g) ?? []) {
    const term = raw.toLowerCase().replace(/^[-./]+|[-./]+$/g, "");
    if (!term || term.includes("..") || stop.has(term) || seen.has(term) || /^(?:sk|pk|rk|ghp|github_pat|xox[baprs]|akia)[-_]/i.test(term) || /^[a-z0-9_-]{40,}$/i.test(term)) continue;
    seen.add(term);
    out.push(term);
    if (out.length === 12) break;
  }
  return out;
}

function taskContinuation(value) {
  const visit = (item) => {
    if (typeof item === "string") return item;
    if (Array.isArray(item)) return item.map(visit).filter(Boolean).join(" ");
    if (!item || typeof item !== "object") return "";
    return visit(item.text ?? item.content ?? item.message ?? "");
  };
  const prompt = visit(value).trim().toLowerCase();
  if (!prompt || prompt.length > 160 || prompt.split(/\s+/).length > 14) return false;
  return /^(?:please\s+)?(?:continue|go ahead|keep going|proceed|do (?:it|that)|fix (?:it|that)|retry|try again|explain (?:it|that)|what do you mean|yes|yep|yeah|why\??|how\??)[.!?\s]*$/.test(prompt);
}

function sessionContext(sessionID) {
  if (!sessionID) return undefined;
  if (!contexts.has(sessionID)) {
    const out = call("SessionStart", { session_id: sessionID, surface: "cli" });
    const context = out?.hookSpecificOutput?.additionalContext;
    if (typeof context === "string" && context) contexts.set(sessionID, context);
  }
  return contexts.get(sessionID);
}

export const CavemanNative = async () => ({
  event: async ({ event }) => {
    const type = event?.type;
    const props = event?.properties ?? {};
    const sessionID = props.sessionID ?? props.info?.id;
    if (type === "session.created") sessionContext(sessionID);
    if (type === "session.idle") call("Stop", { session_id: sessionID });
    if (type === "session.compacted") {
      const out = call("PostCompact", { session_id: sessionID });
      const context = out?.hookSpecificOutput?.additionalContext;
      if (typeof context === "string" && context) contexts.set(sessionID, context);
    }
    if (type === "session.deleted") {
      call("SessionEnd", { session_id: sessionID });
      contexts.delete(sessionID);
      pending.delete(sessionID);
    }
  },
  "chat.message": async (input, output) => {
    const decision = call("UserPromptSubmit", {
      session_id: input.sessionID,
      model: input.model?.modelID,
      provider: input.model?.providerID,
      prompt: digest(output?.parts),
      task_type: taskType(output?.parts),
      task_terms: taskTerms(output?.parts),
      task_continuation: taskContinuation(output?.parts),
    });
    const dynamic = decision?.hookSpecificOutput?.additionalContext;
    if (typeof dynamic === "string" && dynamic) pending.set(input.sessionID, dynamic);
  },
  "experimental.chat.system.transform": async (input, output) => {
    const stable = sessionContext(input.sessionID);
    if (stable) output.system.push(stable);
    const hint = pending.get(input.sessionID);
    if (hint) {
      output.system.push(hint);
      pending.delete(input.sessionID);
    }
  },
  "tool.execute.before": async (input, output) => {
    const decision = call("PreToolUse", {
      session_id: input.sessionID,
      tool_name: input.tool,
      tool_input: output.args,
    });
    if (typeof decision?.hookSpecificOutput?.additionalContext === "string") {
      pending.set(input.sessionID, decision.hookSpecificOutput.additionalContext);
    }
    if (input.tool !== "bash" || typeof output?.args?.command !== "string") return;
    try {
      const raw = execFileSync(command, [...prefix, "shrink-hook"], {
        input: JSON.stringify({ tool_name: "Bash", tool_input: { command: output.args.command } }),
        encoding: "utf8",
        timeout: 750,
      });
      const rewritten = JSON.parse(raw)?.hookSpecificOutput?.updatedInput?.command;
      if (typeof rewritten === "string" && rewritten) output.args.command = rewritten;
    } catch {}
  },
  "tool.execute.after": async (input, output) => {
    const decision = call("PostToolUse", {
      session_id: input.sessionID,
      tool_name: input.tool,
      tool_input: input.args,
      tool_output: output.output,
    });
    if (typeof decision?.hookSpecificOutput?.updatedToolOutput === "string") {
      output.output = decision.hookSpecificOutput.updatedToolOutput;
    } else if (typeof decision?.output_replacement === "string") {
      output.output = decision.output_replacement;
    }
  },
  "experimental.session.compacting": async (input, output) => {
    call("PreCompact", { session_id: input.sessionID });
    const stable = sessionContext(input.sessionID);
    if (stable) output.context.push(stable);
  },
  dispose: async () => {
    for (const sessionID of contexts.keys()) call("SessionEnd", { session_id: sessionID });
    contexts.clear();
    pending.clear();
  },
});
`;
}

function opencodeNativeMutations(gw: string, mcpBinary: string): NativeMutation[] {
  const configPath = join(homedir(), ".config", "opencode", "opencode.json");
  const before = fileBytes(configPath);
  const root = parseJsonFileObject(configPath, before);
  if (root.provider !== undefined && (typeof root.provider !== "object" || root.provider === null || Array.isArray(root.provider))) {
    throw new Error(`${configPath} provider must be a JSON object; refusing to overwrite it`);
  }
  if (root.mcp !== undefined && (typeof root.mcp !== "object" || root.mcp === null || Array.isArray(root.mcp))) {
    throw new Error(`${configPath} mcp must be a JSON object; refusing to overwrite it`);
  }
  const providers = root.provider && typeof root.provider === "object" && !Array.isArray(root.provider) ? root.provider as Record<string, unknown> : {};
  const previousRoutes: Record<string, unknown> = {};
  const base = appendUrlPath(gw, "/w/opencode");
  const routes = { openai: appendUrlPath(base, "/openai/v1"), anthropic: appendUrlPath(base, "/anthropic/v1") };
  for (const [providerID, route] of Object.entries(routes)) {
    const provider = providers[providerID] && typeof providers[providerID] === "object" && !Array.isArray(providers[providerID]) ? providers[providerID] as Record<string, unknown> : {};
    const options = provider.options && typeof provider.options === "object" && !Array.isArray(provider.options) ? provider.options as Record<string, unknown> : {};
    previousRoutes[providerID] = options.baseURL ?? null;
    options.baseURL = route;
    provider.options = options;
    providers[providerID] = provider;
  }
  root.provider = providers;
  const mcp = root.mcp && typeof root.mcp === "object" && !Array.isArray(root.mcp) ? root.mcp as Record<string, unknown> : {};
  const previousMcp = mcp.caveman;
  const installedMcp = { type: "local", command: [mcpBinary], enabled: true };
  mcp.caveman = installedMcp;
  root.mcp = mcp;

  const pluginPath = opencodeNativePluginPath();
  const pluginBefore = fileBytes(pluginPath);
  if (pluginBefore && !pluginBefore.toString("utf8").includes("caveman:native-opencode")) {
    throw new Error(`${pluginPath} already exists and is not Caveman-owned; refusing to overwrite it`);
  }
  return [
    {
      file: configPath,
      before,
      after: Buffer.from(JSON.stringify(root, null, 2) + "\n"),
      kind: "opencode-config",
      owned: { routes, previous_routes: previousRoutes, installed_mcp: installedMcp, previous_mcp: previousMcp ?? null },
    },
    {
      file: pluginPath,
      before: pluginBefore,
      after: Buffer.from(opencodeNativePluginSource()),
      kind: "opencode-plugin",
    },
  ];
}

function aiderCorePath(): string {
  return join(cavemanHome(), "packs", "aider", "CAVEMAN.md");
}

function aiderCoreSource(): string {
  return `${AIDER_NATIVE_CORE_MARKER}\n${NATIVE_CORE}\n\nHost limits: Aider repository map remains authoritative. Caveman observes provider traffic through local proxy; no pre-tool governance or lifecycle interception is active.\n`;
}

function aiderNativeConfig(source: string, route: string, corePath: string): { text: string; routeBlock: string; previousRouteLine: string | null; readBlock: string } {
  if ([AIDER_NATIVE_ROUTE_BEGIN, AIDER_NATIVE_ROUTE_END, AIDER_NATIVE_READ_BEGIN, AIDER_NATIVE_READ_END].some((marker) => source.includes(marker))) {
    throw new Error("existing Aider Caveman block is unjournaled; run `caveman doctor aider`");
  }
  const newline = source.includes("\r\n") ? "\r\n" : "\n";
  const lines = source.replace(/\r\n/g, "\n").split("\n");
  const routeIndexes = lines.flatMap((line, index) => /^openai-api-base\s*:/.test(line) ? [index] : []);
  if (routeIndexes.length > 1) throw new Error("Aider config has duplicate openai-api-base keys; refusing unsafe merge");
  const routeBlockLines = [AIDER_NATIVE_ROUTE_BEGIN, `openai-api-base: ${yamlQuote(route)}`, AIDER_NATIVE_ROUTE_END];
  const routeBlock = routeBlockLines.join(newline);
  const previousRouteLine = routeIndexes.length === 1 ? lines[routeIndexes[0]!]! : null;
  if (routeIndexes.length === 1) lines.splice(routeIndexes[0]!, 1, ...routeBlockLines);
  else {
    if (lines.some((line) => line.trim() !== "") && lines[lines.length - 1]?.trim()) lines.push("");
    lines.push(...routeBlockLines);
  }

  const readIndexes = lines.flatMap((line, index) => /^read\s*:/.test(line) ? [index] : []);
  if (readIndexes.length > 1) throw new Error("Aider config has duplicate read keys; refusing unsafe merge");
  let readBlockLines: string[];
  if (readIndexes.length === 1) {
    const readIndex = readIndexes[0]!;
    if (!/^read\s*:\s*(?:#.*)?$/.test(lines[readIndex]!)) {
      throw new Error("Aider config uses inline/scalar read; use block-list form before enabling Caveman");
    }
    let end = readIndex + 1;
    while (end < lines.length && !/^[A-Za-z0-9][A-Za-z0-9_-]*\s*:/.test(lines[end]!)) end++;
    readBlockLines = [`  ${AIDER_NATIVE_READ_BEGIN}`, `  - ${yamlQuote(corePath)}`, `  ${AIDER_NATIVE_READ_END}`];
    lines.splice(end, 0, ...readBlockLines);
  } else {
    if (lines.some((line) => line.trim() !== "") && lines[lines.length - 1]?.trim()) lines.push("");
    readBlockLines = [AIDER_NATIVE_READ_BEGIN, "read:", `  - ${yamlQuote(corePath)}`, AIDER_NATIVE_READ_END];
    lines.push(...readBlockLines);
  }
  const readBlock = readBlockLines.join(newline);
  const text = lines.join(newline).replace(/(?:\r?\n)+$/, "") + newline;
  return { text, routeBlock, previousRouteLine, readBlock };
}

function aiderNativeMutations(gw: string): NativeMutation[] {
  const configPath = join(homedir(), ".aider.conf.yml");
  const configBefore = fileBytes(configPath);
  const corePath = aiderCorePath();
  const coreBefore = fileBytes(corePath);
  const core = aiderCoreSource();
  if (coreBefore && coreBefore.toString("utf8") !== core) {
    throw new Error(`${corePath} exists and is not exact Caveman-owned content; refusing to overwrite it`);
  }
  const route = appendUrlPath(gw, "/w/aider/openai/v1");
  const native = aiderNativeConfig(configBefore?.toString("utf8") ?? "", route, corePath);
  return [
    {
      file: configPath,
      before: configBefore,
      after: Buffer.from(native.text),
      kind: "aider-config",
      owned: { route, route_block: native.routeBlock, previous_route_line: native.previousRouteLine, read_block: native.readBlock, core_path: corePath },
    },
    {
      file: corePath,
      before: coreBefore,
      after: Buffer.from(core),
      kind: "aider-core",
      owned: { marker: AIDER_NATIVE_CORE_MARKER },
    },
  ];
}

function codexNativeConfig(source: string, gw: string, subscription: boolean, mcpBinary: string): { text: string; rootBlock: string; tablesBlock: string } {
  let stripped = stripCodexCavemanMcpToml(stripCodexCavemanProviderToml(source));
  for (const [begin, end] of [[CODEX_NATIVE_ROOT_BEGIN, CODEX_NATIVE_ROOT_END], [CODEX_NATIVE_TABLES_BEGIN, CODEX_NATIVE_TABLES_END]] as const) {
    const start = stripped.indexOf(begin);
    const finish = stripped.indexOf(end);
    if ((start === -1) !== (finish === -1)) throw new Error("existing Codex Caveman block is corrupted; run `caveman doctor codex`");
    if (start !== -1) stripped = `${stripped.slice(0, start)}${stripped.slice(finish + end.length)}`.trim();
  }
  const rootBlock = `${CODEX_NATIVE_ROOT_BEGIN}\nmodel_provider = "caveman"\n${CODEX_NATIVE_ROOT_END}`;
  const providerLines = codexCavemanProviderToml(gw, subscription).split("\n").slice(1).join("\n");
  const tablesBlock = [
    CODEX_NATIVE_TABLES_BEGIN,
    providerLines,
    "",
    "[mcp_servers.caveman]",
    `command = ${JSON.stringify(mcpBinary)}`,
    CODEX_NATIVE_TABLES_END,
  ].join("\n");
  const middle = stripped ? `\n\n${stripped}` : "";
  return { text: `${rootBlock}${middle}\n\n${tablesBlock}\n`, rootBlock, tablesBlock };
}

function codexNativeMutations(gw: string, mcpBinary: string): NativeMutation[] {
  const hooksPath = codexHooksPath();
  const hooksBefore = fileBytes(hooksPath);
  const hooksRoot = parseJsonFileObject(hooksPath, hooksBefore);
  assertNativeHooksShape(hooksPath, hooksRoot, "codex");
  const hooks = nativeHooksDocument("codex", true, hooksRoot);
  const configPath = join(codexHomeDir(), "config.toml");
  const configBefore = fileBytes(configPath);
  const subscription = detectCodexWrapAuthMode() === "subscription";
  const native = codexNativeConfig(configBefore?.toString("utf8") ?? "", gw, subscription, mcpBinary);
  const route = appendUrlPath(gw, subscription ? "/chatgpt" : "/w/codex");
  return [
    { file: hooksPath, before: hooksBefore, after: Buffer.from(JSON.stringify(hooks, null, 2) + "\n"), kind: "codex-hooks" },
    {
      file: configPath,
      before: configBefore,
      after: Buffer.from(native.text),
      kind: "codex-config",
      owned: { root_block: native.rootBlock, tables_block: native.tablesBlock, route, auth_mode: subscription ? "subscription" : "api-key" },
    },
  ];
}

function hermesNativePluginDir(): string {
  return join(hermesHome(), "plugins", HERMES_NATIVE_PLUGIN_NAME);
}

function hermesNativePluginManifest(): string {
  return [
    "manifest_version: 1",
    `name: ${HERMES_NATIVE_PLUGIN_NAME}`,
    `version: ${JSON.stringify(cliVersion())}`,
    'description: "Caveman native lifecycle bridge"',
    "provides_hooks:",
    "  - on_session_start",
    "  - pre_llm_call",
    "  - pre_tool_call",
    "  - post_tool_call",
    "  - pre_verify",
    "  - post_llm_call",
    "  - on_session_end",
    "  - on_session_finalize",
    "",
  ].join("\n");
}

function hermesNativePluginSource(): string {
  const { cmd, pre } = cavemanInvocation();
  return `# GENERATED by \`caveman enable hermes\`. Thin fail-open lifecycle adapter.
# Source contract: Hermes v0.18 plugin register(ctx) and hook signatures in
# ~/.hermes/hermes-agent/website/docs/guides/build-a-hermes-plugin.md.
import hashlib
import json
import re
import subprocess

_CAVEMAN_CMD = ${JSON.stringify(cmd)}
_CAVEMAN_PRE = ${JSON.stringify(pre)}


def _digest(value):
    try:
        raw = json.dumps(value, sort_keys=True, ensure_ascii=False, default=str).encode("utf-8")
        return {"bytes": len(raw), "sha256": "sha256:" + hashlib.sha256(raw).hexdigest()}
    except Exception:
        return {"bytes": 0}


def _task_type(value):
    try:
        text = json.dumps(value, ensure_ascii=False, default=str).lower()
    except Exception:
        return "general"
    def has(*terms):
        return any(term in text for term in terms)
    if has("migration", "migrate", "schema change", "backfill", "rollback"):
        return "migration"
    if has("bug", "fix", "broken", "regression", "crash", "error", "incorrect"):
        return "bugfix"
    if has("investigate", "diagnose", "root cause", "why does", "trace"):
        return "investigation"
    if has("refactor", "restructure", "reorganize", "cleanup"):
        return "refactor"
    if has("review", "audit", "critique", "assess"):
        return "review"
    if has("verify", "verification", "prove", "validate", "check that"):
        return "verification"
    if has("build", "implement", "add", "create", "ship", "feature"):
        return "feature"
    return "general"


def _task_terms(value):
    try:
        text = json.dumps(value, ensure_ascii=False, default=str)
    except Exception:
        return []
    stop = {"about", "after", "agent", "before", "build", "change", "code", "create", "from", "have", "help", "implement", "into", "make", "please", "project", "repository", "should", "spec", "task", "that", "then", "there", "these", "they", "this", "through", "user", "want", "what", "when", "where", "which", "with", "would", "your"}
    out = []
    for raw in re.findall(r"[A-Za-z][A-Za-z0-9_./-]{2,63}", text):
        term = raw.lower().strip("-./")
        if not term or ".." in term or term in stop or term in out:
            continue
        if re.match(r"^(?:sk|pk|rk|ghp|github_pat|xox[baprs]|akia)[-_]", term, re.I) or re.match(r"^[a-z0-9_-]{40,}$", term, re.I):
            continue
        out.append(term)
        if len(out) == 12:
            break
    return out


def _task_continuation(value):
    if not isinstance(value, str):
        return False
    prompt = value.strip().lower()
    if not prompt or len(prompt) > 160 or len(prompt.split()) > 14:
        return False
    return re.match(r"^(?:please\s+)?(?:continue|go ahead|keep going|proceed|do (?:it|that)|fix (?:it|that)|retry|try again|explain (?:it|that)|what do you mean|yes|yep|yeah|why\??|how\??)[.!?\s]*$", prompt) is not None


def _call(event, payload=None):
    try:
        body = {"event_name": event}
        if isinstance(payload, dict):
            body.update(payload)
        proc = subprocess.run(
            [_CAVEMAN_CMD, *_CAVEMAN_PRE, "native-hook", "hermes", event],
            input=json.dumps(body, ensure_ascii=False),
            text=True,
            capture_output=True,
            timeout=1,
        )
        if proc.returncode != 0 or not proc.stdout.strip():
            return None
        out = json.loads(proc.stdout)
        return out if isinstance(out, dict) else None
    except Exception:
        return None


def _on_session_start(session_id=None, model=None, platform=None, **kwargs):
    _call("SessionStart", {"session_id": session_id, "model": model, "surface": platform})


def _pre_llm_call(session_id=None, user_message=None, model=None, platform=None, **kwargs):
    out = _call("UserPromptSubmit", {
        "session_id": session_id,
        "model": model,
        "surface": platform,
        "prompt": _digest(user_message),
        "task_type": _task_type(user_message),
        "task_terms": _task_terms(user_message),
        "task_continuation": _task_continuation(user_message),
    })
    context = out.get("context") if isinstance(out, dict) else None
    return {"context": context} if isinstance(context, str) and context else None


def _pre_tool_call(tool_name=None, args=None, task_id=None, **kwargs):
    _call("PreToolUse", {"session_id": task_id, "tool_name": tool_name, "tool_input": _digest(args)})


def _post_tool_call(tool_name=None, args=None, result=None, task_id=None, duration_ms=None, **kwargs):
    _call("PostToolUse", {
        "session_id": task_id,
        "tool_name": tool_name,
        "tool_input": _digest(args),
        "tool_output": _digest(result),
        "duration_ms": duration_ms,
    })


def _pre_verify(task_id=None, **kwargs):
    _call("Stop", {"session_id": task_id})


def _post_llm_call(session_id=None, model=None, **kwargs):
    _call("ModelAfter", {"session_id": session_id, "model": model})


def _on_session_end(session_id=None, **kwargs):
    _call("SessionEnd", {"session_id": session_id})


def _on_session_finalize(session_id=None, **kwargs):
    _call("SessionEnd", {"session_id": session_id})


def register(ctx):
    ctx.register_hook("on_session_start", _on_session_start)
    ctx.register_hook("pre_llm_call", _pre_llm_call)
    ctx.register_hook("pre_tool_call", _pre_tool_call)
    ctx.register_hook("post_tool_call", _post_tool_call)
    ctx.register_hook("pre_verify", _pre_verify)
    ctx.register_hook("post_llm_call", _post_llm_call)
    ctx.register_hook("on_session_end", _on_session_end)
    ctx.register_hook("on_session_finalize", _on_session_finalize)
`;
}

function insertHermesNativeListEntry(
  lines: string[],
  sectionName: string,
  childName: string,
  begin: string,
  end: string,
  blockLines: string[],
): string | null {
  const section = topLevelSection(lines, sectionName);
  if (section) {
    if (sectionHasChildKey(lines, section, childName)) return null;
    lines.splice(section.start + 1, 0, ...blockLines);
    return blockLines.join("\n");
  }
  if (lines.length > 0 && lines[lines.length - 1]!.trim()) lines.push("");
  const body = blockLines.filter((line) => !line.includes(begin) && !line.includes(end));
  const whole = [begin, `${sectionName}:`, ...body, end];
  lines.push(...whole);
  return whole.join("\n");
}

function hermesNativeConfig(source: string, gw: string, mcpBinary: string): { text: string; owned: Record<string, unknown> } {
  for (const marker of [HERMES_NATIVE_ROUTE_BEGIN, HERMES_NATIVE_ROUTE_END, HERMES_NATIVE_PLUGIN_BEGIN, HERMES_NATIVE_PLUGIN_END, HERMES_NATIVE_MCP_BEGIN, HERMES_NATIVE_MCP_END]) {
    if (source.includes(marker)) throw new Error("existing Hermes Caveman native block is unjournaled; run `caveman doctor hermes`");
  }
  const lines = yamlLines(source);
  let model = topLevelSection(lines, "model");
  if (!model) {
    lines.unshift("model:");
    model = topLevelSection(lines, "model")!;
  }
  const routeIndexes: number[] = [];
  const previousRouteLines: string[] = [];
  for (let i = model.start + 1; i < model.end; i++) {
    if (/^  (provider|base_url):/.test(lines[i]!)) {
      routeIndexes.push(i);
      previousRouteLines.push(lines[i]!);
    }
  }
  if (new Set(routeIndexes.map((index) => lines[index]!.match(/^  ([^:]+):/)?.[1])).size !== routeIndexes.length) {
    throw new Error("Hermes model provider/base_url keys are duplicated; refusing ambiguous install");
  }
  const insertAt = routeIndexes[0] ?? model.start + 1;
  for (const index of [...routeIndexes].sort((a, b) => b - a)) lines.splice(index, 1);
  const routeBlock = [
    `  ${HERMES_NATIVE_ROUTE_BEGIN}`,
    '  provider: "custom"',
    `  base_url: ${yamlQuote(appendUrlPath(gw, "/w/hermes"))}`,
    `  ${HERMES_NATIVE_ROUTE_END}`,
  ];
  lines.splice(insertAt, 0, ...routeBlock);

  let pluginBlock: string | null = null;
  const plugins = topLevelSection(lines, "plugins");
  if (plugins) {
    let enabled = -1;
    for (let i = plugins.start + 1; i < plugins.end; i++) {
      if (/^  enabled:\s*\[/.test(lines[i]!)) throw new Error("Hermes plugins.enabled uses inline YAML; refusing unsafe native merge");
      if (/^  enabled:\s*(?:#.*)?$/.test(lines[i]!)) { enabled = i; break; }
    }
    if (enabled >= 0) {
      if (!hermesNamedPluginEnabled(source, HERMES_NATIVE_PLUGIN_NAME)) {
        const block = [`    ${HERMES_NATIVE_PLUGIN_BEGIN}`, `    - ${HERMES_NATIVE_PLUGIN_NAME}`, `    ${HERMES_NATIVE_PLUGIN_END}`];
        lines.splice(enabled + 1, 0, ...block);
        pluginBlock = block.join("\n");
      }
    } else {
      const block = [`  ${HERMES_NATIVE_PLUGIN_BEGIN}`, "  enabled:", `    - ${HERMES_NATIVE_PLUGIN_NAME}`, `  ${HERMES_NATIVE_PLUGIN_END}`];
      lines.splice(plugins.start + 1, 0, ...block);
      pluginBlock = block.join("\n");
    }
  } else {
    if (lines.length > 0 && lines[lines.length - 1]!.trim()) lines.push("");
    const block = [HERMES_NATIVE_PLUGIN_BEGIN, "plugins:", "  enabled:", `    - ${HERMES_NATIVE_PLUGIN_NAME}`, HERMES_NATIVE_PLUGIN_END];
    lines.push(...block);
    pluginBlock = block.join("\n");
  }

  const mcpBlockLines = [
    `  ${HERMES_NATIVE_MCP_BEGIN}`,
    "  caveman-native:",
    `    command: ${yamlQuote(mcpBinary)}`,
    "    enabled: true",
    `  ${HERMES_NATIVE_MCP_END}`,
  ];
  const mcpBlock = insertHermesNativeListEntry(lines, "mcp_servers", "caveman-native", HERMES_NATIVE_MCP_BEGIN, HERMES_NATIVE_MCP_END, mcpBlockLines);
  return {
    text: yamlText(lines),
    owned: {
      route: appendUrlPath(gw, "/w/hermes"),
      route_block: routeBlock.join("\n"),
      previous_route_lines: previousRouteLines,
      plugin_block: pluginBlock,
      mcp_block: mcpBlock,
    },
  };
}

function hermesNativeMutations(gw: string, mcpBinary: string): NativeMutation[] {
  const configPath = hermesConfigPath();
  const configBefore = fileBytes(configPath);
  const native = hermesNativeConfig(configBefore?.toString("utf8") ?? "", gw, mcpBinary);
  const pluginDir = hermesNativePluginDir();
  const manifestPath = join(pluginDir, "plugin.yaml");
  const initPath = join(pluginDir, "__init__.py");
  for (const path of [manifestPath, initPath]) {
    if (fileBytes(path)) throw new Error(`${path} already exists; refusing to overwrite an unjournaled plugin`);
  }
  return [
    { file: configPath, before: configBefore, after: Buffer.from(native.text), kind: "hermes-config", owned: native.owned },
    { file: manifestPath, before: null, after: Buffer.from(hermesNativePluginManifest()), kind: "hermes-plugin-manifest" },
    { file: initPath, before: null, after: Buffer.from(hermesNativePluginSource()), kind: "hermes-plugin-init" },
  ];
}

function withIntegrationLock<T>(agent: string, run: () => T): T {
  const lock = join(cavemanHome(), "integrations", `.lock-${agent}`);
  const ownerPath = join(lock, "owner.json");
  const token = randomUUID();
  mkdirSync(dirname(lock), { recursive: true, mode: 0o700 });
  try { mkdirSync(lock, { recursive: false, mode: 0o700 }); }
  catch (error) {
    if ((error as NodeJS.ErrnoException).code !== "EEXIST") throw error;
    let stale = false;
    try {
      const owner = JSON.parse(readFileSync(ownerPath, "utf8")) as { pid?: unknown };
      if (typeof owner.pid === "number" && Number.isInteger(owner.pid) && owner.pid > 1) {
        try { process.kill(owner.pid, 0); }
        catch (probeError) { stale = (probeError as NodeJS.ErrnoException).code === "ESRCH"; }
      }
    } catch {
      try { stale = Date.now() - statSync(lock).mtimeMs > 30_000; } catch { /* raced; treated live */ }
    }
    if (!stale) throw new Error(`integration change already running for ${agent}`);
    const quarantine = `${lock}.stale-${token}`;
    try {
      renameSync(lock, quarantine);
      mkdirSync(lock, { recursive: false, mode: 0o700 });
      process.stderr.write(`${mark("warn")} reclaimed stale integration lock for ${agent}\n`);
    } catch {
      throw new Error(`integration change already running for ${agent}`);
    } finally {
      try { rmSync(quarantine, { recursive: true, force: true }); } catch { /* isolated stale lock only */ }
    }
  }
  try {
    atomicWriteFile(ownerPath, Buffer.from(JSON.stringify({ pid: process.pid, token, started_at: new Date().toISOString() }) + "\n"));
  } catch (error) {
    try { rmSync(lock, { recursive: true, force: true }); } catch { /* original error remains authority */ }
    throw error;
  }
  try { return run(); }
  finally {
    try {
      const owner = JSON.parse(readFileSync(ownerPath, "utf8")) as { token?: unknown };
      if (owner.token === token) rmSync(lock, { recursive: true, force: true });
    } catch { /* best effort; future process can reclaim stale owner */ }
  }
}

function applyNativeMutations(agent: NativeAgent, profile: AgentProfile, mutations: NativeMutation[]): NativeJournal {
  const base = join(cavemanHome(), "integrations", "backups", agent, randomUUID());
  mkdirSync(base, { recursive: true, mode: 0o700 });
  const written: NativeMutation[] = [];
  try {
    const operations = mutations.map((mutation, index) => {
      const backup = join(base, `${index}.bin`);
      if (mutation.before) atomicWriteFile(backup, mutation.before);
      return {
        file: mutation.file,
        kind: mutation.kind,
        backup,
        before_exists: mutation.before !== null,
        before_sha256: mutation.before ? bytesHash(mutation.before) : null,
        after_sha256: bytesHash(mutation.after),
        ...(mutation.owned ? { owned: mutation.owned } : {}),
      };
    });
    const journal: NativeJournal = {
      schema_version: 1,
      agent,
      pack_version: NATIVE_PACK.version,
      installed_at: new Date().toISOString(),
      detected_agent_version: detectedAgentVersion(profile),
      operations,
    };
    // Durable before first host write. A later enable/doctor can recover a
    // process death between individual writes and final journal publication.
    atomicWriteFile(nativePendingJournalPath(agent), Buffer.from(JSON.stringify(journal, null, 2) + "\n"));
    for (const mutation of mutations) {
      atomicWriteFile(mutation.file, mutation.after);
      written.push(mutation);
    }
    atomicWriteFile(nativeJournalPath(agent), Buffer.from(JSON.stringify(journal, null, 2) + "\n"));
    unlinkSync(nativePendingJournalPath(agent));
    return journal;
  } catch (error) {
    for (const mutation of written.reverse()) {
      try {
        if (mutation.before) atomicWriteFile(mutation.file, mutation.before);
        else unlinkSync(mutation.file);
      } catch { /* original error remains authority */ }
    }
    try { unlinkSync(nativeJournalPath(agent)); } catch { /* absent or incomplete */ }
    try { unlinkSync(nativePendingJournalPath(agent)); } catch { /* absent or rollback-only */ }
    throw error;
  }
}

function readNativeJournalAt(path: string, agent: string): NativeJournal | undefined {
  try {
    const value = JSON.parse(readFileSync(path, "utf8")) as NativeJournal;
    return value?.schema_version === 1 && value.agent === agent && Array.isArray(value.operations) ? value : undefined;
  } catch { return undefined; }
}

function readNativeJournal(agent: string): NativeJournal | undefined {
  return readNativeJournalAt(nativeJournalPath(agent), agent);
}

function readPendingNativeJournal(agent: string): NativeJournal | undefined {
  return readNativeJournalAt(nativePendingJournalPath(agent), agent);
}

function recoverPendingNativeInstallUnlocked(agent: NativeAgent): boolean {
  const pending = readPendingNativeJournal(agent);
  if (!pending) return false;
  const committed = readNativeJournal(agent);
  if (committed) {
    if (JSON.stringify(committed) !== JSON.stringify(pending)) {
      throw new Error(`conflicting committed and pending integration journals for ${agent}; refusing recovery`);
    }
    unlinkSync(nativePendingJournalPath(agent));
    return false;
  }
  const current = pending.operations.map((operation) => ({ file: operation.file, bytes: fileBytes(operation.file) }));
  const restored = pending.operations.map((operation) => {
    const before = nativeBackupBytes(operation);
    const now = fileBytes(operation.file);
    const isInstalled = Boolean(now && bytesHash(now) === operation.after_sha256);
    const isBefore = operation.before_exists
      ? Boolean(now && operation.before_sha256 && bytesHash(now) === operation.before_sha256)
      : now === null;
    if (!isInstalled && !isBefore) {
      throw new Error(`${operation.file} changed during interrupted ${agent} install; refusing recovery`);
    }
    return { file: operation.file, bytes: before };
  });
  try {
    for (const item of restored) writeNativeRestoration(item.file, item.bytes);
    unlinkSync(nativePendingJournalPath(agent));
  } catch (error) {
    for (const item of current) {
      try { writeNativeRestoration(item.file, item.bytes); } catch { /* original error remains authority */ }
    }
    throw error;
  }
  process.stderr.write(`${mark("warn")} recovered interrupted ${agent} integration change before continuing\n`);
  return true;
}

function nativeMutationsFor(agent: NativeAgent, gw: string, mcpBinary: string | undefined): NativeMutation[] {
  return agent === "claude"
    ? claudeNativeMutations(gw, mcpBinary!)
    : agent === "codex"
      ? codexNativeMutations(gw, mcpBinary!)
      : agent === "hermes"
        ? hermesNativeMutations(gw, mcpBinary!)
        : agent === "gemini"
          ? geminiNativeMutations(gw, mcpBinary!)
          : agent === "opencode"
            ? opencodeNativeMutations(gw, mcpBinary!)
            : aiderNativeMutations(gw);
}

function enableNative(argv: string[]) {
  const detected = argv.includes("--detected");
  const target = argv.find((arg) => !arg.startsWith("--"));
  if ((!detected && !target) || (detected && target) || argv.some((arg) => arg !== "--detected" && arg !== target)) {
    commandUsage("enable <claude|codex|hermes|gemini|opencode|aider> | enable --detected");
  }
  const profiles = detected
    ? AGENTS.filter((agent) => (agent.id === "claude" || agent.id === "codex" || agent.id === "hermes" || agent.id === "gemini" || agent.id === "opencode" || agent.id === "aider") && which(binOf(agent)))
    : AGENTS.filter((agent) => agent.id === target && (agent.id === "claude" || agent.id === "codex" || agent.id === "hermes" || agent.id === "gemini" || agent.id === "opencode" || agent.id === "aider"));
  if (profiles.length === 0) {
    console.error(detected ? "no supported native agent detected on PATH" : `caveman enable: supported agents are claude, codex, hermes, gemini, opencode, and aider (got ${target ?? ""})`);
    process.exit(1);
  }
  const gw = gatewayURL();
  for (const profile of profiles) {
    const agent = profile.id as NativeAgent;
    const outcome = withIntegrationLock(agent, () => {
      recoverPendingNativeInstallUnlocked(agent);
      if (!which(binOf(profile))) throw new Error(`${profile.display_name} not found on PATH`);
      const mcpBinary = agent === "aider" ? undefined : nativeMcpBinaryRequired();
      nativeProxyBinaryRequired(gw);
      const existing = nativeIntegrationStatus(agent);
      if (existing.installed) {
        if (existing.state === "installed") return "already" as const;
        throw new Error(`${profile.display_name} integration is degraded; run \`caveman doctor ${agent}\` before changing it`);
      }
      const mutations = nativeMutationsFor(agent, gw, mcpBinary);
      const route = mutations.find((item) => typeof item.owned?.route === "string")?.owned?.route;
      process.stderr.write(`caveman enable ${agent}: planned user-scoped writes\n`);
      for (const mutation of mutations) process.stderr.write(`  ${mutation.kind}: ${mutation.file}\n`);
      if (typeof route === "string") process.stderr.write(`  routing: ${route}\n`);
      process.stderr.write(agent === "aider" ? "  recovery MCP: unavailable in Aider\n" : `  recovery MCP: ${mcpBinary}\n`);
      process.stderr.write(agent === "aider"
        ? `  Core: read-only ${aiderCorePath()}; lifecycle/tool interception unavailable; Ledger observational\n`
        : `  lifecycle/Core/tool rewrite: ${nativeHookCommand(agent)}${agent === "hermes" ? " via native plugin" : ` + ${cavemanBinForHook()} shrink-hook`}\n`);
      applyNativeMutations(agent, profile, mutations);
      return "enabled" as const;
    });
    if (outcome === "already") {
      process.stderr.write(`${mark("ok")} ${profile.display_name}: ${agent === "aider" ? "shallow" : "native"} Caveman already enabled\n`);
      continue;
    }
    process.stderr.write(`${mark("ok")} ${profile.display_name}: ${agent === "aider" ? "shallow" : "native"} Caveman enabled; run ${agent} normally\n`);
    if (agent !== "aider") process.stderr.write(dim(`→ host trust remains authoritative; approve Caveman hooks/plugin when ${profile.display_name} asks\n`));
    if (agent === "codex") process.stderr.write(dim("→ review/approve hook hashes through Codex /hooks; Caveman does not bypass native trust\n"));
    if (agent === "aider") {
      process.stderr.write(dim(`→ coding policy: Core ${NATIVE_PACK.version} static on; Aider cannot apply think.core live; \`caveman disable aider\` removes it\n`));
    } else {
      const core = wrapRuntimeConfig().core;
      process.stderr.write(dim(`→ coding policy: Core ${NATIVE_PACK.version} ${core ? "on" : "off"}; change with \`caveman tools config set think.core ${core ? "off" : "on"}\`; start new session to clear previously delivered context\n`));
    }
    process.stderr.write(dim(`→ undo: caveman disable ${agent}\n`));
  }
}

function nativeBackupBytes(operation: NativeJournal["operations"][number]): Buffer | null {
  if (!operation.before_exists) return null;
  const bytes = readFileSync(operation.backup);
  if (operation.before_sha256 && bytesHash(bytes) !== operation.before_sha256) throw new Error(`integration backup hash mismatch: ${operation.backup}`);
  return bytes;
}

function removeNativeHookEntries(root: Record<string, unknown>, agent: "claude" | "codex" | "gemini"): Record<string, unknown> {
  const hooks = root.hooks && typeof root.hooks === "object" && !Array.isArray(root.hooks)
    ? root.hooks as Record<string, unknown>
    : undefined;
  if (!hooks) return root;
  const expected = nativeHooksDocument(agent, true).hooks as Record<string, unknown>;
  for (const [event, expectedRaw] of Object.entries(expected)) {
    const list = Array.isArray(hooks[event]) ? hooks[event] as Array<Record<string, unknown>> : [];
    const expectedEntries = Array.isArray(expectedRaw) ? expectedRaw as Array<Record<string, unknown>> : [];
    const expectedStrings = new Set(expectedEntries.map((entry) => JSON.stringify(entry)));
    for (const entry of list) {
      const encoded = JSON.stringify(entry);
      const command = hookEntryCommand(entry) ?? "";
      const nativeMarker = `native-hook ${agent}`;
      const looksManagedNative = command.endsWith(nativeMarker) || command.includes(`${nativeMarker} --adapter `);
      if ((looksManagedNative || /(?:^|\s)shrink-hook$/.test(command)) && !expectedStrings.has(encoded)) {
        throw new Error(`${agent} ${event} Caveman hook changed after enable; refusing destructive disable`);
      }
    }
    const kept = list.filter((entry) => !expectedStrings.has(JSON.stringify(entry)));
    if (kept.length > 0) hooks[event] = kept;
    else delete hooks[event];
  }
  if (Object.keys(hooks).length === 0) delete root.hooks;
  return root;
}

function jsonBytes(root: Record<string, unknown>): Buffer {
  return Buffer.from(JSON.stringify(root, null, 2) + "\n");
}

function restoreNativeOperation(operation: NativeJournal["operations"][number]): Buffer | null {
  const current = fileBytes(operation.file);
  const before = nativeBackupBytes(operation);
  if (!current) {
    if (operation.before_exists) throw new Error(`${operation.file} was removed after enable; refusing destructive disable`);
    return null;
  }
  if (bytesHash(current) === operation.after_sha256) return before;

  if (operation.kind === "claude-settings") {
    const currentRoot = parseJsonFileObject(operation.file, current);
    const beforeRoot = parseJsonFileObject(operation.file, before);
    const currentEnv = currentRoot.env && typeof currentRoot.env === "object" && !Array.isArray(currentRoot.env)
      ? currentRoot.env as Record<string, unknown>
      : {};
    const beforeEnv = beforeRoot.env && typeof beforeRoot.env === "object" && !Array.isArray(beforeRoot.env)
      ? beforeRoot.env as Record<string, unknown>
      : {};
    const route = operation.owned?.route;
    if (currentEnv.ANTHROPIC_BASE_URL !== undefined && currentEnv.ANTHROPIC_BASE_URL !== route) {
      throw new Error("Claude ANTHROPIC_BASE_URL changed after enable; refusing destructive disable");
    }
    if (currentEnv.ANTHROPIC_BASE_URL === route) {
      if (beforeEnv.ANTHROPIC_BASE_URL === undefined) delete currentEnv.ANTHROPIC_BASE_URL;
      else currentEnv.ANTHROPIC_BASE_URL = beforeEnv.ANTHROPIC_BASE_URL;
    }
    if (Object.keys(currentEnv).length > 0) currentRoot.env = currentEnv;
    else delete currentRoot.env;
    return jsonBytes(removeNativeHookEntries(currentRoot, "claude"));
  }
  if (operation.kind === "claude-mcp") {
    const currentRoot = parseJsonFileObject(operation.file, current);
    const beforeRoot = parseJsonFileObject(operation.file, before);
    const servers = currentRoot.mcpServers && typeof currentRoot.mcpServers === "object" && !Array.isArray(currentRoot.mcpServers)
      ? currentRoot.mcpServers as Record<string, unknown>
      : {};
    const beforeServers = beforeRoot.mcpServers && typeof beforeRoot.mcpServers === "object" && !Array.isArray(beforeRoot.mcpServers)
      ? beforeRoot.mcpServers as Record<string, unknown>
      : {};
    const installed = operation.owned?.installed_mcp;
    if (servers.caveman !== undefined && JSON.stringify(servers.caveman) !== JSON.stringify(installed)) {
      throw new Error("Claude caveman MCP entry changed after enable; refusing destructive disable");
    }
    if (servers.caveman !== undefined) {
      if (beforeServers.caveman === undefined) delete servers.caveman;
      else servers.caveman = beforeServers.caveman;
    }
    if (Object.keys(servers).length > 0) currentRoot.mcpServers = servers;
    else delete currentRoot.mcpServers;
    return jsonBytes(currentRoot);
  }
  if (operation.kind === "codex-hooks") {
    return jsonBytes(removeNativeHookEntries(parseJsonFileObject(operation.file, current), "codex"));
  }
  if (operation.kind === "gemini-settings") {
    const currentRoot = removeNativeHookEntries(parseJsonFileObject(operation.file, current), "gemini");
    const beforeRoot = parseJsonFileObject(operation.file, before);
    const servers = currentRoot.mcpServers && typeof currentRoot.mcpServers === "object" && !Array.isArray(currentRoot.mcpServers)
      ? currentRoot.mcpServers as Record<string, unknown>
      : {};
    const beforeServers = beforeRoot.mcpServers && typeof beforeRoot.mcpServers === "object" && !Array.isArray(beforeRoot.mcpServers)
      ? beforeRoot.mcpServers as Record<string, unknown>
      : {};
    const installed = operation.owned?.installed_mcp;
    if (servers.caveman !== undefined && JSON.stringify(servers.caveman) !== JSON.stringify(installed)) {
      throw new Error("Gemini caveman MCP entry changed after enable; refusing destructive disable");
    }
    if (servers.caveman !== undefined) {
      if (beforeServers.caveman === undefined) delete servers.caveman;
      else servers.caveman = beforeServers.caveman;
    }
    if (Object.keys(servers).length > 0) currentRoot.mcpServers = servers;
    else delete currentRoot.mcpServers;
    return jsonBytes(currentRoot);
  }
  if (operation.kind === "gemini-env") {
    const block = operation.owned?.route_block;
    if (typeof block !== "string") throw new Error("Gemini integration journal lacks owned routing block");
    const text = current.toString("utf8");
    if (text.includes(GEMINI_NATIVE_ENV_BEGIN) && !text.includes(block)) {
      throw new Error("Gemini routing block changed after enable; refusing destructive disable");
    }
    return Buffer.from(text.replace(`${block}\n\n`, "").replace(`\n\n${block}\n`, "\n").replace(`${block}\n`, "").replace(block, ""));
  }
  if (operation.kind === "opencode-plugin") {
    throw new Error(`${operation.file} changed after enable; refusing destructive disable`);
  }
  if (operation.kind === "opencode-config") {
    const root = parseJsonFileObject(operation.file, current);
    const providers = root.provider && typeof root.provider === "object" && !Array.isArray(root.provider) ? root.provider as Record<string, unknown> : {};
    const routes = operation.owned?.routes && typeof operation.owned.routes === "object" && !Array.isArray(operation.owned.routes) ? operation.owned.routes as Record<string, unknown> : {};
    const previousRoutes = operation.owned?.previous_routes && typeof operation.owned.previous_routes === "object" && !Array.isArray(operation.owned.previous_routes) ? operation.owned.previous_routes as Record<string, unknown> : {};
    for (const providerID of ["openai", "anthropic"]) {
      const provider = providers[providerID] && typeof providers[providerID] === "object" && !Array.isArray(providers[providerID]) ? providers[providerID] as Record<string, unknown> : {};
      const options = provider.options && typeof provider.options === "object" && !Array.isArray(provider.options) ? provider.options as Record<string, unknown> : {};
      if (options.baseURL !== undefined && options.baseURL !== routes[providerID]) {
        throw new Error(`OpenCode ${providerID} baseURL changed after enable; refusing destructive disable`);
      }
      if (previousRoutes[providerID] === null || previousRoutes[providerID] === undefined) delete options.baseURL;
      else options.baseURL = previousRoutes[providerID];
      if (Object.keys(options).length > 0) provider.options = options;
      else delete provider.options;
      if (Object.keys(provider).length > 0) providers[providerID] = provider;
      else delete providers[providerID];
    }
    if (Object.keys(providers).length > 0) root.provider = providers;
    else delete root.provider;
    const mcp = root.mcp && typeof root.mcp === "object" && !Array.isArray(root.mcp) ? root.mcp as Record<string, unknown> : {};
    if (mcp.caveman !== undefined && JSON.stringify(mcp.caveman) !== JSON.stringify(operation.owned?.installed_mcp)) {
      throw new Error("OpenCode caveman MCP entry changed after enable; refusing destructive disable");
    }
    if (mcp.caveman !== undefined) {
      if (operation.owned?.previous_mcp === null || operation.owned?.previous_mcp === undefined) delete mcp.caveman;
      else mcp.caveman = operation.owned.previous_mcp;
    }
    if (Object.keys(mcp).length > 0) root.mcp = mcp;
    else delete root.mcp;
    return jsonBytes(root);
  }
  if (operation.kind === "aider-config") {
    let text = current.toString("utf8");
    const routeBlock = operation.owned?.route_block;
    const readBlock = operation.owned?.read_block;
    const previousRouteLine = operation.owned?.previous_route_line;
    if (typeof routeBlock !== "string" || typeof readBlock !== "string") {
      throw new Error("Aider integration journal lacks owned blocks");
    }
    if (!text.includes(routeBlock) || !text.includes(readBlock)) {
      throw new Error("Aider Caveman config block changed after enable; refusing destructive disable");
    }
    text = text.replace(routeBlock, typeof previousRouteLine === "string" ? previousRouteLine : "");
    text = text.replace(readBlock, "");
    return Buffer.from(text.replace(/\n{3,}/g, "\n\n"));
  }
  if (operation.kind === "aider-core") {
    throw new Error(`${operation.file} changed after enable; refusing destructive disable`);
  }
  if (operation.kind === "hermes-plugin-manifest" || operation.kind === "hermes-plugin-init") {
    throw new Error(`${operation.file} changed after enable; refusing destructive disable`);
  }
  if (operation.kind === "hermes-config") {
    let text = current.toString("utf8");
    const routeBlock = operation.owned?.route_block;
    const previousRouteLines = operation.owned?.previous_route_lines;
    if (typeof routeBlock !== "string" || !Array.isArray(previousRouteLines)) {
      throw new Error("Hermes integration journal lacks owned routing block");
    }
    if (!text.includes(routeBlock)) throw new Error("Hermes routing block changed after enable; refusing destructive disable");
    text = text.replace(routeBlock, previousRouteLines.filter((line): line is string => typeof line === "string").join("\n"));
    for (const key of ["plugin_block", "mcp_block"] as const) {
      const block = operation.owned?.[key];
      if (block === null || block === undefined) continue;
      if (typeof block !== "string" || !text.includes(block)) throw new Error(`Hermes ${key.replace("_block", "")} block changed after enable; refusing destructive disable`);
      text = text.replace(block, "");
    }
    return Buffer.from(text);
  }
  const rootBlock = operation.owned?.root_block;
  const tablesBlock = operation.owned?.tables_block;
  if (typeof rootBlock !== "string" || typeof tablesBlock !== "string") throw new Error("Codex integration journal lacks owned blocks");
  let text = current.toString("utf8");
  for (const [block, begin] of [[rootBlock, CODEX_NATIVE_ROOT_BEGIN], [tablesBlock, CODEX_NATIVE_TABLES_BEGIN]] as const) {
    if (text.includes(begin) && !text.includes(block)) throw new Error("Codex Caveman config block changed after enable; refusing destructive disable");
    text = text.replace(`${block}\n\n`, "").replace(`\n\n${block}\n`, "\n").replace(block, "");
  }
  return Buffer.from(text);
}

function writeNativeRestoration(file: string, bytes: Buffer | null): void {
  if (bytes) {
    atomicWriteFile(file, bytes);
    return;
  }
  try { unlinkSync(file); } catch (error) {
    if ((error as NodeJS.ErrnoException).code !== "ENOENT") throw error;
  }
}

function restoreNativeJournalFiles(journal: NativeJournal): Array<{ file: string; bytes: Buffer | null }> {
  // Resolve every merge/conflict before first write. A conflict therefore leaves
  // all host files and the journal byte-identical.
  const restored = journal.operations.map((operation) => ({ operation, bytes: restoreNativeOperation(operation) }));
  const current = journal.operations.map((operation) => ({ file: operation.file, bytes: fileBytes(operation.file) }));
  try {
    for (const item of restored) writeNativeRestoration(item.operation.file, item.bytes);
    unlinkSync(nativeJournalPath(journal.agent));
  } catch (error) {
    for (const item of current) {
      try { writeNativeRestoration(item.file, item.bytes); } catch { /* original error remains authority */ }
    }
    throw error;
  }
  return current;
}

function cleanupNativeAgentFiles(target: NativeAgent): void {
  if (target !== "hermes") return;
  try {
    if (readdirSync(hermesNativePluginDir()).length === 0) rmSync(hermesNativePluginDir(), { recursive: true });
  } catch { /* absent or non-empty: preserve */ }
}

function disableNativeAgent(target: NativeAgent): boolean {
  const disabled = withIntegrationLock(target, () => {
    recoverPendingNativeInstallUnlocked(target);
    const journal = readNativeJournal(target);
    if (!journal) return false;
    restoreNativeJournalFiles(journal);
    return true;
  });
  if (!disabled) {
    process.stderr.write(`${mark("warn")} ${target}: no native Caveman integration journal found\n`);
    return false;
  }
  cleanupNativeAgentFiles(target);
  const name = findAgent(target)?.display_name ?? target;
  process.stderr.write(`${mark("ok")} ${name}: ${target === "aider" ? "shallow" : "native"} Caveman disabled; unrelated host edits preserved\n`);
  return true;
}

function repairNativeAgent(target: NativeAgent): void {
  if (!readNativeJournal(target) && !readPendingNativeJournal(target)) {
    enableNative([target]);
    return;
  }
  const profile = findAgent(target)!;
  const gw = gatewayURL();
  withIntegrationLock(target, () => {
    recoverPendingNativeInstallUnlocked(target);
    const journal = readNativeJournal(target);
    if (!journal) throw new Error(`${target} integration journal disappeared during repair`);
    if (!which(binOf(profile))) throw new Error(`${profile.display_name} not found on PATH`);
    const mcpBinary = target === "aider" ? undefined : nativeMcpBinaryRequired();
    nativeProxyBinaryRequired(gw);
    const journalBytes = readFileSync(nativeJournalPath(target));
    const current = restoreNativeJournalFiles(journal);
    try {
      const mutations = nativeMutationsFor(target, gw, mcpBinary);
      applyNativeMutations(target, profile, mutations);
    } catch (error) {
      for (const item of current) {
        try { writeNativeRestoration(item.file, item.bytes); } catch { /* original error remains authority */ }
      }
      atomicWriteFile(nativeJournalPath(target), journalBytes);
      throw error;
    }
  });
  process.stderr.write(`${mark("ok")} ${profile.display_name}: native Caveman repaired; unrelated host edits preserved\n`);
}

function disableNative(argv: string[]) {
  if (argv.length === 1 && argv[0] === "--all") {
    const targets = (["claude", "codex", "hermes", "gemini", "opencode", "aider"] as NativeAgent[])
      .filter((agent) => Boolean(readNativeJournal(agent) || readPendingNativeJournal(agent)));
    if (targets.length === 0) {
      process.stderr.write(`${mark("warn")} no native Caveman integrations are journaled\n`);
      return;
    }
    let failed = 0;
    for (const target of targets) {
      try { disableNativeAgent(target); }
      catch (error) {
        failed++;
        process.stderr.write(`${mark("bad")} ${target}: ${(error as Error).message}\n`);
      }
    }
    if (failed > 0) process.exitCode = 1;
    return;
  }
  const target = argv[0];
  if ((target !== "claude" && target !== "codex" && target !== "hermes" && target !== "gemini" && target !== "opencode" && target !== "aider") || argv.length !== 1) commandUsage("disable <claude|codex|hermes|gemini|opencode|aider> | disable --all");
  disableNativeAgent(target);
}

function nativeIntegrationStatus(agent: NativeAgent) {
  const profile = findAgent(agent)!;
  const host = nativeHostProbe(profile);
  const available = host.launchable;
  const journal = readNativeJournal(agent);
  const transactionPending = Boolean(readPendingNativeJournal(agent));
  const checks = journal?.operations.map((operation) => {
    const current = fileBytes(operation.file);
    let owned = false;
    if (current) {
      try {
        if (operation.kind === "claude-settings") {
          const root = parseJsonFileObject(operation.file, current);
          const env = root.env && typeof root.env === "object" && !Array.isArray(root.env) ? root.env as Record<string, unknown> : {};
          owned = env.ANTHROPIC_BASE_URL === operation.owned?.route && nativeHookEntriesHealthy(root, "claude");
        } else if (operation.kind === "claude-mcp") {
          const root = parseJsonFileObject(operation.file, current);
          const servers = root.mcpServers && typeof root.mcpServers === "object" && !Array.isArray(root.mcpServers) ? root.mcpServers as Record<string, unknown> : {};
          owned = JSON.stringify(servers.caveman) === JSON.stringify(operation.owned?.installed_mcp);
        } else if (operation.kind === "codex-hooks") {
          owned = nativeHookEntriesHealthy(parseJsonFileObject(operation.file, current), "codex");
        } else if (operation.kind === "codex-config") {
          const text = current.toString("utf8");
          owned = typeof operation.owned?.root_block === "string" && typeof operation.owned?.tables_block === "string"
            && text.includes(operation.owned.root_block) && text.includes(operation.owned.tables_block);
        } else if (operation.kind === "hermes-config") {
          const text = current.toString("utf8");
          owned = typeof operation.owned?.route_block === "string" && text.includes(operation.owned.route_block)
            && (operation.owned.plugin_block === null || (typeof operation.owned.plugin_block === "string" && text.includes(operation.owned.plugin_block)))
            && (operation.owned.mcp_block === null || (typeof operation.owned.mcp_block === "string" && text.includes(operation.owned.mcp_block)));
        } else if (operation.kind === "gemini-settings") {
          const root = parseJsonFileObject(operation.file, current);
          const servers = root.mcpServers && typeof root.mcpServers === "object" && !Array.isArray(root.mcpServers) ? root.mcpServers as Record<string, unknown> : {};
          owned = nativeHookEntriesHealthy(root, "gemini")
            && JSON.stringify(servers.caveman) === JSON.stringify(operation.owned?.installed_mcp);
        } else if (operation.kind === "gemini-env") {
          const block = operation.owned?.route_block;
          owned = typeof block === "string" && current.toString("utf8").includes(block);
        } else if (operation.kind === "opencode-config") {
          const root = parseJsonFileObject(operation.file, current);
          const providers = root.provider && typeof root.provider === "object" && !Array.isArray(root.provider) ? root.provider as Record<string, unknown> : {};
          const routes = operation.owned?.routes && typeof operation.owned.routes === "object" && !Array.isArray(operation.owned.routes) ? operation.owned.routes as Record<string, unknown> : {};
          const mcp = root.mcp && typeof root.mcp === "object" && !Array.isArray(root.mcp) ? root.mcp as Record<string, unknown> : {};
          owned = ["openai", "anthropic"].every((providerID) => {
            const provider = providers[providerID] && typeof providers[providerID] === "object" && !Array.isArray(providers[providerID]) ? providers[providerID] as Record<string, unknown> : {};
            const options = provider.options && typeof provider.options === "object" && !Array.isArray(provider.options) ? provider.options as Record<string, unknown> : {};
            return options.baseURL === routes[providerID];
          }) && JSON.stringify(mcp.caveman) === JSON.stringify(operation.owned?.installed_mcp);
        } else if (operation.kind === "opencode-plugin") {
          owned = current.toString("utf8").includes("caveman:native-opencode");
        } else if (operation.kind === "aider-config") {
          const text = current.toString("utf8");
          owned = typeof operation.owned?.route_block === "string" && typeof operation.owned?.read_block === "string"
            && text.includes(operation.owned.route_block) && text.includes(operation.owned.read_block);
        } else if (operation.kind === "aider-core") {
          owned = current.toString("utf8").startsWith(`${AIDER_NATIVE_CORE_MARKER}\n`);
        } else {
          owned = bytesHash(current) === operation.after_sha256;
        }
      } catch { owned = false; }
    }
    return { file: operation.file, present: Boolean(current), exact: Boolean(current && bytesHash(current) === operation.after_sha256), owned };
  }) ?? [];
  const installed = Boolean(journal);
  const expectedPackVersion = NATIVE_PACK.version;
  const packVersion = journal?.pack_version ?? null;
  const packCurrent = installed ? packVersion === expectedPackVersion : null;
  const drifted = checks.some((check) => !check.exact);
  const ownedHealthy = installed && checks.length > 0 && checks.every((check) => check.owned);
  const runtimeConfig = wrapRuntimeConfig();
  const coreResolution = runtimeConfig.resolution.values["think.core"];
  const coreConfigured = coreResolution.value === true;
  const mcp = probeMcpBinary();
  const expectedRoute = appendUrlPath(gatewayURL(), agent === "claude" ? "/w/claude" : agent === "hermes" ? "/w/hermes" : agent === "gemini" ? "/w/gemini" : agent === "opencode" ? "/w/opencode" : agent === "aider" ? "/w/aider/openai/v1" : detectCodexWrapAuthMode() === "subscription" ? "/chatgpt" : "/w/codex");
  const routeKind: NativeMutation["kind"] = agent === "claude" ? "claude-settings" : agent === "codex" ? "codex-config" : agent === "hermes" ? "hermes-config" : agent === "gemini" ? "gemini-env" : agent === "opencode" ? "opencode-config" : "aider-config";
  const routeOperation = journal?.operations.find((operation) => operation.kind === routeKind);
  const routeHealthy = ownedHealthy && (agent === "opencode"
    ? (routeOperation?.owned?.routes as Record<string, unknown> | undefined)?.openai === appendUrlPath(expectedRoute, "/openai/v1")
      && (routeOperation?.owned?.routes as Record<string, unknown> | undefined)?.anthropic === appendUrlPath(expectedRoute, "/anthropic/v1")
    : routeOperation?.owned?.route === expectedRoute);
  const proxyHealthy = wrapMode(gatewayURL()) === "managed" || Boolean(probeProxyVersion()?.capabilities.includes("native_runtime_v1"));
  const recoveryHealthy = agent === "aider" || Boolean(mcp?.probe.current);
  const state = !available ? "unavailable" : transactionPending ? "degraded" : !installed ? "available" : !packCurrent || !ownedHealthy || !routeHealthy || !proxyHealthy || !recoveryHealthy ? "degraded" : "installed";
	const coreSupported = agent === "aider" ? ownedHealthy : ownedHealthy && Boolean(NATIVE_PACK.core);
	const coreActive = agent === "aider"
	  ? ownedHealthy
	  : coreSupported && nativeCoreRuntimeState().active;
  const fileText = checks.map((check) => fileBytes(check.file)?.toString("utf8") ?? "").join("\n");
  const components: NativeComponents = {
    routing: routeHealthy && proxyHealthy && (agent === "claude" ? fileText.includes("ANTHROPIC_BASE_URL") : agent === "codex" ? fileText.includes("model_providers.caveman") : agent === "hermes" ? fileText.includes(HERMES_NATIVE_ROUTE_BEGIN) : agent === "gemini" ? fileText.includes(GEMINI_NATIVE_ENV_BEGIN) : agent === "opencode" ? fileText.includes("caveman:native-opencode") : fileText.includes(AIDER_NATIVE_ROUTE_BEGIN)),
    lifecycle_hooks: agent !== "aider" && ownedHealthy,
    core: coreActive,
    mcp_recovery: agent !== "aider" && ownedHealthy && Boolean(mcp?.probe.current),
    tool_rewrite: agent !== "aider" && ownedHealthy && (agent === "hermes" ? false : fileText.includes("shrink-hook")),
    shared_runtime: proxyHealthy,
  };
  const versionStatus = nativeVersionStatus(host.version, profile.tested_agent_version);
  return {
    agent,
    state,
    available,
    binary_present: Boolean(host.binary),
    launchable: host.launchable,
    version: host.version,
    version_probe_error: host.error,
    tested_version: profile.tested_agent_version ?? null,
    tested: versionStatus === "tested",
    version_status: versionStatus,
    integration_depth: agent === "aider" ? "shallow" : "native",
    ledger_mode: agent === "aider" ? "observational" : "deterministic_events",
    repository_map: agent === "aider"
      ? "host_native_authoritative"
      : components.shared_runtime ? "runtime_local_index_active" : "runtime_local_index_supported_inactive",
    config_precedence: agent === "aider" ? "home config; repository or cwd config may override" : "host_native",
    core_configured: coreConfigured,
    core_supported: coreSupported,
    core_active: coreActive,
    core_enabled: coreActive,
    core_toggle_supported: agent !== "aider",
    core_source: agent === "aider" ? "static_agent_read" : coreResolution.source,
    core_change_boundary: agent === "aider"
      ? "unsupported; disable Aider integration to remove static Core"
      : "new hook events use current setting; start a new session to clear previously delivered Core",
    coding_policy: coreActive ? agent === "aider" ? "core-static" : nativeProfile() : "off",
    installed,
    transaction_pending: transactionPending,
    pack_version: packVersion,
    expected_pack_version: expectedPackVersion,
    pack_current: packCurrent,
    drifted,
    components,
    capabilities: nativeCapabilityReport(agent, components, versionStatus),
    files: checks,
  };
}

function genericIntegrationStatus(runtimeReachable: boolean) {
  const proxy = probeProxyVersion();
  const runtimeAvailable = Boolean(proxy?.capabilities.includes("native_runtime_v1"));
  const supported = new Set<NativeCapability>(["provider_proxy", "local_runtime_available"]);
  const capabilities = Object.fromEntries(NATIVE_CAPABILITIES.map((capability) => [capability, {
    supported: supported.has(capability),
    active: capability === "provider_proxy" ? runtimeReachable : capability === "local_runtime_available" ? runtimeAvailable : false,
    basis: "generic_safe_subset",
  }])) as Record<NativeCapability, { supported: boolean; active: boolean; basis: string }>;
  return {
    agent: "generic",
    state: runtimeReachable ? "active" : "available",
    available: true,
    binary_present: null,
    launchable: null,
    version: null,
    version_probe_error: null,
    tested_version: null,
    tested: false,
    version_status: "host_unknown",
    integration_depth: "fallback",
    ledger_mode: "provider_observational",
    repository_map: "host_owned_or_unavailable",
    config_precedence: "process environment",
    installed: false,
    drifted: false,
    components: {
      routing: runtimeReachable,
      lifecycle_hooks: false,
      core: false,
      mcp_recovery: false,
      tool_rewrite: false,
      shared_runtime: runtimeAvailable,
    },
    capabilities,
    files: [],
  };
}

async function nativeDoctor(argv: string[]) {
  const fix = argv.includes("--fix");
  const target = argv.find((arg) => arg !== "--fix");
  if ((target !== "claude" && target !== "codex" && target !== "hermes" && target !== "gemini" && target !== "opencode" && target !== "aider" && target !== "generic") || argv.length !== (fix ? 2 : 1) || (fix && target === "generic")) commandUsage("doctor <claude|codex|hermes|gemini|opencode|aider|generic> [--fix]");
  if (target === "generic") {
    const { host, port } = gatewayHostPort();
    const result = genericIntegrationStatus(await portListening(host, port));
    print({ ...result, repair: result.components.shared_runtime ? "caveman start" : "caveman setup --install", trust: "no host lifecycle hooks" });
    return;
  }
  const before = nativeIntegrationStatus(target);
  let fixResult: "not_needed" | "enabled" | "repaired" | "recovered" | undefined;
  if (fix) {
    if (!before.available && before.transaction_pending) {
      withIntegrationLock(target, () => recoverPendingNativeInstallUnlocked(target));
      fixResult = "recovered";
    } else if (!before.available) {
      throw new Error(`${findAgent(target)?.display_name ?? target} is unavailable; repair host installation first`);
    } else if (!before.installed) {
      enableNative([target]);
      fixResult = "enabled";
    } else if (before.state === "installed") {
      fixResult = "not_needed";
    } else {
      repairNativeAgent(target);
      fixResult = "repaired";
    }
  }
  const result = nativeIntegrationStatus(target);
  print({
    ...result,
    repair: result.installed ? `caveman doctor ${target} --fix` : `caveman enable ${target}`,
    trust: target === "codex" && result.installed ? "review through Codex /hooks" : "native host policy",
    ...(fixResult ? { fix: { attempted: true, result: fixResult } } : {}),
  });
  if (result.state === "degraded" || result.state === "unavailable") process.exitCode = 1;
}

function openClawProxyBaseUrl(api: string, gatewayUrl: string): string | undefined {
  const suffix = OPENCLAW_API_BASE_PATH[api];
  return suffix === undefined ? undefined : appendUrlPath(gatewayUrl, suffix);
}

function cloneJsonValue<T>(v: T): T {
  return v === undefined ? v : JSON.parse(JSON.stringify(v)) as T;
}

function openClawMirroredModel(ref: OpenClawModelRef, provider: JsonObject, providerModel: JsonObject | undefined): JsonObject {
  const contextWindow =
    typeof providerModel?.contextWindow === "number" ? providerModel.contextWindow :
    typeof provider.contextWindow === "number" ? provider.contextWindow :
    OPENCLAW_MODEL_DEFAULTS.contextWindow;
  const maxTokens =
    typeof providerModel?.maxTokens === "number" ? providerModel.maxTokens :
    typeof provider.maxTokens === "number" ? provider.maxTokens :
    OPENCLAW_MODEL_DEFAULTS.maxTokens;
  const out: JsonObject = {
    id: ref.model,
    name: typeof providerModel?.name === "string" && providerModel.name.trim() ? providerModel.name : ref.model,
    reasoning: typeof providerModel?.reasoning === "boolean" ? providerModel.reasoning : OPENCLAW_MODEL_DEFAULTS.reasoning,
    input: Array.isArray(providerModel?.input) ? cloneJsonValue(providerModel.input) : cloneJsonValue(OPENCLAW_MODEL_DEFAULTS.input),
    cost: asJsonObject(providerModel?.cost) ? cloneJsonValue(providerModel!.cost) : cloneJsonValue(OPENCLAW_MODEL_DEFAULTS.cost),
    contextWindow,
    maxTokens,
  };
  for (const key of ["contextTokens", "thinkingLevelMap", "params", "agentRuntime", "compat", "mediaInput", "metadataSource"] as const) {
    if (providerModel && providerModel[key] !== undefined) out[key] = cloneJsonValue(providerModel[key]);
  }
  return out;
}

function uniqueStrings(values: string[]): string[] {
  const out: string[] = [];
  for (const value of values) if (value && !out.includes(value)) out.push(value);
  return out;
}

function openClawMcpOverlay(): JsonObject {
  return { mcp: { servers: { caveman: { command: "caveman-mcp", args: [] } } } };
}

function openClawPluginDir(): string {
  return join(cavemanHome(), "openclaw", "plugins", OPENCLAW_PLUGIN_ID);
}

function openClawPluginManifest(): JsonObject {
  return {
    schemaVersion: "1",
    id: OPENCLAW_PLUGIN_ID,
    name: "Caveman Shrink",
    version: "1.0.0",
    description: "Routes oversized OpenClaw tool results through caveman shrink before persistence.",
    configSchema: { type: "object", additionalProperties: false, properties: {} },
  };
}

function openClawPluginPackageJson(): JsonObject {
  return {
    type: "module",
    name: "@caveman/openclaw-shrink-plugin",
    version: "1.0.0",
    openclaw: { extensions: ["./index.mjs"] },
  };
}

function openClawPluginSource(): string {
  const { cmd, pre } = cavemanInvocation();
  const argv = JSON.stringify([...pre, "shrink", "--type", "terminal"]);
  return `// caveman:openclaw-shrink-plugin -- GENERATED by Caveman.
// OpenClaw tool_result_persist returns { message }; on any shape/subprocess problem
// this plugin returns nothing, so the original tool result is persisted unchanged.
import { execFileSync } from "node:child_process";
// Focused subpath import per docs/plugins/building-plugins.md:124 ("All imports use
// focused plugin-sdk/<subpath> paths") — the barrel "openclaw/plugin-sdk" import loads
// but its CJS interop leaves definePluginEntry undefined in the plugin loader.
import { definePluginEntry } from "openclaw/plugin-sdk/plugin-entry";

const MIN_CHARS = 12000;

function textBlocks(message) {
  const blocks = [];
  const content = message && typeof message === "object" ? message.content : undefined;
  if (typeof content === "string") blocks.push({ kind: "string", value: content });
  if (Array.isArray(content)) {
    for (const item of content) {
      if (item && typeof item === "object" && item.type === "text" && typeof item.text === "string") {
        blocks.push({ kind: "block", item, value: item.text });
      }
    }
  }
  return blocks;
}

function replaceText(message, original, replacement) {
  if (!message || typeof message !== "object") return message;
  if (typeof message.content === "string" && message.content === original) return { ...message, content: replacement };
  if (Array.isArray(message.content)) {
    return {
      ...message,
      content: message.content.map((item) =>
        item && typeof item === "object" && item.type === "text" && item.text === original ? { ...item, text: replacement } : item,
      ),
    };
  }
  return message;
}

function shrinkText(text) {
  const out = execFileSync(${JSON.stringify(cmd)}, ${argv}, {
    input: text,
    encoding: "utf8",
    maxBuffer: 64 * 1024 * 1024,
  });
  return typeof out === "string" && out.trim() ? out : "";
}

export default definePluginEntry({
  register(api) {
    api.on("tool_result_persist", (event) => {
      try {
        const blocks = textBlocks(event?.message);
        const target = blocks.find((block) => block.value.length >= MIN_CHARS);
        if (!target) return;
        const shrunk = shrinkText(target.value);
        if (!shrunk || shrunk.length >= target.value.length) return;
        return { message: replaceText(event.message, target.value, shrunk) };
      } catch {
        return;
      }
    });
  },
});
`;
}

function ensureOpenClawShrinkPlugin(): string | undefined {
  const dir = openClawPluginDir();
  try {
    mkdirSync(dir, { recursive: true });
    writeFileSync(join(dir, "package.json"), JSON.stringify(openClawPluginPackageJson(), null, 2) + "\n");
    writeFileSync(join(dir, "openclaw.plugin.json"), JSON.stringify(openClawPluginManifest(), null, 2) + "\n");
    writeFileSync(join(dir, "index.mjs"), openClawPluginSource());
    return dir;
  } catch (e) {
    process.stderr.write(`caveman: openclaw shrink plugin setup skipped (${(e as Error).message})\n`);
    return undefined;
  }
}

function openClawPluginOverlay(baseConfig: unknown, pluginDir: string): JsonObject {
  // OpenClaw docs require native plugin packages to be listed in plugins.load.paths
  // and enabled through plugins.entries.<id>; non-bundled conversation hooks need
  // hooks.allowConversationAccess=true for tool_result_persist access.
  const plugins = getObject(baseConfig, ["plugins"]);
  const load = asJsonObject(plugins?.load);
  const existingPaths = Array.isArray(load?.paths) ? load.paths.filter((v): v is string => typeof v === "string") : [];
  const entries = asJsonObject(plugins?.entries);
  const existingAllow = Array.isArray(plugins?.allow) ? plugins.allow.filter((v): v is string => typeof v === "string") : undefined;
  const overlay: JsonObject = {
    plugins: {
      load: { paths: uniqueStrings([...existingPaths, pluginDir]) },
      entries: {
        ...entries,
        [OPENCLAW_PLUGIN_ID]: { enabled: true, hooks: { allowConversationAccess: true } },
      },
    },
  };
  if (existingAllow) {
    (overlay.plugins as JsonObject).allow = uniqueStrings([...existingAllow, OPENCLAW_PLUGIN_ID]);
  }
  return overlay;
}

function openClawBaseOverlay(baseConfig: unknown): JsonObject {
  const overlay = openClawMcpOverlay();
  const pluginDir = ensureOpenClawShrinkPlugin();
  if (pluginDir) return deepMerge(overlay, openClawPluginOverlay(baseConfig, pluginDir)) as JsonObject;
  return overlay;
}

function buildOpenClawOverlay(_agent: AgentProfile, baseConfig: unknown, ctx: OverlayBuilderContext): JsonObject {
  const overlay = openClawBaseOverlay(baseConfig);
  const configuredRef = resolveOpenClawPrimaryRef(baseConfig);
  const ref = configuredRef ?? freshOpenClawModelRef(ctx);
  const synthesized = configuredRef === undefined;
  if (synthesized) {
    process.stderr.write(`caveman: openclaw primary model not found; routing fresh config through caveman/${ref.model}\n`);
  }
  const provider = resolveOpenClawProvider(baseConfig, ref.provider);
  if (!provider) {
    process.stderr.write(`caveman: openclaw provider "${ref.provider}" not found; leaving primary model unchanged\n`);
    return overlay;
  }
  const configuredProvider = openClawProviderConfigured(baseConfig, ref.provider);
  if (!synthesized && openClawProviderUsesOAuth(ref.provider, provider, configuredProvider)) {
    process.stderr.write(`caveman: openclaw primary provider "${ref.provider}" uses OAuth; leaving primary model unchanged and only injecting caveman MCP/plugin\n`);
    return overlay;
  }
  const providerModel = openClawProviderModel(provider, ref.model);
  const api = openClawProviderApi(ref.provider, provider, providerModel);
  if (!api) {
    process.stderr.write(`caveman: openclaw provider "${ref.provider}" has no API adapter; leaving primary model unchanged\n`);
    return overlay;
  }
  const baseUrl = openClawProxyBaseUrl(api, ctx.gatewayUrl);
  if (!baseUrl) {
    process.stderr.write(`caveman: openclaw provider API "${api}" is not mapped to Caveman; leaving primary model unchanged\n`);
    return overlay;
  }
  const sourceApiKey = openClawResolvedProviderApiKey(ref.provider, provider);
  const headers: Record<string, string> = { [OPENCLAW_AGENT_HEADER]: "openclaw" };
  const workflowSlug = normalizeWorkflowSlug(process.env["CAVE_WORKFLOW"]);
  if (workflowSlug) headers["x-cave-workflow"] = workflowSlug;
  if (ctx.mode === "managed" && sourceApiKey) headers["x-cave-upstream-key"] = sourceApiKey;
  const cavemanProvider: JsonObject = {
    baseUrl,
    api,
    apiKey: ctx.mode === "managed" ? firstEnvSecret(ctx.env, ["CAVE_API_KEY"]) : sourceApiKey,
    headers,
    models: [openClawMirroredModel(ref, provider, providerModel)],
  };
  if (!cavemanProvider.apiKey) delete cavemanProvider.apiKey;

  const modelsAllow = getObject(baseConfig, ["agents", "defaults", "models"]);
  const defaultModelsOverlay = modelsAllow ? {
    models: {
      ...modelsAllow,
      [`caveman/${ref.model}`]: cloneJsonValue(openClawModelConfig(baseConfig, ref) ?? {}),
    },
  } : undefined;
  return deepMerge(overlay, {
    models: { mode: "merge", providers: { caveman: cavemanProvider } },
    agents: {
      defaults: {
        model: { primary: `caveman/${ref.model}` },
        ...(defaultModelsOverlay ? defaultModelsOverlay : {}),
      },
    },
  }) as JsonObject;
}

overlayBuilders.openclaw = buildOpenClawOverlay;

// withoutCavemanMcpServer removes the profile overlay's own `mcp.servers.caveman`
// entry. This is the SECOND way wrap injects the engine MCP tools: config-file
// agents (openclaw) get them from the rendered overlay on every launch, not from
// `maybeInstallMcp`, so skipping the install alone left the ~11k-token surface
// fully in place — and `mcp uninstall` could not remove it, because the overlay
// is re-rendered fresh each run. Only the caveman server is dropped; sibling
// servers (caveman-browse, the user's own) have their own knobs and are the
// user's. Empty parents are pruned so a stripped overlay merges to the same bytes
// as no overlay at all.
function withoutCavemanMcpServer(overlay: JsonObject): JsonObject {
  if (!getObject(overlay, ["mcp", "servers", "caveman"])) return overlay;
  const next = cloneJsonValue(overlay);
  const mcp = getObject(next, ["mcp"])!;
  const servers = getObject(next, ["mcp", "servers"])!;
  delete servers["caveman"];
  if (Object.keys(servers).length === 0) delete mcp["servers"];
  if (Object.keys(mcp).length === 0) delete next["mcp"];
  return next;
}

function applyConfigFileInjection(env: NodeJS.ProcessEnv, agent: AgentProfile, inj: ConfigFileInjection, gw: string, modeGw = gw, mcpMode: McpSurfaceMode = "auto") {
  const baseConfig = readBaseConfig(inj);
  const mode = wrapMode(modeGw);
  const staticOverlay = mode === "managed" && inj.config_overlay.managed !== undefined ? inj.config_overlay.managed : inj.config_overlay.local;
  const builder = overlayBuilders[agent.id];
	const rawOverlay = renderDeep(builder ? builder(agent, baseConfig, { mode, gatewayUrl: gw, env }) : staticOverlay, gw);
  const overlay = mcpMode === "auto" ? rawOverlay : withoutCavemanMcpServer(rawOverlay as JsonObject);
  const merged = deepMerge(baseConfig, overlay);
  const rendered = JSON.stringify(merged, null, 2);
  if (rendered === undefined) throw new Error("merged config is not JSON");
  const outDir = mkdtempSync(join(tmpdir(), "caveman-wrap-"));
  const outPath = join(outDir, `${agent.id}.json`);
  writeFileSync(outPath, rendered + "\n", { mode: 0o600 });
  wrapTempDirs.add(outDir);
  env[inj.env_var] = outPath;
}

function cleanupWrapTempDirs() {
  for (const dir of [...wrapTempDirs]) {
    try {
      rmSync(dir, { recursive: true, force: true });
    } catch {
      // Temp cleanup is best-effort; wrap must never fail after child exit.
    } finally {
      wrapTempDirs.delete(dir);
    }
  }
}

const WRAP_BASE_URL_ENV_VARS = ["ANTHROPIC_BASE_URL", "OPENAI_BASE_URL", "OPENAI_API_BASE", "GOOGLE_GEMINI_BASE_URL"] as const;

function wrapBaseUrlEnv(gw: string): NodeJS.ProcessEnv {
  const env: NodeJS.ProcessEnv = {};
  for (const key of WRAP_BASE_URL_ENV_VARS) env[key] = gw;
  return env;
}

function attributedGatewayUrl(gw: string, agent: AgentProfile): string {
  return appendUrlPath(gw, `/w/${agent.id}`);
}

// Claude Code accepts custom request headers as newline-separated `Name: Value`
// lines. Preserve every unrelated line, remove all case variants of the target
// auth header, then append exactly one current value when one is supplied.
function mergeAnthropicCustomHeader(raw: string | undefined, name: string, value: string | undefined): string {
  const target = name.toLowerCase();
  const kept = (raw ?? "")
    .split(/\r\n|\n|\r/)
    .filter((line) => {
      if (!line.trim()) return false;
      const colon = line.indexOf(":");
      const headerName = (colon < 0 ? line : line.slice(0, colon)).trim().toLowerCase();
      return headerName !== target;
    });
  if (value !== undefined) kept.push(`${name}: ${value}`);
  return kept.join("\n");
}

function bedrockCredentialEnvValue(env: NodeJS.ProcessEnv, name: string): string | undefined {
  const raw = env[name];
  if (typeof raw !== "string" || !raw.trim()) return undefined;
  if (/[\r\n]/.test(raw)) throw new Error(`${name} must not contain a newline`);
  return raw.trim();
}

function bedrockUpstreamCredentialFromEnv(env: NodeJS.ProcessEnv): string | undefined {
  const bearer = bedrockCredentialEnvValue(env, "AWS_BEARER_TOKEN_BEDROCK");
  if (bearer) return bearer;
  const accessKey = bedrockCredentialEnvValue(env, "AWS_ACCESS_KEY_ID");
  const secretKey = bedrockCredentialEnvValue(env, "AWS_SECRET_ACCESS_KEY");
  const sessionToken = bedrockCredentialEnvValue(env, "AWS_SESSION_TOKEN");
  if (!accessKey && !secretKey && !sessionToken) return undefined;
  if (!accessKey || !secretKey) {
    throw new Error("AWS_ACCESS_KEY_ID and AWS_SECRET_ACCESS_KEY must be set together");
  }
  return [accessKey, secretKey, ...(sessionToken ? [sessionToken] : [])].join(":");
}

// applyClaudeBedrockWrap selects Claude Code's native Bedrock or opt-in Mantle
// transport only when the operator explicitly requests it. The default Claude
// profile remains Anthropic-wire. Runtime uses Claude Code's documented AWS
// credential chain; unlike Mantle, Claude Code exposes no supported Runtime
// authentication-bypass variable. In managed mode the validated BYOK value also
// rides through Claude's custom headers. Stored-only, server-injected Claude
// Code auth therefore uses Mantle's documented gateway mode.
function applyClaudeBedrockWrap(env: NodeJS.ProcessEnv, agent: AgentProfile, renderedGw: string, modeGw: string): boolean {
  if (agent.id !== "claude" || process.env.CAVEMAN_WRAP_PROVIDER?.trim().toLowerCase() !== "bedrock") return false;

  const endpoint = process.env.CAVEMAN_BEDROCK_ENDPOINT?.trim().toLowerCase() || "runtime";
  if (endpoint !== "runtime" && endpoint !== "mantle") {
    throw new Error("CAVEMAN_BEDROCK_ENDPOINT must be runtime or mantle");
  }

  // Remove the generic/profile Anthropic route and any stale Bedrock selection
  // inherited from the shell. Exactly one Claude Code provider lane is active.
  for (const key of WRAP_BASE_URL_ENV_VARS) delete env[key];
  delete env.ANTHROPIC_AUTH_TOKEN;
  delete env.CLAUDE_CODE_USE_BEDROCK;
  // Remove stale values if a caller previously relied on this undocumented
  // variable. We intentionally never set it.
  delete env.CLAUDE_CODE_SKIP_BEDROCK_AUTH;
  delete env.ANTHROPIC_BEDROCK_BASE_URL;
  delete env.CLAUDE_CODE_USE_MANTLE;
  delete env.CLAUDE_CODE_SKIP_MANTLE_AUTH;
  delete env.ANTHROPIC_BEDROCK_MANTLE_BASE_URL;

  const bedrockBase = appendUrlPath(renderedGw, "/bedrock");
  if (endpoint === "mantle") {
    env.CLAUDE_CODE_USE_MANTLE = "1";
    env.CLAUDE_CODE_SKIP_MANTLE_AUTH = "1";
    // Claude Code appends /v1/messages verbatim to this override. Caveman's
    // explicit Mantle adapter route is /bedrock/anthropic/v1/messages.
    env.ANTHROPIC_BEDROCK_MANTLE_BASE_URL = appendUrlPath(bedrockBase, "/anthropic");
  } else {
    env.CLAUDE_CODE_USE_BEDROCK = "1";
    env.ANTHROPIC_BEDROCK_BASE_URL = bedrockBase;
  }
  if (wrapMode(modeGw) === "managed") {
    const caveAPIKey = firstEnvSecret(env, ["CAVE_API_KEY"]);
    if (!caveAPIKey || /[\r\n]/.test(caveAPIKey)) {
      throw new Error("managed Bedrock wrap requires a valid CAVE_API_KEY");
    }
    env.ANTHROPIC_CUSTOM_HEADERS = mergeAnthropicCustomHeader(
      env.ANTHROPIC_CUSTOM_HEADERS,
      "x-cave-api-key",
      caveAPIKey,
    );
    const upstreamKey = bedrockUpstreamCredentialFromEnv(env);
    env.ANTHROPIC_CUSTOM_HEADERS = mergeAnthropicCustomHeader(
      env.ANTHROPIC_CUSTOM_HEADERS,
      "x-cave-upstream-key",
      upstreamKey,
    );
  }
  return true;
}

// buildWrapEnv computes the child environment for a wrapped agent. It starts from
// the generic provider base-URL union (the fail-open fallback — harmless for an
// agent that reads only a subset, and the behavior every wrap had before profiles),
// using the bare gateway for raw wraps and the per-agent attribution path for profiles,
// then layers the profile's injection on top:
//   - env:                set each var (omitting any that render empty)
//   - config-env-content: render the mode-selected inline config and set it as one var
//   - config-file:        merge a mode-selected overlay into a temp config file
// An unrecognized method falls through to the generic union (fail-open), not a guess.
// mcpMode governs the config-file arm only (it is the arm that can inject the
// caveman MCP server). It defaults to "auto" — today's behavior — rather than
// resolving config here, so this exported function stays a function of its
// arguments and cannot pick up the developer's own global config in tests. The
// one production caller passes the SAME opts.mcpMode that wrapMcpRecoveryAvailable
// reads, which is what keeps "what we inject" and "what we tell the proxy" from
// ever disagreeing.
export function buildWrapEnv(agent?: AgentProfile, gw = gatewayURL(), mcpMode: McpSurfaceMode = "auto"): NodeJS.ProcessEnv {
  if (agent?.id === "gemini" && wrapMode(gw) === "managed") {
    throw new Error("managed Gemini CLI routing is unsupported because Gemini CLI cannot send separate Caveman and upstream credentials");
  }
  const renderedGw = agent ? attributedGatewayUrl(gw, agent) : gw;
  const env: NodeJS.ProcessEnv = { ...process.env, ...wrapBaseUrlEnv(renderedGw) };
  if (wrapMode(gw) === "managed") {
    const gatewayKey = connectedGatewayAPIKey();
    if (gatewayKey) env.CAVE_API_KEY = gatewayKey;
  }
  if (!agent) return env;
  if (applyClaudeBedrockWrap(env, agent, renderedGw, gw)) return env;
  const inj = agent.injection;
  if (inj.method === "env") {
    for (const [k, raw] of Object.entries(inj.env)) {
      const val = renderTemplate(raw, renderedGw);
      if (val !== "") env[k] = val;
    }
  } else if (inj.method === "config-env-content") {
    const cc = inj.config_content;
    const content = wrapMode(gw) === "managed" && cc.managed !== undefined ? cc.managed : cc.local;
    env[inj.env_var] = JSON.stringify(renderDeep(content, renderedGw));
  } else if (inj.method === "config-file") {
    try {
      applyConfigFileInjection(env, agent, inj, renderedGw, gw, mcpMode);
    } catch (e) {
      // OpenClaw ignores the generic base-URL union. Config injection is its only
      // provider redirect, so a failed/missing route must abort the wrapped path;
      // spawnWrapped then launches direct with an explicit warning.
      if (agent.id === "openclaw") throw e;
      process.stderr.write(`caveman: ${agent.id} config-file injection failed; using generic env wrap (${(e as Error).message})\n`);
    }
  }
  if (agent.id === "hermes") applyHermesAuthEnv(env, renderedGw, gw);
  return env;
}

// orgIdFromConfigFile reads the (non-secret) organization id straight from
// config.json — a cheap file read that avoids touching the keychain on the hot
// wrap path. Empty when logged out or unset.
function orgIdFromConfigFile(): string {
  try {
    const parsed = JSON.parse(readFileSync(configPath(), "utf8")) as { organizationId?: unknown };
    return typeof parsed.organizationId === "string" ? parsed.organizationId : "";
  } catch {
    return "";
  }
}

// wrapInteractive opens an arrow-key picker of the known agents, marking which
// are installed, then launches the chosen one (or shows its install hint).
async function wrapInteractive() {
  const rows = AGENTS.map((a) => ({ agent: a, found: !!which(binOf(a)) }));
  const choice = await selectMenu(
    "Which agent should Caveman wrap?",
    rows.map((r) => ({
      label: r.agent.display_name,
      hint: r.found ? `${green("installed")} ${dim("· " + r.agent.vendor)}` : dim(`not installed · ${r.agent.install}`),
    })),
  );
  if (choice < 0) {
    process.stderr.write(dim("cancelled\n"));
    return;
  }
  const picked = rows[choice];
  if (!picked) return;
  if (!picked.found) {
    wrapNotFoundUI(picked.agent.id, picked.agent);
    process.exit(127);
  }
  const pickedOpts = defaultWrapOptions();
  await bootstrapLocalWrapRuntime(pickedOpts);
  await firstRunExperience();
  await runWrapped(which(binOf(picked.agent))!, picked.agent.args, picked.agent, pickedOpts);
}

// wrapNotFoundUI explains a wrap target that isn't on PATH: an install hint for a
// known agent, or the list of agents you can wrap (plus the raw-command form).
function wrapNotFoundUI(requested: string, agent?: AgentProfile) {
  if (agent) {
    panel(`${agent.display_name} isn't installed`, [
      `${mark("bad")} Couldn't find ${cyan(binOf(agent))} on your PATH.`,
      "",
      `Install it, then re-run ${cyan(`caveman wrap ${agent.id}`)}:`,
      `   ${dim(agent.install)}`,
    ]);
    return;
  }
  panel(`Command not found: ${requested}`, [
    `${mark("bad")} ${cyan(requested)} isn't on your PATH.`,
    "",
    "Wrap a known agent by name:",
    ...AGENTS.map((a) => `   ${cyan(a.id.padEnd(8))} ${dim(a.display_name)}`),
    "",
    `Or wrap any command:  ${cyan("caveman wrap <command> [args...]")}`,
    `Pick interactively:   ${cyan("caveman wrap")}`,
  ]);
}

// login runs the RFC-8628 device-authorization flow: request a code, show the
// user the URL + code to approve in a browser, then poll until the code is
// exchanged for an access token. The token is stored in the OS keychain (or a
// resolveLoginGatewayUrl decides the managed gateway URL to persist so that, after
// login, `caveman wrap` routes through the cloud with no env var
// (SIMPLICITY_SPEC §6.5). Precedence: explicit --gateway-url flag > CAVE_GATEWAY_URL
// set at login > a gateway_url advertised by the device/authorization response
// (forward-compatible if control-api starts returning it) > derived from the
// control-API base URL for the two shapes Caveman ships. Returns "" when it cannot
// derive one honestly — wrap then stays local until the user sets CAVE_GATEWAY_URL.
function resolveLoginGatewayUrl(baseURL: string, tok: Record<string, unknown>, code: Record<string, unknown>, argv: string[]): string {
  const flagged = flagFrom(argv, "--gateway-url", "");
  if (flagged) return flagged;
  if (process.env.CAVE_GATEWAY_URL) return process.env.CAVE_GATEWAY_URL;
  const advertised = (typeof tok.gateway_url === "string" && tok.gateway_url) || (typeof code.gateway_url === "string" && code.gateway_url);
  if (advertised) return advertised as string;
  return deriveGatewayUrl(baseURL);
}

// deriveGatewayUrl maps a control-API base URL to its sibling gateway for the two
// shapes Caveman ships: local docker (8080 control-api -> 8787 gateway) and the
// hosted `api.<domain>` -> `gateway.<domain>` convention. Anything else returns ""
// (no guessing) so login never persists a gateway that may not exist.
function deriveGatewayUrl(baseURL: string): string {
  try {
    const u = new URL(baseURL);
    if (u.hostname === "localhost" || u.hostname === "127.0.0.1") return `${u.protocol}//${u.hostname}:8787`;
    if (u.hostname.startsWith("api.")) return `${u.protocol}//gateway.${u.hostname.slice(4)}`;
  } catch {
    // not a URL we can map; fall through to ""
  }
  return "";
}

// resolveLoginBaseUrl picks the control-API host `caveman login` talks to.
// Precedence: --base-url flag > CAVE_API_URL env var > the deployed prod API.
// The default used to be a local dev port (localhost:8080) that nothing is
// listening on for a new developer who hasn't set CAVE_API_URL — stalling the
// funnel's single most important step. Local development stays a one-flag or
// one-env-var operation (`caveman login --base-url http://localhost:8080` or
// `CAVE_API_URL=http://localhost:8080 caveman login`).
export function resolveLoginBaseUrl(argv: string[]): string {
  return flagFrom(argv, "--base-url", process.env.CAVE_API_URL ?? PROD_API_URL);
}

// RFC 8628 §3.5's slow_down response increases the polling interval by five
// seconds for every subsequent request. Keep this pure so timing behavior is
// testable without waiting in a runtime HTTP test.
export function nextDevicePollIntervalMs(currentMs: number, errorCode?: string): number {
  const current = Math.max(0, Number.isFinite(currentMs) ? currentMs : 0);
  return errorCode === "slow_down" ? current + 5000 : current;
}

export function loginBrowserOpener(
  url: string,
  platform: NodeJS.Platform = process.platform,
): { command: string; args: string[] } {
  if (platform === "darwin") return { command: "open", args: [url] };
  if (platform === "win32") return { command: "rundll32.exe", args: ["url.dll,FileProtocolHandler", url] };
  return { command: "xdg-open", args: [url] };
}

export function shouldOpenLoginBrowser(noBrowser: boolean, interactive = Boolean(process.stdin.isTTY)): boolean {
  return interactive && !noBrowser;
}

function validateLoginArgs(argv: string[]): { noBrowser: boolean } {
  let noBrowser = false;
  for (let index = 0; index < argv.length; index++) {
    const arg = argv[index]!;
    if (arg === "--no-browser") {
      if (noBrowser) commandUsage("login [--no-browser] [--base-url <url>] [--gateway-url <url>]");
      noBrowser = true;
      continue;
    }
    if (arg === "--base-url" || arg === "--gateway-url") {
      const value = argv[++index];
      if (!value || value.startsWith("-")) commandUsage("login [--no-browser] [--base-url <url>] [--gateway-url <url>]");
      continue;
    }
    if (arg.startsWith("--base-url=") || arg.startsWith("--gateway-url=")) {
      if (!arg.slice(arg.indexOf("=") + 1)) commandUsage("login [--no-browser] [--base-url <url>] [--gateway-url <url>]");
      continue;
    }
    commandUsage("login [--no-browser] [--base-url <url>] [--gateway-url <url>]");
  }
  return { noBrowser };
}

function openLoginBrowser(url: string): void {
  const opener = loginBrowserOpener(url);
  if (!which(opener.command)) {
    process.stderr.write(`  browser opener unavailable; open ${url}\n`);
    return;
  }
  const child = spawn(opener.command, opener.args, { detached: true, stdio: "ignore", windowsHide: true });
  child.once("error", () => process.stderr.write(`  browser did not open; open ${url}\n`));
  child.unref();
}

// acknowledgeDeviceGrant is the client receipt fence for durable device
// credentials. The token endpoint can only prove that the HTTP server accepted
// bytes; this second request is sent after the credential envelope is persisted
// locally. Retries are safe when the ACK response itself is lost because the
// control API treats an acknowledged activation idempotently.
async function acknowledgeDeviceGrant(baseURL: string, accessToken: string, deviceCode: string, ackToken: string): Promise<void> {
  let lastError = "unknown error";
  for (let attempt = 0; attempt < 5; attempt++) {
    try {
      const response = await fetch(`${baseURL}/api/v1/auth/device/ack`, {
        method: "POST",
        headers: {
          authorization: `Bearer ${accessToken}`,
          "content-type": "application/json",
          "x-cave-client": "cli",
        },
        body: JSON.stringify({ device_code: deviceCode, ack_token: ackToken }),
        signal: AbortSignal.timeout(5000),
      });
      if (response.ok) return;
      const body = await response.json().catch(() => null) as { error?: { code?: unknown } } | null;
      const code = typeof body?.error?.code === "string" ? body.error.code : `HTTP ${response.status}`;
      lastError = code;
      // Invalid/expired grants are terminal. Infrastructure responses remain
      // retryable so a committed ACK whose response was dropped can converge.
      if (response.status < 500 && response.status !== 429) break;
    } catch (error) {
      lastError = error instanceof Error ? error.message : String(error);
    }
    if (attempt < 4) await sleep(Math.min(2000, 200 * 2 ** attempt));
  }
  throw new Error(`device credential delivery acknowledgement failed (${lastError}); credentials were persisted locally but the server may revoke them after the delivery window`);
}

// 0600 credentials file) — never in plaintext config. organization_id is bound
// from the returned token, never from any local input.
// Keep device-flow implementation dormant for later beta reopening. This gate
// runs before argument parsing, network requests, browser launch, or local writes.
function blockCloudLoginWhileBeta(): void {
  throw new Error("Caveman Cloud platform is still in beta.");
}

async function login(argv: string[] = []) {
  blockCloudLoginWhileBeta();
  const { noBrowser } = validateLoginArgs(argv);
  const baseURL = resolveLoginBaseUrl(argv);

  const codeResp = await fetch(`${baseURL}/api/v1/auth/device/code`, {
    method: "POST",
    headers: { "content-type": "application/json" },
    body: "{}",
    signal: AbortSignal.timeout(5000),
  });
  if (!codeResp.ok) throw new Error(`device authorization failed: HTTP ${codeResp.status}`);
  const code = await codeResp.json();
  if (!code.device_code) throw new Error(`device authorization failed: ${JSON.stringify(code)}`);

  const verificationURL = code.verification_uri_complete ?? code.verification_uri;
  console.error(`\n  Authorize this device in your browser:`);
  console.error(`    ${verificationURL}`);
  console.error(`    code: ${code.user_code}\n`);
  if (typeof verificationURL === "string" && shouldOpenLoginBrowser(noBrowser)) openLoginBrowser(verificationURL);

  let intervalMs = Math.max(0, Number(code.interval ?? 5)) * 1000;
  const deadline = Date.now() + Number(code.expires_in ?? 600) * 1000;
  while (Date.now() < deadline) {
    let tok: Record<string, unknown>;
    let tokenStatus = 0;
    let retryAfterMs = 0;
    try {
      const tokResp = await fetch(`${baseURL}/api/v1/auth/device/token`, {
        method: "POST",
        headers: { "content-type": "application/json" },
        body: JSON.stringify({ device_code: code.device_code }),
        signal: AbortSignal.timeout(5000),
      });
      tokenStatus = tokResp.status;
      const retryAfter = tokResp.headers.get("retry-after");
      if (retryAfter) {
        const seconds = Number(retryAfter);
        if (Number.isFinite(seconds) && seconds >= 0) retryAfterMs = seconds * 1000;
      }
      tok = await tokResp.json() as Record<string, unknown>;
    } catch (error) {
      // RFC 8628 polling is retryable: a dropped connection or malformed
      // transient response must not consume the approved code or abort login
      // before the bounded device deadline. The next poll can reclaim the
      // server-side lease and replay the same durable bundle.
      if (Date.now() >= deadline) {
        throw new Error(`device login polling failed: ${error instanceof Error ? error.message : String(error)}`);
      }
      await sleep(Math.max(intervalMs, retryAfterMs, 200));
      continue;
    }
    if (tokenStatus === 429) {
      // rateLimitAuth returns a nested cave error envelope rather than the RFC
      // `error` string. Status is the authoritative retry signal here.
      await sleep(Math.max(intervalMs, retryAfterMs, 200));
      continue;
    }
    const accessToken = typeof tok.access_token === "string" ? tok.access_token : "";
    if (accessToken) {
	  const credentials: StoredCredentials = {
	    access_token: accessToken,
	    ...(typeof tok.refresh_token === "string" && tok.refresh_token ? { refresh_token: tok.refresh_token } : {}),
	    ...(typeof tok.gateway_api_key === "string" && tok.gateway_api_key ? { gateway_api_key: tok.gateway_api_key } : {}),
	    ...(typeof tok.gateway_key_id === "string" && tok.gateway_key_id ? { gateway_key_id: tok.gateway_key_id } : {}),
	    ...(typeof tok.project_id === "string" && tok.project_id ? { project_id: tok.project_id } : {}),
	  };
	  const tokenStore = storeCredentials(credentials);
	  const organizationId = orgFromToken(accessToken);
	  const gateway = resolveLoginGatewayUrl(baseURL, tok, code, argv);
	  const saved: Config = { baseURL, token: "", tokenStore };
	  if (organizationId) saved.organizationId = organizationId;
	  if (credentials.project_id) saved.projectId = credentials.project_id;
	  if (gateway) saved.gatewayUrl = gateway;
	  // Persist the complete local login state before the server-side receipt fence:
	  // an ACK may permanently purge the replay bundle, so a config write that fails
	  // must leave the grant retryable rather than acknowledging an undiscoverable
	  // credential.
	  await saveConfig(saved);
	  const durableGrant = Boolean(credentials.refresh_token || credentials.gateway_api_key || credentials.gateway_key_id || credentials.project_id);
	  const ackToken = typeof tok.delivery_ack_token === "string" ? tok.delivery_ack_token : "";
	  if (durableGrant) {
	    if (!ackToken) throw new Error("device login failed: server did not provide a delivery acknowledgement token");
	    // Do not print authenticated success or continue the post-login bridge
	    // until the control plane has recorded that this CLI stored the bundle.
	    await acknowledgeDeviceGrant(baseURL, credentials.access_token, code.device_code, ackToken);
	  }
      // Mint/refresh the local-wrap entitlement for this device. Best
      // effort: login never fails for seats or a down entitlement service.
      await fetchAndStoreWrapEntitlement(baseURL, credentials.access_token);
      if (gateway && wrapMode(gateway) === "managed") {
        console.error(`  ${mark("ok")} wrap now routes through the managed gateway (${gateway}) — governed reporting; verified stays zero without qualifying provider evidence`);
      } else if (gateway) {
        console.error(`  ${mark("ok")} connected; wrap routes through ${gateway}`);
      }
      console.error(SYNC_DISCLOSURE);
      print({ authenticated: true, baseURL, gateway_url: gateway || null, organization_id: organizationId ?? null, token_store: tokenStore });
      // The funnel bridge: pull the spans the local proxy already measured into
      // the dashboard, once, right now (always labeled inferred; best-effort).
      await syncAfterLogin();
      return;
    }
    const errorCode = typeof tok.error === "string" ? tok.error : "";
    if (errorCode === "slow_down") {
      intervalMs = nextDevicePollIntervalMs(intervalMs, errorCode);
    } else if (errorCode && errorCode !== "authorization_pending") {
      throw new Error(`device login failed: ${errorCode}`);
    }
    await sleep(Math.max(intervalMs, 200));
  }
  throw new Error("device login timed out before approval");
}

async function logout() {
	const cfg = await config();
	const externalToken = Boolean(process.env.CAVE_TOKEN);
	if (cfg.token && (!cfg.logoutPendingLocalCleanup || externalToken)) {
	  if (cfg.projectId && cfg.gatewayKeyId) {
	    let response: Response;
	    try {
	      response = await fetch(`${cfg.baseURL}/api/v1/projects/${encodeURIComponent(cfg.projectId)}/keys/${encodeURIComponent(cfg.gatewayKeyId)}/revoke`, {
	        method: "POST",
	        headers: { authorization: `Bearer ${cfg.token}`, "content-type": "application/json", "x-cave-csrf": "cli" },
	        body: "{}",
	        signal: AbortSignal.timeout(5000),
	      });
	    } catch {
	      throw new Error("caveman: remote gateway key revocation was unavailable; credentials kept — retry `caveman logout`");
	    }
	    if (!response.ok) {
	      const body = await response.json().catch(() => null) as { error?: { code?: unknown } } | null;
	      const alreadyRevoked = response.status === 404 && body?.error?.code === "cave_key_not_found";
	      if (!alreadyRevoked) throw new Error(`caveman: remote gateway key revocation failed (HTTP ${response.status}); credentials kept — retry \`caveman logout\``);
	    }
	  }
	  const headers: Record<string, string> = {
	    authorization: `Bearer ${cfg.token}`,
	    "x-cave-client": "cli",
	  };
	  const request: RequestInit = {
	    method: "POST",
	    headers,
	    signal: AbortSignal.timeout(5000),
	  };
	  if (cfg.refreshToken) {
	    headers["content-type"] = "application/json";
	    request.body = JSON.stringify({ refresh_token: cfg.refreshToken });
	  }
	  let response: Response;
	  try {
	    response = await fetch(`${cfg.baseURL}/api/v1/auth/logout`, request);
	  } catch {
	    throw new Error("caveman: remote session revocation was unavailable; credentials kept — retry `caveman logout`");
	  }
	  if (!response.ok) throw new Error(`caveman: remote session revocation failed (HTTP ${response.status}); credentials kept — retry \`caveman logout\``);
	}
	if (externalToken) {
	  console.error("caveman: remote session revoked; CAVE_TOKEN remains set by the parent environment — unset it before the next command");
	  print({ logged_out: true, remote_session_revoked: true, external_token_cleared: false, external_token_source: "CAVE_TOKEN" });
	  return;
	}
	// Persist remote completion before deleting the only local credential. If
	// Keychain/file cleanup fails, the next logout safely retries cleanup without
	// trying to authenticate with the now-revoked session.
	await saveConfig({ ...cfg, logoutPendingLocalCleanup: true });
  clearToken(cfg.tokenStore);
  await saveConfig({ baseURL: "", token: "" });
  print({ logged_out: true });
}

function sleep(ms: number) {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

// ── caveman sync ─────────────────────────────────────────────────────────────
// The local→cloud savings bridge: read the standalone proxy's local spend store
// (~/.caveman/caveman.db, the same file `caveman stats` summarizes) since the
// last successful sync, convert each request row to a caveman-jsonl span, and
// POST it to the control-api imports endpoint under the logged-in credentials
// (org/project are stamped server-side from the JWT — tenant-scoped rule).
//
// HONESTY: everything the standalone proxy records is `inferred`, and this
// command only moves those rows — it never relabels, never projects, and the
// word "inferred" is always in its output. verified_savings stays 0 until an
// eval-gated optimizer runs active on real cloud traffic (no-fake-savings).
//
// Idempotency: the max synced rowid is persisted per (control-api, org) in
// ~/.caveman-cloud/sync.json, so re-running never re-uploads a span. The
// watermark only advances after the server confirms the import completed.

type SyncOutcome =
  | { kind: "no_store"; dbPath: string }
  | { kind: "empty" }
  | {
      kind: "synced";
      spans: number;
      tokensSaved: number;
      tokenCountBasis: string;
      cachedInputTokens: number;
      cacheCreationInputTokens: number;
      headlineCompressionRefused: boolean;
      savingsUSD: number;
      dashboard: string;
      firstSync: boolean;
    };

type SyncedOutcome = Extract<SyncOutcome, { kind: "synced" }>;

type PracticeSyncOutcome =
  | { kind: "no_snapshot" }
  | { kind: "synced"; findings: number };

type LocalScanFamilyUpload = { id: "tool_outputs" | "repeated_blocks"; tokens: number };
type LocalScanUpload = {
  schema: "caveman.local_scan.v1";
  source: "local_scan";
  basis: "inferred";
  scanned_at: string;
  window_days: 30;
  sessions_total: number;
  sessions_scanned: number;
  turns_observed: number;
  tokens_observed: number;
  tokens_observed_source: "session_usage" | "o200k_estimate";
  would_cut_tokens: number;
  would_cut_stream_tokens?: number;
  families: LocalScanFamilyUpload[];
  config_prefix_tokens_per_turn: number;
  engine_used: boolean;
  time_boxed: boolean;
};
type LocalScanState = {
  schema: "caveman.local_scan.pending.v1";
  payload: LocalScanUpload;
  delivered?: {
    import_id: string;
    project_id: string;
    organization_id: string;
    base_url: string;
    delivered_at: string;
  };
};
type LocalScanSyncOutcome =
  | { kind: "no_snapshot" | "already_synced" }
  | { kind: "synced"; importId: string; dashboard: string };

function localSpendDbPath(): string {
  return process.env.CAVEMAN_DB ?? join(caveHome(), "caveman.db");
}

function syncStatePath(): string {
  return join(dirname(configPath()), "sync.json");
}

function localScanStatePath(): string {
  return join(dirname(configPath()), "local-scan.json");
}

function localScanCounter(value: unknown): number | null {
  return typeof value === "number" && Number.isSafeInteger(value) && value >= 0 ? value : null;
}

// localScanUploadFromRetro strips every free-text caveat/path from the local
// proxy result. Only closed family ids and aggregate counters may cross the
// authenticated boundary; control-api validates the same contract again.
function localScanUploadFromRetro(retro: LearnRetro, scannedAt: Date): LocalScanUpload | null {
  const sessionsTotal = localScanCounter(retro.sessions_total);
  const sessionsScanned = localScanCounter(retro.sessions_scanned);
  const turnsObserved = localScanCounter(retro.turns_observed);
  const tokensObserved = localScanCounter(retro.tokens_observed);
  const wouldCutTokens = localScanCounter(retro.would_cut_tokens);
  const configPrefixTokensPerTurn = localScanCounter(retro.config_prefix_tokens_per_turn ?? 0);
  const wouldCutStreamTokens = retro.would_cut_stream_tokens === undefined
    ? undefined
    : localScanCounter(retro.would_cut_stream_tokens);
  if (
    retro.basis !== "inferred"
    || retro.window_days !== 30
    || sessionsTotal === null
    || sessionsScanned === null
    || turnsObserved === null
    || tokensObserved === null
    || wouldCutTokens === null
    || configPrefixTokensPerTurn === null
    || wouldCutStreamTokens === null
    || !Number.isFinite(scannedAt.valueOf())
    || !Array.isArray(retro.families)
    || sessionsScanned > sessionsTotal
    || wouldCutTokens > tokensObserved
    || (wouldCutStreamTokens !== undefined && wouldCutStreamTokens > tokensObserved)
    || (sessionsScanned === 0 && (turnsObserved !== 0 || tokensObserved !== 0 || wouldCutTokens !== 0 || (wouldCutStreamTokens ?? 0) !== 0))
    || (retro.tokens_observed_source !== "session_usage" && retro.tokens_observed_source !== "o200k_estimate")
  ) return null;

  const familyTokens = new Map<LocalScanFamilyUpload["id"], number>();
  for (const family of retro.families) {
    if (family.id !== "tool_outputs" && family.id !== "repeated_blocks") return null;
    const tokens = localScanCounter(family.tokens);
    if (tokens === null || familyTokens.has(family.id)) return null;
    familyTokens.set(family.id, tokens);
  }
  const families = (["tool_outputs", "repeated_blocks"] as const)
    .filter((id) => familyTokens.has(id))
    .map((id) => ({ id, tokens: familyTokens.get(id)! }));
  const familyTotal = families.reduce((sum, family) => sum + family.tokens, 0);
  if (!Number.isSafeInteger(familyTotal) || familyTotal !== wouldCutTokens) return null;

  const payload: LocalScanUpload = {
    schema: "caveman.local_scan.v1",
    source: "local_scan",
    basis: "inferred",
    scanned_at: scannedAt.toISOString(),
    window_days: 30,
    sessions_total: sessionsTotal,
    sessions_scanned: sessionsScanned,
    turns_observed: turnsObserved,
    tokens_observed: tokensObserved,
    tokens_observed_source: retro.tokens_observed_source,
    would_cut_tokens: wouldCutTokens,
    families,
    config_prefix_tokens_per_turn: configPrefixTokensPerTurn,
    engine_used: retro.engine_used === true,
    time_boxed: retro.time_boxed === true,
  };
  if (wouldCutStreamTokens !== undefined) payload.would_cut_stream_tokens = wouldCutStreamTokens;
  return payload;
}

function persistPendingLocalScan(retro: LearnRetro, scannedAt = new Date()): boolean {
  const payload = localScanUploadFromRetro(retro, scannedAt);
  if (!payload) return false;
  const state: LocalScanState = { schema: "caveman.local_scan.pending.v1", payload };
  try {
    mkdirSync(dirname(localScanStatePath()), { recursive: true });
    writeFileSync(localScanStatePath(), JSON.stringify(state, null, 2) + "\n", { mode: 0o600 });
    return true;
  } catch {
    return false;
  }
}

function normalizeStoredLocalScanPayload(value: unknown): LocalScanUpload | null {
  if (!value || typeof value !== "object" || Array.isArray(value)) return null;
  const raw = value as Record<string, unknown>;
  if (
    raw.schema !== "caveman.local_scan.v1"
    || raw.source !== "local_scan"
    || raw.basis !== "inferred"
    || raw.window_days !== 30
    || typeof raw.scanned_at !== "string"
    || !Number.isFinite(Date.parse(raw.scanned_at))
    || typeof raw.engine_used !== "boolean"
    || typeof raw.time_boxed !== "boolean"
    || !Array.isArray(raw.families)
  ) return null;
  const retro: LearnRetro = {
    basis: "inferred",
    window_days: 30,
    sessions_total: raw.sessions_total as number,
    sessions_scanned: raw.sessions_scanned as number,
    turns_observed: raw.turns_observed as number,
    tokens_observed: raw.tokens_observed as number,
    tokens_observed_source: raw.tokens_observed_source as LearnRetro["tokens_observed_source"],
    would_cut_tokens: raw.would_cut_tokens as number,
    ...(raw.would_cut_stream_tokens === undefined
      ? {}
      : { would_cut_stream_tokens: raw.would_cut_stream_tokens as number }),
    families: raw.families.map((family) => {
      const item = family && typeof family === "object" && !Array.isArray(family)
        ? family as Record<string, unknown>
        : {};
      return { id: item.id as string, label: "", tokens: item.tokens as number };
    }),
    config_prefix_tokens_per_turn: raw.config_prefix_tokens_per_turn as number,
    engine_used: raw.engine_used,
    time_boxed: raw.time_boxed,
  };
  return localScanUploadFromRetro(retro, new Date(raw.scanned_at));
}

function readLocalScanState(): LocalScanState | null {
  try {
    const raw = JSON.parse(readFileSync(localScanStatePath(), "utf8")) as Record<string, unknown>;
    if (raw?.schema !== "caveman.local_scan.pending.v1") return null;
    const payload = normalizeStoredLocalScanPayload(raw.payload);
    if (!payload) return null;
    const deliveredRaw = raw.delivered && typeof raw.delivered === "object" && !Array.isArray(raw.delivered)
      ? raw.delivered as Record<string, unknown>
      : null;
    const delivered = deliveredRaw && typeof deliveredRaw.import_id === "string" && deliveredRaw.import_id
      ? {
          import_id: deliveredRaw.import_id,
          project_id: typeof deliveredRaw.project_id === "string" ? deliveredRaw.project_id : "",
          organization_id: typeof deliveredRaw.organization_id === "string" ? deliveredRaw.organization_id : "",
          base_url: typeof deliveredRaw.base_url === "string" ? deliveredRaw.base_url : "",
          delivered_at: typeof deliveredRaw.delivered_at === "string" ? deliveredRaw.delivered_at : "",
        }
      : undefined;
    return { schema: "caveman.local_scan.pending.v1", payload, ...(delivered ? { delivered } : {}) };
  } catch {
    return null;
  }
}

type SqliteCtor = (typeof import("node:sqlite"))["DatabaseSync"];
type SqliteDb = InstanceType<SqliteCtor>;

// dbFingerprint returns a stable identity for THIS local spend DB so the sync
// high-water mark is scoped to the specific database it was measured against.
// If the DB is deleted and recreated (e.g. `make reset-local` wipes ~/.caveman),
// SQLite rowids restart at 1; without a per-DB fingerprint the stale watermark
// in ~/.caveman-cloud/sync.json would make `WHERE id > watermark` skip every new
// span forever. We persist a random UUID in a CLI-owned marker table on the
// first sync — durable across file moves and deterministic on later runs. When
// the DB is opened read-only on a read-only filesystem or a writer lock cannot
// be taken within the busy timeout, we fall back to the file's inode + birth
// time, which also changes when the file is recreated.
function dbFingerprint(readDb: SqliteDb, dbPath: string, ctor: SqliteCtor): string {
  // Steady state: the marker already exists — read it over the read-only handle.
  try {
    const row = readDb.prepare("SELECT value FROM cave_cli_meta WHERE key = 'sync_db_uuid'").get() as
      | { value?: unknown }
      | undefined;
    if (row && typeof row.value === "string" && row.value) return row.value;
  } catch {
    // cave_cli_meta does not exist yet on this DB — create it below.
  }
  // First sync for this DB (or first after a reset): create the marker table and
  // record a fresh UUID via a short writable connection. INSERT ... DO NOTHING +
  // re-SELECT is race-safe if two syncs run concurrently.
  try {
    const w = new ctor(dbPath);
    try {
      w.exec("PRAGMA busy_timeout = 3000");
      w.exec("CREATE TABLE IF NOT EXISTS cave_cli_meta (key TEXT PRIMARY KEY, value TEXT NOT NULL)");
      w.prepare("INSERT INTO cave_cli_meta (key, value) VALUES ('sync_db_uuid', ?) ON CONFLICT(key) DO NOTHING").run(
        randomUUID(),
      );
      const row = w.prepare("SELECT value FROM cave_cli_meta WHERE key = 'sync_db_uuid'").get() as
        | { value?: unknown }
        | undefined;
      if (row && typeof row.value === "string" && row.value) return row.value;
    } finally {
      w.close();
    }
  } catch {
    // Read-only filesystem or an unacquirable write lock — fall back to the file
    // identity, which still changes when the DB file is recreated.
  }
  const st = statSync(dbPath);
  return `ino:${st.ino}:${Math.trunc(st.birthtimeMs || st.ctimeMs)}`;
}

// The watermark is keyed by control-api base URL + organization + a per-DB
// fingerprint, so switching orgs/servers never crosses scopes AND a recreated
// local DB (rowids restart at 1) gets a fresh watermark instead of silently
// skipping every new span.
function syncWatermarkKey(cfg: Config, dbFingerprint: string): string {
  return `${cfg.baseURL}|${cfg.organizationId ?? ""}|${dbFingerprint}`;
}

function readSyncWatermark(key: string): number {
  try {
    const parsed = JSON.parse(readFileSync(syncStatePath(), "utf8")) as { watermarks?: Record<string, unknown> };
    const v = parsed.watermarks?.[key];
    return typeof v === "number" && Number.isFinite(v) && v > 0 ? v : 0;
  } catch {
    return 0;
  }
}

function hasSyncWatermark(key: string): boolean {
  try {
    const parsed = JSON.parse(readFileSync(syncStatePath(), "utf8")) as { watermarks?: Record<string, unknown> };
    return Object.prototype.hasOwnProperty.call(parsed.watermarks ?? {}, key);
  } catch {
    return false;
  }
}

function writeSyncWatermark(key: string, id: number) {
  let state: { watermarks: Record<string, number> } = { watermarks: {} };
  try {
    const raw = JSON.parse(readFileSync(syncStatePath(), "utf8"));
    if (raw && typeof raw === "object" && raw.watermarks && typeof raw.watermarks === "object") state = raw;
  } catch {
    // fresh state file
  }
  state.watermarks[key] = id;
  mkdirSync(dirname(syncStatePath()), { recursive: true });
  writeFileSync(syncStatePath(), JSON.stringify(state, null, 2) + "\n", { mode: 0o600 });
}

// deriveDashboardUrl maps the control-api base URL to the dashboard costs page
// for the two shapes Caveman ships (local docker web :3000; hosted api.<domain>
// → apex, which serves the dashboard). Anything else returns "" — no guessing.
function deriveDashboardUrl(baseURL: string): string {
  try {
    const u = new URL(baseURL);
    if (u.hostname === "localhost" || u.hostname === "127.0.0.1") return `${u.protocol}//${u.hostname}:3000/costs`;
    if (u.hostname.startsWith("api.")) return `${u.protocol}//${u.hostname.slice(4)}/costs`;
  } catch {
    // not a URL we can map
  }
  return "";
}

function deriveLocalScanDashboardUrl(baseURL: string): string {
  const costs = deriveDashboardUrl(baseURL);
  return costs.endsWith("/costs") ? `${costs.slice(0, -"/costs".length)}/imports-exports` : "";
}

async function syncPendingLocalScan(cfg: Config): Promise<LocalScanSyncOutcome> {
  const state = readLocalScanState();
  if (!state) return { kind: "no_snapshot" };
  if (state.delivered) return { kind: "already_synced" };
  const project = cfg.projectId ? `&project_id=${encodeURIComponent(cfg.projectId)}` : "";
  const response = await fetch(`${cfg.baseURL}/api/v1/imports?format=local-scan${project}`, {
    method: "POST",
    headers: {
      authorization: `Bearer ${cfg.token}`,
      "content-type": "application/json",
      "x-cave-csrf": "cli",
    },
    body: JSON.stringify(state.payload),
  });
  const result = (await response.json().catch(() => ({}))) as {
    id?: string;
    project_id?: string;
    status?: string;
    source?: string;
    basis?: string;
    error?: { message?: string };
  };
  if (
    !response.ok
    || result.status !== "completed"
    || result.source !== "local_scan"
    || result.basis !== "inferred"
    || typeof result.id !== "string"
    || !result.id
  ) {
    throw new Error(result.error?.message ?? `local scan sync failed (${response.status})`);
  }
  const delivered: LocalScanState = {
    ...state,
    delivered: {
      import_id: result.id,
      project_id: typeof result.project_id === "string" ? result.project_id : cfg.projectId ?? "",
      organization_id: cfg.organizationId ?? "",
      base_url: cfg.baseURL,
      delivered_at: new Date().toISOString(),
    },
  };
  mkdirSync(dirname(localScanStatePath()), { recursive: true });
  writeFileSync(localScanStatePath(), JSON.stringify(delivered, null, 2) + "\n", { mode: 0o600 });
  return { kind: "synced", importId: result.id, dashboard: deriveLocalScanDashboardUrl(cfg.baseURL) };
}

function localScanSyncLine(out: Extract<LocalScanSyncOutcome, { kind: "synced" }>): string {
  const destination = out.dashboard ? ` → ${out.dashboard}` : "";
  return `synced 30-day local scan · inferred token summary · separate from gateway spend and verified savings${destination}`;
}

// syncRequestSpan converts one local `requests` row into a caveman-jsonl span
// line (public/shared/platform/importers Span shape; timestamps are already in
// the ClickHouse layout because the proxy writes them that way). The basis and
// per-row inferred savings ride in attributes — the spans schema has no savings
// column, and imported rows must never look like verified ledger entries.
function syncRequestSpan(r: Record<string, unknown>, directives: string[] = []): string {
  const num = (v: unknown) => (typeof v === "bigint" ? Number(v) : typeof v === "number" && Number.isFinite(v) ? v : 0);
  const str = (v: unknown) => (typeof v === "string" ? v : "");
  const attributes: Record<string, string> = {
    "cave.basis": str(r.basis) || "inferred",
    "cave.savings_usd": String(num(r.savings_usd)),
    "cave.sync_source": "caveman-cli",
  };
  // Wrap-directive session evidence (AUTOPILOT_SPEC §8.4): sessions synced
  // while a directive is installed carry its id as a label so before/after
  // token usage is attributable. TOKENS ONLY, basis stays 'inferred' — the
  // label never adds a dollar field and never upgrades a basis.
  if (directives.length > 0) attributes["cave.directives"] = directives.join(",");
  if (str(r.token_usage_basis)) attributes["cave.token_usage_basis"] = str(r.token_usage_basis);
  if (str(r.auth_mode)) attributes["cave.auth_mode"] = str(r.auth_mode);
  if (str(r.runtime_mode)) attributes["cave.runtime_mode"] = str(r.runtime_mode);
  if (str(r.optimization_ids)) attributes["cave.optimization_ids"] = str(r.optimization_ids);
  // caveman.spans has no first-class cache-write column. Preserve the provider
  // counter in the supported metadata map so imported reports can apply the same
  // cache-write-heavy headline refusal as the live local summary.
  attributes["cave.cache_creation_input_tokens"] = String(Math.max(0, num(r.cache_creation_input_tokens)));
  if (num(r.compression_tokens_before) > 0) {
    attributes["cave.compression_tokens_before"] = String(num(r.compression_tokens_before));
    attributes["cave.compression_tokens_after"] = String(num(r.compression_tokens_after));
    attributes["cave.compression_token_count_basis"] = str(r.compression_token_count_basis) || "unavailable";
  }
  return JSON.stringify({
    timestamp: str(r.ts),
    trace_id: str(r.trace_id) || str(r.request_id),
    span_id: str(r.request_id),
    span_type: "chat",
    span_name: `chat ${str(r.provider) || "unknown"}`,
    status: str(r.error_code) || num(r.status_code) >= 400 ? "error" : "ok",
    duration_ms: Math.max(0, num(r.latency_ms)),
    provider: str(r.provider),
    model: str(r.model),
    agent_slug: str(r.agent_slug),
    input_bytes: Math.max(0, num(r.request_bytes)),
    output_bytes: Math.max(0, num(r.response_bytes)),
    input_tokens: Math.max(0, num(r.input_tokens)),
    output_tokens: Math.max(0, num(r.output_tokens)),
    cached_input_tokens: Math.max(0, num(r.cached_input_tokens)),
    total_cost_usd: num(r.total_cost_usd),
    attributes,
  });
}

// syncLocalSavings does one idempotent sync pass. It throws on transport or
// server errors (the watermark is NOT advanced then) and returns an outcome
// the callers turn into one honest line.
async function syncLocalSavings(cfg: Config): Promise<SyncOutcome> {
  const dbPath = localSpendDbPath();
  try {
    statSync(dbPath);
  } catch {
    return { kind: "no_store", dbPath };
  }
  // node:sqlite is built into Node ≥22.13 — still zero runtime deps. Imported
  // lazily so only sync paths ever load it (or pay its experimental warning).
  let DatabaseSync: (typeof import("node:sqlite"))["DatabaseSync"];
  try {
    ({ DatabaseSync } = await import("node:sqlite"));
  } catch {
    throw new Error(
      `caveman sync requires Node >= 22.13 (it reads the local spend store via node:sqlite); you are running Node ${process.versions.node}`,
    );
  }
  const db = new DatabaseSync(dbPath, { readOnly: true });
  let rows: Record<string, unknown>[];
  const key = syncWatermarkKey(cfg, dbFingerprint(db, dbPath, DatabaseSync));
  const firstSync = !hasSyncWatermark(key);
  const since = readSyncWatermark(key);
  try {
    const columns = new Set(
      (db.prepare("PRAGMA table_info(requests)").all() as Record<string, unknown>[])
        .map((row) => typeof row.name === "string" ? row.name : "")
        .filter(Boolean),
    );
    const optional = (name: string, fallback: string) => columns.has(name) ? name : `${fallback} AS ${name}`;
    rows = db
      .prepare(
        `SELECT id, ts, request_id, trace_id, agent_slug, provider, model,
                status_code, error_code, latency_ms, request_bytes, response_bytes,
                input_tokens, output_tokens, cached_input_tokens,
                ${optional("cache_creation_input_tokens", "0")}, total_cost_usd,
                savings_usd, basis, ${optional("token_usage_basis", "'unavailable'")},
                ${optional("auth_mode", "'unknown'")}, runtime_mode, optimization_ids,
                compression_tokens_before, compression_tokens_after,
                ${optional("compression_token_count_basis", "'unavailable'")}
           FROM requests WHERE id > ? ORDER BY id`,
      )
      .all(since) as Record<string, unknown>[];
  } finally {
    db.close();
  }
  if (rows.length === 0) return { kind: "empty" };

  // Per-row directive labels (review M12): a row synced from BEFORE a
  // directive's install carries no label — backlog sessions are not evidence
  // about a directive that did not exist yet. An unparseable row timestamp
  // labels nothing (fail closed).
  const directiveInstalls = installedDirectivesWithTimes();
  const rowDirectives = (r: Record<string, unknown>): string[] => {
    if (directiveInstalls.length === 0) return [];
    const ts = typeof r.ts === "string" ? r.ts : "";
    // The proxy writes ClickHouse-layout UTC timestamps ("YYYY-MM-DD HH:MM:SS.mmm").
    const ms = Date.parse(ts.includes("T") ? ts : ts.replace(" ", "T") + "Z");
    if (!Number.isFinite(ms)) return [];
    return directiveInstalls.filter((d) => ms >= d.installedAtMs).map((d) => d.id);
  };
  const body = rows.map((r) => syncRequestSpan(r, rowDirectives(r))).join("\n") + "\n";
  const response = await fetch(`${cfg.baseURL}/api/v1/imports?format=caveman-jsonl`, {
    method: "POST",
    headers: {
      authorization: `Bearer ${cfg.token}`,
      "content-type": "application/octet-stream",
      "x-cave-csrf": "cli",
    },
    body,
  });
  const result = (await response.json().catch(() => ({}))) as { status?: string; error?: { message?: string } };
  if (!response.ok || result.status !== "completed") {
    throw new Error(result.error?.message ?? `sync import failed (${response.status})`);
  }

  const num = (v: unknown) => (typeof v === "bigint" ? Number(v) : typeof v === "number" && Number.isFinite(v) ? v : 0);
  const maxId = rows.reduce((m, r) => Math.max(m, num(r.id)), since);
  writeSyncWatermark(key, maxId);
  const tokensSaved = rows.reduce((sum, r) => sum + Math.max(0, num(r.compression_tokens_before) - num(r.compression_tokens_after)), 0);
  const tokenBases = new Set(rows.map((r) => (typeof r.compression_token_count_basis === "string" ? r.compression_token_count_basis : "")).filter(Boolean));
  const tokenCountBasis = tokenBases.size === 0 ? "unavailable" : tokenBases.size === 1 ? [...tokenBases][0] ?? "unavailable" : "mixed";
  const cachedInputTokens = rows.reduce((sum, r) => sum + Math.max(0, num(r.cached_input_tokens)), 0);
  const cacheCreationInputTokens = rows.reduce((sum, r) => sum + Math.max(0, num(r.cache_creation_input_tokens)), 0);
  const headlineCompressionRefused = cacheCreationInputTokens > cachedInputTokens;
  const savingsUSD = rows.reduce((sum, r) => sum + num(r.savings_usd), 0);
  return {
    kind: "synced",
    spans: rows.length,
    tokensSaved,
    tokenCountBasis,
    cachedInputTokens,
    cacheCreationInputTokens,
    headlineCompressionRefused,
    savingsUSD,
    dashboard: deriveDashboardUrl(cfg.baseURL),
    firstSync,
  };
}

function syncSavingsLine(out: SyncedOutcome, local = false): string {
  const destination = out.dashboard ? ` → ${out.dashboard}` : "";
  const subject = `synced ${out.spans}${local ? " local" : ""} spans`;
  if (out.headlineCompressionRefused) {
    return `${subject} · compression headline refused: cache writes ${out.cacheCreationInputTokens} > cache reads ${out.cachedInputTokens} (inferred; no savings headline)${destination}`;
  }
  return `${subject} · ${out.tokensSaved} estimated tokens saved (inferred; counter basis ${out.tokenCountBasis})${destination}`;
}

// syncLocalPracticeFindings moves only stable ids + non-negative token rates
// from the current learn snapshot. Titles, suggestions, evidence, file paths,
// and payload text never leave the machine. Empty snapshots are sent so stale
// rows for this user are removed server-side.
async function syncLocalPracticeFindings(cfg: Config): Promise<PracticeSyncOutcome> {
  const snapshotPath = join(cavemanHome(), "reports", "caveman-learn.json");
  let raw: Record<string, unknown>;
  try {
    raw = JSON.parse(readFileSync(snapshotPath, "utf8")) as Record<string, unknown>;
  } catch (error) {
    if ((error as NodeJS.ErrnoException).code === "ENOENT") return { kind: "no_snapshot" };
    throw new Error(`local learn snapshot is unreadable at ${snapshotPath}`);
  }

  if (!Array.isArray(raw.sinks)) {
    throw new Error(`local learn snapshot has no sinks array at ${snapshotPath}`);
  }
  const observed = typeof raw.generated_at === "string" ? new Date(raw.generated_at) : null;
  if (!observed || Number.isNaN(observed.valueOf())) {
    throw new Error(`local learn snapshot has no valid generated_at at ${snapshotPath}`);
  }
  const sessions = typeof raw.sessions_scanned === "number" && Number.isFinite(raw.sessions_scanned)
    ? Math.max(0, Math.trunc(raw.sessions_scanned))
    : 0;
  const counter = (value: unknown) =>
    typeof value === "number" && Number.isFinite(value) ? Math.max(0, Math.trunc(value)) : 0;

  const findings = raw.sinks
    .filter((sink): sink is Record<string, unknown> =>
      !!sink && typeof sink === "object" && !Array.isArray(sink))
    .filter((sink) => typeof sink.practice_id === "string" && sink.practice_id.length > 0)
    .map((sink) => ({
      sink_id: typeof sink.sink_id === "string" ? sink.sink_id : "",
      practice_id: sink.practice_id as string,
      basis: "inferred",
      tokens_per_turn: counter(sink.tokens_per_turn),
      tokens_per_day_rate: counter(sink.tokens_per_day_rate),
      sessions_scanned: sessions,
      observed_at: observed.toISOString(),
    }));

  const project = cfg.projectId ? `?project_id=${encodeURIComponent(cfg.projectId)}` : "";
  const response = await fetch(`${cfg.baseURL}/api/v1/practice-findings${project}`, {
    method: "POST",
    headers: {
      authorization: `Bearer ${cfg.token}`,
      "content-type": "application/json",
      "x-cave-csrf": "cli",
    },
    body: JSON.stringify({ findings }),
  });
  const result = (await response.json().catch(() => ({}))) as {
    status?: string;
    row_count?: number;
    error?: { message?: string };
  };
  if (!response.ok || result.status !== "completed" || result.row_count !== findings.length) {
    throw new Error(result.error?.message ?? `practice findings sync failed (${response.status})`);
  }
  return { kind: "synced", findings: findings.length };
}

// sync is the first-class verb: clear error when logged out, honest no-op when
// there is nothing to send, one plain line when spans were uploaded.
async function sync() {
  const cfg = await config();
  requireAuth(cfg);
  // These are independent evidence lanes. One corrupt/busy local store or one
  // rejected practice POST must not starve the pending first-run aggregate;
  // all lanes run, successful ones settle, then explicit sync reports any
  // partial failure non-zero so the operator can retry it.
  const [savingsLane, practicesLane, localScanLane] = await Promise.allSettled([
    syncLocalSavings(cfg),
    syncLocalPracticeFindings(cfg),
    syncPendingLocalScan(cfg),
  ] as const);
  const failures: string[] = [];

  if (savingsLane.status === "fulfilled") {
    const out = savingsLane.value;
    if (out.kind === "no_store") {
      console.log(`nothing to sync — no local spend store at ${out.dbPath} (run \`caveman wrap <agent>\` to record local inferred savings first)`);
    } else if (out.kind === "empty") {
      console.log("nothing new to sync — local inferred savings are already up to date");
    } else {
      if (out.firstSync) console.log(SYNC_DISCLOSURE);
      console.log(syncSavingsLine(out));
    }
  } else {
    failures.push(`local spans: ${syncLaneError(savingsLane.reason)}`);
  }
  if (practicesLane.status === "fulfilled") {
    if (practicesLane.value.kind === "synced") {
      console.log(`synced ${practicesLane.value.findings} local practice findings · basis: inferred (tokens only; no payload evidence)`);
    }
  } else {
    failures.push(`practice findings: ${syncLaneError(practicesLane.reason)}`);
  }
  if (localScanLane.status === "fulfilled") {
    if (localScanLane.value.kind === "synced") console.log(localScanSyncLine(localScanLane.value));
  } else {
    failures.push(`local scan: ${syncLaneError(localScanLane.reason)}`);
  }
  if (failures.length > 0) throw new Error(`sync incomplete — ${failures.join("; ")}`);
}

function syncLaneError(reason: unknown): string {
  return reason instanceof Error ? reason.message : String(reason);
}

// syncAfterLogin runs the same sync once right after a successful login so the
// dashboard immediately shows what the local proxy already measured. Strictly
// best-effort: a sync failure never fails the login.
async function syncAfterLogin() {
  try {
    const cfg = await config();
    if (!cfg.token) return;
    const [savingsLane, practicesLane, localScanLane] = await Promise.allSettled([
      syncLocalSavings(cfg),
      syncLocalPracticeFindings(cfg),
      syncPendingLocalScan(cfg),
    ] as const);
    if (savingsLane.status === "fulfilled") {
      if (savingsLane.value.kind === "synced") {
        console.error(`  ${mark("ok")} ${syncSavingsLine(savingsLane.value, true)}`);
      } else {
        console.error(dim("  no local spans to sync yet — `caveman wrap <agent>` records inferred savings locally; `caveman sync` uploads them"));
      }
    } else {
      console.error(`  ${mark("warn")} local spans sync skipped: ${syncLaneError(savingsLane.reason)} — run \`${invokedAs()} sync\` to retry`);
    }
    if (practicesLane.status === "fulfilled") {
      if (practicesLane.value.kind === "synced") {
        console.error(`  ${mark("ok")} synced ${practicesLane.value.findings} local practice findings · inferred, tokens only`);
      }
    } else {
      console.error(`  ${mark("warn")} local practice sync skipped: ${syncLaneError(practicesLane.reason)} — run \`${invokedAs()} sync\` to retry`);
    }
    if (localScanLane.status === "fulfilled") {
      if (localScanLane.value.kind === "synced") console.error(`  ${mark("ok")} ${localScanSyncLine(localScanLane.value)}`);
    } else {
      console.error(`  ${mark("warn")} local scan sync skipped: ${syncLaneError(localScanLane.reason)} — run \`${invokedAs()} sync\` to retry`);
    }
  } catch (error) {
    console.error(`  ${mark("warn")} local sync setup skipped: ${(error as Error).message} — run \`caveman sync\` to retry`);
  }
}

// syncAfterWrap is the end-of-session hook: after a logged-in LOCAL wrapped
// session, push the newly recorded spans. Quiet best-effort — silent when not
// logged in or nothing to send, and never changes the wrapped exit code.
// Managed-mode sessions skip it: that traffic is already measured in the cloud.
async function syncAfterWrap() {
  if (wrapExternalWritesDisabled()) return;
  try {
    const cfg = await config();
    if (!cfg.token) return;
    if (wrapMode(gatewayURL()) !== "local") return;
    const out = await syncLocalSavings(cfg);
    if (out.kind === "synced") {
      process.stderr.write(dim(`→ ${syncSavingsLine(out, true)}`) + "\n");
    }
  } catch (error) {
    process.stderr.write(dim(`→ local savings sync failed (${(error as Error).message}) — run \`caveman sync\` to retry`) + "\n");
  }
}

// Benchmark and air-gapped runs need live local compression without mutating
// account state or uploading synthetic spans. This switch blocks only wrap's
// background entitlement refresh and post-run sync; cached entitlement gating,
// proxy traffic, and local evidence remain unchanged.
export function wrapExternalWritesDisabled(env: NodeJS.ProcessEnv = process.env): boolean {
  return env.CAVEMAN_OFFLINE === "1";
}

// ── caveman mcp install ──────────────────────────────────────────────────────
// Installs the caveman MCP server (its caveman_retrieve tool) into an agent's own
// config so the agent can recover proxy-elided detail itself. This is the
// prerequisite that lets default `caveman wrap` compress STREAMING requests:
// on streams the proxy cannot run its server-side retrieve loop, so it leans on
// the agent's MCP tool (sharing the same ~/.caveman/ccr.db store) — exactly how
// Headroom does it. Install writes a marker; wrap reads it (mcpInstalled) and only
// then signals the proxy (CAVEMAN_RECOVERY=mcp) that recovery is available.

function cavemanHome(): string {
  return process.env.CAVEMAN_HOME ?? join(homedir(), ".caveman");
}

// ensureCavemanHome creates ~/.caveman itself with owner-only perms and repairs
// an existing permissive mode. The proxy refuses CCR (sqlite parent check) when
// this directory is group/world writable, and recursive mkdir with a mode only
// applies it to directories it creates — an earlier no-mode caller (login, mcp
// install) would otherwise have already created it 0775 under umask 002.
function ensureCavemanHome(): string {
  const home = cavemanHome();
  mkdirSync(home, { recursive: true, mode: 0o700 });
  try { chmodSync(home, 0o700); } catch { /* not ours / Windows */ }
  return home;
}

// cavemanBin resolves one of caveman's own Go binaries (proxy/engine/mcp/browse):
// explicit env override first, then PATH, then ~/.caveman/bin — the directory the
// install script and the missing-binary panels tell users to build into, so
// following those instructions works without also editing PATH. Falls back to the
// bare name so callers' existing missing-binary handling still triggers.
function cavemanBin(name: string, envVar: string): string {
  const explicit = process.env[envVar];
  if (explicit) return explicit;
  const onPath = which(name);
  if (onPath) return onPath;
  const local = join(cavemanHome(), "bin", binaryInstallFilename(name));
  if (isExecutable(local)) return local;
  return name;
}
function mcpServerMarkerPath(agentId: string, serverName: string): string {
  return join(cavemanHome(), "mcp", serverName === "caveman" ? `${agentId}.json` : `${agentId}.${serverName}.json`);
}
function mcpMarkerPath(agentId: string): string {
  return mcpServerMarkerPath(agentId, "caveman");
}
function mcpServerInstalled(agentId: string, serverName: string): boolean {
  try {
    return statSync(mcpServerMarkerPath(agentId, serverName)).isFile();
  } catch {
    return false;
  }
}
function mcpInstalled(agentId: string): boolean {
  return mcpServerInstalled(agentId, "caveman");
}

// configFileOverlayHasMcp asks whether the PROFILE would inject this MCP server
// through its config-file overlay. It only understands the `config-file` arm: a
// `config-env-content` profile that ever grows an mcp block would read false here
// AND be missed by withoutCavemanMcpServer (which runs on the config-file arm
// only) — a latent double loss, silent in both directions. Extend both together
// if such a profile is ever added.
function configFileOverlayHasMcp(agent: AgentProfile, serverName: string): boolean {
  const inj = agent.injection;
  if (inj.method !== "config-file") return false;
  const overlays = [inj.config_overlay.local, inj.config_overlay.managed];
  return overlays.some((overlay) => !!getObject(overlay, ["mcp", "servers", serverName]));
}
function configFileOverlayHasCavemanMcp(agent: AgentProfile): boolean {
  return configFileOverlayHasMcp(agent, "caveman");
}

function probeMcpBinary(): { binary: string; probe: VersionedBinaryProbe } | null {
  const binary = resolveGoBin("caveman-mcp", "CAVEMAN_MCP_BIN");
  if (!binary) return null;
  return { binary, probe: probeVersionedBinary(binary, "mcp_recovery") };
}

function queueStaleMcpBinary(probe: VersionedBinaryProbe): void {
  queueRunOffState(OFF_STATES.staleBinary("caveman-mcp", probe.version, cliVersion()));
}

// wrapMcpRecoveryAvailable answers ONE question — does this agent actually have
// a working caveman_retrieve tool right now — and it answers it from evidence on
// disk, never from what wrap intended to do. That separation is what makes
// execute.mcp=marker-only honest: wrap stops installing, so this function is the
// only thing left deciding CAVEMAN_RECOVERY, and it says "mcp" if and only if the
// tool is really registered for this agent. There is no execute.mcp value that
// can make it claim recovery that isn't there, and none that suppresses recovery
// that is — the proxy would otherwise either elide bytes nothing can expand, or
// pass through while the agent still pays for tools it isn't allowed to use.
// (honesty rule: no-placeholder)
function wrapMcpRecoveryAvailable(agent: AgentProfile | undefined, opts: WrapOptions): boolean {
  if (!wrapRecoveryEligible(opts) || !agent) return false;
  // Two independent sources of a real caveman_retrieve: a marker from
  // `caveman tools mcp install`, or the profile's own config-file overlay. The
  // second only counts under `auto`, because that is exactly when buildWrapEnv
  // still merges it — under marker-only/false the overlay is stripped, so
  // counting it here would tell the proxy about a tool this launch removed.
  const overlayInjects = opts.mcpMode === "auto" && configFileOverlayHasCavemanMcp(agent);
  if (!mcpInstalled(agent.id) && !overlayInjects) return false;
  const compatibility = probeMcpBinary();
  if (!compatibility) return false;
  if (!compatibility.probe.current) {
    queueStaleMcpBinary(compatibility.probe);
    return false;
  }
  return true;
}

// Bare start cannot bind a machine-level MCP marker or inherited opt-in to the
// client issuing a later request. Recovery therefore stays off. Agent-scoped wrap
// performs the only supported compatibility check.
function startMcpRecoveryAvailable(): boolean {
  return false;
}

// resolveMcpCommand decides how to launch the caveman MCP server, in order:
// CAVEMAN_MCP_BIN, `caveman-mcp` on PATH or ~/.caveman/bin, else `npx -y
// caveman-mcp`. The returned argv is what gets written into each agent's MCP config.
function resolveMcpCommand(): { command: string; args: string[] } {
  const bin = cavemanBin("caveman-mcp", "CAVEMAN_MCP_BIN");
  if (bin !== "caveman-mcp" || which(bin)) return { command: bin, args: [] };
  const npx = which("npx");
  if (npx) return { command: npx, args: ["-y", "caveman-mcp"] };
  return { command: "caveman-mcp", args: [] };
}

function resolveCloudMcpCommand(): { command: string; args: string[] } {
  const entry = process.argv[1];
  if (!entry) {
    console.error("caveman mcp: cannot resolve CLI entrypoint for caveman-cloud server");
    process.exit(1);
  }
  return { command: process.execPath, args: [realpathSync(entry), "cloud", "mcp-serve"] };
}

// resolveDelegateMcpCommand locates the dependency-free caveman-delegate stdio
// server (CAVEMAN_DELEGATE_MCP override, else the copy shipped alongside the
// CLI). Null when the script is missing — callers must not register a dead entry.
function resolveDelegateMcpCommand(): { command: string; args: string[] } | null {
  const candidates = [
    process.env.CAVEMAN_DELEGATE_MCP || "",
    join(dirname(fileURLToPath(import.meta.url)), "caveman-delegate-mcp.mjs"),
  ].filter(Boolean);
  const script = candidates.find((c) => existsSync(c));
  return script ? { command: process.execPath, args: [script] } : null;
}

const HERMES_MCP_BEGIN = "# >>> caveman:mcp";
const HERMES_MCP_END = "# <<< caveman:mcp";
const HERMES_PLUGIN_ENABLE_BEGIN = "# >>> caveman:hermes-plugin-enable";
const HERMES_PLUGIN_ENABLE_END = "# <<< caveman:hermes-plugin-enable";
const HERMES_PLUGIN_NAME = "caveman_shrink";

function hermesHome(): string {
  return expandTilde(process.env.HERMES_HOME || "~/.hermes");
}

function hermesConfigPath(): string {
  return join(hermesHome(), "config.yaml");
}

function yamlQuote(s: string): string {
  return JSON.stringify(s);
}

function yamlInlineArray(items: string[]): string {
  return `[${items.map(yamlQuote).join(", ")}]`;
}

function escapeRegExp(s: string): string {
  return s.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
}

function yamlLines(text: string): string[] {
  if (!text) return [];
  const lines = text.split(/\r?\n/);
  if (lines[lines.length - 1] === "") lines.pop();
  return lines;
}

function yamlText(lines: string[]): string {
  return lines.length ? `${lines.join("\n")}\n` : "";
}

function stripMarkedBlock(text: string, begin: string, end: string): { text: string; removed: boolean } {
  const kept: string[] = [];
  let skipping = false;
  let removed = false;
  for (const line of yamlLines(text)) {
    if (line.includes(begin)) {
      skipping = true;
      removed = true;
      continue;
    }
    if (skipping) {
      if (line.includes(end)) skipping = false;
      continue;
    }
    kept.push(line);
  }
  return { text: yamlText(kept), removed };
}

function topLevelSection(lines: string[], key: string): { start: number; end: number } | undefined {
  const start = lines.findIndex((line) => new RegExp(`^${key}:\\s*(?:#.*)?$`).test(line));
  if (start < 0) return undefined;
  let end = lines.length;
  for (let i = start + 1; i < lines.length; i++) {
    if (/^[A-Za-z0-9_-]+:\s*/.test(lines[i]!)) {
      end = i;
      break;
    }
  }
  return { start, end };
}

function sectionHasChildKey(lines: string[], section: { start: number; end: number }, key: string): boolean {
  const re = new RegExp(`^\\s{2}${escapeRegExp(key)}:\\s*(?:#.*)?$`);
  for (let i = section.start + 1; i < section.end; i++) {
    if (re.test(lines[i]!)) return true;
  }
  return false;
}

function hermesMcpMarkers(serverName: string): { begin: string; end: string } {
  return serverName === "caveman"
    ? { begin: HERMES_MCP_BEGIN, end: HERMES_MCP_END }
    : { begin: `# >>> ${serverName}:mcp`, end: `# <<< ${serverName}:mcp` };
}

function hermesMcpChildBlock(mcp: { command: string; args: string[] }, serverName = "caveman"): string[] {
  const markers = hermesMcpMarkers(serverName);
  const lines = [
    `  ${markers.begin}`,
    `  ${serverName}:`,
    `    command: ${yamlQuote(mcp.command)}`,
  ];
  if (mcp.args.length > 0) lines.push(`    args: ${yamlInlineArray(mcp.args)}`);
  lines.push("    enabled: true", `  ${markers.end}`);
  return lines;
}

function installMcpHermesYaml(mcp: { command: string; args: string[] }, serverName = "caveman"): boolean {
  // Hermes stores MCP servers in ~/.hermes/config.yaml under mcp_servers.<name>
  // with stdio command/args (sources: ~/.hermes/hermes-agent/hermes_cli/mcp_config.py:1-9,78-104;
  // ~/.hermes/hermes-agent/cli-config.yaml.example:909-945). `hermes mcp add`
  // exists, but is discovery-first and interactive (mcp_config.py:347-548), so
  // this installer writes the same schema in a marker-fenced block.
  const path = hermesConfigPath();
  let existing = "";
  try {
    existing = readFileSync(path, "utf8");
  } catch (e) {
    if ((e as NodeJS.ErrnoException).code !== "ENOENT") {
      console.error(`${mark("warn")} cannot read ${path}: ${(e as Error).message}`);
      return false;
    }
  }
  const markers = hermesMcpMarkers(serverName);
  const stripped = stripMarkedBlock(existing, markers.begin, markers.end).text;
  const lines = yamlLines(stripped);
  const section = topLevelSection(lines, "mcp_servers");
  if (section) {
    if (sectionHasChildKey(lines, section, serverName)) {
      console.error(`${mark("warn")} ${path} already has an unmarked mcp_servers.${serverName} entry; not overwriting it`);
      return false;
    }
    lines.splice(section.start + 1, 0, ...hermesMcpChildBlock(mcp, serverName));
  } else {
    if (lines.length > 0 && lines[lines.length - 1]!.trim() !== "") lines.push("");
    lines.push(
      markers.begin,
      "mcp_servers:",
      `  ${serverName}:`,
      `    command: ${yamlQuote(mcp.command)}`,
      ...(mcp.args.length > 0 ? [`    args: ${yamlInlineArray(mcp.args)}`] : []),
      "    enabled: true",
      markers.end,
    );
  }
  try {
    mkdirSync(dirname(path), { recursive: true });
    writeFileSync(path, yamlText(lines));
    return true;
  } catch (e) {
    console.error(`${mark("warn")} cannot write ${path}: ${(e as Error).message}`);
    return false;
  }
}

function removeMcpHermesYaml(serverName = "caveman"): boolean {
  const path = hermesConfigPath();
  let existing = "";
  try {
    existing = readFileSync(path, "utf8");
  } catch (e) {
    return (e as NodeJS.ErrnoException).code === "ENOENT";
  }
  const markers = hermesMcpMarkers(serverName);
  const stripped = stripMarkedBlock(existing, markers.begin, markers.end);
  if (!stripped.removed) return true;
  try {
    writeFileSync(path, stripped.text);
    return true;
  } catch (e) {
    console.error(`${mark("warn")} cannot write ${path}: ${(e as Error).message}`);
    return false;
  }
}

function mcpUsage(): never {
  const prefix = currentInvocation.group ? `${invokedAs()} ${currentInvocation.group} mcp` : `${invokedAs()} mcp`;
  console.error(`usage: ${prefix} install|uninstall [agent] [--server caveman|caveman-browse|caveman-cloud|caveman-delegate]`);
  console.error("  caveman: recovery tools for streaming compression and pixel disclosure");
  console.error("  caveman-browse: compressed browser tools");
  console.error("  caveman-cloud: project-scoped reports, traces, plans, and read-only experiment evidence");
  console.error("  caveman-delegate: pi-harness delegate tool for bounded subtasks (opt-in via execute.delegate)");
  console.error("  with no agent, installs for every known agent detected on PATH.");
  console.error("  uninstall removes the tool registration and the marker again.");
  process.exit(2);
}

// mcpUninstall reverses mcpInstall: de-register the caveman MCP server from the
// agent's config and drop the marker, so wrap stops signaling MCP recovery.
function mcpUninstall(target?: string, serverName = "caveman") {
  if (serverName !== "caveman" && serverName !== "caveman-browse" && serverName !== "caveman-cloud" && serverName !== "caveman-delegate") {
    console.error(`unknown MCP server '${serverName}'. valid: caveman, caveman-browse, caveman-cloud, caveman-delegate`);
    process.exit(2);
  }
  let targets: AgentProfile[];
  if (target) {
    const a = findAgent(target);
    if (!a) {
      console.error(`unknown agent '${target}'. known: ${AGENTS.map((x) => x.id).join(", ")}`);
      process.exit(2);
    }
    targets = [a];
  } else {
    targets = AGENTS.filter((a) => mcpServerInstalled(a.id, serverName));
    if (targets.length === 0) {
      console.error(`no agents have the ${serverName} MCP tool installed`);
      return;
    }
  }
  for (const a of targets) {
    if (uninstallMcpForAgent(a, serverName)) {
      try {
        unlinkSync(mcpServerMarkerPath(a.id, serverName));
      } catch {
        // marker already gone — the visible state is what matters.
      }
      process.stderr.write(`${mark("ok")} ${a.display_name}: ${serverName} MCP tool removed\n`);
    }
  }
}

function uninstallMcpForAgent(a: AgentProfile, serverName = "caveman"): boolean {
  switch (a.id) {
    case "claude": {
      const claude = which("claude");
      if (!claude) return true; // agent itself is gone; dropping the marker is all that's left
      try {
        execPortableIgnore(claude, ["mcp", "remove", serverName]);
      } catch {
        // not registered — fine, still a successful uninstall.
      }
      return true;
    }
    case "codex":
      return removeMcpCodexToml(serverName);
    case "opencode":
      return removeMcpJson(join(homedir(), ".config", "opencode", "opencode.json"), ["mcp", serverName]);
    case "gemini":
      return removeMcpJson(join(homedir(), ".gemini", "settings.json"), ["mcpServers", serverName]);
    case "hermes":
      return removeMcpHermesYaml(serverName);
    case "openclaw":
      return uninstallMcpOpenClaw(serverName);
    default:
      return false;
  }
}

// removeMcpCodexToml drops the exact [mcp_servers.<name>] block mcpInstall wrote
// (header + its command/args lines, up to the next section or EOF).
function removeMcpCodexToml(serverName = "caveman"): boolean {
  const path = join(homedir(), ".codex", "config.toml");
  let existing = "";
  try {
    existing = readFileSync(path, "utf8");
  } catch (e) {
    return (e as NodeJS.ErrnoException).code === "ENOENT"; // nothing to remove
  }
  const header = `[mcp_servers.${serverName}]`;
  if (!existing.includes(header)) return true;
  const headerMatch = new RegExp(`(^|\\n)[ \\t]*\\[mcp_servers\\.${escapeRegExp(serverName)}\\][ \\t]*(?:\\r?\\n|$)`, "m").exec(existing);
  if (!headerMatch) return true;
  // Include installer-owned separator newline so install→uninstall restores
  // unrelated TOML byte-for-byte instead of accumulating blank lines.
  const blockStart = headerMatch.index;
  const contentStart = headerMatch.index + headerMatch[0].length;
  const nextHeaderOffset = existing.slice(contentStart).search(/^[ \t]*\[/m);
  const blockEnd = nextHeaderOffset === -1 ? existing.length : contentStart + nextHeaderOffset;
  const cleaned = existing.slice(0, blockStart) + existing.slice(blockEnd);
  try {
    writeFileSync(path, cleaned);
    return true;
  } catch (e) {
    console.error(`${mark("warn")} cannot write ${path}: ${(e as Error).message}`);
    return false;
  }
}

// removeMcpJson deletes a nested key from an agent's JSON config, leaving the
// rest byte-identical in structure. Missing file/key counts as removed.
function removeMcpJson(path: string, keyPath: string[]): boolean {
  let root: Record<string, unknown>;
  try {
    const parsed = JSON.parse(readFileSync(path, "utf8"));
    if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) return true;
    root = parsed as Record<string, unknown>;
  } catch (e) {
    if ((e as NodeJS.ErrnoException).code === "ENOENT") return true;
    console.error(`${mark("warn")} cannot read ${path}: ${(e as Error).message}; not modifying it`);
    return false;
  }
  let cur: Record<string, unknown> = root;
  for (let i = 0; i < keyPath.length - 1; i++) {
    const next = cur[keyPath[i]!];
    if (typeof next !== "object" || next === null || Array.isArray(next)) return true; // key absent
    cur = next as Record<string, unknown>;
  }
  if (!(keyPath[keyPath.length - 1]! in cur)) return true;
  delete cur[keyPath[keyPath.length - 1]!];
  try {
    writeFileSync(path, JSON.stringify(root, null, 2) + "\n");
    return true;
  } catch (e) {
    console.error(`${mark("warn")} cannot write ${path}: ${(e as Error).message}`);
    return false;
  }
}

function mcpInstall(target?: string, serverName = "caveman"): number {
  if (serverName !== "caveman" && serverName !== "caveman-browse" && serverName !== "caveman-cloud" && serverName !== "caveman-delegate") {
    console.error(`unknown MCP server '${serverName}'. valid: caveman, caveman-browse, caveman-cloud, caveman-delegate`);
    process.exit(2);
  }
  let mcp: { command: string; args: string[] };
  if (serverName === "caveman") {
    mcp = resolveMcpCommand();
  } else if (serverName === "caveman-browse") {
    const binary = resolveGoBin("caveman-browse", "CAVEMAN_BROWSE_BIN");
    if (!binary) {
      console.error("caveman-browse binary not found — run `caveman setup --install` first");
      process.exit(1);
    }
    mcp = { command: binary, args: [] };
  } else if (serverName === "caveman-delegate") {
    const resolved = resolveDelegateMcpCommand();
    if (!resolved) {
      console.error("caveman-delegate-mcp.mjs not found — set CAVEMAN_DELEGATE_MCP to the script path");
      process.exit(1);
    }
    mcp = resolved;
  } else {
    mcp = resolveCloudMcpCommand();
  }
  let targets: AgentProfile[];
  if (target) {
    const a = findAgent(target);
    if (!a) {
      console.error(`unknown agent '${target}'. known: ${AGENTS.map((x) => x.id).join(", ")}`);
      process.exit(2);
    }
    targets = [a];
  } else {
    targets = AGENTS.filter((a) => which(binOf(a)));
    if (targets.length === 0) {
      console.error("no known agents detected on PATH; pass an agent id, e.g. `caveman mcp install claude`");
      process.exit(1);
    }
  }
  let installed = 0;
  for (const a of targets) {
    if (installMcpForAgent(a, mcp, serverName)) {
      if (serverName === "caveman") {
        writeMcpMarker(a.id, mcp);
      } else {
        writeMcpServerMarker(
          a.id,
          serverName,
          mcp,
          serverName === "caveman-browse" ? "caveman_browse" : serverName === "caveman-delegate" ? "caveman_delegate" : "caveman_context",
        );
      }
      installed++;
      const tool = serverName === "caveman"
        ? "caveman_retrieve"
        : serverName === "caveman-browse"
          ? "caveman_browse"
          : serverName === "caveman-delegate"
            ? "caveman_delegate"
            : "caveman cloud tools";
      process.stderr.write(`${mark("ok")} ${a.display_name}: ${tool} installed\n`);
    }
  }
  if (installed > 0 && serverName === "caveman") {
    process.stderr.write(dim("→ `caveman wrap` will now compress streaming requests too (recovered via MCP)\n"));
  }
  if (installed > 0 && serverName === "caveman-cloud") {
    process.stderr.write(dim("→ credentials stay in Caveman CLI store; run `caveman login` if disconnected\n"));
  }
  return installed;
}

function installMcpForAgent(a: AgentProfile, mcp: { command: string; args: string[] }, serverName = "caveman"): boolean {
  switch (a.id) {
    case "claude":
      return installMcpClaude(mcp, serverName);
    case "codex":
      return installMcpCodexToml(mcp, serverName);
    case "opencode":
      return installMcpJson(join(homedir(), ".config", "opencode", "opencode.json"), ["mcp", serverName], {
        type: "local",
        command: [mcp.command, ...mcp.args],
        enabled: true,
      });
    case "gemini":
      return installMcpJson(join(homedir(), ".gemini", "settings.json"), ["mcpServers", serverName], {
        command: mcp.command,
        args: mcp.args,
      });
    case "hermes":
      return installMcpHermesYaml(mcp, serverName);
    case "openclaw":
      return installMcpOpenClaw(mcp, serverName);
    default:
      // No standardized MCP config we can write safely (e.g. aider): print the
      // server command for the user to wire manually, and do NOT mark it installed
      // (so wrap won't dishonestly signal recovery for an agent that can't retrieve).
      process.stderr.write(
        `${mark("warn")} ${a.display_name}: no automatic MCP install — register an MCP server named "${serverName}" running: ${cyan([mcp.command, ...mcp.args].join(" "))}\n`,
      );
      return false;
  }
}

function openClawCli(): string | null {
  return which("openclaw");
}

function execPortableIgnore(command: string, args: string[]): void {
  const invocation = portableInvocation(command, args);
  execFileSync(invocation.command, invocation.args, { stdio: "ignore" });
}

function installMcpOpenClaw(mcp: { command: string; args: string[] }, serverName = "caveman"): boolean {
  const openclaw = openClawCli();
  if (!openclaw) {
    console.error(`${mark("warn")} openclaw CLI not found on PATH; install OpenClaw first`);
    return false;
  }
  const addArgs = ["mcp", "add", serverName, "--command", mcp.command, "--no-probe"];
  for (const arg of mcp.args) addArgs.push("--arg", arg);
  try {
    execPortableIgnore(openclaw, ["mcp", "unset", serverName]);
  } catch {
    // Missing server or older config state is fine; add below is authoritative.
  }
  try {
    execPortableIgnore(openclaw, addArgs);
    return true;
  } catch (e) {
    console.error(`${mark("warn")} openclaw mcp add failed: ${(e as Error).message}`);
    return false;
  }
}

function uninstallMcpOpenClaw(serverName = "caveman"): boolean {
  const openclaw = openClawCli();
  if (!openclaw) return true; // agent itself is gone; dropping the marker is all that's left
  try {
    execPortableIgnore(openclaw, ["mcp", "unset", serverName]);
  } catch {
    // not registered — fine, still a successful uninstall.
  }
  return true;
}

function installMcpClaude(mcp: { command: string; args: string[] }, serverName = "caveman"): boolean {
  const claude = which("claude");
  if (!claude) {
    console.error(`${mark("warn")} claude CLI not found on PATH; install Claude Code first`);
    return false;
  }
  try {
    execPortableIgnore(claude, ["mcp", "remove", "--scope", "user", serverName]);
  } catch {
    // not previously installed — fine.
  }
  try {
    execPortableIgnore(claude, ["mcp", "add", "--scope", "user", serverName, "--", mcp.command, ...mcp.args]);
    return true;
  } catch (e) {
    console.error(`${mark("warn")} claude mcp add failed: ${(e as Error).message}`);
    return false;
  }
}

function installMcpCodexToml(mcp: { command: string; args: string[] }, serverName = "caveman"): boolean {
  const path = join(homedir(), ".codex", "config.toml");
  let existing = "";
  try {
    existing = readFileSync(path, "utf8");
  } catch (e) {
    if ((e as NodeJS.ErrnoException).code !== "ENOENT") {
      console.error(`${mark("warn")} cannot read ${path}: ${(e as Error).message}`);
      return false;
    }
  }
  const header = `[mcp_servers.${serverName}]`;
  const argsLine = mcp.args.length ? `\nargs = [${mcp.args.map((s) => JSON.stringify(s)).join(", ")}]` : "";
  const expectedBlock = `${header}\ncommand = ${JSON.stringify(mcp.command)}${argsLine}\n`;
  if (existing.includes(header)) {
    const headerMatch = new RegExp(`(^|\\n)[ \\t]*\\[mcp_servers\\.${escapeRegExp(serverName)}\\][ \\t]*(?:\\r?\\n|$)`, "m").exec(existing);
    if (!headerMatch) {
      console.error(`${mark("warn")} malformed ${header} block; refusing unsafe MCP update`);
      return false;
    }
    const blockStart = headerMatch.index + (headerMatch[1] ? 1 : 0);
    const contentStart = headerMatch.index + headerMatch[0].length;
    const nextHeaderOffset = existing.slice(contentStart).search(/^[ \\t]*\[/m);
    const blockEnd = nextHeaderOffset === -1 ? existing.length : contentStart + nextHeaderOffset;
    const currentBlock = existing.slice(blockStart, blockEnd).trim();
    if (currentBlock === expectedBlock.trim()) {
      if (readMcpServerMarker("codex", serverName)) return true;
      console.error(`${mark("warn")} ${header} already exists but is not Caveman-journaled; refusing to claim ownership`);
      return false;
    }
    if (!readMcpServerMarker("codex", serverName)) {
      console.error(`${mark("warn")} ${header} exists with different command/args and is not Caveman-journaled; refusing overwrite`);
      return false;
    }
    existing = existing.slice(0, blockStart) + existing.slice(blockEnd);
  }
  const head = existing.trim() ? existing.replace(/\s*$/, "") + "\n" : "";
  const block = `${head}\n${expectedBlock}`;
  try {
    mkdirSync(dirname(path), { recursive: true });
    writeFileSync(path, block);
    return true;
  } catch (e) {
    console.error(`${mark("warn")} cannot write ${path}: ${(e as Error).message}`);
    return false;
  }
}

function codexMcpRegistrationMatches(serverName: string, mcp: { command: string; args: string[] }): boolean {
  const path = join(homedir(), ".codex", "config.toml");
  let existing = "";
  try { existing = readFileSync(path, "utf8"); } catch { return false; }
  const headerMatch = new RegExp(`(^|\\n)[ \\t]*\\[mcp_servers\\.${escapeRegExp(serverName)}\\][ \\t]*(?:\\r?\\n|$)`, "m").exec(existing);
  if (!headerMatch) return false;
  const blockStart = headerMatch.index + (headerMatch[1] ? 1 : 0);
  const contentStart = headerMatch.index + headerMatch[0].length;
  const nextHeaderOffset = existing.slice(contentStart).search(/^[ \\t]*\[/m);
  const blockEnd = nextHeaderOffset === -1 ? existing.length : contentStart + nextHeaderOffset;
  const argsLine = mcp.args.length ? `\nargs = [${mcp.args.map((arg) => JSON.stringify(arg)).join(", ")}]` : "";
  const expected = `[mcp_servers.${serverName}]\ncommand = ${JSON.stringify(mcp.command)}${argsLine}`;
  return existing.slice(blockStart, blockEnd).trim() === expected.trim();
}

// installMcpJson merges a value at a nested key into an agent's JSON config without
// disturbing the rest of it. It refuses to touch a file that is not a JSON object
// (rather than corrupt it), and is idempotent.
function installMcpJson(path: string, keyPath: string[], value: unknown): boolean {
  let root: Record<string, unknown> = {};
  try {
    const raw = readFileSync(path, "utf8").trim();
    if (raw) {
      const parsed = JSON.parse(raw);
      if (parsed && typeof parsed === "object" && !Array.isArray(parsed)) {
        root = parsed as Record<string, unknown>;
      } else {
        console.error(`${mark("warn")} ${path} is not a JSON object; not modifying it`);
        return false;
      }
    }
  } catch (e) {
    if ((e as NodeJS.ErrnoException).code !== "ENOENT") {
      console.error(`${mark("warn")} cannot read ${path}: ${(e as Error).message}; not modifying it`);
      return false;
    }
  }
  let cur: Record<string, unknown> = root;
  for (let i = 0; i < keyPath.length - 1; i++) {
    const k = keyPath[i]!;
    if (typeof cur[k] !== "object" || cur[k] === null || Array.isArray(cur[k])) cur[k] = {};
    cur = cur[k] as Record<string, unknown>;
  }
  cur[keyPath[keyPath.length - 1]!] = value;
  try {
    mkdirSync(dirname(path), { recursive: true });
    writeFileSync(path, JSON.stringify(root, null, 2) + "\n");
    return true;
  } catch (e) {
    console.error(`${mark("warn")} cannot write ${path}: ${(e as Error).message}`);
    return false;
  }
}

function writeMcpMarker(agentId: string, mcp: { command: string; args: string[] }): void {
  writeMcpServerMarker(agentId, "caveman", mcp, "caveman_retrieve");
}

function writeMcpServerMarker(agentId: string, serverName: string, mcp: { command: string; args: string[] }, tool: string): void {
  const path = mcpServerMarkerPath(agentId, serverName);
  try {
    mkdirSync(dirname(path), { recursive: true });
    writeFileSync(path, JSON.stringify({ tool, command: mcp.command, args: mcp.args }, null, 2) + "\n");
  } catch {
    // best-effort: without the marker, wrap simply tries the loadout again later.
  }
}

// compress streams stdin through the caveman-engine binary (resolved via
// CAVEMAN_ENGINE_BIN, default `caveman-engine` on PATH) and writes the compressed
// payload to stdout, forwarding the engine's JSON ratio report to stderr. The
// engine is fail-closed; if its binary is unavailable the CLI falls back to a
// pass-through that claims a 0 ratio, so `compress` never breaks a pipe.
async function compress(argv: string[]) {
  if (argv[0] === "catalog") return compressCatalog(argv.slice(1));
  if (argv.includes("--toon-stats")) {
    throw new Error("caveman compress --toon-stats is unavailable; use --toon to force TOON compression");
  }
  const input = await readStdin();
  const bin = cavemanBin("caveman-engine", "CAVEMAN_ENGINE_BIN");
  const typeIndex = argv.indexOf("--type");
  if (typeIndex >= 0 && (argv[typeIndex + 1] === undefined || argv[typeIndex + 1]!.startsWith("--"))) {
    throw new Error(`usage: ${invokedCommand("compress")} [--type <content-type>] [--toon]`);
  }
  let forcedType = argv.includes("--toon") ? "toon" : flagFrom(argv, "--type", "");
  if (forcedType === "auto") forcedType = "";
  const engineArgs = ["compress"];
  if (forcedType) engineArgs.push("--type", forcedType);
  let handled = false;
  const child = spawn(bin, engineArgs, { stdio: ["pipe", "inherit", "inherit"] });
  emitCommandRunOnce("ok"); // exit handler below hard-exits; never returns to main()
  child.on("error", () => { if (!handled) { handled = true; compressFallback(input, forcedType); } });
  child.on("exit", (code) => { if (!handled) { handled = true; process.exit(code ?? 0); } });
  if (child.stdin) {
    child.stdin.on("error", () => {}); // ignore broken pipe; the child 'error' drives the fallback
    child.stdin.end(input);
  }
}

// compressCatalog is explicit bridge to dedicated tool-catalog product. It
// preserves caveman-shrink's stdin/stdout/report contract and forwards lint /
// recover arguments without conflating them with command-output `caveman shrink`.
function compressCatalog(argv: string[]): Promise<never> {
  return new Promise(() => {
    const bin = cavemanBin("caveman-shrink", "CAVEMAN_SHRINK_BIN");
    const child = spawn(bin, argv, { stdio: "inherit" });
    child.on("error", (error) => {
      emitCommandRunOnce("error", classifyTelemetryError(error));
      console.error(`${mark("warn")} cannot run ${bin}: ${error.message}; run \`caveman setup --install\``);
      process.exit(1);
    });
    child.on("exit", (code) => {
      emitCommandRunOnce(code === 0 ? "ok" : "error", code === 0 ? undefined : "other");
      process.exit(code ?? 1);
    });
  });
}

// compressFallback is the byte-safe degradation when the engine binary is
// missing: emit the input unchanged and report a 0 ratio (basis inferred).
function compressFallback(input: Buffer, contentType = "") {
  process.stdout.write(input);
  // The engine binary is missing, so nothing was compressed. Make the no-op
  // self-describing in the structured report (stderr stays valid JSON — the same
  // shape the engine emits) so a 0% result can never be mistaken for "it worked".
  console.error(JSON.stringify({
    bytes_in: input.length,
    bytes_out: input.length,
    ratio: 0,
    basis: "inferred",
    token_count_basis: "unavailable",
    content_type: contentType || "unknown",
    engine: "missing",
    note: "caveman-engine not installed — 0% compression, input passed through unchanged. Run `caveman setup` to see what's missing and how to install.",
  }));
}

// toonConvert shells out to `caveman-engine toon encode|decode` — the stateless,
// CCR-free JSON⇄TOON converter (single source of truth in the engine, no JS
// parser). It is the manual surface for the same transform the proxy applies at
// the wire boundary; the converted output must not be fed back into the agent
// that wrote the other form, or it would double the tokens it sees.
async function toonConvert(rest: string[]) {
  const sub = rest[0];
  if (sub !== "encode" && sub !== "decode") {
    throw new Error(`usage: ${invokedCommand("toon")} encode|decode`);
  }
  const input = await readStdin();
  const bin = cavemanBin("caveman-engine", "CAVEMAN_ENGINE_BIN");
  let handled = false;
  const child = spawn(bin, ["toon", sub], { stdio: ["pipe", "inherit", "inherit"] });
  emitCommandRunOnce("ok"); // exit handler below hard-exits; never returns to main()
  child.on("error", () => {
    if (handled) return;
    handled = true;
    // encode degrades byte-safe: the input is still valid JSON, just not compacted.
    // decode cannot be faked without the engine — emitting raw TOON as JSON would
    // hand downstream a broken payload, so it fails loudly instead.
    if (sub === "encode") {
      // Not silent: the pass-through must announce itself so 0% can never be
      // mistaken for "TOON didn't help".
      console.error(`${mark("warn")} caveman-engine not found — emitting input JSON unchanged (no TOON encoding); run \`caveman setup\` to see what's missing`);
      process.stdout.write(input);
      process.exit(0);
    }
    console.error(`caveman toon decode needs the caveman-engine binary (run \`caveman setup\`, set CAVEMAN_ENGINE_BIN, or build it); refusing to emit unconverted TOON as JSON`);
    process.exit(1);
  });
  child.on("exit", (code) => { if (!handled) { handled = true; process.exit(code ?? 0); } });
  if (child.stdin) {
    child.stdin.on("error", () => {});
    child.stdin.end(input);
  }
}

// shrink wraps a command (or a --file / stdin transcript) and shrinks its output
// BEFORE a model reads it — the recoverable counterpart to a crude shell rewriter.
// It runs the command, captures combined stdout+stderr, compresses that through the
// engine's terminal compressor (lossy S4, but byte-exact recoverable via CCR),
// prints the shrunk output plus a one-line recovery footer carrying the handle, and
// propagates the command's exit code. Byte-safe: on `--raw`, oversized output, or
// any engine problem it prints the original output unchanged and claims nothing.
async function shrink(rest: string[]) {
  let raw = false;
  let forcedType = "terminal";
  let file = "";
  const cmd: string[] = [];
  for (let i = 0; i < rest.length; i++) {
    const a = rest[i]!;
    if (a === "--") { cmd.push(...rest.slice(i + 1)); break; }
    if (a === "--raw") { raw = true; continue; }
    if (a === "--stdin") { continue; }
    if (a === "--type") { forcedType = rest[++i] ?? "terminal"; continue; }
    if (a === "--file") { file = rest[++i] ?? ""; continue; }
    if (a.startsWith("--")) { console.error(`unknown shrink flag: ${a}`); process.exit(2); }
    cmd.push(...rest.slice(i)); // first non-flag begins the wrapped command
    break;
  }

  if (cmd.length > 0) {
    const { output, code } = await runCapture(cmd);
    emitShrunk(output, forcedType, raw);
    process.exit(code);
  }
  // No command: shrink a captured transcript or file snippet from --file or stdin.
  const input = file ? readFileSync(file) : await readStdin();
  emitShrunk(input, forcedType, raw);
}

// runCapture runs a command with stdin inherited, collecting stdout and stderr into
// one buffer in arrival order (an approximate 2>&1 merge — what a model would read),
// and resolves with the combined output and the child's exit code. A missing binary
// resolves with code 127 and whatever was captured, so shrink never throws.
function runCapture(cmd: string[]): Promise<{ output: Buffer; code: number }> {
  return new Promise((resolve) => {
    const chunks: Buffer[] = [];
    let child;
    try {
      const command = which(cmd[0]!) ?? cmd[0]!;
      const invocation = portableInvocation(command, cmd.slice(1));
      child = spawn(invocation.command, invocation.args, { stdio: ["inherit", "pipe", "pipe"] });
    } catch (e) {
      console.error(`${mark("warn")} cannot run ${cmd[0]}: ${(e as Error).message}`);
      resolve({ output: Buffer.alloc(0), code: 127 });
      return;
    }
    child.stdout?.on("data", (c: Buffer) => chunks.push(c));
    child.stderr?.on("data", (c: Buffer) => chunks.push(c));
    child.on("error", (e) => {
      console.error(`${mark("warn")} cannot run ${cmd[0]}: ${e.message}`);
      resolve({ output: Buffer.concat(chunks), code: 127 });
    });
    child.on("close", (code, signal) => resolve({ output: Buffer.concat(chunks), code: code ?? (signal ? 1 : 0) }));
  });
}

// emitShrunk compresses captured output through the engine and writes the shrunk
// payload + a recovery footer to stdout (the engine's JSON report goes to stderr).
// It fails open to the original bytes on `--raw`, empty/oversized input, or any
// engine problem, and only prints a footer when a handle was actually minted —
// never a fake-savings line on a pass-through.
function emitShrunk(input: Buffer, forcedType: string, raw: boolean) {
  const cap = Number(process.env.CAVE_MAX_SHRINK_BYTES ?? 8 * 1024 * 1024);
  if (raw || input.length === 0 || input.length > cap) {
    if (!raw && input.length > cap) {
      console.error(`${mark("warn")} output ${input.length}B exceeds CAVE_MAX_SHRINK_BYTES (${cap}B); passing through unshrunk`);
    }
    process.stdout.write(input);
    return;
  }
  const bin = cavemanBin("caveman-engine", "CAVEMAN_ENGINE_BIN");
  const engineArgs = ["compress"];
  if (forcedType && forcedType !== "auto") engineArgs.push("--type", forcedType);
  const r = spawnSync(bin, engineArgs, { input, maxBuffer: 256 * 1024 * 1024 });
  if (r.error || r.status !== 0 || !r.stdout) {
    process.stdout.write(input); // byte-safe: engine unavailable/failed → original, claim nothing
    console.error(JSON.stringify({ bytes_in: input.length, bytes_out: input.length, ratio: 0, basis: "inferred", token_count_basis: "unavailable", note: "engine unavailable; passthrough — run `caveman setup`" }));
    return;
  }
  let report: { recovery_handle?: string; tokens_before?: number; tokens_after?: number; token_count_basis?: string } = {};
  try {
    const lastLine = (r.stderr ?? Buffer.alloc(0)).toString("utf8").trim().split("\n").pop() || "{}";
    report = JSON.parse(lastLine);
  } catch { /* report optional; output is still byte-safe */ }
  process.stdout.write(r.stdout);
  const handle = report.recovery_handle ?? "";
  if (handle) {
    const needsNL = r.stdout.length > 0 && r.stdout[r.stdout.length - 1] !== 0x0a;
    const before = validNonNegativeInteger(report.tokens_before);
    const after = validNonNegativeInteger(report.tokens_after);
    const basis = typeof report.token_count_basis === "string" && report.token_count_basis.trim()
      ? report.token_count_basis.trim()
      : "unavailable";
    const counts = before !== null && after !== null && after <= before
      ? `${before}→${after} estimated tokens`
      : "estimated token count unavailable";
    process.stdout.write(`${needsNL ? "\n" : ""}‹caveman: shrank · ${counts} · counter ${basis} · inferred · recover: caveman retrieve ${handle}›\n`);
  }
  if (r.stderr && r.stderr.length) process.stderr.write(r.stderr);
}

// ── command-output auto-hook (the RTK-parity loadout layer) ──────────────────
// `caveman shrink` is the recoverable shrinker; the auto-hook is what makes it
// load with no manual step — it routes the noisy output of the shell commands a
// coding agent runs through `caveman shrink` before the model reads it (RTK's
// trick), but byte-exact recoverable via CCR instead of a lossy plaintext tee.

// The command families whose output is bulky but finite and safe to capture. We
// err toward NOT shrinking: anything outside this set passes through untouched.
const SHRINK_ALLOW = new Set([
  "git", "cargo", "go", "npm", "pnpm", "yarn", "pip", "pip3", "pytest", "make",
  "jest", "vitest", "tsc", "eslint", "mypy", "ruff", "pylint", "mvn", "gradle",
  "dotnet", "rspec", "bundle", "grep", "rg", "ag", "egrep", "fgrep", "find",
  "ls", "tree", "du", "df", "ps", "docker", "kubectl", "terraform", "helm",
  "aws", "gcloud", "az", "gh",
]);

// shouldShrink decides whether a Bash command's output should be routed through
// `caveman shrink`. Conservative by design: it only rewrites a known-noisy,
// finite, non-interactive command with no shell operators (shrink execs argv
// directly, so a pipe/redirect/substitution would change semantics) and no
// streaming/interactive flags (shrink captures output, so the command must end).
function shouldShrink(command: string): boolean {
  const cmd = command.trim();
  if (!cmd) return false;
  if (/^(caveman|cave)\b/.test(cmd)) return false; // never double-wrap our own
  if (/[|&;<>`]|\$\(|\\\s*$|\n/.test(cmd)) return false; // shell operators → meaning would change
  if (/(^|\s)-(f|it|ti|w)\b|--follow\b|--watch\b|--interactive\b|--tail\b/.test(cmd)) return false; // streaming/interactive
  const tokens = cmd.split(/\s+/);
  const first = tokens[0] ?? "";
  const pair = `${first} ${tokens[1] ?? ""}`;
  const skipPairs = new Set([
    "git commit", "git rebase", "git mergetool", "npm init", "yarn init", "pnpm init",
    "docker run", "docker exec", "docker attach", "kubectl edit", "kubectl exec",
    "kubectl attach", "terraform apply", "terraform destroy",
  ]);
  if (skipPairs.has(pair)) return false;
  return SHRINK_ALLOW.has(first);
}

// cavemanBinForHook is the invocation a Claude hook uses to call back into this
// CLI, robust to PATH: a resolved `caveman`/`cave`, else this very script's node.
function cavemanBinForHook(): string {
  const command = which("caveman") ?? which("cave");
  return command
    ? hookExecutableInvocation(command, undefined)
    : hookExecutableInvocation(process.execPath, process.argv[1]!);
}

// shrinkHook is the settings-hook callback for the agents whose harness can
// deterministically rewrite a shell command before it runs: Claude Code (PreToolUse,
// tool "Bash"), the opencode plugin (which feeds the same "Bash" shape), and Gemini
// CLI (BeforeTool, tool "run_shell_command"). It reads the tool event on stdin and,
// for a noisy command, rewrites it to run through `caveman shrink`. Anything it won't
// safely shrink it passes through: exit 0 with NO stdout = "no rewrite, run as-is".
async function shrinkHook() {
  if (nativePolicyMode() === "record" || !new Set(["full-safe", "full-max"]).has(nativeProfile())) process.exit(0);
  let raw: Buffer;
  try { raw = await readStdin(); } catch { process.exit(0); }
  let evt: { tool_name?: string; tool_input?: { command?: string } };
  try { evt = JSON.parse(raw.toString("utf8") || "{}"); } catch { process.exit(0); }
  const tool = evt?.tool_name;
  const isGemini = tool === "run_shell_command"; // Gemini CLI's shell tool
  const isBash = tool === "Bash";                // Claude Code + the opencode plugin
  const isCodex = tool === "shell" || tool === "shell_command" || tool === "exec_command";
  if (!isGemini && !isBash && !isCodex) process.exit(0);
  const command = evt.tool_input?.command;
  if (typeof command !== "string" || !shouldShrink(command)) process.exit(0);
  const rewritten = `${cavemanBinForHook()} shrink -- ${command.trim()}`;
  // Each harness has a different (silent-on-mismatch) override contract: Gemini merges
  // hookSpecificOutput.tool_input (snake_case, no event discriminator); Claude replaces
  // via hookSpecificOutput.updatedInput (camelCase + hookEventName). Emit the right one.
  const out = isGemini
    ? { hookSpecificOutput: { tool_input: { command: rewritten } } }
    : { hookSpecificOutput: { hookEventName: "PreToolUse", permissionDecision: "allow", updatedInput: { command: rewritten } } };
  process.stdout.write(JSON.stringify(out));
}

type NativePolicyMode = "record" | "safe" | "max";
type NativeProfile = "record-only" | "core" | "core-lean-build" | "ledger" | "ccr-masking" | "cache-aware" | "full-safe" | "full-max";

const NATIVE_PROFILES = new Set<NativeProfile>([
  "record-only", "core", "core-lean-build", "ledger", "ccr-masking", "cache-aware", "full-safe", "full-max",
]);

function nativeProfile(): NativeProfile {
  const explicit = process.env.CAVEMAN_NATIVE_PROFILE?.trim().toLowerCase() as NativeProfile | undefined;
  if (explicit) return NATIVE_PROFILES.has(explicit) ? explicit : "record-only";
  const mode = nativePolicyMode();
  return mode === "record" ? "record-only" : mode === "max" ? "full-max" : "full-safe";
}

function nativePolicyMode(): NativePolicyMode {
  const profile = process.env.CAVEMAN_NATIVE_PROFILE?.trim().toLowerCase();
  if (profile && !NATIVE_PROFILES.has(profile as NativeProfile)) return "record";
  if (profile === "record-only") return "record";
  const explicit = process.env.CAVEMAN_NATIVE_MODE?.trim().toLowerCase();
  if (explicit !== undefined && explicit !== "") {
    if (explicit === "record" || explicit === "safe" || explicit === "max") return explicit;
    return "record";
  }
  const proxyMode = wrapRuntimeConfig().mode;
  return proxyMode === "record" ? "record" : proxyMode === "pixel" ? "max" : "safe";
}

function nativeCoreRuntimeState(): { configured: boolean; active: boolean } {
  const configured = wrapRuntimeConfig().core;
  return {
    configured,
    active: configured && nativePolicyMode() !== "record" && nativeProfile() !== "record-only",
  };
}

const NATIVE_EVENT_NAMES = new Set([
  "SessionStart", "UserPromptSubmit", "PreToolUse", "PermissionRequest", "PostToolUse", "PostToolUseFailure",
  "ModelBefore", "ModelAfter", "PreCompact", "PostCompact", "SubagentStart", "SubagentStop", "Stop", "SessionEnd",
]);

const NATIVE_PROTOCOL_EVENTS: Record<string, string> = {
  SessionStart: "session.start",
  UserPromptSubmit: "prompt.submit",
  PreToolUse: "tool.before",
  PermissionRequest: "tool.before",
  PostToolUse: "tool.after",
  PostToolUseFailure: "tool.failure",
  ModelBefore: "model.before",
  ModelAfter: "model.after",
  PreCompact: "context.compact.before",
  PostCompact: "context.compact.after",
  SubagentStart: "subagent.start",
  SubagentStop: "subagent.stop",
  Stop: "turn.stop",
  SessionEnd: "session.end",
};

type NativeRuntimeResponse = {
  protocol_version: number;
  policy_mode?: NativePolicyMode;
  profile?: NativeProfile;
  action: "allow" | "observe" | "block" | "ask" | "defer";
  context?: string;
  message?: string;
  output_replacement?: string;
  recovery_ref?: string;
  decision_id?: string;
  visibility: "silent" | "advisory" | "user";
  fail_open: boolean;
};

const NATIVE_RUNTIME_ACTIONS = new Set(["allow", "observe", "block", "ask", "defer"]);
const NATIVE_RUNTIME_VISIBILITY = new Set(["silent", "advisory", "user"]);

function validNativeRuntimeResponse(value: unknown): value is NativeRuntimeResponse {
  if (!value || typeof value !== "object" || Array.isArray(value)) return false;
  const response = value as Record<string, unknown>;
  if (response.protocol_version !== 1 || typeof response.action !== "string" || !NATIVE_RUNTIME_ACTIONS.has(response.action)) return false;
  if (typeof response.visibility !== "string" || !NATIVE_RUNTIME_VISIBILITY.has(response.visibility) || response.fail_open !== true) return false;
  if (response.policy_mode !== undefined && response.policy_mode !== "record" && response.policy_mode !== "safe" && response.policy_mode !== "max") return false;
  if (response.profile !== undefined && (typeof response.profile !== "string" || !NATIVE_PROFILES.has(response.profile as NativeProfile))) return false;
  const boundedStrings: Array<[unknown, number]> = [
    [response.context, 64 * 1024],
    [response.message, 4096],
    [response.output_replacement, 2 * 1024 * 1024],
    [response.recovery_ref, 1024],
    [response.decision_id, 256],
  ];
  return boundedStrings.every(([field, max]) => field === undefined || (typeof field === "string" && Buffer.byteLength(field) <= max));
}

type NativeReceiptMetric = { value: number; unit: string; basis: string; detail?: string };
type NativeReceipt = {
  schema: "caveman.native.receipt.v1";
  session_id: string;
  host_session_id?: string;
  agent: string;
  generated_at: string;
  outcome: Record<string, string>;
  execution: Record<string, NativeReceiptMetric>;
  provider_usage?: {
    status: string;
    correlation_basis: string;
    requests: number;
    provider_complete_requests: number;
    input_tokens: number;
    output_tokens: number;
    cached_input_tokens: number;
    cache_creation_input_tokens: number;
    reasoning_tokens: number;
    catalog_list_price_subtotal_usd: number;
    inferred_savings_usd: number;
    compression_tokens_before: number;
    compression_tokens_after: number;
    compression_token_count_basis: string;
    token_usage_coverage: string;
    cost_basis: string;
    savings_basis: string;
  };
  policy: { active?: string[]; unresolved_assumptions?: number; decision_ids?: string[] };
  claim_status: Record<string, string>;
};

type NativeWhy = {
  schema: "caveman.native.why.v1";
  decision_id: string;
  session_id: string;
  timestamp_ms: number;
  action: string;
  reason: string;
  input_basis: Record<string, unknown>;
  alternatives_rejected: string[];
  task_state_before: string;
  task_state_after: string;
  currentness: string;
  recovery_ref?: string;
};

function boundedHookString(value: unknown, max = 160): string | undefined {
  if (typeof value !== "string") return undefined;
  const clean = value.replace(/[\r\n\0]/g, " ").trim();
  return clean ? clean.slice(0, max) : undefined;
}

function nativeDigestObject(value: unknown): { bytes: number; sha256: string } | undefined {
  if (value && typeof value === "object" && !Array.isArray(value)) {
    const candidate = value as Record<string, unknown>;
    if (typeof candidate.bytes === "number" && candidate.bytes >= 0 && typeof candidate.sha256 === "string" && /^sha256:[0-9a-f]{6,64}$/i.test(candidate.sha256)) {
      return { bytes: Math.floor(candidate.bytes), sha256: candidate.sha256.toLowerCase() };
    }
  }
  return undefined;
}

function nativePayloadDigest(value: unknown): { bytes: number; sha256: string } | undefined {
  const existing = nativeDigestObject(value);
  if (existing) return existing;
  if (typeof value !== "string") return undefined;
  const bytes = Buffer.from(value, "utf8");
  return { bytes: bytes.length, sha256: `sha256:${createHash("sha256").update(bytes).digest("hex")}` };
}

type NativeTaskType = "feature" | "bugfix" | "investigation" | "refactor" | "migration" | "verification" | "review" | "general";

const NATIVE_TASK_STOPWORDS = new Set([
  "about", "after", "again", "agent", "before", "build", "change", "code", "could", "create", "does", "from",
  "have", "help", "implement", "into", "make", "please", "project", "repository", "should", "spec", "task", "that",
  "their", "then", "there", "these", "they", "this", "through", "user", "want", "what", "when", "where", "which",
  "with", "would", "your",
]);

function nativeTaskTerms(value: unknown, claimed: unknown): string[] {
  const source = typeof value === "string"
    ? value.match(/[A-Za-z][A-Za-z0-9_./-]{2,63}/g) ?? []
    : Array.isArray(claimed) ? claimed.filter((item): item is string => typeof item === "string") : [];
  const seen = new Set<string>();
  const terms: string[] = [];
  for (const raw of source) {
    const term = raw.toLowerCase().replace(/^[-./]+|[-./]+$/g, "");
    if (!term || term.length < 3 || term.length > 64 || term.includes("..") || term.startsWith("/") || NATIVE_TASK_STOPWORDS.has(term) || seen.has(term)) continue;
    if (/^(?:sk|pk|rk|ghp|github_pat|xox[baprs]|akia)[-_]/i.test(term) || /^[a-z0-9_-]{40,}$/i.test(term)) continue;
    seen.add(term);
    terms.push(term);
    if (terms.length === 12) break;
  }
  return terms;
}

// Classify locally from prompt text, then send only bounded task type. Runtime
// never receives raw user prompt; digest remains sole prompt identity.
function nativeTaskType(value: unknown): NativeTaskType {
  if (typeof value !== "string") return "general";
  const prompt = value.toLowerCase();
  if (/\b(migrat(?:e|ion)|schema change|backfill|rollback)\b/.test(prompt)) return "migration";
  if (/\b(bug|fix|broken|regression|crash|error|fails?|incorrect)\b/.test(prompt)) return "bugfix";
  if (/\b(investigat(?:e|ion)|diagnos(?:e|is)|root cause|why does|trace)\b/.test(prompt)) return "investigation";
  if (/\b(refactor|restructure|reorganize|cleanup|clean up)\b/.test(prompt)) return "refactor";
  if (/\b(review|audit|critique|assess)\b/.test(prompt)) return "review";
  if (/\b(verif(?:y|ication)|test only|prove|validate|check that)\b/.test(prompt)) return "verification";
  if (/\b(build|implement|add|create|ship|feature)\b/.test(prompt)) return "feature";
  return "general";
}

function nativeTaskContinuation(value: unknown, claimed: unknown): boolean {
  if (typeof value !== "string") return claimed === true;
  const prompt = value.trim().toLowerCase();
  if (!prompt || prompt.length > 160 || prompt.split(/\s+/).length > 14) return false;
  return /^(?:please\s+)?(?:continue|go ahead|keep going|proceed|do (?:it|that)|fix (?:it|that)|retry|try again|explain (?:it|that)|what do you mean|yes|yep|yeah|why\??|how\??)[.!?\s]*$/.test(prompt);
}

function nativeTaskProfile(value: unknown, claimed: unknown, claimedTerms: unknown, claimedContinuation: unknown): { type: NativeTaskType; terms: string[]; continuation: boolean } {
  const allowed = new Set<NativeTaskType>(["feature", "bugfix", "investigation", "refactor", "migration", "verification", "review", "general"]);
  const bounded = typeof claimed === "string" ? claimed.trim().toLowerCase() as NativeTaskType : "general";
  return {
    type: typeof value === "string" ? nativeTaskType(value) : allowed.has(bounded) ? bounded : "general",
    terms: nativeTaskTerms(value, claimedTerms),
    continuation: nativeTaskContinuation(value, claimedContinuation),
  };
}

function nativeExactOutput(event: Record<string, unknown>): string | undefined {
  const value = event.tool_output ?? event.tool_response ?? event.output ?? event.result;
  if (value === undefined || nativeDigestObject(value)) return undefined;
  if (typeof value === "string") return Buffer.from(value, "utf8").toString("base64");
  try { return Buffer.from(JSON.stringify(value), "utf8").toString("base64"); } catch { return undefined; }
}

function nativeToolInputState(input: unknown, cwd: string | undefined): string | undefined {
  if (!input || typeof input !== "object" || Array.isArray(input) || !cwd) return undefined;
  const values = input as Record<string, unknown>;
  const rawPath = [values.path, values.file_path, values.file, values.filename]
    .find((value): value is string => typeof value === "string" && value.trim() !== "");
  if (!rawPath || rawPath.includes("\0")) return undefined;
  try {
    const path = resolve(cwd, rawPath);
    const stat = statSync(path);
    const identity = JSON.stringify({
      path: realpathSync(path),
      size: stat.size,
      mtime_ms: stat.mtimeMs,
      mode: stat.mode,
    });
    return `sha256:${createHash("sha256").update(identity).digest("hex")}`;
  } catch {
    return undefined;
  }
}

function nativeToolNeedsRepositoryState(name: string, input: unknown): boolean {
  const lowerName = name.toLowerCase();
  if (lowerName.includes("search") || lowerName.includes("grep") || lowerName === "rg" || lowerName.includes("find")) return true;
  if (!input || typeof input !== "object" || Array.isArray(input)) return false;
  const values = input as Record<string, unknown>;
  const command = typeof values.command === "string" ? values.command : typeof values.cmd === "string" ? values.cmd : "";
  return /(?:^|\s)(?:go\s+test|pytest|test|go\s+build|build)(?:\s|$)/.test(command);
}

function nativeGitDir(cwd: string): string | undefined {
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

// Repository-wide reuse needs current worktree + index identity. Any oversized
// or unreadable changed file returns no identity, suppressing reuse instead of
// claiming stale currentness.
function nativeRepositoryState(cwd: string | undefined): string | undefined {
  if (!cwd) return undefined;
  try {
    const status = execFileSync("git", ["-C", cwd, "status", "--porcelain=v1", "-z", "--branch", "--untracked-files=all"], {
      timeout: 100,
      maxBuffer: 8 * 1024 * 1024,
      encoding: "buffer",
      stdio: ["ignore", "pipe", "ignore"],
    }) as Buffer;
    const gitDir = nativeGitDir(cwd);
    if (!gitDir) return undefined;
    const hash = createHash("sha256");
    hash.update(realpathSync(cwd));
    hash.update("\0status\0");
    hash.update(status);
    let bytes = 0;
    const includeFile = (label: string, path: string) => {
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
    if (!includeFile("index", join(gitDir, "index"))) return undefined;
    const fields = status.toString("utf8").split("\0").filter(Boolean);
    for (const field of fields) {
      if (field.startsWith("## ")) continue;
      const relative = field.length >= 4 && field[2] === " " ? field.slice(3) : field;
      if (!relative || !includeFile("changed", resolve(cwd, relative))) return undefined;
    }
    return `git:sha256:${hash.digest("hex")}`;
  } catch {
    return undefined;
  }
}

function nativeRuntimeRequest(agent: NativeAgent, normalizedEvent: string, sessionId: string, event: Record<string, unknown>) {
  const protocolEvent = NATIVE_PROTOCOL_EVENTS[normalizedEvent];
  if (!protocolEvent) return undefined;
  const sessionCwd = boundedHookString(event.cwd ?? event.working_directory ?? event.workingDirectory ?? process.cwd(), 4096);
  const claimedRepositoryState = boundedHookString(event.repository_state ?? event.repositoryState, 512);
  const session: Record<string, unknown> = {
    caveman_session_id: `${agent}:${sessionId}`,
    host_session_id: sessionId,
    parent_session_id: boundedHookString(event.parent_session_id ?? event.parentSessionId),
    cwd: sessionCwd,
    repository_state: claimedRepositoryState,
  };
  const request: Record<string, unknown> = {
    protocol_version: 1,
    policy_mode: nativePolicyMode(),
    profile: nativeProfile(),
    agent: {
      id: agent,
      version: boundedHookString(event.agent_version ?? event.version),
      surface: boundedHookString(event.surface ?? event.platform),
    },
    session,
    event: { type: protocolEvent, timestamp_ms: Date.now() },
  };
  const eventModel = event.model && typeof event.model === "object" && !Array.isArray(event.model)
    ? event.model as Record<string, unknown>
    : undefined;
  const model = boundedHookString(
    typeof event.model === "string" ? event.model : eventModel?.model ?? eventModel?.id ?? event.model_id ?? event.modelId ?? event.model_name ?? event.modelName,
    512,
  );
  const provider = boundedHookString(eventModel?.provider ?? event.provider ?? event.provider_id ?? event.providerId, 128);
  if (model || provider) request.model = { provider, model };
  const promptValue = event.prompt ?? event.user_prompt ?? event.userMessage;
  const prompt = nativePayloadDigest(promptValue);
  if (prompt) {
    request.prompt = prompt;
    request.task_profile = nativeTaskProfile(
      promptValue,
      event.task_type ?? event.taskType,
      event.task_terms ?? event.taskTerms,
      event.task_continuation ?? event.taskContinuation,
    );
  }
  const toolName = boundedHookString(event.tool_name ?? event.toolName);
  if (toolName) {
    const tool: Record<string, unknown> = { name: toolName };
    const input = event.tool_input ?? event.toolInput ?? event.args;
    if (input !== undefined) {
      tool.input = input;
      const inputState = nativeToolInputState(input, sessionCwd);
      if (inputState) tool.input_state = inputState;
      if (nativeToolNeedsRepositoryState(toolName, input)) {
        session.repository_state = nativeRepositoryState(sessionCwd) ?? claimedRepositoryState;
      }
    }
    const output = nativeExactOutput(event);
    if (output) tool.output = output;
    request.tool = tool;
  }
  return request;
}

function callNativeRuntime(request: Record<string, unknown>): Promise<NativeRuntimeResponse | undefined> {
  return new Promise((settle) => {
    let settled = false;
    let bytes = 0;
    const chunks: Buffer[] = [];
    const finish = (value?: NativeRuntimeResponse) => {
      if (settled) return;
      settled = true;
      socket.destroy();
      settle(value);
    };
    const endpoint = process.platform === "win32"
      ? `\\\\.\\pipe\\caveman-native-${createHash("sha256").update(resolve(cavemanHome()).replaceAll("/", "\\").toLowerCase()).digest("hex").slice(0, 16)}`
      : join(cavemanHome(), "run", "native.sock");
    const socket = netConnect({ path: endpoint });
    socket.setTimeout(250);
    socket.on("connect", () => socket.end(JSON.stringify(request) + "\n"));
    socket.on("data", (chunk: Buffer) => {
      bytes += chunk.length;
      if (bytes > 2 * 1024 * 1024) return finish();
      chunks.push(chunk);
    });
    socket.on("end", () => {
      try {
        const parsed = JSON.parse(Buffer.concat(chunks).toString("utf8"));
        if (validNativeRuntimeResponse(parsed)) finish(parsed);
        else finish();
      } catch { finish(); }
    });
    socket.on("timeout", () => finish());
    socket.on("error", () => finish());
  });
}

function recordNativeFallback(entry: Record<string, unknown>) {
  try {
    const path = join(cavemanHome(), "runtime", "native-events.jsonl");
    mkdirSync(dirname(path), { recursive: true, mode: 0o700 });
    appendFileSync(path, JSON.stringify(entry) + "\n", { mode: 0o600 });
    chmodSync(path, 0o600);
  } catch {
    // Hooks remain fail-open when local fallback persistence is unavailable.
  }
}

function nativeSessionKey(): Buffer | undefined {
  const path = join(cavemanHome(), "runtime", "session.key");
  const readCurrent = () => {
    try {
      const key = readFileSync(path);
      if (key.length !== 32) return undefined;
      chmodSync(path, 0o600);
      return key;
    } catch { return undefined; }
  };
  const current = readCurrent();
  if (current) return current;
  try {
    mkdirSync(dirname(path), { recursive: true, mode: 0o700 });
    chmodSync(dirname(path), 0o700);
    const key = randomBytes(32);
    writeFileSync(path, key, { flag: "wx", mode: 0o600 });
    return key;
  } catch (error) {
    if ((error as NodeJS.ErrnoException).code === "EEXIST") return readCurrent();
    return undefined;
  }
}

function nativeSessionMarker(agent: NativeAgent, sessionId: string): string | undefined {
  if (wrapMode(gatewayURL()) !== "local") return undefined;
  const cavemanSessionId = `${agent}:${sessionId}`;
  if (Buffer.byteLength(cavemanSessionId) > 256) return undefined;
  const key = nativeSessionKey();
  if (!key) return undefined;
  const encoded = Buffer.from(cavemanSessionId, "utf8").toString("base64url");
  const signature = createHmac("sha256", key).update(`caveman-session-v1\0${encoded}`).digest("hex");
  return `[[caveman-session-v1 sid="${encoded}" sig="${signature}"]]`;
}

function readNativeReceipts(): NativeReceipt[] {
  const dir = join(cavemanHome(), "receipts");
  let names: string[];
  try { names = readdirSync(dir).filter((name) => /^session-[0-9a-f]+\.json$/.test(name)); } catch { return []; }
  const receipts: NativeReceipt[] = [];
  for (const name of names) {
    try {
      const value = JSON.parse(readFileSync(join(dir, name), "utf8"));
      if (value?.schema === "caveman.native.receipt.v1" && typeof value.session_id === "string") receipts.push(value as NativeReceipt);
    } catch {
      // One malformed receipt cannot hide other completed sessions.
    }
  }
  return receipts.sort((a, b) => Date.parse(b.generated_at) - Date.parse(a.generated_at));
}

function nativeInspect(argv: string[]) {
  const json = argv.includes("--json");
  const requested = argv.find((arg) => !arg.startsWith("--"));
  const receipts = readNativeReceipts();
  const receipt = requested
    ? receipts.find((item) => item.session_id === requested || item.host_session_id === requested || item.session_id.endsWith(`:${requested}`))
    : receipts[0];
  if (!receipt) {
    console.error(requested ? `No completed Caveman session matches ${requested}.` : "No completed Caveman native session receipt yet.");
    process.exitCode = 1;
    return;
  }
  if (json) {
    print(receipt);
    return;
  }
  const metric = (name: string) => receipt.execution[name];
  const line = (label: string, value: string) => console.log(`  ${label.padEnd(28)} ${value}`);
  console.log(`Caveman session ${receipt.host_session_id ?? receipt.session_id} · ${receipt.agent}`);
  console.log("\nOutcome");
  line("verifier", receipt.outcome.verifier ?? "not_observed");
  line("tests", receipt.outcome.tests ?? "not_observed");
  console.log("\nUsage");
  const usage = receipt.provider_usage;
  if (usage?.status === "correlated") {
    line("session correlation", usage.correlation_basis.startsWith("unique_recent_") ? `${usage.correlation_basis.replaceAll("_", " ")} · approximate` : usage.correlation_basis || "unavailable");
    line("provider requests", `${usage.requests.toLocaleString()} · ${usage.provider_complete_requests.toLocaleString()} provider-complete`);
    line("provider tokens", `${usage.input_tokens.toLocaleString()} input · ${usage.output_tokens.toLocaleString()} output · ${usage.token_usage_coverage}`);
    line("cached input subset", usage.cached_input_tokens.toLocaleString());
    line("catalog list-price subtotal", `$${usage.catalog_list_price_subtotal_usd.toFixed(6)} · not provider invoice`);
    line("local inferred savings", `$${usage.inferred_savings_usd.toFixed(6)} · not verified`);
    if (usage.compression_tokens_before > 0) {
      line("compressed-part tokens", `${usage.compression_tokens_before.toLocaleString()} → ${usage.compression_tokens_after.toLocaleString()} · ${usage.compression_token_count_basis || "basis unavailable"}`);
    }
  } else {
    line("provider tokens and cost", usage?.status ?? "not correlated");
  }
  console.log("\nExecution");
  for (const [label, name] of ([
    ["native events", "native_events"],
    ["repeats detected", "repeated_actions_detected"],
    ["exact context recorded", "exact_context_recorded"],
    ["mask candidate context", "mask_candidate_context"],
  ] as const)) {
    const value = metric(name);
    if (value) line(label, `${value.value.toLocaleString()} ${value.unit} · ${value.basis}`);
  }
  console.log("\nPolicy");
  line("active", (receipt.policy.active ?? []).join(", ") || "none");
  line("unresolved assumptions", String(receipt.policy.unresolved_assumptions ?? 0));
  console.log("\nClaim status");
  line("compression reduction", receipt.claim_status.compression_reduction ?? "not_attested");
  line("task savings", receipt.claim_status.task_savings ?? "not_verified");
}

function nativeWhy(argv: string[]) {
  const json = argv.includes("--json");
  const decisionID = argv.find((arg) => !arg.startsWith("--"));
  if (!decisionID || !/^dec_[0-9a-f]{24}$/.test(decisionID)) {
    console.error(`usage: ${invokedAs()} why <decision-id> [--json]`);
    process.exitCode = 2;
    return;
  }
  let explanation: NativeWhy;
  try {
    const parsed = JSON.parse(proxyExec(["native-why", "--decision", decisionID], process.env, false)) as NativeWhy;
    if (parsed?.schema !== "caveman.native.why.v1" || parsed.decision_id !== decisionID || typeof parsed.input_basis !== "object") {
      throw new Error("invalid Decision Ledger response");
    }
    explanation = parsed;
  } catch (error) {
    if (process.exitCode) return;
    console.error(`caveman why: ${(error as Error).message}`);
    process.exitCode = 1;
    return;
  }
  if (json) {
    print(explanation);
    return;
  }
  const line = (label: string, value: string) => console.log(`  ${label.padEnd(22)} ${value}`);
  console.log(`Decision ${explanation.decision_id}`);
  line("session", explanation.session_id);
  line("action", explanation.action);
  line("reason", explanation.reason);
  line("input basis", JSON.stringify(explanation.input_basis));
  line("alternatives rejected", explanation.alternatives_rejected.join(", ") || "none recorded");
  line("task state", `${explanation.task_state_before} → ${explanation.task_state_after}`);
  line("currentness", explanation.currentness);
  if (explanation.recovery_ref) line("recovery", explanation.recovery_ref);
}

// nativeHook is thin lifecycle glue. It sends normalized events to one local
// runtime; unavailable runtime falls back to bounded metadata only. Raw prompts
// never cross the adapter boundary. Any malformed input/write failure stays
// fail-open and emits no blocking decision.
async function nativeHook(argv: string[]) {
  const agent = argv[0] === "claude" || argv[0] === "codex" || argv[0] === "hermes" || argv[0] === "gemini" || argv[0] === "opencode" ? argv[0] : undefined;
  if (!agent) process.exit(0);
  let raw: Buffer;
  try { raw = await readStdin(); } catch { process.exit(0); }
  if (raw.length > 2 * 1024 * 1024) process.exit(0);
  let event: Record<string, unknown>;
  try {
    const parsed = JSON.parse(raw.toString("utf8") || "{}");
    if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) process.exit(0);
    event = parsed as Record<string, unknown>;
  } catch {
    process.exit(0);
  }
  const rawEventName = boundedHookString(event.hook_event_name ?? event.event_name ?? event.event ?? argv[1]);
  const eventName = agent === "gemini" && rawEventName ? ({
    BeforeAgent: "UserPromptSubmit",
    BeforeTool: "PreToolUse",
    AfterTool: "PostToolUse",
    BeforeModel: "ModelBefore",
    AfterModel: "ModelAfter",
    PreCompress: "PreCompact",
    AfterAgent: "Stop",
  } as Record<string, string>)[rawEventName] ?? rawEventName : rawEventName;
  const normalizedEvent = eventName && NATIVE_EVENT_NAMES.has(eventName) ? eventName : "Unknown";
  const sessionId = boundedHookString(event.session_id ?? event.sessionId);
  const toolName = boundedHookString(event.tool_name ?? event.toolName);
  const cwd = boundedHookString(event.cwd, 4096);
  const entry: Record<string, unknown> = {
    protocol_version: 1,
    recorded_at: new Date().toISOString(),
    agent,
    event: normalizedEvent,
    payload_bytes: raw.length,
    payload_sha256: `sha256:${createHash("sha256").update(raw).digest("hex")}`,
  };
  if (sessionId) entry.host_session_id = sessionId;
  if (toolName) entry.tool_name = toolName;
  if (cwd) entry.cwd_sha256 = `sha256:${createHash("sha256").update(cwd).digest("hex")}`;
  if (normalizedEvent === "SessionStart") {
    try {
      const opts = defaultWrapOptions();
      const gw = gatewayURL();
      const { host, port } = gatewayHostPort(gw);
      if (wrapMode(gw) === "local" && !opts.noProxy && !(await portListening(host, port))) {
        const subscription = agent === "codex" && detectCodexWrapAuthMode() === "subscription";
        const mode = subscription && opts.mode === "pixel" ? "record" : opts.mode;
        const recovery = Boolean(probeMcpBinary()?.probe.current);
        await startWrapProxy(
          mode,
          recovery,
          subscription ? false : opts.toon,
          opts.pixelModels,
          opts.pixelDensity,
          gw,
          subscription ? "codex-subscription" : "standard",
          false,
        );
      }
    } catch {
      // Runtime startup is fail-open; host session and Core still proceed.
    }
  }

  const runtimeRequest = sessionId && normalizedEvent !== "Unknown"
    ? nativeRuntimeRequest(agent, normalizedEvent, sessionId, event)
    : undefined;
  const runtimeResponse = runtimeRequest ? await callNativeRuntime(runtimeRequest) : undefined;
  if (!runtimeResponse) recordNativeFallback(entry);
  if (normalizedEvent === "SessionEnd" && runtimeResponse?.message) {
    process.stderr.write(`${runtimeResponse.message}\n`);
  }

  const policyMode = nativePolicyMode();
  const profile = nativeProfile();
  const coreEnabled = wrapRuntimeConfig().core;
  const runtimeContext = policyMode === "record" || !coreEnabled ? undefined : runtimeResponse?.context;
  const coreContext = policyMode === "record" || profile === "record-only" || !coreEnabled
    ? ""
    : profile === "core-lean-build" ? `${NATIVE_CORE} ${NATIVE_SKILL_INSTRUCTIONS["lean-build"]}` : NATIVE_CORE;
  let marker: string | undefined;
  if (sessionId && runtimeResponse?.decision_id && (normalizedEvent === "SessionStart" || normalizedEvent === "PostCompact")) {
    const opts = defaultWrapOptions();
    const gw = gatewayURL();
    const { host, port } = gatewayHostPort(gw);
    if (wrapMode(gw) === "local" && !opts.noProxy && await portListening(host, port)) {
      marker = nativeSessionMarker(agent, sessionId);
    }
  }
  // Stable Core always precedes dynamic state; marker stays last and proxy strips
  // it before provider forwarding. Generated structure/order are byte-stable.
  const stableContext = [coreContext, marker].filter(Boolean).join("\n");
  const compactContext = [coreContext, runtimeContext, marker].filter(Boolean).join("\n");
  if (normalizedEvent === "SessionStart" && agent !== "hermes" && stableContext) {
    process.stdout.write(JSON.stringify({
      hookSpecificOutput: { hookEventName: normalizedEvent, additionalContext: stableContext },
    }));
  } else if (normalizedEvent === "PostCompact" && agent !== "hermes" && compactContext) {
    process.stdout.write(JSON.stringify({
      hookSpecificOutput: { hookEventName: normalizedEvent, additionalContext: compactContext },
    }));
  } else if (agent === "hermes" && normalizedEvent === "UserPromptSubmit" && compactContext) {
    process.stdout.write(JSON.stringify({ context: compactContext }));
  } else if (runtimeContext && normalizedEvent === "UserPromptSubmit") {
    process.stdout.write(JSON.stringify({
      hookSpecificOutput: { hookEventName: normalizedEvent, additionalContext: runtimeContext },
    }));
  } else if (runtimeContext && (normalizedEvent === "PreToolUse" || normalizedEvent === "PermissionRequest")) {
    process.stdout.write(JSON.stringify({
      hookSpecificOutput: { hookEventName: normalizedEvent, additionalContext: runtimeContext },
    }));
  } else if (policyMode !== "record" && agent === "claude" && normalizedEvent === "PostToolUse" && runtimeResponse?.output_replacement) {
    // Claude Code 2.1.226 supports updatedToolOutput for every tool. Other hosts
    // stay unchanged until their output-rewrite contract is locally proven.
    process.stdout.write(JSON.stringify({
      hookSpecificOutput: { hookEventName: "PostToolUse", updatedToolOutput: runtimeResponse.output_replacement },
    }));
  } else if (policyMode !== "record" && agent === "opencode" && normalizedEvent === "PostToolUse" && runtimeResponse) {
    process.stdout.write(JSON.stringify(runtimeResponse));
  }
}

function claudeSettingsPath(): string {
  return join(homedir(), ".claude", "settings.json");
}
function geminiSettingsPath(): string {
  return join(homedir(), ".gemini", "settings.json");
}
function codexHooksPath(): string {
  return join(homedir(), ".codex", "hooks.json");
}

// installSettingsHook registers a command-output shrink hook in a settings.json that
// follows the Claude/Gemini shape — root.hooks[<event>] is an array of
// { matcher, hooks: [{ type: "command", command }] }. It serves both Claude Code
// (PreToolUse / "Bash") and Gemini CLI (BeforeTool / "run_shell_command"): same file
// shape, different event + matcher. Idempotent (never duplicates the caveman hook),
// never touches the user's other hooks, and refuses to corrupt a non-object file.
function installSettingsHook(path: string, event: string, matcher: string): boolean {
  return installSettingsHookGeneric(path, event, matcher, `${cavemanBinForHook()} shrink-hook`, shrinkHookEntry);
}

// installSettingsHookGeneric is the shared Claude/Gemini settings.json writer:
// root.hooks[<event>] is an array of { matcher?, hooks: [{ type:"command", command }] }.
// A `matcher` is included only when defined (tool-scoped events like PreToolUse);
// prompt-scoped events like UserPromptSubmit carry none. `matches` recognizes an
// existing caveman entry so installs stay idempotent and never touch the user's
// other hooks. Refuses to corrupt a non-object file.
function installSettingsHookGeneric(
  path: string,
  event: string,
  matcher: string | undefined,
  command: string,
  matches: (entry: Record<string, unknown>) => boolean,
): boolean {
  let root: Record<string, unknown> = {};
  try {
    const rawText = readFileSync(path, "utf8").trim();
    if (rawText) {
      const parsed = JSON.parse(rawText);
      if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) {
        console.error(`${mark("warn")} ${path} is not a JSON object; not modifying it`);
        return false;
      }
      root = parsed as Record<string, unknown>;
    }
  } catch (e) {
    if ((e as NodeJS.ErrnoException).code !== "ENOENT") {
      console.error(`${mark("warn")} cannot read ${path}: ${(e as Error).message}; not modifying it`);
      return false;
    }
  }
  const hooks = (root.hooks && typeof root.hooks === "object" && !Array.isArray(root.hooks))
    ? (root.hooks as Record<string, unknown>) : {};
  const list = Array.isArray(hooks[event]) ? (hooks[event] as Array<Record<string, unknown>>) : [];
  if (!list.some((e) => matches(e))) {
    list.push(matcher !== undefined
      ? { matcher, hooks: [{ type: "command", command }] }
      : { hooks: [{ type: "command", command }] });
  }
  hooks[event] = list;
  root.hooks = hooks;
  try {
    mkdirSync(dirname(path), { recursive: true });
    writeFileSync(path, JSON.stringify(root, null, 2) + "\n");
    return true;
  } catch (e) {
    console.error(`${mark("warn")} cannot write ${path}: ${(e as Error).message}`);
    return false;
  }
}

// shrinkHookEntry recognizes the caveman command-output hook inside a settings hook
// entry (any handler whose command invokes `shrink-hook`).
function shrinkHookEntry(entry: Record<string, unknown>): boolean {
  const hs = (entry as { hooks?: unknown }).hooks;
  return Array.isArray(hs) && hs.some((h) => typeof (h as { command?: unknown }).command === "string" && ((h as { command: string }).command).includes("shrink-hook"));
}

function removeSettingsHook(path: string, event: string): boolean {
  return removeSettingsHookGeneric(path, event, shrinkHookEntry);
}

function removeSettingsHookGeneric(
  path: string,
  event: string,
  matches: (entry: Record<string, unknown>) => boolean,
): boolean {
  let root: Record<string, unknown>;
  try {
    const parsed = JSON.parse(readFileSync(path, "utf8"));
    if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) return false;
    root = parsed as Record<string, unknown>;
  } catch {
    return false; // missing/unreadable → nothing to remove
  }
  const hooks = root.hooks as Record<string, unknown> | undefined;
  if (!hooks || !Array.isArray(hooks[event])) return false;
  const list = hooks[event] as Array<Record<string, unknown>>;
  const kept = list.filter((e) => !matches(e));
  if (kept.length === list.length) return false; // nothing removed
  hooks[event] = kept;
  try {
    writeFileSync(path, JSON.stringify(root, null, 2) + "\n");
    return true;
  } catch {
    return false;
  }
}

function installShrinkHookClaude(): boolean { return installSettingsHook(claudeSettingsPath(), "PreToolUse", "Bash"); }
function removeShrinkHookClaude(): boolean { return removeSettingsHook(claudeSettingsPath(), "PreToolUse"); }
function installShrinkHookCodex(): boolean {
  // No matcher: Codex local-function tool names can vary by surface/version;
  // shrinkHook filters known shell tools and silently passes every other tool.
  return installSettingsHookGeneric(codexHooksPath(), "PreToolUse", undefined, `${cavemanBinForHook()} shrink-hook`, shrinkHookEntry);
}
function removeShrinkHookCodex(): boolean { return removeSettingsHook(codexHooksPath(), "PreToolUse"); }
function installShrinkHookGemini(): boolean { return installSettingsHook(geminiSettingsPath(), "BeforeTool", "run_shell_command"); }
function removeShrinkHookGemini(): boolean { return removeSettingsHook(geminiSettingsPath(), "BeforeTool"); }

// expandTilde resolves a profile's "~/…" instructions-file path to an absolute one
// (homedir() honors $HOME, so tests can redirect it).
function expandTilde(p: string): string {
  return p === "~" ? homedir() : p.startsWith("~/") ? join(homedir(), p.slice(2)) : p;
}

// commandHookKind classifies an agent's command-output hook surface so callers can
// speak about it honestly: "hard" = a deterministic pre-exec command rewrite (RTK
// parity), "soft" = a model nudge in an auto-read instructions file (best-effort,
// not a rewrite), "none" = no surface we can install (manual `caveman shrink` only).
function commandHookKind(a: AgentProfile): "hard" | "soft" | "none" {
  const ch = a.command_hook;
  if (!ch) return "none";
  return ch.method === "instruction-note" ? "soft" : "hard";
}

function instructionFileForAgent(a: AgentProfile): string | undefined {
  const hook = a.command_hook;
  return hook && "file" in hook ? hook.file : undefined;
}
// (hard methods: claude-pretooluse, codex-pretooluse, opencode-plugin, gemini-beforetool, hermes-plugin, openclaw-plugin.)

// installShrinkHookForAgent installs the command-output compression hook using the
// mechanism the agent's profile declares. Pure (no stdout) — the caller reports the
// outcome with the right honesty (hard rewrite vs soft nudge). Returns false when the
// agent has no installable surface (commandHookKind "none").
function installShrinkHookForAgent(a: AgentProfile): boolean {
  const ch = a.command_hook;
  if (!ch) return false;
  switch (ch.method) {
    case "claude-pretooluse":
      return installShrinkHookClaude();
    case "codex-pretooluse":
      return installShrinkHookCodex();
    case "gemini-beforetool":
      return installShrinkHookGemini();
    case "opencode-plugin":
      return installShrinkPluginOpencode();
    case "hermes-plugin":
      return installShrinkPluginHermes();
    case "openclaw-plugin":
      return installShrinkPluginOpenClaw();
    case "instruction-note":
      return installShrinkNote(ch.file);
  }
}

function removeShrinkHookForAgent(a: AgentProfile): boolean {
  const ch = a.command_hook;
  if (!ch) return false;
  switch (ch.method) {
    case "claude-pretooluse":
      return removeShrinkHookClaude();
    case "codex-pretooluse":
      return removeShrinkHookCodex();
    case "gemini-beforetool":
      return removeShrinkHookGemini();
    case "opencode-plugin":
      return removeShrinkPluginOpencode();
    case "hermes-plugin":
      return removeShrinkPluginHermes();
    case "openclaw-plugin":
      return removeShrinkPluginOpenClaw();
    case "instruction-note":
      return removeShrinkNote(ch.file);
  }
}

// ── opt-in auto-recall hook (cavemem) ───────────────────────────────────────
// Off by default. The default loop is the editing skill's pointer + agent-driven
// `caveman mem recall`. This hook is an explicit upgrade the user enables, and
// every injection it makes is disclosed and priced. Claude Code is the only agent
// today with a verified live-user-prompt hook (UserPromptSubmit); others fail
// closed (no memory_hook → no surface) and rely on the skill instead.

// recallHookEntry recognizes the caveman auto-recall hook inside a settings entry
// (any handler whose command invokes `mem recall-hook`). Distinct from the shrink
// recognizer so the two hooks never collide.
function recallHookEntry(entry: Record<string, unknown>): boolean {
  const hs = (entry as { hooks?: unknown }).hooks;
  return Array.isArray(hs) && hs.some((h) => typeof (h as { command?: unknown }).command === "string" && ((h as { command: string }).command).includes("mem recall-hook"));
}

function installRecallHookClaude(): boolean {
  return installSettingsHookGeneric(claudeSettingsPath(), "UserPromptSubmit", undefined, `${cavemanBinForHook()} mem recall-hook`, recallHookEntry);
}
function removeRecallHookClaude(): boolean {
  return removeSettingsHookGeneric(claudeSettingsPath(), "UserPromptSubmit", recallHookEntry);
}

// memoryHookKind: "hard" = a deterministic live-prompt recall injection, "none" =
// no surface (the honest ceiling — the agent uses the skill + `caveman mem recall`).
function memoryHookKind(a: AgentProfile): "hard" | "none" {
  return a.memory_hook ? "hard" : "none";
}

function installRecallHookForAgent(a: AgentProfile): boolean {
  const mh = a.memory_hook;
  if (!mh) return false;
  switch (mh.method) {
    case "claude-userpromptsubmit":
      return installRecallHookClaude();
  }
}
function removeRecallHookForAgent(a: AgentProfile): boolean {
  const mh = a.memory_hook;
  if (!mh) return false;
  switch (mh.method) {
    case "claude-userpromptsubmit":
      return removeRecallHookClaude();
  }
}

function recallHookMarkerPath(agentId: string): string {
  return join(cavemanHome(), "recall-hooks", `${agentId}.json`);
}
function recallHookInstalled(agentId: string): boolean {
  try { return statSync(recallHookMarkerPath(agentId)).isFile(); } catch { return false; }
}
function writeRecallHookMarker(agentId: string) {
  const p = recallHookMarkerPath(agentId);
  mkdirSync(dirname(p), { recursive: true });
  writeFileSync(p, JSON.stringify({ installed: true }) + "\n");
}

// memRecallHook is the UserPromptSubmit callback: it does a conservative lexical
// cavemem recall against the LIVE user prompt and injects the above-threshold hits
// (already compressed by cavemem) as additionalContext, each disclosed + priced.
// Fail-open by construction: any problem → exit 0 with no output (never blocks the
// agent, never injects a guess).
async function memRecallHook() {
  let raw: Buffer;
  try { raw = await readStdin(); } catch { process.exit(0); }
  let evt: { prompt?: string };
  try { evt = JSON.parse(raw.toString("utf8") || "{}"); } catch { process.exit(0); }
  const prompt = typeof evt.prompt === "string" ? evt.prompt.trim() : "";
  if (!prompt) process.exit(0);
  const out = cavememRun(["recall", prompt, "3"], { soft: true });
  if (!out) process.exit(0);
  let parsed: { hits?: Array<{ text?: string; tokens_added?: number; recovery_handle?: string }> };
  try { parsed = JSON.parse(out); } catch { process.exit(0); }
  const hits = Array.isArray(parsed.hits) ? parsed.hits : [];
  if (hits.length === 0) process.exit(0);
  const blocks = hits.map((h) => {
    const tokens = typeof h.tokens_added === "number" ? h.tokens_added : 0;
    const recover = h.recovery_handle ? ` · recover: caveman mem recover ${h.recovery_handle}` : "";
    return `[cavemem recall · +${tokens} tokens · basis inferred${recover}]\n${h.text ?? ""}`;
  });
  const additionalContext = "Relevant memories recalled by cavemem (compressed; cost disclosed):\n\n" + blocks.join("\n\n");
  process.stdout.write(JSON.stringify({ hookSpecificOutput: { hookEventName: "UserPromptSubmit", additionalContext } }));
}

// memHook installs/uninstalls the opt-in auto-recall hook (mirrors hooksCmd). With
// no agent it targets every agent that has a memory_hook AND is on PATH.
function memHook(rest: string[]) {
  const sub = rest[0];
  if (sub !== "install" && sub !== "uninstall") {
    console.error(`usage: ${invokedCommand("mem")} hook install|uninstall [agent]`);
    console.error("  opt in to auto-recall: inject a conservative, priced cavemem recall on each prompt (off by default)");
    process.exit(2);
  }
  const target = rest[1];
  let targets: AgentProfile[];
  if (target) {
    const a = findAgent(target);
    if (!a) { console.error(`unknown agent '${target}'. known: ${AGENTS.map((x) => x.id).join(", ")}`); process.exit(2); }
    targets = [a];
  } else {
    targets = AGENTS.filter((a) => memoryHookKind(a) !== "none" && which(binOf(a)));
    if (targets.length === 0) {
      console.error("no agent with an auto-recall surface detected on PATH; pass an agent id, e.g. `caveman mem hook install claude`");
      process.exit(1);
    }
  }
  if (sub === "install") {
    for (const a of targets) {
      if (memoryHookKind(a) === "none") {
        process.stderr.write(`${mark("warn")} ${a.display_name}: no auto-recall surface — use the editing skill + ${cyan("caveman mem recall")}\n`);
        continue;
      }
      if (installRecallHookForAgent(a)) {
        writeRecallHookMarker(a.id);
        process.stderr.write(`${mark("ok")} ${a.display_name}: auto-recall hook installed — each prompt gets a conservative, priced cavemem recall\n`);
      }
    }
    process.stderr.write(dim("→ opt-in auto-recall is on; every injection discloses its token cost. Remove with `caveman mem hook uninstall`.\n"));
    return;
  }
  for (const a of targets) {
    const removed = removeRecallHookForAgent(a);
    try { unlinkSync(recallHookMarkerPath(a.id)); } catch { /* no marker — fine */ }
    process.stderr.write(`${mark(removed ? "ok" : "warn")} ${a.display_name}: ${removed ? "auto-recall hook removed" : "no caveman recall hook found"}\n`);
  }
}

// hookInstalledPhrase is the one-line, honest description of what `hooks install`
// just did for an agent — distinguishing the hard rewrite from the soft nudge so we
// never overstate the instruction-note path.
function hookInstalledPhrase(a: AgentProfile): string {
  const ch = a.command_hook;
  if (ch?.method === "instruction-note") return `shrink preference added to ${ch.file} ${dim("(a model nudge, not a hard rewrite)")}`;
  return "command-output rewrite hook installed";
}

// ── soft tier: instruction-note ──────────────────────────────────────────────
// For agents with no deterministic command-rewrite surface, we append a
// clearly-delimited note to a file the agent auto-reads as model instructions,
// asking the model to prefer `caveman shrink -- <cmd>` for noisy reads. It is a
// best-effort nudge, not a guaranteed rewrite — and it is honest about that.
const NOTE_BEGIN = "<!-- caveman:shrink-hook (managed by `caveman hooks`) -->";
const NOTE_END = "<!-- /caveman:shrink-hook -->";
function shrinkNoteBlock(): string {
  return [
    NOTE_BEGIN,
    "When running noisy, finite, read-only shell commands (git status/diff/log, build",
    "and test output, grep/rg, find, ls/tree, docker/kubectl reads), prefer running them",
    "as `caveman shrink -- <command>`. It runs the command and compresses the output",
    "byte-exactly; recover the full text with `caveman retrieve <handle>`. Skip it for",
    "commands with pipes/redirects, interactive or streaming commands (-f/--watch/--follow),",
    "and editor-opening commands (git commit, git rebase).",
    NOTE_END,
  ].join("\n");
}
// reportCorruptManagedBlock: a begin marker without its end marker (or vice
// versa) means the user's file was hand-edited inside a caveman-managed seam.
// The ONLY safe move is to touch nothing and say so loudly (review C8: the old
// "repair" stripped from the begin marker to end-of-file, which deleted
// sibling managed blocks AND the user's own content below it, then printed a
// success mark). Non-zero exit is the caller's job via the false/"failed"
// return; this prints the instructions.
function reportCorruptManagedBlock(path: string, begin: string, end: string): void {
  console.error(`${mark("warn")} ${path}: a caveman-managed block is corrupted (its begin/end markers do not pair up).`);
  console.error(`  Nothing was modified. Fix it by hand: make sure '${begin}'`);
  console.error(`  is closed by '${end}' (or delete the partial block), then re-run.`);
}
// MANAGED_SENTINEL is the shared prefix of EVERY begin marker on this managed-
// block mechanism (`<!-- caveman:shrink-hook …`, `<!-- caveman:directive:<id> …`;
// end markers start `<!-- /caveman:` and never match it). End-marker searches
// are BOUNDED at the next begin marker: an end marker that sits past another
// block's begin means the blocks are interleaved (hand-edited), and stripping
// through it would silently delete the sibling block's begin marker and body
// (closure B1). The only safe move is to refuse loudly and touch nothing.
const MANAGED_SENTINEL = "<!-- caveman:";
// installManagedBlock appends one delimited managed block to an instructions file.
// Idempotent (a second install with a complete block is a no-op), creates the
// file/dir if missing, and never disturbs the user's existing content — including
// OTHER caveman-managed blocks (each marker namespace owns only its own seam). A
// corrupted half-block (begin with no end, or end with no begin) FAILS LOUDLY
// and modifies nothing — never a silent "repair" that eats user content.
function installManagedBlock(file: string, begin: string, end: string, block: string): boolean {
  const path = expandTilde(file);
  let existing = "";
  try {
    existing = readFileSync(path, "utf8");
  } catch (e) {
    if ((e as NodeJS.ErrnoException).code !== "ENOENT") {
      console.error(`${mark("warn")} cannot read ${path}: ${(e as Error).message}; not modifying it`);
      return false;
    }
  }
  const beginAt = existing.indexOf(begin);
  if (beginAt !== -1) {
    const endAt = existing.indexOf(end, beginAt + begin.length);
    const nextBegin = existing.indexOf(MANAGED_SENTINEL, beginAt + begin.length);
    // Complete only when the end marker exists AND sits before any sibling
    // block's begin marker — an end past another begin is interleaved (B1).
    if (endAt !== -1 && (nextBegin === -1 || endAt < nextBegin)) return true; // complete block present
    reportCorruptManagedBlock(path, begin, end);
    return false;
  }
  if (existing.includes(end)) {
    reportCorruptManagedBlock(path, begin, end);
    return false;
  }
  const sep = existing === "" ? "" : existing.endsWith("\n") ? "\n" : "\n\n";
  try {
    mkdirSync(dirname(path), { recursive: true });
    writeFileSync(path, existing + sep + block + "\n");
    return true;
  } catch (e) {
    console.error(`${mark("warn")} cannot write ${path}: ${(e as Error).message}`);
    return false;
  }
}
// removeManagedBlock strips only the named block (and the single blank-line
// separator install inserted before it), restoring the user's content — and its
// original trailing newline — exactly. Only the seam is touched; user content and
// other managed blocks are never reflowed. EVERY occurrence of the block is
// removed (a duplicated block must not leave a survivor behind a green check).
// A corrupted half-block (begin without its end marker) returns "corrupt"
// after a loud message and modifies NOTHING — the old strip-to-EOF fallback
// deleted sibling managed blocks and user content (review C8).
function removeManagedBlock(file: string, begin: string, endMark: string): "removed" | "absent" | "corrupt" | "failed" {
  const path = expandTilde(file);
  let text: string;
  try {
    text = readFileSync(path, "utf8");
  } catch {
    return "absent"; // missing/unreadable → nothing to remove
  }
  let out = text;
  let removed = 0;
  for (;;) {
    const start = out.indexOf(begin);
    if (start === -1) break;
    const endMarker = out.indexOf(endMark, start + begin.length);
    // Bound the end-marker search at the next managed-block begin marker: an
    // end marker missing OR sitting past another block's begin means the seams
    // are interleaved, and stripping through it would eat the sibling block's
    // begin marker and body behind a green check (closure B1). Refuse loudly.
    const nextBegin = out.indexOf(MANAGED_SENTINEL, start + begin.length);
    if (endMarker === -1 || (nextBegin !== -1 && nextBegin < endMarker)) {
      reportCorruptManagedBlock(path, begin, endMark);
      return "corrupt";
    }
    const end = endMarker + endMark.length;
    const before = out.slice(0, start).replace(/\n\n$/, "\n"); // undo the blank-line separator
    const after = out.slice(end).replace(/^\n/, "");           // drop the block's own trailing newline
    out = before + after;
    removed++;
  }
  // A stray END marker with no begin is the same hand-edited corruption:
  // install refuses this state, so a remove that reports "absent" (exit 0)
  // would lock the user out of both verbs permanently (closure B1). Refuse.
  if (out.includes(endMark)) {
    reportCorruptManagedBlock(path, begin, endMark);
    return "corrupt";
  }
  if (removed === 0) return "absent";
  try {
    writeFileSync(path, out);
    return "removed";
  } catch {
    return "failed";
  }
}

// installShrinkNote / removeShrinkNote: the original shrink-note surface, now a
// thin wrapper over the shared managed-block mechanism (same bytes, same seams).
function installShrinkNote(file: string): boolean {
  return installManagedBlock(file, NOTE_BEGIN, NOTE_END, shrinkNoteBlock());
}
function removeShrinkNote(file: string): boolean {
  const res = removeManagedBlock(file, NOTE_BEGIN, NOTE_END);
  if (res === "corrupt" || res === "failed") process.exitCode = 1;
  return res === "removed";
}

// ── wrap directives (AUTOPILOT_SPEC §8.4/§7.3, slice D) ─────────────────────
// A directive is a SECOND marker namespace on the same instructions-file
// mechanism: a suggest-only model nudge a human installs after the Cave Plan
// proposes it. Distinct begin/end delimiters per directive id, so the shrink
// note and each directive coexist and each removal touches only its own block.
// Honesty rules baked into the text: no dollar claims, no effect percentages —
// the only evidence a directive ever earns is a token delta on measured turns,
// synced as `inferred`, never verified.
function directiveBegin(id: string): string {
  return `<!-- caveman:directive:${id} (managed by \`caveman hooks\`) -->`;
}
function directiveEnd(id: string): string {
  return `<!-- /caveman:directive:${id} -->`;
}
const DIRECTIVES: Record<string, { summary: string; lines: string[] }> = {
  "exploration-offload-directive": {
    summary: "delegate broad read-only exploration to an isolated explorer",
    lines: [
      "Directive: isolate repository exploration.",
      "For broad read-only exploration (finding where something lives, sweeping many",
      "files with Read/Glob/Grep), delegate to an isolated read-only explorer (a",
      "subagent or worker) and have it return compact path:line citations only — not",
      "full file dumps. Keep whole-file reads in the main context only when the exact",
      "contents are needed for the edit at hand. Skip this when the task already names",
      "the exact file or symbol.",
    ],
  },
  "deferred-tool-loading": {
    summary: "load tool descriptions on demand instead of the full catalog",
    lines: [
      "Directive: defer tool loading.",
      "Do not pre-declare every available tool on every request. Keep the task's core",
      "tool set declared, and load other tools' full descriptions through a",
      "search-then-load surface (e.g. tool search) only when a task needs them.",
    ],
  },
};
function directiveBlock(id: string): string {
  // Object.hasOwn, not a truthiness read: id is user input, and a bare
  // DIRECTIVES[id] would resolve prototype keys ('constructor', '__proto__')
  // to non-directive objects (review C8).
  const d = Object.hasOwn(DIRECTIVES, id) ? DIRECTIVES[id] : undefined;
  if (!d) throw new Error(`unknown directive '${id}'`); // callers validate first; fail closed anyway
  return [directiveBegin(id), ...d.lines, directiveEnd(id)].join("\n");
}
function installDirectiveNote(file: string, id: string): boolean {
  return installManagedBlock(file, directiveBegin(id), directiveEnd(id), directiveBlock(id));
}
function removeDirectiveNote(file: string, id: string): "removed" | "absent" | "corrupt" | "failed" {
  return removeManagedBlock(file, directiveBegin(id), directiveEnd(id));
}

// ── hard tier: opencode plugin ───────────────────────────────────────────────
// opencode exposes a real pre-exec command rewrite: a plugin's `tool.execute.before`
// hook can mutate `output.args.command` for the bash tool before it runs (the docs'
// own example does exactly this). We ship a small plugin that routes a noisy command
// through `caveman shrink` — reusing this very CLI's `shrink-hook` decision so the
// allowlist/skip rules stay in one place. Byte-safe: any failure leaves the command
// unchanged. (opencode's hook does not fire for subagent/MCP tool calls — sst/opencode
// #5894/#2319 — so the soft note remains a useful complement there.)
function opencodePluginPath(): string {
  // opencode auto-loads global plugins from ~/.config/opencode/plugins/ (PLURAL — the
  // documented path; a file under the wrong dir is silently ignored, which would make
  // this a fake hook). See https://opencode.ai/docs/plugins.
  return join(homedir(), ".config", "opencode", "plugins", "caveman-shrink.js");
}
// cavemanInvocation returns how to call back into THIS CLI from a generated plugin,
// baked at install time so it is independent of the agent's PATH: a resolved
// caveman/cave binary, else this script under node.
export function generatedPluginInvocation(
  onPath: string | undefined,
  currentScript: string,
  platform: NodeJS.Platform = process.platform,
): { cmd: string; pre: string[] } {
  if (onPath) {
    const invocation = portableInvocation(onPath, [], platform);
    return { cmd: invocation.command, pre: invocation.args };
  }
  return { cmd: process.execPath, pre: [currentScript] };
}

function cavemanInvocation(): { cmd: string; pre: string[] } {
  return generatedPluginInvocation(
    which("caveman") ?? which("cave") ?? undefined,
    process.argv[1] ?? "",
  );
}
function opencodePluginSource(): string {
  const { cmd, pre } = cavemanInvocation();
  const argv = JSON.stringify([...pre, "shrink-hook"]);
  return `// caveman:shrink-plugin — GENERATED by \`caveman hooks install opencode\`.
// Routes opencode's noisy bash output through \`caveman shrink\` (byte-exact, recover
// with \`caveman retrieve\`). Remove with \`caveman hooks uninstall opencode\`.
import { execFileSync } from "node:child_process";

export const CavemanShrink = async () => ({
  "tool.execute.before": async (input, output) => {
    if (input?.tool !== "bash") return;
    const command = output?.args?.command;
    if (typeof command !== "string" || !command.trim()) return;
    try {
      const res = execFileSync(${JSON.stringify(cmd)}, ${argv}, {
        input: JSON.stringify({ tool_name: "Bash", tool_input: { command } }),
        encoding: "utf8",
      });
      if (!res) return;
      const rewritten = JSON.parse(res)?.hookSpecificOutput?.updatedInput?.command;
      if (typeof rewritten === "string" && rewritten) output.args.command = rewritten;
    } catch {
      // byte-safe: any failure leaves the original command untouched.
    }
  },
});
`;
}
function installShrinkPluginOpencode(): boolean {
  const path = opencodePluginPath();
  try {
    mkdirSync(dirname(path), { recursive: true });
    writeFileSync(path, opencodePluginSource());
    return true;
  } catch (e) {
    console.error(`${mark("warn")} cannot write ${path}: ${(e as Error).message}`);
    return false;
  }
}
function removeShrinkPluginOpencode(): boolean {
  const path = opencodePluginPath();
  try {
    if (!readFileSync(path, "utf8").includes("caveman:shrink-plugin")) return false; // not ours — leave it
    unlinkSync(path);
    return true;
  } catch {
    return false;
  }
}

// ── hard tier: Hermes plugin ─────────────────────────────────────────────────
function hermesPluginDir(): string {
  return join(hermesHome(), "plugins", HERMES_PLUGIN_NAME);
}

function hermesPluginManifestPath(): string {
  return join(hermesPluginDir(), "plugin.yaml");
}

function hermesPluginInitPath(): string {
  return join(hermesPluginDir(), "__init__.py");
}

function hermesPluginManifestSource(): string {
  return `${HERMES_MCP_BEGIN.replace("mcp", "shrink-plugin")}
manifest_version: 1
name: ${HERMES_PLUGIN_NAME}
version: "1.0.0"
description: "Route oversized terminal output through caveman shrink."
provides_hooks:
  - transform_terminal_output
${HERMES_MCP_END.replace("mcp", "shrink-plugin")}
`;
}

function hermesPluginSource(): string {
  const { cmd, pre } = cavemanInvocation();
  return `# >>> caveman:shrink-plugin
# GENERATED by \`caveman hooks install hermes\`. Remove with \`caveman hooks uninstall hermes\`.
# Hermes discovers $HERMES_HOME/plugins/<name>/plugin.yaml + __init__.py and calls register(ctx)
# (source: ~/.hermes/hermes-agent/hermes_cli/plugins.py:1-20,1703-1748).
# transform_terminal_output replaces output only when a hook returns a string and fail-opens otherwise
# (source: ~/.hermes/hermes-agent/tools/terminal_tool.py:2662-2681).
import json
import os
import subprocess

_CAVEMAN_CMD = ${JSON.stringify(cmd)}
_CAVEMAN_PRE = ${JSON.stringify(pre)}


def _argv(*tail):
    return [_CAVEMAN_CMD, *_CAVEMAN_PRE, *tail]


def _int_env(name, default):
    try:
        return int(os.environ.get(name, str(default)) or str(default))
    except Exception:
        return default


def _eligible(command):
    try:
        if not isinstance(command, str) or not command.strip():
            return False
        payload = json.dumps({"tool_name": "Bash", "tool_input": {"command": command}})
        proc = subprocess.run(
            _argv("shrink-hook"),
            input=payload,
            text=True,
            capture_output=True,
            timeout=10,
        )
        return proc.returncode == 0 and bool((proc.stdout or "").strip())
    except Exception:
        return False


def _transform_terminal_output(command=None, output=None, **kwargs):
    try:
        if not isinstance(output, str):
            return None
        size = len(output.encode("utf-8", "replace"))
        if size < _int_env("CAVE_HERMES_SHRINK_MIN_BYTES", 4096):
            return None
        if size > _int_env("CAVE_MAX_SHRINK_BYTES", 8 * 1024 * 1024):
            return None
        if not _eligible(command):
            return None
        proc = subprocess.run(
            _argv("shrink", "--stdin"),
            input=output,
            text=True,
            capture_output=True,
            timeout=_int_env("CAVE_HERMES_SHRINK_TIMEOUT_SECONDS", 60),
        )
        if proc.returncode != 0 or not proc.stdout:
            return None
        return proc.stdout
    except Exception:
        return None


def register(ctx):
    ctx.register_hook("transform_terminal_output", _transform_terminal_output)
# <<< caveman:shrink-plugin
`;
}

function hermesNamedPluginEnabled(text: string, name: string): boolean {
  const escaped = escapeRegExp(name);
  return new RegExp(`(^|\\n)\\s*-\\s*["']?${escaped}["']?\\s*(?:#.*)?(?=\\n|$)`).test(text)
    || new RegExp(`enabled:\\s*\\[[^\\]]*["']?${escaped}["']?`).test(text);
}

function hermesPluginEnabled(text: string): boolean {
  return hermesNamedPluginEnabled(text, HERMES_PLUGIN_NAME);
}

function enableHermesPluginInConfig(): boolean {
  const path = hermesConfigPath();
  let existing = "";
  try {
    existing = readFileSync(path, "utf8");
  } catch (e) {
    if ((e as NodeJS.ErrnoException).code !== "ENOENT") {
      console.error(`${mark("warn")} cannot read ${path}: ${(e as Error).message}`);
      return false;
    }
  }
  if (hermesPluginEnabled(existing)) return true;
  const stripped = stripMarkedBlock(existing, HERMES_PLUGIN_ENABLE_BEGIN, HERMES_PLUGIN_ENABLE_END).text;
  const lines = yamlLines(stripped);
  const plugins = topLevelSection(lines, "plugins");
  if (plugins) {
    let enabledLine = -1;
    let inlineEnabled = false;
    for (let i = plugins.start + 1; i < plugins.end; i++) {
      const line = lines[i]!;
      if (/^  enabled:\s*(?:#.*)?$/.test(line)) {
        enabledLine = i;
        break;
      }
      if (/^  enabled:\s*\[/.test(line)) {
        enabledLine = i;
        inlineEnabled = true;
        break;
      }
    }
    if (inlineEnabled) {
      console.error(`${mark("warn")} ${path} uses inline plugins.enabled; wrote Hermes plugin but could not add marker-fenced enable entry`);
      return false;
    }
    if (enabledLine >= 0) {
      lines.splice(enabledLine + 1, 0, `    ${HERMES_PLUGIN_ENABLE_BEGIN}`, `    - ${HERMES_PLUGIN_NAME}`, `    ${HERMES_PLUGIN_ENABLE_END}`);
    } else {
      lines.splice(plugins.start + 1, 0, `  ${HERMES_PLUGIN_ENABLE_BEGIN}`, "  enabled:", `    - ${HERMES_PLUGIN_NAME}`, `  ${HERMES_PLUGIN_ENABLE_END}`);
    }
  } else {
    if (lines.length > 0 && lines[lines.length - 1]!.trim() !== "") lines.push("");
    lines.push(HERMES_PLUGIN_ENABLE_BEGIN, "plugins:", "  enabled:", `    - ${HERMES_PLUGIN_NAME}`, HERMES_PLUGIN_ENABLE_END);
  }
  try {
    mkdirSync(dirname(path), { recursive: true });
    writeFileSync(path, yamlText(lines));
    return true;
  } catch (e) {
    console.error(`${mark("warn")} cannot write ${path}: ${(e as Error).message}`);
    return false;
  }
}

function openClawStateDir(): string {
  const configured = process.env.OPENCLAW_STATE_DIR?.trim();
  return configured ? expandTilde(configured) : join(homedir(), ".openclaw");
}

function openClawConfigPath(): string {
  const configured = process.env.OPENCLAW_CONFIG_PATH?.trim();
  return configured ? expandTilde(configured) : join(openClawStateDir(), "openclaw.json");
}

function readOpenClawConfigForEdit(path: string): JsonObject | undefined {
  try {
    const parsed = readJson5Lenient(path);
    if (asJsonObject(parsed)) return parsed as JsonObject;
    console.error(`${mark("warn")} ${path} is not a JSON object; not modifying it`);
    return undefined;
  } catch (e) {
    if ((e as NodeJS.ErrnoException).code === "ENOENT") return {};
    console.error(`${mark("warn")} cannot read ${path}: ${(e as Error).message}; not modifying it`);
    return undefined;
  }
}

function writeOpenClawConfig(path: string, cfg: JsonObject): boolean {
  try {
    mkdirSync(dirname(path), { recursive: true });
    writeFileSync(path, JSON.stringify(cfg, null, 2) + "\n", { mode: 0o600 });
    return true;
  } catch (e) {
    console.error(`${mark("warn")} cannot write ${path}: ${(e as Error).message}`);
    return false;
  }
}

function disableHermesPluginInConfig(): boolean {
  const path = hermesConfigPath();
  let existing = "";
  try {
    existing = readFileSync(path, "utf8");
  } catch (e) {
    return (e as NodeJS.ErrnoException).code === "ENOENT";
  }
  const stripped = stripMarkedBlock(existing, HERMES_PLUGIN_ENABLE_BEGIN, HERMES_PLUGIN_ENABLE_END);
  if (!stripped.removed) return true;
  try {
    writeFileSync(path, stripped.text);
    return true;
  } catch (e) {
    console.error(`${mark("warn")} cannot write ${path}: ${(e as Error).message}`);
    return false;
  }
}

function installShrinkPluginHermes(): boolean {
  try {
    mkdirSync(hermesPluginDir(), { recursive: true });
    writeFileSync(hermesPluginManifestPath(), hermesPluginManifestSource());
    writeFileSync(hermesPluginInitPath(), hermesPluginSource());
  } catch (e) {
    console.error(`${mark("warn")} cannot write ${hermesPluginDir()}: ${(e as Error).message}`);
    return false;
  }
  if (!enableHermesPluginInConfig()) {
    process.stderr.write(`${mark("warn")} Hermes plugin written to ${hermesPluginDir()}, but plugins.enabled was not updated; run ${cyan(`hermes plugins enable ${HERMES_PLUGIN_NAME}`)}\n`);
  }
  return true;
}

function removeShrinkPluginHermes(): boolean {
  let removed = false;
  try {
    const init = readFileSync(hermesPluginInitPath(), "utf8");
    const manifest = readFileSync(hermesPluginManifestPath(), "utf8");
    if (init.includes("caveman:shrink-plugin") || manifest.includes("caveman:shrink-plugin")) {
      rmSync(hermesPluginDir(), { recursive: true, force: true });
      removed = true;
    }
  } catch {
    // Missing or unreadable plugin is already inactive from Caveman's side.
  }
  disableHermesPluginInConfig();
  return removed;
}

function installShrinkPluginOpenClaw(): boolean {
  const pluginDir = ensureOpenClawShrinkPlugin();
  if (!pluginDir) return false;
  const path = openClawConfigPath();
  const cfg = readOpenClawConfigForEdit(path);
  if (!cfg) return false;
  const next = deepMerge(cfg, openClawPluginOverlay(cfg, pluginDir)) as JsonObject;
  return writeOpenClawConfig(path, next);
}

function removeOpenClawConfigEmptyContainers(root: JsonObject) {
  const plugins = asJsonObject(root.plugins);
  if (!plugins) return;
  const load = asJsonObject(plugins.load);
  if (load && Object.keys(load).length === 0) delete plugins.load;
  const entries = asJsonObject(plugins.entries);
  if (entries && Object.keys(entries).length === 0) delete plugins.entries;
  if (Object.keys(plugins).length === 0) delete root.plugins;
}

function removeShrinkPluginOpenClaw(): boolean {
  const path = openClawConfigPath();
  const cfg = readOpenClawConfigForEdit(path);
  if (!cfg) return false;
  const plugins = asJsonObject(cfg.plugins);
  if (plugins) {
    const load = asJsonObject(plugins.load);
    if (load && Array.isArray(load.paths)) {
      const paths = load.paths.filter((value): value is string => typeof value === "string" && value !== openClawPluginDir());
      if (paths.length > 0) load.paths = paths;
      else delete load.paths;
    }
    const entries = asJsonObject(plugins.entries);
    if (entries) delete entries[OPENCLAW_PLUGIN_ID];
    if (Array.isArray(plugins.allow)) plugins.allow = plugins.allow.filter((value) => value !== OPENCLAW_PLUGIN_ID);
    removeOpenClawConfigEmptyContainers(cfg);
  }
  try {
    const pluginDir = openClawPluginDir();
    const source = readFileSync(join(pluginDir, "index.mjs"), "utf8");
    if (source.includes("caveman:openclaw-shrink-plugin")) rmSync(pluginDir, { recursive: true, force: true });
  } catch {
    // Missing or not ours — leave it alone.
  }
  return writeOpenClawConfig(path, cfg);
}

function shrinkHookMarkerPath(agentId: string): string {
  return join(cavemanHome(), "hooks", `${agentId}.json`);
}
function shrinkHookInstalled(agentId: string): boolean {
  try { return statSync(shrinkHookMarkerPath(agentId)).isFile(); } catch { return false; }
}
function writeShrinkHookMarker(agentId: string) {
  const p = shrinkHookMarkerPath(agentId);
  mkdirSync(dirname(p), { recursive: true });
  writeFileSync(p, JSON.stringify({ installed: true }) + "\n");
}

// directiveMarkerPath / installedDirectiveIds: the local record of which wrap
// directives are installed, so synced session evidence can carry the label
// (cave.directives) — tokens only, basis stays 'inferred'.
function directiveMarkerPath(id: string): string {
  return join(cavemanHome(), "hooks", "directives", `${id}.json`);
}
function installedDirectiveIds(): string[] {
  return Object.keys(DIRECTIVES).filter((id) => {
    try { return statSync(directiveMarkerPath(id)).isFile(); } catch { return false; }
  }).sort();
}
// installedDirectivesWithTimes reads each marker's installed_at so the sync
// pass can label ONLY rows created at or after the install (review M12):
// pre-install backlog sessions are not evidence about a directive, and
// labeling them would fabricate a before/after population. A marker without a
// parseable installed_at labels nothing (fail closed — fewer labels, never
// wrong ones).
function installedDirectivesWithTimes(): { id: string; installedAtMs: number }[] {
  const out: { id: string; installedAtMs: number }[] = [];
  for (const id of installedDirectiveIds()) {
    try {
      const raw = JSON.parse(readFileSync(directiveMarkerPath(id), "utf8")) as { installed_at?: unknown };
      const t = typeof raw.installed_at === "string" ? Date.parse(raw.installed_at) : NaN;
      if (Number.isFinite(t)) out.push({ id, installedAtMs: t });
    } catch {
      // unreadable marker: label nothing for this directive
    }
  }
  return out;
}

function hooksUsage(): never {
  console.error(`usage: ${invokedCommand("hooks")} install|uninstall [agent] [--directive <id>]`);
  console.error("  install a command-output compression hook so an agent's shell output is");
  console.error("  auto-shrunk before the model reads it (recover exact original with `caveman retrieve`).");
  console.error("  hard rewrite: claude, codex, opencode, gemini, hermes, openclaw · manual: aider.");
  console.error("  with no agent, installs for every hookable agent found on PATH.");
  console.error("  --directive <id>: install/remove a Cave Plan wrap directive (a model nudge in the");
  console.error(`  agent's instructions file; suggest-only, evidence stays tokens-only and inferred).`);
  console.error(`  directives: ${Object.keys(DIRECTIVES).sort().join(", ")}`);
  process.exit(2);
}

// hooksDirectiveCmd handles `caveman hooks install|uninstall --directive <id> [agent]`
// (AUTOPILOT_SPEC §7.3): the same verb surface that removes a shrink note today,
// extended with a flag — never a new porcelain verb (capped). Install
// announces itself and prints the undo command (§7 announce+undo).
function hooksDirectiveCmd(sub: "install" | "uninstall", directiveId: string, target: string | undefined) {
  // Object.hasOwn: directiveId is user input; a truthiness read would let
  // prototype keys ('constructor', '__proto__') pass the gate (review C8).
  if (!Object.hasOwn(DIRECTIVES, directiveId)) {
    console.error(`unknown directive '${directiveId}'. known: ${Object.keys(DIRECTIVES).sort().join(", ")}`);
    process.exit(2);
  }
  // A directive rides ONLY an instructions-file surface declared by profile.
  // Agents without one are refused honestly —
  // never written to a guessed path.
  let targets: AgentProfile[];
  if (target) {
    const a = findAgent(target);
    if (!a) { console.error(`unknown agent '${target}'. known: ${AGENTS.map((x) => x.id).join(", ")}`); process.exit(2); }
    if (!instructionFileForAgent(a)) {
      console.error(`${mark("warn")} ${a.display_name} declares no instructions-file surface; directives need one (today: ${AGENTS.filter((x) => instructionFileForAgent(x)).map((x) => x.id).join(", ") || "none"})`);
      process.exit(1);
    }
    targets = [a];
  } else {
    targets = AGENTS.filter((a) => instructionFileForAgent(a) && which(binOf(a)));
    if (targets.length === 0) {
      console.error("no agents with an instructions-file surface detected on PATH; pass an agent id, e.g. `caveman hooks install --directive " + directiveId + " codex`");
      process.exit(1);
    }
  }
  for (const a of targets) {
    const file = instructionFileForAgent(a)!;
    if (sub === "install") {
      if (installDirectiveNote(file, directiveId)) {
        const p = directiveMarkerPath(directiveId);
        mkdirSync(dirname(p), { recursive: true });
        // installed_at bounds which synced rows may carry the directive label
        // (review M12): sessions from BEFORE the install are not evidence
        // about it, so the sync pass labels only rows at or after this time.
        writeFileSync(p, JSON.stringify({ installed: true, installed_at: new Date().toISOString(), agent: a.id, file }) + "\n");
        process.stderr.write(`${mark("ok")} ${a.display_name}: directive '${directiveId}' added to ${file} (a model nudge, not a hard rewrite; its evidence is counted in tokens and stays inferred — never verified)\n`);
        process.stderr.write(dim(`→ undo: caveman tools hooks uninstall --directive ${directiveId} ${a.id}\n`));
      } else {
        process.exitCode = 1; // corrupted block or unwritable file: the error already printed
      }
    } else {
      const removed = removeDirectiveNote(file, directiveId);
      if (removed === "corrupt" || removed === "failed") {
        process.exitCode = 1; // touched nothing; the marker stays so state remains honest
        continue;
      }
      try { unlinkSync(directiveMarkerPath(directiveId)); } catch { /* no marker — fine */ }
      process.stderr.write(`${mark(removed === "removed" ? "ok" : "warn")} ${a.display_name}: ${removed === "removed" ? `directive '${directiveId}' removed from ${file}` : `no '${directiveId}' directive found`}\n`);
    }
  }
}

function hooksCmd(rest: string[]) {
  // --directive <id> (or --directive=<id>) routes to the wrap-directive
  // surface (same verb, no new porcelain). Any OTHER --flag is
  // rejected loudly: silently ignoring `--directive=x` used to install the
  // shrink note and exit 0 while the user believed a directive was installed
  // (review C8).
  let directiveId: string | undefined;
  const positional: string[] = [];
  for (let i = 0; i < rest.length; i++) {
    const arg = rest[i] ?? "";
    if (arg === "--directive") {
      directiveId = rest[++i];
      if (!directiveId) hooksUsage();
    } else if (arg.startsWith("--directive=")) {
      directiveId = arg.slice("--directive=".length);
      if (!directiveId) hooksUsage();
    } else if (arg.startsWith("--")) {
      console.error(`unknown option '${arg}'`);
      hooksUsage();
    } else {
      positional.push(arg);
    }
  }
  rest = positional;
  const sub = rest[0];
  if (sub !== "install" && sub !== "uninstall") hooksUsage();
  if (directiveId) return hooksDirectiveCmd(sub, directiveId, rest[1]);
  const target = rest[1];
  let targets: AgentProfile[];
  if (target) {
    const a = findAgent(target);
    if (!a) { console.error(`unknown agent '${target}'. known: ${AGENTS.map((x) => x.id).join(", ")}`); process.exit(2); }
    targets = [a];
  } else {
    // No agent named: target every known agent with an installable hook surface
    // that is actually present on PATH (mirrors `mcp install`'s detect-all path).
    targets = AGENTS.filter((a) => commandHookKind(a) !== "none" && which(binOf(a)));
    if (targets.length === 0) {
      console.error("no hookable agents detected on PATH; pass an agent id, e.g. `caveman hooks install claude`");
      process.exit(1);
    }
  }
  if (sub === "install") {
    let n = 0;
    let hard = 0;
    for (const a of targets) {
      if (installShrinkHookForAgent(a)) {
        writeShrinkHookMarker(a.id);
        n++;
        if (commandHookKind(a) === "hard") hard++;
        process.stderr.write(`${mark("ok")} ${a.display_name}: ${hookInstalledPhrase(a)}\n`);
      } else if (commandHookKind(a) === "none") {
        process.stderr.write(`${mark("warn")} ${a.display_name}: no automatic command-output hook surface — run noisy commands through ${cyan("caveman shrink -- <cmd>")}\n`);
      } else {
        // A hookable agent whose install failed (corrupted managed block,
        // unwritable file) must not exit 0 behind the already-printed error
        // (closure B1): a green exit with no hook installed is a silent lie.
        process.exitCode = 1;
      }
    }
    // Footer must not overstate the soft tier: only a hard rewrite "auto-shrinks".
    if (hard > 0) {
      process.stderr.write(dim("→ noisy command output is now auto-shrunk where supported; remove with `caveman hooks uninstall`\n"));
    } else if (n > 0) {
      process.stderr.write(dim("→ supported agents will be nudged to prefer `caveman shrink` for noisy output (a model nudge, not a guaranteed rewrite); remove with `caveman hooks uninstall`\n"));
    }
    return;
  }
  for (const a of targets) {
    const removed = removeShrinkHookForAgent(a);
    try { unlinkSync(shrinkHookMarkerPath(a.id)); } catch { /* no marker — fine */ }
    process.stderr.write(`${mark(removed ? "ok" : "warn")} ${a.display_name}: ${removed ? "command-output hook removed" : "no caveman hook found"}\n`);
  }
}

// retrieve prints the byte-exact original behind a CCR handle (or, with a query, the
// most relevant sections via BM25) — the recovery half of `shrink`/`compress`/`pixel`. It
// shells to `caveman-engine retrieve`, inheriting its stdout, and forwards the exit
// code; a missing engine or unknown handle surfaces as a one-line error.
function retrieve(rest: string[]) {
  const handle = rest[0];
  if (!handle) { console.error(`usage: ${invokedCommand("retrieve")} <handle> [query]`); process.exit(2); }
  const bin = cavemanBin("caveman-engine", "CAVEMAN_ENGINE_BIN");
  const engineArgs = ["retrieve", handle];
  if (rest[1]) engineArgs.push(rest[1]);
  const child = spawn(bin, engineArgs, { stdio: ["ignore", "inherit", "inherit"] });
  emitCommandRunOnce("ok"); // exit handler below hard-exits; never returns to main()
  child.on("error", (e) => { console.error(`${mark("warn")} cannot run ${bin}: ${e.message}`); process.exit(1); });
  child.on("exit", (code) => process.exit(code ?? 0));
}

// evalsRun delegates to the engine's local eval harness, which replays the
// fixture set behind fail-closed quality graders and exits non-zero if any gate
// fails. The CLI carries no fixtures of its own; it forwards the engine's exit
// code so callers can gate on it.
function evalsRun(argv: string[] = []) {
  if (argv.length !== 0 && (argv.length !== 2 || argv[0] !== "--fixtures" || !argv[1] || argv[1].startsWith("-"))) {
    commandUsage("evals run [--fixtures <dir>]");
  }
  const bin = cavemanBin("caveman-engine", "CAVEMAN_ENGINE_BIN");
  try {
    const out = execFileSync(bin, ["evals", "run", ...argv], { encoding: "utf8" });
    process.stdout.write(out);
  } catch (error) {
    const e = error as { stdout?: string; status?: number; message?: string };
    if (e.stdout) process.stdout.write(e.stdout);
    else console.error(`failed to run evals via ${bin}: ${e.message}`);
    process.exit(e.status ?? 1);
  }
}

// stats prints the local spend summary by delegating to the proxy binary, which
// owns the ~/.caveman/ SQLite store. The CLI carries no database dependency, so
// it reads through the same Go binary `caveman start` launches.
function stats(argv: string[] = []) {
  if (argv.length > 1 || (argv.length === 1 && argv[0] !== "--json")) commandUsage("stats [--json]");
  const bin = cavemanBin("caveman-proxy", "CAVEMAN_PROXY_BIN");
  try {
    const out = execFileSync(bin, ["stats", ...argv], { encoding: "utf8" });
    process.stdout.write(out);
  } catch (error) {
    console.error(`failed to read stats via ${bin}: ${(error as Error).message}`);
    process.exit(1);
  }
}

type RecentProxyRow = {
  ts: string;
  agent_slug: string;
  provider: string;
  model: string;
  endpoint: string;
  input_tokens: number;
  output_tokens: number;
  basis: string;
};

// verifyFirstRequest waits for a real request row to flow through the local proxy.
// Without --app it confirms ANY fresh traffic (a "did it work" check) — pass
// --app <slug> when other apps may be routing through the proxy concurrently.
async function verifyFirstRequest(rest: string[]) {
  const startMs = Date.now();
  const app = flagFrom(rest, "--app", "");
  const timeoutMs = verifyTimeoutMs(rest);
  const recent = flagFrom(rest, "--recent", "50");
  const bin = cavemanBin("caveman-proxy", "CAVEMAN_PROXY_BIN");
  const resolved = resolveGoBin("caveman-proxy", "CAVEMAN_PROXY_BIN");
  if (!resolved) return startMissingProxyUI(bin);

  const deadline = startMs + timeoutMs;
  while (Date.now() <= deadline) {
    const rows = readRecentProxyRows(resolved, recent);
    const matched = rows.filter((row) => {
      if (parseProxyTS(row.ts) < startMs) return false;
      return !app || row.agent_slug === app;
    });
    if (matched.length) {
      for (const row of matched) {
        console.log(`${row.agent_slug || "unknown"} ${row.provider || "unknown"}/${row.model || "unknown"} input:${row.input_tokens || 0} output:${row.output_tokens || 0} basis: ${row.basis || "inferred"}`);
      }
      return;
    }
    const remaining = deadline - Date.now();
    if (remaining <= 0) break;
    await sleep(Math.min(2000, remaining));
  }
  const seconds = Math.ceil(timeoutMs / 1000);
  console.error(`no request seen through the proxy in ${seconds}s`);
  console.error("hint: check base URL points at the Caveman gateway and run `caveman start`");
  process.exit(1);
}

function verifyTimeoutMs(values: string[]): number {
  const ms = Number(flagFrom(values, "--timeout-ms", ""));
  if (Number.isFinite(ms) && ms > 0) return ms;
  const seconds = Number(flagFrom(values, "--timeout", "60"));
  if (Number.isFinite(seconds) && seconds > 0) return Math.round(seconds * 1000);
  return 60_000;
}

function readRecentProxyRows(bin: string, recent: string): RecentProxyRow[] {
  try {
    const out = execFileSync(bin, ["stats", "--recent", recent, "--json"], { encoding: "utf8", env: process.env });
    const parsed = JSON.parse(out);
    if (!Array.isArray(parsed)) throw new Error("recent stats was not a JSON array");
    return parsed.map((row) => ({
      ts: String(row?.ts ?? ""),
      agent_slug: String(row?.agent_slug ?? ""),
      provider: String(row?.provider ?? ""),
      model: String(row?.model ?? ""),
      endpoint: String(row?.endpoint ?? ""),
      input_tokens: Number(row?.input_tokens ?? 0),
      output_tokens: Number(row?.output_tokens ?? 0),
      basis: String(row?.basis ?? "inferred"),
    }));
  } catch (error) {
    const e = error as { stdout?: string; stderr?: string; status?: number; message?: string };
    if (e.stdout) process.stdout.write(e.stdout);
    if (e.stderr) process.stderr.write(e.stderr);
    if (!e.stdout && !e.stderr) console.error(`failed to read recent proxy requests via ${bin}: ${e.message ?? error}`);
    process.exit(e.status ?? 1);
  }
}

function parseProxyTS(ts: string): number {
  if (!ts) return 0;
  const normalized = ts.includes("T") ? ts : `${ts.replace(" ", "T")}Z`;
  const parsed = Date.parse(normalized);
  return Number.isFinite(parsed) ? parsed : 0;
}

async function trial(rest: string[]) {
  const sub = rest[0] ?? "";
  if (["report", "promote", "export", "analyze"].includes(sub)) {
    return proxyPassthrough(["trial", ...rest]);
  }
  const sep = rest.indexOf("--");
  if (sep < 0) {
    console.error(`usage: ${invokedCommand("trial")} [--learn] -- <agent> [args...]`);
    console.error("       caveman trial report [--html] [--json] [--trial-id <id>]");
    process.exit(2);
  }
  const trialFlags = rest.slice(0, sep);
  const command = rest.slice(sep + 1);
  if (command.length === 0) {
    console.error(`usage: ${invokedCommand("trial")} -- <agent> [args...]`);
    process.exit(2);
  }

  const trialID = flagFrom(trialFlags, "--trial-id", "trial_" + Date.now().toString(36));
  const learnHistory = trialFlags.includes("--learn");
  const since = flagFrom(trialFlags, "--since", "30d");
  const requested = command[0]!;
  const agent = findAgent(requested);
  const agentBin = agent ? binOf(agent) : requested;
  const extra = agent ? [...agent.args, ...command.slice(1)] : command.slice(1);
  const resolvedAgent = which(agentBin);
  if (!resolvedAgent) {
    wrapNotFoundUI(requested, agent);
    process.exit(127);
  }

  const proxyResolved = which(proxyBin());
  if (!proxyResolved) return startMissingProxyUI(proxyBin());

  const port = await freePort();
  const listen = `127.0.0.1:${port}`;
  const trialURL = `http://${listen}`;
  proxyExec(["trial", "start", "--trial-id", trialID, "--agent", agent?.id ?? requested, "--command", command.join(" ")], process.env, true);
  const proxy = spawn(proxyResolved, ["serve"], {
    stdio: ["ignore", "ignore", "inherit"],
    env: { ...process.env, CAVEMAN_LISTEN: listen, CAVEMAN_LABEL: `trial:${trialID}`, CAVEMAN_MODE: "record" },
  });
  proxy.on("error", (error) => {
    console.error(`failed to launch ${proxyBin()}: ${error.message}`);
    process.exit(1);
  });

  try {
    await waitForPort("127.0.0.1", port, 5000);
    const result = await spawnWrapped(resolvedAgent, extra, agent, { mode: "record", noProxy: true, toon: false, noShrink: false, mcpMode: "auto", noBrowse: false, delegate: false, minimal: false, command }, trialURL);
    proxy.kill("SIGTERM");
    await waitForChild(proxy, 1500);
    proxyExec(["trial", "finish", "--trial-id", trialID, "--exit-code", String(result.code)], process.env, true);
    if (learnHistory) {
      proxyExecMaybe(["usage", "import", "codex", "--since", since]);
      proxyExecMaybe(["usage", "import", "claude", "--since", since]);
      proxyExecMaybe(["learn", "scan", "--sources", "codex,claude,caveman", "--since", since]);
    }
    proxyExec(["trial", "analyze", "--trial-id", trialID], process.env, true);
    proxyPassthrough(["trial", "report", "--trial-id", trialID]);
    process.exit(result.code);
  } catch (error) {
    proxy.kill("SIGTERM");
    await waitForChild(proxy, 1000);
    throw error;
  }
}

type LearnSink = {
  sink_id: string;
  practice_id: string;
  title: string;
  class: "reducible" | "recurring_context" | "behavioral" | "load_bearing";
  basis: string;
  tokens_per_turn: number;
  tokens_per_day_rate: number;
  evidence?: Record<string, unknown>;
  suggestion?: string;
};

type LearnPlan = {
  schema: "caveman.learn.v1";
  basis: "inferred";
  sessions_scanned?: number;
  sessions_by_source?: Record<string, number>;
  cave_score: { score: number; basis: string; scope?: string };
  sinks: LearnSink[];
  retro?: LearnRetro;
};

// LearnRetro mirrors the proxy's optional `retro` block (learn scan --retro):
// a retrospective sum over real scanned sessions — never a projection. Older
// proxies simply omit it; every consumer must treat it as absent-able.
type LearnRetroFamily = { id: string; label: string; tokens: number };
type LearnRetro = {
  basis: "inferred";
  window_days: number;
  sessions_total: number;
  sessions_scanned: number;
  turns_observed: number;
  tokens_observed: number;
  tokens_observed_source: "session_usage" | "o200k_estimate";
  would_cut_tokens: number;
  // Tool-output and timestamp-ordered repeated-block cuts re-weighted by later
  // provider-counted turns (until compaction), under one per-turn residency cap
  // — the like-for-like figure against tokens_observed. Absent on older proxies.
  would_cut_stream_tokens?: number;
  families: LearnRetroFamily[];
  config_prefix_tokens_per_turn?: number;
  engine_used: boolean;
  time_boxed: boolean;
  caveats?: string[];
};

type LearnDiff = { days: number; gone: number; back: number; fresh: number };

const LEARN_EMPTY =
  "no Claude Code or Codex sessions found in the last 30d — the plan needs a block repeated across ≥3 sessions; run `caveman claude` a few times, then `caveman learn`";
const LEARN_DETAILED_NEXT =
  "next:  caveman tools skills install caveman-learn   (review + apply, with consent)  ·  preview one: caveman learn apply <sink_id> --dry-run";
const LEARN_SUMMARY_LIMIT = 3;

function learnReportPath(): string {
  return join(cavemanHome(), "reports", "caveman-learn.html");
}

function learnReportMtime(): number {
  try {
    return statSync(learnReportPath()).mtimeMs;
  } catch {
    return 0;
  }
}

function learnReportGeneration(): string {
  try {
    const snapshot = JSON.parse(readFileSync(
      join(cavemanHome(), "reports", "caveman-learn.json"),
      "utf8",
    )) as { generation?: unknown };
    return typeof snapshot.generation === "string" ? snapshot.generation : "";
  } catch {
    return "";
  }
}

const KNOWN_PRACTICE_IDS = new Set<string>(PRACTICE_REGISTRY.map((practice) => practice.id));

function renderLearnDetailedRows(plan: LearnPlan, markdown: boolean): string[] {
  const lines: string[] = [];
  for (const [index, sink] of plan.sinks.entries()) {
    const lead = markdown ? `${index + 1}. **${sink.title}**` : `${index + 1}. ${sink.title}`;
    lines.push(`${lead}  ·  ${sink.sink_id}  ·  ${sink.class}`);
    lines.push(`   ~${humanTokens(sink.tokens_per_turn)} tokens/turn · ~${humanTokens(sink.tokens_per_day_rate)} tokens/day · basis: inferred`);
    if (KNOWN_PRACTICE_IDS.has(sink.practice_id)) {
      lines.push(`   practice: ${sink.practice_id} · unmeasured — verified nowhere yet`);
    }
    if (sink.suggestion) lines.push(`   ${sink.suggestion}`);
  }
  return lines;
}

function learnEvidenceNumber(sink: LearnSink, key: string): number | undefined {
  const value = sink.evidence?.[key];
  return typeof value === "number" && Number.isFinite(value) && value >= 0 ? value : undefined;
}

function compactLearnText(value: string, max = 108): string {
  const oneLine = value.replace(/\s+/g, " ").trim();
  if (oneLine.length <= max) return oneLine;
  const prefix = oneLine.slice(0, max - 1).trimEnd();
  const sentenceEnd = Math.max(prefix.lastIndexOf(". "), prefix.lastIndexOf("; "));
  if (sentenceEnd >= Math.floor(max * 0.35)) return prefix.slice(0, sentenceEnd + 1);
  const wordEnd = prefix.lastIndexOf(" ");
  return `${prefix.slice(0, wordEnd > 0 ? wordEnd : prefix.length)}…`;
}

export type LearnSummaryMove = {
  title: string;
  kind: string;
  detail: string;
  action?: string;
};

export type LearnTuiViewModel = {
  score: number | null;
  scope: string;
  sessions: string;
  diff?: string;
  status?: string;
  moves: LearnSummaryMove[];
  protected?: string;
  findings: number;
  report: string;
};

export function learnSummaryMoves(plan: LearnPlan): LearnSummaryMove[] {
  const recurring = plan.sinks.filter((sink) => sink.class === "recurring_context");
  const moves: LearnSummaryMove[] = [];
  let recurringAdded = false;

  for (const sink of plan.sinks) {
    if (sink.class === "load_bearing") continue;
    if (sink.class === "recurring_context") {
      if (recurringAdded) continue;
      recurringAdded = true;
      const largest = Math.max(0, ...recurring.map((item) => learnEvidenceNumber(item, "block_tokens") ?? 0));
      const sessions = Math.max(0, ...recurring.map((item) => learnEvidenceNumber(item, "recurrence_sessions") ?? 0));
      const facts = [
        "recurring context",
        largest > 0 ? `largest ~${humanTokens(largest)} tokens` : "",
        sessions > 0 ? `up to ${sessions} sessions` : "",
        "inferred",
      ].filter(Boolean);
      moves.push({
        title: `${recurring.length} context block${recurring.length === 1 ? "" : "s"} repeat across sessions`,
        kind: "recurring context",
        detail: facts.join(" · "),
        action: "Review selected blocks before moving them to memory; repetition does not prove they are unnecessary.",
      });
    } else {
      const rates = [
        sink.class.replaceAll("_", " "),
        sink.tokens_per_turn > 0 ? `~${humanTokens(sink.tokens_per_turn)} tokens/turn` : "",
        sink.tokens_per_day_rate > 0 ? `~${humanTokens(sink.tokens_per_day_rate)} tokens/day` : "",
        "inferred",
      ].filter(Boolean);
      moves.push({
        title: sink.title,
        kind: sink.class.replaceAll("_", " "),
        detail: rates.join(" · "),
        ...(sink.suggestion ? { action: compactLearnText(sink.suggestion) } : {}),
      });
    }
    if (moves.length >= LEARN_SUMMARY_LIMIT) break;
  }
  return moves;
}

function renderLearnSummaryRows(plan: LearnPlan): string[] {
  const lines: string[] = [];
  for (const [index, move] of learnSummaryMoves(plan).entries()) {
    lines.push(`${index + 1}  ${move.title}`);
    lines.push(`   ${move.detail}`);
    if (move.action) lines.push(`   ${move.action}`);
  }
  return lines;
}

function learnSourceLine(plan: LearnPlan, sessions: number): string {
  const by = plan.sessions_by_source ?? {};
  const sourceBits = [
    by.claude ? `Claude ${by.claude}` : "",
    by.codex ? `Codex ${by.codex}` : "",
  ].filter(Boolean);
  return `${sessions} sessions${sourceBits.length ? ` · ${sourceBits.join(" · ")}` : ""}`;
}

function learnDiffText(diff: LearnDiff | undefined): string | undefined {
  if (!diff) return undefined;
  const bits = [
    diff.gone ? `${diff.gone} move${diff.gone === 1 ? "" : "s"} gone` : "",
    diff.back ? `${diff.back} back` : "",
    diff.fresh ? `${diff.fresh} new` : "",
  ].filter(Boolean);
  return bits.length ? `since your last run ${diff.days}d ago: ${bits.join(" · ")}` : undefined;
}

export function buildLearnTuiModel(
  plan: LearnPlan,
  options: { report?: string; diff?: LearnDiff } = {},
): LearnTuiViewModel {
  const sessions = Math.max(0, Number(plan.sessions_scanned ?? 0));
  const recurring = plan.sinks.some((sink) => sink.class === "recurring_context");
  const protectedSink = plan.sinks.find((sink) => sink.class === "load_bearing");
  const diffText = learnDiffText(options.diff);
  const status = sessions === 0
    ? LEARN_EMPTY
    : !recurring
      ? `${sessions} sessions scanned · no block repeated across ≥3 sessions yet — keep running \`${invokedAs()} claude\`, then re-run \`${invokedAs()} learn\``
      : undefined;
  return {
    score: recurring ? plan.cave_score.score : null,
    scope: "local setup · inferred · not billed spend · separate from org Cave Score",
    sessions: learnSourceLine(plan, sessions),
    ...(diffText ? { diff: diffText } : {}),
    ...(status ? { status } : {}),
    moves: learnSummaryMoves(plan),
    ...(protectedSink
      ? { protected: `${protectedSink.title.replace(/^Your\s+/i, "")} · included in score, never auto-fixed` }
      : {}),
    findings: plan.sinks.length,
    report: options.report ?? learnReportPath(),
  };
}

export function renderLearnPlan(
  plan: LearnPlan,
  options: { markdown?: boolean; report?: string; diff?: LearnDiff; verbose?: boolean } = {},
): string {
  const markdown = options.markdown === true;
  const verbose = options.verbose === true || markdown;
  const report = options.report ?? learnReportPath();
  const sessions = Math.max(0, Number(plan.sessions_scanned ?? 0));
  const recurring = plan.sinks.some((sink) => sink.class === "recurring_context");
  const lines: string[] = [];

  if (sessions === 0) {
    lines.push(LEARN_EMPTY);
  } else if (!recurring) {
    if (plan.sinks.length > 0) {
      lines.push(...(verbose ? renderLearnDetailedRows(plan, markdown) : ["top moves", ...renderLearnSummaryRows(plan)]), "");
    }
    lines.push(`${sessions} sessions scanned · no block repeated across ≥3 sessions yet — keep running \`caveman claude\`, then re-run \`caveman learn\``);
  } else {
    lines.push(markdown
      ? `## Setup Score ${plan.cave_score.score} — basis: inferred (local sessions, not billed spend)`
      : verbose
        ? `Setup Score ${plan.cave_score.score}  ·  basis: inferred (local sessions, not billed spend)`
        : `Setup Score ${plan.cave_score.score}/100`);
    if (verbose) {
      lines.push("scores your local agent setup — the console's Cave Score (org) scores org traffic;");
      lines.push("the two are different scales and will not match");
      lines.push(`${sessions} sessions scanned${learnSourceLine(plan, sessions).replace(`${sessions} sessions`, "")}`);
    } else {
      lines.push("local setup · inferred · not billed spend · separate from org Cave Score");
      lines.push(learnSourceLine(plan, sessions));
    }
    const diffText = learnDiffText(options.diff);
    if (diffText) lines.push(diffText);
    if (verbose) {
      lines.push("", ...renderLearnDetailedRows(plan, markdown), "", LEARN_DETAILED_NEXT);
    } else {
      const protectedSink = plan.sinks.find((sink) => sink.class === "load_bearing");
      lines.push("", "top moves", ...renderLearnSummaryRows(plan));
      if (protectedSink) {
        const title = protectedSink.title.replace(/^Your\s+/i, "");
        lines.push(`protected  ${title} · included in score, never auto-fixed`);
      }
      lines.push(
        "",
        `next:  ${invokedAs()} learn implement   fix with Claude Code or Codex; asks before every edit`,
        `details: ${invokedAs()} learn --all   ${plan.sinks.length} findings`,
      );
    }
  }
  lines.push("", `report: ${report}`);
  return `${lines.join("\n")}\n`;
}

function readLearnDiff(current: LearnPlan): LearnDiff | undefined {
  const dir = join(cavemanHome(), "reports");
  let files: string[];
  try {
    files = readdirSync(dir)
      .filter((name) => /^caveman-learn\.\d{4}-\d{2}-\d{2}\.json$/.test(name))
      .sort()
      .reverse();
  } catch {
    return undefined;
  }
  const snapshots: Array<{ generated_at: string; sinks: LearnSink[] }> = [];
  for (const name of files) {
    try {
      const parsed = JSON.parse(readFileSync(join(dir, name), "utf8")) as { generated_at?: unknown; sinks?: unknown };
      if (typeof parsed.generated_at === "string" && Array.isArray(parsed.sinks)) {
        snapshots.push({ generated_at: parsed.generated_at, sinks: parsed.sinks as LearnSink[] });
      }
    } catch {
      // A corrupt historical snapshot is ignored; current plan still renders.
    }
  }
  const now = Date.now();
  const older = snapshots.filter((snap) => Date.parse(snap.generated_at) < now - 60_000);
  if (older.length === 0) return undefined;
  const prior = older.find((snap) => now - Date.parse(snap.generated_at) >= 7 * 86_400_000) ?? older[0]!;
  const priorIDs = new Set(prior.sinks.map((sink) => sink.sink_id));
  const currentIDs = new Set(current.sinks.map((sink) => sink.sink_id));
  const seenEarlier = new Set(older.slice(older.indexOf(prior) + 1).flatMap((snap) => snap.sinks.map((sink) => sink.sink_id)));
  let gone = 0;
  let back = 0;
  let fresh = 0;
  for (const id of priorIDs) if (!currentIDs.has(id)) gone++;
  for (const id of currentIDs) {
    if (priorIDs.has(id)) continue;
    if (seenEarlier.has(id)) back++;
    else fresh++;
  }
  return {
    days: Math.max(1, Math.floor((now - Date.parse(prior.generated_at)) / 86_400_000)),
    gone,
    back,
    fresh,
  };
}

function learnTimeoutSeconds(): number {
  const value = Number(process.env.CAVE_LEARN_TIMEOUT ?? "120");
  return Number.isFinite(value) && value > 0 ? Math.floor(value) : 120;
}

function proxyExecLearn(proxyArgs: string[], progress: boolean): string {
  const seconds = learnTimeoutSeconds();
  try {
    return execFileSync(proxyBin(), proxyArgs, {
      encoding: "utf8",
      env: process.env,
      timeout: seconds * 1000,
      maxBuffer: 64 * 1024 * 1024,
      stdio: ["ignore", "pipe", progress ? "inherit" : "pipe"],
    });
  } catch (error) {
    const e = error as { stdout?: string; stderr?: string; status?: number; code?: string; killed?: boolean; signal?: string; message?: string };
    if (e.code === "ETIMEDOUT" || (e.killed && e.code !== "ENOBUFS")) {
      console.error(`learn scan timed out after ${seconds}s — no score computed; re-run with \`caveman learn --json\` to capture the raw scan`);
      process.exit(1);
    }
    if (e.stderr) process.stderr.write(e.stderr);
    if (!e.stderr) console.error(e.message ?? "caveman-proxy learn failed");
    process.exit(e.status ?? 1);
  }
}

function proxyExecLearnAsync(proxyArgs: string[], onProgress?: (message: string) => void): Promise<string> {
  const seconds = learnTimeoutSeconds();
  return new Promise((resolve, reject) => {
    const child = spawn(proxyBin(), proxyArgs, {
      env: process.env,
      stdio: ["ignore", "pipe", "pipe"],
    });
    let stdout = "";
    let stderr = "";
    let progressBuffer = "";
    let timedOut = false;
    child.stdout.setEncoding("utf8");
    child.stderr.setEncoding("utf8");
    child.stdout.on("data", (chunk: string) => { stdout += chunk; });
    child.stderr.on("data", (chunk: string) => {
      stderr += chunk;
      if (!onProgress) return;
      const lines = `${progressBuffer}${chunk}`.split(/\r?\n|\r/);
      progressBuffer = lines.pop() ?? "";
      for (const line of lines) {
        const message = stripAnsi(line).replace(/\s+/g, " ").trim();
        if (message) onProgress(compactLearnText(message, 90));
      }
    });

    const timer = setTimeout(() => {
      timedOut = true;
      child.kill("SIGTERM");
      const force = setTimeout(() => child.kill("SIGKILL"), 750);
      force.unref();
    }, seconds * 1000);
    timer.unref();

    child.once("error", (error) => {
      clearTimeout(timer);
      reject(error);
    });
    child.once("close", (code) => {
      clearTimeout(timer);
      if (timedOut) {
        reject(new Error(`learn scan timed out after ${seconds}s — no score computed; re-run with \`caveman learn --json\` to capture the raw scan`));
        return;
      }
      if (code !== 0) {
        reject(new Error(stderr.trim() || `caveman-proxy learn failed with exit code ${code ?? "unknown"}`));
        return;
      }
      resolve(stdout);
    });
  });
}

export function learnReportOpener(
  report: string,
  platform: NodeJS.Platform = process.platform,
): { command: string; args: string[] } {
  if (platform === "darwin") return { command: "open", args: [report] };
  if (platform === "win32") {
    return { command: "rundll32.exe", args: ["url.dll,FileProtocolHandler", report] };
  }
  return { command: "xdg-open", args: [report] };
}

function openLearnReport(report: string): void {
  try {
    accessSync(report, constants.R_OK);
  } catch {
    process.stderr.write(`visual report not found: ${report}\n`);
    return;
  }
  const opener = learnReportOpener(report);
  if (!which(opener.command)) {
    process.stderr.write(`cannot open visual report; open this file: ${report}\n`);
    return;
  }
  const child = spawn(opener.command, opener.args, { detached: true, stdio: "ignore", windowsHide: true });
  child.once("error", () => {
    process.stderr.write(`cannot open visual report; open this file: ${report}\n`);
  });
  child.unref();
}

function renderLearnApply(raw: Record<string, any>, dryRun: boolean): string {
  const candidate = (raw.candidate && typeof raw.candidate === "object" ? raw.candidate : {}) as Record<string, any>;
  const klass = String(raw.class ?? candidate.class ?? "");
  if (klass === "behavioral" || klass === "load_bearing") {
    return "behavioral finding — no automatic fix; the caveman-learn skill turns this into a consent-gated nudge\n";
  }
  const lines = [
    String(candidate.title ?? raw.sink_id ?? "learn candidate"),
    `sink: ${String(raw.sink_id ?? candidate.sink_id ?? "")}`,
  ];
  const locations = candidate.what_to_offload?.locators ?? candidate.evidence?.locators;
  if (locations) lines.push(`locations: ${JSON.stringify(locations)}`);
  if (candidate.expected_tokens_per_turn_saved != null) {
    lines.push(`expected: ~${humanTokens(Number(candidate.expected_tokens_per_turn_saved))} tokens/turn`);
  }
  lines.push("gates: net-token-negative · never-dumber");
  if (dryRun) lines.push("nothing changed — this is a preview");
  else {
    lines.push(`prepared, not applied — ${String(raw.candidate_path ?? join(cavemanHome(), "candidates", `learn-${raw.sink_id}.json`))}`);
    lines.push("the only thing that applies it: caveman tools skills install caveman-learn");
  }
  return `${lines.join("\n")}\n`;
}

function learnUsage(): void {
  console.log(`${invokedAs()} learn [--all|--plain|--json|--md] [--since 30d] [--sources codex,claude,caveman]
  default       interactive setup score + grouped top moves
  --plain       compact text; no animation or keyboard menu
  --all         every finding, internal id, basis, and suggestion
  --json|--md   machine-readable or detailed Markdown output
  implement     open Claude Code or Codex to review and fix findings
  apply         prepare one finding for consent-gated editing`);
}

function learnImplementUsage(): void {
  console.log(`${invokedAs()} learn implement [claude|codex] [--prompt "<focus>"]
  opens an interactive agent with the current local learn report
  installs the caveman-learn safety guide when missing
  never edits load-bearing findings; asks before every edit`);
}

function learnImplementPrompt(focus: string): string {
  const lines = [
    "Use the caveman-learn skill.",
    "Run `caveman learn report --json`; if no current report exists, run `caveman learn --json` once and retry. Then present a short list of actionable findings.",
    "Work through selected fixes one at a time. Never edit load_bearing findings.",
    "Show the proposed diff and before → after token count, ask before every edit, apply only approved changes, then verify the reduction and any recall path.",
    "Keep every local savings claim labeled inferred and never attach currency.",
  ];
  if (focus) lines.push(`User focus: ${focus}`);
  return lines.join(" ");
}

function ensureLearnAgentGuide(agent: AgentProfile): string {
  const path = agent.id === "claude"
    ? join(process.cwd(), ".claude", "skills", "caveman-learn", "SKILL.md")
    : join(homedir(), ".codex", "skills", "caveman-learn", "SKILL.md");
  try {
    readFileSync(path);
    return "";
  } catch (error) {
    if ((error as NodeJS.ErrnoException).code !== "ENOENT") {
      console.error(`caveman learn implement: cannot read ${path}: ${(error as Error).message}`);
      process.exit(1);
    }
  }
  try {
    mkdirSync(dirname(path), { recursive: true });
    writeFileSync(path, SKILLS["caveman-learn"]!);
  } catch (error) {
    console.error(`caveman learn implement: cannot install guide at ${path}: ${(error as Error).message}`);
    process.exit(1);
  }
  return path;
}

async function chooseLearnAgent(requested: string): Promise<AgentProfile> {
  const supported = AGENTS.filter((agent) => agent.id === "claude" || agent.id === "codex");
  if (requested) {
    const agent = supported.find((item) => item.id === requested || item.binary_names.includes(requested));
    if (!agent) {
      console.error(`caveman learn implement: supported agents are claude and codex (got ${requested})`);
      process.exit(2);
    }
    if (!which(binOf(agent))) {
      wrapNotFoundUI(requested, agent);
      process.exit(127);
    }
    return agent;
  }

  const installed = supported.filter((agent) => which(binOf(agent)));
  if (installed.length === 0) {
    panel("No implementation agent found", supported.map((agent) => `${agent.display_name}  install: ${agent.install}`));
    process.exit(127);
  }
  if (installed.length === 1) return installed[0]!;
  if (!learnTuiTerminal()) {
    console.error(`usage: ${invokedAs()} learn implement [claude|codex]`);
    process.exit(2);
  }
  const learnTui = await import("./learn-tui.js");
  const selected = await learnTui.selectLearnAgent(installed.map((agent) => ({
    value: agent.id,
    label: agent.display_name,
    hint: agent.id,
  })));
  if (!selected) process.exit(130);
  return installed.find((agent) => agent.id === selected)!;
}

async function learnImplement(rest: string[]): Promise<void> {
  if (rest.includes("--help") || rest.includes("-h")) return learnImplementUsage();
  const separator = rest.indexOf("--");
  const beforeSeparator = separator >= 0 ? rest.slice(0, separator) : rest;
  const requested = flagFrom(beforeSeparator, "--agent", "")
    || positionalAfterOptions(beforeSeparator, new Set(["--agent", "--prompt"]))
    || "";
  const focus = flagFrom(beforeSeparator, "--prompt", "")
    || (separator >= 0 ? rest.slice(separator + 1).join(" ") : "");
  const agent = await chooseLearnAgent(requested);
  const installed = ensureLearnAgentGuide(agent);
  if (installed) process.stderr.write(`${mark("ok")} caveman-learn guide installed: ${cyan(installed)}\n`);
  await wrap([agent.id, learnImplementPrompt(focus.trim())]);
}

function learnTuiTerminal(): boolean {
  return interactive()
    && !!process.stdout.isTTY
    && process.env.TERM !== "dumb"
    && !envTruthy(process.env.CAVEMAN_PLAIN);
}

function learnTuiEnabled(rest: string[]): boolean {
  return learnTuiTerminal()
    && !rest.some((arg) => ["--plain", "--json", "--md", "--all", "--verbose"].includes(arg));
}

// learn is the porcelain setup profiler. Machine modes stay clean; terminal
// mode renders one of three history-graded states.
async function learn(rest: string[]) {
  const sub = rest[0];
  if (sub === "--help" || sub === "-h" || sub === "help") return learnUsage();
  if (sub === "implement") return learnImplement(rest.slice(1));
  if (sub === "apply") {
    const json = rest.includes("--json");
    const rawText = proxyExecLearn(["learn", ...rest], false);
    if (json) {
      process.stdout.write(rawText);
      return;
    }
    const parsed = JSON.parse(rawText) as Record<string, any>;
    process.stdout.write(renderLearnApply(parsed, rest.includes("--dry-run")));
    return;
  }
  if (sub === "scan" || sub === "report") return proxyPassthrough(["learn", ...rest]);

  const json = rest.includes("--json");
  const markdown = rest.includes("--md");
  const verbose = rest.includes("--all") || rest.includes("--verbose");
  const tui = learnTuiEnabled(rest);
  const forwarded = rest.filter((arg) => !["--json", "--md", "--all", "--verbose", "--plain"].includes(arg));
  const reportBefore = learnReportMtime();
  const reportToken = randomUUID();
  let planRaw: string;
  if (tui) {
    const learnTui = await import("./learn-tui.js");
    const progress = learnTui.createLearnProgress();
    progress.start("Reading Claude Code and Codex sessions");
    try {
      const scanRaw = await proxyExecLearnAsync(
        ["learn", "scan", "--write-report", "--write-report-token", reportToken, ...forwarded],
        progress.update,
      );
      // Pre-bundled proxies ignore unknown flags. Fall back once when report
      // token/mtime proves this proxy did not write artifacts from the scan plan.
      planRaw = learnReportGeneration() === reportToken || learnReportMtime() > reportBefore
        ? scanRaw
        : await proxyExecLearnAsync(["learn", "report", "--json", ...forwarded]);
    } catch (error) {
      progress.fail("Learn scan failed");
      throw error;
    }
    const plan = JSON.parse(planRaw) as LearnPlan;
    const diff = readLearnDiff(plan);
    const sessions = Math.max(0, Number(plan.sessions_scanned ?? 0));
    progress.stop(`${sessions} session${sessions === 1 ? "" : "s"} analyzed`);
    const report = learnReportPath();
    const result = await learnTui.renderLearnTui(buildLearnTuiModel(plan, {
      report,
      ...(diff ? { diff } : {}),
    }));
    if (result.action === "implement") {
      return learnImplement(result.focus ? ["--prompt", result.focus] : []);
    }
    if (result.action === "details") {
      process.stdout.write(renderLearnPlan(plan, {
        report,
        verbose: true,
        ...(diff ? { diff } : {}),
      }));
      return;
    }
    if (result.action === "report") openLearnReport(report);
    return;
  } else {
    if (!json && !markdown && interactive()) process.stderr.write("Learning from local sessions…\n");
    const scanRaw = proxyExecLearn(
      ["learn", "scan", "--write-report", "--write-report-token", reportToken, ...forwarded],
      !json && !markdown,
    );
    planRaw = learnReportGeneration() === reportToken || learnReportMtime() > reportBefore
      ? scanRaw
      : proxyExecLearn(["learn", "report", "--json", ...forwarded], false);
  }
  if (json) {
    process.stdout.write(planRaw);
    return;
  }
  const plan = JSON.parse(planRaw) as LearnPlan;
  const diff = readLearnDiff(plan);
  process.stdout.write(renderLearnPlan(plan, {
    markdown,
    report: learnReportPath(),
    verbose,
    ...(diff ? { diff } : {}),
  }));
}

// resolveCavememCommand resolves the cavemem binary the same way mcp does:
// explicit env, then PATH, then ~/.caveman/bin. The CLI only ever shells out to it (no
// runtime deps), so recall/scoring logic stays in the gated Go core.
function resolveCavememCommand(): { command: string; args: string[] } {
  return { command: cavemanBin("cavemem", "CAVEMEM_BIN"), args: [] };
}

// cavememRun shells to cavemem and returns its stdout. On failure it surfaces the
// error honestly and exits — except in soft mode (the recall hook), where it
// returns null so the hook can no-op without ever blocking the agent.
function cavememRun(memArgs: string[], opts: { soft?: boolean } = {}): string | null {
  const { command, args: pre } = resolveCavememCommand();
  try {
    return execFileSync(command, [...pre, ...memArgs], { encoding: "utf8", env: process.env });
  } catch (error) {
    const e = error as { stdout?: string; stderr?: string; status?: number; message?: string };
    if (opts.soft) return null;
    if (e.stdout) process.stdout.write(e.stdout);
    if (e.stderr) process.stderr.write(e.stderr);
    if (!e.stdout && !e.stderr) console.error("cavemem not found; set CAVEMEM_BIN or build public/mem");
    process.exit(e.status ?? 1);
  }
  return null;
}

// mem is the durable-memory command family (cavemem). remember/recall/recover/
// forget are mechanical store ops; the editing skill and the opt-in recall hook
// build on them. Memory edits to a user's config are NEVER done here — only the
// consent-gated skill (with the agent's file tools) does that.
function mem(rest: string[]) {
  const sub = rest[0] ?? "";
  switch (sub) {
    case "remember": {
      // `--` ends option parsing: everything after it is literal text, so a
      // block that opens with a `---` rule survives verbatim. Without a `--`,
      // fall back to dropping flag-looking args (remember takes none) for
      // backward compatibility.
      const args = rest.slice(1);
      const sep = args.indexOf("--");
      const text = (sep >= 0 ? args.slice(sep + 1) : args.filter((a) => !a.startsWith("--"))).join(" ");
      if (!text) { console.error(`usage: ${invokedCommand("mem")} remember [--] <text>`); process.exit(2); }
      process.stdout.write(cavememRun(["remember", text]) ?? "");
      return;
    }
    case "recall": {
      // Find the first positional, skipping --limit and the value it consumes.
      const after = rest.slice(1);
      let query: string | undefined;
      for (let i = 0; i < after.length; i++) {
        const a = after[i];
        if (a === "--limit") { i++; continue; }
        if (a && !a.startsWith("--")) { query = a; break; }
      }
      if (!query) { console.error(`usage: ${invokedCommand("mem")} recall <query> [--limit N]`); process.exit(2); }
      const limit = flagFrom(rest, "--limit", "");
      process.stdout.write(cavememRun(limit ? ["recall", query, limit] : ["recall", query]) ?? "");
      return;
    }
    case "forget": {
      const id = rest[1];
      if (!id) { console.error(`usage: ${invokedCommand("mem")} forget <id>`); process.exit(2); }
      process.stdout.write(cavememRun(["forget", id]) ?? "");
      return;
    }
    case "supersede": {
      const id = rest[1];
      // Same `--` end-of-options handling as remember: a replacement block that
      // opens with a `---` rule must survive verbatim instead of filtering empty.
      const rest2 = rest.slice(2);
      const sep = rest2.indexOf("--");
      const text = (sep >= 0 ? rest2.slice(sep + 1) : rest2.filter((a) => !a.startsWith("--"))).join(" ");
      if (!id || !text) { console.error(`usage: ${invokedCommand("mem")} supersede <id> [--] <text>`); process.exit(2); }
      process.stdout.write(cavememRun(["supersede", id, text]) ?? "");
      return;
    }
    case "history": {
      const id = rest[1];
      if (!id) { console.error(`usage: ${invokedCommand("mem")} history <id>`); process.exit(2); }
      process.stdout.write(cavememRun(["history", id]) ?? "");
      return;
    }
    case "recover": {
      const handle = rest[1];
      if (!handle) { console.error(`usage: ${invokedCommand("mem")} recover <handle>`); process.exit(2); }
      return memRecover(handle);
    }
    case "recall-hook":
      return memRecallHook();
    case "hook":
      return memHook(rest.slice(1));
    default:
      return memUsage();
  }
}

// memRecover writes the byte-exact original (Buffer, no utf8 round-trip) for a
// recall hit's recovery_handle, via cavemem's own CCR store.
function memRecover(handle: string) {
  const { command, args: pre } = resolveCavememCommand();
  try {
    process.stdout.write(execFileSync(command, [...pre, "recover", handle], { env: process.env }));
  } catch (error) {
    const e = error as { stdout?: Buffer; stderr?: Buffer; status?: number; message?: string };
    if (e.stdout) process.stdout.write(e.stdout);
    if (e.stderr) process.stderr.write(e.stderr);
    if (!e.stdout && !e.stderr) console.error("cavemem not found; set CAVEMEM_BIN or build public/mem");
    process.exit(e.status ?? 1);
  }
}

function memUsage() {
  console.log(`caveman mem — durable agent memory (cavemem)
  caveman mem remember <text>              store a memory
  caveman mem recall <query> [--limit N]   recall memories (lexical, conservative threshold)
  caveman mem supersede <id> <text>        replace a current fact; preserve history
  caveman mem history <id>                 show oldest-to-newest versions
  caveman mem recover <handle>             byte-exact original behind a recall hit
  caveman mem forget <id>                  delete a memory
  caveman mem hook install [agent]         opt in to auto-recall on each prompt (off by default)
  caveman mem hook uninstall [agent]       remove the auto-recall hook
Memories are stored compressed; every recall reports its inferred token cost.`);
}

function usage(rest: string[]) {
  const sub = rest[0] ?? "";
  const provider = rest[1] ?? "";
  if (sub === "import") return proxyPassthrough(["usage", ...rest]);
  if (sub === "refresh") return proxyPassthrough(["usage", ...rest], usageRefreshEnv(provider || "claude"));
  if (sub === "unlink") {
    if (!["claude", "anthropic", "codex", "openai"].includes(provider)) {
      console.error(`usage: ${invokedCommand("usage")} unlink claude|codex`);
      process.exit(2);
    }
    const out = proxyExec(["usage", ...rest], process.env, false);
    if (provider === "claude" || provider === "anthropic") {
      usageSecretDelete("claude-session-key");
      usageSecretDelete("claude-org-id");
    }
    process.stdout.write(out);
    return;
  }
  if (sub === "link") {
    if (provider === "codex") {
      const refresh = parseJSONMaybe(proxyExec(["usage", ...rest], process.env, false));
      print({ linked: "codex", source: "local_rate_limits", token_store: "none", refresh });
      return;
    }
    if (provider !== "claude") {
      console.error(`usage: ${invokedCommand("usage")} link claude|codex [--session-key <key>] [--org-id <id>]`);
      process.exit(2);
    }
    const sessionKey = flagFrom(rest, "--session-key", "");
    const orgID = flagFrom(rest, "--org-id", "");
    if ((sessionKey && !orgID) || (!sessionKey && orgID)) {
      console.error("Claude link needs both --session-key and --org-id, or neither when using CAVEMAN_CLAUDE_USAGE_JSON.");
      process.exit(2);
    }
    if (process.env.CAVEMAN_CLAUDE_USAGE_JSON && (sessionKey || orgID)) {
      console.error("Claude link accepts either --session-key plus --org-id, or CAVEMAN_CLAUDE_USAGE_JSON, not both.");
      process.exit(2);
    }
    if (!process.env.CAVEMAN_CLAUDE_USAGE_JSON && (!sessionKey || !orgID)) {
      console.error("Claude link needs --session-key plus --org-id, or CAVEMAN_CLAUDE_USAGE_JSON for one-shot refresh.");
      process.exit(2);
    }
    const refreshEnv = { ...process.env };
    if (sessionKey) refreshEnv.CAVEMAN_CLAUDE_SESSION_KEY = sessionKey;
    if (orgID) refreshEnv.CAVEMAN_CLAUDE_ORG_ID = orgID;
    const refresh = parseJSONMaybe(proxyExec(["usage", "link", "claude"], refreshEnv, false));
    let tokenStore: TokenStore | "env" = "env";
    if (sessionKey) tokenStore = usageSecretSet("claude-session-key", sessionKey);
    if (orgID) usageSecretSet("claude-org-id", orgID);
    print({ linked: "claude", token_store: tokenStore, basis: "linked_api", refresh });
    return;
  }
  console.error(`usage: ${invokedCommand("usage")} import|link|refresh|unlink ...`);
  process.exit(2);
}

function parseJSONMaybe(raw: string): unknown {
  try {
    return JSON.parse(raw);
  } catch {
    return raw.trim();
  }
}

function proxyBin(): string {
  return cavemanBin("caveman-proxy", "CAVEMAN_PROXY_BIN");
}

function proxyPassthrough(proxyArgs: string[], env: NodeJS.ProcessEnv = process.env) {
  const out = proxyExec(proxyArgs, env, false);
  process.stdout.write(out);
}

function proxyExec(proxyArgs: string[], env: NodeJS.ProcessEnv, quiet: boolean): string {
  try {
    return execFileSync(proxyBin(), proxyArgs, { encoding: "utf8", env });
  } catch (error) {
    const e = error as { stdout?: string; stderr?: string; status?: number; message?: string };
    if (!quiet) {
      if (e.stdout) process.stdout.write(e.stdout);
      if (e.stderr) process.stderr.write(e.stderr);
      if (!e.stdout && !e.stderr) console.error(e.message ?? "caveman-proxy failed");
    }
    process.exit(e.status ?? 1);
  }
}

function proxyExecMaybe(proxyArgs: string[]) {
  try {
    execFileSync(proxyBin(), proxyArgs, { stdio: "ignore", env: process.env });
  } catch {
    // History imports are opportunistic in `trial --learn`; the trial report still
    // reflects the proxied run if a local history source is absent.
  }
}

function usageRefreshEnv(provider: string): NodeJS.ProcessEnv {
  if (provider !== "claude" && provider !== "anthropic" && provider !== "") return process.env;
  const env = { ...process.env };
  if (!env.CAVEMAN_CLAUDE_SESSION_KEY) env.CAVEMAN_CLAUDE_SESSION_KEY = usageSecretGet("claude-session-key");
  if (!env.CAVEMAN_CLAUDE_ORG_ID) env.CAVEMAN_CLAUDE_ORG_ID = usageSecretGet("claude-org-id");
  return env;
}

function freePort(): Promise<number> {
  return new Promise((resolve, reject) => {
    const server = netCreateServer();
    server.on("error", reject);
    server.listen(0, "127.0.0.1", () => {
      const addr = server.address() as AddressInfo;
      server.close(() => resolve(addr.port));
    });
  });
}

async function waitForPort(host: string, port: number, timeoutMs: number): Promise<void> {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    if (await portListening(host, port)) return;
    await sleep(100);
  }
  throw new Error(`caveman trial proxy did not become ready on ${host}:${port}`);
}

function waitForChild(child: ReturnType<typeof spawn>, timeoutMs: number): Promise<void> {
  return new Promise((resolve) => {
    let done = false;
    const finish = () => {
      if (done) return;
      done = true;
      resolve();
    };
    child.once("exit", finish);
    child.once("close", finish);
    setTimeout(() => {
      try { child.kill("SIGKILL"); } catch {}
      finish();
    }, timeoutMs).unref();
  });
}

function flagFrom(values: string[], name: string, fallback: string) {
  const index = values.indexOf(name);
  if (index >= 0) return values[index + 1] ?? fallback;
  const prefixed = values.find((v) => v.startsWith(name + "="));
  return prefixed ? prefixed.slice(name.length + 1) : fallback;
}

function positionalAfterOptions(values: string[], optionsWithValues: Set<string>): string | undefined {
  for (let i = 0; i < values.length; i++) {
    const value = values[i]!;
    if (optionsWithValues.has(value)) {
      i++;
      continue;
    }
    if ([...optionsWithValues].some((name) => value.startsWith(`${name}=`))) continue;
    if (!value.startsWith("-")) return value;
  }
  return undefined;
}

function usageSecretSet(account: string, value: string): TokenStore {
  if (process.platform === "darwin" && !process.env.CAVE_NO_KEYCHAIN && genericKeychainSet("caveman-usage", account, value)) {
    return "keychain";
  }
  mkdirSync(usageSecretDir(), { recursive: true });
  writeFileSync(join(usageSecretDir(), account), value, { mode: 0o600 });
  return "file";
}

function usageSecretGet(account: string): string {
  if (process.platform === "darwin" && !process.env.CAVE_NO_KEYCHAIN) {
    const got = genericKeychainGet("caveman-usage", account);
    if (got) return got;
  }
  try {
    return readFileSync(join(usageSecretDir(), account), "utf8").trim();
  } catch {
    return "";
  }
}

function usageSecretDelete(account: string) {
  if (process.platform === "darwin" && !process.env.CAVE_NO_KEYCHAIN) {
    genericKeychainDelete("caveman-usage", account);
  }
  try {
    unlinkSync(join(usageSecretDir(), account));
  } catch (error) {
    if ((error as NodeJS.ErrnoException).code !== "ENOENT") throw error;
  }
}

function usageSecretDir() {
  return join(caveHome(), "usage");
}

function readStdin(): Promise<Buffer> {
  return new Promise((resolve, reject) => {
    const chunks: Buffer[] = [];
    process.stdin.on("data", (chunk) => chunks.push(chunk));
    process.stdin.on("end", () => resolve(Buffer.concat(chunks)));
    process.stdin.on("error", reject);
  });
}

async function init(argv: string[]) {
  // Reuse login's own resolution (C8 review finding 6): init used to compute
  // its own baseURL with a bare localhost:8080 literal and no CAVE_API_URL
  // precedence at all, so after `login(argv)` correctly authenticated against
  // prod (or an explicit CAVE_API_URL), init would silently persist a
  // DIFFERENT, incoherent baseURL — a prod/explicit token stored against a
  // localhost config. resolveLoginBaseUrl is exactly what login(argv) itself
  // uses, so the two can no longer disagree.
  const baseURL = resolveLoginBaseUrl(argv);
  await login(argv);
  const projects = await get("/api/v1/projects");
  const first = projects.data?.[0];
  await saveConfig({ ...(await config()), baseURL, projectId: first?.id });
  await writeFile(".env.cave", `CAVE_API_URL=${baseURL}\nCAVE_PROJECT_ID=${first?.id ?? ""}\n`, { mode: 0o600 });
  sdkSnippet();
}

type ProxyRuntimeState = {
  owner: "wrap" | "start" | "unknown";
  mode?: string;
  instance_token?: string;
  pid?: number;
  port?: number;
  started_at?: string;
  version?: string;
  recovery_via_mcp?: boolean;
};

function proxyRuntimeMatches(
  runtime: ProxyRuntimeState,
  mode: WrapRuntimeMode,
  recoveryViaMCP: boolean,
): boolean {
  return runtime.owner !== "unknown"
    && runtime.mode === mode
    && proxyRuntimeGateMatches(runtime, recoveryViaMCP);
}

// The only cross-session gate input left is the recovery contract (the account
// one was removed): reusing a proxy that lacks the agent's caveman_retrieve tool
// would elide bytes nothing can expand.
function proxyRuntimeGateMatches(
  runtime: ProxyRuntimeState,
  recoveryViaMCP: boolean,
): boolean {
  return runtime.recovery_via_mcp === recoveryViaMCP;
}

function proxyRunStatePath(port: number): string {
  return join(cavemanHome(), "run", `${port}.json`);
}

function proxySessionDir(port: number): string {
  return join(cavemanHome(), "run", `${port}.sessions`);
}

function readRawProxyRunState(port: number): ProxyRuntimeState {
  try {
    const parsed = JSON.parse(readFileSync(proxyRunStatePath(port), "utf8")) as Record<string, unknown>;
    if (parsed.schema !== "caveman.proxy.run.v1") return { owner: "unknown" };
    if (parsed.owner !== "wrap" && parsed.owner !== "start") return { owner: "unknown" };
    if (typeof parsed.instance_token !== "string" || typeof parsed.pid !== "number") return { owner: "unknown" };
    return {
      owner: parsed.owner,
      ...(typeof parsed.mode === "string" ? { mode: parsed.mode } : {}),
      instance_token: parsed.instance_token,
      pid: parsed.pid,
      ...(typeof parsed.port === "number" ? { port: parsed.port } : {}),
      ...(typeof parsed.started_at === "string" ? { started_at: parsed.started_at } : {}),
      ...(typeof parsed.version === "string" ? { version: parsed.version } : {}),
      ...(typeof parsed.recovery_via_mcp === "boolean" ? { recovery_via_mcp: parsed.recovery_via_mcp } : {}),
    };
  } catch {
    return { owner: "unknown" };
  }
}

function processAlive(pid: number): boolean {
  try {
    process.kill(pid, 0);
    return true;
  } catch (error) {
    return (error as NodeJS.ErrnoException).code === "EPERM";
  }
}

function createProxySessionMarker(port: number): string | null {
  const dir = proxySessionDir(port);
  try {
    mkdirSync(dir, { recursive: true, mode: 0o700 });
    chmodSync(dir, 0o700);
  } catch {
    return null;
  }
  for (let attempt = 0; attempt < 4; attempt++) {
    const marker = join(dir, `${process.pid}-${randomUUID()}`);
    try {
      writeFileSync(marker, `${new Date().toISOString()}\n`, { flag: "wx", mode: 0o600 });
      return marker;
    } catch (error) {
      if ((error as NodeJS.ErrnoException).code !== "EEXIST") return null;
    }
  }
  return null;
}

function removeProxySessionMarker(marker: string | null): void {
  if (!marker) return;
  try {
    unlinkSync(marker);
  } catch (error) {
    if ((error as NodeJS.ErrnoException).code !== "ENOENT") {
      // Marker cleanup is best-effort; the next reader prunes a dead owner.
    }
  }
}

function countOtherLiveProxySessions(port: number, ownMarker: string | null): number {
  let names: string[];
  try {
    names = readdirSync(proxySessionDir(port));
  } catch {
    return 0;
  }
  let live = 0;
  for (const name of names) {
    const marker = join(proxySessionDir(port), name);
    if (ownMarker && marker === ownMarker) continue;
    const match = /^(\d+)-/.exec(name);
    const pid = match ? Number(match[1]) : NaN;
    if (!Number.isSafeInteger(pid) || pid <= 0 || !processAlive(pid)) {
      try {
        unlinkSync(marker);
      } catch {
        // Concurrent cleanup or a read-only directory: neither upgrades ownership.
      }
      continue;
    }
    live++;
  }
  return live;
}

function proxyRestartTimeoutMs(): number {
  const seconds = Number(process.env.CAVE_PROXY_RESTART_TIMEOUT ?? "10");
  if (!Number.isFinite(seconds)) return 10_000;
  return Math.max(100, Math.min(60_000, Math.round(seconds * 1000)));
}

type StatusView = {
  mode: string | null;
  mode_source: "running" | "resolved";
  owner: ProxyRuntimeState["owner"];
  off_states: OffState[];
  today: ProxyObserveSummary | null;
  mem_blocks: number | null;
  seat: Record<string, unknown>;
  plan: Record<string, unknown> | null;
  config_sources: Record<"think" | "remember" | "execute", string>;
  telemetry: { state: "on" | "off"; change: string };
  next: string | null;
};

function probeProxyVersion(): { version: string; capabilities: string[] } | null {
  const binary = resolveGoBin("caveman-proxy", "CAVEMAN_PROXY_BIN");
  if (!binary) return null;
  try {
    const raw = execFileSync(binary, ["version", "--json"], {
      encoding: "utf8",
      env: process.env,
      timeout: 2000,
      stdio: ["ignore", "pipe", "ignore"],
    });
    const parsed = JSON.parse(raw) as { version?: unknown; capabilities?: unknown };
    return {
      version: typeof parsed.version === "string" ? parsed.version : "unknown",
      capabilities: Array.isArray(parsed.capabilities)
        ? parsed.capabilities.filter((item): item is string => typeof item === "string")
        : [],
    };
  } catch {
    return { version: "pre-run-state", capabilities: [] };
  }
}

function readProxyRuntimeState(port: number, versionInfo: ReturnType<typeof probeProxyVersion>): ProxyRuntimeState {
  if (!versionInfo?.capabilities.includes("run_state")) return { owner: "unknown" };
  const binary = resolveGoBin("caveman-proxy", "CAVEMAN_PROXY_BIN");
  if (!binary) return { owner: "unknown" };
  try {
    const raw = execFileSync(binary, ["status", "--json", "--port", String(port)], {
      encoding: "utf8",
      env: process.env,
      timeout: 2000,
      stdio: ["ignore", "pipe", "ignore"],
    });
    const parsed = JSON.parse(raw) as Record<string, unknown>;
    if (parsed.owner !== "wrap" && parsed.owner !== "start") return { owner: "unknown" };
    return {
      owner: parsed.owner,
      ...(typeof parsed.mode === "string" ? { mode: parsed.mode } : {}),
      ...(typeof parsed.instance_token === "string" ? { instance_token: parsed.instance_token } : {}),
      ...(typeof parsed.pid === "number" ? { pid: parsed.pid } : {}),
      ...(typeof parsed.port === "number" ? { port: parsed.port } : {}),
      ...(typeof parsed.started_at === "string" ? { started_at: parsed.started_at } : {}),
      ...(typeof parsed.version === "string" ? { version: parsed.version } : {}),
      ...(typeof parsed.recovery_via_mcp === "boolean" ? { recovery_via_mcp: parsed.recovery_via_mcp } : {}),
    };
  } catch {
    return { owner: "unknown" };
  }
}

function capabilitySourcesForStatus(): StatusView["config_sources"] {
  const resolution = resolveCapabilities();
  const sourceOrder: CapabilitySource[] = ["proxy-yaml", "legacy-wrap", "global", "project", "env"];
  const sourceFor = (group: "think" | "remember" | "execute") => {
    const found = new Set(
      CAPABILITY_KEYS
        .filter((key) => key.startsWith(`${group}.`))
        .map((key) => resolution.values[key].source),
    );
    const nonDefault = sourceOrder.filter((source) => found.has(source));
    return nonDefault.length ? nonDefault.join("+") : "default";
  };
  return {
    think: sourceFor("think"),
    remember: sourceFor("remember"),
    execute: sourceFor("execute"),
  };
}

function localMidnightRFC3339(): string {
  const now = new Date();
  return new Date(now.getFullYear(), now.getMonth(), now.getDate()).toISOString();
}

function weeklyAllowance(plan: string): number | null {
  if (plan === "free") return 5_000_000;
  if (plan === "indie") return 50_000_000;
  return null;
}

function readLearnSnapshot(): { moves: number; sessions: number; stateOne: boolean } {
  try {
    const raw = JSON.parse(readFileSync(join(cavemanHome(), "reports", "caveman-learn.json"), "utf8")) as Record<string, unknown>;
    const sinks = Array.isArray(raw.sinks) ? raw.sinks as Array<Record<string, unknown>> : [];
    return {
      moves: typeof raw.moves === "number" ? raw.moves : sinks.length,
      sessions: typeof raw.sessions_scanned === "number" ? raw.sessions_scanned : 0,
      stateOne: sinks.some((sink) => sink.class === "recurring_context"),
    };
  } catch {
    return { moves: 0, sessions: 0, stateOne: false };
  }
}

function refreshOffline(): boolean {
  try {
    const raw = JSON.parse(readFileSync(configPath(), "utf8")) as Record<string, unknown>;
    const refresh = raw.wrapEntitlementRefresh;
    return !!refresh && typeof refresh === "object" && !Array.isArray(refresh)
      && (refresh as Record<string, unknown>).ok === false;
  } catch {
    return false;
  }
}

function statusRow(label: string, value: string): string {
  return `${label.padEnd(11)}${value}`;
}

export function renderStatus(view: StatusView): string {
  const displayMode = view.mode === "compress" ? "compress on" : view.mode ?? "unknown";
  const lines = [`caveman  ·  ${displayMode}`];
  if (view.off_states.length === 0) lines.push("no off-states — everything the layer can do is on");
  else lines.push(...view.off_states.map((state) => state.line));
  lines.push("");

  if (view.today && Number(view.today.spans ?? 0) > 0) {
    lines.push(statusRow("today", `${humanTokens(Number(view.today.tokens_in ?? 0))} tokens observed on the layer`));
    const accounting = view.today.token_accounting ?? {};
    const mix = ["provider_complete", "provider_partial", "provider_malformed", "unavailable"]
      .filter((key) => Number(accounting[key] ?? 0) > 0)
      .map((key) => `${accounting[key]} ${key}`)
      .join(" / ");
    lines.push(`         basis: inferred (local counters${mix ? ` · ${mix}` : ""})`);
    const cut = view.mode === "compress"
      ? Number(view.today.compression_tokens_saved ?? 0)
      : Number(view.today.would_save_tokens ?? 0);
    if (cut > 0) {
      lines.push(`         ~${humanTokens(cut)} tokens/day ${view.mode === "compress" ? "cut locally" : "would-have-saved"}`);
      const dollars = view.mode === "compress" ? Number(view.today.savings_usd ?? 0) : view.today.would_save_usd;
      const priced = typeof dollars === "number" && dollars > 0 ? ` · about $${dollars.toFixed(2)} list-price subtotal` : "";
      lines.push(`         basis: inferred (local o200k estimate, not billed spend)${priced}`);
    }
  } else if (view.owner !== "unknown" || !view.off_states.some((state) => state.id === "binary-missing")) {
    lines.push("nothing has run on the layer yet — try `caveman claude`");
  }
  if (view.mem_blocks !== null && view.mem_blocks > 0) lines.push(statusRow("mem", `${view.mem_blocks} blocks`));

  if (view.seat.signed_in === true) {
    if (view.seat.entitled === true) {
      const limit = view.seat.seats_limit == null ? "∞" : String(view.seat.seats_limit);
      lines.push(statusRow("seat", `${String(view.seat.plan)} · ${String(view.seat.seats_used)} of ${limit} seat · entitlement valid to ${String(view.seat.expires_at).slice(0, 10)}   ·  sign out: caveman logout`));
    } else {
      lines.push(statusRow("seat", "signed in · no active wrap entitlement — cloud sync off, local compression unaffected   ·  sign out: caveman logout"));
    }
  } else {
    lines.push(statusRow("seat", "not signed in"));
  }
  if (view.plan) {
    lines.push(statusRow("plan", `${String(view.plan.plan)} · ${humanTokens(Number(view.plan.used))} of ${humanTokens(Number(view.plan.allowance))} optimized tokens this week · resets Mon 00:00 UTC · connected traffic only`));
  }
  lines.push(statusRow("config", `think: ${view.config_sources.think}  ·  remember: ${view.config_sources.remember}  ·  execute: ${view.config_sources.execute}`));
  lines.push(statusRow("telemetry", `${view.telemetry.state} · anonymous usage ping   ·  change: ${view.telemetry.change}`));
  if (view.next) lines.push("", `next:  ${view.next}`);
  return `${lines.join("\n")}\n`;
}

async function status(argv: string[]) {
  const versionInfo = probeProxyVersion();
  const { host, port } = gatewayHostPort();
  const listening = await portListening(host, port);
  const runtime = readProxyRuntimeState(port, versionInfo);
  const resolution = wrapRuntimeConfig().resolution;
  const requested = resolution.values["think.mode"].value as WrapRuntimeMode;
  const entitlement = readWrapEntitlement();
  const rawMeta = globalCapabilityDocument() as Partial<Config>;
  const signedIn = !!resolveCredentials(rawMeta).access_token;
  const gate = resolveWrapGate(entitlement, new Date(), requested);

  const states: OffState[] = [];
  if (!versionInfo) states.push(fixedOffState("binary-missing", OFF_STATES.binaryMissing));
  if (listening && runtime.owner === "unknown" && versionInfo?.capabilities.includes("run_state")) {
    states.push(OFF_STATES.foreignProcess(host, port));
  }
  if (runtime.owner !== "unknown" && runtime.mode && runtime.mode !== gate.mode) {
    states.push(OFF_STATES.runningModeMismatch(runtime.mode, gate.mode));
  }
  const invalid = resolution.values["think.mode"].invalid;
  if (invalid !== undefined) states.push(OFF_STATES.invalidMode(invalid));
  if (gate.reason === "user-record") states.push(fixedOffState("user-record", OFF_STATES.userRecord));
  const allowance = entitlement ? weeklyAllowance(entitlement.plan) : null;
  if (allowance !== null && entitlement?.optimized_tokens_week !== undefined && entitlement.optimized_tokens_week >= allowance) {
    states.push(OFF_STATES.weeklyCap(humanTokens(entitlement.optimized_tokens_week), humanTokens(allowance)));
  }
  const mcpCompatibility = probeMcpBinary();
  if (mcpCompatibility && !mcpCompatibility.probe.current) {
    states.push(OFF_STATES.staleBinary("caveman-mcp", mcpCompatibility.probe.version, cliVersion()));
  } else if (!startMcpRecoveryAvailable()) {
    states.push(fixedOffState(
      "mcp-missing",
      mcpSurfaceMode(resolution.values["execute.mcp"].value) === "marker-only"
        ? OFF_STATES.mcpMarkerOnlyStandalone
        : OFF_STATES.mcpMissing,
    ));
  }
  if (!resolveGoBin("cavemem", "CAVEMEM_BIN")) states.push(fixedOffState("mem-missing", OFF_STATES.memMissing));
  if (entitlement?.telemetry_level === "zdr") states.push(fixedOffState("zdr", OFF_STATES.zdr));
  if (versionInfo && !versionInfo.capabilities.includes("run_state")) {
    states.push(OFF_STATES.staleBinary("caveman-proxy", versionInfo.version, cliVersion()));
  }
  if (refreshOffline()) states.push(fixedOffState("refresh-offline", OFF_STATES.refreshOffline));

  const today = versionInfo ? readProxyObserveSummary(localMidnightRFC3339()) : null;
  const runningMode = runtime.owner !== "unknown" && runtime.mode ? runtime.mode : null;
  const resolvedMode = gate.mode;
  const snapshot = readLearnSnapshot();
  const history = Number(today?.spans ?? 0) > 0 || snapshot.sessions > 0;
  let next: string | null;
  if (!versionInfo) next = "caveman setup --install";
  else if (!signedIn) next = history && !snapshot.stateOne
    ? "caveman learn"
    : "caveman login   (free · 1 seat · no card)";
  else next = snapshot.moves < 1 ? "caveman learn" : "caveman cloud plan";

  const plan = entitlement && allowance !== null && entitlement.optimized_tokens_week !== undefined
    ? { plan: entitlement.plan, used: entitlement.optimized_tokens_week, allowance }
    : null;
  const telemetry = telemetryState();
  const view: StatusView = {
    mode: runningMode ?? resolvedMode,
    mode_source: runningMode ? "running" : "resolved",
    owner: runtime.owner,
    off_states: orderedOffStates(states),
    today,
    mem_blocks: typeof today?.mem_blocks === "number" ? today.mem_blocks : null,
    seat: signedIn
      ? entitlement
        ? {
            signed_in: true,
            entitled: true,
            plan: entitlement.plan,
            seats_used: entitlement.seats_used,
            seats_limit: entitlement.seats_limit,
            expires_at: entitlement.expires_at,
          }
        : { signed_in: true, entitled: false }
      : { signed_in: false },
    plan,
    config_sources: capabilitySourcesForStatus(),
    telemetry: {
      state: telemetry.state === "on" ? "on" : "off",
      change: "caveman telemetry on|off",
    },
    next,
  };
  const native = (["claude", "codex", "hermes", "gemini", "opencode", "aider"] as const).map((agent) => {
    const integration = nativeIntegrationStatus(agent);
    return {
      ...integration,
      runtime_reachable: listening,
    };
  });
  const integrations = [...native, { ...genericIntegrationStatus(listening), runtime_reachable: listening }];
  if (argv.includes("--json")) {
    print({ ...view, native_integrations: integrations });
    return;
  }
  process.stdout.write(renderStatus(view));
  process.stdout.write("\nnative integrations\n");
  for (const integration of integrations) {
    const active = Object.entries(integration.capabilities).filter(([, value]) => value.active).map(([name]) => name);
    process.stdout.write(statusRow(integration.agent, `${integration.state} · ${integration.version_status} · ${active.join(", ") || "proxy-only/none active"}`) + "\n");
  }
  const degraded = native.find((integration) => integration.state === "degraded");
  const available = native.find((integration) => integration.state === "available" && integration.components.shared_runtime);
  const needsRuntime = native.find((integration) => integration.state === "available" && !integration.components.shared_runtime);
  const installed = native.find((integration) => integration.state === "installed");
  if (degraded) process.stdout.write(`\nnext native:  caveman doctor ${degraded.agent} --fix\n`);
  else if (available) process.stdout.write(`\nnext native:  caveman enable ${available.agent}\n`);
  else if (needsRuntime) process.stdout.write(`\nnext native:  caveman setup --install\nthen:         caveman enable ${needsRuntime.agent}\n`);
  else if (installed) process.stdout.write(`\nnative ready: run ${installed.agent} normally\n`);
}

async function doctor() {
  const status = await get("/api/v1/system/status");
  const me = await get("/api/v1/auth/me");
  print({
    // Derived from the actual status payload, not hardcoded: a real status object
    // (with no error envelope) means the API answered; the telemetry/cache health
    // echo what the server reports for ClickHouse/Valkey.
    "Cave API reachable": !!status && !status.error,
    authenticated_as: me.user?.email,
    "policy cache healthy": status.valkey === "ready",
    "telemetry pipeline healthy": status.clickhouse === "ready",
    "retention mode": "metadata-only",
    "dead-letter jobs": status.dead_letter_jobs
  });
}

// cliVersion reads the published version from package.json (next to the built
// module) instead of a hardcoded literal, so `caveman version` stays in sync on bump.
function cliVersion(): string {
  try {
    const pkgPath = join(dirname(fileURLToPath(import.meta.url)), "..", "package.json");
    return JSON.parse(readFileSync(pkgPath, "utf8")).version ?? "0.0.0";
  } catch {
    return "0.0.0";
  }
}

async function createKey(argv: string[]) {
  const body = await post(`/api/v1/projects/${await projectId()}/keys`, { name: flagFrom(argv, "--name", "cli-key"), scopes: ["proxy:write", "sdk:write"] });
  print(body);
}

async function audit(argv: string[]) {
  if (argv[0] === "import") return auditImport(argv);
  if (argv[0] === "eval-import") return auditEvalImport(argv);
  if (argv[0] === "report") return get(`/api/v1/audits/${argv[1] ?? "aud_demo"}`).then(print);
  return post("/api/v1/audits", { last: flagFrom(argv, "--last", "7d") }).then(print);
}

// auditImport reads a telemetry export file and POSTs it to /api/v1/imports.
// Usage: caveman audit import --format <fmt> <file> [--field-map <json>]
// The org/project scope is resolved server-side from the auth token — never
// from the file (tenant-scoped rule).
async function auditImport(argv: string[]) {
  const format = flagFrom(argv, "--format", "caveman-jsonl");
  const file = positionalAfterOptions(argv.slice(1), new Set(["--format", "--field-map"]));
  if (!file) throw new Error(`usage: ${invokedCommand("audit")} import --format <fmt> <file>`);
  const data = await readFile(file);
  const cfg = await config();
  const headers: Record<string, string> = {
    authorization: `Bearer ${cfg.token}`,
    "content-type": "application/octet-stream",
    "x-cave-csrf": "cli"
  };
  const fieldMap = flagFrom(argv, "--field-map", "");
  if (fieldMap) headers["x-cave-field-map"] = fieldMap;
  const response = await fetch(`${cfg.baseURL}/api/v1/imports?format=${encodeURIComponent(format)}`, {
    method: "POST",
    headers,
    body: data
  });
  print(await response.json());
}

// auditEvalImport turns newline-delimited, source-neutral eval records into one
// bounded batch. Server stamps tenant scope, observed basis, and external-only
// authority; CI cannot promote its own results into rollout authority.
async function auditEvalImport(argv: string[]) {
  const file = positionalAfterOptions(argv.slice(1), new Set(["--project"]));
  if (!file) throw new Error(`usage: ${invokedCommand("audit")} eval-import <evidence.jsonl> [--project <uuid>] [--dry-run]`);
  const data = await readFile(file);
  if (data.byteLength > 4 * 1024 * 1024) throw new Error("eval evidence file exceeds 4 MiB batch limit");

  const items: Record<string, unknown>[] = [];
  for (const [index, rawLine] of data.toString("utf8").split(/\r?\n/).entries()) {
    const line = rawLine.trim();
    if (!line) continue;
    let parsed: unknown;
    try {
      parsed = JSON.parse(line);
    } catch {
      throw new Error(`eval evidence line ${index + 1} is not valid JSON`);
    }
    if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) {
      throw new Error(`eval evidence line ${index + 1} must be a JSON object`);
    }
    items.push(parsed as Record<string, unknown>);
    if (items.length > 1000) throw new Error("eval evidence batch exceeds 1000 records");
  }
  if (items.length === 0) throw new Error("eval evidence file contains no records");

  const cfg = await config();
  const project = flagFrom(argv, "--project", cfg.projectId ?? "");
  const response = await fetch(`${cfg.baseURL}/api/v1/eval-evidence/batches`, {
    method: "POST",
    headers: {
      authorization: `Bearer ${cfg.token}`,
      "content-type": "application/json",
      "x-cave-csrf": "cli",
    },
    body: JSON.stringify({
      ...(project ? { project_id: project } : {}),
      dry_run: argv.includes("--dry-run"),
      items,
    }),
  });
  const body = await response.json();
  if (!response.ok) throw new Error(`eval evidence import failed (${response.status}): ${JSON.stringify(body)}`);
  print(body);
}

// ---------------------------------------------------------------------------
// Signed usage receipts: air-gapped export + offline verification.
//
// A receipt is a per-(org,project,day) aggregate, Ed25519-signed and chained by
// prev_receipt_hash. `verify` recomputes each canonical hash, checks the
// signature against the published public key, and walks the chain — all offline,
// so finance (either party) can re-derive trust without contacting Caveman. It is
// the cross-language counterpart of cloud/metering's VerifyChain.
// ---------------------------------------------------------------------------

type ReceiptSignature = { alg: string; key_id: string; sig: string };
type ReceiptScope = { org_hash: string; project_hash: string };
type ReceiptOptimizer = { optimizer_id_hash: string; requests_optimized: number };
type Receipt = {
  schema: string;
  scope: ReceiptScope;
  scope_completeness?: string;
  methods?: string[];
  day: string;
  seq: number;
  prev_receipt_hash: string;
  verified_savings_usd: number;
  verified_savings_units_1e10?: number;
  tokens_before: number;
  tokens_after: number;
  total_cost_usd: number;
  total_cost_microusd?: number;
  total_cost_units_1e10?: number;
  requests_optimized: number;
  optimizers: ReceiptOptimizer[];
  eval_gate: { passed: number; failed: number };
  formula_version: string;
  catalog_version: string;
  receipt_hash: string;
  signature: ReceiptSignature;
};
type ReceiptPublicKey = { key_id: string; alg: string; key: string };
type ReceiptBundle = { schema: string; public_key: ReceiptPublicKey; public_keys?: ReceiptPublicKey[]; verification_coverage?: string; completeness_attested?: boolean; receipts: Receipt[] };
type DecodedReceiptKey = { info: ReceiptPublicKey; raw: Buffer; key: KeyObject };

const RECEIPT_BUNDLE_V1 = "caveman.receipt-bundle.v1";
const RECEIPT_BUNDLE_V2 = "caveman.receipt-bundle.v2";
const RECEIPT_V1 = "caveman.receipt.v1";
const RECEIPT_V2 = "caveman.receipt.v2";
const RECEIPT_V3 = "caveman.receipt.v3";
const RECEIPT_V4 = "caveman.receipt.v4";
const RECEIPT_FORMULA = "verified-savings-ledger.v1";
const CAVEBENCH_RECEIPT_FORMULA = "cavebench.self.v1";
const MAX_CATALOG_VERSION_LENGTH = 512;
const INCLUDED_RECEIPTS_ONLY = "included_receipts_only";
const SHA256_VALUE = /^sha256:[0-9a-f]{64}$/;
const VERIFIED_SAVINGS_METHOD_SET = new Set<string>(VERIFIED_SAVINGS_METHODS);

// The Go metering validator and this offline verifier share the same
// fail-closed method contract: billable v4 receipts name one or more known
// methods in strict lexicographic order, with no duplicates.
function validBillableReceiptMethods(methods: unknown): methods is string[] {
  if (!Array.isArray(methods) || methods.length === 0) return false;
  let previous = "";
  for (const method of methods) {
    if (typeof method !== "string" || !VERIFIED_SAVINGS_METHOD_SET.has(method) || (previous !== "" && method <= previous)) return false;
    previous = method;
  }
  return true;
}

// canonicalize reproduces cloud/metering's canonical form byte-for-byte: compact
// JSON with object keys sorted lexicographically and ES6 (shortest) numbers,
// which JSON.stringify and Go's encoding/json both emit identically.
function canonicalize(value: unknown): string {
  if (value === null || typeof value !== "object") return JSON.stringify(value);
  if (Array.isArray(value)) return "[" + value.map(canonicalize).join(",") + "]";
  const obj = value as Record<string, unknown>;
  return "{" + Object.keys(obj).sort().map((k) => JSON.stringify(k) + ":" + canonicalize(obj[k])).join(",") + "}";
}

// ed25519PublicKey wraps a raw 32-byte key in DER SPKI so node:crypto can verify
// with it (matching Go's raw ed25519 public key).
function ed25519PublicKey(raw: Buffer): KeyObject {
  if (raw.length !== 32) throw new Error(`ed25519 public key must be 32 bytes, got ${raw.length}`);
  const der = Buffer.concat([Buffer.from("302a300506032b6570032100", "hex"), raw]);
  return createPublicKey({ key: der, format: "der", type: "spki" });
}

// verifyReceipt recomputes the hash over the canonical core (every field except
// receipt_hash and signature) and verifies the signature over receipt_hash.
function validateReceiptContent(r: Receipt): string | null {
  if (r.schema !== RECEIPT_V1 && r.schema !== RECEIPT_V2 && r.schema !== RECEIPT_V3 && r.schema !== RECEIPT_V4) return `seq ${r.seq}: unsupported receipt schema ${String(r.schema)}`;
  if (!r.scope || !SHA256_VALUE.test(r.scope.org_hash) || !SHA256_VALUE.test(r.scope.project_hash)) return `seq ${r.seq}: invalid receipt scope hash`;
  if (r.schema === RECEIPT_V4) {
    if (r.scope_completeness !== INCLUDED_RECEIPTS_ONLY) return `seq ${r.seq}: scope_completeness must be ${INCLUDED_RECEIPTS_ONLY}`;
    if (!Array.isArray(r.methods)) return `seq ${r.seq}: methods must be an array`;
    if (r.formula_version === RECEIPT_FORMULA && !validBillableReceiptMethods(r.methods)) return `seq ${r.seq}: unsupported billable methods`;
    if (r.formula_version === CAVEBENCH_RECEIPT_FORMULA && r.methods.length !== 0) return `seq ${r.seq}: CaveBench receipts cannot claim verified methods`;
  } else if (r.scope_completeness !== undefined || r.methods !== undefined) {
    return `seq ${r.seq}: signed scope and methods require receipt v4`;
  }
  if (!validReceiptDay(r.day)) return `seq ${r.seq}: invalid receipt day ${String(r.day)}`;
  if (!Number.isSafeInteger(r.seq) || r.seq < 1) return `invalid receipt seq ${String(r.seq)}`;
  if ((r.seq === 1 && r.prev_receipt_hash !== "") || (r.seq > 1 && !SHA256_VALUE.test(r.prev_receipt_hash))) return `seq ${r.seq}: invalid prev_receipt_hash`;
  if (!Number.isFinite(r.verified_savings_usd) || !Number.isFinite(r.total_cost_usd) || r.total_cost_usd < 0) return `seq ${r.seq}: invalid money fields`;
  if (!validCounter(r.tokens_before) || !validCounter(r.tokens_after) || r.tokens_after > r.tokens_before) return `seq ${r.seq}: invalid token counters`;
  if (!validCounter(r.requests_optimized) || r.requests_optimized === 0) return `seq ${r.seq}: invalid optimized request count`;
  if (r.schema === RECEIPT_V2 || r.schema === RECEIPT_V3 || r.schema === RECEIPT_V4) {
    if (!validCounter(r.total_cost_microusd)) return `seq ${r.seq}: invalid total_cost_microusd`;
    if (roundCents((r.total_cost_microusd as number) / 1_000_000) !== r.total_cost_usd) return `seq ${r.seq}: total cost fields do not reconcile`;
  }
  if (r.schema === RECEIPT_V3 || r.schema === RECEIPT_V4) {
    if (!validSignedCounter(r.verified_savings_units_1e10)) return `seq ${r.seq}: invalid verified_savings_units_1e10`;
    if ((r.verified_savings_units_1e10 as number) / 10_000_000_000 !== r.verified_savings_usd) return `seq ${r.seq}: exact verified savings fields do not reconcile`;
    if (!validCounter(r.total_cost_units_1e10)) return `seq ${r.seq}: invalid total_cost_units_1e10`;
    if (Math.round((r.total_cost_units_1e10 as number) / 10_000) !== r.total_cost_microusd ||
        roundCents((r.total_cost_units_1e10 as number) / 10_000_000_000) !== r.total_cost_usd) return `seq ${r.seq}: exact total cost fields do not reconcile`;
  } else if (r.total_cost_units_1e10 !== undefined || r.verified_savings_units_1e10 !== undefined) {
    return `seq ${r.seq}: exact money units require receipt v3 or newer`;
  }
  if (!r.eval_gate || !validCounter(r.eval_gate.passed) || !validCounter(r.eval_gate.failed)) return `seq ${r.seq}: invalid eval counters`;
  if (r.formula_version === RECEIPT_FORMULA) {
    if (!validCatalogVersion(r.catalog_version, true)) return `seq ${r.seq}: invalid catalog_version`;
    if (r.eval_gate.passed !== r.requests_optimized || r.eval_gate.failed !== 0) return `seq ${r.seq}: verified receipt requires every optimized request to pass its eval gate`;
  } else if (r.formula_version === CAVEBENCH_RECEIPT_FORMULA) {
    if (!validCatalogVersion(r.catalog_version, false)) return `seq ${r.seq}: invalid catalog_version`;
    if (r.verified_savings_usd !== 0 || r.verified_savings_units_1e10 !== 0) return `seq ${r.seq}: CaveBench receipts cannot carry verified savings`;
    if (r.eval_gate.passed > r.requests_optimized || r.eval_gate.failed !== r.requests_optimized - r.eval_gate.passed) return `seq ${r.seq}: CaveBench eval counters do not reconcile`;
  } else {
    return `seq ${r.seq}: unsupported formula version ${String(r.formula_version)}`;
  }
  if (!Array.isArray(r.optimizers) || r.optimizers.length === 0) return `seq ${r.seq}: optimizer-attributed receipt requires optimizers`;
  const optimizers = new Set<string>();
  for (const optimizer of r.optimizers) {
    if (!optimizer || !SHA256_VALUE.test(optimizer.optimizer_id_hash) || !validCounter(optimizer.requests_optimized) || optimizer.requests_optimized === 0 || optimizer.requests_optimized > r.requests_optimized) return `seq ${r.seq}: invalid optimizer counter`;
    if (optimizers.has(optimizer.optimizer_id_hash)) return `seq ${r.seq}: duplicate optimizer hash`;
    optimizers.add(optimizer.optimizer_id_hash);
  }
  if (!SHA256_VALUE.test(r.receipt_hash)) return `seq ${r.seq}: invalid receipt_hash`;
  if (!r.signature || r.signature.alg !== "Ed25519" || typeof r.signature.key_id !== "string" || !r.signature.key_id.trim()) return `seq ${r.seq}: invalid receipt signature metadata`;
  return null;
}

function validCatalogVersion(value: unknown, billable: boolean): value is string {
  if (typeof value !== "string" || value.length === 0 || value.length > MAX_CATALOG_VERSION_LENGTH || value.trim() !== value || value.startsWith("unpriced:")) return false;
  if (!billable) return true;
  const parts = value.startsWith("mixed:") ? value.slice("mixed:".length).split(",") : [value];
  if (value.startsWith("mixed:") && parts.length < 2) return false;
  let previous = "";
  for (const part of parts) {
    if (!validReceiptDay(part) || (previous !== "" && part <= previous)) return false;
    previous = part;
  }
  return true;
}

function validReceiptDay(day: unknown): day is string {
  if (typeof day !== "string" || !/^\d{4}-\d{2}-\d{2}$/.test(day)) return false;
  const parsed = new Date(`${day}T00:00:00.000Z`);
  return Number.isFinite(parsed.getTime()) && parsed.toISOString().slice(0, 10) === day;
}

function validCounter(value: unknown): value is number {
  return typeof value === "number" && Number.isSafeInteger(value) && value >= 0;
}

function validSignedCounter(value: unknown): value is number {
  return typeof value === "number" && Number.isSafeInteger(value);
}

function roundCents(value: number): number {
  const rounded = Math.round(value * 100) / 100;
  return Object.is(rounded, -0) ? 0 : rounded;
}

function verifyReceipt(r: Receipt, pub: KeyObject, expectedKeyID: string): string | null {
  const contentErr = validateReceiptContent(r);
  if (contentErr) return contentErr;
  if (r.signature?.alg !== "Ed25519") return `seq ${r.seq}: unknown signature alg ${r.signature?.alg}`;
  if (r.signature.key_id !== expectedKeyID) return `seq ${r.seq}: signature key_id does not match selected key`;
  const { receipt_hash, signature, ...core } = r;
  const want = "sha256:" + createHash("sha256").update(canonicalize(core)).digest("hex");
  if (want !== receipt_hash) return `seq ${r.seq}: receipt_hash mismatch (tampered)`;
  const sig = Buffer.from(signature.sig, "base64");
  if (sig.length !== 64) return `seq ${r.seq}: invalid Ed25519 signature length`;
  const ok = edVerify(null, Buffer.from(receipt_hash), pub, sig);
  if (!ok) return `seq ${r.seq}: signature does not verify`;
  return null;
}

// verifyChain checks every signature plus the seq/prev_receipt_hash links across
// a window of receipts. Returns the first failure, or null if the chain is sound.
function verifyChain(receipts: Receipt[], keys: Map<string, DecodedReceiptKey>): string | null {
  const sorted = [...receipts].sort((a, b) => a.seq - b.seq);
  let prev: Receipt | undefined;
  for (const r of sorted) {
    const decoded = keys.get(r.signature?.key_id);
    if (!decoded) return `seq ${r.seq}: no trusted public key for key_id ${String(r.signature?.key_id)}`;
    const err = verifyReceipt(r, decoded.key, decoded.info.key_id);
    if (err) return err;
    if (prev) {
      if (r.seq !== prev.seq + 1) return `seq ${r.seq}: not strictly after ${prev.seq}`;
      if (r.prev_receipt_hash !== prev.receipt_hash) return `seq ${r.seq}: prev_receipt_hash does not link to seq ${prev.seq}`;
      if (r.day <= prev.day) return `seq ${r.seq}: day ${r.day} does not follow ${prev.day}`;
    }
    prev = r;
  }
  return null;
}

function decodeReceiptKey(info: ReceiptPublicKey, label: string): DecodedReceiptKey {
  if (!info || typeof info.key_id !== "string" || !info.key_id.trim()) throw new Error(`${label} key_id is required`);
  if (info.alg !== "Ed25519") throw new Error(`${label} has unsupported algorithm ${String(info.alg)}`);
  if (typeof info.key !== "string" || !info.key.trim()) throw new Error(`${label} key is required`);
  const raw = Buffer.from(info.key, "base64");
  if (raw.length !== 32 || raw.toString("base64") !== info.key) throw new Error(`${label} must be a canonical base64 Ed25519 public key`);
  return { info, raw, key: ed25519PublicKey(raw) };
}

function decodeUniqueKeyring(infos: ReceiptPublicKey[], label: string): Map<string, DecodedReceiptKey> {
  const keys = new Map<string, DecodedReceiptKey>();
  for (const [index, info] of infos.entries()) {
    const decoded = decodeReceiptKey(info, `${label}[${index}]`);
    if (keys.has(decoded.info.key_id)) throw new Error(`${label} contains duplicate key_id ${decoded.info.key_id}`);
    keys.set(decoded.info.key_id, decoded);
  }
  return keys;
}

function embeddedReceiptKeys(bundle: ReceiptBundle): { current: DecodedReceiptKey; keys: Map<string, DecodedReceiptKey> } {
  if (bundle.schema !== RECEIPT_BUNDLE_V1 && bundle.schema !== RECEIPT_BUNDLE_V2) throw new Error(`unsupported bundle schema ${String(bundle.schema)}`);
  if (bundle.verification_coverage !== undefined && bundle.verification_coverage !== INCLUDED_RECEIPTS_ONLY) throw new Error(`unsupported unsigned verification coverage ${String(bundle.verification_coverage)}`);
  if (bundle.completeness_attested === true) throw new Error("bundle completeness cannot be attested by unsigned export metadata");
  const current = decodeReceiptKey(bundle.public_key, "public_key");
  if (bundle.public_keys !== undefined && !Array.isArray(bundle.public_keys)) throw new Error("public_keys must be an array");
  if (bundle.schema === RECEIPT_BUNDLE_V2 && (!Array.isArray(bundle.public_keys) || bundle.public_keys.length === 0)) throw new Error("v2 bundle requires public_keys");
  const keys = decodeUniqueKeyring(bundle.public_keys ?? [], "public_keys");
  const currentInRing = keys.get(current.info.key_id);
  if (currentInRing && !currentInRing.raw.equals(current.raw)) throw new Error(`public_key conflicts with public_keys entry ${current.info.key_id}`);
  if (bundle.schema === RECEIPT_BUNDLE_V2 && !currentInRing) throw new Error("v2 public_keys must include public_key");
  if (!currentInRing) keys.set(current.info.key_id, current);
  return { current, keys };
}

async function pinnedReceiptKeys(file: string, current: DecodedReceiptKey): Promise<{ keys: Map<string, DecodedReceiptKey>; trust: string }> {
  const source = (await readFile(file, "utf8")).trim();
  if (!source.startsWith("{")) {
    const pinned = decodeReceiptKey({ ...current.info, key: source }, "--pubkey");
    if (!pinned.raw.equals(current.raw)) throw new Error("bundle public key does not match the published --pubkey");
    return { keys: new Map([[current.info.key_id, pinned]]), trust: "pinned_public_key" };
  }
  let parsed: { public_key?: ReceiptPublicKey; public_keys?: ReceiptPublicKey[] };
  try { parsed = JSON.parse(source); } catch { throw new Error("--pubkey JSON is malformed"); }
  const infos = Array.isArray(parsed.public_keys) ? parsed.public_keys : parsed.public_key ? [parsed.public_key] : [];
  if (infos.length === 0) throw new Error("--pubkey JSON must contain public_key or public_keys");
  const keys = decodeUniqueKeyring(infos, "--pubkey public_keys");
  const pinnedCurrent = keys.get(current.info.key_id);
  if (!pinnedCurrent || !pinnedCurrent.raw.equals(current.raw)) throw new Error("trusted --pubkey keyring does not contain the bundle public key");
  return { keys, trust: "pinned_keyring" };
}

// receiptsVerify validates a signed receipt bundle offline (no network). A raw
// --pubkey pins the current key; JSON may independently pin a full rotation
// keyring. Without either, embedded keys prove self-consistency, not publisher
// authenticity. Exits non-zero on any included content, signature, or
// scope-chain break. Tail/scope omission needs separately trusted head manifest;
// bundle output states completeness is not attested.
//   caveman receipts verify <bundle.json> [--pubkey <file>]
async function receiptsVerify(argv: string[]) {
  const file = positionalAfterOptions(argv.slice(1), new Set(["--pubkey"]));
  if (!file) throw new Error(`usage: ${invokedCommand("receipts")} verify <bundle.json> [--pubkey <file>]`);
  let bundle: ReceiptBundle;
  try { bundle = JSON.parse(await readFile(file, "utf8")) as ReceiptBundle; } catch (e) { return fail(`invalid bundle JSON: ${(e as Error).message}`); }
  if (!Array.isArray(bundle.receipts)) return fail("bundle receipts must be an array");

  try {
    const embedded = embeddedReceiptKeys(bundle);
    const pubkeyFile = flagFrom(argv, "--pubkey", "");
    const pinned = pubkeyFile ? await pinnedReceiptKeys(pubkeyFile, embedded.current) : null;
    const keys = pinned?.keys ?? embedded.keys;
    for (const receipt of bundle.receipts) {
      const embeddedKey = embedded.keys.get(receipt.signature?.key_id);
      if (!embeddedKey) return fail(`seq ${receipt.seq}: no embedded public key for key_id ${String(receipt.signature?.key_id)}`);
      if (pinned) {
        const trusted = keys.get(receipt.signature?.key_id);
        if (!trusted) return fail(`seq ${receipt.seq}: key_id ${String(receipt.signature?.key_id)} is not present in trusted --pubkey material`);
        if (!trusted.raw.equals(embeddedKey.raw)) return fail(`seq ${receipt.seq}: embedded key ${receipt.signature.key_id} does not match trusted --pubkey material`);
      }
    }

    const byScope = new Map<string, Receipt[]>();
    for (const receipt of bundle.receipts) {
      const scope = receipt.scope;
      if (!scope || typeof scope.org_hash !== "string" || typeof scope.project_hash !== "string") return fail(`seq ${receipt.seq}: receipt scope is required`);
      const id = `${scope.org_hash}\0${scope.project_hash}`;
      const chain = byScope.get(id) ?? [];
      chain.push(receipt);
      byScope.set(id, chain);
    }
    for (const chain of byScope.values()) {
      const err = verifyChain(chain, keys);
      if (err) return fail(err);
    }
    if (bundle.receipts.length === 0) {
      return print({ verified: false, valid_bundle: true, empty: true, receipts: 0, scopes: 0, trust_anchor: pinned?.trust ?? "embedded_keys_self_consistency_only" });
    }
    print({
      verified: true,
      verification: "receipt_content_hash_signature_and_scope_chain",
      verification_coverage: INCLUDED_RECEIPTS_ONLY,
      completeness_attested: false,
      scope_completeness: [...new Set(bundle.receipts.map((receipt) => receipt.scope_completeness ?? "legacy_unspecified"))].sort(),
      methods: [...new Set(bundle.receipts.flatMap((receipt) => receipt.methods ?? []))].sort(),
      trust_anchor: pinned?.trust ?? "embedded_keys_self_consistency_only",
      receipts: bundle.receipts.length,
      scopes: byScope.size,
      key_ids: [...new Set(bundle.receipts.map((receipt) => receipt.signature.key_id))].sort(),
    });
  } catch (e) {
    return fail((e as Error).message);
  }
}

// fail prints a one-line reason to stderr and exits non-zero — the contract a CI
// gate or finance script keys off.
function fail(reason: string): never {
  console.error(`receipt verification failed: ${reason}`);
  process.exit(1);
}

// receiptsExport writes the org's signed receipts to a self-verifying bundle
// file. It reads from the LOCAL control plane (in the customer's own env) and
// never contacts Caveman — the air-gapped meter export. The downloaded bundle is
// then checkable offline with `caveman receipts verify`.
//   caveman receipts export [--since YYYY-MM-DD] [--until YYYY-MM-DD] -o bundle.json
async function receiptsExport(argv: string[]) {
  const since = flagFrom(argv, "--since", "");
  const until = flagFrom(argv, "--until", "");
  const out = flagFrom(argv, "-o", flagFrom(argv, "--out", "receipts-bundle.json"));
  const query = new URLSearchParams();
  if (since) query.set("since", since);
  if (until) query.set("until", until);
  const qs = query.toString();
  const bundle = await get(`/api/v1/metering/receipts${qs ? "?" + qs : ""}`);
  await writeFile(out, JSON.stringify(bundle, null, 2) + "\n", { mode: 0o600 });
  print({ exported: out, receipts: Array.isArray(bundle.receipts) ? bundle.receipts.length : 0 });
}

// plan prints the Cave Architect's ranked Cave Plan in one operator voice
// (--json for the raw object). Savings are a per-day rate, basis "inferred".
async function plan(argv: string[]) {
  if (argv.length > 1 || (argv.length === 1 && argv[0] !== "--json")) {
    console.error(`usage: ${invokedCommand("plan")} [--json]`);
    process.exit(2);
  }
  const data = await get(`/api/v1/projects/${await projectId()}/cave-plan`);
  if (argv.includes("--json")) return print(data);
  const h = data.headline ?? {};
  console.log("");
  console.log("  CAVE PLAN — projected savings");
  console.log(`  ${usd(h.base)}/day  (${usd(h.low)} - ${usd(h.high)}, ${h.basis ?? "inferred"})  -  ${h.move_count ?? 0} moves`);
  console.log("");
  const classBreakdown = data.savings_by_class ?? data["head" + "room_by_class"] ?? [];
  for (const c of classBreakdown) {
    console.log(`  ${c.safety_class}: ${usd(c.base)}/day  (${c.move_count} ${c.move_count === 1 ? "move" : "moves"})`);
  }
  if (classBreakdown.length) console.log("");
  for (const m of data.moves ?? []) {
    const save = (m.savings_usd_base ?? 0) > 0 ? `${usd(m.savings_usd_base)}/day` : "enablement";
    const gate = m.requires_eval_gate ? " - eval-gated" : "";
    console.log(`  - ${m.title}  [${save}]  ${m.safety_class}${gate}`);
    console.log(`    ${m.summary}`);
    console.log("");
  }
  if ((data.no_signal ?? []).length) console.log(`  cave still watching for: ${data.no_signal.join(", ")}`);
}

function parseBooleanFlag(argv: string[], name: string): boolean | undefined {
  const raw = flagFrom(argv, name, "");
  if (raw === "") return undefined;
  if (raw === "true") return true;
  if (raw === "false") return false;
  console.error(`${name} must be true or false`);
  process.exit(2);
}

function parseNumberFlag(argv: string[], name: string): number | undefined {
  const raw = flagFrom(argv, name, "");
  if (raw === "") return undefined;
  const value = Number(raw);
  if (!Number.isFinite(value)) {
    console.error(`${name} must be a finite number`);
    process.exit(2);
  }
  return value;
}

const TRACE_SEARCH_VALUE_FLAGS = new Set([
  "--workflow",
  "--agent",
  "--model",
  "--provider",
  "--error-code",
  "--session-id",
  "--trace-id",
  "--status-class",
  "--has-error",
  "--compressed",
  "--min-cost-usd",
  "--max-cost-usd",
  "--min-tokens",
  "--max-tokens",
  "--min-latency-ms",
  "--max-latency-ms",
  "--from",
  "--to",
  "--date-field",
  "--group-by",
  "--sort",
  "--dir",
  "--limit",
]);

function validateTraceSearchArgs(argv: string[]): void {
  for (let index = 0; index < argv.length; index++) {
    const value = argv[index]!;
    if (value === "--json") continue;
    const equal = value.indexOf("=");
    const name = equal >= 0 ? value.slice(0, equal) : value;
    if (!TRACE_SEARCH_VALUE_FLAGS.has(name)) {
      console.error(`unknown trace search argument: ${value}`);
      process.exit(2);
    }
    if (equal < 0) {
      const next = argv[index + 1];
      if (!next || next.startsWith("--")) {
        console.error(`${name} requires a value`);
        process.exit(2);
      }
      index++;
    }
  }
}

function traceSearchBody(argv: string[]): Record<string, unknown> {
  validateTraceSearchArgs(argv);
  const filters: Record<string, unknown> = {};
  for (const [flag, key] of [
    ["--workflow", "workflow"],
    ["--agent", "agent"],
    ["--model", "model"],
    ["--provider", "provider"],
    ["--error-code", "error_code"],
    ["--session-id", "session_id"],
    ["--trace-id", "trace_id"],
    ["--status-class", "status_class"],
  ] as const) {
    const value = flagFrom(argv, flag, "");
    if (value) filters[key] = [value];
  }
  for (const [flag, key] of [
    ["--has-error", "has_error"],
    ["--compressed", "compressed"],
  ] as const) {
    const value = parseBooleanFlag(argv, flag);
    if (value !== undefined) filters[key] = value;
  }
  for (const [flag, key] of [
    ["--min-cost-usd", "min_cost_usd"],
    ["--max-cost-usd", "max_cost_usd"],
    ["--min-tokens", "min_total_tokens"],
    ["--max-tokens", "max_total_tokens"],
    ["--min-latency-ms", "min_latency_ms"],
    ["--max-latency-ms", "max_latency_ms"],
  ] as const) {
    const value = parseNumberFlag(argv, flag);
    if (value !== undefined) filters[key] = value;
  }
  const body: Record<string, unknown> = {};
  if (Object.keys(filters).length > 0) body.filters = filters;
  const from = flagFrom(argv, "--from", "");
  const to = flagFrom(argv, "--to", "");
  const dateField = flagFrom(argv, "--date-field", "");
  const groupBy = flagFrom(argv, "--group-by", "");
  if (from) body.from = from;
  if (to) body.to = to;
  if (dateField) body.date_field = dateField;
  if (groupBy) body.group_by = groupBy;
  const sortBy = flagFrom(argv, "--sort", "");
  const sortDir = flagFrom(argv, "--dir", "");
  if (sortBy || sortDir) body.sort = { by: sortBy || "timestamp", dir: sortDir || "desc" };
  const pageSize = parseNumberFlag(argv, "--limit");
  if (pageSize !== undefined) {
    if (!Number.isSafeInteger(pageSize) || pageSize < 1 || pageSize > 500) {
      console.error("--limit must be an integer from 1 to 500");
      process.exit(2);
    }
    body.page_size = pageSize;
  }
  return body;
}

async function traceCommand(argv: string[]) {
  const action = argv[0] ?? "";
  if (action === "list") {
    const project = await projectId();
    return get(`/api/v1/traces?${new URLSearchParams({ project_id: project })}`).then(print);
  }
  if (action === "search") {
    const project = await projectId();
    return post(`/api/v1/traces/search?${new URLSearchParams({ project_id: project })}`, traceSearchBody(argv.slice(1))).then(print);
  }
  if (action === "show") {
    const traceId = argv[1];
    if (!traceId) return commandUsage("traces show <id>");
    const encoded = encodeURIComponent(traceId);
    const query = `?${new URLSearchParams({ project_id: await projectId() })}`;
    if (!argv.includes("--spans")) return get(`/api/v1/traces/${encoded}${query}`).then(print);
    const [trace, spans] = await Promise.all([
      get(`/api/v1/traces/${encoded}${query}`),
      get(`/api/v1/traces/${encoded}/spans${query}`),
    ]);
    return print({ trace, spans });
  }
  if (action === "export") return post("/api/v1/traces/export", {}).then(print);
  return commandUsage("traces list|search [filters]|show <id> [--spans]|export");
}

async function experimentCommand(argv: string[]) {
  const action = argv[0] ?? "";
  if (action === "list") {
    const project = await projectId();
    return get(`/api/v1/experiments?${new URLSearchParams({ project_id: project })}`).then(print);
  }
  const experimentId = argv[1];
  if ((action === "show" || action === "results") && experimentId) {
    const suffix = action === "results" ? "/results" : "";
    const query = new URLSearchParams({ project_id: await projectId() });
    return get(`/api/v1/experiments/${encodeURIComponent(experimentId)}${suffix}?${query}`).then(print);
  }
  return commandUsage("experiments list|show <id>|results <id>");
}

function usd(value: unknown) {
  const amount = finiteNumber(value);
  if (amount === null) return "—";
  return amount !== 0 && Math.abs(amount) < 1
    ? formatCurrencyAmount(amount, "USD", 2)
    : formatCurrencyAmount(Math.round(amount), "USD", 0);
}

function finiteNumber(value: unknown): number | null {
  return typeof value === "number" && Number.isFinite(value) ? value : null;
}

function validNonNegativeInteger(value: unknown): number | null {
  return typeof value === "number" && Number.isSafeInteger(value) && value >= 0 ? value : null;
}

function formatCurrencyAmount(amount: number, currency: unknown, digits = 2): string {
  const code = typeof currency === "string" ? currency.trim().toUpperCase() : "";
  if (!Number.isFinite(amount) || !/^[A-Z]{3}$/.test(code)) return "—";
  try {
    return new Intl.NumberFormat("en-US", { style: "currency", currency: code, minimumFractionDigits: digits, maximumFractionDigits: digits }).format(amount);
  } catch {
    return "—";
  }
}

function formatMinorCurrency(cents: unknown, currency: unknown): string {
  const amount = finiteNumber(cents);
  return amount === null ? "—" : formatCurrencyAmount(amount / 100, currency);
}

// billingStatus prints the org's gainshare contract + reconciled month-to-date
// fee. Inferred/projected Cave Plan values never enter this view.
async function billingStatus(argv: string[]) {
  const data = await get("/api/v1/billing/account");
  if (argv.includes("--json")) return print(data);
  const bps = finiteNumber(data.gainshare_bps);
  const currency = data.currency;
  console.log("");
  console.log("  BILLING — gainshare on verified savings");
  console.log(`  rate:    ${bps !== null && bps >= 0 && bps <= 10_000 ? `${(bps / 100).toFixed(bps % 100 === 0 ? 0 : 2)}%` : "—"} of verified savings`);
  console.log(`  status:  ${data.connected ? data.status : "not connected"}`);
  console.log(`  MTD fee: ${formatMinorCurrency(data.mtd_fee_cents, currency)}`);
  if (data.next_invoice_estimate_cents != null) console.log(`  next invoice (est.): ${formatMinorCurrency(data.next_invoice_estimate_cents, currency)}`);
  if (!data.billing_enabled) console.log("  (billing is not enabled on this deployment)");
  console.log("");
}

// billingCharges prints the signed daily meter-delta ledger, each row pinned to
// the receipts it summed — the "this invoice = these receipts" audit view.
//   caveman billing charges [--since YYYY-MM-DD] [--until YYYY-MM-DD] [--json]
async function billingCharges(argv: string[]) {
  const since = flagFrom(argv, "--since", "");
  const until = flagFrom(argv, "--until", "");
  const q = new URLSearchParams();
  if (since) q.set("since", since);
  if (until) q.set("until", until);
  const qs = q.toString();
  const data = await get(`/api/v1/billing/charges${qs ? "?" + qs : ""}`);
  if (argv.includes("--json")) return print(data);
  const account = await get("/api/v1/billing/account");
  const feeCurrency = typeof account.currency === "string" && /^[a-zA-Z]{3}$/.test(account.currency) ? account.currency.toUpperCase() : "";
  const charges = Array.isArray(data.charges) ? data.charges : [];
  console.log("");
  console.log(`  DAY       FEE DELTA${feeCurrency ? ` (${feeCurrency})` : ""}  SAVINGS (USD)  STATUS     RECEIPTS`);
  for (const c of charges) {
    const fee = formatMinorCurrency(c.fee_cents, feeCurrency).padStart(12);
    const savings = finiteNumber(c.gross_savings_usd);
    const sav = (savings === null ? "—" : formatCurrencyAmount(savings, "USD")).padStart(13);
    const status = String(c.status ?? "").padEnd(9);
    const n = Array.isArray(c.receipt_hashes) ? c.receipt_hashes.length : 0;
    console.log(`  ${c.day}  ${fee}  ${sav}   ${status}  ${n} linked`);
  }
  if (!charges.length) console.log("  (no charges yet)");
  console.log("");
}

function sdkSnippet() {
  console.log(`OpenAI baseURL: $CAVE_GATEWAY_URL/openai/v1
Anthropic baseURL: $CAVE_GATEWAY_URL/anthropic
Gemini endpoint: $CAVE_GATEWAY_URL/gemini/v1beta/models/gemini-model:generateContent
TypeScript SDK: import { Cave } from "@caveman-ai/sdk";
Python SDK: from caveman_cloud import Cave

Framework snippets now live in the recipe registry:
  caveman snippets
  caveman snippets openai-ts --app my-service`);
}

function snippets(rest: string[]) {
  const id = firstPositionalValue(rest);
  if (!id) {
    for (const recipe of RECIPES) console.log(`${recipe.id}\t${recipe.display_name}`);
    console.log("");
    console.log(`usage: ${invokedCommand("snippets", " snippets")} <id> [--app <slug>]`);
    return;
  }
  const recipe = RECIPES.find((r) => r.id === id);
  if (!recipe) {
    console.error(`unknown snippet recipe: ${id}`);
    console.error(`valid recipes: ${RECIPES.map((r) => r.id).join(", ")}`);
    process.exit(1);
  }
  // "my-service" matches the web wizard's placeholder slug (RECIPE_APP_SLUG) so
  // copy-paste examples read the same on every surface.
  process.stdout.write(renderRecipe(recipe, gatewayURL(), flagFrom(rest, "--app", "my-service")));
}

function firstPositionalValue(values: string[]): string {
  for (let i = 0; i < values.length; i++) {
    const value = values[i] ?? "";
    if (!value.startsWith("--")) return value;
    if (!value.includes("=")) i++;
  }
  return "";
}

function renderRecipe(recipe: IntegrationRecipe, baseURL: string, app: string): string {
  const renderedNote = recipe.note ? renderRecipeTemplate(recipe.note, baseURL, app) : "";
  const renderedCode = renderRecipeTemplate(recipe.code, baseURL, app);
  const parts: string[] = [];
  if (renderedNote) {
    for (const line of renderedNote.split("\n")) parts.push(`${recipeCommentPrefix(recipe.lang)} ${line}`.trimEnd());
  }
  parts.push(renderedCode);
  return parts.join("\n") + "\n";
}

function renderRecipeTemplate(value: string, baseURL: string, app: string): string {
  return value
    .replaceAll("{{baseURL}}", baseURL.replace(/\/+$/, ""))
    .replaceAll("{{app}}", app);
}

function recipeCommentPrefix(lang: IntegrationRecipe["lang"]): string {
  return lang === "ts" ? "//" : "#";
}

async function get(path: string) {
  const cfg = await config();
  requireAuth(cfg);
  const response = await fetch(`${cfg.baseURL}${path}`, { headers: { authorization: `Bearer ${cfg.token}` } });
  const body = await response.json().catch(() => ({}));
  if (!response.ok) {
    // Surface the server's error instead of letting callers render a misleading
    // empty/zero view from an error body (e.g. a $0 billing panel on a 403/500).
    const code = typeof (body as any)?.error === "string" ? (body as any).error : (body as any)?.error?.code;
    const message = (body as any)?.error?.message ?? (body as any)?.message;
    console.error([code, message].filter(Boolean).join(": ") || `request failed (${response.status})`);
    process.exit(1);
  }
  return body;
}

async function post(path: string, body: unknown) {
  const cfg = await config();
  requireAuth(cfg);
  const response = await fetch(`${cfg.baseURL}${path}`, { method: "POST", headers: { authorization: `Bearer ${cfg.token}`, "content-type": "application/json", "x-cave-csrf": "cli" }, body: JSON.stringify(body) });
  const result = await response.json().catch(() => ({}));
  if (!response.ok) {
    const code = typeof (result as any)?.error === "string" ? (result as any).error : (result as any)?.error?.code;
    const message = (result as any)?.error?.message ?? (result as any)?.message;
    console.error([code, message].filter(Boolean).join(": ") || `request failed (${response.status})`);
    process.exit(1);
  }
  return result;
}

class AgentMcpHTTPError extends Error {
  code: string;
  status: number | undefined;

  constructor(message: string, code: string, status?: number) {
    super(message);
    this.name = "AgentMcpHTTPError";
    this.code = code;
    this.status = status;
  }
}

function agentMcpTimeoutMS(): number {
  const raw = process.env.CAVE_AGENT_TOOL_TIMEOUT_MS ?? "30000";
  const value = Number(raw);
  return Number.isSafeInteger(value) && value >= 100 && value <= 120_000 ? value : 30_000;
}

async function agentMcpRequest(
  path: string,
  options: { method?: "GET" | "POST"; body?: JSONObject } = {},
): Promise<JSONValue> {
  const cfg = await config();
  if (!cfg.token) {
    throw new AgentMcpHTTPError(
      "Not logged in. Run `caveman login` or set CAVE_TOKEN for headless use.",
      "cave_auth_required",
      401,
    );
  }
  let response: Response;
  try {
    const request: RequestInit = {
      method: options.method ?? "GET",
      signal: AbortSignal.timeout(agentMcpTimeoutMS()),
      headers: {
        authorization: `Bearer ${cfg.token}`,
        ...(options.method === "POST"
          ? { "content-type": "application/json", "x-cave-csrf": "cli" }
          : {}),
      },
    };
    if (options.method === "POST") request.body = JSON.stringify(options.body ?? {});
    response = await fetch(`${cfg.baseURL}${path}`, request);
  } catch (error) {
    const timedOut = error instanceof Error && error.name === "TimeoutError";
    throw new AgentMcpHTTPError(
      timedOut
        ? `Caveman API request timed out after ${agentMcpTimeoutMS()}ms.`
        : error instanceof Error
          ? error.message
          : "Caveman API request failed.",
      timedOut ? "cave_agent_tool_timeout" : "cave_network_error",
    );
  }
  const body = await response.json().catch(() => ({})) as JSONObject;
  if (!response.ok) {
    const envelope = body.error && typeof body.error === "object" && !Array.isArray(body.error)
      ? body.error as Record<string, unknown>
      : {};
    const flatCode = typeof body.error === "string" ? body.error : undefined;
    throw new AgentMcpHTTPError(
      typeof envelope.message === "string"
        ? envelope.message
        : typeof body.message === "string"
          ? body.message
          : `Caveman API request failed (${response.status}).`,
      typeof envelope.code === "string" ? envelope.code : flatCode ?? "cave_request_failed",
      response.status,
    );
  }
  return body;
}

async function agentMcpProjectId(): Promise<string> {
  const cfg = await config();
  if (cfg.projectId) return cfg.projectId;
  const projects = await agentMcpRequest("/api/v1/projects");
  const projectObject = projects && typeof projects === "object" && !Array.isArray(projects)
    ? projects
    : {};
  const rows = Array.isArray(projectObject.data) ? projectObject.data : [];
  const first = rows[0];
  if (first && typeof first === "object" && !Array.isArray(first) && typeof first.id === "string" && first.id) {
    return first.id;
  }
  throw new AgentMcpHTTPError(
    "No project selected or accessible. Create/select a project before using Caveman agent tools.",
    "cave_project_required",
    404,
  );
}

function recordAgentMcpToolCall(event: { tool: string; result: "ok" | "error"; durationMs: number }): void {
  const state = telemetryState();
  if (!telemetrySendable(state)) return;
  emitTelemetryEvents([{
    schema: "cli/v1",
    anonymous_id: telemetryAnonymousId(state),
    event: "agent_tool_call",
    command: "mcp",
    subcommand: event.tool,
    cli_version: cliVersion(),
    os: process.platform,
    arch: process.arch,
    node_major: Number(process.versions.node.split(".")[0] ?? 0),
    duration_ms: Math.max(0, event.durationMs),
    exit_class: event.result,
    error_class: event.result === "error" ? "other" : "",
    ts: new Date().toISOString(),
  }]);
}

async function serveCloudAgentMcp(): Promise<void> {
  const client: AgentMcpClient = {
    request: agentMcpRequest,
    projectId: agentMcpProjectId,
    recordToolCall: recordAgentMcpToolCall,
  };
  await serveAgentMcp(client);
}

// requireAuth degrades gracefully when logged out: a connected verb prints one
// actionable line and exits non-zero instead of crashing with a stack trace, so
// CI runs that lack credentials skip cleanly rather than fail noisily.
function requireAuth(cfg: Config) {
  if (!cfg.token) {
    console.error("not logged in — run `caveman login` (or set CAVE_TOKEN for non-interactive use)");
    process.exit(2);
  }
}

// resolveConfigBaseUrl mirrors resolveLoginBaseUrl's precedence minus the
// flag tier (config() has no argv at this call site): a previously-saved
// config.json value wins, then CAVE_API_URL, then the deployed prod API.
// C8 review finding 6: a connected verb run with CAVE_TOKEN but no prior
// `login` and no CAVE_API_URL (e.g. CI) used to fall back to a dead
// localhost port here too.
export function resolveConfigBaseUrl(savedBaseURL: string | undefined): string {
  return savedBaseURL ?? process.env.CAVE_API_URL ?? PROD_API_URL;
}

async function config(): Promise<Config> {
  const raw = await readFile(configPath(), "utf8").catch(() => "{}");
  const parsed = JSON.parse(raw) as Partial<Config>;
	const credentials = resolveCredentials(parsed);
  const cfg: Config = {
    baseURL: resolveConfigBaseUrl(parsed.baseURL),
	  token: credentials.access_token,
  };
	if (credentials.refresh_token) cfg.refreshToken = credentials.refresh_token;
	if (credentials.gateway_api_key) cfg.gatewayApiKey = credentials.gateway_api_key;
	if (credentials.gateway_key_id) cfg.gatewayKeyId = credentials.gateway_key_id;
  if (parsed.projectId) cfg.projectId = parsed.projectId;
	else if (credentials.project_id) cfg.projectId = credentials.project_id;
  if (parsed.organizationId) cfg.organizationId = parsed.organizationId;
  if (parsed.tokenStore) cfg.tokenStore = parsed.tokenStore;
  if (parsed.gatewayUrl) cfg.gatewayUrl = parsed.gatewayUrl;
  if (parsed.logoutPendingLocalCleanup === true) cfg.logoutPendingLocalCleanup = true;
  const telemetry = parseTelemetryConfig((parsed as Record<string, unknown>).telemetry);
  if (telemetry) cfg.telemetry = telemetry;
	if (!cfg.logoutPendingLocalCleanup && cfg.refreshToken && accessTokenExpiresSoon(cfg.token)) return refreshCLIConfig(cfg);
	return cfg;
}

async function readRawConfig(): Promise<Record<string, unknown>> {
  const raw = await readFile(configPath(), "utf8").catch(() => "{}");
  const parsed = JSON.parse(raw) as unknown;
  return parsed && typeof parsed === "object" && !Array.isArray(parsed) ? parsed as Record<string, unknown> : {};
}

async function writeRawConfig(out: Record<string, unknown>) {
  await mkdir(dirname(configPath()), { recursive: true });
  try { chmodSync(configPath(), 0o600); } catch { /* created below */ }
  await writeFile(configPath(), JSON.stringify(out, null, 2), { mode: 0o600 });
  chmodSync(configPath(), 0o600);
}

async function saveConfig(cfg: Config) {
  const out = await readRawConfig();
  out.baseURL = cfg.baseURL;
  if (cfg.projectId) out.projectId = cfg.projectId; else delete out.projectId;
  if (cfg.organizationId) out.organizationId = cfg.organizationId; else delete out.organizationId;
  if (cfg.gatewayUrl) out.gatewayUrl = cfg.gatewayUrl; else delete out.gatewayUrl;
  if (cfg.tokenStore) out.tokenStore = cfg.tokenStore; else delete out.tokenStore;
  if (cfg.telemetry) out.telemetry = cfg.telemetry;
  if (cfg.logoutPendingLocalCleanup) out.logoutPendingLocalCleanup = true; else delete out.logoutPendingLocalCleanup;
  // The secret token lives in the keychain / 0600 credentials file. Only the
  // legacy inline path (no tokenStore) ever persists a token to config.json.
  if (cfg.token && !cfg.tokenStore) out.token = cfg.token; else delete out.token;
  await writeRawConfig(out);
}

// resolveCredentials returns all connected-mode credentials while keeping the
// non-interactive CAVE_TOKEN path access-token-only. Legacy stores containing a
// bare token remain readable; new device grants use a versionless JSON envelope.
function resolveCredentials(meta: Partial<Config>): StoredCredentials {
  if (process.env.CAVE_TOKEN) return { access_token: process.env.CAVE_TOKEN };
  let raw = "";
  if (meta.tokenStore === "keychain") raw = keychainGet();
  else if (meta.tokenStore === "file") raw = fileTokenGet();
  else raw = meta.token ?? "";
  return decodeCredentials(raw);
}

function decodeCredentials(raw: string): StoredCredentials {
	const trimmed = raw.trim();
	if (!trimmed) return { access_token: "" };
	try {
	  const parsed = JSON.parse(trimmed) as Partial<StoredCredentials>;
	  if (typeof parsed.access_token === "string") {
	    return {
	      access_token: parsed.access_token,
	      ...(typeof parsed.refresh_token === "string" ? { refresh_token: parsed.refresh_token } : {}),
	      ...(typeof parsed.gateway_api_key === "string" ? { gateway_api_key: parsed.gateway_api_key } : {}),
	      ...(typeof parsed.gateway_key_id === "string" ? { gateway_key_id: parsed.gateway_key_id } : {}),
	      ...(typeof parsed.project_id === "string" ? { project_id: parsed.project_id } : {}),
	    };
	  }
	} catch {
	  // Legacy credential stores are a bare access token.
	}
	return { access_token: trimmed };
}

function encodeCredentials(credentials: StoredCredentials): string {
	if (!credentials.refresh_token && !credentials.gateway_api_key && !credentials.gateway_key_id && !credentials.project_id) {
	  return credentials.access_token;
	}
	return JSON.stringify(credentials);
}

// storeCredentials persists the complete connected session to the OS keychain,
// falling back to a 0600 credentials file. Config contains pointers only.
function storeCredentials(credentials: StoredCredentials): TokenStore {
	const secret = encodeCredentials(credentials);
  if (process.platform === "darwin" && !process.env.CAVE_NO_KEYCHAIN && keychainSet(secret)) {
	  cachedGatewayAPIKey = credentials.gateway_api_key ?? "";
    return "keychain";
  }
	fileTokenSet(secret);
	cachedGatewayAPIKey = credentials.gateway_api_key ?? "";
  return "file";
}

function gatewayAPIKeyFromCredentialStore(): string {
	if (cachedGatewayAPIKey !== undefined) return cachedGatewayAPIKey;
	try {
	  const parsed = JSON.parse(readFileSync(configPath(), "utf8")) as Partial<Config>;
	  cachedGatewayAPIKey = resolveCredentials(parsed).gateway_api_key ?? "";
	} catch {
	  cachedGatewayAPIKey = "";
	}
	return cachedGatewayAPIKey;
}

let cachedGatewayAPIKey: string | undefined;

function connectedGatewayAPIKey(): string {
	return firstEnvSecret(process.env, ["CAVE_API_KEY"]) ?? gatewayAPIKeyFromCredentialStore();
}

function accessTokenExpiresSoon(token: string): boolean {
	const payload = token.split(".")[0];
	if (!payload) return false;
	try {
	  const claims = JSON.parse(Buffer.from(payload, "base64url").toString("utf8")) as { exp?: unknown };
	  return typeof claims.exp === "number" && claims.exp <= Math.floor(Date.now() / 1000) + 60;
	} catch {
	  return false;
	}
}

async function refreshCLIConfig(cfg: Config): Promise<Config> {
	if (!cfg.refreshToken) return cfg;
	try {
	  const response = await fetch(`${cfg.baseURL}/api/v1/auth/refresh`, {
	    method: "POST",
	    headers: { "content-type": "application/json", "x-cave-client": "cli" },
	    body: JSON.stringify({ refresh_token: cfg.refreshToken }),
	    signal: AbortSignal.timeout(5000),
	  });
	  if (!response.ok) return cfg;
	  const body = await response.json() as Record<string, unknown>;
	  if (typeof body.access_token !== "string" || typeof body.refresh_token !== "string") return cfg;
	  const credentials: StoredCredentials = {
	    access_token: body.access_token,
	    refresh_token: body.refresh_token,
	    ...(cfg.gatewayApiKey ? { gateway_api_key: cfg.gatewayApiKey } : {}),
	    ...(cfg.gatewayKeyId ? { gateway_key_id: cfg.gatewayKeyId } : {}),
	    ...(cfg.projectId ? { project_id: cfg.projectId } : {}),
	  };
	  const tokenStore = storeCredentials(credentials);
	  const next = { ...cfg, token: body.access_token, refreshToken: body.refresh_token, tokenStore };
	  await saveConfig(next);
	  return next;
	} catch {
	  return cfg;
	}
}

function clearToken(tokenStore?: TokenStore) {
	cachedGatewayAPIKey = undefined;
  if (tokenStore === "keychain") {
    keychainDelete();
    return;
  }
  if (tokenStore === "file") {
    fileTokenDelete();
    return;
  }
  // Legacy inline-token configs predate tokenStore. Clean up old fallback
  // files, and only touch the macOS Keychain when keychain use is enabled.
  if (process.platform === "darwin" && !process.env.CAVE_NO_KEYCHAIN) keychainDelete();
  fileTokenDelete();
}

const KEYCHAIN_SERVICE = "caveman";
const KEYCHAIN_ACCOUNT = "token";

function keychainSet(token: string): boolean {
  return genericKeychainSet(KEYCHAIN_SERVICE, KEYCHAIN_ACCOUNT, token);
}

function keychainGet(): string {
  return genericKeychainGet(KEYCHAIN_SERVICE, KEYCHAIN_ACCOUNT);
}

function keychainDelete() {
  genericKeychainDelete(KEYCHAIN_SERVICE, KEYCHAIN_ACCOUNT);
}

function genericKeychainSet(service: string, account: string, secret: string): boolean {
  try {
    execFileSync("security", ["add-generic-password", "-U", "-s", service, "-a", account, "-w", secret], { stdio: "ignore" });
    return true;
  } catch {
    return false;
  }
}

function genericKeychainGet(service: string, account: string): string {
  try {
    return execFileSync("security", ["find-generic-password", "-s", service, "-a", account, "-w"], {
      encoding: "utf8",
      stdio: ["ignore", "pipe", "ignore"],
    }).trim();
  } catch {
    return "";
  }
}

function genericKeychainDelete(service: string, account: string) {
  try {
    execFileSync("security", ["delete-generic-password", "-s", service, "-a", account], { stdio: "ignore" });
  } catch (error) {
    // macOS security exits 44 for errSecItemNotFound. Absence is the desired
    // postcondition; every other failure means the secret may still exist.
    if ((error as { status?: unknown }).status === 44) return;
    throw new Error("could not remove credentials from macOS Keychain");
  }
}

function caveHome() {
  return process.env.CAVEMAN_HOME ?? join(homedir(), ".caveman");
}

function credentialsPath() {
  return join(caveHome(), "credentials");
}

function fileTokenSet(token: string) {
  ensureCavemanHome();
  try { chmodSync(credentialsPath(), 0o600); } catch { /* created below */ }
  writeFileSync(credentialsPath(), token, { mode: 0o600 });
  chmodSync(credentialsPath(), 0o600);
}

function fileTokenGet(): string {
  try {
    return readFileSync(credentialsPath(), "utf8").trim();
  } catch {
    return "";
  }
}

function fileTokenDelete() {
  try {
    unlinkSync(credentialsPath());
  } catch (error) {
    if ((error as NodeJS.ErrnoException).code !== "ENOENT") throw error;
  }
}

// orgFromToken decodes the organization id from the access token's claims (the
// base64url JSON payload of the HMAC token). It binds organization_id from the
// server-issued token, never from any local input.
function orgFromToken(token: string): string | undefined {
  const payload = token.split(".")[0];
  if (!payload) return undefined;
  try {
    const claims = JSON.parse(Buffer.from(payload, "base64url").toString("utf8"));
    return typeof claims.oid === "string" ? claims.oid : undefined;
  } catch {
    return undefined;
  }
}

async function projectId() {
  const cfg = await config();
  if (cfg.projectId) return cfg.projectId;
  const projects = await get("/api/v1/projects");
  return projects.data?.[0]?.id ?? "";
}

function configPath() {
  return join(homedir(), ".caveman-cloud", "config.json");
}

function print(value: unknown) {
  console.log(JSON.stringify(value, null, 2));
}

function shellHint(commandLine: string) {
  console.log(commandLine);
}

export const HELP_SCREEN = `caveman

run
  caveman <agent>        run an agent on the layer  ({{agents}})
  caveman run -- <cmd>   run anything else on the layer

understand
  caveman learn          short setup score + top moves
  caveman status         what the layer did today

connect
  caveman login          free · 1 seat · no card

more
  caveman tools          local, no account   ·  caveman help tools
  caveman cloud          connected           ·  caveman help cloud`;

function renderedAgentList(): string {
  const visible = AGENTS.slice(0, 7).map((agent) => agent.id);
  const remaining = AGENTS.length - visible.length;
  return `${visible.join(" | ")}${remaining > 0 ? ` | +${remaining} more — ${invokedAs()} run` : ""}`;
}

function renderHelp(): string {
  const screen = HELP_SCREEN
    .replaceAll("caveman", invokedAs())
    .replace("{{agents}}", renderedAgentList());
  const missingRequired = GO_BINARIES.some((binary) => binary.required && !resolveGoBin(binary.name, binary.env));
  return `${screen}${missingRequired ? `\n${invokedAs()} setup --install   install the missing binaries` : ""}`;
}

function help(argv: string[]) {
  if (argv[0] === "wrap") {
    wrapUsage("stdout");
    return;
  }
  if (argv[0] === "learn") {
    learnUsage();
    return;
  }
  console.log(renderHelp());
}

// ===========================================================================
// Terminal UX toolkit (zero-dependency). Panels and an arrow-key picker make the
// local verbs feel like one tool. Everything is TTY-gated: piped/non-interactive
// runs degrade to plain one-line errors — which is what the test suite asserts.
// ===========================================================================

function interactive(): boolean {
  return !!(process.stdin.isTTY && process.stderr.isTTY);
}

function useColor(): boolean {
  return !process.env.NO_COLOR && !!process.stderr.isTTY;
}

function paint(code: string, s: string): string {
  return useColor() ? `\x1b[${code}m${s}\x1b[0m` : s;
}
const bold = (s: string) => paint("1", s);
const dim = (s: string) => paint("2", s);
const cyan = (s: string) => paint("36", s);
const green = (s: string) => paint("32", s);
const yellow = (s: string) => paint("33", s);
const red = (s: string) => paint("31", s);

function mark(state: "ok" | "bad" | "warn"): string {
  return state === "ok" ? green("✓") : state === "warn" ? yellow("⚠") : red("✗");
}

function stripAnsi(s: string): string {
  return s.replace(/\x1b\[[0-9;]*m/g, "");
}

// panel draws a bordered box to stderr (keeping stdout clean for pipes), sizing
// itself to the widest visible line (ANSI codes excluded from the width math).
function panel(title: string, lines: string[]): void {
  if (!interactive()) {
    process.stderr.write([title, ...lines].map(stripAnsi).join("\n") + "\n");
    return;
  }
  const width = Math.max(stripAnsi(title).length, ...lines.map((l) => stripAnsi(l).length), 0);
  const pad = (s: string) => s + " ".repeat(width - stripAnsi(s).length);
  const bar = "─".repeat(width + 2);
  const out = process.stderr;
  out.write("\n" + dim("┌" + bar + "┐") + "\n");
  out.write(dim("│ ") + bold(pad(title)) + dim(" │") + "\n");
  out.write(dim("│ ") + pad("") + dim(" │") + "\n");
  for (const line of lines) out.write(dim("│ ") + pad(line) + dim(" │") + "\n");
  out.write(dim("└" + bar + "┘") + "\n\n");
}

// truncVisible clips a (possibly ANSI-colored) string to `budget` visible columns,
// copying escape sequences through untouched and appending an ellipsis + reset.
// Keeping every menu row within the terminal width means a row never wraps, so the
// redraw's "move up N lines" math stays exact.
function truncVisible(s: string, budget: number): string {
  const esc = String.fromCharCode(27);
  let res = "";
  let vis = 0;
  for (let i = 0; i < s.length; i++) {
    const ch = s[i]!;
    if (ch === esc) {
      let j = i + 1;
      if (s[j] === "[") {
        j++;
        while (j < s.length && !/[A-Za-z]/.test(s[j] ?? "")) j++;
      }
      res += s.slice(i, j + 1);
      i = j;
      continue;
    }
    if (vis >= budget - 1) return res + "…" + esc + "[0m";
    res += ch;
    vis++;
  }
  return res;
}

// selectMenu renders an arrow-key single-select list and resolves the chosen index
// (or -1 if cancelled). The caller must ensure interactive() first.
//
// Redraw is in-place and scroll-safe: each frame is the N items + a help line with
// NO trailing newline (so drawing never scrolls the buffer), rows are truncated to
// the terminal width (so none wrap), and each repaint rewinds exactly N lines and
// clears to end of screen before rewriting. The cursor is hidden for the duration.
function selectMenu(title: string, items: { label: string; hint?: string }[]): Promise<number> {
  return new Promise((resolve) => {
    const out = process.stderr;
    const stdin = process.stdin;
    const n = items.length;
    const ESC = String.fromCharCode(27);
    const ETX = String.fromCharCode(3);
    let idx = 0;
    let drawn = false;

    const cols = () => (out.columns && out.columns > 0 ? out.columns : 80);
    const frame = () => {
      const budget = cols();
      const rows = items.map((it, i) => {
        const on = i === idx;
        const text = it.label + (it.hint ? "  " + it.hint : "");
        return truncVisible((on ? cyan("❯ " + text) : "  " + text), budget);
      });
      rows.push(truncVisible(dim("↑/↓ move · 1-9 jump · enter select · esc cancel"), budget));
      return rows.join("\n");
    };
    const paint = () => {
      if (drawn) out.write("\r" + ESC + "[" + n + "A" + ESC + "[J");
      out.write(frame());
      drawn = true;
    };

    out.write("\n" + bold(title) + "\n\n" + ESC + "[?25l"); // title, then hide cursor
    paint();
    stdin.setRawMode?.(true);
    stdin.resume();
    stdin.setEncoding("utf8");

    const cleanup = () => {
      out.write(ESC + "[?25h\n"); // show cursor, drop below the menu
      stdin.setRawMode?.(false);
      stdin.pause();
      stdin.removeListener("data", onData);
    };
    const onData = (key: string) => {
      if (key === ETX) { cleanup(); resolve(-1); return; }
      if (key === "\r" || key === "\n") { cleanup(); resolve(idx); return; }
      if (key === ESC + "[A" || key === ESC + "OA" || key === "k") { idx = (idx - 1 + n) % n; paint(); return; }
      if (key === ESC + "[B" || key === ESC + "OB" || key === "j") { idx = (idx + 1) % n; paint(); return; }
      if (key === ESC) { cleanup(); resolve(-1); return; } // bare esc (after the arrow checks) cancels
      if (/^[1-9]$/.test(key)) {
        const d = Number(key) - 1;
        if (d < n) { idx = d; paint(); cleanup(); resolve(idx); }
      }
    };
    stdin.on("data", onData);
  });
}

// isExecutable / which resolve a command to an executable path via PATH (or check
// a literal path) — the basis for detecting whether an agent/proxy is installed.
function isExecutable(p: string): boolean {
  try { accessSync(p, constants.X_OK); return statSync(p).isFile(); } catch { return false; }
}

export function commandHasPath(command: string): boolean {
  return isAbsolute(command) || /[\\/]/.test(command);
}

export function executableCandidateNames(
  command: string,
  platform: NodeJS.Platform = process.platform,
  pathExt = process.env.PATHEXT ?? ".COM;.EXE;.BAT;.CMD",
): string[] {
  if (platform !== "win32" || extname(command)) return [command];
  const candidates: string[] = [];
  for (const extension of pathExt.split(";").map((value) => value.trim()).filter(Boolean)) {
    candidates.push(`${command}${extension.startsWith(".") ? extension : `.${extension}`}`);
  }
  // Windows cannot launch extensionless POSIX npm shims without a shell. Keep
  // the bare name as a fallback for real extensionless executables, but prefer
  // native executables and cmd/bat shims from PATHEXT.
  candidates.push(command);
  const seen = new Set<string>();
  return candidates.filter((candidate) => {
    const key = candidate.toLowerCase();
    if (seen.has(key)) return false;
    seen.add(key);
    return true;
  });
}

function which(cmd: string): string | null {
  if (commandHasPath(cmd)) return isExecutable(cmd) ? cmd : null;
  for (const dir of (process.env.PATH ?? "").split(delimiter)) {
    if (!dir) continue;
    for (const candidate of executableCandidateNames(cmd)) {
      const full = join(dir, candidate);
      if (isExecutable(full)) return full;
    }
  }
  return null;
}

// dockerState reports whether the Docker daemon is reachable, so `caveman start`
// can give an honest hint about the `make dev` path.
function dockerState(): "running" | "stopped" | "absent" {
  if (!which("docker")) return "absent";
  try { execFileSync("docker", ["info"], { stdio: "ignore", timeout: 4000 }); return "running"; }
  catch { return "stopped"; }
}

// portListening probes a TCP port with a short timeout — used to detect whether
// the proxy is already up, so start/wrap can say so without false positives.
function portListening(host: string, port: number): Promise<boolean> {
  return new Promise((resolve) => {
    const sock = netConnect({ host, port });
    const finish = (v: boolean) => { sock.destroy(); resolve(v); };
    sock.setTimeout(600);
    sock.once("connect", () => finish(true));
    sock.once("timeout", () => finish(false));
    sock.once("error", () => finish(false));
  });
}

function standaloneProxyEndpoint(): { host: string; port: number; listen: string } {
  const listen = (process.env.CAVEMAN_LISTEN ?? PROXY_ADDR).trim();
  try {
    const url = new URL(`http://${listen}`);
    const port = Number(url.port);
    if (!url.hostname || !Number.isInteger(port) || port < 1 || port > 65535 || url.pathname !== "/") throw new Error("invalid listener");
    return { host: url.hostname, port, listen };
  } catch {
    throw new Error(`invalid CAVEMAN_LISTEN ${JSON.stringify(listen)}; expected host:port`);
  }
}

export function gatewayHostPort(gw = gatewayURL()): { host: string; port: number } {
  try {
    const u = new URL(gw);
    const defaultPort = u.protocol === "https:" ? 443 : 80;
    return { host: u.hostname || "127.0.0.1", port: Number(u.port) || defaultPort };
  } catch {
    return { host: "127.0.0.1", port: 8787 };
  }
}

function isCliEntrypoint(): boolean {
  if (!process.argv[1]) return false;
  const here = fileURLToPath(import.meta.url);
  try {
    return realpathSync(process.argv[1]) === here;
  } catch {
    return process.argv[1] === here;
  }
}

if (isCliEntrypoint()) {
  main().catch((error) => {
    console.error(error.message);
    process.exitCode = 1;
  });
}
