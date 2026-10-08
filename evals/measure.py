"""
Read evals/snapshots/results.json (produced by llm_run.py) and report
real token compression per skill against the *terse control arm* — i.e.
how much the skill adds on top of a plain "Answer concisely." instruction.

Reports median, min, max and stdev across prompts, not just the mean,
so the reader can see whether a number is solid or noisy.

Tokenizer note: tiktoken o200k_base is OpenAI's tokenizer and is only an
approximation of Claude's BPE. The ratios are still meaningful for
comparing skills against each other, but the absolute numbers should be
read as "approximate output-length reduction", not "exact Claude tokens".
When the snapshot carries the usage Claude Code reported for each call
(`usage`, written by llm_run.py), a second table gives billed output tokens
and the input tokens the skill adds, both against the terse control.

Run: uv run --with tiktoken python evals/measure.py [snapshot.json]

Environment:
  CAVEMAN_EVAL_LANG  read snapshots/results.<lang>.json instead of the
                     English results.json (default: en); an explicit
                     snapshot path wins
"""

from __future__ import annotations

import os
import statistics
import sys
from pathlib import Path

import tiktoken
from snapshot_contract import SnapshotContractError, load_snapshot

# Windows consoles and piped stdout default to the ANSI code page (cp1252),
# which cannot encode the arrows, em-dashes and minus signs printed below —
# a diagnostic that crashes instead of printing is worse than useless
# (#203/#459). Replace unencodable characters rather than raising.
for _stream in (sys.stdout, sys.stderr):
    try:
        _stream.reconfigure(errors="replace")
    except Exception:
        pass


ENCODING = tiktoken.get_encoding("o200k_base")
LANG = os.environ.get("CAVEMAN_EVAL_LANG", "en")
SNAPSHOT = Path(__file__).parent / "snapshots" / (
    "results.json" if LANG == "en" else f"results.{LANG}.json"
)


def count(text: str) -> int:
    return len(ENCODING.encode(text))


def stats(savings: list[float]) -> tuple[float, float, float, float, float]:
    return (
        statistics.median(savings),
        statistics.mean(savings),
        min(savings),
        max(savings),
        statistics.stdev(savings) if len(savings) > 1 else 0.0,
    )


def fmt_pct(x: float) -> str:
    sign = "−" if x < 0 else "+"
    return f"{sign}{abs(x) * 100:.0f}%"


def main() -> None:
    # Optional path: reminder_run.py writes its own snapshot.
    global SNAPSHOT
    if len(sys.argv) > 1:
        SNAPSHOT = Path(sys.argv[1])
    if not SNAPSHOT.exists():
        print(f"No snapshot at {SNAPSHOT}. Run `python evals/llm_run.py` first.")
        return

    try:
        data = load_snapshot(SNAPSHOT)
    except SnapshotContractError as error:
        print(f"Invalid snapshot: {error}", file=sys.stderr)
        raise SystemExit(1) from error
    arms = data["arms"]
    meta = data.get("metadata", {})

    baseline_tokens = [count(o) for o in arms["__baseline__"]]
    terse_tokens = [count(o) for o in arms["__terse__"]]

    print(f"_Generated: {meta.get('generated_at', '?')}_")
    print(
        f"_Model: {meta.get('model', '?')} · CLI: {meta.get('claude_cli_version', '?')}_"
    )
    print(f"_Tokenizer: tiktoken o200k_base (approximation of Claude's BPE)_")
    print(f"_Language: {meta.get('lang', 'en')} · terse control: `{meta['terse_prefix']}`_")
    print(
        f"_n = {meta.get('n_prompts', len(baseline_tokens))} prompts, single run per arm_"
    )
    print()
    print(f"**Reference arms (no skill):**")
    print(f"- baseline (no system prompt): {sum(baseline_tokens)} tokens total")
    print(
        f"- terse control (`{meta['terse_prefix']}`): {sum(terse_tokens)} tokens total "
        f"({fmt_pct(1 - sum(terse_tokens) / sum(baseline_tokens))} vs baseline)"
    )
    print()
    print("**Skills, measured as additional reduction on top of the terse control:**")
    print()
    print("| Skill | Median | Mean | Min | Max | Stdev | Tokens (skill / terse) |")
    print("|-------|--------|------|-----|-----|-------|-------------------------|")

    rows = []
    for skill, outputs in arms.items():
        if skill in ("__baseline__", "__terse__"):
            continue
        skill_tokens = [count(o) for o in outputs]
        savings = [
            1 - (s / t) if t else 0.0 for s, t in zip(skill_tokens, terse_tokens)
        ]
        med, mean, lo, hi, sd = stats(savings)
        rows.append(
            (skill, med, mean, lo, hi, sd, sum(skill_tokens), sum(terse_tokens))
        )

    for row in sorted(rows, key=lambda r: -r[1]):
        skill, med, mean, lo, hi, sd, st, tt = row
        print(
            f"| **{skill}** | {fmt_pct(med)} | {fmt_pct(mean)} | "
            f"{fmt_pct(lo)} | {fmt_pct(hi)} | {sd * 100:.0f}% | {st} / {tt} |"
        )

    print()
    print("_Savings = `1 - skill_tokens / terse_tokens` per prompt._")
    if "usage" in data:
        print_usage(data["usage"])
    print(f"_Source: {SNAPSHOT.name}. Refresh with `python evals/llm_run.py`._")


def billed_input(cell: dict) -> int:
    return (
        cell["input_tokens"]
        + cell["cache_creation_input_tokens"]
        + cell["cache_read_input_tokens"]
    )


def print_usage(usage: dict) -> None:
    terse = usage["__terse__"]
    print()
    print("**Claude-reported usage (`claude -p --output-format json`), against the terse control:**")
    print()
    print("| Skill | Output reduction, median | Output reduction, mean | Output tokens (skill / terse) | Input added by skill, median |")
    print("|-------|--------------------------|------------------------|-------------------------------|------------------------------|")
    rows = []
    for skill, cells in usage.items():
        if skill in ("__baseline__", "__terse__"):
            continue
        out_s = [c["output_tokens"] for c in cells]
        out_t = [c["output_tokens"] for c in terse]
        savings = [1 - (s / t) if t else 0.0 for s, t in zip(out_s, out_t)]
        added = [billed_input(s) - billed_input(t) for s, t in zip(cells, terse)]
        rows.append((skill, statistics.median(savings), statistics.mean(savings),
                     sum(out_s), sum(out_t), statistics.median(added)))
    for skill, med, mean, st, tt, added in sorted(rows, key=lambda r: -r[1]):
        print(
            f"| **{skill}** | {fmt_pct(med)} | {fmt_pct(mean)} | {st} / {tt} | {added:+.0f} |"
        )
    print()
    print(
        "_Output tokens are Claude's billed count and include any thinking tokens. "
        "Reduction = `1 - skill / terse` per prompt. Input added = per-prompt "
        "(input + cache-creation + cache-read tokens) of the skill arm minus the "
        "terse arm; the baseline arm runs Claude Code's default system prompt, "
        "so its input is not comparable._"
    )


if __name__ == "__main__":
    main()
