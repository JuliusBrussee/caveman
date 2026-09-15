import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { test } from "node:test";
import { nativeStub, nodeStub, stubEnv } from "./harness/stub-bin.mjs";

const cli = fileURLToPath(new URL("../dist/index.js", import.meta.url));

function fixture(t) {
  const root = mkdtempSync(join(tmpdir(), "cave-codex recovery "));
  t.after(() => rmSync(root, { recursive: true, force: true }));
  const home = join(root, "home");
  const codexHome = join(root, "codex account");
  const caveHome = join(root, "recovery home");
  const bin = join(root, "bin");
  for (const path of [home, codexHome, caveHome, bin, join(home, ".caveman-cloud")]) mkdirSync(path, { recursive: true });
  writeFileSync(join(home, ".caveman-cloud", "config.json"), JSON.stringify({
    think: { shrink: false }, execute: { proxy: false, mcp: "auto", browse_tool: false, browse_cli: false },
  }));
  nodeStub(bin, "codex", `
import { readFileSync } from "node:fs";
import { join } from "node:path";
if (ARGV[0] === "--version") console.log("codex-cli 0.153.4");
else process.stdout.write(readFileSync(join(process.env.CODEX_HOME, "config.toml"), "utf8"));
`);
  const mcp = nativeStub(bin, "caveman-mcp", `
if (ARGV[0] === "version") console.log(JSON.stringify({version:"test",capabilities:["mcp_recovery"]}));
`);
  const proxy = nativeStub(bin, "caveman-proxy", `
if (ARGV[0] === "version") console.log(JSON.stringify({version:"test",capabilities:["run_state","native_runtime_v1","native_hook_bridge_v1","typed_ccr"]}));
else if (ARGV[0] === "status") console.log(JSON.stringify({owner:"unknown"}));
`);
  const env = stubEnv({
    PATH: dirname(process.execPath),
    ...(process.env.SystemRoot ? { SystemRoot: process.env.SystemRoot } : {}),
    ...(process.env.PATHEXT ? { PATHEXT: process.env.PATHEXT } : {}),
    HOME: home, USERPROFILE: home, CODEX_HOME: codexHome,
    APPDATA: join(home, "AppData", "Roaming"), LOCALAPPDATA: join(home, "AppData", "Local"),
    CAVEMAN_HOME: caveHome, CAVEMAN_CCR_DB: join(caveHome, "custom recovery.db"),
    CAVEMAN_CONFIG: join(caveHome, "missing.yaml"),
    CAVEMAN_MCP_BIN: mcp, CAVEMAN_PROXY_BIN: proxy,
    CAVE_GATEWAY_URL: "http://127.0.0.1:9", CAVE_NO_KEYCHAIN: "1",
    CAVEMAN_OFFLINE: "1", CAVEMAN_TELEMETRY: "0", NO_COLOR: "1",
    OPENAI_API_KEY: "sk-synthetic-do-not-forward-to-mcp",
  }, bin);
  const run = (args) => spawnSync(process.execPath, [cli, ...args], { env, cwd: root, encoding: "utf8", timeout: 20_000 });
  return { run, config: join(codexHome, "config.toml") };
}

function assertRecoveryEnvironment(config) {
  const table = config.match(/^\[mcp_servers\.caveman\]\s*\n([\s\S]*?)(?=^\[|$(?![\s\S]))/m)?.[1];
  assert.ok(table, "Caveman MCP registration must exist");
  const names = JSON.parse(table.match(/^env_vars\s*=\s*(\[[^\n]+\])/m)?.[1] ?? "null");
  assert.deepEqual(names, ["CAVEMAN_HOME", "CAVEMAN_CCR_DB"]);
  assert.doesNotMatch(table, /OPENAI_API_KEY|sk-synthetic|NODE_OPTIONS|approval_mode/);
}

for (const door of ["wrap", "enable", "mcp"]) {
  test(`Codex ${door} forwards only the shared recovery location through its MCP environment filter`, (t) => {
    const fx = fixture(t);
    const args = door === "mcp" ? ["mcp", "install", "codex"] : [door, "codex"];
    const out = fx.run(args);
    assert.equal(out.status, 0, out.stderr);
    assertRecoveryEnvironment(door === "wrap" ? out.stdout : readFileSync(fx.config, "utf8"));
    if (door === "mcp") {
      const before = readFileSync(fx.config, "utf8");
      assert.equal(fx.run(args).status, 0);
      assert.equal(readFileSync(fx.config, "utf8"), before, "repeat install must remain idempotent");
    }
  });
}
