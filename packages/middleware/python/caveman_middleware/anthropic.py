"""Anthropic 1.x public per-attempt middleware and native tool runners.

Also covers AnthropicBedrock/AnthropicVertex: they share the Messages body and
rewrite the URL and sign only after the middleware chain.
"""
from __future__ import annotations

import functools
import asyncio
import json
import uuid
from dataclasses import dataclass
from urllib.parse import urlsplit

from ._versions import framework_import_failed

try:
    import anthropic as _sdk
    from anthropic import Anthropic, AsyncAnthropic, DefaultHttpxClient, __version__
except ImportError as error:
    framework_import_failed("anthropic", error, "Install caveman-middleware[anthropic] to use the Anthropic adapter")
try:
    from anthropic import Middleware, APIRequest
    from anthropic.lib.tools import BetaBuiltinFunctionTool, BetaAsyncBuiltinFunctionTool, ToolError
except ImportError:  # anthropic without public middleware: the version gate declines before these are used
    Middleware = BetaBuiltinFunctionTool = BetaAsyncBuiltinFunctionTool = object
    ToolError = Exception

from caveman_cloud.middleware import MiddlewareError, ensure_async, ensure_sync
from ._guard import fail_open, recovery_failed, recovery_name_conflict
from ._httpx2 import flavour, sdk_flavour
from ._native import NativeSession, owner, plain
from ._versions import family_gate

ADAPTER_ID = "anthropic-sdk"
SYNC_CLIENTS = tuple(c for c in (Anthropic, getattr(_sdk, "AnthropicBedrock", None), getattr(_sdk, "AnthropicVertex", None)) if c)
ASYNC_CLIENTS = tuple(c for c in (AsyncAnthropic, getattr(_sdk, "AsyncAnthropicBedrock", None), getattr(_sdk, "AsyncAnthropicVertex", None)) if c)


def observe_response(response, attempt):
    flavour(sdk_flavour(DefaultHttpxClient)).observe_response(response, attempt)


def _append(client, middleware):
    """``client.with_middleware(middleware)`` that keeps what AnthropicBedrock.copy() drops (anthropic 1.x): the
    caller's http_client (proxy/TLS settings) and ``aws_profile``, without which requests are SigV4-signed by
    the default credential chain, a different AWS identity. AnthropicVertex.copy() keeps its credentials."""
    profile = getattr(client, "aws_profile", None)
    return client.copy(middleware=[*client.middleware, middleware], http_client=getattr(client, "_client", None),
                       **({"_extra_kwargs": {"aws_profile": profile}} if profile is not None else {}))


class CavemanAnthropicMiddleware(Middleware):
    """Append after the application's authorization and original-content guards."""
    def __init__(self, runtime, scope, *, accept_framework_version=False, manifest_bytes=None,
                 _binding=None, _overhead=None, _logical_call_id=None, _is_registered=None):
        supported = family_gate(runtime, "anthropic", ADAPTER_ID, accept_framework_version)
        self.session = NativeSession(runtime, scope, adapter_id=ADAPTER_ID, framework_version=__version__, protocol="anthropic-messages",
                                     binding=_binding, overhead=_overhead, logical_call_id=_logical_call_id, is_registered=_is_registered,
                                     passive_reason=None if supported else "unsupported_version", manifest_bytes=manifest_bytes)

    @staticmethod
    def _messages_call(request):
        """Only a Messages call is ever eligible; any other endpoint passes through without a report."""
        try:
            return request.method.upper() == "POST" and urlsplit(request.url).path in ("/v1/messages", "/messages")
        except Exception:
            return False

    @staticmethod
    def eligible(request: APIRequest):
        if request.method.upper() != "POST" or urlsplit(request.url).path not in ("/v1/messages", "/messages"):
            return False
        headers = {k.lower(): v for k, v in request.headers.items()}
        if any(k in headers for k in ("digest", "content-digest", "content-md5", "signature", "signature-input", "x-amz-content-sha256", "content-encoding", "dpop")):
            return False
        if str(headers.get("authorization", "")).startswith(("AWS4-HMAC", "Signature ")):
            return False
        if "content-type" in headers and "application/json" not in headers["content-type"]:
            return False
        # Extra JSON is merged later by the SDK. Do not optimize an earlier view
        # when later overrides could change its messages, tools or model.
        return not request.options.extra_json and plain(request.json)

    def _passive(self, request):
        try:
            if self.session.runtime.mode == "off" or self.session.passive_reason or not self.eligible(request):
                return self.session.passive_reason or "unsupported_request"
            return None
        except Exception as error:  # Decision 4
            return fail_open(self.session.runtime, ADAPTER_ID, error)

    def _observe(self, result, attempt):
        try:
            if not attempt.passive:
                observe_response(result.http_response, attempt)
        except Exception as error:
            fail_open(self.session.runtime, ADAPTER_ID, error)

    def handle(self, request, call_next):
        if not self._messages_call(request):
            return call_next(request)
        reason = self._passive(request)
        body, attempt = self.session.passive(request.json, reason) if reason else self.session.prepare(request.json)
        if attempt is None:
            return call_next(request)
        attempt.observe("dispatch_intent")
        token = owner.set(attempt)
        try:
            result = call_next(request.copy(body=body) if body is not request.json else request)
            self._observe(result, attempt)
            return result
        except BaseException:
            attempt.observe("failed")
            raise
        finally:
            owner.reset(token)

    async def handle_async(self, request, call_next):
        if not self._messages_call(request):
            return await call_next(request)
        reason = self._passive(request)
        body, attempt = self.session.passive(request.json, reason) if reason else await self.session.prepare_async(request.json)
        if attempt is None:
            return await call_next(request)
        attempt.observe("dispatch_intent")
        token = owner.set(attempt)
        try:
            result = await call_next(request.copy(body=body) if body is not request.json else request)
            self._observe(result, attempt)
            return result
        except asyncio.CancelledError:
            attempt.observe("cancelled")
            raise
        except BaseException:
            attempt.observe("failed")
            raise
        finally:
            owner.reset(token)


@dataclass(frozen=True)
class _RecoveryTool(BetaBuiltinFunctionTool):
    binding: object
    definition: str
    execute: object

    def to_dict(self):
        return json.loads(self.definition)

    def call(self, input):
        try:
            page = self.execute(input)
        except MiddlewareError as error:  # the runner's native is_error result, without its traceback log
            raise ToolError(json.dumps(recovery_failed(ADAPTER_ID, error))) from error
        return json.dumps(page, ensure_ascii=False, separators=(",", ":"))


@dataclass(frozen=True)
class _AsyncRecoveryTool(BetaAsyncBuiltinFunctionTool):
    binding: object
    definition: str
    execute: object

    def to_dict(self):
        return json.loads(self.definition)

    async def call(self, input):
        try:
            page = await self.execute(input)
        except MiddlewareError as error:
            raise ToolError(json.dumps(recovery_failed(ADAPTER_ID, error))) from error
        return json.dumps(page, ensure_ascii=False, separators=(",", ":"))


def with_caveman_anthropic(client, *, runtime, scope, accept_framework_version=False, manifest_bytes=None):
    """Return a native SDK clone. Existing native middleware stays in order.

    Plain ``messages.create`` calls are recovery-free (``recovery_unbound`` in
    compress mode); ``beta.messages.tool_runner`` registers the recovery tool
    and compresses. Anthropic, AnthropicBedrock and AnthropicVertex clients
    (sync and async) are supported.
    """
    if not isinstance(client, SYNC_CLIENTS + ASYNC_CLIENTS):
        raise TypeError("Expected an Anthropic, AnthropicBedrock or AnthropicVertex client (sync or async)")
    async_client = isinstance(client, ASYNC_CLIENTS)
    runtime = ensure_async(runtime) if async_client else ensure_sync(runtime)
    options = dict(accept_framework_version=accept_framework_version, manifest_bytes=manifest_bytes)
    middleware = CavemanAnthropicMiddleware(runtime, scope, **options)
    if middleware.session.passive_reason or runtime.mode == "off":
        return _append(client, middleware) if Middleware is not object else client
    native = _append(client, middleware)
    original_runner = native.beta.messages.tool_runner

    @functools.wraps(original_runner)
    def tool_runner(**params):
        supplied = params.get("tools")
        if runtime.mode != "compress" or not isinstance(supplied, (list, tuple)):
            return original_runner(**params)
        names = [(t.get("name") if plain(t) else getattr(t, "name", None)) for t in supplied]
        if "caveman_retrieve" in names:
            recovery_name_conflict(runtime, ADAPTER_ID)
        if any(type(name) is not str or not name for name in names) or len(set(names)) != len(names) or "caveman_retrieve" in names:
            return original_runner(**params)
        binding = runtime.recovery(scope)
        if binding is None:  # unusable scope (warned by the SDK): recovery-free
            return original_runner(**params)
        definition = json.dumps({"name": binding.name, "description": binding.description, "input_schema": binding.input_schema}, ensure_ascii=False, separators=(",", ":"))
        recovery = (_AsyncRecoveryTool if async_client else _RecoveryTool)(binding, definition, binding.execute)
        execute = recovery.execute
        native_call = type(recovery).call
        middleware = CavemanAnthropicMiddleware(runtime, scope, **options, _binding=binding,
            _overhead=definition, _logical_call_id=str(uuid.uuid4()),
            _is_registered=lambda: recovery.execute is execute and binding.execute is execute and type(recovery).call is native_call and recovery.to_dict() == json.loads(definition))
        # An invocation-owned native clone has the attested real tool. The SDK
        # keeps its lazy iterator, context managers, parameters, and scheduler.
        runner_client = _append(client, middleware)
        return runner_client.beta.messages.tool_runner(**{**params, "tools": [*supplied, recovery]})

    native.beta.messages.tool_runner = tool_runner
    clone = client.copy
    @functools.wraps(clone)
    def copy_client(**kwargs):
        return with_caveman_anthropic(clone(**kwargs), runtime=runtime, scope=scope, **options)
    native.copy = native.with_options = copy_client
    append = client.with_middleware
    @functools.wraps(append)
    def append_middleware(*middleware):
        return with_caveman_anthropic(append(*middleware), runtime=runtime, scope=scope, **options)
    native.with_middleware = append_middleware
    return native
