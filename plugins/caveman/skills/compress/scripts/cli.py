"""CLI wrapper for caveman file compression."""

import sys
from pathlib import Path

from .compress import compress_file
from .detect import detect_file_type, should_compress


def main():
    if len(sys.argv) != 2:
        print("Usage: python3 -m scripts <filepath>")
        sys.exit(1)

    filepath = Path(sys.argv[1]).resolve()

    if not filepath.exists():
        print(f"Error: file not found: {filepath}")
        sys.exit(1)

    file_type = detect_file_type(filepath)
    print(f"File: {filepath}")
    print(f"Type: {file_type}")

    if filepath.name.endswith(".original.md"):
        print("Skip: .original.md backup files should not be compressed")
        sys.exit(0)

    # Check if compressible
    if not should_compress(filepath):
        print(f"Skip: {filepath.name} is not a natural language file")
        sys.exit(0)

    print("Starting caveman compression...\n")

    try:
        success = compress_file(filepath)
        sys.exit(0 if success else 1)
    except Exception as e:
        print(f"Error: {e}")
        sys.exit(1)


if __name__ == "__main__":
    main()
