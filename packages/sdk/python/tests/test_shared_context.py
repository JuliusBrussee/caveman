"""Tests for the S12 shared-context SDK surface. Mirrors the TS shared-context tests."""

from __future__ import annotations

import json
from typing import Any
from unittest.mock import MagicMock, patch

from caveman_cloud import Cave


def _fake_response(data: dict[str, Any]) -> MagicMock:
    body = json.dumps(data).encode()
    cm = MagicMock()
    cm.__enter__ = MagicMock(return_value=MagicMock(read=MagicMock(return_value=body)))
    cm.__exit__ = MagicMock(return_value=False)
    return cm


def test_shared_context_put_posts_session_key_and_content() -> None:
    captured: list[tuple[str, str, dict[str, Any]]] = []

    def fake_urlopen(req: Any, timeout: float) -> MagicMock:  # noqa: ANN401
        captured.append((req.full_url, req.get_method(), json.loads(req.data)))
        return _fake_response({"session_key": "h1", "bytes": 5, "stored": True})

    with patch("urllib.request.urlopen", side_effect=fake_urlopen):
        cave = Cave(api_key="cave_live_test_key", base_url="http://localhost:8787", agent="a")
        out = cave.shared_context.put("h1", "hello")

    assert captured[0][0] == "http://localhost:8787/sdk/v1/shared-context"
    assert captured[0][1] == "POST"
    assert captured[0][2] == {"session_key": "h1", "content": "hello"}
    assert out["stored"] is True


def test_shared_context_get_recovers_content() -> None:
    captured: list[tuple[str, str]] = []

    def fake_urlopen(req: Any, timeout: float) -> MagicMock:  # noqa: ANN401
        captured.append((req.full_url, req.get_method()))
        return _fake_response({"session_key": "h1", "content": "héllo 🦴"})

    with patch("urllib.request.urlopen", side_effect=fake_urlopen):
        cave = Cave(api_key="cave_live_test_key", base_url="http://localhost:8787", agent="a")
        out = cave.shared_context.get("h1")

    assert captured[0][0] == "http://localhost:8787/sdk/v1/shared-context/h1"
    assert captured[0][1] == "GET"
    assert out["content"] == "héllo 🦴"
