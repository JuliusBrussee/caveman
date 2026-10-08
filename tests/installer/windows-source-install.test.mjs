import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";

const root = join(dirname(fileURLToPath(import.meta.url)), "..", "..");
const binaries = [
  "caveman-proxy",
  "caveman-engine",
  "caveman-mcp",
  "cavemem",
  "caveman-browse",
  "caveman-shrink",
];

test("macOS/Linux source installer builds every runtime companion", () => {
  const source = readFileSync(join(root, "scripts", "install-local-cli.sh"), "utf8");
  for (const binary of binaries) {
    assert.match(source, new RegExp(`go build -o \\\"\\$cave_bin/${binary}\\\"`));
  }
});

test("macOS/Linux source installer keeps its shim out of top-level bin/ (#1035)", () => {
  // Plugin root = repo root, so a checkout added as a local marketplace would
  // put a top-level bin/ on PATH even though the shim is gitignored.
  const source = readFileSync(join(root, "scripts", "install-local-cli.sh"), "utf8");
  assert.doesNotMatch(source, /mkdir -p bin\b|> bin\/|\$PWD\/bin\b/);
});

test("Windows source installer builds every runtime companion as .exe", () => {
  const source = readFileSync(join(root, "scripts", "install-local-cli.ps1"), "utf8");
  for (const binary of binaries) assert.match(source, new RegExp(`\\\"${binary}\\\"\\s*=`));
  assert.match(source, /\"\$\(\$Entry\.Key\)\.exe\"/);
  assert.match(source, /npm link/);
});

test("native-hook benchmark uses Windows named pipe instead of refusing platform", () => {
  const source = readFileSync(join(root, "packages", "cli", "scripts", "benchmark-native-hooks.mjs"), "utf8");
  assert.doesNotMatch(source, /requires a POSIX Unix socket/);
  assert.match(source, /caveman-native-/);
  assert.match(source, /process\.platform === "win32"/);
});

test("CI takes pnpm version only from packageManager", () => {
  const source = readFileSync(join(root, ".github", "workflows", "engine-ci.yml"), "utf8");
  assert.match(source, /uses: pnpm\/action-setup@[0-9a-f]{40} # v\d+\.\d+\.\d+/);
  assert.doesNotMatch(source, /pnpm\/action-setup@[0-9a-f]{40}[^\n]*\n\s+with:\n\s+version:/);
});
