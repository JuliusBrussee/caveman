import assert from "node:assert/strict";
import { chmodSync, mkdtempSync, readFileSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";
import {
  buildLearnTuiModel,
  formatLearnProxyJSON,
  learnSummaryMoves,
  renderLearnPlan,
} from "../dist/index.js";
import { learnScoreBody } from "../dist/learn-tui.js";
import { isolatedCliEnv, runCli } from "./_cli.mjs";

const plan = {
  schema: "caveman.learn.v1",
  basis: "inferred",
  sessions_scanned: 8,
  sessions_by_source: { claude: 5, codex: 3 },
  cave_score: { score: 74, basis: "inferred", scope: "local_setup" },
  sinks: [
    {
      sink_id: "config_tax:baseline",
      practice_id: "context-compression",
      title: "Your agent config loads into every turn",
      class: "load_bearing",
      basis: "inferred",
      tokens_per_turn: 6100,
      tokens_per_day_rate: 24_400,
      evidence: {
        measured_prefix_tokens: 7200,
        unexplained_prefix_tokens: 1100,
        token_basis: "session_usage",
      },
    },
    {
      sink_id: "claude_md_weight:project",
      practice_id: "context-compression",
      title: "Trim project instructions",
      class: "reducible",
      basis: "inferred",
      tokens_per_turn: 1200,
      tokens_per_day_rate: 4800,
      suggestion: "Keep only instructions used in this repository.",
    },
    {
      sink_id: "learning_loop:error:abc",
      practice_id: "error-feedback-loop",
      title: "Repeated failing tool loop",
      class: "behavioral",
      basis: "inferred",
      tokens_per_turn: 0,
      tokens_per_day_rate: 0,
      tokens_observed: 52_400,
      evidence: { tokens_observed_basis: "bytes4_estimate" },
      suggestion: "Review the loop before changing workflow.",
    },
    {
      sink_id: "recurring_context:abc",
      practice_id: "prompt-prefix-stability",
      title: "Repeated deployment context",
      class: "recurring_context",
      basis: "inferred",
      tokens_per_turn: 600,
      tokens_per_day_rate: 1800,
      evidence: { block_tokens: 600, recurrence_sessions: 5 },
    },
  ],
  confirmed: [
    {
      sink_id: "fix:improved",
      fix_kind: "config_trim",
      applied_at: "2026-08-01T10:00:00Z",
      before: 1200,
      after: 800,
      unit: "config_tokens_per_turn",
      sessions_after: 6,
      verdict: "improved",
    },
    {
      sink_id: "fix:unchanged",
      fix_kind: "config_trim",
      applied_at: "2026-08-02T10:00:00Z",
      before: 500,
      after: 500,
      unit: "config_tokens_per_turn",
      sessions_after: 4,
      verdict: "unchanged",
    },
    {
      sink_id: "fix:regressed",
      fix_kind: "dumbzone_habit",
      applied_at: "2026-08-03T10:00:00Z",
      before: 12.5,
      after: 19,
      unit: "turns_over_half_window_pct",
      sessions_after: 5,
      verdict: "regressed",
    },
    {
      sink_id: "fix:pending",
      fix_kind: "cavemem_offload",
      applied_at: "2026-08-04T10:00:00Z",
      before: 1,
      unit: "recurrence_present",
      sessions_after: 2,
      verdict: "insufficient_data",
    },
    {
      sink_id: "fix:unavailable",
      fix_kind: "config_trim",
      applied_at: "2026-08-05T10:00:00Z",
      before: 1,
      unit: "unavailable",
      sessions_after: 0,
      verdict: "unchanged",
    },
  ],
  portfolio: {
    groups: [],
    best_next_move: {
      fix_label: "Trim loaded config",
      sink_ids: ["claude_md_weight:project"],
      combined_rate_per_day: 4800,
      combined_observed_in_window: 52_400,
      top_sink_id: "claude_md_weight:project",
      top_sink_title: "Trim project instructions",
      confidence: "measured_usage",
      net_note: "One approved edit resolves this group.",
    },
  },
  repos: [
    { repo: "api", sessions: 5, turns: 44, median_context: 18_200, dumbzone_pct: 23 },
    { repo: "web", sessions: 3, turns: 21, median_context: 9100, dumbzone_pct: 5 },
  ],
};

test("learn v2 summary promotes portfolio move and keeps historical totals separate", () => {
  const moves = learnSummaryMoves(plan);
  assert.equal(moves.length, 3, "summary cap stays three");
  assert.equal(moves[0].title, "Trim project instructions");
  assert.equal(moves[0].kind, "Trim loaded config");
  assert.match(moves[0].detail, /sink: claude_md_weight:project · ~5k tokens\/day · ~52k tokens observed · measured/);
  assert.doesNotMatch(moves[0].detail, /equivalent/);
  assert.doesNotMatch(moves.slice(1).map((move) => move.title).join("\n"), /Trim project instructions/);
  for (const [confidence, label] of [
    ["transcript_inferred", "transcript"],
    ["static_estimate", "estimate"],
  ]) {
    const [mapped] = learnSummaryMoves({
      ...plan,
      portfolio: {
        ...plan.portfolio,
        best_next_move: { ...plan.portfolio.best_next_move, confidence },
      },
    });
    assert.match(mapped.detail, new RegExp(` · ${label}$`));
  }

  const text = renderLearnPlan(plan, { report: "/tmp/report.html" });
  assert.match(text, /1  Trim project instructions/);
  assert.match(text, /confirmed fixes/);
  assert.match(text, /✓ fix:improved — 1200 → 800 config tokens\/turn over 6 sessions \(improved\) · applied 2026-08-01/);
  assert.match(text, /! fix:regressed — 12\.5 → 19 turns over half-window \(%\) over 5 sessions \(regressed\) · applied 2026-08-03/);
  assert.match(text, /· fix:pending — applied 2026-08-04 · needs more post-fix sessions \(2 sessions so far\)/);
  assert.doesNotMatch(text, /fix:unavailable/);
  assert.match(text, /provider-counted prefix ~7k \(turn-1 median\)/);
  assert.doesNotMatch(text, /per-repo/);
  assert.doesNotMatch(text, /saved you/i);
});

test("learn v2 detailed and Markdown views expose history without blending rates", () => {
  const detailed = renderLearnPlan(plan, { report: "/tmp/report.html", verbose: true, all: true });
  assert.match(detailed, /~0 tokens\/turn · ~0 tokens\/day · ~52k tokens observed \(historical\) · basis: inferred/);
  assert.match(detailed, /per-repo\napi · 5 sessions · dumbzone 23% · median context ~18k/);
  assert.match(detailed, /web · 3 sessions · dumbzone 5% · median context ~9k/);
  assert.match(detailed, /caveman learn applied <sink_id>/);
  assert.match(detailed, /caveman learn simulate <sink_id\.\.\.>/);
  assert.match(detailed, /caveman learn --repo <substring>/);

  const markdown = renderLearnPlan(plan, { report: "/tmp/report.html", markdown: true });
  assert.match(markdown, /~52k tokens observed \(historical\)/);
  assert.match(markdown, /### Confirmed fixes/);
  assert.match(markdown, /\*· fix:pending/);
  assert.doesNotMatch(markdown, /per-repo/);
  assert.doesNotMatch(markdown, /caveman learn applied/);

  const withoutRecurring = renderLearnPlan({
    ...plan,
    sinks: plan.sinks.filter((sink) => sink.class !== "recurring_context"),
  }, { report: "/tmp/report.html", verbose: true, all: true });
  assert.match(withoutRecurring, /per-repo\napi · 5 sessions/);
  assert.match(withoutRecurring, /caveman learn applied <sink_id>/);
});

test("learn v2 TUI model reports confirmed-fix count under score", () => {
  const model = buildLearnTuiModel(plan, { report: "/tmp/report.html" });
  assert.equal(model.confirmed, 5);
  assert.equal(model.moves[0].title, "Trim project instructions");
  assert.match(model.protected, /provider-counted prefix ~7k \(turn-1 median\)/);
  assert.match(learnScoreBody(model), /5 fixes measured since you applied them — run caveman learn --all/);

  const noScore = buildLearnTuiModel({ ...plan, sinks: [] }, { report: "/tmp/report.html" });
  assert.equal(noScore.score, null);
  assert.match(learnScoreBody(noScore), /5 fixes measured since you applied them — run caveman learn --all/);
});

test("learn v2 piped plain rendering contains no ANSI when stderr is a TTY", () => {
  const stderrTTY = Object.getOwnPropertyDescriptor(process.stderr, "isTTY");
  const stdoutTTY = Object.getOwnPropertyDescriptor(process.stdout, "isTTY");
  const noColor = process.env.NO_COLOR;
  try {
    delete process.env.NO_COLOR;
    Object.defineProperty(process.stderr, "isTTY", { configurable: true, value: true });
    Object.defineProperty(process.stdout, "isTTY", { configurable: true, value: false });
    const text = renderLearnPlan(plan, { report: "/tmp/report.html" });
    assert.doesNotMatch(text, /\x1b\[[0-9;]*m/);
  } finally {
    if (stderrTTY) Object.defineProperty(process.stderr, "isTTY", stderrTTY);
    else delete process.stderr.isTTY;
    if (stdoutTTY) Object.defineProperty(process.stdout, "isTTY", stdoutTTY);
    else delete process.stdout.isTTY;
    if (noColor === undefined) delete process.env.NO_COLOR;
    else process.env.NO_COLOR = noColor;
  }
});

test("learn applied and simulate forward exact argv and preserve piped proxy JSON", async () => {
  const isolated = isolatedCliEnv();
  const proxy = join(mkdtempSync(join(tmpdir(), "caveman-learn-v2-proxy-")), "caveman-proxy");
  const argsFile = `${proxy}.args`;
  writeFileSync(proxy, `#!/bin/sh
printf '%s\\n' '---' "$@" >> "$CAVE_TEST_ARGS"
if [ "$2" = "applied" ]; then printf '%s' '{"recorded":true,"sink_id":"sink:a"}'; exit 0; fi
if [ "$2" = "simulate" ]; then printf '%s' '{"sink_ids":["sink:a","sink:b"],"tokens":42}'; exit 0; fi
printf '%s' '{"ok":true}'
`);
  chmodSync(proxy, 0o755);
  isolated.env.CAVEMAN_PROXY_BIN = proxy;
  isolated.env.CAVE_TEST_ARGS = argsFile;
  try {
    const applied = await runCli(
      ["learn", "applied", "sink:a", "--fix-kind", "config_trim", "--note", "approved"],
      { env: isolated.env },
    );
    assert.equal(applied.code, 0, applied.stderr);
    assert.equal(applied.stdout, '{"recorded":true,"sink_id":"sink:a"}');

    const simulated = await runCli(["learn", "simulate", "sink:a", "sink:b"], { env: isolated.env });
    assert.equal(simulated.code, 0, simulated.stderr);
    assert.equal(simulated.stdout, '{"sink_ids":["sink:a","sink:b"],"tokens":42}');

    const scan = await runCli(["learn", "scan", "--repo", "api"], { env: isolated.env });
    const report = await runCli(["learn", "report", "--repo", "web"], { env: isolated.env });
    assert.equal(scan.code, 0, scan.stderr);
    assert.equal(report.code, 0, report.stderr);
    const calls = readFileSync(argsFile, "utf8");
    assert.match(calls, /learn\napplied\nsink:a\n--fix-kind\nconfig_trim\n--note\napproved/);
    assert.match(calls, /learn\nsimulate\nsink:a\nsink:b/);
    assert.match(calls, /learn\nscan\n--repo\napi/);
    assert.match(calls, /learn\nreport\n--repo\nweb/);
  } finally {
    isolated.cleanup();
  }
});

test("learn advanced JSON is pretty only for TTY output", () => {
  const raw = '{"z":1,"a":2}';
  assert.equal(formatLearnProxyJSON(raw, false), raw);
  assert.equal(formatLearnProxyJSON(raw, true), '{\n  "z": 1,\n  "a": 2\n}\n');
});

test("learn v2 skill embedded by CLI stays byte-identical to canonical skill", async () => {
  const isolated = isolatedCliEnv();
  try {
    const out = await runCli(["skills", "preview", "caveman-learn"], { env: isolated.env });
    assert.equal(out.code, 0, out.stderr);
    assert.equal(out.stdout, readFileSync(new URL("../../../skills/caveman-learn/SKILL.md", import.meta.url), "utf8"));
  } finally {
    isolated.cleanup();
  }
});
