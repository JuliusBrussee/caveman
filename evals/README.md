# Evals

Measures real token compression of caveman skills by running the same
prompts through Claude Code under three conditions and comparing the
generated output token counts.

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

## Why this design

- **Real LLM output**, not hand-written examples (no circularity).
- **Same Claude Code** the skills target — no separate API key.
- **Snapshot committed to git** so CI runs are deterministic and free,
  and so any change to the numbers is reviewable as a diff.
- **Control arm** isolates the skill's contribution from the generic
  "be terse" effect.

## Files

- `prompts/en.txt` — fixed list of dev questions, one per line.
- `llm_run.py` — runs `claude -p --system-prompt …` per (prompt, arm),
  captures real LLM output, writes `snapshots/results.json` along with
  metadata (model, CLI version, generation timestamp).
- `measure.py` — reads the snapshot, counts tokens with tiktoken
  `o200k_base`, prints a markdown table with median / mean / min / max /
  stdev across prompts.
- `snapshots/results.json` — committed source of truth, regenerated only
  when SKILL.md files or prompts change.

## Semantic contract and paired prompt experiments

The compression benchmark above remains a historical all-skills harness.
Before changing the Caveman behavior prompt, use the semantic contract path:

```bash
python3 -m unittest tests.test_caveman_semantic_contract tests.test_caveman_semantic_eval
python3 evals/caveman_semantic_eval.py measure
python3 evals/caveman_semantic_eval.py run \
  --run-id baseline-YYYY-MM-DD-model \
  --baseline-model claude-haiku-4-5
```

`tests/fixtures/caveman-semantic-invariants.json` defines seven stable behavior
groups separately from replaceable calibration wording. The contract test
materializes the canonical skill, real Claude SessionStart injection, installed
compact rule, OpenClaw bootstrap plus skill, Codex and opencode commands,
isolated hook fallback, and MV3 primer/reminder through their production call
sites.

For an experiment, add `--candidate-skill PATH`. Both arms receive identical
case prompts. Model aliases, provider-reported model IDs, complete system
prompts, raw outputs, provider usage envelopes, deterministic judge results,
and uncertainty are stored under `evals/snapshots/caveman-semantic/`. Snapshot
creation uses exclusive file creation: an existing run ID is immutable.

Render a reviewed input report without calling a model:

```bash
python3 evals/caveman_semantic_eval.py report \
  evals/snapshots/caveman-semantic/RUN-ID.json
```

Exact structural measurements report UTF-8 bytes, whitespace-delimited words,
and lines. `ceil(bytes / 4)` is labeled only as a repository-compatible
input-size proxy; it is not a tokenizer result. Token, cache, output, and cost
claims must use provider-returned usage from a committed run. An `unknown`
judge result remains unknown and never counts as a pass.

## Refresh the snapshot (requires `claude` CLI logged in)

```bash
uv run python evals/llm_run.py
```

This calls Claude once per prompt × (N skills + 2 control arms). Use
a small model to keep it cheap:

```bash
CAVEMAN_EVAL_MODEL=claude-haiku-4-5 uv run python evals/llm_run.py
```

## Read the snapshot (no LLM, no API key, runs in CI)

```bash
uv run --with tiktoken python evals/measure.py
```

## Adding a prompt

Append a line to `prompts/en.txt`, then refresh the snapshot.

## Adding a skill

Drop a `skills/<name>/SKILL.md`, then refresh the snapshot. `llm_run.py`
picks up every skill directory automatically.

## What this does NOT measure

- **Fidelity in the historical all-skills snapshot** — does the compressed
  answer preserve the technical claims? Use the semantic contract runner for
  Caveman fidelity gates; the old `results.json` has no judge evidence.
- **Latency or cost** — out of scope. Note that skills add input tokens
  on every call, so output savings are not the full economic picture.
- **Cross-model behavior** — only the model used to generate the
  snapshot is measured.
- **Exact Claude tokens** — `tiktoken o200k_base` is OpenAI's BPE and is
  only an approximation of Claude's tokenizer. Ratios between arms are
  meaningful; absolute numbers are approximate.
- **Statistical significance** — single run per (prompt, arm) at default
  temperature. The min/max/stdev columns let you eyeball whether a
  number is solid or noisy, but this is not a powered experiment.
