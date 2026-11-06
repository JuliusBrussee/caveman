#!/usr/bin/env python3
"""Detect whether a file is natural language (compressible) or code/config (skip)."""

import json
import re
from pathlib import Path

# Extensions that are natural language and compressible. `.mdc` is a Cursor
# rule: prose with YAML frontmatter, the same shape as a SKILL.md, and the
# format caveman's own `--with-init` writes to `.cursor/rules/caveman.mdc`.
# Content sniffing below runs only for EXTENSIONLESS files, so an extension
# that is on neither list falls through to `unknown` and is never compressed.
COMPRESSIBLE_EXTENSIONS = {".md", ".mdc", ".txt", ".markdown", ".rst", ".typ", ".typst", ".tex"}

# Extensions that are code/config and should be skipped
SKIP_EXTENSIONS = {
    ".py", ".js", ".ts", ".tsx", ".jsx", ".json", ".yaml", ".yml",
    ".toml", ".env", ".lock", ".css", ".scss", ".html", ".xml",
    ".sql", ".sh", ".bash", ".zsh", ".go", ".rs", ".java", ".c",
    ".cpp", ".h", ".hpp", ".rb", ".php", ".swift", ".kt", ".lua",
    ".dockerfile", ".makefile", ".csv", ".ini", ".cfg",
}

# Well-known build/config files that carry no (or a misleading) extension —
# `Dockerfile` has no suffix so `.dockerfile` above never matches it, and
# `CMakeLists.txt` would ride the compressible `.txt` rule. Checked by
# basename before any extension rule.
KNOWN_CODE_FILENAMES = {
    "dockerfile", "containerfile", "makefile", "gnumakefile", "jenkinsfile",
    "vagrantfile", "rakefile", "gemfile", "justfile", "procfile", "brewfile",
    "earthfile", "fastfile", "podfile",
    "cmakelists.txt",
}

# Dotfiles whose entire name sits where pathlib expects a suffix, so
# `Path(".cursorrules").suffix` is `""` just as it is for `.env`. These are the
# prose ones — Cursor's and its imitators' legacy single-file rule formats —
# and the only bare dotfiles that may be compressed. Everything else is
# treated as config; see the dotfile branch in `detect_file_type`.
PROSE_DOTFILES = {
    ".cursorrules", ".windsurfrules", ".clinerules", ".aiderrules",
    ".goosehints", ".augmentrules",
}

# Extensions on SKIP_EXTENSIONS that are data/config rather than program text.
CONFIG_EXTENSIONS = {".json", ".yaml", ".yml", ".toml", ".ini", ".cfg", ".env"}

# Patterns that indicate a line is code
CODE_PATTERNS = [
    re.compile(r"^\s*(import |from .+ import |require\(|const |let |var )"),
    re.compile(r"^\s*(def |class |function |async function |export )"),
    re.compile(r"^\s*(if\s*\(|for\s*\(|while\s*\(|switch\s*\(|try\s*\{)"),
    re.compile(r"^\s*[\}\]\);]+\s*$"),  # closing braces/brackets
    re.compile(r"^\s*@\w+"),  # decorators/annotations
    re.compile(r'^\s*"[^"]+"\s*:\s*'),  # JSON-like key-value
    re.compile(r"^\s*\w+\s*=\s*[{\[\(\"']"),  # assignment with literal
]


def _is_code_line(line: str) -> bool:
    """Check if a line looks like code."""
    return any(p.match(line) for p in CODE_PATTERNS)


def _is_json_content(text: str) -> bool:
    """Check if content is valid JSON."""
    try:
        json.loads(text)
        return True
    except (json.JSONDecodeError, ValueError):
        return False


def _is_yaml_content(lines: list[str]) -> bool:
    """Heuristic: check if content looks like YAML."""
    yaml_indicators = 0
    for line in lines[:30]:
        stripped = line.strip()
        if stripped.startswith("---"):
            yaml_indicators += 1
        elif re.match(r"^\w[\w\s]*:\s", stripped):
            yaml_indicators += 1
        elif stripped.startswith("- ") and ":" in stripped:
            yaml_indicators += 1
    # If most non-empty lines look like YAML
    non_empty = sum(1 for l in lines[:30] if l.strip())
    return non_empty > 0 and yaml_indicators / non_empty > 0.6


def detect_file_type(filepath: Path) -> str:
    """Classify a file as 'natural_language', 'code', 'config', or 'unknown'.

    Returns:
        One of: 'natural_language', 'code', 'config', 'unknown'
    """
    ext = filepath.suffix.lower()
    name = filepath.name.lower()

    # Known code filenames win over any extension rule
    if name in KNOWN_CODE_FILENAMES:
        return "code"

    # A bare dotfile carries its whole name where pathlib expects a suffix, so
    # `Path(".env").suffix` is `""` and every table below was skipped. The file
    # then reached the extensionless content sniffer, which only recognises
    # brace-and-quote code — so `.env`, `.gitignore` and `.editorconfig` all
    # came back `natural_language` and `/caveman-compress .gitignore` shipped
    # the bytes to the configured provider and rewrote the file in place.
    # Resolve the name against the tables, and treat anything unrecognised as
    # config rather than prose: a dotfile is an rc/config file by convention,
    # and guessing wrong is destructive in both directions at once. Guessing
    # "config" only costs a `Skipping` line on a file that is left untouched.
    if not ext and name.startswith(".") and name not in {".", ".."}:
        if name in PROSE_DOTFILES or name in COMPRESSIBLE_EXTENSIONS:
            return "natural_language"
        if name in SKIP_EXTENSIONS:
            ext = name  # fall through to the shared config/code split below
        else:
            return "config"

    # Extension-based classification
    if ext in COMPRESSIBLE_EXTENSIONS:
        return "natural_language"
    if ext in SKIP_EXTENSIONS:
        return "code" if ext not in CONFIG_EXTENSIONS else "config"

    # Extensionless files (like CLAUDE.md, TODO) — check content
    if not ext:
        try:
            text = filepath.read_text(encoding="utf-8", errors="ignore")
        except (OSError, PermissionError):
            return "unknown"

        lines = text.splitlines()[:50]

        # Shebang means executable script, never prose
        if text.startswith("#!"):
            return "code"

        if _is_json_content(text[:10000]):
            return "config"
        if _is_yaml_content(lines):
            return "config"

        code_lines = sum(1 for l in lines if l.strip() and _is_code_line(l))
        non_empty = sum(1 for l in lines if l.strip())
        if non_empty > 0 and code_lines / non_empty > 0.4:
            return "code"

        return "natural_language"

    return "unknown"


def should_compress(filepath: Path) -> bool:
    """Return True if the file is natural language and should be compressed."""
    if not filepath.is_file():
        return False
    # Skip backup files
    if filepath.name.endswith(".original.md"):
        return False
    return detect_file_type(filepath) == "natural_language"


if __name__ == "__main__":
    import sys

    if len(sys.argv) < 2:
        print("Usage: python detect.py <file1> [file2] ...")
        sys.exit(1)

    for path_str in sys.argv[1:]:
        p = Path(path_str).resolve()
        file_type = detect_file_type(p)
        compress = should_compress(p)
        print(f"  {p.name:30s} type={file_type:20s} compress={compress}")
