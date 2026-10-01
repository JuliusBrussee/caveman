import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { chmodSync, existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, statSync, utimesSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import test from "node:test";

const dist = join(dirname(fileURLToPath(import.meta.url)), "..", "dist");
const cli = join(dist, "index.js");
const fastHook = join(dist, "native-hook-fast.js");
const posix = process.platform !== "win32";

const bigPlan = {
  schema: "caveman.learn.v1",
  sinks: [
    { sink_id: "claude_md_weight:project", title: "Project CLAUDE.md loads every turn", class: "reducible", tokens_per_turn: 9800, tokens_per_day_rate: 1 },
    { sink_id: "config_tax:baseline", title: "Config baseline", class: "load_bearing", tokens_per_turn: 20000, tokens_per_day_rate: 1 },
    { sink_id: "tiny", title: "Tiny", class: "reducible", tokens_per_turn: 300, tokens_per_day_rate: 1 },
  ],
};

// Every path lives under one temp HOME: CAVEMAN_HOME, CLAUDE_CONFIG_DIR and
// ~/.caveman-cloud never point at the real machine config.
function sandbox(extra = {}) {
  const home = mkdtempSync(join(tmpdir(), "cave-autopilot-"));
  const bin = join(home, "bin");
  mkdirSync(bin, { recursive: true });
  const proxy = join(bin, "fake-proxy");
  const calls = join(home, "proxy-calls.log");
  const planFile = join(home, "plan.json");
  writeFileSync(planFile, JSON.stringify(bigPlan));
  // FAKE_PROXY_OLD mimics a pre-capabilities proxy: unknown subcommand, exit 1.
  writeFileSync(proxy, `#!/bin/sh
if [ "$1 $2" = "learn capabilities" ]; then
  echo "$*" >> "${calls}.probe"
  if [ -n "$FAKE_PROXY_OLD" ]; then echo "unknown learn subcommand: capabilities" >&2; exit 1; fi
  echo '{"schema":"caveman.learn.capabilities.v1","no_remember":true,"reports_home":true,"memory_health":true}'
  exit 0
fi
echo "$*" >> "${calls}"
if [ -n "$FAKE_PROXY_SLEEP" ]; then sleep "$FAKE_PROXY_SLEEP"; fi
cat "${planFile}"
`, { mode: 0o755 });
  chmodSync(proxy, 0o755);
  const env = {
    ...process.env,
    HOME: home,
    CAVEMAN_HOME: join(home, ".caveman"),
    CLAUDE_CONFIG_DIR: join(home, ".claude"),
    CAVE_NO_KEYCHAIN: "1",
    CAVEMAN_TELEMETRY: "0",
    NO_COLOR: "1",
    CI: "1",
    CAVEMAN_LEARN_AUTOPILOT: "1",
    CAVEMAN_PROXY_BIN: proxy,
    ...extra,
  };
  mkdirSync(join(home, ".caveman-cloud"), { recursive: true });
  writeFileSync(join(home, ".caveman-cloud", "config.json"), JSON.stringify({ wrap: { proxy: false } }));
  const runtime = join(env.CAVEMAN_HOME, "runtime");
  return {
    home, env, planFile, runtime,
    calls: () => existsSync(calls) ? readFileSync(calls, "utf8").trim().split("\n").filter(Boolean) : [],
    probes: () => existsSync(`${calls}.probe`) ? readFileSync(`${calls}.probe`, "utf8").trim().split("\n").filter(Boolean) : [],
    state: () => JSON.parse(readFileSync(join(runtime, "learn-autopilot.json"), "utf8")),
    seed(state) {
      mkdirSync(runtime, { recursive: true });
      writeFileSync(join(runtime, "learn-autopilot.json"), JSON.stringify(state));
    },
    cleanup: () => rmSync(home, { recursive: true, force: true }),
  };
}

function run(args, env, { stdin = "", holdStdin = false } = {}) {
  return new Promise((resolve, reject) => {
    const started = Date.now();
    const child = spawn(process.execPath, args, { env, stdio: ["pipe", "pipe", "pipe"] });
    let stdout = "";
    let stderr = "";
    child.stdout.on("data", (d) => (stdout += d));
    child.stderr.on("data", (d) => (stderr += d));
    child.on("error", reject);
    child.on("exit", (code) => {
      if (holdStdin) child.stdin.destroy();
      resolve({ code, stdout, stderr, ms: Date.now() - started });
    });
    if (holdStdin) child.stdin.write(stdin);
    else child.stdin.end(stdin);
  });
}

const hook = (entry, agent, payload, env, opts = {}) =>
  run([entry, "native-hook", agent], env, { ...opts, stdin: JSON.stringify(payload) });

async function waitFor(check, ms = 8000) {
  const deadline = Date.now() + ms;
  while (Date.now() < deadline) {
    if (check()) return true;
    await new Promise((r) => setTimeout(r, 50));
  }
  return false;
}

const sessionStart = (source) => ({ hook_event_name: "SessionStart", session_id: "s1", source });

test("SessionEnd spawns a detached scan, then throttles", { skip: !posix }, async () => {
  const box = sandbox();
  try {
    const out = await hook(fastHook, "claude", { hook_event_name: "SessionEnd", session_id: "s1" }, box.env);
    assert.equal(out.code, 0, out.stderr);
    assert.ok(await waitFor(() => existsSync(join(box.runtime, "learn-autopilot.json")) && box.state().last_scan_at), "detached scan never finished");
    assert.deepEqual(box.calls(), [`learn scan --write-report --no-remember --reports-home ${join(box.runtime, "learn-autopilot")}`]);
    assert.ok(!existsSync(join(box.runtime, "learn-autopilot.lock")), "lock released");
    // First scan is a baseline: nothing announced, big eligible sink recorded as seen.
    assert.deepEqual(box.state().seen, ["claude_md_weight:project"]);
    assert.ok(!existsSync(join(box.runtime, "learn-autopilot-nudge.json")));

    const again = await hook(cli, "codex", { hook_event_name: "SessionEnd", session_id: "s2" }, box.env);
    assert.equal(again.code, 0, again.stderr);
    await new Promise((r) => setTimeout(r, 800));
    assert.equal(box.calls().length, 1, "second session end inside the window must not rescan");
  } finally { box.cleanup(); }
});

test("SessionEnd hook never waits on the scan, even with stdin held open", { skip: !posix }, async () => {
  const box = sandbox({ FAKE_PROXY_SLEEP: "5" });
  try {
    const out = await hook(fastHook, "claude", { hook_event_name: "SessionEnd", session_id: "s1" }, box.env, { holdStdin: true });
    assert.equal(out.code, 0, out.stderr);
    assert.ok(out.ms < 2000, `hook took ${out.ms}ms`);
    assert.ok(await waitFor(() => box.calls().length === 1, 4000), "scan child did not start");
    assert.ok(existsSync(join(box.runtime, "learn-autopilot.lock")), "child holds the lock while scanning");
  } finally { box.cleanup(); }
});

test("opt-out: env 0, config off, and CI default all skip the spawn", { skip: !posix }, async () => {
  for (const [label, extra, config] of [
    ["env", { CAVEMAN_LEARN_AUTOPILOT: "0" }, undefined],
    ["config", { CAVEMAN_LEARN_AUTOPILOT: "" }, false],
    ["ci", { CAVEMAN_LEARN_AUTOPILOT: "" }, undefined],
  ]) {
    const box = sandbox(extra);
    try {
      if (config !== undefined) writeFileSync(join(box.home, ".caveman-cloud", "config.json"), JSON.stringify({ learnAutopilot: config }));
      const out = await hook(fastHook, "claude", { hook_event_name: "SessionEnd", session_id: "s1" }, box.env);
      assert.equal(out.code, 0, out.stderr);
      await new Promise((r) => setTimeout(r, 600));
      assert.deepEqual(box.calls(), [], `${label} opt-out still scanned`);
      const status = await run([cli, "learn", "autopilot", "status"], box.env);
      assert.match(status.stdout, /learn autopilot: off/, label);
    } finally { box.cleanup(); }
  }
});

test("learn autopilot on/off persists in config and status reports it", async () => {
  const box = sandbox({ CAVEMAN_LEARN_AUTOPILOT: "" });
  try {
    const on = await run([cli, "learn", "autopilot", "on"], box.env);
    assert.equal(on.code, 0, on.stderr);
    assert.match(on.stdout, /learn autopilot: on \(config\)/);
    assert.match(on.stdout, /last scan:\s+never/);
    assert.match(on.stdout, /next scan:\s+next session end/);
    const cfg = JSON.parse(readFileSync(join(box.home, ".caveman-cloud", "config.json"), "utf8"));
    assert.equal(cfg.learnAutopilot, true);
    assert.deepEqual(cfg.wrap, { proxy: false }, "other config keys preserved");
    const off = await run([cli, "learn", "autopilot", "off"], box.env);
    assert.match(off.stdout, /learn autopilot: off \(config\)/);
    const help = await run([cli, "learn", "--help"], box.env);
    assert.match(help.stdout, /autopilot/);
  } finally { box.cleanup(); }
});

test("lock contention: live lock skips, stale lock is taken over", { skip: !posix }, async () => {
  const box = sandbox();
  try {
    mkdirSync(box.runtime, { recursive: true });
    const lock = join(box.runtime, "learn-autopilot.lock");
    writeFileSync(lock, `${process.pid}\n`);
    const busy = await run([cli, "learn", "autopilot", "run"], box.env);
    assert.equal(busy.code, 0, busy.stderr);
    assert.deepEqual(box.calls(), [], "live lock holder must block a second scan");
    assert.ok(existsSync(lock), "foreign lock untouched");

    writeFileSync(lock, "999999999\n");
    const old = new Date(Date.now() - 10 * 60 * 60 * 1000);
    utimesSync(lock, old, old);
    const stale = await run([cli, "learn", "autopilot", "run"], box.env);
    assert.equal(stale.code, 0, stale.stderr);
    assert.equal(box.calls().length, 1);
    assert.ok(!existsSync(lock));
  } finally { box.cleanup(); }
});

test("scan failure records last_error and still throttles", { skip: !posix }, async () => {
  const box = sandbox();
  try {
    writeFileSync(box.env.CAVEMAN_PROXY_BIN, `#!/bin/sh
if [ "$1 $2" = "learn capabilities" ]; then echo '{"schema":"caveman.learn.capabilities.v1","no_remember":true,"reports_home":true,"memory_health":true}'; exit 0; fi
echo 'boom: no sessions dir' >&2
exit 3
`, { mode: 0o755 });
    const failed = await run([cli, "learn", "autopilot", "run"], box.env);
    assert.equal(failed.code, 1);
    assert.equal(box.state().last_error, "boom: no sessions dir");
    assert.ok(box.state().last_attempt_at);
    const status = await run([cli, "learn", "autopilot", "status"], box.env);
    assert.match(status.stdout, /last error:\s+boom: no sessions dir/);
    assert.doesNotMatch(status.stdout, /next session end/);
  } finally { box.cleanup(); }
});

test("scan overrun is killed at CAVE_LEARN_TIMEOUT", { skip: !posix }, async () => {
  const box = sandbox({ FAKE_PROXY_SLEEP: "30", CAVE_LEARN_TIMEOUT: "1" });
  try {
    const out = await run([cli, "learn", "autopilot", "run"], box.env);
    assert.equal(out.code, 1);
    assert.ok(out.ms < 10_000, `took ${out.ms}ms`);
    assert.match(box.state().last_error, /timed out after 1s/);
    assert.ok(!existsSync(join(box.runtime, "learn-autopilot.lock")));
  } finally { box.cleanup(); }
});

test("new big sink is announced once, user-visible, only on startup/clear", { skip: !posix }, async () => {
  const box = sandbox({ CAVEMAN_LEARN_AUTOPILOT_HOURS: "0.000001" });
  try {
    box.seed({ seen: [] });
    const scan = await run([cli, "learn", "autopilot", "run"], box.env);
    assert.equal(scan.code, 0, scan.stderr);
    const expected = "caveman learn: new finding — Project CLAUDE.md loads every turn (~9.8k tokens in every message, estimate). Run `caveman learn` to review.";

    for (const source of ["compact", "resume", "fork", undefined]) {
      const out = await hook(cli, "claude", sessionStart(source), box.env);
      assert.equal(out.code, 0, out.stderr);
      assert.equal(JSON.parse(out.stdout).systemMessage, undefined, `announced on ${source}`);
    }
    const first = await hook(cli, "claude", sessionStart("startup"), box.env);
    const parsed = JSON.parse(first.stdout);
    assert.equal(parsed.systemMessage, expected);
    assert.doesNotMatch(parsed.hookSpecificOutput?.additionalContext ?? "", /caveman learn:/, "nudge stays out of model context");

    const second = await hook(cli, "claude", sessionStart("clear"), box.env);
    assert.equal(JSON.parse(second.stdout).systemMessage, undefined, "announced twice");

    // Rescan: same sink stays seen, never re-announced.
    await new Promise((r) => setTimeout(r, 20));
    await run([cli, "learn", "autopilot", "run"], box.env);
    assert.equal(box.calls().length, 2);
    assert.ok(!existsSync(join(box.runtime, "learn-autopilot-nudge.json")));

    const status = await run([cli, "learn", "autopilot", "status"], box.env);
    assert.ok(status.stdout.includes(expected), status.stdout);
  } finally { box.cleanup(); }
});

test("several new sinks collapse into one line; unclaimed nudge is not overwritten", { skip: !posix }, async () => {
  const box = sandbox({ CAVEMAN_LEARN_AUTOPILOT_HOURS: "0.000001" });
  try {
    box.seed({ seen: [] });
    await run([cli, "learn", "autopilot", "run"], box.env);
    writeFileSync(box.planFile, JSON.stringify({ sinks: [
      ...bigPlan.sinks,
      { sink_id: "recurring_context:x", title: "Deploy notes pasted each session", class: "recurring_context", tokens_per_turn: 2400 },
    ] }));
    await new Promise((r) => setTimeout(r, 20));
    await run([cli, "learn", "autopilot", "run"], box.env);
    assert.ok(!box.state().seen.includes("recurring_context:x"), "sink behind an unclaimed nudge stays unseen");
    const first = await hook(cli, "gemini", sessionStart("startup"), box.env);
    assert.match(JSON.parse(first.stdout).systemMessage, /new finding — Project CLAUDE\.md/);
    await new Promise((r) => setTimeout(r, 20));
    await run([cli, "learn", "autopilot", "run"], box.env);
    const next = await hook(cli, "codex", sessionStart("startup"), box.env);
    assert.match(JSON.parse(next.stdout).systemMessage, /new finding — Deploy notes pasted each session \(~2\.4k tokens per message on average, estimate\)/);

    // Multi-sink single line.
    box.seed({ seen: [] });
    await run([cli, "learn", "autopilot", "run"], box.env);
    const multi = await hook(cli, "claude", sessionStart("startup"), box.env);
    assert.equal(JSON.parse(multi.stdout).systemMessage,
      "caveman learn: 2 new findings, biggest — Project CLAUDE.md loads every turn (~9.8k tokens in every message, estimate). Run `caveman learn` to review.");
  } finally { box.cleanup(); }
});

test("SessionStart nudge path is not skipped by the fast hook and refuses a symlinked nudge", { skip: !posix }, async () => {
  const box = sandbox();
  try {
    mkdirSync(box.runtime, { recursive: true });
    const secret = join(box.home, "secret.json");
    writeFileSync(secret, JSON.stringify({ line: "leaked" }));
    const { symlinkSync } = await import("node:fs");
    symlinkSync(secret, join(box.runtime, "learn-autopilot-nudge.json"));
    const out = await hook(fastHook, "claude", sessionStart("startup"), box.env);
    assert.equal(out.code, 0, out.stderr);
    assert.equal(JSON.parse(out.stdout).systemMessage, undefined);
    assert.ok(statSync(secret).isFile(), "symlink target untouched");

    writeFileSync(join(box.runtime, "learn-autopilot-nudge.json"), JSON.stringify({ line: "caveman learn: hi", sink_ids: [], created_at: "x" }));
    const held = await hook(fastHook, "claude", sessionStart("startup"), box.env, { holdStdin: true });
    assert.equal(held.code, 0, held.stderr);
    assert.ok(held.ms < 3000, `SessionStart took ${held.ms}ms with stdin held open`);
    assert.equal(JSON.parse(held.stdout).systemMessage, "caveman learn: hi");
    assert.ok(!existsSync(join(box.runtime, "learn-autopilot-nudge.inflight.json")), "relayed nudge confirmed by the fast hook");
    assert.equal(JSON.parse(readFileSync(join(box.runtime, "learn-autopilot-announced.json"), "utf8")).line, "caveman learn: hi");
  } finally { box.cleanup(); }
});

test("new broken-import and memory-truncation findings qualify for the nudge, once", { skip: !posix }, async () => {
  const box = sandbox({ CAVEMAN_LEARN_AUTOPILOT_HOURS: "0.000001" });
  try {
    box.seed({ seen: [] });
    await run([cli, "learn", "autopilot", "run"], box.env);
    await hook(cli, "claude", sessionStart("startup"), box.env); // drain the token-sink nudge
    const doctor = [
      { sink_id: "memory_health:broken_imports:abcd1234", title: "CLAUDE.md (project) has 2 @imports that resolve to missing files", class: "behavioral", tokens_per_turn: 0, evidence: { repo: "webapp" } },
      { sink_id: "memory_health:memory_truncation:memory", title: "MEMORY.md is 240 lines; 40 lines past the cutoff never load", class: "behavioral", tokens_per_turn: 0 },
      { sink_id: "memory_health:stale_references:ffff0000", title: "CLAUDE.md names 3 repo paths that no longer exist", class: "behavioral", tokens_per_turn: 0 },
    ];
    writeFileSync(box.planFile, JSON.stringify({ sinks: [...bigPlan.sinks, ...doctor] }));
    await new Promise((r) => setTimeout(r, 20));
    await run([cli, "learn", "autopilot", "run"], box.env);
    const out = await hook(cli, "claude", sessionStart("startup"), box.env);
    assert.equal(JSON.parse(out.stdout).systemMessage,
      "caveman learn: memory files (webapp) — CLAUDE.md (project) has 2 @imports that resolve to missing files (+1 more). Run `caveman learn --all` to review.");
    assert.ok(!box.state().seen.includes("memory_health:stale_references:ffff0000"), "only broken imports and truncation qualify");
    await new Promise((r) => setTimeout(r, 20));
    await run([cli, "learn", "autopilot", "run"], box.env);
    const again = await hook(cli, "claude", sessionStart("startup"), box.env);
    assert.equal(JSON.parse(again.stdout).systemMessage, undefined, "doctor finding announced twice");
  } finally { box.cleanup(); }
});

test("nudge line counts doctor findings behind the biggest finding", async () => {
  const { nudgeLine } = await import(join(dist, "learn-autopilot.js"));
  assert.equal(nudgeLine([
    { title: "Big sink", tokens_per_turn: 5000 },
    { title: "Broken import", tokens_per_turn: 0, doctor: true },
  ]), "caveman learn: new finding — Big sink (~5.0k tokens in every message, estimate), plus 1 memory-file finding. Run `caveman learn` to review.");
  assert.equal(nudgeLine([{ title: "Pasted block", tokens_per_turn: 2400, class: "recurring_context" }]),
    "caveman learn: new finding — Pasted block (~2.4k tokens per message on average, estimate). Run `caveman learn` to review.");
  assert.equal(nudgeLine([{ title: "Big CLAUDE.md", tokens_per_turn: 2400, class: "reducible" }]),
    "caveman learn: new finding — Big CLAUDE.md (~2.4k tokens in every message, estimate). Run `caveman learn` to review.");
});

test("an unconfirmed in-flight nudge re-shows once after 10 minutes, never twice", { skip: !posix }, async () => {
  const box = sandbox();
  try {
    mkdirSync(box.runtime, { recursive: true });
    const inflight = join(box.runtime, "learn-autopilot-nudge.inflight.json");
    const park = (minutesAgo, extra = {}) => writeFileSync(inflight, JSON.stringify({
      line: "caveman learn: lost line", sink_ids: ["a"], created_at: "x", claimed_at: new Date(Date.now() - minutesAgo * 60_000).toISOString(), ...extra,
    }));
    park(1);
    const fresh = await hook(cli, "claude", sessionStart("startup"), box.env);
    assert.equal(JSON.parse(fresh.stdout).systemMessage, undefined, "a just-claimed nudge belongs to another session");
    park(11);
    const again = await hook(cli, "claude", sessionStart("startup"), box.env);
    assert.equal(JSON.parse(again.stdout).systemMessage, "caveman learn: lost line");
    assert.ok(!existsSync(inflight), "re-shown line confirmed");
    park(11, { reshown: true });
    const third = await hook(cli, "claude", sessionStart("startup"), box.env);
    assert.equal(JSON.parse(third.stdout).systemMessage, undefined, "one re-show max");
  } finally { box.cleanup(); }
});

test("a proxy without learn capabilities never scans; the probe is cached per binary", { skip: !posix }, async () => {
  const box = sandbox({ CAVEMAN_LEARN_AUTOPILOT_HOURS: "0.000001", FAKE_PROXY_OLD: "1" });
  try {
    box.seed({ seen: [] });
    const first = await run([cli, "learn", "autopilot", "run"], box.env);
    assert.equal(first.code, 1);
    assert.deepEqual(box.calls(), [], "an old proxy must not be asked to scan");
    assert.equal(box.state().last_error, "proxy too old for autopilot (needs learn capabilities)");
    const status = await run([cli, "learn", "autopilot", "status"], box.env);
    assert.match(status.stdout, /last error: +proxy too old for autopilot \(needs learn capabilities\)/);
    await new Promise((r) => setTimeout(r, 20));
    await run([cli, "learn", "autopilot", "run"], box.env);
    assert.equal(box.probes().length, 1, "unchanged binary is probed once");

    // Upgrading the binary (new mtime/size) re-probes and scans.
    const upgraded = { ...box.env };
    delete upgraded.FAKE_PROXY_OLD;
    const proxy = box.env.CAVEMAN_PROXY_BIN;
    writeFileSync(proxy, readFileSync(proxy, "utf8") + "\n# upgraded\n");
    await new Promise((r) => setTimeout(r, 20));
    const ok = await run([cli, "learn", "autopilot", "run"], upgraded);
    assert.equal(ok.code, 0, ok.stderr);
    assert.equal(box.probes().length, 2);
    assert.equal(box.calls().length, 1);
    assert.equal(box.state().last_error, undefined);
  } finally { box.cleanup(); }
});

test("claims are token-owned: a newer nudge waits behind an unconfirmed one, and only its claimer confirms", async () => {
  const home = mkdtempSync(join(tmpdir(), "cave-nudge-token-"));
  const saved = { CAVEMAN_HOME: process.env.CAVEMAN_HOME, CAVEMAN_LEARN_AUTOPILOT: process.env.CAVEMAN_LEARN_AUTOPILOT };
  process.env.CAVEMAN_HOME = join(home, ".caveman");
  process.env.CAVEMAN_LEARN_AUTOPILOT = "1";
  try {
    const { claimLearnNudge, confirmLearnNudge } = await import(join(dist, "learn-autopilot.js"));
    const runtime = join(home, ".caveman", "runtime");
    mkdirSync(runtime, { recursive: true });
    const nudge = join(runtime, "learn-autopilot-nudge.json");
    const inflight = join(runtime, "learn-autopilot-nudge.inflight.json");
    const announced = join(runtime, "learn-autopilot-announced.json");
    const put = (line) => writeFileSync(nudge, JSON.stringify({ line, sink_ids: [], created_at: "x" }));
    const age = (minutes) => {
      const record = JSON.parse(readFileSync(inflight, "utf8"));
      writeFileSync(inflight, JSON.stringify({ ...record, claimed_at: new Date(Date.now() - minutes * 60_000).toISOString() }));
    };

    // Scenario 1: A claims N1 and its output is lost. N2 arrives; B must wait.
    put("N1");
    assert.equal(claimLearnNudge("startup", "A"), "N1");
    put("N2");
    assert.equal(claimLearnNudge("startup", "B"), undefined, "N2 waits behind unconfirmed N1");
    assert.ok(existsSync(nudge), "N2 still pending");
    age(11);
    assert.equal(claimLearnNudge("startup", "C"), "N1", "N1 gets its one re-show first");

    // Scenario 2: A's late confirm must not confirm C's re-show.
    confirmLearnNudge("A");
    assert.ok(existsSync(inflight) && !existsSync(announced), "stale token confirmed nothing");
    confirmLearnNudge("C");
    assert.ok(!existsSync(inflight));
    assert.equal(JSON.parse(readFileSync(announced, "utf8")).line, "N1");
    assert.equal(claimLearnNudge("startup", "D"), "N2", "then N2");

    // A failed in-flight write still shows the line (and never loses it).
    confirmLearnNudge("D");
    mkdirSync(inflight);
    put("N3");
    assert.equal(claimLearnNudge("startup", "E"), "N3");
  } finally {
    for (const [key, value] of Object.entries(saved)) value === undefined ? delete process.env[key] : (process.env[key] = value);
    rmSync(home, { recursive: true, force: true });
  }
});
