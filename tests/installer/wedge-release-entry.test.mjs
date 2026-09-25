// The shared wedge installer (browse/MCP/shrink launchers) refuses a signed
// checksums.txt whose RELEASE entry is not sha256("<pinned tag>\n"). Runs a
// copy of installer.mjs whose release.generated.mjs carries a throwaway key.
import assert from "node:assert/strict";
import { createHash, generateKeyPairSync } from "node:crypto";
import { copyFileSync, existsSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import http from "node:http";
import { once } from "node:events";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";
import test from "node:test";
import { checksumSignatureBundle } from "../../scripts/sign-binary-checksums.mjs";

const root = join(dirname(fileURLToPath(import.meta.url)), "..", "..");
const RELEASE = "bin-v9.9.9";
const NAME = "cave-wedge-test";
const sha256 = (value) => createHash("sha256").update(value).digest("hex");

const { privateKey, publicKey } = generateKeyPairSync("ec", { namedCurve: "P-256" });
const privatePEM = privateKey.export({ type: "pkcs8", format: "pem" });
const copy = mkdtempSync(join(tmpdir(), "cave-wedge-"));
copyFileSync(join(root, "packages/shared/binary-installer/installer.mjs"), join(copy, "installer.mjs"));
writeFileSync(join(copy, "release.generated.mjs"), [
  `export const BINARY_RELEASE = ${JSON.stringify(RELEASE)};`,
  `export const BINARY_RELEASE_BASE_DEFAULT = "http://127.0.0.1:1";`,
  `export const BINARY_SIGNING_PUBKEY = ${JSON.stringify(publicKey.export({ type: "spki", format: "pem" }))};`,
  "",
].join("\n"));
const { ensureBinary, targetPlatform } = await import(pathToFileURL(join(copy, "installer.mjs")).href);

const { os, arch } = targetPlatform();
const artifact = `${NAME}_${os}_${arch}`;
const body = "#!/bin/sh\nexit 0\n";
let files = {};
const homes = [];
const server = http.createServer((request, response) => {
  const file = files[request.url];
  response.statusCode = file === undefined ? 404 : 200;
  response.end(file);
}).listen(0, "127.0.0.1");
await once(server, "listening");
test.after(() => {
  server.close();
  for (const dir of [copy, ...homes]) rmSync(dir, { recursive: true, force: true });
});

async function install(releaseLine) {
  const lines = [`${sha256(body)}  ${artifact}`];
  if (releaseLine !== null) lines.push(`${sha256(`${releaseLine}\n`)}  RELEASE`);
  const checksums = `${lines.join("\n")}\n`;
  const prefix = `/${RELEASE}`;
  files = {
    [`${prefix}/checksums.txt`]: checksums,
    [`${prefix}/checksums.txt.keysig`]: JSON.stringify(checksumSignatureBundle(Buffer.from(checksums), privatePEM)),
    [`${prefix}/${artifact}`]: body,
  };
  const home = mkdtempSync(join(tmpdir(), "cave-wedge-home-"));
  homes.push(home);
  const saved = { CAVEMAN_HOME: process.env.CAVEMAN_HOME, CAVE_BINARY_RELEASE_BASE: process.env.CAVE_BINARY_RELEASE_BASE };
  process.env.CAVEMAN_HOME = home;
  process.env.CAVE_BINARY_RELEASE_BASE = `http://127.0.0.1:${server.address().port}`;
  try {
    return { home, result: await ensureBinary({ name: NAME, envVar: "CAVE_WEDGE_TEST_UNSET" }).then((bin) => ({ bin }), (error) => ({ error })) };
  } finally {
    for (const [key, value] of Object.entries(saved)) value === undefined ? delete process.env[key] : process.env[key] = value;
  }
}

function assertRefused({ home, result }) {
  assert.match(result.error?.message ?? "", /manifest (is not for bin-v9\.9\.9|does not contain RELEASE)/);
  assert.equal(existsSync(join(home, "bin")), false, "nothing may be written before the manifest is accepted");
}

test("signed manifest without a RELEASE entry is refused", async () => {
  assertRefused(await install(null));
});

test("signed manifest naming another release is refused", async () => {
  assertRefused(await install("bin-v1.1.7"));
});

test("signed manifest naming the pinned release installs", async () => {
  const { home, result } = await install(RELEASE);
  assert.equal(result.error, undefined);
  assert.equal(result.bin, join(home, "bin", os === "win32" ? `${NAME}.exe` : NAME));
  assert.equal(readFileSync(result.bin, "utf8"), body);
});
