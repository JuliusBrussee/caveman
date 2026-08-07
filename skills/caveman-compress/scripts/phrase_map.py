"""Deterministic wordy-phrase-to-word compression.

Why this exists: caveman-compress's only compression step is an LLM call
(see compress.py:build_compress_prompt). That works, but every call costs
real tokens and money just to decide something a fixed dictionary already
knows for free — "due to the fact that" always means "because", no model
judgment required.

This matters specifically for multi-word phrases, not single-word synonyms.
Swapping "extensive" for "big" costs about the same number of BPE tokens
either way, so it saves nothing. But a phrase like "in order to" is 3+
tokens that collapse to a single word ("to") with an identical meaning —
that's a real, mechanical token reduction, not a rewording. Doing this
with a lookup table before the LLM call means:

  1. The phrases are gone before Claude ever sees them, shrinking the
     compression prompt itself (cheaper call).
  2. The savings are free and instant for the phrases that match — no
     model inference needed for the part language already has a fixed
     answer for.

This is deliberately narrow: a fixed table of unambiguous, meaning-preserving
phrase-to-word replacements. It is not trying to replace the LLM compression
pass (which handles rewording, restructuring, and everything context-
dependent) — it is a cheap pre-pass that shrinks the input before the
expensive part runs.
"""

import re

# Left side must be strictly wordier than the right side and swappable in
# any sentence without changing meaning or register. Ordered longest-first
# so multi-word phrases match before any shorter phrase they contain (e.g.
# "in order to" must be tried before "in order").
PHRASE_MAP = {
    "due to the fact that": "because",
    "in light of the fact that": "because",
    "on the grounds that": "because",
    "for the reason that": "because",
    "in order to": "to",
    "in order for": "for",
    "at this point in time": "now",
    "at the present time": "now",
    "in the near future": "soon",
    "in the event that": "if",
    "in the event of": "if",
    "in the case that": "if",
    "with regard to": "about",
    "with regards to": "about",
    "in regard to": "about",
    "in relation to": "about",
    "with respect to": "about",
    "as a result of": "from",
    "as a consequence of": "from",
    "a large number of": "many",
    "a great deal of": "much",
    "a majority of": "most",
    "a small number of": "few",
    "in spite of the fact that": "although",
    "despite the fact that": "although",
    "regardless of the fact that": "although",
    "on a daily basis": "daily",
    "on a regular basis": "regularly",
    "on a monthly basis": "monthly",
    "on a weekly basis": "weekly",
    "prior to": "before",
    "subsequent to": "after",
    "in conjunction with": "with",
    "in the process of": "while",
    "for the purpose of": "for",
    "in a manner that": "so that",
    "is able to": "can",
    "is unable to": "cannot",
    "has the ability to": "can",
    "it is important to note that": "note:",
    "it should be noted that": "note:",
    "it is possible that": "maybe",
    "there is a possibility that": "maybe",
    "take into consideration": "consider",
    "take into account": "consider",
    "make a decision": "decide",
    "come to a conclusion": "conclude",
    "give consideration to": "consider",
    "put an emphasis on": "emphasize",
    "in the majority of cases": "usually",
    "under no circumstances": "never",
    "at all times": "always",
    "in a timely manner": "promptly",
    "each and every": "every",
    "first and foremost": "first",
    "close proximity to": "near",
    "end result": "result",
    "final outcome": "outcome",
    "past history": "history",
    "future plans": "plans",
    "basic fundamentals": "fundamentals",
    "completely eliminate": "eliminate",
    "absolutely essential": "essential",
}

# Longest phrase first so "in order to" is tried before a hypothetical
# shorter entry that is also a prefix of it.
_ORDERED_PHRASES = sorted(PHRASE_MAP, key=len, reverse=True)

_PHRASE_REGEX = re.compile(
    "|".join(re.escape(p) for p in _ORDERED_PHRASES),
    re.IGNORECASE,
)

# Fenced code blocks (```...```) and inline code spans (`...`) must survive
# untouched — a phrase inside a code comment or string literal is not prose,
# and validate.py already treats code-block drift as a hard compression
# failure (see validate.py's code-block-preservation check).
_CODE_SEGMENT_REGEX = re.compile(r"```.*?```|`[^`\n]*`", re.DOTALL)


def _match_case(replacement: str, original: str) -> str:
    """Preserve the original phrase's leading capitalization on the replacement."""
    if original[:1].isupper():
        return replacement[:1].upper() + replacement[1:]
    return replacement


def _substitute(text: str) -> str:
    def replace(match: re.Match) -> str:
        original = match.group(0)
        replacement = PHRASE_MAP[original.lower()]
        return _match_case(replacement, original)

    return _PHRASE_REGEX.sub(replace, text)


def apply_phrase_map(text: str) -> str:
    """Replace known wordy phrases with their concise equivalent.

    Skips fenced code blocks and inline code spans so code samples,
    comments, and string literals are never rewritten — only prose is.
    """
    segments = _CODE_SEGMENT_REGEX.split(text)
    code_spans = _CODE_SEGMENT_REGEX.findall(text)

    result = [_substitute(segments[0])]
    for code_span, prose_segment in zip(code_spans, segments[1:]):
        result.append(code_span)
        result.append(_substitute(prose_segment))
    return "".join(result)
