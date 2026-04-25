#!/usr/bin/env python3
"""
Layman Simplify CLI

Usage:
    layman <filepath>
"""

import sys
from pathlib import Path

from .compress import compress_file
from .detect import detect_file_type, should_compress


def print_usage():
    print("Usage: layman <filepath>")


def main():
    if len(sys.argv) != 2:
        print_usage()
        sys.exit(1)

    filepath = Path(sys.argv[1])

    # Check file exists
    if not filepath.exists():
        print(f"❌ File not found: {filepath}")
        sys.exit(1)

    if not filepath.is_file():
        print(f"❌ Not a file: {filepath}")
        sys.exit(1)

    filepath = filepath.resolve()

    # Detect file type
    file_type = detect_file_type(filepath)

    print(f"Detected: {file_type}")

    # Check if this file type is safe to simplify.
    if not should_compress(filepath):
        print("Skipping: file is not natural language (code/config)")
        sys.exit(0)

    print("Starting Layman simplification...\n")

    try:
        success = compress_file(filepath)

        if success:
            print("\nSimplification completed successfully")
            backup_path = filepath.with_name(filepath.stem + ".original.md")
            print(f"Simplified: {filepath}")
            print(f"Original:   {backup_path}")
            sys.exit(0)
        else:
            print("\n❌ Simplification failed after retries")
            sys.exit(2)

    except KeyboardInterrupt:
        print("\nInterrupted by user")
        sys.exit(130)

    except Exception as e:
        print(f"\n❌ Error: {e}")
        sys.exit(1)


if __name__ == "__main__":
    main()
