"""Codex provider tests: mock the process, exercise the real compression guards."""

import contextlib
import io
import os
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path
from unittest import mock

sys.path.insert(0, str(Path(__file__).resolve().parent.parent / "skills" / "caveman-compress"))
from scripts import compress  # noqa: E402


class CodexProviderTests(unittest.TestCase):
    def setUp(self):
        self.env = mock.patch.dict(os.environ, {"CAVEMAN_COMPRESS_PROVIDER": "codex"}, clear=True)
        self.env.start()
        self.addCleanup(self.env.stop)
        which = mock.patch.object(compress.shutil, "which", return_value="/bin/codex")
        self.which = which.start()
        self.addCleanup(which.stop)
        self.output_paths = []

    def respond(self, command, **kwargs):
        output_path = Path(command[command.index("--output-last-message") + 1])
        self.output_paths.append(output_path)
        output_path.write_text(self.response, encoding="utf-8")
        return subprocess.CompletedProcess(command, 0, stdout="progress, not the answer", stderr="logs")

    def assert_cleaned_up(self):
        self.assertTrue(self.output_paths)
        for path in self.output_paths:
            self.assertFalse(path.parent.exists())

    def test_dispatch_command_stdin_final_message_and_model_selection(self):
        for settings, expected_model in (
            ({}, None),
            ({"CAVEMAN_MODEL": "fallback"}, "fallback"),
            ({"CAVEMAN_MODEL": "fallback", "CAVEMAN_COMPRESS_MODEL": "chosen"}, "chosen"),
        ):
            with self.subTest(settings=settings), mock.patch.dict(os.environ, settings), \
                 mock.patch.object(compress.subprocess, "run", side_effect=self.respond) as run:
                self.response = "```markdown\nCompressed café.\n```"
                self.assertEqual(compress.configured_provider(), "codex")
                self.assertEqual(compress.call_claude("Prompt café."), "Compressed café.")
                command = [
                    "/bin/codex", "exec", "--sandbox", "read-only", "--skip-git-repo-check",
                    "--output-last-message", str(self.output_paths[-1]),
                ]
                if expected_model:
                    command.extend(["--model", expected_model])
                command.append("-")
                run.assert_called_once_with(
                    command, input="Prompt café.", text=True, capture_output=True, check=True,
                    encoding="utf-8", errors="replace", timeout=compress.CLAUDE_CALL_TIMEOUT_SECONDS,
                )
                self.assert_cleaned_up()

    def test_codex_dispatch_precedes_anthropic_key_and_normalizes_provider(self):
        with mock.patch.dict(os.environ, {
            "CAVEMAN_COMPRESS_PROVIDER": " CODEX ", "ANTHROPIC_API_KEY": "unused",
        }), mock.patch.object(compress, "call_codex_cli", return_value="result") as codex, \
             mock.patch.object(compress, "call_anthropic_sdk") as anthropic:
            self.assertEqual(compress.call_claude("prompt"), "result")
            codex.assert_called_once_with("prompt")
            anthropic.assert_not_called()

    def test_missing_binary_is_actionable_without_spawning(self):
        self.which.return_value = None
        with mock.patch.object(compress.subprocess, "run") as run:
            with self.assertRaisesRegex(RuntimeError, "codex CLI not found.*codex login"):
                compress.call_claude("prompt")
            run.assert_not_called()

    def test_process_failures_are_safe_actionable_and_do_not_fall_back(self):
        for failure, message in (
            (FileNotFoundError(), "codex CLI not found"),
            (subprocess.CalledProcessError(1, ["codex"], stderr="401 unauthorized secret"), "codex login"),
            (subprocess.CalledProcessError(2, ["codex"], output="secret", stderr="secret"), "exit code 2"),
            (subprocess.TimeoutExpired(["codex"], 300, output="secret", stderr="secret"), "timed out"),
        ):
            def fail(command, **kwargs):
                self.output_paths.append(Path(command[command.index("--output-last-message") + 1]))
                raise failure

            with self.subTest(failure=failure), \
                 mock.patch.object(compress.subprocess, "run", side_effect=fail), \
                 mock.patch.object(compress, "call_claude_cli") as claude, \
                 mock.patch.object(compress, "call_anthropic_sdk") as anthropic:
                with self.assertRaisesRegex(RuntimeError, message) as error:
                    compress.call_claude("secret prompt")
                self.assertNotIn("secret", str(error.exception))
                self.assertTrue(error.exception.__suppress_context__)
                claude.assert_not_called()
                anthropic.assert_not_called()
                self.assert_cleaned_up()

    def test_missing_empty_or_invalid_final_message_never_uses_stdout(self):
        for response in (None, b"", b" \n", b"```markdown\n\n```", b"\xff"):
            def respond(command, **kwargs):
                output_path = Path(command[command.index("--output-last-message") + 1])
                self.output_paths.append(output_path)
                if response is not None:
                    output_path.write_bytes(response)
                return subprocess.CompletedProcess(command, 0, stdout="plausible but wrong answer")

            with self.subTest(response=response), \
                 mock.patch.object(compress.subprocess, "run", side_effect=respond):
                with self.assertRaisesRegex(RuntimeError, "final response"):
                    compress.call_claude("prompt")
                self.assert_cleaned_up()

    def test_real_validation_backup_and_code_preservation(self):
        original = (
            "---\nname: Notes\n---\n# Notes\n\n"
            "You should always run `pytest` before pushing any changes.\n\n"
            "```sh\npytest  # keep this comment\n```\n"
        )

        def respond(command, **kwargs):
            masked = kwargs["input"].split("TEXT:\n", 1)[1].rstrip("\n") + "\n"
            self.response = masked.replace(
                "You should always run `pytest` before pushing any changes.", "Run `pytest` before push."
            )
            return self.respond(command, **kwargs)

        with tempfile.TemporaryDirectory() as tmp, \
             mock.patch.object(compress, "_state_base_dir", side_effect=lambda kind: Path(tmp) / kind), \
             mock.patch.object(compress.subprocess, "run", side_effect=respond), \
             contextlib.redirect_stdout(io.StringIO()):
            path = Path(tmp) / "notes.md"
            path.write_text(original, encoding="utf-8")
            self.assertTrue(compress.compress_file(path))
            self.assertEqual(path.read_text(encoding="utf-8"), original.replace(
                "You should always run `pytest` before pushing any changes.", "Run `pytest` before push."
            ))
            self.assertEqual((compress.backup_dir_for(path) / "notes.original.md").read_text(encoding="utf-8"), original)
            self.assertFalse(path.with_name("notes.md.caveman-staged").exists())
            self.assert_cleaned_up()

    def test_real_validation_repairs_or_leaves_original_untouched(self):
        original = "# Notes\n\nYou should always run `pytest` before pushing any changes.\n"
        bad = "# Notes\n\nRun `wrong` before push.\n"
        for repair in ("# Notes\n\nRun `pytest` before push.\n", bad, RuntimeError("provider failed")):
            prompts = []

            def respond(command, **kwargs):
                prompts.append(kwargs["input"])
                if len(prompts) == 2 and isinstance(repair, Exception):
                    raise repair
                self.response = bad if len(prompts) == 1 else repair
                return self.respond(command, **kwargs)

            with self.subTest(repair=repair), tempfile.TemporaryDirectory() as tmp, \
                 mock.patch.object(compress, "_state_base_dir", side_effect=lambda kind: Path(tmp) / kind), \
                 mock.patch.object(compress.subprocess, "run", side_effect=respond), \
                 contextlib.redirect_stdout(io.StringIO()):
                path = Path(tmp) / "notes.md"
                path.write_text(original, encoding="utf-8")
                if isinstance(repair, Exception):
                    with self.assertRaisesRegex(RuntimeError, "provider failed"):
                        compress.compress_file(path)
                    success = False
                else:
                    success = compress.compress_file(path)
                    self.assertEqual(success, repair != bad)
                self.assertEqual(path.read_text(encoding="utf-8"), repair.strip() if success else original)
                self.assertEqual((compress.backup_dir_for(path) / "notes.original.md").exists(), success)
                self.assertFalse(path.with_name("notes.md.caveman-staged").exists())
                self.assertEqual(len(prompts), 2)
                self.assertIn("ERRORS TO FIX:", prompts[1])
                self.assert_cleaned_up()


if __name__ == "__main__":
    unittest.main()
