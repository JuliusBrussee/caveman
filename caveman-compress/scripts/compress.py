#!/usr/bin/env python3
"""
Caveman Memory Compression Orchestrator

Usage:
    python scripts/compress.py <filepath>
"""

import os
import re
import subprocess
from pathlib import Path
from typing import List

OUTER_FENCE_REGEX = re.compile(
    r"\A\s*(`{3,}|~{3,})[^\n]*\n(.*)\n\1\s*\Z", re.DOTALL
)


NOCOMPRESS_REGEX = re.compile(
    r"<!--\s*nocompress\s*-->(.*?)<!--\s*/nocompress\s*-->",
    re.DOTALL | re.IGNORECASE,
)

PLACEHOLDER = "<!--NOCOMPRESS_{index}-->"


def strip_llm_wrapper(text: str) -> str:
    """Strip outer ```markdown ... ``` fence when it wraps the entire output."""
    m = OUTER_FENCE_REGEX.match(text)
    if m:
        return m.group(2)
    return text

from .detect import should_compress
from .validate import validate

MAX_RETRIES = 2


# ---------- Nocompress Helpers ----------


def extract_nocompress(text: str) -> tuple[str, list[str]]:
    """Replace <!-- nocompress -->...<!-- /nocompress --> blocks with placeholders.

    Returns (text_with_placeholders, list_of_extracted_regions).
    The extracted regions are spliced back verbatim after compression so Claude
    never sees them — guaranteeing exact preservation regardless of content.
    """
    regions: list[str] = []

    def replace(m: re.Match) -> str:
        regions.append(m.group(0))
        return PLACEHOLDER.format(index=len(regions) - 1)

    return NOCOMPRESS_REGEX.sub(replace, text), regions


def restore_nocompress(text: str, regions: list[str]) -> str:
    """Splice extracted nocompress regions back by placeholder index."""
    for i, region in enumerate(regions):
        text = text.replace(PLACEHOLDER.format(index=i), region)
    return text


# ---------- Claude Calls ----------


def call_claude(prompt: str) -> str:
    api_key = os.environ.get("ANTHROPIC_API_KEY")
    if api_key:
        try:
            import anthropic

            client = anthropic.Anthropic(api_key=api_key)
            msg = client.messages.create(
                model=os.environ.get("CAVEMAN_MODEL", "claude-sonnet-4-5"),
                max_tokens=8192,
                messages=[{"role": "user", "content": prompt}],
            )
            return strip_llm_wrapper(msg.content[0].text.strip())
        except ImportError:
            pass  # anthropic not installed, fall back to CLI
    # Fallback: use claude CLI (handles desktop auth)
    try:
        result = subprocess.run(
            ["claude", "--print"],
            input=prompt,
            text=True,
            capture_output=True,
            check=True,
        )
        return strip_llm_wrapper(result.stdout.strip())
    except subprocess.CalledProcessError as e:
        raise RuntimeError(f"Claude call failed:\n{e.stderr}")


def build_compress_prompt(original: str) -> str:
    return f"""
Compress this markdown into caveman format.

STRICT RULES:
- Do NOT modify anything inside ``` code blocks
- Do NOT modify anything inside inline backticks
- Do NOT add new ``` fences around content that is not already fenced — even if it looks like code or JSON
- Do NOT create or promote text to markdown headings — only preserve existing # headings exactly as-is
- Do NOT convert XML-like tags (e.g. <section>, <foo>) into headings or any other markdown structure — leave them as plain text
- Preserve ALL URLs exactly
- Preserve ALL headings exactly (do not add, remove, or reword any heading)
- Preserve file paths and commands
- Return ONLY the compressed markdown body — do NOT wrap the entire output in a ```markdown fence or any other fence. Inner code blocks from the original stay as-is; do not add a new outer fence around the whole file.

Only compress natural language prose. Never restructure, add formatting, or improve organization.

TEXT:
{original}
"""


def build_fix_prompt(original: str, compressed: str, errors: List[str]) -> str:
    errors_str = "\n".join(f"- {e}" for e in errors)
    return f"""You are fixing a caveman-compressed markdown file. Specific validation errors were found.

CRITICAL RULES:
- DO NOT recompress or rephrase the file
- ONLY fix the listed errors — leave everything else exactly as-is
- The ORIGINAL is provided as reference only (to restore missing content)
- Preserve caveman style in all untouched sections

ERRORS TO FIX:
{errors_str}

HOW TO FIX:
- Missing URL: find it in ORIGINAL, restore it exactly where it belongs in COMPRESSED
- Code block mismatch: find the exact code block in ORIGINAL, restore it in COMPRESSED
- Heading mismatch: restore the exact heading text from ORIGINAL into COMPRESSED
- Do not touch any section not mentioned in the errors

ORIGINAL (reference only):
{original}

COMPRESSED (fix this):
{compressed}

Return ONLY the fixed compressed file. No explanation.
"""


# ---------- Core Logic ----------


def compress_file(filepath: Path) -> bool:
    # Resolve and validate path
    filepath = filepath.resolve()
    MAX_FILE_SIZE = 500_000  # 500KB
    if not filepath.exists():
        raise FileNotFoundError(f"File not found: {filepath}")
    if filepath.stat().st_size > MAX_FILE_SIZE:
        raise ValueError(f"File too large to compress safely (max 500KB): {filepath}")

    print(f"Processing: {filepath}")

    if not should_compress(filepath):
        print("Skipping (not natural language)")
        return False

    original_text = filepath.read_text(errors="ignore")
    backup_path = filepath.with_name(filepath.stem + ".original.md")

    # Check if backup already exists to prevent accidental overwriting
    if backup_path.exists():
        print(f"⚠️ Backup file already exists: {backup_path}")
        print("The original backup may contain important content.")
        print("Aborting to prevent data loss. Please remove or rename the backup file if you want to proceed.")
        return False

    # Step 1: Extract nocompress regions, compress the rest
    sendable, nocompress_regions = extract_nocompress(original_text)
    if nocompress_regions:
        print(f"  ({len(nocompress_regions)} nocompress region(s) preserved verbatim)")
    print("Compressing with Claude...")
    compressed_partial = call_claude(build_compress_prompt(sendable))
    compressed = restore_nocompress(compressed_partial, nocompress_regions)

    # Save original as backup, write compressed to original path
    backup_path.write_text(original_text)
    filepath.write_text(compressed)

    # Step 2: Validate + Retry
    for attempt in range(MAX_RETRIES):
        print(f"\nValidation attempt {attempt + 1}")

        result = validate(backup_path, filepath)

        if result.is_valid:
            print("Validation passed")
            break

        print("❌ Validation failed:")
        for err in result.errors:
            print(f"   - {err}")

        if attempt == MAX_RETRIES - 1:
            # Restore original on failure
            filepath.write_text(original_text)
            backup_path.unlink(missing_ok=True)
            print("❌ Failed after retries — original restored")
            return False

        print("Fixing with Claude...")
        fixed_partial = call_claude(
            build_fix_prompt(sendable, compressed_partial, result.errors)
        )
        compressed_partial = fixed_partial
        compressed = restore_nocompress(compressed_partial, nocompress_regions)
        filepath.write_text(compressed)

    return True