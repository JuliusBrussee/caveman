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
| `packages/middleware/` | MIT | Framework middleware adapters (`@caveman-ai/middleware`, `caveman-middleware`). Call the BSL runtime over HTTP; contain no Engine code. |
| `packages/subagent-tax/` | MIT | Local zero-provider-call harness-prefix measurement tool. |
| `extension/` | MIT shell | Manifest, popup, content scripts, and UI are MIT. Bundled `engine.wasm` is BSL-1.1, so artifacts embedding it carry BSL terms for that combined work. |
| `packages/shared/contracts/` | MIT | Public wire schemas and ecosystem contracts. |
| `shared/provider-catalog/` | MIT | Public provider/model metadata and catalog schemas. |
| `mem/js/` | MIT | Thin JavaScript client for cavemem. |
| `mem/py/` | MIT | Thin Python client for cavemem. |
| `engine/` | BSL-1.1 | Core compression IP and CCR. |
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

## Middleware licensing

Framework middleware has two halves under two licenses:

- **Client and adapters: MIT.** `packages/middleware/` and the `middleware`
  modules of the SDKs (`@caveman-ai/sdk/middleware`, `caveman_cloud.middleware`).
  Use, modify, and ship them inside any product, open or closed.
- **Runtime: BSL-1.1.** The adapters do nothing on their own; they call the
  `caveman-proxy` runtime (binary or container image), which embeds the Engine.
  Running that runtime is use of the Licensed Work under `LICENSE.BSL` and its
  Additional Use Grant. The MIT license of the client does not extend to it.

So the licensing question for a deployment is always about the runtime: who
runs it, and whose traffic it serves.

## Common deployment topologies

Answers below come only from the current text of `LICENSE.BSL`. Where that text
does not settle the case, the row says so; ask the Licensor before relying on
it. This table is guidance, not a license grant, and `LICENSE.BSL` wins if they
ever disagree.

| Topology | Runtime use under the current BSL text |
|---|---|
| Single application: you self-host the runtime for your own app and your own traffic | Permitted. Self-hosted production use for your own first-party traffic is in the Additional Use Grant. |
| Internal platform: one team runs the runtime for other business units or affiliates | PENDING LICENSOR DECISION. The grant does not define "first-party" for affiliates or other entities in a group. |
| Contractors operating the runtime on your behalf, for your traffic | PENDING LICENSOR DECISION. The grant does not say whether a contractor's operation counts as your first-party use. |
| ISV SaaS: you use Caveman inside your own service to serve your customers | PENDING LICENSOR DECISION. Where "your own first-party traffic" ends and "offering its functionality to third parties" begins is not defined for this case. |
| On-prem bundle: you ship the runtime inside a product your customers install | PENDING LICENSOR DECISION. The grant excludes offering the Licensed Work "as a hosted, managed, or embedded service" but does not address distributing it inside an installed product. |
| Hosted or managed offering: third parties use Caveman, or its functionality, through your service | Commercial license required. The grant expressly excludes offering the Licensed Work or its functionality to third parties as a hosted, managed, or embedded service. |

## Change License

BSL-licensed versions convert to Apache License, Version 2.0 on the earlier of:

- `2030-06-21`
- the fourth anniversary of that version's first public distribution under BSL

## Commercial Boundary

Free/source-available:

- local and single-tenant use
- BYOK/self-hosted first-party traffic
- inferred savings
- SDKs, CLI, extension shell, contracts, provider catalog

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
