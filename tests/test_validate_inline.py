import sys
import tempfile
import unittest
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(REPO_ROOT / "skills" / "caveman-compress"))

from scripts.validate import (  # noqa: E402
    ValidationResult,
    extract_code_blocks,
    extract_inline_codes,
    validate,
    validate_headings,
    validate_inline_codes,
    validate_paths,
)


class TestExtractInlineCodes(unittest.TestCase):
    def test_fenced_blocks_excluded(self):
        text = "```\ncode here\n```\n`inline code`"
        result = extract_inline_codes(text)
        self.assertEqual(result, ["inline code"])

    def test_inline_only(self):
        text = "Use `rm -rf /` to delete everything"
        result = extract_inline_codes(text)
        self.assertEqual(result, ["rm -rf /"])

    def test_mixed_content(self):
        text = """
Some text with `inline1` and `inline2`.

```
code block
```

More text with `inline3`.
"""
        result = extract_inline_codes(text)
        self.assertEqual(set(result), {"inline1", "inline2", "inline3"})

    def test_empty(self):
        self.assertEqual(extract_inline_codes("no backticks here"), [])


class TestValidateInlineCodes(unittest.TestCase):
    def test_match(self):
        result = ValidationResult()
        validate_inline_codes("use `cmd` here", "use `cmd` here", result)
        self.assertTrue(result.is_valid)

    def test_lost(self):
        result = ValidationResult()
        validate_inline_codes("use `cmd` here", "use  here", result)
        self.assertFalse(result.is_valid)
        self.assertIn("Inline code lost", result.errors[0])

    def test_added(self):
        result = ValidationResult()
        validate_inline_codes("use  here", "use `new` here", result)
        self.assertTrue(result.is_valid)
        self.assertIn("Inline code added", result.warnings[0])

    def test_empty_orig(self):
        result = ValidationResult()
        validate_inline_codes("no codes", "use `new` here", result)
        self.assertTrue(result.is_valid)

    def test_both_empty(self):
        result = ValidationResult()
        validate_inline_codes("plain text", "also plain", result)
        self.assertTrue(result.is_valid)


class TestVariableLengthFences(unittest.TestCase):
    """Fix #5 — backticks inside a 4+ backtick fence must not count as inline."""

    def test_four_backtick_fence_excludes_inner_backticks(self):
        text = "````\n`not inline` ```nested```\n````\n`real inline`"
        self.assertEqual(extract_inline_codes(text), ["real inline"])

    def test_tilde_variable_fence(self):
        text = "~~~~\n`inside tilde block`\n~~~~\n`outside`"
        self.assertEqual(extract_inline_codes(text), ["outside"])

    def test_closing_fence_must_be_at_least_open_length(self):
        # A 3-backtick line does NOT close a 4-backtick fence; the whole region
        # (including the would-be inline backticks) stays a single code block.
        text = "````\n`a`\n```\n`b`\n````\n`outside`"
        self.assertEqual(extract_inline_codes(text), ["outside"])


class TestIndentedCodeBlocks(unittest.TestCase):
    """Fix #6 — 4-space / tab indented code blocks are preserved like fenced."""

    def test_indented_block_extracted(self):
        text = "Para.\n\n    code line one\n    code line two\n\nMore prose."
        blocks = extract_code_blocks(text)
        self.assertEqual(blocks, ["    code line one\n    code line two"])

    def test_tab_indented_block_extracted(self):
        text = "Para.\n\n\ttabbed code\n\nProse."
        self.assertEqual(extract_code_blocks(text), ["\ttabbed code"])

    def test_inline_backticks_in_indented_block_ignored(self):
        text = "Intro.\n\n    `looks inline but is code`\n\n`truly inline`"
        self.assertEqual(extract_inline_codes(text), ["truly inline"])

    def test_indented_code_loss_fails_validation(self):
        with tempfile.TemporaryDirectory() as tmp:
            orig = Path(tmp) / "o.md"
            comp = Path(tmp) / "c.md"
            orig.write_text("Intro.\n\n    keep me verbatim\n\nEnd.")
            comp.write_text("Intro.\n\nEnd.")
            result = validate(orig, comp)
            self.assertFalse(result.is_valid)
            self.assertTrue(any("Code blocks not preserved" in e for e in result.errors))


class TestHeadingExactEnforced(unittest.TestCase):
    """Fix #7 / #9 — heading text/order/level change is an ERROR, not a warning;
    a count mismatch does not also emit the redundant text-change message."""

    def test_heading_text_change_is_error(self):
        result = ValidationResult()
        validate_headings("## API Reference\n", "## API\n", result)
        self.assertFalse(result.is_valid)
        self.assertTrue(any("Heading text/order changed" in e for e in result.errors))
        self.assertEqual(result.warnings, [])

    def test_heading_level_change_is_error(self):
        result = ValidationResult()
        validate_headings("### Git Workflow\n", "## Git Workflow\n", result)
        self.assertFalse(result.is_valid)

    def test_heading_count_mismatch_no_redundant_warning(self):
        result = ValidationResult()
        validate_headings("# A\n## B\n", "# A\n", result)
        self.assertFalse(result.is_valid)
        self.assertEqual(len(result.errors), 1)
        self.assertIn("Heading count mismatch", result.errors[0])
        self.assertEqual(result.warnings, [])

    def test_identical_headings_pass(self):
        result = ValidationResult()
        validate_headings("# A\n## B\n", "# A\n## B\n", result)
        self.assertTrue(result.is_valid)


class TestPathLossEnforced(unittest.TestCase):
    """Fix #8 — a referenced path dropped/changed is an ERROR; spurious
    prose-like matches that only appear in the compressed output stay a warning."""

    def test_lost_path_is_error(self):
        result = ValidationResult()
        validate_paths("See ./src/app.py for details", "See for details", result)
        self.assertFalse(result.is_valid)
        self.assertTrue(any("path(s) lost" in e.lower() for e in result.errors))

    def test_added_prose_match_is_warning_not_error(self):
        result = ValidationResult()
        # "pros/cons" is caveman prose, not a real path; it must not fail.
        validate_paths("weigh the options", "weigh pros/cons", result)
        self.assertTrue(result.is_valid)
        self.assertTrue(result.warnings)

    def test_preserved_path_passes(self):
        result = ValidationResult()
        validate_paths("edit ./config/settings.json now", "edit ./config/settings.json", result)
        self.assertTrue(result.is_valid)


class TestValidateIntegration(unittest.TestCase):
    def test_validate_inline_codes_wired(self):
        with tempfile.TemporaryDirectory() as tmp:
            orig = Path(tmp) / "original.md"
            comp = Path(tmp) / "compressed.md"
            orig.write_text("Run `rm -rf /` to delete")
            comp.write_text("Run  to delete")
            result = validate(orig, comp)
            self.assertFalse(result.is_valid)
            self.assertTrue(any("Inline code lost" in e for e in result.errors))


if __name__ == "__main__":
    unittest.main()