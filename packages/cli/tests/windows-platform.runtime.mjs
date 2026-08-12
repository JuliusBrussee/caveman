import assert from "node:assert/strict";
import test from "node:test";

import {
  binaryInstallFilename,
  commandHasPath,
  executableCandidateNames,
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
