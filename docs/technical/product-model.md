# Product model

Caveman has two local adoption paths. Install the response skill when you want
shorter answers. Install the runtime when you want to reduce context sent to a
model, keep local measurements, or give an agent recovery tools. They can run
independently.

## Layer map

| Layer | Job | Account | License |
|---|---|---|---|
| Skill, hooks, and plugins | Ask an agent to answer with less filler while preserving technical text | No | Apache-2.0 |
| CLI | Install components, launch agents, expose local commands, and connect optional hosted commands | No for local commands | Apache-2.0 |
| Engine | Detect payload shape, apply a matching transform, count estimated tokens, and store recoveries | No | Apache-2.0 |
| Local proxy | Route provider requests through Engine and write local usage rows | No | Apache-2.0 |
| MCP, memory, browser, and shrink binaries | Expose recovery and specialized local context tools | No | Apache-2.0 |
| SDKs and Agent SDK | Add tracing, context assembly, tools, evals, and provider routing to application code | Local use needs no account | Apache-2.0 |
| Connected commands | Access an authenticated hosted project | Yes | CLI remains Apache-2.0 |

The whole repository is Apache-2.0; see [`LICENSING.md`](../../LICENSING.md).

## Response compression

The [`caveman` skill](../../skills/caveman/SKILL.md) changes response style.
It removes filler, shortens common phrasing, and permits fragments at stronger
levels. Code blocks, exact errors, commands, identifiers, and technical detail
stay intact.

This path affects model output. Its instruction text also consumes input
tokens. A short task can cost more total tokens with the skill enabled. The
correct comparison is provider-reported usage for equivalent tasks; see
[`HONEST-NUMBERS.md`](../HONEST-NUMBERS.md).

## Context compression

The local runtime changes selected model inputs. An agent may repeatedly send
old tool results and logs plus files, schemas or history. Engine classifies each
candidate and applies a content-specific compressor. Every lossy result follows
this sequence:

1. Store exact original bytes in Caveman Context Recovery (CCR).
2. Return a smaller model-visible representation.
3. Include a handle that can recover the original.
4. Pass original bytes through if storage, parsing, or size checks fail.

The local proxy listens on loopback and forwards requests with the caller's
provider credential. It records usage in a local SQLite database. Local token
reductions carry basis `inferred`, because Engine uses an offline tokenizer
rather than provider billing counters.

## Keep your existing agent

The CLI wraps an installed coding agent by changing its provider endpoint for
the child process. It does not replace the agent loop. Declarative profiles
describe seven current launch targets: Claude Code, Codex, Gemini CLI, Aider,
Hermes, OpenClaw and opencode.

Applications can use the same proxy by changing a provider SDK base URL.
Copy-ready recipes cover Anthropic, OpenAI, Google Gen AI, Vercel AI SDK,
LangChain, LiteLLM, CrewAI, Pydantic AI, OpenAI Agents SDK, and raw HTTP. Run:

```bash
caveman tools sdk
caveman snippets
caveman snippets openai-ts --app my-service
```

## Local and connected boundaries

Local compression requires no Caveman account. Signing in adds connected
commands and can persist a hosted gateway URL. Command discovery keeps these
surfaces separate:

```bash
caveman help tools   # local commands
caveman help cloud   # authenticated commands
```

Managed traffic has a different data flow from local wrap. Local wrap sends
request content to the selected model provider and keeps CCR on disk. A hosted
gateway necessarily receives request and response content while proxying it.
[`SECURITY.md`](../../SECURITY.md) lists both flows.

## Evidence labels

Caveman uses labels that identify how a number was produced:

- `inferred`: local estimate, usually from the offline `o200k_base`
  tokenizer or catalog list prices
- provider-reported: usage counters returned by a model provider
- `benchmark_counterfactual`: paired benchmark result under a pinned method
- `verified`: a connected evidence state that local tools never mint

These labels do not convert into one another through wording. A local estimate
stays `inferred` even when its result looks plausible.

## Choose the smallest path

| Goal | Command or component |
|---|---|
| Shorter answers | Install skill; run `/caveman` |
| Byte-identical local metering | `caveman wrap --off <agent>` |
| Recoverable local compression | `caveman <agent>` |
| Dense text rendered for a supported vision model | `caveman wrap --pixel <agent>` |
| Provider SDK integration | Change base URL or use `@caveman-ai/sdk` |
| Durable local memory | `caveman tools mem` |
| Compressed browser context | `caveman tools browse` |
| Build a TypeScript agent | `npm create @caveman-ai/agent@latest` |

Start with one layer. Add another only when its measured result clears its
overhead for your workload.
