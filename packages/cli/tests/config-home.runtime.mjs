import { test } from "node:test";
import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
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
  writeFileSync(legacy, JSON.stringify({ baseURL: "http://127.0.0.1:9", projectId: "project-old" }), { mode: 0o600 });
  try {
    const first = await new Promise((resolve) => {
      const child = spawn(process.execPath, [cli, "tools", "config", "set", "think.toon", "off"], { env: box.env });
      child.on("exit", resolve);
    });
    assert.equal(first, 0);
    const migrated = JSON.parse(readFileSync(join(box.caveHome, "cloud.json"), "utf8"));
    assert.equal(migrated.projectId, "project-old", "first read copies the old config");
    assert.equal(migrated.think.toon, false, "and writes go to the new file");
    assert.deepEqual(JSON.parse(readFileSync(legacy, "utf8")), { baseURL: "http://127.0.0.1:9", projectId: "project-old" }, "old file left untouched");

    writeFileSync(legacy, JSON.stringify({ projectId: "project-changed-later" }));
    await new Promise((resolve) => spawn(process.execPath, [cli, "tools", "config", "get"], { env: box.env }).on("exit", resolve));
    assert.equal(JSON.parse(readFileSync(join(box.caveHome, "cloud.json"), "utf8")).projectId, "project-old", "copied once, never again");
  } finally {
    rmSync(box.home, { recursive: true, force: true });
  }
});
