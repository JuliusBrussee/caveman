"""Local decision reporting has no network or content-capture side effects."""
import dataclasses
import unittest

from caveman_cloud.middleware import AsyncMiddlewareRuntime, MiddlewareRuntime, Optimization


class TestCallReports(unittest.TestCase):
    def test_five_outcomes_and_immutable_bounded_metadata(self):
        received = []
        runtime = MiddlewareRuntime(on_report=received.append)
        runtime._http = lambda *_: self.fail("report invoked transport")
        self.assertIsNone(runtime.last_report)
        view = {"transform_id": "caveman.engine.log.v1", "reused": False}
        runtime.report(Optimization("optimized", "eligible", [view]), adapter="native-test", logical_call_id="logical-1", attempt_id="attempt-1")
        runtime.report(Optimization("optimized", "eligible", [{**view, "reused": True}]))
        runtime.report(None, reason="unsupported_shape")
        runtime.report(Optimization("record", "record"))
        disabled = MiddlewareRuntime(mode="off", on_report=received.append)
        disabled._http = runtime._http
        disabled.report(Optimization("optimized", "eligible", [view]))
        self.assertEqual([item.status for item in received], ["applied", "reused", "skipped", "recorded", "disabled"])
        self.assertEqual([item.transform_ids for item in received], [("caveman.engine.log.v1",), ("caveman.engine.log.v1",), (), (), ()])
        self.assertEqual([item.replacement_count for item in received], [1, 1, 0, 0, 0])
        self.assertEqual([item.reused_count for item in received], [0, 1, 0, 0, 0])
        self.assertEqual((received[0].logical_call_id, received[0].attempt_id), ("logical-1", "attempt-1"))
        self.assertEqual(disabled.last_report.reason, "disabled")
        self.assertIs(runtime.last_report, received[3])
        with self.assertRaises(dataclasses.FrozenInstanceError):
            received[0].status = "forged"
        runtime.close()
        disabled.close()

    def test_throwing_sink_and_unsafe_metadata_do_not_change_call(self):
        secret = "source text with whitespace and api-key=must-not-log"

        def throwing(_):
            raise RuntimeError(secret)

        runtime = MiddlewareRuntime(on_report=throwing)
        report = runtime.report(reason=secret, adapter=secret, attempt_id=secret, logical_call_id=secret)
        self.assertEqual(report.reason, "unknown_reason")
        self.assertEqual((report.adapter, report.attempt_id, report.logical_call_id), (None, None, None))
        self.assertNotIn("must-not-log", repr(report))
        runtime.close()

    def test_prepared_plan_does_not_claim_application(self):
        received = []
        runtime = MiddlewareRuntime(mode="off", on_report=received.append)
        prepared = runtime.optimize(scope=None, adapter=None, candidates=[], manifest=[])
        self.assertEqual(received, [])
        runtime.report(prepared)
        self.assertEqual([item.status for item in received], ["disabled"])
        runtime.close()


class TestAsyncCallReports(unittest.IsolatedAsyncioTestCase):
    async def test_async_view_shares_one_local_report_with_owner(self):
        received = []
        runtime = MiddlewareRuntime(on_report=received.append)
        async_runtime = runtime.as_async()
        report = async_runtime.report(None, reason="no_candidate")
        self.assertIs(report, runtime.last_report)
        self.assertIs(report, async_runtime.last_report)
        self.assertEqual(received, [report])
        await async_runtime.aclose()
        runtime.close()


if __name__ == "__main__":
    unittest.main()
