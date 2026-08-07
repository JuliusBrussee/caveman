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

Entries were sourced from established plain-language style guides
(plainlanguage.gov's wordy-phrase list, Garner's Modern English Usage,
common technical-writing "eliminate wordiness" references) and then
hand-filtered against one hard rule: a phrase only qualifies if swapping
it in verbatim, at any position in any sentence, produces grammatically
valid text. Several plain-language-guide staples were deliberately left
out because the swap breaks depending on what follows: "perform an
analysis of X" -> "analyze of X" is wrong (dangling "of"), and "have a
discussion about X" -> "discuss about X" repeats a preposition "discuss"
doesn't take. A regex substitution can't reshape the rest of the sentence
to fix that, so those stay out rather than ship a replacement that's
sometimes broken.

Word-boundary anchoring: phrase matching is anchored with \\b on both
ends. Without it, a short phrase like "point in time" would falsely match
inside an unrelated word that happens to contain that substring — e.g.
"checkpoint in time" contains the literal text "point in time" starting
mid-word, and an unanchored regex would mangle it into "checkpoint" turning
into a broken word. \\b requires a real word/non-word transition at each
edge, so matches only fire on actual word boundaries.

Recursive pass: apply_phrase_map() re-runs substitution until a pass makes
no further change (bounded by _MAX_PASSES as a safety cap). A single pass
already catches every phrase present in the ORIGINAL text — re.sub finds
all non-overlapping matches in one call, so two unrelated phrases in the
same sentence are both replaced in pass one. The loop exists for the rarer
case where a replacement's output, combined with text next to it, forms a
*new* match that wasn't there originally (chained compression). It costs
nothing when nothing chains — most text stabilizes after exactly one pass.
"""

import re

# Left side must be strictly wordier than the right side and swappable in
# any sentence without changing meaning or register. Ordering doesn't need
# to be longest-first here — _compile_regex sorts before compiling — but
# entries are grouped by category for readability.
PHRASE_MAP = {
    # Causal
    "due to the fact that": "because",
    "in light of the fact that": "because",
    "on the grounds that": "because",
    "for the reason that": "because",
    "owing to the fact that": "because",
    "in view of the fact that": "because",
    "by virtue of the fact that": "because",
    "inasmuch as": "because",
    "on the basis of": "based on",
    # Purpose / conditional
    "in order to": "to",
    "in order for": "for",
    "so as to": "to",
    "in an effort to": "to",
    "in order that": "so that",
    "if it should be the case that": "if",
    "in a situation where": "if",
    "under circumstances in which": "when",
    "provided that": "if",
    "assuming that": "if",
    "in the event that": "if",
    "in the event of": "if",
    "in the case that": "if",
    # Temporal
    "at this point in time": "now",
    "at the present time": "now",
    "at this time": "now",
    "in the near future": "soon",
    "at an early date": "soon",
    "on a daily basis": "daily",
    "on a regular basis": "regularly",
    "on a monthly basis": "monthly",
    "on a weekly basis": "weekly",
    "prior to": "before",
    "previous to": "before",
    "subsequent to": "after",
    "following on from": "after",
    "at the same time as": "while",
    "during the course of": "during",
    "in the course of": "during",
    "for the duration of": "during",
    "until such time as": "until",
    "at such time as": "when",
    "at that point in time": "then",
    "in the interim": "meanwhile",
    "point in time": "moment",
    "period of time": "period",
    "span of time": "period",
    "for a period of": "for",
    "during the period of": "during",
    "for a short period of time": "briefly",
    # Reference / relation
    "with regard to": "about",
    "with regards to": "about",
    "in regard to": "about",
    "in relation to": "about",
    "with respect to": "about",
    "with reference to": "about",
    "pertaining to": "about",
    "in the vicinity of": "near",
    "at the location of": "at",
    "in the area of": "near",
    "close proximity to": "near",
    # Longer variant tried first (see _compile_regex docstring) — without
    # it, "in close proximity to the office" collapses the inner "close
    # proximity to" alone and leaves a dangling leading "in", producing the
    # broken double-preposition "in near the office".
    "in close proximity to": "near",
    "similar to": "like",
    "as a result of": "from",
    "as a consequence of": "from",
    "in conjunction with": "with",
    "in addition": "also",
    "by means of": "by",
    "in the form of": "as",
    "in the nature of": "like",
    "with the exception of": "except",
    "with the exception of the fact that": "except that",
    "in the absence of": "without",
    "in lieu of": "instead of",
    # Legal / authority (replacement is a drop-in for the statute/policy
    # sense of these phrases — see module docstring for the "as prescribed
    # by" and "notwithstanding anything to the contrary" entries that were
    # rejected because the person/authority sense doesn't hold up)
    "pursuant to": "under",
    "in accordance with": "under",
    "under the provisions of": "under",
    "subject to the provisions of": "under",
    "set forth in": "in",
    "comply with": "follow",
    "is authorized to": "may",
    "is applicable to": "applies to",
    # Quantity
    "a large number of": "many",
    "a great deal of": "much",
    "a majority of": "most",
    "the majority of": "most",
    "the vast majority of": "most",
    "a small number of": "few",
    "a limited number of": "few",
    "a number of": "some",
    "a wide range of": "many",
    "an adequate amount of": "enough",
    "a sufficient number of": "enough",
    "an adequate number of": "enough",
    "the totality of": "all",
    "each individual": "each",
    "each and every": "every",
    "for the most part": "mostly",
    "to a certain extent": "somewhat",
    "to a large extent": "largely",
    "in the majority of cases": "usually",
    "in most cases": "usually",
    "in most instances": "usually",
    "more often than not": "usually",
    "in a number of cases": "sometimes",
    "it is often the case that": "often",
    # Concession
    "in spite of the fact that": "although",
    "despite the fact that": "although",
    "regardless of the fact that": "although",
    "notwithstanding the fact that": "although",
    "in spite of": "despite",
    # Manner
    "in a manner that": "so that",
    "in a timely manner": "promptly",
    "in a similar manner": "similarly",
    "in a different manner": "differently",
    "in an efficient manner": "efficiently",
    "in a careful manner": "carefully",
    "in a professional manner": "professionally",
    # Ability / capability
    "is able to": "can",
    "is unable to": "cannot",
    "has the ability to": "can",
    "you will need to": "you must",
    "there is no need to": "don't",
    "in order to be able to": "to",
    "allows you to": "lets you",
    "gives you the ability to": "lets you",
    "give you the ability to": "let you",
    # Hedging / belief
    "it is important to note that": "note:",
    "it should be noted that": "note:",
    "it is worth noting that": "note:",
    "you should be aware that": "note:",
    "keep in mind that": "note:",
    "it is possible that": "maybe",
    "there is a possibility that": "maybe",
    "it would appear that": "apparently",
    "it is likely that": "probably",
    "it is our belief that": "we believe",
    "it is our opinion that": "we think",
    "there can be no doubt that": "undoubtedly",
    "it goes without saying that": "obviously",
    "it may be said that": "arguably",
    "could potentially": "could",
    "please make sure that": "confirm",
    "make sure that": "confirm",
    "it is recommended that you": "you should",
    "in the case where": "if",
    "in such a way that": "so that",
    # Verb phrases (only where the replacement takes the same complement
    # as the original — see module docstring for what was excluded and why)
    "take into consideration": "consider",
    "take into account": "consider",
    "make a decision": "decide",
    "come to a conclusion": "conclude",
    "give consideration to": "consider",
    "put an emphasis on": "emphasize",
    "make an assumption": "assume",
    "make a recommendation": "recommend",
    "make a comparison": "compare",
    "draw a comparison": "compare",
    "reach an agreement": "agree",
    "take action": "act",
    "make a payment": "pay",
    "is dependent on": "depends on",
    "is reflective of": "reflects",
    "is suggestive of": "suggests",
    "is indicative of": "indicates",
    "is contingent upon": "depends on",
    "exhibits a tendency to": "tends to",
    "has a requirement for": "requires",
    "make use of": "use",
    "take ownership of": "own",
    "drill down into": "examine",
    "push back on": "oppose",
    "get the ball rolling": "start",
    "reach out to": "contact",
    "close the loop on": "resolve",
    "get up to speed": "catch up",
    # Doc navigation / reference pointers (same idea as the redundant-
    # modifier and hedging entries above — these are for READMEs/PR
    # descriptions, the exact kind of prose caveman-compress runs on)
    "in the following section": "next",
    "in the section below": "below",
    "as shown below": "below",
    "as described below": "below",
    # Workplace jargon (unambiguous subset only — see module docstring;
    # idioms with a genuine second meaning, like "table this" meaning the
    # opposite in British English, or "circle back" with no real token
    # savings over "follow up", were deliberately left out)
    "touch base": "connect",
    "loop in": "include",
    "in the loop": "informed",
    "on the same page": "aligned",
    "at the end of the day": "ultimately",
    "per our conversation": "as discussed",
    "per our discussion": "as discussed",
    "low-hanging fruit": "easy wins",
    "think outside the box": "innovate",
    "value add": "benefit",
    "action item": "task",
    "par for the course": "typical",
    # Absolutes / emphasis
    "under no circumstances": "never",
    "at all times": "always",
    "first and foremost": "first",
    "for all intents and purposes": "essentially",
    "in the final analysis": "ultimately",
    "when all is said and done": "ultimately",
    # Redundant modifiers (the modifier adds nothing the noun doesn't
    # already mean)
    "end result": "result",
    "final outcome": "outcome",
    "final result": "result",
    "past history": "history",
    "past experience": "experience",
    "future plans": "plans",
    "future prospects": "prospects",
    "basic fundamentals": "fundamentals",
    "basic essentials": "essentials",
    "completely eliminate": "eliminate",
    "absolutely essential": "essential",
    "absolutely necessary": "necessary",
    "very unique": "unique",
    "new innovation": "innovation",
    "true facts": "facts",
    "unexpected surprise": "surprise",
    "added bonus": "bonus",
    "free gift": "gift",
    "general consensus": "consensus",
    "mutual cooperation": "cooperation",
}

_MAX_PASSES = 5


def _compile_regex(phrase_map):
    """Compile a word-boundary-anchored, longest-first alternation.

    Longest-first ordering matters because Python's re engine takes the
    first alternative that matches at a given position (not the longest
    overall match), so a short phrase listed before a longer one that
    contains it would win and leave a mangled remainder — e.g. without
    this ordering, a hypothetical "in order" entry before "in order to"
    would match "in order to" as "in order" + a dangling "to".

    \\b anchors on both ends prevent matching mid-word (see module
    docstring's word-boundary-anchoring note).
    """
    ordered = sorted(phrase_map, key=len, reverse=True)
    pattern = r"\b(?:" + "|".join(re.escape(p) for p in ordered) + r")\b"
    return re.compile(pattern, re.IGNORECASE)


_PHRASE_REGEX = _compile_regex(PHRASE_MAP)

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


def _substitute_once(text: str, phrase_map: dict, regex: re.Pattern) -> str:
    def replace(match: re.Match) -> str:
        original = match.group(0)
        replacement = phrase_map[original.lower()]
        return _match_case(replacement, original)

    return regex.sub(replace, text)


def _substitute_until_stable(
    text: str, phrase_map: dict = PHRASE_MAP, regex: re.Pattern = _PHRASE_REGEX
) -> str:
    """Re-apply substitution until a pass makes no further change.

    Every replacement is strictly shorter than the phrase it replaces, so
    text length can only shrink or hold steady pass over pass — it can
    never grow, and it can only shrink a bounded number of times before
    hitting a fixed point. _MAX_PASSES is a defensive cap, not something
    real input is expected to reach.
    """
    for _ in range(_MAX_PASSES):
        new_text = _substitute_once(text, phrase_map, regex)
        if new_text == text:
            return new_text
        text = new_text
    return text


def apply_phrase_map(text: str) -> str:
    """Replace known wordy phrases with their concise equivalent.

    Skips fenced code blocks and inline code spans so code samples,
    comments, and string literals are never rewritten — only prose is.
    Runs to a fixed point per prose segment (see _substitute_until_stable).
    """
    segments = _CODE_SEGMENT_REGEX.split(text)
    code_spans = _CODE_SEGMENT_REGEX.findall(text)

    result = [_substitute_until_stable(segments[0])]
    for code_span, prose_segment in zip(code_spans, segments[1:]):
        result.append(code_span)
        result.append(_substitute_until_stable(prose_segment))
    return "".join(result)
