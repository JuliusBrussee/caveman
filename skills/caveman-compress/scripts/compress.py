#!/usr/bin/env python3
"""
Caveman Memory Compression Orchestrator

Usage:
    python scripts/compress.py <filepath>
"""

import contextlib
import errno
import hashlib
import os
import re
import shutil
import stat
import subprocess
import sys
import tempfile
import time
from pathlib import Path
from typing import List, Tuple

_IS_WINDOWS = os.name == "nt" or sys.platform == "win32"
_O_NOFOLLOW = getattr(os, "O_NOFOLLOW", 0)  # unix-only; refuses to open through a pre-placed symlink at the lock path

if _IS_WINDOWS:
    import msvcrt
else:
    import fcntl

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
# them ships raw bytes to the Anthropic API — a third-party data boundary that
# developers on sensitive codebases cannot cross. detect.py already skips .env
# by extension, but credentials.md / secrets.txt / ~/.aws/credentials would
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

SENSITIVE_PATH_COMPONENTS = frozenset({
    ".ssh", ".aws", ".gnupg", ".kube", ".docker",
    "credential", "credentials", "secret", "secrets",
})

SENSITIVE_NAME_TOKENS = (
    "secret", "credential", "password", "passwd",
    "apikey", "accesskey", "token", "privatekey",
)


def _state_base_dir(kind: str) -> Path:
    """Shared platform-aware base dir for caveman-compress state (backups, locks) — Windows uses %LOCALAPPDATA%, else $XDG_DATA_HOME or ~/.local/share."""
    if _IS_WINDOWS:
        local_appdata = os.environ.get("LOCALAPPDATA")
        base = Path(local_appdata) if local_appdata else Path.home() / "AppData" / "Local"
    else:
        xdg = os.environ.get("XDG_DATA_HOME")
        base = Path(xdg) if xdg else Path.home() / ".local" / "share"
    return base / "caveman-compress" / kind


def backup_dir_for(filepath: Path) -> Path:
    """Out-of-tree backup dir for filepath, keyed by its parent dir name — kept outside the source tree so skill auto-loaders don't re-ingest `.original.md` backups as live files."""
    return _state_base_dir("backups") / filepath.parent.name


LOCK_WAIT_SECONDS = 900  # must outlast a legitimate holder's worst-case run (up to MAX_RETRIES+1 Claude calls against the 500KB size cap) or a healthy wait misreads as a stuck lock
LOCK_POLL_INTERVAL = 1.0


class LockTimeoutError(TimeoutError):
    """Raised when another process holds the compress lock past LOCK_WAIT_SECONDS."""


def lock_path_for(filepath: Path) -> Path:
    """Cross-session lock path keyed on the same (parent-dir-name, stem) identity backup_dir_for uses for its own collision guard, derived from backup_dir_for itself so the two can't drift apart — two source files that would write the same backup path must also serialize on the same lock. Hashed into a fixed-length digest rather than embedded as plaintext so the key stays filesystem-safe regardless of the source path's length or characters."""
    resolved = filepath.resolve()
    backup_path = backup_dir_for(resolved) / (resolved.stem + ".original.md")
    digest = hashlib.sha256(str(backup_path).encode("utf-8")).hexdigest()[:16]
    return _state_base_dir("locks") / f"{digest}.lock"


def _try_lock_nonblocking(fd: int) -> None:
    """Attempt the OS-native exclusive lock on fd; raises BlockingIOError if another process already holds it."""
    if _IS_WINDOWS:
        try:
            msvcrt.locking(fd, msvcrt.LK_NBLCK, 1)
        except OSError as e:
            if e.errno != errno.EACCES:  # EACCES is LK_NBLCK's documented contention errno; anything else is a real failure (bad fd, permissions, AV lock) and must not be mistaken for another session holding the file
                raise
            raise BlockingIOError(str(e)) from e
    else:
        fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)


def _unlock(fd: int) -> None:
    """Release the OS-native lock on fd; swallows errors since callers use this in a finally block."""
    try:
        if _IS_WINDOWS:
            msvcrt.locking(fd, msvcrt.LK_UNLCK, 1)
        else:
            fcntl.flock(fd, fcntl.LOCK_UN)
    except OSError:
        pass


@contextlib.contextmanager
def file_lock(filepath: Path):
    """Cross-session exclusive lock on filepath's resolved path, backed by the OS's own file lock (fcntl.flock on POSIX, msvcrt.locking on Windows) — a crashed or killed holder releases it automatically, so unlike a hand-rolled marker file there's no staleness bookkeeping to get wrong."""
    lock_path = lock_path_for(filepath)
    lock_dir = lock_path.parent
    if lock_dir.is_symlink():  # best-effort: catches a pre-staged symlink at this exact component; mkdir(exist_ok=True) would otherwise follow it, and _O_NOFOLLOW below only guards the final path component
        raise OSError(f"Refusing to use lock directory through a symlink: {lock_dir}")
    lock_dir.mkdir(parents=True, exist_ok=True)
    if not _IS_WINDOWS:  # best-effort: keys are unsalted hashes of a guessable path, keep the directory listing to this user only; some CIFS/FAT/FUSE mounts reject chmod outright, so a failure here is not fatal
        with contextlib.suppress(OSError):
            os.chmod(lock_dir, 0o700)
    if lock_path.is_symlink():  # best-effort on Windows too, where O_NOFOLLOW (POSIX-only, below) can't guard this component
        raise OSError(f"Refusing to open lock file through a symlink: {lock_path}")
    fd = os.open(lock_path, os.O_CREAT | os.O_RDWR | _O_NOFOLLOW, 0o600)
    try:
        if os.fstat(fd).st_size == 0:
            os.write(fd, b"\0")  # msvcrt.locking needs at least one byte in the file to lock
        os.lseek(fd, 0, 0)
        deadline = time.monotonic() + LOCK_WAIT_SECONDS
        printed_waiting = False
        while True:
            try:
                _try_lock_nonblocking(fd)
                break
            except BlockingIOError:
                if time.monotonic() >= deadline:
                    raise LockTimeoutError(
                        f"Another caveman-compress run appears to be compressing {filepath} "
                        f"(lock: {lock_path}). Giving up after {LOCK_WAIT_SECONDS}s — retry once "
                        "it finishes."
                    ) from None
                if not printed_waiting:  # 900s of silence reads as a hang; tell the user once why nothing's happening yet. flush explicitly since stdout is a pipe when this script runs non-interactively
                    print(f"Waiting for another caveman-compress run to finish with {filepath}...", flush=True)
                    printed_waiting = True
                time.sleep(LOCK_POLL_INTERVAL)
            except OSError as e:
                if e.errno in (errno.EOPNOTSUPP, errno.ENOSYS):  # filesystem doesn't implement flock/byte-range locking at all (some NFS/SMB/FUSE mounts) — degrade to no coordination rather than fail a run that worked before this lock existed. ENOLCK deliberately excluded: it can mean transient kernel lock-record exhaustion, so it isn't a reliable "unsupported" signal — treating it as one could let a genuinely contended lock proceed unlocked
                    print(f"⚠️ {lock_dir}'s filesystem doesn't support file locking — proceeding without cross-session coordination.", flush=True)
                    break
                raise
        try:
            yield
        finally:
            _unlock(fd)
    finally:
        os.close(fd)


def is_sensitive_path(filepath: Path) -> bool:
    """Heuristic denylist for files that must never be shipped to a third-party API."""
    name = filepath.name
    if SENSITIVE_BASENAME_REGEX.match(name):
        return True
    # Normalize every component, not only basename: directories named
    # `api-keys`, `private_keys`, or singular `secret` are equally sensitive.
    normalized_parts = {
        re.sub(r"[_\-\s.]", "", part.lower()) for part in filepath.parts
    }
    if normalized_parts & SENSITIVE_PATH_COMPONENTS:
        return True
    return any(
        token in part
        for part in normalized_parts
        for token in SENSITIVE_NAME_TOKENS
    )


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


def _is_smaller_than_body(candidate_body: str, body: str) -> bool:
    """True when `candidate_body` actually compresses `body`.

    The non-expansion invariant for #776. It lives in a helper because it has
    to hold for EVERY candidate, not just the first one: a candidate that fails
    validation is sent back to Claude for repair, and the repaired text is what
    gets written if it validates. Checking only the first candidate left the
    retry path able to write a longer file and report it as a successful
    compression — the original bug, one branch over.

    Always compares bodies with frontmatter already removed. Frontmatter is
    preserved verbatim, so counting it on one side and not the other would
    measure the wrong thing.
    """
    candidate_len = len(candidate_body.strip())
    body_len = len(body.strip())
    if candidate_len >= body_len:
        print(
            "❌ Compression aborted: output is not smaller than input "
            f"({candidate_len} >= {body_len} chars)."
        )
        return False
    return True


# Bounds each individual Claude call so a stalled CLI (dropped network, an
# auth prompt with no TTY to answer it) can't hang past what LOCK_WAIT_SECONDS
# assumes for the whole run's worst case (MAX_RETRIES+1 calls).
CLAUDE_CALL_TIMEOUT_SECONDS = LOCK_WAIT_SECONDS // (MAX_RETRIES + 1)


# ---------- Claude Calls ----------


def call_claude(prompt: str) -> str:
    """Send a prompt to Claude.

    Prefers the Anthropic SDK when ANTHROPIC_API_KEY is set; otherwise falls
    back to the ``claude --print`` CLI (which handles desktop auth).

    On Windows the CLI subprocess decoding defaults to the system codepage
    (cp1251 / cp1252) and crashes on UTF-8 output — see issue #152. Pinning
    ``encoding="utf-8"`` with ``errors="replace"`` matches the CLI's actual
    native I/O and prevents the UnicodeDecodeError before validation can
    report. Windows users with non-ASCII content can also set
    ``ANTHROPIC_API_KEY`` to route through the SDK and skip the subprocess.
    """
    api_key = os.environ.get("ANTHROPIC_API_KEY")
    if api_key:
        try:
            import anthropic

            client = anthropic.Anthropic(api_key=api_key, timeout=CLAUDE_CALL_TIMEOUT_SECONDS)
            msg = client.messages.create(
                model=os.environ.get("CAVEMAN_MODEL", "claude-sonnet-4-5"),
                max_tokens=8192,
                messages=[{"role": "user", "content": prompt}],
            )
            # Tool-heavy models can put a tool_use or thinking block first; take
            # the first text block instead of trusting content[0].
            text = next((block.text for block in msg.content if getattr(block, "type", None) == "text"), "")
            return strip_llm_wrapper(text.strip())
        except ImportError:
            pass  # anthropic not installed, fall back to CLI
    # Fallback: use claude CLI (handles desktop auth).
    # Resolve binary via shutil.which so Windows .cmd/.bat shims (e.g.
    # %APPDATA%\npm\claude.CMD) work without shell=True. On POSIX,
    # shutil.which returns the same absolute path as the implicit lookup,
    # so this is a no-op there. Falls back to bare "claude" if not found
    # on PATH so subprocess raises a clear FileNotFoundError.
    claude_bin = shutil.which("claude") or "claude"
    try:
        result = subprocess.run(
            [
                claude_bin,
                "--print",
                "--setting-sources",
                "",
                "--strict-mcp-config",
            ],
            input=prompt,
            text=True,
            capture_output=True,
            check=True,
            encoding="utf-8",
            errors="replace",
            timeout=CLAUDE_CALL_TIMEOUT_SECONDS,
        )
        return strip_llm_wrapper(result.stdout.strip())
    except subprocess.CalledProcessError as e:
        raise RuntimeError(f"Claude call failed:\n{e.stderr}")
    except subprocess.TimeoutExpired:
        raise RuntimeError(
            f"Claude CLI call timed out after {CLAUDE_CALL_TIMEOUT_SECONDS}s "
            "(stalled network, or an auth prompt with no TTY to answer it)"
        )


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


CODE_MARKER_PREFIX = "@@CAVEMAN_PRESERVED_CODE_"
FENCE_OPEN_RE = re.compile(r"^[ ]{0,3}(`{3,}|~{3,})(?:[^\r\n]*)$")


def mask_code_blocks(text: str) -> Tuple[str, List[Tuple[str, str]]]:
    """Replace fenced and four-space-indented code with opaque line markers."""
    if CODE_MARKER_PREFIX in text:
        raise ValueError("Input contains reserved Caveman code-preservation marker")
    lines = text.splitlines(keepends=True)
    out: List[str] = []
    blocks: List[Tuple[str, str]] = []
    i = 0
    while i < len(lines):
        line_without_newline = lines[i].rstrip("\r\n")
        fence = FENCE_OPEN_RE.match(line_without_newline)
        indented = bool(line_without_newline) and (
            line_without_newline.startswith("    ") or line_without_newline.startswith("\t")
        )
        if not fence and not indented:
            out.append(lines[i])
            i += 1
            continue

        start = i
        if fence:
            fence_run = fence.group(1)
            close_re = re.compile(
                rf"^[ ]{{0,3}}{re.escape(fence_run[0])}{{{len(fence_run)},}}[ \t]*$"
            )
            i += 1
            while i < len(lines):
                if close_re.match(lines[i].rstrip("\r\n")):
                    i += 1
                    break
                i += 1
        else:
            i += 1
            while i < len(lines):
                candidate = lines[i].rstrip("\r\n")
                if not candidate or candidate.startswith("    ") or candidate.startswith("\t"):
                    i += 1
                    continue
                break

        block = "".join(lines[start:i])
        marker = f"{CODE_MARKER_PREFIX}{len(blocks)}_{hashlib.sha256(block.encode('utf-8')).hexdigest()[:16]}@@"
        blocks.append((marker, block))
        newline = "\r\n" if block.endswith("\r\n") else "\n" if block.endswith("\n") else ""
        out.append(marker + newline)
    return "".join(out), blocks


def restore_code_blocks(text: str, blocks: List[Tuple[str, str]]) -> str:
    """Restore markers exactly; fail closed if model removed, copied, or altered one."""
    restored = text
    for marker, block in blocks:
        if restored.count(marker) != 1:
            raise ValueError(
                f"Claude changed preserved code marker {marker}; refusing to write"
            )
        # Masking gives marker its own transport newline. Consume that wrapper
        # when present so restoring a block that already ended in newline does
        # not silently add another blank line.
        if marker + "\r\n" in restored:
            restored = restored.replace(marker + "\r\n", block, 1)
        elif marker + "\n" in restored:
            restored = restored.replace(marker + "\n", block, 1)
        else:
            restored = restored.replace(marker, block, 1)
    if CODE_MARKER_PREFIX in restored:
        raise ValueError("Claude returned an unknown Caveman code-preservation marker")
    return restored


# ---------- Core Logic ----------


def compress_file(filepath: Path) -> bool:
    # Resolve first so the lock and every check below key off the same canonical path regardless of how the caller spelled it.
    filepath = filepath.resolve()

    MAX_FILE_SIZE = 500_000  # 500KB
    # None of these three checks depends on mutual exclusion, so they run before the lock is taken — a rejected input (bad path, oversized, sensitive name) shouldn't leave a permanent lock file behind in shared state.
    if not filepath.exists():
        raise FileNotFoundError(f"File not found: {filepath}")
    if filepath.stat().st_size > MAX_FILE_SIZE:
        raise ValueError(f"File too large to compress safely (max 500KB): {filepath}")

    # Refuse files that look like they contain secrets or PII. Compressing ships
    # the raw bytes to the Anthropic API — a third-party boundary — so we fail
    # loudly rather than silently exfiltrate credentials or keys. Override is
    # intentional: the user must rename the file if the heuristic is wrong.
    if is_sensitive_path(filepath):
        raise ValueError(
            f"Refusing to compress {filepath}: filename looks sensitive "
            "(credentials, keys, secrets, or known private paths). "
            "Compression sends file contents to the Anthropic API. "
            "Rename the file if this is a false positive."
        )

    with file_lock(filepath):
        return _compress_file_locked(filepath)


def _compress_file_locked(filepath: Path) -> bool:
    """Body of compress_file; runs entirely under compress_file's file_lock for this resolved path."""
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
    print("Compressing with Claude...")
    masked_body, code_blocks = mask_code_blocks(body)
    masked_compressed = call_claude(build_compress_prompt(masked_body))
    try:
        compressed_body = restore_code_blocks(masked_compressed, code_blocks)
    except ValueError as error:
        print(f"❌ Compression aborted: {error}")
        print("   Original file is untouched (no backup created).")
        return False

    if compressed_body is None or not compressed_body.strip():
        print("❌ Compression aborted: Claude returned an empty response.")
        print("   Original file is untouched (no backup created).")
        return False

    # Compare the BODY (not the whole file) — frontmatter is preserved verbatim
    # and would never change, so identity must be judged on the compressible part.
    if compressed_body.strip() == body.strip():
        print("❌ Compression aborted: output is identical to input.")
        print("   Likely causes: Claude refused, returned the prompt verbatim, or the file is")
        print("   already in caveman form. Original file is untouched (no backup created).")
        return False

    # A rewrite that is structurally faithful but LONGER than the input passes
    # every check below (validate() only checks structural invariants, not
    # length) and would otherwise be written over the original and reported
    # as a successful compression — the opposite of what this tool exists to
    # do (issue #776). Same length is also a reject: a compression that saved
    # nothing isn't a compression.
    if not _is_smaller_than_body(compressed_body, body):
        print("   Original file is untouched (no backup created).")
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
    # Step 2: Validate + Retry. Each candidate is staged and validated next
    # to the source; the live file is written only once one passes (#544).
    staging_path = filepath.with_name(filepath.name + ".caveman-staged")
    for attempt in range(MAX_RETRIES):
        print(f"\nValidation attempt {attempt + 1}")

        _write_target(staging_path, compressed, backup_path, newline)
        result = validate(backup_path, staging_path)

        if result.is_valid:
            print("Validation passed")
            _write_target(filepath, compressed, backup_path, newline)
            staging_path.unlink(missing_ok=True)
            return True

        print("❌ Validation failed:")
        for err in result.errors:
            print(f"   - {err}")

        if attempt == MAX_RETRIES - 1:
            staging_path.unlink(missing_ok=True)
            backup_path.unlink(missing_ok=True)
            print("Failed after retries: original left untouched")
            return False

        print("Fixing with Claude...")
        fixed = call_claude(
            build_fix_prompt(original_text, compressed, result.errors)
        )

        if fixed is None or not fixed.strip():
            print("❌ Fix attempt aborted: Claude returned an empty response.")
            print("   Skipping this attempt.")
            continue

        # Guard against a prose preamble smuggled in ahead of the real fixed
        # content (issue #588). Only enforced when the original starts with a
        # structural anchor (frontmatter `---` or a heading) — plain-prose
        # first lines get legitimately rewritten by compression, and requiring
        # them verbatim would reject every valid fix.
        anchor = first_nonblank_line(original_text)
        if anchor.startswith(("---", "#")) and first_nonblank_line(fixed) != anchor:
            print("❌ Fix attempt aborted: output does not start with the original's first line.")
            print("   Possible preamble leak. Skipping this attempt.")
            continue

        # The repaired candidate is what gets written if it validates, so the
        # non-expansion invariant has to hold for it too — a repair that
        # restores the structure validate() asked for by padding the prose back
        # out is exactly the "compression" #776 is about. `fixed` is a whole
        # file (build_fix_prompt is given one, and the anchor check above
        # requires it to start with the original's first line), so its
        # frontmatter is split off to compare like against like.
        _, fixed_body = split_frontmatter(fixed)
        if not _is_smaller_than_body(fixed_body, body):
            print("   Skipping this attempt.")
            continue

        compressed = fixed

    return False
