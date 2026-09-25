# Changelog

`@caveman-ai/middleware` follows semver from 1.0.0: breaking changes wait for a
new major. Read this file before upgrading. Prereleases publish under their own
npm dist-tag, never `latest`. Support policy: [SECURITY.md](../../../SECURITY.md#supported-versions).

## 1.0.0 — 2026-09-24

- First stable release. Requires `@caveman-ai/sdk` 1.2.0 or later.
- **Breaking (license):** relicensed from MIT to Apache-2.0, along with the rest of
  the repository in Caveman 3.0.0. Releases before this one keep the MIT license.
- Release process: prereleases no longer take the `latest` dist-tag, each
  release gets a GitHub Release with these notes and a CycloneDX SBOM, and the
  published dependency graph is audited before publish.
- Version gate:
  - A framework version outside the tested range, or a prerelease, now
    passes content through with one `unsupported_version` warning instead of
    silently doing nothing. `acceptFrameworkVersion` overrides it.
  - Bundled deploys run with a one-time `version_unverified` notice.
  - Nothing throws at wrap time; strict mode raises from `ready()`.
- The framework version is read from the application's installed copy.
  Framework peerDependencies are gone for good.
- `require()` works, and TypeScript resolves with node10, node16, nodenext
  and bundler. `./compatibility` exposes a `tier`. Importing under
  `workerd`/`edge-light` throws a clear unsupported-runtime error. `engines` is
  `>=22.12`.
- Every adapter accepts a per-request scope function. Emails and free-text ids
  are normalized, and a missing `thread_id` no longer fails the call.
- Adapter exceptions and changed SDK internals pass through as
  `adapter_error`. Every pass-through reason is logged once.
- Large histories and earlier images no longer skip the whole call.
  `manifestBytes` and `wireBytes` are configurable.
- A recovery tool-name clash reports `recovery_name_conflict`; the OpenAI tools
  helper no longer throws. OpenAI Responses turns pass through unless
  `store:false`. The Mastra oversize latch is per thread, not per process.
- Entry points that can only record say so at construction and report
  `recovery_unbound`.
- LangChain compressed copies no longer embed the original in `lc_kwargs`.
  Minified bundles keep compressing.
- Tiers: `ai-sdk`, `langchain`, `openai` and `anthropic` are certified; the
  others are experimental.
- `@anthropic-ai/sdk` range widened to `<0.129`. Tested up to ai 7.0.114,
  openai 7.23.0, @google/genai 2.24.0, langchain 1.5.12, @langchain/core 1.2.12,
  strands 1.19.0, mastra 1.70.0 and MCP 1.30.1.
- In strict mode, adapter exceptions now raise
  `MiddlewareError('adapter_error')` instead of passing through.
- Decline warnings name the adapter instead of `adapter=-`.
- New `@caveman-ai/middleware/langchain-model` subpath (`withCavemanModel`,
  `CavemanChatModel`, `scopeFromConfig`). It needs only `@langchain/core`;
  `/langchain` still exports everything.
- `.d.cts` shims type-check under node16 CommonJS without `skipLibCheck`.
- `CavemanDocumentCompressor` is also exported from `/langchain-model`, so it
  needs only `@langchain/core`.
- A `caveman_retrieve` the runtime refuses (an unknown or expired handle, or
  the runtime being down) now returns `{"error":"<code>"}`, or an MCP
  `isError` result, instead of crashing the native tool loop.
- The version gate reads the framework copy the adapter actually runs (openai
  and anthropic: the client's own version), whatever the working directory.
  It warns when the app resolves a different copy. `@ai-sdk/provider` is no
  longer gated.
- LangChain tool errors are marked `status:'error'` and never compressed.
- `withCaveman` is idempotent, and its bundle stays mutable; only the recovery
  tool is frozen. `CavemanChatModel.profile` returns the inner model's
  profile.
- ai-sdk retries reuse one logical call id and one optimization.
- A Mastra thread that overflows the scan budget compresses again on its next
  turn.
- Recovery context no longer leaks into calls on other clients. The
  `recovery_unbound` hint fires on use. Strict mode raises `adapter_error`
  from synchronous hooks.
- The wire now carries the real package version and the `@langchain/core`
  version. Every reported reason is a spec §8 catalog code.

## 0.1.0-alpha.2 — 2026-09-15

- Dropped the optional framework peer declarations. npm resolved them anyway,
  so a plain `npm install @caveman-ai/middleware` failed with ERESOLVE before
  any adapter was chosen. The runtime version gate still reports an
  unsupported framework.

## 0.1.0-alpha.1 — 2026-09-15

- First alpha of the native framework adapters. Superseded: it does not
  install cleanly with npm (see 0.1.0-alpha.2).
