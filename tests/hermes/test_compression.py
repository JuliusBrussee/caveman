"""Provider-agnostic, revision-bound Hermes compression safety contracts."""
from __future__ import annotations

import json
import os
from pathlib import Path
import socket
import stat
import subprocess
import tempfile
import unittest
from unittest import mock

ORIGINAL = """---
title: Setup Guide
version: 1.2.3
---
# Install

You should carefully run `npm install` twice only when needed. Keep `$HOME`, `NODE_ENV`, /src/app.py, https://example.com/docs, 2026-07-15, and 42 unchanged.

1. First long instruction
   - Nested long instruction

| Name | Purpose |
| --- | ---: |
| API | A very long description |

```js
const value = 42;
```
"""
PROPOSED = """---
title: Setup Guide
version: 1.2.3
---
# Install

Run `npm install` twice when needed. Keep `$HOME`, `NODE_ENV`, /src/app.py, https://example.com/docs, 2026-07-15, and 42 unchanged.

1. First instruction
   - Nested instruction

| Name | Purpose |
| --- | ---: |
| API | Long description |

```js
const value = 42;
```
"""


class CompressionTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.root = Path(self.temp.name)
        self.home = self.root / "home"
        self.hh = self.root / "hermes"
        self.docs = self.root / "docs with ünicode"
        self.home.mkdir(); self.docs.mkdir()
        self.patch = mock.patch.dict(
            os.environ,
            {"HOME": str(self.home), "HERMES_HOME": str(self.hh), "XDG_CONFIG_HOME": str(self.root / "xdg")},
            clear=False,
        )
        self.patch.start()
        from hermes_caveman import compression
        self.c = compression

    def tearDown(self):
        self.patch.stop()
        self.temp.cleanup()

    def write(self, name="guide.md", content=ORIGINAL, parent=None):
        path = (parent or self.docs) / name
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(content, encoding="utf-8")
        return path

    def prepare(self, path):
        return self.c.prepare_compression(str(path))

    def test_sensitive_paths_and_basenames_refuse_before_read_or_content_return(self):
        cases = [
            self.write(".env"),
            self.write("id_rsa"),
            self.write("credentials.md"),
            self.write("notes.md", parent=self.root / ".ssh"),
            self.write("notes.md", parent=self.root / ".hermes"),
            self.write("notes.md", parent=self.root / "work_found"),
        ]
        for path in cases:
            with self.subTest(path=path), mock.patch.object(self.c, "_read_bytes", side_effect=AssertionError("sensitive bytes read")) as reader:
                result = self.prepare(path)
                self.assertFalse(result["ok"], result)
                self.assertNotIn("Setup Guide", json.dumps(result))
                reader.assert_not_called()

    def test_supported_and_unsupported_extensions(self):
        for suffix in (".md", ".markdown", ".txt", ".rst", ".typ", ".typst", ".tex"):
            with self.subTest(suffix=suffix):
                result = self.prepare(self.write("guide" + suffix))
                self.assertTrue(result["ok"], result)
        for suffix in (".py", ".js", ".json", ".yaml", ".env", ".sh"):
            with self.subTest(suffix=suffix):
                result = self.prepare(self.write("guide" + suffix))
                self.assertFalse(result["ok"], result)

    def test_empty_oversize_symlink_and_non_file_refused(self):
        self.assertFalse(self.prepare(self.write(content=""))["ok"])
        huge = self.write("huge.md", "x" * (self.c.MAX_SOURCE_BYTES + 1))
        self.assertFalse(self.prepare(huge)["ok"])
        target = self.write("target.md")
        link = self.docs / "link.md"; link.symlink_to(target)
        self.assertFalse(self.prepare(link)["ok"])
        self.assertFalse(self.prepare(self.docs)["ok"])

    def test_symlinked_ancestor_is_refused_before_sensitive_target_read(self):
        sensitive = self.root / ".ssh"
        target = self.write("notes.md", parent=sensitive)
        alias = self.root / "safe-looking"
        alias.symlink_to(sensitive, target_is_directory=True)
        with mock.patch.object(self.c, "_read_bytes", side_effect=AssertionError("sensitive bytes read")) as reader:
            result = self.prepare(alias / target.name)
        self.assertFalse(result["ok"], result)
        reader.assert_not_called()
        self.assertEqual(target.read_text(encoding="utf-8"), ORIGINAL)

    def test_successful_preflight_returns_prose_and_opaque_revision_only_after_checks(self):
        path = self.write()
        result = self.prepare(path)
        self.assertTrue(result["ok"], result)
        self.assertEqual(result["content"], ORIGINAL)
        self.assertEqual(result["path"], str(path.resolve()))
        self.assertRegex(result["revision"], r"^[A-Za-z0-9_-]{24,128}$")
        self.assertNotIn(str(path), result["revision"])
        self.assertNotIn(self.c.sha256_bytes(ORIGINAL.encode()), result["revision"])

    def test_validator_accepts_valid_compression(self):
        result = self.c.validate_content(ORIGINAL, PROPOSED)
        self.assertTrue(result.is_valid, result.errors)

    def test_every_protected_content_mismatch_is_fatal(self):
        cases = {
            "frontmatter": PROPOSED.replace("title: Setup Guide", "title: Other"),
            "heading": PROPOSED.replace("# Install", "# Setup"),
            "fenced code": PROPOSED.replace("const value = 42;", "const value = 41;"),
            "inline code lost": PROPOSED.replace("`npm install`", "npm install"),
            "inline code added": PROPOSED.replace("Run `npm install`", "Run `npm install` and `npm install`"),
            "url": PROPOSED.replace("https://example.com/docs", "https://example.com/help"),
            "path": PROPOSED.replace("/src/app.py", "/src/main.py"),
            "command": PROPOSED.replace("`npm install`", "`npm i`"),
            "environment": PROPOSED.replace("$HOME", "$USER"),
            "environment bare": PROPOSED.replace("NODE_ENV", "APP_ENV"),
            "date": PROPOSED.replace("2026-07-15", "2026-07-16"),
            "version": PROPOSED.replace("1.2.3", "1.2.4"),
            "number": PROPOSED.replace("and 42 unchanged", "and 43 unchanged"),
            "list nesting": PROPOSED.replace("   - Nested", "- Nested"),
            "list numbering": PROPOSED.replace("1. First", "2. First"),
            "table rows": PROPOSED.replace("| API | Long description |\n", ""),
            "table columns": PROPOSED.replace("| API | Long description |", "| API | Long | extra |"),
        }
        for label, proposed in cases.items():
            with self.subTest(label=label):
                result = self.c.validate_content(ORIGINAL, proposed)
                self.assertFalse(result.is_valid, f"{label} unexpectedly valid")
                self.assertTrue(result.errors, label)

    def test_long_fence_protects_content_after_nested_shorter_fence(self):
        original = "````text\nkeep\n```\nprotected sentence\n````\n"
        proposed = original.replace("protected sentence", "changed sentence")

        result = self.c.validate_content(original, proposed)

        self.assertFalse(result.is_valid, result.errors)
        self.assertIn("protected fenced code changed", result.errors)

    def test_repo_relative_paths_are_protected(self):
        original = "Run src/tools/caveman-init.js before docs/index.html.\n"
        proposed = original.replace("src/tools/caveman-init.js", "src/tools/other.js")

        result = self.c.validate_content(original, proposed)

        self.assertFalse(result.is_valid, result.errors)
        self.assertIn("protected paths changed", result.errors)

    def test_apply_validates_before_any_backup_or_source_write(self):
        path = self.write()
        prepared = self.prepare(path)
        bad = PROPOSED.replace("# Install", "# Changed")
        result = self.c.apply_compression(str(path), prepared["revision"], bad)
        self.assertFalse(result["ok"], result)
        self.assertEqual(path.read_text(encoding="utf-8"), ORIGINAL)
        self.assertFalse(self.c.backup_path_for(path).exists())

    def test_apply_writes_verified_out_of_tree_backup_then_atomic_target(self):
        path = self.write("güide.md")
        prepared = self.prepare(path)
        result = self.c.apply_compression(str(path), prepared["revision"], PROPOSED)
        self.assertTrue(result["ok"], result)
        backup = Path(result["backup"])
        self.assertEqual(path.read_text(encoding="utf-8"), PROPOSED)
        self.assertEqual(backup.read_text(encoding="utf-8"), ORIGINAL)
        self.assertFalse(str(backup).startswith(str(path.parent) + os.sep))
        self.assertTrue(str(backup).startswith(str(self.hh / "caveman")))
        self.assertEqual(stat.S_IMODE(backup.stat().st_mode), 0o600)
        self.assertFalse(any(p.name.startswith(".caveman-compress-") for p in path.parent.iterdir()))

    def test_existing_backup_refuses_before_source_overwrite(self):
        path = self.write()
        backup = self.c.backup_path_for(path)
        backup.parent.mkdir(parents=True, exist_ok=True)
        backup.write_text("owner backup", encoding="utf-8")
        prepared = self.prepare(path)
        self.assertFalse(prepared["ok"], prepared)
        self.assertEqual(backup.read_text(encoding="utf-8"), "owner backup")
        self.assertEqual(path.read_text(encoding="utf-8"), ORIGINAL)

    def test_backup_readback_failure_aborts_before_source_replace(self):
        path = self.write()
        prepared = self.prepare(path)
        real = self.c._read_exact
        def fail_backup(candidate):
            if Path(candidate) == self.c.backup_path_for(path):
                raise OSError("simulated backup readback failure")
            return real(candidate)
        with mock.patch.object(self.c, "_read_exact", side_effect=fail_backup):
            result = self.c.apply_compression(str(path), prepared["revision"], PROPOSED)
        self.assertFalse(result["ok"], result)
        self.assertEqual(path.read_text(encoding="utf-8"), ORIGINAL)

    def test_digest_or_metadata_change_before_apply_aborts_without_overwrite(self):
        path = self.write()
        prepared = self.prepare(path)
        path.write_text(ORIGINAL + "editor change\n", encoding="utf-8")
        result = self.c.apply_compression(str(path), prepared["revision"], PROPOSED)
        self.assertFalse(result["ok"], result)
        self.assertIn("editor change", path.read_text(encoding="utf-8"))
        self.assertFalse(self.c.backup_path_for(path).exists())

    def test_change_during_validation_is_detected(self):
        path = self.write()
        prepared = self.prepare(path)
        real_validate = self.c.validate_content
        def race(original, proposed):
            path.write_text("editor won\n", encoding="utf-8")
            return real_validate(original, proposed)
        with mock.patch.object(self.c, "validate_content", side_effect=race):
            result = self.c.apply_compression(str(path), prepared["revision"], PROPOSED)
        self.assertFalse(result["ok"], result)
        self.assertEqual(path.read_text(encoding="utf-8"), "editor won\n")
        self.assertFalse(self.c.backup_path_for(path).exists())

    def test_change_immediately_before_replace_is_detected(self):
        path = self.write()
        prepared = self.prepare(path)
        with mock.patch.object(self.c, "_before_replace", side_effect=lambda _: path.write_text("late editor\n", encoding="utf-8")):
            result = self.c.apply_compression(str(path), prepared["revision"], PROPOSED)
        self.assertFalse(result["ok"], result)
        self.assertEqual(path.read_text(encoding="utf-8"), "late editor\n")

    def test_recreated_path_after_final_digest_is_never_overwritten(self):
        path = self.write()
        prepared = self.prepare(path)
        with mock.patch.object(self.c, "_before_install", side_effect=lambda _: path.write_text("latest editor\n", encoding="utf-8")):
            result = self.c.apply_compression(str(path), prepared["revision"], PROPOSED)
        self.assertFalse(result["ok"], result)
        self.assertEqual(path.read_text(encoding="utf-8"), "latest editor\n")
        claims = list(self.docs.glob(".caveman-original-*"))
        self.assertEqual(len(claims), 1)
        self.assertEqual(claims[0].read_text(encoding="utf-8"), ORIGINAL)

    def test_parent_rename_to_symlink_before_replace_aborts_without_updating_displaced_entry(self):
        path = self.write()
        prepared = self.prepare(path)
        moved = self.root / "displaced-docs"

        def race(_):
            self.docs.rename(moved)
            self.docs.symlink_to(moved, target_is_directory=True)

        with mock.patch.object(self.c, "_before_replace", side_effect=race):
            result = self.c.apply_compression(str(path), prepared["revision"], PROPOSED)
        self.assertFalse(result["ok"], result)
        self.assertEqual(path.read_text(encoding="utf-8"), ORIGINAL)
        self.assertEqual((moved / path.name).read_text(encoding="utf-8"), ORIGINAL)

    def test_revision_is_path_bound_single_use_and_malformed_token_fails_safe(self):
        first = self.write("first.md")
        second = self.write("second.md")
        prepared = self.prepare(first)
        self.assertFalse(self.c.apply_compression(str(second), prepared["revision"], PROPOSED)["ok"])
        self.assertFalse(self.c.apply_compression(str(first), "../../bad-token", PROPOSED)["ok"])
        success = self.c.apply_compression(str(first), prepared["revision"], PROPOSED)
        self.assertTrue(success["ok"], success)
        self.assertFalse(self.c.apply_compression(str(first), prepared["revision"], PROPOSED)["ok"])

    def test_hermes_path_never_invokes_anthropic_claude_or_network(self):
        path = self.write()
        with mock.patch("subprocess.run", side_effect=AssertionError("subprocess")), mock.patch("subprocess.Popen", side_effect=AssertionError("subprocess")), mock.patch("socket.socket.connect", side_effect=AssertionError("network")):
            prepared = self.prepare(path)
            result = self.c.apply_compression(str(path), prepared["revision"], PROPOSED)
        self.assertTrue(result["ok"], result)

    def test_tool_wrappers_return_json_serializable_result_and_accept_host_kwargs(self):
        path = self.write()
        prepared = self.c.caveman_prepare_compression_tool({"path": str(path)}, session_id="s", future="ok")
        self.assertTrue(prepared["ok"], prepared)
        applied = self.c.caveman_apply_compression_tool({"path": str(path), "revision": prepared["revision"], "proposed_content": PROPOSED}, task_id="t", future="ok")
        self.assertTrue(applied["ok"], applied)
        json.dumps(prepared); json.dumps(applied)


if __name__ == "__main__":
    unittest.main()
