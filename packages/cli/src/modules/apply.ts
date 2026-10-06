// Switching modules on and off: the plan, the one confirmation, the apply.
//
// Module state is `modules.<id>` booleans in the global capability config
// (absent = the registry default, or off when a key the module owns is already
// off there). Switching a module writes its registry effects through the CLI's
// own config writer, and agent wiring reuses the native enable/disable/repair
// path and its journal. index.ts owns those primitives and hands them in once
// through setModuleHost, so nothing here is a second wiring path or a second
// config writer.
import { spawnSync } from "node:child_process";
import { existsSync } from "node:fs";
import { homedir } from "node:os";
import { join, sep } from "node:path";
import { portableInvocation } from "../portable-command.js";
import { cloudMe, cloudProduct, routeState, routingPause, type CloudMe } from "./cloud.js";
import { findModule, MODULES, type ModuleDef, type ModuleId } from "./registry.js";
import { moduleFix } from "./status.js";

export type ModuleSelection = Record<ModuleId, boolean>;
export type PlanLine = { action: "CREATE" | "UPDATE" | "DOWNLOAD" | "RUN"; target: string; detail: string };
// `only` scopes a plan to the named modules (`caveman on|off <module…>`);
// without it the plan makes everything match the selection (`--all`,
// onboarding). `notes` are side effects worth saying before Continue.
export type ModulePlan = { selection: ModuleSelection; agents: string[]; lines: PlanLine[]; only?: ModuleId[]; notes?: string[] };
export type ModuleState = { id: ModuleId; on: boolean; active: boolean; reason?: string; perAgent: Record<string, "wired" | "not wired" | "n/a"> };

export type NativeAgentInfo = { id: string; detected: boolean; wired: boolean };
export type LocalRuntime = { host: string; port: number; listening: boolean; foreign: boolean; pid?: number };
// A capability as every layer resolves it (defaults → global → project → env),
// plus the global-file value alone, which is what module state is recorded in.
export type Capability = { value: unknown; source: string; global: unknown; invalid?: string };
export type ModuleHost = {
  configPath(): string;
  readConfig(): Record<string, unknown>;
  mutateConfig(fn: (out: Record<string, unknown>) => void): void;
  // Writes one registry effect key to the global config; refuses unknown keys.
  setConfigValue(key: string, value: string | boolean): void;
  capability(key: string): Capability;
  // Config keys the written agent wiring depends on.
  wiringKeys: readonly string[];
  binaryRelease: string;
  resolveBinary(name: string): string | null;
  installBinaries(modules: ModuleId[]): Promise<void>;
  // Binaries the hub installed for a module, from modules.lock.json.
  lockedBinaries(module: ModuleId): string[];
  staleBinaries(): string[];
  which(name: string): string | null;
  // Every agent the native wiring supports, detected on PATH or journaled.
  nativeAgents(): NativeAgentInfo[];
  // Files `enable <agent>` would write, computed without writing them.
  planWiring(agent: string): { file: string; exists: boolean; kind: string }[];
  wiredFiles(agent: string): string[];
  agentName(agent: string): string;
  // Wiring is quiet: applyModules reports each step through its progress line.
  wireAgent(agent: string): void;
  unwireAgent(agent: string): void;
  refreshAgent(agent: string): void;
  // True when wiring an agent will start the local runtime.
  runtimeAutostarts(): Promise<boolean>;
  // Whether the local runtime answers, waiting up to waitMs for it.
  runtimeListening(waitMs: number): Promise<boolean>;
  agentState(agent: string): string;
  coreActive(): boolean;
  signedIn(): boolean;
  cloudCheck(): Promise<void>;
  localRuntimes(): Promise<LocalRuntime[]>;
  interactive(): boolean;
  confirm(question: string): Promise<boolean>;
  // Where new wiring sends agent traffic, and the line status prints; `fix`
  // when an earlier login left agents on the managed gateway unasked.
  agentTraffic(): { target: "local" | "managed"; line: string; fix?: string };
  // A wired agent's written base URL is not the current traffic target.
  agentRouteStale(agent: string): boolean;
  // Cloud's GET /api/v1/auth/me answer, or null when it cannot be read.
  cloudMe(): Promise<CloudMe | null>;
  openBrowser(url: string): void;
};

let host: ModuleHost | undefined;

export function setModuleHost(value: ModuleHost): void {
  host = value;
}

export function moduleHost(): ModuleHost {
  if (!host) throw new Error("module host not initialized");
  return host;
}

function objectOf(value: unknown): Record<string, unknown> {
  return value && typeof value === "object" && !Array.isArray(value) ? value as Record<string, unknown> : {};
}

function tilde(path: string): string {
  const home = homedir();
  return path.startsWith(home + sep) ? `~${path.slice(home.length)}` : path;
}

function storedModules(): Record<string, unknown> {
  return objectOf(moduleHost().readConfig().modules);
}

function inScope(id: ModuleId, only: ModuleId[] | undefined): boolean {
  return !only || only.includes(id);
}

export function currentSelection(): ModuleSelection {
  const h = moduleHost();
  const stored = storedModules();
  return Object.fromEntries(MODULES.map((m) => [
    m.id,
    typeof stored[m.id] === "boolean" ? stored[m.id]
      // A config from before modules that already switched the module's primary
      // key off reads as off. Only the first key counts: think.toon or
      // think.shrink off alone is a tuning of input, not input off.
      : m.capabilities.slice(0, 1).some((effect) => {
        const capability = h.capability(effect.key);
        return capability.invalid === undefined && capability.global === effect.off;
      }) ? false
      : m.defaultOn,
  ])) as ModuleSelection;
}

// The agents `on` acts on: the ones already wired, or, before any is, every
// supported agent found on PATH.
export function defaultAgents(): string[] {
  const native = moduleHost().nativeAgents();
  const wired = native.filter((agent) => agent.wired);
  return (wired.length ? wired : native.filter((agent) => agent.detected)).map((agent) => agent.id);
}

// Module state is the authority for the keys a module owns, but only where
// the state moves: a module switched on or off (or whose primary key — its
// first capability, think.mode for input — contradicts its state) gets every
// key that disagrees rewritten to that state's value. A module that stays as
// it is keeps deliberate tuning (think.toon=false under input on). An invalid
// value is always rewritten. Only modules in scope are touched.
function configChanges(selection: ModuleSelection, only: ModuleId[] | undefined) {
  const h = moduleHost();
  const stored = storedModules();
  const current = currentSelection();
  const scoped = MODULES.filter((m) => inScope(m.id, only));
  const state = scoped.filter((m) => stored[m.id] !== selection[m.id]).map((m) => [m.id, selection[m.id]] as const);
  const effects: (readonly [string, string | boolean])[] = [];
  const disagrees = (on: boolean, global: unknown, off: string | boolean) => on ? global === off : global !== off;
  for (const m of scoped) {
    const primary = m.capabilities[0];
    const moving = current[m.id] !== selection[m.id]
      || (primary !== undefined && disagrees(selection[m.id], h.capability(primary.key).global, primary.off));
    for (const effect of m.capabilities) {
      const capability = h.capability(effect.key);
      if ((moving && disagrees(selection[m.id], capability.global, effect.off)) || capability.invalid !== undefined) {
        effects.push([effect.key, selection[m.id] ? effect.on : effect.off]);
      }
    }
  }
  return { state, effects };
}

// Agent wiring changes only when an agent-wired module is in scope. Switching
// one on wires the given agents that are not wired yet; the last one off
// unwires every agent. A full plan (no scope) also unwires agents outside
// `agents`.
function wiringChanges(selection: ModuleSelection, agents: string[], only: ModuleId[] | undefined) {
  const native = moduleHost().nativeAgents();
  const wired = native.filter((agent) => agent.wired).map((agent) => agent.id);
  const supported = agents.filter((agent) => native.some((info) => info.id === agent));
  const none = { wire: [] as string[], unwire: [] as string[] };
  if (!MODULES.some((m) => m.wiresAgents && inScope(m.id, only))) return none;
  if (!MODULES.some((m) => m.wiresAgents && selection[m.id])) return { wire: [], unwire: wired };
  const turningOn = MODULES.some((m) => m.wiresAgents && inScope(m.id, only) && selection[m.id]);
  return {
    wire: turningOn ? supported.filter((agent) => !wired.includes(agent)) : [],
    unwire: only ? [] : wired.filter((agent) => !supported.includes(agent)),
  };
}

// Wired agents that stay wired but carry hooks built from a key this run
// changes, or a base URL that is no longer the traffic target (an earlier
// login's managed gateway): any plan re-wires those, never anything else.
function refreshAgents(effects: readonly (readonly [string, unknown])[], unwire: string[]): string[] {
  const h = moduleHost();
  const keyChanged = effects.some(([key]) => h.wiringKeys.includes(key));
  return h.nativeAgents()
    .filter((agent) => agent.wired && !unwire.includes(agent.id) && (keyChanged || h.agentRouteStale(agent.id)))
    .map((agent) => agent.id);
}

// Modules in scope that are on and miss a binary they need. An external
// binary is missing when no usable copy resolves, unless an override names
// one: a download would not be the copy used then.
function binaryNeeds(selection: ModuleSelection, only: ModuleId[] | undefined) {
  const h = moduleHost();
  const on = MODULES.filter((m) => selection[m.id] && inScope(m.id, only));
  const missingOf = (m: ModuleDef) => [
    ...m.binaries.filter((name) => !h.resolveBinary(name)),
    ...(m.external && !externalOverride(m) && !externalBin(m) ? [m.external.binary] : []),
  ];
  const missing = [...new Set(on.flatMap(missingOf))];
  return { missing, modules: on.filter((m) => missingOf(m).length).map((m) => m.id) };
}

function externalOverride(def: ModuleDef): string | undefined {
  return process.env[`${def.external!.binary.toUpperCase().replace(/-/g, "_")}_BIN`] || undefined;
}

// Blocks reports this from `version --json` once it can answer
// `hooks status --json` (rc.2).
const STATUS_JSON = "hooks_status_json";
const answersJson = new Map<string, boolean>();

function reportsStatusJson(bin: string): boolean {
  let known = answersJson.get(bin);
  if (known === undefined) {
    try {
      const out = runExternal(bin, ["version", "--json"], 5000);
      const capabilities = (JSON.parse(out.stdout) as { capabilities?: unknown }).capabilities;
      known = out.status === 0 && Array.isArray(capabilities) && capabilities.includes(STATUS_JSON);
    } catch {
      known = false;
    }
    answersJson.set(bin, known);
  }
  return known;
}

// The external binary the hub runs: the override, the copy the hub installed
// (recorded in modules.lock.json), or one on PATH new enough to report its
// hooks as JSON. A user's own current Blocks stays usable; an older one is
// passed over for the signed download.
function externalBin(def: ModuleDef): string | null {
  const name = def.external!.binary;
  const explicit = externalOverride(def);
  if (explicit) return existsSync(explicit) ? explicit : null;
  const local = join(process.env.CAVEMAN_HOME ?? join(homedir(), ".caveman"), "bin", process.platform === "win32" ? `${name}.exe` : name);
  if (moduleHost().lockedBinaries(def.id).includes(name) && existsSync(local)) return local;
  const onPath = moduleHost().which(name);
  return onPath && reportsStatusJson(onPath) ? onPath : null;
}

// Why an external module cannot run: no binary, or the copy that resolves (or
// the only one on PATH) cannot report its hooks. Undefined when it can.
function externalProblem(def: ModuleDef, bin: string | null, status: Record<string, boolean> | undefined): string | undefined {
  if (bin && status) return undefined;
  const old = bin ?? (externalOverride(def) ? null : moduleHost().which(def.external!.binary));
  return old ? `${tilde(old)} is older than Blocks rc.2 · update or remove it` : `${def.external!.binary} not installed`;
}

// Output is captured: the hub says one line per step, and shows the binary's
// own output only when the step fails.
function runExternal(bin: string, args: readonly string[], timeout: number) {
  const invocation = portableInvocation(bin, args);
  return spawnSync(invocation.command, invocation.args, { encoding: "utf8", timeout, stdio: ["ignore", "pipe", "pipe"] });
}

// Harness name → hook installed, from `<binary> hooks status --json`
// ({ harnesses: [{ name, installed }] }). Undefined when the binary cannot say.
function externalStatus(def: ModuleDef, bin: string, flags: string[] = []): Record<string, boolean> | undefined {
  try {
    const out = runExternal(bin, [...def.external!.status, ...flags], 5000);
    const parsed = JSON.parse(out.stdout) as { harnesses?: { name?: unknown; installed?: unknown }[] };
    if (out.status !== 0 || !Array.isArray(parsed.harnesses)) return undefined;
    return Object.fromEntries(parsed.harnesses.filter((h) => typeof h.name === "string").map((h) => [h.name as string, h.installed === true]));
  } catch {
    return undefined;
  }
}

// Blocks' harness names for the agents the hub wires; it hooks no others.
const EXTERNAL_HARNESSES: Record<string, string> = { claude: "claude-code", codex: "codex", opencode: "opencode" };
const harnessFlags = (agents: string[]) => agents.flatMap((agent) => EXTERNAL_HARNESSES[agent] ? ["--harness", EXTERNAL_HARNESSES[agent]] : []);

// The install/uninstall an external module needs, for the selected agents
// only. It is in scope when named, and also when it is on and this run wires
// an agent, so a new agent gets its hook in the same run. Install runs when a
// selected agent's hook is missing or, without a status answer, when the
// module is first recorded on. Uninstall runs only when the hub had it
// recorded on, so hooks installed with Blocks' own installer stay. `bin` is
// null when no usable binary resolves; the run is then skipped. `before` is
// the status ahead of the run.
function externalRuns(selection: ModuleSelection, only: ModuleId[] | undefined, agents: string[], wiring: boolean) {
  const stored = storedModules();
  const runs: { def: ModuleDef; args: string[]; install: boolean; bin: string | null; flags: string[]; before?: Record<string, boolean> | undefined }[] = [];
  for (const def of MODULES) {
    if (!def.external || !(inScope(def.id, only) || (selection[def.id] && wiring))) continue;
    const bin = externalBin(def);
    if (!inScope(def.id, only) && !bin) continue;
    if (selection[def.id]) {
      const flags = harnessFlags(agents);
      if (flags.length === 0) continue;
      const before = bin ? externalStatus(def, bin, flags) : undefined;
      const needed = before ? Object.values(before).some((installed) => !installed) : stored[def.id] !== true;
      if (needed) runs.push({ def, args: def.external.install, install: true, bin, flags, before });
    } else if (stored[def.id] === true && bin) {
      runs.push({ def, args: def.external.uninstall, install: false, bin, flags: harnessFlags(Object.keys(EXTERNAL_HARNESSES)) });
    }
  }
  return runs;
}

// The hub's name for a Blocks harness.
function harnessName(name: string): string {
  const agent = Object.keys(EXTERNAL_HARNESSES).find((id) => EXTERNAL_HARNESSES[id] === name);
  return agent ? moduleHost().agentName(agent) : name;
}

// What a harness's new hook still asks of the user, from Blocks' trust notes;
// notes that ask nothing are left out.
const EXTERNAL_ASKS: Record<string, string> = {
  codex: "Codex asks once: open /hooks in Codex and approve the caveman-blocks hook",
};

// One line for a finished install, built from the status it leaves, then one
// line per new hook the user still has to approve.
function externalReady(def: ModuleDef, bin: string, flags: string[], before: Record<string, boolean> | undefined): string[] {
  const after = externalStatus(def, bin, flags);
  if (!after) return [`○ ${def.id}: ${externalProblem(def, bin, after)}`];
  const hooked = Object.keys(after).filter((name) => after[name]);
  if (hooked.length === 0) return [`○ ${def.id}: no agent to hook yet`];
  return [
    `✓ ${def.id} ready (${hooked.map(harnessName).join(", ")})`,
    ...hooked.filter((name) => !before?.[name] && EXTERNAL_ASKS[name]).map((name) => `  ${EXTERNAL_ASKS[name]}`),
  ];
}

export async function planModules(selection: ModuleSelection, agents: string[], options: { only?: ModuleId[] } = {}): Promise<ModulePlan> {
  const h = moduleHost();
  const { only } = options;
  const lines: PlanLine[] = [];
  const notes: string[] = [];
  const { state, effects } = configChanges(selection, only);
  const { wire, unwire } = wiringChanges(selection, agents, only);

  const { missing } = binaryNeeds(selection, only);
  // Hub binaries ship in every release; an external one (Blocks) only in a
  // release whose signed modules.json carries it, so the plan says so.
  const externals = new Set(MODULES.flatMap((m) => (m.external ? [m.external.binary] : [])));
  const hubMissing = missing.filter((name) => !externals.has(name));
  const externalMissing = missing.filter((name) => externals.has(name));
  if (hubMissing.length) lines.push({ action: "DOWNLOAD", target: hubMissing.join(", "), detail: `signed, ${h.binaryRelease}` });
  if (externalMissing.length) lines.push({ action: "DOWNLOAD", target: externalMissing.join(", "), detail: `signed, if ${h.binaryRelease} carries it` });

  if (state.length || effects.length) {
    lines.push({
      action: existsSync(h.configPath()) ? "UPDATE" : "CREATE",
      target: tilde(h.configPath()),
      detail: [
        ...[true, false].map((on) => [on, state.filter(([, value]) => value === on).map(([id]) => id)] as const)
          .filter(([, ids]) => ids.length).map(([on, ids]) => `modules ${on ? "on" : "off"}: ${ids.join(", ")}`),
        ...effects.map(([key, value]) => `${key} = ${value}`),
      ].join(" · "),
    });
  }

  for (const agent of unwire) {
    for (const file of h.wiredFiles(agent)) lines.push({ action: "UPDATE", target: tilde(file), detail: `remove ${agent} wiring` });
  }
  for (const agent of wire) {
    try {
      for (const file of h.planWiring(agent)) {
        lines.push({ action: file.exists ? "UPDATE" : "CREATE", target: tilde(file.file), detail: file.kind.replace("-", " ") });
      }
    } catch {
      // The binaries the plan downloads first are what this needs; enable
      // reports any real refusal when it runs.
      lines.push({ action: "UPDATE", target: `${agent} config`, detail: "route + hooks" });
    }
  }
  const target = h.agentTraffic().target === "local" ? "the local runtime" : "the managed gateway";
  for (const agent of refreshAgents(effects, unwire)) {
    const detail = h.agentRouteStale(agent) ? `point ${agent} at ${target}` : `refresh ${agent} hooks`;
    for (const file of h.wiredFiles(agent)) lines.push({ action: "UPDATE", target: tilde(file), detail });
  }
  // aider is wired without the runtime; every other agent starts it.
  if (wire.some((agent) => agent !== "aider") && await h.runtimeAutostarts()) {
    lines.push({ action: "RUN", target: "caveman-proxy", detail: "start local runtime" });
  }
  for (const run of externalRuns(selection, only, agents, wire.length > 0)) {
    const name = run.def.external!.binary;
    // A binary the plan downloads first is not a skip.
    const skipped = !run.bin && !missing.includes(name);
    const names = [...new Set(run.flags.filter((flag) => flag !== "--harness").map(harnessName))];
    lines.push({ action: "RUN", target: `${name} ${run.args.join(" ")}`, detail: skipped ? `skipped: ${externalProblem(run.def, null, undefined)}` : run.install ? names.join(", ") : "" });
  }

  for (const def of MODULES) {
    if (def.external && selection[def.id] && inScope(def.id, only) && agents.length && harnessFlags(agents).length === 0) {
      notes.push(`${def.id} stays idle: none of the selected agents takes its hook (${Object.keys(EXTERNAL_HARNESSES).map((agent) => h.agentName(agent)).join(", ")})`);
    }
  }
  // Core is withheld in record mode, which is what input switched off means.
  if (selection.output && !selection.input && currentSelection().input && inScope("input", only)) {
    notes.push("output also pauses: it runs through input");
  }
  return { selection: { ...selection }, agents: [...agents], lines, ...(only ? { only: [...only] } : {}), ...(notes.length ? { notes } : {}) };
}

// `progress` gets one line per step as it completes ("✓ Claude Code wired");
// failures come back in `problems` with their full message.
export async function applyModules(plan: ModulePlan, opts: { yes: boolean; progress?: (line: string) => void }): Promise<{ ok: boolean; problems: string[] }> {
  const h = moduleHost();
  if (plan.lines.length === 0) return { ok: true, problems: [] };
  if (!opts.yes) {
    if (!h.interactive()) return { ok: false, problems: ["nothing changed: pass --yes to apply without a prompt"] };
    if (!(await h.confirm("Continue? [y/N]"))) return { ok: false, problems: ["nothing changed"] };
  }
  const problems: string[] = [];
  const fail = (what: string, error: unknown) => problems.push(`${what}: ${error instanceof Error ? error.message : String(error)}`);
  const say = opts.progress ?? (() => {});
  const needs = binaryNeeds(plan.selection, plan.only);
  if (needs.missing.length) {
    try {
      await h.installBinaries(needs.modules);
      // A release from before modules.json brings no external binary.
      const still = binaryNeeds(plan.selection, plan.only).missing;
      const got = needs.missing.filter((name) => !still.includes(name));
      if (got.length) say(`✓ downloaded ${got.join(", ")}`);
    } catch (error) { fail("download", error); }
  }
  // Decide everything before the first config write: the external uninstall
  // reads the module state this run replaces.
  const { state, effects } = configChanges(plan.selection, plan.only);
  const { wire, unwire } = wiringChanges(plan.selection, plan.agents, plan.only);
  const refresh = refreshAgents(effects, unwire);
  const runs = externalRuns(plan.selection, plan.only, plan.agents, wire.length > 0);
  const startsRuntime = wire.some((agent) => agent !== "aider") && await h.runtimeAutostarts();

  if (state.length) h.mutateConfig((out) => { out.modules = { ...objectOf(out.modules), ...Object.fromEntries(state) }; });
  // Effects land before wiring: enable and repair read think.shrink for hooks.
  for (const [key, value] of effects) h.setConfigValue(key, value);
  const step = (agent: string, done: string, act: () => void) => {
    try {
      act();
      say(`✓ ${h.agentName(agent)} ${done}`);
    } catch (error) { fail(agent, error); }
  };
  for (const agent of unwire) step(agent, "unwired", () => h.unwireAgent(agent));
  for (const agent of wire) step(agent, "wired", () => h.wireAgent(agent));
  for (const agent of refresh) step(agent, "hooks refreshed", () => h.refreshAgent(agent));
  if (startsRuntime) {
    say(await h.runtimeListening(1000) ? "✓ local runtime started" : "○ local runtime starts with your next agent session");
  }
  for (const run of runs) {
    const name = run.def.external!.binary;
    // Absent binary: the module stays on but inactive, and the verb says why.
    if (!run.bin) {
      const why = externalProblem(run.def, null, undefined)!;
      say(`○ ${run.def.id}: ${why.endsWith(" not installed") ? `${why} yet` : why}`);
      continue;
    }
    const out = runExternal(run.bin, [...run.args, ...run.flags], 120_000);
    const output = `${out.stdout ?? ""}${out.stderr ?? ""}`.trim();
    if (out.status !== 0) problems.push(`${name} ${run.args.join(" ")} failed${out.error ? `: ${out.error.message}` : ""}${output ? `\n${output}` : ""}`);
    else if (run.install) for (const line of externalReady(run.def, run.bin, run.flags, run.before)) say(line);
    else say(`✓ ${run.def.id}: ${name} ${run.args.join(" ")}`);
  }
  return { ok: problems.length === 0, problems };
}

function externalAgentState(status: Record<string, boolean> | undefined, agent: string): "wired" | "not wired" | "n/a" {
  const harness = EXTERNAL_HARNESSES[agent];
  const installed = harness === undefined ? undefined : status?.[harness];
  return installed === undefined ? "n/a" : installed ? "wired" : "not wired";
}

// Env variables resolveCapabilities (index.ts) reads for the keys modules own.
const ENV_NAMES: Record<string, string> = {
  "think.mode": "CAVEMAN_WRAP_MODE",
  "think.core": "CAVEMAN_CORE",
  "think.toon": "CAVEMAN_TOON",
  "think.shrink": "CAVEMAN_SHRINK",
};

// A sign-in module is active once Cloud answers for the signed-in account;
// a /me without the product's fields still counts as an answer.
function inactiveReason(m: ModuleDef, selection: ModuleSelection, signedIn: boolean, external?: { bin: string | null; status: Record<string, boolean> | undefined }, me?: CloudMe | null): string | undefined {
  const h = moduleHost();
  if (m.needsSignIn) {
    if (!signedIn) return `sign in to turn on ${m.id}`;
    // caveman-proxy acts only on an explicit switch: signing in alone never
    // starts a Cloud feature (a config from before modules has none).
    if (storedModules()[m.id] !== true) return `${m.id} not switched on in config`;
    const product = cloudProduct(me ?? null, m.id);
    if (!me || product?.state === "off") return `waiting for Cloud ${m.id}`;
    const pause = m.id === "routing" ? routingPause(product) : product?.state === "limited" ? product : undefined;
    if (pause) return `${m.id} paused · ${(pause.reason ?? "limit").replace(/_/g, " ")}`;
    // caveman-proxy was refused the key (one minted before it could route,
    // or revoked); a new login mints one.
    if (m.id === "routing" && routeState()?.outcome === "degraded") return `${m.id} degraded · Cloud refused this login's key`;
  }
  const blocked = m.external && external ? externalProblem(m, external.bin, external.status) : undefined;
  if (blocked) return blocked;
  const missing = m.binaries.filter((name) => !h.resolveBinary(name));
  if (missing.length) return `${missing.join(", ")} not installed`;
  for (const effect of m.capabilities) {
    const capability = h.capability(effect.key);
    if (capability.invalid !== undefined) return `${effect.key} has an invalid value: ${capability.invalid}`;
    if (capability.value === effect.off && capability.source === "project") return "overridden by project config";
    if (capability.value === effect.off && capability.source === "env") return `overridden by ${ENV_NAMES[effect.key] ?? "the environment"}`;
    if (capability.global === effect.off) return `${effect.key} is ${effect.off} in config`;
  }
  if (m.id === "output" && !h.coreActive()) return selection.input ? "record mode keeps it off" : "paused while input is off";
  return undefined;
}

export async function moduleStates(): Promise<ModuleState[]> {
  const h = moduleHost();
  const selection = currentSelection();
  const agents = h.nativeAgents().filter((agent) => agent.detected || agent.wired);
  const signedIn = h.signedIn();
  const me = signedIn && MODULES.some((m) => m.needsSignIn && selection[m.id]) ? await cloudMe() : null;
  return MODULES.map((m) => {
    const on = selection[m.id];
    const bin = on && m.external ? externalBin(m) : null;
    const status = m.external && bin ? externalStatus(m, bin) : undefined;
    const reason = on ? inactiveReason(m, selection, signedIn, { bin, status }, me) : undefined;
    const perAgent = Object.fromEntries(agents.map((agent) => [
      agent.id,
      m.wiresAgents ? agent.wired ? "wired" : "not wired" : externalAgentState(status, agent.id),
    ])) as ModuleState["perAgent"];
    return { id: m.id, on, active: on && !reason, ...(reason ? { reason } : {}), perAgent };
  });
}

export function renderPlan(plan: ModulePlan): string {
  const width = Math.min(32, Math.max(...plan.lines.map((line) => line.target.length)));
  const lines = plan.lines.map((line) => `  ${line.action.padEnd(9)} ${line.target.padEnd(width)}  ${line.detail}`.trimEnd());
  return `This will\n${[...lines, ...(plan.notes ?? []).map((note) => `note: ${note}`)].join("\n")}\n`;
}

export async function moduleSwitchCommand(on: boolean, argv: string[]): Promise<void> {
  const verb = on ? "on" : "off";
  const flags = argv.filter((arg) => arg.startsWith("-"));
  const names = argv.filter((arg) => !arg.startsWith("-"));
  const all = flags.includes("--all");
  const ids = MODULES.map((m) => m.id).join(", ");
  const badFlag = flags.some((flag) => !["--all", "--yes", "-y", "--dry-run"].includes(flag));
  // Exactly one of: module names, or --all.
  const badTargets = all ? names.length > 0 : names.length === 0;
  if (badFlag || badTargets) {
    console.error(`usage: caveman ${verb} <module…> | --all  [--yes] [--dry-run]\nmodules: ${ids}`);
    process.exit(2);
  }
  const unknown = names.filter((name) => !findModule(name));
  if (unknown.length) {
    console.error(`unknown module: ${unknown.join(", ")} · modules: ${ids}`);
    process.exit(2);
  }
  const named = MODULES.filter((m) => all || names.includes(m.id)).map((m) => m.id);
  const selection = currentSelection();
  for (const id of named) selection[id] = on;
  // `off` never wires an agent that is not wired yet.
  const agents = on ? defaultAgents() : moduleHost().nativeAgents().filter((agent) => agent.wired).map((agent) => agent.id);
  const plan = await planModules(selection, agents, all ? {} : { only: named });
  const label = all ? "every module" : named.join(", ");
  if (plan.lines.length === 0) {
    console.log(`✓ ${label} already ${verb}`);
    return;
  }
  process.stdout.write(renderPlan(plan));
  if (flags.includes("--dry-run")) return;
  const result = await applyModules(plan, { yes: flags.includes("--yes") || flags.includes("-y"), progress: (line) => console.log(line) });
  if (!result.ok) {
    for (const problem of result.problems) console.error(`✗ ${problem}`);
    process.exitCode = 1;
    return;
  }
  for (const state of (await moduleStates()).filter((item) => named.includes(item.id))) {
    const fix = moduleFix(state);
    const why = state.on && !state.active ? ` · ${state.reason}${fix ? ` · ${fix}` : ""}` : "";
    console.log(`✓ ${state.id} ${verb}${why}`);
  }
}
