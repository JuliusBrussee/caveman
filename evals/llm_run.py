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
    CAVEMAN_EVAL_PROVIDER  optional provider: claude (default) or copilot
    CAVEMAN_EVAL_MODEL  optional --model value passed to selected provider CLI
    CAVEMAN_EVAL_SKILLS  optional comma-separated skill names to include
    CAVEMAN_EVAL_MAX_PROMPTS  optional max number of prompts from prompts/en.txt
"""

from __future__ import annotations

import datetime as dt
import json
import os
import textwrap
import subprocess
from pathlib import Path

EVALS = Path(__file__).parent
SKILLS = EVALS.parent / "skills"
PROMPTS = EVALS / "prompts" / "en.txt"
SNAPSHOT = EVALS / "snapshots" / "results.json"

TERSE_PREFIX = "Answer concisely."


def _provider() -> str:
    return os.environ.get("CAVEMAN_EVAL_PROVIDER", "claude").strip().lower()


def _run_claude(prompt: str, system: str | None = None) -> str:
    cmd = ["claude", "-p"]
    if system:
        cmd += ["--system-prompt", system]
    if model := os.environ.get("CAVEMAN_EVAL_MODEL"):
        cmd += ["--model", model]
    cmd.append(prompt)
    out = subprocess.run(cmd, capture_output=True, text=True, check=True)
    return out.stdout.strip()


def _copilot_prompt(prompt: str, system: str | None = None) -> str:
    if not system:
        return prompt
    return textwrap.dedent(
        f"""\
        Follow these instructions exactly:
        {system}

        User prompt:
        {prompt}
        """
    )


def _run_copilot(prompt: str, system: str | None = None) -> str:
    cmd = [
        "copilot",
        "-p",
        _copilot_prompt(prompt, system),
        "--output-format",
        "json",
        "--no-custom-instructions",
        "--disable-builtin-mcps",
        "--no-color",
    ]
    if model := os.environ.get("CAVEMAN_EVAL_MODEL"):
        cmd += ["--model", model]

    out = subprocess.run(cmd, capture_output=True, text=True, check=True)

    final = None
    for line in out.stdout.splitlines():
        line = line.strip()
        if not line:
            continue
        try:
            event = json.loads(line)
        except json.JSONDecodeError:
            continue
        if event.get("type") != "assistant.message":
            continue
        data = event.get("data") or {}
        content = data.get("content")
        if isinstance(content, str):
            final = content.strip()

    if final is None:
        raise RuntimeError(
            "copilot output did not contain assistant.message final_answer"
        )
    return final


def run_llm(prompt: str, system: str | None = None) -> str:
    provider = _provider()
    if provider == "claude":
        return _run_claude(prompt, system)
    if provider == "copilot":
        return _run_copilot(prompt, system)
    raise ValueError(
        f"Unsupported CAVEMAN_EVAL_PROVIDER={provider!r}. Use 'claude' or 'copilot'."
    )


def provider_cli_version(provider: str) -> str:
    try:
        cmd = ["claude", "--version"] if provider == "claude" else ["copilot", "--version"]
        out = subprocess.run(
            cmd, capture_output=True, text=True, check=True
        )
        return out.stdout.strip()
    except Exception:
        return "unknown"


def selected_skills(all_skills: list[str]) -> list[str]:
    raw = os.environ.get("CAVEMAN_EVAL_SKILLS", "").strip()
    if not raw:
        return all_skills

    wanted = {s.strip() for s in raw.split(",") if s.strip()}
    chosen = [s for s in all_skills if s in wanted]
    if not chosen:
        raise ValueError(
            "CAVEMAN_EVAL_SKILLS did not match any skill directories with SKILL.md"
        )
    return chosen


def selected_prompts(all_prompts: list[str]) -> list[str]:
    raw = os.environ.get("CAVEMAN_EVAL_MAX_PROMPTS", "").strip()
    if not raw:
        return all_prompts

    max_prompts = int(raw)
    if max_prompts <= 0:
        raise ValueError("CAVEMAN_EVAL_MAX_PROMPTS must be > 0")
    return all_prompts[:max_prompts]


def main() -> None:
    all_prompts = [p.strip() for p in PROMPTS.read_text().splitlines() if p.strip()]
    prompts = selected_prompts(all_prompts)
    all_skills = sorted(p.name for p in SKILLS.iterdir() if (p / "SKILL.md").exists())
    skills = selected_skills(all_skills)
    provider = _provider()

    print(
        f"=== {len(prompts)} prompts × ({len(skills)} skills + 2 control arms) ===",
        flush=True,
    )

    snapshot: dict = {
        "metadata": {
            "generated_at": dt.datetime.now(dt.timezone.utc).isoformat(),
            "provider": provider,
            "provider_cli_version": provider_cli_version(provider),
            "model": os.environ.get("CAVEMAN_EVAL_MODEL", "default"),
            "n_prompts": len(prompts),
            "n_prompts_total": len(all_prompts),
            "skills": skills,
            "n_skills_total": len(all_skills),
            "terse_prefix": TERSE_PREFIX,
        },
        "prompts": prompts,
        "arms": {},
    }

    print("baseline (no system prompt)", flush=True)
    snapshot["arms"]["__baseline__"] = [run_llm(p) for p in prompts]

    print("terse (control: terse instruction only, no skill)", flush=True)
    snapshot["arms"]["__terse__"] = [
        run_llm(p, system=TERSE_PREFIX) for p in prompts
    ]

    for skill in skills:
        skill_md = (SKILLS / skill / "SKILL.md").read_text()
        system = f"{TERSE_PREFIX}\n\n{skill_md}"
        print(f"  {skill}", flush=True)
        snapshot["arms"][skill] = [run_llm(p, system=system) for p in prompts]

    SNAPSHOT.parent.mkdir(parents=True, exist_ok=True)
    SNAPSHOT.write_text(json.dumps(snapshot, ensure_ascii=False, indent=2))
    print(f"\nWrote {SNAPSHOT}")


if __name__ == "__main__":
    main()
