# public/agent

`@caveman/agent`: opinionated TypeScript efficiency framework over exact-pinned
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
  event contract omits reasoning usage;
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
  provider credential: on a **BYOK gateway it does not**, and a local
  subscription token can pay while `maxUsd` is accepted — the SDK cannot yet
  tell the two apart (issue #84). The Claude lane refuses `maxBudgetUsd` without an API key for the
  same reason: subscription dollars are fiction (ADR 0023). Enforcement is reserve-and-clamp, one mode, no soft
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
  shape is not sent through ADR 0032's anonymous CLI lane; future hub upload
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
  tool+normalized-args hash, with `tool({ allowRepeat: true })` for legitimately
  repetitive tools), a no-progress window over turn outcome signatures, a
  per-turn fan-out cap, and retry budgeted in the run's denomination rather than
  by attempt count. Local exact-repeat enforcement shares worker F16's H6 edge
  rule — including exclusion of a repeat following a failed attempt — but does
  not claim parity with worker-side session SCC + population Isolation-Forest
  finding arithmetic (tracked in #81). No model runs anywhere in this path.
  No-progress signatures include tool identity/result; successful declared
  writes reset that window because identical text cannot prove host state stayed
  unchanged. Breaking stops between calls with
  `stopReason: "loop_detected"` / `"no_progress"`; the fan-out cap only blocks
  the extra calls. Every decision lands on `receipt.breakers`;
- `src/compaction.ts` — compact-at-max, and **the only place in this package
  that rewrites model-visible context**. That is why it lives here: compaction
  is a model-visible rewrite, so it can exist only where the builder owns the
  context — no wrap or gateway path ever performs it. The exhaustion ladder is
  **evict → summarize → clamp → stop**, triggered the moment the reserve check
  fails (no pre-emptive threshold), toggled by `budget.onExhausted` (default
  `"compact"`). Eviction is free and deterministic: stale tool output becomes a
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
  The consequence is deliberate: on the run's own model the summarizer's
  ceiling is essentially the working call's, so at the trigger the
  affordability precondition cannot clear and the run clamps and stops. The
  rung is reachable when the summarizer is genuinely cheaper, which is what the
  cheap-class opt-in (gated on its context window covering the history) exists
  for. Cold pricing is not the whole story: the input ceiling is a UTF-8 BYTE
  count (~3-4x the real token count), so both the working call and the
  summarizer are priced ~4x high, which pushes the affordability trigger earlier
  than a true-token ceiling would. Tightening it needs a provider count-tokens
  endpoint (issue #123 follow-up); until then the byte bound is kept because it
  never under-reserves. A **subagent never compacts** at all: `subagent()` derives the wallet
  and the legacy spend ledger from one `maxCostUsd`, and the ledger prices
  every call at the model's whole context window, so the turn the wallet
  triggers on is the turn the ledger refuses to fund (issue #83). Other preconditions: a yield floor and headroom for several working
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
