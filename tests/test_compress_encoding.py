"""UTF-8 encoding and crash-restore regression tests (issues #686, #766)."""

import locale
import os
import sys
import tempfile
import unittest
from pathlib import Path
from unittest import mock

REPO_ROOT = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(REPO_ROOT / "skills" / "caveman-compress"))

from scripts import compress as compress_mod  # noqa: E402
from scripts import textio  # noqa: E402


UNICODE_MARKDOWN = (
    "# Notes\n\n"
    "Korean: 안녕하세요\n"
    "Arrow: → branch\n"
    "Emoji: ⚠️ ✅\n"
    "Box: ├─ item\n"
)


class CompressEncodingTests(unittest.TestCase):
    def _isolated_env(self):
        data_home = tempfile.mkdtemp()
        return mock.patch.dict(
            os.environ,
            {"XDG_DATA_HOME": data_home, "LOCALAPPDATA": data_home},
        )

    def test_utf8_roundtrip_under_non_utf8_locale_preference(self):
        compressed = "# Notes\n\nKorean ok. Arrow ok. Emoji ok.\n"
        with tempfile.TemporaryDirectory() as tmp, self._isolated_env(), mock.patch.object(
            locale, "getpreferredencoding", return_value="cp1252"
        ):
            path = Path(tmp) / "notes.md"
            path.write_bytes(UNICODE_MARKDOWN.encode("utf-8"))

            with mock.patch.object(compress_mod, "call_claude", return_value=compressed), \
                 mock.patch.object(compress_mod, "validate") as validate_mock:
                validate_mock.return_value = mock.Mock(is_valid=True, errors=[], warnings=[])
                ok = compress_mod.compress_file(path)

            self.assertTrue(ok)
            self.assertEqual(path.read_text(encoding="utf-8"), compressed)
            backup = compress_mod.backup_dir_for(path.resolve()) / "notes.original.md"
            self.assertEqual(backup.read_text(encoding="utf-8"), UNICODE_MARKDOWN)
            self.assertNotIn("\ufffd", path.read_text(encoding="utf-8"))

    def test_fix_retry_exception_restores_original(self):
        first_pass = "# Notes\n\nBroken compressed draft.\n"
        with tempfile.TemporaryDirectory() as tmp, self._isolated_env():
            path = Path(tmp) / "notes.md"
            path.write_text(UNICODE_MARKDOWN, encoding="utf-8")

            invalid = mock.Mock(is_valid=False, errors=["Code blocks not preserved exactly"], warnings=[])
            with mock.patch.object(
                compress_mod,
                "call_claude",
                side_effect=[first_pass, RuntimeError("simulated claude failure")],
            ), mock.patch.object(compress_mod, "validate", return_value=invalid):
                with self.assertRaises(RuntimeError):
                    compress_mod.compress_file(path)

            self.assertEqual(path.read_text(encoding="utf-8"), UNICODE_MARKDOWN)
            backup = compress_mod.backup_dir_for(path.resolve()) / "notes.original.md"
            self.assertFalse(backup.exists())

    def test_write_text_atomic_preserves_target_on_failure(self):
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / "target.md"
            path.write_text("keep me", encoding="utf-8")
            original_bytes = path.read_bytes()

            real_write = textio.write_text

            def fail_on_temp(target: Path, text: str) -> None:
                if target.name.endswith(".tmp"):
                    raise OSError("simulated write failure")
                real_write(target, text)

            with mock.patch("scripts.textio.write_text", side_effect=fail_on_temp):
                with self.assertRaises(OSError):
                    textio.write_text_atomic(path, "new content")

            self.assertEqual(path.read_bytes(), original_bytes)


if __name__ == "__main__":
    unittest.main()
