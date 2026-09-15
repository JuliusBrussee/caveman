package optimizers

import "testing"

// No optimizer ID may appear in more than one family — a member in two families
// would make dedup ambiguous and could double-count headroom. This is the
// "exactly one family" half of the invariant.
func TestNoIDInTwoFamilies(t *testing.T) {
	count := map[string]int{}
	for _, f := range Families {
		for _, id := range f.Members {
			count[id]++
		}
	}
	for id, n := range count {
		if n != 1 {
			t.Errorf("optimizer %q appears in %d families, want exactly 1", id, n)
		}
	}
}

// FamilyIndexOf and FamilyNameOf must round-trip for every member, and both must
// fail closed (index -1, name "") for an unknown ID.
func TestFamilyIndexNameRoundTrip(t *testing.T) {
	for i, f := range Families {
		if f.Name == "" {
			t.Errorf("family %d has empty Name", i)
		}
		if len(f.Members) == 0 {
			t.Errorf("family %q has no members", f.Name)
		}
		for _, id := range f.Members {
			if got := FamilyIndexOf(id); got != i {
				t.Errorf("FamilyIndexOf(%q) = %d, want %d", id, got, i)
			}
			if got := FamilyNameOf(id); got != f.Name {
				t.Errorf("FamilyNameOf(%q) = %q, want %q", id, got, f.Name)
			}
			if !MembersSet(f.Name)[id] {
				t.Errorf("MembersSet(%q) missing member %q", f.Name, id)
			}
		}
	}
	if got := FamilyIndexOf("totally-unknown-id"); got != -1 {
		t.Errorf("FamilyIndexOf(unknown) = %d, want -1", got)
	}
	if got := FamilyNameOf("totally-unknown-id"); got != "" {
		t.Errorf("FamilyNameOf(unknown) = %q, want empty", got)
	}
	if len(MembersSet("no-such-family")) != 0 {
		t.Errorf("MembersSet(unknown) must be empty")
	}
}

func TestRetiredOpportunityIDsPreserveHistoricalMutexIdentity(t *testing.T) {
	want := []string{
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
		"gemini-explicit-cache",
	}
	got := RetiredOpportunityIDs()
	if len(got) != len(want) {
		t.Fatalf("RetiredOpportunityIDs() len = %d, want %d", len(got), len(want))
	}
	for i, id := range want {
		if got[i] != id || !IsRetiredOpportunity(id) {
			t.Errorf("retired id[%d] = %q, want %q", i, got[i], id)
		}
		if id != "gemini-explicit-cache" && (FamilyNameOf(id) == "" || FamilyIndexOf(id) < 0) {
			t.Errorf("retired id %q lost historical mutex identity", id)
		}
	}
	// gemini-explicit-cache was a runtime-policy scaffold, never a money-bearing
	// opportunity. Retiring it must not fabricate a mutex identity it never had.
	if FamilyNameOf("gemini-explicit-cache") != "" || FamilyIndexOf("gemini-explicit-cache") != -1 {
		t.Fatal("retired Gemini runtime scaffold gained a new historical mutex identity")
	}
	got[0] = "mutated"
	if !IsRetiredOpportunity("retry-loop-reduction") {
		t.Fatal("caller mutated the closed retirement vocabulary")
	}
	if !IsRetiredOpportunity("cache-write-read-churn") {
		t.Fatal("cache-write-read-churn must remain a retired historical identity")
	}
	if IsRetiredOpportunity("totally-unknown-id") {
		t.Fatal("unknown optimizer classified as retired")
	}
}

func TestReportOnlyOpportunityBandsAreClosed(t *testing.T) {
	want := map[string][]string{
		ReportOnlyZeroBandMethod:                 {"cache-miss-root-cause", "compaction-roundtrip", "prompt-prefix-stability", "subagent-fanout-duplication"},
		SubagentRunReportOnlyZeroBandMethod:      {"subagent-run-concentration"},
		ToolErrorReportOnlyZeroBandMethod:        {"tool-error-streak-profile"},
		DuplicateStepReportOnlyBandMethod:        {"duplicate-step-profile"},
		ProviderErrorReportOnlyBandMethod:        {"provider-error-traffic-profile"},
		ContextWindowProfileReportOnlyBandMethod: {"context-window-profile"},
		ToolCatalogProfileReportOnlyBandMethod:   {"tool-catalog-profile"},
		ToolOutputProfileReportOnlyBandMethod:    {"tool-output-size-profile"},
		ExplorationProfileReportOnlyBandMethod:   {"exploration-load-profile"},
		QualityFeedbackReportOnlyBandMethod:      {"quality-feedback-profile"},
		RecoveryToolResultReportOnlyBandMethod:   {"recovery-tool-result-profile"},
		HTTPResendReportOnlyBandMethod:           {"http-resend-profile"},
		OTelAgentTopologyReportOnlyBandMethod:    {"otel-agent-topology-profile"},
	}
	for method, ids := range want {
		got := ReportOnlyOpportunityIDsForBand(method)
		if len(got) != len(ids) {
			t.Fatalf("%s ids = %v, want %v", method, got, ids)
		}
		for i := range ids {
			if got[i] != ids[i] {
				t.Errorf("%s ids = %v, want %v", method, got, ids)
			}
			if resolved, ok := ReportOnlyBandMethodForOpportunity(ids[i]); !ok || resolved != method {
				t.Errorf("ReportOnlyBandMethodForOpportunity(%q) = %q/%v, want %q/true", ids[i], resolved, ok, method)
			}
		}
	}
	if got := ReportOnlyOpportunityIDsForBand("unknown"); len(got) != 0 {
		t.Fatalf("unknown report-only band admitted ids: %v", got)
	}
	if method, ok := ReportOnlyBandMethodForOpportunity("unknown"); ok || method != "" {
		t.Fatalf("unknown opportunity resolved report band %q/%v", method, ok)
	}
	contracts := ReportOnlyOpportunityContracts()
	if len(contracts) != 16 {
		t.Fatalf("report-only contracts = %d, want 16", len(contracts))
	}
	for i, contract := range contracts {
		if contract.OpportunityID == "" || contract.EvidenceDetectorID == "" || contract.BandMethod == "" {
			t.Fatalf("incomplete report-only contract: %+v", contract)
		}
		if i > 0 && contracts[i-1].OpportunityID >= contract.OpportunityID {
			t.Fatalf("report-only contracts are not uniquely sorted: %q then %q", contracts[i-1].OpportunityID, contract.OpportunityID)
		}
		resolved, ok := ReportOnlyOpportunityContractFor(contract.OpportunityID)
		if !ok || resolved != contract {
			t.Errorf("ReportOnlyOpportunityContractFor(%q) = %+v/%v, want %+v/true", contract.OpportunityID, resolved, ok, contract)
		}
		if !IsReportProjectionOnlyOpportunity(contract.OpportunityID) {
			t.Errorf("current report-only id %q can enter Cave Plan", contract.OpportunityID)
		}
	}
	prefix, ok := ReportOnlyOpportunityContractFor("prompt-prefix-stability")
	if !ok || prefix.EvidenceDetectorID != "repeated-uncached-prefix" {
		t.Fatalf("stable prefix opportunity lost its detector identity bridge: %+v/%v", prefix, ok)
	}
	contracts[0].OpportunityID = "mutated"
	if _, ok := ReportOnlyOpportunityContractFor("mutated"); ok {
		t.Fatal("caller mutated the closed report-only vocabulary")
	}
	if IsReportProjectionOnlyOpportunity("unknown") {
		t.Fatal("unknown optimizer entered report projection")
	}
}

func TestEnablementOnlyOpportunityContractsAreClosedAndImmutable(t *testing.T) {
	want := []string{"count-baseline-permission", "unlabeled-traffic"}
	contracts := EnablementOnlyOpportunityContracts()
	if len(contracts) != len(want) {
		t.Fatalf("enablement contracts = %d, want %d", len(contracts), len(want))
	}
	for i, id := range want {
		contract := contracts[i]
		if contract.OpportunityID != id || contract.EvidenceDetectorID != id || contract.BandMethod != EnablementZeroBandMethod {
			t.Errorf("enablement contract[%d] = %+v, want exact %q/%q", i, contract, id, EnablementZeroBandMethod)
		}
		if !IsEnablementOnlyOpportunity(id) || !IsReviewOnlyOpportunity(id) || IsReportProjectionOnlyOpportunity(id) {
			t.Errorf("enablement classification drift for %q", id)
		}
		resolved, ok := EnablementOnlyOpportunityContractFor(id)
		if !ok || resolved != contract {
			t.Errorf("EnablementOnlyOpportunityContractFor(%q) = %+v/%v, want %+v/true", id, resolved, ok, contract)
		}
	}
	contracts[0].OpportunityID = "mutated"
	if IsEnablementOnlyOpportunity("mutated") {
		t.Fatal("caller mutated the closed enablement vocabulary")
	}
	for _, id := range []string{"unknown", "toon-reencoding"} {
		if IsEnablementOnlyOpportunity(id) || IsReviewOnlyOpportunity(id) {
			t.Errorf("%q entered the enablement/review-only vocabulary", id)
		}
	}
	if !IsReviewOnlyOpportunity("http-resend-profile") {
		t.Fatal("report-only profile escaped the common review-only boundary")
	}
}
