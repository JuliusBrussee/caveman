"""Exercise the installed CLI from another directory without invoking a model."""

import os
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

SKILL = Path(__file__).resolve().parent.parent / "skills" / "caveman-compress"
CLI = SKILL / "scripts" / "cli.py"


class CompressCLITests(unittest.TestCase):
    def run_cli(self, args, cwd, module=False):
        env = dict(os.environ, PYTHONIOENCODING="utf-8")
        if module:
            env["PYTHONPATH"] = str(SKILL)
        else:
            env.pop("PYTHONPATH", None)
        entry = ["-m", "scripts.cli"] if module else [str(CLI)]
        return subprocess.run([sys.executable, *entry, *args], cwd=cwd, env=env,
                              capture_output=True, text=True, encoding="utf-8", timeout=20)

    def test_help_and_argument_errors_for_direct_and_module_entrypoints(self):
        with tempfile.TemporaryDirectory(prefix="cave cli spaced ") as cwd:
            for module in (False, True):
                result = self.run_cli(["--help"], cwd, module)
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertIn("filepath", result.stdout)
                result = self.run_cli([], cwd, module)
                self.assertEqual(result.returncode, 2, result.stderr)
                self.assertNotIn("Traceback", result.stderr)

    def test_relative_code_path_is_skipped_without_writing_or_spawning(self):
        with tempfile.TemporaryDirectory(prefix="cave cli spaced ") as cwd:
            source = Path(cwd) / "a code file.py"
            data = b"print('untouched')\r\n"
            source.write_bytes(data)
            result = self.run_cli([source.name], cwd)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertIn("Skipping: file is not natural language", result.stdout)
            self.assertEqual(source.read_bytes(), data)
            self.assertEqual(list(Path(cwd).iterdir()), [source])

    def test_missing_unicode_file_reports_error_without_import_or_encoding_failure(self):
        with tempfile.TemporaryDirectory() as cwd:
            result = self.run_cli(["不存在.md"], cwd)
            self.assertEqual(result.returncode, 1)
            self.assertIn("File not found", result.stdout)
            self.assertNotIn("Traceback", result.stderr)


if __name__ == "__main__":
    unittest.main()
