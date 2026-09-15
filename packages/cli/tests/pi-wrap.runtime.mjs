import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { cpSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";

import { nodeStub, stubEnv } from "./harness/index.mjs";

const packageRoot = join(dirname(fileURLToPath(import.meta.url)), "..");
const cli = join(packageRoot, "dist", "index.js");

// Pi is launched through portableInvocation, so a Node shim stands in for the
// real binary on every platform (harness/stub-bin.mjs).
const PI_STUB = `import { writeFileSync } from "node:fs";
const keys = ["ANTHROPIC_BASE_URL", "OPENAI_BASE_URL", "GEMINI_API_BASE_URL"];
writeFileSync(process.env.PI_CAPTURE, JSON.stringify({
  argv: ARGV,
  hook: process.env.CAVEMAN_PI_HOOK_CMD,
  gateway: process.env.CAVE_GATEWAY_URL,
  baseUrls: Object.fromEntries(keys.filter((key) => Object.hasOwn(process.env, key)).map((key) => [key, process.env[key]])),
}));
`;

function fixture(cliPath = cli) {
  const root = mkdtempSync(join(tmpdir(), "cave-pi-wrap-"));
  const home = join(root, "home");
  const bin = join(root, "bin");
  const capture = join(root, "capture.json");
  mkdirSync(join(home, ".caveman-cloud"), { recursive: true });
  mkdirSync(bin, { recursive: true });
  writeFileSync(join(home, ".caveman-cloud", "config.json"), JSON.stringify({
    wrap: { proxy: false, shrink: false, mcp: false, browse: false },
  }));
  nodeStub(bin, "pi", PI_STUB);
  // os.homedir() reads USERPROFILE on Windows, HOME everywhere else.
  const env = stubEnv({
    ...process.env,
    HOME: home,
    USERPROFILE: home,
    CAVEMAN_HOME: join(home, ".caveman"),
    CAVEMAN_PLAIN: "1",
    CAVEMAN_TELEMETRY: "0",
    CAVEMAN_MCP_BIN: join(root, "missing-caveman-mcp"),
    CAVE_GATEWAY_URL: "http://127.0.0.1:18841",
    CI: "1",
    NO_COLOR: "1",
    PI_CAPTURE: capture,
  }, bin);
  for (const key of ["ANTHROPIC_BASE_URL", "OPENAI_BASE_URL", "GEMINI_API_BASE_URL"]) delete env[key];
  return {
    capture,
    cliPath,
    env,
    root,
    cleanup() { rmSync(root, { recursive: true, force: true, maxRetries: 10, retryDelay: 50 }); },
  };
}

function run(fx, command = "wrap") {
  return new Promise((resolve, reject) => {
    const child = spawn(process.execPath, [fx.cliPath, command, "pi", "--", "--print", "hi"], { env: fx.env });
    let stdout = "";
    let stderr = "";
    child.stdout.on("data", (chunk) => (stdout += chunk));
    child.stderr.on("data", (chunk) => (stderr += chunk));
    child.on("error", reject);
    child.on("exit", (code) => resolve({ code, stdout, stderr }));
  });
}

test("wrap and run pi put extension first and stamp native hook environment", async () => {
  for (const command of ["wrap", "run"]) {
    const fx = fixture();
    try {
      const extension = join(fx.root, "Pi extension fixture.mjs");
      writeFileSync(extension, "export default function fixture() {}\n");
      fx.env.CAVEMAN_PI_EXTENSION = extension;
      const out = await run(fx, command);
      assert.equal(out.code, 0, out.stderr);
      const seen = JSON.parse(readFileSync(fx.capture, "utf8"));
      assert.deepEqual(seen.argv, ["--extension", extension, "--", "--print", "hi"]);
      const hook = JSON.parse(seen.hook);
      assert.ok(Array.isArray(hook));
      assert.ok(hook.length >= 1);
      assert.ok(hook.every((value) => typeof value === "string" && value.length > 0));
      assert.equal(seen.gateway, fx.env.CAVE_GATEWAY_URL);
      assert.deepEqual(seen.baseUrls, {}, "native-extension wraps must not receive generic base-URL routing");
    } finally {
      fx.cleanup();
    }
  }
});

test("wrap pi fails open when no bundled extension exists", async () => {
  const packageCopy = mkdtempSync(join(tmpdir(), "cave-pi-cli-copy-"));
  const copiedDist = join(packageCopy, "dist");
  cpSync(join(packageRoot, "dist"), copiedDist, { recursive: true });
  rmSync(join(copiedDist, "caveman-pi-extension.mjs"), { force: true });
  writeFileSync(join(packageCopy, "package.json"), '{"type":"module"}\n');
  const fx = fixture(join(copiedDist, "index.js"));
  delete fx.env.CAVEMAN_PI_EXTENSION;
  try {
    const out = await run(fx);
    assert.equal(out.code, 0, out.stderr);
    const seen = JSON.parse(readFileSync(fx.capture, "utf8"));
    assert.deepEqual(seen.argv, ["--", "--print", "hi"]);
    assert.match(out.stderr, /temporary native pack unavailable: Pi extension not found .* launching Pi directly/);
  } finally {
    fx.cleanup();
    rmSync(packageCopy, { recursive: true, force: true, maxRetries: 10, retryDelay: 50 });
  }
});

test("wrap pi preserves extension paths containing spaces byte-exact", async () => {
  const fx = fixture();
  try {
    const extension = join(fx.root, "dir with spaces", "caveman native extension.mjs");
    mkdirSync(dirname(extension), { recursive: true });
    writeFileSync(extension, "export default function fixture() {}\n");
    fx.env.CAVEMAN_PI_EXTENSION = extension;
    const out = await run(fx);
    assert.equal(out.code, 0, out.stderr);
    assert.equal(JSON.parse(readFileSync(fx.capture, "utf8")).argv[1], extension);
  } finally {
    fx.cleanup();
  }
});
