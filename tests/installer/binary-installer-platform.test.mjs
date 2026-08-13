import assert from "node:assert/strict";
import test from "node:test";

import {
  binaryInstallFilename,
  executableCandidateNames,
  targetPlatform,
} from "../../packages/shared/binary-installer/installer.mjs";

test("standalone binary installer supports Windows targets", () => {
  assert.deepEqual(targetPlatform("win32", "x64"), { os: "win32", arch: "amd64" });
  assert.deepEqual(targetPlatform("win32", "arm64"), { os: "win32", arch: "arm64" });
  assert.equal(binaryInstallFilename("caveman-mcp", "win32"), "caveman-mcp.exe");
  assert.equal(binaryInstallFilename("caveman-mcp", "darwin"), "caveman-mcp");
});

test("Windows PATH lookup respects PATHEXT and existing extensions", () => {
  assert.deepEqual(
    executableCandidateNames("caveman-mcp", "win32", ".EXE;.CMD;.EXE"),
    ["caveman-mcp.EXE", "caveman-mcp.CMD", "caveman-mcp"],
  );
  assert.deepEqual(
    executableCandidateNames("caveman-mcp.exe", "win32", ".EXE;.CMD"),
    ["caveman-mcp.exe"],
  );
});
