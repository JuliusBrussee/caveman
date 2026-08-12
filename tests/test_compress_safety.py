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
from pathlib import Path
from unittest import mock

REPO_ROOT = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(REPO_ROOT / "skills" / "caveman-compress"))

from scripts import compress as compress_mod  # noqa: E402


class CompressSafetyTests(unittest.TestCase):
    def _file_with(self, dirpath: Path, text: str) -> Path:
        path = dirpath / "task.md"
        path.write_text(text, encoding="utf-8")
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

    def test_utf8_roundtrip_survives_compression(self):
        # Path.read_text() without encoding= would decode with the system
        # locale codec (cp1252/cp949 on Windows) and could silently mangle
        # non-ASCII bytes. Read raw bytes and decode strictly as UTF-8 so the
        # assertion is locale-independent (issue #686).
        with tempfile.TemporaryDirectory() as tmp, \
             tempfile.TemporaryDirectory() as data_home, \
             mock.patch.dict(os.environ, {"XDG_DATA_HOME": data_home, "LOCALAPPDATA": data_home}):
            original = "# Heading\n\nCafé, 中文, and an arrow → here.\n"
            compressed = "# Heading\n\nCafé 中文 arrow → here.\n"
            path = self._file_with(Path(tmp), original)
            with mock.patch.object(compress_mod, "call_claude", return_value=compressed), \
                 mock.patch.object(compress_mod, "validate") as v:
                v.return_value = mock.Mock(is_valid=True, errors=[], warnings=[])
                ok = compress_mod.compress_file(path)
            self.assertTrue(ok)
            self.assertEqual(path.read_bytes().decode("utf-8"), compressed)
            backup = compress_mod.backup_dir_for(path.resolve()) / "task.original.md"
            self.assertEqual(backup.read_bytes().decode("utf-8"), original)

    def test_write_text_atomic_leaves_destination_untouched_on_encode_failure(self):
        # Direct unit test of the atomic-write primitive: an encode failure
        # partway through must not truncate the destination or leave a *.tmp
        # file behind (issue #655).
        class ExplodingStr(str):
            def encode(self, *args, **kwargs):
                raise UnicodeEncodeError("utf-8", self, 0, 1, "forced failure")

        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / "task.md"
            path.write_text("original content", encoding="utf-8")

            with self.assertRaises(UnicodeEncodeError):
                compress_mod.write_text_atomic(path, ExplodingStr("new content"))

            self.assertEqual(path.read_text(encoding="utf-8"), "original content")
            self.assertEqual(list(Path(tmp).glob("*.tmp")), [])

    def test_forced_primary_write_failure_leaves_original_and_backup_intact(self):
        # Same failure, exercised through the full compress_file pipeline:
        # the backup must already exist and be intact, the target must be
        # untouched, and no *.tmp litter must remain in either directory.
        with tempfile.TemporaryDirectory() as tmp, \
             tempfile.TemporaryDirectory() as data_home, \
             mock.patch.dict(os.environ, {"XDG_DATA_HOME": data_home, "LOCALAPPDATA": data_home}):
            original = "# Heading\n\nProse to compress.\n"
            compressed = "# Heading\n\nProse.\n"
            path = self._file_with(Path(tmp), original)
            target = path.resolve()
            real_write_text_atomic = compress_mod.write_text_atomic

            def flaky_write(write_path, text):
                if write_path == target:
                    raise UnicodeEncodeError("utf-8", text, 0, 1, "forced failure")
                return real_write_text_atomic(write_path, text)

            with mock.patch.object(compress_mod, "call_claude", return_value=compressed), \
                 mock.patch.object(compress_mod, "validate") as v, \
                 mock.patch.object(compress_mod, "write_text_atomic", side_effect=flaky_write):
                v.return_value = mock.Mock(is_valid=True, errors=[], warnings=[])
                with self.assertRaises(UnicodeEncodeError):
                    compress_mod.compress_file(path)

            self.assertEqual(path.read_text(encoding="utf-8"), original)
            backup_dir = compress_mod.backup_dir_for(target)
            backup = backup_dir / "task.original.md"
            self.assertEqual(backup.read_text(encoding="utf-8"), original)
            self.assertEqual(list(Path(tmp).glob("*.tmp")), [])
            self.assertEqual(list(backup_dir.glob("*.tmp")), [])

    def test_permission_preserved_across_compression(self):
        with tempfile.TemporaryDirectory() as tmp, \
             tempfile.TemporaryDirectory() as data_home, \
             mock.patch.dict(os.environ, {"XDG_DATA_HOME": data_home, "LOCALAPPDATA": data_home}):
            original = "# Heading\n\nProse to compress.\n"
            compressed = "# Heading\n\nProse.\n"
            path = self._file_with(Path(tmp), original)
            path.chmod(0o644)
            with mock.patch.object(compress_mod, "call_claude", return_value=compressed), \
                 mock.patch.object(compress_mod, "validate") as v:
                v.return_value = mock.Mock(is_valid=True, errors=[], warnings=[])
                ok = compress_mod.compress_file(path)
            self.assertTrue(ok)
            self.assertEqual(stat.S_IMODE(path.stat().st_mode), 0o644)

    def test_retry_preamble_output_rejected_and_not_written(self):
        # A fix-retry response with a prose preamble ahead of the real content
        # must never reach disk — only the restore-on-failure write should
        # land, and it must restore the original (issue #588).
        with tempfile.TemporaryDirectory() as tmp, \
             tempfile.TemporaryDirectory() as data_home, \
             mock.patch.dict(os.environ, {"XDG_DATA_HOME": data_home, "LOCALAPPDATA": data_home}):
            original = "# Heading\n\nProse that fails validation.\n"
            first_pass = "# Heading\n\nCompressed prose.\n"
            preamble_fix = "Here is the fixed file:\n\n# Heading\n\nCompressed prose, fixed.\n"
            path = self._file_with(Path(tmp), original)

            invalid = mock.Mock(is_valid=False, errors=["some validation error"], warnings=[])
            written_texts = []
            real_write_target = compress_mod._write_target

            def spy_write_target(target_path, text, backup_path):
                written_texts.append(text)
                return real_write_target(target_path, text, backup_path)

            with mock.patch.object(
                compress_mod, "call_claude", side_effect=[first_pass, preamble_fix]
            ), mock.patch.object(compress_mod, "validate", return_value=invalid), \
                 mock.patch.object(compress_mod, "_write_target", side_effect=spy_write_target):
                ok = compress_mod.compress_file(path)

            self.assertFalse(ok)
            self.assertNotIn(preamble_fix, written_texts)
            self.assertEqual(path.read_text(encoding="utf-8"), original)


if __name__ == "__main__":
    unittest.main()
