// `npx @caveman-ai/cli` (and install.sh, which ends in it) runs the CLI from the
// package runner's cache. Setup must not record a path into that cache: it
// installs the CLI with npm and has the installed copy do the wiring.
import { test } from "node:test";
import assert from "node:assert/strict";
import { cpSync, existsSync, mkdirSync, readFileSync, readdirSync, statSync, writeFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { modulesFixture, runCli } from "./_modules.mjs";

const pkg = join(dirname(fileURLToPath(import.meta.url)), "..");
const version = JSON.parse(readFileSync(join(pkg, "package.json"), "utf8")).version;
const skip = process.platform === "win32" ? "the npm and caveman stand-ins are sh scripts" : false;

// The CLI copied to where npx keeps it, and an `npm` that "installs" it:
// `global` puts a caveman on PATH; `private` fails the global install (a
// system Node) and honours --prefix; `broken` fails both; `hang` never answers.
// `preinstalled`: a caveman of that version is on PATH before the run.
// `cache`: where under HOME the runner keeps it (`mark` names that place).
// `caveHome`: CAVEMAN_HOME under HOME, where the private install goes.
// Like npm exec, the runner's own .bin (with its caveman) leads PATH.
function runner(npm, { preinstalled, agents = ["claude"], cache = "_npx/abc123", mark = "_npx", caveHome } = {}) {
  const fx = modulesFixture({ agents });
  if (caveHome) fx.env.CAVEMAN_HOME = join(fx.home, caveHome);
  const cached = join(fx.home, cache, "node_modules", "@caveman-ai", "cli");
  mkdirSync(cached, { recursive: true });
  cpSync(join(pkg, "dist"), join(cached, "dist"), { recursive: true });
  cpSync(join(pkg, "package.json"), join(cached, "package.json"));
  const runnerBin = join(fx.home, cache, "node_modules", ".bin");
  mkdirSync(runnerBin);
  writeFileSync(join(runnerBin, "caveman"), `#!/bin/sh\nexec node ${cached}/dist/index.js "$@"\n`, { mode: 0o755 });
  fx.env.PATH = `${runnerBin}:${fx.env.PATH}`;
  const installed = join(fx.home, "installed");
  const stub = (target) => `rm -rf '${installed}'; cp -R '${cached}' '${installed}'; mkdir -p "$(dirname '${target}')"; printf '#!/bin/sh\\nexec node %s/dist/index.js "$@"\\n' '${installed}' > '${target}'; chmod +x '${target}'`;
  const prefixed = join(fx.env.CAVEMAN_HOME, "cli", "bin", "caveman");
  writeFileSync(join(fx.bin, "npm"), `#!/bin/sh
echo "$*" >> '${join(fx.home, "npm.log")}'
case "${npm}:$*" in
  broken:*) echo "npm error code EACCES" >&2; exit 243 ;;
  hang:*) exec sleep 30 ;;
  private:*--prefix*) ${stub(prefixed)} ;;
  private:*) echo "npm error code EACCES" >&2; exit 243 ;;
  global:*) ${stub(join(fx.bin, "caveman"))} ;;
esac
`, { mode: 0o755 });
  if (preinstalled) {
    cpSync(cached, installed, { recursive: true });
    writeFileSync(join(installed, "package.json"), JSON.stringify({ ...JSON.parse(readFileSync(join(cached, "package.json"), "utf8")), version: preinstalled }));
    writeFileSync(join(fx.bin, "caveman"), `#!/bin/sh\nexec node ${installed}/dist/index.js "$@"\n`, { mode: 0o755 });
  }
  const calls = () => existsSync(join(fx.home, "npm.log")) ? readFileSync(join(fx.home, "npm.log"), "utf8").trim().split("\n") : [];
  return { ...fx, cached, installed, prefixed, calls, mark, cacheTop: join(fx.home, cache.split("/")[0]), cli: join(cached, "dist", "index.js") };
}

// Every file under HOME that names the runner's cache; the cached copy, the
// installed copy and the stand-in binaries aside.
function cacheReferences(fx) {
  const hits = [];
  const walk = (dir) => {
    for (const name of readdirSync(dir)) {
      const path = join(dir, name);
      if (path === fx.cacheTop || path === fx.installed || path === fx.bin || path === join(fx.home, "npm.log")) continue;
      const stat = statSync(path);
      if (stat.isDirectory()) walk(path);
      else if (stat.size < 2_000_000 && readFileSync(path, "utf8").includes(fx.mark)) hits.push(path);
    }
  };
  walk(fx.home);
  return hits;
}

const setup = (fx, ...argv) => runCli(["setup", ...argv], fx.env, { cli: fx.cli });

test("setup from npx installs the CLI and wires through it: nothing points into the cache", { skip }, async () => {
  const fx = runner("global");
  try {
    const dry = await setup(fx, "--dry-run");
    assert.match(dry.stdout, new RegExp(`RUN +npm install -g @caveman-ai/cli@${version.replaceAll(".", "\\.")} +the caveman command, kept after this run\\n`));
    assert.deepEqual(fx.calls(), [], "a dry run installs nothing");

    const out = await setup(fx, "--yes");
    assert.equal(out.code, 0, out.stdout + out.stderr);
    assert.deepEqual(fx.calls(), [`install -g --no-audit --no-fund @caveman-ai/cli@${version}`]);
    assert.match(out.stdout, /✓ caveman command installed\n[^]*✓ Claude Code wired\n/);
    assert.ok(readFileSync(join(fx.home, ".claude", "settings.json"), "utf8").includes(fx.installed), "the installed copy did the wiring");
    assert.match(out.stdout, /✓ Ready\. Try: {2}caveman claude/, "hints name the installed command, not npx");
    const settings = readFileSync(join(fx.home, ".claude", "settings.json"), "utf8");
    assert.ok(settings.includes(join(fx.installed, "dist", "native-hook-fast.js")) || settings.includes(join(fx.bin, "caveman")), settings);
    assert.deepEqual(cacheReferences(fx), []);
    assert.deepEqual(JSON.parse(readFileSync(join(fx.env.CAVEMAN_HOME, "cloud.json"), "utf8")).setupAgents, ["claude"]);

    // Installed now: a second npx run installs nothing more.
    const again = await setup(fx, "--yes");
    assert.equal(again.code, 0, again.stdout + again.stderr);
    assert.equal(fx.calls().length, 1);
  } finally {
    fx.cleanup();
  }
});

test("with this version installed already, an npx run still wires through the installed copy and runs no npm", { skip }, async () => {
  const fx = runner("broken", { preinstalled: version });
  try {
    const dry = await setup(fx, "--dry-run");
    assert.doesNotMatch(dry.stdout, /npm install/);
    const out = await setup(fx, "--yes");
    assert.equal(out.code, 0, out.stdout + out.stderr);
    assert.deepEqual(fx.calls(), []);
    assert.doesNotMatch(out.stdout, /caveman command installed/);
    assert.match(out.stdout, /✓ Claude Code wired\n[^]*Try: {2}caveman claude/);
    assert.ok(readFileSync(join(fx.home, ".claude", "settings.json"), "utf8").includes(fx.installed), "the installed copy did the wiring");
    assert.deepEqual(cacheReferences(fx), []);
  } finally {
    fx.cleanup();
  }
});

test("an installed caveman of another version is brought to this one before it wires", { skip }, async () => {
  const fx = runner("global", { preinstalled: "0.0.1" });
  try {
    const out = await setup(fx, "--yes");
    assert.equal(out.code, 0, out.stdout + out.stderr);
    assert.deepEqual(fx.calls(), [`install -g --no-audit --no-fund @caveman-ai/cli@${version}`]);
    assert.equal(JSON.parse(readFileSync(join(fx.installed, "package.json"), "utf8")).version, version);
    assert.deepEqual(cacheReferences(fx), []);
  } finally {
    fx.cleanup();
  }
});

test("no agent ticked stays no agent through the handoff", { skip }, async () => {
  const fx = runner("global");
  try {
    const out = await setup(fx, "--yes", "--agents", "none");
    assert.equal(out.code, 0, out.stdout + out.stderr);
    assert.doesNotMatch(out.stdout, /wired/);
    assert.equal(existsSync(join(fx.home, ".claude", "settings.json")), false);
  } finally {
    fx.cleanup();
  }
});

test("a global npm prefix that is not writable falls back to ~/.caveman/cli, and hints name that path", { skip }, async () => {
  const fx = runner("private", { caveHome: "cave man" });
  try {
    const out = await setup(fx, "--yes");
    assert.equal(out.code, 0, out.stdout + out.stderr);
    assert.equal(fx.calls().length, 2);
    assert.match(fx.calls()[1], /^install -g --prefix .*[\\/]cli --no-audit --no-fund @caveman-ai\/cli@/);
    assert.ok(out.stdout.includes(`Try:  '${fx.prefixed}' claude`), "a path with a space is quoted to paste: " + out.stdout);
    assert.match(out.stdout, /✓ Claude Code wired\n/);
    assert.deepEqual(cacheReferences(fx), []);
  } finally {
    fx.cleanup();
  }
});

test("when npm cannot install it at all, setup says how and writes nothing", { skip }, async () => {
  const fx = runner("broken");
  try {
    const out = await setup(fx, "--yes");
    assert.equal(out.code, 1, out.stdout + out.stderr);
    assert.match(out.stdout, new RegExp(`✗ could not install the caveman command \\(code EACCES\\) · run npm install -g @caveman-ai/cli@${version.replaceAll(".", "\\.")}, then caveman setup\\nNothing else changed\\.\\n$`));
    assert.equal(existsSync(join(fx.home, ".claude", "settings.json")), false);
    assert.equal(existsSync(join(fx.env.CAVEMAN_HOME, "integrations", "claude.json")), false);
  } finally {
    fx.cleanup();
  }
});

// A registry that never answers: npm is stopped, the reason is said, and the
// private-prefix retry (for a prefix that is not writable) is not made.
test("an npm install that never finishes is stopped once and named", { skip }, async () => {
  const fx = runner("hang");
  try {
    const out = await runCli(["setup", "--yes"], { ...fx.env, CAVE_NPM_INSTALL_TIMEOUT_MS: "500" }, { cli: fx.cli });
    assert.equal(out.code, 1, out.stdout + out.stderr);
    assert.equal(fx.calls().length, 1);
    assert.match(out.stdout, /✗ could not install the caveman command \(npm did not finish: is the npm registry reachable\?\) · run npm install -g /);
  } finally {
    fx.cleanup();
  }
});

// pnpm's cache is %LOCALAPPDATA%\pnpm-cache on Windows: dlx runs from there.
test("setup from pnpm dlx on Windows' cache path hands off like npx", { skip }, async () => {
  const fx = runner("global", { cache: "AppData/Local/pnpm-cache/dlx/k3y/195-1", mark: "pnpm-cache" });
  try {
    const out = await setup(fx, "--yes");
    assert.equal(out.code, 0, out.stdout + out.stderr);
    assert.deepEqual(fx.calls(), [`install -g --no-audit --no-fund @caveman-ai/cli@${version}`]);
    assert.deepEqual(cacheReferences(fx), []);
  } finally {
    fx.cleanup();
  }
});

// The installed copy runs with npx's PATH; plugins that call back into caveman
// must still name the installed command, not the runner's .bin.
test("plugins wired through the handoff name the installed caveman", { skip }, async () => {
  const fx = runner("global", { agents: ["claude", "opencode", "pi", "hermes"] });
  try {
    const out = await setup(fx, "--yes");
    assert.equal(out.code, 0, out.stdout + out.stderr);
    assert.match(out.stdout, /✓ opencode wired\n[^]*✓ Pi wired\n/);
    assert.deepEqual(cacheReferences(fx), []);
  } finally {
    fx.cleanup();
  }
});

// An older caveman earlier on PATH than the one installed here: say that
// typing caveman reaches it, not that the new one is off PATH.
test("an older caveman that shadows the installed one is named", { skip }, async () => {
  const fx = runner("private");
  try {
    const old = join(fx.home, "old");
    cpSync(fx.cached, old, { recursive: true });
    writeFileSync(join(old, "package.json"), JSON.stringify({ ...JSON.parse(readFileSync(join(fx.cached, "package.json"), "utf8")), version: "0.0.1" }));
    writeFileSync(join(fx.bin, "caveman"), `#!/bin/sh\nexec node ${old}/dist/index.js "$@"\n`, { mode: 0o755 });
    const out = await setup(fx, "--yes");
    assert.equal(out.code, 0, out.stdout + out.stderr);
    assert.ok(out.stdout.includes("✓ caveman command installed at ~/.caveman/cli/bin/caveman · typing caveman runs another copy at ~/bin/caveman\n"), out.stdout);
  } finally {
    fx.cleanup();
  }
});
