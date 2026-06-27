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
- `prompts/rubrics.json` — per-prompt **fidelity rubric**: binary facts a
  correct answer must contain, a `risk` class, and `verbatim` invariants
  (tokens that must appear literally).
- `judge.py` — scores each answer for correctness against the rubrics
  (LLM judge for facts + deterministic verbatim check), writes
  `snapshots/fidelity.json`.
- `gate.py` — the two-gate accept rule: baseline vs candidate, exits
  non-zero unless tokens hold AND fidelity holds.
- `snapshots/fidelity.json` — committed correctness snapshot (regenerated
  by `judge.py` when SKILL.md / prompts / rubrics change).

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

## Fidelity + the two-gate accept rule

`measure.py` only counts tokens. On its own that is gameable: a skill that
replies `k` to everything scores −99% and "wins". `judge.py` + `gate.py` add
the missing correctness axis so an upgrade is accepted only if it **saves
tokens AND stays correct**.

**1. Author rubrics** (`prompts/rubrics.json`) — for each prompt, the binary
facts a correct answer must contain, a `risk` class, and `verbatim` tokens that
must survive compression verbatim (e.g. `TCP`, `EXPLAIN`, `rebase`).

**2. Score fidelity** (writes `snapshots/fidelity.json`) — pure stdlib, no `uv`
needed; requires the authenticated `claude` CLI on PATH (Windows: `claude.exe`'s
bin dir, e.g. `…\node_modules\@anthropic-ai\claude-code\bin`):

```bash
python evals/judge.py            # all arms
python evals/judge.py --runs 3   # majority vote per fact (lower judge variance)
```

Each answer gets `fidelity = passed_facts / total_facts * 100` from an LLM judge
(temp 0), plus a deterministic `verbatim_ok` flag — an abbreviated `useMemo` or
`TCP` is a correctness bug, not a style win, and is caught without the judge.

**3. Gate a change** — commit the current snapshot+fidelity as the baseline,
regenerate `results.json` + `fidelity.json` into a candidate dir for the
proposed SKILL change, then:

```bash
python -m pip install tiktoken      # gate.py and measure.py need it (or: uv run --with tiktoken)
python evals/gate.py \
    --baseline evals/snapshots --candidate evals/snapshots-candidate --arm caveman
```

It exits non-zero unless **both**:

- **token gate** — median savings don't regress beyond the noise floor;
- **quality gate** — mean fidelity drop ≤ tolerance (default 2 pts), no single
  prompt drops past its risk band's hard limit (normal 10 pts, **high 0 pts**),
  and every verbatim invariant still holds.

High-risk prompts (e.g. the git-rebase "rewrites history" warning) allow **zero**
fidelity loss — compression there is historically dangerous. The decision logic
is pure/offline and unit-tested in `tests/test_eval_gate.py`, whose canonical
case asserts a "reply `k`" candidate is **rejected**.

> Calibrate `--token-noise` / `--fidelity-tol` from the measured same-SKILL
> run-to-run spread (run `llm_run.py` + `judge.py` twice unchanged) before
> trusting the gate. Keep the committed baseline pinned to the ORIGINAL so a
> chain of −1.9pt changes can't ratchet quality down unnoticed.

## What this does NOT measure

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
