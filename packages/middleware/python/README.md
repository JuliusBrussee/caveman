# Caveman Python middleware

Native framework adapters over `caveman_cloud.middleware`. Your framework keeps
its models, tool loop, retries, streams and stored conversation. Caveman only
changes the text of selected tool results in the outbound model view, and only
when the local runtime answers in time.

Alpha. Install the extra for the framework you use:

```sh
pip install "caveman-middleware[langchain]"
```

```python
from caveman_cloud.middleware import MiddlewareRuntime, Scope
from caveman_middleware.langchain import with_caveman_agent
from langchain.agents import create_agent

runtime = MiddlewareRuntime(endpoint="http://127.0.0.1:8787")
runtime.ready()
scope = Scope("my-app", session_id="conversation-1", branch_id="main", cache_epoch="0")

agent = create_agent(**with_caveman_agent(existing_agent_options, runtime=runtime, scope=scope))
```

Call `runtime.close()` at shutdown. Nothing in your stored message history is
rewritten; only the request handed to the provider is.

## Supported frameworks

One extra per adapter. The range is the band the adapter's version gate accepts;
the left-hand value is the version its wire handling was written against.

| Extra | Module | Tested range |
|---|---|---|
| `langchain` | `caveman_middleware.langchain` | `langchain>=1.4,<2`, `langchain-core>=1.6,<2`, `langgraph>=1.2,<2` |
| `litellm` | `caveman_middleware.litellm` | `litellm>=1.100,<2` |
| `openai` | `caveman_middleware.openai` | `openai>=3.10,<4` |
| `anthropic` | `caveman_middleware.anthropic` | `anthropic>=1.4,<2` |
| `google` | `caveman_middleware.google` | `google-genai>=2.22,<3` |
| `strands` | `caveman_middleware.strands` | `strands-agents>=1.55,<2` |
| `agno` | `caveman_middleware.agno` | `agno>=3.0,<4` |
| `crewai` | `caveman_middleware.crewai` | `crewai>=1.15,<2` |
| `autogen` | `caveman_middleware.autogen` | `autogen-core`, `autogen-agentchat`, `autogen-ext` all `>=0.7,<0.8` |
| `pydantic-ai` | `caveman_middleware.pydantic_ai` | `pydantic-ai-slim>=2.42,<3` |
| `llama-index` | `caveman_middleware.llama_index` | `llama-index-core>=0.14,<0.15` |
| `asgi` | `caveman_middleware.asgi` | `fastapi>=0.141,<1`, `starlette>=1.6,<2` |
| `mcp` | `caveman_middleware.mcp` | `mcp>=2.2,<3` |

Outside its range an adapter keeps the caller's native input unchanged and
reports `unsupported_version`. It never raises.

Extras carry no provider pins, so most combine: `pip install
"caveman-middleware[langchain,agno,litellm]"` resolves. Three pairs cannot, and
the cause is upstream, not this package: `strands-agents` and `crewai` both
require `mcp<2.2` while the MCP adapter needs `>=2.2`, and `agno` and `crewai`
disagree over `litellm`. Install those families in separate environments.

## What you lose

Read this before turning on `compress`.

- **Markers are scoped.** A replacement marker is bound to
  `namespace` + `session_id` + `branch_id` + `cache_epoch` and to the
  authenticated principal that created it. Replaying it under any other scope
  returns nothing.
- **Markers expire.** A scope lives 24 hours, renewed each time it is used.
  After that the stored original is released.
- **Persisted compressed history is a trap.** If you save the transformed
  messages and replay them later — under a different scope, or after the scope
  expired — that text is gone for good. Caveman does not rewrite your stored
  history for this reason: keep the originals, let Caveman transform the
  outbound copy on every call.
- **Recovery needs a registered executor.** In `compress` mode a lossy
  transform is only used when your framework really holds the
  `caveman_retrieve` tool. Without it, only recovery-free transforms apply.
- **Eligible content is narrow.** Successful tool-result text and explicitly
  passed document bodies. Never system prompts, user messages, assistant
  reasoning, errored tool results, images or other non-text parts, anything
  marked protected, and never a payload the adapter cannot parse exactly.
- **Failure is silent and safe.** Runtime down, over deadline, out of capacity,
  expired scope: the original request goes to the provider and the decision is
  reported as skipped. Set `strict=True` only if you want a pre-inference error
  instead.

## Reports

`on_report=` receives one immutable record per native call: status, reason,
transform ids, replacement and reuse counts. No original content, no provider
credentials. `runtime.last_report` holds the most recent one. A callback that
raises cannot affect inference.

Token counts in reports are estimates from the runtime's tokenizer over the
segments it saw. They are not measured billing savings, and this package does
not claim a reduction figure.
