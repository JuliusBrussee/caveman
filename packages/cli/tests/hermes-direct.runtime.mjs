import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { test } from "node:test";
import { nativeStub, nodeStub, stubEnv } from "./harness/stub-bin.mjs";

const cli = fileURLToPath(new URL("../dist/index.js", import.meta.url));

for (const args of [
  ["-z", "hello"],
  ["--provider", "openai-codex", "-z", "hello"],
]) {
  test(`Hermes failed-proxy fallback preserves original provider arguments: ${args.join(" ")}`, (t) => {
    const root = mkdtempSync(join(tmpdir(), "cave-hermes direct "));
    t.after(() => rmSync(root, { recursive: true, force: true }));
    const home = join(root, "home"),
      bin = join(root, "bin");
    for (const path of [home, bin, join(home, ".caveman-cloud")])
      mkdirSync(path, { recursive: true });
    writeFileSync(
      join(home, ".caveman-cloud", "config.json"),
      JSON.stringify({
        think: { shrink: false },
        execute: {
          proxy: true,
          mcp: false,
          browse_tool: false,
          browse_cli: false,
        },
      }),
    );
    nodeStub(
      bin,
      "hermes",
      `
process.stdout.write(JSON.stringify({args:ARGV,base:process.env.CUSTOM_BASE_URL}));
`,
    );
    const proxy = nativeStub(
      bin,
      "caveman-proxy",
      `
if (ARGV[0] === "version") console.log(JSON.stringify({version:"test",capabilities:["run_state","typed_ccr"]}));
else process.exit(1);
`,
    );
    const env = stubEnv(
      {
        PATH: dirname(process.execPath),
        ...(process.env.SystemRoot
          ? { SystemRoot: process.env.SystemRoot }
          : {}),
        ...(process.env.PATHEXT ? { PATHEXT: process.env.PATHEXT } : {}),
        HOME: home,
        USERPROFILE: home,
        HERMES_HOME: join(home, ".hermes"),
        APPDATA: join(home, "AppData", "Roaming"),
        LOCALAPPDATA: join(home, "AppData", "Local"),
        CAVEMAN_HOME: join(home, ".caveman"),
        CAVEMAN_CONFIG: join(home, "missing.yaml"),
        CAVEMAN_PROXY_BIN: proxy,
        CAVE_GATEWAY_URL: "http://127.0.0.1:9",
        CAVEMAN_OFFLINE: "1",
        CAVEMAN_TELEMETRY: "0",
        CAVE_NO_KEYCHAIN: "1",
        NO_COLOR: "1",
        CUSTOM_BASE_URL: "https://synthetic-provider.example/account-a/v1",
      },
      bin,
    );
    const out = spawnSync(process.execPath, [cli, "wrap", "hermes", ...args], {
      env,
      cwd: root,
      encoding: "utf8",
      timeout: 20_000,
    });
    assert.equal(out.status, 0, out.stderr);
    assert.match(
      out.stderr,
      /launching directly without compression or metering/,
    );
    const actual = JSON.parse(out.stdout);
    assert.deepEqual(
      actual.args,
      args,
      "Caveman's --provider custom must not change a direct launch",
    );
    assert.equal(actual.base, env.CUSTOM_BASE_URL);
  });
}
