import test from "node:test";
import assert from "node:assert";
import { spawn } from "node:child_process";
import { createServer } from "node:http";
import { mkdtempSync, readFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const root = join(dirname(fileURLToPath(import.meta.url)), "..");
const cli = join(root, "dist", "index.js");
const release = readFileSync(join(root, "BINARY_RELEASE"), "utf8").trim();
const cliVersion = JSON.parse(readFileSync(join(root, "package.json"), "utf8")).version;
// NOT in the Windows gate (scripts/test-windows-compat.mjs), and not for a
// harness reason: the committed manifest below carries only the darwin and
// linux artifacts, so on win32 `update` looks up caveman-proxy_win32_amd64,
// misses, and fails before it downloads anything. Adding the rows needs the
// release key, which only the binary-release environment holds:
//   node -e 'const{execFileSync}=require("node:child_process"),{createHash}=require("node:crypto"),{writeFileSync}=require("node:fs");const d=createHash("sha256").update("#!/bin/sh\nexit 0\n").digest("hex");writeFileSync("packages/cli/tests/fixtures/binary-release/checksums.txt",execFileSync("node",["scripts/build-release-binaries.mjs","--list"],{encoding:"utf8"}).trim().split("\n").map((n)=>`${d}  ${n}\n`).join(""))'
//   CAVEMAN_BINARY_SIGNING_PRIVATE_KEY_PEM=... node scripts/sign-binary-checksums.mjs \
//     packages/cli/tests/fixtures/binary-release/checksums.txt \
//     packages/cli/tests/fixtures/binary-release/checksums.txt.keysig \
//     packages/cli/BINARY_SIGNING_PUBKEY.pub
// Once that lands, this file needs no change to run on Windows.
const fixture = join(root, "tests", "fixtures", "binary-release");
const checksums = readFileSync(join(fixture, "checksums.txt"), "utf8");
const signature = readFileSync(join(fixture, "checksums.txt.keysig"), "utf8");
const binaryBody = "#!/bin/sh\nexit 0\n";

function runCli(argv, env) {
  return new Promise((resolve, reject) => {
    const child = spawn(process.execPath, [cli, ...argv], { env });
    let stdout = "";
    let stderr = "";
    child.stdout.on("data", (chunk) => (stdout += chunk));
    child.stderr.on("data", (chunk) => (stderr += chunk));
    child.on("exit", (code) => resolve({ code, stdout, stderr }));
    child.on("error", reject);
  });
}

async function releaseServer() {
  const server = createServer((request, response) => {
    const path = request.url ?? "";
    if (path === `/${release}/checksums.txt`) return void response.end(checksums);
    if (path === `/${release}/checksums.txt.keysig`) return void response.end(signature);
    if (path.startsWith(`/${release}/`)) return void response.end(binaryBody);
    response.writeHead(404).end();
  });
  await new Promise((resolve) => server.listen(0, "127.0.0.1", resolve));
  return { base: `http://127.0.0.1:${server.address().port}`, close: () => new Promise((resolve) => { server.closeAllConnections(); server.close(resolve); }) };
}

async function registryServer(latest) {
  const server = createServer((request, response) => {
    if ((request.url ?? "").startsWith("/@caveman-ai")) {
      return void response.end(JSON.stringify({ "dist-tags": { latest } }));
    }
    response.writeHead(404).end();
  });
  await new Promise((resolve) => server.listen(0, "127.0.0.1", resolve));
  return { base: `http://127.0.0.1:${server.address().port}`, close: () => new Promise((resolve) => { server.closeAllConnections(); server.close(resolve); }) };
}

function updateEnv(home, releaseBase, registryBase) {
  return {
    ...process.env,
    NO_COLOR: "1",
    CAVEMAN_HOME: home,
    CAVE_BINARY_RELEASE_BASE: releaseBase,
    CAVEMAN_NPM_REGISTRY: registryBase,
    CAVE_SETUP_TIMEOUT: "3",
  };
}

test("update syncs binaries and reports up to date when npm has no newer CLI", async () => {
  const home = mkdtempSync(join(tmpdir(), "cave-update-current-"));
  const releases = await releaseServer();
  const registry = await registryServer(cliVersion);
  const out = await runCli(["update"], updateEnv(home, releases.base, registry.base));
  await releases.close();
  await registry.close();
  assert.equal(out.code, 0, out.stderr);
  assert.equal((out.stderr.match(/· checksum verified/g) ?? []).length, 6);
  assert.match(out.stdout, new RegExp(`up to date — CLI ${cliVersion.replace(/\./g, "\\.")}, binaries ${release}`));
});

test("update exits non-zero and names the npm command when a newer CLI exists", async () => {
  const home = mkdtempSync(join(tmpdir(), "cave-update-behind-"));
  const releases = await releaseServer();
  const registry = await registryServer("99.0.0");
  const out = await runCli(["update"], updateEnv(home, releases.base, registry.base));
  await releases.close();
  await registry.close();
  assert.equal(out.code, 1);
  assert.match(out.stderr, /99\.0\.0 is out — update the CLI/);
  assert.match(out.stderr, /npm install -g @caveman-ai\/cli@latest/);
});

test("update exits non-zero when npm is unreachable but still syncs binaries", async () => {
  const home = mkdtempSync(join(tmpdir(), "cave-update-offline-"));
  const releases = await releaseServer();
  const registry = await registryServer(cliVersion);
  const registryBase = registry.base;
  await registry.close();
  const out = await runCli(["update"], updateEnv(home, releases.base, registryBase));
  await releases.close();
  assert.equal(out.code, 1);
  assert.equal((out.stderr.match(/· checksum verified/g) ?? []).length, 6);
  assert.match(out.stderr, /npm was unreachable — cannot confirm CLI/);
});

test("update rejects extra arguments", async () => {
  const home = mkdtempSync(join(tmpdir(), "cave-update-usage-"));
  const out = await runCli(["update", "--json"], updateEnv(home, "http://127.0.0.1:1", "http://127.0.0.1:1"));
  assert.equal(out.code, 2);
  assert.match(out.stderr, /usage: caveman update/);
});
