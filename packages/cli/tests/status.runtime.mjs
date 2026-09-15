import assert from "node:assert/strict";
import test from "node:test";
import { copyFileSync, linkSync, mkdirSync, symlinkSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { renderStatus } from "../dist/index.js";
import { isolatedCliEnv, runCli } from "./_cli.mjs";
import { nativeStub, nodeStub, stubEnv } from "./harness/stub-bin.mjs";

function statusEnv(extra = {}) {
  const isolated = isolatedCliEnv();
  const { home } = isolated;
  const bin = join(home, "bin");
  // Do not probe installed hosts, accounts or config while checking status's
  // output contract. A real node executable keeps POSIX/native Windows stubs
  // launchable without adding the user's package-bin directory to PATH.
  const node = join(bin, process.platform === "win32" ? "node.exe" : "node");
  if (process.platform === "win32") {
    try { linkSync(process.execPath, node); } catch { copyFileSync(process.execPath, node); }
  } else {
    // Keep relative library lookup valid for dynamically linked Node builds.
    symlinkSync(process.execPath, node);
  }
  const noop = nativeStub(bin, "status-noop", `
if (ARGV[0] === "version") process.stdout.write(JSON.stringify({version:"test",capabilities:["run_state","mcp_recovery"]}));
`);
  isolated.env = stubEnv({
    ...(process.env.SystemRoot ? { SystemRoot: process.env.SystemRoot } : {}),
    ...(process.env.PATHEXT ? { PATHEXT: process.env.PATHEXT } : {}),
    PATH: "", HOME: home, USERPROFILE: home,
    APPDATA: join(home, "AppData", "Roaming"), LOCALAPPDATA: join(home, "AppData", "Local"),
    CAVEMAN_HOME: home, CAVE_NO_KEYCHAIN: "1", NO_COLOR: "1", CI: "1",
    CAVEMAN_TELEMETRY: "0", CAVEMAN_OFFLINE: "1", CAVE_GATEWAY_URL: "http://127.0.0.1:9",
    CAVEMAN_PROXY_BIN: noop, CAVEMAN_ENGINE_BIN: noop, CAVEMAN_MCP_BIN: noop,
    CAVEMAN_BROWSE_BIN: noop, CAVEMEM_BIN: noop,
    ...extra,
  }, bin);
  return isolated;
}

const sources = { think: "global", remember: "default", execute: "project" };
const telemetry = { state: "off", change: "caveman telemetry on|off" };

test("status observe block carries today-scoped basis, config, telemetry, and forward action", () => {
  const text = renderStatus({
    mode: "observe",
    mode_source: "running",
    owner: "wrap",
    off_states: [{
      id: "observe",
      line: "observe mode — compression off until you sign in (free · 1 seat · no card)",
      fix: "caveman login",
    }],
    today: {
      spans: 3,
      tokens_in: 41_200,
      would_save_tokens: 118_000,
      token_accounting: { provider_complete: 812, provider_partial: 24 },
      basis: "inferred",
      mem_blocks: 318,
    },
    mem_blocks: 318,
    seat: { signed_in: false },
    plan: null,
    config_sources: sources,
    telemetry,
    next: "caveman login   (free · 1 seat · no card)",
  });
  assert.match(text, /^caveman  ·  observe/m);
  assert.match(text, /41k tokens observed on the layer/);
  assert.match(text, /basis: inferred \(local counters · 812 provider_complete \/ 24 provider_partial\)/);
  assert.match(text, /~118k tokens\/day would-have-saved/);
  assert.match(text, /telemetry  off · anonymous usage ping/);
  assert.match(text, /change: caveman telemetry on\|off/);
  assert.match(text, /next:  caveman login/);
  assert.doesNotMatch(text, /\b(?:measured|verified)\b/i);
});

test("status compress block omits honest unknowns instead of zeroing them", () => {
  const text = renderStatus({
    mode: "compress",
    mode_source: "running",
    owner: "start",
    off_states: [],
    today: null,
    mem_blocks: null,
    seat: {
      signed_in: true,
      entitled: true,
      plan: "free",
      seats_used: 1,
      seats_limit: 1,
      expires_at: "2026-08-01T00:00:00Z",
    },
    plan: null,
    config_sources: { think: "global+env", remember: "default", execute: "default" },
    telemetry,
    next: "caveman learn",
  });
  assert.match(text, /compress on/);
  assert.match(text, /no off-states/);
  assert.match(text, /nothing has run on the layer yet/);
  assert.doesNotMatch(text, /\bmem\s+0\b/);
  assert.doesNotMatch(text, /\bplan\s/);
  assert.doesNotMatch(text, /\b(?:measured|verified)\b/i);
});

test("half-installed status lists every supplied reason and never invents local numbers", () => {
  const reasons = [
    "caveman-proxy not installed — agents still launch, traffic is NOT compressed or metered",
    "observe mode — compression off until you sign in (free · 1 seat · no card)",
    "MCP recovery missing — streaming turns and Claude Pro/Max sessions pass through uncompressed (non-streaming API-key traffic still compresses)",
    "cavemem not installed — memory and auto-recall are off",
  ];
  const text = renderStatus({
    mode: "observe",
    mode_source: "resolved",
    owner: "unknown",
    off_states: reasons.map((line, index) => ({
      id: ["binary-missing", "observe", "mcp-missing", "mem-missing"][index],
      line,
    })),
    today: null,
    mem_blocks: null,
    seat: { signed_in: false },
    plan: null,
    config_sources: { think: "default", remember: "default", execute: "default" },
    telemetry,
    next: "caveman setup --install",
  });
  for (const reason of reasons) assert.match(text, new RegExp(reason.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")));
  assert.doesNotMatch(text, /tokens observed/);
  assert.doesNotMatch(text, /\bmem\s/);
  assert.match(text, /next:  caveman setup --install/);
});

test("status --json pins stable top-level key set and nullable contract", async () => {
  const isolated = statusEnv();
  const proxy = nativeStub(join(isolated.home, "bin"), "status-proxy", `
const cmd = ARGV[0];
if (cmd === "version") {
  process.stdout.write(JSON.stringify({version:"test",schema:"caveman.proxy.run.v1",capabilities:["run_state","sessions_scanned","observe_token_accounting"]}));
} else if (cmd === "status") {
  process.stdout.write(JSON.stringify({owner:"unknown"}));
} else if (cmd === "stats") {
  process.stdout.write(JSON.stringify({spans:0,tokens_in:0,token_accounting:{},basis:"inferred"}));
} else process.exit(2);
`);
  mkdirSync(join(isolated.home, ".caveman-cloud"), { recursive: true });
  writeFileSync(join(isolated.home, ".caveman-cloud", "config.json"), "{}");
  isolated.env.CAVEMAN_PROXY_BIN = proxy;
  try {
    const out = await runCli(["status", "--json"], { env: isolated.env, cwd: isolated.home });
    assert.equal(out.code, 0, out.stderr);
    const parsed = JSON.parse(out.stdout);
    assert.deepEqual(Object.keys(parsed), [
      "mode",
      "mode_source",
      "owner",
      "off_states",
      "today",
      "mem_blocks",
      "seat",
      "plan",
      "config_sources",
      "telemetry",
      "next",
      "native_integrations",
    ]);
    assert.deepEqual(parsed.native_integrations.map((item) => item.agent), [
      "claude", "codex", "hermes", "gemini", "opencode", "pi", "aider", "generic",
    ]);
    assert.equal(parsed.plan, null);
    assert.notEqual(parsed.mem_blocks, 0, "unknown mem count must be null, never a fabricated zero");
  } finally {
    isolated.cleanup();
  }
});

test("status with missing proxy emits half-installed block and no local zero rows", async () => {
  const isolated = statusEnv({
    CAVEMAN_PROXY_BIN: join("/definitely", "missing", "caveman-proxy"),
    CAVEMEM_BIN: join("/definitely", "missing", "cavemem"),
  });
  try {
    const out = await runCli(["status"], { env: isolated.env, cwd: isolated.home });
    assert.equal(out.code, 0, out.stderr);
    assert.match(out.stdout, /caveman-proxy not installed/);
    assert.match(out.stdout, /cavemem not installed/);
    assert.match(out.stdout, /MCP recovery missing/);
    assert.match(out.stdout, /next:  caveman setup --install/);
    assert.doesNotMatch(out.stdout, /tokens observed/);
    assert.doesNotMatch(out.stdout, /\bmem\s+0\b/);
  } finally {
    isolated.cleanup();
  }
});

test("status makes permanent native activation discoverable without expanding porcelain", async () => {
  const isolated = statusEnv();
  const bin = join(isolated.home, "bin");
  nodeStub(bin, "claude", 'console.log("claude 2.1.226");');
  const proxy = nativeStub(bin, "native-proxy", `
if (ARGV[0] === "version") console.log(JSON.stringify({version:"test",capabilities:["run_state","native_runtime_v1","typed_ccr"]}));
else if (ARGV[0] === "status") console.log(JSON.stringify({owner:"unknown"}));
else if (ARGV[0] === "stats") console.log(JSON.stringify({spans:0,tokens_in:0,token_accounting:{},basis:"inferred"}));
`);
  isolated.env.CAVEMAN_PROXY_BIN = proxy;
  try {
    const out = await runCli(["status"], { env: isolated.env, cwd: isolated.home });
    assert.equal(out.code, 0, out.stderr);
    assert.match(out.stdout, /native integrations/);
    assert.match(out.stdout, /next native:  caveman enable claude/);
  } finally {
    isolated.cleanup();
  }
});

test("status gives cold native setup then enable path when runtime is missing", async () => {
  const isolated = statusEnv();
  const bin = join(isolated.home, "bin");
  nodeStub(bin, "claude", 'console.log("claude 2.1.226");');
  isolated.env.CAVEMAN_PROXY_BIN = join("/definitely", "missing", "caveman-proxy");
  try {
    const out = await runCli(["status"], { env: isolated.env, cwd: isolated.home });
    assert.equal(out.code, 0, out.stderr);
    assert.match(out.stdout, /next native:  caveman setup --install/);
    assert.match(out.stdout, /then:\s+caveman enable claude/);
  } finally {
    isolated.cleanup();
  }
});
