# caveman

Talk like smart caveman. Same brain, fewer tokens.

## What it does

Compress every model response to caveman-style prose. Drops articles, filler, pleasantries, and hedging. Keeps every technical detail, code block, error string, and symbol exact. Cuts ~65-75% of output tokens with full accuracy preserved. Mode persists for the whole session until changed or stopped.

Five intensity levels:

| Level | What change |
|-------|-------------|
| `lite` | Drop filler/hedging. Sentences stay full. Professional but tight. |
| `full` | Default. Drop articles, fragments OK, short synonyms. |
| `ultra` | Bare fragments. Abbreviations (DB, auth, fn). Arrows for causality. |
| `super-compress` | Aggressive compression. Symbol-first prose (`+`, `->`, `=`) when unambiguous. Shortest safe answer. |
| `silence` | Minimum viable answer. Emit only blocker/question/safety/final result. One line if enough. |

Auto-clarity rule: caveman drops to normal prose for security warnings, irreversible-action confirmations, multi-step sequences where fragment ambiguity risks misread, and when user repeats a question. Resumes after the clear part.

Internal validation rule (mode silence): final summary mandatory on 100% completed tasks, 1-3 lines (status, result, next action).

## How to invoke

```
/caveman              # full mode (default)
/caveman lite         # lighter compression
/caveman ultra        # extreme compression
/caveman super-compress # extra-aggressive compression
/caveman silence      # minimum viable answer
stop caveman          # back to normal prose
```

## Example output

Question: "Why does my React component re-render?"

Normal prose:
> Your component re-renders because you create a new object reference each render. Wrapping it in `useMemo` will fix the issue.

Caveman (full):
> New object ref each render. Inline object prop = new ref = re-render. Wrap in `useMemo`.

Caveman (ultra):
> Inline obj prop → new ref → re-render. `useMemo`.

## See also

- [`SKILL.md`](./SKILL.md) — full LLM-facing instructions
- [Caveman README](../../README.md) — repo overview, install, benchmarks
