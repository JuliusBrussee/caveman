package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/JuliusBrussee/caveman/cacheengine"
	"github.com/JuliusBrussee/caveman/cacheengine/cachebench"
)

func TestCommandVerifierUsesStructuredNoShellProtocol(t *testing.T) {
	if os.Getenv("CACHE_REPLAY_VERIFIER_HELPER") == "1" {
		if os.Getenv("CACHE_REPLAY_VERIFIER_NO_READ") == "1" {
			os.Exit(0)
		}
		var input cachebench.VerificationCommandInput
		if err := json.NewDecoder(os.Stdin).Decode(&input); err != nil {
			os.Exit(4)
		}
		_ = json.NewEncoder(os.Stdout).Encode(cachebench.VerificationCommandOutput{
			Schema: cachebench.VerificationSchema, RequestID: input.RequestID, Passed: true,
			Verifier: "helper@v1", Evidence: json.RawMessage(`{"task":"passed"}`),
		})
		os.Exit(0)
	}
	body := json.RawMessage(`{"model":"gpt-5.6","messages":[]}`)
	verifier := commandVerifier{
		path: os.Args[0], args: []string{"-test.run=^TestCommandVerifierUsesStructuredNoShellProtocol$"},
		environment: []string{"CACHE_REPLAY_VERIFIER_HELPER=1"}, maxOutputBytes: 1 << 20, timeout: time.Minute,
	}
	verification, err := verifier.Verify(context.Background(), cachebench.ReplayVerificationInput{
		Trace:     cachebench.TraceRecord{RequestID: "request-1", Provider: "openai", Model: "gpt-5.6", Body: body, BodySHA256: digest(body)},
		Optimized: cacheengine.NativeResult{Body: body},
		Response:  cachebench.ReplayResponse{StatusCode: 200, Body: json.RawMessage(`{"usage":{}}`)},
	})
	if err != nil || !verification.Passed || verification.Verifier != "helper@v1" || !json.Valid(verification.Evidence) {
		t.Fatalf("verification=%#v err=%v", verification, err)
	}
}

func TestCommandVerifierStreamingInputDoesNotDeadlockWhenChildExits(t *testing.T) {
	largeText, _ := json.Marshal(strings.Repeat("x", 1<<20))
	verifier := commandVerifier{
		path: os.Args[0], args: []string{"-test.run=^TestCommandVerifierUsesStructuredNoShellProtocol$"},
		environment:    []string{"CACHE_REPLAY_VERIFIER_HELPER=1", "CACHE_REPLAY_VERIFIER_NO_READ=1"},
		maxOutputBytes: 1 << 20, timeout: 5 * time.Second,
	}
	started := time.Now()
	_, err := verifier.Verify(context.Background(), cachebench.ReplayVerificationInput{
		Trace: cachebench.TraceRecord{
			RequestID: "request-no-read", Provider: "openai", Model: "gpt-5.6",
			Body: largeText, BodySHA256: digest(largeText),
		},
		Optimized: cacheengine.NativeResult{Body: largeText},
		Response:  cachebench.ReplayResponse{StatusCode: 200, Body: json.RawMessage(`{"usage":{}}`)},
	})
	if err == nil || time.Since(started) >= 5*time.Second {
		t.Fatalf("err=%v elapsed=%s", err, time.Since(started))
	}
}

func TestVerifierEnvironmentExcludesProviderCredentials(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "must-not-leak")
	t.Setenv("SAFE_GRADER_CONFIG", "fixture")
	environment, err := verifierEnvironment([]string{"SAFE_GRADER_CONFIG"})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(environment, "\n")
	if strings.Contains(joined, "must-not-leak") || !strings.Contains(joined, "SAFE_GRADER_CONFIG=fixture") {
		t.Fatalf("environment = %q", joined)
	}
	if _, err := verifierEnvironment([]string{"OPENAI_API_KEY"}); err == nil {
		t.Fatal("provider credential exposed to verifier")
	}
}

func TestEvidenceDirectoryAndAtomicWritesStayPrivate(t *testing.T) {
	root := filepath.Join(t.TempDir(), "evidence")
	if err := createEvidenceDirectory(root); err != nil {
		t.Fatal(err)
	}
	if err := createEvidenceDirectory(root); err == nil {
		t.Fatal("existing evidence directory accepted")
	}
	path := filepath.Join(root, "responses", "response.json")
	if err := atomicWrite(path, []byte(`{"ok":true}`)); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	// POSIX permission bits are synthetic on Windows; NTFS ACLs govern there.
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("response mode = %o", info.Mode().Perm())
	}
	raw, _ := os.ReadFile(path)
	if !bytes.Equal(raw, []byte(`{"ok":true}`)) {
		t.Fatalf("response = %q", raw)
	}
}

func TestBoundedBufferFailsClosed(t *testing.T) {
	buffer := &boundedBuffer{max: 4}
	if written, err := buffer.Write([]byte("123")); err != nil || written != 3 {
		t.Fatalf("written=%d err=%v", written, err)
	}
	if written, err := buffer.Write([]byte("45")); err == nil || written != 1 || string(buffer.Bytes()) != "1234" {
		t.Fatalf("written=%d buffer=%q err=%v", written, buffer.Bytes(), err)
	}
	if written, err := buffer.Write([]byte("6")); err == nil || written != 0 || string(buffer.Bytes()) != "1234" {
		t.Fatalf("written=%d buffer=%q err=%v", written, buffer.Bytes(), err)
	}
}

func TestCacheReplayCommandEndToEnd(t *testing.T) {
	if os.Getenv("CACHE_REPLAY_E2E_HELPER") == "1" {
		flag.CommandLine = flag.NewFlagSet("cache-replay", flag.ExitOnError)
		os.Args = []string{
			"cache-replay",
			"-trace", os.Getenv("CACHE_REPLAY_E2E_TRACE"),
			"-output", os.Getenv("CACHE_REPLAY_E2E_OUTPUT"),
			"-max-requests", "2",
			"-max-declared-billed-tokens", "30000",
			"-max-concurrency", "2",
			"-min-requests", "2",
			"-execute",
			"-accept-live-cost",
			"-verifier-command", os.Args[0],
			"-verifier-arg", "-test.run=^TestCommandVerifierUsesStructuredNoShellProtocol$",
			"-verifier-env", "CACHE_REPLAY_VERIFIER_HELPER",
			"-base-url", "openai=" + os.Getenv("CACHE_REPLAY_E2E_BASE_URL"),
			"-allow-custom-base-url",
			"-allow-insecure-loopback",
		}
		main()
		return
	}

	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.Header.Get("Authorization") != "Bearer e2e-secret" {
			http.Error(writer, "unauthorized", http.StatusUnauthorized)
			return
		}
		calls.Add(1)
		writer.Header().Set("x-request-id", fmt.Sprintf("provider-%d", calls.Load()))
		writer.Header().Set("content-type", "application/json")
		_, _ = writer.Write([]byte(`{"id":"response","usage":{"prompt_tokens":8000,"completion_tokens":8,"prompt_tokens_details":{"cached_tokens":7900,"cache_write_tokens":100}}}`))
	}))
	defer server.Close()

	scenario := cachebench.DefaultScenario()
	scenario.Turns = 2
	scenario.CompactionEvery = 0
	trace, err := cachebench.GenerateTrace(cachebench.DefaultProviders()[1], scenario)
	if err != nil {
		t.Fatal(err)
	}
	trace.TokenBasis = cachebench.TokenProviderCounted
	trace.TimingBasis = cachebench.TimingGrounded
	trace.Requests[1].At = trace.Requests[0].At
	root := t.TempDir()
	tracePath := filepath.Join(root, "trace.jsonl")
	traceFile, err := os.OpenFile(tracePath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := cachebench.WriteTraceJSONL(traceFile, trace); err != nil {
		_ = traceFile.Close()
		t.Fatal(err)
	}
	if err := traceFile.Close(); err != nil {
		t.Fatal(err)
	}
	outputPath := filepath.Join(root, "evidence")
	command := exec.Command(os.Args[0], "-test.run=^TestCacheReplayCommandEndToEnd$")
	command.Env = append(os.Environ(),
		"CACHE_REPLAY_E2E_HELPER=1",
		"CACHE_REPLAY_E2E_TRACE="+tracePath,
		"CACHE_REPLAY_E2E_OUTPUT="+outputPath,
		"CACHE_REPLAY_E2E_BASE_URL="+server.URL,
		"CACHE_REPLAY_VERIFIER_HELPER=1",
		"OPENAI_API_KEY=e2e-secret",
	)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("cache-replay failed: %v\n%s", err, output)
	}
	var manifest runManifest
	raw, err := os.ReadFile(filepath.Join(outputPath, "manifest.json"))
	if err != nil || json.Unmarshal(raw, &manifest) != nil {
		t.Fatalf("manifest=%q err=%v", raw, err)
	}
	entries, err := os.ReadDir(filepath.Join(outputPath, "evidence"))
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 || manifest.Status != "pass" || manifest.Publishable || manifest.Completed != 2 || manifest.ReplaySummary == nil || manifest.ReplaySummary.Requests != 2 || manifest.ReplaySummary.InputTokens != 16000 || manifest.ReplaySummary.OutputTokens != 16 || !manifest.ReplaySummary.TimingFaithful || !manifest.ReplaySummary.InputBudgetClaimedProviderCounted || len(entries) != 2 {
		t.Fatalf("calls=%d manifest=%#v evidence=%d output=%s", calls.Load(), manifest, len(entries), output)
	}
}
