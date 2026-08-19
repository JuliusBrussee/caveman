import assert from "node:assert/strict";
import test from "node:test";
import { fileURLToPath } from "node:url";
import { join, dirname } from "node:path";

const dist = join(dirname(fileURLToPath(import.meta.url)), "..", "dist", "testable.mjs");
const {
  MAX_CONTEXT_BYTES,
  ROUTES_BY_API,
  additionalContextOf,
  isLoopbackUrl,
  outputReplacementOf,
  routeForApi,
  sanitizeHookResponse,
  taskContinuation,
  taskTerms,
  taskType,
  promptDigest,
  resolveHookInvocations,
} = await import(dist);

test("route table maps supported APIs and refuses everything else", () => {
  const gw = "http://127.0.0.1:8787";
  assert.equal(routeForApi(gw, "anthropic-messages"), `${gw}/w/pi`);
  assert.equal(routeForApi(gw, "openai-completions"), `${gw}/w/pi/openai/v1`);
  assert.equal(routeForApi(gw, "openai-responses"), `${gw}/w/pi/openai/v1`);
  assert.equal(routeForApi(gw, "google-generative-ai"), `${gw}/w/pi/v1beta`);
  for (const api of ["azure-openai-responses", "openai-codex-responses", "mistral-conversations", "google-vertex", "bedrock-converse-stream", "made-up", undefined]) {
    assert.equal(routeForApi(gw, api), undefined, `api ${api} must not route`);
  }
  assert.equal(Object.keys(ROUTES_BY_API).length, 4);
});

test("loopback detection", () => {
  assert.ok(isLoopbackUrl("http://127.0.0.1:8787"));
  assert.ok(isLoopbackUrl("http://localhost:8787"));
  assert.ok(!isLoopbackUrl("https://gateway.example.com"));
  assert.ok(!isLoopbackUrl("not a url"));
});

test("sanitizeHookResponse drops oversized and mistyped fields", () => {
  const big = "x".repeat(MAX_CONTEXT_BYTES + 1);
  const clean = sanitizeHookResponse({
    context: big,
    message: 42,
    output_replacement: "ok",
    hookSpecificOutput: { additionalContext: "ctx", updatedToolOutput: big + big },
  });
  assert.equal(clean.context, undefined);
  assert.equal(clean.message, undefined);
  assert.equal(clean.output_replacement, "ok");
  assert.equal(clean.hookSpecificOutput.additionalContext, "ctx");
  // 128 KiB fits under the 2 MiB replacement cap and is kept.
  assert.equal(clean.hookSpecificOutput.updatedToolOutput, big + big);
  assert.deepEqual(sanitizeHookResponse(null), {});
  assert.deepEqual(sanitizeHookResponse([1]), {});
});

test("both response shapes are accepted for context and replacement", () => {
  assert.equal(additionalContextOf({ hookSpecificOutput: { additionalContext: "a" } }), "a");
  assert.equal(additionalContextOf({ context: "b" }), "b");
  assert.equal(additionalContextOf({}), undefined);
  assert.equal(outputReplacementOf({ hookSpecificOutput: { updatedToolOutput: "u" } }), "u");
  assert.equal(outputReplacementOf({ output_replacement: "r" }), "r");
  assert.equal(outputReplacementOf(undefined), undefined);
});

test("prompt digest carries bytes+sha256, never raw text", () => {
  const digest = promptDigest("secret prompt");
  assert.equal(digest.bytes, 13);
  assert.match(digest.sha256, /^sha256:[0-9a-f]{64}$/);
  assert.ok(!JSON.stringify(digest).includes("secret"));
});

test("task profiling ports match the shared vocabulary", () => {
  assert.equal(taskType("fix the crash in parser"), "bugfix");
  assert.equal(taskType("please investigate why does it hang"), "investigation");
  assert.equal(taskType("hello there"), "general");
  const terms = taskTerms("Fix the parser crash in src/parser.ts sk-abc123 pretty please");
  assert.ok(terms.includes("parser"));
  assert.ok(!terms.some((t) => t.startsWith("sk-")), "secret-shaped tokens stay out");
  assert.ok(taskContinuation("continue"));
  assert.ok(taskContinuation("go ahead"));
  assert.ok(!taskContinuation("rewrite the entire billing system from scratch today"));
});

test("hook invocation resolution honors CAVEMAN_PI_HOOK_CMD with fallback", () => {
  const stamped = resolveHookInvocations({ CAVEMAN_PI_HOOK_CMD: JSON.stringify(["/n/node", "/x/cli.js"]) });
  assert.deepEqual(stamped, [{ command: "/n/node", args: ["/x/cli.js"] }]);
  const fallback = resolveHookInvocations({ CAVEMAN_PI_HOOK_CMD: "not json", CAVEMAN_HOME: "/x/.caveman" });
  assert.deepEqual(fallback.map((i) => i.command), ["caveman", "cave", "/x/.caveman/bin/caveman"]);
  assert.deepEqual(resolveHookInvocations({ CAVEMAN_HOME: "/x/.caveman" }).map((i) => i.command), ["caveman", "cave", "/x/.caveman/bin/caveman"]);
});
