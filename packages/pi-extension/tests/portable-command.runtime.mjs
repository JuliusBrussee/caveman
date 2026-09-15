// The Windows hook-invocation contract, asserted from any platform.
//
// Every pi-extension hook call went through execFile("caveman", …). On Windows
// that name resolves to a `.cmd` shim — or worse, to the NON-EXECUTABLE Unix
// shim npm/pnpm parks beside it under the bare name — and execFile can run
// neither, dying with `spawn EFTYPE`. engine-ci's windows job caught it only
// after the package shipped; these cases catch it on a laptop.
//
// portableInvocation takes an explicit platform/env, so the win32 behaviour is
// exercised on macOS and Linux too.

import assert from "node:assert/strict";
import { mkdtempSync, mkdirSync, writeFileSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";

const { portableInvocation, parseWindowsNodeShim } = await import("../dist/portable-command.mjs")
  .catch(() => import("../src/portable-command.ts"));

function fixture() {
  const root = mkdtempSync(join(tmpdir(), "pi-portable-"));
  const bin = join(root, "bin");
  mkdirSync(bin, { recursive: true });
  return { root, bin };
}

test("non-win32 is an untouched pass-through", () => {
  const r = portableInvocation("caveman", ["native-hook", "pi", "SessionStart"], "darwin", {});
  assert.deepEqual(r, { command: "caveman", args: ["native-hook", "pi", "SessionStart"] });
});

test("win32: a bare name resolves through PATHEXT, not to the Unix shim beside it", () => {
  const { root, bin } = fixture();
  try {
    // Exactly the npm/pnpm .bin layout: an extensionless Unix shim next to the
    // real .CMD. Picking the bare name is what produced spawn EFTYPE.
    writeFileSync(join(bin, "caveman"), "#!/bin/sh\nexec node caveman.js\n");
    // Named .CMD, not .cmd: PATHEXT entries are uppercase and that is exactly
    // what resolveWindowsCommand probes for. Windows and macOS are
    // case-insensitive so either spelling passes there, but on a case-sensitive
    // filesystem (the Linux runner) a lowercase fixture is simply never found —
    // the test would silently stop exercising the .cmd branch.
    // Verbatim npm cmd-shim shape — the `node.exe` token and the `%~dp0`
    // relative target are both load-bearing for the shim parser.
    writeFileSync(
      join(bin, "caveman.CMD"),
      '@IF EXIST "%~dp0\\node.exe" (\r\n'
      + '  "%~dp0\\node.exe"  "%~dp0\\cli\\dist\\index.js" %*\r\n'
      + ') ELSE (\r\n'
      + '  node  "%~dp0\\cli\\dist\\index.js" %*\r\n'
      + ')\r\n',
    );
    mkdirSync(join(bin, "cli", "dist"), { recursive: true });
    writeFileSync(join(bin, "cli", "dist", "index.js"), "//\n");

    const r = portableInvocation("caveman", ["native-hook", "pi", "SessionStart"], "win32", {
      PATH: bin,
      PATHEXT: ".COM;.EXE;.BAT;.CMD",
    });
    assert.equal(r.command, process.execPath, "a .cmd Node shim must launch through node, not execFile");
    assert.equal(r.args[0], join(bin, "cli", "dist", "index.js"));
    assert.deepEqual(r.args.slice(1), ["native-hook", "pi", "SessionStart"]);
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});

test("win32: a real .exe is invoked directly", () => {
  const { root, bin } = fixture();
  try {
    writeFileSync(join(bin, "caveman.EXE"), "MZ");
    const r = portableInvocation("caveman", ["native-hook", "pi", "Stop"], "win32", {
      PATH: bin,
      PATHEXT: ".COM;.EXE;.BAT;.CMD",
    });
    // Exact, not case-folded: the fixture is named for what PATHEXT probes.
    assert.equal(r.command, join(bin, "caveman.EXE"));
    assert.deepEqual(r.args, ["native-hook", "pi", "Stop"]);
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});

test("win32: a shim whose target cannot be parsed throws rather than spawning garbage", () => {
  const { root, bin } = fixture();
  try {
    writeFileSync(join(bin, "caveman.CMD"), "@echo off\r\necho not a node shim\r\n");
    assert.throws(
      () => portableInvocation("caveman", [], "win32", { PATH: bin, PATHEXT: ".CMD" }),
      /non-Node Windows command shim/,
    );
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});

// The CLI harness writes the .cmd that every stubAgent-spawned test launches on
// Windows. It used to emit `node "%~dp0claude.mjs" %*` — no backslash — which the
// parser below does not recognize, so portableInvocation threw "non-Node Windows
// command shim" and took the whole CLI suite down on Windows. Asserted against
// the REAL parser the CLI ships, not a copy of the regex.
test("the CLI harness .cmd shim is parseable by the shipped parser", async () => {
  const { portableInvocation: cliInvocation } = await import("../../cli/src/portable-command.ts");
  const { stubAgent } = await import("../../cli/tests/harness/stub-agent.mjs");
  const { root, bin } = fixture();
  try {
    // platform:"win32" explicitly, so the Windows shim is exercised from macOS
    // and Linux too — this bug shipped precisely because nothing did.
    const agent = stubAgent({ dir: bin, platform: "win32" });
    const r = cliInvocation(agent.path, ["-p", "hi"], "win32", {});
    assert.equal(r.command, process.execPath, "the .cmd must launch through node");
    assert.equal(r.args[0], join(bin, "claude.mjs"));
    assert.deepEqual(r.args.slice(1), ["-p", "hi"]);
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});

test("win32: an absolute extensionless path still resolves through PATHEXT", () => {
  const { root, bin } = fixture();
  try {
    // ~/.caveman/bin/caveman — the hook bridge's third candidate. The
    // extensionless file exists beside the real .CMD (same npm/pnpm layout as
    // above), and returning it verbatim is the EFTYPE this file exists to stop.
    writeFileSync(join(bin, "caveman"), "#!/bin/sh\nexec node caveman.js\n");
    writeFileSync(join(bin, "caveman.CMD"), '@node  "%~dp0\\cli.js" %*\r\n');
    writeFileSync(join(bin, "cli.js"), "//\n");
    const r = portableInvocation(join(bin, "caveman"), ["native-hook", "pi", "Stop"], "win32", { PATHEXT: ".COM;.EXE;.BAT;.CMD" });
    assert.equal(r.command, process.execPath);
    assert.equal(r.args[0], join(bin, "cli.js"));
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});

test("parseWindowsNodeShim reads both the shim-relative and drive-absolute forms", () => {
  assert.equal(
    parseWindowsNodeShim('  "%~dp0\\node.exe"  "%~dp0\\..\\caveman\\dist\\index.js" %*'),
    "..\\caveman\\dist\\index.js",
  );
  // pnpm emits a drive-absolute target when the global bin dir and the store
  // sit on different drives.
  assert.equal(
    parseWindowsNodeShim('@node  "C:\\store\\caveman\\dist\\index.js" %*'),
    "C:\\store\\caveman\\dist\\index.js",
  );
  assert.equal(parseWindowsNodeShim("@echo off\r\necho hi\r\n"), null);
});
