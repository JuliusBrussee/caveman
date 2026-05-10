#!/usr/bin/env python3
from pathlib import Path
import sys

# Support both direct execution and module import
try:
    from .validate import validate
except ImportError:
    sys.path.insert(0, str(Path(__file__).parent))
    from validate import validate

try:
    import tiktoken
    _enc = tiktoken.get_encoding("o200k_base")
except ImportError:
    _enc = None


def count_tokens(text):
    if _enc is None:
        return len(text.split())  # fallback: word count
    return len(_enc.encode(text))


def benchmark_pair(orig_path: Path, comp_path: Path):
    orig_text = orig_path.read_text()
    comp_text = comp_path.read_text()

    orig_tokens = count_tokens(orig_text)
    comp_tokens = count_tokens(comp_text)
    saved = 100 * (orig_tokens - comp_tokens) / orig_tokens if orig_tokens > 0 else 0.0
    result = validate(orig_path, comp_path)

    return (comp_path.name, orig_tokens, comp_tokens, saved, result.is_valid)


def print_table(rows):
    print("\n| File | Original | Compressed | Saved % | Valid |")
    print("|------|----------|------------|---------|-------|")
    for r in rows:
        print(f"| {r[0]} | {r[1]} | {r[2]} | {r[3]:.1f}% | {'✅' if r[4] else '❌'} |")


def main():
    # Direct file pair: python3 benchmark.py original.md compressed.md
    if len(sys.argv) == 3:
        orig = Path(sys.argv[1]).resolve()
        comp = Path(sys.argv[2]).resolve()
        if not orig.exists():
            print(f"❌ Not found: {orig}")
            sys.exit(1)
        if not comp.exists():
            print(f"❌ Not found: {comp}")
            sys.exit(1)
        print_table([benchmark_pair(orig, comp)])
        return

    # Glob mode: repo_root/tests/caveman-compress/
    # __file__ lives at <repo_root>/skills/caveman-compress/scripts/benchmark.py
    # Walk up four dirs: scripts → caveman-compress → skills → repo_root.
    tests_dir = Path(__file__).resolve().parents[3] / "tests" / "caveman-compress"
    if not tests_dir.exists():
        print(f"❌ Tests dir not found: {tests_dir}")
        sys.exit(1)

    rows = []
    for orig in sorted(tests_dir.glob("*.original.md")):
        comp = orig.with_name(orig.stem.removesuffix(".original") + ".md")
        if comp.exists():
            rows.append(benchmark_pair(orig, comp))

    if not rows:
        print("No compressed file pairs found.")
        return

    print_table(rows)


if __name__ == "__main__":
    main()
