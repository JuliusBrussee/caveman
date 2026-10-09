import { test } from "node:test";
import assert from "node:assert/strict";
import { mkdtempSync, readFileSync, rmSync, writeFileSync, mkdirSync } from "node:fs";
import { tmpdir } from "node:os";
import { join, dirname } from "node:path";
import { fileURLToPath } from "node:url";
import { spawnSync } from "node:child_process";

const root = join(dirname(fileURLToPath(import.meta.url)), "..", "..");
const shellShim = readFileSync(join(root, "install.sh"), "utf8");
const powershellShim = readFileSync(join(root, "install.ps1"), "utf8");

test("stdin shell install never executes caller cwd installer/install.js", { skip: process.platform === "win32" }, () => {
  const cwd = mkdtempSync(join(tmpdir(), "caveman-shim-cwd-"));
  mkdirSync(join(cwd, "installer"));
  const marker = join(cwd, "executed");
  writeFileSync(join(cwd, "installer", "install.js"), `require("node:fs").writeFileSync(${JSON.stringify(marker)}, "bad")`);

  const fakeBin = join(cwd, "fake-bin");
  mkdirSync(fakeBin);
  writeFileSync(join(fakeBin, "node"), `#!/bin/sh\nif [ "$1" = "-p" ]; then echo 24; else exec ${JSON.stringify(process.execPath)} "$@"; fi\n`, { mode: 0o755 });
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
  // bumping installer/install.js and the README one-liners is not enough, because
  // each shim carries its own default ref and the curl-pipe path uses that
  // one. Keep the four pins (plus the installer package version) in lockstep.
  const pins = {
    "install.sh": shellShim.match(/^PINNED_REF="\$\{CAVEMAN_REF:-(v[^}"]+)\}"$/m)?.[1],
    "install.ps1": powershellShim.match(/\$PinnedRef = if \(\$env:CAVEMAN_REF\) \{ \$env:CAVEMAN_REF \} else \{ "(v[^"]+)" \}/)?.[1],
    "installer/install.js": readFileSync(join(root, "installer", "install.js"), "utf8")
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

// With no caveman on PATH the first run comes from npx; it must be the CLI
// this repo releases, never whatever @latest is that day.
test("both shims pin the first-run CLI to packages/cli/package.json", () => {
  const cli = JSON.parse(readFileSync(join(root, "packages", "cli", "package.json"), "utf8")).version;
  assert.equal(shellShim.match(/^CLI_VERSION="([^"]+)"$/m)?.[1], cli, "install.sh CLI_VERSION drifted from packages/cli/package.json");
  assert.equal(powershellShim.match(/\$CliVersion = "([^"]+)"/)?.[1], cli, "install.ps1 $CliVersion drifted from packages/cli/package.json");
  assert.doesNotMatch(shellShim + powershellShim, /@caveman-ai\/cli@latest/);
});

// The install ends in the CLI's first run. Without a terminal (CI, a pipe) the
// shim prints the one command instead of running it; flags like --help never
// lead into it.
test("shell install ends by naming the first-run command when no terminal is attached", { skip: process.platform === "win32" }, () => {
  const cwd = mkdtempSync(join(tmpdir(), "caveman-shim-first-run-"));
  const fakeBin = join(cwd, "fake-bin");
  mkdirSync(fakeBin);
  writeFileSync(join(fakeBin, "node"), `#!/bin/sh\nif [ "$1" = "-p" ]; then echo 24; else exec ${JSON.stringify(process.execPath)} "$@"; fi\n`, { mode: 0o755 });
  writeFileSync(join(fakeBin, "npx"), "#!/bin/sh\necho installer-ran\n", { mode: 0o755 });
  writeFileSync(join(fakeBin, "caveman"), "#!/bin/sh\necho caveman-ran \"$@\"\n", { mode: 0o755 });
  const run = (args) => spawnSync("bash", ["-s", "--", ...args], {
    cwd,
    input: shellShim,
    encoding: "utf8",
    env: { ...process.env, PATH: `${fakeBin}:${process.env.PATH ?? ""}` },
  });
  const plain = run([]);
  assert.equal(plain.status, 0, plain.stderr);
  assert.equal(plain.stdout, "installer-ran\nNext: caveman setup\n");
  const help = run(["--help"]);
  assert.equal(help.stdout, "installer-ran\n");
  // No caveman on PATH (and nothing from the host's PATH): the pinned CLI through npx.
  rmSync(join(fakeBin, "caveman"));
  writeFileSync(join(fakeBin, "node"), `#!/bin/sh\nif [ "$1" = "-p" ]; then echo 24; else exec ${JSON.stringify(process.execPath)} "$@"; fi\n`, { mode: 0o755 });
  const viaNpx = spawnSync("bash", ["-s", "--"], {
    cwd,
    input: shellShim,
    encoding: "utf8",
    env: { ...process.env, PATH: `${fakeBin}:/usr/bin:/bin` },
  });
  const cli = JSON.parse(readFileSync(join(root, "packages", "cli", "package.json"), "utf8")).version;
  assert.equal(viaNpx.stdout, `installer-ran\nNext: npx -y @caveman-ai/cli@${cli} setup\n`);
});

// The skills installer runs on Node 18, the CLI needs 22.13: on a Node in
// between, the shim says so instead of handing over to a first run that fails.
test("shell install on a Node older than the CLI's floor names the upgrade, not a first run that cannot start", { skip: process.platform === "win32" }, () => {
  const cwd = mkdtempSync(join(tmpdir(), "caveman-shim-old-node-"));
  const fakeBin = join(cwd, "fake-bin");
  mkdirSync(fakeBin);
  writeFileSync(join(fakeBin, "node"), "#!/bin/sh\ncase \"$1\" in -p) echo 20 ;; -e) exit 1 ;; --version) echo v20.11.0 ;; esac\n", { mode: 0o755 });
  writeFileSync(join(fakeBin, "npx"), "#!/bin/sh\necho installer-ran\n", { mode: 0o755 });
  const out = spawnSync("bash", ["-s", "--"], { cwd, input: shellShim, encoding: "utf8", env: { ...process.env, PATH: `${fakeBin}:/usr/bin:/bin` } });
  const cli = JSON.parse(readFileSync(join(root, "packages", "cli", "package.json"), "utf8")).version;
  assert.equal(out.status, 0, out.stderr);
  assert.equal(out.stdout, `installer-ran\n\ncaveman: skills installed. The runtime (smaller inputs, Auto routing) needs Node 22.13+; this is v20.11.0.\n  Upgrade Node (https://nodejs.org), then run: npx -y @caveman-ai/cli@${cli}\n`);
});
