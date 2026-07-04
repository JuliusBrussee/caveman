"""Auto-bootstrap: makes `pip install caveman-agent` a complete install.

Loaded at interpreter startup via caveman_agent_bootstrap.pth (the same
site-packages mechanism editable installs use). On the FIRST Python run after
pip install, spawns `node <payload>/bin/install.js --non-interactive` detached
in the background — so adding caveman-agent to requirements.txt is the whole
story: the next time anything Python runs, caveman wires itself.

Because this executes on every interpreter startup, the contract is strict:

  * NEVER raise, NEVER block, NEVER write to stdout/stderr. Installer output
    goes to $CLAUDE_CONFIG_DIR/caveman-bootstrap.log.
  * Happy path (already bootstrapped) is one os.stat.
  * At most once per package version: a marker file is claimed atomically
    (O_CREAT|O_EXCL) before anything runs. A version bump re-bootstraps once
    (the installer is idempotent, so a lost race double-run is harmless).
  * Opt-outs: CAVEMAN_NO_AUTO_INSTALL=1 disables entirely; CI environments
    (CI env var) are skipped automatically — build machines shouldn't grow
    agent config as a side effect of installing dependencies.
  * Extra flags via CAVEMAN_AUTO_INSTALL_ARGS (shlex-split), e.g.
    CAVEMAN_AUTO_INSTALL_ARGS="--with-autoallow=dev".
  * `caveman uninstall` and `pip uninstall caveman-agent` both undo their
    halves (marker+log removal is wired into the Node uninstaller; the .pth
    is in the wheel RECORD so pip removes it).
"""

from __future__ import annotations

import os


def _config_dir() -> str:
    return os.environ.get("CLAUDE_CONFIG_DIR") or os.path.join(
        os.path.expanduser("~"), ".claude"
    )


MARKER_NAME = ".caveman-pip-bootstrap"
LOG_NAME = "caveman-bootstrap.log"


def _claim_marker(marker: str, version: str) -> bool:
    """Atomically claim the right to bootstrap. True = we run, False = skip."""
    try:
        fd = os.open(marker, os.O_CREAT | os.O_EXCL | os.O_WRONLY, 0o600)
        try:
            os.write(fd, version.encode())
        finally:
            os.close(fd)
        return True
    except FileExistsError:
        pass
    except OSError:
        return False
    # Marker exists — re-claim only on a version change (upgrade). The rewrite
    # is not race-free; a lost race means two parallel runs of an idempotent
    # installer, which is acceptable.
    try:
        with open(marker, "r", encoding="utf-8", errors="replace") as f:
            if f.read().strip() == version:
                return False
        with open(marker, "w", encoding="utf-8") as f:
            f.write(version)
        return True
    except OSError:
        return False


def _spawn(node: str, installer: str, cfg: str) -> None:
    import shlex
    import subprocess

    args = ["--non-interactive"]
    extra = os.environ.get("CAVEMAN_AUTO_INSTALL_ARGS")
    if extra:
        args += shlex.split(extra)

    log = open(os.path.join(cfg, LOG_NAME), "ab")
    kwargs = {
        "stdin": subprocess.DEVNULL,
        "stdout": log,
        "stderr": log,
        "cwd": os.path.expanduser("~"),  # never write per-repo files from here
    }
    if os.name == "nt":
        kwargs["creationflags"] = 0x00000008 | 0x00000200  # DETACHED | NEW_GROUP
    else:
        kwargs["start_new_session"] = True
    try:
        subprocess.Popen([node, installer, *args], **kwargs)
    finally:
        log.close()


def _run() -> None:
    if os.environ.get("CAVEMAN_NO_AUTO_INSTALL"):
        return
    if os.environ.get("CI"):
        return

    cfg = _config_dir()
    marker = os.path.join(cfg, MARKER_NAME)

    from caveman_agent import __version__

    # Fast path: already bootstrapped at this version.
    if os.path.exists(marker):
        try:
            with open(marker, "r", encoding="utf-8", errors="replace") as f:
                if f.read().strip() == __version__:
                    return
        except OSError:
            return

    # Only now pay for the heavier imports and PATH probing.
    from caveman_agent.cli import find_node, find_payload

    node = find_node()
    payload = find_payload()
    if node is None or payload is None:
        return  # no Node yet — retry on a future startup (marker not claimed)

    try:
        os.makedirs(cfg, exist_ok=True)
    except OSError:
        return
    if not _claim_marker(marker, __version__):
        return
    _spawn(node, str(payload / "bin" / "install.js"), cfg)


# .pth import context: any exception here would break EVERY python startup
# in the environment. Swallow everything.
try:  # pragma: no cover - trivial guard
    _run()
except Exception:
    pass
