# Caveman Python middleware

Native framework adapters for the Caveman compression runtime. Your framework keeps its inference client, tools, retries, streams, and original conversation. Caveman projects eligible tool-result text into a copied outbound request. Inference stays with your provider.

**Alpha.** This quickstart targets published middleware `0.1.0a1` and SDK `1.1.0`. Current source may contain unreleased APIs. Use the [release notes and limitations](https://github.com/JuliusBrussee/caveman/blob/main/packages/middleware/python/CHANGELOG.md) before upgrading.

## Run a complete example

Follow the [LangChain quickstart](https://docs.caveman.so/docs/sdk/middleware/python) for a fresh environment and [runtime installation](https://docs.caveman.so/docs/sdk/middleware/deployment#run-it-as-a-process). Start the local runtime separately; the client package does not include it.

```sh
python -m pip install 'caveman-sdk==1.1.0' 'caveman-middleware[langchain]==0.1.0a1' 'langchain==1.4.1' 'langchain-core==1.6.3' 'langgraph==1.2.11'
curl -fsSLo quickstart.py https://docs.caveman.so/examples/middleware/quickstart.py
DEMO_MODE=record python quickstart.py
DEMO_MODE=compress python quickstart.py
DEMO_MODE=off python quickstart.py
```

The default example makes no provider request. It runs a deterministic native model/tool loop against the real local runtime, checks compression and exact paginated recovery, and asserts that application history retains originals. The optional provider run is separately labeled and can incur charges.

## Choose an integration

- [Framework guide](https://docs.caveman.so/docs/sdk/middleware/frameworks#python): public entrypoints, native APIs, recovery ownership, transports, and limitations.
- [Compatibility matrix](https://docs.caveman.so/docs/sdk/middleware/frameworks): resolver ranges versus accepted ranges versus exact validation evidence. A range is not an exhaustive test result.
- [Deployment](https://docs.caveman.so/docs/sdk/middleware/deployment): process/container lifecycle, remote TLS/authentication, persistence, session affinity, deadlines, and rollback.
- [Recovery and scope](https://github.com/JuliusBrussee/caveman/blob/main/docs/technical/middleware-protocol.md): namespace/session/branch/cache epoch, exact originals, excerpts, and expiry.
- [Troubleshooting](https://docs.caveman.so/docs/sdk/troubleshooting#middleware): final reason codes and strict readiness versus normal inference fallback.
- [Measurement](https://docs.caveman.so/docs/sdk/middleware/deployment#what-to-measure): quality, latency, retries, recovery calls, cache effects, and provider usage.

## Adapters, tiers, and what compresses

Certified adapters (`langchain`, `openai`, `anthropic`, `litellm`) gate every release. Experimental adapters get the same fail-open guard, logging, and behavioral tests, but their APIs may change in a minor release. `caveman_middleware.COMPATIBILITY` exposes each family's tier and tested version range.

Compression needs a recovery tool the model can call, so only some entry points compress. The record-only ones never change input: in `compress` mode each call reports `recovery_unbound`, logged once.

| Extra | Tier | Compresses | Record-only |
|---|---|---|---|
| `langchain` | certified | `with_caveman_agent`, or `CavemanMiddleware` with its `recovery_tool` in the agent's tools | `with_caveman_model`; `CavemanDocumentCompressor` without `source_expansion` |
| `openai` | certified | `with_caveman_openai_tools` | `with_caveman_openai` |
| `anthropic` | certified | `beta.messages.tool_runner` on a `with_caveman_anthropic` client | `messages.create` on that client |
| `litellm` | certified | `CavemanLiteLLM` with `operator_recovery` | calls without `operator_recovery` |
| `google` | experimental | `with_caveman_google` / `with_caveman_google_chat` with callable AFC tools, over the Caveman transports | calls without callable tools |
| `strands` | experimental | `with_caveman_agent` | `with_caveman_model` |
| `agno` | experimental | `with_caveman_agent` | `with_caveman_model` |
| `crewai` | experimental | `with_caveman_agent` for an agent with tools | `with_caveman_llm` alone |
| `pydantic-ai` | experimental | `CavemanCapability` on the `Agent` | `with_caveman_model` |
| `autogen` | experimental | `with_caveman_agent`, or `CavemanWorkbench` with its client | `with_caveman_model` |
| `llama-index` | experimental | `CavemanFunctionAgent`, `with_caveman_tools` | `with_caveman_model`; `CavemanNodePostprocessor` without `source_expansion` |
| `mcp` | experimental | `CavemanMCPHost.register` plus `project_result` | none |
| `asgi` | experimental | `CavemanASGIMiddleware` with an `ASGIContext.recovery` binding | the same middleware without one |

If your app already has a tool named `caveman_retrieve`, that tool keeps its name, recovery stays off for that registration, and the adapter logs `recovery_name_conflict` once.

## Providers

- **OpenAI:** openai 2.x (on `httpx`) and 3.x (on `httpx2`). The adapter follows whichever HTTP library the installed SDK uses. A Responses API call that the provider would store (`store` defaults to true) is sent unchanged with reason `provider_state_retained`. Pass `store=False`, or `allow_stored_responses=True` to opt in.
- **Anthropic:** `Anthropic`, `AnthropicBedrock`, and `AnthropicVertex`, sync and async.
- **LiteLLM:** the async and proxy paths project any provider, because they edit LiteLLM's OpenAI-format input. The sync `Router` path edits the translated request and supports only the `openai` and `anthropic` providers; other providers pass through with reason `unsupported_provider`.
- **LlamaIndex:** the `OpenAI` and `Anthropic` LLMs, including subclasses such as `AzureOpenAI`. Bedrock Converse, Vertex, and other LLMs pass through with reason `unsupported_provider`.
- **Pydantic AI:** `OpenAIChatModel` (including Azure and OpenAI-compatible providers) and `AnthropicModel` (including Bedrock and Vertex clients). `FallbackModel`, Google/Gemini, Bedrock Converse, and other models pass through with reason `unsupported_provider`.
- **Google:** wrapping returns a clone, so your `Client` or `Chat` is never modified. Wrapping a wrapped object does not stack, and `unwrap_google()` returns the original. One Caveman transport serves every clone: each call carries the scope of the clone that made it.

## Framework versions

Each adapter checks the installed framework version against its tested range:

- **Outside the range:** the adapter skips (original input, reason `unsupported_version`) and logs a warning once. Pass `accept_framework_version=True` after testing a newer release yourself.
- **Unreadable version** (vendored or bundled builds): the adapter runs and logs `version_unverified` once.

Wrapping never raises for a version problem, even in strict mode. To fail fast at startup, call `caveman_middleware.preflight(runtime, "langchain")`, which reports `unavailable` for an untested framework. `caveman_middleware.ready(...)` raises instead.

| Extra | Accepted (gate and extra) | Oldest tested | Newest tested |
|---|---|---|---|
| `langchain` | langchain 1.1–<2, langchain-core 1.1–<2, langgraph 1.0.2–<2 | 1.1.0 / 1.1.0 / 1.0.2 | 1.4.2 / 1.6.5 / 1.2.12 |
| `openai` | openai 2.20–<4 | 2.20.0 | 2.54.0, 3.19.2 |
| `anthropic` | anthropic 1.0–<2 | 1.0.0 | 1.8.0 |
| `litellm` | litellm 1.95–<2 | 1.95.0 | 1.102.1 |
| `google` | google-genai 2.18–<3 | 2.18.0 | 2.25.0 |
| `strands` | strands-agents 1.43–<2 | 1.43.0 | 1.57.0 |
| `agno` | agno 3.0–<4 | 3.0.0 | 3.0.11 |
| `crewai` | crewai 1.15.3–<2 | 1.15.3 | 1.15.22 |
| `pydantic-ai` | pydantic-ai-slim 2.36–<3 | 2.36.0 | 2.49.0 |
| `autogen` | autogen-agentchat, -core, -ext 0.7–<0.8 | 0.7.0 | 0.7.5 |
| `llama-index` | llama-index-core 0.14.5–<0.15 | 0.14.5 | 0.14.25 |
| `mcp` | mcp 2.0–<3 | 2.0.0 | 2.2.0 |
| `asgi` | none (ASGI 3) | n/a | n/a |

CI tests the oldest versions on Python 3.11 and the newest on Python 3.13, using `constraints/floor.txt` and `constraints/latest.txt`. It also runs the certified families on 3.12. A nightly run installs each extra's newest releases with no constraints and opens an issue when one breaks an adapter.

`[openai]` accepts openai 2.20 through 3.x. LiteLLM and CrewAI require `openai<3`, so `[openai,litellm]` or `[openai,crewai]` installs openai 2.x. The adapter supports 2.x, but you can't get openai 3.x in the same environment as those two frameworks. `[asgi]` has no dependencies; it works with any ASGI 3 server or framework.

## Failures, scopes, and logging

- **Fail-open:** outside strict mode, any adapter or runtime failure sends your original request and reports a reason (`adapter_error` for a bug in adapter code). In strict mode those reasons raise `MiddlewareError`, except version problems (see above).
- **One warning per problem:** every adapter logs one `WARNING` per adapter and reason on the `caveman.middleware` logger. The line never contains content, scope values, or credentials.
- **Scopes:** thread and session IDs may be free-form. An email or `"user 42 / chat #7"` is hashed into a valid scope token. A missing scope, such as `scope_from_config` without a `thread_id`, passes through with reason `invalid_scope`; strict mode raises.
- **Runtimes:** every adapter accepts either `MiddlewareRuntime` or `AsyncMiddlewareRuntime` and uses the matching view on sync and async paths.
- **Long histories:** history hashing stops at 2 MiB (`manifest_bytes`) and images or bytes are hashed, so a long or multimodal history never skips the whole call. The ASGI adapter's body limit is `max_body_bytes` (default 2 MiB); a larger body passes through with reason `payload_budget`.

## Contracts to keep

Keep original stored history. Register the actual recovery executor through the native helper; a tool schema alone does not attest recovery. Handles are scope-bound and expire according to runtime retention. Recoverability does not guarantee model quality.

Client modes are `off`, `record`, and `compress`. The client defaults to compression; the standalone runtime defaults to recording. Set both deliberately. Runtime unavailability normally retains original inference input. Strict mode, startup `ready()`, cancellation, and requested recovery failures have different error contracts.

Final decision reports contain status, reason, transform IDs, replacement/reuse counts, and call IDs. **They contain no token counters.** Local segment estimates are inferred; provider usage and billed savings are separate evidence. Nothing in this local example verifies billing savings.

Close the runtime client and native framework/provider resources at shutdown. Closing the client does not stop the runtime process. See the deployment guide before sharing a runtime across workers or tenants.

## Licence and support

The client, adapters, and Engine runtime are all Apache-2.0. Read [LICENSING.md](https://github.com/JuliusBrussee/caveman/blob/main/LICENSING.md). This package is separate from Caveman Agent SDK. File sanitized reproducible issues in [Caveman](https://github.com/JuliusBrussee/caveman/issues).
