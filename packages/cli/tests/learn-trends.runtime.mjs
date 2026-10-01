import assert from "node:assert/strict";
import { mkdtempSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";

process.env.CAVEMAN_HOME = mkdtempSync(join(tmpdir(), "caveman-learn-trends-"));
process.env.NO_COLOR = "1";
const { renderLearnPlan, buildLearnTuiModel } = await import("../dist/index.js");
const { learnSparkline, learnTrendLines } = await import("../dist/learn-trends.js");
const { learnScoreBody } = await import("../dist/learn-tui.js");

const metric = (key, label, unit, series, current, prior, delta, direction, better = "lower") => ({
  key, label, unit, better, statistic: "median", series, current, prior,
  current_sessions: 212, prior_sessions: 640, delta_pct: delta, direction,
});

const trends = {
  basis: "inferred",
  bucket: "iso_week_utc",
  current_week: "2026-W39",
  prior_weeks: 4,
  min_sessions: 5,
  dead_band_pct: 10,
  weeks: ["W35", "W36", "W37", "W38", "W39", "W40"].map((week, index) => ({
    week: `2026-${week}`, start: "2026-08-24", partial: index === 0 || index === 5, in_progress: index === 5,
    sessions: index === 0 ? 3 : 150, turns: 1000,
  })),
  metrics: [
    metric("tokens_per_session", "tokens/session", "tokens", [null, 1000, 1200, 1100, 900, 1200], 900, 1100, -18.2, "improved"),
    metric("peak_context_pct", "peak context", "pct", [null, 20, 22, 21, 21.5, 22], 21.5, 21, 2.4, "flat"),
    metric("cache_read_pct", "cache reads", "pct", [null, 95, 96, 97, 90, 90], 90, 96, -6.3, "worse", "higher"),
  ],
  score: { source: "sessions_recomputed", omitted: "x", history_source: "snapshots", history: [{ date: "2026-09-01", score: 80 }, { date: "2026-09-28", score: 77 }] },
  movers: { since: "2026-09-19", days: 8, grew: [{ sink_id: "a", title: "Config grew", status: "changed", delta_tokens_per_turn: 8459 }] },
  note: "Observed week-over-week change in your own sessions. A trend is not a saving and does not show what caused it.",
};

const plan = {
  schema: "caveman.learn.v1",
  basis: "inferred",
  sessions_scanned: 700,
  sessions_by_source: { claude: 700 },
  cave_score: { score: 77, basis: "inferred" },
  sinks: [{ sink_id: "recurring_context:repaste:x", title: "Repeated block", class: "recurring_context", basis: "inferred", tokens_per_turn: 0, tokens_per_day_rate: 0, evidence: {}, suggestion: "", framing: "historical" }],
  trends,
};

test("sparkline scales to the series and marks insufficient weeks", () => {
  assert.equal(learnSparkline([null, 1000, 1200, 1100, 900]), "·▃█▆▁");
  assert.equal(learnSparkline([5, 5]), "▁▁");
  assert.equal(learnSparkline([null, null]), "··");
});

test("plain learn shows a compact trend section after the score", () => {
  const out = renderLearnPlan(plan, { report: "/tmp/r.html" });
  const lines = out.split("\n");
  const at = lines.findIndex((line) => line.startsWith("last 6 weeks"));
  assert.ok(at > 0 && at < 5, out);
  assert.equal(lines[at], "last 6 weeks  tokens/session  ·▃█▆▁┊█  -18% · improved");
  assert.equal(lines[at + 1], "              peak context    ·▁█▅▆┊█  +0.5 points · flat");
  assert.equal(lines[at + 2], "              week of Aug 24 (212 sessions) vs the 4 weeks before");
  assert.equal(lines[at + 3], "              a trend is not a saving, and it does not show the cause");
  assert.ok(!out.includes("cache reads"), "compact view keeps to 2-4 lines");
  assert.ok(!/\$/.test(lines.slice(at, at + 4).join("\n")));
});

test("--md and --all render a trends table with n and honesty notes", () => {
  const md = renderLearnPlan(plan, { report: "/tmp/r.html", markdown: true });
  assert.match(md, /### Trends\n\| measure \| last 6 weeks \| week of Aug 24 \| 4 weeks before \| change \| sessions \|/);
  assert.match(md, /\| cache reads \| ·▆▇█▁┊▁ \| 90% \| 96% \| -6 points worse \| 212 vs 640 \|/);
  assert.match(md, /- weeks start Monday, UTC \(sessions in brackets; \* only partly scanned; ┊ still running, never compared\): Aug 24\* \(3\) .* Aug 24 \(still running\) \(150\)/);
  assert.match(md, /A trend is not a saving/);
  assert.match(md, /Setup Score history: Sep 1 80 → Sep 28 77/);
  assert.match(md, /grew since Sep 19 \(8 days ago\): Config grew \(\+8,459 per message, changed\)/);
  const all = renderLearnPlan(plan, { report: "/tmp/r.html", verbose: true, all: true });
  assert.match(all, /^trends\nmeasure +last 6 weeks +week of Aug 24/m);
  assert.ok(!all.includes("a trend is not a saving, and it does not show the cause"), "verbose view uses the table, not the compact lines");
});

test("TUI score card carries the same trend lines; absent block renders nothing", () => {
  const model = buildLearnTuiModel(plan, { report: "/tmp/r.html" });
  assert.deepEqual(model.trend, learnTrendLines(trends));
  assert.match(learnScoreBody(model), /last 6 weeks {2}tokens\/session/);
  const { trends: _omit, ...older } = plan;
  assert.equal(buildLearnTuiModel(older, { report: "/tmp/r.html" }).trend, undefined);
  assert.ok(!renderLearnPlan(older, { report: "/tmp/r.html" }).includes("last 6 weeks"));
});
