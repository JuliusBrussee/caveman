"""Native dictionary views; no provider-to-common-message round trip."""
from __future__ import annotations

import contextvars
import json
import math
import uuid
from dataclasses import dataclass
from typing import Any

from caveman_cloud.middleware import Adapter, Candidate, Scope, sha256

owner: contextvars.ContextVar[Attempt | None] = contextvars.ContextVar("caveman_middleware_owner", default=None)


def plain(value: Any) -> bool:
    return type(value) is dict


def manifest(items: list) -> list[dict] | None:
    budget = 2 << 20
    seen: set[int] = set()

    def visit(value, depth=0):
        nonlocal budget
        if depth > 64 or budget < 0:
            raise ValueError("bounded")
        if type(value) is str:
            budget -= len(value.encode("utf-8"))
        elif value is None or type(value) in (bool, int):
            budget -= 32
        elif type(value) is float and math.isfinite(value):
            budget -= 32
        elif type(value) in (dict, list):
            if id(value) in seen:
                raise ValueError("cyclic")
            seen.add(id(value))
            for key, entry in (value.items() if type(value) is dict else enumerate(value)):
                if type(value) is dict:
                    if type(key) is not str:
                        raise ValueError("opaque")
                    budget -= len(key.encode("utf-8"))
                visit(entry, depth + 1)
            seen.remove(id(value))
        else:
            raise ValueError("opaque")

    try:
        result = []
        for i, item in enumerate(items):
            visit(item)
            if budget < 0:
                return None
            result.append({"id": f"message-{i}", "sha256": sha256(json.dumps(item, ensure_ascii=False, separators=(",", ":"), allow_nan=False))})
        return result
    except (TypeError, ValueError, RecursionError, UnicodeError):
        return None


def leaves(body: dict, protocol: str) -> tuple[list, list[tuple[tuple, str]]] | None:
    found: list[tuple[tuple, str]] = []
    calls, results = {}, {}

    def only(value, keys):
        return plain(value) and set(value).issubset(keys)

    def call(identifier, name, position, valid):
        if type(identifier) is str and identifier:
            calls[identifier] = (name, position) if identifier not in calls and valid else None

    def result(identifier):
        if type(identifier) is str:
            results[identifier] = results.get(identifier, 0) + 1

    def matched(identifier, position, name=None):
        source = calls.get(identifier) if type(identifier) is str else None
        return bool(source and source[1] < position and source[0] != "caveman_retrieve"
                    and results.get(identifier) == 1 and (name is None or name == source[0]))

    def text_parts(content, path, cache_control=False):
        if type(content) is str:
            found.append((path, content))
        elif type(content) is list:
            keys = {"type", "text", "cache_control"} if cache_control else {"type", "text"}
            # A mixed/cited/unknown block contract protects the entire result.
            if all(only(part, keys) and part.get("type") == "text" and type(part.get("text")) is str for part in content):
                found.extend(((*path, i, "text"), part["text"]) for i, part in enumerate(content))

    if protocol == "openai-chat" and type(body.get("messages")) is list:
        messages = body["messages"]
        for mi, message in enumerate(messages):
            if not plain(message):
                continue
            if message.get("role") == "assistant" and type(message.get("tool_calls")) is list:
                for item in message["tool_calls"]:
                    if not plain(item):
                        continue
                    function = item.get("function")
                    valid = (only(item, {"id", "type", "function", "index"}) and item.get("type") == "function"
                             and ("index" not in item or (type(item["index"]) is int and 0 <= item["index"] <= 2**53 - 1))
                             and only(function, {"name", "arguments"}) and type(function.get("name")) is str
                             and bool(function["name"]) and type(function.get("arguments")) is str)
                    call(item.get("id"), function.get("name") if plain(function) else None, mi, valid)
            elif message.get("role") == "tool":
                result(message.get("tool_call_id"))
        for mi, message in enumerate(messages):
            if (not only(message, {"role", "tool_call_id", "content", "name", "is_error", "cache_breakpoint"})
                    or message.get("role") != "tool" or message.get("is_error") not in (None, False)
                    or not matched(message.get("tool_call_id"), mi, message.get("name"))):
                continue
            text_parts(message.get("content"), ("messages", mi, "content"))
        return messages, found
    if protocol == "openai-responses" and type(body.get("input")) is list:
        if body.get("previous_response_id") or body.get("conversation"):
            return None
        items = body["input"]
        for i, item in enumerate(items):
            if not plain(item):
                continue
            if item.get("type") == "function_call":
                valid = (only(item, {"type", "call_id", "name", "arguments", "id", "status", "parsed_arguments", "caller", "namespace"})
                         and item.get("caller") is None and item.get("namespace") is None
                         and (item.get("parsed_arguments") is None or plain(item["parsed_arguments"]))
                         and type(item.get("name")) is str and bool(item["name"])
                         and type(item.get("arguments")) is str and item.get("status") in (None, "completed"))
                call(item.get("call_id"), item.get("name"), i, valid)
            elif item.get("type") == "function_call_output":
                result(item.get("call_id"))
        for i, item in enumerate(items):
            if (only(item, {"type", "call_id", "output", "id", "status"}) and item.get("type") == "function_call_output"
                    and item.get("status") in (None, "completed") and matched(item.get("call_id"), i)
                    and type(item.get("output")) is str):
                found.append((("input", i, "output"), item["output"]))
        return items, found
    if protocol == "anthropic-messages" and type(body.get("messages")) is list:
        messages = body["messages"]
        for mi, message in enumerate(messages):
            if not plain(message) or type(message.get("content")) is not list:
                continue
            for part in message["content"]:
                if not plain(part):
                    continue
                if message.get("role") == "assistant" and part.get("type") == "tool_use":
                    valid = (only(part, {"type", "id", "name", "input", "cache_control"})
                             and type(part.get("name")) is str and bool(part["name"]) and plain(part.get("input")))
                    call(part.get("id"), part.get("name"), mi, valid)
                elif part.get("type") == "tool_result":
                    result(part.get("tool_use_id"))
        for mi, message in enumerate(messages):
            if not plain(message) or message.get("role") != "user" or type(message.get("content")) is not list:
                continue
            for pi, part in enumerate(message["content"]):
                if (not only(part, {"type", "tool_use_id", "content", "is_error", "cache_control"})
                        or part.get("type") != "tool_result" or part.get("is_error") not in (None, False)
                        or not matched(part.get("tool_use_id"), mi)):
                    continue
                text_parts(part.get("content"), ("messages", mi, "content", pi, "content"), True)
        return messages, found
    if protocol == "google-genai" and type(body.get("contents")) is list:
        for mi, message in enumerate(body["contents"]):
            if not plain(message) or type(message.get("parts")) is not list:
                continue
            for pi, part in enumerate(message["parts"]):
                response = part.get("functionResponse") if plain(part) else None
                if (plain(response) and response.get("name") != "caveman_retrieve" and plain(response.get("response"))
                        and "error" not in response["response"] and type(response["response"].get("output")) is str):
                    found.append((("contents", mi, "parts", pi, "functionResponse", "response", "output"), response["response"]["output"]))
        return body["contents"], found
    return None


def replace_path(value: Any, path: tuple, text: str) -> Any:
    if not path:
        return text
    clone = value.copy()
    clone[path[0]] = replace_path(value[path[0]], path[1:], text)
    return clone


def accepts_recovery(body, protocol, binding):
    if binding is None or type(body.get("tools")) is not list:
        return False
    if (body.get("response_format") is not None or body.get("output_format") is not None
            or (plain(body.get("output_config")) and body["output_config"].get("format") is not None)
            or (protocol == "openai-responses" and plain(body.get("text")) and body["text"].get("format") is not None)):
        return False
    choice = body.get("tool_choice")
    if choice is not None and choice != "auto" and not (plain(choice) and choice.get("type") == "auto"):
        return False
    tools = [t.get("function") if protocol == "openai-chat" and plain(t) else t for t in body["tools"]]
    if any(not plain(tool) or type(tool.get("name")) is not str for tool in tools):
        return False
    if len({tool["name"] for tool in tools}) != len(tools):
        return False
    if protocol == "anthropic-messages" and any(
            plain(message) and type(message.get("content")) is list and any(
                plain(part) and part.get("type") in ("tool_removal", "tool_addition") for part in message["content"])
            for message in body.get("messages", [])):
        return False
    matches = [t for t in tools if plain(t) and t.get("name") == binding.name]
    return len(matches) == 1 and matches[0].get("description") == binding.description and matches[0].get("input_schema" if protocol == "anthropic-messages" else "parameters") == binding.input_schema


@dataclass
class Attempt:
    runtime: Any
    scope: Scope
    logical_call_id: str
    attempt_id: str
    optimization: Any = None
    wire_sha256: str | None = None
    plan_id: str | None = None
    passive: bool = False
    reason: str = "no_candidate"
    adapter: str | None = None
    _reported_attempt_id: str | None = None

    def observe(self, event, usage=None):
        if event == "dispatch_intent" and self._reported_attempt_id != self.attempt_id:
            self._reported_attempt_id = self.attempt_id
            self.runtime.report(self.optimization, logical_call_id=self.logical_call_id, attempt_id=self.attempt_id,
                                reason=self.reason, adapter=self.adapter)
        if self.passive or self.runtime.mode == "off":
            return
        self.runtime.observe_background({"schema_version": 1, "scope": self.scope.__dict__, "logical_call_id": self.logical_call_id,
            "attempt_id": self.attempt_id, "event_kind": event,
            "plan_id": self.plan_id or (self.optimization.plan["replacement_set_id"] if self.optimization and self.optimization.plan else None),
            "usage": usage, "provider_request_sha256": self.wire_sha256})


class NativeSession:
    def __init__(self, runtime, scope, *, adapter_id, framework_version, protocol, binding=None, overhead=None, logical_call_id=None, is_registered=None, passive_reason=None):
        self.runtime, self.scope, self.protocol = runtime, scope, protocol
        self.binding, self.overhead, self.logical_call_id = binding, overhead, logical_call_id
        self.is_registered = is_registered
        self.passive_reason = passive_reason
        self.adapter = Adapter(adapter_id, "0.1.0", framework_version, protocol + "-native-v1")

    def registered(self):
        try:
            return self.is_registered is None or self.is_registered() is True
        except Exception:
            return False

    def options(self, body):
        if owner.get() is not None or self.runtime.mode == "off" or self.passive_reason or not plain(body):
            return None
        selected = leaves(body, self.protocol)
        if selected is None:
            return None
        context, selected_leaves = selected
        context_manifest = manifest(context)
        if context_manifest is None:
            return None
        attempt = Attempt(self.runtime, self.scope, self.logical_call_id or str(uuid.uuid4()), str(uuid.uuid4()), adapter=self.adapter.id)
        binding = self.binding if self.registered() and self.runtime.owns_binding(self.binding, self.scope) and accepts_recovery(body, self.protocol, self.binding) else None
        options = dict(scope=self.scope, adapter=self.adapter, candidates=[Candidate(id=f"leaf-{i}", source_id="/".join(map(str,path)), content=text) for i,(path,text) in enumerate(selected_leaves)],
            manifest=context_manifest, model={"provider": self.adapter.id.removesuffix("-sdk"), "id": body["model"], "protocol": self.protocol} if type(body.get("model")) is str else None,
            binding=binding, recovery_overhead_text=self.overhead, logical_call_id=attempt.logical_call_id, attempt_id=attempt.attempt_id)
        return attempt, selected_leaves, options

    def apply(self, body, state, outcome):
        attempt, selected, options = state
        attempt.optimization = outcome if not outcome.replacements else None
        if options["binding"] is not None and (not self.registered() or
                not self.runtime.owns_binding(options["binding"], self.scope) or
                not accepts_recovery(body, self.protocol, options["binding"])):
            attempt.reason = "recovery_unavailable"
            return body, attempt
        mapping = {f"leaf-{i}": path for i,(path,_) in enumerate(selected)}
        if not all(r["segment_id"] in mapping for r in outcome.replacements):
            attempt.reason = "invalid_plan"
            return body, attempt
        result = body
        for replacement in outcome.replacements:
            result = replace_path(result, mapping[replacement["segment_id"]], replacement["text"])
        attempt.optimization = outcome
        return result, attempt

    def passive(self, body, reason=None):
        if owner.get() is not None:
            return body, None
        return body, Attempt(self.runtime, self.scope, self.logical_call_id or str(uuid.uuid4()), str(uuid.uuid4()),
                             passive=True, reason=reason or self.passive_reason or "unsupported_shape", adapter=self.adapter.id)

    def prepare(self, body):
        state = self.options(body)
        return self.passive(body) if state is None else self.apply(body, state, self.runtime.optimize(**state[2]))

    async def prepare_async(self, body):
        state = self.options(body)
        return self.passive(body) if state is None else self.apply(body, state, await self.runtime.optimize(**state[2]))
