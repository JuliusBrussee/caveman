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

  // Pin the ref via the shim's own CAVEMAN_REF override so this test checks
  // the pass-through shape, not whichever release the shim currently pins.
  const result = spawnSync("bash", ["-s", "--", "--help"], {
    cwd,
    input: shellShim,
    encoding: "utf8",
    env: { ...process.env, PATH: `${fakeBin}:${process.env.PATH ?? ""}`, CAVEMAN_REF: "v3.4.5" },
  });
  assert.equal(result.status, 0, result.stderr);
  assert.doesNotMatch(result.stdout, /bad/);
  assert.match(result.stdout, /^-y\ngithub:JuliusBrussee\/caveman#v3\.4\.5\n--help$/m);
  assert.equal(spawnSync("test", ["-e", marker]).status, 1, "caller payload must not execute");
});

test("both public shims pin bootstrap package to immutable release", () => {
  assert.match(shellShim, /github:\$REPO#\$PINNED_REF/);
  assert.match(powershellShim, /github:\$Repo#\$PinnedRef/);
  assert.doesNotMatch(shellShim, /caveman\/main\/install\.sh/);
  assert.doesNotMatch(powershellShim, /caveman\/main\/install\.ps1/);
});

test("every bootstrap pin names the same release", () => {
  // v2.3.0 shipped with install.sh and install.ps1 still pinned to v2.2.0:
  // bumping bin/install.js and the README one-liners is not enough, because
  // each shim carries its own default ref and the curl-pipe path uses that
  // one. Keep the four pins (plus the installer package version) in lockstep.
  const pins = {
    "install.sh": shellShim.match(/^PINNED_REF="\$\{CAVEMAN_REF:-(v[^}"]+)\}"$/m)?.[1],
    "install.ps1": powershellShim.match(/\$PinnedRef = if \(\$env:CAVEMAN_REF\) \{ \$env:CAVEMAN_REF \} else \{ "(v[^"]+)" \}/)?.[1],
    "bin/install.js": readFileSync(join(root, "bin", "install.js"), "utf8")
      .match(/^const PINNED_REF = process\.env\.CAVEMAN_REF \|\| '(v[^']+)';$/m)?.[1],
  };
  for (const [file, pin] of Object.entries(pins)) {
    assert.ok(pin, `${file} must declare a parseable pinned ref`);
  }
  assert.equal(new Set(Object.values(pins)).size, 1, `bootstrap pins disagree: ${JSON.stringify(pins)}`);

  const version = JSON.parse(readFileSync(join(root, "package.json"), "utf8")).version;
  assert.equal(`v${version}`, pins["install.sh"], "installer package version must match the pinned release");

  for (const doc of ["README.md", "INSTALL.md"]) {
    const text = readFileSync(join(root, doc), "utf8");
    const refs = [...text.matchAll(/raw\.githubusercontent\.com\/JuliusBrussee\/caveman\/(v[\d.]+)\//g)].map((m) => m[1]);
    assert.ok(refs.length > 0, `${doc} must carry at least one pinned install one-liner`);
    for (const ref of refs) assert.equal(ref, pins["install.sh"], `${doc} one-liner pins ${ref}`);
  }
});
