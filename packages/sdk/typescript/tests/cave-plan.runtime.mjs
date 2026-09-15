/**
 * Runtime tests for the TS SDK cave-plan surface (cave.cavePlan()).
 * Run with: node --test tests/cave-plan.runtime.mjs
 * (uses the built-in node:test runner with a global fetch mock; run against dist/).
 */
import { test } from "node:test";
import assert from "node:assert/strict";

// Import compiled dist (must run `pnpm build` first).
import { Cave } from "../dist/index.js";

// ─── Fetch mock helpers ──────────────────────────────────────────────────────

let capturedRequests = [];
const originalFetch = globalThis.fetch;

function mockFetch(responseData, { ok = true, status = 200 } = {}) {
  capturedRequests = [];
  globalThis.fetch = async (url, init) => {
    capturedRequests.push({ url: String(url), method: init?.method ?? "GET", headers: init?.headers ?? {}, body: init?.body ? JSON.parse(init.body) : undefined });
    return {
      ok,
      status,
      json: async () => responseData,
      text: async () => JSON.stringify(responseData)
    };
  };
}

function restoreFetch() {
  globalThis.fetch = originalFetch;
}

// A representative project-scope Cave Plan (snake_case wire fields).
const PLAN = {
  scope: "project",
  project_id: "proj_abc",
  headline: { low: 18.4, base: 31.2, high: 47.9, basis: "inferred", move_count: 2 },
  headroom_by_class: [
    { safety_class: "S2_STRUCTURAL", low: 12.0, base: 21.0, high: 33.0, move_count: 1 }
  ],
  moves: [
    {
      optimizer_id: "tool-result-truncation",
      title: "Truncate oversized tool results",
      family: "input_bloat",
      safety_class: "S2_STRUCTURAL",
      basis: "inferred",
      confidence: "medium",
      quality_risk: "low",
      implementation_effort: "small",
      requires_eval_gate: true,
      scope_count: 3,
      sample_size: 4120,
      savings_usd_low: 12.0,
      savings_usd_base: 21.0,
      savings_usd_high: 33.0,
      share_of_headline_pct: 67.3,
      next_action: "Cap tool-result size at the callsite.",
      summary: "Oversized tool results re-enter context on support-triage.",
      top_scopes: [
        { workflow_id: "wf_1", workflow_name: "support-triage", savings_usd_base: 15.0, share_pct: 71.4 }
      ]
    }
  ],
  no_signal: [
    { optimizer_id: "toon-reencoding", title: "TOON re-encode tabular JSON", reason_code: "no_detector_yet", reason: "No detector emits this signal yet." }
  ],
  methodology: "Inferred per-day headroom from the last 7 closed days of telemetry.",
  as_of: "2026-07-21"
};

// ─── Tests ───────────────────────────────────────────────────────────────────

test("cavePlan() GETs /sdk/v1/cave-plan with the x-cave-api-key header and returns the plan", async () => {
  mockFetch(PLAN);
  try {
    const cave = new Cave({ apiKey: "cave_live_plan_key", baseURL: "http://localhost:8787", agent: "test-agent" });
    const plan = await cave.cavePlan();

    assert.equal(capturedRequests.length, 1, "exactly one fetch call");
    const req = capturedRequests[0];

    // Control-plane read: no controlURL set → falls back to baseURL, GET, no body.
    assert.equal(req.url, "http://localhost:8787/sdk/v1/cave-plan");
    assert.equal(req.method, "GET");
    assert.equal(req.body, undefined, "GET carries no body");

    // Project-key auth via x-cave-api-key — never the gateway Bearer header.
    assert.equal(req.headers["x-cave-api-key"], "cave_live_plan_key");
    assert.ok(!("authorization" in req.headers), "must not send authorization: Bearer");
    assert.equal(req.headers["x-cave-agent"], "test-agent");

    // Passed through verbatim: snake_case wire fields, inferred per-day figures.
    assert.equal(plan.scope, "project");
    assert.equal(plan.project_id, "proj_abc");
    assert.equal(plan.headline.basis, "inferred");
    assert.equal(plan.headline.move_count, 2);
    assert.equal(plan.moves[0].optimizer_id, "tool-result-truncation");
    assert.equal(plan.moves[0].safety_class, "S2_STRUCTURAL");
    assert.equal(plan.moves[0].savings_usd_base, 21.0);
    assert.equal(plan.moves[0].top_scopes[0].workflow_name, "support-triage");
    assert.equal(plan.no_signal[0].reason_code, "no_detector_yet");
    assert.equal(plan.as_of, "2026-07-21");
  } finally {
    restoreFetch();
  }
});

test("cavePlan() stays on gateway when controlURL is set", async () => {
  mockFetch(PLAN);
  try {
    const cave = new Cave({ apiKey: "k", baseURL: "http://gateway.test", agent: "a", controlURL: "http://control.test" });
    await cave.cavePlan();

    assert.equal(capturedRequests[0].url, "http://gateway.test/sdk/v1/cave-plan");
  } finally {
    restoreFetch();
  }
});

test("cavePlan() throws on a non-200 response", async () => {
  mockFetch({ error: "cave_forbidden_scope" }, { ok: false, status: 403 });
  try {
    const cave = new Cave({ apiKey: "k", baseURL: "http://localhost:8787", agent: "a" });
    await assert.rejects(() => cave.cavePlan(), /HTTP 403/);
  } finally {
    restoreFetch();
  }
});
