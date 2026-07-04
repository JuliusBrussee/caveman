# Benchmarks

Reproducible token-reduction measurement for the `caveman` skill.

```bash
python3 -m venv venv && ./venv/bin/pip install tiktoken
./venv/bin/python measure.py
```

`measure.py` holds 8 paired responses (normal baseline vs caveman `full` vs `ultra`) across
a MECE spread of dev tasks — React, auth, DB pooling, git, Python perf, Docker, SQL N+1,
async races — and counts tokens with tiktoken `o200k_base`.

## Results (this repo, 8 prompts)

| Level | Mean reduction | Range |
|---|---|---|
| full (default) | **53%** | 43–68% |
| ultra | **71%** | 63–79% |

Structured answers (SQL N+1, Docker caching) compress least because they're already
information-dense; conversational prose (React, async) compresses most.

## Honesty note

Upstream caveman advertises "~65–75%." That figure matches **ultra**, not the **full**
default. This port states ~50% (full) / ~70% (ultra) instead. Caveats: n=8, responses are
hand-authored (some optimism bias toward clean compression), and `o200k_base` BPE is a
proxy for Claude's tokenizer, not identical. Treat as directional, not a guarantee.
