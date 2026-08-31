import copy
import json
import tempfile
import unittest
from pathlib import Path

from evals.caveman_contract import (
    ContractError,
    REQUIRED_NEGATIVE_FAMILIES,
    REQUIRED_SURFACES,
    load_manifest,
    parse_switch,
    production_surfaces,
    read_json_strict,
    validate_manifest_structure,
    validate_raw_snapshot,
    validate_surface_contract,
)


class CavemanSemanticContractTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.manifest = load_manifest()
        # Production call sites: caveman-activate.js, caveman-init.js,
        # installOpenclaw(), command artifacts, and buildPrimer/buildReminder().
        cls.surfaces = production_surfaces()

    def test_manifest_has_seven_stable_groups_and_nine_negative_families(self):
        self.assertEqual(
            {group["id"] for group in self.manifest["invariant_groups"]},
            {f"CAV-SEM-{number:02d}" for number in range(1, 8)},
        )
        self.assertEqual(
            {case["family"] for case in self.manifest["model_cases"]},
            REQUIRED_NEGATIVE_FAMILIES,
        )
        self.assertTrue(self.manifest["calibration_material"]["replaceable"])

    def test_real_production_surfaces_satisfy_applicable_contract(self):
        self.assertEqual(set(self.surfaces), REQUIRED_SURFACES)
        validate_surface_contract(self.manifest, self.surfaces)

    def test_polarity_narrowing_mutant_is_rejected(self):
        mutant = dict(self.surfaces)
        mutant["canonical_skill"] = mutant["canonical_skill"].replace(
            "Never drop not/never/no/only/except", "Never drop not/never/no/only"
        )
        with self.assertRaisesRegex(ContractError, "except|polarity"):
            validate_surface_contract(self.manifest, mutant)

    def test_safety_class_narrowing_mutant_is_rejected(self):
        mutant = copy.deepcopy(self.manifest)
        mutant["semantic_classes"]["safety"].remove("legal-medical")
        with self.assertRaisesRegex(ContractError, "safety"):
            validate_manifest_structure(mutant)

    def test_warning_narrowing_mutant_is_rejected(self):
        mutant = dict(self.surfaces)
        mutant["canonical_skill"] = mutant["canonical_skill"].replace(
            "**Warning:** This will permanently delete all rows in the `users` table and cannot be undone.",
            "Note: this deletes rows.",
        )
        with self.assertRaisesRegex(ContractError, "warning"):
            validate_surface_contract(self.manifest, mutant)

    def test_language_gate_narrowing_mutant_is_rejected(self):
        mutant = dict(self.surfaces)
        mutant["canonical_skill"] = mutant["canonical_skill"].replace("classical Chinese", "compressed prose")
        with self.assertRaisesRegex(ContractError, "language"):
            validate_surface_contract(self.manifest, mutant)

    def test_artifact_gate_narrowing_mutant_is_rejected(self):
        mutant = dict(self.surfaces)
        mutant["canonical_skill"] = mutant["canonical_skill"].replace("Persisted outside chat", "Some text")
        with self.assertRaisesRegex(ContractError, "artifact"):
            validate_surface_contract(self.manifest, mutant)

    def test_caricature_gate_narrowing_mutant_is_rejected(self):
        mutant = dict(self.surfaces)
        mutant["canonical_skill"] = mutant["canonical_skill"].replace("No causal arrows", "Arrows discouraged")
        with self.assertRaisesRegex(ContractError, "caricature"):
            validate_surface_contract(self.manifest, mutant)

    def test_exact_literal_narrowing_mutant_is_rejected(self):
        mutant = dict(self.surfaces)
        mutant["canonical_skill"] = mutant["canonical_skill"].replace(
            "`250 ms` must not become `250ms`", "Do not reformat units"
        )
        with self.assertRaisesRegex(ContractError, "exactness"):
            validate_surface_contract(self.manifest, mutant)

    def test_example_overfit_mutant_is_rejected(self):
        mutant = copy.deepcopy(self.manifest)
        mutant["invariant_groups"][0]["markers"].append(
            mutant["calibration_material"]["must_not_be_contract_assertions"][0]
        )
        with self.assertRaisesRegex(ContractError, "calibration wording pinned"):
            validate_surface_contract(mutant, self.surfaces)

    def test_intensity_row_swap_mutant_is_rejected(self):
        mutant = dict(self.surfaces)
        full = "| **full** | Drop articles; fragments and short synonyms OK. Classic caveman |"
        ultra = "| **ultra** | Strip unneeded conjunctions. State each fact once. One word when enough |"
        mutant["canonical_skill"] = mutant["canonical_skill"].replace(full, "SWAP", 1)
        mutant["canonical_skill"] = mutant["canonical_skill"].replace(ultra, full.replace("**full**", "**ultra**"), 1)
        mutant["canonical_skill"] = mutant["canonical_skill"].replace("SWAP", ultra.replace("**ultra**", "**full**"), 1)
        with self.assertRaisesRegex(ContractError, "intensity_rows"):
            validate_surface_contract(self.manifest, mutant)

    def test_missing_or_bypassed_surface_is_rejected(self):
        mutant = dict(self.surfaces)
        mutant.pop("mv3_reminder")
        with self.assertRaisesRegex(ContractError, "surface evidence missing"):
            validate_surface_contract(self.manifest, mutant)

        narrowed_manifest = copy.deepcopy(self.manifest)
        safety_group = next(
            group for group in narrowed_manifest["invariant_groups"]
            if group["id"] == "CAV-SEM-05"
        )
        safety_group["surfaces"].remove("hook_fallback")
        with self.assertRaisesRegex(ContractError, "CAV-SEM-05 surface coverage differs"):
            validate_manifest_structure(narrowed_manifest)

    def test_every_documented_switch_is_reachable_through_production_parser(self):
        for switch in self.manifest["documented_switches"]:
            actual = parse_switch(switch["prompt"])
            expected = {key: value for key, value in switch.items() if key != "prompt"}
            self.assertEqual(actual, expected, switch["prompt"])
        for prompt in self.manifest["parser_non_switches"]:
            self.assertIsNone(parse_switch(prompt), prompt)

    def test_unsupported_phrase_promise_mutant_is_rejected(self):
        mutant = copy.deepcopy(self.manifest)
        mutant["documented_switches"].append({"prompt": "be ordinary now", "action": "clear"})
        promised = mutant["documented_switches"][-1]
        self.assertNotEqual(parse_switch(promised["prompt"]), {"action": "clear"})

    def test_absent_malformed_and_partial_reads_never_pass_as_no_evidence(self):
        with tempfile.TemporaryDirectory(prefix="caveman-evidence-") as raw_tmp:
            root = Path(raw_tmp)
            with self.assertRaisesRegex(ContractError, "cannot read"):
                read_json_strict(root / "absent.json")
            for name, body in (("malformed.json", "{"), ("partial.json", '{"metadata":')):
                path = root / name
                path.write_text(body, encoding="utf-8")
                with self.assertRaisesRegex(ContractError, "malformed JSON"):
                    read_json_strict(path)

    def test_raw_snapshot_requires_usage_judge_and_uncertainty(self):
        valid = {
            "metadata": {
                "run_id": "baseline-2026-09-01",
                "generated_at": "2026-09-01T00:00:00Z",
                "repetitions": 1,
                "baseline_model_id": "model-a",
                "candidate_model_id": "model-a",
                "judge_model_id": "deterministic-v1",
            },
            "records": [{
                "case_id": "NEG-POLARITY",
                "family": "polarity",
                "prompt": "prompt",
                "arm": "baseline",
                "model_id": "model-a",
                "system_prompt": "prompt bytes",
                "raw_output": "output bytes",
                "usage": {"input_tokens": 10, "output_tokens": 2},
                "judge": {"result": "unknown", "uncertainty": "judge unavailable"},
            }],
        }
        validate_raw_snapshot(valid)
        for mutation in ("usage", "judge"):
            broken = copy.deepcopy(valid)
            broken["records"][0].pop(mutation)
            with self.assertRaises(ContractError):
                validate_raw_snapshot(broken)


if __name__ == "__main__":
    unittest.main()
