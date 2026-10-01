import test from "node:test";
import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

const root = join(dirname(fileURLToPath(import.meta.url)), "..", "..");
// A bare Windows path is not a valid ESM specifier; go through a file URL.
const { checkProfileScope } = await import(pathToFileURL(join(root, "agents", "check-profile-scope.mjs")).href);

function fixtureRepo(t) {
  const repo = mkdtempSync(join(tmpdir(), "caveman-profile-scope-"));
  t.after(() => rmSync(repo, { recursive: true, force: true }));
  const git = (...args) => {
    const result = spawnSync("git", args, { cwd: repo, encoding: "utf8" });
    assert.equal(result.status, 0, result.stderr);
    return result.stdout.trim();
  };
  const put = (path, content = "{}\n") => {
    const file = join(repo, path);
    mkdirSync(dirname(file), { recursive: true });
    writeFileSync(file, content);
  };
  const commit = (message) => {
    git("add", ".");
    git("commit", "-m", message);
    return git("rev-parse", "HEAD");
  };

  git("init", "-q");
  git("config", "user.email", "profile-scope@example.invalid");
  git("config", "user.name", "Profile Scope Test");
  put("agents/agents.json");
  const base = commit("base");
  return { repo, put, commit, base };
}

test("profile scope accepts closed schema-support file set", (t) => {
  const { repo, put, commit, base } = fixtureRepo(t);
  for (const path of [
    "agents/profiles/schema.json",
    "agents/compile.mjs",
    "agents/check-profile-scope.mjs",
    "agents/agents.json",
    "packages/cli/src/agents.generated.ts",
    "packages/cli/src/reserved-verbs.generated.ts",
    "packages/cli/tests/agent-registry.runtime.mjs",
    "tests/installer/agent-registry-compile.test.mjs",
    "tests/installer/agent-registry-scope.test.mjs",
  ]) put(path, `${path}\n`);
  const head = commit("schema support");
  assert.deepEqual(checkProfileScope({ base, head, cwd: repo }), {
    ok: true,
    skipped: false,
    message: "profile commit scope valid",
  });
});

for (const path of [
  "packages/cli/src/index.ts",
  "agents/docs/CLAUDE.md",
  "tests/installer/unrelated.test.mjs",
  ".github/workflows/profiles.yml",
]) {
  test(`profile scope rejects schema commit support path ${path}`, (t) => {
    const { repo, put, commit, base } = fixtureRepo(t);
    put("agents/profiles/schema.json");
    put(path, "out of scope\n");
    const head = commit("schema plus broad support");
    const result = checkProfileScope({ base, head, cwd: repo });
    assert.equal(result.ok, false);
    assert.match(result.message, new RegExp(`out-of-scope schema-support path.*${path.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")}`));
  });
}

test("profile scope accepts exact concrete JSON plus generated artifacts", (t) => {
  const { repo, put, commit, base } = fixtureRepo(t);
  put("agents/profiles/new-agent.json");
  put("agents/agents.json", "generated\n");
  put("packages/cli/src/agents.generated.ts", "generated\n");
  const head = commit("new profile");
  assert.equal(checkProfileScope({ base, head, cwd: repo }).ok, true);
});

for (const path of [
  "agents/profiles/NewAgent.json",
  "agents/profiles/-agent.json",
  "agents/profiles/.json",
  "agents/profiles/new_agent.json",
  "agents/profiles/new-agent.yaml",
  "agents/profiles/new-agent.json.bak",
  "agents/profiles/nested/new-agent.json",
  "agents/profiles/README.md",
]) {
  test(`profile scope rejects non-contract profile path ${path}`, (t) => {
    const { repo, put, commit, base } = fixtureRepo(t);
    put("agents/profiles/schema.json");
    const schema = commit("valid schema contract");
    put(path);
    const head = commit("invalid profile namespace path");
    const result = checkProfileScope({ base, head, cwd: repo });
    assert.equal(result.ok, false);
    assert.match(result.message, /rejects non-contract profile path/);
    assert.match(result.message, new RegExp(path.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")));
    assert.notEqual(schema, head);
  });
}

test("profile scope keeps schema and concrete profile changes in separate commits", (t) => {
  const { repo, put, commit, base } = fixtureRepo(t);
  put("agents/profiles/schema.json");
  put("agents/profiles/new-agent.json");
  const head = commit("mixed schema and concrete profile");
  const result = checkProfileScope({ base, head, cwd: repo });
  assert.equal(result.ok, false);
  assert.match(result.message, /out-of-scope schema-support path.*agents\/profiles\/new-agent\.json/);
});

test("profile scope rejects non-JSON neighbor in concrete profile commit", (t) => {
  const { repo, put, commit, base } = fixtureRepo(t);
  put("agents/profiles/new-agent.json");
  put("agents/profiles/README.md", "not profile data\n");
  const head = commit("profile plus namespace neighbor");
  const result = checkProfileScope({ base, head, cwd: repo });
  assert.equal(result.ok, false);
  assert.match(result.message, /rejects non-contract profile path.*agents\/profiles\/README\.md/);
});

// gitIn runs git in an existing fixture repo, for the branch/merge shapes the
// helper above does not cover.
function gitIn(repo) {
  return (...args) => {
    const result = spawnSync("git", args, { cwd: repo, encoding: "utf8" });
    assert.equal(result.status, 0, result.stderr);
    return result.stdout.trim();
  };
}

// A long-lived profile branch has to be able to take its base branch's commits.
// Merging them in leaves a merge commit whose INHERITED paths are the base
// branch's, not the merge's own work, so the lane must not read them as an
// out-of-scope profile commit. Without this the gate makes "carries a profile
// bump" and "is up to date with main" mutually exclusive.
test("profile scope accepts a merge that brings the base branch in", (t) => {
  const { repo, put, commit, base } = fixtureRepo(t);
  const git = gitIn(repo);

  // The base branch moves on, touching files the profile lane does not allow.
  git("checkout", "-q", "-b", "mainline", base);
  put(".claude-plugin/plugin.json", "{}\n");
  put("README.md", "mainline\n");
  const mainline = commit("mainline moves on");

  // The profile branch carries a properly scoped concrete-profile commit...
  git("checkout", "-q", "-b", "profile-lane", base);
  put("agents/profiles/opencode.json", '{"id":"opencode"}\n');
  put("agents/agents.json", '{"agents":[]}\n');
  commit("bump the opencode pin");

  // ...and then merges the base branch in, as any long-lived branch must.
  git("merge", "-q", "--no-ff", "-m", "Merge mainline", mainline);
  const head = git("rev-parse", "HEAD");

  assert.deepEqual(checkProfileScope({ base: mainline, head, cwd: repo }), {
    ok: true,
    skipped: false,
    message: "profile commit scope valid",
  });
});

// The guard the merge handling must not cost us: a merge may still not be used
// as a side door. If the RESOLUTION itself edits a concrete profile, that is the
// merge commit's own work and the ordinary scope rule applies to it in full.
test("profile scope still rejects a profile edited by a merge resolution", (t) => {
  const { repo, put, commit, base } = fixtureRepo(t);
  const git = gitIn(repo);

  git("checkout", "-q", "-b", "mainline", base);
  put("agents/profiles/opencode.json", '{"id":"opencode","v":"mainline"}\n');
  const mainline = commit("mainline edits the profile");

  git("checkout", "-q", "-b", "profile-lane", base);
  put("agents/profiles/opencode.json", '{"id":"opencode","v":"lane"}\n');
  put("agents/agents.json", '{"agents":[]}\n');
  commit("lane edits the same profile");

  // Conflicting edits to the same profile: resolve with a third value, and
  // smuggle an out-of-scope path into the same resolution commit.
  const merge = spawnSync("git", ["merge", "--no-ff", "-m", "Merge mainline", mainline], { cwd: repo, encoding: "utf8" });
  assert.notEqual(merge.status, 0, "the profile edit must actually conflict for this test to mean anything");
  put("agents/profiles/opencode.json", '{"id":"opencode","v":"resolved"}\n');
  put(".claude-plugin/plugin.json", "{}\n");
  git("add", ".");
  git("commit", "-q", "--no-edit");
  const head = git("rev-parse", "HEAD");

  const result = checkProfileScope({ base: mainline, head, cwd: repo });
  assert.equal(result.ok, false, "a merge resolution that edits a profile is still a profile commit");
  assert.match(result.message, /out-of-scope path/);
  assert.match(result.message, /\.claude-plugin\/plugin\.json/);
});
