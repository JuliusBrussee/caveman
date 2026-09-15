#!/usr/bin/env node
// Keep concrete profile-data commits atomic while allowing schema changes to carry
// their compiler and tests. Used by .github/workflows/profiles.yml.

import { spawnSync } from "node:child_process";
import { fileURLToPath } from "node:url";
import { resolve } from "node:path";

const REVISION_RE = /^[0-9a-f]{40,64}$/i;
const SCHEMA_PATH = "agents/profiles/schema.json";
const GENERATED_PATHS = new Set([
  "agents/agents.json",
  "packages/cli/src/agents.generated.ts",
  "packages/cli/src/reserved-verbs.generated.ts",
]);
const SCHEMA_SUPPORT_PATHS = new Set([
  SCHEMA_PATH,
  "agents/compile.mjs",
  "agents/check-profile-scope.mjs",
  "packages/cli/tests/agent-registry.runtime.mjs",
  "tests/installer/agent-registry-compile.test.mjs",
  "tests/installer/agent-registry-scope.test.mjs",
  ...GENERATED_PATHS,
]);

function runGit(cwd, args, encoding = "utf8") {
  return spawnSync("git", args, { cwd, encoding, maxBuffer: 8 * 1024 * 1024 });
}

function requireGit(result, operation) {
  if (!result.error && result.status === 0) return result.stdout;
  const detail = result.stderr?.toString().trim() || result.error?.message || `exit ${result.status}`;
  throw new Error(`${operation} failed: ${detail}`);
}

function isConcreteProfile(path) {
  return path !== SCHEMA_PATH && /^agents\/profiles\/[a-z0-9][a-z0-9-]*\.json$/.test(path);
}

function isAllowedConcreteProfileCommitPath(path) {
  return isConcreteProfile(path) || GENERATED_PATHS.has(path);
}

export function checkProfileScope({ base, head, cwd = process.cwd() }) {
  if (!REVISION_RE.test(base) || !REVISION_RE.test(head)) {
    throw new Error("base and head must be full Git object IDs");
  }

  const baseRegistry = runGit(cwd, ["cat-file", "-e", `${base}:agents/agents.json`]);
  if (baseRegistry.error || baseRegistry.status !== 0) {
    return { ok: true, skipped: true, message: "profile registry absent at base (initial import); skipping lane-scope gate" };
  }

  const commitsText = requireGit(runGit(cwd, ["rev-list", "--reverse", `${base}..${head}`]), "git rev-list");
  const commits = commitsText.split(/\r?\n/).filter(Boolean);
  let contractTouched = false;

  for (const commit of commits) {
    const changedBuffer = requireGit(
      // -m takes the union across merge parents; otherwise a merge commit can hide
      // an out-of-scope path from an ordinary single-parent diff-tree view.
      runGit(cwd, ["diff-tree", "--root", "-m", "--no-commit-id", "--name-only", "-r", "-z", commit], null),
      `git diff-tree ${commit}`,
    );
    const changedFiles = changedBuffer.toString("utf8").split("\0").filter(Boolean);
    const schemaTouched = changedFiles.includes(SCHEMA_PATH);
    const concreteProfileTouched = changedFiles.some(isConcreteProfile);
    const invalidProfilePath = changedFiles.find((path) => path.startsWith("agents/profiles/")
      && path !== SCHEMA_PATH
      && !isConcreteProfile(path));
    if (invalidProfilePath) {
      return { ok: false, message: `profile lane rejects non-contract profile path in commit ${commit}: ${invalidProfilePath}` };
    }
    if (schemaTouched || concreteProfileTouched) contractTouched = true;

    if (schemaTouched) {
      const outside = changedFiles.find((path) => !SCHEMA_SUPPORT_PATHS.has(path));
      if (outside) {
        return { ok: false, message: `profile lane rejects out-of-scope schema-support path in commit ${commit}: ${outside}` };
      }
      continue;
    }
    if (!concreteProfileTouched) continue;

    const outside = changedFiles.find((path) => !isAllowedConcreteProfileCommitPath(path));
    if (outside) {
      return { ok: false, message: `profile lane rejects out-of-scope path in commit ${commit}: ${outside}` };
    }
  }

  if (!contractTouched) {
    return { ok: false, message: "profile lane requires one agent profile or profile schema change" };
  }
  return { ok: true, skipped: false, message: "profile commit scope valid" };
}

function main() {
  const [base, head, ...rest] = process.argv.slice(2);
  if (!base || !head || rest.length > 0) {
    process.stderr.write("usage: node agents/check-profile-scope.mjs <base-sha> <head-sha>\n");
    return 2;
  }
  try {
    const result = checkProfileScope({ base, head });
    (result.ok ? process.stdout : process.stderr).write(`${result.message}\n`);
    return result.ok ? 0 : 1;
  } catch (error) {
    process.stderr.write(`profile scope check failed: ${error.message}\n`);
    return 2;
  }
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  process.exit(main());
}
