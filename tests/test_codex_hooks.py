import os
import subprocess
import tempfile
import unittest
from pathlib import Path


REPO_ROOT = Path(__file__).resolve().parent.parent


class CodexHookScriptTests(unittest.TestCase):
    def run_cmd(self, cmd, codex_home, *, env_extra=None, input_text=None):
        env = os.environ.copy()
        env["CODEX_HOME"] = str(codex_home)
        env["HOME"] = str(codex_home.parent)
        if env_extra:
            env.update(env_extra)
        return subprocess.run(
            cmd,
            cwd=REPO_ROOT,
            env=env,
            text=True,
            input=input_text,
            capture_output=True,
            check=True,
        )

    def test_activate_writes_flag_and_rules(self):
        with tempfile.TemporaryDirectory(prefix="caveman-codex-activate-") as tmp:
            codex_home = Path(tmp) / ".codex"
            result = self.run_cmd(
                ["node", "plugins/caveman/scripts/codex-caveman-activate.js"],
                codex_home,
            )

            self.assertIn("CAVEMAN MODE ACTIVE — level: full", result.stdout)
            self.assertIn("Switch: `$caveman lite|full|ultra`.", result.stdout)
            self.assertEqual((codex_home / ".caveman-active").read_text(), "full")

    def test_activate_off_mode_stays_silent(self):
        with tempfile.TemporaryDirectory(prefix="caveman-codex-off-") as tmp:
            codex_home = Path(tmp) / ".codex"
            result = self.run_cmd(
                ["node", "plugins/caveman/scripts/codex-caveman-activate.js"],
                codex_home,
                env_extra={"CAVEMAN_DEFAULT_MODE": "off"},
            )

            self.assertEqual(result.stdout, "")
            self.assertFalse((codex_home / ".caveman-active").exists())

    def test_mode_commands_record_ultra_and_wenyan(self):
        with tempfile.TemporaryDirectory(prefix="caveman-codex-modes-") as tmp:
            codex_home = Path(tmp) / ".codex"

            ultra = self.run_cmd(
                ["node", "plugins/caveman/scripts/codex-caveman-mode-tracker.js"],
                codex_home,
                input_text='{"prompt":"$caveman ultra"}',
            )
            self.assertIn("CAVEMAN MODE ACTIVE (ultra).", ultra.stdout)
            self.assertEqual((codex_home / ".caveman-active").read_text(), "ultra")

            wenyan = self.run_cmd(
                ["node", "plugins/caveman/scripts/codex-caveman-mode-tracker.js"],
                codex_home,
                input_text='{"prompt":"$caveman wenyan"}',
            )
            self.assertIn("CAVEMAN MODE ACTIVE (wenyan).", wenyan.stdout)
            self.assertEqual((codex_home / ".caveman-active").read_text(), "wenyan")

    def test_natural_language_deactivate_clears_flag(self):
        with tempfile.TemporaryDirectory(prefix="caveman-codex-stop-") as tmp:
            codex_home = Path(tmp) / ".codex"
            codex_home.mkdir(parents=True)
            (codex_home / ".caveman-active").write_text("full")

            result = self.run_cmd(
                ["node", "plugins/caveman/scripts/codex-caveman-mode-tracker.js"],
                codex_home,
                input_text='{"prompt":"normal mode"}',
            )

            self.assertEqual(result.stdout, "")
            self.assertFalse((codex_home / ".caveman-active").exists())

    def test_independent_modes_switch_without_reinforcement(self):
        with tempfile.TemporaryDirectory(prefix="caveman-codex-independent-") as tmp:
            codex_home = Path(tmp) / ".codex"

            commit = self.run_cmd(
                ["node", "plugins/caveman/scripts/codex-caveman-mode-tracker.js"],
                codex_home,
                input_text='{"prompt":"$caveman-commit"}',
            )
            self.assertEqual(commit.stdout, "")
            self.assertEqual((codex_home / ".caveman-active").read_text(), "commit")

            review = self.run_cmd(
                ["node", "plugins/caveman/scripts/codex-caveman-mode-tracker.js"],
                codex_home,
                input_text='{"prompt":"$caveman-review"}',
            )
            self.assertEqual(review.stdout, "")
            self.assertEqual((codex_home / ".caveman-active").read_text(), "review")

            compress = self.run_cmd(
                ["node", "plugins/caveman/scripts/codex-caveman-mode-tracker.js"],
                codex_home,
                input_text='{"prompt":"$caveman-compress CLAUDE.md"}',
            )
            self.assertEqual(compress.stdout, "")
            self.assertEqual((codex_home / ".caveman-active").read_text(), "compress")

    def test_help_is_one_shot_and_does_not_mutate_flag(self):
        with tempfile.TemporaryDirectory(prefix="caveman-codex-help-") as tmp:
            codex_home = Path(tmp) / ".codex"
            codex_home.mkdir(parents=True)
            (codex_home / ".caveman-active").write_text("full")

            result = self.run_cmd(
                ["node", "plugins/caveman/scripts/codex-caveman-mode-tracker.js"],
                codex_home,
                input_text='{"prompt":"$caveman-help"}',
            )

            self.assertEqual(result.stdout, "")
            self.assertEqual((codex_home / ".caveman-active").read_text(), "full")

    def test_reinforcement_only_for_core_modes(self):
        with tempfile.TemporaryDirectory(prefix="caveman-codex-reinforce-") as tmp:
            codex_home = Path(tmp) / ".codex"
            codex_home.mkdir(parents=True)

            (codex_home / ".caveman-active").write_text("full")
            reinforce = self.run_cmd(
                ["node", "plugins/caveman/scripts/codex-caveman-mode-tracker.js"],
                codex_home,
                input_text='{"prompt":"why rerender"}',
            )
            self.assertIn("CAVEMAN MODE ACTIVE (full).", reinforce.stdout)

            (codex_home / ".caveman-active").write_text("commit")
            no_reinforce = self.run_cmd(
                ["node", "plugins/caveman/scripts/codex-caveman-mode-tracker.js"],
                codex_home,
                input_text='{"prompt":"why rerender"}',
            )
            self.assertEqual(no_reinforce.stdout, "")


if __name__ == "__main__":
    unittest.main()
