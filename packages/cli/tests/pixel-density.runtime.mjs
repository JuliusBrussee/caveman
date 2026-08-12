import { test } from "node:test";
import assert from "node:assert";
import { spawn } from "node:child_process";
import { mkdtempSync, mkdirSync, readFileSync, writeFileSync, existsSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const cli = join(dirname(fileURLToPath(import.meta.url)), "..", "dist", "index.js");

// An engine stub that RECORDS the argv `caveman convert` shelled it with, so we can
// assert the --density pass-through, and still emits a profitable render report so
// the conversion completes. The rendered file is always the LAST argv entry.
function writeArgvEngineStub() {
  const dir = mkdtempSync(join(tmpdir(), "cave-density-engine-"));
  const bin = join(dir, "caveman-engine");
  const argvFile = join(dir, "argv.json");
  writeFileSync(bin, `#!/usr/bin/env node
import { writeFileSync } from "node:fs";
const argv = process.argv.slice(2);
writeFileSync(${JSON.stringify(argvFile)}, JSON.stringify(argv));
const file = argv[argv.length - 1];
if (argv[0] !== "pixel" || argv[1] !== "render") process.exit(2);
writeFileSync(file + ".px1.png", Buffer.from("png"));
console.log(JSON.stringify({ width: 10, height: 10, charsRendered: 10, droppedChars: 0, estTokens: 400, density: "balanced" }));
console.log(JSON.stringify({ summary: true, pages: 1, textEstTokens: 5000, imageEstTokens: 400, density: "balanced" }));
`, { mode: 0o755 });
  return { bin, argvFile };
}

function makeRootWithSkill(name = "fat", body = "big body line\n".repeat(600)) {
  const root = mkdtempSync(join(tmpdir(), "cave-density-root-"));
  const dir = join(root, name);
  mkdirSync(dir);
  writeFileSync(join(dir, "SKILL.md"), `---\nname: ${name}\ndescription: fake skill\n---\n\n${body}`);
  return { root, dir };
}

function runConvert(extraArgs, extraEnv = {}) {
  const env = { ...process.env, NO_COLOR: "1", ...extraEnv };
  return new Promise((resolve, reject) => {
    const child = spawn("node", [cli, "convert", ...extraArgs], { env });
    let stdout = "";
    let stderr = "";
    child.stdout.on("data", (d) => (stdout += d));
    child.stderr.on("data", (d) => (stderr += d));
    child.on("exit", (code) => resolve({ code, stdout, stderr, output: stdout + stderr }));
    child.on("error", reject);
    child.stdin.end();
  });
}

test("convert --density forwards the level to the engine after --dense", async () => {
  const { bin, argvFile } = writeArgvEngineStub();
  const { root } = makeRootWithSkill("dense-max");
  const out = await runConvert(["--dir", root, "--density", "max"], { CAVEMAN_ENGINE_BIN: bin });
  assert.equal(out.code, 0, out.output);
  const argv = JSON.parse(readFileSync(argvFile, "utf8"));
  assert.deepEqual(argv.slice(0, 3), ["pixel", "render", "--dense"]);
  const d = argv.indexOf("--density");
  assert.ok(d > 0, `--density not forwarded: ${argv.join(" ")}`);
  assert.equal(argv[d + 1], "max");
  // The rendered file must stay the last arg so the engine's positional parse holds.
  assert.match(argv[argv.length - 1], /body\.md$/);
});

test("convert without --density forwards nothing extra (engine default applies)", async () => {
  const { bin, argvFile } = writeArgvEngineStub();
  const { root } = makeRootWithSkill("dense-default");
  const out = await runConvert(["--dir", root], { CAVEMAN_ENGINE_BIN: bin });
  assert.equal(out.code, 0, out.output);
  const argv = JSON.parse(readFileSync(argvFile, "utf8"));
  assert.ok(!argv.includes("--density"), `unexpected --density: ${argv.join(" ")}`);
});

test("convert --density rejects an invalid level loudly", async () => {
  const { bin } = writeArgvEngineStub();
  const { root } = makeRootWithSkill("bad");
  const out = await runConvert(["--dir", root, "--density", "turbo"], { CAVEMAN_ENGINE_BIN: bin });
  assert.equal(out.code, 2, out.output);
  assert.match(out.output, /--density must be one of conservative\|balanced\|max/);
});

function runSkills(extraArgs, extraEnv = {}) {
  const env = { ...process.env, NO_COLOR: "1", ...extraEnv };
  return new Promise((resolve, reject) => {
    const child = spawn("node", [cli, "skills", "install", ...extraArgs], { env });
    let stdout = "";
    let stderr = "";
    child.stdout.on("data", (d) => (stdout += d));
    child.stderr.on("data", (d) => (stderr += d));
    child.on("exit", (code) => resolve({ code, stdout, stderr, output: stdout + stderr }));
    child.on("error", reject);
    child.stdin.end();
  });
}

test("skills install --density (no skill name) installs the default and forwards the level", async () => {
  const { bin, argvFile } = writeArgvEngineStub();
  const dir = mkdtempSync(join(tmpdir(), "cave-density-skill-"));
  // The bug: "balanced" (the --density value) must NOT be parsed as the skill name.
  const out = await runSkills(["--density", "balanced", "--dir", dir], { CAVEMAN_ENGINE_BIN: bin });
  assert.equal(out.code, 0, out.output);
  assert.doesNotMatch(out.output, /unknown skill/);
  assert.ok(existsSync(join(dir, "SKILL.md")), "default skill (caveman-learn) must be installed");
  const argv = JSON.parse(readFileSync(argvFile, "utf8"));
  const d = argv.indexOf("--density");
  assert.ok(d > 0 && argv[d + 1] === "balanced", `--density not forwarded through skills install: ${argv.join(" ")}`);
});

// wrap forwards CAVE_PIXEL_DENSITY to the spawned proxy exactly like CAVE_PIXEL_MODELS.
function validEntitlement() {
  return {
    wrapEntitlement: {
      entitled: true, plan: "free", telemetry_level: "metadata",
      seats_used: 1, seats_limit: 1, devices_used: 1, devices_limit: 3,
      evicted_device_hash: null, expires_at: new Date(Date.now() + 72 * 3600 * 1000).toISOString(),
    },
    wrapEntitlementFetchedAt: new Date().toISOString(),
  };
}

async function runWrapWithProxyStub(cliArgs, extraEnv = {}) {
  const dir = mkdtempSync(join(tmpdir(), "cave-density-wrap-"));
  const modeFile = join(dir, "proxy-env.json");
  const proxy = join(dir, "proxy.mjs");
  const agent = join(dir, "agent");
  writeFileSync(proxy, `#!/usr/bin/env node
import { writeFileSync } from "node:fs";
if (process.argv[2] === "stats") { process.stdout.write("{}"); process.exit(0); }
writeFileSync(${JSON.stringify(modeFile)}, JSON.stringify({
  mode: process.env.CAVEMAN_MODE || "",
  pixelModels: process.env.CAVE_PIXEL_MODELS || "",
  pixelDensity: process.env.CAVE_PIXEL_DENSITY || ""
}));
`, { mode: 0o755 });
  writeFileSync(agent, "#!/bin/sh\nexit 0\n", { mode: 0o755 });
  const home = mkdtempSync(join(tmpdir(), "cave-density-home-"));
  mkdirSync(join(home, ".caveman-cloud"), { recursive: true });
  writeFileSync(join(home, ".caveman-cloud", "config.json"), JSON.stringify(validEntitlement(), null, 2));
  const env = {
    ...process.env,
    NO_COLOR: "1", HOME: home, CAVEMAN_HOME: home,
    CAVEMAN_MCP_BIN: "/nonexistent/caveman-mcp",
    CAVEMAN_BROWSE_BIN: "/nonexistent/caveman-browse",
    CAVE_GATEWAY_URL: "http://127.0.0.1:18811",
    CAVEMAN_PROXY_BIN: proxy,
    PATH: `${dir}:${process.env.PATH}`,
    ...extraEnv,
  };
  const out = await new Promise((resolve, reject) => {
    const child = spawn("node", [cli, ...cliArgs], { env });
    let stderr = "";
    child.stderr.on("data", (d) => (stderr += d));
    child.on("exit", (code) => resolve({ code, stderr }));
    child.on("error", reject);
  });
  const proxyEnv = existsSync(modeFile) ? JSON.parse(readFileSync(modeFile, "utf8")) : {};
  return { ...out, proxyEnv };
}

test("wrap --pixel forwards CAVE_PIXEL_DENSITY to the proxy", async () => {
  const out = await runWrapWithProxyStub(["wrap", "--pixel", "agent"], {
    CAVE_PIXEL_MODELS: "claude-fable-5",
    CAVE_PIXEL_DENSITY: "max",
  });
  assert.equal(out.code, 0, out.stderr);
  assert.equal(out.proxyEnv.mode, "pixel");
  assert.equal(out.proxyEnv.pixelDensity, "max");
});
