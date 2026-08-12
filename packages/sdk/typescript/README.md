# @caveman-ai/sdk

Zero-runtime-dependency TypeScript client for Caveman gateway cooperation.

```bash
npm install @caveman-ai/sdk
```

```ts
import { Cave } from "@caveman-ai/sdk";

const cave = new Cave({
  apiKey: process.env.CAVE_API_KEY!,
  baseURL: "http://127.0.0.1:8787",
  agent: "support-agent",
});

const result = await cave.compress("large payload");
console.log(result.output, result.basis); // basis is inferred
```

Main surfaces: provider clients, `compress`, deferred tool search, reversible
checkpoints and artifacts, retry-loop interruption, runtime policy, and a
dependency-free OTLP/JSON exporter. Async jobs are reserved and fail locally
with `cave_async_jobs_unavailable`; they send no request.

Requires Node.js 22.13 or newer. Package is MIT licensed. Connected calls need
a Caveman gateway key; local Engine compression remains accountless and ships
through the separate Caveman runtime.

See [TypeScript SDK documentation](https://caveman.so/docs/sdk/typescript).
