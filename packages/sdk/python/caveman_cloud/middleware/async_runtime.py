"""Explicit asynchronous API with bounded, lifecycle-owned stdlib I/O workers."""
import asyncio
import contextvars
import functools
import threading
import time
from concurrent.futures import ThreadPoolExecutor

from .runtime import MiddlewareRuntime
from .types import MiddlewareError, Optimization, Scope


class AsyncMiddlewareRuntime:
    def __init__(self, **options):
        self._runtime = MiddlewareRuntime(**options)
        self._owns_runtime = True
        self._init_workers()

    def _init_workers(self):
        self._executor = ThreadPoolExecutor(max_workers=4, thread_name_prefix="caveman-middleware")
        self._slots = threading.BoundedSemaphore(16)
        self._closed = False

    @classmethod
    def from_sync(cls, runtime: MiddlewareRuntime):
        """Bounded async view sharing the existing connection's authority/state.

        Prefer runtime.as_async() for a cached view closed by its sync owner.
        Closing this view does not close the caller-owned synchronous runtime.
        """
        instance = cls.__new__(cls)
        instance._runtime, instance._owns_runtime = runtime, False
        instance._init_workers()
        return instance

    def _shutdown_now(self):
        self._closed = True
        self._executor.shutdown(wait=False, cancel_futures=True)

    @property
    def mode(self):
        return self._runtime.mode

    @property
    def endpoint(self):
        return self._runtime.endpoint

    async def ready(self):
        return await self._submit(self._runtime.ready)

    def recovery(self, scope: Scope):
        async def execute(args=None, **kwargs):
            return await self.retrieve(scope, **(args or kwargs))
        return self._runtime._new_binding(scope, execute)

    def owns_binding(self, binding, scope):
        return self._runtime.owns_binding(binding, scope)

    def decline(self, reason):
        return self._runtime.decline(reason)

    @property
    def last_report(self):
        return self._runtime.last_report

    def report(self, optimization=None, **context):
        return self._runtime.report(optimization, **context)

    async def optimize(self, **options):
        # The deadline begins before the executor queue. Cancelling this await
        # propagates to the host; it never falls back into a fresh model call.
        options["_deadline_at"] = time.monotonic() + self._runtime.deadline_ms / 1000
        try:
            return await self._submit(self._runtime.optimize, **options)
        except MiddlewareError as error:
            return self._runtime._bypass(error.code)

    async def retrieve(self, scope, **args):
        return await self._submit(self._runtime.retrieve, scope, **args)

    async def observe(self, receipt):
        try:
            await self._submit(self._runtime.observe, receipt)
        except MiddlewareError:
            pass

    def observe_background(self, receipt):
        return self._runtime.observe_background(receipt)

    async def delete_session(self, scope):
        return await self._submit(self._runtime.delete_session, scope)

    async def aclose(self):
        self._closed = True
        if self._owns_runtime:
            self._runtime.close()
        await asyncio.to_thread(self._executor.shutdown, wait=True, cancel_futures=True)

    async def __aenter__(self):
        return self

    async def __aexit__(self, *_):
        await self.aclose()

    async def _submit(self, function, *args, **kwargs):
        if self._closed:
            raise MiddlewareError("closed")
        if not self._slots.acquire(blocking=False):
            raise MiddlewareError("capacity")
        context = contextvars.copy_context()
        try:
            future = self._executor.submit(context.run, functools.partial(function, *args, **kwargs))
        except BaseException:
            self._slots.release()
            raise
        # Release only when the underlying worker actually terminates. Releasing
        # on coroutine cancellation would allow unlimited queued/running work.
        future.add_done_callback(lambda _: self._slots.release())
        return await asyncio.wrap_future(future)
