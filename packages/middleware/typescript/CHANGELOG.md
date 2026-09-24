# Changelog

`@caveman-ai/middleware` is pre-1.0. Anything can change between alphas; read
this file before upgrading. Prereleases publish under the `alpha` npm dist-tag,
never `latest`. Support policy: [SECURITY.md](../../../SECURITY.md#supported-versions).

## Unreleased

- Release process: prereleases no longer take the `latest` dist-tag, each
  release gets a GitHub Release with these notes and a CycloneDX SBOM, and the
  published dependency graph is audited before publish.

## 0.1.0-alpha.2 — 2026-09-15

- Dropped the optional framework peer declarations. npm resolved them anyway,
  so a plain `npm install @caveman-ai/middleware` failed with ERESOLVE before
  any adapter was chosen. The runtime version gate still reports an
  unsupported framework.

## 0.1.0-alpha.1 — 2026-09-15

- First alpha of the native framework adapters. Superseded: it does not
  install cleanly with npm (see 0.1.0-alpha.2).
