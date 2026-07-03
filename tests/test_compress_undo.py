"""Tests for `python -m scripts --undo <file>` (compress backup restore).

Undo must restore the pre-compression bytes exactly, remove the backup so a
future compression is not blocked by the backup-exists guard, support the
legacy in-tree sibling backup, and fail loudly (file untouched) when no
backup exists.
"""

import os
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parent.parent
PKG_DIR = REPO_ROOT / "skills" / "caveman-compress"
sys.path.insert(0, str(PKG_DIR))

from scripts import compress as compress_mod  # noqa: E402


def run_undo(target: Path, data_home: Path):
    env = os.environ.copy()
    env["XDG_DATA_HOME"] = str(data_home)
    env["LOCALAPPDATA"] = str(data_home)
    return subprocess.run(
        [sys.executable, "-m", "scripts", "--undo", str(target)],
        cwd=PKG_DIR,
        env=env,
        text=True,
        capture_output=True,
    )


class CompressUndoTests(unittest.TestCase):
    def test_undo_restores_out_of_tree_backup_and_removes_it(self):
        with tempfile.TemporaryDirectory() as tmp, \
             tempfile.TemporaryDirectory() as data_home:
            original = "# Doc\n\nOriginal prose with unicode → intact.\n"
            path = Path(tmp) / "task.md"
            path.write_text("# Doc\n\nCompressed.\n", encoding="utf-8")
            env_backup = {"XDG_DATA_HOME": data_home, "LOCALAPPDATA": data_home}
            os.environ.update(env_backup)
            try:
                backup_dir = compress_mod.backup_dir_for(path.resolve())
            finally:
                for k in env_backup:
                    os.environ.pop(k, None)
            backup_dir.mkdir(parents=True, exist_ok=True)
            backup = backup_dir / "task.original.md"
            backup.write_text(original, encoding="utf-8")

            r = run_undo(path, Path(data_home))

            self.assertEqual(r.returncode, 0, r.stdout + r.stderr)
            self.assertEqual(path.read_text(encoding="utf-8"), original)
            self.assertFalse(backup.exists(), "backup must be removed after restore")

    def test_undo_falls_back_to_legacy_sibling_backup(self):
        with tempfile.TemporaryDirectory() as tmp, \
             tempfile.TemporaryDirectory() as data_home:
            original = "# Doc\n\nLegacy layout original.\n"
            path = Path(tmp) / "task.md"
            path.write_text("compressed", encoding="utf-8")
            sibling = Path(tmp) / "task.original.md"
            sibling.write_text(original, encoding="utf-8")

            r = run_undo(path, Path(data_home))

            self.assertEqual(r.returncode, 0, r.stdout + r.stderr)
            self.assertEqual(path.read_text(encoding="utf-8"), original)
            self.assertFalse(sibling.exists())

    def test_undo_without_backup_fails_and_leaves_file_alone(self):
        with tempfile.TemporaryDirectory() as tmp, \
             tempfile.TemporaryDirectory() as data_home:
            path = Path(tmp) / "task.md"
            path.write_text("current content", encoding="utf-8")

            r = run_undo(path, Path(data_home))

            self.assertEqual(r.returncode, 1)
            self.assertIn("No backup found", r.stdout)
            self.assertEqual(path.read_text(encoding="utf-8"), "current content")

    def test_undo_missing_file_errors(self):
        with tempfile.TemporaryDirectory() as tmp, \
             tempfile.TemporaryDirectory() as data_home:
            r = run_undo(Path(tmp) / "absent.md", Path(data_home))
            self.assertEqual(r.returncode, 1)


if __name__ == "__main__":
    unittest.main()
