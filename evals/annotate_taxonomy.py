#!/usr/bin/env python3
"""Resolve human-readable eval taxonomy keys to stable report IDs."""

from __future__ import annotations

import argparse
import json
import re
import sys
from pathlib import Path
from typing import Any


EVALS = Path(__file__).resolve().parent
DEFAULT_TAXONOMY = EVALS / "semantic_taxonomy.json"
DEFAULT_CASES = EVALS / "prompts" / "semantic_cases.json"
ID_PATTERN = re.compile(r"CAV-SEM-[0-9]{2}\Z")
KEY_PATTERN = re.compile(r"[a-z0-9]+(?:-[a-z0-9]+)*\Z")


class TaxonomyError(ValueError):
    """The taxonomy or case input cannot produce trustworthy annotations."""


def read_object(path: Path) -> dict[str, Any]:
    """Read a JSON object and report malformed inputs as taxonomy errors."""
    try:
        value = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as error:
        raise TaxonomyError(f"cannot read {path}: {error}") from error
    if not isinstance(value, dict):
        raise TaxonomyError(f"expected a JSON object in {path}")
    return value


def taxonomy_index(document: dict[str, Any]) -> dict[str, str]:
    """Validate taxonomy entries and return their key-to-ID mapping."""
    entries = document.get("taxonomy")
    if not isinstance(entries, list) or not entries:
        raise TaxonomyError("taxonomy must be a non-empty list")

    index: dict[str, str] = {}
    seen_ids: set[str] = set()
    for position, entry in enumerate(entries):
        if not isinstance(entry, dict):
            raise TaxonomyError(f"taxonomy entry {position} must be an object")
        key = entry.get("key")
        stable_id = entry.get("id")
        description = entry.get("description")
        if not isinstance(key, str) or not KEY_PATTERN.fullmatch(key):
            raise TaxonomyError(f"taxonomy entry {position} has an invalid key")
        if not isinstance(stable_id, str) or not ID_PATTERN.fullmatch(stable_id):
            raise TaxonomyError(f"taxonomy entry {position} has an invalid stable ID")
        if not isinstance(description, str) or not description.strip():
            raise TaxonomyError(f"taxonomy entry {position} needs a description")
        if key in index:
            raise TaxonomyError(f"duplicate taxonomy key: {key}")
        if stable_id in seen_ids:
            raise TaxonomyError(f"duplicate taxonomy ID: {stable_id}")
        index[key] = stable_id
        seen_ids.add(stable_id)
    return index


def annotate_cases(
    document: dict[str, Any], index: dict[str, str]
) -> dict[str, Any]:
    """Return cases annotated with stable IDs without changing their prompts."""
    cases = document.get("cases")
    if not isinstance(cases, list) or not cases:
        raise TaxonomyError("cases must be a non-empty list")

    annotated: list[dict[str, Any]] = []
    seen_case_ids: set[str] = set()
    for position, case in enumerate(cases):
        if not isinstance(case, dict):
            raise TaxonomyError(f"case {position} must be an object")
        case_id = case.get("id")
        prompt = case.get("prompt")
        keys = case.get("taxonomy")
        if not isinstance(case_id, str) or not case_id:
            raise TaxonomyError(f"case {position} needs a string ID")
        if case_id in seen_case_ids:
            raise TaxonomyError(f"duplicate case ID: {case_id}")
        if not isinstance(prompt, str) or not prompt:
            raise TaxonomyError(f"case {case_id} needs a non-empty prompt")
        if "contract_ids" in case:
            raise TaxonomyError(
                f"case {case_id} must use taxonomy keys, not embedded contract IDs"
            )
        if not isinstance(keys, list) or not keys or not all(
            isinstance(key, str) for key in keys
        ):
            raise TaxonomyError(f"case {case_id} needs taxonomy keys")
        if len(keys) != len(set(keys)):
            raise TaxonomyError(f"case {case_id} repeats a taxonomy key")
        unknown = [key for key in keys if key not in index]
        if unknown:
            raise TaxonomyError(
                f"case {case_id} uses unknown taxonomy keys: {', '.join(unknown)}"
            )

        resolved = dict(case)
        resolved["contract_ids"] = [index[key] for key in keys]
        annotated.append(resolved)
        seen_case_ids.add(case_id)

    result = dict(document)
    result["cases"] = annotated
    return result


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--taxonomy", type=Path, default=DEFAULT_TAXONOMY)
    parser.add_argument("--cases", type=Path, default=DEFAULT_CASES)
    return parser.parse_args()


def main() -> int:
    args = parse_args()
    try:
        index = taxonomy_index(read_object(args.taxonomy))
        annotated = annotate_cases(read_object(args.cases), index)
    except TaxonomyError as error:
        print(f"taxonomy error: {error}", file=sys.stderr)
        return 1
    json.dump(annotated, sys.stdout, ensure_ascii=False, indent=2)
    sys.stdout.write("\n")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
