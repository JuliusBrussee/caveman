#!/usr/bin/env python3
"""Multi-arm caveman benchmark using DeepSeek API.

Compares 7 modes across 10 Korean prompts:
  baseline - no system prompt
  terse    - "Answer concisely."
  en-full  - Caveman English full
  en-ultra - Caveman English ultra
  wenyan-full - Caveman Classical Chinese full
  hangeul-full - Caveman Korean full
  hangeul-ultra - Caveman Korean ultra
"""

import argparse, json, os, statistics, sys, time, urllib.request, urllib.error
from datetime import datetime, timezone
from pathlib import Path

SCRIPT_DIR = Path(__file__).parent
REPO_DIR = SCRIPT_DIR.parent
SKILL_PATH = REPO_DIR / "skills" / "caveman" / "SKILL.md"
RESULTS_DIR = SCRIPT_DIR / "results"

# ── config ──────────────────────────────────────────────────
DEEPSEEK_URL = "https://api.deepseek.com/v1/chat/completions"
DEFAULT_MODEL = "deepseek-v4-pro"
MAX_TOKENS = 4096
TEMPERATURE = 0

# ── system prompts per arm ──────────────────────────────────
SYSTEM_PROMPTS = {
    "baseline": "",
    "terse": "Answer concisely. Be brief and direct.",
    "en-full": None,   # filled from SKILL.md
    "en-ultra": None,  # filled from SKILL.md + ultra directive
    "wenyan-full": None,  # filled from SKILL.md
    "hangeul-full": None,  # filled from SKILL.md + hangeul rules
    "hangeul-ultra": None,  # filled from SKILL.md + hangeul rules
}

# ── Korean prompts ──────────────────────────────────────────
PROMPTS_KO = [
    "React 컴포넌트가 부모 업데이트마다 왜 리렌더링되나요? 객체를 prop으로 넘기고 있어요.",
    "데이터베이스 커넥션 풀링에 대해 설명해주세요.",
    "TCP와 UDP의 차이점은 무엇인가요?",
    "오래 실행되는 Node.js 프로세스에서 메모리 누수를 어떻게 찾고 고치나요?",
    "SQL EXPLAIN 명령어가 알려주는 것은 무엇인가요?",
    "해시 테이블은 충돌을 어떻게 처리하나요?",
    "브라우저 콘솔에 CORS 오류가 발생하는 이유는 뭔가요?",
    "검색 입력에 debouncer를 사용하는 목적이 뭔가요?",
    "git rebase와 git merge의 차이점은 무엇이고 각각 언제 사용해야 하나요?",
    "메시징 시스템에서 queue와 topic은 각각 어떤 경우에 사용해야 하나요?",
]

PROMPT_IDS = [
    "react-rerender", "db-pooling", "tcp-udp", "memory-leak",
    "sql-explain", "hash-collision", "cors-error", "debouncer",
    "git-rebase-merge", "queue-vs-topic"
]

PROMPT_CATEGORIES = [
    "debugging", "explanation", "explanation", "debugging",
    "explanation", "explanation", "debugging", "explanation",
    "explanation", "architecture"
]


def load_skill() -> str:
    """Load SKILL.md body (without YAML frontmatter)."""
    content = SKILL_PATH.read_text()
    if content.startswith("---"):
        parts = content.split("---", 2)
        if len(parts) >= 3:
            return parts[2].strip()
    return content.strip()


def load_hangeul_rules() -> str:
    """Load Korean compression rules."""
    rules_path = REPO_DIR / "rules" / "hangeul-compression.md"
    if rules_path.exists():
        return rules_path.read_text().strip()
    return ""


def build_system_prompt(mode: str, skill_body: str, hangeul_rules: str) -> str:
    """Build system prompt for each mode."""
    if mode == "baseline":
        return "You are a helpful assistant."
    elif mode == "terse":
        return "You are a helpful assistant. Answer concisely. Be brief and direct."
    elif mode == "en-full":
        return skill_body
    elif mode == "en-ultra":
        return skill_body + "\n\nUse ultra intensity: abbreviate terms, use arrows (→), one word when enough."
    elif mode == "wenyan-full":
        return skill_body + "\n\nRespond in Classical Chinese (文言文). Use classical grammar patterns."
    elif mode == "hangeul-full":
        base = skill_body + "\n\nRespond in Korean. Use hangeul-full intensity."
        if hangeul_rules:
            base += "\n\n" + hangeul_rules
        return base
    elif mode == "hangeul-ultra":
        base = skill_body + "\n\nRespond in Korean. Use hangeul-ultra intensity (maximum compression)."
        if hangeul_rules:
            base += "\n\n" + hangeul_rules
        return base
    return "You are a helpful assistant."


def call_deepseek(api_key: str, model: str, system_prompt: str, user_prompt: str, max_retries=3) -> dict:
    """Call DeepSeek API, return response dict with token counts."""
    payload = json.dumps({
        "model": model,
        "messages": [
            {"role": "system", "content": system_prompt},
            {"role": "user", "content": user_prompt},
        ],
        "max_tokens": MAX_TOKENS,
        "temperature": TEMPERATURE,
    }).encode("utf-8")

    req = urllib.request.Request(
        DEEPSEEK_URL,
        data=payload,
        headers={
            "Authorization": f"Bearer {api_key}",
            "Content-Type": "application/json",
        },
        method="POST",
    )

    delays = [5, 10, 20]
    for attempt in range(max_retries + 1):
        try:
            with urllib.request.urlopen(req, timeout=120) as resp:
                data = json.loads(resp.read().decode("utf-8"))
            usage = data.get("usage", {})
            return {
                "input_tokens": usage.get("prompt_tokens", 0),
                "output_tokens": usage.get("completion_tokens", 0),
                "total_tokens": usage.get("total_tokens", 0),
                "text": data["choices"][0]["message"]["content"],
                "finish_reason": data["choices"][0].get("finish_reason", "unknown"),
            }
        except urllib.error.HTTPError as e:
            body = e.read().decode() if e.fp else ""
            if e.code == 429:
                if attempt < max_retries:
                    delay = delays[min(attempt, len(delays) - 1)]
                    print(f"  Rate limited, retrying in {delay}s...", file=sys.stderr)
                    time.sleep(delay)
                    continue
            raise Exception(f"HTTP {e.code}: {body[:200]}")
        except Exception as e:
            if attempt < max_retries:
                delay = delays[min(attempt, len(delays) - 1)]
                print(f"  Error ({e}), retrying in {delay}s...", file=sys.stderr)
                time.sleep(delay)
            else:
                raise


def run_benchmark(api_key: str, model: str, trials: int = 3):
    """Run all arms on all prompts."""
    skill_body = load_skill()
    hangeul_rules = load_hangeul_rules()
    arms = list(SYSTEM_PROMPTS.keys())
    results = {}

    total_calls = len(PROMPTS_KO) * len(arms) * trials
    print(f"Benchmark: {len(PROMPTS_KO)} prompts × {len(arms)} arms × {trials} trials = {total_calls} calls", file=sys.stderr)
    print(f"Model: {model}", file=sys.stderr)
    print(file=sys.stderr)

    for arm in arms:
        print(f"\n━━━ {arm} ━━━", file=sys.stderr)
        sys_prompt = build_system_prompt(arm, skill_body, hangeul_rules)
        arm_results = []

        for i, (pid, prompt, cat) in enumerate(zip(PROMPT_IDS, PROMPTS_KO, PROMPT_CATEGORIES)):
            trial_tokens = []
            for t in range(1, trials + 1):
                print(f"  [{arm}] {pid} trial {t}/{trials}", file=sys.stderr)
                result = call_deepseek(api_key, model, sys_prompt, prompt)
                trial_tokens.append(result["output_tokens"])
                time.sleep(0.3)  # rate limit safety

            median = int(statistics.median(trial_tokens))
            arm_results.append({
                "id": pid,
                "category": cat,
                "prompt": prompt,
                "output_tokens_median": median,
                "output_tokens_trials": trial_tokens,
            })
            print(f"    → median {median} tokens", file=sys.stderr)

        results[arm] = arm_results

    return results


def compute_comparison(results: dict) -> dict:
    """Compute summary statistics and comparison table."""
    arms = list(results.keys())
    baseline = results.get("baseline", [])

    summary = {}
    for arm in arms:
        arm_data = results[arm]
        medians = [r["output_tokens_median"] for r in arm_data]
        avg = round(statistics.mean(medians), 1)
        summary[arm] = {
            "avg_output_tokens": avg,
            "min": min(medians),
            "max": max(medians),
        }
        if arm != "baseline" and baseline:
            baseline_avg = statistics.mean([r["output_tokens_median"] for r in baseline])
            summary[arm]["savings_vs_baseline"] = f"{round((1 - avg / baseline_avg) * 100)}%"

    # Per-prompt comparison rows
    rows = []
    for i, pid in enumerate(PROMPT_IDS):
        row = {"id": pid, "category": PROMPT_CATEGORIES[i], "prompt": PROMPTS_KO[i]}
        for arm in arms:
            row[f"{arm}_tokens"] = results[arm][i]["output_tokens_median"]
        rows.append(row)

    return {"summary": summary, "rows": rows}


def format_table(comparison: dict) -> str:
    """Format markdown comparison table."""
    arms = ["baseline", "terse", "en-full", "en-ultra", "wenyan-full", "hangeul-full", "hangeul-ultra"]
    labels = ["Prompt", "Baseline", "Terse", "EN full", "EN ultra", "WY-full", "HG-full", "HG-ultra"]
    
    lines = ["| " + " | ".join(labels) + " |"]
    lines.append("|" + "|".join(["--------"] * len(labels)) + "|")

    short_prompts = [
        "React 리렌더링", "DB 풀링", "TCP vs UDP", "메모리 누수",
        "SQL EXPLAIN", "해시 충돌", "CORS 오류", "Debouncer",
        "Git rebase/merge", "Queue vs Topic"
    ]

    for i, sp in enumerate(short_prompts):
        cols = [sp]
        for arm in arms:
            cols.append(str(comparison["rows"][i][f"{arm}_tokens"]))
        lines.append("| " + " | ".join(cols) + " |")

    # Average row
    cols = ["**Average**"]
    summary = comparison["summary"]
    for arm in arms:
        cols.append(f"**{summary[arm]['avg_output_tokens']}**")
    lines.append("| " + " | ".join(cols) + " |")

    # Savings row
    cols = ["**vs Baseline**"]
    cols.append("—")
    for arm in arms[1:]:
        cols.append(summary[arm].get("savings_vs_baseline", "—"))
    lines.append("| " + " | ".join(cols) + " |")

    lines.append("")
    lines.append(f"*Model: deepseek-v4-pro. Tokenizer: DeepSeek native. 3 trials per arm, median reported.*")
    lines.append(f"*Korean prompts. English/wenyan arms answered in English/文言文 respectively.*")

    return "\n".join(lines)


def save_results(results, comparison, model, trials):
    ts = datetime.now(timezone.utc).strftime("%Y%m%d_%H%M%S")
    output = {
        "metadata": {
            "model": model,
            "date": datetime.now(timezone.utc).isoformat(),
            "trials": trials,
            "arms": list(results.keys()),
        },
        "comparison": comparison,
        "raw_per_arm": results,
    }
    RESULTS_DIR.mkdir(parents=True, exist_ok=True)
    path = RESULTS_DIR / f"hangeul_benchmark_{ts}.json"
    with open(path, "w") as f:
        json.dump(output, f, indent=2, ensure_ascii=False)
    return path


def main():
    parser = argparse.ArgumentParser(description="Multi-arm caveman benchmark on DeepSeek")
    parser.add_argument("--api-key", help="DeepSeek API key (or set DEEPSEEK_API_KEY env var)")
    parser.add_argument("--model", default=DEFAULT_MODEL, help="Model name")
    parser.add_argument("--trials", type=int, default=3, help="Trials per prompt per arm")
    parser.add_argument("--dry-run", action="store_true", help="Print config, no API calls")
    parser.add_argument("--output", help="Save results JSON to this path")
    args = parser.parse_args()

    if args.dry_run:
        total = len(PROMPTS_KO) * len(SYSTEM_PROMPTS) * args.trials
        print(f"Model: {args.model}")
        print(f"Trials per arm: {args.trials}")
        print(f"Arms: {list(SYSTEM_PROMPTS.keys())}")
        print(f"Prompts: {len(PROMPTS_KO)}")
        print(f"Total API calls: {total}")
        print(f"Estimated cost ($0.435 in + $0.87 out /1M): ~${total * 0.005:.3f}")
        return

    api_key = args.api_key or os.environ.get("DEEPSEEK_API_KEY")

    # If no key yet, try loading from .env files
    if not api_key:
        for env_path in [
            os.path.expanduser("~/.hermes/.env"),
            os.path.expanduser("~/.hermes/profiles/default/.env"),
        ]:
            try:
                with open(env_path) as f:
                    for line in f:
                        if line.startswith("DEEPSEEK_API_KEY="):
                            val = line.strip().split("=", 1)[1].strip()
                            if val and val != "***":
                                api_key = val
                                break
            except Exception:
                pass
            if api_key:
                break

    if not api_key:
        print("ERROR: DEEPSEEK_API_KEY not set. Use --api-key or env var.", file=sys.stderr)
        sys.exit(1)

    results = run_benchmark(api_key, args.model, args.trials)
    comparison = compute_comparison(results)
    table = format_table(comparison)

    json_path = save_results(results, comparison, args.model, args.trials)
    print(f"\nResults saved to {json_path}", file=sys.stderr)
    print(f"\n{table}")


if __name__ == "__main__":
    main()
