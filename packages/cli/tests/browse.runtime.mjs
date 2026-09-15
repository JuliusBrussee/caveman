import { test } from "node:test";
import assert from "node:assert";
import { spawn } from "node:child_process";
import { mkdtempSync, readFileSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const cli = join(here, "..", "dist", "index.js");

async function runBrowse(args) {
  const dir = mkdtempSync(join(tmpdir(), "cave-browse-"));
  const argvFile = join(dir, "argv.json");
  const stub = join(dir, "caveman-browse");
  writeFileSync(stub, `#!/usr/bin/env node
import { writeFileSync } from "node:fs";
writeFileSync(${JSON.stringify(argvFile)}, JSON.stringify(process.argv.slice(2)));
process.stdout.write(JSON.stringify({ ok: true, basis: "inferred" }));
`, { mode: 0o755 });
  const out = await new Promise((resolve, reject) => {
    const child = spawn("node", [cli, "browse", ...args], { env: { ...process.env, CAVEMAN_BROWSE_BIN: stub } });
    let stdout = "";
    let stderr = "";
    child.stdout.on("data", (d) => (stdout += d));
    child.stderr.on("data", (d) => (stderr += d));
    child.on("exit", (code) => resolve({ code, stdout, stderr }));
    child.on("error", reject);
  });
  return { ...out, argv: JSON.parse(readFileSync(argvFile, "utf8")) };
}

test("browse <url> delegates to caveman-browse snapshot", async () => {
  const out = await runBrowse(["http://127.0.0.1:3000", "save", "settings"]);
  assert.equal(out.code, 0, out.stderr);
  assert.deepEqual(out.argv, ["snapshot", "http://127.0.0.1:3000", "save", "settings"]);
  assert.match(out.stdout, /"basis":"inferred"/);
});

test("browse close delegates persistent Chrome shutdown", async () => {
  const out = await runBrowse(["close"]);
  assert.equal(out.code, 0, out.stderr);
  assert.deepEqual(out.argv, ["close"]);
});

test("browse act delegates uid/action/text", async () => {
  const out = await runBrowse(["act", "ua", "type", "hello", "world"]);
  assert.equal(out.code, 0, out.stderr);
  assert.deepEqual(out.argv, ["act", "ua", "type", "hello world"]);
});

test("browse recover delegates handle and query", async () => {
  const out = await runBrowse(["recover", "ccr_abc", "save", "settings"]);
  assert.equal(out.code, 0, out.stderr);
  assert.deepEqual(out.argv, ["recover", "ccr_abc", "save", "settings"]);
});

test("browse with no args prints usage and exits non-zero", async () => {
  const out = await new Promise((resolve, reject) => {
    const child = spawn("node", [cli, "browse"], { env: process.env });
    let stderr = "";
    child.stderr.on("data", (d) => (stderr += d));
    child.on("exit", (code) => resolve({ code, stderr }));
    child.on("error", reject);
  });
  assert.notEqual(out.code, 0);
  assert.match(out.stderr, /usage: caveman browse/);
});

test("browse maps child signal exit to non-zero", async () => {
  const dir = mkdtempSync(join(tmpdir(), "cave-browse-signal-"));
  const stub = join(dir, "caveman-browse");
  writeFileSync(stub, "#!/bin/sh\nkill -TERM $$\n", { mode: 0o755 });
  const out = await new Promise((resolve, reject) => {
    const child = spawn("node", [cli, "browse", "http://local"], { env: { ...process.env, CAVEMAN_BROWSE_BIN: stub } });
    let stderr = "";
    child.stderr.on("data", (d) => (stderr += d));
    child.on("exit", (code) => resolve({ code, stderr }));
    child.on("error", reject);
  });
  assert.equal(out.code, 143, out.stderr);
});
