# `@caveman-ai/agent`

Give every production agent a local catalog-price guard and a token bill.

Define instructions, tools, context, evals, and budget in TypeScript. Caveman runs
agent, reports provider usage, and shows which parts of context consumed tokens.
It can search for cheaper context plan, but writes locked Cave Build only after
every declared eval passes.

Local Caveman Engine is optional. With Engine running, eligible context can use
recoverable local compression. Without it, SDK calls provider directly in explicit
observe-only mode: no transforms or Caveman gateway telemetry. Provider usage and
local context estimates remain available.

## Quick start

Requires Node.js 22.19+ and one supported provider credential.

```bash
npm create @caveman-ai/agent@latest my-agent
cd my-agent
npm run dev
```

Two commands, no Caveman account or hosted Caveman service. Provider credential
and network still required. On machine that has never seen Caveman, first run
looks like this:

```console
$ npm create @caveman-ai/agent@latest my-agent
$ cd my-agent && npm run dev

  cave: observe-only — engine/gateway unavailable; transforms and gateway
  telemetry off (provider usage and local context estimates remain available)

  agent > what does src/index.ts export?
  … model answers through your own provider credential, direct to the provider …
```

`npm run dev` auto-starts local Cave Runtime when installed. When unavailable,
run uses **observe-only** mode: provider's own base URL, no transform, no gateway
telemetry, and `RunResult.mode: "observe-only"`. Provider usage and local context
estimates remain available; no efficiency result is claimed. To enable Engine:

```bash
npm i -g @caveman-ai/cli && caveman start
```

Then same commands can run `mode: "optimized"`, routed through local gateway with
eligible transforms and context telemetry. Gateway proxies `anthropic`, `openai`,
and `google`; other providers go direct and report `observe-only`. Set `cave:
"off"` in `RunOptions` to choose observe-only. Run carrying Cave Build lock or
candidate plan refuses silent downgrade with
`cave_gateway_required_for_locked_plan`.

Framework accepts loopback runtime only when health identity, run state, PID, and
executable ownership validate. Unrelated local listener never receives provider
traffic; run goes direct in observe-only mode. Local results remain `inferred`;
verified savings stay `$0` until active production traffic passes separate rollout
and ledger gates.

Run a zero-provider-call readiness check before first use or deployment:

```console
$ npx caveman-agent doctor          # add --json for the machine-readable report
PASS node         Node 22.19.0
PASS sandbox      tool sandbox containment probe passed
WARN engine       Caveman engine not found — transforms disabled (observe-only)
WARN runtime_cli  Caveman runtime CLI unavailable — runs stay observe-only
WARN gateway      gateway not reachable at 127.0.0.1:8787 — telemetry off
...
run mode: observe-only (no transforms or gateway telemetry)
next: npm i -g @caveman-ai/cli && caveman start
```

Missing Engine, runtime CLI, or gateway is WARN and exits 0 because observe-only
runs still work. Node version, broken sandbox containment, invalid config, or lock
drift fails check. Doctor also checks engine registry, gateway reachability,
project/config load, Context IR, and provider selection.

Harnesses report separately. Pi can execute locked builds. Claude public lane is
unlocked and every Claude Cave Build stays fail-closed pending equivalent source,
budget, recovery, cache, and replay evidence. Vercel AI SDK 7.0.43, Eve 0.29.2,
and Mastra 1.55.0 expose executable locked adapters through
`@caveman-ai/agent/adapters` when foundation checks pass.

Adapter frameworks are optional exact-pinned peers; install only lane used:

```bash
npm install @caveman-ai/agent ai@7.0.43
npm install @caveman-ai/agent @mastra/core@1.55.0
npm install @caveman-ai/agent eve@0.29.2
```

Eve 0.29.2 requires Node.js 24+. Base SDK, Vercel, and Mastra lanes keep package
minimum Node.js 22.19. Adapter startup reads installed framework package version
and rejects missing or drifted versions before model execution.

Security audit boundary (checked 2026-08-10): shipped runtime dependencies pass
`npm audit --omit=dev` with zero advisories. Full optional adapter/dev graph has
one upstream low-severity resource-consumption advisory through Mastra's exact
`@ai-sdk/provider-utils` 3.0.30 compatibility dependency. Latest Mastra 1.57.0
still pins that version, and no patched 3.x release exists. Release CI rejects
any runtime advisory and any high/critical advisory across full graph.

## Smallest agent

```ts
import { agent, auto } from "@caveman-ai/agent";

export default agent({
  id: "support",
  instructions: "Answer from policy. Never invent policy.",
  model: auto(),
});
```

`auto()` selects configured/default model. Resolution order is `CAVE_MODEL`,
`.caveman/provider.json`, then baseline model for the sole supported provider
credential. It never classifies tasks or routes between models.

Run agent from code:

```ts
import support from "./agent.js";
import { run } from "@caveman-ai/agent";

const result = await run(support, "Can I get a refund?");
console.log(result.text);
console.log(result.contextBill);
```

That run works on a machine with nothing but Node and a provider credential. It
returns `mode: "observe-only"` there — direct to the provider, no transform, no
gateway telemetry. Provider usage and local context estimates remain available.
With the Caveman runtime installed and started the same call returns
`mode: "optimized"`.

`usageBasis` covers provider-reported aggregate usage. Reasoning breakdown has
its own `reasoningUsageBasis`: when reasoning-capable Pi models omit that
optional split, unlocked runs label it unavailable, while Cave Builds and
subagent accounting fail closed instead of treating missing as zero.

`RunOptions.maxCostUsd` sets a best-effort local spend cap for one run, in USD
at public catalog list prices. It is not financial enforcement: no provider
invoice, platform quota, or cross-process reservation is involved. When set,
every root and descendant model call reserves the catalog worst-case price of
that call against the cap before the request and settles measured catalog cost
after it. Exhaustion ends the run with `cave_run_cost_budget_exceeded` before
the next model call; emitted records stay, and no `run_end` result is produced.
A model the public catalog cannot price cannot be capped: with the cap set, such
a call fails closed instead of consuming $0 of budget. Leave the cap unset for
runs on unpriced models and bound them by declared call ceilings.

Use `stream()` for typed run, context, Pi, completion, and error events.
Calling iterator `return()` aborts in-flight provider/tool/subagent work before
conversation ownership releases. Terminal `run_end` and `run_error` events
release ownership before delivery, so manual consumers cannot strand session.
Both terminal events carry the ledger: `run_error.receipt` is the partial
receipt of what was spent before the failure, and the promise entry points
throw a `CavemanRunError` whose `receipt`/`cause` carries the same — a run that
fails after spending never loses its per-call breakdown. Every model call is
capped by `RunOptions.maxModelCalls` (and tool calls by `maxToolCalls`, each
default 64); reaching the model ceiling ends the run gracefully with
`stopReason: "call_budget_exhausted"`, not a throw.
For explicit multi-turn code, create one conversation and reuse it:

```ts
import { createConversation, run } from "@caveman-ai/agent";

const conversation = createConversation();
await run(support, "My order is late.", { conversation });
const followUp = await run(support, "What should I do next?", { conversation });
```

`npm run dev` does this automatically. Dev reuses one immutable staged copy of
complete project-relative source graph until watched project source, config,
eval, or file context changes. Reload then replaces snapshot while successful
conversation history remains local to parent process. Executed definition,
sandboxed tools, declared root/child file sources, and Cave Build identity all
use same snapshot.
Definition, model, or plan changes rotate cache epoch without replaying stale
prefix bytes. Failed turns roll back conversation/cache state, and concurrent
use of one conversation fails closed. Restarting command starts fresh ephemeral
session. Installed package code remains pinned and cached. Node ESM cannot tear
down timers or listeners created by old module graph after hot reload: keep agent
module top level side-effect-free, or restart dev command after editing module
that owns process resources.

Programmatic agents with tools and default `sandbox: "required"` must pass
`RunOptions.entryPath`, pointing to module that exports agent definition. CLI
supplies this automatically. Before provider traffic, framework copies complete
project source graph into per-run immutable staging, imports tools only from that
snapshot, and tears staging down when stream settles. Framework refuses to run
such tools in-process. Trusted tests may opt into `sandbox: "fixture"`.

Native Windows supports ordinary agent runs, runtime/engine startup, and explicit
`sandbox: "host"` coding tools through `cmd.exe`. `sandbox: "required"` remains
fail-closed on native Windows because package has no verified OS network-isolation
boundary there; use WSL2 for production sandboxed tools. `doctor` reports exact
`cave_sandbox_os_network_isolation_unavailable` failure and WSL2 remedy. Never
replace required sandbox with fixture mode in production.

`sandbox: "host"` is the third mode, for interactive and coding agents whose
tools need real host access. It is explicit opt-in and never a default: closures
run in this process with no tool worker and no `entryPath` requirement, and
`effect: "write"` tools execute instead of being blocked. Effect declarations
stay mandatory — host mode changes enforcement, not declaration. A host-mode
agent may not sit under a sandbox-required ancestor, so a subagent cannot use it
to leave its root's containment. Live host runs are never lock-eligible: an
unsandboxed run cannot produce a lock, so `compile` refuses a host-mode agent
with `cave_host_sandbox_lock_ineligible` before any search run. Locked builds
for coding agents compile against fixture corpora with a contained sandbox mode
instead.

## Claude Agent SDK

Use same agent definition through exact-pinned Claude Agent SDK lane:

```ts
import { runClaudeAgent } from "@caveman-ai/agent/claude";
import { fileURLToPath } from "node:url";
import support from "./agent.js";

const result = await runClaudeAgent(support, "Can I get a refund?", {
  entryPath: fileURLToPath(new URL("./agent.js", import.meta.url)),
  maxTurns: 8,
  maxBudgetUsd: 0.50,
});
```

Public lane is always unlocked and returns `claimBasis: "inferred"` with
verified savings `$0`. It disables Claude built-in tools and settings, maps
declared read+inline Caveman tools into one in-process MCP server, reuses same immutable
source snapshot and sandbox executor as Pi, enforces declared model/reasoning/
output schema, and sends requests through Caveman Anthropic proxy. Memory and
framework subagents fail closed in Claude lane until equivalent semantics exist.
Write/idempotent/external tools and `auto`/page/compress/CCR results also reject
before SDK/tool execution; this prevents side effects followed by unavailable
recovery. Framework strips inherited `x-cave-*` headers before adding its own
content-blind, explicit-pass-through metadata.
Caller cannot inject Cave Plan or Cave Build identity.
Public lane defaults to 16 turns. `maxTurns` and `maxBudgetUsd` set explicit SDK
caps; declared `output({ maxTokens })` becomes SDK task token budget and a
terminal provider-usage ceiling. Reasoning is model-capability aware: Haiku 4.5
uses fixed manual thinking with no `effort`; known adaptive models use adaptive
thinking plus declared effort; unknown capabilities fail before provider spend.
Manual thinking requires `maxTokens` above its 1,024/4,096/8,192-token
low/medium/high budget. Pinned Agent SDK reports aggregate output tokens but no
authoritative thinking-token split, so results expose
`reasoningUsageBasis: "unavailable"`; `reasoningTokens: 0` is a non-evidence
placeholder and must not be read as measured zero.

Package pins `@anthropic-ai/claude-agent-sdk` 0.3.220 and Claude Code 2.1.220
identity. Anthropic SDK is not MIT; its README points to Anthropic Commercial
Terms and describes data collection. Framework source remains MIT, but users of
Claude lane must review Anthropic terms and data policy.

## Tools

Tool schema, side effect, timeout, and result policy are explicit:

```ts
import { schema, tool } from "@caveman-ai/agent";

const lookupPolicy = tool({
  name: "lookup_policy",
  description: "Read current refund policy.",
  input: schema.object({ region: schema.string() }),
  effect: "read",
  result: "auto",
  async execute({ region }) {
    return { region, refundWindowDays: 14 };
  },
});
```

`input` also accepts Standard Schema v1. Schemas implementing Standard JSON
Schema v1 convert automatically to draft-07 provider schema. Validation-only
Standard Schema libraries pass explicit `inputJSONSchema`; framework still runs
schema validator before tool code, including async validation and transforms.

Effects: `read`, `write`, `idempotent`, `external`.

Result policies:

- `auto` lets locked plan choose inline, paging, compression, or exact CCR.
- `inline` keeps result in current context.
- `page` exposes bounded pages.
- `compress` uses eligible locked transform.
- `exact_ccr` replaces only after byte-exact recovery is stored.

Production tools run in a restricted subprocess under an OS network boundary —
a network namespace on Linux (`unshare --net`), `sandbox-exec (deny network*)`
on macOS — so egress is blocked at the kernel, not by in-process monkeypatching
(which is defense-in-depth only and cannot be a boundary). `sandboxProfile.network:
true` is not a way to allow egress: it requests UNBOUNDED egress and fails closed
with `cave_sandbox_network_egress_unbounded`, because there is no scoped-egress
mechanism yet (a parent-owned CONNECT proxy is the tracked follow-up). See
`SANDBOX_THREAT_MODEL.md` for the per-platform boundary and its known gaps (notably
Linux unix-domain sockets, which a network namespace does not cover). A live
profile may request only one
runtime-owned provider capability: `ANTHROPIC_API_KEY`, `OPENAI_API_KEY`, or
the Google capability (`GEMINI_API_KEY` and `GOOGLE_API_KEY` are aliases).
Signing, deployment, bootstrap, database, cloud, loader, and ambient
environment names are categorically denied, and the child starts from a fixed
baseline (`LANG`, `LC_ALL`, `PATH`, `TZ`, and the fixture marker) rather than a
spread of the parent environment. Host filesystem stays restricted to staged
source plus ephemeral workspace.
Child-process permission fails closed until OS-level descendant containment is
portable and verifiable.
Framework subagents may use normal tools and delegate further subagents. CLI
passes one root entry automatically; programmatic runs pass `entryPath` once at
root. Framework keeps descendant route private, verifies root and selected-tool
definition digests after sandbox re-import, then executes exact child tool in
restricted subprocess. Descendant model calls reserve catalog-priced
worst-case spend against every ancestor `maxCostUsd` cap before provider call;
every turn must report complete usage and exact requested provider/model.
`cave_` tool names remain reserved for framework recovery and memory tools.

## Context, memory, output

```ts
import { context, file, memory, output, schema } from "@caveman-ai/agent";

const playbook = context({
  id: "support.playbook",
  kind: "skill",
  source: file("./support.md"),
  stability: "build",
  safety: "S0",
  priority: "required",
});

const supportMemory = memory({
  namespace: "support",
  ttl: "30d",
  recallBudget: 1_200,
  consent: "local_only",
});

const answer = output({
  maxTokens: 500,
  schema: schema.object({ answer: schema.string() }),
});
```

Build-stable context enters frozen prefix. Session and turn context stay live.
Runtime rejects volatile data in stable cache zone and fails open to original
provider-visible bytes on drift or transform failure. Cave Build binds static
Context IR only; eval inputs, user turns, history, and tool results remain
runtime evidence and never make lock depend on one fixture prompt.

`memory()` is a **durable, tenant-scoped local store**, so `ttl: "30d"` is a real
30-day promise — entries persist across process restarts, not just for the life
of one process. It is deliberately dep-free: a per-namespace JSON file written
with an atomic temp-write + rename (no SQLite driver pulled into the package's
tight dependency surface). The `cave_memory_remember` / `cave_memory_search`
tools read and write it, and a memory past its declared `ttl` is evicted on read
(purged from disk, not just filtered from the response). Recalled memory stays
basis `inferred` — nothing here is ever a saving.

Entries are keyed by **(tenant, agentId, namespace)**, so two `AgentDefinition`s
declaring the same namespace in one process never see each other's memories, and
an embedding server isolates tenants. `RunOptions.memory` controls where and for
whom: `root` (default `CAVE_AGENT_MEMORY_ROOT`, else `~/.caveman/agent-memory`)
points at your own location, and `tenant` (default single-tenant) scopes per
tenant. Only `provenance: "local"` + `consent: "local_only"` are supported — a
shared-backend config is refused at `memory()` construction, never at tool-call
time. In-process writes are serialized per file; across processes an atomic
rename prevents a torn file, with last-writer-wins on a concurrent update.

## The coding agent (`@caveman-ai/agent/code`)

The new caveman-code: an interactive coding agent built on this framework, with
host-sandbox `read_file` / `grep` / `bash` / `edit_file` tools over one
workspace. It is the successor to the deprecated `caveman-code` fork.

```ts
import { createCodingAgent, runCodingSession } from "@caveman-ai/agent/code";

const agent = createCodingAgent({ workspace: process.cwd() });
await runCodingSession({ agent });
```

**Optimized by default, observe-only loudly.** With the Caveman engine present,
the session starts the local Cave runtime and runs with a default efficiency
plan: one recoverable route per live-zone segment kind — `tool_result` through
`caveman.engine.terminal.v1`, `history` through `caveman.engine.text.v1` — with
`cave_retrieve` registered so the model can pull exact original bytes back. Only
CCR-recoverable transforms are eligible; `toon` is forced-only and never routed
by default, and no lossy-without-recovery class enters a default plan. One route
per kind is deliberate: two routes matching one runtime segment collapse into
`dynamic_route_ambiguous` and the segment passes through untouched.

When the runtime cannot be reached, the session degrades to observe-only — but
never silently. It prints the banner (engine/gateway unavailable, transforms and
gateway telemetry off, provider usage and local context estimates still available,
`npm i -g @caveman-ai/cli && caveman start`), records it on
`session.notices`, and shows the mode on the prompt and in every turn's bill. A
turn that carries a plan refuses to degrade on its own: it throws
`cave_gateway_required_for_locked_plan`, and only that failure earns one retry
without the plan. The runtime is probed once per session, not once per turn:
the answer is pinned on the session, so a machine with no runtime pays one
failed start attempt for the whole session, and once a session has degraded it
stays degraded.

**The token bill is a token count.** After every turn the session prints context
tokens before and after transforms (from `RunResult.transformTrace`), tokens
saved labelled `inferred (local estimate)`, provider usage with its
`usageBasis`, and spend in USD with its `priceBasis`. Savings are never
expressed in currency, and a local session mints nothing — verified savings are
platform evidence, not something a laptop can produce.

**Recovery is proved, not asserted.** `/prove-recovery` (and one automatic line
after the first compression) takes a recorded tool output, runs it through the
same engine compress/retrieve pair the plan and `cave_retrieve` use, and reports
the sha256 comparison:

```
recovery proof: read_file:big.txt round-trip OK (sha256 match efac7be09c1a)
```

Mismatches print as `FAILED (sha256 mismatch)`; the plan falls back to the
original body. Tool output is capped **before** compression (24 KB for
`read_file` and `bash`, 16 KB for `grep`, all under the runtime's inline
tool-result ceiling), so a runaway command cannot blow the context even with no
engine installed.

Live coding sessions are never lock-eligible: host mode makes `compile` refuse
with `cave_host_sandbox_lock_ineligible`, so nothing a session does becomes a
Cave Build. Internal runnable example: `examples/coding-agent` (source repository only).

## Evals and Cave Build

```ts
import { eval as defineEval } from "@caveman-ai/agent";

export const refund = defineEval({
  id: "refund",
  approved: true,
  input: "Can I get a refund?",
  quality: [
    { type: "contains", fragments: ["14 days"] },
    { type: "tool_called", tools: ["lookup_policy"] },
  ],
});
```

```bash
npm run build
npm run check
```

Build performs finite search with five seeds per approved fixture. No optimized
lock is written when usage is missing, model is unpriced, cache regresses,
recovery fails, sandbox/privacy fails, quality drops, search is incomplete, or
cost ceiling is exceeded. `check` rejects drift before model call.
Successful build output names selected model, reasoning, transform routes,
baseline and selected public-catalog cost per task, inferred percentage change,
and completed eval evidence before printing lock identity.
Public `run()` cannot inject a plan or build identity. `npm run dev`, `build`,
and `check` own validated Cave Build execution. Locked/candidate execution checks
selected provider, model, and reasoning before provider traffic. Every provider
turn must return exact provider/model identity and arithmetically complete usage;
framework recomputes cost from public catalog.

Advanced build config lives in `@caveman-ai/agent/build`. Claude public execution
lives in `@caveman-ai/agent/claude`. `@caveman-ai/agent/adapters` exports executable
Vercel AI SDK, Eve, and Mastra adapters. Each requires a Cave Build whose harness,
adapter version, upstream version, selected plan, and Context IR match before
provider execution. Runtime result must then carry terminal text, actual response
model identity, complete provider usage, transform/recovery evidence, and a
public-catalog-priced model. Adapter recomputes cost; local result stays
`inferred` with verified savings `$0`.

Adapters accept exact-pinned upstream objects directly: Vercel `ToolLoopAgent`
7.0.43, Eve `ClientSession` 0.29.2, and Mastra `Agent` 1.55.0. Abort signals pass
through all three. Mastra processor retries are forced to zero. Eve aggregates
durable `step.completed` usage and verifies `session.started` runtime identity;
because Eve 0.29.2 omits reasoning-token usage, only `reasoning: "none"` Cave
Builds execute there. Vercel and Mastra reasoning builds require explicit
provider-reported reasoning usage. Dynamic Eve model identity, model drift,
missing usage, terminal failure, unpriced models, and version drift reject.

A Pi lock can never authorize Claude or third-party execution. Adapter identity
requires explicit built-bundle and dependency-lock SHA-256 values; function
source text is never artifact identity. Shared execution kernel owns
lock/harness/plan/Context-IR binding, agent-to-Context-IR lowering, plan
selection, usage validation, and catalog-cost finalization. Standalone proxy
records content-blind Claude groundwork:
ordered prefix-component hashes, actual transform IDs, compression counts, and
CCR handles. Framework does not treat those fields as sufficient proof: strict
Claude locked execution rejects before SDK/MCP launch until all named gaps close.

## Public surface

- `agent`, `run`, `stream`, `createConversation`, `auto`
- `tool`, `schema`, `artifact`, `subagent`
- `context`, `file`, `memory`, `output`
- `eval`
- Context IR types and lowering helpers
- `verifySandboxConformance`
- `runClaudeAgent` from `@caveman-ai/agent/claude` (unlocked/inferred only)
- `createCodingAgent`, `startCodingSession`, `runCodingTurn`, `runCodingSession`,
  `defaultCodingPlan`, `proveRecovery` from `@caveman-ai/agent/code`
- `createVercelAISDKAdapter`, `createEveAdapter`, `createMastraAdapter` from
  `@caveman-ai/agent/adapters`

CLI: `dev`, `build`, `check`, `doctor`, and `register`. `doctor` makes no model
request and prints `verified savings: $0` in human output.

Requires Node.js 22.19+. Framework package license: MIT. Claude Agent SDK use is
subject to Anthropic terms linked from that dependency's README.
