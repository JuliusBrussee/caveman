#!/usr/bin/env python3
import re
from collections import Counter
from pathlib import Path

URL_REGEX = re.compile(r"https?://[^\s)]+")
FENCE_OPEN_REGEX = re.compile(r"^(\s{0,3})(`{3,}|~{3,})(.*)$")
HEADING_REGEX = re.compile(r"^(#{1,6})\s+(.*)", re.MULTILINE)
BULLET_REGEX = re.compile(r"^\s*[-*+]\s+", re.MULTILINE)

# crude but effective path detection
# Requires either a path prefix (./ ../ / or drive letter) or a slash/backslash within the match
PATH_REGEX = re.compile(r"(?:\./|\.\./|/|[A-Za-z]:\\)[\w\-/\\\.]+|[\w\-\.]+[/\\][\w\-/\\\.]+")


class ValidationResult:
    def __init__(self):
        self.is_valid = True
        self.errors = []
        self.warnings = []

    def add_error(self, msg):
        self.is_valid = False
        self.errors.append(msg)

    def add_warning(self, msg):
        self.warnings.append(msg)


def read_file(path: Path) -> str:
    return path.read_text(errors="ignore")


# ---------- Extractors ----------


def extract_headings(text):
    return [(level, title.strip()) for level, title in HEADING_REGEX.findall(text)]


def _is_blank(line):
    return line.strip() == ""


def _is_indented_code(line):
    """A line is indented-code content if it starts with a tab or >=4 spaces."""
    if line.startswith("\t"):
        return True
    return len(line) - len(line.lstrip(" ")) >= 4


def _scan_code_blocks(lines):
    """Walk the lines once and return a list of (start, end_exclusive, kind).

    Covers both block forms the validator must preserve verbatim:

    - Fenced blocks: ``` / ~~~ with variable length (CommonMark — the closing
      fence uses the same char and is at least as long as the opening one).
      Nested fences work because a longer outer fence is not closed by a
      shorter inner one.
    - Indented blocks: runs of lines indented by a tab or >=4 spaces, separated
      only by blank lines. Indented code cannot interrupt a paragraph, so a run
      only starts when the preceding non-blank line is blank (or it is the
      first content). Trailing blank lines are not part of the block.
    """
    spans = []
    i = 0
    n = len(lines)
    while i < n:
        m = FENCE_OPEN_REGEX.match(lines[i])
        if m:
            fence_char = m.group(2)[0]
            fence_len = len(m.group(2))
            start = i
            i += 1
            closed = False
            while i < n:
                close_m = FENCE_OPEN_REGEX.match(lines[i])
                if (
                    close_m
                    and close_m.group(2)[0] == fence_char
                    and len(close_m.group(2)) >= fence_len
                    and close_m.group(3).strip() == ""
                ):
                    i += 1
                    closed = True
                    break
                i += 1
            if closed:
                spans.append((start, i, "fenced"))
            # Unclosed fences are silently skipped — they indicate malformed
            # markdown and including them would cause false-positive failures.
            continue

        # Indented code block: must not interrupt a paragraph, so the line
        # immediately before the block must be blank (or this is the first line).
        prev_is_blank = (i == 0) or _is_blank(lines[i - 1])
        if _is_indented_code(lines[i]) and not _is_blank(lines[i]) and prev_is_blank:
            start = i
            last_content = i
            i += 1
            while i < n and (_is_blank(lines[i]) or _is_indented_code(lines[i])):
                if not _is_blank(lines[i]):
                    last_content = i
                i += 1
            spans.append((start, last_content + 1, "indented"))
            continue

        i += 1
    return spans


def extract_code_blocks(text):
    """Extract verbatim code blocks (fenced and indented) for preservation.

    Handles ``` / ~~~ fences with variable length and nesting, plus 4-space /
    tab indented code blocks. Each block is returned as its exact source text.
    """
    lines = text.split("\n")
    return ["\n".join(lines[start:end]) for start, end, _ in _scan_code_blocks(lines)]


def extract_urls(text):
    return set(URL_REGEX.findall(text))


def extract_paths(text):
    return set(PATH_REGEX.findall(text))


def count_bullets(text):
    return len(BULLET_REGEX.findall(text))


def extract_inline_codes(text):
    """Find inline `code` spans, excluding anything inside a code block.

    Strips every code-block line (variable-length fenced blocks AND indented
    blocks) before scanning, so backticks inside e.g. a ```` ````-fenced block
    or a 4-space-indented block are not mistaken for inline code — which would
    otherwise cause false validation failures.
    """
    lines = text.split("\n")
    block_lines = set()
    for start, end, _ in _scan_code_blocks(lines):
        block_lines.update(range(start, end))
    kept = [line for idx, line in enumerate(lines) if idx not in block_lines]
    return re.findall(r"`([^`]+)`", "\n".join(kept))


# ---------- Validators ----------


def validate_headings(orig, comp, result):
    h1 = extract_headings(orig)
    h2 = extract_headings(comp)

    # Headings must be preserved exactly — anchors, TOCs, and cross-links
    # depend on the exact text and order. A count mismatch and a text/order
    # change are both hard errors; the elif keeps a count mismatch from also
    # emitting the (now redundant) text-change error.
    if len(h1) != len(h2):
        result.add_error(f"Heading count mismatch: {len(h1)} vs {len(h2)}")
    elif h1 != h2:
        result.add_error("Heading text/order changed")


def validate_code_blocks(orig, comp, result):
    c1 = extract_code_blocks(orig)
    c2 = extract_code_blocks(comp)

    if c1 != c2:
        result.add_error("Code blocks not preserved exactly")


def validate_urls(orig, comp, result):
    u1 = extract_urls(orig)
    u2 = extract_urls(comp)

    if u1 != u2:
        result.add_error(f"URL mismatch: lost={u1 - u2}, added={u2 - u1}")


def validate_paths(orig, comp, result):
    p1 = extract_paths(orig)
    p2 = extract_paths(comp)

    # File paths referenced in the original must survive compression — dropping
    # or rewriting one silently breaks what the doc points at, so a LOST path is
    # a hard error. Added matches are kept as a warning: PATH_REGEX is broad and
    # caveman prose like "pros/cons" or "req/min" can coincidentally match, so
    # an extra match in the compressed output is noise, not path loss.
    lost = p1 - p2
    added = p2 - p1
    if lost:
        result.add_error(f"File path(s) lost in compression: {lost}")
    if added:
        result.add_warning(f"Path-like tokens added (possibly prose): {added}")


def validate_bullets(orig, comp, result):
    b1 = count_bullets(orig)
    b2 = count_bullets(comp)

    if b1 == 0:
        return

    diff = abs(b1 - b2) / b1

    if diff > 0.15:
        result.add_warning(f"Bullet count changed too much: {b1} -> {b2}")


def validate_inline_codes(orig, comp, result):
    c1 = Counter(extract_inline_codes(orig))
    c2 = Counter(extract_inline_codes(comp))

    if c1 != c2:
        lost = set(c1.keys()) - set(c2.keys())
        added = set(c2.keys()) - set(c1.keys())
        for code, count in c1.items():
            if code in c2 and c2[code] < count:
                lost.add(f"{code} (lost {count - c2[code]} of {count} occurrences)")
        if lost:
            result.add_error(f"Inline code lost: {lost}")
        if added:
            result.add_warning(f"Inline code added: {added}")


# ---------- Main ----------


def validate(original_path: Path, compressed_path: Path) -> ValidationResult:
    result = ValidationResult()

    orig = read_file(original_path)
    comp = read_file(compressed_path)

    validate_headings(orig, comp, result)
    validate_code_blocks(orig, comp, result)
    validate_urls(orig, comp, result)
    validate_paths(orig, comp, result)
    validate_bullets(orig, comp, result)
    validate_inline_codes(orig, comp, result)

    return result


# ---------- CLI ----------

if __name__ == "__main__":
    import sys

    if len(sys.argv) != 3:
        print("Usage: python validate.py <original> <compressed>")
        sys.exit(1)

    orig = Path(sys.argv[1]).resolve()
    comp = Path(sys.argv[2]).resolve()

    res = validate(orig, comp)

    print(f"\nValid: {res.is_valid}")

    if res.errors:
        print("\nErrors:")
        for e in res.errors:
            print(f"  - {e}")

    if res.warnings:
        print("\nWarnings:")
        for w in res.warnings:
            print(f"  - {w}")
