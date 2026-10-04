# caveman learn

`caveman learn` shows where your coding agent's tokens go, and what to fix
first. It reads the setup files and session history already on your computer.
Nothing is sent anywhere.

You get:

- a **Setup Score** from 0 to 100 (higher is leaner),
- a list of **findings**: places your tokens go, biggest first, each with a
  suggested fix,
- a local HTML report and a JSON file.

It never edits your files. Fixes happen inside Claude Code or Codex, through
the `caveman-learn` skill, one edit at a time, and only after you say yes.

Every number is an estimate from your own history (`inferred` in JSON). No
local number is promoted to `verified`; see
[Accounting and evidence](./accounting-and-evidence.md).

## Words used in the report

The report and terminal use plain words. JSON keeps the precise names in the
right-hand column; the tables further down use them too.

| Word | What it means | Name in JSON |
|---|---|---|
| message | One request your agent sends to the model: your prompt, or one tool step. The agent re-sends its instructions and the whole conversation with every one. | turn (`tokens_per_turn`, `turns`) |
| finding | One place your tokens go, with the evidence and a suggested fix. | sink (`sinks[]`) |
| id | A finding's name, for commands like `caveman learn apply <id>`. Shown by `--all`. | `sink_id` |
| Setup Score | How lean your setup is, 0 to 100, from this computer only. Not money. Caveman Cloud's team score is a different number. | `cave_score` |
| tokens a day at your usual pace | Tokens per message times your average messages per day in the period scanned. A rate from now on, not tokens already spent. | `tokens_per_day_rate` |
| tokens so far | What a habit already used in the period scanned. | `tokens_observed` |
| estimate | Worked out from local files. Not verified, not a bill. | `basis: "inferred"` |
| safe fix | A mechanical fix exists. It is applied only if it uses fewer tokens overall. | class `reducible` |
| repeated text | The same text pasted again across sessions. Caveman memory can recall it instead. | class `recurring_context` |
| habit | A measured pattern in how your sessions run. Changing it is up to you. | class `behavioral` |
| needed | Setup you need. Shown so the picture is complete; never changed. | class `load_bearing` |
| always-loaded setup, instructions | `CLAUDE.md`, `AGENTS.md`, skill descriptions, hooks: loaded into every message. | `config_tax` |
| unused skills | Skills whose descriptions load with every message but were never used in the sessions scanned. | `dead_load` |
| context window | How much the model can hold at once. | window |
| overloaded messages | Messages where the conversation filled more than half the context window. A common rule of thumb: answers tend to get worse past that point. | `dumbzone`, `context_dumbzone` |
| how full sessions get | Each session's peak share of the context window. | `context_depth`, `peak_context_pct` |
| first-message size | What a session sends with its first message: your setup plus your first prompt. | `first_turn_tokens`, `measured_prefix_tokens` |
| Caveman memory (cavemem) | Caveman's local memory store. The agent recalls a short version of the text when it needs it, instead of pasting it again. | `cavemem_offload` |
| read from cache | Share of input the provider served from its prompt cache, which costs less than list price. How much less varies by model: usually a tenth of list price or less. | `cache_read_pct` |
| input really costs | Your input price per million tokens after caching, and its share of list price. | `effective_input_usd_per_mtok`, `effective_input_multiplier` |
| no price | A model missing from Caveman's price list. Its tokens are left out, so the real total is higher. | `unpriced` |
| points | The change in a percentage: 15% to 13% is −2 points. | percentage points |
| week of Sep 21 | A Monday-to-Sunday week, in UTC. | ISO week (`2026-W39`) |
| middle session | The median: half the sessions are above it, half below. | median |

## Privacy: what it reads and writes

Analysis runs locally. `export` writes a file for you to inspect and sends
nothing.

### Reads

| Agent | Session transcripts | Config and memory files | Root override |
|---|---|---|---|
| Claude Code | `~/.claude/projects/**/*.jsonl` | `~/.claude/CLAUDE.md`, `~/.claude/skills/*`, hook count from `~/.claude/settings.json`, plugin count from `~/.claude/plugins/installed_plugins.json`, MCP servers from `~/.claude.json` and `.mcp.json`, every `CLAUDE.md` from the current directory up to `/` | `CAVEMAN_CLAUDE_ROOT`, then `CLAUDE_CONFIG_DIR` (global config then at `$CLAUDE_CONFIG_DIR/.claude.json`) |
| Codex | `~/.codex/sessions/**/rollout-*.jsonl`, `~/.codex/archived_sessions/` | `~/.codex/AGENTS.md`, `./AGENTS.md` | `CAVEMAN_CODEX_ROOT`, then `CODEX_HOME` |
| Gemini CLI | `~/.gemini/tmp/*/chats/session-*.json` | `~/.gemini/GEMINI.md`, `GEMINI.md` from repo root to cwd | `CAVEMAN_GEMINI_ROOT` |
| opencode | `~/.local/share/opencode/storage/{session,message,part}` | none | `CAVEMAN_OPENCODE_ROOT` |
| aider | `.aider.chat.history.md` files under the root | none | `CAVEMAN_AIDER_ROOT` (required; off without it) |

The memory and rules doctor also reads `.claude/CLAUDE.md`, `CLAUDE.local.md`,
`.claude/rules/*.md` and `~/.claude/rules/*.md` (path-scoped rules with a
`paths:` frontmatter are skipped because they load on demand), and Claude Code
auto memory at `<claude root>/projects/<repo-key>/memory/MEMORY.md` plus its
topic files, honoring `CLAUDE_CODE_PROJECT_DIR_NAME`.

Two other inputs: the `session_outcomes` finding reads commit timestamps with
`git` in up to 12 repositories (3 s per call, budgeted overall), and the
`wrap_measured` block reads rows the Caveman proxy already recorded in
`caveman.db`.

### Writes

All paths are under `$CAVEMAN_HOME` (default `~/.caveman`) unless noted.
`CAVEMAN_DB` overrides the database path.

| When | What is written |
|---|---|
| Any command that builds a plan (default run, `scan`, `report`, `apply`, `applied`, `simulate`, `savings`, `export`, `experiment report`) | `caveman.db` tables `config_snapshots`, `config_snapshot_history`, `learn_sinks` |
| `caveman learn` and `learn scan` (not with `--no-remember`, which autopilot always passes) | Also: one cavemem memory per `reducible` sink (title plus suggestion) in `mem/mem.db`, and a matching row in the `learnings` table |
| `caveman learn`, `learn scan --write-report`, `learn report` | `reports/caveman-learn.html`, `reports/caveman-learn.json`, `reports/caveman-learn.YYYY-MM-DD.json` (last 8 dated snapshots kept) |
| `learn apply <sink>` without `--dry-run` | `candidates/learn-<sink>.json` |
| `learn applied` | row in `applied_fixes` |
| `learn experiment start/arm/stop` | rows in `experiments`, `experiment_arms` |
| `learn export` | `reports/caveman-learn-digest.json` (or `--out <path>`) |
| Autopilot | `runtime/learn-autopilot.json`, `.lock`, `-nudge.json`, `-nudge.inflight.json`, `-announced.json`, and its own report, snapshots and trend history under `runtime/learn-autopilot/reports/` |
| `learn autopilot on/off` | key `learnAutopilot` in `~/.caveman-cloud/config.json` |
| `learn implement` | the skill file, only if missing: `./.claude/skills/caveman-learn/SKILL.md` (Claude Code) or `$CODEX_HOME/skills/caveman-learn/SKILL.md` (Codex) |

Report files are created with mode `0600`.

## Quick start

```bash
caveman learn             # read your sessions, score them, show the top findings
caveman learn implement   # open Claude Code or Codex to review and fix, with consent
caveman learn savings     # later: what the applied fixes returned
```

## Commands and flags

The default window is `--since 30d`. The scan times out after 120 s
(`CAVE_LEARN_TIMEOUT`, seconds).

| Command | What it does |
|---|---|
| `caveman learn` | In an interactive terminal: progress, Setup Score, top findings, then a menu (implement, show all findings, open report, done). Otherwise compact text. |
| `--plain` | Compact text; no animation or menu. `CAVEMAN_PLAIN=1` or `TERM=dumb` has the same effect. |
| `--all` | Every finding with internal id, basis, evidence and suggestion; confirmed outcomes; per-repository rows. |
| `--json` | The `caveman.learn.v1` plan on stdout. |
| `--md` | Detailed Markdown. |
| `--since <Nd>` | Window, e.g. `7d`. |
| `--sources <list>` | Any of `claude,codex,gemini,opencode,aider`. Default: all. |
| `--repo <substring>` | Keep only sessions whose repository matches, before any detector runs. Config is still read from the current directory. |
| `--retro` | Adds the "would have saved" replay block (`retro`). Budgets: `--behavior-budget-ms`, `--retro-budget-ms`. Omitted when `--repo` is set. |
| `implement [claude\|codex] [--prompt "<focus>"]` | Opens the agent with the `caveman-learn` skill and the current report. |
| `apply <sink_id> [--dry-run]` | Materializes a candidate for a `reducible` or `recurring_context` sink. Edits nothing. |
| `applied <sink_id> [--fix-kind <k>] [--note <t>]` | Records an approved, re-measured fix in the outcome ledger. |
| `simulate <sink_id...>` | Token counterfactual over scanned history only. |
| `savings` | Attributed savings ledger, grouped by measurement method. |
| `experiment start\|arm\|report\|list\|stop` | On/off holdout over your own sessions. |
| `export [--out <path>]` | Privacy-reduced digest file. |
| `reconcile --usage-export <csv>` | Measured tokens vs a provider usage export. |
| `autopilot [status\|on\|off]` | Background refresh after sessions end. |
| `scan`, `report` | Lower-level passthroughs to the proxy. `report --json` prints the plan and rewrites the report files. |

Examples:

```bash
caveman learn --plain --since 7d --sources claude,codex
caveman learn --repo my-service --all
caveman learn --json > plan.json
caveman learn implement codex --prompt "only config trims"
caveman learn apply claude_md_weight:project --dry-run
caveman learn applied claude_md_weight:project --note "trimmed setup section"
caveman learn simulate claude_md_weight:project recurring_context:repaste:<fingerprint>
caveman learn savings --json
caveman learn experiment start deploy-skill --sink procedure_repeat:<key> --fix-kind skill_distillation
caveman learn experiment arm deploy-skill off
caveman learn experiment report deploy-skill
caveman learn experiment list
caveman learn experiment stop deploy-skill
caveman learn export --out ./learn-digest.json
caveman learn reconcile --usage-export ~/Downloads/usage.csv
caveman learn autopilot off
```

Notes per command:

- `apply` on a `behavioral` or `load_bearing` sink returns `applied: false`
  with a reason; there is no mechanical fix. A `recurring_context` candidate
  carries locators (file, JSONL line, block index, SHA-256), never the block
  text.
- `applied` needs a fix kind. It is taken from the sink's evidence or a
  default (`claude_md_weight`, `claude_md_sections`, `config_tax`,
  `dead_load`, `dumbzone_advice`); otherwise pass `--fix-kind`. The "before"
  value comes from the newest report snapshot older than the current scan, so
  a scan run after your edit is not used as the baseline. The edited file is
  fingerprinted at record time.
- `simulate` accepts only `claude_md_weight:*`, `claude_md_sections:*`,
  `dead_load:skills` and `recurring_context:repaste:*`. Config sinks are
  tokens per turn times scanned provider-counted turns; recurring blocks are
  occurrence sums minus the largest occurrence and one pointer per occurrence.
  Overlapping selections on the same file are counted once. No dollars, no
  forward projection.
- `experiment report` defaults its window to the experiment's start.
- `export` carries sink ids, classes, magnitudes, the score, spend totals and
  per-model rows. It carries no paths, locators, evidence maps, repository
  names or session text.
- `reconcile` matches CSV columns by header name only and fails on any
  ambiguous header. Coverage is a token comparison, not a saving.

## Autopilot

Autopilot rescans in the background and tells you about new findings without you running learn. It keeps its own report under `runtime/learn-autopilot/reports/`: its scans run with whatever working directory and environment the last session-end hook had, so they never overwrite the canonical report, dated snapshots or trend history your own `caveman learn` writes.

| Aspect | Behavior |
|---|---|
| Trigger | The Caveman native hook's `SessionEnd` event (Claude Code, Codex, Gemini CLI; opencode and Hermes bridges forward it). The hook spawns a detached, idle-priority `caveman learn autopilot run` and returns immediately. |
| Throttle | At most one scan per 6 hours. `CAVEMAN_LEARN_AUTOPILOT_HOURS` changes it. One scan at a time (lock file; stale after timeout + 60 s). |
| Scan | `learn scan --write-report --no-remember --reports-home $CAVEMAN_HOME/runtime/learn-autopilot` with the default window and sources, killed at `CAVE_LEARN_TIMEOUT`. |
| Proxy check | Before scanning, the detached child runs `caveman-proxy learn capabilities` and needs `no_remember`, `reports_home` and `memory_health`. An older proxy would ignore those flags and write cavemem and the canonical report, so it never scans: `last_error` becomes "proxy too old for autopilot (needs learn capabilities)" and `learn autopilot status` shows it. The result is cached per proxy path, mtime and size, so an upgrade re-probes. The `SessionEnd` hook itself never probes. |
| Writes | Its own report files, the `caveman.db` plan tables every scan updates, and its own `runtime/learn-autopilot*` state. Never cavemem memories or `learnings` rows: `--no-remember` skips them, because sink titles carry changing counts and each unattended run would add a near-duplicate. |
| Enable precedence | `CAVEMAN_LEARN_AUTOPILOT` env (`0`, `false`, `off`, `no` disable; anything else enables) → `learnAutopilot` in `~/.caveman-cloud/config.json` → off when `CI` is set (not `0`/`false`) or under `NODE_TEST_CONTEXT` → on. |
| Opt out | `caveman learn autopilot off`, or `CAVEMAN_LEARN_AUTOPILOT=0`. |
| State | `$CAVEMAN_HOME/runtime/learn-autopilot*.json`. Writes are temp-file + rename and refuse symlinked parents. |
| Status | `caveman learn autopilot status`: on/off and why, last scan, next scan, last error, last line shown, and a line waiting to show. |

### Session-start nudge

After a background scan, autopilot may show one line at the next session
start, for example:

```
caveman learn: new finding — <title> (~2.4k tokens in every message, estimate). Run `caveman learn` to review.
```

Rules:

- Sinks of class `reducible` or `recurring_context` with `tokens_per_turn`
  ≥ 2000 qualify. This includes `memory_health:duplicate_rules` and
  `memory_health:memory_orphans` when they are `reducible` and above the
  threshold.
- `memory_health:broken_imports` and `memory_health:memory_truncation` qualify
  with no token threshold: they break what the agent loads. When they are all
  that is new they lead the line (`caveman learn: memory files (<repo>) — <title>.
  Run \`caveman learn --all\` to review.`); otherwise they are appended as
  ", plus N memory-file findings".
- Only sinks not seen by an earlier autopilot scan. The first scan records a
  baseline and announces nothing.
- While a nudge is still unclaimed, newer sinks stay unseen and are announced
  after it.
- Shown only on Claude Code, Codex and Gemini CLI `SessionStart` with source
  `startup` or `clear`; never on `resume`, `compact` or `fork`. It is
  delivered as a user-visible `systemMessage`, not model context.
- Claimed with one atomic rename, so concurrent sessions announce it once. The
  claim parks it in `runtime/learn-autopilot-nudge.inflight.json` with the
  claimer's token; it counts as announced only when that claimer confirms,
  after the line was written to the host (the fast hook owns the token and
  confirms after relaying the delegated output). At most one nudge is in flight:
  a newer nudge waits behind an unconfirmed one. An unconfirmed nudge, for
  example after the delegate hit its 3 s timeout, is shown once more after 10
  minutes, never twice.
- Suppressed when autopilot is disabled.

## Setup Score

The Setup Score (`cave_score` in JSON) grades how lean your local setup is.
It starts at 100 and subtracts four capped penalties. The caps sum to 100.

| Component | Penalty | Cap | Measured when |
|---|---|---|---|
| `config_tax` | 50 × (config tokens/turn + recurring re-paste tokens/turn) ÷ median turn context | 35 | config tax > 0 and transcripts with usage exist |
| `dumbzone` | 50 × share of turns over 50% of the model window | 25 | any usage-bearing turn |
| `dead_load` | 40 × skill-description tokens with no detected use ÷ config tokens | 20 | sessions scanned and config tax > 0 |
| `subagent_pressure` | 20 × min(1, subagent spawns per session ÷ 5) | 20 | any session scanned |

Config tokens per turn = user `CLAUDE.md` + every `CLAUDE.md` from cwd to
`/` + Claude skill descriptions. `AGENTS.md` has its own finding and is not in
this sum. A component that cannot be measured has penalty 0,
`measured: false`, and a detail string saying why.

What it is not:

- Not money. It carries no currency and is not derived from spend.
- Not billed usage. It is transcript and file arithmetic.
- Not the Caveman Cloud Cave Score, which scores organization traffic on a
  different scale. The two will not match.

The terminal and TUI show the score only once at least one block has
recurred across three or more sessions (a `recurring_context` sink exists).
Before that, learn prints the session count and top findings without a score.
`--json` always includes it.

## Sink reference

Sinks are ranked: forward-rate sinks first by tokens per day, then historical
sinks by observed tokens.

Classes (the report's label in brackets):

- `reducible` ("safe fix"): a mechanical fix exists and must pass the
  net-token-negative gate.
- `recurring_context` ("repeated text"): content re-established across
  sessions; fix is a cavemem offload.
- `behavioral` ("habit"): a measured habit. Numbers are stated; suggestions
  are soft.
- `load_bearing` ("needed"): measured config you need. Listed so the score
  stays honest; never edited.

| Sink id | Class | Measures | Fix kind | Auto-fixable |
|---|---|---|---|---|
| `config_tax:baseline` | load_bearing | Config tokens loaded every turn (see score); measured turn-1 prefix when available. Plugin tokens are not measured. | none | No |
| `claude_md_weight:{user,project,codex}` | reducible | A `CLAUDE.md` (or `~/.codex/AGENTS.md` for `codex`) over 150 lines or 2000 tokens | `claude_md_weight` | Yes, via skill |
| `claude_md_sections:{user,project}` | reducible | `CLAUDE.md` sections with no distinctive-line echo in ≥5 sessions. Emitted only if at least one other section did echo. | `claude_md_sections` | Yes, via skill |
| `dead_load:skills` | reducible | Skills whose descriptions load every turn with no use detected in the window | `dead_load` | Yes, via skill |
| `recurring_context:repaste:<fingerprint>` | recurring_context | A block of ≥180 tokens re-established in ≥3 sessions. Top 24 listed; the rest still count in the score. | `cavemem_offload` | Yes, via skill |
| `context_dumbzone` | behavioral | Turns over 50% of the model window, if ≥10% of turns | `dumbzone_advice` | No |
| `subagent_overuse` | behavioral | Subagent spawn counts | none | No |
| `subagent_spend` | behavioral | Share of provider-counted context in subagent sidechains (≥100k tokens, ≥3 sessions). Visibility only. | none | No |
| `config_surface` | behavioral | Hooks + plugins ≥ 10 | none | No |
| `mcp_surface` | behavioral | ≥3 configured MCP servers whose schemas load each turn | none | No |
| `cross_provider:depth` | behavioral | Median session peak depth compared across agents (needs ≥2 sources; skips assumed windows) | none | No |
| `cross_provider:repaste` | behavioral | One recurring block seen in two or more agents | none | No |
| `learning_loop:error_loop:<session>:<hash>` | behavioral | Same tool call ≥3 times in one session, at least half failing | none | No |
| `learning_loop:refetch_loop:<session>:<hash>` | behavioral | Same call ≥3 times with only pagination/limit variants, no errors | none | No |
| `cache_churn` | behavioral | ≥2 sessions with ≥3 turns that each re-wrote >10k tokens and >25% of context to cache | none | No |
| `cache_efficiency` | load_bearing, or behavioral when multiplier ≥ 0.6 | Effective input cost per million tokens after cache reuse (needs ≥200k input tokens). A rate, not a volume. | none | No |
| `reread_waste` | behavioral | Repeated full reads of the same file (≥5k tokens) | none | No |
| `compaction_churn` | behavioral | Files re-fetched after compaction (≥2k tokens). May overlap `reread_waste`; never sum them. | none | No |
| `tool_output_portfolio` | behavioral | Tool-output tokens grouped by call shape (local estimate) | none | No |
| `session_outcomes` | behavioral | Share of tokens in sessions with no commit between session start and 45 min after its end (≥8 sessions, ≥50k tokens). Correlational. | none | No |
| `procedure_repeat:<key>` | behavioral | A 3–6 step tool-signature sequence repeated in ≥3 sessions (≥15k tokens) | `skill_distillation` | No; holdout only |
| `config_growth` | behavioral | Growth (≥400 tokens and ≥20%) in always-loaded config versus this machine's snapshot history | none | No |
| `memory_health:duplicate_rules:{claude,codex,gemini}` | reducible | Rules (≥6 words) loaded from more than one file per agent session | `dedupe_rules` | Yes, via skill |
| `memory_health:memory_orphans:<memory-dir hash>` | reducible when index links to missing files load tokens, else behavioral | Memory files `MEMORY.md` never links; index links to missing files | `memory_index_repair` | Via skill, per item |
| `memory_health:memory_truncation:<memory-dir hash>` | behavioral | `MEMORY.md` lines past the session-start cutoff | `memory_index_condense` | Via skill, per item |
| `memory_health:broken_imports:<file-hash>` | behavioral | `@import` paths that resolve to missing files | none | Via skill, per item |
| `memory_health:stale_references:<file-hash>` | behavioral | Backticked repo paths in project instruction files that no longer exist | none | Via skill, per item |
| `memory_health:buried_rules:<file-hash>` | behavioral | ≥3 `NEVER`/`ALWAYS`/`MUST`/`IMPORTANT`/`CRITICAL` lines in the middle 30–70% of a file over 150 lines. Heuristic. | none | Via skill, per item |

The `memory_truncation` cutoff follows the Claude Code memory docs
(https://code.claude.com/docs/en/memory, checked 2026-09-27): "The first 200
lines of MEMORY.md, or the first 25KB, whichever comes first, are loaded at
the start of every conversation."

`<file-hash>` is the first 8 hex characters of a hash of the file path.

Some sinks carry `spend_usd_per_day` or `spend_usd_observed`. That is what the
context cost at your measured effective input rate over the window. It is not
what removing it would return.

## Trends

The `trends` block compares your own sessions week over week.

| Aspect | Rule |
|---|---|
| Buckets | UTC ISO weeks (`2026-W39`), up to 8, inside the window. Sessions are bucketed by start time; undated sessions are counted in `undated_sessions` and left out. |
| Compared week | The last complete week (`current_week`). The week in progress is shown (`in_progress: true`) but never drives the comparison. No trends block when the window holds no complete week. |
| Prior | Sessions of up to 4 weeks before the compared week, pooled. |
| Minimum | A bucket with fewer than 5 sessions has a null value and `insufficient_data`. |
| Direction | `improved`, `worse`, `flat` or `insufficient_data`. Flat when the relative change is under 10%, or, for percent and per-100 metrics, the absolute change is ≤1 point. |
| Partial weeks | `partial: true` marks a week cut by the window start or still in progress. |

Metrics:

| Key | Statistic | Better |
|---|---|---|
| `tokens_per_session` | median | lower |
| `peak_context_pct` | median | lower |
| `dumbzone_turn_pct` | pooled share of turns | lower |
| `first_turn_tokens` | median | lower |
| `cache_read_pct` | pooled share of context | higher |
| `tool_errors_per_100_turns` | pooled rate | lower |
| `score_session_penalty` | recomputed dumbzone + subagent penalty | lower |

Only the session-derived score components are recomputed per week;
`config_tax` and `dead_load` depend on today's config and are not replayed.
Full score history comes from the dated report snapshots
(`score.history`). `movers` lists up to three sinks that grew and three that
shrank by ≥10 tokens/turn versus the newest snapshot at least 7 days old (else
the newest one).

A trend is not a saving and does not show what caused the change.

## Fixing

### The consent loop

`caveman learn implement` (or the TUI's "Implement with agent") opens Claude
Code or Codex with the `caveman-learn` skill, installing it if missing. The
skill:

1. Reads `caveman learn report --json` and shows the score, components and
   ranked sinks.
2. For each sink you choose, proposes one concrete diff with before and after
   tokens per turn, and asks yes or no.
3. Applies only approved edits, with the agent's own file tools.
4. Re-measures. **Net-token-negative gate:** if tokens per turn did not drop,
   the edit is reverted.
5. For a cavemem offload, also checks that `caveman mem recall` returns the
   content and a pointer is in place; otherwise it reverts (`caveman mem
   forget <id>` and restores the source).
6. After an edit passes its gate, runs `caveman learn applied <sink_id>`.

`load_bearing` sinks are never edited. `procedure_repeat` candidates skip the
gate and go to an experiment instead, because a skill adds prefix tokens every
session and pays back only on some.

You can install the skill directly: `caveman tools skills install caveman-learn`
(add `--user` for all repos, `--agent codex` for Codex).

### Applied ledger and verdicts

Each later scan compares sessions after `applied_at` with the recorded
before-value and reports a row in `confirmed`:

| Fix | After-value | Verdict rule (needs ≥3 sessions after) | Method |
|---|---|---|---|
| `claude_md_weight`, `claude_md_sections`, `dead_load`, `config_tax` | Current token count of the same file, sections or skills | lower = `improved`, higher = `regressed`, equal = `unchanged` | `deterministic_remeasure` |
| `cavemem_offload` | Whether the block's fingerprint recurs after the fix | absent = `improved`, present = `unchanged` | `interrupted_time_series` |
| `dumbzone_advice` | Share of turns over 50% of the window | ≥5 points lower = `improved`, ≥5 higher = `regressed` | `interrupted_time_series` |
| Anything else | none | always `insufficient_data` | `unattributed` |

Provenance compares the file's SHA-256 with the fingerprint taken at
`applied` time: `intact`, `changed_since` (someone edited past the fix),
`target_missing`, or `not_fingerprinted`. `changed_since` and `target_missing`
force confidence to `low`.

### Savings

`caveman learn savings` lists every recorded fix, including regressions,
grouped by measurement method (rung), strongest first:

| Rung | Method | Confidence |
|---|---|---|
| 4 | `deterministic_remeasure`: the edited file was re-counted | high |
| 3 | `controlled_holdout`: change on vs off on this machine | high |
| 2 | `counterfactual_replay`: scanned history re-run with the change | medium |
| 1 | `interrupted_time_series`: before vs after sessions, no control | low |

Rungs are never summed together, and there is no blended headline. Each row
carries standing confounders for its method. Only positive savings are
priced, as a per-day rate over the scanned window at your measured effective
input rate, when the plan has a `spend` block. `total_saved_usd_by_rung` sums
within one rung only. Nothing is projected to a month or labeled verified.

### Experiments

A holdout compares arms over your own sessions:

1. `experiment start <label> [--sink <id>] [--fix-kind <kind>] [--note <t>]`
   opens the `on` arm.
2. `experiment arm <label> off` (later `on` again) switches arms. Switching to
   the running arm is a no-op.
3. `experiment report <label>` assigns each session to the arm active at its
   start and compares median tokens per session.
4. `experiment stop <label>` closes it.

| Verdict | Condition |
|---|---|
| `insufficient_data` | Either arm has fewer than 5 sessions (method drops to `unattributed`) |
| `improved` | on-arm median ≥10% below off-arm |
| `regressed` | on-arm median ≥10% above off-arm |
| `unchanged` | otherwise |

When the on-arm has more than 1.25× the off-arm's tool errors per turn, the
report says so. Arms run at different times on different work, so the result
is still `inferred`.

### Reverting

Every edit is made by the skill and reported exactly, so it can be undone with
your own tools or version control. An offload reverts with
`caveman mem forget <id>` plus restoring the trimmed text. A `regressed`
verdict is shown with no dollar figure; the skill offers the revert path.

## Output files and JSON

`caveman learn --json` and `caveman learn report --json` print one document
with `schema: "caveman.learn.v1"` and `basis: "inferred"`. The same plan, plus
`moves`, `generated_at` and `generation`, is saved as
`reports/caveman-learn.json`. The Go types are in
[`proxy/internal/store/learn_types.go`](../../proxy/internal/store/learn_types.go).

| Block | Present | Content |
|---|---|---|
| `window` | always | `from`, `to`, `since` |
| `cave_score` | always | `score`, `scope: local_setup`, four `components` |
| `sessions_scanned`, `sessions_by_source` | always | counts |
| `sinks` | always | ranked sinks: `sink_id`, `class`, `basis`, `framing` (`forward_rate` or `historical`), token fields, `evidence`, `suggestion` |
| `caveats` | always | plain-language limits for this run |
| `spend` | usage for a cataloged model exists | window cost at dated catalog rates, per-component and per-model rows, `unpriced` models, effective input rate and multiplier |
| `confirmed` | a fix was recorded | ledger rows with verdict and attribution |
| `trends` | a complete week with dated sessions exists | see Trends |
| `context_depth` | usage-bearing sessions exist | peak-depth histogram in 10% buckets |
| `wrap_measured` | the proxy recorded savings in the window | tokens only, proxy-counted |
| `retro` | `--retro` and something to report | replay totals by family |
| `portfolio` | sinks share a fix family | grouping of existing sinks, no new estimate |
| `repos` | ≥2 repositories with ≥2 sessions each | per-repository summary |

Other subcommands print their own schemas: `caveman.learn.savings.v1`,
`caveman.learn.simulate.v1`, `caveman.learn.experiment.v1`,
`caveman.learn.digest.v1`, `caveman.learn.reconcile.v1`.

`spend` is what the scanned window cost at published rates. It is a floor
when `unpriced` is non-empty, it is not an invoice, and on a subscription plan
the marginal cost is zero. The compact plain view folds several unpriced models
into one line; `--all`, `--md`, JSON and the HTML report list each model.

## Limits and known gaps

- **Headless sessions can consume the nudge.** A scripted `claude -p` or
  `codex exec` fires `SessionStart` with source `startup`, so it can claim and
  confirm the nudge where no one sees it.
- **Unsupported agents.** Only the five sources above are scanned. Cursor,
  Windsurf, Hermes, Pi and other agents are not read.
- **aider** is off unless `CAVEMAN_AIDER_ROOT` points at a directory to walk.
  aider history has no provider usage, so it adds nothing to depth, dumbzone,
  spend or trends.
- **Context windows.** A window comes from the shared model catalog. For
  un-cataloged models learn assumes 1M if the model id contains `1m`, 400k
  for OpenAI, 1,048,576 for Gemini, and 200k otherwise. If a session's context
  grows past the assumed window, that whole session is re-measured against 1M
  (then 2M). A 1M-window session that never exceeds 200k is still measured
  against 200k. Assumed windows add a caveat and are excluded from the excess
  token floor and from `cross_provider:depth`.
- **Turn-1 prefix** includes the first user prompt, so the measured prefix is
  an upper bound.
- **Plugins** are counted but their tokens are not measured.
- **`claude_md_sections`** sees only visible transcript text. Claude Code
  injects `CLAUDE.md` into the system prompt, so the finding is emitted only
  when at least one section echoes (a positive control).
- **`--repo`** drops Gemini sessions (their transcripts have no working
  directory) and omits `retro`.
- **Auto memory** location ignores a custom `autoMemoryDirectory` setting.
- **Time budget.** With `--retro`, a scan that hits its budget covers partial
  history; repository, section-echo, trend and applied-fix blocks are omitted
  rather than zero-filled.
- **Ledger coverage.** `memory_health` fixes can be recorded with `applied`
  but have no after-value measurement yet, so they stay `insufficient_data`.
  `simulate` does not accept `memory_health` sinks.
- **Savings rungs.** Confirmed ledger rows are currently produced only at the
  `deterministic_remeasure` and `interrupted_time_series` rungs.
  `controlled_holdout` results appear in `experiment report`, not in
  `savings`, and no command yet produces `counterfactual_replay` rows.
