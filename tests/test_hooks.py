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

    def run_cmd_with_env(self, cmd, home, extra_env, input_text=None):
        env = os.environ.copy()
        env["HOME"] = str(home)
        env["USERPROFILE"] = str(home)
        env.update(extra_env)
        return subprocess.run(
            cmd,
            cwd=REPO_ROOT,
            env=env,
            input=input_text,
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
            self.assertTrue((home / ".claude" / "rules" / "hangeul-compression.md").exists())
            self.assertTrue((home / ".claude" / "rules" / "wenyan-compression.md").exists())

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
            self.assertTrue((claude_dir / "rules" / "hangeul-compression.md").exists())
            self.assertTrue((claude_dir / "rules" / "wenyan-compression.md").exists())

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

    def test_hangeul_statusline_renders(self):
        with tempfile.TemporaryDirectory(prefix="caveman-hooks-hangeul-") as tmp:
            home = Path(tmp)
            claude_dir = home / ".claude"
            claude_dir.mkdir(parents=True)

            self.run_cmd_with_env(
                ["node", "hooks/caveman-activate.js"],
                home,
                {"CLAUDE_CONFIG_DIR": str(claude_dir), "CAVEMAN_DEFAULT_MODE": "hangeul-full"},
            )

            result = self.run_cmd_with_env(
                ["bash", "hooks/caveman-statusline.sh"],
                home,
                {"CLAUDE_CONFIG_DIR": str(claude_dir)},
            )

            self.assertIn("[CAVEMAN:HANGEUL-FULL]", result.stdout)

    def test_tracker_accepts_documented_compress_command(self):
        with tempfile.TemporaryDirectory(prefix="caveman-hooks-compress-") as tmp:
            home = Path(tmp)
            claude_dir = home / ".claude"
            claude_dir.mkdir(parents=True)

            self.run_cmd_with_env(
                ["node", "hooks/caveman-mode-tracker.js"],
                home,
                {"CLAUDE_CONFIG_DIR": str(claude_dir)},
                input_text=json.dumps({"prompt": "/caveman:compress CLAUDE.md"}),
            )

            self.assertEqual((claude_dir / ".caveman-active").read_text(), "compress")

    def test_natural_language_activation_respects_negation(self):
        with tempfile.TemporaryDirectory(prefix="caveman-hooks-negation-") as tmp:
            home = Path(tmp)
            claude_dir = home / ".claude"
            claude_dir.mkdir(parents=True)

            self.run_cmd_with_env(
                ["node", "hooks/caveman-mode-tracker.js"],
                home,
                {"CLAUDE_CONFIG_DIR": str(claude_dir)},
                input_text=json.dumps({"prompt": "don't activate caveman yet"}),
            )

            self.assertFalse((claude_dir / ".caveman-active").exists())


if __name__ == "__main__":
    unittest.main()
