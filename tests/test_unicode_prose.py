"""Regression test: multi-byte Unicode prose corruption on Windows (issue behind PR #619).

`compress_file` read and wrote skill files without an explicit
``encoding="utf-8"`` argument (`Path.read_text()` / `Path.write_text()`). On
Windows, omitting `encoding` falls back to `locale.getpreferredencoding(False)`
(commonly cp1252), not UTF-8. Multi-byte prose characters -- em-dash, arrows,
not-equal, ellipsis, curly quotes, bullets -- are mangled or dropped by that
codec, and `validate()` never notices because it only checks code-block /
frontmatter / heading / URL preservation, not general UTF-8 integrity. The
script still prints "Validation passed" and exits 0 while the file on disk is
no longer valid UTF-8.

This test drives `compress_file` end-to-end (only `call_claude` is mocked --
everything else, including the real file I/O and the real `validate()`, runs
for real) and asserts the file written to disk, read back with an *explicit*
UTF-8 decode (what git / an editor / CI would do), is byte-for-byte the exact
Unicode text the "model" returned.
"""

import sys
import tempfile
import unittest
from pathlib import Path
from unittest import mock

REPO_ROOT = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(REPO_ROOT / "skills" / "caveman-compress"))

from scripts import compress as compress_mod  # noqa: E402

ORIGINAL_PROSE = (
    "# Notes\n\n"
    "Use an em-dash — like this, an arrow → there, a not-equal ≠ sign, "
    "an ellipsis … and curly quotes “yes” ‘no’ and a bullet • too.\n"
)

# Distinct from ORIGINAL_PROSE (shorter/reworded) so compress_file's
# identical-output guard doesn't short-circuit the write -- but keeps every
# special character so we can assert each one survived the round trip.
COMPRESSED_PROSE = (
    "# Notes\n\n"
    "Dash — arrow → neq ≠ etc … quote “yes” ‘no’ bullet •.\n"
)

SPECIAL_CHARS = "—→≠…“”‘’•"


class UnicodeProseIntegrityTests(unittest.TestCase):
    def test_compress_file_preserves_multibyte_unicode_prose(self):
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / "notes.md"
            # Write the fixture explicitly as UTF-8 so we know the *input*
            # bytes are correct before compress_file ever touches them.
            path.write_text(ORIGINAL_PROSE, encoding="utf-8")

            # Simulate a real compression model call: it rewrites the prose
            # but faithfully reproduces every Unicode character it saw.
            with mock.patch.object(
                compress_mod, "call_claude", return_value=COMPRESSED_PROSE
            ):
                ok = compress_mod.compress_file(path)

            self.assertTrue(ok, "compress_file should report success")

            # Read back the exact bytes as UTF-8 -- this is what a real
            # editor / git / CI would do. Must not raise and must not have
            # introduced replacement characters.
            raw_bytes = path.read_bytes()
            decoded = raw_bytes.decode("utf-8")  # raises UnicodeDecodeError if corrupted

            self.assertNotIn("�", decoded)
            for ch in SPECIAL_CHARS:
                self.assertIn(ch, decoded, f"character {ch!r} missing/corrupted in output")
            # Normalize line endings before the exact-content comparison --
            # Python's default text-mode write performs universal-newline
            # translation (\n -> os.linesep) independent of this bug, and
            # asserting on that platform-specific detail isn't this test's
            # concern; the Unicode *characters* are what must be exact.
            self.assertEqual(decoded.replace("\r\n", "\n"), COMPRESSED_PROSE)


if __name__ == "__main__":
    unittest.main()
