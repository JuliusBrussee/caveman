# caveman learn v2: from setup linter to smart multi-provider optimizer

Status: plan. Written 2026-08-16 against `optimized` @ 766dce6.

Where we are: `caveman learn` is a solid, honest **setup linter**. The analyzer
(`proxy/internal/store/learn.go` + friends) scans Claude Code and Codex
transcripts, computes a Cave Score from four capped penalties, ranks token
sinks, and the `caveman-learn` skill applies fixes with per-edit consent and a
net-token-negative gate. The honesty architecture (inferred-only, no dollars,
no projection, locator-not-body, under-claim) is the best part — nothing below
weakens it.

What it is not yet: an optimizer. Detectors are independent static-threshold
checks; the biggest number (config tax) is a bytes/4 guess when a
provider-counted truth sits in the same transcripts; two of ~30 supported
agents are scanned; and nothing ever measures whether an applied fix worked.
It diagnoses once — it does not learn.

## Gap table

| Gap | Where | Today |
|---|---|---|
| Config tax is bytes/4, never provider-counted | `config_scan.go:60`, `learn.go:314` | turn-1 `cache_creation+input` usage in the same JSONL would measure the real prefix exactly — unused |
| Only claude + codex scanned; codex misses repaste | `learn.go:608-617` | Gemini CLI, opencode, OpenClaw, aider: zero coverage; `SessionsBySource` already generalizes |
| Skill-use = substring grep of slug over raw line | `learn.go:739-743` | a skill named `run` or `delegate` can never be flagged dead; dead_load is one of only two reducible classes and it's built on this |
| Behavioral sinks rank at zero | `learn_loops.go:196`, `learn.go:175` | a 500k-token error-loop session sorts below a 200-token CLAUDE.md trim; `OutputTokenFloor` is computed and then dropped |
| No outcome measurement | skill + `readLearnDiff` (index.ts) | diff tracks sink IDs gone/back/new, never "prefix dropped 1.9k/turn in the 14 sessions since you applied the fix" |
| Config history overwritten | `config_scan.go:288` `ON CONFLICT ... UPDATE` | can't say "your config tax grew 34% this month because plugin X arrived" |
| MCP/tool-schema tax invisible | `config_scan.go` | often the single largest per-turn prefix cost; hooks/plugins are counted, tool schemas never estimated |
| Window sizes hardcoded | `learn.go:995-1006` | `openai→400k, else 200k`; `shared/` already owns a provider catalog |
| No per-repo segmentation | `scanConfig(cwd)` + global scan | one blended median context across every project; config measured for cwd only |

Ordering below is leverage per line of diff. P0 turns the headline number from
a guess into a measurement. P1 is reach. P2 is the new intelligence. P3 is what
makes it an optimizer rather than a linter.

---

## P0 — Measure what we already guess (small diffs, big honesty upgrade)

### 0.1 Provider-counted config tax from turn-1 usage

The first assistant turn of every Claude Code session carries
`cache_creation_input_tokens + input_tokens` = the exact provider-counted size
of system prompt + tool schemas + CLAUDE.md + skill catalog + first prompt.
`claudeTurnContext` (`learn.go:774`) already parses it — we just never treat
turn 1 specially.

- Record per-session first-turn context; take the per-repo median as
  `measured_prefix_tokens`. Emit it on the `config_tax:baseline` sink evidence
  next to the static bytes/4 figure.
- The delta `measured_prefix − static_config_estimate` is the **invisible
  tax** (system prompt + tool schemas + hook stdout) — today's report can't
  see it at all. New load_bearing evidence line: "provider counted ~Nk of
  fixed prefix; your files explain ~Mk of it".
- Basis stays `inferred` at the plan level, but the evidence names its source
  (`session_usage` vs `bytes4_estimate`) exactly like retro already does.

This is the measurement ladder made explicit: static file scan < transcript
inference < provider-counted usage < proxy-observed exact. Every detector
should state its rung and prefer the highest rung available.

### 0.2 Give behavioral sinks their measured magnitude

`learning_loop` sinks already compute `OutputTokenFloor` — emit it as
`TokensPerTurn`... no: it's historical, not a rate. Instead add a
`tokens_observed` field on the Sink (historical framing keeps it out of
`tokens_per_day_rate`), and let ranking use
`max(tokens_per_day_rate, tokens_observed/window_days)` so a 500k error-loop
outranks a 200-token trim. Dumbzone gets the same: sum provider-counted tokens
on turns past the 50% line. No new claims — the numbers already exist.

### 0.3 Real skill-use detection

Claude Code writes skill invocations as `tool_use` blocks (Skill tool, and
`<command-name>` markers in user turns). Parse those instead of
`strings.Contains(line, slug)`. Keep the substring pass only as a
false-negative guard (if the slug appears at all, don't flag dead). Dead-load
becomes trustworthy enough to raise from "consider gating" to a ranked
reducible with confidence attached.

### 0.4 Tokenizer honesty

`estimateTokens` is bytes/4. The retro pass already links the engine with its
o200k counter. Use it for config snapshots when the engine is importable
(it is — same binary), keep bytes/4 as fallback, and name the basis per
snapshot. Memory note from 2026-07 stands: o200k measurements exposed fake
savings before; stop compounding on bytes/4.

---

## P1 — Multi-provider reach

### 1.1 SessionSource adapter interface

One Go interface in `internal/store`:

```go
type SessionSource interface {
    ID() string                      // "claude", "codex", "gemini", ...
    Discover(since time.Time) []SessionRef
    ScanSession(ref SessionRef, sink func(TurnEvent)) error
}
```

`TurnEvent` normalizes what every detector needs: timestamp, provider-counted
context (input/cache_read/cache_creation where available), model, tool calls
(name, input summary, output text, is_error), text blocks for the repaste
miner, spawn markers, compaction markers. Port the two existing scanners onto
it — `scanClaudeTranscriptBehaviorUntil` and `scanCodexSessionBehaviorUntil`
become adapters, and the deferred Codex repaste mining (`learn.go:612`) falls
out for free because the miner consumes normalized text blocks.

### 1.2 New adapters, in order of user base

1. **Gemini CLI** — session JSON under `~/.gemini/` (verify current layout;
   the extension install base already exists).
2. **opencode** — `~/.local/share/opencode/storage/` session store; we ship a
   native plugin there already.
3. **OpenClaw** — workspace session logs.
4. **aider** — `.aider.chat.history.md` (markdown, no usage blocks: text-only
   rung, repaste mining and loop detection only, no dumbzone).

Each adapter states its ladder rung: sources without provider usage simply
omit dumbzone/context-depth instead of estimating them. That is the existing
"omit rather than emit a zero" rule applied to providers.

### 1.3 Model catalog for window sizes

Replace the `contextWindow` switch with the `shared/` provider catalog (which
the proxy already depends on) + explicit fallback. The dumbzone caveat then
names real windows per model instead of two hardcoded numbers.

### 1.4 Cross-provider insights (only possible after 1.1)

Same-repo comparison lines, all historical framing: "on this repo your Codex
sessions peak 2.1× deeper into the window than Claude sessions"; "recurring
block X appears in Claude AND Gemini sessions — one cavemem offload covers
both agents" (cavemem is already multi-agent per the sibling plan).

---

## P2 — New detectors (the advanced insights)

### 2.1 Cache-hygiene / prefix-churn detector

From per-turn usage: a healthy session shows large `cache_read` + small
`cache_creation` after turn 1. Repeated full-prefix `cache_creation` spikes
mean something is busting the cache — typically a hook or plugin injecting
content that changes every turn (timestamps, counters). Evidence: per-session
`cache_creation` histogram; sink class behavioral, historical framing.
This detector must be allowed to flag caveman's own per-turn reinforcement
hook if the data says so — that's the honesty brand doing its job.
No dollars, and it must not resurrect the retired cache actuation ids — it
names the churn, the user fixes the config.

### 2.2 Re-read waste detector

Tool inputs are already parsed (`claudeToolCalls`). Same file Read ≥3 times in
one session → sum of the later reads' output tokens is a measured floor.
Generalizes the existing refetch_loop beyond identical signatures (Read with
different offsets on one path). Fix suggestion routes to the wrap (CCR makes
re-reads cheap) or to session habits — soft, behavioral.

### 2.3 Compaction-churn detector

Claude Code transcripts mark compaction summaries. Detect the
compact-then-re-establish pattern: compaction event followed by re-reads of
files already read pre-compaction. Measured floor = tokens spent
re-establishing. This is the strongest evidence for the dumbzone advice we
currently assert as folklore — and it's per-user, from their own history.

### 2.4 MCP / tool-schema tax

Static: read `.mcp.json` + `settings.json` server lists, count servers and
tools where schemas are locally known. Exact: the turn-1 measured prefix
(0.1) minus file-explained tokens already bounds it. When the proxy has
recorded wrapped sessions, use exact request bytes instead — highest rung.
Evidence names per-server tool counts so the fix ("defer/disable server X in
this repo") is concrete.

### 2.5 Section-level CLAUDE.md attribution

Today's advice is "trim to 150 lines". Better: segment CLAUDE.md by headings,
fingerprint each section with the existing normalizer, and check which
sections' distinctive content ever surfaces in session text (echoed rules,
cited paths). Sections with zero echo over N sessions are the trim candidates,
named specifically. Still consent-gated, still "no echo detected is
window-bounded, not proof".

---

## P3 — From linter to optimizer (the compounding loop)

### 3.1 Outcome ledger: fixes get measured

**Shipped.** After an approved fix passes its re-measure gate, the
`caveman-learn` skill calls `caveman learn applied <sink_id>`. Caveman's own
outcome store records sink ID, fix kind, application time, and before-value;
user and repository config remain untouched. Later scans emit confirmed rows
with sink ID, application date, before/after values labeled by that row's unit,
post-fix session count, and one verdict: `improved`, `unchanged`, `regressed`,
or `insufficient_data`. Rows without a usable after-value stay absent unless
the verdict is `insufficient_data`, which gets a friendly wait-for-more-data
line. Basis remains inferred/local. Outcome history now appears in CLI text and
Markdown; a dedicated HTML report section remains deferred.

### 3.2 Config snapshot history + regression attribution

Change `config_snapshots` upsert to dated rows (keep a `latest` view for
existing readers). Then the score diff can attribute: "config tax +34% since
last scan — new: plugin X (+2.1k), CLAUDE.md +40 lines". The `readLearnDiff`
sink-ID diff in the CLI upgrades from "2 moves gone" to named causes.

### 3.3 Counterfactual replay: simulate the plan

The retro pass already replays real history through the engine. Extend it:
`caveman learn simulate <sink_id...>` re-runs the retro walk with the proposed
change applied (config block removed from the believed prefix, recurring block
replaced by its pointer + recall cost). Output: measured would-have-been
tokens on the user's actual last-30d history — a real counterfactual, not
rate × turns arithmetic. Framing rules carry over verbatim: sums over scanned
sessions, never projected, never a dollar.

### 3.4 Portfolio ranking

**Partly shipped.** Analyzer groups sinks that one fix resolves and promotes a
`best_next_move`. CLI uses concrete top sink title as move title, fix label as
kind, includes top sink ID, and surfaces confidence as `measured`, `transcript`,
or `estimate`. Portfolio volume is split into `combined_rate_per_day` and
`combined_observed_in_window`; CLI renders each independently. Forward-rate
sinks rank before historical observed-only sinks. Subtracting measured cavemem
recall overhead and folding a user's confirmed-fix history into ranking remain
deferred.

### 3.5 Per-repo segmentation

**Partly shipped.** `--repo <substring>` filters sessions before analysis;
default output remains aggregated. `--all` adds a per-repository block with
repository name, session count, dumbzone percentage, and median context. It
does not emit per-repository scores or per-repository top-sink lists. Those
deeper breakdowns remain deferred.

---

## Invariants that must survive every phase

- Analyzer stays read-only; the skill (with consent) stays the only writer.
- `inferred` basis everywhere local; no dollars, no monthly projection,
  under-claim on every floor.
- Locators, never bodies, in sink evidence.
- Count-only for subagents; no model-right-sizing resurrection; no cache
  actuation minted from observation (proxy CLAUDE.md gotchas are binding).
- A provider/rung that can't measure something omits it — never a zero,
  never an estimate dressed as usage.
- Schema: everything above is additive-optional on `caveman.learn.v1`
  (evidence keys, `tokens_observed`, `confirmed` block) until a breaking
  change forces `v2`; consumers already treat optional blocks as absent-able.
