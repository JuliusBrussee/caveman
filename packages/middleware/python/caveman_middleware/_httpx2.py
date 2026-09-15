"""Pull-driven response accounting for the pinned native provider SDK transport."""
from __future__ import annotations

import httpx2
import asyncio
import hashlib
import uuid

from ._native import Attempt, owner


from ._usage import UsageReader


class OpenAICall:
    """One prepared native operation and its observed physical HTTP attempts."""
    def __init__(self, attempt, transport):
        self.attempt, self.transport, self.attempt_count = attempt, transport, 0

    def next_attempt(self, request):
        original = self.attempt
        attempt = original if self.attempt_count == 0 else Attempt(original.runtime, original.scope,
            original.logical_call_id, str(uuid.uuid4()), optimization=original.optimization, plan_id=original.plan_id)
        self.attempt_count += 1
        try:
            attempt.wire_sha256 = hashlib.sha256(request.content).hexdigest()
        except httpx2.RequestNotRead:
            pass
        return attempt


class CavemanOpenAITransport(httpx2.BaseTransport):
    """Wrap an application-owned HTTPX2 transport before client construction.

    Pass this same object as ``transport`` to ``with_caveman_openai``. Requests
    outside that native wrapper pass through. The supplied transport retains its
    connection pool, TLS/proxy settings, and close behavior; this adds no retries.
    """
    def __init__(self, transport):
        if not isinstance(transport, httpx2.BaseTransport):
            raise TypeError("Expected an HTTPX2 BaseTransport")
        self.transport = transport

    def handle_request(self, request):
        call = owner.get()
        if not isinstance(call, OpenAICall) or call.transport is not self:
            return self.transport.handle_request(request)
        attempt = call.next_attempt(request)
        attempt.observe("dispatch_intent")
        token = owner.set(attempt)
        try:
            response = self.transport.handle_request(request)
            observe_response(response, attempt)
            return response
        except BaseException:
            attempt.observe("failed")
            raise
        finally:
            owner.reset(token)

    def close(self):
        self.transport.close()


class CavemanAsyncOpenAITransport(httpx2.AsyncBaseTransport):
    """Async counterpart of CavemanOpenAITransport, using the native pool."""
    def __init__(self, transport):
        if not isinstance(transport, httpx2.AsyncBaseTransport):
            raise TypeError("Expected an HTTPX2 AsyncBaseTransport")
        self.transport = transport

    async def handle_async_request(self, request):
        call = owner.get()
        if not isinstance(call, OpenAICall) or call.transport is not self:
            return await self.transport.handle_async_request(request)
        attempt = call.next_attempt(request)
        attempt.observe("dispatch_intent")
        token = owner.set(attempt)
        try:
            response = await self.transport.handle_async_request(request)
            observe_response(response, attempt)
            return response
        except (asyncio.CancelledError, GeneratorExit):
            attempt.observe("cancelled")
            raise
        except BaseException:
            attempt.observe("failed")
            raise
        finally:
            owner.reset(token)

    async def aclose(self):
        await self.transport.aclose()


class SyncObservedStream(httpx2.SyncByteStream):
    def __init__(self, original, attempt, parser):
        self.original, self.attempt, self.parser, self.ended = original, attempt, parser, False

    def finish(self, event):
        if not self.ended:
            self.ended = True
            self.attempt.observe(event, self.parser.finish() if event == "completed" else None)

    def __iter__(self):
        iterator = iter(self.original)
        try:
            while True:
                token = owner.set(self.attempt)
                try:
                    chunk = next(iterator)
                except StopIteration:
                    self.finish("completed")
                    return
                finally:
                    owner.reset(token)
                self.parser.feed(chunk)
                yield chunk
        except GeneratorExit:
            self.finish("completed" if self.parser.terminal else "cancelled")
            raise
        except BaseException:
            self.finish("failed")
            raise

    def close(self):
        try:
            self.original.close()
        finally:
            self.finish("completed" if self.parser.terminal else "cancelled")


class AsyncObservedStream(httpx2.AsyncByteStream):
    def __init__(self, original, attempt, parser):
        self.original, self.attempt, self.parser, self.ended = original, attempt, parser, False

    def finish(self, event):
        if not self.ended:
            self.ended = True
            self.attempt.observe(event, self.parser.finish() if event == "completed" else None)

    async def __aiter__(self):
        iterator = self.original.__aiter__()
        try:
            while True:
                token = owner.set(self.attempt)
                try:
                    chunk = await anext(iterator)
                except StopAsyncIteration:
                    self.finish("completed")
                    return
                finally:
                    owner.reset(token)
                self.parser.feed(chunk)
                yield chunk
        except GeneratorExit:
            self.finish("completed" if self.parser.terminal else "cancelled")
            raise
        except asyncio.CancelledError:
            self.finish("cancelled")
            raise
        except BaseException:
            self.finish("failed")
            raise

    async def aclose(self):
        try:
            await self.original.aclose()
        finally:
            self.finish("completed" if self.parser.terminal else "cancelled")


def observe_response(response, attempt):
    if not isinstance(response, httpx2.Response):
        return
    if response.status_code >= 400:
        attempt.observe("failed")
        return
    content_type = response.headers.get("content-type", "")
    if "json" not in content_type and "text/event-stream" not in content_type:
        attempt.observe("completed")
        return
    parser = UsageReader("text/event-stream" in content_type)
    if response.is_stream_consumed:
        parser.feed(response.content)
        attempt.observe("completed", parser.finish())
    elif isinstance(response.stream, httpx2.SyncByteStream):
        response.stream = SyncObservedStream(response.stream, attempt, parser)
    elif isinstance(response.stream, httpx2.AsyncByteStream):
        response.stream = AsyncObservedStream(response.stream, attempt, parser)
