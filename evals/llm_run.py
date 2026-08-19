"""Run each prompt through Claude Code in three conditions and snapshot the
real LLM outputs:

  1. baseline      - no extra system prompt at all
  2. terse         - system prompt: "Answer concisely."
  3. terse+skill   - system prompt: "Answer concisely.\n\n{SKILL.md}"

The honest delta is (3) vs (2). Run locally when SKILL.md files change.
Requires: `claude` CLI on PATH (Claude Code), authenticated.

Run: uv run python evals/llm_run.py

Fault tolerance: a single failed `claude -p` call used to abort the whole
multi-dollar run and write nothing. Now every call is isolated - a failure
is recorded as an `__ERROR__:` marker and the run continues, saving a
partial snapshot (results.partial.json) instead of throwing all progress
away. Set CAVEMAN_EVAL_TIMEOUT to bound a single hung call.
"""

from __future__ import annotations

import datetime as dt
import json
import os
import subprocess
import sys
from pathlib import Path

EVALS = Path(__file__).parent
SKILLS = EVALS.parent / "skills"
PROMPTS = EVALS / "prompts" / "en.txt"
SNAPSHOT = EVALS / "snapshots" / "results.json"

TERSE_PREFIX = "Answer concisely."

# Cap a single `claude -p` call so a hang can't block the whole run forever.
CALL_TIMEOUT = float(os.environ.get("CAVEMAN_EVAL_TIMEOUT", "300"))


def run_claude(prompt: str, system: str | None = None, timeout: float = CALL_TIMEOUT) -> str:
    cmd = ["claude", "-p"]
    if system:
        cmd += ["--system-prompt", system]
    if model := os.environ.get("CAVEMAN_EVAL_MODEL"):
        cmd += ["--model", model]
    cmd.append(prompt)
    out = subprocess.run(cmd, capture_output=True, text=True, check=True, timeout=timeout)
    return out.stdout.strip()


def safe_run(prompt: str, system: str | None = None) -> str:
    """run_claude that never raises. Returns the output, or an `__ERROR__:`
    marker on any failure, so one bad call can't abort the entire run."""
    try:
        return run_claude(prompt, system)
    except subprocess.TimeoutExpired as e:
        return f"__ERROR__: timeout after {int(e.timeout)}s"
    except subprocess.CalledProcessError as e:
        return f"__ERROR__: claude exited {e.returncode}"
    except Exception as e:  # noqa: BLE001 - survive any failure, record it
        return f"__ERROR__: {type(e).__name__}: {e}"


def claude_version() -> str:
    try:
        out = subprocess.run(["claude", "--version"], capture_output=True, text=True, check=True)
        return out.stdout.strip()
    except Exception:
        return "unknown"


def _run_arm(prompts, system, errors, label):
    out = []
    for p in prompts:
        r = safe_run(p, system)
        out.append(r)
        if isinstance(r, str) and r.startswith("__ERROR__"):
            errors.append(f"{label}: {r}")
    return out


def main() -> None:
    prompts = [p.strip() for p in PROMPTS.read_text().splitlines() if p.strip()]
    skills = sorted(p.name for p in SKILLS.iterdir() if (p / "SKILL.md").exists())

    print(f"=== {len(prompts)} prompts x ({len(skills)} skills + 2 control arms) ===", flush=True)

    errors: list[str] = []
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
    snapshot["arms"]["__baseline__"] = _run_arm(prompts, None, errors, "baseline")

    print("terse (control: terse instruction only, no skill)", flush=True)
    snapshot["arms"]["__terse__"] = _run_arm(prompts, TERSE_PREFIX, errors, "terse")

    for skill in skills:
        skill_md = (SKILLS / skill / "SKILL.md").read_text()
        system = f"{TERSE_PREFIX}\n\n{skill_md}"
        print(f"  {skill}", flush=True)
        snapshot["arms"][skill] = _run_arm(prompts, system, errors, skill)

    SNAPSHOT.parent.mkdir(parents=True, exist_ok=True)

    if errors:
        # Save partial progress instead of discarding the whole run.
        partial = SNAPSHOT.with_name("results.partial.json")
        partial.write_text(json.dumps(snapshot, ensure_ascii=False, indent=2))
        sys.stderr.write(f"\ncaveman-eval: {len(errors)} call(s) failed; wrote PARTIAL snapshot to {partial}\n")
        for e in errors:
            sys.stderr.write(f"  - {e}\n")
        sys.exit(1)

    SNAPSHOT.write_text(json.dumps(snapshot, ensure_ascii=False, indent=2))
    print(f"\nWrote {SNAPSHOT}")


if __name__ == "__main__":
    main()
