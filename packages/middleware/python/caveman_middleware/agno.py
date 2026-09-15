"""Agno's native Model loop with a delegate at every provider invocation."""
from __future__ import annotations

import asyncio
import contextvars
import copy
import functools
import inspect
import json
import uuid
from dataclasses import dataclass, field, fields
from importlib.metadata import version

try:
    from agno.models.base import Model
    from agno.models.message import Message
    from agno.run.base import RunContext
    from agno.run.cancel import araise_if_cancelled, raise_if_cancelled
    from agno.tools.function import Function
except ModuleNotFoundError as error:
    raise ImportError("Install caveman-middleware[agno] to use the Agno adapter") from error

from caveman_cloud.middleware import Adapter, Candidate, MiddlewareError, MiddlewareRuntime, Scope
from caveman_cloud.middleware.runtime import RECOVERY_DESCRIPTION, RECOVERY_SCHEMA
from ._native import Attempt, manifest, owner, plain
from ._versions import matches_framework

ADAPTER = Adapter("agno", "0.1.0", "3.0.9", "agno-message-v1")
_SIGNATURES = {name: inspect.signature(getattr(Model, name)) for name in
               ("response", "aresponse", "response_stream", "aresponse_stream")}


def scope_from_run(run, *, namespace: str) -> Scope:
    """Resolve a native Agent or Team run's session, with explicit branch metadata."""
    session = getattr(run, "session_id", None)
    if not isinstance(session, str) or not session:
        raise ValueError("Agno middleware requires a nonempty native session_id")
    metadata = getattr(run, "metadata", None) or {}
    return Scope(namespace, session, metadata.get("caveman_branch_id", "main"), metadata.get("caveman_cache_epoch", "0"))


def _scope(source, run):
    result = source if isinstance(source, Scope) else source(run)
    if not isinstance(result, Scope):
        raise TypeError("Agno scope resolver must return a Caveman Scope")
    return result


def _message_view(messages):
    if type(messages) is not list or any(type(message) is not Message for message in messages):
        return None
    # Metrics/timers and checkpoint bookkeeping are not model input. All other
    # native values participate in lineage; opaque values bypass the whole view.
    excluded = {"metrics", "created_at", "from_history", "checkpoint_status", "checkpoint_created_at"}
    try:
        context = manifest([message.model_dump(mode="python", exclude=excluded) for message in messages])
    except (TypeError, ValueError):
        return None
    if context is None:
        return None
    names = {call["id"]: call["function"]["name"] for message in messages for call in (message.tool_calls or [])
             if plain(call) and type(call.get("id")) is str and plain(call.get("function"))
             and type(call["function"].get("name")) is str}
    candidates, paths = [], {}
    for mi, message in enumerate(messages):
        name = names.get(message.tool_call_id, message.tool_name)
        if (message.role != "tool" or not message.tool_call_id or not name or name == "caveman_retrieve"
                or message.tool_call_error or message.citations or message.compressed_content is not None):
            continue
        parts = [(None, message.content)] if type(message.content) is str else [
            (pi, part["text"]) for pi, part in enumerate(message.content or [])
            if plain(part) and set(part) == {"type", "text"} and part["type"] == "text" and type(part["text"]) is str]
        for pi, text in parts:
            key = f"message-{mi}.part-{pi if pi is not None else 0}"
            candidates.append(Candidate(key, text, message.id))
            paths[key] = (mi, pi)
    return context, candidates, paths


def _apply(messages, optimization, paths):
    if not optimization.replacements or any(r["segment_id"] not in paths for r in optimization.replacements):
        return messages
    view = messages.copy()
    for replacement in optimization.replacements:
        mi, pi = paths[replacement["segment_id"]]
        content = replacement["text"]
        if pi is not None:
            content = view[mi].content.copy()
            content[pi] = {**content[pi], "text": replacement["text"]}
        view[mi] = view[mi].model_copy(update={"content": content})
    return view


def _usage(response):
    metrics = getattr(response, "response_usage", None)
    if metrics is None:
        return None
    # Agno's native provider parsers normalize these fields. Absent usage stays
    # absent; we never read the default counters on an assistant Message.
    def count(name):
        value = getattr(metrics, name, None)
        return value if type(value) is int and 0 <= value <= 2**53 - 1 else None
    return {"provenance": "client_observed_sdk", "complete": count("input_tokens") is not None and count("output_tokens") is not None,
            **{name: count(name) for name in ("input_tokens", "output_tokens", "cache_read_tokens", "cache_write_tokens", "reasoning_tokens")}}


@dataclass
class _Stream:
    iterator: object
    attempt: Attempt | None
    finished: bool = False

    def finish(self, event, response=None):
        if not self.finished:
            self.finished = True
            if self.attempt is not None:
                self.attempt.observe(event, _usage(response) if event == "completed" else None)


@dataclass
class _Frame:
    scope: Scope
    tools: list
    run_id: str | None = None
    streams: list[_Stream] = field(default_factory=list)


class _Connection:
    def __init__(self, runtime, scope):
        self.sync = runtime if isinstance(runtime, MiddlewareRuntime) else None
        self.async_runtime = runtime.as_async() if self.sync else runtime
        self.scope, self.recovery_tool = scope, None
        self.active = contextvars.ContextVar("caveman_agno_run", default=None)
        self.version_supported = matches_framework(("agno", "3.0", "4"))
        if not self.version_supported and runtime.mode != "off":
            runtime.decline("unsupported_version")

    def passive(self, runtime, reason):
        return Attempt(runtime, None, str(uuid.uuid4()), str(uuid.uuid4()), passive=True, reason=reason, adapter=ADAPTER.id), {}, None, None

    def register(self):
        def recover(handle: str, run_context: RunContext, offset: int = 0, limit: int = 262144, query: str = ""):
            frame = self.active.get()
            if frame is None:
                raise MiddlewareError("recovery_unavailable")
            if self.sync is None:
                raise TypeError("Synchronous Agno recovery requires MiddlewareRuntime")
            if run_context is not None:
                raise_if_cancelled(run_context.run_id)
            return json.dumps(self.sync.retrieve(frame.scope, handle=handle, offset=offset, limit=limit, query=query),
                              ensure_ascii=False, separators=(",", ":"))

        async def arecover(handle: str, run_context: RunContext, offset: int = 0, limit: int = 262144, query: str = ""):
            frame = self.active.get()
            if frame is None:
                raise MiddlewareError("recovery_unavailable")
            if run_context is not None:
                await araise_if_cancelled(run_context.run_id)
            return json.dumps(await self.async_runtime.retrieve(frame.scope, handle=handle, offset=offset, limit=limit, query=query),
                              ensure_ascii=False, separators=(",", ":"))

        # Agno preserves a Function's entrypoint identity in its per-run copy.
        # Explicit processing avoids schema rewriting and keeps RunContext hidden.
        self.recovery_tool = Function(name="caveman_retrieve", description=RECOVERY_DESCRIPTION,
            parameters=copy.deepcopy(RECOVERY_SCHEMA), entrypoint=recover, skip_entrypoint_processing=True)
        self.recovery_entrypoint = recover
        self.async_recovery = arecover

    def binding_details(self, frame, options):
        if not frame or not self.recovery_tool or options.get("response_format") is not None or options.get("tool_choice") not in (None, "auto"):
            return None
        registered = [tool for tool in frame.tools if isinstance(tool, Function) and tool.name == "caveman_retrieve"]
        actual = [tool.get("function") for tool in (options.get("tools") or [])
                  if plain(tool) and plain(tool.get("function")) and tool["function"].get("name") == "caveman_retrieve"]
        if (len(registered) == 1 and (registered[0].entrypoint is self.recovery_entrypoint or registered[0].entrypoint is self.async_recovery)
                and not registered[0].external_execution and not registered[0].stop_after_tool_call
                and not registered[0].requires_confirmation and not registered[0].requires_user_input
                and len(actual) == 1 and actual[0] == registered[0].to_dict()
                and actual[0].get("parameters") == RECOVERY_SCHEMA and actual[0].get("description") == RECOVERY_DESCRIPTION):
            return actual[0]
        return None

    def state(self, model, messages, options, runtime):
        if owner.get() is not None:
            return None
        if runtime.mode == "off":
            return self.passive(runtime, "disabled")
        if not self.version_supported:
            return self.passive(runtime, "unsupported_version")
        if options.get("compress_tool_results"):
            return self.passive(runtime, "host_compression")
        selected = _message_view(messages)
        if selected is None:
            return self.passive(runtime, "unsupported_shape")
        context, candidates, paths = selected
        frame = self.active.get()
        scope = frame.scope if frame else _scope(self.scope, options.get("run_response"))
        binding, overhead = None, None
        actual = self.binding_details(frame, options)
        if actual is not None:
            binding = runtime.recovery(scope)
            overhead = json.dumps({"type": "function", "function": actual}, ensure_ascii=False, separators=(",", ":"))
        attempt = Attempt(runtime, scope, str(uuid.uuid4()), str(uuid.uuid4()), adapter=ADAPTER.id)
        recheck = lambda: binding is None or (runtime.owns_binding(binding, scope) and self.binding_details(frame, options) is not None)
        runtime_options = dict(scope=scope, adapter=ADAPTER, manifest=context, candidates=candidates, binding=binding,
            model={"provider": model.provider or type(model).__name__, "id": model.id, "protocol": "agno"}, recovery_overhead_text=overhead,
            logical_call_id=attempt.logical_call_id, attempt_id=attempt.attempt_id)
        return attempt, paths, runtime_options, recheck


class CavemanModel(Model):
    """Native Agno Model delegate. Model-only use has no recovery executor.

    Agno's Model.response methods retain orchestration. Only invoke/ainvoke and
    their streaming equivalents receive a separate model-facing Message view.
    The caller owns both its existing provider clients and the shared runtime.
    """
    def __init__(self, model: Model, *, runtime=None, scope=None, connection=None):
        if not isinstance(model, Model):
            raise TypeError("Expected an existing native Agno Model")
        super().__init__(**{item.name: getattr(model, item.name) for item in fields(Model) if not item.name.startswith("_")})
        self.model = model
        self.connection = connection or _Connection(runtime, scope)
        self.signatures = {name: inspect.signature(getattr(model, name)) for name in ("invoke", "ainvoke", "invoke_stream", "ainvoke_stream")}

    def get_provider(self):
        return self.model.get_provider()

    def to_dict(self):
        return self.model.to_dict()

    def count_tokens(self, *args, **kwargs):
        return self.model.count_tokens(*args, **kwargs)

    async def acount_tokens(self, *args, **kwargs):
        return await self.model.acount_tokens(*args, **kwargs)

    def get_system_message_for_model(self, tools=None):
        return self.model.get_system_message_for_model(tools)

    def get_instructions_for_model(self, tools=None):
        return self.model.get_instructions_for_model(tools)

    def parse_tool_calls(self, tool_calls_data):
        return self.model.parse_tool_calls(tool_calls_data)

    def _parse_provider_response(self, response, **kwargs):
        return self.model._parse_provider_response(response, **kwargs)

    def _parse_provider_response_delta(self, response):
        return self.model._parse_provider_response_delta(response)

    def _frame(self, name, args, kwargs):
        if self.connection.async_runtime.mode == "off" or not self.connection.version_supported:
            return _Frame(None, [])
        values = _SIGNATURES[name].bind_partial(self, *args, **kwargs).arguments
        run = values.get("run_response")
        return _Frame(_scope(self.connection.scope, run), values.get("tools") or [], getattr(run, "run_id", None))

    def _check_cancelled(self):
        frame = self.connection.active.get()
        if frame and frame.run_id:
            raise_if_cancelled(frame.run_id)

    async def _acheck_cancelled(self):
        frame = self.connection.active.get()
        if frame and frame.run_id:
            await araise_if_cancelled(frame.run_id)

    def _async_tools(self, name, args, kwargs):
        bound = _SIGNATURES[name].bind_partial(self, *args, **kwargs)
        tools = bound.arguments.get("tools")
        registered = self.connection.recovery_tool
        if registered and tools:
            view = []
            for tool in tools:
                if isinstance(tool, Function) and tool.entrypoint is registered.entrypoint:
                    tool = tool.model_copy()
                    tool.entrypoint = self.connection.async_recovery
                view.append(tool)
            bound.arguments["tools"] = view
        return bound.args[1:], bound.kwargs

    def response(self, *args, **kwargs):
        token = self.connection.active.set(self._frame("response", args, kwargs))
        try:
            return super().response(*args, **kwargs)
        finally:
            self.connection.active.reset(token)

    async def aresponse(self, *args, **kwargs):
        args, kwargs = self._async_tools("aresponse", args, kwargs)
        token = self.connection.active.set(self._frame("aresponse", args, kwargs))
        try:
            return await super().aresponse(*args, **kwargs)
        finally:
            self.connection.active.reset(token)

    def response_stream(self, *args, **kwargs):
        frame = self._frame("response_stream", args, kwargs)
        iterator = super().response_stream(*args, **kwargs)
        try:
            while True:
                token = self.connection.active.set(frame)
                try:
                    event = next(iterator)
                except StopIteration:
                    return
                finally:
                    self.connection.active.reset(token)
                yield event
        finally:
            iterator.close()
            for stream in frame.streams:
                if not stream.finished:
                    stream.finish("cancelled")
                    stream.iterator.close()

    async def aresponse_stream(self, *args, **kwargs):
        args, kwargs = self._async_tools("aresponse_stream", args, kwargs)
        frame = self._frame("aresponse_stream", args, kwargs)
        iterator = super().aresponse_stream(*args, **kwargs)
        try:
            while True:
                token = self.connection.active.set(frame)
                try:
                    event = await anext(iterator)
                except StopAsyncIteration:
                    return
                finally:
                    self.connection.active.reset(token)
                yield event
        finally:
            await iterator.aclose()
            for stream in frame.streams:
                if not stream.finished:
                    stream.finish("cancelled")
                    await stream.iterator.aclose()

    def _prepare(self, name, messages, args, kwargs, runtime):
        if owner.get() is not None:
            return None
        if runtime.mode == "off" or not self.connection.version_supported:
            return self.connection.passive(runtime, "disabled" if runtime.mode == "off" else "unsupported_version")
        values = self.signatures[name].bind_partial(messages, *args, **kwargs).arguments
        return self.connection.state(self.model, messages, values, runtime)

    def _apply_prepared(self, messages, state, outcome):
        attempt, paths, _, recheck = state
        attempt.optimization = outcome if not outcome.replacements else None
        if not recheck():
            attempt.reason = "recovery_unavailable"
            return messages
        if any(item["segment_id"] not in paths for item in outcome.replacements):
            attempt.reason = "invalid_plan"
            return messages
        view = _apply(messages, outcome, paths)
        attempt.optimization = outcome
        return view

    def invoke(self, messages, *args, **kwargs):
        self._check_cancelled()
        runtime = self.connection.sync
        if runtime is None:
            raise TypeError("Synchronous Agno calls require MiddlewareRuntime")
        state = self._prepare("invoke", messages, args, kwargs, runtime)
        attempt = None
        if state:
            attempt, _, options, _ = state
            if options is not None:
                messages = self._apply_prepared(messages, state, runtime.optimize(**options))
        self._check_cancelled()
        if attempt:
            attempt.observe("dispatch_intent")
        token = owner.set(attempt) if attempt else None
        try:
            result = self.model.invoke(messages, *args, **kwargs)
            if attempt:
                attempt.observe("completed", _usage(result))
            return result
        except BaseException:
            if attempt:
                attempt.observe("failed")
            raise
        finally:
            if token is not None:
                owner.reset(token)

    async def ainvoke(self, messages, *args, **kwargs):
        await self._acheck_cancelled()
        runtime = self.connection.async_runtime
        state = self._prepare("ainvoke", messages, args, kwargs, runtime)
        attempt = None
        if state:
            attempt, _, options, _ = state
            if options is not None:
                messages = self._apply_prepared(messages, state, await runtime.optimize(**options))
        await self._acheck_cancelled()
        if attempt:
            attempt.observe("dispatch_intent")
        token = owner.set(attempt) if attempt else None
        try:
            result = await self.model.ainvoke(messages, *args, **kwargs)
            if attempt:
                attempt.observe("completed", _usage(result))
            return result
        except asyncio.CancelledError:
            if attempt:
                attempt.observe("cancelled")
            raise
        except BaseException:
            if attempt:
                attempt.observe("failed")
            raise
        finally:
            if token is not None:
                owner.reset(token)

    def invoke_stream(self, messages, *args, **kwargs):
        self._check_cancelled()
        runtime = self.connection.sync
        if runtime is None:
            raise TypeError("Synchronous Agno calls require MiddlewareRuntime")
        state = self._prepare("invoke_stream", messages, args, kwargs, runtime)
        attempt = None
        if state:
            attempt, _, options, _ = state
            if options is not None:
                messages = self._apply_prepared(messages, state, runtime.optimize(**options))
        self._check_cancelled()
        if attempt:
            attempt.observe("dispatch_intent")
        stream = _Stream(iter(self.model.invoke_stream(messages, *args, **kwargs)), attempt)
        frame = self.connection.active.get()
        if frame:
            frame.streams.append(stream)
        last = None
        try:
            while True:
                token = owner.set(attempt) if attempt else None
                try:
                    event = next(stream.iterator)
                except StopIteration:
                    stream.finish("completed", last)
                    return
                finally:
                    if token is not None:
                        owner.reset(token)
                if event.response_usage is not None:
                    last = event
                yield event
        except GeneratorExit:
            stream.finish("cancelled")
            raise
        except BaseException:
            stream.finish("failed")
            raise
        finally:
            stream.iterator.close()

    async def ainvoke_stream(self, messages, *args, **kwargs):
        await self._acheck_cancelled()
        runtime = self.connection.async_runtime
        state = self._prepare("ainvoke_stream", messages, args, kwargs, runtime)
        attempt = None
        if state:
            attempt, _, options, _ = state
            if options is not None:
                messages = self._apply_prepared(messages, state, await runtime.optimize(**options))
        await self._acheck_cancelled()
        if attempt:
            attempt.observe("dispatch_intent")
        stream = _Stream(self.model.ainvoke_stream(messages, *args, **kwargs).__aiter__(), attempt)
        frame = self.connection.active.get()
        if frame:
            frame.streams.append(stream)
        last = None
        try:
            while True:
                token = owner.set(attempt) if attempt else None
                try:
                    event = await anext(stream.iterator)
                except StopAsyncIteration:
                    stream.finish("completed", last)
                    return
                finally:
                    if token is not None:
                        owner.reset(token)
                if event.response_usage is not None:
                    last = event
                yield event
        except (asyncio.CancelledError, GeneratorExit):
            stream.finish("cancelled")
            raise
        except BaseException:
            stream.finish("failed")
            raise
        finally:
            await stream.iterator.aclose()


def with_caveman_model(model: Model, *, runtime, scope):
    return CavemanModel(model, runtime=runtime, scope=scope)


def _has_recovery(tool):
    if plain(tool):
        return tool.get("name") == "caveman_retrieve" or (plain(tool.get("function")) and tool["function"].get("name") == "caveman_retrieve")
    return (getattr(tool, "name", None) == "caveman_retrieve" or getattr(tool, "__name__", None) == "caveman_retrieve"
            or "caveman_retrieve" in (getattr(tool, "functions", None) or {}))


def with_caveman_agent(options: dict, *, runtime, scope) -> dict:
    """Return native Agent/Team constructor options, including scoped recovery.

    Pass an existing native Model. Dynamic tool factories keep their native
    invocation signature. Reasoning/output/parser/fallback models, when supplied,
    use the same connection but require recovery in their own actual tool list.
    """
    connection = _Connection(runtime, scope)
    if runtime.mode == "off" or not connection.version_supported:
        result = dict(options)
        for name in ("model", "reasoning_model", "parser_model", "output_model", "followup_model"):
            if name == "model" or result.get(name) is not None:
                result[name] = CavemanModel(result.get(name), connection=connection)
        if result.get("fallback_models"):
            result["fallback_models"] = [CavemanModel(model, connection=connection) for model in result["fallback_models"]]
        return result
    connection.register()

    def add_tools(tools):
        result = list(tools or [])
        if (runtime.mode == "compress" and options.get("output_schema") is None and options.get("tool_choice") in (None, "auto")
                and not any(_has_recovery(tool) for tool in result)):
            result.append(connection.recovery_tool)
        return result

    tools = options.get("tools")
    if callable(tools):
        if inspect.iscoroutinefunction(tools):
            @functools.wraps(tools)
            async def factory(*args, **kwargs):
                return add_tools(await tools(*args, **kwargs))
        else:
            @functools.wraps(tools)
            def factory(*args, **kwargs):
                return add_tools(tools(*args, **kwargs))
        registered = factory
    else:
        registered = add_tools(tools)
    result = {**options, "tools": registered}
    for name in ("model", "reasoning_model", "parser_model", "output_model", "followup_model"):
        if name == "model" or result.get(name) is not None:
            result[name] = CavemanModel(result.get(name), connection=connection)
    if result.get("fallback_models"):
        result["fallback_models"] = [CavemanModel(model, connection=connection) for model in result["fallback_models"]]
    return result
