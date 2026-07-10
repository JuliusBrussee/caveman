# Cost Analysis: When Caveman Increases Token Spend

## Overhead Measurement

| Metric | Value |
|--------|-------|
| SKILL.md raw size | 5,304 chars / 78 lines |
| Emitted after filtering (full mode) | ~3,500 chars |
| **Token overhead per session** | **~875 input tokens** |
| With statusline nudge (first run) | ~975 input tokens |
| On context compression re-injection | 875 tokens **again** |

Token estimate uses 1 token per 4 English chars (cl100k_base average).

## Break-Even Calculation

Claude models maintain a consistent 1:5 input:output cost ratio ($3/$15 Sonnet, $15/$75 Opus). The break-even formula is model-independent:

```
break_even_output_tokens = overhead / (savings_rate * output_cost_ratio)
                         = 875 / (0.65 * 5)
                         = 269 output tokens
```

**For a pure-prose session, caveman pays for itself after ~269 output tokens** (~1 short response).

## When Costs INCREASE

### 1. Short sessions (< 269 output tokens)

A one-shot "what does this error mean?" session generating 150 output tokens:

- Overhead cost: 875 input tokens
- Savings: 150 * 0.65 = 97.5 output tokens saved = 487.5 input-equivalent tokens saved
- **Net loss: 387.5 input-equivalent tokens**

### 2. Code-heavy sessions

SKILL.md explicitly states "Code blocks unchanged." If output is 80% code:

- Effective savings rate: 0.65 * 0.20 = 13%
- Break-even: 875 / (0.13 * 5) = **1,346 output tokens**
- At 90% code: break-even rises to **2,692 output tokens**

A session generating 5 code-heavy responses of 400 tokens each (2000 total, 90% code) loses money until the 7th response.

### 3. Context compression re-injection

The hook reads and emits SKILL.md on SessionStart. When the context window fills and the provider compresses, the skill text re-enters as fresh input. Each compression cycle adds another 875 tokens of overhead:

| Compressions | Effective overhead | Break-even (prose) | Break-even (80% code) |
|---|---|---|---|
| 0 | 875 | 269 | 1,346 |
| 1 | 1,750 | 538 | 2,692 |
| 2 | 2,625 | 808 | 4,038 |

Long sessions with many compressions and mostly code output are the worst case.

## Proposed Mitigations

### 1. Lite-prompt mode (~25 tokens overhead)

```
Respond terse. Drop filler/articles/hedging. Fragments OK. Code unchanged. Technical terms exact.
```

Break-even: 8 output tokens. Effectively always profitable. Trade-off: weaker behavioral anchoring -- models drift back to verbose after 5-10 turns (observed in pre-v0.9 testing per hook comments).

Add to activation hook: `CAVEMAN_LITE_PROMPT=1` env var or `/caveman --lite-prompt` flag.

### 2. Skip activation for code-heavy sessions

Heuristic: if the first user message contains a code block > 20 lines or matches patterns like "refactor", "implement", "write a function", predict code-heavy output and suppress activation. Re-enable if prose ratio exceeds 40% after 3 responses.

### 3. Break-even tracking with auto-disable

Track in the hook's flag file:

```json
{
  "mode": "full",
  "session_input_overhead": 875,
  "estimated_tokens_saved": 0,
  "break_even_reached": false
}
```

After each response, estimate savings (output_length * 0.65 * code_fraction_adjustment). If after 5 responses the session is still running at a loss, emit a one-line suggestion: "caveman overhead exceeds savings this session. `/caveman off` to disable."

## Summary

| Session type | Caveman profitable? |
|---|---|
| Multi-turn prose Q&A (>3 responses) | Yes, strongly |
| Single question, short answer | **No** -- overhead dominates |
| Code generation / refactoring | **No** unless session is long (>7 responses) |
| Mixed prose + code, medium length | Yes, after 2-3 responses |
| Any session with context compression | Marginal -- re-injection erodes savings |

The 65% output savings claim is real for prose, but the 875-token fixed overhead means caveman is a net cost for the short, code-heavy sessions that many developers run most often.
