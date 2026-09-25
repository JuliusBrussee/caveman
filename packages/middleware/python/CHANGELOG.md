# Changelog

`caveman-middleware` follows semver from 1.0.0: breaking changes wait for a new
major. Read this file before upgrading. pip skips prereleases unless you pin one
or pass `--pre`. Support policy: [SECURITY.md](../../../SECURITY.md#supported-versions).

## 1.0.0 — 2026-09-24

- First stable release. Requires `caveman-sdk` 1.2.0 or later
  (`caveman-sdk>=1.2,<2`): 1.1.0 lacks APIs the adapters import.
- `caveman_middleware.__version__` is read from the installed distribution.
- A refused `caveman_retrieve` (an unknown or expired handle, or a
  404/410/503) returns `{"error": code}` through each framework's tool-error
  result and warns once. The run carries on, and cancellation still
  propagates.
- A wrapped `AnthropicBedrock` client keeps `aws_profile`, so calls are signed
  with the caller's AWS identity.
- LiteLLM registers one process-wide callback however many instances are
  live, so the host's own callbacks are never dropped.
- Importing an adapter whose framework is out of range names the installed
  version and the supported range.
- Every reported reason is a spec §8 catalog code. Calls that were never LLM
  calls (embeddings, token counting, non-LLM ASGI routes) report nothing.
- `Adapter.version` is the installed package version.
- Strands 1.43 recovery no longer fails on a missing `cancel_signal`.
- CI tests the installed wheel, adds a Python 3.14 lane and classifier, and
  watches `tests/middleware-e2e/**`.
- `recovery_name_conflict` now reaches `on_diagnostic`, and strict `ready()`
  raises it.
- Version-gate warnings name the adapter through `decline()`, on both sync
  and async runtimes.
- Every adapter fails open: outside strict mode, an adapter or runtime error
  sends the original request and records a reason code (`adapter_error` for
  adapter bugs). Strict mode raises `MiddlewareError`. Every pass-through
  reason is logged once on `caveman.middleware`.
- Version gate: a framework outside its tested range is skipped with
  `unsupported_version` and one warning. `accept_framework_version=True`
  overrides it, and an unreadable version runs with `version_unverified`. New
  `preflight()` and `ready()` for startup checks; `COMPATIBILITY` lists tiers
  and ranges.
- Certified tier: langchain, openai, anthropic, litellm. Everything else is
  experimental.
- openai 2.x (httpx) and 3.x (httpx2) are both supported, so importing the
  adapter no longer fails with `No module named 'httpx2'`. Stored Responses
  calls pass through with `provider_state_retained` unless `store=False` or
  `allow_stored_responses=True`.
- Anthropic Bedrock and Vertex clients are supported.
- An existing `caveman_retrieve` tool no longer raises: recovery is disabled
  for that registration and `recovery_name_conflict` is logged once.
- Free-form thread and session ids are hashed into valid scopes. A missing
  scope passes through instead of failing the agent call.
- Adapters accept a sync or async runtime on either code path.
- Long histories are budgeted (`manifest_bytes`, 2 MiB), and bytes and images
  are hashed instead of skipping the call. ASGI reports `payload_budget` for
  oversize bodies and no longer requires fastapi or starlette.
- Google: wrapping returns clones and never mutates or closes your client, and
  `unwrap_google()` returns the original.
- LiteLLM: bounded in-flight state, sync Router support for OpenAI and
  Anthropic, `unsupported_provider` for other providers.
- CrewAI registers its hooks once per process. AutoGen lists workbench tools
  once per turn.
- Accepted ranges now match the lowest versions actually tested. Python 3.11–3.13.
  The `pydantic-ai` extra uses `pydantic-ai-slim`.
- CI: floor and latest lanes with constraints files, and a nightly canary that
  opens an issue on failure.
- **Breaking (license):** relicensed from MIT to Apache-2.0, along with the rest of
  the repository in Caveman 3.0.0. Releases before this one keep the MIT license.
- Release process: each release gets a GitHub Release with these notes and a
  CycloneDX SBOM of its dependency graph.

## 0.1.0a1 — 2026-09-15

- First alpha of the native framework adapters, one install extra per
  framework family.
