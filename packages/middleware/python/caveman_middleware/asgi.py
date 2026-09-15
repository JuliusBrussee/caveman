"""Pure ASGI projection for explicit LLM routes, after host authentication.

No ASGI server, request class, inference client, or recovery executor is owned
here. The operator supplies an authenticated scope and, optionally, the binding
already executed by its application tool loop. HTTP headers cannot enable it.
"""
from __future__ import annotations

import asyncio
from collections import deque
from collections.abc import Awaitable, Callable, Mapping
from dataclasses import dataclass
import inspect
import json
import uuid
from typing import Any, Literal

from caveman_cloud.middleware import AsyncMiddlewareRuntime, Scope

from ._native import Attempt, NativeSession, owner, plain
from ._versions import matches_framework
from ._usage import UsageReader

Protocol = Literal["openai-chat", "openai-responses", "anthropic-messages"]
ASGIScope = dict[str, Any]


@dataclass(frozen=True)
class ASGIContext:
    """Returned by server-side auth, never deserialized from caller input.

    recovery is a binding registered with the host's actual native tool loop.
    Omitting it is safe: no lossy result can be sent downstream.
    """
    scope: Scope
    recovery: Any = None
    recovery_overhead: str | None = None


def _unique_object(pairs):
    result = {}
    for key, value in pairs:
        if key in result:
            raise ValueError("duplicate JSON key")
        result[key] = value
    return result


def _reject_constant(_value):
    raise ValueError("non-finite JSON")


class CavemanASGIMiddleware:
    """Place inside authentication and original-content guard middleware.

    resolve_context may decline by returning None; existing application routing
    and auth then run unchanged. It must use authenticated server state, not
    namespace/session headers. This middleware never handles an auth rejection.
    """
    def __init__(self, app, *, runtime: AsyncMiddlewareRuntime,
                 routes: Mapping[str, Protocol],
                 resolve_context: Callable[[ASGIScope], ASGIContext | None | Awaitable[ASGIContext | None]],
                 max_body_bytes: int = 2 << 20, max_request_chunks: int = 256):
        if not isinstance(runtime, AsyncMiddlewareRuntime):
            raise TypeError("ASGI requires AsyncMiddlewareRuntime")
        if not routes or any(not isinstance(path, str) or not path.startswith("/") or "*" in path or "?" in path
                             or protocol not in ("openai-chat", "openai-responses", "anthropic-messages") for path, protocol in routes.items()):
            raise ValueError("Configure exact POST paths and native LLM protocols")
        if not callable(resolve_context) or not 1 <= max_body_bytes <= 2 << 20 or not 1 <= max_request_chunks <= 4096:
            raise ValueError("ASGI context resolver or request bounds are invalid")
        self.app, self.runtime = app, runtime
        self.routes, self.resolve_context = dict(routes), resolve_context
        self.max_body_bytes, self.max_request_chunks = max_body_bytes, max_request_chunks
        self._version_supported = matches_framework(("fastapi", "0.141", "1"), ("starlette", "1.6", "2"))
        if not self._version_supported and runtime.mode != "off":
            runtime.decline("unsupported_version")

    async def __call__(self, scope, receive, send):
        if owner.get() is not None:
            return await self.app(scope, receive, send)

        protocol = self.routes.get(scope.get("path"))
        async def passthrough(reader, reason):
            if scope.get("type") != "http" or scope.get("method") != "POST" or protocol is None:
                self.runtime.report(None, reason=reason, adapter="asgi")
                return await self.app(scope, reader, send)
            # This exact inference route still owns its native request when
            # projection is disabled or declined. A nested adapter must not
            # transform protected content or report the same decision twice.
            attempt = Attempt(self.runtime, None, str(uuid.uuid4()), str(uuid.uuid4()),
                              passive=True, reason=reason, adapter="asgi")
            attempt.observe("dispatch_intent")
            token = owner.set(attempt)
            try:
                return await self.app(scope, reader, send)
            finally:
                owner.reset(token)

        if not self._version_supported or self.runtime.mode == "off":
            return await passthrough(receive, "unsupported_version")
        if scope.get("type") != "http" or scope.get("method") != "POST" or protocol is None:
            return await passthrough(receive, "unsupported_endpoint")
        headers = scope.get("headers", [])
        protected = {b"content-encoding", b"digest", b"content-digest", b"content-md5", b"signature", b"signature-input", b"x-amz-content-sha256"}
        if any(key.lower() in protected for key, _ in headers):
            return await passthrough(receive, "protected_request")
        types = [value.lower().split(b";", 1)[0].strip() for key, value in headers if key.lower() == b"content-type"]
        lengths = [value for key, value in headers if key.lower() == b"content-length"]
        if types != [b"application/json"] or len(lengths) > 1:
            return await passthrough(receive, "unsupported_shape")
        if lengths:
            try:
                if not 0 <= int(lengths[0]) <= self.max_body_bytes:
                    return await passthrough(receive, "payload_limit")
            except ValueError:
                return await passthrough(receive, "unsupported_shape")

        context = self.resolve_context(scope)
        if inspect.isawaitable(context):
            context = await context
        if context is None:
            return await passthrough(receive, "scope_unavailable")
        if not isinstance(context, ASGIContext):
            raise TypeError("ASGI context must come from authenticated server state")

        buffered, parts, size = deque(), [], 0

        async def replay():
            return buffered.popleft() if buffered else await receive()

        while True:
            message = await receive()
            buffered.append(message)
            if message.get("type") != "http.request" or set(message) - {"type", "body", "more_body"}:
                return await passthrough(replay, "request_interrupted")
            body = message.get("body", b"")
            if type(body) is not bytes:
                return await passthrough(replay, "unsupported_shape")
            size += len(body)
            if size > self.max_body_bytes or len(buffered) > self.max_request_chunks:
                return await passthrough(replay, "payload_limit")
            parts.append(body)
            if not message.get("more_body", False):
                break
        try:
            native = json.loads(b"".join(parts).decode("utf-8"), object_pairs_hook=_unique_object, parse_constant=_reject_constant)
        except (ValueError, UnicodeError, RecursionError):
            return await passthrough(replay, "unsupported_shape")
        if not plain(native) or type(native.get("model")) is not str:
            return await passthrough(replay, "unsupported_shape")
        session = NativeSession(self.runtime, context.scope, adapter_id="asgi", framework_version="3.0",
                                protocol=protocol, binding=context.recovery, overhead=context.recovery_overhead)
        projected, attempt = await session.prepare_async(native)
        if attempt is None:
            return await self.app(scope, replay, send)
        if projected is not native:
            wire = json.dumps(projected, ensure_ascii=False, separators=(",", ":"), allow_nan=False).encode("utf-8")
            # These framing headers describe the replacement body. All other
            # headers and scope metadata retain their original values/order.
            scope = {**scope, "headers": [(key, value) for key, value in headers if key.lower() not in (b"content-length", b"transfer-encoding")]
                     + [(b"content-length", str(len(wire)).encode("ascii"))]}
            buffered.clear()
            buffered.append({"type": "http.request", "body": wire, "more_body": False})
        # Drop temporary body/JSON references before a long response stream.
        parts.clear()
        del native, projected
        if attempt.optimization and attempt.optimization.plan:
            attempt.plan_id = attempt.optimization.plan["replacement_set_id"]
        attempt.observe("dispatch_intent")
        attempt.optimization = None
        if attempt.passive:
            token = owner.set(attempt)
            try:
                return await self.app(scope, replay, send)
            finally:
                owner.reset(token)
        parser, ended, failed = None, False, False

        async def observed_send(message):
            nonlocal parser, ended, failed
            if message.get("type") == "http.response.start":
                failed = message.get("status", 500) >= 400
                content_types = [v.lower() for k, v in message.get("headers", []) if k.lower() == b"content-type"]
                if len(content_types) == 1 and (b"json" in content_types[0] or b"text/event-stream" in content_types[0]):
                    parser = UsageReader(b"text/event-stream" in content_types[0], max_buffer=65536)
            elif message.get("type") == "http.response.body" and parser is not None:
                parser.feed(message.get("body", b""))
            # Forward each message on the host's pull/backpressure schedule.
            await send(message)
            if message.get("type") == "http.response.body" and not message.get("more_body", False):
                ended = True
                attempt.observe("failed" if failed else "completed", parser.finish() if parser and not failed else None)

        token = owner.set(attempt)
        try:
            return await self.app(scope, replay, observed_send)
        except asyncio.CancelledError:
            if not ended:
                attempt.observe("cancelled")
                ended = True
            raise
        except BaseException:
            if not ended:
                attempt.observe("failed")
                ended = True
            raise
        finally:
            owner.reset(token)
            if not ended:
                attempt.observe("cancelled")
