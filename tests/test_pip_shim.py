"""Tests for the pip shim (python/caveman_agent) in dev mode.

Dev mode = running from a git checkout, where cli.find_payload() falls back
to the repo root instead of the bundled _payload/. The wheel path is covered
by the same code (only the payload directory differs), and building a wheel
needs network for build isolation, so CI exercises dev mode only.

Run: pytest tests/test_pip_shim.py
"""

import os
import subprocess
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
ENV = {**os.environ, "PYTHONPATH": str(ROOT / "python")}


def run_shim(*args):
    return subprocess.run(
        [sys.executable, "-m", "caveman_agent", *args],
        capture_output=True, text=True, env=ENV, cwd=ROOT, timeout=120,
    )


def test_version_prints_and_exits_zero():
    r = run_shim("--version")
    assert r.returncode == 0
    assert "caveman-agent" in r.stdout


def test_list_forwards_to_node_installer():
    r = run_shim("list")
    assert r.returncode == 0
    assert "caveman provider matrix" in r.stdout


def test_flags_forward_verbatim():
    # --help reaches bin/install.js and comes back with its usage text,
    # including flags added after the shim was written (no flag list to sync).
    r = run_shim("--help")
    assert r.returncode == 0
    assert "--with-autoallow" in r.stdout
    assert "--with-rtk" in r.stdout


def test_unknown_subcommand_rejected():
    r = run_shim("frobnicate")
    assert r.returncode == 2
    assert "unknown subcommand" in r.stderr


def test_uninstall_maps_to_flag_dry():
    # `caveman uninstall` must reach the installer's uninstall path. Use an
    # empty temp config dir + --dry-run so nothing on the machine is touched.
    import tempfile

    with tempfile.TemporaryDirectory() as tmp:
        r = subprocess.run(
            [sys.executable, "-m", "caveman_agent", "uninstall",
             "--dry-run", "--config-dir", tmp],
            capture_output=True, text=True, env=ENV, cwd=ROOT, timeout=120,
        )
    assert r.returncode == 0
    assert "caveman uninstall" in r.stdout


def test_find_payload_dev_mode():
    sys.path.insert(0, str(ROOT / "python"))
    try:
        from caveman_agent.cli import find_payload
        payload = find_payload()
        assert payload is not None
        assert (payload / "bin" / "install.js").is_file()
        assert (payload / "src" / "hooks").is_dir()
    finally:
        sys.path.pop(0)


def test_build_argv_mapping():
    sys.path.insert(0, str(ROOT / "python"))
    try:
        from caveman_agent.cli import build_argv
        assert build_argv(["install", "--all"]) == ["--all"]
        assert build_argv(["uninstall"]) == ["--uninstall"]
        assert build_argv(["list"]) == ["--list"]
        assert build_argv(["--dry-run"]) == ["--dry-run"]
        assert build_argv([]) == []
    finally:
        sys.path.pop(0)
