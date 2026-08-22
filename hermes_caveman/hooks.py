"""Caveman mode lifecycle hooks and deterministic slash commands."""
from __future__ import annotations

import os
from pathlib import Path
import re
import threading
from typing import Any, Optional

from . import state
from .config import CANONICAL_MODES, normalize_mode, resolve_default_mode


_ACTIVE_MODES = frozenset(mode for mode in CANONICAL_MODES if mode != "off")
_MODE_PATTERN = "|".join(sorted((re.escape(mode) for mode in _ACTIVE_MODES), key=len, reverse=True))
_MODE_TABLE_ROW = re.compile(rf"^\|\s*\*\*({_MODE_PATTERN})\*\*\s*\|", re.IGNORECASE)
_MODE_EXAMPLE_ROW = re.compile(rf"^\s*-\s*({_MODE_PATTERN})\s*:", re.IGNORECASE)
_QUESTION = re.compile(
    r"^(?:what|whats|what's|how|why|when|where|who|does|do|did|is|are|"
    r"can|could|would|should|tell\s+me|explain)\b",
    re.IGNORECASE,
)
_ACTIVATE = re.compile(
    r"\b(?:activate|enable|start|turn\s+on|use|switch\s+to|want|give\s+me)\b"
    r"[^.?!]{0,40}\bcaveman\b",
    re.IGNORECASE,
)
_TALK_LIKE = re.compile(r"\btalk\s+like\b[^.?!]{0,40}\bcaveman\b", re.IGNORECASE)
_CAVEMAN_ON = re.compile(r"\bcaveman\s+mode\s+(?:on|please|now)\b", re.IGNORECASE)
_BARE_CAVEMAN = re.compile(r"^caveman(?:\s+mode)?\s*[.!]*$", re.IGNORECASE)
_BREVITY = re.compile(
    r"\b(?:less\s+tokens|fewer\s+tokens|be\s+brief|be\s+terse|shorter\s+answers)\b"
    r"(?!\s+(?:in|for|on|about|when|during|with)\b)",
    re.IGNORECASE,
)
_MODE_LOCK = threading.RLock()
_SKILL_PATH = Path(__file__).resolve().parents[1] / "skills" / "caveman" / "SKILL.md"

_FALLBACK_DESCRIPTIONS = {
    "lite": "No filler or hedging. Keep articles and full sentences.",
    "full": "Drop articles and filler; fragments are acceptable.",
    "ultra": "State each fact once; remove safe conjunctions; keep exact terms.",
    "wenyan-lite": "Use concise semi-classical Chinese while retaining grammar.",
    "wenyan-full": "Use terse literary Chinese with full technical accuracy.",
    "wenyan-ultra": "Use maximum literary-Chinese compression without ambiguity.",
}


def _strip_literal_regions(message: str) -> str:
    """Remove code and quoted examples before interpreting natural language."""
    text = re.sub(r"```.*?(?:```|\Z)", " ", message, flags=re.DOTALL)
    text = re.sub(r"~~~.*?(?:~~~|\Z)", " ", text, flags=re.DOTALL)
    text = re.sub(r"`[^`]*(?:`|\Z)", " ", text, flags=re.DOTALL)
    text = re.sub(r'"(?:\\.|[^"\\])*"', " ", text)
    text = re.sub(r"“[^”]*”", " ", text)
    text = re.sub(r"‘[^’]*’", " ", text)
    text = re.sub(r"'[^'\n]*'", " ", text)
    text = re.sub(r"(?m)^\s*>.*$", " ", text)
    return text


def _normalized_prose(message: object) -> str:
    if not isinstance(message, str):
        return ""
    return " ".join(_strip_literal_regions(message).strip().lower().split())


def _wants_deactivation(prompt: str) -> bool:
    if "vim" in prompt and "normal mode" in prompt and "caveman" not in prompt:
        return False
    return bool(
        re.search(
            r"\b(?:stop|disable|deactivate|quit|exit|kill)\s+(?:the\s+)?caveman\b",
            prompt,
        )
        or re.search(r"\bcaveman(?:\s+mode)?\s+(?:off|stop|disabled?)\b", prompt)
        or re.search(r"\bturn\s+off\s+(?:the\s+)?caveman\b", prompt)
        or re.search(
            r"^(?:please\s+)?(?:(?:go\s+)|(?:back\s+to\s+)|"
            r"(?:switch\s+(?:back\s+)?to\s+)|(?:return\s+to\s+))?"
            r"normal\s+mode\b",
            prompt,
        )
        or ("normal mode" in prompt and "caveman" in prompt)
    )


def detect_mode_switch(message: object, cwd: os.PathLike = None) -> Optional[str]:
    """Detect an unquoted, unscoped natural-language mode switch."""
    prompt = _normalized_prose(message)
    if not prompt or _QUESTION.match(prompt):
        return None
    if _wants_deactivation(prompt):
        return "off"
    if not (
        _ACTIVATE.search(prompt)
        or _TALK_LIKE.search(prompt)
        or _CAVEMAN_ON.search(prompt)
        or _BARE_CAVEMAN.match(prompt)
        or _BREVITY.search(prompt)
    ):
        return None
    mode = resolve_default_mode(cwd if cwd is not None else Path.cwd())
    return mode if mode in _ACTIVE_MODES else None


def _strip_frontmatter(text: str) -> str:
    lines = text.splitlines()
    if not lines or lines[0].strip() != "---":
        return text.strip()
    for index in range(1, len(lines)):
        if lines[index].strip() == "---":
            return "\n".join(lines[index + 1 :]).strip()
    return ""


def _fallback_reinforcement(mode: str) -> str:
    description = _FALLBACK_DESCRIPTIONS.get(mode, _FALLBACK_DESCRIPTIONS["full"])
    return "\n".join(
        (
            f"CAVEMAN MODE ACTIVE — level: {mode}",
            "",
            "Respond tersely while preserving all technical substance.",
            "Code, commands, exact errors, security warnings, and irreversible-action confirmations stay normal and explicit.",
            "",
            "## Intensity",
            "",
            "| Level | What change |",
            "|-------|------------|",
            f"| **{mode}** | {description} |",
            "",
            "## Auto-Clarity",
            "",
            "Use normal prose whenever compression could make meaning, ordering, security, or irreversible effects ambiguous. Resume terse prose afterward.",
        )
    )


def render_reinforcement(mode: str) -> str:
    """Render the canonical skill body with only the active mode's rows."""
    canonical = normalize_mode(mode)
    if canonical not in _ACTIVE_MODES:
        return ""
    try:
        source = _SKILL_PATH.read_text(encoding="utf-8")
        if len(source) > 131072:
            raise ValueError("skill file exceeds safe reinforcement limit")
        body = _strip_frontmatter(source)
        if not body:
            raise ValueError("skill body missing")
        filtered = []
        for line in body.splitlines():
            table_match = _MODE_TABLE_ROW.match(line)
            if table_match and table_match.group(1).lower() != canonical:
                continue
            example_match = _MODE_EXAMPLE_ROW.match(line)
            if example_match and example_match.group(1).lower() != canonical:
                continue
            filtered.append(line)
        rendered = "\n".join(filtered).strip()
        if "## Auto-Clarity" not in rendered or f"| **{canonical}** |" not in rendered:
            raise ValueError("canonical skill lacks required sections")
        return f"CAVEMAN MODE ACTIVE — level: {canonical}\n\n{rendered}"
    except (OSError, UnicodeError, ValueError):
        return _fallback_reinforcement(canonical)


def _start_stats(session_id: object, kwargs: dict) -> None:
    if not session_id:
        return
    try:
        from . import stats

        stats.start_session(str(session_id), db=kwargs.get("db"))
    except Exception:
        pass


def _mark_turn_stats(session_id: object, mode: Optional[str]) -> None:
    if not session_id:
        return
    try:
        from . import stats

        stats.mark_turn_mode(str(session_id), mode)
    except Exception:
        pass


def on_session_start(
    session_id: object = None,
    cwd: os.PathLike = None,
    **kwargs: Any,
) -> None:
    """Initialize the global mode once; an existing ``off`` remains durable."""
    try:
        with _MODE_LOCK:
            current = state.read_mode()
            if current is None:
                mode = resolve_default_mode(cwd if cwd is not None else Path.cwd())
                state.write_mode(mode)
    except Exception:
        pass
    _start_stats(session_id, kwargs)


def pre_llm_call(
    session_id: object = None,
    user_message: object = "",
    cwd: os.PathLike = None,
    **kwargs: Any,
):
    """Apply a switch and inject full, per-turn reinforcement while active."""
    try:
        switch = detect_mode_switch(
            user_message,
            cwd=cwd if cwd is not None else kwargs.get("working_directory"),
        )
        with _MODE_LOCK:
            if switch is not None:
                state.write_mode(switch)
            current = state.read_mode()
        mode = normalize_mode(current)
        active = mode if mode in _ACTIVE_MODES else None
        _mark_turn_stats(session_id, active)
        if active is None:
            return None
        return {"context": render_reinforcement(active)}
    except Exception:
        return None


def post_llm_call(**kwargs: Any):
    """Delegate usage reconciliation when the optional stats runtime exists."""
    try:
        from . import stats

        return stats.post_llm_call(**kwargs)
    except Exception:
        return None


def caveman_command(raw_args: str = "", **kwargs: Any) -> str:
    """Set Caveman mode deterministically; unknown input never changes state."""
    argument = raw_args.strip().lower() if isinstance(raw_args, str) else ""
    off_aliases = {
        "off",
        "stop",
        "disable",
        "disabled",
        "deactivate",
        "normal",
        "normal-mode",
        "normal mode",
    }
    default_aliases = {"", "on", "start", "enable", "default"}

    if argument in off_aliases:
        mode = "off"
    elif argument in default_aliases:
        try:
            mode = resolve_default_mode(kwargs.get("cwd") or Path.cwd())
        except Exception:
            return "Caveman mode unavailable"
    else:
        mode = normalize_mode(argument)

    if mode is None:
        return (
            f"Unknown mode: {raw_args.strip()}. Valid modes: off, lite, full, ultra, "
            "wenyan-lite, wenyan-full, wenyan-ultra (alias: wenyan)."
        )
    try:
        with _MODE_LOCK:
            state.write_mode(mode)
    except Exception:
        return "Caveman mode unavailable"
    return f"Caveman mode: {mode}"


def caveman_init_command(raw_args: str = "", **kwargs: Any) -> str:
    """Redirect to the validated project-only CLI without performing writes."""
    return (
        "Project initialization is CLI-only. Run: hermes caveman init "
        "[--dry-run] [--force] [--only cursor|windsurf|cline|copilot|opencode|agents]"
    )
