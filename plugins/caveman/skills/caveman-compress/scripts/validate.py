#!/usr/bin/env python3
import re
from collections import Counter
from pathlib import Path

URL_REGEX = re.compile(r"https?://[^\s)]+")
FENCE_OPEN_REGEX = re.compile(r"^(\s{0,3})(`{3,}|~{3,})(.*)$")

# A line that is nothing but a fence marker plus an optional info string, at ANY
# indentation. Used ONLY to scrub leaked markers before inline-code pairing (see
# extract_inline_codes) — never for block extraction.
#
# Widening FENCE_OPEN_REGEX itself to `\s*` looks like the obvious fix for #820
# and is a net regression: a lone indented ``` (the natural way to SHOW a fence
# inside prose) then opens a block that runs to EOF, swallowing real code blocks
# and silently removing their inline spans from validation. That turns a
# false-failure bug into a false-PASS bug, and a false PASS overwrites the
# user's file with unvalidated output.
FENCE_MARKER_LINE_REGEX = re.compile(r"^\s*(?:`{3,}|~{3,})[^`~]*$")

# Cap on how much of a lost/added span is echoed in an error message. Unpaired
# backticks can make a "span" hundreds of characters of prose; printing it whole
# is what made #820's failures undiagnosable.
MAX_REPORTED_SPAN = 60
HEADING_REGEX = re.compile(r"^(#{1,6})\s+(.*)", re.MULTILINE)
BULLET_REGEX = re.compile(r"^\s*[-*+]\s+", re.MULTILINE)
# Any list item, ordered or not. Four spaces inside a list item is the item's
# content indentation, never an indented code block.
LIST_ITEM_REGEX = re.compile(r"^\s*(?:[-*+]|\d+[.)])\s")

# crude but effective path detection
# Requires either a path prefix (./ ../ / or drive letter) or a slash/backslash within the match
PATH_REGEX = re.compile(r"(?:\./|\.\./|/|[A-Za-z]:\\)[\w\-/\\\.]+|[\w\-\.]+[/\\][\w\-/\\\.]+")

# PATH_REGEX is crude on purpose and also matches ordinary prose pairs —
# "pros/cons", "Node/browser", "state/lifecycle". Caveman prose ADDS those
# constructions freely and dropping one breaks nothing, so only an unambiguous
# path — a leading ./ ../ / or drive letter, or a dotted filename in the last
# component — is treated as a hard loss.
DEFINITE_PATH_REGEX = re.compile(r"^(?:\./|\.\./|/|[A-Za-z]:\\)|[^/\\]*\.[A-Za-z0-9]{1,8}$")


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
    return path.read_text(encoding="utf-8")


# ---------- Extractors ----------


def extract_headings(text):
    return [(level, title.strip()) for level, title in HEADING_REGEX.findall(text)]


def extract_code_blocks(text):
    """Line-based fenced code block extractor.

    Handles ``` and ~~~ fences with variable length (CommonMark: closing
    fence must use same char and be at least as long as opening). Supports
    nested fences (e.g. an outer 4-backtick block wrapping inner 3-backtick
    content).
    """
    blocks = []
    lines = text.split("\n")
    i = 0
    n = len(lines)
    while i < n:
        m = FENCE_OPEN_REGEX.match(lines[i])
        if not m:
            i += 1
            continue
        fence_char = m.group(2)[0]
        fence_len = len(m.group(2))
        open_line = lines[i]
        block_lines = [open_line]
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
                block_lines.append(lines[i])
                closed = True
                i += 1
                break
            block_lines.append(lines[i])
            i += 1
        if closed:
            blocks.append("\n".join(block_lines))
        # Unclosed fences are silently skipped — they indicate malformed markdown
        # and including them would cause false-positive validation failures.
    return blocks + extract_indented_code_blocks(text)


def extract_indented_code_blocks(text):
    """CommonMark indented code blocks — 4-space-indented runs outside any fence.

    Without these, `    kubectl delete pod --all -n prod` was prose to the
    validator: "code blocks preserved exactly" compared empty to empty and
    PASSED while the compressor rewrote the command to
    `kubectl delete pod -n dev`. A clean pass on a mutated destructive command
    is the worst failure this tool has, because it overwrites the user's file.

    Deliberately conservative about lists: inside a list item, four spaces are
    the item's content indentation, not code, and nested bullets are ordinary
    prose the compressor SHOULD rewrite. A run is only treated as code when the
    document is not inside a list and the run is preceded by a blank line — so
    this adds detections, it never turns existing passes into false failures.
    """
    blocks = []
    lines = text.split("\n")
    fenced = set()
    for block in extract_fenced_spans(lines):
        fenced.update(block)
    in_list = False
    previous_blank = True
    i = 0
    n = len(lines)
    while i < n:
        line = lines[i]
        stripped = line.strip()
        if i in fenced:
            in_list, previous_blank = in_list, False
            i += 1
            continue
        if not stripped:
            previous_blank = True
            i += 1
            continue
        indent = len(line) - len(line.lstrip(" \t"))
        if LIST_ITEM_REGEX.match(line):
            in_list = True
        elif indent == 0:
            in_list = False
        if not in_list and previous_blank and indent >= 4:
            run = []
            while i < n and i not in fenced:
                current = lines[i]
                if not current.strip():
                    # A blank line continues an indented block only if more
                    # indented content follows.
                    lookahead = i + 1
                    while lookahead < n and not lines[lookahead].strip():
                        lookahead += 1
                    if lookahead < n and lookahead not in fenced and \
                            len(lines[lookahead]) - len(lines[lookahead].lstrip(" \t")) >= 4:
                        run.extend(lines[i:lookahead])
                        i = lookahead
                        continue
                    break
                if len(current) - len(current.lstrip(" \t")) < 4:
                    break
                run.append(current)
                i += 1
            if run:
                blocks.append("\n".join(run))
            previous_blank = False
            continue
        previous_blank = False
        i += 1
    return blocks


def extract_fenced_spans(lines):
    """Line-index ranges covered by fenced blocks, so indented-code detection
    never reaches inside one."""
    spans = []
    i = 0
    n = len(lines)
    while i < n:
        m = FENCE_OPEN_REGEX.match(lines[i])
        if not m:
            i += 1
            continue
        fence_char = m.group(2)[0]
        fence_len = len(m.group(2))
        start = i
        i += 1
        while i < n:
            close_m = FENCE_OPEN_REGEX.match(lines[i])
            if (
                close_m
                and close_m.group(2)[0] == fence_char
                and len(close_m.group(2)) >= fence_len
                and close_m.group(3).strip() == ""
            ):
                i += 1
                break
            i += 1
        spans.append(range(start, i))
    return spans


def extract_urls(text):
    return set(URL_REGEX.findall(text))


def extract_paths(text):
    return set(PATH_REGEX.findall(text))


def count_bullets(text):
    return len(BULLET_REGEX.findall(text))


def extract_inline_codes(text):
    """Backtick-delimited inline spans, with fenced code blocks stripped first.

    Previously used a column-0-anchored regex to strip fences, which misses
    fences indented 1-3 spaces (valid CommonMark). Reuse extract_code_blocks
    (FENCE_OPEN_REGEX-based, indentation-aware) instead so an indented fence's
    body backticks don't leak into inline-code pairing.

    Any fence-marker line that survives that pass is then blanked (#820). A
    fence indented 4+ spaces — what you get from showing an example inside a
    bullet — is not matched by FENCE_OPEN_REGEX, so extract_code_blocks does
    not remove it and its OWN backticks used to leak in and shift the pairing
    of every following span, making the file permanently uncompressible.
    Blanking just the marker lines fixes that without removing any prose, and
    cannot run away the way a widened fence opener does.

    The span pattern deliberately still spans newlines. CommonMark permits a
    line ending inside a code span and hard-wrapped markdown produces them, so
    a single-line pattern silently drops real spans — which downgrades a
    deleted or mutated span from error to PASS. Long/garbled spans are a
    presentation problem, handled by truncating in the error message instead.
    """
    text_without_fences = text
    for block in extract_code_blocks(text):
        text_without_fences = text_without_fences.replace(block, "", 1)
    text_without_fences = "\n".join(
        "" if FENCE_MARKER_LINE_REGEX.match(line) else line
        for line in text_without_fences.split("\n")
    )
    return re.findall(r"`([^`]+)`", text_without_fences)


# ---------- Validators ----------


def validate_headings(orig, comp, result):
    h1 = extract_headings(orig)
    h2 = extract_headings(comp)

    # Changed heading TEXT is an error, not a warning. Every in-document anchor
    # link points at a heading's slug, so renaming "# Configuration Options" to
    # "# Config" silently breaks all of them — and SKILL.md and CLAUDE.md both
    # state headings are preserved. Only counts used to gate the overwrite, so
    # a run that renamed every heading reported "Validation passed". A level-only
    # change keeps every slug intact and stays a warning.
    if len(h1) != len(h2):
        result.add_error(f"Heading count mismatch: {len(h1)} vs {len(h2)}")
        return

    t1 = [text for _, text in h1]
    t2 = [text for _, text in h2]
    if t1 != t2:
        lost = [t for t in t1 if t not in t2]
        added = [t for t in t2 if t not in t1]
        result.add_error(f"Heading text/order changed: lost={lost}, added={added}")
    elif h1 != h2:
        # Same text, different level. The outline moved but no anchor broke —
        # slugs come from the text, so links still resolve.
        result.add_warning("Heading levels changed")


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
    """File paths are preserved — an error, never a warning.

    This validator never called add_error at all, so a compressed file that had
    dropped `src/hooks/caveman-config.js` still reported "Validation passed" and
    the in-place overwrite stood. SKILL.md and CLAUDE.md both promise paths
    survive compression; nothing enforced it.
    """
    p1 = extract_paths(orig)
    p2 = extract_paths(comp)
    lost = p1 - p2
    added = p2 - p1

    definite = {p for p in lost if DEFINITE_PATH_REGEX.search(p)}
    if definite:
        result.add_error(f"File paths lost: {sorted(definite)}")
    if (lost - definite) or added:
        result.add_warning(f"Path mismatch: lost={sorted(lost)}, added={sorted(added)}")


def validate_bullets(orig, comp, result):
    b1 = count_bullets(orig)
    b2 = count_bullets(comp)

    if b1 == 0:
        return

    diff = abs(b1 - b2) / b1

    if diff > 0.15:
        result.add_warning(f"Bullet count changed too much: {b1} -> {b2}")


def validate_inline_codes(orig, comp, result):
    def _render_spans(spans):
        """Render spans for an error message, truncated and newline-escaped.

        A span may legitimately contain newlines, and an unpaired backtick can
        make one hundreds of characters of prose. Printing those whole is what
        made #820's failures undiagnosable — but the fix belongs here, in
        presentation, not in what counts as a span.
        """
        out = []
        for span in sorted(spans):
            flat = span.replace("\n", "\\n")
            if len(flat) > MAX_REPORTED_SPAN:
                flat = flat[:MAX_REPORTED_SPAN] + "…"
            out.append(repr(flat))
        return "{" + ", ".join(out) + "}"

    c1 = Counter(extract_inline_codes(orig))
    c2 = Counter(extract_inline_codes(comp))

    if c1 != c2:
        lost = set(c1.keys()) - set(c2.keys())
        added = set(c2.keys()) - set(c1.keys())
        for code, count in c1.items():
            if code in c2 and c2[code] < count:
                lost.add(f"{code} (lost {count - c2[code]} of {count} occurrences)")
        if lost:
            result.add_error(f"Inline code lost: {_render_spans(lost)}")
        if added:
            result.add_warning(f"Inline code added: {_render_spans(added)}")


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
