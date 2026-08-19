"""cavemem — thin Python client (stdlib only).

It shells out to the ``cavemem`` Go binary (the single source of truth for
storage, BM25 recall, and engine compression); it reimplements none of that.
Resolve the binary via the ``CAVEMEM_BIN`` env var or PATH. Mirrors the TS client
in ``../js/index.mjs``.
"""
from __future__ import annotations

import json
import os
import subprocess
from typing import Any


MEMORY_TOO_LARGE_EXIT_CODE = 65


def _binary() -> str:
    return os.environ.get("CAVEMEM_BIN", "cavemem")


def _call(args: list[str], input_text: str | None = None) -> dict[str, Any]:
    # Pin utf-8 on both directions. text=True alone decodes the child's stdout
    # (and encodes our stdin) with the locale encoding, which on Windows is the
    # ANSI code page: remember("cafe\u0301") would raise UnicodeEncodeError
    # before the Go binary saw it, and any recall of a non-ASCII memory would
    # come back mojibake or blow up inside json.loads. The binary speaks utf-8
    # JSON on every platform, so say so.
    kwargs: dict[str, Any] = {
        "capture_output": True,
        "text": True,
        "check": True,
        "encoding": "utf-8",
    }
    if input_text is not None:
        kwargs["input"] = input_text
    proc = subprocess.run([_binary(), *args], **kwargs)
    return json.loads(proc.stdout)


def remember(text: str) -> dict[str, Any]:
    """Store a memory. Returns {id, created_at, basis}. Idempotent on identical text."""
    return _call(["remember", "--stdin"], text)


def recall(
    query: str,
    limit: int | None = None,
    token_budget: int | None = None,
) -> dict[str, Any]:
    """Recall memories. token_budget defaults to 2000; explicit 0 is unlimited."""
    args = ["recall", query]
    if limit is not None or token_budget is not None:
        args.append(str(limit if limit is not None else 0))
    if token_budget is not None:
        args.append(str(token_budget))
    return _call(args)


def supersede(mem_id: str, text: str) -> dict[str, Any]:
    """Replace one current memory while preserving its version history."""
    return _call(["supersede", mem_id, text])


def history(mem_id: str) -> dict[str, Any]:
    """Return oldest-to-newest versions for a memory lineage."""
    return _call(["history", mem_id])


def forget(mem_id: str) -> dict[str, Any]:
    """Delete a memory by id. Returns {forgotten}."""
    return _call(["forget", mem_id])
