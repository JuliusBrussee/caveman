"""Installer-side Hermes config inspection and anchored state cleanup."""
from __future__ import annotations

import argparse
import json
import os
from pathlib import Path
import stat
import sys
from typing import Any

_NOFOLLOW = getattr(os, "O_NOFOLLOW", 0)
_DIRECTORY = getattr(os, "O_DIRECTORY", 0)


def _plugin_override(snapshot_home: Path) -> dict[str, Any]:
    if not snapshot_home.is_absolute():
        raise ValueError("snapshot home must be absolute")
    info = snapshot_home.lstat()
    if not stat.S_ISDIR(info.st_mode) or stat.S_ISLNK(info.st_mode) or info.st_mode & 0o002:
        raise ValueError("snapshot home must be a private directory")
    snapshot_config = snapshot_home / "config.yaml"
    try:
        config_info = snapshot_config.lstat()
    except FileNotFoundError:
        pass
    else:
        if not stat.S_ISREG(config_info.st_mode) or stat.S_ISLNK(config_info.st_mode) or config_info.st_mode & 0o022:
            raise ValueError("snapshot config must be a private regular file")

    snapshot_env = {
        key: os.environ.get(key)
        for key in ("HERMES_HOME", "HERMES_PROFILE", "HERMES_CONFIG")
    }
    try:
        os.environ["HERMES_HOME"] = str(snapshot_home)
        os.environ.pop("HERMES_PROFILE", None)
        os.environ.pop("HERMES_CONFIG", None)
        from hermes_cli.config import read_raw_config  # type: ignore[import-not-found]

        snapshot = read_raw_config()
    finally:
        for key, value in snapshot_env.items():
            if value is None:
                os.environ.pop(key, None)
            else:
                os.environ[key] = value

    plugins = snapshot.get("plugins") if isinstance(snapshot, dict) else None
    entries = plugins.get("entries") if isinstance(plugins, dict) else None
    entry = entries.get("caveman") if isinstance(entries, dict) else None
    present = isinstance(entry, dict) and "allow_tool_override" in entry
    return {
        "present": present,
        "value": entry["allow_tool_override"] if present and isinstance(entry, dict) else None,
    }


def _same_inode(left: os.stat_result, right: os.stat_result) -> bool:
    return (left.st_dev, left.st_ino, stat.S_IFMT(left.st_mode)) == (
        right.st_dev,
        right.st_ino,
        stat.S_IFMT(right.st_mode),
    )


def _unlink_regular(directory_fd: int, name: str) -> bool:
    try:
        descriptor = os.open(name, os.O_RDONLY | _NOFOLLOW, dir_fd=directory_fd)
    except FileNotFoundError:
        return False
    try:
        pinned = os.fstat(descriptor)
        current = os.stat(name, dir_fd=directory_fd, follow_symlinks=False)
        if not stat.S_ISREG(pinned.st_mode) or not _same_inode(pinned, current):
            raise OSError(f"non-regular or changed state file refused: {name}")
        os.unlink(name, dir_fd=directory_fd)
        return True
    finally:
        os.close(descriptor)


def _remove_tree(directory_fd: int, name: str) -> None:
    child_fd = os.open(name, os.O_RDONLY | _DIRECTORY | _NOFOLLOW, dir_fd=directory_fd)
    pinned = os.fstat(child_fd)
    try:
        for child in os.listdir(child_fd):
            info = os.stat(child, dir_fd=child_fd, follow_symlinks=False)
            if stat.S_ISDIR(info.st_mode):
                _remove_tree(child_fd, child)
            elif stat.S_ISREG(info.st_mode):
                _unlink_regular(child_fd, child)
            else:
                raise OSError(f"symlink or special state entry refused: {name}/{child}")
        current = os.stat(name, dir_fd=directory_fd, follow_symlinks=False)
        if not _same_inode(pinned, current):
            raise OSError(f"state directory changed during cleanup: {name}")
    finally:
        os.close(child_fd)
    os.rmdir(name, dir_fd=directory_fd)


def _cleanup_state(home: Path, purge_history: bool) -> dict[str, Any]:
    if not home.is_absolute():
        raise ValueError("Hermes home must be absolute")
    home_fd = os.open(home, os.O_RDONLY | _DIRECTORY | _NOFOLLOW)
    try:
        try:
            state_fd = os.open("caveman", os.O_RDONLY | _DIRECTORY | _NOFOLLOW, dir_fd=home_fd)
        except FileNotFoundError:
            return {"history": "absent"}
        try:
            for name in ("mode.json", ".history.lock"):
                _unlink_regular(state_fd, name)
            for name in ("sessions", "revisions", "locks"):
                try:
                    info = os.stat(name, dir_fd=state_fd, follow_symlinks=False)
                except FileNotFoundError:
                    continue
                if not stat.S_ISDIR(info.st_mode):
                    raise OSError(f"non-directory state path refused: {name}")
                _remove_tree(state_fd, name)

            try:
                history_info = os.stat("history.jsonl", dir_fd=state_fd, follow_symlinks=False)
            except FileNotFoundError:
                history = "absent"
            else:
                if not stat.S_ISREG(history_info.st_mode):
                    raise OSError("non-regular history path refused")
                if purge_history:
                    _unlink_regular(state_fd, "history.jsonl")
                    history = "purged"
                else:
                    history = "preserved"
            os.fsync(state_fd)
            return {"history": history}
        finally:
            os.close(state_fd)
    finally:
        os.close(home_fd)


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser()
    subcommands = parser.add_subparsers(dest="operation", required=True)
    inspect = subcommands.add_parser("inspect-plugin-override")
    inspect.add_argument("--snapshot-home", required=True)
    cleanup = subcommands.add_parser("cleanup-state")
    cleanup.add_argument("--hermes-home", required=True)
    cleanup.add_argument("--purge-history", action="store_true")
    args = parser.parse_args(argv)
    try:
        if args.operation == "inspect-plugin-override":
            result = _plugin_override(Path(args.snapshot_home))
        else:
            result = _cleanup_state(Path(args.hermes_home), bool(args.purge_history))
        print(json.dumps(result, separators=(",", ":")))
        return 0
    except (ImportError, json.JSONDecodeError, OSError, TypeError, ValueError) as exc:
        print(f"caveman lifecycle helper failed: {exc}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
