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
REQUIRED_KEYS = {
    "provider",
    "model",
    "region",
    "currency",
    "pricing",
    "capabilities",
    "sources",
    "verified_at",
}
# capabilities_verified_at is allowed but not required: it tracks capability
# provenance (tools/vision/json_mode/context_window_tokens last checked
# against the vendor's model docs) separately from verified_at (price
# provenance, the only field catalogVersion()/receipts read — see
# public/shared/platform/catalog/catalog.go). Older immutable dated snapshots
# predate this field and must keep validating without it.
TOP_LEVEL_KEYS = REQUIRED_KEYS | {"capabilities_verified_at"}
# The fields the immutable dated snapshot actually pins (mirrors
# pricingIdentity in public/shared/platform/catalog/catalog_test.go).
# capabilities_verified_at, sources, and every capability EXCEPT the
# price-affecting ones below are deliberately excluded so a capability-only edit
# doesn't have to mint a new price-dated snapshot just to keep this check
# passing.
PRICING_IDENTITY_KEYS = ("provider", "model", "region", "currency", "pricing", "verified_at")
# These capability keys are not capability data: they decide what a request
# costs. The first two MULTIPLY the row's token rates (catalog.PricingMultiplier
# -> scaleStandaloneTokenRates in the gateway proxies); the third decides
# whether a global row's price answers a regional lookup at all
# (PriceForRegionOrAgnostic), so removing it drops a whole region's spend to an
# honest zero. They live under `capabilities` for schema reasons only;
# semantically they are price, so the snapshot must pin them. Leaving them out
# let a one-character edit change real money while verified_at — and the
# catalog_version a signed receipt attests — stayed put.
#
# MUST stay equal to catalog.PriceAffectingCapabilities in
# public/shared/platform/catalog/catalog.go, which is the source of truth. The
# test suite asserts that equality by parsing the Go file, so the two lists
# cannot drift apart the way this one drifted from the reader that consumed it.
PRICE_AFFECTING_CAPABILITY_KEYS = (
    "regional_processing_multiplier",
    "inference_geo_us_multiplier",
    "region_agnostic_pricing",
)
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
# scripts/catalog_sync_modelsdev.py proposes price changes by editing
# current.yaml AND bumping verified_at, then marking every row it touched with a
# comment line starting with this literal. verified_at is price provenance — the
# catalog_version a signed receipt attests — so a proposal that merges with its
# marker intact attests a price check nobody performed. Deleting the marker is
# the reviewer's statement that they confirmed the number against the provider's
# own pricing page; until then, the catalog is invalid.
# MUST stay a prefix of REVIEW_MARKER_PREFIX in that script (the test suite
# asserts it), or a renamed marker would sail past this rule.
REVIEW_MARKER = "# proposed-by:"


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


def check_review_markers(text: str, label: str) -> None:
    """Refuse a catalog that still carries an unreviewed sync proposal.

    YAML comments are invisible to the parsed rows, so this reads the raw file:
    a rubber-stamped merge must not be able to advance price provenance while
    the line saying "nobody has confirmed this yet" is still in the file.
    """
    offending = [
        index + 1
        for index, line in enumerate(text.splitlines())
        if line.lstrip().startswith(REVIEW_MARKER)
    ]
    if offending:
        raise CatalogError(
            f"{label}: unreviewed sync proposal marker on line(s) "
            f"{', '.join(str(number) for number in offending)}: the proposal bumped "
            "verified_at, so merging with the marker intact attests a price check "
            "nobody performed -- confirm each row against its sources, then delete "
            f"the {REVIEW_MARKER!r} line"
        )


def pricing_identity(row: dict[str, Any]) -> dict[str, Any]:
    """The part of a row the immutable dated snapshot pins.

    Present-with-a-value and absent are different attestations, so only the
    price-affecting capability keys the row actually carries are recorded:
    adding one to a row that had none changes that row's price and must break
    the pin exactly like editing one that was already there.
    """
    identity = {key: row.get(key) for key in PRICING_IDENTITY_KEYS}
    capabilities = row.get("capabilities") or {}
    identity["price_affecting_capabilities"] = {
        key: capabilities[key] for key in PRICE_AFFECTING_CAPABILITY_KEYS if key in capabilities
    }
    return identity


def validate_catalog(now: datetime | None = None) -> None:
    check_time = now or datetime.now(timezone.utc)
    current_path = CATALOG_DIR / "current.yaml"
    current = load_yaml(current_path)
    check_review_markers(current_path.read_text(encoding="utf-8"), "current.yaml")
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
        identity = pricing_identity(row)
        matched = next(
            (snap_row for snap_row in snapshot if pricing_identity(snap_row) == identity),
            None,
        )
        if matched is None:
            raise CatalogError(
                f"{label}: pricing changed without a new verified_at snapshot version (checked catalog/{verified}.yaml)"
            )
        # sources is excluded from pricing_identity so a capability citation can
        # be added without minting a new price-dated snapshot, but a PRICING
        # citation must never be silently swapped or dropped while verified_at
        # stays put. Mirrors the Go isSubset check in catalog_test.go.
        snapshot_sources = set(matched.get("sources") or [])
        if not snapshot_sources.issubset(set(row["sources"])):
            raise CatalogError(
                f"{label}: dropped or replaced a source from its immutable snapshot "
                f"catalog/{verified}.yaml without a new verified_at "
                f"(snapshot had {sorted(snapshot_sources)}, current has {sorted(row['sources'])})"
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
