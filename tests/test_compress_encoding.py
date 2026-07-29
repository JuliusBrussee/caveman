"""Tests for the Windows text-mode file I/O guards in `compress_file` (issue #762).

`Path.read_text` / `Path.write_text` default to the locale encoding and to CRLF
translation on Windows. That corrupted every non-ASCII character the model
emitted (issue #686 and friends) and silently rewrote the line endings of every
file the compressor touched. These tests pin the fix: output is always UTF-8,
the source file's line endings survive the round-trip, and the backup stays a
byte-for-byte copy of the input.
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

# Claude answers in LF and reaches for typographic punctuation unprompted. An
# em dash is exactly the character that lands as a bare 0x97 under cp1252.
COMPRESSED_LF = "# Heading\n\nFox jump dog — done.\n"
EM_DASH_UTF8 = b"\xe2\x80\x94"
SOURCE_LF = "# Heading\n\nThe quick brown fox jumps over the lazy dog.\n"


class MatchLineEndingsTests(unittest.TestCase):
    def test_lf_reference_yields_lf(self):
        self.assertEqual(
            compress_mod.match_line_endings("a\r\nb\r\n", "x\ny\n"), "a\nb\n"
        )

    def test_crlf_reference_yields_crlf(self):
        self.assertEqual(
            compress_mod.match_line_endings("a\nb\n", "x\r\ny\r\n"), "a\r\nb\r\n"
        )

    def test_reference_without_newlines_defaults_to_lf(self):
        self.assertEqual(compress_mod.match_line_endings("a\nb\n", "x"), "a\nb\n")

    def test_is_idempotent(self):
        once = compress_mod.match_line_endings("a\nb\n", "x\r\ny\r\n")
        twice = compress_mod.match_line_endings(once, "x\r\ny\r\n")
        self.assertEqual(once, twice)


class ReadExactTests(unittest.TestCase):
    def _written(self, tmp: str, raw: bytes) -> Path:
        path = Path(tmp) / "sample.md"
        path.write_bytes(raw)
        return path

    def test_preserves_crlf(self):
        with tempfile.TemporaryDirectory() as tmp:
            path = self._written(tmp, b"one\r\ntwo\r\n")
            self.assertEqual(compress_mod.read_exact(path), "one\r\ntwo\r\n")

    def test_preserves_lf(self):
        with tempfile.TemporaryDirectory() as tmp:
            path = self._written(tmp, b"one\ntwo\n")
            self.assertEqual(compress_mod.read_exact(path), "one\ntwo\n")

    def test_decodes_utf8_regardless_of_locale(self):
        with tempfile.TemporaryDirectory() as tmp:
            path = self._written(tmp, "dash — here".encode("utf-8"))
            self.assertEqual(compress_mod.read_exact(path), "dash — here")


class CompressTextModeTests(unittest.TestCase):
    """End-to-end through `compress_file` with the model call stubbed out."""

    def _compress(self, raw: bytes):
        """Run the real orchestrator over `raw`; return (output, backup) bytes.

        The backup data dir is redirected into the temp tree so the out-of-tree
        backup (issue #420) never lands in the developer's real home dir.
        """
        with tempfile.TemporaryDirectory() as tmp, \
             tempfile.TemporaryDirectory() as data_home, \
             mock.patch.dict(os.environ, {"XDG_DATA_HOME": data_home,
                                          "LOCALAPPDATA": data_home}):
            path = Path(tmp) / "task.md"
            path.write_bytes(raw)
            with mock.patch.object(compress_mod, "call_claude",
                                   return_value=COMPRESSED_LF), \
                 mock.patch.object(compress_mod, "validate") as v:
                v.return_value = mock.Mock(is_valid=True, errors=[], warnings=[])
                ok = compress_mod.compress_file(path)
            self.assertTrue(ok)
            backup = compress_mod.backup_dir_for(path.resolve()) / "task.original.md"
            return path.read_bytes(), backup.read_bytes()

    def test_non_ascii_output_is_utf8(self):
        out, _ = self._compress(SOURCE_LF.encode("utf-8"))
        self.assertIn(EM_DASH_UTF8, out)
        self.assertNotIn(b"\x97", out)
        out.decode("utf-8")  # raises if the locale codec leaked through

    def test_lf_source_stays_lf(self):
        out, _ = self._compress(SOURCE_LF.encode("utf-8"))
        self.assertNotIn(b"\r", out)

    def test_crlf_source_stays_crlf(self):
        raw = SOURCE_LF.replace("\n", "\r\n").encode("utf-8")
        out, _ = self._compress(raw)
        self.assertIn(b"\r\n", out)
        self.assertNotIn(b"\n", out.replace(b"\r\n", b""))

    def test_backup_is_byte_identical_for_lf_source(self):
        raw = SOURCE_LF.encode("utf-8")
        _, backup = self._compress(raw)
        self.assertEqual(backup, raw)

    def test_backup_is_byte_identical_for_crlf_source(self):
        raw = SOURCE_LF.replace("\n", "\r\n").encode("utf-8")
        _, backup = self._compress(raw)
        self.assertEqual(backup, raw)

    def test_no_op_guard_still_fires_on_crlf_source(self):
        """A CRLF file whose body Claude echoes back in LF must not be touched.

        The body carries the source's CRLF while Claude always answers LF, so
        the no-op comparison has to normalize before matching — otherwise the
        guard silently stops firing on every CRLF file.
        """
        raw = SOURCE_LF.replace("\n", "\r\n").encode("utf-8")
        with tempfile.TemporaryDirectory() as tmp, \
             tempfile.TemporaryDirectory() as data_home, \
             mock.patch.dict(os.environ, {"XDG_DATA_HOME": data_home,
                                          "LOCALAPPDATA": data_home}):
            path = Path(tmp) / "task.md"
            path.write_bytes(raw)
            with mock.patch.object(compress_mod, "call_claude",
                                   return_value=SOURCE_LF):
                ok = compress_mod.compress_file(path)
            self.assertFalse(ok)
            self.assertEqual(path.read_bytes(), raw)
            backup = compress_mod.backup_dir_for(path.resolve()) / "task.original.md"
            self.assertFalse(backup.exists())


if __name__ == "__main__":
    unittest.main()
