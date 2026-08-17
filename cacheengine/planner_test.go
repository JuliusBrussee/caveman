package cacheengine

import (
	"context"
	"strings"
	"testing"
	"time"
)

func explicitProfile() Profile {
	return Profile{
		ID:              "test-explicit",
		Mode:            ModeExplicit,
		Attribution:     AttributionCausal,
		MinPrefixTokens: 512,
		MaxBreakpoints:  4,
		EconomicsKnown:  true,
		WriteMultiplier: 1.25,
		ReadMultiplier:  0.10,
		TTL:             5 * time.Minute,
		RoutingKey:      true,
	}
}

func TestPlanSelectsReuseFrontierWithoutDoubleCounting(t *testing.T) {
	engine := New(Config{MaxKeyShards: 32})
	plan, err := engine.Plan(PlanRequest{
		Scope:                     "org-a/project-a",
		Epoch:                     "conversation-1",
		PartitionKey:              "session-7",
		ExpectedRequestsPerMinute: 31,
		ExpectedCalls:             10,
		Profile:                   explicitProfile(),
		Segments: []Segment{
			{Name: "tools", Content: []byte(strings.Repeat("t", 2400)), Tokens: 600, Stable: true, Cacheable: true, ExpectedCalls: 10},
			{Name: "system", Content: []byte(strings.Repeat("s", 2000)), Tokens: 500, Stable: true, Cacheable: true, ExpectedCalls: 4},
			{Name: "live", Content: []byte("changes each request"), Tokens: 5, Stable: false, Cacheable: false},
		},
	})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if plan.Decision != DecisionApply {
		t.Fatalf("decision = %q, want %q (%s)", plan.Decision, DecisionApply, plan.Reason)
	}
	if len(plan.Breakpoints) != 2 {
		t.Fatalf("breakpoints = %d, want 2: %#v", len(plan.Breakpoints), plan.Breakpoints)
	}
	if plan.Breakpoints[0].AfterSegment != "tools" || plan.Breakpoints[1].AfterSegment != "system" {
		t.Fatalf("breakpoint frontier = %#v", plan.Breakpoints)
	}
	if plan.ExpectedNetInputRateUnits != plan.Breakpoints[0].ExpectedNetInputRateUnits {
		t.Fatalf("headline net = %v, want best single frontier value %v", plan.ExpectedNetInputRateUnits, plan.Breakpoints[0].ExpectedNetInputRateUnits)
	}
	if plan.KeyShardCount != 3 || plan.KeyShard < 0 || plan.KeyShard >= plan.KeyShardCount {
		t.Fatalf("sharding = %d/%d, want stable shard among 3", plan.KeyShard, plan.KeyShardCount)
	}
	if len(plan.RoutingKey) != 32 || strings.Contains(plan.RoutingKey, "org-a") {
		t.Fatalf("routing key must be opaque 128-bit hex, got %q", plan.RoutingKey)
	}
}

func TestRoutingShardMathHandlesMaximumTrafficInput(t *testing.T) {
	profile := explicitProfile()
	plan, err := New(Config{MaxKeyShards: 32}).Plan(PlanRequest{
		Scope: "org/project", Epoch: "max-traffic", ExpectedCalls: 2,
		ExpectedRequestsPerMinute: int(^uint(0) >> 1), Profile: profile,
		Segments: []Segment{{Name: "stable", Content: []byte("stable"), Tokens: 1024, Stable: true, Cacheable: true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.KeyShardCount != 32 || plan.KeyShard < 0 || plan.KeyShard >= 32 || !containsString(plan.Warnings, "routing_key_shard_cap_reached") {
		t.Fatalf("sharding = %d/%d", plan.KeyShard, plan.KeyShardCount)
	}
}

func TestInvalidEngineConfigurationFailsClosed(t *testing.T) {
	driver := DriverFunc(func(context.Context, NativeRequest, Plan) DriverResult { return DriverResult{} })
	tests := []struct {
		name   string
		config Config
	}{
		{name: "negative shards", config: Config{MaxKeyShards: -1}},
		{name: "excessive shards", config: Config{MaxKeyShards: maxConfiguredKeyShards + 1}},
		{name: "negative request bytes", config: Config{MaxRequestBytes: -1}},
		{name: "excessive request bytes", config: Config{MaxRequestBytes: maxConfiguredByteLimit + 1}},
		{name: "negative prefix bytes", config: Config{MaxStablePrefixBytes: -1}},
		{name: "excessive prefix bytes", config: Config{MaxStablePrefixBytes: maxConfiguredByteLimit + 1}},
		{name: "empty driver", config: Config{Drivers: map[string]Driver{"": driver}}},
		{name: "nil driver", config: Config{Drivers: map[string]Driver{"acme": nil}}},
		{name: "normalized collision", config: Config{Drivers: map[string]Driver{"ACME": driver, " acme ": driver}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := NewChecked(test.config); err == nil {
				t.Fatal("invalid checked configuration accepted")
			}
			if _, err := New(test.config).Plan(PlanRequest{}); err == nil {
				t.Fatal("invalid legacy configuration became usable")
			}
		})
	}
}

func TestEngineRejectsOversizedStablePrefixBeforePlanning(t *testing.T) {
	engine, err := NewChecked(Config{MaxStablePrefixBytes: 16})
	if err != nil {
		t.Fatal(err)
	}
	_, err = engine.Plan(PlanRequest{
		Scope: "scope", Epoch: "epoch", ExpectedCalls: 2,
		Profile:  Profile{ID: "acme", Provider: "acme", Mode: ModeExplicit, Attribution: AttributionCausal, MaxBreakpoints: 1},
		Segments: []Segment{{Name: "system", Content: []byte("too-large"), Stable: true, Cacheable: true}},
	})
	if err == nil || !strings.Contains(err.Error(), "byte limit") {
		t.Fatalf("error=%v", err)
	}
}

func TestPlanRejectsAmbiguousOrUnboundedIdentity(t *testing.T) {
	tests := []PlanRequest{
		{Scope: "scope\x00", Epoch: "epoch", ExpectedCalls: 2, Profile: explicitProfile(), Segments: []Segment{{Name: "stable", Content: []byte("x"), Stable: true, Cacheable: true}}},
		{Scope: "scope", Epoch: strings.Repeat("e", 4097), ExpectedCalls: 2, Profile: explicitProfile(), Segments: []Segment{{Name: "stable", Content: []byte("x"), Stable: true, Cacheable: true}}},
		{Scope: "scope", Epoch: "epoch", ExpectedCalls: 2, Profile: explicitProfile(), Segments: []Segment{{Name: "same", Content: []byte("x"), Stable: true, Cacheable: true}, {Name: "same", Content: []byte("y"), Stable: true, Cacheable: true}}},
	}
	for index, request := range tests {
		if _, err := New(Config{}).Plan(request); err == nil {
			t.Errorf("case %d accepted", index)
		}
	}
}

func TestPlanRejectsCumulativeTokenOverflow(t *testing.T) {
	_, err := New(Config{}).Plan(PlanRequest{
		Scope: "org/project", Epoch: "token-overflow", ExpectedCalls: 2, Profile: explicitProfile(),
		Segments: []Segment{
			{Name: "first", Content: []byte("first"), Tokens: int(^uint(0) >> 1), Stable: true, Cacheable: true},
			{Name: "second", Content: []byte("second"), Tokens: 1, Stable: true, Cacheable: true},
		},
	})
	if err == nil {
		t.Fatal("cumulative token overflow accepted")
	}
}

func TestBedrockModelSpecificCacheMinimums(t *testing.T) {
	tests := map[string]int{
		"anthropic.claude-3-5-haiku-20241022-v1:0":        2048,
		"global.anthropic.claude-haiku-4-5-20251001-v1:0": 4096,
		"global.anthropic.claude-sonnet-4-6":              1024,
	}
	for model, expected := range tests {
		if actual := bedrockMinimum(model); actual != expected {
			t.Fatalf("%s minimum=%d want=%d", model, actual, expected)
		}
	}
}

func TestBedrockCacheCapabilityIsBoundToRuntimeSurface(t *testing.T) {
	if !bedrockCachePointEndpointEligible("global.anthropic.claude-sonnet-4-6", "converse") {
		t.Fatal("catalog-backed Bedrock Converse surface rejected")
	}
	if bedrockCachePointEndpointEligible("anthropic.claude-sonnet-4-6-v1", "converse") {
		t.Fatal("Bedrock Mantle-only model accepted on runtime Converse surface")
	}
}

func TestPlanRejectsVolatileDeclaredStablePrefix(t *testing.T) {
	engine := New(Config{})
	plan, err := engine.Plan(PlanRequest{
		Scope:         "org-a/project-a",
		Epoch:         "conversation-volatile",
		ExpectedCalls: 3,
		Profile:       explicitProfile(),
		Segments: []Segment{{
			Name: "system", Content: []byte(`generated_at: 2026-08-09T12:34:56Z`), Tokens: 900, Stable: true, Cacheable: true,
		}},
	})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if plan.Decision != DecisionPassThrough || plan.Reason != ReasonVolatilePrefix {
		t.Fatalf("plan = %#v, want volatile pass-through", plan)
	}
	if len(plan.Warnings) == 0 || plan.Warnings[0] != "volatile_stable_slot" {
		t.Fatalf("warnings = %#v", plan.Warnings)
	}
}

func TestPlanDetectsDriftUntilCallerStartsNewEpoch(t *testing.T) {
	engine := New(Config{})
	base := PlanRequest{
		Scope:         "org-a/project-a",
		Epoch:         "conversation-drift",
		ExpectedCalls: 3,
		Profile:       explicitProfile(),
		Segments:      []Segment{{Name: "system", Content: []byte(strings.Repeat("a", 2200)), Tokens: 600, Stable: true, Cacheable: true}},
	}
	first, err := engine.Plan(base)
	if err != nil || first.Decision != DecisionApply {
		t.Fatalf("first = %#v, err=%v", first, err)
	}
	base.Segments[0].Content = []byte(strings.Repeat("b", 2200))
	drift, err := engine.Plan(base)
	if err != nil {
		t.Fatalf("drift plan: %v", err)
	}
	if drift.Decision != DecisionPassThrough || drift.Reason != ReasonPrefixDrift {
		t.Fatalf("drift = %#v", drift)
	}
	if _, err := engine.StartEpoch(base); err != nil {
		t.Fatalf("start epoch: %v", err)
	}
	reset, err := engine.Plan(base)
	if err != nil || reset.Decision != DecisionApply {
		t.Fatalf("reset = %#v, err=%v", reset, err)
	}
}

func TestPlanEconomicsAndEligibilityFailClosed(t *testing.T) {
	engine := New(Config{})
	request := PlanRequest{
		Scope: "org-a/project-a", Epoch: "economics", ExpectedCalls: 2, Profile: explicitProfile(),
		Segments: []Segment{{Name: "system", Content: []byte(strings.Repeat("x", 4096)), Tokens: 1024, Stable: true, Cacheable: true}},
	}
	plan, err := engine.Plan(request)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	// Baseline: 2.0 input-rate units/token. Cached: 1.25 write + 0.10 read.
	if plan.ExpectedNetInputRateUnits != 665.6 {
		t.Fatalf("net units = %v, want 665.6", plan.ExpectedNetInputRateUnits)
	}
	request.Epoch = "below-minimum"
	request.Segments[0].Tokens = 511
	below, err := engine.Plan(request)
	if err != nil {
		t.Fatalf("below plan: %v", err)
	}
	if below.Decision != DecisionPassThrough || below.Reason != ReasonBelowMinimum {
		t.Fatalf("below = %#v", below)
	}
	request.Epoch = "single-call"
	request.Segments[0].Tokens = 1024
	request.ExpectedCalls = 1
	single, err := engine.Plan(request)
	if err != nil {
		t.Fatalf("single plan: %v", err)
	}
	if single.Decision != DecisionPassThrough || single.Reason != ReasonNoExpectedReuse {
		t.Fatalf("single = %#v", single)
	}
}

func TestImplicitProfileObservesWithoutClaimingTransform(t *testing.T) {
	profile := explicitProfile()
	profile.Mode = ModeImplicit
	profile.RoutingKey = false
	engine := New(Config{})
	plan, err := engine.Plan(PlanRequest{
		Scope: "org-a/project-a", Epoch: "implicit", ExpectedCalls: 3, Profile: profile,
		Segments: []Segment{{Name: "prefix", Content: []byte(strings.Repeat("x", 4096)), Tokens: 1024, Stable: true, Cacheable: true}},
	})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if plan.Decision != DecisionObserveOnly || plan.Reason != ReasonProviderManaged {
		t.Fatalf("plan = %#v", plan)
	}
	if plan.ExpectedNetInputRateUnits != 0 || plan.EconomicsBasis != "provider_managed_unattributed" {
		t.Fatalf("implicit plan claimed engine economics: %#v", plan)
	}
	for _, breakpoint := range plan.Breakpoints {
		if breakpoint.ExpectedNetInputRateUnits != 0 {
			t.Fatalf("implicit breakpoint claimed engine economics: %#v", breakpoint)
		}
	}
	if plan.RoutingKey != "" {
		t.Fatalf("implicit plan invented routing key %q", plan.RoutingKey)
	}
}

func TestUnknownTokenCountAppliesWithoutInventingEconomics(t *testing.T) {
	profile := explicitProfile()
	profile.MinPrefixTokens = 1024
	engine := New(Config{})
	plan, err := engine.Plan(PlanRequest{
		Scope: "org-a/project-a", Epoch: "unknown-count", ExpectedCalls: 3, Profile: profile,
		Segments: []Segment{{Name: "prefix", Content: []byte(strings.Repeat("x", 4096)), Stable: true, Cacheable: true}},
	})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if plan.Decision != DecisionApply || plan.EconomicsBasis != "unavailable" || plan.ExpectedNetInputRateUnits != 0 {
		t.Fatalf("plan = %#v", plan)
	}
	if !containsString(plan.Warnings, "token_count_unavailable") {
		t.Fatalf("warnings = %#v", plan.Warnings)
	}
}

func TestUnknownEconomicsAppliesWithoutInventingThreshold(t *testing.T) {
	profile := explicitProfile()
	profile.EconomicsKnown = false
	profile.WriteMultiplier = 0
	profile.ReadMultiplier = 0
	engine := New(Config{})
	request := PlanRequest{
		Scope: "org-a/project-a", Epoch: "unknown-economics", ExpectedCalls: 3, Profile: profile,
		Segments: []Segment{{Name: "prefix", Content: []byte(strings.Repeat("x", 4096)), Tokens: 1024, Stable: true, Cacheable: true}},
	}
	plan, err := engine.Plan(request)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if plan.Decision != DecisionApply || plan.EconomicsBasis != "unavailable" || plan.ExpectedNetInputRateUnits != 0 {
		t.Fatalf("plan = %#v", plan)
	}
	if len(plan.Breakpoints) != 1 || plan.Breakpoints[0].BreakEvenCalls != 0 || !containsString(plan.Warnings, "cache_economics_unavailable") {
		t.Fatalf("unknown economics detail = %#v", plan)
	}
	started, err := engine.StartEpoch(request)
	if err != nil {
		t.Fatalf("start epoch: %v", err)
	}
	if started.EconomicsBasis != "unavailable" || !containsString(started.Warnings, "cache_economics_unavailable") {
		t.Fatalf("new epoch = %#v", started)
	}
}

func TestKnownFreeWriteEconomicsRemainValid(t *testing.T) {
	profile := explicitProfile()
	profile.WriteMultiplier = 0
	profile.ReadMultiplier = 0.10
	plan, err := New(Config{}).Plan(PlanRequest{
		Scope: "org-a/project-a", Epoch: "free-write", ExpectedCalls: 2, Profile: profile,
		Segments: []Segment{{Name: "prefix", Content: []byte(strings.Repeat("x", 4096)), Tokens: 1000, Stable: true, Cacheable: true}},
	})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if plan.Decision != DecisionApply || plan.ExpectedNetInputRateUnits != 1900 || plan.Breakpoints[0].BreakEvenCalls != 2 {
		t.Fatalf("plan = %#v", plan)
	}
}

func TestInvalidProfileDataFailsClosed(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Profile)
	}{
		{"negative breakpoints", func(profile *Profile) { profile.MaxBreakpoints = -1 }},
		{"negative key rate", func(profile *Profile) { profile.MaxRPMPerKey = -1 }},
		{"negative ttl", func(profile *Profile) { profile.TTL = -time.Second }},
		{"unknown attribution", func(profile *Profile) { profile.Attribution = "invented" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			profile := explicitProfile()
			test.mutate(&profile)
			_, err := New(Config{}).Plan(PlanRequest{
				Scope: "org-a/project-a", Epoch: test.name, ExpectedCalls: 2, Profile: profile,
				Segments: []Segment{{Name: "prefix", Content: []byte("stable"), Tokens: 1024, Stable: true, Cacheable: true}},
			})
			if err == nil {
				t.Fatal("invalid profile accepted")
			}
		})
	}
}

func TestPlanCapsBreakpointsByHighestModeledValueThenRestoresOrder(t *testing.T) {
	profile := explicitProfile()
	profile.MaxBreakpoints = 2
	segments := make([]Segment, 5)
	for index := range segments {
		segments[index] = Segment{
			Name: string(rune('a' + index)), Content: []byte(strings.Repeat(string(rune('a'+index)), 2400)),
			Tokens: 600, Stable: true, Cacheable: true, ExpectedCalls: 10 - index,
		}
	}
	plan, err := New(Config{}).Plan(PlanRequest{
		Scope: "org-a/project-a", Epoch: "breakpoint-cap", ExpectedCalls: 10,
		Profile: profile, Segments: segments,
	})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if len(plan.Breakpoints) != 2 {
		t.Fatalf("breakpoints = %#v", plan.Breakpoints)
	}
	if plan.Breakpoints[0].index >= plan.Breakpoints[1].index {
		t.Fatalf("breakpoints lost provider order: %#v", plan.Breakpoints)
	}
}
