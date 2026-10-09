// First run: one screen says what was found and what setup will do, one key
// applies it (Customize and Details are a key away), then sign in only when
// routing is on, then offer to start the agent. `caveman setup`, bare `caveman` before any
// setup, `npx @caveman-ai/cli`, the end of install.sh and `caveman <agent>`
// before any setup all land here. Nothing is written before Continue.
import { readFileSync } from "node:fs";
import { homedir } from "node:os";
import { sep } from "node:path";
import { emitKeypressEvents } from "node:readline";

import { applyModules, currentSelection, moduleHost, planModules, renderPlan, type ModulePlan, type ModuleSelection } from "./apply.js";
import { ROUTING_ON_LINE } from "./cloud.js";
import { cloudConfigPath } from "./config-home.js";
import type { FoundKey } from "./provider-logins.js";
import { MODULES, findModule, type ModuleId } from "./registry.js";

// `wired`: Caveman already routes this agent. A wired agent stays ticked even
// off PATH, because the plan unwires every agent left out of the list.
export type OnboardAgent = { id: string; name: string; installed: boolean; wired: boolean; version?: string };
export type OnboardOptions = { yes: boolean; dryRun: boolean; only?: ModuleId[]; skip?: ModuleId[]; agents?: string[] };
// Logins found beside the agents. Shown, never used unasked: an API key joins
// Auto's pool only when the user ticks it.
export type OnboardFound = {
  // Claude Code config directories, when there is more than one.
  claudeLogins?: string[];
  // How Codex signs in here: "ChatGPT plan" or "API key".
  codexLogin?: string;
  keys?: FoundKey[];
};
// `approved` is called once the browser step is done, before sign-in prints
// its own lines (what routing sends).
export type SignInUi = { signal: AbortSignal; code(url: string, userCode: string, opened: boolean): void; approved?(): void };
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
  found?: OnboardFound;
  // Stores one found key in Auto's pool (`caveman providers add`).
  addKey?(key: FoundKey): void;
  // Set when this CLI runs from a package runner's cache (npx): `run` installs
  // it for good and returns the command to name in hints (it throws when it
  // cannot, and setup then writes nothing); `apply` has that copy do the
  // wiring, so nothing records a path into the cache.
  installCli?: {
    command: string;
    run(): string;
    apply(selection: ModuleSelection, agents: string[], say: (line: string) => void): Promise<{ ok: boolean; problems: string[] }>;
  };
  // Setup may end by offering to start the agent; `launch` in the result names it.
  offerLaunch?: boolean;
  // Set when `caveman <agent>` continues into the agent after setup.
  launching?: string;
  input?: NodeJS.ReadStream;
  output?: NodeJS.WriteStream;
};
export type OnboardResult = { confirmed: boolean; cancelled: boolean; ok: boolean; plan?: ModulePlan; launch?: string };

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

  const found = deps.found ?? {};
  out.write(`${c.bold("caveman")} ${c.dim("· make your coding agent cheaper")}\n\n${foundBlock(deps.agents, found, c)}\n\n`);
  let selection = initialSelection(opts);
  // Re-runs keep what is wired; a first run takes what is installed. --agents
  // adds to the wired ones: unwiring is `caveman off` or unticking here.
  const wired = deps.agents.filter((agent) => agent.wired).map((agent) => agent.id);
  let agents = [...new Set([...wired, ...(opts.agents ?? (wired.length ? [] : deps.agents.filter((agent) => agent.installed).map((agent) => agent.id)))])];
  // Off until ticked: Auto can spend on a key it is given.
  const keys = (deps.addKey ? found.keys ?? [] : []).filter((key) => !key.added).map((key) => ({ ...key, on: false }));
  let plan: ModulePlan;
  if (ask) {
    for (;;) {
      plan = await planModules(selection, agents);
      const rows = summaryRows(plan, selection, agents.map((id) => byId.get(id)!.name), found);
      if (deps.installCli) rows.push(["install", `the caveman command · ${deps.installCli.command}`]);
      const moved = await portMove(selection, agents);
      if (moved) rows.push(["port", `${moved.held} is in use by another program · the runtime will use ${moved.free}`]);
      const pick = await choose(input, out, c, rows, keys, plan.lines.length > 0 ? "Set up" : "Continue");
      if (pick === null) return cancelled(out, c);
      if (pick === "details") {
        out.write(`\n${plan.lines.length ? renderPlan(plan) : `This will\n  ${c.dim("change nothing")}\n`}\n`);
        continue;
      }
      if (pick === "customize") {
        out.write("\n");
        const picked = await customize(input, out, c, selection, agents, keys, deps.agents.filter(usable), deps.agents.filter((a) => !usable(a)));
        if (!picked) return cancelled(out, c);
        ({ selection, agents } = picked);
        out.write("\n");
        continue;
      }
      await deps.markFirstRun();
      if (pick === "no") {
        deps.markDeclined();
        const session = deps.launching ? ` · ${deps.launching} runs this session only` : "";
        out.write(`${c.dim(`Nothing changed${session} · ${deps.cmd} setup when you want it`)}\n`);
        return { confirmed: false, cancelled: false, ok: true, plan };
      }
      break;
    }
    out.write("\n");
  } else {
    out.write(`Modules  ${MODULES.filter((m) => selection[m.id]).map((m) => m.title).join(" · ") || "none"}\n`);
    out.write(`Agents   ${agents.map((id) => byId.get(id)!.name).join(" · ") || "none"}\n\n`);
    plan = await planModules(selection, agents);
    out.write(plan.lines.length ? renderPlan(plan) : `This will\n  ${c.dim("change nothing")}\n`);
    if (deps.installCli) out.write(`  ${"RUN".padEnd(9)} ${deps.installCli.command}  the caveman command, kept after this run\n`);
    const moved = await portMove(selection, agents);
    if (moved) out.write(`  ${"RUN".padEnd(9)} local runtime on port ${moved.free}  ${moved.held} is in use by another program\n`);
    if (opts.dryRun) {
      out.write(`${c.dim("Dry run: nothing was written.")}\n`);
      return { confirmed: false, cancelled: false, ok: true, plan };
    }
    // Without a terminal to ask in (CI counts), only --yes applies.
    if (!deps.interactive && !opts.yes) {
      out.write(`Nothing changed: pass --yes to apply · ${deps.cmd} setup --yes\n`);
      return { confirmed: false, cancelled: false, ok: true, plan };
    }
  }

  // One line that the download rewrites; each finished step prints over it.
  const busy = spinner(out, c, ask);
  let cmd = deps.cmd;
  if (deps.installCli) {
    busy.show("installing the caveman command");
    try {
      cmd = deps.installCli.run();
      busy.say(`${c.green("✓")} caveman command installed${cmd === "caveman" ? "" : ` at ${tilde(cmd)} ${c.dim("· not on your PATH")}`}`);
    } catch (error) {
      busy.stop();
      out.write(`${c.red("✗")} ${error instanceof Error ? error.message : String(error)}\n${c.dim("Nothing else changed.")}\n`);
      return { confirmed: true, cancelled: false, ok: false, plan };
    }
  }
  const progress = (line: string) => busy.say(line.replace(/^✓/, c.green("✓")).replace(/^○/, c.yellow("○")));
  let result: { ok: boolean; problems: string[] };
  if (deps.installCli) {
    result = await deps.installCli.apply(selection, agents, (line) => line.startsWith("downloading ") ? busy.show(line) : progress(line));
  } else {
    // Before the first agent is wired: never to a port another program answers on.
    const moved = await portMove(selection, agents);
    if (moved) {
      moduleHost().useRuntimePort(moved.free);
      progress(`○ ${moved.held} is in use by another program · local runtime on port ${moved.free}`);
    }
    // The agents this setup chose: `caveman claude` re-wires Claude Code later
    // only when it was one of them.
    moduleHost().mutateConfig((out) => { out.setupAgents = [...agents]; });
    result = await applyModules(plan, { yes: true, progress, downloading: (name) => busy.show(`downloading ${name}`) });
  }
  busy.stop();
  for (const key of keys.filter((item) => item.on)) {
    try {
      deps.addKey!(key);
      out.write(`${c.green("✓")} ${key.name} key added for Auto ${c.dim(`· ${cmd} providers remove ${key.id} takes it back`)}\n`);
    } catch (error) {
      result.problems.push(`${key.name} key: ${error instanceof Error ? error.message.replace(/^caveman: /, "") : String(error)}`);
      result.ok = false;
    }
  }
  for (const problem of result.problems) out.write(`${c.red("✗")} ${problem}\n`);
  out.write("\n");
  const auto = selection.routing ? await routingStep(opts, { ...deps, cmd }, input, out, c) : false;
  const tryAgent = ["claude", "codex"].find((id) => agents.includes(id)) ?? agents[0] ?? "claude";
  let launch: string | undefined;
  if (!result.ok) {
    out.write(`${c.red("✗")} Setup finished with problems. Fix them, then run ${cmd} setup again.\n`);
  } else if (deps.launching) {
    out.write(`${c.green("✓")} Ready. Starting ${deps.launching}.\n`);
  } else {
    out.write(`${c.green("✓")} Ready. Try:  ${c.cyan(`${cmd} ${tryAgent}`)}      See it:  ${c.cyan(`${cmd} status`)}\n`);
    if (auto) out.write(`${c.dim(`  Auto is in the model picker${tryAgent === "claude" ? " · /model in Claude Code" : ""}`)}\n`);
  }
  if (ask) await deps.discloseTelemetry();
  const startable = byId.get(tryAgent);
  if (ask && result.ok && deps.offerLaunch && !deps.launching && startable?.installed && agents.includes(tryAgent)) {
    out.write("\n");
    if (await confirm(input, out, c, `Start ${startable.name} now?`)) launch = tryAgent;
  }
  return { confirmed: true, cancelled: false, ok: result.ok, plan, ...(launch ? { launch } : {}) };
}

// The runtime's port matters only when this setup wires an agent through it
// (aider is wired without the runtime).
async function portMove(selection: ModuleSelection, agents: string[]) {
  if (!MODULES.some((m) => m.wiresAgents && selection[m.id]) || !agents.some((id) => id !== "aider")) return undefined;
  return moduleHost().runtimePortTaken();
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
// `caveman login` finishes it later (its sign-in says what routing sends).
// Already signed in, routing starts now, so this says it. True when Auto is
// live as setup ends.
async function routingStep(opts: OnboardOptions, deps: OnboardDeps, input: NodeJS.ReadStream, out: NodeJS.WriteStream, c: Colors): Promise<boolean> {
  if (await deps.signedIn()) {
    out.write(`${ROUTING_ON_LINE}\n\n`);
    return true;
  }
  const waits = (why: string) => {
    out.write(`${c.yellow("○")} routing is on and ${why} · ${c.cyan(`${deps.cmd} login`)}\n\n`);
    return false;
  };
  if (!deps.interactive || opts.yes) return waits("starts after you sign in");
  out.write(`${c.bold("Sign in to switch on Auto")} ${c.dim("· free account · everything else already works without it")}\n`);
  const skip = new AbortController();
  const stop = keys(input, (key) => {
    if (key.name === "escape" || key.name === "s" || key.name === "q" || (key.ctrl && key.name === "c")) skip.abort();
  });
  const busy = spinner(out, c, true);
  busy.show("reaching Caveman Cloud");
  try {
    const { email } = await deps.signIn({
      signal: skip.signal,
      code(url, userCode, opened) {
        busy.say(`  Open ${c.cyan(url)} and enter ${c.bold(userCode)}   ${c.dim(opened ? "(browser opened · esc skips)" : "(esc skips)")}`);
        busy.show("waiting for you to approve in the browser");
      },
      approved: () => busy.stop(),
    });
    busy.stop();
    out.write(`  ${c.green("✓")} signed in${email ? ` as ${email}` : ""}\n\n`);
    return true;
  } catch (error) {
    busy.stop();
    // Esc during the receipt step, after the credentials were saved.
    if (skip.signal.aborted && await deps.signedIn()) {
      out.write(`  ${c.green("✓")} signed in\n\n${ROUTING_ON_LINE}\n\n`);
      return true;
    }
    if (skip.signal.aborted) return waits("starts after you sign in");
    const message = error instanceof Error ? error.message : String(error);
    const code = (error as { code?: unknown }).code;
    if (code === "sign_in_closed") {
      out.write(`  ${c.yellow("!")} ${message}\n`);
      return waits("starts once sign-in opens");
    }
    if (code === "cloud_unreachable") {
      out.write(`  ${c.yellow("!")} ${message}\n`);
      return waits("starts after you sign in");
    }
    out.write(`  ${c.red("✗")} sign-in failed: ${message}\n`);
    return waits("starts after you sign in");
  } finally {
    busy.stop();
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

function tilde(path: string): string {
  const home = homedir();
  return path === home || path.startsWith(home + sep) ? `~${path.slice(home.length)}` : path;
}

// The agents line, then one line per kind of login found beside them.
function foundBlock(agents: OnboardAgent[], found: OnboardFound, c: Colors): string {
  const lines = [foundLine(agents)];
  const row = (label: string, value: string) => lines.push(`  ${c.dim(label.padEnd(15))}${value}`);
  if ((found.claudeLogins?.length ?? 0) > 1) row("Claude logins", found.claudeLogins!.map(tilde).join(" · "));
  if (found.codexLogin) row("Codex login", found.codexLogin);
  if (found.keys?.length) row("API keys", found.keys.map((key) => key.added ? `${key.env} ${c.dim("(in Auto's pool)")}` : key.env).join(" · "));
  return lines.join("\n");
}

// What Continue does, in four rows: the full file list is under Details.
function summaryRows(plan: ModulePlan, selection: ModuleSelection, agentNames: string[], found: OnboardFound): [string, string][] {
  const logins = found.claudeLogins?.length ?? 0;
  const names = agentNames.map((name) => name === "Claude Code" && logins > 1 ? `${name} (${logins} logins)` : name);
  const downloads = plan.lines.filter((line) => line.action === "DOWNLOAD").flatMap((line) => line.target.split(", "));
  const files = plan.lines.filter((line) => line.action === "CREATE" || line.action === "UPDATE").length;
  const rows: [string, string][] = [
    ["agents", names.join(" · ") || "none"],
    ["modules", MODULES.filter((m) => selection[m.id]).map((m) => m.title).join(" · ") || "none"],
    // What leaves the machine is said before the key that agrees to it.
    ...MODULES.filter((m) => m.needsSignIn && selection[m.id]).map((m): [string, string] => [m.title, `${m.summary} · free account`]),
  ];
  if (downloads.length) rows.push(["download", `${downloads.length} signed ${downloads.length === 1 ? "binary" : "binaries"}`]);
  if (files) rows.push(["write", `${files} config ${files === 1 ? "file" : "files"}, your own lines kept · undo: caveman off --all`]);
  if (plan.lines.length === 0) rows.push(["change", "nothing: this is already set up"]);
  return rows;
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
type FoundKeyChoice = FoundKey & { on: boolean };
type Pick = "go" | "customize" | "details" | "no";

// A legacy Windows console (conhost on a raster font) has none of these
// glyphs; Windows Terminal and the VS Code terminal do.
function glyphs() {
  const plain = process.platform === "win32" && !process.env.WT_SESSION && process.env.TERM_PROGRAM !== "vscode";
  return plain
    ? { on: "[x]", off: "[ ]", pointer: ">", spin: ["|", "/", "-", "\\"] }
    : { on: "◼", off: "◻", pointer: "›", spin: [..."⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏"] };
}

function clip(text: string, width: number): string {
  return text.length > width ? `${text.slice(0, Math.max(0, width - 1))}…` : text;
}

// One line that work in progress rewrites. `say` prints a finished line above
// it. Without a terminal to redraw in, each update is its own line.
function spinner(out: NodeJS.WriteStream, c: Colors, animate: boolean) {
  const frames = glyphs().spin;
  let frame = 0;
  let text = "";
  let timer: ReturnType<typeof setInterval> | undefined;
  const erase = () => { if (animate && text) out.write("\r\x1b[2K"); };
  const draw = () => out.write(`\r\x1b[2K${c.cyan(frames[frame++ % frames.length]!)} ${clip(text, Math.max(20, (out.columns || 80) - 3))}`);
  return {
    show(next: string) {
      if (!animate) return void out.write(`${next}\n`);
      text = next;
      draw();
      timer ??= setInterval(draw, 80);
      timer.unref?.();
    },
    say(line: string) {
      erase();
      out.write(`${line}\n`);
      if (animate && text) draw();
    },
    stop() {
      if (timer) clearInterval(timer);
      timer = undefined;
      erase();
      text = "";
    },
  };
}

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

function live(out: NodeJS.WriteStream, render: (active: boolean) => string[], onKey: (key: Key, done: () => void) => void, input: NodeJS.ReadStream, onCancel?: () => void): Promise<boolean> {
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
      if (key.name === "escape" || (key.ctrl && key.name === "c")) {
        onCancel?.();
        return finish(false);
      }
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
  const g = glyphs();
  const box = (item: Item) => item.on ? g.on : g.off;
  const render = (active: boolean): string[] => {
    const current = active ? enabled[at] : -1;
    if (layout === "column") {
      return [title, ...items.map((item, i) => {
        const label = item.label.padEnd(labelWidth);
        const hint = (item.hint ?? "").slice(0, Math.max(0, width() - 3 - labelWidth));
        return `${i === current ? c.cyan(g.pointer) : " "}${box(item)} ${i === current ? c.cyan(label) : label}${c.dim(hint)}`;
      })];
    }
    // Agents that are not installed share one dimmed entry at the end.
    const missing = items.filter((item) => item.disabled).map((item) => item.label);
    const cells = items.flatMap((item, i) => item.disabled ? [] : [{ text: `${box(item)} ${item.label}`, hot: i === current }]);
    if (missing.length) cells.push({ text: `${g.off} ${missing.join(", ")} (not installed)`, hot: false });
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
  const pointer = c.dim(glyphs().pointer);
  const render = (active: boolean) => [active
    ? `${question} ${pointer} ${yes ? c.cyan("Yes") : c.dim("Yes")} ${c.dim("/")} ${yes ? c.dim("No") : c.cyan("No")}`
    : `${question} ${pointer} ${yes ? "Yes" : "No"}`];
  return live(out, render, (key, done) => {
    const early = Date.now() - shownAt < CONFIRM_GRACE_MS;
    if (early && (key.name === "y" || key.name === "return" || key.name === "enter")) return;
    if (key.name === "y") { yes = true; return done(); }
    if (key.name === "n") { yes = false; return done(); }
    if (key.name === "return" || key.name === "enter") return done();
    if (["left", "right", "tab", "h", "l", "up", "down"].includes(key.name ?? "")) yes = !yes;
    // Esc leaves the question showing No, never a Yes that did not happen.
  }, input, () => { yes = false; }).then((ok) => ok ? yes : null);
}

// The whole first screen: what setup will do, and the one key that does it.
// `k` ticks the found API keys (off until then); Customize and Details return
// here.
function choose(input: NodeJS.ReadStream, out: NodeJS.WriteStream, c: Colors, rows: [string, string][], found: FoundKeyChoice[], primary: string): Promise<Pick | null> {
  const options: { id: Pick; label: string }[] = [
    { id: "go", label: primary },
    { id: "customize", label: "Customize" },
    { id: "details", label: "Details" },
    { id: "no", label: "Not now" },
  ];
  const g = glyphs();
  let at = 0;
  const shownAt = Date.now();
  const width = () => Math.max(20, (out.columns || 80) - 1);
  const render = (active: boolean): string[] => {
    const lines = ["Setup will", ...rows.map(([label, value]) => `  ${c.dim(label.padEnd(10))}${clip(value, width() - 12)}`)];
    if (found.length) {
      const text = `let Auto use ${found.map((key) => key.env).join(", ")} · Auto can spend on them`;
      lines.push(`  ${c.dim("keys".padEnd(10))}${found.some((key) => key.on) ? g.on : g.off} ${clip(text, width() - 14 - g.on.length)}`);
    }
    if (!active) return [...lines, `${c.dim(g.pointer)} ${options[at]!.label}`];
    return [
      ...lines,
      "",
      options.map((option, i) => i === at ? c.cyan(`${g.pointer} ${option.label}`) : `  ${option.label}`).join("  "),
      c.dim(clip(`enter · c customize · d details${found.length ? " · k keys" : ""} · n not now · esc cancels`, width())),
    ];
  };
  return live(out, render, (key, done) => {
    const name = key.name ?? "";
    // The tail of a double-tapped Enter is not a Yes to a plan nobody has read.
    const early = Date.now() - shownAt < CONFIRM_GRACE_MS;
    if (name === "return" || name === "enter") return early && at === 0 ? undefined : done();
    if (name === "y") {
      if (early) return;
      at = 0;
      return done();
    }
    const hot = ({ c: 1, d: 2, n: 3 } as Record<string, number>)[name];
    if (hot !== undefined) {
      at = hot;
      return done();
    }
    if (name === "k" && found.length) {
      const on = !found.some((item) => item.on);
      for (const item of found) item.on = on;
    } else if (["left", "up", "h"].includes(name) || (name === "tab" && key.shift)) {
      at = (at - 1 + options.length) % options.length;
    } else if (["right", "down", "l", "tab"].includes(name)) {
      at = (at + 1) % options.length;
    }
  }, input).then((ok) => ok ? options[at]!.id : null);
}

// Customize: the module, agent and key pickers in turn; null when cancelled.
async function customize(
  input: NodeJS.ReadStream, out: NodeJS.WriteStream, c: Colors,
  selection: ModuleSelection, agents: string[], found: FoundKeyChoice[], usable: OnboardAgent[], missing: OnboardAgent[],
): Promise<{ selection: ModuleSelection; agents: string[] } | null> {
  const modules = await toggle(input, out, c, `Modules ${c.dim("· space toggles, enter continues")}`, "column",
    MODULES.map((m) => ({ label: m.title, hint: m.needsSignIn ? `${m.summary} · free account` : m.summary, on: selection[m.id] })));
  if (!modules) return null;
  const picked = Object.fromEntries(MODULES.map((m, i) => [m.id, modules[i]!])) as ModuleSelection;
  const shown = [...usable, ...missing];
  if (shown.length > 0) {
    out.write("\n");
    const ticked = await toggle(input, out, c, "Agents", "row",
      shown.map((a) => ({ label: a.name, on: agents.includes(a.id), disabled: missing.includes(a) })));
    if (!ticked) return null;
    agents = shown.filter((_, i) => ticked[i]).map((a) => a.id);
  }
  if (found.length > 0) {
    out.write("\n");
    const ticked = await toggle(input, out, c, `Keys for Auto ${c.dim("· Auto can spend on a key it is given")}`, "column",
      found.map((key) => ({ label: key.env, hint: key.name, on: key.on })));
    if (!ticked) return null;
    found.forEach((key, i) => { key.on = ticked[i]!; });
  }
  return { selection: picked, agents };
}
