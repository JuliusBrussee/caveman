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
// system Node) and honours --prefix; `broken` fails both.
function runner(npm) {
  const fx = modulesFixture({ agents: ["claude"] });
  const cached = join(fx.home, "_npx", "abc123", "node_modules", "@caveman-ai", "cli");
  mkdirSync(cached, { recursive: true });
  cpSync(join(pkg, "dist"), join(cached, "dist"), { recursive: true });
  cpSync(join(pkg, "package.json"), join(cached, "package.json"));
  const installed = join(fx.home, "installed");
  const stub = (target) => `cp -R '${cached}' '${installed}'; mkdir -p "$(dirname '${target}')"; printf '#!/bin/sh\\nexec node %s/dist/index.js "$@"\\n' '${installed}' > '${target}'; chmod +x '${target}'`;
  const prefixed = join(fx.env.CAVEMAN_HOME, "cli", "bin", "caveman");
  writeFileSync(join(fx.bin, "npm"), `#!/bin/sh
echo "$*" >> '${join(fx.home, "npm.log")}'
case "${npm}:$*" in
  broken:*) echo "npm error code EACCES" >&2; exit 243 ;;
  private:*--prefix*) ${stub(prefixed)} ;;
  private:*) echo "npm error code EACCES" >&2; exit 243 ;;
  global:*) ${stub(join(fx.bin, "caveman"))} ;;
esac
`, { mode: 0o755 });
  const calls = () => existsSync(join(fx.home, "npm.log")) ? readFileSync(join(fx.home, "npm.log"), "utf8").trim().split("\n") : [];
  return { ...fx, cached, installed, prefixed, calls, cli: join(cached, "dist", "index.js") };
}

// Every file under HOME that names the runner's cache; the cached copy, the
// installed copy and the stand-in binaries aside.
function cacheReferences(fx) {
  const hits = [];
  const walk = (dir) => {
    for (const name of readdirSync(dir)) {
      const path = join(dir, name);
      if (path === join(fx.home, "_npx") || path === fx.installed || path === fx.bin || path === join(fx.home, "npm.log")) continue;
      const stat = statSync(path);
      if (stat.isDirectory()) walk(path);
      else if (stat.size < 2_000_000 && readFileSync(path, "utf8").includes("_npx")) hits.push(path);
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
    assert.match(out.stdout, /✓ Ready\. Try: {2}caveman claude/, "hints name the installed command, not npx");
    const settings = readFileSync(join(fx.home, ".claude", "settings.json"), "utf8");
    assert.ok(settings.includes(join(fx.installed, "dist", "native-hook-fast.js")) || settings.includes(join(fx.bin, "caveman")), settings);
    assert.deepEqual(cacheReferences(fx), []);
    assert.deepEqual(JSON.parse(readFileSync(join(fx.env.CAVEMAN_HOME, "cloud.json"), "utf8")).setupAgents, ["claude"]);

    // Installed now: a second npx run wires in place and installs nothing more.
    const again = await setup(fx, "--yes");
    assert.equal(again.code, 0, again.stdout + again.stderr);
    assert.equal(fx.calls().length, 1);
    assert.deepEqual(cacheReferences(fx), []);
  } finally {
    fx.cleanup();
  }
});

test("a global npm prefix that is not writable falls back to ~/.caveman/cli, and hints name that path", { skip }, async () => {
  const fx = runner("private");
  try {
    const out = await setup(fx, "--yes");
    assert.equal(out.code, 0, out.stdout + out.stderr);
    assert.equal(fx.calls().length, 2);
    assert.match(fx.calls()[1], /^install -g --prefix .*[\\/]cli --no-audit --no-fund @caveman-ai\/cli@/);
    assert.ok(out.stdout.includes(`Try:  ${fx.prefixed} claude`), out.stdout);
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
