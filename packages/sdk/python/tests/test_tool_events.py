"""Mirrored tool-event producer contract. See sdk-typescript tool-events.runtime.mjs."""

from __future__ import annotations

import asyncio
import json
from concurrent.futures import ThreadPoolExecutor
from typing import Any
from unittest.mock import MagicMock, patch

import pytest

from caveman_cloud import Cave

TRACE_ID = "aaaabbbbccccddddeeeeffff00001111"
SPAN_ID = "1122334455667788"


def _response() -> MagicMock:
    body = json.dumps({"ok": True}).encode()
    context = MagicMock()
    context.__enter__ = MagicMock(return_value=MagicMock(read=MagicMock(return_value=body)))
    context.__exit__ = MagicMock(return_value=False)
    return context


def _capture(*, fail: bool = False) -> tuple[list[dict[str, Any]], Any]:
    captured: list[dict[str, Any]] = []

    def fake_urlopen(req: Any, timeout: float) -> MagicMock:  # noqa: ANN401
        captured.append(
            {
                "url": req.full_url,
                "method": req.get_method(),
                "headers": {key.lower(): value for key, value in dict(req.headers).items()},
                "body": json.loads(req.data),
            }
        )
        if fail:
            raise OSError("telemetry-transport-secret")
        return _response()

    return captured, fake_urlopen


def _cave() -> Cave:
    return Cave(api_key="cave_live_test", base_url="http://localhost:8787", agent="tool-event-agent")


def _assert_event(request: dict[str, Any], *, name: str, outcome: str, sequence: int, workflow: str = "unlabeled-workflow") -> None:
    assert request["url"] == "http://localhost:8787/sdk/v1/events"
    assert request["method"] == "POST"
    assert request["headers"]["x-cave-trace-id"] == TRACE_ID
    assert request["headers"]["x-cave-parent-span-id"] == SPAN_ID
    assert request["headers"]["x-cave-workflow"] == workflow
    assert request["body"]["span_type"] == "tool.call"
    assert request["body"]["name"] == name
    assert request["body"]["workflow"] == workflow
    assert request["body"]["outcome"] == outcome
    assert request["body"]["sequence"] == sequence
    assert isinstance(request["body"]["sequence"], int) and not isinstance(request["body"]["sequence"], bool) and request["body"]["sequence"] > 0
    assert isinstance(request["body"]["duration_ms"], int) and not isinstance(request["body"]["duration_ms"], bool) and request["body"]["duration_ms"] >= 0
    assert sorted(request["body"]) == sorted(("duration_ms", "name", "options", "outcome", "sequence", "span_type", "tags", "workflow"))


def test_success_returns_original_value_and_emits_joined_ok_event() -> None:
    captured, fake_urlopen = _capture()
    original = {"exact": "result"}
    with patch("urllib.request.urlopen", side_effect=fake_urlopen):
        with _cave().trace(workflow="event-workflow", tags={"env": "test"}, trace_id=TRACE_ID, span_id=SPAN_ID) as trace:
            result = trace.tool("lookup", {"read_only": True}, lambda: original)
    assert result is original
    assert len(captured) == 1
    _assert_event(captured[0], name="lookup", outcome="ok", sequence=1, workflow="event-workflow")


def test_synchronous_and_asynchronous_failures_emit_error_without_exception_leakage() -> None:
    captured, fake_urlopen = _capture()
    sync_error = RuntimeError("private-sync-exception-message")

    def sync_failure() -> Any:
        raise sync_error

    with patch("urllib.request.urlopen", side_effect=fake_urlopen):
        with _cave().trace(trace_id=TRACE_ID, span_id=SPAN_ID) as trace:
            with pytest.raises(RuntimeError) as raised:
                trace.tool("sync-danger", {}, sync_failure)
    assert raised.value is sync_error
    _assert_event(captured[0], name="sync-danger", outcome="error", sequence=1)
    assert sync_error.args[0] not in json.dumps(captured[0]["body"])

    captured, fake_urlopen = _capture()
    async_error = RuntimeError("private-async-exception-message")

    async def async_failure() -> Any:
        raise async_error

    async def run_async_failure() -> None:
        with patch("urllib.request.urlopen", side_effect=fake_urlopen):
            with _cave().trace(trace_id=TRACE_ID, span_id=SPAN_ID) as trace:
                await trace.tool("async-danger", {}, async_failure)

    with pytest.raises(RuntimeError) as raised:
        asyncio.run(run_async_failure())
    assert raised.value is async_error
    _assert_event(captured[0], name="async-danger", outcome="error", sequence=1)
    wire = json.dumps(captured[0]["body"])
    assert async_error.args[0] not in wire
    assert "traceback" not in wire


def test_cancellation_is_error_and_original_cancellation_propagates() -> None:
    captured, fake_urlopen = _capture()

    async def cancelled() -> Any:
        raise asyncio.CancelledError("private-cancel-message")

    async def run_cancelled() -> None:
        with patch("urllib.request.urlopen", side_effect=fake_urlopen):
            with _cave().trace(trace_id=TRACE_ID, span_id=SPAN_ID) as trace:
                await trace.tool("cancelled", {}, cancelled)

    with pytest.raises(asyncio.CancelledError):
        asyncio.run(run_cancelled())
    _assert_event(captured[0], name="cancelled", outcome="error", sequence=1)
    assert "private-cancel-message" not in json.dumps(captured[0]["body"])


def test_telemetry_failure_never_changes_successful_return_or_original_throw() -> None:
    captured, fake_urlopen = _capture(fail=True)
    value = {"preserved": True}
    with patch("urllib.request.urlopen", side_effect=fake_urlopen):
        with _cave().trace(trace_id=TRACE_ID, span_id=SPAN_ID) as trace:
            assert trace.tool("ok", {}, lambda: value) is value
    _assert_event(captured[0], name="ok", outcome="ok", sequence=1)

    captured, fake_urlopen = _capture(fail=True)
    original = RuntimeError("original-tool-secret")

    def fail() -> Any:
        raise original

    with patch("urllib.request.urlopen", side_effect=fake_urlopen):
        with _cave().trace(trace_id=TRACE_ID, span_id=SPAN_ID) as trace:
            with pytest.raises(RuntimeError) as raised:
                trace.tool("bad", {}, fail)
    assert raised.value is original
    _assert_event(captured[0], name="bad", outcome="error", sequence=1)
    assert original.args[0] not in json.dumps(captured[0]["body"])


def test_sequence_follows_start_order_when_async_completions_reverse() -> None:
    captured, fake_urlopen = _capture()

    async def run() -> None:
        first_gate = asyncio.Event()

        async def first() -> str:
            await first_gate.wait()
            return "first-result"

        async def second() -> str:
            return "second-result"

        with patch("urllib.request.urlopen", side_effect=fake_urlopen):
            with _cave().trace(trace_id=TRACE_ID, span_id=SPAN_ID) as trace:
                first_call = trace.tool("first", {}, first)
                second_call = trace.tool("second", {}, second)
                assert await second_call == "second-result"
                first_gate.set()
                assert await first_call == "first-result"

    asyncio.run(run())
    assert len(captured) == 2
    _assert_event(captured[0], name="second", outcome="ok", sequence=2)
    _assert_event(captured[1], name="first", outcome="ok", sequence=1)


def test_each_trace_owns_independent_sequence() -> None:
    captured, fake_urlopen = _capture()
    cave = _cave()
    with patch("urllib.request.urlopen", side_effect=fake_urlopen):
        with cave.trace(trace_id=TRACE_ID, span_id=SPAN_ID) as first:
            first.tool("one", {}, lambda: 1)
        with cave.trace(trace_id=TRACE_ID, span_id=SPAN_ID) as second:
            second.tool("two", {}, lambda: 2)
    assert [request["body"]["sequence"] for request in captured] == [1, 1]


def test_sequence_increment_is_thread_safe() -> None:
    captured, fake_urlopen = _capture()
    with patch("urllib.request.urlopen", side_effect=fake_urlopen):
        with _cave().trace(trace_id=TRACE_ID, span_id=SPAN_ID) as trace:
            with ThreadPoolExecutor(max_workers=8) as executor:
                results = list(executor.map(lambda index: trace.tool(f"tool-{index}", {}, lambda: index), range(64)))
    assert results == list(range(64))
    assert sorted(request["body"]["sequence"] for request in captured) == list(range(1, 65))
