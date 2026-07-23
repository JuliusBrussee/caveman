"""Tests for mask_code()/unmask_code() (protect code by construction).

Code preservation during compression used to be enforced only by asking the
compression model nicely, then catching violations after the fact with the
byte-exact validator in validate.py. These tests pin the mechanical
alternative: fenced blocks and inline code are masked with opaque placeholder
tokens before the model ever sees them, so the model cannot mangle content it
never received, and splicing fails closed (raises, no partial output) if a
placeholder comes back missing, duplicated, or altered.
"""

import sys
import unittest
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(REPO_ROOT / "skills" / "caveman-compress"))

from scripts.compress import (  # noqa: E402
    PlaceholderIntegrityError,
    mask_code,
    unmask_code,
)

BODY = """# Title

Run `terraform plan` before `terraform apply`. See `docs/README.md` for context.

```yaml
key: value   # a comment the model likes to strip
list:
  - one
  - two
```

More inline: `kubectl rollout status deployment/x` and `--dry-run=client`.
"""


class MaskUnmaskRoundTripTests(unittest.TestCase):
    def test_round_trip_through_aggressive_rewrite(self):
        masked, fragments = mask_code(BODY)
        # Simulate an aggressive "compression" pass that rewrites all prose
        # (whitespace collapsed) but leaves placeholder tokens untouched, as
        # instructed in the compression prompt.
        rewritten = "\n".join(line.strip() for line in masked.strip().split("\n"))
        restored = unmask_code(rewritten, fragments)
        for fragment in fragments:
            self.assertIn(fragment, restored)

    def test_masking_produces_no_real_code_in_prompt_text(self):
        masked, fragments = mask_code(BODY)
        self.assertNotIn("terraform apply", masked)
        self.assertNotIn("kubectl rollout status", masked)
        self.assertGreater(len(fragments), 0)

    def test_inline_code_inside_fence_not_double_masked(self):
        # A backtick-looking span that lives inside a fenced block must be
        # masked once, as part of the fence, not a second time as "inline
        # code" -- fences are masked first so this can't happen.
        body = "```sh\necho `not inline`\n```\n"
        masked, fragments = mask_code(body)
        self.assertEqual(len(fragments), 1)
        self.assertEqual(fragments[0], "```sh\necho `not inline`\n```")


class UnmaskFailClosedTests(unittest.TestCase):
    def test_dropped_placeholder_raises(self):
        masked, fragments = mask_code(BODY)
        # Drop the line containing the first placeholder entirely.
        bad = "\n".join(
            line for line in masked.split("\n") if "CVMN-CODE-0" not in line
        )
        with self.assertRaises(PlaceholderIntegrityError):
            unmask_code(bad, fragments)

    def test_duplicated_placeholder_raises(self):
        masked, fragments = mask_code(BODY)
        bad = masked + "\n⟦CVMN-CODE-0⟧"
        with self.assertRaises(PlaceholderIntegrityError):
            unmask_code(bad, fragments)

    def test_no_placeholders_no_fragments_is_a_noop(self):
        # The collision-guard fallback path: no code masked, nothing to
        # splice back.
        self.assertEqual(unmask_code("plain text, no codes", []), "plain text, no codes")


if __name__ == "__main__":
    unittest.main()
