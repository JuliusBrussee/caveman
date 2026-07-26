import { execFileSync, spawn, spawnSync } from "node:child_process";
import {
  accessSync,
  chmodSync,
  constants,
  copyFileSync,
  mkdirSync,
  mkdtempSync,
  readdirSync,
  readFileSync,
  realpathSync,
  rmSync,
  statSync,
  symlinkSync,
  unlinkSync,
  writeFileSync,
} from "node:fs";
import { chmod, mkdir, open, readFile, rename, writeFile } from "node:fs/promises";
import { homedir, hostname, tmpdir } from "node:os";
import { basename, delimiter, dirname, join, resolve } from "node:path";
import { connect as netConnect, createServer as netCreateServer, isIP, type AddressInfo } from "node:net";
import { createHash, createPublicKey, randomUUID, verify as edVerify, type KeyObject } from "node:crypto";
import { fileURLToPath } from "node:url";
import { PROFILES, type AgentProfile } from "./agents.generated.js";
import {
  BINARY_RELEASE,
  BINARY_RELEASE_BASE_DEFAULT,
  BINARY_SIGNING_PUBKEY,
} from "./binaries.generated.js";
import { RECIPES, type IntegrationRecipe } from "./recipes.generated.js";
import { RESERVED_VERBS } from "./reserved-verbs.generated.js";

type TokenStore = "keychain" | "file";
type TelemetryConfig = { enabled: boolean; anonymousId?: string; decidedAt: string; promptVersion: number };
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
// load — so `caveman login` persisting a managed gateway URL flips `wrap`/`start`
// to the cloud with no env var (SIMPLICITY_SPEC §6.5, audit finding #3). Precedence:
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
  verbs: { verb: string; description: string }[];
};

const TOOL_DISCOVERY: DiscoveryGroup[] = [
  { heading: "think", verbs: [
    { verb: "compress", description: "compress stdin with truthful fallback" },
    { verb: "shrink", description: "shrink command output with recovery" },
    { verb: "shrink-hook", description: "run output-shrink hook" },
    { verb: "toon", description: "encode or decode structured data" },
    { verb: "convert", description: "convert agent configuration" },
  ] },
  { heading: "remember", verbs: [
    { verb: "mem", description: "store and recall durable memory" },
    { verb: "retrieve", description: "recover byte-exact compressed content" },
  ] },
  { heading: "execute", verbs: [
    { verb: "mcp", description: "install or remove recovery tools" },
    { verb: "hooks", description: "install or remove agent hooks" },
    { verb: "browse", description: "browse through compressed CDP tools" },
    { verb: "skills", description: "install Caveman agent skills" },
    { verb: "sdk", description: "print SDK integration recipes" },
  ] },
  { heading: "inspect", verbs: [
    { verb: "stats", description: "print raw local proxy statistics" },
    { verb: "evals", description: "run local quality gates" },
    { verb: "config", description: "inspect or change capability config" },
  ] },
];

const CLOUD_DISCOVERY: DiscoveryGroup[] = [
  { heading: "account", verbs: [
    { verb: "whoami", description: "show connected identity" },
    { verb: "projects", description: "list or create projects" },
    { verb: "keys", description: "create or revoke project keys" },
    { verb: "providers", description: "list or verify providers" },
    { verb: "billing", description: "inspect verified-savings billing" },
  ] },
  { heading: "evidence", verbs: [
    { verb: "score", description: "show scoped Cave Score" },
    { verb: "costs", description: "show provider-complete cost totals" },
    { verb: "plan", description: "show ranked inferred Cave Plan" },
    { verb: "traces", description: "list, show, or export traces" },
    { verb: "experiments", description: "list eval-gated experiments" },
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
    if (argv[0] === "install") return mcpInstall(argv[1]);
    if (argv[0] === "uninstall") return mcpUninstall(argv[1], flagFrom(argv, "--server", "caveman"));
    return mcpUsage();
  },
  hooks: hooksCmd,
  browse,
  skills,
  sdk: (argv) => {
    if (argv[0] === "snippet" || argv[0] === "snippets") {
      return argv.length > 1 ? snippets(argv.slice(1)) : sdkSnippet();
    }
    return sdkSnippet();
  },
  stats: () => stats(),
  evals: (argv) => argv[0] === "run" ? evalsRun() : evalsRun(),
  config: capabilityConfigCommand,
};

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
  traces: (argv) => {
    if (argv[0] === "list") return get("/api/v1/traces").then(print);
    if (argv[0] === "show") return get(`/api/v1/traces/${argv[1] ?? "demo-trace"}`).then(print);
    if (argv[0] === "export") return post("/api/v1/traces/export", {}).then(print);
    return commandUsage("traces list|show <id>|export");
  },
  experiments: (argv) => argv[0] === "list" ? get("/api/v1/experiments").then(print) : commandUsage("experiments list"),
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
  login,
  logout: () => logout(),
  init,
  doctor: () => doctor(),
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
  start: () => start(),
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

for (const [verb, handler] of Object.entries(TOOL_HANDLERS)) LEGACY_HANDLERS[verb] = handler;
for (const [verb, handler] of Object.entries(CLOUD_HANDLERS)) LEGACY_HANDLERS[verb] = handler;

function commandUsage(suffix: string): never {
  const prefix = currentInvocation?.group ? `${invokedAs()} ${currentInvocation.group}` : invokedAs();
  console.error(`usage: ${prefix} ${suffix}`);
  process.exit(2);
}

function printDiscovery(group: CommandGroup): void {
  const groups = group === "tools" ? TOOL_DISCOVERY : CLOUD_DISCOVERY;
  console.log(`${invokedAs()} ${group} — ${group === "tools" ? "local capabilities" : "connected verbs"}`);
  for (const section of groups) {
    console.log(`\n${section.heading}`);
    for (const entry of section.verbs) console.log(`  ${entry.verb.padEnd(13)} ${entry.description}`);
  }
}

function resolveInvocation(raw: string[]): ResolvedInvocation {
  const top = raw[0] ?? "help";
  if (top === "--help") return { verb: "help", argv: [], handler: help };
  if ((top === "tools" || top === "cloud") && raw.length === 1) {
    return { verb: top, argv: [], group: top, handler: () => printDiscovery(top) };
  }
  if (top === "help" && (raw[1] === "tools" || raw[1] === "cloud")) {
    const group = raw[1];
    return { verb: "help", argv: [group], handler: () => printDiscovery(group) };
  }
  if (top === "tools" || top === "cloud") {
    const group = top;
    const verb = raw[1] ?? "";
    const handlers = group === "tools" ? TOOL_HANDLERS : CLOUD_HANDLERS;
    const handler = handlers[verb];
    if (handler) return { verb, argv: raw.slice(2), group, handler };
    return { verb, argv: raw.slice(2), group, handler: () => unknownInvocation(verb, group) };
  }
  const handler = LEGACY_HANDLERS[top];
  if (handler) return { verb: top, argv: raw.slice(1), handler };
  const agent = RESERVED_VERBS.has(top) ? undefined : findAgent(top);
  if (agent) return {
    verb: "wrap",
    argv: normalizeAgentShortcutWrapArgs(raw),
    handler: wrap,
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
  if (!group && (token.includes("/") || token.startsWith(".") || which(token))) {
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
const TELEMETRY_PROMPT_VERSION = 1;
const TELEMETRY_URL = "https://api.caveman.so/telemetry/cli";
const TELEMETRY_PROMPT_COPY =
  "Help improve Caveman? Caveman can send anonymous usage data — the command name, CLI version, OS, error class, and timings. Never prompts, completions, code, or file paths. Never tied to your account. Change anytime: caveman telemetry off · CAVEMAN_TELEMETRY=0 · DO_NOT_TRACK=1";
const TELEMETRY_PROMPT_LINE = "Send anonymous usage data? [y/N]";
const SYNC_DISCLOSURE =
  "sync uploads span metadata to your org's dashboard — tokens, cost, latency, model, status. Imported standalone observations never affect managed budgets, verified savings, or billing. Subscription/OAuth sessions carry token counts only — no dollar figure. Never prompt or response bytes.";
const TELEMETRY_COMMAND_ALLOWLIST = [
  "agent", "audit", "billing", "browse", "compress", "convert", "costs", "deploy", "dev", "doctor", "evals", "experiments",
  "explore", "help", "hooks", "init", "keys", "learn", "login", "logout", "mcp", "mem", "opportunities", "plan",
  "projects", "providers", "receipts", "retrieve", "score", "sdk", "setup", "shrink", "shrink-hook", "skills",
  "run", "snippets", "start", "stats", "status", "sync", "telemetry", "toon", "traces", "trial", "unknown", "usage", "verify", "version", "whoami", "wrap",
] as const;
const TELEMETRY_COMMANDS = new Set<string>(TELEMETRY_COMMAND_ALLOWLIST);
const TELEMETRY_PROMPT_COMMANDS = new Set(["start", "run", "wrap", "compress", "login", "setup", "init", "sync"]);
const TELEMETRY_SUBCOMMANDS = new Set([
  "act", "apply", "aws", "charges", "create", "decode", "down", "encode", "eval", "export", "forget", "import",
  "install", "link", "list", "off", "on", "recall", "recover", "refresh", "remember", "report", "reset", "revoke",
  "run", "show", "snippet", "status", "uninstall", "unlink", "up", "verify",
]);
const TELEMETRY_START_MS = Date.now();
let telemetryCommandSent = false;
let telemetryEphemeralId = "";

async function main() {
  await maybePromptTelemetry();
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

type TelemetryState = "on" | "off" | "undecided";
type TelemetrySource = "env" | "config" | "runtime" | "undecided";
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
  return { state: "undecided", source: "undecided", config: undefined };
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

async function maybePromptTelemetry() {
  const state = telemetryState();
  if (state.state !== "undecided") return;
  if (isHelpLikeInvocation()) return;
  if (!TELEMETRY_PROMPT_COMMANDS.has(telemetryCommandName())) return;
  const enabled = await promptTelemetryConsent();
  const telemetry: TelemetryConfig = { enabled, decidedAt: new Date().toISOString(), promptVersion: TELEMETRY_PROMPT_VERSION };
  if (enabled) telemetry.anonymousId = randomUUID();
  await saveTelemetryConfig(telemetry);
  if (enabled && telemetry.anonymousId) emitConsentGranted(telemetry.anonymousId);
}

function isHelpLikeInvocation(): boolean {
  return currentInvocation.verb === "help"
    || currentInvocation.verb === "version"
    || currentInvocation.argv[0] === "--help"
    || currentInvocation.argv[0] === "-h";
}

function promptTelemetryConsent(): Promise<boolean> {
  process.stderr.write(`${TELEMETRY_PROMPT_COPY}\n${TELEMETRY_PROMPT_LINE} `);
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
    // Ctrl-D / closed stdin must resolve as the default No, never hang the CLI.
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
  print({ telemetry: "off", anonymous_id: "none" });
}

function telemetryCommandName(): string {
  return TELEMETRY_COMMANDS.has(currentInvocation.verb) ? currentInvocation.verb : "unknown";
}

function telemetrySubcommand(): string | undefined {
  if (currentInvocation.verb === "telemetry" && !currentInvocation.argv[0]) return "status";
  const sub = currentInvocation.argv[0];
  return sub && TELEMETRY_SUBCOMMANDS.has(sub) ? sub : undefined;
}

function telemetryAgent(): string | undefined {
  if (currentInvocation.agent) return currentInvocation.agent.id;
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

function emitCommandRunOnce(exitClass: TelemetryExitClass, errorClass?: TelemetryErrorClass) {
  if (telemetryCommandSent) return;
  const state = telemetryState();
  if (state.state !== "on") return;
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
  emitTelemetryEvents([event]);
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

function emitTelemetryEvents(events: Record<string, unknown>[]) {
  const url = process.env.CAVEMAN_TELEMETRY_URL || TELEMETRY_URL;
  fetch(url, {
    method: "POST",
    headers: { "content-type": "application/json" },
    body: JSON.stringify(events),
    signal: AbortSignal.timeout(1500),
  }).catch(() => {});
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
  } else {
    browserArgs = ["snapshot", rest[0]!];
  }
  await spawnBrowse(bin, browserArgs);
}

function browseUsage(): never {
  console.error(`usage: ${invokedCommand("browse")} <url>`);
  console.error("       caveman browse act <uid> click|type|select|scroll [text]");
  console.error("       caveman browse recover <handle> [query]");
  console.error("       caveman browse eval <expression>");
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

// canonical artifact lives in public/skills/caveman-explore/SKILL.md; this
// constant is a byte-identical copy because the published CLI ships no sibling
// assets (public/cli/tests/skills.runtime.mjs asserts they stay equal). Keep them
// in sync.
const CAVEMAN_EXPLORE_SKILL_MD = `---
name: caveman-explore
description: Read-only repository explorer. Use PROACTIVELY for cold-start exploration, broad cross-file localization, or when a direct search has failed and you need to find where something lives. Skip it when the issue already names the exact file or symbol, or a previous turn already returned usable file:line evidence. Returns only compact path:line citations; its reads and greps never enter the main conversation.
tools: Read, Glob, Grep
model: haiku
---

You are FastContext, a fast, cheap, read-only repository explorer. Another agent
(the solver) delegates a localization question to you. Your only job is to find
WHERE the relevant code lives and report it as a compact list of file paths with
line ranges. You never edit files, run commands, or propose a solution.

How to work:

1. Issue several tool calls IN PARALLEL in your first turn — cast a broad net.
   Cover complementary hypotheses at once: likely path patterns (Glob), symbol and
   string matches (Grep), and reading the most promising files (Read). Do not probe
   one file at a time when you can fan out.
2. Follow the evidence over one or two more turns only if needed. Stop as soon as
   you can name the relevant locations. You are optimizing for the solver's token
   budget, so finish fast.
3. Only cite line ranges you actually read. Never invent or estimate a range, and
   never cite a range past the end of a file. A precise small range beats a vague
   large one.

Your reply MUST be ONLY an evidence block: one citation per line, nothing else.
No preamble, no explanation, no summary, no markdown headings. Use exactly this
shape, one per line:

  path/to/file.ext:START-END  reason it is relevant

Example reply:

  src/router/pick.go:42-71  route selection — where a model is chosen
  src/router/pick_test.go:18-40  the table test covering pick()

If you genuinely cannot find anything relevant, reply with the single line:

  no relevant locations found

That honest answer is better than a guess. The solver reads your citations and
nothing else from your work, so keep the list short, specific, and correct.
`;

// canonical artifact lives in public/skills/caveman-learn/SKILL.md; this constant
// is a byte-identical copy because the published CLI ships no sibling assets
// (public/cli/tests/skills.runtime.mjs asserts they stay equal). Keep them in sync.
const CAVEMAN_LEARN_SKILL_MD = `---
name: caveman-learn
description: Close the loop on a Caveman learn report — review the ranked token sinks and apply cost-lowering fixes (trim config, offload recurring context to cavemem) with per-edit consent. Use when the user runs "caveman learn", asks to lower their agent's token cost, wants to trim a heavy CLAUDE.md, or wants to offload context they re-paste every session into cavemem.
---

You are the Caveman Learn editing skill. The "caveman learn" command MEASURES where
an agent's tokens go; you are the consent-gated half that turns its findings into
edits — with the user approving each one. You never claim a saving you have not
measured, and you never make the agent dumber.

Read the plan first:

1. Run: caveman learn report --json
   Parse the caveman.learn.v1 JSON. Show the Cave Score, its four components, and the
   ranked token sinks. For each sink state its class and basis. Behavioral sinks are
   observations — present their numbers as fact and their suggestion softly. Do not
   turn a behavioral finding into an imperative.

Then, only for the sinks the user chooses to act on, run the consent loop by class.

REDUCIBLE (a heavy CLAUDE.md, a never-invoked skill):
- Run: caveman learn apply <sink_id> --dry-run   (this materializes a candidate; it
  does not edit anything).
- Propose a concrete diff and show before -> after tokens/turn.
- Ask the user yes or no. On yes, apply the edit with your own file tools.
- Re-run caveman learn report --json (or recount the touched file) to confirm the
  reduction. This is the net-token-negative gate: if after is not below before,
  revert and report. Never keep an edit that does not reduce tokens/turn.

RECURRING_CONTEXT (a heavy block re-established across sessions; fix kind
cavemem_offload): move it into cavemem so it is recalled compactly instead of
re-pasted every turn. The candidate carries only a LOCATOR — never the block body.
- Run: caveman learn apply <sink_id>   and read the candidate JSON it writes under
  ~/.caveman/candidates/. Take only the locator, the numbers, and the proposed pointer
  text. Do not trust any body from the candidate; there is none.
- Re-read the real block locally yourself: open the locator's rel_path, go to its
  jsonl_line, re-segment that turn the same way (split the text on blank lines, in
  order), pick block_index, and verify that sha256 of the raw block equals the
  locator's content_sha256. If it does not match, the file changed since the scan —
  abort this item.
- Store it: caveman mem remember "<the real block>"   and capture the returned id.
- Measure the gate honestly. before = the block's tokens/turn (it loaded every turn).
  after = the pointer's tokens/turn plus the recall cost. Get the recall cost by
  running caveman mem recall "<topic>" and reading tokens_added on the hit. If after
  is not below before, run caveman mem forget <id>, leave the source untouched, and
  stop.
- Trim the source and write the pointer. Remove the block from its CLAUDE.md or
  AGENTS.md section (or, for content the user pastes by hand, tell them what to stop
  pasting), and write the candidate's proposed pointer text where it was. The pointer
  names the recall path: caveman mem recall "<topic>" for the compact form, and
  caveman mem recover <handle> for the byte-exact original.
- Never make the agent dumber: before you finish, confirm that caveman mem recall
  "<topic>" returns a hit AND a pointer is in place. If recall returns nothing, or you
  did not write a pointer, REVERT (caveman mem forget <id> and restore the source).
  Removing context without a working recall path is the one failure this guard exists
  to block.
- Re-measure and report the confirmed reduction and the recall path.

LOAD_BEARING: never touch. It appears in the report only so the score stays honest.

Binding rules:
- Consent per edit. No "apply all" that hides the individual diffs.
- Every edit is reversible: report exactly what you changed. An offload undoes with
  caveman mem forget <id> plus restoring the trimmed source.
- inferred only. Never present a local number as verified, and never attach a currency.
- The analyzer (caveman learn) is read-only. You are the only writer, and only after a
  yes.
`;

// canonical artifact lives in public/skills/caveman/SKILL.md; this constant is
// a byte-identical copy because the published CLI ships no sibling assets
// (public/cli/tests/skills.runtime.mjs asserts they stay equal). Keep them in sync.
const CAVEMAN_SKILL_MD = `---
name: caveman
description: >
  Ultra-compressed communication mode. Cuts output tokens 65% (measured) by speaking like caveman
  while keeping full technical accuracy. Supports intensity levels: lite, full (default), ultra,
  wenyan-lite, wenyan-full, wenyan-ultra.
  Use when user says "caveman mode", "talk like caveman", "use caveman", "less tokens",
  "be brief", or invokes /caveman. Also auto-triggers when token efficiency is requested.
---

Respond terse like smart caveman. All technical substance stay. Only fluff die.

## Persistence

ACTIVE EVERY RESPONSE. No revert after many turns. No filler drift. Still active if unsure. Off only: "stop caveman" / "normal mode".

Default: **full**. Switch: \`/caveman lite|full|ultra\`.

## Rules

Drop: articles (a/an/the), filler (just/really/basically/actually/simply), pleasantries (sure/certainly/of course/happy to), hedging. Fragments OK. Short synonyms (big not extensive, fix not "implement a solution for"). No tool-call narration, no decorative tables/emoji, no dumping long raw error logs unless asked — quote shortest decisive line. Standard well-known tech acronyms OK (DB/API/HTTP); never invent new abbreviations (cfg/impl/req/res/fn) — tokenizer split them same as full word: zero token saved, reader still decode. Full word cheaper AND clearer. No causal arrows (→) either — own token, save nothing. Technical terms exact. Code blocks unchanged. Errors quoted exact.

Preserve user's dominant language. User write Portuguese → reply Portuguese caveman. User write Spanish → reply Spanish caveman. Compress the style, not the language. No forced English openings or status phrases. ALWAYS keep technical terms, code, API names, CLI commands, commit-type keywords (feat/fix/...), and exact error strings verbatim — unless user explicitly ask for translation.

No self-reference. Never name or announce the style. No "caveman mode on", "me caveman think", no third-person caveman tags. Output caveman-only — never normal answer plus "Caveman:" recap. Exception: user explicitly ask what the mode is.

Pattern: \`[thing] [action] [reason]. [next step].\`

Not: "Sure! I'd be happy to help you with that. The issue you're experiencing is likely caused by..."
Yes: "Bug in auth middleware. Token expiry check use \`<\` not \`<=\`. Fix:"

## Intensity

| Level | What change |
|-------|------------|
| **lite** | No filler/hedging. Keep articles + full sentences. Professional but tight |
| **full** | Drop articles, fragments OK, short synonyms. Classic caveman. No tool-call narration, no decorative tables/emoji, no long raw error-log dumps unless asked. Standard acronyms OK; no invented abbreviations |
| **ultra** | Strip conjunctions when cause-then-effect stay unambiguous. One word when one word enough. State each fact once. NO prose abbreviations (cfg/impl/req/res/fn/auth), NO arrows (X → Y) — measured zero token saving under tokenizer, cost decode clarity. Code symbols, function names, API names, error strings: never touch |
| **wenyan-lite** | Semi-classical. Drop filler/hedging but keep grammar structure, classical register |
| **wenyan-full** | Maximum classical terseness. Fully 文言文. 80-90% character reduction. Classical sentence patterns, verbs precede objects, subjects often omitted, classical particles (之/乃/為/其) |
| **wenyan-ultra** | Extreme abbreviation while keeping classical Chinese feel. Maximum compression, ultra terse |

Example — "Why React component re-render?"
- lite: "Your component re-renders because you create a new object reference each render. Wrap it in \`useMemo\`."
- full: "New object ref each render. Inline object prop = new ref = re-render. Wrap in \`useMemo\`."
- ultra: "Inline obj prop, new ref, re-render. \`useMemo\`."
- wenyan-lite: "組件頻重繪，以每繪新生對象參照故。以 useMemo 包之。"
- wenyan-full: "每繪新生對象參照，故重繪；以 useMemo 包之則免。"
- wenyan-ultra: "新參照則重繪。useMemo 包之。"

Example — "Explain database connection pooling."
- lite: "Connection pooling reuses open connections instead of creating new ones per request. Avoids repeated handshake overhead."
- full: "Pool reuse open DB connections. No new connection per request. Skip handshake overhead."
- ultra: "Pool reuse open DB connections. No per-request handshake."
- wenyan-full: "池蓄已開之連，不逐請而新開，省握手之費。"
- wenyan-ultra: "池蓄連，免逐請新開，省握手。"

## Auto-Clarity

Drop caveman when:
- Security warnings
- Irreversible action confirmations
- Multi-step sequences where fragment order or omitted conjunctions risk misread
- Compression itself creates technical ambiguity (e.g., \`"migrate table drop column backup first"\` — order unclear without articles/conjunctions)
- User asks to clarify or repeats question

Resume caveman after clear part done.

Example — destructive op:
> **Warning:** This will permanently delete all rows in the \`users\` table and cannot be undone.
> \`\`\`sql
> DROP TABLE users;
> \`\`\`
> Caveman resume. Verify backup exist first.

## Boundaries

Code/commits/PRs: write normal. "stop caveman" or "normal mode": revert. Level persist until changed or session end.`;

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
  console.error(`caveman explore: only --agent claude is wired today (codex needs verified transcript isolation first — see docs/FASTCONTEXT_EXPLORER_SPEC.md). got: ${agent}`);
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

const SKILLS: Record<string, string> = {
  caveman: CAVEMAN_SKILL_MD,
  "caveman-explore": CAVEMAN_EXPLORE_SKILL_MD,
  "caveman-learn": CAVEMAN_LEARN_SKILL_MD,
};
const STUB_EST_TOKENS = 120;
const PIXEL_MARKER_PREFIX = "<!-- caveman-pixel v1 sha256:";
const SKILL_MD = "SKILL.md";
const SKILL_ORIG_MD = "SKILL.orig.md";

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
  return bin.includes("/") ? (isExecutable(bin) ? bin : null) : which(bin);
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

function writeConvertReport(results: ConvertResult[]) {
  const lines = results.map((result) => {
    if (result.kind === "converted") {
      return `${result.name}: ${conversionMath(result.textEst, result.afterEst)}${result.dryRun ? " (dry-run)" : ""}`;
    }
    if (result.kind === "reverted") return `${result.name}: reverted`;
    return `${result.name}: skipped — ${result.reason}`;
  });
  const converted = results.filter((r): r is Extract<ConvertResult, { kind: "converted" }> => r.kind === "converted");
  if (converted.length > 0) {
    const before = converted.reduce((sum, r) => sum + r.textEst, 0);
    const after = converted.reduce((sum, r) => sum + r.afterEst, 0);
    lines.push(`total: ${conversionMath(before, after)}`);
  }
  if (lines.length === 0) lines.push("caveman convert: no skills found");
  if (interactive()) panel("Caveman convert", lines);
  else console.log(lines.join("\n"));
}

function skillsUsage(): never {
  console.error(`usage: ${invokedCommand("skills")} install [name] [--agent claude|codex] [--user] [--dir <path>] [--density conservative|balanced|max] [--no-pixel]`);
  console.error("  install a Caveman agent skill (default name: caveman-learn)");
  console.error("  --agent claude   Claude Code (default) — writes .claude/skills/<name>/SKILL.md");
  console.error("  --agent codex    Codex — writes ~/.codex/skills/<name>/SKILL.md");
  console.error("  --user           install for all repos (~/.claude/skills) instead of this one");
  console.error("  --dir <path>     write the skill file directly into <path>");
  console.error("  --density LEVEL  pixel pack geometry (default balanced): conservative|balanced|max");
  console.error("  --no-pixel       install plain SKILL.md without pixel conversion");
  process.exit(2);
}

// skills installs a Caveman agent skill (its SKILL.md). The CLI embeds a
// byte-identical copy of each skill because the published binary ships no sibling
// assets; the canonical files live in public/skills/<name>/.
async function skills(rest: string[]) {
  if (rest[0] !== "install") return skillsUsage();
  // First positional after "install" is the skill name; skip flags and the values
  // that --agent/--dir/--density consume so a flag value is never mistaken for the name.
  const after = rest.slice(1);
  let name = "caveman-learn";
  for (let i = 0; i < after.length; i++) {
    const a = after[i];
    if (a === "--agent" || a === "--dir" || a === "--density") { i++; continue; }
    if (a && !a.startsWith("--")) { name = a; break; }
  }
  const body = SKILLS[name];
  if (!body) {
    console.error(`caveman skills: unknown skill ${JSON.stringify(name)} (known: ${Object.keys(SKILLS).join(", ")})`);
    process.exit(2);
  }
  const agent = flagFrom(rest, "--agent", "claude");
  if (name === "caveman-explore") guardExploreAgent(agent);
  let dest: string;
  if (agent === "claude") {
    const fallbackDir = rest.includes("--user")
      ? join(homedir(), ".claude", "skills", name)
      : join(process.cwd(), ".claude", "skills", name);
    const dir = flagFrom(rest, "--dir", fallbackDir);
    dest = join(dir, "SKILL.md");
  } else if (agent === "codex") {
    const dir = flagFrom(rest, "--dir", join(homedir(), ".codex", "skills", name));
    dest = join(dir, "SKILL.md");
  } else {
    console.error(`caveman skills: --agent must be claude or codex (got ${agent})`);
    process.exit(2);
  }
  try {
    mkdirSync(dirname(dest), { recursive: true });
    writeFileSync(dest, body);
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
  const formLine = rest.includes("--no-pixel")
    ? `${mark("ok")} Installed plain text (--no-pixel).`
    : pixelInstallLine(pixelResult);
  const note = agent === "codex"
    ? "Codex auto-loads skill directories from ~/.codex/skills."
    : "Claude Code auto-loads this skill when its description matches.";
  panel("Caveman skill installed", [
    `${mark("ok")} Wrote ${cyan(dest)}`,
    formLine,
    "",
    `The ${cyan(name)} skill is installed as a standard SKILL.md directory.`,
    "",
    dim(note),
  ]);
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
async function start() {
  const bin = cavemanBin("caveman-proxy", "CAVEMAN_PROXY_BIN");
  const { host, port } = gatewayHostPort();

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
  if (!resolved) return startMissingProxyUI(bin);

  // `caveman start` is the sibling entry point to `caveman wrap`, so it runs the
  // SAME account gate: the entitlement comes from config.json and the account signal
  // is stamped EXPLICITLY "1"/"0", never inherited — otherwise an exported
  // CAVEMAN_WRAP_ENTITLED=1 would unlock subscription compression here that wrap
  // refuses to grant. The mode this proxy actually runs is CAVEMAN_MODE (the proxy
  // itself falls back to record for anything unknown), so a plain `caveman start`
  // resolves to record → "0"; record never mutates bytes anyway. A mode set only in
  // the proxy's own caveman.yaml is invisible from here and also stamps "0" — the
  // gate may withhold the capability, never grant it. (honesty rule: byte-safe)
  const runtime = wrapRuntimeConfig({ forStart: true });
  const invalidMode = invalidModeLine(runtime.resolution);
  if (invalidMode) process.stderr.write(`${invalidMode}\n`);
  const modeSource = runtime.resolution.values["think.mode"].source;
  const requestedMode = modeSource === "default" ? "record" : runtime.mode;
  const startGate = resolveWrapGate(readWrapEntitlement(), new Date(), requestedMode);
  const subscriptionCompress = subscriptionCompressEnabled(startGate);
  // The entitlement is only half the gate: the proxy also refuses to compress
  // subscription/OAuth traffic without a recovery path (CAVEMAN_RECOVERY=mcp), so
  // `caveman start` stamps that signal the same way `caveman wrap` does — and only
  // claims compression when BOTH halves hold. Announcing it on the entitlement
  // alone advertised a capability the proxy had off. (honesty rule: no-placeholder)
  // Stamped EXPLICITLY in both directions, like the entitlement: the resolved
  // answer is what reaches the proxy, never whatever the environment happened to
  // carry. (start does count an explicit CAVEMAN_RECOVERY=mcp as the operator's own
  // opt-in — see startMcpRecoveryAvailable — but that decision is made HERE and
  // re-stamped, so the disclosure line and the proxy can never disagree.)
  const mcpRecovery = startMcpRecoveryAvailable();
  const env: NodeJS.ProcessEnv = {
    ...process.env,
    CAVEMAN_PROXY_OWNER: "start",
    CAVEMAN_WRAP_ENTITLED: subscriptionCompress ? "1" : "0",
    CAVEMAN_RECOVERY: mcpRecovery ? "mcp" : "",
  };
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
function startMissingProxyUI(bin: string): never {
  const { host, port } = gatewayHostPort();

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
// run in caveman's Go binaries (proxy/engine/mcp/browse). Without them every
// affected command degrades to a LOUD byte-safe pass-through — setup is the one
// place that shows exactly what works, what doesn't, and the one command that
// fixes it. It never installs anything itself. Exits non-zero when a required
// binary is missing so scripts can gate on it. (honesty rule: no-fake-savings —
// a pass-through claims 0% and says so.)
const GO_BINARIES = [
  { name: "caveman-proxy", env: "CAVEMAN_PROXY_BIN", required: true, powers: "start · wrap · stats · verify — local compression + truthful metering", without: "wrap still launches agents, but LLM traffic is NOT compressed or metered" },
  { name: "caveman-engine", env: "CAVEMAN_ENGINE_BIN", required: true, powers: "compress · shrink · retrieve · toon · evals", without: "compress/shrink pass input through unchanged (reported as 0%); toon decode refuses" },
  { name: "caveman-mcp", env: "CAVEMAN_MCP_BIN", required: true, powers: "mcp install — agent-side recovery that lets streaming requests compress", without: "streaming requests pass through uncompressed" },
  { name: "cavemem", env: "CAVEMEM_BIN", required: true, powers: "remember · recall · learn offload", without: "memory and auto-recall are off" },
  { name: "caveman-browse", env: "CAVEMAN_BROWSE_BIN", required: false, powers: "browse + agent-side compressed browsing MCP tools — wrap auto-registers once present", without: "agent-side compressed browsing MCP tools unavailable; wrap auto-registers once installed" },
] as const;

// resolveGoBin is cavemanBin plus an honest "is it actually there" answer: the
// bare-name fallback that keeps the missing-binary panels working is NOT a find.
function resolveGoBin(name: string, envVar: string): string | null {
  const bin = cavemanBin(name, envVar);
  return bin.includes("/") ? (isExecutable(bin) ? bin : null) : which(bin);
}

type VersionedBinaryProbe = {
  version: string;
  capabilities: string[];
  current: boolean;
};

const versionedBinaryProbeCache = new Map<string, VersionedBinaryProbe>();

// Compatibility probes run on the launch path, so each resolved executable is
// checked at most once per process and file generation. stdin is closed and the
// hard timeout turns old/hung binaries into an explicit stale state.
function probeVersionedBinary(binary: string, requiredCapability: string): VersionedBinaryProbe {
  let cacheKey = `${binary}:unstatable`;
  try {
    const stat = statSync(binary);
    cacheKey = `${binary}:${stat.mtimeMs}:${stat.size}`;
  } catch {
    // execFileSync below supplies the fail-closed compatibility result.
  }
  const cached = versionedBinaryProbeCache.get(cacheKey);
  if (cached) return cached;

  let result: VersionedBinaryProbe = { version: "pre-versioned", capabilities: [], current: false };
  try {
    const raw = execFileSync(binary, ["version", "--json"], {
      encoding: "utf8",
      env: process.env,
      timeout: 2000,
      stdio: ["ignore", "pipe", "ignore"],
    });
    const parsed = JSON.parse(raw) as { version?: unknown; capabilities?: unknown };
    const capabilities = Array.isArray(parsed.capabilities)
      ? parsed.capabilities.filter((item): item is string => typeof item === "string")
      : [];
    result = {
      version: typeof parsed.version === "string" && parsed.version ? parsed.version : "unknown",
      capabilities,
      current: capabilities.includes(requiredCapability),
    };
  } catch {
    // Pre-versioned binaries exit 2; broken binaries may hang or emit invalid
    // JSON. All are stale, never silently treated as compatible.
  }
  versionedBinaryProbeCache.set(cacheKey, result);
  return result;
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
    console.error(`CAVE_SETUP_TIMEOUT must be a positive integer (got ${JSON.stringify(raw)})`);
    process.exit(2);
  }
  return parsed;
}

function setupPlatform(): { os: string; arch: string } {
  const os = process.platform;
  const arch = process.arch;
  if (!((os === "darwin" || os === "linux") && (arch === "arm64" || arch === "x64"))) {
    console.error(OFF_STATES.unsupportedPlatform(os, arch).line);
    process.exit(1);
  }
  return { os, arch: arch === "x64" ? "amd64" : arch };
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
    const path = join(binDir, name);
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
    console.error(OFF_STATES.downloadStalled(timeoutSeconds).line);
    console.error(`fix: ${OFF_STATES.downloadStalled(timeoutSeconds).fix}`);
  } else {
    console.error(OFF_STATES.downloadUnreachable.line);
    console.error(`fix: ${OFF_STATES.downloadUnreachable.fix}`);
  }
  process.exit(1);
}

function printInstallResult(
  installed: InstalledBinary[],
  platform: { os: string; arch: string },
  binDir: string,
  json: boolean,
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
    console.log(`${item.name}  ${platform.os}/${platform.arch}  ${item.path}  ${item.status} · checksum verified`);
  }
  console.log(`next: caveman claude`);
}

async function setupInstall(json: boolean) {
  const platform = setupPlatform();
  const timeoutSeconds = setupTimeoutSeconds();
  const binDir = join(cavemanHome(), "bin");
  mkdirSync(binDir, { recursive: true });

  const local = verifiedLocalInstall(binDir);
  if (local) {
    printInstallResult(local, platform, binDir, json);
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
    console.error("signature check failed for checksums.txt — refusing to install; partial download deleted");
    process.exit(1);
  }

  let checksums: Map<string, string>;
  try {
    checksums = parseSignedChecksums(checksumsRaw!);
  } catch {
    console.error("signature check failed for checksums.txt — refusing to install; partial download deleted");
    process.exit(1);
  }

  const installed: InstalledBinary[] = [];
  const artifactDigests: Record<string, string> = {};
  for (const name of INSTALL_BINARIES) {
    const artifact = `${name}_${platform.os}_${platform.arch}`;
    const expected = checksums.get(artifact);
    if (!expected) {
      console.error(`signature check failed for ${artifact} — refusing to install; partial download deleted`);
      process.exit(1);
    }
    const target = join(binDir, name);
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
      console.error(`signature check failed for ${artifact} — refusing to install; partial download deleted`);
      process.exit(1);
    }
    await chmod(partial, 0o755);
    await rename(partial, target);
    installProgressComplete(name, platform, result!.bytes);
    installed.push({ name, path: target, sha256: expected, status: "installed" });
  }

  const manifest: BinaryInstallManifest = { release: BINARY_RELEASE, artifacts: artifactDigests };
  await writeFile(binaryInstallManifestPath(), `${JSON.stringify(manifest, null, 2)}\n`, { mode: 0o600 });
  await chmod(binaryInstallManifestPath(), 0o600);
  printInstallResult(installed, platform, binDir, json);
}

async function setup(argv: string[] = []) {
  const json = argv.includes("--json");
  const install = argv.includes("--install");
  const unknown = argv.filter((arg) => arg !== "--json" && arg !== "--install");
  if (unknown.length > 0) commandUsage("setup [--install] [--json]");
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
type WrapOptions = { mode: WrapRuntimeMode; noProxy: boolean; toon: boolean; pixelModels?: string; pixelDensity?: string; noShrink: boolean; noMcp: boolean; noBrowse: boolean; minimal: boolean; autoRecall?: boolean; workflow?: string; command: string[] };
type CapabilitySource = "default" | "proxy-yaml" | "legacy-wrap" | "global" | "project" | "env";
type CapabilityKey =
  | "think.mode"
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
  toon: boolean;
  shrink: boolean;
  mcp: boolean;
  browse: boolean;
  autoRecall: boolean;
  proxy: boolean;
  pixelModels?: string;
  pixelDensity?: string;
  resolution: CapabilityResolution;
};
type CodexWrapAuthMode = "api-key" | "subscription";

const CAPABILITY_KEYS: CapabilityKey[] = [
  "think.mode",
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
  "execute.proxy",
];

const CAPABILITY_DEFAULTS: Record<CapabilityKey, CapabilityValue> = {
  "think.mode": "compress",
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
  if (key === "execute.mcp" && value === "auto") return "auto";
  return wrapBoolValue(value);
}

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
  apply("think.toon", process.env.CAVEMAN_TOON, "env");
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
    toon: value<boolean>("think.toon"),
    shrink: value<boolean>("think.shrink"),
    mcp: value<boolean | string>("execute.mcp") !== false,
    browse: value<boolean>("execute.browse_tool"),
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
        console.error(`not a mode: "${rawValue}" — valid: compress | record | pixel   (status calls record "observe" while you are signed out)`);
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
    noMcp: !cfg.mcp,
    noBrowse: !cfg.browse,
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

// ── Account-gated wrap: entitlement + observe-only estimate (ADR 0022) ──────────
// Local compression enables only with a valid Caveman Cloud entitlement minted at
// `caveman login`. Without one, wrap runs observe-only pass-through and reports what
// compression WOULD have saved. Enforcement ALWAYS fails open: an expired/absent/
// unreadable entitlement degrades to byte-safe pass-through — never a broken pipe.
// (docs/decisions/0022-*, docs/strategy/DEVELOPER_PLAN_TRANSITION_SPEC.md §4/§8.)

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

export type WrapGateReason =
  | "user-record"
  | "entitled"
  | "grace"
  | "observe"
  | "seat-wall"
  | "denied"
  | "unverified";
export type WrapGate = { mode: WrapRuntimeMode; estimate: boolean; reason: WrapGateReason };

const WRAP_GRACE_MS = 7 * 24 * 60 * 60 * 1000;

type OffStateID =
  | "binary-missing"
  | "foreign-process"
  | "running-mode-mismatch"
  | "invalid-mode"
  | "user-record"
  | "seat-wall"
  | "denied"
  | "unverified"
  | "observe"
  | "grace"
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
export const OFF_STATES = {
  observe: {
    line: "observe mode — compression off until you sign in (free · 1 seat · no card)",
    fix: "caveman login",
  },
  seatWall: (used: number | string, limit: number | string): OffState => ({
    id: "seat-wall",
    line: `signed in — no seat available; compression off, traffic untouched (org is using ${used} of ${limit} seats — add seats at app.caveman.so/billing)`,
    fix: "caveman cloud billing",
  }),
  denied: {
    line: "signed in — wrap entitlement denied; byte-safe pass-through only",
    fix: "caveman cloud whoami",
  },
  unverified: {
    line: "signed in — entitlement check never completed; compression off until it succeeds",
    fix: "caveman login",
  },
  grace: (date: string): OffState => ({
    id: "grace",
    line: `entitlement lapsed — byte-safe pass-through, compression resumes on refresh (grace ends ${date})`,
    fix: "caveman login",
  }),
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
    line: `no prebuilt binary for ${os}/${arch} — supported: darwin/arm64, darwin/amd64, linux/arm64, linux/amd64`,
  }),
  memMissing: {
    line: "cavemem not installed — memory and auto-recall are off",
    fix: "caveman setup --install",
  },
  mcpMissing: {
    line: "MCP recovery missing — streaming turns and Claude Pro/Max sessions pass through uncompressed (non-streaming API-key traffic still compresses)",
    fix: "caveman tools mcp install <agent>",
  },
  staleBinary: (binary: string, found: string, expected: string): OffState => ({
    id: "stale-binary",
    line: `${binary} ${found} is older than ${expected} — update before compressing`,
    fix: "caveman setup --install",
  }),
  refreshOffline: {
    line: "entitlement refresh offline — byte-safe pass-through, no traffic blocked",
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
  "seat-wall",
  "denied",
  "unverified",
  "observe",
  "grace",
  "weekly-cap",
  "mcp-missing",
  "mem-missing",
  "zdr",
  "stale-binary",
];

function fixedOffState(id: OffStateID, item: { line: string; fix?: string }): OffState {
  return { id, line: item.line, ...(item.fix ? { fix: item.fix } : {}) };
}

const pendingRunLoadoutLines: string[] = [];
const pendingRunOffStates: OffState[] = [];

function queueRunLoadout(line: string): void {
  pendingRunLoadoutLines.push(line);
}

function takeRunLoadoutLines(): string[] {
  return pendingRunLoadoutLines.splice(0);
}

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
  for (const line of takeRunLoadoutLines()) process.stderr.write(dim(`${line}\n`));
  const first = orderedOffStates(options.states)[0];
  if (first) process.stderr.write(dim(`${first.line}\n`));
  if (options.projectGroups.length > 0) {
    process.stderr.write(dim(`project overlay active — ./.caveman/config.json is setting ${options.projectGroups.join(", ")}\n`));
  }
  process.stderr.write(dim("watching…\n"));
}

// resolveWrapGate is the PURE gating decision (no IO) so it is unit-testable.
// `requestedMode` is what the user asked for; the returned `mode` is what the local
// proxy actually runs and `estimate` is whether to enable observe-only estimation.
//   - user asked for plain record (--off): stays plain record, no estimate.
//   - valid entitlement (now < expires_at): compression as requested.
//   - lapsed ≤7 days (grace): compression stays on (caller kicks a refresh + notice).
//   - no/expired-by->7d/unreadable entitlement: force record + observe estimate.
// Every no-entitlement path returns byte-safe record mode — compression is withheld,
// traffic never is.
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
  return { mode: "record", estimate: true, reason: "observe" };
}

// subscriptionCompressEnabled is the PURE decision behind the account signal the
// local proxy needs to compress subscription traffic (CAVEMAN_WRAP_ENTITLED=1).
// Subscription/OAuth coding-agent logins (Claude Pro/Max, …) are compressed only:
//   - LOCALLY — this is the local wrap only; the managed gateway's lossless+stealth
//     rule for non-PAYG traffic is unchanged and is not configured from here;
//   - with a valid (or in-grace) wrap entitlement — no account means the existing
//     byte-identical pass-through;
//   - in compress mode — record never mutates anything, and pixel is not supported
//     for subscription traffic (the proxy passes it through).
// Their savings are a TOKEN count only: a seat has no per-token price, so they can
// never mint a dollar figure. (honesty rule: no-fake-savings)
export function subscriptionCompressEnabled(gate: WrapGate | null): boolean {
  if (!gate || gate.mode !== "compress") return false;
  return gate.reason === "entitled" || gate.reason === "grace";
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

// planWeeklyAllowanceText mirrors cloud/web/lib/plan.ts PLAN_WEEKLY_ALLOWANCE — the
// parenthetical shows only for the capped tiers (free 5M / indie 50M).
function planWeeklyAllowanceText(plan: string): string | null {
  if (plan === "free") return "5M optimized tokens/week";
  if (plan === "indie") return "50M optimized tokens/week";
  return null;
}

function humanTokens(n: number): string {
  if (!Number.isFinite(n) || n <= 0) return "0";
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
// fails for seats or a down entitlement service — the worst case is observe-only.
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
      process.stderr.write(dim("  → wrap entitlement denied — running observe-only; compression is off\n"));
      return;
    case "unavailable":
      if (!readWrapEntitlement()) saveWrapEntitlementState({ kind: "unverified" });
      process.stderr.write(dim("  → wrap entitlement check unavailable — keeping any cached entitlement; compression state unchanged\n"));
      return;
  }
}

function printLoginEntitlement(e: WrapEntitlement) {
  const seatLimit = e.seats_limit == null ? "∞" : String(e.seats_limit);
  const allowance = planWeeklyAllowanceText(e.plan);
  const paren = allowance ? `${planLabel(e.plan)} — ${allowance}` : planLabel(e.plan);
  process.stderr.write(dim(`→ seat ${e.seats_used} of ${seatLimit} active  (${paren})\n`));
  process.stderr.write(dim("→ compression: entitled\n"));
  process.stderr.write(dim("→ telemetry: token counts only, never your prompts — caveman.so/data-use\n"));
  process.stderr.write(dim("compression is on for your next run — start one with `caveman claude`\n"));
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
  process.stderr.write(dim("→ running observe-only. your traffic is untouched; compression is off.\n"));
}

// refreshWrapEntitlementInBackground silently re-fetches during offline grace so a
// renewed plan lifts the notice next session. Fire-and-forget; failure stays in
// grace. On 401 it rotates through the CLI's refresh-token path, then retries once.
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

function refreshWrapEntitlementInBackground(options: { wrappedRun?: boolean } = {}) {
  const wrappedRun = options.wrappedRun === true;
  if (wrappedRun && !claimWeeklyRunRefresh()) return;
  void (async () => {
    try {
      let cfg = await config();
      if (!cfg.token) return;
      const deviceId = ensureDeviceId();
      const body = JSON.stringify({
        device_hash: deviceHashFromId(deviceId),
        device_name: hostname(),
        ...(wrappedRun ? { wrapped_run: true } : {}),
      });
      const doPost = (tok: string) =>
        fetch(`${cfg.baseURL}/api/v1/me/wrap-entitlement`, {
          method: "POST",
          headers: { authorization: `Bearer ${tok}`, "content-type": "application/json", "x-cave-csrf": "cli" },
          body,
          signal: AbortSignal.timeout(5000),
        });
      let resp = await doPost(cfg.token);
      if (resp.status === 401 && cfg.refreshToken) {
        cfg = await refreshCLIConfig(cfg);
        if (cfg.token) resp = await doPost(cfg.token);
      }
      if (!resp.ok) {
        mutateRawConfig((out) => {
          out.wrapEntitlementRefresh = { at: new Date().toISOString(), ok: false };
        });
        return;
      }
      const ent = await resp.json().catch(() => null);
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
};

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
  s: ProxyObserveSummary,
  authModes: readonly string[] = [],
  windowTruncated = false,
): string[] {
  const n = (v: unknown) => (typeof v === "number" && Number.isFinite(v) ? v : 0);
  const spans = n(s.spans);
  const tokensIn = n(s.tokens_in);
  const observe = kind === "observe";
  const emptyLine = observe
    ? "no compressible context in this session — `caveman login` (free · 1 seat · no card) turns compression on when there is"
    : "nothing compressible in this session — the layer stayed byte-safe; `caveman status` shows today's totals";
  if (spans <= 0 || tokensIn <= 0) return [emptyLine];
  const cut = observe ? n(s.would_save_tokens) : n(s.compression_tokens_saved);
  if (cut <= 0) return [emptyLine];
  const pct = Math.round((observe ? n(s.would_save_pct) || cut / tokensIn : cut / tokensIn) * 100);
  const usd = observe ? (s.would_save_usd == null ? 0 : n(s.would_save_usd)) : n(s.savings_usd);
  const tokensOnly = windowTruncated || authModes.includes("subscription") || authModes.includes("oauth");
  const before = n(s.compression_tokens_before);
  const after = n(s.compression_tokens_after);
  const lines = [`${spans} requests · ${humanTokens(tokensIn)} tokens sent`];
  let line = `compression ${observe ? "would have cut" : "cut"} ~${humanTokens(cut)} of those (${pct}%)`;
  if (before > 0 && after >= 0 && after < before) {
    line += ` · compressed parts ${humanTokens(before)} → ${humanTokens(after)}`;
  }
  if (usd > 0) {
    // Dollars only ever come from list-price-eligible rows; name that scope whenever
    // unpriceable traffic shared the same session — or whenever we cannot prove it
    // did not.
    const scope = tokensOnly ? " on the API-key traffic" : "";
    line += observe
      ? ` — about $${usd.toFixed(2)} today${scope},\nestimated locally (inferred).`
      : ` — about $${usd.toFixed(2)} today${scope}, estimated locally (inferred).`;
  } else {
    line += " — estimated locally (inferred).";
  }
  lines.push(line);
  if (tokensOnly) lines.push(SUBSCRIPTION_TOKENS_ONLY_NOTE + ".");
  if (observe) lines.push("turn it on:  caveman login   (free · 1 seat · no card)");
  return lines;
}

// printSessionSavings reads what the local proxy measured for this session and
// writes the block. A missing summary is itself unmeasurable, so it gets the same
// honest state-specific line rather than silence.
function printSessionSavings(kind: "observe" | "compress", sinceISO: string) {
  const s = readProxyObserveSummary(sinceISO);
  if (!s) {
    for (const line of formatSessionSavings(kind, {})) process.stderr.write(dim(line + "\n"));
    return;
  }
  const window = readProxySessionAuthModes(sinceISO);
  const lines = formatSessionSavings(kind, s, window.modes, window.truncated);
  for (const line of lines) process.stderr.write(dim(line + "\n"));
}

// wrap runs an agent with provider base URLs pointed at the gateway, so the
// agent's LLM traffic flows through Caveman with no code change. Local wrap is
// compression-first: it starts the standalone proxy in `compress` mode unless
// config/env or --off selects record mode. Known agents (claude, codex, …) are launchable by short id
// and get detected; any other command still runs verbatim.
async function wrap(rest: string[]) {
  pendingRunLoadoutLines.length = 0;
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
  if (codexAuthMode !== "subscription") {
    maybeInstallLoadoutHooks(agent, parsed);
    maybeInstallMcp(agent, parsed);
    maybeInstallBrowseMcp(agent, parsed);
    maybeInstallRecallHook(agent, parsed);
  }
  await runWrapped(resolved, extra, agent, parsed, codexAuthMode === "subscription");
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
  write("  (default)    full stack: S4 compression + TOON best-of + caveman & browse MCP tools + output shrink");
  write("  --off        byte-safe pass-through metering only — nothing is rewritten");
  write("  --pixel      lossy text→PNG pixel mode (model-gated); originals recoverable via caveman_retrieve");
  write("  --workflow   label this session's traffic (x-cave-workflow) so it groups by name in the dashboard");
  write(`Capability groups: think / remember / execute — inspect with \`${invokedAs()} tools config get\`.`);
  write("Auth passes through untouched: API keys AND subscription OAuth logins (Claude Pro/Max) both work.");
  write("With an account, subscription logins are compressed locally too (live zone only — compressed turns are re-sent byte-identically so the provider cache stays warm);");
  write("without one they stay byte-identical pass-through. Subscription savings are reported in tokens, never dollars.");
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

// runWrapped execs the resolved command with the gateway injection applied. It
// auto-starts the local proxy when routing to loopback, so `caveman wrap claude`
// is the one-command compression path. If the proxy can't be reached, a TTY run
// offers to launch the agent directly (no Caveman, no compression this run) rather
// than wire it to a dead endpoint; non-TTY runs warn and route through as before.
async function runWrapped(bin: string, cmdArgs: string[], agent?: AgentProfile, opts: WrapOptions = { mode: "compress", noProxy: false, toon: true, noShrink: false, noMcp: false, noBrowse: false, minimal: false, command: [] }, codexSubscription = false) {
  const result = await spawnWrapped(bin, cmdArgs, agent, opts, gatewayURL(), codexSubscription);
  if (result.summaryKind && result.sessionStart) printSessionSavings(result.summaryKind, result.sessionStart);
  if (result.proxyStarted) process.stderr.write(dim("→ Caveman proxy left running; inspect with `caveman stats`\n"));
  await syncAfterWrap();
  process.exit(result.code);
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
  let direct = false;
  // Account-gated wrap (ADR 0022): the local proxy compresses only with a valid
  // entitlement. When wrap runs the LOCAL proxy path we resolve the gate; managed
  // gateway traffic is governed by the cloud policy engine, so we never gate it.
  //
  const gateApplies = local && !codexSubscription && !opts.noProxy;
  const entitlement = gateApplies ? readWrapEntitlement() : null;
  const entitlementState = gateApplies ? readWrapEntitlementState() : null;
  let gate = gateApplies ? resolveWrapGate(entitlement, new Date(), opts.mode) : null;
  if (gate?.reason === "observe") {
    if (entitlementState?.kind === "seat-wall") gate = { ...gate, reason: "seat-wall" };
    if (entitlementState?.kind === "denied") gate = { ...gate, reason: "denied" };
    if (entitlementState?.kind === "unverified") gate = { ...gate, reason: "unverified" };
  }
  const effectiveMode = gate ? gate.mode : opts.mode;
  const observeEstimate = gate ? gate.estimate : false;
  // Entitled local compress sessions also compress subscription/OAuth logins, live
  // zone only. Never for managed traffic (gate is null there) and never for the
  // codex-subscription /chatgpt route, which stays raw pass-through.
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
  const mcpRecovery = codexSubscription ? false : wrapMcpRecoveryAvailable(agent, opts);
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
    } else if (runtime.mode !== effectiveMode) {
      if (countOtherLiveProxySessions(port, sessionMarker) > 0) {
        runtimeState = OFF_STATES.runningModeHeld(runtime.mode ?? "unknown", effectiveMode);
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
                observeEstimate ? false : mcpRecovery,
                observeEstimate ? false : opts.toon,
                opts.pixelModels,
                opts.pixelDensity,
                gw,
                "standard",
                observeEstimate,
                subscriptionCompress,
              );
              proxyReady = await portListening(host, port);
            }
            runtime = proxyReady ? readProxyRuntimeState(port, proxyVersion) : { owner: "unknown" };
            if (runtime.owner === "unknown" || runtime.mode !== effectiveMode) {
              runtimeState = runtime.owner === "unknown"
                ? OFF_STATES.foreignProcess(host, port)
                : OFF_STATES.runningModeMismatch(runtime.mode ?? "unknown", effectiveMode);
            }
          } catch {
            runtime = readProxyRuntimeState(port, proxyVersion);
            runtimeState = runtime.owner === "unknown"
              ? OFF_STATES.foreignProcess(host, port)
              : runtime.mode !== effectiveMode
                ? OFF_STATES.runningModeMismatch(runtime.mode ?? "unknown", effectiveMode)
                : null;
          }
        } else {
          runtime = readProxyRuntimeState(port, proxyVersion);
          runtimeState = runtime.owner === "unknown"
            ? OFF_STATES.foreignProcess(host, port)
            : runtime.mode !== effectiveMode
              ? OFF_STATES.runningModeMismatch(runtime.mode ?? "unknown", effectiveMode)
              : null;
        }
      }
    }
  }
  if (!proxyReady) {
    if (local && !opts.noProxy) {
      proxyStarted = codexSubscription
        ? await startWrapProxy("record", false, false, undefined, undefined, gw, "codex-subscription")
        : await startWrapProxy(effectiveMode, observeEstimate ? false : mcpRecovery, observeEstimate ? false : opts.toon, opts.pixelModels, opts.pixelDensity, gw, "standard", observeEstimate, subscriptionCompress);
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
    if (!proxyReady) {
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
  if (direct) {
    process.stderr.write(dim(`→ wrapping ${agent ? agent.display_name : bin} · direct (no Caveman this run) · using your own provider key`) + "\n");
  } else if (codexSubscription) {
    process.stderr.write("caveman: codex subscription login detected — routing via ephemeral CODEX_HOME through /chatgpt (byte-safe pass-through)\n");
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
    if (gate?.reason === "seat-wall") states.push(OFF_STATES.seatWall("?", "?"));
    if (gate?.reason === "denied") states.push(fixedOffState("denied", OFF_STATES.denied));
    if (gate?.reason === "unverified") states.push(fixedOffState("unverified", OFF_STATES.unverified));
    if (gate?.reason === "observe") states.push(fixedOffState("observe", OFF_STATES.observe));
    if (gate?.reason === "grace" && entitlement) {
      const exp = Date.parse(entitlement.expires_at);
      const until = Number.isFinite(exp) ? new Date(exp + WRAP_GRACE_MS).toISOString().slice(0, 10) : "unknown";
      states.push(OFF_STATES.grace(until));
    }
    if (!codexSubscription && wrapRecoveryEligible(opts) && agent && !mcpRecovery && !staleMcpBinary) {
      states.push(fixedOffState("mcp-missing", OFF_STATES.mcpMissing));
    }
    if (!resolveGoBin("cavemem", "CAVEMEM_BIN")) states.push(fixedOffState("mem-missing", OFF_STATES.memMissing));
    if (entitlement?.telemetry_level === "zdr") states.push(fixedOffState("zdr", OFF_STATES.zdr));

    const runningLocalMode = runtime.owner !== "unknown" && runtime.mode ? runtime.mode : null;
    const modeText = local
      ? runningLocalMode === "record" && ["observe", "seat-wall", "denied", "unverified"].includes(String(gate?.reason))
        ? "observe"
        : runningLocalMode
      : "managed";
    printRunBanner({
      agent,
      binary: bin,
      runningMode: modeText,
      states,
      projectGroups: resolution.projectActive ? capabilityProjectGroups() : [],
    });
    // One honest line about what this account unlocks and what it can never claim —
    // but only when the proxy will actually take that path. The entitlement is half
    // the gate; without MCP recovery the proxy stays byte-identical pass-through, so
    // an entitled-but-unrecoverable session says so instead. (honesty rule: no-placeholder)
    if (subscriptionCompress && mcpRecovery) {
      process.stderr.write(dim("→ subscription logins (Claude Pro/Max) compress locally too — live zone only; compressed turns are re-sent byte-identically so the provider cache stays warm\n"));
      process.stderr.write(dim(`→ ${SUBSCRIPTION_TOKENS_ONLY_NOTE}\n`));
    } else if (subscriptionCompress) {
      process.stderr.write(dim(`→ ${SUBSCRIPTION_NO_RECOVERY_NOTE}\n`));
    }
  }
  // Whether this session will produce a session-end savings line, and from when.
  // Only local proxy sessions that actually route through us can be summarized.
  let summaryKind: "observe" | "compress" | undefined;
  if (!direct && local && !codexSubscription && !opts.noProxy) {
    if (runtime.owner !== "unknown" && runtime.mode === "record" && observeEstimate) summaryKind = "observe";
    if (runtime.owner !== "unknown" && (runtime.mode === "compress" || runtime.mode === "pixel")) summaryKind = "compress";
  }
  const sessionStart = summaryKind ? new Date().toISOString() : undefined;
  // Direct mode: inherit the shell env with NO profile injection, stripping only
  // our own routing if it leaked in — so the agent talks straight to the provider
  // with its own key. Any unrelated base URL the user set themselves stays put.
  const env = direct ? { ...process.env } : codexSubscription ? buildCodexSubscriptionWrapEnv(gw) : buildWrapEnv(agent, gw);
  if (!direct) maybeWarnHermesMissingKey(agent, gw);
  if (direct) {
    for (const k of WRAP_BASE_URL_ENV_VARS) {
      if (env[k] === gw) delete env[k];
    }
  }
  emitCommandRunOnce("ok");
  let code: number;
  try {
    code = await new Promise<number>((resolve, reject) => {
      const child = spawn(bin, cmdArgs, { stdio: "inherit", env });
      child.on("error", (error) => reject(new Error(`failed to exec ${bin}: ${error.message}`)));
      child.on("exit", (code) => resolve(code ?? 0));
    });
  } finally {
    cleanupWrapTempDirs();
    removeProxySessionMarker(sessionMarker);
  }
  return { code, proxyStarted, sessionStart, summaryKind };
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

async function startWrapProxy(mode: WrapRuntimeMode, mcpRecovery: boolean, toon: boolean, pixelModels: string | undefined, pixelDensity: string | undefined, gw = gatewayURL(), purpose: "standard" | "codex-subscription" = "standard", observeEstimate = false, subscriptionCompress = false): Promise<boolean> {
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
  const env = {
    ...process.env,
    CAVEMAN_PROXY_OWNER: purpose === "standard" ? "wrap" : "start",
    CAVEMAN_MODE: mode,
    CAVEMAN_LISTEN: `${host}:${port}`,
    // The recovery half of the proxy's subscription gate, and the switch that lets
    // it compress streams at all. Stamped EXPLICITLY in both directions like the
    // entitlement below: wrap derives it from the agent's OWN MCP install (and
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
    // ever mutating the forwarded request (the account-gated, no-entitlement path).
    ...(observeEstimate ? { CAVEMAN_OBSERVE_ESTIMATE: "1" } : {}),
    // The account signal that unlocks live-zone compression for subscription/OAuth
    // coding-agent traffic (Claude Pro/Max …) in the proxy. Always set EXPLICITLY in
    // both directions so an inherited CAVEMAN_WRAP_ENTITLED can never grant the
    // capability to an unentitled wrap — no entitlement keeps subscription traffic
    // byte-identical. The operator off-switch (`subscription_compress: off`) stays
    // the operator's: we never override it from here.
    CAVEMAN_WRAP_ENTITLED: subscriptionCompress ? "1" : "0",
  };
  const child = spawn(resolved, [], { stdio: "ignore", env, detached: true });
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

function codexCavemanProviderToml(gw: string): string {
  return [
    `model_provider = "caveman"`,
    `[model_providers.caveman]`,
    `name = "Caveman"`,
    `base_url = ${JSON.stringify(appendUrlPath(gw, "/chatgpt"))}`,
    `wire_api = "responses"`,
    `requires_openai_auth = true`,
  ].join("\n");
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

function buildCodexSubscriptionHome(gw: string): string {
  const sourceHome = codexHomeDir();
  const outDir = mkdtempSync(join(tmpdir(), "caveman-wrap-"));
  wrapTempDirs.add(outDir);
  writeFileSync(join(outDir, "auth.json"), readFileSync(codexAuthPath()), { mode: 0o600 });

  let sourceConfig = "";
  try {
    sourceConfig = readFileSync(join(sourceHome, "config.toml"), "utf8");
  } catch {
    sourceConfig = "";
  }
  const stripped = stripCodexCavemanProviderToml(sourceConfig);
  const provider = codexCavemanProviderToml(gw);
  writeFileSync(join(outDir, "config.toml"), `${stripped ? `${stripped}\n\n` : ""}${provider}\n`, { mode: 0o600 });

  linkCodexReadOnly(sourceHome, outDir, "skills");
  linkCodexReadOnly(sourceHome, outDir, "AGENTS.md");
  return outDir;
}

function buildCodexSubscriptionWrapEnv(gw: string): NodeJS.ProcessEnv {
  const env: NodeJS.ProcessEnv = { ...process.env };
  for (const key of WRAP_BASE_URL_ENV_VARS) delete env[key];
  env.CODEX_HOME = buildCodexSubscriptionHome(gw);
  return env;
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
  const ref = resolveOpenClawPrimaryRef(baseConfig);
  if (!ref) {
    process.stderr.write("caveman: openclaw primary model not found; leaving model routing unchanged (generic env fallback still applies)\n");
    return overlay;
  }
  const provider = resolveOpenClawProvider(baseConfig, ref.provider);
  if (!provider) {
    process.stderr.write(`caveman: openclaw provider "${ref.provider}" not found; leaving primary model unchanged\n`);
    return overlay;
  }
  const configuredProvider = openClawProviderConfigured(baseConfig, ref.provider);
  if (openClawProviderUsesOAuth(ref.provider, provider, configuredProvider)) {
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

function applyConfigFileInjection(env: NodeJS.ProcessEnv, agent: AgentProfile, inj: ConfigFileInjection, gw: string, modeGw = gw) {
  const baseConfig = readBaseConfig(inj);
  const mode = wrapMode(modeGw);
  const staticOverlay = mode === "managed" && inj.config_overlay.managed !== undefined ? inj.config_overlay.managed : inj.config_overlay.local;
  const builder = overlayBuilders[agent.id];
	const overlay = renderDeep(builder ? builder(agent, baseConfig, { mode, gatewayUrl: gw, env }) : staticOverlay, gw);
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

const WRAP_BASE_URL_ENV_VARS = ["ANTHROPIC_BASE_URL", "OPENAI_BASE_URL", "OPENAI_API_BASE", "GEMINI_BASE_URL", "GOOGLE_GEMINI_BASE_URL"] as const;

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
export function buildWrapEnv(agent?: AgentProfile, gw = gatewayURL()): NodeJS.ProcessEnv {
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
      applyConfigFileInjection(env, agent, inj, renderedGw, gw);
    } catch (e) {
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
  maybeInstallLoadoutHooks(picked.agent, pickedOpts);
  maybeInstallMcp(picked.agent, pickedOpts);
  maybeInstallBrowseMcp(picked.agent, pickedOpts);
  maybeInstallRecallHook(picked.agent, pickedOpts);
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
// login, `caveman wrap`/`caveman start` route through the cloud with no env var
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

// 0600 credentials file) — never in plaintext config. organization_id is bound
// from the returned token, never from any local input.
async function login(argv: string[] = []) {
  const baseURL = flagFrom(argv, "--base-url", process.env.CAVE_API_URL ?? "http://localhost:8080");

  const codeResp = await fetch(`${baseURL}/api/v1/auth/device/code`, {
    method: "POST",
    headers: { "content-type": "application/json" },
    body: "{}"
  });
  const code = await codeResp.json();
  if (!code.device_code) throw new Error(`device authorization failed: ${JSON.stringify(code)}`);

  console.error(`\n  Authorize this device in your browser:`);
  console.error(`    ${code.verification_uri_complete ?? code.verification_uri}`);
  console.error(`    code: ${code.user_code}\n`);

  const intervalMs = Math.max(0, Number(code.interval ?? 5)) * 1000;
  const deadline = Date.now() + Number(code.expires_in ?? 600) * 1000;
  while (Date.now() < deadline) {
    const tokResp = await fetch(`${baseURL}/api/v1/auth/device/token`, {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify({ device_code: code.device_code })
    });
    const tok = await tokResp.json();
    if (tok.access_token) {
	  const credentials: StoredCredentials = {
	    access_token: String(tok.access_token),
	    ...(typeof tok.refresh_token === "string" && tok.refresh_token ? { refresh_token: tok.refresh_token } : {}),
	    ...(typeof tok.gateway_api_key === "string" && tok.gateway_api_key ? { gateway_api_key: tok.gateway_api_key } : {}),
	    ...(typeof tok.gateway_key_id === "string" && tok.gateway_key_id ? { gateway_key_id: tok.gateway_key_id } : {}),
	    ...(typeof tok.project_id === "string" && tok.project_id ? { project_id: tok.project_id } : {}),
	  };
	  const tokenStore = storeCredentials(credentials);
      const organizationId = orgFromToken(tok.access_token);
      const gateway = resolveLoginGatewayUrl(baseURL, tok, code, argv);
      const saved: Config = { baseURL, token: "", tokenStore };
      if (organizationId) saved.organizationId = organizationId;
	  if (credentials.project_id) saved.projectId = credentials.project_id;
      if (gateway) saved.gatewayUrl = gateway;
      await saveConfig(saved);
      // Mint/refresh the local-wrap entitlement for this device (ADR 0022). Best
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
    if (tok.error && tok.error !== "authorization_pending" && tok.error !== "slow_down") {
      throw new Error(`device login failed: ${tok.error}`);
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
  | { kind: "synced"; spans: number; tokensSaved: number; tokenCountBasis: string; savingsUSD: number; dashboard: string; firstSync: boolean };

function localSpendDbPath(): string {
  return process.env.CAVEMAN_DB ?? join(caveHome(), "caveman.db");
}

function syncStatePath(): string {
  return join(dirname(configPath()), "sync.json");
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

// syncRequestSpan converts one local `requests` row into a caveman-jsonl span
// line (public/shared/platform/importers Span shape; timestamps are already in
// the ClickHouse layout because the proxy writes them that way). The basis and
// per-row inferred savings ride in attributes — the spans schema has no savings
// column, and imported rows must never look like verified ledger entries.
function syncRequestSpan(r: Record<string, unknown>): string {
  const num = (v: unknown) => (typeof v === "bigint" ? Number(v) : typeof v === "number" && Number.isFinite(v) ? v : 0);
  const str = (v: unknown) => (typeof v === "string" ? v : "");
  const attributes: Record<string, string> = {
    "cave.basis": str(r.basis) || "inferred",
    "cave.savings_usd": String(num(r.savings_usd)),
    "cave.sync_source": "caveman-cli",
  };
  if (str(r.token_usage_basis)) attributes["cave.token_usage_basis"] = str(r.token_usage_basis);
  if (str(r.auth_mode)) attributes["cave.auth_mode"] = str(r.auth_mode);
  if (str(r.runtime_mode)) attributes["cave.runtime_mode"] = str(r.runtime_mode);
  if (str(r.optimization_ids)) attributes["cave.optimization_ids"] = str(r.optimization_ids);
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
                input_tokens, output_tokens, cached_input_tokens, total_cost_usd,
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

  const body = rows.map(syncRequestSpan).join("\n") + "\n";
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
  const savingsUSD = rows.reduce((sum, r) => sum + num(r.savings_usd), 0);
  return { kind: "synced", spans: rows.length, tokensSaved, tokenCountBasis, savingsUSD, dashboard: deriveDashboardUrl(cfg.baseURL), firstSync };
}

// sync is the first-class verb: clear error when logged out, honest no-op when
// there is nothing to send, one plain line when spans were uploaded.
async function sync() {
  const cfg = await config();
  requireAuth(cfg);
  const out = await syncLocalSavings(cfg);
  if (out.kind === "no_store") {
    console.log(`nothing to sync — no local spend store at ${out.dbPath} (run \`caveman wrap <agent>\` to record local inferred savings first)`);
    return;
  }
  if (out.kind === "empty") {
    console.log("nothing new to sync — local inferred savings are already up to date");
    return;
  }
  if (out.firstSync) console.log(SYNC_DISCLOSURE);
  console.log(`synced ${out.spans} spans · ${out.tokensSaved} estimated tokens saved (inferred; counter basis ${out.tokenCountBasis})${out.dashboard ? ` → ${out.dashboard}` : ""}`);
}

// syncAfterLogin runs the same sync once right after a successful login so the
// dashboard immediately shows what the local proxy already measured. Strictly
// best-effort: a sync failure never fails the login.
async function syncAfterLogin() {
  try {
    const cfg = await config();
    if (!cfg.token) return;
    const out = await syncLocalSavings(cfg);
    if (out.kind === "synced") {
      console.error(`  ${mark("ok")} synced ${out.spans} local spans · ${out.tokensSaved} estimated tokens saved (inferred; counter basis ${out.tokenCountBasis})${out.dashboard ? ` → ${out.dashboard}` : ""}`);
    } else {
      console.error(dim("  no local spans to sync yet — `caveman wrap <agent>` records inferred savings locally; `caveman sync` uploads them"));
    }
  } catch (error) {
    console.error(`  ${mark("warn")} local savings sync skipped: ${(error as Error).message} — run \`caveman sync\` to retry`);
  }
}

// syncAfterWrap is the end-of-session hook: after a logged-in LOCAL wrapped
// session, push the newly recorded spans. Quiet best-effort — silent when not
// logged in or nothing to send, and never changes the wrapped exit code.
// Managed-mode sessions skip it: that traffic is already measured in the cloud.
async function syncAfterWrap() {
  try {
    const cfg = await config();
    if (!cfg.token) return;
    if (wrapMode(gatewayURL()) !== "local") return;
    const out = await syncLocalSavings(cfg);
    if (out.kind === "synced") {
      process.stderr.write(dim(`→ synced ${out.spans} local spans · ${out.tokensSaved} estimated tokens saved (inferred; counter basis ${out.tokenCountBasis})${out.dashboard ? ` → ${out.dashboard}` : ""}`) + "\n");
    }
  } catch (error) {
    process.stderr.write(dim(`→ local savings sync failed (${(error as Error).message}) — run \`caveman sync\` to retry`) + "\n");
  }
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
  const local = join(cavemanHome(), "bin", name);
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

function wrapMcpRecoveryAvailable(agent: AgentProfile | undefined, opts: WrapOptions): boolean {
  if (!wrapRecoveryEligible(opts) || !agent) return false;
  if (!mcpInstalled(agent.id) && !configFileOverlayHasCavemanMcp(agent)) return false;
  const compatibility = probeMcpBinary();
  if (!compatibility) return false;
  if (!compatibility.probe.current) {
    queueStaleMcpBinary(compatibility.probe);
    return false;
  }
  return true;
}

// startMcpRecoveryAvailable is `caveman start`'s version of the same question.
// start launches a bare proxy with no agent context, so it cannot narrow to one
// profile; it asks the machine instead: has `caveman mcp install <agent>` ever
// written a marker? Config-file overlays are deliberately NOT counted — those are
// injected by `caveman wrap` at launch, so they say nothing about an agent that
// points at this proxy on its own. An explicit CAVEMAN_RECOVERY=mcp is the
// operator's own opt-in and is honored. No evidence → no signal, so the proxy
// stays byte-identical pass-through (fail closed: an elided marker the agent
// cannot retrieve is unrecoverable).
function startMcpRecoveryAvailable(): boolean {
  if (process.env.CAVEMAN_RECOVERY === "mcp") return true;
  if (!AGENTS.some((agent) => mcpInstalled(agent.id))) return false;
  return probeMcpBinary()?.probe.current ?? false;
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

// maybeInstallMcp is the loadout step that lets wrapped agents recover proxy
// transforms through the caveman_retrieve MCP tool. Compress streams need it to
// transform safely; pixel mode uses it as an extra recovery disclosure path.
// Opt out with wrap.mcp=false. It only
// wires a server it can resolve to a real executable — never the npx fallback —
// so it cannot register a dead MCP server into the agent's config.
function maybeInstallMcp(agent: AgentProfile | undefined, opts: WrapOptions) {
  if (!agent || opts.minimal || opts.noMcp || !wrapRecoveryEligible(opts)) return;
  const compatibility = probeMcpBinary();
  if (!compatibility) return; // no real server binary — wrap prints the mcp-install tip instead
  if (!compatibility.probe.current) {
    queueStaleMcpBinary(compatibility.probe);
    return;
  }
  if (configFileOverlayHasCavemanMcp(agent)) return;
  if (mcpInstalled(agent.id)) return;
  const mcp = { command: compatibility.binary, args: [] };
  if (installMcpForAgent(agent, mcp, "caveman")) {
    writeMcpMarker(agent.id, mcp);
    queueRunLoadout(`→ installed streaming-compression recovery into ${agent.display_name}   ·  undo: caveman tools mcp uninstall ${agent.id}`);
  }
}

function maybeInstallBrowseMcp(agent: AgentProfile | undefined, opts: WrapOptions) {
  if (!agent || opts.noBrowse) return;
  if (configFileOverlayHasMcp(agent, "caveman-browse")) return;
  if (mcpServerInstalled(agent.id, "caveman-browse")) return;
  const resolved = resolveGoBin("caveman-browse", "CAVEMAN_BROWSE_BIN");
  if (!resolved) return;
  const mcp = { command: resolved, args: [] };
  if (installMcpForAgent(agent, mcp, "caveman-browse")) {
    writeMcpServerMarker(agent.id, "caveman-browse", mcp, "caveman_browse");
    queueRunLoadout(`→ installed compressed browsing tools into ${agent.display_name}   ·  undo: caveman tools mcp uninstall ${agent.id} --server caveman-browse`);
  }
}

function mcpUsage(): never {
  const prefix = currentInvocation.group ? `${invokedAs()} ${currentInvocation.group} mcp` : `${invokedAs()} mcp`;
  console.error(`usage: ${prefix} install|uninstall [agent] [--server caveman|caveman-browse]`);
  console.error("  install the caveman_retrieve MCP tool for an agent (claude, codex, opencode, gemini, hermes, openclaw)");
  console.error("  so `caveman wrap` can compress streams and `--pixel` can disclose recovery via MCP.");
  console.error("  with no agent, installs for every known agent detected on PATH.");
  console.error("  uninstall removes the tool registration and the marker again.");
  process.exit(2);
}

// mcpUninstall reverses mcpInstall: de-register the caveman MCP server from the
// agent's config and drop the marker, so wrap stops signaling MCP recovery.
function mcpUninstall(target?: string, serverName = "caveman") {
  if (serverName !== "caveman" && serverName !== "caveman-browse") {
    console.error(`unknown MCP server '${serverName}'. valid: caveman, caveman-browse`);
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
        execFileSync(claude, ["mcp", "remove", serverName], { stdio: "ignore" });
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
  const cleaned = existing.replace(new RegExp(`\\n?\\[mcp_servers\\.${escapeRegExp(serverName)}\\][^[]*`), "\n");
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

function mcpInstall(target?: string) {
  const mcp = resolveMcpCommand();
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
    if (installMcpForAgent(a, mcp, "caveman")) {
      writeMcpMarker(a.id, mcp);
      installed++;
      process.stderr.write(`${mark("ok")} ${a.display_name}: caveman_retrieve MCP tool installed\n`);
    }
  }
  if (installed > 0) {
    process.stderr.write(dim("→ `caveman wrap` will now compress streaming requests too (recovered via MCP)\n"));
  }
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

function installMcpOpenClaw(mcp: { command: string; args: string[] }, serverName = "caveman"): boolean {
  const openclaw = openClawCli();
  if (!openclaw) {
    console.error(`${mark("warn")} openclaw CLI not found on PATH; install OpenClaw first`);
    return false;
  }
  const addArgs = ["mcp", "add", serverName, "--command", mcp.command, "--no-probe"];
  for (const arg of mcp.args) addArgs.push("--arg", arg);
  try {
    execFileSync(openclaw, ["mcp", "unset", serverName], { stdio: "ignore" });
  } catch {
    // Missing server or older config state is fine; add below is authoritative.
  }
  try {
    execFileSync(openclaw, addArgs, { stdio: "ignore" });
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
    execFileSync(openclaw, ["mcp", "unset", serverName], { stdio: "ignore" });
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
    execFileSync(claude, ["mcp", "remove", serverName], { stdio: "ignore" });
  } catch {
    // not previously installed — fine.
  }
  try {
    execFileSync(claude, ["mcp", "add", serverName, "--", mcp.command, ...mcp.args], { stdio: "ignore" });
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
  if (existing.includes(header)) return true; // idempotent
  const argsLine = mcp.args.length ? `\nargs = [${mcp.args.map((s) => JSON.stringify(s)).join(", ")}]` : "";
  const head = existing.trim() ? existing.replace(/\s*$/, "") + "\n" : "";
  const block = `${head}\n${header}\ncommand = ${JSON.stringify(mcp.command)}${argsLine}\n`;
  try {
    mkdirSync(dirname(path), { recursive: true });
    writeFileSync(path, block);
    return true;
  } catch (e) {
    console.error(`${mark("warn")} cannot write ${path}: ${(e as Error).message}`);
    return false;
  }
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
      child = spawn(cmd[0]!, cmd.slice(1), { stdio: ["inherit", "pipe", "pipe"] });
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
  return which("caveman") ?? which("cave") ?? `${process.execPath} ${process.argv[1]}`;
}

// shrinkHook is the settings-hook callback for the agents whose harness can
// deterministically rewrite a shell command before it runs: Claude Code (PreToolUse,
// tool "Bash"), the opencode plugin (which feeds the same "Bash" shape), and Gemini
// CLI (BeforeTool, tool "run_shell_command"). It reads the tool event on stdin and,
// for a noisy command, rewrites it to run through `caveman shrink`. Anything it won't
// safely shrink it passes through: exit 0 with NO stdout = "no rewrite, run as-is".
async function shrinkHook() {
  let raw: Buffer;
  try { raw = await readStdin(); } catch { process.exit(0); }
  let evt: { tool_name?: string; tool_input?: { command?: string } };
  try { evt = JSON.parse(raw.toString("utf8") || "{}"); } catch { process.exit(0); }
  const tool = evt?.tool_name;
  const isGemini = tool === "run_shell_command"; // Gemini CLI's shell tool
  const isBash = tool === "Bash";                // Claude Code + the opencode plugin
  if (!isGemini && !isBash) process.exit(0);
  const command = evt.tool_input?.command;
  if (typeof command !== "string" || !shouldShrink(command)) process.exit(0);
  const rewritten = `${cavemanBinForHook()} shrink -- ${command.trim()}`;
  // Each harness has a different (silent-on-mismatch) override contract: Gemini merges
  // hookSpecificOutput.tool_input (snake_case, no event discriminator); Claude replaces
  // via hookSpecificOutput.updatedInput (camelCase + hookEventName). Emit the right one.
  const out = isGemini
    ? { hookSpecificOutput: { tool_input: { command: rewritten } } }
    : { hookSpecificOutput: { hookEventName: "PreToolUse", updatedInput: { command: rewritten } } };
  process.stdout.write(JSON.stringify(out));
}

function claudeSettingsPath(): string {
  return join(homedir(), ".claude", "settings.json");
}
function geminiSettingsPath(): string {
  return join(homedir(), ".gemini", "settings.json");
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
// (hard methods: claude-pretooluse, opencode-plugin, gemini-beforetool, hermes-plugin, openclaw-plugin.)

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

// maybeInstallRecallHook is the wrap.auto_recall glue: install the opt-in recall
// hook once, only when the user asked for it (never automatic).
function maybeInstallRecallHook(agent: AgentProfile | undefined, opts: WrapOptions) {
  if (!agent || !opts.autoRecall) return;
  if (memoryHookKind(agent) === "none") {
    process.stderr.write(dim(`→ ${agent.display_name} has no auto-recall surface; use the editing skill + caveman mem recall instead\n`));
    return;
  }
  if (recallHookInstalled(agent.id)) return;
  if (installRecallHookForAgent(agent)) {
    writeRecallHookMarker(agent.id);
    queueRunLoadout(`→ installed auto-recall into ${agent.display_name}   ·  undo: caveman tools mem hook uninstall ${agent.id}`);
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
// For agents with no deterministic command-rewrite surface (e.g. Codex — its
// PreToolUse runtime rejects updatedInput, openai/codex#18491), we append a
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
// installShrinkNote appends the note to the agent's instructions file as a trailing,
// delimited block. Idempotent (a second install with a complete block is a no-op),
// creates the file/dir if missing, and never disturbs the user's existing content. A
// corrupted half-block (NOTE_BEGIN with no NOTE_END) is repaired, not duplicated.
function installShrinkNote(file: string): boolean {
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
  if (existing.includes(NOTE_BEGIN) && existing.includes(NOTE_END)) return true; // complete block present
  // Repair a corrupted half-block (NOTE_BEGIN without END): drop from it to EOF so we
  // re-append exactly one clean block rather than nesting.
  const beginAt = existing.indexOf(NOTE_BEGIN);
  if (beginAt !== -1) existing = existing.slice(0, beginAt).replace(/\n+$/, "\n");
  const sep = existing === "" ? "" : existing.endsWith("\n") ? "\n" : "\n\n";
  try {
    mkdirSync(dirname(path), { recursive: true });
    writeFileSync(path, existing + sep + shrinkNoteBlock() + "\n");
    return true;
  } catch (e) {
    console.error(`${mark("warn")} cannot write ${path}: ${(e as Error).message}`);
    return false;
  }
}
// removeShrinkNote strips only our trailing block (and the single blank-line separator
// install inserted before it), restoring the user's content — and its original trailing
// newline — exactly. Only the seam is touched; user content above is never reflowed.
// Tolerates a corrupted half-block (missing NOTE_END) by stripping to end-of-file.
function removeShrinkNote(file: string): boolean {
  const path = expandTilde(file);
  let text: string;
  try {
    text = readFileSync(path, "utf8");
  } catch {
    return false; // missing/unreadable → nothing to remove
  }
  const start = text.indexOf(NOTE_BEGIN);
  if (start === -1) return false;
  const endMarker = text.indexOf(NOTE_END, start);
  const end = endMarker === -1 ? text.length : endMarker + NOTE_END.length;
  const before = text.slice(0, start).replace(/\n\n$/, "\n"); // undo the blank-line separator
  const after = text.slice(end).replace(/^\n/, "");           // drop the block's own trailing newline
  try {
    writeFileSync(path, before + after);
    return true;
  } catch {
    return false;
  }
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
function cavemanInvocation(): { cmd: string; pre: string[] } {
  const onPath = which("caveman") ?? which("cave");
  if (onPath) return { cmd: onPath, pre: [] };
  return { cmd: process.execPath, pre: [process.argv[1] ?? ""] };
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

function hermesPluginEnabled(text: string): boolean {
  const escaped = escapeRegExp(HERMES_PLUGIN_NAME);
  return new RegExp(`(^|\\n)\\s*-\\s*["']?${escaped}["']?\\s*(?:#.*)?(?=\\n|$)`).test(text)
    || new RegExp(`enabled:\\s*\\[[^\\]]*["']?${escaped}["']?`).test(text);
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

function hooksUsage(): never {
  console.error(`usage: ${invokedCommand("hooks")} install|uninstall [agent]`);
  console.error("  install a command-output compression hook so an agent's shell output is");
  console.error("  auto-shrunk before the model reads it (byte-safe; recover with `caveman retrieve`).");
  console.error("  hard rewrite: claude, opencode, gemini, hermes, openclaw · soft model-nudge: codex · manual: aider.");
  console.error("  with no agent, installs for every hookable agent found on PATH.");
  process.exit(2);
}

function hooksCmd(rest: string[]) {
  const sub = rest[0];
  if (sub !== "install" && sub !== "uninstall") hooksUsage();
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

// maybeInstallLoadoutHooks is the loadout glue: the first time `caveman wrap`
// launches a supporting agent in compress mode, auto-install the command-output
// hook (RTK-parity) so "one command loads everything" is literally true. Opt out
// with wrap.shrink=false or record/pixel mode. Idempotent via the marker; discloses
// the one global side effect and how to undo it.
function maybeInstallLoadoutHooks(agent: AgentProfile | undefined, opts: WrapOptions) {
  if (!agent || opts.minimal || opts.noShrink || !wrapCompressEnabled(opts)) return;
  if (agent.command_hook?.method === "openclaw-plugin") return; // wrap uses the temp config overlay; persistent config only changes via `caveman hooks install openclaw`.
  const kind = commandHookKind(agent);
  if (kind === "none") return; // no surface (e.g. aider) — stay quiet in the loadout
  if (shrinkHookInstalled(agent.id)) return;
  if (installShrinkHookForAgent(agent)) {
    writeShrinkHookMarker(agent.id);
    const how = kind === "hard" ? "command-output compression" : "a shrink preference (model nudge)";
    queueRunLoadout(`→ installed ${how} into ${agent.display_name}   ·  undo: caveman tools hooks uninstall ${agent.id}`);
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
function evalsRun() {
  const bin = cavemanBin("caveman-engine", "CAVEMAN_ENGINE_BIN");
  try {
    const out = execFileSync(bin, ["evals", "run"], { encoding: "utf8" });
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
function stats() {
  const bin = cavemanBin("caveman-proxy", "CAVEMAN_PROXY_BIN");
  try {
    const out = execFileSync(bin, ["stats"], { encoding: "utf8" });
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
    const result = await spawnWrapped(resolvedAgent, extra, agent, { mode: "record", noProxy: true, toon: false, noShrink: false, noMcp: false, noBrowse: false, minimal: false, command }, trialURL);
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
};

type LearnDiff = { days: number; gone: number; back: number; fresh: number };

const LEARN_EMPTY =
  "no Claude Code or Codex sessions found in the last 30d — the plan needs a block repeated across ≥3 sessions; run `caveman claude` a few times, then `caveman learn`";
const LEARN_NEXT =
  "next:  caveman tools skills install caveman-learn   (review + apply, with consent)  ·  preview one: caveman learn apply <sink_id> --dry-run";

function learnReportPath(): string {
  return join(cavemanHome(), "reports", "caveman-learn.html");
}

function renderLearnRows(plan: LearnPlan, markdown: boolean): string[] {
  const lines: string[] = [];
  for (const [index, sink] of plan.sinks.entries()) {
    const lead = markdown ? `${index + 1}. **${sink.title}**` : `${index + 1}. ${sink.title}`;
    lines.push(`${lead}  ·  ${sink.sink_id}  ·  ${sink.class}`);
    lines.push(`   ~${humanTokens(sink.tokens_per_turn)} tokens/turn · ~${humanTokens(sink.tokens_per_day_rate)} tokens/day · basis: inferred`);
    if (sink.suggestion) lines.push(`   ${sink.suggestion}`);
  }
  return lines;
}

export function renderLearnPlan(plan: LearnPlan, options: { markdown?: boolean; report?: string; diff?: LearnDiff } = {}): string {
  const markdown = options.markdown === true;
  const report = options.report ?? learnReportPath();
  const sessions = Math.max(0, Number(plan.sessions_scanned ?? 0));
  const recurring = plan.sinks.some((sink) => sink.class === "recurring_context");
  const lines: string[] = [];

  if (sessions === 0) {
    lines.push(LEARN_EMPTY);
  } else if (!recurring) {
    if (plan.sinks.length > 0) lines.push(...renderLearnRows(plan, markdown), "");
    lines.push(`${sessions} sessions scanned · no block repeated across ≥3 sessions yet — keep running \`caveman claude\`, then re-run \`caveman learn\``);
  } else {
    lines.push(markdown
      ? `## Setup Score ${plan.cave_score.score} — basis: inferred (local sessions, not billed spend)`
      : `Setup Score ${plan.cave_score.score}  ·  basis: inferred (local sessions, not billed spend)`);
    lines.push("scores your local agent setup — the console's Cave Score (org) scores org traffic;");
    lines.push("the two are different scales and will not match");
    const by = plan.sessions_by_source ?? {};
    const sourceBits = [
      by.claude ? `claude-code ${by.claude}` : "",
      by.codex ? `codex ${by.codex}` : "",
    ].filter(Boolean);
    lines.push(`${sessions} sessions scanned${sourceBits.length ? ` · ${sourceBits.join(" · ")}` : ""}`);
    if (options.diff) {
      const d = options.diff;
      const bits = [
        d.gone ? `${d.gone} move${d.gone === 1 ? "" : "s"} gone` : "",
        d.back ? `${d.back} back` : "",
        d.fresh ? `${d.fresh} new` : "",
      ].filter(Boolean);
      if (bits.length) lines.push(`since your last run ${d.days}d ago: ${bits.join(" · ")}`);
    }
    lines.push("", ...renderLearnRows(plan, markdown), "", LEARN_NEXT);
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
      stdio: ["ignore", "pipe", progress ? "inherit" : "pipe"],
    });
  } catch (error) {
    const e = error as { stdout?: string; stderr?: string; status?: number; code?: string; killed?: boolean; signal?: string; message?: string };
    if (e.code === "ETIMEDOUT" || e.killed || e.signal === "SIGTERM") {
      console.error(`learn scan timed out after ${seconds}s — no score computed; re-run with \`caveman learn --json\` to capture the raw scan`);
      process.exit(1);
    }
    if (e.stderr) process.stderr.write(e.stderr);
    if (!e.stderr) console.error(e.message ?? "caveman-proxy learn failed");
    process.exit(e.status ?? 1);
  }
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

// learn is the porcelain setup profiler. Machine modes stay clean; terminal
// mode renders one of three history-graded states.
function learn(rest: string[]) {
  const sub = rest[0];
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
  const forwarded = rest.filter((arg) => arg !== "--json" && arg !== "--md");
  proxyExecLearn(["learn", "scan", ...forwarded], !json && !markdown);
  const planRaw = proxyExecLearn(["learn", "report", "--json", ...forwarded], false);
  if (json) {
    process.stdout.write(planRaw);
    return;
  }
  const plan = JSON.parse(planRaw) as LearnPlan;
  const diff = readLearnDiff(plan);
  process.stdout.write(renderLearnPlan(plan, {
    markdown,
    report: learnReportPath(),
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
      const text = rest.slice(1).filter((a) => !a.startsWith("--")).join(" ");
      if (!text) { console.error(`usage: ${invokedCommand("mem")} remember <text>`); process.exit(2); }
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
  const baseURL = flagFrom(argv, "--base-url", "http://localhost:8080");
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
};

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
      lines.push(statusRow("seat", "signed in · no active wrap entitlement   ·  sign out: caveman logout"));
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
  const entitlementState = readWrapEntitlementState();
  const rawMeta = globalCapabilityDocument() as Partial<Config>;
  const signedIn = !!resolveCredentials(rawMeta).access_token;
  let gate = resolveWrapGate(entitlement, new Date(), requested);
  if (gate.reason === "observe" && signedIn) {
    if (entitlementState?.kind === "seat-wall") gate = { ...gate, reason: "seat-wall" };
    else if (entitlementState?.kind === "denied") gate = { ...gate, reason: "denied" };
    else if (entitlementState?.kind === "unverified") gate = { ...gate, reason: "unverified" };
  }

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
  if (gate.reason === "seat-wall") {
    states.push(OFF_STATES.seatWall(entitlementState?.seats_used ?? "?", entitlementState?.seats_limit ?? "?"));
  }
  if (gate.reason === "denied") states.push(fixedOffState("denied", OFF_STATES.denied));
  if (gate.reason === "unverified") states.push(fixedOffState("unverified", OFF_STATES.unverified));
  if (gate.reason === "observe") states.push(fixedOffState("observe", OFF_STATES.observe));
  if (gate.reason === "grace" && entitlement) {
    const expiry = Date.parse(entitlement.expires_at);
    const graceEnd = Number.isFinite(expiry) ? new Date(expiry + WRAP_GRACE_MS).toISOString().slice(0, 10) : "unknown";
    states.push(OFF_STATES.grace(graceEnd));
  }
  const allowance = entitlement ? weeklyAllowance(entitlement.plan) : null;
  if (allowance !== null && entitlement?.optimized_tokens_week !== undefined && entitlement.optimized_tokens_week >= allowance) {
    states.push(OFF_STATES.weeklyCap(humanTokens(entitlement.optimized_tokens_week), humanTokens(allowance)));
  }
  const mcpCompatibility = probeMcpBinary();
  if (mcpCompatibility && !mcpCompatibility.probe.current) {
    states.push(OFF_STATES.staleBinary("caveman-mcp", mcpCompatibility.probe.version, cliVersion()));
  } else if (!startMcpRecoveryAvailable()) {
    states.push(fixedOffState("mcp-missing", OFF_STATES.mcpMissing));
  }
  if (!resolveGoBin("cavemem", "CAVEMEM_BIN")) states.push(fixedOffState("mem-missing", OFF_STATES.memMissing));
  if (entitlement?.telemetry_level === "zdr") states.push(fixedOffState("zdr", OFF_STATES.zdr));
  if (versionInfo && !versionInfo.capabilities.includes("run_state")) {
    states.push(OFF_STATES.staleBinary("caveman-proxy", versionInfo.version, cliVersion()));
  }
  if (refreshOffline()) states.push(fixedOffState("refresh-offline", OFF_STATES.refreshOffline));

  const today = versionInfo ? readProxyObserveSummary(localMidnightRFC3339()) : null;
  const runningMode = runtime.owner !== "unknown" && runtime.mode
    ? gate.reason === "observe" && runtime.mode === "record" ? "observe" : runtime.mode
    : null;
  const resolvedMode = gate.reason === "observe" || gate.reason === "seat-wall" || gate.reason === "denied" || gate.reason === "unverified"
    ? "observe"
    : gate.mode;
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
  if (argv.includes("--json")) {
    print(view);
    return;
  }
  process.stdout.write(renderStatus(view));
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
const RECEIPT_FORMULA = "verified-savings-ledger.v1";
const CAVEBENCH_RECEIPT_FORMULA = "cavebench.self.v1";
const MAX_CATALOG_VERSION_LENGTH = 512;
const INCLUDED_RECEIPTS_ONLY = "included_receipts_only";
const SHA256_VALUE = /^sha256:[0-9a-f]{64}$/;

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
  if (r.schema !== RECEIPT_V1 && r.schema !== RECEIPT_V2 && r.schema !== RECEIPT_V3) return `seq ${r.seq}: unsupported receipt schema ${String(r.schema)}`;
  if (!r.scope || !SHA256_VALUE.test(r.scope.org_hash) || !SHA256_VALUE.test(r.scope.project_hash)) return `seq ${r.seq}: invalid receipt scope hash`;
  if (!validReceiptDay(r.day)) return `seq ${r.seq}: invalid receipt day ${String(r.day)}`;
  if (!Number.isSafeInteger(r.seq) || r.seq < 1) return `invalid receipt seq ${String(r.seq)}`;
  if ((r.seq === 1 && r.prev_receipt_hash !== "") || (r.seq > 1 && !SHA256_VALUE.test(r.prev_receipt_hash))) return `seq ${r.seq}: invalid prev_receipt_hash`;
  if (!Number.isFinite(r.verified_savings_usd) || !Number.isFinite(r.total_cost_usd) || r.total_cost_usd < 0) return `seq ${r.seq}: invalid money fields`;
  if (!validCounter(r.tokens_before) || !validCounter(r.tokens_after) || r.tokens_after > r.tokens_before) return `seq ${r.seq}: invalid token counters`;
  if (!validCounter(r.requests_optimized) || r.requests_optimized === 0) return `seq ${r.seq}: invalid optimized request count`;
  if (r.schema === RECEIPT_V2 || r.schema === RECEIPT_V3) {
    if (!validCounter(r.total_cost_microusd)) return `seq ${r.seq}: invalid total_cost_microusd`;
    if (roundCents((r.total_cost_microusd as number) / 1_000_000) !== r.total_cost_usd) return `seq ${r.seq}: total cost fields do not reconcile`;
  }
  if (r.schema === RECEIPT_V3) {
    if (!validSignedCounter(r.verified_savings_units_1e10)) return `seq ${r.seq}: invalid verified_savings_units_1e10`;
    if ((r.verified_savings_units_1e10 as number) / 10_000_000_000 !== r.verified_savings_usd) return `seq ${r.seq}: exact verified savings fields do not reconcile`;
    if (!validCounter(r.total_cost_units_1e10)) return `seq ${r.seq}: invalid total_cost_units_1e10`;
    if (Math.round((r.total_cost_units_1e10 as number) / 10_000) !== r.total_cost_microusd ||
        roundCents((r.total_cost_units_1e10 as number) / 10_000_000_000) !== r.total_cost_usd) return `seq ${r.seq}: exact total cost fields do not reconcile`;
  } else if (r.total_cost_units_1e10 !== undefined || r.verified_savings_units_1e10 !== undefined) {
    return `seq ${r.seq}: exact money units require receipt v3`;
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
TypeScript SDK: import { Cave } from "@caveman/sdk";
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
    console.error((body as any)?.error?.message ?? `request failed (${response.status})`);
    process.exit(1);
  }
  return body;
}

async function post(path: string, body: unknown) {
  const cfg = await config();
  requireAuth(cfg);
  const response = await fetch(`${cfg.baseURL}${path}`, { method: "POST", headers: { authorization: `Bearer ${cfg.token}`, "content-type": "application/json", "x-cave-csrf": "cli" }, body: JSON.stringify(body) });
  return response.json();
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

async function config(): Promise<Config> {
  const raw = await readFile(configPath(), "utf8").catch(() => "{}");
  const parsed = JSON.parse(raw) as Partial<Config>;
	const credentials = resolveCredentials(parsed);
  const cfg: Config = {
    baseURL: parsed.baseURL ?? process.env.CAVE_API_URL ?? "http://localhost:8080",
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
    return execFileSync("security", ["find-generic-password", "-s", service, "-a", account, "-w"], { encoding: "utf8" }).trim();
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
  mkdirSync(caveHome(), { recursive: true });
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

export const HELP_SCREEN = `caveman <agent>          run an agent on the layer  ({{agents}})
caveman run -- <cmd>     run anything else on the layer
caveman learn            what the layer would save you — score + ranked plan
caveman login            connect  (free · 1 seat · no card)
caveman status           what the layer did today

caveman tools <verb>     local capabilities   ·  caveman help tools
caveman cloud <verb>     connected verbs      ·  caveman help cloud`;

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
function which(cmd: string): string | null {
  if (cmd.includes("/")) return isExecutable(cmd) ? cmd : null;
  const exts = process.platform === "win32" ? (process.env.PATHEXT ?? ".EXE;.CMD;.BAT").split(";") : [""];
  for (const dir of (process.env.PATH ?? "").split(delimiter)) {
    if (!dir) continue;
    for (const ext of exts) {
      const full = join(dir, cmd + ext);
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

function gatewayHostPort(gw = gatewayURL()): { host: string; port: number } {
  try {
    const u = new URL(gw);
    return { host: u.hostname || "127.0.0.1", port: Number(u.port) || 80 };
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
