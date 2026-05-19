"""Tests for caveman's project-scope gating.

caveman's SessionStart hook fires globally on every Claude Code session.
Project-scope gating lets users opt specific directories out of caveman
without flipping the global mode. Resolution order (first match wins):

  1. CWD/.caveman-disable file → disabled
  2. CWD/.caveman-enable  file → enabled
  3. CAVEMAN_PROJECT_SCOPE env var (disabled | enabled)
  4. config.projectScope.deny[] contains a path prefix of CWD → disabled
  5. config.projectScope.allow[] set but no entry matches CWD → disabled
  6. config.projectScope.allow[] contains a path prefix of CWD → enabled
  7. Otherwise → inherit (use global default)

When 'disabled', the activate hook exits early with 'OK (project-scope disabled)'
and does NOT write the .caveman-active flag, mirroring the behavior of global
'off' mode but scoped to one CWD.
"""

import json
import os
import subprocess
import tempfile
import unittest
from pathlib import Path


REPO_ROOT = Path(__file__).resolve().parent.parent
ACTIVATE_HOOK = REPO_ROOT / "src" / "hooks" / "caveman-activate.js"


def run_activate(*, cwd, home, env_extra=None):
    env = os.environ.copy()
    env["HOME"] = str(home)
    env["USERPROFILE"] = str(home)
    env["CLAUDE_CONFIG_DIR"] = str(home / ".claude")
    # Force default mode so we always know what we'd activate with absent gating.
    env.setdefault("CAVEMAN_DEFAULT_MODE", "full")
    # Don't let the test inherit the host's project-scope env.
    env.pop("CAVEMAN_PROJECT_SCOPE", None)
    if env_extra:
        env.update(env_extra)

    (home / ".claude").mkdir(parents=True, exist_ok=True)

    return subprocess.run(
        ["node", str(ACTIVATE_HOOK)],
        cwd=cwd,
        env=env,
        text=True,
        capture_output=True,
        check=False,
    )


class ProjectScopeTests(unittest.TestCase):
    def test_no_markers_no_config_activates_normally(self):
        with tempfile.TemporaryDirectory() as tmp:
            home = Path(tmp) / "home"
            cwd = Path(tmp) / "cwd"
            cwd.mkdir()
            result = run_activate(cwd=cwd, home=home)

            self.assertEqual(result.returncode, 0)
            self.assertIn("CAVEMAN MODE ACTIVE", result.stdout)
            # Flag file should have been written by safeWriteFlag.
            self.assertTrue((home / ".claude" / ".caveman-active").exists())

    def test_disable_marker_skips_activation(self):
        with tempfile.TemporaryDirectory() as tmp:
            home = Path(tmp) / "home"
            cwd = Path(tmp) / "cwd"
            cwd.mkdir()
            (cwd / ".caveman-disable").write_text("")
            result = run_activate(cwd=cwd, home=home)

            self.assertEqual(result.returncode, 0)
            self.assertEqual(result.stdout, "OK (project-scope disabled)")
            # No flag should be written when disabled.
            self.assertFalse((home / ".claude" / ".caveman-active").exists())

    def test_enable_marker_activates_even_with_default_full(self):
        with tempfile.TemporaryDirectory() as tmp:
            home = Path(tmp) / "home"
            cwd = Path(tmp) / "cwd"
            cwd.mkdir()
            (cwd / ".caveman-enable").write_text("")
            result = run_activate(cwd=cwd, home=home)

            self.assertEqual(result.returncode, 0)
            self.assertIn("CAVEMAN MODE ACTIVE", result.stdout)

    def test_disable_marker_beats_enable_marker(self):
        with tempfile.TemporaryDirectory() as tmp:
            home = Path(tmp) / "home"
            cwd = Path(tmp) / "cwd"
            cwd.mkdir()
            (cwd / ".caveman-disable").write_text("")
            (cwd / ".caveman-enable").write_text("")
            result = run_activate(cwd=cwd, home=home)

            self.assertEqual(result.stdout, "OK (project-scope disabled)")

    def test_env_var_disabled(self):
        with tempfile.TemporaryDirectory() as tmp:
            home = Path(tmp) / "home"
            cwd = Path(tmp) / "cwd"
            cwd.mkdir()
            result = run_activate(
                cwd=cwd,
                home=home,
                env_extra={"CAVEMAN_PROJECT_SCOPE": "disabled"},
            )

            self.assertEqual(result.stdout, "OK (project-scope disabled)")

    def test_config_denylist_prefix_match(self):
        with tempfile.TemporaryDirectory() as tmp:
            home = Path(tmp) / "home"
            cwd = Path(tmp) / "deny-me" / "subdir"
            cwd.mkdir(parents=True)

            config_dir = home / ".config" / "caveman"
            config_dir.mkdir(parents=True)
            (config_dir / "config.json").write_text(json.dumps({
                "projectScope": {
                    "deny": [str(Path(tmp) / "deny-me")]
                }
            }))

            env_extra = {"XDG_CONFIG_HOME": str(home / ".config")}
            result = run_activate(cwd=cwd, home=home, env_extra=env_extra)

            self.assertEqual(result.stdout, "OK (project-scope disabled)")

    def test_config_allowlist_only_enables_listed_dirs(self):
        with tempfile.TemporaryDirectory() as tmp:
            home = Path(tmp) / "home"
            allowed = Path(tmp) / "allowed"
            other = Path(tmp) / "other"
            allowed.mkdir()
            other.mkdir()

            config_dir = home / ".config" / "caveman"
            config_dir.mkdir(parents=True)
            (config_dir / "config.json").write_text(json.dumps({
                "projectScope": {
                    "allow": [str(allowed)]
                }
            }))

            env_extra = {"XDG_CONFIG_HOME": str(home / ".config")}

            # In the allowlisted dir → activates
            r_allowed = run_activate(cwd=allowed, home=home, env_extra=env_extra)
            self.assertIn("CAVEMAN MODE ACTIVE", r_allowed.stdout)

            # Elsewhere → disabled
            r_other = run_activate(cwd=other, home=home, env_extra=env_extra)
            self.assertEqual(r_other.stdout, "OK (project-scope disabled)")

    def test_global_off_plus_enable_marker_activates(self):
        # The canonical "allowlist mode": user flips global default to off so
        # caveman is silent everywhere, then drops .caveman-enable markers in
        # projects where they want it. Without this, the .caveman-enable
        # marker is useless — the global 'off' branch fires first.
        with tempfile.TemporaryDirectory() as tmp:
            home = Path(tmp) / "home"
            cwd = Path(tmp) / "cwd"
            cwd.mkdir()
            (cwd / ".caveman-enable").write_text("")

            # Flip global default to off via config file.
            config_dir = home / ".config" / "caveman"
            config_dir.mkdir(parents=True)
            (config_dir / "config.json").write_text(json.dumps({"defaultMode": "off"}))

            env_extra = {
                "XDG_CONFIG_HOME": str(home / ".config"),
                # Drop the test's default CAVEMAN_DEFAULT_MODE so config.json wins.
                "CAVEMAN_DEFAULT_MODE": "",
            }

            result = run_activate(cwd=cwd, home=home, env_extra=env_extra)
            self.assertIn("CAVEMAN MODE ACTIVE", result.stdout)

    def test_global_off_without_marker_stays_silent(self):
        # Counterpart to the above: same global off config, but no marker →
        # silent. This is the failure mode the reorder fixes.
        with tempfile.TemporaryDirectory() as tmp:
            home = Path(tmp) / "home"
            cwd = Path(tmp) / "cwd"
            cwd.mkdir()

            config_dir = home / ".config" / "caveman"
            config_dir.mkdir(parents=True)
            (config_dir / "config.json").write_text(json.dumps({"defaultMode": "off"}))

            env_extra = {
                "XDG_CONFIG_HOME": str(home / ".config"),
                "CAVEMAN_DEFAULT_MODE": "",
            }

            result = run_activate(cwd=cwd, home=home, env_extra=env_extra)
            self.assertEqual(result.stdout, "OK")


if __name__ == "__main__":
    unittest.main()
