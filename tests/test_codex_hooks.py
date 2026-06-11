import json
import os
import subprocess
import tempfile
import unittest
from pathlib import Path


REPO_ROOT = Path(__file__).resolve().parent.parent
CODEX_HOOK = REPO_ROOT / "plugins/caveman/scripts/caveman-codex-hook.js"
CODEX_PLUGIN_MANIFEST = REPO_ROOT / "plugins/caveman/.codex-plugin/plugin.json"
CODEX_HOOKS_JSON = REPO_ROOT / "plugins/caveman/hooks/hooks.json"
REPO_CODEX_HOOKS_JSON = REPO_ROOT / ".codex/hooks.json"


class CodexHookScriptTests(unittest.TestCase):
    def run_hook(self, home, event, payload):
        self.assertTrue(CODEX_HOOK.is_file(), f"missing Codex hook script: {CODEX_HOOK}")
        result = subprocess.run(
            ["node", str(CODEX_HOOK), event],
            cwd=REPO_ROOT,
            env={
                **os.environ,
                "HOME": str(home),
                "USERPROFILE": str(home),
                "XDG_CONFIG_HOME": str(home / ".config"),
                "CODEX_HOME": str(home / ".codex"),
            },
            input=json.dumps(payload),
            text=True,
            encoding="utf-8",
            capture_output=True,
            check=True,
        )
        self.assertEqual(result.stderr, "")
        return result.stdout

    def with_home(self):
        return tempfile.TemporaryDirectory(prefix="caveman-codex-hooks-")

    def test_plugin_hooks_json_wires_codex_lifecycle_events(self):
        manifest = json.loads(CODEX_PLUGIN_MANIFEST.read_text(encoding="utf-8"))
        self.assertEqual(manifest["hooks"], "./hooks/hooks.json")
        self.assertTrue(CODEX_HOOKS_JSON.is_file(), f"missing Codex hooks config: {CODEX_HOOKS_JSON}")
        hooks = json.loads(CODEX_HOOKS_JSON.read_text(encoding="utf-8"))
        session_hook = hooks["hooks"]["SessionStart"][0]["hooks"][0]
        prompt_hook = hooks["hooks"]["UserPromptSubmit"][0]["hooks"][0]

        self.assertEqual(hooks["hooks"]["SessionStart"][0]["matcher"], "startup|resume|clear|compact")
        self.assertEqual(session_hook["type"], "command")
        self.assertEqual(session_hook["timeout"], 5)
        self.assertIn('node "${PLUGIN_ROOT}/scripts/caveman-codex-hook.js" SessionStart', session_hook["command"])
        self.assertIn("%PLUGIN_ROOT%", session_hook["commandWindows"])
        self.assertEqual(prompt_hook["type"], "command")
        self.assertEqual(prompt_hook["timeout"], 5)
        self.assertIn('node "${PLUGIN_ROOT}/scripts/caveman-codex-hook.js" UserPromptSubmit', prompt_hook["command"])
        self.assertIn("%PLUGIN_ROOT%", prompt_hook["commandWindows"])

    def test_repo_hooks_json_wires_native_codex_hook(self):
        self.assertTrue(REPO_CODEX_HOOKS_JSON.is_file(), f"missing repo Codex hooks config: {REPO_CODEX_HOOKS_JSON}")
        hooks = json.loads(REPO_CODEX_HOOKS_JSON.read_text(encoding="utf-8"))
        session_hook = hooks["hooks"]["SessionStart"][0]["hooks"][0]
        prompt_hook = hooks["hooks"]["UserPromptSubmit"][0]["hooks"][0]

        self.assertEqual(hooks["hooks"]["SessionStart"][0]["matcher"], "startup|resume|clear|compact")
        self.assertIn("git rev-parse --show-toplevel", session_hook["command"])
        self.assertIn("plugins/caveman/scripts/caveman-codex-hook.js", session_hook["command"])
        self.assertIn("SessionStart", session_hook["command"])
        self.assertIn("git rev-parse --show-toplevel", prompt_hook["command"])
        self.assertIn("plugins/caveman/scripts/caveman-codex-hook.js", prompt_hook["command"])
        self.assertIn("UserPromptSubmit", prompt_hook["command"])
        self.assertIn("git rev-parse --show-toplevel", session_hook["commandWindows"])
        self.assertIn("git rev-parse --show-toplevel", prompt_hook["commandWindows"])

    def test_session_start_emits_codex_context_and_records_default_mode(self):
        with self.with_home() as tmp:
            home = Path(tmp)
            stdout = self.run_hook(home, "SessionStart", {"hook_event_name": "SessionStart"})
            output = json.loads(stdout)

            self.assertTrue(output["continue"])
            self.assertEqual(output["hookSpecificOutput"]["hookEventName"], "SessionStart")
            self.assertIn("CAVEMAN MODE ACTIVE - level: full", output["hookSpecificOutput"]["additionalContext"])
            self.assertEqual((home / ".config/caveman/active-mode").read_text(encoding="utf-8"), "full\n")

    def test_session_start_does_not_overwrite_symlinked_active_mode(self):
        with self.with_home() as tmp:
            home = Path(tmp)
            config_dir = home / ".config/caveman"
            config_dir.mkdir(parents=True)
            decoy = home / "decoy.txt"
            decoy.write_text("SECRET\n", encoding="utf-8")
            try:
                os.symlink(decoy, config_dir / "active-mode")
            except (AttributeError, NotImplementedError, OSError):
                self.skipTest("symlinks unavailable")

            self.run_hook(home, "SessionStart", {"hook_event_name": "SessionStart"})

            self.assertEqual(decoy.read_text(encoding="utf-8"), "SECRET\n")

    def test_prompt_submit_tracks_alias_modes_and_emits_reinforcement(self):
        with self.with_home() as tmp:
            home = Path(tmp)
            stdout = self.run_hook(
                home,
                "UserPromptSubmit",
                {"hook_event_name": "UserPromptSubmit", "prompt": "$caveman ultra"},
            )
            output = json.loads(stdout)

            self.assertEqual(output["hookSpecificOutput"]["hookEventName"], "UserPromptSubmit")
            self.assertIn("CAVEMAN MODE ACTIVE (ultra)", output["hookSpecificOutput"]["additionalContext"])
            self.assertEqual((home / ".config/caveman/active-mode").read_text(encoding="utf-8"), "ultra\n")

    def test_normal_mode_clears_active_mode_without_context(self):
        with self.with_home() as tmp:
            home = Path(tmp)
            flag = home / ".config/caveman/active-mode"
            flag.parent.mkdir(parents=True)
            flag.write_text("full\n", encoding="utf-8")

            stdout = self.run_hook(
                home,
                "UserPromptSubmit",
                {"hook_event_name": "UserPromptSubmit", "prompt": "normal mode"},
            )

            self.assertEqual(stdout, "")
            self.assertFalse(flag.exists())

    def test_negated_caveman_prompt_clears_active_mode_without_context(self):
        with self.with_home() as tmp:
            home = Path(tmp)
            flag = home / ".config/caveman/active-mode"
            flag.parent.mkdir(parents=True)
            flag.write_text("full\n", encoding="utf-8")

            stdout = self.run_hook(
                home,
                "UserPromptSubmit",
                {"hook_event_name": "UserPromptSubmit", "prompt": "do not use caveman"},
            )

            self.assertEqual(stdout, "")
            self.assertFalse(flag.exists())

    def test_caveman_stats_blocks_prompt_with_codex_transcript_usage(self):
        with self.with_home() as tmp:
            home = Path(tmp)
            transcript = home / ".codex/sessions/session.jsonl"
            transcript.parent.mkdir(parents=True)
            transcript.write_text(
                "\n".join(
                    json.dumps(line)
                    for line in [
                        {"type": "event_msg", "payload": {"type": "user_message", "message": "/caveman"}},
                        {"type": "event_msg", "payload": {"type": "user_message", "message": "/caveman-stats"}},
                        {
                            "type": "event_msg",
                            "payload": {
                                "type": "token_count",
                                "info": {
                                    "total_token_usage": {
                                        "input_tokens": 1234,
                                        "output_tokens": 456,
                                        "total_tokens": 1690,
                                    }
                                },
                            },
                        },
                    ]
                )
                + "\n",
                encoding="utf-8",
            )

            stdout = self.run_hook(
                home,
                "UserPromptSubmit",
                {
                    "hook_event_name": "UserPromptSubmit",
                    "prompt": "/caveman-stats",
                    "transcript_path": str(transcript),
                },
            )
            output = json.loads(stdout)

            self.assertEqual(output["decision"], "block")
            self.assertIn("Session: 2 turns", output["reason"])
            self.assertIn("Input:   1,234 tokens", output["reason"])
            self.assertIn("Output:  456 tokens (caveman)", output["reason"])
            self.assertIn("Saved:    847 tokens (~65%)", output["reason"])

    def test_caveman_stats_requires_explicit_safe_codex_transcript(self):
        with self.with_home() as tmp:
            home = Path(tmp)
            sessions = home / ".codex/sessions"
            sessions.mkdir(parents=True)
            (sessions / "latest.jsonl").write_text(
                json.dumps(
                    {
                        "type": "event_msg",
                        "payload": {
                            "type": "token_count",
                            "info": {
                                "total_token_usage": {
                                    "input_tokens": 1,
                                    "output_tokens": 1,
                                    "total_tokens": 2,
                                }
                            },
                        },
                    }
                )
                + "\n",
                encoding="utf-8",
            )

            stdout = self.run_hook(
                home,
                "UserPromptSubmit",
                {"hook_event_name": "UserPromptSubmit", "prompt": "/caveman-stats"},
            )
            output = json.loads(stdout)

            self.assertEqual(output["decision"], "block")
            self.assertIn("no Codex session transcript found", output["reason"])

    def test_caveman_stats_rejects_transcript_outside_codex_sessions(self):
        with self.with_home() as tmp:
            home = Path(tmp)
            outside = home / "session.jsonl"
            outside.write_text(
                json.dumps(
                    {
                        "type": "event_msg",
                        "payload": {
                            "type": "token_count",
                            "info": {
                                "total_token_usage": {
                                    "input_tokens": 1234,
                                    "output_tokens": 456,
                                    "total_tokens": 1690,
                                }
                            },
                        },
                    }
                )
                + "\n",
                encoding="utf-8",
            )

            stdout = self.run_hook(
                home,
                "UserPromptSubmit",
                {
                    "hook_event_name": "UserPromptSubmit",
                    "prompt": "/caveman-stats",
                    "transcript_path": str(outside),
                },
            )
            output = json.loads(stdout)

            self.assertEqual(output["decision"], "block")
            self.assertIn("no Codex session transcript found", output["reason"])


if __name__ == "__main__":
    unittest.main()
