"""Provider-agnostic, revision-bound document compression helpers."""
from __future__ import annotations

from collections import Counter
from dataclasses import dataclass
import hashlib
import json
import os
from pathlib import Path
import re
import secrets
import stat
import threading
from typing import Any, Callable

from . import state

MAX_SOURCE_BYTES = 1_048_576
_SUPPORTED_EXTENSIONS = {".md", ".markdown", ".txt", ".rst", ".typ", ".typst", ".tex"}
_SENSITIVE_PARTS = {".ssh", ".gnupg", ".aws", ".kube", ".hermes", "work_found"}
_REVISION_RE = re.compile(r"^[A-Za-z0-9_-]{24,128}$")
_REVISION_LOCK = threading.RLock()
_NOFOLLOW = getattr(os, "O_NOFOLLOW", 0) | getattr(os, "O_CLOEXEC", 0)


@dataclass(frozen=True)
class ValidationResult:
    is_valid: bool
    errors: tuple[str, ...]


def sha256_bytes(value: bytes) -> str:
    return hashlib.sha256(value).hexdigest()


def _absolute(candidate: str | os.PathLike[str]) -> Path:
    return Path(os.path.abspath(os.path.expanduser(os.fspath(candidate))))


def _sensitive(path: Path) -> bool:
    parts = {part.casefold() for part in path.parts}
    name = path.name.casefold()
    if parts & _SENSITIVE_PARTS:
        return True
    return (
        name == ".env"
        or name.startswith(".env.")
        or name in {"id_rsa", "id_dsa", "id_ecdsa", "id_ed25519"}
        or "credential" in name
        or name in {"secrets", "secrets.txt", "secrets.md"}
    )


def _metadata(info: os.stat_result) -> dict[str, int]:
    return {
        "dev": int(info.st_dev),
        "ino": int(info.st_ino),
        "size": int(info.st_size),
        "mtime_ns": int(info.st_mtime_ns),
        "ctime_ns": int(info.st_ctime_ns),
        "mode": int(stat.S_IMODE(info.st_mode)),
    }


def _same_metadata(left: dict[str, Any], right: os.stat_result) -> bool:
    return left == _metadata(right)


def _same_claimed_metadata(left: dict[str, Any], right: os.stat_result) -> bool:
    """Compare stable source identity after rename, which may update ctime."""

    current = _metadata(right)
    return all(left.get(key) == current[key] for key in ("dev", "ino", "size", "mtime_ns", "mode"))


def _open_source_parent(path: Path) -> int:
    if not path.is_absolute() or not path.name:
        raise OSError("source path must be absolute")
    flags = os.O_RDONLY | getattr(os, "O_DIRECTORY", 0) | _NOFOLLOW
    descriptor = os.open(path.anchor, flags)
    try:
        for part in path.parent.parts[1:]:
            before = os.stat(part, dir_fd=descriptor, follow_symlinks=False)
            if stat.S_ISLNK(before.st_mode) or not stat.S_ISDIR(before.st_mode):
                raise OSError("symlinked or non-directory source ancestor refused")
            child = os.open(part, flags, dir_fd=descriptor)
            opened = os.fstat(child)
            if (before.st_dev, before.st_ino) != (opened.st_dev, opened.st_ino):
                os.close(child)
                raise OSError("source ancestor changed during open")
            os.close(descriptor)
            descriptor = child
        return descriptor
    except Exception:
        os.close(descriptor)
        raise


def _source_stat_at(directory_fd: int, name: str) -> os.stat_result:
    info = os.stat(name, dir_fd=directory_fd, follow_symlinks=False)
    if stat.S_ISLNK(info.st_mode) or not stat.S_ISREG(info.st_mode):
        raise OSError("source must be a regular non-symlink file")
    return info


def _source_stat(path: Path) -> os.stat_result:
    directory_fd = _open_source_parent(path)
    try:
        return _source_stat_at(directory_fd, path.name)
    finally:
        os.close(directory_fd)


def _read_from_directory(directory_fd: int, name: str, limit: int | None) -> bytes:
    before = _source_stat_at(directory_fd, name)
    if limit is not None and before.st_size > limit:
        raise OSError("source exceeds size limit")
    descriptor = os.open(name, os.O_RDONLY | _NOFOLLOW, dir_fd=directory_fd)
    try:
        opened = os.fstat(descriptor)
        if not stat.S_ISREG(opened.st_mode) or (before.st_dev, before.st_ino) != (
            opened.st_dev,
            opened.st_ino,
        ):
            raise OSError("source changed during open")
        chunks: list[bytes] = []
        remaining = None if limit is None else limit + 1
        while remaining is None or remaining > 0:
            size = 131_072 if remaining is None else min(131_072, remaining)
            chunk = os.read(descriptor, size)
            if not chunk:
                break
            chunks.append(chunk)
            if remaining is not None:
                remaining -= len(chunk)
        payload = b"".join(chunks)
        after = os.fstat(descriptor)
        if _metadata(opened) != _metadata(after):
            raise OSError("source changed during read")
        if limit is not None and len(payload) > limit:
            raise OSError("source exceeds size limit")
        return payload
    finally:
        os.close(descriptor)


def _read_bytes(candidate: str | os.PathLike[str]) -> bytes:
    path = _absolute(candidate)
    directory_fd = _open_source_parent(path)
    try:
        return _read_from_directory(directory_fd, path.name, MAX_SOURCE_BYTES)
    finally:
        os.close(directory_fd)


def _read_exact(candidate: str | os.PathLike[str]) -> bytes:
    path = _absolute(candidate)
    directory_fd = _open_source_parent(path)
    try:
        return _read_from_directory(directory_fd, path.name, None)
    finally:
        os.close(directory_fd)


def _open_private_subdir(name: str, *, create: bool) -> tuple[int, int] | None:
    root_fd = state._open_state_directory(create=create)
    if root_fd is None:
        return None
    try:
        if create:
            try:
                os.mkdir(name, mode=0o700, dir_fd=root_fd)
            except FileExistsError:
                pass
        try:
            before = os.stat(name, dir_fd=root_fd, follow_symlinks=False)
        except FileNotFoundError:
            os.close(root_fd)
            return None
        if stat.S_ISLNK(before.st_mode) or not stat.S_ISDIR(before.st_mode):
            raise state.StateSecurityError(f"unsafe Caveman {name} directory")
        descriptor = os.open(
            name,
            os.O_RDONLY | getattr(os, "O_DIRECTORY", 0) | _NOFOLLOW,
            dir_fd=root_fd,
        )
        opened = os.fstat(descriptor)
        if (before.st_dev, before.st_ino) != (opened.st_dev, opened.st_ino):
            os.close(descriptor)
            raise state.StateSecurityError(f"Caveman {name} directory changed")
        os.fchmod(descriptor, 0o700)
        return root_fd, descriptor
    except Exception:
        try:
            os.close(root_fd)
        except OSError:
            pass
        raise


def _backup_name(path: Path) -> str:
    return f"{sha256_bytes(os.fsencode(str(path)))[:40]}.bak"


def backup_path_for(candidate: str | os.PathLike[str]) -> Path:
    path = _absolute(candidate)
    return state.state_root() / "backups" / _backup_name(path)


def _revision_path(token: str) -> Path:
    return state.state_root() / "revisions" / f"{token}.json"


def _write_revision(token: str, record: dict[str, Any]) -> None:
    opened = _open_private_subdir("revisions", create=True)
    if opened is None:  # pragma: no cover
        raise OSError("could not create revision directory")
    root_fd, directory_fd = opened
    descriptor = -1
    try:
        descriptor = os.open(
            f"{token}.json",
            os.O_WRONLY | os.O_CREAT | os.O_EXCL | _NOFOLLOW,
            0o600,
            dir_fd=directory_fd,
        )
        payload = (json.dumps(record, separators=(",", ":")) + "\n").encode("utf-8")
        state._write_all(descriptor, payload)
        os.fsync(descriptor)
        os.close(descriptor)
        descriptor = -1
        os.fsync(directory_fd)
    finally:
        if descriptor >= 0:
            os.close(descriptor)
        os.close(directory_fd)
        os.close(root_fd)


def _load_revision(token: str) -> dict[str, Any] | None:
    if not isinstance(token, str) or not _REVISION_RE.fullmatch(token):
        return None
    opened = _open_private_subdir("revisions", create=False)
    if opened is None:
        return None
    root_fd, directory_fd = opened
    descriptor = -1
    try:
        name = f"{token}.json"
        info = state._entry_stat(directory_fd, name)
        if info is None:
            return None
        if stat.S_ISLNK(info.st_mode) or not stat.S_ISREG(info.st_mode):
            raise state.StateSecurityError("unsafe Caveman revision record")
        descriptor = os.open(name, os.O_RDONLY | _NOFOLLOW, dir_fd=directory_fd)
        with os.fdopen(descriptor, "r", encoding="utf-8") as handle:
            descriptor = -1
            value = json.load(handle)
        return value if isinstance(value, dict) else None
    except (json.JSONDecodeError, UnicodeError, OSError):
        return None
    finally:
        if descriptor >= 0:
            os.close(descriptor)
        os.close(directory_fd)
        os.close(root_fd)


def _consume_revision(token: str) -> None:
    opened = _open_private_subdir("revisions", create=False)
    if opened is None:
        return
    root_fd, directory_fd = opened
    try:
        info = state._entry_stat(directory_fd, f"{token}.json")
        if info is not None:
            if stat.S_ISLNK(info.st_mode) or not stat.S_ISREG(info.st_mode):
                raise state.StateSecurityError("unsafe Caveman revision record")
            os.unlink(f"{token}.json", dir_fd=directory_fd)
            os.fsync(directory_fd)
    finally:
        os.close(directory_fd)
        os.close(root_fd)


def _preflight_path(candidate: str | os.PathLike[str]) -> tuple[Path, os.stat_result]:
    path = _absolute(candidate)
    if _sensitive(path):
        raise OSError("sensitive path refused")
    if path.suffix.casefold() not in _SUPPORTED_EXTENSIONS:
        raise OSError("unsupported document extension")
    info = _source_stat(path)
    if info.st_size == 0:
        raise OSError("empty source refused")
    if info.st_size > MAX_SOURCE_BYTES:
        raise OSError("source exceeds size limit")
    backup = backup_path_for(path)
    try:
        backup_info = backup.lstat()
    except FileNotFoundError:
        backup_info = None
    if backup_info is not None:
        raise OSError("backup already exists; refusing to overwrite it")
    return path, info


def prepare_compression(candidate: str | os.PathLike[str]) -> dict[str, Any]:
    """Validate a source and issue an opaque, single-use revision token."""

    try:
        path, before = _preflight_path(candidate)
        payload = _read_bytes(path)
        text = payload.decode("utf-8")
        if not text:
            raise OSError("empty source refused")
        after = _source_stat(path)
        if _metadata(before) != _metadata(after) or before.st_size != len(payload):
            raise OSError("source changed during preflight")
        token = secrets.token_urlsafe(32)
        record = {
            "version": 1,
            "path": str(path),
            "digest": sha256_bytes(payload),
            "metadata": _metadata(after),
            "backup": str(backup_path_for(path)),
        }
        with _REVISION_LOCK:
            _write_revision(token, record)
        return {"ok": True, "path": str(path), "revision": token, "content": text}
    except (OSError, UnicodeError, ValueError, state.StateSecurityError) as exc:
        return {"ok": False, "error": str(exc)}


def _frontmatter(text: str) -> str | None:
    if not text.startswith("---\n"):
        return None
    end = text.find("\n---\n", 4)
    return text[: end + 5] if end >= 0 else "<unterminated>"


def _fenced_blocks(text: str) -> list[str]:
    lines = text.splitlines(keepends=True)
    blocks: list[str] = []
    current: list[str] | None = None
    marker = ""
    for line in lines:
        match = re.match(r"^[ \t]*(`{3,}|~{3,})", line)
        if current is None:
            if match:
                current = [line]
                marker = match.group(1)
        else:
            current.append(line)
            closing = re.match(r"^[ \t]*(`+|~+)[ \t]*(?:\r?\n)?$", line)
            if closing and closing.group(1)[0] == marker[0] and len(closing.group(1)) >= len(marker):
                blocks.append("".join(current))
                current = None
    if current is not None:
        blocks.append("".join(current))
    return blocks


def _inline_code(text: str) -> list[str]:
    without_fences = re.sub(r"(?ms)^[ \t]*(`{3,}|~{3,}).*?^[ \t]*\1[ \t]*$", "", text)
    return re.findall(r"(?<!`)`([^`\n]+)`(?!`)", without_fences)


def _list_shape(text: str) -> list[tuple[int, str]]:
    shape = []
    for line in text.splitlines():
        match = re.match(r"^([ \t]*)([-+*]|\d+[.)])\s+", line)
        if match:
            indent = len(match.group(1).expandtabs(4))
            shape.append((indent, match.group(2)))
    return shape


def _table_shape(text: str) -> list[tuple[int, bool]]:
    result = []
    for line in text.splitlines():
        stripped = line.strip()
        if stripped.startswith("|") and stripped.endswith("|"):
            cells = stripped[1:-1].split("|")
            separator = all(re.fullmatch(r"\s*:?-{3,}:?\s*", cell) for cell in cells)
            result.append((len(cells), separator))
    return result


def _paths(text: str) -> list[str]:
    absolute = re.findall(r"(?<![:\w])/(?:[A-Za-z0-9._~-]+/)*[A-Za-z0-9._~-]+", text)
    relative = re.findall(
        r"(?<![:\w/])(?:(?:\.\.?)/)*(?:[A-Za-z0-9._~-]+/)+[A-Za-z0-9._~-]+",
        text,
    )
    return absolute + relative


def _protected(text: str) -> dict[str, Any]:
    return {
        "frontmatter": _frontmatter(text),
        "headings": re.findall(r"(?m)^[ \t]*#{1,6}[ \t]+[^\n]+$", text),
        "fenced code": _fenced_blocks(text),
        "inline code": Counter(_inline_code(text)),
        "urls": Counter(re.findall(r"https?://[^\s<>()\]}`]+", text)),
        "paths": Counter(_paths(text)),
        "environment": Counter(re.findall(r"\$[A-Z][A-Z0-9_]*|\b[A-Z][A-Z0-9]*_[A-Z0-9_]+\b", text)),
        "dates": Counter(re.findall(r"\b\d{4}-\d{2}-\d{2}\b", text)),
        "versions": Counter(re.findall(r"\b[vV]?\d+\.\d+(?:\.\d+)+(?:[-+][A-Za-z0-9.-]+)?\b", text)),
        "numbers": Counter(re.findall(r"(?<![A-Za-z])[-+]?\d+(?:\.\d+)?(?![A-Za-z])", text)),
        "list structure": _list_shape(text),
        "table structure": _table_shape(text),
    }


def validate_content(original: str, proposed: str) -> ValidationResult:
    if not isinstance(original, str) or not isinstance(proposed, str):
        return ValidationResult(False, ("content must be text",))
    if not proposed or len(proposed.encode("utf-8")) > MAX_SOURCE_BYTES:
        return ValidationResult(False, ("proposed content is empty or too large",))
    left, right = _protected(original), _protected(proposed)
    errors = tuple(f"protected {name} changed" for name in left if left[name] != right[name])
    return ValidationResult(not errors, errors)


def _write_backup(path: Path, payload: bytes) -> None:
    opened = _open_private_subdir("backups", create=True)
    if opened is None:  # pragma: no cover
        raise OSError("could not create backup directory")
    root_fd, directory_fd = opened
    descriptor = -1
    try:
        descriptor = os.open(
            path.name,
            os.O_WRONLY | os.O_CREAT | os.O_EXCL | _NOFOLLOW,
            0o600,
            dir_fd=directory_fd,
        )
        os.fchmod(descriptor, 0o600)
        state._write_all(descriptor, payload)
        os.fsync(descriptor)
        os.close(descriptor)
        descriptor = -1
        os.fsync(directory_fd)
    finally:
        if descriptor >= 0:
            os.close(descriptor)
        os.close(directory_fd)
        os.close(root_fd)


def _before_replace(path: Path) -> None:
    """Test seam immediately before the final revision check."""


def _before_install(path: Path) -> None:
    """Test seam after the source entry is claimed and verified."""


def _atomic_replace_source(
    path: Path,
    payload: bytes,
    mode: int,
    expected_metadata: dict[str, Any],
    expected_digest: str,
) -> None:
    directory_fd = _open_source_parent(path)
    temporary = f".caveman-compress-{os.getpid()}-{threading.get_ident()}-{secrets.token_hex(8)}"
    claimed = f".caveman-original-{os.getpid()}-{threading.get_ident()}-{secrets.token_hex(8)}"
    descriptor = -1
    try:
        descriptor = os.open(
            temporary,
            os.O_WRONLY | os.O_CREAT | os.O_EXCL | _NOFOLLOW,
            mode,
            dir_fd=directory_fd,
        )
        os.fchmod(descriptor, mode)
        state._write_all(descriptor, payload)
        os.fsync(descriptor)
        os.close(descriptor)
        descriptor = -1
        _before_replace(path)
        verification_fd = _open_source_parent(path)
        try:
            pinned_parent = os.fstat(directory_fd)
            current_parent = os.fstat(verification_fd)
            if (pinned_parent.st_dev, pinned_parent.st_ino) != (current_parent.st_dev, current_parent.st_ino):
                raise OSError("source parent changed before replace")
        finally:
            os.close(verification_fd)
        os.rename(path.name, claimed, src_dir_fd=directory_fd, dst_dir_fd=directory_fd)
        try:
            current = _source_stat_at(directory_fd, claimed)
            if not _same_claimed_metadata(expected_metadata, current):
                raise OSError("source changed before replace")
            if sha256_bytes(_read_from_directory(directory_fd, claimed, MAX_SOURCE_BYTES)) != expected_digest:
                raise OSError("source digest changed before replace")
            _before_install(path)
            try:
                os.link(
                    temporary,
                    path.name,
                    src_dir_fd=directory_fd,
                    dst_dir_fd=directory_fd,
                    follow_symlinks=False,
                )
            except FileExistsError as exc:
                raise OSError("source path was recreated before install") from exc
            os.unlink(temporary, dir_fd=directory_fd)
            temporary = ""
            os.unlink(claimed, dir_fd=directory_fd)
            claimed = ""
        except Exception as exc:
            if claimed:
                try:
                    os.link(
                        claimed,
                        path.name,
                        src_dir_fd=directory_fd,
                        dst_dir_fd=directory_fd,
                        follow_symlinks=False,
                    )
                except FileExistsError:
                    recovery = path.parent / claimed
                    raise OSError(f"{exc}; claimed source preserved at {recovery}") from exc
                else:
                    os.unlink(claimed, dir_fd=directory_fd)
                    claimed = ""
            raise
        os.fsync(directory_fd)
    finally:
        if descriptor >= 0:
            os.close(descriptor)
        if temporary:
            try:
                os.unlink(temporary, dir_fd=directory_fd)
            except FileNotFoundError:
                pass
        os.close(directory_fd)


def apply_compression(candidate: str | os.PathLike[str], revision: str, proposed_content: str) -> dict[str, Any]:
    """Validate, back up, recheck the revision, and atomically replace a source."""

    try:
        path = _absolute(candidate)
        if _sensitive(path) or path.suffix.casefold() not in _SUPPORTED_EXTENSIONS:
            raise OSError("path refused")
        if not isinstance(revision, str) or not _REVISION_RE.fullmatch(revision):
            raise OSError("invalid revision")
        if not isinstance(proposed_content, str):
            raise OSError("proposed content must be text")
        with _REVISION_LOCK:
            record = _load_revision(revision)
            if not record or record.get("path") != str(path):
                raise OSError("unknown revision for path")
            original = _read_bytes(path)
            current = _source_stat(path)
            expected_metadata = record.get("metadata")
            if not isinstance(expected_metadata, dict) or not _same_metadata(expected_metadata, current):
                raise OSError("source metadata changed")
            if sha256_bytes(original) != record.get("digest"):
                raise OSError("source digest changed")
            original_text = original.decode("utf-8")
            validation = validate_content(original_text, proposed_content)
            if not validation.is_valid:
                raise OSError("; ".join(validation.errors))
            after_validation = _source_stat(path)
            if not _same_metadata(expected_metadata, after_validation) or sha256_bytes(_read_bytes(path)) != record.get("digest"):
                raise OSError("source changed during validation")
            backup = backup_path_for(path)
            if str(backup) != record.get("backup"):
                raise OSError("revision backup mismatch")
            try:
                backup.lstat()
            except FileNotFoundError:
                pass
            else:
                raise OSError("backup already exists")
            _write_backup(backup, original)
            if sha256_bytes(_read_exact(backup)) != record.get("digest"):
                raise OSError("backup verification failed")
            replacement = proposed_content.encode("utf-8")
            _atomic_replace_source(
                path,
                replacement,
                int(expected_metadata["mode"]),
                expected_metadata,
                str(record.get("digest")),
            )
            if sha256_bytes(_read_exact(path)) != sha256_bytes(replacement):
                raise OSError("source verification failed")
            _consume_revision(revision)
            return {"ok": True, "path": str(path), "backup": str(backup)}
    except (OSError, UnicodeError, ValueError, TypeError, state.StateSecurityError) as exc:
        return {"ok": False, "error": str(exc)}


def caveman_prepare_compression_tool(args: dict[str, Any] | None = None, **kwargs: Any) -> dict[str, Any]:
    values = args if isinstance(args, dict) else {}
    path = values.get("path")
    if not isinstance(path, str) or not path:
        return {"ok": False, "error": "path is required"}
    return prepare_compression(path)


def caveman_apply_compression_tool(args: dict[str, Any] | None = None, **kwargs: Any) -> dict[str, Any]:
    values = args if isinstance(args, dict) else {}
    path = values.get("path")
    revision = values.get("revision")
    proposed = values.get("proposed_content")
    if not isinstance(path, str) or not path:
        return {"ok": False, "error": "path, revision, and proposed_content are required"}
    if not isinstance(revision, str) or not revision:
        return {"ok": False, "error": "path, revision, and proposed_content are required"}
    if not isinstance(proposed, str) or not proposed:
        return {"ok": False, "error": "path, revision, and proposed_content are required"}
    return apply_compression(path, revision, proposed)


def caveman_prepare_compression(args: dict[str, Any] | None = None, **kwargs: Any) -> dict[str, Any]:
    return caveman_prepare_compression_tool(args, **kwargs)


def caveman_apply_compression(args: dict[str, Any] | None = None, **kwargs: Any) -> dict[str, Any]:
    return caveman_apply_compression_tool(args, **kwargs)
