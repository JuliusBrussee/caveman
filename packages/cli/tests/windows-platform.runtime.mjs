import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { mkdtempSync, mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";

import { stubEnv } from "./harness/index.mjs";
import {
  binaryInstallFilename,
  commandHasPath,
  executableCandidateNames,
  generatedPluginInvocation,
  hookExecutableInvocation,
  nativeHookInvocation,
  normalizeHookPath,
  quoteHookPath,
  setupPlatform,
} from "../dist/index.js";

test("CLI setup accepts Windows x64 and arm64", () => {
  assert.deepEqual(setupPlatform("win32", "x64"), { os: "win32", arch: "amd64" });
  assert.deepEqual(setupPlatform("win32", "arm64"), { os: "win32", arch: "arm64" });
  assert.equal(binaryInstallFilename("caveman-proxy", "win32"), "caveman-proxy.exe");
});

test("CLI recognizes Windows paths and PATHEXT without double extensions", () => {
  assert.equal(commandHasPath("C:\\Users\\cave\\caveman-proxy.exe"), true);
  assert.equal(commandHasPath(".\\bin\\caveman-proxy.exe"), true);
  assert.equal(commandHasPath("caveman-proxy"), false);
  assert.deepEqual(
    executableCandidateNames("caveman-proxy", "win32", ".EXE;.CMD;.EXE"),
    ["caveman-proxy.EXE", "caveman-proxy.CMD", "caveman-proxy"],
  );
  assert.deepEqual(
    executableCandidateNames("caveman-proxy.exe", "win32", ".EXE;.CMD"),
    ["caveman-proxy.exe"],
  );
});

test("CLI quotes Windows hook paths for PowerShell", () => {
  assert.equal(
    normalizeHookPath("C:\\Users\\cave\\AppData\\Roaming\\npm\\caveman.cmd", "win32"),
    "C:/Users/cave/AppData/Roaming/npm/caveman.cmd",
  );
  assert.equal(
    quoteHookPath("C:\\Users\\Jane Doe\\AppData\\Roaming\\npm\\caveman.cmd", "win32"),
    "'C:/Users/Jane Doe/AppData/Roaming/npm/caveman.cmd'",
  );
  assert.equal(
    quoteHookPath("C:\\Users\\Jane $mith\\it's `cave`\\caveman.cmd", "win32"),
    "'C:/Users/Jane $mith/it''s `cave`/caveman.cmd'",
  );
  assert.equal(normalizeHookPath("/usr/local/bin/caveman", "linux"), "/usr/local/bin/caveman");
});

test("CLI normalizes every path in Windows lifecycle hook commands", () => {
  assert.equal(
    nativeHookInvocation(
      "C:\\Users\\Jane Doe\\.caveman\\bin\\caveman-proxy.exe",
      "C:\\Program Files\\Caveman\\native-hook-fast.js",
      "claude",
      true,
      "win32",
    ),
    "& 'C:/Users/Jane Doe/.caveman/bin/caveman-proxy.exe' native-hook claude --adapter 'C:/Program Files/Caveman/native-hook-fast.js'",
  );
  assert.equal(
    nativeHookInvocation(
      "C:\\Program Files\\nodejs\\node.exe",
      "C:\\Program Files\\Caveman\\native-hook-fast.js",
      "claude",
      false,
      "win32",
    ),
    "& 'C:/Program Files/nodejs/node.exe' 'C:/Program Files/Caveman/native-hook-fast.js' native-hook claude",
  );
});

test("every Windows hook executable prefix uses PowerShell invocation", () => {
  assert.equal(
    hookExecutableInvocation("C:\\Users\\Jane Doe\\AppData\\Roaming\\npm\\caveman.CMD", undefined, "win32", false),
    "'C:/Users/Jane Doe/AppData/Roaming/npm/caveman.CMD'",
  );
  assert.equal(
    hookExecutableInvocation("C:\\Users\\Jane Doe\\AppData\\Roaming\\npm\\caveman.CMD", undefined, "win32"),
    "& 'C:/Users/Jane Doe/AppData/Roaming/npm/caveman.CMD'",
  );
  assert.equal(
    hookExecutableInvocation("/usr/local/bin/caveman", undefined, "darwin"),
    "'/usr/local/bin/caveman'",
  );
});

test("generated plugins bake Node target instead of a Windows CMD shim", () => {
  const root = mkdtempSync(join(tmpdir(), "caveman-plugin-shim-"));
  const packageDir = join(root, "node_modules", "@caveman-ai", "cli");
  mkdirSync(join(packageDir, "dist"), { recursive: true });
  const target = join(packageDir, "dist", "index.js");
  writeFileSync(target, "", "utf8");
  const shim = join(root, "caveman.CMD");
  writeFileSync(shim, '@IF EXIST "%~dp0\\node.exe" (\r\n  "%~dp0\\node.exe"  "%~dp0\\node_modules\\@caveman-ai\\cli\\dist\\index.js" %*\r\n) ELSE (\r\n  node  "%~dp0\\node_modules\\@caveman-ai\\cli\\dist\\index.js" %*\r\n)\r\n', "utf8");

  const invocation = generatedPluginInvocation(shim, "ignored.js", "win32");
  assert.equal(invocation.cmd, process.execPath);
  assert.deepEqual(invocation.pre, [target]);
});

test("stub preload path survives Node's own NODE_OPTIONS parsing", { skip: process.platform !== "win32" }, () => {
  // Node unescapes backslashes inside a quoted NODE_OPTIONS value, so an
  // unescaped Windows path reaches --require as `C:UsersRUNNER~1AppData…` and
  // every stub-backed suite dies in the preload with MODULE_NOT_FOUND before
  // the CLI under test runs a line. The space in the directory is why the
  // quotes cannot simply be dropped.
  const dir = join(mkdtempSync(join(tmpdir(), "caveman-stub-env-")), "Program Files", "bin");
  const env = stubEnv({ ...process.env }, dir);
  const result = spawnSync(process.execPath, ["-e", "process.stdout.write('ok')"], { env, encoding: "utf8" });
  assert.equal(result.status, 0, result.stderr);
  assert.equal(result.stdout, "ok");
});

test("native PowerShell executes generated lifecycle hook commands", { skip: process.platform !== "win32" }, () => {
  const root = mkdtempSync(join(tmpdir(), "caveman-hook-smoke-"));
  const fixtureDir = join(root, "O'Brien space");
  mkdirSync(fixtureDir);
  const fixture = join(fixtureDir, "hook.js");
  const marker = join(root, "args.json");
  writeFileSync(fixture, 'require("node:fs").writeFileSync(process.env.CAVEMAN_HOOK_MARKER, JSON.stringify(process.argv.slice(2)));\n', "utf8");
  const command = nativeHookInvocation(process.execPath, fixture, "claude", false, "win32");
  const result = spawnSync("powershell.exe", ["-NoProfile", "-Command", command], {
    env: { ...process.env, CAVEMAN_HOOK_MARKER: marker },
    encoding: "utf8",
    windowsHide: true,
  });
  assert.equal(result.status, 0, result.stderr);
  assert.deepEqual(JSON.parse(readFileSync(marker, "utf8")), ["native-hook", "claude"]);
});
