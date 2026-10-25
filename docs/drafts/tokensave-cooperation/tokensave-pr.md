# Title: feat: add optional Caveman cooperation and read-only diagnostics

## Problem and behavior

Users combining independently managed TokenSave and Caveman need cooperation
without changing semantic results or confusing installation with activation.
The existing local compatibility implementation provides read-only discovery,
conditional MCP initialization instructions, preferences, and doctor diagnostics.
This follow-up makes cooperation opt-in by default, makes indexed-project
readiness explicit, and includes errors in the exact-payload contract.

## Changes

- Reuse the existing `feat/caveman-compatibility` implementation: bounded skill
  discovery, installation/activation diagnostics, existing config preference and
  opt-out, and conditional `initialize.instructions`.
- Default cooperation off to satisfy existing issue #797; retain explicitly
  saved preferences and the per-run environment override.
- Clarify that suitable semantic queries require an indexed project and that
  uncertain readiness should be checked with `tokensave_status`.
- Preserve errors alongside code, identifiers, paths, signatures, numbers, and
  semantic relationships; avoid redundant exploration after sufficient evidence.
- Add a focused instruction regression and maintain compatibility documentation
  with the companion Caveman implementation and its unreleased status.
- Retain existing semantic-result equality tests and paired-trial runner. The
  runner's synthetic accounting tests provide no measured model savings.

No result formatter, public MCP contract, SQL, endpoint, provider/model selection,
or Caveman-owned configuration is modified. No installation or dependency is
introduced. Existing untracked package files are untouched.

## Validation

The isolated follow-up checkout passed:

```bash
cargo test --offline --locked --no-default-features --features lite --test integration caveman --target-dir /tmp/tokensave-cooperation-target
cargo test --offline --locked --no-default-features --features lite --lib user_config::tests --target-dir /tmp/tokensave-cooperation-target
cargo clippy --offline --locked --no-default-features --features lite --lib --tests --target-dir /tmp/tokensave-cooperation-target
cargo fmt --all -- --check
python3 -m unittest discover -s scripts/caveman_bench -p 'test_*.py'
git diff --check
```

The focused Rust run passed 12 tests, including actual semantic-result equality
across installation/removal and the new readiness/error contract. Three Python
accounting tests passed. Existing lite dead-code/unused-import warnings remain.
The opt-in correction also passed all 49 user-configuration tests and Clippy.
No warnings were reported in the changed cooperation code; unrelated existing
warnings remain. Full workspace
checks documented in the prior implementation record were not repeated here.

Resource review: the follow-up changes only constant instructions, a test, and
documentation. It adds no allocations on retrieval, growing cache, scan, SQL,
subprocess, background task, or changed resource lifecycle. Upstream master
`AGENTS.md` was compared; intentional branch-specific compatibility guidance
remains, with no missing shared instructions.

## Limits and related work

Caveman owns proxy preservation. Its new controls are unpublished and must not be
advertised as released. Unsupported identities, pixel fallback, and direct
Engine limitations are documented. Real telemetry, paid model trials,
independent task scoring, and billing are still missing. No measured savings or
selection equivalence is claimed. Beta synchronization remains a separate task.

The existing TokenSave draft is [PR #800](https://github.com/aovestdipaperino/tokensave/pull/800),
Refs [#797](https://github.com/aovestdipaperino/tokensave/issues/797). This follow-up
and its Caveman companion remain local and unmerged. No issue is closed.
