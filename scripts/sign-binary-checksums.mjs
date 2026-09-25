#!/usr/bin/env node
import { createHash, createPrivateKey, createPublicKey, sign, verify } from "node:crypto";
import { readFileSync, writeFileSync } from "node:fs";
import { resolve } from "node:path";
import { fileURLToPath } from "node:url";

export function checksumSignatureBundle(checksums, privateKeyPEM) {
  const privateKey = createPrivateKey(privateKeyPEM);
  return {
    mediaType: "application/vnd.dev.sigstore.bundle.v0.3+json",
    messageSignature: {
      messageDigest: {
        algorithm: "SHA2_256",
        digest: createHash("sha256").update(checksums).digest("base64"),
      },
      signature: sign("sha256", checksums, privateKey).toString("base64"),
    },
  };
}

export function verifyChecksumSignatureBundle(checksums, bundle, publicKeyPEM) {
  const digest = createHash("sha256").update(checksums).digest("base64");
  return bundle?.mediaType === "application/vnd.dev.sigstore.bundle.v0.3+json" &&
    bundle?.messageSignature?.messageDigest?.algorithm === "SHA2_256" &&
    bundle?.messageSignature?.messageDigest?.digest === digest &&
    verify(
      "sha256",
      checksums,
      createPublicKey(publicKeyPEM),
      Buffer.from(bundle?.messageSignature?.signature ?? "", "base64"),
    );
}

// The CLI refuses a manifest (bin-v2.0.0 on) whose RELEASE entry is not the
// sha256 of "<its pinned tag>\n"; refuse to sign one that would not pass.
export function assertManifestNamesRelease(checksums, release) {
  const want = createHash("sha256").update(`${release}\n`).digest("hex");
  const entries = String(checksums).split("\n").filter((line) => line.endsWith("  RELEASE"));
  if (entries.length !== 1 || entries[0] !== `${want}  RELEASE`) {
    throw new Error(`checksums.txt must carry exactly one RELEASE entry for ${release}: ${want}  RELEASE`);
  }
}

function normalizePublicKey(value) {
  return createPublicKey(value).export({ type: "spki", format: "pem" }).toString();
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try {
    const [checksumsPath, outputPath, publicKeyPath, release] = process.argv.slice(2);
    const privateKeyPEM = process.env.CAVEMAN_BINARY_SIGNING_PRIVATE_KEY_PEM;
    if (!checksumsPath || !outputPath || !publicKeyPath || !release) {
      throw new Error("usage: sign-binary-checksums.mjs <checksums.txt> <output.keysig> <public-key.pem> <release-tag>");
    }
    if (!privateKeyPEM) throw new Error("CAVEMAN_BINARY_SIGNING_PRIVATE_KEY_PEM is required");
    const checksums = readFileSync(checksumsPath);
    assertManifestNamesRelease(checksums, release);
    const publicKeyPEM = readFileSync(publicKeyPath, "utf8");
    if (normalizePublicKey(privateKeyPEM) !== normalizePublicKey(publicKeyPEM)) {
      throw new Error("binary signing private key does not match committed public key");
    }
    const bundle = checksumSignatureBundle(checksums, privateKeyPEM);
    if (!verifyChecksumSignatureBundle(checksums, bundle, publicKeyPEM)) {
      throw new Error("generated checksum signature failed local verification");
    }
    writeFileSync(outputPath, `${JSON.stringify(bundle)}\n`, { mode: 0o600 });
  } catch (error) {
    process.stderr.write(`${error.message}\n`);
    process.exit(1);
  }
}
