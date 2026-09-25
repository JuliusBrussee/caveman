import json
import os
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
            self.assertEqual((claude_dir / ".caveman-active").read_text(encoding="utf-8"), "full")

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
        (self.sessions / f"{session_id}.mode").write_text(mode)

    def session_mode(self, session_id):
        p = self.sessions / f"{session_id}.mode"
        return p.read_text() if p.exists() else None

    def test_startup_persists_the_session_mode(self):
        self.activate({"session_id": "sessA", "source": "startup"})
        self.assertEqual(self.session_mode("sessA"), "full")
        self.assertEqual((self.claude_dir / ".caveman-active").read_text(), "full")

    def test_compact_does_not_resurrect_a_deactivated_session(self):
        self.set_session_mode("sessA", "off")
        r = self.activate({"session_id": "sessA", "source": "compact"})
        self.assertNotIn("CAVEMAN MODE ACTIVE", r.stdout)
        self.assertEqual(self.session_mode("sessA"), "off", "state must stay off")

    def test_compact_still_re_emits_the_ruleset_when_active(self):
        # Compaction is exactly what prunes the rules out of context, so the
        # hook must keep re-injecting them — it just must not change the mode.
        self.set_session_mode("sessA", "ultra")
        r = self.activate({"session_id": "sessA", "source": "compact"})
        self.assertIn("CAVEMAN MODE ACTIVE — level: ultra", r.stdout)

    def test_compact_does_not_re_derive_the_configured_default(self):
        self.set_session_mode("sessA", "lite")
        r = self.activate(
            {"session_id": "sessA", "source": "compact"},
            extra_env={"CAVEMAN_DEFAULT_MODE": "ultra"},
        )
        self.assertIn("level: lite", r.stdout)
        self.assertNotIn("level: ultra", r.stdout)

    def test_resume_preserves_a_deactivated_session_too(self):
        # Not just compaction: a resumed session that was turned off must stay
        # off. #691's flag read could not tell "off" from "never set".
        self.set_session_mode("sessA", "off")
        r = self.activate({"session_id": "sessA", "source": "resume"})
        self.assertNotIn("CAVEMAN MODE ACTIVE", r.stdout)

    def test_resume_without_stored_state_uses_the_default(self):
        r = self.activate({"session_id": "brandnew", "source": "resume"})
        self.assertIn("CAVEMAN MODE ACTIVE — level: full", r.stdout)

    def test_clear_re_applies_the_default(self):
        # /clear is an explicit user reset, unlike a compaction: nothing else
        # in the conversation survives it, so neither does a "stop caveman".
        self.set_session_mode("sessA", "off")
        r = self.activate({"session_id": "sessA", "source": "clear"})
        self.assertIn("CAVEMAN MODE ACTIVE", r.stdout)

    def test_a_pre_upgrade_legacy_flag_survives_a_compaction(self):
        # Upgrade path: the session began before per-session state existed, so
        # only the machine-wide flag holds its mode.
        (self.claude_dir / ".caveman-active").write_text("lite")
        r = self.activate(
            {"session_id": "sessA", "source": "compact"},
            extra_env={"CAVEMAN_DEFAULT_MODE": "ultra"},
        )
        self.assertIn("level: lite", r.stdout)

    def test_payloadless_invocation_behaves_as_before(self):
        for payload in (None, {}, {"source": "startup"}):
            r = self.activate(payload)
            self.assertIn("CAVEMAN MODE ACTIVE — level: full", r.stdout)

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
        self.assertEqual((self.claude_dir / ".caveman-active").read_text(), "full")
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
