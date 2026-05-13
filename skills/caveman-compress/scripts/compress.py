#!/usr/bin/env python3
"""
Caveman Memory Compression Orchestrator

Usage:
    python scripts/compress.py <filepath>
"""

import os
import re
import shlex
import shutil
import subprocess
import sys
from pathlib import Path
from typing import Dict, List

OUTER_FENCE_REGEX = re.compile(
    r"\A\s*(`{3,}|~{3,})[^\n]*\n(.*)\n\1\s*\Z", re.DOTALL
)

# Filenames and paths that almost certainly hold secrets or PII. Compressing
# them ships raw bytes to the Claude service (via `claude -p`) — a third-party
# data boundary that developers on sensitive codebases cannot cross. detect.py
# already skips .env by extension, but credentials.md / secrets.txt /
# ~/.aws/credentials would slip through the natural-language filter. This is
# a hard refuse before read.
SENSITIVE_BASENAME_REGEX = re.compile(
    r"(?ix)^("
    r"\.env(\..+)?"
    r"|\.netrc"
    r"|credentials(\..+)?"
    r"|secrets?(\..+)?"
    r"|passwords?(\..+)?"
    r"|id_(rsa|dsa|ecdsa|ed25519)(\.pub)?"
    r"|authorized_keys"
    r"|known_hosts"
    r"|.*\.(pem|key|p12|pfx|crt|cer|jks|keystore|asc|gpg)"
    r")$"
)

SENSITIVE_PATH_COMPONENTS = frozenset({".ssh", ".aws", ".gnupg", ".kube", ".docker"})

SENSITIVE_NAME_TOKENS = (
    "secret", "credential", "password", "passwd",
    "apikey", "accesskey", "token", "privatekey",
)


def is_sensitive_path(filepath: Path) -> bool:
    """Heuristic denylist for files that must never be shipped to a third-party API."""
    name = filepath.name
    if SENSITIVE_BASENAME_REGEX.match(name):
        return True
    lowered_parts = {p.lower() for p in filepath.parts}
    if lowered_parts & SENSITIVE_PATH_COMPONENTS:
        return True
    # Normalize separators so "api-key" and "api_key" both match "apikey".
    lower = re.sub(r"[_\-\s.]", "", name.lower())
    return any(tok in lower for tok in SENSITIVE_NAME_TOKENS)


def strip_llm_wrapper(text: str) -> str:
    """Strip outer ```markdown ... ``` fence when it wraps the entire output."""
    m = OUTER_FENCE_REGEX.match(text)
    if m:
        return m.group(2)
    return text

from .detect import should_compress
from .validate import validate

MAX_RETRIES = 2


# ---------- Claude Calls ----------
#
# All requests route through the local `claude -p` CLI. Same wire shape as the
# OpenClaw Claude Code Bridge (https://github.com/minz/openclaw-claude-code-bridge
# — `oc-webchat`), which is itself the documented non-interactive invocation
# pattern for Claude Code. Benefits over the previous Anthropic API path:
#
#   * Reuses the user's existing Claude Code login. No ANTHROPIC_API_KEY
#     required, no separate billing surface.
#   * Honors the user's configured model and auth — same one their editor uses.
#   * Eliminates the `anthropic` Python dependency at runtime.
#
# Mirrors the bridge's safety defaults: auto-detect binary across PATH and
# known install dirs, strip API-key env vars when in cli-login auth mode,
# strip nested `CLAUDE_CODE_*` so a child invocation cannot recurse into the
# parent's session state, disable slash-command interpretation (the prompt
# body may legitimately contain `/office-hours` etc. as literal text), and
# use `shell=False` to avoid shell metacharacter parsing.


_PRESERVED_CLAUDE_CODE_ENV = frozenset({
    "CLAUDE_CODE_OAUTH_TOKEN",
    "CLAUDE_CODE_USE_BEDROCK",
    "CLAUDE_CODE_USE_VERTEX",
    "CLAUDE_CODE_USE_FOUNDRY",
    "CLAUDE_CODE_API_KEY_HELPER_TTL_MS",
})


def detect_claude_bin(configured: str = "claude", auto_detect: bool = True) -> str:
    """Resolve the `claude` binary path.

    Search order matches the OpenClaw bridge so behavior is identical for
    users who run both: configured value → PATH → known per-OS install dirs.
    """
    expanded = os.path.expanduser(configured or "claude")
    if not auto_detect or expanded != "claude":
        return expanded

    found = shutil.which("claude")
    if found:
        return found

    home = Path.home()
    if sys.platform == "win32":
        candidates = [
            home / "AppData" / "Roaming" / "npm" / "claude.cmd",
            home / "AppData" / "Roaming" / "npm" / "claude",
        ]
    else:
        candidates = [
            home / ".local" / "bin" / "claude",
            home / ".npm-global" / "bin" / "claude",
            Path("/usr/local/bin/claude"),
            Path("/opt/homebrew/bin/claude"),
        ]
    for candidate in candidates:
        if candidate.is_file() and os.access(candidate, os.X_OK):
            return str(candidate)
    return expanded


def sanitize_env(base_env: Dict[str, str], auth_mode: str = "cli-login") -> Dict[str, str]:
    """Strip env that would steer the child off the user's CLI login.

    `cli-login` (default): drop ANTHROPIC_* credentials so the child must use
    the local Claude Code session.  `inherit`: leave them in place (advanced
    use only — e.g. running compress against a different account in CI).

    Always strips nested CLAUDE_CODE_* (except the OAuth/provider toggles)
    so a recursive invocation can't poison the child with the parent's
    session-scoped state. Same rule the bridge applies.
    """
    env = dict(base_env)

    if auth_mode == "cli-login":
        for key in (
            "ANTHROPIC_API_KEY",
            "ANTHROPIC_AUTH_TOKEN",
            "ANTHROPIC_BEDROCK_BASE_URL",
            "ANTHROPIC_VERTEX_BASE_URL",
        ):
            env.pop(key, None)

    for key in list(env.keys()):
        if key == "CLAUDECODE":
            del env[key]
        elif key.startswith("CLAUDE_CODE_") and key not in _PRESERVED_CLAUDE_CODE_ENV:
            del env[key]

    return env


def call_claude(prompt: str) -> str:
    """Send `prompt` to Claude via the local `claude -p` CLI.

    All wire-shape knobs are env-driven. No model/binary is hardcoded.

      CAVEMAN_CLAUDE_BIN          Binary name or absolute path. Default: 'claude'.
      CAVEMAN_CLAUDE_AUTO_DETECT  '0' disables PATH/install-dir auto-detect.
                                  Default: enabled.
      CAVEMAN_AUTH_MODE           'cli-login' (default) strips ANTHROPIC_* env.
                                  Set to 'inherit' to keep them (advanced).
      CAVEMAN_MODEL               Forwarded as `--model <value>`. Default: unset
                                  (CLI picks the user's configured default).
      CAVEMAN_PERMISSION_MODE     Forwarded as `--permission-mode`. Default: 'plan'.
      CAVEMAN_MAX_TURNS           Forwarded as `--max-turns <n>` only if set
                                  (flag is not present in all CLI versions).
      CAVEMAN_CLAUDE_ARGS         Extra args, shell-quoted. Default: ''.
      CAVEMAN_TIMEOUT_SEC         Subprocess timeout in seconds. Default: 900.
    """
    bin_path = detect_claude_bin(
        os.environ.get("CAVEMAN_CLAUDE_BIN", "claude"),
        os.environ.get("CAVEMAN_CLAUDE_AUTO_DETECT", "1") != "0",
    )
    auth_mode = os.environ.get("CAVEMAN_AUTH_MODE", "cli-login")
    model = os.environ.get("CAVEMAN_MODEL", "").strip()
    permission_mode = os.environ.get("CAVEMAN_PERMISSION_MODE", "plan")
    extra = shlex.split(os.environ.get("CAVEMAN_CLAUDE_ARGS", ""))

    def _int_env(name: str, default: int, lo: int = 1) -> int:
        try:
            value = int(os.environ.get(name, str(default)))
        except ValueError:
            return default
        return max(lo, value)

    timeout = _int_env("CAVEMAN_TIMEOUT_SEC", 900, lo=10)
    max_turns_raw = os.environ.get("CAVEMAN_MAX_TURNS", "").strip()

    args: List[str] = [bin_path]
    if model:
        args += ["--model", model]
    args += ["--permission-mode", permission_mode]
    if max_turns_raw:
        # --max-turns is only present on newer CLI builds; pass only if the
        # user explicitly requested a cap.
        try:
            args += ["--max-turns", str(max(1, int(max_turns_raw)))]
        except ValueError:
            pass
    # Prompt body may legitimately contain `/office-hours`, `/ship`, etc. as
    # literal slugs (e.g. a CLAUDE.md describing slash-command triggers). The
    # CLI would otherwise interpret those at the start of a turn.
    args.append("--disable-slash-commands")
    args += extra
    # Positional prompt — same shape as the OpenClaw bridge. The CLI also
    # accepts prompts via stdin, but positional avoids stdin-edge-case bugs
    # on Windows shells.
    args += ["-p", prompt]

    try:
        result = subprocess.run(
            args,
            env=sanitize_env(dict(os.environ), auth_mode),
            text=True,
            capture_output=True,
            check=True,
            timeout=timeout,
            shell=False,
        )
    except FileNotFoundError:
        raise RuntimeError(
            f"claude CLI not found (looked for '{bin_path}'). "
            "Install Claude Code from https://claude.com/claude-code or set "
            "CAVEMAN_CLAUDE_BIN to the absolute path of the binary."
        )
    except subprocess.TimeoutExpired:
        raise RuntimeError(
            f"`claude -p` timed out after {timeout}s. "
            "Raise CAVEMAN_TIMEOUT_SEC for large files."
        )
    except subprocess.CalledProcessError as e:
        raise RuntimeError(f"`claude -p` failed (exit {e.returncode}):\n{e.stderr}")

    return strip_llm_wrapper(result.stdout.strip())


def build_compress_prompt(original: str) -> str:
    return f"""
Compress this markdown into caveman format.

STRICT RULES:
- Do NOT modify anything inside ``` code blocks
- Do NOT modify anything inside inline backticks
- Preserve ALL URLs exactly
- Preserve ALL headings exactly
- Preserve file paths and commands
- Return ONLY the compressed markdown body — do NOT wrap the entire output in a ```markdown fence or any other fence. Inner code blocks from the original stay as-is; do not add a new outer fence around the whole file.

Only compress natural language.

TEXT:
{original}
"""


def build_fix_prompt(original: str, compressed: str, errors: List[str]) -> str:
    errors_str = "\n".join(f"- {e}" for e in errors)
    return f"""You are fixing a caveman-compressed markdown file. Specific validation errors were found.

CRITICAL RULES:
- DO NOT recompress or rephrase the file
- ONLY fix the listed errors — leave everything else exactly as-is
- The ORIGINAL is provided as reference only (to restore missing content)
- Preserve caveman style in all untouched sections

ERRORS TO FIX:
{errors_str}

HOW TO FIX:
- Missing URL: find it in ORIGINAL, restore it exactly where it belongs in COMPRESSED
- Code block mismatch: find the exact code block in ORIGINAL, restore it in COMPRESSED
- Heading mismatch: restore the exact heading text from ORIGINAL into COMPRESSED
- Do not touch any section not mentioned in the errors

ORIGINAL (reference only):
{original}

COMPRESSED (fix this):
{compressed}

Return ONLY the fixed compressed file. No explanation.
"""


# ---------- Core Logic ----------


def compress_file(filepath: Path) -> bool:
    # Resolve and validate path
    filepath = filepath.resolve()
    MAX_FILE_SIZE = 500_000  # 500KB
    if not filepath.exists():
        raise FileNotFoundError(f"File not found: {filepath}")
    if filepath.stat().st_size > MAX_FILE_SIZE:
        raise ValueError(f"File too large to compress safely (max 500KB): {filepath}")

    # Refuse files that look like they contain secrets or PII. Compressing ships
    # the raw bytes through `claude -p` to the Claude service — still a
    # third-party boundary — so we fail loudly rather than silently exfiltrate
    # credentials or keys. Override is intentional: the user must rename the
    # file if the heuristic is wrong.
    if is_sensitive_path(filepath):
        raise ValueError(
            f"Refusing to compress {filepath}: filename looks sensitive "
            "(credentials, keys, secrets, or known private paths). "
            "Compression sends file contents to the Claude service via `claude -p`. "
            "Rename the file if this is a false positive."
        )

    print(f"Processing: {filepath}")

    if not should_compress(filepath):
        print("Skipping (not natural language)")
        return False

    original_text = filepath.read_text(errors="ignore")
    backup_path = filepath.with_name(filepath.stem + ".original.md")

    if not original_text.strip():
        print("❌ Refusing to compress: file is empty or whitespace-only.")
        return False

    # Check if backup already exists to prevent accidental overwriting
    if backup_path.exists():
        print(f"⚠️ Backup file already exists: {backup_path}")
        print("The original backup may contain important content.")
        print("Aborting to prevent data loss. Please remove or rename the backup file if you want to proceed.")
        return False

    # Step 1: Compress
    print("Compressing with Claude...")
    compressed = call_claude(build_compress_prompt(original_text))

    if compressed is None or not compressed.strip():
        print("❌ Compression aborted: Claude returned an empty response.")
        print("   Original file is untouched (no backup created).")
        return False

    if compressed.strip() == original_text.strip():
        print("❌ Compression aborted: output is identical to input.")
        print("   Likely causes: Claude refused, returned the prompt verbatim, or the file is")
        print("   already in caveman form. Original file is untouched (no backup created).")
        return False

    # Save original as backup, then verify the backup readback before
    # touching the input file. If the filesystem dropped bytes (encoding,
    # antivirus, disk full), unlink the bad backup and abort instead of
    # leaving the user with a corrupt backup + compressed primary.
    backup_path.write_text(original_text)
    backup_readback = backup_path.read_text(errors="ignore")
    if backup_readback != original_text:
        print(f"❌ Backup write verification failed: {backup_path}")
        print("   In-memory original differs from on-disk backup. Aborting before touching the input file.")
        try:
            backup_path.unlink()
        except OSError:
            pass
        return False
    filepath.write_text(compressed)

    # Step 2: Validate + Retry
    for attempt in range(MAX_RETRIES):
        print(f"\nValidation attempt {attempt + 1}")

        result = validate(backup_path, filepath)

        if result.is_valid:
            print("Validation passed")
            break

        print("❌ Validation failed:")
        for err in result.errors:
            print(f"   - {err}")

        if attempt == MAX_RETRIES - 1:
            # Restore original on failure
            filepath.write_text(original_text)
            backup_path.unlink(missing_ok=True)
            print("❌ Failed after retries — original restored")
            return False

        print("Fixing with Claude...")
        compressed = call_claude(
            build_fix_prompt(original_text, compressed, result.errors)
        )
        filepath.write_text(compressed)

    return True
