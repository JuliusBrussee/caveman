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

    async def test_observe_burst_does_not_starve_concurrent_optimize(self):
        # observe() carries receipts, which are never on the provider's critical
        # path. Sharing optimize()'s executor lets a receipt burst hold every
        # worker until an unrelated optimize() has already spent its deadline.
        # max_concurrency must match the receipt burst below: with the default
        # 16 workers, 4 blocked receipts could not starve the optimize pool even
        # if they shared it, and the test would pass without proving anything.
        runtime = AsyncMiddlewareRuntime(deadline_ms=200, max_concurrency=4)
        release = threading.Event()
        self.addCleanup(release.set)
        entered = []
        def blocked_receipts(path, *_):
            if path == "receipts":
                entered.append(path)
                release.wait(5)
                return {}
            return copy.deepcopy(FIXTURE["capabilities"])
        runtime._runtime._http = blocked_receipts
        # The receipt needs a normalizable scope or _receipt() drops it before
        # _deliver(), observe() no-ops, and nothing ever reaches the seam above.
        scope = FIXTURE["request"]["scope"]
        receipts = [asyncio.create_task(runtime.observe({"scope": scope, "n": i})) for i in range(4)]
        # Bounded on purpose: if observe() stops routing through the receipt
        # pool, this test must fail in seconds, not hang until the job timeout.
        deadline = time.monotonic() + 5
        while not entered:
            self.assertLess(time.monotonic(), deadline, "no receipt reached the HTTP seam")
            await asyncio.sleep(0.001)
        # Every receipt has had its chance to occupy a shared worker before
        # optimize() is submitted; CPU scheduling cannot turn this into a race.
        await asyncio.sleep(0.05)
        result = await runtime.optimize(**inputs())
        # Both starvation shapes must be excluded, not just the slow one: a
        # shared pool shows up as `capacity` when every slot is already held and
        # as `deadline` when the burst outlasts optimize()'s own window.
        self.assertNotIn(result.reason, ("capacity", "deadline"),
                         "a receipt burst must not starve an unrelated optimize()")
        release.set()
        await asyncio.gather(*receipts)
        await runtime.aclose()

    async def test_shutdown_from_another_thread_never_leaks_a_bare_runtimeerror(self):
        # guarded_submit blocks the event loop thread, so the trigger below
        # must run on a plain OS thread, not an awaited coroutine.
        runtime = AsyncMiddlewareRuntime()
        entered_submit, release_submit = threading.Event(), threading.Event()
        real_submit = runtime._pools["optimize"][0].submit

        def guarded_submit(*args, **kwargs):
            entered_submit.set()
            release_submit.wait(2)
            return real_submit(*args, **kwargs)

        runtime._pools["optimize"][0].submit = guarded_submit

        def trigger_shutdown_mid_submit():
            entered_submit.wait(2)
            shutdown_thread = threading.Thread(target=runtime._shutdown_now)
            shutdown_thread.start()
            # Do not wait for shutdown_thread here: the fix makes it block on
            # the lock _submit holds until release_submit fires below.
            time.sleep(0.05)
            release_submit.set()
            shutdown_thread.join(2)

        watcher = threading.Thread(target=trigger_shutdown_mid_submit)
        watcher.start()
        try:
            result = await runtime._submit(1.0, "optimize", lambda: "ok")
        except MiddlewareError as error:
            self.assertEqual(error.code, "closed")
        else:
            self.assertEqual(result, "ok")
        watcher.join(2)

    async def test_shutdown_completing_between_the_two_checks_still_raises_closed_and_frees_the_slot(self):
        # Forces _shutdown_now to complete strictly between _submit's two
        # checks, deterministically, by closing from inside the slot acquire.
        runtime = AsyncMiddlewareRuntime()
        real_acquire = runtime._pools["optimize"][1].acquire

        def acquire_then_close_first(*args, **kwargs):
            runtime._shutdown_now()
            return real_acquire(*args, **kwargs)

        runtime._pools["optimize"][1].acquire = acquire_then_close_first
        with self.assertRaisesRegex(MiddlewareError, "closed"):
            await runtime._submit(1.0, "optimize", lambda: "ok")
        # The failed submit must not have leaked the slot it acquired.
        reacquired = [runtime._pools["optimize"][1].acquire(blocking=False) for _ in range(16)]
        self.assertTrue(all(reacquired))
        for _ in range(16):
            runtime._pools["optimize"][1].release()

    async def test_receipt_pool_shares_the_same_shutdown_guard(self):
        # observe() runs on its own executor/semaphore pair, so the guard has
        # to live in _submit rather than on the optimize() pool alone.
        runtime = AsyncMiddlewareRuntime()
        real_acquire = runtime._pools["receipt"][1].acquire

        def acquire_then_close_first(*args, **kwargs):
            runtime._shutdown_now()
            return real_acquire(*args, **kwargs)

        runtime._pools["receipt"][1].acquire = acquire_then_close_first
        with self.assertRaisesRegex(MiddlewareError, "closed"):
            await runtime._submit(1.0, "receipt", lambda: "ok")
        reacquired = [runtime._pools["receipt"][1].acquire(blocking=False) for _ in range(16)]
        self.assertTrue(all(reacquired))
        for _ in range(16):
            runtime._pools["receipt"][1].release()

    async def test_aclose_closes_the_receipt_pool_under_the_same_guard(self):
        # aclose() wrote _closed directly, outside the closed check _submit rechecks
        # under, so it opened the identical window _shutdown_now() closes.
        runtime = AsyncMiddlewareRuntime()
        real_acquire = runtime._pools["optimize"][1].acquire
        closed = []

        def acquire_then_aclose(*args, **kwargs):
            if not closed:
                closed.append(True)
                done = threading.Event()
                threading.Thread(
                    target=lambda: (asyncio.run(runtime.aclose()), done.set())).start()
                done.wait(3)
            return real_acquire(*args, **kwargs)

        runtime._pools["optimize"][1].acquire = acquire_then_aclose
        with self.assertRaisesRegex(MiddlewareError, "closed"):
            await runtime._submit(1.0, "optimize", lambda: "ok")
