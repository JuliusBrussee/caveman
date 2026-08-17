#!/usr/bin/env python3
"""Stream LMCache Agentic Traces parquet rows as cachebench JSONL."""

from __future__ import annotations

import argparse
import hashlib
import json
import sys
from pathlib import Path


def file_sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as source:
        for chunk in iter(lambda: source.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("parquet", type=Path)
    parser.add_argument("--expected-sha256", help="fail before output when parquet digest differs")
    parser.add_argument("--batch-size", type=int, default=32)
    args = parser.parse_args()
    if args.batch_size < 1 or args.batch_size > 4096:
        parser.error("--batch-size must be between 1 and 4096")
    if not args.parquet.is_file():
        parser.error(f"file not found: {args.parquet}")
    if args.expected_sha256:
        actual = file_sha256(args.parquet)
        if actual.lower() != args.expected_sha256.lower():
            parser.error(f"sha256 mismatch: got {actual}")

    try:
        import pyarrow.parquet as parquet
    except ImportError:
        parser.error("pyarrow required: python -m pip install pyarrow")

    output = sys.stdout
    source = parquet.ParquetFile(args.parquet)
    try:
        for batch in source.iter_batches(batch_size=args.batch_size):
            for row in batch.to_pylist():
                output.write(
                    json.dumps(row, ensure_ascii=False, allow_nan=False, separators=(",", ":"))
                    + "\n"
                )
    except BrokenPipeError:
        return 0
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
