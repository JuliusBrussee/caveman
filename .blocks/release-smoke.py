#!/usr/bin/env python3
# /// block
# name = "release-smoke"
# summary = "Smoke-test a published CLI and its signed companions in an isolated home."
# effects = "network"
# example = ["--version", "$CLI_VERSION", "--binary-tag", "$BINARY_TAG", "--repo", "$REPO"]
#
# [params]
# version = { type = "str", required = true, help = "Exact published npm CLI version" }
# binary-tag = { type = "str", required = true, help = "Expected signed binary release tag" }
# repo = { type = "str", required = true, help = "Expected GitHub owner/repository in npm metadata" }
# tarball = { type = "str", required = false, help = "Optional release-workflow tarball for pre-publish validation" }
#
# [returns]
# keys = ["version", "binary_tag", "repo", "source", "platform", "signed_install", "wrap_record", "byte_exact_recovery", "clean_uninstall"]
# doc = "Reports release installation, record-mode wrap, recovery and isolated uninstall checks."
# ///
import argparse
import json
import os
from pathlib import Path
import platform
import re
import shutil
import socket
import subprocess
import sys
import tempfile
import time
import urllib.request


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--version", required=True)
    parser.add_argument("--binary-tag", required=True)
    parser.add_argument("--repo", required=True)
    parser.add_argument("--tarball")
    args = parser.parse_args()
    if not re.fullmatch(r"\d+\.\d+\.\d+", args.version) or not re.fullmatch(r"bin-v\d+\.\d+\.\d+", args.binary_tag):
        raise ValueError("expected stable CLI version and binary tag")
    if not re.fullmatch(r"[\w.-]+/[\w.-]+", args.repo):
        raise ValueError("invalid GitHub repository")
    source = str(Path(args.tarball).resolve()) if args.tarball else f"@caveman-ai/cli@{args.version}"
    node, npm = shutil.which("node"), shutil.which("npm")
    if not node or not npm:
        raise ValueError("node and npm must be on PATH")
    with tempfile.TemporaryDirectory(prefix="caveman-release-smoke-") as temporary:
        root = Path(temporary)
        home = root / "home"
        home.mkdir()
        cave = home / ".caveman"
        sentinel = home / "user-config.json"
        sentinel.write_bytes(b'{"preserve":true}\n')
        env = {key: value for key, value in os.environ.items()
               if key.upper() in {"PATH", "SYSTEMROOT", "WINDIR", "COMSPEC", "PATHEXT", "TMP", "TEMP", "TMPDIR"}}
        env.update(HOME=str(home), USERPROFILE=str(home), CAVEMAN_HOME=str(cave),
                   APPDATA=str(home / "AppData" / "Roaming"), LOCALAPPDATA=str(home / "AppData" / "Local"),
                   XDG_CONFIG_HOME=str(home / ".config"), CLAUDE_CONFIG_DIR=str(home / ".claude"),
                   CODEX_HOME=str(home / ".codex"), CAVEMAN_TELEMETRY="0", DO_NOT_TRACK="1",
                   CAVEMAN_PLAIN="1", NO_COLOR="1", TERM="dumb", CI="1",
                   NPM_CONFIG_USERCONFIG=str(root / "npmrc"), NPM_CONFIG_CACHE=str(root / "npm-cache"))
        (root / "npmrc").write_text("", encoding="utf-8")

        def run(command, data=None, timeout=180):
            result = subprocess.run(command, input=data, env=env, cwd=root, capture_output=True, timeout=timeout)
            if result.returncode:
                detail = result.stderr.decode("utf-8", errors="replace")[-1600:]
                raise ValueError(f"{Path(command[0]).name} exited {result.returncode}: {detail}")
            return result

        def npm_run(*options):
            command = [npm, *options]
            if os.name == "nt" and npm.lower().endswith((".cmd", ".bat")):
                command = [env.get("COMSPEC", "cmd.exe"), "/d", "/c", *command]
            return run(command)

        npm_run("install", "--prefix", str(root), "--ignore-scripts", "--no-audit", "--no-fund",
                source)
        package = root / "node_modules" / "@caveman-ai" / "cli"
        metadata = json.loads((package / "package.json").read_text(encoding="utf-8"))
        if metadata.get("repository", {}).get("url") != f"git+https://github.com/{args.repo}.git":
            raise ValueError("installed package repository differs")
        cli = [node, str(package / metadata["bin"]["caveman"])]
        version = json.loads(run([*cli, "--version"]).stdout)
        if version.get("version") != args.version or version.get("binary_release") != args.binary_tag:
            raise ValueError("installed CLI version or binary pin differs")
        run([*cli, "setup", "--install", "--json"], timeout=300)
        suffix = ".exe" if os.name == "nt" else ""
        proxy = cave / "bin" / f"caveman-proxy{suffix}"
        engine = cave / "bin" / f"caveman-engine{suffix}"
        proxy_version = json.loads(run([str(proxy), "version", "--json"]).stdout)
        if proxy_version.get("version") != args.binary_tag:
            raise ValueError(f"installed proxy version {proxy_version.get('version')!r} differs from {args.binary_tag}")
        # Pin resolution to the binaries just installed, never global companions.
        env.update(CAVEMAN_PROXY_BIN=str(proxy), CAVEMAN_ENGINE_BIN=str(engine))
        payload = json.dumps({"results": [{"id": item} for item in range(1, 65)], "error": None}).encode()
        compressed = run([*cli, "tools", "compress", "--type", "json"], payload)
        report = json.loads(compressed.stderr)
        handle = report.get("recovery_handle")
        if not handle or report.get("ratio", 0) <= 0 or compressed.stdout == payload:
            raise ValueError("compression did not produce a recoverable transform")
        if run([*cli, "tools", "retrieve", handle]).stdout != payload:
            raise ValueError("retrieval differs from original bytes")
        with socket.socket() as listener:
            listener.bind(("127.0.0.1", 0))
            port = listener.getsockname()[1]
        gateway = f"http://127.0.0.1:{port}"
        env.update(CAVEMAN_LISTEN=f"127.0.0.1:{port}", CAVEMAN_MODE="record", CAVE_GATEWAY_URL=gateway)
        with (root / "proxy.log").open(mode="wb") as log:
            process = subprocess.Popen([str(proxy), "serve"], env=env, cwd=root, stdout=log, stderr=log)
            try:
                deadline = time.monotonic() + 30
                while True:
                    try:
                        with urllib.request.urlopen(f"{gateway}/health/live", timeout=1) as response:
                            if response.status == 200:
                                break
                    except (OSError, ValueError):
                        pass
                    if process.poll() is not None or time.monotonic() >= deadline:
                        detail = (root / "proxy.log").read_text(encoding="utf-8", errors="replace")[-1000:]
                        raise ValueError(f"published proxy did not become healthy: {detail}")
                    time.sleep(0.1)
                child = ("const base=process.env.OPENAI_BASE_URL;"
                         "if(!base||base!==process.env.CAVE_GATEWAY_URL)process.exit(2);"
                         "fetch(base+'/health/live').then(r=>{if(!r.ok)process.exit(1);console.log('release-wrap-ok')})")
                wrapped = run([*cli, "wrap", "--off", node, "-e", child])
                state = json.loads(run([str(proxy), "status", "--json", "--port", str(port)]).stdout)
                if b"release-wrap-ok" not in wrapped.stdout or state.get("mode") != "record":
                    raise ValueError("wrap did not use healthy record-mode proxy")
            finally:
                process.terminate()
                try:
                    process.wait(timeout=10)
                except subprocess.TimeoutExpired:
                    process.kill()
                    process.wait(timeout=10)
        npm_run("uninstall", "--prefix", str(root), "--ignore-scripts", "--no-audit", "--no-fund", "@caveman-ai/cli")
        if package.exists() or sentinel.read_bytes() != b'{"preserve":true}\n':
            raise ValueError("uninstall left CLI or damaged unrelated config")
        print(json.dumps({"version": args.version, "binary_tag": args.binary_tag,
                          "repo": args.repo, "source": "tarball" if args.tarball else "registry",
                          "platform": f"{platform.system()}/{platform.machine()}", "signed_install": True,
                          "wrap_record": True, "byte_exact_recovery": True, "clean_uninstall": True}))


if __name__ == "__main__":
    try:
        main()
    except Exception as error:
        print(json.dumps({"error": str(error)}))
        sys.exit(1)
