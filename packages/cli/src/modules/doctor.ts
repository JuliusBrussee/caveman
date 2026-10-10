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
    else if (findModule(state.id)?.external) failures.push(`${state.id}: ${state.reason}${state.reason?.endsWith(" not installed") ? " · fix: install it" : ""}, or caveman off ${state.id}`);
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
  const stale = h.staleBinaries();
  if (stale.length) failures.push(`${stale.join(", ")} ${stale.length > 1 ? "are" : "is"} out of date · fix: caveman setup --install`);
  for (const runtime of await h.localRuntimes()) {
    if (runtime.foreign) failures.push(`${runtime.host}:${runtime.port} is held by another program · fix: stop it, then caveman start`);
    // A runtime keeps the binary it started from until it restarts.
    else if (runtime.stale) failures.push(`${runtime.host}:${runtime.port} still runs caveman-proxy ${runtime.stale.running}; ${runtime.stale.installed} is installed · fix: caveman stop, then start your agent again`);
  }
  const wired = h.nativeAgents().filter((agent) => agent.wired);
  for (const agent of wired) {
    const state = h.agentState(agent.id);
    if (state === "degraded") failures.push(`${agent.id}: wiring degraded · fix: caveman doctor ${agent.id} --fix`);
    else if (state === "unavailable") failures.push(`${agent.id}: wired but not runnable · fix: reinstall ${agent.id}, or caveman disable ${agent.id}`);
  }
  const traffic = h.agentTraffic();
  if (traffic.fix) notes.push(`· ${traffic.line} · ${traffic.fix}`);
  for (const failure of failures) console.log(`✗ ${failure}`);
  for (const note of notes) console.log(note);

  let cloudFailed = false;
  if (h.signedIn()) {
    try {
      await h.cloudCheck();
    } catch (error) {
      const message = error instanceof Error ? error.message : String(error);
      const status = (error as { status?: unknown }).status;
      // Down, slow or erroring (no answer, 5xx): signing in again fixes none of it.
      if (status === 0 || (typeof status === "number" && status >= 500)) console.log(`· cloud: ${message} · try again later`);
      else {
        cloudFailed = true;
        console.log(`✗ cloud: ${message} · fix: caveman login`);
      }
    }
  }
  if (failures.length || cloudFailed) {
    process.exitCode = 1;
    return;
  }
  console.log(`✓ healthy · ${states.filter((state) => state.on).length} modules on · ${wired.length} agents wired`);
}
