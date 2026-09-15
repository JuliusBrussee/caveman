package store

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"sort"
	"strings"
)

// learn_attribution.go answers the question a savings number has to survive:
// "how do you know CAVEMAN caused this?"
//
// Two separate problems, solved separately:
//
//  1. PROVENANCE — is the edit we proposed still the edit that is there? At
//     apply time we fingerprint the artifact the fix targeted. Later scans
//     re-hash it. Intact means the reduction we measure sits on top of our own
//     change; changed-since means someone edited past it and the attribution is
//     downgraded rather than quietly kept.
//
//  2. METHOD — how was the after-value obtained? The rungs below are ordered by
//     how much of a counterfactual each one actually establishes. Every
//     confirmed row names its rung and carries the confounders that rung cannot
//     rule out, so a weak measurement can never be read as a strong one.
//
// No rung produces `verified`. `verified` remains gateway-only and requires
// provider-causal evidence this side cannot obtain.
const (
	// attrDeterministic re-counts the artifact we changed. The before and after
	// are arithmetic on bytes on disk, so the delta is not in dispute — only
	// whether the user would have made the change anyway.
	attrDeterministic = "deterministic_remeasure"
	// attrReplay re-runs real scanned history with the change applied
	// (learn_simulate.go). A true counterfactual over sessions that happened,
	// bounded to those sessions.
	attrReplay = "counterfactual_replay"
	// attrHoldout compares sessions with the change on against sessions with it
	// off, on this machine. The only rung that controls for the user.
	attrHoldout = "controlled_holdout"
	// attrTimeSeries compares before-sessions to after-sessions. It cannot
	// separate the fix from anything else that changed that week.
	attrTimeSeries = "interrupted_time_series"
	// attrNone is an honest refusal: something was recorded, nothing can be
	// attributed yet.
	attrNone = "unattributed"

	// Provenance verdicts for the fingerprinted artifact.
	provenanceIntact       = "intact"
	provenanceChanged      = "changed_since"
	provenanceMissing      = "target_missing"
	provenanceUnfingerprnt = "not_fingerprinted"
)

// attributionRung orders the methods. Higher is stronger. Used to pick the best
// available method for a row and to sort the savings ledger.
func attributionRung(method string) int {
	switch method {
	case attrDeterministic:
		return 4
	case attrHoldout:
		return 3
	case attrReplay:
		return 2
	case attrTimeSeries:
		return 1
	default:
		return 0
	}
}

func attributionConfidence(method, provenance string) string {
	if provenance == provenanceChanged || provenance == provenanceMissing {
		return "low"
	}
	switch method {
	case attrDeterministic, attrHoldout:
		return "high"
	case attrReplay:
		return "medium"
	case attrTimeSeries:
		return "low"
	default:
		return "none"
	}
}

// attributionConfounders names what each rung cannot rule out. These are
// STANDING confounders — they are attached whether or not the number looks
// good, because a caveat that only appears on bad news is marketing.
func attributionConfounders(method, provenance string) []string {
	var out []string
	switch method {
	case attrDeterministic:
		out = append(out,
			"The token delta is arithmetic on the edited file; whether you would have trimmed it anyway is not measured.")
	case attrHoldout:
		out = append(out,
			"Arms are your own consecutive sessions, not randomized tasks; task difficulty may differ between arms.")
	case attrReplay:
		out = append(out,
			"Replay applies the change to sessions that already happened; an agent given the changed context might have acted differently.")
	case attrTimeSeries:
		out = append(out,
			"Before/after sessions differ in more than this fix: task mix, model version, and repository state all moved too.",
			"No control arm exists, so a coincident change elsewhere would look identical to this fix working.")
	}
	switch provenance {
	case provenanceChanged:
		out = append(out, "The targeted file changed after the fix was recorded, so part of any delta belongs to that later edit.")
	case provenanceMissing:
		out = append(out, "The targeted file is gone, so the delta cannot be tied to the recorded fix at all.")
	case provenanceUnfingerprnt:
		out = append(out, "This fix predates artifact fingerprinting, so the edit's continued presence is unverified.")
	}
	return out
}

// fixTarget is the artifact a fix edited, fingerprinted at apply time so a later
// scan can prove the edit is still in place.
type fixTarget struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Bytes  int64  `json:"bytes"`
	Tokens int    `json:"tokens"`
}

// fingerprintFixTarget reads the artifact a sink names and hashes it as it
// stands right now — which, at `caveman learn applied` time, is immediately
// after the skill applied the approved edit. Failure is not an error: a fix
// with no fingerprintable target still records, it just cannot claim
// provenance later.
func fingerprintFixTarget(sink Sink) *fixTarget {
	path := fixTargetPath(sink)
	if path == "" {
		return nil
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	tokens, _ := configTokenCount(string(raw))
	sum := sha256.Sum256(raw)
	return &fixTarget{
		Path: path, SHA256: hex.EncodeToString(sum[:]),
		Bytes: int64(len(raw)), Tokens: tokens,
	}
}

// fixTargetPath resolves which file a sink's fix edits. Both file-editing sink
// families already carry the path in their evidence (claude_md_weight from the
// config snapshot, claude_md_sections from addSectionConfigPaths). Behavioral
// sinks return "" because there is no artifact to fingerprint.
func fixTargetPath(sink Sink) string {
	path, _ := sink.Evidence["path"].(string)
	return strings.TrimSpace(path)
}

// checkFixProvenance re-hashes a recorded target. It reports whether the bytes
// we fingerprinted are still the bytes on disk, plus the file's current token
// count so a deterministic re-measure can be taken from it.
func checkFixProvenance(target *fixTarget) (verdict string, currentTokens int, ok bool) {
	if target == nil || target.Path == "" || target.SHA256 == "" {
		return provenanceUnfingerprnt, 0, false
	}
	info, err := os.Lstat(target.Path)
	if err != nil || !info.Mode().IsRegular() {
		return provenanceMissing, 0, false
	}
	raw, err := os.ReadFile(target.Path)
	if err != nil {
		return provenanceMissing, 0, false
	}
	tokens, _ := configTokenCount(string(raw))
	sum := sha256.Sum256(raw)
	if hex.EncodeToString(sum[:]) == target.SHA256 {
		return provenanceIntact, tokens, true
	}
	return provenanceChanged, tokens, true
}

// LearnAttribution is the evidence block attached to every confirmed saving.
type LearnAttribution struct {
	Method      string   `json:"method"`
	Rung        int      `json:"rung"`
	Confidence  string   `json:"confidence"`
	Provenance  string   `json:"provenance"`
	TargetPath  string   `json:"target_path,omitempty"`
	Confounders []string `json:"confounders,omitempty"`
}

func buildAttribution(method, provenance, targetPath string) LearnAttribution {
	return LearnAttribution{
		Method: method, Rung: attributionRung(method),
		Confidence: attributionConfidence(method, provenance),
		Provenance: provenance, TargetPath: targetPath,
		Confounders: attributionConfounders(method, provenance),
	}
}

// LearnSavingsRow is one attributed saving in the savings ledger.
type LearnSavingsRow struct {
	SinkID     string           `json:"sink_id"`
	PracticeID string           `json:"practice_id,omitempty"`
	FixKind    string           `json:"fix_kind"`
	AppliedAt  string           `json:"applied_at"`
	Verdict    string           `json:"verdict"`
	Unit       string           `json:"unit"`
	Before     float64          `json:"before"`
	After      *float64         `json:"after,omitempty"`
	SavedPer   string           `json:"saved_per,omitempty"` // "turn" for rate units
	SavedValue *float64         `json:"saved_value,omitempty"`
	SavedUSD   *float64         `json:"saved_usd,omitempty"`
	Attributed LearnAttribution `json:"attribution"`
}

// LearnSavings is the ledger `caveman learn savings` renders: every fix the
// user applied on caveman's advice, what it returned, and how strongly that
// return can be attributed. Rows are grouped by rung so a deterministic
// re-measure never sits visually beside a time-series guess.
type LearnSavings struct {
	Schema   string            `json:"schema"`
	Basis    string            `json:"basis"`
	Currency string            `json:"currency,omitempty"`
	Window   LearnWindow       `json:"window"`
	Rows     []LearnSavingsRow `json:"rows"`
	// TotalSavedUSDByRung sums priced savings per attribution method. It is
	// deliberately NOT a single headline: summing a deterministic re-measure
	// with a time-series guess would launder the weak number into the strong
	// one.
	TotalSavedUSDByRung map[string]float64 `json:"total_saved_usd_by_rung,omitempty"`
	Caveats             []string           `json:"caveats"`
}

const learnSavingsSchema = "caveman.learn.savings.v1"

// buildLearnSavings turns confirmed rows into the attributed ledger. Rows whose
// verdict is not a measured improvement still appear — a regression that
// disappears from the ledger is the same lie as a savings number without a
// denominator.
func buildLearnSavings(plan LearnPlan) LearnSavings {
	out := LearnSavings{
		Schema: learnSavingsSchema, Basis: learnBasis, Window: plan.Window,
		Rows: []LearnSavingsRow{},
	}
	if plan.Spend != nil {
		out.Currency = plan.Spend.Currency
	}
	byRung := map[string]float64{}
	for _, confirmed := range plan.Confirmed {
		row := LearnSavingsRow{
			SinkID: confirmed.SinkID, PracticeID: confirmed.PracticeID,
			FixKind: confirmed.FixKind, AppliedAt: confirmed.AppliedAt,
			Verdict: confirmed.Verdict, Unit: confirmed.Unit,
			Before: confirmed.Before, After: confirmed.After,
			Attributed: confirmed.Attribution,
		}
		if confirmed.After != nil {
			saved := confirmed.Before - *confirmed.After
			row.SavedValue = &saved
			if strings.Contains(confirmed.Unit, "per_turn") {
				row.SavedPer = "turn"
			}
			// Only a POSITIVE saving is priced. A regression's dollar figure
			// would read as money returned; it is reported as tokens with its
			// regressed verdict instead.
			if saved > 0 && plan.Spend != nil {
				perDay := saved
				if row.SavedPer == "turn" && plan.observedTurns > 0 && windowDaysOf(plan) > 0 {
					perDay = saved * float64(plan.observedTurns) / windowDaysOf(plan)
				}
				usd := plan.Spend.priceInputTokens(int64(perDay))
				if usd > 0 {
					row.SavedUSD = &usd
					byRung[confirmed.Attribution.Method] += usd
				}
			}
		}
		out.Rows = append(out.Rows, row)
	}
	sort.SliceStable(out.Rows, func(i, j int) bool {
		if out.Rows[i].Attributed.Rung != out.Rows[j].Attributed.Rung {
			return out.Rows[i].Attributed.Rung > out.Rows[j].Attributed.Rung
		}
		return out.Rows[i].AppliedAt > out.Rows[j].AppliedAt
	})
	if len(byRung) > 0 {
		out.TotalSavedUSDByRung = map[string]float64{}
		for method, usd := range byRung {
			out.TotalSavedUSDByRung[method] = roundUSDShallow(usd)
		}
	}
	out.Caveats = append(out.Caveats,
		"Savings are grouped by attribution method and never summed across methods: a re-counted file and a before/after session median are not the same kind of evidence.",
		"Priced savings are a per-day rate over the scanned window at your measured effective input rate. They are inferred, never verified, and never projected to a month.",
	)
	if len(out.Rows) == 0 {
		out.Caveats = append(out.Caveats,
			"No fix has been recorded yet. Apply one through the caveman-learn skill and it will appear here with its attribution.")
	}
	return out
}

func windowDaysOf(plan LearnPlan) float64 {
	return windowDays(plan.Window.Since, plan.Window.From, plan.Window.To)
}

func roundUSDShallow(v float64) float64 {
	return float64(int64(v*1_000_000+0.5)) / 1_000_000
}

// describeAttribution renders one line for the CLI/Markdown surfaces.
func describeAttribution(a LearnAttribution) string {
	if a.Method == "" || a.Method == attrNone {
		return "unattributed"
	}
	return fmt.Sprintf("%s · confidence %s · %s", a.Method, a.Confidence, a.Provenance)
}

// BuildLearnSavings is the front door for `caveman learn savings`: build the
// plan (which loads the outcome ledger and re-checks every fingerprint), then
// render the attributed ledger from it.
func (s *Store) BuildLearnSavings(cwd string, sources []string, sinceExpr string) (LearnSavings, error) {
	plan, err := s.BuildLearnPlan(cwd, sources, sinceExpr)
	if err != nil {
		return LearnSavings{}, err
	}
	return buildLearnSavings(plan), nil
}
