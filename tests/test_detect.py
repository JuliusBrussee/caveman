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

DOTENV_BODY = """DATABASE_URL=postgres://app:secret@localhost:5432/app
API_KEY=sk-test-123
DEBUG=true
"""

GITIGNORE_BODY = """node_modules/
dist/
*.log
.DS_Store
"""

EDITORCONFIG_BODY = """root = true

[*]
indent_style = space
indent_size = 2
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
            for name in ("dockerfile", "Containerfile", "MAKEFILE", "Jenkinsfile", "Vagrantfile", "Earthfile", "Fastfile", "Podfile"):
                p = self._write(tmp, name, "irrelevant body\n")
                self.assertEqual(detect_file_type(p), "code", name)

    def test_cmakelists_txt_not_compressible_despite_txt_extension(self):
        with tempfile.TemporaryDirectory() as tmp:
            p = self._write(tmp, "CMakeLists.txt", "add_executable(app main.c)\n")
            self.assertEqual(detect_file_type(p), "code")
            self.assertFalse(should_compress(p))

    def test_dotfiles_are_never_prose(self):
        """A bare dotfile carries its whole name where pathlib expects a suffix.

        `Path(".env").suffix` is `""`, so every extension table was skipped and
        the file reached the extensionless content sniffer. That sniffer only
        recognises brace-and-quote code, so `.env`, `.gitignore` and
        `.editorconfig` all came back `natural_language` and
        `/caveman-compress .gitignore` rewrote them in place after shipping
        the bytes to the configured provider.
        """
        cases = {
            ".env": DOTENV_BODY,
            ".gitignore": GITIGNORE_BODY,
            ".editorconfig": EDITORCONFIG_BODY,
            ".dockerignore": GITIGNORE_BODY,
            ".npmrc": "//registry.npmjs.org/:_authToken=npm_secret\n",
            ".bashrc": "alias ll='ls -la'\nPS1='$ '\n",
        }
        with tempfile.TemporaryDirectory() as tmp:
            for name, body in cases.items():
                with self.subTest(name=name):
                    p = self._write(tmp, name, body)
                    self.assertNotEqual(detect_file_type(p), "natural_language", name)
                    self.assertFalse(should_compress(p), name)

    def test_unknown_dotfile_is_not_compressed(self):
        """An unlisted dotfile fails closed: never sent to a provider, never rewritten."""
        with tempfile.TemporaryDirectory() as tmp:
            p = self._write(tmp, ".someneworc", "key = value\n")
            self.assertNotEqual(detect_file_type(p), "natural_language")
            self.assertFalse(should_compress(p))

    def test_prose_rule_dotfile_still_compressible(self):
        """`.cursorrules` is Cursor's legacy rule file — prose, and the one
        dotfile shape caveman is meant to compress."""
        with tempfile.TemporaryDirectory() as tmp:
            p = self._write(tmp, ".cursorrules", PROSE_BODY)
            self.assertEqual(detect_file_type(p), "natural_language")
            self.assertTrue(should_compress(p))

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

    def test_cursor_rule_mdc_is_natural_language(self):
        """A Cursor rule (.mdc) is prose with YAML frontmatter, the same shape a
        SKILL.md already has. Content sniffing only runs for extensionless
        files, so an unlisted extension fell through to 'unknown' and the rule
        file caveman itself writes was not compressible."""
        body = '---\ndescription: Example rule\nalwaysApply: true\n---\n\n' + PROSE_BODY
        with tempfile.TemporaryDirectory() as tmp:
            p = self._write(tmp, "caveman.mdc", body)
            self.assertEqual(detect_file_type(p), "natural_language")
            self.assertTrue(should_compress(p))


if __name__ == "__main__":
    unittest.main()
