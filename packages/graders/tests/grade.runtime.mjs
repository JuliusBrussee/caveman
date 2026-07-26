/**
 * Runtime tests for the eval grader taxonomy (all 15 types).
 * Run with: node --test packages/evals/tests/grade.runtime.mjs
 * (Node 22, built-in node:test. Requires `tsc` build to dist first.)
 */
import { test } from "node:test";
import assert from "node:assert/strict";

import { grade } from "../dist/index.js";

const permissiveSsrf = async () => ({ allowed: true, reason: "ok" });
const fakeFetch = (responseObj) => async () => ({ ok: true, json: async () => responseObj });
const throwingFetch = async () => {
  throw new Error("fetch must not be called when SSRF guard blocks the URL");
};

test("exact_match pass/fail", async () => {
  assert.equal((await grade({ type: "exact_match", expected: "Paris" }, "Paris")).passed, true);
  assert.equal((await grade({ type: "exact_match", expected: "Paris" }, "Berlin")).passed, false);
});

test("exact_match normalises like Python (case + key order insensitive)", async () => {
  // Mirrors cloud/optimizer _normalise_str: case-insensitive, whitespace-trimmed,
  // key-order-insensitive for objects — same verdict in both languages.
  assert.equal((await grade({ type: "exact_match", expected: "Paris" }, " paris ")).passed, true);
  assert.equal((await grade({ type: "exact_match", expected: { a: 1, b: 2 } }, { b: 2, a: 1 })).passed, true);
});

test("contains pass/fail", async () => {
  assert.equal((await grade({ type: "contains", fragments: ["hello", "world"] }, "hello world")).passed, true);
  assert.equal((await grade({ type: "contains", fragments: ["zzz"] }, "hello world")).passed, false);
});

test("regex pass/fail (invalid pattern fails closed)", async () => {
  assert.equal((await grade({ type: "regex", pattern: "order-\\d+" }, "order-123")).passed, true);
  assert.equal((await grade({ type: "regex", pattern: "order-\\d+" }, "order-abc")).passed, false);
  assert.equal((await grade({ type: "regex", pattern: "(" }, "x")).passed, false);
});

test("json_schema pass/fail (bool is not integer)", async () => {
  const schema = { type: "object", required: ["name", "age"], properties: { name: { type: "string" }, age: { type: "integer" } } };
  assert.equal((await grade({ type: "json_schema", schema }, { name: "a", age: 3 })).passed, true);
  assert.equal((await grade({ type: "json_schema", schema }, { name: "a", age: "x" })).passed, false);
  assert.equal((await grade({ type: "json_schema", schema }, { name: "a" })).passed, false);
  assert.equal((await grade({ type: "json_schema", schema }, { name: "a", age: true })).passed, false);
});

test("json_schema unknown type keyword fails closed", async () => {
  // A misspelled type ("strng") is an invalid schema — must not silently satisfy.
  assert.equal((await grade({ type: "json_schema", schema: { type: "strng" } }, "anything")).passed, false);
});

test("json_path_assertion equals + exists", async () => {
  const cand = { a: { b: 5 }, items: [{ id: 1 }, { id: 2 }] };
  assert.equal((await grade({ type: "json_path_assertion", path: "a.b", equals: 5 }, cand)).passed, true);
  assert.equal((await grade({ type: "json_path_assertion", path: "a.b", equals: 6 }, cand)).passed, false);
  assert.equal((await grade({ type: "json_path_assertion", path: "items.1.id", exists: true }, cand)).passed, true);
  assert.equal((await grade({ type: "json_path_assertion", path: "nope", exists: true }, cand)).passed, false);
});

test("tool_called pass/fail", async () => {
  const cand = { tool_calls: [{ name: "search" }, { name: "summarize" }] };
  assert.equal((await grade({ type: "tool_called", tools: ["search"] }, cand)).passed, true);
  assert.equal((await grade({ type: "tool_called", tools: ["delete"] }, cand)).passed, false);
});

test("tool_not_called pass/fail", async () => {
  const cand = { tool_calls: [{ name: "search" }] };
  assert.equal((await grade({ type: "tool_not_called", tools: ["delete"] }, cand)).passed, true);
  assert.equal((await grade({ type: "tool_not_called", tools: ["search"] }, cand)).passed, false);
});

test("tool_sequence ordered subsequence", async () => {
  const cand = { tool_calls: [{ name: "a" }, { name: "b" }, { name: "c" }] };
  assert.equal((await grade({ type: "tool_sequence", tools: ["a", "c"] }, cand)).passed, true);
  assert.equal((await grade({ type: "tool_sequence", tools: ["c", "a"] }, cand)).passed, false);
});

test("tool_argument_assertion pass/fail", async () => {
  const cand = { tool_calls: [{ name: "search", arguments: { q: "cats" } }] };
  assert.equal((await grade({ type: "tool_argument_assertion", tool: "search", path: "q", equals: "cats" }, cand)).passed, true);
  assert.equal((await grade({ type: "tool_argument_assertion", tool: "search", path: "q", equals: "dogs" }, cand)).passed, false);
});

test("json_path_assertion equals is key-order-insensitive (parity with Python)", async () => {
  // Python compares dicts structurally (order-independent); TS must match.
  assert.equal(
    (await grade({ type: "json_path_assertion", path: "obj", equals: { a: 1, b: 2 } }, { obj: { b: 2, a: 1 } })).passed,
    true,
  );
  // A genuinely different value still fails in both engines.
  assert.equal(
    (await grade({ type: "json_path_assertion", path: "obj", equals: { a: 1, b: 2 } }, { obj: { a: 1, b: 3 } })).passed,
    false,
  );
  // Type mismatch (string "5" vs number 5) fails in both engines.
  assert.equal((await grade({ type: "json_path_assertion", path: "v", equals: 5 }, { v: "5" })).passed, false);
});

test("tool_argument_assertion equals is key-order-insensitive (parity with Python)", async () => {
  const cand = { tool_calls: [{ name: "search", arguments: { filters: { b: 2, a: 1 } } }] };
  assert.equal(
    (await grade({ type: "tool_argument_assertion", tool: "search", path: "filters", equals: { a: 1, b: 2 } }, cand)).passed,
    true,
  );
});

test("http_status pass/fail", async () => {
  assert.equal((await grade({ type: "http_status", status: 200 }, { status: 200 })).passed, true);
  assert.equal((await grade({ type: "http_status", status: 200 }, { status: 500 })).passed, false);
});

test("latency_threshold pass/fail", async () => {
  assert.equal((await grade({ type: "latency_threshold", p95_ms: 200 }, { p95_ms: 100 })).passed, true);
  assert.equal((await grade({ type: "latency_threshold", p95_ms: 200 }, { p95_ms: 300 })).passed, false);
});

test("cost_threshold pass/fail", async () => {
  assert.equal((await grade({ type: "cost_threshold", max_usd: 0.02 }, { cost_usd: 0.01 })).passed, true);
  assert.equal((await grade({ type: "cost_threshold", max_usd: 0.02 }, { cost_usd: 0.05 })).passed, false);
});

test("cost_threshold fails closed when cost absent", async () => {
  // A candidate with no cost data must NOT pass a ceiling it was never measured against.
  assert.equal((await grade({ type: "cost_threshold", max_usd: 0.02 }, {})).passed, false);
});

test("token_threshold pass/fail", async () => {
  assert.equal((await grade({ type: "token_threshold", max_tokens: 200 }, { tokens: 100 })).passed, true);
  assert.equal((await grade({ type: "token_threshold", max_tokens: 200 }, { tokens: 500 })).passed, false);
});

test("token_threshold fails closed when tokens absent", async () => {
  assert.equal((await grade({ type: "token_threshold", max_tokens: 200 }, {})).passed, false);
});

test("custom_webhook pass/fail with injected fetch + permissive ssrf", async () => {
  const okDeps = { ssrfCheck: permissiveSsrf, fetch: fakeFetch({ passed: true, reason: "approved" }) };
  assert.equal((await grade({ type: "custom_webhook", url: "http://hook.example.com/x" }, { a: 1 }, okDeps)).passed, true);
  const badDeps = { ssrfCheck: permissiveSsrf, fetch: fakeFetch({ passed: false }) };
  assert.equal((await grade({ type: "custom_webhook", url: "http://hook.example.com/x" }, { a: 1 }, badDeps)).passed, false);
});

test("custom_webhook to 169.254.169.254 is rejected by the SSRF guard (fail closed, no fetch)", async () => {
  const r = await grade({ type: "custom_webhook", url: "http://169.254.169.254/latest/meta-data/" }, { a: 1 }, { fetch: throwingFetch });
  assert.equal(r.passed, false);
  assert.match(r.reason.toLowerCase(), /ssrf|blocked/);
});

test("custom_webhook missing 'passed' fails closed", async () => {
  const deps = { ssrfCheck: permissiveSsrf, fetch: fakeFetch({ nope: 1 }) };
  assert.equal((await grade({ type: "custom_webhook", url: "http://hook.example.com/x" }, {}, deps)).passed, false);
});

test("custom_webhook non-2xx fails closed even with passed:true body", async () => {
  // A 500 that still returns {"passed":true} must NOT pass — HTTP status wins.
  const fetch500 = async () => ({ ok: false, status: 500, json: async () => ({ passed: true }) });
  const deps = { ssrfCheck: permissiveSsrf, fetch: fetch500 };
  assert.equal((await grade({ type: "custom_webhook", url: "http://hook.example.com/x" }, {}, deps)).passed, false);
});

test("llm_judge deterministic PASS/FAIL/ambiguous", async () => {
  const judge = (text) => ({ ssrfCheck: permissiveSsrf, fetch: fakeFetch({ output_text: text }) });
  assert.equal((await grade({ type: "llm_judge", rubric: "Correct?", gateway_url: "http://gw.example.com" }, "Paris", judge("PASS"))).passed, true);
  assert.equal((await grade({ type: "llm_judge", rubric: "Correct?", gateway_url: "http://gw.example.com" }, "Berlin", judge("FAIL"))).passed, false);
  assert.equal((await grade({ type: "llm_judge", rubric: "Correct?", gateway_url: "http://gw.example.com" }, "x", judge("maybe"))).passed, false);
});

test("llm_judge records the judge model in its verdict reason", async () => {
  const deps = { ssrfCheck: permissiveSsrf, fetch: fakeFetch({ output_text: "PASS" }) };
  const r = await grade({ type: "llm_judge", rubric: "Correct?", gateway_url: "http://gw.example.com", model: "claude-opus-4-8" }, "Paris", deps);
  assert.equal(r.passed, true);
  assert.match(r.reason, /claude-opus-4-8/);
});

test("llm_judge bias guard fails closed when judge shares SUT family", async () => {
  const deps = { ssrfCheck: permissiveSsrf, fetch: fakeFetch({ output_text: "PASS" }), subjectModel: "gpt-5.5-mini" };
  // Same family (both OpenAI) → must fail closed, never even call the judge.
  const same = await grade({ type: "llm_judge", rubric: "Correct?", gateway_url: "http://gw.example.com", model: "gpt-5.5" }, "Paris", deps);
  assert.equal(same.passed, false);
  assert.match(same.reason.toLowerCase(), /bias guard|same family|shares family/);
  // Different family judge → guard lets it through and the verdict stands.
  const diff = await grade({ type: "llm_judge", rubric: "Correct?", gateway_url: "http://gw.example.com", model: "claude-opus-4-8" }, "Paris", deps);
  assert.equal(diff.passed, true);
});

test("localization_f1 pass/fail + fail-closed parse", async () => {
  const ref = [{ path: "a.py", lines: [[1, 10]] }];
  // exact match -> both F1 = 1.0 -> pass
  assert.equal((await grade({ type: "localization_f1", reference: ref }, [{ path: "a.py", lines: [[1, 10]] }])).passed, true);
  // partial line overlap (IoU 6/14 ~ 0.43) below default 0.5 -> fail
  assert.equal((await grade({ type: "localization_f1", reference: ref }, [{ path: "a.py", lines: [[5, 14]] }])).passed, false);
  // unparseable candidate -> fail closed (never pass)
  const r = await grade({ type: "localization_f1", reference: ref }, 12345);
  assert.equal(r.passed, false);
  assert.match(r.reason.toLowerCase(), /unparseable|empty/);
  // missing reference -> fail closed
  assert.equal((await grade({ type: "localization_f1", reference: null }, [{ path: "a.py", lines: [[1, 10]] }])).passed, false);
});

test("unknown grader type fails closed", async () => {
  const r = await grade({ type: "totally_made_up" }, "x");
  assert.equal(r.passed, false);
  assert.match(r.reason.toLowerCase(), /unknown grader/);
});
