import assert from "node:assert/strict";
import test from "node:test";
import { renderLearnPlan } from "../dist/index.js";

const base = {
  schema: "caveman.learn.v1",
  basis: "inferred",
  cave_score: { score: 88, basis: "inferred", scope: "local_setup" },
  sessions_by_source: {},
  sinks: [],
};

test("learn state 3 names real three-session threshold and prints no score", () => {
  const text = renderLearnPlan({ ...base, sessions_scanned: 0 }, { report: "/tmp/report.html" });
  assert.match(text, /no Claude Code or Codex sessions found in the last 30d/);
  assert.match(text, /repeated across ≥3 sessions/);
  assert.doesNotMatch(text, /Setup Score/);
  assert.doesNotMatch(text, /\$/);
});

test("learn state 2 reports thin history without a zero-score", () => {
  const text = renderLearnPlan({
    ...base,
    sessions_scanned: 2,
    sessions_by_source: { claude: 2 },
  }, { report: "/tmp/report.html" });
  assert.match(text, /2 sessions scanned · no block repeated across ≥3 sessions yet/);
  assert.doesNotMatch(text, /Setup Score/);
  assert.doesNotMatch(text, /\$/);
});

test("learn state 1 renders setup scope, sink ids/classes, diff, and loop closer", () => {
  const text = renderLearnPlan({
    ...base,
    sessions_scanned: 3,
    sessions_by_source: { claude: 2, codex: 1 },
    sinks: [{
      sink_id: "recurring_context:abc",
      title: "Repeated deployment preamble",
      class: "recurring_context",
      basis: "inferred",
      tokens_per_turn: 420,
      tokens_per_day_rate: 1260,
      suggestion: "Offload after consent.",
    }],
  }, {
    report: "/tmp/report.html",
    diff: { days: 12, gone: 2, back: 1, fresh: 1 },
  });
  assert.match(text, /Setup Score 88  ·  basis: inferred \(local sessions, not billed spend\)/);
  assert.match(text, /Cave Score \(org\)/);
  assert.match(text, /recurring_context:abc  ·  recurring_context/);
  assert.match(text, /since your last run 12d ago: 2 moves gone · 1 back · 1 new/);
  assert.match(text, /caveman tools skills install caveman-learn/);
  assert.ok(text.trimEnd().endsWith("report: /tmp/report.html"), "report path must print last");
  assert.doesNotMatch(text, /\b(?:measured|verified)\b/i);
  assert.doesNotMatch(text, /\$/);
});

test("learn Markdown preserves basis and per-day units", () => {
  const text = renderLearnPlan({
    ...base,
    sessions_scanned: 3,
    sinks: [{
      sink_id: "recurring_context:abc",
      title: "Repeated deployment preamble",
      class: "recurring_context",
      basis: "inferred",
      tokens_per_turn: 420,
      tokens_per_day_rate: 1260,
    }],
  }, { markdown: true, report: "/tmp/report.html" });
  assert.match(text, /^## Setup Score 88 — basis: inferred/m);
  assert.match(text, /tokens\/day · basis: inferred/);
  assert.doesNotMatch(text, /\$/);
});
