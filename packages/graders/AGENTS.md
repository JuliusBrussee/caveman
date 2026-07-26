# public/evals — eval grader taxonomy for Caveman Cloud

Single-file TypeScript package (`@caveman/evals`). Exports `grade(grader, value, deps?)` and
the `Grader` discriminated union. Mirrors `cloud/optimizer/caveman_optimizer/graders.py` — keep
the type names in sync. No runtime deps; stdlib-only (uses global `fetch`).

## Layout

- `src/index.ts` — all 16 current TypeScript grader types + `grade()` dispatch + helpers (SSRF guard, JSON-schema subset, tool-call extraction, localization F1)
- `tests/grade.runtime.mjs` — Node 22 `node:test` runtime tests; imports from `dist/`
- `tsconfig.json` / `tsconfig.test.json` — separate TS configs for src vs. tests

## Grader types (src/index.ts:1-30)

`exact_match` · `contains` · `regex` · `json_schema` · `json_path_assertion` · `tool_called` ·
`tool_not_called` · `tool_sequence` · `tool_argument_assertion` · `http_status` ·
`latency_threshold` · `cost_threshold` · `token_threshold` · `custom_webhook` · `localization_f1` · `llm_judge`

## Conventions

- Build: `pnpm build` → `tsc`; Test: `pnpm test` (tsc + tsc --project tsconfig.test.json + node --test tests/grade.runtime.mjs)
- Tests inject `fetch` and `ssrfCheck` via `GradeDeps`; real network calls are never made in tests
- `llm_judge` posts to `<gateway_url>/openai/v1/responses`; parses PASS/FAIL from model text
- Add new grader: extend `Grader` union in `src/index.ts`, add a `case` in `grade()`, add tests in `tests/grade.runtime.mjs`

## Gotchas

- **Fail closed (no-placeholder)**: the `default` branch at `src/index.ts:662-664` returns `fail(...)`, never `pass()`. Never change this.
- **exact_match is normalised** (case-insensitive + key-order-insensitive via `normaliseExact`/`stableStringify`) to MATCH the Python grader's verdict — do not revert to raw `JSON.stringify` (that diverged). Known *intentional* asymmetries vs Python: the legacy `semantic`/`custom` graders are Python-only (this package is the 16 current TypeScript grader types — a `semantic`/`custom` suite fails closed as "unknown grader" here), and the TS `GradeResult` is `{passed,reason}` vs Python `{passed,grader,score,reason}`. These are by design, not drift.
- **SSRF guard**: `custom_webhook` and `llm_judge` both call `defaultSsrfCheck` before fetching. IP literals and bare hostnames (without an injected resolver-backed `ssrfCheck`) are blocked.
- `llm_judge` needs `gateway_url` set — fails with a clear message if missing, not silently.
- `tool_sequence` checks ordered *subsequence*, not exact sequence; tests at `tests/grade.runtime.mjs:73-77` clarify the contract.

See ../../CLAUDE.md (root) · ../../docs/design.md
