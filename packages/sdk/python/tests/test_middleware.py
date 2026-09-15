import asyncio
import copy
import json
import threading
import time
import unittest
from pathlib import Path

from caveman_cloud.middleware import Adapter, AsyncMiddlewareRuntime, Candidate, MiddlewareError, MiddlewareRuntime, Scope, sha256
from caveman_cloud.middleware import validate

FIXTURE = json.loads((Path(__file__).resolve().parents[2] / "parity/middleware.fixtures.json").read_text(encoding="utf-8"))


def inputs(binding=None):
    r = FIXTURE["request"]
    return dict(scope=Scope(**r["scope"]), adapter=Adapter(**r["adapter"]), candidates=[Candidate(**{k: v for k, v in s.items() if k != "sha256"}) for s in r["segments"]],
                manifest=r["context_manifest"], sequence=r["sequence"], binding=binding, recovery_overhead_text="registered executor",
                request_id=r["request_id"], logical_call_id=r["logical_call_id"], attempt_id=r["attempt_id"], idempotency_key=r["idempotency_key"])


class TestMiddlewareProtocol(unittest.TestCase):
    def test_unsupported_native_version_declines_without_io(self):
        diagnostics = []
        runtime = MiddlewareRuntime(on_diagnostic=diagnostics.append)
        self.addCleanup(runtime.close)
        runtime._http = lambda *_: self.fail("unexpected network")
        outcome = runtime.decline("unsupported_version")
        self.assertEqual(outcome.reason, "unsupported_version")
        self.assertIsNone(outcome.plan)
        self.assertEqual(len(outcome.replacements), 0)
        self.assertEqual(diagnostics, [{"code": "unsupported_version", "cache_continuity": "unavailable"}])
        strict = MiddlewareRuntime(strict=True)
        self.addCleanup(strict.close)
        with self.assertRaisesRegex(MiddlewareError, "unsupported_version"):
            strict.decline("unsupported_version")
        off = MiddlewareRuntime(mode="off", strict=True)
        self.addCleanup(off.close)
        self.assertEqual(off.decline("unsupported_version").status, "off")

    def test_shared_vectors(self):
        self.assertEqual(json.dumps(FIXTURE["request"], ensure_ascii=False, separators=(",", ":")), FIXTURE["request_wire"])
        for vector in FIXTURE["digest_vectors"]:
            self.assertEqual(sha256(vector["text"]), vector["sha256"])
        caps = validate.capabilities(FIXTURE["capabilities"])
        self.assertEqual(validate.plan(FIXTURE["plan"], FIXTURE["request"], sha256(FIXTURE["request_wire"]), caps), FIXTURE["plan"])
        for vector in FIXTURE["invalid_plans"]:
            with self.subTest(vector["id"]):
                bad = copy.deepcopy(FIXTURE["plan"])
                target = bad
                for key in vector["path"][:-1]:
                    target = target[key]
                target[vector["path"][-1]] = vector["value"]
                with self.assertRaisesRegex(MiddlewareError, "invalid_plan"):
                    validate.plan(bad, FIXTURE["request"], FIXTURE["plan"]["input_digest"], caps)
        self.assertEqual(validate.page(FIXTURE["page"], {"handle": FIXTURE["page"]["handle"]}, 262144), FIXTURE["page"])

    def test_delegation_and_binding(self):
        runtime = MiddlewareRuntime()
        sent = []

        def http(path, body, timeout):
            if path == "capabilities":
                return copy.deepcopy(FIXTURE["capabilities"])
            request = json.loads(body)
            sent.append(request)
            plan = copy.deepcopy(FIXTURE["plan"])
            plan["input_digest"] = sha256(body)
            plan["recovery"]["binding_id"] = request["recovery_binding"]["id"]
            return plan

        runtime._http = http
        binding = runtime.recovery(Scope(**FIXTURE["request"]["scope"]))
        self.assertEqual(runtime.optimize(**inputs(binding)).status, "optimized")
        expected = copy.deepcopy(FIXTURE["request"])
        expected["recovery_binding"]["id"] = binding.id
        self.assertEqual(sent[0], expected)
        self.assertFalse(runtime.owns_binding(copy.copy(binding), binding.scope))

    def test_recovery_binding_mutation_cannot_keep_attestation(self):
        with MiddlewareRuntime() as runtime:
            scope = Scope("native", "registration")
            binding = runtime.recovery(scope)
            self.assertTrue(runtime.owns_binding(binding, scope))
            binding.input_schema["properties"]["handle"]["type"] = "integer"
            self.assertFalse(runtime.owns_binding(binding, scope))
            fresh = runtime.recovery(scope)
            self.assertEqual(fresh.input_schema["properties"]["handle"]["type"], "string")
            self.assertTrue(runtime.owns_binding(fresh, scope))
            object.__setattr__(fresh, "execute", lambda **_: "wrong executor")
            self.assertFalse(runtime.owns_binding(fresh, scope))
            self.assertFalse(runtime.owns_binding(binding, Scope("native", "other")))

    def test_off_and_endpoint_controls(self):
        runtime = MiddlewareRuntime(mode="off")
        runtime._http = lambda *_: self.fail("off transferred content")
        self.assertEqual(runtime.optimize(**inputs()).status, "off")
        with self.assertRaisesRegex(MiddlewareError, "remote_content_not_enabled"):
            MiddlewareRuntime(endpoint="https://remote.example")

    def test_valid_fallback_does_not_claim_unavailable_cache_continuity(self):
        for reason in ("cache_state_unavailable", "recovery_unavailable", "protected"):
            with self.subTest(reason=reason):
                diagnostics = []
                runtime = MiddlewareRuntime(on_diagnostic=diagnostics.append)
                def http(path, body, timeout):
                    if path == "capabilities":
                        return copy.deepcopy(FIXTURE["capabilities"])
                    plan = copy.deepcopy(FIXTURE["plan"])
                    plan["input_digest"] = sha256(body)
                    plan["status"], plan["reason"] = "bypassed", reason
                    plan["skipped"] = [{"segment_id": plan["replacements"][0]["segment_id"], "reason": reason}]
                    plan["replacements"] = []
                    plan["measurement"]["tokens_after"] = plan["measurement"]["tokens_before"]
                    plan["measurement"]["unique_tokens_reduced"] = 0
                    return plan
                runtime._http = http
                try:
                    result = runtime.optimize(**inputs(runtime.recovery(Scope(**FIXTURE["request"]["scope"]))))
                    self.assertEqual(result.reason, reason)
                    self.assertEqual(result.replacements, [])
                    expected = "persistent_choices" if reason == "protected" else "unavailable"
                    self.assertEqual(result.cache_continuity, expected)
                    self.assertEqual(diagnostics, [{"code": reason, "cache_continuity": expected}])
                finally:
                    runtime.close()

    def test_deadline_and_stale_scope_do_not_open_outage_circuit(self):
        runtime = MiddlewareRuntime()
        discovery, mode = [], ["deadline"]
        def http(path, *_):
            if path == "capabilities":
                discovery.append(path)
                return copy.deepcopy(FIXTURE["capabilities"])
            raise MiddlewareError(mode[0])
        runtime._http = http
        try:
            for _ in range(5):
                self.assertEqual(runtime.optimize(**inputs()).reason, "deadline")
            self.assertEqual(len(discovery), 1)
            mode[0] = "epoch_changed"
            for _ in range(5):
                self.assertEqual(runtime.optimize(**inputs()).reason, "epoch_changed")
            mode[0] = "runtime_unavailable"
            for _ in range(3):
                self.assertEqual(runtime.optimize(**inputs()).reason, "runtime_unavailable")
            self.assertEqual(runtime.optimize(**inputs()).reason, "circuit_open")
        finally:
            runtime.close()


class TestAsyncMiddleware(unittest.IsolatedAsyncioTestCase):
    async def test_io_does_not_block_loop_and_cancel_never_returns_fallback(self):
        runtime = AsyncMiddlewareRuntime(deadline_ms=500)
        entered, release = threading.Event(), threading.Event()

        def slow(*args):
            entered.set()
            release.wait(0.4)
            raise MiddlewareError("runtime_unavailable")

        runtime._runtime._http = slow
        task = asyncio.create_task(runtime.optimize(**inputs()))
        for _ in range(100):
            if entered.is_set():
                break
            await asyncio.sleep(0.001)
        self.assertTrue(entered.is_set())
        started = time.monotonic()
        await asyncio.sleep(0.01)
        self.assertLess(time.monotonic() - started, 0.2)
        task.cancel()
        with self.assertRaises(asyncio.CancelledError):
            await task
        release.set()
        await runtime.aclose()

    async def test_queued_work_counts_against_one_deadline(self):
        runtime = AsyncMiddlewareRuntime(deadline_ms=20)
        release = threading.Event()
        paths = []
        def blocked_discovery(path, *_):
            paths.append(path)
            release.wait(2)
            return copy.deepcopy(FIXTURE["capabilities"])
        runtime._runtime._http = blocked_discovery
        startup = [asyncio.create_task(runtime.ready()) for _ in range(4)]
        while len(paths) < 4:
            await asyncio.sleep(0.001)
        tasks = [asyncio.create_task(runtime.optimize(**inputs())) for _ in range(20)]
        # Hold the worker boundary until all submissions have had a chance to
        # enter. CPU scheduling cannot turn the capacity assertion into a race.
        await asyncio.sleep(0.06)
        release.set()
        await asyncio.gather(*startup)
        outcomes = await asyncio.gather(*tasks)
        self.assertTrue(all(out.status == "bypassed" for out in outcomes))
        self.assertIn("capacity", [out.reason for out in outcomes])
        self.assertIn("deadline", [out.reason for out in outcomes])
        self.assertEqual(paths, ["capabilities"] * 4, "expired queued calls never start optimization I/O")
        await runtime.aclose()
