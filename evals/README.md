# Evals

Measures how much each caveman skill compresses vs a verbose baseline answer.

## How it works

`cases.yaml` holds golden pairs:

- `input` — the user question
- `baseline` — a typical verbose, hedged LLM answer (the thing caveman replaces)
- `outputs` — per-skill, per-level caveman answer (taken from each `SKILL.md` example)

`measure.py` counts tokens with `tiktoken` (`cl100k_base`) and reports savings as
`1 − (caveman_tokens / baseline_tokens)`, averaged across cases.

## Run

```bash
uv run --with tiktoken --with pyyaml python evals/measure.py
```

Or with pip:

```bash
pip install tiktoken pyyaml
python evals/measure.py
```

## Adding a case

Append a new entry to `cases.yaml`. Pull the `outputs` block straight from the
relevant `SKILL.md` examples — that way the eval measures what the skills
actually promise, not invented reference data.

## Adding a skill

Add the skill's name and per-level outputs to existing cases. New skills should
hit at least the same savings band as the existing family at the same level
(see the table in the root `README.md`) before being merged.

## What this does NOT measure

- LLM faithfulness (does the model actually follow the skill?) — manual eval
- Quality of the compressed answer — subjective
- Latency or cost — out of scope

A future v2 could add `evals/llm_run.py` that calls a real model with each skill
loaded and measures observed compression on held-out questions.
