# Title: feat: preserve configured semantic tool results and catalogs

## Problem and behavior

Tool-result compression can elide semantic information already retrieved by
TokenSave. Configured tools now bypass adapter rewrite candidates, including
cached substitutions, while unrelated results retain ordinary compression.
Caveman also prefers suitable semantic queries when session tools and an indexed
project are available, and reuses sufficient findings before repeating research.

## Changes

- Add `preserve_tools` to existing proxy YAML and the JSON-array environment
  override `CAVEMAN_PRESERVE_TOOLS`. Validate exact-name and final-prefix wildcard
  rules. Empty arrays disable the policy; malformed rules refuse startup.
- Correlate Anthropic, OpenAI Chat, and OpenAI Responses/custom-tool results using
  protocol call IDs. Gemini uses the response's protocol tool name. Configured
  results and unresolved identities bypass compression and replacement lookup.
- Report configured rules and per-request matched names, protected/unresolved
  counts, ambiguity, and unsupported protocols without payloads.
- Add catalog-shrinker library/CLI policy preserving complete matching MCP/OpenAI
  tool envelopes, descriptions, and schemas. Lint applies the same policy.
- Disable pixel rendering and proxy catalog annotation stripping while policy is
  configured; these paths lack the general identity-aware preservation mechanism.
- Update voice/Cavecrew/investigation instructions and regenerate delivery files
  from canonical sources. Document configuration, scope, and verification.

Example:

```yaml
preserve_tools: ['mcp__tokensave__*', 'tokensave_*']
```

Tools are not automatically discovered or installed. The integration does not
change endpoints, models, providers, unrelated delegation, or MCP contracts.

## Validation

Passed Go tests and vet across the policy, all provider adapters, compressor,
shrinker, config, gateway, and standalone packages. Tests include interleaving,
mixed payloads, both bare and qualified names, custom calls, malformed rules,
exact serialized payload preservation, and a cached-elision regression.

Registry compilation, rule-copy tests, full local repository verification,
formatting, and whitespace checks passed. The repository verification included
regenerated plugin mirrors and the skill archive. See the
[consolidated validation record](https://github.com/atontini/caveman/blob/feat/tokensave-cooperation/docs/drafts/tokensave-cooperation/README.md) for commands and related PR status.

## Limits

Policy applies to supported model protocol envelopes, not arbitrary raw Engine
inputs. Unknown naming formats require explicit rules and verification. Start a
fresh session after changing policy. Pixel mode conservatively passes through;
Gemini catalog shrinking is unsupported and currently leaves its envelope alone.

No live A/B performance trials were run. TokenSave's existing benchmark runner
supplies a protocol, but actual telemetry, independently scored agent tasks, and
billing remain pending. Recovery is not unchanged delivery, and schema equality
does not prove equivalent tool selection. This PR is prepared for draft review and remains unmerged.

Refs #1403

Companion: https://github.com/aovestdipaperino/tokensave/pull/800 (Refs its issue #797).
