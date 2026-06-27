"""
evals/gate.py — the two-gate accept rule.

Generalizes the project's "honest delta = skill vs terse" philosophy into
"honest accept = saves tokens AND stays correct". Compares a BASELINE (the
committed SKILL) against a CANDIDATE (a proposed change) and exits non-zero
unless BOTH hold for the target arm:

  TOKEN GATE    candidate per-prompt token savings do not regress beyond the
                noise floor: median(savings_candidate) >= median(savings_baseline)
                - token_noise.

  QUALITY GATE  candidate fidelity does not drop beyond tolerance:
                  * mean fidelity drop across prompts <= fidelity_tol (default 2 pts)
                  * no single prompt drops more than its risk band's hard_tol
                    (normal: 10 pts, high: 0 pts)
                  * every verbatim invariant still holds (a miss is an absolute fail)

High-risk prompts (rubric risk == "high", e.g. the git-rebase "rewrites history"
warning) get hard_tol = 0: zero fidelity loss and zero verbatim misses allowed,
because compression there is historically dangerous.

The decision logic is pure and offline (no LLM, tiktoken only for the token
axis), so it is unit-tested in tests/test_eval_gate.py — including the canonical
acceptance test: a "reply k to everything" candidate MUST be rejected.

Inputs are two snapshot directories, each with results.json + fidelity.json:

  uv run --with tiktoken python evals/gate.py \
      --baseline evals/snapshots \
      --candidate evals/snapshots-candidate \
      --arm caveman

Workflow: commit the current snapshot+fidelity as the baseline; for a proposed
SKILL change, regenerate results.json (llm_run.py) + fidelity.json (judge.py)
into a candidate dir, then run this gate. Keep the committed baseline pinned to
the ORIGINAL so serial -1.9pt changes can't ratchet quality down unnoticed.
"""

from __future__ import annotations

import argparse
import json
import statistics
import sys
from pathlib import Path

# Default thresholds. token_noise and fidelity_tol should be calibrated from the
# measured same-SKILL run-to-run noise floor (run llm_run.py + judge.py twice on
# an unchanged SKILL and look at the spread) before being trusted in CI.
DEFAULT_CFG = {
    "token_noise": 0.03,  # fraction (3 percentage points of savings)
    "fidelity_tol": 2.0,  # mean fidelity-point drop allowed
    "tolerances": {
        "normal": {"hard_tol": 10.0},
        "high": {"hard_tol": 0.0},
    },
}


def token_savings(results: dict, arm: str, count_fn) -> list[float]:
    """Per-prompt savings = 1 - arm_tokens / terse_tokens (terse control arm)."""
    terse = results["arms"]["__terse__"]
    skill = results["arms"][arm]
    out = []
    for s, t in zip(skill, terse):
        tt = count_fn(t)
        out.append(1 - (count_fn(s) / tt) if tt else 0.0)
    return out


def _hard_tol(cfg: dict, risk: str) -> float:
    return cfg["tolerances"].get(risk, cfg["tolerances"]["normal"])["hard_tol"]


def evaluate_gate(base_results, base_fid, cand_results, cand_fid, arm, cfg, count_fn):
    """Pure decision. Returns {passed, reasons, metrics}. No I/O, no LLM."""
    reasons: list[str] = []
    metrics: dict = {}

    # ---- TOKEN GATE ----
    base_sav = statistics.median(token_savings(base_results, arm, count_fn))
    cand_sav = statistics.median(token_savings(cand_results, arm, count_fn))
    metrics["token_savings_baseline"] = round(base_sav, 4)
    metrics["token_savings_candidate"] = round(cand_sav, 4)
    if cand_sav < base_sav - cfg["token_noise"]:
        reasons.append(
            f"TOKEN: median savings regressed {base_sav:.1%} -> {cand_sav:.1%} "
            f"(beyond {cfg['token_noise']:.0%} noise floor)"
        )

    # ---- QUALITY GATE ----
    base_arm = base_fid["fidelity"].get(arm, {})
    cand_arm = cand_fid["fidelity"].get(arm, {})
    shared = [p for p in base_arm if p in cand_arm]
    if not shared:
        reasons.append(f"QUALITY: no shared judged prompts for arm '{arm}'")
        return {"passed": False, "reasons": reasons, "metrics": metrics}

    drops: list[float] = []
    for p in shared:
        b, c = base_arm[p], cand_arm[p]
        risk = c.get("risk", b.get("risk", "normal"))
        drop = b["fidelity"] - c["fidelity"]
        drops.append(drop)
        hard = _hard_tol(cfg, risk)
        if drop > hard:
            reasons.append(
                f"QUALITY: '{p[:50]}' fidelity dropped {drop:.0f}pt "
                f"(> {hard:.0f}pt hard limit for risk={risk})"
            )
        # Verbatim invariants are absolute: a miss in the candidate fails outright.
        if not c.get("verbatim_ok", True):
            reasons.append(
                f"VERBATIM: '{p[:50]}' is missing required token(s) "
                f"{c.get('verbatim_missing', [])} (risk={risk})"
            )

    mean_drop = statistics.mean(drops) if drops else 0.0
    metrics["mean_fidelity_drop"] = round(mean_drop, 2)
    metrics["max_fidelity_drop"] = round(max(drops), 2) if drops else 0.0
    if mean_drop > cfg["fidelity_tol"]:
        reasons.append(
            f"QUALITY: mean fidelity dropped {mean_drop:.1f}pt "
            f"(> {cfg['fidelity_tol']:.1f}pt tolerance)"
        )

    return {"passed": len(reasons) == 0, "reasons": reasons, "metrics": metrics}


def load_snapshot(d: Path) -> tuple[dict, dict]:
    results = json.loads((d / "results.json").read_text(encoding="utf-8"))
    fidelity = json.loads((d / "fidelity.json").read_text(encoding="utf-8"))
    return results, fidelity


def main() -> None:
    ap = argparse.ArgumentParser(description="Two-gate accept rule for a SKILL change.")
    ap.add_argument("--baseline", type=Path, required=True, help="dir with results.json + fidelity.json")
    ap.add_argument("--candidate", type=Path, required=True, help="dir with results.json + fidelity.json")
    ap.add_argument("--arm", required=True, help="skill arm to gate, e.g. caveman")
    ap.add_argument("--token-noise", type=float, default=DEFAULT_CFG["token_noise"])
    ap.add_argument("--fidelity-tol", type=float, default=DEFAULT_CFG["fidelity_tol"])
    args = ap.parse_args()

    import tiktoken  # lazy: keep the module importable (and unit-testable) without it

    enc = tiktoken.get_encoding("o200k_base")
    count_fn = lambda s: len(enc.encode(s))

    cfg = dict(DEFAULT_CFG)
    cfg["token_noise"] = args.token_noise
    cfg["fidelity_tol"] = args.fidelity_tol

    base_results, base_fid = load_snapshot(args.baseline)
    cand_results, cand_fid = load_snapshot(args.candidate)

    res = evaluate_gate(base_results, base_fid, cand_results, cand_fid, args.arm, cfg, count_fn)

    print(f"Gate for arm '{args.arm}':")
    for k, v in res["metrics"].items():
        print(f"  {k}: {v}")
    if res["passed"]:
        print("\nACCEPT: saves tokens and holds correctness.")
        sys.exit(0)
    print("\nREJECT:")
    for r in res["reasons"]:
        print(f"  - {r}")
    sys.exit(1)


if __name__ == "__main__":
    main()
