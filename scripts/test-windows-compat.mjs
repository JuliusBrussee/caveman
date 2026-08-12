#!/usr/bin/env node
import { mkdirSync } from "node:fs";
import { createRequire } from "node:module";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { spawnSync } from "node:child_process";
import { fileURLToPath } from "node:url";

const root = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const require = createRequire(import.meta.url);
const { portableInvocation } = require(join(root, "bin", "lib", "portable-process.js"));
const goCache = join(tmpdir(), "caveman-windows-go-cache");
mkdirSync(goCache, { recursive: true });

function run(label, command, args, options = {}) {
  process.stderr.write(`\n[windows-compat] ${label}\n`);
  const result = spawnSync(command, args, {
    cwd: root,
    stdio: "inherit",
    ...options,
  });
  if (result.error) throw result.error;
  if (result.status !== 0) process.exit(result.status ?? 1);
}

function runPnpm(label, args) {
  const invocation = portableInvocation("pnpm", args);
  run(label, invocation.command, invocation.args);
}

runPnpm("build CLI", ["--dir", "packages/cli", "build"]);
runPnpm("build agent", ["--dir", "packages/agent", "build"]);
run("Node Windows contracts", process.execPath, [
  "--test",
  "packages/cli/tests/windows-platform.runtime.mjs",
  "packages/cli/tests/portable-command.runtime.mjs",
  "packages/cli/tests/delegate-windows.runtime.mjs",
  "tests/installer/binary-installer-platform.test.mjs",
  "tests/installer/release-binaries.test.mjs",
  "tests/installer/windows-source-install.test.mjs",
  "tests/installer/portable-process.test.mjs",
  "tests/installer/mcp-shrink-windows.test.mjs",
  "packages/subagent-tax/tests/process-tree.test.mjs",
  "packages/agent/tests/windows-process.runtime.mjs",
  "packages/agent/tests/windows-shell-smoke.runtime.mjs",
]);

const baseGoEnv = {
  ...process.env,
  CGO_ENABLED: "0",
  GOOS: "windows",
  GOCACHE: goCache,
  GOTMPDIR: tmpdir(),
};
const hostGoArch = { x64: "amd64", arm64: "arm64" }[process.arch];
for (const arch of ["amd64", "arm64"]) {
  const args = ["test", "-count=1", "-run", "^$"];
  // Compiled test binaries only execute when host OS and arch both match;
  // everywhere else a no-op -exec keeps this a pure compile check (a Windows
  // amd64 runner cannot execute the arm64 binaries).
  if (process.platform !== "win32") {
    args.push("-exec", process.platform === "darwin" ? "/usr/bin/true" : "/bin/true");
  } else if (arch !== hostGoArch) {
    args.push("-exec", "cmd /c exit 0");
  }
  args.push("./...");
  run(`Go Windows/${arch} compile`, "go", args, { env: { ...baseGoEnv, GOARCH: arch } });
}

process.stdout.write("windows compatibility gate passed\n");
