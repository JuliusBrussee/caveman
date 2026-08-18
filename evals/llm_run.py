"""
Run each prompt through Claude Code in three conditions and snapshot the
real LLM outputs:

  1. baseline      — no extra system prompt at all
  2. terse         — system prompt: "Answer concisely."
  3. terse+skill   — system prompt: "Answer concisely.\n\n{SKILL.md}"

The honest delta is (3) vs (2): how much does the SKILL itself add on top
of a plain "be terse" instruction? Comparing (3) vs (1) conflates the
skill with the generic terseness ask, which is what the previous version
of this harness did.

This is the source-of-truth generator. It calls a real LLM and produces
evals/snapshots/results.json. Run it locally when SKILL.md files change.
The CI-side `measure.py` only reads the snapshot and counts tokens.

Requires:
  - `claude` CLI on PATH (Claude Code), authenticated

Run: uv run python evals/llm_run.py

Environment:
  CAVEMAN_EVAL_MODEL  optional --model flag value passed through to claude
"""

from __future__ import annotations

import datetime as dt
import json
import os
import subprocess
from pathlib import Path

EVALS = Path(__file__).parent
SKILLS = EVALS.parent / "skills"

# Language of the prompt set. Defaults to "en" so existing invocations and the
# committed snapshot path are unchanged. Any other value reads
# prompts/<lang>.txt and writes snapshots/results.<lang>.json, so per-language
# snapshots never overwrite each other.
LANG = os.environ.get("CAVEMAN_EVAL_LANG", "en")
PROMPTS = EVALS / "prompts" / f"{LANG}.txt"
SNAPSHOT = EVALS / "snapshots" / (
    "results.json" if LANG == "en" else f"results.{LANG}.json"
)

# The terse control arm has to be written in the prompt set's language. An
# English "Answer concisely." in front of a French prompt set measures a
# language switch on top of terseness, which is not the control we want.
TERSE_PREFIX_BY_LANG = {
    "en": "Answer concisely.",
    "fr": "Réponds de façon concise.",
}
TERSE_PREFIX = TERSE_PREFIX_BY_LANG.get(LANG, TERSE_PREFIX_BY_LANG["en"])


def run_claude(prompt: str, system: str | None = None) -> str:
    cmd = ["claude", "-p"]
    if system:
        cmd += ["--system-prompt", system]
    if model := os.environ.get("CAVEMAN_EVAL_MODEL"):
        cmd += ["--model", model]
    cmd.append(prompt)
    out = subprocess.run(cmd, capture_output=True, text=True, check=True)
    return out.stdout.strip()


def claude_version() -> str:
    try:
        out = subprocess.run(
            ["claude", "--version"], capture_output=True, text=True, check=True
        )
        return out.stdout.strip()
    except Exception:
        return "unknown"


def main() -> None:
    if not PROMPTS.exists():
        available = sorted(p.stem for p in (EVALS / "prompts").glob("*.txt"))
        raise SystemExit(
            f"No prompt set at {PROMPTS}. "
            f"CAVEMAN_EVAL_LANG={LANG!r}; available: {', '.join(available)}"
        )
    prompts = [p.strip() for p in PROMPTS.read_text(encoding="utf-8").splitlines() if p.strip()]
    skills = sorted(p.name for p in SKILLS.iterdir() if (p / "SKILL.md").exists())

    print(
        f"=== {len(prompts)} prompts × ({len(skills)} skills + 2 control arms) ===",
        flush=True,
    )

    snapshot: dict = {
        "metadata": {
            "generated_at": dt.datetime.now(dt.timezone.utc).isoformat(),
            "claude_cli_version": claude_version(),
            "model": os.environ.get("CAVEMAN_EVAL_MODEL", "default"),
            "n_prompts": len(prompts),
            "lang": LANG,
            "terse_prefix": TERSE_PREFIX,
        },
        "prompts": prompts,
        "arms": {},
    }

    print("baseline (no system prompt)", flush=True)
    snapshot["arms"]["__baseline__"] = [run_claude(p) for p in prompts]

    print("terse (control: terse instruction only, no skill)", flush=True)
    snapshot["arms"]["__terse__"] = [
        run_claude(p, system=TERSE_PREFIX) for p in prompts
    ]

    for skill in skills:
        skill_md = (SKILLS / skill / "SKILL.md").read_text()
        system = f"{TERSE_PREFIX}\n\n{skill_md}"
        print(f"  {skill}", flush=True)
        snapshot["arms"][skill] = [run_claude(p, system=system) for p in prompts]

    SNAPSHOT.parent.mkdir(parents=True, exist_ok=True)
    SNAPSHOT.write_text(json.dumps(snapshot, ensure_ascii=False, indent=2))
    print(f"\nWrote {SNAPSHOT}")


if __name__ == "__main__":
    main()
