"""Tests that call_claude() fails fast instead of hanging (PR #798 review).

subprocess.run([claude_bin, "--print"], ...) used to have no timeout, so a
stalled `claude` CLI (dropped network, an auth prompt with no TTY to answer
it) blocked the caller forever. These tests pin the fix: the CLI subprocess
call and the SDK call both carry a bounded timeout, and a timeout on either
path surfaces as a RuntimeError the existing retry loop already handles.
"""

import subprocess
import sys
import unittest
from pathlib import Path
from unittest import mock

REPO_ROOT = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(REPO_ROOT / "skills" / "caveman-compress"))

from scripts import compress as compress_mod  # noqa: E402


class CallClaudeTimeoutTests(unittest.TestCase):
    def setUp(self):
        # Force the CLI subprocess path (not the Anthropic SDK path).
        env_patch = mock.patch.dict("os.environ", {}, clear=False)
        env_patch.start()
        self.addCleanup(env_patch.stop)
        compress_mod.os.environ.pop("ANTHROPIC_API_KEY", None)

    def test_cli_subprocess_call_carries_a_timeout(self):
        with mock.patch.object(compress_mod.subprocess, "run") as run:
            run.return_value = mock.Mock(stdout="compressed", returncode=0)
            compress_mod.call_claude("prompt")
            args, kwargs = run.call_args
            self.assertEqual(kwargs.get("timeout"), compress_mod.CLAUDE_CALL_TIMEOUT_SECONDS)
            self.assertEqual(
                args[0][1:],
                ["--print", "--setting-sources", "", "--strict-mcp-config"],
            )

    def test_cli_timeout_raises_runtime_error_not_hang(self):
        with mock.patch.object(
            compress_mod.subprocess,
            "run",
            side_effect=subprocess.TimeoutExpired(cmd="claude", timeout=compress_mod.CLAUDE_CALL_TIMEOUT_SECONDS),
        ):
            with self.assertRaises(RuntimeError):
                compress_mod.call_claude("prompt")


if __name__ == "__main__":
    unittest.main()
