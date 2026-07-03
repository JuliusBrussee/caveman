"""Tests for detect.py file-type classification (issue #600).

Extensionless build files (`Dockerfile`, `Makefile`) and shebang scripts
used to fall through to the content heuristic and come back as
compressible natural language — so `/caveman-compress Dockerfile` would
overwrite a Dockerfile with caveman prose. These tests pin the basename
and shebang guards.
"""

import sys
import tempfile
import unittest
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(REPO_ROOT / "skills" / "caveman-compress"))

from scripts.detect import detect_file_type, should_compress  # noqa: E402


DOCKERFILE_BODY = """FROM node:20-bookworm
WORKDIR /app
COPY package.json .
RUN npm install
CMD ["node", "index.js"]
"""

MAKEFILE_BODY = """all: build

build:
\tgo build -o bin/app ./cmd/app

clean:
\trm -rf bin
"""

SHEBANG_BODY = """#!/usr/bin/env bash
set -euo pipefail
echo "deploying"
"""

PROSE_BODY = """This project collects notes about our deployment process.

The main goal is to keep the steps simple enough that anyone on the
team can run a release without asking for help. Start by reading the
overview, then follow the checklist in order.
"""


class DetectFileTypeTests(unittest.TestCase):
    def _write(self, dirpath, name, body):
        p = Path(dirpath) / name
        p.write_text(body, encoding="utf-8")
        return p

    def test_dockerfile_is_code(self):
        with tempfile.TemporaryDirectory() as tmp:
            p = self._write(tmp, "Dockerfile", DOCKERFILE_BODY)
            self.assertEqual(detect_file_type(p), "code")
            self.assertFalse(should_compress(p))

    def test_makefile_is_code(self):
        with tempfile.TemporaryDirectory() as tmp:
            p = self._write(tmp, "Makefile", MAKEFILE_BODY)
            self.assertEqual(detect_file_type(p), "code")
            self.assertFalse(should_compress(p))

    def test_known_names_case_insensitive(self):
        with tempfile.TemporaryDirectory() as tmp:
            for name in ("dockerfile", "MAKEFILE", "Jenkinsfile", "Vagrantfile"):
                p = self._write(tmp, name, "irrelevant body\n")
                self.assertEqual(detect_file_type(p), "code", name)

    def test_cmakelists_txt_not_compressible_despite_txt_extension(self):
        with tempfile.TemporaryDirectory() as tmp:
            p = self._write(tmp, "CMakeLists.txt", "add_executable(app main.c)\n")
            self.assertEqual(detect_file_type(p), "code")
            self.assertFalse(should_compress(p))

    def test_shebang_script_is_code(self):
        with tempfile.TemporaryDirectory() as tmp:
            p = self._write(tmp, "deploy", SHEBANG_BODY)
            self.assertEqual(detect_file_type(p), "code")
            self.assertFalse(should_compress(p))

    def test_extensionless_prose_still_compressible(self):
        with tempfile.TemporaryDirectory() as tmp:
            p = self._write(tmp, "NOTES", PROSE_BODY)
            self.assertEqual(detect_file_type(p), "natural_language")
            self.assertTrue(should_compress(p))

    def test_markdown_still_compressible(self):
        with tempfile.TemporaryDirectory() as tmp:
            p = self._write(tmp, "README.md", PROSE_BODY)
            self.assertEqual(detect_file_type(p), "natural_language")
            self.assertTrue(should_compress(p))


if __name__ == "__main__":
    unittest.main()
