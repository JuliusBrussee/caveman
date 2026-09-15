from __future__ import annotations

import json
import subprocess
import sys
from types import SimpleNamespace

import pytest

import cavemem


def test_python_client_exports_the_too_large_process_exit_contract() -> None:
    assert cavemem.MEMORY_TOO_LARGE_EXIT_CODE == 65


def test_python_client_forwards_every_command(monkeypatch: pytest.MonkeyPatch) -> None:
    calls: list[tuple[list[str], dict[str, object]]] = []

    def fake_run(command: list[str], **kwargs: object) -> SimpleNamespace:
        calls.append((command, kwargs))
        return SimpleNamespace(stdout=json.dumps({"args": command[1:], "basis": "inferred"}))

    monkeypatch.setenv("CAVEMEM_BIN", "/fixture/cavemem")
    monkeypatch.setattr(cavemem.subprocess, "run", fake_run)

    assert cavemem.remember("raw memory")["args"] == ["remember", "--stdin"]
    assert cavemem.recall("billing")["args"] == ["recall", "billing"]
    assert cavemem.recall("billing", 4)["args"] == ["recall", "billing", "4"]
    assert cavemem.recall("billing", 4, 900)["args"] == ["recall", "billing", "4", "900"]
    assert cavemem.recall("billing", token_budget=0)["args"] == ["recall", "billing", "0", "0"]
    assert cavemem.supersede("mem_1", "replacement")["args"] == ["supersede", "mem_1", "replacement"]
    assert cavemem.history("mem_1")["args"] == ["history", "mem_1"]
    assert cavemem.forget("mem_1")["args"] == ["forget", "mem_1"]
    assert all(command[0] == "/fixture/cavemem" for command, _ in calls)
    # encoding is pinned so a non-ASCII memory survives a Windows locale that
    # is not utf-8 — text=True alone would use the ANSI code page there.
    base = {"capture_output": True, "text": True, "check": True, "encoding": "utf-8"}
    assert calls[0][1] == {**base, "input": "raw memory"}
    assert all(kwargs == base for _, kwargs in calls[1:])


def test_python_client_round_trips_non_ascii_memory(
    monkeypatch: pytest.MonkeyPatch,
    tmp_path,
) -> None:
    """text=True with no encoding= encodes stdin and decodes stdout with the
    locale encoding; on a cp1252 Windows this raised before the binary ever
    saw the text, and mojibake'd anything that came back."""
    binary = tmp_path / "cavemem-echo.py"
    binary.write_text(
        "#!/usr/bin/env python3\n"
        "import json, sys\n"
        "sys.stdout.buffer.write(json.dumps({'text': sys.stdin.buffer.read().decode('utf-8')},\n"
        "                                   ensure_ascii=False).encode('utf-8'))\n",
        encoding="utf-8",
    )
    monkeypatch.setenv("CAVEMEM_BIN", str(binary))
    real_run = subprocess.run

    def run_python_fixture(command: list[str], **kwargs: object):
        return real_run([sys.executable, str(binary), *command[1:]], **kwargs)

    monkeypatch.setattr(cavemem.subprocess, "run", run_python_fixture)
    assert cavemem.remember("café ≠ tea 記憶")["text"] == "café ≠ tea 記憶"


def test_python_client_oversized_memory_reaches_stable_exit_contract(
    monkeypatch: pytest.MonkeyPatch,
    tmp_path,
) -> None:
    binary = tmp_path / "cavemem-fixture.py"
    binary.write_text(
        "#!/usr/bin/env python3\n"
        "import sys\n"
        "data = sys.stdin.buffer.read()\n"
        "raise SystemExit(65 if len(data) > 256 * 1024 else 0)\n",
        encoding="utf-8",
    )
    monkeypatch.setenv("CAVEMEM_BIN", str(binary))
    real_run = subprocess.run

    def run_python_fixture(command: list[str], **kwargs: object):
        return real_run([sys.executable, str(binary), *command[1:]], **kwargs)

    monkeypatch.setattr(cavemem.subprocess, "run", run_python_fixture)
    with pytest.raises(subprocess.CalledProcessError) as caught:
        cavemem.remember("x" * (256 * 1024 + 1))
    assert caught.value.returncode == cavemem.MEMORY_TOO_LARGE_EXIT_CODE


def test_python_client_uses_path_default(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.delenv("CAVEMEM_BIN", raising=False)
    assert cavemem._binary() == "cavemem"


def test_python_client_propagates_process_and_json_failures(monkeypatch: pytest.MonkeyPatch) -> None:
    def process_failure(*_args: object, **_kwargs: object) -> SimpleNamespace:
        raise subprocess.CalledProcessError(17, ["cavemem"])

    monkeypatch.setattr(cavemem.subprocess, "run", process_failure)
    with pytest.raises(subprocess.CalledProcessError):
        cavemem.remember("failure")

    monkeypatch.setattr(
        cavemem.subprocess,
        "run",
        lambda *_args, **_kwargs: SimpleNamespace(stdout="not json"),
    )
    with pytest.raises(json.JSONDecodeError):
        cavemem.remember("invalid")
