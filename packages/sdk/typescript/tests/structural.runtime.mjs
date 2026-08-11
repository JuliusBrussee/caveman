/**
 * Runtime tests for the W15 structural SDK surface: the retry-loop breaker and
 * the async job client. Run with:
 *   node --test packages/sdk-ts/tests/structural.runtime.mjs
 * (Node 22, built-in node:test runner with global fetch mocking.)
 */
import { test } from "node:test";
import assert from "node:assert/strict";

// Import compiled dist (must run `pnpm --filter @caveman-ai/sdk build` first)
import { AsyncJobsUnavailableError, Cave, RetryLoopBreaker, RetryLoopError } from "../dist/index.js";

// ─── Retry-loop breaker ───────────────────────────────────────────────────────

test("retry-loop breaker interrupts the configured loop after N identical repeats", () => {
  const breaker = new RetryLoopBreaker(3); // fires on the 4th identical call
  const args = { id: 42 };
  // 3 identical calls are tolerated...
  for (let i = 0; i < 3; i++) {
    assert.doesNotThrow(() => breaker.record("fetch_order", args));
  }
  // ...the 4th interrupts.
  assert.throws(() => breaker.record("fetch_order", args), RetryLoopError);
});

test("cave.retryLoopBreaker() default threshold fires on the 4th identical call", () => {
  const cave = new Cave({ apiKey: "k", baseURL: "http://localhost:8787", agent: "a" });
  const breaker = cave.retryLoopBreaker();
  let fired = 0;
  for (let i = 0; i < 10; i++) {
    try {
      breaker.record("loop_tool", { same: true });
    } catch (e) {
      assert.ok(e instanceof RetryLoopError);
      fired++;
      break;
    }
  }
  assert.equal(fired, 1, "breaker must fire exactly once on the repeated loop");
});

test("different tool calls reset the streak (no false interrupt)", () => {
  const breaker = new RetryLoopBreaker(2);
  breaker.record("a", { x: 1 });
  breaker.record("a", { x: 1 });
  // Different args reset the streak.
  assert.doesNotThrow(() => breaker.record("a", { x: 2 }));
  assert.doesNotThrow(() => breaker.record("a", { x: 2 }));
  // Different argument ORDER is the same signature (sorted keys) — still same.
  breaker.reset();
  breaker.record("a", { x: 1, y: 2 });
  assert.doesNotThrow(() => breaker.record("a", { y: 2, x: 1 }));
  assert.throws(() => breaker.record("a", { x: 1, y: 2 }), RetryLoopError);
});

test("guard() throws on the call past the threshold without invoking fn", async () => {
  const breaker = new RetryLoopBreaker(1);
  let calls = 0;
  const fn = () => {
    calls++;
    return "ok";
  };
  await breaker.guard("t", { a: 1 }, fn); // repeats=1, ok
  await assert.rejects(() => breaker.guard("t", { a: 1 }, fn), RetryLoopError);
  assert.equal(calls, 1, "fn must not run on the interrupted call");
});

// ─── Async job client ─────────────────────────────────────────────────────────

test("all async job methods fail locally without network or persistence claims", async () => {
  const originalFetch = globalThis.fetch;
  let networkCalls = 0;
  globalThis.fetch = async () => {
    networkCalls++;
    throw new Error("job API must not reach network");
  };
  try {
    const cave = new Cave({ apiKey: "k", baseURL: "http://localhost:8787", agent: "a" });
    const calls = [
      () => cave.jobs.submit({ kind: "summarize" }, { latencyClass: "offline" }),
      () => cave.jobs.status("job-9"),
      () => cave.jobs.wait("job-9"),
      () => cave.jobs.submitAndWait({ kind: "audit" }),
      () => cave.jobs.cancel("job-9"),
    ];
    for (const call of calls) {
      await assert.rejects(call, (err) => err instanceof AsyncJobsUnavailableError && err.code === "cave_async_jobs_unavailable");
    }
    assert.equal(networkCalls, 0);
  } finally {
    globalThis.fetch = originalFetch;
  }
});
