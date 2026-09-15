"""Google GenAI 2.22 native HTTP transports and native AFC registration.

Install the transport through HttpOptions.httpx_client/httpx_async_client. Wrap
the existing client or chat to register the recovery executor in its native AFC
loop. The SDK keeps provider auth, Vertex configuration, retries and history.
"""
import codecs
import contextvars
import functools
import json
import re
import uuid
from urllib.parse import urlsplit

try:
    import httpx
    from google import genai
    from google.genai import types
    from google.genai.chats import Chat, AsyncChat
except ModuleNotFoundError as error:
    raise ImportError("Install caveman-middleware[google] to use the Google adapter") from error

from caveman_cloud.middleware import Adapter, Candidate, MiddlewareRuntime, AsyncMiddlewareRuntime, sha256
from ._native import Attempt, manifest, owner, plain
from ._versions import supports_framework
from ._usage import usage
from ._google_wire import parse, patch

_invocation = contextvars.ContextVar("caveman_google_invocation", default=None)


def _get(value, name, default=None):
    if plain(value):
        pieces = name.split("_")
        alias = pieces[0] + "".join(piece.title() for piece in pieces[1:])
        return value.get(name, value.get(alias, default))
    return getattr(value, name, default)


def _permits(config):
    choice = _get(_get(config, "tool_config"), "function_calling_config")
    return not (_get(config, "cached_content") or _get(config, "response_schema") or _get(config, "response_json_schema")) and _get(config, "response_mime_type") in (None, "text/plain") and _get(choice, "mode") in (None, "AUTO", types.FunctionCallingConfigMode.AUTO)


def _bind(config, runtime, scope, *, asynchronous=False):
    context = {"binding": None, "logical_call_id": str(uuid.uuid4()), "streams": set()}
    if _invocation.get() is not None:
        return config, _invocation.get()
    if type(config) not in (dict, types.GenerateContentConfig):
        return config, context
    tools = _get(config, "tools")
    afc = _get(config, "automatic_function_calling")
    maximum = _get(afc, "maximum_remote_calls")
    if runtime.mode != "compress" or owner.get() is not None or not isinstance(tools, (list, tuple)) or not any(callable(t) for t in tools) or _get(afc, "disable") or (maximum is not None and maximum <= 0) or not _permits(config):
        return config, context
    for tool in tools:
        if callable(tool) and getattr(tool, "__name__", None) == "caveman_retrieve":
            return config, context
        if not callable(tool) and _get(tool, "function_declarations"):
            # Preserve the SDK's mixed callable/declaration handling.
            return config, context
    binding = runtime.recovery(scope)

    def caveman_retrieve(handle: str, offset: int = 0, limit: int = 262144, query: str = "") -> dict:
        return binding.execute({"handle": handle, "offset": offset, "limit": limit, **({"query": query} if query else {})})

    async def async_retrieve(handle: str, offset: int = 0, limit: int = 262144, query: str = "") -> dict:
        return await binding.execute({"handle": handle, "offset": offset, "limit": limit, **({"query": query} if query else {})})

    function = async_retrieve if asynchronous else caveman_retrieve
    function.__name__, function.__doc__ = binding.name, binding.description
    # The SDK generates the tool declaration from this exact Python callable.
    # Its native schema is separately attested; runtime argument validation is
    # still authoritative for limits and handle syntax.
    declarations = [types.FunctionDeclaration.from_callable_with_api_option(callable=function, api_option=api, use_json_schema=True).model_dump(mode="json", exclude_none=True) for api in ("GEMINI_API", "VERTEX_AI")]
    context.update(binding=binding, declarations=declarations, overhead=json.dumps(declarations[0], ensure_ascii=False, separators=(",", ":")))
    changed = {"tools": [*tools, function]}
    return ({**config, **changed} if plain(config) else config.model_copy(update=changed)), context


def _scope_iterator(iterator, context):
    try:
        while True:
            token = _invocation.set(context)
            try:
                value = next(iterator)
            except StopIteration:
                return
            finally:
                _invocation.reset(token)
            yield value
    finally:
        if hasattr(iterator, "close"):
            iterator.close()
        for stream in list(context.get("streams", ())):
            if isinstance(stream, _SyncStream):
                stream.close()


async def _scope_async_iterator(iterator, context):
    try:
        while True:
            token = _invocation.set(context)
            try:
                value = await anext(iterator)
            except StopAsyncIteration:
                return
            finally:
                _invocation.reset(token)
            yield value
    finally:
        if hasattr(iterator, "aclose"):
            await iterator.aclose()
        for stream in list(context.get("streams", ())):
            if isinstance(stream, _AsyncStream):
                await stream.aclose()


def _wrap_model(module, runtime, scope, asynchronous=False):
    generate, stream = module.generate_content, module.generate_content_stream
    if asynchronous:
        @functools.wraps(generate)
        async def generate_content(*, model, contents, config=None):
            config, context = _bind(config, runtime, scope, asynchronous=True)
            token = _invocation.set(context)
            try:
                return await generate(model=model, contents=list(contents) if type(contents) is list else contents, config=config)
            finally:
                _invocation.reset(token)

        @functools.wraps(stream)
        async def generate_content_stream(*, model, contents, config=None):
            config, context = _bind(config, runtime, scope, asynchronous=True)
            token = _invocation.set(context)
            try:
                result = await stream(model=model, contents=list(contents) if type(contents) is list else contents, config=config)
            finally:
                _invocation.reset(token)
            return _scope_async_iterator(result, context)
    else:
        @functools.wraps(generate)
        def generate_content(*, model, contents, config=None):
            config, context = _bind(config, runtime, scope)
            token = _invocation.set(context)
            try:
                return generate(model=model, contents=list(contents) if type(contents) is list else contents, config=config)
            finally:
                _invocation.reset(token)

        @functools.wraps(stream)
        def generate_content_stream(*, model, contents, config=None):
            config, context = _bind(config, runtime, scope)
            return _scope_iterator(stream(model=model, contents=list(contents) if type(contents) is list else contents, config=config), context)
    module.generate_content, module.generate_content_stream = generate_content, generate_content_stream


def with_caveman_google(client, *, runtime, scope):
    """Register native model AFC on an existing, transport-configured Client.

    This is instance-scoped registration on public methods. Construct the client
    with the transports below; existing auth and client options remain native.
    Use with_caveman_google_chat for a Chat, which owns a separate AFC loop.
    """
    if not isinstance(client, genai.Client) or not isinstance(runtime, MiddlewareRuntime):
        raise TypeError("Expected a Google Client and synchronous MiddlewareRuntime")
    if not supports_framework(runtime, ("google-genai", "2.22", "3")):
        return client
    _wrap_model(client.models, runtime, scope)
    _wrap_model(client.aio.models, runtime.as_async(), scope, True)
    return client


def with_caveman_google_chat(chat, *, runtime, scope, config=None):
    """Register recovery in an existing native Chat/AsyncChat's own AFC loop.

    Pass the original default config here, or supply config on each send. No
    private chat configuration/history is read or modified.
    """
    asynchronous = isinstance(chat, AsyncChat)
    if not isinstance(chat, (Chat, AsyncChat)):
        raise TypeError("Expected a native Google Chat or AsyncChat")
    if asynchronous and isinstance(runtime, MiddlewareRuntime):
        runtime = runtime.as_async()
    if not isinstance(runtime, AsyncMiddlewareRuntime if asynchronous else MiddlewareRuntime):
        raise TypeError("Match the sync/async runtime to the chat")
    if not supports_framework(runtime, ("google-genai", "2.22", "3")):
        return chat
    send, stream, default = chat.send_message, chat.send_message_stream, config
    if asynchronous:
        @functools.wraps(send)
        async def send_message(message, config=None):
            selected, context = _bind(config if config is not None else default, runtime, scope, asynchronous=True)
            token = _invocation.set(context)
            try:
                return await send(message, config=selected)
            finally:
                _invocation.reset(token)

        @functools.wraps(stream)
        async def send_message_stream(message, config=None):
            selected, context = _bind(config if config is not None else default, runtime, scope, asynchronous=True)
            token = _invocation.set(context)
            try:
                result = await stream(message, config=selected)
            finally:
                _invocation.reset(token)
            return _scope_async_iterator(result, context)
    else:
        @functools.wraps(send)
        def send_message(message, config=None):
            selected, context = _bind(config if config is not None else default, runtime, scope)
            token = _invocation.set(context)
            try:
                return send(message, config=selected)
            finally:
                _invocation.reset(token)

        @functools.wraps(stream)
        def send_message_stream(message, config=None):
            selected, context = _bind(config if config is not None else default, runtime, scope)
            return _scope_iterator(stream(message, config=selected), context)
    chat.send_message, chat.send_message_stream = send_message, send_message_stream
    return chat


def _selected(body, strings):
    leaves, names = [], set()
    for ci, content in enumerate(body["contents"]):
        if not plain(content) or type(content.get("parts")) is not list:
            continue
        for part in content["parts"]:
            if plain(part) and plain(part.get("functionCall")) and type(part["functionCall"].get("name")) is str:
                names.add(part["functionCall"]["name"])
        for pi, part in enumerate(content["parts"]):
            if not plain(part) or part.get("thought") or part.get("thoughtSignature") or not plain(part.get("functionResponse")):
                continue
            result = part["functionResponse"]
            if type(result.get("name")) is not str or result["name"] not in names or result["name"] == "caveman_retrieve" or not plain(result.get("response")) or "error" in result["response"]:
                continue
            for key in ("output", "result"):
                path = ("contents", ci, "parts", pi, "functionResponse", "response", key)
                if type(result["response"].get(key)) is str and path in strings:
                    leaves.append((path, strings[path]))
    return leaves


def _prepare(request, runtime, scope):
    if owner.get() is not None or request.method != "POST" or not re.search(r":(?:generateContent|streamGenerateContent)$", request.url.path):
        return None
    context = _invocation.get() or {}
    attempt = Attempt(runtime, scope, context.get("logical_call_id") or str(uuid.uuid4()), str(uuid.uuid4()),
                      adapter="google-sdk", reason="opaque_payload")
    if runtime.mode == "off" or not supports_framework(runtime, ("google-genai", "2.22", "3")):
        attempt.passive = True
        attempt.reason = "disabled" if runtime.mode == "off" else "unsupported_version"
        return attempt, None
    if any(name in request.headers for name in ("content-encoding", "digest", "content-digest", "content-md5", "signature", "signature-input", "x-amz-content-sha256", "dpop")):
        return attempt, None
    try:
        if len(request.content) > 2 << 20:
            return attempt, None
        text = request.content.decode("utf-8")
    except (httpx.RequestNotRead, UnicodeError):
        return attempt, None
    parsed = parse(text)
    if not parsed:
        return attempt, None
    body, strings = parsed
    if not plain(body) or type(body.get("contents")) is not list:
        return attempt, None
    if body.get("cachedContent"):
        attempt.reason = "opaque_history_reference"
        return attempt, None
    history = manifest([{key: value for key, value in body.items() if key != "contents"}, *body["contents"]])
    if history is None:
        return attempt, None
    binding = context.get("binding")
    generation = body.get("generationConfig") or {}
    tool_config, tools = body.get("toolConfig") or {}, body.get("tools") or []
    if not plain(generation) or not plain(tool_config) or type(tools) is not list:
        return attempt, None
    choice = tool_config.get("functionCallingConfig") or {}
    if not plain(choice):
        return attempt, None
    mode = choice.get("mode")
    declarations = [d for t in tools if plain(t) and type(t.get("functionDeclarations")) is list for d in t["functionDeclarations"] if plain(d) and d.get("name") == "caveman_retrieve"]
    if not (runtime.owns_binding(binding, scope) and len(declarations) == 1 and declarations[0] in context.get("declarations", []) and mode in (None, "AUTO") and not generation.get("responseSchema") and not generation.get("responseJsonSchema") and generation.get("responseMimeType") in (None, "text/plain")):
        binding = None
    leaves = _selected(body, strings)
    options = dict(scope=scope, adapter=Adapter("google-sdk", "0.1.0", "2.22.0", "google-genai-wire-v1"), manifest=history,
        model={"provider": "google", "id": re.sub(r":(?:generateContent|streamGenerateContent)$", "", request.url.path), "protocol": "google-genai"},
        candidates=[Candidate(id=f"leaf-{i}", source_id="/".join(map(str, path)), content=leaf[2]) for i, (path, leaf) in enumerate(leaves)],
        binding=binding, recovery_overhead_text=context.get("overhead"), logical_call_id=attempt.logical_call_id, attempt_id=attempt.attempt_id)
    return attempt, (text, leaves, options)


def _apply(request, state, outcome):
    attempt, plan = state
    outgoing = request
    if attempt.passive:
        return outgoing
    if plan and outcome:
        text, leaves, options = plan
        if attempt.runtime.mode == "off" or (options["binding"] is not None and
                not attempt.runtime.owns_binding(options["binding"], attempt.scope)):
            attempt.reason = "recovery_unavailable"
            return outgoing
        attempt.optimization = outcome if not outcome.replacements else None
        attempt.reason = "invalid_plan" if outcome.replacements else outcome.reason
        mapping = {f"leaf-{i}": leaf for i, (_, leaf) in enumerate(leaves)}
        if outcome.replacements and all(r["segment_id"] in mapping for r in outcome.replacements):
            changed = patch(text, [(mapping[r["segment_id"]], r["text"]) for r in outcome.replacements])
            if changed is not None:
                content = changed.encode("utf-8")
                headers = request.headers.copy()
                if "content-length" in headers:
                    headers["content-length"] = str(len(content))
                outgoing = httpx.Request(request.method, request.url, headers=headers, content=content, extensions=request.extensions)
                attempt.optimization = outcome
    try:
        attempt.wire_sha256 = sha256(outgoing.content.decode("utf-8"))
    except (httpx.RequestNotRead, UnicodeError):
        pass
    return outgoing


class _Usage:
    def __init__(self, sse):
        self.sse, self.text, self.last, self.overflow = sse, "", None, False
        self.terminal = False
        self.decoder = codecs.getincrementaldecoder("utf-8")("replace")

    def event(self, value):
        if plain(value) and plain(value.get("usageMetadata")):
            self.last = value["usageMetadata"]
        if plain(value) and type(value.get("candidates")) is list and any(plain(c) and c.get("finishReason") for c in value["candidates"]):
            self.terminal = True

    def feed(self, chunk):
        if self.overflow:
            return
        self.text += self.decoder.decode(chunk)
        if len(self.text) > (262144 if self.sse else 2 << 20):
            self.text, self.overflow = "", True
            return
        if self.sse:
            while "\n" in self.text:
                line, self.text = self.text.split("\n", 1)
                if line.startswith("data:"):
                    try:
                        self.event(json.loads(line[5:]))
                    except (TypeError, ValueError):
                        pass

    def finish(self):
        if not self.sse and not self.overflow:
            try:
                self.event(json.loads(self.text))
            except (TypeError, ValueError):
                pass
        return None if self.overflow or self.last is None else usage(self.last, not self.sse or self.terminal)


class _Observed:
    def __init__(self, stream, attempt, sse):
        self.stream, self.attempt, self.parser, self.ended = stream, attempt, _Usage(sse), False
        self.resources = (_invocation.get() or {}).get("streams")
        if self.resources is not None:
            self.resources.add(self)

    def finish(self, event):
        if not self.ended:
            self.ended = True
            if self.resources is not None:
                self.resources.discard(self)
            self.attempt.observe(event, self.parser.finish() if event == "completed" else None)


class _SyncStream(_Observed, httpx.SyncByteStream):
    def __iter__(self):
        try:
            for chunk in self.stream:
                self.parser.feed(chunk)
                yield chunk
            self.finish("completed")
        except GeneratorExit:
            self.finish("cancelled")
            raise
        except BaseException:
            self.finish("failed")
            raise
        finally:
            self.close()

    def close(self):
        try:
            self.stream.close()
        finally:
            self.finish("cancelled")


class _AsyncStream(_Observed, httpx.AsyncByteStream):
    async def __aiter__(self):
        try:
            async for chunk in self.stream:
                self.parser.feed(chunk)
                yield chunk
            self.finish("completed")
        except BaseException:
            self.finish("cancelled")
            raise
        finally:
            await self.aclose()

    async def aclose(self):
        try:
            await self.stream.aclose()
        finally:
            self.finish("cancelled")


class CavemanGoogleTransport(httpx.BaseTransport):
    """Public HTTPX transport; original-content request hooks run before it."""
    def __init__(self, *, runtime, scope, provider_base_url="https://generativelanguage.googleapis.com", transport=None):
        if not isinstance(runtime, MiddlewareRuntime):
            raise TypeError("Use MiddlewareRuntime with the synchronous transport")
        self.runtime, self.scope, self.transport = runtime, scope, transport or httpx.HTTPTransport()
        self.base = urlsplit(provider_base_url)

    def handle_request(self, request):
        url = urlsplit(str(request.url))
        if (url.scheme, url.netloc) != (self.base.scheme, self.base.netloc) or not url.path.startswith(self.base.path.rstrip("/") + "/"):
            return self.transport.handle_request(request)
        state = _prepare(request, self.runtime, self.scope)
        if state is None:
            return self.transport.handle_request(request)
        attempt, plan = state
        outgoing = _apply(request, state, self.runtime.optimize(**plan[2]) if plan else None)
        attempt.observe("dispatch_intent")
        token = owner.set(attempt)
        try:
            response = self.transport.handle_request(outgoing)
            if response.status_code >= 400:
                attempt.observe("failed")
            else:
                response.stream = _SyncStream(response.stream, attempt, "text/event-stream" in response.headers.get("content-type", ""))
            return response
        except BaseException:
            attempt.observe("failed")
            raise
        finally:
            owner.reset(token)

    def close(self):
        self.transport.close()


class CavemanGoogleAsyncTransport(httpx.AsyncBaseTransport):
    """Async HTTPX transport with the runtime's bounded async execution path."""
    def __init__(self, *, runtime, scope, provider_base_url="https://generativelanguage.googleapis.com", transport=None):
        if isinstance(runtime, MiddlewareRuntime):
            runtime = runtime.as_async()
        if not isinstance(runtime, AsyncMiddlewareRuntime):
            raise TypeError("Use AsyncMiddlewareRuntime with the asynchronous transport")
        self.runtime, self.scope, self.transport = runtime, scope, transport or httpx.AsyncHTTPTransport()
        self.base = urlsplit(provider_base_url)

    async def handle_async_request(self, request):
        url = urlsplit(str(request.url))
        if (url.scheme, url.netloc) != (self.base.scheme, self.base.netloc) or not url.path.startswith(self.base.path.rstrip("/") + "/"):
            return await self.transport.handle_async_request(request)
        state = _prepare(request, self.runtime, self.scope)
        if state is None:
            return await self.transport.handle_async_request(request)
        attempt, plan = state
        outgoing = _apply(request, state, await self.runtime.optimize(**plan[2]) if plan else None)
        attempt.observe("dispatch_intent")
        token = owner.set(attempt)
        try:
            response = await self.transport.handle_async_request(outgoing)
            if response.status_code >= 400:
                attempt.observe("failed")
            else:
                response.stream = _AsyncStream(response.stream, attempt, "text/event-stream" in response.headers.get("content-type", ""))
            return response
        except BaseException:
            attempt.observe("failed")
            raise
        finally:
            owner.reset(token)

    async def aclose(self):
        await self.transport.aclose()
