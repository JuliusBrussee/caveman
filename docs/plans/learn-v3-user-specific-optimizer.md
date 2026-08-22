# caveman learn v3: from honest linter to personal cost optimizer

Status: research + viability assessment. Written 2026-08-20 against `main` @ a42ef76.
Supersedes nothing — it is the layer above `docs/plans/learn-v2-smart-optimizer.md`,
most of whose P0/P1/P2 is now shipped.

## Where v2 landed

Shipped and verifiable in `proxy/internal/store/`: five session sources
(`source_{claude,codex,gemini,opencode,aider}.go`), provider-counted turn-1 config
tax, structured skill-use detection, engine-o200k config counting, and detectors for
cache churn, re-read waste, compaction churn, MCP surface, CLAUDE.md section echo,
learning loops, dumbzone, dead skills and subagent count. Plus the loop-closers:
outcome ledger (`learn_outcomes.go`), counterfactual replay (`learn_simulate.go`),
portfolio ranking (`learn_portfolio.go`), per-repo segmentation.

That is a genuinely strong **diagnostic**. The remaining distance to "saves the user
money" is three structural gaps, not more detectors:

1. **No price model.** Everything is tokens. A user cannot compare a 40k-token
   re-read floor against a 2k/turn config trim, because those tokens cost 60× different
   amounts depending on whether they were cache reads, cache writes, or output.
2. **No outcome model.** Every number is per-turn or per-window. Nothing knows which
   sessions *finished the job*. The largest real waste in coding agents is not verbose
   context — it is sessions that burned 300k tokens and produced no commit.
3. **No actuation.** learn diagnoses; the skill edits config once, with consent. Nothing
   converts a finding into recurring, automatic savings on the next session.

Everything below attacks one of those three. Ranked by (leverage × confidence) / effort.

---

## The honesty argument that unblocks half of this

The current decree is "no dollars locally" (`learn.go:292`, `proxy/CLAUDE.md`
no-fake-savings). That rule was written when local numbers were `bytes/4` guesses. It
conflates two different things:

- **Spend attribution** — "these 4.1M cache-read tokens on claude-opus-5 cost $X" is
  *arithmetic over provider-counted usage at a dated published rate*. No causality is
  claimed. No counterfactual. No projection.
- **Savings claims** — "this fix saved you $Y" requires a counterfactual and a
  measurement of the change. That is what `verified` is reserved for, and that stays
  gateway-only.

`shared/provider-catalog/` already carries the exact per-model `input_per_million`,
`output_per_million`, `cache_read_input_per_million`, `cache_write_input_per_million`,
dated `verified_at`, and a snapshot pin that signed cloud receipts already stand on.
The cloud prices provider-counted usage with it. Local traces contain provider-counted
usage (`turnEvent.CacheReadInputTokens` / `CacheCreationInputTokens` / `ContextTotal`,
`Model`, `ProviderKey`).

So: local learn may state **spend**, with basis `provider_counted_x_published_rate`,
stamped with the catalog version, window-bounded, never projected forward, never
labelled `verified`, and never attached to a fix as a saving. That is strictly more
honest than today's silence, which forces the user to do the pricing in their head
with worse data.

**Subscription users get quota, not dollars.** Claude Max / ChatGPT Plus / Gemini
sessions have no marginal dollar cost; their scarce resource is the weekly limit. Learn
should detect billing origin from the trace and switch units: dollars for API-key
traffic, *share of window / rate-limit headroom* for subscription traffic. Presenting a
Max user a dollar figure is exactly the kind of fake precision the honesty brand exists
to refuse.

---

## F1 — Spend attribution (the unlock)

**What.** Every existing sink gains an optional priced view: `tokens_observed` and
`tokens_per_day_rate` multiplied through the catalog row for the model that actually
served those turns, split by component (fresh input / cache read / cache write /
output). Report headline becomes: "scanned window: 11.2M tokens, $34.10 at published
rates — 71% of it cache reads, 9% cache writes, 12% output."

**Why it is the unlock.** Ranking is currently by token volume, which is the wrong
ordering function. A 500k-token cache-read sink and a 500k-token output sink differ by
~60× in cost on Anthropic pricing. Every downstream feature (portfolio, best next move,
simulate) inherits a wrong priority order until this lands.

**Data.** Present, except per-turn completion `output_tokens` — `turnEvent` captures
context/cache fields but not `usage.output_tokens`. That is a ~5-line addition to
`session_source.go` + each adapter.

**Viability.** High. Effort M. The catalog is Go-loadable and already a proxy dependency.
Main work is the units switch (dollars vs quota) and a caveat generator that fails
closed to tokens-only when the model is unknown to the catalog or billing origin is
ambiguous.

**Risk.** Brand. Mitigate structurally, the way `kit` does: a `basis` that is required,
a distinct label from `verified`, catalog version rendered next to the figure, and no
API that can multiply a window figure into a month.

**Cloud link.** Direct. This is the same math the gateway does, on the same catalog,
which makes the local→cloud story continuous instead of a units change at the paywall.

---

## F2 — Cache economics, not just cache churn

**What.** `detect_cache_hygiene.go` today flags `cache_creation` spikes. Turn it into
the primary money detector:

- **Effective input multiplier** per session/repo/agent:
  `(1.0·fresh + 0.1·cache_read + 1.25·cache_write) / total_input`. A well-formed agent
  session sits near 0.15; a prefix-busting one sits near 1.1. One number, immediately
  legible, provider-counted.
- **Miss attribution.** When `cache_creation` re-spikes mid-session, correlate against
  what the local config can inject per-turn: hook stdout, statusline, plugin lists,
  MCP server sets, and anything containing a timestamp/counter. Name the suspect file.
- **Breakpoint shape.** Anthropic traces disclose where writes land. Sessions that
  write the full prefix every turn are the 71% cost delta case, measured.

**Why SOTA.** Prefix stability is the single highest-leverage application-level
optimization in production LLM systems: stable vs perturbed prefixes measure ~71% cost
difference on identical requests, and teams report 7% → 74% hit rate purely by moving
dynamic content out of the prefix. No local coding-agent tool measures this from the
user's own transcripts today.

**Data.** Fully present.

**Viability.** High. Effort M. The correlation half is heuristic and must ship
soft-framed ("these turns re-wrote the prefix; these config sources change per turn"),
but the multiplier half is pure arithmetic on provider counts.

**Risk.** Must not mint a cache actuation id — `proxy/CLAUDE.md` is binding: observing
churn never proves stable-prefix eligibility. Report the churn, name the suspect file,
let the user fix the config. Also: this detector must be allowed to flag caveman's own
per-turn reinforcement hook. If it never does, it is not being honest.

**Cloud link.** Strongest of any feature. The residual after the user fixes their own
config is exactly what the gateway's prefix stabilizer and breakpoint planner exist to
capture, and the gateway can measure it `verified`.

---

## F3 — Session outcome join: the dead-end tax

**What.** Segment scanned sessions into tasks, then join each session's window against
the repository's own git history (the transcripts already carry `cwd`/repo). Emit
observational cohorts:

- sessions whose window contains a commit touching files the session read/edited
- sessions with ≥N error-loop turns and no commit
- sessions that compacted ≥2× and produced no commit

Headline: "31% of your scanned tokens went to sessions that ended without a commit;
those sessions averaged 3.4× the error-loop turns of the ones that did."

**Why it matters more than any prose compression.** Compression trims the cost of doing
work. This finds work that produced nothing — where the money actually is. It is also
the most *user-specific* signal available: it is literally this person's own hit rate,
in their own repos, and it differs enormously between users and between repos.

**Data.** Present locally (git is on the machine, repo is known per session). No network.

**Viability.** Medium-high. Effort M-L. The segmentation is the fiddly part
(session ≠ task; use compaction boundaries and idle gaps).

**Risk.** Highest framing risk in this document. This is **correlational**, and a
session with no commit is not necessarily wasted (exploration, review, debugging that
informed a later commit). It must ship as `behavioral` class, historical framing, with
the suggestion softened to the point of being an observation, and with an explicit
"no-commit does not mean no value" caveat that is not deletable. Under those rules it is
still the most interesting line in the report.

**Cloud link.** Indirect but valuable: cost-per-outcome is the metric a team buyer wants,
and it is the natural headline for a cloud team dashboard where the git join can run
per-repo across seats.

---

## F4 — Tool-output portfolio: what your tokens are actually made of

**What.** Group observed tool-result tokens by normalized tool signature — `Bash(git
diff)`, `Read(*.lock)`, `mcp__x__query`, `Grep(-r)` — and rank. Then, per signature,
attach what retro already computes: how much the engine would have cut. Output is a
five-row table naming the user's five most expensive tool shapes and the cut available
on each.

**Why.** Today's retro reports one blended `would_cut_tokens`. A user cannot act on a
blended number; they can act on "`Read` on lockfiles cost you 180k tokens this month and
compresses 94%". It also turns a wrap upsell into a specific, checkable claim.

**Data.** Present — `turnToolCall` carries name, input summary, output text; the engine
is already linked in the retro path.

**Viability.** High. Effort S-M. Mostly a grouping key plus rendering; the measurement
already exists. Best value-per-diff item in this list.

**Risk.** Low. Signature normalization must not leak paths outside the locator rules.

**Cloud link.** Direct: this *is* the wrap's product surface, itemized against the user's
own history rather than a marketing average.

---

## F5 — Personal baselines instead of static thresholds

**What.** Every detector currently trips on a global constant: `cacheChurnSpikeTokens =
10_000`, `rereadMinCalls = 3`, `sectionEchoMinSessions = 5`, `mcpSurfaceMinServers = 3`.
Replace the fixed side with percentiles of *this user's own* distribution over a longer
baseline window, keeping the constant as a floor. Then add the thing thresholds enable:
**regression alerting** — "your config tax is at the 96th percentile of your own last 90
days; it grew 34% when plugin X arrived on 2026-08-03" (v2's §3.2 dated config snapshots
are the prerequisite).

**Why.** This is the literal answer to "make it user-specific". A heavy user drowns in
findings; a light user sees an empty report. Personal percentiles fix both, and turn
learn from a one-shot audit into something worth re-running weekly.

**Data.** Present, given dated config snapshots.

**Viability.** High. Effort S per detector, plus the snapshot-history change.

**Risk.** Low, with one trap: percentile thresholds on a user who is uniformly bad
normalize their badness away. Keep the absolute floor as a second gate — trip on
`max(personal_p90, absolute_floor)`.

**Cloud link.** Baselines are the honest precursor to team benchmarking ("your effective
input multiplier vs your org's median") without ever shipping a synthetic industry average.

---

## F6 — Learn → runtime policy: the actuation gap

**What.** Learn currently ends at a consent-gated one-time edit. Add a second output: a
machine-readable policy the local runtime consumes every session.

- `caveman learn policy --emit` writes a small, reviewable policy file: the tool
  signatures this user's history proves are worth eliding, the context depth at which
  their sessions historically start looping, the recurring blocks already offloaded to
  cavemem and their recall topics.
- Hooks and the local wrap read it: pre-load offloaded pointers at SessionStart, warn
  once on dumbzone entry, apply elision to the user's own top-N tool signatures.

**Why.** A diagnosis applied once decays. Every finding that is not actuated is worth
zero on the next session. This is the difference between a linter and an optimizer, and
it is where recurring savings actually come from.

**Viability.** Medium. Effort L, and it crosses the analyzer/writer boundary that the
current design deliberately keeps clean.

**Risk.** Real, and it is architectural. The invariant "analyzer is read-only, the skill
with consent is the only writer" is load-bearing. Keep it by making the policy file
itself a consent-gated artifact: learn proposes, the user approves once, the runtime
reads it thereafter, and every policy entry carries the sink id and date that produced
it, so `caveman learn` can later report a policy entry that stopped paying off. Never
auto-emit.

**Cloud link.** This is the local shape of what the gateway does server-side; a user who
outgrows the local policy file is the cloud's ideal customer, and the policy file
transfers as-is.

---

## F7 — Trajectory → skill distillation (the ACE/CODESKILL move)

**What.** Mine recurring *procedures* across sessions — the same tool sequence appearing
in ≥N sessions in the same repo ("read config → run `pytest -k`→ patch → re-run") — and
propose a compact skill or recipe that encodes it, so the agent stops re-deriving the
sequence from scratch each time.

**Why SOTA.** This is the live research frontier: ACE (ICLR 2026) treats context as an
evolving playbook and reports +10.6% on agent benchmarks with reduced adaptation cost;
CODESKILL and Skill-DisCo distill coding-agent trajectories into reusable procedural
skills; ClawTrace/CostCraft adds the cost signal so distillation optimizes for waste
removal rather than accuracy alone. Caveman already has the two halves nobody else has
together: trace mining *and* a memory store with a measured recall cost.

**Viability.** Medium, and lower than its excitement suggests. Effort L. The
sequence-mining is tractable; the honest measurement is the hard part, and the failure
mode is severe: a distilled skill adds prefix tokens *every turn of every session*
(that is exactly what `dead_load:skills` punishes), while paying back only on the
sessions that hit the pattern. Ship it only behind the outcome ledger: propose, apply
with consent, and let the ledger return `improved`/`regressed` over the following weeks
with an automatic revert path.

**Risk.** Also the quality risk: a distilled procedure that encodes a *bad* habit
compounds it. The `improved`/`regressed` verdict is the only defense, and it needs an
outcome signal — which is F3. **Do not build F7 before F3.**

**Cloud link.** Distilled skills are portable across agents and seats; this is the most
obvious team-tier feature in the list ("your team's five most repeated procedures").

---

## F8 — Subagent economics, within the decree

**What.** Claude Code marks sidechains (`turnEvent.Side` is already parsed). Report the
measured split: what share of provider-counted tokens went to subagents, median cost per
spawn by agent type, and the depth distribution of subagent sessions.

**Why.** Fan-out is the fastest-growing line item in agent bills and users have zero
visibility into it. "41% of your tokens went to subagents; `Explore` averages 47k per
spawn" is actionable on its own without any recommendation attached.

**Data.** Present.

**Viability.** High. Effort S.

**Risk.** The decree in `proxy/CLAUDE.md` is explicit: `subagent_overuse` is count-only,
carries no practice id, and spawn count may never reactivate `context-exploration-offload`
or prove a spawn unnecessary. Measuring *cost* is not the same as claiming *waste* — keep
it observational, keep the practice id empty, never emit a "spawn fewer" suggestion.

**Cloud link.** Per-agent cost attribution is already a `CavePlanScopeShare` concept
(`agent_id`, `workflow_id`); this is its local twin.

---

## F9 — Invoice reconciliation (the only honest local `verified`)

**What.** Let the user point learn at a provider usage export (Anthropic Console CSV,
OpenAI usage export). Reconcile: measured local tokens vs billed tokens over the same
window. Output the coverage ratio and the unattributed remainder — "learn accounts for
78% of your billed Anthropic tokens; the other 22% came from something not in these
transcripts."

**Why.** It is the only way a local tool can anchor to real money, and the *unattributed*
share is often the most valuable finding — it is where a forgotten script, a CI agent, or
another machine is spending.

**Viability.** Medium. Effort M. Export formats churn, so parse defensively and fail
closed to "could not reconcile" rather than a wrong ratio.

**Risk.** Low on honesty (an invoice is ground truth), moderate on UX (the user has to
fetch a file).

**Cloud link.** Excellent on-ramp: reconciliation is the moment the user learns their
local view is incomplete, which is precisely the gateway's value proposition.

---

## F10 — Cloud handoff digest (cold-start the Cave Plan)

**What.** `caveman learn export --digest` produces a small, privacy-safe artifact:
sink ids, practice ids, counts, provider-counted aggregates, catalog version, confidence
rungs. Locators and bodies stay home — the digest carries no transcript content, by
construction, the same rule the candidate files already follow.

**Why.** The cloud Cave Plan today needs gateway traffic before it can say anything. A
new tenant sees an empty dashboard. The digest lets day 0 of the cloud show a plan built
from the user's own last 30 days, with every move labelled `inferred` until gateway
traffic upgrades it to `verified`. It also makes the local→cloud boundary legible: the
report can split its moves into "you can apply these yourself, now" and "these need a
proxy in the path", with the second list carrying the *measured counterfactual from your
own history* rather than a marketing number.

**Viability.** High. Effort S-M locally (the plan already has stable sink/practice ids;
`learn_portfolio.go` already groups by fix family). The cloud side is the larger half and
lives in the private repo.

**Risk.** Privacy. The digest must be inspectable before it is sent, opt-in per send, and
schema-pinned so a future field cannot smuggle content into it. Given the telemetry
opt-out stance, treat it as an explicit user-initiated export, never a background upload.

---

## Sequencing

| Wave | Items | Rationale |
|---|---|---|
| 1 | F1, F2, F4 | Price the report, fix the ranking function, itemize the wrap's value. All arithmetic on provider-counted data; no new honesty surface beyond the spend/savings split. |
| 2 | F5, F8, F10 | Make it personal, make fan-out visible, make the cloud bridge real. All small-to-medium and independent. |
| 3 | F3, F9 | Outcome model and money anchor. Highest framing care; F3 is the prerequisite for anything that claims a fix helped. |
| 4 | F6, F7 | Actuation and distillation. Only after F3 exists to grade them, and F6 only with the consent-gated policy artifact. |

## Explicit non-goals

- No model right-sizing, ever, from local heuristics. The retired id stays retired; a
  model name in a transcript proves nothing. F1 may *report* that 72% of turns were
  sub-200-output-token tool loops on a frontier model — as an observation, with no
  recommendation and no optimizer id.
- No cache actuation minted from observation. F2 names churn; it never claims eligibility.
- No monthly projection, in any feature, in any unit.
- No `verified` basis outside the gateway and F9's reconciliation against a real invoice.
- No background upload of anything (F10 is a user-initiated export).

## Sources

- [Agentic Context Engineering (ACE), arXiv:2510.04618 / ICLR 2026](https://arxiv.org/abs/2510.04618)
- [CODESKILL: Learning Self-Evolving Skills for Coding Agents](https://arxiv.org/html/2605.25430)
- [Skill-DisCo: Distilling and Compiling Agent Traces into Reusable Procedural Skills](https://arxiv.org/html/2606.26669)
- [ClawTrace: Cost-Aware Tracing for LLM Agent Skill Distillation](https://arxiv.org/html/2604.23853)
- [Prompt Cache Hit Rate Engineering](https://agentmarketcap.ai/blog/2026/04/11/prompt-cache-hit-rate-engineering-2026)
- [Prompt Caching in Practice: From 7% to 74% Hit Rate (DigitalOcean)](https://www.digitalocean.com/community/conceptual-articles/prompt-caching-in-practice-hit-rate)
- [Tool Attention: Dynamic Tool Gating and Lazy Schema Loading, arXiv:2604.21816](https://arxiv.org/html/2604.21816)
- [MCP SEP-1576: Mitigating Token Bloat in MCP](https://github.com/modelcontextprotocol/modelcontextprotocol/issues/1576)
- [LLM Model Routing: Cost-Quality Optimization](https://www.digitalapplied.com/blog/llm-model-routing-2026-cost-quality-optimization-engineering-guide)
