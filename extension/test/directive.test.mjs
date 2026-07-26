// Unit tests for the caveman directive builders (src/directive.js). Pure string
// logic — no chrome.*, no DOM — so it runs under plain `node --test`.
import { test } from "node:test";
import assert from "node:assert/strict";
import { createRequire } from "node:module";

const require = createRequire(import.meta.url);
const D = require("../src/directive.js");

test("buildPrimer carries the activation + the selected intensity clause", () => {
  const full = D.buildPrimer("full");
  assert.ok(full.startsWith("[Caveman mode is ON"), "primer must open with the activation");
  assert.ok(full.includes("Intensity FULL"), "full primer names its level");
  assert.ok(full.includes("when user wants compact structured output"), "primer scopes the structured-data companion guidance");
  assert.ok(full.includes("did not request a specific format/protocol"), "primer preserves user-requested formats");
  assert.ok(full.includes("never use TOON for tool-call arguments"), "primer protects tool-call protocols");
  assert.equal((full.match(/```/g) || []).length % 2, 0, "primer must not contain an unmatched code fence");
  assert.ok(full.trimEnd().endsWith("]"), "primer is a single closed bracket directive");
  assert.ok(D.buildPrimer("lite").includes("Intensity LITE"));
  assert.ok(D.buildPrimer("ultra").includes("Intensity ULTRA"));
});

test("buildPrimer fails safe to FULL on an unknown level", () => {
  assert.ok(D.buildPrimer("banana").includes("Intensity FULL"));
  assert.ok(D.buildPrimer(undefined).includes("Intensity FULL"));
});

test("buildReminder is a short stay-in-mode nudge naming the level", () => {
  assert.equal(D.buildReminder("full"), "[stay in caveman mode — FULL]");
  assert.equal(D.buildReminder("ultra"), "[stay in caveman mode — ULTRA]");
  assert.equal(D.buildReminder("nonsense"), "[stay in caveman mode — FULL]");
});

test("a reminder is much shorter than the primer (the smart-hybrid saving)", () => {
  assert.ok(D.buildReminder("full").length * 4 < D.buildPrimer("full").length);
});

test("isPrefixed detects both primer and reminder, tolerating leading space/newlines", () => {
  assert.equal(D.isPrefixed(D.buildPrimer("full") + "\n\nhello"), true);
  assert.equal(D.isPrefixed("\n  " + D.buildReminder("lite") + "\n\nhi"), true);
  assert.equal(D.isPrefixed("just a normal prompt"), false);
  assert.equal(D.isPrefixed(""), false);
  assert.equal(D.isPrefixed(null), false);
  assert.equal(D.isPrefixed(undefined), false);
});

test("normLevel clamps to the known set", () => {
  assert.equal(D.normLevel("lite"), "lite");
  assert.equal(D.normLevel("full"), "full");
  assert.equal(D.normLevel("ultra"), "ultra");
  assert.equal(D.normLevel("WAT"), "full");
  assert.deepEqual(D.LEVELS, ["lite", "full", "ultra"]);
});
