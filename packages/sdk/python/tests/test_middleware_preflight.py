"""The startup contract is shared with TypeScript, without provider calls."""
import asyncio
import copy
import dataclasses
import json
import threading
import unittest
from pathlib import Path

from caveman_cloud.middleware import AsyncMiddlewareRuntime, MiddlewareError, MiddlewareRuntime

PARITY = Path(__file__).resolve().parents[2] / "parity"
PROTOCOL = json.loads((PARITY / "middleware.fixtures.json").read_text())
FIXTURE = json.loads((PARITY / "middleware-preflight.fixtures.json").read_text())


def transport(vector, calls):
    def http(path, body, timeout):
        calls.append(path)
        assert path == "capabilities" and body is None
        if vector.get("transport_error"):
            raise OSError("source text with api-key=must-not-log")
        if vector.get("timeout_error"):
            raise TimeoutError("source text with api-key=must-not-log")
        if vector.get("error"):
            raise MiddlewareError(vector["error"])
        return {**copy.deepcopy(PROTOCOL["capabilities"]), **vector.get("capabilities", {})}
    return http


class TestPreflight(unittest.TestCase):
    def test_shared_preflight_outcomes(self):
        for vector in FIXTURE["cases"]:
            with self.subTest(vector["id"]), MiddlewareRuntime(mode=vector["mode"], strict=True) as runtime:
                calls = []
                runtime._http = transport(vector, calls) if not vector.get("closed") else runtime._http
                if vector.get("closed"):
                    runtime.close()
                result = runtime.preflight()
                self.assertEqual(dataclasses.asdict(result), vector["expected"])
                self.assertNotIn("must-not-log", repr(result))
                self.assertEqual(len(calls), 0 if vector["mode"] == "off" or vector.get("closed") else 1)
                self.assertIsNone(runtime.last_report)
                with self.assertRaises(dataclasses.FrozenInstanceError):
                    result.status = "forged"

    def test_ready_stays_throwing_and_preflight_recovers(self):
        with MiddlewareRuntime() as runtime:
            runtime._http = transport({"transport_error": True}, [])
            with self.assertRaises(OSError):
                runtime.ready()
            self.assertEqual(runtime.preflight().reason, "runtime_unavailable")
            runtime._http = transport({}, [])
            self.assertEqual(runtime.preflight().reason, "ready")
            self.assertIsNotNone(runtime._caps)
            runtime._http = transport({"transport_error": True}, [])
            self.assertEqual(runtime.preflight().reason, "runtime_unavailable")
            self.assertIsNone(runtime._caps)


class TestAsyncPreflight(unittest.IsolatedAsyncioTestCase):
    async def test_shared_preflight_outcomes(self):
        for vector in FIXTURE["cases"]:
            with self.subTest(vector["id"]):
                runtime = AsyncMiddlewareRuntime(mode=vector["mode"], strict=True, deadline_ms=2000)
                calls = []
                runtime._runtime._http = transport(vector, calls)
                try:
                    if vector.get("closed"):
                        await runtime.aclose()
                    self.assertEqual(dataclasses.asdict(await runtime.preflight()), vector["expected"])
                    self.assertEqual(len(calls), 0 if vector["mode"] == "off" or vector.get("closed") else 1)
                finally:
                    await runtime.aclose()

    async def test_caller_cancel_propagates_and_queue_deadline_is_bounded(self):
        runtime = AsyncMiddlewareRuntime(deadline_ms=500)
        entered, release = threading.Event(), threading.Event()

        def slow(*_):
            entered.set()
            release.wait(2)
            raise OSError("runtime down")

        runtime._runtime._http = slow
        try:
            task = asyncio.create_task(runtime.preflight())
            while not entered.is_set():
                await asyncio.sleep(0.001)
            task.cancel()
            with self.assertRaises(asyncio.CancelledError):
                await task
            runtime._runtime.deadline_ms = 20
            self.assertEqual((await runtime.preflight()).reason, "deadline")
        finally:
            release.set()
            await runtime.aclose()

    async def test_close_during_discovery_cannot_restore_capabilities(self):
        runtime = AsyncMiddlewareRuntime(deadline_ms=2000)
        entered, release = threading.Event(), threading.Event()
        def slow(*_):
            entered.set()
            release.wait(2)
            return copy.deepcopy(PROTOCOL["capabilities"])
        runtime._runtime._http = slow
        task = asyncio.create_task(runtime.preflight())
        try:
            while not entered.is_set():
                await asyncio.sleep(0.001)
            runtime._runtime.close()
            release.set()
            self.assertEqual((await task).reason, "closed")
            self.assertIsNone(runtime._runtime._caps)
        finally:
            release.set()
            await runtime.aclose()


if __name__ == "__main__":
    unittest.main()
