# Token Stats — Design Proposal

> **Status:** design proposal — implementation TBD pending maintainer feedback. Tracks #251.
>
> The user in #251 burnt ~35% of their 5-hour Claude limit on two small tasks and had no way to tell whether caveman was actually configured / active / saving anything. This document proposes how to give caveman users a real, local "tokens saved" readout without forcing them to trust the marketing number on the README.

## 1. Goals & non-goals

### Goals

- **Self-serve diagnostic.** A user who suspects caveman is misconfigured should be able to run a single command and see "your last N sessions saved X tokens" — independent of any vendor billing dashboard.
- **Per-mode breakdown.** Show savings broken down by intensity level (`lite`, `full`, `ultra`, `wenyan*`) so users can pick the level that fits their workload.
- **Local only.** Stats live on the user's disk. Caveman ships locally; the stats subsystem does the same. No telemetry, no opt-in network call, no third-party endpoint.
- **Off by default until trivially enabled.** Adding 200 lines of always-on hook overhead to a tool that already runs on every prompt is a regression. Feature flag controls whether the hook records anything.

### Non-goals

- **Per-call attribution.** We don't try to count "tokens saved on this exact prompt." Token counts are estimates; the meaningful unit is rolling savings over many turns.
- **Cross-machine sync.** This is not a SaaS dashboard. If a user wants a unified view across machines they can rsync `~/.claude/.caveman-stats.jsonl` themselves.
- **Vendor billing reconciliation.** We can't see Anthropic's billed token counts. Stats are a model-side estimate using `tiktoken`, not authoritative billing.

## 2. Proposed UX

```bash
$ caveman stats
─── caveman tokens saved ──────────────────
Last 7 days
  prompts:        128
  output tokens:  41,203 (~10,300 saved vs estimated baseline)
  by mode:        full 71% · ultra 24% · lite 5%
  est. cost saved $0.12 (Sonnet 4.x output rate, 2026 pricing)

Last 30 days
  prompts:        612
  output tokens:  198,440 (~52,800 saved)

$ caveman stats --json   # for piping into a status bar / CI
{ "window_days": 7, "prompts": 128, ... }

$ caveman stats --reset  # nuke ~/.claude/.caveman-stats.jsonl
```

Optional second view: `caveman stats --session` reports just the current Claude Code session (matched by `session_id`).

## 3. How tokens get counted

Two viable points to instrument:

### Option A — `UserPromptSubmit` + `Stop` hook pair (preferred)

Claude Code already invokes `caveman-mode-tracker.js` on every `UserPromptSubmit`. Add a complementary `Stop` (or `PreCompact`) hook that fires when the assistant finishes a turn. The Stop hook receives the assistant's response transcript on stdin.

Flow:

1. `UserPromptSubmit` records `(session_id, turn_id, user_prompt_tokens, mode, t_start)`.
2. `Stop` records `(session_id, turn_id, assistant_response_tokens, t_end)`.
3. Each line appended to `~/.claude/.caveman-stats.jsonl` is one finished turn.

Token counting is offline via `tiktoken` (already a transitive dep through `evals/measure.py`). For text where `tiktoken` is unavailable (Windows-only Python install), fall back to a 4-chars-per-token estimate — labeled as estimated in the output.

### Option B — wrap the compress flow only

If maintainers want to ship the smallest possible increment first: only count tokens through `caveman-compress`. The compressor already reads `original_text` and `compressed`; emitting `(original_tokens, compressed_tokens, ratio)` to the stats file is ~5 lines.

This catches input-side savings (the README claims ~46% on input) but misses the output-side savings, which is the bigger story and the one the user in #251 actually cares about.

**Recommendation: Option A.** Option B is too narrow to answer "is caveman working?".

## 4. Storage format

Append-only JSONL at `${CLAUDE_CONFIG_DIR:-~/.claude}/.caveman-stats.jsonl`. One line per finished turn:

```json
{"ts":"2026-04-26T08:13:42Z","session_id":"abc-123","mode":"full","prompt_tokens":820,"response_tokens":312,"baseline_response_tokens":1240,"saved":928,"counter":"tiktoken-cl100k_base"}
```

- **Append-only.** No mutation, easy to recover, easy to ship to another tool.
- **One file, no SQLite.** Keeps the dependency surface flat. JSONL parses everywhere. Rolling read of last 30 days is fine on the scales we expect (~10,000 lines = ~2MB).
- **Rotation.** When the file crosses ~5MB the `caveman stats` reader rotates it to `.caveman-stats.jsonl.1` and starts a fresh file. No background process required.
- **`baseline_response_tokens`** is computed from the response by adding back articles / filler / common verbose patterns — a heuristic, not a measurement, but spelled out in the help text so users know what the "saved" number means.

`session_id` is the same scoped identifier introduced in #184 / PR #291, so per-session and aggregated views share a key.

## 5. Configuration

Mirror the existing `caveman-config.js` resolution order:

1. `CAVEMAN_STATS=on|off` env var (highest priority).
2. `stats: true` field in `${XDG_CONFIG_HOME}/caveman/config.json`.
3. Default: **off**.

Off ≠ no-op. With `stats=off`, the hook still parses stdin (it has to anyway, to extract `session_id` and `prompt`) but skips the JSONL append. This keeps the per-prompt overhead measured-and-bounded.

## 6. CLI surface

Add `caveman` as a thin Node entry-point in `bin/caveman` (or extend `cli.py` if Python is preferred — the rest of `compress` is Python). Minimal subcommands:

| Subcommand | What |
|------------|------|
| `caveman stats` | Default 7-day summary, human-readable. |
| `caveman stats --window 30d` | Custom window. |
| `caveman stats --json` | Machine-readable for status bars / CI. |
| `caveman stats --session` | Current session only. |
| `caveman stats --reset` | Truncate the JSONL after a `y/N` confirm. |

Anything more (graphs, weekly digests, badge generation) is out of scope for v1.

## 7. Should this live with `compress` or separately?

Separate. `compress` is an episodic, opt-in transform on a single file; stats is a passive recorder that runs on every turn. Bundling them couples a high-frequency hook to a tool that's used once a session. Keep `compress` lean, ship stats as its own subsystem under `skills/stats/` (or `hooks/stats.js` if it's purely hook-shaped).

## 8. Privacy

- The JSONL file contains `session_id`, mode, and **token counts** — not prompt or response content.
- The file is written `0600` (matching the existing flag-file safety pattern).
- No network egress. No telemetry. The only way data leaves the machine is if the user explicitly shares the file.

## 9. Risks / open questions

- **`tiktoken` accuracy across model families.** The cl100k_base encoder is a reasonable proxy for Sonnet/Haiku 4.x but not exact. Worth labeling the count as "estimated" rather than "actual."
- **Hook ordering with other plugins.** A `Stop` hook from another plugin that exits non-zero would prevent ours from firing. Guard with explicit `try/catch` and consider using `PostStop` instead if Claude Code adds it.
- **Baseline estimation.** "Tokens saved" requires inventing a counterfactual — what would the response have looked like *without* caveman? The simplest defensible answer: compare against the historical mean response length on the same model when caveman is `off`. That requires the user to have run the model both with and without caveman on similar workloads. For users who immediately install caveman and never run without, fall back to a fixed 4x baseline (matching the README's "75% saved" headline) and label it as estimated.
- **Cross-platform JSONL append safety.** Append from two concurrent Claude Code sessions on the same machine could interleave bytes. Mitigation: open with `O_APPEND` (atomic on POSIX up to PIPE_BUF, fine for our ~200-byte rows) and on Windows, fall back to `appendFileSync` which is good enough at our throughput.

## 10. Suggested rollout

1. **Phase 1 — opt-in compress-only counter.** Ship Option B behind `stats=on`. ~50 LOC. Low risk. Lets users see ratios on the input side.
2. **Phase 2 — output-side counter via Stop hook.** Add the `UserPromptSubmit` + `Stop` pair. ~150 LOC plus tests.
3. **Phase 3 — `caveman stats` CLI.** ~100 LOC. Read-only, can be released independently from the hook.

Each phase is independently mergeable and reverts cleanly. No single big-bang PR.

---

## Asks of maintainers

Before any code lands, three calls would help:

1. **Is opt-in by default acceptable, or must this be off-by-default for plugin install paths and opt-in only via env var?** I'd argue opt-in (config flag) is fine because the data never leaves the machine, but it's the maintainer's call.
2. **Python or Node for the `caveman stats` CLI?** The `compress` skill is Python; the hooks are Node. Either is fine, but cross-language plumbing for a stats reader is not.
3. **Baseline assumption:** is the fixed 4x baseline OK, or should we require users to run a no-caveman calibration period first? The first is simpler; the second is more honest.

Closing on these three would unblock Phase 1.
