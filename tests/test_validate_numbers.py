"""#1350: validation checked structure only, so a changed number passed.

`skills/caveman-compress/SKILL.md` lists "Dates, version numbers, numeric
values" under "Preserve EXACTLY (never modify)", and the skill README repeats
it under "What Is Preserved". Nothing enforced it: every heading, code block,
URL, path and inline span could be intact while `at most 3 attempts` came back
as `max 5 attempts`, and `validate.py` reported "Validation passed".

The check is deliberately ONE-DIRECTIONAL — a numeric value in the compressed
text that does not occur in the original is an error. Compression is allowed to
drop prose, so a number that disappears with its sentence is not flagged; what
it may never do is invent or alter a value, which is the gap #1350 reports.
"""

import sys
import tempfile
import unittest
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(REPO_ROOT / "skills" / "caveman-compress"))

from scripts.validate import extract_numbers, spelled_numbers, validate  # noqa: E402


class TestExtractNumbers(unittest.TestCase):
    def test_plain_integers(self):
        self.assertEqual(extract_numbers("retry at most 3 attempts"), {"3"})

    def test_versions_and_decimals(self):
        self.assertEqual(extract_numbers("caveman 4.0.1 cuts 0.15 of it"), {"4.0.1", "0.15"})

    def test_dates_split_into_their_parts(self):
        # Each component is a value of its own; preserving them individually is
        # what keeps 2026-11-17 from being rewritten as 2026-11-18.
        self.assertEqual(extract_numbers("shipped 2026-11-17"), {"2026", "11", "17"})

    def test_percentages_keep_only_the_number(self):
        self.assertEqual(extract_numbers("cuts 65% of tokens"), {"65"})

    def test_unit_suffix_keeps_the_value(self):
        # The fixture pair writes "a maximum of 60 seconds" as "Max wait: 60s".
        # A bare trailing guard let backtracking read that as 6.
        self.assertEqual(extract_numbers("Max wait: 60s"), {"60"})
        self.assertEqual(extract_numbers("5ms, 2x, 64bit"), {"5", "2", "64"})

    def test_ordered_list_markers_are_not_values(self):
        # The 1 numbers the item; it states nothing. Counting it would make
        # "write these bullets as a numbered list" an invented-number error.
        self.assertEqual(extract_numbers("1. first step\n2. second step\n"), set())

    def test_ordered_marker_does_not_hide_a_real_value(self):
        self.assertEqual(extract_numbers("1. retry 3 times\n"), {"3"})

    def test_digits_inside_identifiers_are_not_values(self):
        # The committed fixture pair compresses "accessibility" to "a11y";
        # reading that 11 as a value reported an invented number.
        self.assertEqual(extract_numbers("Radix for a11y, utf8 output, sha256 sum"), set())

    def test_identifier_digits_do_not_hide_a_neighbouring_value(self):
        self.assertEqual(extract_numbers("a11y needs 2 passes"), {"2"})

    def test_version_prefix_still_reads_as_a_value(self):
        self.assertEqual(extract_numbers("ship v4.0.1 now"), {"4.0.1"})

    def test_version_prefix_only_at_a_word_start(self):
        self.assertEqual(extract_numbers("rev2 of the doc"), set())

    def test_spelled_out_numbers_may_become_digits(self):
        # Compression that preserves the value: the fixture turns "about two
        # years" into "2 years" and "three small PRs" into "3 small".
        orig = "We waited about two years and merged three small PRs for one feature.\n"
        self.assertTrue({"2", "3", "1"} <= (extract_numbers(orig) | spelled_numbers(orig)))

    def test_thousands_separators_normalize(self):
        # Rewriting 1,000 as 1000 preserves the value, so it must not be an
        # error: flagging pure formatting would add noise and protect nothing.
        self.assertEqual(extract_numbers("1,000 tokens"), extract_numbers("1000 tokens"))


class TestValidateNumbers(unittest.TestCase):
    def _validate(self, orig, comp):
        with tempfile.TemporaryDirectory() as d:
            o = Path(d) / "orig.md"
            c = Path(d) / "comp.md"
            o.write_text(orig, encoding="utf-8")
            c.write_text(comp, encoding="utf-8")
            return validate(o, c)

    def test_changed_number_fails(self):
        """The exact case #1350 reports: structure intact, value altered."""
        orig = "# Retry\n\n- retry the request at most 3 attempts before giving up\n"
        comp = "# Retry\n\n- retry request max 5 attempts\n"
        result = self._validate(orig, comp)
        self.assertFalse(result.is_valid)
        self.assertTrue(any("5" in e for e in result.errors), result.errors)

    def test_changed_version_fails(self):
        orig = "# Install\n\nThis needs caveman 4.0.1 or newer to run correctly.\n"
        comp = "# Install\n\nNeed caveman 4.0.2 or newer.\n"
        self.assertFalse(self._validate(orig, comp).is_valid)

    def test_unchanged_numbers_pass(self):
        orig = "# Retry\n\n- retry the request at most 3 attempts before giving up\n"
        comp = "# Retry\n\n- retry request max 3 attempts\n"
        self.assertTrue(self._validate(orig, comp).is_valid, self._validate(orig, comp).errors)

    def test_dropped_number_passes(self):
        """Compression may drop prose; only inventing a value is an error."""
        orig = "# Limits\n\nThe cache holds 50 entries. Eviction is least recently used.\n"
        comp = "# Limits\n\nCache evict least recently used.\n"
        result = self._validate(orig, comp)
        self.assertTrue(result.is_valid, result.errors)

    def test_reformatted_thousands_passes(self):
        orig = "# Budget\n\nThe prompt budget is 1,000 tokens for each request made.\n"
        comp = "# Budget\n\nPrompt budget 1000 token per request.\n"
        result = self._validate(orig, comp)
        self.assertTrue(result.is_valid, result.errors)


if __name__ == "__main__":
    unittest.main()
