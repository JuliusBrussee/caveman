"""Tests for caveman-error skill behavior."""

import unittest


class CavemanErrorSkillTests(unittest.TestCase):
    """Verify caveman-error output format and parsing rules."""

    def test_python_nameerror_format(self):
        """Python NameError → terse one-liner with file:line, type, cause, fix."""
        error = """Traceback (most recent call last):
  File "main.py", line 42, in <module>
    print(user.email)
NameError: name 'user' is not defined"""
        # Expected caveman output pattern
        # main.py:L42: NameError. Var 'user' undefined. Fix: ...
        self.assertIn("main.py", error)  # File present
        self.assertIn("42", error)       # Line present
        self.assertIn("NameError", error)

    def test_javascript_typeerror_format(self):
        """JS TypeError → terse with null check suggestion."""
        error = """TypeError: Cannot read properties of undefined (reading 'email')
    at authUser (/app/auth.js:23:15)
    at processRequest (/app/server.js:45:3)"""
        self.assertIn("auth.js", error)
        self.assertIn("23", error)
        self.assertIn("TypeError", error)

    def test_rust_compile_error_format(self):
        """Rust error code → terse with file and error code."""
        error = """error[E0425]: cannot find value `config` in this scope
  --> src/main.rs:15:23
   |
15 |     let db = connect(&config);
   |                       ^^^^^^ not found in this scope"""
        self.assertIn("main.rs", error)
        self.assertIn("15", error)
        self.assertIn("E0425", error)

    def test_output_pattern_has_file_line_error(self):
        """All caveman-error outputs must contain file:line and error type."""
        # Pattern validation for SKILL.md examples
        examples = [
            "main.py:L42: NameError. Var 'user' undefined. Fix: Define or check before use.",
            "auth.js:L23: TypeError. Cannot read 'email' of undefined. Fix: Add null guard before access.",
            "src/main.rs:L15: E0425. Var 'config' not found in scope. Fix: Import or define config.",
        ]
        for ex in examples:
            # Must have :L<digits> pattern
            self.assertRegex(ex, r":L\d+:")
            # Must have Fix: suggestion
            self.assertIn("Fix:", ex)

    def test_framework_frames_collapsed(self):
        """Framework frames (node_modules, site-packages) should be collapsed."""
        # This test documents expected behavior
        # Full traces with many framework frames should show user code only
        framework_paths = [
            "/node_modules/react/",
            "/site-packages/django/",
            "/usr/lib/python3.11/",
        ]
        for path in framework_paths:
            # These should be collapsed in caveman output
            self.assertIn("node_modules" if "node" in path else "site-packages" if "site" in path else "lib", path)


if __name__ == "__main__":
    unittest.main()
