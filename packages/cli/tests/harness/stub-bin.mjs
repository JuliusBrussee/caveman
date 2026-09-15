import { chmodSync, copyFileSync, linkSync, mkdirSync, writeFileSync } from "node:fs";
import { delimiter, join } from "node:path";

// Fake binaries have to be launchable the same way the real ones are, and
// Windows offers none of the POSIX shortcuts: there is no shebang, and Node
// refuses to spawn a .cmd/.bat without a shell (CVE-2024-27980), so neither an
// extensionless `#!/bin/sh` file nor a plain .cmd is spawnable by execFile.
//
// Two shapes exist here because the CLI launches things two ways:
//
//   nodeStub    for commands routed through portableInvocation (agent hosts
//               such as `pi`). On Windows that is a .cmd Node shim, which
//               portableInvocation unwraps into `node <target> <args>` — the
//               same shape npm/pnpm park on PATH.
//   nativeStub  for the Go binaries the CLI execFile's directly
//               (caveman-proxy, caveman-mcp). On Windows that must be a REAL
//               executable, so the stub is node.exe hardlinked under the
//               binary's name; the preload installed by stubEnv() answers and
//               exits before Node resolves argv[1] as a script. The product
//               spawns a native .exe, exactly as it does in production.
//
// Both shapes hand the stub body its arguments as `ARGV`, so one source string
// works on every platform. nodeStub bodies are ESM, nativeStub bodies are CJS —
// a Node preload cannot be an ES module.
const isWindows = process.platform === "win32";

const DISPATCH = `// Preloaded into every node process that inherits this env — including the CLI
// under test. It only acts when the running executable is one of the stubs.
const { basename, join } = require("node:path");
const { existsSync } = require("node:fs");
const stub = join(__dirname, basename(process.execPath, ".exe") + ".stub.cjs");
if (existsSync(stub)) {
  // Node resolved argv[1] against cwd on its way to treating it as a script;
  // basename gives the stub back the word the product actually passed.
  globalThis.__stubArgv = [basename(process.argv[1] ?? ""), ...process.argv.slice(2)];
  require(stub);
  process.exit(0);
}
`;

// A command the CLI launches through portableInvocation.
export function nodeStub(dir, name, source) {
  mkdirSync(dir, { recursive: true });
  const target = join(dir, `${name}.stub.mjs`);
  writeFileSync(target, `const ARGV = process.argv.slice(2);\n${source}`);
  if (!isWindows) {
    const path = join(dir, name);
    writeFileSync(path, `#!/bin/sh\nexec "${process.execPath}" "${target}" "$@"\n`, { mode: 0o755 });
    chmodSync(path, 0o755);
    return path;
  }
  const path = join(dir, `${name}.cmd`);
  // The `"%~dp0\<file>" %*` form is the only one parseWindowsNodeShim accepts.
  writeFileSync(path, ["@echo off", `node "%~dp0\\${name}.stub.mjs" %*`, "exit /b %errorlevel%", ""].join("\r\n"));
  return path;
}

// A binary the CLI execFile's directly. Requires stubEnv() for the child env.
export function nativeStub(dir, name, source) {
  mkdirSync(dir, { recursive: true });
  if (!isWindows) {
    const path = join(dir, name);
    writeFileSync(path, `#!/usr/bin/env node\nconst ARGV = process.argv.slice(2);\n${source}`, { mode: 0o755 });
    chmodSync(path, 0o755);
    return path;
  }
  writeFileSync(join(dir, `${name}.stub.cjs`), `const ARGV = globalThis.__stubArgv;\n${source}`);
  const path = join(dir, `${name}.exe`);
  // Hardlink, so no 100MB copy per fixture; falls back when node.exe and the
  // temp dir sit on different volumes.
  try { linkSync(process.execPath, path); } catch { copyFileSync(process.execPath, path); }
  return path;
}

// Puts `dir` on PATH (respecting the existing key's casing — a spread copy of
// process.env loses Windows' case-insensitivity, so setting "PATH" next to an
// inherited "Path" would leave two of them) and arms the nativeStub preload.
export function stubEnv(base, dir) {
  mkdirSync(dir, { recursive: true });
  const env = { ...base };
  const pathKey = Object.keys(env).find((key) => key.toLowerCase() === "path") ?? "PATH";
  env[pathKey] = `${dir}${delimiter}${env[pathKey] ?? ""}`;
  if (!isWindows) return env;
  const dispatch = join(dir, "stub-dispatch.cjs");
  writeFileSync(dispatch, DISPATCH);
  // Node parses NODE_OPTIONS itself, and inside a quoted value a backslash is
  // an escape character — a raw Windows path arrives with every separator
  // eaten (`C:UsersRUNNER~1AppData…`) and the preload never resolves. The
  // quotes still have to stay for a temp dir under a profile name with spaces,
  // so the separators are doubled rather than the quotes dropped.
  const quoted = `"${dispatch.replace(/\\/g, "\\\\")}"`;
  env.NODE_OPTIONS = `${env.NODE_OPTIONS ? `${env.NODE_OPTIONS} ` : ""}--require ${quoted}`;
  return env;
}
