import test from "node:test";
import assert from "node:assert/strict";
import { PRACTICE_REGISTRY } from "../dist/practices.generated.js";

test("generated public practice registry is complete, unique, and path-free", () => {
  // 110 = lane 2's 108 + slice D's two wrap_directive recipes
  // (exploration-offload-directive, deferred-tool-loading) — re-pinned
  // deliberately with the registry change (AUTOPILOT_SPEC §8.4/§11.6).
  assert.equal(PRACTICE_REGISTRY.length, 110);
  assert.equal(new Set(PRACTICE_REGISTRY.map((practice) => practice.id)).size, 110);
  assert.equal(new Set(PRACTICE_REGISTRY.map((practice) => practice.skill_render.skill_name)).size, 110);
  assert.equal(PRACTICE_REGISTRY.filter((practice) => practice.skill_render.eligible).length, 96);

  for (const practice of PRACTICE_REGISTRY) {
    assert.equal(practice.evidence.status, "unmeasured");
    assert.ok(!("sources" in practice), `${practice.id} leaked source paths`);
    assert.ok(!("cave_agent" in practice), `${practice.id} leaked private Cave Agent scope`);
  }
});
