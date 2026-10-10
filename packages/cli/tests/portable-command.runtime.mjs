import assert from "node:assert/strict";
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { createRequire } from "node:module";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import test from "node:test";

import { parseWindowsExeShim, parseWindowsNodeShim, portableInvocation } from "../dist/portable-command.js";

test("parses managed Pi's Node command shim", () => {
  assert.equal(parseWindowsNodeShim('@ECHO off\r\nnode "%~dp0pi-launcher.js" %*\r\n'), "pi-launcher.js");
});

test("parses npm and pnpm Node command shims", () => {
  assert.equal(
    parseWindowsNodeShim('endLocal & "%_prog%" "%dp0%\\..\\pkg\\cli.js" %*'),
    "..\\pkg\\cli.js",
  );
  assert.equal(
    parseWindowsNodeShim('node "%~dp0\\..\\pkg\\cli.mjs" %*'),
    "..\\pkg\\cli.mjs",
  );
  // Exact npm cmd-shim@7 payload line (PATHEXT strip + two quoted segments).
  assert.equal(
    parseWindowsNodeShim(
      'endLocal & goto #_undefined_# 2>NUL || title %COMSPEC% & set PATHEXT=%PATHEXT:;.JS;=;% & "%_prog%"  "%dp0%\\..\\pkg\\cli.js" %*\r\n',
    ),
    "..\\pkg\\cli.js",
  );
  // pnpm / yarn-classic (@zkochan/cmd-shim) IF EXIST form — the node.exe quoted
  // prefix must not shadow the .js target.
  assert.equal(
    parseWindowsNodeShim(
      '@SETLOCAL\r\n@IF EXIST "%~dp0\\node.exe" (\r\n  "%~dp0\\node.exe"   "%~dp0\\..\\pkg\\cli.js" %*\r\n) ELSE (\r\n  @SET PATHEXT=%PATHEXT:;.JS;=;%\r\n  node   "%~dp0\\..\\pkg\\cli.js" %*\r\n)\r\n',
    ),
    "..\\pkg\\cli.js",
  );
  // pnpm cross-drive: bin dir and store on different drives makes the target
  // absolute instead of %~dp0-relative.
  assert.equal(
    parseWindowsNodeShim(
      '@SETLOCAL\r\n@IF EXIST "%~dp0\\node.exe" (\r\n  "%~dp0\\node.exe"   "D:\\pnpm-store\\pkg\\cli.js" %*\r\n) ELSE (\r\n  @SET PATHEXT=%PATHEXT:;.JS;=;%\r\n  node   "D:\\pnpm-store\\pkg\\cli.js" %*\r\n)\r\n',
    ),
    "D:\\pnpm-store\\pkg\\cli.js",
  );
  // pnpm pinned-node variant: absolute node interpreter, %~dp0-relative target.
  assert.equal(
    parseWindowsNodeShim('@SETLOCAL\r\n@"C:\\Program Files\\nodejs\\node.exe"  "%~dp0\\..\\pkg\\cli.js" %*\r\n'),
    "..\\pkg\\cli.js",
  );
});

test("Windows Node shim launches target with Node and preserves argument bytes", () => {
  const root = mkdtempSync(join(tmpdir(), "cave-win-shim-"));
  try {
    const target = join(root, "pkg", "cli.js");
    mkdirSync(join(root, "pkg"), { recursive: true });
    writeFileSync(target, "process.exit(0);\n");
    const shim = join(root, "agent.cmd");
    writeFileSync(shim, 'endLocal & "%_prog%" "%dp0%\\pkg\\cli.js" %*\r\n');
    const args = ["--prompt", "100% & literal", 'quote"kept'];
    assert.deepEqual(portableInvocation(shim, args, "win32"), {
      command: process.execPath,
      args: [target, ...args],
    });
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});

test("Pi-style nested Windows shim launches JS without a shell", () => {
  const root = mkdtempSync(join(tmpdir(), "cave nested shim "));
  try {
    const child = join(root, "node_modules", ".bin", "pi.cmd");
    const script = join(root, "pi package", "cli.mjs");
    mkdirSync(join(root, "node_modules", ".bin"), { recursive: true });
    mkdirSync(join(root, "pi package"), { recursive: true });
    writeFileSync(script, "// fixture\n");
    writeFileSync(child, 'node "%~dp0\\..\\..\\pi package\\cli.mjs" %*\r\n');
    const shim = join(root, "pi.CMD");
    writeFileSync(shim, '@ECHO off\r\n"%~dp0node_modules\\.bin\\pi.cmd" %*\r\n');
    const args = ["space & %PATH%", 'quote"kept'];
    assert.deepEqual(portableInvocation(shim, args, "win32"), {
      command: process.execPath, args: [script, ...args],
    });
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});

test("nested shims reject unsafe wrappers, missing targets, cycles and excessive depth", () => {
  const root = mkdtempSync(join(tmpdir(), "cave nested reject "));
  try {
    const shim = join(root, "pi.cmd");
    const child = join(root, "child.bat");
    writeFileSync(join(root, "cli.js"), "// fixture\n");
    const args = ["x&y"];
    const invoke = () => portableInvocation(shim, args, "win32");
    for (const content of [
      '"%~dp0child.bat" %*\r\necho unsafe\r\n',
      '"%~dp0child.bat" %* & echo unsafe\r\n',
      '"%~dp0child.ps1" %*\r\n',
      '"%~dp0child.bat" %*\r\nnode "%~dp0missing.js" %*\r\n',
      '"%~dp0child.bat" %*\r\nnode "%~dp0cli.js" %*\r\n',
    ]) {
      writeFileSync(shim, content);
      assert.throws(invoke, /cannot safely launch non-Node Windows command shim/);
    }
    // A forward to an executable is run directly (next test), so only a
    // missing one is refused; with anything after it, the whole shim is.
    writeFileSync(shim, '"%~dp0child.exe" %*\r\n');
    assert.throws(invoke, /Windows command shim target is missing/);
    writeFileSync(shim, '"%~dp0child.exe" %* & echo unsafe\r\n');
    assert.throws(invoke, /cannot safely launch non-Node Windows command shim/);
    writeFileSync(shim, '"%~dp0child.bat" %*\r\n');
    assert.throws(invoke, /Windows command shim target is missing/);
    writeFileSync(child, '"%~dp0pi.cmd" %*\r\n');
    assert.throws(invoke, /Windows command shim cycle/);
    for (let i = 0; i <= 5; i++) {
      writeFileSync(join(root, `shim${i}.cmd`), `"%~dp0shim${i + 1}.cmd" %*\r\n`);
    }
    assert.throws(() => portableInvocation(join(root, "shim0.cmd"), args, "win32"), /nesting too deep/);
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});

test("non-Node Windows shims fail closed instead of using injectable shell mode", () => {
  const root = mkdtempSync(join(tmpdir(), "cave-win-shim-"));
  try {
    const shim = join(root, "agent.cmd");
    writeFileSync(shim, "@echo off\r\necho %*\r\n");
    assert.throws(
      () => portableInvocation(shim, ["unsafe&arg"], "win32"),
      /cannot safely launch non-Node Windows command shim/,
    );
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});

// npm's shim for a package whose bin is a native executable (Claude Code).
test("a Windows shim that forwards to an .exe beside it runs that executable directly", () => {
  const root = mkdtempSync(join(tmpdir(), "cave-win-exe-shim-"));
  try {
    const exe = join(root, "node_modules", "@anthropic-ai", "claude-code", "bin", "claude.exe");
    mkdirSync(dirname(exe), { recursive: true });
    writeFileSync(exe, "");
    const shim = join(root, "claude.cmd");
    writeFileSync(shim, [
      "@ECHO off", "GOTO start", ":find_dp0", "SET dp0=%~dp0", "EXIT /b", ":start", "SETLOCAL", "CALL :find_dp0",
      '"%dp0%\\node_modules\\@anthropic-ai\\claude-code\\bin\\claude.exe"   %*', "",
    ].join("\r\n"));
    assert.deepEqual(portableInvocation(shim, ["--version", "a&b"], "win32"), { command: exe, args: ["--version", "a&b"] });
    // The target must exist: a shim naming a missing executable is refused.
    rmSync(exe);
    assert.throws(() => portableInvocation(shim, [], "win32"), /shim target is missing/);
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});

// pnpm (@zkochan/cmd-shim) writes a shebang-less .exe bin as `@"<target>" %*`,
// shim-relative, or drive-absolute when the store sits on another drive.
test("pnpm's shim for an .exe bin runs that executable directly", () => {
  const root = mkdtempSync(join(tmpdir(), "cave-win-pnpm-exe-"));
  try {
    const exe = join(root, "global", "5", ".pnpm", "claude-code", "bin", "claude.exe");
    mkdirSync(dirname(exe), { recursive: true });
    writeFileSync(exe, "");
    const shim = join(root, "claude.cmd");
    writeFileSync(shim, '@SETLOCAL\r\n@"%~dp0\\global\\5\\.pnpm\\claude-code\\bin\\claude.exe"   %*\r\n');
    assert.deepEqual(portableInvocation(shim, ["a&b"], "win32"), { command: exe, args: ["a&b"] });
    assert.equal(parseWindowsExeShim('@SETLOCAL\r\n@"D:\\pnpm\\claude.exe"   %*\r\n'), "D:\\pnpm\\claude.exe");
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});

// Node's own npm.cmd and npx.cmd (npm 10.9.7, byte for byte): what every
// Windows Node install puts first on PATH, so `npm`/`npx` resolve to them.
const NPM_SHIM = (name) => [
  ":: Created by npm, please don't edit manually.", "@ECHO OFF", "", "SETLOCAL", "",
  'SET "NODE_EXE=%~dp0\\node.exe"', 'IF NOT EXIST "%NODE_EXE%" (', '  SET "NODE_EXE=node"', ")", "",
  'SET "NPM_PREFIX_JS=%~dp0\\node_modules\\npm\\bin\\npm-prefix.js"',
  `SET "${name.toUpperCase()}_CLI_JS=%~dp0\\node_modules\\npm\\bin\\${name}-cli.js"`,
  `FOR /F "delims=" %%F IN ('CALL "%NODE_EXE%" "%NPM_PREFIX_JS%"') DO (`,
  `  SET "NPM_PREFIX_${name.toUpperCase()}_CLI_JS=%%F\\node_modules\\npm\\bin\\${name}-cli.js"`, ")",
  `IF EXIST "%NPM_PREFIX_${name.toUpperCase()}_CLI_JS%" (`,
  `  SET "${name.toUpperCase()}_CLI_JS=%NPM_PREFIX_${name.toUpperCase()}_CLI_JS%"`, ")", "",
  `"%NODE_EXE%" "%${name.toUpperCase()}_CLI_JS%" %*`, "",
].join("\r\n");

test("Node's own npm.cmd and npx.cmd launch their CLI script under this Node", () => {
  const root = mkdtempSync(join(tmpdir(), "cave nodejs "));
  try {
    for (const name of ["npm", "npx"]) {
      const script = join(root, "node_modules", "npm", "bin", `${name}-cli.js`);
      mkdirSync(dirname(script), { recursive: true });
      writeFileSync(script, "");
      writeFileSync(join(root, `${name}.CMD`), NPM_SHIM(name));
      assert.deepEqual(
        portableInvocation(name, ["install", "-g", "a&b"], "win32", { PATH: root, PATHEXT: ".COM;.EXE;.BAT;.CMD" }),
        { command: process.execPath, args: [script, "install", "-g", "a&b"] },
      );
    }
    // The variable it runs must be the one it set.
    writeFileSync(join(root, "npm.CMD"), NPM_SHIM("npm").replace('"%NODE_EXE%" "%NPM_CLI_JS%" %*', '"%NODE_EXE%" "%NPX_CLI_JS%" %*'));
    assert.throws(() => portableInvocation(join(root, "npm.CMD"), [], "win32"), /cannot safely launch/);
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});

test("native executables and POSIX commands pass through", () => {
  assert.deepEqual(portableInvocation("agent.exe", ["x"], "win32"), {
    command: "agent.exe",
    args: ["x"],
  });
  assert.deepEqual(portableInvocation("agent", ["x"], "darwin"), {
    command: "agent",
    args: ["x"],
  });
});

// The installer (CommonJS, runs before any build), the delegate MCP server
// (copied verbatim into dist/) and the pi extension each launch Windows shims
// too. The first two cannot import this TypeScript, so they keep ported copies;
// every copy must launch the same shims the same way, or one of them is the
// next Windows-only break.
test("every copy of the Windows shim launcher agrees", async () => {
  const require = createRequire(import.meta.url);
  const installer = require("../../../installer/lib/portable-process.js");
  const delegate = await import("../../../agents/delegate/portable-process.mjs");
  const pi = await import("../../pi-extension/src/portable-command.ts");
  const copies = {
    cli: (command, args, env) => portableInvocation(command, args, "win32", env),
    installer: (command, args, env) => installer.portableInvocation(command, args, { platform: "win32", env, execPath: process.execPath }),
    delegate: (command, args, env) => delegate.portableInvocation(command, args, "win32", env),
    pi: (command, args, env) => pi.portableInvocation(command, args, "win32", env),
  };
  const node = (script) => ({ node: script });
  const cases = [
    ["npm cmd-shim", { "a.CMD": 'endLocal & "%_prog%" "%dp0%\\pkg\\cli.js" %*\r\n', "pkg/cli.js": "" }, "a", node("pkg/cli.js")],
    ["managed Pi launcher", { "a.CMD": '@ECHO off\r\nnode "%~dp0pi-launcher.js" %*\r\n', "pi-launcher.js": "" }, "a", node("pi-launcher.js")],
    ["pnpm IF EXIST", { "a.CMD": '@SETLOCAL\r\n@IF EXIST "%~dp0\\node.exe" (\r\n  "%~dp0\\node.exe"   "%~dp0\\pkg\\cli.js" %*\r\n) ELSE (\r\n  node   "%~dp0\\pkg\\cli.js" %*\r\n)\r\n', "pkg/cli.js": "" }, "a", node("pkg/cli.js")],
    ["nested cmd to cmd", { "a.CMD": '@ECHO off\r\n"%~dp0b\\a.cmd" %*\r\n', "b/a.cmd": 'node "%~dp0\\cli.mjs" %*\r\n', "b/cli.mjs": "" }, "a", node("b/cli.mjs")],
    ["stock npm.cmd", { "npm.CMD": NPM_SHIM("npm"), "node_modules/npm/bin/npm-cli.js": "" }, "npm", node("node_modules/npm/bin/npm-cli.js")],
    ["stock npx.cmd", { "npx.CMD": NPM_SHIM("npx"), "node_modules/npm/bin/npx-cli.js": "" }, "npx", node("node_modules/npm/bin/npx-cli.js")],
    ["npm .exe shim", { "a.CMD": '@ECHO off\r\nSETLOCAL\r\n"%dp0%\\bin\\a.exe"   %*\r\n', "bin/a.exe": "MZ" }, "a", { exe: "bin/a.exe" }],
    ["pnpm .exe shim", { "a.CMD": '@SETLOCAL\r\n@"%~dp0\\bin\\a.exe"   %*\r\n', "bin/a.exe": "MZ" }, "a", { exe: "bin/a.exe" }],
    ["Unix shim beside .CMD", { a: "#!/bin/sh\n", "a.CMD": 'node "%~dp0\\cli.js" %*\r\n', "cli.js": "" }, "a", node("cli.js")],
    ["absolute extensionless path", { a: "#!/bin/sh\n", "a.CMD": 'node "%~dp0\\cli.js" %*\r\n', "cli.js": "" }, "/a", node("cli.js")],
    ["non-Node shim", { "a.CMD": "@echo off\r\necho %*\r\n" }, "a", /cannot safely launch/],
    ["command after an .exe forward", { "a.CMD": '"%~dp0bin\\a.exe" %* & echo unsafe\r\n', "bin/a.exe": "MZ" }, "a", /cannot safely launch/],
    ["command after a nested forward", { "a.CMD": '"%~dp0b.bat" %*\r\necho unsafe\r\n', "b.bat": 'node "%~dp0cli.js" %*\r\n', "cli.js": "" }, "a", /cannot safely launch/],
    ["npm.cmd running another variable", { "npm.CMD": NPM_SHIM("npm").replace('"%NPM_CLI_JS%" %*', '"%NPX_CLI_JS%" %*') }, "npm", /cannot safely launch/],
    ["missing .exe", { "a.CMD": '"%~dp0a.exe" %*\r\n' }, "a", /target is missing/],
    ["cycle", { "a.CMD": '"%~dp0b.cmd" %*\r\n', "b.cmd": '"%~dp0a.CMD" %*\r\n' }, "a", /cycle/],
  ];
  for (const [name, files, command, expected] of cases) {
    const root = mkdtempSync(join(tmpdir(), "cave parity "));
    try {
      for (const [file, content] of Object.entries(files)) {
        mkdirSync(dirname(join(root, file)), { recursive: true });
        writeFileSync(join(root, file), content);
      }
      const env = { PATH: root, PATHEXT: ".COM;.EXE;.BAT;.CMD" };
      const target = command.startsWith("/") ? join(root, command.slice(1)) : command;
      const args = ["x&y", "%PATH%", 'quote"kept'];
      for (const [copy, invoke] of Object.entries(copies)) {
        const label = `${copy}: ${name}`;
        if (expected instanceof RegExp) { assert.throws(() => invoke(target, args, env), expected, label); continue; }
        assert.deepEqual(invoke(target, args, env), expected.exe
          ? { command: join(root, expected.exe), args }
          : { command: process.execPath, args: [join(root, expected.node), ...args] }, label);
      }
    } finally {
      rmSync(root, { recursive: true, force: true });
    }
  }
});
