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

// Module state is the authority for the keys a module owns: an on module whose
// key sits at its off value (or holds an invalid one), or an off module whose
// key does not, is rewritten. Only modules in scope are touched.
function configChanges(selection: ModuleSelection, only: ModuleId[] | undefined) {
  const h = moduleHost();
  const stored = storedModules();
  const scoped = MODULES.filter((m) => inScope(m.id, only));
  const state = scoped.filter((m) => stored[m.id] !== selection[m.id]).map((m) => [m.id, selection[m.id]] as const);
  const effects: (readonly [string, string | boolean])[] = [];
  for (const m of scoped) {
    for (const effect of m.capabilities) {
      const capability = h.capability(effect.key);
      const wrong = selection[m.id] ? capability.global === effect.off : capability.global !== effect.off;
      if (wrong || capability.invalid !== undefined) effects.push([effect.key, selection[m.id] ? effect.on : effect.off]);
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

// Wired agents that stay wired but carry hooks built from a key this run changes.
function refreshAgents(effects: readonly (readonly [string, unknown])[], unwire: string[]): string[] {
  const h = moduleHost();
  if (!effects.some(([key]) => h.wiringKeys.includes(key))) return [];
  return h.nativeAgents().filter((agent) => agent.wired && !unwire.includes(agent.id)).map((agent) => agent.id);
}

// Modules in scope that are on and miss a binary they need.
function binaryNeeds(selection: ModuleSelection, only: ModuleId[] | undefined) {
  const h = moduleHost();
  const on = MODULES.filter((m) => selection[m.id] && inScope(m.id, only));
  const names = [...new Set(on.flatMap((m) => m.binaries))];
  const missing = names.filter((name) => !h.resolveBinary(name));
  return { missing, modules: on.filter((m) => m.binaries.some((name) => missing.includes(name))).map((m) => m.id) };
}

function externalBin(name: string): string | null {
  const explicit = process.env[`${name.toUpperCase().replace(/-/g, "_")}_BIN`];
  if (explicit) return existsSync(explicit) ? explicit : null;
  const local = join(process.env.CAVEMAN_HOME ?? join(homedir(), ".caveman"), "bin", process.platform === "win32" ? `${name}.exe` : name);
  return moduleHost().which(name) ?? (existsSync(local) ? local : null);
}

function runExternal(bin: string, args: readonly string[], capture: boolean) {
  const invocation = portableInvocation(bin, args);
  return spawnSync(invocation.command, invocation.args, {
    encoding: "utf8",
    timeout: capture ? 5000 : 120_000,
    stdio: capture ? ["ignore", "pipe", "ignore"] : ["ignore", 2, 2],
  });
}

// Harness name → hook installed, from `<binary> hooks status --json`
// ({ harnesses: [{ name, installed }] }). Undefined when the binary cannot say.
function externalStatus(def: ModuleDef, bin: string): Record<string, boolean> | undefined {
  try {
    const out = runExternal(bin, def.external!.status, true);
    const parsed = JSON.parse(out.stdout) as { harnesses?: { name?: unknown; installed?: unknown }[] };
    if (out.status !== 0 || !Array.isArray(parsed.harnesses)) return undefined;
    return Object.fromEntries(parsed.harnesses.filter((h) => typeof h.name === "string").map((h) => [h.name as string, h.installed === true]));
  } catch {
    return undefined;
  }
}

// The install/uninstall an external module in scope needs. Without a status
// answer, install runs when the module is switched on (or first recorded on).
// `bin` is null when the binary is not installed: the run is then skipped.
function externalRuns(selection: ModuleSelection, only: ModuleId[] | undefined) {
  const current = currentSelection();
  const stored = storedModules();
  const runs: { def: ModuleDef; args: string[]; bin: string | null }[] = [];
  for (const def of MODULES) {
    if (!def.external || !inScope(def.id, only)) continue;
    const bin = externalBin(def.external.binary);
    if (selection[def.id]) {
      const status = bin ? externalStatus(def, bin) : undefined;
      const needed = status ? Object.values(status).some((installed) => !installed) : stored[def.id] !== true;
      if (needed) runs.push({ def, args: def.external.install, bin });
    } else if (current[def.id] && bin) {
      runs.push({ def, args: def.external.uninstall, bin });
    }
  }
  return runs;
}

export async function planModules(selection: ModuleSelection, agents: string[], options: { only?: ModuleId[] } = {}): Promise<ModulePlan> {
  const h = moduleHost();
  const { only } = options;
  const lines: PlanLine[] = [];
  const notes: string[] = [];
  const { state, effects } = configChanges(selection, only);
  const { wire, unwire } = wiringChanges(selection, agents, only);

  const { missing } = binaryNeeds(selection, only);
  if (missing.length) lines.push({ action: "DOWNLOAD", target: missing.join(", "), detail: `signed, ${h.binaryRelease}` });

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
  for (const agent of refreshAgents(effects, unwire)) {
    for (const file of h.wiredFiles(agent)) lines.push({ action: "UPDATE", target: tilde(file), detail: `refresh ${agent} hooks` });
  }
  // aider is wired without the runtime; every other agent starts it.
  if (wire.some((agent) => agent !== "aider") && await h.runtimeAutostarts()) {
    lines.push({ action: "RUN", target: "caveman-proxy", detail: "start local runtime" });
  }
  for (const run of externalRuns(selection, only)) {
    const name = run.def.external!.binary;
    lines.push({ action: "RUN", target: `${name} ${run.args.join(" ")}`, detail: run.bin ? "" : `skipped: ${name} not installed` });
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
      say(`✓ downloaded ${needs.missing.join(", ")}`);
    } catch (error) { fail("download", error); }
  }
  // Decide everything before the first config write: the external uninstall
  // reads the module state this run replaces.
  const { state, effects } = configChanges(plan.selection, plan.only);
  const { wire, unwire } = wiringChanges(plan.selection, plan.agents, plan.only);
  const refresh = refreshAgents(effects, unwire);
  const runs = externalRuns(plan.selection, plan.only);
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
      say(`○ ${run.def.id}: ${name} not installed yet`);
      continue;
    }
    const out = runExternal(run.bin, run.args, false);
    if (out.status !== 0) problems.push(`${name} ${run.args.join(" ")} failed${out.error ? `: ${out.error.message}` : ""}`);
    else say(`✓ ${run.def.id}: ${name} ${run.args.join(" ")}`);
  }
  return { ok: problems.length === 0, problems };
}

function externalAgentState(status: Record<string, boolean> | undefined, agent: string): "wired" | "not wired" | "n/a" {
  // Blocks names Claude Code "claude-code"; the hub calls it "claude".
  const installed = status?.[agent] ?? status?.[`${agent}-code`];
  return installed === undefined ? "n/a" : installed ? "wired" : "not wired";
}

// Env variables resolveCapabilities (index.ts) reads for the keys modules own.
const ENV_NAMES: Record<string, string> = {
  "think.mode": "CAVEMAN_WRAP_MODE",
  "think.core": "CAVEMAN_CORE",
  "think.toon": "CAVEMAN_TOON",
  "think.shrink": "CAVEMAN_SHRINK",
};

function inactiveReason(m: ModuleDef, selection: ModuleSelection, signedIn: boolean): string | undefined {
  const h = moduleHost();
  if (m.needsSignIn) return signedIn ? `waiting for Cloud ${m.id}` : `sign in to turn on ${m.id}`;
  if (m.external && !externalBin(m.external.binary)) return `${m.external.binary} not installed`;
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
  return MODULES.map((m) => {
    const on = selection[m.id];
    const bin = on && m.external ? externalBin(m.external.binary) : null;
    const status = m.external && bin ? externalStatus(m, bin) : undefined;
    const reason = on ? inactiveReason(m, selection, signedIn) : undefined;
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
