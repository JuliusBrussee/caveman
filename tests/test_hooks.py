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

    def test_activate_per_session_flag_does_not_hang_when_stdin_is_tty(self):
        """SessionStart hook must not block waiting for stdin EOF.

        Reviewer flagged: `fs.readFileSync(0)` on an inherited tty stdin
        would hang the hook. Guard with isTTY check — if isTTY, skip read.
        We can't make stdin a tty from a subprocess, but we CAN attach the
        subprocess to /dev/null (closed stdin) and confirm the hook still
        completes promptly, proving the read doesn't hang on an empty pipe.
        """
        import time
        with tempfile.TemporaryDirectory(prefix="caveman-hooks-stdin-") as tmp:
            home = Path(tmp)
            claude_dir = home / ".claude"
            claude_dir.mkdir(parents=True)
            env = os.environ.copy()
            env["HOME"] = str(home)
            env["USERPROFILE"] = str(home)
            env["CLAUDE_CONFIG_DIR"] = str(claude_dir)

            start = time.monotonic()
            proc = subprocess.run(
                ["node", "src/hooks/caveman-activate.js"],
                cwd=REPO_ROOT,
                env=env,
                text=True,
                stdin=subprocess.DEVNULL,
                capture_output=True,
                timeout=5,
            )
            elapsed = time.monotonic() - start

            self.assertEqual(proc.returncode, 0)
            self.assertLess(elapsed, 3, "activate hook must not block on stdin read")

    def test_off_mode_clears_both_per_session_and_global_flag(self):
        """Reviewer flagged: when default mode is 'off' and session_id is
        present, hook deletes only the per-session flag, leaving the legacy
        global flag stale. The hook must clear BOTH.
        """
        with tempfile.TemporaryDirectory(prefix="caveman-hooks-off-") as tmp:
            home = Path(tmp)
            claude_dir = home / ".claude"
            claude_dir.mkdir(parents=True)
            sid = "abc123"
            per = claude_dir / f".caveman-active-{sid}"
            glob = claude_dir / ".caveman-active"
            per.write_text("ultra")
            glob.write_text("full")

            env = os.environ.copy()
            env["HOME"] = str(home)
            env["USERPROFILE"] = str(home)
            env["CLAUDE_CONFIG_DIR"] = str(claude_dir)
            env["CAVEMAN_DEFAULT_MODE"] = "off"

            subprocess.run(
                ["node", "src/hooks/caveman-activate.js"],
                cwd=REPO_ROOT,
                env=env,
                text=True,
                input=json.dumps({"session_id": sid}),
                capture_output=True,
                check=True,
                timeout=5,
            )

            self.assertFalse(per.exists(), "off mode must delete the per-session flag")
            self.assertFalse(glob.exists(), "off mode must ALSO delete the global flag")

    def test_per_session_flag_is_written_and_mirrored_to_global(self):
        """Concurrent sessions A and B with different modes must each see
        their own per-session flag. The global flag mirrors the latest write
        for statusline configs that don't get session_id on stdin.
        """
        with tempfile.TemporaryDirectory(prefix="caveman-hooks-persession-") as tmp:
            home = Path(tmp)
            claude_dir = home / ".claude"
            claude_dir.mkdir(parents=True)
            env_base = os.environ.copy()
            env_base["HOME"] = str(home)
            env_base["USERPROFILE"] = str(home)
            env_base["CLAUDE_CONFIG_DIR"] = str(claude_dir)

            # Session A → /caveman ultra
            subprocess.run(
                ["node", "src/hooks/caveman-mode-tracker.js"],
                cwd=REPO_ROOT,
                env=env_base,
                text=True,
                input=json.dumps({"session_id": "sessA", "prompt": "/caveman ultra"}),
                capture_output=True,
                check=True,
            )
            # Session B → /caveman lite
            subprocess.run(
                ["node", "src/hooks/caveman-mode-tracker.js"],
                cwd=REPO_ROOT,
                env=env_base,
                text=True,
                input=json.dumps({"session_id": "sessB", "prompt": "/caveman lite"}),
                capture_output=True,
                check=True,
            )

            self.assertEqual((claude_dir / ".caveman-active-sessA").read_text().strip(), "ultra")
            self.assertEqual((claude_dir / ".caveman-active-sessB").read_text().strip(), "lite")
            # Global mirrors the latest write (last writer wins on the legacy path).
            self.assertEqual((claude_dir / ".caveman-active").read_text().strip(), "lite")

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


if __name__ == "__main__":
    unittest.main()
