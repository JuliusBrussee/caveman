#!/usr/bin/env python3
# /// block
# name = "cli-signal-check"
# summary = "Check one stop signal reaches a CLI child, drains, and leaves no orphan."
# effects = "network"
# example = ["--cli", "packages/cli/dist/index.js", "--node", "node", "--delivery", "pid"]
#
# [returns]
# keys = ["delivery", "signals", "drained", "proxy_exited", "child_exited"]
# doc = "Signal delivery mode, count, graceful drain result and child exit result."
#
# [params]
# cli = { type = "path", required = true, help = "Built CLI entrypoint" }
# node = { type = "str", required = true, help = "Node executable" }
# delivery = { type = "str", default = "pid", help = "Signal delivery: pid or group" }
# launch = { type = "str", default = "start", help = "Launcher: start or wrap" }
# signal = { type = "str", default = "SIGINT", help = "Stop signal: SIGINT or SIGQUIT" }
# input = { type = "str", default = "tty", help = "Launcher input: tty or pipe" }
# timeout = { type = "int", default = 5, help = "Seconds allowed for each lifecycle transition" }
# ///
"""Exercise POSIX terminal descriptors and process groups with temporary state."""
import argparse
import json
import os
import pathlib
import pty
import shlex
import signal
import socket
import subprocess
import sys
import tempfile
import time

def wait_until(check, timeout, message):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        if check():
            return
        time.sleep(0.01)
    raise RuntimeError(message)

def check(args):
    cli = pathlib.Path(args.cli).resolve()
    if os.name != "posix" or not cli.is_file() or args.timeout <= 0:
        raise RuntimeError("requires POSIX, a built CLI file, and a positive timeout")
    with tempfile.TemporaryDirectory(prefix="caveman-pty-stop-") as folder:
        home = pathlib.Path(folder)
        signum = getattr(signal, args.signal)
        pid_file, stop_file, release = [home / name for name in ("proxy.pid", "signals", "release")]
        stub = home / "proxy.mjs"
        stub.write_text(
            'import { existsSync, writeFileSync, appendFileSync } from "node:fs";\n'
            + f'const stop = {json.dumps(str(stop_file))}, release = {json.dumps(str(release))};\n'
            + f'process.on({json.dumps(args.signal)}, () => appendFileSync(stop, {json.dumps(args.signal + chr(10))}));\n'
            + f'writeFileSync({json.dumps(str(pid_file))}, String(process.pid));\n'
            + 'setInterval(() => { if (existsSync(release)) process.exit(0); }, 10);\n', encoding="utf-8"
        )
        proxy = home / "proxy"
        proxy.write_text(f'#!/bin/sh\nexec {shlex.quote(args.node)} {shlex.quote(str(stub))} "$@"\n', encoding="utf-8")
        proxy.chmod(0o755)
        with socket.socket() as sock:
            sock.bind(("127.0.0.1", 0))
            port = sock.getsockname()[1]
        env = {key: value for key, value in os.environ.items() if not key.startswith(("CAVE_", "CAVEMAN_"))}
        env.update(HOME=folder, USERPROFILE=folder, XDG_CONFIG_HOME=folder,
                   CAVEMAN_HOME=folder, CAVEMAN_PROXY_BIN=str(proxy),
                   CAVEMAN_TELEMETRY="0", DO_NOT_TRACK="1", CAVE_NO_KEYCHAIN="1")
        command = [args.node, str(cli), "start", "--host", "127.0.0.1", "--port", str(port)]
        if args.launch == "wrap":
            config_dir = home / ".caveman-cloud"
            config_dir.mkdir()
            (config_dir / "config.json").write_text(json.dumps({"wrap": {
                "proxy": False, "shrink": False, "mcp": False, "browse": False,
            }}), encoding="utf-8")
            env["CAVEMAN_MCP_BIN"] = str(home / "missing-mcp")
            command = [args.node, str(cli), "wrap", str(proxy)]
        master, slave = pty.openpty()
        proc = None
        try:
            proc = subprocess.Popen(command,
                                    cwd=folder, env=env, stdin=slave if args.input == "tty" else subprocess.DEVNULL, stdout=subprocess.DEVNULL,
                                    stderr=subprocess.DEVNULL, start_new_session=True)
            wait_until(pid_file.exists, args.timeout, "proxy fixture did not start")
            if args.delivery == "group":
                os.killpg(proc.pid, signum)
            else:
                os.kill(proc.pid, signum)
            wait_until(stop_file.exists, args.timeout, f"{args.signal} did not reach {args.launch} child with {args.input} input")
            time.sleep(0.2)
            if stop_file.read_text(encoding="utf-8").splitlines() != [args.signal]:
                raise RuntimeError("one stop signal was delivered more than once")
            if proc.poll() is not None:
                raise RuntimeError("launcher abandoned the draining proxy")
            release.write_text("done", encoding="utf-8")
            expected_status = 0 if args.launch == "start" else -signum
            if proc.wait(timeout=args.timeout) != expected_status:
                raise RuntimeError("graceful drain returned a failure status")
            try:
                os.kill(int(pid_file.read_text(encoding="utf-8")), 0)
            except ProcessLookupError:
                pass
            else:
                raise RuntimeError("proxy process survived launcher completion")
            return {"delivery": args.delivery, "signals": 1, "drained": True,
                    "proxy_exited" if args.launch == "start" else "child_exited": True}
        finally:
            if pid_file.exists():
                try:
                    os.kill(int(pid_file.read_text(encoding="utf-8")), signal.SIGKILL)
                except ProcessLookupError:
                    pass
            if proc is not None:
                if proc.poll() is None:
                    proc.kill()
                proc.wait(timeout=args.timeout)
            os.close(master)
            os.close(slave)

def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--cli", required=True)
    parser.add_argument("--node", required=True)
    parser.add_argument("--delivery", choices=("pid", "group"), default="pid")
    parser.add_argument("--launch", choices=("start", "wrap"), default="start")
    parser.add_argument("--signal", choices=("SIGINT", "SIGQUIT"), default="SIGINT")
    parser.add_argument("--input", choices=("tty", "pipe"), default="tty")
    parser.add_argument("--timeout", type=int, default=5)
    try:
        print(json.dumps(check(parser.parse_args())))
    except Exception as error:
        print(json.dumps({"error": str(error)}))
        return 1
    return 0

if __name__ == "__main__":
    sys.exit(main())
