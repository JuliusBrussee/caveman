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
        # §8: unsupported_version is a `ready` reason. The request path passes through even in strict mode.
        strict = MiddlewareRuntime(strict=True)
        self.addCleanup(strict.close)
        self.assertEqual(strict.decline("unsupported_version").reason, "unsupported_version")
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
        # Endpoint refusals never raise at construction (decision 3); calls bypass and preflight reports them.
        for strict in (False, True):
            remote = MiddlewareRuntime(endpoint="https://remote.example", strict=strict)
            self.addCleanup(remote.close)
            remote._transport = lambda *_: self.fail("refused endpoint transferred content")
            self.assertEqual(remote.optimize(**inputs()).reason, "remote_content_not_enabled")
            self.assertEqual(remote.preflight().reason, "remote_content_not_enabled")
            with self.assertRaisesRegex(MiddlewareError, "remote_content_not_enabled"):
                remote.ready()

    def test_record_mode_never_prepares_replacements(self):
        with MiddlewareRuntime(mode="record") as runtime:
            def http(path, body, timeout):
                if path == "capabilities":
                    return copy.deepcopy(FIXTURE["capabilities"])
                request = json.loads(body)
                self.assertEqual(request["mode"], "record")
                plan = copy.deepcopy(FIXTURE["plan"])
                plan["input_digest"] = sha256(body)
                plan["status"], plan["reason"], plan["replacements"] = "record", "record", []
                plan["skipped"] = [{"segment_id": segment["id"], "reason": "record"} for segment in request["segments"]]
                plan["measurement"]["tokens_after"] = plan["measurement"]["tokens_before"]
                plan["measurement"]["unique_tokens_reduced"] = 0
                return plan
            runtime._http = http
            result = runtime.optimize(**inputs())
            self.assertEqual((result.status, result.replacements), ("record", []))
            self.assertEqual(runtime.report(result).status, "recorded")

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

    def test_breaker_counts_deadlines_and_outages_not_client_codes(self):
        # §10/K8: deadlines now count (B3); 4xx decisions such as epoch_changed are successes.
        runtime = MiddlewareRuntime(deadline_ms=5000)
        discovery, mode = [], ["epoch_changed"]
        def http(path, *_):
            if path == "capabilities":
                discovery.append(path)
                return copy.deepcopy(FIXTURE["capabilities"])
            raise MiddlewareError(mode[0])
        runtime._http = http
        binding = runtime.recovery(Scope(**FIXTURE["request"]["scope"]))
        try:
            for _ in range(8):
                self.assertEqual(runtime.optimize(**inputs(binding)).reason, "epoch_changed")
            self.assertEqual(len(discovery), 1, "a 4xx decision keeps cached capabilities")
            mode[0] = "deadline"
            for _ in range(5):
                self.assertEqual(runtime.optimize(**inputs(binding)).reason, "deadline")
            self.assertEqual(len(discovery), 1, "a deadline does not clear capabilities")
            self.assertEqual(runtime.optimize(**inputs(binding)).reason, "circuit_open")
            self.assertEqual(runtime._breaker.state, "open")
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

    async def test_full_pool_is_capacity_and_stuck_io_is_deadline(self):
        # Workers == slots, so no job ever waits in a queue; a stuck worker is bounded by the outer deadline.
        runtime = AsyncMiddlewareRuntime(deadline_ms=50, max_concurrency=4)
        release = threading.Event()
        paths = []
        def blocked_discovery(path, *_):
            paths.append(path)
            release.wait(6)
            return copy.deepcopy(FIXTURE["capabilities"])
        runtime._runtime._http = blocked_discovery
        binding = runtime.recovery(Scope(**FIXTURE["request"]["scope"]))
        started = time.monotonic()
        tasks = [asyncio.create_task(runtime.optimize(**inputs(binding))) for _ in range(10)]
        outcomes = await asyncio.gather(*tasks)
        self.assertLess(time.monotonic() - started, 3)  # unbounded: 6 s
        release.set()
        self.assertTrue(all(out.status == "bypassed" for out in outcomes))
        self.assertEqual(sorted(out.reason for out in outcomes), ["capacity"] * 6 + ["deadline"] * 4)
        self.assertLessEqual(len(paths), 4)
        await runtime.aclose()
