/**
 * Type-level tests for the TS SDK runtime-policy client (spec §21.2, issue #88).
 *
 * Compiled by `tsc --project tsconfig.test.json`; runtime assertions live in
 * tests/runtime-policy.runtime.mjs.
 */
import type {
  Cave,
  CaveTrace,
  DecideOptions,
  OTelExporter,
  PolicyDecision,
  PolicyDecisionReason,
  PolicySpanRecorder,
  PolicySpanSink,
  RuntimePolicyClient,
  RuntimePolicyDoc,
  RuntimePolicyOptions,
  RuntimePolicyRefresh,
  RuntimePolicyState
} from "../src/index.js";
import { policyUnitFraction } from "../src/index.js";

// ─── Type assertions ────────────────────────────────────────────────────────

declare const cave: Cave;

// runtimePolicy() takes no arguments, or the documented option bag.
const client: RuntimePolicyClient = cave.runtimePolicy();
const _pinned: RuntimePolicyClient = cave.runtimePolicy({ publicKey: "IVL4…", autoRefreshSeconds: 60, killEnv: "CAVEMAN_POLICY_KILL" });
declare const options: RuntimePolicyOptions;
const _fromOptions: RuntimePolicyClient = cave.runtimePolicy(options);

// refresh() is async and never surfaces an error as a rejection type.
const _refresh: Promise<RuntimePolicyRefresh> = client.refresh();
declare const refreshed: RuntimePolicyRefresh;
const _ok: boolean = refreshed.ok;
const _refreshSigned: boolean = refreshed.signed;
const _error: string | undefined = refreshed.error;

// decide() is SYNCHRONOUS — this assignment is the contract.
const decision: PolicyDecision = client.decide("fix_failing_test_with_stacktrace");
const _withOptions: PolicyDecision = client.decide("fix_failing_test_with_stacktrace", {
  unitKey: "task-a",
  context: { stack_trace_location_confidence: 0.95, language: "typescript" }
});
declare const decideOptions: DecideOptions;
const _fromDecideOptions: PolicyDecision = client.decide("f", decideOptions);

const _kind: "execute" | "fallback" | "baseline" = decision.decision;
const _reason: PolicyDecisionReason = decision.reason;
const _policyId: string | null = decision.policyId;
const _workflow: string | null = decision.workflow;
const _experimentId: string | undefined = decision.experimentId;
const _arm: string | undefined = decision.arm;
const _propensity: number | undefined = decision.propensity;
const _budget: Record<string, unknown> | undefined = decision.budget;
const _verify: string[] | undefined = decision.verify;
const _escalation: Array<Record<string, unknown>> | undefined = decision.escalation;
const _policyVersion: number | undefined = decision.policyVersion;
const _sequence: number | undefined = decision.sequence;
const _signed: boolean = decision.signed;

// kill() / stop() are local and synchronous; state() is a plain snapshot.
const _kill: void = client.kill();
const _close: void = client.close();
const state: RuntimePolicyState = client.state();
const _hasBundle: boolean = state.hasBundle;
const _stateKill: boolean = state.kill;
const _killedLocally: boolean = state.killedLocally;
const _stateVersion: number | undefined = state.policyVersion;
const _stateSequence: number | undefined = state.sequence;

// An OTel exporter or a CaveTrace is a valid decision-span sink (the trace
// machinery that already batches).
declare const exporter: OTelExporter;
declare const trace: CaveTrace;
const _recorder: PolicySpanRecorder = exporter;
const _sink: PolicySpanSink = exporter;
const _traceSink: PolicySpanSink = trace;
const _traced: PolicyDecision = client.decide("f", { unitKey: "u", trace: exporter });
const _tracedByTrace: PolicyDecision = client.decide("f", { unitKey: "u", trace });

// The wire-shaped policy document is snake_case, verbatim from the bundle.
declare const doc: RuntimePolicyDoc;
const _id: string = doc.id;
const _family: string = doc.task_family;
const _executeWorkflow: string = doc.execute.workflow;
const _guardOp: string | undefined = doc.applies_when?.[0]?.op;
const _armFraction: number | undefined = doc.experiment?.arms[0]?.fraction;
const _holdout: number | undefined = doc.experiment?.holdout_frac;

// The assignment hash is exported so a caller can reproduce an assignment.
const _fraction: number = policyUnitFraction("proj-a", "exp-1", "task-1");
