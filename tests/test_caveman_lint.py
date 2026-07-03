"""Tests for src/tools/caveman-lint.py — waste reporter for AI prose."""

import importlib.util
import json
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parent.parent
LINT = REPO_ROOT / "src" / "tools" / "caveman-lint.py"

spec = importlib.util.spec_from_file_location("caveman_lint", LINT)
lint_mod = importlib.util.module_from_spec(spec)
spec.loader.exec_module(lint_mod)

VERBOSE = """# Answer

Sure! I'd be happy to help you with that. The issue you're experiencing is
likely caused by a token expiry bug. It's important to note that this only
affects the auth middleware. Basically, the check is just off by one.

In conclusion, I hope this helps! Let me know if you have any questions.

# Answer

The reason is a token expiry bug in the auth middleware of your application code.
The reason is a token expiry bug in the auth middleware of your application code.
"""

CAVEMAN = """# Answer

Bug in auth middleware. Token expiry check use `<` not `<=`. Fix:

```js
if (expiresAt <= now) reject();
```
"""

CODE_WITH_TRAP = """# Doc

```text
Sure! I'd be happy to help you with that. In conclusion, basically just really.
```

Short prose line.
"""


class LintUnitTests(unittest.TestCase):
    def test_verbose_text_flags_expected_categories(self):
        r = lint_mod.lint_text(VERBOSE)
        cats = r["categories"]
        for expected in ("greeting", "hedging", "boilerplate", "filler-word",
                         "duplicate-heading", "duplicate-sentence"):
            self.assertIn(expected, cats, f"missing category {expected}")
        self.assertGreater(r["wasted_tokens"], 0)
        self.assertGreater(r["wasted_pct"], 0)

    def test_caveman_text_is_clean(self):
        r = lint_mod.lint_text(CAVEMAN)
        self.assertEqual(r["categories"], {})
        self.assertEqual(r["wasted_tokens"], 0)

    def test_code_blocks_are_never_flagged(self):
        r = lint_mod.lint_text(CODE_WITH_TRAP)
        self.assertEqual(r["categories"], {}, "waste inside fences must not count")

    def test_oversized_code_block_is_advisory_not_waste(self):
        big = "# Doc\n\n```\n" + "\n".join(f"line {i}" for i in range(100)) + "\n```\n"
        r = lint_mod.lint_text(big, max_code_lines=80)
        self.assertEqual(len(r["oversized_code_blocks"]), 1)
        self.assertEqual(r["wasted_tokens"], 0, "advisory blocks excluded from waste")

    def test_short_repeated_sentences_not_flagged(self):
        r = lint_mod.lint_text("Yes. Yes. Yes. Fine. Fine.")
        self.assertNotIn("duplicate-sentence", r["categories"])


class LintCliTests(unittest.TestCase):
    def test_json_output_parses_and_reports(self):
        with tempfile.TemporaryDirectory() as tmp:
            p = Path(tmp) / "answer.md"
            p.write_text(VERBOSE, encoding="utf-8")
            r = subprocess.run([sys.executable, str(LINT), str(p), "--json"],
                               capture_output=True, text=True)
            self.assertEqual(r.returncode, 0, r.stderr)
            data = json.loads(r.stdout)
            self.assertIn(str(p), data)
            self.assertGreater(data[str(p)]["wasted_tokens"], 0)

    def test_missing_file_exits_2(self):
        r = subprocess.run([sys.executable, str(LINT), "/nonexistent/x.md"],
                           capture_output=True, text=True)
        self.assertEqual(r.returncode, 2)


if __name__ == "__main__":
    unittest.main()
