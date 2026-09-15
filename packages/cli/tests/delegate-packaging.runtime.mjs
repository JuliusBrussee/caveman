import { test } from "node:test";
import assert from "node:assert";
import { spawnSync } from "node:child_process";
import {
  existsSync,
  mkdirSync,
  mkdtempSync,
  readFileSync,
  writeFileSync,
} from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const packageRoot = join(dirname(fileURLToPath(import.meta.url)), "..");
const packageParent = join(packageRoot, "..");
const surfaceRoot = existsSync(join(packageParent, "agents")) ? packageParent : join(packageParent, "..");

test("packed CLI injects bundled delegate before Claude and Codex startup", () => {
  const workspace = mkdtempSync(join(tmpdir(), "cave-delegate-package-"));
  const packDir = join(workspace, "pack");
  const installDir = join(workspace, "install");
  const npmEnv = { ...process.env, npm_config_cache: join(workspace, "npm-cache") };
  mkdirSync(packDir, { recursive: true });

  const packed = spawnSync(
    "npm",
    ["pack", "--ignore-scripts", "--json", "--pack-destination", packDir],
    { cwd: packageRoot, env: npmEnv, encoding: "utf8", maxBuffer: 10 * 1024 * 1024 },
  );
  assert.equal(packed.status, 0, packed.stderr);
  const metadata = JSON.parse(packed.stdout)[0];
  assert.ok(
    metadata.files.some((file) => file.path === "dist/caveman-delegate-mcp.mjs"),
    "npm artifact must contain bundled delegate server",
  );

  const tarball = join(packDir, metadata.filename);
  const installed = spawnSync(
    "npm",
    ["install", "--ignore-scripts", "--no-audit", "--no-fund", "--prefix", installDir, tarball],
    { env: npmEnv, encoding: "utf8", maxBuffer: 10 * 1024 * 1024 },
  );
  assert.equal(installed.status, 0, installed.stderr);

  const installedPackage = join(installDir, "node_modules", "@caveman-ai", "cli");
  const cli = join(installedPackage, "dist", "index.js");
  const delegate = join(installedPackage, "dist", "caveman-delegate-mcp.mjs");
  assert.equal(existsSync(delegate), true);
  assert.deepEqual(
    readFileSync(delegate),
    readFileSync(join(surfaceRoot, "agents", "delegate", "caveman-delegate-mcp.mjs")),
  );

  for (const agentId of ["claude", "codex"]) {
    const home = mkdtempSync(join(tmpdir(), `cave-packed-${agentId}-home-`));
    const binDir = mkdtempSync(join(tmpdir(), `cave-packed-${agentId}-bin-`));
    const capture = join(home, "delegate-startup-config");
    const userConfig = join(home, agentId === "codex" ? ".codex/config.toml" : ".claude.json");
    const userConfigBefore = agentId === "codex" ? 'model = "gpt-5.5"\n' : '{"sentinel":"keep"}\n';
    mkdirSync(dirname(userConfig), { recursive: true });
    writeFileSync(userConfig, userConfigBefore);
    const stub = agentId === "codex"
      ? '#!/bin/sh\ncp "$CODEX_HOME/config.toml" "$CAVE_DELEGATE_CAPTURE"\n'
      : '#!/bin/sh\nif [ "$1" = "--plugin-dir" ] && [ -f "$2/.mcp.json" ]; then cp "$2/.mcp.json" "$CAVE_DELEGATE_CAPTURE"; fi\n';
    writeFileSync(join(binDir, agentId), `${stub}exit 0\n`, { mode: 0o755 });
    mkdirSync(join(home, ".caveman-cloud"), { recursive: true });
    writeFileSync(
      join(home, ".caveman-cloud", "config.json"),
      JSON.stringify({ execute: { delegate: true }, wrap: { proxy: false, shrink: false, mcp: false, browse: false } }),
    );
    const env = {
      ...process.env,
      NO_COLOR: "1",
      HOME: home,
      CAVEMAN_HOME: join(home, ".caveman"),
      CAVE_DELEGATE_CAPTURE: capture,
      CAVE_GATEWAY_URL: "http://127.0.0.1:9",
      PATH: `${binDir}:${process.env.PATH}`,
    };
    delete env.CAVEMAN_DELEGATE_MCP;
    const run = spawnSync(process.execPath, [cli, "wrap", agentId], { env, encoding: "utf8" });
    assert.equal(run.status, 0, run.stderr);
    assert.ok(readFileSync(capture, "utf8").includes(delegate));
    assert.equal(readFileSync(userConfig, "utf8"), userConfigBefore);
    assert.equal(existsSync(join(home, ".caveman", "mcp", `${agentId}.caveman-delegate.json`)), false);
  }
});
