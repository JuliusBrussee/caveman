import assert from "node:assert/strict";
import test from "node:test";

import {
  binaryInstallFilename,
  commandHasPath,
  executableCandidateNames,
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
    ["caveman-proxy", "caveman-proxy.EXE", "caveman-proxy.CMD"],
  );
  assert.deepEqual(
    executableCandidateNames("caveman-proxy.exe", "win32", ".EXE;.CMD"),
    ["caveman-proxy.exe"],
  );
});

test("CLI quotes Windows hook paths and emits POSIX separators", () => {
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
    "'C:/Users/Jane $mith/it'\"'\"'s `cave`/caveman.cmd'",
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
    "'C:/Users/Jane Doe/.caveman/bin/caveman-proxy.exe' native-hook claude --adapter 'C:/Program Files/Caveman/native-hook-fast.js'",
  );
  assert.equal(
    nativeHookInvocation(
      "C:\\Program Files\\nodejs\\node.exe",
      "C:\\Program Files\\Caveman\\native-hook-fast.js",
      "claude",
      false,
      "win32",
    ),
    "'C:/Program Files/nodejs/node.exe' 'C:/Program Files/Caveman/native-hook-fast.js' native-hook claude",
  );
});
