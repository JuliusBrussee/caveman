import { test } from "node:test";
import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { chmodSync, mkdtempSync, readdirSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const packageRoot = resolve(fileURLToPath(new URL("..", import.meta.url)));
const delegate = join(packageRoot, "dist", "caveman-delegate-mcp.mjs");

test("delegate tolerates invalid timeout and removes session data", async () => {
  const root = mkdtempSync(join(tmpdir(), "cave-delegate-runtime-"));
  const worker = join(root, "fake-pi.mjs");
  writeFileSync(worker, `#!/usr/bin/env node
import { writeFileSync } from "node:fs";
import { join } from "node:path";
const at = process.argv.indexOf("--session-dir");
const sessionDir = process.argv[at + 1];
writeFileSync(join(sessionDir, "usage.jsonl"), JSON.stringify({ usage: { input: 2, output: 3, cacheRead: 0, cacheWrite: 0, cost: { total: 0 } } }) + "\\n");
setTimeout(() => { process.stdout.write("worker ok\\n"); }, 50);
`, { mode: 0o700 });
  chmodSync(worker, 0o700);

  const server = spawn(process.execPath, [delegate], {
    env: {
      ...process.env,
      TMPDIR: root,
      CAVE_DELEGATE_PI_BIN: worker,
      CAVE_DELEGATE_TIMEOUT_MS: "not-a-number",
    },
    stdio: ["pipe", "pipe", "pipe"],
  });
  let stderr = "";
  server.stderr.on("data", (chunk) => { stderr += chunk; });

  try {
    const response = new Promise((resolveResponse, reject) => {
      let buffer = "";
      server.stdout.on("data", (chunk) => {
        buffer += chunk;
        const newline = buffer.indexOf("\n");
        if (newline === -1) return;
        try { resolveResponse(JSON.parse(buffer.slice(0, newline))); }
        catch (error) { reject(error); }
      });
      server.once("error", reject);
      server.once("exit", (code) => reject(new Error(`delegate exited ${code}: ${stderr}`)));
    });
    server.stdin.write(JSON.stringify({
      jsonrpc: "2.0",
      id: 1,
      method: "tools/call",
      params: { name: "caveman_delegate", arguments: { task: "probe" } },
    }) + "\n");

    const message = await response;
    assert.equal(message.error, undefined, JSON.stringify(message.error));
    assert.match(message.result.content[0].text, /worker ok/);
    assert.match(message.result.content[0].text, /input 2/);
    assert.deepEqual(
      readdirSync(root).filter((name) => name.startsWith("cave-delegate-")),
      [],
      "delegate session directory survived worker completion",
    );
  } finally {
    server.kill("SIGTERM");
    rmSync(root, { recursive: true, force: true });
  }
});
