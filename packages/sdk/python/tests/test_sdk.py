from __future__ import annotations

import json
import urllib.error
from unittest.mock import MagicMock, patch

import pytest

from caveman_cloud import Cave, CaveTool, ToolSearchResult


def test_init_no_network() -> None:
    cave = Cave(api_key="cave_live_abcdefghijkl_x", base_url="http://localhost:8787", agent="support-agent")
    assert cave.default_workflow == "unlabeled-workflow"


def test_bedrock_descriptor_defaults_to_runtime() -> None:
    cave = Cave(api_key="cave_live_abcdefghijkl_x", base_url="http://localhost:8787", agent="support-agent")
    assert cave.bedrock("eu-west-1") == {
        "region": "eu-west-1",
        "endpoint": "runtime",
        "gateway_prefix": "/bedrock",
        "instrumented": True,
        "sdk_only": False,
    }


def test_bedrock_descriptor_mantle_is_explicit() -> None:
    cave = Cave(api_key="cave_live_abcdefghijkl_x", base_url="http://localhost:8787", agent="support-agent")
    assert cave.bedrock("us-east-1", endpoint="mantle")["gateway_prefix"] == "/bedrock/anthropic"


def test_bedrock_descriptor_rejects_unknown_endpoint() -> None:
    cave = Cave(api_key="cave_live_abcdefghijkl_x", base_url="http://localhost:8787", agent="support-agent")
    with pytest.raises(ValueError, match="runtime or mantle"):
        cave.bedrock("us-east-1", endpoint="other")


def test_vertex_provider_client() -> None:
    cave = Cave(api_key="cave_live_abcdefghijkl_x", base_url="http://localhost:8787", agent="support-agent")
    provider = cave.vertex(upstream_key="ya29.token")
    assert provider.prefix == "/vertex"
    assert provider.upstream_key == "ya29.token"


def _fake_urlopen(response_data: dict) -> MagicMock:
    """Return a context-manager mock that yields a fake HTTP response."""
    body = json.dumps(response_data).encode()
    cm = MagicMock()
    cm.__enter__ = MagicMock(return_value=MagicMock(read=MagicMock(return_value=body)))
    cm.__exit__ = MagicMock(return_value=False)
    return cm


def test_tool_search_posts_correct_body_and_returns_result() -> None:
    """SDK posts the tool catalog + query to /sdk/v1/tool-search and parses the response."""
    mock_response = {
        "session_id": "tool-session-1",
        "tools": [
            {"name": "search_customers", "description": "Search customers by name or email"},
        ],
        "sent_schema_tokens": 120,
        "full_schema_tokens": 840,
        "deferred_count": 9,
        "method": "lexical-hit-rate",
        "token_basis": "estimated_bytes_div_4",
    }

    captured_requests: list[tuple[str, dict, dict]] = []

    def fake_urlopen(req, timeout):  # type: ignore[no-untyped-def]
        body = json.loads(req.data)
        captured_requests.append((req.full_url, dict(req.headers), body))
        return _fake_urlopen(mock_response)

    with patch("urllib.request.urlopen", side_effect=fake_urlopen):
        cave = Cave(api_key="cave_live_test_key", base_url="http://localhost:8787", agent="test-agent")
        catalog = [
            CaveTool(
                name="search_customers",
                description="Search customers by name or email",
                input_schema={"type": "object", "properties": {"query": {"type": "string"}}},
                read_only=True,
                always_load=False,
            ),
            CaveTool(
                name="fetch_order",
                description="Fetch order details by order ID",
                input_schema={"type": "object", "properties": {"order_id": {"type": "string"}}},
                read_only=True,
                always_load=False,
            ),
            *[
                CaveTool(
                    name=f"tool_{i}",
                    description=f"Tool number {i} for testing",
                    input_schema={"type": "object", "properties": {}},
                    read_only=False,
                    always_load=False,
                )
                for i in range(8)
            ],
        ]

        result = cave.tool_search(catalog, query="find customer", context="support workflow", max_tools=5, session_id="tool-session-1")

    # One HTTP call made
    assert len(captured_requests) == 1
    url, hdrs, body = captured_requests[0]

    # URL
    assert url == "http://localhost:8787/sdk/v1/tool-search"

    # Headers (urllib capitalises keys)
    assert hdrs["Authorization"] == "Bearer cave_live_test_key"
    assert hdrs["X-cave-agent"] == "test-agent"
    assert hdrs["Content-type"] == "application/json"

    # Body shape matches gateway contract
    assert body["query"] == "find customer"
    assert body["context"] == "support workflow"
    assert body["max_tools"] == 5
    assert body["session_id"] == "tool-session-1"
    assert len(body["tools"]) == 10  # 2 named + 8 generated

    first_tool = body["tools"][0]
    assert first_tool["name"] == "search_customers"
    assert "input_schema" in first_tool
    assert "read_only" in first_tool
    assert first_tool["idempotent"] is False
    assert "always_load" in first_tool

    # Parsed result
    assert isinstance(result, ToolSearchResult)
    assert len(result.tools) == 1
    assert result.tools[0]["name"] == "search_customers"
    assert result.sent_schema_tokens == 120
    assert result.full_schema_tokens == 840
    assert result.deferred_count == 9
    assert result.method == "lexical-hit-rate"
    assert result.session_id == "tool-session-1"
    assert result.token_basis == "estimated_bytes_div_4"
    assert result.basis == "inferred"

    # Derived helpers
    assert result.saved_tokens == 720
    assert result.reduction_pct == 85.7


def test_tool_search_malformed_counters_fail_closed() -> None:
    mock_response = {
        "tools": [],
        "sent_schema_tokens": 200,
        "full_schema_tokens": 100,
        "deferred_count": -4,
        "method": "lexical-hit-rate",
        "token_basis": "estimated_bytes_div_4",
    }

    with patch("urllib.request.urlopen", return_value=_fake_urlopen(mock_response)):
        cave = Cave(api_key="k", base_url="http://localhost:8787", agent="a")
        result = cave.tool_search([], query="anything")

    assert result.sent_schema_tokens == 0
    assert result.full_schema_tokens == 0
    assert result.saved_tokens == 0
    assert result.reduction_pct == 0
    assert result.deferred_count == 0
    assert result.basis == "inferred"


def test_tool_search_minimal_args() -> None:
    """tool_search works without optional context and max_tools."""
    mock_response = {"tools": [], "sent_schema_tokens": 0, "full_schema_tokens": 100, "deferred_count": 3, "method": "lexical-hit-rate"}

    captured_bodies: list[dict] = []

    def fake_urlopen(req, timeout):  # type: ignore[no-untyped-def]
        captured_bodies.append(json.loads(req.data))
        return _fake_urlopen(mock_response)

    with patch("urllib.request.urlopen", side_effect=fake_urlopen):
        cave = Cave(api_key="cave_live_test_key", base_url="http://localhost:8787", agent="test-agent")
        result = cave.tool_search([], query="anything")

    body = captured_bodies[0]
    assert "context" not in body
    assert "max_tools" not in body
    assert "session_id" not in body
    assert result.reduction_pct == 100.0


def test_tools_handle_applies_default_cap_and_allows_override() -> None:
    mock_response = {"tools": [], "sent_schema_tokens": 0, "full_schema_tokens": 10, "deferred_count": 1, "method": "bm25"}
    bodies: list[dict] = []

    def fake_urlopen(req, timeout):  # type: ignore[no-untyped-def]
        bodies.append(json.loads(req.data))
        return _fake_urlopen(mock_response)

    tool = CaveTool(name="safe", description="safe", input_schema={}, read_only=True, idempotent=True)
    with patch("urllib.request.urlopen", side_effect=fake_urlopen):
        cave = Cave(api_key="k", base_url="http://localhost:8787", agent="a")
        handle = cave.tools([tool], strategy="deferred", max_loaded_tools=2)
        handle.search("first")
        handle.search("second", max_tools=1)
        with pytest.raises(ValueError, match="positive integer"):
            cave.tools([tool], max_loaded_tools=0)

    assert bodies[0]["max_tools"] == 2
    assert bodies[1]["max_tools"] == 1
    assert bodies[0]["tools"][0]["idempotent"] is True


def test_tools_handle_validates_deferred_counts_and_never_hides_mandatory_tools() -> None:
    cave = Cave(api_key="k", base_url="http://localhost:8787", agent="a")
    always = CaveTool(name="always", description="mandatory", input_schema={}, read_only=True, idempotent=True, always_load=True)
    lazy_one = CaveTool(name="lazy1", description="lazy", input_schema={}, read_only=True, idempotent=True)
    lazy_two = CaveTool(name="lazy2", description="lazy", input_schema={}, read_only=True, idempotent=True)
    handle = cave.tools([always, lazy_one, lazy_two], strategy="deferred", initial_tool_count=8, max_loaded_tools=2)
    assert [tool.name for tool in handle.initial] == ["always", "lazy1"]
    with pytest.raises(ValueError, match="strategy"):
        cave.tools([always], strategy="unknown")
    with pytest.raises(ValueError, match="non-negative integer"):
        cave.tools([always], initial_tool_count=-1)
    with pytest.raises(ValueError, match="positive integer"):
        cave.tool_search([always], "query", max_tools=0)
    always_two = CaveTool(name="always2", description="mandatory", input_schema={}, read_only=True, idempotent=True, always_load=True)
    with pytest.raises(ValueError, match="always_load tool count"):
        cave.tools([always, always_two], strategy="deferred", max_loaded_tools=1)


def test_tool_search_uses_custom_workflow_header() -> None:
    """tool_search sends the workflow in the x-cave-workflow header."""
    mock_response = {"tools": [], "sent_schema_tokens": 50, "full_schema_tokens": 200, "deferred_count": 2, "method": "lexical-hit-rate"}

    captured_headers: list[dict] = []

    def fake_urlopen(req, timeout):  # type: ignore[no-untyped-def]
        captured_headers.append(dict(req.headers))
        return _fake_urlopen(mock_response)

    with patch("urllib.request.urlopen", side_effect=fake_urlopen):
        cave = Cave(api_key="k", base_url="http://localhost:8787", agent="a", default_workflow="my-workflow")
        cave.tool_search([], query="test", workflow="custom-workflow")

    assert captured_headers[0]["X-cave-workflow"] == "custom-workflow"


def test_provider_request_can_send_tool_session_header() -> None:
    captured_headers: list[dict] = []

    def fake_urlopen(req, timeout):  # type: ignore[no-untyped-def]
        captured_headers.append(dict(req.headers))
        return _fake_urlopen({"ok": True})

    with patch("urllib.request.urlopen", side_effect=fake_urlopen):
        cave = Cave(api_key="k", base_url="http://localhost:8787", agent="a")
        cave.openai().chat["completions"].create({"model": "gpt-5.5", "messages": []}, tool_session_id="tool-session-1")

    assert captured_headers[0]["X-cave-tool-session"] == "tool-session-1"


def test_service_urls_normalize_trailing_slashes_and_reject_query_or_fragment() -> None:
    captured: list[str] = []

    def fake_urlopen(req, timeout):  # type: ignore[no-untyped-def]
        captured.append(req.full_url)
        return _fake_urlopen({
            "output": "small",
            "content_type": "text/plain",
            "tokens_before": 2,
            "tokens_after": 1,
        })

    with patch("urllib.request.urlopen", side_effect=fake_urlopen):
        cave = Cave(
            api_key="k",
            base_url="https://gateway.example///",
            control_url="https://control.example/",
            agent="a",
        )
        assert cave.base_url == "https://gateway.example"
        assert cave.control_url == "https://control.example"
        cave.compress("large input")
    assert captured == ["https://gateway.example/sdk/v1/compress"]

    for base_url in (
        "https://gateway.example/?tenant=x",
        "https://gateway.example/#unsafe",
        "relative/path",
    ):
        with pytest.raises(ValueError, match="base_url must"):
            Cave(api_key="k", base_url=base_url, agent="a")


# A representative project-scope Cave Plan (snake_case wire fields).
_PLAN = {
    "scope": "project",
    "project_id": "proj_abc",
    "headline": {"low": 18.4, "base": 31.2, "high": 47.9, "basis": "inferred", "move_count": 2},
    "headroom_by_class": [
        {"safety_class": "S2_STRUCTURAL", "low": 12.0, "base": 21.0, "high": 33.0, "move_count": 1},
    ],
    "moves": [
        {
            "optimizer_id": "tool-result-truncation",
            "title": "Truncate oversized tool results",
            "family": "input_bloat",
            "safety_class": "S2_STRUCTURAL",
            "basis": "inferred",
            "confidence": "medium",
            "quality_risk": "low",
            "implementation_effort": "small",
            "requires_eval_gate": True,
            "scope_count": 3,
            "sample_size": 4120,
            "savings_usd_low": 12.0,
            "savings_usd_base": 21.0,
            "savings_usd_high": 33.0,
            "share_of_headline_pct": 67.3,
            "next_action": "Cap tool-result size at the callsite.",
            "summary": "Oversized tool results re-enter context on support-triage.",
            "top_scopes": [
                {"workflow_id": "wf_1", "workflow_name": "support-triage", "savings_usd_base": 15.0, "share_pct": 71.4},
            ],
        },
    ],
    "no_signal": [
        {"optimizer_id": "toon-reencoding", "title": "TOON re-encode tabular JSON", "reason_code": "no_detector_yet", "reason": "No detector emits this signal yet."},
    ],
    "methodology": "Inferred per-day headroom from the last 7 closed days of telemetry.",
    "as_of": "2026-07-21",
}


def test_cave_plan_gets_plan_with_key_header() -> None:
    """cave_plan GETs /sdk/v1/cave-plan with x-cave-api-key and returns the plan verbatim."""
    captured: list[dict] = []

    def fake_urlopen(req, timeout):  # type: ignore[no-untyped-def]
        captured.append({"url": req.full_url, "method": req.get_method(), "headers": dict(req.headers), "body": req.data})
        return _fake_urlopen(_PLAN)

    with patch("urllib.request.urlopen", side_effect=fake_urlopen):
        cave = Cave(api_key="cave_live_plan_key", base_url="http://localhost:8787", agent="test-agent")
        plan = cave.cave_plan()

    req = captured[0]
    # Control-plane read: no control_url set → falls back to base_url, GET, no body.
    assert req["url"] == "http://localhost:8787/sdk/v1/cave-plan"
    assert req["method"] == "GET"
    assert req["body"] is None
    # Project-key auth via x-cave-api-key — never the gateway Bearer header.
    assert req["headers"]["X-cave-api-key"] == "cave_live_plan_key"
    assert "Authorization" not in req["headers"]
    # Passed through verbatim: snake_case wire fields, inferred per-day figures.
    assert plan["scope"] == "project"
    assert plan["project_id"] == "proj_abc"
    assert plan["headline"]["basis"] == "inferred"
    assert plan["moves"][0]["optimizer_id"] == "tool-result-truncation"
    assert plan["moves"][0]["safety_class"] == "S2_STRUCTURAL"
    assert plan["moves"][0]["savings_usd_base"] == 21.0
    assert plan["moves"][0]["top_scopes"][0]["workflow_name"] == "support-triage"
    assert plan["no_signal"][0]["reason_code"] == "no_detector_yet"


def test_cave_plan_stays_on_gateway_when_control_url_is_set() -> None:
    captured: list[str] = []

    def fake_urlopen(req, timeout):  # type: ignore[no-untyped-def]
        captured.append(req.full_url)
        return _fake_urlopen(_PLAN)

    with patch("urllib.request.urlopen", side_effect=fake_urlopen):
        cave = Cave(api_key="k", base_url="http://gateway.test", agent="a", control_url="http://control.test")
        cave.cave_plan()

    assert captured[0] == "http://gateway.test/sdk/v1/cave-plan"


def test_cave_plan_propagates_non_200() -> None:
    """A non-200 raises HTTPError (no byte-safe pass-through — this reads state)."""

    def fake_urlopen(req, timeout):  # type: ignore[no-untyped-def]
        raise urllib.error.HTTPError(req.full_url, 403, "Forbidden", {}, None)  # type: ignore[arg-type]

    with patch("urllib.request.urlopen", side_effect=fake_urlopen):
        cave = Cave(api_key="k", base_url="http://localhost:8787", agent="a")
        with pytest.raises(urllib.error.HTTPError):
            cave.cave_plan()
