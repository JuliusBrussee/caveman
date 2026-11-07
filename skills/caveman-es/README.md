# caveman-es

Caveman in Spanish. Articles, filler, and courtesies dropped; fragments allowed; short synonyms; technical terms exact; code, commands, paths, errors, and numbers untouched.

**Invoke:** load the skill by Spanish trigger (`modo cavernícola`, `habla breve`, `menos tokens`). There is no `/caveman-es` — `VALID_MODES` has no `es` entry, so no statusline badge and no subagent propagation. Session persistence with the hook is the `caveman` mode writing in Spanish.

**Numbers:** no Spanish eval snapshot is committed. The pairing with #1383 (`evals/prompts/es.txt`) is the natural place to measure it; that PR is open. This skill claims no measured saving.

Example:

> `useState` no es deconstruido. `dispatch` estable, referencia nueva cada render → `[CAVEMAN]` no se dispara. `useMemo` lo envuelve.

See [CLAUDE.md](../../CLAUDE.md) for the mode/hook contract (`VALID_MODES`, legacy level mapping, `sync-skill.yml` mirror list) before extending this skill.
