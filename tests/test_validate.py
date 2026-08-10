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
    validate_code_blocks,
    validate_inline_codes,
)

MANGLED = "- step:\n{i}```\n{i}rm -rf /  # rewritten\n{i}```\n"
INTACT = "- step:\n{i}```\n{i}rm -rf /important\n{i}```\n"


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

    def test_indented_fence_backtick_not_leaked_as_inline(self):
        # A fence indented 1-3 spaces is valid CommonMark and already handled
        # by extract_code_blocks/FENCE_OPEN_REGEX. The old column-0-anchored
        # strip regex missed it, so a backtick inside the indented fence body
        # leaked out and got paired with the next real inline span (issue
        # from PR #619 review). Only the real trailing inline span should
        # come back.
        text = "   ```\n   `weird`\n   ```\nReal `inline` span here."
        result = extract_inline_codes(text)
        self.assertEqual(result, ["inline"])

    def test_list_nested_fence_backtick_not_leaked_as_inline(self):
        text = "- step:\n    ```\n    `weird`\n    ```\nReal `inline` span here."
        result = extract_inline_codes(text)
        self.assertEqual(result, ["inline"])

    def test_deeply_nested_fence_backtick_not_leaked_as_inline(self):
        text = "- a:\n  - b:\n        ```\n        `weird`\n        ```\nReal `inline` span."
        result = extract_inline_codes(text)
        self.assertEqual(result, ["inline"])


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


class TestExtractCodeBlocks(unittest.TestCase):
    def test_top_level_fence(self):
        self.assertEqual(len(extract_code_blocks("```\ncode\n```\n")), 1)

    def test_fence_indented_within_list_item(self):
        self.assertEqual(len(extract_code_blocks(INTACT.format(i="    "))), 1)

    def test_fence_indented_within_nested_list_item(self):
        text = "- a:\n  - b:\n        ```\n        code\n        ```\n"
        self.assertEqual(len(extract_code_blocks(text)), 1)

    def test_tilde_fence_indented_within_list_item(self):
        text = "- step:\n    ~~~\n    code\n    ~~~\n"
        self.assertEqual(len(extract_code_blocks(text)), 1)


class TestValidateCodeBlocks(unittest.TestCase):
    def test_rewritten_block_is_rejected_at_every_indent(self):
        for indent in ("", "  ", "    ", "        "):
            with self.subTest(indent=len(indent)):
                result = ValidationResult()
                validate_code_blocks(
                    INTACT.format(i=indent), MANGLED.format(i=indent), result
                )
                self.assertFalse(result.is_valid)

    def test_untouched_block_passes(self):
        for indent in ("", "  ", "    ", "        "):
            with self.subTest(indent=len(indent)):
                result = ValidationResult()
                text = INTACT.format(i=indent)
                validate_code_blocks(text, text, result)
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