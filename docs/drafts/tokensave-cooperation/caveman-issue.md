# Title: Support safe, optional cooperation with TokenSave

## Problem

When both projects are used, Caveman instructions can overlap with semantic code
research, while input and catalog compression may alter information TokenSave
provides. Users need optional cooperation that preserves exact tool results and
keeps installation, session availability, and activation distinct.

## Proposed scope

1. Pin both upstream revisions. Inspect Caveman skills, behavior modifiers,
   Cavecrew, agent integrations, compression entrypoints, catalog shrinking, and
   routing alongside TokenSave instructions, schemas, responses, and indexing
   readiness. Record supported interfaces and gaps before changing behavior.
2. Update Caveman-owned instructions to prefer targeted semantic queries when
   appropriate tools are available in the session and the project is indexed.
   Avoid redundant broad exploration or Cavecrew research after sufficient
   evidence. Explicit user instructions take precedence. Preserve exact code,
   identifiers, paths, signatures, errors, numbers, and semantic relationships.
3. Add a configurable general tool-result preservation policy using existing
   configuration and CLI conventions. Resolve identity from protocol tool calls
   and results, including verified agent naming formats. Never match result prose
   or infer server identity from an ambiguous name.
4. Apply preservation before every applicable lossy transformation, including
   downstream compression or elision paths discovered during the audit. Define
   exact content preservation separately from transport JSON serialization.
   Recovery handles alone do not satisfy this requirement.
5. Audit catalog shrinking separately. Provide description preservation where
   selection or argument guidance could be lost. Preserve names, schemas,
   parameters, constraints, and defaults; structural equality alone does not
   establish equivalent tool selection.
6. Report enabled mechanisms, protected identities, unsupported protocols,
   ambiguous identities, invalid settings, and conflicts without logging payloads.
   Document conservative behavior when identity cannot be established; never
   claim protection for such cases without a tested safe fallback.

## Acceptance criteria

- [ ] Source-pinned interface matrix distinguishes verified support from gaps.
- [ ] TokenSave absent, unavailable, unindexed, and removed retain normal behavior.
- [ ] Available indexed tools receive suitable targeted queries; unrelated
      delegation, models, providers, and routing remain unchanged.
- [ ] Interleaved tool calls resolve to the correct results for each supported
      protocol; ambiguous, malformed, and unsupported cases have diagnostics.
- [ ] Mixed requests preserve protected content exactly while unrelated content
      still receives normal compression. Test every applicable lossy path.
- [ ] Fixtures include JSON, source snippets, identifiers, paths, signatures,
      errors, numbers, and semantic relationships, including nested content.
- [ ] Catalog tests cover preserved descriptions and unchanged schemas,
      constraints, defaults, and tool names.
- [ ] Configuration examples use implemented options, with reproducible commands
      and relevant passing tests, lint, and formatting checks recorded.
- [ ] Correctness evidence is separate from reproducible A/B measurements:
      TokenSave alone versus TokenSave plus Caveman, using input/output tokens,
      MCP calls, redundant reads, independently verified task success, latency,
      and cost per successful task. Missing telemetry or billing is explicit.

## Boundaries

No automatic TokenSave installation, mandatory dependency, TokenSave-owned
configuration edits, extra proxy, endpoint replacement, model/provider switching,
or TokenSave public MCP contract changes. TokenSave cannot be assumed to set
headers on an agent's model requests. Begin with the smallest verified protocol
and expand only with evidence. No synthetic savings or unsupported percentages.

Published tracking issue: https://github.com/JuliusBrussee/caveman/issues/1403

Companion: TokenSave issue #797 and draft PR #800.
