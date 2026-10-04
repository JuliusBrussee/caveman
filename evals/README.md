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
  `--setting-sources project --strict-mcp-config --disable-slash-commands`
  from an empty temp dir: no user settings (so no installed plugins or
  their SessionStart hooks), no MCP servers, no installed skills, no
  CLAUDE.md. Without it a plugin's injected ruleset, or an MCP auth nag the
  model repeats in its answer, lands in every arm including the baseline.

## Files

- `prompts/en.txt` — fixed list of dev questions, one per line.
- `llm_run.py` — runs `claude -p --system-prompt …` per (prompt, arm),
  captures real LLM output, writes `snapshots/results.json` along with
  metadata (model, CLI version, generation timestamp).
- `measure.py` — reads the snapshot, counts tokens with tiktoken
  `o200k_base`, prints a markdown table with median / mean / min / max /
  stdev across prompts.
- `snapshot_contract.py` — rejects incomplete or malformed snapshot matrices
  before `measure.py` reports metrics.
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

By default every `skills/*/SKILL.md` gets an arm. `CAVEMAN_EVAL_SKILLS`
(comma-separated skill ids) restricts the skill arms; an unknown id aborts
before any call:

```bash
CAVEMAN_EVAL_SKILLS=caveman,ultracave,megacave uv run python evals/llm_run.py
```

## Read the snapshot (no LLM, no API key, runs in CI)

```bash
uv run --with tiktoken python evals/measure.py
```

Reporting fails closed unless the snapshot has both control arms, at least one
skill arm, exactly one string output per prompt in every arm, and metadata whose
`n_prompts` matches the prompt list.

## Adding a prompt

Append a line to `prompts/en.txt`, then refresh the snapshot.

## Adding a skill

Drop a `skills/<name>/SKILL.md`, then refresh the snapshot. `llm_run.py`
picks up every skill directory automatically, unless `CAVEMAN_EVAL_SKILLS`
is set, in which case add the new id to that list.

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

## Historical: reminder_run.py

`reminder_run.py` and `snapshots/reminder.json` measured the pre-3.1 per-turn reminder text (`Enforce this reply: ...`), which the mode tracker no longer emits. The snapshot is kept as history; regenerate before citing it.
