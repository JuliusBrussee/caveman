# Caveman Typescript middleware

Native framework adapters for the Caveman compression runtime. Your framework keeps its inference client, tools, retries, streams, and original conversation. Caveman projects eligible tool-result text into a copied outbound request. Inference stays with your provider.

**Alpha.** This quickstart targets published middleware `0.1.0-alpha.2` and SDK `1.1.0`. Current source may contain unreleased APIs. Use the [release notes and limitations](https://docs.caveman.so/docs/sdk/middleware/releases) before upgrading.

## Run a complete example

Follow the [AI SDK quickstart](https://docs.caveman.so/docs/sdk/middleware/typescript) for a fresh environment and [runtime installation](https://docs.caveman.so/docs/sdk/middleware/deployment#local-process). Start the local runtime separately; the client package does not include it.

```sh
npm install --save-exact @caveman-ai/sdk@1.1.0 @caveman-ai/middleware@0.1.0-alpha.2 ai@7.0.94 @ai-sdk/provider@4.0.11 zod@4.4.3
curl -fsSLo quickstart.ts https://docs.caveman.so/examples/middleware/quickstart.ts
DEMO_MODE=record node --experimental-strip-types quickstart.ts
DEMO_MODE=compress node --experimental-strip-types quickstart.ts
DEMO_MODE=off node --experimental-strip-types quickstart.ts
```

The default example makes no provider request. It runs a deterministic native model/tool loop against the real local runtime, checks compression and exact paginated recovery, and asserts that application history retains originals. The optional provider run is separately labeled and can incur charges.

## Choose an integration

- [Framework guide](https://docs.caveman.so/docs/sdk/middleware/typescript-frameworks): public entrypoints, native APIs, recovery ownership, transports, and limitations.
- [Compatibility matrix](https://docs.caveman.so/docs/sdk/middleware/compatibility): resolver ranges versus accepted ranges versus exact validation evidence. A range is not an exhaustive test result.
- [Deployment](https://docs.caveman.so/docs/sdk/middleware/deployment): process/container lifecycle, remote TLS/authentication, persistence, session affinity, deadlines, and rollback.
- [Recovery and scope](https://docs.caveman.so/docs/sdk/middleware/recovery): namespace/session/branch/cache epoch, exact originals, excerpts, and expiry.
- [Troubleshooting](https://docs.caveman.so/docs/sdk/troubleshooting#middleware-decisions): final reason codes and strict readiness versus normal inference fallback.
- [Measurement](https://docs.caveman.so/docs/sdk/middleware/measurement): quality, latency, retries, recovery calls, cache effects, and provider usage.

## Contracts to keep

Keep original stored history. Register the actual recovery executor through the native helper; a tool schema alone does not attest recovery. Handles are scope-bound and expire according to runtime retention. Recoverability does not guarantee model quality.

Client modes are `off`, `record`, and `compress`. The client defaults to compression; the standalone runtime defaults to recording. Set both deliberately. Runtime unavailability normally retains original inference input. Strict mode, startup `ready()`, cancellation, and requested recovery failures have different error contracts.

Final decision reports contain status, reason, transform IDs, replacement/reuse counts, and call IDs. **They contain no token counters.** Local segment estimates are inferred; provider usage and billed savings are separate evidence. Nothing in this local example verifies billing savings.

Close the runtime client and native framework/provider resources at shutdown. Closing the client does not stop the runtime process. See the deployment guide before sharing a runtime across workers or tenants.

## Licence and support

The client and adapters are MIT; the Engine runtime has separate BSL terms. Read [LICENSING.md](https://github.com/JuliusBrussee/caveman/blob/main/LICENSING.md). This package is separate from Caveman Agent SDK. File sanitized reproducible issues in [Caveman](https://github.com/JuliusBrussee/caveman/issues).
