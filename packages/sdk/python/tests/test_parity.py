"""Cross-language SDK parity conformance suite (Python half).

Runs every operation in the shared fixtures (../../parity/fixtures.json) against a
mocked transport and asserts the exact wire request (method, full URL, header set,
body) and the derived result. The TypeScript half (tests/parity.runtime.mjs) runs
the SAME fixtures. If TS and Python diverge on any field, one side fails — so this
file plus the fixtures are a release gate.

Run with: pytest (from public/sdk/python, or via `make product-test PRODUCT=sdk-python`).
"""

from __future__ import annotations

import json
import urllib.error
import urllib.request
from pathlib import Path
from typing import Any, Callable
from unittest.mock import MagicMock, patch

import pytest

from caveman_cloud import (
    AsyncJobsUnavailableError,
    AssembleOptions,
    AssemblySlot,
    Cave,
    CaveTool,
    ContextPackItem,
    ContextPackOptions,
    RetryLoopBreaker,
    RetryLoopError,
)

FIXTURES = json.loads((Path(__file__).resolve().parents[2] / "parity" / "fixtures.json").read_text(encoding="utf-8"))
CONFIG = FIXTURES["config"]
OPS = FIXTURES["operations"]


def _make_cave() -> Cave:
    return Cave(
        api_key=CONFIG["api_key"],
        base_url=CONFIG["base_url"],
        agent=CONFIG["agent"],
        default_workflow=CONFIG["default_workflow"],
        retention=CONFIG["retention"],
        control_url=CONFIG["control_url"],
        user=CONFIG["user"],
    )


def _fake_response(data: dict[str, Any]) -> MagicMock:
    body = json.dumps(data).encode()
    cm = MagicMock()
    cm.__enter__ = MagicMock(return_value=MagicMock(read=MagicMock(return_value=body)))
    cm.__exit__ = MagicMock(return_value=False)
    return cm


def _build_catalog(entries: list[dict[str, Any]]) -> list[CaveTool]:
    return [
        CaveTool(
            name=t["name"],
            description=t["description"],
            input_schema=t["input_schema"],
            read_only=t["read_only"],
            idempotent=t.get("idempotent", False),
            always_load=t["always_load"],
        )
        for t in entries
    ]


# ─── Per-operation handlers (return the canonical, snake-keyed result) ──────────


def _context_assemble(cave: Cave, inp: dict[str, Any]) -> dict[str, Any]:
    result = cave.assemble(
        AssembleOptions(
            provider=inp["provider"],
            model=inp["model"],
            session_id=inp["session_id"],
            slots=[AssemblySlot(**slot) for slot in inp["slots"]],
            emit_cache_hints=inp.get("emit_cache_hints", "gateway"),
        )
    )
    return {
        "request": result.request,
        "headers": result.headers,
        "prefix_hash": result.prefix_hash,
        "breakpoints": result.breakpoints,
        "stable_tokens": result.stable_tokens,
        "token_basis": result.token_basis,
        "basis": result.basis,
        "volatile_below_breakpoint": result.volatile_below_breakpoint,
    }


def _tool_search(cave: Cave, inp: dict[str, Any]) -> dict[str, Any]:
    catalog = _build_catalog(inp["catalog"])
    kwargs: dict[str, Any] = {}
    if "context" in inp:
        kwargs["context"] = inp["context"]
    if "max_tools" in inp:
        kwargs["max_tools"] = inp["max_tools"]
    if "ranker" in inp:
        kwargs["ranker"] = inp["ranker"]
    if "session_id" in inp:
        kwargs["session_id"] = inp["session_id"]
    r = cave.tool_search(catalog, inp["query"], **kwargs)
    out = {
        "sent_schema_tokens": r.sent_schema_tokens,
        "full_schema_tokens": r.full_schema_tokens,
        "deferred_count": r.deferred_count,
        "method": r.method,
        "token_basis": r.token_basis,
        "basis": r.basis,
        "saved_tokens": r.saved_tokens,
        "reduction_pct": r.reduction_pct,
        "tool_count": len(r.tools),
    }
    if r.session_id is not None:
        out["session_id"] = r.session_id
    return out


def _tools_builder_search(cave: Cave, inp: dict[str, Any]) -> dict[str, Any]:
    catalog = _build_catalog(inp["catalog"])
    handle = cave.tools(catalog, strategy=inp["strategy"])
    kwargs: dict[str, Any] = {}
    if "context" in inp:
        kwargs["context"] = inp["context"]
    if "max_tools" in inp:
        kwargs["max_tools"] = inp["max_tools"]
    if "ranker" in inp:
        kwargs["ranker"] = inp["ranker"]
    if "session_id" in inp:
        kwargs["session_id"] = inp["session_id"]
    r = handle.search(inp["query"], **kwargs)
    out = {
        "strategy": handle.strategy,
        "initial_tool_names": [t.name for t in handle.initial],
        "sent_schema_tokens": r.sent_schema_tokens,
        "full_schema_tokens": r.full_schema_tokens,
        "deferred_count": r.deferred_count,
        "method": r.method,
        "token_basis": r.token_basis,
        "basis": r.basis,
        "saved_tokens": r.saved_tokens,
        "reduction_pct": r.reduction_pct,
        "tool_count": len(r.tools),
    }
    if r.session_id is not None:
        out["session_id"] = r.session_id
    return out


def _artifacts_page(cave: Cave, inp: dict[str, Any]) -> Any:
    with cave.trace(trace_id=inp["trace_id"], span_id=inp["span_id"]) as t:
        return t.artifacts.page(inp["value"], inp["options"])


def _artifacts_get(cave: Cave, inp: dict[str, Any]) -> Any:
    with cave.trace(trace_id=inp["trace_id"], span_id=inp["span_id"]) as t:
        return t.artifacts.get(inp["artifact_id"])


def _model_create_async(cave: Cave, inp: dict[str, Any]) -> dict[str, Any]:
    with cave.trace(trace_id=inp["trace_id"], span_id=inp["span_id"]) as t:
        return t.model["openai"].responses.create(inp["body"], latency_class=inp["latency_class"])


def _model_create_traced(cave: Cave, inp: dict[str, Any]) -> dict[str, Any]:
    with cave.trace(trace_id=inp["trace_id"], span_id=inp["span_id"]) as t:
        return t.model["openai"].responses.create(inp["body"])


def _provider_create_untraced(cave: Cave, inp: dict[str, Any]) -> dict[str, Any]:
    # A provider client built off the Cave (not a trace) carries no continuity ids.
    return cave.openai().responses.create(inp["body"])


def _bedrock(cave: Cave, inp: dict[str, Any]) -> dict[str, Any]:
    kwargs: dict[str, Any] = {}
    if "endpoint" in inp:
        kwargs["endpoint"] = inp["endpoint"]
    return cave.bedrock(inp["region"], **kwargs)


def _compress(cave: Cave, inp: dict[str, Any]) -> dict[str, Any]:
    kwargs: dict[str, Any] = {}
    if "content_type" in inp:
        kwargs["content_type"] = inp["content_type"]
    r = cave.compress(inp["payload"], **kwargs)
    return {
        "output": r.output,
        "content_type": r.content_type,
        "tokens_before": r.tokens_before,
        "tokens_after": r.tokens_after,
        "ratio": r.ratio,
        "basis": r.basis,
        "token_count_basis": r.token_count_basis,
        "recovery_handle": r.recovery_handle,
        "method": r.method,
        "lossless_to_model": r.lossless_to_model,
    }


def _cave_plan(cave: Cave, inp: dict[str, Any]) -> dict[str, Any]:
    # cave_plan passes the plan through verbatim (snake_case wire fields), so
    # the parity result equals the canned response byte-for-byte.
    return cave.cave_plan()


def _checkpoint(cave: Cave, inp: dict[str, Any]) -> dict[str, Any]:
    with cave.trace(trace_id=inp["trace_id"], span_id=inp["span_id"]) as t:
        return t.checkpoint(inp["messages"], inp["options"])


def _context_pack(cave: Cave, inp: dict[str, Any]) -> dict[str, Any]:
    items = [ContextPackItem(**item) for item in inp["items"]]
    options = ContextPackOptions(**inp["options"])
    result = cave.context.pack(inp["query"], items, options)
    return {
        "items": [
            {
                key: value
                for key, value in {
                    "id": item.id,
                    "text": item.text,
                    "tokens": item.tokens,
                    "timestamp": item.timestamp,
                    "priority": item.priority,
                    "pin": item.pin,
                }.items()
                if value is not None
            }
            for item in result.items
        ],
        "tokens_used": result.tokens_used,
        "tokens_before": result.tokens_before,
        "tokens_saved": result.tokens_saved,
        "deferred_count": result.deferred_count,
        "deferred_ids": result.deferred_ids,
        "basis": result.basis,
    }


def _checkpoint_expand(cave: Cave, inp: dict[str, Any]) -> dict[str, Any]:
    with cave.trace(trace_id=inp["trace_id"], span_id=inp["span_id"]) as t:
        return t.expand(inp["source_ref"])


def _event_tool_call(cave: Cave, inp: dict[str, Any]) -> Any:
    with cave.trace(tags=inp["tags"], trace_id=inp["trace_id"], span_id=inp["span_id"]) as t:
        return t.tool(inp["name"], inp["options"], lambda: "tool-result")


def _otlp_export(cave: Cave, inp: dict[str, Any]) -> dict[str, Any]:
    ex = cave.exporter(service_name=inp["service_name"])
    s = inp["span"]
    ex.record_span(
        s["name"],
        trace_id=s["trace_id"],
        span_id=s["span_id"],
        start_time_ns=s["start_time_ns"],
        end_time_ns=s["end_time_ns"],
        operation=s["operation"],
        provider=s["provider"],
        model=s["model"],
        input_tokens=s["input_tokens"],
        output_tokens=s["output_tokens"],
    )
    return ex.export()


def _otlp_export_traced(cave: Cave, inp: dict[str, Any]) -> dict[str, Any]:
    # The trace-bound exporter reuses the trace's id, so SDK spans and the
    # gateway's request rows land in one trace.
    with cave.trace(trace_id=inp["trace_id"], span_id=inp["root_span_id"]) as t:
        ex = t.exporter(service_name=inp["service_name"])
        s = inp["span"]
        ex.record_span(
            s["name"],
            span_id=s["span_id"],
            parent_span_id=t.span_id,
            start_time_ns=s["start_time_ns"],
            end_time_ns=s["end_time_ns"],
            operation=s["operation"],
            provider=s["provider"],
            model=s["model"],
            input_tokens=s["input_tokens"],
            output_tokens=s["output_tokens"],
        )
        return ex.export()


def _jobs_unavailable(cave: Cave, inp: dict[str, Any]) -> dict[str, Any]:
    with pytest.raises(AsyncJobsUnavailableError) as exc_info:
        cave.jobs.submit(inp["body"], latency_class=inp["latency_class"])
    return {"error_code": exc_info.value.code}


def _run_breaker(_cave: Cave, inp: dict[str, Any]) -> dict[str, Any]:
    breaker = RetryLoopBreaker(threshold=inp["threshold"])
    fired_at_call = -1
    repeats = 0
    threshold = inp["threshold"]
    for i, call in enumerate(inp["calls"]):
        try:
            breaker.record(call["name"], call["args"])
        except RetryLoopError as e:
            fired_at_call = i
            repeats = e.repeats
            threshold = e.threshold
            break
    return {"fired_at_call": fired_at_call, "repeats": repeats, "threshold": threshold}


HANDLERS: dict[str, Callable[[Cave, dict[str, Any]], Any]] = {
    "context_assemble": _context_assemble,
    "context_assemble_self": _context_assemble,
    "context_assemble_none": _context_assemble,
    "tool_search": _tool_search,
    "tool_search_embeddings": _tool_search,
    "tools_builder_search": _tools_builder_search,
    "artifacts_page": _artifacts_page,
    "artifacts_get": _artifacts_get,
    "model_create_async": _model_create_async,
    "model_create_traced": _model_create_traced,
    "provider_create_untraced": _provider_create_untraced,
    "bedrock": _bedrock,
    "bedrock_mantle": _bedrock,
    "compress": _compress,
    "compress_toon": _compress,
    "compress_passthrough": _compress,
    "compress_bad_report": _compress,
    "compress_optimistic_ratio": _compress,
    "compress_unchanged_false_claim": _compress,
    "cave_plan": _cave_plan,
    "checkpoint": _checkpoint,
    "context_pack": _context_pack,
    "checkpoint_expand": _checkpoint_expand,
    "event_tool_call": _event_tool_call,
    "otlp_export": _otlp_export,
    "otlp_export_traced": _otlp_export_traced,
    "jobs_unavailable": _jobs_unavailable,
    "retry_loop_breaker": _run_breaker,
    "retry_loop_breaker_key_order": _run_breaker,
}


# ─── Drive every fixture ────────────────────────────────────────────────────────


@pytest.mark.parametrize("op", OPS, ids=[o["name"] for o in OPS])
def test_parity(op: dict[str, Any]) -> None:
    handler = HANDLERS.get(op["name"])
    assert handler is not None, f'no Python parity handler for operation "{op["name"]}" — the SDK is missing this capability'

    captured: list[dict[str, Any]] = []

    def fake_urlopen(req: Any, timeout: float) -> MagicMock:  # noqa: ANN401
        captured.append(
            {
                "url": req.full_url,
                "method": req.get_method(),
                "headers": {k.lower(): v for k, v in dict(req.headers).items()},
                "body": json.loads(req.data) if req.data else None,
            }
        )
        if op.get("transport") == "error":
            raise urllib.error.URLError("simulated transport error")
        return _fake_response(op.get("response", {}))

    with patch("urllib.request.urlopen", side_effect=fake_urlopen):
        actual = handler(_make_cave(), op["input"])

    wire = op["expect"].get("wire")
    if wire:
        assert len(captured) == 1, f'{op["name"]}: expected exactly one wire request, got {len(captured)}'
        req = captured[0]
        base = CONFIG["control_url"] if wire.get("base") == "control" else CONFIG["base_url"]
        assert req["url"] == base + wire["path"], f'{op["name"]}: url'
        assert req["method"] == wire["method"], f'{op["name"]}: method'
        exp_headers = FIXTURES[wire["headers"]] if isinstance(wire["headers"], str) else wire["headers"]
        assert req["headers"] == exp_headers, f'{op["name"]}: headers'
        if "body" in wire:
            assert req["body"] == wire["body"], f'{op["name"]}: body'
        elif "body_keys" in wire:
            assert sorted((req["body"] or {}).keys()) == sorted(wire["body_keys"]), f'{op["name"]}: body_keys'
    else:
        assert len(captured) == 0, f'{op["name"]}: expected no wire request'

    expected = op["response"] if op["expect"].get("result_from") == "response" else op["expect"].get("result")
    assert actual == expected, f'{op["name"]}: result'


def test_parity_fixtures_cover_surface() -> None:
    # Guard against an empty/short fixture file silently passing the gate.
    assert len(OPS) >= 10, "parity fixtures must cover the SDK surface"
    for op in OPS:
        assert op["name"] in HANDLERS, f'Python parity handler missing for "{op["name"]}"'


def test_tool_events_are_traced_while_bare_provider_calls_stay_untraced() -> None:
    event = next(op for op in OPS if op["name"] == "event_tool_call")
    bare = next(op for op in OPS if op["name"] == "provider_create_untraced")
    assert event["expect"]["wire"]["headers"] == "std_headers_traced"
    assert bare["expect"]["wire"]["headers"] == "std_headers"
    assert sorted(event["expect"]["wire"]["body_keys"]) == sorted(
        ("duration_ms", "name", "options", "outcome", "sequence", "span_type", "tags", "workflow")
    )
