import { sha256, stableStringify } from "./context-ir.js";

/**
 * Deterministic circuit breakers.
 *
 * Every decision in this file is a hash comparison or an integer count. No
 * model runs anywhere in the breaker path: a model judge cannot participate in
 * stopping or accounting, so all breaker decisions stay deterministic.
 *
 * Local enforcement shares the worker detector's H6 repeat edge: same tool +
 * normalized arguments, excluding a repeat whose immediately-preceding
 * attempt failed. Worker-side finding arithmetic is intentionally broader:
 * session graph SCCs plus a population Isolation-Forest confirmation. Local
 * runtime has neither full-session population nor authority to mint findings.
 */
export interface RunBreakers {
  /**
   * How many identical tool calls — same tool, same normalized arguments — end
   * the run. Counted per hash, reset by an intervening failure. Defaults to 3.
   */
  readonly repeatedToolCalls?: number;
  /**
   * How many consecutive read-only turns with an identical outcome signature
   * (same model conclusion and tool identities/results) end the run. Successful
   * declared writes reset the window because equal display text is not proof
   * that host state stayed equal. Defaults to 3.
   */
  readonly noProgressTurns?: number;
  /** Most tool calls one assistant turn may fan out to. Extras are blocked. Defaults to 8. */
  readonly maxToolCallsPerTurn?: number;
  /**
   * Cost-aware retry for model calls that fail before producing any usage.
   * Retries are budgeted in the run's own denomination rather than counted, so
   * a provider-error storm exhausts its allowance instead of the wallet. A
   * retry policy requires a budget: there is no denomination to budget in
   * without one.
   */
  readonly retry?: {
    /** Worst-case spend the run will expose to retries, in the budget's denomination. */
    readonly maxSpend: number;
    /** First backoff, doubled each attempt. Deterministic — no jitter. Defaults to 250ms. */
    readonly backoffMs?: number;
  };
}

export interface NormalizedBreakers {
  readonly repeatedToolCalls: number;
  readonly noProgressTurns: number;
  readonly maxToolCallsPerTurn: number;
  readonly retryMaxSpend: number | undefined;
  readonly retryBackoffMs: number;
}

const DEFAULT_REPEATED_TOOL_CALLS = 3;
const DEFAULT_NO_PROGRESS_TURNS = 3;
const DEFAULT_MAX_TOOL_CALLS_PER_TURN = 8;
const DEFAULT_RETRY_BACKOFF_MS = 250;

export function normalizeRunBreakers(
  breakers: RunBreakers,
  hasBudget: boolean,
): NormalizedBreakers {
  const repeatedToolCalls = breakers.repeatedToolCalls ?? DEFAULT_REPEATED_TOOL_CALLS;
  const noProgressTurns = breakers.noProgressTurns ?? DEFAULT_NO_PROGRESS_TURNS;
  const maxToolCallsPerTurn = breakers.maxToolCallsPerTurn ?? DEFAULT_MAX_TOOL_CALLS_PER_TURN;
  for (const value of [repeatedToolCalls, noProgressTurns, maxToolCallsPerTurn]) {
    if (!Number.isSafeInteger(value) || value <= 0) {
      throw new Error("cave_breaker_threshold_invalid");
    }
  }
  if (breakers.retry !== undefined && !hasBudget) {
    throw new Error("cave_breaker_retry_requires_budget");
  }
  if (breakers.retry !== undefined &&
      (!Number.isFinite(breakers.retry.maxSpend) || breakers.retry.maxSpend <= 0)) {
    throw new Error("cave_breaker_retry_spend_invalid");
  }
  const retryBackoffMs = breakers.retry?.backoffMs ?? DEFAULT_RETRY_BACKOFF_MS;
  if (!Number.isSafeInteger(retryBackoffMs) || retryBackoffMs < 0) {
    throw new Error("cave_breaker_retry_backoff_invalid");
  }
  return Object.freeze({
    repeatedToolCalls,
    noProgressTurns,
    maxToolCallsPerTurn,
    retryMaxSpend: breakers.retry?.maxSpend,
    retryBackoffMs,
  });
}

/** One breaker decision, recorded on the receipt so a break is never silent. */
export interface BreakerEvent {
  readonly kind: "loop_detected" | "no_progress" | "fan_out_blocked" | "retry_attempted" | "retry_exhausted";
  readonly tool: string | undefined;
  /** Repeats for a loop, identical turns for no-progress, blocked calls for fan-out, attempt number for a retry. */
  readonly count: number;
  /** The call hash for a loop break, so the offending window is identifiable. */
  readonly signature: string | undefined;
}

type ToolRepeat = { count: number; previousFailed: boolean };

/**
 * The breaker bookkeeping for one run. Every method is pure counting over
 * hashes the runtime already has in hand.
 */
export class BreakerState {
  readonly config: NormalizedBreakers;
  private readonly repeats = new Map<string, ToolRepeat>();
  private readonly hashByToolCallId = new Map<string, string>();
  private readonly events: BreakerEvent[] = [];
  private lastTurnSignature: string | undefined;
  private repeatedTurns = 0;
  private currentTurnKey: unknown;
  private currentTurnFanOut = 0;
  private retrySpent = 0;
  private trip: "loop_detected" | "no_progress" | undefined;

  constructor(config: NormalizedBreakers) {
    this.config = config;
  }

  /** The break this run has already decided on, if any. */
  get tripped(): "loop_detected" | "no_progress" | undefined {
    return this.trip;
  }

  get recorded(): readonly BreakerEvent[] {
    return Object.freeze([...this.events]);
  }

  /**
   * Count one tool call. Returns true when it must be blocked — because it is
   * the repeat that trips the loop breaker, or because its turn has already
   * fanned out as far as it may.
   */
  observeToolCall(input: {
    toolCallId: string;
    toolName: string;
    args: unknown;
    allowRepeat: boolean;
    turnKey: unknown;
  }): { block: boolean; reason: string | undefined } {
    if (input.turnKey !== this.currentTurnKey) {
      this.currentTurnKey = input.turnKey;
      this.currentTurnFanOut = 0;
    }
    this.currentTurnFanOut++;
    if (this.currentTurnFanOut > this.config.maxToolCallsPerTurn) {
      this.events.push(Object.freeze({
        kind: "fan_out_blocked",
        tool: input.toolName,
        count: this.currentTurnFanOut,
        signature: undefined,
      }));
      return { block: true, reason: "cave_fan_out_cap_exceeded" };
    }
    // A tool whose whole job is to be called again — polling a queue, waiting
    // on a build — is legitimately repetitive and opts out of the loop count.
    if (input.allowRepeat) return { block: false, reason: undefined };
    const signature = callSignature(input.toolName, input.args);
    this.hashByToolCallId.set(input.toolCallId, signature);
    const entry = this.repeats.get(signature) ?? { count: 0, previousFailed: false };
    if (entry.previousFailed) {
      // The previous identical attempt failed, so this one is a necessary
      // retry, not a re-traversal. Start the window again rather than counting
      // work the run had to pay for anyway.
      entry.count = 1;
      entry.previousFailed = false;
    } else {
      entry.count++;
    }
    this.repeats.set(signature, entry);
    if (entry.count >= this.config.repeatedToolCalls) {
      this.trip ??= "loop_detected";
      this.events.push(Object.freeze({
        kind: "loop_detected",
        tool: input.toolName,
        count: entry.count,
        signature,
      }));
      return { block: true, reason: "cave_tool_call_loop_detected" };
    }
    return { block: false, reason: undefined };
  }

  observeToolResult(toolCallId: string, isError: boolean): void {
    const signature = this.hashByToolCallId.get(toolCallId);
    if (signature === undefined) return;
    this.hashByToolCallId.delete(toolCallId);
    const entry = this.repeats.get(signature);
    if (entry !== undefined) entry.previousFailed = isError;
  }

  /**
   * Fold one completed turn into the no-progress window. Tool identity is part
   * of the signature; equal output from different operations is not equality.
   * A successful declared write resets the window. Generic runtime cannot
   * inspect arbitrary host state, so treating write text as proof of no change
   * would be an unsafe false stop.
   */
  observeTurn(
    conclusion: string,
    toolResults: readonly unknown[],
    stateChanged = false,
  ): void {
    if (stateChanged) {
      this.lastTurnSignature = undefined;
      this.repeatedTurns = 0;
      return;
    }
    const signature = sha256(stableStringify({ conclusion, toolResults }));
    if (signature === this.lastTurnSignature) this.repeatedTurns++;
    else this.repeatedTurns = 1;
    this.lastTurnSignature = signature;
    if (this.repeatedTurns >= this.config.noProgressTurns) {
      this.trip ??= "no_progress";
      this.events.push(Object.freeze({
        kind: "no_progress",
        tool: undefined,
        count: this.repeatedTurns,
        signature,
      }));
    }
  }

  /**
   * Take `amount` of the retry allowance for one more attempt, in the run's
   * own denomination. False means the allowance cannot cover another attempt's
   * worst case, so the failure stands instead of the wallet draining.
   */
  allowRetry(amount: number, attempt: number): boolean {
    const allowance = this.config.retryMaxSpend;
    if (allowance === undefined) return false;
    if (!Number.isFinite(amount) || amount < 0) return false;
    if (this.retrySpent + amount > allowance) {
      this.events.push(Object.freeze({
        kind: "retry_exhausted",
        tool: undefined,
        count: attempt,
        signature: undefined,
      }));
      return false;
    }
    this.retrySpent += amount;
    this.events.push(Object.freeze({
      kind: "retry_attempted",
      tool: undefined,
      count: attempt,
      signature: undefined,
    }));
    return true;
  }

  /** Deterministic exponential backoff. No jitter: a breaker must be reproducible. */
  backoffMs(attempt: number): number {
    return this.config.retryBackoffMs * 2 ** Math.max(0, attempt - 1);
  }
}

export function callSignature(toolName: string, args: unknown): string {
  return sha256(stableStringify({ tool: toolName, args: args ?? null }));
}
