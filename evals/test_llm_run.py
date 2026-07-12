"""Tests for evals/llm_run.py fault tolerance (no `claude` CLI required)."""
import json
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path
from unittest import mock

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE))
import llm_run  # noqa: E402


def _tmp_env():
    root = Path(tempfile.mkdtemp(prefix="caveman-eval-"))
    (root / "skills" / "demo").mkdir(parents=True)
    (root / "skills" / "demo" / "SKILL.md").write_text("Be terse.")
    (root / "prompts").mkdir(parents=True)
    (root / "prompts" / "en.txt").write_text("Q1\nQ2\n")
    return root


class EvalRunTest(unittest.TestCase):
    def test_success_writes_canonical_snapshot(self):
        env = _tmp_env()
        llm_run.SKILLS = env / "skills"
        llm_run.PROMPTS = env / "prompts" / "en.txt"
        llm_run.SNAPSHOT = env / "evals" / "snapshots" / "results.json"
        ok = subprocess.CompletedProcess("claude", 0, "out", "")
        with mock.patch("subprocess.run", return_value=ok):
            llm_run.main()
        self.assertTrue(llm_run.SNAPSHOT.exists())
        data = json.loads(llm_run.SNAPSHOT.read_text())
        self.assertIn("__baseline__", data["arms"])
        self.assertIn("__terse__", data["arms"])
        self.assertIn("demo", data["arms"])
        self.assertNotIn("__ERROR__", str(data["arms"]["__baseline__"]))

    def test_midrun_failure_writes_partial_no_crash(self):
        env = _tmp_env()
        llm_run.SKILLS = env / "skills"
        llm_run.PROMPTS = env / "prompts" / "en.txt"
        llm_run.SNAPSHOT = env / "evals" / "snapshots" / "results.json"
        calls = {"n": 0}

        def fake(*a, **k):
            calls["n"] += 1
            if calls["n"] >= 2:  # transient claude failure mid-run
                raise subprocess.CalledProcessError(1, "claude")
            return subprocess.CompletedProcess("claude", 0, "out", "")

        with mock.patch("subprocess.run", side_effect=fake):
            with self.assertRaises(SystemExit) as cm:
                llm_run.main()
            self.assertEqual(cm.exception.code, 1)
        # partial progress is saved ...
        partial = llm_run.SNAPSHOT.with_name("results.partial.json")
        self.assertTrue(partial.exists(), "partial snapshot must be saved on failure")
        data = json.loads(partial.read_text())
        # ... every arm was still attempted (no early abort) ...
        self.assertEqual(len(data["arms"]["__baseline__"]), 2)
        self.assertTrue(any("__ERROR__" in str(x) for x in data["arms"]["__baseline__"]))
        # ... and the canonical snapshot is left untouched.
        self.assertFalse(llm_run.SNAPSHOT.exists())


if __name__ == "__main__":
    unittest.main()
