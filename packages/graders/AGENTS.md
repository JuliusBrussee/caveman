# packages/graders — eval grader taxonomy for Caveman Cloud

Single-file TypeScript package (`@caveman/evals`). Exports `grade(grader, value, deps?)` and
the `Grader` discriminated union. Mirrors `cloud/optimizer/caveman_optimizer/graders.py` — keep
the type names in sync. No runtime deps; stdlib-only (uses global `fetch`).

## Layout

- `src/index.ts` — all 27 current TypeScript grader types + `grade()` dispatch + helpers (SSRF guard, JSON-schema subset, tool-call extraction, localization F1, shared BLEU/ROUGE tokenizer)
- `tests/grade.runtime.mjs` — Node 22 `node:test` runtime tests; imports from `dist/`
- `tests/langevals-port.vectors.json` + `tests/langevals-port.parity.runtime.mjs` — cross-language parity vectors for the 7 langevals-ported types below; follows the existing `localization_f1.vectors.json`/`localization_f1.parity.runtime.mjs` convention (one JSON fixture, read by both the TS and the Python parity test)
- `tests/langevals-judge.vectors.json` + `tests/langevals-judge.parity.runtime.mjs` — cross-language parity vectors for the 4 LLM-judge types below; each vector also carries `expected_prompts` (the exact prompt string(s) both sides must send to the judge model) to assert prompt-template byte-parity; same one-JSON-fixture-read-by-both-sides convention as `langevals-port.vectors.json`
- `tests/grader-registry.parity.runtime.mjs` — holds `public/shared/contracts/schemas/grader-registry.json` (the registry the dashboard grader editor reads) to this dispatch: every non-`python_only` entry must be recognised by `grade()`, `semantic`/`custom` must still fail closed here, and each judge entry's `prompt_template` must byte-match the prompt actually sent
- `tsconfig.json` / `tsconfig.test.json` — separate TS configs for src vs. tests

## Grader types (src/index.ts)

`exact_match` · `contains` · `regex` · `json_schema` · `json_path_assertion` · `tool_called` ·
`tool_not_called` · `tool_sequence` · `tool_argument_assertion` · `http_status` ·
`latency_threshold` · `cost_threshold` · `token_threshold` · `custom_webhook` · `localization_f1` · `llm_judge` ·
`not_contains` · `not_regex` · `blocklist` · `bleu_score` · `rouge_score` · `context_f1` · `no_pii` ·
`llm_score` · `llm_category` · `llm_pairwise` · `llm_answer_match`

The last 11 are ported from langevals (behavior reference only, reimplemented — see Gotchas):
- `not_contains` — inverse of `contains`: fails if any required fragment is present, passes only when none are
- `not_regex` — inverse of `regex`: fails if the pattern matches, passes when it does not
- `blocklist` — case-insensitive whole-word match (lookaround-based, not `\b`) against a term list
- `bleu_score` — modified n-gram precision + brevity penalty vs. a reference string (0-1)
- `rouge_score` — unigram or LCS overlap vs. a reference string, precision/recall/fmeasure (0-1)
- `context_f1` — precision/recall/F1 between retrieved vs. expected context lists via normalized Levenshtein similarity
- `no_pii` — fails on any pinned-regex PII match (email/credit_card/iban/ipv4/ipv6/phone/crypto)
- `llm_score` — LLM-judge: scores the RESPONSE 0.00–1.00 against a rubric via the shared `llm_judge` gateway plumbing; passes iff the parsed score >= `min_score` (inclusive)
- `llm_category` — LLM-judge: classifies the RESPONSE into exactly one category from a fixed list; passes iff the matched category is in `passing_categories` (case-insensitive)
- `llm_pairwise` — LLM-judge: compares candidate vs. `baseline` under BOTH A/B orderings (2 judge calls, position-bias mitigation); passes iff the candidate wins or ties under both orderings, fails if baseline wins either one
- `llm_answer_match` — LLM-judge: decides whether the RESPONSE conveys the same answer as `expected`, ignoring style/wording/formatting; passes iff MATCH: YES

`exact_match` also gained optional `case_sensitive`/`remove_punctuation` knobs; both default `false` — old verdicts unchanged.

## Conventions

- Build: `pnpm build` → `tsc`; Test: `pnpm test` (tsc + tsc --project tsconfig.test.json + node --test tests/grade.runtime.mjs tests/localization_f1.parity.runtime.mjs tests/langevals-port.parity.runtime.mjs tests/langevals-judge.parity.runtime.mjs tests/grader-registry.parity.runtime.mjs)
- Tests inject `fetch` and `ssrfCheck` via `GradeDeps`; real network calls are never made in tests
- `llm_judge` posts to `<gateway_url>/openai/v1/responses`; parses PASS/FAIL from model text
- `bleu_score`/`rouge_score` share one pinned tokenizer (lowercase, then `[a-z0-9]+` runs / single-char symbols, no `\w`, no locale) — identical on the Python side, NOT the sacrebleu/rouge-score tokenizer
- Add new grader: extend `Grader` union in `src/index.ts`, add a `case` in `grade()`, add tests in `tests/grade.runtime.mjs`, and add an entry to `public/shared/contracts/schemas/grader-registry.json` (the registry parity test fails without one)

## Gotchas

- **Fail closed (no-placeholder)**: the `default` branch at the end of `grade()`'s switch in `src/index.ts` returns `fail(...)`, never `pass()`. Never change this — it is also the langevals-port's core deviation: upstream langevals *skips* on empty/missing input and returns an `error` status on exceptions; Caveman inverts both into `{passed: false}` with a reason, never a silent pass or skip.
- **`bleu_score`/`rouge_score` are not sacrebleu/rouge-score parity metrics** — deterministic and cross-language identical via the pinned tokenizer + spec-fixed algorithm, but not comparable to published BLEU/ROUGE numbers. Never cite them as such. "Cross-language identical" means precisely this: every langevals-ported grader tokenizes/normalizes on the pinned ASCII whitespace class `[ \t\n\r\f\v]`, never `\s` (JS and Python disagree on what `\s` covers). The TS tokenizer regex carries the `u` flag so an astral character (e.g. an emoji) counts as one code point exactly like Python's code-point-based `re`; every Python pinned regex additionally compiles with `re.ASCII` so `\d`/`\w`/`\b` mean their ASCII forms on both sides. Accepted residuals — documented, not fixed: the pre-existing `exact_match` casefold `ß`→`ss` vs JS `toLowerCase()` divergence (predates this port); `json.dumps` vs `JSON.stringify` spacing differs for non-string `exact_match` candidates (Python's serializer inserts `", "`/`": "`, JS's does not); `not_regex`: JS `.` excludes U+2028/U+2029 that Python's `.` does not.
- **`not_regex` normalization**: both sides replace CRLF and a lone CR with LF, then strip exactly one trailing LF, before matching — so `$` and `.` behave the same for Windows line endings or a single trailing newline. Python compiles the user pattern with `re.ASCII`.
- **Size caps (fail closed, identical both sides)**: `bleu_score`/`rouge_score` fail if either side tokenizes to more than 4096 tokens; `context_f1` fails if either list has more than 64 members or the sum of its members' lengths exceeds 8192 chars. Reason string: "input exceeds grader size cap".
- **Name collision with `public/engine/evals` (Go)**: that package has its own `not_contains` grader with a different option shape (`value: string`, byte-match via `bytes.Contains`, no fragment list) — same type name, different harness. Both fail closed on the other's option shape; they are not interchangeable and share no code.
- **`no_pii` is a conservative regex subset, not Presidio**: fewer entity types, checksum-valid credit cards/IBANs only, international `+`-prefixed phone numbers only. Never claim Presidio-equivalent coverage.
- **LLM-judge parse regexes** (`llm_score`'s `SCORE:`, `llm_category`'s `CATEGORY:`, `llm_pairwise`'s `WINNER:`, `llm_answer_match`'s `MATCH:`) use the same pinned ASCII whitespace class `[ \t\n\r\f\v]` as the langevals-ported types above — never `\s`. All four KEYWORDS (`SCORE:`/`CATEGORY:`/`WINNER:`/`MATCH:`) are matched CASE-SENSITIVELY; only the captured tokens (`A`/`B`/`TIE`, `YES`/`NO`) accept either case, spelled per character — no `i` flag / `re.IGNORECASE` anywhere, because an engine-wide flag would also accept a lowercased keyword on one side only. Model text (never a user pattern, never the candidate) is normalized CR→LF **and** U+2028/U+2029→LF before parsing on both sides: JS `^`/`$` under `m` anchor at all of them, Python's `re.MULTILINE` only at `\n`. A NON-STRING judge candidate is serialized as COMPACT JSON on both sides (`JSON.stringify` / `json.dumps(separators=(",", ":"))`) — the `json.dumps` spacing residual documented above applies to `exact_match`, NOT to this family, which has its own serializer so the shared one stays frozen. Any model-controlled text quoted in a verdict reason (e.g. an unknown category label) is truncated to 160 code points + `…`. Prompt interpolation is string-only: non-string options (`rubric`/`prompt`/`criteria`/`baseline`/`expected`/category names) fail closed before any judge call is made — never spend a model call on an invalid grader. `gateway_url` resolution is DELIBERATELY asymmetric: the Python side falls back to `$CAVE_GATEWAY_URL` (via `_judge_preflight`, exactly as the pre-existing `llm_judge` always has), while the TypeScript package has no env access and fails closed when the option is missing. That is by design — do not "fix" either side; a vector always passes `gateway_url` explicitly, so the asymmetry never reaches parity.
- **`llm_pairwise` makes TWO judge calls per grade** (candidate as A then candidate as B, to mitigate position bias) — roughly double the judge-model cost and latency of a single `llm_judge`/`llm_score` call. Factor this into eval-gate cost estimates.
- **LLM-judge graders are non-deterministic in production** (`llm_judge`, `llm_score`, `llm_category`, `llm_pairwise`, `llm_answer_match`): verdicts are only as reliable as the judge model. Pin the judge model for any eval gate that uses one. Every parse failure, refusal, or (for `llm_pairwise`) ordering disagreement fails closed — never a silent pass or retry. Judge scores are grader verdicts, not money figures: never describe them as `measured` anything.
- **exact_match is normalised** (case-insensitive + key-order-insensitive via `normaliseExact`/`stableStringify`) to MATCH the Python grader's verdict — do not revert to raw `JSON.stringify` (that diverged). Known *intentional* asymmetries vs Python: the legacy `semantic`/`custom` graders are Python-only (this package is the 27 current TypeScript grader types — a `semantic`/`custom` suite fails closed as "unknown grader" here), and the TS `GradeResult` is `{passed,reason}` vs Python `{passed,grader,score,reason}` (Python also sets `score` for `bleu_score`/`rouge_score`/`context_f1`). These are by design, not drift.
- **SSRF guard**: `custom_webhook` calls `defaultSsrfCheck` directly, and every LLM-judge type — `llm_judge` plus `llm_score`/`llm_category`/`llm_pairwise`/`llm_answer_match` — calls it through the shared `callJudge` transport (the mirror of the Python `_judge_preflight`). IP literals and bare hostnames (without an injected resolver-backed `ssrfCheck`) are blocked.
- **Redirects are REFUSED, never followed**: every outbound call fetches with `redirect: "manual"` and fails closed on any 3xx (`redirect refused`). The SSRF guard only validated the url the caller supplied; a redirect moves the request to a host nothing checked. This CHANGED pre-existing behaviour for `custom_webhook` and `llm_judge` (a redirecting endpoint used to be followed), and it is the one place `llm_judge`'s otherwise-frozen non-2xx handling does not apply. `cloud/optimizer` refuses the same way (`_NoRedirectHandler`), so the two sides stay symmetric.
- `llm_judge` needs `gateway_url` set — fails with a clear message if missing, not silently.
- `tool_sequence` checks ordered *subsequence*, not exact sequence; tests at `tests/grade.runtime.mjs:73-77` clarify the contract.
- Each langevals-ported section carries the comment `Behavioral reference: langevals (MIT, (c) 2024 Reasoning Engine B.V.), reimplemented`.

See ../../CLAUDE.md (root)
