// First run: pick modules and agents, see the plan, confirm once, apply, then
// sign in only when routing is on. `caveman setup`, bare `caveman` before any
// setup, `npx caveman`, the end of install.sh and `caveman <agent>` before any
// setup all land here. Nothing is written before Continue.
import { readFileSync } from "node:fs";
import { emitKeypressEvents } from "node:readline";

import { applyModules, currentSelection, planModules, renderPlan, type ModulePlan, type ModuleSelection } from "./apply.js";
import { cloudConfigPath } from "./config-home.js";
import { MODULES, findModule, type ModuleId } from "./registry.js";

// `wired`: Caveman already routes this agent. A wired agent stays ticked even
// off PATH, because the plan unwires every agent left out of the list.
export type OnboardAgent = { id: string; name: string; installed: boolean; wired: boolean; version?: string };
export type OnboardOptions = { yes: boolean; dryRun: boolean; only?: ModuleId[]; skip?: ModuleId[]; agents?: string[] };
export type SignInUi = { signal: AbortSignal; code(url: string, userCode: string, opened: boolean): void };
export type OnboardDeps = {
  cmd: string;
  agents: OnboardAgent[];
  interactive: boolean;
  signedIn(): Promise<boolean>;
  // Throws on failure; an error with code "sign_in_closed" means the Cloud
  // does not accept sign-ins yet.
  signIn(ui: SignInUi): Promise<{ email?: string }>;
  discloseTelemetry(): Promise<void>;
  markFirstRun(): Promise<void>;
  // A No is remembered: the agent door and bare `caveman` stop asking, and
  // `caveman setup` is the way back.
  markDeclined(): void;
  // Set when `caveman <agent>` continues into the agent after setup.
  launching?: string;
  input?: NodeJS.ReadStream;
  output?: NodeJS.WriteStream;
};
export type OnboardResult = { confirmed: boolean; cancelled: boolean; ok: boolean; plan?: ModulePlan };

const LEGACY_SETUP_FLAGS = new Set(["--json", "--install", "--remove", "--agent-native"]);
export const ONBOARD_USAGE = "setup [--yes] [--dry-run] [--only a,b] [--skip a,b] [--agents a,b]";

// parseOnboardArgs returns undefined when the argv belongs to the older setup
// verbs (--install, --json, --agent-native), which keep their own handler.
export function parseOnboardArgs(argv: string[]): OnboardOptions | { error: string } | undefined {
  if (argv.some((arg) => LEGACY_SETUP_FLAGS.has(arg.split("=", 1)[0]!))) return undefined;
  const opts: OnboardOptions = { yes: false, dryRun: false };
  for (let i = 0; i < argv.length; i++) {
    const arg = argv[i]!;
    if (arg === "--yes" || arg === "-y") { opts.yes = true; continue; }
    if (arg === "--dry-run") { opts.dryRun = true; continue; }
    const flag = arg.split("=", 1)[0]!;
    if (flag !== "--only" && flag !== "--skip" && flag !== "--agents") return { error: `unknown flag ${arg}` };
    const value = arg.includes("=") ? arg.slice(flag.length + 1) : argv[++i];
    if (!value || value.startsWith("-")) return { error: `${flag} needs a comma-separated list` };
    const ids = value.split(",").map((id) => id.trim()).filter(Boolean);
    if (flag === "--agents") { opts.agents = ids; continue; }
    const unknown = ids.filter((id) => !findModule(id));
    if (unknown.length) return { error: `unknown module ${unknown.join(", ")} · modules: ${MODULES.map((m) => m.id).join(", ")}` };
    opts[flag === "--only" ? "only" : "skip"] = ids as ModuleId[];
  }
  return opts;
}

function storedConfig(): Record<string, unknown> {
  try {
    const parsed = JSON.parse(readFileSync(cloudConfigPath(), "utf8")) as unknown;
    return parsed && typeof parsed === "object" && !Array.isArray(parsed) ? parsed as Record<string, unknown> : {};
  } catch {
    return {};
  }
}

// Setup has run once the module state exists in the cloud config.
export function setupRan(): boolean {
  const modules = storedConfig().modules;
  return Boolean(modules) && typeof modules === "object" && !Array.isArray(modules);
}

export function setupDeclined(): boolean {
  return typeof storedConfig().setupDeclinedAt === "string";
}

export function onboardInteractive(): boolean {
  const ci = process.env.CI;
  return Boolean(process.stdin.isTTY && process.stdout.isTTY)
    && !(ci && ci !== "0" && ci.toLowerCase() !== "false")
    && process.env.TERM !== "dumb";
}

export async function onboard(opts: OnboardOptions, deps: OnboardDeps): Promise<OnboardResult> {
  const out = deps.output ?? process.stdout;
  const input = deps.input ?? process.stdin;
  const c = colors(out);
  const ask = deps.interactive && !opts.yes && !opts.dryRun;
  const byId = new Map(deps.agents.map((agent) => [agent.id, agent]));
  const usable = (agent: OnboardAgent) => agent.installed || agent.wired;
  for (const id of opts.agents ?? []) {
    const agent = byId.get(id);
    if (!agent || !usable(agent)) {
      throw new Error(`--agents: ${id} is not installed here · found: ${deps.agents.filter(usable).map((a) => a.id).join(", ") || "none"}`);
    }
  }

  out.write(`${c.bold("caveman")} ${c.dim("· make your coding agent cheaper")}\n\n${foundLine(deps.agents)}\n\n`);
  let selection = initialSelection(opts);
  // Re-runs keep what is wired; a first run takes what is installed. --agents
  // adds to the wired ones: unwiring is `caveman off` or unticking here.
  const wired = deps.agents.filter((agent) => agent.wired).map((agent) => agent.id);
  let agents = [...new Set([...wired, ...(opts.agents ?? (wired.length ? [] : deps.agents.filter((agent) => agent.installed).map((agent) => agent.id)))])];
  if (ask) {
    const modules = await toggle(input, out, c, `Modules ${c.dim("· space toggles, enter continues")}`, "column",
      MODULES.map((m) => ({ label: m.title, hint: m.needsSignIn ? `${m.summary} · free account` : m.summary, on: selection[m.id] })));
    if (!modules) return cancelled(out, c);
    selection = Object.fromEntries(MODULES.map((m, i) => [m.id, modules[i]!])) as ModuleSelection;
    out.write("\n");
    if (deps.agents.length > 0) {
      const shown = [...deps.agents.filter(usable), ...deps.agents.filter((a) => !usable(a))];
      const picked = await toggle(input, out, c, "Agents", "row",
        shown.map((a) => ({ label: a.name, on: agents.includes(a.id), disabled: !usable(a) })));
      if (!picked) return cancelled(out, c);
      agents = shown.filter((_, i) => picked[i]).map((a) => a.id);
      out.write("\n");
    }
  } else {
    out.write(`Modules  ${MODULES.filter((m) => selection[m.id]).map((m) => m.title).join(" · ") || "none"}\n`);
    out.write(`Agents   ${agents.map((id) => byId.get(id)!.name).join(" · ") || "none"}\n\n`);
  }

  const plan = await planModules(selection, agents);
  out.write(plan.lines.length ? renderPlan(plan) : `This will\n  ${c.dim("change nothing")}\n`);
  if (opts.dryRun) {
    out.write(`${c.dim("Dry run: nothing was written.")}\n`);
    return { confirmed: false, cancelled: false, ok: true, plan };
  }
  // Without a terminal to ask in (CI counts), only --yes applies.
  if (!deps.interactive && !opts.yes) {
    out.write(`Nothing changed: pass --yes to apply · ${deps.cmd} setup --yes\n`);
    return { confirmed: false, cancelled: false, ok: true, plan };
  }
  if (ask) {
    const yes = await confirm(input, out, c, "Continue?");
    if (yes === null) return cancelled(out, c);
    await deps.markFirstRun();
    if (!yes) {
      deps.markDeclined();
      const session = deps.launching ? ` · ${deps.launching} runs this session only` : "";
      out.write(`${c.dim(`Nothing changed${session} · ${deps.cmd} setup when you want it`)}\n`);
      return { confirmed: false, cancelled: false, ok: true, plan };
    }
  }

  const result = await applyModules(plan, { yes: true });
  for (const problem of result.problems) out.write(`${c.red("✗")} ${problem}\n`);
  out.write("\n");
  if (selection.routing) await routingStep(opts, deps, input, out, c);
  if (!result.ok) {
    out.write(`${c.red("✗")} Setup finished with problems. Fix them, then run ${deps.cmd} setup again.\n`);
  } else if (deps.launching) {
    out.write(`${c.green("✓")} Ready. Starting ${deps.launching}.\n`);
  } else {
    out.write(`${c.green("✓")} Ready. Try:  ${c.cyan(`${deps.cmd} ${agents[0] ?? "claude"}`)}      See it:  ${c.cyan(`${deps.cmd} status`)}\n`);
  }
  if (ask) await deps.discloseTelemetry();
  return { confirmed: true, cancelled: false, ok: result.ok, plan };
}

// currentSelection is the registry defaults on a fresh home, the stored state on
// a re-run, and off for a module whose key an older config already switched off.
function initialSelection(opts: OnboardOptions): ModuleSelection {
  const selection = currentSelection();
  if (opts.only) for (const m of MODULES) selection[m.id] = opts.only.includes(m.id);
  for (const id of opts.skip ?? []) selection[id] = false;
  return selection;
}

// Routing stays on whatever happens here; without an account it waits, and
// `caveman login` finishes it later.
async function routingStep(opts: OnboardOptions, deps: OnboardDeps, input: NodeJS.ReadStream, out: NodeJS.WriteStream, c: Colors) {
  if (await deps.signedIn()) return;
  const waits = (why: string) => out.write(`${c.yellow("○")} routing is on and ${why} · ${c.cyan(`${deps.cmd} login`)}\n\n`);
  if (!deps.interactive || opts.yes) return waits("starts after you sign in");
  out.write("Routing needs a free Caveman account.\n");
  const skip = new AbortController();
  const stop = keys(input, (key) => {
    if (key.name === "escape" || key.name === "s" || key.name === "q" || (key.ctrl && key.name === "c")) skip.abort();
  });
  try {
    const { email } = await deps.signIn({
      signal: skip.signal,
      code(url, userCode, opened) {
        out.write(`  Open ${url} and enter ${c.bold(userCode)}   ${c.dim(opened ? "(browser opened · esc skips)" : "(esc skips)")}\n`);
      },
    });
    out.write(`  ${c.green("✓")} signed in${email ? ` as ${email}` : ""}\n\n`);
  } catch (error) {
    if (skip.signal.aborted) return waits("starts after you sign in");
    const message = error instanceof Error ? error.message : String(error);
    if ((error as { code?: unknown }).code === "sign_in_closed") {
      out.write(`  ${c.yellow("!")} ${message}\n`);
      return waits("starts once sign-in opens");
    }
    out.write(`  ${c.red("✗")} sign-in failed: ${message}\n`);
    return waits("starts after you sign in");
  } finally {
    stop();
  }
}

function foundLine(agents: OnboardAgent[]): string {
  const found = agents.filter((a) => a.installed).map((a) => {
    const version = a.version?.match(/\d+\.\d+/)?.[0];
    return version ? `${a.name} ${version}` : a.name;
  });
  if (found.length === 0) return "No coding agents found on this machine";
  return `Found ${found.length === 1 ? found[0] : `${found.slice(0, -1).join(", ")} and ${found.at(-1)}`}`;
}

function cancelled(out: NodeJS.WriteStream, c: Colors): OnboardResult {
  out.write(`${c.dim("Cancelled. Nothing changed.")}\n`);
  return { confirmed: false, cancelled: true, ok: true };
}

// ── Prompts ─────────────────────────────────────────────────────────────────
// Zero-dependency, redrawn in place. Each frame has no trailing newline, so a
// redraw rewinds exactly to its first row; rows are clipped to the terminal
// width so none wraps.

type Colors = ReturnType<typeof colors>;
type Key = { name?: string; ctrl?: boolean; shift?: boolean };
type Item = { label: string; hint?: string; on: boolean; disabled?: boolean };

function colors(out: NodeJS.WriteStream) {
  const on = Boolean(out.isTTY) && !process.env.NO_COLOR;
  const paint = (code: string) => (s: string) => on ? `\x1b[${code}m${s}\x1b[0m` : s;
  return { bold: paint("1"), dim: paint("2"), cyan: paint("36"), green: paint("32"), yellow: paint("33"), red: paint("31") };
}

// Keys typed before a prompt is on screen (an Enter hit twice while the plan
// was computed) are dropped, so nothing is ever answered unseen.
function keys(input: NodeJS.ReadStream, onKey: (key: Key) => void): () => void {
  emitKeypressEvents(input);
  while (input.read() !== null);
  input.setRawMode?.(true);
  input.resume();
  const handler = (_: string | undefined, key: Key | undefined) => onKey(key ?? {});
  input.on("keypress", handler);
  return () => {
    input.off("keypress", handler);
    input.setRawMode?.(false);
    input.pause();
  };
}

function live(out: NodeJS.WriteStream, render: (active: boolean) => string[], onKey: (key: Key, done: () => void) => void, input: NodeJS.ReadStream): Promise<boolean> {
  return new Promise((resolve) => {
    let rows = 0;
    const draw = (active: boolean) => {
      const lines = render(active);
      out.write(`${rows > 1 ? `\r\x1b[${rows - 1}A` : "\r"}\x1b[J${lines.join("\n")}`);
      rows = lines.length;
    };
    let finished = false;
    // The cursor comes back however the process ends, a kill included.
    const restore = () => out.write("\x1b[?25h");
    const onSignal = (signal: NodeJS.Signals) => {
      restore();
      input.setRawMode?.(false);
      process.kill(process.pid, signal);
    };
    process.once("exit", restore);
    process.once("SIGTERM", onSignal);
    process.once("SIGHUP", onSignal);
    out.write("\x1b[?25l");
    draw(true);
    const finish = (ok: boolean) => {
      finished = true;
      process.off("exit", restore);
      process.off("SIGTERM", onSignal);
      process.off("SIGHUP", onSignal);
      stop();
      draw(false);
      out.write("\n\x1b[?25h");
      resolve(ok);
    };
    const stop = keys(input, (key) => {
      if (finished) return;
      if (key.name === "escape" || (key.ctrl && key.name === "c")) return finish(false);
      onKey(key, () => finish(true));
      if (!finished) draw(true);
    });
  });
}

function toggle(input: NodeJS.ReadStream, out: NodeJS.WriteStream, c: Colors, title: string, layout: "column" | "row", items: Item[]): Promise<boolean[] | null> {
  const enabled = items.map((item, i) => item.disabled ? -1 : i).filter((i) => i >= 0);
  let at = 0;
  const width = () => Math.max(20, (out.columns || 80) - 1);
  const labelWidth = Math.max(0, ...items.map((item) => item.label.length)) + 3;
  const box = (item: Item) => item.on ? "◼" : "◻";
  const render = (active: boolean): string[] => {
    const current = active ? enabled[at] : -1;
    if (layout === "column") {
      return [title, ...items.map((item, i) => {
        const label = item.label.padEnd(labelWidth);
        const hint = (item.hint ?? "").slice(0, Math.max(0, width() - 3 - labelWidth));
        return `${i === current ? c.cyan("›") : " "}${box(item)} ${i === current ? c.cyan(label) : label}${c.dim(hint)}`;
      })];
    }
    // Agents that are not installed share one dimmed entry at the end.
    const missing = items.filter((item) => item.disabled).map((item) => item.label);
    const cells = items.flatMap((item, i) => item.disabled ? [] : [{ text: `${box(item)} ${item.label}`, hot: i === current }]);
    if (missing.length) cells.push({ text: `◻ ${missing.join(", ")} (not installed)`, hot: false });
    const lines = [title];
    let line = "";
    let length = 0;
    for (const cell of cells) {
      if (length > 0 && length + 3 + cell.text.length > width()) {
        lines.push(line);
        line = "";
        length = 0;
      }
      const styled = cell.hot ? c.cyan(cell.text) : cell.text.endsWith("(not installed)") ? c.dim(cell.text) : cell.text;
      line += `${length === 0 ? " " : "   "}${styled}`;
      length += (length === 0 ? 1 : 3) + cell.text.length;
    }
    lines.push(line);
    return lines;
  };
  if (enabled.length === 0) {
    out.write(`${render(false).join("\n")}\n`);
    return Promise.resolve(items.map((item) => item.on));
  }
  return live(out, render, (key, done) => {
    if (key.name === "return" || key.name === "enter") return done();
    if (key.name === "space") {
      const item = items[enabled[at]!]!;
      item.on = !item.on;
    } else if (["up", "left", "k", "h"].includes(key.name ?? "") || (key.name === "tab" && key.shift)) {
      at = (at - 1 + enabled.length) % enabled.length;
    } else if (["down", "right", "j", "l", "tab"].includes(key.name ?? "")) {
      at = (at + 1) % enabled.length;
    }
  }, input).then((ok) => ok ? items.map((item) => item.on) : null);
}

// A Yes within the first moments after the question draws is the tail of a
// double-tapped Enter, not an answer to a plan nobody has read yet.
const CONFIRM_GRACE_MS = 400;

function confirm(input: NodeJS.ReadStream, out: NodeJS.WriteStream, c: Colors, question: string): Promise<boolean | null> {
  let yes = true;
  const shownAt = Date.now();
  const render = (active: boolean) => [active
    ? `${question} ${c.dim("›")} ${yes ? c.cyan("Yes") : c.dim("Yes")} ${c.dim("/")} ${yes ? c.dim("No") : c.cyan("No")}`
    : `${question} ${c.dim("›")} ${yes ? "Yes" : "No"}`];
  return live(out, render, (key, done) => {
    const early = Date.now() - shownAt < CONFIRM_GRACE_MS;
    if (early && (key.name === "y" || key.name === "return" || key.name === "enter")) return;
    if (key.name === "y") { yes = true; return done(); }
    if (key.name === "n") { yes = false; return done(); }
    if (key.name === "return" || key.name === "enter") return done();
    if (["left", "right", "tab", "h", "l", "up", "down"].includes(key.name ?? "")) yes = !yes;
  }, input).then((ok) => ok ? yes : null);
}
