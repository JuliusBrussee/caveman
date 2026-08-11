import { test } from "node:test";
import assert from "node:assert";
import { spawn } from "node:child_process";
import { mkdtempSync, readFileSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const cli = join(here, "..", "dist", "index.js");

function runCompress(input, env) {
  return new Promise((resolve, reject) => {
    const child = spawn("node", [cli, "compress"], { env });
    let stdout = "";
    let stderr = "";
    child.stdout.on("data", (d) => (stdout += d));
    child.stderr.on("data", (d) => (stderr += d));
    child.on("exit", (code) => resolve({ code, stdout, stderr }));
    child.on("error", reject);
    child.stdin.write(input);
    child.stdin.end();
  });
}

// compress shells out to the caveman-engine binary: stdout is the engine's
// compressed payload, stderr is its JSON ratio report. Here a stub engine
// stands in for the real binary.
test("compress shells out to the engine binary and forwards its output", async () => {
  const input = '{"results":[1,2,3]}';
  const stubDir = mkdtempSync(join(tmpdir(), "cave-engine-"));
  const stub = join(stubDir, "caveman-engine");
  // The stub reads stdin, emits a compressed marker on stdout, and a report on stderr.
  writeFileSync(stub, `#!/bin/sh\ncat >/dev/null\nprintf '{"compressed":true}'\nprintf '{"ratio":0.8,"basis":"inferred"}' 1>&2\n`, { mode: 0o755 });

  const out = await runCompress(input, { ...process.env, CAVEMAN_ENGINE_BIN: stub });
  assert.equal(out.code, 0, out.stderr);
  assert.equal(out.stdout, '{"compressed":true}', "compress must forward the engine's stdout");
  const report = JSON.parse(out.stderr);
  assert.equal(report.ratio, 0.8);
  assert.equal(report.basis, "inferred");
});

// When the engine binary is unavailable, compress must degrade to a byte-safe
// pass-through that claims a 0 ratio — never breaking a pipe.
test("compress falls back to byte-safe pass-through when the engine is missing", async () => {
  const input = '{"hello":"world","emoji":"🦴"}';
  const out = await runCompress(input, { ...process.env, CAVEMAN_ENGINE_BIN: "caveman-engine-does-not-exist" });
  assert.equal(out.code, 0, out.stderr);
  assert.equal(out.stdout, input, "fallback must emit the input bytes unchanged");
  const report = JSON.parse(out.stderr);
  assert.equal(report.ratio, 0, "fallback must claim no savings");
  assert.equal(report.basis, "inferred");
  assert.equal(report.token_count_basis, "unavailable");
});

// evals run delegates to the engine harness and forwards its non-zero exit code
// so callers can gate on the quality result.
test("evals run forwards the engine harness exit code", async () => {
  const stubDir = mkdtempSync(join(tmpdir(), "cave-engine-evals-"));
  const stub = join(stubDir, "caveman-engine");
  const argvLog = join(stubDir, "argv.txt");
  writeFileSync(stub, `#!/bin/sh\nprintf '%s' "$*" > ${JSON.stringify(argvLog)}\nprintf '{"passed":false}'\nexit 1\n`, { mode: 0o755 });

  const out = await new Promise((resolve, reject) => {
    const child = spawn("node", [cli, "evals", "run", "--fixtures", "/tmp/release-fixtures"], { env: { ...process.env, CAVEMAN_ENGINE_BIN: stub } });
    let stdout = "";
    let stderr = "";
    child.stdout.on("data", (d) => (stdout += d));
    child.stderr.on("data", (d) => (stderr += d));
    child.on("exit", (code) => resolve({ code, stdout, stderr }));
    child.on("error", reject);
  });
  assert.equal(out.code, 1, "a failing gate must exit non-zero");
  assert.match(out.stdout, /"passed":\s*false/);
  assert.equal(readFileSync(argvLog, "utf8"), "evals run --fixtures /tmp/release-fixtures");

  const invalid = await new Promise((resolve, reject) => {
    const child = spawn("node", [cli, "evals", "run", "--fixture", "wrong"], { env: { ...process.env, CAVEMAN_ENGINE_BIN: stub } });
    let stderr = "";
    child.stderr.on("data", (d) => (stderr += d));
    child.on("exit", (code) => resolve({ code, stderr }));
    child.on("error", reject);
  });
  assert.equal(invalid.code, 2);
  assert.match(invalid.stderr, /evals run \[--fixtures <dir>\]/);
});

// stats delegates to the proxy binary (which owns the SQLite store) and prints
// whatever summary it returns.
test("stats delegates to the proxy binary and prints its summary", async () => {
  const summary = '{"requests":3,"total_cost_usd":0.12,"savings_usd":0,"basis":"inferred"}';
  const stubDir = mkdtempSync(join(tmpdir(), "cave-proxy-"));
  const stub = join(stubDir, "caveman-proxy");
  writeFileSync(stub, `#!/bin/sh\necho '${summary}'\n`, { mode: 0o755 });

  const out = await new Promise((resolve, reject) => {
    const child = spawn("node", [cli, "stats"], { env: { ...process.env, CAVEMAN_PROXY_BIN: stub } });
    let stdout = "";
    let stderr = "";
    child.stdout.on("data", (d) => (stdout += d));
    child.stderr.on("data", (d) => (stderr += d));
    child.on("exit", (code) => resolve({ code, stdout, stderr }));
    child.on("error", reject);
  });

  assert.equal(out.code, 0, out.stderr);
  assert.match(out.stdout, /"basis":\s*"inferred"/);
  assert.match(out.stdout, /"requests":\s*3/);
});

test("stats forwards --json and rejects unknown public flags", async () => {
  const stubDir = mkdtempSync(join(tmpdir(), "cave-proxy-json-"));
  const stub = join(stubDir, "caveman-proxy");
  const argvLog = join(stubDir, "argv.txt");
  writeFileSync(stub, `#!/bin/sh\nprintf '%s' "$*" > ${JSON.stringify(argvLog)}\nprintf '{"basis":"inferred"}\\n'\n`, { mode: 0o755 });

  const run = (args) => new Promise((resolve, reject) => {
    const child = spawn("node", [cli, "stats", ...args], { env: { ...process.env, CAVEMAN_PROXY_BIN: stub } });
    let stderr = "";
    child.stderr.on("data", (d) => (stderr += d));
    child.on("exit", (code) => resolve({ code, stderr }));
    child.on("error", reject);
  });

  const json = await run(["--json"]);
  assert.equal(json.code, 0, json.stderr);
  assert.equal(readFileSync(argvLog, "utf8"), "stats --json");
  const unknown = await run(["--recent", "5"]);
  assert.equal(unknown.code, 2);
  assert.match(unknown.stderr, /stats \[--json\]/);
});

// The package must expose both `caveman` and `cave` binaries.
test("package.json bin exposes both caveman and cave", () => {
  const pkg = JSON.parse(readFileSync(join(here, "..", "package.json"), "utf8"));
  assert.equal(pkg.bin.caveman, "dist/index.js");
  assert.equal(pkg.bin.cave, "dist/index.js");
});

// npm invokes prepack for both `npm pack` and `npm publish`. Keeping the
// shebang build there prevents packed binaries from failing with ENOEXEC.
test("package lifecycle builds an executable CLI before packing", () => {
  const pkg = JSON.parse(readFileSync(join(here, "..", "package.json"), "utf8"));
  assert.equal(pkg.scripts.prepack, "npm run build");
  assert.equal(readFileSync(cli, "utf8").split("\n", 1)[0], "#!/usr/bin/env node");
});
