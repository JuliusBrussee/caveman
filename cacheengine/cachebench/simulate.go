package cachebench

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/JuliusBrussee/caveman/cacheengine"
)

type cachedPrefix struct {
	segments  []PrefixSegment
	expiresAt time.Time
}

type cachedPrefixGroup map[string]cachedPrefix

// RunSimulated evaluates deterministic agent workload without provider calls.
func RunSimulated(ctx context.Context, engine *cacheengine.Engine, providers []ProviderConfig, scenario Scenario, target Target) (Report, error) {
	if engine == nil {
		return Report{}, errors.New("cachebench: nil cache engine")
	}
	if err := validateTarget(target); err != nil {
		return Report{}, err
	}
	if len(providers) == 0 {
		return Report{}, errors.New("cachebench: no providers")
	}
	if len(providers) > 1024 {
		return Report{}, errors.New("cachebench: provider population exceeds 1024")
	}
	report := baseReport(BasisSimulated, scenario, target, QualityEquivalence)
	for _, provider := range providers {
		trace, err := GenerateTrace(provider, scenario)
		if err != nil {
			return Report{}, err
		}
		providerReport := evaluateSimulatedTrace(ctx, engine, trace, target)
		report.Providers = append(report.Providers, providerReport)
	}
	report.Overall = aggregateProviders(report.Providers, target)
	if report.Overall.GatePassed {
		report.Status = "pass"
	} else {
		report.Status = "fail"
	}
	report.EvidenceLimitations = []string{
		"deterministic provider-cache simulation; no provider request was sent",
		"declared fixture token counts are modeled, not provider-counted",
		"request equivalence proves cache metadata preserved prompt semantics; it does not prove task quality",
		"97% gate is benchmark evidence only, never production or verified savings",
	}
	return report, nil
}

// EvaluateTrace runs one caller-built trace, including custom Provider/Driver
// profiles, through the same cache and safety gate used by built-in scenarios.
func EvaluateTrace(ctx context.Context, engine *cacheengine.Engine, trace Trace, target Target) (ProviderReport, error) {
	if engine == nil {
		return ProviderReport{}, errors.New("cachebench: nil cache engine")
	}
	if err := validateTarget(target); err != nil {
		return ProviderReport{}, err
	}
	if len(trace.Requests) == 0 {
		return ProviderReport{}, errors.New("cachebench: empty trace")
	}
	if err := validateTrace(trace); err != nil {
		return ProviderReport{}, err
	}
	return evaluateSimulatedTrace(ctx, engine, trace, target), nil
}

func validateTrace(trace Trace) error {
	seen := make(map[string]bool, len(trace.Requests))
	var previous time.Time
	for index, request := range trace.Requests {
		if strings.TrimSpace(request.ID) == "" || seen[request.ID] {
			return fmt.Errorf("cachebench: trace request %d empty or duplicate ID", index)
		}
		seen[request.ID] = true
		if request.At.IsZero() || (!previous.IsZero() && request.At.Before(previous)) {
			return fmt.Errorf("cachebench: trace request %q has invalid time order", request.ID)
		}
		previous = request.At
		if len(request.Native.Body) == 0 || strings.TrimSpace(request.Native.Provider) == "" || strings.TrimSpace(request.Native.Model) == "" || strings.TrimSpace(request.Native.Epoch) == "" {
			return fmt.Errorf("cachebench: trace request %q has incomplete native request", request.ID)
		}
		if request.StableSegmentCount < 0 || request.StableSegmentCount > len(request.Prefix) || !validPrefix(request.Prefix) {
			return fmt.Errorf("cachebench: trace request %q has invalid prefix", request.ID)
		}
		if request.DeclaredInputTokens <= 0 || request.DeclaredInputTokens < request.Native.PrefixTokens || request.MaxOutputTokens <= 0 {
			return fmt.Errorf("cachebench: trace request %q has invalid billed-token budget", request.ID)
		}
	}
	return nil
}

func validPrefix(prefix []PrefixSegment) bool {
	if len(prefix) == 0 {
		return false
	}
	total := 0
	for _, segment := range prefix {
		if !validBoundedText(segment.ID, 1024, false) || segment.Tokens <= 0 || segment.Tokens > math.MaxInt-total {
			return false
		}
		total += segment.Tokens
	}
	return true
}

func evaluateSimulatedTrace(ctx context.Context, engine *cacheengine.Engine, trace Trace, target Target) ProviderReport {
	report := ProviderReport{Provider: trace.Provider.Provider, Model: trace.Provider.Model, EvaluatedRequests: len(trace.Requests)}
	states := map[string]cachedPrefixGroup{}
	opportunityStates := map[string]cachedPrefixGroup{}
	qualityPasses := 0
	for _, request := range trace.Requests {
		requestResult := RequestResult{RequestID: request.ID, Epoch: request.Native.Epoch}
		optimized, err := engine.Optimize(ctx, request.Native)
		if err != nil {
			requestResult.Error = err.Error()
			report.InvalidSamples++
			report.Requests = append(report.Requests, requestResult)
			continue
		}
		report.Mode = optimized.Profile.Mode
		report.Attribution = optimized.Profile.Attribution
		report.Rolling = optimized.Profile.Rolling
		requestResult.Decision = optimized.Decision
		requestResult.Reason = optimized.Reason
		requestResult.Attribution = optimized.Profile.Attribution
		if optimized.Applied {
			requestResult.Equivalent = ModelVisibleEquivalent(request.Native.Body, optimized.Body)
		} else {
			requestResult.Equivalent = bytes.Equal(request.Native.Body, optimized.Body)
		}
		if requestResult.Equivalent {
			qualityPasses++
		} else {
			report.SafetyFailures++
		}
		if optimized.Decision != cacheengine.DecisionApply && optimized.Decision != cacheengine.DecisionObserveOnly {
			if optimized.Reason == cacheengine.ReasonBelowMinimum {
				report.IneligibleRequests++
				report.Requests = append(report.Requests, requestResult)
				continue
			}
			requestResult.Error = "cache engine did not produce cacheable request"
			report.InvalidSamples++
			report.Requests = append(report.Requests, requestResult)
			continue
		}
		prefix := request.Prefix
		if !optimized.Profile.Rolling {
			if request.StableSegmentCount < 0 || request.StableSegmentCount > len(prefix) {
				requestResult.Error = "invalid stable segment boundary"
				report.InvalidSamples++
				report.Requests = append(report.Requests, requestResult)
				continue
			}
			prefix = prefix[:request.StableSegmentCount]
		}
		eligible := prefixTokens(prefix)
		requestResult.EligibleTokens = eligible
		if eligible < optimized.Profile.MinPrefixTokens {
			report.IneligibleRequests++
			report.Requests = append(report.Requests, requestResult)
			continue
		}
		requestResult.Eligible = true
		if int64(eligible) > math.MaxInt64-report.EligibleTokens {
			requestResult.Error = "token metric overflow"
			report.InvalidSamples++
			report.Requests = append(report.Requests, requestResult)
			continue
		}
		report.EligibleRequests++
		report.EligibleTokens += int64(eligible)
		lookupPrefix := simulatedLookupPrefix(request, optimized, prefix)
		stateKey := simulatedStateKey(trace, request, optimized)
		opportunityGroup := opportunityStates[stateKey]
		if opportunityGroup == nil {
			opportunityGroup = cachedPrefixGroup{}
			opportunityStates[stateKey] = opportunityGroup
		}
		possibleRead := longestCommonPrefix(opportunityGroup, lookupPrefix, optimized.Profile.MinPrefixTokens, false, request.At)
		if possibleRead > eligible {
			possibleRead = eligible
		}
		if possibleRead > 0 {
			report.ReusableOpportunityRequests++
			report.ReusableOpportunityTokens += int64(possibleRead)
		}
		group := states[stateKey]
		if group == nil {
			group = cachedPrefixGroup{}
			states[stateKey] = group
		}
		read := longestCommonPrefix(group, lookupPrefix, optimized.Profile.MinPrefixTokens, true, request.At)
		warm := len(group) > 0
		if read > eligible {
			read = eligible
		}
		requestResult.CacheReadTokens = read
		requestResult.CacheWriteTokens = eligible - read
		requestResult.Hit = read > 0
		requestResult.ColdWrite = read == 0
		requestResult.Invalidated = warm && read == 0
		if requestResult.Hit {
			report.RequestHits++
		}
		if requestResult.ColdWrite {
			report.ColdWrites++
		}
		if requestResult.Invalidated {
			report.Invalidations++
		}
		report.CacheReadTokens += int64(requestResult.CacheReadTokens)
		report.CacheWriteTokens += int64(requestResult.CacheWriteTokens)
		if optimized.Applied && optimized.Profile.Attribution == cacheengine.AttributionCausal {
			report.AttributedReadTokens += int64(requestResult.CacheReadTokens)
		}
		ttl := optimized.Profile.TTL
		if ttl <= 0 {
			ttl = trace.Scenario.AssumedTTL
		}
		group[request.Native.Epoch] = cachedPrefix{segments: append([]PrefixSegment(nil), lookupPrefix...), expiresAt: request.At.Add(ttl)}
		opportunityGroup[request.Native.Epoch] = cachedPrefix{segments: append([]PrefixSegment(nil), lookupPrefix...)}
		report.Requests = append(report.Requests, requestResult)
	}
	if len(trace.Requests) > 0 {
		report.QualityPassRate = float64(qualityPasses) / float64(len(trace.Requests))
	}
	finalizeProvider(&report, target)
	return report
}

func longestCommonPrefix(group cachedPrefixGroup, prefix []PrefixSegment, minimum int, expire bool, now time.Time) int {
	longest := 0
	for epoch, state := range group {
		if expire && now.After(state.expiresAt) {
			delete(group, epoch)
			continue
		}
		candidate := commonPrefixTokens(state.segments, prefix)
		if candidate >= minimum && candidate > longest {
			longest = candidate
		}
	}
	return longest
}

func simulatedLookupPrefix(request TraceRequest, result cacheengine.NativeResult, prefix []PrefixSegment) []PrefixSegment {
	if !strings.EqualFold(request.Native.Provider, "openai") || result.Profile.Mode != cacheengine.ModeExplicit {
		return prefix
	}
	var root map[string]any
	if json.Unmarshal(result.Body, &root) != nil {
		return nil
	}
	sequenceName := "messages"
	if strings.Contains(strings.ToLower(request.Native.Endpoint), "responses") {
		sequenceName = "input"
	}
	items, ok := root[sequenceName].([]any)
	if !ok || len(items) == 0 {
		return nil
	}
	leadingStable := 0
	boundary := -1
	for index, raw := range items {
		item, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		role, _ := item["role"].(string)
		if index == leadingStable && (role == "system" || role == "developer") {
			leadingStable++
		}
		if containsCacheBreakpoint(item) {
			boundary = index
		}
	}
	if boundary < 0 && result.Reason == cacheengine.ReasonAffinityFallback {
		for index := len(items) - 1; index >= 0; index-- {
			item, _ := items[index].(map[string]any)
			role, _ := item["role"].(string)
			if role == "user" || role == "tool" {
				boundary = index
				break
			}
		}
	}
	if boundary < 0 {
		return nil
	}
	offset := request.StableSegmentCount - leadingStable
	if offset < 0 {
		offset = 0
	}
	segmentCount := offset + boundary + 1
	if segmentCount > len(prefix) {
		segmentCount = len(prefix)
	}
	return prefix[:segmentCount]
}

func containsCacheBreakpoint(value any) bool {
	switch node := value.(type) {
	case map[string]any:
		for key, child := range node {
			if key == "prompt_cache_breakpoint" {
				return true
			}
			if containsCacheBreakpoint(child) {
				return true
			}
		}
	case []any:
		for _, child := range node {
			if containsCacheBreakpoint(child) {
				return true
			}
		}
	}
	return false
}

func simulatedStateKey(trace Trace, request TraceRequest, result cacheengine.NativeResult) string {
	key := strings.Join([]string{request.Native.Provider, request.Native.Model, request.Native.Scope}, "\x00")
	// Public corpora usually provide per-session gaps but no global timeline.
	// Never infer that unrelated sessions overlapped inside provider TTL.
	if !trace.AssumeCrossPartitionReuse {
		partition := request.Native.PartitionKey
		if partition == "" {
			partition = request.Native.Epoch
		}
		key += "\x00partition=" + partition
	}
	if result.Plan.RoutingKey != "" {
		key += "\x00" + result.Plan.RoutingKey
	}
	return key
}

func prefixTokens(segments []PrefixSegment) int {
	total := 0
	for _, segment := range segments {
		if segment.Tokens > 0 {
			total += segment.Tokens
		}
	}
	return total
}

func commonPrefixTokens(left, right []PrefixSegment) int {
	limit := len(left)
	if len(right) < limit {
		limit = len(right)
	}
	total := 0
	for index := 0; index < limit; index++ {
		if left[index] != right[index] {
			break
		}
		total += left[index].Tokens
	}
	return total
}

func validateTarget(target Target) error {
	if math.IsNaN(target.RequestHitRate) || math.IsInf(target.RequestHitRate, 0) || math.IsNaN(target.TokenHitRate) || math.IsInf(target.TokenHitRate, 0) || target.RequestHitRate < 0 || target.RequestHitRate > 1 || target.TokenHitRate < 0 || target.TokenHitRate > 1 || target.MinEligibleRequest <= 0 || target.MinEligibleRequest > 1_000_000 {
		return errors.New("cachebench: invalid target")
	}
	return nil
}

func finalizeProvider(report *ProviderReport, target Target) {
	if report.EligibleRequests > 0 {
		report.RequestHitRate = float64(report.RequestHits) / float64(report.EligibleRequests)
	}
	if report.EligibleTokens > 0 {
		report.TokenHitRate = float64(report.CacheReadTokens) / float64(report.EligibleTokens)
		report.AttributedTokenHitRate = float64(report.AttributedReadTokens) / float64(report.EligibleTokens)
	}
	if report.ReusableOpportunityRequests > 0 {
		report.OpportunityRequestCaptureRate = float64(report.RequestHits) / float64(report.ReusableOpportunityRequests)
	}
	if report.ReusableOpportunityTokens > 0 {
		report.OpportunityTokenCaptureRate = float64(report.CacheReadTokens) / float64(report.ReusableOpportunityTokens)
	}
	if report.EligibleRequests < target.MinEligibleRequest {
		report.BlockingReasons = append(report.BlockingReasons, fmt.Sprintf("eligible requests %d below minimum %d", report.EligibleRequests, target.MinEligibleRequest))
	}
	if report.RequestHitRate < target.RequestHitRate {
		report.BlockingReasons = append(report.BlockingReasons, fmt.Sprintf("request hit rate %.4f below target %.4f", report.RequestHitRate, target.RequestHitRate))
	}
	if report.TokenHitRate < target.TokenHitRate {
		report.BlockingReasons = append(report.BlockingReasons, fmt.Sprintf("token hit rate %.4f below target %.4f", report.TokenHitRate, target.TokenHitRate))
	}
	if report.QualityPassRate < 1 {
		report.BlockingReasons = append(report.BlockingReasons, fmt.Sprintf("quality pass rate %.4f below required 1.0000", report.QualityPassRate))
	}
	if report.SafetyFailures > 0 {
		report.BlockingReasons = append(report.BlockingReasons, fmt.Sprintf("%d model-visible equivalence failures", report.SafetyFailures))
	}
	if report.InvalidSamples > 0 {
		report.BlockingReasons = append(report.BlockingReasons, fmt.Sprintf("%d invalid samples", report.InvalidSamples))
	}
	report.GatePassed = len(report.BlockingReasons) == 0
}

func aggregateProviders(providers []ProviderReport, target Target) ProviderReport {
	overall := ProviderReport{Provider: "all", Model: "mixed", Rolling: true}
	qualityWeighted := 0.0
	qualitySamples := 0
	for _, provider := range providers {
		overall.EvaluatedRequests += provider.EvaluatedRequests
		overall.EligibleRequests += provider.EligibleRequests
		overall.IneligibleRequests += provider.IneligibleRequests
		overall.RequestHits += provider.RequestHits
		overall.ColdWrites += provider.ColdWrites
		overall.Invalidations += provider.Invalidations
		if provider.EligibleTokens > math.MaxInt64-overall.EligibleTokens {
			overall.InvalidSamples++
		} else {
			overall.EligibleTokens += provider.EligibleTokens
			overall.CacheReadTokens += provider.CacheReadTokens
			overall.CacheWriteTokens += provider.CacheWriteTokens
			overall.AttributedReadTokens += provider.AttributedReadTokens
		}
		overall.ReusableOpportunityRequests += provider.ReusableOpportunityRequests
		if provider.ReusableOpportunityTokens <= math.MaxInt64-overall.ReusableOpportunityTokens {
			overall.ReusableOpportunityTokens += provider.ReusableOpportunityTokens
		} else {
			overall.InvalidSamples++
		}
		overall.SafetyFailures += provider.SafetyFailures
		overall.InvalidSamples += provider.InvalidSamples
		qualityWeighted += provider.QualityPassRate * float64(provider.EvaluatedRequests)
		qualitySamples += provider.EvaluatedRequests
		if !provider.Rolling {
			overall.Rolling = false
		}
		if !provider.GatePassed {
			overall.BlockingReasons = append(overall.BlockingReasons, provider.Provider+" provider gate failed")
		}
	}
	if qualitySamples > 0 {
		overall.QualityPassRate = qualityWeighted / float64(qualitySamples)
	}
	providerFailures := append([]string(nil), overall.BlockingReasons...)
	overall.BlockingReasons = nil
	finalizeProvider(&overall, Target{
		RequestHitRate: target.RequestHitRate, TokenHitRate: target.TokenHitRate,
		MinEligibleRequest: target.MinEligibleRequest * len(providers),
	})
	overall.BlockingReasons = append(providerFailures, overall.BlockingReasons...)
	overall.GatePassed = len(overall.BlockingReasons) == 0
	return overall
}

func baseReport(basis string, scenario Scenario, target Target, qualityBasis string) Report {
	return Report{
		Schema: Schema, Basis: basis, Status: "fail", Publishable: false,
		GeneratedAt: time.Now().UTC().Format(time.RFC3339), Target: target, QualityBasis: qualityBasis,
		Scenario: ScenarioSummary{
			Name: scenario.Name, Turns: scenario.Turns, CompactionEvery: scenario.CompactionEvery,
			StaticTokens: scenario.StaticTokens, UserTokens: scenario.UserTokens,
			AssistantTokens: scenario.AssistantTokens, ToolResultTokens: scenario.ToolResultTokens,
			SummaryTokens: scenario.SummaryTokens, Step: scenario.Step.String(),
			AssumedTTL: scenario.AssumedTTL.String(),
			TokenBasis: "deterministic declared fixture tokens",
		},
	}
}
