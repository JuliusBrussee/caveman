"""CrewAI 1.15.20 model delegates with native, scoped recovery tools.

The PRE_MODEL_CALL hook supplies the real executor identity. It never changes
the executor's message list; the public BaseLLM delegate makes a provider view.
"""
from __future__ import annotations

import asyncio
import contextvars
import json
import threading
import uuid
import weakref
from contextlib import ExitStack
from dataclasses import dataclass, field
from importlib.metadata import version
from typing import Any

try:
    from crewai import BaseLLM
    from crewai.events import LLMCallCompletedEvent, crewai_event_bus
    from crewai.hooks import InterceptionPoint, on, unregister_hook
    from crewai.llms.base_llm import call_stop_override, call_stream_override
    from crewai.tools import BaseTool
    from crewai.utilities.agent_utils import convert_tools_to_openai_schema
    from crewai.utilities.string_utils import sanitize_tool_name
    from pydantic import BaseModel, ConfigDict, Field, PrivateAttr
except ModuleNotFoundError as error:
    raise ImportError("Install caveman-middleware[crewai] to use the CrewAI adapter") from error

from caveman_cloud.middleware import Adapter, Candidate, MiddlewareError, MiddlewareRuntime, Scope
from caveman_cloud.middleware.runtime import RECOVERY_DESCRIPTION
from ._native import Attempt, leaves, manifest, owner, plain, replace_path
from ._versions import matches_framework
from ._usage import usage

FRAMEWORK_VERSION = version("crewai")
ADAPTER = Adapter("crewai", "0.1.0", FRAMEWORK_VERSION, "crewai-messages-v1")
_call = contextvars.ContextVar("caveman_crewai_call", default=None)


class _RecoveryInput(BaseModel):
    model_config = ConfigDict(extra="forbid")
    handle: str = Field(pattern=r"^cmw_[a-f0-9]{48}$")
    offset: int = Field(default=0, ge=0)
    limit: int = Field(default=262144, ge=4, le=262144)
    query: str = Field(default="", max_length=1024)


def _never_cache(_args=None, _result=None):
    return False


class CavemanRecoveryTool(BaseTool):
    """Executed by CrewAI's existing native tool scheduler."""
    name: str = "caveman_retrieve"
    description: str = RECOVERY_DESCRIPTION
    args_schema: type[BaseModel] = _RecoveryInput
    cache_function: Any = _never_cache
    _binding: Any = PrivateAttr()
    _model: Any = PrivateAttr()

    def __init__(self, model):
        super().__init__()
        self._binding = model.runtime.recovery(model.scope)
        self._model = weakref.ref(model)

    def _run(self, handle, offset=0, limit=262144, query=""):
        model = self._model()
        if model is None or model.closed:
            raise MiddlewareError("recovery_unavailable")
        result = self._binding.execute(handle=handle, offset=offset, limit=limit, query=query)
        return json.dumps(result, ensure_ascii=False, separators=(",", ":"))


@dataclass
class _Call:
    model: Any
    attempt: Attempt
    finished: bool = False
    lock: Any = field(default_factory=threading.Lock)

    def finish(self, event, measured=None):
        with self.lock:
            if self.finished:
                return
            self.finished = True
        self.attempt.observe(event, measured)


class CavemanLLM(BaseLLM):
    """Wrap a native CrewAI LLM; assign a separate trusted Scope per context.

    Use ``with_caveman_agent`` to register the matching recovery BaseTool.
    Register before creating the Agent, whose executor snapshots native hooks.
    ``close`` removes only this delegate's registrations. The application keeps
    ownership of the native provider client and the Caveman runtime.
    """
    llm_type: str = "caveman"
    delegate: BaseLLM = Field(exclude=True, repr=False)
    runtime: Any = Field(exclude=True, repr=False)
    scope: Scope
    closed: bool = Field(default=False, exclude=True)
    _context: Any = PrivateAttr()
    _hook: Any = PrivateAttr(default=None)
    _completed: Any = PrivateAttr(default=None)
    _recovery: Any = PrivateAttr(default=None)

    def __init__(self, delegate, *, runtime, scope):
        if not isinstance(delegate, BaseLLM):
            raise TypeError("Expected an installed CrewAI BaseLLM")
        if not isinstance(runtime, MiddlewareRuntime) or not isinstance(scope, Scope):
            raise TypeError("CrewAI requires a MiddlewareRuntime and a stable Scope per agent context")
        super().__init__(delegate=delegate, runtime=runtime, scope=scope, model=delegate.model,
                         provider=delegate.provider, stream=delegate.stream, stop=list(delegate.stop),
                         is_litellm=delegate.is_litellm)
        self._context = contextvars.ContextVar(f"caveman_crewai_context_{id(self)}", default=None)
        supported = matches_framework(("crewai", "1.15", "2"))
        if not supported and runtime.mode != "off":
            runtime.decline("unsupported_version")
        if not supported or runtime.mode == "off":
            return
        reference = weakref.ref(self)

        @on(InterceptionPoint.PRE_MODEL_CALL)
        def before(context):
            model = reference()
            if model is not None and not model.closed and context.llm is model:
                # Store only a weak executor reference. Aborted hooks and stale
                # executor snapshots cannot retain complete message histories.
                model._context.set(weakref.ref(context.executor) if context.executor is not None else None)

        @crewai_event_bus.on(LLMCallCompletedEvent)
        def completed(source, event):
            state = _call.get()
            model = reference()
            if model is None or state is None or state.model is not model or source is not model.delegate:
                return
            measured = usage(event.usage)
            # Some native paths synthesize zero totals when no provider usage
            # was received. Those defaults are not billing measurements.
            if measured and measured["input_tokens"] == measured["output_tokens"] == 0:
                measured = None
            state.finish("completed", measured)

        self._hook, self._completed = before, completed

    @property
    def recovery_tool(self):
        if self._recovery is None:
            self._recovery = CavemanRecoveryTool(self)
        return self._recovery

    def close(self):
        self.closed = True
        if self._hook is not None:
            unregister_hook(InterceptionPoint.PRE_MODEL_CALL, self._hook)
        if self._completed is not None:
            crewai_event_bus.off(LLMCallCompletedEvent, self._completed)
        self._context.set(None)

    def supports_function_calling(self):
        function = getattr(self.delegate, "supports_function_calling", None)
        return function() if function is not None else False

    def supports_stop_words(self):
        return self.delegate.supports_stop_words()

    def get_context_window_size(self):
        return self.delegate.get_context_window_size()

    def supports_multimodal(self):
        return self.delegate.supports_multimodal()

    def format_text_content(self, text):
        return self.delegate.format_text_content(text)

    def get_file_uploader(self):
        return self.delegate.get_file_uploader()

    def get_token_usage_summary(self):
        return self.delegate.get_token_usage_summary()

    def to_config_dict(self):
        raise ValueError("Rebind CavemanLLM from the original provider configuration and trusted runtime; active middleware is not serializable")

    def _options(self, messages, tools, available_functions, from_task, from_agent, response_model):
        reference = self._context.get()
        self._context.set(None)
        executor = reference() if reference is not None else None
        if owner.get() is not None:
            return None
        reason = ("closed" if self.closed else "off" if self.runtime.mode == "off" else
                  "unsupported_version" if not matches_framework(("crewai", "1.15", "2")) else
                  "unsupported_shape" if type(messages) is not list else None)
        if reason:
            return Attempt(self.runtime, self.scope, str(uuid.uuid4()), str(uuid.uuid4()),
                           passive=True, reason=reason, adapter="crewai"), {}, None
        context = manifest(messages)
        if context is None:
            return Attempt(self.runtime, self.scope, str(uuid.uuid4()), str(uuid.uuid4()),
                           passive=True, reason="unsupported_shape", adapter="crewai"), {}, None
        bound = (executor is not None and executor.llm is self and executor.messages is messages
                 and executor.task is from_task and executor.agent is from_agent)
        originals = executor.original_tools if bound else []
        native_schemas, _, native_tools = convert_tools_to_openai_schema(originals)
        registered = [tool for tool in originals if sanitize_tool_name(tool.name) == "caveman_retrieve"]
        binding = None
        params = self.delegate.additional_params
        recovery_allowed = (bound and self._recovery is not None and len(registered) == 1
                            and registered[0] is self._recovery and tools == native_schemas
                            and available_functions is None and response_model is None
                            and not getattr(self.delegate, "response_format", None)
                            and getattr(self.delegate, "api", "completions") == "completions"
                            and params.get("tool_choice", "auto") == "auto"
                            and not any(params.get(key) for key in ("response_format", "output_config", "output_format", "previous_response_id")))
        if recovery_allowed:
            binding = self._recovery._binding
        selected = leaves({"messages": messages}, "openai-chat")
        paths, candidates = {}, []
        frozen = max((i for i, message in enumerate(messages) if plain(message) and message.get("cache_breakpoint")), default=-1)
        for index, (path, content) in enumerate(selected[1] if selected else []):
            message = messages[path[1]]
            native_tool = native_tools.get(message.get("name"))
            # CrewAI's native executor currently renders failures as text rather
            # than attaching is_error. Protect its known failure envelopes.
            failed = content.startswith(("Error executing tool:", "Tool execution blocked by hook.", "Tool not found", "Error:"))
            failed |= content.startswith("Tool '") and "has reached its usage limit" in content
            if failed or (bound and (native_tool is None or native_tool.result_schema is not None)):
                continue
            key = f"leaf-{index}"
            paths[key] = path[1:]
            candidates.append(Candidate(key, content, "/".join(map(str, path)),
                                        cache_region="frozen_prefix" if path[1] <= frozen else "live_zone"))
        attempt = Attempt(self.runtime, self.scope, str(uuid.uuid4()), str(uuid.uuid4()), adapter="crewai")
        options = dict(scope=self.scope, adapter=ADAPTER, candidates=candidates, manifest=context,
                       binding=binding, model={"provider": self.delegate.provider, "id": self.delegate.model, "protocol": "crewai"},
                       recovery_overhead_text=json.dumps(native_schemas, ensure_ascii=False, separators=(",", ":")) if binding else None,
                       logical_call_id=attempt.logical_call_id, attempt_id=attempt.attempt_id)
        return attempt, paths, options

    @staticmethod
    def _apply(messages, state, result):
        attempt, paths, _ = state
        view = messages
        if all(replacement["segment_id"] in paths for replacement in result.replacements):
            for replacement in result.replacements:
                view = replace_path(view, paths[replacement["segment_id"]], replacement["text"])
            attempt.optimization = result
            attempt.plan_id = result.plan["replacement_set_id"] if result.plan else None
        else:
            attempt.reason = "invalid_replacement_plan"
        return view, attempt

    def _native_call(self, attempt):
        stack = ExitStack()
        # These public CrewAI contexts preserve executor stop overrides without
        # mutating the shared provider instance. BaseLLM supplies stream mode.
        stack.enter_context(call_stop_override(self.delegate, self.stop_sequences))
        effective_stream = self._effective_stream()
        if effective_stream is not None:
            stack.enter_context(call_stream_override(self.delegate, effective_stream))
        if attempt is not None:
            state = _Call(self, attempt)
            owner_token, call_token = owner.set(attempt), _call.set(state)
            stack.callback(_call.reset, call_token)
            stack.callback(owner.reset, owner_token)
            attempt.observe("dispatch_intent")
        else:
            state = None
        return stack, state

    def call(self, messages, tools=None, callbacks=None, available_functions=None, from_task=None, from_agent=None, response_model=None):
        args = (tools, available_functions, from_task, from_agent, response_model)
        state = self._options(messages, *args)
        view, attempt = ((messages, None) if state is None else (messages, state[0]) if state[2] is None
                         else self._apply(messages, state, self.runtime.optimize(**state[2])))
        stack, active = self._native_call(attempt)
        with stack:
            try:
                result = self.delegate.call(view, tools=tools, callbacks=callbacks, available_functions=available_functions,
                                            from_task=from_task, from_agent=from_agent, response_model=response_model)
                self._finish_unreported_tool_response(active, result)
                return result
            except BaseException:
                if active:
                    active.finish("failed")
                raise

    async def acall(self, messages, tools=None, callbacks=None, available_functions=None, from_task=None, from_agent=None, response_model=None):
        args = (tools, available_functions, from_task, from_agent, response_model)
        state = self._options(messages, *args)
        view, attempt = ((messages, None) if state is None else (messages, state[0]) if state[2] is None
                         else self._apply(messages, state, await self.runtime.as_async().optimize(**state[2])))
        stack, active = self._native_call(attempt)
        with stack:
            try:
                result = await self.delegate.acall(view, tools=tools, callbacks=callbacks, available_functions=available_functions,
                                                  from_task=from_task, from_agent=from_agent, response_model=response_model)
                self._finish_unreported_tool_response(active, result)
                return result
            except asyncio.CancelledError:
                if active:
                    active.finish("cancelled")
                raise
            except BaseException:
                if active:
                    active.finish("failed")
                raise

    def _finish_unreported_tool_response(self, state, result):
        # CrewAI 1.15.20 returns LiteLLM tool calls and Anthropic streamed
        # tool-use blocks before emitting LLMCallCompletedEvent. Usage updates,
        # but taking a delta from that shared counter races concurrent calls.
        # Keep this call's accounting explicitly unknown.
        missing_event = self.delegate.is_litellm or (self.delegate.provider == "anthropic" and self._effective_stream())
        if state is not None and missing_event and isinstance(result, list):
            state.finish("completed")


def with_caveman_llm(llm, *, runtime, scope):
    return CavemanLLM(llm, runtime=runtime, scope=scope)


def with_caveman_agent(options, *, runtime, scope):
    """Return native Agent constructor options, preserving its native scheduler."""
    model = CavemanLLM(options["llm"], runtime=runtime, scope=scope)
    tools = list(options.get("tools", []))
    # Adding the first tool switches CrewAI out of its native no-tool/typed
    # response path. There is no eligible tool result in that path anyway.
    params = model.delegate.additional_params
    recovery_allowed = (runtime.mode == "compress" and matches_framework(("crewai", "1.15", "2")) and tools
                        and not getattr(model.delegate, "response_format", None)
                        and params.get("tool_choice", "auto") == "auto"
                        and not any(params.get(key) for key in ("response_format", "output_config", "output_format")))
    if recovery_allowed and not any(sanitize_tool_name(tool.name) == "caveman_retrieve" for tool in tools):
        tools.append(model.recovery_tool)
    return {**options, "llm": model, "tools": tools}
