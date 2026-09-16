import { test } from "node:test";
import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { createHash } from "node:crypto";
import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { isolatedCliEnv, runCli } from "./_cli.mjs";

const cli = join(dirname(fileURLToPath(import.meta.url)), "..", "dist", "index.js");

function run(argv, env) {
  return new Promise((resolve, reject) => {
    const child = spawn(process.execPath, [cli, ...argv], { env });
    let stdout = "";
    let stderr = "";
    child.stdout.on("data", (d) => (stdout += d));
    child.stderr.on("data", (d) => (stderr += d));
    child.on("exit", (code) => resolve({ code, stdout, stderr }));
    child.on("error", reject);
  });
}

function stubProxy(dir) {
  const path = join(dir, "caveman-proxy-stub.mjs");
  writeFileSync(
    path,
    `#!/usr/bin/env node
import fs from "node:fs";
import http from "node:http";
const argv = process.argv.slice(2);
const log = process.env.STUB_PROXY_LOG;
if (log) fs.appendFileSync(log, JSON.stringify({argv, env:{listen:process.env.CAVEMAN_LISTEN,label:process.env.CAVEMAN_LABEL,mode:process.env.CAVEMAN_MODE}})+"\\n");
if (argv[0] === "serve") {
  const [host, port] = String(process.env.CAVEMAN_LISTEN || "127.0.0.1:0").split(":");
  http.createServer((req, res) => res.end("{}")).listen(Number(port), host);
} else if (argv[0] === "trial" && argv[1] === "report") {
  console.log(JSON.stringify({trial_id: argv[argv.indexOf("--trial-id")+1], report: "/tmp/caveman-trial.html", basis: "inferred"}));
} else if (argv[0] === "learn" && argv[1] === "report") {
  console.log(JSON.stringify({
    schema: "caveman.learn.v1",
    basis: "inferred",
    sessions_scanned: 3,
    sessions_by_source: {claude: 3},
    report: "/tmp/caveman-learn.html",
    cave_score: {score: 73, basis: "inferred", scope: "local_setup"},
    sinks: [{
      sink_id: "recurring_context:test",
      title: "Repeated setup context",
      class: "recurring_context",
      basis: "inferred",
      tokens_per_turn: 900,
      tokens_per_day_rate: 2700
    }]
  }));
} else {
  console.log(JSON.stringify({ok:true, argv}));
}
`,
    { mode: 0o755 },
  );
  return path;
}

test("trial starts record-mode proxy, wraps child through temp URL, reports, and preserves child exit", async () => {
  const dir = mkdtempSync(join(tmpdir(), "cave-trial-"));
  const envFile = join(dir, "child-url.txt");
  const logFile = join(dir, "proxy.log");
  const proxy = stubProxy(dir);
  const env = { ...process.env, CAVEMAN_PROXY_BIN: proxy, STUB_PROXY_LOG: logFile, CHILD_ENV_FILE: envFile, NO_COLOR: "1" };

  const script = `require("node:fs").writeFileSync(process.env.CHILD_ENV_FILE, process.env.OPENAI_BASE_URL || ""); process.exit(7)`;
  const out = await run(["trial", "--trial-id", "trial_cli", "--", "node", "-e", script], env);

  assert.equal(out.code, 7, `trial must preserve child exit code; stderr=${out.stderr}`);
  assert.match(out.stdout, /"report":\s*"\/tmp\/caveman-trial\.html"/);
  const childURL = readFileSync(envFile, "utf8");
  assert.match(childURL, /^http:\/\/127\.0\.0\.1:\d+$/);

  const logs = readFileSync(logFile, "utf8").trim().split("\n").map((line) => JSON.parse(line));
  assert.ok(logs.some((l) => l.argv[0] === "trial" && l.argv[1] === "start"));
  assert.ok(logs.some((l) => l.argv[0] === "trial" && l.argv[1] === "finish"));
  const serve = logs.find((l) => l.argv[0] === "serve");
  assert.equal(serve.env.label, "trial:trial_cli");
  assert.equal(serve.env.mode, "record");
});

test("tools trial routes through existing local trial machinery", async () => {
  const dir = mkdtempSync(join(tmpdir(), "cave-tools-trial-"));
  const envFile = join(dir, "child-url.txt");
  const logFile = join(dir, "proxy.log");
  const proxy = stubProxy(dir);
  const env = { ...process.env, CAVEMAN_PROXY_BIN: proxy, STUB_PROXY_LOG: logFile, CHILD_ENV_FILE: envFile, NO_COLOR: "1" };

  const script = `require("node:fs").writeFileSync(process.env.CHILD_ENV_FILE, process.env.OPENAI_BASE_URL || "")`;
  const out = await run(["tools", "trial", "--trial-id", "trial_tools", "--", "node", "-e", script], env);

  assert.equal(out.code, 0, out.stderr);
  assert.match(out.stdout, /"trial_id":\s*"trial_tools"/);
  const logs = readFileSync(logFile, "utf8").trim().split("\n").map((line) => JSON.parse(line));
  assert.ok(logs.some((l) => l.argv[0] === "trial" && l.argv[1] === "start"));
  assert.ok(logs.some((l) => l.argv[0] === "trial" && l.argv[1] === "finish"));
});

test("usage import delegates to proxy binary", async () => {
  const dir = mkdtempSync(join(tmpdir(), "cave-usage-import-"));
  const proxy = stubProxy(dir);
  const out = await run(["usage", "import", "codex", "--since", "30d"], { ...process.env, CAVEMAN_PROXY_BIN: proxy });
  assert.equal(out.code, 0, out.stderr);
  assert.match(out.stdout, /"usage","import","codex","--since","30d"/);
});

test("usage link codex delegates to proxy import path without storing secrets", async () => {
  const dir = mkdtempSync(join(tmpdir(), "cave-usage-link-codex-"));
  const proxy = stubProxy(dir);
  const out = await run(["usage", "link", "codex"], { ...process.env, CAVEMAN_PROXY_BIN: proxy });
  assert.equal(out.code, 0, out.stderr);
  const body = JSON.parse(out.stdout);
  assert.equal(body.linked, "codex");
  assert.deepEqual(body.refresh.argv, ["usage", "link", "codex"]);
});

test("usage link stores Claude credential outside config and unlink removes it", async () => {
  const dir = mkdtempSync(join(tmpdir(), "cave-usage-link-"));
  const proxy = stubProxy(dir);
  const home = join(dir, "home");
  const caveHome = join(dir, ".caveman");
  const env = { ...process.env, HOME: home, CAVEMAN_HOME: caveHome, CAVEMAN_PROXY_BIN: proxy, CAVE_NO_KEYCHAIN: "1" };

  const linked = await run(["usage", "link", "claude", "--session-key", "secret-session", "--org-id", "org-123"], env);
  assert.equal(linked.code, 0, linked.stderr);
  assert.match(linked.stdout, /"linked":\s*"claude"/);
  assert.doesNotMatch(linked.stdout, /secret-session/);
  assert.equal(readFileSync(join(caveHome, "usage", "claude-session-key"), "utf8"), "secret-session");

  const unlinked = await run(["usage", "unlink", "claude"], env);
  assert.equal(unlinked.code, 0, unlinked.stderr);
  assert.equal(existsSync(join(caveHome, "usage", "claude-session-key")), false);
});

test("usage link claude with env JSON refreshes proxy without storing credentials", async () => {
  const dir = mkdtempSync(join(tmpdir(), "cave-usage-link-json-"));
  const proxy = stubProxy(dir);
  const caveHome = join(dir, ".caveman");
  const env = {
    ...process.env,
    CAVEMAN_HOME: caveHome,
    CAVEMAN_PROXY_BIN: proxy,
    CAVE_NO_KEYCHAIN: "1",
    CAVEMAN_CLAUDE_USAGE_JSON: '{"five_hour":{"used_percentage":42}}',
  };

  const linked = await run(["usage", "link", "claude"], env);
  assert.equal(linked.code, 0, linked.stderr);
  const body = JSON.parse(linked.stdout);
  assert.equal(body.linked, "claude");
  assert.deepEqual(body.refresh.argv, ["usage", "link", "claude"]);
  assert.equal(existsSync(join(caveHome, "usage", "claude-session-key")), false);
});

test("usage link rejects partial Claude credentials", async () => {
  const dir = mkdtempSync(join(tmpdir(), "cave-usage-link-partial-"));
  const proxy = stubProxy(dir);
  const env = { ...process.env, CAVEMAN_PROXY_BIN: proxy, CAVE_NO_KEYCHAIN: "1", CAVEMAN_HOME: join(dir, ".caveman") };
  delete env.CAVEMAN_CLAUDE_USAGE_JSON;
  delete env.CAVEMAN_CLAUDE_ORG_ID;

  const linked = await run(["usage", "link", "claude", "--session-key", "secret-session"], env);
  assert.notEqual(linked.code, 0);
  assert.match(linked.stderr, /both --session-key and --org-id/);
  assert.equal(existsSync(join(env.CAVEMAN_HOME, "usage", "claude-session-key")), false);
});

test("usage link does not use env Claude credentials as partial flag fallbacks", async () => {
  const dir = mkdtempSync(join(tmpdir(), "cave-usage-link-env-partial-"));
  const proxy = stubProxy(dir);
  const env = {
    ...process.env,
    CAVEMAN_PROXY_BIN: proxy,
    CAVE_NO_KEYCHAIN: "1",
    CAVEMAN_HOME: join(dir, ".caveman"),
    CAVEMAN_CLAUDE_ORG_ID: "org-from-env",
  };
  delete env.CAVEMAN_CLAUDE_USAGE_JSON;

  const linked = await run(["usage", "link", "claude", "--session-key", "secret-session"], env);
  assert.equal(linked.code, 2);
  assert.match(linked.stderr, /both --session-key and --org-id/);
  assert.equal(existsSync(join(env.CAVEMAN_HOME, "usage", "claude-session-key")), false);
});

test("usage unlink validates provider before deleting Claude credential", async () => {
  const dir = mkdtempSync(join(tmpdir(), "cave-usage-unlink-invalid-"));
  const proxy = stubProxy(dir);
  const caveHome = join(dir, ".caveman");
  const env = { ...process.env, CAVEMAN_PROXY_BIN: proxy, CAVE_NO_KEYCHAIN: "1", CAVEMAN_HOME: caveHome };
  const usageDir = join(caveHome, "usage");
  mkdirSync(usageDir, { recursive: true });
  writeFileSync(join(usageDir, "claude-session-key"), "still-here");

  const unlinked = await run(["usage", "unlink", "wat"], env);
  assert.equal(unlinked.code, 2);
  assert.equal(readFileSync(join(usageDir, "claude-session-key"), "utf8"), "still-here");
});

test("usage link rejects partial Claude credentials even with usage JSON", async () => {
  const dir = mkdtempSync(join(tmpdir(), "cave-usage-link-partial-json-"));
  const proxy = stubProxy(dir);
  const env = {
    ...process.env,
    CAVEMAN_PROXY_BIN: proxy,
    CAVE_NO_KEYCHAIN: "1",
    CAVEMAN_HOME: join(dir, ".caveman"),
    CAVEMAN_CLAUDE_USAGE_JSON: "{}",
  };
  delete env.CAVEMAN_CLAUDE_ORG_ID;

  const linked = await run(["usage", "link", "claude", "--session-key", "secret-session"], env);
  assert.notEqual(linked.code, 0);
  assert.match(linked.stderr, /both --session-key and --org-id/);
  assert.equal(existsSync(join(env.CAVEMAN_HOME, "usage", "claude-session-key")), false);
});

test("usage link rejects Claude usage JSON mixed with explicit credentials", async () => {
  const dir = mkdtempSync(join(tmpdir(), "cave-usage-link-json-creds-"));
  const proxy = stubProxy(dir);
  const env = {
    ...process.env,
    CAVEMAN_PROXY_BIN: proxy,
    CAVE_NO_KEYCHAIN: "1",
    CAVEMAN_HOME: join(dir, ".caveman"),
    CAVEMAN_CLAUDE_USAGE_JSON: "{}",
  };

  const linked = await run(["usage", "link", "claude", "--session-key", "secret-session", "--org-id", "org-123"], env);
  assert.equal(linked.code, 2);
  assert.match(linked.stderr, /not both/);
  assert.equal(existsSync(join(env.CAVEMAN_HOME, "usage", "claude-session-key")), false);
});

test("learn front door scans then reports through the proxy", async () => {
  const dir = mkdtempSync(join(tmpdir(), "cave-learn-"));
  const logFile = join(dir, "proxy.log");
  const proxy = stubProxy(dir);
  const env = { ...process.env, CAVEMAN_PROXY_BIN: proxy, STUB_PROXY_LOG: logFile, NO_COLOR: "1" };

  const out = await run(["learn"], env);
  assert.equal(out.code, 0, `learn front door should succeed; stderr=${out.stderr}`);
  assert.match(out.stdout, /Setup Score 73/);

  const logs = readFileSync(logFile, "utf8").trim().split("\n").map((l) => JSON.parse(l));
  assert.ok(logs.some((l) => l.argv[0] === "learn" && l.argv[1] === "scan"), "front door runs learn scan");
  assert.ok(logs.some((l) => l.argv[0] === "learn" && l.argv[1] === "report"), "front door runs learn report");
});

test("learn report --json passes through to the proxy", async () => {
  const dir = mkdtempSync(join(tmpdir(), "cave-learn2-"));
  const proxy = stubProxy(dir);
  const env = { ...process.env, CAVEMAN_PROXY_BIN: proxy, NO_COLOR: "1" };

  const out = await run(["learn", "report", "--json"], env);
  assert.equal(out.code, 0, `learn report should succeed; stderr=${out.stderr}`);
  assert.match(out.stdout, /"basis":\s*"inferred"/);
});

// #1068: a trial points the child at its own labelled proxy through the
// environment, but native routing pins the base URL in the agent's OWN config
// file, and an agent prefers its config to its environment. The child then
// talks to the persistent listener, which carries no trial label; RecordPayload
// only stores payloads under a `trial:` label, so nothing is captured and every
// number in the report renders 0 while the trial still exits 0. Refusing up
// front is the difference between "we could not measure this" and a report that
// states a measurement of zero.
test("trial refuses to run against an agent whose native routing pins its base URL", async () => {
  const isolated = isolatedCliEnv();
  try {
    const bin = join(isolated.home, "bin");
    mkdirSync(bin, { recursive: true });
    const claude = join(bin, "claude");
    writeFileSync(claude, "#!/bin/sh\nexit 0\n", { mode: 0o755 });
    const logFile = join(isolated.home, "proxy.log");
    const proxy = stubProxy(isolated.home);
    Object.assign(isolated.env, {
      PATH: `${bin}:${isolated.env.PATH}`,
      CAVEMAN_PROXY_BIN: proxy,
      STUB_PROXY_LOG: logFile,
    });

    const settings = join(isolated.home, ".claude", "settings.json");
    const journal = join(isolated.home, "integrations", "claude.json");
    mkdirSync(join(isolated.home, "integrations"), { recursive: true });
    writeFileSync(journal, JSON.stringify({
      schema_version: 1,
      agent: "claude",
      pack_version: "test",
      installed_at: new Date().toISOString(),
      detected_agent_version: null,
      operations: [{
        file: settings,
        kind: "claude-settings",
        backup: `${settings}.bak`,
        before_exists: false,
        before_sha256: null,
        after_sha256: "x",
        owned: { route: "http://127.0.0.1:8787/w/claude" },
      }],
    }));

    const refused = await runCli(["trial", "--trial-id", "trial_native", "--", "claude"], { env: isolated.env });
    assert.equal(refused.code, 2, `expected refusal, got code=${refused.code} stderr=${refused.stderr}`);
    assert.match(refused.stderr, /native routing/i);
    assert.match(refused.stderr, /127\.0\.0\.1:8787/);
    assert.match(refused.stderr, /settings\.json/);
    // The refusal must land before `trial start`, or it leaves an open trial row
    // that nothing will ever finish.
    const logs = existsSync(logFile)
      ? readFileSync(logFile, "utf8").trim().split("\n").filter(Boolean).map((line) => JSON.parse(line))
      : [];
    assert.ok(!logs.some((l) => l.argv[0] === "trial" && l.argv[1] === "start"), "refusal opened a trial row anyway");
    assert.ok(!logs.some((l) => l.argv[0] === "serve"), "refusal still started a trial proxy");

    // An interrupted install is the same hazard reached by a crash rather than
    // by a successful enable: installNativeAgent writes the pending journal
    // first, then the host files, and publishes the committed journal LAST, so
    // dying in between leaves a fully applied pinned route with only the
    // pending journal on disk.
    rmSync(journal);
    const settingsBody = Buffer.from(JSON.stringify({ env: { ANTHROPIC_BASE_URL: "http://127.0.0.1:8787/w/claude" } }, null, 2) + "\n");
    mkdirSync(join(isolated.home, ".claude"), { recursive: true });
    writeFileSync(settings, settingsBody);
    const pendingJournal = (afterSha) => JSON.stringify({
      schema_version: 1,
      agent: "claude",
      pack_version: "test",
      installed_at: new Date().toISOString(),
      detected_agent_version: null,
      operations: [{
        file: settings,
        kind: "claude-settings",
        backup: `${settings}.bak`,
        before_exists: false,
        before_sha256: null,
        after_sha256: afterSha,
        owned: { route: "http://127.0.0.1:8787/w/claude" },
      }],
    });
    // Must match bytesHash()'s spelling, which is prefixed.
    const appliedSha = `sha256:${createHash("sha256").update(settingsBody).digest("hex")}`;
    writeFileSync(join(isolated.home, "integrations", ".pending-claude.json"), pendingJournal(appliedSha));

    const refusedPending = await runCli(["trial", "--trial-id", "trial_pending", "--", "claude"], { env: isolated.env });
    assert.equal(refusedPending.code, 2, `interrupted install bypassed the guard: ${refusedPending.stderr}`);
    assert.match(refusedPending.stderr, /interrupted native install/i);
    assert.match(refusedPending.stderr, /127\.0\.0\.1:8787/);

    // A pending journal whose write never landed is NOT a pin, and must not
    // refuse a legitimate trial: same journal, a hash that does not match what
    // is on disk.
    writeFileSync(join(isolated.home, "integrations", ".pending-claude.json"), pendingJournal("0".repeat(64)));
    const allowedUnapplied = await runCli(["trial", "--trial-id", "trial_unapplied", "--", "claude"], { env: isolated.env });
    assert.notEqual(allowedUnapplied.code, 2, `unapplied pending journal refused a trial: ${allowedUnapplied.stderr}`);
    rmSync(join(isolated.home, "integrations", ".pending-claude.json"));
    rmSync(settings);

    // Control: the journal is the only thing standing in the way. With native
    // routing absent the same invocation runs, which is what proves the guard
    // fired on the pin rather than on the stub agent.
    const allowed = await runCli(["trial", "--trial-id", "trial_native_off", "--", "claude"], { env: isolated.env });
    assert.notEqual(allowed.code, 2, `guard still refused without a pin: ${allowed.stderr}`);
    assert.doesNotMatch(allowed.stderr, /native routing/i);
    const after = readFileSync(logFile, "utf8").trim().split("\n").filter(Boolean).map((line) => JSON.parse(line));
    assert.ok(after.some((l) => l.argv[0] === "trial" && l.argv[1] === "start"), "control run never started a trial");
  } finally {
    isolated.cleanup();
  }
});
