"""LlamaIndex native LLM delegation, tool recovery, and immutable RAG views."""
from __future__ import annotations

import asyncio
import contextvars
import copy
import json
import uuid
from contextlib import contextmanager
from dataclasses import dataclass
from importlib.metadata import version
from typing import Any

try:
    from llama_index.core.agent.workflow import FunctionAgent
    from llama_index.core.base.llms.types import ChatMessage, MessageRole, TextBlock, ToolCallBlock
    from llama_index.core.llms import LLM
    from llama_index.core.llms.llm import ToolSelection
    from llama_index.core.llms.function_calling import FunctionCallingLLM
    from llama_index.core.postprocessor.types import BaseNodePostprocessor
    from llama_index.core.schema import NodeWithScore, TextNode
    from llama_index.core.tools import FunctionTool, ToolMetadata, ToolOutput
    from llama_index.core.workflow import Context
    from pydantic import Field
    from pydantic_core import PydanticSerializationError
except ModuleNotFoundError as error:
    raise ImportError("Install caveman-middleware[llama-index] to use the LlamaIndex adapter") from error

from caveman_cloud.middleware import Adapter, Candidate, MiddlewareError, MiddlewareRuntime, RecoveryBinding, Scope
from caveman_cloud.middleware.runtime import RECOVERY_DESCRIPTION, RECOVERY_SCHEMA
from ._native import Attempt, manifest, owner
from ._usage import usage
from ._versions import matches_framework, supports_framework

ADAPTER = Adapter("llama-index", "0.1.0", "0.14.24", "llama-index-message-v1")
RAG_ADAPTER = Adapter("llama-index-rag", "0.1.0", "0.14.24", "llama-index-node-v1")


def _check_version(runtime):
    return supports_framework(runtime, ("llama-index-core", "0.14", "0.15"))


def _scope(source, context=None):
    result = source if isinstance(source, Scope) else source(context)
    if not isinstance(result, Scope):
        raise TypeError("LlamaIndex scope resolver must return a Caveman Scope")
    return result


def _async_runtime(runtime):
    return runtime.as_async() if isinstance(runtime, MiddlewareRuntime) else runtime


def _protocol(model, runtime):
    provider = (type(model).__module__, type(model).__name__)
    supported = {
        ("llama_index.llms.openai.base", "OpenAI"): ("llama-index-llms-openai", "0.8", "1", "openai-chat"),
        ("llama_index.llms.anthropic.base", "Anthropic"): ("llama-index-llms-anthropic", "0.12", "1", "anthropic-messages"),
    }
    match = supported.get(provider)
    if match and not supports_framework(runtime, match[:3]):
        return None
    if match:
        sdk = ("openai", "2.54", "4") if match[3] == "openai-chat" else ("anthropic", "0.125", "2")
        if not supports_framework(runtime, sdk):
            return None
    return match[3] if match else None


@dataclass
class _Invocation:
    registration: Any
    scope: Scope
    tools: Any
    successful: dict
    recovery_allowed: bool


_invocation = contextvars.ContextVar("caveman_llama_index_invocation", default=None)


class _RecoveryMetadata(ToolMetadata):
    def get_parameters_dict(self):
        # Default ToolMetadata removes additionalProperties from the schema.
        return copy.deepcopy(RECOVERY_SCHEMA)


class _FrozenRecoveryMetadata(_RecoveryMetadata):
    def __init__(self, **kwargs):
        super().__init__(**kwargs)
        object.__setattr__(self, "_caveman_frozen", True)

    def __setattr__(self, name, value):
        if getattr(self, "_caveman_frozen", False):
            raise AttributeError("Caveman recovery metadata is immutable")
        super().__setattr__(name, value)


class _FrozenFunctionTool(FunctionTool):
    def __init__(self, **kwargs):
        super().__init__(**kwargs)
        object.__setattr__(self, "_caveman_frozen", True)

    def __setattr__(self, name, value):
        if getattr(self, "_caveman_frozen", False):
            raise AttributeError("Caveman recovery tool is immutable")
        super().__setattr__(name, value)


class _Recovery:
    def __init__(self, runtime, scope):
        self.runtime, self.scope = runtime, scope

        def recover(ctx: Context, handle: str, offset: int = 0, limit: int = 262144, query: str = ""):
            page = runtime.retrieve(_scope(scope, ctx), handle=handle, offset=offset, limit=limit, query=query)
            return json.dumps(page, ensure_ascii=False)

        async def arecover(ctx: Context, handle: str, offset: int = 0, limit: int = 262144, query: str = ""):
            page = await _async_runtime(runtime).retrieve(_scope(scope, ctx), handle=handle, offset=offset, limit=limit, query=query)
            return json.dumps(page, ensure_ascii=False)

        # FunctionTool inspects runtime annotations for its public context injection.
        recover.__annotations__["ctx"] = Context
        arecover.__annotations__["ctx"] = Context
        self.sync, self.async_ = recover, arecover
        self.metadata = _RecoveryMetadata(name="caveman_retrieve", description=RECOVERY_DESCRIPTION, fn_schema=None)
        self.tool = FunctionTool(fn=recover, async_fn=arecover, metadata=self.metadata)

    def registered(self, tools, invocation):
        if (invocation is None or invocation.registration is not self or not invocation.recovery_allowed
                or len(tools) != len(invocation.tools) or any(a is not b for a, b in zip(tools, invocation.tools))):
            return False
        offered = [tool for tool in tools if tool.metadata.name == "caveman_retrieve"]
        return (len(offered) == 1 and offered[0] is self.tool and self.tool.fn is self.sync
                and self.tool.async_fn is self.async_ and self.tool.metadata is self.metadata
                and not self.tool.partial_params and self.tool.requires_context
                and self.metadata.description == RECOVERY_DESCRIPTION and not self.metadata.return_direct
                and self.metadata.get_parameters_dict() == RECOVERY_SCHEMA)


class _ApplicationTools:
    """Executor registration for a caller-owned loop of native LLM calls."""
    def __init__(self, runtime, scope, tools, *, enabled=True):
        self.runtime, self.scope, self.successful = runtime, scope, {}
        selected = tuple(tool if isinstance(tool, FunctionTool) else FunctionTool.from_defaults(tool) for tool in tools)
        names = [tool.metadata.name for tool in selected]
        if any(not isinstance(name, str) or not name for name in names) or len(set(names)) != len(names) or "caveman_retrieve" in names:
            raise ValueError("Expected distinct native tools without caveman_retrieve")
        self.binding = runtime.recovery(scope) if enabled else None
        self.async_binding = _async_runtime(runtime).recovery(scope) if enabled else None
        if enabled:
            def recover(handle: str, offset: int = 0, limit: int = 262144, query: str = ""):
                return json.dumps(self.binding.execute(dict(handle=handle, offset=offset, limit=limit, query=query)), ensure_ascii=False)
            async def arecover(handle: str, offset: int = 0, limit: int = 262144, query: str = ""):
                return json.dumps(await self.async_binding.execute(dict(handle=handle, offset=offset, limit=limit, query=query)), ensure_ascii=False)
            self.sync, self.async_ = recover, arecover
            self.metadata = _FrozenRecoveryMetadata(name="caveman_retrieve", description=RECOVERY_DESCRIPTION, fn_schema=None)
            self.tool = _FrozenFunctionTool(fn=recover, async_fn=arecover, metadata=self.metadata)
            selected += (self.tool,)
        self.tools, self.names = selected, tuple(tool.metadata.name for tool in selected)

    def registered(self, tools, invocation):
        return bool(self.binding is not None and invocation is not None and invocation.registration is self
            and invocation.recovery_allowed and len(tools) == len(self.tools)
            and all(left is right for left, right in zip(tools, self.tools))
            and tuple(tool.metadata.name for tool in tools) == self.names
            and self.runtime.owns_binding(self.binding, self.scope)
            and self.runtime.owns_binding(self.async_binding, self.scope)
            and self.tool.fn is self.sync and self.tool.async_fn is self.async_
            and self.tool.metadata is self.metadata and not self.tool.partial_params and not self.tool.requires_context
            and self.metadata.description == RECOVERY_DESCRIPTION and not self.metadata.return_direct
            and self.metadata.get_parameters_dict() == RECOVERY_SCHEMA)

    def invocation(self):
        return _Invocation(self, self.scope, self.tools, self.successful, True)

    def selected(self, call: ToolSelection):
        if self.binding is not None and not self.registered(self.tools, self.invocation()):
            raise ValueError("Native executor registration changed")
        if not isinstance(call, ToolSelection) or not isinstance(call.tool_id, str) or not call.tool_id or not isinstance(call.tool_kwargs, dict):
            raise TypeError("Expected a native ToolSelection")
        matches = [tool for tool in self.tools if tool.metadata.name == call.tool_name]
        if len(matches) != 1:
            raise ValueError("Native tool is not registered")
        self.successful.pop(call.tool_id, None)
        return matches[0]

    def completed(self, call, result):
        if not isinstance(result, ToolOutput):
            raise TypeError("Native FunctionTool returned an unexpected output")
        if not result.is_error and call.tool_name != "caveman_retrieve" and len(self.successful) < 4096:
            self.successful[call.tool_id] = call.tool_name
        return result


@dataclass(frozen=True)
class CavemanLLMTools:
    """Native model and tools for an application-owned loop; no scheduler."""
    model: LLM
    tools: tuple[FunctionTool, ...]
    _registration: Any

    def execute(self, call: ToolSelection) -> ToolOutput:
        tool = self._registration.selected(call)
        return self._registration.completed(call, tool.call(**call.tool_kwargs))

    async def aexecute(self, call: ToolSelection) -> ToolOutput:
        tool = self._registration.selected(call)
        return self._registration.completed(call, await tool.acall(**call.tool_kwargs))


def _message_view(messages, invocation):
    if type(messages) not in (list, tuple) or any(type(message) is not ChatMessage for message in messages):
        return None
    try:
        context = manifest([message.model_dump(mode="json", warnings="error") for message in messages])
    except (TypeError, ValueError, PydanticSerializationError, RecursionError):
        return None
    if context is None:
        return None
    calls, candidates, paths = {}, [], {}
    for mi, message in enumerate(messages):
        if message.role == MessageRole.ASSISTANT:
            for block in message.blocks:
                if type(block) is ToolCallBlock and type(block.tool_call_id) is str:
                    calls[block.tool_call_id] = block.tool_name
        elif message.role == MessageRole.TOOL:
            call_id = message.additional_kwargs.get("tool_call_id")
            name = calls.get(call_id) if type(call_id) is str else None
            # FunctionAgent drops ToolOutput.is_error when it creates messages.
            # Its public result hook supplies the actual successful call IDs.
            success = (invocation.successful.get(call_id) == name if invocation else
                       message.additional_kwargs.get("is_error") is False)
            if not name or name == "caveman_retrieve" or not success or message.additional_kwargs.get("is_error"):
                continue
            for bi, block in enumerate(message.blocks):
                if type(block) is TextBlock:
                    key = f"message-{mi}.block-{bi}"
                    candidates.append(Candidate(key, block.text, call_id))
                    paths[key] = (mi, bi)
    return context, candidates, paths


def _apply(messages, result, paths):
    if not result.replacements or any(r["segment_id"] not in paths for r in result.replacements):
        return messages
    view = list(messages)
    for replacement in result.replacements:
        mi, bi = paths[replacement["segment_id"]]
        blocks = list(view[mi].blocks)
        blocks[bi] = blocks[bi].model_copy(update={"text": replacement["text"]})
        view[mi] = view[mi].model_copy(update={"blocks": blocks})
    return view


def _usage(response):
    raw = getattr(response, "raw", None)
    if hasattr(raw, "model_dump"):
        raw = raw.model_dump()
    if isinstance(raw, dict):
        source = raw.get("message", raw) if raw.get("type") == "message_start" else raw
        measured = usage(source.get("usage"))
        if measured:
            return measured
    # Native ChatResponse.additional_kwargs carries observed token counts.
    measured = usage(getattr(response, "additional_kwargs", None))
    return measured if measured and any(measured[key] is not None for key in
        ("input_tokens", "output_tokens", "cache_read_tokens", "cache_write_tokens", "reasoning_tokens")) else None


def _merge_usage(previous, current):
    if current is None:
        return previous
    result = {**(previous or {}), **{key: value for key, value in current.items() if value is not None}}
    result["complete"] = result.get("input_tokens") is not None and result.get("output_tokens") is not None
    return result


@contextmanager
def _owned(attempt):
    token = owner.set(attempt) if attempt else None
    try:
        yield
    finally:
        if token is not None:
            owner.reset(token)


class _OwnedIterator:
    def __init__(self, iterator, attempt):
        self.iterator, self.attempt = iterator, attempt

    def __iter__(self):
        return self

    def __next__(self):
        with _owned(self.attempt):
            return next(self.iterator)

    def send(self, value):
        with _owned(self.attempt):
            return self.iterator.send(value)

    def throw(self, *args):
        with _owned(self.attempt):
            return self.iterator.throw(*args)

    def close(self):
        with _owned(self.attempt):
            return self.iterator.close()


class _OwnedAsyncIterator:
    def __init__(self, iterator, attempt):
        self.iterator, self.attempt = iterator, attempt

    def __aiter__(self):
        return self

    async def __anext__(self):
        with _owned(self.attempt):
            return await anext(self.iterator)

    async def asend(self, value):
        with _owned(self.attempt):
            return await self.iterator.asend(value)

    async def athrow(self, *args):
        with _owned(self.attempt):
            return await self.iterator.athrow(*args)

    async def aclose(self):
        with _owned(self.attempt):
            return await self.iterator.aclose()


class CavemanLLM(FunctionCallingLLM):
    """Delegate to an existing native LLM. Model-only calls are recovery-free.

    Public provider methods retain native serialization, retries, callbacks,
    response objects, and tool parsing. This class never runs a tool loop.
    """
    wrapped: LLM = Field(exclude=True)
    runtime: Any = Field(exclude=True)
    scope: Any = Field(exclude=True)
    protocol: str | None = Field(exclude=True)
    passthrough_reason: str = Field(exclude=True)
    registration: Any = Field(default=None, exclude=True)

    def __init__(self, wrapped: LLM, *, runtime, scope, registration=None):
        if not isinstance(wrapped, LLM):
            raise TypeError("Expected an existing native LlamaIndex LLM")
        # Copy public prompt settings so inherited predict/structured helpers
        # build exactly the same native input as the caller's LLM.
        settings = {name: getattr(wrapped, name) for name in LLM.model_fields}
        supported = _check_version(runtime)
        protocol = _protocol(wrapped, runtime) if supported else None
        known_provider = (type(wrapped).__module__, type(wrapped).__name__) in {
            ("llama_index.llms.openai.base", "OpenAI"), ("llama_index.llms.anthropic.base", "Anthropic")}
        super().__init__(wrapped=wrapped, runtime=runtime, scope=scope, protocol=protocol,
                         passthrough_reason="unsupported_version" if not supported or (known_provider and protocol is None) else "unsupported_provider",
                         registration=registration, **settings)

    @property
    def metadata(self):
        return self.wrapped.metadata

    def _passive(self, reason):
        if owner.get() is not None:
            return None
        return Attempt(self.runtime, None, str(uuid.uuid4()), str(uuid.uuid4()), passive=True,
                       reason="disabled" if self.runtime.mode == "off" else self.passthrough_reason if self.protocol is None else reason,
                       adapter=ADAPTER.id)

    def _passive_stream(self, method, args, kwargs, reason):
        attempt = self._passive(reason)
        with _owned(attempt):
            if attempt:
                attempt.observe("dispatch_intent")
            iterator = method(*args, **kwargs)
        return _OwnedIterator(iterator, attempt)

    async def _passive_astream(self, method, args, kwargs, reason):
        attempt = self._passive(reason)
        with _owned(attempt):
            if attempt:
                attempt.observe("dispatch_intent")
            iterator = await method(*args, **kwargs)
        return _OwnedAsyncIterator(iterator, attempt)

    def _permits_recovery(self, settings, kwargs):
        extra_body = settings.get("extra_body")
        return (not settings.get("tool_required") and settings.get("tool_choice") in (None, "auto")
                and "messages" not in kwargs
                and not getattr(self.wrapped, "mcp_servers", None)
                and not any(tool.get("name") == "caveman_retrieve" for tool in (getattr(self.wrapped, "tools", None) or []) if isinstance(tool, dict))
                and not any(settings.get(key) for key in ("response_format", "output_format", "output_config"))
                and (extra_body is None or type(extra_body) is dict)
                and not any(key in (extra_body or {}) for key in
                            ("tools", "tool_choice", "messages", "response_format", "output_format", "output_config")))

    def _options(self, messages, tools, kwargs):
        if owner.get() is not None:
            return None
        if self.runtime.mode == "off" or self.protocol is None:
            return self._passive("no_candidate"), {}, None
        current = _invocation.get()
        if current is not None and current.registration is not self.registration:
            current = None
        if current is None and isinstance(self.registration, _ApplicationTools):
            current = self.registration.invocation()
            if _scope(self.scope) != current.scope:
                current = None
        selected = _message_view(messages, current)
        if selected is None:
            return self._passive("opaque_payload"), {}, None
        context, candidates, paths = selected
        user_msg = kwargs.get("user_msg")
        if user_msg is not None:
            if type(user_msg) not in (str, ChatMessage):
                return self._passive("opaque_payload"), {}, None
            try:
                extra = manifest([{"user_msg": user_msg if type(user_msg) is str else user_msg.model_dump(mode="json", warnings="error")}])
            except (TypeError, ValueError, PydanticSerializationError, RecursionError):
                return self._passive("opaque_payload"), {}, None
            if extra is None:
                return self._passive("opaque_payload"), {}, None
            context.append({**extra[0], "id": f"message-{len(context)}"})
        scope = current.scope if current else _scope(self.scope)
        binding, overhead = None, None
        settings = {**(getattr(self.wrapped, "additional_kwargs", None) or {}), **kwargs}
        if tools is not None and self._permits_recovery(settings, kwargs) and self.registration and self.registration.registered(tools, current):
            binding = self.registration.binding if isinstance(self.registration, _ApplicationTools) else self.runtime.recovery(scope)
            overhead = json.dumps(self.registration.metadata.to_openai_tool(), ensure_ascii=False, separators=(",", ":"))
        attempt = Attempt(self.runtime, scope, str(uuid.uuid4()), str(uuid.uuid4()), adapter=ADAPTER.id)
        options = dict(scope=scope, adapter=ADAPTER, candidates=candidates, manifest=context, binding=binding,
            recovery_overhead_text=overhead, model={"provider": "openai" if self.protocol == "openai-chat" else "anthropic",
                "id": settings.get("model", self.metadata.model_name), "protocol": self.protocol},
            logical_call_id=attempt.logical_call_id, attempt_id=attempt.attempt_id)
        return attempt, paths, options

    def _prepare(self, messages, tools, kwargs):
        state = self._options(messages, tools, kwargs)
        if state is None:
            return messages, None
        attempt, paths, options = state
        if options is None:
            return messages, attempt
        result = self.runtime.optimize(**options)
        if not self._still_registered(options, tools, kwargs):
            attempt.reason = "recovery_unavailable"
            return messages, attempt
        view = _apply(messages, result, paths)
        if not result.replacements or view is not messages:
            attempt.optimization = result
        else:
            attempt.reason = "invalid_plan"
        return view, attempt

    async def _aprepare(self, messages, tools, kwargs):
        state = self._options(messages, tools, kwargs)
        if state is None:
            return messages, None
        attempt, paths, options = state
        if options is None:
            return messages, attempt
        try:
            result = await _async_runtime(self.runtime).optimize(**options)
            if not self._still_registered(options, tools, kwargs):
                attempt.reason = "recovery_unavailable"
                return messages, attempt
            view = _apply(messages, result, paths)
            if not result.replacements or view is not messages:
                attempt.optimization = result
            else:
                attempt.reason = "invalid_plan"
            return view, attempt
        except asyncio.CancelledError:
            attempt.observe("cancelled")
            raise

    def _still_registered(self, options, tools, kwargs):
        if self.runtime.mode == "off":
            return False
        if options["binding"] is None:
            return True
        try:
            if not self._permits_recovery({**(getattr(self.wrapped, "additional_kwargs", None) or {}), **kwargs}, kwargs):
                return False
            current = self.registration.invocation() if isinstance(self.registration, _ApplicationTools) else _invocation.get()
            return bool(current is not None and (not isinstance(self.registration, _ApplicationTools) or _scope(self.scope) == current.scope)
                        and self.runtime.owns_binding(options["binding"], current.scope)
                        and self.registration.registered(tools, current))
        except Exception:
            return False

    def _call(self, method, messages, tools, kwargs, *, passthrough=None):
        view, attempt = (messages, self._passive(passthrough)) if passthrough else self._prepare(messages, tools, kwargs)
        token = owner.set(attempt) if attempt else None
        try:
            if attempt:
                attempt.observe("dispatch_intent")
            result = method(view, **kwargs) if tools is None else method(tools=tools, chat_history=list(view), **kwargs)
            if attempt:
                attempt.observe("completed", None if attempt.passive else _usage(result))
            return result
        except BaseException:
            if attempt:
                attempt.observe("failed")
            raise
        finally:
            if token is not None:
                owner.reset(token)

    async def _acall(self, method, messages, tools, kwargs, *, passthrough=None):
        view, attempt = (messages, self._passive(passthrough)) if passthrough else await self._aprepare(messages, tools, kwargs)
        token = owner.set(attempt) if attempt else None
        try:
            if attempt:
                attempt.observe("dispatch_intent")
            result = await (method(view, **kwargs) if tools is None else method(tools=tools, chat_history=list(view), **kwargs))
            if attempt:
                attempt.observe("completed", None if attempt.passive else _usage(result))
            return result
        except BaseException as error:
            if attempt:
                attempt.observe("cancelled" if isinstance(error, asyncio.CancelledError) else "failed")
            raise
        finally:
            if token is not None:
                owner.reset(token)

    def _stream(self, method, messages, tools, kwargs):
        # Defer preparation and provider dispatch until the consumer advances.
        view, attempt = self._prepare(messages, tools, kwargs)
        iterator, last = None, None
        try:
            token = owner.set(attempt) if attempt else None
            try:
                if attempt:
                    attempt.observe("dispatch_intent")
                iterator = method(view, **kwargs) if tools is None else method(tools=tools, chat_history=list(view), **kwargs)
            finally:
                if token is not None:
                    owner.reset(token)
            while True:
                token = owner.set(attempt) if attempt else None
                try:
                    value = next(iterator)
                except StopIteration:
                    if attempt:
                        attempt.observe("completed", last)
                    return
                finally:
                    if token is not None:
                        owner.reset(token)
                if attempt and not attempt.passive:
                    last = _merge_usage(last, _usage(value))
                yield value
        except BaseException as error:
            if attempt:
                attempt.observe("cancelled" if isinstance(error, GeneratorExit) else "failed")
            raise
        finally:
            if iterator is not None and hasattr(iterator, "close"):
                iterator.close()

    async def _astream(self, method, messages, tools, kwargs):
        view, attempt = await self._aprepare(messages, tools, kwargs)
        iterator, last = None, None
        try:
            token = owner.set(attempt) if attempt else None
            try:
                if attempt:
                    attempt.observe("dispatch_intent")
                iterator = await (method(view, **kwargs) if tools is None else method(tools=tools, chat_history=list(view), **kwargs))
            finally:
                if token is not None:
                    owner.reset(token)
            while True:
                token = owner.set(attempt) if attempt else None
                try:
                    value = await anext(iterator)
                except StopAsyncIteration:
                    if attempt:
                        attempt.observe("completed", last)
                    return
                finally:
                    if token is not None:
                        owner.reset(token)
                if attempt and not attempt.passive:
                    last = _merge_usage(last, _usage(value))
                yield value
        except BaseException as error:
            if attempt:
                attempt.observe("cancelled" if isinstance(error, (asyncio.CancelledError, GeneratorExit)) else "failed")
            raise
        finally:
            if iterator is not None and hasattr(iterator, "aclose"):
                await iterator.aclose()

    def chat(self, messages, **kwargs):
        return self._call(self.wrapped.chat, messages, None, kwargs)

    async def achat(self, messages, **kwargs):
        return await self._acall(self.wrapped.achat, messages, None, kwargs)

    def stream_chat(self, messages, **kwargs):
        return self._stream(self.wrapped.stream_chat, messages, None, kwargs)

    async def astream_chat(self, messages, **kwargs):
        return self._astream(self.wrapped.astream_chat, messages, None, kwargs)

    def chat_with_tools(self, tools, user_msg=None, chat_history=None, **kwargs):
        return self._call(self.wrapped.chat_with_tools, chat_history or [], tools, {"user_msg": user_msg, **kwargs})

    async def achat_with_tools(self, tools, user_msg=None, chat_history=None, **kwargs):
        return await self._acall(self.wrapped.achat_with_tools, chat_history or [], tools, {"user_msg": user_msg, **kwargs})

    def stream_chat_with_tools(self, tools, user_msg=None, chat_history=None, **kwargs):
        return self._stream(self.wrapped.stream_chat_with_tools, chat_history or [], tools, {"user_msg": user_msg, **kwargs})

    async def astream_chat_with_tools(self, tools, user_msg=None, chat_history=None, **kwargs):
        return self._astream(self.wrapped.astream_chat_with_tools, chat_history or [], tools, {"user_msg": user_msg, **kwargs})

    def _prepare_chat_with_tools(self, *args, **kwargs):
        # Abstract extension contract only; public tool methods delegate above.
        raise NotImplementedError("Caveman delegates through public chat_with_tools methods")

    def get_tool_calls_from_response(self, response, **kwargs):
        return self.wrapped.get_tool_calls_from_response(response, **kwargs)

    def complete(self, prompt, formatted=False, **kwargs):
        return self._call(self.wrapped.complete, prompt, None, {"formatted": formatted, **kwargs}, passthrough="no_candidate")

    async def acomplete(self, prompt, formatted=False, **kwargs):
        return await self._acall(self.wrapped.acomplete, prompt, None, {"formatted": formatted, **kwargs}, passthrough="no_candidate")

    def stream_complete(self, prompt, formatted=False, **kwargs):
        return self._passive_stream(self.wrapped.stream_complete, (prompt,), {"formatted": formatted, **kwargs}, "no_candidate")

    async def astream_complete(self, prompt, formatted=False, **kwargs):
        return await self._passive_astream(self.wrapped.astream_complete, (prompt,), {"formatted": formatted, **kwargs}, "no_candidate")

    def structured_predict(self, *args, **kwargs):
        return self._call(lambda _: self.wrapped.structured_predict(*args, **kwargs), None, None, {}, passthrough="structured_output")

    async def astructured_predict(self, *args, **kwargs):
        return await self._acall(lambda _: self.wrapped.astructured_predict(*args, **kwargs), None, None, {}, passthrough="structured_output")

    def stream_structured_predict(self, *args, **kwargs):
        return self._passive_stream(self.wrapped.stream_structured_predict, args, kwargs, "structured_output")

    async def astream_structured_predict(self, *args, **kwargs):
        return await self._passive_astream(self.wrapped.astream_structured_predict, args, kwargs, "structured_output")

    def as_structured_llm(self, *args, **kwargs):
        return self.wrapped.as_structured_llm(*args, **kwargs).model_copy(update={"llm": self})


class CavemanFunctionAgent(FunctionAgent):
    """Native FunctionAgent with stable recovery registration before first call.

    Pass the existing LLM and tools as usual. The native workflow still owns
    tool authorization, state, structured output, iteration limits, and events.
    """
    caveman_recovery: Any = Field(exclude=True)

    def __init__(self, *, runtime, scope, llm: LLM, tools=None, **kwargs):
        if not _check_version(runtime):
            super().__init__(llm=CavemanLLM(llm, runtime=runtime, scope=scope), tools=tools, caveman_recovery=None, **kwargs)
            return
        registration = _Recovery(runtime, scope)
        selected = list(tools or [])
        names = [tool.metadata.name if hasattr(tool, "metadata") else getattr(tool, "__name__", None) for tool in selected]
        if "caveman_retrieve" not in names:
            selected.append(registration.tool)
        super().__init__(llm=CavemanLLM(llm, runtime=runtime, scope=scope, registration=registration),
                         tools=selected, caveman_recovery=registration, **kwargs)

    async def take_step(self, ctx, llm_input, tools, memory):
        if self.caveman_recovery is None:
            return await super().take_step(ctx, llm_input, tools, memory)
        successful = await ctx.store.get("caveman_llama_index_successful_tools", default={})
        current = _Invocation(self.caveman_recovery, _scope(self.caveman_recovery.scope, ctx), tools, successful,
                              self.output_cls is None and self.structured_output_fn is None)
        token = _invocation.set(current)
        try:
            return await super().take_step(ctx, llm_input, tools, memory)
        finally:
            _invocation.reset(token)

    async def handle_tool_call_results(self, ctx, results, memory):
        await super().handle_tool_call_results(ctx, results, memory)
        if self.caveman_recovery is None:
            return
        successful = dict(await ctx.store.get("caveman_llama_index_successful_tools", default={}))
        for result in results:
            successful.pop(result.tool_id, None)
            if not result.tool_output.is_error and result.tool_name != "caveman_retrieve" and len(successful) < 4096:
                successful[result.tool_id] = result.tool_name
        await ctx.store.set("caveman_llama_index_successful_tools", successful)


class CavemanNodePostprocessor(BaseNodePostprocessor):
    """Model views preserving indexed/cached nodes and provenance.

    Install source_expansion.execute in the application's source reader before
    supplying that runtime-owned RecoveryBinding for this scope. Without that
    reader, an ordinary synthesizer only receives recovery-free transforms.
    Offset-cited nodes and unknown node subclasses always remain untouched.
    """
    runtime: Any = Field(exclude=True)
    scope: Any = Field(exclude=True)
    source_expansion: Any = Field(default=None, exclude=True)

    def __init__(self, *, runtime, scope, source_expansion=None, **kwargs):
        super().__init__(runtime=runtime, scope=scope, source_expansion=source_expansion, **kwargs)

    def _options(self, nodes, query_bundle):
        if not _check_version(self.runtime) or owner.get() is not None:
            return None
        if type(nodes) is not list or any(type(item) is not NodeWithScore for item in nodes):
            return None
        try:
            context = manifest([item.model_dump(mode="json", warnings="error") for item in nodes])
        except (TypeError, ValueError, PydanticSerializationError, RecursionError):
            return None
        if context is None:
            return None
        candidates = [Candidate(f"node-{i}", item.node.text, item.node.node_id, kind="artifact")
                      for i, item in enumerate(nodes) if type(item.node) is TextNode
                      and item.node.start_char_idx is None and item.node.end_char_idx is None
                      and "citations" not in item.node.metadata]
        selected_scope = _scope(self.scope, query_bundle)
        reader = self.source_expansion
        binding = reader if isinstance(reader, RecoveryBinding) and callable(reader.execute) and self.runtime.owns_binding(reader, selected_scope) else None
        return dict(scope=selected_scope, adapter=RAG_ADAPTER, candidates=candidates,
                    manifest=context, binding=binding)

    @staticmethod
    def _apply(nodes, result):
        replacements = {r["segment_id"]: r["text"] for r in result.replacements}
        return [item.model_copy(update={"node": item.node.model_copy(update={"text": replacements[f"node-{i}"]})})
                if f"node-{i}" in replacements else item for i, item in enumerate(nodes)]

    def _postprocess_nodes(self, nodes, query_bundle=None):
        if owner.get() is not None:
            return nodes
        options = self._options(nodes, query_bundle)
        if options is None:
            self._report_original()
            return nodes
        result = self.runtime.optimize(**options)
        return self._finish(nodes, result, options, query_bundle)

    async def _apostprocess_nodes(self, nodes, query_bundle=None):
        if owner.get() is not None:
            return nodes
        options = self._options(nodes, query_bundle)
        if options is None:
            self._report_original()
            return nodes
        result = await _async_runtime(self.runtime).optimize(**options)
        return self._finish(nodes, result, options, query_bundle)

    def _report_original(self):
        reason = "disabled" if self.runtime.mode == "off" else "unsupported_version" if not matches_framework(("llama-index-core", "0.14", "0.15")) else "opaque_payload"
        self.runtime.report(reason=reason, adapter=RAG_ADAPTER.id, logical_call_id=str(uuid.uuid4()), attempt_id=str(uuid.uuid4()))

    def _finish(self, nodes, result, options, query_bundle):
        selected = {candidate.id for candidate in options["candidates"]}
        current = self._reader_current(options, query_bundle)
        valid = all(replacement["segment_id"] in selected for replacement in result.replacements)
        view = self._apply(nodes, result) if current and valid else nodes
        self.runtime.report(result if current and valid else None,
                            reason="recovery_unavailable" if not current else "invalid_plan", adapter=RAG_ADAPTER.id,
                            logical_call_id=str(uuid.uuid4()), attempt_id=str(uuid.uuid4()))
        return view

    def _reader_current(self, options, query_bundle):
        reader = options["binding"]
        try:
            return self.runtime.mode != "off" and (reader is None or (self.source_expansion is reader and _scope(self.scope, query_bundle) == options["scope"]
                                      and self.runtime.owns_binding(reader, options["scope"])))
        except Exception:
            return False


def with_caveman_model(llm: LLM, *, runtime, scope) -> LLM:
    """Wrap an existing LLM. No recovery executor is implied by this function."""
    return CavemanLLM(llm, runtime=runtime, scope=scope)


def with_caveman_tools(llm: LLM, *, runtime, scope, tools=()) -> CavemanLLMTools:
    """Bundle native tools and their executor for an application-owned loop.

    Pass bundle.tools to bundle.model.chat_with_tools (or its native async or
    streaming counterpart), then dispatch parsed native ToolSelection objects
    through execute/aexecute. Keep the original ToolOutput in application
    history. This function never schedules calls or runs a second tool loop.
    """
    selected_scope = _scope(scope)
    enabled = _check_version(runtime) and _protocol(llm, runtime) is not None
    registration = _ApplicationTools(runtime, selected_scope, tools, enabled=enabled)
    model = CavemanLLM(llm, runtime=runtime, scope=selected_scope, registration=registration)
    return CavemanLLMTools(model, registration.tools, registration)
