/**
 * Cross-language parity test for the localization_f1 grader.
 * Drives the SAME canonical fixture (localization_f1.vectors.json) that the
 * Python test in cloud/optimizer reads, so a TS/Python verdict drift is a hard
 * release-gate failure (the exact bug class that bit exact_match).
 * Run with: node --test packages/evals/tests/localization_f1.parity.runtime.mjs
 */
import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { dirname, join } from "node:path";

import { grade } from "../dist/index.js";

const here = dirname(fileURLToPath(import.meta.url));
const vectors = JSON.parse(readFileSync(join(here, "localization_f1.vectors.json"), "utf8"));

function parseMetric(reason, key) {
  const m = reason.match(new RegExp(`${key}=([0-9.]+)`));
  return m ? Number(m[1]) : null;
}

for (const v of vectors) {
  test(`localization_f1 parity: ${v.name}`, async () => {
    const grader = { type: "localization_f1", reference: v.reference, ...(v.options ?? {}) };
    const r = await grade(grader, v.candidate);
    assert.equal(r.passed, v.expected_passed, `${v.name}: ${r.reason}`);
    if (v.expected_file_f1 !== null && v.expected_file_f1 !== undefined) {
      const fileF1 = parseMetric(r.reason, "file_f1");
      const lineF1 = parseMetric(r.reason, "line_f1");
      assert.ok(
        fileF1 !== null && Math.abs(fileF1 - v.expected_file_f1) < 1e-3,
        `${v.name}: file_f1 ${fileF1} != expected ${v.expected_file_f1}`,
      );
      assert.ok(
        lineF1 !== null && Math.abs(lineF1 - v.expected_line_f1) < 1e-3,
        `${v.name}: line_f1 ${lineF1} != expected ${v.expected_line_f1}`,
      );
    }
  });
}
