/**
 * Cross-language parity test for the 7 langevals-ported graders + the new
 * exact_match knobs.
 * Drives the SAME canonical fixture (langevals-port.vectors.json) that the Python
 * test in cloud/optimizer reads, so a TS/Python verdict drift is a hard
 * release-gate failure (the exact bug class that bit exact_match).
 * Run with: node --test packages/evals/tests/langevals-port.parity.runtime.mjs
 *
 * Vector wire shape: {name, type, candidate, options, expected_passed} plus, where
 * the grader reached its scoring stage, expected_score (the Python `score` field,
 * which the TS side reports inside its reason as `score=`), and for a failing
 * no_pii vector, expected_pii — the entity KINDS both sides name, in pinned order.
 * `options` holds every grader field: TS spreads them onto the grader object;
 * Python passes them as `options` (with options.expected / options.reference also
 * handed to grade()'s positional reference argument). See the Python mirror at
 * cloud/optimizer/tests/test_langevals_port_parity.py.
 */
import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { dirname, join } from "node:path";

import { grade } from "../dist/index.js";

const here = dirname(fileURLToPath(import.meta.url));
const vectors = JSON.parse(readFileSync(join(here, "langevals-port.vectors.json"), "utf8"));

// Scores are compared with a 1e-3 tolerance, not for equality: Python rounds with
// round(x, 4) (half-to-even) and JS formats with toFixed(4) (half-away-from-zero),
// so a dyadic-rational score can legitimately differ in the 4th decimal. Any real
// algorithmic divergence is orders of magnitude larger than that.
const SCORE_TOLERANCE = 1e-3;

function parseScore(reason) {
  const m = reason.match(/score=([0-9.]+)/);
  return m ? Number(m[1]) : null;
}

function parsePiiEntities(reason) {
  const m = reason.match(/^no_pii: detected (.+)$/);
  return m ? m[1].split(", ") : null;
}

test("langevals-port vectors fixture is populated", () => {
  assert.ok(Array.isArray(vectors) && vectors.length >= 60, `expected >=60 vectors, got ${vectors.length}`);
});

for (const v of vectors) {
  test(`langevals-port parity: ${v.name}`, async () => {
    const grader = { type: v.type, ...(v.options ?? {}) };
    const r = await grade(grader, v.candidate);
    assert.equal(r.passed, v.expected_passed, `${v.name}: ${r.reason}`);
    if (v.expected_score !== undefined && v.expected_score !== null) {
      const score = parseScore(r.reason);
      assert.ok(
        score !== null && Math.abs(score - v.expected_score) < SCORE_TOLERANCE,
        `${v.name}: score ${score} != expected ${v.expected_score} (${r.reason})`,
      );
    }
    if (v.expected_pii !== undefined && v.expected_pii !== null) {
      assert.deepEqual(
        parsePiiEntities(r.reason),
        v.expected_pii,
        `${v.name}: detected entities differ (${r.reason})`,
      );
    }
  });
}
