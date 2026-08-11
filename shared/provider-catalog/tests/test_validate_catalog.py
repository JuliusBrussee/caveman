from datetime import datetime, timezone
import importlib.util
from pathlib import Path
import re
import unittest


MODULE_PATH = Path(__file__).resolve().parents[1] / "validate_catalog.py"
SPEC = importlib.util.spec_from_file_location("validate_catalog", MODULE_PATH)
assert SPEC and SPEC.loader
validator = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(validator)


class ValidateCatalogTest(unittest.TestCase):
    def test_shipped_catalog_is_valid(self) -> None:
        # The real shipped catalog's verified_at dates move forward over time;
        # pinning "now" to a fixed past date makes this test re-break on every
        # legitimate price recheck (it already had, against a stale 2026-07-26
        # pin, once rows moved to 2026-07-30). Use the real current time so this
        # only fails for an actual problem (future-dated or >120-day-stale row).
        validator.validate_catalog(datetime.now(timezone.utc))

    def test_unknown_field_fails_closed(self) -> None:
        row = {
            "provider": "test",
            "model": "model",
            "region": "global",
            "currency": "USD",
            "pricing": {"input_per_million": 1, "output_per_million": 2},
            "capabilities": {},
            "sources": ["https://example.com/pricing"],
            "verified_at": "2026-07-26T00:00:00Z",
            "guessed_price": True,
        }
        with self.assertRaisesRegex(validator.CatalogError, "unknown field"):
            validator.validate_row(
                row,
                "fixture",
                datetime(2026, 7, 26, tzinfo=timezone.utc),
            )

    def test_price_affecting_keys_match_the_go_source_of_truth(self) -> None:
        # catalog.PriceAffectingCapabilities in catalog.go is the source of
        # truth; this module's tuple is a mirror, and an unchecked mirror is
        # exactly how region_agnostic_pricing stayed outside the pin after the
        # two multipliers were pulled inside it. Parse the Go slice and compare.
        go_source = (
            MODULE_PATH.parents[1] / "platform" / "catalog" / "catalog.go"
        ).read_text(encoding="utf-8")
        block = re.search(
            r"var PriceAffectingCapabilities = \[\]string\{(.*?)\n\}", go_source, re.S
        )
        self.assertIsNotNone(block, "PriceAffectingCapabilities not found in catalog.go")
        body = block.group(1)
        go_keys = re.findall(r'"([^"]+)"', body)
        # An entry may reference a Go const rather than a literal; resolve those
        # so the comparison is over real key names, not identifiers.
        for name in re.findall(r"^\s*([A-Z]\w+),\s*$", body, re.M):
            const = re.search(rf'{name}\s*=\s*"([^"]+)"', go_source)
            self.assertIsNotNone(const, f"could not resolve Go const {name}")
            go_keys.append(const.group(1))
        self.assertEqual(
            sorted(go_keys),
            sorted(validator.PRICE_AFFECTING_CAPABILITY_KEYS),
            "the Go and Python price-affecting capability lists have drifted; a "
            "key pinned on one side but not the other can move money the "
            "catalog_version in a signed receipt does not attest",
        )

    def test_price_multiplier_is_inside_the_snapshot_identity(self) -> None:
        # Mirrors TestTamperedPriceCapabilityBreaksTheSnapshotPin on the Go side.
        # regional_processing_multiplier and inference_geo_us_multiplier are
        # capabilities the gateway MULTIPLIES the row's rates by, so a change to
        # one changes real dollars. Before they joined pricing_identity, moving
        # 1.10 to 1.95 (a 77% inflation) still matched its immutable snapshot.
        base = {
            "provider": "openai",
            "model": "gpt-5.5",
            "region": "global",
            "currency": "USD",
            "pricing": {"input_per_million": 5, "output_per_million": 30},
            "capabilities": {"regional_processing_multiplier": 1.10, "vision": True},
            "sources": ["https://example.com/pricing"],
            "verified_at": "2026-07-26T00:00:00Z",
        }
        tampered = {
            **base,
            "capabilities": {**base["capabilities"], "regional_processing_multiplier": 1.95},
        }
        self.assertNotEqual(
            validator.pricing_identity(base),
            validator.pricing_identity(tampered),
            "a price multiplier escaped the identity the immutable snapshot pins",
        )
        # A genuine capability-only edit must still pass without minting a new
        # price-dated snapshot — that exclusion is the whole point of the split.
        capability_edit = {
            **base,
            "capabilities": {**base["capabilities"], "vision": False, "tools": True},
        }
        self.assertEqual(
            validator.pricing_identity(base),
            validator.pricing_identity(capability_edit),
        )
        # Absent and present are different attestations: adding a multiplier to
        # a row that documented none is a price change too.
        without = {**base, "capabilities": {"vision": True}}
        self.assertNotEqual(validator.pricing_identity(base), validator.pricing_identity(without))

    def test_unreviewed_sync_marker_fails_closed(self) -> None:
        # The sync script bumps verified_at on every row it proposes, so the
        # marker surviving into current.yaml means price provenance moved on a
        # number nobody confirmed. Comments are invisible to the YAML rows, so
        # nothing else in this validator can see it.
        proposed = (
            "# proposed-by: models.dev sync 2026-08-05 -- confirm against the "
            "provider pricing page in sources before merge, then delete this line\n"
            "- provider: openai\n"
            "  model: gpt-5.5\n"
        )
        with self.assertRaisesRegex(validator.CatalogError, "proposed-by"):
            validator.check_review_markers(proposed, "fixture")
        # An indented marker is the same claim, indented.
        with self.assertRaisesRegex(validator.CatalogError, "proposed-by"):
            validator.check_review_markers(f"  {proposed}", "fixture")
        validator.check_review_markers("- provider: openai\n  model: gpt-5.5\n", "fixture")

    def test_review_marker_matches_the_sync_script(self) -> None:
        # A marker the sync script writes but the validator does not recognise
        # is worse than no rule: the PR checklist would still say "delete the
        # marker" while nothing enforced it. Sync automation is intentionally
        # monorepo-only; standalone public checkouts still validate marker
        # rejection above, but cannot compare against an absent private lane.
        sync_path = next(
            (
                path
                for path in (
                    MODULE_PATH.parents[3] / "scripts" / "catalog_sync_modelsdev.py",
                    MODULE_PATH.parents[2] / "scripts" / "catalog_sync_modelsdev.py",
                )
                if path.exists()
            ),
            None,
        )
        if sync_path is None:
            self.skipTest("models.dev sync automation is not shipped in the public repository")
        sync_source = sync_path.read_text(encoding="utf-8")
        declared = re.search(
            r'^REVIEW_MARKER_PREFIX = "([^"]+)"', sync_source, re.M
        )
        self.assertIsNotNone(declared, "REVIEW_MARKER_PREFIX not found in the sync script")
        self.assertTrue(
            declared.group(1).startswith(validator.REVIEW_MARKER),
            "the sync script's marker no longer starts with the literal this "
            "validator refuses; a renamed marker would merge unreviewed",
        )

    def test_non_https_source_fails_closed(self) -> None:
        row = {
            "provider": "test",
            "model": "model",
            "region": "global",
            "currency": "USD",
            "pricing": {"input_per_million": 1, "output_per_million": 2},
            "capabilities": {},
            "sources": ["http://example.com/pricing"],
            "verified_at": "2026-07-26T00:00:00Z",
        }
        with self.assertRaisesRegex(validator.CatalogError, "must be HTTPS"):
            validator.validate_row(
                row,
                "fixture",
                datetime(2026, 7, 26, tzinfo=timezone.utc),
            )


if __name__ == "__main__":
    unittest.main()
