# caveman-sdk

`caveman-sdk` is the MIT-licensed Python client in the main Caveman repository. Import it as `caveman_cloud`. Version `1.1.0` requires Python 3.13 or newer, uses only the standard library at runtime, and includes `py.typed` type information.

## Install

```bash
mkdir caveman-python-example
cd caveman-python-example
python3.13 -m venv .venv
source .venv/bin/activate
python -m pip install caveman-sdk==1.1.0
```

On Windows, activate with `.venv\Scripts\Activate.ps1` in PowerShell. Use `python -m pip` so installation targets the interpreter running your app. The PyPI package named `caveman` is unrelated.

## Configure your service

Set these variables through your shell or secret manager:

```bash
export CAVE_BASE_URL="https://your-caveman-service.example"
export CAVE_API_KEY="your-service-key"
export CAVE_MODEL="your-enabled-model-id"
# Only when your service requires a provider key:
export OPENAI_API_KEY="your-provider-key"
```

Replace the placeholder address with your configured service. The SDK does not supply a default address, obtain credentials, or start a local runtime. See [configuration](https://docs.caveman.so/docs/sdk/configuration).

## Make your first request

Save this as `quickstart.py`:

```python
import json
import os
from urllib.error import HTTPError, URLError
from caveman_cloud import Cave

cave = Cave(
    api_key=os.environ["CAVE_API_KEY"],
    base_url=os.environ["CAVE_BASE_URL"],
    agent="support-agent",
    default_workflow="answer-question",
)

try:
    response = cave.openai(
        upstream_key=os.environ.get("OPENAI_API_KEY"),
    ).responses.create({
        "model": os.environ["CAVE_MODEL"],
        "input": "Explain what a retry loop is in one sentence.",
    })
    print(json.dumps(response, indent=2))
except HTTPError as error:
    raise SystemExit(f"Provider request failed: HTTP {error.code}") from error
except (URLError, TimeoutError) as error:
    raise SystemExit(f"Service connection failed: {error}") from error
```

```bash
python quickstart.py
```

A successful call returns the provider JSON as a Python dictionary. It is not an HTTP response object: it has no `.headers`, `.json()`, or `.output_text` attribute.

## Compress a string

After creating `cave`, call a service that supports the compression API:

```python
original = json.dumps({"records": [
    {"id": 1, "status": "ok"},
    {"id": 2, "status": "ok"},
]})
compressed = cave.compress(original, content_type="json")
print(compressed.output)
print(compressed.tokens_before, compressed.tokens_after)
print(compressed.basis, compressed.recovery_handle)
```

The return value is a `CompressResult` dataclass. Access its fields with dots. The SDK preserves the original string on transport or parse failure; small inputs may also remain unchanged. Read [compression](https://docs.caveman.so/docs/sdk/compression) before treating an unchanged result as a working service check.

## Call from an async application

Core client methods perform blocking I/O. They are not coroutines. Move a call off the event loop when integrating with an async application:

```python
import asyncio

async def compress_tool_output(text: str):
    return await asyncio.to_thread(cave.compress, text, content_type="text")
```

Cancelling the awaiting task does not forcibly stop the underlying thread's HTTP request. Provider calls, compression, and shared-context calls use a 300-second urllib timeout; most other connected SDK operations use 30 seconds. The core `Cave` constructor has no timeout or cancellation option. These socket timeouts are not a whole-workflow deadline.

For native async framework compression, the separate middleware entrypoint exposes `AsyncMiddlewareRuntime`. That does not turn the core provider clients into async clients.

## Python naming and limits

Python uses `base_url`, `default_workflow`, `tool_search`, and `retry_loop_breaker`; TypeScript uses camelCase. Python chat completions use `cave.openai().chat["completions"].create(body)`. Trace providers use `trace.model["openai"]`.

The SDK does not execute tool calls, process provider SSE streams, or install a compression runtime. Follow [provider calls](https://docs.caveman.so/docs/sdk/providers), [deferred tools](https://docs.caveman.so/docs/sdk/tools), [tracing](https://docs.caveman.so/docs/sdk/tracing), and the [API reference](https://docs.caveman.so/docs/sdk/reference) for complete workflows.

## Full documentation

[SDK overview](https://docs.caveman.so/docs/sdk) · [API reference](https://docs.caveman.so/docs/sdk/reference) · [Troubleshooting](https://docs.caveman.so/docs/sdk/troubleshooting)

## Native framework middleware

For automatic projection of eligible tool results in an existing framework, use the separate [middleware package](https://docs.caveman.so/docs/sdk/middleware). Start with the complete [LangChain quickstart](https://docs.caveman.so/docs/sdk/middleware/python). The local runtime is accountless; inference stays in your provider client. The thin connected APIs above remain explicit calls.
