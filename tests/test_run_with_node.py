import os
import subprocess
import tempfile
import unittest
from pathlib import Path


REPO_ROOT = Path(__file__).resolve().parent.parent
WRAPPER = REPO_ROOT / "src" / "hooks" / "run-with-node.sh"


# Regression for hooks printing "/bin/sh: 1: node: not found" on every prompt:
# plugin hooks run with a minimal PATH (no nvm/profile sourcing), so a bare
# `node "$script"` fails even when node is installed and usable interactively.
class RunWithNodeTests(unittest.TestCase):
    def run_wrapper(self, script, path, home=None, extra_env=None):
        env = {"PATH": path}
        if home is not None:
            env["HOME"] = str(home)
        if extra_env:
            env.update(extra_env)
        return subprocess.run(
            ["sh", str(WRAPPER), str(script)],
            env=env,
            text=True,
            capture_output=True,
        )

    def test_resolves_node_from_path(self):
        with tempfile.TemporaryDirectory(prefix="run-with-node-path-") as tmp:
            script = Path(tmp) / "probe.js"
            script.write_text("console.log('ok-from-path')\n")
            real_node = subprocess.run(
                ["sh", "-c", "command -v node"], text=True, capture_output=True
            ).stdout.strip()
            if not real_node:
                self.skipTest("no node on PATH in this test environment")
            result = self.run_wrapper(script, os.path.dirname(real_node) + ":/usr/bin:/bin")
            self.assertEqual(result.returncode, 0)
            self.assertIn("ok-from-path", result.stdout)

    def test_falls_back_to_nvm_absolute_path_when_path_is_broken(self):
        # Simulates the reported bug: hook PATH has no node, but node is
        # installed via nvm for the interactive shell.
        with tempfile.TemporaryDirectory(prefix="run-with-node-nvm-") as tmp:
            home = Path(tmp)
            fake_version_dir = home / ".nvm" / "versions" / "node" / "v99.0.0" / "bin"
            fake_version_dir.mkdir(parents=True)
            fake_node = fake_version_dir / "node"
            fake_node.write_text(
                "#!/bin/sh\necho \"fake-node saw: $*\"\n"
            )
            fake_node.chmod(0o755)

            script = home / "probe.js"
            script.write_text("unused\n")

            result = self.run_wrapper(script, "/usr/bin:/bin", home=home)
            self.assertEqual(result.returncode, 0)
            self.assertIn("fake-node saw:", result.stdout)
            self.assertIn(str(script), result.stdout)

    def test_survives_unset_home_without_crashing(self):
        # Regression: under `set -eu`, referencing "$HOME" while HOME is
        # entirely absent from the environment (not just empty — genuinely
        # unset, as hook runners occasionally leave it) aborts the `for`
        # candidate loop with "HOME: parameter not set" before any of the
        # other absolute-path fallbacks are even tried. `run_wrapper` with no
        # `home=` already omits HOME from the child env, so this only needs
        # a PATH with no node on it to force the fallback loop to run.
        with tempfile.TemporaryDirectory(prefix="run-with-node-nohome-") as tmp:
            script = Path(tmp) / "probe.js"
            script.write_text("unused\n")

            result = self.run_wrapper(script, "/usr/bin:/bin")
            self.assertEqual(result.returncode, 0)
            self.assertNotIn("parameter not set", result.stderr)

    def test_silently_skips_when_node_is_nowhere_to_be_found(self):
        with tempfile.TemporaryDirectory(prefix="run-with-node-none-") as tmp:
            home = Path(tmp)
            script = home / "probe.js"
            script.write_text("unused\n")

            result = self.run_wrapper(script, "/usr/bin:/bin", home=home)
            self.assertEqual(result.returncode, 0)
            self.assertEqual(result.stdout, "")
            self.assertEqual(result.stderr, "")


if __name__ == "__main__":
    unittest.main()
