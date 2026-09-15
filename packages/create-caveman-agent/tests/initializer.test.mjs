import { test } from "node:test";
import assert from "node:assert/strict";
import { mkdir, mkdtemp, readFile, realpath, rm, stat, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { resolve } from "node:path";
import { spawn } from "node:child_process";

async function invoke(args, env = {}) {
  return new Promise((done) => {
    const child = spawn(process.execPath, ["dist/index.js", ...args], {
      cwd: resolve(import.meta.dirname, ".."),
      env: { PATH: process.env.PATH, ...env },
      stdio: ["ignore", "pipe", "pipe"],
    });
    const stdout = [];
    const stderr = [];
    child.stdout.on("data", (chunk) => stdout.push(chunk));
    child.stderr.on("data", (chunk) => stderr.push(chunk));
    child.once("close", (code) => done({
      code,
      stdout: Buffer.concat(stdout).toString("utf8"),
      stderr: Buffer.concat(stderr).toString("utf8"),
    }));
  });
}

test("package lifecycle builds executable initializer before packing", async () => {
  const packageRoot = resolve(import.meta.dirname, "..");
  const pkg = JSON.parse(await readFile(resolve(packageRoot, "package.json"), "utf8"));
  assert.equal(pkg.scripts.prepack, "npm run build");
  assert.deepEqual(pkg.repository, {
    type: "git",
    url: "git+https://github.com/JuliusBrussee/caveman.git",
    directory: "packages/create-caveman-agent",
  });
  assert.equal(pkg.homepage, "https://caveman.so/products/caveman-agent");
  assert.equal(pkg.bugs.url, "https://github.com/JuliusBrussee/caveman/issues");
  assert.equal(
    (await readFile(resolve(packageRoot, pkg.bin["create-caveman-agent"]), "utf8")).split("\n", 1)[0],
    "#!/usr/bin/env node",
  );
});

test("initializer exposes help without credentials or filesystem writes", async () => {
  for (const flag of ["--help", "-h"]) {
    const result = await invoke([flag]);
    assert.equal(result.code, 0);
    assert.match(result.stdout, /^usage: npm create @caveman-ai\/agent@latest/);
    assert.equal(result.stderr, "");
  }
});

test("initializer writes one required agent source and deterministic provider choice", async () => {
  const root = await mkdtemp(`${tmpdir()}/create-caveman-agent-`);
  const target = resolve(root, "support-agent");
  try {
    const result = await invoke([target, "--provider", "openai", "--no-install"], {
      OPENAI_API_KEY: "secret-never-print",
    });
    assert.equal(result.code, 0);
    assert.doesNotMatch(`${result.stdout}${result.stderr}`, /secret-never-print/);
    assert.match(await readFile(resolve(target, "src/agent.ts"), "utf8"), /agent\(/);
    assert.deepEqual((await stat(resolve(target, "src/agent.ts"))).isFile(), true);
    assert.equal(JSON.parse(await readFile(resolve(target, ".caveman/provider.json"), "utf8")).model, "openai/gpt-5.4-mini");
    const packageJSON = JSON.parse(await readFile(resolve(target, "package.json"), "utf8"));
    assert.equal(packageJSON.scripts.doctor, "caveman-agent doctor");
    assert.equal(packageJSON.scripts.dev, "caveman-agent dev src/agent.ts");
    assert.equal(packageJSON.scripts.run, "node --experimental-strip-types src/run.ts");
    assert.equal(packageJSON.scripts.typecheck, "tsc --noEmit");
    assert.equal(packageJSON.devDependencies.typescript, "5.9.3");
    const tsconfig = JSON.parse(await readFile(resolve(target, "tsconfig.json"), "utf8"));
    assert.equal(tsconfig.compilerOptions.strict, true);
    assert.deepEqual(tsconfig.include, ["src/**/*.ts", "evals/**/*.ts", "caveman.config.ts"]);
    const readme = await readFile(resolve(target, "README.md"), "utf8");
    assert.match(readme, /OPENAI_API_KEY/);
    assert.match(readme, /npm run doctor/);
    assert.match(readme, /approved: true/);
    assert.match(readme, /npm run build/);
    assert.match(readme, /npm run check/);
    assert.doesNotMatch(readme, /never spend past/i);
    assert.match(readme, /capBreached/);
    assert.match(readme, /overspent/);
    const runSource = await readFile(resolve(target, "src/run.ts"), "utf8");
    assert.doesNotMatch(runSource, /never spend past/i);
    assert.match(runSource, /capBreached\/overspent/);
    assert.match(runSource, /entryPath: "src\/agent\.ts"/);
    assert.match(runSource, /rootDir/);

    const mockPackage = resolve(target, "node_modules/@caveman-ai/agent");
    await mkdir(mockPackage, { recursive: true });
    await writeFile(resolve(mockPackage, "package.json"), JSON.stringify({
      name: "@caveman-ai/agent",
      type: "module",
      exports: "./index.js",
    }));
    await writeFile(resolve(mockPackage, "index.js"), [
      'import { writeFileSync } from "node:fs";',
      'export const auto = () => "model";',
      'export const schema = { object: (value) => value };',
      'export const tool = (value) => value;',
      'export const agent = (value) => value;',
      'export async function run(_definition, prompt, options) {',
      '  writeFileSync(process.env.CAVE_STARTER_CAPTURE, JSON.stringify({ prompt, options }));',
      '  return { stopReason: "completed", text: "ready", receipt: { totalEstimatedUsd: 0 } };',
      '}',
      '',
    ].join("\n"));
    const capture = resolve(root, "run-capture.json");
    const run = await new Promise((done) => {
      const child = spawn(process.execPath, ["--experimental-strip-types", "src/run.ts"], {
        cwd: target,
        env: { ...process.env, CAVE_STARTER_CAPTURE: capture },
        stdio: ["ignore", "pipe", "pipe"],
      });
      const stdout = [];
      const stderr = [];
      child.stdout.on("data", (chunk) => stdout.push(chunk));
      child.stderr.on("data", (chunk) => stderr.push(chunk));
      child.once("close", (code) => done({
        code,
        stdout: Buffer.concat(stdout).toString("utf8"),
        stderr: Buffer.concat(stderr).toString("utf8"),
      }));
    });
    assert.equal(run.code, 0, run.stderr);
    assert.match(run.stdout, /completed ready/);
    const invocation = JSON.parse(await readFile(capture, "utf8"));
    assert.equal(invocation.prompt, "Reply with exactly: ready");
    assert.equal(await realpath(invocation.options.rootDir), await realpath(target));
    assert.equal(invocation.options.entryPath, "src/agent.ts");
  } finally {
    await rm(root, { recursive: true, force: true });
  }
});

test("exactly one credential selects provider without question", async () => {
  const root = await mkdtemp(`${tmpdir()}/create-caveman-agent-`);
  const target = resolve(root, "auto-agent");
  try {
    const result = await invoke([target, "--no-install"], { ANTHROPIC_API_KEY: "not-printed" });
    assert.equal(result.code, 0);
    assert.equal(JSON.parse(await readFile(resolve(target, ".caveman/provider.json"), "utf8")).provider, "anthropic");
  } finally {
    await rm(root, { recursive: true, force: true });
  }
});

test("noninteractive ambiguous provider fails without partial directory", async () => {
  const root = await mkdtemp(`${tmpdir()}/create-caveman-agent-`);
  const target = resolve(root, "ambiguous-agent");
  try {
    const result = await invoke([target, "--no-install"], {
      ANTHROPIC_API_KEY: "one",
      OPENAI_API_KEY: "two",
    });
    assert.equal(result.code, 1);
    assert.match(result.stderr, /multiple provider credentials/);
    await assert.rejects(stat(target), { code: "ENOENT" });
  } finally {
    await rm(root, { recursive: true, force: true });
  }
});

test("initializer installs dependencies by default and leaves two-command first-run flow", async () => {
  const root = await mkdtemp(`${tmpdir()}/create-caveman-agent-`);
  const target = resolve(root, "installed-agent");
  const fakeNpm = resolve(root, "fake-npm.mjs");
  await writeFile(fakeNpm, [
    'import { writeFileSync } from "node:fs";',
    'writeFileSync("install-ran.json", JSON.stringify({ argv: process.argv.slice(2), env: process.env }));',
    "",
  ].join("\n"));
  try {
    const result = await invoke(["--provider", "google", target], {
      npm_execpath: fakeNpm,
      OPENAI_API_KEY: "secret-openai",
      CAVE_API_KEY: "secret-cave",
      AWS_SECRET_ACCESS_KEY: "secret-aws",
    });
    assert.equal(result.code, 0);
    const install = JSON.parse(await readFile(resolve(target, "install-ran.json"), "utf8"));
    assert.deepEqual(install.argv, ["install", "--no-audit", "--no-fund", "--ignore-scripts"]);
    for (const key of ["OPENAI_API_KEY", "CAVE_API_KEY", "AWS_SECRET_ACCESS_KEY"]) {
      assert.equal(key in install.env, false, `${key} reached dependency installer`);
    }
    assert.equal(typeof install.env.PATH, "string");
    assert.doesNotMatch(result.stdout, /^npm install$/m);
    assert.match(result.stdout, /^npm run dev$/m);
  } finally {
    await rm(root, { recursive: true, force: true });
  }
});

test("initializer rejects unknown options without writing target", async () => {
  const root = await mkdtemp(`${tmpdir()}/create-caveman-agent-`);
  const target = resolve(root, "unknown-option");
  try {
    const result = await invoke([target, "--wat"]);
    assert.equal(result.code, 1);
    assert.match(result.stderr, /unknown option --wat/);
    await assert.rejects(stat(target), { code: "ENOENT" });
  } finally {
    await rm(root, { recursive: true, force: true });
  }
});
