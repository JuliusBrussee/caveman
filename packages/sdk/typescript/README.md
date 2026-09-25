# @caveman-ai/sdk

`@caveman-ai/sdk` is the MIT-licensed TypeScript client in the main Caveman repository. It ships ESM and TypeScript declarations with no runtime dependencies. This quickstart uses published version `1.1.0` on Node.js 22.13 or newer.

## Install

```bash
mkdir caveman-ts-example
cd caveman-ts-example
npm init -y
npm install @caveman-ai/sdk@1.1.0
```

Use an `.mjs` file for the first run. In a TypeScript project, use ESM and NodeNext module resolution. The package does not publish a CommonJS `require` entrypoint.

## Configure your service

Set these variables in your shell or secret manager. Replace the values with your deployment's service address, accepted key, and enabled model:

```bash
export CAVE_BASE_URL="https://your-caveman-service.example"
export CAVE_API_KEY="your-service-key"
export CAVE_MODEL="your-enabled-model-id"
# Only when your service requires a provider key:
export OPENAI_API_KEY="your-provider-key"
```

The example address is a placeholder. Installing this package does not launch a service. The Caveman key authenticates to your configured service; the provider key supplies upstream access. Keep both on your application server.

## Make your first request

Save this as `quickstart.mjs`:

```js
import { Cave } from "@caveman-ai/sdk";

function required(name) {
  const value = process.env[name];
  if (!value) throw new Error(`Set ${name} before running this example`);
  return value;
}

const cave = new Cave({
  apiKey: required("CAVE_API_KEY"),
  baseURL: required("CAVE_BASE_URL"),
  agent: "support-agent",
  defaultWorkflow: "answer-question",
  timeoutMs: 30_000,
});

try {
  const response = await cave.openai({
    upstreamKey: process.env.OPENAI_API_KEY,
  }).responses.create({
    model: required("CAVE_MODEL"),
    input: "Explain what a retry loop is in one sentence.",
  });
  console.log(JSON.stringify(response, null, 2));
} catch (error) {
  console.error(error instanceof Error ? error.message : String(error));
  process.exitCode = 1;
}
```

```bash
node quickstart.mjs
```

A successful call prints the provider's parsed JSON response, including its native output and usage fields when present. The client does not add the official OpenAI SDK's convenience properties. Read the provider response shape your application requested.

## Compress a string

In the same program, after creating `cave`, you can call a service that supports the compression API:

```ts
const original = JSON.stringify({ records: [
  { id: 1, status: "ok" },
  { id: 2, status: "ok" },
] });
const compressed = await cave.compress(original, { contentType: "json" });
console.log({
  output: compressed.output,
  before: compressed.tokensBefore,
  after: compressed.tokensAfter,
  basis: compressed.basis,
  recoveryHandle: compressed.recoveryHandle,
});
```

Small payloads may remain unchanged. Network or malformed-response failures also return the original string with zero reported reduction. Use the [compression report](https://docs.caveman.so/docs/sdk/compression) to distinguish a useful transformation from pass-through; successful JavaScript execution alone proves neither compression nor savings.

## Use TypeScript

Import public result and option types from the same entrypoint:

```ts
import type { CaveOptions, CompressResult } from "@caveman-ai/sdk";

const options: CaveOptions = {
  apiKey: process.env.CAVE_API_KEY!,
  baseURL: process.env.CAVE_BASE_URL!,
  agent: "support-agent",
};
const report: CompressResult = await cave.compress("tool output");
console.log(report.tokenCountBasis);
```

The non-null assertions above are type annotations, not environment validation. Keep the quickstart's `required()` checks at your application boundary.

## Next steps

- [Configure credentials, labels, timeouts, and cancellation](https://docs.caveman.so/docs/sdk/configuration).
- [Use Anthropic, Gemini, Vertex, raw responses, or streaming](https://docs.caveman.so/docs/sdk/providers).
- [Select tools](https://docs.caveman.so/docs/sdk/tools), [manage context](https://docs.caveman.so/docs/sdk/context), or [trace a workflow](https://docs.caveman.so/docs/sdk/tracing).
- [Add local framework middleware](https://docs.caveman.so/docs/sdk/middleware) when you want to keep an existing framework integration.

The SDK does not install the engine, run an agent loop, or guarantee that your configured service supports every exported method.

## Full documentation

[SDK overview](https://docs.caveman.so/docs/sdk) · [API reference](https://docs.caveman.so/docs/sdk/reference) · [Troubleshooting](https://docs.caveman.so/docs/sdk/troubleshooting)

## Native framework middleware

For automatic projection of eligible tool results in an existing framework, use the separate [middleware package](https://docs.caveman.so/docs/sdk/middleware). Start with the complete [AI SDK quickstart](https://docs.caveman.so/docs/sdk/middleware/typescript). The local runtime is accountless; inference stays in your provider client. The thin connected APIs above remain explicit calls.
