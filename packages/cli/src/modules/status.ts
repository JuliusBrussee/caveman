// `caveman status`: one row per module, one column per detected agent, then
// one next step.
import type { ModuleState } from "./apply.js";
import { findModule, type ModuleId } from "./registry.js";

// The command that clears an on-but-inactive module, when there is one.
export function moduleFix(state: ModuleState): string | undefined {
  const reason = state.reason ?? "";
  if (reason === "not set up") return "caveman setup";
  if (reason.startsWith("sign in") || reason === "login expired") return "caveman login";
  if (reason.startsWith(`${state.id} paused · `)) return "caveman billing";
  if (reason.startsWith(`${state.id} degraded · `)) return "caveman login";
  if (reason === "paused while input is off") return "caveman on input";
  if (reason.endsWith(" in config") || reason.includes(" has an invalid value: ")) return `caveman on ${state.id}`;
  if (reason.endsWith(" not installed") && !findModule(state.id)?.external) return "caveman setup --install";
  return undefined;
}

// An inactive module the user's own choices explain (not signed in, an
// override, input off, setup not run yet) rather than something broken.
export function moduleChoice(state: ModuleState): boolean {
  const reason = state.reason ?? "";
  return ["not set up", "sign in", "waiting for Cloud", "overridden by", "paused while", "record mode", `${state.id} paused · `].some((start) => reason.startsWith(start))
    || reason.endsWith(" in config");
}

export function nextStep(states: ModuleState[], opts: { degraded?: string | undefined; fallback: string | null }): string | null {
  const inactive = states.filter((state) => state.on && !state.active && moduleFix(state));
  if (inactive.some((state) => moduleFix(state) === "caveman setup --install")) return "caveman setup --install";
  const broken = inactive.find((state) => !moduleChoice(state));
  if (broken) return moduleFix(broken)!;
  if (opts.degraded) return `caveman doctor ${opts.degraded} --fix`;
  const wiredOn = states.filter((state) => state.on && findModule(state.id)?.wiresAgents);
  const unwired = Object.keys(wiredOn[0]?.perAgent ?? {}).filter((agent) => wiredOn[0]!.perAgent[agent] === "not wired");
  if (unwired.length) return `caveman enable ${unwired.length > 1 ? "--detected" : unwired[0]}`;
  if (inactive[0]) return moduleFix(inactive[0])!;
  return opts.fallback;
}

// `degraded` names wired agents whose wiring no longer checks out; their cells
// say so instead of "wired". `lines` print under the grid, before next.
export function renderModuleGrid(
  states: ModuleState[],
  opts: { notes?: Partial<Record<ModuleId, string>>; next: string | null; degraded?: string[]; lines?: string[] },
): string {
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
        : state.perAgent[agent] === "wired" && findModule(state.id)?.wiresAgents && opts.degraded?.includes(agent) ? "degraded"
        : state.perAgent[agent]!);
    const fix = moduleFix(state);
    const note = !state.on ? `caveman on ${state.id}`
      : !state.active ? `${state.reason}${fix ? ` · ${fix}` : ""}`
      : opts.notes?.[state.id] ?? "";
    lines.push(row(`  ${state.on ? "on " : "off"} ${state.id.padEnd(13)}`, cells, note));
  }
  lines.push(...(opts.lines ?? []));
  if (opts.next) lines.push(`next: ${opts.next}`);
  return `${lines.join("\n")}\n`;
}
