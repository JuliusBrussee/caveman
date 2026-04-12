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

            self.assertNotIn("STATUSLINE SETUP NEEDED", result.stdout)
            self.assertEqual((claude_dir / ".caveman-active").read_text(), "full")


class PortableShebangTests(unittest.TestCase):
    """Shell scripts must use #!/usr/bin/env so interpreters are found portably."""

    def _first_line(self, rel_path):
        return (REPO_ROOT / rel_path).read_text().splitlines()[0]

    def test_statusline_uses_env_sh(self):
        self.assertEqual(
            self._first_line("hooks/caveman-statusline.sh"),
            "#!/usr/bin/env sh",
        )

    def test_install_uses_env_bash(self):
        self.assertEqual(
            self._first_line("hooks/install.sh"),
            "#!/usr/bin/env bash",
        )

    def test_uninstall_uses_env_bash(self):
        self.assertEqual(
            self._first_line("hooks/uninstall.sh"),
            "#!/usr/bin/env bash",
        )

    def test_run_hook_uses_env_sh(self):
        self.assertEqual(
            self._first_line("hooks/run-hook.sh"),
            "#!/usr/bin/env sh",
        )

    def test_run_hook_finds_node_in_fallback_path(self):
        """run-hook.sh should find node via PATH even in a restricted environment."""
        node_path = subprocess.check_output(["which", "node"], text=True).strip()
        node_dir = str(Path(node_path).parent)

        # Run with a PATH that does NOT contain node's directory, then make
        # run-hook.sh discover it via the first fallback ($HOME/.nvm/current/bin
        # wins only if real node is there; here we use a fake HOME that puts a
        # node wrapper in the nvm slot).
        with tempfile.TemporaryDirectory(prefix="caveman-run-hook-") as tmp:
            fake_home = Path(tmp)
            nvm_bin = fake_home / ".nvm" / "current" / "bin"
            nvm_bin.mkdir(parents=True)
            # Symlink real node into the fake nvm bin dir
            (nvm_bin / "node").symlink_to(node_path)

            env = os.environ.copy()
            env["HOME"] = str(fake_home)
            env["PATH"] = "/usr/bin:/bin"  # node_dir intentionally absent

            result = subprocess.run(
                ["sh", str(REPO_ROOT / "hooks" / "run-hook.sh"),
                 str(REPO_ROOT / "hooks" / "caveman-activate.js")],
                env=env,
                capture_output=True,
                text=True,
            )
            self.assertEqual(result.returncode, 0)
            self.assertIn("CAVEMAN MODE ACTIVE", result.stdout)

    def test_plugin_json_uses_run_hook(self):
        """plugin.json hook commands must go through run-hook.sh, not call node directly."""
        plugin_json = REPO_ROOT / ".claude-plugin" / "plugin.json"
        data = json.loads(plugin_json.read_text())
        for event in ("SessionStart", "UserPromptSubmit"):
            hooks = data["hooks"][event]
            for entry in hooks:
                for h in entry["hooks"]:
                    cmd = h["command"]
                    self.assertIn("run-hook.sh", cmd, f"{event} command should use run-hook.sh")
                    self.assertNotIn("node ", cmd, f"{event} command should not call node directly")


if __name__ == "__main__":
    unittest.main()
