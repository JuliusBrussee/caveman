# Caveman documentation

How the public Caveman repository works, from install to runtime. Hosted-service
internals are out of scope. Why it is built this way: [WHY.md](./WHY.md).

## Start here

- [Product model](./technical/product-model.md): what each Caveman layer does
- [Architecture](./technical/architecture.md): request path, local processes,
  storage, and failure behavior
- [Install and update](./technical/install-and-update.md): skill-only and local
  runtime installs; [Windows fallback](./install-windows.md)
- [CLI reference](./technical/cli-reference.md): command groups and output
  contracts
- [Configuration](./technical/configuration.md): capability keys, local files,
  environment variables, and precedence

## Runtime

- [Agent wrapping](./technical/agent-wrapping.md): supported agents, routing,
  recovery requirements, and profile format
- [Agent-profile registry](./technical/agent-profile-registry.md): profile
  fields, injection tiers, and the profile compiler
- [Compression Engine](./technical/engine.md): detection, compressors, safety
  classes, token counts, and pass-through rules
- [Context recovery](./technical/context-recovery.md): CCR handles, storage,
  retrieval, and retention limits
- [Local proxy and providers](./technical/proxy-and-providers.md): routes,
  credentials, reliability, usage parsing, and network boundaries
- [Provider authentication](./technical/provider-authentication.md): which
  credential the proxy forwards, per provider
- [Deploy the proxy for a team](./technical/deploy.md): shared VPC and cloud
  deployment, inbound token, containers, and manifests
- [TOON and Pixel](./technical/toon-and-pixel.md): structured-data and image
  transforms
- [Cache planner](./technical/cache-planner.md): provider prompt caching
- [Local stats](./technical/stats-accounting.md): what `caveman stats` counts
  and how savings are priced
- Framework middleware: [TypeScript](../packages/middleware/typescript/README.md)
  and [Python](../packages/middleware/python/README.md) adapters, and the
  normative [middleware protocol](./technical/middleware-protocol.md)

## Agent-facing tools

- [Skills, hooks, and plugins](./technical/skills-hooks-and-plugins.md): modes,
  plugin layout, Claude Code hooks, the installer, and how each agent gets
  caveman
- [Memory, MCP, browser, and shrink](./technical/local-tools.md): durable memory,
  recovery tools, accessibility snapshots, command output, and tool catalogs
- [Exploration and delegation](./technical/exploration-and-delegation.md):
  read-only repository search, compact return contracts, and delegation limits

## Building on Caveman

- [SDKs and packages](./technical/sdks-and-packages.md): TypeScript and Python
  SDKs, shared contracts and schemas, provider catalog, and benchmark tooling
- [caveman learn](./technical/learn.md): local setup profiler, Setup Score,
  sinks, autopilot, and consent-gated fixes
- [Accounting and evidence](./technical/accounting-and-evidence.md): `inferred`,
  provider-reported, benchmark, and `verified` labels
- [Security and privacy](./technical/security-and-privacy.md): data flows and
  secrets, local files, SSRF controls, binary verification
- [Testing and benchmarks](./technical/testing-and-benchmarks.md): local gates,
  evals, benchmark rules, and reproducibility
- [Extending Caveman](./technical/extending.md): agent profiles, compressors,
  providers, schemas, and prices
- [Glossary](./technical/glossary.md): every Caveman-specific term used here

## Published numbers

- [Honest numbers](./HONEST-NUMBERS.md): what the voice saves and where it loses
- [Wrap benchmark](./WRAP-BENCHMARK.md): input tokens with and without the
  runtime, six tool-output workloads
- [Auto benchmark](./AUTO-BENCHMARK.md): model routing against Opus at xhigh

## Contributing and releasing

- [Contributing agent profiles](./CONTRIBUTING_PROFILES.md)
- [Package releases](./PACKAGE_RELEASES.md): tags, publish lanes, signing, and
  release order
- [Release notes](./releases/)

Package READMEs stay the narrow API references.
