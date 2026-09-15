import test from "node:test";
import assert from "node:assert";
import { mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";

import { runCli } from "./harness/index.mjs";

const enabled = process.env.CAVEMAN_COLD_SMOKE === "1";

test("cold package renders honest empty learn state", { skip: !enabled }, async () => {
  const cli = process.env.CAVEMAN_TEST_CLI;
  const proxy = process.env.CAVEMAN_TEST_PROXY_BIN;
  assert.ok(cli, "CAVEMAN_TEST_CLI is required");
  assert.ok(proxy, "CAVEMAN_TEST_PROXY_BIN is required");
  const home = mkdtempSync(join(tmpdir(), "cave-cold-empty-home-"));
  const history = mkdtempSync(join(tmpdir(), "cave-cold-empty-history-"));
  try {
    const env = {
      ...process.env,
      HOME: home,
      CAVEMAN_PROXY_BIN: proxy,
      CAVEMAN_CLAUDE_ROOT: history,
      CAVEMAN_CODEX_ROOT: join(history, "no-codex"),
      CAVEMAN_TELEMETRY: "0",
      NO_COLOR: "1",
    };
    const out = await runCli(cli, ["learn", "--sources", "claude", "--since", "30d"], {
      env,
      cwd: home,
      timeoutMs: 30_000,
    });
    assert.equal(out.code, 0, out.stderr);
    assert.match(
      out.stdout,
      /no Claude Code or Codex sessions found in the last 30d — the plan needs a block repeated across ≥3 sessions/,
    );
  } finally {
    rmSync(home, { recursive: true, force: true });
    rmSync(history, { recursive: true, force: true });
  }
});
