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

Run: uv run --with tiktoken python evals/measure.py
"""

from __future__ import annotations

import json
import statistics
from pathlib import Path

import tiktoken

ENCODING = tiktoken.get_encoding("o200k_base")
SNAPSHOT = Path(__file__).parent / "snapshots" / "results.json"


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
    if not SNAPSHOT.exists():
        print(f"No snapshot at {SNAPSHOT}. Run `python evals/llm_run.py` first.")
        return

    data = json.loads(SNAPSHOT.read_text())
    arms = data["arms"]
    meta = data.get("metadata", {})

    baseline_tokens = [count(o) for o in arms["__baseline__"]]
    terse_tokens = [count(o) for o in arms["__terse__"]]

    print(f"_Generated: {meta.get('generated_at', '?')}_")
    print(
        f"_Model: {meta.get('model', '?')} · CLI: {meta.get('claude_cli_version', '?')}_"
    )
    print(f"_Tokenizer: tiktoken o200k_base (approximation of Claude's BPE)_")
    print(
        f"_n = {meta.get('n_prompts', len(baseline_tokens))} prompts, single run per arm_"
    )
    print()
    print(f"**Reference arms (no skill):**")
    print(f"- baseline (no system prompt): {sum(baseline_tokens)} tokens total")
    print(
        f"- terse control (`Answer concisely.`): {sum(terse_tokens)} tokens total "
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
    print(f"_Source: {SNAPSHOT.name}. Refresh with `python evals/llm_run.py`._")


if __name__ == "__main__":
    main()
