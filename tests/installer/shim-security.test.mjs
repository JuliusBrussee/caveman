import { test } from "node:test";
import assert from "node:assert/strict";
import { mkdtempSync, readFileSync, writeFileSync, mkdirSync } from "node:fs";
import { tmpdir } from "node:os";
import { join, dirname } from "node:path";
import { fileURLToPath } from "node:url";
import { spawnSync } from "node:child_process";

const root = join(dirname(fileURLToPath(import.meta.url)), "..", "..");
const shellShim = readFileSync(join(root, "install.sh"), "utf8");
const powershellShim = readFileSync(join(root, "install.ps1"), "utf8");

test("stdin shell install never executes caller cwd bin/install.js", { skip: process.platform === "win32" }, () => {
  const cwd = mkdtempSync(join(tmpdir(), "caveman-shim-cwd-"));
  mkdirSync(join(cwd, "bin"));
  const marker = join(cwd, "executed");
  writeFileSync(join(cwd, "bin", "install.js"), `require("node:fs").writeFileSync(${JSON.stringify(marker)}, "bad")`);

  const fakeBin = join(cwd, "fake-bin");
  mkdirSync(fakeBin);
  writeFileSync(join(fakeBin, "node"), "#!/bin/sh\nif [ \"$1\" = \"-p\" ]; then echo 24; else exec /usr/bin/env node \"$@\"; fi\n", { mode: 0o755 });
  writeFileSync(join(fakeBin, "npx"), "#!/bin/sh\nprintf '%s\\n' \"$@\"\n", { mode: 0o755 });

  const result = spawnSync("bash", ["-s", "--", "--help"], {
    cwd,
    input: shellShim,
    encoding: "utf8",
    env: { ...process.env, PATH: `${fakeBin}:${process.env.PATH ?? ""}` },
  });
  assert.equal(result.status, 0, result.stderr);
  assert.doesNotMatch(result.stdout, /bad/);
  assert.match(result.stdout, /^-y\ngithub:JuliusBrussee\/caveman#v1\.10\.0\n--help$/m);
  assert.equal(spawnSync("test", ["-e", marker]).status, 1, "caller payload must not execute");
});

test("both public shims pin bootstrap package to immutable release", () => {
  assert.match(shellShim, /github:\$REPO#\$PINNED_REF/);
  assert.match(powershellShim, /github:\$Repo#\$PinnedRef/);
  assert.doesNotMatch(shellShim, /caveman\/main\/install\.sh/);
  assert.doesNotMatch(powershellShim, /caveman\/main\/install\.ps1/);
});
