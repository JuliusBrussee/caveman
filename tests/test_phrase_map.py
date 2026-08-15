"""Tests for the deterministic wordy-phrase compression pre-pass.

See skills/caveman-compress/scripts/phrase_map.py's module docstring for
why this exists: multi-word phrases collapse to fewer tokens for free,
without needing an LLM call to decide it.
"""

import sys
import unittest
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(REPO_ROOT / "skills" / "caveman-compress"))

from scripts.phrase_map import (  # noqa: E402
    _compile_regex,
    _substitute_until_stable,
    apply_phrase_map,
)


class PhraseMapTests(unittest.TestCase):
    def test_known_phrase_replaced(self):
        text = "We skipped it due to the fact that the server was down."
        result = apply_phrase_map(text)
        self.assertIn("because", result)
        self.assertNotIn("due to the fact that", result)

    def test_case_preserved_at_sentence_start(self):
        text = "Due to the fact that it failed, we retried."
        result = apply_phrase_map(text)
        self.assertTrue(result.startswith("Because"))

    def test_lowercase_mid_sentence_preserved(self):
        text = "We retried in order to confirm the fix."
        result = apply_phrase_map(text)
        self.assertIn(" to confirm", result)

    def test_longest_phrase_wins_over_shorter_overlap(self):
        # "in order to" must match before a shorter phrase that could also
        # match a prefix of it, so the whole phrase collapses to "to", not
        # a partial replacement leaving a dangling fragment.
        text = "in order to ship this"
        result = apply_phrase_map(text)
        self.assertEqual(result, "to ship this")

    def test_fenced_code_block_untouched(self):
        text = "Explain why.\n\n```\n# due to the fact that x, do y\n```\n"
        result = apply_phrase_map(text)
        self.assertIn("due to the fact that", result)

    def test_inline_code_span_untouched(self):
        text = "Run `in order to` literally as a shell alias name."
        result = apply_phrase_map(text)
        self.assertIn("`in order to`", result)

    def test_prose_outside_code_still_replaced_when_code_present(self):
        text = "In order to build, run `make in order to build` first."
        result = apply_phrase_map(text)
        # Prose phrase before the code span is replaced...
        self.assertTrue(result.startswith("To build"))
        # ...but the identical phrase inside the code span survives.
        self.assertIn("`make in order to build`", result)

    def test_no_match_returns_unchanged_text(self):
        text = "Nothing here should change at all."
        self.assertEqual(apply_phrase_map(text), text)

    def test_empty_string(self):
        self.assertEqual(apply_phrase_map(""), "")

    def test_short_phrase_does_not_match_mid_word(self):
        # "point in time" is a real PHRASE_MAP entry (-> "moment"). Without
        # a word-boundary anchor, the literal substring "point in time" also
        # occurs mid-word inside "checkpoint in time-series data" (the "point"
        # in "checkpoint" is not a separate word). This must NOT be replaced —
        # doing so would mangle "checkpoint" into a broken word.
        text = "Snapshot the checkpoint in time-series data before restart."
        result = apply_phrase_map(text)
        self.assertIn("checkpoint in time-series", result)

    def test_boundary_anchor_still_matches_real_word_boundaries(self):
        # The fix for the mid-word false-match above must not cost real,
        # properly word-bounded matches elsewhere in the same dictionary.
        text = "We'll ship it at some point in time next quarter."
        result = apply_phrase_map(text)
        self.assertIn("moment", result)

    def test_recursive_pass_catches_chained_compression(self):
        # A single pass only catches phrases present in the ORIGINAL text.
        # This proves the loop in _substitute_until_stable also catches a
        # phrase that only exists after an earlier replacement created it —
        # using a synthetic map so the test doesn't depend on whether any
        # real PHRASE_MAP entries happen to chain (most don't, by design).
        synthetic_map = {"alpha beta": "gamma delta", "gamma delta": "epsilon"}
        regex = _compile_regex(synthetic_map)
        result = _substitute_until_stable("alpha beta", synthetic_map, regex)
        self.assertEqual(result, "epsilon")

    def test_single_pass_alone_would_not_catch_the_chain(self):
        # Companion to the test above: confirms the chain genuinely requires
        # a second pass, so the recursive test isn't accidentally passing
        # for an unrelated reason (e.g. a direct "alpha beta" -> "epsilon"
        # entry existing).
        from scripts.phrase_map import _substitute_once

        synthetic_map = {"alpha beta": "gamma delta", "gamma delta": "epsilon"}
        regex = _compile_regex(synthetic_map)
        one_pass = _substitute_once("alpha beta", synthetic_map, regex)
        self.assertEqual(one_pass, "gamma delta")
        self.assertNotEqual(one_pass, "epsilon")

    def test_leading_in_does_not_produce_doubled_preposition(self):
        # "close proximity to" -> "near" is correct on its own, but text
        # commonly reads "in close proximity to" — collapsing only the
        # shorter phrase would leave a dangling "in" in front of "near",
        # producing the broken "in near the office". The longer variant
        # must win (longest-first ordering) and consume the leading "in" too.
        text = "The warehouse is in close proximity to the depot."
        result = apply_phrase_map(text)
        self.assertIn("is near the depot", result)
        self.assertNotIn("in near", result)

    def test_measurable_character_reduction_on_wordy_prose(self):
        # Character count is a dependency-free stand-in for "did this shrink
        # the text at all" — every replacement in PHRASE_MAP is strictly
        # shorter than its phrase, so any match must reduce length.
        text = (
            "We paused the rollout due to the fact that error rates spiked. "
            "In order to recover, we rolled back prior to the next deploy "
            "window. With regard to root cause, a large number of requests "
            "hit a stale cache."
        )
        result = apply_phrase_map(text)
        self.assertLess(len(result), len(text))


class PhraseMapTokenReductionTests(unittest.TestCase):
    """Real token-count comparison using the same tokenizer evals/measure.py
    uses. tiktoken is an optional dependency (see CONTRIBUTING.md's `uv run
    --with tiktoken` invocations) so this skips cleanly in the plain
    `python3 -m unittest` CI run rather than failing on import.
    """

    @classmethod
    def setUpClass(cls):
        try:
            import tiktoken
        except ImportError:
            raise unittest.SkipTest("tiktoken not installed — run via `uv run --with tiktoken`")
        cls.encoding = tiktoken.get_encoding("o200k_base")

    def _count(self, text: str) -> int:
        return len(self.encoding.encode(text))

    def test_token_count_drops_on_wordy_prose(self):
        text = (
            "Due to the fact that the API was down, we retried in order to "
            "confirm the fix. With regard to the root cause, a large number "
            "of requests had timed out prior to the deploy, and at this "
            "point in time we believe it is possible that a stale cache "
            "entry was to blame."
        )
        before = self._count(text)
        after = self._count(apply_phrase_map(text))
        self.assertLess(after, before)

    def test_fixture_files_show_real_reduction(self):
        # Run the phrase map over the repo's own compression test fixtures
        # (real prose, not a contrived sample) so the reduction number is
        # grounded in actual files rather than a cherry-picked string.
        fixtures_dir = REPO_ROOT / "tests" / "caveman-compress"
        originals = sorted(fixtures_dir.glob("*.original.md"))
        self.assertGreater(len(originals), 0, "expected fixture files to exist")

        total_before = 0
        total_after = 0
        for path in originals:
            text = path.read_text(encoding="utf-8")
            total_before += self._count(text)
            total_after += self._count(apply_phrase_map(text))

        self.assertLessEqual(total_after, total_before)


if __name__ == "__main__":
    unittest.main()
