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

            self.run_cmd(["bash", "src/hooks/install.sh"], home)

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

            result = self.run_cmd(["bash", "src/hooks/install.sh"], home)

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

            self.run_cmd(["bash", "src/hooks/uninstall.sh"], home)

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

            result = self.run_cmd(["node", "src/hooks/caveman-activate.js"], home)

            self.assertNotIn("STATUSLINE SETUP NEEDED", result.stdout)
            self.assertEqual((claude_dir / ".caveman-active").read_text(), "full")


class ManualModeTests(unittest.TestCase):
    """defaultMode 'manual': SessionStart must not auto-activate, but an explicit
    bare /caveman must still activate (at MANUAL_DEFAULT_LEVEL = 'full'). 'off'
    behavior must remain a no-op on explicit activation."""

    def run_hook(self, script, home, default_mode=None, stdin=None):
        env = os.environ.copy()
        env["HOME"] = str(home)
        env["USERPROFILE"] = str(home)
        if default_mode is not None:
            env["CAVEMAN_DEFAULT_MODE"] = default_mode
        return subprocess.run(
            ["node", script],
            cwd=REPO_ROOT,
            env=env,
            input=stdin,
            text=True,
            capture_output=True,
            check=True,
        )

    def test_activate_manual_does_not_auto_activate(self):
        with tempfile.TemporaryDirectory(prefix="caveman-manual-activate-") as tmp:
            home = Path(tmp)
            (home / ".claude").mkdir(parents=True)

            result = self.run_hook(
                "src/hooks/caveman-activate.js", home, default_mode="manual"
            )

            self.assertEqual(result.stdout, "OK")
            self.assertFalse(
                (home / ".claude" / ".caveman-active").exists(),
                "manual mode must not write the active flag at session start",
            )

    def test_bare_caveman_activates_full_under_manual(self):
        with tempfile.TemporaryDirectory(prefix="caveman-manual-tracker-") as tmp:
            home = Path(tmp)
            (home / ".claude").mkdir(parents=True)

            self.run_hook(
                "src/hooks/caveman-mode-tracker.js",
                home,
                default_mode="manual",
                stdin=json.dumps({"prompt": "/caveman"}),
            )

            flag = home / ".claude" / ".caveman-active"
            self.assertTrue(flag.exists(), "bare /caveman under manual must activate")
            self.assertEqual(flag.read_text().strip(), "full")

    def test_bare_caveman_is_noop_under_off(self):
        with tempfile.TemporaryDirectory(prefix="caveman-off-tracker-") as tmp:
            home = Path(tmp)
            (home / ".claude").mkdir(parents=True)

            self.run_hook(
                "src/hooks/caveman-mode-tracker.js",
                home,
                default_mode="off",
                stdin=json.dumps({"prompt": "/caveman"}),
            )

            self.assertFalse(
                (home / ".claude" / ".caveman-active").exists(),
                "bare /caveman under off must remain a no-op (unchanged behavior)",
            )

    def test_nl_activation_activates_full_under_manual(self):
        with tempfile.TemporaryDirectory(prefix="caveman-manual-nl-") as tmp:
            home = Path(tmp)
            (home / ".claude").mkdir(parents=True)

            self.run_hook(
                "src/hooks/caveman-mode-tracker.js",
                home,
                default_mode="manual",
                stdin=json.dumps({"prompt": "talk like caveman"}),
            )

            flag = home / ".claude" / ".caveman-active"
            self.assertTrue(
                flag.exists(),
                "natural-language activation under manual must activate",
            )
            self.assertEqual(flag.read_text().strip(), "full")

    def test_nl_activation_is_noop_under_off(self):
        with tempfile.TemporaryDirectory(prefix="caveman-off-nl-") as tmp:
            home = Path(tmp)
            (home / ".claude").mkdir(parents=True)

            self.run_hook(
                "src/hooks/caveman-mode-tracker.js",
                home,
                default_mode="off",
                stdin=json.dumps({"prompt": "talk like caveman"}),
            )

            self.assertFalse(
                (home / ".claude" / ".caveman-active").exists(),
                "natural-language activation under off must remain a no-op",
            )


if __name__ == "__main__":
    unittest.main()
