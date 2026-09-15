from __future__ import annotations

import json
from dataclasses import asdict

from caveman_cloud import TaskProfile

# The 14 wire field names, mirroring the Go policy.TaskProfile JSON tags and the
# TS SDK TaskProfile interface. snake_case, identical across both SDKs.
SCHEMA_FIELDS = {
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
}


def test_task_profile_field_names_match_schema() -> None:
    """Every dataclass field serializes under its snake_case wire name."""
    assert set(asdict(TaskProfile()).keys()) == SCHEMA_FIELDS


def test_task_profile_json_round_trip() -> None:
    """A fully-populated profile survives JSON serialize + deserialize unchanged."""
    tp = TaskProfile(
        quality_floor=0.98,
        alpha=7,
        candidate_allowlist=["anthropic/*"],
        candidate_denylist=["openai:gpt-5.5-pro"],
        max_p95_latency_delta_ms=500,
        max_error_delta=0.01,
        max_cost_ratio=0.5,
        cascade_enabled=True,
        cascade_tau=0.42,
        max_escalation_rate=0.2,
        stickiness="conversation",
        cross_provider=True,
        data_residency=["eu"],
        trusted_route_hints=["x-cave-route"],
    )
    decoded = json.loads(json.dumps(asdict(tp)))
    assert set(decoded.keys()) == SCHEMA_FIELDS
    assert TaskProfile(**decoded) == tp
