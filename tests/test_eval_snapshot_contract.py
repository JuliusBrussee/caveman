from __future__ import annotations

import copy
import importlib.util
import json
import tempfile
import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
MODULE_PATH = ROOT / "evals" / "snapshot_contract.py"
SPEC = importlib.util.spec_from_file_location("snapshot_contract", MODULE_PATH)
assert SPEC and SPEC.loader
snapshot_contract = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(snapshot_contract)


class EvalSnapshotContractTests(unittest.TestCase):
    def setUp(self) -> None:
        self.snapshot = json.loads(
            (ROOT / "evals" / "snapshots" / "results.json").read_text(
                encoding="utf-8"
            )
        )

    # Proves the committed complete matrix is accepted as the positive control.
    def test_committed_snapshot_is_valid(self) -> None:
        self.assertIs(
            self.snapshot, snapshot_contract.validate_snapshot(self.snapshot)
        )

    # Proves reporting cannot proceed without either comparison control.
    def test_missing_control_arm_is_rejected(self) -> None:
        snapshot = copy.deepcopy(self.snapshot)
        del snapshot["arms"]["__terse__"]

        with self.assertRaisesRegex(
            snapshot_contract.SnapshotContractError, "required control arms missing"
        ):
            snapshot_contract.validate_snapshot(snapshot)

    # Proves one truncated arm cannot masquerade as a complete evidence matrix.
    def test_truncated_skill_arm_is_rejected(self) -> None:
        snapshot = copy.deepcopy(self.snapshot)
        snapshot["arms"]["caveman"].pop()

        with self.assertRaisesRegex(
            snapshot_contract.SnapshotContractError, "expected 10"
        ):
            snapshot_contract.validate_snapshot(snapshot)

    # Proves malformed outputs are rejected before token measurement.
    def test_non_string_output_is_rejected(self) -> None:
        snapshot = copy.deepcopy(self.snapshot)
        snapshot["arms"]["caveman"][0] = {"text": "not a raw output"}

        with self.assertRaisesRegex(
            snapshot_contract.SnapshotContractError, "output 0 must be a string"
        ):
            snapshot_contract.validate_snapshot(snapshot)

    # Proves declared sample size must equal the prompt matrix cardinality.
    def test_metadata_prompt_count_mismatch_is_rejected(self) -> None:
        snapshot = copy.deepcopy(self.snapshot)
        snapshot["metadata"]["n_prompts"] += 1

        with self.assertRaisesRegex(
            snapshot_contract.SnapshotContractError, "prompts contains 10 items"
        ):
            snapshot_contract.validate_snapshot(snapshot)

    # Proves controls alone cannot be reported as evidence for a skill.
    def test_snapshot_without_skill_arm_is_rejected(self) -> None:
        snapshot = copy.deepcopy(self.snapshot)
        snapshot["arms"] = {
            name: outputs
            for name, outputs in snapshot["arms"].items()
            if name in snapshot_contract.CONTROL_ARMS
        }

        with self.assertRaisesRegex(
            snapshot_contract.SnapshotContractError, "at least one skill arm"
        ):
            snapshot_contract.validate_snapshot(snapshot)

    # Proves the file-loading boundary rejects malformed evidence before reporting.
    def test_loader_rejects_malformed_json(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "results.json"
            path.write_text('{"metadata":', encoding="utf-8")

            with self.assertRaisesRegex(
                snapshot_contract.SnapshotContractError, "cannot read"
            ):
                snapshot_contract.load_snapshot(path)


if __name__ == "__main__":
    unittest.main()
