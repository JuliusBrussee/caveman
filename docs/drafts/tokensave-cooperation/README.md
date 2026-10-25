# Caveman–TokenSave cooperation: implementation and publication record

Both changes are prepared for draft review. Caveman tracking issue:
[#1403](https://github.com/JuliusBrussee/caveman/issues/1403). The existing TokenSave
draft is [#800](https://github.com/aovestdipaperino/tokensave/pull/800), Refs #797.
Caveman draft PR publication is in progress. Neither change is merged or released.
The existing TokenSave compatibility implementation is retained; the follow-up
makes cooperation opt-in and clarifies readiness and exact-payload preservation.

| Repository | Issue | PR | Implementation | Dependency |
| --- | --- | --- | --- | --- |
| Caveman | [#1403](https://github.com/JuliusBrussee/caveman/issues/1403) | [Description](caveman-pr.md), draft publication in progress | Configurable tool-result and catalog preservation, diagnostics, conditional instructions, regression tests | Independent of TokenSave runtime changes |
| TokenSave | [Existing #797](https://github.com/aovestdipaperino/tokensave/issues/797), [local draft](tokensave-issue.md) | [Existing draft #800](https://github.com/aovestdipaperino/tokensave/pull/800), [local follow-up description](tokensave-pr.md) | Existing read-only discovery/diagnostics and MCP cooperation; follow-up makes cooperation opt-in and clarifies indexing readiness and error preservation | Companion documentation must label Caveman controls unreleased until its PR ships |

Sources checked before implementation:

- Caveman upstream HEAD and local base:
  `9b68f7ed12b05e7efaff18e65c5a3fc2849fd0a4`.
- TokenSave upstream master: `ed7ae0f49d4ee4442fceb00770c657789dffdc3e`.
- Existing local TokenSave `feat/caveman-compatibility` base:
  `b45e77557c11f8b19334dd542c0d695ae293f9ba`.
- TokenSave master `AGENTS.md` was fetched and compared. The branch retains only
  its intentional additional compatibility guidance; shared instructions match.

The TokenSave follow-up was prepared and checked in
`/tmp/tokensave-cooperation-pr`, then applied to the existing local TokenSave
checkout. Untracked package files and node_modules were left untouched. Its
existing runtime implementation was reused, not replaced or recreated.

## Supported behavior and limits

See [implemented configuration and protocol support](../../technical/tokensave-cooperation.md).
Results are associated by protocol identity, never prose. Matching results bypass
new compression and stored replacements. Unresolved identities stay raw while
rules are enabled. Catalog preservation retains complete matching MCP/OpenAI tool
envelopes and schemas. Pixel mode passes through and proxy annotation stripping
is disabled when preservation is configured.

There is no automatic installation, mandatory dependency, extra proxy, endpoint
replacement, provider/model switch, or MCP contract change. Raw Engine calls have
no MCP identity and are outside the policy. Renamed server/tool formats need
explicit matching rules. Changing rules requires restarting the proxy and using
a fresh session; an existing compressed history can recache raw.

## Validation from this turn

Passed:

- Go tests across `shared/toolpolicy`, all provider adapters, `engine/compressors`,
  `shrink/...`, proxy config, gateway, and standalone wiring.
- `go vet` on the same package set.
- `node packages/cli/scripts/compile-registries.mjs`.
- `node --test tests/installer/rule-copies.test.mjs`.
- `python3 tests/verify_repo.py`, after regenerating distribution mirrors and the
  skill ZIP from canonical sources using the sync workflow's source mapping.
- TokenSave focused lite integration run: 12 tests passed, including actual
  semantic-result equality and the new readiness/error instruction contract.
- TokenSave `cargo fmt --all -- --check` and three Python benchmark-accounting
  tests. Existing lite dead-code and unused-import warnings remain.
- `git diff --check` in both working copies.

The opt-in default correction passed all 49 TokenSave user-configuration tests,
all 12 focused compatibility tests, and Clippy. No warnings were reported in the
changed cooperation code; unrelated existing warnings remain.
The Go tests required `umask 077` for SQLite parent permissions and loopback
access for existing HTTP fixtures. First-run environment failures and the
metadata golden update were corrected before the passing final runs.

## Remaining work

- Track review and CI on the draft PRs. TokenSave issue #797 remains open;
  issue closure belongs to its maintainer.
- Actual agent A/B trials remain unperformed. The existing TokenSave runner is
  available, but no live telemetry adapter, paid model trials, independent task
  outcomes, or billing evidence were supplied. No savings or tool-selection
  equivalence is claimed. Synthetic accounting tests are not performance data.
- Both changes remain unmerged; the existing TokenSave draft PR remains open. TokenSave beta synchronization is an existing separate
  pending task; do not advertise unported runtime behavior on beta.
