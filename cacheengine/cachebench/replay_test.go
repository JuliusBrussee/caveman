package cachebench

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/JuliusBrussee/caveman/cacheengine"
	"github.com/JuliusBrussee/caveman/shared/platform/awssig"
)

func TestReplayRunnerProducesBoundObservedPopulation(t *testing.T) {
	records := replayTraceRecords(t, 2, 8192)
	responses := 0
	transport := ReplayTransportFunc(func(_ context.Context, request ReplayOutbound) (ReplayResponse, error) {
		responses++
		if request.Provider != "openai" || !ModelVisibleEquivalent(records[responses-1].Body, request.Body) {
			t.Fatalf("outbound = %#v", request)
		}
		cached, write := 0, 8000
		if responses == 2 {
			cached, write = 8000, 0
		}
		body := []byte(fmt.Sprintf(`{"id":"provider-%d","usage":{"prompt_tokens":%d,"completion_tokens":8,"prompt_tokens_details":{"cached_tokens":%d,"cache_write_tokens":%d}}}`, responses, records[responses-1].DeclaredInputTokens, cached, write))
		return ReplayResponse{StatusCode: 200, Body: body, ProviderRequestID: fmt.Sprintf("provider-%d", responses)}, nil
	})
	verifier := ReplayVerifierFunc(func(_ context.Context, input ReplayVerificationInput) (TaskVerification, error) {
		return TaskVerification{Passed: true, Verifier: "fixture-agent@v1", Evidence: []byte(fmt.Sprintf(`{"request_id":%q,"passed":true}`, input.Trace.RequestID))}, nil
	})
	var slept []time.Duration
	var results []ReplayResult
	clock := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	runner := ReplayRunner{
		Engine: cacheengine.New(cacheengine.Config{}), Transport: transport, Verifier: verifier,
		Limits: ReplayLimits{MaxRequests: 2, MaxDeclaredBilledTokens: 30000, MaxGap: time.Minute, MaxScheduleDrift: time.Second, MaxConcurrency: 1}, TimeScale: 1,
		Now: func() time.Time { return clock },
		Sleep: func(_ context.Context, delay time.Duration) error {
			slept = append(slept, delay)
			clock = clock.Add(delay)
			return nil
		},
	}
	if err := runner.Run(context.Background(), records, func(result ReplayResult) error {
		results = append(results, result)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 || len(slept) != 1 || slept[0] != 3*time.Second {
		t.Fatalf("results=%d sleep=%v", len(results), slept)
	}
	observations := make([]ObservationRecord, 0, len(results))
	for _, result := range results {
		if !result.Evidence.Success || result.Evidence.TimingFaithful || result.Evidence.ProviderEvidenceSHA256 != bodyDigest(result.ProviderResponse) || result.Observation == nil || result.Observation.Schema != ObservationSchema || !result.Observation.CacheEligible {
			t.Fatalf("result = %#v", result)
		}
		observations = append(observations, *result.Observation)
		var evidence bytes.Buffer
		if err := WriteReplayEvidenceJSON(&evidence, result.Evidence); err != nil || !json.Valid(bytes.TrimSpace(evidence.Bytes())) {
			t.Fatalf("evidence write = %q, err=%v", evidence.String(), err)
		}
	}
	report, err := EvaluateObservedAgainstTrace(observations, records, Target{RequestHitRate: 0, TokenHitRate: 0, MinEligibleRequest: 2})
	if err != nil || !report.Overall.GatePassed || report.Overall.RequestHits != 1 || report.Overall.InvalidSamples != 0 {
		t.Fatalf("report=%#v err=%v", report.Overall, err)
	}
}

func TestReplayRunnerPreservesBelowMinimumPopulation(t *testing.T) {
	records := replayTraceRecords(t, 2, 10)
	runner := ReplayRunner{
		Engine: cacheengine.New(cacheengine.Config{}),
		Transport: ReplayTransportFunc(func(_ context.Context, request ReplayOutbound) (ReplayResponse, error) {
			declared := records[0].DeclaredInputTokens
			if request.RequestID == records[1].RequestID {
				declared = records[1].DeclaredInputTokens
			}
			return ReplayResponse{StatusCode: 200, Body: []byte(fmt.Sprintf(`{"usage":{"prompt_tokens":%d,"completion_tokens":8,"prompt_tokens_details":{}}}`, declared))}, nil
		}),
		Verifier: ReplayVerifierFunc(func(_ context.Context, _ ReplayVerificationInput) (TaskVerification, error) {
			return TaskVerification{Passed: true, Verifier: "fixture@v1", Evidence: []byte(`{"passed":true}`)}, nil
		}),
		Limits: ReplayLimits{MaxRequests: 2, MaxDeclaredBilledTokens: 2000, MaxGap: time.Minute, MaxScheduleDrift: time.Second, MaxConcurrency: 1}, TimeScale: 1,
		Sleep: func(context.Context, time.Duration) error { return nil },
	}
	var observations []ObservationRecord
	if err := runner.Run(context.Background(), records, func(result ReplayResult) error {
		if result.Observation == nil {
			t.Fatal("missing observation")
		}
		observations = append(observations, *result.Observation)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, observation := range observations {
		if observation.CacheEligible || observation.EngineReason != cacheengine.ReasonBelowMinimum {
			t.Fatalf("observation = %#v", observation)
		}
	}
	report, err := EvaluateObservedAgainstTrace(observations, records, Target{RequestHitRate: 0, TokenHitRate: 0, MinEligibleRequest: 1})
	if err != nil || report.Overall.IneligibleRequests != 2 || report.Overall.InvalidSamples != 0 || report.Overall.GatePassed {
		t.Fatalf("report=%#v err=%v", report.Overall, err)
	}
}

func TestReplayRunnerRejectsKnownEngineFailureBeforeProviderTraffic(t *testing.T) {
	record := replayTraceRecords(t, 2, 8192)[0]
	var body map[string]any
	if err := json.Unmarshal(record.Body, &body); err != nil {
		t.Fatal(err)
	}
	body["prompt_cache_key"] = "caller-managed"
	mutated, _ := json.Marshal(body)
	record.Body = mutated
	record.BodySHA256 = bodyDigest(mutated)
	transportCalls := 0
	runner := ReplayRunner{
		Engine: cacheengine.New(cacheengine.Config{}),
		Transport: ReplayTransportFunc(func(context.Context, ReplayOutbound) (ReplayResponse, error) {
			transportCalls++
			return ReplayResponse{}, nil
		}),
		Verifier: ReplayVerifierFunc(func(context.Context, ReplayVerificationInput) (TaskVerification, error) {
			t.Fatal("verifier called")
			return TaskVerification{}, nil
		}),
		Limits: ReplayLimits{
			MaxRequests: 1, MaxDeclaredBilledTokens: int64(record.DeclaredInputTokens + record.MaxOutputTokens),
			MaxGap: time.Minute, MaxScheduleDrift: time.Second, MaxConcurrency: 1,
		},
		TimeScale: 1,
	}
	err := runner.Run(context.Background(), []TraceRecord{record}, func(ReplayResult) error {
		t.Fatal("emitter called")
		return nil
	})
	var replayErr *ReplayRunError
	if !errors.As(err, &replayErr) || replayErr.RequestID != record.RequestID || replayErr.FailureCode != "engine_not_cacheable" || transportCalls != 0 {
		t.Fatalf("replayErr=%#v calls=%d err=%v", replayErr, transportCalls, err)
	}
}

func TestReplayRunnerRejectsInsufficientEngineEligiblePopulationBeforeTraffic(t *testing.T) {
	records := replayTraceRecords(t, 2, 10)
	transportCalls := 0
	target := Target{RequestHitRate: .97, TokenHitRate: .97, MinEligibleRequest: 1}
	runner := ReplayRunner{
		Engine: cacheengine.New(cacheengine.Config{}),
		Transport: ReplayTransportFunc(func(context.Context, ReplayOutbound) (ReplayResponse, error) {
			transportCalls++
			return ReplayResponse{}, nil
		}),
		Verifier: ReplayVerifierFunc(func(context.Context, ReplayVerificationInput) (TaskVerification, error) {
			t.Fatal("verifier called")
			return TaskVerification{}, nil
		}),
		Limits: ReplayLimits{
			MaxRequests: 2, MaxDeclaredBilledTokens: 2000, MaxGap: time.Minute,
			MaxScheduleDrift: time.Second, MaxConcurrency: 1,
		},
		Target: &target, TimeScale: 1,
	}
	if err := runner.Run(context.Background(), records, func(ReplayResult) error { return nil }); err == nil || transportCalls != 0 {
		t.Fatalf("calls=%d err=%v", transportCalls, err)
	}
}

func TestReplayPreflightFailsClosed(t *testing.T) {
	records := replayTraceRecords(t, 2, 8192)
	limits := ReplayLimits{MaxRequests: 2, MaxDeclaredBilledTokens: 30000, MaxGap: time.Minute, MaxScheduleDrift: time.Second, MaxConcurrency: 1, RequireGroundedTiming: true}
	if _, err := ValidateReplay(records, limits, 1); err == nil {
		t.Fatal("synthetic timing accepted as grounded")
	}
	for index := range records {
		records[index].TimingBasis = TimingGrounded
	}
	preflight, err := ValidateReplay(records, limits, 1)
	worstCase := int64(records[0].DeclaredInputTokens + records[0].MaxOutputTokens + records[1].DeclaredInputTokens + records[1].MaxOutputTokens)
	if err != nil || !preflight.TimingGrounded || preflight.Requests != 2 || preflight.MaxConcurrency != 1 || preflight.DeclaredBilledTokens != worstCase {
		t.Fatalf("preflight=%#v err=%v", preflight, err)
	}
	mutated := append([]TraceRecord(nil), records...)
	mutated[0].Schema = TraceSchemaV1
	if _, err := ValidateReplay(mutated, limits, 1); err == nil {
		t.Fatal("legacy trace accepted")
	}
	mutated = append([]TraceRecord(nil), records...)
	mutated[0].Schema = TraceSchemaV2
	if _, err := mutated[0].NativeRequest(); err != nil {
		t.Fatalf("v2 optimizer reconstruction failed: %v", err)
	}
	if _, err := ValidateReplay(mutated, limits, 1); err == nil {
		t.Fatal("v2 trace without billed-token budget accepted for live replay")
	}
	mutated = append([]TraceRecord(nil), records...)
	mutated[0].BodySHA256 = strings.Repeat("0", 64)
	if _, err := ValidateReplay(mutated, limits, 1); err == nil {
		t.Fatal("tampered trace accepted")
	}
	if _, err := ValidateReplay(records, ReplayLimits{MaxRequests: 1, MaxDeclaredBilledTokens: 30000, MaxGap: time.Minute, MaxScheduleDrift: time.Second, MaxConcurrency: 1}, 1); err == nil {
		t.Fatal("request budget bypassed")
	}
	if _, err := ValidateReplay(records, ReplayLimits{MaxRequests: 2, MaxDeclaredBilledTokens: 1, MaxGap: time.Minute, MaxScheduleDrift: time.Second, MaxConcurrency: 1}, 1); err == nil {
		t.Fatal("token budget bypassed")
	}
	if _, err := ValidateReplay(records, ReplayLimits{MaxRequests: 2, MaxDeclaredBilledTokens: 30000, MaxGap: time.Minute, MaxScheduleDrift: time.Second, MaxConcurrency: 1025}, 1); err == nil {
		t.Fatal("unsafe concurrency accepted")
	}
	mutated = append([]TraceRecord(nil), records...)
	mutated[0].MaxOutputTokens++
	if _, err := ValidateReplay(mutated, limits, 1); err == nil {
		t.Fatal("output ceiling not bound to provider body")
	}
	mutated = append([]TraceRecord(nil), records...)
	mutated[0].Body = json.RawMessage(`{"model":"gpt-5.6","model":"other","max_completion_tokens":256}`)
	mutated[0].BodySHA256 = bodyDigest(mutated[0].Body)
	if _, err := mutated[0].NativeRequest(); err == nil {
		t.Fatal("duplicate-key body reconstructed")
	}
	mutated = append([]TraceRecord(nil), records...)
	mutated[0].StableSegmentCount = len(mutated[0].Prefix) + 1
	if _, err := mutated[0].NativeRequest(); err == nil {
		t.Fatal("invalid prefix reconstructed")
	}
}

func TestReplayTargetFailsBeforeUnmeetablePopulation(t *testing.T) {
	records := replayTraceRecords(t, 2, 8192)
	if err := ValidateReplayTarget(records, Target{RequestHitRate: .97, TokenHitRate: .97, MinEligibleRequest: 2}); err != nil {
		t.Fatal(err)
	}
	if err := ValidateReplayTarget(records, Target{RequestHitRate: .97, TokenHitRate: .97, MinEligibleRequest: 3}); err == nil {
		t.Fatal("unmeetable minimum population accepted")
	}
	if err := ValidateReplayTarget(records, Target{RequestHitRate: math.NaN(), TokenHitRate: .97, MinEligibleRequest: 1}); err == nil {
		t.Fatal("NaN target accepted")
	}
}

func TestReplayRunnerFailsWhenProviderExceedsDeclaredInputCeiling(t *testing.T) {
	record := replayTraceRecords(t, 2, 8192)[0]
	runner := ReplayRunner{
		Engine: cacheengine.New(cacheengine.Config{}),
		Transport: ReplayTransportFunc(func(context.Context, ReplayOutbound) (ReplayResponse, error) {
			body := fmt.Sprintf(`{"usage":{"prompt_tokens":%d,"completion_tokens":8,"prompt_tokens_details":{"cached_tokens":0}}}`, record.DeclaredInputTokens+1)
			return ReplayResponse{StatusCode: 200, Body: []byte(body)}, nil
		}),
		Verifier: ReplayVerifierFunc(func(context.Context, ReplayVerificationInput) (TaskVerification, error) {
			t.Fatal("verifier called after provider input budget overrun")
			return TaskVerification{}, nil
		}),
		Limits: ReplayLimits{
			MaxRequests: 1, MaxDeclaredBilledTokens: int64(record.DeclaredInputTokens + record.MaxOutputTokens),
			MaxGap: time.Minute, MaxScheduleDrift: time.Second, MaxConcurrency: 1,
		},
		TimeScale: 1,
	}
	var result ReplayResult
	err := runner.Run(context.Background(), []TraceRecord{record}, func(value ReplayResult) error {
		result = value
		return nil
	})
	var replayErr *ReplayRunError
	if !errors.As(err, &replayErr) || replayErr.RequestID != record.RequestID || replayErr.FailureCode != "provider_input_budget_exceeded" || result.Observation != nil || result.Evidence.FailureCode != "provider_input_budget_exceeded" {
		t.Fatalf("result=%#v replayErr=%#v err=%v", result, replayErr, err)
	}
}

func TestReplayRunnerFailsWhenProviderExceedsOutputCeiling(t *testing.T) {
	record := replayTraceRecords(t, 2, 8192)[0]
	runner := ReplayRunner{
		Engine: cacheengine.New(cacheengine.Config{}),
		Transport: ReplayTransportFunc(func(context.Context, ReplayOutbound) (ReplayResponse, error) {
			body := fmt.Sprintf(`{"usage":{"prompt_tokens":%d,"completion_tokens":%d,"prompt_tokens_details":{"cached_tokens":0}}}`, record.DeclaredInputTokens, record.MaxOutputTokens+1)
			return ReplayResponse{StatusCode: 200, Body: []byte(body)}, nil
		}),
		Verifier: ReplayVerifierFunc(func(context.Context, ReplayVerificationInput) (TaskVerification, error) {
			t.Fatal("verifier called after provider output budget overrun")
			return TaskVerification{}, nil
		}),
		Limits: ReplayLimits{
			MaxRequests: 1, MaxDeclaredBilledTokens: int64(record.DeclaredInputTokens + record.MaxOutputTokens),
			MaxGap: time.Minute, MaxScheduleDrift: time.Second, MaxConcurrency: 1,
		},
		TimeScale: 1,
	}
	var result ReplayResult
	err := runner.Run(context.Background(), []TraceRecord{record}, func(value ReplayResult) error {
		result = value
		return nil
	})
	var replayErr *ReplayRunError
	if !errors.As(err, &replayErr) || replayErr.RequestID != record.RequestID || replayErr.FailureCode != "provider_output_budget_exceeded" || result.Observation != nil || result.Evidence.ProviderOutputTokens != record.MaxOutputTokens+1 {
		t.Fatalf("result=%#v replayErr=%#v err=%v", result, replayErr, err)
	}
}

func TestReplayRunnerRejectsMalformedEmbeddedProviderAndVerifierEvidence(t *testing.T) {
	record := replayTraceRecords(t, 2, 8192)[0]
	responseBody := []byte(fmt.Sprintf(`{"usage":{"prompt_tokens":%d,"completion_tokens":8,"prompt_tokens_details":{"cached_tokens":8000}}}`, record.DeclaredInputTokens))
	tests := []struct {
		name, failureCode string
		requestID         string
		verification      TaskVerification
		wantVerifierCalls int
	}{
		{name: "provider request id", failureCode: "provider_response_invalid", requestID: "bad\nrequest", verification: TaskVerification{Passed: true, Verifier: "fixture@v1", Evidence: []byte(`{"ok":true}`)}},
		{name: "verifier duplicate JSON", failureCode: "quality_provenance_missing", requestID: "provider-id", verification: TaskVerification{Passed: true, Verifier: "fixture@v1", Evidence: []byte(`{"ok":true,"ok":false}`)}, wantVerifierCalls: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			verifierCalls := 0
			runner := ReplayRunner{
				Engine: cacheengine.New(cacheengine.Config{}),
				Transport: ReplayTransportFunc(func(context.Context, ReplayOutbound) (ReplayResponse, error) {
					return ReplayResponse{StatusCode: 200, Body: responseBody, ProviderRequestID: test.requestID}, nil
				}),
				Verifier: ReplayVerifierFunc(func(context.Context, ReplayVerificationInput) (TaskVerification, error) {
					verifierCalls++
					return test.verification, nil
				}),
				Limits: ReplayLimits{
					MaxRequests: 1, MaxDeclaredBilledTokens: int64(record.DeclaredInputTokens + record.MaxOutputTokens),
					MaxGap: time.Minute, MaxScheduleDrift: time.Second, MaxConcurrency: 1,
				},
				TimeScale: 1,
			}
			var result ReplayResult
			err := runner.Run(context.Background(), []TraceRecord{record}, func(value ReplayResult) error {
				result = value
				return nil
			})
			var replayErr *ReplayRunError
			if !errors.As(err, &replayErr) || replayErr.FailureCode != test.failureCode || result.Evidence.FailureCode != test.failureCode || verifierCalls != test.wantVerifierCalls {
				t.Fatalf("result=%#v replayErr=%#v calls=%d err=%v", result, replayErr, verifierCalls, err)
			}
		})
	}
}

func TestReplayRunnerFailsClosedOnClockRegression(t *testing.T) {
	record := replayTraceRecords(t, 2, 8192)[0]
	clock := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	runner := ReplayRunner{
		Engine: cacheengine.New(cacheengine.Config{}),
		Transport: ReplayTransportFunc(func(context.Context, ReplayOutbound) (ReplayResponse, error) {
			clock = clock.Add(-time.Second)
			return ReplayResponse{StatusCode: 200, Body: []byte(`{"ignored":true}`)}, nil
		}),
		Verifier: ReplayVerifierFunc(func(context.Context, ReplayVerificationInput) (TaskVerification, error) {
			t.Fatal("verifier called after clock regression")
			return TaskVerification{}, nil
		}),
		Limits: ReplayLimits{
			MaxRequests: 1, MaxDeclaredBilledTokens: int64(record.DeclaredInputTokens + record.MaxOutputTokens),
			MaxGap: time.Minute, MaxScheduleDrift: time.Second, MaxConcurrency: 1,
		},
		TimeScale: 1, Now: func() time.Time { return clock },
	}
	var result ReplayResult
	err := runner.Run(context.Background(), []TraceRecord{record}, func(value ReplayResult) error {
		result = value
		return nil
	})
	var replayErr *ReplayRunError
	if !errors.As(err, &replayErr) || replayErr.FailureCode != "clock_regression" || result.Evidence.FailureCode != "clock_regression" || result.Evidence.CompletedAt != result.Evidence.StartedAt {
		t.Fatalf("result=%#v replayErr=%#v err=%v", result, replayErr, err)
	}
}

func TestReplayRunnerEmitsBoundFailureWithoutObservation(t *testing.T) {
	records := replayTraceRecords(t, 2, 8192)[:1]
	runner := ReplayRunner{
		Engine: cacheengine.New(cacheengine.Config{}),
		Transport: ReplayTransportFunc(func(context.Context, ReplayOutbound) (ReplayResponse, error) {
			return ReplayResponse{}, errors.New("network unavailable")
		}),
		Verifier: ReplayVerifierFunc(func(context.Context, ReplayVerificationInput) (TaskVerification, error) {
			t.Fatal("verifier called after transport failure")
			return TaskVerification{}, nil
		}),
		Limits: ReplayLimits{MaxRequests: 1, MaxDeclaredBilledTokens: 10000, MaxGap: time.Minute, MaxScheduleDrift: time.Second, MaxConcurrency: 1}, TimeScale: 1,
	}
	var result ReplayResult
	err := runner.Run(context.Background(), records, func(value ReplayResult) error { result = value; return nil })
	if err == nil || result.Observation != nil || result.Evidence.Success || result.Evidence.FailureCode != "transport_error" {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	var output bytes.Buffer
	if err := WriteReplayEvidenceJSON(&output, result.Evidence); err != nil {
		t.Fatalf("failed evidence rejected: %v", err)
	}
}

func TestGroundedReplayAbortsWhenProviderLatencyBreaksAbsoluteSchedule(t *testing.T) {
	records := replayTraceRecords(t, 2, 8192)
	for index := range records {
		records[index].TimingBasis = TimingGrounded
	}
	clock := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	transportCalls := 0
	runner := ReplayRunner{
		Engine: cacheengine.New(cacheengine.Config{}),
		Transport: ReplayTransportFunc(func(context.Context, ReplayOutbound) (ReplayResponse, error) {
			transportCalls++
			clock = clock.Add(5 * time.Second)
			return ReplayResponse{StatusCode: 200, Body: []byte(fmt.Sprintf(`{"usage":{"prompt_tokens":%d,"completion_tokens":8,"prompt_tokens_details":{"cached_tokens":0,"cache_write_tokens":8000}}}`, records[0].DeclaredInputTokens))}, nil
		}),
		Verifier: ReplayVerifierFunc(func(context.Context, ReplayVerificationInput) (TaskVerification, error) {
			return TaskVerification{Passed: true, Verifier: "fixture@v1", Evidence: []byte(`{"passed":true}`)}, nil
		}),
		Limits: ReplayLimits{
			MaxRequests: 2, MaxDeclaredBilledTokens: 30000, MaxGap: time.Minute,
			MaxScheduleDrift: 100 * time.Millisecond, MaxConcurrency: 1, RequireGroundedTiming: true,
		},
		TimeScale: 1, Now: func() time.Time { return clock },
		Sleep: func(_ context.Context, delay time.Duration) error { clock = clock.Add(delay); return nil },
	}
	var evidence []ReplayEvidenceRecord
	err := runner.Run(context.Background(), records, func(result ReplayResult) error {
		evidence = append(evidence, result.Evidence)
		return nil
	})
	if err == nil || transportCalls != 1 || len(evidence) != 2 || evidence[1].FailureCode != "schedule_drift" || evidence[1].TimingFaithful {
		t.Fatalf("calls=%d evidence=%#v err=%v", transportCalls, evidence, err)
	}
}

func TestReplayRunnerBoundsConcurrentAbsoluteDispatch(t *testing.T) {
	records := replayTraceRecords(t, 3, 8192)
	for index := range records {
		records[index].At = records[0].At
		records[index].TimingBasis = TimingGrounded
	}
	var stateMu sync.Mutex
	active, maximum, calls := 0, 0, 0
	twoStarted := make(chan struct{})
	release := make(chan struct{})
	transport := ReplayTransportFunc(func(ctx context.Context, _ ReplayOutbound) (ReplayResponse, error) {
		stateMu.Lock()
		active++
		calls++
		if active > maximum {
			maximum = active
		}
		if active == 2 && calls == 2 {
			close(twoStarted)
		}
		stateMu.Unlock()
		select {
		case <-release:
		case <-ctx.Done():
			return ReplayResponse{}, ctx.Err()
		}
		stateMu.Lock()
		active--
		stateMu.Unlock()
		return ReplayResponse{StatusCode: 200, Body: []byte(`{"usage":{"prompt_tokens":8000,"completion_tokens":8,"prompt_tokens_details":{"cached_tokens":8000}}}`)}, nil
	})
	runner := ReplayRunner{
		Engine: cacheengine.New(cacheengine.Config{}), Transport: transport,
		Verifier: ReplayVerifierFunc(func(context.Context, ReplayVerificationInput) (TaskVerification, error) {
			return TaskVerification{Passed: true, Verifier: "fixture@v1", Evidence: []byte(`{"passed":true}`)}, nil
		}),
		Limits: ReplayLimits{
			MaxRequests: 3, MaxDeclaredBilledTokens: 40000, MaxGap: time.Minute,
			MaxScheduleDrift: 5 * time.Second, MaxConcurrency: 2, RequireGroundedTiming: true,
		},
		TimeScale: 1,
	}
	var resultMu sync.Mutex
	var results []ReplayResult
	done := make(chan error, 1)
	go func() {
		done <- runner.Run(context.Background(), records, func(result ReplayResult) error {
			resultMu.Lock()
			results = append(results, result)
			resultMu.Unlock()
			return nil
		})
	}()
	select {
	case <-twoStarted:
		close(release)
	case <-time.After(5 * time.Second):
		t.Fatal("two concurrent provider requests did not start")
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("concurrent replay did not finish")
	}
	stateMu.Lock()
	gotCalls, gotMaximum := calls, maximum
	stateMu.Unlock()
	resultMu.Lock()
	gotResults := append([]ReplayResult(nil), results...)
	resultMu.Unlock()
	if gotCalls != 3 || gotMaximum != 2 || len(gotResults) != 3 {
		t.Fatalf("calls=%d max=%d results=%d", gotCalls, gotMaximum, len(gotResults))
	}
	for _, result := range gotResults {
		if !result.Evidence.Success || !result.Evidence.TimingFaithful || result.Observation == nil {
			t.Fatalf("result=%#v", result)
		}
	}
}

func TestVerificationCommandOutputRejectsDuplicateAndMismatchedEvidence(t *testing.T) {
	valid := []byte(`{"schema":"caveman.cachebench.verification.v1","request_id":"request-1","passed":true,"verifier":"fixture@v1","evidence":{"ok":true}}`)
	verification, err := ParseVerificationCommandOutput(valid, "request-1")
	if err != nil || !verification.Passed {
		t.Fatalf("verification=%#v err=%v", verification, err)
	}
	duplicate := bytes.Replace(valid, []byte(`{"schema":`), []byte(`{"schema":"duplicate","schema":`), 1)
	if _, err := ParseVerificationCommandOutput(duplicate, "request-1"); err == nil {
		t.Fatal("duplicate verifier key accepted")
	}
	if _, err := ParseVerificationCommandOutput(valid, "request-2"); err == nil {
		t.Fatal("mismatched verifier request accepted")
	}
}

func TestReplayEvidenceSummaryUsesSameValidatedPopulation(t *testing.T) {
	records := []ReplayEvidenceRecord{
		successfulReplayEvidence("openai", 10, true),
		successfulReplayEvidence("openai", 20, false),
		successfulReplayEvidence("anthropic", 100, true),
	}
	records[1].RequestID = "openai-request-2"
	summary, err := SummarizeReplayEvidence(records)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Requests != 3 || summary.Successful != 3 || summary.QualityPassed != 2 || summary.Latency.P50MS != 20 || summary.Latency.P95MS != 100 || summary.Latency.P99MS != 100 || summary.Latency.MaxMS != 100 || len(summary.Providers) != 2 || !summary.TimingFaithful || !summary.InputBudgetClaimedProviderCounted {
		t.Fatalf("summary = %#v", summary)
	}
	if summary.Providers[0].Provider != "anthropic" || summary.Providers[0].QualityPassed != 1 || summary.Providers[1].Provider != "openai" || summary.Providers[1].QualityPassed != 1 {
		t.Fatalf("provider summaries = %#v", summary.Providers)
	}
	records[0].ProviderEvidenceSHA256 = "invalid"
	if _, err := SummarizeReplayEvidence(records); err == nil {
		t.Fatal("invalid evidence summarized")
	}
}

func successfulReplayEvidence(provider string, latency int64, quality bool) ReplayEvidenceRecord {
	wireDigest := bodyDigest([]byte("body"))
	providerDigest := bodyDigest([]byte("provider"))
	usageDigest := bodyDigest([]byte("usage"))
	qualityDigest := bodyDigest([]byte("quality"))
	return ReplayEvidenceRecord{
		Schema: ReplayEvidenceSchema, RequestID: provider + "-request", TraceBodySHA256: wireDigest,
		WireBodySHA256: wireDigest, Provider: provider, Model: "model", Epoch: "epoch",
		TimingBasis: TimingGrounded, TokenBasis: TokenProviderCounted, TimeScale: 1, TimingFaithful: true,
		ScheduledAt: "2026-08-10T00:00:00Z", ScheduleDriftMilliseconds: 0, ScheduleToleranceMilliseconds: 1000,
		StartedAt: "2026-08-10T00:00:00Z", CompletedAt: "2026-08-10T00:00:01Z", LatencyMilliseconds: latency,
		HTTPStatus: 200, ProviderEvidenceSHA256: providerDigest, ProviderUsageSHA256: usageDigest,
		ProviderTotalInputTokens: 1000, QualityPassed: quality, QualityVerifier: "fixture@v1",
		QualityEvidenceSHA256: qualityDigest, Success: true,
		Applied: true, Decision: cacheengine.DecisionApply, Reason: cacheengine.ReasonApplied,
		Attribution: cacheengine.AttributionCausal, OptimizerIDs: []string{"fixture-cache-v1"},
	}
}

func TestHTTPReplayTransportBuildsOfficialProviderRequests(t *testing.T) {
	tests := []struct {
		provider, model, region, endpoint, wantPath, wantHeader, wantValue, response string
	}{
		{"openai", "gpt-5.6", "", "/v1/chat/completions", "/v1/chat/completions", "Authorization", "Bearer openai-secret", `{"usage":{"prompt_tokens":10,"prompt_tokens_details":{"cached_tokens":0,"cache_write_tokens":0}}}`},
		{"anthropic", "claude-sonnet-4-6", "", "/v1/messages", "/v1/messages", "x-api-key", "anthropic-secret", `{"usage":{"input_tokens":10,"cache_read_input_tokens":0,"cache_creation_input_tokens":0}}`},
		{"gemini", "gemini-2.5-pro", "", "generateContent", "/v1beta/models/gemini-2.5-pro:generateContent", "x-goog-api-key", "gemini-secret", `{"usageMetadata":{"promptTokenCount":10,"cachedContentTokenCount":0}}`},
		{"bedrock", "global.anthropic.claude-sonnet-4-6", "us-east-1", "converse", "/model/global.anthropic.claude-sonnet-4-6/converse", "Authorization", "Bearer bedrock-secret", `{"usage":{"inputTokens":10,"cacheReadInputTokens":0,"cacheWriteInputTokens":0}}`},
	}
	for _, test := range tests {
		t.Run(test.provider, func(t *testing.T) {
			client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
				if request.URL.Path != test.wantPath || request.Header.Get(test.wantHeader) != test.wantValue || request.Header.Get("content-type") != "application/json" {
					t.Fatalf("request URL=%s headers=%v", request.URL, request.Header)
				}
				return &http.Response{StatusCode: 200, Header: http.Header{"X-Request-Id": []string{"provider-id"}}, Body: io.NopCloser(strings.NewReader(test.response))}, nil
			})}
			transport, err := NewHTTPReplayTransport(HTTPReplayConfig{Client: client, Credentials: HTTPReplayCredentials{
				OpenAIAPIKey: "openai-secret", AnthropicAPIKey: "anthropic-secret", GeminiAPIKey: "gemini-secret", BedrockAPIKey: "bedrock-secret",
			}})
			if err != nil {
				t.Fatal(err)
			}
			response, err := transport.Send(context.Background(), ReplayOutbound{Provider: test.provider, Model: test.model, Region: test.region, Endpoint: test.endpoint, Body: []byte(`{"messages":[]}`)})
			if err != nil || response.StatusCode != 200 || response.ProviderRequestID != "provider-id" {
				t.Fatalf("response=%#v err=%v", response, err)
			}
		})
	}
}

func TestHTTPReplayTransportSignsBedrockAndBoundsResponses(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		auth := request.Header.Get("Authorization")
		if !strings.HasPrefix(auth, "AWS4-HMAC-SHA256 ") || strings.Contains(auth, "aws-secret") || request.Header.Get("X-Amz-Content-Sha256") == "" {
			t.Fatalf("unsafe or missing signature: %q", auth)
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("12345"))}, nil
	})}
	transport, err := NewHTTPReplayTransport(HTTPReplayConfig{
		Client: client, MaxResponseBytes: 4,
		Credentials: HTTPReplayCredentials{AWS: awssig.Credentials{AccessKeyID: "AKIAEXAMPLE", SecretAccessKey: "aws-secret"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	response, err := transport.Send(context.Background(), ReplayOutbound{Provider: "bedrock", Model: "model", Region: "us-east-1", Endpoint: "converse", Body: []byte(`{}`)})
	if err == nil || response.StatusCode != 200 || len(response.Body) != 0 || strings.Contains(err.Error(), "aws-secret") {
		t.Fatalf("response=%#v err=%v", response, err)
	}
}

func TestHTTPReplayTransportRejectsUnsafeBaseURLsAndRedirectCredentialForwarding(t *testing.T) {
	if _, err := NewHTTPReplayTransport(HTTPReplayConfig{BaseURLs: map[string]string{"openai": "http://127.0.0.1:8787"}}); err == nil {
		t.Fatal("insecure custom base accepted")
	}
	if _, err := NewHTTPReplayTransport(HTTPReplayConfig{BaseURLs: map[string]string{"openai": "https://user:secret@example.com"}}); err == nil {
		t.Fatal("userinfo base URL accepted")
	}
	calls := 0
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{
			StatusCode: 302, Header: http.Header{"Location": []string{"https://attacker.example/steal"}},
			Body: io.NopCloser(strings.NewReader(`{"error":"redirect"}`)),
		}, nil
	})}
	transport, err := NewHTTPReplayTransport(HTTPReplayConfig{Client: client, Credentials: HTTPReplayCredentials{OpenAIAPIKey: "must-not-forward"}})
	if err != nil {
		t.Fatal(err)
	}
	response, err := transport.Send(context.Background(), ReplayOutbound{Provider: "openai", Model: "gpt-5.6", Endpoint: "/v1/chat/completions", Body: []byte(`{}`)})
	if err != nil || response.StatusCode != 302 || calls != 1 {
		t.Fatalf("response=%#v calls=%d err=%v", response, calls, err)
	}
}

func TestHTTPReplayTransportEnforcesRequestTimeout(t *testing.T) {
	if _, err := NewHTTPReplayTransport(HTTPReplayConfig{RequestTimeout: 500 * time.Millisecond}); err == nil {
		t.Fatal("sub-second request timeout accepted")
	}
	transport, err := NewHTTPReplayTransport(HTTPReplayConfig{Client: &http.Client{}})
	if err != nil || transport.client.Timeout != 2*time.Minute {
		t.Fatalf("default timeout=%v err=%v", transport.client.Timeout, err)
	}
	strictClient := &http.Client{Timeout: 30 * time.Second}
	transport, err = NewHTTPReplayTransport(HTTPReplayConfig{Client: strictClient})
	if err != nil || transport.client.Timeout != 30*time.Second || strictClient.Timeout != 30*time.Second {
		t.Fatalf("strict timeout=%v original=%v err=%v", transport.client.Timeout, strictClient.Timeout, err)
	}
	transport, err = NewHTTPReplayTransport(HTTPReplayConfig{Client: strictClient, RequestTimeout: 3 * time.Minute})
	if err != nil || transport.client.Timeout != 3*time.Minute || strictClient.Timeout != 30*time.Second {
		t.Fatalf("explicit timeout=%v original=%v err=%v", transport.client.Timeout, strictClient.Timeout, err)
	}
}

func TestHTTPReplayTransportBoundsRequestsAndRejectsControlCharacters(t *testing.T) {
	calls := 0
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls++
		return nil, errors.New("must not send")
	})}
	transport, err := NewHTTPReplayTransport(HTTPReplayConfig{
		Client: client, MaxRequestBytes: 2,
		Credentials: HTTPReplayCredentials{OpenAIAPIKey: "secret"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := transport.Send(context.Background(), ReplayOutbound{Provider: "openai", Model: "gpt-5.6", Endpoint: "/v1/responses", Body: []byte(`{}` + "x")}); err == nil || calls != 0 {
		t.Fatalf("oversized request sent calls=%d err=%v", calls, err)
	}
	transport, err = NewHTTPReplayTransport(HTTPReplayConfig{Client: client, Credentials: HTTPReplayCredentials{OpenAIAPIKey: "secret\x00value"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := transport.Send(context.Background(), ReplayOutbound{Provider: "openai", Model: "gpt-5.6", Endpoint: "/v1/responses", Body: []byte(`{}`)}); err == nil || calls != 0 {
		t.Fatalf("control-character secret sent calls=%d err=%v", calls, err)
	}
	if _, err := transport.Send(context.Background(), ReplayOutbound{Provider: "openai", Model: "gpt-5.6\x00", Endpoint: "/v1/responses", Body: []byte(`{}`)}); err == nil || calls != 0 {
		t.Fatalf("control-character model sent calls=%d err=%v", calls, err)
	}
	if _, err := transport.Send(context.Background(), ReplayOutbound{Provider: "openai", Model: "gpt-5.6", Endpoint: "/v1/responses", Body: []byte(`{"model":"gpt-5.6","model":"other"}`)}); err == nil || calls != 0 {
		t.Fatalf("duplicate-key request sent calls=%d err=%v", calls, err)
	}
}

func TestTraceAndReplayEvidenceRejectControlCharacters(t *testing.T) {
	records := replayTraceRecords(t, 2, 8192)
	var raw bytes.Buffer
	bad := records[0]
	bad.RequestID = "request\u0000id"
	if err := json.NewEncoder(&raw).Encode(bad); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadTraceJSONL(&raw); err == nil {
		t.Fatal("trace control character accepted")
	}
	evidence := successfulReplayEvidence("openai", 1000, true)
	evidence.ProviderRequestID = "provider\u0000id"
	if err := validateReplayEvidence(evidence); err == nil {
		t.Fatal("provider request ID control character accepted")
	}
	verification := TaskVerification{Passed: true, Verifier: "fixture\u0000v1", Evidence: []byte(`{"passed":true}`)}
	if validTaskVerification(verification) {
		t.Fatal("verifier control character accepted")
	}
}

func TestTraceReaderEnforcesRecordAndBodyLimitsBeforeReplay(t *testing.T) {
	records := replayTraceRecords(t, 2, 8192)
	var raw bytes.Buffer
	for _, record := range records {
		if err := json.NewEncoder(&raw).Encode(record); err != nil {
			t.Fatal(err)
		}
	}
	limits := DefaultTraceReadLimits()
	limits.MaxRecords = 1
	if _, err := ReadTraceJSONLWithLimits(bytes.NewReader(raw.Bytes()), limits); err == nil {
		t.Fatal("trace record limit ignored")
	}
	limits = DefaultTraceReadLimits()
	limits.MaxBodyBytes = len(records[0].Body) - 1
	if _, err := ReadTraceJSONLWithLimits(bytes.NewReader(raw.Bytes()), limits); err == nil {
		t.Fatal("trace body limit ignored")
	}
}

func FuzzTraceJSONLFailClosed(f *testing.F) {
	records := replayTraceRecords(f, 2, 8192)
	var valid bytes.Buffer
	for _, record := range records {
		_ = json.NewEncoder(&valid).Encode(record)
	}
	f.Add(valid.Bytes())
	f.Add([]byte(`{"schema":`))
	f.Add([]byte{0, 1, 2, 3})
	f.Fuzz(func(t *testing.T, raw []byte) {
		parsed, err := ReadTraceJSONL(bytes.NewReader(raw))
		if err != nil {
			return
		}
		for _, record := range parsed {
			if _, err := record.NativeRequest(); err != nil {
				t.Fatalf("accepted unreplayable trace: %#v, err=%v", record, err)
			}
		}
	})
}

func FuzzVerificationCommandOutputFailClosed(f *testing.F) {
	f.Add([]byte(`{"schema":"caveman.cachebench.verification.v1","request_id":"request-1","passed":true,"verifier":"fixture@v1","evidence":{"ok":true}}`))
	f.Add([]byte(`{"schema":`))
	f.Add([]byte{0, 1, 2, 3})
	f.Fuzz(func(t *testing.T, raw []byte) {
		verification, err := ParseVerificationCommandOutput(raw, "request-1")
		if err == nil && (verification.Verifier == "" || len(verification.Evidence) == 0 || !validUniqueJSONObject(verification.Evidence)) {
			t.Fatalf("accepted invalid verification: %#v", verification)
		}
	})
}

func replayTraceRecords(t interface {
	Helper()
	Fatal(...any)
}, turns, staticTokens int) []TraceRecord {
	t.Helper()
	scenario := DefaultScenario()
	scenario.Turns = turns
	scenario.CompactionEvery = 0
	scenario.StaticTokens = staticTokens
	trace, err := GenerateTrace(DefaultProviders()[1], scenario)
	if err != nil {
		t.Fatal(err)
	}
	var raw bytes.Buffer
	if err := WriteTraceJSONL(&raw, trace); err != nil {
		t.Fatal(err)
	}
	records, err := ReadTraceJSONL(&raw)
	if err != nil {
		t.Fatal(err)
	}
	return records
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return fn(request) }
