import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { existsSync, mkdtempSync, mkdirSync, readFileSync, writeFileSync, readdirSync, rmSync, copyFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";
import test from "node:test";

const root = join(dirname(fileURLToPath(import.meta.url)), "..", "..");
function fixture(t, code, failures) {
  const dir = mkdtempSync(join(tmpdir(), "caveman-wedge-lock-#-"));
  t.after(() => rmSync(dir, { recursive: true, force: true }));
  const cli = join(dir, "packages", "cli");
  const target = join(dir, "packages", "shared", "binary-installer");
  mkdirSync(join(cli, "scripts"), { recursive: true });
  mkdirSync(target, { recursive: true });
  copyFileSync(join(root, "packages", "cli", "scripts", "gen-wedge-installer.mjs"), join(cli, "scripts", "gen-wedge-installer.mjs"));
  writeFileSync(join(cli, "BINARY_RELEASE"), "fixture-v1\n");
  writeFileSync(join(cli, "BINARY_SIGNING_PUBKEY.pub"), "fixture-key\n");
  const output = join(target, "release.generated.mjs");
  writeFileSync(output, "// old output\n");
  const preload = join(dir, "locked-fs.mjs");
  const attempts = join(dir, "attempts");
  writeFileSync(preload, `import fs from 'node:fs';
import {syncBuiltinESMExports} from 'node:module';
const rename=fs.renameSync;let calls=0;
fs.renameSync=(from,to)=>{calls++;fs.writeFileSync(${JSON.stringify(attempts)},String(calls));
if(calls<=${failures})throw Object.assign(new Error('injected rename lock'),{code:${JSON.stringify(code)}});
return rename(from,to);};syncBuiltinESMExports();`);
  const result = spawnSync(process.execPath, ["--import", pathToFileURL(preload).href, join(cli, "scripts", "gen-wedge-installer.mjs")], { cwd: dir, encoding: "utf8", timeout: 10_000, env: { PATH: process.env.PATH, HOME: dir, USERPROFILE: dir, TMPDIR: dir, TEMP: dir, TMP: dir } });
  assert.equal(result.error, undefined, result.error?.message);
  assert.ok(existsSync(attempts), `preload did not reach renameSync: ${result.stderr}`);
  return { result, output: readFileSync(output, "utf8"), calls: Number(readFileSync(attempts, "utf8")), leftovers: readdirSync(target).filter((name) => name.endsWith(".tmp")) };
}

for (const code of ["EPERM", "EBUSY", "EACCES"]) {
  test(`wedge generation retries transient ${code} before publishing`, (t) => {
    const { result, output, calls, leftovers } = fixture(t, code, 2);
    assert.equal(result.status, 0, result.stderr);
    assert.match(output, /BINARY_RELEASE = "fixture-v1"/);
    assert.equal(calls, 3);
    assert.deepEqual(leftovers, []);
  });
}

test("wedge generation bounds retries and preserves old output on a persistent lock", (t) => {
  const { result, output, calls, leftovers } = fixture(t, "EPERM", 100);
  assert.notEqual(result.status, 0);
  assert.match(result.stderr, /injected rename lock/);
  assert.equal(calls, 6);
  assert.equal(output, "// old output\n");
  assert.deepEqual(leftovers, []);
});

test("wedge generation does not retry unrelated filesystem errors", (t) => {
  const { result, output, calls, leftovers } = fixture(t, "ENOSPC", 100);
  assert.notEqual(result.status, 0);
  assert.equal(calls, 1);
  assert.equal(output, "// old output\n");
  assert.deepEqual(leftovers, []);
});
