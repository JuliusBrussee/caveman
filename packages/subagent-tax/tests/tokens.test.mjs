import { test } from "node:test";
import assert from "node:assert/strict";

import { estimateTokens, buildCountTokensRequest, countTokensAnthropic, DEFAULT_CHARS_PER_TOKEN } from "../lib/tokens.mjs";
import { startSink } from "../lib/sink.mjs";

test("estimateTokens math and labeling", () => {
  const est = estimateTokens(64000);
  assert.equal(est.basis, "est");
  assert.equal(est.ratio, DEFAULT_CHARS_PER_TOKEN);
  assert.equal(est.tokens, Math.round(64000 / DEFAULT_CHARS_PER_TOKEN));
  assert.equal(estimateTokens(100, 5).tokens, 20);
  assert.throws(() => estimateTokens(100, 0));
});

test("buildCountTokensRequest passes captured fields through verbatim", () => {
  const body = { model: "claude-opus-5", system: "s", tools: [{ name: "t" }], messages: [{ role: "user", content: "hi" }], stream: true, max_tokens: 5 };
  const req = buildCountTokensRequest(body);
  assert.deepEqual(Object.keys(req).sort(), ["messages", "model", "system", "tools"]);
  assert.equal(req.system, body.system);
  assert.equal(req.tools, body.tools);
  assert.equal(buildCountTokensRequest({ system: "no model or messages" }), null);
});

test("countTokensAnthropic against a local endpoint; never guesses on error", async () => {
  const { server, port } = await startSink({ port: 0 });
  const ok = await countTokensAnthropic(
    { model: "m", messages: [{ role: "user", content: "hi" }] },
    { apiKey: "k", baseUrl: `http://127.0.0.1:${port}` },
  );
  assert.equal(ok.basis, "exact");
  assert.equal(typeof ok.tokens, "number");
  server.close();

  const bad = await countTokensAnthropic({ notCountable: true }, { apiKey: "k", baseUrl: "http://127.0.0.1:1" });
  assert.ok(bad.error);
  assert.equal(bad.tokens, undefined);
});
