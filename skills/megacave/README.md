# megacave

Caveman in Classical Chinese (文言文). Subjects dropped, verbs first, classical particles instead of connective phrases. Code, commands, paths, errors, and numbers stay exactly as they are.

**Invoke:** `/megacave` in Claude Code. `/caveman wenyan` still works as an alias.

**Characters, not tokens.** The reply on screen gets far shorter. The token bill moves much less: measured once on the 3.1.0 skill text (`evals/snapshots/results.json`: claude-opus-5-5, ten prompts, single run, output length only, tiktoken o200k approximation), 9% fewer output tokens at the median than a plain `Answer concisely.` control, ranging from 12% more to 39% fewer. See [docs/HONEST-NUMBERS.md](../../docs/HONEST-NUMBERS.md).

Example:

> 每繪新生對象參照，故重繪；以 `useMemo` 包之則免。
