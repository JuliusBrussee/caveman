import { test } from "node:test";
import assert from "node:assert";
import { execFileSync, spawnSync } from "node:child_process";
import { chmodSync, copyFileSync, existsSync, mkdtempSync, readFileSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

import { makeClaudeRoot } from "./fixtures/make-claude-root.mjs";
import { runCli } from "./harness/index.mjs";

const cliDir = join(dirname(fileURLToPath(import.meta.url)), "..");
const cli = process.env.CAVEMAN_TEST_CLI ?? join(cliDir, "dist", "index.js");
const packageParent = join(cliDir, "..");
const publicRoot = existsSync(join(packageParent, "proxy")) ? packageParent : join(packageParent, "..");
const goToolchainAvailable = spawnSync("go", ["version"], { stdio: "ignore" }).status === 0;

function reportFrom(output) {
  return JSON.parse(output.trim());
}

test("seeded Claude root sits on the real three-session recurrence threshold", async (t) => {
  if (!process.env.CAVEMAN_TEST_PROXY_BIN && !goToolchainAvailable) return t.skip("go toolchain not found");
  const root = mkdtempSync(join(tmpdir(), "cave-claude-root-"));
  const canonicalRoot = mkdtempSync(join(tmpdir(), "cave-claude-canonical-"));
  const caveHome = mkdtempSync(join(tmpdir(), "cave-learn-home-"));
  const workDir = mkdtempSync(join(tmpdir(), "cave-learn-work-"));
  const binDir = mkdtempSync(join(tmpdir(), "cave-learn-bin-"));
  const proxyBin = join(binDir, "caveman-proxy");
  try {
    const canonical = makeClaudeRoot(canonicalRoot);
    const committedRoot = join(cliDir, "tests", "fixtures", "claude-root");
    for (const slug of canonical.projectSlugs) {
      assert.equal(
        readFileSync(join(committedRoot, "projects", slug, "session.jsonl"), "utf8"),
        readFileSync(join(canonicalRoot, "projects", slug, "session.jsonl"), "utf8"),
        `checked-in ${slug} fixture must match deterministic generator output`,
      );
    }

    const fixture = makeClaudeRoot(root, { timestamp: new Date().toISOString() });
    assert.equal(fixture.files.length, 3);
    assert.equal(new Set(fixture.projectSlugs).size, 3);
    assert.ok(fixture.estimatedBlockTokens >= 400);

    if (process.env.CAVEMAN_TEST_PROXY_BIN) {
      copyFileSync(process.env.CAVEMAN_TEST_PROXY_BIN, proxyBin);
      chmodSync(proxyBin, 0o755);
    } else {
      execFileSync("go", ["build", "-o", proxyBin, "./proxy/cmd/caveman-proxy"], {
        cwd: publicRoot,
        stdio: "pipe",
      });
    }

    const env = {
      ...process.env,
      HOME: workDir,
      CAVEMAN_HOME: caveHome,
      CAVEMAN_PROXY_BIN: proxyBin,
      CAVEMAN_CLAUDE_ROOT: root,
      CAVEMAN_CODEX_ROOT: join(root, "no-codex-history"),
      CAVEMAN_TELEMETRY: "0",
    };

    const atThreshold = await runCli(cli, ["learn", "--sources", "claude", "--since", "30d", "--json"], {
      env,
      cwd: workDir,
    });
    assert.equal(atThreshold.code, 0, atThreshold.stderr);
    assert.ok(reportFrom(atThreshold.stdout).sinks.length >= 1, atThreshold.stdout);

    rmSync(fixture.files[2]);
    const belowThreshold = await runCli(cli, ["learn", "--sources", "claude", "--since", "30d", "--json"], {
      env,
      cwd: workDir,
    });
    assert.equal(belowThreshold.code, 0, belowThreshold.stderr);
    assert.equal(reportFrom(belowThreshold.stdout).sinks.length, 0, belowThreshold.stdout);
  } finally {
    for (const dir of [root, canonicalRoot, caveHome, workDir, binDir]) {
      rmSync(dir, { recursive: true, force: true });
    }
  }
});
