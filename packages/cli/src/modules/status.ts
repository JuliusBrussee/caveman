// `caveman status`: one row per module, one column per detected agent, then
// one next step.
import type { ModuleState } from "./apply.js";
import { findModule, type ModuleId } from "./registry.js";

// The command that clears an on-but-inactive module, when there is one.
export function moduleFix(state: ModuleState): string | undefined {
  const reason = state.reason ?? "";
  if (reason.startsWith("sign in")) return "caveman login";
  if (reason === "record mode keeps it off") return "caveman on input";
  if (reason.endsWith(" in config")) return `caveman on ${state.id}`;
  if (reason.endsWith(" not installed") && !findModule(state.id)?.external) return "caveman setup --install";
  return undefined;
}

export function nextStep(states: ModuleState[], opts: { degraded?: string | undefined; fallback: string | null }): string | null {
  if (states.some((state) => state.on && moduleFix(state) === "caveman setup --install")) return "caveman setup --install";
  if (opts.degraded) return `caveman doctor ${opts.degraded} --fix`;
  const wiredOn = states.filter((state) => state.on && findModule(state.id)?.wiresAgents);
  const unwired = Object.keys(wiredOn[0]?.perAgent ?? {}).filter((agent) => wiredOn[0]!.perAgent[agent] === "not wired");
  if (unwired.length) return `caveman enable ${unwired.length > 1 ? "--detected" : unwired[0]}`;
  const login = states.find((state) => state.on && moduleFix(state) === "caveman login");
  if (login) return "caveman login";
  return opts.fallback;
}

// `degraded` names wired agents whose wiring no longer checks out; their cells
// say so instead of "wired".
export function renderModuleGrid(states: ModuleState[], notes: Partial<Record<ModuleId, string>>, next: string | null, degraded: string[] = []): string {
  const agents = Object.keys(states[0]?.perAgent ?? {});
  const width = Math.max(9, ...agents.map((agent) => agent.length)) + 2;
  const row = (head: string, cells: string[], note: string) => `${head}${cells.map((cell) => cell.padEnd(width)).join("")}${note}`.trimEnd();
  const lines = ["caveman status", row(" ".repeat(19), agents, "")];
  for (const state of states) {
    const notApplicable = Object.values(state.perAgent).every((cell) => cell === "n/a");
    const cells = !state.on ? agents.map(() => "")
      : !state.active ? agents.map(() => "—")
      : notApplicable ? ["✓", ...agents.slice(1).map(() => "")]
      : agents.map((agent) => state.perAgent[agent] === "n/a" ? ""
        : state.perAgent[agent] === "wired" && findModule(state.id)?.wiresAgents && degraded.includes(agent) ? "degraded"
        : state.perAgent[agent]!);
    const fix = moduleFix(state);
    const note = !state.on ? `caveman on ${state.id}`
      : !state.active ? `${state.reason}${fix ? ` · ${fix}` : ""}`
      : notes[state.id] ?? "";
    lines.push(row(`  ${state.on ? "on " : "off"} ${state.id.padEnd(13)}`, cells, note));
  }
  if (next) lines.push(`next: ${next}`);
  return `${lines.join("\n")}\n`;
}
