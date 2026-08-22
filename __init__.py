"""Hermes Caveman plugin package.

Importing this module only declares registration helpers. Runtime modules are
loaded when Hermes invokes a registered surface, keeping plugin discovery free
of filesystem writes, subprocesses, and network activity.
"""
from __future__ import annotations

import importlib
import inspect
import json
from pathlib import Path
from typing import Any, Callable, Dict


_ROOT = Path(__file__).resolve().parent
_SKILLS = (
    "caveman",
    "caveman-commit",
    "caveman-review",
    "caveman-help",
    "caveman-stats",
    "caveman-compress",
    "cavecrew",
)

_TOOL_DESCRIPTIONS = {
    "caveman_stats": "Report Caveman token-use statistics.",
    "caveman_prepare_compression": "Prepare a safe Caveman compression plan.",
    "caveman_apply_compression": "Apply a previously prepared Caveman compression plan.",
}

_TOOL_PARAMETERS = {
    "caveman_stats": {
        "type": "object",
        "properties": {},
        "additionalProperties": False,
    },
    "caveman_prepare_compression": {
        "type": "object",
        "properties": {
            "path": {
                "type": "string",
                "minLength": 1,
                "description": "Supported prose document to inspect.",
            },
        },
        "required": ["path"],
        "additionalProperties": False,
    },
    "caveman_apply_compression": {
        "type": "object",
        "properties": {
            "path": {
                "type": "string",
                "minLength": 1,
                "description": "Same document path passed to prepare.",
            },
            "revision": {
                "type": "string",
                "minLength": 1,
                "description": "Opaque revision token returned by prepare.",
            },
            "proposed_content": {
                "type": "string",
                "minLength": 1,
                "description": "Complete validated replacement document.",
            },
        },
        "required": ["path", "revision", "proposed_content"],
        "additionalProperties": False,
    },
}


def _lazy_tool(module_name: str, handler_name: str) -> Callable[..., str]:
    """Return a tool handler that imports its optional implementation on use."""

    def handler(args: Dict[str, Any] = None, **kwargs: Any) -> str:
        try:
            module = importlib.import_module(
                f".hermes_caveman.{module_name}", package=__package__
            )
            runtime_handler = getattr(module, handler_name)
        except (ImportError, AttributeError) as exc:
            return json.dumps(
                {
                    "success": False,
                    "error": (
                        f"{handler_name} unavailable: Caveman runtime handler "
                        f"is not installed ({exc.__class__.__name__})"
                    ),
                }
            )
        result = runtime_handler(args or {}, **kwargs)
        if isinstance(result, str):
            return result
        return json.dumps(result, ensure_ascii=False)

    handler.__name__ = handler_name
    return handler


def _on_session_start(**kwargs: Any):
    from .hermes_caveman.hooks import on_session_start

    return on_session_start(**kwargs)


def _pre_llm_call(**kwargs: Any):
    from .hermes_caveman.hooks import pre_llm_call

    return pre_llm_call(**kwargs)


def _post_llm_call(**kwargs: Any):
    from .hermes_caveman.hooks import post_llm_call

    return post_llm_call(**kwargs)


def _caveman_command(raw_args: str = "", **kwargs: Any):
    from .hermes_caveman.hooks import caveman_command

    return caveman_command(raw_args, **kwargs)


def _caveman_init_command(raw_args: str = "", **kwargs: Any):
    from .hermes_caveman.hooks import caveman_init_command

    return caveman_init_command(raw_args, **kwargs)


def _setup_caveman_cli(parser: Any) -> None:
    from .hermes_caveman.commands import setup_caveman_cli

    setup_caveman_cli(parser)


def _handle_caveman_cli(args: Any) -> int:
    from .hermes_caveman.commands import handle_caveman_cli

    return handle_caveman_cli(args)


def _register_tool(ctx: Any, name: str, handler: Callable, schema: dict) -> None:
    """Register against both current and earlier Hermes PluginContext shapes."""
    try:
        parameters = inspect.signature(ctx.register_tool).parameters
    except (TypeError, ValueError):
        parameters = {}
    kwargs = {
        "name": name,
        "handler": handler,
        "schema": schema,
        "description": _TOOL_DESCRIPTIONS[name],
    }
    if "toolset" in parameters:
        kwargs["toolset"] = "caveman"
    ctx.register_tool(**kwargs)


def _register_skill(ctx: Any, name: str, path: Path) -> None:
    """Register skills across the path-only and name-plus-path APIs."""
    try:
        parameters = inspect.signature(ctx.register_skill).parameters
    except (TypeError, ValueError):
        parameters = {}
    if "name" in parameters:
        ctx.register_skill(name=name, path=path)
    else:
        ctx.register_skill(path)


def register(ctx: Any) -> None:
    """Register Caveman tools, hooks, skills, slash commands, and CLI."""
    tools = (
        (
            "caveman_stats",
            _lazy_tool("stats", "caveman_stats"),
        ),
        (
            "caveman_prepare_compression",
            _lazy_tool("compression", "caveman_prepare_compression"),
        ),
        (
            "caveman_apply_compression",
            _lazy_tool("compression", "caveman_apply_compression"),
        ),
    )
    for name, handler in tools:
        schema = {
            "name": name,
            "description": _TOOL_DESCRIPTIONS[name],
            "parameters": _TOOL_PARAMETERS[name],
        }
        _register_tool(ctx, name, handler, schema)

    ctx.register_hook("on_session_start", _on_session_start)
    ctx.register_hook("pre_llm_call", _pre_llm_call)
    ctx.register_hook("post_llm_call", _post_llm_call)

    for name in _SKILLS:
        _register_skill(ctx, name, _ROOT / "skills" / name / "SKILL.md")

    ctx.register_command(
        "caveman",
        _caveman_command,
        description="Set or show the persistent Caveman response mode.",
        args_hint="[off|lite|full|ultra|wenyan-lite|wenyan-full|wenyan-ultra]",
    )
    ctx.register_command(
        "caveman-init",
        _caveman_init_command,
        description="Show the safe project-only Caveman initialization command.",
        args_hint="",
    )
    ctx.register_cli_command(
        name="caveman",
        help="Caveman response-mode utilities",
        setup_fn=_setup_caveman_cli,
        handler_fn=_handle_caveman_cli,
        description="Initialize project-only Caveman agent rules.",
    )
