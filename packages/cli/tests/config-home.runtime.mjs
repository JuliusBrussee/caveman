import { test } from "node:test";
import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { chmodSync, mkdirSync, mkdtempSync, readdirSync, readFileSync, rmSync, statSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

// One config home: ~/.caveman (CAVEMAN_HOME). The first read copies the old
// ~/.caveman-cloud/config.json to ~/.caveman/cloud.json and leaves it there.
const cli = join(dirname(fileURLToPath(import.meta.url)), "..", "dist", "index.js");

function env() {
  const home = mkdtempSync(join(tmpdir(), "cave-config-home-"));
  const caveHome = join(home, ".caveman");
  const out = { ...process.env, HOME: home, CAVEMAN_HOME: caveHome, CAVE_NO_KEYCHAIN: "1", NO_COLOR: "1" };
  delete out.CAVE_TOKEN;
  return { env: out, home, caveHome };
}

test("the old ~/.caveman-cloud config is copied once to ~/.caveman/cloud.json and left in place", async () => {
  const box = env();
  const legacy = join(box.home, ".caveman-cloud", "config.json");
  mkdirSync(dirname(legacy), { recursive: true });
  writeFileSync(legacy, JSON.stringify({ baseURL: "http://127.0.0.1:9", projectId: "project-old" }), { mode: 0o644 });
  chmodSync(legacy, 0o644);
  try {
    const first = await new Promise((resolve) => {
      const child = spawn(process.execPath, [cli, "tools", "config", "set", "think.toon", "off"], { env: box.env });
      child.on("exit", resolve);
    });
    assert.equal(first, 0);
    const migrated = JSON.parse(readFileSync(join(box.caveHome, "cloud.json"), "utf8"));
    assert.equal(migrated.projectId, "project-old", "first read copies the old config");
    assert.equal(migrated.think.toon, false, "and writes go to the new file");
    assert.equal(statSync(join(box.caveHome, "cloud.json")).mode & 0o777, 0o600, "the copy is 0600 whatever the old mode");
    assert.deepEqual(JSON.parse(readFileSync(legacy, "utf8")), { baseURL: "http://127.0.0.1:9", projectId: "project-old" }, "old file left untouched");

    writeFileSync(legacy, JSON.stringify({ projectId: "project-changed-later" }));
    await new Promise((resolve) => spawn(process.execPath, [cli, "tools", "config", "get"], { env: box.env }).on("exit", resolve));
    assert.equal(JSON.parse(readFileSync(join(box.caveHome, "cloud.json"), "utf8")).projectId, "project-old", "copied once, never again");
  } finally {
    rmSync(box.home, { recursive: true, force: true });
  }
});

function cli_(argv, environment) {
  return new Promise((resolve, reject) => {
    const child = spawn(process.execPath, [cli, ...argv], { env: environment });
    let stdout = "";
    child.stdout.on("data", (d) => (stdout += d));
    child.on("exit", (code) => resolve({ code, stdout }));
    child.on("error", reject);
  });
}

test("racing first reads all see the whole copy and leave no temp files", async () => {
  const box = env();
  const legacy = join(box.home, ".caveman-cloud", "config.json");
  mkdirSync(dirname(legacy), { recursive: true });
  const body = JSON.stringify({ projectId: "project-race", think: { mode: "record" } });
  writeFileSync(legacy, body, { mode: 0o600 });
  try {
    const runs = await Promise.all(Array.from({ length: 6 }, () => cli_(["tools", "config", "get", "think.mode"], box.env)));
    for (const run of runs) {
      assert.equal(run.code, 0);
      assert.match(run.stdout, /^think\.mode = record /);
    }
    assert.equal(readFileSync(join(box.caveHome, "cloud.json"), "utf8"), body);
    assert.equal(statSync(join(box.caveHome, "cloud.json")).mode & 0o777, 0o600);
    assert.deepEqual(readdirSync(box.caveHome).filter((name) => name.endsWith(".tmp")), []);
  } finally {
    rmSync(box.home, { recursive: true, force: true });
  }
});

test("when the new home cannot be written the old config stays in use", async () => {
  const box = env();
  const legacy = join(box.home, ".caveman-cloud", "config.json");
  mkdirSync(dirname(legacy), { recursive: true });
  writeFileSync(legacy, JSON.stringify({ think: { mode: "record" } }), { mode: 0o600 });
  // A file where CAVEMAN_HOME's directory should be: nothing can be created under it.
  writeFileSync(join(box.home, "not-a-dir"), "");
  const blocked = { ...box.env, CAVEMAN_HOME: join(box.home, "not-a-dir", ".caveman") };
  try {
    const out = await cli_(["tools", "config", "get", "think.mode"], blocked);
    assert.equal(out.code, 0);
    assert.match(out.stdout, /^think\.mode = record /, "settings still come from the old file");
  } finally {
    rmSync(box.home, { recursive: true, force: true });
  }
});

test("a telemetry opt-out is mirrored into the old file for a rolled-back CLI", async () => {
  const box = env();
  const legacy = join(box.home, ".caveman-cloud", "config.json");
  mkdirSync(dirname(legacy), { recursive: true });
  writeFileSync(legacy, JSON.stringify({ projectId: "project-old", telemetry: { enabled: true, decidedAt: "2026-01-01T00:00:00.000Z", promptVersion: 5 } }), { mode: 0o600 });
  try {
    const out = await cli_(["telemetry", "off"], box.env);
    assert.equal(out.code, 0);
    const old = JSON.parse(readFileSync(legacy, "utf8"));
    assert.equal(old.telemetry.enabled, false, "the old file carries the opt-out");
    assert.equal(old.projectId, "project-old", "and nothing else changes there");
    assert.equal(statSync(legacy).mode & 0o777, 0o600);
    assert.equal(JSON.parse(readFileSync(join(box.caveHome, "cloud.json"), "utf8")).telemetry.enabled, false);
  } finally {
    rmSync(box.home, { recursive: true, force: true });
  }
});

