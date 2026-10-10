// The signed module index (modules.json) and the module lockfile. Runs a copy
// of the built CLI with a throwaway release key against a local release server.
import test from "node:test";
import assert from "node:assert";
import { spawn, spawnSync } from "node:child_process";
import { createHash } from "node:crypto";
import { createServer } from "node:http";
import { existsSync, mkdirSync, mkdtempSync, readdirSync, readFileSync, renameSync, statSync, utimesSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";
import { RELEASE_TARGETS, releaseArtifactName } from "../../../scripts/build-release-binaries.mjs";
import { modulesIndex } from "../scripts/gen-modules-index.mjs";
import { binaryBody, releaseManifest, signedReleaseCli } from "./_binary-release.mjs";
import { FAKE_BLOCKS, modulesFixture, runCli } from "./_modules.mjs";

const root = join(dirname(fileURLToPath(import.meta.url)), "..");
const { cli, release, sign } = signedReleaseCli();
const { ensureModuleBinaries, loadModuleIndex, NoModuleIndexError, readLock } = await import(
  pathToFileURL(join(dirname(cli), "modules", "index-file.js")).href
);
const sha256 = (value) => createHash("sha256").update(value).digest("hex");
const here = { os: process.platform, arch: process.arch === "x64" ? "amd64" : process.arch };
const exe = (name) => (here.os === "win32" ? `${name}.exe` : name);

// A signed release whose caveman-blocks builds skip the artifacts in `skip`.
function fixtureRelease({ skip = [] } = {}) {
  const blocks = RELEASE_TARGETS.map(([goos, arch]) => releaseArtifactName("caveman-blocks", goos, arch))
    .filter((name) => !skip.includes(name))
    .map((name) => `${sha256(binaryBody)}  ${name}\n`)
    .join("");
  const unlisted = releaseManifest(release) + blocks;
  const modules = `${JSON.stringify(modulesIndex(unlisted, release), null, 2)}\n`;
  const checksums = `${unlisted}${sha256(modules)}  modules.json\n`;
  return { "checksums.txt": checksums, "checksums.txt.keysig": sign(checksums), "modules.json": modules, unlisted };
}

async function serve(files, { delayMs = 0 } = {}) {
  let binaries = 0;
  const server = createServer((request, response) => {
    const name = (request.url ?? "").slice(`/${release}/`.length);
    if (!(request.url ?? "").startsWith(`/${release}/`)) return response.writeHead(404).end();
    if (files[name] === null) return response.writeHead(404).end();
    if (name in files) return response.end(files[name]);
    binaries++;
    setTimeout(() => response.end(binaryBody), delayMs);
  });
  await new Promise((resolve) => server.listen(0, "127.0.0.1", resolve));
  return {
    base: `http://127.0.0.1:${server.address().port}`,
    binaries: () => binaries,
    close: async () => {
      server.closeAllConnections();
      await new Promise((resolve) => server.close(resolve));
    },
  };
}

// Points the imported module at a fresh CAVEMAN_HOME and the given server.
function useHome(base) {
  const home = mkdtempSync(join(tmpdir(), "cave-modules-index-"));
  process.env.CAVEMAN_HOME = home;
  process.env.CAVE_BINARY_RELEASE_BASE = base;
  process.env.CAVE_SETUP_TIMEOUT = "5";
  return home;
}

test("signed index installs the scripts module's caveman-blocks and locks every hash", async () => {
  const server = await serve(fixtureRelease());
  try {
    const home = useHome(server.base);
    const { lock, problems } = await ensureModuleBinaries(["scripts", "browse", "output"]);
    assert.deepEqual(problems, []);
    const digest = sha256(binaryBody);
    assert.deepEqual(lock.modules.scripts, { asked: release, installed: release, binaries: { "caveman-blocks": digest } });
    assert.deepEqual(lock.modules.browse.binaries, { "caveman-browse": digest });
    // output needs the runtime for agent wiring (registry: caveman-proxy + caveman-mcp).
    assert.deepEqual(lock.modules.output.binaries, { "caveman-proxy": digest, "caveman-mcp": digest });
    for (const name of ["caveman-blocks", "caveman-browse"]) {
      const path = join(home, "bin", exe(name));
      assert.equal(readFileSync(path, "utf8"), binaryBody);
      if (here.os !== "win32") assert.equal(statSync(path).mode & 0o777, 0o755);
    }
    const onDisk = JSON.parse(readFileSync(join(home, "modules.lock.json"), "utf8"));
    assert.deepEqual(onDisk, lock);
    assert.deepEqual(readLock(), lock);
    if (here.os !== "win32") assert.equal(statSync(join(home, "modules.lock.json")).mode & 0o777, 0o600);

    // Already-verified binaries are not downloaded again.
    const before = server.binaries();
    await ensureModuleBinaries(["scripts"]);
    assert.equal(server.binaries(), before);
  } finally {
    await server.close();
  }
});

// A Blocks older than rc.2 on PATH heals: `on scripts` fetches the signed
// copy, the lockfile records it, and the hub runs that one from then on.
test("on scripts replaces a pre-rc.2 Blocks on PATH with the signed one", { skip: here.os === "win32" ? "shell stand-in" : false }, async () => {
  const fake = `#!/bin/sh\n${FAKE_BLOCKS}\n`;
  const artifact = releaseArtifactName("caveman-blocks", here.os, here.arch);
  const blocks = RELEASE_TARGETS.map(([goos, arch]) => releaseArtifactName("caveman-blocks", goos, arch))
    .map((name) => `${sha256(name === artifact ? fake : binaryBody)}  ${name}\n`)
    .join("");
  const unlisted = releaseManifest(release) + blocks;
  const modules = `${JSON.stringify(modulesIndex(unlisted, release), null, 2)}\n`;
  const checksums = `${unlisted}${sha256(modules)}  modules.json\n`;
  const server = await serve({ "checksums.txt": checksums, "checksums.txt.keysig": sign(checksums), "modules.json": modules, [artifact]: fake });
  const fx = modulesFixture();
  try {
    writeFileSync(join(fx.bin, "caveman-blocks"), "#!/bin/sh\ncase \"$*\" in *--json*) exit 2 ;; esac\n", { mode: 0o755 });
    const env = { ...fx.env, CAVE_BINARY_RELEASE_BASE: server.base, CAVE_SETUP_TIMEOUT: "5" };
    delete env.CAVEMAN_BLOCKS_BIN;
    // Async: the release server answers from this process.
    const run = (argv) => new Promise((resolve, reject) => {
      const child = spawn(process.execPath, [cli, ...argv], { env, stdio: ["ignore", "pipe", "pipe"] });
      let stdout = "";
      let stderr = "";
      child.stdout.on("data", (d) => (stdout += d));
      child.stderr.on("data", (d) => (stderr += d));
      child.on("error", reject);
      child.on("exit", (code) => resolve({ code, stdout, stderr }));
    });
    const on = await run(["on", "scripts", "--yes"]);
    assert.equal(on.code, 0, on.stderr);
    assert.match(on.stdout, /^✓ downloaded caveman-blocks\n✓ scripts ready \(Claude Code, Codex\)$/m);
    assert.equal(readFileSync(join(env.CAVEMAN_HOME, "bin", exe("caveman-blocks")), "utf8"), fake);
    assert.ok(JSON.parse(readFileSync(join(env.CAVEMAN_HOME, "modules.lock.json"), "utf8")).modules.scripts.binaries["caveman-blocks"]);
    const status = await run(["status", "--json"]);
    assert.equal(JSON.parse(status.stdout).modules.find((state) => state.id === "scripts").active, true);
  } finally {
    fx.cleanup();
    await server.close();
  }
});

// The hub binaries a modules fixture puts on PATH, moved into ~/.caveman/bin
// under a manifest naming `installed`: what an older CLI's install left.
const HUB_BINS = { "caveman-proxy": "CAVEMAN_PROXY_BIN", "caveman-engine": "CAVEMAN_ENGINE_BIN", "caveman-mcp": "CAVEMAN_MCP_BIN", cavemem: "CAVEMEM_BIN", "caveman-browse": "CAVEMAN_BROWSE_BIN", "caveman-shrink": "CAVEMAN_SHRINK_BIN" };
function managedInstall(env, installed) {
  const binDir = join(env.CAVEMAN_HOME, "bin");
  mkdirSync(binDir, { recursive: true });
  const artifacts = {};
  for (const [name, key] of Object.entries(HUB_BINS)) {
    renameSync(env[key], join(binDir, name));
    delete env[key];
    artifacts[name] = sha256(readFileSync(join(binDir, name)));
  }
  writeFileSync(join(binDir, ".bin-manifest.json"), JSON.stringify({ release: installed, artifacts }));
  return binDir;
}

// A CLI upgrade pins a new release, but the old binaries still answer every
// capability probe: only the install manifest tells them apart.
test("binaries an older release installed are out of date, and on replaces them all", { skip: here.os === "win32" ? "shell stand-in" : false }, async () => {
  const server = await serve(fixtureRelease());
  const fx = modulesFixture();
  try {
    const env = { ...fx.env, CAVE_BINARY_RELEASE_BASE: server.base, CAVE_SETUP_TIMEOUT: "5" };
    const binDir = managedInstall(env, "bin-v0.0.1");
    const doctor = await runCli(["doctor"], env, { cli });
    assert.match(doctor.stdout, /^✗ caveman-proxy, caveman-engine, caveman-mcp, cavemem, caveman-browse, caveman-shrink are out of date · fix: caveman setup --install$/m);
    const status = await runCli(["status"], env, { cli });
    assert.match(status.stdout, new RegExp(`^Caveman binaries are from bin-v0\\.0\\.1; this CLI needs ${release} — update before compressing · caveman setup --install$`, "m"));
    const plan = await runCli(["on", "browse", "--dry-run"], env, { cli });
    assert.match(plan.stdout, new RegExp(`DOWNLOAD +caveman-browse +signed, ${release}`));

    const on = await runCli(["on", "browse", "--yes"], env, { cli });
    assert.equal(on.code, 0, on.stdout + on.stderr);
    assert.equal(JSON.parse(readFileSync(join(binDir, ".bin-manifest.json"), "utf8")).release, release);
    for (const name of Object.keys(HUB_BINS)) assert.equal(readFileSync(join(binDir, name), "utf8"), binaryBody, name);
    // The stand-in binaries the release serves answer no probe; only the
    // release check is under test here.
    assert.doesNotMatch((await runCli(["doctor"], env, { cli })).stdout, /caveman-engine/);
  } finally {
    fx.cleanup();
    await server.close();
  }
});

// A running runtime keeps the binary it started from. Replacing caveman-proxy
// restarts the one agents route through, at the same address and mode.
// Onboarding draws its own progress line: the restart says nothing there.
for (const [label, argv] of [["an update", ["setup", "--install"]], ["onboarding", ["setup", "--yes", "--only", "input", "--agents", "none"]]]) test(`${label} restarts the runtime agents route through on the new proxy`, { skip: here.os === "win32" ? "shell stand-in" : false }, async () => {
  const serveJs = `const fs = require("node:fs"); const port = Number(process.env.CAVEMAN_LISTEN.split(":").pop());
const file = process.env.CAVEMAN_HOME + "/run/" + port + ".json";
const server = require("node:net").createServer().listen(port, "127.0.0.1", () => {
  fs.mkdirSync(process.env.CAVEMAN_HOME + "/run", { recursive: true });
  fs.writeFileSync(file, JSON.stringify({ owner: process.env.CAVEMAN_PROXY_OWNER, pid: process.pid, port, version: "bin-new", mode: process.env.CAVEMAN_MODE, instance_token: "n" }));
});
process.on("SIGTERM", () => { fs.rmSync(file, { force: true }); process.exit(0); });`;
  const proxy = `#!/bin/sh
case "$1" in
  version) printf '%s\\n' '{"version":"bin-new","capabilities":["run_state"]}' ;;
  status) if [ -f "$CAVEMAN_HOME/run/$4.json" ]; then cat "$CAVEMAN_HOME/run/$4.json"; else printf '%s\\n' '{"owner":"unknown"}'; fi ;;
  stats) printf '%s\\n' '[]' ;;
  "") exec ${JSON.stringify(process.execPath)} -e '${serveJs}' ;;
esac
`;
  const artifact = releaseArtifactName("caveman-proxy", here.os, here.arch);
  const unlisted = fixtureRelease().unlisted.replace(`${sha256(binaryBody)}  ${artifact}\n`, `${sha256(proxy)}  ${artifact}\n`);
  const modules = `${JSON.stringify(modulesIndex(unlisted, release), null, 2)}\n`;
  const checksums = `${unlisted}${sha256(modules)}  modules.json\n`;
  const server = await serve({ "checksums.txt": checksums, "checksums.txt.keysig": sign(checksums), "modules.json": modules, [artifact]: proxy });
  const fx = modulesFixture();
  const port = await new Promise((resolve) => {
    const probe = createServer().listen(0, "127.0.0.1", () => {
      const { port } = probe.address();
      probe.close(() => resolve(port));
    });
  });
  const env = { ...fx.env, CAVE_BINARY_RELEASE_BASE: server.base, CAVE_SETUP_TIMEOUT: "5", CAVE_GATEWAY_URL: `http://127.0.0.1:${port}`, CAVEMAN_LISTEN: `127.0.0.1:${port}` };
  managedInstall(env, "bin-v0.0.1");
  const runFile = join(env.CAVEMAN_HOME, "run", `${port}.json`);
  mkdirSync(dirname(runFile), { recursive: true });
  // The runtime an older caveman-proxy started for an agent session.
  const old = spawn(process.execPath, ["-e", `
    const fs = require("node:fs");
    require("node:net").createServer().listen(${port}, "127.0.0.1", () => {
      fs.writeFileSync(${JSON.stringify(runFile)}, JSON.stringify({ owner: "wrap", pid: process.pid, port: ${port}, version: "bin-old", mode: "compress", instance_token: "o" }));
      process.stdout.write("ready\\n");
    });
    process.on("SIGTERM", () => { fs.rmSync(${JSON.stringify(runFile)}, { force: true }); process.exit(0); });
  `], { stdio: ["ignore", "pipe", "inherit"] });
  await new Promise((resolve) => old.stdout.once("data", resolve));
  const oldExit = new Promise((resolve) => old.once("exit", resolve));
  try {
    const update = await runCli(argv, env, { cli });
    assert.equal(update.code, 0, update.stdout + update.stderr);
    assert.ok(await Promise.race([oldExit.then(() => true), new Promise((resolve) => setTimeout(resolve, 10_000, false))]), "the old runtime still runs");
    if (argv.includes("--install")) assert.match(update.stderr, new RegExp(`started Caveman proxy on 127\\.0\\.0\\.1:${port} \\(compress\\)`));
    else assert.doesNotMatch(update.stderr, /started Caveman proxy/);
    const running = JSON.parse(readFileSync(runFile, "utf8"));
    assert.deepEqual([running.version, running.owner, running.mode], ["bin-new", "wrap", "compress"]);
  } finally {
    old.kill("SIGKILL");
    try { process.kill(JSON.parse(readFileSync(runFile, "utf8")).pid, "SIGTERM"); } catch { /* not started */ }
    fx.cleanup();
    await server.close();
  }
});

// Every agent launched right after a CLI upgrade installs at once. One
// downloads; the others wait and reuse its install instead of sharing, and
// deleting, its half-written download. One a killed install left is removed.
test("concurrent installs download each binary once and all succeed", { skip: here.os === "win32" ? "shell stand-in" : false }, async () => {
  const server = await serve(fixtureRelease(), { delayMs: 200 });
  const fx = modulesFixture();
  try {
    const env = { ...fx.env, CAVE_BINARY_RELEASE_BASE: server.base, CAVE_SETUP_TIMEOUT: "20" };
    mkdirSync(join(env.CAVEMAN_HOME, "bin"), { recursive: true });
    writeFileSync(join(env.CAVEMAN_HOME, "bin", `caveman-proxy.${spawnSync(process.execPath, ["-e", ""]).pid}.part`), "killed");
    const runs = await Promise.all([1, 2, 3].map(() => runCli(["setup", "--install"], env, { cli, timeoutMs: 120_000 })));
    for (const run of runs) assert.equal(run.code, 0, run.stdout + run.stderr);
    assert.equal(server.binaries(), Object.keys(HUB_BINS).length);
    for (const name of Object.keys(HUB_BINS)) assert.equal(readFileSync(join(env.CAVEMAN_HOME, "bin", name), "utf8"), binaryBody, name);
    assert.deepEqual(readdirSync(join(env.CAVEMAN_HOME, "bin")).filter((name) => name.endsWith(".part")), []);
  } finally {
    fx.cleanup();
    await server.close();
  }
});

// A lock whose token write failed (a full disk) is left empty. Read as a
// holder still writing its token, it held every install for ten minutes; one
// seconds old holds nothing.
test("an empty install lock a failed write left does not hold installs", { skip: here.os === "win32" ? "shell stand-in" : false }, async () => {
  const server = await serve(fixtureRelease());
  const fx = modulesFixture();
  try {
    const env = { ...fx.env, CAVE_BINARY_RELEASE_BASE: server.base, CAVE_SETUP_TIMEOUT: "20" };
    const lock = join(env.CAVEMAN_HOME, ".install.lock");
    writeFileSync(lock, "");
    const before = new Date(Date.now() - 30_000);
    utimesSync(lock, before, before);
    const run = await runCli(["setup", "--install"], env, { cli, timeoutMs: 60_000 });
    assert.equal(run.code, 0, run.stdout + run.stderr);
    assert.doesNotMatch(run.stderr, /another Caveman is installing/);
  } finally {
    fx.cleanup();
    await server.close();
  }
});

// An agent launch or `caveman on` updates implicitly. A restart would cut every
// stream another session has in flight, so a runtime in use keeps running on
// the old binary with a hint; an idle one restarts. Setup draws its own
// progress line, so there the hint is one of its steps, not a stray stderr line.
for (const [name, { marker, rows, restarts, argv = ["on", "browse", "--yes"] }] of Object.entries({
  "a live caveman session": { marker: true, rows: "[]", restarts: false },
  "a live caveman session during setup": { marker: true, rows: "[]", restarts: false, argv: ["setup", "--yes"] },
  "a request in the last 30 minutes": { marker: false, rows: JSON.stringify([{ ts: new Date().toISOString() }]), restarts: false },
  "nothing recent": { marker: false, rows: "[]", restarts: true },
})) {
  test(`${argv[0]} after an upgrade ${restarts ? "restarts" : "leaves running"} a runtime with ${name}`, { skip: here.os === "win32" ? "shell stand-in" : false }, async () => {
    const serveJs = `const fs = require("node:fs"); const port = Number(process.env.CAVEMAN_LISTEN.split(":").pop());
const file = process.env.CAVEMAN_HOME + "/run/" + port + ".json";
require("node:net").createServer().listen(port, "127.0.0.1", () => {
  fs.writeFileSync(file, JSON.stringify({ owner: process.env.CAVEMAN_PROXY_OWNER, pid: process.pid, port, version: "bin-new", mode: process.env.CAVEMAN_MODE, instance_token: "n" }));
});
process.on("SIGTERM", () => { fs.rmSync(file, { force: true }); process.exit(0); });`;
    const proxy = `#!/bin/sh
case "$1" in
  version) printf '%s\\n' '{"version":"bin-new","capabilities":["run_state"]}' ;;
  status) if [ -f "$CAVEMAN_HOME/run/$4.json" ]; then cat "$CAVEMAN_HOME/run/$4.json"; else printf '%s\\n' '{"owner":"unknown"}'; fi ;;
  stats) printf '%s\\n' "$STATS_ROWS" ;;
  "") exec ${JSON.stringify(process.execPath)} -e '${serveJs}' ;;
esac
`;
    const artifact = releaseArtifactName("caveman-proxy", here.os, here.arch);
    const unlisted = fixtureRelease().unlisted.replace(`${sha256(binaryBody)}  ${artifact}\n`, `${sha256(proxy)}  ${artifact}\n`);
    const modules = `${JSON.stringify(modulesIndex(unlisted, release), null, 2)}\n`;
    const checksums = `${unlisted}${sha256(modules)}  modules.json\n`;
    const server = await serve({ "checksums.txt": checksums, "checksums.txt.keysig": sign(checksums), "modules.json": modules, [artifact]: proxy });
    const fx = modulesFixture();
    const port = await new Promise((resolve) => {
      const probe = createServer().listen(0, "127.0.0.1", () => {
        const { port } = probe.address();
        probe.close(() => resolve(port));
      });
    });
    const env = { ...fx.env, CAVE_BINARY_RELEASE_BASE: server.base, CAVE_SETUP_TIMEOUT: "5", CAVE_GATEWAY_URL: `http://127.0.0.1:${port}`, CAVEMAN_LISTEN: `127.0.0.1:${port}`, STATS_ROWS: rows };
    managedInstall(env, "bin-v0.0.1");
    const runFile = join(env.CAVEMAN_HOME, "run", `${port}.json`);
    mkdirSync(dirname(runFile), { recursive: true });
    if (marker) {
      mkdirSync(join(env.CAVEMAN_HOME, "run", `${port}.sessions`), { recursive: true });
      writeFileSync(join(env.CAVEMAN_HOME, "run", `${port}.sessions`, `${process.pid}-session`), "");
    }
    const old = spawn(process.execPath, ["-e", `
      const fs = require("node:fs");
      require("node:net").createServer().listen(${port}, "127.0.0.1", () => {
        fs.writeFileSync(${JSON.stringify(runFile)}, JSON.stringify({ owner: "wrap", pid: process.pid, port: ${port}, version: "bin-old", mode: "compress", instance_token: "o" }));
        process.stdout.write("ready\\n");
      });
      process.on("SIGTERM", () => { fs.rmSync(${JSON.stringify(runFile)}, { force: true }); process.exit(0); });
    `], { stdio: ["ignore", "pipe", "inherit"] });
    await new Promise((resolve) => old.stdout.once("data", resolve));
    const oldExit = new Promise((resolve) => old.once("exit", resolve));
    try {
      const on = await runCli(argv, env, { cli });
      // Setup goes on to wire agents, which the stand-in binaries cannot serve.
      if (argv[0] !== "setup") assert.equal(on.code, 0, on.stdout + on.stderr);
      const stopped = await Promise.race([oldExit.then(() => true), new Promise((resolve) => setTimeout(resolve, restarts ? 10_000 : 1_000, false))]);
      assert.equal(stopped, restarts, on.stdout + on.stderr);
      const stillOld = `caveman-proxy bin-old still runs on 127\\.0\\.0\\.1:${port} — run \`caveman stop\`, then start your agent again to use bin-new`;
      if (!restarts && argv[0] === "setup") {
        assert.match(on.stdout, new RegExp(`^○ ${stillOld}$`, "m"));
        assert.doesNotMatch(on.stderr, /still runs on/);
      } else if (!restarts) assert.match(on.stderr, new RegExp(stillOld));
    } finally {
      old.kill("SIGKILL");
      try { process.kill(JSON.parse(readFileSync(runFile, "utf8")).pid, "SIGTERM"); } catch { /* not started */ }
      fx.cleanup();
      await server.close();
    }
  });
}

test("a tampered modules.json is refused and nothing is written", async () => {
  const files = fixtureRelease();
  files["modules.json"] = files["modules.json"].replace('"needsSignIn": true', '"needsSignIn": false');
  const server = await serve(files);
  try {
    const home = useHome(server.base);
    await assert.rejects(ensureModuleBinaries(["scripts"]), /signature check failed for modules\.json/);
    assert.equal(existsSync(join(home, "modules.lock.json")), false);
    assert.equal(existsSync(join(home, "bin", exe("caveman-blocks"))), false);
  } finally {
    await server.close();
  }
});

test("a modules.json the signed manifest does not list is refused", async () => {
  const files = fixtureRelease();
  files["checksums.txt"] = files.unlisted;
  files["checksums.txt.keysig"] = sign(files.unlisted);
  const server = await serve(files);
  try {
    useHome(server.base);
    await assert.rejects(loadModuleIndex(), new RegExp(`${release} has no signed modules\\.json`));
    // An older signed release: callers may fall back to the full install.
    await assert.rejects(loadModuleIndex(), NoModuleIndexError);
  } finally {
    await server.close();
  }
});

test("a manifest with a bad signature is refused", async () => {
  const files = fixtureRelease();
  files["checksums.txt.keysig"] = sign("other bytes");
  const server = await serve(files);
  try {
    useHome(server.base);
    await assert.rejects(loadModuleIndex(), /signature check failed for checksums\.txt/);
    // A refusal never looks like an older release, so nothing falls back past it.
    await assert.rejects(loadModuleIndex(), (error) => !(error instanceof NoModuleIndexError));
  } finally {
    await server.close();
  }
});

test("a module with no build for this platform is named; the rest still install", async () => {
  const goos = here.os === "win32" ? "windows" : here.os;
  const server = await serve(fixtureRelease({ skip: [releaseArtifactName("caveman-blocks", goos, here.arch)] }));
  try {
    const home = useHome(server.base);
    const { lock, problems } = await ensureModuleBinaries(["scripts", "browse"]);
    assert.deepEqual(problems, [`scripts: caveman-blocks has no ${here.os}/${here.arch} build in ${release}`]);
    assert.equal(lock.modules.scripts, undefined);
    assert.ok(lock.modules.browse);
    assert.equal(existsSync(join(home, "bin", exe("caveman-blocks"))), false);
  } finally {
    await server.close();
  }
});

test("generator: modules.json shape from the registry and checksums.txt", () => {
  const { unlisted } = fixtureRelease({ skip: ["caveman-blocks_win32_arm64"] });
  const index = modulesIndex(unlisted, release);
  assert.equal(index.schema, "caveman.modules.v1");
  assert.equal(index.release, release);
  assert.deepEqual(
    index.modules.map((m) => [m.id, m.kind, m.defaultOn, m.needsSignIn]),
    [
      ["output", "pack", true, false],
      ["input", "stage", true, false],
      ["waste-fixes", "pack", true, false],
      ["routing", "stage", true, true],
      ["scripts", "tool", true, false],
      ["browse", "tool", true, false],
    ],
  );
  const input = index.modules.find((m) => m.id === "input");
  assert.deepEqual(Object.keys(input.binaries), ["caveman-proxy", "caveman-engine", "caveman-mcp", "cavemem", "caveman-shrink"]);
  assert.equal(input.binaries["caveman-proxy"].length, RELEASE_TARGETS.length);
  assert.deepEqual(
    input.binaries["caveman-proxy"].find((b) => b.os === "win32" && b.arch === "amd64"),
    { os: "win32", arch: "amd64", name: "caveman-proxy_win32_amd64", sha256: sha256(binaryBody) },
  );
  const blocks = index.modules.find((m) => m.id === "scripts").binaries["caveman-blocks"];
  assert.equal(blocks.length, RELEASE_TARGETS.length - 1);
  assert.ok(!blocks.some((b) => b.os === "win32" && b.arch === "arm64"));

  // A hub binary missing from the release is a generator error, not a gap.
  const withoutProxy = unlisted.split("\n").filter((line) => !line.endsWith("  caveman-proxy_linux_amd64")).join("\n");
  assert.throws(() => modulesIndex(withoutProxy, release), /checksums\.txt has no caveman-proxy_linux_amd64/);

  // The script prints the same index.
  const path = join(mkdtempSync(join(tmpdir(), "cave-modules-gen-")), "checksums.txt");
  writeFileSync(path, unlisted);
  const out = spawnSync(process.execPath, [join(root, "scripts", "gen-modules-index.mjs"), path, release], { encoding: "utf8" });
  assert.equal(out.status, 0, out.stderr);
  assert.deepEqual(JSON.parse(out.stdout), index);
});

test("a tampered module binary is refused; modules installed before it stay locked", async () => {
  const blocks = releaseArtifactName("caveman-blocks", here.os === "win32" ? "windows" : here.os, here.arch);
  const server = await serve({ ...fixtureRelease(), [blocks]: "tampered\n" });
  try {
    const home = useHome(server.base);
    await assert.rejects(
      ensureModuleBinaries(["browse", "scripts"]),
      new RegExp(`signature check failed for ${blocks} — refusing to install the scripts module`),
    );
    const lock = readFileSync(join(home, "modules.lock.json"), "utf8");
    assert.deepEqual(Object.keys(JSON.parse(lock).modules), ["browse"]);
    assert.deepEqual(readdirSync(join(home, "bin")), [exe("caveman-browse")]);

    await assert.rejects(ensureModuleBinaries(["scripts"]), /signature check failed/);
    assert.equal(readFileSync(join(home, "modules.lock.json"), "utf8"), lock);
    assert.deepEqual(readdirSync(join(home, "bin")), [exe("caveman-browse")]);
  } finally {
    await server.close();
  }
});

test("a failed module download names the module and its retry", async () => {
  const blocks = releaseArtifactName("caveman-blocks", here.os === "win32" ? "windows" : here.os, here.arch);
  const server = await serve({ ...fixtureRelease(), [blocks]: null });
  try {
    const home = useHome(server.base);
    await assert.rejects(
      ensureModuleBinaries(["scripts"]),
      new RegExp(`the scripts module could not download ${blocks} \\(404.*retry with \`caveman on scripts\``),
    );
    assert.deepEqual(readdirSync(join(home, "bin")), []);
  } finally {
    await server.close();
  }
});

test("setup lists caveman-blocks only once a module installed it", () => {
  const home = mkdtempSync(join(tmpdir(), "cave-modules-setup-"));
  const caveHome = join(home, ".caveman");
  const blocksRow = () => {
    const out = spawnSync(process.execPath, [cli, "setup", "--json"], {
      encoding: "utf8",
      env: { ...process.env, NO_COLOR: "1", HOME: home, CAVEMAN_HOME: caveHome, CAVEMAN_BLOCKS_BIN: "", PATH: "/usr/bin:/bin" },
    });
    return JSON.parse(out.stdout).binaries.find((b) => b.name === "caveman-blocks");
  };
  assert.equal(blocksRow(), undefined);
  mkdirSync(caveHome, { recursive: true });
  writeFileSync(join(caveHome, "modules.lock.json"), JSON.stringify({
    schema: "caveman.modules.lock.v1",
    modules: { scripts: { asked: release, installed: release, binaries: { "caveman-blocks": sha256(binaryBody) } } },
  }));
  const row = blocksRow();
  assert.equal(row.required, false);
  assert.equal(row.path, null);
});
