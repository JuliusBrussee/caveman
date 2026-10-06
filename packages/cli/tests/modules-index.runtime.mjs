// The signed module index (modules.json) and the module lockfile. Runs a copy
// of the built CLI with a throwaway release key against a local release server.
import test from "node:test";
import assert from "node:assert";
import { spawnSync } from "node:child_process";
import { createHash } from "node:crypto";
import { createServer } from "node:http";
import { existsSync, mkdirSync, mkdtempSync, readdirSync, readFileSync, statSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";
import { RELEASE_TARGETS, releaseArtifactName } from "../../../scripts/build-release-binaries.mjs";
import { modulesIndex } from "../scripts/gen-modules-index.mjs";
import { binaryBody, releaseManifest, signedReleaseCli } from "./_binary-release.mjs";

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

async function serve(files) {
  let binaries = 0;
  const server = createServer((request, response) => {
    const name = (request.url ?? "").slice(`/${release}/`.length);
    if (!(request.url ?? "").startsWith(`/${release}/`)) return response.writeHead(404).end();
    if (files[name] === null) return response.writeHead(404).end();
    if (name in files) return response.end(files[name]);
    binaries++;
    response.end(binaryBody);
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
