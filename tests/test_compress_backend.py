import os
import sys
import tempfile
import unittest
from pathlib import Path
from types import SimpleNamespace
from unittest import mock


REPO_ROOT = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(REPO_ROOT / "caveman-compress"))

from scripts import compress as compress_module  # noqa: E402


class CompressBackendTests(unittest.TestCase):
    def test_detect_provider_prefers_explicit_env(self):
        with mock.patch.dict(os.environ, {"CAVEMAN_PROVIDER": "codex"}, clear=False):
            self.assertEqual(compress_module.detect_provider(), "codex")

    def test_detect_provider_uses_codex_for_codex_home(self):
        with mock.patch.dict(
            os.environ,
            {"CODEX_HOME": "/tmp/fake-codex-home"},
            clear=True,
        ):
            with mock.patch.object(compress_module.shutil, "which", side_effect=lambda cmd: "/usr/bin/codex" if cmd == "codex" else None):
                self.assertEqual(compress_module.detect_provider(), "codex")

    def test_call_with_codex_invokes_expected_exec_command(self):
        def fake_run(cmd, input, text, capture_output, check):
            self.assertEqual(input, "prompt body")
            self.assertTrue(text)
            self.assertTrue(capture_output)
            self.assertTrue(check)
            self.assertIn("--ephemeral", cmd)
            self.assertEqual(cmd[:2], ["codex", "exec"])
            self.assertIn("--ignore-user-config", cmd)
            self.assertIn("--ignore-rules", cmd)
            self.assertIn("--disable", cmd)
            self.assertIn("codex_hooks", cmd)
            self.assertIn("--skip-git-repo-check", cmd)
            self.assertIn("--sandbox", cmd)
            self.assertIn("read-only", cmd)
            self.assertIn("--ask-for-approval", cmd)
            self.assertIn("never", cmd)
            self.assertIn("--model", cmd)
            model_index = cmd.index("--model")
            self.assertEqual(cmd[model_index + 1], "codex-mini-latest")
            output_index = cmd.index("-o")
            Path(cmd[output_index + 1]).write_text("```markdown\nshrunk text\n```")
            return SimpleNamespace(stdout="", stderr="")

        with mock.patch.object(compress_module.shutil, "which", return_value="/usr/bin/codex"):
            with mock.patch.object(compress_module.subprocess, "run", side_effect=fake_run):
                self.assertEqual(compress_module.call_with_codex("prompt body"), "shrunk text")

    def test_call_with_codex_honors_model_override(self):
        def fake_run(cmd, input, text, capture_output, check):
            model_index = cmd.index("--model")
            self.assertEqual(cmd[model_index + 1], "gpt-5.3-codex")
            output_index = cmd.index("-o")
            Path(cmd[output_index + 1]).write_text("custom model output")
            return SimpleNamespace(stdout="", stderr="")

        with mock.patch.dict(os.environ, {"CAVEMAN_MODEL": "gpt-5.3-codex"}, clear=False):
            with mock.patch.object(compress_module.shutil, "which", return_value="/usr/bin/codex"):
                with mock.patch.object(compress_module.subprocess, "run", side_effect=fake_run):
                    self.assertEqual(
                        compress_module.call_with_codex("prompt body"),
                        "custom model output",
                    )

    def test_compress_file_retries_with_targeted_fix_prompt(self):
        with tempfile.TemporaryDirectory(prefix="caveman-compress-test-") as tmp:
            filepath = Path(tmp) / "notes.md"
            filepath.write_text("# Title\n\nOriginal text\n")
            calls = []

            def fake_call_model(prompt):
                calls.append(prompt)
                return "bad output" if len(calls) == 1 else "# Title\n\nTight text\n"

            invalid = SimpleNamespace(is_valid=False, errors=["URL mismatch"], warnings=[])
            valid = SimpleNamespace(is_valid=True, errors=[], warnings=[])

            with mock.patch.object(compress_module, "call_model", side_effect=fake_call_model):
                with mock.patch.object(compress_module, "validate", side_effect=[invalid, valid]):
                    success = compress_module.compress_file(filepath)

            self.assertTrue(success)
            backup = filepath.with_name("notes.original.md")
            self.assertTrue(backup.exists())
            self.assertEqual(filepath.read_text(), "# Title\n\nTight text\n")
            self.assertEqual(calls[0], compress_module.build_compress_prompt("# Title\n\nOriginal text\n"))
            self.assertEqual(
                calls[1],
                compress_module.build_fix_prompt("# Title\n\nOriginal text\n", "bad output", ["URL mismatch"]),
            )


if __name__ == "__main__":
    unittest.main()
