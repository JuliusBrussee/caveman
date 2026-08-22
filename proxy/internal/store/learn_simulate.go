package store

import (
	"fmt"
	"math"
	"sort"
	"strings"
)

const (
	learnSimulateSchema  = "caveman.learn.simulate.v1"
	learnPointerTemplate = "Context stored in cavemem. Recall topic %s only when needed; recover exact source through its handle."
)

type LearnSimulation struct {
	Schema                      string               `json:"schema"`
	Basis                       string               `json:"basis"`
	Window                      LearnWindow          `json:"window"`
	Rows                        []LearnSimulationRow `json:"per_sink"`
	TotalTokensWouldHaveSkipped *int64               `json:"total_tokens_would_have_skipped,omitempty"`
	Caveats                     []string             `json:"caveats"`
}

type LearnSimulationRow struct {
	SinkID                 string   `json:"sink_id"`
	Method                 string   `json:"method"`
	Basis                  string   `json:"basis,omitempty"`
	TokensWouldHaveSkipped *int64   `json:"tokens_would_have_skipped,omitempty"`
	Caveats                []string `json:"caveats"`
}

// BuildLearnSimulation evaluates only content observed in the selected scan
// window. It never invokes retro, projects future turns, or prices tokens.
func (s *Store) BuildLearnSimulation(cwd string, sources []string, sinceExpr string, sinkIDs []string) (LearnSimulation, error) {
	return s.BuildLearnSimulationFiltered(cwd, sources, sinceExpr, sinkIDs, "")
}

func (s *Store) BuildLearnSimulationFiltered(cwd string, sources []string, sinceExpr string, sinkIDs []string, repoFilter string) (LearnSimulation, error) {
	plan, err := s.BuildLearnPlanFilteredWithRetro(cwd, sources, sinceExpr, RetroOptions{}, repoFilter)
	if err != nil {
		return LearnSimulation{}, err
	}
	return simulateLearnPlan(plan, plan.observedTurns, sinkIDs)
}

func simulateLearnPlan(plan LearnPlan, observedTurns int, sinkIDs []string) (LearnSimulation, error) {
	out := LearnSimulation{
		Schema: learnSimulateSchema, Basis: learnBasis, Window: plan.Window,
		Caveats: []string{
			"Counterfactual covers only content and provider-counted turns actually scanned; it is never projected forward and never converted to dollars.",
			"Config and recurring-context methods are summed only under the assumption that they describe disjoint content.",
		},
	}
	byID := make(map[string]Sink, len(plan.Sinks))
	for _, sink := range plan.Sinks {
		byID[sink.SinkID] = sink
	}
	unique := map[string]bool{}
	for _, id := range sinkIDs {
		id = strings.TrimSpace(id)
		if id != "" {
			unique[id] = true
		}
	}
	ids := make([]string, 0, len(unique))
	for id := range unique {
		if _, ok := byID[id]; !ok {
			return LearnSimulation{}, fmt.Errorf("sink %q not found in current learn plan", id)
		}
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return LearnSimulation{}, fmt.Errorf("at least one sink id is required")
	}
	sort.Strings(ids)

	// One owner per config file/family. Prefer broader measured weight, then sink
	// id, so overlap refusal is stable regardless of CLI argument order.
	owners := map[string]string{}
	for _, id := range ids {
		sink := byID[id]
		key := simulationOverlapKey(sink)
		if key == "" {
			continue
		}
		owner, exists := owners[key]
		if !exists || sink.TokensPerTurn > byID[owner].TokensPerTurn ||
			(sink.TokensPerTurn == byID[owner].TokensPerTurn && id < owner) {
			owners[key] = id
		}
	}

	var total int64
	hasSummable := false
	totalValid := true
	for _, id := range ids {
		sink := byID[id]
		row := LearnSimulationRow{SinkID: id}
		if strings.HasPrefix(id, "recurring_context:repaste:") {
			row.Method = "occurrence_sum"
			row.Basis = "bytes4_estimate"
			row.Caveats = []string{"Arithmetic retains one largest observed occurrence and charges one standard pointer for every observed occurrence; recall cost is unavailable here, so real apply still requires the net-token-negative gate."}
		} else if isSimulatableConfigSink(sink) {
			row.Method = "per_turn_times_observed_turns"
			row.Caveats = []string{fmt.Sprintf("Would not have been established across the %d provider-counted turns actually scanned; no future turn is included.", observedTurns)}
		} else {
			return LearnSimulation{}, fmt.Errorf("sink %q is not a bounded recurring-context or reducible config sink", id)
		}

		if key := simulationOverlapKey(sink); key != "" && owners[key] != id {
			row.Caveats = append(row.Caveats, fmt.Sprintf("Excluded from arithmetic because it overlaps %s on %s; counting both would double-count.", owners[key], key))
			out.Rows = append(out.Rows, row)
			continue
		}

		var skipped int64
		if row.Method == "occurrence_sum" {
			totalTokens, totalOK := int64Evidence(sink.Evidence["occurrence_tokens_total"])
			largest, largestOK := int64Evidence(sink.Evidence["block_tokens"])
			occurrences, occurrencesOK := int64Evidence(sink.Evidence["occurrences_total"])
			if !totalOK || !largestOK || !occurrencesOK || occurrences <= 0 {
				row.Caveats = append(row.Caveats, "Required occurrence totals were unavailable; no arithmetic emitted.")
				out.Rows = append(out.Rows, row)
				continue
			}
			fingerprint := strings.TrimPrefix(id, "recurring_context:repaste:")
			pointerTokens := estimateTokens(fmt.Sprintf(learnPointerTemplate, fingerprint))
			pointerCost, ok := checkedProduct(occurrences, int64(pointerTokens))
			if !ok {
				row.Caveats = append(row.Caveats, "Pointer arithmetic overflowed; no arithmetic emitted.")
				out.Rows = append(out.Rows, row)
				continue
			}
			if largest >= totalTokens || pointerCost >= totalTokens-largest {
				skipped = 0
			} else {
				skipped = totalTokens - largest - pointerCost
			}
		} else {
			product, ok := checkedProduct(sink.TokensPerTurn, int64(observedTurns))
			if !ok {
				row.Caveats = append(row.Caveats, "Observed-turn arithmetic overflowed; no arithmetic emitted.")
				out.Rows = append(out.Rows, row)
				continue
			}
			skipped = product
		}
		if skipped <= 0 {
			row.Caveats = append(row.Caveats, "Bounded arithmetic established no positive token skip, so omit-not-zero suppresses the value.")
			out.Rows = append(out.Rows, row)
			continue
		}
		value := skipped
		row.TokensWouldHaveSkipped = &value
		out.Rows = append(out.Rows, row)
		if totalValid {
			if sum, ok := checkedSum(total, skipped); ok {
				total = sum
				hasSummable = true
			} else {
				totalValid = false
				hasSummable = false
				total = 0
				out.Caveats = append(out.Caveats, "Selected rows overflowed a safe total; per-sink values remain separate.")
			}
		}
	}
	if totalValid && hasSummable && total > 0 {
		out.TotalTokensWouldHaveSkipped = &total
	}
	return out, nil
}

func isSimulatableConfigSink(sink Sink) bool {
	if sink.Class != classReducible {
		return false
	}
	return strings.HasPrefix(sink.SinkID, "claude_md_weight:") ||
		strings.HasPrefix(sink.SinkID, "claude_md_sections:") || sink.SinkID == "dead_load:skills"
}

func simulationOverlapKey(sink Sink) string {
	switch {
	case strings.HasPrefix(sink.SinkID, "claude_md_weight:"):
		return "config:" + strings.TrimPrefix(sink.SinkID, "claude_md_weight:")
	case strings.HasPrefix(sink.SinkID, "claude_md_sections:"):
		return "config:" + strings.TrimPrefix(sink.SinkID, "claude_md_sections:")
	case sink.SinkID == "dead_load:skills":
		return "config:skills"
	case strings.HasPrefix(sink.SinkID, "recurring_context:repaste:"):
		return "recurring:" + fmt.Sprint(sink.Evidence["fingerprint"])
	default:
		return ""
	}
}

func int64Evidence(value any) (int64, bool) {
	number, ok := numberFromAny(value)
	if !ok || number < 0 || number > math.MaxInt64 || math.Trunc(number) != number {
		return 0, false
	}
	return int64(number), true
}

func checkedProduct(a, b int64) (int64, bool) {
	if a < 0 || b < 0 || (a != 0 && b > math.MaxInt64/a) {
		return 0, false
	}
	return a * b, true
}

func checkedSum(a, b int64) (int64, bool) {
	if a < 0 || b < 0 || b > math.MaxInt64-a {
		return 0, false
	}
	return a + b, true
}
