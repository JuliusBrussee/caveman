import { test } from "node:test";
import assert from "node:assert";
import { existsSync, mkdirSync, mkdtempSync, readFileSync, statSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { isolatedCliEnv, runCli } from "./_cli.mjs";

function validEntitlement() {
  return {
    entitled: true,
    plan: "free",
    telemetry_level: "metadata",
    seats_used: 1,
    seats_limit: 1,
    devices_used: 1,
    devices_limit: 3,
    evicted_device_hash: null,
    expires_at: new Date(Date.now() + 24 * 3600 * 1000).toISOString(),
  };
}

function writeGlobal(home, value) {
  const dir = join(home, ".caveman-cloud");
  mkdirSync(dir, { recursive: true });
  writeFileSync(join(dir, "config.json"), JSON.stringify(value, null, 2), { mode: 0o600 });
  return join(dir, "config.json");
}

function startEnvHarness(config, { yaml, overlay, extraEnv = {} } = {}) {
  const isolated = isolatedCliEnv();
  const capture = join(isolated.home, "proxy-env.json");
  const proxy = join(isolated.home, "proxy.mjs");
  writeFileSync(proxy, `#!/usr/bin/env node
import { writeFileSync } from "node:fs";
writeFileSync(${JSON.stringify(capture)}, JSON.stringify({
  mode: process.env.CAVEMAN_MODE || "<unset>",
  entitled: process.env.CAVEMAN_WRAP_ENTITLED || "<unset>",
  recovery: process.env.CAVEMAN_RECOVERY || "<unset>"
}));
`, { mode: 0o755 });
  isolated.env.CAVEMAN_PROXY_BIN = proxy;
  // Stub exits without binding. Use fixed closed discard-adjacent port so a
  // parallel test's random listener cannot make start mistake it for live proxy.
  isolated.env.CAVEMAN_LISTEN = "127.0.0.1:1";
  Object.assign(isolated.env, extraEnv);
  const configPath = writeGlobal(isolated.home, config);
  if (yaml !== undefined) writeFileSync(join(isolated.home, "caveman.yaml"), yaml);
  const cwd = mkdtempSync(join(tmpdir(), "cave-config-cwd-"));
  if (overlay !== undefined) {
    mkdirSync(join(cwd, ".caveman"), { recursive: true });
    writeFileSync(join(cwd, ".caveman", "config.json"), JSON.stringify(overlay, null, 2));
  }
  return { isolated, capture, configPath, cwd };
}

async function runStart(harness) {
  const out = await runCli(["start"], { env: harness.isolated.env, cwd: harness.cwd });
  const proxyEnv = existsSync(harness.capture) ? JSON.parse(readFileSync(harness.capture, "utf8")) : {};
  return { ...out, proxyEnv };
}

test("start preserves legacy wrap.mode record and global think.mode wins above it", async () => {
  const legacy = startEnvHarness({ wrap: { mode: "record" } });
  const global = startEnvHarness({ wrap: { mode: "record" }, think: { mode: "compress" } });
  try {
    const legacyOut = await runStart(legacy);
    const globalOut = await runStart(global);
    assert.equal(legacyOut.code, 0, legacyOut.stderr);
    assert.equal(globalOut.code, 0, globalOut.stderr);
    assert.equal(legacyOut.proxyEnv.mode, "record");
    assert.equal(globalOut.proxyEnv.mode, "compress");
  } finally {
    legacy.isolated.cleanup();
    global.isolated.cleanup();
  }
});

test("start leaves mode unstamped for built-in default and proxy yaml", async () => {
  const fresh = startEnvHarness({});
  const yaml = startEnvHarness({}, { yaml: "mode: record\n" });
  try {
    const freshOut = await runStart(fresh);
    const yamlOut = await runStart(yaml);
    assert.equal(freshOut.proxyEnv.mode, "<unset>");
    assert.equal(yamlOut.proxyEnv.mode, "<unset>");
  } finally {
    fresh.isolated.cleanup();
    yaml.isolated.cleanup();
  }
});

test("project overlay cannot change mode or forge account state at start door", async () => {
  const harness = startEnvHarness({
    think: { mode: "compress" },
    wrapEntitlement: validEntitlement(),
  }, {
    overlay: {
      think: { mode: "record", pixel: { density: "max" } },
      wrapEntitlement: { entitled: false },
      token: "repo-token",
      telemetry: { enabled: true },
    },
  });
  try {
    const out = await runStart(harness);
    assert.equal(out.code, 0, out.stderr);
    assert.equal(out.proxyEnv.mode, "compress");
    // There is no account signal in the child env at all any more.
    assert.equal(out.proxyEnv.entitled, "<unset>");
    const persisted = JSON.parse(readFileSync(harness.configPath, "utf8"));
    assert.equal(persisted.wrapEntitlement.entitled, true);
    assert.ok(!("token" in persisted));
    assert.ok(!("telemetry" in persisted));
  } finally {
    harness.isolated.cleanup();
  }
});

test("config set invalid mode exits 2 and leaves file byte-identical", async () => {
  const isolated = isolatedCliEnv();
  const path = writeGlobal(isolated.home, { deviceId: "device-stable", think: { mode: "record" } });
  const before = readFileSync(path);
  try {
    const out = await runCli(["config", "set", "think.mode", "observe"], { env: isolated.env, prefix: "tools" });
    assert.equal(out.code, 2);
    assert.equal(out.stderr, 'not a mode: "observe" — valid: compress | record | pixel\n');
    assert.deepEqual(readFileSync(path), before);
  } finally {
    isolated.cleanup();
  }
});

test("config set writes grouped key, preserves deviceId, mode 0600, and reports source", async () => {
  const isolated = isolatedCliEnv();
  const path = writeGlobal(isolated.home, {
    deviceId: "device-stable",
    telemetry: { enabled: false, decidedAt: "2026-07-26T00:00:00.000Z", promptVersion: 1 },
  });
  try {
    const set = await runCli(["config", "set", "think.mode", "compress"], { env: isolated.env, prefix: "tools" });
    assert.equal(set.code, 0, set.stderr);
    assert.equal(set.stdout, "think.mode = compress  (global)\n");
    const get = await runCli(["config", "get", "think.mode"], { env: isolated.env, prefix: "tools" });
    assert.equal(get.code, 0, get.stderr);
    assert.equal(get.stdout, "think.mode = compress  (global)\n");
    const parsed = JSON.parse(readFileSync(path, "utf8"));
    assert.equal(parsed.deviceId, "device-stable");
    assert.equal(parsed.telemetry.enabled, false);
    assert.equal(parsed.think.mode, "compress");
    assert.equal(statSync(path).mode & 0o777, 0o600);
  } finally {
    isolated.cleanup();
  }
});

test("think.core is independently configurable without disabling compression", async () => {
  const isolated = isolatedCliEnv();
  const path = writeGlobal(isolated.home, { think: { mode: "compress" } });
  try {
    const set = await runCli(["config", "set", "think.core", "off"], { env: isolated.env, prefix: "tools" });
    assert.equal(set.code, 0, set.stderr);
    assert.equal(set.stdout, "think.core = false  (global)\n");
    const parsed = JSON.parse(readFileSync(path, "utf8"));
    assert.equal(parsed.think.mode, "compress");
    assert.equal(parsed.think.core, false);
    const get = await runCli(["config", "get", "think.core"], { env: isolated.env, prefix: "tools" });
    assert.equal(get.stdout, "think.core = false  (global)\n");
  } finally {
    isolated.cleanup();
  }
});

test("project allowlist contributes local capability sources only", async () => {
  const isolated = isolatedCliEnv({ CAVE_PIXEL_DENSITY: "max" });
  writeGlobal(isolated.home, { think: { mode: "record", core: true, toon: true } });
  const cwd = mkdtempSync(join(tmpdir(), "cave-overlay-"));
  mkdirSync(join(cwd, ".caveman"), { recursive: true });
  writeFileSync(join(cwd, ".caveman", "config.json"), JSON.stringify({
    think: { mode: "compress", core: false, toon: false, shrink: false, pixel: { density: "conservative" } },
    remember: { recall: true },
    execute: { browse_tool: false },
    deviceId: "repo-device",
  }));
  try {
    const get = await runCli(["config", "get"], { env: isolated.env, prefix: "tools", cwd });
    assert.equal(get.code, 0, get.stderr);
    assert.match(get.stdout, /^think\.mode = record  \(global\)$/m);
    assert.match(get.stdout, /^think\.core = true  \(global\)$/m);
    assert.match(get.stdout, /^think\.toon = false  \(project\)$/m);
    assert.match(get.stdout, /^think\.shrink = false  \(project\)$/m);
    assert.match(get.stdout, /^think\.pixel\.density = max  \(env\)$/m);
    assert.match(get.stdout, /^remember\.recall = true  \(project\)$/m);
    assert.match(get.stdout, /^execute\.browse_tool = false  \(project\)$/m);
  } finally {
    isolated.cleanup();
  }
});

test("invalid stored mode fails closed to record and is surfaced", async () => {
  const harness = startEnvHarness({ think: { mode: "observe" } });
  try {
    const out = await runStart(harness);
    assert.equal(out.code, 0, out.stderr);
    assert.equal(out.proxyEnv.mode, "record");
    assert.match(out.stderr, /think\.mode "observe" is not a valid mode — running record \(pass-through\)/);
  } finally {
    harness.isolated.cleanup();
  }
});

test("config surface rejects account and consent keys without writing", async () => {
  const isolated = isolatedCliEnv();
  const path = writeGlobal(isolated.home, { deviceId: "device-stable" });
  const before = readFileSync(path);
  try {
    for (const key of ["telemetry", "token", "wrapEntitlement", "deviceId"]) {
      const out = await runCli(["config", "set", key, "bad"], { env: isolated.env, prefix: "tools" });
      assert.equal(out.code, 2);
      assert.match(out.stderr, new RegExp(`config key not settable: ${key}`));
      assert.deepEqual(readFileSync(path), before);
    }
  } finally {
    isolated.cleanup();
  }
});
