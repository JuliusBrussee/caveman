/**
 * Type-level tests for the TS SDK TaskProfile wire object.
 *
 * Compiled by `tsc --noEmit` (the package's "test" script). The runtime JSON
 * round-trip lives in task-profile.runtime.mjs; this file locks the field names
 * and optionality against the Go policy.TaskProfile struct and sdk-python.
 */
import type { TaskProfile } from "../src/index.js";

// Full profile: every field present under its snake_case wire name.
const _full: TaskProfile = {
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

// Every field is optional → an empty object is a valid TaskProfile.
const _empty: TaskProfile = {};

// Field types are readable back out with the expected shapes.
const _floor: number | undefined = _full.quality_floor;
const _allow: string[] | undefined = _full.candidate_allowlist;
const _sticky: string | undefined = _full.stickiness;
const _cascade: boolean | undefined = _full.cascade_enabled;
const _residency: string[] | undefined = _full.data_residency;
