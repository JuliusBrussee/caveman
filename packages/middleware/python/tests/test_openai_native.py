"""Real OpenAI resource calls against an in-memory HTTP peer; no API charges."""
import asyncio
import copy
import json

import pytest

from frameworks import require_adapter


@pytest.fixture
def adapter():
    return require_adapter("openai")


ORIGINAL = "[INFO] café 🌍\r\n" * 400


def inputs(protocol):
    if protocol == "openai-chat":
        return {"messages": [
            {"role": "assistant", "tool_calls": [{"type": "function", "id": "call-1", "function": {"name": "read_log", "arguments": "{}"}}]},
            {"role": "tool", "tool_call_id": "call-1", "content": ORIGINAL},
        ]}
    return {"input": [
        {"type": "function_call", "call_id": "call-1", "name": "read_log", "arguments": "{}"},
        {"type": "function_call_output", "call_id": "call-1", "output": ORIGINAL},
    ]}


def reply(protocol):
    if protocol == "openai-chat":
        return {"id": "c", "object": "chat.completion", "created": 0, "model": "m", "choices": [
            {"index": 0, "finish_reason": "stop", "message": {"role": "assistant", "content": "done"}}]}
    return {"id": "r", "object": "response", "created_at": 0, "model": "m", "status": "completed", "output": [
        {"id": "msg", "type": "message", "role": "assistant", "status": "completed", "content": [
            {"type": "output_text", "text": "done", "annotations": []}]}]}


@pytest.mark.parametrize("protocol", ["openai-chat", "openai-responses"])
@pytest.mark.parametrize("asynchronous", [False, True])
def test_provider_projection_and_registered_recovery(adapter, protocol_runtime, protocol, asynchronous):
    import httpx2
    from openai import AsyncOpenAI, OpenAI
    from caveman_cloud.middleware import Scope

    received = []
    original = inputs(protocol)
    before = copy.deepcopy(original)
    definitions = [{"type": "function", "function": {"name": "read_log", "description": "Read log", "parameters": {"type": "object", "properties": {}}}}]
    if protocol == "openai-responses":
        definitions = [{"type": "function", **definitions[0]["function"]}]

    def provider(request):
        received.append(json.loads(request.content))
        return httpx2.Response(200, json=reply(protocol))

    def wrap(client, runtime):
        return adapter.with_caveman_openai_tools(client, runtime=runtime, scope=Scope("tests", "openai"), protocol=protocol,
                                                tools=definitions, functions={"read_log": lambda _: ORIGINAL})

    def projected():
        if protocol == "openai-chat":
            return received[0]["messages"][-1]["content"]
        return received[0]["input"][-1]["output"]

    def call(loop):
        create = loop.client.chat.completions.create if protocol == "openai-chat" else loop.client.responses.create
        return create(model="m", tools=loop.tools, **original)

    def handle():
        return projected().split("handle=", 1)[1].split("]", 1)[0]

    if asynchronous:
        async def drive():
            async with AsyncOpenAI(api_key="test", http_client=httpx2.AsyncClient(transport=httpx2.MockTransport(provider))) as client:
                loop = wrap(client, protocol_runtime.as_async())
                result = await call(loop)
                restored = await loop.functions["caveman_retrieve"]({"handle": handle()})
                return result, restored
        result, restored = asyncio.run(drive())
    else:
        with OpenAI(api_key="test", http_client=httpx2.Client(transport=httpx2.MockTransport(provider))) as client:
            loop = wrap(client, protocol_runtime)
            result = call(loop)
            restored = loop.functions["caveman_retrieve"]({"handle": handle()})
    assert (result.choices[0].message.content if protocol == "openai-chat" else result.output_text) == "done"
    assert original == before
    assert restored["text"].encode() == ORIGINAL.encode()
    assert projected().startswith("[caveman: shortened;")
    assert [event.status for event in protocol_runtime.reports] == ["applied"]
    assert [receipt["event_kind"] for receipt in protocol_runtime.receipts] == ["dispatch_intent", "completed"]


@pytest.mark.parametrize("asynchronous", [False, True])
@pytest.mark.parametrize("ending", ["completed", "cancelled", "failed"])
def test_native_stream_lifecycle_preserved(adapter, protocol_runtime, asynchronous, ending):
    import httpx2
    from openai import AsyncOpenAI, OpenAI
    from caveman_cloud.middleware import Scope
    from caveman_middleware._native import owner

    closed = []
    chunk = {"id": "chunk", "object": "chat.completion.chunk", "created": 0, "model": "m", "choices": [
        {"index": 0, "delta": {"content": "hello"}, "finish_reason": None}]}
    first = f"data: {json.dumps(chunk)}\n\n".encode()

    class SyncBody(httpx2.SyncByteStream):
        def __iter__(self):
            yield first
            if ending == "failed":
                raise ValueError("provider stream failed")
            yield b"data: [DONE]\n\n"
        def close(self):
            closed.append(True)

    class AsyncBody(httpx2.AsyncByteStream):
        async def __aiter__(self):
            yield first
            if ending == "failed":
                raise ValueError("provider stream failed")
            yield b"data: [DONE]\n\n"
        async def aclose(self):
            closed.append(True)

    def provider(request):
        assert json.loads(request.content)["messages"][-1]["content"] == "short excerpt"
        return httpx2.Response(200, headers={"content-type": "text/event-stream"}, stream=AsyncBody() if asynchronous else SyncBody())

    if asynchronous:
        async def drive():
            async with AsyncOpenAI(api_key="test", http_client=httpx2.AsyncClient(transport=httpx2.MockTransport(provider))) as client:
                wrapped = adapter.with_caveman_openai(client, runtime=protocol_runtime.as_async(), scope=Scope("tests", "stream"))
                stream = await wrapped.chat.completions.create(model="m", stream=True, **inputs("openai-chat"))
                async with stream:
                    assert (await anext(stream)).choices[0].delta.content == "hello"
                    assert owner.get() is None
                    if ending == "failed":
                        with pytest.raises(ValueError, match="provider stream failed"):
                            await anext(stream)
                    elif ending == "completed":
                        with pytest.raises(StopAsyncIteration):
                            await anext(stream)
        asyncio.run(drive())
    else:
        with OpenAI(api_key="test", http_client=httpx2.Client(transport=httpx2.MockTransport(provider))) as client:
            wrapped = adapter.with_caveman_openai(client, runtime=protocol_runtime, scope=Scope("tests", "stream"))
            with wrapped.chat.completions.create(model="m", stream=True, **inputs("openai-chat")) as stream:
                assert next(stream).choices[0].delta.content == "hello"
                assert owner.get() is None
                if ending == "failed":
                    with pytest.raises(ValueError, match="provider stream failed"):
                        next(stream)
                elif ending == "completed":
                    with pytest.raises(StopIteration):
                        next(stream)
    assert closed
    assert [receipt["event_kind"] for receipt in protocol_runtime.receipts] == ["dispatch_intent", ending]
