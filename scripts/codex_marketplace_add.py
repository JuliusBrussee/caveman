#!/usr/bin/env python3
"""
Add a local Codex plugin entry to a project's `.agents/plugins/marketplace.json`.

Why: Codex Desktop local plugin discovery uses marketplace JSON files. Manually editing
them is error-prone; this script makes it idempotent and safe.
"""

from __future__ import annotations

import argparse
import json
import os
import shutil
import sys
from dataclasses import dataclass
from datetime import datetime
from pathlib import Path
from typing import Any


@dataclass(frozen=True)
class PluginSpec:
    name: str
    plugin_dir: Path  # directory that contains `.codex-plugin/plugin.json`
    category: str
    source_path: str  # value written to marketplace.json `source.path`


def _now_stamp() -> str:
    return datetime.now().strftime("%Y%m%d_%H%M%S")


def _project_default_marketplace(project_root: Path) -> Path:
    return project_root / ".agents" / "plugins" / "marketplace.json"


def _default_plugin_dir() -> Path:
    # repo_root/scripts/codex_marketplace_add.py -> repo_root/plugins/caveman
    here = Path(__file__).resolve()
    repo_root = here.parent.parent
    return repo_root / "plugins" / "caveman"


def _ensure_plugin_dir(plugin_dir: Path) -> None:
    manifest = plugin_dir / ".codex-plugin" / "plugin.json"
    if not manifest.exists():
        raise SystemExit(
            f"Plugin manifest not found: {manifest}\n"
            f"Expected `--plugin-dir` to point at a directory containing `.codex-plugin/plugin.json`."
        )


def _read_json(path: Path) -> Any:
    try:
        return json.loads(path.read_text(encoding="utf-8"))
    except FileNotFoundError:
        raise
    except Exception as exc:
        raise SystemExit(f"Failed to parse JSON: {path}\n{exc}") from exc


def _write_json(path: Path, data: Any) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(data, indent=2, ensure_ascii=False) + "\n", encoding="utf-8")


def _backup_file(path: Path) -> Path:
    backup = path.with_suffix(path.suffix + f".bak.{_now_stamp()}")
    shutil.copy2(path, backup)
    return backup


def _new_marketplace(name: str, display_name: str) -> dict[str, Any]:
    return {
        "name": name,
        "interface": {"displayName": display_name},
        "plugins": [],
    }


def _normalize_path(p: Path) -> str:
    return str(p.expanduser().resolve())


def _plugin_entry(spec: PluginSpec) -> dict[str, Any]:
    return {
        "name": spec.name,
        "source": {"source": "local", "path": spec.source_path},
        "policy": {"installation": "AVAILABLE", "authentication": "ON_INSTALL"},
        "category": spec.category,
    }


def _upsert_plugin(marketplace: dict[str, Any], spec: PluginSpec) -> tuple[bool, str]:
    plugins = marketplace.get("plugins")
    if plugins is None:
        marketplace["plugins"] = []
        plugins = marketplace["plugins"]

    if not isinstance(plugins, list):
        raise SystemExit("Invalid marketplace.json: `plugins` must be a list.")

    desired = _plugin_entry(spec)
    desired_path = desired["source"]["path"]

    # Match by name first, then by path.
    for idx, p in enumerate(plugins):
        if not isinstance(p, dict):
            continue
        if p.get("name") == spec.name:
            existing_path = (
                ((p.get("source") or {}).get("path")) if isinstance(p.get("source"), dict) else None
            )
            plugins[idx] = {**p, **desired, "source": {**(p.get("source") or {}), **desired["source"]}}
            if existing_path == desired_path:
                return (False, "already-present")
            return (True, "updated-path")

    for idx, p in enumerate(plugins):
        if not isinstance(p, dict):
            continue
        source = p.get("source")
        if isinstance(source, dict) and source.get("path") == desired_path:
            # Same path but different name: update name + required fields.
            plugins[idx] = {**p, **desired, "source": {**source, **desired["source"]}}
            return (True, "updated-name")

    plugins.append(desired)
    return (True, "added")


def _parse_args(argv: list[str]) -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        description="Add a local Codex plugin to a project's `.agents/plugins/marketplace.json`.",
    )
    parser.add_argument(
        "--project",
        type=Path,
        default=Path.cwd(),
        help="Target project root (default: current directory).",
    )
    parser.add_argument(
        "--marketplace",
        type=Path,
        default=None,
        help="Explicit marketplace.json path (default: <project>/.agents/plugins/marketplace.json).",
    )
    parser.add_argument(
        "--plugin-dir",
        type=Path,
        default=_default_plugin_dir(),
        help="Plugin directory containing `.codex-plugin/plugin.json` (default: this repo's plugins/caveman).",
    )
    parser.add_argument(
        "--path-mode",
        choices=["absolute", "relative", "vendor"],
        default="absolute",
        help="How to write `source.path` (default: absolute). Use vendor for team/CI-friendly setup.",
    )
    parser.add_argument(
        "--vendor-dest",
        type=Path,
        default=Path(".codex-plugins") / "caveman",
        help="When --path-mode=vendor, copy plugin to <project>/<vendor-dest> (default: .codex-plugins/caveman).",
    )
    parser.add_argument(
        "--force-vendor",
        action="store_true",
        help="When --path-mode=vendor and destination exists, overwrite it.",
    )
    parser.add_argument(
        "--plugin-name",
        default="caveman",
        help="Plugin name entry in marketplace.json (default: caveman).",
    )
    parser.add_argument(
        "--category",
        default="Productivity",
        help='Plugin category (default: "Productivity").',
    )
    parser.add_argument(
        "--create-name",
        default="local-plugins",
        help="If marketplace.json is missing, create it with this `name` field.",
    )
    parser.add_argument(
        "--create-display-name",
        default="Local Plugins",
        help="If marketplace.json is missing, create it with this display name.",
    )
    parser.add_argument(
        "--no-backup",
        action="store_true",
        help="Do not create a timestamped backup when editing an existing marketplace.json.",
    )
    return parser.parse_args(argv)


def _format_project_relative(project_root: Path, target: Path) -> str:
    rel = target.relative_to(project_root)
    rel_str = rel.as_posix()
    if not rel_str.startswith("."):
        rel_str = "./" + rel_str
    return rel_str


def _vendor_plugin(project_root: Path, src: Path, dest_rel: Path, force: bool) -> Path:
    dest = (project_root / dest_rel).resolve()
    if dest.exists():
        if not force:
            raise SystemExit(
                f"Vendor destination already exists: {dest}\n"
                f"Use --force-vendor to overwrite, or choose a different --vendor-dest."
            )
        shutil.rmtree(dest)
    dest.parent.mkdir(parents=True, exist_ok=True)
    shutil.copytree(src, dest)
    return dest


def main(argv: list[str]) -> int:
    args = _parse_args(argv)

    project_root: Path = args.project.expanduser().resolve()
    marketplace_path: Path = (
        args.marketplace.expanduser().resolve()
        if args.marketplace is not None
        else _project_default_marketplace(project_root)
    )
    plugin_dir: Path = args.plugin_dir.expanduser().resolve()

    _ensure_plugin_dir(plugin_dir)

    if args.path_mode == "absolute":
        source_path = _normalize_path(plugin_dir)
    elif args.path_mode == "relative":
        # Only allow relative when the plugin lives inside the project root.
        # This keeps the result portable for other machines.
        try:
            source_path = _format_project_relative(project_root, plugin_dir)
        except ValueError as exc:
            raise SystemExit(
                f"--path-mode=relative requires --plugin-dir to be inside the project root.\n"
                f"project:   {project_root}\n"
                f"plugin-dir: {plugin_dir}\n"
                f"Use --path-mode=absolute (local-only) or --path-mode=vendor (team-friendly)."
            ) from exc
    elif args.path_mode == "vendor":
        vendored_dir = _vendor_plugin(project_root, plugin_dir, args.vendor_dest, args.force_vendor)
        source_path = _format_project_relative(project_root, vendored_dir)
        plugin_dir = vendored_dir
    else:
        raise SystemExit(f"Unknown --path-mode: {args.path_mode}")

    spec = PluginSpec(
        name=args.plugin_name,
        plugin_dir=plugin_dir,
        category=args.category,
        source_path=source_path,
    )

    existed = marketplace_path.exists()
    if existed:
        marketplace = _read_json(marketplace_path)
        if not isinstance(marketplace, dict):
            raise SystemExit(f"Invalid marketplace.json root (expected object): {marketplace_path}")
    else:
        marketplace = _new_marketplace(args.create_name, args.create_display_name)

    changed, reason = _upsert_plugin(marketplace, spec)

    if not changed:
        print(f"OK: {marketplace_path} ({reason})")
        return 0

    if existed and not args.no_backup:
        backup = _backup_file(marketplace_path)
        print(f"Backup: {backup}")

    _write_json(marketplace_path, marketplace)
    print(f"Updated: {marketplace_path} ({reason})")
    print("Next: restart Codex Desktop to reload plugins.")
    if args.path_mode == "absolute":
        print("Tip: `source.path` is machine-specific in absolute mode; avoid committing this marketplace.json.")
    return 0


if __name__ == "__main__":
    raise SystemExit(main(sys.argv[1:]))
