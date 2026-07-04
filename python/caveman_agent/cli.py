"""Console entry point: locate Node, locate the bundled installer, forward.

Design constraints (mirrors install.sh / install.ps1 — CLAUDE.md forbids
re-implementing install logic outside bin/install.js):
  * This file NEVER contains install logic. It resolves paths and executes
    `node <payload>/bin/install.js <args>` — nothing else.
  * pip wheels have no post-install hooks, so installation happens on the
    first `caveman install` run, which is idempotent and safe to put in a
    Makefile / devcontainer postCreateCommand / CI bootstrap.
"""

from __future__ import annotations

import os
import shutil
import subprocess
import sys
from pathlib import Path

MIN_NODE_MAJOR = 18

# Sugar subcommands → installer flags. Anything already flag-shaped is
# forwarded verbatim, so every `bin/install.js` flag works unchanged
# (e.g. `caveman install --with-rtk --with-autoallow=dev`).
SUBCOMMANDS = {
    "install": [],
    "uninstall": ["--uninstall"],
    "list": ["--list"],
}


def find_payload() -> Path | None:
    """Locate the directory that holds bin/install.js.

    Installed wheel: caveman_agent/_payload/. Git checkout (dev mode /
    editable install): the repo root two levels above python/caveman_agent/.
    """
    here = Path(__file__).resolve().parent
    for candidate in (here / "_payload", here.parent.parent):
        if (candidate / "bin" / "install.js").is_file():
            return candidate
    return None


def find_node() -> str | None:
    return shutil.which("node")


def node_major(node: str) -> int | None:
    try:
        out = subprocess.run(
            [node, "--version"], capture_output=True, text=True, timeout=15
        ).stdout.strip()
        return int(out.lstrip("v").split(".")[0])
    except (OSError, ValueError, subprocess.TimeoutExpired):
        return None


def build_argv(args: list[str]) -> list[str]:
    """Map sugar subcommands; forward everything else untouched."""
    if args and not args[0].startswith("-"):
        head, rest = args[0], args[1:]
        if head not in SUBCOMMANDS:
            valid = ", ".join(SUBCOMMANDS)
            sys.stderr.write(f"caveman: unknown subcommand '{head}' (valid: {valid})\n")
            sys.stderr.write("         flags are forwarded to the installer — try 'caveman install --help'\n")
            raise SystemExit(2)
        return SUBCOMMANDS[head] + rest
    return args


def main(argv: list[str] | None = None) -> int:
    args = build_argv(list(sys.argv[1:] if argv is None else argv))

    if args and args[0] in ("--version", "-V"):
        from caveman_agent import __version__
        sys.stdout.write(f"caveman-agent {__version__}\n")
        return 0

    payload = find_payload()
    if payload is None:
        sys.stderr.write(
            "caveman: bundled installer payload not found — broken install?\n"
            "         reinstall with: pip install --force-reinstall caveman-agent\n"
        )
        return 1

    node = find_node()
    if node is None:
        sys.stderr.write(
            "caveman: Node.js >= 18 is required (the installer is a Node script).\n"
            "         install it from https://nodejs.org or your package manager,\n"
            "         then re-run: caveman install\n"
        )
        return 1
    major = node_major(node)
    if major is not None and major < MIN_NODE_MAJOR:
        sys.stderr.write(
            f"caveman: Node {major} found, but >= {MIN_NODE_MAJOR} is required.\n"
        )
        return 1

    installer = payload / "bin" / "install.js"
    cmd = [node, str(installer), *args]
    try:
        return subprocess.call(cmd)
    except KeyboardInterrupt:
        return 130


if __name__ == "__main__":  # pragma: no cover
    raise SystemExit(main())
