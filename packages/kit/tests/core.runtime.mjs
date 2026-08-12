/**
 * Runtime guards for the headless honesty core. Run with:
 *   node --test public/kit/tests/core.runtime.mjs
 * Imports compiled dist (the package "test" script builds first).
 */
import { test } from "node:test";
import assert from "node:assert/strict";

import { normalizeBasis, isVerified, basisLabel } from "../dist/core.js";

test("known bases pass through unchanged", () => {
  for (const b of ["measured", "inferred", "verified", "realized"]) {
    assert.equal(normalizeBasis(b), b);
  }
});

test("unknown basis fails closed to inferred and never to verified", () => {
  for (const bad of ["VERIFIED", "verified ", "trust-me", "", null, undefined, 42, {}]) {
    assert.equal(normalizeBasis(bad), "inferred", `normalizeBasis(${JSON.stringify(bad)})`);
    assert.equal(isVerified(bad), false);
    assert.notEqual(basisLabel(bad), "Verified");
  }
});

test("isVerified is true only for an exact verified", () => {
  assert.equal(isVerified("verified"), true);
  assert.equal(isVerified("Verified"), false);
  assert.equal(isVerified("realized"), false);
});

test("basisLabel maps each known basis", () => {
  assert.equal(basisLabel("measured"), "Measured");
  assert.equal(basisLabel("inferred"), "Inferred");
  assert.equal(basisLabel("verified"), "Verified");
  assert.equal(basisLabel("realized"), "Realized");
});
