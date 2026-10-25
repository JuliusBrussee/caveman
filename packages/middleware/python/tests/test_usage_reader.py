"""UsageReader keeps a response under its limit, however many chunks carry it."""
import json
import unittest

from caveman_middleware._usage import UsageReader


class UsageReaderLimitTests(unittest.TestCase):
    def test_json_response_under_the_limit_is_read_in_many_chunks(self):
        body = json.dumps({
            "choices": [{"message": {"content": "x" * 20000}}],
            "usage": {"prompt_tokens": 5, "completion_tokens": 7},
        }).encode()
        # 20 KB against a 64 KB limit: the reader must not give up on the
        # number of chunks it arrived in.
        for chunks in (1, 2, 8, 40):
            with self.subTest(chunks=chunks):
                reader = UsageReader(sse=False, max_buffer=65536)
                step = -(-len(body) // chunks)
                for start in range(0, len(body), step):
                    reader.feed(body[start:start + step])
                self.assertFalse(reader.overflow)
                usage = reader.finish()
                self.assertEqual((usage["input_tokens"], usage["output_tokens"]), (5, 7))

    def test_json_response_over_the_limit_still_overflows(self):
        reader = UsageReader(sse=False, max_buffer=1024)
        for _ in range(8):
            reader.feed(b"x" * 400)
        self.assertTrue(reader.overflow)
        self.assertIsNone(reader.finish())


if __name__ == "__main__":
    unittest.main()
