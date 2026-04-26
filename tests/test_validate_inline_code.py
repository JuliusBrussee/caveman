"""Tests for the inline-code validation guard (issue #112).

The validator used to never inspect inline backtick spans, so a
compression run could rewrite `npm install` -> `yarn install` and still
print "Validation passed". These tests pin the new check.
"""

import sys
import tempfile
import unittest
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(REPO_ROOT))

from skills.compress.scripts.validate import (  # noqa: E402
    validate,
    extract_inline_code,
)


class ExtractInlineCodeTests(unittest.TestCase):
    def test_extracts_simple_inline(self):
        self.assertEqual(
            extract_inline_code("Run `npm install` then `yarn build`."),
            ["npm install", "yarn build"],
        )

    def test_skips_fenced_block_content(self):
        text = "Outside `keep`.\n\n```bash\necho `inside fence`\n```\n\nOutside `also`.\n"
        self.assertEqual(extract_inline_code(text), ["keep", "also"])

    def test_returns_empty_when_none(self):
        self.assertEqual(extract_inline_code("Just prose with no code."), [])


class ValidateInlineCodeTests(unittest.TestCase):
    def _validate(self, orig: str, comp: str):
        with tempfile.TemporaryDirectory() as tmp:
            o = Path(tmp) / "o.md"
            c = Path(tmp) / "c.md"
            o.write_text(orig)
            c.write_text(comp)
            return validate(o, c)

    def test_changed_inline_command_is_error(self):
        result = self._validate(
            "# T\nRun `npm install` and set `NODE_ENV=production`.\n",
            "# T\nRun `yarn install` and set `NODE_ENV=development`.\n",
        )
        self.assertFalse(result.is_valid)
        self.assertTrue(
            any("Inline code not preserved exactly" in e for e in result.errors),
            f"missing inline-code error in {result.errors}",
        )

    def test_unchanged_inline_passes(self):
        # Same inline tokens preserved; surrounding prose differs.
        result = self._validate(
            "# T\nRun `npm install` to install all dependencies.\n",
            "# T\nRun `npm install` to install deps.\n",
        )
        self.assertTrue(result.is_valid, f"unexpected errors: {result.errors}")

    def test_inline_inside_fence_does_not_trigger_false_positive(self):
        # Backticks inside a fenced block are validated by validate_code_blocks,
        # not by validate_inline_code. The block is identical between orig and
        # comp, so the inline check should see zero spans on both sides.
        block = "```bash\nrun `cmd a` then `cmd b`\n```"
        result = self._validate(
            f"# T\n\n{block}\n\nProse with `keep` token.\n",
            f"# T\n\n{block}\n\nShorter prose with `keep` token.\n",
        )
        self.assertTrue(result.is_valid, f"unexpected errors: {result.errors}")


if __name__ == "__main__":
    unittest.main()
