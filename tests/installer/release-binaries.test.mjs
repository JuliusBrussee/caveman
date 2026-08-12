import assert from "node:assert/strict";
import { generateKeyPairSync } from "node:crypto";
import test from "node:test";

import {
  releaseArtifactName,
  releaseArtifactNames,
} from "../../scripts/build-release-binaries.mjs";
import {
  checksumSignatureBundle,
  verifyChecksumSignatureBundle,
} from "../../scripts/sign-binary-checksums.mjs";

test("release matrix contains six binaries for six OS/architecture targets", () => {
  const names = releaseArtifactNames();
  assert.equal(names.length, 36);
  assert.equal(new Set(names).size, 36);
  assert.ok(names.includes("caveman-proxy_win32_amd64"));
  assert.ok(names.includes("caveman-shrink_win32_arm64"));
  assert.equal(releaseArtifactName("cavemem", "windows", "amd64"), "cavemem_win32_amd64");
});

test("checksum signer emits bundle accepted by pinned-key verifier contract", () => {
  const { privateKey, publicKey } = generateKeyPairSync("ec", { namedCurve: "P-256" });
  const privatePEM = privateKey.export({ type: "pkcs8", format: "pem" });
  const publicPEM = publicKey.export({ type: "spki", format: "pem" });
  const checksums = Buffer.from(`${"a".repeat(64)}  caveman-proxy_win32_amd64\n`);
  const bundle = checksumSignatureBundle(checksums, privatePEM);
  assert.equal(verifyChecksumSignatureBundle(checksums, bundle, publicPEM), true);
  assert.equal(verifyChecksumSignatureBundle(Buffer.from("changed"), bundle, publicPEM), false);
});
