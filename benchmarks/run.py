#!/usr/bin/env python3
"""Benchmark normal vs caveman vs cvmn output token counts via OpenCode Zen."""

import argparse
import hashlib
import json
import os
import statistics
import sys
import time
import requests
from datetime import datetime, timezone
from pathlib import Path

ENV_FILE = Path(__file__).parent.parent / ".env.local"
if ENV_FILE.exists():
    for line in ENV_FILE.read_text().splitlines():
        line = line.strip()
        if line and not line.startswith("#") and "=" in line:
            key, _, value = line.partition("=")
            os.environ.setdefault(key.strip(), value.strip())

SCRIPT_VERSION = "2.0.0"
SCRIPT_DIR = Path(__file__).parent
REPO_DIR = SCRIPT_DIR.parent
PROMPTS_PATH = SCRIPT_DIR / "prompts.json"
CAVEMAN_SKILL_PATH = REPO_DIR / "skills" / "caveman" / "SKILL.md"
CVMN_SKILL_PATH = SCRIPT_DIR / ".opencode" / "skills" / "cvmn" / "SKILL.md"
README_PATH = REPO_DIR / "README.md"
RESULTS_DIR = SCRIPT_DIR / "results"

NORMAL_SYSTEM = "You are a helpful assistant."
BENCHMARK_START = "<!-- BENCHMARK-TABLE-START -->"
BENCHMARK_END = "<!-- BENCHMARK-TABLE-END -->"

ZEN_API_URL = "https://opencode.ai/zen/v1/chat/completions"
MODEL = "big-pickle"


def load_prompts():
    with open(PROMPTS_PATH) as f:
        data = json.load(f)
    return data["prompts"]


def load_caveman_system():
    return CAVEMAN_SKILL_PATH.read_text(encoding="utf-8")


def load_cvmn_system():
    return CVMN_SKILL_PATH.read_text(encoding="utf-8")


def sha256_file(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def call_api(api_key, system, prompt, max_retries=3):
    headers = {
        "Authorization": f"Bearer {api_key}",
        "Content-Type": "application/json",
    }
    payload = {
        "model": MODEL,
        "messages": [
            {"role": "system", "content": system},
            {"role": "user", "content": prompt},
        ],
        "max_tokens": 4096,
        "temperature": 0,
    }
    delays = [5, 10, 20]
    
    for attempt in range(max_retries + 1):
        try:
            response = requests.post(ZEN_API_URL, headers=headers, json=payload, timeout=120)
            response.raise_for_status()
            data = response.json()
            
            usage = data.get("usage", {})
            return {
                "input_tokens": usage.get("prompt_tokens", 0),
                "output_tokens": usage.get("completion_tokens", 0),
                "text": data["choices"][0]["message"]["content"],
                "stop_reason": data["choices"][0].get("finish_reason"),
            }
        except requests.exceptions.HTTPError as e:
            if response.status_code == 429:
                if attempt < max_retries:
                    delay = delays[min(attempt, len(delays) - 1)]
                    print(f"  Rate limited, retrying in {delay}s...", file=sys.stderr)
                    time.sleep(delay)
                else:
                    raise
            else:
                raise
        except requests.exceptions.RequestException:
            if attempt < max_retries:
                delay = delays[min(attempt, len(delays) - 1)]
                print(f"  Request failed, retrying in {delay}s...", file=sys.stderr)
                time.sleep(delay)
            else:
                raise


def run_benchmarks(api_key, prompts, systems, trials):
    results = []
    total = len(prompts)
    modes = list(systems.keys())

    for i, prompt_entry in enumerate(prompts, 1):
        pid = prompt_entry["id"]
        prompt_text = prompt_entry["prompt"]
        entry = {
            "id": pid,
            "category": prompt_entry["category"],
            "prompt": prompt_text,
            **{mode: [] for mode in modes},
        }

        for mode in modes:
            for t in range(1, trials + 1):
                print(
                    f"  [{i}/{total}] {pid} | {mode} | trial {t}/{trials}",
                    file=sys.stderr,
                )
                result = call_api(api_key, systems[mode], prompt_text)
                entry[mode].append(result)
                time.sleep(0.5)

        results.append(entry)

    return results


def compute_stats(results, modes):
    rows = []
    all_savings_caveman = []
    all_savings_cvmn = []

    for entry in results:
        normal_medians = statistics.median([t["output_tokens"] for t in entry["normal"]])
        caveman_medians = statistics.median([t["output_tokens"] for t in entry["caveman"]])
        cvmn_medians = statistics.median([t["output_tokens"] for t in entry["cvmn"]])
        
        savings_caveman = 1 - (caveman_medians / normal_medians) if normal_medians > 0 else 0
        savings_cvmn = 1 - (cvmn_medians / normal_medians) if normal_medians > 0 else 0
        all_savings_caveman.append(savings_caveman)
        all_savings_cvmn.append(savings_cvmn)

        rows.append(
            {
                "id": entry["id"],
                "category": entry["category"],
                "prompt": entry["prompt"],
                "normal_median": int(normal_medians),
                "caveman_median": int(caveman_medians),
                "cvmn_median": int(cvmn_medians),
                "caveman_savings_pct": round(savings_caveman * 100),
                "cvmn_savings_pct": round(savings_cvmn * 100),
            }
        )

    def summarize(savings_list, mode):
        return {
            f"avg_savings_{mode}": round(statistics.mean(savings_list) * 100),
            f"min_savings_{mode}": round(min(savings_list) * 100),
            f"max_savings_{mode}": round(max(savings_list) * 100),
            f"avg_{mode}": round(statistics.mean([r[f"{mode}_median"] for r in rows])),
        }

    return rows, {
        **summarize(all_savings_caveman, "caveman"),
        **summarize(all_savings_cvmn, "cvmn"),
        "avg_normal": round(statistics.mean([r["normal_median"] for r in rows])),
    }


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
    lines = [
        "| Task | Normal | Caveman | CVMN | Caveman Saved | CVMN Saved |",
        "|------|-------:|--------:|-----:|-------------:|----------:|",
    ]
    for r in rows:
        label = format_prompt_label(r["id"])
        lines.append(
            f"| {label} | {r['normal_median']} | {r['caveman_median']} | {r['cvmn_median']} | {r['caveman_savings_pct']}% | {r['cvmn_savings_pct']}% |"
        )
    lines.append(
        f"| **Average** | **{summary['avg_normal']}** | **{summary['avg_caveman']}** | **{summary['avg_cvmn']}** | **{summary['avg_savings_caveman']}%** | **{summary['avg_savings_cvmn']}%** |"
    )
    lines.append("")
    lines.append(
        f"*Caveman savings: {summary['min_savings_caveman']}%–{summary['max_savings_caveman']}% | CVMN savings: {summary['min_savings_cvmn']}%–{summary['max_savings_cvmn']}%*"
    )
    return "\n".join(lines)


def save_results(results, rows, summary, trials, skill_hashes):
    ts = datetime.now(timezone.utc).strftime("%Y%m%d_%H%M%S")
    output = {
        "metadata": {
            "script_version": SCRIPT_VERSION,
            "model": MODEL,
            "provider": "opencode-zen",
            "date": datetime.now(timezone.utc).isoformat(),
            "trials": trials,
            "skill_hashes": skill_hashes,
        },
        "summary": summary,
        "rows": rows,
        "raw": results,
    }
    path = RESULTS_DIR / f"benchmark_{ts}.json"
    RESULTS_DIR.mkdir(parents=True, exist_ok=True)
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


def dry_run(prompts, trials, modes):
    print(f"Model:  {MODEL} (OpenCode Zen)")
    print(f"Provider: opencode-zen")
    print(f"Modes: {', '.join(modes)}")
    print(f"Trials: {trials}")
    print(f"Prompts: {len(prompts)}")
    print(f"Total API calls: {len(prompts) * len(modes) * trials}")
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
    parser = argparse.ArgumentParser(description="Benchmark normal vs caveman vs cvmn via OpenCode Zen")
    parser.add_argument("--trials", type=int, default=3, help="Trials per prompt per mode (default: 3)")
    parser.add_argument("--dry-run", action="store_true", help="Print config, no API calls")
    parser.add_argument("--update-readme", action="store_true", help="Update README.md benchmark table")
    parser.add_argument("--api-key", help="OpenCode Zen API key (or set OPENCODE_API_KEY env var)")
    args = parser.parse_args()

    prompts = load_prompts()
    modes = ["normal", "caveman", "cvmn"]
    
    if args.dry_run:
        dry_run(prompts, args.trials, modes)
        return

    api_key = args.api_key or os.environ.get("OPENCODE_API_KEY")
    if not api_key:
        print("ERROR: OpenCode Zen API key required. Set OPENCODE_API_KEY env var or use --api-key", file=sys.stderr)
        sys.exit(1)

    systems = {
        "normal": NORMAL_SYSTEM,
        "caveman": load_caveman_system(),
        "cvmn": load_cvmn_system(),
    }
    skill_hashes = {
        "caveman": sha256_file(CAVEMAN_SKILL_PATH),
        "cvmn": sha256_file(CVMN_SKILL_PATH),
    }

    print(f"Running benchmarks: {len(prompts)} prompts x {len(modes)} modes x {args.trials} trials", file=sys.stderr)
    print(f"Model: {MODEL} via OpenCode Zen", file=sys.stderr)
    print(file=sys.stderr)

    results = run_benchmarks(api_key, prompts, systems, args.trials)
    rows, summary = compute_stats(results, modes)
    table_md = format_table(rows, summary)

    json_path = save_results(results, rows, summary, args.trials, skill_hashes)
    print(f"\nResults saved to {json_path}", file=sys.stderr)

    if args.update_readme:
        update_readme(table_md)

    print(table_md)


if __name__ == "__main__":
    main()
