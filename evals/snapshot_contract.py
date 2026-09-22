"""Fail-closed validation for committed evaluation snapshots."""

from __future__ import annotations

import json
from pathlib import Path
from typing import Any


CONTROL_ARMS = {"__baseline__", "__terse__"}
REQUIRED_METADATA = {
    "generated_at",
    "claude_cli_version",
    "model",
    "n_prompts",
    "terse_prefix",
}


class SnapshotContractError(ValueError):
    """Snapshot evidence is absent, incomplete, or malformed."""


def validate_snapshot(data: Any) -> dict[str, Any]:
    """Return a complete snapshot or raise a precise contract error."""
    if not isinstance(data, dict):
        raise SnapshotContractError("snapshot must be a JSON object")

    metadata = data.get("metadata")
    prompts = data.get("prompts")
    arms = data.get("arms")
    if not isinstance(metadata, dict):
        raise SnapshotContractError("metadata must be an object")
    missing_metadata = REQUIRED_METADATA - metadata.keys()
    if missing_metadata:
        raise SnapshotContractError(
            f"metadata missing: {', '.join(sorted(missing_metadata))}"
        )
    for field in REQUIRED_METADATA - {"n_prompts"}:
        if not isinstance(metadata[field], str) or not metadata[field]:
            raise SnapshotContractError(f"metadata.{field} must be a non-empty string")

    n_prompts = metadata["n_prompts"]
    if isinstance(n_prompts, bool) or not isinstance(n_prompts, int) or n_prompts < 1:
        raise SnapshotContractError("metadata.n_prompts must be a positive integer")
    if not isinstance(prompts, list) or not prompts:
        raise SnapshotContractError("prompts must be a non-empty list")
    if not all(isinstance(prompt, str) and prompt for prompt in prompts):
        raise SnapshotContractError("every prompt must be a non-empty string")
    if len(prompts) != n_prompts:
        raise SnapshotContractError(
            f"metadata.n_prompts is {n_prompts}, but prompts contains {len(prompts)} items"
        )

    if not isinstance(arms, dict):
        raise SnapshotContractError("arms must be an object")
    missing_controls = CONTROL_ARMS - arms.keys()
    if missing_controls:
        raise SnapshotContractError(
            f"required control arms missing: {', '.join(sorted(missing_controls))}"
        )
    skill_arms = set(arms) - CONTROL_ARMS
    if not skill_arms:
        raise SnapshotContractError("at least one skill arm is required")

    for arm, outputs in arms.items():
        if not isinstance(arm, str) or not arm:
            raise SnapshotContractError("arm names must be non-empty strings")
        if not isinstance(outputs, list):
            raise SnapshotContractError(f"arm {arm} must contain a list of outputs")
        if len(outputs) != n_prompts:
            raise SnapshotContractError(
                f"arm {arm} contains {len(outputs)} outputs; expected {n_prompts}"
            )
        for position, output in enumerate(outputs):
            if not isinstance(output, str):
                raise SnapshotContractError(
                    f"arm {arm} output {position} must be a string"
                )
    return data


def load_snapshot(path: Path) -> dict[str, Any]:
    """Read and validate one snapshot before a consumer reports from it."""
    try:
        data = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as error:
        raise SnapshotContractError(f"cannot read {path}: {error}") from error
    return validate_snapshot(data)
