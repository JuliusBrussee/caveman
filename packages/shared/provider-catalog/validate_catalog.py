#!/usr/bin/env python3
from __future__ import annotations

import math
import re
import sys
from datetime import datetime, timedelta, timezone
from pathlib import Path
from typing import Any

import yaml

ROOT = Path(__file__).resolve().parent
CATALOG_DIR = ROOT / "catalog"
TOP_LEVEL_KEYS = {
    "provider",
    "model",
    "region",
    "currency",
    "pricing",
    "capabilities",
    "sources",
    "verified_at",
}
REQUIRED_KEYS = TOP_LEVEL_KEYS
PRICING_KEYS = {
    "input_per_million",
    "output_per_million",
    "cache_read_input_per_million",
    "cache_write_input_per_million",
    "cache_write_1h_input_per_million",
    "reasoning_output_per_million",
    "batch_discount_fraction",
    "cache_storage_per_million_tokens_hour",
    "long_context_threshold_tokens",
    "long_context_threshold_inclusive",
    "long_context_input_multiplier",
    "long_context_output_multiplier",
}
IDENTIFIER = re.compile(r"^[A-Za-z0-9][A-Za-z0-9._:@/-]*$")


class CatalogError(ValueError):
    pass


def load_yaml(path: Path) -> list[dict[str, Any]]:
    try:
        document = yaml.safe_load(path.read_text(encoding="utf-8"))
    except (OSError, yaml.YAMLError) as error:
        raise CatalogError(f"{path}: invalid YAML: {error}") from error
    if not isinstance(document, list) or not document:
        raise CatalogError(f"{path}: catalog must be a non-empty list")
    if not all(isinstance(row, dict) for row in document):
        raise CatalogError(f"{path}: every catalog row must be an object")
    return document


def parsed_time(value: Any, label: str) -> datetime:
    if isinstance(value, datetime):
        parsed = value
    elif isinstance(value, str):
        try:
            parsed = datetime.fromisoformat(value.replace("Z", "+00:00"))
        except ValueError as error:
            raise CatalogError(f"{label}: verified_at must be RFC3339") from error
    else:
        raise CatalogError(f"{label}: verified_at must be an RFC3339 timestamp")
    if parsed.tzinfo is None:
        raise CatalogError(f"{label}: verified_at must include a timezone")
    return parsed.astimezone(timezone.utc)


def validate_row(row: dict[str, Any], label: str, now: datetime) -> tuple[str, str, str]:
    unknown = set(row) - TOP_LEVEL_KEYS
    missing = REQUIRED_KEYS - set(row)
    if unknown:
        raise CatalogError(f"{label}: unknown field(s): {', '.join(sorted(unknown))}")
    if missing:
        raise CatalogError(f"{label}: missing field(s): {', '.join(sorted(missing))}")

    identity: list[str] = []
    for field in ("provider", "model", "region"):
        value = row[field]
        if not isinstance(value, str) or not IDENTIFIER.fullmatch(value):
            raise CatalogError(f"{label}: {field} must be a safe non-empty identifier")
        identity.append(value)
    if row["currency"] != "USD":
        raise CatalogError(f"{label}: currency must be USD")

    pricing = row["pricing"]
    if not isinstance(pricing, dict):
        raise CatalogError(f"{label}: pricing must be an object")
    pricing_unknown = set(pricing) - PRICING_KEYS
    if pricing_unknown:
        raise CatalogError(f"{label}: unknown pricing field(s): {', '.join(sorted(pricing_unknown))}")
    for required in ("input_per_million", "output_per_million"):
        if required not in pricing:
            raise CatalogError(f"{label}: pricing.{required} is required")
    for field, value in pricing.items():
        if value is None or field == "long_context_threshold_inclusive":
            if field == "long_context_threshold_inclusive" and value is not None and not isinstance(value, bool):
                raise CatalogError(f"{label}: pricing.{field} must be boolean or null")
            continue
        if isinstance(value, bool) or not isinstance(value, (int, float)):
            raise CatalogError(f"{label}: pricing.{field} must be numeric or null")
        if not math.isfinite(value) or value < 0:
            raise CatalogError(f"{label}: pricing.{field} must be finite and nonnegative")
        if field == "batch_discount_fraction" and value > 1:
            raise CatalogError(f"{label}: pricing.batch_discount_fraction must be <= 1")

    if not isinstance(row["capabilities"], dict):
        raise CatalogError(f"{label}: capabilities must be an object")
    sources = row["sources"]
    if not isinstance(sources, list) or not sources:
        raise CatalogError(f"{label}: sources must be a non-empty list")
    if any(not isinstance(source, str) or not source.startswith("https://") for source in sources):
        raise CatalogError(f"{label}: every source must be HTTPS")

    verified_at = parsed_time(row["verified_at"], label)
    if verified_at > now:
        raise CatalogError(f"{label}: verified_at is in the future")
    if verified_at < now - timedelta(days=120):
        raise CatalogError(f"{label}: verified_at is older than 120 days")
    return identity[0], identity[1], identity[2]


def validate_catalog(now: datetime | None = None) -> None:
    check_time = now or datetime.now(timezone.utc)
    current = load_yaml(CATALOG_DIR / "current.yaml")
    snapshots: dict[str, list[dict[str, Any]]] = {}
    seen: set[tuple[str, str, str]] = set()

    for index, row in enumerate(current):
        label = f"current.yaml row {index + 1}"
        identity = validate_row(row, label, check_time)
        if identity in seen:
            raise CatalogError(f"{label}: duplicate identity {'/'.join(identity)}")
        seen.add(identity)

        verified = parsed_time(row["verified_at"], label).date().isoformat()
        snapshot = snapshots.setdefault(
            verified,
            load_yaml(CATALOG_DIR / f"{verified}.yaml"),
        )
        if row not in snapshot:
            raise CatalogError(
                f"{label}: row must match catalog/{verified}.yaml by decoded value"
            )


def main() -> int:
    try:
        validate_catalog()
    except CatalogError as error:
        print(f"provider catalog invalid: {error}", file=sys.stderr)
        return 1
    print("provider catalog valid: current rows are fresh, sourced, and snapshot-backed")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
