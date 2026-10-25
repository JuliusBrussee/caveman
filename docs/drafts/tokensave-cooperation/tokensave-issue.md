# Title: Document optional Caveman coexistence and compatibility fixtures

## Problem

Users combining TokenSave semantic code intelligence with Caveman need a clear
ownership boundary and reproducible evidence that semantic payloads and tool
selection guidance survive Caveman's supported transformations.

## Proposed scope

Existing tracking issue: [#797](https://github.com/aovestdipaperino/tokensave/issues/797);
existing draft implementation: [PR #800](https://github.com/aovestdipaperino/tokensave/pull/800).
This local issue draft is superseded by that tracking issue; do not publish a duplicate.

Reuse the existing read-only discovery, diagnostics, preferences, and conditional
MCP instructions. Make cooperation opt-in to meet #797 and add readiness/error
guardrails. Public MCP tool contracts and semantic results remain unchanged.
Caveman implements its own preservation policy.

- Pin TokenSave and Caveman source revisions and document existing instructions,
  tool names, schemas, result shapes, and the supported way to establish indexing
  readiness. Investigate these facts rather than inventing a readiness API.
- Document independent installation and management. Presence of a binary,
  configuration, or skill file does not prove active session availability.
- Explain conditional semantic-query preference, stopping redundant exploration
  when evidence is sufficient, and honoring explicit user instructions.
- Add sanitized, representative fixtures covering existing response forms:
  JSON, code, identifiers, paths, signatures, errors, numbers, and relationships.
  Include selection and argument guidance carried in descriptions.
- Document observed agent naming formats and the limits of MCP server identity
  visibility at the model proxy. Do not promise that TokenSave can set model
  request headers or enforce Caveman preservation.
- Provide a reproducible A/B procedure and a results template that separates
  correctness, task success, latency, calls, reads, token usage, and billing.

## Acceptance criteria

- [ ] References and fixtures match pinned source and documented public contracts.
- [ ] No TokenSave dependency on Caveman, automatic installation, configuration
      rewriting, endpoint changes, model changes, or contract changes.
- [ ] Coexistence documentation states which guarantees belong to Caveman and
      links to its tested support matrix once available.
- [ ] Absent/unavailable tools, missing index readiness, removal, and unsupported
      protocols are described without implying active support.
- [ ] Reproducible verification instructions use real implemented commands.
- [ ] Measurements use actual telemetry and independent task verification;
      unavailable billing or instrumentation is listed explicitly.

Caveman work does not depend on these TokenSave changes. Runtime work here is
limited to the already drafted cooperation surface; do not change retrieval
formatting, proxy configuration, routing, or public MCP contracts.

Companion: proposed Caveman cooperation issue; link after publication. Link
TokenSave updates with `Refs #797`; issue closure belongs to its maintainer.
