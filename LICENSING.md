# Caveman Licensing

This repository uses a split license model. The public repo identity stays MIT
for the Caveman skill and adoption surfaces. The compression engine and Go
binaries that embed it use Business Source License 1.1 (BSL-1.1), with an
Additional Use Grant that permits first-party self-hosted production use and
requires a commercial license for third-party hosted, managed, or embedded
services.

## Canonical Files

- Root `LICENSE` is the MIT license plus a top-level scope note that points
  engine-linked directories to `LICENSE.BSL`.
- `LICENSE.BSL` is the canonical BSL-1.1 text for Caveman Engine-linked code.
- `LICENSING.md` is the per-directory source of truth.

## Per-Directory License

Default rule: a new module that imports, links, embeds, or ships as part of
Engine-linked runtime is BSL-1.1 unless a later decision explicitly classifies
it as MIT adoption surface.

| Path | License | Notes |
|---|---|---|
| `skills/` | MIT | Existing Caveman skill stays MIT and untouched. |
| `packages/agent/` | MIT | Agent runtime, build compiler, Claude lane, framework adapters, and coding-agent API. |
| `packages/create-caveman-agent/` | MIT | Zero-runtime-dependency Agent SDK initializer. |
| `packages/cli/` | MIT | Funnel/on-ramp. Launches BSL binaries but does not contain engine code. |
| `packages/sdk/typescript/` | MIT | Thin client and structural SDK surface. |
| `packages/sdk/python/` | MIT | Thin client; distribution name is `caveman-sdk`. |
| `packages/kit/` | MIT | React/headless honesty UI kit and badges. |
| `packages/graders/` | MIT | Public grader package; cloud eval-gating remains commercial. |
| `packages/mastra/` | MIT | Mastra telemetry and outcome adapter. |
| `packages/subagent-tax/` | MIT | Local zero-provider-call harness-prefix measurement tool. |
| `extension/` | MIT shell | Manifest, popup, content scripts, and UI are MIT. Bundled `engine.wasm` is BSL-1.1, so artifacts embedding it carry BSL terms for that combined work. |
| `packages/shared/contracts/` | MIT | Public wire schemas and ecosystem contracts. |
| `shared/provider-catalog/` | MIT | Public provider/model metadata and catalog schemas. |
| `mem/js/` | MIT | Thin JavaScript client for cavemem. |
| `mem/py/` | MIT | Thin Python client for cavemem. |
| `ui/` | MIT | In-tree utility package; `package.json` disables registry publication. |
| `engine/` | BSL-1.1 | Core compression IP and CCR. |
| `cacheengine/` | BSL-1.1 | Provider-native prompt-cache planner and wire engine. |
| `rewriter/` | BSL-1.1 | Engine-linked reflection rewriter and recovery gates. |
| `browse/` | BSL-1.1 | Local browser driver; embeds the engine, vendors MIT chromedp modules. |
| `proxy/` | BSL-1.1 | Standalone gateway and provider adapters. |
| `mcp/` | BSL-1.1 | Go binary embeds the engine. |
| `shrink/` | BSL-1.1 | Go binary/package embeds the engine tool-schema compressor. |
| `mem/` Go core | BSL-1.1 | Go core embeds the engine; `mem/js` and `mem/py` remain MIT clients. |
| `shared/platform/` | BSL-1.1 | Statically linked into BSL Go binaries. |

Paths are relative to repository root. Skill's own benchmark harness lives at
`evals/` and is MIT alongside skill.

## Additional Use Grant

The BSL grant permits internal evaluation, local development, CI testing,
integration, and self-hosted use for your own first-party traffic, including
production.

Offering Caveman, the Licensed Work, or the Licensed Work's functionality to
third parties as a hosted, managed, or embedded service requires a commercial
license from the Licensor. This is the OEM/platform boundary.

Named commercial/OEM partners may receive a separate signed carve-out that
allows the specific hosted, managed, or embedded use covered by that agreement.

## Change License

BSL-licensed versions convert to Apache License, Version 2.0 on the earlier of:

- `2030-06-21`
- the fourth anniversary of that version's first public distribution under BSL

## Commercial Boundary

Free/source-available:

- local and single-tenant use
- BYOK/self-hosted first-party traffic
- inferred savings
- SDKs, CLI, kit, extension shell, contracts, provider catalog

Commercial:

- third-party hosted/managed/embedded optimization service
- verified savings across an org
- multi-tenant control plane, SSO/RBAC/RLS, governance, audit
- signed metering receipts, gainshare billing, Enterprise/OEM licenses

## Contributions

MIT areas use inbound=outbound MIT.

BSL areas require DCO sign-off and a relicense grant to Julius Brussee so
commercial and OEM licenses can include community contributions. The public repo
`CONTRIBUTING.md` must document both requirements before accepting external
contributions to BSL-covered code.

## Third-party code

`engine/pixel/` is a Go port of pxpipe (MIT, Copyright (c) 2026
claude-image-proxy contributors) with embedded glyph atlases derived from the
Spleen 5x8 (BSD-2-Clause) and GNU Unifont (OFL-1.1 / GPLv2 with font-embedding
exception) fonts. BSL-1.1 applies to the combined work; the upstream MIT and
font notices are preserved in `engine/pixel/NOTICE` and
`engine/pixel/assets/`.

`browse/` vendors MIT-licensed chromedp modules; see `browse/NOTICE`.

## Trademarks

"Caveman" and Caveman logos are trademarks of Julius Brussee. Code licenses do
not grant trademark rights. Nominative use such as "Powered by Caveman" or
"Optimized by Caveman" is allowed when truthful. Naming a product, hosted
service, or fork in a way that implies Caveman sponsorship requires written
permission.

See `TRADEMARKS.md` for the full trademark policy.
