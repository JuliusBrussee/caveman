import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { test } from "node:test";
import { nativeStub, nodeStub, stubEnv } from "./harness/stub-bin.mjs";

const cli = fileURLToPath(new URL("../dist/index.js", import.meta.url));

function fixture(t, unset = []) {
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
  for (const name of unset) delete env[name];
  const run = (args) => spawnSync(process.execPath, [cli, ...args], { env, cwd: root, encoding: "utf8", timeout: 20_000 });
  return { run, config: join(codexHome, "config.toml") };
}

function assertRecoveryEnvironment(config, expected = ["CAVEMAN_HOME", "CAVEMAN_CCR_DB"]) {
  const table = config.match(/^\[mcp_servers\.caveman\]\s*\n([\s\S]*?)(?=^\[|$(?![\s\S]))/m)?.[1];
  assert.ok(table, "Caveman MCP registration must exist");
  const names = JSON.parse(table.match(/^env_vars\s*=\s*(\[[^\n]+\])/m)?.[1] ?? "null");
  assert.deepEqual(names, expected);
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

// `codex doctor` warns "env var CAVEMAN_CCR_DB is not set" for every name
// env_vars forwards that is unset, so only the ones set are named.
for (const door of ["wrap", "enable", "mcp"]) {
  test(`Codex ${door} names only the recovery variables that are set`, (t) => {
    const homeOnly = fixture(t, ["CAVEMAN_CCR_DB"]);
    const args = door === "mcp" ? ["mcp", "install", "codex"] : [door, "codex"];
    let out = homeOnly.run(args);
    assert.equal(out.status, 0, out.stderr);
    assertRecoveryEnvironment(door === "wrap" ? out.stdout : readFileSync(homeOnly.config, "utf8"), ["CAVEMAN_HOME"]);
    const neither = fixture(t, ["CAVEMAN_HOME", "CAVEMAN_CCR_DB"]);
    out = neither.run(args);
    assert.equal(out.status, 0, out.stderr);
    assertRecoveryEnvironment(door === "wrap" ? out.stdout : readFileSync(neither.config, "utf8"), null);
  });
}

// #1134: the block-boundary scan used the regex LITERAL /^[ \\t]*\[/m. Inside a
// literal `\\t` is an escaped backslash, so the class is [space, backslash, "t"] —
// it never matched a tab. A tab-indented table after our own (valid TOML: leading
// whitespace before a header is allowed) therefore looked like part of the caveman
// block, which has two consequences on the same shipped path:
//   * the block never compares equal, so an install that should be idempotent
//     takes the rewrite path every time, and
//   * that rewrite splices out blockStart..blockEnd, and blockEnd is now EOF, so
//     it DELETES the user's following tables from ~/.codex/config.toml.
test("a tab-indented table after the caveman block survives a re-install", (t) => {
  const fx = fixture(t);
  assert.equal(fx.run(["mcp", "install", "codex"]).status, 0);

  // A table the user (or Codex itself) owns, indented with a tab, placed after
  // ours — installMcpCodexToml always appends its block last.
  const foreign = '\t[projects."/home/u/app"]\n\ttrust_level = "trusted"\n';
  writeFileSync(fx.config, readFileSync(fx.config, "utf8") + foreign);

  const again = fx.run(["mcp", "install", "codex"]);
  assert.equal(again.status, 0, again.stderr);
  const after = readFileSync(fx.config, "utf8");
  assert.match(after, /\[projects\."\/home\/u\/app"\]/, "the user's table must not be deleted");
  assert.match(after, /trust_level = "trusted"/, "its keys must not be deleted either");
  assert.match(after, /\[mcp_servers\.caveman\]/, "and ours must still be registered");
});

// The same boundary bug seen from the read side: `caveman doctor` asks whether the
// registration still matches, and a tab-indented neighbour made it answer "no" for
// a registration that is present and correct.
test("a tab-indented neighbour does not make the registration look unregistered", (t) => {
  const fx = fixture(t);
  assert.equal(fx.run(["mcp", "install", "codex"]).status, 0);
  const before = readFileSync(fx.config, "utf8");
  writeFileSync(fx.config, before + '\t[history]\n\tpersistence = "none"\n');
  const out = fx.run(["mcp", "install", "codex"]);
  assert.equal(out.status, 0, out.stderr);
  // Idempotent: our block is recognized, so nothing about it is rewritten.
  assert.equal(
    readFileSync(fx.config, "utf8"),
    before + '\t[history]\n\tpersistence = "none"\n',
    "an already-correct registration next to a tab-indented table must be a no-op",
  );
});
