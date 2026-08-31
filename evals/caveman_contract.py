"""Production-surface loader and deterministic Caveman semantic checks."""

from __future__ import annotations

import json
import os
import re
import shutil
import subprocess
import tempfile
from pathlib import Path
from typing import Any


ROOT = Path(__file__).resolve().parent.parent
MANIFEST_PATH = ROOT / "tests" / "fixtures" / "caveman-semantic-invariants.json"
REQUIRED_GROUP_IDS = {f"CAV-SEM-{number:02d}" for number in range(1, 8)}
REQUIRED_NEGATIVE_FAMILIES = {
    "polarity",
    "safety",
    "language",
    "artifact",
    "caricature",
    "surface-coverage",
    "exact-artifact-preservation",
    "mode-boundary",
    "evidence-integrity",
}
REQUIRED_SURFACES = {
    "canonical_skill",
    "claude_session_start",
    "compact_rule",
    "openclaw_bootstrap",
    "codex_command",
    "opencode_command",
    "hook_fallback",
    "mv3_primer",
    "mv3_reminder",
}
REQUIRED_GROUP_SURFACES = {
    "CAV-SEM-01": REQUIRED_SURFACES - {"mv3_reminder"},
    "CAV-SEM-02": REQUIRED_SURFACES - {"mv3_reminder"},
    "CAV-SEM-03": {"canonical_skill", "claude_session_start", "openclaw_bootstrap", "hook_fallback", "mv3_primer"},
    "CAV-SEM-04": {"canonical_skill", "claude_session_start", "openclaw_bootstrap", "hook_fallback"},
    "CAV-SEM-05": {"canonical_skill", "claude_session_start", "compact_rule", "openclaw_bootstrap", "opencode_command", "hook_fallback", "mv3_primer"},
    "CAV-SEM-06": {"canonical_skill", "claude_session_start", "compact_rule", "openclaw_bootstrap", "opencode_command", "hook_fallback"},
    "CAV-SEM-07": REQUIRED_SURFACES,
}
REQUIRED_SEMANTIC_CLASSES = {
    "safety": {"destructive", "security", "legal-medical", "ambiguity", "data-loss", "ordered-recovery"},
    "artifact": {"code", "comments", "commits", "documentation", "issue-pr-text", "memory", "third-party-message"},
    "caricature": {"fake-grammar", "mode-prefix", "invented-abbreviation", "causal-arrow"},
    "language": {"dominant-language", "grammatical-role-marker", "wenyan-only-classical-chinese"},
}


class ContractError(ValueError):
    """A semantic contract or evidence artifact is absent or invalid."""


def read_json_strict(path: Path) -> dict[str, Any]:
    """Read a complete JSON object. Absence and malformed reads are errors."""
    try:
        raw = path.read_text(encoding="utf-8")
    except OSError as error:
        raise ContractError(f"cannot read {path}: {error}") from error
    try:
        value = json.loads(raw)
    except json.JSONDecodeError as error:
        raise ContractError(f"malformed JSON in {path}: {error}") from error
    if not isinstance(value, dict):
        raise ContractError(f"expected JSON object in {path}")
    return value


def load_manifest(path: Path = MANIFEST_PATH) -> dict[str, Any]:
    manifest = read_json_strict(path)
    validate_manifest_structure(manifest)
    return manifest


def validate_manifest_structure(manifest: dict[str, Any]) -> None:
    groups = manifest.get("invariant_groups")
    if not isinstance(groups, list) or len(groups) != 7:
        raise ContractError("manifest must define exactly seven invariant groups")
    ids = {group.get("id") for group in groups if isinstance(group, dict)}
    if ids != REQUIRED_GROUP_IDS:
        raise ContractError(f"invariant IDs differ: {sorted(ids)}")
    names = [group.get("name") for group in groups]
    if len(set(names)) != 7:
        raise ContractError("invariant names must be unique")
    for group in groups:
        if not group.get("description") or not group.get("markers"):
            raise ContractError(f"{group.get('id')} lacks description or markers")
        surfaces = set(group.get("surfaces", []))
        expected_surfaces = REQUIRED_GROUP_SURFACES[group["id"]]
        if surfaces != expected_surfaces:
            raise ContractError(
                f"{group.get('id')} surface coverage differs: {sorted(surfaces)}"
            )
    covered_surfaces = {surface for group in groups for surface in group["surfaces"]}
    if covered_surfaces != REQUIRED_SURFACES:
        raise ContractError(f"manifest surface coverage differs: {sorted(covered_surfaces)}")

    cases = manifest.get("model_cases")
    if not isinstance(cases, list):
        raise ContractError("model_cases must be a list")
    families = {case.get("family") for case in cases if isinstance(case, dict)}
    if families != REQUIRED_NEGATIVE_FAMILIES:
        raise ContractError(f"negative families differ: {sorted(families)}")
    if len({case.get("id") for case in cases}) != len(cases):
        raise ContractError("model case IDs must be unique")

    calibration = manifest.get("calibration_material", {})
    if calibration.get("replaceable") is not True:
        raise ContractError("calibration material must be explicitly replaceable")
    classes = manifest.get("semantic_classes", {})
    for category, expected in REQUIRED_SEMANTIC_CLASSES.items():
        actual = set(classes.get(category, []))
        if actual != expected:
            raise ContractError(f"{category} semantic classes differ: {sorted(actual)}")


def _run_node(source: str, *, env: dict[str, str] | None = None) -> str:
    result = subprocess.run(
        ["node", "-e", source],
        cwd=ROOT,
        env=env,
        text=True,
        encoding="utf-8",
        capture_output=True,
        check=False,
    )
    if result.returncode != 0:
        raise ContractError(f"node production entry failed ({result.returncode}): {result.stderr.strip()}")
    return result.stdout


def _run_hook(hook: Path, *, isolated: bool = False) -> str:
    with tempfile.TemporaryDirectory(prefix="caveman-contract-hook-") as raw_tmp:
        tmp = Path(raw_tmp)
        target = hook
        if isolated:
            target = tmp / "caveman-activate.js"
            target.write_bytes(hook.read_bytes())
        config = tmp / "claude"
        config.mkdir()
        (config / "settings.json").write_text('{"statusLine":{"type":"command","command":"true"}}\n', encoding="utf-8")
        env = os.environ.copy()
        env.pop("CLAUDE_PLUGIN_ROOT", None)
        env.update({"CLAUDE_CONFIG_DIR": str(config), "CAVEMAN_DEFAULT_MODE": "full"})
        payload = json.dumps({
            "session_id": "semantic-contract",
            "cwd": str(ROOT),
            "hook_event_name": "SessionStart",
            "source": "startup",
        })
        result = subprocess.run(
            ["node", str(target)], cwd=ROOT, env=env, input=payload,
            text=True, encoding="utf-8", capture_output=True, check=False,
        )
        if result.returncode != 0 or not result.stdout:
            raise ContractError(
                f"SessionStart production entry failed ({result.returncode}): {result.stderr.strip()}"
            )
        return result.stdout


def _run_installed_claude_hook() -> str:
    if not shutil.which("bash"):
        raise ContractError("bash required to drive installed Claude hook path")
    with tempfile.TemporaryDirectory(prefix="caveman-contract-claude-install-") as raw_tmp:
        tmp = Path(raw_tmp)
        config = tmp / "claude"
        config.mkdir()
        (config / "settings.json").write_text("{}\n", encoding="utf-8")
        env = {
            **os.environ,
            "HOME": str(tmp),
            "USERPROFILE": str(tmp),
            "CLAUDE_CONFIG_DIR": str(config),
        }
        install = subprocess.run(
            ["bash", "src/hooks/install.sh"], cwd=ROOT, env=env,
            text=True, encoding="utf-8", capture_output=True, check=False,
        )
        if install.returncode != 0:
            raise ContractError(f"Claude hook installer failed ({install.returncode}): {install.stderr.strip()}")
        skill_dir = config / "skills/caveman"
        skill_dir.mkdir(parents=True)
        shutil.copyfile(ROOT / "skills/caveman/SKILL.md", skill_dir / "SKILL.md")
        return _run_installed_hook_file(config / "hooks/caveman-activate.js", config)


def _run_installed_hook_file(hook: Path, config: Path) -> str:
    env = os.environ.copy()
    env.pop("CLAUDE_PLUGIN_ROOT", None)
    env.update({"CLAUDE_CONFIG_DIR": str(config), "CAVEMAN_DEFAULT_MODE": "full"})
    payload = json.dumps({
        "session_id": "semantic-contract-installed",
        "cwd": str(ROOT),
        "hook_event_name": "SessionStart",
        "source": "startup",
    })
    result = subprocess.run(
        ["node", str(hook)], cwd=ROOT, env=env, input=payload,
        text=True, encoding="utf-8", capture_output=True, check=False,
    )
    if result.returncode != 0 or not result.stdout:
        raise ContractError(f"installed Claude hook failed ({result.returncode}): {result.stderr.strip()}")
    return result.stdout


def _installed_opencode_command() -> str:
    with tempfile.TemporaryDirectory(prefix="caveman-contract-opencode-install-") as raw_tmp:
        tmp = Path(raw_tmp)
        shim = tmp / "bin"
        shim.mkdir()
        executable = shim / "opencode"
        executable.write_text("#!/bin/sh\nexit 0\n", encoding="utf-8")
        executable.chmod(0o755)
        xdg = tmp / "xdg"
        env = {
            **os.environ,
            "XDG_CONFIG_HOME": str(xdg),
            "PATH": str(shim) + os.pathsep + os.environ.get("PATH", ""),
            "NO_COLOR": "1",
        }
        result = subprocess.run(
            ["node", "bin/install.js", "--only", "opencode", "--config-dir", str(tmp / "claude"),
             "--non-interactive", "--no-mcp-shrink"],
            cwd=ROOT, env=env, text=True, encoding="utf-8", capture_output=True, check=False,
        )
        if result.returncode != 0:
            raise ContractError(f"opencode installer failed ({result.returncode}): {result.stderr.strip()}")
        return (xdg / "opencode/commands/caveman.md").read_text(encoding="utf-8")


def production_surfaces() -> dict[str, str]:
    """Materialize every delivery surface through its production call site."""
    surfaces: dict[str, str] = {}
    surfaces["canonical_skill"] = (ROOT / "skills/caveman/SKILL.md").read_text(encoding="utf-8")
    surfaces["claude_session_start"] = _run_installed_claude_hook()

    with tempfile.TemporaryDirectory(prefix="caveman-contract-init-") as raw_tmp:
        result = subprocess.run(
            ["node", "src/tools/caveman-init.js", raw_tmp, "--only", "cline"],
            cwd=ROOT, text=True, encoding="utf-8", capture_output=True, check=False,
            env={**os.environ, "OPENCLAW_WORKSPACE": str(Path(raw_tmp) / "unused-openclaw")},
        )
        if result.returncode != 0:
            raise ContractError(f"caveman-init production entry failed ({result.returncode}): {result.stderr.strip()}")
        surfaces["compact_rule"] = (Path(raw_tmp) / ".clinerules/caveman.md").read_text(encoding="utf-8")

    with tempfile.TemporaryDirectory(prefix="caveman-contract-openclaw-") as raw_tmp:
        workspace = Path(raw_tmp) / "workspace"
        workspace.mkdir()
        js = f"""
const openclaw = require('./bin/lib/openclaw.js');
const result = openclaw.installOpenclaw({{
  workspace: {json.dumps(str(workspace))}, repoRoot: process.cwd(), force: true
}});
if (!result.ok) process.exit(3);
"""
        _run_node(js)
        surfaces["openclaw_bootstrap"] = (
            (workspace / "SOUL.md").read_text(encoding="utf-8")
            + "\n"
            + (workspace / "skills/caveman/SKILL.md").read_text(encoding="utf-8")
        )

    surfaces["codex_command"] = (ROOT / "commands/caveman.toml").read_text(encoding="utf-8")
    surfaces["opencode_command"] = _installed_opencode_command()
    surfaces["hook_fallback"] = _run_hook(ROOT / "src/hooks/caveman-activate.js", isolated=True)
    directive = _run_node("""
const directive = require('./extension/src/directive.js');
process.stdout.write(JSON.stringify({primer: directive.buildPrimer('full'), reminder: directive.buildReminder('full')}));
""")
    directive_value = json.loads(directive)
    surfaces["mv3_primer"] = directive_value["primer"]
    surfaces["mv3_reminder"] = directive_value["reminder"]
    return surfaces


def validate_surface_contract(manifest: dict[str, Any], surfaces: dict[str, str]) -> None:
    missing = REQUIRED_SURFACES - set(surfaces)
    if missing:
        raise ContractError(f"production surface evidence missing: {sorted(missing)}")
    for surface_id, content in surfaces.items():
        if not isinstance(content, str) or not content.strip():
            raise ContractError(f"empty production surface: {surface_id}")
    for group in manifest["invariant_groups"]:
        for surface_id in group["surfaces"]:
            haystack = surfaces[surface_id].casefold()
            if not any(marker.casefold() in haystack for marker in group["markers"]):
                raise ContractError(f"{group['id']} missing from {surface_id}")

    canonical = surfaces["canonical_skill"]
    for category, literals in manifest["protected_literals"].items():
        for literal in literals:
            if literal not in canonical:
                raise ContractError(f"protected {category} literal absent: {literal}")
    for category, concepts in manifest.get("canonical_concepts", {}).items():
        for concept in concepts:
            if concept.casefold() not in canonical.casefold():
                raise ContractError(f"canonical {category} concept absent: {concept}")

    forbidden = manifest["calibration_material"]["must_not_be_contract_assertions"]
    serialized_groups = json.dumps(manifest["invariant_groups"], ensure_ascii=False)
    for phrase in forbidden:
        if phrase in serialized_groups:
            raise ContractError(f"calibration wording pinned by invariant: {phrase}")


def parse_switch(prompt: str) -> dict[str, Any] | None:
    js = f"""
const parser = require('./src/hooks/caveman-parse.js');
const value = parser.parseModeChange({json.dumps(prompt)}, {{getDefaultMode: () => 'full'}});
process.stdout.write(JSON.stringify(value));
"""
    return json.loads(_run_node(js))


def measure_text(text: str) -> dict[str, int]:
    encoded = text.encode("utf-8")
    return {
        "utf8_bytes": len(encoded),
        "words": len(re.findall(r"\S+", text)),
        "lines": len(text.splitlines()),
        "bytes_div_4_proxy": (len(encoded) + 3) // 4,
    }


def validate_raw_snapshot(snapshot: dict[str, Any]) -> None:
    metadata = snapshot.get("metadata")
    records = snapshot.get("records")
    if not isinstance(metadata, dict) or not isinstance(records, list) or not records:
        raise ContractError("raw snapshot needs metadata and non-empty records")
    required_meta = {"run_id", "generated_at", "repetitions", "baseline_model_id", "candidate_model_id", "judge_model_id"}
    if not required_meta <= metadata.keys():
        raise ContractError(f"raw snapshot metadata missing: {sorted(required_meta - metadata.keys())}")
    required_record = {"case_id", "family", "prompt", "arm", "model_id", "system_prompt", "raw_output", "usage", "judge"}
    for index, record in enumerate(records):
        if not isinstance(record, dict) or not required_record <= record.keys():
            missing = required_record - set(record) if isinstance(record, dict) else required_record
            raise ContractError(f"raw snapshot record {index} missing: {sorted(missing)}")
        judge = record["judge"]
        if not isinstance(judge, dict) or not {"result", "uncertainty"} <= judge.keys():
            raise ContractError(f"raw snapshot record {index} has malformed judge evidence")
        if judge["result"] not in {"pass", "fail", "unknown"}:
            raise ContractError(f"raw snapshot record {index} has invalid judge result")
        if not isinstance(record["usage"], dict) or not record["usage"]:
            raise ContractError(f"raw snapshot record {index} has malformed usage")
    if metadata.get("paired"):
        pairs: dict[tuple[Any, Any], list[dict[str, Any]]] = {}
        for record in records:
            pairs.setdefault((record["case_id"], record.get("repetition")), []).append(record)
        for pair_id, pair in pairs.items():
            if {record["arm"] for record in pair} != {"baseline", "candidate"} or len(pair) != 2:
                raise ContractError(f"paired snapshot has incomplete arms for {pair_id}")
            if pair[0]["prompt"] != pair[1]["prompt"]:
                raise ContractError(f"paired snapshot changed user prompt for {pair_id}")
