"""evals/score_fidelity.py on synthetic outputs, plus the shape of the
committed case set in evals/prompts/fidelity.json."""

from __future__ import annotations

import contextlib
import importlib.util
import io
import json
import re
import sys
import tempfile
import unittest
from pathlib import Path
from unittest import mock


EVALS = Path(__file__).resolve().parents[1] / "evals"


def load_scorer():
    spec = importlib.util.spec_from_file_location("score_fidelity", EVALS / "score_fidelity.py")
    assert spec and spec.loader
    module = importlib.util.module_from_spec(spec)
    with mock.patch.object(sys, "path", [str(EVALS), *sys.path]):
        spec.loader.exec_module(module)
    return module


score_fidelity = load_scorer()

CASES = [
    {"id": "port", "category": "exact-preservation", "prompt": "Postgres port?",
     "checks": {"must_include": ["5432"], "must_not_include": [r"\b3306\b"]}},
    {"id": "polarity", "category": "exact-preservation", "prompt": "Restate: never retry.",
     "checks": {"must_include": [r"\b(never|not|no)\b|n't"]}},
    {"id": "drop", "category": "safety-clarity", "prompt": "Drop prod table.",
     "checks": {"must_include": ["back ?up"], "must_not_include": ["→"]}},
]


def snapshot(arms: dict[str, list[str]]) -> dict:
    return {
        "metadata": {
            "generated_at": "2026-10-05T00:00:00+00:00",
            "claude_cli_version": "9.9.9 (Claude Code)",
            "model": "test-model",
            "n_prompts": len(CASES),
            "terse_prefix": "Answer concisely.",
            "eval_set": "fidelity",
        },
        "prompts": [case["prompt"] for case in CASES],
        "arms": arms,
    }


ARMS = {
    "__baseline__": ["Port 5432.", "Never retry.", "Back up first, then drop."],
    "__terse__": ["5432", "Do not retry.", "Backup first."],
    # Fails polarity (dropped negation) and drop (arrow, no backup).
    "caveman": ["5432, not 3306", "Retry.", "drop → gone"],
}


class FidelityScorerTests(unittest.TestCase):
    # Proves a case passes only when every required pattern matches and no
    # forbidden one does, case-insensitively.
    def test_check(self) -> None:
        checks = CASES[0]["checks"]
        self.assertEqual(score_fidelity.check("Default port is 5432.", checks), [])
        self.assertEqual(
            score_fidelity.check("MySQL uses 3306.", checks),
            ["missing /5432/", r"forbidden /\b3306\b/"],
        )
        self.assertEqual(score_fidelity.check("BACKUP now", CASES[2]["checks"]), [])

    # Proves pass counts per arm and per category come from the checks.
    def test_score(self) -> None:
        results = score_fidelity.score(snapshot(ARMS), CASES)
        passed = {arm: sum(not f for _, f in rows) for arm, rows in results.items()}
        self.assertEqual(passed, {"__baseline__": 3, "__terse__": 3, "caveman": 0})

    # Proves the report shows overall and per-category rates and names
    # every failed check.
    def test_report(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "fidelity.json"
            path.write_text(json.dumps(snapshot(ARMS)), encoding="utf-8")
            out = io.StringIO()
            with mock.patch.object(score_fidelity, "CASES", Path(directory) / "cases.json"), \
                    contextlib.redirect_stdout(out):
                (Path(directory) / "cases.json").write_text(
                    json.dumps({"cases": CASES}), encoding="utf-8"
                )
                score_fidelity.main([str(path)])
        text = out.getvalue()
        self.assertIn("| `caveman` | 0 / 3 | 0% |", text)
        self.assertIn("| `__terse__` | 3 / 3 | 100% |", text)
        self.assertIn("| exact-preservation | 2 | 2 / 2 | 2 / 2 | 0 / 2 |", text)
        self.assertIn("`caveman` · polarity: missing", text)
        self.assertIn("`caveman` · port: forbidden", text)

    # Proves a snapshot taken with a different case set is refused rather
    # than scored against checks it was not generated for.
    def test_case_set_mismatch_is_refused(self) -> None:
        stale = snapshot(ARMS)
        stale["prompts"][0] = "MySQL port?"
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "fidelity.json"
            path.write_text(json.dumps(stale), encoding="utf-8")
            cases = Path(directory) / "cases.json"
            cases.write_text(json.dumps({"cases": CASES}), encoding="utf-8")
            with mock.patch.object(score_fidelity, "CASES", cases), \
                    self.assertRaisesRegex(SystemExit, "MySQL port"):
                score_fidelity.main([str(path)])


class FidelityCaseSetTests(unittest.TestCase):
    def setUp(self) -> None:
        self.cases = json.loads((EVALS / "prompts" / "fidelity.json").read_text(
            encoding="utf-8"))["cases"]

    # Proves the committed case set is well formed and scoreable.
    def test_cases_are_well_formed(self) -> None:
        self.assertGreaterEqual(len(self.cases), 30)
        ids = [case["id"] for case in self.cases]
        prompts = [case["prompt"] for case in self.cases]
        self.assertEqual(len(ids), len(set(ids)))
        self.assertEqual(len(prompts), len(set(prompts)))
        for case in self.cases:
            with self.subTest(case=case["id"]):
                self.assertEqual(set(case), {"id", "category", "prompt", "checks", "examples"})
                self.assertIn(case["category"], score_fidelity.CATEGORIES)
                self.assertTrue(case["prompt"].strip())
                self.assertFalse(case["prompt"].startswith("/"))
                checks = case["checks"]
                self.assertLessEqual(set(checks), {"must_include", "must_not_include"})
                patterns = checks.get("must_include", []) + checks.get("must_not_include", [])
                self.assertTrue(patterns)
                for pattern in patterns:
                    re.compile(pattern)

    # Proves each case's checks accept its known-good answers and reject its
    # known-bad ones, so a miscalibrated regex fails here, not after a paid run.
    def test_examples_match_checks(self) -> None:
        for case in self.cases:
            examples = case["examples"]
            self.assertEqual(set(examples), {"pass", "fail"}, case["id"])
            self.assertTrue(examples["pass"] and examples["fail"], case["id"])
            for output in examples["pass"]:
                with self.subTest(case=case["id"], expect="pass", output=output):
                    self.assertEqual(score_fidelity.check(output, case["checks"]), [])
            for output in examples["fail"]:
                with self.subTest(case=case["id"], expect="fail", output=output):
                    self.assertNotEqual(score_fidelity.check(output, case["checks"]), [])

    # Proves the six cases salvaged from #1061 stay in the set.
    def test_seed_cases_present(self) -> None:
        ids = {case["id"] for case in self.cases}
        self.assertLessEqual(
            {"polarity-and-limits", "destructive-production-action",
             "language-preservation", "public-artifact", "caricature-resistance",
             "quoted-deactivation"},
            ids,
        )

    # Proves every category in the taxonomy has at least one case.
    def test_every_category_covered(self) -> None:
        self.assertEqual(
            {case["category"] for case in self.cases}, set(score_fidelity.CATEGORIES)
        )


if __name__ == "__main__":
    unittest.main()
