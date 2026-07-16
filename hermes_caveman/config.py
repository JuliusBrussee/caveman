"""Default-mode configuration discovery for the Hermes Caveman plugin."""

from __future__ import annotations

import json
import os
from pathlib import Path
import stat
import sys
from typing import Mapping


CANONICAL_MODES = frozenset(
    {
        "lite",
        "full",
        "ultra",
        "wenyan-lite",
        "wenyan-full",
        "wenyan-ultra",
        "off",
    }
)
_MODE_ALIASES = {"wenyan": "wenyan-full"}


def normalize_mode(value: object) -> str | None:
    """Return a canonical persistent mode, or ``None`` for invalid modes."""

    if not isinstance(value, str):
        return None
    mode = value.strip().lower()
    mode = _MODE_ALIASES.get(mode, mode)
    return mode if mode in CANONICAL_MODES else None


def _read_default_mode(path: Path) -> str | None:
    descriptor: int | None = None
    try:
        before = path.lstat()
        if not stat.S_ISREG(before.st_mode):
            return None
        flags = os.O_RDONLY | getattr(os, "O_CLOEXEC", 0) | getattr(os, "O_NOFOLLOW", 0)
        descriptor = os.open(path, flags)
        after = os.fstat(descriptor)
        if not stat.S_ISREG(after.st_mode) or (before.st_dev, before.st_ino) != (
            after.st_dev,
            after.st_ino,
        ):
            return None
        with os.fdopen(descriptor, "r", encoding="utf-8") as handle:
            descriptor = None
            data = json.load(handle)
    except (OSError, UnicodeError, json.JSONDecodeError):
        return None
    finally:
        if descriptor is not None:
            os.close(descriptor)
    if not isinstance(data, dict):
        return None
    return normalize_mode(data.get("defaultMode"))


def find_repo_config(start: str | os.PathLike[str], max_levels: int = 64) -> Path | None:
    """Find the nearest valid repository configuration file."""

    current = Path(start)
    if not current.is_dir():
        current = current.parent

    try:
        levels = max(0, min(int(max_levels), 64))
    except (TypeError, ValueError, OverflowError):
        return None

    for _ in range(levels):
        for candidate in (current / ".caveman" / "config.json", current / ".caveman.json"):
            if _read_default_mode(candidate) is not None:
                return candidate
        parent = current.parent
        if parent == current:
            break
        current = parent
    return None


def user_config_path(
    env: Mapping[str, str] | None = None,
    platform: str | None = None,
    home: str | os.PathLike[str] | None = None,
) -> Path:
    """Return the platform-appropriate per-user configuration path."""

    environment = os.environ if env is None else env
    xdg = environment.get("XDG_CONFIG_HOME")
    if xdg:
        return Path(xdg) / "caveman" / "config.json"

    current_platform = sys.platform if platform is None else platform
    if current_platform.startswith("win"):
        appdata = environment.get("APPDATA")
        if appdata:
            return Path(appdata) / "caveman" / "config.json"

    if home is None:
        configured_home = environment.get("HOME")
        home_path = Path(configured_home) if configured_home else Path.home()
    else:
        home_path = Path(home)
    return home_path / ".config" / "caveman" / "config.json"


def resolve_default_mode(
    cwd: str | os.PathLike[str], env: Mapping[str, str] | None = None
) -> str:
    """Resolve a default mode using environment, repository, then user config."""

    environment = os.environ if env is None else env
    configured = normalize_mode(environment.get("CAVEMAN_DEFAULT_MODE"))
    if configured is not None:
        return configured

    repo_config = find_repo_config(cwd)
    if repo_config is not None:
        repo_mode = _read_default_mode(repo_config)
        if repo_mode is not None:
            return repo_mode

    user_mode = _read_default_mode(user_config_path(environment))
    return user_mode if user_mode is not None else "full"
