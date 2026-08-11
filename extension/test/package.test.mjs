import assert from "node:assert/strict";
import { copyFileSync, mkdirSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import test from "node:test";

import { SHIPPABLE_FILES, verifyExtensionRoot } from "../scripts/verify-extension-stage.mjs";

const extensionRoot = resolve(import.meta.dirname, "..");

test("store package allowlist covers every runtime reference", () => {
  const result = verifyExtensionRoot(extensionRoot);
  assert.equal(result.manifest.manifest_version, 3);
  assert.equal(result.files.length, SHIPPABLE_FILES.length);
});

test("stage verifier rejects files outside explicit allowlist", () => {
  const stage = mkdtempSync(join(tmpdir(), "caveman-extension-stage-"));
  try {
    for (const file of SHIPPABLE_FILES) {
      const target = join(stage, file);
      mkdirSync(dirname(target), { recursive: true });
      copyFileSync(join(extensionRoot, file), target);
    }
    verifyExtensionRoot(stage, { exact: true });
    writeFileSync(join(stage, "unreviewed.js"), "alert('unexpected')\n");
    assert.throws(
      () => verifyExtensionRoot(stage, { exact: true }),
      /staged allowlist mismatch; unexpected=unreviewed\.js/,
    );
  } finally {
    rmSync(stage, { recursive: true, force: true });
  }
});
