import assert from "node:assert/strict";
import test from "node:test";
import { buildLearnTuiModel, renderLearnPlan } from "../dist/index.js";

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

test("learn state 1 defaults to a short summary with grouped recurring context", () => {
  const plan = {
    ...base,
    sessions_scanned: 3,
    sessions_by_source: { claude: 2, codex: 1 },
    sinks: [
      {
        sink_id: "config_tax:baseline",
        practice_id: "context-compression",
        title: "Agent config loads ~6,370 tokens into every turn",
        class: "load_bearing",
        basis: "observed_local",
        tokens_per_turn: 6370,
        tokens_per_day_rate: 25_000,
      },
      {
        sink_id: "claude_md_weight:project",
        practice_id: "context-compression",
        title: "Project CLAUDE.md is 243 lines (~4,623 tokens) loaded every turn",
        class: "reducible",
        basis: "observed_local",
        tokens_per_turn: 4623,
        tokens_per_day_rate: 20_000,
        suggestion: "Trim to the sections actually used.",
      },
      {
        sink_id: "recurring_context:abc",
        practice_id: "prompt-prefix-stability",
        title: "A ~623-token context block was re-established across 75 sessions",
        class: "recurring_context",
        basis: "observed_local",
        tokens_per_turn: 1,
        tokens_per_day_rate: 1260,
        evidence: { block_tokens: 623, recurrence_sessions: 75 },
      },
      {
        sink_id: "recurring_context:def",
        practice_id: "prompt-prefix-stability",
        title: "A ~543-token context block was re-established across 60 sessions",
        class: "recurring_context",
        basis: "observed_local",
        tokens_per_turn: 1,
        tokens_per_day_rate: 1000,
        evidence: { block_tokens: 543, recurrence_sessions: 60 },
      },
    ],
  };
  const text = renderLearnPlan(plan, {
    report: "/tmp/report.html",
    diff: { days: 12, gone: 2, back: 1, fresh: 1 },
  });
  assert.match(text, /Setup Score 88\/100/);
  assert.match(text, /local setup · inferred · not billed spend · separate from org Cave Score/);
  assert.match(text, /top moves/);
  assert.match(text, /Project CLAUDE\.md/);
  assert.match(text, /2 context blocks repeat across sessions/);
  assert.match(text, /largest ~623 tokens · up to 75 sessions/);
  assert.match(text, /protected\s+Agent config loads ~6,370 tokens into every turn/);
  assert.match(text, /since your last run 12d ago: 2 moves gone · 1 back · 1 new/);
  assert.match(text, /caveman learn implement/);
  assert.match(text, /caveman learn --all/);
  assert.doesNotMatch(text, /recurring_context:abc/);
  assert.doesNotMatch(text, /practice:/);
  assert.ok(text.trimEnd().split("\n").length <= 18, "default learn view must stay bounded");
  assert.ok(text.trimEnd().endsWith("report: /tmp/report.html"), "report path must print last");
  assert.doesNotMatch(text, /\$/);

  const model = buildLearnTuiModel(plan, {
    report: "/tmp/report.html",
    diff: { days: 12, gone: 2, back: 1, fresh: 1 },
  });
  assert.equal(model.score, 88);
  assert.equal(model.moves.length, 2);
  assert.equal(model.moves[1].title, "2 context blocks repeat across sessions");
  assert.match(model.protected, /included in score, never auto-fixed/);
  assert.equal(model.diff, "since your last run 12d ago: 2 moves gone · 1 back · 1 new");
});

test("learn TUI model handles empty history without inventing a score", () => {
  const model = buildLearnTuiModel({ ...base, sessions_scanned: 0 }, { report: "/tmp/report.html" });
  assert.equal(model.score, null);
  assert.equal(model.moves.length, 0);
  assert.match(model.status, /repeated across ≥3 sessions/);
  assert.doesNotMatch(model.status, /\$/);
});

test("learn --all preserves sink ids, classes, practices, and suggestions", () => {
  const text = renderLearnPlan({
    ...base,
    sessions_scanned: 3,
    sinks: [{
      sink_id: "recurring_context:abc",
      practice_id: "plausible-but-unknown",
      title: "Repeated deployment preamble",
      class: "recurring_context",
      basis: "inferred",
      tokens_per_turn: 420,
      tokens_per_day_rate: 1260,
      suggestion: "Offload after consent.",
    }],
  }, { report: "/tmp/report.html", verbose: true });
  assert.match(text, /recurring_context:abc  ·  recurring_context/);
  assert.match(text, /Offload after consent/);
  assert.doesNotMatch(text, /practice:/, "unknown practice must still be omitted");
  assert.doesNotMatch(text, /verified nowhere yet/);
});

test("learn Markdown stays detailed and preserves basis and per-day units", () => {
  const text = renderLearnPlan({
    ...base,
    sessions_scanned: 3,
    sinks: [{
      sink_id: "recurring_context:abc",
      practice_id: "context-compression",
      title: "Repeated deployment preamble",
      class: "recurring_context",
      basis: "inferred",
      tokens_per_turn: 420,
      tokens_per_day_rate: 1260,
    }],
  }, { markdown: true, report: "/tmp/report.html" });
  assert.match(text, /^## Setup Score 88 — basis: inferred/m);
  assert.match(text, /tokens\/day · basis: inferred/);
  assert.match(text, /practice: context-compression · unmeasured — verified nowhere yet/);
  assert.doesNotMatch(text, /\$/);
});
