# Caveman terminal UX

This file defines presentation contract for whole CLI. Command behavior, safety
gates, machine output, and exit codes remain owned by code and product specs.

## Direction

Use bounded inline terminal UI: short sections, status panels, progress while
work runs, and one keyboard picker when choice is required. No full-screen app,
alternate-screen buffer, or persistent dashboard.

Clack powers interactive `learn`. It is bundled at build time into a lazy
25 KB chunk, so the published package still has zero runtime dependencies and
ordinary command startup does not load the UI library.

Every human command answers, in order:

1. What happened?
2. What matters?
3. What should I do next?
4. Where can I inspect full detail?

Default output stays bounded. Detail never disappears; it moves behind `--all`,
`--verbose`, `--json`, `--md`, or named subcommands.

## Surface map

- `caveman` / `--help`: four porcelain jobs, grouped as run, understand,
  connect, and more.
- `caveman <agent>`: quiet launch banner. Show mode, first blocking/off state,
  newly installed loadout only, then agent. No repeated capability dump.
- first run (once per machine, TTY only; replay via unprinted `caveman
  welcome`): wordmark brighten-in, spinner while the 30-day retrospective scan
  runs (read-only over local session logs), count-up reveal of tokens sent /
  would-have-cut with family breakdown and the inferred/tokens-only label,
  telemetry disclosure line, then one `[y/N]` account question. Any failure or
  empty history degrades to one dim line; the wrapped agent always launches.
  Non-TTY, `CAVEMAN_PLAIN=1`, and `TERM=dumb` skip the whole moment silently.
- `caveman learn`: animated scan, Setup Score card, source/session scope, at
  most three top-move cards, grouped recurring context, protected load-bearing
  baseline, then keyboard actions: implement, details, report, done. Full sink
  ids and detector detail live under `--all`; `--plain` disables interactive UI.
  Current proxy writes visual report from same plan, so front door performs one
  full analysis. Portfolio output promotes one best next move: its concrete top
  sink is the title, its fix label is the kind, and its measurement confidence
  remains visible. Recorded fixes appear in a confirmed section with application
  date, before/after unit, post-fix session count, and longitudinal verdict.
  TUI confirmed-count note points to `caveman learn --all`, which also adds a
  per-repository block. That block reports session count, dumbzone percentage,
  and median context; it does not invent per-repository scores.
- `caveman learn implement`: select Claude Code or Codex, install missing
  `caveman-learn` guide, then launch agent with current report and optional user
  focus. Agent asks before every edit. Load-bearing findings are never edited.
- `caveman status`: today, off states, account/config state, one next action.
- `caveman tools`: local commands grouped by think, remember, execute, inspect.
  Default stays at 15 entries; advanced internal surfaces use
  `caveman help tools --all`.
- `caveman cloud`: connected commands grouped by account, evidence, governance,
  with login as clear starting action.
- errors: one problem, one likely correction, one help pointer. No stack trace.

## Output contracts

- Machine output stays pipe-safe. JSON, Markdown, compressed bytes, recipes, and
  delegated command output receive no decoration or prompt.
- Interactive UI requires stdin, stdout, and stderr TTYs. Non-TTY paths keep
  stable compact text.
- `--plain`, `CAVEMAN_PLAIN=1`, or `TERM=dumb` disables animation and keyboard
  input. `NO_COLOR` disables color.
- Local savings remain `inferred`, currency-free in learn, and never projected
  from daily to monthly.
- `tokens/day` is a forward rate. `tokens observed` is a historical window
  total. Learn renders both separately and never adds them together or describes
  observed totals as rates.
- Unknown values stay absent. No synthetic zeros or guessed state.

## Measurement

Disclosed opt-out `cli/v1` events cover command exposure/outcome/failure plus
content-free aggregate local Engine sessions; CI/non-TTY defaults off and all
kill switches apply. `implement` is an allowlisted subcommand.
Telemetry never includes prompts, argv values, report contents, sink ids, paths,
provider/model/account/request/session IDs, hashes, or dollars. Owner reviews
adoption and errors alongside local ClickHouse/runtime evidence; no product
decision uses anonymous telemetry alone.
