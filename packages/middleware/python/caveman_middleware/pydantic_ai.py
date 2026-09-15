"""Pydantic AI capabilities and native, copy-on-write model delegation."""
from __future__ import annotations

import asyncio
import copy
import json
import uuid
from contextlib import asynccontextmanager
from dataclasses import replace
from importlib.metadata import version
from typing import Any

try:
    from pydantic_ai import RunContext, Tool
    from pydantic_ai.capabilities import AbstractCapability
    from pydantic_ai.messages import (
        ModelMessage, ModelMessagesTypeAdapter, ModelRequest, ModelResponse,
        ToolCallPart, ToolReturnPart,
    )
    from pydantic_ai.models import Model, ModelRequestContext, ModelRequestParameters
    from pydantic_ai.models.wrapper import WrapperModel
    from pydantic_ai.toolsets import FunctionToolset
    from pydantic_core import PydanticSerializationError
except ModuleNotFoundError as error:
    raise ImportError("Install caveman-middleware[pydantic-ai] to use the Pydantic AI adapter") from error

from caveman_cloud.middleware import Adapter, Candidate, MiddlewareError, MiddlewareRuntime, Scope
from caveman_cloud.middleware.runtime import RECOVERY_DESCRIPTION, RECOVERY_SCHEMA
from ._native import Attempt, manifest, owner
from ._versions import supports_framework

ADAPTER = Adapter("pydantic-ai", "0.1.0", "2.42.0", "pydantic-ai-message-v1")


def scope_from_run(ctx: RunContext, *, namespace: str) -> Scope:
    """Use native conversation identity plus application-owned branch metadata."""
    if not isinstance(ctx.conversation_id, str) or not ctx.conversation_id:
        raise ValueError("Pydantic AI middleware requires a native conversation_id")
    metadata = ctx.metadata or {}
    return Scope(namespace, ctx.conversation_id, metadata.get("caveman_branch_id", "main"),
                 metadata.get("caveman_cache_epoch", "0"))


def _scope(source, ctx=None):
    scope = source if isinstance(source, Scope) else source(ctx)
    if not isinstance(scope, Scope):
        raise TypeError("Pydantic AI scope resolver must return a Caveman Scope")
    return scope


def _runtime(runtime):
    return runtime.as_async() if isinstance(runtime, MiddlewareRuntime) else runtime


def _check_version(runtime):
    return supports_framework(runtime, ("pydantic-ai-slim", "2.42", "3"))


def _protocol(model):
    # Capability-only integration wraps the model selected for this request.
    # Routing containers and other providers stay opaque until separately tested.
    while isinstance(model, WrapperModel):
        model = model.wrapped
    if type(model).__name__ == "OpenAIChatModel" and type(model).__module__ == "pydantic_ai.models.openai":
        return "openai-chat"
    if type(model).__name__ == "AnthropicModel" and type(model).__module__ == "pydantic_ai.models.anthropic":
        return "anthropic-messages"
    return None


def _message_view(messages):
    if type(messages) is not list or any(type(message) not in (ModelRequest, ModelResponse) for message in messages):
        return None
    try:
        # Serialization supplies lineage digests only. Provider calls receive
        # the original native objects, except explicitly replaced text leaves.
        serialized = ModelMessagesTypeAdapter.dump_python(messages, mode="json", warnings="error")
        context = manifest(serialized)
    except (TypeError, ValueError, PydanticSerializationError, RecursionError):
        return None
    if context is None:
        return None
    calls, candidates, paths = {}, [], {}
    for mi, message in enumerate(messages):
        for pi, part in enumerate(message.parts):
            if type(part) is ToolCallPart:
                calls[part.tool_call_id] = part.tool_name
            elif (type(part) is ToolReturnPart and part.outcome == "success" and part.tool_kind is None
                  and type(part.content) is str and part.tool_name != "caveman_retrieve"
                  and calls.get(part.tool_call_id) == part.tool_name):
                key = f"message-{mi}.part-{pi}"
                candidates.append(Candidate(key, part.content, part.tool_call_id))
                paths[key] = (mi, pi)
    return context, candidates, paths


def _apply(messages, outcome, paths):
    if not outcome.replacements or any(r["segment_id"] not in paths for r in outcome.replacements):
        return messages
    view = messages.copy()
    for replacement in outcome.replacements:
        mi, pi = paths[replacement["segment_id"]]
        parts = list(view[mi].parts)
        parts[pi] = replace(parts[pi], content=replacement["text"])
        view[mi] = replace(view[mi], parts=parts)
    return view


def _usage(response):
    if response.state not in ("complete", "suspended"):
        return None
    # Native RequestUsage exposes default class-level zeros even when the SDK
    # supplied no usage. Only explicitly populated counters are observations.
    values = vars(response.usage)
    def count(name):
        value = values.get(name)
        if name == "reasoning_tokens" and value is None:
            value = values.get("details", {}).get("reasoning_tokens")
        return value if type(value) is int and 0 <= value <= 2**53 - 1 else None
    result = {name: count(name) for name in
              ("input_tokens", "output_tokens", "cache_read_tokens", "cache_write_tokens", "reasoning_tokens")}
    if not any(value is not None for value in result.values()):
        return None
    return {"provenance": "client_observed_sdk", "complete": result["input_tokens"] is not None
            and result["output_tokens"] is not None, **result}


class CavemanCapability(AbstractCapability[Any]):
    """Add scoped recovery and wrap each selected native model invocation.

    Other capabilities and the native Agent own authorization, history, tools,
    retries, dependencies, and execution. The caller owns the shared runtime.
    """
    def __init__(self, *, runtime, scope):
        self.runtime, self.scope_source = _runtime(runtime), scope
        self.version_supported = _check_version(runtime)
        if not self.version_supported:
            self.toolset = None
            return

        async def recover(ctx: RunContext, handle: str, offset: int = 0, limit: int = 262144, query: str = ""):
            return await self.runtime.retrieve(_scope(self.scope_source, ctx), handle=handle,
                                               offset=offset, limit=limit, query=query)

        self.recovery_tool = Tool.from_schema(recover, name="caveman_retrieve", description=RECOVERY_DESCRIPTION,
            json_schema=copy.deepcopy(RECOVERY_SCHEMA), takes_ctx=True)
        self.recovery_callable = recover
        self.toolset = FunctionToolset([self.recovery_tool], id="caveman-recovery")

    async def for_run(self, ctx: RunContext):
        if not self.version_supported:
            return self
        # Each native run receives its own resolved scope and actual toolset.
        return CavemanCapability(runtime=self.runtime, scope=_scope(self.scope_source, ctx))

    def get_toolset(self):
        return self.toolset

    def registered(self, ctx, parameters, settings):
        if (not self.version_supported or ctx is None or ctx.tool_manager is None or not ctx.tool_manager.tools
                or parameters.output_mode != "text" or parameters.output_object is not None
                or parameters.output_tools or not parameters.allow_text_output
                or (settings or {}).get("tool_choice") not in (None, "auto")):
            return False
        extra = (settings or {}).get("extra_body")
        if isinstance(extra, dict) and any(key in extra for key in
                ("messages", "tools", "tool_choice", "response_format", "output_format", "output_config")):
            return False
        actual = ctx.tool_manager.tools.get("caveman_retrieve")
        offered = [tool for tool in parameters.function_tools if tool.name == "caveman_retrieve"]
        # CombinedToolset wraps its prepared tool but retains the public source
        # toolset and validator. Check the source's actual native function;
        # matching a model-visible schema alone never authorizes lossiness.
        if not (actual is not None and actual.toolset is self.toolset
                and self.toolset.tools.get("caveman_retrieve") is self.recovery_tool
                and self.recovery_tool.function_schema.function is self.recovery_callable
                and actual.args_validator is self.recovery_tool.function_schema.validator
                and actual.args_validator_func is self.recovery_tool.args_validator
                and len(offered) == 1):
            return False
        definition = offered[0]
        return (definition == actual.tool_def and definition.kind == "function" and not definition.defer_loading
                and definition.parameters_json_schema == RECOVERY_SCHEMA and definition.description == RECOVERY_DESCRIPTION
                and parameters.visibility_of(definition.name) == "visible")

    async def wrap_model_request(self, ctx: RunContext, *, request_context: ModelRequestContext, handler):
        model = CavemanModel(request_context.model, runtime=self.runtime, scope=self.scope_source,
                             registration=self, run_context=ctx)
        return await handler(replace(request_context, model=model))


class CavemanModel(WrapperModel):
    """Native Model delegate; direct model-only use has no recovery executor."""
    def __init__(self, wrapped: Model, *, runtime, scope, registration=None, run_context=None):
        if not isinstance(wrapped, Model):
            raise TypeError("Expected an existing native Pydantic AI Model")
        super().__init__(wrapped)
        self.runtime, self.scope_source = _runtime(runtime), scope
        self.version_supported = _check_version(runtime)
        self.registration, self.run_context = registration, run_context

    async def _prepare(self, messages, settings, parameters):
        if owner.get() is not None:
            return messages, None
        protocol = _protocol(self.wrapped)
        def passive(reason):
            return messages, Attempt(self.runtime, None, str(uuid.uuid4()), str(uuid.uuid4()),
                                     passive=True, reason=reason, adapter=ADAPTER.id)
        if self.runtime.mode == "off":
            return passive("disabled")
        if not self.version_supported or protocol is None:
            return passive("unsupported_version" if not self.version_supported else "unsupported_provider")
        selected = _message_view(messages)
        if selected is None:
            return passive("opaque_payload")
        context, candidates, paths = selected
        scope = _scope(self.scope_source, self.run_context)
        binding, overhead = None, None
        resolved_settings = {**(self.wrapped.settings or {}), **(settings or {})}
        if self.registration and self.registration.registered(self.run_context, parameters, resolved_settings):
            binding = self.runtime.recovery(scope)
            overhead = json.dumps({"name": binding.name, "description": binding.description,
                                   "parameters": binding.input_schema}, ensure_ascii=False, separators=(",", ":"))
        attempt = Attempt(self.runtime, scope, str(uuid.uuid4()), str(uuid.uuid4()), adapter=ADAPTER.id)
        try:
            outcome = await self.runtime.optimize(scope=scope, adapter=ADAPTER, candidates=candidates, manifest=context,
                binding=binding, recovery_overhead_text=overhead,
                model={"provider": self.wrapped.system, "id": self.wrapped.model_name, "protocol": protocol},
                logical_call_id=attempt.logical_call_id, attempt_id=attempt.attempt_id)
            try:
                current = self.runtime.mode != "off" and (binding is None or
                    (_scope(self.scope_source, self.run_context) == scope and self.runtime.owns_binding(binding, scope)
                     and self.registration.registered(self.run_context, parameters, {**(self.wrapped.settings or {}), **(settings or {})})))
            except Exception:
                current = False
            if not current:
                attempt.reason = "recovery_unavailable"
                return messages, attempt
            view = _apply(messages, outcome, paths)
            if not outcome.replacements or view is not messages:
                attempt.optimization = outcome
            else:
                attempt.reason = "invalid_plan"
            return view, attempt
        except asyncio.CancelledError:
            attempt.observe("cancelled")
            raise

    async def request(self, messages: list[ModelMessage], model_settings, model_request_parameters: ModelRequestParameters):
        view, attempt = await self._prepare(messages, model_settings, model_request_parameters)
        token = owner.set(attempt) if attempt else None
        try:
            if attempt:
                attempt.observe("dispatch_intent")
            response = await self.wrapped.request(view, model_settings, model_request_parameters)
            if attempt:
                attempt.observe("completed", None if attempt.passive else _usage(response))
            return response
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

    @asynccontextmanager
    async def request_stream(self, messages, model_settings, model_request_parameters, run_context=None):
        view, attempt = await self._prepare(messages, model_settings, model_request_parameters)
        token = owner.set(attempt) if attempt else None
        try:
            if attempt:
                attempt.observe("dispatch_intent")
            async with self.wrapped.request_stream(view, model_settings, model_request_parameters, run_context) as stream:
                # run_stream_sync enters and resumes native context managers
                # from different task contexts. Ownership covers dispatch;
                # never keep a ContextVar token alive across a consumer yield.
                if token is not None:
                    owner.reset(token)
                    token = None
                # Yield the actual native StreamedResponse, including cancel,
                # accumulation, provider state, usage, and first-chunk timing.
                yield stream
            if attempt:
                if attempt.passive:
                    attempt.observe("completed")
                    return
                response = stream.get()
                event = "completed" if response.state in ("complete", "suspended") else "cancelled"
                attempt.observe(event, _usage(response))
        except (asyncio.CancelledError, GeneratorExit):
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


def with_caveman_model(model: Model, *, runtime, scope: Scope) -> CavemanModel:
    """Wrap an existing model with the recovery-free default."""
    return CavemanModel(model, runtime=runtime, scope=scope)
