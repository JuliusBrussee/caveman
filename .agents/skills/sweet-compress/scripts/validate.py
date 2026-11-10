#!/usr/bin/env python3
import re
from collections import Counter
from pathlib import Path

URL_REGEX = re.compile(r"https?://[^\s)]+")
FENCE_OPEN_REGEX = re.compile(r"^(\s{0,3})(`{3,}|~{3,})(.*)$")
HEADING_REGEX = re.compile(r"^(#{1,6})\s+(.*)", re.MULTILINE)
BULLET_REGEX = re.compile(r"^\s*[-*+]\s+", re.MULTILINE)

# Polarity and constraint markers. Structural validation (headings, code,
# URLs, inline code, bullets) cannot catch the failure that actually costs
# something when a CLAUDE.md is compressed: a rule that flips meaning. "Never
# mock the DB" -> "Mock the DB" passes every structural check. So we also
# require that a section which carried negation still carries negation, and
# warn when its constraint words all vanish.
# Asymmetric on purpose. What TRIGGERS the check is a prohibition in the
# original — "never", "don't", "avoid", 금지, 절대. An incidental "if not
# provided" or "not your preferred approach" is descriptive prose and must not
# trigger anything, or honest compressions would fail.
NEG_TRIGGER_EN_REGEX = re.compile(
    r"\b(?:never|avoid|forbidden|prohibited|must\s+not|do\s*n[o']?t|"
    r"does\s*n[o']?t|cannot|can\s*n[o']?t)",
    re.IGNORECASE,
)
NEG_TRIGGER_KO_REGEX = re.compile(r"금지|절대|마라|말고|하지\s*마|안\s*된다|안\s*됨|불가")

# What SATISFIES it is any negation at all, because compression legitimately
# rewrites "Please don't use any" as "No `any`" and "never mock the DB" as
# "DB 모킹 안 함". We only want to catch the drop to zero.
NEG_ANY_EN_REGEX = re.compile(
    r"\b(?:no|not|never|none|nor|neither|cannot|cant|dont|doesnt|isnt|arent|"
    r"wont|avoid|without|forbidden|prohibited|skip|instead\s+of)\b|n['’]t\b",
    re.IGNORECASE,
)
NEG_ANY_KO_REGEX = re.compile(r"금지|않|없|절대|말고|마라|아니|불가|안|못|대신")

CONSTRAINT_EN_REGEX = re.compile(
    r"\b(?:must|only|unless|before|after|until|require|requires|required|"
    r"always|first|optional|least)\b",
    re.IGNORECASE,
)
CONSTRAINT_KO_REGEX = re.compile(r"반드시|항상|필수|선택|먼저|전에|후에|때만|이상|이하|우선")

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
    return blocks


def extract_urls(text):
    return set(URL_REGEX.findall(text))


def extract_paths(text):
    return set(PATH_REGEX.findall(text))


def count_bullets(text):
    return len(BULLET_REGEX.findall(text))


def strip_code(text):
    """Drop fenced blocks and inline code — those are preserved verbatim, so a
    `!=` or a `--no-verify` inside them is not prose polarity."""
    text = re.sub(r"^(\s{0,3})(`{3,}|~{3,})[\s\S]*?^\s{0,3}\2", "", text, flags=re.MULTILINE)
    text = re.sub(r"`[^`\n]+`", "", text)
    return text


def split_sections(text):
    """Split markdown into positional (heading, body) pairs.

    Body text before the first heading is section "" so a file with no
    headings still yields one comparable chunk.
    """
    sections = []
    title = ""
    body = []
    for line in text.split("\n"):
        m = re.match(r"^(#{1,6})\s+(.*)", line)
        if m:
            sections.append((title, "\n".join(body)))
            title = m.group(2).strip()
            body = []
        else:
            body.append(line)
    sections.append((title, "\n".join(body)))
    return sections


def count_markers(text, en_regex, ko_regex):
    clean = strip_code(text)
    return len(en_regex.findall(clean)) + len(ko_regex.findall(clean))


def extract_inline_codes(text):
    text_without_fences = re.sub(r"^```[\s\S]*?^```", "", text, flags=re.MULTILINE)
    text_without_fences = re.sub(r"^~~~[\s\S]*?^~~~", "", text_without_fences, flags=re.MULTILINE)
    return re.findall(r"`([^`]+)`", text_without_fences)


# ---------- Validators ----------


def validate_headings(orig, comp, result):
    h1 = extract_headings(orig)
    h2 = extract_headings(comp)

    if len(h1) != len(h2):
        result.add_error(f"Heading count mismatch: {len(h1)} vs {len(h2)}")

    if h1 != h2:
        result.add_warning("Heading text/order changed")


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

    if p1 != p2:
        result.add_warning(f"Path mismatch: lost={p1 - p2}, added={p2 - p1}")


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


def validate_polarity(orig, comp, result):
    """Fail when a section that stated a prohibition no longer states one.

    Deliberately lenient about *how* the negation is written — "don't use X"
    compressing to "No X" is fine, both carry a marker. What it catches is the
    drop to zero, which is the case where a rule silently inverts. Constraint
    words (must/only/unless/before/먼저/반드시) get a warning instead of an
    error: losing them muddies a rule without inverting it.
    """
    o_sections = split_sections(orig)
    c_sections = split_sections(comp)

    if len(o_sections) != len(c_sections):
        # Heading counts already differ — validate_headings reports that. Fall
        # back to a whole-document comparison so we still catch inversion.
        o_sections = [("(document)", orig)]
        c_sections = [("(document)", comp)]

    for (o_title, o_body), (_, c_body) in zip(o_sections, c_sections):
        where = f"section '{o_title}'" if o_title else "text before first heading"

        o_neg = count_markers(o_body, NEG_TRIGGER_EN_REGEX, NEG_TRIGGER_KO_REGEX)
        if o_neg:
            c_neg = count_markers(c_body, NEG_ANY_EN_REGEX, NEG_ANY_KO_REGEX)
            if c_neg == 0:
                result.add_error(
                    f"Negation lost in {where}: original had {o_neg} marker(s), "
                    f"compressed has none — a prohibition may have flipped"
                )

        o_con = count_markers(o_body, CONSTRAINT_EN_REGEX, CONSTRAINT_KO_REGEX)
        if o_con:
            c_con = count_markers(c_body, CONSTRAINT_EN_REGEX, CONSTRAINT_KO_REGEX)
            if c_con == 0:
                result.add_warning(
                    f"Constraint words lost in {where}: {o_con} -> 0 "
                    f"(must/only/unless/before/반드시/먼저 …)"
                )


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
    validate_polarity(orig, comp, result)

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
