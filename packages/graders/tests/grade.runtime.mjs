/**
 * Runtime tests for the eval grader taxonomy (all 27 types).
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

test("regex rejects catastrophic nested quantifiers before execution", async () => {
  const started = Date.now();
  for (const pattern of ["^(a+)+$", "^((a+))+$"]) {
    const result = await grade({ type: "regex", pattern }, "a".repeat(30) + "!");
    assert.equal(result.passed, false);
    assert.match(result.reason, /catastrophic|safety/);
  }
  assert.ok(Date.now() - started < 250, "unsafe regex reached JavaScript's backtracking engine");
});

test("regex worker deadline stops catastrophic adjacent quantifiers", async () => {
  const started = Date.now();
  const result = await grade(
    { type: "regex", pattern: "^a*a*a*a*a*a*a*a*b$" },
    "a".repeat(50),
  );
  assert.equal(result.passed, false);
  // Must be the EXECUTION deadline (worker came online, regex ran too long),
  // not the startup guard — they are different failure modes.
  assert.match(result.reason, /execution deadline/);
  // Budget covers worker startup (up to the 5s guard) plus the 100ms
  // execution deadline; a loaded CI runner can spend seconds on spawn alone.
  assert.ok(Date.now() - started < 5_500, "regex worker deadline did not settle promptly");
});

test("hostname SSRF checks require a fetch transport that pins the checked resolution", async () => {
  const result = await grade(
    { type: "custom_webhook", url: "https://rebind.example/hook" },
    {},
    { ssrfCheck: permissiveSsrf, fetch: fakeFetch({ passed: true }) },
  );
  assert.equal(result.passed, false);
  assert.match(result.reason, /resolution-pinned fetch transport/);
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

test("json schema and path lookup ignore inherited prototype keys", async () => {
  assert.equal((await grade({ type: "json_schema", schema: { type: "object", required: ["toString"] } }, {})).passed, false);
  assert.equal((await grade({ type: "json_path_assertion", path: "constructor", exists: false }, {})).passed, true);
  assert.equal((await grade({ type: "json_path_assertion", path: "__proto__", exists: false }, {})).passed, true);
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

test("threshold graders reject negative candidates and configurations", async () => {
  for (const [grader, candidate] of [
    [{ type: "latency_threshold", p95_ms: 10 }, { latency_ms: -1 }],
    [{ type: "cost_threshold", max_usd: 1 }, { cost_usd: -1 }],
    [{ type: "token_threshold", max_tokens: 10 }, { tokens: -1 }],
    [{ type: "latency_threshold", p95_ms: -1 }, { latency_ms: 0 }],
    [{ type: "cost_threshold", max_usd: -1 }, { cost_usd: 0 }],
    [{ type: "token_threshold", max_tokens: -1 }, { tokens: 0 }],
  ]) {
    assert.equal((await grade(grader, candidate)).passed, false);
  }
});

test("custom_webhook pass/fail with injected fetch + permissive ssrf", async () => {
  const okDeps = { ssrfCheck: permissiveSsrf, resolutionPinnedFetch: fakeFetch({ passed: true, reason: "approved" }) };
  assert.equal((await grade({ type: "custom_webhook", url: "http://hook.example.com/x" }, { a: 1 }, okDeps)).passed, true);
  const badDeps = { ssrfCheck: permissiveSsrf, resolutionPinnedFetch: fakeFetch({ passed: false }) };
  assert.equal((await grade({ type: "custom_webhook", url: "http://hook.example.com/x" }, { a: 1 }, badDeps)).passed, false);
});

test("custom_webhook to 169.254.169.254 is rejected by the SSRF guard (fail closed, no fetch)", async () => {
  const r = await grade({ type: "custom_webhook", url: "http://169.254.169.254/latest/meta-data/" }, { a: 1 }, { resolutionPinnedFetch: throwingFetch });
  assert.equal(r.passed, false);
  assert.match(r.reason.toLowerCase(), /ssrf|blocked/);
});

test("custom_webhook blocks canonical IPv4-mapped, link-local, and transition IPv6 literals", async () => {
  for (const url of [
    "http://[::ffff:127.0.0.1]/",
    "http://[fe90::1]/",
    "http://[2002:7f00:1::]/",
    "http://[2001:0000::1]/",
  ]) {
    const result = await grade({ type: "custom_webhook", url }, {}, { resolutionPinnedFetch: throwingFetch });
    assert.equal(result.passed, false, url);
    assert.match(result.reason, /blocked address/, url);
  }
});

test("network grader deadline covers stalled SSRF, headers, and response body", async () => {
  const cases = [
    { ssrfCheck: async () => new Promise(() => {}), resolutionPinnedFetch: throwingFetch },
    { ssrfCheck: permissiveSsrf, resolutionPinnedFetch: async () => new Promise(() => {}) },
    { ssrfCheck: permissiveSsrf, resolutionPinnedFetch: async () => ({ ok: true, status: 200, json: async () => new Promise(() => {}) }) },
  ];
  for (const deps of cases) {
    const started = Date.now();
    const result = await grade(
      { type: "custom_webhook", url: "http://hook.example.com/x" },
      {},
      { ...deps, networkTimeoutMs: 10 },
    );
    assert.equal(result.passed, false);
    assert.match(result.reason, /timeout/);
    assert.ok(Date.now() - started < 250);
  }
});

test("custom_webhook missing 'passed' fails closed", async () => {
  const deps = { ssrfCheck: permissiveSsrf, resolutionPinnedFetch: fakeFetch({ nope: 1 }) };
  assert.equal((await grade({ type: "custom_webhook", url: "http://hook.example.com/x" }, {}, deps)).passed, false);
});

test("custom_webhook non-2xx fails closed even with passed:true body", async () => {
  // A 500 that still returns {"passed":true} must NOT pass — HTTP status wins.
  const fetch500 = async () => ({ ok: false, status: 500, json: async () => ({ passed: true }) });
  const deps = { ssrfCheck: permissiveSsrf, resolutionPinnedFetch: fetch500 };
  assert.equal((await grade({ type: "custom_webhook", url: "http://hook.example.com/x" }, {}, deps)).passed, false);
});

test("custom_webhook redirect fails closed and is never followed", async () => {
  // The SSRF guard only saw the url the operator supplied. A 302 to somewhere it
  // never checked is refused, even though the redirect target would have passed.
  const seen = [];
  const fetch302 = async (_url, init) => {
    seen.push(init);
    return { ok: false, status: 302, json: async () => ({ passed: true, reason: "redirect target" }) };
  };
  const deps = { ssrfCheck: permissiveSsrf, resolutionPinnedFetch: fetch302 };
  const r = await grade({ type: "custom_webhook", url: "http://hook.example.com/x" }, {}, deps);
  assert.equal(r.passed, false);
  assert.match(r.reason, /redirect refused/);
  assert.doesNotMatch(r.reason, /redirect target/);
  // The refusal is enforced at the transport too, not only by reading the status.
  assert.equal(seen.length, 1);
  assert.equal(seen[0].redirect, "manual");
});

test("llm_judge deterministic PASS/FAIL/ambiguous", async () => {
  const judge = (text) => ({ ssrfCheck: permissiveSsrf, resolutionPinnedFetch: fakeFetch({ output_text: text }) });
  assert.equal((await grade({ type: "llm_judge", rubric: "Correct?", gateway_url: "http://gw.example.com" }, "Paris", judge("PASS"))).passed, true);
  assert.equal((await grade({ type: "llm_judge", rubric: "Correct?", gateway_url: "http://gw.example.com" }, "Berlin", judge("FAIL"))).passed, false);
  assert.equal((await grade({ type: "llm_judge", rubric: "Correct?", gateway_url: "http://gw.example.com" }, "x", judge("maybe"))).passed, false);
});

test("llm_judge records the judge model in its verdict reason", async () => {
  const deps = { ssrfCheck: permissiveSsrf, resolutionPinnedFetch: fakeFetch({ output_text: "PASS" }) };
  const r = await grade({ type: "llm_judge", rubric: "Correct?", gateway_url: "http://gw.example.com", model: "claude-opus-4-8" }, "Paris", deps);
  assert.equal(r.passed, true);
  assert.match(r.reason, /claude-opus-4-8/);
});

test("llm_judge bias guard fails closed when judge shares SUT family", async () => {
  const deps = { ssrfCheck: permissiveSsrf, resolutionPinnedFetch: fakeFetch({ output_text: "PASS" }), subjectModel: "gpt-5.5-mini" };
  // Same family (both OpenAI) → must fail closed, never even call the judge.
  const same = await grade({ type: "llm_judge", rubric: "Correct?", gateway_url: "http://gw.example.com", model: "gpt-5.5" }, "Paris", deps);
  assert.equal(same.passed, false);
  assert.match(same.reason.toLowerCase(), /bias guard|same family|shares family/);
  // Different family judge → guard lets it through and the verdict stands.
  const diff = await grade({ type: "llm_judge", rubric: "Correct?", gateway_url: "http://gw.example.com", model: "claude-opus-4-8" }, "Paris", deps);
  assert.equal(diff.passed, true);
});

test("every judge type refuses a redirecting gateway and is never followed", async () => {
  // Same rule as custom_webhook, on the shared judge transport: `llm_judge`'s
  // frozen non-2xx behaviour does not extend to a 3xx, because a redirect leaves
  // the gateway the SSRF guard actually checked.
  const graders = [
    { type: "llm_judge", rubric: "Correct?", gateway_url: "http://gw.example.com" },
    { type: "llm_score", rubric: "Correct?", min_score: 0.5, gateway_url: "http://gw.example.com" },
    { type: "llm_category", prompt: "Classify the tone.", categories: ["good", "bad"], passing_categories: ["good"], gateway_url: "http://gw.example.com" },
    { type: "llm_pairwise", baseline: "b", criteria: "better?", gateway_url: "http://gw.example.com" },
    { type: "llm_answer_match", expected: "Paris", gateway_url: "http://gw.example.com" },
  ];
  for (const grader of graders) {
    const seen = [];
    const fetch302 = async (_url, init) => {
      seen.push(init);
      return { ok: false, status: 302, json: async () => ({ output_text: "SCORE: 1.0\nCATEGORY: good\nWINNER: A\nMATCH: YES\nPASS" }) };
    };
    const r = await grade(grader, "Paris", { ssrfCheck: permissiveSsrf, resolutionPinnedFetch: fetch302 });
    assert.equal(r.passed, false, `${grader.type} passed through a redirect`);
    assert.match(r.reason, /redirect refused/);
    assert.equal(seen[0].redirect, "manual");
  }
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

// ─── langevals-ported types (7) + exact_match knobs ──────────────────────────
// Behavioral reference: langevals (MIT, (c) 2024 Reasoning Engine B.V.), reimplemented.
// Every new type is exercised for pass, fail AND option validation: upstream langevals
// skips on missing/empty input, Caveman fails closed instead.

test("not_contains mirrors contains, inverted quantifier", async () => {
  assert.equal((await grade({ type: "not_contains", fragments: ["error", "traceback"] }, "all good")).passed, true);
  assert.equal((await grade({ type: "not_contains", fragments: ["error"] }, "error: boom")).passed, false);
  // ANY fragment present fails, even when the others are absent.
  assert.equal((await grade({ type: "not_contains", fragments: ["nope", "boom"] }, "error: boom")).passed, false);
  // Substring matching is case-sensitive, exactly like `contains`.
  assert.equal((await grade({ type: "not_contains", fragments: ["Error"] }, "error: boom")).passed, true);
});

test("not_contains fails closed on an empty/blank fragment list", async () => {
  const empty = await grade({ type: "not_contains", fragments: [] }, "anything");
  assert.equal(empty.passed, false);
  assert.match(empty.reason, /at least one fragment/);
  assert.equal((await grade({ type: "not_contains", fragments: [""] }, "anything")).passed, false);
  assert.equal((await grade({ type: "not_contains", fragments: "error" }, "error")).passed, false);
});

test("not_regex mirrors the regex engine, inverted verdict", async () => {
  assert.equal((await grade({ type: "not_regex", pattern: "order-\\d+" }, "order-abc")).passed, true);
  assert.equal((await grade({ type: "not_regex", pattern: "order-\\d+" }, "order-123")).passed, false);
  // Unanchored search semantics, same as `regex`.
  assert.equal((await grade({ type: "not_regex", pattern: "^denied$" }, "request denied by policy")).passed, true);
});

test("not_regex fails closed on an empty or invalid pattern", async () => {
  const empty = await grade({ type: "not_regex", pattern: "" }, "anything");
  assert.equal(empty.passed, false);
  assert.match(empty.reason, /non-empty pattern/);
  const invalid = await grade({ type: "not_regex", pattern: "(" }, "anything");
  assert.equal(invalid.passed, false);
  assert.match(invalid.reason, /invalid regex/);
});

test("blocklist matches whole words, case-insensitively", async () => {
  const r = await grade({ type: "blocklist", terms: ["OpenAI", "Google"] }, "We evaluated openai and Anthropic");
  assert.equal(r.passed, false);
  assert.match(r.reason, /OpenAI/);
  assert.equal((await grade({ type: "blocklist", terms: ["OpenAI"] }, "We evaluated Anthropic only")).passed, true);
  // Whole-word: "cat" must not match inside "concatenate" (pinned lookarounds, not \b).
  assert.equal((await grade({ type: "blocklist", terms: ["cat"] }, "concatenate the strings")).passed, true);
  // A non-alphanumeric neighbour is still a word boundary.
  assert.equal((await grade({ type: "blocklist", terms: ["openai"] }, "an openai-compatible endpoint")).passed, false);
});

test("blocklist treats terms literally (regex metacharacters escaped)", async () => {
  assert.equal((await grade({ type: "blocklist", terms: ["a.b"] }, "axb is fine")).passed, true);
  assert.equal((await grade({ type: "blocklist", terms: ["a.b"] }, "a.b is not")).passed, false);
});

test("blocklist fails closed on empty or blank terms", async () => {
  const empty = await grade({ type: "blocklist", terms: [] }, "anything");
  assert.equal(empty.passed, false);
  assert.match(empty.reason, /at least one non-empty term/);
  assert.equal((await grade({ type: "blocklist", terms: [""] }, "anything")).passed, false);
  assert.equal((await grade({ type: "blocklist", terms: [42] }, "anything")).passed, false);
});

test("bleu_score identical text scores 1.0, disjoint text fails", async () => {
  const identical = await grade({ type: "bleu_score", reference: "the cat sat on the mat" }, "the cat sat on the mat");
  assert.equal(identical.passed, true);
  assert.match(identical.reason, /score=1\.0000/);
  // Disjoint tokens hit the smoothing branch (clipped == 0 => 1/(2*count)) => 0.2259.
  const disjoint = await grade({ type: "bleu_score", reference: "one two three four" }, "alpha beta gamma delta");
  assert.equal(disjoint.passed, false);
  assert.match(disjoint.reason, /score=0\.2259/);
});

test("bleu_score applies the brevity penalty to short candidates", async () => {
  // Perfect 1- and 2-gram precision, but 2 candidate tokens vs 6 reference tokens:
  // BP = exp(1 - 6/2) = 0.1353, which is the whole score.
  const short = await grade({ type: "bleu_score", reference: "the cat sat on the mat" }, "the cat");
  assert.equal(short.passed, false);
  assert.match(short.reason, /score=0\.1353/);
  assert.equal((await grade({ type: "bleu_score", reference: "the cat sat on the mat", threshold: 0.1 }, "the cat")).passed, true);
});

test("bleu_score threshold boundary is inclusive", async () => {
  assert.equal((await grade({ type: "bleu_score", reference: "a b c", threshold: 1 }, "a b c")).passed, true);
});

test("bleu_score option validation fails closed", async () => {
  const noRef = await grade({ type: "bleu_score", reference: "   " }, "hello");
  assert.equal(noRef.passed, false);
  assert.match(noRef.reason, /non-empty reference/);
  assert.equal((await grade({ type: "bleu_score", reference: 5 }, "hello")).passed, false);
  const badThreshold = await grade({ type: "bleu_score", reference: "hello", threshold: 1.5 }, "hello");
  assert.equal(badThreshold.passed, false);
  assert.match(badThreshold.reason, /threshold must be a number in \[0,1\]/);
  assert.equal((await grade({ type: "bleu_score", reference: "hello", threshold: "0.5" }, "hello")).passed, false);
  // Only an ABSENT key defaults; an explicit null/boolean is an invalid option.
  assert.equal((await grade({ type: "bleu_score", reference: "hello", threshold: null }, "hello")).passed, false);
  assert.equal((await grade({ type: "bleu_score", reference: "hello", threshold: true }, "hello")).passed, false);
  // Nothing tokenizable on either side is unjudgeable, never a silent pass.
  const empty = await grade({ type: "bleu_score", reference: "hello" }, "   ");
  assert.equal(empty.passed, false);
  assert.match(empty.reason, /empty after tokenization/);
});

test("rouge_score rouge_1 fmeasure on a half-overlap", async () => {
  // 2 of 3 unigrams overlap both ways => P = R = F = 2/3.
  const r = await grade({ type: "rouge_score", reference: "the dog sat" }, "the cat sat");
  assert.equal(r.passed, true);
  assert.match(r.reason, /rouge_1 fmeasure score=0\.6667/);
  assert.equal((await grade({ type: "rouge_score", reference: "the dog sat", threshold: 0.7 }, "the cat sat")).passed, false);
});

test("rouge_l is order-sensitive where rouge_1 is not", async () => {
  const unigram = await grade({ type: "rouge_score", reference: "a b", threshold: 0.9 }, "b a");
  assert.equal(unigram.passed, true);
  assert.match(unigram.reason, /score=1\.0000/);
  // LCS of [b,a] and [a,b] is 1 => P = R = F = 0.5 (boundary equality passes at 0.5).
  const lcs = await grade({ type: "rouge_score", reference: "a b", rouge_type: "rouge_l", threshold: 0.9 }, "b a");
  assert.equal(lcs.passed, false);
  assert.match(lcs.reason, /rouge_l fmeasure score=0\.5000/);
  assert.equal((await grade({ type: "rouge_score", reference: "a b", rouge_type: "rouge_l", threshold: 0.5 }, "b a")).passed, true);
});

test("rouge_score precision and recall measures differ", async () => {
  const opts = { type: "rouge_score", reference: "b d", rouge_type: "rouge_l", threshold: 0.9 };
  const precision = await grade({ ...opts, measure: "precision" }, "a b c d");
  assert.equal(precision.passed, false);
  assert.match(precision.reason, /precision score=0\.5000/);
  const recall = await grade({ ...opts, measure: "recall" }, "a b c d");
  assert.equal(recall.passed, true);
  assert.match(recall.reason, /recall score=1\.0000/);
});

test("rouge_score unknown enum values fail closed", async () => {
  const badType = await grade({ type: "rouge_score", reference: "a b", rouge_type: "rouge_2" }, "a b");
  assert.equal(badType.passed, false);
  assert.match(badType.reason, /unknown rouge_type/);
  // "f1" is context_f1's spelling; rouge_score uses "fmeasure" — a near miss must fail.
  const badMeasure = await grade({ type: "rouge_score", reference: "a b", measure: "f1" }, "a b");
  assert.equal(badMeasure.passed, false);
  assert.match(badMeasure.reason, /unknown measure/);
  // An explicit null does not fall back to the default enum value.
  assert.equal((await grade({ type: "rouge_score", reference: "a b", rouge_type: null }, "a b")).passed, false);
});

test("rouge_score option validation fails closed", async () => {
  assert.equal((await grade({ type: "rouge_score", reference: "" }, "a b")).passed, false);
  assert.equal((await grade({ type: "rouge_score", reference: "a b", threshold: -0.1 }, "a b")).passed, false);
  const empty = await grade({ type: "rouge_score", reference: "a b" }, "");
  assert.equal(empty.passed, false);
  assert.match(empty.reason, /empty after tokenization/);
});

test("context_f1 scores retrieved vs expected context lists", async () => {
  // Both lists arrive on the grader options; the candidate value is unused.
  const exact = await grade(
    { type: "context_f1", retrieved: ["Paris is the capital of France"], expected: ["paris   is the  capital of france"] },
    null,
  );
  assert.equal(exact.passed, true);
  assert.match(exact.reason, /f1 score=1\.0000/);
  // 1 of 2 retrieved items matches, 1 of 1 expected covered => P=0.5, R=1, F1=0.6667.
  const partial = { type: "context_f1", retrieved: ["doc about cats", "totally unrelated string here"], expected: ["doc about cats"] };
  const f1 = await grade(partial, null);
  assert.equal(f1.passed, true);
  assert.match(f1.reason, /f1 score=0\.6667/);
  assert.equal((await grade({ ...partial, threshold: 0.7 }, null)).passed, false);
});

test("context_f1 measures and similarity_threshold", async () => {
  const partial = { type: "context_f1", retrieved: ["doc about cats", "totally unrelated string here"], expected: ["doc about cats"] };
  const precision = await grade({ ...partial, measure: "precision", threshold: 0.6 }, null);
  assert.equal(precision.passed, false);
  assert.match(precision.reason, /precision score=0\.5000/);
  assert.equal((await grade({ ...partial, measure: "recall" }, null)).passed, true);
  // "colour" vs "color": normalised Levenshtein similarity = 1 - 1/6 = 0.8333.
  const fuzzy = { type: "context_f1", retrieved: ["colour"], expected: ["color"] };
  assert.equal((await grade(fuzzy, null)).passed, true);
  assert.equal((await grade({ ...fuzzy, similarity_threshold: 0.9 }, null)).passed, false);
});

test("context_f1 fails closed when either list is entirely blank", async () => {
  // Two empty strings ARE identical by the similarity definition (1.0), which is
  // exactly why the all-blank case must be rejected before scoring: a retrieval gate
  // that passes on nothing retrieved and nothing expected is a gate that never fires.
  const both = await grade({ type: "context_f1", retrieved: [""], expected: [""] }, null);
  assert.equal(both.passed, false);
  assert.match(both.reason, /normalises to empty/);
  // Whitespace-only members normalise away too (pinned ASCII whitespace class).
  assert.equal((await grade({ type: "context_f1", retrieved: [" \t\n"], expected: ["a"] }, null)).passed, false);
  assert.equal((await grade({ type: "context_f1", retrieved: ["a"], expected: ["", "  "] }, null)).passed, false);
  // A blank member ALONGSIDE a real one is still judgeable — only all-blank fails.
  const mixed = await grade({ type: "context_f1", retrieved: ["", "doc about cats"], expected: ["doc about cats"] }, null);
  assert.equal(mixed.passed, true);
});

test("context_f1 option validation fails closed", async () => {
  const emptyRetrieved = await grade({ type: "context_f1", retrieved: [], expected: ["a"] }, null);
  assert.equal(emptyRetrieved.passed, false);
  assert.match(emptyRetrieved.reason, /non-empty retrieved list/);
  const emptyExpected = await grade({ type: "context_f1", retrieved: ["a"], expected: [] }, null);
  assert.equal(emptyExpected.passed, false);
  assert.match(emptyExpected.reason, /non-empty expected list/);
  assert.equal((await grade({ type: "context_f1", retrieved: ["a"], expected: [7] }, null)).passed, false);
  const badMeasure = await grade({ type: "context_f1", retrieved: ["a"], expected: ["a"], measure: "fmeasure" }, null);
  assert.equal(badMeasure.passed, false);
  assert.match(badMeasure.reason, /unknown measure/);
  assert.equal((await grade({ type: "context_f1", retrieved: ["a"], expected: ["a"], similarity_threshold: 2 }, null)).passed, false);
  assert.equal((await grade({ type: "context_f1", retrieved: ["a"], expected: ["a"], threshold: 2 }, null)).passed, false);
});

test("no_pii passes clean text and detects each entity kind", async () => {
  const clean = await grade({ type: "no_pii" }, "The quick brown fox jumped over the lazy dog.");
  assert.equal(clean.passed, true);
  assert.match(clean.reason, /no matches for email, credit_card, iban, ipv4, ipv6, phone, crypto/);
  const cases = [
    ["email", "ping a.b+tag@example.co.uk when done"],
    ["credit_card", "card 4111 1111 1111 1111 on file"],
    ["iban", "pay DE89370400440532013000 today"],
    ["ipv4", "host 10.0.0.1 is down"],
    ["ipv6", "host 2001:0db8:85a3:0000:0000:8a2e:0370:7334 is down"],
    ["phone", "call +31 6 1234 5678 now"],
    ["crypto", "send to 1BvBMSEYstWetqTFn5Au4m4GFg7xJaNVN2 now"],
  ];
  for (const [entity, text] of cases) {
    const r = await grade({ type: "no_pii", entities: [entity] }, text);
    assert.equal(r.passed, false, `${entity} should have been detected in: ${text}`);
    assert.match(r.reason, new RegExp(`detected ${entity}`));
  }
  // Multiple kinds are all named, in canonical order.
  const multi = await grade({ type: "no_pii" }, "mail a@b.co from 10.0.0.1");
  assert.equal(multi.passed, false);
  assert.match(multi.reason, /detected email, ipv4/);
});

test("no_pii checksums: only Luhn-valid cards and mod-97 IBANs count", async () => {
  assert.equal((await grade({ type: "no_pii", entities: ["credit_card"] }, "card 4111 1111 1111 1112 on file")).passed, true);
  assert.equal((await grade({ type: "no_pii", entities: ["credit_card"] }, "ref 12345678901234567890 x")).passed, true);
  assert.equal((await grade({ type: "no_pii", entities: ["iban"] }, "pay DE89370400440532013001 today")).passed, true);
});

test("no_pii ipv6 rejects a bare :: and ipv4 rejects out-of-range octets", async () => {
  assert.equal((await grade({ type: "no_pii", entities: ["ipv6"] }, "use the :: scope operator")).passed, true);
  assert.equal((await grade({ type: "no_pii", entities: ["ipv6"] }, "host fe80::1 is down")).passed, false);
  assert.equal((await grade({ type: "no_pii", entities: ["ipv4"] }, "build 999.1.1.1 ok")).passed, true);
});

test("no_pii phone only matches international +NNNNNNN format (deliberately conservative)", async () => {
  assert.equal((await grade({ type: "no_pii", entities: ["phone"] }, "call (555) 123-4567 now")).passed, true);
  assert.equal((await grade({ type: "no_pii", entities: ["phone"] }, "seq 1234567890123 ok")).passed, true);
  assert.equal((await grade({ type: "no_pii", entities: ["phone"] }, "call +31612345678 now")).passed, false);
});

test("no_pii entity selection narrows detection", async () => {
  // An email is present but not selected, so this passes — selection is honoured.
  assert.equal((await grade({ type: "no_pii", entities: ["ipv4"] }, "ping a.b@example.com")).passed, true);
  // Duplicates collapse; the reason stays in canonical order.
  const r = await grade({ type: "no_pii", entities: ["ipv4", "email", "email"] }, "clean text");
  assert.equal(r.passed, true);
  assert.match(r.reason, /no matches for email, ipv4$/);
});

test("no_pii unknown or empty entity lists fail closed", async () => {
  const unknown = await grade({ type: "no_pii", entities: ["us_ssn"] }, "anything");
  assert.equal(unknown.passed, false);
  assert.match(unknown.reason, /unknown entity/);
  // An empty selection would vacuously "pass" — reject it instead.
  const empty = await grade({ type: "no_pii", entities: [] }, "anything");
  assert.equal(empty.passed, false);
  assert.match(empty.reason, /non-empty list of entity names/);
  assert.equal((await grade({ type: "no_pii", entities: "email" }, "anything")).passed, false);
  // An explicit null does not silently select every entity either.
  assert.equal((await grade({ type: "no_pii", entities: null }, "anything")).passed, false);
});

test("no_pii never echoes the matched PII into its reason", async () => {
  const card = await grade({ type: "no_pii", entities: ["credit_card"] }, "card 4111 1111 1111 1111 on file");
  assert.equal(card.passed, false);
  assert.ok(!card.reason.includes("4111"), `reason leaked card digits: ${card.reason}`);
  const email = await grade({ type: "no_pii", entities: ["email"] }, "ping a.b@example.com");
  assert.equal(email.passed, false);
  assert.ok(!email.reason.includes("a.b@example.com"), `reason leaked the address: ${email.reason}`);
});

test("exact_match knobs default to today's behaviour", async () => {
  // Absent knobs => case-insensitive, punctuation kept: identical to pre-port verdicts.
  assert.equal((await grade({ type: "exact_match", expected: "Paris" }, " paris ")).passed, true);
  assert.equal((await grade({ type: "exact_match", expected: "Hello, world!" }, "hello world")).passed, false);
  assert.equal(
    (await grade({ type: "exact_match", expected: "Paris", case_sensitive: false, remove_punctuation: false }, " paris ")).passed,
    true,
  );
});

test("exact_match case_sensitive skips the lowercase step only", async () => {
  assert.equal((await grade({ type: "exact_match", expected: "Paris", case_sensitive: true }, "paris")).passed, false);
  assert.equal((await grade({ type: "exact_match", expected: "Paris", case_sensitive: true }, "  Paris  ")).passed, true);
  // Key-order normalisation for structured values is unaffected by the knob.
  assert.equal((await grade({ type: "exact_match", expected: { a: 1, b: 2 }, case_sensitive: true }, { b: 2, a: 1 })).passed, true);
});

test("exact_match remove_punctuation strips ASCII punctuation", async () => {
  assert.equal((await grade({ type: "exact_match", expected: "Hello, world!", remove_punctuation: true }, "hello world")).passed, true);
  assert.equal((await grade({ type: "exact_match", expected: "e.g. this", remove_punctuation: true }, "eg this")).passed, true);
  assert.equal((await grade({ type: "exact_match", expected: "Berlin", remove_punctuation: true }, "Paris!")).passed, false);
  // Structured values still compare key-order-insensitively when the knob is OFF.
  assert.equal((await grade({ type: "exact_match", expected: { a: 1, b: 2 } }, { b: 2, a: 1 })).passed, true);
  assert.equal((await grade({ type: "exact_match", expected: { a: "1" } }, { a: 1 })).passed, false);
});

test("exact_match remove_punctuation is string-only and fails closed otherwise", async () => {
  // Stripping punctuation from a serialised structure deletes the braces, brackets,
  // quotes and colons that carry its shape, so {"a":1} and ["a",1] would collapse to
  // the same string — a false equal. Reject the combination instead.
  const structured = await grade({ type: "exact_match", expected: { a: 1, b: 2 }, remove_punctuation: true }, { b: 2, a: 1 });
  assert.equal(structured.passed, false);
  assert.match(structured.reason, /remove_punctuation requires string candidate and expected/);
  // The false-equal case it prevents, in both directions.
  assert.equal((await grade({ type: "exact_match", expected: { a: "1" }, remove_punctuation: true }, { a: 1 })).passed, false);
  assert.equal((await grade({ type: "exact_match", expected: ["a", 1], remove_punctuation: true }, { a: 1 })).passed, false);
  // One non-string side is enough to fail closed.
  assert.equal((await grade({ type: "exact_match", expected: "a1", remove_punctuation: true }, { a: 1 })).passed, false);
  assert.equal((await grade({ type: "exact_match", expected: 42, remove_punctuation: true }, "42")).passed, false);
  // Same inputs with the knob absent keep behaving exactly as before.
  assert.equal((await grade({ type: "exact_match", expected: { a: 1, b: 2 } }, { b: 2, a: 1 })).passed, true);
});

test("exact_match with both knobs on", async () => {
  const both = { type: "exact_match", expected: "Hello, World!", case_sensitive: true, remove_punctuation: true };
  assert.equal((await grade(both, "Hello World")).passed, true);
  assert.equal((await grade(both, "hello world")).passed, false);
});

// ─── cross-engine pinning (amendments A1-A7) ─────────────────────────────────
// These tests pin the places where JS and Python would otherwise disagree, or where
// an unbounded input would let a gate hang. Each one has a mirror in
// cloud/optimizer/tests/test_graders.py.

test("A1 the shared tokenizer treats exotic whitespace as ordinary symbols", async () => {
  // U+0085 (NEL) and U+FEFF are `\s` in one engine and not the other, so the pinned
  // ASCII class makes them ordinary single-character tokens on BOTH sides.
  // "a\u0085b" => [a, \u0085, b]: 3 tokens, not the 2 that a \s-split would give, so
  // precision against the 2-token reference "a b" is 2/3 instead of 1.
  const nel = await grade({ type: "rouge_score", reference: "a b", measure: "precision", threshold: 1 }, "a\u0085b");
  assert.equal(nel.passed, false);
  assert.match(nel.reason, /score=0\.6667/);
  const bom = await grade({ type: "rouge_score", reference: "a b", measure: "precision", threshold: 1 }, "a\ufeffb");
  assert.equal(bom.passed, false);
  assert.match(bom.reason, /score=0\.6667/);
  // A file-separator control char (U+001C) is likewise a counted token.
  assert.equal((await grade({ type: "bleu_score", reference: "a b", threshold: 1 }, "a\u001cb")).passed, false);
  // Real ASCII whitespace still only separates.
  assert.equal((await grade({ type: "bleu_score", reference: "a b", threshold: 1 }, "a\t\n b")).passed, true);
});

test("A1 context_f1 normalisation collapses only pinned ASCII whitespace", async () => {
  // ASCII whitespace runs collapse to one space and the edges are trimmed.
  const ascii = { type: "context_f1", retrieved: ["  a \t\n b  "], expected: ["a b"], similarity_threshold: 1 };
  assert.equal((await grade(ascii, null)).passed, true);
  // U+FEFF is NOT whitespace here: it survives normalisation and costs similarity,
  // exactly as it does on the Python side (where str.strip() would not remove it).
  const bom = { type: "context_f1", retrieved: ["a\ufeffb"], expected: ["ab"], similarity_threshold: 1 };
  assert.equal((await grade(bom, null)).passed, false);
});

test("A2 not_regex reads the candidate as text, not [object Object]", async () => {
  // String({}) is "[object Object]", so a content pattern would silently stop matching
  // a structured candidate — the exact failure this pins.
  const hit = await grade({ type: "not_regex", pattern: "secret" }, { note: "a secret value" });
  assert.equal(hit.passed, false);
  assert.match(hit.reason, /regex matched/);
  assert.equal((await grade({ type: "not_regex", pattern: "secret" }, { note: "a public value" })).passed, true);
  // Arrays and numbers go through the same text path.
  assert.equal((await grade({ type: "not_regex", pattern: "denied" }, ["ok", "denied"])).passed, false);
  assert.equal((await grade({ type: "not_regex", pattern: "42" }, 42)).passed, false);
});

test("A2 not_regex normalises CR and one trailing newline before matching", async () => {
  // CRLF becomes LF, so `$` in multiline-free patterns is not blocked by a stray CR.
  assert.equal((await grade({ type: "not_regex", pattern: "ok$" }, "ok\r\n")).passed, false);
  assert.equal((await grade({ type: "not_regex", pattern: "ok$" }, "ok\n")).passed, false);
  assert.equal((await grade({ type: "not_regex", pattern: "ok$" }, "ok\r")).passed, false);
  // Only ONE trailing newline is stripped: a second one is still content.
  // Documented residual (A2 allows engine exotica): Python's `$` also matches just
  // before a final newline, JS's matches only at the absolute end, so this candidate
  // PASSES here and FAILS in cloud/optimizer. The pinned normalisation covers the
  // single-trailing-newline case, which is the one that actually occurs; the parity
  // fixture must not carry a `$` vector with two trailing newlines.
  assert.equal((await grade({ type: "not_regex", pattern: "ok$" }, "ok\n\n")).passed, true);
  // `.` does not cross the normalised line break.
  assert.equal((await grade({ type: "not_regex", pattern: "a.b" }, "a\r\nb")).passed, true);
  assert.equal((await grade({ type: "not_regex", pattern: "a.b" }, "a-b")).passed, false);
});

test("A3 not_contains fails closed on a non-string fragment", async () => {
  const numeric = await grade({ type: "not_contains", fragments: [42] }, "order 42 shipped");
  assert.equal(numeric.passed, false);
  assert.match(numeric.reason, /fragments must be strings/);
  // Even when the coerced fragment would NOT have matched, the option is invalid.
  const absent = await grade({ type: "not_contains", fragments: ["ok", null] }, "clean text");
  assert.equal(absent.passed, false);
  assert.match(absent.reason, /fragments must be strings/);
  assert.equal((await grade({ type: "not_contains", fragments: [["nested"]] }, "clean")).passed, false);
  // The pre-existing `contains` grader keeps its coercion — untouched by this rule.
  assert.equal((await grade({ type: "contains", fragments: [42] }, "order 42 shipped")).passed, true);
});

test("A5 no_pii ipv6 needs a digit: std::vector is code, ::1 is an address", async () => {
  const ipv6 = (text) => grade({ type: "no_pii", entities: ["ipv6"] }, text);
  // Hex-letter-only compressed forms are source code, not addresses.
  assert.equal((await ipv6("std::vector<int> xs;")).passed, true);
  assert.equal((await ipv6("call a::b now")).passed, true);
  assert.equal((await ipv6("Foo::Bar::baz()")).passed, true);
  // Addresses with at least one digit still fail.
  assert.equal((await ipv6("bind ::1 only")).passed, false);
  assert.equal((await ipv6("host fe80:: is down")).passed, false);
  assert.equal((await ipv6("host 2001:db8::ff00:42:8329 up")).passed, false);
  // The full 8-group form never needed the digit rule.
  assert.equal((await ipv6("host 2001:0db8:85a3:0000:0000:8a2e:0370:7334 up")).passed, false);
});

test("A5 no_pii digits are ASCII-only, matching Python's re.ASCII", async () => {
  // An Arabic-Indic digit is NOT a digit for these detectors, so it cannot act as the
  // (?<!\d) guard that would suppress an adjacent card, and it cannot pad a run.
  const arabic = await grade({ type: "no_pii", entities: ["credit_card"] }, "card \u0663 4111 1111 1111 1111 on file");
  assert.equal(arabic.passed, false);
  // A phone made of Arabic-Indic digits is not a phone number.
  assert.equal((await grade({ type: "no_pii", entities: ["phone"] }, "call +\u0663\u0663\u0663\u0663\u0663\u0663\u0663 now")).passed, true);
});

test("A6 bleu_score/rouge_score cap the token count at 4096", async () => {
  const tokens = (n) => Array.from({ length: n }, () => "a").join(" ");
  const at = tokens(4096);
  const over = tokens(4097);
  // Exactly at the cap still scores.
  assert.equal((await grade({ type: "bleu_score", reference: at }, at)).passed, true);
  assert.equal((await grade({ type: "rouge_score", reference: at }, at)).passed, true);
  // One token over fails CLOSED on both sides of the comparison.
  const candOver = await grade({ type: "bleu_score", reference: at }, over);
  assert.equal(candOver.passed, false);
  assert.match(candOver.reason, /input exceeds grader size cap/);
  const refOver = await grade({ type: "rouge_score", reference: over }, at);
  assert.equal(refOver.passed, false);
  assert.match(refOver.reason, /input exceeds grader size cap/);
});

test("A6 context_f1 caps member count and total characters", async () => {
  const members = (n, len) => Array.from({ length: n }, () => "a".repeat(len));
  // 64 members is the cap; 65 fails.
  assert.equal((await grade({ type: "context_f1", retrieved: members(64, 3), expected: ["aaa"] }, null)).passed, true);
  const tooMany = await grade({ type: "context_f1", retrieved: members(65, 3), expected: ["aaa"] }, null);
  assert.equal(tooMany.passed, false);
  assert.match(tooMany.reason, /input exceeds grader size cap/);
  assert.equal((await grade({ type: "context_f1", retrieved: ["aaa"], expected: members(65, 3) }, null)).passed, false);
  // 64 x 128 = 8192 characters is the cap; 64 x 129 = 8256 fails.
  assert.equal((await grade({ type: "context_f1", retrieved: members(64, 128), expected: ["a".repeat(128)] }, null)).passed, true);
  const tooLong = await grade({ type: "context_f1", retrieved: members(64, 129), expected: ["a".repeat(129)] }, null);
  assert.equal(tooLong.passed, false);
  assert.match(tooLong.reason, /input exceeds grader size cap/);
});

// ─── langevals judge family (llm_score · llm_category · llm_pairwise · llm_answer_match) ──
// Every judge call is stubbed through GradeDeps.fetch — no test ever reaches a model.
// The stub also RECORDS each request so the pinned prompt templates (byte contracts shared
// with the Python mirror) and the pairwise A/B swap can be asserted on the wire.

const GW = "http://gw.example.com";

/** judgeFetch replies with `texts[n]` for the n-th call and records every request. */
const judgeFetch = (...texts) => {
  const calls = [];
  const fn = async (url, init) => {
    calls.push({ url, body: JSON.parse(init.body) });
    return { ok: true, json: async () => ({ output_text: texts[calls.length - 1] ?? "" }) };
  };
  fn.calls = calls;
  return fn;
};

/** countingFetch records calls and never answers usefully — for "must not call" assertions. */
const countingFetch = () => {
  const calls = [];
  const fn = async () => {
    calls.push(1);
    return { ok: true, json: async () => ({ output_text: "" }) };
  };
  fn.calls = calls;
  return fn;
};

const nonOkFetch = async () => ({ ok: false, status: 503, json: async () => ({ output_text: "SCORE: 1.0" }) });

const judgeDeps = (...texts) => ({ ssrfCheck: permissiveSsrf, resolutionPinnedFetch: judgeFetch(...texts) });

const SCORE_GRADER = { type: "llm_score", rubric: "Is it factual?", min_score: 0.7, gateway_url: GW };
const CATEGORY_GRADER = {
  type: "llm_category",
  prompt: "Classify the tone.",
  categories: ["safe", "unsafe"],
  passing_categories: ["safe"],
  gateway_url: GW,
};
const PAIRWISE_GRADER = { type: "llm_pairwise", baseline: "base", criteria: "Which is clearer?", gateway_url: GW };
const ANSWER_GRADER = { type: "llm_answer_match", expected: "Paris", gateway_url: GW };

test("llm_score passes at/above min_score and fails below", async () => {
  assert.equal((await grade(SCORE_GRADER, "Paris", judgeDeps("SCORE: 0.90"))).passed, true);
  assert.equal((await grade(SCORE_GRADER, "Paris", judgeDeps("SCORE: 0.60"))).passed, false);
  // Boundary equality is INCLUSIVE: exactly min_score passes.
  assert.equal((await grade(SCORE_GRADER, "Paris", judgeDeps("SCORE: 0.70"))).passed, true);
  assert.equal((await grade(SCORE_GRADER, "Paris", judgeDeps("SCORE: 0"))).passed, false);
  assert.equal((await grade({ ...SCORE_GRADER, min_score: 0 }, "Paris", judgeDeps("SCORE: 0"))).passed, true);
});

test("llm_score parse edges: whitespace class, CRLF, verdict not on the first line", async () => {
  // Pinned whitespace class [ \t\n\r\f\v] — a leading TAB is skipped, not a parse miss.
  assert.equal((await grade(SCORE_GRADER, "Paris", judgeDeps("\tSCORE: 0.90"))).passed, true);
  // Model text is normalised CR→LF before parsing, and the scan is multiline.
  assert.equal((await grade(SCORE_GRADER, "Paris", judgeDeps("Reasoning: solid.\r\nSCORE: 0.95"))).passed, true);
  // `SCORE:` is matched case-SENSITIVELY.
  const lower = await grade(SCORE_GRADER, "Paris", judgeDeps("score: 0.95"));
  assert.equal(lower.passed, false);
  assert.match(lower.reason, /unparseable judge score/);
});

test("llm_score fails closed on garbage, empty text, trailing junk and out-of-range scores", async () => {
  for (const text of ["I think it is fine", "", "SCORE: 0.9 extra", "SCORE: abc", "SCORE: 1.5"]) {
    const r = await grade(SCORE_GRADER, "Paris", judgeDeps(text));
    assert.equal(r.passed, false, `expected fail for judge text ${JSON.stringify(text)}`);
    assert.match(r.reason, /unparseable judge score/);
  }
});

test("llm_score option validation fails closed BEFORE any judge call", async () => {
  const cases = [
    [{ ...SCORE_GRADER, rubric: "   " }, /non-empty rubric/],
    [{ ...SCORE_GRADER, rubric: 42 }, /non-empty rubric/],
    [{ ...SCORE_GRADER, min_score: undefined }, /min_score/],
    [{ ...SCORE_GRADER, min_score: "0.7" }, /min_score/],
    [{ ...SCORE_GRADER, min_score: Number.NaN }, /min_score/],
    [{ ...SCORE_GRADER, min_score: 1.5 }, /min_score/],
    [{ ...SCORE_GRADER, gateway_url: undefined }, /gateway_url/],
  ];
  for (const [grader, expected] of cases) {
    const fetchStub = countingFetch();
    const r = await grade(grader, "Paris", { ssrfCheck: permissiveSsrf, resolutionPinnedFetch: fetchStub });
    assert.equal(r.passed, false);
    assert.match(r.reason, expected);
    assert.equal(fetchStub.calls.length, 0, "an invalid grader must never spend a judge call");
  }
});

test("llm_score sends the pinned prompt template byte-for-byte", async () => {
  const deps = judgeDeps("SCORE: 0.9");
  await grade(SCORE_GRADER, "Paris", deps);
  assert.equal(deps.resolutionPinnedFetch.calls.length, 1);
  assert.equal(deps.resolutionPinnedFetch.calls[0].url, "http://gw.example.com/openai/v1/responses");
  assert.equal(
    deps.resolutionPinnedFetch.calls[0].body.input,
    "You are a strict evaluation judge. Is it factual?\n\n" +
      "Score the RESPONSE from 0.00 to 1.00 against the rubric.\n" +
      "Reply with exactly one line: SCORE: <number>\n\n" +
      "RESPONSE:\nParis",
  );
  // Non-string candidates go through the same asText path as the other ported graders.
  const structured = judgeDeps("SCORE: 0.9");
  await grade(SCORE_GRADER, { city: "Paris" }, structured);
  assert.match(structured.resolutionPinnedFetch.calls[0].body.input, /RESPONSE:\n\{"city":"Paris"\}$/);
});

test("llm_category passes only for a passing category", async () => {
  assert.equal((await grade(CATEGORY_GRADER, "hello", judgeDeps("CATEGORY: safe"))).passed, true);
  assert.equal((await grade(CATEGORY_GRADER, "hello", judgeDeps("CATEGORY: unsafe"))).passed, false);
  // The label match is case-insensitive and trimmed on both sides of the comparison.
  assert.equal((await grade(CATEGORY_GRADER, "hello", judgeDeps("CATEGORY:   SaFe  "))).passed, true);
  const mixedCase = { ...CATEGORY_GRADER, categories: ["Safe", "Unsafe"], passing_categories: ["SAFE"] };
  assert.equal((await grade(mixedCase, "hello", judgeDeps("CATEGORY: safe"))).passed, true);
});

test("llm_category fails closed on an unknown category and on an unparseable reply", async () => {
  const unknown = await grade(CATEGORY_GRADER, "hello", judgeDeps("CATEGORY: borderline"));
  assert.equal(unknown.passed, false);
  assert.match(unknown.reason, /unknown category/);
  for (const text of ["", "it seems safe to me", "category: safe"]) {
    const r = await grade(CATEGORY_GRADER, "hello", judgeDeps(text));
    assert.equal(r.passed, false, `expected fail for judge text ${JSON.stringify(text)}`);
    assert.match(r.reason, /unparseable judge category/);
  }
});

test("llm_category option validation fails closed BEFORE any judge call", async () => {
  const cases = [
    [{ ...CATEGORY_GRADER, prompt: "" }, /non-empty prompt/],
    [{ ...CATEGORY_GRADER, prompt: { a: 1 } }, /non-empty prompt/],
    [{ ...CATEGORY_GRADER, categories: ["safe"] }, /at least two/],
    [{ ...CATEGORY_GRADER, categories: ["safe", "  "] }, /at least two/],
    [{ ...CATEGORY_GRADER, categories: ["safe", 7] }, /at least two/],
    [{ ...CATEGORY_GRADER, categories: ["safe", "SAFE"] }, /unique case-insensitively/],
    [{ ...CATEGORY_GRADER, passing_categories: [] }, /passing_categories/],
    [{ ...CATEGORY_GRADER, passing_categories: ["neutral"] }, /not one of categories/],
  ];
  for (const [grader, expected] of cases) {
    const fetchStub = countingFetch();
    const r = await grade(grader, "hello", { ssrfCheck: permissiveSsrf, resolutionPinnedFetch: fetchStub });
    assert.equal(r.passed, false);
    assert.match(r.reason, expected);
    assert.equal(fetchStub.calls.length, 0, "an invalid grader must never spend a judge call");
  }
});

test("llm_category sends the pinned prompt template byte-for-byte", async () => {
  const deps = judgeDeps("CATEGORY: safe");
  await grade(CATEGORY_GRADER, "hello", deps);
  assert.equal(
    deps.resolutionPinnedFetch.calls[0].body.input,
    "You are a strict classification judge. Classify the tone.\n\n" +
      "Classify the RESPONSE into exactly one of these categories: safe, unsafe.\n" +
      "Reply with exactly one line: CATEGORY: <category>\n\n" +
      "RESPONSE:\nhello",
  );
});

test("llm_pairwise passes when the candidate wins both orderings", async () => {
  // Candidate is A in call 1 and B in call 2, so "A" then "B" is a clean sweep.
  const r = await grade(PAIRWISE_GRADER, "cand", judgeDeps("WINNER: A", "WINNER: B"));
  assert.equal(r.passed, true);
  assert.match(r.reason, /AB=candidate BA=candidate/);
});

test("llm_pairwise passes on win/tie and tie/tie", async () => {
  assert.equal((await grade(PAIRWISE_GRADER, "cand", judgeDeps("WINNER: A", "WINNER: TIE"))).passed, true);
  assert.equal((await grade(PAIRWISE_GRADER, "cand", judgeDeps("WINNER: TIE", "WINNER: B"))).passed, true);
  const tie = await grade(PAIRWISE_GRADER, "cand", judgeDeps("WINNER: TIE", "WINNER: tie"));
  assert.equal(tie.passed, true);
  assert.match(tie.reason, /AB=tie BA=tie/);
});

test("llm_pairwise fails closed when the orderings disagree", async () => {
  // Call 1 picks the candidate (slot A), call 2 picks the baseline (slot A) — position, not text.
  const disagree = await grade(PAIRWISE_GRADER, "cand", judgeDeps("WINNER: A", "WINNER: A"));
  assert.equal(disagree.passed, false);
  assert.match(disagree.reason, /orderings disagreed: candidate\/baseline/);
  // The mirror image: baseline wins the first ordering, candidate the second.
  const mirrored = await grade(PAIRWISE_GRADER, "cand", judgeDeps("WINNER: B", "WINNER: B"));
  assert.equal(mirrored.passed, false);
  assert.match(mirrored.reason, /orderings disagreed: baseline\/candidate/);
  // A tie on one side and a baseline win on the other is still undecided → fail.
  assert.equal((await grade(PAIRWISE_GRADER, "cand", judgeDeps("WINNER: TIE", "WINNER: A"))).passed, false);
});

test("llm_pairwise fails closed when the baseline wins both orderings", async () => {
  const r = await grade(PAIRWISE_GRADER, "cand", judgeDeps("WINNER: B", "WINNER: A"));
  assert.equal(r.passed, false);
  assert.match(r.reason, /baseline won both orderings/);
  assert.match(r.reason, /AB=baseline BA=baseline/);
});

test("llm_pairwise makes TWO judge calls with the A/B orders actually swapped", async () => {
  const deps = judgeDeps("WINNER: A", "WINNER: B");
  await grade(PAIRWISE_GRADER, "cand", deps);
  assert.equal(deps.resolutionPinnedFetch.calls.length, 2, "llm_pairwise must make exactly two judge calls");
  const header =
    "You are a strict evaluation judge comparing two responses. Criteria: Which is clearer?\n\n" +
    "Reply with exactly one line: WINNER: A or WINNER: B or WINNER: TIE\n\n";
  // Call 1: candidate in slot A, baseline in slot B. Call 2: the two are swapped.
  assert.equal(deps.resolutionPinnedFetch.calls[0].body.input, `${header}RESPONSE A:\ncand\n\nRESPONSE B:\nbase`);
  assert.equal(deps.resolutionPinnedFetch.calls[1].body.input, `${header}RESPONSE A:\nbase\n\nRESPONSE B:\ncand`);
  assert.notEqual(deps.resolutionPinnedFetch.calls[0].body.input, deps.resolutionPinnedFetch.calls[1].body.input);
});

test("llm_pairwise fails closed on an unparseable winner in either ordering", async () => {
  const first = judgeDeps("the first one, probably", "WINNER: B");
  const r = await grade(PAIRWISE_GRADER, "cand", first);
  assert.equal(r.passed, false);
  assert.match(r.reason, /unparseable judge winner \(candidate as A\)/);
  // Both orderings are asked before either reply is parsed, so an unparseable FIRST reply
  // still produces the fixed two-prompt shape the shared parity vectors pin (and that the
  // Python mirror emits).
  assert.equal(first.resolutionPinnedFetch.calls.length, 2, "both orderings are asked before either is parsed");
  const second = judgeDeps("WINNER: A", "");
  const r2 = await grade(PAIRWISE_GRADER, "cand", second);
  assert.equal(r2.passed, false);
  assert.match(r2.reason, /unparseable judge winner \(candidate as B\)/);
  assert.equal(second.resolutionPinnedFetch.calls.length, 2);
});

test("llm_pairwise option validation fails closed BEFORE any judge call", async () => {
  const cases = [
    [{ ...PAIRWISE_GRADER, baseline: "  " }, /non-empty baseline/],
    [{ ...PAIRWISE_GRADER, baseline: ["base"] }, /non-empty baseline/],
    [{ ...PAIRWISE_GRADER, criteria: "" }, /criteria/],
    [{ ...PAIRWISE_GRADER, criteria: null }, /criteria/],
    [{ ...PAIRWISE_GRADER, gateway_url: "" }, /gateway_url/],
  ];
  for (const [grader, expected] of cases) {
    const fetchStub = countingFetch();
    const r = await grade(grader, "cand", { ssrfCheck: permissiveSsrf, resolutionPinnedFetch: fetchStub });
    assert.equal(r.passed, false);
    assert.match(r.reason, expected);
    assert.equal(fetchStub.calls.length, 0, "an invalid grader must never spend a judge call");
  }
});

test("llm_answer_match passes on YES and fails on NO", async () => {
  assert.equal((await grade(ANSWER_GRADER, "The capital is Paris.", judgeDeps("MATCH: YES"))).passed, true);
  assert.equal((await grade(ANSWER_GRADER, "Berlin", judgeDeps("MATCH: NO"))).passed, false);
  // The captured token is case-insensitive; the verdict line may sit anywhere in the reply.
  assert.equal((await grade(ANSWER_GRADER, "Paris", judgeDeps("Reasoning first.\nMATCH: yes"))).passed, true);
  assert.equal((await grade(ANSWER_GRADER, "Paris", judgeDeps("MATCH: No"))).passed, false);
});

test("llm_answer_match fails closed on an unparseable reply", async () => {
  for (const text of ["", "maybe?", "MATCH: MAYBE", "MATCH: YES please"]) {
    const r = await grade(ANSWER_GRADER, "Paris", judgeDeps(text));
    assert.equal(r.passed, false, `expected fail for judge text ${JSON.stringify(text)}`);
    assert.match(r.reason, /unparseable judge match verdict/);
  }
});

test("llm_answer_match option validation fails closed BEFORE any judge call", async () => {
  const cases = [
    [{ ...ANSWER_GRADER, expected: "" }, /non-empty expected/],
    [{ ...ANSWER_GRADER, expected: 42 }, /non-empty expected/],
    [{ ...ANSWER_GRADER, gateway_url: undefined }, /gateway_url/],
  ];
  for (const [grader, expected] of cases) {
    const fetchStub = countingFetch();
    const r = await grade(grader, "Paris", { ssrfCheck: permissiveSsrf, resolutionPinnedFetch: fetchStub });
    assert.equal(r.passed, false);
    assert.match(r.reason, expected);
    assert.equal(fetchStub.calls.length, 0, "an invalid grader must never spend a judge call");
  }
});

test("llm_answer_match sends the pinned prompt template byte-for-byte", async () => {
  const deps = judgeDeps("MATCH: YES");
  await grade(ANSWER_GRADER, "The capital is Paris.", deps);
  assert.equal(
    deps.resolutionPinnedFetch.calls[0].body.input,
    "You are a strict evaluation judge. Decide whether the RESPONSE conveys the same answer as the EXPECTED answer, " +
      "ignoring differences in style, wording, or formatting.\n" +
      "Reply with exactly one line: MATCH: YES or MATCH: NO\n\n" +
      "EXPECTED:\nParis\n\n" +
      "RESPONSE:\nThe capital is Paris.",
  );
});

test("judge family shares llm_judge's transport: SSRF guard, bias guard, HTTP errors, model option", async () => {
  const graders = [
    [SCORE_GRADER, "SCORE: 1.0"],
    [CATEGORY_GRADER, "CATEGORY: safe"],
    [PAIRWISE_GRADER, "WINNER: A"],
    [ANSWER_GRADER, "MATCH: YES"],
  ];
  for (const [grader, verdict] of graders) {
    // A blocked gateway URL fails closed and never reaches fetch.
    const blocked = await grade(grader, "x", {
      ssrfCheck: async () => ({ allowed: false, reason: "blocked address 127.0.0.1" }),
      resolutionPinnedFetch: throwingFetch,
    });
    assert.equal(blocked.passed, false);
    assert.match(blocked.reason, /blocked by SSRF guard/);

    // Judge-bias guard: same family as the system-under-test fails closed, and the guard
    // fires before the request, so a would-be passing verdict never counts.
    const biased = await grade({ ...grader, model: "gpt-5.5" }, "x", {
      ssrfCheck: permissiveSsrf,
      resolutionPinnedFetch: judgeFetch(verdict, verdict),
      subjectModel: "gpt-5.5-mini",
    });
    assert.equal(biased.passed, false);
    assert.match(biased.reason, new RegExp(`^${grader.type} bias guard:`));

    // A non-2xx gateway reply fails closed even when its body carries a passing verdict.
    const http = await grade(grader, "x", { ssrfCheck: permissiveSsrf, resolutionPinnedFetch: nonOkFetch });
    assert.equal(http.passed, false);
    assert.match(http.reason, /non-2xx \(503\)/);

    // A transport exception fails closed with the shared reason.
    const threw = await grade(grader, "x", {
      ssrfCheck: permissiveSsrf,
      resolutionPinnedFetch: async () => {
        throw new Error("connection reset");
      },
    });
    assert.equal(threw.passed, false);
    assert.match(threw.reason, /judge call failed: .*connection reset/);

    // The `model` option flows through and is recorded in the verdict reason.
    const named = await grade({ ...grader, model: "claude-opus-4-8" }, "x", judgeDeps(verdict, "WINNER: B"));
    assert.match(named.reason, /claude-opus-4-8/);
  }
});

// ─── judge parse/serialisation contract (amendments J5-J8) ────────────────────────────
// Each test below has a twin in cloud/optimizer/tests/test_graders.py
// (TestJudgeParseAndSerialisationContract). They exist because every one of these is a
// point where the two regex/JSON engines disagree by DEFAULT, so an innocent-looking
// simplification on either side silently produces two verdicts for one judge reply.

const LINE_SEP = "\u2028";
const PARA_SEP = "\u2029";

test("judge KEYWORDS are case-sensitive while their tokens are not (J5)", async () => {
  // A lowercase keyword is not the pinned reply contract. An engine-wide `i` flag would
  // have accepted `winner: a` here while Python (per-character since day one) failed it.
  const lowerWinner = await grade(PAIRWISE_GRADER, "cand", judgeDeps("winner: a", "WINNER: B"));
  assert.equal(lowerWinner.passed, false);
  assert.match(lowerWinner.reason, /unparseable judge winner/);
  const lowerMatch = await grade(ANSWER_GRADER, "Paris", judgeDeps("match: yes"));
  assert.equal(lowerMatch.passed, false);
  assert.match(lowerMatch.reason, /unparseable judge match verdict/);
  assert.equal((await grade(SCORE_GRADER, "x", judgeDeps("score: 0.9"))).passed, false);
  assert.equal((await grade(CATEGORY_GRADER, "x", judgeDeps("category: safe"))).passed, false);
  // The captured TOKEN stays case-insensitive, spelled per character rather than by flag.
  assert.equal((await grade(PAIRWISE_GRADER, "cand", judgeDeps("WINNER: a", "WINNER: b"))).passed, true);
  assert.equal((await grade(PAIRWISE_GRADER, "cand", judgeDeps("WINNER: Tie", "WINNER: tIe"))).passed, true);
  assert.equal((await grade(ANSWER_GRADER, "Paris", judgeDeps("MATCH: Yes"))).passed, true);
  const no = await grade(ANSWER_GRADER, "Paris", judgeDeps("MATCH: nO"));
  assert.equal(no.passed, false);
  // A parsed NO is a verdict, not a parse miss — the two must stay distinguishable.
  assert.match(no.reason, /returned MATCH: NO/);
});

test("judge model text folds CR, U+2028 and U+2029 to LF before parsing (J6)", async () => {
  // Python's re.MULTILINE anchors only at \n; JS `m` anchors at CR, U+2028 and U+2029.
  // Folding first makes the FIRST verdict line (0.9) win on both sides.
  const cr = await grade({ ...SCORE_GRADER, min_score: 0.8 }, "x", judgeDeps("SCORE: 0.9\rSCORE: 0.1"));
  assert.equal(cr.passed, true);
  for (const sep of [LINE_SEP, PARA_SEP]) {
    const wrap = (verdict) => `Reasoning${sep}${verdict}${sep}closing note`;
    assert.equal((await grade(SCORE_GRADER, "x", judgeDeps(wrap("SCORE: 0.90")))).passed, true);
    assert.equal((await grade(CATEGORY_GRADER, "x", judgeDeps(wrap("CATEGORY: safe")))).passed, true);
    const pair = judgeDeps(wrap("WINNER: A"), wrap("WINNER: B"));
    assert.equal((await grade(PAIRWISE_GRADER, "cand", pair)).passed, true);
    assert.equal((await grade(ANSWER_GRADER, "Paris", judgeDeps(wrap("MATCH: YES")))).passed, true);
  }
  // The REPLY is normalised; the operator's candidate bytes are not.
  const deps = judgeDeps("SCORE: 0.90");
  const candidate = `line one${LINE_SEP}line two\rline three`;
  await grade(SCORE_GRADER, candidate, deps);
  assert.ok(deps.resolutionPinnedFetch.calls[0].body.input.endsWith(`RESPONSE:\n${candidate}`));
});

test("a non-string judge candidate is serialised as COMPACT JSON (J7)", async () => {
  const deps = judgeDeps("SCORE: 0.90");
  await grade(SCORE_GRADER, { b: 2, a: [1, 2] }, deps);
  // No space after `,` or `:`. The Python mirror pins separators=(",", ":") to match
  // these exact bytes; the default json.dumps spacing would not.
  assert.ok(deps.resolutionPinnedFetch.calls[0].body.input.endsWith('RESPONSE:\n{"b":2,"a":[1,2]}'));
  const arr = judgeDeps("MATCH: YES");
  await grade(ANSWER_GRADER, [1, { a: "b" }], arr);
  assert.ok(arr.resolutionPinnedFetch.calls[0].body.input.endsWith('RESPONSE:\n[1,{"a":"b"}]'));
});

test("a model-controlled quote in a judge reason is capped at 160 code points (J8)", async () => {
  const long = "z".repeat(200);
  const r = await grade(CATEGORY_GRADER, "hello", judgeDeps(`CATEGORY: ${long}`));
  assert.equal(r.passed, false);
  assert.match(r.reason, /unknown category/);
  assert.ok(r.reason.includes(`${"z".repeat(160)}…`), "the quote is truncated with an ellipsis");
  assert.ok(!r.reason.includes(long), "the full model label never reaches the reason");
  // Exactly at the cap, nothing is truncated and no ellipsis is added.
  const exact = "y".repeat(160);
  const atCap = await grade(CATEGORY_GRADER, "hello", judgeDeps(`CATEGORY: ${exact}`));
  assert.ok(atCap.reason.includes(exact));
  assert.ok(!atCap.reason.includes("…"));
  // Counting is by CODE POINT, so an astral label cuts where Python's len() cuts.
  const astral = await grade(CATEGORY_GRADER, "hello", judgeDeps(`CATEGORY: ${"😀".repeat(200)}`));
  assert.ok(astral.reason.includes(`${"😀".repeat(160)}…`));
  // The cap is a REASON concern only: matching still ran on the full label, so a long
  // label that IS configured still resolves normally.
  const configured = { ...CATEGORY_GRADER, categories: [long, "unsafe"], passing_categories: [long] };
  assert.equal((await grade(configured, "hello", judgeDeps(`CATEGORY: ${long}`))).passed, true);
});

test("judge family sends llm_judge's request shape (model, auth and upstream-key headers)", async () => {
  const deps = judgeDeps("SCORE: 0.9");
  await grade({ ...SCORE_GRADER, gateway_url: `${GW}/`, model: "claude-opus-4-8", api_key: "k", upstream_key: "u" }, "x", deps);
  // A trailing slash on the gateway URL is trimmed, exactly as llm_judge does.
  assert.equal(deps.resolutionPinnedFetch.calls[0].url, "http://gw.example.com/openai/v1/responses");
  assert.equal(deps.resolutionPinnedFetch.calls[0].body.model, "claude-opus-4-8");
  // The default judge model is shared with llm_judge.
  const dflt = judgeDeps("SCORE: 0.9");
  await grade(SCORE_GRADER, "x", dflt);
  assert.equal(dflt.resolutionPinnedFetch.calls[0].body.model, "gpt-5.5");
});

test("unknown grader type fails closed", async () => {
  const r = await grade({ type: "totally_made_up" }, "x");
  assert.equal(r.passed, false);
  assert.match(r.reason.toLowerCase(), /unknown grader/);
});
