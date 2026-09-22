from __future__ import annotations

import copy
import importlib.util
import json
import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
MODULE_PATH = ROOT / "evals" / "annotate_taxonomy.py"
SPEC = importlib.util.spec_from_file_location("annotate_taxonomy", MODULE_PATH)
assert SPEC and SPEC.loader
annotate_taxonomy = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(annotate_taxonomy)


class EvalTaxonomyTests(unittest.TestCase):
    def setUp(self) -> None:
        self.taxonomy = json.loads(
            (ROOT / "evals" / "semantic_taxonomy.json").read_text(encoding="utf-8")
        )
        self.cases = json.loads(
            (ROOT / "evals" / "prompts" / "semantic_cases.json").read_text(
                encoding="utf-8"
            )
        )

    # Proves readable case keys resolve to stable IDs without changing prompts.
    def test_annotation_resolves_ids_and_preserves_prompts(self) -> None:
        index = annotate_taxonomy.taxonomy_index(self.taxonomy)
        before = [case["prompt"] for case in self.cases["cases"]]

        result = annotate_taxonomy.annotate_cases(self.cases, index)

        self.assertEqual(before, [case["prompt"] for case in result["cases"]])
        self.assertEqual(
            ["CAV-SEM-04", "CAV-SEM-02"], result["cases"][2]["contract_ids"]
        )

    # Proves duplicate stable IDs make the taxonomy fail closed.
    def test_duplicate_taxonomy_id_is_rejected(self) -> None:
        duplicate = copy.deepcopy(self.taxonomy)
        duplicate["taxonomy"][1]["id"] = duplicate["taxonomy"][0]["id"]

        with self.assertRaisesRegex(
            annotate_taxonomy.TaxonomyError, "duplicate taxonomy ID"
        ):
            annotate_taxonomy.taxonomy_index(duplicate)

    # Proves misspelled or retired taxonomy keys cannot silently lose coverage.
    def test_unknown_case_key_is_rejected(self) -> None:
        cases = copy.deepcopy(self.cases)
        cases["cases"][0]["taxonomy"] = ["exact-preservtion"]

        with self.assertRaisesRegex(
            annotate_taxonomy.TaxonomyError, "unknown taxonomy keys"
        ):
            annotate_taxonomy.annotate_cases(
                cases, annotate_taxonomy.taxonomy_index(self.taxonomy)
            )

    # Proves source cases cannot bypass readable keys by embedding stable IDs.
    def test_embedded_contract_ids_are_rejected(self) -> None:
        cases = copy.deepcopy(self.cases)
        cases["cases"][0]["contract_ids"] = ["CAV-SEM-02"]

        with self.assertRaisesRegex(
            annotate_taxonomy.TaxonomyError, "not embedded contract IDs"
        ):
            annotate_taxonomy.annotate_cases(
                cases, annotate_taxonomy.taxonomy_index(self.taxonomy)
            )

    # Proves eval traceability IDs do not consume runtime skill context.
    def test_runtime_skill_contains_no_taxonomy_ids(self) -> None:
        skill = (ROOT / "skills" / "caveman" / "SKILL.md").read_text(
            encoding="utf-8"
        )

        self.assertNotIn("CAV-SEM-", skill)


if __name__ == "__main__":
    unittest.main()
