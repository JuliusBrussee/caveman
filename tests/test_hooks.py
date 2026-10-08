import json
import os
import re
import shutil
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

# The install.sh/uninstall.sh under test are the POSIX install path; Windows
# installs go through install.ps1, so driving them via git-bash proves nothing.
POSIX_SHELL_ONLY = unittest.skipIf(
    sys.platform == "win32", "POSIX shell install path; Windows uses install.ps1"
)


REPO_ROOT = Path(__file__).resolve().parent.parent
BASH = shutil.which("bash")

# A headless runner (`claude -p`, the cloud triage routine) exports
# CLAUDE_CODE_ENTRYPOINT=sdk-*, which starts SessionStart under the manual
# policy (#377). Every subprocess here copies os.environ, so drop it once.
os.environ.pop("CLAUDE_CODE_ENTRYPOINT", None)


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
            encoding="utf-8",
            stdin=subprocess.DEVNULL,
            capture_output=True,
            check=True,
        )

    @POSIX_SHELL_ONLY
    def test_install_upgrades_old_two_file_install(self):
        if BASH is None:
            self.skipTest("bash not found")
        with tempfile.TemporaryDirectory(prefix="caveman-hooks-upgrade-") as tmp:
            home = Path(tmp)
            hooks_dir = home / ".claude" / "hooks"
            hooks_dir.mkdir(parents=True)
            (home / ".claude" / "settings.json").write_text("{}\n", encoding="utf-8")
            (hooks_dir / "caveman-activate.js").write_text("", encoding="utf-8")
            (hooks_dir / "caveman-mode-tracker.js").write_text("", encoding="utf-8")

            self.run_cmd(["bash", "src/hooks/install.sh"], home)

            statusline = hooks_dir / "caveman-statusline.sh"
            self.assertTrue(statusline.exists(), "upgrade should install statusline script")

            settings = json.loads((home / ".claude" / "settings.json").read_text(encoding="utf-8"))
            self.assertIn("statusLine", settings)
            self.assertIn(str(statusline), settings["statusLine"]["command"])

    @POSIX_SHELL_ONLY
    def test_install_reconfigures_missing_statusline(self):
        if BASH is None:
            self.skipTest("bash not found")
        with tempfile.TemporaryDirectory(prefix="caveman-hooks-statusline-") as tmp:
            home = Path(tmp)
            claude_dir = home / ".claude"
            hooks_dir = claude_dir / "hooks"
            hooks_dir.mkdir(parents=True)

            for name in ("caveman-activate.js", "caveman-mode-tracker.js", "caveman-statusline.sh"):
                (hooks_dir / name).write_text("", encoding="utf-8")

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
            (claude_dir / "settings.json").write_text(json.dumps(settings, indent=2) + "\n", encoding="utf-8")

            result = self.run_cmd(["bash", "src/hooks/install.sh"], home)

            self.assertNotIn("Nothing to do", result.stdout)

            updated = json.loads((claude_dir / "settings.json").read_text(encoding="utf-8"))
            self.assertIn("statusLine", updated)
            self.assertIn(str(hooks_dir / "caveman-statusline.sh"), updated["statusLine"]["command"])

    @POSIX_SHELL_ONLY
    def test_uninstall_preserves_custom_statusline(self):
        if BASH is None:
            self.skipTest("bash not found")
        with tempfile.TemporaryDirectory(prefix="caveman-hooks-uninstall-") as tmp:
            home = Path(tmp)
            claude_dir = home / ".claude"
            hooks_dir = claude_dir / "hooks"
            hooks_dir.mkdir(parents=True)

            for name in ("caveman-activate.js", "caveman-mode-tracker.js", "caveman-statusline.sh"):
                (hooks_dir / name).write_text("", encoding="utf-8")

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
            (claude_dir / "settings.json").write_text(json.dumps(settings, indent=2) + "\n", encoding="utf-8")

            self.run_cmd(["bash", "src/hooks/uninstall.sh"], home)

            updated = json.loads((claude_dir / "settings.json").read_text(encoding="utf-8"))
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
                + "\n",
                encoding="utf-8",
            )

            result = self.run_cmd(["node", "src/hooks/caveman-activate.js"], home)

            self.assertNotIn("STATUSLINE SETUP NEEDED", result.stdout)
            self.assertEqual((claude_dir / ".caveman-active").read_text(encoding="utf-8"), "caveman")

    def test_activate_does_not_flag_a_tilde_statusline_as_stale(self):
        # `~` and `$HOME` are expanded by the shell at statusline time, not by
        # the hook's existence probe. A hand-written command using them works,
        # so it must not trigger a "repair needed" nudge that invites the model
        # to rewrite the user's settings.
        for command in (
            'bash "~/.claude/hooks/caveman-statusline.sh"',
            'bash "$HOME/.claude/hooks/caveman-statusline.sh"',
            'bash "${CLAUDE_CONFIG_DIR}/hooks/caveman-statusline.sh"',
        ):
            with tempfile.TemporaryDirectory(prefix="caveman-hooks-activate-") as tmp:
                home = Path(tmp)
                claude_dir = home / ".claude"
                claude_dir.mkdir(parents=True)
                (claude_dir / ".caveman-nudge-shown").write_text("1", encoding="utf-8")
                (claude_dir / "settings.json").write_text(
                    json.dumps({"statusLine": {"type": "command", "command": command}}) + "\n",
                    encoding="utf-8",
                )

                result = self.run_cmd(["node", "src/hooks/caveman-activate.js"], home)

                self.assertNotIn("STATUSLINE REPAIR NEEDED", result.stdout, command)
                self.assertNotIn("STATUSLINE SETUP NEEDED", result.stdout, command)

    # --- #1147: the statusline nudge must not pin a versioned plugin-cache path ---
    #
    # A plugin install runs the hook out of
    #   ~/.claude/plugins/cache/caveman/caveman/<version>/src/hooks/
    # and the nudge built its recommended command from __dirname, so the command
    # the user accepted froze that version directory. Claude Code prunes old
    # plugin cache versions; once the pinned directory goes, `bash <missing>`
    # exits 127 and Claude Code hides the whole status bar (#711). The nudge is
    # one-shot, so the badge is never offered again.
    def _plugin_install(self, home, version="3.0.0"):
        """Lay out a plugin-cache install of the hooks and return its hooks dir."""
        cache = home / ".claude" / "plugins" / "cache" / "caveman" / "caveman" / version
        hooks = cache / "src" / "hooks"
        hooks.parent.mkdir(parents=True)
        shutil.copytree(REPO_ROOT / "src" / "hooks", hooks)
        # loadRuleset resolves skills/ as a sibling of src/
        shutil.copytree(REPO_ROOT / "skills", cache / "skills")
        (home / ".claude" / "settings.json").write_text("{}\n", encoding="utf-8")
        return hooks

    def _nudge_command(self, stdout):
        match = re.search(r'"command":\s*("(?:[^"\\]|\\.)*")', stdout)
        self.assertIsNotNone(match, f"no statusline command in nudge:\n{stdout}")
        return json.loads(match.group(1))

    def test_nudge_does_not_recommend_a_versioned_plugin_cache_path(self):
        with tempfile.TemporaryDirectory(prefix="caveman-nudge-pin-") as tmp:
            home = Path(tmp)
            hooks = self._plugin_install(home)

            result = self.run_cmd(["node", str(hooks / "caveman-activate.js")], home)
            self.assertIn("STATUSLINE SETUP NEEDED", result.stdout)

            command = self._nudge_command(result.stdout)
            self.assertNotIn(
                "plugins/cache",
                command.replace("\\", "/"),
                f"nudge pinned a prunable plugin-cache path: {command}",
            )

    def test_nudge_recommends_a_path_that_exists(self):
        """A recommendation the user accepts must be runnable, not just stable."""
        with tempfile.TemporaryDirectory(prefix="caveman-nudge-exists-") as tmp:
            home = Path(tmp)
            hooks = self._plugin_install(home)

            result = self.run_cmd(["node", str(hooks / "caveman-activate.js")], home)
            command = self._nudge_command(result.stdout)

            match = re.search(r"(/[^\"]*caveman-statusline\.(?:sh|ps1))", command)
            self.assertIsNotNone(match, f"no script path in command: {command}")
            script = Path(match.group(1))
            self.assertTrue(script.exists(), f"nudge recommended a missing script: {script}")
            self.assertIn(
                ".caveman-sessions",
                script.read_text(encoding="utf-8"),
                "the copied script is not a caveman statusline",
            )

    def test_nudge_survives_a_plugin_version_bump(self):
        """The accepted command must still resolve after the old version is pruned."""
        with tempfile.TemporaryDirectory(prefix="caveman-nudge-bump-") as tmp:
            home = Path(tmp)
            old_hooks = self._plugin_install(home, version="2.7.0")

            result = self.run_cmd(["node", str(old_hooks / "caveman-activate.js")], home)
            command = self._nudge_command(result.stdout)
            script = Path(re.search(r"(/[^\"]*caveman-statusline\.(?:sh|ps1))", command).group(1))
            self.assertTrue(script.exists())

            # Claude Code updates the plugin and prunes the version it replaced.
            shutil.rmtree(home / ".claude" / "plugins" / "cache" / "caveman" / "caveman" / "2.7.0")
            self.assertTrue(
                script.exists(),
                "the recommended statusline script died with the pruned plugin version",
            )

    def test_activate_reoffers_a_statusline_whose_script_is_gone(self):
        """Already-nudged users are broken on disk; re-offer rather than stay silent."""
        with tempfile.TemporaryDirectory(prefix="caveman-nudge-stale-") as tmp:
            home = Path(tmp)
            hooks = self._plugin_install(home)
            claude_dir = home / ".claude"
            pruned = claude_dir / "plugins" / "cache" / "caveman" / "caveman" / "1.0.0" / "src" / "hooks" / "caveman-statusline.sh"
            (claude_dir / "settings.json").write_text(
                json.dumps({"statusLine": {"type": "command", "command": f'bash "{pruned}"'}}) + "\n",
                encoding="utf-8",
            )
            # The one-shot marker is already set for these users.
            (claude_dir / ".caveman-nudge-shown").write_text("1", encoding="utf-8")

            result = self.run_cmd(["node", str(hooks / "caveman-activate.js")], home)

            self.assertIn("STATUSLINE SETUP NEEDED", result.stdout)
            command = self._nudge_command(result.stdout)
            self.assertNotIn("1.0.0", command, f"re-offered the dead path: {command}")
            script = Path(re.search(r"(/[^\"]*caveman-statusline\.(?:sh|ps1))", command).group(1))
            self.assertTrue(script.exists(), f"re-offer recommended a missing script: {script}")

    def test_activate_leaves_a_working_statusline_alone(self):
        """A configured statusline that resolves must not be re-nudged."""
        with tempfile.TemporaryDirectory(prefix="caveman-nudge-ok-") as tmp:
            home = Path(tmp)
            hooks = self._plugin_install(home)
            live = hooks / "caveman-statusline.sh"
            (home / ".claude" / "settings.json").write_text(
                json.dumps({"statusLine": {"type": "command", "command": f'bash "{live}"'}}) + "\n",
                encoding="utf-8",
            )

            result = self.run_cmd(["node", str(hooks / "caveman-activate.js")], home)
            self.assertNotIn("STATUSLINE SETUP NEEDED", result.stdout)

    def test_activate_leaves_a_working_statusline_with_a_space_in_its_path(self):
        """A home directory with a space must not read as a pruned install."""
        with tempfile.TemporaryDirectory(prefix="caveman-nudge-space-") as tmp:
            home = Path(tmp) / "Jane Doe"
            home.mkdir(parents=True)
            hooks = self._plugin_install(home)
            live = hooks / "caveman-statusline.sh"
            self.assertIn(" ", str(live), "fixture must exercise a path with a space")
            (home / ".claude" / "settings.json").write_text(
                json.dumps({"statusLine": {"type": "command", "command": f'bash "{live}"'}}) + "\n",
                encoding="utf-8",
            )

            result = self.run_cmd(["node", str(hooks / "caveman-activate.js")], home)
            self.assertNotIn("STATUSLINE", result.stdout)

    def test_activate_does_not_overwrite_a_foreign_statusline_script(self):
        """A user's own script at the stable path is never clobbered."""
        with tempfile.TemporaryDirectory(prefix="caveman-nudge-foreign-") as tmp:
            home = Path(tmp)
            hooks = self._plugin_install(home)
            stable_dir = home / ".claude" / "hooks"
            stable_dir.mkdir(parents=True)
            foreign = stable_dir / "caveman-statusline.sh"
            foreign.write_text("#!/bin/bash\necho MINE\n", encoding="utf-8")

            self.run_cmd(["node", str(hooks / "caveman-activate.js")], home)

            self.assertEqual(
                foreign.read_text(encoding="utf-8"),
                "#!/bin/bash\necho MINE\n",
                "the hook overwrote a script it does not own",
            )

    # A pre-3.1 statusline whitelists only the old mode ids, so a command still
    # pinned to an old (unpruned) plugin-cache copy renders nothing at all.
    OLD_STATUSLINE = "#!/bin/bash\n# reads .caveman-sessions\ncase \"$m\" in lite|full|ultra) echo CAVEMAN;; esac\n"

    def test_activate_reoffers_an_outdated_statusline_script_once(self):
        with tempfile.TemporaryDirectory(prefix="caveman-nudge-outdated-") as tmp:
            home = Path(tmp)
            hooks = self._plugin_install(home, version="3.1.0")
            claude_dir = home / ".claude"
            old = claude_dir / "plugins" / "cache" / "caveman" / "caveman" / "3.0.0" / "src" / "hooks" / "caveman-statusline.sh"
            old.parent.mkdir(parents=True)
            old.write_text(self.OLD_STATUSLINE, encoding="utf-8")
            (claude_dir / "settings.json").write_text(
                json.dumps({"statusLine": {"type": "command", "command": f'bash "{old}"'}}) + "\n",
                encoding="utf-8",
            )
            (claude_dir / ".caveman-nudge-shown").write_text("1", encoding="utf-8")

            result = self.run_cmd(["node", str(hooks / "caveman-activate.js")], home)

            self.assertIn("STATUSLINE REPAIR NEEDED", result.stdout)
            self.assertIn("outdated copy", result.stdout)
            command = self._nudge_command(result.stdout)
            self.assertNotIn("3.0.0", command, f"re-offered the outdated path: {command}")
            script = Path(re.search(r"(/[^\"]*caveman-statusline\.(?:sh|ps1))", command).group(1))
            self.assertEqual(
                script.read_text(encoding="utf-8"),
                (hooks / "caveman-statusline.sh").read_text(encoding="utf-8"),
                "repair recommended a script that is not the current one",
            )

            # One-shot per distinct command: a declined repair is not re-asked.
            again = self.run_cmd(["node", str(hooks / "caveman-activate.js")], home)
            self.assertNotIn("STATUSLINE", again.stdout)

    def test_activate_leaves_an_outdated_foreign_statusline_alone(self):
        """A script named like ours but without the ownership marker is the user's."""
        with tempfile.TemporaryDirectory(prefix="caveman-nudge-outdated-foreign-") as tmp:
            home = Path(tmp)
            hooks = self._plugin_install(home)
            mine = home / "bin" / "caveman-statusline.sh"
            mine.parent.mkdir(parents=True)
            mine.write_text("#!/bin/bash\necho MINE\n", encoding="utf-8")
            (home / ".claude" / "settings.json").write_text(
                json.dumps({"statusLine": {"type": "command", "command": f'bash "{mine}"'}}) + "\n",
                encoding="utf-8",
            )

            result = self.run_cmd(["node", str(hooks / "caveman-activate.js")], home)
            self.assertNotIn("STATUSLINE", result.stdout)

    def test_activate_refreshes_an_accepted_stable_statusline_copy(self):
        """The stable copy a user accepted keeps up with plugin updates without a nudge."""
        with tempfile.TemporaryDirectory(prefix="caveman-stable-refresh-") as tmp:
            home = Path(tmp)
            hooks = self._plugin_install(home, version="3.1.0")
            claude_dir = home / ".claude"
            stable = claude_dir / "hooks" / "caveman-statusline.sh"
            stable.parent.mkdir(parents=True)
            stable.write_text(self.OLD_STATUSLINE, encoding="utf-8")
            (claude_dir / "settings.json").write_text(
                json.dumps({"statusLine": {"type": "command", "command": f'bash "{stable}"'}}) + "\n",
                encoding="utf-8",
            )
            (claude_dir / ".caveman-nudge-shown").write_text("1", encoding="utf-8")

            result = self.run_cmd(["node", str(hooks / "caveman-activate.js")], home)

            self.assertEqual(
                stable.read_text(encoding="utf-8"),
                (hooks / "caveman-statusline.sh").read_text(encoding="utf-8"),
                "the accepted stable statusline copy was not refreshed",
            )
            self.assertNotIn("STATUSLINE", result.stdout)

    # Regression for #587/#589 — hook at <root>/src/hooks/ must resolve SKILL.md
    # at <root>/skills/caveman/, not the nonexistent <root>/src/skills/.
    def test_activate_emits_skill_md_not_fallback_from_repo_layout(self):
        with tempfile.TemporaryDirectory(prefix="caveman-hooks-skillpath-") as tmp:
            home = Path(tmp)
            (home / ".claude").mkdir(parents=True)

            result = self.run_cmd(["node", "src/hooks/caveman-activate.js"], home)

            # The whole skill body, unfiltered — the fallback carries only
            # rule headlines, never the `### n.` sections.
            skill = (REPO_ROOT / "skills" / "caveman" / "SKILL.md").read_text(encoding="utf-8")
            body = re.sub(r"\A---[\s\S]*?---\s*", "", skill).rstrip()
            self.assertIn("### 1. Answer first", result.stdout)
            self.assertIn(body, result.stdout)
            self.assertTrue(result.stdout.startswith("CAVEMAN MODE ACTIVE — mode: caveman\n\n# caveman\n"))
            self.assertIn("Switch: /caveman, /ultracave, /megacave.", result.stdout)

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
                "---\nname: caveman\n---\nSTANDALONE MARKER RULESET\n",
                encoding="utf-8",
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
                "---\nname: caveman\n---\nPLUGIN ROOT MARKER RULESET\n",
                encoding="utf-8",
            )

            result = self.run_cmd(
                ["node", "src/hooks/caveman-activate.js"],
                home,
                extra_env={"CLAUDE_PLUGIN_ROOT": str(plugin_root)},
            )

            self.assertIn("PLUGIN ROOT MARKER RULESET", result.stdout)


class SessionStartSourceTests(unittest.TestCase):
    """SessionStart must re-emit the ruleset on every source, but must only
    re-derive the configured default when a session genuinely begins.

    #691 fixed half of this by branching on `source`. It could not fix the
    other half: deactivation was spelled "no flag file", so a session that had
    been turned off found nothing stored and fell back to getDefaultMode() —
    "stop caveman" was still silently undone by the next auto-compaction. The
    durable 'off' in the per-session store is what closes that.
    """

    ACTIVATE = "src/hooks/caveman-activate.js"

    def setUp(self):
        self._tmp = tempfile.TemporaryDirectory(prefix="caveman-source-")
        self.home = Path(self._tmp.name)
        self.claude_dir = self.home / ".claude"
        self.claude_dir.mkdir(parents=True)
        self.sessions = self.claude_dir / ".caveman-sessions"

    def tearDown(self):
        self._tmp.cleanup()

    def activate(self, payload=None, extra_env=None, timeout=30):
        env = os.environ.copy()
        env.pop("CLAUDE_PLUGIN_ROOT", None)
        env.pop("CAVEMAN_DEFAULT_MODE", None)
        env["HOME"] = str(self.home)
        env["USERPROFILE"] = str(self.home)
        env["CLAUDE_CONFIG_DIR"] = str(self.claude_dir)
        if extra_env:
            env.update(extra_env)
        return subprocess.run(
            ["node", self.ACTIVATE],
            cwd=REPO_ROOT,
            env=env,
            input="" if payload is None else json.dumps(payload),
            text=True,
            encoding="utf-8",
            capture_output=True,
            check=True,
            timeout=timeout,
        )

    def set_session_mode(self, session_id, mode):
        self.sessions.mkdir(parents=True, exist_ok=True)
        (self.sessions / f"{session_id}.mode").write_text(mode, encoding="utf-8")

    def session_mode(self, session_id):
        p = self.sessions / f"{session_id}.mode"
        return p.read_text(encoding="utf-8") if p.exists() else None

    def test_startup_persists_the_session_mode(self):
        self.activate({"session_id": "sessA", "source": "startup"})
        self.assertEqual(self.session_mode("sessA"), "caveman")
        self.assertEqual((self.claude_dir / ".caveman-active").read_text(encoding="utf-8"), "caveman")

    def test_compact_does_not_resurrect_a_deactivated_session(self):
        self.set_session_mode("sessA", "off")
        r = self.activate({"session_id": "sessA", "source": "compact"})
        self.assertNotIn("CAVEMAN MODE ACTIVE", r.stdout)
        self.assertEqual(self.session_mode("sessA"), "off", "state must stay off")

    def test_compact_still_re_emits_the_ruleset_when_active(self):
        # Compaction is exactly what prunes the rules out of context, so the
        # hook must keep re-injecting them — it just must not change the mode.
        self.set_session_mode("sessA", "ultracave")
        r = self.activate({"session_id": "sessA", "source": "compact"})
        self.assertIn("CAVEMAN MODE ACTIVE — mode: ultracave", r.stdout)
        self.assertIn("Ultracave is caveman with the grammar stripped.", r.stdout)

    def test_compact_does_not_re_derive_the_configured_default(self):
        # A pre-three-skill session file: 'lite' resolves to caveman.
        self.set_session_mode("sessA", "lite")
        r = self.activate(
            {"session_id": "sessA", "source": "compact"},
            extra_env={"CAVEMAN_DEFAULT_MODE": "ultra"},
        )
        self.assertIn("mode: caveman", r.stdout)
        self.assertNotIn("mode: ultracave", r.stdout)

    def test_legacy_wenyan_session_file_resolves_to_megacave(self):
        self.set_session_mode("sessA", "wenyan-lite")
        r = self.activate({"session_id": "sessA", "source": "compact"})
        self.assertIn("CAVEMAN MODE ACTIVE — mode: megacave", r.stdout)
        self.assertIn("Megacave is caveman in Classical Chinese.", r.stdout)
        # Re-persisted under the new id; the mirror follows.
        self.assertEqual(self.session_mode("sessA"), "megacave")
        self.assertEqual((self.claude_dir / ".caveman-active").read_text(encoding="utf-8"), "megacave")

    def test_legacy_env_default_resolves_to_its_skill(self):
        r = self.activate(
            {"session_id": "sessA", "source": "startup"},
            extra_env={"CAVEMAN_DEFAULT_MODE": "ultra"},
        )
        self.assertIn("CAVEMAN MODE ACTIVE — mode: ultracave", r.stdout)
        self.assertEqual(self.session_mode("sessA"), "ultracave")

    def test_fallback_ruleset_without_any_skill_file(self):
        # Standalone hooks with no skills dir: thesis + rule headlines, plus the
        # mode's own thesis for ultracave/megacave.
        with tempfile.TemporaryDirectory(prefix="caveman-noskill-") as tmp:
            hooks = Path(tmp) / "hooks"
            hooks.mkdir()
            for name in ("caveman-activate.js", "caveman-config.js", "package.json"):
                shutil.copy(REPO_ROOT / "src" / "hooks" / name, hooks / name)
            self.ACTIVATE = str(hooks / "caveman-activate.js")
            r = self.activate({"session_id": "sessA", "source": "startup"},
                              extra_env={"CAVEMAN_DEFAULT_MODE": "megacave"})
        self.assertIn("CAVEMAN MODE ACTIVE — mode: megacave", r.stdout)
        self.assertIn("Respond terse like smart caveman.", r.stdout)
        self.assertIn("9. Never perform caveman.", r.stdout)
        self.assertIn("以文言答。技術之實皆存，唯贅言去之。", r.stdout)
        self.assertNotIn("### 1.", r.stdout)

    def test_resume_preserves_a_deactivated_session_too(self):
        # Not just compaction: a resumed session that was turned off must stay
        # off. #691's flag read could not tell "off" from "never set".
        self.set_session_mode("sessA", "off")
        r = self.activate({"session_id": "sessA", "source": "resume"})
        self.assertNotIn("CAVEMAN MODE ACTIVE", r.stdout)

    def test_resume_without_stored_state_uses_the_default(self):
        r = self.activate({"session_id": "brandnew", "source": "resume"})
        self.assertIn("CAVEMAN MODE ACTIVE — mode: caveman", r.stdout)

    def test_clear_re_applies_the_default(self):
        # /clear is an explicit user reset, unlike a compaction: nothing else
        # in the conversation survives it, so neither does a "stop caveman".
        self.set_session_mode("sessA", "off")
        r = self.activate({"session_id": "sessA", "source": "clear"})
        self.assertIn("CAVEMAN MODE ACTIVE", r.stdout)

    def test_a_pre_upgrade_legacy_flag_survives_a_compaction(self):
        # Upgrade path: the session began before per-session state existed, so
        # only the machine-wide flag holds its mode.
        # Its legacy 'lite' value resolves to caveman.
        (self.claude_dir / ".caveman-active").write_text("lite", encoding="utf-8")
        r = self.activate(
            {"session_id": "sessA", "source": "compact"},
            extra_env={"CAVEMAN_DEFAULT_MODE": "ultra"},
        )
        self.assertIn("mode: caveman", r.stdout)

    def test_payloadless_invocation_behaves_as_before(self):
        for payload in (None, {}, {"source": "startup"}):
            r = self.activate(payload)
            self.assertIn("CAVEMAN MODE ACTIVE — mode: caveman", r.stdout)

    def test_malformed_payload_degrades_instead_of_failing(self):
        env = os.environ.copy()
        env.pop("CLAUDE_PLUGIN_ROOT", None)
        env.pop("CAVEMAN_DEFAULT_MODE", None)
        env["HOME"] = str(self.home)
        env["USERPROFILE"] = str(self.home)
        env["CLAUDE_CONFIG_DIR"] = str(self.claude_dir)
        r = subprocess.run(
            ["node", self.ACTIVATE],
            cwd=REPO_ROOT, env=env, input="not json {{{",
            text=True, encoding="utf-8", capture_output=True, check=True, timeout=30,
        )
        self.assertIn("CAVEMAN MODE ACTIVE", r.stdout)

    def test_rejected_session_id_writes_no_state_file(self):
        self.activate({"session_id": "../../escape", "source": "startup"})
        self.assertEqual((self.claude_dir / ".caveman-active").read_text(encoding="utf-8"), "caveman")
        stray = list(self.claude_dir.rglob("*.mode")) + list(self.claude_dir.rglob("*escape*"))
        self.assertEqual(stray, [], f"unexpected files: {stray}")

    def test_startup_sweeps_stale_session_files(self):
        self.set_session_mode("staleSess", "full")
        stale = self.sessions / "staleSess.mode"
        old = 1.0  # epoch-ish; far older than any TTL
        os.utime(stale, (old, old))

        self.activate({"session_id": "freshSess", "source": "startup"})
        self.assertFalse(stale.exists(), "stale session file should be swept")
        self.assertTrue((self.sessions / "freshSess.mode").exists())

    def test_compact_does_not_sweep(self):
        # GC walks a directory inside a 5s hook budget; compactions are frequent.
        self.set_session_mode("staleSess", "full")
        self.set_session_mode("sessA", "full")
        stale = self.sessions / "staleSess.mode"
        os.utime(stale, (1.0, 1.0))

        self.activate({"session_id": "sessA", "source": "compact"})
        self.assertTrue(stale.exists(), "compact must not spend budget on GC")

    def test_hook_never_blocks_on_stdin_that_never_closes(self):
        """The hook must finish while the host still holds the write end open.

        Claude Code writes one payload and closes, but that close can lag
        arbitrarily on Windows (#729/#833) — and several suites invoke this hook
        with no `input=` at all, inheriting a stdin that never reaches EOF. The
        payload watchdog plus stdin.unref() is what keeps a slow or absent close
        from spending the whole 5s budget.
        """
        read_fd, write_fd = os.pipe()  # write end stays open: no EOF, ever
        try:
            r = subprocess.run(
                ["node", self.ACTIVATE],
                cwd=REPO_ROOT,
                env={
                    **os.environ,
                    "HOME": str(self.home),
                    "USERPROFILE": str(self.home),
                    "CLAUDE_CONFIG_DIR": str(self.claude_dir),
                },
                stdin=read_fd,
                capture_output=True,
                text=True,
                encoding="utf-8",
                timeout=15,  # TimeoutExpired => the hook hangs => test fails
            )
            self.assertEqual(r.returncode, 0)
            self.assertIn("CAVEMAN MODE ACTIVE", r.stdout)
        finally:
            os.close(read_fd)
            os.close(write_fd)


if __name__ == "__main__":
    unittest.main()
