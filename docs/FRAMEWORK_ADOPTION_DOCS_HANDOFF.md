# Framework adoption: documentation and website handoff

Status: implementation brief, not a claim that the website changes have shipped.
Audit date: 2026-09-16. Recheck live routes and published versions before release.

## Assignment

Make Caveman's Python and TypeScript integrations independently adoptable by a
developer who has never used Caveman. They must be able to choose an integration,
install it, run a complete example, see whether optimization happened, deploy it,
and diagnose failures without reading implementation source or asking a maintainer.

Work in `caveman-docs` for documentation and `caveman-site` for marketing redirects
and navigation. Coordinate public package README/metadata updates with the agent
working in `caveman`. Preserve all existing edits in every checkout. The docs
checkout already contains substantial modified and untracked work, including new
SDK pages; inspect and finish that work instead of replacing it.

Read each repository's instructions first. In `caveman-docs`, the canonical
instructions also require its referenced documentation authoring skill. Follow
the public-content gate. Do not publish private implementation details or promote
planned capabilities to shipped features. These middleware packages are separate
from the Agent SDK; do not use an old in-tree Agent SDK copy as authority.

## 1. Repair discovery and old URLs first

The canonical site is `https://docs.caveman.so`. Do not build a second docs site on
the marketing origin.

Observed HTTP responses on the audit date:

| URL | Result | Required action |
| --- | --- | --- |
| `https://caveman.so/docs` | 404 | Permanent redirect to the canonical docs entry page |
| `https://caveman.so/docs/sdk/typescript` | 404 | Permanent redirect to `https://docs.caveman.so/docs/sdk/typescript` |
| `https://caveman.so/docs/sdk/python` | 404 | Permanent redirect to `https://docs.caveman.so/docs/sdk/python` |
| `https://docs.caveman.so/` | 200 | Keep as the canonical docs homepage |
| `https://docs.caveman.so/docs/sdk/typescript` | 200 | Update the existing page; do not duplicate it |
| `https://docs.caveman.so/docs/sdk/python` | 200 | Update the existing page; do not duplicate it |
| `https://docs.caveman.so/docs/sdk` | 404 | Finish, validate, and deploy the existing local SDK overview |

Files to inspect:

- `caveman-site/next.config.ts`: add `/docs` and `/docs/:path*` redirects, with
  deliberate handling of the docs root and Markdown URLs. Preserve query strings
  and avoid redirect loops.
- `caveman-site/lib/web-surfaces.ts`: reuse its existing `docsHref` helper.
- Marketing navigation, product cards, footer, SDK calls to action, search results,
  and machine-readable indexes: point to canonical pages that exist.
- `caveman/packages/sdk/typescript/{README.md,package.json}` and
  `caveman/packages/sdk/python/{README.md,pyproject.toml}`: replace obsolete
  marketing-origin SDK links.
- `caveman/packages/middleware/typescript/package.json`: give middleware a
  middleware-specific canonical homepage after that page is available.
- Registry pages reflect published metadata. Source edits alone do not repair old
  npm/PyPI releases; redirects must keep already-published links useful.

Acceptance: old URLs redirect to the correct content, final pages return 200,
canonical metadata uses the docs origin, and no successful-looking soft 404s exist.

## 2. Finish the existing SDK section before adding another information hierarchy

The docs checkout already contains these source pages, some untracked or not yet
deployed. Review them against current public code and published packages:

- `app/docs/sdk/page.mdx`
- `app/docs/sdk/typescript/page.mdx`
- `app/docs/sdk/python/page.mdx`
- `app/docs/sdk/configuration/page.mdx`
- `app/docs/sdk/providers/page.mdx`
- `app/docs/sdk/compression/page.mdx`
- `app/docs/sdk/tools/page.mdx`
- `app/docs/sdk/middleware/page.mdx`
- `app/docs/sdk/reference/page.mdx`
- `app/docs/sdk/context/page.mdx`
- `app/docs/sdk/tracing/page.mdx`
- `app/docs/sdk/policy/page.mdx`
- `app/docs/sdk/troubleshooting/page.mdx`

`lib/docs-nav.ts` is the source of truth for navigation and indexes. Every page
must have the proper nav entry, public capability, license, metadata, breadcrumbs,
and Markdown mirror. Preserve the site's existing reading layout and components.

The overview must answer this decision in plain language:

| Developer goal | Integration path |
| --- | --- |
| Shorter assistant answers | Caveman skill |
| Put an existing provider client behind Caveman | Proxy/base-URL integration |
| Compress eligible tool results inside an existing framework | Native middleware |
| Call explicit compression, context, tool-search, or tracing APIs | Thin SDK, with endpoint requirements stated per operation |
| Build a new agent runtime | Separate Agent SDK documentation |

Explain which optimizations are automatic, which require explicit calls, which
require recovery, and which are outside middleware scope. Separate local,
accountless middleware from connected gateway operations and their credentials.
Warn against stacking adapters/proxies without a documented ownership rule.

## 3. Ship complete TypeScript and Python middleware quickstarts

Create dedicated middleware quickstarts linked from `sdk/middleware`; suggested
slugs are `sdk/middleware/typescript` and `sdk/middleware/python`. Keep the thin SDK
quickstarts distinct rather than silently changing their purpose.

Use Vercel AI SDK for the primary TypeScript example and LangChain for Python.
Each quickstart must include:

1. Exact supported language/runtime and framework versions.
2. A fresh directory, package/environment setup, and all necessary dependencies.
3. Runtime installation, startup, and an explicit capability/preflight check.
4. Full executable source with a model, tools, messages, stopping conditions,
   runtime lifecycle, and a large deterministic tool output that is eligible.
5. Environment variables with clear credential ownership. No real credentials.
6. A safe initial observation run, then an explicit compression run.
7. Expected reports and an explanation of `applied`, `reused`, `skipped`,
   `recorded`, and `disabled`.
8. One example showing original recovery and unchanged application history.
9. Shutdown, removal, and rollback instructions.
10. A deterministic local validation command and a separate optional provider run,
    clearly identifying which one incurs provider charges.

Do not use undefined `existingOptions`, `existing_agent_options`, unspecified
services, or missing tool implementations in the primary quickstart. Integration
snippets may use those placeholders only after a complete example exists.

The current strict `ready()` operation can raise before the model is called.
Describe that contract accurately. A nonthrowing `preflight()` API is being added
in the code workstream; document it only after verifying its final export, result
fields, tests, and released version. Do not promise that a new source API already
exists in published 1.1.0 packages.

Explain where to obtain namespace/session identity, how long a runtime instance
lives, and when branch/cache epoch changes are needed. Keep advanced scope theory
out of the first three steps, but never encourage one shared scope for all users.

## 4. Add framework-specific guides and an honest compatibility matrix

Cover every advertised family. A guide can contain language tabs where both exist:

- TypeScript: AI SDK, OpenAI, Anthropic, Google, LangChain, Strands, Mastra, MCP.
- Python: OpenAI, Anthropic, Google, LangChain, LiteLLM, Strands, Agno, CrewAI,
  AutoGen, Pydantic AI, LlamaIndex, ASGI, MCP.

For each, show installation, the exact public entrypoint, a minimal complete
integration, sync/async support, streaming behavior, tool-loop/recovery ownership,
custom transport handling, lifecycle, unsupported operations, and a reproduction
command. Existing loops and stored messages remain owned by the application.

The compatibility matrix must separate:

- dependency resolver range;
- adapter's accepted version range;
- exact versions actually tested;
- supported protocols and operations;
- runtime/platform/bundling restrictions;
- alpha/experimental status and known gaps.

Never call an entire accepted semver range tested because one version passed.
List incompatible Python extra combinations and supported isolation strategies.
State ESM/Node requirements; do not imply browser/edge support from TypeScript
alone. The code agents are changing compatibility checks, so regenerate the matrix
from final manifests and gates rather than copying the earlier audit verbatim.

## 5. Document deployment and the real data path

Add `sdk/middleware/deployment` or an equivalent clearly linked section. Reuse
existing public proxy deployment material instead of inventing another deployment
system. The public repository already has `docs/technical/deploy.md`, a Dockerfile,
and deployment manifests.

Explain the difference between:

- native middleware calling a runtime for optimization while inference stays with
  the application's provider client; and
- a proxy/base-URL integration that forwards inference traffic itself.

Required examples and operational contracts:

- Local development, one application plus runtime container, and production Node
  or Python service lifecycle.
- Loopback versus remote endpoint configuration, runtime authentication versus
  provider credentials, TLS, and explicit remote-content consent/configuration.
- Readiness, liveness, graceful shutdown, optimizer deadline, recovery deadline,
  concurrency/capacity bypass, and the behavior when runtime is unavailable.
- Storage location, durable volumes, permissions, restart behavior, retention,
  deletion, and the exact middleware scope expiry policy.
- Multiple workers/instances: session routing and recovery reachability. Do not
  claim stateless horizontal scaling or shared SQLite safety.
- Serverless/bundled deployment constraints. State unsupported configurations.
- Pinned package/runtime versions, upgrade compatibility, staged rollout, and
  switching off or rolling back without losing original history.

Do not copy proxy-wide mode defaults into middleware documentation without checking
them. Their defaults and request paths are not automatically identical.

## 6. Make recovery and troubleshooting usable

Extend `sdk/troubleshooting` and `sdk/context`, and link directly from examples.
Add a symptom/reason/action table for missing runtime, authentication failures,
unsupported versions/shapes, no eligible candidates, deadline, capacity, circuit
open, missing recovery executor, expired or mismatched scope, and mode disabled.
Use actual current reason codes; distinguish installation failures from safe skips.

Explain `ready()` versus preflight versus inference, sync/async mismatches, and
why successful inference alone does not prove optimization happened. Show one
sanitized diagnostic output and a minimal issue template with versions and a
reproducer, without original prompts or credentials.

Recovery documentation must cover exact originals, pagination, excerpt queries,
expiry, restart, namespace/session/branch/cache-epoch isolation, and keeping
original stored history. Distinguish middleware recovery handles from connected
SDK checkpoints and artifacts. Availability of originals does not guarantee that
the model will request them or preserve task quality.

## 7. Show how to measure value without overstating it

Connect `sdk/compression`, `sdk/tracing`, and the existing measurement vocabulary.
Provide a reproducible off/record/compress evaluation recipe using identical tasks,
models, tools, and success criteria. Include warm/cold cache conditions, retries,
recovery calls, input/output tokens, latency, and quality checks.

Distinguish decision reports, estimated segment tokens, provider usage, pricing
estimates, and actual billed/verified savings. Current `CallReport` does not itself
contain token counters; correct the local middleware page's statement that reports
contain estimated segment-token counts. Show the real source for each metric.

A local deterministic fixture proves mechanism behavior, not provider acceptance,
production quality, or billing savings. Do not invent a universal savings number,
equate compressibility with task success, or call alpha integrations production
certified. Disclose unavailable metrics rather than filling them with estimates.

## 8. Clarify licensing and release maturity

Link the authoritative public license files. Explain MIT SDK/adapters versus the
Engine runtime's BSL terms, first-party self-hosted use, and the separate commercial
path for relevant hosted/managed/embedded offerings. Do not apply the Agent SDK's
license to these packages or invent legal interpretations.

State published package versions, runtime compatibility, alpha status, support
policy, where to file issues, changelog location, and known limitations. Source
improvements pending publication must be clearly marked or withheld from the
default released-package quickstarts.

## 9. Keep human and machine documentation synchronized

For every changed/new page, verify HTML, `/docs/<slug>.md`, navigation, pager,
search, `/llms.txt`, `/llms-full.txt`, `/llms-index.json`, sitemap, canonical URL,
anchors, and structured metadata. Preserve existing Accept-header Markdown
negotiation; do not serve different substantive claims to bots and people.

Check mobile reading/copy behavior, keyboard navigation, focus visibility, code
overflow, direct anchor links, and readable language labels. Prioritize working
instructions over a visual redesign.

## 10. Completion gates and delivery

- All primary examples run from a clean environment using the documented release.
- At least one complete example per primary language demonstrates a real runtime
  transform and original recovery, with fixture/provider evidence labeled.
- Required framework compatibility tests cannot silently skip broken imports.
- Docs `npm run check:public`, `npm run lint`, `npm test`, and `npm run build` pass,
  or exact pre-existing failures are identified without claiming a clean gate.
- The marketing site's relevant redirect/link tests and build pass.
- A rendered browser pass validates navigation and copyable examples.
- An HTTP audit checks old and canonical URLs after deployment. A local build is
  not proof that published 404s are fixed.
- Existing dirty work is preserved; final diff and source-versus-published status
  are explicit. Follow the user's deployment authorization and repository rules.

Deliver the implemented pages, corrected links and redirects, a short tested
version matrix, commands/results, remaining unsupported paths, and the exact
deployment status. Do not finish with only a content outline.
