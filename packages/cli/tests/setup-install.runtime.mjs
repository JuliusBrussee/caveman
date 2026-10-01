import test from "node:test";
import assert from "node:assert";
import { spawn } from "node:child_process";
import { createServer } from "node:http";
import { mkdtempSync, readdirSync, statSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";
import { binaryBody, releaseManifest, signedReleaseCli } from "./_binary-release.mjs";

const { cli, release, sign } = signedReleaseCli();
const checksums = releaseManifest(release);
const signature = sign(checksums);

function runCli(argv, env, prefix = []) {
  return new Promise((resolve, reject) => {
    const child = spawn(process.execPath, [...prefix, cli, ...argv], { env });
    let stdout = "";
    let stderr = "";
    child.stdout.on("data", (chunk) => (stdout += chunk));
    child.stderr.on("data", (chunk) => (stderr += chunk));
    child.on("exit", (code) => resolve({ code, stdout, stderr }));
    child.on("error", reject);
  });
}

// Same digest, signature over other bytes.
function badSignature() {
  const bundle = JSON.parse(signature);
  bundle.messageSignature.signature = JSON.parse(sign("other")).messageSignature.signature;
  return JSON.stringify(bundle);
}

async function releaseServer(mode = "valid", manifest = checksums) {
  let requests = 0;
  const server = createServer((request, response) => {
    requests++;
    const path = request.url ?? "";
    if (path === `/${release}/checksums.txt`) {
      response.end(manifest);
      return;
    }
    if (path === `/${release}/checksums.txt.keysig`) {
      response.end(mode === "bad-signature" ? badSignature() : sign(manifest));
      return;
    }
    if (path.startsWith(`/${release}/`)) {
      if (mode === "stall") {
        response.writeHead(200, { "content-type": "application/octet-stream" });
        response.write(binaryBody.slice(0, 4));
        return;
      }
      response.end(mode === "bad-binary" ? "tampered\n" : binaryBody);
      return;
    }
    response.writeHead(404).end();
  });
  await new Promise((resolve) => server.listen(0, "127.0.0.1", resolve));
  const address = server.address();
  return {
    base: `http://127.0.0.1:${address.port}`,
    requests: () => requests,
    close: async () => {
      server.closeAllConnections();
      await new Promise((resolve) => server.close(resolve));
    },
  };
}

function setupEnv(home, base, timeout = "3") {
  return {
    ...process.env,
    NO_COLOR: "1",
    CAVEMAN_HOME: home,
    CAVE_BINARY_RELEASE_BASE: base,
    CAVE_SETUP_TIMEOUT: timeout,
  };
}

function partials(home) {
  try {
    return readdirSync(join(home, "bin")).filter((name) => name.endsWith(".part"));
  } catch {
    return [];
  }
}

test("setup --install verifies, installs, emits clean JSON, then works offline", async () => {
  const home = mkdtempSync(join(tmpdir(), "cave-setup-install-"));
  const server = await releaseServer();
  const first = await runCli(["setup", "--install", "--json"], setupEnv(home, server.base));
  assert.equal(first.code, 0, first.stderr);
  const result = JSON.parse(first.stdout);
  assert.equal(result.release, release);
  assert.equal(result.binaries.length, 6);
  assert.ok(result.binaries.every((item) => item.status === "installed"));
  assert.ok(result.binaries.every((item) => (statSync(item.path).mode & 0o777) === 0o755));
  assert.equal(statSync(join(home, "bin", ".bin-manifest.json")).mode & 0o777, 0o600);
  assert.match(first.stderr, /checksum verified/);
  assert.deepEqual(partials(home), []);
  assert.ok(server.requests() >= 8);
  await server.close();

  const second = await runCli(["setup", "--install"], setupEnv(home, server.base));
  assert.equal(second.code, 0, second.stderr);
  assert.equal((second.stdout.match(/already installed · checksum verified/g) ?? []).length, 6);
  assert.equal(second.stderr, "");
});

test("setup --install rejects a bad manifest signature before writing binaries", async () => {
  const home = mkdtempSync(join(tmpdir(), "cave-setup-signature-"));
  const server = await releaseServer("bad-signature");
  const out = await runCli(["setup", "--install"], setupEnv(home, server.base));
  await server.close();
  assert.notEqual(out.code, 0);
  assert.match(out.stderr, /signature check failed for checksums\.txt — refusing to install; partial download deleted/);
  assert.deepEqual(partials(home), []);
});

test("setup --install refuses a validly signed manifest for another release", async () => {
  for (const [label, manifest] of [["older release", releaseManifest("bin-v1.9.9")], ["unnamed", releaseManifest(null)]]) {
    const home = mkdtempSync(join(tmpdir(), "cave-setup-release-"));
    const server = await releaseServer("valid", manifest);
    const out = await runCli(["setup", "--install"], setupEnv(home, server.base));
    await server.close();
    assert.notEqual(out.code, 0, label);
    assert.match(out.stderr, new RegExp(`signature check failed for checksums\\.txt \\(manifest is not signed for release ${release}\\)`), label);
    assert.deepEqual(readdirSync(join(home, "bin")), [], label);
  }
});

test("signed manifest must name the pinned release from bin-v2.0.0 on", async () => {
  const { parseSignedChecksums } = await import(pathToFileURL(join(dirname(fileURLToPath(import.meta.url)), "..", "dist", "index.js")).href);
  const licenses = `${"b".repeat(64)}  LICENSE\n${"c".repeat(64)}  NOTICE\n`;
  const named = releaseManifest("bin-v2.0.0") + licenses;
  const unnamed = releaseManifest(null) + licenses;
  // Matching name passes, appended license entries included.
  assert.equal(parseSignedChecksums(named, "bin-v2.0.0").get("NOTICE"), "c".repeat(64));
  // A manifest signed for another release is refused, older or newer.
  assert.throws(() => parseSignedChecksums(named, "bin-v2.1.0"), /not signed for release bin-v2\.1\.0/);
  assert.throws(() => parseSignedChecksums(releaseManifest("bin-v2.1.0"), "bin-v2.0.0"), /not signed for release/);
  // No name at all: refused for bin-v2.0.0 and later (prereleases of it too)...
  for (const pin of ["bin-v2.0.0", "bin-v2.0.0-rc.1", "bin-v2.0.1", "bin-v3.0.0"]) {
    assert.throws(() => parseSignedChecksums(unnamed, pin), /not signed for release/, pin);
  }
  // ...accepted for the releases signed before the name existed.
  assert.equal(parseSignedChecksums(unnamed, "bin-v1.1.8").size, 38);
  assert.throws(() => parseSignedChecksums(named, "bin-v1.1.8"), /not signed for release/);
});

test("setup --install deletes a checksum-mismatched partial", async () => {
  const home = mkdtempSync(join(tmpdir(), "cave-setup-checksum-"));
  const server = await releaseServer("bad-binary");
  const out = await runCli(["setup", "--install"], setupEnv(home, server.base));
  await server.close();
  assert.notEqual(out.code, 0);
  assert.match(out.stderr, /signature check failed for caveman-proxy_/);
  assert.deepEqual(partials(home), []);
});

test("setup --install reports unreachable release without partials", async () => {
  const home = mkdtempSync(join(tmpdir(), "cave-setup-unreachable-"));
  const server = await releaseServer();
  const base = server.base;
  await server.close();
  const out = await runCli(["setup", "--install"], setupEnv(home, base));
  assert.notEqual(out.code, 0);
  assert.match(out.stderr, /^binary download unreachable — agents still launch; traffic is NOT compressed or metered/m);
  assert.deepEqual(partials(home), []);
});

test("setup --install times out stalled artifacts and deletes partials", async () => {
  const home = mkdtempSync(join(tmpdir(), "cave-setup-stall-"));
  const server = await releaseServer("stall");
  const out = await runCli(["setup", "--install"], setupEnv(home, server.base, "1"));
  await server.close();
  assert.notEqual(out.code, 0);
  assert.match(out.stderr, /^binary download stalled after 1s — nothing installed; agents still launch, traffic is NOT compressed or metered/m);
  assert.deepEqual(partials(home), []);
});

test("setup --install rejects unsupported platforms before network", async () => {
  const home = mkdtempSync(join(tmpdir(), "cave-setup-platform-"));
  const preload = "data:text/javascript,Object.defineProperty(process,'platform',{value:'freebsd'})";
  const out = await runCli(
    ["setup", "--install"],
    setupEnv(home, "http://127.0.0.1:1"),
    ["--import", preload],
  );
  assert.notEqual(out.code, 0);
  assert.match(out.stderr, /^no prebuilt binary for freebsd\//m);
  assert.deepEqual(partials(home), []);
});
