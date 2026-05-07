#!/usr/bin/env python3
"""Local verification runner for caveman install surfaces."""

from __future__ import annotations

import json
import os
import shutil
import subprocess
import sys
import tempfile
import zipfile
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]


class CheckFailure(RuntimeError):
    pass


def section(title: str) -> None:
    print(f"\n== {title} ==")


def ensure(condition: bool, message: str) -> None:
    if not condition:
        raise CheckFailure(message)


def run(
    args: list[str],
    *,
    cwd: Path = ROOT,
    env: dict[str, str] | None = None,
    check: bool = True,
) -> subprocess.CompletedProcess[str]:
    merged_env = os.environ.copy()
    # Keep Python subprocess output decodable on Windows when the CLI prints Unicode.
    merged_env.setdefault("PYTHONIOENCODING", "utf-8")
    if env:
        merged_env.update(env)
    result = subprocess.run(
        args,
        cwd=cwd,
        env=merged_env,
        text=True,
        encoding="utf-8",
        capture_output=True,
        check=False,
    )
    if check and result.returncode != 0:
        raise CheckFailure(
            f"Command failed ({result.returncode}): {' '.join(args)}\n"
            f"stdout:\n{result.stdout}\n"
            f"stderr:\n{result.stderr}"
        )
    return result


def read_json(path: Path) -> object:
    return json.loads(path.read_text(encoding="utf-8"))


def shell_path(path: Path) -> str:
    return str(path).replace("\\", "/") if os.name == "nt" else str(path)


def _frontmatter_description(path: Path) -> str:
    lines = path.read_text(encoding="utf-8").splitlines()
    ensure(lines and lines[0] == "---", f"{path} missing YAML frontmatter")

    description_lines: list[str] = []
    collecting = False
    block_indent: int | None = None
    for line in lines[1:]:
        if line == "---":
            break
        if collecting:
            stripped = line.strip()
            if not stripped:
                description_lines.append("")
                continue
            indent = len(line) - len(line.lstrip(" \t"))
            if block_indent is None:
                if indent == 0:
                    break
                block_indent = indent
            elif indent < block_indent:
                break
            description_lines.append(stripped)
            continue
        if line.startswith("description:"):
            value = line.split(":", 1)[1].strip()
            # Folded (>) and literal (|) block scalars, with optional chomping (-/+).
            if value and value[0] in ("|", ">"):
                collecting = True
                continue
            return value.strip("'\"")
    return " ".join(part for part in description_lines if part)


def verify_skill_frontmatter_upload_compatibility() -> None:
    section("Skill Frontmatter Upload Compatibility")

    skill_paths = [
        ROOT / "skills/caveman/SKILL.md",
        ROOT / "skills/caveman-commit/SKILL.md",
        ROOT / "skills/caveman-help/SKILL.md",
        ROOT / "skills/caveman-review/SKILL.md",
        ROOT / "caveman-compress/SKILL.md",
    ]
    for path in skill_paths:
        description = _frontmatter_description(path)
        ensure(
            "<" not in description and ">" not in description,
            f"{path} description contains XML-like angle brackets",
        )

    print("Skill frontmatter descriptions avoid XML-like tags")


def verify_synced_files() -> None:
    section("Synced Files")
    skill_source = ROOT / "skills/caveman/SKILL.md"
    rule_source = ROOT / "rules/caveman-activate.md"

    skill_copies = [
        ROOT / "caveman/SKILL.md",
        ROOT / "plugins/caveman/skills/caveman/SKILL.md",
        ROOT / ".cursor/skills/caveman/SKILL.md",
        ROOT / ".windsurf/skills/caveman/SKILL.md",
    ]
    for copy in skill_copies:
        ensure(
            copy.read_text(encoding="utf-8") == skill_source.read_text(encoding="utf-8"),
            f"Skill copy mismatch: {copy}",
        )

    rule_copies = [
        ROOT / ".clinerules/caveman.md",
        ROOT / ".github/copilot-instructions.md",
    ]
    for copy in rule_copies:
        ensure(
            copy.read_text(encoding="utf-8") == rule_source.read_text(encoding="utf-8"),
            f"Rule copy mismatch: {copy}",
        )

    with zipfile.ZipFile(ROOT / "caveman.skill") as archive:
        ensure("caveman/SKILL.md" in archive.namelist(), "caveman.skill missing caveman/SKILL.md")
        ensure(
            archive.read("caveman/SKILL.md").decode("utf-8")
            == skill_source.read_text(encoding="utf-8"),
            "caveman.skill payload mismatch",
        )

    print("Synced copies and caveman.skill zip OK")


def verify_manifests_and_syntax() -> None:
    section("Manifests And Syntax")

    manifest_paths = [
        ROOT / ".agents/plugins/marketplace.json",
        ROOT / ".claude-plugin/plugin.json",
        ROOT / ".claude-plugin/marketplace.json",
        ROOT / ".codex/hooks.json",
        ROOT / "gemini-extension.json",
        ROOT / "plugins/caveman/.codex-plugin/plugin.json",
    ]
    for path in manifest_paths:
        read_json(path)

    run(["node", "--check", "hooks/caveman-config.js"])
    run(["node", "--check", "hooks/caveman-activate.js"])
    run(["node", "--check", "hooks/caveman-mode-tracker.js"])
    run(["bash", "-n", "hooks/install.sh"])
    run(["bash", "-n", "hooks/uninstall.sh"])
    run(["bash", "-n", "hooks/caveman-statusline.sh"])

    # Ensure install/uninstall scripts include caveman-config.js
    install_sh = (ROOT / "hooks/install.sh").read_text(encoding="utf-8")
    uninstall_sh = (ROOT / "hooks/uninstall.sh").read_text(encoding="utf-8")
    ensure("caveman-config.js" in install_sh, "install.sh missing caveman-config.js")
    ensure("caveman-config.js" in uninstall_sh, "uninstall.sh missing caveman-config.js")

    print("JSON manifests and JS/bash syntax OK")


def verify_powershell_static() -> None:
    section("PowerShell Static Checks")
    install_text = (ROOT / "hooks/install.ps1").read_text(encoding="utf-8")
    uninstall_text = (ROOT / "hooks/uninstall.ps1").read_text(encoding="utf-8")
    statusline_text = (ROOT / "hooks/caveman-statusline.ps1").read_text(encoding="utf-8")

    ensure("caveman-config.js" in install_text, "install.ps1 missing caveman-config.js")
    ensure("caveman-config.js" in uninstall_text, "uninstall.ps1 missing caveman-config.js")
    ensure("caveman-statusline.ps1" in install_text, "install.ps1 missing statusline.ps1")
    ensure("caveman-statusline.ps1" in uninstall_text, "uninstall.ps1 missing statusline.ps1")
    ensure("-AsHashtable" not in install_text, "install.ps1 should stay compatible with Windows PowerShell 5.1")
    ensure(
        "powershell -ExecutionPolicy Bypass -File" in install_text,
        "install.ps1 missing PowerShell statusline command",
    )
    ensure("[CAVEMAN" in statusline_text, "caveman-statusline.ps1 missing badge output")

    print("Windows install path statically wired")


def load_compress_modules():
    sys.path.insert(0, str(ROOT / "caveman-compress"))
    import scripts.benchmark  # noqa: F401
    import scripts.cli as cli
    import scripts.compress  # noqa: F401
    import scripts.detect as detect
    import scripts.validate as validate

    return cli, detect, validate


def verify_compress_fixtures() -> None:
    section("Compress Fixtures")
    _, detect, validate = load_compress_modules()

    fixtures = sorted((ROOT / "tests/caveman-compress").glob("*.original.md"))
    ensure(fixtures, "No caveman-compress fixtures found")

    for original in fixtures:
        compressed = original.with_name(original.name.replace(".original.md", ".md"))
        ensure(compressed.exists(), f"Missing compressed fixture for {original.name}")
        result = validate.validate(original, compressed)
        ensure(result.is_valid, f"Fixture validation failed for {compressed.name}: {result.errors}")
        ensure(detect.should_compress(compressed), f"Fixture should be compressible: {compressed.name}")

    print(f"Validated {len(fixtures)} caveman-compress fixture pairs")


def verify_compress_cli() -> None:
    section("Compress CLI")

    skip_result = run(
        ["python3", "-m", "scripts", "../hooks/install.sh"],
        cwd=ROOT / "caveman-compress",
        check=False,
    )
    ensure(skip_result.returncode == 0, "compress CLI skip path should exit 0")
    ensure("Detected: code" in skip_result.stdout, "compress CLI skip path missing detection output")
    ensure(
        "Skipping: file is not natural language" in skip_result.stdout,
        "compress CLI skip path missing skip output",
    )

    missing_result = run(
        ["python3", "-m", "scripts", "../does-not-exist.md"],
        cwd=ROOT / "caveman-compress",
        check=False,
    )
    ensure(missing_result.returncode == 1, "compress CLI missing-file path should exit 1")
    ensure("File not found" in missing_result.stdout, "compress CLI missing-file output mismatch")

    print("Compress CLI skip/error paths OK")


def verify_hook_install_flow() -> None:
    section("Claude Hook Flow")

    ensure(shutil.which("node") is not None, "node is required for hook verification")
    ensure(shutil.which("bash") is not None, "bash is required for hook verification")

    with tempfile.TemporaryDirectory(prefix="caveman-verify-") as temp_root:
        temp_root_path = Path(temp_root)
        home = temp_root_path / "home"
        claude_dir = home / ".claude"
        claude_dir.mkdir(parents=True)

        existing_settings = {
            "statusLine": {"type": "command", "command": "bash /tmp/existing-statusline.sh"},
            "hooks": {"Notification": [{"hooks": [{"type": "command", "command": "echo keep-me"}]}]},
        }
        (claude_dir / "settings.json").write_text(json.dumps(existing_settings, indent=2) + "\n")
        hook_env = {"HOME": shell_path(home), "CLAUDE_CONFIG_DIR": shell_path(claude_dir)}

        run(["bash", "hooks/install.sh"], env=hook_env)

        settings = read_json(claude_dir / "settings.json")
        hooks = settings["hooks"]
        ensure(settings["statusLine"]["command"] == "bash /tmp/existing-statusline.sh", "install.sh clobbered existing statusLine")
        ensure("SessionStart" in hooks, "SessionStart hook missing after install")
        ensure("UserPromptSubmit" in hooks, "UserPromptSubmit hook missing after install")

        activate = run(
            ["node", "hooks/caveman-activate.js"],
            env=hook_env,
        )
        ensure("CAVEMAN MODE ACTIVE" in activate.stdout, "activation output missing caveman banner")
        ensure("STATUSLINE SETUP NEEDED" not in activate.stdout, "activation should stay quiet when custom statusline exists")
        ensure((claude_dir / ".caveman-active").read_text(encoding="utf-8") == "full", "activation flag should default to full")

        # Test configurable default mode via CAVEMAN_DEFAULT_MODE env var
        activate_custom = run(
            ["node", "hooks/caveman-activate.js"],
            env={**hook_env, "CAVEMAN_DEFAULT_MODE": "ultra"},
        )
        ensure("CAVEMAN MODE ACTIVE" in activate_custom.stdout, "activation with custom default missing banner")
        ensure(
            (claude_dir / ".caveman-active").read_text(encoding="utf-8") == "ultra",
            "CAVEMAN_DEFAULT_MODE=ultra should set flag to ultra",
        )
        # Test "off" mode — activation skipped, flag removed
        activate_off = run(
            ["node", "hooks/caveman-activate.js"],
            env={**hook_env, "CAVEMAN_DEFAULT_MODE": "off"},
        )
        ensure("CAVEMAN MODE ACTIVE" not in activate_off.stdout, "off mode should not emit caveman banner")
        ensure(not (claude_dir / ".caveman-active").exists(), "off mode should remove flag file")

        # Test mode tracker with /caveman when default is off — should NOT write flag
        subprocess.run(
            ["node", "hooks/caveman-mode-tracker.js"],
            cwd=ROOT,
            env={**os.environ, **hook_env, "CAVEMAN_DEFAULT_MODE": "off"},
            text=True,
            encoding="utf-8",
            input='{"prompt":"/caveman"}',
            capture_output=True,
            check=True,
        )
        ensure(not (claude_dir / ".caveman-active").exists(), "/caveman with off default should not write flag")

        # Reset back to full for subsequent tests
        (claude_dir / ".caveman-active").write_text("full")

        run(
            ["node", "hooks/caveman-mode-tracker.js"],
            env=hook_env,
            check=True,
        )

        ultra_prompt = subprocess.run(
            ["node", "hooks/caveman-mode-tracker.js"],
            cwd=ROOT,
            env={**os.environ, **hook_env},
            text=True,
            encoding="utf-8",
            input='{"prompt":"/caveman ultra"}',
            capture_output=True,
            check=True,
        )
        ensure(
            "CAVEMAN MODE ACTIVE (ultra)" in ultra_prompt.stdout,
            "mode tracker should emit active-mode reinforcement",
        )
        ensure((claude_dir / ".caveman-active").read_text(encoding="utf-8") == "ultra", "mode tracker did not record ultra")

        subprocess.run(
            ["node", "hooks/caveman-mode-tracker.js"],
            cwd=ROOT,
            env={**os.environ, **hook_env},
            text=True,
            encoding="utf-8",
            input='{"prompt":"normal mode"}',
            capture_output=True,
            check=True,
        )
        ensure(not (claude_dir / ".caveman-active").exists(), "normal mode should remove flag file")

        (claude_dir / ".caveman-active").write_text("wenyan-ultra")
        statusline = run(
            ["bash", "hooks/caveman-statusline.sh"],
            env=hook_env,
        )
        ensure("[CAVEMAN:WENYAN-ULTRA]" in statusline.stdout, "statusline badge output mismatch")

        reinstall = run(["bash", "hooks/install.sh"], env=hook_env)
        ensure("Nothing to do" in reinstall.stdout, "install.sh should be idempotent")

        run(["bash", "hooks/uninstall.sh"], env=hook_env)
        settings_after = read_json(claude_dir / "settings.json")
        ensure(settings_after == existing_settings, "uninstall.sh did not restore non-caveman settings")
        ensure(not (claude_dir / ".caveman-active").exists(), "uninstall.sh should remove flag file")

    with tempfile.TemporaryDirectory(prefix="caveman-verify-fresh-") as temp_root:
        home = Path(temp_root) / "home"
        claude_dir = home / ".claude"
        hook_env = {"HOME": shell_path(home), "CLAUDE_CONFIG_DIR": shell_path(claude_dir)}
        run(["bash", "hooks/install.sh"], env=hook_env)
        settings = read_json(claude_dir / "settings.json")
        ensure("statusLine" in settings, "fresh install should configure statusline")
        activate = run(["node", "hooks/caveman-activate.js"], env=hook_env)
        ensure("STATUSLINE SETUP NEEDED" not in activate.stdout, "fresh install should not nudge for statusline")
        run(["bash", "hooks/uninstall.sh"], env=hook_env)
        ensure(read_json(claude_dir / "settings.json") == {}, "fresh uninstall should leave empty settings")

    print("Claude hook install/uninstall flow OK")


def main() -> int:
    checks = [
        verify_skill_frontmatter_upload_compatibility,
        verify_synced_files,
        verify_manifests_and_syntax,
        verify_powershell_static,
        verify_compress_fixtures,
        verify_compress_cli,
        verify_hook_install_flow,
    ]

    try:
        for check in checks:
            check()
    except CheckFailure as exc:
        print(f"\nFAIL: {exc}", file=sys.stderr)
        return 1

    print("\nAll local verification checks passed")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
