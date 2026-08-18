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

## Other languages

The prompt set defaults to English. `CAVEMAN_EVAL_LANG` selects another one:

```bash
CAVEMAN_EVAL_LANG=fr uv run python evals/llm_run.py
CAVEMAN_EVAL_LANG=fr uv run --with tiktoken python evals/measure.py
```

It reads `prompts/<lang>.txt` and writes `snapshots/results.<lang>.json`, so
per-language snapshots never overwrite each other. `en` keeps the original
`snapshots/results.json` path, so existing invocations are unchanged.

The `__terse__` control arm is written in the prompt set's language
(`TERSE_PREFIX_BY_LANG` in `llm_run.py`). This matters: an English
"Answer concisely." in front of a French prompt measures a language switch on
top of terseness, which is not the control the harness is trying to isolate.
A language with no entry falls back to the English prefix — add one when you
add a prompt set.

`prompts/fr.txt` mirrors `prompts/en.txt` line for line, same ten topics in the
same order, so the two sets are comparable arm by arm.

## Adding a language

1. Write `prompts/<lang>.txt`, ideally mirroring `en.txt` line for line.
2. Add the language's terse instruction to `TERSE_PREFIX_BY_LANG`.
3. Refresh with `CAVEMAN_EVAL_LANG=<lang> uv run python evals/llm_run.py`.

## Adding a prompt

Append a line to `prompts/en.txt`, then refresh the snapshot.

## Adding a skill

Drop a `skills/<name>/SKILL.md`, then refresh the snapshot. `llm_run.py`
picks up every skill directory automatically.

## What this does NOT measure

- **Fidelity** — does the compressed answer preserve the technical
  claims? A skill that replies `k` to everything would score −99% and
  "win". A future v2 could add a judge-model rubric.
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
