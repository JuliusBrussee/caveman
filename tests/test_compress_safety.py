"""Tests for the data-loss guards in `compress_file` (issue #237).

The compress orchestrator used to overwrite the input even when Claude
returned an empty string or a no-op echo, and used to write a backup
without verifying that the bytes survived the round-trip. These tests
pin the new defensive checks: nothing on disk changes when the compressed
output is empty or identical to the input, and a backup-write that drops
bytes is detected before the input is overwritten.
"""

import os
import stat
import sys
import tempfile
import unittest
from contextlib import redirect_stdout
from io import StringIO
from pathlib import Path
from unittest import mock

REPO_ROOT = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(REPO_ROOT / "skills" / "caveman-compress"))

from scripts import compress as compress_mod  # noqa: E402

UTF8_ENCODING = "utf-8"
NON_CP1252_ARROW = "\u2192"
WINDOWS_CHARMAP_ENCODING = "cp1252"
WINDOWS_CHARMAP_ERROR = "character maps to <undefined>"
ORIGINAL_PROSE = "# Heading\n\nSome prose that should compress safely.\n"
COMPRESSED_WITH_ARROW = f"# Heading\n\nArrow {NON_CP1252_ARROW} survives.\n"
WRITE_FAILURE_MESSAGE = "synthetic write failure"
BACKUP_LOCATION_PREFIX = "Original backup remains at:"
PRESERVED_FILE_MODE = 0o644


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

    def test_non_cp1252_output_uses_utf8_writes(self):
        with tempfile.TemporaryDirectory() as tmp, \
             tempfile.TemporaryDirectory() as data_home, \
             mock.patch.dict(os.environ, {"XDG_DATA_HOME": data_home, "LOCALAPPDATA": data_home}):
            original = ORIGINAL_PROSE
            compressed = COMPRESSED_WITH_ARROW
            path = self._file_with(Path(tmp), original)
            real_write_text = Path.write_text

            def windows_default_write_text(self, data, encoding=None, errors=None, newline=None):
                if encoding is None and NON_CP1252_ARROW in data:
                    real_write_text(self, "", encoding=WINDOWS_CHARMAP_ENCODING)
                    raise UnicodeEncodeError(
                        WINDOWS_CHARMAP_ENCODING,
                        data,
                        data.index(NON_CP1252_ARROW),
                        data.index(NON_CP1252_ARROW) + 1,
                        WINDOWS_CHARMAP_ERROR,
                    )
                return real_write_text(self, data, encoding=encoding, errors=errors, newline=newline)

            with mock.patch.object(compress_mod, "call_claude", return_value=compressed), \
                 mock.patch.object(compress_mod, "validate") as v, \
                 mock.patch.object(Path, "write_text", windows_default_write_text):
                v.return_value = mock.Mock(is_valid=True, errors=[], warnings=[])
                ok = compress_mod.compress_file(path)
            self.assertTrue(ok)
            self.assertEqual(path.read_text(encoding=UTF8_ENCODING), compressed)
            backup = compress_mod.backup_dir_for(path.resolve()) / "task.original.md"
            self.assertEqual(backup.read_text(encoding=UTF8_ENCODING), original)

    def test_primary_write_failure_reports_backup_path(self):
        with tempfile.TemporaryDirectory() as tmp, \
             tempfile.TemporaryDirectory() as data_home, \
             mock.patch.dict(os.environ, {"XDG_DATA_HOME": data_home, "LOCALAPPDATA": data_home}):
            original = ORIGINAL_PROSE
            compressed = COMPRESSED_WITH_ARROW
            path = self._file_with(Path(tmp), original)
            real_write_text_atomic = compress_mod.write_text_atomic

            def fail_primary_write(target_path, text):
                if target_path == path:
                    raise OSError(WRITE_FAILURE_MESSAGE)
                return real_write_text_atomic(target_path, text)

            output = StringIO()
            with mock.patch.object(compress_mod, "call_claude", return_value=compressed), \
                 mock.patch.object(compress_mod, "write_text_atomic", side_effect=fail_primary_write), \
                 redirect_stdout(output):
                ok = compress_mod.compress_file(path)

            backup = compress_mod.backup_dir_for(path.resolve()) / "task.original.md"
            self.assertFalse(ok)
            self.assertEqual(path.read_text(encoding=UTF8_ENCODING), original)
            self.assertEqual(backup.read_text(encoding=UTF8_ENCODING), original)
            self.assertIn(WRITE_FAILURE_MESSAGE, output.getvalue())
            self.assertIn(BACKUP_LOCATION_PREFIX, output.getvalue())
            self.assertIn(str(backup), output.getvalue())

    def test_atomic_write_preserves_existing_file_mode(self):
        with tempfile.TemporaryDirectory() as tmp:
            path = self._file_with(Path(tmp), ORIGINAL_PROSE)
            os.chmod(path, PRESERVED_FILE_MODE)

            compress_mod.write_text_atomic(path, COMPRESSED_WITH_ARROW)

            self.assertEqual(stat.S_IMODE(path.stat().st_mode), PRESERVED_FILE_MODE)
            self.assertEqual(path.read_text(encoding=UTF8_ENCODING), COMPRESSED_WITH_ARROW)


if __name__ == "__main__":
    unittest.main()
