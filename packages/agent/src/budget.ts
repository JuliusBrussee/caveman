import type { BreakerEvent } from "./breakers.js";
import { catalogSearchCeiling } from "./catalog.js";
import { normalizeCompaction, type CompactionOptions, type NormalizedCompaction } from "./compaction.js";

/** Versioned cross-package wire identity for an SDK economic receipt. */
export const AGENT_RUN_RECEIPT_SCHEMA = "caveman.agent.run-receipt.v1" as const;

/**
 * The economic runtime's meter.
 *
 * Every figure here is a public-catalog **list-price subtotal**, not an
 * invoice. A USD budget can only bind on a model the catalog prices; an
 * unpriced model fails closed rather than treating an unknown price as zero.
 *
 * Denominations are runtime-gated: dollars only where the runtime
 * honestly meters dollars — the API-key provider paths this package drives —
 * and raw tokens where it does not. A run declares exactly one.
 */
export type BudgetDenomination = "usd" | "tokens";

/**
 * Why a run stopped issuing calls. `complete` is the ordinary end of an agent
 * loop; every other value means the runtime stopped the run between calls with
 * partial work in hand. Unknown values are not representable — a stop reason is
 * always one of these, so a consumer switch can fail closed on the default.
 */
export type RunStopReason =
  | "complete"
  | "budget_exhausted"
  | "deadline"
  | "loop_detected"
  | "no_progress"
  | "wallet_revoked"
  // The run hit its hard model-call ceiling (RunOptions.maxModelCalls, default
  // 64). Like every other stop reason this ends the run gracefully between
  // calls with the partial work, usage, and receipt in hand — never a throw
  // that would destroy the ledger of what was already spent.
  | "call_budget_exhausted";

/**
 * Smallest output allowance the clamp will hand a provider call. Below it a
 * clamped call buys a truncated fragment rather than progress, so the runtime
 * enters the exhaustion ladder instead of spending on one.
 */
export const OUTPUT_CLAMP_FLOOR_TOKENS = 256;

/**
 * Provider-side turn framing (role markers, tool-call envelopes) that our own
 * serialization of a context does not contain. Added per message when ceilings
 * are computed so the reserve stays an over-estimate.
 */
const MESSAGE_FRAMING_TOKENS = 16;

export interface RunBudget {
  /** Hard cap in USD at public catalog list prices. Mutually exclusive with `maxTokens`. */
  readonly maxUsd?: number;
  /** Hard cap in provider-counted tokens. Mutually exclusive with `maxUsd`. */
  readonly maxTokens?: number;
  /** Staged release: the run starts metered against this, not `maxUsd`. */
  readonly initialUsd?: number;
  /** Staged release: the run starts metered against this, not `maxTokens`. */
  readonly initialTokens?: number;
  /** Output floor for the clamp rung. Defaults to {@link OUTPUT_CLAMP_FLOOR_TOKENS}. */
  readonly outputFloorTokens?: number;
  /**
   * What the run does as reserve headroom approaches exhaustion. `"compact"`
   * is the default: compress context while the same cap can still fund the
   * rewrite and useful work. `"stop"` skips compaction and clamps/stops only
   * when the next call itself no longer fits.
   */
  readonly onExhausted?: "compact" | "stop";
  /** Tuning for the compaction rung. Ignored when `onExhausted` is `"stop"`. */
  readonly compaction?: CompactionOptions;
}

export interface NormalizedBudget {
  readonly denomination: BudgetDenomination;
  readonly max: number;
  readonly initial: number;
  readonly outputFloorTokens: number;
  readonly onExhausted: "compact" | "stop";
  readonly compaction: NormalizedCompaction;
}

/**
 * Validate a caller-supplied budget. Fails closed: an ambiguous, unbounded, or
 * self-contradicting budget is rejected before the first provider call rather
 * than silently degrading into no cap at all.
 */
export function normalizeRunBudget(budget: RunBudget): NormalizedBudget {
  const usd = budget.maxUsd !== undefined;
  const tokens = budget.maxTokens !== undefined;
  if (usd === tokens) throw new Error("cave_budget_denomination_ambiguous");
  const denomination: BudgetDenomination = usd ? "usd" : "tokens";
  const max = usd ? budget.maxUsd! : budget.maxTokens!;
  if (!Number.isFinite(max) || max <= 0) throw new Error("cave_budget_max_invalid");
  if (denomination === "tokens" && !Number.isSafeInteger(max)) {
    throw new Error("cave_budget_max_invalid");
  }
  const wrongInitial = denomination === "usd" ? budget.initialTokens : budget.initialUsd;
  if (wrongInitial !== undefined) throw new Error("cave_budget_denomination_ambiguous");
  const declaredInitial = denomination === "usd" ? budget.initialUsd : budget.initialTokens;
  const initial = declaredInitial ?? max;
  if (!Number.isFinite(initial) || initial <= 0 || initial > max) {
    throw new Error("cave_budget_initial_invalid");
  }
  if (denomination === "tokens" && !Number.isSafeInteger(initial)) {
    throw new Error("cave_budget_initial_invalid");
  }
  const outputFloorTokens = budget.outputFloorTokens ?? OUTPUT_CLAMP_FLOOR_TOKENS;
  if (!Number.isSafeInteger(outputFloorTokens) || outputFloorTokens <= 0) {
    throw new Error("cave_budget_output_floor_invalid");
  }
  const onExhausted = budget.onExhausted ?? "compact";
  if (onExhausted !== "compact" && onExhausted !== "stop") {
    throw new Error("cave_budget_on_exhausted_invalid");
  }
  return Object.freeze({
    denomination,
    max,
    initial,
    outputFloorTokens,
    onExhausted,
    compaction: normalizeCompaction(budget.compaction),
  });
}

/** Identity and shape of one prospective provider call. */
export interface CallCeiling {
  readonly provider: string;
  readonly model: string;
  /** Upper bound on the request's input tokens. Never an estimate that can be low. */
  readonly inputTokenCeiling: number;
  /** The output allowance this call would ask for. */
  readonly outputTokenCap: number;
}

export interface BudgetReservation {
  readonly amount: number;
  readonly outputTokenCap: number;
  settled: boolean;
}

/**
 * Worst-case price of one call in the meter's denomination, or `undefined` when
 * the catalog cannot price the model. Undefined is a hard stop for a USD
 * budget, never a zero: a cap that cannot be measured is not a cap.
 */
export function callCeilingCost(
  denomination: BudgetDenomination,
  call: CallCeiling,
  outputTokens: number,
): number | undefined {
  if (!Number.isSafeInteger(call.inputTokenCeiling) || call.inputTokenCeiling < 0 ||
      !Number.isSafeInteger(outputTokens) || outputTokens < 0) {
    return undefined;
  }
  if (denomination === "tokens") return call.inputTokenCeiling + outputTokens;
  return catalogSearchCeiling(
    `${call.provider}/${call.model}`,
    call.inputTokenCeiling,
    outputTokens,
  );
}

/**
 * Whether the remaining budget covers this call at its full output allowance.
 * Pure: unlike {@link planCall} it takes nothing, so it is safe to ask at any
 * point that is only deciding, not spending.
 */
export function callFitsBudget(meter: BudgetMeter, call: CallCeiling): boolean {
  if (meter.revoked || meter.capBreached) return false;
  const full = callCeilingCost(meter.denomination, call, call.outputTokenCap);
  return full !== undefined && full <= meter.remaining();
}

/** True when the public catalog prices this model, so a USD budget can bind on it. */
export function modelIsPriced(provider: string, model: string): boolean {
  return catalogSearchCeiling(`${provider}/${model}`, 0, 0) !== undefined;
}

/**
 * Upper bound on the input tokens of a request carrying `serialized` bytes over
 * `messageCount` messages.
 *
 * Every production tokenizer in the catalog is byte-level BPE, so a token
 * always covers at least one byte and the byte count is a true ceiling on the
 * token count. Provider-side turn framing is added on top, and the model's own
 * context window caps the result because a larger request cannot be served at
 * all. Under-reserving would let a run spend past its cap, so this stays a
 * hard over-estimate.
 *
 * This bound intentionally over-reserves. A provider count-tokens endpoint could
 * make it tighter, but the byte bound is the available ceiling in this layer and
 * never under-reserves.
 */
export function inputTokenCeiling(
  serializedBytes: number,
  messageCount: number,
  contextWindow: number,
): number {
  const framed = serializedBytes + Math.max(0, messageCount) * MESSAGE_FRAMING_TOKENS;
  const window = Number.isSafeInteger(contextWindow) && contextWindow > 0
    ? contextWindow
    : Number.MAX_SAFE_INTEGER;
  return Math.max(0, Math.min(framed, window));
}

/**
 * Byte length of a context as this process can serialize it. Unserializable
 * context returns `undefined`, which callers treat as "assume the whole context
 * window" rather than guessing a smaller number.
 */
export function serializedContextBytes(value: unknown): number | undefined {
  try {
    const encoded = JSON.stringify(value);
    if (encoded === undefined) return undefined;
    return Buffer.byteLength(encoded, "utf8");
  } catch {
    return undefined;
  }
}

/** A subagent wallet and the hold it placed on its parent. */
export interface BudgetCarve {
  readonly child: BudgetMeter;
  readonly reservation: BudgetReservation;
}

/** One staged release of budget. */
export interface BudgetTranche {
  readonly amount: number;
  readonly reason: string;
  readonly atCall: number;
}

/**
 * The per-run (or per-wallet) ledger.
 *
 * Invariant this class exists to hold: settled spend plus outstanding
 * reservations never exceeds `max`. Reservations are taken before a provider
 * request and replaced by the measured cost afterwards, so a call that turns
 * out cheaper than its worst case returns the difference to the run.
 */
export class BudgetMeter {
  readonly denomination: BudgetDenomination;
  readonly max: number;
  readonly outputFloorTokens: number;
  readonly onExhausted: "compact" | "stop";
  readonly compaction: NormalizedCompaction;
  private releasedAmount: number;
  private settledAmount = 0;
  private reservedAmount = 0;
  private revokedFlag = false;
  private breachedFlag = false;
  private callIndex = 0;
  private readonly trancheLog: BudgetTranche[] = [];
  private readonly liveWallets = new Set<BudgetMeter>();

  constructor(normalized: NormalizedBudget) {
    this.denomination = normalized.denomination;
    this.max = normalized.max;
    this.outputFloorTokens = normalized.outputFloorTokens;
    this.onExhausted = normalized.onExhausted;
    this.compaction = normalized.compaction;
    this.releasedAmount = normalized.initial;
  }

  /** Spend the meter has actually measured. */
  get settled(): number {
    return this.settledAmount;
  }

  /** Total released so far. Equals `max` unless the run uses tranches. */
  get released(): number {
    return this.releasedAmount;
  }

  get revoked(): boolean {
    return this.revokedFlag;
  }

  /**
   * True once measured spend has crossed `max`.
   *
   * The runtime never *chooses* to spend past max: every call is reserved at
   * its worst case first. A breach means reality came in above what the
   * provider's own contract allowed us to bound — and when it does, the ledger
   * records the real amount rather than clamping it, because a clamped ledger
   * is fake accounting. A breach is loud, signed on the receipt, and terminal:
   * a breached meter funds nothing further.
   */
  get capBreached(): boolean {
    return this.breachedFlag;
  }

  /** Signed amount past `max`. Zero when the cap held. */
  get overspent(): number {
    if (this.settledAmount <= this.max) return 0;
    const over = this.settledAmount - this.max;
    return this.denomination === "usd" ? roundUsd(over) : over;
  }

  get tranches(): readonly BudgetTranche[] {
    return Object.freeze([...this.trancheLog]);
  }

  /** Budget available to a new reservation right now. */
  remaining(): number {
    return Math.max(0, this.releasedAmount - this.settledAmount - this.reservedAmount);
  }

  /** Budget that could still be released without breaching `max`. */
  releasable(): number {
    return Math.max(0, this.max - this.releasedAmount);
  }

  /**
   * Release a further tranche. Throws when it would breach `max`: this is
   * pre-flight validation at a developer-controlled checkpoint, so the caller
   * asked for something the contract cannot grant.
   */
  release(amount: number, reason: string): BudgetTranche {
    if (!Number.isFinite(amount) || amount <= 0) {
      throw new Error("cave_budget_release_invalid");
    }
    if (this.denomination === "tokens" && !Number.isSafeInteger(amount)) {
      throw new Error("cave_budget_release_invalid");
    }
    if (typeof reason !== "string" || reason.trim() === "") {
      throw new Error("cave_budget_release_reason_required");
    }
    if (this.revokedFlag) throw new Error("cave_budget_revoked");
    // A breached ledger is dead. Releasing into it would record a tranche and
    // raise an escalation for money that can never be spent, and would read on
    // the receipt as a run that was still being funded after it went past cap.
    if (this.breachedFlag) throw new Error("cave_budget_cap_breached");
    if (amount > this.releasable()) throw new Error("cave_budget_release_exceeds_max");
    this.releasedAmount += amount;
    const tranche: BudgetTranche = Object.freeze({
      amount,
      reason,
      atCall: this.callIndex,
    });
    this.trancheLog.push(tranche);
    return tranche;
  }

  /**
   * Hold `amount` against the ledger. Returns `undefined` when it does not fit,
   * which is the caller's signal to clamp, compact, or stop — never to proceed.
   */
  reserve(amount: number, outputTokenCap: number): BudgetReservation | undefined {
    const held = this.hold(amount, outputTokenCap);
    if (held !== undefined) this.callIndex++;
    return held;
  }

  private hold(amount: number, outputTokenCap: number): BudgetReservation | undefined {
    if (this.revokedFlag || this.breachedFlag) return undefined;
    if (!Number.isFinite(amount) || amount < 0) return undefined;
    if (amount > this.remaining()) return undefined;
    this.reservedAmount += amount;
    return { amount, outputTokenCap, settled: false };
  }

  /**
   * Carve a subagent wallet out of what this meter has left.
   *
   * The whole wallet is held against the parent the moment the child is
   * spawned, before any await, so parallel spawns cannot both be funded from
   * the same remainder. The child is a meter in its own right, hard-capped at
   * the wallet, and whatever it does not spend returns to the parent when the
   * carve settles. `undefined` means the parent cannot fund this wallet.
   */
  carve(amount: number): BudgetCarve | undefined {
    if (!Number.isFinite(amount) || amount <= 0) return undefined;
    if (this.denomination === "tokens" && !Number.isSafeInteger(amount)) return undefined;
    const reservation = this.hold(amount, 0);
    if (reservation === undefined) return undefined;
    const child = new BudgetMeter({
      denomination: this.denomination,
      max: amount,
      initial: amount,
      outputFloorTokens: this.outputFloorTokens,
      onExhausted: this.onExhausted,
      compaction: this.compaction,
    });
    this.liveWallets.add(child);
    return Object.freeze({ child, reservation });
  }

  /** Return a carved wallet's unspent remainder to this meter. */
  settleCarve(carve: BudgetCarve): void {
    this.liveWallets.delete(carve.child);
    this.settle(carve.reservation, carve.child.settled);
  }

  /**
   * Replace a reservation with the measured cost of the call it covered.
   *
   * `actual` is recorded as it came in. It is never clamped to the reservation
   * and never floored at `max`: a ledger that quietly rewrites what a call cost
   * is a fake ledger, and the honest failure is a flagged breach, not a
   * flattering number.
   */
  settle(reservation: BudgetReservation, actual: number): void {
    if (reservation.settled) throw new Error("cave_budget_reservation_double_settle");
    reservation.settled = true;
    this.reservedAmount = Math.max(0, this.reservedAmount - reservation.amount);
    if (!Number.isFinite(actual)) {
      // A NaN/Infinity measured cost is not knowable, so it fails closed at the
      // reservation's worst case rather than booking $0. A call
      // whose real cost we cannot read is never a free call — booking zero would
      // be the flattering number this ledger refuses to write.
      this.settledAmount += reservation.amount;
    } else if (actual > 0) {
      this.settledAmount += actual;
    }
    if (this.settledAmount > this.max) this.breachedFlag = true;
  }

  /** Drop a reservation whose call never reached the provider. */
  cancel(reservation: BudgetReservation): void {
    if (reservation.settled) return;
    reservation.settled = true;
    this.reservedAmount = Math.max(0, this.reservedAmount - reservation.amount);
  }

  /**
   * Stop this meter funding anything further, and every wallet carved from it.
   * A revoked child stops between its own calls like any other exhaustion —
   * nothing in flight is interrupted.
   */
  revoke(): void {
    this.revokedFlag = true;
    for (const wallet of this.liveWallets) wallet.revoke();
  }

  /** Number of reservations taken so far. Used to stamp tranche history. */
  get calls(): number {
    return this.callIndex;
  }
}

/**
 * One provider call as the receipt records it.
 *
 * `estimatedUsd` is a public-catalog list-price subtotal for this call — what
 * the call would list at, never what any invoice said. `unpriced` marks a call
 * the catalog could not price at all, whose contribution to the total is an
 * honest zero rather than a measured amount.
 */
export interface ReceiptCall {
  readonly provider: string;
  readonly model: string;
  readonly inputTokens: number;
  readonly outputTokens: number;
  readonly cacheReadTokens: number;
  readonly cacheWriteTokens: number;
  readonly reasoningTokens: number;
  readonly estimatedUsd: number;
  readonly unpriced: boolean;
  /** How the tokens above were arrived at. `unavailable` means the provider's report was unreadable. */
  readonly usageBasis: "provider_reported" | "unavailable";
  /** The output allowance this call was given when the budget lowered it below the configured cap. */
  readonly clampedOutputTokens: number | undefined;
}

/**
 * One compaction event.
 *
 * `meteredCost` is REAL: what the summarization call actually cost, in the
 * run's denomination, zero for a free eviction. `modeledNetTokens` is MODELED
 * and labelled so — the context reduction this rewrite bought over the working
 * calls that actually followed it, net of the cache-write penalty the rewrite
 * forced. It is not a saving and is never called one.
 */
export interface ReceiptCompaction {
  readonly index: number;
  readonly tier: "evicted" | "summarized";
  readonly preTokens: number;
  readonly postTokens: number;
  readonly pinnedSegmentIds: readonly string[];
  readonly elidedSegmentDigests: readonly string[];
  readonly summarySchemaVersion: number | undefined;
  /**
   * The cache state the provider's own usage reported on the last working call,
   * recorded as evidence. The affordability model prices the summarizer COLD
   * whatever this says: the rewrite diverges from the working call's prefix at
   * its first changed message, so a warm read there is not evidence for a warm
   * read here.
   */
  readonly cacheState: "warm" | "cold" | "unknown";
  readonly meteredCost: number;
  readonly meteredBasis: "measured";
  readonly modeledNetTokens: number;
  readonly modeledBasis: "modeled";
  /** Working calls that actually followed this compaction, the multiplier in the modeled figure. */
  readonly workingCallsAfter: number;
}

/**
 * Tool activity for one tool name. Tools carry no model spend of their own —
 * a subagent tool's spend is its nested receipt under `subagents`.
 */
export interface ReceiptTool {
  readonly name: string;
  readonly calls: number;
  readonly errors: number;
}

/**
 * The economic receipt every run returns.
 *
 * Every money figure here is a public-catalog price estimate. It is not an
 * invoice, bill, or provider savings claim.
 */
export interface RunReceipt {
  readonly schema: typeof AGENT_RUN_RECEIPT_SCHEMA;
  readonly runId: string;
  readonly agentId: string;
  readonly basis: "estimated_list_price_subtotal";
  readonly claimBasis: "inferred";
  readonly stopReason: RunStopReason;
  /** `none` when the run declared no budget: the receipt still reports, nothing enforces. */
  readonly denomination: BudgetDenomination | "none";
  readonly max: number | undefined;
  readonly released: number | undefined;
  /** Metered spend in the run's own denomination, `undefined` without a budget. */
  readonly spent: number | undefined;
  /**
   * True when measured spend crossed `max`. The runtime never chooses to spend
   * past max — every call is reserved at its worst case first — so this means
   * a provider-side amount came in above what could be bounded. The receipt
   * never shows `spent > max` without this flag set.
   */
  readonly capBreached: boolean;
  /**
   * Signed amount **this run's own meter** went past its `max`, zero when its
   * own cap held. Deliberately not a tree total: settling a subagent's carve
   * books the child's real spend against this run, so a child's overspend
   * usually shows up here too and adding the two would count the same money
   * twice. Each subagent's own figure is on its own receipt under `subagents`.
   */
  readonly overspent: number;
  /** Estimated list-price subtotal of this run and everything under it. */
  readonly totalEstimatedUsd: number;
  readonly totalTokens: number;
  /** True when any call in this receipt or its subagents went unpriced. */
  readonly unpriced: boolean;
  readonly calls: readonly ReceiptCall[];
  readonly tools: readonly ReceiptTool[];
  readonly subagents: readonly RunReceipt[];
  readonly tranches: readonly BudgetTranche[];
  /** Every deterministic breaker decision this run made, so a break is never silent. */
  readonly breakers: readonly BreakerEvent[];
  /** Every context compaction, with its real cost and its modeled effect kept apart. */
  readonly compactions: readonly ReceiptCompaction[];
}

/**
 * Accumulates one run's receipt. The runtime feeds it from the same provider
 * evidence the run result is built from, so the receipt and the result can
 * never disagree about what happened.
 */
/** A compaction as it is recorded while the run is still going. */
export interface PendingCompaction {
  readonly tier: "evicted" | "summarized";
  readonly preTokens: number;
  readonly postTokens: number;
  readonly pinnedSegmentIds: readonly string[];
  readonly elidedSegmentDigests: readonly string[];
  readonly summarySchemaVersion: number | undefined;
  readonly cacheState: "warm" | "cold" | "unknown";
  readonly meteredCost: number;
}

export class ReceiptRecorder {
  private readonly callLog: ReceiptCall[] = [];
  private readonly toolLog = new Map<string, { calls: number; errors: number }>();
  private readonly subagentLog: RunReceipt[] = [];
  private readonly compactionLog: Array<PendingCompaction & { callsBefore: number }> = [];

  recordCall(call: ReceiptCall): void {
    this.callLog.push(Object.freeze({ ...call }));
  }

  /**
   * The call watermark is taken here, not passed in.
   *
   * A compaction is recorded after its own summarizer call has already been
   * logged, so anything the caller counted beforehand is one too low and every
   * compaction would claim a working call it never bought. Reading the log's
   * own length at record time makes "calls after this rewrite" mean exactly
   * that.
   */
  recordCompaction(compaction: PendingCompaction): void {
    this.compactionLog.push(Object.freeze({
      ...compaction,
      callsBefore: this.callLog.length,
    }));
  }

  recordToolCall(name: string): void {
    const entry = this.toolLog.get(name) ?? { calls: 0, errors: 0 };
    entry.calls++;
    this.toolLog.set(name, entry);
  }

  recordToolOutcome(name: string, isError: boolean): void {
    if (!isError) return;
    const entry = this.toolLog.get(name) ?? { calls: 0, errors: 0 };
    entry.errors++;
    this.toolLog.set(name, entry);
  }

  recordSubagent(receipt: RunReceipt): void {
    this.subagentLog.push(receipt);
  }

  build(input: {
    runId: string;
    agentId: string;
    stopReason: RunStopReason;
    meter: BudgetMeter | undefined;
    breakers?: readonly BreakerEvent[];
  }): RunReceipt {
    const ownUsd = this.callLog.reduce((total, call) => total + call.estimatedUsd, 0);
    const ownTokens = this.callLog.reduce(
      (total, call) => total + call.inputTokens + call.outputTokens +
        call.cacheReadTokens + call.cacheWriteTokens,
      0,
    );
    const nestedUsd = this.subagentLog.reduce(
      (total, child) => total + child.totalEstimatedUsd,
      0,
    );
    const nestedTokens = this.subagentLog.reduce(
      (total, child) => total + child.totalTokens,
      0,
    );
    const denomination = input.meter?.denomination ?? "none";
    return Object.freeze({
      schema: AGENT_RUN_RECEIPT_SCHEMA,
      runId: input.runId,
      agentId: input.agentId,
      basis: "estimated_list_price_subtotal",
      claimBasis: "inferred",
      stopReason: input.stopReason,
      denomination,
      max: input.meter?.max,
      released: input.meter?.released,
      spent: input.meter?.settled,
      // A breach anywhere under this run is a breach of this run. Wallets are
      // small carves off a larger parent, so a child outspending its wallet is
      // the ordinary shape of a breach — reading only the local meter would
      // report the cap held while money under it had already gone past.
      capBreached: (input.meter?.capBreached ?? false) ||
        this.subagentLog.some((child) => child.capBreached),
      // THIS level's own overspend, never a tree total. Settling a carve books
      // the child's real spend against the parent, so a child's overspend
      // usually pushes the parent past its own cap too — adding the two would
      // count the same dollars twice and can print a figure larger than the
      // whole tree spent. Each child's receipt carries its own.
      overspent: input.meter?.overspent ?? 0,
      totalEstimatedUsd: roundUsd(ownUsd + nestedUsd),
      totalTokens: ownTokens + nestedTokens,
      unpriced: this.callLog.some((call) => call.unpriced) ||
        this.subagentLog.some((child) => child.unpriced),
      calls: Object.freeze([...this.callLog]),
      tools: Object.freeze([...this.toolLog].map(([name, entry]) => Object.freeze({
        name,
        calls: entry.calls,
        errors: entry.errors,
      }))),
      subagents: Object.freeze([...this.subagentLog]),
      tranches: input.meter?.tranches ?? Object.freeze([]),
      breakers: input.breakers ?? Object.freeze([]),
      // The modeled effect of a compaction depends on how many working calls
      // actually followed it, which is knowable only now, at run end. Nothing
      // modeled is emitted before the run settles.
      compactions: Object.freeze(this.compactionLog.map((entry, index) => Object.freeze({
        index,
        tier: entry.tier,
        preTokens: entry.preTokens,
        postTokens: entry.postTokens,
        pinnedSegmentIds: Object.freeze([...entry.pinnedSegmentIds]),
        elidedSegmentDigests: Object.freeze([...entry.elidedSegmentDigests]),
        summarySchemaVersion: entry.summarySchemaVersion,
        cacheState: entry.cacheState,
        meteredCost: entry.meteredCost,
        meteredBasis: "measured" as const,
        modeledNetTokens: modeledNetTokens(
          entry,
          Math.max(0, this.callLog.length - entry.callsBefore),
        ),
        modeledBasis: "modeled" as const,
        workingCallsAfter: Math.max(0, this.callLog.length - entry.callsBefore),
      }))),
    });
  }
}

function roundUsd(value: number): number {
  return Math.round((value + Number.EPSILON) * 1e10) / 1e10;
}


/**
 * Local estimate: tokens the rewrite kept out of subsequent requests,
 * net of what the rewrite itself put back.
 *
 * The context shrank by `pre - post`, and each working call that followed it
 * carried the smaller context — that is the numerator. Against it stands the
 * rewrite's own cost: a compaction invalidates the cached prefix, so the next
 * request re-sends `post` tokens that would otherwise have been a cache read.
 * A figure that counted only the reduction would overstate the result, so the
 * penalty is subtracted here rather than footnoted.
 *
 * A negative result is kept signed. A compaction that did not pay for itself
 * says so.
 */
function modeledNetTokens(entry: PendingCompaction, workingCallsAfter: number): number {
  const reduction = (entry.preTokens - entry.postTokens) * workingCallsAfter;
  const cacheRewritePenalty = workingCallsAfter > 0 ? entry.postTokens : 0;
  return reduction - cacheRewritePenalty;
}

/**
 * What the escalation hook is told when a budget binds. Read-only:
 * the handler decides, the runtime applies. Every figure is in the run's own
 * denomination.
 */
export interface BudgetExhaustionContext {
  readonly denomination: BudgetDenomination;
  readonly max: number;
  readonly released: number;
  readonly spent: number;
  readonly remaining: number;
  /** How much could still be released without breaching `max`. */
  readonly releasable: number;
  /** Provider calls this run has already made. */
  readonly calls: number;
}

/**
 * Caller-side escalation for budget exhaustion.
 *
 * Called **between** calls, never mid-tool, and never with anything in flight.
 * Return `"stop"` to take the default outcome — partial work plus receipt — or
 * `{ release, reason }` to top up a tranche through the budget controller and carry
 * on. A release past `max` throws, because `max` is the contract.
 *
 * The embedding application owns this decision: ask a human, check a policy,
 * consult a quota. Nothing the model produces reaches it.
 *
 * Out of scope for v1: pausing a run and resuming it later from a serializable
 * handle. The hook is synchronous with the run it belongs to.
 */
export type BudgetExhaustionHandler = (
  context: BudgetExhaustionContext,
) => Promise<"stop" | { release: number; reason: string }>;

export function budgetExhaustionContext(meter: BudgetMeter): BudgetExhaustionContext {
  return Object.freeze({
    denomination: meter.denomination,
    max: meter.max,
    released: meter.released,
    spent: meter.settled,
    remaining: meter.remaining(),
    releasable: meter.releasable(),
    calls: meter.calls,
  });
}

const controllerMeters = new WeakMap<BudgetController, BudgetMeter>();

/**
 * The handle a developer uses to release staged budget.
 *
 * Tranches are released at **deterministic, developer-defined** checkpoints —
 * a plan produced, a build compiled, tests located. Nothing the model says can
 * reach this object: it exists only in the embedding application's own code and
 * in tool implementations the developer wrote, which keeps every model out of
 * the money path.
 *
 * A controller is bound to exactly one live run at a time and is inert outside
 * one, so a stale handle cannot quietly release budget into nothing.
 */
export class BudgetController {
  /**
   * Release a further tranche. Throws past `max`, at the release site: the
   * checkpoint asked for something the run's contract cannot grant, and that is
   * a programming error rather than a run outcome.
   */
  releaseBudget(amount: number, reason: string): BudgetTranche {
    return this.meter().release(amount, reason);
  }

  /** Metered spend so far, in the run's denomination. */
  get spent(): number {
    return this.meter().settled;
  }

  /** Budget available to the next call right now. */
  get remaining(): number {
    return this.meter().remaining();
  }

  /** Total released so far, across every tranche. */
  get released(): number {
    return this.meter().released;
  }

  get max(): number {
    return this.meter().max;
  }

  get denomination(): BudgetDenomination {
    return this.meter().denomination;
  }

  get tranches(): readonly BudgetTranche[] {
    return this.meter().tranches;
  }

  private meter(): BudgetMeter {
    const bound = controllerMeters.get(this);
    if (bound === undefined) throw new Error("cave_budget_controller_unbound");
    return bound;
  }
}

export function createBudgetController(): BudgetController {
  return new BudgetController();
}

/** Package-internal. Binds a controller to the run that owns its budget. */
export function bindBudgetController(
  controller: BudgetController,
  meter: BudgetMeter,
): void {
  if (controllerMeters.has(controller)) throw new Error("cave_budget_controller_in_use");
  controllerMeters.set(controller, meter);
}

/** Package-internal. Releases the binding when the run ends. */
export function unbindBudgetController(controller: BudgetController): void {
  controllerMeters.delete(controller);
}

/** What the meter says the runtime may do with the next provider call. */
export type CallPlan =
  | { readonly action: "proceed"; readonly reservation: BudgetReservation | undefined; readonly outputTokenCap: number }
  | { readonly action: "compact" }
  | { readonly action: "stop"; readonly reason: RunStopReason };

/**
 * The exhaustion ladder's arithmetic: full allowance, else a clamped allowance
 * down to the floor, else exhaustion. `compactionAvailable` lets the caller
 * insert the compaction rung between the full and clamped rungs.
 */
export function planCall(
  meter: BudgetMeter | undefined,
  call: CallCeiling,
  compactionAvailable: boolean,
): CallPlan {
  if (meter === undefined) {
    return { action: "proceed", reservation: undefined, outputTokenCap: call.outputTokenCap };
  }
  if (meter.revoked) return { action: "stop", reason: "wallet_revoked" };
  const full = callCeilingCost(meter.denomination, call, call.outputTokenCap);
  if (full === undefined) return { action: "stop", reason: "budget_exhausted" };
  const fullReservation = meter.reserve(full, call.outputTokenCap);
  if (fullReservation !== undefined) {
    return { action: "proceed", reservation: fullReservation, outputTokenCap: call.outputTokenCap };
  }
  if (compactionAvailable) return { action: "compact" };
  const clampedCap = affordableOutputTokens(meter, call);
  if (clampedCap === undefined) return { action: "stop", reason: "budget_exhausted" };
  const clampedCost = callCeilingCost(meter.denomination, call, clampedCap);
  if (clampedCost === undefined) return { action: "stop", reason: "budget_exhausted" };
  const clampedReservation = meter.reserve(clampedCost, clampedCap);
  if (clampedReservation === undefined) return { action: "stop", reason: "budget_exhausted" };
  return { action: "proceed", reservation: clampedReservation, outputTokenCap: clampedCap };
}

/**
 * Largest output allowance at or above the floor that the remaining budget can
 * still fund for this call, or `undefined` when even the floor does not fit.
 *
 * Cost is monotone in output tokens, so this is a binary search rather than a
 * second copy of the catalog's rate table.
 */
export function affordableOutputTokens(
  meter: BudgetMeter,
  call: CallCeiling,
): number | undefined {
  const remaining = meter.remaining();
  const floor = meter.outputFloorTokens;
  if (call.outputTokenCap < floor) return undefined;
  const floorCost = callCeilingCost(meter.denomination, call, floor);
  if (floorCost === undefined || floorCost > remaining) return undefined;
  let low = floor;
  let high = call.outputTokenCap;
  while (low < high) {
    const middle = low + Math.ceil((high - low) / 2);
    const cost = callCeilingCost(meter.denomination, call, middle);
    if (cost !== undefined && cost <= remaining) low = middle;
    else high = middle - 1;
  }
  return low;
}
