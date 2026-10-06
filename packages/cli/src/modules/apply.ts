// Switching modules on and off: the plan, the one confirmation, the apply.
//
// Module state is `modules.<id>` booleans in the global capability config
// (absent = the registry default). Switching a module writes its registry
// effects through the CLI's own config writer, and agent wiring reuses the
// native enable/disable path and its journal. index.ts owns those primitives
// and hands them in once through setModuleHost, so nothing here is a second
// wiring path or a second config writer.
import { spawnSync } from "node:child_process";
import { existsSync } from "node:fs";
import { homedir } from "node:os";
import { join, sep } from "node:path";
import { portableInvocation } from "../portable-command.js";
import { findModule, MODULES, type ModuleDef, type ModuleId } from "./registry.js";

export type ModuleSelection = Record<ModuleId, boolean>;
export type PlanLine = { action: "CREATE" | "UPDATE" | "DOWNLOAD" | "RUN"; target: string; detail: string };
export type ModulePlan = { selection: ModuleSelection; agents: string[]; lines: PlanLine[] };
export type ModuleState = { id: ModuleId; on: boolean; active: boolean; reason?: string; perAgent: Record<string, "wired" | "not wired" | "n/a"> };

export type NativeAgentInfo = { id: string; detected: boolean; wired: boolean };
export type LocalRuntime = { host: string; port: number; listening: boolean; pid?: number };
export type ModuleHost = {
  configPath(): string;
  readConfig(): Record<string, unknown>;
  mutateConfig(fn: (out: Record<string, unknown>) => void): void;
  // Writes one registry effect key: a capability key or a top-level config key.
  setConfigValue(key: string, value: string | boolean): void;
  configDefault(key: string): unknown;
  binaryRelease: string;
  resolveBinary(name: string): string | null;
  installBinaries(): Promise<void>;
  which(name: string): string | null;
  // Every agent the native wiring supports, detected on PATH or journaled.
  nativeAgents(): NativeAgentInfo[];
  // Files `enable <agent>` would write, computed without writing them.
  planWiring(agent: string): { file: string; exists: boolean; kind: string }[];
  wiredFiles(agent: string): string[];
  wireAgent(agent: string): void;
  unwireAgent(agent: string): void;
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

export function currentSelection(): ModuleSelection {
  const stored = storedModules();
  return Object.fromEntries(MODULES.map((m) => [m.id, typeof stored[m.id] === "boolean" ? stored[m.id] : m.defaultOn])) as ModuleSelection;
}

// The agents `on`/`off` act on: the ones already wired, or, before any is,
// every supported agent found on PATH.
export function defaultAgents(): string[] {
  const native = moduleHost().nativeAgents();
  const wired = native.filter((agent) => agent.wired);
  return (wired.length ? wired : native.filter((agent) => agent.detected)).map((agent) => agent.id);
}

function configValue(doc: Record<string, unknown>, key: string): unknown {
  let value: unknown = doc;
  for (const part of key.split(".")) value = objectOf(value)[part];
  return value === undefined ? moduleHost().configDefault(key) : value;
}

// Module state is the authority for the keys a module owns: an on module whose
// key sits at its off value, or an off module whose key does not, is rewritten.
function configChanges(selection: ModuleSelection) {
  const doc = moduleHost().readConfig();
  const stored = storedModules();
  const state = MODULES.filter((m) => stored[m.id] !== selection[m.id]).map((m) => [m.id, selection[m.id]] as const);
  const effects: (readonly [string, string | boolean])[] = [];
  for (const m of MODULES) {
    for (const effect of m.capabilities) {
      const value = configValue(doc, effect.key);
      if (selection[m.id] ? value === effect.off : value !== effect.off) effects.push([effect.key, selection[m.id] ? effect.on : effect.off]);
    }
  }
  return { state, effects };
}

// `agents` is the set to have wired while any agent-wired module is on: missing
// ones are wired, wired ones outside it are unwired. With none on, every wired
// agent is unwired.
function wiringChanges(selection: ModuleSelection, agents: string[]) {
  const want = MODULES.some((m) => m.wiresAgents && selection[m.id]);
  const native = moduleHost().nativeAgents();
  const wired = native.filter((agent) => agent.wired).map((agent) => agent.id);
  const supported = agents.filter((agent) => native.some((info) => info.id === agent));
  return {
    wire: want ? supported.filter((agent) => !wired.includes(agent)) : [],
    unwire: wired.filter((agent) => !want || !supported.includes(agent)),
  };
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

// The install/uninstall an external module needs. Without a status answer,
// install runs when the module is switched on (or first recorded on).
function externalRuns(selection: ModuleSelection): { def: ModuleDef; args: string[] }[] {
  const current = currentSelection();
  const stored = storedModules();
  const runs: { def: ModuleDef; args: string[] }[] = [];
  for (const def of MODULES) {
    if (!def.external) continue;
    const bin = externalBin(def.external.binary);
    if (selection[def.id]) {
      const status = bin ? externalStatus(def, bin) : undefined;
      const needed = status ? Object.values(status).some((installed) => !installed) : stored[def.id] !== true;
      if (needed) runs.push({ def, args: def.external.install });
    } else if (current[def.id] && bin) {
      runs.push({ def, args: def.external.uninstall });
    }
  }
  return runs;
}

export async function planModules(selection: ModuleSelection, agents: string[]): Promise<ModulePlan> {
  const h = moduleHost();
  const lines: PlanLine[] = [];
  const binaries = [...new Set(MODULES.filter((m) => selection[m.id]).flatMap((m) => m.binaries))];
  const missing = binaries.filter((name) => !h.resolveBinary(name));
  if (missing.length) lines.push({ action: "DOWNLOAD", target: missing.join(", "), detail: `signed, ${h.binaryRelease}` });

  const { state, effects } = configChanges(selection);
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

  const { wire, unwire } = wiringChanges(selection, agents);
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

  for (const run of externalRuns(selection)) {
    lines.push({ action: "RUN", target: `${run.def.external!.binary} ${run.args.join(" ")}`, detail: "" });
  }
  return { selection: { ...selection }, agents: [...agents], lines };
}

export async function applyModules(plan: ModulePlan, opts: { yes: boolean }): Promise<{ ok: boolean; problems: string[] }> {
  const h = moduleHost();
  if (plan.lines.length === 0) return { ok: true, problems: [] };
  if (!opts.yes) {
    if (!h.interactive()) return { ok: false, problems: ["nothing changed: pass --yes to apply without a prompt"] };
    if (!(await h.confirm("Continue? [y/N]"))) return { ok: false, problems: ["nothing changed"] };
  }
  const problems: string[] = [];
  const fail = (what: string, error: unknown) => problems.push(`${what}: ${error instanceof Error ? error.message : String(error)}`);
  if (plan.lines.some((line) => line.action === "DOWNLOAD")) {
    try { await h.installBinaries(); } catch (error) { fail("download", error); }
  }
  // Decide everything before the first config write: the external uninstall
  // reads the module state this run replaces.
  const { state, effects } = configChanges(plan.selection);
  const { wire, unwire } = wiringChanges(plan.selection, plan.agents);
  const runs = externalRuns(plan.selection);

  if (state.length) h.mutateConfig((out) => { out.modules = { ...objectOf(out.modules), ...Object.fromEntries(state) }; });
  // Effects land before wiring: enable reads think.shrink when it writes hooks.
  for (const [key, value] of effects) h.setConfigValue(key, value);
  for (const agent of unwire) {
    try { h.unwireAgent(agent); } catch (error) { fail(agent, error); }
  }
  for (const agent of wire) {
    try { h.wireAgent(agent); } catch (error) { fail(agent, error); }
  }
  for (const run of runs) {
    const name = run.def.external!.binary;
    const bin = externalBin(name);
    // Absent: the module stays on but inactive, and status says why.
    if (!bin) continue;
    const out = runExternal(bin, run.args, false);
    if (out.status !== 0) problems.push(`${name} ${run.args.join(" ")} failed${out.error ? `: ${out.error.message}` : ""}`);
  }
  return { ok: problems.length === 0, problems };
}

function externalAgentState(status: Record<string, boolean> | undefined, agent: string): "wired" | "not wired" | "n/a" {
  // Blocks names Claude Code "claude-code"; the hub calls it "claude".
  const installed = status?.[agent] ?? status?.[`${agent}-code`];
  return installed === undefined ? "n/a" : installed ? "wired" : "not wired";
}

export async function moduleStates(): Promise<ModuleState[]> {
  const h = moduleHost();
  const selection = currentSelection();
  const doc = h.readConfig();
  const agents = h.nativeAgents().filter((agent) => agent.detected || agent.wired);
  const signedIn = h.signedIn();
  return MODULES.map((m) => {
    const on = selection[m.id];
    const bin = m.external ? externalBin(m.external.binary) : null;
    const status = on && m.external && bin ? externalStatus(m, bin) : undefined;
    const missing = m.binaries.filter((name) => !h.resolveBinary(name));
    const drift = m.capabilities.find((effect) => configValue(doc, effect.key) === effect.off);
    const reason = !on ? undefined
      : m.needsSignIn ? signedIn ? `waiting for Cloud ${m.id}` : `sign in to turn on ${m.id}`
      : m.external && !bin ? `${m.external.binary} not installed`
      : missing.length ? `${missing.join(", ")} not installed`
      : drift ? `${drift.key} is ${drift.off} in config`
      // Core is withheld in record mode, which is input switched off.
      : m.id === "output" && !h.coreActive() ? "record mode keeps it off"
      : undefined;
    const perAgent = Object.fromEntries(agents.map((agent) => [
      agent.id,
      m.wiresAgents ? agent.wired ? "wired" : "not wired" : externalAgentState(status, agent.id),
    ])) as ModuleState["perAgent"];
    return { id: m.id, on, active: on && !reason, ...(reason ? { reason } : {}), perAgent };
  });
}

export function renderPlan(plan: ModulePlan): string {
  const width = Math.min(32, Math.max(...plan.lines.map((line) => line.target.length)));
  return `This will\n${plan.lines.map((line) => `  ${line.action.padEnd(9)} ${line.target.padEnd(width)}  ${line.detail}`.trimEnd()).join("\n")}\n`;
}

export async function moduleSwitchCommand(on: boolean, argv: string[]): Promise<void> {
  const verb = on ? "on" : "off";
  const flags = argv.filter((arg) => arg.startsWith("-"));
  const names = argv.filter((arg) => !arg.startsWith("-"));
  const all = flags.includes("--all");
  const ids = MODULES.map((m) => m.id).join(", ");
  if (flags.some((flag) => !["--all", "--yes", "-y", "--dry-run"].includes(flag)) || all === names.length > 0) {
    console.error(`usage: caveman ${verb} <module…> | --all  [--yes] [--dry-run]\nmodules: ${ids}`);
    process.exit(2);
  }
  const unknown = names.filter((name) => !findModule(name));
  if (unknown.length) {
    console.error(`unknown module: ${unknown.join(", ")} · modules: ${ids}`);
    process.exit(2);
  }
  const selection = currentSelection();
  for (const m of MODULES) if (all || names.includes(m.id)) selection[m.id] = on;
  // `off` never wires an agent that is not wired yet.
  const agents = on ? defaultAgents() : moduleHost().nativeAgents().filter((agent) => agent.wired).map((agent) => agent.id);
  const plan = await planModules(selection, agents);
  const label = all ? "every module" : names.join(", ");
  if (plan.lines.length === 0) {
    console.log(`✓ ${label} already ${verb}`);
    return;
  }
  process.stdout.write(renderPlan(plan));
  if (flags.includes("--dry-run")) return;
  const result = await applyModules(plan, { yes: flags.includes("--yes") || flags.includes("-y") });
  if (!result.ok) {
    for (const problem of result.problems) console.error(`✗ ${problem}`);
    process.exitCode = 1;
    return;
  }
  console.log(`✓ ${label} ${verb}`);
  if (on && selection.routing && !moduleHost().signedIn()) console.log("routing starts after sign-in: caveman login");
}
