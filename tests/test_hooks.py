import json
import os
import subprocess
import tempfile
import unittest
from pathlib import Path


REPO_ROOT = Path(__file__).resolve().parent.parent


class HookScriptTests(unittest.TestCase):
    def run_cmd(self, cmd, home, input_text=None):
        env = os.environ.copy()
        env["HOME"] = str(home)
        env["USERPROFILE"] = str(home)
        return subprocess.run(
            cmd,
            cwd=REPO_ROOT,
            env=env,
            text=True,
            input=input_text,
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

            self.assertNotIn("STATUSLINE SETUP NEEDED", result.stdout)
            self.assertEqual((claude_dir / ".caveman-active").read_text(), "full")

    def test_mode_tracker_preserves_exact_normal_mode_only(self):
        with tempfile.TemporaryDirectory(prefix="caveman-hooks-normal-mode-") as tmp:
            home = Path(tmp)
            claude_dir = home / ".claude"
            claude_dir.mkdir(parents=True)
            flag = claude_dir / ".caveman-active"

            for prompt in (
                "switch from normal mode to debug mode",
                "use normal mode for this task",
                "is this normal mode behavior?",
            ):
                flag.write_text("full")
                self.run_cmd(
                    ["node", "hooks/caveman-mode-tracker.js"],
                    home,
                    input_text=json.dumps({"prompt": prompt}),
                )
                self.assertTrue(flag.exists(), f"{prompt!r} should not deactivate caveman")

            flag.write_text("full")
            self.run_cmd(
                ["node", "hooks/caveman-mode-tracker.js"],
                home,
                input_text=json.dumps({"prompt": "normal mode"}),
            )
            self.assertFalse(flag.exists(), '"normal mode" should still deactivate caveman')

    def test_mode_tracker_allows_activate_caveman_no_markdown(self):
        with tempfile.TemporaryDirectory(prefix="caveman-hooks-no-markdown-") as tmp:
            home = Path(tmp)
            claude_dir = home / ".claude"
            claude_dir.mkdir(parents=True)

            self.run_cmd(
                ["node", "hooks/caveman-mode-tracker.js"],
                home,
                input_text=json.dumps({"prompt": "activate caveman, no markdown"}),
            )

            self.assertEqual((claude_dir / ".caveman-active").read_text(), "full")

    def test_mode_tracker_ignores_dont_activate_caveman(self):
        with tempfile.TemporaryDirectory(prefix="caveman-hooks-dont-activate-") as tmp:
            home = Path(tmp)
            claude_dir = home / ".claude"
            claude_dir.mkdir(parents=True)

            self.run_cmd(
                ["node", "hooks/caveman-mode-tracker.js"],
                home,
                input_text=json.dumps({"prompt": "don't activate caveman"}),
            )

            self.assertFalse((claude_dir / ".caveman-active").exists())

    def test_mode_tracker_only_activates_exact_start_caveman(self):
        with tempfile.TemporaryDirectory(prefix="caveman-hooks-start-") as tmp:
            home = Path(tmp)
            claude_dir = home / ".claude"
            claude_dir.mkdir(parents=True)
            flag = claude_dir / ".caveman-active"

            self.run_cmd(
                ["node", "hooks/caveman-mode-tracker.js"],
                home,
                input_text=json.dumps({"prompt": "How do I start a caveman fire?"}),
            )
            self.assertFalse(flag.exists())

            self.run_cmd(
                ["node", "hooks/caveman-mode-tracker.js"],
                home,
                input_text=json.dumps({"prompt": "start caveman"}),
            )
            self.assertEqual(flag.read_text(), "full")


if __name__ == "__main__":
    unittest.main()
