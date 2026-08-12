/**
 * Runtime tests for the eval grader taxonomy (all 16 types).
 * Run with: node --test packages/evals/tests/grade.runtime.mjs
 * (Node 22, built-in node:test. Requires `tsc` build to dist first.)
 */
import { test } from "node:test";
import assert from "node:assert/strict";

import { grade } from "../dist/index.js";

const permissiveSsrf = async () => ({ allowed: true, reason: "ok" });
const fakeFetch = (responseObj, status = 200) => async () => ({ ok: true, status, json: async () => responseObj });
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

test("json_schema rejects malformed supported keyword shapes", async () => {
  const malformedSchemas = [
    { type: [] },
    { type: ["string", "string"] },
    { type: ["string", "strng"] },
    { enum: "ok" },
    { enum: [] },
    { required: "id" },
    { required: ["id", 1] },
    { required: ["id", "id"] },
    { properties: [] },
    { properties: { id: "string" } },
    { items: "string" },
    { items: { type: "strng" } },
  ];
  for (const schema of malformedSchemas) {
    const result = await grade({ type: "json_schema", schema }, { id: "ok" });
    assert.equal(result.passed, false, `malformed schema must fail closed: ${JSON.stringify(schema)}`);
  }

  const valid = {
    type: "array",
    items: { type: ["string", "null"] },
    enum: [["ok", null]],
  };
  assert.equal((await grade({ type: "json_schema", schema: valid }, ["ok", null])).passed, true);
});

test("json_schema and json paths ignore inherited candidate properties", async () => {
  const inherited = Object.create({ secret: "not-an-own-property", wrong: 42 });
  assert.equal(
    (await grade({ type: "json_path_assertion", path: "secret", exists: true }, inherited)).passed,
    false,
  );
  assert.equal(
    (await grade({ type: "json_schema", schema: { type: "object", required: ["secret"] } }, inherited)).passed,
    false,
  );
  // `properties` must not validate inherited values. With no own `wrong` key,
  // this schema has nothing to validate and therefore passes.
  assert.equal(
    (await grade({ type: "json_schema", schema: { type: "object", properties: { wrong: { type: "string" } } } }, inherited)).passed,
    true,
  );
});

test("json paths reject sparse array holes", async () => {
  const sparse = [];
  sparse.length = 1;
  assert.equal((await grade({ type: "json_path_assertion", path: "0", exists: true }, sparse)).passed, false);
});

test("json_schema enum uses JSON Schema value equality", async () => {
  // JSON Schema equality keeps booleans distinct from numbers, compares all
  // numeric representations by value, ignores object key order recursively,
  // preserves array order, and treats null as its own value.
  assert.equal((await grade({ type: "json_schema", schema: { enum: [true] } }, true)).passed, true);
  assert.equal((await grade({ type: "json_schema", schema: { enum: [true] } }, 1)).passed, false);
  assert.equal((await grade({ type: "json_schema", schema: { enum: [false] } }, 0)).passed, false);
  assert.equal((await grade({ type: "json_schema", schema: { enum: [1] } }, 1.0)).passed, true);
  const nested = { outer: { right: [{ keep: 1, nil: null }, 2], left: "x" } };
  const reordered = { outer: { left: "x", right: [{ nil: null, keep: 1 }, 2] } };
  assert.equal((await grade({ type: "json_schema", schema: { enum: [nested] } }, reordered)).passed, true);
  assert.equal((await grade({ type: "json_schema", schema: { enum: [[1, 2]] } }, [2, 1])).passed, false);
  assert.equal((await grade({ type: "json_schema", schema: { enum: [null] } }, null)).passed, true);
  assert.equal((await grade({ type: "json_schema", schema: { enum: [null] } }, {})).passed, false);
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

test("custom_webhook accepts only a primitive boolean verdict", async () => {
  for (const passed of ["true", "false", 1, 0, null, {}, []]) {
    const deps = { ssrfCheck: permissiveSsrf, fetch: fakeFetch({ passed }) };
    const r = await grade({ type: "custom_webhook", url: "http://hook.example.com/x" }, {}, deps);
    assert.equal(r.passed, false, `malformed passed=${JSON.stringify(passed)} must fail closed`);
    assert.match(r.reason, /primitive boolean/);
  }
  assert.equal(
    (await grade({ type: "custom_webhook", url: "http://hook.example.com/x" }, {}, {
      ssrfCheck: permissiveSsrf,
      fetch: fakeFetch({ passed: true, reason: "approved" }),
    })).passed,
    true,
  );
});

test("network graders require primitive true from the SSRF guard", async () => {
  const malformedAllowed = [false, "true", 1, 0, null, {}, [], undefined];
  for (const allowed of malformedAllowed) {
    let fetchCalls = 0;
    const fetch = async () => {
      fetchCalls += 1;
      return { ok: true, status: 200, json: async () => ({ passed: true, output_text: "PASS" }) };
    };
    const ssrfCheck = async () => ({ allowed, reason: "test" });
    const webhook = await grade(
      { type: "custom_webhook", url: "http://hook.example.com/x" },
      {},
      { ssrfCheck, fetch },
    );
    assert.equal(webhook.passed, false, `malformed allowed=${JSON.stringify(allowed)} must fail closed`);
    assert.equal(fetchCalls, 0, "blocked webhook must not fetch");

    const judge = await grade(
      { type: "llm_judge", rubric: "Correct?", gateway_url: "http://gw.example.com" },
      "candidate",
      { ssrfCheck, fetch },
    );
    assert.equal(judge.passed, false, `malformed allowed=${JSON.stringify(allowed)} must fail closed`);
    assert.equal(fetchCalls, 0, "blocked judge must not fetch");
  }
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

test("llm_judge rejects every non-2xx response before parsing a PASS body", async () => {
  for (const status of [400, 404, 500, 502]) {
    let jsonCalls = 0;
    const fetchError = async () => ({
      ok: false,
      status,
      json: async () => {
        jsonCalls += 1;
        return { output_text: "PASS" };
      },
    });
    const r = await grade(
      { type: "llm_judge", rubric: "Correct?", gateway_url: "http://gw.example.com" },
      "candidate",
      { ssrfCheck: permissiveSsrf, fetch: fetchError },
    );
    assert.equal(r.passed, false, `HTTP ${status} must not authorize PASS`);
    assert.match(r.reason, /non-2xx/);
    assert.equal(jsonCalls, 0, `HTTP ${status} body must not be parsed`);
  }
});

test("llm_judge fails closed when a successful response body cannot be parsed", async () => {
  const deps = {
    ssrfCheck: permissiveSsrf,
    fetch: async () => ({ ok: true, status: 200, json: async () => { throw new Error("invalid JSON"); } }),
  };
  const r = await grade({ type: "llm_judge", rubric: "Correct?", gateway_url: "http://gw.example.com" }, "candidate", deps);
  assert.equal(r.passed, false);
  assert.match(r.reason, /judge call failed/);
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

test("localization_f1 threshold validation is finite, ranged, and fail-closed", async () => {
  const candidate = [{ path: "wrong.py", lines: [[1, 1]] }];
  const reference = [{ path: "right.py", lines: [[1, 1]] }];

  // Omitted options preserve the conservative default. Valid boundary values
  // are accepted, but disjoint zero-quality evidence never passes at threshold 0.
  assert.equal((await grade({ type: "localization_f1", reference }, candidate)).passed, false);
  assert.equal((await grade({ type: "localization_f1", reference, threshold: 0 }, candidate)).passed, false);
  assert.equal((await grade({ type: "localization_f1", reference, threshold: 0 }, reference)).passed, true);
  assert.equal((await grade({ type: "localization_f1", reference, threshold: 1 }, reference)).passed, true);

  const invalidValues = [-1, 1.0001, true, "0.5", null, Number.NaN, Number.POSITIVE_INFINITY];
  for (const field of ["threshold", "file_threshold", "line_threshold"]) {
    for (const value of invalidValues) {
      const options = { [field]: value };
      const r = await grade({ type: "localization_f1", reference, ...options }, candidate);
      assert.equal(r.passed, false, JSON.stringify(options));
      assert.match(r.reason, /invalid threshold/);
    }
  }
});

test("grade rejects malformed runtime grader configurations across string/array-backed types", async () => {
  const malformed = [
    ["missing grader", undefined, "candidate"],
    ["null grader", null, "candidate"],
    ["array grader", [], "candidate"],
    ["missing type", {}, "candidate"],
    ["non-string type", { type: 42 }, "candidate"],
    ["unknown type", { type: "secret-grader-type" }, "candidate"],
    ["contains null fragments", { type: "contains", fragments: null }, undefined],
    ["contains non-string fragment", { type: "contains", fragments: ["ok", null] }, undefined],
    ["regex null pattern", { type: "regex", pattern: null }, undefined],
    ["json schema null schema", { type: "json_schema", schema: null }, undefined],
    ["json path null path", { type: "json_path_assertion", path: null }, undefined],
    ["tool called null tools", { type: "tool_called", tools: null }, undefined],
    ["tool not called wrong tools", { type: "tool_not_called", tools: "search" }, undefined],
    ["tool sequence empty tools", { type: "tool_sequence", tools: [] }, undefined],
    ["tool argument null tool", { type: "tool_argument_assertion", tool: null, path: "id", equals: 1 }, undefined],
    ["tool argument missing equals", { type: "tool_argument_assertion", tool: "search", path: "id" }, undefined],
    ["http status string", { type: "http_status", status: "200" }, undefined],
    ["latency threshold string", { type: "latency_threshold", p95_ms: "200" }, undefined],
    ["cost threshold null", { type: "cost_threshold", max_usd: null }, undefined],
    ["token threshold NaN", { type: "token_threshold", max_tokens: Number.NaN }, undefined],
    ["custom webhook null url", { type: "custom_webhook", url: null }, undefined],
    ["localization missing reference", { type: "localization_f1" }, undefined],
    ["llm judge null rubric", { type: "llm_judge", rubric: null, gateway_url: "http://gw.example.com" }, undefined],
  ];

  for (const [name, grader, candidate] of malformed) {
    const result = await grade(grader, candidate);
    assert.equal(result.passed, false, `${name} must fail closed`);
    assert.equal(typeof result.reason, "string", `${name} must return a reason`);
  }
});

test("grade rejects omitted candidates and never rejects null candidates", async () => {
  const graders = [
    { type: "exact_match", expected: "candidate" },
    { type: "contains", fragments: ["candidate"] },
    { type: "regex", pattern: "candidate" },
    { type: "json_schema", schema: { type: "string" } },
    { type: "json_path_assertion", path: "value", exists: true },
    { type: "tool_called", tools: ["search"] },
    { type: "tool_not_called", tools: ["search"] },
    { type: "tool_sequence", tools: ["search"] },
    { type: "tool_argument_assertion", tool: "search", path: "id", equals: 1 },
    { type: "http_status", status: 200 },
    { type: "latency_threshold", p95_ms: 200 },
    { type: "cost_threshold", max_usd: 1 },
    { type: "token_threshold", max_tokens: 100 },
    { type: "localization_f1", reference: [{ path: "a.ts", lines: [[1, 1]] }] },
    { type: "custom_webhook", url: "http://hook.example.com/x" },
    { type: "llm_judge", rubric: "is it good?", gateway_url: "http://gw.example.com" },
  ];

  for (const graderConfig of graders) {
    const missing = await grade(graderConfig, undefined);
    assert.equal(missing.passed, false, `${graderConfig.type} must reject an omitted candidate`);
    assert.equal(missing.reason, "candidate is missing");
  }

  // null is a valid JSON value for some graders; it must still be handled as
  // a value (never reject) rather than being blanket-rejected at the boundary.
  for (const graderConfig of [
    { type: "exact_match", expected: "candidate" },
    { type: "contains", fragments: ["candidate"] },
    { type: "regex", pattern: "candidate" },
  ]) {
    const result = await grade(graderConfig, null);
    assert.equal(result.passed, false);
    assert.equal(typeof result.reason, "string");
  }
});

test("grade catches throwing dependencies and regex/config exceptions without leaking details", async () => {
  const secret = "super-secret-eval-payload";
  const throwingSsrf = async () => {
    throw new Error(secret);
  };
  const throwingFetch = async () => {
    throw new Error(secret);
  };

  const webhookFromSsrf = await grade(
    { type: "custom_webhook", url: "http://hook.example.com/x" },
    "candidate",
    { ssrfCheck: throwingSsrf },
  );
  assert.equal(webhookFromSsrf.passed, false);
  assert.doesNotMatch(webhookFromSsrf.reason, new RegExp(secret));

  const webhookFromFetch = await grade(
    { type: "custom_webhook", url: "http://hook.example.com/x" },
    "candidate",
    { ssrfCheck: permissiveSsrf, fetch: throwingFetch },
  );
  assert.equal(webhookFromFetch.passed, false);
  assert.doesNotMatch(webhookFromFetch.reason, new RegExp(secret));

  const judgeFromSsrf = await grade(
    { type: "llm_judge", rubric: "is it good?", gateway_url: "http://gw.example.com" },
    "candidate",
    { ssrfCheck: throwingSsrf },
  );
  assert.equal(judgeFromSsrf.passed, false);
  assert.doesNotMatch(judgeFromSsrf.reason, new RegExp(secret));

  const invalidRegex = await grade({ type: "regex", pattern: `[${secret}` }, "candidate");
  assert.equal(invalidRegex.passed, false);
  assert.doesNotMatch(invalidRegex.reason, new RegExp(secret));

  const throwingConfig = {};
  Object.defineProperty(throwingConfig, "type", {
    get() {
      throw new Error(secret);
    },
  });
  const configResult = await grade(throwingConfig, "candidate");
  assert.equal(configResult.passed, false);
  assert.doesNotMatch(configResult.reason, new RegExp(secret));
});

test("grade snapshots validated config values and sanitizes SSRF diagnostics", async () => {
  let reads = 0;
  const changingConfig = {
    type: "contains",
    get fragments() {
      reads += 1;
      return reads === 1 ? ["missing"] : [];
    },
  };
  const changingResult = await grade(changingConfig, "candidate");
  assert.equal(changingResult.passed, false, "dispatch must use the validated snapshot");
  assert.equal(reads, 1, "config accessors must not be re-read after validation");

  const secret = "secret-ssrf-diagnostic";
  const blocked = async () => ({ allowed: false, reason: secret });
  const webhook = await grade(
    { type: "custom_webhook", url: "http://hook.example.com/x" },
    "candidate",
    { ssrfCheck: blocked },
  );
  assert.equal(webhook.passed, false);
  assert.doesNotMatch(webhook.reason, new RegExp(secret));

  const judge = await grade(
    { type: "llm_judge", rubric: "is it good?", gateway_url: "http://gw.example.com" },
    "candidate",
    { ssrfCheck: blocked },
  );
  assert.equal(judge.passed, false);
  assert.doesNotMatch(judge.reason, new RegExp(secret));
});

test("grade snapshots string-array elements exactly once", async () => {
  let reads = 0;
  const fragments = [];
  fragments.length = 1;
  Object.defineProperty(fragments, 0, {
    configurable: true,
    get() {
      reads += 1;
      return reads === 1 ? "needle" : null;
    },
  });
  const result = await grade({ type: "contains", fragments }, "needle");
  assert.equal(result.passed, true, "dispatch must use the validated array snapshot");
  assert.equal(reads, 1, "array element accessors must not be re-read after validation");
});

test("unknown grader type fails closed", async () => {
  const r = await grade({ type: "totally_made_up" }, "x");
  assert.equal(r.passed, false);
  assert.match(r.reason.toLowerCase(), /unknown grader/);
});
