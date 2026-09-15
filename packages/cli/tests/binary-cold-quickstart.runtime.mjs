import test from "node:test";
import assert from "node:assert";
import { createServer as createNetServer } from "node:net";
import { mkdtempSync, mkdirSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { delimiter, join } from "node:path";

import { makeClaudeRoot } from "./fixtures/make-claude-root.mjs";
import { runCli, stubAgent, stubControlApi, stubUpstream } from "./harness/index.mjs";

const enabled = process.env.CAVEMAN_COLD_SMOKE === "1";
const cli = process.env.CAVEMAN_TEST_CLI;
const proxyBin = process.env.CAVEMAN_TEST_PROXY_BIN;

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

test("cold package completes seeded six-beat quickstart", { skip: !enabled }, async () => {
  assert.ok(cli, "CAVEMAN_TEST_CLI is required");
  assert.ok(proxyBin, "CAVEMAN_TEST_PROXY_BIN is required");
  const upstream = await stubUpstream();
  const api = await stubControlApi();
  const home = mkdtempSync(join(tmpdir(), "cave-cold-home-"));
  const work = mkdtempSync(join(tmpdir(), "cave-cold-work-"));
  const agents = mkdtempSync(join(tmpdir(), "cave-cold-agent-"));
  const sessions = mkdtempSync(join(tmpdir(), "cave-cold-sessions-"));
  const caveHome = process.env.CAVEMAN_HOME;
  assert.ok(caveHome, "CAVEMAN_HOME is required");

  try {
    const proxyPort = await freePort();
    const gateway = `http://127.0.0.1:${proxyPort}`;
    const agent = stubAgent({ dir: agents });
    makeClaudeRoot(sessions, { timestamp: new Date().toISOString() });
    mkdirSync(caveHome, { recursive: true });
    const config = join(caveHome, "caveman.yaml");
    writeFileSync(
      config,
      [
        "mode: record",
        `listen: 127.0.0.1:${proxyPort}`,
        "providers:",
        "  anthropic:",
        `    base_url: ${upstream.baseURL}`,
        "",
      ].join("\n"),
    );
    const env = {
      ...process.env,
      HOME: home,
      CAVEMAN_HOME: caveHome,
      CAVEMAN_CONFIG: config,
      CAVEMAN_PROXY_BIN: proxyBin,
      CAVEMAN_CLAUDE_ROOT: sessions,
      CAVEMAN_CODEX_ROOT: join(sessions, "no-codex"),
      CAVE_GATEWAY_URL: gateway,
      CAVE_SSRF_ALLOWLIST: "127.0.0.1",
      CAVE_NO_KEYCHAIN: "1",
      CAVEMAN_TELEMETRY: "0",
      NO_COLOR: "1",
      PATH: `${agent.dir}${delimiter}${process.env.PATH}`,
    };

    const setup = await runCli(cli, ["setup"], { env, cwd: work });
    assert.equal(setup.code, 0, setup.stderr);
    assert.match(setup.stdout, /All required binaries found/);

    const firstRun = await runCli(cli, ["claude"], { env, cwd: work, timeoutMs: 30_000 });
    assert.equal(firstRun.code, 0, firstRun.stderr);
    assert.match(firstRun.stdout, /stub-agent: ok/);
    assert.match(
      firstRun.stderr,
      /no compressible context|compression would have cut|turn it on:/,
      "session must end with an honest gate-specific line",
    );

    const learn = await runCli(cli, ["learn", "--sources", "claude", "--since", "30d", "--json"], {
      env,
      cwd: work,
      timeoutMs: 30_000,
    });
    assert.equal(learn.code, 0, learn.stderr);
    assert.ok(JSON.parse(learn.stdout).sinks.length >= 1, learn.stdout);

    const honestDayZero = await runCli(cli, ["learn", "--sources", "claude", "--since", "30d"], {
      env,
      cwd: work,
      timeoutMs: 30_000,
    });
    assert.equal(honestDayZero.code, 0, honestDayZero.stderr);
    assert.match(honestDayZero.stdout, /practice: [a-z0-9-]+ · unmeasured — verified nowhere yet/);

    const login = await runCli(
      cli,
      ["login", "--base-url", api.baseURL, "--gateway-url", gateway],
      { env, cwd: work },
    );
    assert.equal(login.code, 0, login.stderr);

    const rerun = await runCli(cli, ["claude"], { env, cwd: work, timeoutMs: 30_000 });
    assert.equal(rerun.code, 0, rerun.stderr);
    assert.match(rerun.stdout, /stub-agent: ok/);
    assert.doesNotMatch(rerun.stderr, /observe mode — compression off/);

    const remembered = await runCli(
      cli,
      ["tools", "mem", "remember", "cold install memory round trip"],
      { env, cwd: work },
    );
    assert.equal(remembered.code, 0, remembered.stderr);
    const memoryID = JSON.parse(remembered.stdout).id;
    assert.match(memoryID, /^mem_/);

    const recalled = await runCli(
      cli,
      ["tools", "mem", "recall", "cold install memory round trip"],
      { env, cwd: work },
    );
    assert.equal(recalled.code, 0, recalled.stderr);
    assert.ok(
      JSON.parse(recalled.stdout).hits.some((hit) => hit.id === memoryID),
      recalled.stdout,
    );

    const status = await runCli(cli, ["status", "--json"], { env, cwd: work });
    assert.equal(status.code, 0, status.stderr);
    assert.equal(JSON.parse(status.stdout).mem_blocks, 1);
  } finally {
    await Promise.allSettled([upstream.close(), api.close()]);
    for (const dir of [home, work, agents, sessions]) rmSync(dir, { recursive: true, force: true });
  }
});
