"""Native MCP result views and local host recovery registration.

The caller keeps its Client/ClientSession, transports and original results.
This adapter adds no MCP server or protocol implementation. All compression and
scoped recovery use the shared Engine-backed Caveman runtime.
"""
from __future__ import annotations

from collections.abc import Awaitable, Callable, Mapping, Sequence
from dataclasses import dataclass
from importlib.metadata import version
import json
import uuid
from typing import Any

try:
    from mcp.types import CallToolResult, TextContent, Tool
except ModuleNotFoundError as error:
    raise ImportError("Install caveman-middleware[mcp] for the native MCP adapter") from error

from caveman_cloud.middleware import Adapter, AsyncMiddlewareRuntime, Candidate, MiddlewareError, Scope, sha256
from ._native import owner
from ._versions import matches_framework


@dataclass(frozen=True)
class MCPToolBinding:
    """The native MCP definition and the callable registered in the host loop."""
    tool: Tool
    execute: Callable[..., Awaitable[CallToolResult]]


def bind_mcp_tool(client, tool: Tool) -> MCPToolBinding:
    """Use an existing native client; it keeps auth, IDs, validation and options."""
    async def execute(arguments=None, **native_options):
        return await client.call_tool(tool.name, arguments, **native_options)
    return MCPToolBinding(tool, execute)


class CavemanMCPHost:
    def __init__(self, *, runtime: AsyncMiddlewareRuntime, scope: Scope,
                 server_id: str, protocol_version: str):
        if not isinstance(runtime, AsyncMiddlewareRuntime):
            raise TypeError("Native MCP clients require AsyncMiddlewareRuntime")
        self._version_supported = matches_framework(("mcp", "2.2", "3"))
        if not self._version_supported and runtime.mode != "off":
            runtime.decline("unsupported_version")
        if not server_id or not protocol_version:
            raise ValueError("Provide the host's server identity and negotiated protocol version")
        self.runtime, self.scope, self.server_id = runtime, scope, server_id
        self.adapter = Adapter("mcp", "0.1.0", "2.2.0", "mcp-native-" + protocol_version + "-v1")
        binding = runtime.recovery(scope)
        self._binding = binding
        tool = Tool(name=binding.name, description=binding.description, input_schema=dict(binding.input_schema))

        async def execute(arguments=None, **_native_options):
            # This is a host-local executor, not an outbound MCP tools/call.
            page = await binding.execute(arguments or {})
            return CallToolResult(content=[TextContent(type="text", text=json.dumps(page, ensure_ascii=False, separators=(",", ":")))])
        self.recovery = MCPToolBinding(tool, execute)
        self._recovery_executor = execute
        self._recovery_definition = tool.model_dump_json(by_alias=True, exclude_none=True)

    def register(self, tools: Sequence[MCPToolBinding]) -> list[MCPToolBinding]:
        """Append only our executable tool. A name collision leaves tools intact."""
        if not self._version_supported or self.runtime.mode != "compress" or any(item.tool.name == self.recovery.tool.name for item in tools):
            return list(tools)
        return [*tools, self.recovery]

    async def project_result(self, result: CallToolResult, *, tool: Tool, call_id: str,
                             context_manifest: Sequence[Mapping[str, str]],
                             registered_tools: Sequence[MCPToolBinding] = (),
                             sequence: int | None = None) -> CallToolResult:
        """Build the model view immediately before dispatch. Persist result itself.

        context_manifest is the host's append-only original-context manifest.
        It must be reused across resumes; deliberate edits need a new cache epoch.
        This boundary sees MCP values, not the provider's final serialized prompt.
        """
        def skipped(reason):
            self.runtime.report(None, reason=reason, adapter=self.adapter.id)
            return result

        if owner.get() is not None:
            return result
        if not self._version_supported or self.runtime.mode == "off":
            return skipped("unsupported_version")
        if type(result) is not CallToolResult or type(tool) is not Tool:
            return skipped("unsupported_shape")
        if result.is_error or result.result_type != "complete" or "structured_content" in result.model_fields_set or tool.output_schema is not None or tool.name.startswith("caveman_"):
            return skipped("protected_result")
        candidates, indices = [], {}
        for i, part in enumerate(result.content):
            if type(part) is not TextContent or part.annotations and part.annotations.audience is not None and "assistant" not in part.annotations.audience:
                continue
            key = "mcp-" + sha256(json.dumps([self.server_id, tool.name, call_id, i], separators=(",", ":")))
            indices[key] = i
            candidates.append(Candidate(id=key, source_id=key, content=part.text))
        if not candidates:
            return skipped("no_candidate")

        def registered():
            matching = [item for item in registered_tools if item.tool.name == self._binding.name]
            return (self.runtime.owns_binding(self._binding, self.scope) and len(matching) == 1 and matching[0] is self.recovery and
                    self.recovery.execute is self._recovery_executor and
                    self.recovery.tool.model_dump_json(by_alias=True, exclude_none=True) == self._recovery_definition)

        bound = registered()
        outcome = await self.runtime.optimize(scope=self.scope, adapter=self.adapter, candidates=candidates,
                    manifest=context_manifest, sequence=sequence, binding=self._binding if bound else None,
                    recovery_overhead_text=self.recovery.tool.model_dump_json(by_alias=True, exclude_none=True),
                    logical_call_id=str(uuid.uuid4()), attempt_id=str(uuid.uuid4()))
        if not outcome.replacements:
            self.runtime.report(outcome)
            return result
        if bound and not registered():
            return skipped("recovery_unavailable")
        if not all(item["segment_id"] in indices for item in outcome.replacements):
            return skipped("invalid_plan")
        content = list(result.content)
        for replacement in outcome.replacements:
            index = indices[replacement["segment_id"]]
            content[index] = content[index].model_copy(update={"text": replacement["text"]})
        view = result.model_copy(update={"content": content})
        self.runtime.report(outcome)
        return view
