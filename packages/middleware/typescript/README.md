# Caveman framework middleware

Native framework adapters over the local Caveman compression runtime. Your
framework keeps its models, tool loop, retries, streams and stored conversation.
Caveman only changes the text of selected tool results in the outbound model
view, and only when the local runtime answers in time.

Alpha. Install the package plus whichever framework you already use — every
framework is an optional peer, so installing this package installs none of them.

```sh
npm install @caveman-ai/sdk @caveman-ai/middleware
```

```ts
import { createMiddlewareRuntime } from '@caveman-ai/sdk/middleware';
import { withCaveman } from '@caveman-ai/middleware/ai-sdk';
import { streamText } from 'ai';

const runtime = createMiddlewareRuntime({
  endpoint: 'http://127.0.0.1:8787',
  onReport: report => console.log(report.status, report.reason),
});
await runtime.ready();
const scope = { namespace: 'my-app', session_id: 'conversation-1', branch_id: 'main', cache_epoch: '0' };

// Keep the application's existing model, tools, messages and stop conditions.
const result = streamText(withCaveman(existingOptions, { runtime, scope }));
```

`withCaveman` registers a native `caveman_retrieve` tool so the model can read
an original back. The `createCavemanMiddleware` model-only variant uses
recovery-free transforms instead. Neither rewrites your stored messages. Call
`runtime.close()` at shutdown.

## Supported frameworks

The range is the band the adapter's version gate accepts; the left-hand value is
the version its wire handling was written against.

| Subpath | Tested range |
|---|---|
| `@caveman-ai/middleware/ai-sdk` | `ai >=7.0.94 <8`, `@ai-sdk/provider >=4.0.11 <5` |
| `@caveman-ai/middleware/openai` | `openai >=7.12 <8` |
| `@caveman-ai/middleware/anthropic` | `@anthropic-ai/sdk >=0.124 <1` |
| `@caveman-ai/middleware/google` | `@google/genai >=2.21 <3` |
| `@caveman-ai/middleware/langchain` | `langchain >=1.5 <2`, `@langchain/core >=1.2 <2`, `@langchain/langgraph >=1.4 <2` |
| `@caveman-ai/middleware/strands` | `@strands-agents/sdk >=1.17 <2` |
| `@caveman-ai/middleware/mastra` | `@mastra/core >=1.65 <2` |
| `@caveman-ai/middleware/mcp` | `@modelcontextprotocol/sdk >=1.30 <2` |

Outside its range an adapter keeps the caller's native input unchanged and
reports `unsupported_version`. It never throws. Set `strict: true` on the
runtime only if you want a pre-inference error instead.

## OpenAI SDK

```ts
import { withCavemanOpenAI } from '@caveman-ai/middleware/openai';

const client = withCavemanOpenAI(existingOpenAIClient, { runtime, scope, fetch: existingFetch });
const runner = client.chat.completions.runTools(existingToolLoopOptions);
const answer = await runner.finalContent();
```

Pass the fetch function the existing client uses. The returned native client
keeps its public response helpers. `runTools` supplies the native recovery
executor; plain generation calls stay recovery-free. Server-held Responses
histories remain opaque.

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
  reported as skipped.

## Runtime modes and reports

- `off` delegates every native call and reports `disabled`, with no optimizer
  request and no receipts.
- `record` measures candidates without replacing any request text.
- `compress` replaces text, and requires recovery when the transform is lossy.

`onReport` receives one immutable record per native call after the adapter picks
its final request view: status, reason, transform ids, replacement and reuse
counts. No original content, no provider credentials. `runtime.lastReport` holds
the most recent one; it is not a history. A callback that throws cannot affect
inference.

Token counts in reports are estimates from the runtime's tokenizer over the
segments it saw. They are not measured billing savings, and this package does
not claim a reduction figure.
