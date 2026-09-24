"""Instance-scoped integration through OpenAI's public client post method.

Works on openai 2.x (httpx) and 3.x (httpx2): the transport helpers follow the
installed SDK's own HTTP library, so an openai 2.x stack never needs httpx2.
"""
from __future__ import annotations

import functools
import asyncio
import copy
import json
from dataclasses import dataclass
from types import MappingProxyType
from urllib.parse import urlsplit

try:
    from openai import OpenAI, AsyncOpenAI, Stream, AsyncStream, DefaultHttpxClient, __version__
except ModuleNotFoundError as error:
    raise ImportError("Install caveman-middleware[openai] to use the OpenAI adapter") from error

from caveman_cloud.middleware import ensure_async, ensure_sync
from ._guard import fail_open, recovery, recovery_name_conflict
from ._httpx2 import flavour, sdk_flavour
from ._usage import usage
from ._native import NativeSession, owner, plain
from ._versions import COMPATIBILITY, family_gate, framework_state

ADAPTER_ID = "openai-sdk"
_http = flavour(sdk_flavour(DefaultHttpxClient))  # the HTTP library this openai release already imported
OpenAICall, observe_response = _http.OpenAICall, _http.observe_response
CavemanOpenAITransport, CavemanAsyncOpenAITransport = _http.CavemanOpenAITransport, _http.CavemanAsyncOpenAITransport


def _runtime_for(client, runtime):
    return ensure_async(runtime) if isinstance(client, AsyncOpenAI) else ensure_sync(runtime)


def with_caveman_openai(client, *, runtime, scope, transport=None, allow_stored_responses=False,
                        accept_framework_version=False, manifest_bytes=None):
    """Return the same native client class with native resources and helpers.

    Model-only: no recovery executor is registered, so in compress mode every
    call reports ``recovery_unbound`` and sends the original input. Use
    ``with_caveman_openai_tools`` (or a native agent integration) to compress.
    Supply the same CavemanOpenAITransport/CavemanAsyncOpenAITransport used by
    the client's public http_client to observe physical SDK retries. Without
    that explicit seam, receipts describe one native operation.
    """
    return _wrap(client, runtime=_runtime_for(client, runtime), scope=scope, transport=transport,
                 allow_stored_responses=allow_stored_responses, accept=accept_framework_version, manifest_bytes=manifest_bytes)


@dataclass(frozen=True)
class CavemanOpenAIToolLoop:
    """Native client and immutable dispatch map for an application-owned loop.

    Call the selected native Chat Completions or Responses API with ``tools``.
    Dispatch every client function call through ``functions[name](arguments)``.
    Async clients expose async recovery, so await its result in the native loop.
    Each tools read returns fresh native dictionaries; application state stays
    original and mutating a schema cannot mutate the private registration.
    """
    client: object
    functions: object
    _definitions: str

    @property
    def tools(self):
        return json.loads(self._definitions)


def with_caveman_openai_tools(client, *, runtime, scope, protocol, tools, functions, transport=None, allow_stored_responses=False,
                              accept_framework_version=False, manifest_bytes=None):
    """Bind real application dispatch without introducing a second scheduler.

    ``protocol`` is ``openai-chat`` or ``openai-responses``. ``tools`` contains
    the corresponding native definitions; ``functions`` maps each native tool
    name to a callable accepting the decoded arguments dictionary. A host tool
    already named ``caveman_retrieve`` keeps its name and recovery stays off
    (``recovery_name_conflict``, warned once). Responses calls compress only
    with ``store=False`` unless ``allow_stored_responses=True``.
    """
    if protocol not in ("openai-chat", "openai-responses"):
        raise ValueError("Expected openai-chat or openai-responses protocol")
    definitions = copy.deepcopy(list(tools))
    if not plain(functions) or any(type(name) is not str or not callable(fn) for name, fn in functions.items()):
        raise TypeError("functions must map native tool names to callables")
    names = []
    for definition in definitions:
        tool = definition.get("function") if plain(definition) and protocol == "openai-chat" else definition
        if not plain(definition) or definition.get("type") != "function" or not plain(tool) or type(tool.get("name")) is not str:
            raise TypeError("Expected native client function tool definitions")
        names.append(tool["name"])
    if len(set(names)) != len(names):
        raise ValueError("Duplicate native tool name")
    if set(names) != set(functions):
        raise ValueError("Every native function definition needs exactly one executor")
    runtime = _runtime_for(client, runtime)
    options = dict(runtime=runtime, scope=scope, transport=transport, allow_stored_responses=allow_stored_responses,
                   accept=accept_framework_version, manifest_bytes=manifest_bytes)
    conflict = "caveman_retrieve" in names
    if conflict and runtime.mode == "compress":
        recovery_name_conflict(runtime, ADAPTER_ID)
    supported = accept_framework_version or framework_state(*COMPATIBILITY["openai"].pins) != "unsupported"
    binding = recovery(runtime, scope) if runtime.mode == "compress" and supported and not conflict else None
    if binding is None:  # off/record, untested version, name conflict, or an unusable scope: recovery-free
        return CavemanOpenAIToolLoop(_wrap(client, **options), MappingProxyType(dict(functions)), json.dumps(definitions))
    tool = {"name": binding.name, "description": binding.description, "parameters": copy.deepcopy(binding.input_schema)}
    definition = {"type": "function", "function": tool} if protocol == "openai-chat" else {"type": "function", **tool}
    definitions.append(definition)
    registry = MappingProxyType({**functions, binding.name: binding.execute})
    registration = (protocol, binding, registry, registry[binding.name], json.dumps(definition, ensure_ascii=False, separators=(",", ":")))
    return CavemanOpenAIToolLoop(_wrap(client, registration=registration, **options), registry,
                                json.dumps(definitions, ensure_ascii=False, separators=(",", ":")))


def _wrap(client, *, runtime, scope, registration=None, transport=None, allow_stored_responses=False, accept=False, manifest_bytes=None):
    if not isinstance(client, (OpenAI, AsyncOpenAI)):
        raise TypeError("Expected an OpenAI or AsyncOpenAI client")
    is_async = isinstance(client, AsyncOpenAI)
    if transport is not None and not isinstance(transport, CavemanAsyncOpenAITransport if is_async else CavemanOpenAITransport):
        raise TypeError("Match the sync/async Caveman transport to the native client")
    version_supported = family_gate(runtime, "openai", ADAPTER_ID, accept)
    native = client.with_options()
    post = native.post
    sessions = {}
    for path, protocol in (("/chat/completions", "openai-chat"), ("/responses", "openai-responses")):
        bound = registration is not None and registration[0] == protocol
        sessions[path] = NativeSession(runtime, scope, adapter_id=ADAPTER_ID, framework_version=__version__, protocol=protocol,
                                      binding=registration[1] if bound else None, overhead=registration[4] if bound else None,
                                      is_registered=(lambda: registration[2].get(registration[1].name) is registration[3] and registration[1].execute is registration[3]) if bound else None,
                                      passive_reason=None if version_supported else "unsupported_version",
                                      allow_stored_responses=allow_stored_responses, manifest_bytes=manifest_bytes)
    passive_session = sessions["/chat/completions"]

    def select(path, kwargs):
        try:
            session = passive_session if runtime.mode == "off" or not version_supported else session_for(path, kwargs)
            return session, None if session else "unsupported_request"
        except Exception as error:  # Decision 4
            return None, fail_open(runtime, ADAPTER_ID, error)

    def session_for(path, kwargs):
        if not isinstance(path, str) or not plain(kwargs.get("options", {})) or kwargs.get("options", {}).get("extra_json") or kwargs.get("files"):
            return None
        target = urlsplit(path)
        if target.netloc and target.netloc != urlsplit(str(native.base_url)).netloc:
            return None
        headers = {str(key).lower(): value for key, value in native.default_headers.items()}
        headers.update({str(key).lower(): value for key, value in (kwargs.get("options", {}).get("headers") or {}).items()})
        if any(key in headers for key in ("digest", "content-digest", "content-md5", "signature", "signature-input", "x-amz-content-sha256", "content-encoding", "dpop")):
            return None
        if str(headers.get("authorization", "")).startswith(("AWS4-HMAC", "Signature ")):
            return None
        if "content-type" in headers and "application/json" not in str(headers["content-type"]):
            return None
        return sessions.get(urlsplit(path).path)

    def finish(result, attempt):
        try:
            if isinstance(result, (Stream, AsyncStream)):
                observe_response(result.response, attempt)
            elif hasattr(result, "http_response"):
                observe_response(result.http_response, attempt)
            else:
                attempt.observe("completed", usage(getattr(result, "usage", None)))
        except Exception as error:  # accounting never replaces the provider's result
            fail_open(runtime, ADAPTER_ID, error)
        return result

    @functools.wraps(post)
    def sync_post(path, **kwargs):
        session, reason = select(path, kwargs)
        body, attempt = session.prepare(kwargs.get("body")) if session else passive_session.passive(kwargs.get("body"), reason)
        if attempt is None:
            return post(path, **kwargs)
        if attempt.passive:
            attempt.observe("dispatch_intent")
            token = owner.set(attempt)
            try:
                return post(path, **kwargs)
            finally:
                owner.reset(token)
        call = OpenAICall(attempt, transport) if transport is not None else None
        if call is None:
            attempt.observe("dispatch_intent")
        token = owner.set(call or attempt)
        try:
            result = post(path, **{**kwargs, "body": body})
            if call is not None and call.attempt_count:
                return result
            if call is not None:
                attempt.observe("dispatch_intent")
            return finish(result, attempt)
        except BaseException:
            if call is None or not call.attempt_count:
                if call is not None:
                    attempt.observe("dispatch_intent")
                attempt.observe("failed")
            raise
        finally:
            owner.reset(token)

    @functools.wraps(post)
    async def async_post(path, **kwargs):
        session, reason = select(path, kwargs)
        body, attempt = await session.prepare_async(kwargs.get("body")) if session else passive_session.passive(kwargs.get("body"), reason)
        if attempt is None:
            return await post(path, **kwargs)
        if attempt.passive:
            attempt.observe("dispatch_intent")
            token = owner.set(attempt)
            try:
                return await post(path, **kwargs)
            finally:
                owner.reset(token)
        call = OpenAICall(attempt, transport) if transport is not None else None
        if call is None:
            attempt.observe("dispatch_intent")
        token = owner.set(call or attempt)
        try:
            result = await post(path, **{**kwargs, "body": body})
            if call is not None and call.attempt_count:
                return result
            if call is not None:
                attempt.observe("dispatch_intent")
            return finish(result, attempt)
        except asyncio.CancelledError:
            if call is None or not call.attempt_count:
                if call is not None:
                    attempt.observe("dispatch_intent")
                attempt.observe("cancelled")
            raise
        except BaseException:
            if call is None or not call.attempt_count:
                if call is not None:
                    attempt.observe("dispatch_intent")
                attempt.observe("failed")
            raise
        finally:
            owner.reset(token)

    native.post = async_post if is_async else sync_post
    clone = native.copy
    @functools.wraps(clone)
    def copy_client(**kwargs):
        return _wrap(clone(**kwargs), runtime=runtime, scope=scope, registration=registration, transport=transport,
                     allow_stored_responses=allow_stored_responses, accept=accept, manifest_bytes=manifest_bytes)
    native.copy = native.with_options = copy_client
    return native
