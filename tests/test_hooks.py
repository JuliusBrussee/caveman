import json
import os
import subprocess
import tempfile
import unittest
from pathlib import Path


REPO_ROOT = Path(__file__).resolve().parent.parent


class HookScriptTests(unittest.TestCase):
    def run_cmd(self, cmd, home):
        env = os.environ.copy()
        env["HOME"] = str(home)
        env["USERPROFILE"] = str(home)
        return subprocess.run(
            cmd,
            cwd=REPO_ROOT,
            env=env,
            text=True,
            capture_output=True,
            check=True,
        )

    def test_install_upgrades_old_two_file_install(self):
        with tempfile.TemporaryDirectory(prefix="caveman-hooks-upgrade-") as tmp:
            home = Path(tmp)
            hooks_dir = home / ".claude" / "hooks"
            hooks_dir.mkdir(parents=True)
            (home / ".claude" / "settings.json").write_text("{}\n")
            (hooks_dir / "caveman-activate.js").write_text("")
            (hooks_dir / "caveman-mode-tracker.js").write_text("")

            self.run_cmd(["bash", "hooks/install.sh"], home)

            statusline = hooks_dir / "caveman-statusline.sh"
            self.assertTrue(statusline.exists(), "upgrade should install statusline script")

            settings = json.loads((home / ".claude" / "settings.json").read_text())
            self.assertIn("statusLine", settings)
            self.assertIn(str(statusline), settings["statusLine"]["command"])

    def test_install_reconfigures_missing_statusline(self):
        with tempfile.TemporaryDirectory(prefix="caveman-hooks-statusline-") as tmp:
            home = Path(tmp)
            claude_dir = home / ".claude"
            hooks_dir = claude_dir / "hooks"
            hooks_dir.mkdir(parents=True)

            for name in ("caveman-activate.js", "caveman-mode-tracker.js", "caveman-statusline.sh"):
                (hooks_dir / name).write_text("")

            settings = {
                "hooks": {
                    "SessionStart": [
                        {
                            "hooks": [
                                {
                                    "type": "command",
                                    "command": f'node "{hooks_dir / "caveman-activate.js"}"',
                                }
                            ]
                        }
                    ],
                    "UserPromptSubmit": [
                        {
                            "hooks": [
                                {
                                    "type": "command",
                                    "command": f'node "{hooks_dir / "caveman-mode-tracker.js"}"',
                                }
                            ]
                        }
                    ],
                }
            }
            (claude_dir / "settings.json").write_text(json.dumps(settings, indent=2) + "\n")

            result = self.run_cmd(["bash", "hooks/install.sh"], home)

            self.assertNotIn("Nothing to do", result.stdout)

            updated = json.loads((claude_dir / "settings.json").read_text())
            self.assertIn("statusLine", updated)
            self.assertIn(str(hooks_dir / "caveman-statusline.sh"), updated["statusLine"]["command"])

    def test_uninstall_preserves_custom_statusline(self):
        with tempfile.TemporaryDirectory(prefix="caveman-hooks-uninstall-") as tmp:
            home = Path(tmp)
            claude_dir = home / ".claude"
            hooks_dir = claude_dir / "hooks"
            hooks_dir.mkdir(parents=True)

            for name in ("caveman-activate.js", "caveman-mode-tracker.js", "caveman-statusline.sh"):
                (hooks_dir / name).write_text("")

            settings = {
                "statusLine": {
                    "type": "command",
                    "command": "bash /tmp/custom-status-with-caveman.sh",
                },
                "hooks": {
                    "SessionStart": [
                        {
                            "hooks": [
                                {
                                    "type": "command",
                                    "command": f'node "{hooks_dir / "caveman-activate.js"}"',
                                }
                            ]
                        }
                    ],
                    "UserPromptSubmit": [
                        {
                            "hooks": [
                                {
                                    "type": "command",
                                    "command": f'node "{hooks_dir / "caveman-mode-tracker.js"}"',
                                }
                            ]
                        }
                    ],
                },
            }
            (claude_dir / "settings.json").write_text(json.dumps(settings, indent=2) + "\n")

            self.run_cmd(["bash", "hooks/uninstall.sh"], home)

            updated = json.loads((claude_dir / "settings.json").read_text())
            self.assertEqual(
                updated["statusLine"]["command"],
                "bash /tmp/custom-status-with-caveman.sh",
            )
            self.assertNotIn("hooks", updated)

    def test_activate_does_not_nudge_when_custom_statusline_exists(self):
        with tempfile.TemporaryDirectory(prefix="caveman-hooks-activate-") as tmp:
            home = Path(tmp)
            claude_dir = home / ".claude"
            claude_dir.mkdir(parents=True)
            (claude_dir / "settings.json").write_text(
                json.dumps(
                    {
                        "statusLine": {
                            "type": "command",
                            "command": "bash /tmp/my-statusline.sh",
                        }
                    }
                )
                + "\n"
            )

            result = self.run_cmd(["node", "hooks/caveman-activate.js"], home)

            payload = json.loads(result.stdout)
            ctx = payload["hookSpecificOutput"]["additionalContext"]
            self.assertNotIn("STATUSLINE SETUP NEEDED", ctx)
            self.assertEqual((claude_dir / ".caveman-active").read_text(), "full")


class HookOutputShapeTests(unittest.TestCase):
    """Shape tests for the SessionStart + UserPromptSubmit hook outputs.

    These exist because the persistence fix depends on Claude Code receiving
    structured `hookSpecificOutput` payloads with level-specific reinforcement
    on every turn. A regression that silently drops back to plain stdout or to
    the old name-tag reinforcement would reintroduce the drift the user
    reported.
    """

    def _run(self, cmd, home, stdin=None, extra_env=None):
        env = os.environ.copy()
        env["HOME"] = str(home)
        env["USERPROFILE"] = str(home)
        if extra_env:
            env.update(extra_env)
        return subprocess.run(
            cmd,
            cwd=REPO_ROOT,
            env=env,
            input=stdin,
            text=True,
            capture_output=True,
            check=True,
        )

    def test_session_start_emits_canonical_hookSpecificOutput_json(self):
        with tempfile.TemporaryDirectory(prefix="caveman-sessionstart-json-") as tmp:
            home = Path(tmp)
            (home / ".claude").mkdir(parents=True)

            result = self._run(["node", "hooks/caveman-activate.js"], home)
            payload = json.loads(result.stdout)

            self.assertIn("hookSpecificOutput", payload)
            self.assertEqual(
                payload["hookSpecificOutput"]["hookEventName"], "SessionStart"
            )
            ctx = payload["hookSpecificOutput"]["additionalContext"]
            self.assertIn("CAVEMAN MODE ACTIVE", ctx)
            self.assertIn("level: full", ctx)

    def test_session_start_off_mode_still_emits_plain_ok(self):
        # The 'off' path is a no-op signal — must not be wrapped in JSON,
        # otherwise Claude Code injects an empty caveman ruleset as context.
        with tempfile.TemporaryDirectory(prefix="caveman-sessionstart-off-") as tmp:
            home = Path(tmp)
            (home / ".claude").mkdir(parents=True)

            result = self._run(
                ["node", "hooks/caveman-activate.js"],
                home,
                extra_env={"CAVEMAN_DEFAULT_MODE": "off"},
            )
            self.assertEqual(result.stdout.strip(), "OK")
            self.assertFalse((home / ".claude" / ".caveman-active").exists())

    def test_user_prompt_submit_reinforcement_includes_level_rules(self):
        with tempfile.TemporaryDirectory(prefix="caveman-reinforce-level-") as tmp:
            home = Path(tmp)
            claude_dir = home / ".claude"
            claude_dir.mkdir(parents=True)
            (claude_dir / ".caveman-active").write_text("ultra")

            result = self._run(
                ["node", "hooks/caveman-mode-tracker.js"],
                home,
                stdin=json.dumps({"prompt": "any prompt"}),
            )
            payload = json.loads(result.stdout)
            ctx = payload["hookSpecificOutput"]["additionalContext"]

            self.assertIn("level: ultra", ctx)
            # ultra row in SKILL.md mentions "Abbreviate" and uses "→" for causality
            self.assertTrue(
                ("Abbreviate" in ctx) or ("→" in ctx),
                f"reinforcement should include ultra-level signature, got: {ctx!r}",
            )

    def test_user_prompt_submit_includes_plan_mode_clause(self):
        # Plan mode is the headline regression the fix addresses; the
        # reinforcement must explicitly cover it on every turn.
        with tempfile.TemporaryDirectory(prefix="caveman-reinforce-plan-") as tmp:
            home = Path(tmp)
            claude_dir = home / ".claude"
            claude_dir.mkdir(parents=True)
            (claude_dir / ".caveman-active").write_text("full")

            result = self._run(
                ["node", "hooks/caveman-mode-tracker.js"],
                home,
                stdin=json.dumps({"prompt": "any prompt"}),
            )
            payload = json.loads(result.stdout)
            ctx = payload["hookSpecificOutput"]["additionalContext"]
            self.assertIn("Plan mode", ctx)

    def test_user_prompt_submit_skipped_for_independent_modes(self):
        with tempfile.TemporaryDirectory(prefix="caveman-reinforce-indep-") as tmp:
            home = Path(tmp)
            claude_dir = home / ".claude"
            claude_dir.mkdir(parents=True)
            (claude_dir / ".caveman-active").write_text("commit")

            result = self._run(
                ["node", "hooks/caveman-mode-tracker.js"],
                home,
                stdin=json.dumps({"prompt": "any prompt"}),
            )
            self.assertEqual(result.stdout, "")

    def test_user_prompt_submit_skipped_when_no_flag(self):
        with tempfile.TemporaryDirectory(prefix="caveman-reinforce-noflag-") as tmp:
            home = Path(tmp)
            (home / ".claude").mkdir(parents=True)

            result = self._run(
                ["node", "hooks/caveman-mode-tracker.js"],
                home,
                stdin=json.dumps({"prompt": "any prompt"}),
            )
            self.assertEqual(result.stdout, "")


if __name__ == "__main__":
    unittest.main()
