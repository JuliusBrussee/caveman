"""
Measure token compression for caveman skills against golden cases.

Reads evals/cases.yaml. Each case has:
  - input: the user question
  - baseline: a typical verbose LLM answer (the thing caveman is replacing)
  - outputs: per-skill, per-level caveman answer

Compression is measured as savings vs the verbose baseline:
  savings = 1 - (caveman_tokens / baseline_tokens)

Token counts use tiktoken cl100k_base. This is OpenAI's tokenizer; Claude uses
a similar BPE so the relative numbers transfer well even though the absolute
counts differ slightly.

Run: uv run --with tiktoken --with pyyaml python evals/measure.py
"""

from __future__ import annotations

from collections import defaultdict
from pathlib import Path

import tiktoken
import yaml

ENCODING = tiktoken.get_encoding("cl100k_base")


def count(text: str) -> int:
    return len(ENCODING.encode(text))


def main() -> None:
    cases_path = Path(__file__).parent / "cases.yaml"
    cases = yaml.safe_load(cases_path.read_text())

    # savings[skill][level] = list of savings ratios across cases
    savings: dict[str, dict[str, list[float]]] = defaultdict(lambda: defaultdict(list))

    for case in cases:
        baseline_tokens = count(case["baseline"])
        for skill, levels in case["outputs"].items():
            for level, output in levels.items():
                out_tokens = count(output)
                saving = 1 - (out_tokens / baseline_tokens) if baseline_tokens else 0.0
                savings[skill][level].append(saving)

    skills = sorted(savings.keys())
    levels = ["lite", "full", "ultra"]

    print("| Skill | Lite | Full | Ultra |")
    print("|-------|------|------|-------|")
    for skill in skills:
        row = [f"**{skill}**"]
        for level in levels:
            ratios = savings[skill].get(level, [])
            if not ratios:
                row.append("—")
            else:
                avg = sum(ratios) / len(ratios)
                row.append(f"−{avg * 100:.0f}%")
        print("| " + " | ".join(row) + " |")

    print()
    print("_Savings vs a verbose LLM baseline answer. Higher is more compression._")
    print(
        f"_Measured across {len(cases)} golden cases. Tokenizer: tiktoken cl100k_base._"
    )


if __name__ == "__main__":
    main()
