"""AutoGen 0.7.5 public model and workbench delegates.

Only the model-facing copy of successful text tool results is eligible. The
workbench keeps native execution, state, and tool streaming; the application
keeps ownership of the runtime and its credentials.
"""
from __future__ import annotations

import asyncio
import contextvars
import copy
import json
import uuid
import weakref
from collections import Counter
from contextlib import contextmanager
from dataclasses import asdict
from importlib.metadata import version
from typing import Any, Mapping

try:
    from autogen_core import CancellationToken, Component, ComponentModel, FunctionCall
    from autogen_core.models import (
        AssistantMessage, ChatCompletionClient, CreateResult,
        FunctionExecutionResult, FunctionExecutionResultMessage, SystemMessage, UserMessage,
    )
    from autogen_core.tools import BaseTool, FunctionTool, StaticStreamWorkbench, TextResultContent, ToolResult, Workbench
    from pydantic import BaseModel
except ModuleNotFoundError as error:
    raise ImportError("Install caveman-middleware[autogen] to use the AutoGen adapter") from error

from caveman_cloud.middleware import Adapter, Candidate, MiddlewareError, MiddlewareRuntime, Scope
from caveman_cloud.middleware.runtime import RECOVERY_DESCRIPTION, RECOVERY_SCHEMA
from ._native import Attempt, manifest, owner
from ._versions import matches_framework, supports_framework
from ._usage import usage

FRAMEWORK_VERSION = version("autogen-core")
ADAPTER = Adapter("autogen", "0.1.0", FRAMEWORK_VERSION, "autogen-messages-v1")
_component_runtimes = contextvars.ContextVar("caveman_autogen_component_runtimes", default={})
_loaded_components = contextvars.ContextVar("caveman_autogen_loaded_components", default=None)


@contextmanager
def component_runtimes(runtimes: Mapping[str, Any]):
    """Bind trusted runtimes while AutoGen loads a saved component.

    Configurations store a reference key, never runtime credentials. Loading a
    key requires this explicit application-owned binding; the key grants no
    authority by itself. Nested contexts restore the previous bindings.
    """
    token = _component_runtimes.set({**_component_runtimes.get(), **runtimes})
    loaded = []
    resources = _loaded_components.set(loaded)
    try:
        yield loaded
    finally:
        _loaded_components.reset(resources)
        _component_runtimes.reset(token)


def _runtime(key):
    try:
        return _component_runtimes.get()[key]
    except KeyError:
        raise ValueError(f"Bind runtime {key!r} with caveman_middleware.autogen.component_runtimes before loading") from None


def _supported(runtime):
    return supports_framework(runtime, ("autogen-core", "0.7", "0.8"), ("autogen-agentchat", "0.7", "0.8"), ("autogen-ext", "0.7", "0.8"))


def _version_supported():
    return matches_framework(("autogen-core", "0.7", "0.8"), ("autogen-agentchat", "0.7", "0.8"), ("autogen-ext", "0.7", "0.8"))


def _check(runtime, scope):
    if not isinstance(scope, Scope):
        raise TypeError("AutoGen requires a stable Caveman Scope for each agent or model context")
    return runtime.as_async() if isinstance(runtime, MiddlewareRuntime) else runtime


def _schema():
    parameters = copy.deepcopy(RECOVERY_SCHEMA)
    # AutoGen's OpenAI structured-output helper rejects every non-strict tool,
    # even when that tool is not called. A stable strict schema remains usable
    # on typed calls; those calls still use only recovery-free transformations.
    parameters["required"] = list(parameters["properties"])
    return {"name": "caveman_retrieve", "description": RECOVERY_DESCRIPTION,
            "parameters": parameters, "strict": True}


def _observed_usage(result):
    if result is None or result.cached:
        return None
    # Native AutoGen converts an absent provider usage block to two zeros.
    # Preserve its CreateResult, but do not book those defaults as measurement.
    if result.usage.prompt_tokens == result.usage.completion_tokens == 0:
        return None
    return usage(asdict(result.usage))


def _loaded(component):
    components = _loaded_components.get()
    if components is not None:
        components.append(component)
    return component


class _RecoverySchema(dict):
    """A native ToolSchema with an out-of-band link to its real executor."""
    def __init__(self, workbench):
        super().__init__(_schema())
        self.workbench = weakref.ref(workbench)


class _Cancellation:
    """One weak native callback per invocation, with no per-chunk callback list."""
    def __init__(self, token):
        self.token, self.pending = token, None
        if token is not None:
            reference, loop = weakref.ref(self), asyncio.get_running_loop()

            def cancel():
                state = reference()
                if state is not None and state.pending is not None:
                    loop.call_soon_threadsafe(state.pending.cancel)

            token.add_callback(cancel)

    def check(self):
        if self.token is not None and self.token.is_cancelled():
            raise asyncio.CancelledError

    async def wait(self, operation):
        if self.token is None:
            return await operation
        self.pending = asyncio.ensure_future(operation)
        try:
            if self.token.is_cancelled():
                self.pending.cancel()
            return await self.pending
        finally:
            self.pending = None


class CavemanModelConfig(BaseModel):
    model_client: ComponentModel
    scope: Scope
    runtime_key: str = "default"


class CavemanWorkbenchConfig(BaseModel):
    workbench: ComponentModel | list[ComponentModel]
    scope: Scope
    runtime_key: str = "default"


class CavemanChatCompletionClient(ChatCompletionClient, Component[CavemanModelConfig]):
    """Delegate every native call; model-only use is recovery-free."""
    component_provider_override = "caveman_middleware.autogen.CavemanChatCompletionClient"
    component_config_schema = CavemanModelConfig
    component_type = "model"

    def __init__(self, model_client: ChatCompletionClient, *, runtime, scope: Scope, runtime_key="default"):
        if not isinstance(model_client, ChatCompletionClient):
            raise TypeError("Expected an AutoGen ChatCompletionClient")
        self.model_client = model_client
        self.runtime, self.scope = _check(runtime, scope), scope
        self.runtime_key = runtime_key
        self.version_supported = _version_supported()
        if not self.version_supported and self.runtime.mode != "off":
            self.runtime.decline("unsupported_version")
        # Public configuration inspection occurs once, and only safe route
        # metadata is retained. Unknown contracts stay recovery-free.
        try:
            config = model_client.dump_component().config
            self.model_id = config.get("model")
            self.recovery_contract = not any(config.get(k) for k in ("response_format", "json_output", "output_format", "output_config", "extra_body"))
            self.recovery_contract &= config.get("tool_choice", "auto") == "auto"
        except (TypeError, ValueError, NotImplementedError, AttributeError):
            self.model_id, self.recovery_contract = None, False

    @property
    def capabilities(self):
        return self.model_client.capabilities

    @property
    def model_info(self):
        return self.model_client.model_info

    def actual_usage(self):
        return self.model_client.actual_usage()

    def total_usage(self):
        return self.model_client.total_usage()

    def count_tokens(self, messages, *, tools=()):
        return self.model_client.count_tokens(messages, tools=tools)

    def remaining_tokens(self, messages, *, tools=()):
        return self.model_client.remaining_tokens(messages, tools=tools)

    async def close(self):
        await self.model_client.close()

    def _to_config(self):
        return CavemanModelConfig(model_client=self.model_client.dump_component(), scope=self.scope, runtime_key=self.runtime_key)

    @classmethod
    def _from_config(cls, config):
        # Resolve the trusted runtime before constructing a provider client.
        runtime = _runtime(config.runtime_key)
        return _loaded(cls(ChatCompletionClient.load_component(config.model_client), runtime=runtime,
                   scope=config.scope, runtime_key=config.runtime_key))

    def _binding(self, tools, tool_choice, json_output, extra):
        if not self.recovery_contract or tool_choice != "auto" or json_output not in (None, False):
            return None, None
        if extra.get("tool_choice", "auto") != "auto" or any(extra.get(k) for k in ("response_format", "json_output", "output_format", "output_config", "tools", "messages", "extra_body")):
            return None, None
        matches = [tool for tool in tools if (tool.get("name") if isinstance(tool, dict) else getattr(tool, "name", None)) == "caveman_retrieve"]
        if len(matches) != 1 or type(matches[0]) is not _RecoverySchema:
            return None, None
        schema = matches[0]
        workbench = schema.workbench()
        if (workbench is None or schema is not workbench.recovery_schema or schema != _schema()
                or not workbench.recovery_enabled or workbench.runtime is not self.runtime
                or workbench.scope != self.scope or not self.runtime.owns_binding(workbench.binding, self.scope)):
            return None, None
        return workbench.binding, json.dumps(schema, ensure_ascii=False, separators=(",", ":"))

    async def _prepare(self, messages, tools, tool_choice, json_output, extra_create_args):
        if owner.get() is not None:
            return messages, None

        def passive(reason):
            return messages, Attempt(self.runtime, None, str(uuid.uuid4()), str(uuid.uuid4()), passive=True, reason=reason, adapter=ADAPTER.id)

        if self.runtime.mode == "off":
            return passive("disabled")
        if not self.version_supported:
            return passive("unsupported_version")
        if any(type(m) not in (SystemMessage, UserMessage, AssistantMessage, FunctionExecutionResultMessage) for m in messages):
            return passive("unsupported_shape")
        # These are hashes of native messages, not a reconstructed request. In
        # particular Image and unknown opaque values cause a conservative bypass.
        try:
            context = manifest([m.model_dump(mode="python") for m in messages])
        except (TypeError, ValueError, RecursionError):
            return passive("unsupported_shape")
        if context is None:
            return passive("unsupported_shape")
        calls = [(mi, call) for mi, m in enumerate(messages) if type(m) is AssistantMessage and type(m.content) is list
                 for call in m.content if type(call) is FunctionCall]
        counts = Counter(call.id for _, call in calls)
        names = {call.id: (call.name, mi) for mi, call in calls if counts[call.id] == 1}
        result_counts = Counter(result.call_id for m in messages if type(m) is FunctionExecutionResultMessage
                                for result in m.content if type(result) is FunctionExecutionResult)
        candidates, paths = [], {}
        for mi, message in enumerate(messages):
            if type(message) is not FunctionExecutionResultMessage:
                continue
            for ri, result in enumerate(message.content):
                if (type(result) is not FunctionExecutionResult or result.is_error or result.name == "caveman_retrieve"
                        or result_counts[result.call_id] != 1 or result.call_id not in names
                        or names[result.call_id][0] != result.name or names[result.call_id][1] >= mi or type(result.content) is not str):
                    continue
                key = f"message-{mi}.result-{ri}"
                candidates.append(Candidate(key, result.content, key))
                paths[key] = mi, ri
        binding, overhead = self._binding(tools, tool_choice, json_output, extra_create_args)
        model_id = extra_create_args.get("model", self.model_id)
        attempt = Attempt(self.runtime, self.scope, str(uuid.uuid4()), str(uuid.uuid4()), adapter=ADAPTER.id)
        result = await self.runtime.optimize(scope=self.scope, adapter=ADAPTER, manifest=context, candidates=candidates,
            binding=binding, recovery_overhead_text=overhead, logical_call_id=attempt.logical_call_id, attempt_id=attempt.attempt_id,
            model={"provider": type(self.model_client).__name__, "id": model_id, "protocol": "autogen-chat"} if type(model_id) is str else None)
        attempt.optimization = result if not result.replacements else None
        if not result.replacements:
            return messages, attempt
        if binding and self._binding(tools, tool_choice, json_output, extra_create_args)[0] is not binding:
            attempt.reason = "recovery_unavailable"
            return messages, attempt
        if not all(item["segment_id"] in paths for item in result.replacements):
            attempt.reason = "invalid_plan"
            return messages, attempt
        view = list(messages)
        for replacement in result.replacements:
            mi, ri = paths[replacement["segment_id"]]
            content = list(view[mi].content)
            content[ri] = content[ri].model_copy(update={"content": replacement["text"]})
            view[mi] = view[mi].model_copy(update={"content": content})
        attempt.optimization = result
        return view, attempt

    async def _prepare_cancellable(self, cancellation, messages, tools, tool_choice, json_output, extra_create_args):
        try:
            cancellation.check()
            result = await cancellation.wait(self._prepare(messages, tools, tool_choice, json_output, extra_create_args))
            cancellation.check()
            return result
        except asyncio.CancelledError:
            if owner.get() is None:
                self.runtime.report(reason="cancelled", adapter=ADAPTER.id)
            raise

    async def create(self, messages, *, tools=(), tool_choice="auto", json_output=None, extra_create_args={}, cancellation_token=None):
        cancellation = _Cancellation(cancellation_token)
        view, attempt = await self._prepare_cancellable(cancellation, messages, tools, tool_choice, json_output, extra_create_args)
        marker = owner.set(attempt) if attempt is not None else None
        try:
            if attempt:
                attempt.observe("dispatch_intent")
            result = await cancellation.wait(self.model_client.create(view, tools=tools, tool_choice=tool_choice,
                json_output=json_output, extra_create_args=extra_create_args, cancellation_token=cancellation_token))
            if attempt:
                attempt.observe("completed", _observed_usage(result))
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
            if marker is not None:
                owner.reset(marker)

    async def create_stream(self, messages, *, tools=(), tool_choice="auto", json_output=None, extra_create_args={}, cancellation_token=None, **kwargs):
        cancellation = _Cancellation(cancellation_token)
        view, attempt = await self._prepare_cancellable(cancellation, messages, tools, tool_choice, json_output, extra_create_args)
        iterator = self.model_client.create_stream(view, tools=tools, tool_choice=tool_choice, json_output=json_output,
            extra_create_args=extra_create_args, cancellation_token=cancellation_token, **kwargs).__aiter__()
        if attempt:
            attempt.observe("dispatch_intent")
        final = None
        try:
            while True:
                cancellation.check()
                marker = owner.set(attempt) if attempt is not None else None
                try:
                    event = await cancellation.wait(anext(iterator))
                except StopAsyncIteration:
                    if attempt:
                        attempt.observe("completed", _observed_usage(final))
                    return
                finally:
                    if marker is not None:
                        owner.reset(marker)
                if isinstance(event, CreateResult):
                    final = event
                yield event
        except (asyncio.CancelledError, GeneratorExit):
            if attempt:
                attempt.observe("cancelled")
            raise
        except BaseException:
            if attempt:
                attempt.observe("failed")
            raise
        finally:
            await iterator.aclose()


class CavemanWorkbench(StaticStreamWorkbench):
    """Add scoped recovery to a real workbench without changing its tool results.

    AutoGen 0.7.5 selects streaming tools by this public base class. Delegating
    call_tool_stream preserves native streaming workbenches and their events.
    """
    component_provider_override = "caveman_middleware.autogen.CavemanWorkbench"
    component_config_schema = CavemanWorkbenchConfig

    def __init__(self, workbench: Workbench | list[Workbench], *, runtime, scope: Scope, runtime_key="default"):
        delegates = workbench if type(workbench) is list else [workbench]
        if not all(isinstance(item, Workbench) for item in delegates):
            raise TypeError("Expected an AutoGen Workbench or list of workbenches")
        super().__init__([])
        self.workbench = workbench
        self.delegates = tuple(delegates)
        self.runtime, self.scope = _check(runtime, scope), scope
        self.runtime_key = runtime_key
        self.binding = self.runtime.recovery(scope)
        self.recovery_schema = _RecoverySchema(self)
        self.recovery_enabled = False
        self.stopped = False

    async def list_tools(self):
        tools = [tool for workbench in self.delegates for tool in await workbench.list_tools()]
        self.recovery_enabled = (not self.stopped and _supported(self.runtime) and self.runtime.mode == "compress"
                                 and not any(t.get("name") == "caveman_retrieve" for t in tools))
        return [*tools, self.recovery_schema] if self.recovery_enabled else tools

    async def call_tool(self, name, arguments=None, cancellation_token=None, call_id=None):
        # Refresh dynamic registries before deciding which implementation owns
        # the name. A collision always belongs to the original workbench.
        await self.list_tools()
        if name != "caveman_retrieve" or not self.recovery_enabled:
            for workbench in self.delegates:
                if any(tool["name"] == name for tool in await workbench.list_tools()):
                    return await workbench.call_tool(name, arguments, cancellation_token, call_id)
            if self.delegates:
                return await self.delegates[0].call_tool(name, arguments, cancellation_token, call_id)
            return ToolResult(name=name, result=[TextResultContent(content=f"Tool {name} not found.")], is_error=True)
        cancellation = _Cancellation(cancellation_token)
        cancellation.check()
        try:
            args = dict(arguments or {})
            if set(args) - set(RECOVERY_SCHEMA["properties"]) or "handle" not in args:
                raise MiddlewareError("invalid_recovery_arguments")
            result = await cancellation.wait(self.binding.execute(args))
            return ToolResult(name=name, result=[TextResultContent(content=json.dumps(result, ensure_ascii=False, separators=(",", ":")))])
        except MiddlewareError as error:
            return ToolResult(name=name, result=[TextResultContent(content=json.dumps({"error": {"code": error.code}}))], is_error=True)
        except (TypeError, ValueError):
            return ToolResult(name=name, result=[TextResultContent(content='{"error":{"code":"invalid_recovery_arguments"}}')], is_error=True)

    async def call_tool_stream(self, name, arguments=None, cancellation_token=None, call_id=None):
        await self.list_tools()
        workbench = None
        if name != "caveman_retrieve" or not self.recovery_enabled:
            for candidate in self.delegates:
                if any(tool["name"] == name for tool in await candidate.list_tools()):
                    workbench = candidate
                    break
        if not isinstance(workbench, StaticStreamWorkbench):
            yield await self.call_tool(name, arguments, cancellation_token, call_id)
            return
        iterator = workbench.call_tool_stream(name, arguments, cancellation_token, call_id).__aiter__()
        try:
            async for event in iterator:
                yield event
        finally:
            await iterator.aclose()

    async def start(self):
        for workbench in self.delegates:
            await workbench.start()
        self.stopped = False

    async def stop(self):
        self.stopped, self.recovery_enabled = True, False
        first_error = None
        for workbench in self.delegates:
            try:
                await workbench.stop()
            except BaseException as error:
                if first_error is None:
                    first_error = error
        if first_error is not None:
            raise first_error

    async def reset(self):
        for workbench in self.delegates:
            await workbench.reset()

    async def save_state(self):
        if type(self.workbench) is list:
            return {"workbenches": [await workbench.save_state() for workbench in self.delegates]}
        return await self.workbench.save_state()

    async def load_state(self, state):
        if type(self.workbench) is list:
            if len(state.get("workbenches", [])) != len(self.delegates):
                raise ValueError("Saved state must match the configured workbench count")
            for workbench, saved in zip(self.delegates, state["workbenches"]):
                await workbench.load_state(saved)
        else:
            await self.workbench.load_state(state)

    def _to_config(self):
        config = [workbench.dump_component() for workbench in self.delegates] if type(self.workbench) is list else self.workbench.dump_component()
        return CavemanWorkbenchConfig(workbench=config, scope=self.scope, runtime_key=self.runtime_key)

    @classmethod
    def _from_config(cls, config):
        runtime = _runtime(config.runtime_key)
        workbench = [Workbench.load_component(item) for item in config.workbench] if type(config.workbench) is list else Workbench.load_component(config.workbench)
        return _loaded(cls(workbench, runtime=runtime, scope=config.scope, runtime_key=config.runtime_key))


def with_caveman_model(model_client, *, runtime, scope, runtime_key="default"):
    """Wrap an existing client; recovery-free unless paired with the workbench."""
    return CavemanChatCompletionClient(model_client, runtime=runtime, scope=scope, runtime_key=runtime_key)


def with_caveman_agent(options: dict, *, runtime, scope, runtime_key="default") -> dict:
    """Return native AssistantAgent constructor options, leaving its loop intact.

    Accepts the native ``tools`` list or ``workbench`` (including a workbench
    list), retaining tool order and the original native executor for every call.
    """
    model = CavemanChatCompletionClient(options["model_client"], runtime=runtime, scope=scope, runtime_key=runtime_key)
    if runtime.mode == "off" or not model.version_supported:
        return {**options, "model_client": model}
    if options.get("tools") and options.get("workbench") is not None:
        raise ValueError("AutoGen tools and workbench are mutually exclusive")
    workbench = options.get("workbench")
    if workbench is None:
        tools = [t if isinstance(t, BaseTool) else FunctionTool(t, description=t.__doc__ or "") for t in options.get("tools", [])]
        workbench = StaticStreamWorkbench(tools)
    result = {**options, "model_client": model,
              "workbench": CavemanWorkbench(workbench, runtime=runtime, scope=scope, runtime_key=runtime_key)}
    result.pop("tools", None)
    return result
