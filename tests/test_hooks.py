import json
import os
import shutil
import subprocess
import tempfile
import unittest
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parent.parent


class HookScriptTests(unittest.TestCase):
    def run_cmd(self, cmd, home, extra_env=None, stdin_payload=None):
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
            input=json.dumps(stdin_payload) if stdin_payload is not None else None,
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
            self.assertTrue(
                statusline.exists(), "upgrade should install statusline script"
            )

            settings = json.loads((home / ".claude" / "settings.json").read_text())
            self.assertIn("statusLine", settings)
            self.assertIn(str(statusline), settings["statusLine"]["command"])

    def test_install_reconfigures_missing_statusline(self):
        with tempfile.TemporaryDirectory(prefix="caveman-hooks-statusline-") as tmp:
            home = Path(tmp)
            claude_dir = home / ".claude"
            hooks_dir = claude_dir / "hooks"
            hooks_dir.mkdir(parents=True)

            for name in (
                "caveman-activate.js",
                "caveman-mode-tracker.js",
                "caveman-statusline.sh",
            ):
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
            (claude_dir / "settings.json").write_text(
                json.dumps(settings, indent=2) + "\n"
            )

            result = self.run_cmd(["bash", "src/hooks/install.sh"], home)

            self.assertNotIn("Nothing to do", result.stdout)

            updated = json.loads((claude_dir / "settings.json").read_text())
            self.assertIn("statusLine", updated)
            self.assertIn(
                str(hooks_dir / "caveman-statusline.sh"),
                updated["statusLine"]["command"],
            )

    def test_uninstall_preserves_custom_statusline(self):
        with tempfile.TemporaryDirectory(prefix="caveman-hooks-uninstall-") as tmp:
            home = Path(tmp)
            claude_dir = home / ".claude"
            hooks_dir = claude_dir / "hooks"
            hooks_dir.mkdir(parents=True)

            for name in (
                "caveman-activate.js",
                "caveman-mode-tracker.js",
                "caveman-statusline.sh",
            ):
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
            (claude_dir / "settings.json").write_text(
                json.dumps(settings, indent=2) + "\n"
            )

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

            result = self.run_cmd(
                ["node", str(hooks_dir / "caveman-activate.js")], home
            )

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

    # ---- session-sync-with-opt-in-isolation: caveman-activate.js write-target ----

    def _run_activate(self, home, source, session_id=None):
        payload = {"source": source}
        if session_id is not None:
            payload["session_id"] = session_id
        return self.run_cmd(
            ["node", "src/hooks/caveman-activate.js"], home, stdin_payload=payload
        )

    def test_startup_no_legacy_seeds_legacy_no_scoped_file(self):
        # case (a): true startup, no legacy file yet -> legacy gets seeded
        # from getDefaultMode() (no config anywhere in the fresh temp HOME,
        # so 'full'); no scoped file is ever created.
        with tempfile.TemporaryDirectory(prefix="caveman-hooks-startup-fresh-") as tmp:
            home = Path(tmp)
            (home / ".claude").mkdir(parents=True)

            self._run_activate(home, "startup", session_id="sess1")

            self.assertEqual((home / ".claude" / ".caveman-active").read_text(), "full")
            self.assertFalse(
                (home / ".claude" / ".caveman-active-sess1").exists(),
                "true startup must never create a scoped file for an untouched session",
            )

    def test_startup_existing_legacy_gets_refreshed_no_scoped_file(self):
        # case (b): true startup with an existing legacy value -> legacy gets
        # refreshed (overwritten) from getDefaultMode() again, matching
        # upstream's unconditional-refresh-on-every-startup mechanism; still
        # no scoped file.
        with tempfile.TemporaryDirectory(
            prefix="caveman-hooks-startup-refresh-"
        ) as tmp:
            home = Path(tmp)
            claude_dir = home / ".claude"
            claude_dir.mkdir(parents=True)
            (claude_dir / ".caveman-active").write_text("lite")

            self._run_activate(home, "startup", session_id="sess1")

            self.assertEqual(
                (claude_dir / ".caveman-active").read_text(),
                "full",
                "true startup must refresh the legacy value from getDefaultMode(), "
                "overwriting whatever was there before",
            )
            self.assertFalse((claude_dir / ".caveman-active-sess1").exists())

    def test_resume_on_synced_session_does_not_create_scoped_file(self):
        # case (h), T1 v3 High regression guard: a resume/compact/clear
        # re-fire on a SYNCED session (no scoped file, real session_id) must
        # NOT create one. This is the exact bug the review round caught in
        # the inherited resume branch (writeFlagPath was computed
        # unconditionally from flagBaseName(sessionId) before the source
        # branch).
        with tempfile.TemporaryDirectory(prefix="caveman-hooks-resume-synced-") as tmp:
            home = Path(tmp)
            claude_dir = home / ".claude"
            claude_dir.mkdir(parents=True)
            (claude_dir / ".caveman-active").write_text("full")

            for source in ("resume", "compact", "clear"):
                self._run_activate(home, source, session_id="sess1")
                self.assertFalse(
                    (claude_dir / ".caveman-active-sess1").exists(),
                    f"source={source!r} on a synced session must not create a scoped file",
                )
            self.assertEqual(
                (claude_dir / ".caveman-active").read_text(),
                "full",
                "legacy value must be preserved (not overwritten) by non-startup events",
            )

    def test_reset_then_resume_does_not_recreate_scoped_file(self):
        # case (i): a scoped-file unlink (simulating /caveman default having
        # just run) immediately followed by a resume re-fire must NOT
        # re-create the scoped file -- guards against the revert command
        # being silently undone by the very next hook fire.
        with tempfile.TemporaryDirectory(prefix="caveman-hooks-reset-resume-") as tmp:
            home = Path(tmp)
            claude_dir = home / ".claude"
            claude_dir.mkdir(parents=True)
            (claude_dir / ".caveman-active").write_text("full")
            (claude_dir / ".caveman-active-sess1").write_text("ultra")
            (
                claude_dir / ".caveman-active-sess1"
            ).unlink()  # simulate /caveman default's unlink

            self._run_activate(home, "resume", session_id="sess1")

            self.assertFalse(
                (claude_dir / ".caveman-active-sess1").exists(),
                "a resume re-fire right after /caveman default must not re-isolate the session",
            )
            self.assertEqual((claude_dir / ".caveman-active").read_text(), "full")

    def test_isolated_session_preserved_across_startup_and_resume(self):
        # case (j), T1 v3 Medium regression guard. Claude Code's actual
        # SessionStart `source` value for `claude --resume` continuing an
        # existing session is not verified anywhere in this repo (T1 v4 Low
        # finding) -- per the plan's fallback, cover BOTH 'startup' and
        # 'resume' on an already-isolated session, since the unified design
        # is correct under either value and the invariant (isolated mode
        # preserved, ruleset matches) must hold regardless.
        for source in ("startup", "resume"):
            with tempfile.TemporaryDirectory(
                prefix="caveman-hooks-isolated-preserve-"
            ) as tmp:
                home = Path(tmp)
                claude_dir = home / ".claude"
                claude_dir.mkdir(parents=True)
                (claude_dir / ".caveman-active").write_text("full")
                (claude_dir / ".caveman-active-sess1").write_text("ultra")

                result = self._run_activate(home, source, session_id="sess1")

                self.assertEqual(
                    (claude_dir / ".caveman-active-sess1").read_text(),
                    "ultra",
                    f"source={source!r} must preserve the isolated session's stored mode",
                )
                self.assertEqual(
                    (claude_dir / ".caveman-active").read_text(),
                    "full",
                    "the legacy/shared value must be untouched by an isolated session's own hook fire",
                )
                self.assertIn(
                    "| **ultra** |",
                    result.stdout,
                    f"source={source!r}: emitted ruleset must match the stored ISOLATED mode "
                    "(ultra), never fall back to the config default (full)",
                )
                self.assertNotIn("| **full** |", result.stdout)


if __name__ == "__main__":
    unittest.main()
