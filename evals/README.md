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

- `prompts/<lang>.txt` — fixed list of dev questions, one per line.
  `en.txt` is the default; `pt.txt` is Brazilian Portuguese.
- `llm_run.py` — runs `claude -p --system-prompt-file …` per (prompt, arm),
  captures real LLM output, writes `snapshots/results.json` along with
  metadata (model, CLI version, language, generation timestamp).
  Every call is isolated from the local machine (`--setting-sources ""`,
  `--disable-slash-commands`, `--tools ""`, `--strict-mcp-config`):
  without that the user's own `~/.claude/CLAUDE.md` is in every arm,
  baseline included (measured with a probe prompt), and any hook or
  plugin that injects style rules would be too.
- `measure.py` — reads the snapshot, counts tokens with tiktoken
  `o200k_base`, prints a markdown table with median / mean / min / max /
  stdev across prompts.
- `snapshots/results.json` — committed source of truth, regenerated only
  when SKILL.md files or prompts change. Other languages live next to it
  as `results.<lang>.json`.

## Refresh the snapshot (requires `claude` CLI logged in)

```bash
uv run python evals/llm_run.py
```

This calls Claude once per prompt × (N skills + 2 control arms). Use
a small model to keep it cheap:

```bash
CAVEMAN_EVAL_MODEL=claude-haiku-4-5 uv run python evals/llm_run.py
```

`CAVEMAN_EVAL_SKILLS=caveman,caveman-compress` limits the run to the
named skills instead of every `skills/*/SKILL.md`.

### Other languages

```bash
CAVEMAN_EVAL_LANG=pt CAVEMAN_EVAL_MODEL=claude-haiku-4-5 CAVEMAN_EVAL_SKILLS=caveman uv run python evals/llm_run.py
CAVEMAN_EVAL_LANG=pt uv run --with tiktoken python evals/measure.py
```

The terse control is translated per language (`TERSE_PREFIXES` in
`llm_run.py`): an English "Answer concisely." on a Portuguese question
also nudges the model toward English, which would be a second variable.
The skill text itself stays in English, as shipped.

Why a separate language matters: SKILL.md promises "compress the style,
not the language", and the rules target English function words
(a/an/the, just/really). Whether that transfers to a language with
gendered articles and a different filler vocabulary is an empirical
question, so it gets its own snapshot.

## Read the snapshot (no LLM, no API key, runs in CI)

```bash
uv run --with tiktoken python evals/measure.py
```

## Adding a prompt

Append a line to `prompts/<lang>.txt`, then refresh that language's snapshot.

## Adding a language

Add `prompts/<lang>.txt`, add the translated terse control to
`TERSE_PREFIXES` in `llm_run.py`, run with `CAVEMAN_EVAL_LANG=<lang>`
and commit `snapshots/results.<lang>.json`.

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
