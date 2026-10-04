// Unit tests for the caveman directive builders (src/directive.js). Pure string
// logic — no chrome.*, no DOM — so it runs under plain `node --test`.
import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { createRequire } from "node:module";

const require = createRequire(import.meta.url);
const D = require("../src/directive.js");

test("buildPrimer carries the activation + the selected mode clause", () => {
  const caveman = D.buildPrimer("caveman");
  assert.ok(caveman.startsWith("[Caveman mode is ON"), "primer must open with the activation");
  assert.ok(caveman.includes("Reply to EVERY message like a smart caveman"), "primer keeps the caveman framing");
  assert.ok(caveman.includes("Mode CAVEMAN"), "caveman primer names its mode");
  assert.ok(caveman.includes("when user wants compact structured output"), "primer scopes the structured-data companion guidance");
  assert.ok(caveman.includes("did not request a specific format/protocol"), "primer preserves user-requested formats");
  assert.ok(caveman.includes("never use TOON for tool-call arguments"), "primer protects tool-call protocols");
  assert.match(caveman, /one status line per phase, nothing between routine calls/);
  assert.doesNotMatch(caveman, /no narrating/i);
  for (const mode of D.MODES) {
    const primer = D.buildPrimer(mode);
    assert.ok(primer.includes(`Mode ${mode.toUpperCase()}:`), `${mode} primer names its mode`);
    assert.equal((primer.match(/```/g) || []).length % 2, 0, "primer must not contain an unmatched code fence");
    assert.ok(primer.trimEnd().endsWith("]"), "primer is a single closed bracket directive");
    assert.equal(primer.indexOf("]"), primer.length - 1, "no early bracket close");
  }
  assert.match(D.buildPrimer("megacave"), /文言文/);
});

test("each mode clause opens with its skill's thesis line", () => {
  for (const mode of D.MODES) {
    const skill = readFileSync(new URL(`../../skills/${mode}/SKILL.md`, import.meta.url), "utf8").split("\n");
    const thesis = skill.slice(skill.indexOf(`# ${mode}`) + 1).find((line) => line.trim() !== "");
    assert.ok(D.buildPrimer(mode).includes(`Mode ${mode.toUpperCase()}: ${thesis}`), `${mode} clause drifted from skills/${mode}/SKILL.md`);
  }
});

test("buildPrimer fails safe to CAVEMAN on an unknown mode", () => {
  assert.ok(D.buildPrimer("banana").includes("Mode CAVEMAN"));
  assert.ok(D.buildPrimer(undefined).includes("Mode CAVEMAN"));
  assert.ok(D.buildPrimer("toString").includes("Mode CAVEMAN"));
});

test("buildReminder is a short stay-in-mode nudge naming the mode", () => {
  assert.equal(D.buildReminder("caveman"), "[stay in caveman mode — CAVEMAN]");
  assert.equal(D.buildReminder("ultracave"), "[stay in caveman mode — ULTRACAVE]");
  assert.equal(D.buildReminder("megacave"), "[stay in caveman mode — MEGACAVE]");
  assert.equal(D.buildReminder("nonsense"), "[stay in caveman mode — CAVEMAN]");
});

test("a reminder is much shorter than the primer (the smart-hybrid saving)", () => {
  for (const mode of D.MODES) assert.ok(D.buildReminder(mode).length * 4 < D.buildPrimer(mode).length);
});

test("isPrefixed detects both primer and reminder, tolerating leading space/newlines", () => {
  assert.equal(D.isPrefixed(D.buildPrimer("caveman") + "\n\nhello"), true);
  assert.equal(D.isPrefixed("\n  " + D.buildReminder("megacave") + "\n\nhi"), true);
  assert.equal(D.isPrefixed("just a normal prompt"), false);
  assert.equal(D.isPrefixed(""), false);
  assert.equal(D.isPrefixed(null), false);
  assert.equal(D.isPrefixed(undefined), false);
});

test("normMode clamps to the three modes and maps saved lite/full/ultra values", () => {
  assert.deepEqual(D.MODES, ["caveman", "ultracave", "megacave"]);
  for (const mode of D.MODES) assert.equal(D.normMode(mode), mode);
  assert.equal(D.normMode("lite"), "caveman");
  assert.equal(D.normMode("full"), "caveman");
  assert.equal(D.normMode("ultra"), "ultracave");
  assert.equal(D.normMode("WAT"), "caveman");
  assert.equal(D.normMode("__proto__"), "caveman");
});
