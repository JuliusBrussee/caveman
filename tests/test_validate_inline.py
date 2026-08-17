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
    validate_inline_codes,
)


class TestIndentedFence(unittest.TestCase):
    """#820: a fence indented 4+ spaces (nested in a list item) made the file
    permanently uncompressible. FENCE_OPEN_REGEX does not match it, so
    extract_code_blocks never removed it and its own backticks leaked into
    inline-code pairing, shifting every subsequent span."""

    NESTED = "# T\n\n* Example:\n    ```markdown\n    x = 1\n    ```\n* Use `alpha` and `beta` here.\n"

    def test_indented_fence_markers_not_leaked_as_inline(self):
        self.assertEqual(extract_inline_codes(self.NESTED), ["alpha", "beta"])

    def test_nested_fence_file_validates_against_itself(self):
        result = ValidationResult()
        validate_inline_codes(self.NESTED, self.NESTED, result)
        self.assertTrue(result.is_valid, result.errors)

    def test_deeply_indented_fence_markers_not_leaked(self):
        text = "* a\n  * b\n        ```\n        y = 2\n        ```\n* Use `gamma`.\n"
        self.assertEqual(extract_inline_codes(text), ["gamma"])

    def test_tilde_fence_indented_in_list(self):
        text = "* Example:\n    ~~~python\n    z = 3\n    ~~~\n* Use `delta`.\n"
        self.assertEqual(extract_inline_codes(text), ["delta"])


class TestIndentedFenceDoesNotSwallow(unittest.TestCase):
    """Widening FENCE_OPEN_REGEX to `\\s*` is the tempting fix for #820 and is a
    net regression: a lone indented ``` shown as an example then opens a block
    that runs to the next bare fence, hiding a REAL code block from validation.
    That converts a false failure into a false PASS, and a false PASS
    overwrites the user's original file with unvalidated output."""

    def test_lone_indented_marker_does_not_capture_the_real_block(self):
        doc = "To open a fence write:\n\n    ```\n\nThen prose with `alpha`.\n\n```js\nreal = 1\n```\n"
        self.assertEqual(extract_code_blocks(doc), ["```js\nreal = 1\n```"])
        self.assertEqual(extract_inline_codes(doc), ["alpha"])

    def test_content_loss_after_an_indented_marker_still_fails(self):
        orig = (
            "# Runbook\n\nA fence opens with three backticks:\n\n    ```\n\n"
            "Emergency rollback:\n\n```\nhelm rollback prod 41 --namespace production\n```\n"
        )
        comp = orig.replace("prod 41", "prod")
        with tempfile.TemporaryDirectory() as tmp:
            o, c = Path(tmp) / "o.md", Path(tmp) / "c.md"
            o.write_text(orig, encoding="utf-8")
            c.write_text(comp, encoding="utf-8")
            self.assertFalse(validate(o, c).is_valid, "dropped revision number must be caught")


class TestMultiLineSpansStillCompared(unittest.TestCase):
    """CommonMark permits a line ending inside a code span, and hard-wrapped
    markdown produces them. Restricting the span pattern to a single line drops
    those spans from the comparison set entirely, which silently downgrades a
    deleted or mutated span to PASS."""

    def test_multiline_span_is_extracted(self):
        self.assertEqual(
            extract_inline_codes("Run `npm install --save-dev\nsome-package` first and `x` after."),
            ["npm install --save-dev\nsome-package", "x"],
        )

    def test_deleting_a_multiline_span_is_an_error(self):
        result = ValidationResult()
        validate_inline_codes(
            "Pass the `--dangerously-skip-permissions\nflag` before running.",
            "Run it.",
            result,
        )
        self.assertFalse(result.is_valid)

    def test_mutating_a_multiline_span_is_an_error_not_a_warning(self):
        result = ValidationResult()
        validate_inline_codes("Set `--flag\nvalue` now.", "Set `--flag other` now.", result)
        self.assertFalse(result.is_valid, "a changed CLI argument must not pass as a warning")


class TestErrorRendering(unittest.TestCase):
    """#820's failures were undiagnosable because a garbled span was printed
    whole. That is a presentation problem — fix it in the message, not by
    narrowing what counts as a span."""

    def test_long_span_is_truncated_and_newlines_escaped(self):
        result = ValidationResult()
        validate_inline_codes("a `" + "x" * 500 + "\nmore` b", "a b", result)
        message = result.errors[0]
        self.assertLess(len(message), 200, message)
        self.assertNotIn("\n", message[len("Inline code lost: "):])


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


class TestValidateIntegration(unittest.TestCase):
    def test_validate_inline_codes_wired(self):
        with tempfile.TemporaryDirectory() as tmp:
            orig = Path(tmp) / "original.md"
            comp = Path(tmp) / "compressed.md"
            orig.write_text("Run `rm -rf /` to delete", encoding="utf-8")
            comp.write_text("Run  to delete", encoding="utf-8")
            result = validate(orig, comp)
            self.assertFalse(result.is_valid)
            self.assertTrue(any("Inline code lost" in e for e in result.errors))


if __name__ == "__main__":
    unittest.main()
