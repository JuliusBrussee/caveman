#!/usr/bin/env python3
"""caveman-lint — report wasted tokens in AI-generated (or any) prose.

Usage:
    python3 src/tools/caveman-lint.py <file> [<file> ...] [--json] [--max-code-lines N]

Scans prose for the waste patterns caveman exists to eliminate (greetings,
apologies, hedging, filler, boilerplate transitions, duplicated content) and
reports estimated token waste per category. Makes waste visible and measurable
— it never edits the file.

Honesty rules (project policy):
  - Fenced code blocks and inline code are NEVER counted as waste.
  - Token counts use tiktoken (cl100k_base) when installed, else a chars/4
    approximation — the report says which was used.
  - Oversized code blocks are reported as advisory only (the user may have
    asked for the full file) and are excluded from the waste total.
"""

import json
import re
import sys
from pathlib import Path

# Waste patterns, matched case-insensitively against prose (never code).
# Each hit's matched span is counted as wasted tokens.
PHRASE_RULES = [
    ("greeting", [
        r"\bsure[,!]? i(?:'d| would) be happy to help(?: you)?(?: with (?:that|this))?\b",
        r"\bi(?:'d| would) be happy to\b",
        r"^(?:sure|certainly|of course|absolutely|great question)[,!.]\s*",
        r"\bhappy to help\b",
        r"\bthanks for (?:asking|reaching out|your question)\b",
    ]),
    ("apology", [
        r"\bi (?:sincerely )?apologi[sz]e\b[^.!\n]*",
        r"\b(?:my apologies|sorry for (?:the|any) (?:confusion|inconvenience|trouble))\b",
    ]),
    ("hedging", [
        r"\bit(?:'s| is) (?:important|worth) (?:to note|noting)(?: that)?\b",
        r"\bit should be noted that\b",
        r"\bplease (?:note|keep in mind|be aware) that\b",
        r"\bkeep in mind that\b",
        r"\bgenerally speaking,?\b",
        r"\bas an ai(?: language model| assistant)?,?\b[^.!\n]*",
        r"\bthere are (?:a few|several|many) (?:things|factors|considerations) to (?:consider|keep in mind)\b",
    ]),
    ("boilerplate", [
        r"\bin (?:conclusion|summary),?\b",
        r"\bto summari[sz]e,?\b",
        r"\bin other words,?\b",
        r"\bthat being said,?\b",
        r"\bwith that (?:said|in mind),?\b",
        r"\bas (?:mentioned|noted|discussed) (?:earlier|above|previously),?\b",
        r"\bas (?:we|you) can see,?\b",
        r"\blet me know if you (?:have any(?: other| more)? questions|need anything else)\b[^.!\n]*",
        r"\bi hope (?:this|that) helps\b[^.!\n]*",
    ]),
]

# Filler words counted individually (~1 token each). The same list SKILL.md
# tells the model to drop.
FILLER_WORDS = r"\b(?:just|really|basically|actually|simply|essentially|obviously|clearly|very|quite|rather)\b"

MIN_DUP_SENTENCE_CHARS = 40  # short sentences repeat legitimately ("Yes.")


def count_tokens_factory():
    """Return (counter, method_label). Prefers tiktoken, falls back to chars/4."""
    try:
        import tiktoken  # type: ignore

        enc = tiktoken.get_encoding("cl100k_base")
        return (lambda s: len(enc.encode(s))), "tiktoken (cl100k_base)"
    except Exception:
        return (lambda s: max(1, round(len(s) / 4))), "chars/4 approximation"


def split_prose_and_code(text):
    """Split markdown into (prose_text, code_blocks). Fence-aware; inline
    code spans are stripped from prose so backticked commands never match."""
    prose_lines = []
    code_blocks = []
    fence_re = re.compile(r"^\s*(`{3,}|~{3,})")
    in_fence = False
    fence_marker = ""
    current = []
    for line in text.splitlines():
        m = fence_re.match(line)
        if in_fence:
            if m and line.strip().startswith(fence_marker):
                in_fence = False
                code_blocks.append("\n".join(current))
                current = []
            else:
                current.append(line)
            continue
        if m:
            in_fence = True
            fence_marker = m.group(1)
            continue
        prose_lines.append(line)
    if current:
        code_blocks.append("\n".join(current))  # unterminated fence
    prose = "\n".join(prose_lines)
    prose = re.sub(r"`[^`\n]+`", "", prose)  # drop inline code spans
    return prose, code_blocks


def duplicate_spans(prose):
    """Yield (start, end, category, text) for 2nd+ occurrences of repeated
    headings and repeated sentences. Heading lines are blanked (offset-
    preserving) before sentence detection so a heading never glues onto the
    sentence that follows it."""
    spans = []
    headings = {}
    for m in re.finditer(r"^#{1,6}\s+(.+)$", prose, re.MULTILINE):
        key = re.sub(r"\s+", " ", m.group(1).strip().lower())
        headings[key] = headings.get(key, 0) + 1
        if headings[key] > 1:
            spans.append((m.start(), m.end(), "duplicate-heading", m.group(0)))

    body = re.sub(r"^#{1,6}\s+.+$", lambda m: " " * len(m.group(0)), prose, flags=re.MULTILINE)
    seen = {}
    for m in re.finditer(r"[^\s.!?][^.!?]*[.!?]", body):
        sentence = m.group(0).strip()
        if len(sentence) < MIN_DUP_SENTENCE_CHARS:
            continue
        key = re.sub(r"\s+", " ", sentence.lower())
        seen[key] = seen.get(key, 0) + 1
        if seen[key] > 1:
            spans.append((m.start(), m.end(), "duplicate-sentence", sentence))
    return spans


def lint_text(text, max_code_lines=80):
    count_tokens, method = count_tokens_factory()
    prose, code_blocks = split_prose_and_code(text)

    # Collect every candidate waste span, then claim greedily left-to-right,
    # longest-first — overlapping matches ("Sure! I'd be happy to help you
    # with that" + the inner "I'd be happy to" + "Sure!") must count ONCE, or
    # the waste total overstates and the report stops being honest.
    candidates = []
    for cat, patterns in PHRASE_RULES:
        for pat in patterns:
            for m in re.finditer(pat, prose, re.IGNORECASE | re.MULTILINE):
                candidates.append((m.start(), m.end(), cat, m.group(0)))
    for m in re.finditer(FILLER_WORDS, prose, re.IGNORECASE):
        candidates.append((m.start(), m.end(), "filler-word", m.group(0)))
    candidates.extend(duplicate_spans(prose))

    candidates.sort(key=lambda t: (t[0], -(t[1] - t[0])))
    categories = {}  # name -> {"hits": [...], "tokens": int}
    claimed_end = -1
    for start, end, cat, span_text in candidates:
        if start < claimed_end:
            continue  # overlaps an already-counted span
        claimed_end = end
        entry = categories.setdefault(cat, {"hits": [], "tokens": 0})
        entry["hits"].append(span_text.strip()[:80])
        entry["tokens"] += count_tokens(span_text)

    total_tokens = count_tokens(text)
    wasted = sum(c["tokens"] for c in categories.values())
    pct = round(wasted / total_tokens * 100) if total_tokens else 0

    oversized = [
        {"lines": n, "advisory": True}
        for n in (b.count("\n") + 1 for b in code_blocks)
        if n > max_code_lines
    ]

    return {
        "total_tokens": total_tokens,
        "wasted_tokens": wasted,
        "wasted_pct": pct,
        "token_method": method,
        "categories": {
            k: {"count": len(v["hits"]), "tokens": v["tokens"], "examples": v["hits"][:3]}
            for k, v in sorted(categories.items(), key=lambda kv: -kv[1]["tokens"])
        },
        "oversized_code_blocks": oversized,
        "max_code_lines": max_code_lines,
    }


def format_report(path, r):
    sep = "─" * 40
    lines = [f"\ncaveman-lint: {path}", sep,
             f"Total tokens:   {r['total_tokens']:,}  ({r['token_method']})"]
    if not r["categories"]:
        lines.append("No waste patterns found. Rock solid.")
    else:
        lines.append(f"Wasted tokens:  {r['wasted_tokens']:,}  (~{r['wasted_pct']}% of document)")
        lines.append(sep)
        for name, c in r["categories"].items():
            lines.append(f"  {name:<20} {c['count']:>3}×  ~{c['tokens']} tokens")
            for ex in c["examples"]:
                lines.append(f"      · {ex}")
    if r["oversized_code_blocks"]:
        lines.append(sep)
        n = len(r["oversized_code_blocks"])
        biggest = max(b["lines"] for b in r["oversized_code_blocks"])
        lines.append(f"  advisory: {n} code block{'s' if n > 1 else ''} over "
                     f"{r['max_code_lines']} lines (largest: {biggest}) — "
                     "prefer a diff unless the full file was requested. "
                     "Not counted as waste.")
    lines.append(sep)
    return "\n".join(lines) + "\n"


def main(argv):
    args = list(argv)
    as_json = "--json" in args
    if as_json:
        args.remove("--json")
    max_code_lines = 80
    if "--max-code-lines" in args:
        i = args.index("--max-code-lines")
        try:
            max_code_lines = int(args[i + 1])
        except (IndexError, ValueError):
            print("caveman-lint: --max-code-lines takes an integer", file=sys.stderr)
            return 2
        del args[i:i + 2]

    if not args:
        print(__doc__.strip().splitlines()[2].strip(), file=sys.stderr)
        return 2

    results = {}
    for name in args:
        p = Path(name)
        if not p.is_file():
            print(f"caveman-lint: not a file: {p}", file=sys.stderr)
            return 2
        results[str(p)] = lint_text(p.read_text(encoding="utf-8", errors="ignore"),
                                    max_code_lines=max_code_lines)

    if as_json:
        print(json.dumps(results, indent=2))
    else:
        for path, r in results.items():
            sys.stdout.write(format_report(path, r))
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
