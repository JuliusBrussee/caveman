import test from "node:test";
import assert from "node:assert";
import { spawnSync } from "node:child_process";
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const root = join(dirname(fileURLToPath(import.meta.url)), "..");
const cli = join(root, "dist", "index.js");
const release = readFileSync(join(root, "BINARY_RELEASE"), "utf8").trim();

test("version JSON names the independently pinned binary release", () => {
  const out = spawnSync(process.execPath, [cli, "version", "--json"], {
    encoding: "utf8",
    env: { ...process.env, NO_COLOR: "1" },
  });
  assert.equal(out.status, 0, out.stderr);
  assert.equal(JSON.parse(out.stdout).binary_release, release);
});

test("generated binary constants match their reviewable source files", () => {
  const out = spawnSync(process.execPath, [join(root, "scripts", "gen-binaries.mjs"), "--check"], {
    encoding: "utf8",
  });
  assert.equal(out.status, 0, out.stderr);
});
