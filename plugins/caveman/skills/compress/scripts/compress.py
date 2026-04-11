"""Core compression logic for caveman file compression.

Usage:
    python scripts/compress.py <filepath>
"""

from pathlib import Path
from typing import List

from .detect import should_compress
from .validate import validate


def call_claude(prompt: str) -> str:
    """Call Claude CLI with prompt, return stdout."""
    import subprocess

    result = subprocess.run(
        ["claude", "-p", prompt],
        capture_output=True,
        text=True,
        check=False,
    )

    if result.returncode != 0:
        raise RuntimeError(f"Claude failed: {result.stderr}")

    return result.stdout.strip()


def build_compress_prompt(original: str) -> str:
    return f"""Compress this markdown file into caveman-speak.

Preserve EXACTLY:
- code blocks
- inline code
- URLs
- file paths
- commands
- proper nouns
- dates/numbers/versions
- heading text
- list structure

Only compress natural language.
Return ONLY compressed markdown. No explanation.

FILE:

{original}
"""


def build_fix_prompt(original: str, compressed: str, errors: List[str]) -> str:
    errors_text = "\n".join(f"- {e}" for e in errors)
    return f"""You are fixing a caveman-compressed markdown file. Specific validation errors were found.

Rules:
- Fix ONLY the listed errors
- DO NOT recompress or rephrase the file
- Preserve current wording everywhere else
- Code blocks, URLs, paths, commands, headings must match original exactly where validator says broken
- Return ONLY the fixed markdown

VALIDATION ERRORS:
{errors_text}

ORIGINAL FILE:
{original}

CURRENT COMPRESSED FILE:
{compressed}

Return ONLY the fixed compressed file. No explanation.
"""


def compress_file(filepath: Path) -> bool:
    """Compress a file in place, saving backup as .original.md.

    Returns True if successful.
    """
    if filepath.stat().st_size > 500_000:
        raise ValueError(f"File too large to compress safely (max 500KB): {filepath}")

    if not should_compress(filepath):
        print(f"Skip: file type not compressible: {filepath}")
        return False

    original_text = filepath.read_text()
    compressed = call_claude(build_compress_prompt(original_text))

    # Save original as backup, write compressed to original path
    backup = filepath.with_name(f"{filepath.stem}.original.md")
    backup.write_text(original_text)
    filepath.write_text(compressed)

    print(f"Saved backup: {backup}")
    print(f"Wrote compressed file: {filepath}")

    # Validate, retry with targeted fixes if needed
    for attempt in range(3):
        result = validate(backup, filepath)
        if result.ok:
            print("Validation passed.")
            return True

        print(f"Validation failed (attempt {attempt + 1}/3):")
        for error in result.errors:
            print(f"  - {error}")

        if attempt == 2:
            print("Validation failed after 3 attempts.")
            return False

        compressed = call_claude(
            build_fix_prompt(original_text, compressed, result.errors)
        )
        filepath.write_text(compressed)
        print("Applied targeted fix, retrying validation...")

    return False
