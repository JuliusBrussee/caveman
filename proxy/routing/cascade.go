package routing

// CascadeRouter implements a FrugalGPT-style cascade on top of any base Router.
//
// The cascade answers the CHEAP rung first; the proxy loop later decides whether
// to escalate to the next, more-expensive rung — using the eval grader (shadow
// mode) or the cheap confidence heuristic (active-mode fallback). The Pick here
// is PURE and deterministic: it makes no upstream calls and never escalates by
// itself. It only reports which rung to try first and whether a higher rung
// exists to escalate to. The actual escalation (a second live call) is the
// proxy's job, never the router's.
type CascadeRouter struct {
	Base Router // e.g. RulesRouter{}
}

// Pick delegates to Base to choose the cheap rung and rank the cheapest-first
// ladder, then marks the decision escalatable iff a more-expensive rung exists
// after the chosen one. When Base returns no route, the decision is propagated
// unchanged (reason intact, not escalatable). Pure and deterministic.
func (c CascadeRouter) Pick(f Features, pool []Candidate, alpha float64) (Decision, error) {
	dec, err := c.Base.Pick(f, pool, alpha)
	if err != nil {
		return dec, err
	}
	if dec.Model == "" {
		// No route — propagate the base reason untouched; nothing to escalate.
		return dec, nil
	}
	_, escalatable := NextActionRung(dec.Ranked, dec.ActionID)
	dec.Reason = "cascade_cheap_pick"
	dec.Escalatable = escalatable
	return dec, nil
}

// NextRung returns the next more-expensive candidate after current in a
// cheapest-first ranked ladder, and true. It returns false when current is the
// most-expensive rung or is absent from the ladder. ranked is cheapest-first, so
// the escalation target is the candidate at the index immediately after current.
func NextRung(ranked []Candidate, current string) (Candidate, bool) {
	idx := rungIndex(ranked, current)
	if idx < 0 || idx+1 >= len(ranked) {
		return Candidate{}, false
	}
	return ranked[idx+1], true
}

// NextActionRung is effort-safe NextRung. Model-only lookup is ambiguous once
// one model appears at several effort levels, so v3 cascade walks by canonical
// action identity.
func NextActionRung(ranked []Candidate, actionID string) (Candidate, bool) {
	if actionID == "" {
		return Candidate{}, false
	}
	for i, candidate := range ranked {
		if CandidateActionID(candidate) != actionID {
			continue
		}
		if i+1 >= len(ranked) {
			return Candidate{}, false
		}
		return ranked[i+1], true
	}
	return Candidate{}, false
}

func rungIndex(ranked []Candidate, current string) int {
	if current == "" {
		return -1
	}
	for i, c := range ranked {
		if c.Model == current {
			return i
		}
	}
	return -1
}
