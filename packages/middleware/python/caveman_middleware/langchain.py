"""LangChain agent/model/document adapters; LangGraph keeps the scheduler."""
from __future__ import annotations

import functools
import asyncio
import copy
import json
import uuid
from dataclasses import asdict
from contextlib import aclosing

from ._versions import framework_import_failed

try:
    from langchain.agents.middleware import AgentMiddleware
    from langchain_core.documents import Document, BaseDocumentCompressor
    from langchain_core.language_models import BaseChatModel
    from langchain_core.messages import BaseMessage, ToolMessage, convert_to_messages
    from langchain_core.prompt_values import PromptValue
    from langchain_core.runnables import RunnableConfig, ensure_config
    from langchain_core.tools import StructuredTool, ToolException
except ImportError as error:
    framework_import_failed("langchain", error, "Install caveman-middleware[langchain] to use the LangChain adapter")

from caveman_cloud.middleware import Adapter, Candidate, MiddlewareError, Scope, RecoveryBinding, ensure_async, ensure_sync
from caveman_cloud.middleware.runtime import RECOVERY_DESCRIPTION, RECOVERY_SCHEMA
from ._guard import fail_open, recovery_failed, recovery_name_conflict, resolve_scope
from ._native import Attempt, manifest, owner, plain
from ._versions import VERSION, family_gate, installed_version

ADAPTER = Adapter("langchain", VERSION, installed_version("langchain") or "unknown", "langchain-message-v1")
OVERHEAD = json.dumps({"name": "caveman_retrieve", "description": RECOVERY_DESCRIPTION, "input_schema": RECOVERY_SCHEMA},
                      ensure_ascii=False, separators=(",", ":"))


def scope_from_config(config: RunnableConfig, *, namespace: str) -> Scope:
    """Use the caller's checkpoint thread and explicit branch/epoch identity.

    Raises ValueError without a thread. Used as an adapter ``scope`` resolver,
    that becomes a pass-through with ``invalid_scope`` (warned once) in
    non-strict mode. Free-form thread IDs (emails, "user 42 / chat #7") are
    hashed into valid scope tokens.
    """
    values = config.get("configurable", {})
    thread = values.get("thread_id")
    if not isinstance(thread, str) or not thread:
        raise ValueError("LangGraph middleware requires a nonempty configurable.thread_id")
    return Scope(namespace, thread, values.get("caveman_branch_id", "main"), values.get("caveman_cache_epoch", "0"))


def _scope(runtime, source, config=None, adapter="langchain"):
    return resolve_scope(runtime, adapter, source, ensure_config(config))


def _message_view(messages, prefix=(), manifest_bytes=None):
    try:
        context = manifest([m.model_dump(mode="json") for m in [*prefix, *messages]], manifest_bytes)
    except (TypeError, ValueError, AttributeError):
        return None
    if context is None:
        return None
    candidates, setters = [], {}
    names = {call["id"]: call["name"] for message in messages for call in getattr(message, "tool_calls", [])
             if plain(call) and type(call.get("id")) is str and type(call.get("name")) is str}
    for mi, message in enumerate(messages):
        if type(message) is not ToolMessage or message.name == "caveman_retrieve" or names.get(message.tool_call_id) == "caveman_retrieve" or message.status == "error":
            continue
        if not message.name and message.tool_call_id not in names:
            continue
        values = [(None, message.content)] if isinstance(message.content, str) else [(pi, p.get("text")) for pi, p in enumerate(message.content)
            if plain(p) and p.get("type") == "text" and "citations" not in p]
        for pi, text in values:
            if type(text) is not str:
                continue
            key = f"message-{mi}.part-{pi if pi is not None else 0}"
            candidates.append(Candidate(key, text, key))
            setters[key] = (mi, pi)
    return context, candidates, setters


def _apply(messages, outcome, setters):
    if not all(r["segment_id"] in setters for r in outcome.replacements):
        return messages
    result = messages.copy()
    for replacement in outcome.replacements:
        mi, pi = setters[replacement["segment_id"]]
        content = replacement["text"]
        if pi is not None:
            content = result[mi].content.copy()
            content[pi] = {**content[pi], "text": replacement["text"]}
        result[mi] = result[mi].model_copy(update={"content": content})
    return result if outcome.replacements else messages


class _Connection:
    """Either runtime type works on either path (D5): sync calls use its sync view, async calls its async view."""
    def __init__(self, runtime, scope, *, accept_framework_version=False, manifest_bytes=None):
        self.sync, self.async_runtime = ensure_sync(runtime), ensure_async(runtime)
        self.scope, self.manifest_bytes = scope, manifest_bytes
        self.supported = family_gate(self.sync, "langchain", ADAPTER.id, accept_framework_version)

    @staticmethod
    def passive(runtime, reason):
        if owner.get() is not None:
            return None
        return Attempt(runtime, None, str(uuid.uuid4()), str(uuid.uuid4()), passive=True, reason=reason, adapter=ADAPTER.id)

    def state(self, messages, config, runtime, binding=None, model=None, prefix=()):
        if owner.get() is not None:
            return None
        reason = "disabled" if runtime.mode == "off" else "unsupported_version" if not self.supported else None
        if reason:
            return self.passive(runtime, reason), {}, None
        view = _message_view(messages, prefix, self.manifest_bytes)
        if view is None:
            return self.passive(runtime, "unsupported_shape"), {}, None
        scope = _scope(runtime, self.scope, config)
        if scope is None:
            return self.passive(runtime, "invalid_scope"), {}, None
        binding = binding(runtime) if callable(binding) else binding
        attempt = Attempt(runtime, scope, str(uuid.uuid4()), str(uuid.uuid4()), adapter=ADAPTER.id)
        context, candidates, setters = view
        options = dict(scope=scope, adapter=ADAPTER, manifest=context, sequence=context.sequence, candidates=candidates, binding=binding,
                       model=model, recovery_overhead_text=OVERHEAD if binding else None,
                       logical_call_id=attempt.logical_call_id, attempt_id=attempt.attempt_id)
        return attempt, setters, options

    @staticmethod
    def _finish(messages, attempt, setters):
        if not all(replacement["segment_id"] in setters for replacement in attempt.optimization.replacements):
            attempt.optimization, attempt.reason = None, "invalid_plan"
            return messages, attempt
        return _apply(messages, attempt.optimization, setters), attempt

    def prepare(self, messages, config=None, binding=None, model=None, prefix=()):
        """``binding`` is a RecoveryBinding or ``callable(runtime)`` resolved inside the fail-open guard."""
        try:
            state = self.state(messages, config, self.sync, binding, model, prefix)
            if state is None:
                return messages, None
            attempt, setters, options = state
            if options is None:
                return messages, attempt
            attempt.optimization = self.sync.optimize(**options)
            return self._finish(messages, attempt, setters)
        except Exception as error:  # Decision 4: the model call still runs with the caller's messages
            return messages, self.passive(self.sync, fail_open(self.sync, ADAPTER.id, error))

    async def prepare_async(self, messages, config=None, binding=None, model=None, prefix=()):
        try:
            state = self.state(messages, config, self.async_runtime, binding, model, prefix)
            if state is None:
                return messages, None
            attempt, setters, options = state
            if options is None:
                return messages, attempt
            attempt.optimization = await self.async_runtime.optimize(**options)
            return self._finish(messages, attempt, setters)
        except Exception as error:
            return messages, self.passive(self.async_runtime, fail_open(self.async_runtime, ADAPTER.id, error))


def _observe_messages(attempt, response):
    if attempt is None:
        return
    # LC usage_metadata is already normalized by the host's native provider.
    results = getattr(response, "result", [response])
    values = next((m.usage_metadata for m in reversed(results) if getattr(m, "usage_metadata", None)), None)
    from ._usage import langchain_usage
    attempt.observe("completed", langchain_usage(values))


class CavemanMiddleware(AgentMiddleware):
    """Native per-model middleware. Register recovery_tool in the host tool list.

    with_caveman_agent() performs collision-safe model/tool registration together.
    scope may be a fixed Scope or a trusted Callable[[RunnableConfig], Scope].
    """
    tools = ()

    def __init__(self, *, runtime, scope, accept_framework_version=False, manifest_bytes=None):
        self.connection = connection = _Connection(runtime, scope, accept_framework_version=accept_framework_version,
                                                   manifest_bytes=manifest_bytes)
        self.recovery_tool = None
        if not connection.supported or connection.sync.mode == "off":
            return

        # A refused handle is a ToolException: handle_tool_error turns it into an error ToolMessage, so the
        # agent run (and its checkpointed thread) keeps going instead of raising out of the tools node.
        def recover(handle: str, config: RunnableConfig, offset: int = 0, limit: int = 262144, query: str = ""):
            try:
                page = connection.sync.retrieve(_scope(connection.sync, scope, config), handle=handle, offset=offset, limit=limit, query=query)
            except MiddlewareError as error:
                raise ToolException(json.dumps(recovery_failed(ADAPTER.id, error))) from error
            return json.dumps(page, ensure_ascii=False, separators=(",", ":"))

        async def arecover(handle: str, config: RunnableConfig, offset: int = 0, limit: int = 262144, query: str = ""):
            try:
                page = await connection.async_runtime.retrieve(_scope(connection.sync, scope, config), handle=handle, offset=offset, limit=limit, query=query)
            except MiddlewareError as error:
                raise ToolException(json.dumps(recovery_failed(ADAPTER.id, error))) from error
            return json.dumps(page, ensure_ascii=False, separators=(",", ":"))

        # Native tools expose mutable schemas and executor fields. Give the
        # framework its own schema, and attest the callable registration each
        # time before asking the runtime to issue lossy source grants.
        schema = copy.deepcopy(RECOVERY_SCHEMA)
        self.recovery_tool = StructuredTool.from_function(func=recover, coroutine=arecover, name="caveman_retrieve", description=RECOVERY_DESCRIPTION,
                                                          args_schema=schema, handle_tool_error=True)
        self._recovery_schema = json.dumps(schema, sort_keys=True, allow_nan=False)
        self._recovery_methods = {name: getattr(self.recovery_tool, name) for name in
                                  ("func", "coroutine", "invoke", "ainvoke", "run", "arun")}

    def _recovery_intact(self):
        tool = self.recovery_tool
        try:
            return (type(tool) is StructuredTool and tool.name == "caveman_retrieve"
                    and tool.description == RECOVERY_DESCRIPTION and tool.return_direct is False
                    and tool.response_format == "content"
                    and json.dumps(tool.args_schema, sort_keys=True, allow_nan=False) == self._recovery_schema
                    and all(getattr(tool, name) == method for name, method in self._recovery_methods.items()))
        except (TypeError, ValueError, AttributeError, RecursionError):
            return False

    def binding(self, request, runtime):
        if self.recovery_tool is None or runtime.mode != "compress" or owner.get() is not None:
            return None
        actual = [tool for tool in request.tools if getattr(tool, "name", None) == "caveman_retrieve" or (plain(tool) and tool.get("name") == "caveman_retrieve")]
        if any(tool is not self.recovery_tool for tool in actual):
            recovery_name_conflict(runtime, ADAPTER.id)
        if len(actual) != 1 or actual[0] is not self.recovery_tool or not self._recovery_intact() or request.response_format is not None or request.tool_choice not in (None, "auto"):
            return None
        scope = _scope(runtime, self.connection.scope)
        return runtime.recovery(scope) if scope is not None else None

    @property
    def overhead(self):
        return OVERHEAD

    def wrap_model_call(self, request, handler):
        messages, attempt = self.connection.prepare(request.messages, binding=lambda runtime: self.binding(request, runtime),
                                                    prefix=[request.system_message] if request.system_message else [])
        if attempt is None:
            return handler(request)
        attempt.observe("dispatch_intent")
        token = owner.set(attempt)
        try:
            response = handler(request.override(messages=messages))
            _observe_messages(attempt, response)
            return response
        except BaseException:
            attempt.observe("failed")
            raise
        finally:
            owner.reset(token)

    async def awrap_model_call(self, request, handler):
        messages, attempt = await self.connection.prepare_async(request.messages, binding=lambda runtime: self.binding(request, runtime),
                                                                prefix=[request.system_message] if request.system_message else [])
        if attempt is None:
            return await handler(request)
        attempt.observe("dispatch_intent")
        token = owner.set(attempt)
        try:
            response = await handler(request.override(messages=messages))
            _observe_messages(attempt, response)
            return response
        except asyncio.CancelledError:
            attempt.observe("cancelled")
            raise
        except BaseException:
            attempt.observe("failed")
            raise
        finally:
            owner.reset(token)


def with_caveman_agent(options: dict, *, runtime, scope, accept_framework_version=False, manifest_bytes=None) -> dict:
    """Return native create_agent keyword arguments; do not run another loop.

    Compresses: the recovery tool is registered with the middleware. A host tool
    already named ``caveman_retrieve`` keeps its name and recovery stays off
    (``recovery_name_conflict``).
    """
    middleware = CavemanMiddleware(runtime=runtime, scope=scope, accept_framework_version=accept_framework_version,
                                   manifest_bytes=manifest_bytes)
    tools = list(options.get("tools", []))
    collision = any((tool.get("name") if plain(tool) else getattr(tool, "name", None)) == "caveman_retrieve" for tool in tools)
    if collision and middleware.recovery_tool is not None and middleware.connection.sync.mode == "compress":
        recovery_name_conflict(middleware.connection.sync, ADAPTER.id)
    if not collision and middleware.connection.sync.mode == "compress" and middleware.recovery_tool is not None:
        tools.append(middleware.recovery_tool)
    return {**options, "tools": tools, "middleware": [*options.get("middleware", []), middleware]}


def with_caveman_model(model: BaseChatModel, *, runtime, scope, accept_framework_version=False, manifest_bytes=None):
    """Native model clone preserving bind_tools/structured helpers and callbacks.

    The pinned providers' public batch APIs call invoke/ainvoke for each item,
    so every item resolves its own RunnableConfig scope. Record-only: this
    variant has no recovery executor, so compress mode reports
    ``recovery_unbound`` and sends originals. Use ``with_caveman_agent`` to compress.
    """
    if not isinstance(model, BaseChatModel):
        raise TypeError("Expected a native LangChain BaseChatModel")
    native = model.model_copy()
    connection = _Connection(runtime, scope, accept_framework_version=accept_framework_version, manifest_bytes=manifest_bytes)

    def messages(input):
        if isinstance(input, PromptValue):
            return input.to_messages()
        if isinstance(input, str):
            return convert_to_messages([("human", input)])
        return convert_to_messages(input)

    def wrap_call(method):
        @functools.wraps(method)
        def call(input, config=None, **kwargs):
            view, attempt = connection.prepare(messages(input), config)
            if attempt is None:
                return method(input, config, **kwargs)
            attempt.observe("dispatch_intent")
            token = owner.set(attempt)
            try:
                result = method(view, config, **kwargs)
                _observe_messages(attempt, result)
                return result
            except BaseException:
                attempt.observe("failed")
                raise
            finally:
                owner.reset(token)
        return call

    def wrap_acall(method):
        @functools.wraps(method)
        async def call(input, config=None, **kwargs):
            view, attempt = await connection.prepare_async(messages(input), config)
            if attempt is None:
                return await method(input, config, **kwargs)
            attempt.observe("dispatch_intent")
            token = owner.set(attempt)
            try:
                result = await method(view, config, **kwargs)
                _observe_messages(attempt, result)
                return result
            except asyncio.CancelledError:
                attempt.observe("cancelled")
                raise
            except BaseException:
                attempt.observe("failed")
                raise
            finally:
                owner.reset(token)
        return call

    def wrap_stream(method):
        @functools.wraps(method)
        def stream(input, config=None, **kwargs):
            view, attempt = connection.prepare(messages(input), config)
            from ._streams import observe_iterator
            yield from observe_iterator(method(view if attempt else input, config, **kwargs), attempt)
        return stream

    def wrap_astream(method):
        @functools.wraps(method)
        async def stream(input, config=None, **kwargs):
            view, attempt = await connection.prepare_async(messages(input), config)
            from ._streams import observe_async_iterator
            async with aclosing(observe_async_iterator(method(view if attempt else input, config, **kwargs), attempt)) as observed:
                async for value in observed:
                    yield value
        return stream

    # Override public operations only on the public model_copy() clone.
    # Native bind_tools/with_structured_output bind this clone as their target.
    for name, wrapper in (("invoke", wrap_call), ("ainvoke", wrap_acall), ("stream", wrap_stream), ("astream", wrap_astream)):
        object.__setattr__(native, name, wrapper(getattr(native, name)))
    return native


class CavemanDocumentCompressor(BaseDocumentCompressor):
    """Native RAG views with an optional application-owned source reader.

    source_expansion must be the RecoveryBinding returned by this runtime for
    this scope. Register its execute callable in the application's reader path
    before passing the binding here; a flag, schema, or callback is insufficient.
    Without that reader it is record-only: compress mode reports ``recovery_unbound``.
    """
    runtime: object
    scope: object
    source_expansion: object = None
    accept_framework_version: bool = False
    manifest_bytes: int | None = None

    def compress_documents(self, documents, query, callbacks=None):
        documents = list(documents)
        runtime = ensure_sync(self.runtime)
        try:
            options = self._options(documents, runtime)
            return documents if options is None else self._apply_documents(documents, runtime.optimize(**options), runtime)
        except Exception as error:  # Decision 4
            runtime.report(reason=fail_open(runtime, "langchain-rag", error), adapter="langchain-rag")
            return documents

    async def acompress_documents(self, documents, query, callbacks=None):
        documents = list(documents)
        runtime = ensure_async(self.runtime)
        try:
            options = self._options(documents, runtime)
            return documents if options is None else self._apply_documents(documents, await runtime.optimize(**options), runtime)
        except Exception as error:
            runtime.report(reason=fail_open(runtime, "langchain-rag", error), adapter="langchain-rag")
            return documents

    def _options(self, documents, runtime):
        if runtime.mode == "off" or not family_gate(runtime, "langchain", "langchain-rag", self.accept_framework_version):
            runtime.report(reason="disabled" if runtime.mode == "off" else "unsupported_version", adapter="langchain-rag")
            return None
        if any(type(document) is not Document for document in documents):
            runtime.report(reason="unsupported_shape", adapter="langchain-rag")
            return None
        context = manifest([{"id": d.id, "metadata": d.metadata, "page_content": d.page_content} for d in documents], self.manifest_bytes)
        if context is None:
            runtime.report(reason="unsupported_shape", adapter="langchain-rag")
            return None
        scope = _scope(runtime, self.scope, adapter="langchain-rag")
        if scope is None:
            runtime.report(reason="invalid_scope", adapter="langchain-rag")
            return None
        reader = self.source_expansion
        binding = reader if (isinstance(reader, RecoveryBinding) and callable(reader.execute)
                             and runtime.owns_binding(reader, scope)) else None
        return dict(scope=scope, adapter=Adapter("langchain-rag", VERSION, ADAPTER.framework_version, "langchain-document-v1"), manifest=context,
                    sequence=context.sequence, binding=binding,
                    candidates=[Candidate(f"document-{i}", d.page_content, d.id or f"document-{i}", kind="artifact") for i, d in enumerate(documents)])

    def _apply_documents(self, documents, outcome, runtime):
        replacements = {r["segment_id"]: r["text"] for r in outcome.replacements}
        if not replacements.keys() <= {f"document-{i}" for i in range(len(documents))}:
            runtime.report(reason="invalid_plan", adapter="langchain-rag")
            return documents
        result = [document.model_copy(update={"page_content": replacements[f"document-{i}"]}) if f"document-{i}" in replacements else document for i, document in enumerate(documents)]
        runtime.report(outcome, adapter="langchain-rag")
        return result
