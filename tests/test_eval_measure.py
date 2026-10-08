"""evals/measure.py's Claude-usage table, with tiktoken stubbed so the test
runs without it (CI does not install tiktoken)."""

from __future__ import annotations

import contextlib
import importlib.util
import io
import json
import sys
import tempfile
import types
import unittest
from pathlib import Path
from unittest import mock


EVALS = Path(__file__).resolve().parents[1] / "evals"


def load_measure():
    fake = types.ModuleType("tiktoken")
    fake.get_encoding = lambda name: types.SimpleNamespace(encode=str.split)
    spec = importlib.util.spec_from_file_location("measure", EVALS / "measure.py")
    assert spec and spec.loader
    module = importlib.util.module_from_spec(spec)
    with mock.patch.dict(sys.modules, {"tiktoken": fake}), \
            mock.patch.object(sys, "path", [str(EVALS), *sys.path]):
        spec.loader.exec_module(module)
    return module


def usage(inp: int, out: int) -> dict:
    return {
        "input_tokens": 4,
        "cache_creation_input_tokens": inp - 4,
        "cache_read_input_tokens": 0,
        "output_tokens": out,
        "total_cost_usd": 0.01,
    }


SNAPSHOT = {
    "metadata": {
        "generated_at": "2026-10-05T00:00:00+00:00",
        "claude_cli_version": "9.9.9 (Claude Code)",
        "model": "test-model",
        "n_prompts": 2,
        "lang": "en",
        "terse_prefix": "Answer concisely.",
    },
    "prompts": ["q1", "q2"],
    "arms": {
        "__baseline__": ["one two three four", "one two three four"],
        "__terse__": ["one two", "one two"],
        "caveman": ["one", "one"],
    },
}


class MeasureUsageTests(unittest.TestCase):
    def run_measure(self, snapshot: dict) -> str:
        measure = load_measure()
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "results.json"
            path.write_text(json.dumps(snapshot), encoding="utf-8")
            out = io.StringIO()
            with mock.patch.object(sys, "argv", ["measure.py", str(path)]), \
                    contextlib.redirect_stdout(out):
                measure.main()
        return out.getvalue()

    # Proves the second table reports billed output and the input the
    # skill adds, both against the terse control, from Claude's own usage.
    def test_usage_table_reports_billed_output_and_input_added(self) -> None:
        snapshot = dict(SNAPSHOT)
        snapshot["usage"] = {
            "__baseline__": [usage(20000, 400), usage(20000, 500)],
            "__terse__": [usage(17000, 100), usage(17000, 200)],
            "caveman": [usage(18000, 50), usage(18200, 100)],
        }
        text = self.run_measure(snapshot)

        self.assertIn("| **caveman** | +50% | +50% | 150 / 300 | +1100 |", text)
        self.assertIn("thinking", text)
        # The tiktoken table is still printed above it.
        self.assertIn("| **caveman** | +50% | +50% |", text.split("Claude-reported")[0])

    # Proves a snapshot without usage (the committed one) prints no usage table.
    def test_no_usage_table_without_usage(self) -> None:
        text = self.run_measure(SNAPSHOT)
        self.assertNotIn("Claude-reported", text)


if __name__ == "__main__":
    unittest.main()
