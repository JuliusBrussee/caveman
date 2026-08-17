# Caveman technical documentation

This manual explains the public Caveman repository from installation through
runtime behavior. It covers the files and interfaces that users can inspect in
this repository. Hosted-service implementation details are outside its scope.

## Start here

- [Product model](./technical/product-model.md): what each Caveman layer does
- [Architecture](./technical/architecture.md): request path, local processes,
  storage, and failure behavior
- [Install and update](./technical/install-and-update.md): skill-only and local
  runtime installs
- [CLI reference](./technical/cli-reference.md): command groups and output
  contracts
- [Configuration](./technical/configuration.md): capability keys, local files,
  environment variables, and precedence

## Runtime

- [Agent wrapping](./technical/agent-wrapping.md): supported agents, routing,
  recovery requirements, and profile format
- [Compression Engine](./technical/engine.md): detection, compressors, safety
  classes, token counts, and pass-through rules
- [Context recovery](./technical/context-recovery.md): CCR handles, storage,
  retrieval, and retention limits
- [Local proxy and providers](./technical/proxy-and-providers.md): routes,
  credentials, usage parsing, and network boundaries
- [TOON and Pixel](./technical/toon-and-pixel.md): structured-data and image
  transforms
- [Cache planner and rewriter](./technical/cache-and-rewriter.md): provider
  prompt caching and gated trajectory rewriting

## Agent-facing tools

- [Skills, hooks, and plugins](./technical/skills-hooks-and-plugins.md): response
  style, Claude Code hooks, opencode integration, extension behavior, and
  compressed subagents
- [Memory, MCP, browser, and shrink](./technical/local-tools.md): durable memory,
  five recovery tools, accessibility snapshots, command output, and tool
  catalogs
- [Exploration and delegation](./technical/exploration-and-delegation.md):
  read-only repository search, compact return contracts, and delegation limits

## Building on Caveman

- [SDKs and packages](./technical/sdks-and-packages.md): TypeScript, Python,
  Agent SDK, schemas, graders, React kit, Mastra, and provider catalog
- [Accounting and evidence](./technical/accounting-and-evidence.md): `inferred`,
  provider-reported, benchmark, and `verified` labels
- [Security and privacy](./technical/security-and-privacy.md): data flows and
  secrets; local files; SSRF controls; binary verification
- [Testing and benchmarks](./technical/testing-and-benchmarks.md): local gates,
  benchmark boundaries, and reproducibility
- [Extending Caveman](./technical/extending.md): agent profiles, compressors,
  providers, schemas, and prices
- [Glossary](./technical/glossary.md): every Caveman-specific term used by this
  manual

Package READMEs remain the narrow API references. This manual supplies the
cross-component model and links to those references where details belong.
