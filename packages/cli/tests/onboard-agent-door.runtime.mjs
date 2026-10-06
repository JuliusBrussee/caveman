import { test } from "node:test";
import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const cli = join(dirname(fileURLToPath(import.meta.url)), "..", "dist", "index.js");
const skip = process.platform === "win32" ? "shell agent stubs" : false;

// Conforming caveman-mcp/caveman-proxy stubs: if the door called the native
// enable, it would succeed and write ~/.claude/settings.json. It must not —
// before setup, nothing machine-wide is written without Continue.
function doorEnv() {
  const home = mkdtempSync(join(tmpdir(), "cave-door-"));
  const bin = join(home, "bin");
  mkdirSync(bin);
  writeFileSync(join(bin, "claude"), "#!/bin/sh\nif [ \"$1\" = --version ]; then echo 'claude 2.1.0'; exit 0; fi\nprintf 'agent:%s\\n' \"$*\"\n", { mode: 0o755 });
  writeFileSync(join(bin, "caveman-mcp"), "#!/bin/sh\nif [ \"$1\" = version ]; then printf '%s\\n' '{\"version\":\"1.0.0\",\"capabilities\":[\"mcp_recovery\"]}'; fi\n", { mode: 0o755 });
  writeFileSync(join(bin, "caveman-proxy"), "#!/bin/sh\nif [ \"$1\" = version ]; then printf '%s\\n' '{\"version\":\"1.0.0\",\"capabilities\":[\"native_runtime_v1\",\"native_hook_bridge_v1\",\"typed_ccr\"]}'; fi\n", { mode: 0o755 });
  // Proxy off so the session-only wrap launches the stub without a listener.
  writeFileSync(join(home, "cloud.json"), JSON.stringify({ wrap: { proxy: false, shrink: false, mcp: false, browse: false } }));
  const env = {
    ...process.env,
    NO_COLOR: "1",
    CI: "1",
    HOME: home,
    CAVEMAN_HOME: home,
    CAVE_NO_KEYCHAIN: "1",
    PATH: `${bin}:${process.env.PATH}`,
    CAVEMAN_MCP_BIN: join(bin, "caveman-mcp"),
    CAVEMAN_PROXY_BIN: join(bin, "caveman-proxy"),
  };
  delete env.CAVE_GATEWAY_URL;
  delete env.CLAUDE_CONFIG_DIR;
  delete env.ANTHROPIC_BASE_URL;
  return { env, home };
}

function run(env, argv) {
  return new Promise((resolve, reject) => {
    const child = spawn(process.execPath, [cli, ...argv], { env, stdio: ["ignore", "pipe", "pipe"] });
    let stdout = "";
    let stderr = "";
    child.stdout.on("data", (d) => (stdout += d));
    child.stderr.on("data", (d) => (stderr += d));
    child.on("exit", (code) => resolve({ code, stdout, stderr }));
    child.on("error", reject);
  });
}

test("caveman claude before setup, with no terminal to confirm in, writes nothing machine-wide", { skip }, async () => {
  const { env, home } = doorEnv();
  try {
    const out = await run(env, ["claude", "-p", "hi"]);
    assert.equal(out.code, 0, out.stderr);
    assert.match(out.stdout, /agent:--plugin-dir \S*caveman-wrap-claude-\S* -p hi/, "the agent runs through the session-only wrap");
    assert.doesNotMatch(out.stderr, /native Caveman enabled|planned user-scoped writes/);
    assert.equal(existsSync(join(home, ".claude", "settings.json")), false, "no Claude settings written");
    assert.equal(existsSync(join(home, "integrations", "claude.json")), false, "no native install journaled");
    assert.equal(JSON.parse(readFileSync(join(home, "cloud.json"), "utf8")).modules, undefined, "setup did not run");
  } finally {
    rmSync(home, { recursive: true, force: true });
  }
});

test("caveman claude after setup launches through the native door as before", { skip }, async () => {
  const { env, home } = doorEnv();
  try {
    const setup = await run(env, ["setup", "--yes", "--only", "output"]);
    assert.equal(setup.code, 0, setup.stderr);
    const out = await run(env, ["claude", "-p", "hi"]);
    assert.equal(out.code, 0, out.stderr);
    assert.equal(out.stdout, "agent:-p hi\n", "direct launch, no temp wrap pack");
    assert.ok(existsSync(join(home, "integrations", "claude.json")));
  } finally {
    rmSync(home, { recursive: true, force: true });
  }
});
