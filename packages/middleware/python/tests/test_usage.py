"""Usage accounting must not depend on transport chunk boundaries."""
import json

import pytest

from caveman_middleware._usage import UsageReader


@pytest.mark.parametrize("sse", [False, True])
@pytest.mark.parametrize("text", ["x" * 700, "é" * 350, "🪨" * 175], ids=["ascii", "two-byte", "four-byte"])
@pytest.mark.parametrize("chunk_size", [1, 127, 1024])
def test_usage_accepts_body_at_exact_byte_limit(sse, text, chunk_size):
    body = json.dumps({"text": text, "usage": {"input_tokens": 11, "output_tokens": 7}},
                      ensure_ascii=False).encode("utf-8")
    if sse:
        body = b"data: " + body + b"\n"
    reader = UsageReader(sse=sse, max_buffer=len(body))
    for offset in range(0, len(body), chunk_size):
        reader.feed(body[offset:offset + chunk_size])
    if sse:
        reader.feed(b"data: [DONE]\n")
    result = reader.finish()
    assert result is not None
    assert result["complete"]
    assert (result["input_tokens"], result["output_tokens"]) == (11, 7)


@pytest.mark.parametrize("sse", [False, True])
def test_usage_rejects_over_limit_even_with_split_utf8(sse):
    reader = UsageReader(sse=sse, max_buffer=12)
    for byte in ("🪨" * 4).encode("utf-8"):
        reader.feed(bytes([byte]))
    assert reader.overflow
    assert reader.finish() is None


def test_sse_reclaims_consumed_lines_and_keeps_usage_through_unknown_events():
    reader = UsageReader(sse=True, max_buffer=100)
    for _ in range(50):
        reader.feed(b'data: {"type":"ping"}\n\n')
    reader.feed(b'data: {"usage":{"input_tokens":11}}\n')
    reader.feed(b'data: {"usage":{"output_tokens":7}}\n')
    reader.feed(b'data: {"type":"message_stop"}\n')
    assert reader.finish()["complete"]


def test_invalid_utf8_does_not_change_byte_budget():
    body = b'{"text":"\xff","usage":{"input_tokens":1,"output_tokens":2}}'
    reader = UsageReader(max_buffer=len(body))
    for byte in body:
        reader.feed(bytes([byte]))
    assert reader.finish()["complete"]
