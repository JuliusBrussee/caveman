import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
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

// The Windows suite runs on a PR unless every changed path is one Windows
// cannot break (docs, benchmarks, deploy, prose). An allowlist of Windows
// code missed engine/ccr/*_windows.go, mem/, shared/, go.mod and the like.
test("engine-ci runs Windows on a PR unless every change is Windows-irrelevant", { skip: process.platform === "win32" && "bash step" }, () => {
  const lines = readFileSync(join(root, ".github", "workflows", "engine-ci.yml"), "utf8").split("\n");
  const at = lines.findIndex((line) => line.trim() === "id: windows-scope");
  const run = lines.findIndex((line, i) => i > at && line.trim() === "run: |");
  const indent = lines[run].search(/\S/);
  const body = [];
  for (const line of lines.slice(run + 1)) {
    if (line.trim() && line.search(/\S/) <= indent) break;
    body.push(line.slice(indent + 2));
  }
  const dir = mkdtempSync(join(tmpdir(), "caveman-windows-scope-"));
  try {
    writeFileSync(join(dir, "git"), '#!/bin/sh\n[ -n "$FAKE_DIFF_FAIL" ] && exit 128\nprintf \'%s\\n\' "$FAKE_DIFF"\n', { mode: 0o755 });
    const windows = (diff, extra = {}) => {
      const output = join(dir, "output");
      writeFileSync(output, "");
      const r = spawnSync("bash", ["-eo", "pipefail", "-c", body.join("\n")], {
        encoding: "utf8", env: { PATH: `${dir}:/usr/bin:/bin`, GITHUB_OUTPUT: output, FAKE_DIFF: diff, ...extra },
      });
      assert.equal(r.status, 0, r.stderr);
      return readFileSync(output, "utf8") === "windows=true\n";
    };
    for (const changed of ["engine/ccr/launch_windows.go", "browse/cmd/caveman-browse/main_windows.go", "agents/delegate/run.mjs",
      "src/mcp-servers/caveman-shrink/index.js", "packages/pi-extension/src/index.ts", "packages/subagent-tax/run.mjs", "mem/store.go",
      "shared/catalog.json", "tests/test_hooks.py", "go.mod", "go.sum", "package.json", "pnpm-lock.yaml", "skills/caveman/SKILL.md",
      "installer/install.js", "docs/a.md\nproxy/main.go"]) {
      assert.equal(windows(changed), true, `${changed} skipped the Windows suite`);
    }
    for (const changed of ["docs/guide.md", "README.md", "benchmarks/run.py\nevals/llm_run.py", "deploy/fly.toml", "supabase/migrations/1.sql", "packages/cli/README.md"]) {
      assert.equal(windows(changed), false, `${changed} ran the Windows suite`);
    }
    assert.equal(windows("", { FAKE_DIFF_FAIL: "1" }), true, "an unreadable diff must run Windows");
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
});
