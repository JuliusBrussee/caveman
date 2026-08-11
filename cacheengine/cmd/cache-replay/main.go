package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/JuliusBrussee/caveman/cacheengine"
	"github.com/JuliusBrussee/caveman/cacheengine/cachebench"
	"github.com/JuliusBrussee/caveman/shared/platform/awssig"
)

const (
	manifestSchema         = "caveman.cachebench.replay-run.v1"
	maxReplayTraceBytes    = 512 << 20
	maxReplayInflightBytes = 1 << 30
	maxReplayRequests      = 100_000
)

type stringValues []string

func (values *stringValues) String() string { return strings.Join(*values, ",") }
func (values *stringValues) Set(value string) error {
	*values = append(*values, value)
	return nil
}

type baseURLValues map[string]string

func (values *baseURLValues) String() string {
	keys := make([]string, 0, len(*values))
	for key := range *values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return strings.Join(keys, ",")
}

func (values *baseURLValues) Set(value string) error {
	provider, raw, ok := strings.Cut(value, "=")
	provider = strings.ToLower(strings.TrimSpace(provider))
	if !ok || provider == "" || strings.TrimSpace(raw) == "" {
		return errors.New("want provider=https://host")
	}
	if *values == nil {
		*values = map[string]string{}
	}
	if _, exists := (*values)[provider]; exists {
		return fmt.Errorf("duplicate provider %q", provider)
	}
	(*values)[provider] = strings.TrimSpace(raw)
	return nil
}

type runManifest struct {
	Schema        string                            `json:"schema"`
	Status        string                            `json:"status"`
	Publishable   bool                              `json:"publishable"`
	TraceSHA256   string                            `json:"trace_sha256"`
	StartedAt     string                            `json:"started_at"`
	CompletedAt   string                            `json:"completed_at,omitempty"`
	Preflight     cachebench.ReplayPreflight        `json:"preflight"`
	Target        cachebench.Target                 `json:"target"`
	Completed     int                               `json:"completed_requests"`
	FailedRequest string                            `json:"failed_request,omitempty"`
	FailureCode   string                            `json:"failure_code,omitempty"`
	EvidenceBasis string                            `json:"evidence_basis"`
	ReplaySummary *cachebench.ReplayEvidenceSummary `json:"replay_summary,omitempty"`
}

type commandVerifier struct {
	path           string
	args           []string
	environment    []string
	maxOutputBytes int64
	timeout        time.Duration
}

func (verifier commandVerifier) Verify(ctx context.Context, input cachebench.ReplayVerificationInput) (cachebench.TaskVerification, error) {
	verificationInput := cachebench.VerificationCommandInput{
		Schema: cachebench.VerificationSchema, RequestID: input.Trace.RequestID,
		Provider: input.Trace.Provider, Model: input.Trace.Model,
		TraceBodySHA256: input.Trace.BodySHA256, WireBodySHA256: digest(input.Optimized.Body),
		OriginalRequest: input.Trace.Body, OptimizedRequest: input.Optimized.Body,
		ProviderResponse: input.Response.Body,
	}
	verifyCtx, cancel := context.WithTimeout(ctx, verifier.timeout)
	defer cancel()
	command := exec.CommandContext(verifyCtx, verifier.path, verifier.args...)
	inputReader, inputWriter := io.Pipe()
	encodeDone := make(chan error, 1)
	go func() {
		err := json.NewEncoder(inputWriter).Encode(verificationInput)
		_ = inputWriter.CloseWithError(err)
		encodeDone <- err
	}()
	command.Stdin = inputReader
	command.Env = append([]string(nil), verifier.environment...)
	stdout := &boundedBuffer{max: verifier.maxOutputBytes}
	stderr := &boundedBuffer{max: 64 << 10}
	command.Stdout = stdout
	command.Stderr = stderr
	runErr := command.Run()
	_ = inputReader.Close()
	encodeErr := <-encodeDone
	if runErr != nil {
		return cachebench.TaskVerification{}, errors.New("cache-replay: verifier process failed")
	}
	if encodeErr != nil {
		return cachebench.TaskVerification{}, errors.New("cache-replay: could not encode verifier input")
	}
	return cachebench.ParseVerificationCommandOutput(stdout.Bytes(), input.Trace.RequestID)
}

type boundedBuffer struct {
	buffer bytes.Buffer
	max    int64
}

func (buffer *boundedBuffer) Write(value []byte) (int, error) {
	if int64(buffer.buffer.Len())+int64(len(value)) > buffer.max {
		remaining := buffer.max - int64(buffer.buffer.Len())
		if remaining > 0 {
			written, _ := buffer.buffer.Write(value[:remaining])
			return written, errors.New("output limit exceeded")
		}
		return 0, errors.New("output limit exceeded")
	}
	return buffer.buffer.Write(value)
}

func (buffer *boundedBuffer) Bytes() []byte { return buffer.buffer.Bytes() }

type replaySink struct {
	root         string
	evidence     []cachebench.ReplayEvidenceRecord
	observations []cachebench.ObservationRecord
}

func (sink *replaySink) emit(result cachebench.ReplayResult) error {
	name := digest([]byte(result.Evidence.RequestID))
	if len(result.ProviderResponse) > 0 {
		if err := atomicWrite(filepath.Join(sink.root, "responses", name+".json"), result.ProviderResponse); err != nil {
			return err
		}
	}
	if len(result.VerificationEvidence) > 0 {
		if err := atomicWrite(filepath.Join(sink.root, "quality", name+".json"), result.VerificationEvidence); err != nil {
			return err
		}
	}
	var encoded bytes.Buffer
	if err := cachebench.WriteReplayEvidenceJSON(&encoded, result.Evidence); err != nil {
		return err
	}
	if err := atomicWrite(filepath.Join(sink.root, "evidence", name+".json"), encoded.Bytes()); err != nil {
		return err
	}
	sink.evidence = append(sink.evidence, result.Evidence)
	if result.Observation != nil {
		encoded.Reset()
		if err := json.NewEncoder(&encoded).Encode(result.Observation); err != nil {
			return err
		}
		if err := atomicWrite(filepath.Join(sink.root, "observations", name+".json"), encoded.Bytes()); err != nil {
			return err
		}
		sink.observations = append(sink.observations, *result.Observation)
	}
	return nil
}

func main() {
	var verifierArgs, verifierEnv stringValues
	baseURLs := baseURLValues{}
	tracePath := flag.String("trace", "", "v3 trace JSONL to preflight or replay")
	outputPath := flag.String("output", "", "new private evidence directory; required with -execute")
	executeReplay := flag.Bool("execute", false, "send paid provider requests after preflight")
	acceptCost := flag.Bool("accept-live-cost", false, "confirm live provider traffic can incur cost")
	maxRequests := flag.Int("max-requests", 0, "hard maximum provider requests; required")
	maxTokens := flag.Int64("max-declared-billed-tokens", 0, "maximum declared total input plus request-capped output tokens; required")
	maxTraceBytes := flag.Int64("max-trace-bytes", 256<<20, "hard trace file byte limit")
	maxGap := flag.Duration("max-gap", 10*time.Minute, "reject any scaled inter-request gap above this")
	maxScheduleDrift := flag.Duration("max-schedule-drift", 250*time.Millisecond, "abort grounded replay when request start drifts beyond this")
	maxConcurrency := flag.Int("max-concurrency", 1, "maximum concurrent replay request lifecycles (1-1024)")
	timeScale := flag.Float64("time-scale", 1, "trace timing multiplier; 1 preserves grounded timing")
	allowUngrounded := flag.Bool("allow-ungrounded-timing", false, "permit synthetic/per-partition schedules; evidence is not timing-faithful")
	allowEstimatedTokens := flag.Bool("allow-estimated-token-budget", false, "permit non-provider token estimates; input ceiling is not provider-grounded")
	maxResponseBytes := flag.Int64("max-response-bytes", 16<<20, "maximum retained provider response bytes/request")
	providerTimeout := flag.Duration("provider-timeout", 2*time.Minute, "hard timeout per provider request (1s-1h)")
	verifierPath := flag.String("verifier-command", "", "task verifier executable; receives one JSON object on stdin")
	verifierOutputBytes := flag.Int64("max-verifier-output-bytes", 16<<20, "maximum verifier JSON bytes/request")
	targetRate := flag.Float64("target", .97, "required request and token cache-hit rate")
	minEligible := flag.Int("min-requests", 100, "minimum eligible requests/provider")
	allowInsecureLoopback := flag.Bool("allow-insecure-loopback", false, "allow HTTP only for explicit loopback test base URLs")
	allowCustomBaseURL := flag.Bool("allow-custom-base-url", false, "confirm credentials may be sent to explicit custom HTTPS base URLs")
	verifierTimeout := flag.Duration("verifier-timeout", 5*time.Minute, "hard timeout per task-verifier invocation")
	flag.Var(&verifierArgs, "verifier-arg", "verifier argument; repeatable, no shell parsing")
	flag.Var(&verifierEnv, "verifier-env", "environment variable exposed to verifier; repeatable")
	flag.Var(&baseURLs, "base-url", "test/custom provider base URL as provider=https://host; repeatable")
	flag.Parse()

	if *tracePath == "" || !filepath.IsAbs(*tracePath) || *maxRequests <= 0 || *maxRequests > maxReplayRequests || *maxTokens <= 0 || *maxTraceBytes <= 0 || *maxTraceBytes > maxReplayTraceBytes || *maxResponseBytes <= 0 || *maxResponseBytes > 256<<20 || *providerTimeout < time.Second || *providerTimeout > time.Hour || *verifierOutputBytes <= 0 || *verifierOutputBytes > 256<<20 || *verifierTimeout <= 0 || *maxScheduleDrift <= 0 || *maxConcurrency <= 0 || *maxConcurrency > 1024 {
		fatalConfig(errors.New("cache-replay: -trace and positive request/token/trace/response/verifier limits required"))
	}
	if (*maxResponseBytes+*verifierOutputBytes)*int64(*maxConcurrency) > maxReplayInflightBytes {
		fatalConfig(fmt.Errorf("cache-replay: concurrent response and verifier buffers exceed %d bytes", maxReplayInflightBytes))
	}
	if len(baseURLs) > 0 && !*allowCustomBaseURL {
		fatalConfig(errors.New("cache-replay: custom base URLs require -allow-custom-base-url"))
	}
	records, traceSHA, err := readTrace(*tracePath, *maxTraceBytes, *maxRequests)
	if err != nil {
		fatalConfig(err)
	}
	limits := cachebench.ReplayLimits{
		MaxRequests: *maxRequests, MaxDeclaredBilledTokens: *maxTokens, MaxGap: *maxGap, MaxScheduleDrift: *maxScheduleDrift,
		MaxConcurrency:        *maxConcurrency,
		RequireGroundedTiming: !*allowUngrounded, RequireProviderTokens: !*allowEstimatedTokens,
	}
	preflight, err := cachebench.ValidateReplay(records, limits, *timeScale)
	if err != nil {
		fatalConfig(err)
	}
	target := cachebench.Target{RequestHitRate: *targetRate, TokenHitRate: *targetRate, MinEligibleRequest: *minEligible}
	if err := cachebench.ValidateReplayTarget(records, target); err != nil {
		fatalConfig(err)
	}
	if !*executeReplay {
		writeStdout(map[string]any{
			"schema": "caveman.cachebench.replay-preflight.v1", "execute": false,
			"trace_sha256": traceSHA, "preflight": preflight, "target": target,
			"message": "preflight only; no provider request sent",
		})
		return
	}
	if !*acceptCost || *outputPath == "" || !filepath.IsAbs(*outputPath) || *verifierPath == "" || !filepath.IsAbs(*verifierPath) {
		fatalConfig(errors.New("cache-replay: -execute requires -accept-live-cost, -output, and -verifier-command"))
	}
	verifierInfo, err := os.Stat(*verifierPath)
	if err != nil || !verifierInfo.Mode().IsRegular() {
		fatalConfig(errors.New("cache-replay: verifier command must be an existing regular file"))
	}
	if err := validateProviderCredentials(records); err != nil {
		fatalConfig(err)
	}
	environment, err := verifierEnvironment(verifierEnv)
	if err != nil {
		fatalConfig(err)
	}
	transport, err := cachebench.NewHTTPReplayTransport(cachebench.HTTPReplayConfig{
		Credentials: cachebench.HTTPReplayCredentials{
			OpenAIAPIKey: os.Getenv("OPENAI_API_KEY"), AnthropicAPIKey: os.Getenv("ANTHROPIC_API_KEY"),
			GeminiAPIKey: os.Getenv("GEMINI_API_KEY"), BedrockAPIKey: os.Getenv("AWS_BEARER_TOKEN_BEDROCK"),
			AWS: awssig.Credentials{AccessKeyID: os.Getenv("AWS_ACCESS_KEY_ID"), SecretAccessKey: os.Getenv("AWS_SECRET_ACCESS_KEY"), SessionToken: os.Getenv("AWS_SESSION_TOKEN")},
		},
		BaseURLs: map[string]string(baseURLs), AllowInsecureLoopback: *allowInsecureLoopback,
		MaxResponseBytes: *maxResponseBytes, RequestTimeout: *providerTimeout,
	})
	if err != nil {
		fatalConfig(err)
	}
	if err := createEvidenceDirectory(*outputPath); err != nil {
		fatalConfig(err)
	}
	manifest := runManifest{
		Schema: manifestSchema, Status: "running", Publishable: false, TraceSHA256: traceSHA,
		StartedAt: time.Now().UTC().Format(time.RFC3339Nano), Preflight: preflight, Target: target,
		EvidenceBasis: "provider_observed; retained responses and external task-verifier artifacts; never verified savings",
	}
	if err := writeJSON(filepath.Join(*outputPath, "manifest.json"), manifest); err != nil {
		fatalRuntime(err)
	}
	sink := &replaySink{root: *outputPath}
	verifier := commandVerifier{path: *verifierPath, args: verifierArgs, environment: environment, maxOutputBytes: *verifierOutputBytes, timeout: *verifierTimeout}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	runner := cachebench.ReplayRunner{
		Engine: cacheengine.New(cacheengine.Config{}), Transport: transport, Verifier: verifier,
		Limits: limits, Target: &target, TimeScale: *timeScale,
	}
	runErr := runner.Run(ctx, records, sink.emit)
	manifest.Completed = len(sink.evidence)
	manifest.CompletedAt = time.Now().UTC().Format(time.RFC3339Nano)
	if err := writeJSONL(filepath.Join(*outputPath, "evidence.jsonl"), sink.evidence); err != nil {
		fatalRuntime(err)
	}
	if err := writeJSONL(filepath.Join(*outputPath, "observations.jsonl"), sink.observations); err != nil {
		fatalRuntime(err)
	}
	if len(sink.evidence) > 0 {
		summary, summaryErr := cachebench.SummarizeReplayEvidence(sink.evidence)
		if summaryErr != nil {
			fatalRuntime(summaryErr)
		}
		manifest.ReplaySummary = &summary
		if err := writeJSON(filepath.Join(*outputPath, "replay-summary.json"), summary); err != nil {
			fatalRuntime(err)
		}
	}
	if runErr != nil {
		manifest.Status = "failed"
		var replayFailure *cachebench.ReplayRunError
		if errors.As(runErr, &replayFailure) {
			manifest.FailedRequest, manifest.FailureCode = replayFailure.RequestID, replayFailure.FailureCode
		} else if len(sink.evidence) > 0 {
			last := sink.evidence[len(sink.evidence)-1]
			manifest.FailedRequest, manifest.FailureCode = last.RequestID, last.FailureCode
		}
		if err := writeJSON(filepath.Join(*outputPath, "manifest.json"), manifest); err != nil {
			fatalRuntime(fmt.Errorf("cache-replay: failed to finalize failure manifest: %w", err))
		}
		fatalRuntime(runErr)
	}
	report, err := cachebench.EvaluateObservedAgainstTrace(sink.observations, records, target)
	if err != nil {
		fatalRuntime(err)
	}
	if err := writeJSON(filepath.Join(*outputPath, "report.json"), report); err != nil {
		fatalRuntime(err)
	}
	manifest.Status = report.Status
	if err := writeJSON(filepath.Join(*outputPath, "manifest.json"), manifest); err != nil {
		fatalRuntime(err)
	}
	writeStdout(map[string]any{"manifest": manifest, "report": report.Overall})
	if !report.Overall.GatePassed {
		os.Exit(1)
	}
}

func readTrace(path string, maxBytes int64, maxRecords int) ([]cachebench.TraceRecord, string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, "", err
	}
	defer file.Close()
	hash := sha256.New()
	limited := &io.LimitedReader{R: file, N: maxBytes + 1}
	readLimits := cachebench.DefaultTraceReadLimits()
	readLimits.MaxLineBytes = int(maxBytes)
	readLimits.MaxRecords = maxRecords
	records, err := cachebench.ReadTraceJSONLWithLimits(io.TeeReader(limited, hash), readLimits)
	if err != nil {
		return nil, "", err
	}
	if limited.N <= 0 {
		return nil, "", fmt.Errorf("cache-replay: trace exceeds %d bytes", maxBytes)
	}
	return records, hex.EncodeToString(hash.Sum(nil)), nil
}

func validateProviderCredentials(records []cachebench.TraceRecord) error {
	providers := map[string]bool{}
	for _, record := range records {
		providers[record.Provider] = true
	}
	for provider := range providers {
		switch provider {
		case "openai":
			if os.Getenv("OPENAI_API_KEY") == "" {
				return errors.New("cache-replay: OPENAI_API_KEY unavailable")
			}
		case "anthropic":
			if os.Getenv("ANTHROPIC_API_KEY") == "" {
				return errors.New("cache-replay: ANTHROPIC_API_KEY unavailable")
			}
		case "gemini":
			if os.Getenv("GEMINI_API_KEY") == "" {
				return errors.New("cache-replay: GEMINI_API_KEY unavailable")
			}
		case "bedrock":
			if os.Getenv("AWS_BEARER_TOKEN_BEDROCK") == "" && (os.Getenv("AWS_ACCESS_KEY_ID") == "" || os.Getenv("AWS_SECRET_ACCESS_KEY") == "") {
				return errors.New("cache-replay: Bedrock bearer token or AWS access credentials unavailable")
			}
		default:
			return fmt.Errorf("cache-replay: unsupported provider %q", provider)
		}
	}
	return nil
}

func verifierEnvironment(extra []string) ([]string, error) {
	allowed := map[string]bool{"PATH": true, "LANG": true, "LC_ALL": true, "TMPDIR": true}
	blocked := map[string]bool{
		"OPENAI_API_KEY": true, "ANTHROPIC_API_KEY": true, "GEMINI_API_KEY": true,
		"AWS_BEARER_TOKEN_BEDROCK": true, "AWS_ACCESS_KEY_ID": true, "AWS_SECRET_ACCESS_KEY": true, "AWS_SESSION_TOKEN": true,
	}
	for _, name := range extra {
		name = strings.TrimSpace(name)
		if !validEnvironmentName(name) || blocked[name] {
			return nil, fmt.Errorf("cache-replay: verifier environment variable %q is invalid or credential-bearing", name)
		}
		allowed[name] = true
	}
	names := make([]string, 0, len(allowed))
	for name := range allowed {
		names = append(names, name)
	}
	sort.Strings(names)
	result := make([]string, 0, len(names))
	for _, name := range names {
		if value, ok := os.LookupEnv(name); ok {
			result = append(result, name+"="+value)
		}
	}
	return result, nil
}

func validEnvironmentName(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for index, char := range value {
		if char == '_' || char >= 'A' && char <= 'Z' || index > 0 && char >= '0' && char <= '9' {
			continue
		}
		return false
	}
	return true
}

func createEvidenceDirectory(path string) error {
	if err := os.Mkdir(path, 0o700); err != nil {
		return fmt.Errorf("cache-replay: output directory must be new: %w", err)
	}
	for _, name := range []string{"responses", "quality", "evidence", "observations"} {
		if err := os.Mkdir(filepath.Join(path, name), 0o700); err != nil {
			return err
		}
	}
	return nil
}

func atomicWrite(path string, body []byte) error {
	return atomicWriteStream(path, func(writer io.Writer) error {
		_, err := writer.Write(body)
		return err
	})
}

func atomicWriteStream(path string, write func(io.Writer) error) error {
	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, ".cache-replay-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	clean := func() { _ = os.Remove(temporaryPath) }
	defer clean()
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := write(temporary); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	if runtime.GOOS == "windows" {
		// Windows cannot fsync a directory handle (Access is denied); the
		// rename above is already atomic on NTFS.
		return nil
	}
	dir, err := os.Open(directory)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func writeJSON(path string, value any) error {
	return atomicWriteStream(path, func(writer io.Writer) error {
		encoder := json.NewEncoder(writer)
		encoder.SetEscapeHTML(false)
		encoder.SetIndent("", "  ")
		return encoder.Encode(value)
	})
}

func writeJSONL(path string, values any) error {
	return atomicWriteStream(path, func(writer io.Writer) error {
		encoder := json.NewEncoder(writer)
		encoder.SetEscapeHTML(false)
		switch typed := values.(type) {
		case []cachebench.ReplayEvidenceRecord:
			for _, value := range typed {
				if err := encoder.Encode(value); err != nil {
					return err
				}
			}
		case []cachebench.ObservationRecord:
			for _, value := range typed {
				if err := encoder.Encode(value); err != nil {
					return err
				}
			}
		default:
			return errors.New("cache-replay: unsupported JSONL value")
		}
		return nil
	})
}

func writeStdout(value any) {
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		fatalRuntime(err)
	}
}

func digest(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}

func fatalConfig(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(2)
}

func fatalRuntime(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(3)
}
