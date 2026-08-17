/**
 * Parity test between the machine-readable grader registry
 * (public/shared/contracts/schemas/grader-registry.json) and this package's grade()
 * dispatch. The registry drives the dashboard's grader-editor forms, so an entry the
 * dispatch does not know — or a prompt template that no longer matches what the judge
 * actually receives — is a UI that lies about what the eval gate will do.
 * Run with: node --test public/evals/tests/grader-registry.parity.runtime.mjs
 *
 * Three things are asserted:
 *   1. REGISTRATION — every entry that is not python_only is driven through grade()
 *      with a deliberately empty option bag (each declared option filled with the
 *      empty value of its declared type). The verdict may be either — an empty
 *      fragment/tool list is a vacuous predicate on the older types and still passes —
 *      but the reason must never be the unknown-grader default, which is what an
 *      unregistered type returns. The sentinel itself is pinned by driving a type that
 *      really is unknown, so this check cannot rot into a tautology.
 *   2. NO CALL ON INVALID OPTIONS — the injected fetch throws, so a type that reached
 *      the network on an unusable grader fails here rather than spending a judge call.
 *   3. PROMPT BYTES — each judge entry is run once with a representative input and a
 *      recording fetch; the prompt(s) that reached the wire must equal the registry's
 *      prompt_template with its {name} markers filled in, and the call COUNT must equal
 *      the entry's judge_calls. No test here ever calls a model.
 *
 * The Python mirror is cloud/optimizer/tests/test_grader_registry_parity.py, which
 * holds the same file to GRADER_TYPES and to the Python prompt builders.
 */
import { test } from "node:test";
import assert from "node:assert/strict";
import { existsSync, readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { dirname, join } from "node:path";

import { grade } from "../dist/index.js";

/** Locate the canonical registry by walking up to the repo root. */
function registryPath() {
  let dir = dirname(fileURLToPath(import.meta.url));
  for (;;) {
    const candidates = [
      join(dir, "public", "shared", "contracts", "schemas", "grader-registry.json"),
      join(dir, "packages", "shared", "contracts", "schemas", "grader-registry.json"),
    ];
    for (const candidate of candidates) {
      if (existsSync(candidate)) return candidate;
    }
    const parent = dirname(dir);
    if (parent === dir) throw new Error("grader-registry.json not found above this test");
    dir = parent;
  }
}

const registry = JSON.parse(readFileSync(registryPath(), "utf8"));
const entries = registry.graders;
const byType = new Map(entries.map((e) => [e.type, e]));

const GATEWAY = "http://judge.example.com";
const PROBE = "registry parity probe";
const permissiveSsrf = async () => ({ allowed: true, reason: "test stub" });
const refusingFetch = async () => {
  throw new Error("registry parity: a grader with unusable options must not reach the network");
};

/** UNKNOWN_REASON_PREFIX is grade()'s fail-closed default branch, pinned by a test below. */
const UNKNOWN_REASON_PREFIX = "unknown grader type";

/** emptyOptions fills every declared option with the empty value of its declared type. */
function emptyOptions(entry) {
  const out = {};
  for (const [name, spec] of Object.entries(entry.options.properties ?? {})) {
    switch (spec.type) {
      case "array":
        out[name] = [];
        break;
      case "string":
        out[name] = "";
        break;
      case "number":
      case "integer":
        out[name] = 0;
        break;
      case "boolean":
        out[name] = false;
        break;
      case "object":
        out[name] = {};
        break;
      default:
        out[name] = null;
    }
  }
  return out;
}

/**
 * recordingJudge answers the n-th judge call with `replies[n]` and records the prompt
 * that call carried; a call beyond the script answers non-2xx so an extra call is loud.
 */
function recordingJudge(replies) {
  const prompts = [];
  const fn = async (_url, init) => {
    prompts.push(String(JSON.parse(init.body).input ?? ""));
    const reply = replies[prompts.length - 1];
    if (reply === undefined) return { ok: false, status: 500, json: async () => ({}) };
    return { ok: true, json: async () => ({ output_text: reply }) };
  };
  fn.prompts = prompts;
  return fn;
}

/** fill substitutes one {marker}; the replacement is a function so `$` stays literal. */
function fill(text, marker, value) {
  return text.replace(`{${marker}}`, () => value);
}

/**
 * JUDGE_INPUTS is one representative input per judge type plus the expected prompt(s),
 * rebuilt from the registry template. Values deliberately contain no `{`/`}` so the
 * substitution order cannot matter.
 */
const JUDGE_INPUTS = {
  llm_judge: {
    options: { rubric: "The answer must name the file it changed." },
    candidate: "It changed src/index.ts.",
    replies: ["PASS"],
    prompts: (tpl, o, c) => [fill(fill(tpl, "rubric", o.rubric), "candidate", c)],
  },
  llm_score: {
    options: { rubric: "Score how completely the answer covers the question.", min_score: 0.5 },
    candidate: "The gateway records spend per request.",
    replies: ["SCORE: 0.80"],
    prompts: (tpl, o, c) => [fill(fill(tpl, "rubric", o.rubric), "candidate", c)],
  },
  llm_category: {
    options: {
      prompt: "Decide whether the answer is safe to send to a customer.",
      categories: ["safe", "needs review", "unsafe"],
      passing_categories: ["safe"],
    },
    candidate: "Your invoice is attached.",
    replies: ["CATEGORY: safe"],
    prompts: (tpl, o, c) => [
      fill(fill(fill(tpl, "prompt", o.prompt), "categories", o.categories.join(", ")), "candidate", c),
    ],
  },
  llm_pairwise: {
    options: { baseline: "The old answer.", criteria: "Which answer is more specific?" },
    candidate: "The new answer, with the file name.",
    replies: ["WINNER: A", "WINNER: B"],
    prompts: (tpl, o, c) => [
      fill(fill(fill(tpl, "criteria", o.criteria), "first", c), "second", o.baseline),
      fill(fill(fill(tpl, "criteria", o.criteria), "first", o.baseline), "second", c),
    ],
  },
  llm_answer_match: {
    options: { expected: "Four." },
    candidate: "The answer is 4.",
    replies: ["MATCH: YES"],
    prompts: (tpl, o, c) => [fill(fill(tpl, "expected", o.expected), "candidate", c)],
  },
};

test("grader registry is populated and internally consistent", () => {
  assert.ok(Array.isArray(entries) && entries.length === 29, `expected 29 entries, got ${entries.length}`);
  assert.equal(new Set(entries.map((e) => e.type)).size, entries.length, "grader types must be unique");
  for (const entry of entries) {
    const isJudge = entry.category === "judge";
    assert.equal(
      typeof entry.prompt_template === "string",
      isJudge,
      `${entry.type}: prompt_template belongs to the judge family and only there`,
    );
    assert.equal(entry.judge_calls > 0, isJudge, `${entry.type}: judge_calls must be 0 outside the judge family`);
    if (isJudge) assert.equal(entry.deterministic, false, `${entry.type}: a judge verdict is not deterministic`);
  }
});

test("the unknown-grader sentinel this test relies on is still the fail-closed default", async () => {
  const r = await grade({ type: "definitely_not_a_grader" }, PROBE, { resolutionPinnedFetch: refusingFetch });
  assert.equal(r.passed, false);
  assert.ok(r.reason.startsWith(UNKNOWN_REASON_PREFIX), `sentinel changed: ${r.reason}`);
});

for (const entry of entries.filter((e) => e.python_only !== true)) {
  test(`grader registry: ${entry.type} is registered in the TypeScript dispatch`, async () => {
    const r = await grade({ type: entry.type, ...emptyOptions(entry) }, PROBE, {
      resolutionPinnedFetch: refusingFetch,
      ssrfCheck: permissiveSsrf,
    });
    assert.ok(
      !r.reason.startsWith(UNKNOWN_REASON_PREFIX),
      `${entry.type}: dispatch does not know this registry entry (${r.reason})`,
    );
  });
}

for (const entry of entries.filter((e) => e.python_only === true)) {
  test(`grader registry: ${entry.type} is python_only and fails closed here`, async () => {
    const r = await grade({ type: entry.type, ...emptyOptions(entry) }, PROBE, { resolutionPinnedFetch: refusingFetch });
    assert.equal(r.passed, false);
    assert.ok(
      r.reason.startsWith(UNKNOWN_REASON_PREFIX),
      `${entry.type}: marked python_only but the TypeScript dispatch handles it (${r.reason})`,
    );
  });
}

for (const entry of entries.filter((e) => typeof e.prompt_template === "string")) {
  test(`grader registry: ${entry.type} prompt_template matches the prompt sent`, async () => {
    const input = JUDGE_INPUTS[entry.type];
    assert.ok(input, `${entry.type}: no representative input in this test — add one`);
    const judge = recordingJudge(input.replies);
    await grade({ type: entry.type, ...input.options, gateway_url: GATEWAY }, input.candidate, {
      ssrfCheck: permissiveSsrf,
      resolutionPinnedFetch: judge,
    });
    assert.deepEqual(
      judge.prompts,
      input.prompts(entry.prompt_template, input.options, input.candidate),
      `${entry.type}: prompt bytes differ from the registry template`,
    );
    assert.equal(judge.prompts.length, entry.judge_calls, `${entry.type}: judge_calls is wrong in the registry`);
  });
}

test("grader registry judge coverage", () => {
  const judgeTypes = entries.filter((e) => e.category === "judge").map((e) => e.type).sort();
  assert.deepEqual(judgeTypes, Object.keys(JUDGE_INPUTS).sort(), "every judge type needs a representative input");
  assert.ok(byType.get("llm_pairwise").judge_calls === 2, "llm_pairwise asks both orderings");
});
