import { test } from "node:test";
import assert from "node:assert";
import { spawn } from "node:child_process";
import { mkdirSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const cli = join(dirname(fileURLToPath(import.meta.url)), "..", "dist", "index.js");

// Launch via the absolute node path so tests that wipe PATH can still start the
// CLI itself; only the CLI's *internal* PATH lookups are affected by env.PATH.
function run(argv, env) {
  return new Promise((resolve, reject) => {
    const child = spawn(process.execPath, [cli, ...argv], { env: env ?? process.env });
    let stdout = "";
    let stderr = "";
    child.stdout.on("data", (d) => (stdout += d));
    child.stderr.on("data", (d) => (stderr += d));
    child.on("exit", (code) => resolve({ code, stdout, stderr }));
    child.on("error", reject);
  });
}

// `caveman start` with a proxy binary that doesn't exist must render the cohesive
// "proxy not found" panel (signed installer / env paths) and exit non-zero — not a
// bare spawn stack trace. CAVEMAN_LISTEN points at a closed port so the "already
// running" branch is skipped deterministically.
test("start renders a help panel when the proxy binary is missing", async () => {
  const env = {
    ...process.env,
    NO_COLOR: "1",
    CAVEMAN_PROXY_BIN: "/nonexistent/caveman-proxy",
    CAVEMAN_LISTEN: "127.0.0.1:59071",
  };
  const out = await run(["start"], env);
  assert.notEqual(out.code, 0, "missing proxy must exit non-zero");
  assert.match(out.stderr, /proxy not found/i);
  assert.match(out.stderr, /caveman setup --install/, "must offer the signed installer");
  assert.match(out.stderr, /CAVEMAN_PROXY_BIN/, "must show the explicit binary override");
  assert.doesNotMatch(out.stderr, /go build|make dev|git clone/, "npm users must not receive contributor setup");
});

// `caveman wrap <unknown>` (a command not on PATH and not a known agent) must
// show the not-found panel listing the agents you can wrap, and exit non-zero.
test("wrap shows selectable agents when the command isn't found", async () => {
  const env = { ...process.env, NO_COLOR: "1" };
  const out = await run(["wrap", "definitely-not-a-real-cmd-9134"], env);
  assert.notEqual(out.code, 0, "unknown command must exit non-zero");
  assert.match(out.stderr, /not found/i);
  assert.match(out.stderr, /claude/, "must list claude as a wrap target");
  assert.match(out.stderr, /codex/, "must list codex as a wrap target");
});

// A known agent that isn't installed must surface its install hint. Forcing an
// empty PATH makes even an installed agent look absent — deterministic anywhere.
test("wrap a known agent that isn't installed shows the install hint", async () => {
  const env = { ...process.env, NO_COLOR: "1", PATH: "" };
  const out = await run(["wrap", "claude"], env);
  assert.notEqual(out.code, 0, "an uninstalled agent must exit non-zero");
  assert.match(out.stderr, /Claude Code/, "must name the agent");
  assert.match(out.stderr, /claude-code/, "must show the npm install hint");
});

// Byte-safe contract preserved: a known agent that IS resolvable still execs with
// the gateway base URLs injected (here a stub `claude` on a temp PATH echoes env).
test("wrap a resolvable agent injects the gateway base URLs", async () => {
  const { mkdtempSync, writeFileSync } = await import("node:fs");
  const { tmpdir } = await import("node:os");
  const binDir = mkdtempSync(join(tmpdir(), "cave-agent-"));
  const stub = join(binDir, "claude");
  // The stub prints the two base URLs it was launched with.
  writeFileSync(stub, '#!/bin/sh\nprintf \'{"a":"%s","o":"%s"}\' "$ANTHROPIC_BASE_URL" "$OPENAI_BASE_URL"\n', { mode: 0o755 });
  // Isolate HOME/CAVEMAN_HOME and prove wrap through child-visible environment.
  const home = mkdtempSync(join(tmpdir(), "cave-agent-home-"));
  mkdirSync(join(home, ".caveman-cloud"), { recursive: true });
  writeFileSync(join(home, ".caveman-cloud", "config.json"), JSON.stringify({ wrap: { proxy: false, shrink: false, mcp: false, browse: false } }, null, 2));
  const env = { ...process.env, NO_COLOR: "1", HOME: home, CAVEMAN_HOME: home, PATH: `${binDir}:${process.env.PATH}` };

  const out = await run(["wrap", "claude"], env);
  assert.equal(out.code, 0, out.stderr);
  const seen = JSON.parse(out.stdout);
  assert.equal(seen.a, "http://127.0.0.1:8787/w/claude", "ANTHROPIC_BASE_URL must point at the attributed gateway");
  assert.equal(seen.o, "http://127.0.0.1:8787/w/claude", "OPENAI_BASE_URL must point at the attributed gateway");
});
