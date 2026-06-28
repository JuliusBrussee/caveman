"""
Offline unit tests for evals/gate.py — the two-gate accept rule.

Pure decision logic: no LLM, no tiktoken (a char-count stands in for the
tokenizer). The canonical acceptance test is that a "reply k to everything"
candidate — which crushes tokens but destroys correctness — is REJECTED.

Run: python -m unittest tests.test_eval_gate
"""

import sys
import unittest
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(REPO_ROOT / "evals"))

import gate  # noqa: E402  (evals/gate.py)

COUNT = len  # char count stands in for the tokenizer — deterministic, no deps
PROMPTS = ["p-react", "p-tcp", "p-rebase"]
RISKS = {"p-react": "normal", "p-tcp": "normal", "p-rebase": "high"}


def results(arm, arm_answers):
    """Build a snapshot-shaped results dict with a terse control of 100 chars each."""
    return {
        "prompts": PROMPTS,
        "arms": {
            "__terse__": ["x" * 100 for _ in PROMPTS],
            arm: arm_answers,
        },
    }


def fidelity(arm, per_prompt):
    """per_prompt: {prompt: (fidelity, verbatim_ok)}."""
    return {
        "fidelity": {
            arm: {
                p: {
                    "risk": RISKS[p],
                    "fidelity": fid,
                    "verbatim_ok": vok,
                    "verbatim_missing": [] if vok else ["TOKEN"],
                }
                for p, (fid, vok) in per_prompt.items()
            }
        }
    }


ARM = "caveman"
# Baseline: ~60% token savings, perfect fidelity everywhere.
BASE_RESULTS = results(ARM, ["x" * 40 for _ in PROMPTS])
BASE_FID = fidelity(ARM, {p: (100.0, True) for p in PROMPTS})


def run(cand_results, cand_fid, cfg=None):
    return gate.evaluate_gate(
        BASE_RESULTS, BASE_FID, cand_results, cand_fid, ARM,
        cfg or gate.DEFAULT_CFG, COUNT,
    )


class EvalGateTests(unittest.TestCase):
    def test_reply_k_to_everything_is_rejected(self):
        # Crushes tokens (len 1) but fidelity collapses to 0 — the canonical
        # failure the README warns about. Must be REJECTED.
        cand_results = results(ARM, ["k" for _ in PROMPTS])
        cand_fid = fidelity(ARM, {p: (0.0, True) for p in PROMPTS})
        res = run(cand_results, cand_fid)
        self.assertFalse(res["passed"])
        self.assertTrue(any("QUALITY" in r for r in res["reasons"]), res["reasons"])

    def test_identical_candidate_is_accepted(self):
        res = run(BASE_RESULTS, BASE_FID)
        self.assertTrue(res["passed"], res["reasons"])

    def test_token_win_holding_fidelity_is_accepted(self):
        # Shorter answers (more savings), fidelity unchanged.
        cand_results = results(ARM, ["x" * 20 for _ in PROMPTS])
        cand_fid = fidelity(ARM, {p: (100.0, True) for p in PROMPTS})
        res = run(cand_results, cand_fid)
        self.assertTrue(res["passed"], res["reasons"])

    def test_high_risk_one_point_drop_is_rejected(self):
        # 1pt fidelity drop on the high-risk (git rebase) prompt — hard_tol=0.
        cand_fid = fidelity(ARM, {
            "p-react": (100.0, True),
            "p-tcp": (100.0, True),
            "p-rebase": (99.0, True),
        })
        res = run(BASE_RESULTS, cand_fid)
        self.assertFalse(res["passed"])
        self.assertTrue(any("rebase" in r for r in res["reasons"]), res["reasons"])

    def test_normal_risk_small_drop_within_tolerance_is_accepted(self):
        # A 1pt drop on a single normal-risk prompt: mean drop 0.33 <= 2, and
        # under the 10pt hard limit. Accept.
        cand_fid = fidelity(ARM, {
            "p-react": (99.0, True),
            "p-tcp": (100.0, True),
            "p-rebase": (100.0, True),
        })
        res = run(BASE_RESULTS, cand_fid)
        self.assertTrue(res["passed"], res["reasons"])

    def test_verbatim_miss_is_rejected(self):
        cand_fid = fidelity(ARM, {
            "p-react": (100.0, True),
            "p-tcp": (100.0, False),  # dropped a required verbatim token
            "p-rebase": (100.0, True),
        })
        res = run(BASE_RESULTS, cand_fid)
        self.assertFalse(res["passed"])
        self.assertTrue(any("VERBATIM" in r for r in res["reasons"]), res["reasons"])

    def test_token_regression_is_rejected(self):
        # Candidate answers longer than the terse control → negative savings,
        # far below baseline's 60%. Fidelity is fine, but the token gate fails.
        cand_results = results(ARM, ["x" * 120 for _ in PROMPTS])
        cand_fid = fidelity(ARM, {p: (100.0, True) for p in PROMPTS})
        res = run(cand_results, cand_fid)
        self.assertFalse(res["passed"])
        self.assertTrue(any("TOKEN" in r for r in res["reasons"]), res["reasons"])


if __name__ == "__main__":
    unittest.main()
