"""Untrusted runtime results are validated in full before any native edit."""
import hashlib
import json
import re
from dataclasses import asdict
from typing import Any

from .protocol import parse_capabilities
from .types import RECOVERY_MARKER_PREFIX, CapabilitiesView, MiddlewareError, Scope


def sha256(text: str) -> str:
    return hashlib.sha256(text.encode("utf-8")).hexdigest()


def token(value: Any) -> bool:
    return isinstance(value, str) and re.fullmatch(r"[a-zA-Z0-9._:/-]{1,256}", value) is not None


def reason(value: Any) -> bool:
    return isinstance(value, str) and re.fullmatch(r"[a-z][a-z0-9_]{0,63}", value) is not None


def utf8(text: Any) -> bytes | None:
    """Strict UTF-8 of well-formed text; None for non-strings and unpaired surrogates."""
    try:
        return text.encode("utf-8") if isinstance(text, str) else None
    except UnicodeEncodeError:
        return None


def digest(value: Any) -> bool:
    return isinstance(value, str) and re.fullmatch(r"[a-f0-9]{64}", value) is not None


def integer(value: Any) -> bool:
    return type(value) is int and 0 <= value <= 9007199254740991


def scope_key(scope: Scope) -> str:
    values = list(asdict(scope).values())
    if not all(token(v) for v in values):
        raise MiddlewareError("invalid_scope")
    return json.dumps(values, separators=(",", ":"))


def capabilities(value: Any) -> dict[str, Any]:
    """Protocol 1.0 name, now the §4 tolerant reader: raises only unsupported_version and returns the document."""
    return parse_capabilities(value).capabilities


def plan(value: Any, request: dict, input_digest: str, caps: CapabilitiesView | dict) -> dict:
    """Validate the whole plan before any replacement applies (§7, K5); any violation is invalid_plan.

    `caps` is the capabilities snapshot the request was built from (view or raw document). Only transforms on the
    client recovery allowlist apply, every replacement carries its marker and handle and is strictly shorter in
    UTF-8 bytes. A differing policy_revision or transform_version is accepted (K3); the runtime refreshes.
    """
    view = caps if isinstance(caps, CapabilitiesView) else parse_capabilities(caps)
    try:
        p = value
        if (type(p["schema_version"]) is not int or p["schema_version"] != 1 or p["request_id"] != request["request_id"] or p["input_digest"] != input_digest
                or not token(p["policy_revision"]) or not digest(p["replacement_set_id"])
                or p["status"] not in ("optimized", "bypassed", "record") or not token(p["reason"])
                or not isinstance(p["replacements"], list) or not isinstance(p["skipped"], list)):
            raise ValueError()
        m = p["measurement"]
        recovery = p["recovery"]
        if (m["basis"] != "inferred" or m["scope"] != "segment" or m["verified_saved_usd"] != 0
                or not isinstance(m["tokenizer"], str)
                or not all(integer(m[k]) for k in ("tokens_before", "tokens_after", "unique_tokens_reduced", "recovery_overhead_tokens"))
                or m["tokens_after"] > m["tokens_before"] or p["stability"]["provider_bytes"] != "unobserved"
                or p["stability"]["provider_cache_hits"] != "unobserved" or p["stability"]["native"] not in ("persistent_choices", "unavailable")
                or type(recovery["available"]) is not bool or type(recovery["persistent"]) is not bool or not integer(recovery["expires_at"])):
            raise ValueError()
        segments = {s["id"]: s for s in request["segments"]}
        transforms = {t["transform_id"]: t for t in view.transforms}  # allowlisted recovery only: `none` never applies
        seen, credited = set(), set()
        reduction = unique = 0
        for r in p["replacements"]:
            s, t = segments[r["segment_id"]], transforms[r["transform_id"]]
            if (r["segment_id"] in seen or r["original_sha256"] != s["sha256"] or r["source_id"] != s["source_id"]
                    or r["transform_id"] not in request["policy"]["transforms"] or not token(r["transform_version"])
                    or s["kind"] not in t["eligible_segment_kinds"] or s["protected"] or s["opaque"] or request["mode"] == "record" or view.mode == "record"
                    or not isinstance(r["text"], str) or len(r["text"].encode("utf-8")) > view.limits.segment_bytes
                    or len(r["text"].encode("utf-8")) >= len(s["content"].encode("utf-8"))
                    or not digest(r["sha256"]) or r["sha256"] != sha256(r["text"])
                    or not integer(r["tokens_before"]) or not integer(r["tokens_after"]) or r["tokens_after"] >= r["tokens_before"] or type(r["reused"]) is not bool
                    or type(r["unique_original"]) is not bool or (r["unique_original"] and (r["reused"] or r["original_sha256"] in credited))):
                raise ValueError()
            if (not request["recovery_binding"] or recovery["binding_id"] != request["recovery_binding"]["id"]
                    or not recovery["available"] or not recovery["persistent"] or re.fullmatch(r"cmw_[a-f0-9]{48}", r["recovery_handle"]) is None
                    or not r["text"].startswith(f"{RECOVERY_MARKER_PREFIX}{r['recovery_handle']}]\n")):
                raise ValueError()
            seen.add(r["segment_id"])
            reduction += r["tokens_before"] - r["tokens_after"]
            if r["unique_original"]:
                unique += r["tokens_before"] - r["tokens_after"]
                credited.add(r["original_sha256"])
        for skipped in p["skipped"]:
            if skipped["segment_id"] not in segments or skipped["segment_id"] in seen or not token(skipped["reason"]):
                raise ValueError()
            seen.add(skipped["segment_id"])
        if (len(seen) != len(segments) or m["tokens_before"] - m["tokens_after"] != reduction or m["unique_tokens_reduced"] != unique
                or (p["replacements"] and (p["status"] != "optimized" or reduction <= m["recovery_overhead_tokens"]))
                or (p["status"] == "optimized" and not p["replacements"])):
            raise ValueError()
        return p
    except (KeyError, TypeError, ValueError, UnicodeError):
        raise MiddlewareError("invalid_plan") from None


def page(value: Any, args: dict, max_bytes: int) -> dict:
    try:
        p = value
        if (type(p["schema_version"]) is not int or p["schema_version"] != 1 or p["handle"] != args["handle"] or not token(p["source_id"]) or not digest(p["original_sha256"])
                or not isinstance(p["text"], str) or not integer(p["total_bytes"]) or not integer(p["offset"]) or p["offset"] != args.get("offset", 0)
                or p["kind"] not in ("original_page", "excerpt") or type(p["complete"]) is not bool
                or not (p["next_offset"] is None or integer(p["next_offset"]))):
            raise ValueError()
        length = len(p["text"].encode("utf-8"))
        end = p["offset"] + length
        if length > max_bytes:
            raise ValueError()
        if p["kind"] == "original_page" and (end > p["total_bytes"] or p["next_offset"] != (end if end < p["total_bytes"] else None)
                or p["complete"] != (p["offset"] == 0 and length == p["total_bytes"])):
            raise ValueError()
        if p["kind"] == "excerpt" and (p["complete"] or not args.get("query") or p["next_offset"] != 0):
            raise ValueError()
        if p["complete"] and sha256(p["text"]) != p["original_sha256"]:
            raise ValueError()
        return p
    except (KeyError, TypeError, ValueError, UnicodeError):
        raise MiddlewareError("invalid_recovery") from None
