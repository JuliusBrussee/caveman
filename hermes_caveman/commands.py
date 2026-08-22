"""Project-only ``hermes caveman init`` command."""
from __future__ import annotations

import argparse
import copy
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import sys
from typing import Any


PROJECT_TARGETS = ("cursor", "windsurf", "cline", "copilot", "opencode", "agents")
_INIT_SCRIPT = Path(__file__).resolve().parents[1] / "src" / "tools" / "caveman-init.js"
_SAFE_NAME = re.compile(r"^[A-Za-z0-9_-]{1,128}$")
_MISSING = object()


def mutate_lifecycle_config(
    config: dict[str, Any],
    *,
    operation: str,
    plugin: str | None = None,
    mcp_name: str | None = None,
    mcp_entry: dict[str, Any] | None = None,
    expected_mcp: dict[str, Any] | None = None,
    restore_mcp: Any = None,
    restore_present: bool = False,
    plugin_override_present: bool = False,
    plugin_override: Any = None,
    expected_plugin_override: Any = _MISSING,
    restore_plugin_override_present: bool = False,
    restore_plugin_override: Any = None,
) -> Any:
    """Mutate only Caveman-owned config leaves and return prior MCP state."""

    if operation == "set-mcp":
        if not mcp_name or not _SAFE_NAME.fullmatch(mcp_name) or not isinstance(mcp_entry, dict):
            raise ValueError("valid MCP name and object entry required")
        servers = config.setdefault("mcp_servers", {})
        if not isinstance(servers, dict):
            raise ValueError("mcp_servers must be a mapping")
        previous = copy.deepcopy(servers.get(mcp_name)) if mcp_name in servers else None
        servers[mcp_name] = copy.deepcopy(mcp_entry)
        return previous

    if operation == "set-plugin-override":
        if plugin != "caveman":
            raise ValueError("invalid plugin override owner")
        plugins = config.setdefault("plugins", {})
        if not isinstance(plugins, dict):
            raise ValueError("plugins must be a mapping")
        entries = plugins.setdefault("entries", {})
        if not isinstance(entries, dict):
            raise ValueError("plugins.entries must be a mapping")
        entry = entries.setdefault(plugin, {})
        if not isinstance(entry, dict):
            raise ValueError("plugins.entries.caveman must be a mapping")
        if plugin_override_present:
            entry["allow_tool_override"] = copy.deepcopy(plugin_override)
        else:
            entry.pop("allow_tool_override", None)
            if not entry:
                entries.pop(plugin, None)
            if not entries:
                plugins.pop("entries", None)
        return None

    if operation != "remove":
        raise ValueError(f"unknown lifecycle operation: {operation}")

    servers = config.get("mcp_servers")
    if (
        mcp_name
        and isinstance(servers, dict)
        and mcp_name in servers
        and servers[mcp_name] != expected_mcp
    ):
        raise ValueError(f"managed MCP entry {mcp_name!r} was modified")

    if plugin:
        plugins = config.get("plugins")
        if isinstance(plugins, dict):
            for key in ("enabled", "disabled"):
                values = plugins.get(key)
                if isinstance(values, list):
                    plugins[key] = [value for value in values if value != plugin]

    removed = False
    if mcp_name:
        if not _SAFE_NAME.fullmatch(mcp_name):
            raise ValueError("invalid MCP name")
        if isinstance(servers, dict) and mcp_name in servers:
            if restore_present:
                servers[mcp_name] = copy.deepcopy(restore_mcp)
            else:
                del servers[mcp_name]
            removed = True
            if not servers:
                config.pop("mcp_servers", None)

    if expected_plugin_override is not _MISSING:
        plugins = config.get("plugins")
        entries = plugins.get("entries") if isinstance(plugins, dict) else None
        entry = entries.get(plugin) if isinstance(entries, dict) else None
        if (
            isinstance(entry, dict)
            and entry.get("allow_tool_override", _MISSING) == expected_plugin_override
        ):
            if restore_plugin_override_present:
                entry["allow_tool_override"] = copy.deepcopy(restore_plugin_override)
            else:
                entry.pop("allow_tool_override", None)
                if not entry and isinstance(entries, dict):
                    entries.pop(plugin, None)
                if not entries and isinstance(plugins, dict):
                    plugins.pop("entries", None)
    return removed


def setup_caveman_cli(parser: Any, **kwargs: Any) -> None:
    """Add the required, project-only ``init`` subcommand."""
    subcommands = parser.add_subparsers(dest="caveman_action", required=True)
    init_parser = subcommands.add_parser(
        "init",
        help="Install Caveman rules in the current project only",
        description="Install Caveman rules in the current project only.",
    )
    init_parser.add_argument(
        "--dry-run",
        action="store_true",
        help="Show changes without writing files",
    )
    init_parser.add_argument(
        "--force",
        action="store_true",
        help="Overwrite existing replace-mode rule files",
    )
    init_parser.add_argument(
        "--only",
        choices=PROJECT_TARGETS,
        help="Install exactly one project-local target",
    )

    lifecycle = subcommands.add_parser("_lifecycle", help=argparse.SUPPRESS)
    lifecycle_operations = lifecycle.add_subparsers(
        dest="lifecycle_operation", required=True
    )
    get_mcp = lifecycle_operations.add_parser("get-mcp", help=argparse.SUPPRESS)
    get_mcp.add_argument("--name", required=True)

    set_mcp = lifecycle_operations.add_parser("set-mcp", help=argparse.SUPPRESS)
    set_mcp.add_argument("--name", required=True)
    set_mcp.add_argument("--entry", required=True)

    inspect_override = lifecycle_operations.add_parser(
        "inspect-plugin-override", help=argparse.SUPPRESS
    )
    inspect_override.add_argument("--snapshot-home", required=True)

    set_override = lifecycle_operations.add_parser(
        "set-plugin-override", help=argparse.SUPPRESS
    )
    set_override.add_argument("--present", action="store_true")
    set_override.add_argument("--value")

    remove = lifecycle_operations.add_parser("remove", help=argparse.SUPPRESS)
    remove.add_argument("--plugin", default="caveman")
    remove.add_argument("--mcp-name")
    remove.add_argument("--expected-mcp")
    remove.add_argument("--restore-mcp")
    remove.add_argument("--restore-present", action="store_true")
    remove.add_argument("--expected-plugin-override")
    remove.add_argument("--restore-plugin-override")
    remove.add_argument("--restore-plugin-override-present", action="store_true")


def _decode_object(raw: str | None, label: str) -> Any:
    if raw is None:
        return None
    try:
        return json.loads(raw)
    except json.JSONDecodeError as exc:
        raise ValueError(f"invalid {label} JSON: {exc}") from exc


def _handle_lifecycle(args: Any) -> int:
    """Use Hermes' config loader/writer; never parse config.yaml in Node."""

    try:
        from hermes_cli.config import (  # type: ignore[import-not-found]
            load_config,
            read_raw_config,
            save_config,
        )

        config = load_config()
        operation = args.lifecycle_operation
        name = getattr(args, "name", None)
        if operation == "get-mcp":
            if not isinstance(name, str) or not _SAFE_NAME.fullmatch(name):
                raise ValueError("invalid MCP name")
            servers = config.get("mcp_servers")
            value = servers.get(name) if isinstance(servers, dict) else None
            print(json.dumps(value, separators=(",", ":")))
            return 0
        if operation == "set-mcp":
            entry = _decode_object(args.entry, "MCP entry")
            mutate_lifecycle_config(
                config,
                operation="set-mcp",
                mcp_name=name,
                mcp_entry=entry,
            )
            save_config(config)
            return 0
        if operation == "inspect-plugin-override":
            snapshot_home = Path(args.snapshot_home)
            if not snapshot_home.is_absolute():
                raise ValueError("snapshot home must be absolute")
            info = snapshot_home.lstat()
            if not snapshot_home.is_dir() or info.st_mode & 0o002:
                raise ValueError("snapshot home must be a private directory")
            snapshot_config = snapshot_home / "config.yaml"
            if snapshot_config.exists():
                snapshot_info = snapshot_config.lstat()
                if not snapshot_config.is_file() or snapshot_info.st_mode & 0o022:
                    raise ValueError("snapshot config must be a private regular file")
            snapshot_env = {
                key: os.environ.get(key)
                for key in ("HERMES_HOME", "HERMES_PROFILE", "HERMES_CONFIG")
            }
            try:
                os.environ["HERMES_HOME"] = str(snapshot_home)
                os.environ.pop("HERMES_PROFILE", None)
                os.environ.pop("HERMES_CONFIG", None)
                snapshot = read_raw_config()
            finally:
                for key, value in snapshot_env.items():
                    if value is None:
                        os.environ.pop(key, None)
                    else:
                        os.environ[key] = value
            plugins = snapshot.get("plugins") if isinstance(snapshot, dict) else None
            entries = plugins.get("entries") if isinstance(plugins, dict) else None
            entry = entries.get("caveman") if isinstance(entries, dict) else None
            present = isinstance(entry, dict) and "allow_tool_override" in entry
            value = copy.deepcopy(entry["allow_tool_override"]) if isinstance(entry, dict) and present else None
            print(json.dumps({"present": present, "value": value}, separators=(",", ":")))
            return 0
        if operation == "set-plugin-override":
            value = _decode_object(args.value, "plugin override")
            mutate_lifecycle_config(
                config,
                operation="set-plugin-override",
                plugin="caveman",
                plugin_override_present=bool(args.present),
                plugin_override=value,
            )
            save_config(config)
            return 0
        if operation == "remove":
            expected = _decode_object(args.expected_mcp, "expected MCP")
            restore = _decode_object(args.restore_mcp, "restore MCP")
            lifecycle_kwargs: dict[str, Any] = {}
            if args.expected_plugin_override is not None:
                lifecycle_kwargs.update(
                    expected_plugin_override=_decode_object(
                        args.expected_plugin_override, "expected plugin override"
                    ),
                    restore_plugin_override=_decode_object(
                        args.restore_plugin_override, "restore plugin override"
                    ),
                    restore_plugin_override_present=bool(
                        args.restore_plugin_override_present
                    ),
                )
            removed = mutate_lifecycle_config(
                config,
                operation="remove",
                plugin=args.plugin,
                mcp_name=args.mcp_name,
                expected_mcp=expected,
                restore_mcp=restore,
                restore_present=bool(args.restore_present),
                **lifecycle_kwargs,
            )
            save_config(config)
            print(json.dumps({"mcp_changed": bool(removed)}))
            return 0
        raise ValueError(f"unknown lifecycle operation: {operation}")
    except (ImportError, OSError, TypeError, ValueError) as exc:
        print(f"caveman lifecycle failed: {exc}", file=sys.stderr)
        return 1


def handle_caveman_cli(args: Any, **kwargs: Any) -> int:
    """Run an internal lifecycle operation or project-only initializer."""
    if getattr(args, "caveman_action", None) == "_lifecycle":
        return _handle_lifecycle(args)
    if getattr(args, "caveman_action", None) != "init":
        return 2
    if not _INIT_SCRIPT.is_file():
        print(f"caveman init unavailable: {_INIT_SCRIPT} not found", file=sys.stderr)
        return 1
    node = shutil.which("node")
    if node is None:
        print("caveman init unavailable: node executable not found", file=sys.stderr)
        return 1

    selected = getattr(args, "only", None)
    targets = (selected,) if selected else PROJECT_TARGETS
    cwd = os.getcwd()
    all_succeeded = True

    for target in targets:
        argv = [node, str(_INIT_SCRIPT), "--only", target]
        if bool(getattr(args, "dry_run", False)):
            argv.append("--dry-run")
        if bool(getattr(args, "force", False)):
            argv.append("--force")
        try:
            result = subprocess.run(
                argv,
                cwd=cwd,
                check=False,
                shell=False,
            )
        except OSError as exc:
            print(f"caveman init failed for {target}: {exc}", file=sys.stderr)
            all_succeeded = False
            continue
        if result.returncode != 0:
            all_succeeded = False

    return 0 if all_succeeded else 1
