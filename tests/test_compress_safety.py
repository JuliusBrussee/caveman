"""Tests for the data-loss guards in `compress_file` (issue #237).

The compress orchestrator used to overwrite the input even when Claude
returned an empty string or a no-op echo, and used to write a backup
without verifying that the bytes survived the round-trip. These tests
pin the new defensive checks: nothing on disk changes when the compressed
output is empty or identical to the input, and a backup-write that drops
bytes is detected before the input is overwritten.
"""

import os
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
        # Isolate the backup data dir to a temp location so the out-of-tree
        # backup (issue #420) never lands in the developer's real home dir.
        with tempfile.TemporaryDirectory() as tmp, \
             tempfile.TemporaryDirectory() as data_home, \
             mock.patch.dict(os.environ, {"XDG_DATA_HOME": data_home, "LOCALAPPDATA": data_home}):
            original = "# Heading\n\nThe quick brown fox jumps over the lazy dog.\n"
            compressed = "# Heading\n\nFox jump dog.\n"
            path = self._file_with(Path(tmp), original)
            with mock.patch.object(compress_mod, "call_claude", return_value=compressed), \
                 mock.patch.object(compress_mod, "validate") as v:
                v.return_value = mock.Mock(is_valid=True, errors=[], warnings=[])
                ok = compress_mod.compress_file(path)
            self.assertTrue(ok)
            self.assertEqual(path.read_text(), compressed)
            # Backups now live OUTSIDE the source dir (issue #420), under a
            # platform-aware data dir mirroring the source parent name.
            backup = compress_mod.backup_dir_for(path.resolve()) / "task.original.md"
            self.assertEqual(backup.read_text(), original)
            self.assertFalse((Path(tmp) / "task.original.md").exists())


if __name__ == "__main__":
    unittest.main()


class Utf8AtomicWriteTests(unittest.TestCase):
    """Regression tests for issue #533 — cp1252 write crash truncated live file."""

    def test_write_text_atomic_utf8_bytes_on_disk(self):
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / "unicode.md"
            text = "# Arrows\n\nA → B — C ✓ 中文\n"
            compress_mod.write_text_atomic(path, text)
            self.assertEqual(path.read_bytes().decode("utf-8"), text)

    def test_write_text_atomic_failure_leaves_destination_untouched(self):
        # Path.write_text truncates before encoding, so an encode error used to
        # zero the file. The atomic writer must fail WITHOUT touching it.
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / "keep.md"
            path.write_text("precious content", encoding="utf-8")
            with self.assertRaises(UnicodeEncodeError):
                compress_mod.write_text_atomic(path, "bad \ud800 surrogate")
            self.assertEqual(path.read_text(encoding="utf-8"), "precious content")
            leftovers = [p for p in Path(tmp).iterdir() if p.name != "keep.md"]
            self.assertEqual(leftovers, [], "temp file must not leak on failure")

    def test_unicode_content_round_trips_through_compression(self):
        with tempfile.TemporaryDirectory() as tmp, \
             tempfile.TemporaryDirectory() as data_home, \
             mock.patch.dict(os.environ, {"XDG_DATA_HOME": data_home, "LOCALAPPDATA": data_home}):
            original = "# Título\n\nFlow: A → B — depois C. Ünïcödé everywhere ✓\n"
            compressed = "# Título\n\nA → B — C. ✓\n"
            path = Path(tmp) / "task.md"
            path.write_text(original, encoding="utf-8")
            with mock.patch.object(compress_mod, "call_claude", return_value=compressed), \
                 mock.patch.object(compress_mod, "validate") as v:
                v.return_value = mock.Mock(is_valid=True, errors=[], warnings=[])
                ok = compress_mod.compress_file(path)
            self.assertTrue(ok)
            self.assertEqual(path.read_bytes().decode("utf-8"), compressed)
            backup = compress_mod.backup_dir_for(path.resolve()) / "task.original.md"
            self.assertEqual(backup.read_bytes().decode("utf-8"), original)

    def test_fix_retry_none_output_restores_original(self):
        # call_claude returning None/empty during the fix-retry loop used to
        # crash Path.write_text AFTER truncating the live file.
        with tempfile.TemporaryDirectory() as tmp, \
             tempfile.TemporaryDirectory() as data_home, \
             mock.patch.dict(os.environ, {"XDG_DATA_HOME": data_home, "LOCALAPPDATA": data_home}):
            original = "# Heading\n\nProse to compress with → unicode.\n"
            path = Path(tmp) / "task.md"
            path.write_text(original, encoding="utf-8")
            invalid = mock.Mock(is_valid=False, errors=["broken"], warnings=[])
            with mock.patch.object(compress_mod, "call_claude",
                                   side_effect=["# H\n\nBad output.\n", None, None]), \
                 mock.patch.object(compress_mod, "validate", return_value=invalid):
                ok = compress_mod.compress_file(path)
            self.assertFalse(ok)
            self.assertEqual(path.read_text(encoding="utf-8"), original,
                             "original must be restored after failed retries")
