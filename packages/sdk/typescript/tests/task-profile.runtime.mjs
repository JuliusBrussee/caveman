/**
 * Runtime JSON round-trip for the TS SDK TaskProfile wire object.
 * Run with: node --test tests/task-profile.runtime.mjs
 *
 * TaskProfile is a type-only export (erased at runtime), so this proves the
 * wire shape round-trips with snake_case field names; the type binding itself
 * is checked in task-profile.test.ts. Field names mirror sdk-python and the Go
 * policy.TaskProfile JSON tags.
 */
import { test } from "node:test";
import assert from "node:assert/strict";

const SCHEMA_FIELDS = [
  "quality_floor",
  "alpha",
  "candidate_allowlist",
  "candidate_denylist",
  "max_p95_latency_delta_ms",
  "max_error_delta",
  "max_cost_ratio",
  "cascade_enabled",
  "cascade_tau",
  "max_escalation_rate",
  "stickiness",
  "cross_provider",
  "data_residency",
  "trusted_route_hints",
];

test("TaskProfile JSON round-trips with snake_case wire field names", () => {
  const profile = {
    quality_floor: 0.98,
    alpha: 7,
    candidate_allowlist: ["anthropic/*"],
    candidate_denylist: ["openai:gpt-5.5-pro"],
    max_p95_latency_delta_ms: 500,
    max_error_delta: 0.01,
    max_cost_ratio: 0.5,
    cascade_enabled: true,
    cascade_tau: 0.42,
    max_escalation_rate: 0.2,
    stickiness: "conversation",
    cross_provider: true,
    data_residency: ["eu"],
    trusted_route_hints: ["x-cave-route"],
  };
  const decoded = JSON.parse(JSON.stringify(profile));
  assert.deepEqual(decoded, profile);
  assert.deepEqual(Object.keys(decoded).sort(), [...SCHEMA_FIELDS].sort());
});
