# Testing and benchmarks

Repository uses language-specific tests plus cross-package verification. Run
smallest relevant command first, then broader gates before release.

## Core test commands

### Go

```bash
go test ./...
go vet ./...
go build ./...
```

This covers Engine, proxy, memory, browser bridge, cache planner, and other Go
packages in module.

### TypeScript and JavaScript

```bash
pnpm install --frozen-lockfile
pnpm -r build
pnpm -r test
```

Individual packages expose narrower scripts:

```bash
pnpm --dir packages/cli test
npm --prefix extension test
pnpm --dir packages/shared/contracts test
```

Use package manifest to confirm exact scripts.

### Python

```bash
python -m pytest -q packages/sdk/python
```

### Repository verification

```bash
python tests/verify_repo.py
```

Verifier checks repository invariants that ordinary unit tests do not cover,
including generated or packaging expectations. Editing anything in `src/hooks/`
requires regenerating `src/hooks/checksums.sha256`, which the verifier compares
digest by digest.

### Claude Code hooks

```bash
npm test                                   # includes tests/hooks/*.test.mjs
node --test --test-force-exit tests/*.js
python -m unittest discover -s tests
bash tests/manual/session-mode-smoke.sh    # end-to-end, throwaway config dir
```

The smoke script drives the real hook binaries with the payloads Claude Code
sends and never touches your own `~/.claude`.

## Platform coverage

Continuous integration builds and tests native Go and JavaScript paths and
includes Windows coverage for platform-sensitive launcher behavior. Native
release workflow builds a 36-artifact operating-system, architecture, and
binary matrix, then publishes signed checksums.

A local pass on one platform does not prove full release matrix. Report exact
commands and environment tested.

## Benchmark categories

### Engine fixtures

Engine benchmarks compare original and compact representations on committed
fixtures. Valid report includes selected compressor, recovery result, byte or
token counter, and any invariant checks.

### Browser fixtures

Browser benchmark measures accessibility-tree capture, query focus, compression,
and recovery on recorded pages. See [`browse/BENCHMARK.md`](../../browse/BENCHMARK.md).

### Cache corpus

Cache planner corpus tests stress capability and fail-safe gates. Report
unsupported and rejected cases; raw pass rate is not a market-ranking result.

### Subagent tax

`packages/subagent-tax` runs local fixtures without provider requests. Its
counterfactual results apply to exact fixture, assembly method and token counter.

### Response-skill evals

`evals/` compares three arms per prompt: no system prompt (`__baseline__`),
`Answer concisely.` (`__terse__`), and `Answer concisely.` plus the skill. The
honest delta is skill against terse, never skill against baseline, because
baseline mixes the skill with plain terseness. `evals/llm_run.py` runs
`claude -p` with host settings, plugins and MCP isolated and stores text plus
Claude-reported usage in `evals/snapshots/`; `evals/measure.py` reads the
snapshot offline. A new `skills/<name>/SKILL.md` is picked up automatically; a
new prompt is one line in `evals/prompts/en.txt`. Snapshots are committed, so
CI needs no API calls. Regenerate only when a SKILL.md or a prompt changes. See
[`evals/README.md`](../../evals/README.md).

`benchmarks/run.py` sends real prompts through the Claude API and writes raw
token counts to `benchmarks/results/`
(`uv run python benchmarks/run.py`, `ANTHROPIC_API_KEY` in `.env.local`). The
README benchmark table between the `BENCHMARK-TABLE` markers is generated from
those results.

### Wrap report

Published wrap benchmark documents one recorded comparison and provenance. See
[`WRAP-BENCHMARK.md`](../WRAP-BENCHMARK.md) for reproduction availability and
claim limits.

## Writing a benchmark report

Every public result should include:

1. question being tested;
2. fixture names, source, and hashes;
3. code revision and date;
4. hardware or provider/model where relevant;
5. exact command;
6. count basis and price source;
7. baseline and treatment definitions;
8. recovery, parse, and quality checks;
9. failures, exclusions, and negative deltas;
10. narrow conclusion supported by data.

A fixture-level result is not production validation. Local token estimate is not
provider cost, and successful retrieval does not establish quality parity.

## Failure testing

Compression and integration tests should cover:

- malformed input;
- duplicate or ambiguous JSON;
- empty and boundary-sized payloads;
- transformation larger than original;
- full or failed recovery store;
- unknown mode, provider, model, and grader;
- stream interruption;
- unsafe endpoint and redirect;
- secret-bearing headers;
- unsupported platform or upstream version.

Expected safe result is often original input or explicit rejection. Tests should
assert that behavior, not only successful compression.

## Documentation verification

Documentation changes need:

- relative-link check: `node .github/scripts/check-doc-links.mjs <files>`
  (CI runs it on every README, INSTALL, SECURITY, LICENSING and
  `docs/technical` page);
- command and path check against current help and package tree;
- private or internal reference scan;
- stale claim scan against code and committed evidence;
- plain-language review;
- `git diff --check`.

Passing documentation checks proves consistency with checkout, not runtime or
release readiness.
