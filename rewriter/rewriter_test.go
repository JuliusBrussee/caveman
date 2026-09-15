package rewriter

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/JuliusBrussee/caveman/engine/ccr"
)

// recordedCall captures what the client put on the wire, so provider framing is
// asserted without a network.
type recordedCall struct {
	url    string
	header http.Header
	body   map[string]any
}

func fakeDoer(t *testing.T, status int, payload string, calls *[]recordedCall) func(*http.Request) (*http.Response, error) {
	t.Helper()
	return func(req *http.Request) (*http.Response, error) {
		raw, err := io.ReadAll(req.Body)
		if err != nil {
			t.Fatalf("read request body: %v", err)
		}
		var decoded map[string]any
		if err := json.Unmarshal(raw, &decoded); err != nil {
			t.Fatalf("request body is not JSON: %v", err)
		}
		*calls = append(*calls, recordedCall{url: req.URL.String(), header: req.Header.Clone(), body: decoded})
		return &http.Response{
			StatusCode: status,
			Body:       io.NopCloser(strings.NewReader(payload)),
			Header:     make(http.Header),
		}, nil
	}
}

func anthropicPayloadStop(text, stop string, in, out int) string {
	body, _ := json.Marshal(map[string]any{
		"content":     []map[string]any{{"type": "text", "text": text}},
		"stop_reason": stop,
		"usage":       map[string]any{"input_tokens": in, "output_tokens": out},
	})
	return string(body)
}

func anthropicPayload(text string, in, out int) string {
	return anthropicPayloadStop(text, "end_turn", in, out)
}

func openAIPayloadFinish(text, finish string, in, out int) string {
	body, _ := json.Marshal(map[string]any{
		"choices": []map[string]any{{
			"message":       map[string]any{"content": text},
			"finish_reason": finish,
		}},
		"usage": map[string]any{"prompt_tokens": in, "completion_tokens": out},
	})
	return string(body)
}

func openAIPayload(text string, in, out int) string {
	return openAIPayloadFinish(text, "stop", in, out)
}

// bigStep is comfortably over the default θ so the invocation gate opens.
func bigStep(t *testing.T) []byte {
	t.Helper()
	var b strings.Builder
	b.WriteString("$ python -m pytest tests/ -q\n")
	for i := 0; i < 400; i++ {
		b.WriteString("tests/test_alpha.py::test_case_")
		b.WriteString(strings.Repeat("x", 4))
		b.WriteString(" PASSED\n")
	}
	b.WriteString("tests/test_beta.py::test_edge FAILED\n")
	b.WriteString("FAILED tests/test_beta.py::test_edge - AssertionError: assert 'a' == 'b'\n")
	b.WriteString("1 failed, 400 passed in 12.44s\nexit code 1")
	step := []byte(b.String())
	if got := countTokens(step); got <= DefaultTheta {
		t.Fatalf("fixture is only %d tokens, need more than theta %d", got, DefaultTheta)
	}
	return step
}

const goodRewrite = `$ python -m pytest tests/ -q
[individual test lines omitted; 400 PASSED]
tests/test_beta.py::test_edge FAILED
FAILED tests/test_beta.py::test_edge - AssertionError: assert 'a' == 'b'
1 failed, 400 passed in 12.44s
exit code 1`

func newTestClient(t *testing.T, cfg Config) *Client {
	t.Helper()
	client, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return client
}

func TestRewriteAcceptedAnthropic(t *testing.T) {
	step := bigStep(t)
	var calls []recordedCall
	client := newTestClient(t, Config{
		Provider: "anthropic",
		Model:    "claude-haiku-4-5",
		APIKey:   "sk-test",
		HTTPDoer: fakeDoer(t, 200, anthropicPayload(goodRewrite, 1200, 90), &calls),
	})

	res, err := client.Rewrite(context.Background(), Request{
		StepBytes:   step,
		ToolName:    "Bash",
		WindowBytes: [][]byte{[]byte("older"), step, []byte("newer"), []byte("newest")},
	})
	if err != nil {
		t.Fatalf("Rewrite: %v", err)
	}
	if !res.Accepted || res.RejectReason != "" {
		t.Fatalf("Rewrite accepted=%v reason=%q, want accepted", res.Accepted, res.RejectReason)
	}
	if res.InputTokens != 1200 || res.OutputTokens != 90 {
		t.Errorf("usage = %d/%d, want 1200/90", res.InputTokens, res.OutputTokens)
	}

	wantPointer := "\n[condensed by caveman; original recoverable via <<ccr:" + ccr.Handle(step) + ">>]"
	if !strings.HasSuffix(string(res.Rewritten), wantPointer) {
		t.Errorf("Rewritten does not end with the recovery pointer:\n%s", res.Rewritten)
	}
	if res.CandidateTokens != countTokens(res.Rewritten) {
		t.Errorf("CandidateTokens = %d, want %d", res.CandidateTokens, countTokens(res.Rewritten))
	}
	if !strings.HasPrefix(string(res.Rewritten), "$ python -m pytest") {
		t.Errorf("Rewritten does not start with the model's text:\n%s", res.Rewritten)
	}
	if countTokens(res.Rewritten) >= countTokens(step) {
		t.Errorf("Rewritten is not smaller than the original")
	}

	if len(calls) != 1 {
		t.Fatalf("made %d calls, want 1", len(calls))
	}
	call := calls[0]
	if call.url != anthropicEndpoint {
		t.Errorf("url = %q, want %q", call.url, anthropicEndpoint)
	}
	if call.header.Get("x-api-key") != "sk-test" {
		t.Errorf("x-api-key = %q", call.header.Get("x-api-key"))
	}
	if call.header.Get("anthropic-version") != anthropicVersion {
		t.Errorf("anthropic-version = %q", call.header.Get("anthropic-version"))
	}
	if call.body["temperature"] != float64(0) {
		t.Errorf("temperature = %v, want 0", call.body["temperature"])
	}
	if call.body["model"] != "claude-haiku-4-5" {
		t.Errorf("model = %v", call.body["model"])
	}
	if system, _ := call.body["system"].(string); !strings.Contains(system, promptPartGuidelines) {
		t.Errorf("system prompt was not sent")
	}
}

func TestRewriteAcceptedOpenAI(t *testing.T) {
	step := bigStep(t)
	var calls []recordedCall
	client := newTestClient(t, Config{
		Provider: "openai",
		Model:    "gpt-5-mini",
		APIKey:   "sk-test",
		HTTPDoer: fakeDoer(t, 200, openAIPayload(goodRewrite, 1500, 80), &calls),
	})

	res, err := client.Rewrite(context.Background(), Request{StepBytes: step, ToolName: "Bash"})
	if err != nil {
		t.Fatalf("Rewrite: %v", err)
	}
	if !res.Accepted {
		t.Fatalf("Rewrite rejected: %q", res.RejectReason)
	}
	if res.InputTokens != 1500 || res.OutputTokens != 80 {
		t.Errorf("usage = %d/%d, want 1500/80", res.InputTokens, res.OutputTokens)
	}

	call := calls[0]
	if call.url != openAIEndpoint {
		t.Errorf("url = %q, want %q", call.url, openAIEndpoint)
	}
	if call.header.Get("Authorization") != "Bearer sk-test" {
		t.Errorf("Authorization = %q", call.header.Get("Authorization"))
	}
	// gpt-5-mini rejects a caller-set temperature; sending one would 400 every
	// rewrite on the module's documented OpenAI model.
	if _, set := call.body["temperature"]; set {
		t.Errorf("temperature was sent to a reasoning-tier model: %v", call.body["temperature"])
	}
	if _, set := call.body["max_completion_tokens"]; !set {
		t.Errorf("max_completion_tokens was not sent")
	}
}

func TestOpenAITemperatureIsSentToModelsThatAcceptIt(t *testing.T) {
	step := bigStep(t)
	var calls []recordedCall
	client := newTestClient(t, Config{
		Provider: "openai",
		Model:    "gpt-4.1-mini",
		APIKey:   "sk-test",
		HTTPDoer: fakeDoer(t, 200, openAIPayload(goodRewrite, 1, 1), &calls),
	})
	if _, err := client.Rewrite(context.Background(), Request{StepBytes: step}); err != nil {
		t.Fatalf("Rewrite: %v", err)
	}
	if calls[0].body["temperature"] != float64(0) {
		t.Errorf("temperature = %v, want 0", calls[0].body["temperature"])
	}
}

func TestOpenAIAcceptsTemperature(t *testing.T) {
	cases := map[string]bool{
		"gpt-5-mini":   false,
		"gpt-5":        false,
		"GPT-5-Nano":   false,
		"o3-mini":      false,
		"o4-mini":      false,
		"gpt-4.1-mini": true,
		"gpt-4o":       true,
		"":             true,
	}
	for model, want := range cases {
		if got := openAIAcceptsTemperature(model); got != want {
			t.Errorf("openAIAcceptsTemperature(%q) = %v, want %v", model, got, want)
		}
	}
}

func TestProviderResponseIsBounded(t *testing.T) {
	var calls []recordedCall
	client := newTestClient(t, Config{
		Provider: "anthropic",
		Model:    "claude-haiku-4-5",
		APIKey:   "sk-test",
		HTTPDoer: fakeDoer(t, 200, strings.Repeat("x", maxProviderResponseBody+1), &calls),
	})

	raw, err := client.do(context.Background(), anthropicEndpoint, map[string]any{}, nil)
	if err == nil || !strings.Contains(err.Error(), "response exceeds") {
		t.Fatalf("do() error = %v, want response size error", err)
	}
	if raw != nil {
		t.Fatalf("do() returned %d bytes on oversized response", len(raw))
	}
}

func TestRewriteBelowThetaMakesNoCall(t *testing.T) {
	var calls []recordedCall
	client := newTestClient(t, Config{
		Provider: "anthropic",
		Model:    "claude-haiku-4-5",
		APIKey:   "sk-test",
		HTTPDoer: fakeDoer(t, 200, anthropicPayload("anything", 1, 1), &calls),
	})

	res, err := client.Rewrite(context.Background(), Request{StepBytes: []byte("short output\n")})
	if err != nil {
		t.Fatalf("Rewrite: %v", err)
	}
	if res.Accepted || res.RejectReason != reasonBelowTheta {
		t.Fatalf("res = %+v, want below_theta", res)
	}
	if len(res.Rewritten) != 0 || res.InputTokens != 0 || res.OutputTokens != 0 {
		t.Errorf("below-theta result carried data: %+v", res)
	}
	if len(calls) != 0 {
		t.Errorf("made %d API calls below theta, want 0", len(calls))
	}
}

// Gate 1 is `l_orig <= theta` -> skip (Algorithm 1 line 17), so a step of
// exactly theta tokens is skipped and one token more is not.
func TestRewriteThetaBoundary(t *testing.T) {
	step := bigStep(t)
	exact := countTokens(step)

	for _, tc := range []struct {
		name     string
		theta    int
		wantCall bool
	}{
		{name: "exactly theta is skipped", theta: exact, wantCall: false},
		{name: "one over theta is invoked", theta: exact - 1, wantCall: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls []recordedCall
			client := newTestClient(t, Config{
				Provider: "anthropic",
				Model:    "claude-haiku-4-5",
				APIKey:   "sk-test",
				Theta:    tc.theta,
				HTTPDoer: fakeDoer(t, 200, anthropicPayload(goodRewrite, 1, 1), &calls),
			})
			res, err := client.Rewrite(context.Background(), Request{StepBytes: step})
			if err != nil {
				t.Fatalf("Rewrite: %v", err)
			}
			if got := len(calls) > 0; got != tc.wantCall {
				t.Fatalf("call made = %v, want %v (reason %q)", got, tc.wantCall, res.RejectReason)
			}
			if !tc.wantCall && res.RejectReason != reasonBelowTheta {
				t.Fatalf("reason = %q, want %q", res.RejectReason, reasonBelowTheta)
			}
		})
	}
}

// Gate 2 is `l_orig - l_reduced > theta` -> apply (Algorithm 1 line 22). With
// theta one token above the achievable saving the rewrite must be rejected.
func TestRewriteApplicationGateBoundary(t *testing.T) {
	step := bigStep(t)
	candidate := goodRewrite + recoveryPointer(step)
	saving := countTokens(step) - countTokens([]byte(candidate))
	if saving <= 0 {
		t.Fatalf("fixture rewrite does not save tokens")
	}

	for _, tc := range []struct {
		name  string
		theta int
		want  string
	}{
		{name: "saving one over theta applies", theta: saving - 1, want: ""},
		{name: "saving exactly theta is rejected", theta: saving, want: reasonTokens},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls []recordedCall
			client := newTestClient(t, Config{
				Provider: "anthropic",
				Model:    "claude-haiku-4-5",
				APIKey:   "sk-test",
				Theta:    tc.theta,
				HTTPDoer: fakeDoer(t, 200, anthropicPayload(goodRewrite, 10, 5), &calls),
			})
			res, err := client.Rewrite(context.Background(), Request{StepBytes: step})
			if err != nil {
				t.Fatalf("Rewrite: %v", err)
			}
			if res.RejectReason != tc.want {
				t.Fatalf("reason = %q, want %q", res.RejectReason, tc.want)
			}
			if tc.want != "" && len(res.Rewritten) != 0 {
				t.Errorf("rejected result carried bytes")
			}
			// Rewriter spend is reported on rejected calls too — it was still paid.
			if res.InputTokens != 10 || res.OutputTokens != 5 {
				t.Errorf("usage = %d/%d, want 10/5", res.InputTokens, res.OutputTokens)
			}
		})
	}
}

func TestRewriteRejectsLossyRewrite(t *testing.T) {
	step := bigStep(t)
	var calls []recordedCall
	client := newTestClient(t, Config{
		Provider: "anthropic",
		Model:    "claude-haiku-4-5",
		APIKey:   "sk-test",
		HTTPDoer: fakeDoer(t, 200, anthropicPayload("$ python -m pytest tests/ -q\n400 tests ran, everything green", 10, 5), &calls),
	})

	res, err := client.Rewrite(context.Background(), Request{StepBytes: step})
	if err != nil {
		t.Fatalf("Rewrite: %v", err)
	}
	if res.Accepted || res.RejectReason != reasonFailureSignal {
		t.Fatalf("res = %+v, want %q", res, reasonFailureSignal)
	}
	if len(res.Rewritten) != 0 {
		t.Errorf("rejected result carried bytes: %s", res.Rewritten)
	}
}

func TestRewriteEmptyCompletion(t *testing.T) {
	step := bigStep(t)
	var calls []recordedCall
	client := newTestClient(t, Config{
		Provider: "anthropic",
		Model:    "claude-haiku-4-5",
		APIKey:   "sk-test",
		HTTPDoer: fakeDoer(t, 200, anthropicPayload("   \n ", 10, 0), &calls),
	})

	res, err := client.Rewrite(context.Background(), Request{StepBytes: step})
	if err != nil {
		t.Fatalf("Rewrite: %v", err)
	}
	if res.Accepted || res.RejectReason != reasonEmpty {
		t.Fatalf("res = %+v, want %q", res, reasonEmpty)
	}
	if res.InputTokens != 10 {
		t.Errorf("usage was dropped on an empty completion: %+v", res)
	}
}

func TestRewriteAPIErrorFailsClosed(t *testing.T) {
	step := bigStep(t)

	cases := []struct {
		name string
		doer func(*http.Request) (*http.Response, error)
	}{
		{
			name: "non-2xx status",
			doer: func(*http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: 429,
					Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"rate limited"}}`)),
					Header:     make(http.Header),
				}, nil
			},
		},
		{
			name: "transport failure",
			doer: func(*http.Request) (*http.Response, error) {
				return nil, errors.New("dial tcp: connection refused")
			},
		},
		{
			name: "unparseable body",
			doer: func(*http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: 200,
					Body:       io.NopCloser(strings.NewReader("not json")),
					Header:     make(http.Header),
				}, nil
			},
		},
		{
			name: "nil response from an injected doer",
			doer: func(*http.Request) (*http.Response, error) { return nil, nil },
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client := newTestClient(t, Config{
				Provider: "anthropic",
				Model:    "claude-haiku-4-5",
				APIKey:   "sk-test",
				HTTPDoer: tc.doer,
			})
			res, err := client.Rewrite(context.Background(), Request{StepBytes: step})
			if err == nil {
				t.Fatalf("Rewrite returned nil error on %s", tc.name)
			}
			if res.Accepted || res.RejectReason != reasonAPIError || len(res.Rewritten) != 0 {
				t.Fatalf("res = %+v, want a closed api_error result", res)
			}
		})
	}
}

func TestNewValidation(t *testing.T) {
	base := Config{Provider: "anthropic", Model: "claude-haiku-4-5", APIKey: "sk-test"}

	if _, err := New(Config{Provider: "gemini", Model: "m", APIKey: "k"}); err == nil {
		t.Error("New accepted an unsupported provider")
	}
	if _, err := New(Config{Provider: "anthropic", APIKey: "k"}); err == nil {
		t.Error("New accepted an empty model")
	}
	if _, err := New(Config{Provider: "anthropic", Model: "m"}); err == nil {
		t.Error("New accepted an empty api key")
	}
	negative := base
	negative.Theta = -1
	if _, err := New(negative); err == nil {
		t.Error("New accepted a negative theta")
	}

	client := newTestClient(t, base)
	if client.theta != DefaultTheta {
		t.Errorf("theta = %d, want the AgentDiet default %d", client.theta, DefaultTheta)
	}
	if client.doer == nil {
		t.Error("nil HTTPDoer was not defaulted")
	}

	upper := base
	upper.Provider = "Anthropic"
	if _, err := New(upper); err != nil {
		t.Errorf("New rejected a differently-cased provider: %v", err)
	}
}

// The pointer must carry the marker syntax the proxy emits and the retrieve
// tool parses, with the handle the engine's own CCR store keys on. A pointer
// the retrieve path cannot resolve is worse than no rewrite.
func TestRecoveryPointerUsesTheEstablishedMarker(t *testing.T) {
	original := []byte("some original step bytes")
	handle := ccr.Handle(original)
	want := "\n[condensed by caveman; original recoverable via <<ccr:" + handle + ">>]"
	got := recoveryPointer(original)
	if got != want {
		t.Fatalf("recoveryPointer() = %q, want %q", got, want)
	}
	if !strings.HasPrefix(handle, "ccr_") {
		t.Errorf("handle %q is not a ccr store handle", handle)
	}
	if !strings.Contains(got, "<<ccr:"+handle+">>") {
		t.Errorf("pointer does not carry a parseable <<ccr:…>> marker: %q", got)
	}
}

// A completion clipped at the output ceiling can look perfectly well formed:
// the elided tail is exactly where a preserved failure would have been, and the
// gate cannot see what is missing. It is rejected on the provider's own signal.
func TestRewriteRejectsTruncatedCompletion(t *testing.T) {
	step := bigStep(t)

	cases := []struct {
		name     string
		provider string
		model    string
		payload  string
	}{
		{
			name:     "anthropic max_tokens",
			provider: "anthropic",
			model:    "claude-haiku-4-5",
			payload:  anthropicPayloadStop(goodRewrite, "max_tokens", 1200, 90),
		},
		{
			name:     "openai length",
			provider: "openai",
			model:    "gpt-5-mini",
			payload:  openAIPayloadFinish(goodRewrite, "length", 1200, 90),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var calls []recordedCall
			client := newTestClient(t, Config{
				Provider: tc.provider,
				Model:    tc.model,
				APIKey:   "sk-test",
				HTTPDoer: fakeDoer(t, 200, tc.payload, &calls),
			})
			res, err := client.Rewrite(context.Background(), Request{StepBytes: step})
			if err != nil {
				t.Fatalf("Rewrite: %v", err)
			}
			// The same completion is accepted when the provider says it finished,
			// so this is the stop reason doing the work and nothing else.
			if res.Accepted || res.RejectReason != reasonTruncated {
				t.Fatalf("res = %+v, want %q", res, reasonTruncated)
			}
			if len(res.Rewritten) != 0 {
				t.Errorf("truncated result carried bytes")
			}
			if res.InputTokens != 1200 || res.OutputTokens != 90 {
				t.Errorf("usage was dropped on truncation: %+v", res)
			}
			if res.CandidateTokens == 0 {
				t.Errorf("CandidateTokens was not reported on truncation")
			}
		})
	}
}

func TestRewriteHandlesEnvelopeArtefacts(t *testing.T) {
	step := bigStep(t)

	cases := []struct {
		name       string
		completion string
		wantReason string
	}{
		{
			name:       "well-formed wrapping fence is stripped",
			completion: "```text\n" + goodRewrite + "\n```",
			wantReason: "",
		},
		{
			name:       "unlabelled wrapping fence is stripped",
			completion: "```\n" + goodRewrite + "\n```",
			wantReason: "",
		},
		{
			name:       "unbalanced opening fence is rejected",
			completion: "```text\n" + goodRewrite,
			wantReason: reasonFence,
		},
		{
			name:       "fence with trailing prose is rejected",
			completion: "```\n" + goodRewrite + "\n```\nHope that helps!",
			wantReason: reasonFence,
		},
		{
			name:       "echoed step wrapper is rejected",
			completion: "<step id=\"s-2\">\n" + goodRewrite + "\n</step>",
			wantReason: reasonStepWrapper,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var calls []recordedCall
			client := newTestClient(t, Config{
				Provider: "anthropic",
				Model:    "claude-haiku-4-5",
				APIKey:   "sk-test",
				HTTPDoer: fakeDoer(t, 200, anthropicPayload(tc.completion, 10, 5), &calls),
			})
			res, err := client.Rewrite(context.Background(), Request{StepBytes: step})
			if err != nil {
				t.Fatalf("Rewrite: %v", err)
			}
			if res.RejectReason != tc.wantReason {
				t.Fatalf("reason = %q, want %q", res.RejectReason, tc.wantReason)
			}
			if tc.wantReason == "" {
				if !strings.HasPrefix(string(res.Rewritten), "$ python -m pytest") {
					t.Errorf("fence was not stripped: %q", res.Rewritten)
				}
				if strings.Contains(string(res.Rewritten), "```") {
					t.Errorf("fence survived into the accepted rewrite: %q", res.Rewritten)
				}
			} else if len(res.Rewritten) != 0 {
				t.Errorf("rejected result carried bytes")
			}
		})
	}
}

func TestStripWrappingFence(t *testing.T) {
	cases := []struct {
		name   string
		in     string
		want   string
		wantOK bool
	}{
		{name: "no fence", in: "plain body", want: "plain body", wantOK: true},
		{name: "labelled fence", in: "```json\n{\"a\":1}\n```", want: "{\"a\":1}", wantOK: true},
		{name: "interior fence is left alone", in: "before\n```\nx\n```\nafter", want: "before\n```\nx\n```\nafter", wantOK: true},
		{name: "opening fence with no newline", in: "```", wantOK: false},
		{name: "unterminated fence", in: "```\nbody", wantOK: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := stripWrappingFence(tc.in)
			if ok != tc.wantOK {
				t.Fatalf("stripWrappingFence() ok = %v, want %v", ok, tc.wantOK)
			}
			if ok && got != tc.want {
				t.Fatalf("stripWrappingFence() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestOutputCeiling(t *testing.T) {
	cases := map[int]int{
		10:     minOutputTokens,
		1000:   1000,
		100000: maxOutputTokens,
	}
	for in, want := range cases {
		if got := outputCeiling(in); got != want {
			t.Errorf("outputCeiling(%d) = %d, want %d", in, got, want)
		}
	}
}
