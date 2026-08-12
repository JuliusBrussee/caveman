import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { mkdirSync, mkdtempSync, readFileSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import test from "node:test";

const packageRoot = resolve(import.meta.dirname, "..");

test("packed caveman-browse installs and launches an explicit verified binary", () => {
  const root = mkdtempSync(join(tmpdir(), "caveman-browse-package-"));
  const packed = join(root, "packed");
  const consumer = join(root, "consumer");
  try {
    mkdirSync(packed, { recursive: true });
    const env = { ...process.env, NPM_CONFIG_CACHE: join(root, "npm-cache") };
    const pack = spawnSync("npm", ["pack", "--json", "--pack-destination", packed], {
      cwd: packageRoot,
      env,
      encoding: "utf8",
    });
    assert.equal(pack.status, 0, pack.stderr);
    const jsonAt = pack.stdout.indexOf('[\n');
    assert.notEqual(jsonAt, -1, pack.stdout);
    const metadata = JSON.parse(pack.stdout.slice(jsonAt))[0];
    const names = new Set(metadata.files.map((entry) => entry.path));
    assert.ok(names.has("bin/caveman-browse.mjs"));
    assert.ok(names.has("bin/binary-installer.generated.mjs"));
    assert.ok(names.has("bin/release.generated.mjs"));
    assert.equal([...names].some((name) => name.startsWith("dist/")), false);

    const tarball = join(packed, metadata.filename);
    const install = spawnSync("npm", [
      "install", "--ignore-scripts", "--no-audit", "--no-fund", "--prefix", consumer, tarball,
    ], { env, encoding: "utf8" });
    assert.equal(install.status, 0, install.stderr);

    const manifest = JSON.parse(readFileSync(join(consumer, "node_modules", "caveman-browse", "package.json"), "utf8"));
    const launcher = join(consumer, "node_modules", "caveman-browse", manifest.bin["caveman-browse"]);
    const smoke = spawnSync(process.execPath, [launcher, "--version"], {
      env: { ...process.env, CAVEMAN_BROWSE_BIN: process.execPath },
      encoding: "utf8",
    });
    assert.equal(smoke.status, 0, smoke.stderr);
    assert.match(smoke.stdout, /^v\d+\./);
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});
