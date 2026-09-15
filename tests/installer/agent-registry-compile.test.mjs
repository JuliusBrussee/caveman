import test from "node:test";
import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { cpSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const root = join(dirname(fileURLToPath(import.meta.url)), "..", "..");

function fixtureRepo({ workflow = (text) => text, includeWorkflow = true } = {}) {
  const repo = mkdtempSync(join(tmpdir(), "caveman-agent-compile-"));
  mkdirSync(join(repo, "agents", "profiles"), { recursive: true });
  cpSync(join(root, "agents", "compile.mjs"), join(repo, "agents", "compile.mjs"));
  cpSync(join(root, "agents", "reserved-verbs.json"), join(repo, "agents", "reserved-verbs.json"));
  cpSync(join(root, "agents", "profiles"), join(repo, "agents", "profiles"), { recursive: true });
  mkdirSync(join(repo, "shared", "provider-catalog", "catalog"), { recursive: true });
  cpSync(
    join(root, "shared", "provider-catalog", "catalog", "current.yaml"),
    join(repo, "shared", "provider-catalog", "catalog", "current.yaml"),
  );
  mkdirSync(join(repo, "packages", "cli", "src"), { recursive: true });
  cpSync(join(root, "packages", "cli", "src", "index.ts"), join(repo, "packages", "cli", "src", "index.ts"));
  if (includeWorkflow) {
    mkdirSync(join(repo, ".github", "workflows"), { recursive: true });
    const source = readFileSync(join(root, ".github", "workflows", "agent-conformance.yml"), "utf8");
    writeFileSync(join(repo, ".github", "workflows", "agent-conformance.yml"), workflow(source));
  }
  return repo;
}

function compile(repo) {
  const env = { ...process.env };
  delete env.CAVEMAN_CATALOG_FILE;
  delete env.CAVEMAN_CLI_DIR;
  delete env.CAVEMAN_CONFORMANCE_WORKFLOW;
  delete env.CAVEMAN_PROFILES_DIR;
  delete env.CAVEMAN_REGISTRY_GATES_ADVISORY;
  return spawnSync(process.execPath, [join(repo, "agents", "compile.mjs")], {
    cwd: repo,
    env,
    encoding: "utf8",
  });
}

// Pins come from the profiles, never from literals here: the nightly drift
// workflow bumps tested_agent_version, and a literal in this file turned every
// legitimate bump into a red main (2026-09-03).
function pinOf(id) {
  return JSON.parse(readFileSync(join(root, "agents", "profiles", `${id}.json`), "utf8")).tested_agent_version;
}
const KILO_PIN = pinOf("kilo");
const QWEN_PIN = pinOf("qwen");
const KILO_BUMPED = KILO_PIN.replace(/(\d+)$/, (n) => String(Number(n) + 1));
const escapeRegExp = (value) => value.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");

function replaceExactly(text, from, to) {
  assert.equal(text.split(from).length, 2, `fixture expected one ${JSON.stringify(from)}`);
  return text.replace(from, to);
}

test("compiler resolves default conformance workflow inside repository and catches pin mismatch", (t) => {
  const repo = fixtureRepo({
    workflow: (text) => replaceExactly(text, `@kilocode/cli@${KILO_PIN}`, `@kilocode/cli@${KILO_BUMPED}`),
  });
  t.after(() => rmSync(repo, { recursive: true, force: true }));
  const result = compile(repo);
  assert.notEqual(result.status, 0);
  assert.match(result.stderr, new RegExp(`pins kilo@${escapeRegExp(KILO_BUMPED)} but profile tested_agent_version is ${escapeRegExp(KILO_PIN)}`));
});

test("compiler fails closed when conformance workflow is missing", (t) => {
  const repo = fixtureRepo({ includeWorkflow: false });
  t.after(() => rmSync(repo, { recursive: true, force: true }));
  const result = compile(repo);
  assert.notEqual(result.status, 0);
  assert.match(result.stderr, /conformance workflow not readable.*cannot verify shipped profile pins \(fail-closed\)/);
});

test("compiler rejects unparseable pinned install", (t) => {
  const repo = fixtureRepo({
    workflow: (text) => replaceExactly(text, `@kilocode/cli@${KILO_PIN}`, "@kilocode/cli@latest"),
  });
  t.after(() => rmSync(repo, { recursive: true, force: true }));
  const result = compile(repo);
  assert.notEqual(result.status, 0);
  assert.match(result.stderr, /pinned install for kilo must carry exactly one parseable version pin.*found 0/);
});

test("compiler rejects duplicate pinned profile id", (t) => {
  const repo = fixtureRepo({
    workflow: (text) => replaceExactly(
      text,
      `          - id: kilo\n            install: npm install -g @kilocode/cli@${KILO_PIN}`,
      `          - id: kilo\n            install: npm install -g @kilocode/cli@${KILO_PIN}\n          - id: kilo\n            install: npm install -g @kilocode/cli@${KILO_PIN}`,
    ),
  });
  t.after(() => rmSync(repo, { recursive: true, force: true }));
  const result = compile(repo);
  assert.notEqual(result.status, 0);
  assert.match(result.stderr, /duplicate pinned profile id\(s\): kilo/);
});

for (const [id, block] of [
  ["kilo", `          - id: kilo\n            install: npm install -g @kilocode/cli@${KILO_PIN}\n`],
  ["qwen", `          - id: qwen\n            install: npm install -g @qwen-code/qwen-code@${QWEN_PIN}\n`],
]) {
  test(`compiler rejects missing shipped ${id} pin entry`, (t) => {
    const repo = fixtureRepo({ workflow: (text) => replaceExactly(text, block, "") });
    t.after(() => rmSync(repo, { recursive: true, force: true }));
    const result = compile(repo);
    assert.notEqual(result.status, 0);
    assert.match(result.stderr, new RegExp(`missing shipped profile id\\(s\\): ${id}`));
  });
}
