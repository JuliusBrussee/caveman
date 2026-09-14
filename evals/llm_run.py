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

Every call runs with the user's settings, hooks, CLAUDE.md, skills, MCP
servers and tools switched off. Without that, the user's own CLAUDE.md
leaks into every arm (measured: a probe prompt saw it in the baseline),
and any hook or plugin that injects style rules would do the same.

Requires:
  - `claude` CLI on PATH (Claude Code), authenticated

Run: uv run python evals/llm_run.py

Environment:
  CAVEMAN_EVAL_MODEL   optional --model flag value passed through to claude
  CAVEMAN_EVAL_LANG    prompt language: picks prompts/<lang>.txt and the
                       terse prefix for that language (default: en). Any
                       language other than en writes snapshots/results.<lang>.json
  CAVEMAN_EVAL_SKILLS  comma-separated skill names to run (default: every
                       skills/*/SKILL.md)
"""

from __future__ import annotations

import datetime as dt
import json
import os
import shutil
import subprocess
import sys
import tempfile
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

# One terse control per language. The control must be in the prompt's
# language: an English "Answer concisely." on a Portuguese question also
# nudges the model toward English, which is a second variable.
TERSE_PREFIXES = {
    "en": "Answer concisely.",
    "pt": "Responda de forma concisa.",
}

LANG = os.environ.get("CAVEMAN_EVAL_LANG", "en")
PROMPTS = EVALS / "prompts" / f"{LANG}.txt"
SNAPSHOT = EVALS / "snapshots" / (
    "results.json" if LANG == "en" else f"results.{LANG}.json"
)

TERSE_PREFIX = TERSE_PREFIXES[LANG]

# Isolate the call from the local machine: no settings/hooks, no CLAUDE.md
# (measured leaking into the baseline), no skills list, no MCP servers,
# no tools. `--bare` would do all of this
# but also skips keychain reads, so it cannot authenticate.
ISOLATION = [
    "--setting-sources", "",
    "--disable-slash-commands",
    "--tools", "",
    "--strict-mcp-config",
    "--no-session-persistence",
]


def claude_bin() -> str:
    """Resolve the CLI through PATHEXT. npm installs it as claude.CMD on
    Windows and CreateProcess does not apply PATHEXT, so a bare "claude"
    raises FileNotFoundError there."""
    return shutil.which("claude") or "claude"


def run_claude(prompt: str, system: str | None = None) -> str:
    cmd = [claude_bin(), "-p", *ISOLATION]
    if model := os.environ.get("CAVEMAN_EVAL_MODEL"):
        cmd += ["--model", model]
    cmd.append(prompt)
    # The skill arm's system prompt is a multi-line SKILL.md with quotes,
    # backticks and `&`. On Windows the CLI is claude.CMD, which cmd.exe
    # re-parses, and that mangles such an argument into an empty prompt
    # ("Input must be provided either through stdin or as a prompt
    # argument"). A file sidesteps every shell.
    with tempfile.TemporaryDirectory() as tmp:
        if system:
            system_file = Path(tmp) / "system.md"
            system_file.write_text(system, encoding="utf-8")
            cmd += ["--system-prompt-file", str(system_file)]
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
    if wanted := os.environ.get("CAVEMAN_EVAL_SKILLS"):
        wanted_set = {w.strip() for w in wanted.split(",") if w.strip()}
        missing = wanted_set - set(skills)
        if missing:
            raise SystemExit(f"CAVEMAN_EVAL_SKILLS: no SKILL.md for {sorted(missing)}")
        skills = [s for s in skills if s in wanted_set]

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
        skill_md = (SKILLS / skill / "SKILL.md").read_text(encoding="utf-8")
        system = f"{TERSE_PREFIX}\n\n{skill_md}"
        print(f"  {skill}", flush=True)
        snapshot["arms"][skill] = [run_claude(p, system=system) for p in prompts]

    SNAPSHOT.parent.mkdir(parents=True, exist_ok=True)
    SNAPSHOT.write_text(json.dumps(snapshot, ensure_ascii=False, indent=2), encoding="utf-8")
    print(f"\nWrote {SNAPSHOT}")


if __name__ == "__main__":
    main()
