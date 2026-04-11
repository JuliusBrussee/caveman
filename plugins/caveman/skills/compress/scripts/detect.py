"""Detect whether a file is natural language (compressible) or code/config (skip)."""

from pathlib import Path


# Extensions that are natural language and compressible
COMPRESSIBLE_EXTENSIONS = {
    ".md",
    ".txt",
}

# Extensions that should never be compressed
NON_COMPRESSIBLE_EXTENSIONS = {
    ".py",
    ".js",
    ".ts",
    ".tsx",
    ".jsx",
    ".json",
    ".yaml",
    ".yml",
    ".toml",
    ".env",
    ".lock",
    ".css",
    ".scss",
    ".html",
    ".xml",
    ".sql",
    ".sh",
    ".bash",
    ".zsh",
    ".fish",
    ".ps1",
    ".bat",
    ".cmd",
    ".ini",
    ".cfg",
    ".conf",
    ".dockerfile",
}


def detect_file_type(filepath: Path) -> str:
    """Return 'natural-language', 'code-config', or 'unknown'."""
    suffix = filepath.suffix.lower()

    if suffix in COMPRESSIBLE_EXTENSIONS:
        return "natural-language"

    if suffix in NON_COMPRESSIBLE_EXTENSIONS:
        return "code-config"

    if suffix == "":
        # Extensionless files: heuristic based on filename
        name = filepath.name.lower()
        natural_names = {
            "claude.md",
            "readme",
            "todo",
            "todos",
            "notes",
            "preferences",
            "memory",
            "context",
        }
        if name in natural_names:
            return "natural-language"

    return "unknown"


def should_compress(filepath: Path) -> bool:
    """Return True if the file is natural language and should be compressed."""
    if filepath.name.endswith(".original.md"):
        return False
    return detect_file_type(filepath) == "natural-language"


if __name__ == "__main__":
    import sys

    if len(sys.argv) < 2:
        print("Usage: python detect.py <filepath> [filepath2 ...]")
        raise SystemExit(1)

    for arg in sys.argv[1:]:
        p = Path(arg)
        file_type = detect_file_type(p)
        compress = should_compress(p)
        print(f"{p}: type={file_type} compress={compress}")
