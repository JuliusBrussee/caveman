"""Native LangChain contracts; deterministic protocol peer, no model API calls."""
import asyncio
import copy
import json

import pytest

from frameworks import require_adapter


@pytest.fixture
def langchain():
    return require_adapter("langchain")


def messages():
    from langchain_core.messages import AIMessage, HumanMessage, ToolMessage
    return [HumanMessage("summarize"), AIMessage("", tool_calls=[{"id": "call-1", "name": "read_log", "args": {}}]),
            ToolMessage("[INFO] café 🌍\r\n" * 400, tool_call_id="call-1", name="read_log")]


@pytest.mark.parametrize("asynchronous", [False, True])
def test_agent_projection_and_native_recovery_preserve_history(langchain, protocol_runtime, asynchronous):
    from caveman_cloud.middleware import Scope
    from langchain.agents.middleware.types import ModelRequest, ModelResponse
    from langchain_core.language_models.fake_chat_models import FakeListChatModel
    from langchain_core.messages import AIMessage

    original = messages()
    before = copy.deepcopy(original)
    options = langchain.with_caveman_agent({"tools": []}, runtime=protocol_runtime, scope=Scope("tests", "langchain"))
    middleware, recovery = options["middleware"][0], options["tools"][0]
    request = ModelRequest(model=FakeListChatModel(responses=["unused"]), messages=original, tools=options["tools"])
    seen = []

    def handler(projected):
        seen.extend(projected.messages)
        return ModelResponse(result=[AIMessage("done")])

    if asynchronous:
        async def drive():
            async def async_handler(projected):
                return handler(projected)
            response = await middleware.awrap_model_call(request, async_handler)
            handle = seen[-1].content.split("handle=", 1)[1].split("]", 1)[0]
            return response, json.loads(await recovery.ainvoke({"handle": handle}))
        response, restored = asyncio.run(drive())
    else:
        response = middleware.wrap_model_call(request, handler)
        handle = seen[-1].content.split("handle=", 1)[1].split("]", 1)[0]
        restored = json.loads(recovery.invoke({"handle": handle}))
    assert response.result[0].content == "done"
    assert original == before
    assert seen[-1].content.startswith("[caveman: shortened;")
    assert seen[-1].tool_call_id == original[-1].tool_call_id
    assert restored["text"].encode() == original[-1].content.encode()
    assert [event.status for event in protocol_runtime.reports] == ["applied"]
    assert [receipt["event_kind"] for receipt in protocol_runtime.receipts] == ["dispatch_intent", "completed"]


def test_model_only_entry_resolves_scope_per_call_and_never_compresses(langchain, protocol_runtime, caplog):
    """D-C7: with_caveman_model has no recovery executor, so compress mode is recovery_unbound, warned once."""
    from langchain_core.language_models.fake_chat_models import FakeListChatModel

    scopes = []
    def scope(config):
        scopes.append(config["configurable"]["thread_id"])
        return langchain.scope_from_config(config, namespace="tests")

    model = langchain.with_caveman_model(FakeListChatModel(responses=["done"]), runtime=protocol_runtime, scope=scope)
    original = messages()
    assert model.invoke(original, {"configurable": {"thread_id": "one"}}).content == "done"
    assert asyncio.run(model.ainvoke(original, {"configurable": {"thread_id": "two"}})).content == "done"
    assert scopes == ["one", "two"]
    assert protocol_runtime.requests == []
    assert original[-1].content.startswith("[INFO]")
    assert [(event.status, event.reason) for event in protocol_runtime.reports] == [("skipped", "recovery_unbound")] * 2
    assert "adapter=langchain reason=recovery_unbound" in caplog.text


@pytest.mark.parametrize("asynchronous", [False, True])
def test_stream_failure_is_failed_not_cancelled(langchain, protocol_runtime, asynchronous):
    from caveman_cloud.middleware import Scope
    from langchain_core.language_models.fake_chat_models import FakeListChatModel, FakeListChatModelError
    from caveman_middleware._native import owner

    model = langchain.with_caveman_model(FakeListChatModel(responses=["ok"], error_on_chunk_number=1),
                                       runtime=protocol_runtime, scope=Scope("tests", "stream-failure"))
    if asynchronous:
        async def drive():
            stream = model.astream(messages())
            assert (await anext(stream)).content == "o"
            assert owner.get() is None
            with pytest.raises(FakeListChatModelError):
                await anext(stream)
        asyncio.run(drive())
    else:
        stream = model.stream(messages())
        assert next(stream).content == "o"
        assert owner.get() is None
        with pytest.raises(FakeListChatModelError):
            next(stream)
    assert [receipt["event_kind"] for receipt in protocol_runtime.receipts] == ["dispatch_intent", "failed"]


def test_async_stream_close_propagates_immediately(langchain, protocol_runtime):
    from caveman_cloud.middleware import Scope
    from langchain_core.language_models.fake_chat_models import FakeListChatModel

    model = langchain.with_caveman_model(FakeListChatModel(responses=["hello"]), runtime=protocol_runtime, scope=Scope("tests", "cancel"))
    async def drive():
        stream = model.astream(messages())
        assert (await anext(stream)).content == "h"
        await stream.aclose()
        assert [receipt["event_kind"] for receipt in protocol_runtime.receipts] == ["dispatch_intent", "cancelled"]
    asyncio.run(drive())


@pytest.mark.parametrize("asynchronous", [False, True])
def test_native_stream_completion(langchain, protocol_runtime, asynchronous):
    from caveman_cloud.middleware import Scope
    from langchain_core.language_models.fake_chat_models import FakeListChatModel
    from caveman_middleware._native import owner

    model = langchain.with_caveman_model(FakeListChatModel(responses=["done"]), runtime=protocol_runtime, scope=Scope("tests", "complete"))
    if asynchronous:
        async def drive():
            chunks = []
            async for value in model.astream(messages()):
                assert owner.get() is None
                chunks.append(value.content)
            return "".join(chunks)
        answer = asyncio.run(drive())
    else:
        answer = "".join(value.content for value in model.stream(messages()))
    assert answer == "done"
    assert protocol_runtime.requests == []  # model-only: recovery_unbound, no optimize I/O
    assert [receipt["event_kind"] for receipt in protocol_runtime.receipts] == ["dispatch_intent", "completed"]


def test_async_invoke_cancellation_propagates(langchain, protocol_runtime):
    from caveman_cloud.middleware import Scope
    from langchain_core.language_models.fake_chat_models import FakeListChatModel

    async def drive():
        entered = asyncio.Event()
        class BlockedModel(FakeListChatModel):
            async def _agenerate(self, *args, **kwargs):
                entered.set()
                await asyncio.Future()

        model = langchain.with_caveman_model(BlockedModel(responses=["unused"]), runtime=protocol_runtime, scope=Scope("tests", "cancel-call"))
        task = asyncio.create_task(model.ainvoke(messages()))
        await asyncio.wait_for(entered.wait(), 1)
        task.cancel()
        with pytest.raises(asyncio.CancelledError):
            await task
    asyncio.run(drive())
    assert [receipt["event_kind"] for receipt in protocol_runtime.receipts] == ["dispatch_intent", "cancelled"]


def test_async_agent_cancellation_propagates(langchain, protocol_runtime):
    from caveman_cloud.middleware import Scope
    from langchain.agents.middleware.types import ModelRequest
    from langchain_core.language_models.fake_chat_models import FakeListChatModel

    middleware = langchain.CavemanMiddleware(runtime=protocol_runtime, scope=Scope("tests", "cancel-agent"))
    request = ModelRequest(model=FakeListChatModel(responses=["unused"]), messages=messages())
    async def handler(projected):
        raise asyncio.CancelledError()
    async def drive():
        with pytest.raises(asyncio.CancelledError):
            await middleware.awrap_model_call(request, handler)
    asyncio.run(drive())
    assert [receipt["event_kind"] for receipt in protocol_runtime.receipts] == ["dispatch_intent", "cancelled"]


def test_sync_document_entry_accepts_async_runtime(langchain, protocol_runtime):
    """D5: an async runtime on the sync path is coerced to its sync view instead of raising."""
    from caveman_cloud.middleware import Scope
    from langchain_core.documents import Document

    compressor = langchain.CavemanDocumentCompressor(runtime=protocol_runtime.as_async(), scope=Scope("tests", "documents"))
    documents = [Document(page_content="original text")]
    assert compressor.compress_documents(documents, "query") == documents
    assert [(event.status, event.reason) for event in protocol_runtime.reports] == [("skipped", "recovery_unbound")]
