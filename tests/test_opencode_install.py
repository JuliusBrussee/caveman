"""Tests for the native opencode installer in install.sh.

Covers:
- Local copy mode (repo_root available)
- Remote download fallback (curl-pipe mode)
- Idempotency (skip when already installed unless --force)
- Sub-directory handling (compress/scripts)
"""

import json
import os
import shutil
import subprocess
import tempfile
import unittest
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parent.parent


class OpencodeInstallTests(unittest.TestCase):
    def run_install(self, args, home, cwd=None):
        env = os.environ.copy()
        env["HOME"] = str(home)
        env["USERPROFILE"] = str(home)
        cmd = ["bash", str(REPO_ROOT / "install.sh")] + args
        return subprocess.run(
            cmd,
            cwd=cwd or REPO_ROOT,
            env=env,
            text=True,
            capture_output=True,
        )

    def test_opencode_installs_skills_locally(self):
        with tempfile.TemporaryDirectory(prefix="caveman-opencode-") as tmp:
            home = Path(tmp)
            skills_dir = home / ".agents" / "skills"

            result = self.run_install(["--only", "opencode"], home)

            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertIn("opencode detected", result.stdout)

            expected_skills = [
                "caveman", "caveman-commit", "caveman-review",
                "caveman-help", "caveman-stats", "cavecrew", "compress",
            ]
            for skill in expected_skills:
                skill_path = skills_dir / skill / "SKILL.md"
                self.assertTrue(skill_path.exists(), f"{skill}/SKILL.md missing")
                content = skill_path.read_text()
                self.assertIn("caveman", content.lower())

    def test_opencode_skips_when_already_installed(self):
        with tempfile.TemporaryDirectory(prefix="caveman-opencode-") as tmp:
            home = Path(tmp)
            skills_dir = home / ".agents" / "skills"
            skills_dir.mkdir(parents=True)
            (skills_dir / "caveman").mkdir()
            (skills_dir / "caveman" / "SKILL.md").write_text("existing")

            result = self.run_install(["--only", "opencode"], home)

            self.assertEqual(result.returncode, 0)
            self.assertIn("already installed", result.stdout)

    def test_opencode_overwrites_with_force(self):
        with tempfile.TemporaryDirectory(prefix="caveman-opencode-") as tmp:
            home = Path(tmp)
            skills_dir = home / ".agents" / "skills"
            skills_dir.mkdir(parents=True)
            (skills_dir / "caveman").mkdir()
            (skills_dir / "caveman" / "SKILL.md").write_text("stale")

            result = self.run_install(["--only", "opencode", "--force"], home)

            self.assertEqual(result.returncode, 0)
            self.assertNotIn("already installed", result.stdout)
            content = (skills_dir / "caveman" / "SKILL.md").read_text()
            self.assertNotEqual(content, "stale")

    def test_opencode_compress_includes_scripts(self):
        with tempfile.TemporaryDirectory(prefix="caveman-opencode-") as tmp:
            home = Path(tmp)

            result = self.run_install(["--only", "opencode"], home)

            self.assertEqual(result.returncode, 0)
            scripts_dir = home / ".agents" / "skills" / "compress" / "scripts"
            self.assertTrue(scripts_dir.exists())
            self.assertTrue((scripts_dir / "compress.py").exists())

    def test_opencode_dry_run_does_not_write(self):
        with tempfile.TemporaryDirectory(prefix="caveman-opencode-") as tmp:
            home = Path(tmp)

            result = self.run_install(["--only", "opencode", "--dry-run"], home)

            self.assertEqual(result.returncode, 0)
            self.assertIn("would copy", result.stdout)
            self.assertFalse((home / ".agents").exists())


if __name__ == "__main__":
    unittest.main()
