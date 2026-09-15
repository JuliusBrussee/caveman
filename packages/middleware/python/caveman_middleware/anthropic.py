"""Anthropic 1.4 public per-attempt middleware and native tool runners."""
from __future__ import annotations

import functools
import asyncio
import json
import uuid
from dataclasses import dataclass
from urllib.parse import urlsplit

try:
    from anthropic import Anthropic, AsyncAnthropic, Middleware, APIRequest, __version__
    from anthropic.lib.tools import BetaBuiltinFunctionTool, BetaAsyncBuiltinFunctionTool
except ModuleNotFoundError as error:
    raise ImportError("Install caveman-middleware[anthropic] to use the Anthropic adapter") from error

from caveman_cloud.middleware import MiddlewareRuntime, AsyncMiddlewareRuntime
from ._httpx2 import observe_response
from ._native import NativeSession, owner, plain
from ._versions import in_range


class CavemanAnthropicMiddleware(Middleware):
    """Append after the application's authorization and original-content guards."""
    def __init__(self, runtime, scope, *, _binding=None, _overhead=None, _logical_call_id=None, _is_registered=None):
        self.session = NativeSession(runtime, scope, adapter_id="anthropic-sdk", framework_version="1.4.0", protocol="anthropic-messages",
                                     binding=_binding, overhead=_overhead, logical_call_id=_logical_call_id, is_registered=_is_registered,
                                     passive_reason=None if in_range(__version__, "1.4", "2") else "unsupported_version")

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

    def handle(self, request, call_next):
        passive = self.session.runtime.mode == "off" or self.session.passive_reason or not self.eligible(request)
        body, attempt = self.session.passive(request.json, self.session.passive_reason or "unsupported_request") if passive else self.session.prepare(request.json)
        if attempt is None:
            return call_next(request)
        attempt.observe("dispatch_intent")
        token = owner.set(attempt)
        try:
            result = call_next(request.copy(body=body) if body is not request.json else request)
            if not attempt.passive:
                observe_response(result.http_response, attempt)
            return result
        except BaseException:
            attempt.observe("failed")
            raise
        finally:
            owner.reset(token)

    async def handle_async(self, request, call_next):
        passive = self.session.runtime.mode == "off" or self.session.passive_reason or not self.eligible(request)
        body, attempt = self.session.passive(request.json, self.session.passive_reason or "unsupported_request") if passive else await self.session.prepare_async(request.json)
        if attempt is None:
            return await call_next(request)
        attempt.observe("dispatch_intent")
        token = owner.set(attempt)
        try:
            result = await call_next(request.copy(body=body) if body is not request.json else request)
            if not attempt.passive:
                observe_response(result.http_response, attempt)
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
        return json.dumps(self.execute(input), ensure_ascii=False, separators=(",", ":"))


@dataclass(frozen=True)
class _AsyncRecoveryTool(BetaAsyncBuiltinFunctionTool):
    binding: object
    definition: str
    execute: object

    def to_dict(self):
        return json.loads(self.definition)

    async def call(self, input):
        return json.dumps(await self.execute(input), ensure_ascii=False, separators=(",", ":"))


def with_caveman_anthropic(client, *, runtime, scope):
    """Return a native SDK clone. Existing native middleware stays in order."""
    if not isinstance(client, (Anthropic, AsyncAnthropic)):
        raise TypeError("Expected an Anthropic or AsyncAnthropic client")
    async_client = isinstance(client, AsyncAnthropic)
    if not isinstance(runtime, AsyncMiddlewareRuntime if async_client else MiddlewareRuntime):
        raise TypeError("Match the sync/async middleware runtime to the native client")
    version_supported = in_range(__version__, "1.4", "2")
    if not version_supported and runtime.mode != "off":
        runtime.decline("unsupported_version")
    native = client.with_middleware(CavemanAnthropicMiddleware(runtime, scope))
    if not version_supported or runtime.mode == "off":
        return native
    original_runner = native.beta.messages.tool_runner

    @functools.wraps(original_runner)
    def tool_runner(**params):
        supplied = params.get("tools")
        if runtime.mode != "compress" or not isinstance(supplied, (list, tuple)):
            return original_runner(**params)
        names = [(t.get("name") if plain(t) else getattr(t, "name", None)) for t in supplied]
        if any(type(name) is not str or not name for name in names) or len(set(names)) != len(names) or "caveman_retrieve" in names:
            return original_runner(**params)
        binding = runtime.recovery(scope)
        definition = json.dumps({"name": binding.name, "description": binding.description, "input_schema": binding.input_schema}, ensure_ascii=False, separators=(",", ":"))
        recovery = (_AsyncRecoveryTool if async_client else _RecoveryTool)(binding, definition, binding.execute)
        execute = recovery.execute
        native_call = type(recovery).call
        middleware = CavemanAnthropicMiddleware(runtime, scope, _binding=binding,
            _overhead=definition, _logical_call_id=str(uuid.uuid4()),
            _is_registered=lambda: recovery.execute is execute and binding.execute is execute and type(recovery).call is native_call and recovery.to_dict() == json.loads(definition))
        # An invocation-owned native clone has the attested real tool. The SDK
        # keeps its lazy iterator, context managers, parameters, and scheduler.
        runner_client = client.with_middleware(middleware)
        return runner_client.beta.messages.tool_runner(**{**params, "tools": [*supplied, recovery]})

    native.beta.messages.tool_runner = tool_runner
    clone = client.copy
    @functools.wraps(clone)
    def copy_client(**kwargs):
        return with_caveman_anthropic(clone(**kwargs), runtime=runtime, scope=scope)
    native.copy = native.with_options = copy_client
    append = client.with_middleware
    @functools.wraps(append)
    def append_middleware(*middleware):
        return with_caveman_anthropic(append(*middleware), runtime=runtime, scope=scope)
    native.with_middleware = append_middleware
    return native
