from datetime import datetime, timezone
import importlib.util
from pathlib import Path
import unittest


MODULE_PATH = Path(__file__).resolve().parents[1] / "validate_catalog.py"
SPEC = importlib.util.spec_from_file_location("validate_catalog", MODULE_PATH)
assert SPEC and SPEC.loader
validator = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(validator)


class ValidateCatalogTest(unittest.TestCase):
    def test_shipped_catalog_is_valid(self) -> None:
        validator.validate_catalog(datetime(2026, 7, 26, tzinfo=timezone.utc))

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
