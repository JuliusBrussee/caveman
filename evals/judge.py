"""
evals/judge.py — the FIDELITY (correctness) axis for the eval harness.

`measure.py` answers "how many tokens did the skill save?" but says nothing
about whether the compressed answer is still *correct* — its own README notes a
skill that replies `k` to everything would score -99% and "win". This module
adds the missing second axis.

For each (arm, prompt) answer in snapshots/results.json it produces two signals,
written to snapshots/fidelity.json:

  1. Rubric facts — an LLM judge (`claude -p`, temperature 0) checks each binary
     fact from prompts/rubrics.json against the answer. A fact passes only if the
     answer actually contains/implies it. fidelity = passed / total * 100.

  2. Verbatim invariants — DETERMINISTIC, no LLM: every required token (code
     symbol, command, exact identifier) must appear verbatim in the answer. A
     miss is a hard correctness failure the gate treats separately from fidelity,
     because an abbreviated `useMemo`/`EXPLAIN`/`TCP` is a bug, not a style win.

The token gate (gate.py) is meaningless without this — together they enforce
"saves tokens AND stays correct".

Run:  python evals/judge.py                 # judge every arm
      python evals/judge.py --arm caveman   # one arm
      python evals/judge.py --runs 3        # majority vote per fact

Env:  CAVEMAN_JUDGE_MODEL  optional --model passed through to `claude`

Pure stdlib — no `uv`/pyproject needed. Requires the authenticated `claude` CLI
on PATH (same dependency as llm_run.py). On Windows the CLI is often a `.cmd`
shim; run_claude resolves it so subprocess can launch it without a shell.
"""

from __future__ import annotations

import argparse
import datetime as dt
import json
import os
import re
import shutil
import subprocess
import sys
from collections import Counter
from pathlib import Path

EVALS = Path(__file__).parent
SNAPSHOT = EVALS / "snapshots" / "results.json"
RUBRICS = EVALS / "prompts" / "rubrics.json"
FIDELITY = EVALS / "snapshots" / "fidelity.json"

JUDGE_SYSTEM = (
    "You are a strict technical grader. You are given a question, an answer, and "
    "a checklist of required facts. For each fact decide whether the answer "
    "actually states or clearly implies it. Be conservative: vague, adjacent, or "
    "hand-wavy statements do NOT satisfy a fact. Judge correctness only, never "
    "style, length, or tone — a terse fragment that contains the fact passes."
)


def build_judge_prompt(question: str, answer: str, facts: list[str]) -> str:
    lines = [
        "QUESTION:",
        question,
        "",
        "ANSWER:",
        answer,
        "",
        "REQUIRED FACTS:",
    ]
    for i, fact in enumerate(facts, 1):
        lines.append(f"{i}. {fact}")
    lines += [
        "",
        "For each fact in order, output whether the answer satisfies it. Respond "
        'with ONLY a JSON object: {"verdicts": [true, false, ...]} — exactly one '
        "boolean per fact, same order. No prose, no markdown.",
    ]
    return "\n".join(lines)


def _claude_argv():
    # Resolve the `claude` CLI to something subprocess can launch WITHOUT a shell.
    # On Windows the CLI is commonly a .cmd/.bat shim (npm), which CreateProcess
    # cannot execute directly — route those through cmd.exe. A real .exe (which
    # shutil.which prefers via PATHEXT) launches as-is. Falls back to the bare
    # name so subprocess raises a clear "not found" when claude isn't on PATH.
    exe = shutil.which("claude")
    if not exe:
        return ["claude"]
    if os.name == "nt" and exe.lower().endswith((".cmd", ".bat")):
        return ["cmd", "/c", exe]
    return [exe]


def run_claude(prompt: str, system: str) -> str:
    cmd = _claude_argv() + ["-p", "--system-prompt", system]
    if model := os.environ.get("CAVEMAN_JUDGE_MODEL"):
        cmd += ["--model", model]
    cmd.append(prompt)
    out = subprocess.run(cmd, capture_output=True, text=True, check=True)
    return out.stdout.strip()


def extract_verdicts(raw: str, n_facts: int) -> list[bool] | None:
    """Pull the verdicts array out of the judge output, tolerating code fences."""
    # Strip ```json ... ``` fences if present.
    fenced = re.search(r"```(?:json)?\s*(.*?)```", raw, re.DOTALL)
    candidate = fenced.group(1) if fenced else raw
    # Find the first {...} object.
    obj = re.search(r"\{.*\}", candidate, re.DOTALL)
    if not obj:
        return None
    try:
        data = json.loads(obj.group(0))
    except json.JSONDecodeError:
        return None
    verdicts = data.get("verdicts")
    if not isinstance(verdicts, list) or len(verdicts) != n_facts:
        return None
    return [bool(v) for v in verdicts]


def judge_facts(question: str, answer: str, facts: list[str], runs: int) -> list[bool]:
    """Return one boolean per fact, majority-voted across `runs` judge calls."""
    prompt = build_judge_prompt(question, answer, facts)
    tallies: list[Counter] = [Counter() for _ in facts]
    collected = 0
    for _ in range(runs):
        raw = run_claude(prompt, JUDGE_SYSTEM)
        verdicts = extract_verdicts(raw, len(facts))
        if verdicts is None:
            # one retry with a blunter nudge
            raw = run_claude(prompt + "\n\nOutput ONLY the JSON object.", JUDGE_SYSTEM)
            verdicts = extract_verdicts(raw, len(facts))
        if verdicts is None:
            continue
        collected += 1
        for i, v in enumerate(verdicts):
            tallies[i][v] += 1
    if collected == 0:
        # Could not get a parseable verdict — fail closed (count as not satisfied)
        # so a flaky judge never silently certifies an answer as correct.
        return [False] * len(facts)
    return [t.most_common(1)[0][0] for t in tallies]


def check_verbatim(answer: str, tokens: list[str]) -> tuple[bool, list[str]]:
    """Deterministic: every token must appear verbatim (case-sensitive) in answer."""
    missing = [t for t in tokens if t not in answer]
    return (len(missing) == 0, missing)


def main() -> None:
    ap = argparse.ArgumentParser(description="Score answer fidelity against rubrics.")
    ap.add_argument("--arm", action="append", help="limit to these arms (repeatable)")
    ap.add_argument("--runs", type=int, default=1, help="judge calls per answer (majority vote)")
    ap.add_argument("--snapshot", type=Path, default=SNAPSHOT)
    ap.add_argument("--rubrics", type=Path, default=RUBRICS)
    ap.add_argument("--out", type=Path, default=FIDELITY)
    args = ap.parse_args()

    if not args.snapshot.exists():
        sys.exit(f"No snapshot at {args.snapshot}. Run evals/llm_run.py first.")
    if not args.rubrics.exists():
        sys.exit(f"No rubrics at {args.rubrics}.")

    snap = json.loads(args.snapshot.read_text(encoding="utf-8"))
    rubrics_doc = json.loads(args.rubrics.read_text(encoding="utf-8"))
    by_prompt = {r["prompt"]: r for r in rubrics_doc["rubrics"]}

    prompts: list[str] = snap["prompts"]
    arms: dict[str, list[str]] = snap["arms"]
    targets = args.arm if args.arm else list(arms.keys())

    missing_rubrics = [p for p in prompts if p not in by_prompt]
    if missing_rubrics:
        print(f"warning: {len(missing_rubrics)} prompt(s) have no rubric and will be skipped:", flush=True)
        for p in missing_rubrics:
            print(f"  - {p}", flush=True)

    result: dict = {
        "metadata": {
            "generated_at": dt.datetime.now(dt.timezone.utc).isoformat(),
            "judge_model": os.environ.get("CAVEMAN_JUDGE_MODEL", "default"),
            "runs": args.runs,
            "source_snapshot": args.snapshot.name,
        },
        "fidelity": {},
    }

    for arm in targets:
        if arm not in arms:
            print(f"warning: arm '{arm}' not in snapshot; skipping", flush=True)
            continue
        print(f"judging arm: {arm}", flush=True)
        result["fidelity"][arm] = {}
        for prompt, answer in zip(prompts, arms[arm]):
            rub = by_prompt.get(prompt)
            if rub is None:
                continue
            facts = rub.get("facts", [])
            passes = judge_facts(prompt, answer, facts, args.runs) if facts else []
            v_ok, v_missing = check_verbatim(answer, rub.get("verbatim", []))
            fidelity = (sum(passes) / len(passes) * 100.0) if passes else 100.0
            result["fidelity"][arm][prompt] = {
                "risk": rub.get("risk", "normal"),
                "fidelity": round(fidelity, 1),
                "facts": [{"fact": f, "pass": p} for f, p in zip(facts, passes)],
                "verbatim_ok": v_ok,
                "verbatim_missing": v_missing,
            }
            flag = "" if (fidelity == 100.0 and v_ok) else "  <-- below 100 / verbatim miss"
            print(f"  {fidelity:5.1f}%  {prompt[:60]}{flag}", flush=True)

    args.out.parent.mkdir(parents=True, exist_ok=True)
    args.out.write_text(json.dumps(result, ensure_ascii=False, indent=2), encoding="utf-8")
    print(f"\nWrote {args.out}")


if __name__ == "__main__":
    main()
