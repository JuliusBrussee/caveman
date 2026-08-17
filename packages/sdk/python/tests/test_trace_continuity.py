"""Tests for SDK → gateway trace continuity. Mirrors the TS trace-continuity tests."""

from __future__ import annotations

import json
import re
import threading
import time
from typing import Any
from unittest.mock import MagicMock, patch

from caveman_cloud import Cave
from caveman_cloud.core import OTelExporter as CoreOTelExporter

TRACE_ID = re.compile(r"^[0-9a-f]{32}$")
SPAN_ID = re.compile(r"^[0-9a-f]{16}$")


def _fake_response(data: dict[str, Any]) -> MagicMock:
    body = json.dumps(data).encode()
    cm = MagicMock()
    cm.__enter__ = MagicMock(return_value=MagicMock(read=MagicMock(return_value=body)))
    cm.__exit__ = MagicMock(return_value=False)
    return cm


def _cave() -> Cave:
    return Cave(api_key="cave_live_test_key", base_url="http://localhost:8787", agent="a")


def _capture(data: dict[str, Any]) -> tuple[list[dict[str, Any]], Any]:
    """A urlopen stub plus the list it records lowercased headers into."""
    captured: list[dict[str, Any]] = []

    def fake_urlopen(req: Any, timeout: float) -> MagicMock:  # noqa: ANN401
        captured.append({"url": req.full_url, "headers": {k.lower(): v for k, v in dict(req.headers).items()}})
        return _fake_response(data)

    return captured, fake_urlopen


def test_trace_mints_hex_ids() -> None:
    with _cave().trace() as t:
        assert TRACE_ID.match(t.trace_id)
        assert SPAN_ID.match(t.span_id)


def test_two_traces_get_distinct_ids() -> None:
    cave = _cave()
    with cave.trace() as first, cave.trace() as second:
        assert first.trace_id != second.trace_id
        assert first.span_id != second.span_id


def test_provider_calls_inside_a_trace_carry_continuity_headers() -> None:
    captured, fake_urlopen = _capture({"id": "resp_1"})
    with patch("urllib.request.urlopen", side_effect=fake_urlopen):
        with _cave().trace() as t:
            t.model["openai"].responses.create({"model": "gpt-4o", "input": "hi"})
            t.model["openai"].chat["completions"].create({"model": "gpt-4o", "messages": []})
            trace_id, span_id = t.trace_id, t.span_id

    assert len(captured) == 2
    for req in captured:
        assert req["headers"]["x-cave-trace-id"] == trace_id
        assert req["headers"]["x-cave-parent-span-id"] == span_id


def test_injected_ids_are_used_verbatim() -> None:
    captured, fake_urlopen = _capture({"id": "resp_1"})
    with patch("urllib.request.urlopen", side_effect=fake_urlopen):
        with _cave().trace(trace_id="0123456789abcdef0123456789abcdef", span_id="fedcba9876543210") as t:
            assert t.trace_id == "0123456789abcdef0123456789abcdef"
            assert t.span_id == "fedcba9876543210"
            t.model["openai"].responses.create({"model": "gpt-4o", "input": "hi"})

    assert captured[0]["headers"]["x-cave-trace-id"] == "0123456789abcdef0123456789abcdef"
    assert captured[0]["headers"]["x-cave-parent-span-id"] == "fedcba9876543210"


def test_injected_ids_are_canonicalized_or_replaced() -> None:
    captured, fake_urlopen = _capture({"id": "resp_1"})
    cave = _cave()
    with patch("urllib.request.urlopen", side_effect=fake_urlopen):
        with cave.trace(trace_id="0123456789ABCDEF0123456789ABCDEF", span_id="FEDCBA9876543210") as t:
            assert t.trace_id == "0123456789abcdef0123456789abcdef"
            assert t.span_id == "fedcba9876543210"
        for trace_id in ("0123", "zz23456789abcdef0123456789abcdef", "0" * 32):
            with cave.trace(trace_id=trace_id) as t:
                assert TRACE_ID.match(t.trace_id)
                assert t.trace_id != trace_id
                t.model["openai"].responses.create({"model": "gpt-4o", "input": "hi"})
        for span_id in ("01", "zedcba9876543210", "0" * 16):
            with cave.trace(span_id=span_id) as t:
                assert SPAN_ID.match(t.span_id)
                assert t.span_id != span_id
                t.model["openai"].responses.create({"model": "gpt-4o", "input": "hi"})

    for req in captured:
        assert TRACE_ID.match(req["headers"]["x-cave-trace-id"])
        assert SPAN_ID.match(req["headers"]["x-cave-parent-span-id"])


def test_trace_scoped_sdk_calls_carry_continuity_headers() -> None:
    captured, fake_urlopen = _capture({"ok": True, "artifact_id": "art_1", "stored": True})
    with patch("urllib.request.urlopen", side_effect=fake_urlopen):
        with _cave().trace() as t:
            t.tool("do_thing", {"read_only": True}, lambda: "ok")
            t.artifacts.page({"big": "payload"}, {"source": "tool:fetch", "strategy": "json-index"})
            t.checkpoint([{"role": "user", "content": "hi"}], {"keep_last": 1})
            t.expand("tenant/ref")
            trace_id, span_id = t.trace_id, t.span_id

    assert len(captured) == 4
    for req in captured:
        assert req["headers"]["x-cave-trace-id"] == trace_id
        assert req["headers"]["x-cave-parent-span-id"] == span_id


def test_provider_clients_off_the_cave_carry_no_continuity_headers() -> None:
    captured, fake_urlopen = _capture({"id": "resp_1"})
    with patch("urllib.request.urlopen", side_effect=fake_urlopen):
        _cave().openai().responses.create({"model": "gpt-4o", "input": "hi"})

    assert "x-cave-trace-id" not in captured[0]["headers"]
    assert "x-cave-parent-span-id" not in captured[0]["headers"]


def test_trace_bound_exporter_reuses_the_trace_id() -> None:
    payloads: list[dict[str, Any]] = []

    def fake_urlopen(req: Any, timeout: float) -> MagicMock:  # noqa: ANN401
        payloads.append(json.loads(req.data))
        return _fake_response({"ok": True, "spans_accepted": 1, "spans_total": 1})

    with patch("urllib.request.urlopen", side_effect=fake_urlopen):
        with _cave().trace() as t:
            exporter = t.exporter()
            assert t.exporter() is exporter
            assert exporter.default_trace_id == t.trace_id
            span = exporter.record_span("plan", parent_span_id=t.span_id)
            assert span.trace_id == t.trace_id
            assert span.parent_span_id == t.span_id
            exporter.export()
            trace_id = t.trace_id

    assert payloads[0]["resourceSpans"][0]["scopeSpans"][0]["spans"][0]["traceId"] == trace_id


def test_trace_exporters_with_different_service_names_keep_separate_buffers() -> None:
    with _cave().trace() as trace:
        default_exporter = trace.exporter()
        custom_exporter = trace.exporter(service_name="custom")
        assert default_exporter is not custom_exporter
        assert trace.exporter(service_name="custom") is custom_exporter


def test_concurrent_first_exporter_lookup_returns_one_buffer() -> None:
    with _cave().trace() as trace:
        created: list[Any] = []

        def slow_exporter(*args: Any, **kwargs: Any) -> Any:
            time.sleep(0.01)  # release the GIL so an unlocked lookup deterministically races
            exporter = CoreOTelExporter(*args, **kwargs)
            created.append(exporter)
            return exporter

        workers = 8
        barrier = threading.Barrier(workers)
        results: list[Any] = [None] * workers

        def resolve(index: int) -> None:
            barrier.wait()
            results[index] = trace.exporter()

        with patch("caveman_cloud.core.OTelExporter", side_effect=slow_exporter):
            threads = [threading.Thread(target=resolve, args=(index,)) for index in range(workers)]
            for thread in threads:
                thread.start()
            for thread in threads:
                thread.join(timeout=1)

        assert all(not thread.is_alive() for thread in threads)
        assert len(created) == 1
        assert all(exporter is results[0] for exporter in results)


def test_explicit_span_trace_id_wins_over_the_trace_binding() -> None:
    with _cave().trace() as t:
        span = t.exporter().record_span("plan", trace_id="11112222333344445555666677778888")
        assert span.trace_id == "11112222333344445555666677778888"


def test_cave_level_exporter_has_no_default_trace_id() -> None:
    exporter = _cave().exporter()
    assert exporter.default_trace_id is None
    assert TRACE_ID.match(exporter.record_span("plan").trace_id)
