import { test } from "node:test";
import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { existsSync, mkdirSync, mkdtempSync, readFileSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

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
