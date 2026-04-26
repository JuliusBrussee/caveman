#!/usr/bin/env python3
"""
Caveman Compress CLI

Usage:
    caveman [--no-backup] <filepath>

Flags:
    --no-backup    Skip writing `<file>.original.md` next to the input.
                   Useful for low-stakes, version-controlled files where
                   git already provides the rollback path.
"""

import sys
from pathlib import Path

from .compress import compress_file
from .detect import detect_file_type, should_compress


def print_usage():
    print("Usage: caveman [--no-backup] <filepath>")


def main():
    args = sys.argv[1:]
    no_backup = False
    if "--no-backup" in args:
        no_backup = True
        args = [a for a in args if a != "--no-backup"]

    if len(args) != 1:
        print_usage()
        sys.exit(1)

    filepath = Path(args[0])

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

    # Check if compressible
    if not should_compress(filepath):
        print("Skipping: file is not natural language (code/config)")
        sys.exit(0)

    print("Starting caveman compression...\n")

    try:
        success = compress_file(filepath, no_backup=no_backup)

        if success:
            print("\nCompression completed successfully")
            print(f"Compressed: {filepath}")
            if not no_backup:
                backup_path = filepath.with_name(filepath.stem + ".original.md")
                print(f"Original:   {backup_path}")
            else:
                print("Original:   (skipped, --no-backup)")
            sys.exit(0)
        else:
            print("\n❌ Compression failed after retries")
            sys.exit(2)

    except KeyboardInterrupt:
        print("\nInterrupted by user")
        sys.exit(130)

    except Exception as e:
        print(f"\n❌ Error: {e}")
        sys.exit(1)


if __name__ == "__main__":
    main()
