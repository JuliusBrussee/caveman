import unittest
import tempfile
from pathlib import Path
import sys
import os

CAVEMAN_COMPRESS = Path(__file__).parent.parent / "caveman-compress"
os.chdir(CAVEMAN_COMPRESS)
sys.path.insert(0, str(CAVEMAN_COMPRESS))

from scripts.compress import is_sensitive_path


class TestIsSensitivePath(unittest.TestCase):
    def test_doc_extensions_false_positives(self):
        false_positive_cases = [
            "token_refresh.md",
            "password_policy.md",
            "secrets_management_guide.md",
            "credential_rotation.txt",
            "apikey_rotation.rst",
            "access_token.md",
            "token_refresh.markdown",
            "password_policy.txt",
            "api_secret_guide.md",
            "privatekey_documentation.rst",
            "credential_handbook.md",
            "token_usage.txt",
            "password_guidelines.markdown",
            "secret_rotation.md",
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
        ]
        for name in true_positive_cases:
            with tempfile.TemporaryDirectory() as tmp:
                f = Path(tmp) / name
                f.touch()
                self.assertTrue(is_sensitive_path(f), f"{name} SHOULD be blocked")


if __name__ == "__main__":
    unittest.main()