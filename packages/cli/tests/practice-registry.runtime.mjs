import test from "node:test";
import assert from "node:assert/strict";
import { existsSync, readFileSync } from "node:fs";
import { PRACTICE_REGISTRY } from "../dist/practices.generated.js";

test("generated public practice registry is complete, unique, and path-free", () => {
  const canonicalURL = new URL("../../../docs/strategy/practices-initial.yaml", import.meta.url);
  if (existsSync(canonicalURL)) {
    const canonical = readFileSync(canonicalURL, "utf8");
    const canonicalIDs = [...canonical.matchAll(/^- id:\s+([a-z0-9-]+)\s*$/gm)].map((match) => match[1]);
    const canonicalEligibleCount = [...canonical.matchAll(/^    eligible: true\s*$/gm)].length;
    assert.deepEqual(PRACTICE_REGISTRY.map((practice) => practice.id), canonicalIDs);
    assert.equal(PRACTICE_REGISTRY.filter((practice) => practice.skill_render.eligible).length, canonicalEligibleCount);
  }
  assert.equal(new Set(PRACTICE_REGISTRY.map((practice) => practice.id)).size, PRACTICE_REGISTRY.length);
  assert.equal(new Set(PRACTICE_REGISTRY.map((practice) => practice.skill_render.skill_name)).size, PRACTICE_REGISTRY.length);
  for (const id of ["unlabeled-traffic", "retry-loop-reduction", "model-right-sizing", "model-distillation-candidate", "fusion-routing", "wasted-reasoning-suppression", "wake-gate", "loop-bound-guard", "error-spend-reduction", "retry-idempotency-guard", "gemini-explicit-cache", "gemini-cache-ttl-right-sizing"]) {
    assert.equal(PRACTICE_REGISTRY.some((practice) => practice.id === id), false, `${id} remains public`);
  }

  for (const practice of PRACTICE_REGISTRY) {
    assert.equal(practice.evidence.status, "unmeasured");
    assert.ok(!("sources" in practice), `${practice.id} leaked source paths`);
    assert.ok(!("cave_agent" in practice), `${practice.id} leaked private Cave Agent scope`);
  }

  const fanout = PRACTICE_REGISTRY.find((practice) => practice.id === "subagent-fanout-guard");
  assert.ok(fanout);
  assert.equal(fanout.family, "cache");
  assert.equal(fanout.skill_render.eligible, false);
  assert.match(fanout.never_claim, /Never derive a cap/);
  assert.match(fanout.never_claim, /mints nothing/);

  const anthropicBreakpoint = PRACTICE_REGISTRY.find((practice) => practice.id === "anthropic-cache-breakpoints");
  assert.ok(anthropicBreakpoint);
  const anthropicContract = [
    anthropicBreakpoint.title,
    anthropicBreakpoint.predicate.payload_shape,
    anthropicBreakpoint.fix.prose,
    anthropicBreakpoint.never_claim,
  ].join(" ");
  assert.match(anthropicContract, /explicit/);
  assert.match(anthropicContract, /last non-deferred tool/);
  assert.match(anthropicContract, /last system block/);
  assert.match(anthropicContract, /net-negative without reuse/);
  assert.doesNotMatch(anthropicBreakpoint.fix.prose, /top-level|automatic movement/i);
});
