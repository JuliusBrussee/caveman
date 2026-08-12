package routing

import "testing"

func TestCascadePicksCheapestCapableAtAlphaZero(t *testing.T) {
	f := Features{
		Provider:         "openai",
		Endpoint:         "/openai/v1/responses",
		CurrentModel:     "cave-auto",
		BaselineModel:    "gpt-5.5",
		BaselinePrice:    price(5, 30),
		BodyModelRewrite: true,
	}
	pool := []Candidate{
		candidate("openai", "gpt-5.4-mini", price(0.6, 2.4), capsWithContext(128000, "responses_api")),
		candidate("openai", "gpt-5.4-nano", price(0.15, 0.6), capsWithContext(128000, "responses_api")),
	}
	dec, err := (CascadeRouter{Base: RulesRouter{}}).Pick(f, pool, 0)
	if err != nil {
		t.Fatal(err)
	}
	if dec.Model != "gpt-5.4-nano" {
		t.Fatalf("model = %q, want cheapest capable nano", dec.Model)
	}
	if dec.Reason != "cascade_cheap_pick" {
		t.Fatalf("reason = %q, want cascade_cheap_pick", dec.Reason)
	}
	if !dec.Escalatable {
		t.Fatalf("escalatable = false, want true with a more-expensive rung above the cheap pick")
	}
}

func TestCascadeNotEscalatableWithSingleRung(t *testing.T) {
	f := Features{
		Provider:         "openai",
		Endpoint:         "/openai/v1/responses",
		CurrentModel:     "cave-auto",
		BaselineModel:    "gpt-5.5",
		BaselinePrice:    price(5, 30),
		BodyModelRewrite: true,
	}
	pool := []Candidate{
		candidate("openai", "gpt-5.4-mini", price(0.6, 2.4), capsWithContext(128000, "responses_api")),
	}
	dec, err := (CascadeRouter{Base: RulesRouter{}}).Pick(f, pool, 0)
	if err != nil {
		t.Fatal(err)
	}
	if dec.Model != "gpt-5.4-mini" {
		t.Fatalf("model = %q, want the only cheaper candidate", dec.Model)
	}
	if dec.Escalatable {
		t.Fatalf("escalatable = true, want false with a single rung")
	}
}

func TestCascadeAlphaOneTopRungNotEscalatable(t *testing.T) {
	f := Features{
		Provider:         "openai",
		Endpoint:         "/openai/v1/responses",
		CurrentModel:     "cave-auto",
		BaselineModel:    "gpt-5.5",
		BaselinePrice:    price(5, 30),
		BodyModelRewrite: true,
	}
	pool := []Candidate{
		candidate("openai", "gpt-5.4-mini", price(0.6, 2.4), capsWithContext(128000, "responses_api")),
		candidate("openai", "gpt-5.4-nano", price(0.15, 0.6), capsWithContext(128000, "responses_api")),
	}
	// alpha 1 selects the closest-cheaper (most expensive) rung — nothing above it.
	dec, err := (CascadeRouter{Base: RulesRouter{}}).Pick(f, pool, 1)
	if err != nil {
		t.Fatal(err)
	}
	if dec.Model != "gpt-5.4-mini" {
		t.Fatalf("model = %q, want closest cheaper mini at alpha 1", dec.Model)
	}
	if dec.Escalatable {
		t.Fatalf("escalatable = true, want false at the top of the cheaper ladder")
	}
}

func TestCascadeNoRoutePropagatedUnchanged(t *testing.T) {
	f := Features{Provider: "openai", BodyModelRewrite: true} // no priced baseline
	dec, err := (CascadeRouter{Base: RulesRouter{}}).Pick(f, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if dec.Model != "" {
		t.Fatalf("model = %q, want no route", dec.Model)
	}
	if dec.Escalatable {
		t.Fatalf("escalatable = true, want false with no route")
	}
	if dec.Reason != "missing_priced_baseline" {
		t.Fatalf("reason = %q, want base reason propagated unchanged", dec.Reason)
	}
}

func TestNextRungWalksLadderCheapestToExpensive(t *testing.T) {
	ranked := []Candidate{
		candidate("openai", "nano", price(0.15, 0.6), nil),
		candidate("openai", "mini", price(0.6, 2.4), nil),
		candidate("openai", "full", price(5, 30), nil),
	}
	next, ok := NextRung(ranked, "nano")
	if !ok || next.Model != "mini" {
		t.Fatalf("NextRung(nano) = (%q, %v), want (mini, true)", next.Model, ok)
	}
	next, ok = NextRung(ranked, "mini")
	if !ok || next.Model != "full" {
		t.Fatalf("NextRung(mini) = (%q, %v), want (full, true)", next.Model, ok)
	}
	if _, ok := NextRung(ranked, "full"); ok {
		t.Fatalf("NextRung(full) = true, want false at the most-expensive rung")
	}
	if _, ok := NextRung(ranked, "absent"); ok {
		t.Fatalf("NextRung(absent) = true, want false for a model not on the ladder")
	}
	if _, ok := NextRung(ranked, ""); ok {
		t.Fatalf("NextRung(\"\") = true, want false for an empty current model")
	}
}

func TestNextActionRungDistinguishesEffort(t *testing.T) {
	ranked := []Candidate{
		{Provider: "openai", Model: "mini", Effort: "low"},
		{Provider: "openai", Model: "mini", Effort: "high"},
		{Provider: "openai", Model: "full", Effort: "high"},
	}
	next, ok := NextActionRung(ranked, CandidateActionID(ranked[0]))
	if !ok || next.Model != "mini" || next.Effort != "high" {
		t.Fatalf("next action = %+v/%v, want mini/high", next, ok)
	}
	if _, ok := NextActionRung(ranked, CandidateActionID(ranked[2])); ok {
		t.Fatal("last action reported escalatable")
	}
}
