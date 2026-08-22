"""Hermes SessionDB-backed Caveman usage and attribution statistics."""
from __future__ import annotations

from contextlib import contextmanager
import hashlib
import json
import math
import os
from pathlib import Path
import secrets
import stat
import threading
from typing import Any, Iterator

from . import state
from .config import normalize_mode

_SESSION_LOCK = threading.RLock()
_SESSIONS_DIR = "sessions"
_ACTIVE_MODES = {"lite", "full", "ultra", "wenyan-lite", "wenyan-full", "wenyan-ultra"}
_TOKEN_FIELDS = (
    "input_tokens",
    "output_tokens",
    "cache_read_tokens",
    "cache_write_tokens",
    "reasoning_tokens",
)


def _session_key(session_id: str) -> str:
    return hashlib.sha256(session_id.encode("utf-8")).hexdigest()


def _empty_attribution(last_output_tokens: int = 0) -> dict[str, Any]:
    return {
        "last_output_tokens": max(0, int(last_output_tokens or 0)),
        "pending_mode": None,
        "by_mode": {},
        "unknown_output_tokens": 0,
    }


def _open_sessions_dir(create: bool) -> tuple[int, int] | None:
    root_fd = state._open_state_directory(create=create)
    if root_fd is None:
        return None
    try:
        if create:
            try:
                os.mkdir(_SESSIONS_DIR, mode=0o700, dir_fd=root_fd)
            except FileExistsError:
                pass
        try:
            before = os.stat(_SESSIONS_DIR, dir_fd=root_fd, follow_symlinks=False)
        except FileNotFoundError:
            os.close(root_fd)
            return None
        if stat.S_ISLNK(before.st_mode) or not stat.S_ISDIR(before.st_mode):
            raise state.StateSecurityError("unsafe Caveman sessions directory")
        flags = os.O_RDONLY | getattr(os, "O_DIRECTORY", 0) | getattr(os, "O_CLOEXEC", 0) | getattr(os, "O_NOFOLLOW", 0)
        sessions_fd = os.open(_SESSIONS_DIR, flags, dir_fd=root_fd)
        after = os.fstat(sessions_fd)
        if (before.st_dev, before.st_ino) != (after.st_dev, after.st_ino):
            os.close(sessions_fd)
            raise state.StateSecurityError("Caveman sessions directory changed during access")
        os.fchmod(sessions_fd, 0o700)
        return root_fd, sessions_fd
    except Exception:
        try:
            os.close(root_fd)
        except OSError:
            pass
        raise


@contextmanager
def _locked_session(session_id: str, *, create: bool) -> Iterator[tuple[int, str]]:
    key = _session_key(session_id)
    with _SESSION_LOCK:
        opened = _open_sessions_dir(create=create)
        if opened is None:
            yield (-1, key)
            return
        root_fd, sessions_fd = opened
        lock_fd = -1
        try:
            lock_name = f"{key}.lock"
            lock_fd = os.open(
                lock_name,
                os.O_RDWR | os.O_CREAT | getattr(os, "O_NOFOLLOW", 0) | getattr(os, "O_CLOEXEC", 0),
                0o600,
                dir_fd=sessions_fd,
            )
            opened_lock = os.fstat(lock_fd)
            if not stat.S_ISREG(opened_lock.st_mode):
                raise state.StateSecurityError("non-regular Caveman session lock")
            os.fchmod(lock_fd, 0o600)
            state._lock_file(lock_fd, exclusive=True)
            try:
                yield sessions_fd, key
            finally:
                state._unlock_file(lock_fd)
        finally:
            if lock_fd >= 0:
                os.close(lock_fd)
            os.close(sessions_fd)
            os.close(root_fd)


def _read_locked(directory_fd: int, key: str) -> dict[str, Any] | None:
    if directory_fd < 0:
        return None
    name = f"{key}.json"
    try:
        before = os.stat(name, dir_fd=directory_fd, follow_symlinks=False)
    except FileNotFoundError:
        return None
    if stat.S_ISLNK(before.st_mode) or not stat.S_ISREG(before.st_mode):
        raise state.StateSecurityError("non-regular Caveman attribution file")
    fd = os.open(name, os.O_RDONLY | getattr(os, "O_NOFOLLOW", 0), dir_fd=directory_fd)
    try:
        after = os.fstat(fd)
        if (before.st_dev, before.st_ino) != (after.st_dev, after.st_ino):
            raise state.StateSecurityError("Caveman attribution changed during read")
        with os.fdopen(fd, "r", encoding="utf-8") as handle:
            fd = -1
            value = json.load(handle)
    except (json.JSONDecodeError, UnicodeError, OSError):
        return None
    finally:
        if fd >= 0:
            os.close(fd)
    return value if isinstance(value, dict) else None


def _write_locked(directory_fd: int, key: str, value: dict[str, Any]) -> None:
    name = f"{key}.json"
    existing = state._entry_stat(directory_fd, name)
    if existing is not None and (stat.S_ISLNK(existing.st_mode) or not stat.S_ISREG(existing.st_mode)):
        raise state.StateSecurityError("non-regular Caveman attribution file")
    payload = (json.dumps(value, ensure_ascii=False, separators=(",", ":")) + "\n").encode("utf-8")
    temporary = f".{key}.{os.getpid()}.{threading.get_ident()}.{secrets.token_hex(8)}.tmp"
    fd = -1
    try:
        fd = os.open(
            temporary,
            os.O_WRONLY | os.O_CREAT | os.O_EXCL | getattr(os, "O_NOFOLLOW", 0),
            0o600,
            dir_fd=directory_fd,
        )
        os.fchmod(fd, 0o600)
        state._write_all(fd, payload)
        os.fsync(fd)
        os.close(fd)
        fd = -1
        current = state._entry_stat(directory_fd, name)
        if current is not None and (stat.S_ISLNK(current.st_mode) or not stat.S_ISREG(current.st_mode)):
            raise state.StateSecurityError("non-regular Caveman attribution file")
        os.replace(temporary, name, src_dir_fd=directory_fd, dst_dir_fd=directory_fd)
        temporary = ""
        os.fsync(directory_fd)
    finally:
        if fd >= 0:
            os.close(fd)
        if temporary:
            try:
                os.unlink(temporary, dir_fd=directory_fd)
            except FileNotFoundError:
                pass


@contextmanager
def _database(db: Any = None):
    if db is not None:
        yield db
        return
    from hermes_state import SessionDB  # type: ignore[import-not-found]

    hermes_home = Path(os.environ.get("HERMES_HOME", Path.home() / ".hermes"))
    owned = SessionDB(db_path=hermes_home / "state.db")
    try:
        yield owned
    finally:
        owned.close()


def _session_row(session_id: str, db: Any = None) -> dict[str, Any] | None:
    with _database(db) as database:
        row = database.get_session(session_id)
    return dict(row) if row else None


def _token(value: Any) -> int:
    try:
        return max(0, int(value or 0))
    except (TypeError, ValueError, OverflowError):
        return 0


def start_session(session_id: str, db: Any = None, **kwargs: Any) -> None:
    """Set a cumulative baseline so pre-plugin output is never guessed."""

    if not session_id:
        return
    row = _session_row(session_id, db)
    baseline = _token(row.get("output_tokens")) if row else 0
    with _locked_session(session_id, create=True) as (directory_fd, key):
        current = _read_locked(directory_fd, key)
        if current is None:
            _write_locked(directory_fd, key, _empty_attribution(baseline))


def mark_turn_mode(session_id: str, mode: str | None, db: Any = None, **kwargs: Any) -> None:
    if not session_id:
        return
    canonical = normalize_mode(mode) if mode is not None else None
    if canonical not in _ACTIVE_MODES:
        canonical = None
    with _locked_session(session_id, create=True) as (directory_fd, key):
        current = _read_locked(directory_fd, key)
        if current is None:
            row = _session_row(session_id, db)
            baseline = _token(row.get("output_tokens")) if row else 0
            current = _empty_attribution(baseline)
        current["pending_mode"] = canonical
        _write_locked(directory_fd, key, current)


def post_llm_call(session_id: str = "", db: Any = None, **kwargs: Any):
    """Reconcile one newly persisted cumulative output-token delta."""

    if not session_id:
        return None
    row = _session_row(str(session_id), db)
    if not row:
        return None
    cumulative = _token(row.get("output_tokens"))
    event = None
    with _locked_session(str(session_id), create=True) as (directory_fd, key):
        current = _read_locked(directory_fd, key) or _empty_attribution()
        previous = _token(current.get("last_output_tokens"))
        pending = current.get("pending_mode")
        if cumulative < previous:
            current["last_output_tokens"] = cumulative
            current["pending_mode"] = None
            _write_locked(directory_fd, key, current)
            return None
        delta = cumulative - previous
        current["last_output_tokens"] = cumulative
        current["pending_mode"] = None
        if delta > 0:
            raw_by_mode = current.get("by_mode")
            by_mode: dict[str, Any] = dict(raw_by_mode) if isinstance(raw_by_mode, dict) else {}
            if pending in _ACTIVE_MODES:
                attributed_mode = str(pending)
                by_mode[attributed_mode] = _token(by_mode.get(attributed_mode)) + delta
                current["by_mode"] = by_mode
                event = {"session": key, "mode": attributed_mode, "output_tokens": delta}
            else:
                current["unknown_output_tokens"] = _token(current.get("unknown_output_tokens")) + delta
                event = {"session": key, "mode": None, "output_tokens": delta}
        _write_locked(directory_fd, key, current)
    if event is not None:
        state.append_history(event)
        try:
            if state.history_path().stat().st_size > 4 * 1_048_576:
                compact_history()
        except (OSError, state.StateSecurityError):
            pass
    return None


def _read_attribution(session_id: str) -> dict[str, Any]:
    with _locked_session(session_id, create=False) as (directory_fd, key):
        if directory_fd < 0:
            return _empty_attribution()
        return _read_locked(directory_fd, key) or _empty_attribution()


def estimate_full_savings(full_output_tokens: int) -> tuple[int, int]:
    eligible = _token(full_output_tokens)
    if eligible == 0:
        return 0, 0
    normal = int(math.floor(eligible / 0.35 + 0.5))
    return normal, max(0, normal - eligible)


def _cost(row: dict[str, Any]) -> tuple[float | None, str | None]:
    actual = row.get("actual_cost_usd")
    estimated = row.get("estimated_cost_usd")
    if isinstance(actual, (int, float)):
        return float(actual), "actual"
    if isinstance(estimated, (int, float)):
        return float(estimated), "estimated"
    return None, None


def _aggregate_rows(rows: list[dict[str, Any]]) -> dict[str, Any]:
    by_mode: dict[str, int] = {}
    unknown = 0
    for row in rows:
        if row.get("kind") == "summary" and isinstance(row.get("by_mode"), dict):
            for mode, tokens in row["by_mode"].items():
                if mode in _ACTIVE_MODES:
                    by_mode[mode] = by_mode.get(mode, 0) + _token(tokens)
            unknown += _token(row.get("unknown_output_tokens"))
            continue
        tokens = _token(row.get("output_tokens"))
        mode = row.get("mode")
        if mode in _ACTIVE_MODES:
            by_mode[mode] = by_mode.get(mode, 0) + tokens
        else:
            unknown += tokens
    normal, saved = estimate_full_savings(by_mode.get("full", 0))
    return {
        "by_mode": by_mode,
        "unknown_output_tokens": unknown,
        "attributed_output_tokens": sum(by_mode.values()),
        "estimated_normal_output_tokens": normal,
        "estimated_saved_output_tokens": saved,
    }


def _aggregate_history() -> dict[str, Any]:
    return _aggregate_rows(state.read_history())


def compact_history(max_rows: int = 10_000) -> bool:
    """Bound lifetime JSONL while preserving exact aggregate attribution."""

    def compact(rows: list[dict[str, Any]]) -> list[dict[str, Any]]:
        aggregate = _aggregate_rows(rows)
        return [
            {
                "kind": "summary",
                "by_mode": aggregate["by_mode"],
                "unknown_output_tokens": aggregate["unknown_output_tokens"],
            }
        ]

    return state.compact_history_rows(max_rows, compact)


def build_stats(session_id: str, db: Any = None, **kwargs: Any) -> dict[str, Any]:
    row = _session_row(session_id, db) or {}
    cost_usd, cost_kind = _cost(row)
    current: dict[str, Any] = {field: _token(row.get(field)) for field in _TOKEN_FIELDS}
    current.update(
        {
            "model": row.get("model") or "unknown",
            "cost_usd": cost_usd,
            "cost_kind": cost_kind,
            "cost_status": row.get("cost_status"),
            "cost_source": row.get("cost_source"),
        }
    )
    attribution = _read_attribution(session_id)
    by_mode = {
        str(mode): _token(tokens)
        for mode, tokens in (attribution.get("by_mode") or {}).items()
        if mode in _ACTIVE_MODES and _token(tokens) > 0
    }
    unknown = _token(attribution.get("unknown_output_tokens"))
    full = by_mode.get("full", 0)
    normal, saved = estimate_full_savings(full)
    return {
        "current": current,
        "attribution": {"by_mode": by_mode, "unknown_output_tokens": unknown},
        "savings": {
            "eligible_full_output_tokens": full,
            "estimated_normal_output_tokens": normal,
            "estimated_saved_output_tokens": saved,
        },
        "lifetime": _aggregate_history(),
    }


def _number(value: int) -> str:
    return f"{value:,}"


def format_stats(data: dict[str, Any]) -> str:
    current = data["current"]
    attribution = data["attribution"]
    savings = data["savings"]
    lifetime = data["lifetime"]
    lines = [
        "Caveman stats",
        "",
        "Current session",
        f"Model: {current['model']}",
        f"Input tokens: {_number(current['input_tokens'])}",
        f"Output tokens: {_number(current['output_tokens'])}",
        f"Cache read/write: {_number(current['cache_read_tokens'])} / {_number(current['cache_write_tokens'])}",
        f"Reasoning tokens: {_number(current['reasoning_tokens'])}",
    ]
    if current["cost_usd"] is None:
        lines.append("Cost: unavailable")
    elif current["cost_kind"] == "actual":
        lines.append(f"Actual cost: ${current['cost_usd']:.6f}")
    else:
        lines.append(f"Estimated cost (Hermes): ${current['cost_usd']:.6f}")
    lines.extend(
        [
            "",
            "Measured output attribution",
            f"By mode: {json.dumps(attribution['by_mode'], sort_keys=True)}",
            f"Unknown output tokens: {_number(attribution['unknown_output_tokens'])}",
            "",
            "Estimated savings (full mode only)",
            f"Eligible full output tokens: {_number(savings['eligible_full_output_tokens'])}",
            f"Estimated normal output tokens: {_number(savings['estimated_normal_output_tokens'])}",
            f"Estimated output tokens saved: {_number(savings['estimated_saved_output_tokens'])}",
            "Estimate covers output tokens only; input/cache/reasoning unchanged.",
            "",
            "Lifetime",
            f"Attributed output tokens: {_number(lifetime['attributed_output_tokens'])}",
            f"Estimated output tokens saved: {_number(lifetime['estimated_saved_output_tokens'])}",
        ]
    )
    return "\n".join(lines)


def caveman_stats_tool(args: dict[str, Any] | None = None, session_id: str = "", db: Any = None, **kwargs: Any) -> str:
    if not session_id:
        return "caveman_stats requires the Hermes dispatch session_id; no session was guessed."
    return format_stats(build_stats(str(session_id), db=db))


def caveman_stats(args: dict[str, Any] | None = None, **kwargs: Any) -> str:
    return caveman_stats_tool(args, **kwargs)
