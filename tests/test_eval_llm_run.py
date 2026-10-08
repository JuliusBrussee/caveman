"""evals/llm_run.py with subprocess stubbed: no claude CLI, no API calls."""

from __future__ import annotations

import importlib.util
import json
import os
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path
from unittest import mock


ROOT = Path(__file__).resolve().parents[1]
EVALS = ROOT / "evals"


def eval_env(env: dict[str, str] | None = None) -> dict[str, str]:
    """The host environment minus any CAVEMAN_EVAL_* the shell set, plus `env`."""
    clean = {k: v for k, v in os.environ.items() if not k.startswith("CAVEMAN_EVAL_")}
    clean.update(env or {})
    return clean


def load(name: str, env: dict[str, str] | None = None):
    """Import an evals module fresh, with eval env vars controlled."""
    spec = importlib.util.spec_from_file_location(name, EVALS / f"{name}.py")
    assert spec and spec.loader
    module = importlib.util.module_from_spec(spec)
    with mock.patch.dict(os.environ, eval_env(env), clear=True):
        spec.loader.exec_module(module)
    return module


snapshot_contract = load("snapshot_contract")


def usage_for(system: str | None) -> dict:
    """Deterministic per-arm usage: input grows with the system prompt."""
    return {
        "input_tokens": 10,
        "cache_creation_input_tokens": 1000 + len(system or ""),
        "cache_read_input_tokens": 0,
        "output_tokens": 50 - len(system or "") % 7,
    }


class FakeClaude:
    """Stands in for subprocess.run. `fail(cell)` returns True when that
    call should fail; a cell is (system prompt text or None, prompt).
    Successful calls answer in `claude -p --output-format json` shape;
    `is_error(cell)` makes the CLI report an error result instead."""

    def __init__(self, fail=lambda cell, attempt: False, is_error=lambda cell: False):
        self.fail = fail
        self.is_error = is_error
        self.calls: list[list[str]] = []
        self.kwargs: list[dict] = []
        self.attempts: dict = {}

    def __call__(self, cmd, **kwargs):
        if cmd[1:] == ["--version"]:
            return subprocess.CompletedProcess(cmd, 0, "9.9.9 (Claude Code)\n", "")
        self.calls.append(cmd)
        self.kwargs.append(kwargs)
        system = None
        if "--system-prompt-file" in cmd:
            system = Path(cmd[cmd.index("--system-prompt-file") + 1]).read_text(
                encoding="utf-8"
            )
        cell = (system, cmd[-1])
        attempt = self.attempts[cell] = self.attempts.get(cell, 0) + 1
        if self.fail(cell, attempt):
            raise subprocess.CalledProcessError(1, cmd, "", "quota exceeded")
        result = {
            "type": "result",
            "subtype": "success",
            "is_error": self.is_error(cell),
            "result": f"answer to {cmd[-1]}\n",
            "total_cost_usd": 0.0125,
            "usage": {**usage_for(system), "service_tier": "standard",
                      "output_tokens_details": {"thinking_tokens": 3}},
        }
        return subprocess.CompletedProcess(cmd, 0, json.dumps(result), "")


class LlmRunTests(unittest.TestCase):
    def setUp(self) -> None:
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        root = Path(self.tmp.name)
        (root / "skills" / "caveman").mkdir(parents=True)
        (root / "skills" / "caveman" / "SKILL.md").write_text("Be caveman.", encoding="utf-8")
        (root / "prompts").mkdir()
        (root / "prompts" / "en.txt").write_text("q1\nq2\n", encoding="utf-8")
        (root / "prompts" / "fr.txt").write_text("fq1\nfq2\n", encoding="utf-8")
        self.root = root

    def run_main(self, fake: FakeClaude, env: dict[str, str] | None = None):
        llm_run = load("llm_run", env)
        llm_run.SKILLS = self.root / "skills"
        llm_run.PROMPTS = self.root / "prompts" / llm_run.PROMPTS.name
        llm_run.SNAPSHOT = self.root / "snapshots" / llm_run.SNAPSHOT.name
        self.sleep = mock.Mock()
        with mock.patch.object(llm_run.subprocess, "run", fake), \
                mock.patch.object(llm_run.time, "sleep", self.sleep), \
                mock.patch.dict(os.environ, eval_env(env), clear=True):
            llm_run.main()
        return llm_run

    # Proves a clean run writes the canonical snapshot through the
    # Windows-safe system-prompt file and with session persistence off.
    def test_all_calls_succeed(self) -> None:
        fake = FakeClaude()
        llm_run = self.run_main(fake)

        data = json.loads(llm_run.SNAPSHOT.read_text(encoding="utf-8"))
        snapshot_contract.validate_snapshot(data)
        self.assertEqual(data["arms"]["caveman"], ["answer to q1", "answer to q2"])
        self.assertEqual(len(fake.calls), 6)
        for cmd in fake.calls:
            self.assertIn("--no-session-persistence", cmd)
            self.assertEqual(cmd[cmd.index("--output-format") + 1], "json")
            self.assertNotIn("--system-prompt", cmd)
        self.assertEqual(sum("--system-prompt-file" in c for c in fake.calls), 4)
        self.assertEqual(
            {(s, p) for s, p in fake.attempts if s},
            {("Answer concisely.", "q1"), ("Answer concisely.", "q2"),
             ("Answer concisely.\n\nBe caveman.", "q1"),
             ("Answer concisely.\n\nBe caveman.", "q2")},
        )
        self.assertTrue(all(k["timeout"] == llm_run.CALL_TIMEOUT for k in fake.kwargs))
        self.assertFalse(llm_run.SNAPSHOT.with_suffix(".partial.json").exists())

    # Proves Claude's own usage report is stored next to each text output,
    # in a parallel structure that leaves the arms contract unchanged.
    def test_usage_is_stored_alongside_text(self) -> None:
        llm_run = self.run_main(FakeClaude())

        data = json.loads(llm_run.SNAPSHOT.read_text(encoding="utf-8"))
        snapshot_contract.validate_snapshot(data)
        self.assertEqual(set(data["usage"]), set(data["arms"]))
        skill = "Answer concisely.\n\nBe caveman."
        self.assertEqual(
            data["usage"]["caveman"][1], {**usage_for(skill), "total_cost_usd": 0.0125}
        )
        self.assertEqual(data["usage"]["__baseline__"][0]["cache_creation_input_tokens"], 1000)

    # Proves an error result reported on exit 0 is not stored as an answer.
    def test_error_result_is_a_failure(self) -> None:
        fake = FakeClaude(is_error=lambda cell: cell == (None, "q1"))
        with self.assertRaises(SystemExit):
            self.run_main(fake)

        data = json.loads(
            (self.root / "snapshots" / "results.partial.json").read_text(encoding="utf-8")
        )
        self.assertEqual(data["arms"]["__baseline__"], [None, None])
        self.assertEqual(data["usage"]["__baseline__"], [None, None])
        self.assertIn("__baseline__ prompt 0", data["metadata"]["error"])

    # Proves one transient failure is retried instead of discarding the run.
    def test_transient_failure_is_retried(self) -> None:
        fake = FakeClaude(lambda cell, attempt: cell == ("Answer concisely.", "q2") and attempt == 1)
        llm_run = self.run_main(fake)

        data = json.loads(llm_run.SNAPSHOT.read_text(encoding="utf-8"))
        snapshot_contract.validate_snapshot(data)
        self.assertEqual(data["arms"]["__terse__"], ["answer to q1", "answer to q2"])
        self.sleep.assert_called_once_with(5)

    # Proves a hard failure stops the run, keeps completed cells in a
    # partial file the contract rejects, and leaves results.json alone.
    def test_hard_failure_writes_partial_and_exits(self) -> None:
        fake = FakeClaude(lambda cell, attempt: cell == ("Answer concisely.", "q2"))
        with self.assertRaises(SystemExit) as raised:
            self.run_main(fake)
        self.assertEqual(raised.exception.code, 1)

        canonical = self.root / "snapshots" / "results.json"
        partial = self.root / "snapshots" / "results.partial.json"
        self.assertFalse(canonical.exists())
        data = json.loads(partial.read_text(encoding="utf-8"))
        self.assertEqual(data["arms"]["__baseline__"], ["answer to q1", "answer to q2"])
        self.assertEqual(data["arms"]["__terse__"], ["answer to q1", None])
        self.assertEqual(data["arms"]["caveman"], [None, None])
        self.assertIn("__terse__ prompt 1", data["metadata"]["error"])
        self.assertIn("quota exceeded", data["metadata"]["error"])
        # 2 baseline + 1 terse + 3 attempts at the failing cell, then stop.
        self.assertEqual(len(fake.calls), 6)
        self.assertEqual([c.args for c in self.sleep.call_args_list], [(5,), (20,)])
        with self.assertRaises(snapshot_contract.SnapshotContractError):
            snapshot_contract.validate_snapshot(data)

    # Proves a translated language uses its own control and snapshot path.
    def test_language_selects_control_and_snapshot(self) -> None:
        fake = FakeClaude()
        llm_run = self.run_main(fake, {"CAVEMAN_EVAL_LANG": "fr"})

        self.assertEqual(llm_run.SNAPSHOT.name, "results.fr.json")
        data = json.loads(llm_run.SNAPSHOT.read_text(encoding="utf-8"))
        self.assertEqual(data["metadata"]["lang"], "fr")
        self.assertEqual(data["metadata"]["terse_prefix"], "Réponds de façon concise.")
        self.assertIn(("Réponds de façon concise.", "fq1"), fake.attempts)

    # Proves the fidelity set reuses the arm loop on the case prompts and
    # writes its own snapshot, never results.json.
    def test_fidelity_set_runs_case_prompts(self) -> None:
        cases = {"cases": [
            {"id": "a", "category": "exact-preservation", "prompt": "port?",
             "checks": {"must_include": ["5432"]}},
            {"id": "b", "category": "safety-clarity", "prompt": "drop it",
             "checks": {"must_not_include": ["DROP"]}},
        ]}
        (self.root / "prompts" / "fidelity.json").write_text(json.dumps(cases), encoding="utf-8")
        fake = FakeClaude()
        llm_run = self.run_main(fake, {"CAVEMAN_EVAL_SET": "fidelity"})

        self.assertEqual(llm_run.SNAPSHOT.name, "fidelity.json")
        self.assertFalse((self.root / "snapshots" / "results.json").exists())
        data = json.loads(llm_run.SNAPSHOT.read_text(encoding="utf-8"))
        snapshot_contract.validate_snapshot(data)
        self.assertEqual(data["prompts"], ["port?", "drop it"])
        self.assertEqual(data["metadata"]["eval_set"], "fidelity")
        self.assertEqual(data["arms"]["caveman"], ["answer to port?", "answer to drop it"])

    # Proves an unknown eval set, or fidelity with a non-English control,
    # stops before any call.
    def test_bad_eval_set_exits_before_any_call(self) -> None:
        for env, message in (({"CAVEMAN_EVAL_SET": "nope"}, "CAVEMAN_EVAL_SET=nope"),
                             ({"CAVEMAN_EVAL_SET": "fidelity", "CAVEMAN_EVAL_LANG": "fr"},
                              "English")):
            fake = FakeClaude()
            with self.subTest(env=env), self.assertRaisesRegex(SystemExit, message):
                self.run_main(fake, env)
            self.assertEqual(fake.calls, [])

    # Proves reminder_run.py imports and stays on the English length prompts
    # and control whatever CAVEMAN_EVAL_SET / CAVEMAN_EVAL_LANG the shell
    # left exported: it writes one reminder.json, and a leftover
    # CAVEMAN_EVAL_SET=fidelity must not feed it fidelity.json line by line.
    def test_reminder_run_ignores_eval_set_and_lang(self) -> None:
        for env in ({"CAVEMAN_EVAL_SET": "fidelity"}, {"CAVEMAN_EVAL_LANG": "fr"},
                    {"CAVEMAN_EVAL_LANG": "de"}):
            with self.subTest(env=env), mock.patch.object(sys, "path", list(sys.path)), \
                    mock.patch.dict(sys.modules):
                reminder_run = load("reminder_run", env)
                self.assertEqual(reminder_run.TERSE_PREFIX, "Answer concisely.")
                self.assertEqual(reminder_run.PROMPTS, EVALS / "prompts" / "en.txt")

    # Proves an untranslated language fails closed before any claude call.
    def test_unknown_language_exits_before_any_call(self) -> None:
        (self.root / "prompts" / "zz.txt").write_text("q\n", encoding="utf-8")
        fake = FakeClaude()
        with self.assertRaisesRegex(SystemExit, "no terse control"):
            self.run_main(fake, {"CAVEMAN_EVAL_LANG": "zz"})
        self.assertEqual(fake.calls, [])

        with self.assertRaisesRegex(SystemExit, "available: en, fr, pt"):
            load("llm_run", {"CAVEMAN_EVAL_LANG": "de"}).main()


if __name__ == "__main__":
    unittest.main()
