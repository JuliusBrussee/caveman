"""Real Hermes SessionDB usage and Caveman attribution contracts."""
from __future__ import annotations

from concurrent.futures import ThreadPoolExecutor
import os
from pathlib import Path
import sys
import tempfile
import unittest
from unittest import mock


class FakeSessionDB:
    def __init__(self):
        self.rows = {}

    def get_session(self, session_id):
        row = self.rows.get(session_id)
        return dict(row) if row else None

    def set(self, session_id, **values):
        base = {
            "id": session_id,
            "source": "cli",
            "model": "openai/gpt-5.6-sol",
            "input_tokens": 0,
            "output_tokens": 0,
            "cache_read_tokens": 0,
            "cache_write_tokens": 0,
            "reasoning_tokens": 0,
            "estimated_cost_usd": None,
            "actual_cost_usd": None,
            "cost_status": None,
            "cost_source": None,
        }
        base.update(values)
        self.rows[session_id] = base


class StatsTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.root = Path(self.temp.name)
        self.patch = mock.patch.dict(
            os.environ,
            {
                "HOME": str(self.root / "home"),
                "HERMES_HOME": str(self.root / "hermes"),
                "XDG_CONFIG_HOME": str(self.root / "xdg"),
            },
            clear=False,
        )
        self.patch.start()
        from hermes_caveman import state, stats
        self.state, self.stats = state, stats
        self.db = FakeSessionDB()
        self.db.set("s")

    def tearDown(self):
        self.patch.stop()
        self.temp.cleanup()

    def reconcile(self, *, mode, output, **usage):
        self.stats.mark_turn_mode("s", mode)
        self.db.set("s", output_tokens=output, **usage)
        self.stats.post_llm_call(session_id="s", db=self.db, future="ok")

    def test_current_session_reads_actual_counters_model_and_stored_cost(self):
        self.db.set(
            "s",
            model="openai/gpt-5.6-sol",
            input_tokens=1234,
            output_tokens=350,
            cache_read_tokens=500,
            cache_write_tokens=20,
            reasoning_tokens=77,
            actual_cost_usd=0.1234,
            estimated_cost_usd=0.2,
            cost_status="actual",
            cost_source="provider",
        )
        data = self.stats.build_stats("s", db=self.db)
        current = data["current"]
        self.assertEqual(current["model"], "openai/gpt-5.6-sol")
        self.assertEqual(
            {k: current[k] for k in ("input_tokens", "output_tokens", "cache_read_tokens", "cache_write_tokens", "reasoning_tokens")},
            {"input_tokens": 1234, "output_tokens": 350, "cache_read_tokens": 500, "cache_write_tokens": 20, "reasoning_tokens": 77},
        )
        self.assertEqual(current["cost_usd"], 0.1234)
        self.assertEqual(current["cost_kind"], "actual")

    def test_post_turn_attributes_only_new_cumulative_delta_across_mode_changes(self):
        self.stats.start_session("s", db=self.db)
        self.reconcile(mode="full", output=100)
        self.reconcile(mode="lite", output=150)
        self.reconcile(mode=None, output=175)
        data = self.stats.build_stats("s", db=self.db)
        self.assertEqual(data["attribution"]["by_mode"], {"full": 100, "lite": 50})
        self.assertEqual(data["attribution"]["unknown_output_tokens"], 25)
        self.assertEqual(data["savings"]["eligible_full_output_tokens"], 100)
        self.assertEqual(data["savings"]["estimated_normal_output_tokens"], 286)
        self.assertEqual(data["savings"]["estimated_saved_output_tokens"], 186)

    def test_repeated_retry_snapshot_is_idempotent_and_counter_decrease_is_not_negative(self):
        self.stats.start_session("s", db=self.db)
        self.reconcile(mode="full", output=100)
        self.reconcile(mode="full", output=100)
        self.reconcile(mode="full", output=25)
        self.reconcile(mode="full", output=40)
        data = self.stats.build_stats("s", db=self.db)
        self.assertEqual(data["attribution"]["by_mode"], {"full": 115})

    def test_only_full_mode_receives_measured_estimate_and_rounding_is_exact(self):
        self.assertEqual(self.stats.estimate_full_savings(350), (1000, 650))
        self.assertEqual(self.stats.estimate_full_savings(100), (286, 186))
        self.stats.start_session("s", db=self.db)
        self.reconcile(mode="ultra", output=350)
        data = self.stats.build_stats("s", db=self.db)
        self.assertEqual(data["savings"]["estimated_saved_output_tokens"], 0)
        self.assertEqual(data["savings"]["eligible_full_output_tokens"], 0)

    def test_unattributed_existing_prefix_is_excluded_not_guessed(self):
        self.db.set("s", output_tokens=90)
        self.stats.start_session("s", db=self.db)
        self.reconcile(mode="full", output=100)
        data = self.stats.build_stats("s", db=self.db)
        self.assertEqual(data["attribution"]["by_mode"], {"full": 10})
        self.assertEqual(data["savings"]["estimated_saved_output_tokens"], 19)

    def test_first_turn_in_resumed_session_seeds_missing_sidecar_from_sessiondb(self):
        self.db.set("s", output_tokens=90)
        self.stats.mark_turn_mode("s", "full", db=self.db)
        self.db.set("s", output_tokens=100)
        self.stats.post_llm_call(session_id="s", db=self.db)
        data = self.stats.build_stats("s", db=self.db)
        self.assertEqual(data["attribution"]["by_mode"], {"full": 10})
        self.assertEqual(data["attribution"]["unknown_output_tokens"], 0)

    def test_lifetime_aggregation_skips_malformed_history(self):
        self.stats.start_session("s", db=self.db)
        self.reconcile(mode="full", output=100)
        self.db.set("other")
        self.stats.start_session("other", db=self.db)
        self.stats.mark_turn_mode("other", "lite")
        self.db.set("other", output_tokens=40)
        self.stats.post_llm_call(session_id="other", db=self.db)
        with self.state.history_path().open("a", encoding="utf-8") as fh:
            fh.write('{"truncated":')
        data = self.stats.build_stats("s", db=self.db)
        self.assertEqual(data["lifetime"]["by_mode"], {"full": 100, "lite": 40})
        self.assertEqual(data["lifetime"]["attributed_output_tokens"], 140)
        self.assertEqual(data["lifetime"]["estimated_saved_output_tokens"], 186)

    def test_history_compaction_bounds_rows_without_losing_lifetime_totals(self):
        for index in range(40):
            self.state.append_history(
                {
                    "session": f"s{index}",
                    "mode": "full" if index % 2 == 0 else "lite",
                    "output_tokens": index + 1,
                }
            )
        before = self.stats.build_stats("s", db=self.db)["lifetime"]
        self.stats.compact_history(max_rows=5)
        after = self.stats.build_stats("s", db=self.db)["lifetime"]
        self.assertEqual(after, before)
        self.assertLessEqual(len(self.state.read_history()), 5)

    def test_concurrent_post_hook_reconciles_one_delta_once(self):
        self.stats.start_session("s", db=self.db)
        self.stats.mark_turn_mode("s", "full")
        self.db.set("s", output_tokens=100)
        with ThreadPoolExecutor(max_workers=12) as pool:
            list(pool.map(lambda _: self.stats.post_llm_call(session_id="s", db=self.db), range(30)))
        data = self.stats.build_stats("s", db=self.db)
        self.assertEqual(data["attribution"]["by_mode"], {"full": 100})
        self.assertEqual(data["lifetime"]["by_mode"], {"full": 100})

    def test_missing_cost_never_uses_claude_or_other_hardcoded_pricing(self):
        self.db.set("s", model="claude-opus-4-6", output_tokens=1000, actual_cost_usd=None, estimated_cost_usd=None)
        data = self.stats.build_stats("s", db=self.db)
        self.assertIsNone(data["current"]["cost_usd"])
        text = self.stats.format_stats(data)
        self.assertIn("Cost: unavailable", text)
        self.assertNotIn("$75", text)
        self.assertIn("output tokens only", text)
        self.assertIn("input/cache/reasoning unchanged", text)

    def test_skill_documents_native_sessiondb_tool_and_estimate_boundary(self):
        skill = (Path(__file__).resolve().parents[2] / "skills" / "caveman-stats" / "SKILL.md").read_text(encoding="utf-8")
        self.assertIn("## Hermes Agent", skill)
        self.assertIn("`caveman_stats`", skill)
        self.assertIn("SessionDB", skill)
        self.assertIn("output tokens only", skill)
        self.assertIn("input/cache/reasoning", skill)
        hermes = skill.split("## Hermes Agent", 1)[1]
        self.assertNotIn("character count", hermes.lower())
        self.assertNotIn("Claude transcript", hermes)

    def test_tool_requires_dispatch_session_id_and_returns_native_text(self):
        self.assertIn("session_id", self.stats.caveman_stats_tool({}, session_id="", db=self.db).lower())
        self.db.set("s", output_tokens=12)
        text = self.stats.caveman_stats_tool({}, session_id="s", db=self.db, task_id="t", future="ok")
        self.assertIn("Current session", text)
        self.assertIn("Output tokens", text)
        self.assertNotIn("estimate from characters", text)


@unittest.skipUnless(
    os.environ.get("CAVEMAN_HERMES_SOURCE_ROOT") and sys.version_info >= (3, 10),
    "run with Hermes Python and CAVEMAN_HERMES_SOURCE_ROOT",
)
class LiveSessionDBFixtureTests(unittest.TestCase):
    def test_public_sessiondb_fixture_round_trip(self):
        source = Path(os.environ["CAVEMAN_HERMES_SOURCE_ROOT"]).resolve()
        sys.path.insert(0, str(source))
        try:
            from hermes_state import SessionDB
            from hermes_caveman import stats
            with tempfile.TemporaryDirectory() as td:
                root = Path(td)
                with mock.patch.dict(os.environ, {"HERMES_HOME": str(root / "hermes"), "HOME": str(root / "home")}, clear=False):
                    db = SessionDB(db_path=root / "state.db")
                    try:
                        db.create_session(session_id="fixture", source="cli", model="openai/gpt-5.6-sol")
                        db.update_token_counts(
                            "fixture",
                            input_tokens=111,
                            output_tokens=35,
                            cache_read_tokens=9,
                            cache_write_tokens=2,
                            reasoning_tokens=7,
                            actual_cost_usd=0.0123,
                            cost_status="actual",
                            cost_source="fixture",
                            model="openai/gpt-5.6-sol",
                            billing_provider="openai-codex",
                        )
                        data = stats.build_stats("fixture", db=db)
                    finally:
                        db.close()
            self.assertEqual(data["current"]["output_tokens"], 35)
            self.assertEqual(data["current"]["reasoning_tokens"], 7)
            self.assertEqual(data["current"]["cost_usd"], 0.0123)
            self.assertEqual(data["current"]["model"], "openai/gpt-5.6-sol")
        finally:
            try: sys.path.remove(str(source))
            except ValueError: pass


if __name__ == "__main__":
    unittest.main()
