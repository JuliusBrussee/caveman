from __future__ import annotations

import importlib.util
import unittest
from pathlib import Path
from unittest import mock


ROOT = Path(__file__).resolve().parents[1]
MODULE_PATH = ROOT / "evals" / "llm_run.py"
SPEC = importlib.util.spec_from_file_location("eval_llm_run", MODULE_PATH)
assert SPEC and SPEC.loader
llm_run = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(llm_run)


class EvalClaudeIsolationTests(unittest.TestCase):
    # Proves every generation excludes inherited settings and MCP configuration.
    def test_run_uses_isolated_command_with_exact_inputs(self) -> None:
        with (
            mock.patch.object(llm_run, "claude_bin", return_value="claude-test"),
            mock.patch.object(llm_run.subprocess, "run") as run,
            mock.patch.dict(
                llm_run.os.environ,
                {"CAVEMAN_EVAL_MODEL": "claude-test-model"},
                clear=False,
            ),
        ):
            run.return_value = mock.Mock(stdout="  response\n")

            result = llm_run.run_claude(
                "user prompt --setting-sources project", system="exact system prompt"
            )

        self.assertEqual(result, "response")
        self.assertEqual(
            run.call_args.args[0],
            [
                "claude-test",
                "-p",
                "--setting-sources",
                "",
                "--strict-mcp-config",
                "--system-prompt",
                "exact system prompt",
                "--model",
                "claude-test-model",
                "user prompt --setting-sources project",
            ],
        )
        self.assertEqual(
            run.call_args.kwargs,
            {
                "capture_output": True,
                "text": True,
                "check": True,
                "encoding": "utf-8",
                "errors": "replace",
            },
        )

    # Proves a baseline arm remains isolated without inventing a system prompt.
    def test_baseline_command_has_no_system_prompt(self) -> None:
        with mock.patch.object(llm_run, "claude_bin", return_value="claude-test"):
            command = llm_run.build_claude_command("exact prompt")

        self.assertEqual(
            command,
            [
                "claude-test",
                "-p",
                "--setting-sources",
                "",
                "--strict-mcp-config",
                "exact prompt",
            ],
        )
        self.assertNotIn("--system-prompt", command)


if __name__ == "__main__":
    unittest.main()
