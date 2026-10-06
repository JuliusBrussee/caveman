// STUB: lane A owns this file; replaced at merge.
// Implements only the V4-SPEC contract shape so lane C (onboarding) compiles
// and its tests run: module state is the `modules` key of the cloud config.
import { readFileSync, renameSync, writeFileSync, mkdirSync } from "node:fs";
import { dirname } from "node:path";

import { cloudConfigPath } from "./config-home.js";
import { MODULES, type ModuleId } from "./registry.js";

export type ModuleSelection = Record<ModuleId, boolean>;
export type PlanLine = { action: "CREATE" | "UPDATE" | "DOWNLOAD" | "RUN"; target: string; detail: string };
export type ModulePlan = { selection: ModuleSelection; agents: string[]; lines: PlanLine[] };
export type ModuleState = { id: ModuleId; on: boolean; active: boolean; reason?: string; perAgent: Record<string, "wired" | "not wired" | "n/a"> };

function readConfig(): Record<string, unknown> {
  try {
    const parsed = JSON.parse(readFileSync(cloudConfigPath(), "utf8")) as unknown;
    return parsed && typeof parsed === "object" && !Array.isArray(parsed) ? parsed as Record<string, unknown> : {};
  } catch {
    return {};
  }
}

export function currentSelection(): ModuleSelection {
  const stored = readConfig().modules as Record<string, unknown> | undefined;
  return Object.fromEntries(MODULES.map((m) => [m.id, typeof stored?.[m.id] === "boolean" ? stored[m.id] : m.defaultOn])) as ModuleSelection;
}

export async function planModules(selection: ModuleSelection, agents: string[]): Promise<ModulePlan> {
  return { selection, agents, lines: [{ action: "UPDATE", target: cloudConfigPath(), detail: "modules" }] };
}

export async function applyModules(plan: ModulePlan, _opts: { yes: boolean }): Promise<{ ok: boolean; problems: string[] }> {
  const config = readConfig();
  config.modules = plan.selection;
  const path = cloudConfigPath();
  mkdirSync(dirname(path), { recursive: true, mode: 0o700 });
  writeFileSync(`${path}.${process.pid}.tmp`, JSON.stringify(config, null, 2), { mode: 0o600 });
  renameSync(`${path}.${process.pid}.tmp`, path);
  return { ok: true, problems: [] };
}

export async function moduleStates(): Promise<ModuleState[]> {
  const selection = currentSelection();
  return MODULES.map((m) => ({ id: m.id, on: selection[m.id], active: selection[m.id] && !m.needsSignIn, perAgent: {} }));
}
