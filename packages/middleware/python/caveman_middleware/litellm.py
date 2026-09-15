"""LiteLLM's native deployment callbacks and instance-scoped SDK call wrapper.

Install the callback after original-content policy hooks. A proxy scope resolver
receives the authenticated UserAPIKeyAuth object, never a model-supplied identity.
LiteLLM remains the sole inference hop and owns retries, fallbacks and streams.
"""
from __future__ import annotations

import contextlib
import contextvars
import threading
import time
import uuid
from collections import OrderedDict
from dataclasses import dataclass

try:
    import litellm as native
    from litellm.integrations.custom_logger import CustomLogger
except ModuleNotFoundError as error:
    raise ImportError("Install caveman-middleware[litellm] to use the LiteLLM adapter") from error

from caveman_cloud.middleware import MiddlewareError, MiddlewareRuntime, Scope
from ._native import Attempt, NativeSession, owner, plain
from ._versions import matches_framework
from ._usage import usage

_active = contextvars.ContextVar("caveman_litellm_request", default=None)
_sync_router = contextvars.ContextVar("caveman_litellm_sync_router", default=None)
_prepared_sync = contextvars.ContextVar("caveman_litellm_prepared_sync", default=None)
_registration_lock = threading.RLock()
_KEY = "caveman_middleware_registration"


@dataclass
class _Request:
    scope: Scope
    logical_id: str
    expires: float
    binding: object = None
    overhead: str | None = None
    protocol: str | None = None


class CavemanLiteLLM(CustomLogger):
    """Use as a context manager around a native LiteLLM module or Router.

    completion/acompletion/responses/aresponses take an explicit ``scope``.
    Model-only calls have no recovery executor. ``operator_recovery`` may bind
    an executor registered by an enclosing host; untrusted HTTP metadata never
    enables recovery. ``proxy_scope`` is required for proxy-originated requests.
    Registration is bounded, removable, and inert for unrelated SDK calls.
    """
    def __init__(self, *, runtime, client=native, proxy_scope=None, operator_recovery=None):
        super().__init__(turn_off_message_logging=True)
        # LiteLLM keys CustomLogger registrations by public scalar attributes.
        # Concurrent runtimes must not collapse into the first callback object.
        self.registration_id = uuid.uuid4().hex
        self.runtime, self.client = runtime, client
        self.async_runtime = runtime.as_async() if isinstance(runtime, MiddlewareRuntime) else runtime
        self.proxy_scope, self.operator_recovery = proxy_scope, operator_recovery
        self._requests, self._attempts = OrderedDict(), OrderedDict()
        self._lock, self._registrations = threading.RLock(), 0
        self._version_supported = matches_framework(("litellm", "1.100", "2"))
        if not self._version_supported and runtime.mode != "off":
            runtime.decline("unsupported_version")

    def __enter__(self):
        if not self._version_supported or (self.runtime.mode == "off" and self.proxy_scope is None):
            return self
        with _registration_lock:
            if self._registrations == 0:
                native.callbacks.append(self)
            self._registrations += 1
        return self

    def __exit__(self, *_):
        with _registration_lock:
            self._registrations = max(0, self._registrations - 1)
            if self._registrations == 0:
                native.logging_callback_manager.remove_callback_from_all_lists(self)
                native.logging_callback_manager.remove_callback_from_list_by_object(native.input_callback, self, require_self=False)

    def close(self):
        with _registration_lock:
            self._registrations = 1
            self.__exit__()
        with self._lock:
            self._requests.clear()
            self._attempts.clear()

    def _remember(self, scope, call_type=None):
        if not isinstance(scope, Scope):
            raise TypeError("LiteLLM scope must be a trusted Caveman Scope")
        request = _Request(scope, str(uuid.uuid4()), time.monotonic() + 3600, protocol=self._protocol(call_type))
        if self.operator_recovery:
            supplied = self.operator_recovery(scope)
            if supplied is not None:
                request.binding, request.overhead = supplied
                if not self.async_runtime.owns_binding(request.binding, scope):
                    raise ValueError("Recovery executor belongs to another runtime or scope")
        key = uuid.uuid4().hex
        with self._lock:
            now = time.monotonic()
            for old in list(self._requests):
                if self._requests[old].expires <= now:
                    del self._requests[old]
            while len(self._requests) >= 1024:
                self._requests.popitem(last=False)
            self._requests[key] = request
        return key, request

    def _request(self, kwargs):
        params = kwargs.get("litellm_params")
        params = params if plain(params) else {}
        with self._lock:
            for metadata in (kwargs.get("litellm_metadata"), kwargs.get("metadata"), params.get("litellm_metadata"), params.get("metadata")):
                key = metadata.get(_KEY) if plain(metadata) else None
                request = self._requests.get(key) if type(key) is str else None
                if request and request.expires > time.monotonic():
                    return key, request
        return None

    def _tag(self, kwargs, key, protocol):
        # Responses metadata belongs to the provider's public storage contract.
        # LiteLLM keeps its own routing/auth metadata in a separate native field.
        name = "litellm_metadata" if protocol == "openai-responses" else "metadata"
        metadata = kwargs.get(name)
        if metadata is not None and not plain(metadata):
            raise TypeError("LiteLLM metadata must be a native dictionary")
        return {**kwargs, name: {**(metadata or {}), _KEY: key}}

    def _report(self, reason, logical_id=None):
        return self.runtime.report(None, reason=reason, adapter="litellm",
                                   logical_call_id=logical_id or str(uuid.uuid4()), attempt_id=str(uuid.uuid4()))

    def _passive_reason(self, method, kwargs):
        if self.runtime.mode == "off":
            return "disabled"
        if not self._version_supported:
            return "unsupported_version"
        responses = method in ("responses", "aresponses")
        source = kwargs.get("input" if responses else "messages")
        if type(source) not in ((list, str) if responses else (list,)):
            # Native async preprocessing can normalize an opaque collection.
            # Preserve its public-call behavior without claiming ownership.
            return "unsupported_shape"
        return None

    async def async_pre_call_hook(self, user_api_key_dict, cache, data, call_type):
        if self.proxy_scope is None or owner.get() is not None:
            return data
        if not self._version_supported or self.runtime.mode == "off":
            self._report("unsupported_version")
            return data
        # The proxy invokes this public hook after its authentication. The
        # configured resolver must include virtual-key/team boundaries in scope.
        scope = self.proxy_scope(user_api_key_dict, data)
        if scope is None:
            self._report("scope_unavailable")
            return data
        key, _ = self._remember(scope, call_type)
        return self._tag(data, key, self._protocol(call_type))

    @staticmethod
    def _protocol(call_type):
        value = getattr(call_type, "value", call_type)
        return "openai-chat" if value in ("completion", "acompletion") else "openai-responses" if value in ("responses", "aresponses") else None

    def _session(self, request, call_type, runtime):
        protocol = self._protocol(call_type)
        if protocol is None:
            return None
        return NativeSession(runtime, request.scope, adapter_id="litellm", framework_version="1.100.0", protocol=protocol,
                             binding=request.binding, overhead=request.overhead, logical_call_id=request.logical_id)

    def _save_attempt(self, kwargs, attempt):
        call_id = kwargs.get("litellm_call_id")
        if not isinstance(call_id, str):
            return
        # The callback registry retains metadata only; no request or replacement
        # text survives in it while native streaming/logging callbacks run.
        if attempt.optimization and attempt.optimization.plan:
            attempt.plan_id = attempt.optimization.plan["replacement_set_id"]
        attempt.optimization = None
        with self._lock:
            while len(self._attempts) >= 2048:
                self._attempts.popitem(last=False)
            self._attempts[call_id] = attempt

    async def async_pre_call_deployment_hook(self, kwargs, call_type):
        if _prepared_sync.get() is self:
            return kwargs
        registered = self._request(kwargs)
        if registered is None or (owner.get() is not None and _active.get() != registered[0]):
            return kwargs
        if registered[1].protocol and registered[1].protocol != self._protocol(call_type):
            # Native Responses translation may call Chat Completions internally.
            # The outer Responses adapter owns that same provider request.
            return kwargs
        session = self._session(registered[1], call_type, self.async_runtime)
        if session is None:
            self._report("unsupported_method", registered[1].logical_id)
            return kwargs
        token = owner.set(None)
        try:
            body, attempt = await session.prepare_async(kwargs)
        finally:
            owner.reset(token)
        if attempt:
            attempt.observe("dispatch_intent")
            self._save_attempt(body, attempt)
        return body

    def log_pre_api_call(self, model, messages, kwargs):
        # LiteLLM 1.100.0 passes the actual translated request dictionary to
        # this public callback immediately before provider dispatch. A sync
        # Router has already selected its deployment here, including fallbacks.
        # Replace only its request-local messages list, never logging/history
        # messages or Router/client fields. Earlier policy callbacks see originals.
        if _sync_router.get() is not self:
            return
        registered = self._request(kwargs)
        if registered is None or _active.get() != registered[0]:
            return
        params = kwargs.get("litellm_params")
        additional = kwargs.get("additional_args")
        body = additional.get("complete_input_dict") if plain(additional) else None
        provider = params.get("custom_llm_provider") if plain(params) else None
        protocol = {"openai": "openai-chat", "anthropic": "anthropic-messages"}.get(provider)
        metadata = params.get("metadata") if plain(params) else None
        selected = metadata.get("deployment") if plain(metadata) else None
        if not (protocol and plain(body) and type(body.get("messages")) is list
                and type(selected) is str and selected.startswith(provider + "/")
                and body.get("model") == selected.removeprefix(provider + "/")):
            self._report("unsupported_shape", registered[1].logical_id)
            return
        request = registered[1]
        session = NativeSession(self.runtime, request.scope, adapter_id="litellm", framework_version="1.100.0", protocol=protocol,
                                binding=request.binding, overhead=request.overhead, logical_call_id=request.logical_id)
        token = owner.set(None)
        try:
            projected, attempt = session.prepare({**body, "model": selected})
        finally:
            owner.reset(token)
        body["messages"] = projected["messages"]
        if attempt:
            attempt.observe("dispatch_intent")
            self._save_attempt(kwargs, attempt)

    def _finish(self, data, response, event):
        call_id = data.get("litellm_call_id")
        with self._lock:
            attempt = self._attempts.pop(call_id, None) if isinstance(call_id, str) else None
        if attempt:
            attempt.observe(event, usage(getattr(response, "usage", None)) if event == "completed" else None)

    async def async_post_call_success_deployment_hook(self, request_data, response, call_type):
        self._finish(request_data, response, "completed")
        return response

    async def async_post_call_failure_deployment_hook(self, request_data, exception, call_type, fallback_depth=None):
        self._finish(request_data, None, "failed")

    def log_success_event(self, kwargs, response_obj, start_time, end_time):
        self._finish(kwargs, response_obj, "completed")

    async def async_log_success_event(self, kwargs, response_obj, start_time, end_time):
        self._finish(kwargs, response_obj, "completed")

    def log_failure_event(self, kwargs, response_obj, start_time, end_time):
        self._finish(kwargs, response_obj, "failed")

    async def async_log_failure_event(self, kwargs, response_obj, start_time, end_time):
        self._finish(kwargs, response_obj, "failed")

    @contextlib.contextmanager
    def _activation(self, scope, kwargs, method):
        key, request = self._remember(scope, method)
        active_token, owner_token = _active.set(key), owner.set(request)
        try:
            yield self._tag(kwargs, key, request.protocol), request
        finally:
            owner.reset(owner_token)
            _active.reset(active_token)

    def _sync(self, method, scope, kwargs):
        function = getattr(self.client, method)
        if owner.get() is not None:
            return function(**kwargs)
        passive_reason = self._passive_reason(method, kwargs)
        if passive_reason:
            attempt = Attempt(self.runtime, None, str(uuid.uuid4()), str(uuid.uuid4()), passive=True,
                              reason=passive_reason, adapter="litellm")
            attempt.observe("dispatch_intent")
            token = owner.set(attempt)
            try:
                return function(**kwargs)
            finally:
                owner.reset(token)
        if not isinstance(self.runtime, MiddlewareRuntime):
            raise TypeError("Synchronous LiteLLM calls require MiddlewareRuntime")
        router = isinstance(self.client, native.Router) and method == "completion"
        if router and self.runtime.strict:
            # Logging callbacks swallow their exceptions. Reject this capability
            # before calling the Router so strict failure cannot dispatch a request.
            self._report("unsupported_sync_router_strict")
            raise MiddlewareError("unsupported_sync_router_strict")
        if router and self._registrations == 0:
            self._report("not_registered")
            raise RuntimeError("Use CavemanLiteLLM as a context manager while sync Router calls and streams are active")
        with self._activation(scope, kwargs, method) as (params, request):
            if router:
                token = _sync_router.set(self)
                try:
                    return function(**params)
                finally:
                    _sync_router.reset(token)
            # Direct sync SDK calls already name the selected provider/model.
            session = self._session(request, method, self.runtime) if self.client is native else None
            token = owner.set(None)
            try:
                body, attempt = session.prepare(params) if session else (params, None)
            finally:
                owner.reset(token)
            if attempt:
                body = {**body, "litellm_call_id": str(uuid.uuid4())}
                attempt.observe("dispatch_intent")
                self._save_attempt(body, attempt)
            else:
                self._report("router_ownership", request.logical_id)
            prepared = _prepared_sync.set(self if attempt else None)
            try:
                try:
                    result = function(**body)
                except BaseException:
                    self._finish(body, None, "failed")
                    raise
            finally:
                _prepared_sync.reset(prepared)
            if not kwargs.get("stream"):
                self._finish(body, result, "completed")
            return result

    async def _async(self, method, scope, kwargs):
        function = getattr(self.client, method)
        if owner.get() is not None:
            return await function(**kwargs)
        passive_reason = self._passive_reason(method, kwargs)
        if passive_reason:
            attempt = Attempt(self.runtime, None, str(uuid.uuid4()), str(uuid.uuid4()), passive=True,
                              reason=passive_reason, adapter="litellm")
            attempt.observe("dispatch_intent")
            token = owner.set(attempt)
            try:
                return await function(**kwargs)
            finally:
                owner.reset(token)
        if self._registrations == 0:
            self._report("not_registered")
            raise RuntimeError("Use CavemanLiteLLM as a context manager while async calls and streams are active")
        with self._activation(scope, kwargs, method) as (params, _):
            return await function(**params)

    def completion(self, *, scope, **kwargs):
        return self._sync("completion", scope, kwargs)

    async def acompletion(self, *, scope, **kwargs):
        return await self._async("acompletion", scope, kwargs)

    def responses(self, *, scope, **kwargs):
        return self._sync("responses", scope, kwargs)

    async def aresponses(self, *, scope, **kwargs):
        return await self._async("aresponses", scope, kwargs)
