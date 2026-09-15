"""Proxy down must never break a model call.

Each test drives a real native object through the adapter with the Caveman
runtime pointed at a closed port. The adapter has to hand the framework back the
caller's own input and report that it skipped, not raise and not hang.
"""
import pytest

from conftest import bypassed


def test_langchain_returns_the_callers_messages(unreachable_runtime):
    pytest.importorskip("langchain_core", reason="install caveman-middleware[langchain]")
    from langchain_core.messages import AIMessage, HumanMessage, ToolMessage

    from caveman_middleware.langchain import _Connection
    from caveman_cloud.middleware import Scope

    original = [
        HumanMessage("summarise the log"),
        AIMessage("", tool_calls=[{"id": "call-1", "name": "read_log", "args": {}}]),
        ToolMessage("[ERROR] preserve this exactly\n" + "noise " * 400, tool_call_id="call-1", name="read_log"),
    ]
    view, attempt = _Connection(unreachable_runtime, Scope("tests", "session-1")).prepare(list(original))
    assert [m.content for m in view] == [m.content for m in original], "an unreachable runtime rewrote the request"
    attempt.observe("dispatch_intent")
    assert bypassed(unreachable_runtime), "the adapter reported no decision for a call it could not optimize"


def test_asgi_passes_the_original_request_body_downstream():
    pytest.importorskip("caveman_cloud", reason="caveman-sdk is not installed")
    pytest.importorskip("starlette", reason="install caveman-middleware[asgi]")
    import asyncio
    import json

    from caveman_cloud.middleware import AsyncMiddlewareRuntime, Scope
    from caveman_middleware.asgi import ASGIContext, CavemanASGIMiddleware
    from conftest import _closed_port

    body = json.dumps({"model": "m", "messages": [
        {"role": "user", "content": "summarise"},
        {"role": "assistant", "tool_calls": [{"type": "function", "id": "c1", "function": {"name": "read", "arguments": "{}"}}]},
        {"role": "tool", "tool_call_id": "c1", "content": "[ERROR] keep this\n" + "noise " * 400},
    ]}).encode()

    seen = []

    async def application(scope, receive, send):
        chunks = b""
        while True:
            message = await receive()
            chunks += message.get("body", b"")
            if not message.get("more_body"):
                break
        seen.append(chunks)
        await send({"type": "http.response.start", "status": 200, "headers": []})
        await send({"type": "http.response.body", "body": b"{}"})

    async def drive():
        reports = []
        runtime = AsyncMiddlewareRuntime(endpoint=f"http://127.0.0.1:{_closed_port()}", deadline_ms=200,
                                         on_report=reports.append)
        middleware = CavemanASGIMiddleware(application, runtime=runtime, routes={"/v1/chat/completions": "openai-chat"},
                                           resolve_context=lambda scope: ASGIContext(Scope("tests", "asgi-1")))
        received = iter([{"type": "http.request", "body": body, "more_body": False}])

        async def receive():
            return next(received)

        await middleware({"type": "http", "method": "POST", "path": "/v1/chat/completions", "headers": []},
                         receive, lambda message: asyncio.sleep(0))
        await runtime.aclose()
        return reports

    reports = asyncio.run(drive())
    assert seen == [body], "an unreachable runtime changed the body the application received"
    assert [event.status for event in reports] == ["skipped"], reports


def test_openai_client_still_reaches_the_provider(unreachable_runtime):
    pytest.importorskip("openai", reason="install caveman-middleware[openai]")
    import json
    import threading
    from http.server import BaseHTTPRequestHandler, HTTPServer

    from openai import OpenAI

    from caveman_cloud.middleware import Scope
    from caveman_middleware.openai import with_caveman_openai

    received = []

    class Provider(BaseHTTPRequestHandler):
        def do_POST(self):
            received.append(self.rfile.read(int(self.headers["Content-Length"])))
            payload = json.dumps({"id": "c", "object": "chat.completion", "created": 0, "model": "m",
                                  "choices": [{"index": 0, "finish_reason": "stop",
                                               "message": {"role": "assistant", "content": "done"}}]}).encode()
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(payload)))
            self.end_headers()
            self.wfile.write(payload)

        def log_message(self, *_):
            pass

    provider = HTTPServer(("127.0.0.1", 0), Provider)
    threading.Thread(target=provider.serve_forever, daemon=True).start()
    try:
        client = with_caveman_openai(
            OpenAI(api_key="test-key", base_url=f"http://127.0.0.1:{provider.server_port}/v1"),
            runtime=unreachable_runtime, scope=Scope("tests", "openai-1"))
        tool_result = "[ERROR] keep this exactly\n" + "noise " * 400
        answer = client.chat.completions.create(model="m", messages=[
            {"role": "user", "content": "summarise"},
            {"role": "assistant", "tool_calls": [{"type": "function", "id": "c1", "function": {"name": "read", "arguments": "{}"}}]},
            {"role": "tool", "tool_call_id": "c1", "content": tool_result},
        ])
    finally:
        provider.shutdown()
    assert answer.choices[0].message.content == "done", "an unreachable runtime broke the provider call"
    sent = json.loads(received[0])["messages"][-1]["content"]
    assert sent == tool_result, "an unreachable runtime sent the provider something other than the original"
    assert bypassed(unreachable_runtime), "the adapter reported no decision for a call it could not optimize"
