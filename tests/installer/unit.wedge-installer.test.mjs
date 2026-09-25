import assert from "node:assert/strict";
import { execFile } from "node:child_process";
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import test from "node:test";
import { promisify } from "node:util";

const execFileAsync = promisify(execFile);
const root = join(dirname(fileURLToPath(import.meta.url)), "..", "..");
const script = join(root, "packages", "cli", "scripts", "gen-wedge-installer.mjs");
const targets = [
  join(root, "mcp", "bin"),
  join(root, "shrink", "bin"),
  join(root, "browse", "bin"),
];

// mcp, shrink, browse and the CLI each run this script from their own
// prepack/pretest, and `pnpm -r` runs them concurrently. Four processes writing
// the same bytes to the same shared paths is EBUSY on Windows, which is exactly
// how it failed in CI.
test("concurrent wedge generation does not collide on shared outputs", async () => {
  const before = targets.map((dir) => readFileSync(join(dir, "binary-installer.generated.mjs")));
  const results = await Promise.all(
    Array.from({ length: 8 }, () => execFileAsync(process.execPath, [script], { cwd: root })),
  );
  for (const result of results) {
    assert.match(result.stdout, /generated standalone wedge installers for /);
  }
  targets.forEach((dir, index) => {
    assert.deepEqual(readFileSync(join(dir, "binary-installer.generated.mjs")), before[index]);
  });
});

test("wedge generation rewrites a drifted output", async () => {
  const path = join(root, "mcp", "bin", "release.generated.mjs");
  const original = readFileSync(path);
  const { writeFileSync } = await import("node:fs");
  writeFileSync(path, "// drifted\n");
  try {
    await execFileAsync(process.execPath, [script], { cwd: root });
    assert.deepEqual(readFileSync(path), original, "skip-if-identical must not skip a drifted file");
  } finally {
    writeFileSync(path, original);
  }
});
