import test from "node:test";
import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const cli = join(here, "..", "dist", "index.js");

function run(args) {
  return spawnSync(process.execPath, [cli, ...args], { encoding: "utf8" });
}

test("tools practices render emits installable skill with honest seed evidence", () => {
  const out = run(["tools", "practices", "render", "stable-map-serialization"]);
  assert.equal(out.status, 0, out.stderr);
  assert.match(out.stdout, /^---\nname: caveman-practice-stable-map-serialization\n/);
  assert.match(out.stdout, /\n---\n\n# Serialize stable maps deterministically\n/);
  assert.match(out.stdout, /Evidence status: `unmeasured`/);
  assert.match(out.stdout, /unmeasured — verified nowhere yet/);
  assert.match(out.stdout, /Grader: `exact_match`/);
  assert.match(out.stdout, /Experiment: `local_ab`/);
  assert.match(out.stdout, /Never claim/);
  assert.match(out.stdout, /Never normalize fields whose order is semantically meaningful\./);
});

test("tools practices render rejects engine-dependent practice", () => {
  const out = run(["tools", "practices", "render", "context-compression"]);
  assert.equal(out.status, 2);
  assert.match(out.stderr, /skill_render\.eligible=false/);
  assert.equal(out.stdout, "");
});

test("tools practices render rejects unknown id", () => {
  const out = run(["tools", "practices", "render", "plausible-practice"]);
  assert.equal(out.status, 2);
  assert.match(out.stderr, /unknown practice "plausible-practice"/);
  assert.equal(out.stdout, "");
});

test("practice renderer has no top-level porcelain alias", () => {
  const out = run(["practices", "render", "stable-map-serialization"]);
  assert.equal(out.status, 2);
  assert.match(out.stderr, /unknown command "practices"/);
  assert.equal(out.stdout, "");
});
