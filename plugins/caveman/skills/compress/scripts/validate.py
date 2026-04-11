"""Validate compressed markdown preserves critical content."""

import re
from dataclasses import dataclass
from pathlib import Path
from typing import List


@dataclass
class ValidationResult:
    ok: bool
    errors: List[str]


def read_file(path: Path) -> str:
    return path.read_text()


def extract_code_blocks(text: str) -> List[str]:
    return re.findall(r"```.*?```", text, flags=re.DOTALL)


def extract_inline_code(text: str) -> List[str]:
    return re.findall(r"`[^`\n]+`", text)


def extract_urls(text: str) -> List[str]:
    return re.findall(r"https?://[^\s)]+", text)


def extract_headings(text: str) -> List[str]:
    return re.findall(r"^#+ .+$", text, flags=re.MULTILINE)


def validate(original_path: Path, compressed_path: Path) -> ValidationResult:
    orig = read_file(original_path)
    comp = read_file(compressed_path)
    errors: List[str] = []

    orig_blocks = extract_code_blocks(orig)
    comp_blocks = extract_code_blocks(comp)
    if orig_blocks != comp_blocks:
        errors.append("Code blocks changed")

    orig_inline = extract_inline_code(orig)
    comp_inline = extract_inline_code(comp)
    if orig_inline != comp_inline:
        errors.append("Inline code changed")

    orig_urls = extract_urls(orig)
    comp_urls = extract_urls(comp)
    if orig_urls != comp_urls:
        errors.append("URLs changed")

    orig_headings = extract_headings(orig)
    comp_headings = extract_headings(comp)
    if orig_headings != comp_headings:
        errors.append("Headings changed")

    return ValidationResult(ok=not errors, errors=errors)


if __name__ == "__main__":
    import sys

    if len(sys.argv) != 3:
        print("Usage: python validate.py <original> <compressed>")
        raise SystemExit(1)

    result = validate(Path(sys.argv[1]), Path(sys.argv[2]))
    if result.ok:
        print("OK")
    else:
        for error in result.errors:
            print(error)
        raise SystemExit(1)
