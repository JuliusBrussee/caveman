import json
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

from evals.caveman_contract import ContractError, ROOT, load_manifest, validate_raw_snapshot
from evals.caveman_semantic_eval import (
    build_run,
    deterministic_judge,
    render_report,
    structural_snapshot,
    write_new,
)


class CavemanSemanticEvalTests(unittest.TestCase):
    def test_structured_eval_corpus_matches_manifest_cases(self):
        manifest = load_manifest()
        corpus = json.loads(
            (ROOT / "evals/prompts/caveman-semantic-cases.json").read_text(encoding="utf-8")
        )
        expected = manifest["model_cases"] + [
            {**case, "family": "positive"} for case in manifest["positive_cases"]
        ]
        self.assertEqual(corpus["cases"], expected)

    def test_structural_snapshot_labels_proxy_as_not_tokenizer_output(self):
        snapshot = structural_snapshot()
        self.assertIn("not tokenizer output", snapshot["metric_policy"]["bytes_div_4_proxy"])
        self.assertIn("canonical_skill_body", snapshot["surfaces"])
        for measurement in snapshot["surfaces"].values():
            self.assertGreater(measurement["utf8_bytes"], 0)
            self.assertEqual(
                measurement["bytes_div_4_proxy"],
                (measurement["utf8_bytes"] + 3) // 4,
            )

    def test_snapshot_writer_refuses_overwrite(self):
        with tempfile.TemporaryDirectory(prefix="caveman-immutable-") as raw_tmp:
            path = Path(raw_tmp) / "snapshot.json"
            write_new(path, {"first": True})
            with self.assertRaises(FileExistsError):
                write_new(path, {"second": True})
            self.assertEqual(json.loads(path.read_text(encoding="utf-8")), {"first": True})

    def test_deterministic_judge_fails_missing_polarity_and_preserved_artifact(self):
        polarity = {"family": "polarity"}
        self.assertEqual(deterministic_judge(polarity, "Run 3 times after 250 ms for HTTP 429.")["result"], "fail")
        exact = {"family": "exact-artifact-preservation"}
        self.assertEqual(deterministic_judge(exact, "refreshToken failed.")["result"], "fail")
        self.assertEqual(
            deterministic_judge(
                exact,
                '`refreshToken` called `POST /v2/token` after 250 ms and returned "ECONNRESET".',
            )["result"],
            "pass",
        )
        self.assertEqual(
            deterministic_judge({"id": "POS-COMPRESSION"}, "Short answer.")["result"],
            "unknown",
        )

    def test_report_preserves_unknown_instead_of_forcing_pass(self):
        snapshot = {
            "metadata": {
                "run_id": "test",
                "generated_at": "2026-09-01T00:00:00Z",
                "repetitions": 1,
                "baseline_model_id": "model-a",
                "candidate_model_id": "not-run",
                "judge_model_id": "deterministic-v1",
            },
            "records": [{
                "case_id": "NEG-SAFETY", "family": "safety", "prompt": "prompt",
                "arm": "baseline", "model_id": "model-a", "system_prompt": "system",
                "raw_output": "output", "usage": {"input_tokens": 1, "output_tokens": 1},
                "judge": {"result": "unknown", "uncertainty": "review required"},
            }],
        }
        report = render_report(snapshot)
        self.assertIn("unknown=1", report)
        self.assertIn("it is not a pass", report)

    def test_partial_snapshot_is_an_error_not_absent_evidence(self):
        with self.assertRaises(ContractError):
            validate_raw_snapshot({"metadata": {}, "records": []})

    def test_paired_run_uses_identical_case_prompts_and_records_both_arms(self):
        fake_envelope = {
            "result": "output",
            "usage": {"input_tokens": 2, "output_tokens": 1},
            "modelUsage": {"claude-haiku-4-5-20251001": {}},
        }
        fake_response = {
            "output": "output",
            "usage": fake_envelope["usage"],
            "provider_envelope": fake_envelope,
        }
        args = type("Args", (), {
            "run_id": "paired-test",
            "baseline_skill": ROOT / "skills/caveman/SKILL.md",
            "candidate_skill": ROOT / "skills/caveman/SKILL.md",
            "baseline_model": "model-a",
            "candidate_model": "model-a",
            "repetitions": 1,
        })()
        with patch("evals.caveman_semantic_eval.run_claude", return_value=fake_response):
            snapshot = build_run(args)
        self.assertTrue(snapshot["metadata"]["paired"])
        self.assertEqual(snapshot["metadata"]["provider_model_ids"], ["claude-haiku-4-5-20251001"])
        by_case = {}
        for record in snapshot["records"]:
            by_case.setdefault(record["case_id"], []).append(record)
        for records in by_case.values():
            self.assertEqual({record["arm"] for record in records}, {"baseline", "candidate"})
            self.assertEqual(len({record["prompt"] for record in records}), 1)

        broken = json.loads(json.dumps(snapshot))
        broken["records"][1]["prompt"] += " changed"
        with self.assertRaisesRegex(ContractError, "changed user prompt"):
            validate_raw_snapshot(broken)


if __name__ == "__main__":
    unittest.main()
