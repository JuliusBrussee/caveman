"""Tests for the data-loss guards in `compress_file` (issue #237).

The compress orchestrator used to overwrite the input even when Claude
returned an empty string or a no-op echo, and used to write a backup
without verifying that the bytes survived the round-trip. These tests
pin the new defensive checks: nothing on disk changes when the compressed
output is empty or identical to the input, and a backup-write that drops
bytes is detected before the input is overwritten.
"""

import sys
import tempfile
import unittest
from pathlib import Path
from unittest import mock

REPO_ROOT = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(REPO_ROOT / "skills" / "caveman-compress"))

from scripts import compress as compress_mod  # noqa: E402


class CompressSafetyTests(unittest.TestCase):
    def _file_with(self, dirpath: Path, text: str) -> Path:
        path = dirpath / "task.md"
        path.write_text(text)
        return path

    def test_empty_input_refused(self):
        with tempfile.TemporaryDirectory() as tmp:
            path = self._file_with(Path(tmp), "")
            with mock.patch.object(compress_mod, "call_claude") as call:
                ok = compress_mod.compress_file(path)
            self.assertFalse(ok)
            call.assert_not_called()
            self.assertEqual(path.read_text(), "")
            self.assertFalse((Path(tmp) / "task.original.md").exists())

    def test_empty_compressed_output_does_not_touch_disk(self):
        with tempfile.TemporaryDirectory() as tmp:
            original = "# Heading\n\nSome long natural language paragraph that should be compressed.\n"
            path = self._file_with(Path(tmp), original)
            with mock.patch.object(compress_mod, "call_claude", return_value=""):
                ok = compress_mod.compress_file(path)
            self.assertFalse(ok)
            self.assertEqual(path.read_text(), original)
            self.assertFalse((Path(tmp) / "task.original.md").exists())

    def test_whitespace_only_compressed_output_does_not_touch_disk(self):
        with tempfile.TemporaryDirectory() as tmp:
            original = "# Heading\n\nProse that should change.\n"
            path = self._file_with(Path(tmp), original)
            with mock.patch.object(compress_mod, "call_claude", return_value="   \n  "):
                ok = compress_mod.compress_file(path)
            self.assertFalse(ok)
            self.assertEqual(path.read_text(), original)
            self.assertFalse((Path(tmp) / "task.original.md").exists())

    def test_identical_compressed_output_does_not_touch_disk(self):
        with tempfile.TemporaryDirectory() as tmp:
            original = "# Heading\n\nProse.\n"
            path = self._file_with(Path(tmp), original)
            with mock.patch.object(compress_mod, "call_claude", return_value=original):
                ok = compress_mod.compress_file(path)
            self.assertFalse(ok)
            self.assertEqual(path.read_text(), original)
            self.assertFalse((Path(tmp) / "task.original.md").exists())

    def test_real_compression_writes_backup_and_target(self):
        with tempfile.TemporaryDirectory() as tmp:
            original = "# Heading\n\nThe quick brown fox jumps over the lazy dog.\n"
            compressed = "# Heading\n\nFox jump dog.\n"
            path = self._file_with(Path(tmp), original)
            with mock.patch.object(compress_mod, "call_claude", return_value=compressed), \
                 mock.patch.object(compress_mod, "validate") as v:
                v.return_value = mock.Mock(is_valid=True, errors=[], warnings=[])
                ok = compress_mod.compress_file(path)
            self.assertTrue(ok)
            self.assertEqual(path.read_text(), compressed)
            backup = Path(tmp) / "task.original.md"
            self.assertEqual(backup.read_text(), original)

    def test_no_backup_skips_companion_file_on_success(self):
        """--no-backup must not leave `.original.md` next to the input."""
        with tempfile.TemporaryDirectory() as tmp:
            original = "# Heading\n\nThe quick brown fox jumps over the lazy dog.\n"
            compressed = "# Heading\n\nFox jump dog.\n"
            path = self._file_with(Path(tmp), original)
            with mock.patch.object(compress_mod, "call_claude", return_value=compressed), \
                 mock.patch.object(compress_mod, "validate") as v:
                v.return_value = mock.Mock(is_valid=True, errors=[], warnings=[])
                ok = compress_mod.compress_file(path, no_backup=True)
            self.assertTrue(ok)
            self.assertEqual(path.read_text(), compressed)
            self.assertFalse(
                (Path(tmp) / "task.original.md").exists(),
                "no_backup=True must NOT leave .original.md next to the input",
            )

    def test_no_backup_cleans_tempfile_when_call_claude_raises(self):
        """--no-backup must not orphan the tempfile on exception.

        Reviewer flagged: when no_backup=True, an exception during the
        Claude call / validation / write leaves the NamedTemporaryFile
        behind in /tmp. The cleanup must live in finally so it runs on
        every exit path, including unexpected exceptions.
        """
        captured: list[Path] = []
        real_named_tempfile = tempfile.NamedTemporaryFile

        def spy_named_tempfile(*args, **kwargs):
            handle = real_named_tempfile(*args, **kwargs)
            captured.append(Path(handle.name))
            return handle

        with tempfile.TemporaryDirectory() as tmp:
            original = "# Heading\n\nProse to compress.\n"
            path = self._file_with(Path(tmp), original)
            with mock.patch.object(compress_mod, "call_claude",
                                   side_effect=RuntimeError("network exploded")), \
                 mock.patch("tempfile.NamedTemporaryFile", side_effect=spy_named_tempfile):
                with self.assertRaises(RuntimeError):
                    compress_mod.compress_file(path, no_backup=True)

        self.assertEqual(len(captured), 1, "expected exactly one tempfile to be created")
        self.assertFalse(
            captured[0].exists(),
            f"tempfile {captured[0]} must be unlinked even when call_claude raises",
        )


if __name__ == "__main__":
    unittest.main()
