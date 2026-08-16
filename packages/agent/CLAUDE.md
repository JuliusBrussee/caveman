# packages/agent

> **Repository routing:** do not continue Agent SDK product work here. Source of
> truth is `JuliusBrussee/caveman-agent-sdk`, local checkout
> `/Users/julb/Desktop/GitHub/caveman-agent-sdk`. This directory is a historical
> consumer copy; edit only for pinned integration, migration/removal, or an
> explicit cross-repo sync.

`@caveman-ai/agent`: opinionated TypeScript efficiency framework over exact-pinned
Pi. `src/runtime.ts` owns agent execution, cache safety, tool isolation, runtime
supervision, and content-blind evidence. Loopback runtime readiness requires
health identity plus proxy-validated run-state/PID/executable ownership.
`src/build.ts` owns finite candidate search, eval-complete selection, immutable
lock, and drift checks.
Build lock Context IR contains static definition segments only. Eval/user input,
history, and tool results are runtime segments and never enter lock digest.
Conversation handles are opaque, process-local, transactional, single-owner,
and bind cache epoch to agent/model/full-plan/prefix fingerprint. Stream close
aborts and settles provider/tool/subagent execution before releasing ownership.
Dev reuses one immutable staged project-relative source graph until watched
project inputs change. Definition, sandboxed tools, nested file sources, and
lock identity use same snapshot. Reload preserves parent-owned conversation.
Programmatic required-sandbox runs create per-run immutable copy of complete
source graph before provider traffic and import tool workers only from copy.
Keep module top level side-effect-free: Node ESM cannot tear down old graph
timers/listeners after hot reload; restart when editing resource-owning modules.
Nested normal tools use private root-relative agent paths, root/leaf definition
digests, recursive graph validation, ancestor-shared pre-spend ledgers, and
process-group sandbox teardown. Required sandbox policy propagates down graph;
each reserved turn needs complete usage and exact provider/model identity.
Third sandbox mode `host` is explicit opt-in for interactive/coding agents whose
tools need real host access: closures run in-process with no worker and no
`entryPath`, and `effect: "write"` executes instead of being blocked, while
effect declaration stays mandatory. Host mode under a required ancestor fails
closed (`cave_host_sandbox_nested_under_required`) so a subagent cannot escape
root containment. Live host runs are lock-ineligible (EAB-101): `compile` throws
`cave_host_sandbox_lock_ineligible` before any search run for host mode ANYWHERE
in the definition graph — root or subagent, since a host subagent runs closures
in this process just as a host root does — and locked builds for coding agents
compile against fixture corpora (EAB-112) under a contained mode.
Optional `RunOptions.maxCostUsd` seeds one root ledger into that same ancestor
chain, so root turns reserve against it too. It is a best-effort public-catalog
cap (EAB-102), not financial enforcement; exhaustion ends the run with
`cave_run_cost_budget_exceeded` before the next model call, and a model the
catalog cannot price fails closed instead of consuming $0 of budget.
Cold machines degrade instead of failing: when the loopback gateway cannot be
reached (or `RunOptions.cave: "off"` is set), the run keeps the provider's own
base URL, applies no transform, sends no Caveman account key, and reports
`RunResult.mode: "observe-only"`. `ensureRuntime: false` skips loopback startup
and probing because the caller manages that runtime; it never bypasses HTTPS and
gateway-identity verification for a non-loopback URL.
Concurrent cold runs coalesce by gateway URL onto one readiness/start attempt;
the completed positive or negative result is then cached for five seconds.
Caller-supplied fetch transports bypass both shared states.
Route resolution is not routing: the gateway proxies only `anthropic`, `openai`,
and `google`, so every other Pi provider (xai, groq, bedrock, openrouter…) keeps
its own base URL even on a reachable gateway. Actual routing is the source of
truth for both honesty questions — a request that does not go through the
gateway carries NO `x-cave-*` header at all (the account key is a credential;
agent/workflow/session/cache-epoch/prefix-digest/context-bill/build+plan digests
are account-linked identifiers), and `mode` is `observe-only`. Mixed graphs
under-claim: one subagent call off the gateway makes the whole run
`observe-only`.
Gateway-routed Pi runs carry one framework-owned 32-hex trace id. Every root or
child agent invocation gets a distinct 16-hex span id; provider requests name
the current invocation through `x-cave-parent-span-id`, and child invocation
spans share their parent invocation. With a route-time `CAVE_API_KEY`, children
append only identity, timing, depth, and status metadata to one bounded
root-owned batch; after descendants settle, the root defers exactly one
best-effort OTLP/JSON request. Prompt, message, tool, result, and error content
never enter that payload. Each child `invoke_agent` span also carries a bounded
`cave.guard.*` manifest describing the controls effective at admission: only
fixed categorical states for child call/spend/context, depth, root budget,
per-turn fan-out, and total model/tool calls. It contains no thresholds, tool
names, prompt/content, or spend. Its basis is `client_runtime_declared`: useful
for advisory coverage and avoiding redundant proposals, never platform
attestation, verified enforcement, or a reason to suppress a finding. Missing
or ambiguous state is `unknown`, never inferred as unprotected. The immutable
route-time key and root agent/workflow/session labels propagate through children;
account-less local routing keeps request correlation headers but sends no
unauthenticated OTLP request. The batch labels its delivery basis
`attempted_unconfirmed`: HTTP
acceptance is deliberately not awaited or surfaced, and export failure never
changes paid execution, so Cloud detector coverage is measured and may
honestly be zero.
After every descendant settles, the root `invoke_agent` span emits four closed
integer outcome attributes outside the guard manifest:
`cave.agent.tree.admitted_descendants`,
`cave.agent.tree.peak_active_descendants`,
`cave.agent.tree.invocation_limit_rejections`, and
`cave.agent.tree.concurrency_limit_rejections`. They are exact root-ledger
outcomes, including admitted children whose individual span was dropped by the
1,024-span batch ceiling. They appear only on the root span and are zero when
no child was admitted. They never contain configured cap magnitudes, content,
task text, tool names, or error text, and do not change guard-manifest v2.
Per-tool child-call counters, per-run model/tool counters, and breaker state
still restart in every child; `maxCalls`, `maxSubagentDepth`, and the per-turn
breaker are not tree-width contracts. Callers that need a root-tree bound may
opt into `RunOptions.maxSubagentInvocations` (monotonic admissions across all
tools and depths) and/or `maxConcurrentSubagents` (simultaneously active
descendants). Descendants inherit one mutable root ledger; reservation is
synchronous, and active capacity is released after success, error, or abort.
Depth and wallet rejections happen before admission and consume no tree slot.
Leaving both options unset preserves the prior behavior. New child spans emit
strict guard-manifest v2: `tree_invocations` and `tree_concurrency` are each
only `active` or `absent`, derived from whether the root option was supplied.
The manifest never exports either numeric value and remains client-declared,
not enforcement attestation. Historical v1 stays valid only when both v2-only
keys are absent and cannot describe either tree control; malformed, missing, or
unknown v2 states are invalid rather than inferred.
A run carrying a locked build or candidate plan never degrades silently and
throws `cave_gateway_required_for_locked_plan`. Nested runs inherit the parent's
resolved route instead of re-probing. `doctor` treats a missing engine, missing
runtime CLI, or unreachable gateway as WARN with exit 0 and reports
`execution_mode`; locked-execution readiness stays false in that state.
Child-process permission fails closed without portable descendant containment.
`cave_` tool names are framework-reserved.
Public `RunOptions` excludes nested routing/recursion and compiled plan/build
identity. Only package-internal compiler/CLI path may execute validated plans.

Public entry points:

- `src/index.ts` and `src/primitives.ts` — builder API;
- `src/build.ts` — compiler API;
- `src/execution-kernel.ts` — locked harness/plan/Context-IR preparation,
  shared agent-to-Context-IR lowering, selected model/reasoning enforcement,
  provider usage validation, and public catalog cost finalization shared by Pi
  runtime, compiler, checker, and adapter boundary. Reasoning-breakdown
  availability stays separate from aggregate usage; locked/nested evidence
  rejects a missing split from reasoning-capable models;
- `src/runtime-identity.ts` — single source for framework, Pi adapter, and
  exact-pinned upstream versions used by compiler, checker, and runtime;
- `src/catalog.ts` — GENERATED from
  `public/shared/provider-catalog/catalog/current.yaml` by
  `scripts/generate-agent-catalog.mjs`; never hand-edit it and never hand-type a
  price. It carries every USD row the catalog prices region-agnostically
  (`region: global`) and omits regional-only rows rather than borrowing one
  region's rate. `CATALOG_SHA256` is the sha256 of those exact catalog bytes and
  is stamped into lock evidence; `tests/catalog.drift.runtime.mjs` fails until
  the generator is re-run after a catalog edit. `RunResult.priceBasis` labels
  whether `costUsd` came from that catalog or is an honest zero;
- `src/source-graph.ts` — strict project/workspace dependency graph plus opaque
  installed-package artifact closure. It uses `es-module-lexer` for ESM and
  narrow comment-aware scanners for TypeScript type edges, `require`, and
  `new URL(..., import.meta.url)`. It resolves ESM import-only exports,
  follows dependency edges from physical package roots so pnpm symlink layouts
  lock the same reachable artifacts as npm installs,
  rejects computed project loaders, hashes every file in reachable installed
  packages and their declared dependency closure, and never regex-parses vendor
  comments as project source;
- `src/code.ts` — the new caveman-code: `createCodingAgent` (host-sandbox
  read_file/grep/bash/edit_file over one workspace, output capped BEFORE any
  transform and under the 32 KiB inline tool-result ceiling so observe-only
  works with no engine) plus the session surface `startCodingSession`,
  `runCodingTurn`, `runCodingSession`. Optimized is the default:
  `defaultCodingPlan` routes exactly one CCR-recoverable transform per live-zone
  kind (`tool_result`→terminal, `history`→text; two routes on one kind collapse
  into `dynamic_route_ambiguous`), never `toon`, with `cave_retrieve` on.
  Degrading to observe-only is loud and recorded on `session.notices`; only
  `cave_gateway_required_for_locked_plan` earns the one retry without the plan.
  The route is resolved ONCE at `startCodingSession` and pinned on
  `session.route`; every turn is handed it via the internal `caveRoute` option,
  so a session makes exactly one runtime-ensure attempt however many turns it
  runs, and session mode governs (degradation is sticky, and a turn override can
  never re-open routing). Caller `overrides`/`runOverrides` face
  `rejectInternalRunOptions` before any session-internal field is merged.
  Tool containment is realpath-based (a symlink out of the workspace is out),
  and `bash` runs its command in its own process group so a timeout kills the
  tree instead of waiting on a backgrounded child's inherited stdout. `bash` is
  **uncontained by design** — it runs arbitrary host commands with the user's
  privileges — but its subprocess env is a fixed shell/locale allow-list, not a
  spread of `process.env`, so a model-driven command cannot read the framework's
  own account/provider credentials (`CAVE_API_KEY`, `ANTHROPIC_API_KEY`, …) and
  exfiltrate them (issue #143).
  Bills print token counts labelled `inferred (local estimate)` and spend in USD
  with its `priceBasis` — no dollar figure is ever attached to a saving; a
  zero-turn session prints an honest absence instead of basis-labelled zeros.
  `proveRecovery` runs the real engine compress/retrieve pair and reports the
  sha256 comparison. Live sessions are lock-ineligible by construction (host
  mode anywhere in the graph, root or subagent, is refused by `compile`).
  Example wrapper: `examples/coding-agent/`;
- `src/claude.ts` — public unlocked Claude Agent SDK facade;
- `src/claude-runtime.ts` — exact-pinned public Claude executor. Public calls
  cannot inject build identity. Every locked/candidate call rejects before SDK
  or MCP launch pending current source/runtime provenance, per-turn semantic
  bills, byte-exact CCR proof, cached-substitution evidence, and parity replay.
  Memory and framework subagents also remain fail-closed. Public tools are
  read+inline only, inherited `x-cave-*` headers are stripped, model-specific
  thinking capability is resolved before spend, and provider output usage is a
  hard terminal ceiling. SDK aggregate output stays provider-reported, while its
  unavailable authoritative thinking split is explicitly marked unavailable;
- `src/adapters.ts` — public advanced adapter surface with explicit bundle/
  dependency manifest digests and executable exact-pinned Vercel AI SDK 7.0.43,
  Eve 0.29.2, and Mastra 1.55.0 bridges. Every call binds matching harness lock,
  plan, Context IR, upstream identity, response model, complete usage, transforms,
  recovery, and catalog cost. Eve supports reasoning-off locks because its durable
  event contract omits reasoning usage. Pre-execution limit support is deliberately
  framework-specific: Mastra alone accepts the adapter's opt-in `maxSteps`, passed
  unchanged to `Agent.generate` and recorded in the adapter contract. Omitting it
  preserves the existing call shape. Vercel's `stopWhen` belongs to construction of
  the already-built `ToolLoopAgent`, not its generic `generate` call; Eve's client
  `send` API exposes no server execution limit. Those integrations therefore require
  an agent-construction/server-definition boundary before Caveman can enforce a
  native limit. The Claude facade already forwards operator-supplied `maxTurns` and
  `maxBudgetUsd`; its task-budget field does not qualify because upstream documents
  task budgets as advisory and unsupported on Claude Code/Cowork, while its
  provider-output check is post-execution. Neither is described as an adapter hard
  cap. None of these
  adapter controls proves dollar savings, a reserve-guaranteed cost cap, or fanout;
- `src/cli.ts` — `dev`, `build`, `check`, zero-spend `doctor`, `register`;
- `src/budget.ts` — the run budget contract. `RunOptions.budget` declares
  exactly one denomination (`maxUsd` at public catalog list prices, or
  `maxTokens`), runtime-gated on two independent grounds: the catalog must
  price the model, AND the run must be billed in dollars — a Claude Pro/Max
  subscription reached through Pi's credential store fails closed as
  `cave_budget_denomination_unavailable`, read from `checkAuth` and never
  inferred from the model. The regime is judged on the credential that
  actually pays, so the check runs AFTER routing and does not apply to a
  caller-supplied `streamFn` (that transport never asks Pi to authenticate
  anything) or to a gateway-routed run (the account key pays, not the local
  login). That last exemption holds only where the gateway supplies the
  provider credential. Gateway readiness makes that boundary explicit:
  managed returns `billing: "managed"`, standalone returns `billing: "byok"`,
  and missing/unknown billing provenance falls through to the local credential
  gate rather than authorizing dollars. The Claude lane reads the selected
  `apiKeySource` from the SDK's first init message: OAuth/unknown auth reports
  token counts but `costUsd: 0`, `priceBasis: "unpriced"`, and unpriced receipt
  calls; `maxBudgetUsd` requires a positively identified API-key source.
  Subscription dollars are fiction. Enforcement is reserve-and-clamp, one mode, no soft
  option: each call reserves its worst case (byte-derived input ceiling capped
  at the context window, times the catalog's worst rate, plus the configured
  output allowance), and a remainder that cannot cover the full allowance
  clamps the call's output down to what it affords, to
  `OUTPUT_CLAMP_FLOOR_TOKENS`. The input ceiling includes whatever the request
  could still GROW by if `onPayload` restores uncompressed originals on cache
  drift, so the hold bounds the payload that actually leaves. Below the floor
  the run stops **between** calls and returns a normal result carrying
  `RunResult.stopReason` — never a throw, never mid-tool, and an in-flight call
  always finishes and is counted. The runtime never *chooses* to spend past
  max; when a provider nonetheless reports more than could be bounded, the
  ledger records the REAL amount (never clamped — a rewritten ledger is fake
  accounting), sets `capBreached` with a signed `overspent` on both
  `RunResult` and its receipt, and funds nothing further — reserve, carve and
  tranche release all refuse. `spent > max` never appears without that flag.
  The FLAG rolls up from any subagent wallet that breached beneath the run
  (the ordinary shape, since wallets are small carves); the AMOUNT does not —
  `overspent` is always this level's own `max(0, spent − max)`, because
  settling a carve books the child's real spend against the parent too, and
  summing would count the same money twice and could print a figure larger
  than the whole tree spent. Each subagent's amount is on its own receipt.
  `capBreached` sits beside `stopReason` because both a clean stop at the cap
  and a breached one report `budget_exhausted`.
  `RunOptions.deadlineMs` stops at the same points. `maxCostUsd` is the older
  error-terminating cap and cannot be combined with `budget`. `budget.ts` also
  owns `RunResult.receipt`: every run — budgeted or not — returns the per-call,
  per-tool, per-subagent breakdown plus tranche history. Its money figures are
  **estimated list-price subtotals** from the public catalog, never invoices;
  an unpriced call is flagged, never counted as free. Serialized receipts carry
  `schema: caveman.agent.run-receipt.v1` and must validate against
  `public/shared/contracts/schemas/agent-run-receipt.schema.json`. That shared
  shape is not sent through the anonymous CLI telemetry lane; future hub upload
  requires separate authenticated, tenant-scoped consent. Under a budget,
  `subagent()` caps become **wallets**: the child's `maxCostUsd` (USD runs) or
  `maxTokens` (token runs) is carved out of the parent's *remaining* budget
  synchronously at spawn, so parallel spawns cannot double-spend, and the
  unspent remainder returns to the parent when the child finishes. A revoked
  parent revokes every wallet under it. `RunOptions.maxSubagentDepth` defaults
  to 2 and is capped at `ABSOLUTE_SUBAGENT_DEPTH_LIMIT`. Budget can be **staged**:
  `budget.initialUsd`/`initialTokens` meters the run against a first tranche and
  `createBudgetController()` + `RunOptions.budgetController` lets the developer's
  own deterministic checkpoints release more, up to `max` — releasing past `max`
  throws at the release site. No model can reach the controller (detection law 1:
  never a model in the money path), and a controller is inert outside its run.
  `RunOptions.onBudgetExhausted` is `"stop"` by default; a handler instead gets
  the read-only exhaustion context between calls (never mid-tool) and answers
  `"stop"` or `{ release, reason }`, which tops up a tranche through the same
  `max`-bounded mechanism. Exactly one escalation per exhaustion. Pausing and
  resuming a run from a serializable handle is deliberately not built;
- `src/breakers.ts` — opt-in deterministic circuit breakers
  (`RunOptions.breakers`): repeated-tool-call loop detection (exact
  tool+normalized-args hash within a configurable assistant-turn window,
  default 8, with `tool({ allowRepeat: true })` for legitimately repetitive
  tools), a no-progress window over turn outcome signatures, a
  per-turn fan-out cap, and retry budgeted in the run's denomination rather than
  by attempt count. Each retry takes a real BudgetMeter hold; pre-stream
  failures cancel at measured zero, successful attempts settle provider usage,
  and receipt events expose reserved + measured spend with basis. Old exact
  repeats decay out of the turn window instead of poisoning a long run. Local
  enforcement shares worker F16's H6 edge rule — including exclusion of a
  repeat following a failed attempt — but does not claim parity with worker-side
  session SCC + population Isolation-Forest finding arithmetic. No model runs
  anywhere in this path.
  No-progress signatures include tool identity/result; successful declared
  writes reset that window because identical text cannot prove host state stayed
  unchanged. Breaking stops between calls with
  `stopReason: "loop_detected"` / `"no_progress"`; the fan-out cap only blocks
  the extra calls. Every decision lands on `receipt.breakers`;
- `src/compaction.ts` — budget-triggered compaction, and **the only place in this package
  that rewrites model-visible context**. That is why it lives here: compaction
  is a model-visible rewrite, so it can exist only where the builder owns the
  context — no wrap or gateway path ever performs it. The exhaustion ladder is
  **evict → summarize → clamp → stop**. Default-on compaction triggers when
  remaining budget falls below four full cold next-call ceilings; `"stop"`
  skips that pre-emptive rung and only clamps/stops once a call stops fitting.
  Eviction is free and deterministic: stale tool output becomes a
  citation carrying its digest, selected by role and freshness — the class is
  safe to elide because every runtime tool result the IR lowers carries
  `recovery: "exact_ccr"`, but the choice is not driven off each segment's own
  `recovery` field.
  Summarization is a real provider call metered from the same budget and from
  every ancestor subagent wallet, built by the same request shape as a working
  call — same system prompt, same tool definitions, same history, same gateway
  headers, instruction appended last. Its usage joins `RunResult`'s own totals,
  not just the receipt. The rung is closed once the run has decided to stop: a
  turn that asked for no tools, a tripped breaker, or an expired deadline all
  skip it, because no working call would follow. Its reserve is priced **cold,
  always** — the rewrite diverges from the working call's prefix at its first
  changed message, so a warm read there is not evidence for a warm read here.
  Earlier timing makes the own-model default reachable without discounting its
  cold reserve; a cheap-class summarizer remains an opt-in gated on its context
  window covering the history. Cold pricing is not the whole story: the input ceiling is a UTF-8 BYTE
  count (~3-4x the real token count), so both the working call and the
  summarizer are priced ~4x high, which pushes the affordability trigger earlier
  than a true-token ceiling would. Tightening it needs a provider count-tokens
  endpoint (issue #165); until then the byte bound is kept because it never
  under-reserves. A subagent with a carved wallet uses that child meter as its
  sole economic boundary, so its compaction can run and rolls usage into the
  parent receipt; an unfunded child cannot borrow around the parent. Other
  preconditions: a yield floor and headroom for several working
  calls. `maxCompactions` counts attempts that actually reserved — a free
  decline does not burn it. Safeguards after: schema-validated
  sectioned summary (invalid ⇒ discard and clamp), a constraint-integrity
  assertion comparing the accepted rewrite's CONTENT against every pinned
  segment (identity comparison cannot fail), an inflation guard, and a
  self-contained tail so no tool result outlives its call. `receipt.compactions`
  keeps the REAL metered cost and the MODELED effect in separate fields with
  separate bases; the word "saved" appears nowhere.

`doctor` is framework readiness truth surface: Node, sandbox, engine registry,
runtime CLI, project/Context IR, lock drift, provider selection, and per-harness
locked-execution state. Caveman public CLI version probe is `caveman version`
(not `--version`). Optional project/provider warnings do not hide foundation
failures; Claude detail distinguishes public execution from fail-closed Cave
Build execution; third-party adapter readiness remains separate per harness.

Claude Agent SDK dependency is governed by Anthropic Commercial Terms linked
from its README, not package MIT license. Keep disclosure in public README.

Run `pnpm --dir public/agent test`. Unknown state fails closed. Transform failure
passes original bytes. Missing usage/pricing/eval/recovery writes no optimized
lock. Local evidence is always `inferred`; this package never mints verified
savings.

Authority: `docs/strategy/EFFICIENT_AGENT_BUILDER_SPEC.md`.
