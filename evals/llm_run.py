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
import shutil
import subprocess
import sys
from pathlib import Path

# Windows consoles and piped stdout default to the ANSI code page (cp1252),
# which cannot encode the arrows, em-dashes and minus signs printed below —
# a diagnostic that crashes instead of printing is worse than useless
# (#203/#459). Replace unencodable characters rather than raising.
for _stream in (sys.stdout, sys.stderr):
    try:
        _stream.reconfigure(errors="replace")
    except Exception:
        pass


EVALS = Path(__file__).parent
SKILLS = EVALS.parent / "skills"
PROMPTS = EVALS / "prompts" / "en.txt"
SNAPSHOT = EVALS / "snapshots" / "results.json"

TERSE_PREFIX = "Answer concisely."


def claude_bin() -> str:
    """Resolve the CLI through PATHEXT. npm installs it as claude.CMD on
    Windows and CreateProcess does not apply PATHEXT, so a bare "claude"
    raises FileNotFoundError there."""
    return shutil.which("claude") or "claude"


def run_claude(prompt: str, system: str | None = None) -> str:
    cmd = [claude_bin(), "-p"]
    if system:
        cmd += ["--system-prompt", system]
    if model := os.environ.get("CAVEMAN_EVAL_MODEL"):
        cmd += ["--model", model]
    cmd.append(prompt)
    out = subprocess.run(
        cmd, capture_output=True, text=True, check=True,
        encoding="utf-8", errors="replace",
    )
    return out.stdout.strip()


def claude_version() -> str:
    try:
        out = subprocess.run(
            [claude_bin(), "--version"], capture_output=True, text=True,
            check=True, encoding="utf-8", errors="replace",
        )
        return out.stdout.strip()
    except Exception:
        return "unknown"


def main() -> None:
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
        skill_md = (SKILLS / skill / "SKILL.md").read_text(encoding="utf-8")
        system = f"{TERSE_PREFIX}\n\n{skill_md}"
        print(f"  {skill}", flush=True)
        snapshot["arms"][skill] = [run_claude(p, system=system) for p in prompts]

    SNAPSHOT.parent.mkdir(parents=True, exist_ok=True)
    SNAPSHOT.write_text(json.dumps(snapshot, ensure_ascii=False, indent=2), encoding="utf-8")
    print(f"\nWrote {SNAPSHOT}")


if __name__ == "__main__":
    main()
