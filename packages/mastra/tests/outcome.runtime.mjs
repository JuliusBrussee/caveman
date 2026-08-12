/**
 * Outcome-helper tests. Every request goes through an injected fake fetch —
 * nothing here touches the network.
 */
import { test } from "node:test";
import assert from "node:assert/strict";

import { CavemanOutcomeError, recordOutcome, stableStringify } from "../dist/index.js";

function fakeFetch(response = { status: 201, body: JSON.stringify({ data: { id: "rec_1" }, created: true }) }) {
  const calls = [];
  const fn = async (url, init) => {
    calls.push({ url, init });
    return { status: response.status, text: async () => response.body };
  };
  fn.calls = calls;
  return fn;
}

const client = (fetch) => ({
  controlApiUrl: "https://api.caveman.so/",
  token: "jwt-token",
  projectId: "3f1b0f4e-0000-4000-8000-000000000001",
  fetch,
});

const input = {
  taskId: "ticket_991",
  contract: "resolved_support_ticket",
  values: { ticketResolved: true, customerReopenedWithin72Hours: false, csat: 5 },
  evidence: { ticketId: "ticket_991", resolutionEventId: "evt_77" },
  confidence: 1,
  source: "business_event",
  occurredAt: "2026-08-08T10:00:00.000Z",
};

test("posts the Outcome Contract body the control API expects", async () => {
  const fetch = fakeFetch();
  const result = await recordOutcome(client(fetch), input);

  assert.equal(fetch.calls.length, 1);
  const call = fetch.calls[0];
  assert.equal(
    call.url,
    "https://api.caveman.so/api/v1/projects/3f1b0f4e-0000-4000-8000-000000000001/outcomes",
  );
  assert.equal(call.init.method, "POST");
  assert.equal(call.init.headers.authorization, "Bearer jwt-token");
  assert.equal(call.init.headers["content-type"], "application/json");

  const body = JSON.parse(call.init.body);
  assert.deepEqual(body, {
    correlation_kind: "task",
    correlation_id: "ticket_991",
    source: "business_event",
    observed_outcome:
      'resolved_support_ticket {"csat":5,"customerReopenedWithin72Hours":false,"ticketResolved":true}',
    confidence: 1,
    evidence_refs: ["ticketId:ticket_991", "resolutionEventId:evt_77"],
    occurred_at: "2026-08-08T10:00:00.000Z",
  });
  assert.equal("verified_outcome" in body, false);
  assert.deepEqual(result, { status: 201, created: true, record: { id: "rec_1" } });
});

test("observed_outcome is stable across value key order", async () => {
  const a = fakeFetch();
  const b = fakeFetch();
  await recordOutcome(client(a), input);
  await recordOutcome(client(b), {
    ...input,
    values: { csat: 5, ticketResolved: true, customerReopenedWithin72Hours: false },
  });
  assert.equal(
    JSON.parse(a.calls[0].init.body).observed_outcome,
    JSON.parse(b.calls[0].init.body).observed_outcome,
  );
});

test("confidence and occurredAt are honoured", async () => {
  const fetch = fakeFetch();
  await recordOutcome(client(fetch), { ...input, confidence: 0.5, occurredAt: new Date(0) });
  const body = JSON.parse(fetch.calls[0].init.body);
  assert.equal(body.confidence, 0.5);
  assert.equal(body.occurred_at, "1970-01-01T00:00:00.000Z");
});

test("an idempotent re-delivery reports created:false", async () => {
  const fetch = fakeFetch({ status: 200, body: JSON.stringify({ data: { id: "rec_1" }, created: false }) });
  const result = await recordOutcome(client(fetch), input);
  assert.deepEqual(result, { status: 200, created: false, record: { id: "rec_1" } });
});

test("HTTP errors surface verbatim and are not retried", async () => {
  const fetch = fakeFetch({
    status: 400,
    body: '{"error":{"code":"cave_outcome_invalid_confidence"}}',
  });
  await assert.rejects(
    () => recordOutcome(client(fetch), input),
    (error) => {
      assert.ok(error instanceof CavemanOutcomeError);
      assert.equal(error.status, 400);
      assert.equal(error.body, '{"error":{"code":"cave_outcome_invalid_confidence"}}');
      return true;
    },
  );
  assert.equal(fetch.calls.length, 1);
});

test("a non-JSON success body is an error carrying the body verbatim", async () => {
  const fetch = fakeFetch({ status: 201, body: "<html>proxy</html>" });
  await assert.rejects(() => recordOutcome(client(fetch), input), /<html>proxy<\/html>/);
});

test("fails closed before any network call", async () => {
  const never = async () => {
    throw new Error("fetch must not be called when validation fails");
  };
  const c = client(never);
  const cases = [
    [{ ...input, taskId: "" }, /taskId is required/],
    [{ ...input, taskId: "   " }, /taskId is required/],
    [{ ...input, contract: "" }, /contract is required/],
    [{ ...input, values: {} }, /values must be a non-empty plain object/],
    [{ ...input, values: null }, /values must be a non-empty plain object/],
    [{ ...input, values: [1, 2] }, /values must be a non-empty plain object/],
    [{ ...input, values: { at: new Date() } }, /must be a plain object, array, or primitive/],
    [{ ...input, values: { n: Number.NaN } }, /finite number/],
    [{ ...input, evidence: {} }, /at least one reference/],
    [{ ...input, evidence: { k: null } }, /must be a string, number, or boolean/],
    [{ ...input, evidence: { k: "" } }, /must be non-empty/],
    [{ ...input, confidence: 1.5 }, /within \[0,1\]/],
    [{ ...input, confidence: -0.1 }, /within \[0,1\]/],
    [{ ...input, confidence: undefined }, /confidence is required/],
    [{ ...input, source: undefined }, /source is required/],
    [{ ...input, source: "agent_claim" }, /source is required and must be one of/],
    [{ ...input, source: "judge" }, /source is required and must be one of/],
    [{ ...input, occurredAt: "not-a-date" }, /valid date/],
  ];
  for (const [payload, pattern] of cases) {
    await assert.rejects(() => recordOutcome(c, payload), pattern);
  }

  const tooMuchEvidence = Object.fromEntries(Array.from({ length: 65 }, (_, i) => [`k${i}`, i]));
  await assert.rejects(() => recordOutcome(c, { ...input, evidence: tooMuchEvidence }), /at most 64 references/);

  await assert.rejects(() => recordOutcome({ ...c, token: "" }, input), /token is required/);
  await assert.rejects(() => recordOutcome({ ...c, projectId: "" }, input), /projectId is required/);
  await assert.rejects(() => recordOutcome({ ...c, controlApiUrl: "api.caveman.so" }, input), /absolute http\(s\)/);
});

test("exactly 64 evidence refs are accepted", async () => {
  const fetch = fakeFetch();
  const evidence = Object.fromEntries(Array.from({ length: 64 }, (_, i) => [`k${i}`, i]));
  await recordOutcome(client(fetch), { ...input, evidence });
  assert.equal(JSON.parse(fetch.calls[0].init.body).evidence_refs.length, 64);
});

test("stableStringify sorts keys at every level", () => {
  assert.equal(
    stableStringify({ b: 1, a: { d: [3, { f: 1, e: 2 }], c: "x" } }),
    '{"a":{"c":"x","d":[3,{"e":2,"f":1}]},"b":1}',
  );
  assert.equal(stableStringify(null), "null");
  assert.throws(() => stableStringify(undefined), /must be JSON data/);
});
