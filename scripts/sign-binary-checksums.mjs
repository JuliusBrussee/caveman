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

function normalizePublicKey(value) {
  return createPublicKey(value).export({ type: "spki", format: "pem" }).toString();
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try {
    const [checksumsPath, outputPath, publicKeyPath] = process.argv.slice(2);
    const privateKeyPEM = process.env.CAVEMAN_BINARY_SIGNING_PRIVATE_KEY_PEM;
    if (!checksumsPath || !outputPath || !publicKeyPath) {
      throw new Error("usage: sign-binary-checksums.mjs <checksums.txt> <output.keysig> <public-key.pem>");
    }
    if (!privateKeyPEM) throw new Error("CAVEMAN_BINARY_SIGNING_PRIVATE_KEY_PEM is required");
    const checksums = readFileSync(checksumsPath);
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
