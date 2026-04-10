#!/usr/bin/env python3
"""Benchmark caveman vs normal Claude output token counts.

Two modes:
  API mode (default): calls api.anthropic.com via the Anthropic SDK; requires
    ANTHROPIC_API_KEY (or ANTHROPIC_AUTH_TOKEN for bearer-token auth). Token
    counts are exact values returned by the API.
  CLI mode (--cli): calls the `claude` CLI via subprocess; works with a
    claude.ai subscription, no API key needed. Token counts are tiktoken
    (o200k_base) approximations — accurate enough for relative savings
    comparisons, but absolute numbers will differ slightly from API mode.
"""

import argparse
import functools
import hashlib
import json
import os
import statistics
import subprocess
import sys
import time
from datetime import datetime, timezone
from pathlib import Path

import anthropic
from tqdm.auto import tqdm

# Load .env.local from repo root if it exists
_env_file = Path(__file__).parent.parent / ".env.local"
if _env_file.exists():
    for line in _env_file.read_text().splitlines():
        line = line.strip()
        if line and not line.startswith("#") and "=" in line:
            key, _, value = line.partition("=")
            os.environ.setdefault(key.strip(), value.strip())

SCRIPT_VERSION = "1.0.0"
SCRIPT_DIR = Path(__file__).parent
REPO_DIR = SCRIPT_DIR.parent
PROMPTS_PATH = SCRIPT_DIR / "prompts.json"
SKILL_PATH = REPO_DIR / "skills" / "caveman" / "SKILL.md"
README_PATH = REPO_DIR / "README.md"
RESULTS_DIR = SCRIPT_DIR / "results"

NORMAL_SYSTEM = "You are a helpful assistant."
BENCHMARK_START = "<!-- BENCHMARK-TABLE-START -->"
BENCHMARK_END = "<!-- BENCHMARK-TABLE-END -->"
LEVELS = ["lite", "full", "ultra"]


def load_prompts():
    with open(PROMPTS_PATH) as f:
        data = json.load(f)
    return data["prompts"]


def load_caveman_systems():
    base = SKILL_PATH.read_text()
    return {
        level: base.replace("Default: **full**.", f"Default: **{level}**.")
        for level in LEVELS
    }


def sha256_file(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def _retry(fn, *, catch, msg_fn=str, max_retries=3):
    delays = [5, 10, 20]
    for attempt in range(max_retries + 1):
        try:
            return fn()
        except catch as e:
            if attempt < max_retries:
                delay = delays[min(attempt, len(delays) - 1)]
                print(f"  Retrying in {delay}s: {msg_fn(e)}", file=sys.stderr)
                time.sleep(delay)
            else:
                raise


def call_api(client, model, system, prompt, max_retries=3):
    def _call():
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
    return _retry(_call, catch=anthropic.RateLimitError, msg_fn=lambda _: "rate limited", max_retries=max_retries)


def call_cli(model, system, prompt, max_retries=3):
    try:
        import tiktoken
    except ImportError:
        print("ERROR: tiktoken required for CLI mode: pip install tiktoken", file=sys.stderr)
        sys.exit(1)
    cmd = ["claude", "-p", "--system-prompt", system, "--model", model, prompt]
    text = _retry(
        lambda: subprocess.run(cmd, capture_output=True, text=True, check=True).stdout.strip(),
        catch=subprocess.CalledProcessError,
        msg_fn=lambda e: e.stderr.strip(),
        max_retries=max_retries,
    )
    enc = tiktoken.get_encoding("o200k_base")
    return {
        "input_tokens": len(enc.encode(system + "\n\n" + prompt)),
        "output_tokens": len(enc.encode(text)),
        "text": text,
        "stop_reason": "end_turn",
    }


def run_benchmarks(caller, model, prompts, caveman_systems, trials, *, resume_from=None, on_prompt_done=None):
    modes = [("normal", NORMAL_SYSTEM)] + [
        (f"caveman_{level}", caveman_systems[level]) for level in LEVELS
    ]

    # Index any resumed data by prompt id
    resumed = {e["id"]: e for e in (resume_from or [])}

    # Only count calls that still need to happen
    remaining = sum(
        max(0, trials - len(resumed.get(p["id"], {}).get(mk, [])))
        for p in prompts
        for mk, _ in modes
    )

    results = []
    with tqdm(total=remaining, unit="call", file=sys.stderr) as bar:
        for prompt_entry in prompts:
            pid = prompt_entry["id"]
            prompt_text = prompt_entry["prompt"]
            prior = resumed.get(pid, {})
            entry = {
                "id": pid,
                "category": prompt_entry["category"],
                "prompt": prompt_text,
                "normal": list(prior.get("normal", [])),
                **{f"caveman_{level}": list(prior.get(f"caveman_{level}", [])) for level in LEVELS},
            }

            for mode_key, system in modes:
                already = len(entry[mode_key])
                for t in range(already + 1, trials + 1):
                    bar.set_description(f"{pid} | {mode_key} | t{t}")
                    result = caller(model, system, prompt_text)
                    entry[mode_key].append(result)
                    bar.set_postfix(out=result["output_tokens"])
                    bar.update(1)
                    time.sleep(0.5)

            results.append(entry)
            if on_prompt_done:
                on_prompt_done(results)

    return results


def compute_stats(results):
    rows = []
    all_savings = {level: [] for level in LEVELS}

    for entry in results:
        normal_median = statistics.median([t["output_tokens"] for t in entry["normal"]])
        row = {
            "id": entry["id"],
            "category": entry["category"],
            "prompt": entry["prompt"],
            "normal_median": int(normal_median),
        }
        for level in LEVELS:
            caveman_median = statistics.median(
                [t["output_tokens"] for t in entry[f"caveman_{level}"]]
            )
            savings = 1 - (caveman_median / normal_median) if normal_median > 0 else 0
            all_savings[level].append(savings)
            row[f"{level}_median"] = int(caveman_median)
            row[f"{level}_savings_pct"] = round(savings * 100)
        rows.append(row)

    summary = {"avg_normal": round(statistics.mean([r["normal_median"] for r in rows]))}
    for level in LEVELS:
        s = all_savings[level]
        summary[f"avg_{level}"] = round(statistics.mean([r[f"{level}_median"] for r in rows]))
        summary[f"avg_{level}_savings"] = round(statistics.mean(s) * 100)
        summary[f"min_{level}_savings"] = round(min(s) * 100)
        summary[f"max_{level}_savings"] = round(max(s) * 100)

    return rows, summary


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
        "error-boundary": "Error boundary vs try/catch",
    }
    return labels.get(prompt_id, prompt_id)


def format_table(rows, summary):
    lines = [
        "| Task | Normal (tokens) | Caveman [lite] | Caveman [full] | Caveman [ultra] |",
        "|------|---------------:|---------------:|---------------:|----------------:|",
    ]
    for r in rows:
        label = format_prompt_label(r["id"])
        lines.append(
            f"| {label}"
            f" | {r['normal_median']}"
            f" | {r['lite_median']} ({r['lite_savings_pct']}%)"
            f" | {r['full_median']} ({r['full_savings_pct']}%)"
            f" | {r['ultra_median']} ({r['ultra_savings_pct']}%) |"
        )
    lines.append(
        f"| **Average**"
        f" | **{summary['avg_normal']}**"
        f" | **{summary['avg_lite']}** (**{summary['avg_lite_savings']}%**)"
        f" | **{summary['avg_full']}** (**{summary['avg_full_savings']}%**)"
        f" | **{summary['avg_ultra']}** (**{summary['avg_ultra_savings']}%**) |"
    )
    lines.append("")
    range_parts = [
        f"{level}: {summary[f'min_{level}_savings']}%–{summary[f'max_{level}_savings']}%"
        for level in LEVELS
    ]
    lines.append(f"*Savings range — {'; '.join(range_parts)}.*")
    return "\n".join(lines)


def find_resumable(model, trials, skill_hash):
    """Return (path, raw_results) of the latest compatible partial run, or (None, [])."""
    if not RESULTS_DIR.exists():
        return None, []
    candidates = sorted(RESULTS_DIR.glob("benchmark_*.json"), reverse=True)
    for path in candidates:
        try:
            with open(path) as f:
                data = json.load(f)
        except (json.JSONDecodeError, OSError):
            continue
        meta = data.get("metadata", {})
        raw = data.get("raw", [])
        if (meta.get("model") == model
            and meta.get("trials") == trials
            and meta.get("skill_md_sha256") == skill_hash
            and raw
        ):
            return path, raw
    return None, []


def save_partial(results, *, model, trials, skill_hash, path):
    """Write raw results to disk without computing summary (called after each prompt)."""
    output = {
        "metadata": {
            "script_version": SCRIPT_VERSION,
            "model": model,
            "date": datetime.now(timezone.utc).isoformat(),
            "trials": trials,
            "skill_md_sha256": skill_hash,
            "partial": True,
        },
        "raw": results,
    }
    with open(path, "w") as f:
        json.dump(output, f, indent=2)


def save_results(results, rows, summary, model, trials, skill_hash, path):
    output = {
        "metadata": {
            "script_version": SCRIPT_VERSION,
            "model": model,
            "date": datetime.now(timezone.utc).isoformat(),
            "trials": trials,
            "skill_md_sha256": skill_hash,
        },
        "summary": summary,
        "rows": rows,
        "raw": results,
    }
    with open(path, "w") as f:
        json.dump(output, f, indent=2)
    return path


def update_readme(table_md):
    content = README_PATH.read_text()
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
    README_PATH.write_text(new_content)
    print("README.md updated.", file=sys.stderr)


def dry_run(prompts, model, trials):
    modes = 1 + len(LEVELS)  # normal + lite + full + ultra
    print(f"Model:  {model}")
    print(f"Trials: {trials}")
    print(f"Prompts: {len(prompts)}")
    print(f"Modes: normal + {', '.join(LEVELS)}")
    print(f"Total API calls: {len(prompts) * modes * trials}")
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
    parser.add_argument("--cli", action="store_true", help="Use claude CLI (subscription mode); token counts are tiktoken approximations")
    parser.add_argument("--resume", action="store_true", help="Resume from the latest compatible partial result file")
    args = parser.parse_args()

    prompts = load_prompts()

    if args.dry_run:
        dry_run(prompts, args.model, args.trials)
        return

    caveman_systems = load_caveman_systems()
    skill_hash = sha256_file(SKILL_PATH)

    if args.cli:
        caller = call_cli
        print("Mode: claude CLI (subscription — token counts are tiktoken approximations)", file=sys.stderr)
    else:
        auth_token = os.environ.get("ANTHROPIC_AUTH_TOKEN")
        client = anthropic.Anthropic(auth_token=auth_token) if auth_token else anthropic.Anthropic()
        caller = functools.partial(call_api, client)
        print("Mode: Anthropic API", file=sys.stderr)

    # Resume from latest compatible partial run if --resume was passed, otherwise always start fresh
    resume_path, resume_raw = find_resumable(args.model, args.trials, skill_hash) if args.resume else (None, [])
    if resume_raw:
        print(f"Resuming: {resume_path} ({len(resume_raw)}/{len(prompts)} prompts done)", file=sys.stderr)
        output_path = resume_path
    else:
        ts = datetime.now(timezone.utc).strftime("%Y%m%d_%H%M%S")
        RESULTS_DIR.mkdir(parents=True, exist_ok=True)
        output_path = RESULTS_DIR / f"benchmark_{ts}.json"

    partial_saver = functools.partial(
        save_partial, model=args.model, trials=args.trials, skill_hash=skill_hash, path=output_path
    )

    print(f"Running benchmarks: {len(prompts)} prompts x {1 + len(LEVELS)} modes x {args.trials} trials", file=sys.stderr)
    print(f"Model: {args.model}", file=sys.stderr)
    print(file=sys.stderr)

    results = run_benchmarks(
        caller, args.model, prompts, caveman_systems, args.trials,
        resume_from=resume_raw, on_prompt_done=partial_saver,
    )
    rows, summary = compute_stats(results)
    table_md = format_table(rows, summary)

    json_path = save_results(results, rows, summary, args.model, args.trials, skill_hash, output_path)
    print(f"\nResults saved to {json_path}", file=sys.stderr)

    if args.update_readme:
        update_readme(table_md)

    print(table_md)


if __name__ == "__main__":
    main()
