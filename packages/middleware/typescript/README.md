# Caveman Typescript middleware

Native framework adapters for the Caveman compression runtime. Your framework keeps its inference client, tools, retries, streams, and original conversation. Caveman projects eligible tool-result text into a copied outbound request. Inference stays with your provider.

**Alpha.** This quickstart targets published middleware `0.1.0-alpha.2` and SDK `1.1.0`. Current source may contain unreleased APIs. Use the [release notes and limitations](https://github.com/JuliusBrussee/caveman/blob/main/packages/middleware/typescript/CHANGELOG.md) before upgrading.

## Run a complete example

Follow the [AI SDK quickstart](https://docs.caveman.so/docs/sdk/middleware/vercel-ai-sdk) for a fresh environment and [runtime installation](https://docs.caveman.so/docs/sdk/middleware/deployment#run-it-as-a-process). Start the local runtime separately; the client package does not include it.

```sh
npm install --save-exact @caveman-ai/sdk@1.1.0 @caveman-ai/middleware@0.1.0-alpha.2 ai@7.0.94 @ai-sdk/provider@4.0.11 zod@4.4.3
curl -fsSLo quickstart.ts https://docs.caveman.so/examples/middleware/quickstart.ts
DEMO_MODE=record node --experimental-strip-types quickstart.ts
DEMO_MODE=compress node --experimental-strip-types quickstart.ts
DEMO_MODE=off node --experimental-strip-types quickstart.ts
```

The default example makes no provider request. It runs a deterministic native model/tool loop against the real local runtime, checks compression and exact paginated recovery, and asserts that application history retains originals. The optional provider run is separately labeled and can incur charges.

## Choose an integration

- [Framework guide](https://docs.caveman.so/docs/sdk/middleware/frameworks#typescript): public entrypoints, native APIs, recovery ownership, transports, and limitations.
- [Compatibility matrix](https://docs.caveman.so/docs/sdk/middleware/frameworks): resolver ranges versus accepted ranges versus exact validation evidence. A range is not an exhaustive test result.
- [Deployment](https://docs.caveman.so/docs/sdk/middleware/deployment): process/container lifecycle, remote TLS/authentication, persistence, session affinity, deadlines, and rollback.
- [Recovery and scope](https://github.com/JuliusBrussee/caveman/blob/main/docs/technical/middleware-protocol.md): namespace/session/branch/cache epoch, exact originals, excerpts, and expiry.
- [Troubleshooting](https://docs.caveman.so/docs/sdk/troubleshooting#middleware): final reason codes and strict readiness versus normal inference fallback.
- [Measurement](https://docs.caveman.so/docs/sdk/middleware/deployment#what-to-measure): quality, latency, retries, recovery calls, cache effects, and provider usage.

## Entry points: which ones compress

Compression needs a recovery tool the adapter registered itself, so the model can always fetch an exact original. Entry points that cannot register one only record: in `compress` mode they pass content through unchanged, report `recovery_unbound`, and log once which entry point to use instead.

| Adapter | Tier | Compresses | Records only (`recovery_unbound` in compress mode) |
|---|---|---|---|
| `ai-sdk` | certified | `withCaveman` | `createCavemanMiddleware` (`wrapLanguageModel`) |
| `openai` | certified | `withCavemanOpenAITools`, `chat.completions.runTools` on `withCavemanOpenAI` | plain `create` calls on `withCavemanOpenAI` |
| `anthropic` | certified | `beta.messages.toolRunner` on `withCavemanAnthropic` | `messages.create` / `messages.stream` |
| `langchain` | certified | `withCavemanAgent`, `createCavemanLangChain` | `withCavemanModel`, `CavemanChatModel` |
| `google` | experimental | `CavemanGoogleGenAI` with callable tools (automatic function calling) | calls without callable tools |
| `strands` | experimental | `withCavemanStrands` | `withCavemanStrandsModel` |
| `mastra` | experimental | `withCavemanMastra` | `createCavemanMastraProcessor` |
| `mcp` | experimental | `CavemanMCPHost` with `register()`ed tools | |

Certified adapters are gated by the conformance suite. Experimental ones get the same fail-open guard, logging and tests, but may change in a minor release. `CavemanGoogleGenAI` hooks the Google SDK's internal `ApiClient`; if that moves, the client falls back to the native one (`adapter_error`). `CavemanDocumentCompressor` (LangChain RAG) compresses only with a `sourceExpansion` reader. `@caveman-ai/middleware/langchain` loads `langchain`; if you only wrap a chat model, import `withCavemanModel`, `CavemanChatModel` and `scopeFromConfig` from `@caveman-ai/middleware/langchain-model` instead, which needs only `@langchain/core`. Every compressing entry point disables recovery if your tools already include one named `caveman_retrieve`; calls then report `recovery_name_conflict`.

## Framework versions

The package declares **no peer dependencies**. Optional framework peers made a plain `npm install` fail with `ERESOLVE` (0.1.0-alpha.1), so the version check happens at run time instead, against the copy your application installed. Under pnpm with `hoist=false`, add the frameworks you use to `public-hoist-pattern`. `@strands-agents/sdk` itself declares peers on `openai` 6, `@ai-sdk/provider` 3 and `@anthropic-ai/sdk` 0.109, so npm needs `--legacy-peer-deps` to install it next to newer versions of those.

`inspectFrameworkCompatibility(adapter)` from `@caveman-ai/middleware/compatibility` reports the installed version, supported range, tested releases and tier without importing a framework. `package.json` lists the same data in `supportedFrameworkVersions` and `testedFrameworkVersions`.

- **Out of range** (including every prerelease, such as `7.1.0-canary.3`): calls pass through unchanged, reporting `unsupported_version`, with one warning. After testing that version, set `acceptFrameworkVersion: true` in the adapter options.
- **Unreadable** (a deploy bundle with no `node_modules`): the adapter checks that the framework hooks it needs exist and runs, with one `version_unverified` warning. If a hook is missing it passes through with `version_unavailable`.
- Nothing throws when you wrap a client. With `strict: true`, the version decision surfaces from `runtime.ready()` / `preflight()`.

## Scope per request

Every adapter takes `scope` as a value or a function. A function is called for each request, inside that request's async context, so one module-level client can serve many users, and `runtime.deleteSession(scope)` removes exactly one user's originals:

```ts
const client = withCavemanOpenAI(new OpenAI(), { runtime, fetch, scope: () => ({ namespace: 'app', session_id: currentUser().id }) });
```

LangChain passes the `RunnableConfig` (`config => scopeFromConfig(config, 'app')` reads `configurable.thread_id`), Mastra its `RequestContext`, Strands the agent. Any ID text works: values outside `[A-Za-z0-9._:/-]` (emails, `user 42 / chat #7`) become a stable `h-` hash. A missing scope (no `thread_id`) runs the call recovery-free with `recovery_unbound`. An unusable one reports `invalid_scope`. Neither throws, except `invalid_scope` in strict mode.

## Budgets and logs

A large history never skips the whole call. Each tool result over the runtime's `segment_bytes` is skipped on its own (`payload_budget`), and images or bytes enter the context manifest as hashes. `manifestBytes` (default 2 MiB) bounds how much history is hashed, and `wireBytes` (default 16 MiB, OpenAI and Anthropic) bounds the request body the adapter parses. OpenAI Responses turns pass through with `provider_state_retained` unless `store: false`, because OpenAI would store the compressed turn; `allowStoredResponses: true` opts in.

Each pass-through reason logs once per process through `console.warn`, as `adapter=<id> reason=<code>`, never content, scope values or credentials. Any exception inside adapter code becomes a pass-through with `adapter_error`; with `strict: true` the call raises `MiddlewareError('adapter_error')` instead.

## Runtimes and module systems

Node.js 22.12 or later, ESM `import` or CommonJS `require()`. TypeScript resolves the types with `moduleResolution` `bundler`, `nodenext`, `node16` or `node10`. A CommonJS project on `node16` also needs `skipLibCheck` (the `tsc --init` default). Edge runtimes (Cloudflare `workerd`, Vercel `edge-light`) are not supported: adapters rely on `node:async_hooks` call ownership, and importing one under those conditions throws an explanatory error. Run the adapter in a Node.js function instead.

## Contracts to keep

Keep original stored history. Register the actual recovery executor through the native helper; a tool schema alone does not attest recovery. Handles are scope-bound and expire according to runtime retention. Recoverability does not guarantee model quality.

Client modes are `off`, `record`, and `compress`. The client defaults to compression; the standalone runtime defaults to recording. Set both deliberately. Runtime unavailability normally retains original inference input. Strict mode, startup `ready()`, cancellation, and requested recovery failures have different error contracts.

Final decision reports contain status, reason, transform IDs, replacement/reuse counts, and call IDs. **They contain no token counters.** Local segment estimates are inferred; provider usage and billed savings are separate evidence. Nothing in this local example verifies billing savings.

Close the runtime client and native framework/provider resources at shutdown. Closing the client does not stop the runtime process. See the deployment guide before sharing a runtime across workers or tenants.

## Licence and support

The client, adapters, and Engine runtime are all Apache-2.0. Read [LICENSING.md](https://github.com/JuliusBrussee/caveman/blob/main/LICENSING.md). This package is separate from Caveman Agent SDK. File sanitized reproducible issues in [Caveman](https://github.com/JuliusBrussee/caveman/issues).
