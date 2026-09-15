import assert from "node:assert/strict";
import test from "node:test";
import { fmtMoney, renderLearnPlan, renderLearnSavings } from "../dist/index.js";

const basePlan = {
  schema: "caveman.learn.v1",
  basis: "inferred",
  sessions_scanned: 9,
  cave_score: { score: 70, basis: "inferred", scope: "local_setup" },
  sinks: [
    {
      sink_id: "recurring_context:repaste:fp1",
      practice_id: "context-compression",
      title: "A 900-token block is re-established across sessions",
      class: "recurring_context",
      basis: "inferred",
      tokens_per_turn: 900,
      tokens_per_day_rate: 9_000,
      spend_usd_per_day: 0.027,
      evidence: {},
      suggestion: "offload to cavemem",
      framing: "forward_rate",
    },
  ],
};

test("window cost renders provider-counted spend and the effective input rate", () => {
  const out = renderLearnPlan({
    ...basePlan,
    spend: {
      basis: "provider_counted_x_published_rate",
      currency: "USD",
      catalog_version: "2026-08-10",
      window_days: 30,
      usd: 34.1,
      components: [
        { key: "cache_read", tokens: 7_900_000, usd: 11.85, share_pct: 34.8 },
        { key: "output", tokens: 1_300_000, usd: 19.5, share_pct: 57.2 },
      ],
      effective_input_usd_per_mtok: 1.42,
      effective_input_multiplier: 0.19,
    },
  });
  assert.match(out, /window cost/);
  assert.match(out, /\$34\.10/);
  assert.match(out, /effective input/);
  assert.match(out, /0\.19x list after cache reuse/);
  // The subscription disclaimer is not optional: a Max user's marginal cost is
  // zero and the figure must never read as money they spent.
  assert.match(out, /subscription plans have no marginal cost/);
});

test("an unpriced model is disclosed so the total reads as a floor", () => {
  const out = renderLearnPlan({
    ...basePlan,
    spend: {
      basis: "provider_counted_x_published_rate",
      currency: "USD",
      usd: 0,
      unpriced: [{ provider: "anthropic", model: "claude-imaginary-9", tokens: 6_000_000, reason: "no catalog row" }],
    },
  });
  assert.match(out, /unpriced/);
  assert.match(out, /claude-imaginary-9/);
  assert.match(out, /total is a floor/);
});

test("a plan without spend renders exactly as before", () => {
  const out = renderLearnPlan(basePlan);
  assert.doesNotMatch(out, /window cost/);
  assert.doesNotMatch(out, /effective input/);
});

test("confirmed rows carry how they were measured", () => {
  const out = renderLearnPlan({
    ...basePlan,
    confirmed: [{
      sink_id: "claude_md_weight:project",
      fix_kind: "claude_md_weight",
      applied_at: "2026-08-01T00:00:00Z",
      before: 1600,
      after: 400,
      unit: "config_tokens_per_turn",
      sessions_after: 6,
      verdict: "improved",
      attribution: { method: "deterministic_remeasure", rung: 4, confidence: "high", provenance: "intact" },
    }],
  });
  assert.match(out, /deterministic_remeasure \(high\)/);
});

test("a tainted fingerprint is shown next to the number, not hidden", () => {
  const out = renderLearnPlan({
    ...basePlan,
    confirmed: [{
      sink_id: "claude_md_weight:project",
      fix_kind: "claude_md_weight",
      applied_at: "2026-08-01T00:00:00Z",
      before: 1600,
      after: 400,
      unit: "config_tokens_per_turn",
      sessions_after: 6,
      verdict: "improved",
      attribution: { method: "deterministic_remeasure", rung: 4, confidence: "low", provenance: "changed_since" },
    }],
  });
  assert.match(out, /deterministic_remeasure \(low, changed_since\)/);
});

test("the savings ledger groups by rung and never prints a blended total", () => {
  const out = renderLearnSavings({
    schema: "caveman.learn.savings.v1",
    basis: "inferred",
    currency: "USD",
    window: { since: "30d" },
    rows: [
      {
        sink_id: "claude_md_weight:project",
        fix_kind: "claude_md_weight",
        applied_at: "2026-08-01T00:00:00Z",
        verdict: "improved",
        unit: "config_tokens_per_turn",
        before: 1600,
        after: 400,
        saved_value: 1200,
        saved_usd: 0.036,
        attribution: {
          method: "deterministic_remeasure",
          rung: 4,
          confidence: "high",
          provenance: "intact",
          confounders: ["The token delta is arithmetic on the edited file; whether you would have trimmed it anyway is not measured."],
        },
      },
      {
        sink_id: "context_dumbzone",
        fix_kind: "dumbzone_advice",
        applied_at: "2026-08-02T00:00:00Z",
        verdict: "improved",
        unit: "turns_over_half_window_pct",
        before: 40,
        after: 20,
        saved_value: 20,
        attribution: {
          method: "interrupted_time_series",
          rung: 1,
          confidence: "low",
          provenance: "not_fingerprinted",
          confounders: ["Before/after sessions differ in more than this fix."],
        },
      },
    ],
    total_saved_usd_by_rung: { deterministic_remeasure: 0.036 },
    caveats: ["Savings are grouped by attribution method and never summed across methods."],
  });
  assert.match(out, /deterministic_remeasure/);
  assert.match(out, /interrupted_time_series/);
  assert.match(out, /confidence high/);
  assert.match(out, /confidence low/);
  // Confounders must be visible in the default view, not behind a flag.
  assert.match(out, /whether you would have trimmed it anyway is not measured/);
  assert.doesNotMatch(out, /\btotal saved\b/i);
});

test("an empty ledger explains itself instead of showing zero", () => {
  const out = renderLearnSavings({
    schema: "caveman.learn.savings.v1",
    basis: "inferred",
    window: { since: "30d" },
    rows: [],
    caveats: ["No fix has been recorded yet."],
  });
  assert.match(out, /no fix recorded yet/);
  assert.doesNotMatch(out, /\$0\.00/);
});

test("sub-cent spend stays legible instead of rounding to nothing", () => {
  assert.equal(fmtMoney(12.5, "USD"), "$12.50");
  assert.equal(fmtMoney(0.036, "USD"), "$0.036");
  assert.equal(fmtMoney(0.000031, "USD"), "$0.00003");
});

test("a holdout report shows arm sizes next to the verdict", async () => {
  const { renderExperimentReport } = await import("../dist/index.js");
  const out = renderExperimentReport({
    schema: "caveman.learn.experiment.v1",
    label: "pytest-loop",
    sink_id: "procedure_repeat:abc",
    fix_kind: "skill_distillation",
    arms: [
      { arm: "on", sessions: 7, median_session_tokens: 60000, error_turns_per_turn: 0.03 },
      { arm: "off", sessions: 6, median_session_tokens: 100000, error_turns_per_turn: 0.03 },
    ],
    verdict: "improved",
    median_session_tokens_delta_pct: -40,
    saved_usd_per_session: 0.12,
    currency: "USD",
    attribution: {
      method: "controlled_holdout",
      rung: 3,
      confidence: "high",
      provenance: "not_fingerprinted",
      confounders: ["Arms are your own consecutive sessions, not randomized tasks."],
    },
    caveats: ["This is the strongest local evidence available and it is still inferred."],
  });
  assert.match(out, /on\s+7 sessions/);
  assert.match(out, /off\s+6 sessions/);
  assert.match(out, /improved\s+-40\.0%/);
  assert.match(out, /\$0\.120\/session/);
  assert.match(out, /controlled_holdout/);
  // A verdict without its confounder is the failure mode this guards.
  assert.match(out, /not randomized tasks/);
});

test("an underpowered holdout shows no verdict dressed as a win", async () => {
  const { renderExperimentReport } = await import("../dist/index.js");
  const out = renderExperimentReport({
    label: "thin",
    arms: [{ arm: "on", sessions: 2, median_session_tokens: 10000, error_turns_per_turn: 0 }],
    verdict: "insufficient_data",
    attribution: { method: "unattributed", rung: 0, confidence: "none", provenance: "not_fingerprinted" },
    caveats: ["Each arm needs at least 5 sessions before a verdict."],
  });
  assert.match(out, /insufficient_data/);
  assert.match(out, /at least 5 sessions/);
  assert.doesNotMatch(out, /\$/);
});
