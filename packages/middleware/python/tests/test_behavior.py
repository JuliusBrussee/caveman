"""D9: the same behavioral contract for every adapter, driven through the real framework.

Each driver makes one native call whose history holds a successful tool result
and returns the text the native provider/model actually received. A driver
skips when its framework is absent (CI installs each family in its own lane).
The five contracts:

- projection applied, caller input untouched (free-form scope IDs work);
- runtime outage passes the original through;
- an adapter exception becomes an ``adapter_error`` pass-through;
- an invalid scope is recovery-free (``invalid_scope``), never an error;
- a framework version outside the tested range warns once and skips,
  unless ``accept_framework_version=True``.
"""
import asyncio
import copy
import json

import pytest

from frameworks import require_adapter

ORIGINAL = "[INFO] café 🌍\r\n" * 300
MARKER = "[caveman: shortened;"
DRIVERS = {}


def driver(family, target):
    """Register ``drive(runtime, scope, **options) -> received text``; ``target`` is the
    ``module:attribute`` a test breaks to prove the fail-open guard."""
    def register(function):
        DRIVERS[family] = (function, target)
        return function
    return register


def untouched(value):
    snapshot = copy.deepcopy(value)
    return lambda: value == snapshot


# ---------------------------------------------------------------- certified


@driver("langchain", "caveman_middleware.langchain:_message_view")
def drive_langchain(runtime, scope, **options):
    from langchain.agents.middleware.types import ModelRequest, ModelResponse
    from langchain_core.language_models.fake_chat_models import FakeListChatModel
    from langchain_core.messages import AIMessage, HumanMessage, ToolMessage
    from caveman_middleware.langchain import with_caveman_agent

    history = [HumanMessage("summarize"), AIMessage("", tool_calls=[{"id": "call-1", "name": "read_log", "args": {}}]),
               ToolMessage(ORIGINAL, tool_call_id="call-1", name="read_log")]
    same = untouched(history)
    agent = with_caveman_agent({"tools": []}, runtime=runtime, scope=scope, **options)
    request = ModelRequest(model=FakeListChatModel(responses=["unused"]), messages=history, tools=agent["tools"])
    seen = []
    agent["middleware"][0].wrap_model_call(request, lambda projected: seen.append(projected.messages[-1].content) or ModelResponse(result=[AIMessage("done")]))
    assert same()
    return seen[0]


def _openai_http():
    import importlib
    from openai import DefaultHttpxClient
    from caveman_middleware._httpx2 import sdk_flavour
    return importlib.import_module(sdk_flavour(DefaultHttpxClient))


@driver("openai", "caveman_middleware._native:leaves")
def drive_openai(runtime, scope, **options):
    from openai import OpenAI
    from caveman_middleware.openai import with_caveman_openai_tools

    http, received = _openai_http(), []

    def provider(request):
        received.append(json.loads(request.content)["messages"][-1]["content"])
        return http.Response(200, json={"id": "c", "object": "chat.completion", "created": 0, "model": "m", "choices": [
            {"index": 0, "finish_reason": "stop", "message": {"role": "assistant", "content": "done"}}]})

    messages = [{"role": "user", "content": "summarize"},
                {"role": "assistant", "tool_calls": [{"type": "function", "id": "call-1", "function": {"name": "read_log", "arguments": "{}"}}]},
                {"role": "tool", "tool_call_id": "call-1", "content": ORIGINAL}]
    same = untouched(messages)
    tools = [{"type": "function", "function": {"name": "read_log", "description": "Read", "parameters": {"type": "object", "properties": {}}}}]
    with OpenAI(api_key="test", http_client=http.Client(transport=http.MockTransport(provider))) as client:
        loop = with_caveman_openai_tools(client, runtime=runtime, scope=scope, protocol="openai-chat", tools=tools,
                                         functions={"read_log": lambda _: ORIGINAL}, **options)
        loop.client.chat.completions.create(model="m", tools=loop.tools, messages=messages)
    assert same()
    return received[0]


@driver("anthropic", "caveman_middleware._native:leaves")
def drive_anthropic(runtime, scope, **options):
    from anthropic import Anthropic, DefaultHttpxClient
    from anthropic.lib.tools import beta_tool
    from caveman_middleware._httpx2 import sdk_flavour
    from caveman_middleware.anthropic import with_caveman_anthropic
    import importlib

    http, received = importlib.import_module(sdk_flavour(DefaultHttpxClient)), []

    def provider(request):
        received.append(json.loads(request.content)["messages"][-1]["content"][0]["content"])
        return http.Response(200, json={"id": "msg", "type": "message", "role": "assistant", "model": "m", "stop_reason": "end_turn",
                                        "stop_sequence": None, "content": [{"type": "text", "text": "done"}],
                                        "usage": {"input_tokens": 1, "output_tokens": 1}})

    @beta_tool
    def read_log() -> str:
        """Read the log."""
        return ORIGINAL

    messages = [{"role": "user", "content": "summarize"},
                {"role": "assistant", "content": [{"type": "tool_use", "id": "toolu_1", "name": "read_log", "input": {}}]},
                {"role": "user", "content": [{"type": "tool_result", "tool_use_id": "toolu_1", "content": ORIGINAL}]}]
    same = untouched(messages)
    with Anthropic(api_key="test", http_client=http.Client(transport=http.MockTransport(provider))) as client:
        wrapped = with_caveman_anthropic(client, runtime=runtime, scope=scope, **options)
        runner = wrapped.beta.messages.tool_runner(model="m", max_tokens=16, messages=messages, tools=[read_log])
        runner.until_done()
    assert same()
    return received[0]


@driver("litellm", "caveman_middleware._native:leaves")
def drive_litellm(runtime, scope, **options):
    from caveman_cloud.middleware import ensure_sync
    from caveman_middleware.litellm import CavemanLiteLLM

    received = []
    server, base = _openai_server(received)
    binding = []

    def operator_recovery(resolved):
        bound = ensure_sync(runtime).recovery(resolved)
        binding.append(bound)
        return (bound, json.dumps({"name": bound.name})) if bound else None

    messages = [{"role": "user", "content": "summarize"},
                {"role": "assistant", "tool_calls": [{"type": "function", "id": "call-1", "function": {"name": "read_log", "arguments": "{}"}}]},
                {"role": "tool", "tool_call_id": "call-1", "content": ORIGINAL}]
    same = untouched(messages)
    from caveman_cloud.middleware import RECOVERY_DESCRIPTION, RECOVERY_SCHEMA
    tools = [{"type": "function", "function": {"name": "read_log", "description": "Read", "parameters": {"type": "object", "properties": {}}}},
             {"type": "function", "function": {"name": "caveman_retrieve", "description": RECOVERY_DESCRIPTION, "parameters": RECOVERY_SCHEMA}}]
    try:
        with CavemanLiteLLM(runtime=runtime, operator_recovery=operator_recovery, **options) as caveman:
            caveman.completion(scope=scope, model="openai/m", api_base=base, api_key="test", messages=messages, tools=tools)
    finally:
        server.shutdown()
    assert same()
    return received[0]["messages"][-1]["content"]


def _openai_server(received):
    import threading
    from http.server import BaseHTTPRequestHandler, HTTPServer

    class Provider(BaseHTTPRequestHandler):
        def do_POST(self):
            received.append(json.loads(self.rfile.read(int(self.headers["Content-Length"]))))
            payload = json.dumps({"id": "c", "object": "chat.completion", "created": 0, "model": "m", "choices": [
                {"index": 0, "finish_reason": "stop", "message": {"role": "assistant", "content": "done"}}],
                "usage": {"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2}}).encode()
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(payload)))
            self.end_headers()
            self.wfile.write(payload)

        def log_message(self, *_):
            pass

    server = HTTPServer(("127.0.0.1", 0), Provider)
    threading.Thread(target=server.serve_forever, daemon=True).start()
    return server, f"http://127.0.0.1:{server.server_port}/v1"


# ---------------------------------------------------------------- experimental


@driver("asgi", "caveman_middleware._native:leaves")
def drive_asgi(runtime, scope, **options):
    from caveman_cloud.middleware import RECOVERY_DESCRIPTION, RECOVERY_SCHEMA, ensure_async
    from caveman_middleware._guard import recovery
    from caveman_middleware.asgi import ASGIContext, CavemanASGIMiddleware

    binding = recovery(ensure_async(runtime), scope)
    body = {"model": "m", "messages": [
        {"role": "user", "content": "summarize"},
        {"role": "assistant", "tool_calls": [{"type": "function", "id": "c1", "function": {"name": "read", "arguments": "{}"}}]},
        {"role": "tool", "tool_call_id": "c1", "content": ORIGINAL}],
        "tools": [{"type": "function", "function": {"name": "read", "description": "Read", "parameters": {"type": "object"}}},
                  {"type": "function", "function": {"name": "caveman_retrieve", "description": RECOVERY_DESCRIPTION, "parameters": RECOVERY_SCHEMA}}]}
    seen = []

    async def application(scope_, receive, send):
        chunks = b""
        while True:
            message = await receive()
            chunks += message.get("body", b"")
            if not message.get("more_body"):
                break
        seen.append(json.loads(chunks)["messages"][-1]["content"])
        await send({"type": "http.response.start", "status": 200, "headers": [(b"content-type", b"application/json")]})
        await send({"type": "http.response.body", "body": b"{}"})

    async def drive():
        middleware = CavemanASGIMiddleware(application, runtime=runtime, routes={"/v1/chat/completions": "openai-chat"},
                                           resolve_context=lambda _: ASGIContext(scope, recovery=binding, recovery_overhead="{}"), **options)
        received = iter([{"type": "http.request", "body": json.dumps(body).encode(), "more_body": False}])

        async def receive():
            return next(received)

        async def send(_):
            pass

        await middleware({"type": "http", "method": "POST", "path": "/v1/chat/completions",
                          "headers": [(b"content-type", b"application/json")]}, receive, send)

    asyncio.run(drive())
    return seen[0]


@driver("google", "caveman_middleware.google:manifest")
def drive_google(runtime, scope, **options):
    import httpx
    from google import genai
    from google.genai import types
    from caveman_middleware.google import CavemanGoogleTransport, with_caveman_google

    received = []

    def provider(request):
        body = json.loads(request.content)
        received.append(body["contents"][2]["parts"][0]["functionResponse"]["response"]["output"])
        return httpx.Response(200, json={"candidates": [{"content": {"role": "model", "parts": [{"text": "done"}]}, "finishReason": "STOP"}]})

    def read_log() -> str:
        """Read the log."""
        return ORIGINAL

    contents = [types.Content(role="user", parts=[types.Part(text="summarize")]),
                types.Content(role="model", parts=[types.Part(function_call=types.FunctionCall(name="read_log", args={}))]),
                types.Content(role="user", parts=[types.Part(function_response=types.FunctionResponse(name="read_log", response={"output": ORIGINAL}))])]
    same = untouched(contents)
    transport = CavemanGoogleTransport(runtime=runtime, transport=httpx.MockTransport(provider), **options)
    client = genai.Client(api_key="test", http_options=types.HttpOptions(httpx_client=httpx.Client(transport=transport)))
    wrapped = with_caveman_google(client, runtime=runtime, scope=scope, **options)
    wrapped.models.generate_content(model="gemini-test", contents=contents, config=types.GenerateContentConfig(tools=[read_log]))
    assert same()
    return received[0]


@driver("strands", "caveman_middleware.strands:manifest")
def drive_strands(runtime, scope, **options):
    from strands import Agent
    from strands.models.model import Model
    from caveman_middleware.strands import with_caveman_agent

    received = []

    class Provider(Model):
        def update_config(self, **config):
            pass

        def get_config(self):
            return {"model_id": "fake"}

        async def stream(self, messages, tool_specs=None, system_prompt=None, **kwargs):
            received.append(messages[-1]["content"][0]["toolResult"]["content"][0]["text"])
            yield {"messageStart": {"role": "assistant"}}
            yield {"messageStop": {"stopReason": "end_turn"}}

        async def structured_output(self, output_model, prompt, system_prompt=None, **kwargs):
            yield {}

    messages = [{"role": "user", "content": [{"text": "summarize"}]},
                {"role": "assistant", "content": [{"toolUse": {"toolUseId": "t1", "name": "read_log", "input": {}}}]},
                {"role": "user", "content": [{"toolResult": {"toolUseId": "t1", "status": "success", "content": [{"text": ORIGINAL}]}}]}]
    same = untouched(messages)
    agent_options = with_caveman_agent({"model": Provider(), "tools": []}, runtime=runtime, scope=scope, **options)
    agent = Agent(**agent_options, callback_handler=None)  # the plugin attests registration in this agent's tool registry
    specs = [tool.tool_spec for tool in agent_options.get("tools", [])]

    async def drive():
        async for _ in agent_options["model"].stream(messages, specs, None):
            pass
        return agent

    asyncio.run(drive())
    assert same()
    return received[0]


@driver("agno", "caveman_middleware.agno:manifest")
def drive_agno(runtime, scope, **options):
    from agno.models.base import Model
    from agno.models.message import Message
    from agno.models.response import ModelResponse
    from caveman_middleware.agno import with_caveman_agent

    received = []

    class Provider(Model):
        def invoke(self, messages, assistant_message=None, response_format=None, tools=None, tool_choice=None, run_response=None,
                   compress_tool_results=False):
            received.append(messages[-1].content)
            return ModelResponse(role="assistant", content="done")

        async def ainvoke(self, *args, **kwargs):
            return self.invoke(*args, **kwargs)

        def invoke_stream(self, *args, **kwargs):
            yield self.invoke(*args, **kwargs)

        async def ainvoke_stream(self, *args, **kwargs):
            yield self.invoke(*args, **kwargs)

        def _parse_provider_response(self, response, **kwargs):
            return response

        def _parse_provider_response_delta(self, response):
            return response

    history = [Message(role="user", content="summarize"),
               Message(role="assistant", tool_calls=[{"id": "call-1", "type": "function", "function": {"name": "read_log", "arguments": "{}"}}]),
               Message(role="tool", tool_call_id="call-1", tool_name="read_log", content=ORIGINAL)]
    same = untouched(history)
    agent = with_caveman_agent({"model": Provider(id="fake"), "tools": []}, runtime=runtime, scope=scope, **options)
    model, tools = agent["model"], agent.get("tools") or []
    # Agno's Model.response() opens this per-run frame with the run's tools; drive one provider call inside it.
    token = model.connection.active.set(model._frame("response", (), {"messages": history, "tools": tools}))
    try:
        model.invoke(history, Message(role="assistant"), tools=[{"type": "function", "function": tool.to_dict()} for tool in tools])
    finally:
        model.connection.active.reset(token)
    assert same()
    return received[0]


@driver("crewai", "caveman_middleware.crewai:manifest")
def drive_crewai(runtime, scope, **options):
    from types import SimpleNamespace
    from crewai import BaseLLM
    from crewai.tools import BaseTool
    from crewai.utilities.agent_utils import convert_tools_to_openai_schema
    from caveman_middleware import crewai as adapter

    received = []

    class Provider(BaseLLM):
        def call(self, messages, tools=None, callbacks=None, available_functions=None, from_task=None, from_agent=None, response_model=None):
            received.append(messages[-1]["content"])
            return "done"

    class ReadLog(BaseTool):
        name: str = "read_log"
        description: str = "Read the log"

        def _run(self):
            return ORIGINAL

    class Executor:  # the fields CrewAI's PRE_MODEL_CALL context exposes for the running executor
        pass

    messages = [{"role": "user", "content": "summarize"},
                {"role": "assistant", "tool_calls": [{"id": "call-1", "type": "function", "function": {"name": "read_log", "arguments": "{}"}}]},
                {"role": "tool", "tool_call_id": "call-1", "name": "read_log", "content": ORIGINAL}]
    same = untouched(messages)
    agent = adapter.with_caveman_agent({"llm": Provider(model="fake"), "tools": [ReadLog()]}, runtime=runtime, scope=scope, **options)
    model, executor = agent["llm"], Executor()
    executor.llm, executor.messages, executor.task, executor.agent, executor.original_tools = model, messages, None, None, agent["tools"]
    adapter._before_model_call(SimpleNamespace(llm=model, executor=executor))
    model.call(messages, tools=convert_tools_to_openai_schema(agent["tools"])[0])
    assert same()
    return received[0]


@driver("pydantic_ai", "caveman_middleware.pydantic_ai:_message_view")
def drive_pydantic_ai(runtime, scope, **options):
    from openai import AsyncOpenAI
    from pydantic_ai import Agent
    from pydantic_ai.messages import ModelRequest, ModelResponse, ToolCallPart, ToolReturnPart, UserPromptPart
    from pydantic_ai.models.openai import OpenAIChatModel
    from pydantic_ai.providers.openai import OpenAIProvider
    from caveman_middleware.pydantic_ai import CavemanCapability

    http, received = _openai_http(), []

    def provider(request):
        body = json.loads(request.content)
        received.append(next(message["content"] for message in body["messages"] if message["role"] == "tool"))
        return http.Response(200, json={"id": "c", "object": "chat.completion", "created": 0, "model": "m", "choices": [
            {"index": 0, "finish_reason": "stop", "message": {"role": "assistant", "content": "done"}}]})

    client = AsyncOpenAI(api_key="test", http_client=http.AsyncClient(transport=http.MockTransport(provider)))
    agent = Agent(OpenAIChatModel("gpt-test", provider=OpenAIProvider(openai_client=client)),
                  capabilities=[CavemanCapability(runtime=runtime, scope=scope, **options)])

    @agent.tool_plain
    def read_log() -> str:
        """Read the log."""
        return ORIGINAL

    history = [ModelRequest(parts=[UserPromptPart("summarize")]),
               ModelResponse(parts=[ToolCallPart("read_log", {}, tool_call_id="call-1")]),
               ModelRequest(parts=[ToolReturnPart("read_log", ORIGINAL, tool_call_id="call-1")])]
    same = untouched(history)
    agent.run_sync(message_history=history)
    assert same()
    return received[0]


@driver("autogen", "caveman_middleware.autogen:manifest")
def drive_autogen(runtime, scope, **options):
    from autogen_core import FunctionCall
    from autogen_core.models import AssistantMessage, FunctionExecutionResult, FunctionExecutionResultMessage, UserMessage
    from autogen_ext.models.replay import ReplayChatCompletionClient
    from caveman_middleware.autogen import with_caveman_agent

    received = []
    provider = ReplayChatCompletionClient(["done"])
    native_create = provider.create

    async def create(messages, **kwargs):
        received.append(messages[-1].content[0].content)
        return await native_create(messages, **kwargs)

    provider.create = create

    def read_log() -> str:
        """Read the log."""
        return ORIGINAL

    history = [UserMessage(content="summarize", source="user"),
               AssistantMessage(content=[FunctionCall(id="call-1", name="read_log", arguments="{}")], source="assistant"),
               FunctionExecutionResultMessage(content=[FunctionExecutionResult(call_id="call-1", name="read_log", content=ORIGINAL)])]
    same = untouched(history)
    agent = with_caveman_agent({"model_client": provider, "tools": [read_log]}, runtime=runtime, scope=scope, **options)

    async def drive():
        workbench = agent.get("workbench")
        tools = await workbench.list_tools() if workbench is not None else []
        await agent["model_client"].create(history, tools=tools)

    asyncio.run(drive())
    assert same()
    return received[0]


@driver("llama_index", "caveman_middleware.llama_index:manifest")
def drive_llama_index(runtime, scope, **options):
    from llama_index.core.base.llms.types import ChatMessage, ChatResponse, MessageRole, TextBlock, ToolCallBlock
    from llama_index.core.llms.llm import ToolSelection
    from llama_index.llms.openai import OpenAI
    from caveman_middleware.llama_index import with_caveman_tools

    received = []

    class Provider(OpenAI):  # a subclass (like AzureOpenAI) keeps the OpenAI message shape
        def chat_with_tools(self, tools, user_msg=None, chat_history=None, **kwargs):
            received.append(chat_history[-1].blocks[0].text)
            return ChatResponse(message=ChatMessage(role=MessageRole.ASSISTANT, content="done"))

    def read_log() -> str:
        """Read the log."""
        return ORIGINAL

    bundle = with_caveman_tools(Provider(api_key="test", model="gpt-4o"), runtime=runtime, scope=scope, tools=[read_log], **options)
    bundle.execute(ToolSelection(tool_id="call-1", tool_name="read_log", tool_kwargs={}))  # the application loop's dispatch
    history = [ChatMessage(role=MessageRole.USER, content="summarize"),
               ChatMessage(role=MessageRole.ASSISTANT, blocks=[ToolCallBlock(tool_call_id="call-1", tool_name="read_log", tool_kwargs={})]),
               ChatMessage(role=MessageRole.TOOL, blocks=[TextBlock(text=ORIGINAL)], additional_kwargs={"tool_call_id": "call-1"})]
    same = untouched(history)
    bundle.model.chat_with_tools(bundle.tools, chat_history=history)
    assert same()
    return received[0]


@driver("mcp", "caveman_middleware.mcp:sha256")
def drive_mcp(runtime, scope, **options):
    from mcp.types import CallToolResult, TextContent, Tool
    from caveman_middleware.mcp import CavemanMCPHost, MCPToolBinding

    result = CallToolResult(content=[TextContent(type="text", text=ORIGINAL)])
    same = untouched(result.model_dump())
    tool = Tool(name="read_log", input_schema={"type": "object"})

    async def execute(arguments=None, **_):
        return result

    async def drive():
        host = CavemanMCPHost(runtime=runtime, scope=scope, server_id="server", protocol_version="2025-06-18", **options)
        registered = host.register([MCPToolBinding(tool, execute)])
        view = await host.project_result(result, tool=tool, call_id="c1", registered_tools=registered,
                                         context_manifest=[{"id": "message-0", "sha256": "0" * 64}])
        return view.content[0].text

    text = asyncio.run(drive())
    assert same()
    return text


# ---------------------------------------------------------------- the contracts


def _drive(family, runtime, scope, **options):
    require_adapter(family)
    return DRIVERS[family][0](runtime, scope, **options)


def _reasons(runtime):
    return [event.reason for event in runtime.reports]


@pytest.mark.parametrize("family", sorted(DRIVERS))
@pytest.mark.parametrize("session", ["user@example.com", "user 42 / chat #7"])
@pytest.mark.parametrize("view", ["sync", "async"])  # D5: either runtime type works on every path
def test_projection_applied_and_caller_input_untouched(family, session, view, protocol_runtime):
    from caveman_cloud.middleware import Scope, normalize_scope_token
    runtime = protocol_runtime if view == "sync" else protocol_runtime.as_async()
    received = _drive(family, runtime, Scope("tests", session))
    assert received.startswith(MARKER), f"{family} did not apply the plan"
    assert "applied" in [event.status for event in protocol_runtime.reports]
    hashed = normalize_scope_token(session)
    assert hashed.startswith("h-") and {request["scope"]["session_id"] for request in protocol_runtime.requests} == {hashed}
    assert all(receipt["scope"]["session_id"] == hashed for receipt in protocol_runtime.receipts)


@pytest.mark.parametrize("family", sorted(DRIVERS))
def test_strict_mode_raises_adapter_error_instead_of_passing_through(family, protocol_runtime, monkeypatch):
    import importlib
    from caveman_cloud.middleware import MiddlewareError, Scope
    require_adapter(family)
    module, attribute = DRIVERS[family][1].split(":")
    monkeypatch.setattr(importlib.import_module(module), attribute, lambda *a, **k: (_ for _ in ()).throw(RuntimeError("adapter bug")))
    with pytest.raises(MiddlewareError) as raised:
        _drive(family, protocol_runtime, Scope("tests", "strict"))
    assert raised.value.code == "adapter_error"


@pytest.mark.parametrize("family", sorted(DRIVERS))
def test_runtime_outage_passes_the_original_through(family, unreachable_runtime):
    from caveman_cloud.middleware import Scope
    assert _drive(family, unreachable_runtime, Scope("tests", "outage")) == ORIGINAL
    assert unreachable_runtime.reports and all(event.status == "skipped" for event in unreachable_runtime.reports)


@pytest.mark.parametrize("family", sorted(DRIVERS))
def test_adapter_exception_becomes_adapter_error_pass_through(family, lenient_runtime, monkeypatch, caplog):
    import importlib
    from caveman_cloud.middleware import Scope
    require_adapter(family)
    module, attribute = DRIVERS[family][1].split(":")

    def broken(*args, **kwargs):
        raise RuntimeError("adapter bug")

    monkeypatch.setattr(importlib.import_module(module), attribute, broken)
    assert _drive(family, lenient_runtime, Scope("tests", "broken")) == ORIGINAL
    assert "adapter_error" in _reasons(lenient_runtime)
    assert "reason=adapter_error" in caplog.text


@pytest.mark.parametrize("family", sorted(DRIVERS))
def test_invalid_scope_is_recovery_free(family, lenient_runtime, caplog):
    from caveman_cloud.middleware import Scope
    assert _drive(family, lenient_runtime, Scope("tests", "")) == ORIGINAL
    assert "invalid_scope" in _reasons(lenient_runtime)
    assert lenient_runtime.requests == []
    assert "reason=invalid_scope" in caplog.text


@pytest.mark.parametrize("family", sorted(set(DRIVERS) - {"asgi"}))  # pure ASGI has no framework version
def test_out_of_range_version_warns_once_and_skips(family, lenient_runtime, monkeypatch, caplog):
    from caveman_cloud.middleware import Scope
    from caveman_middleware import _versions
    require_adapter(family)
    monkeypatch.setattr(_versions, "installed_version", lambda name: "0.0.1")
    assert _drive(family, lenient_runtime, Scope("tests", "old")) == ORIGINAL
    assert "unsupported_version" in _reasons(lenient_runtime)
    assert "reason=unsupported_version" in caplog.text
    assert _drive(family, lenient_runtime, Scope("tests", "old"), accept_framework_version=True).startswith(MARKER)
