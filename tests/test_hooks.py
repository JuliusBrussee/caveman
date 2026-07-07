import json
import os
import shutil
import subprocess
import tempfile
import unittest
from pathlib import Path


REPO_ROOT = Path(__file__).resolve().parent.parent


class HookScriptTests(unittest.TestCase):
    def run_cmd(self, cmd, home, extra_env=None):
        env = os.environ.copy()
        env.pop("CLAUDE_PLUGIN_ROOT", None)
        env["HOME"] = str(home)
        env["USERPROFILE"] = str(home)
        if extra_env:
            env.update(extra_env)
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

    # Regression for #587/#589 — hook at <root>/src/hooks/ must resolve SKILL.md
    # at <root>/skills/caveman/, not the nonexistent <root>/src/skills/.
    def test_activate_emits_skill_md_not_fallback_from_repo_layout(self):
        with tempfile.TemporaryDirectory(prefix="caveman-hooks-skillpath-") as tmp:
            home = Path(tmp)
            (home / ".claude").mkdir(parents=True)

            result = self.run_cmd(["node", "src/hooks/caveman-activate.js"], home)

            # Intensity table exists only in SKILL.md, never in the fallback
            self.assertIn("## Intensity", result.stdout)
            # Default mode is full — table filtered to the active level's row
            self.assertIn("| **full** |", result.stdout)
            self.assertNotIn("| **lite** |", result.stdout)

    def test_activate_finds_skill_beside_config_dir_hooks(self):
        # Standalone layout: hooks at $CLAUDE_CONFIG_DIR/hooks/, skill installed
        # at $CLAUDE_CONFIG_DIR/skills/caveman/SKILL.md
        with tempfile.TemporaryDirectory(prefix="caveman-hooks-standalone-") as tmp:
            home = Path(tmp)
            claude_dir = home / ".claude"
            hooks_dir = claude_dir / "hooks"
            hooks_dir.mkdir(parents=True)
            for name in ("caveman-activate.js", "caveman-config.js", "package.json"):
                shutil.copy(REPO_ROOT / "src" / "hooks" / name, hooks_dir / name)
            skill_dir = claude_dir / "skills" / "caveman"
            skill_dir.mkdir(parents=True)
            (skill_dir / "SKILL.md").write_text(
                "---\nname: caveman\n---\nSTANDALONE MARKER RULESET\n"
            )

            result = self.run_cmd(["node", str(hooks_dir / "caveman-activate.js")], home)

            self.assertIn("STANDALONE MARKER RULESET", result.stdout)

    def test_activate_prefers_claude_plugin_root(self):
        with tempfile.TemporaryDirectory(prefix="caveman-hooks-pluginroot-") as tmp:
            home = Path(tmp)
            (home / ".claude").mkdir(parents=True)
            plugin_root = home / "plugin-cache"
            skill_dir = plugin_root / "skills" / "caveman"
            skill_dir.mkdir(parents=True)
            (skill_dir / "SKILL.md").write_text(
                "---\nname: caveman\n---\nPLUGIN ROOT MARKER RULESET\n"
            )

            result = self.run_cmd(
                ["node", "src/hooks/caveman-activate.js"],
                home,
                extra_env={"CLAUDE_PLUGIN_ROOT": str(plugin_root)},
            )

            self.assertIn("PLUGIN ROOT MARKER RULESET", result.stdout)

    def test_msys_path_conversion_for_hook_commands(self):
        """Regression: plugin.json hook commands use sed to convert MSYS/MINGW
        paths like /c/Users/foo into Windows paths like c:/Users/foo so that
        Node can resolve them inside Git Bash on Windows."""
        # The exact sed expression from plugin.json:
        #   HOOK_ROOT=$(echo "${CLAUDE_PLUGIN_ROOT}" | sed 's|^/\([a-zA-Z]\)/|\1:/|')
        msys_sed = r"""s|^/\([a-zA-Z]\)/|\1:/|"""

        cases = [
            ("/c/Users/test/path", "c:/Users/test/path"),
            ("/d/projects/caveman", "d:/projects/caveman"),
            ("/C/Windows/System32", "C:/Windows/System32"),
            # Already-Windows paths pass through unchanged:
            ("C:/Users/test/path", "C:/Users/test/path"),
            ("D:\\Projects\\caveman", "D:\\Projects\\caveman"),
            # Unix paths without MSYS drive-letter prefix pass through:
            ("/home/user/project", "/home/user/project"),
            ("/usr/local/bin", "/usr/local/bin"),
            # Relative paths pass through:
            ("src/hooks/caveman-activate.js", "src/hooks/caveman-activate.js"),
        ]

        for input_path, expected in cases:
            with self.subTest(input=input_path):
                result = subprocess.run(
                    ["bash", "-c", f'echo "{input_path}" | sed "{msys_sed}"'],
                    capture_output=True, text=True, check=True,
                )
                self.assertEqual(result.stdout.strip(), expected)


if __name__ == "__main__":
    unittest.main()
