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

## Framework middleware runtime

The `@caveman-ai/sdk/middleware` entry connects native framework adapters to an
existing local runtime. It installs no framework or inference client.

```ts
import { createMiddlewareRuntime, type CallReport } from '@caveman-ai/sdk/middleware';

const report = (event: CallReport) => console.log(event.status, event.reason);
const runtime = createMiddlewareRuntime({
  endpoint: 'http://127.0.0.1:8787',
  onReport: report,
});
await runtime.ready();
// Pass runtime and an explicit conversation scope to a native adapter.
// Close runtime when the application shuts down.
```

Adapters report `applied`, `reused`, `skipped`, `recorded`, or `disabled` after
deciding which request view to use. Reports contain immutable transform IDs,
replacement/reuse counts, and optional adapter/call IDs, never original content.
`runtime.lastReport` holds the latest report across this runtime, without a
history. A throwing or rejected report callback does not affect inference.
Reports describe projection decisions; provider usage and billing evidence are
separate. Calling the low-level `optimize()` primitive only prepares a plan and
does not claim it was applied.

See [TypeScript SDK documentation](https://caveman.so/docs/sdk/typescript).
