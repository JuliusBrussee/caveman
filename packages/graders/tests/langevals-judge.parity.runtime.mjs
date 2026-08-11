/**
 * Cross-language parity test for the 4 langevals LLM-judge graders
 * (llm_score · llm_category · llm_pairwise · llm_answer_match).
 * Drives the SAME canonical fixture (langevals-judge.vectors.json) that the Python
 * test in cloud/optimizer reads, so a TS/Python drift is a hard release-gate failure.
 * Run with: node --test public/evals/tests/langevals-judge.parity.runtime.mjs
 *
 * Two things are asserted per vector, not one:
 *   1. the VERDICT (`expected_passed`), and
 *   2. the exact PROMPT TEXT(S) that reached the wire (`expected_prompts`) — the
 *      pinned templates are a byte contract between the two languages, so a reflowed
 *      or re-punctuated prompt on either side fails here even when the verdict agrees.
 * `expected_prompts` also pins the judge-call COUNT: it is empty for every vector
 * whose options are invalid (an invalid grader must never spend a model call) and has
 * exactly two entries for llm_pairwise, which always asks both A/B orderings.
 *
 * A vector may also carry `expected_reason_contains`: a FRAGMENT the verdict reason must
 * include on both sides. Reasons themselves are not byte-identical across the languages (TS
 * prefixes the grader and judge model, Python quotes with repr), so this is deliberately a
 * containment check rather than equality. It exists for the J8 cap: the fragment is the
 * truncated model quote plus its ellipsis, which is absent whenever the cap is not applied.
 *
 * Vector wire shape: {name, type, options, candidate, stub_responses, expected_passed,
 * expected_prompts, expected_reason_contains?}. `options` holds every grader field EXCEPT gateway_url: each side
 * injects its own stub gateway (this file a fake URL + injected fetch, the Python
 * mirror a loopback HTTP stub), and the gateway URL never reaches a prompt.
 * No test here ever calls a model. See cloud/optimizer/tests/test_langevals_judge_parity.py.
 */
import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { dirname, join } from "node:path";

import { grade } from "../dist/index.js";

const here = dirname(fileURLToPath(import.meta.url));
const vectors = JSON.parse(readFileSync(join(here, "langevals-judge.vectors.json"), "utf8"));

const GATEWAY = "http://judge.example.com";
const permissiveSsrf = async () => ({ allowed: true, reason: "test stub" });

/**
 * recordingJudge answers the n-th judge call with `replies[n]` and records the prompt
 * that call carried. A call beyond the script answers non-2xx (so an implementation
 * that makes MORE calls than the vector scripts fails loudly instead of silently
 * reusing the last reply), and is still recorded.
 */
function recordingJudge(replies) {
  const prompts = [];
  const urls = [];
  const fn = async (url, init) => {
    const body = JSON.parse(init.body);
    prompts.push(String(body.input ?? ""));
    urls.push(url);
    const reply = replies[prompts.length - 1];
    if (reply === undefined) return { ok: false, status: 500, json: async () => ({}) };
    return { ok: true, json: async () => ({ output_text: reply }) };
  };
  fn.prompts = prompts;
  fn.urls = urls;
  return fn;
}

test("langevals-judge vectors fixture is populated", () => {
  assert.ok(Array.isArray(vectors) && vectors.length >= 24, `expected >=24 vectors, got ${vectors.length}`);
});

for (const v of vectors) {
  test(`langevals-judge parity: ${v.name}`, async () => {
    const judge = recordingJudge(v.stub_responses ?? []);
    const grader = { type: v.type, ...(v.options ?? {}), gateway_url: GATEWAY };
    const r = await grade(grader, v.candidate, { ssrfCheck: permissiveSsrf, resolutionPinnedFetch: judge });
    assert.equal(r.passed, v.expected_passed, `${v.name}: ${r.reason}`);
    assert.deepEqual(
      judge.prompts,
      v.expected_prompts,
      `${v.name}: prompt bytes/count differ from the pinned template contract`,
    );
    if (typeof v.expected_reason_contains === "string") {
      assert.ok(
        r.reason.includes(v.expected_reason_contains),
        `${v.name}: reason must carry the capped model quote, got ${JSON.stringify(r.reason)}`,
      );
    }
    for (const url of judge.urls) assert.equal(url, `${GATEWAY}/openai/v1/responses`);
  });
}
