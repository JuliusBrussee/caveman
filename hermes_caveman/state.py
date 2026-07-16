"""Durable state storage for the Hermes Caveman plugin."""

from __future__ import annotations

import json
import os
from pathlib import Path
import secrets
import stat
import threading
from typing import Any, Callable

try:  # POSIX Hermes hosts; thread lock below remains the in-process guard.
    import fcntl
except ImportError:  # pragma: no cover - Windows fallback uses the thread guard.
    fcntl = None

from .config import normalize_mode


class StateSecurityError(OSError):
    """Raised when a state path would cross a symbolic link."""


_STATE_DIRECTORY = "caveman"
_MODE_FILE = "mode.json"
_HISTORY_FILE = "history.jsonl"
_DIRECTORY_FLAGS = (
    os.O_RDONLY
    | getattr(os, "O_DIRECTORY", 0)
    | getattr(os, "O_CLOEXEC", 0)
    | getattr(os, "O_NOFOLLOW", 0)
)
_FILE_NOFOLLOW = getattr(os, "O_NOFOLLOW", 0) | getattr(os, "O_CLOEXEC", 0)
_HISTORY_LOCK = threading.RLock()


def state_root() -> Path:
    """Return Caveman's namespace under the active Hermes home."""

    configured = os.environ.get("HERMES_HOME")
    hermes_home = Path(configured) if configured else Path.home() / ".hermes"
    return hermes_home / _STATE_DIRECTORY


def mode_path() -> Path:
    return state_root() / _MODE_FILE


def history_path() -> Path:
    return state_root() / _HISTORY_FILE


def _unsafe_path(path: object, reason: str = "unsafe state path") -> StateSecurityError:
    return StateSecurityError(f"{reason}: {path}")


def _open_state_directory(create: bool) -> int | None:
    """Open the state directory without following either managed path component."""

    root = state_root()
    hermes_home = root.parent
    created_home = False
    if create:
        try:
            hermes_home.mkdir(parents=True, mode=0o700)
            created_home = True
        except FileExistsError:
            pass
    try:
        home_before = hermes_home.lstat()
    except FileNotFoundError:
        if not create:
            return None
        raise
    if stat.S_ISLNK(home_before.st_mode) or not stat.S_ISDIR(home_before.st_mode):
        raise _unsafe_path(hermes_home)

    try:
        home_descriptor = os.open(hermes_home, _DIRECTORY_FLAGS)
    except OSError as error:
        if hermes_home.is_symlink():
            raise _unsafe_path(hermes_home) from error
        raise
    try:
        home_after = os.fstat(home_descriptor)
        if not stat.S_ISDIR(home_after.st_mode) or (
            home_before.st_dev,
            home_before.st_ino,
        ) != (home_after.st_dev, home_after.st_ino):
            raise _unsafe_path(hermes_home, "state parent changed during access")
        if created_home:
            os.fchmod(home_descriptor, 0o700)

        if create:
            try:
                os.mkdir(_STATE_DIRECTORY, mode=0o700, dir_fd=home_descriptor)
            except FileExistsError:
                pass
        try:
            root_before = os.stat(
                _STATE_DIRECTORY, dir_fd=home_descriptor, follow_symlinks=False
            )
        except FileNotFoundError:
            if not create:
                return None
            raise
        if stat.S_ISLNK(root_before.st_mode) or not stat.S_ISDIR(root_before.st_mode):
            raise _unsafe_path(root)

        try:
            root_descriptor = os.open(
                _STATE_DIRECTORY, _DIRECTORY_FLAGS, dir_fd=home_descriptor
            )
        except OSError as error:
            raise _unsafe_path(root) from error
        root_after = os.fstat(root_descriptor)
        if not stat.S_ISDIR(root_after.st_mode) or (
            root_before.st_dev,
            root_before.st_ino,
        ) != (root_after.st_dev, root_after.st_ino):
            os.close(root_descriptor)
            raise _unsafe_path(root, "state directory changed during access")
        os.fchmod(root_descriptor, 0o700)
        return root_descriptor
    finally:
        os.close(home_descriptor)


def _entry_stat(directory_descriptor: int, name: str) -> os.stat_result | None:
    try:
        return os.stat(name, dir_fd=directory_descriptor, follow_symlinks=False)
    except FileNotFoundError:
        return None


def _require_regular_entry(
    directory_descriptor: int, name: str, *, allow_missing: bool
) -> os.stat_result | None:
    entry = _entry_stat(directory_descriptor, name)
    if entry is None:
        if allow_missing:
            return None
        raise FileNotFoundError(name)
    if stat.S_ISLNK(entry.st_mode) or not stat.S_ISREG(entry.st_mode):
        raise _unsafe_path(state_root() / name, "non-regular state file rejected")
    return entry


def _create_mode_temporary(directory_descriptor: int) -> tuple[int, str]:
    for _ in range(128):
        name = (
            f".mode-{os.getpid()}-{threading.get_ident()}-"
            f"{secrets.token_hex(12)}.tmp"
        )
        try:
            descriptor = os.open(
                name,
                os.O_WRONLY | os.O_CREAT | os.O_EXCL | _FILE_NOFOLLOW,
                0o600,
                dir_fd=directory_descriptor,
            )
        except FileExistsError:
            continue
        os.fchmod(descriptor, 0o600)
        return descriptor, name
    raise FileExistsError("could not allocate a unique mode-state temporary file")


def _write_all(descriptor: int, payload: bytes) -> None:
    view = memoryview(payload)
    while view:
        written = os.write(descriptor, view)
        if written <= 0:
            raise OSError("short write while persisting Caveman state")
        view = view[written:]


def write_mode(mode: str) -> None:
    """Atomically persist the active canonical mode."""

    canonical = normalize_mode(mode)
    if canonical is None:
        raise ValueError(f"invalid persistent Caveman mode: {mode!r}")
    payload = (json.dumps({"mode": canonical}, separators=(",", ":")) + "\n").encode(
        "utf-8"
    )

    root_descriptor = _open_state_directory(create=True)
    if root_descriptor is None:  # pragma: no cover - create=True guarantees a descriptor
        raise OSError("could not create Caveman state directory")
    temporary_descriptor = -1
    temporary_name: str | None = None
    try:
        _require_regular_entry(root_descriptor, _MODE_FILE, allow_missing=True)
        temporary_descriptor, temporary_name = _create_mode_temporary(root_descriptor)
        _write_all(temporary_descriptor, payload)
        os.fsync(temporary_descriptor)
        os.close(temporary_descriptor)
        temporary_descriptor = -1

        # Check again immediately before replacement. Replacing a raced symlink is
        # itself safe, but an already-present symlink is a configuration error.
        _require_regular_entry(root_descriptor, _MODE_FILE, allow_missing=True)
        os.replace(
            temporary_name,
            _MODE_FILE,
            src_dir_fd=root_descriptor,
            dst_dir_fd=root_descriptor,
        )
        temporary_name = None
        os.fsync(root_descriptor)
    finally:
        if temporary_descriptor >= 0:
            os.close(temporary_descriptor)
        if temporary_name is not None:
            try:
                os.unlink(temporary_name, dir_fd=root_descriptor)
            except FileNotFoundError:
                pass
        os.close(root_descriptor)


def _open_regular_for_read(directory_descriptor: int, name: str) -> int | None:
    entry = _require_regular_entry(directory_descriptor, name, allow_missing=True)
    if entry is None:
        return None
    try:
        descriptor = os.open(name, os.O_RDONLY | _FILE_NOFOLLOW, dir_fd=directory_descriptor)
    except FileNotFoundError:
        return None
    except OSError as error:
        raise _unsafe_path(state_root() / name) from error
    opened = os.fstat(descriptor)
    if not stat.S_ISREG(opened.st_mode):
        os.close(descriptor)
        raise _unsafe_path(state_root() / name, "non-regular state file rejected")
    current = _entry_stat(directory_descriptor, name)
    if current is not None and stat.S_ISLNK(current.st_mode):
        os.close(descriptor)
        raise _unsafe_path(state_root() / name)
    return descriptor


def read_mode() -> str | None:
    """Read the active mode, returning ``None`` for absent or invalid state."""

    root_descriptor = _open_state_directory(create=False)
    if root_descriptor is None:
        return None
    try:
        descriptor = _open_regular_for_read(root_descriptor, _MODE_FILE)
        if descriptor is None:
            return None
        try:
            with os.fdopen(descriptor, "r", encoding="utf-8") as handle:
                descriptor = -1
                data: Any = json.load(handle)
        except (OSError, UnicodeError, json.JSONDecodeError):
            return None
        finally:
            if descriptor >= 0:
                os.close(descriptor)
    finally:
        os.close(root_descriptor)
    if not isinstance(data, dict):
        return None
    return normalize_mode(data.get("mode"))


def _lock_file(descriptor: int, *, exclusive: bool) -> None:
    if fcntl is not None:
        fcntl.flock(descriptor, fcntl.LOCK_EX if exclusive else fcntl.LOCK_SH)


def _unlock_file(descriptor: int) -> None:
    if fcntl is not None:
        fcntl.flock(descriptor, fcntl.LOCK_UN)


def _open_history_guard(root_descriptor: int) -> int:
    name = ".history.lock"
    before = _require_regular_entry(root_descriptor, name, allow_missing=True)
    descriptor = os.open(
        name,
        os.O_RDWR | os.O_CREAT | _FILE_NOFOLLOW,
        0o600,
        dir_fd=root_descriptor,
    )
    opened = os.fstat(descriptor)
    if not stat.S_ISREG(opened.st_mode):
        os.close(descriptor)
        raise _unsafe_path(state_root() / name, "non-regular history lock rejected")
    if before is not None and (before.st_dev, before.st_ino) != (
        opened.st_dev,
        opened.st_ino,
    ):
        os.close(descriptor)
        raise _unsafe_path(state_root() / name, "history lock changed during access")
    os.fchmod(descriptor, 0o600)
    _lock_file(descriptor, exclusive=True)
    return descriptor


def _read_history_unlocked(root_descriptor: int) -> list[dict[str, Any]]:
    descriptor = _open_regular_for_read(root_descriptor, _HISTORY_FILE)
    if descriptor is None:
        return []
    try:
        with os.fdopen(descriptor, "r", encoding="utf-8") as handle:
            descriptor = -1
            rows = []
            for line in handle:
                try:
                    value = json.loads(line)
                except (json.JSONDecodeError, UnicodeError):
                    continue
                if isinstance(value, dict):
                    rows.append(value)
            return rows
    finally:
        if descriptor >= 0:
            os.close(descriptor)


def _replace_history_unlocked(
    root_descriptor: int, rows: list[dict[str, Any]]
) -> None:
    payload = b"".join(
        (json.dumps(row, ensure_ascii=False, separators=(",", ":")) + "\n").encode(
            "utf-8"
        )
        for row in rows
    )
    if len(payload) > 16 * 1_048_576:
        raise ValueError("compacted history exceeds 16 MiB")
    _require_regular_entry(root_descriptor, _HISTORY_FILE, allow_missing=True)
    temporary = f".history-{os.getpid()}-{threading.get_ident()}-{secrets.token_hex(8)}.tmp"
    descriptor = -1
    try:
        descriptor = os.open(
            temporary,
            os.O_WRONLY | os.O_CREAT | os.O_EXCL | _FILE_NOFOLLOW,
            0o600,
            dir_fd=root_descriptor,
        )
        os.fchmod(descriptor, 0o600)
        _write_all(descriptor, payload)
        os.fsync(descriptor)
        os.close(descriptor)
        descriptor = -1
        _require_regular_entry(root_descriptor, _HISTORY_FILE, allow_missing=True)
        os.replace(
            temporary,
            _HISTORY_FILE,
            src_dir_fd=root_descriptor,
            dst_dir_fd=root_descriptor,
        )
        temporary = ""
        os.fsync(root_descriptor)
    finally:
        if descriptor >= 0:
            os.close(descriptor)
        if temporary:
            try:
                os.unlink(temporary, dir_fd=root_descriptor)
            except FileNotFoundError:
                pass


def append_history(row: dict[str, Any]) -> None:
    """Append one durable JSONL attribution row without following links."""

    if not isinstance(row, dict):
        raise TypeError("history row must be a mapping")
    payload = (json.dumps(row, ensure_ascii=False, separators=(",", ":")) + "\n").encode(
        "utf-8"
    )
    if len(payload) > 1_048_576:
        raise ValueError("history row exceeds 1 MiB")

    with _HISTORY_LOCK:
        root_descriptor = _open_state_directory(create=True)
        if root_descriptor is None:  # pragma: no cover
            raise OSError("could not create Caveman state directory")
        guard = descriptor = -1
        try:
            guard = _open_history_guard(root_descriptor)
            before = _require_regular_entry(
                root_descriptor, _HISTORY_FILE, allow_missing=True
            )
            descriptor = os.open(
                _HISTORY_FILE,
                os.O_WRONLY | os.O_CREAT | os.O_APPEND | _FILE_NOFOLLOW,
                0o600,
                dir_fd=root_descriptor,
            )
            opened = os.fstat(descriptor)
            if not stat.S_ISREG(opened.st_mode):
                raise _unsafe_path(history_path(), "non-regular history rejected")
            if before is not None and (before.st_dev, before.st_ino) != (
                opened.st_dev,
                opened.st_ino,
            ):
                raise _unsafe_path(history_path(), "history changed during access")
            os.fchmod(descriptor, 0o600)
            _write_all(descriptor, payload)
            os.fsync(descriptor)
        finally:
            if descriptor >= 0:
                os.close(descriptor)
            if guard >= 0:
                _unlock_file(guard)
                os.close(guard)
            os.close(root_descriptor)


def read_history() -> list[dict[str, Any]]:
    """Read complete JSONL rows; ignore a truncated final or malformed row."""

    with _HISTORY_LOCK:
        root_descriptor = _open_state_directory(create=False)
        if root_descriptor is None:
            return []
        try:
            return _read_history_unlocked(root_descriptor)
        finally:
            os.close(root_descriptor)


def compact_history_rows(
    max_rows: int,
    compactor: Callable[[list[dict[str, Any]]], list[dict[str, Any]]],
) -> bool:
    """Atomically compact history under the same cross-process append lock."""

    if max_rows < 1:
        raise ValueError("max_rows must be positive")
    with _HISTORY_LOCK:
        root_descriptor = _open_state_directory(create=False)
        if root_descriptor is None:
            return False
        guard = -1
        try:
            guard = _open_history_guard(root_descriptor)
            rows = _read_history_unlocked(root_descriptor)
            if len(rows) <= max_rows:
                return False
            compacted = compactor(rows)
            if not isinstance(compacted, list) or len(compacted) > max_rows or not all(
                isinstance(row, dict) for row in compacted
            ):
                raise ValueError("history compactor returned invalid rows")
            _replace_history_unlocked(root_descriptor, compacted)
            return True
        finally:
            if guard >= 0:
                _unlock_file(guard)
                os.close(guard)
            os.close(root_descriptor)


def clear_ephemeral_state() -> None:
    """Remove mode state only; lifetime history is deliberately retained."""

    root_descriptor = _open_state_directory(create=False)
    if root_descriptor is None:
        return
    try:
        entry = _require_regular_entry(root_descriptor, _MODE_FILE, allow_missing=True)
        if entry is not None:
            os.unlink(_MODE_FILE, dir_fd=root_descriptor)
            os.fsync(root_descriptor)
    finally:
        os.close(root_descriptor)
