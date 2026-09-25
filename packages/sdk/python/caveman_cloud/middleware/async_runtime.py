"""Explicit asynchronous API with bounded, lifecycle-owned stdlib I/O workers (experimental API).

Every await is bounded by its own deadline, even while queued or when a worker is stuck. Retrieve has its
own pool, so slow recoveries never delay optimize. Pools are rebuilt in a forked child.
"""
import asyncio
import contextvars
import functools
import threading
import time
from concurrent.futures import ThreadPoolExecutor

from .protocol import normalize_scope
from .runtime import MiddlewareRuntime, _live, _preflight_report
from .types import MiddlewareError, Scope


def _local(code: str) -> MiddlewareError:
    error = MiddlewareError(code)
    error.local = True  # decided by this view, not by the sync runtime's strict policy
    return error


class AsyncMiddlewareRuntime:
    def __init__(self, **options):
        self._runtime = MiddlewareRuntime(**options)
        self._owns_runtime = True
        self._closed = False
        self._init_workers()
        _live.add(self)

    def _init_workers(self):
        size = self._runtime.max_concurrency
        self._executor = ThreadPoolExecutor(max_workers=size, thread_name_prefix="caveman-middleware")
        self._retrieve_executor = ThreadPoolExecutor(max_workers=size, thread_name_prefix="caveman-retrieve")
        self._slots = threading.BoundedSemaphore(size)
        self._retrieve_slots = threading.BoundedSemaphore(size)

    def _after_fork(self):
        self._init_workers()  # inherited workers are dead in the child; their executors would never run a job

    @classmethod
    def from_sync(cls, runtime: MiddlewareRuntime):
        """Bounded async view sharing the existing connection's authority/state.

        Prefer runtime.as_async() for a cached view closed by its sync owner.
        Closing this view does not close the caller-owned synchronous runtime.
        """
        instance = cls.__new__(cls)
        instance._runtime, instance._owns_runtime, instance._closed = runtime, False, False
        instance._init_workers()
        _live.add(instance)
        return instance

    def as_sync(self) -> MiddlewareRuntime:
        """The underlying synchronous runtime, for sync code paths that must never receive coroutines."""
        return self._runtime

    def as_async(self) -> "AsyncMiddlewareRuntime":
        """Identity; lets adapters call as_async() on whichever runtime they were given."""
        return self

    def _shutdown_now(self):
        # No cancel_futures: a cancelled job would raise CancelledError into its caller. It runs, sees closed, bypasses.
        self._closed = True
        self._executor.shutdown(wait=False)
        self._retrieve_executor.shutdown(wait=False)

    @property
    def mode(self):
        return self._runtime.mode

    @property
    def endpoint(self):
        return self._runtime.endpoint

    @property
    def strict(self):
        return self._runtime.strict

    def _optimize_s(self) -> float:
        return self._runtime._deadlines()[0] / 1000

    def _retrieve_s(self) -> float:
        return self._runtime._deadlines()[1] / 1000

    async def ready(self):
        return await self._submit(self._optimize_s(), False, self._runtime.ready)

    async def preflight(self):
        """Nonthrowing discovery; caller cancellation still propagates."""
        if self.mode == "off" and not self._runtime._config_error:
            return _preflight_report(self.mode, "disabled")
        try:
            return await self._submit(self._optimize_s(), False, self._runtime.preflight)
        except Exception as error:
            return _preflight_report(self.mode, error.code if isinstance(error, MiddlewareError) else "runtime_unavailable")

    def recovery(self, scope: Scope):
        normalized = normalize_scope(scope)
        if normalized is None:
            return self._runtime._no_scope()

        async def execute(args=None, **kwargs):
            return await self.retrieve(normalized, **(args or kwargs))
        return self._runtime._new_binding(normalized, execute)

    def owns_binding(self, binding, scope):
        return self._runtime.owns_binding(binding, scope)

    def decline(self, reason, adapter=None):
        return self._runtime.decline(reason, adapter)

    @property
    def last_report(self):
        return self._runtime.last_report

    def report(self, optimization=None, **context):
        return self._runtime.report(optimization, **context)

    async def optimize(self, **options):
        # The deadline begins before the executor queue and bounds the whole await, so a stuck or
        # dead worker can never hold the host call. Cancelling this await propagates to the host.
        started = time.monotonic()
        budget = self._optimize_s()
        options["_deadline_at"] = started + budget
        try:
            return await self._submit(budget, False, self._runtime.optimize, **options)
        except MiddlewareError as error:
            if self.strict and not getattr(error, "local", False):
                raise  # the sync runtime already applied its strict policy
            return self._runtime._bypass(error.code, adapter=getattr(options.get("adapter"), "id", None), started=started)

    async def retrieve(self, scope, **args):
        return await self._submit(self._retrieve_s(), True, self._runtime.retrieve, scope, **args)

    async def observe(self, receipt):
        try:
            await self._submit(self._optimize_s(), False, self._runtime.observe, receipt)
        except MiddlewareError:
            pass

    def observe_background(self, receipt):
        return self._runtime.observe_background(receipt)

    async def delete_session(self, scope):
        return await self._submit(self._retrieve_s(), True, self._runtime.delete_session, scope)

    async def aclose(self):
        # Never joins the workers: each in-flight await ends at its own deadline and queued jobs finish with a
        # `closed` bypass instead of CancelledError. A view of a caller-owned runtime cannot abort that runtime's
        # transport, so a join could wait on a stuck custom transport indefinitely.
        self._shutdown_now()
        if self._owns_runtime:
            self._runtime.close()

    async def __aenter__(self):
        return self

    async def __aexit__(self, *_):
        await self.aclose()

    def _run(self, function, *args, **kwargs):
        if self._closed:
            raise _local("closed")
        return function(*args, **kwargs)

    async def _submit(self, timeout: float, retrieve: bool, function, *args, **kwargs):
        if self._closed:
            raise _local("closed")
        executor, slots = (self._retrieve_executor, self._retrieve_slots) if retrieve else (self._executor, self._slots)
        if not slots.acquire(blocking=False):
            raise _local("capacity")
        context = contextvars.copy_context()
        try:
            future = executor.submit(context.run, functools.partial(self._run, function, *args, **kwargs))
        except RuntimeError:  # shut down between the closed check and submit
            slots.release()
            raise _local("closed") from None
        except BaseException:
            slots.release()
            raise
        # Release only when the underlying worker actually terminates. Releasing
        # on coroutine cancellation would allow unlimited queued/running work.
        future.add_done_callback(lambda _: slots.release())
        try:
            return await asyncio.wait_for(asyncio.wrap_future(future), timeout)
        except TimeoutError:
            raise _local("deadline") from None


def ensure_sync(runtime):
    """Sync view of either runtime type, so sync code paths never receive coroutines. Other objects pass through."""
    return runtime.as_sync() if isinstance(runtime, (MiddlewareRuntime, AsyncMiddlewareRuntime)) else runtime


def ensure_async(runtime):
    """Async view of either runtime type. Other objects pass through."""
    return runtime.as_async() if isinstance(runtime, (MiddlewareRuntime, AsyncMiddlewareRuntime)) else runtime
