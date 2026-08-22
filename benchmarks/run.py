#!/usr/bin/env python3
"""Benchmark caveman vs normal Claude output token counts."""

import argparse
import hashlib
import json
import os
import statistics
import sys
import time
from datetime import datetime, timezone
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


try:
    import anthropic
except ModuleNotFoundError:  # Dry-run and contract tests need no paid-provider SDK.
    anthropic = None

# The only env var this benchmark needs: the anthropic SDK reads it in
# anthropic.Anthropic(). Read it — and ONLY it — from repo-root .env.local.
# Deliberately narrow (issue #528): the old loader setdefault'ed EVERY key in
# .env.local into os.environ, which security scanners rightly flag as an
# exfiltration surface. Nothing else from the file is ever read or exported.
_API_KEY_VAR = "ANTHROPIC_API_KEY"

_env_file = Path(__file__).parent.parent / ".env.local"
if _API_KEY_VAR not in os.environ and _env_file.exists():
    for line in _env_file.read_text(encoding="utf-8").splitlines():
        line = line.strip()
        if line.startswith("#") or "=" not in line:
            continue
        key, _, value = line.partition("=")
        if key.strip() == _API_KEY_VAR:
            os.environ.setdefault(_API_KEY_VAR, value.strip())
            break

SCRIPT_VERSION = "1.1.0"
SCRIPT_DIR = Path(__file__).parent
REPO_DIR = SCRIPT_DIR.parent
PROMPTS_PATH = SCRIPT_DIR / "prompts.json"
SKILL_PATH = REPO_DIR / "skills" / "caveman" / "SKILL.md"
README_PATH = REPO_DIR / "README.md"
RESULTS_DIR = SCRIPT_DIR / "results"

NORMAL_SYSTEM = "You are a helpful assistant."
# The control arm. Without it this script measures caveman against an UNPROMPTED
# baseline, which is exactly the conflation evals/llm_run.py was built to prevent:
# "how much of the reduction is the skill, and how much is any 'be terse'
# instruction?" A baseline-only comparison credits the skill for both. Same string
# the eval harness uses (evals/llm_run.py TERSE_PREFIX) so the two agree.
TERSE_SYSTEM = "Answer concisely."
BENCHMARK_START = "<!-- BENCHMARK-TABLE-START -->"
BENCHMARK_END = "<!-- BENCHMARK-TABLE-END -->"


def load_prompts():
    with open(PROMPTS_PATH, encoding="utf-8") as f:
        data = json.load(f)
    return data["prompts"]


def load_caveman_system():
    return SKILL_PATH.read_text(encoding="utf-8")


def sha256_file(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def call_api(client, model, system, prompt, max_retries=3):
    delays = [5, 10, 20]
    for attempt in range(max_retries + 1):
        try:
            response = client.messages.create(
                model=model,
                max_tokens=4096,
                temperature=0,
                system=system,
                messages=[{"role": "user", "content": prompt}],
            )
            return {
                "input_tokens": response.usage.input_tokens,
                "output_tokens": response.usage.output_tokens,
                "text": response.content[0].text,
                "stop_reason": response.stop_reason,
            }
        except anthropic.RateLimitError:
            if attempt < max_retries:
                delay = delays[min(attempt, len(delays) - 1)]
                print(f"  Rate limited, retrying in {delay}s...", file=sys.stderr)
                time.sleep(delay)
            else:
                raise


def run_benchmarks(client, model, prompts, caveman_system, trials):
    results = []
    total = len(prompts)

    for i, prompt_entry in enumerate(prompts, 1):
        pid = prompt_entry["id"]
        prompt_text = prompt_entry["prompt"]
        entry = {
            "id": pid,
            "category": prompt_entry["category"],
            "prompt": prompt_text,
            "normal": [],
            "terse": [],
            "caveman": [],
        }

        for mode, system in [
            ("normal", NORMAL_SYSTEM),
            ("terse", TERSE_SYSTEM),
            ("caveman", caveman_system),
        ]:
            for t in range(1, trials + 1):
                print(
                    f"  [{i}/{total}] {pid} | {mode} | trial {t}/{trials}",
                    file=sys.stderr,
                )
                result = call_api(client, model, system, prompt_text)
                entry[mode].append(result)
                time.sleep(0.5)

        results.append(entry)

    return results


def compute_stats(results):
    """Per-task medians plus BOTH deltas: caveman vs terse (the honest one, what
    the skill adds over any 'be terse' instruction) and caveman vs the unprompted
    baseline (what the two arms together achieve)."""
    rows = []
    all_savings = []
    all_savings_vs_terse = []

    for entry in results:
        normal_median = statistics.median([t["output_tokens"] for t in entry["normal"]])
        terse_median = statistics.median([t["output_tokens"] for t in entry["terse"]])
        caveman_median = statistics.median([t["output_tokens"] for t in entry["caveman"]])
        savings = 1 - (caveman_median / normal_median) if normal_median > 0 else 0
        savings_vs_terse = 1 - (caveman_median / terse_median) if terse_median > 0 else 0
        all_savings.append(savings)
        all_savings_vs_terse.append(savings_vs_terse)

        rows.append(
            {
                "id": entry["id"],
                "category": entry["category"],
                "prompt": entry["prompt"],
                "normal_median": int(normal_median),
                "terse_median": int(terse_median),
                "caveman_median": int(caveman_median),
                "savings_pct": round(savings * 100),
                "savings_vs_terse_pct": round(savings_vs_terse * 100),
            }
        )

    avg_normal = round(statistics.mean([r["normal_median"] for r in rows]))
    avg_terse = round(statistics.mean([r["terse_median"] for r in rows]))
    avg_caveman = round(statistics.mean([r["caveman_median"] for r in rows]))

    # Two DIFFERENT statistics, kept apart and labelled. The Average ROW prints
    # the ratio of the averaged token columns printed beside it, because a
    # mean-of-per-task-ratios in that cell contradicts the two numbers it sits
    # between (1214 and 294 is 76%, printed next to a 65% mean-of-ratios).
    return rows, {
        "avg_savings": pct(avg_normal, avg_caveman),
        "avg_savings_vs_terse": pct(avg_terse, avg_caveman),
        "mean_task_savings": round(statistics.mean(all_savings) * 100),
        "mean_task_savings_vs_terse": round(statistics.mean(all_savings_vs_terse) * 100),
        "min_savings": round(min(all_savings) * 100),
        "max_savings": round(max(all_savings) * 100),
        "min_savings_vs_terse": round(min(all_savings_vs_terse) * 100),
        "max_savings_vs_terse": round(max(all_savings_vs_terse) * 100),
        "avg_normal": avg_normal,
        "avg_terse": avg_terse,
        "avg_caveman": avg_caveman,
    }


def pct(before, after):
    """Reduction from before to after, as a whole-number percent."""
    return round((1 - after / before) * 100) if before > 0 else 0


def format_prompt_label(prompt_id):
    labels = {
        "react-rerender": "Explain React re-render bug",
        "auth-middleware-fix": "Fix auth middleware token expiry",
        "postgres-pool": "Set up PostgreSQL connection pool",
        "git-rebase-merge": "Explain git rebase vs merge",
        "async-refactor": "Refactor callback to async/await",
        "microservices-monolith": "Architecture: microservices vs monolith",
        "pr-security-review": "Review PR for security issues",
        "docker-multi-stage": "Docker multi-stage build",
        "race-condition-debug": "Debug PostgreSQL race condition",
        "error-boundary": "Implement React error boundary",
    }
    return labels.get(prompt_id, prompt_id)


def format_table(rows, summary):
    """Three arms, and the terse column is not decoration: "vs terse" is what the
    skill itself buys. "vs baseline" is the skill plus the generic terseness ask,
    and quoting it alone credits the skill for both."""
    lines = [
        "| Task | Baseline (tokens) | Terse (tokens) | Caveman (tokens) | vs terse | vs baseline |",
        "|------|-----------------:|--------------:|----------------:|--------:|-----------:|",
    ]
    for r in rows:
        label = format_prompt_label(r["id"])
        lines.append(
            f"| {label} | {r['normal_median']} | {r['terse_median']} | {r['caveman_median']} "
            f"| {r['savings_vs_terse_pct']}% | {r['savings_pct']}% |"
        )
    lines.append(
        f"| **Average** | **{summary['avg_normal']}** | **{summary['avg_terse']}** "
        f"| **{summary['avg_caveman']}** | **{summary['avg_savings_vs_terse']}%** "
        f"| **{summary['avg_savings']}%** |"
    )
    lines.append("")
    lines.append(
        f"*Average row is the ratio of the token columns beside it. Per-task means differ: "
        f"{summary['mean_task_savings_vs_terse']}% vs terse, {summary['mean_task_savings']}% vs baseline. "
        f"Per-task range vs terse: {summary['min_savings_vs_terse']}%–{summary['max_savings_vs_terse']}%.*"
    )
    return "\n".join(lines)


def save_results(results, rows, summary, model, trials, skill_hash):
    ts = datetime.now(timezone.utc).strftime("%Y%m%d_%H%M%S")
    output = {
        "metadata": {
            "script_version": SCRIPT_VERSION,
            "model": model,
            "date": datetime.now(timezone.utc).isoformat(),
            "trials": trials,
            "skill_md_sha256": skill_hash,
            "quality_evaluated": False,
            "quality_note": "Token counts do not establish semantic or technical equivalence; review raw paired outputs separately.",
        },
        "summary": summary,
        "rows": rows,
        "raw": results,
    }
    path = RESULTS_DIR / f"benchmark_{ts}.json"
    RESULTS_DIR.mkdir(parents=True, exist_ok=True)
    with open(path, "w", encoding="utf-8", newline="\n") as f:
        json.dump(output, f, indent=2)
    return path


def update_readme(table_md):
    content = README_PATH.read_text(encoding="utf-8")
    start_idx = content.find(BENCHMARK_START)
    end_idx = content.find(BENCHMARK_END)
    if start_idx == -1 or end_idx == -1:
        print(
            "ERROR: Benchmark markers not found in README.md",
            file=sys.stderr,
        )
        sys.exit(1)

    before = content[: start_idx + len(BENCHMARK_START)]
    after = content[end_idx:]
    new_content = before + "\n" + table_md + "\n" + after
    README_PATH.write_text(new_content, encoding="utf-8", newline="\n")
    print("README.md updated.", file=sys.stderr)


def dry_run(prompts, model, trials):
    print(f"Model:  {model}")
    print(f"Trials: {trials}")
    print(f"Prompts: {len(prompts)}")
    print(f"Total API calls: {len(prompts) * 2 * trials}")
    print()
    for p in prompts:
        print(f"  [{p['id']}] ({p['category']})")
        preview = p["prompt"][:80]
        if len(p["prompt"]) > 80:
            preview += "..."
        print(f"    {preview}")
    print()
    print("Dry run complete. No API calls made.")


def main():
    parser = argparse.ArgumentParser(description="Benchmark caveman vs normal Claude")
    parser.add_argument("--trials", type=int, default=3, help="Trials per prompt per mode (default: 3)")
    parser.add_argument("--dry-run", action="store_true", help="Print config, no API calls")
    parser.add_argument("--update-readme", action="store_true", help="Update README.md benchmark table")
    parser.add_argument("--model", default="claude-sonnet-4-20250514", help="Model to use")
    args = parser.parse_args()

    if args.trials < 1:
        parser.error("--trials must be at least 1")

    prompts = load_prompts()

    if args.dry_run:
        dry_run(prompts, args.model, args.trials)
        return

    if anthropic is None:
        parser.error("anthropic package required for live benchmark; install benchmarks/requirements.txt")

    caveman_system = load_caveman_system()
    skill_hash = sha256_file(SKILL_PATH)

    client = anthropic.Anthropic()

    print(f"Running benchmarks: {len(prompts)} prompts x 2 modes x {args.trials} trials", file=sys.stderr)
    print(f"Model: {args.model}", file=sys.stderr)
    print(file=sys.stderr)

    results = run_benchmarks(client, args.model, prompts, caveman_system, args.trials)
    rows, summary = compute_stats(results)
    table_md = format_table(rows, summary)

    json_path = save_results(results, rows, summary, args.model, args.trials, skill_hash)
    print(f"\nResults saved to {json_path}", file=sys.stderr)

    if args.update_readme:
        update_readme(table_md)

    print(table_md)


if __name__ == "__main__":
    main()
