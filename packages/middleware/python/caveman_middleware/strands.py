"""Strands native Model delegate, including the structured-output hook gap."""
from __future__ import annotations

import asyncio
import json
import uuid
import weakref

try:
    from strands import tool
    from strands.models.model import Model
    from strands.plugins import Plugin
    from strands.types.tools import ToolContext
except ModuleNotFoundError as error:
    raise ImportError("Install caveman-middleware[strands] to use the Strands adapter") from error

from caveman_cloud.middleware import Adapter, Candidate, MiddlewareRuntime, Scope
from caveman_cloud.middleware.runtime import RECOVERY_DESCRIPTION, RECOVERY_SCHEMA
from ._native import Attempt, manifest, owner, plain, replace_path
from ._versions import matches_framework
from ._usage import usage

ADAPTER = Adapter("strands", "0.1.0", "1.55.0", "strands-content-v1")


def _scope(source, state):
    scope = source if isinstance(source, Scope) else source(state or {})
    if not isinstance(scope, Scope):
        raise TypeError("Strands scope resolver must return a Caveman Scope")
    return scope


class CavemanModel(Model):
    """Public native model delegate. A model wrapper alone is recovery-free."""
    def __init__(self, model, *, runtime, scope):
        if not isinstance(model, Model):
            raise TypeError("Expected a native Strands Model")
        self.model = model
        self.runtime = runtime.as_async() if isinstance(runtime, MiddlewareRuntime) else runtime
        self.scope, self.registration = scope, None
        self.version_supported = matches_framework(("strands-agents", "1.55", "2"))
        if not self.version_supported and self.runtime.mode != "off":
            self.runtime.decline("unsupported_version")

    @property
    def stateful(self):
        return self.model.stateful

    def update_config(self, **model_config):
        return self.model.update_config(**model_config)

    def get_config(self):
        return self.model.get_config()

    async def count_tokens(self, *args, **kwargs):
        return await self.model.count_tokens(*args, **kwargs)

    async def _prepare(self, messages, tool_specs, system_prompt, *, tool_choice=None, system_prompt_content=None, invocation_state=None, structured=False):
        if owner.get() is not None:
            return messages, None

        def passive(reason):
            return messages, Attempt(self.runtime, None, str(uuid.uuid4()), str(uuid.uuid4()), passive=True, reason=reason, adapter=ADAPTER.id)

        if self.runtime.mode == "off":
            return passive("disabled")
        if not self.version_supported:
            return passive("unsupported_version")
        if self.stateful:
            return passive("opaque_context")
        prefix = system_prompt_content if system_prompt_content is not None else [{"text": system_prompt}] if system_prompt else []
        context = manifest([{"system": prefix}, *messages])
        if context is None:
            return passive("unsupported_shape")
        scope = _scope(self.scope, invocation_state)
        binding, overhead = None, None
        if not structured and tool_choice in (None, {"auto": {}}) and self.registration and self.registration.bound(tool_specs):
            binding = self.runtime.recovery(scope)
            overhead = json.dumps(self.registration.recovery_tool.tool_spec, ensure_ascii=False, separators=(",", ":"))
        names = {p["toolUse"]["toolUseId"]: p["toolUse"]["name"] for m in messages if plain(m) for p in m.get("content", [])
            if plain(p) and plain(p.get("toolUse")) and type(p["toolUse"].get("toolUseId")) is str and type(p["toolUse"].get("name")) is str}
        candidates, paths = [], {}
        for mi, message in enumerate(messages):
            if not plain(message):
                continue
            for bi, block in enumerate(message.get("content", [])):
                result = block.get("toolResult") if plain(block) else None
                if not plain(result) or type(result.get("toolUseId")) is not str or result["toolUseId"] not in names or names[result["toolUseId"]] == "caveman_retrieve" or result.get("status") == "error":
                    continue
                for pi, part in enumerate(result.get("content", [])):
                    if not plain(part) or set(part) != {"text"} or type(part["text"]) is not str:
                        continue
                    key = f"message-{mi}.block-{bi}.part-{pi}"
                    candidates.append(Candidate(key, part["text"], key))
                    paths[key] = (mi, "content", bi, "toolResult", "content", pi, "text")
        config = self.get_config()
        model_id = config.get("model_id") if plain(config) else None
        attempt = Attempt(self.runtime, scope, str(uuid.uuid4()), str(uuid.uuid4()), adapter=ADAPTER.id)
        result = await self.runtime.optimize(scope=scope, adapter=ADAPTER, manifest=context, candidates=candidates, binding=binding,
            recovery_overhead_text=overhead, model={"provider": type(self.model).__name__, "id": model_id, "protocol": "strands"} if isinstance(model_id, str) else None,
            logical_call_id=attempt.logical_call_id, attempt_id=attempt.attempt_id)
        attempt.optimization = result if not result.replacements else None
        if binding and not self.registration.bound(tool_specs):
            attempt.reason = "recovery_unavailable"
            return messages, attempt
        if not all(item["segment_id"] in paths for item in result.replacements):
            attempt.reason = "invalid_plan"
            return messages, attempt
        view = messages
        for replacement in result.replacements:
            view = replace_path(view, paths[replacement["segment_id"]], replacement["text"])
        attempt.optimization = result
        return view, attempt

    async def _observe(self, iterator, attempt):
        if attempt is None:
            async for event in iterator:
                yield event
            return
        attempt.observe("dispatch_intent")
        last = None
        try:
            while True:
                token = owner.set(attempt)
                try:
                    event = await anext(iterator)
                except StopAsyncIteration:
                    attempt.observe("completed", usage(last))
                    return
                finally:
                    owner.reset(token)
                if not attempt.passive and plain(event) and plain(event.get("metadata")):
                    last = event["metadata"].get("usage") or last
                yield event
        except (asyncio.CancelledError, GeneratorExit):
            attempt.observe("cancelled")
            raise
        except BaseException:
            attempt.observe("failed")
            raise
        finally:
            if hasattr(iterator, "aclose"):
                await iterator.aclose()

    async def stream(self, messages, tool_specs=None, system_prompt=None, *, tool_choice=None, system_prompt_content=None, invocation_state=None, cancel_signal=None, **kwargs):
        if cancel_signal is not None and cancel_signal.is_set():
            if owner.get() is None:
                self.runtime.report(None, reason="cancelled", adapter=ADAPTER.id)
            raise asyncio.CancelledError
        view, attempt = await self._prepare(messages, tool_specs, system_prompt, tool_choice=tool_choice,
            system_prompt_content=system_prompt_content, invocation_state=invocation_state)
        if cancel_signal is not None and cancel_signal.is_set():
            if owner.get() is None:
                self.runtime.report(None, reason="cancelled", adapter=ADAPTER.id)
            raise asyncio.CancelledError
        native = self.model.stream(view, tool_specs, system_prompt, tool_choice=tool_choice,
            system_prompt_content=system_prompt_content, invocation_state=invocation_state, cancel_signal=cancel_signal, **kwargs).__aiter__()
        async for event in self._observe(native, attempt):
            yield event

    async def structured_output(self, output_model, prompt, system_prompt=None, **kwargs):
        view, attempt = await self._prepare(prompt, None, system_prompt, invocation_state=kwargs.get("invocation_state"), structured=True)
        native = self.model.structured_output(output_model, view, system_prompt, **kwargs).__aiter__()
        async for event in self._observe(native, attempt):
            yield event


class _Registration(Plugin):
    name = "caveman:middleware"

    def __init__(self, model):
        self.model, self.agent = model, None
        super().__init__()

        @tool(name="caveman_retrieve", description=RECOVERY_DESCRIPTION, inputSchema={"json": RECOVERY_SCHEMA}, context=True)
        async def recover(handle: str, tool_context: ToolContext, offset: int = 0, limit: int = 262144, query: str = ""):
            if tool_context.cancel_signal.is_set():
                raise asyncio.CancelledError
            scope = _scope(model.scope, tool_context.invocation_state)
            result = await model.runtime.retrieve(scope, handle=handle, offset=offset, limit=limit, query=query)
            return json.dumps(result, ensure_ascii=False, separators=(",", ":"))
        self.recovery_tool = recover

    def init_agent(self, agent):
        if self.agent is not None and self.agent() is not agent:
            raise ValueError("Create a separate Caveman Strands bundle for each agent")
        self.agent = weakref.ref(agent)

    def bound(self, specs):
        agent = self.agent() if self.agent else None
        return (agent is not None and agent.tool_registry.registry.get("caveman_retrieve") is self.recovery_tool
                and type(specs) is list and sum(s.get("name") == "caveman_retrieve" for s in specs if plain(s)) == 1
                and any(s == self.recovery_tool.tool_spec for s in specs))


def with_caveman_model(model, *, runtime, scope):
    return CavemanModel(model, runtime=runtime, scope=scope)


def with_caveman_agent(options: dict, *, runtime, scope) -> dict:
    """Return native Agent constructor options; Strands keeps its own loop."""
    model = CavemanModel(options["model"], runtime=runtime, scope=scope)
    if runtime.mode == "off" or not model.version_supported:
        return {**options, "model": model}
    registration = _Registration(model)
    model.registration = registration
    tools = list(options.get("tools", []))
    if runtime.mode == "compress" and not any(getattr(t, "tool_name", None) == "caveman_retrieve" for t in tools):
        tools.append(registration.recovery_tool)
    return {**options, "model": model, "tools": tools, "plugins": [*options.get("plugins", []), registration]}
