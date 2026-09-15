import { test } from "node:test";
import assert from "node:assert";
import { spawn } from "node:child_process";
import { existsSync, mkdirSync, mkdtempSync, readFileSync, statSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const cli = join(here, "..", "dist", "index.js");

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

function writeEngineStub() {
  const dir = mkdtempSync(join(tmpdir(), "cave-pixel-engine-"));
  const bin = join(dir, "caveman-engine");
  writeFileSync(bin, `#!/usr/bin/env node
import { writeFileSync } from "node:fs";
const file = process.argv[5];
if (process.argv[2] !== "pixel" || process.argv[3] !== "render" || process.argv[4] !== "--dense" || !file) process.exit(2);
if (process.env.PIXEL_EXIT) process.exit(Number(process.env.PIXEL_EXIT));
writeFileSync(file + ".px1.png", Buffer.from("png"));
if (process.env.PIXEL_BAD_JSON) {
  console.log("not-json");
  process.exit(0);
}
const dropped = Number(process.env.PIXEL_DROPPED ?? "0");
const text = Number(process.env.PIXEL_TEXT ?? "5000");
const image = Number(process.env.PIXEL_IMAGE ?? "400");
console.log(JSON.stringify({ width: 10, height: 10, charsRendered: 10, droppedChars: dropped, estTokens: image }));
if (!process.env.PIXEL_NO_SUMMARY) {
  console.log(JSON.stringify({ summary: true, pages: 1, textEstTokens: text, imageEstTokens: image }));
}
`, { mode: 0o755 });
  return bin;
}

function makeRootWithSkill(name = "fat", body = "big body line\n".repeat(600)) {
  const root = mkdtempSync(join(tmpdir(), "cave-skills-root-"));
  const dir = join(root, name);
  mkdirSync(dir);
  const original = `---
name: ${name}
description: fake skill
---

${body}`;
  writeFileSync(join(dir, "SKILL.md"), original);
  return { root, dir, original };
}

function escapedRegExp(value) {
  return value.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
}

test("convert --dir rewrites profitable skill, then --revert restores bytes", async () => {
  const engine = writeEngineStub();
  const { root, dir, original } = makeRootWithSkill();
  const out = await runConvert(["--dir", root], { CAVEMAN_ENGINE_BIN: engine, PIXEL_TEXT: "5000", PIXEL_IMAGE: "400" });
  assert.equal(out.code, 0, out.output);
  const skill = readFileSync(join(dir, "SKILL.md"), "utf8");
  assert.match(skill, /^---\nname: fat\n[\s\S]*?\n---\n\n<!-- caveman-pixel v1 sha256:[a-f0-9]{64} -->/);
  assert.match(skill, /Read \(view\) these image files NOW/);
  assert.match(skill, new RegExp(escapedRegExp(join(dir, "SKILL.px1.png"))));
  assert.equal(readFileSync(join(dir, "SKILL.orig.md"), "utf8"), original);
  assert.ok(existsSync(join(dir, "SKILL.px1.png")), "converted page must move into skill dir");
  assert.match(out.output, /fat: 5000 → 520 est tokens/);

  const reverted = await runConvert(["--dir", root, "--revert"], { CAVEMAN_ENGINE_BIN: engine });
  assert.equal(reverted.code, 0, reverted.output);
  assert.equal(readFileSync(join(dir, "SKILL.md"), "utf8"), original);
  assert.ok(!existsSync(join(dir, "SKILL.orig.md")), "revert removes saved original");
  assert.ok(!existsSync(join(dir, "SKILL.px1.png")), "revert removes pixel pages");
});

test("convert --dry-run renders and reports but leaves skill dir byte-identical", async () => {
  const engine = writeEngineStub();
  const { root, dir, original } = makeRootWithSkill("dry");
  const out = await runConvert(["--dir", root, "--dry-run"], { CAVEMAN_ENGINE_BIN: engine });
  assert.equal(out.code, 0, out.output);
  assert.equal(readFileSync(join(dir, "SKILL.md"), "utf8"), original);
  assert.ok(!existsSync(join(dir, "SKILL.orig.md")));
  assert.ok(!existsSync(join(dir, "SKILL.px1.png")));
  assert.match(out.output, /dry: 5000 → 520 est tokens/);
});

test("convert keeps text when render summary is not smaller", async () => {
  const engine = writeEngineStub();
  const { root, dir, original } = makeRootWithSkill("plain");
  const out = await runConvert(["--dir", root], { CAVEMAN_ENGINE_BIN: engine, PIXEL_TEXT: "500", PIXEL_IMAGE: "480" });
  assert.equal(out.code, 0, out.output);
  assert.equal(readFileSync(join(dir, "SKILL.md"), "utf8"), original);
  assert.ok(!existsSync(join(dir, "SKILL.orig.md")));
  assert.match(out.output, /not smaller/);
});

test("convert skips untouched when render drops chars", async () => {
  const engine = writeEngineStub();
  const { root, dir, original } = makeRootWithSkill("dropped");
  const out = await runConvert(["--dir", root], { CAVEMAN_ENGINE_BIN: engine, PIXEL_DROPPED: "1" });
  assert.equal(out.code, 0, out.output);
  assert.equal(readFileSync(join(dir, "SKILL.md"), "utf8"), original);
  assert.ok(!existsSync(join(dir, "SKILL.orig.md")));
  assert.match(out.output, /dropped chars/);
});

test("convert is marker-idempotent on second run", async () => {
  const engine = writeEngineStub();
  const { root, dir } = makeRootWithSkill("again");
  const first = await runConvert(["--dir", root], { CAVEMAN_ENGINE_BIN: engine });
  assert.equal(first.code, 0, first.output);
  const stub = readFileSync(join(dir, "SKILL.md"), "utf8");
  const orig = readFileSync(join(dir, "SKILL.orig.md"), "utf8");
  const pxStat = statSync(join(dir, "SKILL.px1.png"));

  const second = await runConvert(["--dir", root], { CAVEMAN_ENGINE_BIN: engine, PIXEL_TEXT: "9999", PIXEL_IMAGE: "1" });
  assert.equal(second.code, 0, second.output);
  assert.match(second.output, /already converted/);
  assert.equal(readFileSync(join(dir, "SKILL.md"), "utf8"), stub);
  assert.equal(readFileSync(join(dir, "SKILL.orig.md"), "utf8"), orig);
  assert.equal(statSync(join(dir, "SKILL.px1.png")).size, pxStat.size);
});

test("convert with missing engine leaves skill untouched and points at setup", async () => {
  const { root, dir, original } = makeRootWithSkill("missing");
  const missing = join(tmpdir(), "caveman-engine-does-not-exist");
  const out = await runConvert(["--dir", root], { CAVEMAN_ENGINE_BIN: missing });
  assert.equal(out.code, 0, out.output);
  assert.equal(readFileSync(join(dir, "SKILL.md"), "utf8"), original);
  assert.ok(!existsSync(join(dir, "SKILL.orig.md")));
  assert.match(out.output, /caveman setup/);
});

test("convert skips skill without frontmatter untouched", async () => {
  const engine = writeEngineStub();
  const root = mkdtempSync(join(tmpdir(), "cave-skills-root-"));
  const dir = join(root, "bare");
  mkdirSync(dir);
  const original = "No frontmatter\n" + "body\n".repeat(200);
  writeFileSync(join(dir, "SKILL.md"), original);
  const out = await runConvert(["--dir", root], { CAVEMAN_ENGINE_BIN: engine });
  assert.equal(out.code, 0, out.output);
  assert.equal(readFileSync(join(dir, "SKILL.md"), "utf8"), original);
  assert.match(out.output, /no frontmatter/);
});
