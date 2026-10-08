# Evals

Measures real token compression of caveman skills by running the same
prompts through Claude Code under three conditions and comparing the
generated output token counts. A second eval, [Fidelity](#fidelity),
runs the same arms on fixed correctness cases and checks that the facts
survive.

## The three arms

| Arm | System prompt |
|-----|--------------|
| `__baseline__` | none |
| `__terse__` | `Answer concisely.` |
| `<skill>` | `Answer concisely.\n\n{SKILL.md}` |

The honest delta for any skill is **`<skill>` vs `__terse__`** — i.e.
how much the skill itself adds on top of a plain "be terse" instruction.
Comparing a skill to the no-system-prompt baseline conflates the skill
with the generic terseness ask, which is what an earlier version of
this harness did and is why its numbers were inflated.

## Current snapshot

`snapshots/results.json` was generated on `claude-opus-5-5` with Claude
Code 2.1.288 (2026-10-02), with the three response-style skills as the
skill arms: `caveman`, `ultracave`, `megacave`. The other `skills/*`
directories are workflow skills, not response styles, so they are left out.

```bash
CAVEMAN_EVAL_MODEL=claude-opus-5-5 CAVEMAN_EVAL_SKILLS=caveman,ultracave,megacave \
  python3 evals/llm_run.py
```

The previous snapshot (`claude-opus-4-6`, April 2026) was generated
without host isolation, so whatever plugins and CLAUDE.md files were
installed could reach every arm. `llm_run.py` now isolates each call
(see below); numbers from the two snapshots are not directly comparable.

## Why this design

- **Real LLM output**, not hand-written examples (no circularity).
- **Same Claude Code** the skills target — no separate API key.
- **Snapshot committed to git** so CI runs are deterministic and free,
  and so any change to the numbers is reviewable as a diff.
- **Control arm** isolates the skill's contribution from the generic
  "be terse" effect.
- **Host isolation.** Each `claude -p` call runs with
  `--setting-sources project --strict-mcp-config --disable-slash-commands
  --no-session-persistence` from an empty temp dir: no user settings (so
  no installed plugins or their SessionStart hooks), no MCP servers, no
  installed skills, no CLAUDE.md. Without it a plugin's injected ruleset,
  or an MCP auth nag the model repeats in its answer, lands in every arm
  including the baseline.

## Files

- `prompts/<lang>.txt` — fixed list of dev questions, one per line.
  `en.txt` is the default; `pt.txt` is Brazilian Portuguese; `fr.txt`
  is French and mirrors `en.txt` line for line.
- `llm_run.py` — runs `claude -p --output-format json --system-prompt-file …`
  per (prompt, arm), captures real LLM output, writes
  `snapshots/results.json` along with metadata (model, CLI version,
  language, generation timestamp). Next to the text it stores the usage
  Claude Code reports for each call under `usage` (same arm/prompt layout
  as `arms`): input, output, cache-creation and cache-read tokens plus
  `total_cost_usd`.
- `measure.py` — reads the snapshot, counts tokens with tiktoken
  `o200k_base`, prints a markdown table with median / mean / min / max /
  stdev across prompts. When the snapshot has `usage`, a second table
  gives Claude's own output-token counts against the terse control and
  the median input tokens the skill adds per call (skill arm minus terse
  arm, cache tokens included).
- `snapshot_contract.py` — rejects incomplete or malformed snapshot matrices
  before `measure.py` or `score_fidelity.py` reports metrics.
- `prompts/fidelity.json` — fixed correctness cases, each with regex
  checks (see [Fidelity](#fidelity)).
- `score_fidelity.py` — scores `snapshots/fidelity.json` against those
  checks, offline, stdlib only.
- `snapshots/results.json` — committed source of truth, regenerated only
  when SKILL.md files or prompts change. Other languages write
  `results.<lang>.json` next to it; none is committed yet.

## Refresh the snapshot (requires `claude` CLI logged in)

```bash
uv run python evals/llm_run.py
```

This calls Claude once per prompt × (N skills + 2 control arms). Use
a small model to keep it cheap:

```bash
CAVEMAN_EVAL_MODEL=claude-haiku-4-5 uv run python evals/llm_run.py
```

By default every `skills/*/SKILL.md` gets an arm. `CAVEMAN_EVAL_SKILLS`
(comma-separated skill ids) restricts the skill arms; an unknown id aborts
before any call:

```bash
CAVEMAN_EVAL_SKILLS=caveman,ultracave,megacave uv run python evals/llm_run.py
```

Each call times out after `CAVEMAN_EVAL_TIMEOUT` seconds (default 300). A
failed or timed-out call is retried twice, after 5 s and then 20 s. If it
still fails, the run stops (auth, quota or a bad model name would fail
every remaining call too), writes the finished calls to
`snapshots/results.partial.json` with the unfinished cells set to `null`
and the error in `metadata.error`, leaves `results.json` untouched and
exits 1. `measure.py` refuses a partial file; it is gitignored.

### Other languages

```bash
CAVEMAN_EVAL_LANG=pt CAVEMAN_EVAL_MODEL=claude-haiku-4-5 CAVEMAN_EVAL_SKILLS=caveman uv run python evals/llm_run.py
CAVEMAN_EVAL_LANG=pt uv run --with tiktoken python evals/measure.py
```

The terse control is translated per language (`TERSE_PREFIXES` in
`llm_run.py`): an English "Answer concisely." on a Portuguese question
also nudges the model toward English, which would be a second variable.
A language with no translated control, or no `prompts/<lang>.txt`, stops
before any call instead of falling back to English. The skill text
itself stays in English, as shipped.

Why a separate language matters: SKILL.md promises "compress the style,
not the language", and the rules target English function words
(a/an/the, just/really). Whether that transfers to a language with
gendered articles and a different filler vocabulary is an empirical
question, so it gets its own snapshot.

## Read the snapshot (no LLM, no API key, runs in CI)

```bash
uv run --with tiktoken python evals/measure.py
```

Reporting fails closed unless the snapshot has both control arms, at least one
skill arm, exactly one string output per prompt in every arm, and metadata whose
`n_prompts` matches the prompt list. A `usage` block, when present, must
cover the same arms and prompts with non-negative token counts.

## Adding a prompt

Append a line to `prompts/<lang>.txt`, then refresh that language's snapshot.

## Adding a language

Add `prompts/<lang>.txt` (ideally mirroring `en.txt` line for line),
add the translated terse control to `TERSE_PREFIXES` in `llm_run.py`,
run with `CAVEMAN_EVAL_LANG=<lang>` and commit
`snapshots/results.<lang>.json`.

## Adding a skill

Drop a `skills/<name>/SKILL.md`, then refresh the snapshot. `llm_run.py`
picks up every skill directory automatically, unless `CAVEMAN_EVAL_SKILLS`
is set, in which case add the new id to that list.

## Fidelity

The length eval alone rewards a skill that replies `k` to everything.
The fidelity eval asks whether each arm kept what the answer needs: exact
numbers and units, every not/never/only/except, the user's language,
verbatim errors, safety wording before destructive steps, normal prose in
persisted artifacts.

`prompts/fidelity.json` holds the cases: `{id, category, prompt, checks,
examples}` with `checks.must_include` and `checks.must_not_include` as
regex lists, and `examples.pass` / `examples.fail` as answers the checks
must accept / reject.
A case passes for an arm when every `must_include` pattern matches the
output and no `must_not_include` pattern does (Python `re.search`,
case-insensitive). There is no judge model, so the same snapshot always
gets the same score, and checks can be fixed and re-scored without new
calls. The first six cases come from
[#1061](https://github.com/JuliusBrussee/caveman/pull/1061) by alexis.

```bash
CAVEMAN_EVAL_SET=fidelity CAVEMAN_EVAL_MODEL=claude-opus-5-5 \
  CAVEMAN_EVAL_SKILLS=caveman,ultracave,megacave python3 evals/llm_run.py
python3 evals/score_fidelity.py
```

The run uses the same arms, isolation, retries and usage capture as the
length eval and writes `snapshots/fidelity.json`. The scorer prints the
pass rate per arm, overall and per category, then every failed check.
It refuses a snapshot whose prompts no longer match the case file.

No fidelity snapshot is committed yet. A regex pass is evidence, not a
verdict: read the failures before citing a rate, and publish no
quality-equivalence claim without a committed, reviewed snapshot.

| Category | What the checks look for |
|----------|--------------------------|
| `substance-preservation` | Filler removed, the technical substance needed to act kept. |
| `exact-preservation` | Polarity, limits, numbers, units, code, identifiers, APIs, commands and quoted errors unchanged. |
| `no-caricature` | No fake grammar, mode prefixes, invented abbreviations or arrows. |
| `language-and-grammar` | The requested or dominant language kept, grammatical markers kept. |
| `safety-clarity` | Plain, complete warnings for destructive actions, security, data loss and ordered recovery steps. |
| `artifact-boundary` | Persisted or external human-facing artifacts in normal prose. |
| `mode-boundaries` | Explicit activation, mode switch, persistence and deactivation boundaries preserved. |

To add a case, append it to `prompts/fidelity.json` with a unique `id`,
one of the categories above, at least one check, and at least one
known-good and one known-bad example answer, then rerun the fidelity
eval. `tests/test_eval_fidelity.py` checks the file's shape and runs
every case's checks against its examples, so a check that rejects a
correct answer fails there instead of after a paid run.

## What this does NOT measure

- **Fidelity beyond the checks** — the fidelity eval only sees what its
  regexes test. Tone, ordering and whether an explanation is actually
  right are left to human review of the outputs.
- **Latency or cost** — latency is out of scope. Skills add input tokens
  on every call, so output savings are not the full economic picture.
  Snapshots with `usage` record that input cost per call; the committed
  `results.json` predates usage capture, so it has none.
- **Cross-model behavior** — only the model used to generate the
  snapshot is measured.
- **Exact Claude tokens, from the tiktoken table** — `tiktoken
  o200k_base` is OpenAI's BPE and is only an approximation of Claude's
  tokenizer. Ratios between arms are meaningful; absolute numbers are
  approximate. The usage table uses Claude's own counts instead, but
  Claude's output count includes thinking tokens, which the user never
  reads.
- **Statistical significance** — single run per (prompt, arm) at default
  temperature. The min/max/stdev columns let you eyeball whether a
  number is solid or noisy, but this is not a powered experiment.

## Historical: reminder_run.py

`reminder_run.py` and `snapshots/reminder.json` measured the pre-3.1 per-turn reminder text (`Enforce this reply: ...`), which the mode tracker no longer emits. The snapshot is kept as history; regenerate before citing it.
