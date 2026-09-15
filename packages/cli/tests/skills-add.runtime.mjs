import { after, test } from "node:test";
import assert from "node:assert";
import { spawn } from "node:child_process";
import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { homedir, tmpdir } from "node:os";
import { delimiter, dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { nativeStub, nodeStub, stubEnv } from "./harness/stub-bin.mjs";

const here = dirname(fileURLToPath(import.meta.url));
const cli = join(here, "..", "dist", "index.js");
const temporaryDirs = [];
function temporary(prefix) {
  const directory = mkdtempSync(join(tmpdir(), prefix));
  temporaryDirs.push(directory);
  return directory;
}
after(() => temporaryDirs.forEach(directory => rmSync(directory, { recursive: true, force: true })));

function runAdd(project, args, extraEnv = {}) {
  let env = { ...process.env, NO_COLOR: "1", ...extraEnv };
  // Do not let the runner's account/profile decide where global test skills go.
  for (const key of ["CODEX_HOME", "CLAUDE_CONFIG_DIR"]) if (!(key in extraEnv)) delete env[key];
  if (extraEnv.HOME) env.USERPROFILE = extraEnv.HOME;
  if (extraEnv.PATH !== undefined) {
    for (const key of Object.keys(env)) if (key !== "PATH" && key.toLowerCase() === "path") delete env[key];
  }
  if (env.CAVEMAN_ENGINE_BIN && existsSync(env.CAVEMAN_ENGINE_BIN)) env = stubEnv(env, dirname(env.CAVEMAN_ENGINE_BIN));
  return new Promise((resolve, reject) => {
    const child = spawn(process.execPath, [cli, "skills", "add", ...args], { cwd: project, env });
    let stdout = "";
    let stderr = "";
    child.stdout.on("data", (data) => (stdout += data));
    child.stderr.on("data", (data) => (stderr += data));
    child.on("exit", (code) => resolve({ code, stdout, stderr, output: stdout + stderr }));
    child.on("error", reject);
    child.stdin.end();
  });
}

function writeFakeNpx() {
  const dir = temporary("cave-skills-npx-");
  const bin = nodeStub(dir, "npx", `
import { mkdirSync, writeFileSync } from "node:fs";
import { homedir } from "node:os";
import { join } from "node:path";
const args = process.argv.slice(2);
if (process.env.NPX_ARGS_FILE) writeFileSync(process.env.NPX_ARGS_FILE, JSON.stringify(args));
if (process.env.NPX_EXIT) process.exit(Number(process.env.NPX_EXIT));
if (args.includes("--list") || args.includes("-l")) {
  console.log("tdd");
  process.exit(0);
}
const global = args.includes("--global") || args.includes("-g");
const root = args.includes("claude-code")
  ? join(global ? homedir() : process.cwd(), ".claude", "skills")
  : join(global ? homedir() : process.cwd(), ".agents", "skills");
const skill = join(root, "tdd");
mkdirSync(join(skill, "references"), { recursive: true });
writeFileSync(join(skill, "SKILL.md"), \`---
name: tdd
description: Test-drive code changes.
---

# TDD

\${"Write one failing test, then make it pass.\\n".repeat(500)}\`);
writeFileSync(join(skill, "references", "notes.md"), "resource stays plain and untouched\\n");
`);
  return { dir, bin };
}

function writeEngineStub() {
  const dir = temporary("cave-skills-add-engine-");
  return nativeStub(dir, "caveman-engine", `
const { writeFileSync } = require("node:fs");
const file = ARGV[3];
if (ARGV[0] !== "pixel" || ARGV[1] !== "render" || ARGV[2] !== "--dense" || !file) process.exit(2);
writeFileSync(file + ".px1.png", Buffer.from("png"));
console.log(JSON.stringify({ width: 10, height: 10, charsRendered: 10, droppedChars: 0, estTokens: 400 }));
console.log(JSON.stringify({ summary: true, pages: 1, textEstTokens: 5000, imageEstTokens: 400 }));
`);
}

function fakePath(fakeNpxDir) {
  return [fakeNpxDir, process.env.PATH ?? ""].join(delimiter);
}

test("skills add forwards Skills CLI source/options and pixelizes only installed skill", async () => {
  const project = temporary("cave-skills-add-project-");
  const existing = join(project, ".agents", "skills", "existing");
  mkdirSync(existing, { recursive: true });
  const existingBody = `---\nname: existing\ndescription: Leave this skill alone.\n---\n\nExisting body.\n`;
  writeFileSync(join(existing, "SKILL.md"), existingBody);
  const sameNameOtherAgent = join(project, ".claude", "skills", "tdd");
  mkdirSync(sameNameOtherAgent, { recursive: true });
  const otherAgentBody = `---\nname: tdd\ndescription: Existing Claude-only variant.\n---\n\nDo not rewrite me.\n`;
  writeFileSync(join(sameNameOtherAgent, "SKILL.md"), otherAgentBody);
  const fake = writeFakeNpx();
  const argsFile = join(project, "npx-args.json");
  const engine = writeEngineStub();

  const out = await runAdd(
    project,
    ["mattpocock/skills", "--skill", "tdd", "--agent", "codex", "-y"],
    { PATH: fakePath(fake.dir), NPX_ARGS_FILE: argsFile, CAVEMAN_ENGINE_BIN: engine },
  );
  assert.equal(out.code, 0, out.output);
  assert.deepEqual(JSON.parse(readFileSync(argsFile, "utf8")), [
    "--yes", "skills", "add", "mattpocock/skills", "--skill", "tdd", "--agent", "codex", "-y", "--copy",
  ]);

  const installed = join(project, ".agents", "skills", "tdd");
  const stub = readFileSync(join(installed, "SKILL.md"), "utf8");
  assert.match(stub, /<!-- caveman-pixel v1 sha256:[a-f0-9]{64} -->/);
  assert.ok(existsSync(join(installed, "SKILL.px1.png")));
  assert.match(readFileSync(join(installed, "SKILL.orig.md"), "utf8"), /^---\nname: tdd\n/);
  assert.equal(readFileSync(join(installed, "references", "notes.md"), "utf8"), "resource stays plain and untouched\n");
  assert.equal(readFileSync(join(existing, "SKILL.md"), "utf8"), existingBody, "unrelated installed skill must stay untouched");
  assert.equal(readFileSync(join(sameNameOtherAgent, "SKILL.md"), "utf8"), otherAgentBody, "same-name skill in unchanged agent root must stay untouched");
  assert.match(out.output, /tdd: 5000 → 520 est tokens \(−90% inferred\)/);
  assert.match(out.output, /not security-reviewed by Caveman/);
});

test("skills add preserves upstream exit code and does not claim installation", async () => {
  const project = temporary("cave-skills-add-fail-");
  const fake = writeFakeNpx();
  const out = await runAdd(project, ["mattpocock/skills", "-y"], {
    PATH: fakePath(fake.dir),
    NPX_EXIT: "7",
    CAVEMAN_ENGINE_BIN: writeEngineStub(),
  });
  assert.equal(out.code, 7, out.output);
  assert.doesNotMatch(out.output, /Third-party skill installed/);
  assert.ok(!existsSync(join(project, ".agents", "skills", "tdd", "SKILL.md")));
});

test("skills add --no-pixel installs plain text and does not leak Caveman flags upstream", async () => {
  const project = temporary("cave-skills-add-plain-");
  const fake = writeFakeNpx();
  const argsFile = join(project, "npx-args.json");
  const out = await runAdd(project, ["mattpocock/skills", "--skill", "tdd", "--agent", "codex", "-y", "--no-pixel"], {
    PATH: fakePath(fake.dir),
    NPX_ARGS_FILE: argsFile,
    CAVEMAN_ENGINE_BIN: join(project, "must-not-run"),
  });
  assert.equal(out.code, 0, out.output);
  const forwarded = JSON.parse(readFileSync(argsFile, "utf8"));
  assert.ok(!forwarded.includes("--no-pixel"));
  assert.ok(!forwarded.includes("--copy"));
  const installed = join(project, ".agents", "skills", "tdd");
  assert.doesNotMatch(readFileSync(join(installed, "SKILL.md"), "utf8"), /caveman-pixel/);
  assert.ok(!existsSync(join(installed, "SKILL.orig.md")));
  assert.match(out.output, /Installed plain text \(--no-pixel\)/);
});

test("skills add --list stays an upstream read-only listing", async () => {
  const project = temporary("cave-skills-add-list-");
  const fake = writeFakeNpx();
  const argsFile = join(project, "npx-args.json");
  const out = await runAdd(project, ["mattpocock/skills", "--list"], {
    PATH: fakePath(fake.dir),
    NPX_ARGS_FILE: argsFile,
  });
  assert.equal(out.code, 0, out.output);
  assert.match(out.stdout, /tdd/);
  assert.ok(!JSON.parse(readFileSync(argsFile, "utf8")).includes("--copy"));
  assert.doesNotMatch(out.output, /Third-party skill installed/);
  assert.ok(!existsSync(join(project, ".agents", "skills", "tdd")));
});

for (const options of [["--list"], ["--no-pixel", "--agent", "claude-code"]]) {
  test(`global skills add ${options[0]} does not resolve unrelated Codex home`, async () => {
    const project = temporary("cave-skills-add-unrelated-home-");
    const home = temporary("cave-skills-add-isolated-home-");
    const fake = writeFakeNpx();
    const out = await runAdd(project, ["mattpocock/skills", "--global", ...options], {
      PATH: fakePath(fake.dir), HOME: home, CODEX_HOME: join(project, "absent-codex-profile"),
    });
    assert.equal(out.code, 0, out.output);
  });
}

test("skills add supports upstream URL, Claude Code, and global scope", async () => {
  const project = temporary("cave-skills-add-global-project-");
  const isolatedHome = temporary("cave-skills-add-home-");
  const fake = writeFakeNpx();
  const out = await runAdd(
    project,
    ["https://github.com/mattpocock/skills", "--skill", "tdd", "--agent", "claude-code", "--global", "-y"],
    { PATH: fakePath(fake.dir), HOME: isolatedHome, CAVEMAN_ENGINE_BIN: writeEngineStub() },
  );
  assert.equal(out.code, 0, out.output);
  const installed = join(isolatedHome, ".claude", "skills", "tdd");
  assert.match(readFileSync(join(installed, "SKILL.md"), "utf8"), /caveman-pixel v1/);
  assert.ok(existsSync(join(installed, "SKILL.orig.md")));
  assert.ok(existsSync(join(installed, "SKILL.px1.png")));
  assert.equal(homedir() === isolatedHome, false, "test HOME must not replace parent process home");
});

test("skills add scans current upstream global Codex path", async () => {
  const project = temporary("cave-skills-add-global-codex-project-");
  const isolatedHome = temporary("cave-skills-add-global-codex-home-");
  const fake = writeFakeNpx();
  const out = await runAdd(project, ["mattpocock/skills", "--skill", "tdd", "--agent", "codex", "--global", "-y"], {
    PATH: fakePath(fake.dir),
    HOME: isolatedHome,
    CAVEMAN_ENGINE_BIN: writeEngineStub(),
  });
  assert.equal(out.code, 0, out.output);
  const installed = join(isolatedHome, ".agents", "skills", "tdd");
  assert.match(readFileSync(join(installed, "SKILL.md"), "utf8"), /caveman-pixel v1/);
  assert.ok(existsSync(join(installed, "SKILL.orig.md")));
  assert.ok(existsSync(join(installed, "SKILL.px1.png")));
});

test("skills add keeps successful upstream install as text when engine is missing", async () => {
  const project = temporary("cave-skills-add-no-engine-");
  const fake = writeFakeNpx();
  const out = await runAdd(project, ["mattpocock/skills", "--skill", "tdd", "--agent", "codex", "-y"], {
    PATH: fakePath(fake.dir),
    CAVEMAN_ENGINE_BIN: join(project, "missing-caveman-engine"),
  });
  assert.equal(out.code, 0, out.output);
  const installed = join(project, ".agents", "skills", "tdd");
  assert.match(readFileSync(join(installed, "SKILL.md"), "utf8"), /^---\nname: tdd\n/);
  assert.ok(!existsSync(join(installed, "SKILL.orig.md")));
  assert.match(out.output, /caveman-engine not found — kept as text/);
});

test("skills add fails loudly before writes when npx is missing", async () => {
  const project = temporary("cave-skills-add-no-npx-");
  const out = await runAdd(project, ["mattpocock/skills", "--skill", "tdd", "--agent", "codex", "-y"], {
    PATH: "",
    CAVEMAN_ENGINE_BIN: writeEngineStub(),
  });
  assert.equal(out.code, 127, out.output);
  assert.match(out.stderr, /npx not found; install Node\.js with npm/);
  assert.ok(!existsSync(join(project, ".agents", "skills", "tdd")));
});
