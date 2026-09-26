"""Tests that the SDK path streams instead of capping output at 8192 tokens.

compress_file() accepts inputs up to MAX_FILE_SIZE (500KB, ~125k tokens). A
non-streaming create(max_tokens=8192) truncated the compressed body silently:
validate() then failed on the missing tail and the retry loop spent two more
paid calls before restoring the original. These tests pin the fix.
"""

import sys
import unittest
from pathlib import Path
from unittest import mock

REPO_ROOT = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(REPO_ROOT / "skills" / "caveman-compress"))

from scripts import compress as compress_mod  # noqa: E402


def _fake_client(text="compressed", stop_reason="end_turn"):
    """Anthropic client whose messages.stream() yields one text block."""
    message = mock.Mock(content=[mock.Mock(type="text", text=text)], stop_reason=stop_reason)
    stream_ctx = mock.MagicMock()
    stream_ctx.__enter__.return_value.get_final_message.return_value = message
    client = mock.Mock()
    client.messages.stream.return_value = stream_ctx
    return client


class CallClaudeStreamingTests(unittest.TestCase):
    def setUp(self):
        env_patch = mock.patch.dict("os.environ", {"ANTHROPIC_API_KEY": "sk-test"}, clear=False)
        env_patch.start()
        self.addCleanup(env_patch.stop)

    def _call(self, client):
        fake_sdk = mock.Mock(Anthropic=mock.Mock(return_value=client))
        with mock.patch.dict(sys.modules, {"anthropic": fake_sdk}):
            return compress_mod.call_claude("prompt")

    def test_sdk_path_streams_and_returns_the_final_message(self):
        client = _fake_client()
        self.assertEqual(self._call(client), "compressed")
        client.messages.stream.assert_called_once()
        client.messages.create.assert_not_called()

    def test_output_ceiling_fits_a_max_size_input(self):
        client = _fake_client()
        self._call(client)
        max_tokens = client.messages.stream.call_args.kwargs["max_tokens"]
        self.assertEqual(max_tokens, compress_mod.MAX_OUTPUT_TOKENS)
        # 500KB of prose is ~125k tokens; 8192 could not hold a compression of it.
        self.assertGreater(max_tokens, 8192)

    def test_output_at_the_cap_raises_instead_of_returning_a_truncated_body(self):
        client = _fake_client(text="first half only", stop_reason="max_tokens")
        with self.assertRaisesRegex(RuntimeError, "token cap"):
            self._call(client)


if __name__ == "__main__":
    unittest.main()
