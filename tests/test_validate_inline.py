import sys
import tempfile
import unittest
from pathlib import Path

CAVEMAN_COMPRESS = Path(__file__).parent.parent / "caveman-compress"
sys.path.insert(0, str(CAVEMAN_COMPRESS))

from scripts.validate import ValidationResult, extract_inline_codes, validate_inline_codes


class TestExtractInlineCodes(unittest.TestCase):
    def test_basic_extraction(self):
        text = "Run `git commit` then `git push`."
        self.assertEqual(sorted(extract_inline_codes(text)), ["git commit", "git push"])

    def test_ignores_content_inside_triple_backtick_fence(self):
        text = "Before.\n```\n`not inline`\n```\nAfter `real`."
        self.assertEqual(extract_inline_codes(text), ["real"])

    def test_ignores_content_inside_four_backtick_fence(self):
        """4-backtick fences (carousel blocks) must not leak inline spans."""
        text = "Before `a`.\n````carousel\n`inside carousel`\n````\nAfter `b`."
        codes = extract_inline_codes(text)
        self.assertIn("a", codes)
        self.assertIn("b", codes)
        self.assertNotIn("inside carousel", codes)

    def test_ignores_content_inside_tilde_fence(self):
        text = "Before `a`.\n~~~\n`inside tilde`\n~~~\nAfter `b`."
        codes = extract_inline_codes(text)
        self.assertIn("a", codes)
        self.assertIn("b", codes)
        self.assertNotIn("inside tilde", codes)

    def test_empty_text(self):
        self.assertEqual(extract_inline_codes(""), [])

    def test_no_inline_codes(self):
        self.assertEqual(extract_inline_codes("plain text no backticks"), [])

    def test_duplicate_spans_counted_separately(self):
        text = "Use `foo` and `foo` again."
        self.assertEqual(extract_inline_codes(text), ["foo", "foo"])


class TestValidateInlineCodes(unittest.TestCase):
    def _run(self, orig, comp):
        result = ValidationResult()
        validate_inline_codes(orig, comp, result)
        return result

    def test_lost_inline_code_is_error(self):
        orig = "Run `git commit` to save."
        comp = "Run git commit to save."
        result = self._run(orig, comp)
        self.assertFalse(result.is_valid)
        self.assertTrue(any("git commit" in e for e in result.errors))

    def test_preserved_inline_code_passes(self):
        orig = "Use `git push` to publish."
        comp = "Use `git push` publish."
        result = self._run(orig, comp)
        self.assertTrue(result.is_valid)
        self.assertEqual(result.errors, [])

    def test_added_inline_code_does_not_warn(self):
        """Claude legitimately adds backticks during compression — not an error or warning."""
        orig = "Run git commit to save."
        comp = "Run `git commit` save."
        result = self._run(orig, comp)
        self.assertTrue(result.is_valid)
        self.assertEqual(result.warnings, [])

    def test_no_codes_anywhere_passes(self):
        orig = "Plain prose only."
        comp = "Plain prose."
        result = self._run(orig, comp)
        self.assertTrue(result.is_valid)

    def test_codes_inside_fence_not_counted_as_lost(self):
        """Content inside 4-backtick fences must not trigger false lost-code errors."""
        orig = "Text `real`.\n````\n`inside fence`\n````"
        comp = "Text `real`.\n````\n`inside fence`\n````"
        result = self._run(orig, comp)
        self.assertTrue(result.is_valid)
        self.assertEqual(result.errors, [])

    def test_multiple_lost_codes_reported(self):
        orig = "Use `foo`, `bar`, and `baz`."
        comp = "Use foo, bar, and baz."
        result = self._run(orig, comp)
        self.assertFalse(result.is_valid)
        combined = " ".join(result.errors)
        self.assertIn("foo", combined)
        self.assertIn("bar", combined)
        self.assertIn("baz", combined)


if __name__ == "__main__":
    unittest.main()
