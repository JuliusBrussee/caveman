// Learn autopilot: a native SessionEnd hook starts a detached, low-priority
// `caveman learn autopilot run`, which refreshes the learn report at most once
// per throttle window. A later SessionStart surfaces ONE user-visible line when
// that refresh found a new, big token sink. Shared by the full CLI and
// native-hook-fast, so it imports nothing from index.ts.
//
// Every state file lives under $CAVEMAN_HOME/runtime. Writes go through a
// temp file created O_EXCL + rename (rename replaces a planted symlink instead
// of following it) and refuse a symlinked parent; reads refuse symlinks. All
// hook-side entry points are fail-silent.
import { spawn, spawnSync } from "node:child_process";
import { closeSync, lstatSync, mkdirSync, openSync, readFileSync, renameSync, statSync, unlinkSync, writeSync } from "node:fs";
import { homedir, setPriority } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

import { cloudConfigPath } from "./modules/config-home.js";

export type AutopilotState = {
  last_attempt_at?: string;
  last_scan_at?: string;
  last_error?: string;
  seen?: string[];
  // Last capability probe, keyed by proxy path + mtime + size so a proxy
  // upgrade re-probes and an unchanged one is not spawned twice.
  proxy_caps?: { key: string; ok: boolean };
};

type Nudge = { line: string; sink_ids: string[]; created_at: string; announced_at?: string; claimed_at?: string; reshown?: boolean; token?: string };

const ANNOUNCE_CLASSES = new Set(["reducible", "recurring_context"]);
const ANNOUNCE_MIN_TOKENS_PER_TURN = 2000;
// Doctor findings that break what the agent loads qualify with no token rate.
const ANNOUNCE_DOCTOR_PREFIXES = ["memory_health:broken_imports:", "memory_health:memory_truncation:"];
const SEEN_CAP = 500;
// A claimed nudge whose output never reached the host (killed hook, delegate
// timeout) is shown once more after this long. One re-show, never more.
const NUDGE_RESHOW_MS = 10 * 60_000;

function caveHome(): string {
  return process.env.CAVEMAN_HOME ?? join(homedir(), ".caveman");
}

function runtimeDir(): string {
  return join(caveHome(), "runtime");
}

export function autopilotPaths() {
  const dir = runtimeDir();
  return {
    state: join(dir, "learn-autopilot.json"),
    lock: join(dir, "learn-autopilot.lock"),
    nudge: join(dir, "learn-autopilot-nudge.json"),
    inflight: join(dir, "learn-autopilot-nudge.inflight.json"),
    announced: join(dir, "learn-autopilot-announced.json"),
    // Autopilot's own reports home: its scans run with whatever cwd and env the
    // last SessionEnd hook had, so they never overwrite the canonical report,
    // dated snapshots or trend history a user's own run writes.
    reports: join(dir, "learn-autopilot"),
  };
}

export function autopilotThrottleMs(): number {
  const hours = Number(process.env.CAVEMAN_LEARN_AUTOPILOT_HOURS ?? "6");
  return (Number.isFinite(hours) && hours > 0 ? hours : 6) * 3_600_000;
}

export function autopilotTimeoutSeconds(): number {
  const value = Number(process.env.CAVE_LEARN_TIMEOUT ?? "120");
  return Number.isFinite(value) && value > 0 ? Math.floor(value) : 120;
}

function staleLockMs(): number {
  return (autopilotTimeoutSeconds() + 60) * 1000;
}

// Precedence: env (explicit either way) → config.json `learnAutopilot` →
// CI/test runners (off unless forced by env) → on.
export function autopilotEnabled(): { enabled: boolean; source: "env" | "config" | "ci" | "default" } {
  const env = process.env.CAVEMAN_LEARN_AUTOPILOT?.trim().toLowerCase();
  if (env) return { enabled: !["0", "false", "off", "no"].includes(env), source: "env" };
  try {
    const config = JSON.parse(readFileSync(cloudConfigPath(), "utf8")) as Record<string, unknown>;
    if (typeof config.learnAutopilot === "boolean") return { enabled: config.learnAutopilot, source: "config" };
  } catch { /* default below */ }
  const ci = process.env.CI;
  if ((ci && ci !== "0" && ci.toLowerCase() !== "false") || process.env.NODE_TEST_CONTEXT) return { enabled: false, source: "ci" };
  return { enabled: true, source: "default" };
}

function readJson<T>(path: string): T | undefined {
  try {
    if (!lstatSync(path).isFile()) return undefined;
    const parsed = JSON.parse(readFileSync(path, "utf8")) as unknown;
    return parsed && typeof parsed === "object" && !Array.isArray(parsed) ? parsed as T : undefined;
  } catch {
    return undefined;
  }
}

function writeJson(path: string, value: unknown): void {
  const dir = dirname(path);
  mkdirSync(dir, { recursive: true, mode: 0o700 });
  if (lstatSync(dir).isSymbolicLink()) throw new Error("refusing symlinked state directory");
  const temp = `${path}.${process.pid}.${Math.random().toString(36).slice(2)}.tmp`;
  const fd = openSync(temp, "wx", 0o600);
  try {
    writeSync(fd, `${JSON.stringify(value)}\n`);
  } finally {
    closeSync(fd);
  }
  try {
    renameSync(temp, path);
  } catch (error) {
    try { unlinkSync(temp); } catch { /* already gone */ }
    throw error;
  }
}

export function readAutopilotState(): AutopilotState {
  return readJson<AutopilotState>(autopilotPaths().state) ?? {};
}

function pidAlive(pid: number): boolean {
  if (!Number.isInteger(pid) || pid <= 0) return false;
  try {
    process.kill(pid, 0);
    return true;
  } catch (error) {
    return (error as NodeJS.ErrnoException).code === "EPERM";
  }
}

function lockHeld(): boolean {
  const { lock } = autopilotPaths();
  try {
    const stat = lstatSync(lock);
    if (Date.now() - stat.mtimeMs > staleLockMs()) return false;
    const pid = Number(readFileSync(lock, "utf8").split("\n")[0]);
    return pidAlive(pid);
  } catch {
    return false;
  }
}

// ponytail: stale-lock takeover is unlink-then-O_EXCL; two takers racing on
// the same stale lock can both proceed once. Harmless (one extra scan).
function acquireLock(): boolean {
  const { lock } = autopilotPaths();
  mkdirSync(dirname(lock), { recursive: true, mode: 0o700 });
  for (let attempt = 0; attempt < 2; attempt++) {
    try {
      const fd = openSync(lock, "wx", 0o600);
      writeSync(fd, `${process.pid}\n${new Date().toISOString()}\n`);
      closeSync(fd);
      return true;
    } catch (error) {
      if ((error as NodeJS.ErrnoException).code !== "EEXIST" || lockHeld()) return false;
      try { unlinkSync(lock); } catch { return false; }
    }
  }
  return false;
}

function releaseLock(): void {
  try { unlinkSync(autopilotPaths().lock); } catch { /* already gone */ }
}

function due(state: AutopilotState, now = Date.now()): boolean {
  const last = Date.parse(state.last_attempt_at ?? "");
  return !Number.isFinite(last) || now - last >= autopilotThrottleMs();
}

// Hook side (SessionEnd). Only small file reads before the spawn; the child
// is detached, stdio-ignored, unref'd and lowered to idle priority, so the host
// never waits on it and a killed hook process group does not take it down.
export function maybeSpawnAutopilot(cliPath = join(dirname(fileURLToPath(import.meta.url)), "index.js")): boolean {
  try {
    if (!autopilotEnabled().enabled || !due(readAutopilotState()) || lockHeld()) return false;
    const child = spawn(process.execPath, [cliPath, "learn", "autopilot", "run"], {
      detached: true,
      stdio: "ignore",
      windowsHide: true,
      env: process.env,
    });
    child.on("error", () => { /* fail-open */ });
    if (child.pid) {
      try { setPriority(child.pid, 19); } catch { /* best effort */ }
    }
    child.unref();
    return true;
  } catch {
    return false;
  }
}

function compactTokens(n: number): string {
  if (n >= 10_000) return `${Math.round(n / 1000)}k`;
  if (n >= 1000) return `${(n / 1000).toFixed(1)}k`;
  return String(Math.round(n));
}

type ScanSink = { sink_id?: unknown; title?: unknown; class?: unknown; tokens_per_turn?: unknown; evidence?: { repo?: unknown } };

function sinksOf(value: unknown): ScanSink[] | undefined {
  const sinks = value && typeof value === "object" ? (value as { sinks?: unknown }).sinks : undefined;
  return Array.isArray(sinks) ? sinks as ScanSink[] : undefined;
}

type Fresh = { title: string; tokens_per_turn: number; class?: string; doctor?: boolean; repo?: string };

// nudgeLine leads with the biggest new finding; memory-file findings
// ride along as a count, or lead when they are all that is new.
export function nudgeLine(fresh: Fresh[]): string {
  const clean = (title: string) => title.replace(/[\u0000-\u001f\u007f]+/g, " ").trim().slice(0, 80);
  const sinks = fresh.filter((item) => !item.doctor);
  const doctor = fresh.filter((item) => item.doctor);
  const findings = (n: number) => `${n} memory-file finding${n === 1 ? "" : "s"}`;
  if (sinks.length === 0) {
    const more = doctor.length > 1 ? ` (+${doctor.length - 1} more)` : "";
    const where = doctor[0]!.repo ? ` (${clean(doctor[0]!.repo)})` : "";
    return `caveman learn: memory files${where} — ${clean(doctor[0]!.title)}${more}. Run \`caveman learn --all\` to review.`;
  }
  const top = sinks[0]!;
  const lead = sinks.length === 1 ? "new finding" : `${sinks.length} new findings, biggest`;
  const extra = doctor.length > 0 ? `, plus ${findings(doctor.length)}` : "";
  // A recurring_context sink is a pasted block averaged over all messages, not
  // something loaded with each one.
  const where = top.class === "recurring_context" ? "per message on average" : "in every message";
  return `caveman learn: ${lead} — ${clean(top.title)} (~${compactTokens(top.tokens_per_turn)} tokens ${where}, estimate)${extra}. Run \`caveman learn\` to review.`;
}

export const AUTOPILOT_PROXY_TOO_OLD = "proxy too old for autopilot (needs learn capabilities)";
const REQUIRED_CAPS = ["no_remember", "reports_home", "memory_health"];

// proxySupportsAutopilot probes `learn capabilities`. A proxy that predates it
// ignores --no-remember/--reports-home, so an unattended scan would write
// cavemem and the canonical report; such a proxy never scans.
function proxySupportsAutopilot(proxyBin: string, state: AutopilotState): boolean {
  let key: string;
  try {
    const info = statSync(proxyBin);
    key = `${proxyBin}:${info.mtimeMs}:${info.size}`;
  } catch {
    key = `${proxyBin}:missing`;
  }
  if (state.proxy_caps?.key === key) return state.proxy_caps.ok;
  let ok = false;
  try {
    const probe = spawnSync(proxyBin, ["learn", "capabilities"], {
      encoding: "utf8", env: process.env, timeout: 10_000, killSignal: "SIGKILL", stdio: ["ignore", "pipe", "ignore"], windowsHide: true,
    });
    const caps = probe.status === 0 ? JSON.parse(probe.stdout) as Record<string, unknown> : undefined;
    ok = caps?.schema === "caveman.learn.capabilities.v1" && REQUIRED_CAPS.every((cap) => caps[cap] === true);
  } catch { ok = false; }
  state.proxy_caps = { key, ok };
  return ok;
}

// Child side (`caveman learn autopilot run`). Holds the lock for the whole
// scan; the proxy is SIGKILLed at the learn timeout. Returns an exit code.
export function runAutopilot(proxyBin: string): number {
  if (!autopilotEnabled().enabled || !acquireLock()) return 0;
  const paths = autopilotPaths();
  try {
    const state = readAutopilotState();
    if (!due(state)) return 0;
    state.last_attempt_at = new Date().toISOString();
    if (!proxySupportsAutopilot(proxyBin, state)) {
      state.last_error = AUTOPILOT_PROXY_TOO_OLD;
      writeJson(paths.state, state);
      return 1;
    }
    writeJson(paths.state, state);
    const result = spawnSync(proxyBin, ["learn", "scan", "--write-report", "--no-remember", "--reports-home", paths.reports], {
      encoding: "utf8",
      env: process.env,
      timeout: autopilotTimeoutSeconds() * 1000,
      killSignal: "SIGKILL",
      maxBuffer: 64 * 1024 * 1024,
      stdio: ["ignore", "pipe", "pipe"],
      windowsHide: true,
    });
    if (result.error || result.status !== 0) {
      const timedOut = (result.error as NodeJS.ErrnoException | undefined)?.code === "ETIMEDOUT";
      state.last_error = timedOut
        ? `scan timed out after ${autopilotTimeoutSeconds()}s`
        : (result.stderr || result.error?.message || `scan exited ${result.status}`).trim().split("\n").pop()!.slice(0, 300);
      writeJson(paths.state, state);
      return 1;
    }
    let sinks: ScanSink[] | undefined;
    try { sinks = sinksOf(JSON.parse(result.stdout)); } catch { /* read the written report */ }
    sinks ??= sinksOf(readJson(join(paths.reports, "reports", "caveman-learn.json")));
    if (!sinks) {
      state.last_error = "scan produced no readable report";
      writeJson(paths.state, state);
      return 1;
    }
    const doctorFinding = (sink: ScanSink) => typeof sink.sink_id === "string"
      && ANNOUNCE_DOCTOR_PREFIXES.some((prefix) => (sink.sink_id as string).startsWith(prefix));
    const big = sinks
      .filter((sink) => doctorFinding(sink) || (typeof sink.sink_id === "string" && typeof sink.class === "string" && ANNOUNCE_CLASSES.has(sink.class)
        && typeof sink.tokens_per_turn === "number" && sink.tokens_per_turn >= ANNOUNCE_MIN_TOKENS_PER_TURN))
      .map((sink) => ({
        sink_id: sink.sink_id as string,
        title: typeof sink.title === "string" ? sink.title : sink.sink_id as string,
        tokens_per_turn: typeof sink.tokens_per_turn === "number" ? sink.tokens_per_turn : 0,
        ...(typeof sink.class === "string" ? { class: sink.class } : {}),
        doctor: doctorFinding(sink),
        ...(typeof sink.evidence?.repo === "string" ? { repo: sink.evidence.repo } : {}),
      }))
      .sort((a, b) => Number(a.doctor) - Number(b.doctor) || b.tokens_per_turn - a.tokens_per_turn);
    const baseline = state.seen === undefined;
    const seen = new Set(state.seen ?? []);
    const fresh = big.filter((sink) => !seen.has(sink.sink_id));
    // The first scan only records a baseline: autopilot announces what is NEW.
    // A still-unclaimed nudge is left alone and its successors stay unseen, so
    // they are announced after it rather than silently absorbed.
    const pendingExists = readJson(paths.nudge) !== undefined;
    if (baseline || !pendingExists) {
      if (!baseline && fresh.length > 0) {
        const nudge: Nudge = { line: nudgeLine(fresh), sink_ids: fresh.map((sink) => sink.sink_id), created_at: new Date().toISOString() };
        writeJson(paths.nudge, nudge);
      }
      for (const sink of fresh) seen.add(sink.sink_id);
    }
    state.seen = [...seen].slice(-SEEN_CAP);
    state.last_scan_at = new Date().toISOString();
    delete state.last_error;
    writeJson(paths.state, state);
    return 0;
  } catch {
    return 1;
  } finally {
    releaseLock();
  }
}

// Hook side (SessionStart). At most one nudge is in flight at a time:
//  - an unconfirmed in-flight nudge younger than NUDGE_RESHOW_MS belongs to
//    another session start, so a pending newer nudge waits behind it;
//  - an older one is re-shown once (reshown), and dropped after that;
//  - otherwise the pending nudge is claimed with one atomic rename.
// The in-flight record carries the claimer's token; confirmLearnNudge(token)
// marks it announced only for that claimer, once the line reached the host.
// Only fresh sessions (startup / clear) announce; resume, compact and fork
// never do.
export function claimLearnNudge(source: string | undefined, token: string): string | undefined {
  if (source !== "startup" && source !== "clear") return undefined;
  try {
    if (!autopilotEnabled().enabled) return undefined;
    const paths = autopilotPaths();
    const claimed = `${paths.inflight}.${process.pid}.claim`;
    let nudge: Nudge | undefined;
    const held = readJson<Nudge>(paths.inflight);
    if (held) {
      const age = Date.now() - Date.parse(held.claimed_at ?? "");
      // age < 0 = clock went backwards; treat as expired rather than block.
      if (Number.isFinite(age) && age >= 0 && age < NUDGE_RESHOW_MS) return undefined;
      if (!held.reshown) {
        renameSync(paths.inflight, claimed); // one concurrent re-claimer wins
        nudge = { ...held, reshown: true };
      } else {
        try { unlinkSync(paths.inflight); } catch { /* another session dropped it */ }
      }
    }
    if (!nudge) {
      renameSync(paths.nudge, claimed);
      nudge = readJson<Nudge>(claimed);
    }
    if (!nudge || typeof nudge.line !== "string" || !nudge.line) {
      try { unlinkSync(claimed); } catch { /* best effort */ }
      return undefined;
    }
    // In-flight first, then drop the claim file: a failed write must not lose
    // the line, it only forfeits the re-show.
    try { writeJson(paths.inflight, { ...nudge, token, claimed_at: new Date().toISOString() }); } catch { /* shown once, unconfirmable */ }
    try { unlinkSync(claimed); } catch { /* best effort */ }
    return nudge.line.replace(/[\u0000-\u001f\u007f]+/g, " ").slice(0, 300);
  } catch {
    return undefined;
  }
}

// confirmLearnNudge runs once the claimed line has reached the host's stdout.
// It only confirms the in-flight record this claimer's token owns.
export function confirmLearnNudge(token: string): void {
  try {
    const paths = autopilotPaths();
    const nudge = readJson<Nudge>(paths.inflight);
    if (!nudge || !token || nudge.token !== token) return;
    unlinkSync(paths.inflight);
    writeJson(paths.announced, { ...nudge, announced_at: new Date().toISOString() });
  } catch { /* status only */ }
}

function ago(iso: string | undefined, now: number): string {
  const at = Date.parse(iso ?? "");
  if (!Number.isFinite(at)) return "never";
  const minutes = Math.max(0, Math.round((now - at) / 60_000));
  const rel = minutes < 60 ? `${minutes}m ago` : minutes < 2880 ? `${Math.round(minutes / 60)}h ago` : `${Math.round(minutes / 1440)}d ago`;
  return `${iso} (${rel})`;
}

export function autopilotStatusText(now = Date.now()): string {
  const enabled = autopilotEnabled();
  const state = readAutopilotState();
  const paths = autopilotPaths();
  const announced = readJson<Nudge>(paths.announced);
  const pending = readJson<Nudge>(paths.nudge);
  const lastAttempt = Date.parse(state.last_attempt_at ?? "");
  const next = !Number.isFinite(lastAttempt) || now - lastAttempt >= autopilotThrottleMs()
    ? "next session end"
    : new Date(lastAttempt + autopilotThrottleMs()).toISOString();
  const lines = [
    `learn autopilot: ${enabled.enabled ? "on" : "off"} (${enabled.source === "env" ? "CAVEMAN_LEARN_AUTOPILOT" : enabled.source === "ci" ? "CI/test run" : enabled.source})`,
    `  last scan:      ${ago(state.last_scan_at, now)}${lockHeld() ? " · scan running now" : ""}`,
    `  next scan:      ${enabled.enabled ? next : "disabled"}`,
    `  last error:     ${state.last_error ?? "none"}`,
    `  last shown:     ${announced?.line ? `${announced.line} (${announced.announced_at ?? "?"})` : "nothing yet"}`,
  ];
  if (pending?.line) lines.push(`  waiting to show: ${pending.line}`);
  return `${lines.join("\n")}\n`;
}
