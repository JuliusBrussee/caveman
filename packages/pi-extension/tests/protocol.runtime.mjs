import assert from "node:assert/strict";
import test from "node:test";
import { fileURLToPath, pathToFileURL } from "node:url";
import { join, dirname } from "node:path";

// pathToFileURL, not a bare path: dynamic import() of an absolute Windows path
// throws ERR_UNSUPPORTED_ESM_URL_SCHEME ("Received protocol 'd:'") because the
// drive letter reads as a URL scheme.
const dist = pathToFileURL(join(dirname(fileURLToPath(import.meta.url)), "..", "dist", "testable.mjs")).href;
const {
  MAX_CONTEXT_BYTES,
  ROUTES_BY_API,
  additionalContextOf,
  hostOf,
  isLoopbackUrl,
  outputReplacementOf,
  routeForApi,
  compatUpstreamFor,
  sanitizeHookResponse,
  taskContinuation,
  taskTerms,
  taskType,
  upstreamHostFor,
  promptDigest,
  resolveHookInvocations,
} = await import(dist);

test("route table maps supported APIs and refuses everything else", () => {
  const gw = "http://127.0.0.1:8787";
  assert.equal(routeForApi(gw, "anthropic-messages"), `${gw}/w/pi`);
  assert.equal(routeForApi(gw, "openai-completions"), `${gw}/w/pi/openai/v1`);
  assert.equal(routeForApi(gw, "openai-responses"), `${gw}/w/pi/openai/v1`);
  assert.equal(routeForApi(gw, "google-generative-ai"), `${gw}/w/pi/v1beta`);
  assert.equal(routeForApi(gw, "openai-responses", "opencode-go"), `${gw}/w/pi/compat/opencode-go/v1`);
  assert.equal(routeForApi(gw, "openai-completions", "opencode-go"), `${gw}/w/pi/compat/opencode-go/v1`);
  assert.equal(routeForApi(gw, "anthropic-messages", "opencode-go"), `${gw}/w/pi/compat/opencode-go`);
  assert.equal(routeForApi(gw, "openai-responses", "openrouter"), undefined, "unknown provider must not inherit OpenAI upstream");
  assert.equal(routeForApi(gw, "anthropic-messages", "anthropic"), `${gw}/w/pi`);
  assert.equal(routeForApi(gw, "openai-completions", "openai"), `${gw}/w/pi/openai/v1`);
  assert.equal(routeForApi(gw, "openai-responses", "openai"), `${gw}/w/pi/openai/v1`);
  assert.equal(routeForApi(gw, "google-generative-ai", "google"), `${gw}/w/pi/v1beta`);
  assert.equal(routeForApi(gw, "openai-completions", "stubprov"), undefined, "provider outside allowlist must not route");
  for (const api of ["azure-openai-responses", "openai-codex-responses", "mistral-conversations", "google-vertex", "bedrock-converse-stream", "made-up", undefined]) {
    assert.equal(routeForApi(gw, api), undefined, `api ${api} must not route`);
  }
  assert.equal(Object.keys(ROUTES_BY_API).length, 4);
});

test("route gate compares the original provider host with the proxy upstream host", () => {
  const gw = "http://127.0.0.1:8787";
  assert.equal(routeForApi(gw, "openai-completions", "openai", "https://api.openai.com/v1"), `${gw}/w/pi/openai/v1`);
  assert.equal(routeForApi(gw, "openai-completions", "openai", "http://127.0.0.1:4000/v1"), undefined, "a local relay named openai must stay direct");
  assert.equal(routeForApi(gw, "openai-completions", "openai", "https://my-resource.openai.azure.com/openai"), undefined, "Azure named openai must stay direct");
  assert.equal(routeForApi(gw, "anthropic-messages", "anthropic", "https://api.anthropic.com"), `${gw}/w/pi`);
  assert.equal(routeForApi(gw, "anthropic-messages", "anthropic", "http://127.0.0.1:1"), undefined);
  assert.equal(routeForApi(gw, "google-generative-ai", "google", "https://generativelanguage.googleapis.com"), `${gw}/w/pi/v1beta`);
  assert.equal(routeForApi(gw, "anthropic-messages", "opencode-go", "https://opencode.ai/zen/go"), `${gw}/w/pi/compat/opencode-go`);
  assert.equal(routeForApi(gw, "openai-completions", "opencode-go", "https://opencode.ai/zen/go/v1"), `${gw}/w/pi/compat/opencode-go/v1`);
  assert.equal(routeForApi(gw, "openai-completions", "opencode-go", "https://other.example/v1"), undefined, "a moved opencode-go endpoint must stay direct");
  assert.equal(routeForApi(gw, "openai-completions", "openai", "not a url"), undefined, "an unreadable base URL must stay direct");
  assert.equal(upstreamHostFor("openai"), "api.openai.com");
  assert.equal(upstreamHostFor("openrouter"), undefined);
  assert.equal(hostOf("https://API.OpenAI.com/v1"), "api.openai.com");
  assert.equal(hostOf("not a url"), undefined);
});

test("a published compat mount routes a custom provider and gates it on the mount host", () => {
  const gw = "http://127.0.0.1:8787";
  const mounts = { "my-relay": "http://127.0.0.1:4000", "opencode-go": "http://127.0.0.1:9000/zen" };
  assert.equal(routeForApi(gw, "openai-completions", "my-relay", "http://127.0.0.1:4000/v1", mounts), `${gw}/w/pi/compat/my-relay/v1`);
  assert.equal(routeForApi(gw, "openai-responses", "my-relay", "http://127.0.0.1:4000/v1", mounts), `${gw}/w/pi/compat/my-relay/v1`);
  assert.equal(routeForApi(gw, "anthropic-messages", "my-relay", "http://127.0.0.1:4000", mounts), `${gw}/w/pi/compat/my-relay`);
  assert.equal(routeForApi(gw, "google-generative-ai", "my-relay", "http://127.0.0.1:4000", mounts), undefined, "google has no compat route");
  assert.equal(routeForApi(gw, "openai-completions", "my-relay", "https://api.openai.com/v1", mounts), undefined, "host mismatch must stay direct");
  assert.equal(routeForApi(gw, "openai-completions", "my-relay", "http://127.0.0.1:4001/v1", mounts), undefined, "another loopback port is another upstream");
  assert.equal(routeForApi(gw, "openai-completions", "unlisted-relay", "http://127.0.0.1:4000/v1", mounts), undefined, "a provider with no mount stays direct");
  // A caveman.yaml entry that repoints a built-in name wins over the static table.
  assert.equal(routeForApi(gw, "openai-completions", "opencode-go", "http://127.0.0.1:9000/zen/v1", mounts), `${gw}/w/pi/compat/opencode-go/v1`);
  assert.equal(routeForApi(gw, "openai-completions", "opencode-go", "https://opencode.ai/zen/go/v1", mounts), undefined, "the static host no longer applies once the mount moved");
  // A name that could not be a mount, or a non-string value, falls back to the static table.
  assert.equal(routeForApi(gw, "openai-completions", "openai", "https://api.openai.com/v1", { "": "http://x", "UPPER": "http://x" }), `${gw}/w/pi/openai/v1`);
  assert.equal(routeForApi(gw, "openai-completions", "constructor", "http://127.0.0.1:4000/v1", {}), undefined, "inherited properties are not mounts");
  assert.equal(compatUpstreamFor("my-relay", mounts), "http://127.0.0.1:4000");
  assert.equal(compatUpstreamFor("My-Relay", mounts), undefined);
  assert.equal(compatUpstreamFor("my-relay", undefined), undefined);
  assert.equal(hostOf("http://127.0.0.1:4000/v1"), "127.0.0.1:4000");
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
  const localBinFromHome = join("/x/.caveman", "bin", "caveman");
  const stamped = resolveHookInvocations({ CAVEMAN_PI_HOOK_CMD: JSON.stringify(["/n/node", "/x/cli.js"]), CAVEMAN_HOME: "/x/.caveman" });
  // Stamped first, but the PATH candidates must still follow it: the stamp bakes
  // an absolute node path at enable time, so an nvm switch ENOENTs every hook
  // call and a stamp-only list degrades the session to silent direct mode.
  assert.deepEqual(stamped[0], { command: "/n/node", args: ["/x/cli.js"] });
  assert.deepEqual(stamped.map((i) => i.command), ["/n/node", "caveman", "cave", localBinFromHome]);
  // Build the expected local-bin path with join() rather than hardcoding a
  // POSIX string: resolveHookInvocations joins too, so on Windows it yields
  // "\x\.caveman\bin\caveman" and a literal comparison fails on separators
  // alone — a platform-coupled assertion, not a real difference.
  const localBin = join("/x/.caveman", "bin", "caveman");
  const fallback = resolveHookInvocations({ CAVEMAN_PI_HOOK_CMD: "not json", CAVEMAN_HOME: "/x/.caveman" });
  assert.deepEqual(fallback.map((i) => i.command), ["caveman", "cave", localBin]);
  assert.deepEqual(resolveHookInvocations({ CAVEMAN_HOME: "/x/.caveman" }).map((i) => i.command), ["caveman", "cave", localBin]);
});
