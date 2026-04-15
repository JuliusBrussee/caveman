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
    if env:
        merged_env.update(env)
    result = subprocess.run(
        args,
        cwd=cwd,
        env=merged_env,
        text=True,
        encoding="utf-8",
        errors="replace",
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


def bash_args(*args: str) -> list[str]:
    return ["bash", *args]


def read_json(path: Path) -> object:
    return json.loads(path.read_text(encoding="utf-8"))


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
            archive.read("caveman/SKILL.md").decode("utf-8") == skill_source.read_text(encoding="utf-8"),
            "caveman.skill payload mismatch",
        )

    print("Synced copies and caveman.skill zip OK")


def verify_extension_sync() -> None:
    section("Copilot CLI Extension Sync")
    ext_dir = ROOT / ".github/extensions/caveman"

    # Extension files exist
    for name in ("extension.mjs", "rules.mjs", "parser.mjs", "config.js"):
        ensure((ext_dir / name).exists(), f"Extension file missing: {name}")

    # parser.mjs matches canonical source
    canonical = (ROOT / "lib/caveman-mode-parser.mjs").read_text(encoding="utf-8")
    copy = (ext_dir / "parser.mjs").read_text(encoding="utf-8")
    ensure(canonical == copy, "parser.mjs out of sync with lib/caveman-mode-parser.mjs")

    canonical_config = (ROOT / "lib/caveman-config.js").read_text(encoding="utf-8")
    hook_config = (ROOT / "hooks/caveman-config.js").read_text(encoding="utf-8")
    extension_config = (ext_dir / "config.js").read_text(encoding="utf-8")
    ensure(hook_config == canonical_config, "hooks/caveman-config.js out of sync with lib/caveman-config.js")
    ensure(extension_config == canonical_config, ".github/extensions/caveman/config.js out of sync with lib/caveman-config.js")

    # rules.mjs contains expected exports with non-empty content
    rules_text = (ext_dir / "rules.mjs").read_text(encoding="utf-8")
    ensure(
        rules_text.startswith("// caveman — bundled SKILL.md rules for Copilot CLI extension"),
        "rules.mjs header mismatch",
    )
    for export_name in ("CAVEMAN_SKILL", "CAVEMAN_COMMIT_SKILL", "CAVEMAN_REVIEW_SKILL",
                        "CAVEMAN_HELP_SKILL", "CAVEMAN_COMPRESS_SKILL"):
        ensure(f"export const {export_name}" in rules_text,
               f"rules.mjs missing export: {export_name}")
        # Verify export is not an empty string
        idx = rules_text.find(f"export const {export_name}")
        ensure(idx != -1 and '""' not in rules_text[idx:idx+len(export_name)+30],
               f"rules.mjs export {export_name} appears empty")

    print("Copilot CLI extension files present and in sync")


def verify_copilot_installer_static() -> None:
    section("Copilot CLI Installer")

    installer = ROOT / "scripts/install-copilot-extension.mjs"
    installer_sh = ROOT / "scripts/install-copilot-extension.sh"
    installer_ps1 = ROOT / "scripts/install-copilot-extension.ps1"

    ensure(installer.exists(), "Copilot installer missing")
    ensure(installer_sh.exists(), "Copilot shell wrapper missing")
    ensure(installer_ps1.exists(), "Copilot PowerShell wrapper missing")

    run(["node", "scripts/install-copilot-extension.mjs", "--help"])
    if os.name != "nt":
        run(bash_args("-n", "scripts/install-copilot-extension.sh"))

    shell_text = installer_sh.read_text(encoding="utf-8")
    ps1_text = installer_ps1.read_text(encoding="utf-8")
    ensure("install-copilot-extension.mjs" in shell_text, "Shell wrapper missing installer reference")
    ensure("install-copilot-extension.mjs" in ps1_text, "PowerShell wrapper missing installer reference")

    print("Copilot CLI installer files present and wired")


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

    run(["node", "--check", "lib/caveman-config.js"])
    run(["node", "--check", "hooks/caveman-config.js"])
    run(["node", "--check", "hooks/caveman-activate.js"])
    run(["node", "--check", "hooks/caveman-mode-tracker.js"])

    # Copilot CLI extension syntax (ES module). Use --check instead of dynamic
    # import because the SDK package is only available inside Copilot CLI.
    run(["node", "--check", ".github/extensions/caveman/extension.mjs"])
    run(["node", "--check", ".github/extensions/caveman/config.js"])
    run(["node", "--input-type=module", "-e",
         f"import('{(ROOT / '.github/extensions/caveman/rules.mjs').resolve().as_uri()}')"])
    run(["node", "--input-type=module", "-e",
         f"import('{(ROOT / '.github/extensions/caveman/parser.mjs').resolve().as_uri()}')"])
    if os.name != "nt":
        run(bash_args("-n", "hooks/install.sh"))
        run(bash_args("-n", "hooks/uninstall.sh"))
        run(bash_args("-n", "hooks/caveman-statusline.sh"))

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
    python_env = {"PYTHONIOENCODING": "utf-8", "PYTHONUTF8": "1"}

    skip_result = run(
        [sys.executable, "-m", "scripts", "../hooks/install.sh"],
        cwd=ROOT / "caveman-compress",
        env=python_env,
        check=False,
    )
    ensure(skip_result.returncode == 0, "compress CLI skip path should exit 0")
    ensure("Detected: code" in skip_result.stdout, "compress CLI skip path missing detection output")
    ensure(
        "Skipping: file is not natural language" in skip_result.stdout,
        "compress CLI skip path missing skip output",
    )

    missing_result = run(
        [sys.executable, "-m", "scripts", "../does-not-exist.md"],
        cwd=ROOT / "caveman-compress",
        env=python_env,
        check=False,
    )
    ensure(missing_result.returncode == 1, "compress CLI missing-file path should exit 1")
    ensure("File not found" in missing_result.stdout, "compress CLI missing-file output mismatch")

    print("Compress CLI skip/error paths OK")


def verify_hook_install_flow() -> None:
    section("Claude Hook Flow")

    ensure(shutil.which("node") is not None, "node is required for hook verification")
    if os.name == "nt":
        print("Skipping bash hook flow on Windows; PowerShell path is covered separately")
        return

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

        run(bash_args("hooks/install.sh"), env={"HOME": str(home)})

        settings = read_json(claude_dir / "settings.json")
        hooks = settings["hooks"]
        ensure(settings["statusLine"]["command"] == "bash /tmp/existing-statusline.sh", "install.sh clobbered existing statusLine")
        ensure("SessionStart" in hooks, "SessionStart hook missing after install")
        ensure("UserPromptSubmit" in hooks, "UserPromptSubmit hook missing after install")

        activate = run(
            ["node", "hooks/caveman-activate.js"],
            env={"HOME": str(home)},
        )
        ensure("CAVEMAN MODE ACTIVE." in activate.stdout, "activation output missing caveman banner")
        ensure("STATUSLINE SETUP NEEDED" not in activate.stdout, "activation should stay quiet when custom statusline exists")
        ensure((claude_dir / ".caveman-active").read_text() == "full", "activation flag should default to full")

        # Test configurable default mode via CAVEMAN_DEFAULT_MODE env var
        activate_custom = run(
            ["node", "hooks/caveman-activate.js"],
            env={"HOME": str(home), "CAVEMAN_DEFAULT_MODE": "ultra"},
        )
        ensure("CAVEMAN MODE ACTIVE." in activate_custom.stdout, "activation with custom default missing banner")
        ensure((claude_dir / ".caveman-active").read_text() == "ultra", "CAVEMAN_DEFAULT_MODE=ultra should set flag to ultra")
        # Test "off" mode — activation skipped, flag removed
        activate_off = run(
            ["node", "hooks/caveman-activate.js"],
            env={"HOME": str(home), "CAVEMAN_DEFAULT_MODE": "off"},
        )
        ensure("CAVEMAN MODE ACTIVE." not in activate_off.stdout, "off mode should not emit caveman banner")
        ensure(not (claude_dir / ".caveman-active").exists(), "off mode should remove flag file")

        # Test mode tracker with /caveman when default is off — should NOT write flag
        subprocess.run(
            ["node", "hooks/caveman-mode-tracker.js"],
            cwd=ROOT,
            env={**os.environ, "HOME": str(home), "CAVEMAN_DEFAULT_MODE": "off"},
            text=True,
            input='{"prompt":"/caveman"}',
            capture_output=True,
            check=True,
        )
        ensure(not (claude_dir / ".caveman-active").exists(), "/caveman with off default should not write flag")

        # Reset back to full for subsequent tests
        (claude_dir / ".caveman-active").write_text("full")

        run(
            ["node", "hooks/caveman-mode-tracker.js"],
            env={"HOME": str(home)},
            check=True,
        )

        ultra_prompt = subprocess.run(
            ["node", "hooks/caveman-mode-tracker.js"],
            cwd=ROOT,
            env={**os.environ, "HOME": str(home)},
            text=True,
            input='{"prompt":"/caveman ultra"}',
            capture_output=True,
            check=True,
        )
        ensure(ultra_prompt.stdout == "", "mode tracker should stay silent")
        ensure((claude_dir / ".caveman-active").read_text() == "ultra", "mode tracker did not record ultra")

        full_prompt = subprocess.run(
            ["node", "hooks/caveman-mode-tracker.js"],
            cwd=ROOT,
            env={**os.environ, "HOME": str(home)},
            text=True,
            input='{"prompt":"/caveman full"}',
            capture_output=True,
            check=True,
        )
        ensure(full_prompt.stdout == "", "mode tracker should stay silent for /caveman full")
        ensure((claude_dir / ".caveman-active").read_text() == "full", "mode tracker did not record full")

        not_stop_prompt = subprocess.run(
            ["node", "hooks/caveman-mode-tracker.js"],
            cwd=ROOT,
            env={**os.environ, "HOME": str(home)},
            text=True,
            input='{"prompt":"How do I stop caveman?"}',
            capture_output=True,
            check=True,
        )
        ensure(not_stop_prompt.stdout == "", "mode tracker should stay silent for non-stop prose")
        ensure((claude_dir / ".caveman-active").read_text() == "full", "question about stop should not clear flag")

        off_prompt = subprocess.run(
            ["node", "hooks/caveman-mode-tracker.js"],
            cwd=ROOT,
            env={**os.environ, "HOME": str(home)},
            text=True,
            input='{"prompt":"/caveman off"}',
            capture_output=True,
            check=True,
        )
        ensure(off_prompt.stdout == "", "mode tracker should stay silent for /caveman off")
        ensure(not (claude_dir / ".caveman-active").exists(), "/caveman off should remove flag file")

        (claude_dir / ".caveman-active").write_text("full")
        subprocess.run(
            ["node", "hooks/caveman-mode-tracker.js"],
            cwd=ROOT,
            env={**os.environ, "HOME": str(home)},
            text=True,
            input='{"prompt":"normal mode"}',
            capture_output=True,
            check=True,
        )
        ensure(not (claude_dir / ".caveman-active").exists(), "normal mode should remove flag file")

        (claude_dir / ".caveman-active").write_text("wenyan-ultra")
        statusline = run(
            bash_args("hooks/caveman-statusline.sh"),
            env={"HOME": str(home)},
        )
        ensure("[CAVEMAN:WENYAN-ULTRA]" in statusline.stdout, "statusline badge output mismatch")

        reinstall = run(bash_args("hooks/install.sh"), env={"HOME": str(home)})
        ensure("Nothing to do" in reinstall.stdout, "install.sh should be idempotent")

        run(bash_args("hooks/uninstall.sh"), env={"HOME": str(home)})
        settings_after = read_json(claude_dir / "settings.json")
        ensure(settings_after == existing_settings, "uninstall.sh did not restore non-caveman settings")
        ensure(not (claude_dir / ".caveman-active").exists(), "uninstall.sh should remove flag file")

    with tempfile.TemporaryDirectory(prefix="caveman-verify-fresh-") as temp_root:
        home = Path(temp_root) / "home"
        run(bash_args("hooks/install.sh"), env={"HOME": str(home)})
        claude_dir = home / ".claude"
        settings = read_json(claude_dir / "settings.json")
        ensure("statusLine" in settings, "fresh install should configure statusline")
        activate = run(["node", "hooks/caveman-activate.js"], env={"HOME": str(home)})
        ensure("STATUSLINE SETUP NEEDED" not in activate.stdout, "fresh install should not nudge for statusline")
        run(bash_args("hooks/uninstall.sh"), env={"HOME": str(home)})
        ensure(read_json(claude_dir / "settings.json") == {}, "fresh uninstall should leave empty settings")

    print("Claude hook install/uninstall flow OK")


def main() -> int:
    checks = [
        verify_synced_files,
        verify_extension_sync,
        verify_copilot_installer_static,
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
