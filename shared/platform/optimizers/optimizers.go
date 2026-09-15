// Package optimizers is the single source of truth for the mutex families that
// group overlapping-headroom optimizer IDs.
//
// Two independent services dedup on these families:
//   - cloud/worker's detectors collapse overlapping opportunities at WRITE time
//     (detectors.go dedupeMutexFamilies), and
//   - cloud/control-api's Cave Architect collapses them again at READ time
//     (caveplan.go dedupeFamilies).
//
// Both MUST agree on which optimizer IDs share a family AND on the family
// ORDERING (the integer index is part of each dedup key). If they disagreed, the
// same headroom would be double-counted or silently dropped — a no-fake-savings
// violation. Keeping the membership here, imported by both, makes that drift
// impossible.
//
// This package is pure (no dependencies) and lives under public/ so both the
// public and private modules may import it without crossing the publish boundary
// (tools/check-boundaries.sh).
package optimizers

// HeadroomCapVersion marks opportunities whose combined cross-family high band
// was bounded by matching provider-complete, catalog-priced daily spend. Readers
// hide legacy open rows without this marker so pre-cap estimates cannot reappear.
const HeadroomCapVersion = "scope-spend.v1"

// GeminiExplicitCacheOptimizerID is retained only as a historical runtime and
// telemetry join key. The old scaffold never created a cachedContents resource,
// never transformed a request, and has no current policy, practice, experiment,
// opportunity, or verified-savings path.
const GeminiExplicitCacheOptimizerID = "gemini-explicit-cache"

// EnablementZeroBandMethod is the exact immutable zero band used by
// measurement-enablement observations. These findings can be reviewed and
// dismissed, but they are not optimization moves and have no proposal,
// experiment, implementation, score, or savings lifecycle.
const EnablementZeroBandMethod = "enablement_zero.v1"

const (
	ReportOnlyZeroBandMethod                 = "report_only_zero.v1"
	SubagentRunReportOnlyZeroBandMethod      = "subagent_run_report_only_zero.v1"
	ToolErrorReportOnlyZeroBandMethod        = "tool_error_streak_report_only_zero.v1"
	DuplicateStepReportOnlyBandMethod        = "duplicate_step_profile_report_only_zero.v1"
	ProviderErrorReportOnlyBandMethod        = "provider_error_traffic_report_only_zero.v1"
	ContextWindowProfileReportOnlyBandMethod = "context_window_profile_report_only_zero.v1"
	ToolCatalogProfileReportOnlyBandMethod   = "tool_catalog_profile_report_only_zero.v1"
	ToolOutputProfileReportOnlyBandMethod    = "tool_output_size_profile_report_only_zero.v1"
	ExplorationProfileReportOnlyBandMethod   = "exploration_load_profile_report_only_zero.v1"
	QualityFeedbackReportOnlyBandMethod      = "quality_feedback_report_only_zero.v1"
	RecoveryToolResultReportOnlyBandMethod   = "recovery_tool_result_report_only_zero.v1"
	HTTPResendReportOnlyBandMethod           = "http_resend_profile_report_only_zero.v1"
	OTelAgentTopologyReportOnlyBandMethod    = "otel_agent_topology_profile_report_only_zero.v1"
)

// ReportOnlyOpportunityContract is the immutable cross-service identity for a
// report projection. OpportunityID is the persisted/public join key;
// EvidenceDetectorID is the validated bundle identity and may differ when a
// stable opportunity id outlives a detector rename. BandMethod is the exact
// zero-band method the report SQL and renderer admit.
type ReportOnlyOpportunityContract struct {
	OpportunityID      string
	EvidenceDetectorID string
	BandMethod         string
	ProjectOnly        bool
}

// reportOnlyOpportunityContracts is sorted by OpportunityID. It is the one
// vocabulary used by the worker cap exemption, report SQL, report DTO,
// Cave Plan exclusion, and proposal/lifecycle fences.
var reportOnlyOpportunityContracts = [...]ReportOnlyOpportunityContract{
	{OpportunityID: "cache-miss-root-cause", EvidenceDetectorID: "cache-miss-root-cause", BandMethod: ReportOnlyZeroBandMethod},
	{OpportunityID: "compaction-roundtrip", EvidenceDetectorID: "compaction-roundtrip", BandMethod: ReportOnlyZeroBandMethod},
	{OpportunityID: "context-window-profile", EvidenceDetectorID: "context-window-profile", BandMethod: ContextWindowProfileReportOnlyBandMethod},
	{OpportunityID: "duplicate-step-profile", EvidenceDetectorID: "duplicate-step-profile", BandMethod: DuplicateStepReportOnlyBandMethod},
	{OpportunityID: "exploration-load-profile", EvidenceDetectorID: "exploration-load-profile", BandMethod: ExplorationProfileReportOnlyBandMethod},
	{OpportunityID: "http-resend-profile", EvidenceDetectorID: "http-resend-profile", BandMethod: HTTPResendReportOnlyBandMethod},
	{OpportunityID: "otel-agent-topology-profile", EvidenceDetectorID: "otel-agent-topology-profile", BandMethod: OTelAgentTopologyReportOnlyBandMethod, ProjectOnly: true},
	{OpportunityID: "prompt-prefix-stability", EvidenceDetectorID: "repeated-uncached-prefix", BandMethod: ReportOnlyZeroBandMethod},
	{OpportunityID: "provider-error-traffic-profile", EvidenceDetectorID: "provider-error-traffic-profile", BandMethod: ProviderErrorReportOnlyBandMethod},
	{OpportunityID: "quality-feedback-profile", EvidenceDetectorID: "quality-feedback-profile", BandMethod: QualityFeedbackReportOnlyBandMethod},
	{OpportunityID: "recovery-tool-result-profile", EvidenceDetectorID: "recovery-tool-result-profile", BandMethod: RecoveryToolResultReportOnlyBandMethod},
	{OpportunityID: "subagent-fanout-duplication", EvidenceDetectorID: "subagent-fanout-duplication", BandMethod: ReportOnlyZeroBandMethod},
	{OpportunityID: "subagent-run-concentration", EvidenceDetectorID: "subagent-run-concentration", BandMethod: SubagentRunReportOnlyZeroBandMethod},
	{OpportunityID: "tool-catalog-profile", EvidenceDetectorID: "tool-catalog-profile", BandMethod: ToolCatalogProfileReportOnlyBandMethod},
	{OpportunityID: "tool-error-streak-profile", EvidenceDetectorID: "tool-error-streak-profile", BandMethod: ToolErrorReportOnlyZeroBandMethod},
	{OpportunityID: "tool-output-size-profile", EvidenceDetectorID: "tool-output-size-profile", BandMethod: ToolOutputProfileReportOnlyBandMethod},
}

// IsReportProjectionOnlyOpportunity identifies observations admitted by the
// dedicated report API but excluded from Cave Plan, scoring, proposals, and
// every lifecycle mutation except Dismiss.
func IsReportProjectionOnlyOpportunity(optimizerID string) bool {
	_, ok := ReportOnlyOpportunityContractFor(optimizerID)
	return ok
}

// ReportOnlyOpportunityContractFor resolves the exact report projection
// contract. Unknown ids fail closed.
func ReportOnlyOpportunityContractFor(optimizerID string) (ReportOnlyOpportunityContract, bool) {
	for _, contract := range reportOnlyOpportunityContracts {
		if contract.OpportunityID == optimizerID {
			return contract, true
		}
	}
	return ReportOnlyOpportunityContract{}, false
}

// ReportOnlyOpportunityContracts returns a copy in stable OpportunityID order.
// Callers cannot mutate the package's closed vocabulary.
func ReportOnlyOpportunityContracts() []ReportOnlyOpportunityContract {
	contracts := make([]ReportOnlyOpportunityContract, len(reportOnlyOpportunityContracts))
	copy(contracts, reportOnlyOpportunityContracts[:])
	return contracts
}

// ReportOnlyBandMethodForOpportunity resolves the exact immutable zero-band
// contract for a report-only opportunity. Unknown ids fail closed.
func ReportOnlyBandMethodForOpportunity(optimizerID string) (string, bool) {
	contract, ok := ReportOnlyOpportunityContractFor(optimizerID)
	return contract.BandMethod, ok
}

// ReportOnlyOpportunityIDsForBand returns the closed report-only ids admitted
// by the report API for one exact evidence band. Unknown bands return nil.
func ReportOnlyOpportunityIDsForBand(bandMethod string) []string {
	ids := []string{}
	for _, contract := range reportOnlyOpportunityContracts {
		if contract.BandMethod == bandMethod {
			ids = append(ids, contract.OpportunityID)
		}
	}
	return ids
}

// EnablementOnlyOpportunityContract is the immutable cross-service identity
// for an exact-zero measurement-enablement observation that remains visible in
// Cave Plan for operator review. It is distinct from report projections only
// in where it renders; both categories are non-actuatable.
type EnablementOnlyOpportunityContract struct {
	OpportunityID      string
	EvidenceDetectorID string
	BandMethod         string
}

// enablementOnlyOpportunityContracts is sorted by OpportunityID. Keeping the
// closed vocabulary here prevents the control plane, worker, and Cave Plan
// from independently deciding that a zero-dollar observation is actionable.
var enablementOnlyOpportunityContracts = [...]EnablementOnlyOpportunityContract{
	{OpportunityID: "count-baseline-permission", EvidenceDetectorID: "count-baseline-permission", BandMethod: EnablementZeroBandMethod},
	{OpportunityID: "unlabeled-traffic", EvidenceDetectorID: "unlabeled-traffic", BandMethod: EnablementZeroBandMethod},
}

// IsEnablementOnlyOpportunity reports whether an optimizer is a visible,
// exact-zero enablement observation with a Dismiss-only lifecycle.
func IsEnablementOnlyOpportunity(optimizerID string) bool {
	_, ok := EnablementOnlyOpportunityContractFor(optimizerID)
	return ok
}

// EnablementOnlyOpportunityContractFor resolves an exact enablement contract.
// Unknown ids fail closed.
func EnablementOnlyOpportunityContractFor(optimizerID string) (EnablementOnlyOpportunityContract, bool) {
	for _, contract := range enablementOnlyOpportunityContracts {
		if contract.OpportunityID == optimizerID {
			return contract, true
		}
	}
	return EnablementOnlyOpportunityContract{}, false
}

// EnablementOnlyOpportunityContracts returns a stable copy. Callers cannot
// mutate the package's closed vocabulary.
func EnablementOnlyOpportunityContracts() []EnablementOnlyOpportunityContract {
	contracts := make([]EnablementOnlyOpportunityContract, len(enablementOnlyOpportunityContracts))
	copy(contracts, enablementOnlyOpportunityContracts[:])
	return contracts
}

// IsReviewOnlyOpportunity is the common proposal/actuation boundary for all
// current non-actuatable observations. Continuous-improvement report ids keep
// their separate namespace fence in the control plane.
func IsReviewOnlyOpportunity(optimizerID string) bool {
	return IsReportProjectionOnlyOpportunity(optimizerID) || IsEnablementOnlyOpportunity(optimizerID)
}

// retiredOpportunityIDs are immutable historical optimizer join keys whose
// current telemetry does not support a Cave Plan card, score penalty, practice,
// or proposal. Their family memberships below remain unchanged so historical
// dedup identity never shifts.
var retiredOpportunityIDs = [...]string{
	"cache-write-read-churn",
	"retry-loop-reduction",
	"model-right-sizing",
	"model-distillation-candidate",
	"fusion-routing",
	"wasted-reasoning-suppression",
	"cron-unchanged-input",
	"loop-scc",
	"duplicate-step",
	"error-spend-reduction",
	"context-window-bloat",
	"tool-catalog-utilization",
	"verbose-tool-output",
	"context-exploration-offload",
	"pixel-density",
	GeminiExplicitCacheOptimizerID,
}

var retiredOpportunitySet = func() map[string]struct{} {
	set := make(map[string]struct{}, len(retiredOpportunityIDs))
	for _, id := range retiredOpportunityIDs {
		set[id] = struct{}{}
	}
	return set
}()

// IsRetiredOpportunity reports whether an optimizer id is a historical
// opportunity identity that must be absent from current read/proposal surfaces.
func IsRetiredOpportunity(optimizerID string) bool {
	_, ok := retiredOpportunitySet[optimizerID]
	return ok
}

// RetiredOpportunityIDs returns a fresh list suitable for a scoped SQL ANY
// predicate. Callers cannot mutate the package's closed retirement vocabulary.
func RetiredOpportunityIDs() []string {
	ids := make([]string, len(retiredOpportunityIDs))
	copy(ids, retiredOpportunityIDs[:])
	return ids
}

// Family is one mutex group. For each family, at most one member survives per
// (agent, workflow, model, day) scope, so overlapping detectors never
// double-count the same dollars.
type Family struct {
	// Name is the canonical family label surfaced in the Cave Plan UI.
	Name string
	// Members are the optimizer IDs that belong to this family.
	Members []string
}

// Families is the ordered list of mutex families. The ORDER is load-bearing:
// FamilyIndexOf returns a family's position here, and that index is used as part
// of the dedup key on both the worker and control-api sides. Appending a new
// family is safe; reordering or reindexing existing families is NOT. Changing
// MEMBERSHIP re-classifies headroom — do that deliberately, never as a drive-by.
var Families = []Family{
	{Name: "input_bloat", Members: []string{
		// DELIBERATE MEMBERSHIP CHANGE (AUTOPILOT_SPEC §12.7 / §3.5, phase 4,
		// 2026-08-02 — the input-bloat collapse). These memberships are retained
		// as immutable historical mutex identity. The current report-only profile
		// ids deliberately do not enter this money-bearing family.
		// This is exactly the re-classification the append-only warning above
		// exists to make deliberate, done as its own reviewed change, never a
		// drive-by. The family INDEX is untouched (membership only), so no
		// dedup key shifts. Note the name collision that survives on purpose:
		// the GATEWAY runtime optimizer id "tool-schema-deferral"
		// (tool_memory.go / policy defaults) is a different namespace and is
		// not affected by retiring the opportunity id.
		"context-window-bloat",
		"toon-reencoding",
		"context-exploration-offload",
		// Retired F3 identity; it no longer claims unused-catalog dollars.
		"tool-catalog-utilization",
		// Retired W3 identity; byte-length estimates no longer mint headroom.
		"verbose-tool-output",
	}},
	{Name: "cache", Members: []string{
		"prompt-prefix-stability",
		// Historical identity only. The typed telemetry collapses 5-minute and
		// 1-hour cache writes, and the old aggregate re-priced both with the
		// current global rate without proving a caller-controlled marker. Keep
		// the mutex member so stored history never changes identity, but never
		// admit it to current read or proposal surfaces.
		"cache-write-read-churn",
		// F1 cache-miss root-cause (AUTOPILOT_SPEC §3.2, slice A2). Deliberate
		// membership append: F1 headroom draws from the same prompt-cache
		// dollars as the two ids above, so it must dedup against them rather
		// than double-count. Appending to an existing family's members does not
		// change any family index.
		"cache-miss-root-cause",
	}},
	{Name: "reliability", Members: []string{
		// Historical identity only. Every member in this family is retired from
		// current opportunity output; keep the immutable membership so old rows
		// never change mutex identity.
		"retry-loop-reduction",
		"error-spend-reduction",
		"duplicate-step",
		"cron-unchanged-input",
		"loop-scc",
	}},
	{Name: "routing", Members: []string{
		// Historical identity only; current telemetry does not prove same-task
		// quality parity or safe routing recovery for any member.
		"model-right-sizing",
		"wasted-reasoning-suppression",
		"fusion-routing",
		"model-distillation-candidate",
	}},
	// Historical pixel-density identity only. No per-request producer/schema
	// exists, so ordinary traffic cannot synthesize a current profile.
	{Name: "pixel_density", Members: []string{
		"pixel-density",
	}},
}

// membership is the reverse index (optimizer ID -> Families slice index), built
// once at package initialization.
var membership = func() map[string]int {
	m := make(map[string]int)
	for i, f := range Families {
		for _, id := range f.Members {
			m[id] = i
		}
	}
	return m
}()

// FamilyIndexOf returns the index (into Families) of the mutex family the
// optimizer belongs to, or -1 if the optimizer is not part of any family (its
// opportunities are never deduped). Both cloud/worker and cloud/control-api key
// their dedup on this value, so they can never disagree.
func FamilyIndexOf(optimizerID string) int {
	if i, ok := membership[optimizerID]; ok {
		return i
	}
	return -1
}

// FamilyNameOf returns the canonical family label for an optimizer, or "" if the
// optimizer is not a mutex-family member.
func FamilyNameOf(optimizerID string) string {
	if i, ok := membership[optimizerID]; ok {
		return Families[i].Name
	}
	return ""
}

// MembersSet returns a fresh set of the optimizer IDs in the named family, or an
// empty set if the name is unknown. Callers that want a set-membership lookup
// (id -> bool) build their view from this instead of re-listing the IDs.
func MembersSet(name string) map[string]bool {
	set := map[string]bool{}
	for _, f := range Families {
		if f.Name == name {
			for _, id := range f.Members {
				set[id] = true
			}
			return set
		}
	}
	return set
}
