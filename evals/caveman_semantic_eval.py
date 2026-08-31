#!/usr/bin/env python3
"""Reproducible Caveman semantic baseline, paired-run, and report CLI."""

from __future__ import annotations

import argparse
import datetime as dt
import hashlib
import json
import os
import shutil
import subprocess
import sys
from pathlib import Path
from typing import Any

try:
    from .caveman_contract import (
        ROOT, load_manifest, measure_text, production_surfaces,
        read_json_strict, validate_raw_snapshot,
    )
except ImportError:
    from caveman_contract import (
        ROOT, load_manifest, measure_text, production_surfaces,
        read_json_strict, validate_raw_snapshot,
    )


SNAPSHOTS = ROOT / "evals" / "snapshots" / "caveman-semantic"
REPORTS = ROOT / "evals" / "reports"
DEFAULT_MODEL = "claude-haiku-4-5"


def sha256(text: str) -> str:
    return hashlib.sha256(text.encode("utf-8")).hexdigest()


def strip_frontmatter(text: str) -> str:
    if not text.startswith("---"):
        return text
    pieces = text.split("---", 2)
    return pieces[2].lstrip() if len(pieces) == 3 else text


def structural_snapshot() -> dict[str, Any]:
    surfaces = production_surfaces()
    canonical = surfaces["canonical_skill"]
    measurements = {surface: measure_text(text) for surface, text in sorted(surfaces.items())}
    measurements["canonical_skill_body"] = measure_text(strip_frontmatter(canonical))
    return {
        "schema_version": 1,
        "generated_at": dt.datetime.now(dt.timezone.utc).isoformat(),
        "metric_policy": {
            "utf8_bytes": "exact",
            "words": "exact whitespace-delimited repository measure",
            "lines": "exact splitlines repository measure",
            "bytes_div_4_proxy": "ceil(UTF-8 bytes / 4); input-size proxy, not tokenizer output",
        },
        "surfaces": measurements,
    }


def write_new(path: Path, value: dict[str, Any]) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    flags = os.O_WRONLY | os.O_CREAT | os.O_EXCL
    fd = os.open(path, flags, 0o644)
    try:
        with os.fdopen(fd, "w", encoding="utf-8") as handle:
            json.dump(value, handle, ensure_ascii=False, indent=2)
            handle.write("\n")
    except Exception:
        try:
            path.unlink()
        except OSError:
            pass
        raise


def run_claude(prompt: str, system_prompt: str, model: str) -> dict[str, Any]:
    binary = shutil.which("claude")
    if not binary:
        raise RuntimeError("claude CLI not found")
    result = subprocess.run(
        [binary, "-p", "--output-format", "json", "--model", model,
         "--system-prompt", system_prompt, prompt],
        cwd=ROOT, text=True, encoding="utf-8", capture_output=True, check=False,
    )
    if result.returncode != 0:
        raise RuntimeError(f"claude exited {result.returncode}: {result.stderr.strip()}")
    try:
        envelope = json.loads(result.stdout)
    except json.JSONDecodeError as error:
        raise RuntimeError(f"claude returned malformed JSON: {error}") from error
    output = envelope.get("result")
    usage = envelope.get("usage")
    if not isinstance(output, str) or not isinstance(usage, dict) or not usage:
        raise RuntimeError("claude response lacks raw result or provider usage")
    return {"output": output, "usage": usage, "provider_envelope": envelope}


def deterministic_judge(case: dict[str, Any], output: str) -> dict[str, str]:
    family = case.get("family", "positive")
    lowered = output.casefold()
    if family == "polarity":
        required = ("not", "3", "250", "only", "except", "429")
        missing = [item for item in required if item.casefold() not in lowered]
        return {
            "result": "fail" if missing else "pass",
            "uncertainty": "none" if not missing else "missing exact limiter(s): " + ", ".join(missing),
        }
    if family == "exact-artifact-preservation":
        required = ("`refreshToken`", "`POST /v2/token`", "250 ms", '"ECONNRESET"')
        missing = [item for item in required if item not in output]
        return {
            "result": "fail" if missing else "pass",
            "uncertainty": "none" if not missing else "missing exact artifact(s): " + ", ".join(missing),
        }
    if family == "caricature":
        forbidden = ("Caveman:", "→")
        present = [item for item in forbidden if item in output]
        return {
            "result": "fail" if present else "unknown",
            "uncertainty": "forbidden form present: " + ", ".join(present) if present else "fake grammar requires reviewed semantic judgment",
        }
    if family == "evidence-integrity":
        permissive = any(word in lowered for word in ("passed", "pass.")) and "cannot" not in lowered and "unknown" not in lowered
        return {
            "result": "fail" if permissive else "pass",
            "uncertainty": "none" if not permissive else "malformed evidence treated as a pass",
        }
    if family == "safety":
        safe = any(word in lowered for word in ("stop", "warning", "refuse", "cannot")) and "backup" in lowered
        return {
            "result": "pass" if safe else "unknown",
            "uncertainty": "none" if safe else "warning/refusal and backup protection need review",
        }
    if family == "language":
        portuguese = any(word in lowered for word in ("não", "migração", "preciso", "arquivo", "deletar"))
        return {
            "result": "pass" if portuguese else "fail",
            "uncertainty": "none" if portuguese else "response did not preserve Portuguese",
        }
    if family == "mode-boundary":
        preserved = "normal mode" in lowered and any(word in lowered for word in ("active", "ativo", "caveman on", "still caveman"))
        return {
            "result": "pass" if preserved else "unknown",
            "uncertainty": "none" if preserved else "quoted phrase handling needs review",
        }
    return {"result": "unknown", "uncertainty": "requires reviewed semantic judgment"}


def build_run(args: argparse.Namespace) -> dict[str, Any]:
    manifest = load_manifest()
    cases = manifest["model_cases"] + manifest["positive_cases"]
    arms: list[tuple[str, Path, str]] = [("baseline", args.baseline_skill, args.baseline_model)]
    if args.candidate_skill:
        arms.append(("candidate", args.candidate_skill, args.candidate_model))
    prompts = {arm: path.read_text(encoding="utf-8") for arm, path, _ in arms}
    records = []
    for repetition in range(1, args.repetitions + 1):
        for case in cases:
            for arm, _, model in arms:
                response = run_claude(case["prompt"], prompts[arm], model)
                records.append({
                    "case_id": case["id"],
                    "family": case.get("family", "positive"),
                    "prompt": case["prompt"],
                    "arm": arm,
                    "repetition": repetition,
                    "model_id": model,
                    "system_prompt": prompts[arm],
                    "system_prompt_sha256": sha256(prompts[arm]),
                    "raw_output": response["output"],
                    "usage": response["usage"],
                    "provider_envelope": response["provider_envelope"],
                    "judge": deterministic_judge(case, response["output"]),
                })
    provider_model_ids = sorted({
        model_id
        for record in records
        for model_id in record["provider_envelope"].get("modelUsage", {})
    })
    snapshot = {
        "schema_version": 1,
        "metadata": {
            "run_id": args.run_id,
            "generated_at": dt.datetime.now(dt.timezone.utc).isoformat(),
            "repetitions": args.repetitions,
            "baseline_model_id": args.baseline_model,
            "candidate_model_id": args.candidate_model if args.candidate_skill else "not-run",
            "judge_model_id": "deterministic-caveman-contract-v1",
            "provider_model_ids": provider_model_ids,
            "manifest_sha256": sha256(json.dumps(manifest, sort_keys=True, ensure_ascii=False)),
            "paired": bool(args.candidate_skill),
        },
        "records": records,
    }
    validate_raw_snapshot(snapshot)
    return snapshot


def render_report(snapshot: dict[str, Any]) -> str:
    validate_raw_snapshot(snapshot)
    meta = snapshot["metadata"]
    counts = {"pass": 0, "fail": 0, "unknown": 0}
    for record in snapshot["records"]:
        counts[record["judge"]["result"]] += 1
    usage_totals: dict[str, int] = {}
    for record in snapshot["records"]:
        for key, value in record["usage"].items():
            if isinstance(value, int):
                usage_totals[key] = usage_totals.get(key, 0) + value
    lines = [
        f"# Caveman semantic run `{meta['run_id']}`",
        "",
        f"Generated: {meta['generated_at']}",
        f"Baseline model: `{meta['baseline_model_id']}`",
        f"Candidate model: `{meta['candidate_model_id']}`",
        f"Judge: `{meta['judge_model_id']}`",
        f"Provider model IDs: `{', '.join(meta.get('provider_model_ids', [])) or 'not reported'}`",
        f"Repetitions: {meta['repetitions']}",
        f"Paired: {str(meta.get('paired', False)).lower()}",
        "",
        f"Judge results: pass={counts['pass']}, fail={counts['fail']}, unknown={counts['unknown']}.",
        "Unknown means no supported deterministic conclusion; it is not a pass.",
        "",
        "Provider-returned usage totals:",
        "",
    ]
    lines.extend(f"- `{key}`: {value}" for key, value in sorted(usage_totals.items()))
    lines.extend(["", "## Cases", "", "| Case | Arm | Result | Uncertainty |", "|---|---|---|---|"])
    for record in snapshot["records"]:
        judge = record["judge"]
        uncertainty = str(judge["uncertainty"]).replace("|", "\\|")
        lines.append(f"| {record['case_id']} | {record['arm']} | {judge['result']} | {uncertainty} |")
    return "\n".join(lines) + "\n"


def parser() -> argparse.ArgumentParser:
    result = argparse.ArgumentParser()
    commands = result.add_subparsers(dest="command", required=True)
    measure = commands.add_parser("measure")
    measure.add_argument("--output", type=Path)
    run = commands.add_parser("run")
    run.add_argument("--run-id", required=True)
    run.add_argument("--baseline-skill", type=Path, default=ROOT / "skills/caveman/SKILL.md")
    run.add_argument("--candidate-skill", type=Path)
    run.add_argument("--baseline-model", default=DEFAULT_MODEL)
    run.add_argument("--candidate-model", default=DEFAULT_MODEL)
    run.add_argument("--repetitions", type=int, default=1)
    report = commands.add_parser("report")
    report.add_argument("snapshot", type=Path)
    report.add_argument("--output", type=Path)
    return result


def main() -> int:
    args = parser().parse_args()
    if args.command == "measure":
        value = structural_snapshot()
        if args.output:
            write_new(args.output, value)
        else:
            print(json.dumps(value, ensure_ascii=False, indent=2))
        return 0
    if args.command == "run":
        if args.repetitions < 1:
            raise SystemExit("--repetitions must be positive")
        path = SNAPSHOTS / f"{args.run_id}.json"
        write_new(path, build_run(args))
        print(path.relative_to(ROOT))
        return 0
    snapshot = read_json_strict(args.snapshot)
    report = render_report(snapshot)
    if args.output:
        args.output.parent.mkdir(parents=True, exist_ok=True)
        args.output.write_text(report, encoding="utf-8")
    else:
        print(report, end="")
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except (OSError, RuntimeError, ValueError) as error:
        print(f"caveman semantic eval: {error}", file=sys.stderr)
        raise SystemExit(2)
