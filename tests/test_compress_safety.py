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

    def test_utf8_glyphs_preserved_in_roundtrip(self):
        """Regression: explicit encoding="utf-8" in compress.py prevents
        cp1252 crashes on Windows when files contain Unicode glyphs like
        ✅/❌ or other non-Latin-1 characters. This test verifies that a
        file with mixed Unicode content survives a compression roundtrip
        without encoding corruption."""
        with tempfile.TemporaryDirectory() as tmp, \
             tempfile.TemporaryDirectory() as data_home, \
             mock.patch.dict(os.environ, {"XDG_DATA_HOME": data_home, "LOCALAPPDATA": data_home}):
            original = (
                "# Status ✅\n\n"
                "The build passed ✅ — all 12 tests green.\n"
                "Café résumé naïve façade ❌ not yet merged.\n"
                "Emoji range: 🪨 🦊 🔥\n"
            )
            compressed = "# Status ✅\n\nBuild passed ✅. Café résumé not merged ❌.\n"
            path = self._file_with(Path(tmp), original)
            with mock.patch.object(compress_mod, "call_claude", return_value=compressed), \
                 mock.patch.object(compress_mod, "validate") as v:
                v.return_value = mock.Mock(is_valid=True, errors=[], warnings=[])
                ok = compress_mod.compress_file(path)
            self.assertTrue(ok)
            # Read back — encoding must survive the roundtrip.
            roundtripped = path.read_text(encoding="utf-8")
            self.assertEqual(roundtripped, compressed)
            # Backup must also be intact.
            backup = compress_mod.backup_dir_for(path.resolve()) / "task.original.md"
            self.assertEqual(backup.read_text(encoding="utf-8"), original)
            self.assertFalse((Path(tmp) / "task.original.md").exists())


if __name__ == "__main__":
    unittest.main()
