# Why Caveman

## Mission

Make AI coding agents spend fewer tokens without getting worse. An agent should
say less, read less, and redo less, while code, commands, paths, and error text
stay exact. "Why many token when few do trick."

Caveman started as one skill that makes the agent answer in a terse voice. Two
outside results shaped what it grew into:

- Adobe Research found that compressing the *user's prompt* makes answers worse.
  So Caveman never touches the prompt.
- JetBrains found that agent sessions are mostly code and tool calls, which a
  voice leaves alone. So Caveman added parts that shrink what the agent *reads*
  (logs, JSON, test output, web pages, tool catalogs, memory files) and what it
  *runs on* (model and effort choice, cached prompts, reusable scripts).

Each part has its own switch, runs on the user's machine with the user's keys,
and can be turned off without breaking the others.

## Standing rulings

These hold until changed here.

1. **The whole repository is Apache-2.0**, from 3.0.0 on. Caveman Cloud, the
   hosted service, is separate software and not in this repository
   ([LICENSING.md](../LICENSING.md)).
2. **Only measured numbers get published.** A reduction or quality claim needs
   a committed run with its method: `evals/`, `benchmarks/`, or a write-up in
   `docs/`. Nothing is rounded up. Losing rows stay in the table, and
   [HONEST-NUMBERS.md](HONEST-NUMBERS.md) lists every case where Caveman costs
   more than it saves.
3. **"Answer concisely." is the baseline.** Current models already follow it, so
   the voice is measured against that instruction, not against no instruction.
4. **Every number says how it was made.** `inferred` for local estimates,
   `provider_reported` for usage the provider returned,
   `benchmark_counterfactual` for controlled runs, and `verified` only for a
   named, enforced method ([accounting](technical/accounting-and-evidence.md)).
5. **The user's prompt is never rewritten**, by the voice or by any compressor.
6. **Compression is recoverable or it does not happen.** A lossy transform is
   emitted only after the original bytes are stored behind a handle the agent
   can retrieve. On a parse problem, no size win, or no store, the input passes
   through unchanged.
7. **Local first.** The skills, the CLI's local commands, the Engine, the proxy,
   and the recovery tools need no account. Only Auto (model routing) needs a
   free sign-in, and nothing is sent to Caveman Cloud until the user signs in
   and picks it.
8. **The CLI sends usage telemetry by default** so a project maintained by one
   person knows what to build next. It never carries prompts, code, file paths,
   or scripts, and one command (`caveman telemetry off`, or `DO_NOT_TRACK=1`)
   turns it off for good. [SECURITY.md](../SECURITY.md) lists every field.
9. **Changes to a user's setup happen only on their yes.** `caveman learn`
   measures and proposes; edits are applied one at a time after consent. Blocks
   suggest a saved script but never deny or rewrite a command.
10. **Adding an agent is data, not code**, where the agent's wire protocol and
    hooks allow it: one JSON profile, checked by a fail-closed compiler. Changes
    to the compiler's allowlists need the founder's review
    ([CONTRIBUTING_PROFILES.md](CONTRIBUTING_PROFILES.md)).
11. **The README speaks caveman on purpose.** It is the front page for people
    deciding whether to install, so it stays readable by non-programmers and
    every install command in it works.

## Non-negotiables

- Code, commands, paths, URLs, numbers, and error messages are never altered by
  the voice or by a compressor.
- The voice drops to full sentences for security warnings, irreversible
  actions, and confused users, then resumes.
- If Caveman is net-negative for a workload (short Q&A, per-request billing),
  the docs say so and tell the user to turn it off.
- Hooks and plugins never block or break the host agent. They fail silent and
  fall back to the agent's normal behavior.
- Uninstall removes what Caveman installed and leaves the user's own files and
  edits in place.
