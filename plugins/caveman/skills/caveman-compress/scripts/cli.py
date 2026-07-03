#!/usr/bin/env python3
"""
Caveman Compress CLI

Usage:
    caveman <filepath>
"""

import sys

# Force UTF-8 on stdout/stderr before any code can print. Windows consoles
# default to cp1252 and crash on the ❌ glyphs in error/validation branches,
# masking the real error and leaving the user with a half-compressed file.
for _stream in (sys.stdout, sys.stderr):
    reconfigure = getattr(_stream, "reconfigure", None)
    if callable(reconfigure):
        try:
            reconfigure(encoding="utf-8", errors="replace")
        except Exception:
            pass

from pathlib import Path

import os
import tempfile

from .compress import backup_dir_for, compress_file
from .detect import detect_file_type, should_compress


def print_usage():
    print("Usage: caveman <filepath>")
    print("       caveman --undo <filepath>   restore the pre-compression backup")


def _atomic_write_bytes(path: Path, data: bytes) -> None:
    """Write bytes via temp file + os.replace so a failure never truncates."""
    fd, tmp = tempfile.mkstemp(dir=str(path.parent), prefix=path.name + ".", suffix=".tmp")
    try:
        with os.fdopen(fd, "wb") as fh:
            fh.write(data)
            fh.flush()
            os.fsync(fh.fileno())
        os.replace(tmp, path)
    except BaseException:
        try:
            os.unlink(tmp)
        except OSError:
            pass
        raise


def undo_file(filepath: Path) -> int:
    """Restore <file> from its pre-compression backup and remove the backup.

    Looks in the current out-of-tree backup dir first (issue #420 layout),
    then falls back to the legacy in-tree `<file>.original.md` sibling.
    Removing the backup after a verified restore matters: compress_file
    refuses to run while a backup exists, so a kept backup would block the
    next compression of the same file.
    """
    filepath = filepath.resolve()
    candidates = [
        backup_dir_for(filepath) / (filepath.stem + ".original.md"),
        filepath.parent / (filepath.stem + ".original.md"),
    ]
    backup_path = next((p for p in candidates if p.is_file()), None)
    if backup_path is None:
        print(f"❌ No backup found for {filepath}")
        print("   Looked in:")
        for p in candidates:
            print(f"   - {p}")
        print("   (Backups are created by compression; nothing to undo.)")
        return 1

    data = backup_path.read_bytes()
    _atomic_write_bytes(filepath, data)
    if filepath.read_bytes() != data:
        print(f"❌ Restore verification failed for {filepath} — backup kept at {backup_path}")
        return 2
    try:
        backup_path.unlink()
    except OSError:
        print(f"⚠️ Restored, but could not remove backup: {backup_path}")
        print("   Remove it manually — compression refuses to run while it exists.")
        return 0
    print(f"Restored: {filepath}")
    print(f"Removed backup: {backup_path}")
    return 0


def main():
    args = [a for a in sys.argv[1:] if a != "--undo"]
    undo = "--undo" in sys.argv[1:]
    if len(args) != 1:
        print_usage()
        sys.exit(1)

    if undo:
        target = Path(args[0])
        if not target.is_file():
            print(f"❌ File not found: {target}")
            sys.exit(1)
        sys.exit(undo_file(target))

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
        success = compress_file(filepath)

        if success:
            print("\nCompression completed successfully")
            backup_path = backup_dir_for(filepath) / (filepath.stem + ".original.md")
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
