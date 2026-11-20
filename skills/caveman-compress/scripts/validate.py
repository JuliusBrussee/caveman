#!/usr/bin/env python3
import re
from collections import Counter
from pathlib import Path

URL_REGEX = re.compile(r"https?://[^\s)`]+")  # a backtick is never part of a URL
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

# Numeric values, for the "never modify" contract SKILL.md states and the skill
# README repeats ("Dates, version numbers, numeric values"). One token covers
# plain integers, decimals, versions and thousands-separated figures: 3, 0.15,
# 4.0.1, 1,000. A date's parts (2026-11-17) come out as 2026/11/17, which is
# what keeps each component from being rewritten on its own.
# Digits touching letters are part of an identifier, not a value: a11y, i18n,
# utf8, sha256, x86_64. The committed fixture pair compresses "accessibility"
# to "a11y", so counting those digits reported an invented 11.
# The lookbehind excludes a digit as well as a letter: without that, matching
# could start PAST the letter and still succeed — "sha256" yielded "56", since
# the 5 is preceded by a digit and the 6 ends the word.
#
# Because the token must START at the digits, trailing letters are safe to
# absorb as a unit: "60s", "5ms", "2x", "64bit" are values, while "a11y" and
# "utf8" are already excluded by the lookbehind. The unit is matched and
# discarded so only the digits are compared — and matching it is what makes
# the boundary whole-token. A bare `(?!\w)` after the digits instead let
# backtracking shorten the run until the guard passed, so "60s" read as "6".
NUMBER_REGEX = re.compile(r"(?<![A-Za-z_\d])(\d+(?:[.,]\d+)*)[A-Za-z]*(?!\w)")
# ...which would also drop the v off a version. Removed before matching so
# v4.0.1 is still read as the value 4.0.1. `\b` keeps it from firing inside a
# word, so "rev2" is untouched.
VERSION_PREFIX_REGEX = re.compile(r"\bv(?=\d)", re.IGNORECASE)
# An ordered-list marker is structure, not data: the 1 in "1. step" numbers the
# item, it does not state a value. Blanked before extraction so re-numbering a
# list — or writing prose bullets as an ordered list — reports itself through
# the list/bullet checks instead of as an invented number.
ORDERED_MARKER_REGEX = re.compile(r"^(\s*)\d+([.)])(\s)", re.MULTILINE)

# Writing a spelled-out number as a digit is compression that PRESERVES the
# value, and caveman prose does it constantly — the committed fixture pair
# turns "about two years" into "2 years" and "three small PRs than one large"
# into "3 small > 1 large". So the original's permitted set includes the digits
# for any number word it spells out. Only that side is widened: the compressed
# side stays literal, so `at most 3 attempts` → `max 5 attempts` is still an
# error. Ordinals are here for the same reason ("third" → "3rd").
WORD_NUMBERS = {
    "zero": "0", "one": "1", "two": "2", "three": "3", "four": "4", "five": "5",
    "six": "6", "seven": "7", "eight": "8", "nine": "9", "ten": "10",
    "eleven": "11", "twelve": "12", "thirteen": "13", "fourteen": "14",
    "fifteen": "15", "sixteen": "16", "seventeen": "17", "eighteen": "18",
    "nineteen": "19", "twenty": "20", "thirty": "30", "forty": "40",
    "fifty": "50", "sixty": "60", "seventy": "70", "eighty": "80",
    "ninety": "90", "hundred": "100", "thousand": "1000", "million": "1000000",
    "first": "1", "second": "2", "third": "3", "fourth": "4", "fifth": "5",
    "sixth": "6", "seventh": "7", "eighth": "8", "ninth": "9", "tenth": "10",
}
WORD_NUMBER_REGEX = re.compile(r"\b(" + "|".join(WORD_NUMBERS) + r")\b", re.IGNORECASE)
# A comma between a digit and exactly three digits is a thousands separator, so
# 1,000 and 1000 are the same value written two ways. Normalizing it away is
# deliberate: flagging pure reformatting would add noise and protect no value.
THOUSANDS_SEPARATOR_REGEX = re.compile(r"(?<=\d),(?=\d{3}(?!\d))")

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

    Merges fenced and indented blocks by their DOCUMENT POSITION, not by
    type. validate_code_blocks compares this list positionally (c1 != c2),
    so a type-grouped return (all fenced, then all indented) makes two
    blocks that swapped RELATIVE order in the document produce the same
    list on both sides: the comparison passes while the invariant it
    exists to check (block order preserved) has actually been violated.
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
        start = i
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
            blocks.append((start, "\n".join(block_lines)))
        # Unclosed fences are silently skipped — they indicate malformed markdown
        # and including them would cause false-positive validation failures.
    ordered = sorted(blocks + extract_indented_code_blocks(text), key=lambda pair: pair[0])
    return [block_text for _, block_text in ordered]


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

    Returns (start_line, text) pairs rather than bare text: extract_code_blocks
    needs each block's document position to merge it with fenced blocks in
    order (see that function's docstring).
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
            start = i
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
                blocks.append((start, "\n".join(run)))
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


# Sentence punctuation right after a URL or path ("see docs/a.md.") is matched
#  by both regexes but is not part of it; compression often moves it.
TRAILING_PUNCTUATION = ".,;:!?"


def extract_urls(text):
    return {url.rstrip(TRAILING_PUNCTUATION) for url in URL_REGEX.findall(text)}


def extract_paths(text):
    return {path.rstrip(TRAILING_PUNCTUATION) for path in PATH_REGEX.findall(text)}


def count_bullets(text):
    return len(BULLET_REGEX.findall(text))


def extract_numbers(text):
    """Every numeric value in the text, thousands separators normalized away.

    Code blocks, inline spans, URLs and paths are deliberately NOT masked out
    first. Each of those is required to survive byte-for-byte and is checked
    exactly by its own validator, so it contributes the same numbers to both
    sides and can never raise a false error here. Masking them would instead
    CREATE one: a value the model moved out of an inline span would look newly
    invented, reporting the wrong defect for a loss already caught elsewhere.
    """
    scrubbed = VERSION_PREFIX_REGEX.sub(" ", ORDERED_MARKER_REGEX.sub(r"\1\2\3", text))
    return {
        THOUSANDS_SEPARATOR_REGEX.sub("", match)
        for match in NUMBER_REGEX.findall(scrubbed)
    }


def spelled_numbers(text):
    """Digits for the number words the text spells out ("three" -> "3").

    Widens only what the ORIGINAL permits, never what the compressed side
    claims, so the check stays one-directional.
    """
    return {WORD_NUMBERS[match.group(1).lower()] for match in WORD_NUMBER_REGEX.finditer(text)}


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
    # Run-aware pairing (CommonMark): a span opens with a backtick run and
    # closes with an equal-length run. A mid-line ``` in prose ("inside a
    # ```diff fence") has no equal closer, so it matches nothing — instead of
    # its trailing backtick shifting the pairing of every following span.
    # The opener is the WHOLE run (a lookahead refuses one more backtick), so a lone ``` can never
    # shrink to a single backtick and pair with the next span. The content may
    # hold a run of a different length (``a`b``), so it is "anything, lazily"
    # up to the first equal run, not "anything but a backtick"; DOTALL keeps
    # hard-wrapped spans in the comparison set.
    return [
        m.group(2)
        # \x60 is a backtick: spelled out so no "!" sits before a literal one.
        for m in re.finditer(r"(?<!\x60)(\x60+)(?!\x60)(.+?)(?<!\x60)\1(?!\x60)", text_without_fences, re.DOTALL)
    ]


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


def validate_numbers(orig, comp, result):
    """A numeric value in the compressed text that is not in the original.

    One-directional on purpose. Compression is allowed to drop prose, so a
    number that disappears along with its sentence is not a violation and is
    not reported — the many-numbers case (a changelog, a table of limits)
    would otherwise fail on every legitimate shortening. What compression may
    never do is invent or alter a value, and that is exactly what the opposite
    direction detects: `at most 3 attempts` coming back as `max 5 attempts`
    puts a 5 in the output that the input never had (#1350).
    """
    allowed = extract_numbers(orig) | spelled_numbers(orig)
    invented = sorted(extract_numbers(comp) - allowed)
    if invented:
        result.add_error(
            f"Numeric values not in the original: {', '.join(invented)} "
            "(dates, versions and numbers must be preserved exactly)"
        )


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
    validate_numbers(orig, comp, result)

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
