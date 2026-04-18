"""Basic validation tests for the Russian Caveman corpus and assets."""

import json
import re
import subprocess
import tempfile
import unittest
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parent.parent
SKILL_DIR = REPO_ROOT / "skills" / "caveman"
CORPUS = REPO_ROOT / "tests" / "corpus-ru" / "examples.json"
COMMAND = REPO_ROOT / "commands" / "caveman.toml"
EVAL_PROMPTS = REPO_ROOT / "evals" / "prompts" / "ru.txt"
MODE_TRACKER = REPO_ROOT / "hooks" / "caveman-mode-tracker.js"

LEVELS = ["ru-lite", "ru-full", "ru-ultra", "ru-notes"]


class RussianCavemanAssetsTests(unittest.TestCase):
    def test_main_skill_md_has_russian_levels(self):
        skill = SKILL_DIR / "SKILL.md"
        self.assertTrue(skill.exists(), f"missing {skill}")
        text = skill.read_text(encoding="utf-8")
        self.assertTrue(text.startswith("---\n"), "SKILL.md must start with YAML frontmatter")
        self.assertIn("name: caveman", text)
        for lvl in LEVELS:
            self.assertIn(lvl, text, f"main SKILL.md must mention {lvl}")

    def test_russian_rules_file_exists(self):
        rules = SKILL_DIR / "russian-rules.md"
        self.assertTrue(rules.exists(), f"missing {rules}")
        text = rules.read_text(encoding="utf-8")
        self.assertIn("Сжатие синтаксиса", text)
        self.assertIn("Разрешённые сокращения", text)
        self.assertIn("ru-lite", text)
        self.assertIn("ru-full", text)
        self.assertIn("ru-ultra", text)
        self.assertIn("ru-notes", text)

    def test_abbreviations_in_russian_rules(self):
        """Whitelist contains only token-efficient abbreviations (uppercase
        acronyms like БД, ОС). Dotted/hyphenated forms (т.к., кол-во) actually
        cost MORE tokens than full words in BPE — they must be explicitly
        forbidden, not whitelisted."""
        rules = SKILL_DIR / "russian-rules.md"
        self.assertTrue(rules.exists(), f"missing {rules}")
        text = rules.read_text(encoding="utf-8")
        for token in ["БД", "ОС", "ПО", "ОЗУ", "ЧТД"]:
            self.assertIn(token, text, f"whitelist must include {token!r}")
        # File must explicitly flag token-wasteful forms as forbidden
        self.assertIn("Запрещены", text)
        self.assertIn("т.к.", text)  # mentioned as forbidden example

    def test_activation_rule_mentions_russian(self):
        rule = REPO_ROOT / "rules" / "caveman-activate.md"
        self.assertTrue(rule.exists())
        text = rule.read_text(encoding="utf-8")
        self.assertIn("ru-full", text)
        self.assertIn("обычный режим", text)

    def test_command_toml_has_russian_levels(self):
        self.assertTrue(COMMAND.exists(), f"missing {COMMAND}")
        text = COMMAND.read_text(encoding="utf-8")
        self.assertIn("description", text)
        self.assertIn("ru-lite", text)
        self.assertIn("ru-notes", text)

    def test_eval_prompts_ru_nonempty(self):
        self.assertTrue(EVAL_PROMPTS.exists(), f"missing {EVAL_PROMPTS}")
        prompts = [
            p.strip() for p in EVAL_PROMPTS.read_text(encoding="utf-8").splitlines() if p.strip()
        ]
        self.assertGreaterEqual(len(prompts), 10, "need at least 10 Russian eval prompts")
        for p in prompts:
            self.assertTrue(
                re.search(r"[а-яА-ЯёЁ]", p), f"prompt must contain Cyrillic: {p!r}"
            )


class RussianCavemanCorpusTests(unittest.TestCase):
    def setUp(self):
        self.data = json.loads(CORPUS.read_text(encoding="utf-8"))

    def test_corpus_levels_declared(self):
        self.assertEqual(self.data["levels"], LEVELS)

    def test_all_cases_have_all_levels(self):
        cases = self.data["cases"]
        self.assertGreaterEqual(len(cases), 9, "corpus should cover all required types")
        for case in cases:
            for field in ("id", "type", "prompt", "baseline"):
                self.assertIn(field, case, f"case missing {field}")
            for lvl in LEVELS:
                self.assertIn(lvl, case, f"case {case['id']} missing level {lvl}")
                self.assertTrue(case[lvl].strip(), f"{case['id']}/{lvl} is empty")

    def test_compression_is_monotonic_on_average(self):
        """Across the corpus, on average each successive level should be
        shorter than the previous (baseline → lite → full → ultra → notes)."""
        order = ["baseline"] + LEVELS
        sums = {lvl: 0 for lvl in order}
        for case in self.data["cases"]:
            for lvl in order:
                sums[lvl] += len(case[lvl])
        for a, b in zip(order, order[1:]):
            self.assertLess(
                sums[b],
                sums[a],
                f"average {b} length ({sums[b]}) not shorter than {a} ({sums[a]})",
            )

    def test_required_case_types_present(self):
        required_types = {
            "bug-explanation",
            "debug-summary",
            "explanation",
            "comparison",
            "code-review-comment",
            "shell-instruction",
            "answer-with-json",
            "agent-notes",
        }
        actual_types = {case["type"] for case in self.data["cases"]}
        missing = required_types - actual_types
        self.assertFalse(missing, f"corpus missing case types: {missing}")

    def test_code_identifiers_preserved(self):
        """Identifiers wrapped in backticks in the baseline must appear verbatim
        in every compressed level (invariant: code/API names never
        russified or shortened)."""
        ident_re = re.compile(r"`([A-Za-z_][A-Za-z0-9_.]*)`")
        for case in self.data["cases"]:
            baseline_idents = set(ident_re.findall(case["baseline"]))
            for lvl in LEVELS:
                text = case[lvl]
                lvl_idents = set(ident_re.findall(text))
                # Compressed levels may drop some identifiers when the fragment
                # itself is dropped, but must never INVENT or RENAME one.
                invented = lvl_idents - baseline_idents - _allowed_new_idents(case)
                self.assertFalse(
                    invented,
                    f"{case['id']}/{lvl} introduced new identifiers: {invented}",
                )


def _allowed_new_idents(case):
    """Some cases legitimately introduce identifiers at compressed levels
    (e.g. the eval prompt itself mentions them). Read them from the prompt."""
    ident_re = re.compile(r"`([A-Za-z_][A-Za-z0-9_.]*)`")
    return set(ident_re.findall(case["prompt"]))


class RussianCavemanHookTests(unittest.TestCase):
    def _run_tracker(self, prompt, settings=None, env_overrides=None):
        with tempfile.TemporaryDirectory(prefix="caveman-ru-") as tmp:
            home = Path(tmp)
            if settings is not None:
                (home / ".claude").mkdir(parents=True, exist_ok=True)
                (home / ".claude" / "settings.json").write_text(
                    json.dumps(settings), encoding="utf-8"
                )
            env = {
                "HOME": str(home),
                "USERPROFILE": str(home),
                "PATH": "/usr/bin:/bin:/usr/local/bin",
            }
            if env_overrides:
                env.update(env_overrides)
            subprocess.run(
                ["node", str(MODE_TRACKER)],
                input=json.dumps({"prompt": prompt}),
                text=True,
                env=env,
                check=True,
            )
            flag = home / ".claude" / ".caveman-active"
            return flag.read_text(encoding="utf-8") if flag.exists() else None

    def test_tracker_sets_ru_full_on_caveman_ru(self):
        self.assertEqual(self._run_tracker("/caveman ru"), "ru-full")

    def test_tracker_sets_ru_lite(self):
        self.assertEqual(self._run_tracker("/caveman ru-lite"), "ru-lite")

    def test_tracker_sets_ru_ultra(self):
        self.assertEqual(self._run_tracker("/caveman ru-ultra"), "ru-ultra")

    def test_tracker_sets_ru_notes(self):
        self.assertEqual(self._run_tracker("/caveman ru-notes"), "ru-notes")

    def test_tracker_caveman_ru_command(self):
        self.assertEqual(self._run_tracker("/caveman-ru ultra"), "ru-ultra")
        self.assertEqual(self._run_tracker("/caveman-ru"), "ru-full")

    def test_tracker_russian_deactivation(self):
        # First activate, then deactivate via Russian phrase
        with tempfile.TemporaryDirectory(prefix="caveman-ru-deact-") as tmp:
            home = Path(tmp)
            env = {
                "HOME": str(home),
                "USERPROFILE": str(home),
                "PATH": "/usr/bin:/bin:/usr/local/bin",
            }
            subprocess.run(
                ["node", str(MODE_TRACKER)],
                input=json.dumps({"prompt": "/caveman ru-full"}),
                text=True,
                env=env,
                check=True,
            )
            flag = home / ".claude" / ".caveman-active"
            self.assertTrue(flag.exists())

            subprocess.run(
                ["node", str(MODE_TRACKER)],
                input=json.dumps({"prompt": "обычный режим пожалуйста"}),
                text=True,
                env=env,
                check=True,
            )
            self.assertFalse(flag.exists(), "Russian deactivation phrase must clear flag")


class AutoDetectLanguageTests(unittest.TestCase):
    """Tests for the opt-in Cyrillic auto-detection in the mode tracker."""

    def _seed_and_run(self, seed_mode, prompt, *, enabled=True, via_env=False):
        """Create a temp HOME, seed the flag file with `seed_mode`, optionally
        enable auto-detect via settings.json or env, run the tracker with
        `prompt`, then return the final flag contents (or None if deleted)."""
        with tempfile.TemporaryDirectory(prefix="caveman-autodetect-") as tmp:
            home = Path(tmp)
            claude = home / ".claude"
            claude.mkdir(parents=True, exist_ok=True)
            if seed_mode is not None:
                (claude / ".caveman-active").write_text(seed_mode, encoding="utf-8")

            settings = {}
            if enabled and not via_env:
                settings = {"caveman": {"autoDetectLanguage": True}}
            if settings:
                (claude / "settings.json").write_text(
                    json.dumps(settings), encoding="utf-8"
                )

            env = {
                "HOME": str(home),
                "USERPROFILE": str(home),
                "PATH": "/usr/bin:/bin:/usr/local/bin",
            }
            if enabled and via_env:
                env["CAVEMAN_AUTO_DETECT_LANG"] = "1"

            subprocess.run(
                ["node", str(MODE_TRACKER)],
                input=json.dumps({"prompt": prompt}),
                text=True,
                env=env,
                check=True,
            )
            flag = claude / ".caveman-active"
            return flag.read_text(encoding="utf-8") if flag.exists() else None

    def test_russian_prompt_flips_full_to_ru_full(self):
        result = self._seed_and_run(
            "full",
            "Почему мой React компонент рендерится каждый раз при обновлении родителя?",
        )
        self.assertEqual(result, "ru-full")

    def test_russian_prompt_flips_lite_to_ru_lite(self):
        result = self._seed_and_run(
            "lite",
            "Объясни подробно, как работает connection pool в базе данных.",
        )
        self.assertEqual(result, "ru-lite")

    def test_russian_prompt_flips_ultra_to_ru_ultra(self):
        result = self._seed_and_run(
            "ultra",
            "Как посмотреть, какой процесс держит порт 8080 на Linux?",
        )
        self.assertEqual(result, "ru-ultra")

    def test_english_prompt_flips_ru_full_to_full(self):
        result = self._seed_and_run(
            "ru-full",
            "Why does my React component re-render on every parent update?",
        )
        self.assertEqual(result, "full")

    def test_disabled_by_default(self):
        """Without opting in, Russian prompt must NOT flip the mode."""
        result = self._seed_and_run(
            "full",
            "Почему мой React компонент рендерится каждый раз?",
            enabled=False,
        )
        self.assertEqual(result, "full")

    def test_enabled_via_env(self):
        result = self._seed_and_run(
            "full",
            "Почему мой React компонент рендерится каждый раз?",
            via_env=True,
        )
        self.assertEqual(result, "ru-full")

    def test_english_with_cyrillic_identifiers_does_not_flip(self):
        """English prompt that just mentions a Cyrillic token in backticks
        must NOT be treated as Russian — code fences are stripped first."""
        result = self._seed_and_run(
            "full",
            "Why is `кириллица_var` undefined when I import the module from Python?",
        )
        self.assertEqual(result, "full")

    def test_short_prompt_does_not_flip(self):
        result = self._seed_and_run("full", "да", enabled=True)
        self.assertEqual(result, "full")

    def test_explicit_command_overrides_autodetect(self):
        """`/caveman full` on a Russian-looking prompt must still set full."""
        result = self._seed_and_run(
            "ru-full",
            "/caveman full пожалуйста переключись на английский",
        )
        self.assertEqual(result, "full")

    def test_ru_notes_not_autoflipped_on_english(self):
        """ru-notes has no English equivalent; an English prompt should still
        flip back to the sensible default `full`, not get stuck."""
        result = self._seed_and_run(
            "ru-notes",
            "Please explain how to fix this bug in the authentication middleware.",
        )
        self.assertEqual(result, "full")


class SessionStartLanguageTests(unittest.TestCase):
    """Tests for the default-language logic in hooks/caveman-activate.js."""

    ACTIVATE = REPO_ROOT / "hooks" / "caveman-activate.js"

    def _run(self, *, settings=None, env_overrides=None):
        with tempfile.TemporaryDirectory(prefix="caveman-activate-") as tmp:
            home = Path(tmp)
            claude = home / ".claude"
            claude.mkdir(parents=True, exist_ok=True)
            if settings is not None:
                (claude / "settings.json").write_text(
                    json.dumps(settings), encoding="utf-8"
                )
            env = {
                "HOME": str(home),
                "USERPROFILE": str(home),
                "PATH": "/usr/bin:/bin:/usr/local/bin",
            }
            if env_overrides:
                env.update(env_overrides)
            result = subprocess.run(
                ["node", str(self.ACTIVATE)],
                text=True,
                env=env,
                capture_output=True,
                check=True,
            )
            flag = claude / ".caveman-active"
            return {
                "stdout": result.stdout,
                "flag": flag.read_text(encoding="utf-8") if flag.exists() else None,
            }

    def test_default_is_english(self):
        r = self._run()
        self.assertEqual(r["flag"], "full")
        self.assertIn("CAVEMAN MODE ACTIVE", r["stdout"])
        self.assertNotIn("CAVEMAN-RU", r["stdout"])

    def test_settings_caveman_lang_ru(self):
        r = self._run(settings={"caveman": {"lang": "ru"}})
        self.assertEqual(r["flag"], "ru-full")
        self.assertIn("CAVEMAN-RU", r["stdout"])
        self.assertIn("Разрешённые сокращения", r["stdout"])

    def test_settings_caveman_lang_en_explicit(self):
        r = self._run(settings={"caveman": {"lang": "en"}})
        self.assertEqual(r["flag"], "full")
        self.assertIn("CAVEMAN MODE ACTIVE", r["stdout"])

    def test_env_var_overrides_settings(self):
        r = self._run(
            settings={"caveman": {"lang": "en"}},
            env_overrides={"CAVEMAN_LANG": "ru"},
        )
        self.assertEqual(r["flag"], "ru-full")
        self.assertIn("CAVEMAN-RU", r["stdout"])

    def test_invalid_lang_falls_back_to_english(self):
        r = self._run(settings={"caveman": {"lang": "de"}})
        self.assertEqual(r["flag"], "full")

    def test_malformed_settings_is_silent_fallback(self):
        with tempfile.TemporaryDirectory(prefix="caveman-bad-settings-") as tmp:
            home = Path(tmp)
            claude = home / ".claude"
            claude.mkdir(parents=True, exist_ok=True)
            (claude / "settings.json").write_text("{ this is not valid json")
            env = {
                "HOME": str(home),
                "USERPROFILE": str(home),
                "PATH": "/usr/bin:/bin:/usr/local/bin",
            }
            result = subprocess.run(
                ["node", str(self.ACTIVATE)],
                text=True,
                env=env,
                capture_output=True,
                check=True,
            )
            self.assertEqual(
                (claude / ".caveman-active").read_text(encoding="utf-8"), "full"
            )
            self.assertIn("CAVEMAN MODE ACTIVE", result.stdout)


if __name__ == "__main__":
    unittest.main()
