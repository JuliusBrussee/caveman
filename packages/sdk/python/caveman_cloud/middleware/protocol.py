"""Protocol 1.1 client rules, pure and I/O-free (docs/technical/middleware-protocol.md).

Experimental: ``caveman_cloud.middleware`` may change in any minor release.
Every helper here is pinned by packages/sdk/parity/middleware-v1_1.fixtures.json.
"""
import hashlib
import ipaddress
import json
import logging
import os
import re
import threading
from collections import deque
from collections.abc import Mapping, Sequence
from dataclasses import asdict
from typing import Any
from urllib.parse import urlsplit

from .types import (
    BREAKER_DEFAULTS, CLIENT_RECOVERY_ALLOWLIST, DEFAULT_BRANCH_ID, DEFAULT_CACHE_EPOCH, KNOWN_FEATURES, MIDDLEWARE_DEFAULTS,
    REASON_CATALOG, REASON_PATTERN, SCOPE_HASH_HEX_CHARS, SCOPE_HASH_PREFIX, SCOPE_TOKEN_PATTERN, BreakerParams, BudgetItem,
    BudgetResult, CapabilitiesView, EffectiveLimits, FailureInput, FailureOutcome, MiddlewareError, Scope,
)

LOGGER = logging.getLogger("caveman.middleware")
LOOPBACK = frozenset(("localhost", "127.0.0.1", "::1"))
CAPABILITIES_TTL_S = 300
_TOKEN = re.compile(SCOPE_TOKEN_PATTERN)
_REASON = re.compile(REASON_PATTERN)
_PATH_SEGMENT = re.compile(r"[A-Za-z0-9._~-]+")
_MAX_SAFE = 2**53 - 1
_CLEARS = frozenset(("runtime_unavailable", "unknown_capability", "unsupported_version"))


def _positive(value: Any) -> bool:
    return type(value) is int and 0 < value <= _MAX_SAFE


def _safe(value: Any) -> bool:
    return type(value) is int and 0 <= value <= _MAX_SAFE


def _token(value: Any) -> bool:
    return isinstance(value, str) and _TOKEN.fullmatch(value) is not None


def normalize_scope_token(value: Any) -> str | None:
    """§9: a valid token passes unchanged; other strings become h- + 32 hex of sha256; else None."""
    if not isinstance(value, str) or not value:
        return None
    try:
        data = value.encode("utf-8")
    except UnicodeEncodeError:  # unpaired surrogate
        return None
    if _TOKEN.fullmatch(value):
        return value
    return SCOPE_HASH_PREFIX + hashlib.sha256(data).hexdigest()[:SCOPE_HASH_HEX_CHARS]


def normalize_scope(value: Any) -> Scope | None:
    """§9: namespace and session_id required; branch_id/cache_epoch default when absent or None."""
    if isinstance(value, Scope):
        value = asdict(value)
    if not isinstance(value, Mapping):
        return None
    out = {}
    for key, default in (("namespace", None), ("session_id", None), ("branch_id", DEFAULT_BRANCH_ID), ("cache_epoch", DEFAULT_CACHE_EPOCH)):
        raw = value.get(key)
        out[key] = default if raw is None else normalize_scope_token(raw)
        if out[key] is None:
            return None
    return Scope(**out)


def parse_capabilities(value: Any) -> CapabilitiesView:
    """§4 tolerant reader. Raises MiddlewareError('unsupported_version') only for step-1 failures."""
    if not isinstance(value, dict):
        raise MiddlewareError("unsupported_version")
    limits, protocol = value.get("limits"), value.get("protocol")
    if not (isinstance(protocol, dict) and _safe(protocol.get("min")) and _safe(protocol.get("max"))):
        protocol = None  # malformed reads as absent
    if (type(value.get("schema_version")) is not int or value["schema_version"] != 1
            or (protocol is not None and not protocol["min"] <= 1 <= protocol["max"])
            or not _token(value.get("policy_revision")) or not isinstance(value.get("runtime_build"), str)
            or not isinstance(value.get("transforms"), list) or not isinstance(limits, dict)
            or not all(_positive(limits.get(k)) for k in ("deadline_ms", "request_bytes", "segment_bytes", "page_bytes"))
            or type(value.get("persistent")) is not bool or type(value.get("recovery")) is not bool
            or not _safe(value.get("retention_seconds"))):
        raise MiddlewareError("unsupported_version")
    features = value.get("features")
    legacy = not isinstance(features, list)
    known = () if legacy else tuple(sorted({f for f in features if isinstance(f, str) and f in KNOWN_FEATURES}))
    usable, seen = [], set()
    for t in value["transforms"]:
        if (isinstance(t, dict) and _token(t.get("transform_id")) and _token(t.get("implementation_version"))
                and isinstance(t.get("eligible_segment_kinds"), list) and t.get("deterministic") is True
                and t.get("recovery") in CLIENT_RECOVERY_ALLOWLIST and t["transform_id"] not in seen):
            seen.add(t["transform_id"])
            usable.append(t)

    def limit(key, default):
        return limits[key] if _positive(limits.get(key)) else default

    d = MIDDLEWARE_DEFAULTS
    effective = EffectiveLimits(limits["deadline_ms"], limits["request_bytes"], limits["segment_bytes"], limits["page_bytes"],
                                limit("retrieve_deadline_ms", d["retrieve_deadline_ms"]), limit("max_segments", d["max_segments"]),
                                limit("max_manifest_items", d["max_manifest_items"]), limit("receipt_bytes", d["receipt_bytes"]),
                                limit("queue_depth", None), limit("retrieve_queue_depth", None), limit("quota_requests_per_minute", None))
    retention = value.get("max_retention_seconds")
    return CapabilitiesView(value, legacy, known, tuple(usable), value["mode"] if value.get("mode") in ("record", "compress") else "record",
                            effective, retention if _positive(retention) else None)


def code_outcome(code: str, status: int = 0, retry_after_ms: int | None = None) -> FailureOutcome:
    """§6 step 6 for an error code already extracted from a response (or raised by a transport)."""
    policy = REASON_CATALOG.get(code)
    return FailureOutcome(code, policy.breaker if policy else status >= 500,
                          code in _CLEARS or (policy is None and status >= 500), retry_after_ms)


def classify_failure(failure: FailureInput) -> FailureOutcome:
    """§6 client classification. Outcomes derive from error.code, never the status alone."""
    if failure.transport == "error":
        return FailureOutcome("runtime_unavailable", True, True, None)
    if failure.transport == "timeout":
        return FailureOutcome("deadline", True, False, None)
    if failure.transport == "redirect":
        return FailureOutcome("redirect_refused", True, True, None)
    status = failure.status or 0
    try:
        code = json.loads(failure.body or "")["error"]["code"]
    except (ValueError, TypeError, KeyError, IndexError):
        code = None
    if not isinstance(code, str) or not _REASON.fullmatch(code):
        return FailureOutcome("runtime_unavailable", True, True, None)
    if code == "request_timeout":
        return FailureOutcome("deadline", True, False, None)
    retry = failure.retry_after.strip() if isinstance(failure.retry_after, str) else ""
    retry_ms = (min(int(retry) * 1000, MIDDLEWARE_DEFAULTS["retry_after_cap_ms"])
                if status in (429, 503) and retry.isascii() and retry.isdigit() else None)
    return code_outcome(code, status, retry_ms)


class CircuitBreaker:
    """§10 breaker. Pure state machine: callers serialize allow()/record() themselves."""

    def __init__(self, params: BreakerParams = BREAKER_DEFAULTS):
        self._params = params
        self.state = "closed"
        self._consecutive = 0
        self._window: deque[bool] = deque(maxlen=params.window_size)
        self._opened_at = 0
        self._probing = False

    def allow(self, now_ms: float) -> bool:
        if self.state == "open":
            if now_ms - self._opened_at < self._params.open_ms:
                return False
            self.state, self._probing = "half_open", False
        if self.state == "half_open":
            if self._probing:
                return False
            self._probing = True
        return True

    def record(self, result: str, now_ms: float) -> None:
        failure = result == "failure"
        if self.state == "half_open":
            self._probing = False
            if failure:
                self.state, self._opened_at = "open", now_ms
            else:
                self.state, self._consecutive = "closed", 0
                self._window.clear()
            return
        if self.state == "open":
            return
        self._window.append(failure)
        self._consecutive = self._consecutive + 1 if failure else 0
        if failure and (self._consecutive >= self._params.consecutive_failures
                        or (len(self._window) == self._params.window_size and sum(self._window) >= self._params.window_failures)):
            self.state, self._opened_at = "open", now_ms


def resolve_deadlines(deadline_ms: int | None = None, retrieve_deadline_ms: int | None = None,
                      limits: EffectiveLimits | Mapping | None = None) -> tuple[int, int]:
    """§10: (optimize_ms, retrieve_ms) = override, else capabilities limits, else 500 / 5000."""
    def get(key):
        return getattr(limits, key, None) if isinstance(limits, EffectiveLimits) else (limits or {}).get(key)

    def pick(override, key, default):
        return next((v for v in (override, get(key)) if _positive(v)), default)

    return (pick(deadline_ms, "deadline_ms", MIDDLEWARE_DEFAULTS["bootstrap_deadline_ms"]),
            pick(retrieve_deadline_ms, "retrieve_deadline_ms", MIDDLEWARE_DEFAULTS["retrieve_deadline_ms"]))


def plan_budget(items: Sequence[BudgetItem], *, max_segments: int, max_bytes: int, replaced=()) -> BudgetResult:
    """§11: previously replaced items first (input order), then newest first; first-fit; input order out."""
    replaced = set(replaced)
    order = [item for item in items if item.key in replaced] + [item for item in reversed(items) if item.key not in replaced]
    admitted, used = set(), 0
    for item in order:
        if len(admitted) < max_segments and used + item.bytes <= max_bytes:
            admitted.add(item.id)
            used += item.bytes
    return BudgetResult(tuple(i.id for i in items if i.id in admitted), tuple(i.id for i in items if i.id not in admitted))


def manifest_window(sizes: Sequence[int], max_items: int, max_bytes: int) -> int:
    """§11: the largest head length k with k <= max_items and sum(sizes[:k]) <= max_bytes."""
    total = 0
    for k, size in enumerate(sizes):
        total += size
        if k >= max_items or total > max_bytes:
            return k
    return len(sizes)


def opaque_manifest_value(value: Any) -> dict[str, str]:
    """§11: manifest stand-in for an image/bytes part; never a reason to bypass."""
    if isinstance(value, (bytes, bytearray, memoryview)):
        return {"caveman_opaque": hashlib.sha256(value).hexdigest()}
    if isinstance(value, str):
        try:
            return {"caveman_opaque": hashlib.sha256(value.encode("utf-8")).hexdigest()}
        except UnicodeEncodeError:
            pass
    return {"caveman_opaque": "unhashable"}


def resolve_endpoint(endpoint: Any, allow_remote_content: bool = False, allow_insecure_transport: bool = False) -> str:
    """§15: the base URL with its path prefix and a trailing slash, or MiddlewareError(refusal code)."""
    try:
        if not isinstance(endpoint, str) or "?" in endpoint or "#" in endpoint:
            raise ValueError
        url = urlsplit(endpoint)
        host, _ = url.hostname, url.port  # .port raises ValueError for a malformed port
        path = url.path[:-1] if url.path.endswith("/") else url.path
        if (url.scheme not in ("http", "https") or not host or url.username is not None or url.password is not None
                or (path and (not path.startswith("/") or any(
                    s in (".", "..") or not _PATH_SEGMENT.fullmatch(s) for s in path[1:].split("/"))))):
            raise ValueError
    except ValueError:
        raise MiddlewareError("invalid_endpoint") from None
    if host not in LOOPBACK:
        if not allow_remote_content:
            raise MiddlewareError("remote_content_not_enabled")
        if url.scheme == "http" and not allow_insecure_transport:
            raise MiddlewareError("insecure_transport_not_enabled")
    return f"{url.scheme}://{url.netloc}{path}/"


def _ip(host: str) -> bool:
    try:
        ipaddress.ip_address(host)
        return True
    except ValueError:
        return False


def resolve_proxy(url: str, env: Mapping[str, str] | None = None) -> str | None:
    """§15: the proxy URL for url from http(s)_proxy / no_proxy (lowercase wins, empty = unset), else None."""
    env = os.environ if env is None else env
    parts = urlsplit(url)
    host = (parts.hostname or "").lower()
    if not host or host in LOOPBACK:
        return None
    name = "https_proxy" if parts.scheme == "https" else "http_proxy"
    proxy = env.get(name) or env.get(name.upper()) or None
    if proxy is None:
        return None
    port, host_ip = str(parts.port or (443 if parts.scheme == "https" else 80)), _ip(host)
    for entry in (env.get("no_proxy") or env.get("NO_PROXY") or "").split(","):
        entry, entry_port = entry.strip().lower(), None
        if entry == "*":
            return None
        if entry.startswith("["):
            entry, _, rest = entry[1:].partition("]")
            entry_port = rest[1:] if rest.startswith(":") else None
        elif entry.count(":") == 1:
            entry, entry_port = entry.split(":")
        entry = entry[1:] if entry.startswith(".") else entry
        if not entry or (entry_port is not None and entry_port != port):
            continue
        if host == entry or (not host_ip and not _ip(entry) and host.endswith("." + entry)):
            return None
    return proxy


_warned: set[tuple[str, str]] = set()
_warned_lock = threading.Lock()


def warn_once(adapter: Any, reason: Any) -> bool:
    """Log one WARNING per (adapter, reason) per process (bounded); returns whether it logged.

    The line carries only `adapter=<id or ->` and `reason=<code>`: never content, scope, handles or credentials.
    """
    policy = REASON_CATALOG.get(reason) if isinstance(reason, str) else None
    if policy is not None and not policy.warn_once:
        return False
    key = (adapter if _token(adapter) else "-", reason if isinstance(reason, str) and _REASON.fullmatch(reason) else "unknown_reason")
    with _warned_lock:
        # ponytail: a full set stops logging new pairs instead of evicting; 1024 pairs is far past real adapter x reason counts.
        if key in _warned or len(_warned) >= MIDDLEWARE_DEFAULTS["warn_once_entries"]:
            return False
        _warned.add(key)
    LOGGER.warning("Caveman middleware passed content through unchanged: adapter=%s reason=%s", *key)
    return True


def _after_fork_in_child() -> None:
    global _warned_lock
    _warned_lock = threading.Lock()


if hasattr(os, "register_at_fork"):
    os.register_at_fork(after_in_child=_after_fork_in_child)
