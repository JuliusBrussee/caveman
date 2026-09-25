// Signed binary-release fixtures. The real signing key never leaves the
// binary-release environment, so tests run a copy of the built CLI whose
// embedded public key is a throwaway one and sign their own manifests.
import { createHash, generateKeyPairSync } from "node:crypto";
import { cpSync, mkdtempSync, readFileSync, realpathSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { releaseArtifactNames } from "../../../scripts/build-release-binaries.mjs";
import { checksumSignatureBundle } from "../../../scripts/sign-binary-checksums.mjs";

const cliRoot = join(dirname(fileURLToPath(import.meta.url)), "..");
export const pinnedRelease = readFileSync(join(cliRoot, "BINARY_RELEASE"), "utf8").trim();
export const binaryBody = "#!/bin/sh\nexit 0\n";
const sha256 = (value) => createHash("sha256").update(value).digest("hex");

// Every artifact of the release matrix, all with binaryBody's digest, plus the
// RELEASE entry naming `named` (omitted when null).
export function releaseManifest(named = pinnedRelease) {
  const lines = releaseArtifactNames().sort().map((name) => `${sha256(binaryBody)}  ${name}`);
  if (named !== null) lines.push(`${sha256(`${named}\n`)}  RELEASE`);
  return `${lines.join("\n")}\n`;
}

export function signedReleaseCli(release = pinnedRelease) {
  const { privateKey, publicKey } = generateKeyPairSync("ec", { namedCurve: "P-256" });
  const root = realpathSync(mkdtempSync(join(tmpdir(), "cave-release-cli-")));
  cpSync(join(cliRoot, "dist"), join(root, "dist"), { recursive: true });
  cpSync(join(cliRoot, "package.json"), join(root, "package.json"));
  writeFileSync(join(root, "dist", "binaries.generated.js"), [
    `export const BINARY_RELEASE = ${JSON.stringify(release)};`,
    `export const BINARY_RELEASE_BASE_DEFAULT = "http://127.0.0.1:1";`,
    `export const BINARY_SIGNING_PUBKEY = ${JSON.stringify(publicKey.export({ type: "spki", format: "pem" }))};`,
    "",
  ].join("\n"));
  const privatePEM = privateKey.export({ type: "pkcs8", format: "pem" });
  return {
    cli: join(root, "dist", "index.js"),
    release,
    sign: (checksums) => JSON.stringify(checksumSignatureBundle(Buffer.from(checksums), privatePEM)),
  };
}
