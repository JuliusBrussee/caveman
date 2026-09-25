"""A refused ``caveman_retrieve`` never ends the host's run.

The model can call recovery with a well-formed handle the runtime does not know
(404 ``not_found``), one past retention (410 ``expired``), or during an outage
(503 ``runtime_unavailable``). Each adapter's native executor must hand its
framework the native tool-error result carrying ``{"error": code}`` and warn
once; only cancellation propagates. Each driver runs the framework's own
recovery execution path and returns ``(text the model receives, error flag)``.
The flag is None where the executor returns the payload as an ordinary result
(OpenAI and LlamaIndex application loops, Google AFC, CrewAI, and Agno, which
flags a failure only for a raised exception it logs with a traceback).
LiteLLM and ASGI own no executor: their operator supplies it.
"""
import asyncio
import json

import pytest

from conftest import peer_runtime
from frameworks import require_adapter

HANDLE = "cmw_" + "0" * 48
ANSWERS = [(404, "not_found"), (410, "expired"), (503, "runtime_unavailable")]
RECOVER = {}


def recover(family):
    def register(function):
        RECOVER[family] = function
        return function
    return register


def _text(value):
    return value if isinstance(value, str) else "".join(block["text"] for block in value)


@recover("langchain")
def recover_langchain(runtime, scope, asynchronous):
    from langchain.agents import create_agent
    from langchain_core.language_models.fake_chat_models import FakeMessagesListChatModel
    from langchain_core.messages import AIMessage, HumanMessage
    from langgraph.checkpoint.memory import InMemorySaver
    from caveman_middleware.langchain import with_caveman_agent

    class Model(FakeMessagesListChatModel):
        def bind_tools(self, tools, **kwargs):
            return self

    model = Model(responses=[AIMessage("", tool_calls=[{"id": "r1", "name": "caveman_retrieve", "args": {"handle": HANDLE}}]),
                             AIMessage("done"), AIMessage("resumed")])
    options = with_caveman_agent({"tools": []}, runtime=runtime, scope=scope)
    agent = create_agent(model, tools=options["tools"], middleware=options["middleware"], checkpointer=InMemorySaver())
    config = {"configurable": {"thread_id": "thread-1"}}
    invoke = (lambda state: asyncio.run(agent.ainvoke(state, config))) if asynchronous else (lambda state: agent.invoke(state, config))
    result = invoke({"messages": [HumanMessage("go")]})
    assert result["messages"][-1].content == "done" and agent.get_state(config).next == ()
    # The checkpointed thread is not stuck on the tools node: the next turn runs.
    assert invoke({"messages": [HumanMessage("again")]})["messages"][-1].content == "resumed"
    message = next(m for m in result["messages"] if m.type == "tool")
    return message.content, message.status == "error"


@recover("openai")
def recover_openai(runtime, scope, asynchronous):
    from openai import AsyncOpenAI, OpenAI
    from caveman_middleware.openai import with_caveman_openai_tools

    client = (AsyncOpenAI if asynchronous else OpenAI)(api_key="test", base_url="http://127.0.0.1:9")
    loop = with_caveman_openai_tools(client, runtime=runtime, scope=scope, protocol="openai-chat", tools=[], functions={})
    result = loop.functions["caveman_retrieve"]({"handle": HANDLE})  # the application loop's native dispatch
    return json.dumps(asyncio.run(result) if asynchronous else result), None


@recover("anthropic")
def recover_anthropic(runtime, scope, asynchronous):
    import importlib
    from anthropic import Anthropic, AsyncAnthropic, DefaultHttpxClient
    from caveman_middleware._httpx2 import sdk_flavour
    from caveman_middleware.anthropic import with_caveman_anthropic

    http, received = importlib.import_module(sdk_flavour(DefaultHttpxClient)), []

    def provider(request):
        received.append(json.loads(request.content))
        content, stop = (([{"type": "tool_use", "id": "r1", "name": "caveman_retrieve", "input": {"handle": HANDLE}}], "tool_use")
                         if len(received) == 1 else ([{"type": "text", "text": "done"}], "end_turn"))
        return http.Response(200, json={"id": "msg", "type": "message", "role": "assistant", "model": "m", "stop_reason": stop,
                                        "stop_sequence": None, "content": content, "usage": {"input_tokens": 1, "output_tokens": 1}})

    params = dict(model="m", max_tokens=16, messages=[{"role": "user", "content": "go"}], tools=[])
    if asynchronous:
        async def run():
            async with AsyncAnthropic(api_key="test", http_client=http.AsyncClient(transport=http.MockTransport(provider))) as client:
                await with_caveman_anthropic(client, runtime=runtime, scope=scope).beta.messages.tool_runner(**params).until_done()
        asyncio.run(run())
    else:
        with Anthropic(api_key="test", http_client=http.Client(transport=http.MockTransport(provider))) as client:
            with_caveman_anthropic(client, runtime=runtime, scope=scope).beta.messages.tool_runner(**params).until_done()
    result = next(block for block in received[1]["messages"][-1]["content"] if block.get("tool_use_id") == "r1")
    return _text(result["content"]), result.get("is_error") is True


@recover("google")
def recover_google(runtime, scope, asynchronous):
    import httpx
    from google import genai
    from google.genai import types
    from caveman_middleware.google import with_caveman_google

    received = []

    def provider(request):
        received.append(json.loads(request.content))
        part = ({"functionCall": {"name": "caveman_retrieve", "args": {"handle": HANDLE}}} if len(received) == 1 else {"text": "done"})
        return httpx.Response(200, json={"candidates": [{"content": {"role": "model", "parts": [part]}, "finishReason": "STOP"}]})

    def read_log() -> str:
        """Read the log."""
        return "log"

    options = types.HttpOptions(httpx_client=httpx.Client(transport=httpx.MockTransport(provider)),
                                httpx_async_client=httpx.AsyncClient(transport=httpx.MockTransport(provider)))
    wrapped = with_caveman_google(genai.Client(api_key="test", http_options=options), runtime=runtime, scope=scope)
    call = dict(model="gemini-test", contents="go", config=types.GenerateContentConfig(tools=[read_log]))
    asyncio.run(wrapped.aio.models.generate_content(**call)) if asynchronous else wrapped.models.generate_content(**call)
    response = next(part["functionResponse"]["response"] for part in received[1]["contents"][-1]["parts"] if "functionResponse" in part)
    return json.dumps(response["result"]), None  # AFC sends a function's return value as {"result": ...}


@recover("strands")
def recover_strands(runtime, scope, asynchronous):
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
            received.append(messages)
            yield {"messageStart": {"role": "assistant"}}
            if len(received) == 1:
                yield {"contentBlockStart": {"start": {"toolUse": {"toolUseId": "r1", "name": "caveman_retrieve"}}}}
                yield {"contentBlockDelta": {"delta": {"toolUse": {"input": json.dumps({"handle": HANDLE})}}}}
                yield {"contentBlockStop": {}}
                yield {"messageStop": {"stopReason": "tool_use"}}
            else:
                yield {"contentBlockDelta": {"delta": {"text": "done"}}}
                yield {"contentBlockStop": {}}
                yield {"messageStop": {"stopReason": "end_turn"}}

        async def structured_output(self, output_model, prompt, system_prompt=None, **kwargs):
            yield {}

    agent = Agent(**with_caveman_agent({"model": Provider(), "tools": []}, runtime=runtime, scope=scope), callback_handler=None)
    asyncio.run(agent.invoke_async("go")) if asynchronous else agent("go")
    result = next(block["toolResult"] for message in received[1] for block in message["content"] if "toolResult" in block)
    return _text(result["content"]), result["status"] == "error"


@recover("agno")
def recover_agno(runtime, scope, asynchronous):
    from agno.models.base import Model
    from agno.tools.function import FunctionCall
    from caveman_middleware.agno import with_caveman_agent

    class Provider(Model):
        def invoke(self, *args, **kwargs):
            raise AssertionError("not called")

        async def ainvoke(self, *args, **kwargs):
            raise AssertionError("not called")

        def invoke_stream(self, *args, **kwargs):
            raise AssertionError("not called")

        async def ainvoke_stream(self, *args, **kwargs):
            raise AssertionError("not called")

        def _parse_provider_response(self, response, **kwargs):
            return response

        def _parse_provider_response_delta(self, response):
            return response

    agent = with_caveman_agent({"model": Provider(id="fake"), "tools": []}, runtime=runtime, scope=scope)
    model, tool = agent["model"], agent["tools"][-1]
    if asynchronous:
        tool = tool.model_copy()
        tool.entrypoint = model.connection.async_recovery  # what Model.aresponse registers for the run
    # Agno's Model.response() opens this per-run frame; its tool loop runs each call through FunctionCall.
    token = model.connection.active.set(model._frame("response", (), {"messages": [], "tools": [tool]}))
    try:
        call = FunctionCall(function=tool, arguments={"handle": HANDLE}, call_id="r1")
        result = asyncio.run(call.aexecute()) if asynchronous else call.execute()
    finally:
        model.connection.active.reset(token)
    assert result.status == "success"
    return result.result, None


@recover("crewai")
def recover_crewai(runtime, scope, asynchronous):
    from crewai import BaseLLM
    from crewai.tools import BaseTool
    from caveman_middleware.crewai import with_caveman_agent

    class Provider(BaseLLM):
        def call(self, *args, **kwargs):
            return "done"

    class ReadLog(BaseTool):
        name: str = "read_log"
        description: str = "Read the log"

        def _run(self):
            return "log"

    agent = with_caveman_agent({"llm": Provider(model="fake"), "tools": [ReadLog()]}, runtime=runtime, scope=scope)
    tool = agent["tools"][-1]  # the agent keeps its CavemanLLM alive; the tool holds only a weak reference
    return tool.run(handle=HANDLE), None  # CrewAI's executors run tools through the sync run(), async crews included


@recover("pydantic_ai")
def recover_pydantic_ai(runtime, scope, asynchronous):
    from pydantic_ai import Agent
    from pydantic_ai.messages import ModelResponse, TextPart, ToolCallPart, ToolReturnPart
    from pydantic_ai.models.function import FunctionModel
    from caveman_middleware.pydantic_ai import CavemanCapability

    def model(messages, info):
        if len(messages) == 1:
            return ModelResponse(parts=[ToolCallPart("caveman_retrieve", {"handle": HANDLE}, tool_call_id="r1")])
        return ModelResponse(parts=[TextPart("done")])

    agent = Agent(FunctionModel(model), capabilities=[CavemanCapability(runtime=runtime, scope=scope)])
    result = asyncio.run(agent.run("go")) if asynchronous else agent.run_sync("go")
    assert result.output == "done"
    part = next(p for m in result.all_messages() for p in m.parts if type(p) is ToolReturnPart and p.tool_name == "caveman_retrieve")
    return part.model_response_str(), part.outcome == "failed"


@recover("autogen")
def recover_autogen(runtime, scope, asynchronous):
    from autogen_ext.models.replay import ReplayChatCompletionClient
    from caveman_middleware.autogen import with_caveman_agent

    def read_log() -> str:
        """Read the log."""
        return "log"

    workbench = with_caveman_agent({"model_client": ReplayChatCompletionClient(["done"]), "tools": [read_log]},
                                   runtime=runtime, scope=scope)["workbench"]

    async def call():
        await workbench.list_tools()  # AssistantAgent lists the tools every turn
        return await workbench.call_tool("caveman_retrieve", {"handle": HANDLE})

    result = asyncio.run(call())
    return result.result[0].content, result.is_error


@recover("llama_index")
def recover_llama_index(runtime, scope, asynchronous):
    from llama_index.core.llms.llm import ToolSelection
    from llama_index.llms.openai import OpenAI
    from caveman_middleware.llama_index import CavemanFunctionAgent, with_caveman_tools

    def read_log() -> str:
        """Read the log."""
        return "log"

    bundle = with_caveman_tools(OpenAI(api_key="test", model="gpt-4o"), runtime=runtime, scope=scope, tools=[read_log])
    call = ToolSelection(tool_id="r1", tool_name="caveman_retrieve", tool_kwargs={"handle": HANDLE})
    output = asyncio.run(bundle.aexecute(call)) if asynchronous else bundle.execute(call)  # the application loop's dispatch
    # The FunctionAgent's registered executor answers the same way.
    agent = CavemanFunctionAgent(runtime=runtime, scope=scope, llm=OpenAI(api_key="test", model="gpt-4o"), tools=[read_log])
    page = asyncio.run(agent.caveman_recovery.async_(None, HANDLE)) if asynchronous else agent.caveman_recovery.sync(None, HANDLE)
    assert json.loads(page) == json.loads(output.content)
    return output.content, None


@recover("mcp")
def recover_mcp(runtime, scope, asynchronous):
    from caveman_middleware.mcp import CavemanMCPHost

    host = CavemanMCPHost(runtime=runtime, scope=scope, server_id="server", protocol_version="2025-06-18")
    result = asyncio.run(host.recovery.execute({"handle": HANDLE}))
    return result.content[0].text, result.is_error


@pytest.mark.parametrize("family", sorted(RECOVER))
@pytest.mark.parametrize("asynchronous", [False, True])
@pytest.mark.parametrize("status,code", ANSWERS)
def test_refused_recovery_is_a_native_tool_error_and_the_run_completes(family, asynchronous, status, code, caplog):
    from caveman_cloud.middleware import REASON_CATALOG, Scope
    require_adapter(family)
    runtime = peer_runtime(strict=True)  # even strict mode never raises out of a tool executor
    runtime.refuse = None if code == "not_found" else (status, code)  # an unknown handle is the runtime's own 404
    try:
        text, is_error = RECOVER[family](runtime, Scope("tests", "recover"), asynchronous)
    finally:
        runtime.close()
    assert runtime.retrievals and runtime.retrievals[-1]["handle"] == HANDLE
    assert json.loads(text) == {"error": code}
    assert is_error in (True, None), f"{family} returned the refusal as a successful tool result"
    assert (f"reason={code}" in caplog.text) == REASON_CATALOG[code].warn_once


def test_cancellation_still_propagates_out_of_recovery():
    """Only MiddlewareError becomes a tool result: a cancelled retrieve still cancels the host's call."""
    require_adapter("openai")
    from openai import AsyncOpenAI
    from caveman_cloud.middleware import Scope
    from caveman_middleware.openai import with_caveman_openai_tools

    runtime = peer_runtime()
    view = runtime.as_async()

    async def cancelled(*args, **kwargs):
        raise asyncio.CancelledError

    view.retrieve = cancelled
    loop = with_caveman_openai_tools(AsyncOpenAI(api_key="test", base_url="http://127.0.0.1:9"), runtime=view,
                                     scope=Scope("tests", "cancel"), protocol="openai-chat", tools=[], functions={})
    with pytest.raises(asyncio.CancelledError):
        asyncio.run(loop.functions["caveman_retrieve"]({"handle": HANDLE}))
    runtime.close()


@pytest.mark.parametrize("arguments", [None, [], "handle", 7, {}, {"handle": 1}, [{"handle": HANDLE}]])
def test_malformed_recovery_arguments_are_invalid_request(arguments):
    """Model-written arguments that are not an object with a string handle never reach the runtime or raise past it."""
    from caveman_cloud.middleware import MiddlewareError
    from caveman_middleware._guard import recovery_args

    with pytest.raises(MiddlewareError) as raised:
        recovery_args(arguments)
    assert raised.value.code == "invalid_request"
    assert recovery_args(None, {"handle": HANDLE}) == {"handle": HANDLE}


@pytest.mark.parametrize("asynchronous", [False, True])
def test_openai_recovery_answers_malformed_arguments(asynchronous):
    require_adapter("openai")
    from openai import AsyncOpenAI, OpenAI
    from caveman_cloud.middleware import Scope
    from caveman_middleware.openai import with_caveman_openai_tools

    runtime = peer_runtime()
    client = (AsyncOpenAI if asynchronous else OpenAI)(api_key="test", base_url="http://127.0.0.1:9")
    loop = with_caveman_openai_tools(client, runtime=runtime, scope=Scope("tests", "args"), protocol="openai-chat", tools=[], functions={})
    for arguments in (None, [], "handle", {}):
        result = loop.functions["caveman_retrieve"](arguments)
        assert (asyncio.run(result) if asynchronous else result) == {"error": "invalid_request"}
    assert runtime.retrievals == []
    runtime.close()
