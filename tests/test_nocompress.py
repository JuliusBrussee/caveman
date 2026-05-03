"""Tests for <!-- nocompress --> / <!-- /nocompress --> escape in caveman-compress."""
import sys
import unittest
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(REPO_ROOT / "caveman-compress"))

from scripts.compress import extract_nocompress, restore_nocompress, PLACEHOLDER
from scripts.validate import strip_nocompress, validate, ValidationResult


class TestExtractNocompress(unittest.TestCase):
    def test_no_regions(self):
        text = "Normal **caveman** text."
        out, regions = extract_nocompress(text)
        self.assertEqual(out, text)
        self.assertEqual(regions, [])

    def test_single_region_replaced(self):
        text = 'Before\n<!-- nocompress -->\n{"key": "value"}\n<!-- /nocompress -->\nAfter'
        out, regions = extract_nocompress(text)
        self.assertIn(PLACEHOLDER.format(index=0), out)
        self.assertNotIn('"key"', out)
        self.assertEqual(len(regions), 1)
        self.assertIn('"key": "value"', regions[0])

    def test_multiple_regions(self):
        text = (
            "<!-- nocompress -->block1<!-- /nocompress -->"
            " middle "
            "<!-- nocompress -->block2<!-- /nocompress -->"
        )
        out, regions = extract_nocompress(text)
        self.assertEqual(len(regions), 2)
        self.assertIn(PLACEHOLDER.format(index=0), out)
        self.assertIn(PLACEHOLDER.format(index=1), out)
        self.assertNotIn("block1", out)
        self.assertNotIn("block2", out)

    def test_case_insensitive_tags(self):
        text = "<!-- NOCOMPRESS -->secret<!-- /NOCOMPRESS -->"
        out, regions = extract_nocompress(text)
        self.assertEqual(len(regions), 1)
        self.assertNotIn("secret", out)

    def test_whitespace_in_tags(self):
        text = "<!--  nocompress  -->data<!--  /nocompress  -->"
        out, regions = extract_nocompress(text)
        self.assertEqual(len(regions), 1)


class TestRestoreNocompress(unittest.TestCase):
    def test_roundtrip(self):
        original = 'Prose\n<!-- nocompress -->\n{"exact": true}\n<!-- /nocompress -->\nMore prose.'
        extracted, regions = extract_nocompress(original)
        restored = restore_nocompress(extracted, regions)
        self.assertEqual(restored, original)

    def test_multiple_roundtrip(self):
        original = "<!-- nocompress -->A<!-- /nocompress --> mid <!-- nocompress -->B<!-- /nocompress -->"
        extracted, regions = extract_nocompress(original)
        restored = restore_nocompress(extracted, regions)
        self.assertEqual(restored, original)

    def test_no_regions_unchanged(self):
        text = "Plain text, no regions."
        _, regions = extract_nocompress(text)
        result = restore_nocompress(text, regions)
        self.assertEqual(result, text)


class TestUrlRegex(unittest.TestCase):
    """URL_REGEX should not capture trailing backticks from inline code wrapping."""

    def test_url_stops_at_trailing_backtick(self):
        from scripts.validate import extract_urls
        # Claude sometimes wraps URLs in inline code: `http://example.com/`
        text = "See `http://example.com/` for details."
        urls = extract_urls(text)
        self.assertIn("http://example.com/", urls)
        self.assertNotIn("http://example.com/`", urls)

    def test_url_stops_at_trailing_paren(self):
        from scripts.validate import extract_urls
        text = "See (http://example.com/) for details."
        urls = extract_urls(text)
        self.assertIn("http://example.com/", urls)

    def test_backtick_wrapped_url_matches_plain_url(self):
        """Validation: original has plain URL, compressed wraps in backticks — should still match."""
        import tempfile
        from pathlib import Path
        with tempfile.TemporaryDirectory() as d:
            d = Path(d)
            original = "See http://example.com/ for details."
            compressed = "See `http://example.com/` for details."
            (d / "orig.md").write_text(original)
            (d / "comp.md").write_text(compressed)
            result = validate(d / "orig.md", d / "comp.md")
            self.assertTrue(result.is_valid, f"URL mismatch false positive: {result.errors}")


class TestStripNocompressForValidation(unittest.TestCase):
    def test_strips_region(self):
        text = "Before\n<!-- nocompress -->\n```json\n{}\n```\n<!-- /nocompress -->\nAfter"
        stripped = strip_nocompress(text)
        self.assertNotIn("```json", stripped)
        self.assertIn("Before", stripped)
        self.assertIn("After", stripped)

    def test_no_regions_unchanged(self):
        text = "# Heading\n\nSome text."
        self.assertEqual(strip_nocompress(text), text)


class TestValidateIgnoresNocompressRegions(unittest.TestCase):
    """Validation should pass when nocompress regions differ between original and compressed."""

    def _write_tmp(self, tmp_dir: Path, name: str, content: str) -> Path:
        p = tmp_dir / name
        p.write_text(content)
        return p

    def test_nocompress_region_not_flagged_as_code_block_mismatch(self):
        import tempfile

        with tempfile.TemporaryDirectory() as tmp:
            d = Path(tmp)
            original = (
                "# Doc\n\n"
                "<!-- nocompress -->\n"
                "```json\n{\"key\": \"original\"}\n```\n"
                "<!-- /nocompress -->\n\n"
                "Some prose here."
            )
            # Compressed version has caveman prose but nocompress region intact
            compressed = (
                "# Doc\n\n"
                "<!-- nocompress -->\n"
                "```json\n{\"key\": \"original\"}\n```\n"
                "<!-- /nocompress -->\n\n"
                "Prose here."
            )
            orig_path = self._write_tmp(d, "orig.md", original)
            comp_path = self._write_tmp(d, "comp.md", compressed)
            result = validate(orig_path, comp_path)
            self.assertTrue(result.is_valid, f"Unexpected errors: {result.errors}")

    def test_nocompress_region_preserved_verbatim_after_roundtrip(self):
        """extract → simulate Claude compression → restore → validate passes."""
        import tempfile

        with tempfile.TemporaryDirectory() as tmp:
            d = Path(tmp)
            original = (
                "# Title\n\n"
                "The system is a very big and important platform that does lots of things.\n\n"
                "<!-- nocompress -->\n"
                "```yaml\nexact:\n  - preserved: true\n```\n"
                "<!-- /nocompress -->\n\n"
                "Please make sure to always run the tests before committing."
            )
            # Simulate what Claude returns: prose compressed, placeholder intact
            sendable, regions = extract_nocompress(original)
            # Claude would compress sendable; we simulate that here
            simulated_claude_output = sendable.replace(
                "The system is a very big and important platform that does lots of things.",
                "System: big platform, many things.",
            ).replace(
                "Please make sure to always run the tests before committing.",
                "Run tests before commit.",
            )
            compressed = restore_nocompress(simulated_claude_output, regions)

            orig_path = self._write_tmp(d, "orig.md", original)
            comp_path = self._write_tmp(d, "comp.md", compressed)
            result = validate(orig_path, comp_path)
            self.assertTrue(result.is_valid, f"Unexpected errors: {result.errors}")


class TestXmlTagHeadingPrevention(unittest.TestCase):
    """Validator must catch when Claude converts XML tags to markdown headings."""

    def test_xml_tag_promoted_to_heading_fails_validation(self):
        """Original has XML tags + no headings; compressed invents a heading → fail."""
        import tempfile
        with tempfile.TemporaryDirectory() as tmp:
            d = Path(tmp)
            original = (
                "<section_intro>\n\n"
                "This is the introduction text.\n\n"
                "</section_intro>\n"
            )
            # Claude converts <section_intro> to a markdown heading
            compressed = (
                "# Introduction\n\n"
                "This is intro text.\n"
            )
            (d / "orig.md").write_text(original)
            (d / "comp.md").write_text(compressed)
            result = validate(d / "orig.md", d / "comp.md")
            self.assertFalse(result.is_valid, "Should fail: Claude added a heading that wasn't in original")
            self.assertTrue(
                any("Heading count mismatch" in e for e in result.errors),
                f"Expected heading count error, got: {result.errors}",
            )

    def test_xml_tags_preserved_as_plain_text_passes_validation(self):
        """Original has XML tags + no headings; compressed keeps XML tags as plain text → pass."""
        import tempfile
        with tempfile.TemporaryDirectory() as tmp:
            d = Path(tmp)
            original = (
                "<section_intro>\n\n"
                "This is the introduction text with lots of extra filler words.\n\n"
                "</section_intro>\n"
            )
            # Claude compresses prose but leaves XML tags intact, no headings added
            compressed = (
                "<section_intro>\n\n"
                "Introduction text.\n\n"
                "</section_intro>\n"
            )
            (d / "orig.md").write_text(original)
            (d / "comp.md").write_text(compressed)
            result = validate(d / "orig.md", d / "comp.md")
            self.assertTrue(result.is_valid, f"Unexpected errors: {result.errors}")


if __name__ == "__main__":
    unittest.main()