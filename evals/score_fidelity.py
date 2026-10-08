"""
Score a fidelity snapshot: did each arm keep the facts, negations, language
and safety wording the case asks for?

Every case in prompts/fidelity.json carries deterministic regex checks.
A case passes for an arm when every `must_include` pattern matches the
output and no `must_not_include` pattern does. Patterns are matched
case-insensitively with re.search; use `(?-i:...)` for a case-sensitive
part and `(?s)` to let `.` cross lines. No judge model: the same snapshot
always gives the same score, and checks can be refined and re-scored
offline without new LLM calls.

Generate the snapshot (calls Claude once per case × arm):
  CAVEMAN_EVAL_SET=fidelity python3 evals/llm_run.py

Score it (offline, stdlib only):
  python3 evals/score_fidelity.py [snapshots/fidelity.json]
"""

from __future__ import annotations

import json
import re
import sys
from pathlib import Path

from snapshot_contract import CONTROL_ARMS, SnapshotContractError, load_snapshot

for _stream in (sys.stdout, sys.stderr):
    try:
        _stream.reconfigure(errors="replace")
    except Exception:
        pass


EVALS = Path(__file__).parent
CASES = EVALS / "prompts" / "fidelity.json"
SNAPSHOT = EVALS / "snapshots" / "fidelity.json"

# Category keys from #1061's semantic taxonomy; descriptions in evals/README.md.
CATEGORIES = (
    "substance-preservation",
    "exact-preservation",
    "no-caricature",
    "language-and-grammar",
    "safety-clarity",
    "artifact-boundary",
    "mode-boundaries",
)


def check(output: str, checks: dict) -> list[str]:
    """Failed checks for one output; empty means the case passed."""
    failures = [
        f"missing /{p}/" for p in checks.get("must_include", [])
        if not re.search(p, output, re.IGNORECASE)
    ]
    failures += [
        f"forbidden /{p}/" for p in checks.get("must_not_include", [])
        if re.search(p, output, re.IGNORECASE)
    ]
    return failures


def score(snapshot: dict, cases: list[dict]) -> dict[str, list[tuple[dict, list[str]]]]:
    """Per arm, (case, failures) for every snapshot prompt."""
    by_prompt = {case["prompt"]: case for case in cases}
    if unknown := [p for p in snapshot["prompts"] if p not in by_prompt]:
        raise SystemExit(
            f"snapshot prompts not in {CASES.name}: {unknown}. "
            "Rerun CAVEMAN_EVAL_SET=fidelity python3 evals/llm_run.py"
        )
    if missing := [c["id"] for c in cases if c["prompt"] not in snapshot["prompts"]]:
        raise SystemExit(
            f"cases not in the snapshot: {missing}. "
            "Rerun CAVEMAN_EVAL_SET=fidelity python3 evals/llm_run.py"
        )
    return {
        arm: [
            (by_prompt[prompt], check(output, by_prompt[prompt]["checks"]))
            for prompt, output in zip(snapshot["prompts"], outputs)
        ]
        for arm, outputs in snapshot["arms"].items()
    }


def main(argv: list[str]) -> None:
    path = Path(argv[0]) if argv else SNAPSHOT
    try:
        snapshot = load_snapshot(path)
    except SnapshotContractError as error:
        raise SystemExit(f"Invalid snapshot: {error}") from error
    cases = json.loads(CASES.read_text(encoding="utf-8"))["cases"]
    results = score(snapshot, cases)
    # Controls first, then skills in snapshot order.
    arms = sorted(results, key=lambda arm: arm not in CONTROL_ARMS)
    meta = snapshot["metadata"]

    print(f"_Generated: {meta['generated_at']}_")
    print(f"_Model: {meta['model']} · CLI: {meta['claude_cli_version']}_")
    print(f"_n = {len(snapshot['prompts'])} cases, single run per arm, regex checks from prompts/{CASES.name}_")
    print()
    print("| Arm | Passed | Rate |")
    print("|-----|--------|------|")
    for arm in arms:
        passed = sum(not failures for _, failures in results[arm])
        total = len(results[arm])
        print(f"| `{arm}` | {passed} / {total} | {passed / total:.0%} |")

    print()
    print("**By category:**")
    print()
    print("| Category | n | " + " | ".join(f"`{arm}`" for arm in arms) + " |")
    print("|----------|---|" + "|".join("---" for _ in arms) + "|")
    for category in CATEGORIES:
        cells = []
        n = 0
        for arm in arms:
            rows = [f for case, f in results[arm] if case["category"] == category]
            n = len(rows)
            cells.append(f"{sum(not f for f in rows)} / {n}")
        if n:
            print(f"| {category} | {n} | " + " | ".join(cells) + " |")

    print()
    print("**Failures:**")
    print()
    for arm in arms:
        for case, failures in results[arm]:
            if failures:
                print(f"- `{arm}` · {case['id']}: {'; '.join(failures)}")
    print()
    print(f"_Source: {path.name}. A pass is a regex match, not a reviewed judgment; read the failures before citing a rate._")


if __name__ == "__main__":
    main(sys.argv[1:])
