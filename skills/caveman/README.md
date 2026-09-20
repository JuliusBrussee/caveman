# caveman

Talk like smart caveman. Same brain, fewer tokens.

## What it does

Compress model responses to caveman-style prose by dropping articles, filler,
pleasantries, and hedging. Instruction preserves technical detail, code blocks,
error strings, and symbols. Result depends on model and workload; no aggregate
reduction or quality-equivalence claim is published, and mode persists until
changed or stopped.

Six intensity levels:

| Level | What change |
|-------|-------------|
| `lite` | Drop filler/hedging. Sentences stay full. Professional but tight. |
| `full` | Default. Drop articles, fragments OK, short synonyms. |
| `ultra` | Bare fragments. Abbreviations (DB, auth, fn). Arrows for causality. |
| `wenyan-lite` | Classical Chinese register, light compression. |
| `wenyan-full` | Maximum 文言文 compression. |
| `wenyan-ultra` | Extreme classical compression. |

Auto-clarity rule: caveman drops to normal prose for security warnings, irreversible-action confirmations, multi-step sequences where fragment ambiguity risks misread, and when user repeats a question. Resumes after the clear part.

## How to invoke

```
/caveman              # full mode (default)
/caveman status       # show current mode without changing it
/caveman lite         # lighter compression
/caveman ultra        # extreme compression
/caveman wenyan       # classical Chinese
stop caveman          # back to normal prose
```

Claude Code and the standalone OpenCode plugin read stored mode state. Other hosts report the mode known in the conversation, or `unknown` if none is known.

Want Claude Code sessions to start with normal prose? Set `{"defaultMode":"manual"}` in `.caveman.json` for one project or `~/.config/caveman/config.json` for your user. You can also set `CAVEMAN_DEFAULT_MODE=manual`. Then `/caveman` activates full mode; `/caveman lite` selects lite. Stop persists across compaction and resume; a fresh session starts off again. This startup policy is Claude Code-only: OpenCode keeps its full-mode default because the installer also supplies static activation rules.

## Example output

Question: "Why does my React component re-render?"

Normal prose:
> Your component re-renders because you create a new object reference each render. Wrapping it in `useMemo` will fix the issue.

Caveman (full):
> New object ref each render. Inline object prop = new ref = re-render. Wrap in `useMemo`.

Caveman (ultra):
> Inline obj prop → new ref → re-render. `useMemo`.

## See also

- [`SKILL.md`](./SKILL.md): full LLM-facing instructions
- [Caveman README](../../README.md): repo overview, install, benchmarks
