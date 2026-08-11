#!/usr/bin/env node
// subagent-tax — how big is the per-call prefix each coding harness installed
// on THIS machine sends? Local capture sink, no provider API calls, no account.
// Basis: inferred, always. See METHOD.md for how and why.

import { spawn, spawnSync } from "node:child_process";
import { mkdirSync, writeFileSync, readdirSync, readFileSync, rmSync, mkdtempSync, openSync, closeSync, existsSync } from "node:fs";
import { join, dirname, resolve } from "node:path";
import { pathToFileURL } from "node:url";
import { tmpdir, platform, arch } from "node:os";

import { startSink } from "./lib/sink.mjs";
import { pickPrimary, LLM_KINDS } from "./lib/analyze.mjs";
import { estimateTokens, countTokensAnthropic, DEFAULT_CHARS_PER_TOKEN, CALIBRATION_NOTE } from "./lib/tokens.mjs";
import { renderTable, renderHonesty, writeReport, writeManifest, FOOTER, HONESTY_LINES, SPREAD_NOTE, LEGEND } from "./lib/report.mjs";
import { HARNESSES, HARNESS_IDS, PROMPT } from "./lib/harnesses.mjs";
import { forceKillTree, harnessSpawnOptions, portableProcessInvocation, stopTree } from "./lib/process-tree.mjs";

const TOOL_VERSION = "0.1.0";

function fail(message) {
  process.stderr.write(`subagent-tax: ${message}\n`);
  process.exit(2);
}

export function parseArgs(argv, onError = fail) {
  const args = {
    harness: null, out: "subagent-tax-report", timeout: 90, grace: 3,
    ratio: DEFAULT_CHARS_PER_TOKEN, repeat: 1, countTokens: false,
    json: false, list: false, isolate: false,
  };
  // Every valued flag validates its argument: a missing or non-numeric value
  // used to hang the run forever (NaN deadline) or crash after every harness
  // had already been measured.
  const value = (flag, i) => {
    const v = argv[i];
    if (v === undefined || v.startsWith("--")) onError(`${flag} needs a value`);
    return v;
  };
  const positive = (flag, raw) => {
    const n = Number(raw);
    if (!Number.isFinite(n) || n <= 0) onError(`${flag} needs a positive number (got ${JSON.stringify(raw)})`);
    return n;
  };
  for (let i = 0; i < argv.length; i++) {
    const a = argv[i];
    if (a === "--harness") args.harness = value(a, ++i).split(",").map((s) => s.trim()).filter(Boolean);
    else if (a === "--out") args.out = value(a, ++i);
    else if (a === "--timeout") args.timeout = positive(a, value(a, ++i));
    else if (a === "--grace") args.grace = positive(a, value(a, ++i));
    else if (a === "--ratio") args.ratio = positive(a, value(a, ++i));
    else if (a === "--repeat") args.repeat = Math.floor(positive(a, value(a, ++i)));
    else if (a === "--count-tokens") args.countTokens = true;
    else if (a === "--isolate") args.isolate = true;
    else if (a === "--json") args.json = true;
    else if (a === "--list") args.list = true;
    else if (a === "--help" || a === "-h") args.help = true;
    else onError(`unknown flag: ${a}`);
  }
  if (args.harness && args.harness.length === 0) onError("--harness needs at least one harness id");
  return args;
}

const HELP = `subagent-tax — how big is your coding harness's per-call prefix?

Captures the first-request prefix (system prompt + tool schemas) each installed
harness sends, using a local capture sink. No provider API calls (except the
opt-in --count-tokens, which posts captured bodies to Anthropic).

usage: node run.mjs [flags]
  --harness a,b,c   harnesses to measure (default: all installed; known: ${HARNESS_IDS.join(", ")})
  --out DIR         report + repro pack dir (default: ./subagent-tax-report)
  --timeout N       per-harness seconds before giving up (default 90)
  --grace N         quiet seconds after last capture before stopping (default 3)
  --ratio N         chars-per-token for estimates (default ${DEFAULT_CHARS_PER_TOKEN})
  --repeat N        run each harness N times; report the median trial plus the
                    observed min/max spread (still free — no provider calls)
  --isolate         measure the harness floor: run every harness against an
                    isolated config (default runs claude against your real one)
  --count-tokens    upgrade anthropic-protocol rows to provider-exact via the
                    free count_tokens endpoint. NOTE: this sends the captured
                    body — your real system prompt — to api.anthropic.com.
                    Needs ANTHROPIC_API_KEY; opt-in, never automatic
  --json            print report.json to stdout instead of the table
  --list            show harness registry + detection status
`;

const OUT_MARKER = ".subagent-tax-report";

// This tool writes into --out and clears stale captures under it. Never do
// that to a directory it did not create: an existing non-empty dir must carry
// our marker or the run refuses.
function claimOutDir(outDir) {
  if (existsSync(outDir)) {
    const entries = readdirSync(outDir);
    if (entries.length > 0 && !entries.includes(OUT_MARKER)) {
      process.stderr.write(
        `subagent-tax: refusing to write into ${outDir} — it is not empty and was not created by this tool.\n` +
        `Pass --out with a new or previously-used report directory.\n`,
      );
      process.exit(2);
    }
  }
  mkdirSync(outDir, { recursive: true });
  writeFileSync(join(outDir, OUT_MARKER), "subagent-tax report directory; safe to delete\n");
}

function detect(reg) {
  for (const bin of reg.binNames) {
    let invocation;
    try {
      invocation = portableProcessInvocation(bin, ["--version"]);
    } catch {
      continue;
    }
    const res = spawnSync(invocation.command, invocation.args, {
      encoding: "utf8",
      timeout: 15000,
      ...harnessSpawnOptions(),
    });
    if (!res.error && res.status === 0) {
      const version = `${res.stdout || res.stderr}`.trim().split("\n")[0].slice(0, 80);
      return { bin, version };
    }
  }
  return null;
}

// Harnesses spawn children (language servers, MCP servers, bundled runtimes).
// POSIX uses a detached process group; Windows uses taskkill /t. The cleanup
// registry keeps Ctrl-C from stranding either tree.
const live = { child: null, workdir: null, server: null };

async function killTree(child) {
  await stopTree(child);
}

function cleanupNow() {
  if (live.child) { try { forceKillTree(live.child); } catch { /* gone */ } }
  if (live.server) { try { live.server.closeAllConnections?.(); live.server.close(); } catch { /* gone */ } }
  if (live.workdir) { try { rmSync(live.workdir, { recursive: true, force: true }); } catch { /* gone */ } }
  live.child = null; live.server = null; live.workdir = null;
}

for (const sig of ["SIGINT", "SIGTERM", "SIGHUP"]) {
  process.on(sig, () => {
    process.stderr.write(`\nsubagent-tax: ${sig} — stopping harness and cleaning up\n`);
    cleanupNow();
    process.exit(130);
  });
}
process.on("exit", cleanupNow);

async function measureHarness(id, reg, args, trial = 1) {
  const found = detect(reg);
  if (!found) return { harness: id, status: "not installed", primary: null };
  if (!reg.launch) {
    return { harness: id, version: found.version, status: "unmeasurable", reason: reg.unmeasurableReason, primary: null };
  }

  const outH = trial > 1 ? resolve(args.out, id, `trial-${trial}`) : resolve(args.out, id);
  const captureDir = join(outH, "captures");
  rmSync(captureDir, { recursive: true, force: true });
  mkdirSync(captureDir, { recursive: true });
  const homeDir = join(outH, "home");
  const sessionDir = join(outH, "sessions");
  mkdirSync(homeDir, { recursive: true });
  mkdirSync(sessionDir, { recursive: true });
  const workdir = mkdtempSync(join(tmpdir(), `subagent-tax-${id}-`));

  const state = { firstLlmAt: null, lastCaptureAt: null, llmSeen: false };
  const { server, port } = await startSink({
    port: 0,
    captureDir,
    onCapture: ({ kind }) => {
      const now = Date.now();
      state.lastCaptureAt = now;
      if (LLM_KINDS.has(kind) && !state.llmSeen) {
        state.llmSeen = true;
        state.firstLlmAt = now;
      }
    },
  });

  const recipe = reg.launch({ port, homeDir, sessionDir, isolate: args.isolate });
  for (const dir of recipe.dirs ?? []) mkdirSync(join(homeDir, dir), { recursive: true });
  for (const file of recipe.files ?? []) {
    const dest = join(homeDir, file.path);
    mkdirSync(dirname(dest), { recursive: true });
    writeFileSync(dest, file.content, { mode: 0o600 });
  }

  // Both streams: harnesses report "Not logged in" / "Model not found" on
  // stdout as often as stderr, and that line is the whole diagnosis.
  const logFd = openSync(join(outH, "harness-output.log"), "w");
  const t0 = Date.now();
  // A configured HTTP proxy would send the captured prefix off-machine —
  // the one thing this tool promises never happens. Loopback is excluded from
  // any inherited proxy and the proxy vars are blanked for the child.
  const noProxyEnv = {
    NO_PROXY: "127.0.0.1,localhost,::1", no_proxy: "127.0.0.1,localhost,::1",
    HTTP_PROXY: "", HTTPS_PROXY: "", ALL_PROXY: "",
    http_proxy: "", https_proxy: "", all_proxy: "",
  };
  const childEnv = { ...process.env, ...noProxyEnv, ...recipe.env };
  const invocation = portableProcessInvocation(recipe.argv[0], recipe.argv.slice(1), { env: childEnv });
  const child = spawn(invocation.command, invocation.args, {
    cwd: workdir,
    env: childEnv,
    stdio: ["ignore", logFd, logFd],
    ...harnessSpawnOptions(),
  });
  live.child = child;
  live.workdir = workdir;
  live.server = server;

  let exited = false;
  child.on("exit", () => { exited = true; });
  const spawnError = await new Promise((resolveWait) => {
    child.on("error", (err) => resolveWait(err));
    child.on("spawn", () => resolveWait(null));
  });

  if (!spawnError) {
    const deadline = t0 + args.timeout * 1000;
    const graceMs = args.grace * 1000;
    await new Promise((resolveWait) => {
      const tick = setInterval(() => {
        const now = Date.now();
        const quiet = state.lastCaptureAt && now - state.lastCaptureAt >= graceMs;
        if ((state.llmSeen && quiet) || now >= deadline || (exited && (state.llmSeen ? quiet : now - t0 > 5000))) {
          clearInterval(tick);
          resolveWait();
        }
      }, 200);
    });
    await killTree(child);
  }
  // close() stops listening but leaves keep-alive sockets attached; without
  // closeAllConnections a surviving grandchild keeps the process alive.
  server.closeAllConnections?.();
  server.close();
  closeSync(logFd);
  rmSync(workdir, { recursive: true, force: true });
  live.child = null; live.workdir = null; live.server = null;

  const records = [];
  const unreadable = [];
  for (const f of readdirSync(captureDir).filter((n) => n.endsWith(".json")).sort()) {
    try {
      records.push(JSON.parse(readFileSync(join(captureDir, f), "utf8")));
    } catch (err) {
      unreadable.push({ file: f, error: String(err?.message ?? err) });
    }
  }
  const { primary, all, skipped, pick_rule } = pickPrimary(records, reg.mcpPattern ? { mcpPattern: reg.mcpPattern } : undefined);

  let tokens = null;
  if (primary) {
    if (args.countTokens && primary.kind === "anthropic-messages" && process.env.ANTHROPIC_API_KEY) {
      const raw = records.find((r) => r.seq === primary.seq);
      const exact = await countTokensAnthropic(raw.body, { apiKey: process.env.ANTHROPIC_API_KEY });
      tokens = exact.error
        ? { ...estimateTokens(primary.total_chars, args.ratio), basis: "est (count_tokens failed)", count_tokens_error: exact.error }
        : exact;
    } else {
      tokens = estimateTokens(primary.total_chars, args.ratio);
    }
  }

  // When nothing was captured, the harness's own first words are the most
  // useful thing we have ("Not logged in", "Model not found", …).
  let stderrHead = null;
  if (!primary) {
    try {
      stderrHead = readFileSync(join(outH, "harness-output.log"), "utf8")
        .split("\n").map((l) => l.replace(/\[[0-9;]*m/g, "").trim()).find(Boolean)?.slice(0, 160) ?? null;
    } catch { /* no log */ }
  }

  const status = spawnError
    ? `spawn failed: ${spawnError.code ?? spawnError.message}`
    : primary
      ? "ok"
      : reg.confirmed
        ? "no capture"
        : "no capture (recipe unconfirmed)";

  const usingRealConfig = Boolean(reg.touchesRealConfig) && !args.isolate;
  return {
    harness: id,
    variant: usingRealConfig ? reg.variant : (reg.isolateVariant ?? reg.variant),
    version: found.version,
    status,
    trial,
    invocation: { argv: recipe.argv, env: recipe.env },
    seconds_to_first_capture: state.firstLlmAt ? (state.firstLlmAt - t0) / 1000 : null,
    captures_total: records.length,
    pick_rule,
    harness_said: stderrHead,
    primary,
    all_captures: all.map(({ tools, ...rest }) => rest),
    skipped_captures: [...(skipped ?? []).map((s) => ({ seq: s.seq, kind: s.kind, error: s.error })), ...unreadable],
    tokens,
    recipe_confirmed: reg.confirmed,
    mcp_pattern_validated: Boolean(reg.mcpPattern),
    touches_real_config: usingRealConfig,
    notes: reg.notes,
  };
}

// Repeat runs report the MEDIAN trial as the row, plus the observed spread.
// A spread is a range across N runs on one machine — not a confidence interval
// and not a variance claim; the report labels it as observed min/max.
function summarizeTrials(trials) {
  if (trials.length === 1) return trials[0];
  const ok = trials.filter((t) => t.primary);
  if (ok.length === 0) return { ...trials[0], trials: trials.length };
  const sorted = [...ok].sort((a, b) => a.primary.total_chars - b.primary.total_chars);
  const median = sorted[Math.floor((sorted.length - 1) / 2)];
  const sizes = sorted.map((t) => t.primary.total_chars);
  return {
    ...median,
    trials: trials.length,
    trials_ok: ok.length,
    spread: { min_chars: sizes[0], max_chars: sizes[sizes.length - 1], median_chars: median.primary.total_chars, all_chars: sizes },
  };
}

function topSchemaHogs(rows) {
  const out = [];
  for (const row of rows) {
    if (!row.primary || row.primary.tools_count === 0) continue;
    const top = [...row.primary.tools].sort((a, b) => b.chars - a.chars).slice(0, 5);
    out.push(`  ${row.harness}: ${top.map((t) => `${t.name} ${(t.chars / 1024).toFixed(1)}k`).join(" · ")}`);
  }
  return out.length ? `\ntop schema weight per harness (chars):\n${out.join("\n")}` : "";
}

async function main() {
  const args = parseArgs(process.argv.slice(2));
  if (args.help) {
    process.stdout.write(HELP);
    return;
  }
  if (args.list) {
    for (const id of HARNESS_IDS) {
      const reg = HARNESSES[id];
      const found = detect(reg);
      const suffix = !reg.launch ? ` — unmeasurable: ${reg.unmeasurableReason}` : reg.confirmed ? "" : " — recipe unconfirmed";
      process.stdout.write(`${id.padEnd(14)} ${found ? `installed (${found.version})` : "not installed"}${suffix}\n`);
    }
    return;
  }

  const ids = args.harness ?? HARNESS_IDS;
  const unknown = ids.filter((id) => !HARNESSES[id]);
  if (unknown.length) {
    process.stderr.write(`unknown harness: ${unknown.join(", ")} (known: ${HARNESS_IDS.join(", ")})\n`);
    process.exit(2);
  }

  if (args.countTokens && !process.env.ANTHROPIC_API_KEY) {
    process.stderr.write("warning: --count-tokens needs ANTHROPIC_API_KEY; anthropic rows will stay estimated.\n");
  }
  claimOutDir(resolve(args.out));

  // Disclose real-config side effects BEFORE launching anything.
  if (!args.isolate) {
    for (const id of ids) {
      const reg = HARNESSES[id];
      if (reg.touchesRealConfig && detect(reg)) {
        process.stderr.write(
          `note: the ${id} row runs against your real config — it ${reg.realConfigEffects}.\n` +
          `      That is deliberate (your installed setup is the tax being measured). Use --isolate to measure the harness floor instead.\n`,
        );
      }
    }
  }

  const rows = [];
  for (const id of ids) {
    const trials = [];
    for (let t = 1; t <= args.repeat; t++) {
      process.stderr.write(`measuring ${id}${args.repeat > 1 ? ` (trial ${t}/${args.repeat})` : ""}...\n`);
      const row = await measureHarness(id, HARNESSES[id], args, t);
      trials.push(row);
      if (!row.primary && (row.status === "not installed" || row.status === "unmeasurable")) break;
    }
    rows.push(summarizeTrials(trials));
  }

  const report = {
    tool: "subagent-tax",
    tool_version: TOOL_VERSION,
    date: new Date().toISOString(),
    basis: "inferred",
    // No hostname: machine names are commonly a person's name.
    platform: { os: platform(), arch: arch(), node: process.version },
    prompt: PROMPT,
    calibration: { chars_per_token: args.ratio, note: CALIBRATION_NOTE },
    repeat: args.repeat,
    honesty: args.repeat > 1 ? [...HONESTY_LINES, SPREAD_NOTE] : HONESTY_LINES,
    rows,
  };
  writeReport(resolve(args.out), report);
  const artifactCount = writeManifest(resolve(args.out));

  if (args.json) {
    process.stdout.write(`${JSON.stringify(report, null, 2)}\n`);
    return;
  }
  const measurable = rows.filter((r) => r.status !== "not installed");
  process.stdout.write(`\nThe Subagent Tax — first-request prefix per harness (this machine)\n\n`);
  process.stdout.write(`${renderTable(measurable)}\n\n`);
  process.stdout.write(`${LEGEND}\n`);
  process.stdout.write(`${topSchemaHogs(measurable)}\n\n`);
  const extraNotes = args.repeat > 1 ? [SPREAD_NOTE] : [];
  const oddPick = measurable.filter((r) => r.pick_rule && r.pick_rule !== "most-tools");
  for (const r of oddPick) extraNotes.push(`${r.harness}: primary capture chosen by ${r.pick_rule}.`);
  for (const r of measurable.filter((x) => !x.primary && x.harness_said)) {
    extraNotes.push(`${r.harness}: no capture — the harness said "${r.harness_said}"`);
  }
  const skipped = measurable.filter((r) => r.skipped_captures?.length);
  for (const r of skipped) extraNotes.push(`${r.harness}: ${r.skipped_captures.length} capture(s) could not be analyzed and were skipped (see report.json).`);
  process.stdout.write(`${renderHonesty(extraNotes)}\n\n`);
  process.stdout.write(
    `repro pack: ${resolve(args.out)} (${artifactCount} artifacts, sha256 manifest; auth headers + known account/session ids redacted — bodies are still your real system prompt, review before sharing)\n`,
  );
  process.stdout.write(`${FOOTER}\n`);
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  main().catch((err) => {
    process.stderr.write(`subagent-tax: ${err.stack ?? err}\n`);
    process.exit(1);
  });
}
