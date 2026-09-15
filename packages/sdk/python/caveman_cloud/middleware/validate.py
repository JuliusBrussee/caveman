"""Untrusted runtime results are validated in full before any native edit."""
import hashlib
import json
import re
from dataclasses import asdict
from typing import Any

from .types import MiddlewareError, Scope


def sha256(text: str) -> str:
    return hashlib.sha256(text.encode("utf-8")).hexdigest()


def token(value: Any) -> bool:
    return isinstance(value, str) and re.fullmatch(r"[a-zA-Z0-9._:/-]{1,256}", value) is not None


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
    try:
        if (type(value["schema_version"]) is not int or value["schema_version"] != 1 or not token(value["policy_revision"])
                or not isinstance(value["runtime_build"], str) or value["mode"] not in ("record", "compress")
                or not isinstance(value["transforms"], list) or len(value["transforms"]) > 128
                or not all(integer(value["limits"][k]) and value["limits"][k] > 0 for k in ("deadline_ms", "request_bytes", "segment_bytes", "page_bytes"))
                or not integer(value["retention_seconds"]) or type(value["recovery"]) is not bool
                or type(value["persistent"]) is not bool):
            raise ValueError()
        seen = set()
        for t in value["transforms"]:
            if (not token(t["transform_id"]) or t["transform_id"] in seen or not token(t["implementation_version"])
                    or not isinstance(t["eligible_segment_kinds"], list) or t["recovery"] not in ("exact_ccr", "none") or t["deterministic"] is not True):
                raise MiddlewareError("unknown_capability")
            seen.add(t["transform_id"])
        return value
    except (KeyError, TypeError, ValueError) as error:
        raise MiddlewareError("unsupported_version") from None


def plan(value: Any, request: dict, input_digest: str, caps: dict) -> dict:
    try:
        p = value
        if (type(p["schema_version"]) is not int or p["schema_version"] != 1 or p["request_id"] != request["request_id"] or p["input_digest"] != input_digest
                or p["policy_revision"] != request["policy"]["revision"] or not digest(p["replacement_set_id"])
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
        transforms = {t["transform_id"]: t for t in caps["transforms"]}
        seen, credited = set(), set()
        reduction = unique = 0
        for r in p["replacements"]:
            s, t = segments[r["segment_id"]], transforms[r["transform_id"]]
            if (r["segment_id"] in seen or r["original_sha256"] != s["sha256"] or r["source_id"] != s["source_id"]
                    or r["transform_id"] not in request["policy"]["transforms"] or r["transform_version"] != t["implementation_version"]
                    or s["kind"] not in t["eligible_segment_kinds"] or s["protected"] or s["opaque"] or request["mode"] == "record" or caps["mode"] == "record"
                    or not isinstance(r["text"], str) or len(r["text"].encode("utf-8")) > caps["limits"]["segment_bytes"]
                    or not digest(r["sha256"]) or r["sha256"] != sha256(r["text"])
                    or not integer(r["tokens_before"]) or not integer(r["tokens_after"]) or r["tokens_after"] >= r["tokens_before"] or type(r["reused"]) is not bool
                    or type(r["unique_original"]) is not bool or (r["unique_original"] and (r["reused"] or r["original_sha256"] in credited))):
                raise ValueError()
            if t["recovery"] == "exact_ccr" and (not request["recovery_binding"] or recovery["binding_id"] != request["recovery_binding"]["id"]
                    or not recovery["available"] or not recovery["persistent"] or re.fullmatch(r"cmw_[a-f0-9]{48}", r["recovery_handle"]) is None
                    or not r["text"].startswith(f"[caveman: shortened; exact original via caveman_retrieve handle={r['recovery_handle']}]\n")):
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
