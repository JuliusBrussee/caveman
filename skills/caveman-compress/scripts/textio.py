"""UTF-8 text I/O helpers for caveman-compress (issues #686, #766)."""

from __future__ import annotations

import os
from pathlib import Path

UTF8 = "utf-8"


def read_text(path: Path) -> str:
    return path.read_text(encoding=UTF8)


def write_text(path: Path, text: str) -> None:
    path.write_text(text, encoding=UTF8)


def write_text_atomic(path: Path, text: str) -> None:
    """Write UTF-8 text atomically so a failed encode cannot truncate the target."""
    tmp_path = path.with_name(path.name + ".tmp")
    try:
        write_text(tmp_path, text)
        os.replace(tmp_path, path)
    finally:
        tmp_path.unlink(missing_ok=True)
