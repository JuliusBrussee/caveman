import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { existsSync, mkdirSync, mkdtempSync, readFileSync, realpathSync, rmSync, symlinkSync, writeFileSync } from "node:fs";
import { homedir, tmpdir } from "node:os";
import { delimiter, dirname, join, relative } from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";

import { codexHomeDir } from "../dist/index.js";

const cli = fileURLToPath(new URL("../dist/index.js", import.meta.url));

function fixture(t) {
  const root = mkdtempSync(join(tmpdir(), "cave-codex-home-contract-"));
  t.after(() => rmSync(root, { recursive: true, force: true }));
  const home = join(root, "home");
  const custom = join(root, "work account");
  const bin = join(root, "bin");
  for (const directory of [home, custom, bin, join(home, ".codex"), join(home, ".caveman-cloud")]) {
    mkdirSync(directory, { recursive: true });
  }
  writeFileSync(join(home, ".caveman-cloud", "config.json"), JSON.stringify({
    wrap: { proxy: false, shrink: false, mcp: false, browse: false },
  }));
  const source = `#!/usr/bin/env node
const fs = require("node:fs"), path = require("node:path");
if (process.argv[2] === "--version") { console.log("codex 0.153.0"); process.exit(0); }
const home = process.env.CODEX_HOME;
const read = name => { try { return fs.readFileSync(path.join(home, name), "utf8"); } catch { return null; } };
console.log(JSON.stringify({ home, config: read("config.toml"), auth: read("auth.json"), hooks: read("hooks.json") }));
`;
  writeFileSync(join(bin, "codex.cjs"), source, { mode: 0o755 });
  if (process.platform === "win32") {
    writeFileSync(join(bin, "codex.cmd"), '@node "%~dp0\\codex.cjs" %*\r\n');
  } else {
    writeFileSync(join(bin, "codex"), source, { mode: 0o755 });
  }
  // Isolated home and credentials; no inherited provider keys or account token.
  const env = {
    PATH: `${bin}${delimiter}${dirname(process.execPath)}`,
    ...(process.env.SystemRoot ? { SystemRoot: process.env.SystemRoot } : {}),
    ...(process.env.PATHEXT ? { PATHEXT: process.env.PATHEXT } : {}),
    HOME: home,
    USERPROFILE: home,
    CODEX_HOME: custom,
    CAVEMAN_HOME: join(home, ".caveman"),
    CAVE_GATEWAY_URL: "http://127.0.0.1:9",
    CAVEMAN_TELEMETRY: "0",
    CAVEMAN_OFFLINE: "1",
    NO_COLOR: "1",
  };
  const run = (args, override = {}) => spawnSync(process.execPath, [cli, ...args], {
    env: { ...env, ...override }, cwd: root, encoding: "utf8", timeout: 20_000,
  });
  return { root, home, custom, run };
}

test("Codex home honors nonempty absolute and relative overrides, including spaces", (t) => {
  const fx = fixture(t);
  assert.equal(codexHomeDir({ CODEX_HOME: fx.custom }), realpathSync(fx.custom));
  assert.equal(codexHomeDir({ CODEX_HOME: relative(process.cwd(), fx.custom) }), realpathSync(fx.custom));
  const spaces = join(fx.root, "   leading-space-account");
  mkdirSync(spaces);
  assert.equal(codexHomeDir({ CODEX_HOME: spaces }), realpathSync(spaces));
});

test("Codex home canonicalizes directory links", (t) => {
  const fx = fixture(t);
  const link = join(fx.root, "linked-account");
  symlinkSync(fx.custom, link, process.platform === "win32" ? "junction" : "dir");
  assert.equal(codexHomeDir({ CODEX_HOME: link }), realpathSync(fx.custom));
});

test("Codex home uses default for missing or empty override", () => {
  assert.equal(codexHomeDir({}), join(homedir(), ".codex"));
  assert.equal(codexHomeDir({ CODEX_HOME: "" }), join(homedir(), ".codex"));
});

test("Codex home rejects nonexistent paths and files instead of switching accounts", (t) => {
  const fx = fixture(t);
  assert.throws(() => codexHomeDir({ CODEX_HOME: join(fx.root, "missing") }), /CODEX_HOME.*does not exist/);
  const file = join(fx.root, "not-directory");
  writeFileSync(file, "");
  assert.throws(() => codexHomeDir({ CODEX_HOME: file }), /CODEX_HOME.*not a directory/);
});

test("wrap preserves custom Codex account auth, config, and hooks in its temporary home", (t) => {
  const fx = fixture(t);
  const config = 'model = "custom-account-model"\n';
  const auth = JSON.stringify({ auth_mode: "chatgpt", tokens: { access_token: "fake-local-test-token", account_id: "fake" } });
  const hooks = JSON.stringify({ hooks: { SessionStart: [{ hooks: [{ type: "command", command: "custom-account-hook" }] }] } });
  writeFileSync(join(fx.home, ".codex", "config.toml"), 'model = "other-account-model"\n');
  writeFileSync(join(fx.custom, "config.toml"), config);
  writeFileSync(join(fx.custom, "auth.json"), auth);
  writeFileSync(join(fx.custom, "hooks.json"), hooks);

  const result = fx.run(["wrap", "codex"], { CODEX_HOME: relative(fx.root, fx.custom) });
  assert.equal(result.status, 0, result.stderr);
  const child = JSON.parse(result.stdout);
  assert.equal(child.auth, auth);
  assert.match(child.config, /custom-account-model/);
  assert.doesNotMatch(child.config, /other-account-model/);
  assert.match(child.config, /\/chatgpt/);
  assert.match(child.hooks, /custom-account-hook/);
  assert.equal(existsSync(child.home), false, "temporary home is removed after child exit");
  assert.equal(readFileSync(join(fx.custom, "config.toml"), "utf8"), config);
  assert.equal(readFileSync(join(fx.custom, "auth.json"), "utf8"), auth);
  assert.equal(readFileSync(join(fx.custom, "hooks.json"), "utf8"), hooks);
});

test("wrap rejects invalid CODEX_HOME before launching a different account", (t) => {
  const fx = fixture(t);
  const result = fx.run(["wrap", "codex"], { CODEX_HOME: join(fx.root, "missing") });
  assert.notEqual(result.status, 0);
  assert.match(result.stderr, /CODEX_HOME.*does not exist/);
  assert.equal(result.stdout, "");
});

test("MCP install and uninstall use custom Codex config only", (t) => {
  const fx = fixture(t);
  const baseline = 'model = "custom-account-model"\n';
  const defaultConfig = join(fx.home, ".codex", "config.toml");
  const customConfig = join(fx.custom, "config.toml");
  writeFileSync(defaultConfig, 'model = "other-account-model"\n');
  writeFileSync(customConfig, baseline);
  const installed = fx.run(["mcp", "install", "codex"], { CAVEMAN_MCP_BIN: process.execPath });
  assert.equal(installed.status, 0, installed.stderr);
  assert.match(readFileSync(customConfig, "utf8"), /\[mcp_servers\.caveman\]/);
  const removed = fx.run(["mcp", "uninstall", "codex"]);
  assert.equal(removed.status, 0, removed.stderr);
  assert.equal(readFileSync(customConfig, "utf8"), baseline);
  assert.equal(readFileSync(defaultConfig, "utf8"), 'model = "other-account-model"\n');
});

test("skill install targets custom Codex home", (t) => {
  const fx = fixture(t);
  const result = fx.run(["skills", "install", "caveman-learn", "--agent", "codex", "--no-pixel"]);
  assert.equal(result.status, 0, result.stderr);
  assert.ok(existsSync(join(fx.custom, "skills", "caveman-learn", "SKILL.md")));
  assert.equal(existsSync(join(fx.home, ".codex", "skills")), false);
});
