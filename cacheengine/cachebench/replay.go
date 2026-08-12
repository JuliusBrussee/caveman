package cachebench

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/JuliusBrussee/caveman/cacheengine"
)

const (
	ReplayEvidenceSchema = "caveman.cachebench.replay-evidence.v1"
	ReplaySummarySchema  = "caveman.cachebench.replay-summary.v1"
	VerificationSchema   = "caveman.cachebench.verification.v1"
)

// VerificationCommandInput is JSON request sent to external task grader.
type VerificationCommandInput struct {
	Schema           string          `json:"schema"`
	RequestID        string          `json:"request_id"`
	Provider         string          `json:"provider"`
	Model            string          `json:"model"`
	TraceBodySHA256  string          `json:"trace_body_sha256"`
	WireBodySHA256   string          `json:"wire_body_sha256"`
	OriginalRequest  json.RawMessage `json:"original_request"`
	OptimizedRequest json.RawMessage `json:"optimized_request"`
	ProviderResponse json.RawMessage `json:"provider_response"`
}

// VerificationCommandOutput is strict external task-grader response.
type VerificationCommandOutput struct {
	Schema    string          `json:"schema"`
	RequestID string          `json:"request_id"`
	Passed    bool            `json:"passed"`
	Verifier  string          `json:"verifier"`
	Evidence  json.RawMessage `json:"evidence"`
}

// ParseVerificationCommandOutput validates and binds external grader evidence.
func ParseVerificationCommandOutput(raw []byte, requestID string) (TaskVerification, error) {
	raw = bytes.TrimSpace(raw)
	if !validUniqueJSONObject(raw) {
		return TaskVerification{}, errors.New("cachebench: verifier returned duplicate or invalid JSON")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var result VerificationCommandOutput
	if decoder.Decode(&result) != nil {
		return TaskVerification{}, errors.New("cachebench: verifier returned invalid JSON")
	}
	var trailing any
	if decoder.Decode(&trailing) != io.EOF || result.Schema != VerificationSchema || result.RequestID != requestID || !validBoundedText(result.Verifier, 256, false) || len(result.Evidence) == 0 || !json.Valid(result.Evidence) || bytes.Equal(bytes.TrimSpace(result.Evidence), []byte("null")) {
		return TaskVerification{}, errors.New("cachebench: verifier returned incomplete or mismatched evidence")
	}
	return TaskVerification{Passed: result.Passed, Verifier: result.Verifier, Evidence: append([]byte(nil), raw...)}, nil
}

// ReplayLimits bounds paid population, schedule, and concurrency.
type ReplayLimits struct {
	MaxRequests             int
	MaxDeclaredBilledTokens int64
	MaxGap                  time.Duration
	MaxScheduleDrift        time.Duration
	MaxConcurrency          int
	RequireGroundedTiming   bool
	RequireProviderTokens   bool
}

// ReplayPreflight is zero-network validation and declared-budget summary.
type ReplayPreflight struct {
	Requests                          int      `json:"requests"`
	DeclaredInputTokens               int64    `json:"declared_total_input_tokens"`
	DeclaredMaxOutputTokens           int64    `json:"declared_max_output_tokens"`
	DeclaredBilledTokens              int64    `json:"declared_billed_token_ceiling"`
	ScheduledDuration                 string   `json:"scheduled_duration"`
	TimingBases                       []string `json:"timing_bases"`
	TokenBases                        []string `json:"token_bases"`
	TimingGrounded                    bool     `json:"timing_grounded"`
	InputBudgetClaimedProviderCounted bool     `json:"input_budget_claimed_provider_counted"`
	MaxConcurrency                    int      `json:"max_concurrency"`
}

// ReplayRunError binds a runtime failure to its trace request and stable failure
// code. Callers should use errors.As instead of parsing Error text.
type ReplayRunError struct {
	RequestID   string
	FailureCode string
	Err         error
}

func (err *ReplayRunError) Error() string {
	if err == nil || err.Err == nil {
		return "cachebench: replay failed"
	}
	return err.Err.Error()
}

func (err *ReplayRunError) Unwrap() error {
	if err == nil {
		return nil
	}
	return err.Err
}

// ReplayOutbound is exact provider request passed to transport.
type ReplayOutbound struct {
	RequestID string
	Provider  string
	Model     string
	Region    string
	Endpoint  string
	Body      []byte
}

// ReplayResponse is retained non-streaming provider result.
type ReplayResponse struct {
	StatusCode        int
	Body              []byte
	ProviderRequestID string
}

// ReplayTransport sends one provider request without automatic retry.
type ReplayTransport interface {
	Send(context.Context, ReplayOutbound) (ReplayResponse, error)
}

// ReplayTransportFunc adapts a function to ReplayTransport.
type ReplayTransportFunc func(context.Context, ReplayOutbound) (ReplayResponse, error)

// Send calls wrapped transport function.
func (fn ReplayTransportFunc) Send(ctx context.Context, request ReplayOutbound) (ReplayResponse, error) {
	return fn(ctx, request)
}

// ReplayVerificationInput binds trace, optimized body, and provider response.
type ReplayVerificationInput struct {
	Trace     TraceRecord
	Optimized cacheengine.NativeResult
	Response  ReplayResponse
}

// ReplayVerifier grades task outcome for one retained provider response.
type ReplayVerifier interface {
	Verify(context.Context, ReplayVerificationInput) (TaskVerification, error)
}

// ReplayVerifierFunc adapts a function to ReplayVerifier.
type ReplayVerifierFunc func(context.Context, ReplayVerificationInput) (TaskVerification, error)

// Verify calls wrapped verifier function.
func (fn ReplayVerifierFunc) Verify(ctx context.Context, input ReplayVerificationInput) (TaskVerification, error) {
	return fn(ctx, input)
}

// ReplayEvidenceRecord is hash-bound per-request live replay evidence.
type ReplayEvidenceRecord struct {
	Schema                        string                  `json:"schema"`
	RequestID                     string                  `json:"request_id"`
	TraceBodySHA256               string                  `json:"trace_body_sha256"`
	WireBodySHA256                string                  `json:"wire_body_sha256"`
	Provider                      string                  `json:"provider"`
	Model                         string                  `json:"model"`
	Epoch                         string                  `json:"epoch"`
	TimingBasis                   string                  `json:"timing_basis"`
	TokenBasis                    string                  `json:"token_basis"`
	TimeScale                     float64                 `json:"time_scale"`
	TimingFaithful                bool                    `json:"timing_faithful"`
	ScheduledAt                   string                  `json:"scheduled_at"`
	ScheduleDriftMilliseconds     int64                   `json:"schedule_drift_ms"`
	ScheduleToleranceMilliseconds int64                   `json:"schedule_tolerance_ms"`
	StartedAt                     string                  `json:"started_at"`
	CompletedAt                   string                  `json:"completed_at"`
	LatencyMilliseconds           int64                   `json:"latency_ms"`
	HTTPStatus                    int                     `json:"http_status,omitempty"`
	ProviderRequestID             string                  `json:"provider_request_id,omitempty"`
	ProviderEvidenceSHA256        string                  `json:"provider_evidence_sha256,omitempty"`
	ProviderUsageSHA256           string                  `json:"provider_usage_sha256,omitempty"`
	ProviderTotalInputTokens      int                     `json:"provider_total_input_tokens,omitempty"`
	ProviderOutputTokens          int                     `json:"provider_output_tokens,omitempty"`
	Applied                       bool                    `json:"applied"`
	Decision                      cacheengine.Decision    `json:"decision"`
	Reason                        string                  `json:"reason"`
	Attribution                   cacheengine.Attribution `json:"attribution"`
	OptimizerIDs                  []string                `json:"optimizer_ids,omitempty"`
	QualityPassed                 bool                    `json:"quality_passed"`
	QualityVerifier               string                  `json:"quality_verifier,omitempty"`
	QualityEvidenceSHA256         string                  `json:"quality_evidence_sha256,omitempty"`
	Success                       bool                    `json:"success"`
	FailureCode                   string                  `json:"failure_code,omitempty"`
}

// ReplayResult contains validated evidence and retained success artifacts.
type ReplayResult struct {
	Evidence             ReplayEvidenceRecord
	Observation          *ObservationRecord
	ProviderResponse     []byte
	VerificationEvidence []byte
}

// LatencyDistribution contains nearest-rank replay latency percentiles.
type LatencyDistribution struct {
	Samples int   `json:"samples"`
	P50MS   int64 `json:"p50_ms"`
	P95MS   int64 `json:"p95_ms"`
	P99MS   int64 `json:"p99_ms"`
	MaxMS   int64 `json:"max_ms"`
}

// ProviderReplaySummary aggregates one provider's retained replay population.
type ProviderReplaySummary struct {
	Provider      string              `json:"provider"`
	Requests      int                 `json:"requests"`
	Successful    int                 `json:"successful"`
	Failed        int                 `json:"failed"`
	QualityPassed int                 `json:"quality_passed"`
	InputTokens   int64               `json:"provider_total_input_tokens"`
	OutputTokens  int64               `json:"provider_output_tokens"`
	Latency       LatencyDistribution `json:"latency"`
}

// ReplayEvidenceSummary aggregates exact validated evidence population.
type ReplayEvidenceSummary struct {
	Schema                            string                  `json:"schema"`
	Requests                          int                     `json:"requests"`
	Successful                        int                     `json:"successful"`
	Failed                            int                     `json:"failed"`
	QualityPassed                     int                     `json:"quality_passed"`
	InputTokens                       int64                   `json:"provider_total_input_tokens"`
	OutputTokens                      int64                   `json:"provider_output_tokens"`
	TimingFaithful                    bool                    `json:"timing_faithful"`
	InputBudgetClaimedProviderCounted bool                    `json:"input_budget_claimed_provider_counted"`
	Latency                           LatencyDistribution     `json:"latency"`
	Providers                         []ProviderReplaySummary `json:"providers"`
}

// SummarizeReplayEvidence validates and aggregates replay records.
func SummarizeReplayEvidence(records []ReplayEvidenceRecord) (ReplayEvidenceSummary, error) {
	if len(records) == 0 {
		return ReplayEvidenceSummary{}, errors.New("cachebench: no replay evidence")
	}
	summary := ReplayEvidenceSummary{
		Schema: ReplaySummarySchema, Requests: len(records),
		TimingFaithful: true, InputBudgetClaimedProviderCounted: true,
	}
	type accumulator struct {
		summary   ProviderReplaySummary
		latencies []int64
	}
	groups := map[string]*accumulator{}
	seen := map[string]bool{}
	latencies := make([]int64, 0, len(records))
	for index, record := range records {
		if err := validateReplayEvidence(record); err != nil {
			return ReplayEvidenceSummary{}, fmt.Errorf("cachebench: replay evidence %d: %w", index, err)
		}
		if seen[record.RequestID] {
			return ReplayEvidenceSummary{}, fmt.Errorf("cachebench: duplicate replay evidence request %q", record.RequestID)
		}
		seen[record.RequestID] = true
		group := groups[record.Provider]
		if group == nil {
			group = &accumulator{summary: ProviderReplaySummary{Provider: record.Provider}}
			groups[record.Provider] = group
		}
		group.summary.Requests++
		if record.Success {
			summary.Successful++
			group.summary.Successful++
			if record.QualityPassed {
				summary.QualityPassed++
				group.summary.QualityPassed++
			}
		} else {
			summary.Failed++
			group.summary.Failed++
		}
		if !record.TimingFaithful {
			summary.TimingFaithful = false
		}
		if record.TokenBasis != TokenProviderCounted {
			summary.InputBudgetClaimedProviderCounted = false
		}
		if record.ProviderUsageSHA256 != "" {
			input, output := int64(record.ProviderTotalInputTokens), int64(record.ProviderOutputTokens)
			if input > math.MaxInt64-summary.InputTokens || output > math.MaxInt64-summary.OutputTokens || input > math.MaxInt64-group.summary.InputTokens || output > math.MaxInt64-group.summary.OutputTokens {
				return ReplayEvidenceSummary{}, errors.New("cachebench: replay token summary overflow")
			}
			summary.InputTokens += input
			summary.OutputTokens += output
			group.summary.InputTokens += input
			group.summary.OutputTokens += output
		}
		if record.HTTPStatus > 0 {
			latencies = append(latencies, record.LatencyMilliseconds)
			group.latencies = append(group.latencies, record.LatencyMilliseconds)
		}
	}
	summary.Latency = summarizeLatency(latencies)
	providers := make([]string, 0, len(groups))
	for provider := range groups {
		providers = append(providers, provider)
	}
	sort.Strings(providers)
	for _, provider := range providers {
		group := groups[provider]
		group.summary.Latency = summarizeLatency(group.latencies)
		summary.Providers = append(summary.Providers, group.summary)
	}
	return summary, nil
}

func summarizeLatency(values []int64) LatencyDistribution {
	if len(values) == 0 {
		return LatencyDistribution{}
	}
	sorted := append([]int64(nil), values...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	percentile := func(value float64) int64 {
		index := int(math.Ceil(value*float64(len(sorted)))) - 1
		if index < 0 {
			index = 0
		}
		return sorted[index]
	}
	return LatencyDistribution{
		Samples: len(sorted), P50MS: percentile(.50), P95MS: percentile(.95),
		P99MS: percentile(.99), MaxMS: sorted[len(sorted)-1],
	}
}

// ReplayRunner performs prevalidated absolute-time live replay.
type ReplayRunner struct {
	Engine    *cacheengine.Engine
	Transport ReplayTransport
	Verifier  ReplayVerifier
	Limits    ReplayLimits
	// Target enables a post-engine eligible-population gate before any provider
	// call. Nil leaves target evaluation to caller.
	Target    *Target
	TimeScale float64
	Now       func() time.Time
	Sleep     func(context.Context, time.Duration) error
}

// ValidateReplay performs zero-network trace, schedule, and budget preflight.
func ValidateReplay(records []TraceRecord, limits ReplayLimits, timeScale float64) (ReplayPreflight, error) {
	if len(records) == 0 {
		return ReplayPreflight{}, errors.New("cachebench: replay trace is empty")
	}
	if limits.MaxRequests <= 0 || limits.MaxDeclaredBilledTokens <= 0 || limits.MaxGap <= 0 || limits.MaxScheduleDrift <= 0 || limits.MaxConcurrency <= 0 || limits.MaxConcurrency > 1024 {
		return ReplayPreflight{}, errors.New("cachebench: positive replay request, token, gap, schedule-drift, and concurrency limits required; concurrency cannot exceed 1024")
	}
	if len(records) > limits.MaxRequests {
		return ReplayPreflight{}, fmt.Errorf("cachebench: replay population %d exceeds request limit %d", len(records), limits.MaxRequests)
	}
	if timeScale <= 0 || math.IsNaN(timeScale) || math.IsInf(timeScale, 0) || timeScale > 1000 {
		return ReplayPreflight{}, errors.New("cachebench: time scale must be greater than zero and at most 1000")
	}
	seen := make(map[string]bool, len(records))
	timing := map[string]bool{}
	tokens := map[string]bool{}
	var previous time.Time
	var declaredInput, declaredOutput, worstCase, scheduled int64
	for index, record := range records {
		if seen[record.RequestID] {
			return ReplayPreflight{}, fmt.Errorf("cachebench: duplicate replay request %q", record.RequestID)
		}
		seen[record.RequestID] = true
		if record.Schema != TraceSchema {
			return ReplayPreflight{}, fmt.Errorf("cachebench: replay request %q needs %s billed-token metadata", record.RequestID, TraceSchema)
		}
		if _, err := record.NativeRequest(); err != nil {
			return ReplayPreflight{}, fmt.Errorf("cachebench: replay request %d: %w", index, err)
		}
		at, err := time.Parse(time.RFC3339Nano, record.At)
		if err != nil || !previous.IsZero() && at.Before(previous) {
			return ReplayPreflight{}, fmt.Errorf("cachebench: replay request %q has invalid time order", record.RequestID)
		}
		if limits.RequireGroundedTiming && record.TimingBasis != TimingGrounded {
			return ReplayPreflight{}, fmt.Errorf("cachebench: replay request %q timing basis %q is not globally grounded", record.RequestID, record.TimingBasis)
		}
		timing[record.TimingBasis] = true
		if limits.RequireProviderTokens && record.TokenBasis != TokenProviderCounted {
			return ReplayPreflight{}, fmt.Errorf("cachebench: replay request %q token basis %q is not provider-counted", record.RequestID, record.TokenBasis)
		}
		tokens[record.TokenBasis] = true
		if record.DeclaredInputTokens <= 0 || record.DeclaredInputTokens < record.PrefixTokens || record.MaxOutputTokens <= 0 || !requestBudgetMatchesBody(record) {
			return ReplayPreflight{}, fmt.Errorf("cachebench: replay request %q has invalid billed-token budget", record.RequestID)
		}
		input, output := int64(record.DeclaredInputTokens), int64(record.MaxOutputTokens)
		if input > limits.MaxDeclaredBilledTokens-worstCase || output > limits.MaxDeclaredBilledTokens-worstCase-input {
			return ReplayPreflight{}, fmt.Errorf("cachebench: replay declared billed-token ceiling exceeds limit %d", limits.MaxDeclaredBilledTokens)
		}
		declaredInput += input
		declaredOutput += output
		worstCase += input + output
		if !previous.IsZero() {
			gap, err := scaledReplayGap(at.Sub(previous), timeScale)
			if err != nil || gap > limits.MaxGap {
				return ReplayPreflight{}, fmt.Errorf("cachebench: replay gap before %q exceeds limit %s", record.RequestID, limits.MaxGap)
			}
			if int64(gap) > math.MaxInt64-scheduled {
				return ReplayPreflight{}, errors.New("cachebench: replay schedule duration overflow")
			}
			scheduled += int64(gap)
		}
		previous = at
	}
	bases := make([]string, 0, len(timing))
	for basis := range timing {
		bases = append(bases, basis)
	}
	sort.Strings(bases)
	tokenBases := make([]string, 0, len(tokens))
	for basis := range tokens {
		tokenBases = append(tokenBases, basis)
	}
	sort.Strings(tokenBases)
	return ReplayPreflight{
		Requests: len(records), DeclaredInputTokens: declaredInput,
		DeclaredMaxOutputTokens: declaredOutput, DeclaredBilledTokens: worstCase,
		ScheduledDuration: time.Duration(scheduled).String(), TimingBases: bases, TokenBases: tokenBases,
		TimingGrounded:                    len(bases) == 1 && bases[0] == TimingGrounded && timeScale == 1,
		InputBudgetClaimedProviderCounted: len(tokenBases) == 1 && tokenBases[0] == TokenProviderCounted,
		MaxConcurrency:                    limits.MaxConcurrency,
	}, nil
}

// ValidateReplayTarget rejects a live population that cannot meet its minimum
// sample gate even if every captured request proves cache eligible.
func ValidateReplayTarget(records []TraceRecord, target Target) error {
	if err := validateTarget(target); err != nil {
		return err
	}
	if len(records) == 0 {
		return errors.New("cachebench: replay trace is empty")
	}
	counts := map[string]int{}
	for _, record := range records {
		provider := strings.ToLower(strings.TrimSpace(record.Provider))
		if provider == "" {
			return fmt.Errorf("cachebench: replay request %q has empty provider", record.RequestID)
		}
		counts[provider]++
	}
	for provider, count := range counts {
		if count < target.MinEligibleRequest {
			return fmt.Errorf("cachebench: provider %q population %d cannot meet minimum eligible requests %d", provider, count, target.MinEligibleRequest)
		}
	}
	return nil
}

// Run validates full population, prepares equivalent bodies, then dispatches replay.
func (runner ReplayRunner) Run(ctx context.Context, records []TraceRecord, emit func(ReplayResult) error) error {
	if runner.Engine == nil || runner.Transport == nil || runner.Verifier == nil || emit == nil {
		return errors.New("cachebench: replay engine, transport, verifier, and emitter required")
	}
	if _, err := ValidateReplay(records, runner.Limits, runner.TimeScale); err != nil {
		return err
	}
	prepared, err := runner.prepare(ctx, records)
	if err != nil {
		return err
	}
	if runner.Target != nil {
		if err := validatePreparedReplayTarget(prepared, *runner.Target); err != nil {
			return err
		}
	}
	now := runner.Now
	if now == nil {
		now = time.Now
	}
	sleep := runner.Sleep
	if sleep == nil {
		sleep = sleepContext
	}
	anchorTrace, _ := time.Parse(time.RFC3339Nano, records[0].At)
	anchorReal := now().UTC()
	if runner.Limits.MaxConcurrency == 1 {
		return runner.runSequential(ctx, prepared, anchorTrace, anchorReal, now, sleep, emit)
	}
	return runner.runConcurrent(ctx, prepared, anchorTrace, anchorReal, now, sleep, emit)
}

type preparedReplay struct {
	record    TraceRecord
	optimized cacheengine.NativeResult
}

func (runner ReplayRunner) prepare(ctx context.Context, records []TraceRecord) ([]preparedReplay, error) {
	prepared := make([]preparedReplay, 0, len(records))
	for _, record := range records {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("cachebench: replay preparation interrupted: %w", err)
		}
		native, err := record.NativeRequest()
		if err != nil {
			return nil, err
		}
		optimized, err := runner.Engine.Optimize(ctx, native)
		if err != nil {
			return nil, fmt.Errorf("cachebench: optimize request %q: %w", record.RequestID, err)
		}
		equivalent := bytes.Equal(native.Body, optimized.Body)
		if optimized.Applied {
			equivalent = ModelVisibleEquivalent(native.Body, optimized.Body)
		}
		if !equivalent {
			return nil, &ReplayRunError{
				RequestID: record.RequestID, FailureCode: "model_visible_mismatch",
				Err: fmt.Errorf("cachebench: request %q failed model-visible equivalence", record.RequestID),
			}
		}
		if optimized.Decision != cacheengine.DecisionApply && optimized.Decision != cacheengine.DecisionObserveOnly && optimized.Reason != cacheengine.ReasonBelowMinimum {
			return nil, &ReplayRunError{
				RequestID: record.RequestID, FailureCode: "engine_not_cacheable",
				Err: fmt.Errorf("cachebench: request %q is not cacheable: %s", record.RequestID, optimized.Reason),
			}
		}
		prepared = append(prepared, preparedReplay{record: record, optimized: optimized})
	}
	return prepared, nil
}

func validatePreparedReplayTarget(prepared []preparedReplay, target Target) error {
	if err := validateTarget(target); err != nil {
		return err
	}
	providers := map[string]bool{}
	eligible := map[string]int{}
	for _, item := range prepared {
		provider := strings.ToLower(strings.TrimSpace(item.record.Provider))
		providers[provider] = true
		if item.optimized.Decision == cacheengine.DecisionApply || item.optimized.Decision == cacheengine.DecisionObserveOnly {
			eligible[provider]++
		}
	}
	for provider := range providers {
		if eligible[provider] < target.MinEligibleRequest {
			return fmt.Errorf("cachebench: provider %q engine-eligible population %d cannot meet minimum %d", provider, eligible[provider], target.MinEligibleRequest)
		}
	}
	return nil
}

func (runner ReplayRunner) runSequential(ctx context.Context, prepared []preparedReplay, anchorTrace, anchorReal time.Time, now func() time.Time, sleep func(context.Context, time.Duration) error, emit func(ReplayResult) error) error {
	for _, item := range prepared {
		record := item.record
		traceAt, _ := time.Parse(time.RFC3339Nano, record.At)
		offset, _ := scaledReplayGap(traceAt.Sub(anchorTrace), runner.TimeScale)
		scheduled := anchorReal.Add(offset)
		if delay := scheduled.Sub(now().UTC()); delay > 0 {
			if err := sleep(ctx, delay); err != nil {
				return fmt.Errorf("cachebench: replay schedule interrupted: %w", err)
			}
		}
		started := now().UTC()
		evidence := replayEvidenceBase(record, item.optimized, runner.TimeScale, scheduled, started, runner.Limits.MaxScheduleDrift)
		if runner.Limits.RequireGroundedTiming && evidence.ScheduleDriftMilliseconds > evidence.ScheduleToleranceMilliseconds {
			evidence.FailureCode = "schedule_drift"
			evidence.CompletedAt = now().UTC().Format(time.RFC3339Nano)
			if err := emitValidatedReplayResult(emit, ReplayResult{Evidence: evidence}); err != nil {
				return err
			}
			return &ReplayRunError{
				RequestID: record.RequestID, FailureCode: evidence.FailureCode,
				Err: fmt.Errorf("cachebench: request %q exceeded schedule drift tolerance", record.RequestID),
			}
		}
		result, runErr := runner.executePrepared(ctx, item, evidence, started, now)
		if err := emitValidatedReplayResult(emit, result); err != nil {
			return err
		}
		if runErr != nil {
			return runErr
		}
	}
	return nil
}

func (runner ReplayRunner) runConcurrent(ctx context.Context, prepared []preparedReplay, anchorTrace, anchorReal time.Time, now func() time.Time, sleep func(context.Context, time.Duration) error, emit func(ReplayResult) error) error {
	scheduleCtx, stopScheduling := context.WithCancel(ctx)
	defer stopScheduling()
	semaphore := make(chan struct{}, runner.Limits.MaxConcurrency)
	var workers sync.WaitGroup
	var emitMu sync.Mutex
	var firstErr error
	var firstErrOnce sync.Once
	recordError := func(err error) {
		if err == nil {
			return
		}
		firstErrOnce.Do(func() {
			firstErr = err
			stopScheduling()
		})
	}
	emitResult := func(result ReplayResult) {
		emitMu.Lock()
		err := emitValidatedReplayResult(emit, result)
		emitMu.Unlock()
		recordError(err)
	}

schedule:
	for _, item := range prepared {
		if err := scheduleCtx.Err(); err != nil {
			break
		}
		traceAt, _ := time.Parse(time.RFC3339Nano, item.record.At)
		offset, _ := scaledReplayGap(traceAt.Sub(anchorTrace), runner.TimeScale)
		scheduled := anchorReal.Add(offset)
		if delay := scheduled.Sub(now().UTC()); delay > 0 {
			if err := sleep(scheduleCtx, delay); err != nil {
				if ctx.Err() != nil {
					recordError(fmt.Errorf("cachebench: replay schedule interrupted: %w", ctx.Err()))
				}
				break
			}
		}
		select {
		case semaphore <- struct{}{}:
		case <-scheduleCtx.Done():
			break schedule
		}
		if scheduleCtx.Err() != nil {
			<-semaphore
			break
		}
		workers.Add(1)
		go func(item preparedReplay, scheduled time.Time) {
			defer workers.Done()
			started := now().UTC()
			evidence := replayEvidenceBase(item.record, item.optimized, runner.TimeScale, scheduled, started, runner.Limits.MaxScheduleDrift)
			if runner.Limits.RequireGroundedTiming && evidence.ScheduleDriftMilliseconds > evidence.ScheduleToleranceMilliseconds {
				evidence.FailureCode = "schedule_drift"
				evidence.CompletedAt = now().UTC().Format(time.RFC3339Nano)
				<-semaphore
				emitResult(ReplayResult{Evidence: evidence})
				recordError(&ReplayRunError{
					RequestID: item.record.RequestID, FailureCode: evidence.FailureCode,
					Err: fmt.Errorf("cachebench: request %q exceeded schedule drift tolerance", item.record.RequestID),
				})
				return
			}
			result, runErr := runner.executePrepared(ctx, item, evidence, started, now)
			<-semaphore
			emitResult(result)
			recordError(runErr)
		}(item, scheduled)
	}
	workers.Wait()
	if firstErr == nil && ctx.Err() != nil {
		return fmt.Errorf("cachebench: replay schedule interrupted: %w", ctx.Err())
	}
	return firstErr
}

func (runner ReplayRunner) executePrepared(ctx context.Context, item preparedReplay, evidence ReplayEvidenceRecord, started time.Time, now func() time.Time) (ReplayResult, error) {
	record, optimized := item.record, item.optimized
	response, sendErr := runner.Transport.Send(ctx, ReplayOutbound{
		RequestID: record.RequestID, Provider: record.Provider, Model: record.Model,
		Region: record.Region, Endpoint: record.Endpoint, Body: append([]byte(nil), optimized.Body...),
	})
	completed := now().UTC()
	evidence.HTTPStatus = response.StatusCode
	if len(response.Body) > 0 {
		evidence.ProviderEvidenceSHA256 = bodyDigest(response.Body)
	}
	if completed.Before(started) {
		evidence.CompletedAt = started.Format(time.RFC3339Nano)
		evidence.FailureCode = "clock_regression"
		return ReplayResult{Evidence: evidence, ProviderResponse: append([]byte(nil), response.Body...)}, &ReplayRunError{
			RequestID: record.RequestID, FailureCode: evidence.FailureCode,
			Err: fmt.Errorf("cachebench: clock regressed while replaying %q", record.RequestID),
		}
	}
	evidence.CompletedAt = completed.Format(time.RFC3339Nano)
	evidence.LatencyMilliseconds = completed.Sub(started).Milliseconds()
	if sendErr != nil {
		evidence.FailureCode = "transport_error"
		return ReplayResult{Evidence: evidence, ProviderResponse: append([]byte(nil), response.Body...)}, &ReplayRunError{
			RequestID: record.RequestID, FailureCode: evidence.FailureCode,
			Err: fmt.Errorf("cachebench: provider transport failed for %q: %w", record.RequestID, sendErr),
		}
	}
	if !validProviderRequestID(response.ProviderRequestID) {
		evidence.FailureCode = "provider_response_invalid"
		return ReplayResult{Evidence: evidence, ProviderResponse: append([]byte(nil), response.Body...)}, &ReplayRunError{
			RequestID: record.RequestID, FailureCode: evidence.FailureCode,
			Err: fmt.Errorf("cachebench: provider returned invalid request identity for %q", record.RequestID),
		}
	}
	evidence.ProviderRequestID = response.ProviderRequestID
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		evidence.FailureCode = "provider_http_status"
		return ReplayResult{Evidence: evidence, ProviderResponse: append([]byte(nil), response.Body...)}, &ReplayRunError{
			RequestID: record.RequestID, FailureCode: evidence.FailureCode,
			Err: fmt.Errorf("cachebench: provider returned HTTP %d for %q", response.StatusCode, record.RequestID),
		}
	}
	usage, ok := cacheengine.ExtractProviderUsage(record.Provider, response.Body)
	if !ok {
		evidence.FailureCode = "provider_usage_invalid"
		return ReplayResult{Evidence: evidence, ProviderResponse: append([]byte(nil), response.Body...)}, &ReplayRunError{
			RequestID: record.RequestID, FailureCode: evidence.FailureCode,
			Err: fmt.Errorf("cachebench: provider usage unavailable for %q", record.RequestID),
		}
	}
	evidence.ProviderUsageSHA256 = bodyDigest(usage.RawUsage)
	evidence.ProviderTotalInputTokens = usage.TotalInputTokens
	evidence.ProviderOutputTokens = usage.OutputTokens
	if usage.TotalInputTokens > record.DeclaredInputTokens {
		evidence.FailureCode = "provider_input_budget_exceeded"
		return ReplayResult{Evidence: evidence, ProviderResponse: append([]byte(nil), response.Body...)}, &ReplayRunError{
			RequestID: record.RequestID, FailureCode: evidence.FailureCode,
			Err: fmt.Errorf("cachebench: provider input tokens %d exceed declared ceiling %d for %q", usage.TotalInputTokens, record.DeclaredInputTokens, record.RequestID),
		}
	}
	if usage.OutputTokens > record.MaxOutputTokens {
		evidence.FailureCode = "provider_output_budget_exceeded"
		return ReplayResult{Evidence: evidence, ProviderResponse: append([]byte(nil), response.Body...)}, &ReplayRunError{
			RequestID: record.RequestID, FailureCode: evidence.FailureCode,
			Err: fmt.Errorf("cachebench: provider output tokens %d exceed request ceiling %d for %q", usage.OutputTokens, record.MaxOutputTokens, record.RequestID),
		}
	}
	verification, err := runner.Verifier.Verify(ctx, ReplayVerificationInput{Trace: record, Optimized: optimized, Response: response})
	if err != nil {
		evidence.FailureCode = "quality_verifier_error"
		return ReplayResult{Evidence: evidence, ProviderResponse: append([]byte(nil), response.Body...)}, &ReplayRunError{
			RequestID: record.RequestID, FailureCode: evidence.FailureCode,
			Err: fmt.Errorf("cachebench: quality verifier failed for %q: %w", record.RequestID, err),
		}
	}
	if !validTaskVerification(verification) {
		evidence.FailureCode = "quality_provenance_missing"
		return ReplayResult{Evidence: evidence, ProviderResponse: append([]byte(nil), response.Body...)}, &ReplayRunError{
			RequestID: record.RequestID, FailureCode: evidence.FailureCode,
			Err: fmt.Errorf("cachebench: quality provenance missing for %q", record.RequestID),
		}
	}
	evidence.QualityPassed = verification.Passed
	evidence.QualityVerifier = verification.Verifier
	evidence.QualityEvidenceSHA256 = bodyDigest(verification.Evidence)
	evidence.Success = true
	observation := NewObservationRecord(record.RequestID, record.Provider, record.Epoch, usage.TotalInputTokens, record.Body, response.Body, optimized, verification, usage.RawUsage)
	return ReplayResult{
		Evidence: evidence, Observation: &observation,
		ProviderResponse:     append([]byte(nil), response.Body...),
		VerificationEvidence: append([]byte(nil), verification.Evidence...),
	}, nil
}

func replayEvidenceBase(record TraceRecord, optimized cacheengine.NativeResult, scale float64, scheduled, started time.Time, tolerance time.Duration) ReplayEvidenceRecord {
	drift := absDuration(started.Sub(scheduled))
	return ReplayEvidenceRecord{
		Schema: ReplayEvidenceSchema, RequestID: record.RequestID,
		TraceBodySHA256: record.BodySHA256, WireBodySHA256: bodyDigest(optimized.Body),
		Provider: record.Provider, Model: record.Model, Epoch: record.Epoch,
		TimingBasis: record.TimingBasis, TokenBasis: record.TokenBasis, TimeScale: scale,
		TimingFaithful: record.TimingBasis == TimingGrounded && scale == 1 && drift <= tolerance,
		ScheduledAt:    scheduled.Format(time.RFC3339Nano), ScheduleDriftMilliseconds: drift.Milliseconds(),
		ScheduleToleranceMilliseconds: tolerance.Milliseconds(),
		StartedAt:                     started.Format(time.RFC3339Nano), Applied: optimized.Applied,
		Decision: optimized.Decision, Reason: optimized.Reason, Attribution: optimized.Profile.Attribution,
		OptimizerIDs: append([]string(nil), optimized.OptimizerIDs...),
	}
}

func scaledReplayGap(gap time.Duration, scale float64) (time.Duration, error) {
	if gap < 0 || scale <= 0 || math.IsNaN(scale) || math.IsInf(scale, 0) {
		return 0, errors.New("cachebench: invalid replay gap")
	}
	if gap == 0 {
		return 0, nil
	}
	if scale > float64(math.MaxInt64)/float64(gap) {
		return 0, errors.New("cachebench: replay gap overflow")
	}
	return time.Duration(float64(gap) * scale), nil
}

func sleepContext(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// WriteReplayEvidenceJSON validates and writes one evidence record.
func WriteReplayEvidenceJSON(writer interface{ Write([]byte) (int, error) }, record ReplayEvidenceRecord) error {
	if err := validateReplayEvidence(record); err != nil {
		return err
	}
	encoder := json.NewEncoder(writer)
	encoder.SetEscapeHTML(false)
	return encoder.Encode(record)
}

func emitValidatedReplayResult(emit func(ReplayResult) error, result ReplayResult) error {
	if err := validateReplayResult(result); err != nil {
		return err
	}
	return emit(result)
}

func validateReplayResult(result ReplayResult) error {
	if err := validateReplayEvidence(result.Evidence); err != nil {
		return err
	}
	if result.Evidence.Success {
		if result.Observation == nil || len(result.ProviderResponse) == 0 || len(result.VerificationEvidence) == 0 || bodyDigest(result.ProviderResponse) != result.Evidence.ProviderEvidenceSHA256 || bodyDigest(result.VerificationEvidence) != result.Evidence.QualityEvidenceSHA256 {
			return errors.New("cachebench: successful replay result is not bound to retained artifacts")
		}
		if err := validateObservationRecords([]ObservationRecord{*result.Observation}); err != nil {
			return err
		}
		observation := result.Observation
		if observation.RequestID != result.Evidence.RequestID || observation.Provider != result.Evidence.Provider || observation.Epoch != result.Evidence.Epoch || observation.RequestBodySHA256 != result.Evidence.TraceBodySHA256 || observation.ProviderEvidenceSHA256 != result.Evidence.ProviderEvidenceSHA256 || observation.EligibleInputTokens != result.Evidence.ProviderTotalInputTokens || observation.Applied != result.Evidence.Applied || observation.EngineDecision != result.Evidence.Decision || observation.EngineReason != result.Evidence.Reason || observation.Attribution != result.Evidence.Attribution || !slices.Equal(observation.OptimizerIDs, result.Evidence.OptimizerIDs) || observation.QualityPassed != result.Evidence.QualityPassed || observation.QualityEvidenceSHA256 != result.Evidence.QualityEvidenceSHA256 || observation.QualityVerifier != result.Evidence.QualityVerifier || bodyDigest(observation.Usage) != result.Evidence.ProviderUsageSHA256 {
			return errors.New("cachebench: replay observation is not bound to replay evidence")
		}
		return nil
	}
	if result.Observation != nil || len(result.VerificationEvidence) != 0 {
		return errors.New("cachebench: failed replay result contains success-only artifacts")
	}
	if len(result.ProviderResponse) > 0 {
		if bodyDigest(result.ProviderResponse) != result.Evidence.ProviderEvidenceSHA256 {
			return errors.New("cachebench: failed replay response is not bound to evidence")
		}
	} else if result.Evidence.ProviderEvidenceSHA256 != "" {
		return errors.New("cachebench: failed replay evidence references missing response")
	}
	return nil
}

func validTaskVerification(verification TaskVerification) bool {
	return validBoundedText(verification.Verifier, 256, false) && validUniqueJSONObject(verification.Evidence)
}

func validProviderRequestID(value string) bool {
	return validBoundedText(value, 256, true)
}

func validateReplayEvidence(record ReplayEvidenceRecord) error {
	if record.Schema != ReplayEvidenceSchema || !validBoundedText(record.RequestID, 512, false) || !validSHA256(record.TraceBodySHA256) || !validSHA256(record.WireBodySHA256) || !validBoundedText(record.Provider, 64, false) || !validBoundedText(record.Model, 512, false) || !validBoundedText(record.Epoch, 1024, false) || !validTimingBasis(record.TimingBasis) || !validBoundedText(record.TokenBasis, 128, false) || record.TimeScale <= 0 || record.TimeScale > 1000 || !validBoundedText(record.Reason, 1024, false) || record.ScheduleDriftMilliseconds < 0 || record.ScheduleToleranceMilliseconds <= 0 || record.HTTPStatus < 0 || record.HTTPStatus > 999 || !validProviderRequestID(record.ProviderRequestID) {
		return errors.New("cachebench: invalid replay evidence")
	}
	scheduled, scheduledErr := time.Parse(time.RFC3339Nano, record.ScheduledAt)
	started, startErr := time.Parse(time.RFC3339Nano, record.StartedAt)
	completed, completedErr := time.Parse(time.RFC3339Nano, record.CompletedAt)
	if scheduledErr != nil || startErr != nil || completedErr != nil || completed.Before(started) || record.TimingFaithful != (record.TimingBasis == TimingGrounded && record.TimeScale == 1 && record.ScheduleDriftMilliseconds <= record.ScheduleToleranceMilliseconds) || absDuration(started.Sub(scheduled)).Milliseconds() != record.ScheduleDriftMilliseconds {
		return errors.New("cachebench: invalid replay timing evidence")
	}
	if record.LatencyMilliseconds < 0 {
		return errors.New("cachebench: negative replay latency")
	}
	if record.ProviderUsageSHA256 != "" {
		if !validEvidenceSHA256(record.ProviderUsageSHA256) || !validEvidenceSHA256(record.ProviderEvidenceSHA256) || record.ProviderTotalInputTokens <= 0 || record.ProviderOutputTokens < 0 {
			return errors.New("cachebench: invalid replay provider usage evidence")
		}
	} else if record.ProviderTotalInputTokens != 0 || record.ProviderOutputTokens != 0 {
		return errors.New("cachebench: replay token counters lack provider usage evidence")
	}
	if record.Applied != (record.Decision == cacheengine.DecisionApply) || record.Applied && len(record.OptimizerIDs) == 0 {
		return errors.New("cachebench: inconsistent replay engine decision")
	}
	switch record.Attribution {
	case cacheengine.AttributionNone, cacheengine.AttributionCausal, cacheengine.AttributionAffinity, cacheengine.AttributionOrganic:
	default:
		return errors.New("cachebench: invalid replay attribution")
	}
	if record.Success {
		if record.HTTPStatus < 200 || record.HTTPStatus >= 300 || !validEvidenceSHA256(record.ProviderEvidenceSHA256) || !validEvidenceSHA256(record.ProviderUsageSHA256) || record.ProviderTotalInputTokens <= 0 || !validBoundedText(record.QualityVerifier, 256, false) || !validEvidenceSHA256(record.QualityEvidenceSHA256) || record.FailureCode != "" {
			return errors.New("cachebench: incomplete successful replay evidence")
		}
	} else if !validReplayFailureCode(record.FailureCode) {
		return errors.New("cachebench: failed replay evidence needs known failure_code")
	}
	return nil
}

func validReplayFailureCode(value string) bool {
	switch value {
	case "schedule_drift", "model_visible_mismatch", "engine_not_cacheable", "clock_regression", "transport_error", "provider_response_invalid", "provider_http_status", "provider_usage_invalid", "provider_input_budget_exceeded", "provider_output_budget_exceeded", "quality_verifier_error", "quality_provenance_missing":
		return true
	default:
		return false
	}
}

func absDuration(value time.Duration) time.Duration {
	if value == time.Duration(math.MinInt64) {
		return time.Duration(math.MaxInt64)
	}
	if value < 0 {
		return -value
	}
	return value
}
