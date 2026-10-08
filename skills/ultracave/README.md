# ultracave

Caveman with the grammar stripped. Fragments. One word when one word is enough. Each fact once.

Same floor as caveman: code, commands, paths, errors, numbers, and negations are never touched. The difference is everything around them.

**Invoke:** `/ultracave` in Claude Code. `/caveman ultra` still works as an alias.

**Use it when** you read fast and want payload only.

**Skip it for** onboarding, security review, and anything other humans will read. Ultracave drops to plain prose on its own for warnings and irreversible actions.

Measured once on the 3.1.0 skill text (`evals/snapshots/results.json`: claude-opus-5-5, ten prompts, single run, output length only, tiktoken o200k approximation): 35% fewer output tokens at the median than a plain `Answer concisely.` control. Length only, not correctness. See [docs/HONEST-NUMBERS.md](../../docs/HONEST-NUMBERS.md).
