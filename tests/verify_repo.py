#!/usr/bin/env python3
"""Local verification runner for layman install surfaces."""

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
    if env:
        merged_env.update(env)
    result = subprocess.run(
        args,
        cwd=cwd,
        env=merged_env,
        text=True,
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
    return json.loads(path.read_text())


def verify_synced_files() -> None:
    section("Synced Files")
    skill_source = ROOT / "skills/layman/SKILL.md"
    rule_source = ROOT / "rules/layman-activate.md"

    skill_copies = [
        ROOT / "layman/SKILL.md",
        ROOT / "plugins/layman/skills/layman/SKILL.md",
        ROOT / ".cursor/skills/layman/SKILL.md",
        ROOT / ".windsurf/skills/layman/SKILL.md",
    ]
    for copy in skill_copies:
        ensure(copy.read_text() == skill_source.read_text(), f"Skill copy mismatch: {copy}")

    rule_copies = [
        ROOT / ".clinerules/layman.md",
        ROOT / ".github/copilot-instructions.md",
    ]
    for copy in rule_copies:
        ensure(copy.read_text() == rule_source.read_text(), f"Rule copy mismatch: {copy}")

    with zipfile.ZipFile(ROOT / "layman.skill") as archive:
        ensure("layman/SKILL.md" in archive.namelist(), "layman.skill missing layman/SKILL.md")
        ensure(
            archive.read("layman/SKILL.md").decode("utf-8") == skill_source.read_text(),
            "layman.skill payload mismatch",
        )

    print("Synced copies and layman.skill zip OK")


def verify_manifests_and_syntax() -> None:
    section("Manifests And Syntax")

    manifest_paths = [
        ROOT / ".agents/plugins/marketplace.json",
        ROOT / ".claude-plugin/plugin.json",
        ROOT / ".claude-plugin/marketplace.json",
        ROOT / ".codex/hooks.json",
        ROOT / "gemini-extension.json",
        ROOT / "plugins/layman/.codex-plugin/plugin.json",
    ]
    for path in manifest_paths:
        read_json(path)

    run(["node", "--check", "hooks/layman-config.js"])
    run(["node", "--check", "hooks/layman-activate.js"])
    run(["node", "--check", "hooks/layman-mode-tracker.js"])
    run(["bash", "-n", "hooks/install.sh"])
    run(["bash", "-n", "hooks/uninstall.sh"])
    run(["bash", "-n", "hooks/layman-statusline.sh"])

    # Ensure install/uninstall scripts include layman-config.js
    install_sh = (ROOT / "hooks/install.sh").read_text()
    uninstall_sh = (ROOT / "hooks/uninstall.sh").read_text()
    ensure("layman-config.js" in install_sh, "install.sh missing layman-config.js")
    ensure("layman-config.js" in uninstall_sh, "uninstall.sh missing layman-config.js")

    print("JSON manifests and JS/bash syntax OK")


def verify_powershell_static() -> None:
    section("PowerShell Static Checks")
    install_text = (ROOT / "hooks/install.ps1").read_text()
    uninstall_text = (ROOT / "hooks/uninstall.ps1").read_text()
    statusline_text = (ROOT / "hooks/layman-statusline.ps1").read_text()

    ensure("layman-config.js" in install_text, "install.ps1 missing layman-config.js")
    ensure("layman-config.js" in uninstall_text, "uninstall.ps1 missing layman-config.js")
    ensure("layman-statusline.ps1" in install_text, "install.ps1 missing statusline.ps1")
    ensure("layman-statusline.ps1" in uninstall_text, "uninstall.ps1 missing statusline.ps1")
    ensure("-AsHashtable" not in install_text, "install.ps1 should stay compatible with Windows PowerShell 5.1")
    ensure(
        "powershell -ExecutionPolicy Bypass -File" in install_text,
        "install.ps1 missing PowerShell statusline command",
    )
    ensure("[LAYMAN" in statusline_text, "layman-statusline.ps1 missing badge output")

    print("Windows install path statically wired")


def load_compress_modules():
    sys.path.insert(0, str(ROOT / "layman-compress"))
    import scripts.benchmark  # noqa: F401
    import scripts.cli as cli
    import scripts.compress  # noqa: F401
    import scripts.detect as detect
    import scripts.validate as validate

    return cli, detect, validate


def verify_compress_fixtures() -> None:
    section("Compress Fixtures")
    _, detect, validate = load_compress_modules()

    fixtures = sorted((ROOT / "tests/layman-compress").glob("*.original.md"))
    ensure(fixtures, "No layman-compress fixtures found")

    for original in fixtures:
        compressed = original.with_name(original.name.replace(".original.md", ".md"))
        ensure(compressed.exists(), f"Missing compressed fixture for {original.name}")
        result = validate.validate(original, compressed)
        ensure(result.is_valid, f"Fixture validation failed for {compressed.name}: {result.errors}")
        ensure(detect.should_compress(compressed), f"Fixture should be compressible: {compressed.name}")

    print(f"Validated {len(fixtures)} layman-compress fixture pairs")


def verify_compress_cli() -> None:
    section("Compress CLI")

    skip_result = run(
        ["python3", "-m", "scripts", "../hooks/install.sh"],
        cwd=ROOT / "layman-compress",
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
        cwd=ROOT / "layman-compress",
        check=False,
    )
    ensure(missing_result.returncode == 1, "compress CLI missing-file path should exit 1")
    ensure("File not found" in missing_result.stdout, "compress CLI missing-file output mismatch")

    print("Compress CLI skip/error paths OK")


def verify_hook_install_flow() -> None:
    section("Claude Hook Flow")

    ensure(shutil.which("node") is not None, "node is required for hook verification")
    ensure(shutil.which("bash") is not None, "bash is required for hook verification")

    with tempfile.TemporaryDirectory(prefix="layman-verify-") as temp_root:
        temp_root_path = Path(temp_root)
        home = temp_root_path / "home"
        claude_dir = home / ".claude"
        claude_dir.mkdir(parents=True)

        existing_settings = {
            "statusLine": {"type": "command", "command": "bash /tmp/existing-statusline.sh"},
            "hooks": {"Notification": [{"hooks": [{"type": "command", "command": "echo keep-me"}]}]},
        }
        (claude_dir / "settings.json").write_text(json.dumps(existing_settings, indent=2) + "\n")

        run(["bash", "hooks/install.sh"], env={"HOME": str(home)})

        settings = read_json(claude_dir / "settings.json")
        hooks = settings["hooks"]
        ensure(settings["statusLine"]["command"] == "bash /tmp/existing-statusline.sh", "install.sh clobbered existing statusLine")
        ensure("SessionStart" in hooks, "SessionStart hook missing after install")
        ensure("UserPromptSubmit" in hooks, "UserPromptSubmit hook missing after install")

        activate = run(
            ["node", "hooks/layman-activate.js"],
            env={"HOME": str(home)},
        )
        ensure("LAYMAN MODE ACTIVE" in activate.stdout, "activation output missing layman banner")
        ensure("STATUSLINE SETUP NEEDED" not in activate.stdout, "activation should stay quiet when custom statusline exists")
        ensure((claude_dir / ".layman-active").read_text() == "summary", "activation flag should default to summary")

        # Test configurable default mode via LAYMAN_DEFAULT_MODE env var
        activate_custom = run(
            ["node", "hooks/layman-activate.js"],
            env={"HOME": str(home), "LAYMAN_DEFAULT_MODE": "explain"},
        )
        ensure("LAYMAN MODE ACTIVE" in activate_custom.stdout, "activation with custom default missing banner")
        ensure((claude_dir / ".layman-active").read_text() == "explain", "LAYMAN_DEFAULT_MODE=explain should set flag to explain")
        # Test "off" mode — activation skipped, flag removed
        activate_off = run(
            ["node", "hooks/layman-activate.js"],
            env={"HOME": str(home), "LAYMAN_DEFAULT_MODE": "off"},
        )
        ensure("LAYMAN MODE ACTIVE" not in activate_off.stdout, "off mode should not emit layman banner")
        ensure(not (claude_dir / ".layman-active").exists(), "off mode should remove flag file")

        # Test mode tracker with /layman when default is off — should NOT write flag
        subprocess.run(
            ["node", "hooks/layman-mode-tracker.js"],
            cwd=ROOT,
            env={**os.environ, "HOME": str(home), "LAYMAN_DEFAULT_MODE": "off"},
            text=True,
            input='{"prompt":"/layman"}',
            capture_output=True,
            check=True,
        )
        ensure(not (claude_dir / ".layman-active").exists(), "/layman with off default should not write flag")

        # Reset back to summary for subsequent tests
        (claude_dir / ".layman-active").write_text("summary")

        run(
            ["node", "hooks/layman-mode-tracker.js"],
            env={"HOME": str(home)},
            check=True,
        )

        explain_prompt = subprocess.run(
            ["node", "hooks/layman-mode-tracker.js"],
            cwd=ROOT,
            env={**os.environ, "HOME": str(home)},
            text=True,
            input='{"prompt":"/layman explain"}',
            capture_output=True,
            check=True,
        )
        ensure("LAYMAN MODE ACTIVE" in explain_prompt.stdout, "mode tracker should emit Layman reinforcement")
        ensure((claude_dir / ".layman-active").read_text() == "explain", "mode tracker did not record explain")

        subprocess.run(
            ["node", "hooks/layman-mode-tracker.js"],
            cwd=ROOT,
            env={**os.environ, "HOME": str(home)},
            text=True,
            input='{"prompt":"normal mode"}',
            capture_output=True,
            check=True,
        )
        ensure(not (claude_dir / ".layman-active").exists(), "normal mode should remove flag file")

        (claude_dir / ".layman-active").write_text("explain")
        statusline = run(
            ["bash", "hooks/layman-statusline.sh"],
            env={"HOME": str(home)},
        )
        ensure("[LAYMAN:EXPLAIN]" in statusline.stdout, "statusline badge output mismatch")

        reinstall = run(["bash", "hooks/install.sh"], env={"HOME": str(home)})
        ensure("Nothing to do" in reinstall.stdout, "install.sh should be idempotent")

        run(["bash", "hooks/uninstall.sh"], env={"HOME": str(home)})
        settings_after = read_json(claude_dir / "settings.json")
        ensure(settings_after == existing_settings, "uninstall.sh did not restore non-layman settings")
        ensure(not (claude_dir / ".layman-active").exists(), "uninstall.sh should remove flag file")

    with tempfile.TemporaryDirectory(prefix="layman-verify-fresh-") as temp_root:
        home = Path(temp_root) / "home"
        run(["bash", "hooks/install.sh"], env={"HOME": str(home)})
        claude_dir = home / ".claude"
        settings = read_json(claude_dir / "settings.json")
        ensure("statusLine" in settings, "fresh install should configure statusline")
        activate = run(["node", "hooks/layman-activate.js"], env={"HOME": str(home)})
        ensure("STATUSLINE SETUP NEEDED" not in activate.stdout, "fresh install should not nudge for statusline")
        run(["bash", "hooks/uninstall.sh"], env={"HOME": str(home)})
        ensure(read_json(claude_dir / "settings.json") == {}, "fresh uninstall should leave empty settings")

    print("Claude hook install/uninstall flow OK")


def main() -> int:
    checks = [
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
