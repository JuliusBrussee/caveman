from __future__ import annotations

import json
import urllib.request
from unittest.mock import MagicMock, patch

from caveman_cloud import Cave, ContextPackItem, ContextPackOptions


def _cave() -> Cave:
    return Cave(
        api_key="cave_live_test",
        base_url="http://localhost:8787",
        agent="context-test",
        default_workflow="pack",
    )


def _response(data: object) -> MagicMock:
    cm = MagicMock()
    cm.__enter__ = MagicMock(
        return_value=MagicMock(read=MagicMock(return_value=json.dumps(data).encode()))
    )
    cm.__exit__ = MagicMock(return_value=False)
    return cm


def test_context_pack_maps_wire_and_exact_deferred_ids() -> None:
    captured: list[urllib.request.Request] = []
    response = {
        "items": [{"id": "deploy", "text": "server copy is ignored"}],
        "tokens_used": 30,
        "tokens_before": 75,
        "tokens_saved": 45,
        "deferred_count": 2,
        "deferred_ids": ["intro", "billing"],
        "basis": "inferred",
    }

    def fake_urlopen(req: urllib.request.Request, timeout: float) -> MagicMock:
        captured.append(req)
        assert timeout == 30
        return _response(response)

    items = [
        ContextPackItem(id="intro", text="overview", tokens=20),
        ContextPackItem(id="deploy", text="deploy ERROR", tokens=30, pin=True),
        ContextPackItem(id="billing", text="billing polish", tokens=25),
    ]
    with patch("urllib.request.urlopen", side_effect=fake_urlopen):
        result = _cave().context.pack(
            "deploy failure",
            items,
            ContextPackOptions(
                max_tokens=30,
                reserve_tokens=5,
                recency_half_life_ms=3_600_000,
            ),
        )

    assert len(captured) == 1
    assert captured[0].full_url == "http://localhost:8787/sdk/v1/context/pack"
    assert captured[0].get_method() == "POST"
    body = captured[0].data
    assert isinstance(body, bytes)
    assert json.loads(body) == {
        "query": "deploy failure",
        "items": [
            {"id": "intro", "text": "overview", "tokens": 20},
            {"id": "deploy", "text": "deploy ERROR", "tokens": 30, "pin": True},
            {"id": "billing", "text": "billing polish", "tokens": 25},
        ],
        "options": {
            "max_tokens": 30,
            "reserve_tokens": 5,
            "recency_half_life_ms": 3_600_000,
        },
    }
    assert result.items == [items[1]]
    assert result.deferred_ids == ["intro", "billing"]
    assert result.deferred_count == 2
    assert result.tokens_saved == 45
    assert result.basis == "inferred"


def test_context_pack_fails_closed_to_honest_zero_on_malformed_report() -> None:
    items = [
        ContextPackItem(id="a", text="alpha"),
        ContextPackItem(id="b", text="beta"),
    ]
    response = {
        "items": [{"id": "a"}],
        "tokens_used": 5,
        "tokens_before": 10,
        "tokens_saved": 999,
        "deferred_count": 1,
        "deferred_ids": ["b"],
        "basis": "verified",
    }

    with patch("urllib.request.urlopen", return_value=_response(response)):
        result = _cave().context.pack(
            "alpha",
            items,
            ContextPackOptions(max_tokens=5),
        )

    assert result.items == items
    assert result.tokens_saved == 0
    assert result.deferred_ids == []
    assert result.basis == "inferred"
