"""Tests for caveman-error skill discovery and registration."""

import re
import unittest
from pathlib import Path


REPO_ROOT = Path(__file__).resolve().parent.parent


class CavemanErrorRegistrationTests(unittest.TestCase):
    """Verify caveman-error skill is properly registered and discoverable."""

    def test_skill_file_exists(self):
        """SKILL.md must exist at expected location."""
        skill_path = REPO_ROOT / "skills" / "caveman-error" / "SKILL.md"
        self.assertTrue(skill_path.exists(), f"Skill file not found: {skill_path}")

    def test_skill_has_required_sections(self):
        """SKILL.md must have frontmatter, rules, examples, boundaries."""
        skill_path = REPO_ROOT / "skills" / "caveman-error" / "SKILL.md"
        content = skill_path.read_text()

        # Required sections
        self.assertIn("name: caveman-error", content, "Missing skill name in frontmatter")
        self.assertIn("description:", content, "Missing description in frontmatter")
        self.assertIn("## Rules", content, "Missing Rules section")
        self.assertIn("## Examples", content, "Missing Examples section")
        self.assertIn("## Boundaries", content, "Missing Boundaries section")

    def test_command_file_exists(self):
        """Command TOML must exist for Gemini CLI."""
        cmd_path = REPO_ROOT / "commands" / "caveman-error.toml"
        self.assertTrue(cmd_path.exists(), f"Command file not found: {cmd_path}")

    def test_command_has_required_fields(self):
        """Command TOML must have description and prompt."""
        cmd_path = REPO_ROOT / "commands" / "caveman-error.toml"
        content = cmd_path.read_text()

        self.assertIn("description", content, "Missing description in command file")
        self.assertIn("prompt", content, "Missing prompt in command file")

    def test_referenced_in_agents_md(self):
        """Skill must be listed in AGENTS.md for agent context loading."""
        agents_path = REPO_ROOT / "AGENTS.md"
        content = agents_path.read_text()

        self.assertIn("caveman-error/SKILL.md", content, "Not referenced in AGENTS.md")

    def test_referenced_in_gemini_md(self):
        """Skill must be listed in GEMINI.md for Gemini CLI context."""
        gemini_path = REPO_ROOT / "GEMINI.md"
        content = gemini_path.read_text()

        self.assertIn("caveman-error/SKILL.md", content, "Not referenced in GEMINI.md")

    def test_skill_output_format_examples(self):
        """Examples in SKILL.md must follow output format pattern."""
        skill_path = REPO_ROOT / "skills" / "caveman-error" / "SKILL.md"
        content = skill_path.read_text()

        # Count lines that start with arrow + backtick (output examples)
        # Arrow char: → (Unicode U+2192)
        arrow_lines = [line for line in content.split('\n') if line.strip().startswith('→ `')]
        matches = [line.strip()[2:].strip().strip('`') for line in arrow_lines]

        self.assertTrue(len(matches) >= 3, f"Expected at least 3 examples, found {len(matches)}")

        for example in matches:
            # Must have :L<digits>: pattern
            self.assertRegex(example, r":L\d+:", f"Example missing :L<line>: pattern: {example}")
            # Must have Fix: suggestion
            self.assertIn("Fix:", example, f"Example missing Fix: suggestion: {example}")


if __name__ == "__main__":
    unittest.main()
