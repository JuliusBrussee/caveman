package cachebench

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"sort"
	"strings"

	"github.com/JuliusBrussee/caveman/cacheengine"
)

// NewObservationRecord binds one engine result to provider and task evidence.
func NewObservationRecord(requestID, provider, epoch string, eligibleInputTokens int, originalRequestBody, providerEvidence []byte, result cacheengine.NativeResult, verification TaskVerification, rawUsage []byte) ObservationRecord {
	cacheEligible := result.Decision == cacheengine.DecisionApply || result.Decision == cacheengine.DecisionObserveOnly
	return ObservationRecord{
		Schema: ObservationSchema, RequestID: requestID, RequestBodySHA256: bodyDigest(originalRequestBody),
		ProviderEvidenceSHA256: bodyDigest(providerEvidence),
		Provider:               provider, Epoch: epoch,
		EligibleInputTokens: eligibleInputTokens, CacheEligible: cacheEligible, Applied: result.Applied,
		EngineDecision: result.Decision, EngineReason: result.Reason, ProfileID: result.Profile.ID,
		Attribution: result.Profile.Attribution, OptimizerIDs: append([]string(nil), result.OptimizerIDs...),
		QualityPassed: verification.Passed, QualityVerifier: verification.Verifier,
		QualityEvidenceSHA256: bodyDigest(verification.Evidence), Usage: append(json.RawMessage(nil), rawUsage...),
	}
}

// EvaluateObservedAgainstTrace requires exact request population and body joins.
func EvaluateObservedAgainstTrace(records []ObservationRecord, trace []TraceRecord, target Target) (Report, error) {
	if err := validateObservationRecords(records); err != nil {
		return Report{}, err
	}
	if len(records) != len(trace) {
		return Report{}, fmt.Errorf("cachebench: observation population %d does not match trace population %d", len(records), len(trace))
	}
	byID := make(map[string]TraceRecord, len(trace))
	for index, request := range trace {
		if request.Schema != TraceSchema && request.Schema != TraceSchemaV2 || strings.TrimSpace(request.RequestID) == "" {
			return Report{}, fmt.Errorf("cachebench: trace request %d has invalid schema or request_id", index)
		}
		if _, exists := byID[request.RequestID]; exists {
			return Report{}, fmt.Errorf("cachebench: duplicate trace request %q", request.RequestID)
		}
		if strings.TrimSpace(request.Provider) == "" || strings.TrimSpace(request.Epoch) == "" || !validUniqueJSONObject(request.Body) || request.BodySHA256 == "" || request.BodySHA256 != bodyDigest(request.Body) {
			return Report{}, fmt.Errorf("cachebench: trace request %q has invalid identity, body, or digest", request.RequestID)
		}
		if request.StableSegmentCount < 0 || request.StableSegmentCount > len(request.Prefix) || !validPrefix(request.Prefix) {
			return Report{}, fmt.Errorf("cachebench: trace request %q has invalid prefix", request.RequestID)
		}
		byID[request.RequestID] = request
	}
	for _, record := range records {
		request, exists := byID[record.RequestID]
		if !exists {
			return Report{}, fmt.Errorf("cachebench: observation request %q absent from trace", record.RequestID)
		}
		if record.Provider != request.Provider || record.Epoch != request.Epoch {
			return Report{}, fmt.Errorf("cachebench: observation request %q provider/epoch mismatch", record.RequestID)
		}
		if record.RequestBodySHA256 == "" || record.RequestBodySHA256 != request.BodySHA256 {
			return Report{}, fmt.Errorf("cachebench: observation request %q body digest mismatch", record.RequestID)
		}
	}
	report, err := EvaluateObserved(records, target)
	if err != nil {
		return Report{}, err
	}
	report.EvidenceLimitations = append([]string{
		"observation population exactly joined to supplied request trace by request ID and body SHA-256",
	}, report.EvidenceLimitations...)
	return report, nil
}

// ObservationReadLimits bounds JSONL evidence decoding.
type ObservationReadLimits struct {
	MaxLineBytes int
	MaxRecords   int
}

// DefaultObservationReadLimits returns conservative retained-evidence limits.
func DefaultObservationReadLimits() ObservationReadLimits {
	return ObservationReadLimits{MaxLineBytes: 8 << 20, MaxRecords: 100_000}
}

// ReadObservationJSONL reads observation records under default limits.
func ReadObservationJSONL(reader io.Reader) ([]ObservationRecord, error) {
	return ReadObservationJSONLWithLimits(reader, DefaultObservationReadLimits())
}

// ReadObservationJSONLWithLimits reads strict observation JSONL under explicit limits.
func ReadObservationJSONLWithLimits(reader io.Reader, limits ObservationReadLimits) ([]ObservationRecord, error) {
	if limits.MaxLineBytes <= 0 || limits.MaxLineBytes > 64<<20 || limits.MaxRecords <= 0 || limits.MaxRecords > 1_000_000 {
		return nil, errors.New("cachebench: invalid observation read limits")
	}
	scanner := bufio.NewScanner(reader)
	initial := 64 * 1024
	if limits.MaxLineBytes < initial {
		initial = limits.MaxLineBytes
	}
	scanner.Buffer(make([]byte, initial), limits.MaxLineBytes)
	seen := map[string]bool{}
	var records []ObservationRecord
	for line := 1; scanner.Scan(); line++ {
		raw := bytes.TrimSpace(scanner.Bytes())
		if len(raw) == 0 {
			continue
		}
		if len(records) >= limits.MaxRecords {
			return nil, fmt.Errorf("cachebench: observations exceed record limit %d", limits.MaxRecords)
		}
		if !validUniqueJSONObject(raw) {
			return nil, fmt.Errorf("cachebench: observation line %d: duplicate or invalid JSON", line)
		}
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		var record ObservationRecord
		if err := decoder.Decode(&record); err != nil {
			return nil, fmt.Errorf("cachebench: observation line %d: %w", line, err)
		}
		var trailing any
		if err := decoder.Decode(&trailing); err != io.EOF {
			return nil, fmt.Errorf("cachebench: observation line %d: trailing JSON", line)
		}
		if record.Schema != ObservationSchema {
			return nil, fmt.Errorf("cachebench: observation line %d: schema %q", line, record.Schema)
		}
		if strings.TrimSpace(record.RequestID) == "" || seen[record.RequestID] {
			return nil, fmt.Errorf("cachebench: observation line %d: empty or duplicate request_id", line)
		}
		seen[record.RequestID] = true
		record.Usage = append(json.RawMessage(nil), record.Usage...)
		records = append(records, record)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("cachebench: read observations: %w", err)
	}
	if len(records) == 0 {
		return nil, errors.New("cachebench: no observation records")
	}
	if err := validateObservationRecords(records); err != nil {
		return nil, err
	}
	return records, nil
}

// EvaluateObserved evaluates supplied observations without completeness claim.
func EvaluateObserved(records []ObservationRecord, target Target) (Report, error) {
	if err := validateTarget(target); err != nil {
		return Report{}, err
	}
	if len(records) == 0 {
		return Report{}, errors.New("cachebench: no observation records")
	}
	if err := validateObservationRecords(records); err != nil {
		return Report{}, err
	}
	groups := map[string][]ObservationRecord{}
	for _, record := range records {
		provider := strings.ToLower(strings.TrimSpace(record.Provider))
		groups[provider] = append(groups[provider], record)
	}
	if len(groups) > 1024 {
		return Report{}, errors.New("cachebench: provider population exceeds 1024")
	}
	providers := make([]string, 0, len(groups))
	for provider := range groups {
		providers = append(providers, provider)
	}
	sort.Strings(providers)
	scenario := Scenario{Name: "provider-observed-agent-trace", Turns: len(records)}
	report := baseReport(BasisObserved, scenario, target, QualityTask)
	report.Scenario.TokenBasis = "provider-reported cache counters over caller-declared eligible input tokens"
	for _, provider := range providers {
		report.Providers = append(report.Providers, evaluateObservedProvider(provider, groups[provider], target))
	}
	report.Overall = aggregateProviders(report.Providers, target)
	if report.Overall.GatePassed {
		report.Status = "pass"
	}
	report.EvidenceLimitations = []string{
		"population completeness is not proven unless EvaluateObservedAgainstTrace is used",
		"provider counters prove reads and writes only for supplied records; evaluator binds but does not inspect retained response artifacts",
		"quality_passed is supplied by external task verifier; evaluator binds but does not inspect retained grader artifacts",
		"report does not prove omitted requests, tail completeness, production prevalence, invoice spend, or verified savings",
	}
	return report, nil
}

func validateObservationRecords(records []ObservationRecord) error {
	seen := make(map[string]bool, len(records))
	for index, record := range records {
		if record.Schema != ObservationSchema && record.Schema != ObservationSchemaV2 {
			return fmt.Errorf("cachebench: observation %d schema %q", index, record.Schema)
		}
		if !validBoundedText(record.RequestID, 512, false) || seen[record.RequestID] {
			return fmt.Errorf("cachebench: observation %d empty or duplicate request_id", index)
		}
		seen[record.RequestID] = true
		if !validBoundedText(record.Provider, 64, false) || !validBoundedText(record.Epoch, 1024, false) || !validBoundedText(record.QualityVerifier, 256, false) || !validBoundedText(record.ProfileID, 256, true) || !validBoundedText(record.EngineReason, 1024, true) || !validEvidenceSHA256(record.ProviderEvidenceSHA256) || !validEvidenceSHA256(record.QualityEvidenceSHA256) {
			return fmt.Errorf("cachebench: observation %d missing provider or quality evidence provenance", index)
		}
		if record.RequestBodySHA256 != "" && !validSHA256(record.RequestBodySHA256) {
			return fmt.Errorf("cachebench: observation %d has invalid request body digest", index)
		}
		optimizerIDs := map[string]bool{}
		for _, optimizerID := range record.OptimizerIDs {
			if !validBoundedText(optimizerID, 256, false) || optimizerIDs[optimizerID] {
				return fmt.Errorf("cachebench: observation %d has invalid optimizer identity", index)
			}
			optimizerIDs[optimizerID] = true
		}
	}
	return nil
}

func validSHA256(value string) bool {
	return len(value) == 64 && strings.Trim(value, "0123456789abcdef") == ""
}

func validEvidenceSHA256(value string) bool {
	return validSHA256(value) && value != bodyDigest(nil)
}

func evaluateObservedProvider(provider string, records []ObservationRecord, target Target) ProviderReport {
	report := ProviderReport{Provider: provider, Model: "observed", Rolling: true, EvaluatedRequests: len(records)}
	qualityPasses := 0
	for _, record := range records {
		result := RequestResult{RequestID: record.RequestID, Epoch: record.Epoch, Attribution: record.Attribution}
		cacheEligible := record.CacheEligible
		if record.Schema == ObservationSchemaV2 {
			cacheEligible = true
		}
		if record.QualityPassed {
			qualityPasses++
			result.Equivalent = true
		}
		if strings.TrimSpace(record.Provider) == "" || strings.TrimSpace(record.Epoch) == "" || record.EligibleInputTokens <= 0 {
			result.Error = "invalid observation identity or eligible token count"
			report.InvalidSamples++
			report.Requests = append(report.Requests, result)
			continue
		}
		if !cacheEligible {
			if record.Applied || record.EngineDecision != cacheengine.DecisionPassThrough || record.EngineReason != cacheengine.ReasonBelowMinimum {
				result.Error = "invalid ineligible engine decision"
				report.InvalidSamples++
			} else {
				report.IneligibleRequests++
			}
			report.Requests = append(report.Requests, result)
			continue
		}
		if record.Schema == ObservationSchema && (record.EngineDecision != cacheengine.DecisionApply && record.EngineDecision != cacheengine.DecisionObserveOnly || strings.TrimSpace(record.ProfileID) == "") {
			result.Error = "invalid eligible engine decision"
			report.InvalidSamples++
			report.Requests = append(report.Requests, result)
			continue
		}
		if !validObservedAttribution(provider, record) {
			result.Error = "attribution does not match provider optimizer evidence"
			report.InvalidSamples++
			report.Requests = append(report.Requests, result)
			continue
		}
		usage, ok := cacheengine.NormalizeRawCacheUsage(provider, record.Usage)
		if !ok || !usage.CacheObserved {
			result.Error = "cache usage unavailable or malformed"
			report.InvalidSamples++
			report.Requests = append(report.Requests, result)
			continue
		}
		if usage.CachedInputTokens > record.EligibleInputTokens || usage.CacheCreationInputTokens > record.EligibleInputTokens-usage.CachedInputTokens {
			result.Error = "provider cache counter exceeds eligible input tokens"
			report.InvalidSamples++
			report.Requests = append(report.Requests, result)
			continue
		}
		if int64(record.EligibleInputTokens) > math.MaxInt64-report.EligibleTokens {
			result.Error = "token metric overflow"
			report.InvalidSamples++
			report.Requests = append(report.Requests, result)
			continue
		}
		report.Attribution = record.Attribution
		report.EligibleRequests++
		result.Eligible = true
		report.EligibleTokens += int64(record.EligibleInputTokens)
		report.CacheReadTokens += int64(usage.CachedInputTokens)
		report.CacheWriteTokens += int64(usage.CacheCreationInputTokens)
		result.EligibleTokens = record.EligibleInputTokens
		result.CacheReadTokens = usage.CachedInputTokens
		result.CacheWriteTokens = usage.CacheCreationInputTokens
		result.Hit = usage.CachedInputTokens > 0
		result.ColdWrite = usage.CachedInputTokens == 0 && usage.CacheCreationInputTokens > 0
		if result.Hit {
			report.RequestHits++
		}
		if result.ColdWrite {
			report.ColdWrites++
		}
		if record.Applied && record.Attribution == cacheengine.AttributionCausal {
			report.AttributedReadTokens += int64(usage.CachedInputTokens)
		}
		report.Requests = append(report.Requests, result)
	}
	report.QualityPassRate = float64(qualityPasses) / float64(len(records))
	finalizeProvider(&report, target)
	return report
}

func validObservedAttribution(provider string, record ObservationRecord) bool {
	if record.Applied && len(record.OptimizerIDs) == 0 {
		return false
	}
	switch record.Attribution {
	case cacheengine.AttributionCausal:
		if !record.Applied {
			return false
		}
		switch provider {
		case "anthropic":
			return hasOptimizer(record.OptimizerIDs, cacheengine.AnthropicStableOptimizerID) || hasOptimizer(record.OptimizerIDs, cacheengine.AnthropicRollingOptimizerID)
		case "openai":
			return hasOptimizer(record.OptimizerIDs, cacheengine.OpenAIExplicitOptimizerID)
		case "bedrock":
			return hasOptimizer(record.OptimizerIDs, cacheengine.BedrockCacheOptimizerID) || hasOptimizer(record.OptimizerIDs, cacheengine.BedrockRollingOptimizerID)
		default:
			return false
		}
	case cacheengine.AttributionAffinity:
		return provider == "openai" && record.Applied && hasOptimizer(record.OptimizerIDs, cacheengine.OpenAIKeyOptimizerID)
	case cacheengine.AttributionOrganic:
		return !record.Applied
	case cacheengine.AttributionNone:
		return !record.Applied
	default:
		return false
	}
}

func hasOptimizer(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
