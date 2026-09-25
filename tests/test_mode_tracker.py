"""Tests for caveman-mode-tracker.js prompt parsing (issues #598, #599).

Drives the UserPromptSubmit hook with real prompts over stdin against an
isolated CLAUDE_CONFIG_DIR and asserts the flag-file state afterwards.

#598: natural-language triggers misfired — "turn caveman mode off"
ACTIVATED caveman (and clobbered the level to default), "turn caveman off"
was a no-op, questions about caveman armed it, and vim's "normal mode"
deactivated it.

#599: one-shot independent modes (/caveman-commit etc.) permanently
overwrote the active prose level, and the plugin-namespaced
/caveman:caveman-commit|-review variants were not recognized at all.
"""

import json
import os
import subprocess
import tempfile
import time
import unittest
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parent.parent
TRACKER = REPO_ROOT / "src" / "hooks" / "caveman-mode-tracker.js"


class ModeTrackerTests(unittest.TestCase):
    def setUp(self):
        self._tmp = tempfile.TemporaryDirectory(prefix="caveman-tracker-")
        self.claude_dir = Path(self._tmp.name) / ".claude"
        self.claude_dir.mkdir(parents=True)
        self.flag = self.claude_dir / ".caveman-active"
        self.prev = self.claude_dir / ".caveman-active.prev"

    def tearDown(self):
        self._tmp.cleanup()

    def send(self, prompt):
        env = os.environ.copy()
        env.pop("CAVEMAN_DEFAULT_MODE", None)
        env["HOME"] = self._tmp.name
        env["USERPROFILE"] = self._tmp.name
        env["CLAUDE_CONFIG_DIR"] = str(self.claude_dir)
        return subprocess.run(
            ["node", str(TRACKER)],
            cwd=REPO_ROOT,
            env=env,
            input=json.dumps({"prompt": prompt}),
            text=True,
            capture_output=True,
            check=True,
        )

    def flag_value(self):
        return self.flag.read_text(encoding="utf-8") if self.flag.exists() else None

    # ── hook budget: act on the first complete payload, not on EOF ──────

    def test_acts_on_first_complete_payload_without_waiting_for_eof(self):
        """The hook is registered with a 5s host budget. Blocking until stdin
        EOF spends that budget waiting for a close it already has all the data
        for — the Windows pipe implementation can lag that close arbitrarily
        (#729/#833). Write one complete object, hold the pipe open, and the
        flag must land anyway."""
        env = os.environ.copy()
        env.pop("CAVEMAN_DEFAULT_MODE", None)
        env["HOME"] = self._tmp.name
        env["USERPROFILE"] = self._tmp.name
        env["CLAUDE_CONFIG_DIR"] = str(self.claude_dir)

        proc = subprocess.Popen(
            ["node", str(TRACKER)],
            cwd=REPO_ROOT,
            env=env,
            stdin=subprocess.PIPE,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            text=True,
        )
        try:
            proc.stdin.write(json.dumps({"prompt": "/caveman ultra"}))
            proc.stdin.flush()  # deliberately NOT closed
            deadline = time.time() + 5
            while time.time() < deadline and self.flag_value() is None:
                time.sleep(0.05)
            self.assertEqual(self.flag_value(), "ultra")
        finally:
            try:
                proc.stdin.close()
            except Exception:
                pass
            proc.kill()
            proc.wait(timeout=5)

    # ── #598: deactivation word orders ──────────────────────────────────

    def test_turn_caveman_mode_off_deactivates(self):
        # Pre-fix: this ACTIVATED caveman and downgraded ultra -> full.
        self.flag.write_text("ultra", encoding="utf-8")
        self.send("turn caveman mode off")
        self.assertIsNone(self.flag_value())

    def test_turn_caveman_off_deactivates(self):
        self.flag.write_text("full", encoding="utf-8")
        self.send("turn caveman off")
        self.assertIsNone(self.flag_value())

    def test_turn_off_caveman_deactivates(self):
        self.flag.write_text("full", encoding="utf-8")
        self.send("turn off caveman")
        self.assertIsNone(self.flag_value())

    def test_stop_caveman_multiline_deactivates(self):
        # Pre-fix: `.*` without the s flag never matched across lines.
        self.flag.write_text("ultra", encoding="utf-8")
        self.send("stop\ncaveman")
        self.assertIsNone(self.flag_value())

    def test_normal_mode_command_deactivates(self):
        self.flag.write_text("full", encoding="utf-8")
        self.send("normal mode")
        self.assertIsNone(self.flag_value())

    def test_back_to_normal_mode_deactivates(self):
        self.flag.write_text("full", encoding="utf-8")
        self.send("back to normal mode please")
        self.assertIsNone(self.flag_value())

    def test_vim_normal_mode_does_not_deactivate(self):
        self.flag.write_text("full", encoding="utf-8")
        self.send("how do I exit vim normal mode")
        self.assertEqual(self.flag_value(), "full")

    # ── #598: activation guards ─────────────────────────────────────────

    def test_enable_caveman_with_stop_elsewhere_activates(self):
        # Pre-fix: "stop" anywhere suppressed activation, then the
        # deactivation regex matched "caveman and stop" and deleted the flag.
        self.flag.write_text("full", encoding="utf-8")
        self.send("enable caveman and stop apologizing")
        self.assertEqual(self.flag_value(), "full")

    def test_question_does_not_activate(self):
        self.send("what is caveman mode?")
        self.assertIsNone(self.flag_value())
        self.send("does caveman lite mode drop articles?")
        self.assertIsNone(self.flag_value())

    def test_scoped_brevity_does_not_activate(self):
        self.send("be brief in the summary section")
        self.assertIsNone(self.flag_value())

    def test_unscoped_brevity_activates(self):
        self.send("be brief")
        self.assertEqual(self.flag_value(), "full")

    def test_activate_caveman_still_works(self):
        self.send("activate caveman")
        self.assertEqual(self.flag_value(), "full")

    def test_turn_on_caveman_mode_still_works(self):
        self.send("turn on caveman mode")
        self.assertEqual(self.flag_value(), "full")

    def test_talk_like_caveman_still_works(self):
        self.send("talk like a caveman")
        self.assertEqual(self.flag_value(), "full")

    def test_bare_caveman_mode_still_works(self):
        self.send("caveman mode")
        self.assertEqual(self.flag_value(), "full")

    # ── slash commands ──────────────────────────────────────────────────

    def test_slash_caveman_level_switch(self):
        self.send("/caveman ultra")
        self.assertEqual(self.flag_value(), "ultra")

    def test_slash_caveman_off(self):
        self.flag.write_text("full", encoding="utf-8")
        self.send("/caveman off")
        self.assertIsNone(self.flag_value())

    # ── #599: one-shot independent modes ────────────────────────────────

    def test_commit_restores_prior_level_on_next_prompt(self):
        self.flag.write_text("ultra", encoding="utf-8")
        self.send("/caveman-commit")
        self.assertEqual(self.flag_value(), "commit")
        r = self.send("ordinary follow-up question")
        self.assertEqual(self.flag_value(), "ultra")
        self.assertIn("CAVEMAN MODE ACTIVE (ultra)", r.stdout)

    def test_commit_with_no_prior_mode_deactivates_after(self):
        self.send("/caveman-commit")
        self.assertEqual(self.flag_value(), "commit")
        r = self.send("ordinary follow-up question")
        self.assertIsNone(self.flag_value())
        self.assertNotIn("CAVEMAN MODE ACTIVE", r.stdout)

    def test_chained_independent_modes_keep_original_prev(self):
        self.flag.write_text("wenyan-ultra", encoding="utf-8")
        self.send("/caveman-commit")
        self.send("/caveman-review")
        self.assertEqual(self.flag_value(), "review")
        self.send("ordinary follow-up question")
        self.assertEqual(self.flag_value(), "wenyan-ultra")

    def test_namespaced_commit_and_review_recognized(self):
        # Pre-fix: only compress and stats had the /caveman:caveman- variant.
        self.flag.write_text("full", encoding="utf-8")
        self.send("/caveman:caveman-commit")
        self.assertEqual(self.flag_value(), "commit")
        self.send("next prompt")  # restore
        self.send("/caveman:caveman-review")
        self.assertEqual(self.flag_value(), "review")

    def test_no_reinforcement_during_independent_turn(self):
        self.flag.write_text("full", encoding="utf-8")
        r = self.send("/caveman-commit")
        self.assertNotIn("CAVEMAN MODE ACTIVE", r.stdout)

    def test_deactivation_clears_saved_prev(self):
        self.flag.write_text("ultra", encoding="utf-8")
        self.send("/caveman-commit")
        self.send("stop caveman")
        self.assertIsNone(self.flag_value())
        self.assertFalse(self.prev.exists(), "prev file must not survive deactivation")
        self.send("ordinary prompt")
        self.assertIsNone(self.flag_value(), "nothing should resurrect the mode")


class SessionScopedModeTests(unittest.TestCase):
    """Per-session state: two Claude Code windows must not share a mode.

    Everything here drives the same hook, but with a session_id in the payload
    — the field Claude Code actually sends and the tests above deliberately
    omit, so those keep exercising the legacy machine-wide fallback.
    """

    def setUp(self):
        self._tmp = tempfile.TemporaryDirectory(prefix="caveman-session-")
        self.claude_dir = Path(self._tmp.name) / ".claude"
        self.claude_dir.mkdir(parents=True)
        self.legacy = self.claude_dir / ".caveman-active"
        self.sessions = self.claude_dir / ".caveman-sessions"

    def tearDown(self):
        self._tmp.cleanup()

    def send(self, prompt, session_id):
        env = os.environ.copy()
        env.pop("CAVEMAN_DEFAULT_MODE", None)
        env["HOME"] = self._tmp.name
        env["USERPROFILE"] = self._tmp.name
        env["CLAUDE_CONFIG_DIR"] = str(self.claude_dir)
        return subprocess.run(
            ["node", str(TRACKER)],
            cwd=REPO_ROOT,
            env=env,
            input=json.dumps({"prompt": prompt, "session_id": session_id}),
            text=True,
            capture_output=True,
            check=True,
        )

    def mode_of(self, session_id):
        p = self.sessions / f"{session_id}.mode"
        return p.read_text() if p.exists() else None

    def legacy_value(self):
        return self.legacy.read_text() if self.legacy.exists() else None

    def test_parallel_sessions_keep_separate_modes(self):
        self.send("/caveman ultra", "sessA")
        self.send("/caveman lite", "sessB")
        self.assertEqual(self.mode_of("sessA"), "ultra")
        self.assertEqual(self.mode_of("sessB"), "lite")

    def test_reinforcement_reflects_own_session(self):
        self.send("/caveman ultra", "sessA")
        self.send("/caveman lite", "sessB")
        r = self.send("ordinary prompt", "sessA")
        self.assertIn("CAVEMAN MODE ACTIVE (ultra)", r.stdout)
        self.assertNotIn("lite", r.stdout)

    def test_deactivating_one_session_leaves_the_other_active(self):
        self.send("/caveman ultra", "sessA")
        self.send("/caveman full", "sessB")
        self.send("stop caveman", "sessB")

        self.assertEqual(self.mode_of("sessB"), "off", "off must be durable on disk")
        r = self.send("ordinary prompt", "sessB")
        self.assertNotIn("CAVEMAN MODE ACTIVE", r.stdout)

        r = self.send("ordinary prompt", "sessA")
        self.assertIn("CAVEMAN MODE ACTIVE (ultra)", r.stdout)

    def test_legacy_mirror_never_holds_literal_off(self):
        # An older statusline or hook reading the legacy path must see absence,
        # not the string 'off' — it would render [CAVEMAN:OFF] / inject
        # "CAVEMAN MODE ACTIVE (off)".
        self.send("/caveman full", "sessA")
        self.assertEqual(self.legacy_value(), "full")
        self.send("stop caveman", "sessA")
        self.assertIsNone(self.legacy_value())

    def test_legacy_mirror_tracks_last_write(self):
        self.send("/caveman ultra", "sessA")
        self.send("/caveman lite", "sessB")
        self.assertEqual(self.legacy_value(), "lite")

    def test_prev_restore_is_session_scoped(self):
        # Each window runs a one-shot skill; each must return to its own level.
        self.send("/caveman ultra", "sessA")
        self.send("/caveman lite", "sessB")
        self.send("/caveman-commit", "sessA")
        self.send("/caveman-commit", "sessB")

        self.send("follow-up", "sessA")
        self.assertEqual(self.mode_of("sessA"), "ultra")
        self.send("follow-up", "sessB")
        self.assertEqual(self.mode_of("sessB"), "lite")

    def test_malformed_session_id_falls_back_to_legacy(self):
        self.send("/caveman ultra", "../../escape")
        self.assertEqual(self.legacy_value(), "ultra", "must degrade, not fail")
        self.assertFalse(
            self.sessions.exists() and any(self.sessions.iterdir()),
            "no state file may be created for a rejected session id",
        )

    def test_session_id_cannot_escape_the_sessions_directory(self):
        for bad in ["../../evil", "a/b", "..", "x" * 200]:
            self.send("/caveman ultra", bad)
        stray = list(self.claude_dir.rglob("*evil*")) + list(self.claude_dir.rglob("*.mode"))
        self.assertEqual(stray, [], f"unexpected files written: {stray}")

    def test_one_shot_skill_does_not_switch_an_off_session_on(self):
        # A stale machine-wide prev from some earlier session-less run.
        (self.claude_dir / ".caveman-active.prev").write_text("ultra")

        # This session never had caveman on, so /caveman-commit displaces
        # nothing and saves no prev. Borrowing the stale machine-wide one would
        # switch the session on at ultra when the skill's turn ended.
        self.send("/caveman-commit", "sessY")
        r = self.send("ordinary follow-up", "sessY")

        self.assertNotIn("CAVEMAN MODE ACTIVE", r.stdout)
        self.assertEqual(self.mode_of("sessY"), "off")
        self.assertEqual(
            (self.claude_dir / ".caveman-active.prev").read_text(), "ultra",
            "a session-scoped restore must not consume machine-wide state",
        )

    def test_existing_legacy_flag_is_honored_before_first_session_write(self):
        # Upgrade path: only the old flag exists when a new session starts.
        self.legacy.write_text("wenyan")
        r = self.send("ordinary prompt", "sessA")
        self.assertIn("CAVEMAN MODE ACTIVE (wenyan)", r.stdout)


if __name__ == "__main__":
    unittest.main()
