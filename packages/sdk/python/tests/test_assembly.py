from __future__ import annotations

import hashlib
import json
from typing import Any
from unittest.mock import MagicMock, patch

import pytest

from caveman_cloud import (
    AssembleOptions,
    AssemblySlot,
    AssemblyStabilityError,
    Cave,
)


def _cave(agent: str) -> Cave:
    return Cave(
        api_key="cave_live_test",
        base_url="http://localhost:8787",
        agent=agent,
        default_workflow="assembly",
    )


def _slots(turn: int) -> list[AssemblySlot]:
    return [
        AssemblySlot(
            id="tools",
            stability="stable",
            content=[
                {
                    "name": "lookup",
                    "description": "Lookup one record",
                    "input_schema": {"type": "object"},
                }
            ],
        ),
        AssemblySlot(id="system", stability="stable", content="You are exact."),
        AssemblySlot(
            id="playbook",
            stability="session",
            content="Escalate refunds above 100.",
        ),
        AssemblySlot(id="turn", stability="volatile", content=f"turn {turn}"),
        AssemblySlot(
            id="clock",
            stability="volatile",
            content=f"2026-07-26T12:00:{turn:02d}Z",
        ),
    ]


def _canonical(value: Any) -> str:
    return json.dumps(
        value,
        sort_keys=True,
        separators=(",", ":"),
        ensure_ascii=False,
    )


def _contains_key(value: Any, key: str) -> bool:
    if isinstance(value, list):
        return any(_contains_key(item, key) for item in value)
    if isinstance(value, dict):
        return any(name == key or _contains_key(child, key) for name, child in value.items())
    return False


def _options(
    *,
    provider: str = "anthropic",
    session_id: str,
    turn: int,
    emit_cache_hints: str = "gateway",
) -> AssembleOptions:
    return AssembleOptions(
        provider=provider,
        model="claude-test" if provider == "anthropic" else "gpt-test",
        session_id=session_id,
        slots=_slots(turn),
        emit_cache_hints=emit_cache_hints,
    )


def test_assemble_keeps_prefix_bytes_identical_across_ten_volatile_turns() -> None:
    cave = _cave("assembly-ten-turn")
    hashes: list[str] = []
    for turn in range(10):
        built = cave.assemble(_options(session_id="ten-turn", turn=turn))
        prefix = {
            "model": built.request["model"],
            "system": built.request["system"],
            "tools": built.request["tools"],
        }
        digest = hashlib.sha256(_canonical(prefix).encode()).hexdigest()
        hashes.append(digest)
        assert built.prefix_hash == digest
        assert not _contains_key(built.request, "cache_control")
        assert built.breakpoints == []
        assert built.basis == "inferred"
        assert built.token_basis == "estimated_bytes_div_4"
        assert built.volatile_below_breakpoint is True
    assert len(set(hashes)) == 1


def test_assemble_hashes_canonical_utf8_prefix_bytes() -> None:
    built = _cave("assembly-unicode").assemble(
        AssembleOptions(
            provider="anthropic",
            model="claude-test",
            session_id="unicode",
            slots=[
                AssemblySlot(id="system", stability="stable", content="Précis 🦴"),
                AssemblySlot(id="turn", stability="volatile", content="réponds"),
            ],
        )
    )
    prefix = {"model": built.request["model"], "system": built.request["system"]}
    assert built.prefix_hash == hashlib.sha256(_canonical(prefix).encode()).hexdigest()


def test_mutating_stable_slot_on_turn_six_fails_without_request() -> None:
    cave = _cave("assembly-stability")
    for turn in range(5):
        cave.assemble(_options(session_id="mutation", turn=turn))
    changed = _slots(5)
    changed[1] = AssemblySlot(id="system", stability="stable", content="You changed.")
    with pytest.raises(AssemblyStabilityError, match="system") as caught:
        cave.assemble(
            AssembleOptions(
                provider="anthropic",
                model="claude-test",
                session_id="mutation",
                slots=changed,
            )
        )
    assert caught.value.slot_id == "system"


def test_anthropic_self_is_tools_first_and_none_emits_no_hints() -> None:
    cave = _cave("assembly-anthropic-modes")
    self_built = cave.assemble(
        _options(
            session_id="self",
            turn=1,
            emit_cache_hints="self",
        )
    )
    assert self_built.breakpoints == ["tools[0]"]
    assert self_built.request["tools"][0]["cache_control"] == {"type": "ephemeral"}
    assert all("cache_control" not in block for block in self_built.request["system"])

    none = cave.assemble(
        _options(
            session_id="none",
            turn=2,
            emit_cache_hints="none",
        )
    )
    assert not _contains_key(none.request, "cache_control")
    assert none.breakpoints == []


def test_openai_self_emits_key_and_unknown_provider_orders_only() -> None:
    cave = _cave("assembly-other-providers")
    openai = cave.assemble(
        _options(
            provider="openai",
            session_id="openai-self",
            turn=1,
            emit_cache_hints="self",
        )
    )
    assert openai.request["prompt_cache_key"] == openai.prefix_hash[:32]
    assert openai.breakpoints == ["prompt_cache_key"]
    assert [message["role"] for message in openai.request["messages"]] == [
        "system",
        "user",
        "user",
    ]

    source = _slots(1)
    unknown = cave.assemble(
        AssembleOptions(
            provider="other",
            model="other-model",
            session_id="unknown",
            slots=[source[3], source[1], source[2]],
            emit_cache_hints="self",
        )
    )
    assert [slot["stability"] for slot in unknown.request["slots"]] == [
        "stable",
        "session",
        "volatile",
    ]
    assert unknown.breakpoints == []


def test_provider_client_attaches_assembly_declaration_header() -> None:
    captured: dict[str, Any] = {}

    def fake_urlopen(req: Any, timeout: float) -> MagicMock:
        captured["url"] = req.full_url
        captured["headers"] = {key.lower(): value for key, value in dict(req.headers).items()}
        captured["body"] = json.loads(req.data)
        body = json.dumps({"ok": True}).encode()
        response = MagicMock(read=MagicMock(return_value=body))
        cm = MagicMock()
        cm.__enter__ = MagicMock(return_value=response)
        cm.__exit__ = MagicMock(return_value=False)
        return cm

    cave = _cave("assembly-header")
    built = cave.assemble(
        _options(provider="openai", session_id="header", turn=1)
    )
    with patch("urllib.request.urlopen", side_effect=fake_urlopen):
        cave.openai().chat["completions"].create(built.request)

    assert captured["url"] == "http://localhost:8787/openai/v1/chat/completions"
    assert (
        captured["headers"]["x-cave-assembly"]
        == built.headers["x-cave-assembly"]
    )
    assert captured["body"] == built.request
