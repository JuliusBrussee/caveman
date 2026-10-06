// Bare `caveman doctor`: local checks first (modules, config, binaries, the
// runtime port, wired agents), one failure per line with its fix. States the
// user chose print as notes and do not fail. Cloud is checked only when signed
// in, after the local lines are out, so a Cloud failure never hides them.
import { currentSelection, moduleHost, moduleStates } from "./apply.js";
import { findModule, MODULES } from "./registry.js";
import { moduleChoice, moduleFix } from "./status.js";

export async function modulesDoctor(): Promise<void> {
  const h = moduleHost();
  const failures: string[] = [];
  const notes: string[] = [];
  const states = await moduleStates();
  for (const state of states) {
    if (!state.on || state.active || state.reason?.includes(" has an invalid value: ")) continue;
    const fix = moduleFix(state);
    if (moduleChoice(state)) notes.push(`· ${state.id}: ${state.reason}${fix ? ` · ${fix}` : ""}`);
    else if (findModule(state.id)?.external) failures.push(`${state.id}: ${state.reason} · fix: install it, or caveman off ${state.id}`);
    else failures.push(`${state.id}: ${state.reason}${fix ? ` · fix: ${fix}` : ""}`);
  }
  // Invalid values fail whether their module is on or off.
  const selection = currentSelection();
  for (const m of MODULES) {
    for (const effect of m.capabilities) {
      const invalid = h.capability(effect.key).invalid;
      if (invalid !== undefined) failures.push(`${effect.key} has an invalid value: ${invalid} · fix: caveman ${selection[m.id] ? "on" : "off"} ${m.id}`);
    }
  }
  for (const name of h.staleBinaries()) failures.push(`${name} is out of date · fix: caveman setup --install`);
  for (const runtime of await h.localRuntimes()) {
    if (runtime.foreign) failures.push(`${runtime.host}:${runtime.port} is held by another program · fix: stop it, then caveman start`);
  }
  const wired = h.nativeAgents().filter((agent) => agent.wired);
  for (const agent of wired) {
    const state = h.agentState(agent.id);
    if (state === "degraded") failures.push(`${agent.id}: wiring degraded · fix: caveman doctor ${agent.id} --fix`);
    else if (state === "unavailable") failures.push(`${agent.id}: wired but not runnable · fix: reinstall ${agent.id}, or caveman disable ${agent.id}`);
  }
  for (const failure of failures) console.log(`✗ ${failure}`);
  for (const note of notes) console.log(note);

  let cloudFailed = false;
  if (h.signedIn()) {
    try {
      await h.cloudCheck();
    } catch (error) {
      cloudFailed = true;
      console.log(`✗ cloud: ${error instanceof Error ? error.message : String(error)} · fix: caveman login`);
    }
  }
  if (failures.length || cloudFailed) {
    process.exitCode = 1;
    return;
  }
  console.log(`✓ healthy · ${states.filter((state) => state.on).length} modules on · ${wired.length} agents wired`);
}
