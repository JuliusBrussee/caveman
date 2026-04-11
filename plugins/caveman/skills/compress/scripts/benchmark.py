"""Benchmark token savings from markdown compression.

Usage:
    python benchmark.py original.md compressed.md
"""

import sys
from pathlib import Path

import tiktoken


def count_tokens(text: str, model: str = "gpt-4") -> int:
    """Count tokens using OpenAI tokenizer."""
    try:
        encoding = tiktoken.encoding_for_model(model)
    except KeyError:
        encoding = tiktoken.get_encoding("cl100k_base")
    return len(encoding.encode(text))


def benchmark_pair(original_path: Path, compressed_path: Path):
    """Compare original vs compressed token counts."""
    original = original_path.read_text()
    compressed = compressed_path.read_text()

    orig_tokens = count_tokens(original)
    comp_tokens = count_tokens(compressed)
    saved = orig_tokens - comp_tokens
    reduction = (saved / orig_tokens * 100) if orig_tokens > 0 else 0

    print(f"Original:   {orig_tokens:6d} tokens")
    print(f"Compressed: {comp_tokens:6d} tokens")
    print(f"Saved:      {saved:6d} tokens")
    print(f"Reduction:  {reduction:6.1f}%")


def main():
    if len(sys.argv) == 3:
        # Direct file pair: python3 benchmark.py original.md compressed.md
        original = Path(sys.argv[1])
        compressed = Path(sys.argv[2])
        benchmark_pair(original, compressed)
        return

    if len(sys.argv) != 1:
        print("Usage: python benchmark.py [original.md compressed.md]")
        print("   or: python benchmark.py  # benchmark all test pairs")
        sys.exit(1)

    # Glob mode: repo_root/tests/caveman-compress/
    tests_dir = Path(__file__).parent.parent.parent / "tests" / "caveman-compress"
    pairs = []

    for original in tests_dir.glob("*.original.md"):
        compressed = original.parent / original.name.replace(".original.md", ".compressed.md")
        if compressed.exists():
            pairs.append((original, compressed))

    if not pairs:
        print("No compressed file pairs found.")
        return

    print(f"Found {len(pairs)} file pairs\n")
    for original, compressed in pairs:
        print(f"=== {original.stem.replace('.original', '')} ===")
        benchmark_pair(original, compressed)
        print()


if __name__ == "__main__":
    main()
