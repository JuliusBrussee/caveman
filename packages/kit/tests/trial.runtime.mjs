import { test } from "node:test";
import assert from "node:assert/strict";
import React from "react";
import { renderToStaticMarkup } from "react-dom/server";

import { UsageOriginReport, QuotaBadge, TrialMoveList } from "../dist/index.js";

test("trial components render with required basis badges and local-only labels", () => {
  const origins = [{
    source_kind: "codex_session",
    agent_slug: "codex",
    provider: "openai",
    model: "gpt-4o",
    requests: 2,
    input_tokens: 1200,
    total_cost_usd: 0.003,
    basis: "observed_local",
  }];
  const moves = [{
    optimizer_id: "provider-native-prompt-cache",
    title: "Stabilize repeated prompt prefixes",
    safety_class: "S1_PROVIDER_NATIVE",
    status: "safe_now",
    savings_usd_base: 0.001,
    confidence: "medium",
  }];
  const quota = {
    provider: "anthropic",
    plan_type: "max",
    window: "five_hour",
    used_pct: 91,
    resets_at: "2026-06-01T17:00:00Z",
    basis: "linked_api",
  };

  const html = renderToStaticMarkup(
    React.createElement(React.Fragment, null,
      React.createElement(UsageOriginReport, { origins, basis: "inferred" }),
      React.createElement(QuotaBadge, { snapshot: quota, basis: "inferred" }),
      React.createElement(TrialMoveList, { moves, basis: "inferred" }),
    ),
  );

  assert.match(html, /Usage Origins/);
  assert.match(html, /codex_session/);
  assert.match(html, /Trial Moves/);
  assert.match(html, /provider-native-prompt-cache/);
  assert.match(html, /five_hour/);
  assert.match(html, /data-caveman-basis="unverified"/);
  assert.doesNotMatch(html, /Verified/);
});

test("trial components fail closed for row-level verified basis", () => {
  const html = renderToStaticMarkup(
    React.createElement(UsageOriginReport, {
      basis: "inferred",
      origins: [{
        source_kind: "caveman_proxy",
        agent_slug: "codex",
        provider: "openai",
        model: "gpt-4o",
        requests: 1,
        input_tokens: 1,
        total_cost_usd: 0,
        basis: "verified",
      }],
    }),
  );

  assert.doesNotMatch(html, /Verified/);
  assert.match(html, /Inferred/);
  assert.match(html, /data-caveman-trial-basis="inferred"/);
});

test("trial components fail closed for top-level verified basis from untyped callers", () => {
  const quota = { provider: "anthropic", plan_type: "max", window: "daily", used_pct: 1, resets_at: "", basis: "linked_api" };
  const move = { optimizer_id: "x", title: "Move", safety_class: "S1_PROVIDER_NATIVE", status: "safe_now", savings_usd_base: 0, confidence: "low" };
  const html = renderToStaticMarkup(
    React.createElement(React.Fragment, null,
      React.createElement(UsageOriginReport, { basis: "verified", origins: [] }),
      React.createElement(QuotaBadge, { basis: "verified", snapshot: quota }),
      React.createElement(TrialMoveList, { basis: "verified", moves: [move] }),
    ),
  );

  assert.doesNotMatch(html, /Verified/);
  assert.match(html, /Inferred/);
});

test("trial cost fields never turn missing, non-finite, or negative data into exact zero", () => {
  const badCosts = [undefined, Number.NaN, Number.POSITIVE_INFINITY, -1];
  for (const value of badCosts) {
    const html = renderToStaticMarkup(
      React.createElement(TrialMoveList, {
        basis: "inferred",
        moves: [{
          optimizer_id: `bad-${String(value)}`,
          title: "Bad cost",
          safety_class: "S1_PROVIDER_NATIVE",
          status: "insufficient_data",
          savings_usd_base: value,
          confidence: "low",
        }],
      }),
    );
    assert.match(html, /Unavailable/);
    assert.doesNotMatch(html, /\$0\.0000/);
  }
});
