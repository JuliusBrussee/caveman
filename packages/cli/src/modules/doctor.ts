// Bare `caveman doctor`: local checks first (modules, then wired agents), one
// failure per line with its fix. Cloud is checked only when signed in, after
// the local lines are out, so a Cloud failure never hides them.
import { moduleHost, moduleStates } from "./apply.js";
import { findModule } from "./registry.js";
import { moduleFix } from "./status.js";

export async function modulesDoctor(): Promise<void> {
  const h = moduleHost();
  const failures: string[] = [];
  const notes: string[] = [];
  const states = await moduleStates();
  for (const state of states) {
    if (!state.on || state.active) continue;
    const fix = moduleFix(state);
    // Sign-in and Cloud readiness are states the user chose, not breakage.
    if (findModule(state.id)?.needsSignIn) notes.push(`· ${state.id}: ${state.reason}${fix ? ` · ${fix}` : ""}`);
    else if (findModule(state.id)?.external) failures.push(`${state.id}: ${state.reason} · fix: install it, or caveman off ${state.id}`);
    else failures.push(`${state.id}: ${state.reason}${fix ? ` · fix: ${fix}` : ""}`);
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
