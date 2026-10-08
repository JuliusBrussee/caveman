"""Tests for the data-loss guards in `compress_file` (issue #237).

The compress orchestrator used to overwrite the input even when Claude
returned an empty string or a no-op echo, and used to write a backup
without verifying that the bytes survived the round-trip. These tests
pin the new defensive checks: nothing on disk changes when the compressed
output is empty or identical to the input, and a backup-write that drops
bytes is detected before the input is overwritten.
"""

import contextlib
import io
import json
import os
import stat
import sys
import tempfile
import unittest
from contextlib import contextmanager
from pathlib import Path
from unittest import mock

REPO_ROOT = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(REPO_ROOT / "skills" / "caveman-compress"))

from scripts import compress as compress_mod  # noqa: E402

LLM_ENV_KEYS = (
    "ANTHROPIC_API_KEY",
    "CAVEMAN_MODEL",
    "CAVEMAN_PROVIDER",
    "CAVEMAN_COMPRESS_MODEL",
    "CAVEMAN_COMPRESS_PROVIDER",
    "CAVEMAN_COMPRESS_ENDPOINT",
    "CAVEMAN_COMPRESS_API_KEY",
)
OPENCODE_PROVIDER = "opencode"
OPENCODE_MODEL = "github-copilot/gpt-4.1"
PROMPT_TEXT = "Compress this memory."
OPENCODE_OUTPUT = "Memory compressed."
OPENCODE_BIN = "/usr/local/bin/opencode"
OPENCODE_PROMPT_MESSAGE = "Follow the attached prompt exactly. Return only the final answer."
OPENCODE_FILE_ARG = "--file"
CLAUDE_MODEL = "claude-haiku-4-5"
CLAUDE_OUTPUT = "Claude compressed."
CLAUDE_BIN = "/usr/local/bin/claude"


@contextmanager
def llm_env(**overrides):
    original = {key: os.environ.get(key) for key in LLM_ENV_KEYS}
    try:
        for key in LLM_ENV_KEYS:
            os.environ.pop(key, None)
        os.environ.update(overrides)
        yield
    finally:
        for key, value in original.items():
            if value is None:
                os.environ.pop(key, None)
            else:
                os.environ[key] = value


class CompressSafetyTests(unittest.TestCase):
    def _file_with(self, dirpath: Path, text: str) -> Path:
        path = dirpath / "task.md"
        # newline="" or Python's text mode rewrites every \n as \r\n on Windows,
        # so the fixture would land as CRLF. compress.py then correctly
        # PRESERVES the source's line endings (issue #762) and the assertions
        # below, which compare against LF strings, fail — measuring the
        # fixture's line endings rather than the compressor's behaviour.
        path.write_text(text, encoding="utf-8", newline="")
        return path

    def test_empty_input_refused(self):
        with tempfile.TemporaryDirectory() as tmp:
            path = self._file_with(Path(tmp), "")
            with mock.patch.object(compress_mod, "call_claude") as call:
                ok = compress_mod.compress_file(path)
            self.assertFalse(ok)
            call.assert_not_called()
            self.assertEqual(path.read_text(encoding="utf-8"), "")
            self.assertFalse((Path(tmp) / "task.original.md").exists())

    def test_empty_compressed_output_does_not_touch_disk(self):
        with tempfile.TemporaryDirectory() as tmp:
            original = "# Heading\n\nSome long natural language paragraph that should be compressed.\n"
            path = self._file_with(Path(tmp), original)
            with mock.patch.object(compress_mod, "call_claude", return_value=""):
                ok = compress_mod.compress_file(path)
            self.assertFalse(ok)
            self.assertEqual(path.read_text(encoding="utf-8"), original)
            self.assertFalse((Path(tmp) / "task.original.md").exists())

    def test_whitespace_only_compressed_output_does_not_touch_disk(self):
        with tempfile.TemporaryDirectory() as tmp:
            original = "# Heading\n\nProse that should change.\n"
            path = self._file_with(Path(tmp), original)
            with mock.patch.object(compress_mod, "call_claude", return_value="   \n  "):
                ok = compress_mod.compress_file(path)
            self.assertFalse(ok)
            self.assertEqual(path.read_text(encoding="utf-8"), original)
            self.assertFalse((Path(tmp) / "task.original.md").exists())

    def test_identical_compressed_output_does_not_touch_disk(self):
        with tempfile.TemporaryDirectory() as tmp:
            original = "# Heading\n\nProse.\n"
            path = self._file_with(Path(tmp), original)
            with mock.patch.object(compress_mod, "call_claude", return_value=original):
                ok = compress_mod.compress_file(path)
            self.assertFalse(ok)
            self.assertEqual(path.read_text(encoding="utf-8"), original)
            self.assertFalse((Path(tmp) / "task.original.md").exists())

    def test_expanded_compressed_output_does_not_touch_disk(self):
        with tempfile.TemporaryDirectory() as tmp:
            original = "# Heading\n\nFox jump dog.\n"
            expanded = "# Heading\n\nThe quick brown fox jumps over the lazy dog, repeatedly.\n"
            path = self._file_with(Path(tmp), original)
            with mock.patch.object(compress_mod, "call_claude", return_value=expanded):
                ok = compress_mod.compress_file(path)
            self.assertFalse(ok)
            self.assertEqual(path.read_text(encoding="utf-8"), original)
            self.assertFalse((Path(tmp) / "task.original.md").exists())

    def test_same_length_compressed_output_does_not_touch_disk(self):
        with tempfile.TemporaryDirectory() as tmp:
            original = "# Heading\n\nFox jump dog now.\n"
            same_length = "# Heading\n\nDog jump fox now.\n"
            self.assertEqual(len(original.strip()), len(same_length.strip()))
            path = self._file_with(Path(tmp), original)
            with mock.patch.object(compress_mod, "call_claude", return_value=same_length):
                ok = compress_mod.compress_file(path)
            self.assertFalse(ok)
            self.assertEqual(path.read_text(encoding="utf-8"), original)
            self.assertFalse((Path(tmp) / "task.original.md").exists())

    def test_expanded_retry_candidate_does_not_touch_disk(self):
        # The size guard runs once, on the FIRST candidate. If that candidate
        # fails validation, build_fix_prompt's repaired candidate is assigned
        # straight to `compressed` and validated — so a repair that is longer
        # than the original could still be written over the source and
        # reported as a successful compression, which is #776 again on the
        # retry path. Here the first candidate is smaller but drops a heading
        # (so validate() rejects it), and the repair restores the heading
        # while being longer than the input.
        with tempfile.TemporaryDirectory() as tmp, \
             tempfile.TemporaryDirectory() as data_home, \
             mock.patch.dict(os.environ, {"XDG_DATA_HOME": data_home, "LOCALAPPDATA": data_home}):
            original = "# Heading\n\n## Sub\n\nThe quick brown fox jumps over the lazy dog.\n"
            # Structurally invalid (## Sub missing) but shorter — passes the
            # size guard, fails validate().
            first = "# Heading\n\nFox jump dog.\n"
            # Structurally faithful, and longer than the original.
            repair = (
                "# Heading\n\n## Sub\n\nThe quick brown fox jumps over the lazy dog, "
                "and then jumps over it again, repeatedly and at length.\n"
            )
            self.assertGreater(len(repair.strip()), len(original.strip()))
            path = self._file_with(Path(tmp), original)
            with mock.patch.object(compress_mod, "call_claude", side_effect=[first, repair, repair]):
                ok = compress_mod.compress_file(path)
            self.assertFalse(ok)
            self.assertEqual(path.read_text(encoding="utf-8"), original)
            backup = compress_mod.backup_dir_for(path.resolve()) / "task.original.md"
            self.assertFalse(backup.exists())
            self.assertFalse((Path(tmp) / "task.md.caveman-staged").exists())

    def test_real_compression_writes_backup_and_target(self):
        # Isolate the backup data dir to a temp location so the out-of-tree
        # backup (issue #420) never lands in the developer's real home dir.
        with tempfile.TemporaryDirectory() as tmp, \
             tempfile.TemporaryDirectory() as data_home, \
             mock.patch.dict(os.environ, {"XDG_DATA_HOME": data_home, "LOCALAPPDATA": data_home}):
            original = "# Heading\n\nThe quick brown fox jumps over the lazy dog.\n"
            compressed = "# Heading\n\nFox jump dog.\n"
            path = self._file_with(Path(tmp), original)
            with mock.patch.object(compress_mod, "call_claude", return_value=compressed), \
                 mock.patch.object(compress_mod, "validate") as v:
                v.return_value = mock.Mock(is_valid=True, errors=[], warnings=[])
                ok = compress_mod.compress_file(path)
            self.assertTrue(ok)
            self.assertEqual(path.read_text(encoding="utf-8"), compressed)
            # Backups now live OUTSIDE the source dir (issue #420), under a
            # platform-aware data dir mirroring the source parent name.
            backup = compress_mod.backup_dir_for(path.resolve()) / "task.original.md"
            self.assertEqual(backup.read_text(encoding="utf-8"), original)
            self.assertFalse((Path(tmp) / "task.original.md").exists())

    def test_utf8_roundtrip_survives_compression(self):
        # Path.read_text() without encoding= would decode with the system
        # locale codec (cp1252/cp949 on Windows) and could silently mangle
        # non-ASCII bytes. Read raw bytes and decode strictly as UTF-8 so the
        # assertion is locale-independent (issue #686).
        with tempfile.TemporaryDirectory() as tmp, \
             tempfile.TemporaryDirectory() as data_home, \
             mock.patch.dict(os.environ, {"XDG_DATA_HOME": data_home, "LOCALAPPDATA": data_home}):
            original = "# Heading\n\nCafé, 中文, and an arrow → here.\n"
            compressed = "# Heading\n\nCafé 中文 arrow → here.\n"
            path = self._file_with(Path(tmp), original)
            with mock.patch.object(compress_mod, "call_claude", return_value=compressed), \
                 mock.patch.object(compress_mod, "validate") as v:
                v.return_value = mock.Mock(is_valid=True, errors=[], warnings=[])
                ok = compress_mod.compress_file(path)
            self.assertTrue(ok)
            self.assertEqual(path.read_bytes().decode("utf-8"), compressed)
            backup = compress_mod.backup_dir_for(path.resolve()) / "task.original.md"
            self.assertEqual(backup.read_bytes().decode("utf-8"), original)

    def test_write_text_atomic_leaves_destination_untouched_on_encode_failure(self):
        # Direct unit test of the atomic-write primitive: an encode failure
        # partway through must not truncate the destination or leave a *.tmp
        # file behind (issue #655).
        class ExplodingStr(str):
            def encode(self, *args, **kwargs):
                raise UnicodeEncodeError("utf-8", self, 0, 1, "forced failure")

        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / "task.md"
            path.write_text("original content", encoding="utf-8")

            with self.assertRaises(UnicodeEncodeError):
                compress_mod.write_text_atomic(path, ExplodingStr("new content"))

            self.assertEqual(path.read_text(encoding="utf-8"), "original content")
            self.assertEqual(list(Path(tmp).glob("*.tmp")), [])

    def test_forced_primary_write_failure_leaves_original_and_backup_intact(self):
        # Same failure, exercised through the full compress_file pipeline:
        # the backup must already exist and be intact, the target must be
        # untouched, and no *.tmp litter must remain in either directory.
        with tempfile.TemporaryDirectory() as tmp, \
             tempfile.TemporaryDirectory() as data_home, \
             mock.patch.dict(os.environ, {"XDG_DATA_HOME": data_home, "LOCALAPPDATA": data_home}):
            original = "# Heading\n\nProse to compress.\n"
            compressed = "# Heading\n\nProse.\n"
            path = self._file_with(Path(tmp), original)
            target = path.resolve()
            real_write_text_atomic = compress_mod.write_text_atomic

            def flaky_write(write_path, text, newline="\n"):
                if write_path == target:
                    raise UnicodeEncodeError("utf-8", text, 0, 1, "forced failure")
                return real_write_text_atomic(write_path, text, newline)

            with mock.patch.object(compress_mod, "call_claude", return_value=compressed), \
                 mock.patch.object(compress_mod, "validate") as v, \
                 mock.patch.object(compress_mod, "write_text_atomic", side_effect=flaky_write):
                v.return_value = mock.Mock(is_valid=True, errors=[], warnings=[])
                with self.assertRaises(UnicodeEncodeError):
                    compress_mod.compress_file(path)

            self.assertEqual(path.read_text(encoding="utf-8"), original)
            backup_dir = compress_mod.backup_dir_for(target)
            backup = backup_dir / "task.original.md"
            self.assertEqual(backup.read_text(encoding="utf-8"), original)
            self.assertEqual(list(Path(tmp).glob("*.tmp")), [])
            self.assertEqual(list(backup_dir.glob("*.tmp")), [])

    @unittest.skipIf(os.name == "nt", "Windows ACLs are not represented by POSIX mode bits")
    def test_permission_preserved_across_compression(self):
        with tempfile.TemporaryDirectory() as tmp, \
             tempfile.TemporaryDirectory() as data_home, \
             mock.patch.dict(os.environ, {"XDG_DATA_HOME": data_home, "LOCALAPPDATA": data_home}):
            original = "# Heading\n\nProse to compress.\n"
            compressed = "# Heading\n\nProse.\n"
            path = self._file_with(Path(tmp), original)
            path.chmod(0o644)
            with mock.patch.object(compress_mod, "call_claude", return_value=compressed), \
                 mock.patch.object(compress_mod, "validate") as v:
                v.return_value = mock.Mock(is_valid=True, errors=[], warnings=[])
                ok = compress_mod.compress_file(path)
            self.assertTrue(ok)
            self.assertEqual(stat.S_IMODE(path.stat().st_mode), 0o644)

    def test_retry_preamble_output_rejected_and_not_written(self):
        # A fix-retry response with a prose preamble ahead of the real content
        # must never reach disk, staging included, and the live file must be
        # left holding the original (issue #588).
        with tempfile.TemporaryDirectory() as tmp, \
             tempfile.TemporaryDirectory() as data_home, \
             mock.patch.dict(os.environ, {"XDG_DATA_HOME": data_home, "LOCALAPPDATA": data_home}):
            original = "# Heading\n\nProse that fails validation.\n"
            first_pass = "# Heading\n\nCompressed prose.\n"
            preamble_fix = "Here is the fixed file:\n\n# Heading\n\nCompressed prose, fixed.\n"
            path = self._file_with(Path(tmp), original)

            invalid = mock.Mock(is_valid=False, errors=["some validation error"], warnings=[])
            written_texts = []
            real_write_target = compress_mod._write_target

            def spy_write_target(target_path, text, backup_path, newline="\n"):
                written_texts.append(text)
                return real_write_target(target_path, text, backup_path, newline)

            with mock.patch.object(
                compress_mod, "call_claude", side_effect=[first_pass, preamble_fix]
            ), mock.patch.object(compress_mod, "validate", return_value=invalid), \
                 mock.patch.object(compress_mod, "_write_target", side_effect=spy_write_target):
                ok = compress_mod.compress_file(path)

            self.assertFalse(ok)
            self.assertNotIn(preamble_fix, written_texts)
            self.assertEqual(path.read_text(encoding="utf-8"), original)

    def test_live_file_never_holds_unvalidated_output_before_validation_passes(self):
        # validate() must never see the live file already holding the
        # un-validated candidate (issue #544).
        with tempfile.TemporaryDirectory() as tmp, \
             tempfile.TemporaryDirectory() as data_home, \
             mock.patch.dict(os.environ, {"XDG_DATA_HOME": data_home, "LOCALAPPDATA": data_home}):
            original = "# Heading\n\nProse to compress, long enough to pass the identity check here.\n"
            compressed = "# Heading\n\nProse.\n"
            path = self._file_with(Path(tmp), original)

            seen_live_contents = []

            def spy_validate(orig_path, comp_path):
                seen_live_contents.append(path.read_text(encoding="utf-8"))
                return mock.Mock(is_valid=True, errors=[], warnings=[])

            with mock.patch.object(compress_mod, "call_claude", return_value=compressed), \
                 mock.patch.object(compress_mod, "validate", side_effect=spy_validate):
                ok = compress_mod.compress_file(path)

            self.assertTrue(ok)
            self.assertEqual(seen_live_contents, [original])
            self.assertEqual(path.read_text(encoding="utf-8"), compressed)
            self.assertFalse((Path(tmp) / (path.name + ".caveman-staged")).exists())

    def test_live_file_untouched_when_all_validation_attempts_fail(self):
        with tempfile.TemporaryDirectory() as tmp, \
             tempfile.TemporaryDirectory() as data_home, \
             mock.patch.dict(os.environ, {"XDG_DATA_HOME": data_home, "LOCALAPPDATA": data_home}):
            original = "# Heading\n\nProse to compress, long enough to pass the identity check here.\n"
            compressed = "# Heading\n\nProse.\n"
            path = self._file_with(Path(tmp), original)

            invalid = mock.Mock(is_valid=False, errors=["some validation error"], warnings=[])
            seen_live_contents = []

            def spy_validate(orig_path, comp_path):
                seen_live_contents.append(path.read_text(encoding="utf-8"))
                return invalid

            with mock.patch.object(compress_mod, "call_claude", return_value=compressed), \
                 mock.patch.object(compress_mod, "validate", side_effect=spy_validate):
                ok = compress_mod.compress_file(path)

            self.assertFalse(ok)
            self.assertEqual(seen_live_contents, [original] * compress_mod.MAX_RETRIES)
            self.assertEqual(path.read_text(encoding="utf-8"), original)
            self.assertFalse((Path(tmp) / (path.name + ".caveman-staged")).exists())

    def test_non_utf8_input_refused_before_anything_is_written(self):
        """errors="ignore" used to drop the undecodable byte, write the mangled
        text to the backup, pass the mangled-vs-mangled readback check, then
        overwrite the original — losing the byte with no error (issue #686)."""
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / "task.md"
            # cp1252 "caf<e-acute>" — 0xe9 is not valid UTF-8.
            raw = b"# Notes\n\nCaf\xe9 build steps are long and prose-like.\n"
            path.write_bytes(raw)

            with mock.patch.object(compress_mod, "call_claude") as call:
                with self.assertRaises(ValueError) as ctx:
                    compress_mod.compress_file(path)

            call.assert_not_called()
            self.assertIn("not valid UTF-8", str(ctx.exception))
            self.assertEqual(path.read_bytes(), raw)
            backup_dir = compress_mod.backup_dir_for(path)
            self.assertFalse((backup_dir / "task.original.md").exists())

    def test_sensitive_directory_names_are_blocked(self):
        self.assertTrue(
            compress_mod.is_sensitive_path(Path("C:/dev/CREDENTIALS/hetzner/webhosting.md"))
        )
        self.assertTrue(compress_mod.is_sensitive_path(Path("project/secrets/service-notes.md")))
        self.assertTrue(compress_mod.is_sensitive_path(Path("project/secret/service-notes.md")))
        self.assertTrue(compress_mod.is_sensitive_path(Path("project/api-keys/service-notes.md")))
        self.assertTrue(compress_mod.is_sensitive_path(Path("project/private_keys/service-notes.md")))
        self.assertFalse(compress_mod.is_sensitive_path(Path("project/docs/service-notes.md")))

    def test_code_blocks_are_masked_before_model_and_restored_byte_exact(self):
        original = (
            "# Tree\n\nProse before.\n\n"
            "```text\nroot\n├── src\n│   └── app.py\n```\n\n"
            "    indented()\n    code()\n\nProse after.\n"
        )
        masked, blocks = compress_mod.mask_code_blocks(original)
        self.assertNotIn("├── src", masked)
        self.assertNotIn("indented()", masked)
        self.assertEqual(len(blocks), 2)
        self.assertEqual(compress_mod.restore_code_blocks(masked, blocks), original)
        compressed = masked.replace("Prose before.", "Before.").replace("Prose after.", "After.")
        restored = compress_mod.restore_code_blocks(compressed, blocks)
        self.assertIn("```text\nroot\n├── src\n│   └── app.py\n```", restored)
        self.assertIn("    indented()\n    code()", restored)

    def test_missing_or_duplicated_code_marker_fails_closed(self):
        masked, blocks = compress_mod.mask_code_blocks("```sh\necho safe\n```\n")
        marker = blocks[0][0]
        with self.assertRaisesRegex(ValueError, "changed preserved code marker"):
            compress_mod.restore_code_blocks(masked.replace(marker, ""), blocks)
        with self.assertRaisesRegex(ValueError, "changed preserved code marker"):
            compress_mod.restore_code_blocks(masked + marker, blocks)

    def test_crlf_line_endings_survive_the_round_trip(self):
        """Reading with universal newlines and writing back "\n" rewrote every
        line ending in every file the tool touched (issue #762)."""
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / "task.md"
            path.write_bytes(b"# Title\r\n\r\nSome long prose body to compress here.\r\n")
            compressed = "# Title\n\nShort body.\n"

            with mock.patch.object(compress_mod, "call_claude", return_value=compressed), \
                 mock.patch.object(compress_mod, "validate") as v:
                v.return_value = mock.Mock(is_valid=True, errors=[], warnings=[])
                ok = compress_mod.compress_file(path)

            self.assertTrue(ok)
            out = path.read_bytes()
            self.assertNotIn(b"\n", out.replace(b"\r\n", b""))
            backup = compress_mod.backup_dir_for(path) / "task.original.md"
            self.assertIn(b"\r\n", backup.read_bytes())
            backup.unlink()

    def test_one_crlf_line_does_not_convert_an_lf_document(self):
        """A single pasted CRLF line used to rewrite every ending in the file —
        and the backup with it, so the original bytes were unrecoverable."""
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / "task.md"
            raw = b"# Title\nline one\r\nline two\nlong prose body to compress.\n"
            path.write_bytes(raw)

            with mock.patch.object(compress_mod, "call_claude", return_value="# Title\n\nShort body.\n"), \
                 mock.patch.object(compress_mod, "validate") as v:
                v.return_value = mock.Mock(is_valid=True, errors=[], warnings=[])
                ok = compress_mod.compress_file(path)

            self.assertTrue(ok)
            self.assertNotIn(b"\r\n", path.read_bytes())
            backup = compress_mod.backup_dir_for(path) / "task.original.md"
            self.assertEqual(backup.read_bytes(), raw)
            backup.unlink()

    def test_opencode_provider_uses_configured_model(self):
        def run_opencode(command, **kwargs):
            prompt_path = Path(command[command.index(OPENCODE_FILE_ARG) + 1])
            self.assertEqual(prompt_path.read_text(encoding="utf-8"), PROMPT_TEXT)
            return mock.Mock(stdout=OPENCODE_OUTPUT)

        with llm_env(
            CAVEMAN_COMPRESS_PROVIDER=OPENCODE_PROVIDER,
            CAVEMAN_COMPRESS_MODEL=OPENCODE_MODEL,
        ), \
             mock.patch.object(compress_mod.shutil, "which", return_value=OPENCODE_BIN), \
             mock.patch.object(compress_mod.subprocess, "run", side_effect=run_opencode) as run:
            output = compress_mod.call_claude(PROMPT_TEXT)

        self.assertEqual(output, OPENCODE_OUTPUT)
        run.assert_called_once()
        command = run.call_args.args[0]
        prompt_path = Path(command[command.index(OPENCODE_FILE_ARG) + 1])
        self.assertEqual(command[:4], [OPENCODE_BIN, "run", "--model", OPENCODE_MODEL])
        self.assertEqual(command[-1], OPENCODE_PROMPT_MESSAGE)
        self.assertNotIn(PROMPT_TEXT, command)
        self.assertNotEqual(prompt_path.parent, Path.cwd())
        self.assertFalse(prompt_path.exists())
        kwargs = dict(run.call_args.kwargs)
        kwargs.pop("env")  # asserted in test_opencode_runs_standalone_with_tools_denied
        self.assertEqual(
            kwargs,
            {
                "text": True,
                "capture_output": True,
                "check": True,
                "encoding": "utf-8",
                "errors": "replace",
                "timeout": compress_mod.CLAUDE_CALL_TIMEOUT_SECONDS,
            },
        )

    def test_opencode_runs_standalone_with_tools_denied(self):
        # The file being compressed is untrusted input sent as a prompt to
        # opencode's agent. opencode 2.x allows every action not denied
        # (websearch, MCP tools, subagents...), so deny all of them.
        # The background service ignores the client's env, so the deny
        # config only applies to a private --standalone server.
        completed = mock.Mock(stdout=OPENCODE_OUTPUT)
        with llm_env(CAVEMAN_COMPRESS_PROVIDER=OPENCODE_PROVIDER), \
             mock.patch.dict(os.environ, {"OPENCODE_CONFIG_CONTENT": '{"model": "x/y"}'}), \
             mock.patch.object(compress_mod.subprocess, "run", return_value=completed) as run:
            compress_mod.call_claude(PROMPT_TEXT)

        command = run.call_args.args[0]
        self.assertIn("--standalone", command)
        self.assertNotIn("--auto", command)
        config = json.loads(run.call_args.kwargs["env"]["OPENCODE_CONFIG_CONTENT"])
        self.assertEqual(config["permission"], {"*": "deny"})
        self.assertEqual(config["model"], "x/y")  # user's inline config kept

    def test_opencode_runs_a_dedicated_deny_all_agent(self):
        # opencode applies a per-agent rule after the global one and the last
        # matching rule wins, so `agent.build.permission.edit: "allow"` in the
        # user's own config re-enables edit for the default agent (probed on
        # 2.0.22). An agent only compress defines can't be re-allowed that way.
        completed = mock.Mock(stdout=OPENCODE_OUTPUT)
        inline = '{"agent": {"plan": {"model": "x/y"}}}'
        with llm_env(CAVEMAN_COMPRESS_PROVIDER=OPENCODE_PROVIDER), \
             mock.patch.dict(os.environ, {"OPENCODE_CONFIG_CONTENT": inline}), \
             mock.patch.object(compress_mod.subprocess, "run", return_value=completed) as run:
            compress_mod.call_claude(PROMPT_TEXT)

        command = run.call_args.args[0]
        self.assertEqual(command[command.index("--agent") + 1], "caveman-compress")
        config = json.loads(run.call_args.kwargs["env"]["OPENCODE_CONFIG_CONTENT"])
        self.assertEqual(
            config["agent"],
            {"plan": {"model": "x/y"}, "caveman-compress": {"permission": {"*": "deny"}}},
        )

    def test_opencode_unparseable_inline_config_is_named(self):
        # opencode reads OPENCODE_CONFIG_CONTENT as JSONC; json.loads does not.
        for bad in ('{"model": "x/y", // pinned\n}', "[]"):
            with self.subTest(bad=bad), \
                 llm_env(CAVEMAN_COMPRESS_PROVIDER=OPENCODE_PROVIDER), \
                 mock.patch.dict(os.environ, {"OPENCODE_CONFIG_CONTENT": bad}), \
                 mock.patch.object(compress_mod.subprocess, "run") as run:
                with self.assertRaisesRegex(RuntimeError, "OPENCODE_CONFIG_CONTENT"):
                    compress_mod.call_claude(PROMPT_TEXT)
                run.assert_not_called()

    def test_compression_status_names_configured_provider(self):
        with tempfile.TemporaryDirectory() as tmp:
            path = self._file_with(
                Path(tmp),
                "# Title\n\nA sufficiently long body for provider status testing.\n",
            )
            with llm_env(CAVEMAN_COMPRESS_PROVIDER=OPENCODE_PROVIDER), \
                 mock.patch.object(compress_mod, "call_claude", return_value=""), \
                 mock.patch("builtins.print") as print_message:
                ok = compress_mod.compress_file(path)

        self.assertFalse(ok)
        print_message.assert_any_call("Compressing with opencode...")
        print_message.assert_any_call(
            "❌ Compression aborted: opencode returned an empty response."
        )

    def test_claude_cli_uses_configured_model(self):
        completed = mock.Mock(stdout=CLAUDE_OUTPUT)
        with llm_env(CAVEMAN_COMPRESS_MODEL=CLAUDE_MODEL), \
             mock.patch.object(compress_mod.shutil, "which", return_value=CLAUDE_BIN), \
             mock.patch.object(compress_mod.subprocess, "run", return_value=completed) as run:
            output = compress_mod.call_claude(PROMPT_TEXT)

        self.assertEqual(output, CLAUDE_OUTPUT)
        run.assert_called_once_with(
            [
                CLAUDE_BIN,
                "--model",
                CLAUDE_MODEL,
                "--print",
                "--setting-sources",
                "",
                "--strict-mcp-config",
            ],
            text=True,
            capture_output=True,
            check=True,
            encoding="utf-8",
            errors="replace",
            timeout=compress_mod.CLAUDE_CALL_TIMEOUT_SECONDS,
            input=PROMPT_TEXT,
        )

    def test_provider_specific_environment_takes_precedence(self):
        with llm_env(
            CAVEMAN_COMPRESS_PROVIDER=OPENCODE_PROVIDER,
            CAVEMAN_MODEL="fallback/model",
            CAVEMAN_COMPRESS_MODEL=OPENCODE_MODEL,
        ):
            self.assertEqual(compress_mod.configured_provider(), OPENCODE_PROVIDER)
            self.assertEqual(compress_mod.configured_model(), OPENCODE_MODEL)
        with llm_env(CAVEMAN_MODEL="fallback/model"):
            self.assertEqual(compress_mod.configured_model(), "fallback/model")

    def test_generic_caveman_provider_is_not_read(self):
        # Only CAVEMAN_COMPRESS_PROVIDER picks the provider; a generic
        # CAVEMAN_PROVIDER would collide with CLI/proxy configuration.
        with llm_env(CAVEMAN_PROVIDER=OPENCODE_PROVIDER):
            self.assertEqual(compress_mod.configured_provider(), "claude")

    def test_explicit_anthropic_provider_requires_api_key(self):
        with llm_env(CAVEMAN_COMPRESS_PROVIDER="anthropic"):
            with self.assertRaisesRegex(RuntimeError, "ANTHROPIC_API_KEY is required"):
                compress_mod.call_claude(PROMPT_TEXT)

    def test_opencode_cleanup_failure_does_not_mask_the_real_error(self):
        # A failed temp-file unlink in the finally block used to raise its own
        # RuntimeError, replacing the opencode failure the user needs to see.
        prompt_paths = []
        failure = compress_mod.subprocess.CalledProcessError(
            1, [OPENCODE_BIN, "run"], stderr="model not found",
        )

        def run_opencode(command, **kwargs):
            prompt_paths.append(Path(command[command.index(OPENCODE_FILE_ARG) + 1]))
            raise failure

        try:
            with llm_env(CAVEMAN_COMPRESS_PROVIDER=OPENCODE_PROVIDER), \
                 mock.patch.object(compress_mod.subprocess, "run", side_effect=run_opencode), \
                 mock.patch.object(Path, "unlink", side_effect=OSError("denied")), \
                 mock.patch("sys.stderr", new_callable=io.StringIO) as stderr:
                with self.assertRaisesRegex(RuntimeError, "opencode call failed:\nmodel not found"):
                    compress_mod.call_claude(PROMPT_TEXT)
        finally:
            for prompt_path in prompt_paths:
                prompt_path.unlink(missing_ok=True)

        self.assertIn("warning: could not delete temporary opencode prompt", stderr.getvalue())
        self.assertIn(str(prompt_paths[0]), stderr.getvalue())

    def test_opencode_prompt_is_removed_when_write_fails(self):
        prompt_paths = []
        named_temporary_file = tempfile.NamedTemporaryFile

        class FailingPromptFile:
            def __init__(self, *args, **kwargs):
                self._prompt_file = named_temporary_file(*args, **kwargs)
                self.name = self._prompt_file.name
                prompt_paths.append(Path(self.name))

            def __enter__(self):
                return self

            def __exit__(self, exc_type, exc_value, traceback):
                return self._prompt_file.__exit__(exc_type, exc_value, traceback)

            def write(self, _prompt):
                raise OSError("prompt write failed")

        try:
            with llm_env(CAVEMAN_COMPRESS_PROVIDER=OPENCODE_PROVIDER), \
                 mock.patch.object(
                     compress_mod.tempfile,
                     "NamedTemporaryFile",
                     side_effect=FailingPromptFile,
                 ):
                with self.assertRaisesRegex(OSError, "prompt write failed"):
                    compress_mod.call_claude(PROMPT_TEXT)

            self.assertEqual(len(prompt_paths), 1)
            self.assertFalse(prompt_paths[0].exists())
        finally:
            for prompt_path in prompt_paths:
                prompt_path.unlink(missing_ok=True)

    def test_unknown_provider_is_rejected_before_subprocess(self):
        with llm_env(CAVEMAN_COMPRESS_PROVIDER="bogus"), \
             mock.patch.object(compress_mod.subprocess, "run") as run:
            with self.assertRaisesRegex(ValueError, "Unsupported caveman-compress provider"):
                compress_mod.call_claude(PROMPT_TEXT)

        run.assert_not_called()

    def test_missing_provider_cli_has_actionable_error(self):
        with llm_env(CAVEMAN_COMPRESS_PROVIDER=OPENCODE_PROVIDER), \
             mock.patch.object(compress_mod.shutil, "which", return_value=None), \
             mock.patch.object(
                 compress_mod.subprocess,
                 "run",
                 side_effect=FileNotFoundError,
             ):
            with self.assertRaisesRegex(RuntimeError, "opencode CLI not found on PATH"):
                compress_mod.call_claude(PROMPT_TEXT)

    def test_provider_cli_failure_includes_stderr(self):
        failure = compress_mod.subprocess.CalledProcessError(
            1,
            [OPENCODE_BIN, "run"],
            stderr="authentication failed",
        )
        with llm_env(CAVEMAN_COMPRESS_PROVIDER=OPENCODE_PROVIDER), \
             mock.patch.object(
                 compress_mod.subprocess,
                 "run",
                 side_effect=failure,
             ):
            with self.assertRaisesRegex(RuntimeError, "authentication failed"):
                compress_mod.call_claude(PROMPT_TEXT)

    def test_default_provider_falls_back_when_anthropic_sdk_is_missing(self):
        completed = mock.Mock(stdout=CLAUDE_OUTPUT)
        with llm_env(ANTHROPIC_API_KEY="test-key"), \
             mock.patch.dict(sys.modules, {"anthropic": None}), \
             mock.patch.object(compress_mod.subprocess, "run", return_value=completed):
            self.assertEqual(compress_mod.call_claude(PROMPT_TEXT), CLAUDE_OUTPUT)

    def test_unknown_provider_code_marker_is_rejected(self):
        unknown_marker = f"{compress_mod.CODE_MARKER_PREFIX}unknown@@"
        with self.assertRaisesRegex(ValueError, "unknown Caveman code-preservation marker"):
            compress_mod.restore_code_blocks(unknown_marker, [])

    def test_oversized_file_is_rejected_before_provider_call(self):
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / "task.md"
            path.write_bytes(b"x" * (compress_mod.MAX_FILE_SIZE_BYTES + 1))
            with mock.patch.object(compress_mod, "call_claude") as call:
                with self.assertRaisesRegex(ValueError, compress_mod.MAX_FILE_SIZE_LABEL):
                    compress_mod.compress_file(path)

        call.assert_not_called()

    def test_empty_fix_response_names_configured_provider(self):
        invalid = mock.Mock(is_valid=False, errors=["heading mismatch"], warnings=[])
        with tempfile.TemporaryDirectory() as tmp:
            path = self._file_with(
                Path(tmp),
                "# Title\n\nA sufficiently long body that needs a structural repair.\n",
            )
            with llm_env(CAVEMAN_COMPRESS_PROVIDER=OPENCODE_PROVIDER), \
                 mock.patch.object(
                     compress_mod,
                     "call_claude",
                     side_effect=["# Title\n\nShort.\n", ""],
                 ), \
                 mock.patch.object(compress_mod, "validate", return_value=invalid), \
                 mock.patch("builtins.print") as print_message:
                ok = compress_mod.compress_file(path)

        self.assertFalse(ok)
        print_message.assert_any_call("Fixing with opencode...")
        print_message.assert_any_call(
            "❌ Fix attempt aborted: opencode returned an empty response."
        )


class NocompressRegionTests(unittest.TestCase):
    """<!-- nocompress --> ... <!-- /nocompress --> keeps a region verbatim (#163)."""

    REGION = (
        "<!-- nocompress -->\n"
        "<example>\n"
        "This very long prose line must stay exactly as written.\n"
        '{"key": [1, 2, 3]}\n'
        "</example>\n"
        "<!-- /nocompress -->\n"
    )
    ORIGINAL = (
        "# Title\n\nThis very long prose line should be compressed down.\n\n"
        + REGION
        + "\nAnother very long prose line to compress here.\n"
    )

    def _run(self, text, call_claude, validate=None):
        tmp = tempfile.TemporaryDirectory()
        data_home = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        self.addCleanup(data_home.cleanup)
        path = Path(tmp.name) / "task.md"
        path.write_text(text, encoding="utf-8", newline="")
        patches = [
            mock.patch.dict(os.environ, {"XDG_DATA_HOME": data_home.name, "LOCALAPPDATA": data_home.name}),
            mock.patch.object(compress_mod, "call_claude", side_effect=call_claude),
        ]
        if validate is not None:
            patches.append(mock.patch.object(compress_mod, "validate", side_effect=validate))
        with contextlib.ExitStack() as stack:
            for p in patches:
                stack.enter_context(p)
            ok = compress_mod.compress_file(path)
            backup = compress_mod.backup_dir_for(path.resolve()) / "task.original.md"
            return ok, path, backup.exists()

    @staticmethod
    def _fake_model(prompt):
        # "Compresses" every prose line it can see, so an unmasked region
        # would come back rewritten. Markers pass through untouched.
        text = prompt.split("TEXT:\n", 1)[1].rstrip("\n")
        return "\n".join(
            "Short." if "very long prose" in line else line for line in text.splitlines()
        ) + "\n"

    def test_region_is_hidden_from_the_model_and_restored_byte_identical(self):
        prompts = []

        def fake_model(prompt):
            prompts.append(prompt)
            return self._fake_model(prompt)

        ok, path, _ = self._run(self.ORIGINAL, fake_model)
        self.assertTrue(ok)
        self.assertNotIn("must stay exactly", prompts[0])
        self.assertEqual(
            path.read_text(encoding="utf-8"),
            "# Title\n\nShort.\n\n" + self.REGION + "\nShort.\n",
        )

    def test_unclosed_region_aborts_before_model_call_and_backup(self):
        text = "# Title\n\n<!-- nocompress -->\nKeep me.\n\nSome long prose body to compress.\n"
        call = mock.Mock()
        with self.assertRaisesRegex(ValueError, "unclosed <!-- nocompress --> region"):
            self._run(text, call)
        call.assert_not_called()

    def test_fix_attempt_that_rewrites_the_region_is_skipped(self):
        # validate() never looks at prose, and the fix prompt sends the region
        # unmasked, so a repair that compresses it must be rejected here.
        first = "# Title\n\nShort.\n\n" + self.REGION + "\nShort.\n"
        bad_fix = first.replace("must stay exactly as written", "stay")
        invalid = mock.Mock(is_valid=False, errors=["heading mismatch"], warnings=[])
        valid = mock.Mock(is_valid=True, errors=[], warnings=[])
        ok, path, _ = self._run(
            self.ORIGINAL,
            lambda prompt: self._fake_model(prompt) if "TEXT:" in prompt else bad_fix,
            validate=[invalid, valid],
        )
        self.assertTrue(ok)
        self.assertIn(self.REGION, path.read_text(encoding="utf-8"))


class OpenAICompatProviderTests(unittest.TestCase):
    """CAVEMAN_COMPRESS_PROVIDER=openai-compat: Ollama, llama.cpp, vLLM, LM Studio (#201)."""

    def _response(self, content="compressed", finish_reason="stop"):
        body = json.dumps(
            {"choices": [{"message": {"content": content}, "finish_reason": finish_reason}]}
        ).encode()
        response = mock.MagicMock()
        response.__enter__.return_value.read.return_value = body
        return response

    def test_posts_chat_completion_to_configured_endpoint(self):
        with llm_env(
            CAVEMAN_COMPRESS_PROVIDER="openai-compat",
            CAVEMAN_COMPRESS_ENDPOINT="http://localhost:1234/v1/",
            CAVEMAN_COMPRESS_MODEL="qwen3:8b",
            CAVEMAN_COMPRESS_API_KEY="sk-local",
        ), mock.patch("urllib.request.urlopen", return_value=self._response()) as urlopen:
            self.assertEqual(compress_mod.call_claude(PROMPT_TEXT), "compressed")

        request = urlopen.call_args.args[0]
        self.assertEqual(request.full_url, "http://localhost:1234/v1/chat/completions")
        self.assertEqual(request.get_header("Authorization"), "Bearer sk-local")
        self.assertEqual(
            json.loads(request.data),
            {
                "model": "qwen3:8b",
                "messages": [{"role": "user", "content": PROMPT_TEXT}],
                "stream": False,
            },
        )
        self.assertEqual(
            urlopen.call_args.kwargs["timeout"], compress_mod.CLAUDE_CALL_TIMEOUT_SECONDS
        )

    def test_api_key_is_not_forwarded_on_redirect(self):
        import urllib.request

        with llm_env(
            CAVEMAN_COMPRESS_PROVIDER="openai-compat",
            CAVEMAN_COMPRESS_MODEL="m",
            CAVEMAN_COMPRESS_API_KEY="sk-local",
        ), mock.patch("urllib.request.urlopen", return_value=self._response()) as urlopen:
            compress_mod.call_claude(PROMPT_TEXT)

        request = urlopen.call_args.args[0]
        self.assertEqual(request.get_header("Authorization"), "Bearer sk-local")
        redirected = urllib.request.HTTPRedirectHandler().redirect_request(
            request, io.BytesIO(), 302, "Found", {}, "http://elsewhere.example/v1/chat/completions"
        )
        self.assertIsNone(redirected.get_header("Authorization"))

    def test_malformed_response_body_is_a_runtime_error(self):
        for body in (b"<html>502 Bad Gateway</html>", b'{"error": "model loading"}', b"[]"):
            response = mock.MagicMock()
            response.__enter__.return_value.read.return_value = body
            with self.subTest(body=body), \
                 llm_env(CAVEMAN_COMPRESS_PROVIDER="openai-compat", CAVEMAN_COMPRESS_MODEL="m"), \
                 mock.patch("urllib.request.urlopen", return_value=response):
                with self.assertRaisesRegex(RuntimeError, "unexpected response") as raised:
                    compress_mod.call_claude(PROMPT_TEXT)
                self.assertIn(body.decode(), str(raised.exception))

    def test_defaults_to_local_ollama_without_auth_header(self):
        with llm_env(CAVEMAN_COMPRESS_PROVIDER="openai-compat", CAVEMAN_COMPRESS_MODEL="m"), \
             mock.patch("urllib.request.urlopen", return_value=self._response()) as urlopen:
            compress_mod.call_claude(PROMPT_TEXT)

        request = urlopen.call_args.args[0]
        self.assertEqual(request.full_url, "http://localhost:11434/v1/chat/completions")
        self.assertIsNone(request.get_header("Authorization"))

    def test_missing_model_raises_before_any_network_call(self):
        with llm_env(CAVEMAN_COMPRESS_PROVIDER="openai-compat"), \
             mock.patch("urllib.request.urlopen") as urlopen:
            with self.assertRaisesRegex(RuntimeError, "CAVEMAN_COMPRESS_MODEL is required"):
                compress_mod.call_claude(PROMPT_TEXT)
        urlopen.assert_not_called()

    def test_output_at_the_length_cap_raises(self):
        with llm_env(CAVEMAN_COMPRESS_PROVIDER="openai-compat", CAVEMAN_COMPRESS_MODEL="m"), \
             mock.patch(
                 "urllib.request.urlopen",
                 return_value=self._response("first half", finish_reason="length"),
             ):
            with self.assertRaisesRegex(RuntimeError, "cap"):
                compress_mod.call_claude(PROMPT_TEXT)

    def test_http_error_body_surfaces(self):
        import urllib.error

        error = urllib.error.HTTPError(
            "http://localhost:11434/v1/chat/completions", 404, "Not Found", {},
            io.BytesIO(b'{"error": "model \'m\' not found"}'),
        )
        with llm_env(CAVEMAN_COMPRESS_PROVIDER="openai-compat", CAVEMAN_COMPRESS_MODEL="m"), \
             mock.patch("urllib.request.urlopen", side_effect=error):
            with self.assertRaisesRegex(RuntimeError, "404.*model 'm' not found"):
                compress_mod.call_claude(PROMPT_TEXT)

    def test_unreachable_server_is_a_runtime_error(self):
        import urllib.error

        with llm_env(CAVEMAN_COMPRESS_PROVIDER="openai-compat", CAVEMAN_COMPRESS_MODEL="m"), \
             mock.patch(
                 "urllib.request.urlopen",
                 side_effect=urllib.error.URLError("Connection refused"),
             ):
            with self.assertRaisesRegex(RuntimeError, "Connection refused"):
                compress_mod.call_claude(PROMPT_TEXT)


class TestOuterWrapperStripping(unittest.TestCase):
    """strip_llm_wrapper removes an outer ```markdown fence the model added
    around the WHOLE output. It must not fire on a document that merely starts
    and ends with a fence.

    The old regex (\\A\\s*(fence)[^\\n]*\\n(.*)\\n\\1\\s*\\Z with DOTALL and a greedy
    .*) never checked the two fences were the same block, so an ordinary README
    section came back with its first and last fence markers deleted and its two
    code blocks merged into prose. Validation then failed on both the compress
    and the fix path, and the section was permanently uncompressible after three
    paid API calls.
    """

    def test_two_separate_blocks_are_left_alone(self):
        text = "```bash\nnpm install\n```\n\nSome prose.\n\n```bash\nnpm test\n```"
        self.assertEqual(compress_mod.strip_llm_wrapper(text), text)

    def test_a_real_wrapper_is_stripped(self):
        text = "```markdown\n# Title\n\nbody text\n```"
        self.assertEqual(compress_mod.strip_llm_wrapper(text), "# Title\n\nbody text")

    def test_a_longer_wrapper_around_inner_fences_is_stripped(self):
        text = "````markdown\n# Title\n\n```bash\nls\n```\n````"
        self.assertEqual(compress_mod.strip_llm_wrapper(text), "# Title\n\n```bash\nls\n```")


class FirstTextBlockTests(unittest.TestCase):
    """The paid-call crash guard: ``content[0]`` must never be assumed text.

    A tool_use/thinking block can order before the text block on tool-heavy
    sessions; the old ``msg.content[0].text`` crashed AFTER the paid call with
    AttributeError. The fix takes the first actual text block (Anthropic SDK
    shape only — the only provider that reaches this call site).
    """

    def _run(self, blocks):
        import types

        fake_msg = types.SimpleNamespace(content=blocks, stop_reason="end_turn")
        fake_client = mock.Mock()
        stream_ctx = mock.MagicMock()
        stream_ctx.__enter__.return_value.get_final_message.return_value = fake_msg
        fake_client.messages.stream.return_value = stream_ctx
        anthropic_stub = types.SimpleNamespace(Anthropic=mock.Mock(return_value=fake_client))
        with mock.patch.dict(os.environ, {"ANTHROPIC_API_KEY": "test-key"}), \
             mock.patch.dict(sys.modules, {"anthropic": anthropic_stub}):
            return compress_mod.call_claude("prompt")

    def test_tool_use_first_block_does_not_crash_and_returns_text(self):
        # Regression: tool_use ordered before text used to crash after the
        # paid call; now the first text block is selected.
        self.assertEqual(
            self._run([mock.Mock(type="tool_use", id="toolu_1"), mock.Mock(type="text", text="  compressed  ")]),
            "compressed",
        )

    def test_thinking_first_block_skipped(self):
        self.assertEqual(
            self._run([mock.Mock(type="thinking", text="..."), mock.Mock(type="text", text="body")]),
            "body",
        )

    def test_takes_first_text_when_multiple_text_blocks(self):
        self.assertEqual(
            self._run([mock.Mock(type="text", text="first"), mock.Mock(type="text", text="second")]),
            "first",
        )

    def test_no_text_block_returns_empty_like_cli_arm(self):
        # tool_use-only (or empty) replies return "", matching the CLI arm's
        # contract, so the caller's "Claude returned an empty response"
        # message applies instead of an unhandled AttributeError traceback.
        self.assertEqual(self._run([mock.Mock(type="tool_use", id="toolu_1")]), "")


if __name__ == "__main__":
    unittest.main()
