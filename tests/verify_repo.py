#!/usr/bin/env python3
"""Local verification runner for caveman install surfaces."""

from __future__ import annotations

import hashlib
import json
import os
import re
import shutil
import subprocess
import sys
import tempfile
import zipfile
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
        stdin=subprocess.DEVNULL,
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


def verify_shipped_skills_are_documented() -> None:
    """Every skills/*/SKILL.md installs into end users' agents.

    .claude-plugin/marketplace.json sets source "./", so the plugin root is the
    repo root and Claude Code auto-discovers every skills/ subdirectory — there
    is no allowlist in plugin.json to gate it. A directory added here silently
    claims a slot in every user's skill list and competes for activation, so it
    must at least be named in the docs that tell users what they installed.
    """
    section("Shipped Skills Are Documented")

    shipped = sorted(
        path.parent.name
        for path in (ROOT / "skills").glob("*/SKILL.md")
    )
    ensure(shipped, "no skills found — check the glob")

    docs = "\n".join(
        (ROOT / name).read_text(encoding="utf-8")
        for name in ("README.md", "CLAUDE.md")
    )
    # Only count a skill as documented when it is named in a code span — a
    # path, a table cell, a slash command. A bare substring search over the
    # whole document passes on any sentence that happens to use the word, so
    # `skills/migration/` was "documented" by every prose mention of the word
    # migration, and the guard could not fail for the drift it exists to catch.
    spans = re.findall(r"`([^`\n]+)`", docs)
    # Brace groups (`skills/{a,b,c}/SKILL.md`) are how the tables list families.
    expanded = re.sub(r"[{}]", ",", "\n".join(spans))
    named = set(re.split(r"[^A-Za-z0-9-]+", expanded))
    undocumented = [name for name in shipped if name not in named]
    ensure(
        not undocumented,
        "these skills ship to users but are named in neither README.md nor "
        f"CLAUDE.md: {', '.join(undocumented)}. Document them, or move them "
        "out of skills/ so they stop auto-installing.",
    )

    print(f"All {len(shipped)} shipped skills are documented")


def verify_skill_frontmatter_upload_compatibility() -> None:
    section("Skill Frontmatter Upload Compatibility")

    skill_paths = [
        ROOT / "skills/caveman/SKILL.md",
        ROOT / "skills/caveman-commit/SKILL.md",
        ROOT / "skills/caveman-help/SKILL.md",
        ROOT / "skills/caveman-review/SKILL.md",
        ROOT / "skills/caveman-compress/SKILL.md",
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

    # Every artifact sync-skill.yml mirrors, not just the first one. Checking one
    # of four let the other three drift silently between runs.
    skill_copies = [
        (ROOT / "plugins/caveman/skills/caveman/SKILL.md", skill_source),
        (ROOT / "plugins/caveman/skills/cavecrew/SKILL.md", ROOT / "skills/cavecrew/SKILL.md"),
        (
            ROOT / "plugins/caveman/skills/caveman-compress/SKILL.md",
            ROOT / "skills/caveman-compress/SKILL.md",
        ),
    ]
    for agent in ("cavecrew-investigator", "cavecrew-builder", "cavecrew-reviewer"):
        skill_copies.append(
            (ROOT / f"plugins/caveman/agents/{agent}.md", ROOT / f"agents/{agent}.md")
        )
    for copy, source in skill_copies:
        ensure(copy.exists(), f"Missing plugin mirror: {copy}")
        ensure(
            copy.read_text(encoding="utf-8") == source.read_text(encoding="utf-8"),
            f"Skill copy mismatch: {copy}",
        )

    with zipfile.ZipFile(ROOT / "dist" / "caveman.skill") as archive:
        ensure("caveman/SKILL.md" in archive.namelist(), "caveman.skill missing caveman/SKILL.md")
        ensure(
            archive.read("caveman/SKILL.md").decode("utf-8")
            == skill_source.read_text(encoding="utf-8"),
            "caveman.skill payload mismatch",
        )
        # EXTRA entries, not just missing ones. `zip -r` adds to an existing
        # archive, and dist/caveman.skill is tracked, so a file deleted from
        # skills/caveman/ stayed in the shipped ZIP forever — invisible to a
        # presence-only check.
        packaged = {
            name for name in archive.namelist() if not name.endswith("/")
        }
        on_disk = {
            f"caveman/{path.relative_to(ROOT / 'skills/caveman').as_posix()}"
            for path in (ROOT / "skills/caveman").rglob("*")
            if path.is_file() and "__pycache__" not in path.parts
        }
        ensure(
            packaged == on_disk,
            f"caveman.skill contents drifted: stale={sorted(packaged - on_disk)}, "
            f"missing={sorted(on_disk - packaged)}",
        )

    ensure(
        (ROOT / "bin" / "install.js").exists(),
        "bin/install.js missing — package.json bin entry would break npx caveman",
    )
    ensure(
        (ROOT / "bin" / "lib" / "settings.js").exists(),
        "bin/lib/settings.js missing — installer would crash on JSONC settings.json",
    )

    print("Synced copies, caveman.skill zip, and installer entrypoints OK")


def verify_manifests_and_syntax() -> None:
    section("Manifests And Syntax")

    claude_manifest_path = ROOT / ".claude-plugin/plugin.json"
    manifest_paths = [
        claude_manifest_path,
        ROOT / ".claude-plugin/marketplace.json",
        ROOT / ".codex/hooks.json",
        ROOT / "gemini-extension.json",
        ROOT / "plugins/caveman/.codex-plugin/plugin.json",
    ]
    for path in manifest_paths:
        read_json(path)

    claude_manifest = read_json(claude_manifest_path)
    ensure(isinstance(claude_manifest, dict), "Claude plugin manifest must be an object")
    # An explicit `agents` array loads ZERO agents on Claude Code 2.1.235 —
    # `claude plugin details caveman` reports "Agents (0)" with the array and
    # "Agents (3)" without it, so the cavecrew subagents the cavecrew skill
    # delegates to simply did not exist for plugin users. The docs say
    # string|string[] is valid; the shipped loader disagrees, and a directory
    # string is rejected outright ("agents: Invalid input"). Rely on the
    # default agents/ scan instead.
    ensure(
        "agents" not in claude_manifest,
        "plugin.json must not declare `agents` — the array form loads no "
        "agents at all; the default agents/ scan is the only working path",
    )
    # The default scan turns EVERY top-level .md in agents/ into a subagent,
    # named from its frontmatter or filename. agents/AGENTS.md and
    # agents/CLAUDE.md (maintainer docs for the profile registry) shipped as
    # bogus subagents named AGENTS and CLAUDE; they now live in agents/docs/,
    # which the scan does not recurse into.
    expected_agent_files = {
        "cavecrew-builder.md",
        "cavecrew-investigator.md",
        "cavecrew-reviewer.md",
    }
    top_level_agent_md = {path.name for path in (ROOT / "agents").glob("*.md")}
    ensure(
        top_level_agent_md == expected_agent_files,
        "agents/*.md is scanned wholesale into every user's subagent list; "
        f"unexpected files ship as subagents: {sorted(top_level_agent_md - expected_agent_files)}. "
        "Move non-agent markdown into agents/docs/.",
    )

    # Claude Code loads commands/*.md as flat skills alongside skills/*/SKILL.md,
    # so a .md stub sharing a skill's name registers that name twice — `caveman`,
    # `caveman-commit`, `caveman-review` and `caveman-stats` each appeared twice
    # in `claude plugin details`, with a 3-line stub competing against the real
    # ruleset for the same slash command. The .toml stubs are Codex/Gemini-only
    # and are not scanned.
    skill_names = {path.parent.name for path in (ROOT / "skills").glob("*/SKILL.md")}
    command_stubs = {path.stem for path in (ROOT / "commands").glob("*.md")}
    collisions = sorted(skill_names & command_stubs)
    ensure(
        not collisions,
        f"commands/*.md shadows same-named skills/: {', '.join(collisions)}. "
        "Both register as skills, so the slash command is ambiguous.",
    )

    hook_dir = ROOT / "src/hooks"
    expected_hooks = {
        "package.json",
        "caveman-config.js",
        "caveman-parse.js",
        "caveman-activate.js",
        "caveman-mode-tracker.js",
        "caveman-stats.js",
        "caveman-statusline.sh",
        "caveman-statusline.ps1",
        "cavecrew-model-overrides.js",
    }
    manifest: dict[str, str] = {}
    for line in (hook_dir / "checksums.sha256").read_text(encoding="utf-8").splitlines():
        digest, filename = line.split(maxsplit=1)
        manifest[filename] = digest
    ensure(set(manifest) == expected_hooks, "hook checksum manifest file set mismatch")
    for filename, expected in manifest.items():
        actual = hashlib.sha256((hook_dir / filename).read_bytes()).hexdigest()
        ensure(actual == expected, f"hook checksum mismatch: {filename}")

    run(["node", "--check", "src/hooks/caveman-config.js"])
    run(["node", "--check", "src/hooks/caveman-parse.js"])
    run(["node", "--check", "src/hooks/caveman-activate.js"])
    run(["node", "--check", "src/hooks/caveman-mode-tracker.js"])
    run(["node", "--check", "src/hooks/cavecrew-model-overrides.js"])
    run(["node", "--check", "bin/install.js"])
    run(["node", "--check", "bin/lib/settings.js"])
    bash = shutil.which("bash")
    if bash is not None:
        run([bash, "-n", "src/hooks/install.sh"])
        run([bash, "-n", "src/hooks/uninstall.sh"])
        run([bash, "-n", "src/hooks/caveman-statusline.sh"])
    else:
        print("SKIP: Bash syntax checks require Bash; PowerShell static checks still run")

    # Ensure install/uninstall scripts include caveman-config.js
    install_sh = (ROOT / "src/hooks/install.sh").read_text(encoding="utf-8")
    uninstall_sh = (ROOT / "src/hooks/uninstall.sh").read_text(encoding="utf-8")
    ensure("caveman-config.js" in install_sh, "install.sh missing caveman-config.js")
    ensure("caveman-config.js" in uninstall_sh, "uninstall.sh missing caveman-config.js")

    print("JSON manifests and available script syntax OK")


def verify_package_contents() -> None:
    section("Package Contents")
    npm = shutil.which("npm")
    ensure(npm is not None, "npm missing — cannot audit launch tarball")
    with tempfile.TemporaryDirectory(prefix="caveman-pack-audit-") as tmp:
        result = run(
            [npm, "pack", "--dry-run", "--json", "--ignore-scripts"],
            env={"npm_config_cache": str(Path(tmp) / "npm-cache")},
        )
    payload = json.loads(result.stdout)
    ensure(isinstance(payload, list) and len(payload) == 1, "unexpected npm pack manifest")
    files = {entry["path"] for entry in payload[0]["files"]}
    required = {
        "bin/install.js",
        "agents/cavecrew-investigator.md",
        "agents/cavecrew-builder.md",
        "agents/cavecrew-reviewer.md",
        "skills/caveman-compress/scripts/compress.py",
        "src/hooks/caveman-parse.js",
        "src/hooks/caveman-statusline.sh",
        "dist/caveman.skill",
    }
    ensure(required <= files, f"launch tarball missing required files: {sorted(required - files)}")
    leaked = sorted(
        path for path in files
        if "__pycache__" in Path(path).parts or Path(path).suffix in {".pyc", ".pyo", ".pyd"}
    )
    ensure(not leaked, f"launch tarball contains Python cache artifacts: {leaked}")
    print(f"Launch tarball contains {len(files)} files with no Python cache artifacts")


def verify_powershell_static() -> None:
    section("PowerShell Static Checks")
    install_text = (ROOT / "src/hooks/install.ps1").read_text(encoding="utf-8")
    uninstall_text = (ROOT / "src/hooks/uninstall.ps1").read_text(encoding="utf-8")
    statusline_text = (ROOT / "src/hooks/caveman-statusline.ps1").read_text(encoding="utf-8")

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
    sys.path.insert(0, str(ROOT / "skills/caveman-compress"))
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
        [sys.executable, "-m", "scripts", "../../src/hooks/install.sh"],
        cwd=ROOT / "skills/caveman-compress",
        check=False,
    )
    ensure(skip_result.returncode == 0, "compress CLI skip path should exit 0")
    ensure("Detected: code" in skip_result.stdout, "compress CLI skip path missing detection output")
    ensure(
        "Skipping: file is not natural language" in skip_result.stdout,
        "compress CLI skip path missing skip output",
    )

    missing_result = run(
        [sys.executable, "-m", "scripts", "../../does-not-exist.md"],
        cwd=ROOT / "skills/caveman-compress",
        check=False,
    )
    ensure(missing_result.returncode == 1, "compress CLI missing-file path should exit 1")
    ensure("File not found" in missing_result.stdout, "compress CLI missing-file output mismatch")

    print("Compress CLI skip/error paths OK")


def verify_hook_install_flow() -> None:
    section("Claude Hook Flow")

    ensure(shutil.which("node") is not None, "node is required for hook verification")
    bash = shutil.which("bash")
    if bash is None:
        print("SKIP: Bash hook install flow requires Bash; native PowerShell path covered statically")
        return

    with tempfile.TemporaryDirectory(prefix="caveman-verify-") as temp_root:
        temp_root_path = Path(temp_root)
        home = temp_root_path / "home"
        claude_dir = home / ".claude"
        claude_dir.mkdir(parents=True)

        existing_settings = {
            "statusLine": {"type": "command", "command": "bash /tmp/existing-statusline.sh"},
            "hooks": {"Notification": [{"hooks": [{"type": "command", "command": "echo keep-me"}]}]},
        }
        (claude_dir / "settings.json").write_text(json.dumps(existing_settings, indent=2) + "\n", encoding="utf-8")
        hook_env = {"HOME": shell_path(home), "CLAUDE_CONFIG_DIR": shell_path(claude_dir)}

        run([bash, "src/hooks/install.sh"], env=hook_env)

        settings = read_json(claude_dir / "settings.json")
        hooks = settings["hooks"]
        ensure(settings["statusLine"]["command"] == "bash /tmp/existing-statusline.sh", "install.sh clobbered existing statusLine")
        ensure("SessionStart" in hooks, "SessionStart hook missing after install")
        ensure("UserPromptSubmit" in hooks, "UserPromptSubmit hook missing after install")

        activate = run(
            ["node", "src/hooks/caveman-activate.js"],
            env=hook_env,
        )
        ensure("CAVEMAN MODE ACTIVE" in activate.stdout, "activation output missing caveman banner")
        ensure("STATUSLINE SETUP NEEDED" not in activate.stdout, "activation should stay quiet when custom statusline exists")
        ensure((claude_dir / ".caveman-active").read_text(encoding="utf-8") == "full", "activation flag should default to full")

        # Test configurable default mode via CAVEMAN_DEFAULT_MODE env var
        activate_custom = run(
            ["node", "src/hooks/caveman-activate.js"],
            env={**hook_env, "CAVEMAN_DEFAULT_MODE": "ultra"},
        )
        ensure("CAVEMAN MODE ACTIVE" in activate_custom.stdout, "activation with custom default missing banner")
        ensure(
            (claude_dir / ".caveman-active").read_text(encoding="utf-8") == "ultra",
            "CAVEMAN_DEFAULT_MODE=ultra should set flag to ultra",
        )
        # Test "off" mode — activation skipped, flag removed
        activate_off = run(
            ["node", "src/hooks/caveman-activate.js"],
            env={**hook_env, "CAVEMAN_DEFAULT_MODE": "off"},
        )
        ensure("CAVEMAN MODE ACTIVE" not in activate_off.stdout, "off mode should not emit caveman banner")
        ensure(not (claude_dir / ".caveman-active").exists(), "off mode should remove flag file")

        # Test mode tracker with /caveman when default is off — should NOT write flag
        subprocess.run(
            ["node", "src/hooks/caveman-mode-tracker.js"],
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
        (claude_dir / ".caveman-active").write_text("full", encoding="utf-8")

        run(
            ["node", "src/hooks/caveman-mode-tracker.js"],
            env=hook_env,
            check=True,
        )

        ultra_prompt = subprocess.run(
            ["node", "src/hooks/caveman-mode-tracker.js"],
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
            ["node", "src/hooks/caveman-mode-tracker.js"],
            cwd=ROOT,
            env={**os.environ, **hook_env},
            text=True,
            encoding="utf-8",
            input='{"prompt":"normal mode"}',
            capture_output=True,
            check=True,
        )
        ensure(not (claude_dir / ".caveman-active").exists(), "normal mode should remove flag file")

        (claude_dir / ".caveman-active").write_text("wenyan-ultra", encoding="utf-8")
        statusline = run(
            [bash, "src/hooks/caveman-statusline.sh"],
            env=hook_env,
        )
        ensure("[CAVEMAN:WENYAN-ULTRA]" in statusline.stdout, "statusline badge output mismatch")

        reinstall = run([bash, "src/hooks/install.sh"], env=hook_env)
        ensure("Nothing to do" in reinstall.stdout, "install.sh should be idempotent")

        run([bash, "src/hooks/uninstall.sh"], env=hook_env)
        settings_after = read_json(claude_dir / "settings.json")
        ensure(settings_after == existing_settings, "uninstall.sh did not restore non-caveman settings")
        ensure(not (claude_dir / ".caveman-active").exists(), "uninstall.sh should remove flag file")

    with tempfile.TemporaryDirectory(prefix="caveman-verify-fresh-") as temp_root:
        home = Path(temp_root) / "home"
        claude_dir = home / ".claude"
        hook_env = {"HOME": shell_path(home), "CLAUDE_CONFIG_DIR": shell_path(claude_dir)}
        run([bash, "src/hooks/install.sh"], env=hook_env)
        settings = read_json(claude_dir / "settings.json")
        ensure("statusLine" in settings, "fresh install should configure statusline")
        activate = run(["node", "src/hooks/caveman-activate.js"], env=hook_env)
        ensure("STATUSLINE SETUP NEEDED" not in activate.stdout, "fresh install should not nudge for statusline")
        run([bash, "src/hooks/uninstall.sh"], env=hook_env)
        ensure(read_json(claude_dir / "settings.json") == {}, "fresh uninstall should leave empty settings")

    # The settings.json backup must be written ONCE. Without the guard, a
    # --force reinstall copies the already-merged file over the only
    # pre-caveman copy, so the user's recovery path silently becomes a
    # caveman-flavoured settings.json.
    with tempfile.TemporaryDirectory(prefix="caveman-verify-bak-") as temp_root:
        home = Path(temp_root) / "home"
        claude_dir = home / ".claude"
        claude_dir.mkdir(parents=True)
        hook_env = {"HOME": shell_path(home), "CLAUDE_CONFIG_DIR": shell_path(claude_dir)}
        pristine = {"theme": "dark", "myImportantSetting": True}
        (claude_dir / "settings.json").write_text(
            json.dumps(pristine, indent=2) + "\n", encoding="utf-8"
        )
        for _ in range(3):
            run([bash, "src/hooks/install.sh", "--force"], env=hook_env)
        ensure(
            read_json(claude_dir / "settings.json.bak") == pristine,
            "install.sh --force must not overwrite the pre-caveman settings.json.bak",
        )

    # #593: uninstall must own ONLY its own scripts. A user hook that merely
    # mentions "caveman" in a path, and a foreign handler sharing a matcher
    # group with ours, both have to survive.
    with tempfile.TemporaryDirectory(prefix="caveman-verify-foreign-") as temp_root:
        home = Path(temp_root) / "home"
        claude_dir = home / ".claude"
        claude_dir.mkdir(parents=True)
        hook_env = {"HOME": shell_path(home), "CLAUDE_CONFIG_DIR": shell_path(claude_dir)}
        run([bash, "src/hooks/install.sh"], env=hook_env)

        settings = read_json(claude_dir / "settings.json")
        foreign = {"type": "command", "command": 'bash "/home/me/caveman-notes-sync.sh"'}
        settings["hooks"]["SessionStart"].append({"hooks": [dict(foreign)]})
        # Foreign handler sharing OUR entry's matcher group.
        settings["hooks"]["UserPromptSubmit"][0]["hooks"].append(dict(foreign))
        (claude_dir / "settings.json").write_text(
            json.dumps(settings, indent=2) + "\n", encoding="utf-8"
        )

        run([bash, "src/hooks/uninstall.sh"], env=hook_env)
        after = read_json(claude_dir / "settings.json")
        remaining = [
            h
            for entries in after.get("hooks", {}).values()
            for entry in entries
            for h in entry.get("hooks", [])
        ]
        ensure(
            remaining == [foreign, foreign],
            f"uninstall.sh must preserve foreign hooks mentioning 'caveman'; got {remaining}",
        )

    print("Claude hook install/uninstall flow OK")


def verify_license_boundaries() -> None:
    section("License Boundaries")

    bsl_text = (ROOT / "LICENSE.BSL").read_text(encoding="utf-8")
    bsl_directories = (
        "engine",
        "proxy",
        "cacheengine",
        "rewriter",
        "browse",
        "mcp",
        "shrink",
        "mem",
        "shared/platform",
    )
    licensing = (ROOT / "LICENSING.md").read_text(encoding="utf-8")
    for relative in bsl_directories:
        license_path = ROOT / relative / "LICENSE"
        ensure(license_path.exists(), f"BSL directory missing LICENSE: {relative}")
        ensure(
            license_path.read_text(encoding="utf-8") == bsl_text,
            f"BSL directory license differs from LICENSE.BSL: {relative}",
        )
        ensure(f"`{relative}/`" in licensing, f"LICENSING.md omits BSL directory: {relative}")

    package = read_json(ROOT / "package.json")
    ensure(isinstance(package, dict) and package.get("license") == "MIT", "root installer must remain MIT")
    readme = (ROOT / "README.md").read_text(encoding="utf-8")
    ensure("New Engine-linked runtime modules default to BSL-1.1" in readme, "README missing new-runtime BSL rule")
    ensure("not OSI Open Source before Change Date" in readme, "README missing BSL source-available boundary")

    print(f"{len(bsl_directories)} BSL directories carry canonical license; MIT installer boundary preserved")


def main() -> int:
    checks = [
        verify_license_boundaries,
        verify_shipped_skills_are_documented,
        verify_skill_frontmatter_upload_compatibility,
        verify_synced_files,
        verify_manifests_and_syntax,
        verify_package_contents,
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
