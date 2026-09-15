package store

import (
	"fmt"
	"sort"
	"strings"
)

// learn_digest.go is the local -> cloud bridge.
//
// The cloud Cave Plan needs gateway traffic before it can say anything, so a new
// tenant sees an empty dashboard on day 0. This exports what the local scan
// already measured, in a form that can seed a plan immediately: sink identities,
// practice ids, counts, provider-counted aggregates, catalog version, and the
// confidence rung behind each row.
//
// What it deliberately does NOT carry, by construction rather than by filtering:
//
//   - no locators (rel_path / jsonl_line / block_index / content hashes)
//   - no evidence maps, so no paths, hook names, tool signatures, or step lists
//   - no repository names, no model-free text of any kind
//   - no session content, ever
//
// The rule is allowlist, not denylist: this file builds rows field by field from
// scalars it names, so a future evidence key cannot smuggle content into an
// export. It is user-initiated (`caveman learn export --digest`), never a
// background upload — telemetry here is opt-in by decree.

const learnDigestSchema = "caveman.learn.digest.v1"

// LearnDigestSink is one sink, reduced to identity and magnitude.
type LearnDigestSink struct {
	SinkID     string `json:"sink_id"`
	PracticeID string `json:"practice_id,omitempty"`
	Class      string `json:"class"`
	Basis      string `json:"basis"`
	Framing    string `json:"framing"`
	// Rung is the measurement ladder position behind this row, so the cloud can
	// rank a provider-counted finding above a static-file estimate rather than
	// treating every local row as equally solid.
	Rung             int     `json:"rung"`
	TokensPerTurn    int64   `json:"tokens_per_turn,omitempty"`
	TokensPerDayRate int64   `json:"tokens_per_day_rate,omitempty"`
	TokensObserved   int64   `json:"tokens_observed,omitempty"`
	SpendUSDPerDay   float64 `json:"spend_usd_per_day,omitempty"`
	SpendUSDObserved float64 `json:"spend_usd_observed,omitempty"`
}

// LearnDigestConfirmed is one applied fix reduced to its outcome and rung. The
// target path is dropped: the cloud needs to know a fix held, not where it lives.
type LearnDigestConfirmed struct {
	SinkID     string   `json:"sink_id"`
	FixKind    string   `json:"fix_kind"`
	AppliedAt  string   `json:"applied_at"`
	Verdict    string   `json:"verdict"`
	Unit       string   `json:"unit"`
	Before     float64  `json:"before"`
	After      *float64 `json:"after,omitempty"`
	Method     string   `json:"attribution_method"`
	Confidence string   `json:"attribution_confidence"`
	Provenance string   `json:"attribution_provenance"`
}

// LearnDigest is the exported artifact. It is inspectable before it is sent,
// which is the point of keeping it small and flat.
type LearnDigest struct {
	Schema           string                 `json:"schema"`
	Basis            string                 `json:"basis"`
	Window           LearnWindow            `json:"window"`
	CaveScore        int                    `json:"cave_score"`
	SessionsScanned  int                    `json:"sessions_scanned"`
	SessionsBySource map[string]int         `json:"sessions_by_source,omitempty"`
	Sinks            []LearnDigestSink      `json:"sinks"`
	Confirmed        []LearnDigestConfirmed `json:"confirmed,omitempty"`
	// Spend carries totals and per-component shares. Per-MODEL rows are kept
	// because a model id is not user content and the cloud needs it to price;
	// nothing else from the spend block travels.
	SpendUSD                 float64               `json:"spend_usd,omitempty"`
	SpendCurrency            string                `json:"spend_currency,omitempty"`
	SpendComponents          []LearnSpendComponent `json:"spend_components,omitempty"`
	SpendModels              []LearnSpendModel     `json:"spend_models,omitempty"`
	CatalogVersion           string                `json:"catalog_version,omitempty"`
	EffectiveInputUSDPerMTok float64               `json:"effective_input_usd_per_mtok,omitempty"`
	EffectiveInputMultiplier float64               `json:"effective_input_multiplier,omitempty"`
	Contains                 []string              `json:"contains"`
	Excludes                 []string              `json:"excludes"`
	Caveats                  []string              `json:"caveats"`
}

// digestRung maps a sink's basis onto the measurement ladder. Unknown bases sit
// at the bottom rather than being credited with a rung they did not earn.
func digestRung(basis string) int {
	switch strings.TrimSpace(basis) {
	case "provider_counted":
		return 3
	case observedLocal:
		return 2
	case learnBasis:
		return 1
	default:
		return 0
	}
}

// BuildLearnDigest builds the export from a scan.
func (s *Store) BuildLearnDigest(cwd string, sources []string, sinceExpr string) (LearnDigest, error) {
	plan, err := s.BuildLearnPlan(cwd, sources, sinceExpr)
	if err != nil {
		return LearnDigest{}, err
	}
	return buildLearnDigest(plan), nil
}

func buildLearnDigest(plan LearnPlan) LearnDigest {
	digest := LearnDigest{
		Schema: learnDigestSchema, Basis: learnBasis, Window: plan.Window,
		CaveScore: plan.CaveScore.Score, SessionsScanned: plan.SessionsScanned,
		SessionsBySource: plan.SessionsBySource,
		Sinks:            []LearnDigestSink{},
	}
	for _, sink := range plan.Sinks {
		digest.Sinks = append(digest.Sinks, LearnDigestSink{
			SinkID: digestSinkID(sink.SinkID), PracticeID: sink.PracticeID,
			Class: sink.Class, Basis: sink.Basis, Framing: sink.Framing,
			Rung:          digestRung(sink.Basis),
			TokensPerTurn: sink.TokensPerTurn, TokensPerDayRate: sink.TokensPerDayRate,
			TokensObserved: sink.TokensObserved,
			SpendUSDPerDay: sink.SpendUSDPerDay, SpendUSDObserved: sink.SpendUSDObserved,
		})
	}
	sort.SliceStable(digest.Sinks, func(i, j int) bool { return digest.Sinks[i].SinkID < digest.Sinks[j].SinkID })

	for _, confirmed := range plan.Confirmed {
		digest.Confirmed = append(digest.Confirmed, LearnDigestConfirmed{
			SinkID: digestSinkID(confirmed.SinkID), FixKind: confirmed.FixKind,
			AppliedAt: confirmed.AppliedAt, Verdict: confirmed.Verdict, Unit: confirmed.Unit,
			Before: confirmed.Before, After: confirmed.After,
			Method: confirmed.Attribution.Method, Confidence: confirmed.Attribution.Confidence,
			Provenance: confirmed.Attribution.Provenance,
		})
	}
	if plan.Spend != nil {
		digest.SpendUSD = plan.Spend.USD
		digest.SpendCurrency = plan.Spend.Currency
		digest.SpendComponents = plan.Spend.Components
		digest.SpendModels = plan.Spend.Models
		digest.CatalogVersion = plan.Spend.CatalogVersion
		digest.EffectiveInputUSDPerMTok = plan.Spend.EffectiveInputUSDPerMTok
		digest.EffectiveInputMultiplier = plan.Spend.EffectiveInputMultiplier
	}
	digest.Contains = []string{
		"sink identities and practice ids",
		"token and spend magnitudes per sink",
		"provider and model names with their priced totals",
		"applied-fix outcomes with their attribution rung",
	}
	digest.Excludes = []string{
		"no session content, prompts, or tool output",
		"no file paths, repository names, hook names, or tool signatures",
		"no locators or content hashes",
		"no evidence maps of any kind",
	}
	digest.Caveats = []string{
		"This export is built field by field from named scalars, so evidence added to a sink in future cannot leak into it.",
		"Every figure is inferred and window-bounded. Uploading it does not make anything verified; only gateway-observed traffic can do that.",
		"Nothing here is sent anywhere by running this command. It writes a file for you to inspect and decide about.",
	}
	return digest
}

// digestSinkID strips the opaque suffix from identity-bearing sink ids. The
// suffix is a content hash of user data (a repeated block, a procedure's step
// sequence); the family is what the cloud needs, the hash is not.
func digestSinkID(sinkID string) string {
	for _, prefix := range []string{"recurring_context:repaste:", "procedure_repeat:", "learning_loop:"} {
		if strings.HasPrefix(sinkID, prefix) {
			return strings.TrimSuffix(prefix, ":") + ":*"
		}
	}
	return sinkID
}

// DigestSummaryLine is the one-line human framing the CLI prints next to the path.
func (d LearnDigest) DigestSummaryLine() string {
	return fmt.Sprintf("%d findings · %d sessions · cave score %d · nothing sent",
		len(d.Sinks), d.SessionsScanned, d.CaveScore)
}
