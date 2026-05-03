import unittest
import tempfile
from pathlib import Path
import sys

CAVEMAN_COMPRESS = Path(__file__).parent.parent / "caveman-compress"
sys.path.insert(0, str(CAVEMAN_COMPRESS))

from scripts.compress import is_sensitive_path


class TestIsSensitivePath(unittest.TestCase):
    def test_doc_extensions_false_positives(self):
        false_positive_cases = [
            "readme.md",
            "notes.txt",
            "documentation.rst",
            "changelog.md",
            "project_plan.txt",
            "installation_guide.md",
            "getting_started.txt",
            "architecture.rst",
            "contributing.md",
            "license.txt",
            "api_docs.markdown",
            "tutorial.md",
            "faq.txt",
            "roadmap.rst",
        ]
        for name in false_positive_cases:
            with tempfile.TemporaryDirectory() as tmp:
                f = Path(tmp) / name
                f.touch()
                self.assertFalse(is_sensitive_path(f), f"{name} should NOT be blocked")

    def test_true_positives_still_blocked(self):
        true_positive_cases = [
            "credentials.yaml",
            "secrets.json",
            ".env",
            ".env.local",
            "password.txt",
            "id_rsa",
            "id_ed25519.pub",
            "authorized_keys",
            "known_hosts",
            "token_refresh.md",
            "password_policy.txt",
            "secrets_management_guide.md",
            "access_token.md",
            "credential_rotation.rst",
        ]
        for name in true_positive_cases:
            with tempfile.TemporaryDirectory() as tmp:
                f = Path(tmp) / name
                f.touch()
                self.assertTrue(is_sensitive_path(f), f"{name} SHOULD be blocked")


if __name__ == "__main__":
    unittest.main()