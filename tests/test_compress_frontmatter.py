"""Body-only compression and repair preserve locally owned frontmatter."""
import contextlib
import io
import os
from pathlib import Path
import sys
import tempfile
import unittest
from unittest import mock

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / "skills/caveman-compress"))
from scripts import compress

FM = "---\nname: task\nupdated: 2026-09-30\n---\n"
BODY = "# Rules\n\n" + "Keep the operational guidance clear and preserve the technical facts. " * 40 + "\nRead https://example.com/rules.\n"
GOOD = "# Rules\n\nKeep guidance. Read https://example.com/rules.\n"
BAD = "# Rules\n\nKeep guidance.\n"
EXTRA = "---\nupdated: 2026-10-21\n---\n"
PREFACE = "Here is the compressed version of your file:\n"
TRAILER = "\n\n---\n\nUntouched: the shared/skills/ directory structure. Risk: compression removed polite filler but preserved all instructions intact.\n"


class BodyOnlyTests(unittest.TestCase):
    def run_reply(self, original, replies, accepted, expected=None):
        with tempfile.TemporaryDirectory() as tmp, tempfile.TemporaryDirectory() as data:
            path = Path(tmp) / "task.md"
            path.write_bytes(original.encode())
            with mock.patch.dict(os.environ, {"XDG_DATA_HOME":data, "LOCALAPPDATA":data}), mock.patch.object(compress, "call_claude", side_effect=replies) as call, contextlib.redirect_stdout(io.StringIO()):
                result = compress.compress_file(path)
                self.assertEqual(result, accepted)
                if accepted:
                    self.assertEqual(path.read_bytes(), expected.encode())
                else:
                    self.assertEqual(path.read_bytes(), original.encode())
                    backup = compress.backup_dir_for(path.resolve()) / "task.original.md"
                    self.assertFalse(backup.exists())
                self.assertFalse(path.with_name("task.md.caveman-staged").exists())
                return [entry.args[0] for entry in call.call_args_list]

    def test_first_pass_commentary_and_frontmatter_rejected(self):
        self.run_reply(FM + BODY, [PREFACE + EXTRA + GOOD], False)

    def test_first_pass_frontmatter_only_rejected(self):
        self.run_reply(FM + BODY, [EXTRA + GOOD], False)

    def test_fix_frontmatter_and_commentary_rejected(self):
        self.run_reply(FM + BODY, [BAD, FM + PREFACE + EXTRA + GOOD], False)

    def test_fix_heading_preface_rejected(self):
        # PASS_BOTH: the old whole-file frontmatter anchor also rejects this.
        self.run_reply(FM + BODY, [BAD, PREFACE + GOOD], False)

    def test_fix_prompt_and_reply_are_body_only(self):
        prompts = self.run_reply(FM + BODY, [BAD, GOOD], True, FM + GOOD)
        self.assertEqual(len(prompts), 2)
        for prompt in prompts:
            self.assertNotIn("name: task", prompt)
            self.assertNotIn("updated: 2026-09-30", prompt)
        self.assertIn("You are fixing", prompts[1])

    def test_heading_preface_without_frontmatter_rejected(self):
        self.run_reply(BODY, [PREFACE + GOOD], False)

    def test_plain_body_invented_frontmatter_rejected(self):
        original = "Keep the operational guidance clear. " * 40
        self.run_reply(original, [PREFACE + EXTRA + "Keep guidance."], False)

    def test_clean_first_pass(self):
        self.run_reply(FM + BODY, [GOOD], True, FM + GOOD)

    def test_thematic_breaks_are_not_frontmatter(self):
        section = "\n---\n\nOrdinary prose between thematic breaks.\n\n---\n\n"
        self.run_reply(FM + BODY + section, [GOOD + section], True, FM + GOOD + section)

    def test_fenced_yaml_is_not_frontmatter(self):
        code = "```yaml\n---\nkey: value\n---\n```\n"
        masked, _ = compress.mask_code_blocks(BODY + code)
        reply = masked.replace(BODY, GOOD)
        self.run_reply(FM + BODY + code, [reply], True, FM + GOOD + code)

    def test_crlf_frontmatter_preserved(self):
        self.run_reply((FM + BODY).replace("\n", "\r\n"), [GOOD], True, (FM + GOOD).replace("\n", "\r\n"))

    def test_plain_prose_can_change_its_first_line(self):
        self.run_reply("Keep the operational guidance clear. " * 40, ["Keep guidance."], True, "Keep guidance.")

    def test_existing_mapping_block_in_body_preserved(self):
        section = "\n---\nkey: value\n---\n"
        self.run_reply(FM + BODY + section, [GOOD + section], True, FM + GOOD + section)

    def test_code_first_preface_rejected(self):
        code = "```sh\necho exact\n```\n"
        original = code + BODY
        masked, _ = compress.mask_code_blocks(original)
        self.run_reply(original, [PREFACE + masked.replace(BODY, GOOD)], False)

    def test_plain_body_invented_list_frontmatter_rejected(self):
        original = "Keep the operational guidance clear. " * 40
        reply = "---\nname: task\ntags:\n  - one\n---\nKeep guidance."
        self.run_reply(original, [reply], False)

    def test_colon_prose_between_thematic_breaks_is_preserved(self):
        section = "\n---\n\nUse this command: keep it clear.\n\n---\n\n"
        original_section = section.replace("Use this command: keep it clear.", "Use this command to keep it clear.")
        self.run_reply(FM + BODY + original_section, [GOOD + section], True, FM + GOOD + section)

    def test_plain_body_fix_cannot_return_frontmatter(self):
        original = EXTRA + "Keep the operational guidance clear. " * 40 + "\nRead https://example.com/rules.\n"
        self.run_reply(original, ["Keep guidance.", EXTRA + "Keep guidance. Read https://example.com/rules."], False)

    def test_first_pass_trailing_commentary_rejected(self):
        # #1433: the body guard anchored the head only, so an appended
        # "Untouched: ... Risk: ..." trailer under a new thematic break was
        # written into the file with "Validation passed".
        self.run_reply(FM + BODY, [GOOD + TRAILER], False)

    def test_plain_body_trailing_commentary_rejected(self):
        # Same leak on a file with no frontmatter to anchor against.
        self.run_reply(BODY, [GOOD + TRAILER], False)

    def test_fix_trailing_commentary_rejected(self):
        # The repair pass shares the guard, so it must refuse the trailer too.
        self.run_reply(FM + BODY, [BAD, GOOD + TRAILER], False)

    def test_trailing_commentary_under_asterisk_break_rejected(self):
        # A model that introduces the trailer under *** instead of --- is the
        # same appended commentary and must not slip past the counter.
        self.run_reply(FM + BODY, [GOOD + TRAILER.replace("---", "***")], False)

    def test_crlf_trailing_commentary_rejected(self):
        # Windows parity: splitlines() drops the \r, so the CRLF trailer must be
        # caught by the same counter rather than slipping past it.
        crlf = (FM + BODY).replace("\n", "\r\n")
        self.run_reply(crlf, [(GOOD + TRAILER).replace("\n", "\r\n")], False)

    def test_break_carried_over_from_the_original_still_passes(self):
        # The guard refuses an *added* break only. A break the original already
        # had keeps the count equal and must not cost a false rejection.
        section = "\n---\n\nSection two prose here.\n"
        self.run_reply(
            FM + BODY + section, [GOOD + "\n---\n\nSection two.\n"],
            True, FM + GOOD + "\n---\n\nSection two.\n",
        )

    def test_plain_body_fix_duplicate_frontmatter_rejected(self):
        original = EXTRA + "Keep the operational guidance clear. " * 40 + "\nRead https://example.com/rules.\n"
        repair = EXTRA + "---\nupdated: 2026-10-22\n---\nKeep guidance. Read https://example.com/rules."
        self.run_reply(original, ["Keep guidance.", repair], False)


class CompressPromptRuleTests(unittest.TestCase):
    """The rules that reach the provider, which is the only place they act.

    SKILL.md's prose rules are never sent to the provider (#1355), so a rule
    that is not in build_compress_prompt does not exist for the model.
    """

    def setUp(self):
        self.prompt = compress.build_compress_prompt("# Title\n\nSome prose.\n")

    def test_prompt_forbids_appended_trailer(self):
        # #1433
        self.assertIn("Output ends where the input ends", self.prompt)

    def test_prompt_keeps_the_instruction_verb(self):
        # #1434
        self.assertIn("Keep the verb that carries each instruction", self.prompt)
        self.assertIn("a verbless noun phrase is not", self.prompt)

    def test_rules_are_inside_the_strict_rules_block(self):
        # A rule after the "TEXT:" payload boundary is advice about the text,
        # not a constraint on the reply.
        rules, _, text = self.prompt.partition("\nTEXT:")
        self.assertIn("Output ends where the input ends", rules)
        self.assertIn("Keep the verb that carries each instruction", rules)
        self.assertNotIn("Keep the verb that carries each instruction", text)


if __name__ == "__main__":
    unittest.main()
