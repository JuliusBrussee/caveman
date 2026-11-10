import unittest
import sys
from pathlib import Path
sys.path.insert(0, str(Path(__file__).resolve().parent.parent / "skills" / "caveman-compress"))
from scripts.validate import ValidationResult, validate_polarity

class TestValidatePolarity(unittest.TestCase):
    """Structural validation passes an inverted rule; this check is what stops
    a compressed CLAUDE.md from telling the agent the opposite thing."""

    def check(self, orig, comp):
        result = ValidationResult()
        validate_polarity(orig, comp, result)
        return result

    def test_dropped_prohibition_is_an_error(self):
        r = self.check(
            "## Testing\n\nNever mock the database in integration tests.\n",
            "## Testing\n\nMock database in integration tests.\n",
        )
        self.assertFalse(r.is_valid)
        self.assertIn("Negation lost", r.errors[0])

    def test_dropped_korean_prohibition_is_an_error(self):
        r = self.check(
            "## 커밋\n\n`.env` 파일은 절대 커밋 금지.\n",
            "## 커밋\n\n`.env` 커밋.\n",
        )
        self.assertFalse(r.is_valid)

    def test_rephrased_negation_passes(self):
        # "Please don't use any" -> "No `any`" is a legitimate compression.
        r = self.check(
            "## Style\n\nPlease don't use the any type.\n",
            "## Style\n\nNo any type.\n",
        )
        self.assertTrue(r.is_valid)

    def test_incidental_not_does_not_trigger(self):
        # Descriptive "not", no rule attached — dropping it is free.
        r = self.check(
            "## Notes\n\nAll other fields are optional and use defaults if not provided.\n",
            "## Notes\n\nOptional fields use defaults.\n",
        )
        self.assertTrue(r.is_valid)

    def test_negation_inside_code_does_not_count(self):
        # Code is preserved verbatim, so a `--no-verify` in a fence must not
        # satisfy a prose prohibition that got dropped.
        r = self.check(
            "## Hooks\n\nNever bypass the pre-commit hook.\n\n```\ngit commit --no-verify\n```\n",
            "## Hooks\n\nBypass pre-commit hook.\n\n```\ngit commit --no-verify\n```\n",
        )
        self.assertFalse(r.is_valid)

    def test_lost_constraint_words_are_a_warning_not_an_error(self):
        r = self.check(
            "## Order\n\nAlways run the migration before the deploy.\n",
            "## Order\n\n마이그레이션 실행, 배포.\n",
        )
        self.assertTrue(r.is_valid)
        self.assertTrue(any("Constraint words lost" in w for w in r.warnings))



if __name__ == "__main__":
    unittest.main()
