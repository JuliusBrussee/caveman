"""
Measure the per-turn reinforcement (caveman-mode-tracker.js) on top of the
caveman skill, old text vs new text, with the same prompts and control arms
as llm_run.py.

llm_run.py puts SKILL.md in the system prompt and nothing else, so it cannot
see the reminder the UserPromptSubmit hook adds to every Claude Code turn
(#1125). This script adds arms that deliver that reminder the way Claude Code
does: a real UserPromptSubmit hook, passed with --settings, whose
additionalContext is the exact text the tracker emits for a level.

Arms:
  1. __baseline__            no system prompt, no hook
  2. __terse__               "Answer concisely."
  3. reminder:<level>@base   terse + caveman SKILL.md + tracker from --base
  4. reminder:<level>@head   terse + caveman SKILL.md + tracker in this tree

Reminder text comes from running each tracker version against an isolated
CLAUDE_CONFIG_DIR, so the arms use exactly what users get. CAVEMAN_DEFAULT_MODE
is forced to "off" for every call so an installed caveman plugin on the
machine running this cannot inject its own ruleset into any arm.

Run sequentially on purpose: parallel `claude -p` calls have been seen to
drop installed plugins from settings.json on the host machine.

Run:  CAVEMAN_EVAL_MODEL=claude-opus-5-5 python evals/reminder_run.py
      uv run --with tiktoken python evals/measure.py \\
          evals/snapshots/reminder.json
"""

from __future__ import annotations

import argparse
import datetime as dt
import json
import os
import shutil
import subprocess
import sys
import tempfile
from pathlib import Path

sys.path.insert(0, str(Path(__file__).parent))
from llm_run import (  # noqa: E402
    PROMPTS, SKILLS, TERSE_PREFIX, claude_bin, claude_version,
)

ROOT = Path(__file__).parent.parent
HOOKS = ROOT / "src" / "hooks"
TRACKER = "caveman-mode-tracker.js"
SNAPSHOT = Path(__file__).parent / "snapshots" / "reminder.json"


def reminder_text(tracker_source: str, level: str) -> str:
    """additionalContext the given tracker emits on an ordinary prompt."""
    with tempfile.TemporaryDirectory(prefix="caveman-reminder-") as tmp:
        hooks = Path(tmp) / "hooks"
        shutil.copytree(HOOKS, hooks)
        (hooks / TRACKER).write_text(tracker_source, encoding="utf-8")
        config = Path(tmp) / ".claude"
        config.mkdir()
        (config / ".caveman-active").write_text(level, encoding="utf-8")
        env = os.environ.copy()
        env.pop("CAVEMAN_DEFAULT_MODE", None)
        env.update(HOME=tmp, USERPROFILE=tmp, CLAUDE_CONFIG_DIR=str(config))
        out = subprocess.run(
            ["node", str(hooks / TRACKER)], cwd=tmp, env=env,
            input=json.dumps({"prompt": "ordinary prompt"}),
            capture_output=True, text=True, check=True, encoding="utf-8",
        )
    return json.loads(out.stdout)["hookSpecificOutput"]["additionalContext"]


def hook_settings(context: str, workdir: Path, name: str) -> str:
    """--settings JSON with a UserPromptSubmit hook that emits `context`."""
    payload = workdir / f"{name}.json"
    payload.write_text(json.dumps({"hookSpecificOutput": {
        "hookEventName": "UserPromptSubmit",
        "additionalContext": context,
    }}), encoding="utf-8")
    script = ("process.stdout.write(require('fs')"
              ".readFileSync(process.argv[1],'utf8'))")
    command = f"node -e {json.dumps(script)} {json.dumps(str(payload))}"
    return json.dumps({"hooks": {"UserPromptSubmit": [
        {"hooks": [{"type": "command", "command": command}]},
    ]}})


def run(prompt: str, system: str | None = None,
        settings: str | None = None) -> str:
    cmd = [claude_bin(), "-p"]
    if system:
        cmd += ["--system-prompt", system]
    if settings:
        cmd += ["--settings", settings]
    if model := os.environ.get("CAVEMAN_EVAL_MODEL"):
        cmd += ["--model", model]
    cmd.append(prompt)
    env = dict(os.environ, CAVEMAN_DEFAULT_MODE="off")
    out = subprocess.run(
        cmd, capture_output=True, text=True, check=True, env=env,
        encoding="utf-8", errors="replace", stdin=subprocess.DEVNULL,
    )
    return out.stdout.strip()


def main() -> None:
    ap = argparse.ArgumentParser(description=__doc__.split("\n\n")[0])
    ap.add_argument("--base", default="origin/main",
                    help="git ref holding the tracker to compare against")
    ap.add_argument("--levels", default="full",
                    help="comma-separated caveman levels, e.g. full,ultra")
    args = ap.parse_args()
    levels = [x for x in args.levels.split(",") if x]

    base_src = subprocess.run(
        ["git", "show", f"{args.base}:src/hooks/{TRACKER}"], cwd=ROOT,
        capture_output=True, text=True, check=True, encoding="utf-8",
    ).stdout
    head_src = (HOOKS / TRACKER).read_text(encoding="utf-8")
    base_sha = subprocess.run(
        ["git", "rev-parse", "--short", args.base], cwd=ROOT,
        capture_output=True, text=True, check=True,
    ).stdout.strip()

    prompts = [p.strip() for p in PROMPTS.read_text(encoding="utf-8")
               .splitlines() if p.strip()]
    skill_md = (SKILLS / "caveman" / "SKILL.md").read_text(encoding="utf-8")
    system = f"{TERSE_PREFIX}\n\n{skill_md}"

    reminders = {}
    for level in levels:
        reminders[f"reminder:{level}@base"] = reminder_text(base_src, level)
        reminders[f"reminder:{level}@head"] = reminder_text(head_src, level)

    snapshot: dict = {
        "metadata": {
            "generated_at": dt.datetime.now(dt.timezone.utc).isoformat(),
            "claude_cli_version": claude_version(),
            "model": os.environ.get("CAVEMAN_EVAL_MODEL", "default"),
            "n_prompts": len(prompts),
            "terse_prefix": TERSE_PREFIX,
            "base": f"{args.base} ({base_sha})",
            "reminders": reminders,
        },
        "prompts": prompts,
        "arms": {},
    }

    n_arms = 2 + len(reminders)
    print(f"=== {len(prompts)} prompts × {n_arms} arms ===", flush=True)
    print("baseline (no system prompt)", flush=True)
    snapshot["arms"]["__baseline__"] = [run(p) for p in prompts]
    print("terse (control)", flush=True)
    snapshot["arms"]["__terse__"] = [run(p, system=TERSE_PREFIX)
                                     for p in prompts]

    with tempfile.TemporaryDirectory(prefix="caveman-eval-") as tmp:
        for i, (arm, context) in enumerate(reminders.items()):
            print(f"  {arm}", flush=True)
            settings = hook_settings(context, Path(tmp), f"arm{i}")
            snapshot["arms"][arm] = [
                run(p, system=system, settings=settings) for p in prompts
            ]

    SNAPSHOT.parent.mkdir(parents=True, exist_ok=True)
    SNAPSHOT.write_text(json.dumps(snapshot, ensure_ascii=False, indent=2),
                        encoding="utf-8")
    print(f"\nWrote {SNAPSHOT}")


if __name__ == "__main__":
    main()
