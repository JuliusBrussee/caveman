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

// `npx github:JuliusBrussee/caveman -- --uninstall` puts this package's own CLI
// dependency first on PATH, so that CLI runs `caveman disable --all`. One from
// an older major cannot undo what the release wrote: 1.x left Claude Code on
// the caveman-auto model with its route gone. The floor may trail the CLI
// version: the repo is tagged before the CLI is published, and a range nothing
// satisfies yet would break the installer in between. So publish a new CLI
// major before the tag that bumps this range to it.
test("installer package depends on the CLI major this repo releases", () => {
  const cli = JSON.parse(readFileSync(join(root, "packages", "cli", "package.json"), "utf8")).version;
  const range = JSON.parse(readFileSync(join(root, "package.json"), "utf8")).dependencies["@caveman-ai/cli"];
  const floor = /^\^(\d+)\.(\d+)\.(\d+)$/.exec(range)?.slice(1).map(Number);
  const want = cli.split(".").map(Number);
  assert.ok(floor, `package.json @caveman-ai/cli must be a ^x.y.z range, got ${range}`);
  assert.equal(floor[0], want[0], `package.json @caveman-ai/cli ${range} is not the packages/cli ${cli} major`);
  assert.ok(floor[1] < want[1] || (floor[1] === want[1] && floor[2] <= want[2]), `package.json @caveman-ai/cli ${range} is above packages/cli ${cli}`);
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
  const cli = JSON.parse(readFileSync(join(root, "packages", "cli", "package.json"), "utf8")).version;
  // `caveman --version` prints JSON, as the CLI does.
  const caveman = (version) => writeFileSync(join(fakeBin, "caveman"), `#!/bin/sh\nprintf '{\\n  "version": "%s"\\n}\\n' ${version}\n`, { mode: 0o755 });
  caveman(cli);
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
  // An older CLI on PATH has an older setup: the pinned one runs instead.
  caveman("0.0.1");
  assert.equal(run([]).stdout, `installer-ran\nNext: npx -y @caveman-ai/cli@${cli} setup\n`);
  // No caveman on PATH (and nothing from the host's PATH): the pinned CLI through npx.
  rmSync(join(fakeBin, "caveman"));
  writeFileSync(join(fakeBin, "node"), `#!/bin/sh\nif [ "$1" = "-p" ]; then echo 24; else exec ${JSON.stringify(process.execPath)} "$@"; fi\n`, { mode: 0o755 });
  const viaNpx = spawnSync("bash", ["-s", "--"], {
    cwd,
    input: shellShim,
    encoding: "utf8",
    env: { ...process.env, PATH: `${fakeBin}:/usr/bin:/bin` },
  });
  assert.equal(viaNpx.stdout, `installer-ran\nNext: npx -y @caveman-ai/cli@${cli} setup\n`);
});

// npx would install its pinned CLI over a newer one on PATH, so the caveman on
// PATH takes the first run when it is this release or newer. A prerelease ranks
// below its own release; a version that cannot be read counts as older.
test("shell install hands the first run to a caveman on PATH that is this release or newer", { skip: process.platform === "win32" }, () => {
  const cwd = mkdtempSync(join(tmpdir(), "caveman-shim-path-cli-"));
  const fakeBin = join(cwd, "fake-bin");
  mkdirSync(fakeBin);
  writeFileSync(join(fakeBin, "node"), `#!/bin/sh\nif [ "$1" = "-p" ]; then echo 24; else exec ${JSON.stringify(process.execPath)} "$@"; fi\n`, { mode: 0o755 });
  writeFileSync(join(fakeBin, "npx"), "#!/bin/sh\necho installer-ran\n", { mode: 0o755 });
  const cli = JSON.parse(readFileSync(join(root, "packages", "cli", "package.json"), "utf8")).version;
  const [major, minor, patch] = cli.split(".").map(Number);
  const run = (body) => {
    writeFileSync(join(fakeBin, "caveman"), `#!/bin/sh\n${body}\n`, { mode: 0o755 });
    return spawnSync("bash", ["-s", "--"], { cwd, input: shellShim, encoding: "utf8", timeout: 55_000, env: { ...process.env, PATH: `${fakeBin}:/usr/bin:/bin` } });
  };
  const viaPath = "installer-ran\nNext: caveman setup\n";
  const viaNpx = `installer-ran\nNext: npx -y @caveman-ai/cli@${cli} setup\n`;
  for (const [version, want] of [
    [cli, viaPath],
    [`${major}.${minor}.${patch + 1}`, viaPath],
    [`${major + 1}.0.0`, viaPath],
    [`${major}.${minor + 1}.0-beta.1`, viaPath],
    [`${cli}-rc.1`, viaNpx],
    [`${major - 1}.99.99`, viaNpx],
    ["0.0.1", viaNpx],
    ["banana", viaNpx],
  ]) {
    assert.equal(run(`printf '%s\\n' '${JSON.stringify({ version })}'`).stdout, want, version);
  }
  assert.equal(run("echo not json").stdout, viaNpx);
  // A `caveman --version` that never answers is given up on after 10s.
  const hung = run("exec sleep 120");
  assert.equal(hung.stdout, viaNpx, hung.error?.message);
});

// Both shims ask the caveman on PATH the same question in the same words.
test("both shims carry the same PATH CLI probe", () => {
  const sh = shellShim.match(/node -e '([^']+)' "\$CLI_VERSION"/)?.[1];
  const ps1 = powershellShim.match(/& node -e '([^']+)' \$CliVersion/)?.[1];
  assert.ok(sh, "install.sh has no PATH CLI probe");
  assert.equal(ps1, sh, "install.ps1 probes the PATH CLI differently from install.sh");
  // PowerShell 5.1 drops a double quote inside an argument to a native command (#249).
  assert.doesNotMatch(sh, /"/);
  assert.match(sh, /timeout:1e4/);
});

// Two prereleases of one release rank by semver precedence: a PATH CLI at
// 2.1.0-beta.1 is older than a pinned 2.1.0-beta.2, so npx runs the pinned one.
test("the PATH CLI probe orders two prereleases of one release by semver", { skip: process.platform === "win32" }, () => {
  const probe = shellShim.match(/node -e '([^']+)' "\$CLI_VERSION"/)?.[1];
  const fakeBin = mkdtempSync(join(tmpdir(), "caveman-shim-prerelease-"));
  try {
    const keepsPath = (have, want) => {
      writeFileSync(join(fakeBin, "caveman"), `#!/bin/sh\nprintf '%s\\n' '${JSON.stringify({ version: have })}'\n`, { mode: 0o755 });
      return spawnSync(process.execPath, ["-e", probe, want], { env: { ...process.env, PATH: `${fakeBin}:/usr/bin:/bin` } }).status === 0;
    };
    for (const [have, want, keep] of [
      ["2.1.0-beta.1", "2.1.0-beta.2", false],
      ["2.1.0-beta.2", "2.1.0-beta.1", true],
      ["2.1.0-beta.10", "2.1.0-beta.2", true],
      ["2.1.0-alpha", "2.1.0-alpha.1", false],
      ["2.1.0-1", "2.1.0-alpha", false],
      ["2.1.0-rc.1", "2.1.0-rc.1", true],
      ["2.1.0-rc.1", "2.1.0", false],
      ["2.1.0", "2.1.0-rc.1", true],
    ]) assert.equal(keepsPath(have, want), keep, `${have} on PATH, ${want} pinned`);
  } finally {
    rmSync(fakeBin, { recursive: true, force: true });
  }
});

// With a terminal the shim starts the first run, unless --non-interactive
// ("never prompt") asked it not to: then it names the command.
const python = spawnSync("sh", ["-c", "command -v python3"], { encoding: "utf8" }).stdout.trim();
test("shell install with --non-interactive names the first run instead of starting it, even in a terminal", { skip: process.platform === "win32" || !python }, () => {
  const cwd = mkdtempSync(join(tmpdir(), "caveman-shim-tty-"));
  const fakeBin = join(cwd, "fake-bin");
  mkdirSync(fakeBin);
  writeFileSync(join(fakeBin, "node"), `#!/bin/sh\nif [ "$1" = "-p" ]; then echo 24; else exec ${JSON.stringify(process.execPath)} "$@"; fi\n`, { mode: 0o755 });
  writeFileSync(join(fakeBin, "npx"), `#!/bin/sh\necho "$*" >> '${join(cwd, "npx.log")}'\n`, { mode: 0o755 });
  // Outside a clone, so the shim takes the npx path.
  writeFileSync(join(cwd, "install.sh"), shellShim);
  const cli = JSON.parse(readFileSync(join(root, "packages", "cli", "package.json"), "utf8")).version;
  // Under a pseudo-terminal, the way a person runs it.
  const run = (args) => {
    rmSync(join(cwd, "npx.log"), { force: true });
    const out = spawnSync(python, ["-c", "import pty,sys; sys.exit(pty.spawn(sys.argv[1:]) >> 8)", "bash", join(cwd, "install.sh"), ...args], {
      cwd, input: "", encoding: "utf8", env: { HOME: cwd, PATH: `${fakeBin}:/usr/bin:/bin`, TERM: "xterm" },
    });
    return { ...out, npx: readFileSync(join(cwd, "npx.log"), "utf8") };
  };
  assert.match(run([]).npx, new RegExp(`^-y @caveman-ai/cli@${cli.replaceAll(".", "\\.")} setup$`, "m"), "a terminal starts the first run");
  const quiet = run(["--non-interactive"]);
  assert.equal(quiet.status, 0, quiet.stdout);
  assert.doesNotMatch(quiet.npx, /setup/);
  assert.match(quiet.stdout, new RegExp(`Next: npx -y @caveman-ai/cli@${cli.replaceAll(".", "\\.")} setup`));
});

// The skills installer runs on Node 18, the CLI needs 22.13: on a Node in
// between, the shim says so instead of handing over to a first run that fails.
test("shell install on a Node older than the CLI's floor names the upgrade, not a first run that cannot start", { skip: process.platform === "win32" }, () => {
  const cli = JSON.parse(readFileSync(join(root, "packages", "cli", "package.json"), "utf8")).version;
  const run = (version) => {
    const cwd = mkdtempSync(join(tmpdir(), "caveman-shim-old-node-"));
    const fakeBin = join(cwd, "fake-bin");
    mkdirSync(fakeBin);
    // `node -p` answers the major to the shim's first question, the whole version to its second.
    writeFileSync(join(fakeBin, "node"), `#!/bin/sh\ncase "$2" in *split*) echo ${version.split(".")[0]} ;; *) echo ${version} ;; esac\n`, { mode: 0o755 });
    writeFileSync(join(fakeBin, "npx"), "#!/bin/sh\necho installer-ran\n", { mode: 0o755 });
    return spawnSync("bash", ["-s", "--"], { cwd, input: shellShim, encoding: "utf8", env: { ...process.env, PATH: `${fakeBin}:/usr/bin:/bin` } });
  };
  for (const old of ["20.11.0", "22.12.0"]) {
    const out = run(old);
    assert.equal(out.status, 0, out.stderr);
    assert.equal(out.stdout, `installer-ran\n\ncaveman: skills installed. The runtime (smaller inputs, Auto routing) needs Node 22.13+; this is v${old}.\n  Upgrade Node (https://nodejs.org), then run: npx -y @caveman-ai/cli@${cli}\n`);
  }
  for (const fine of ["22.13.0", "24.1.0", "25.0.0-nightly20260101"]) {
    assert.equal(run(fine).stdout, `installer-ran\nNext: npx -y @caveman-ai/cli@${cli} setup\n`, fine);
  }
});
