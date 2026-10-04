# caveman

Talk like smart caveman. Same brain, fewer tokens.

## What it does

Makes the model answer first and cut the ceremony: no greeting, hedging,
filler, recap, or closer. Articles drop when the sentence still reads in one
pass. Code, commands, paths, numbers, and exact error strings never change.
Result depends on model and workload. Measured once (`evals/snapshots/results.json`: claude-opus-5-5, ten prompts, single run, output length only, tiktoken o200k approximation): caveman 3% fewer output tokens at the median than a plain `Answer concisely.` control, inside the noise; ultracave 35%; megacave 9%. No quality-equivalence claim is published. Mode persists until changed or stopped.

Caveman is one of three sibling skills:

| Skill | Command | What change |
|-------|---------|-------------|
| `caveman` | `/caveman` | The voice. Default. Answer first, fluff gone, every fact kept. |
| `ultracave` | `/ultracave` | Grammar stripped. Fragments, one word when one word is enough, each fact once. |
| `megacave` | `/megacave` | Classical Chinese (文言文). Far fewer characters, technical terms verbatim. |

`/caveman ultra` and `/caveman wenyan` still work as aliases.

Auto-clarity: every skill drops to normal prose for security warnings,
irreversible-action confirmations, step sequences a fragment could scramble,
and a user who repeats a question. Resumes after the clear part.

## How to invoke

```
/caveman              # the voice (default)
/caveman status       # show current mode without changing it
/ultracave            # grammar stripped
/megacave             # classical Chinese
stop caveman          # back to normal prose
```

Claude Code and the standalone OpenCode plugin read stored mode state. Other
hosts report the mode known in the conversation, or `unknown` if none is known.

Want Claude Code sessions to start with normal prose? Set
`{"defaultMode":"manual"}` in `.caveman.json` for one project or
`~/.config/caveman/config.json` for your user. You can also set
`CAVEMAN_DEFAULT_MODE=manual`. Then `/caveman` activates the voice and
`/ultracave` selects ultracave. Stop persists across compaction and resume; a
fresh session starts off again. This startup policy is Claude Code-only:
OpenCode keeps its caveman default because the installer also supplies static
activation rules.

## Example output

Question: "Why does my React component re-render?"

Normal prose:
> Your component re-renders because you create a new object reference each render. Wrapping it in `useMemo` will fix the issue.

caveman:
> New object ref each render, so React re-renders. Wrap the prop in `useMemo`.

ultracave:
> Inline object prop, new ref, re-render. `useMemo`.

megacave:
> 每繪新生對象參照，故重繪；以 `useMemo` 包之則免。

## See also

- [`SKILL.md`](./SKILL.md): full LLM-facing instructions
- [`../ultracave/`](../ultracave/) and [`../megacave/`](../megacave/): the sibling skills
- [Caveman README](../../README.md): repo overview, install, benchmarks
