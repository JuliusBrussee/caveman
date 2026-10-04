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

from ._versions import framework_import_failed

try:
    from mcp.types import CallToolResult, TextContent, Tool
except ImportError as error:
    framework_import_failed("mcp", error, "Install caveman-middleware[mcp] for the native MCP adapter")

from caveman_cloud.middleware import Adapter, Candidate, MiddlewareError, Scope, ensure_async, sha256
from ._guard import fail_open, recovery, recovery_args, recovery_failed, recovery_name_conflict
from ._native import owner
from ._versions import VERSION, family_gate, installed_version


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
    """Either SDK runtime type is accepted; recovery pages are awaited through its async view.

    An unusable scope never raises here: recovery stays unregistered and calls
    report ``invalid_scope``. A host tool already named ``caveman_retrieve``
    keeps its name and recovery stays off (``recovery_name_conflict``).
    """
    def __init__(self, *, runtime, scope: Scope, server_id: str, protocol_version: str, accept_framework_version=False):
        runtime = ensure_async(runtime)
        self._version_supported = family_gate(runtime, "mcp", "mcp", accept_framework_version)
        if not server_id or not protocol_version:
            raise ValueError("Provide the host's server identity and negotiated protocol version")
        self.runtime, self.scope, self.server_id = runtime, scope, server_id
        self.adapter = Adapter("mcp", VERSION, installed_version("mcp") or "unknown", "mcp-native-" + protocol_version + "-v1")
        binding = recovery(runtime, scope) if runtime.mode != "off" else None
        self._binding = binding
        self.recovery = None
        if binding is None:
            return
        tool = Tool(name=binding.name, description=binding.description, input_schema=dict(binding.input_schema))

        async def execute(arguments=None, **_native_options):
            # This is a host-local executor, not an outbound MCP tools/call.
            try:
                page = await binding.execute(recovery_args(arguments))
            except MiddlewareError as error:  # MCP's native tool error: is_error with the code
                return CallToolResult(content=[TextContent(type="text", text=json.dumps(recovery_failed(self.adapter.id, error)))], is_error=True)
            return CallToolResult(content=[TextContent(type="text", text=json.dumps(page, ensure_ascii=False, separators=(",", ":")))])
        self.recovery = MCPToolBinding(tool, execute)
        self._recovery_executor = execute
        self._recovery_definition = tool.model_dump_json(by_alias=True, exclude_none=True)

    def register(self, tools: Sequence[MCPToolBinding]) -> list[MCPToolBinding]:
        """Append only our executable tool. A name collision leaves tools intact."""
        if not self._version_supported or self.runtime.mode != "compress" or self.recovery is None:
            return list(tools)
        if any(item.tool.name == self.recovery.tool.name for item in tools):
            recovery_name_conflict(self.runtime, self.adapter.id)
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
        try:
            return await self._project(result, tool, call_id, context_manifest, registered_tools, sequence, skipped)
        except Exception as error:  # Decision 4: the host keeps the original result
            return skipped(fail_open(self.runtime, self.adapter.id, error))

    async def _project(self, result, tool, call_id, context_manifest, registered_tools, sequence, skipped):
        if not self._version_supported or self.runtime.mode == "off":
            return skipped("unsupported_version")
        if type(result) is not CallToolResult or type(tool) is not Tool:
            return skipped("unsupported_shape")
        if result.is_error or result.result_type != "complete" or "structured_content" in result.model_fields_set or tool.output_schema is not None or tool.name.startswith("caveman_"):
            return skipped("protected")
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
            if self.recovery is None:
                return False
            matching = [item for item in registered_tools if item.tool.name == self._binding.name]
            return (self.runtime.owns_binding(self._binding, self.scope) and len(matching) == 1 and matching[0] is self.recovery and
                    self.recovery.execute is self._recovery_executor and
                    self.recovery.tool.model_dump_json(by_alias=True, exclude_none=True) == self._recovery_definition)

        bound = registered()
        outcome = await self.runtime.optimize(scope=self.scope, adapter=self.adapter, candidates=candidates,
                    manifest=context_manifest, sequence=sequence, binding=self._binding if bound else None,
                    recovery_overhead_text=self._recovery_definition if bound else None,
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
