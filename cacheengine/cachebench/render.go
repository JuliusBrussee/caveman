package cachebench

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// Render formats concise human-readable benchmark result.
func Render(report Report) string {
	var out strings.Builder
	fmt.Fprintf(&out, "CACHEBENCH agent-cache evaluation: %s\n", strings.ToUpper(report.Status))
	fmt.Fprintf(&out, "Evidence: %s | publishable: %t | quality: %s\n", report.Basis, report.Publishable, report.QualityBasis)
	fmt.Fprintf(&out, "Target: request hits >= %s, eligible-token hits >= %s, >= %d eligible requests/provider\n", percent(report.Target.RequestHitRate), percent(report.Target.TokenHitRate), report.Target.MinEligibleRequest)
	fmt.Fprintf(&out, "Workload: %s | %d turns | compact every %d | %d static tokens | %s step\n", report.Scenario.Name, report.Scenario.Turns, report.Scenario.CompactionEvery, report.Scenario.StaticTokens, report.Scenario.Step)
	if report.Corpus != nil {
		fmt.Fprintf(&out, "Corpus: %s | %d %s | %d requests | turns %d/%d/%d/%d min/p50/p95/max | sha256 %s\n",
			report.Corpus.Name, report.Corpus.Sessions, plural(report.Corpus.Sessions, "session", "sessions"), report.Corpus.Requests,
			report.Corpus.MinTurns, report.Corpus.MedianTurns, report.Corpus.P95Turns, report.Corpus.MaxTurns,
			shortDigest(report.Corpus.SHA256))
		fmt.Fprintf(&out, "Within-session opportunity: request %.2f%% | estimated-token %.2f%% | cold-start ceiling %.2f%% | mutations/compactions %d\n",
			report.Corpus.OpportunityRequestHitRate*100, report.Corpus.OpportunityTokenHitRate*100,
			report.Corpus.ColdStartCeilingRequestHitRate*100, report.Corpus.MutatedOrCompactedTransitions)
	}
	fmt.Fprintln(&out)
	fmt.Fprintln(&out, "provider   mode       rolling  eligible  inelig  req-hit  token-hit  attributed  cold  invalid  safety  gate")
	for _, provider := range report.Providers {
		fmt.Fprintf(&out, "%-10s %-10s %-8t %8d  %6d  %7s  %9s  %10s  %4d  %7d  %6d  %s\n",
			provider.Provider, provider.Mode, provider.Rolling, provider.EligibleRequests, provider.IneligibleRequests,
			percent(provider.RequestHitRate), percent(provider.TokenHitRate), percent(provider.AttributedTokenHitRate),
			provider.ColdWrites, provider.InvalidSamples, provider.SafetyFailures, passWord(provider.GatePassed),
		)
	}
	fmt.Fprintf(&out, "\noverall: request-hit %s | token-hit %s | attributed %s | %s\n",
		percent(report.Overall.RequestHitRate), percent(report.Overall.TokenHitRate),
		percent(report.Overall.AttributedTokenHitRate), passWord(report.Overall.GatePassed),
	)
	if report.Overall.ReusableOpportunityRequests > 0 {
		fmt.Fprintf(&out, "reusable-opportunity capture: request %s | token %s (diagnostic, not gate)\n",
			percent(report.Overall.OpportunityRequestCaptureRate), percent(report.Overall.OpportunityTokenCaptureRate))
	}
	for _, provider := range report.Providers {
		for _, reason := range provider.BlockingReasons {
			fmt.Fprintf(&out, "block[%s]: %s\n", provider.Provider, reason)
		}
	}
	fmt.Fprintln(&out, "\nEvidence limits:")
	for _, limitation := range report.EvidenceLimitations {
		fmt.Fprintf(&out, "- %s\n", limitation)
	}
	return out.String()
}

// WriteJSON writes report, optionally omitting per-request detail.
func WriteJSON(writer io.Writer, report Report, includeRequests bool) error {
	if !includeRequests {
		providers := append([]ProviderReport(nil), report.Providers...)
		report.Providers = providers
		for index := range report.Providers {
			report.Providers[index].Requests = nil
		}
		report.Overall.Requests = nil
	}
	encoder := json.NewEncoder(writer)
	encoder.SetIndent("", "  ")
	return encoder.Encode(report)
}

func percent(rate float64) string { return fmt.Sprintf("%.2f%%", 100*rate) }

func shortDigest(value string) string {
	if len(value) <= 12 {
		return value
	}
	return value[:12]
}

func passWord(passed bool) string {
	if passed {
		return "PASS"
	}
	return "FAIL"
}

func plural(value int, singular, plural string) string {
	if value == 1 {
		return singular
	}
	return plural
}
