#!/usr/bin/env python3
"""
Caveman Compress CLI

Usage:
    caveman <filepath>
"""

import sys
import argparse
from pathlib import Path

# Add script directory to sys.path to fix direct execution ImportError
sys.path.insert(0, str(Path(__file__).resolve().parent))

from compress import compress_file
from detect import detect_file_type, should_compress


def main():
    parser = argparse.ArgumentParser(description="Caveman Compress CLI")
    parser.add_argument("filepath", type=Path, help="Markdown file to compress")
    parser.add_argument("-f", "--force", action="store_true", help="Overwrite existing backup")
    parser.add_argument("--max-size", type=int, default=500_000, help="Max file size in bytes")
    parser.add_argument("--retries", type=int, default=2, help="Validation retry budget")
    args = parser.parse_args()

    filepath = args.filepath


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
        success = compress_file(
            filepath, 
            force=args.force, 
            max_size=args.max_size, 
            max_retries=args.retries
        )

        if success:

            print("\nCompression completed successfully")
            backup_path = filepath.with_name(filepath.stem + ".original.md")
            print(f"Compressed: {filepath}")
            print(f"Original:   {backup_path}")
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
