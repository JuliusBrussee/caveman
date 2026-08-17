import { test } from "node:test";
import assert from "node:assert";
import { spawn, execFileSync, spawnSync } from "node:child_process";
import { chmodSync, copyFileSync, existsSync, mkdirSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join, delimiter } from "node:path";
import { createServer as createNetServer, connect } from "node:net";
import { fileURLToPath } from "node:url";
import { DatabaseSync } from "node:sqlite";

import { runCli, stubAgent, stubUpstream } from "./harness/index.mjs";

const cliDir = join(dirname(fileURLToPath(import.meta.url)), "..");
const cli = process.env.CAVEMAN_TEST_CLI ?? join(cliDir, "dist", "index.js");
const packageParent = join(cliDir, "..");
const publicRoot = existsSync(join(packageParent, "proxy")) ? packageParent : join(packageParent, "..");
const goToolchainAvailable = spawnSync("go", ["version"], { stdio: "ignore" }).status === 0;

function freePort() {
  return new Promise((resolve, reject) => {
    const server = createNetServer();
    server.once("error", reject);
    server.listen(0, "127.0.0.1", () => {
      const port = server.address().port;
      server.close((error) => (error ? reject(error) : resolve(port)));
    });
  });
}

async function waitForPort(port, timeoutMs = 5_000) {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    const ready = await new Promise((resolve) => {
      const socket = connect({ host: "127.0.0.1", port });
      socket.once("connect", () => {
        socket.destroy();
        resolve(true);
      });
      socket.once("error", () => resolve(false));
    });
    if (ready) return;
    await new Promise((resolve) => setTimeout(resolve, 25));
  }
  throw new Error(`port ${port} did not become ready`);
}

function waitForExit(child, timeoutMs) {
  return new Promise((resolve) => {
    if (child.exitCode !== null || child.signalCode !== null) return resolve(true);
    const onExit = () => {
      clearTimeout(timer);
      resolve(true);
    };
    const timer = setTimeout(() => {
      child.off("exit", onExit);
      resolve(false);
    }, timeoutMs);
    child.once("exit", onExit);
  });
}

async function stopChild(child) {
  if (child.exitCode !== null || child.signalCode !== null) return;
  child.kill("SIGTERM");
  if (await waitForExit(child, 1_500)) return;
  child.kill("SIGKILL");
  await waitForExit(child, 1_500);
}

test("real proxy wraps stub agent through stub upstream and records SQLite telemetry", async (t) => {
  if (!process.env.CAVEMAN_TEST_PROXY_BIN && !goToolchainAvailable) return t.skip("go toolchain not found");
  const upstream = await stubUpstream();
  const home = mkdtempSync(join(tmpdir(), "cave-agent-home-"));
  const caveHome = mkdtempSync(join(tmpdir(), "cave-agent-store-"));
  const binDir = mkdtempSync(join(tmpdir(), "cave-agent-bin-"));
  const proxyBin = join(binDir, "caveman-proxy");
  let proxy = null;
  try {
    const proxyPort = await freePort();
    const agent = stubAgent({ dir: binDir });
    if (process.env.CAVEMAN_TEST_PROXY_BIN) {
      copyFileSync(process.env.CAVEMAN_TEST_PROXY_BIN, proxyBin);
      chmodSync(proxyBin, 0o755);
    } else {
      execFileSync("go", ["build", "-o", proxyBin, "./proxy/cmd/caveman-proxy"], {
        cwd: publicRoot,
        stdio: "pipe",
      });
    }
    mkdirSync(caveHome, { recursive: true });
    const configPath = join(caveHome, "caveman.yaml");
    writeFileSync(
      configPath,
      [
        "mode: record",
        `listen: 127.0.0.1:${proxyPort}`,
        "providers:",
        "  anthropic:",
        `    base_url: ${upstream.baseURL}`,
        "",
      ].join("\n"),
    );

    const proxyEnv = {
      ...process.env,
      CAVEMAN_HOME: caveHome,
      CAVEMAN_CONFIG: configPath,
      CAVE_SSRF_ALLOWLIST: "127.0.0.1",
      CAVEMAN_TELEMETRY: "0",
    };
    proxy = spawn(proxyBin, ["serve"], { env: proxyEnv, stdio: ["ignore", "pipe", "pipe"] });
    proxy.stdout.resume();
    proxy.stderr.resume();

    await waitForPort(proxyPort);
    const env = {
      ...proxyEnv,
      HOME: home,
      CAVE_GATEWAY_URL: `http://127.0.0.1:${proxyPort}`,
      CAVEMAN_PROXY_BIN: proxyBin,
      PATH: `${agent.dir}${delimiter}${process.env.PATH}`,
      CAVE_NO_KEYCHAIN: "1",
      NO_COLOR: "1",
    };
    const wrapped = await runCli(cli, ["wrap", agent.name], { env });
    assert.equal(wrapped.code, 0, wrapped.stderr);
    assert.match(wrapped.stdout, /stub-agent: ok/);
    assert.equal(upstream.requests.length, 1);
    assert.equal(upstream.requests[0].path, "/v1/messages");
    assert.equal(upstream.requests[0].body.model, "claude-sonnet-4-6");

    const db = new DatabaseSync(join(caveHome, "caveman.db"), { readOnly: true });
    try {
      const rows = db
        .prepare(
          "SELECT provider, model, status_code, input_tokens, output_tokens, token_usage_basis FROM requests ORDER BY id",
        )
        .all();
      assert.equal(rows.length, 1);
      const row = rows[0];
      assert.equal(row.provider, "anthropic");
      assert.equal(row.model, "claude-sonnet-4-6");
      assert.equal(Number(row.status_code), 200);
      assert.equal(Number(row.input_tokens), 12);
      assert.equal(Number(row.output_tokens), 2);
      assert.equal(row.token_usage_basis, "provider_complete");
    } finally {
      db.close();
    }
  } finally {
    if (proxy) await stopChild(proxy);
    try {
      await upstream.close();
    } finally {
      for (const dir of [home, caveHome, binDir]) {
        rmSync(dir, { recursive: true, force: true });
      }
    }
  }
});
