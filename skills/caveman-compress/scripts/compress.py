#!/usr/bin/env python3
"""
Caveman Memory Compression Orchestrator

Usage:
    python scripts/compress.py <filepath>
"""

import os
import re
import shutil
import stat
import subprocess
import sys
import tempfile
from pathlib import Path
from typing import List, Optional

# Windows consoles default to cp1252, which cannot encode the emoji glyphs in
# our status lines; replace unencodable characters instead of crashing.
for _stream in (sys.stdout, sys.stderr):
    try:
        _stream.reconfigure(errors="replace")
    except Exception:
        pass

# A fence marker at the start of a line, at CommonMark's 0-3 space indent.
FENCE_LINE_REGEX = re.compile(r"^\s{0,3}(`{3,}|~{3,})")

# YAML frontmatter: starts at file start with --- on its own line, ends with --- on its own line.
# Captures the entire block (including delimiters and trailing newline) and the body after.
FRONTMATTER_REGEX = re.compile(
    r"\A(---\r?\n.*?\r?\n---\r?\n)(.*)", re.DOTALL
)


def split_frontmatter(text: str):
    """Split YAML frontmatter from body. Returns (frontmatter, body).

    Memory files (and many other markdown docs) start with a YAML frontmatter
    block delimited by `---` lines. The compression LLM has a habit of stripping
    or rewriting these despite preserve-structure rules in the prompt — so we
    surgically remove the frontmatter before compression and prepend it back
    verbatim to the output. Files without frontmatter pass through unchanged.
    """
    m = FRONTMATTER_REGEX.match(text)
    if m:
        return m.group(1), m.group(2)
    return "", text

# Filenames and paths that almost certainly hold secrets or PII. Compressing
# them ships raw bytes to the configured LLM provider — a third-party data
# boundary that developers on sensitive codebases cannot cross. detect.py
# already skips .env by extension, but credentials.md / secrets.txt / ~/.aws/credentials would
# slip through the natural-language filter. This is a hard refuse before read.
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


def backup_dir_for(filepath: Path) -> Path:
    """Resolve the out-of-tree backup directory for a given source file.

    Backups must live OUTSIDE the source directory so skill auto-loaders
    (Claude Code rules/, opencode instructions/, etc.) stop re-ingesting the
    `.original.md` copies as live files. Base dir is platform-aware:
      - Windows: %LOCALAPPDATA%\\caveman-compress\\backups
      - else:    $XDG_DATA_HOME/caveman-compress/backups if set,
                 else ~/.local/share/caveman-compress/backups

    The source file's parent-dir name is mirrored under the base to reduce
    cross-project collisions (e.g. two `task.md` files in different repos).
    """
    if os.name == "nt" or sys.platform == "win32":
        local_appdata = os.environ.get("LOCALAPPDATA")
        base = Path(local_appdata) if local_appdata else Path.home() / "AppData" / "Local"
        base = base / "caveman-compress" / "backups"
    else:
        xdg = os.environ.get("XDG_DATA_HOME")
        base = Path(xdg) if xdg else Path.home() / ".local" / "share"
        base = base / "caveman-compress" / "backups"
    return base / filepath.parent.name


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
    r"""Strip an outer ```markdown ... ``` fence when it wraps the ENTIRE output.

    The wrapper is only real when the first and last fence lines are the SAME
    block. The old regex (``\A\s*(fence)[^\n]*\n(.*)\n\1\s*\Z`` with DOTALL and
    a greedy ``.*``) never checked that: it matched any document that merely
    STARTS and ENDS with a fence line. An ordinary README section —
    ```bash npm install``` , prose, ```bash npm test``` — came back with its
    first and last fence markers deleted and its two code blocks merged into
    prose, so validation failed on both the compress and the fix path and the
    section was permanently uncompressible after three paid API calls.
    """
    lines = text.split("\n")
    first, last = 0, len(lines) - 1
    while first < len(lines) and not lines[first].strip():
        first += 1
    while last > first and not lines[last].strip():
        last -= 1
    if first >= last:
        return text
    opener = FENCE_LINE_REGEX.match(lines[first])
    closer = FENCE_LINE_REGEX.match(lines[last])
    if not opener or not closer:
        return text
    marker = opener.group(1)
    # Closing fence: same character, at least as long, and nothing else on the line.
    if closer.group(1)[0] != marker[0] or len(closer.group(1)) < len(marker):
        return text
    if lines[last].strip() != closer.group(1):
        return text
    # Any fence of the same kind in between means these two are not one block.
    for line in lines[first + 1:last]:
        inner = FENCE_LINE_REGEX.match(line)
        if inner and inner.group(1)[0] == marker[0] and len(inner.group(1)) >= len(marker):
            return text
    return "\n".join(lines[first + 1:last])


def write_text_atomic(path: Path, text: str, newline: str = "\n") -> None:
    """Write ``text`` to ``path`` atomically as UTF-8.

    Path.write_text() truncates the destination before encoding the string —
    a UnicodeEncodeError (or any other failure) partway through leaves a
    0-byte file, destroying whatever was there before (issue #655). Encode
    first, write the bytes to a sibling temp file, fsync, then os.replace()
    so the destination only ever moves from one complete, valid file to
    another. Preserves the original file's permission bits across the swap.

    ``newline`` is the line terminator to emit. Callers pass the terminator
    read_source() found in the source file so a CRLF document stays CRLF —
    text-mode writes translating LF to the platform default rewrote every
    line ending in every file the tool touched (issue #762), and reading the
    bytes ourselves means nothing translates them back.
    """
    if newline != "\n":
        # Normalise first: model output can already carry CRLF, and a bare
        # "\n" -> "\r\n" replace would turn those into "\r\r\n".
        text = text.replace("\r\n", "\n").replace("\n", newline)
    write_bytes_atomic(path, text.encode("utf-8"))


def write_bytes_atomic(path: Path, data: bytes) -> None:
    """Write ``data`` to ``path`` atomically, preserving permission bits."""
    fd, tmp_name = tempfile.mkstemp(
        dir=str(path.parent), prefix=path.name + ".", suffix=".tmp"
    )
    tmp_path = Path(tmp_name)
    try:
        with os.fdopen(fd, "wb") as f:
            f.write(data)
            f.flush()
            os.fsync(f.fileno())
        if path.exists():
            os.chmod(tmp_path, stat.S_IMODE(path.stat().st_mode))
        os.replace(tmp_path, path)
    except Exception:
        try:
            tmp_path.unlink()
        except OSError:
            pass
        raise


def read_source(filepath: Path) -> tuple[str, str, bytes]:
    """Read a source file as UTF-8, returning (text, line_terminator, raw_bytes).

    Decodes strictly. The old errors="ignore" silently DROPPED every byte that
    was not valid UTF-8 — a cp1252-authored file holding `\xe9` for "e-acute"
    lost that byte, the mangled text was what got written to the backup, the
    backup readback compared mangled-to-mangled so verification passed, and
    then the original was overwritten. The bytes were unrecoverable and
    nothing reported a problem (the destructive form of issue #686). A file we
    cannot read exactly is a file we must not rewrite.

    Line endings are detected from the raw bytes and returned to the caller
    rather than being universal-newline'd away, so write_text_atomic can put
    back what was there (issue #762). A mixed-ending file takes the terminator
    the majority of its lines use — presence of one CRLF is not a mandate to
    rewrite every LF in the document. The raw bytes come back too, so the
    backup can be a byte-for-byte copy rather than a re-rendering.
    """
    raw = filepath.read_bytes()
    try:
        text = raw.decode("utf-8")
    except UnicodeDecodeError as e:
        raise ValueError(
            f"Refusing to compress {filepath}: not valid UTF-8 "
            f"(byte 0x{raw[e.start]:02x} at offset {e.start}). "
            "Compression rewrites the file in place, and any byte this tool "
            "cannot decode would be destroyed by the round trip. "
            "Convert the file to UTF-8 first."
        ) from None
    crlf = text.count("\r\n")
    newline = "\r\n" if crlf * 2 > text.count("\n") else "\n"
    return text.replace("\r\n", "\n").replace("\r", "\n"), newline, raw


def first_nonblank_line(text: str) -> str:
    """Return the first non-blank line, stripped — used to detect a prose
    preamble smuggled in ahead of the real content (issue #588)."""
    for line in text.splitlines():
        if line.strip():
            return line.strip()
    return ""


def _write_target(filepath: Path, text: str | bytes, backup_path: Path, newline: str = "\n") -> None:
    """Write to the target file, surfacing the backup location if the write
    itself fails. write_text_atomic already leaves the target untouched on
    failure, but the caller still needs to know where the pre-compression
    original lives instead of being left to guess (issue #652).

    ``bytes`` restore the source verbatim; ``str`` is model output that still
    has to be rendered with the document's line terminator."""
    try:
        if isinstance(text, bytes):
            write_bytes_atomic(filepath, text)
        else:
            write_text_atomic(filepath, text, newline)
    except Exception:
        print(f"❌ Write to {filepath} failed. Original preserved at backup: {backup_path}")
        raise


from .detect import should_compress
from .validate import validate

MAX_RETRIES = 2
MAX_FILE_SIZE = 500_000  # 500KB

ENV_ANTHROPIC_API_KEY = "ANTHROPIC_API_KEY"
ENV_COMPRESS_PROVIDER = "CAVEMAN_COMPRESS_PROVIDER"
ENV_FALLBACK_PROVIDER = "CAVEMAN_PROVIDER"
ENV_COMPRESS_MODEL = "CAVEMAN_COMPRESS_MODEL"
ENV_FALLBACK_MODEL = "CAVEMAN_MODEL"

PROVIDER_CLAUDE = "claude"
PROVIDER_ANTHROPIC = "anthropic"
PROVIDER_OPENCODE = "opencode"
SUPPORTED_PROVIDERS = frozenset({
    PROVIDER_CLAUDE,
    PROVIDER_ANTHROPIC,
    PROVIDER_OPENCODE,
})

DEFAULT_CLAUDE_MODEL = "claude-sonnet-4-5"
CLAUDE_CLI = "claude"
CLAUDE_PRINT_ARG = "--print"
OPENCODE_CLI = "opencode"
OPENCODE_RUN_ARG = "run"
OPENCODE_FILE_ARG = "--file"
MODEL_ARG = "--model"
OPENCODE_PROMPT_PREFIX = ".caveman-compress-prompt-"
OPENCODE_PROMPT_SUFFIX = ".md"
OPENCODE_PROMPT_MESSAGE = "Follow the attached prompt exactly. Return only the final answer."


class AnthropicSdkUnavailable(RuntimeError):
    pass


# ---------- LLM Calls ----------


def configured_provider() -> str:
    provider = (
        os.environ.get(ENV_COMPRESS_PROVIDER)
        or os.environ.get(ENV_FALLBACK_PROVIDER)
        or PROVIDER_CLAUDE
    ).strip().lower()
    if provider not in SUPPORTED_PROVIDERS:
        supported = ", ".join(sorted(SUPPORTED_PROVIDERS))
        raise ValueError(
            f"Unsupported caveman-compress provider: {provider}. "
            f"Supported providers: {supported}"
        )
    return provider


def configured_model(default_model: Optional[str] = None) -> Optional[str]:
    model = (
        os.environ.get(ENV_COMPRESS_MODEL)
        or os.environ.get(ENV_FALLBACK_MODEL)
        or default_model
    )
    if model is None:
        return None
    model = model.strip()
    return model or None


def run_cli(binary_name: str, args: List[str], prompt: str) -> str:
    binary = shutil.which(binary_name) or binary_name
    command = [binary, *args]
    run_kwargs = {
        "text": True,
        "capture_output": True,
        "check": True,
        "encoding": "utf-8",
        "errors": "replace",
    }
    if binary_name == CLAUDE_CLI:
        run_kwargs["input"] = prompt
    try:
        result = subprocess.run(command, **run_kwargs)
        return strip_llm_wrapper(result.stdout.strip())
    except FileNotFoundError as e:
        raise RuntimeError(f"{binary_name} CLI not found on PATH") from e
    except subprocess.CalledProcessError as e:
        stderr = e.stderr.strip() if e.stderr else "no stderr"
        raise RuntimeError(f"{binary_name} call failed:\n{stderr}") from e


def call_anthropic_sdk(prompt: str) -> str:
    api_key = os.environ.get(ENV_ANTHROPIC_API_KEY)
    if not api_key:
        raise RuntimeError(
            f"{ENV_ANTHROPIC_API_KEY} is required for provider '{PROVIDER_ANTHROPIC}'"
        )
    try:
        import anthropic
    except ImportError as e:
        raise AnthropicSdkUnavailable(
            f"anthropic package is required for provider '{PROVIDER_ANTHROPIC}'"
        ) from e

    client = anthropic.Anthropic(api_key=api_key)
    msg = client.messages.create(
        model=configured_model(DEFAULT_CLAUDE_MODEL),
        max_tokens=8192,
        messages=[{"role": "user", "content": prompt}],
    )
    return strip_llm_wrapper(msg.content[0].text.strip())


def call_claude_cli(prompt: str) -> str:
    args = []
    model = configured_model()
    if model:
        args.extend([MODEL_ARG, model])
    args.append(CLAUDE_PRINT_ARG)
    return run_cli(CLAUDE_CLI, args, prompt)


def call_opencode_cli(prompt: str) -> str:
    args = [OPENCODE_RUN_ARG]
    model = configured_model()
    if model:
        args.extend([MODEL_ARG, model])
    prompt_path = None
    try:
        with tempfile.NamedTemporaryFile(
            "w",
            encoding="utf-8",
            prefix=OPENCODE_PROMPT_PREFIX,
            suffix=OPENCODE_PROMPT_SUFFIX,
            delete=False,
        ) as prompt_file:
            prompt_file.write(prompt)
            prompt_path = Path(prompt_file.name)
        args.extend([OPENCODE_FILE_ARG, str(prompt_path), OPENCODE_PROMPT_MESSAGE])
        return run_cli(OPENCODE_CLI, args, prompt)
    finally:
        if prompt_path is not None:
            try:
                prompt_path.unlink(missing_ok=True)
            except OSError:
                pass


def call_claude(prompt: str) -> str:
    """Send a prompt to the configured compression provider.

    Prefers the Anthropic SDK when ANTHROPIC_API_KEY is set; otherwise falls
    back to the ``claude --print`` CLI (which handles desktop auth). Set
    ``CAVEMAN_COMPRESS_PROVIDER=opencode`` and ``CAVEMAN_COMPRESS_MODEL`` (or
    ``CAVEMAN_MODEL``) to route compression through opencode instead.

    On Windows the CLI subprocess decoding defaults to the system codepage
    (cp1251 / cp1252) and crashes on UTF-8 output — see issue #152. Pinning
    ``encoding="utf-8"`` with ``errors="replace"`` matches the CLI's actual
    native I/O and prevents the UnicodeDecodeError before validation can
    report. Windows users with non-ASCII content can also set
    ``ANTHROPIC_API_KEY`` to route through the SDK and skip the subprocess.
    """
    provider = configured_provider()
    if provider == PROVIDER_OPENCODE:
        return call_opencode_cli(prompt)
    if provider == PROVIDER_ANTHROPIC:
        return call_anthropic_sdk(prompt)
    if os.environ.get(ENV_ANTHROPIC_API_KEY):
        try:
            return call_anthropic_sdk(prompt)
        except AnthropicSdkUnavailable:
            pass
    return call_claude_cli(prompt)


def build_compress_prompt(original: str) -> str:
    return f"""
Compress this markdown into caveman format.

STRICT RULES:
- Do NOT modify anything inside ``` code blocks
- Do NOT modify anything inside a 4-space-indented code block either — those are code too, and they are validated
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
    if not filepath.exists():
        raise FileNotFoundError(f"File not found: {filepath}")
    if filepath.stat().st_size > MAX_FILE_SIZE:
        raise ValueError(f"File too large to compress safely (max 500KB): {filepath}")

    # Refuse files that look like they contain secrets or PII. Compressing ships
    # the raw bytes to the configured LLM provider — a third-party boundary —
    # so we fail loudly rather than silently exfiltrate credentials or keys. Override is
    # intentional: the user must rename the file if the heuristic is wrong.
    if is_sensitive_path(filepath):
        raise ValueError(
            f"Refusing to compress {filepath}: filename looks sensitive "
            "(credentials, keys, secrets, or known private paths). "
            "Compression sends file contents to the configured LLM provider. "
            "Rename the file if this is a false positive."
        )

    print(f"Processing: {filepath}")

    if not should_compress(filepath):
        print("Skipping (not natural language)")
        return False

    original_text, newline, original_raw = read_source(filepath)
    # Store backup outside the source directory so skill auto-loaders don't
    # re-ingest the `.original.md` copy as a live file. Mirror the source's
    # parent-dir name + stem under a platform-aware base to reduce collisions.
    backup_dir = backup_dir_for(filepath)
    backup_path = backup_dir / (filepath.stem + ".original.md")

    if not original_text.strip():
        print("❌ Refusing to compress: file is empty or whitespace-only.")
        return False

    # Check if backup already exists to prevent accidental overwriting
    if backup_path.exists():
        print(f"⚠️ Backup file already exists: {backup_path}")
        print("The original backup may contain important content.")
        print("Aborting to prevent data loss. Please remove or rename the backup file if you want to proceed.")
        return False

    # Split YAML frontmatter off before compression. Claude tends to strip or
    # rewrite frontmatter despite preserve-structure rules; we keep it verbatim
    # by removing it from the input and re-prepending it to the output.
    frontmatter, body = split_frontmatter(original_text)
    if frontmatter:
        print(f"Detected YAML frontmatter ({len(frontmatter)} chars) — preserving verbatim")

    if not body.strip():
        print("❌ Refusing to compress: body is empty after frontmatter removal.")
        return False

    # Step 1: Compress (body only, frontmatter excluded)
    provider = configured_provider()
    print(f"Compressing with {provider}...")
    compressed_body = call_claude(build_compress_prompt(body))

    if compressed_body is None or not compressed_body.strip():
        print(f"❌ Compression aborted: {provider} returned an empty response.")
        print("   Original file is untouched (no backup created).")
        return False

    # Compare the BODY (not the whole file) — frontmatter is preserved verbatim
    # and would never change, so identity must be judged on the compressible part.
    if compressed_body.strip() == body.strip():
        print("❌ Compression aborted: output is identical to input.")
        print(f"   Likely causes: {provider} refused, returned the prompt verbatim, or the file is")
        print("   already in caveman form. Original file is untouched (no backup created).")
        return False

    # Reassemble: frontmatter (verbatim) + compressed body
    compressed = frontmatter + compressed_body

    # Save original as backup, then verify the backup readback before
    # touching the input file. If the filesystem dropped bytes (encoding,
    # antivirus, disk full), unlink the bad backup and abort instead of
    # leaving the user with a corrupt backup + compressed primary.
    backup_dir.mkdir(parents=True, exist_ok=True)
    write_bytes_atomic(backup_path, original_raw)
    if backup_path.read_bytes() != original_raw:
        print(f"❌ Backup write verification failed: {backup_path}")
        print("   In-memory original differs from on-disk backup. Aborting before touching the input file.")
        try:
            backup_path.unlink()
        except OSError:
            pass
        return False
    _write_target(filepath, compressed, backup_path, newline)

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
            _write_target(filepath, original_raw, backup_path, newline)
            backup_path.unlink(missing_ok=True)
            print("❌ Failed after retries — original restored")
            return False

        print(f"Fixing with {provider}...")
        compressed = call_claude(
            build_fix_prompt(original_text, compressed, result.errors)
        )

        if compressed is None or not compressed.strip():
            print("❌ Fix attempt aborted: Claude returned an empty response.")
            print("   Skipping this attempt.")
            continue

        # Guard against a prose preamble smuggled in ahead of the real fixed
        # content (issue #588). Only enforced when the original starts with a
        # structural anchor (frontmatter `---` or a heading) — plain-prose
        # first lines get legitimately rewritten by compression, and requiring
        # them verbatim would reject every valid fix.
        anchor = first_nonblank_line(original_text)
        if anchor.startswith(("---", "#")) and first_nonblank_line(compressed) != anchor:
            print("❌ Fix attempt aborted: output does not start with the original's first line.")
            print("   Possible preamble leak. Skipping this attempt.")
            continue

        _write_target(filepath, compressed, backup_path, newline)

    return True
