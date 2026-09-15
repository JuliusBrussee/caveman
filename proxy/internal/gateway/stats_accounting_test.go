package gateway

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/JuliusBrussee/caveman/proxy/providers"
	"github.com/JuliusBrussee/caveman/proxy/providers/openai"
	"github.com/JuliusBrussee/caveman/shared/platform/catalog"
	"github.com/JuliusBrussee/caveman/shared/platform/cost"
	"github.com/tiktoken-go/tokenizer"
)

func completeStatsUsage() providers.UsageObservation {
	return providers.UsageObservation{
		InputTokens: 10_000, OutputTokens: 100, CachedInputTokens: 6_000,
		InputTokensReported: true, OutputTokensReported: true,
	}
}

func countStatsJSON(t *testing.T, body []byte) int {
	t.Helper()
	var compact bytes.Buffer
	var value map[string]any
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		t.Fatal(err)
	}
	encoder := json.NewEncoder(&compact)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		t.Fatal(err)
	}
	codec, err := tokenizer.Get(tokenizer.O200kBase)
	if err != nil {
		t.Fatal(err)
	}
	n, err := codec.Count(strings.TrimSuffix(compact.String(), "\n"))
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func TestRequestAccountingIncludesMarkersAndToolOverhead(t *testing.T) {
	original := []byte(chatReqBody)
	compressed := []byte(`{"model":"gpt-5.5","messages":[{"role":"user","content":"X"}]}`)
	accepted := []byte(`{"model":"gpt-5.5","messages":[{"role":"user","content":"X\n<<ccr:ccr_fixture>>"}],"tools":[{"type":"function","function":{"name":"caveman_retrieve","description":"Read the original source when compressed context is insufficient.","parameters":{"type":"object","properties":{"handle":{"type":"string"}}}}}]}`)
	row := RequestRecord{StatusCode: http.StatusOK, AuthMode: string(AuthModePAYG)}
	requestAccounting(&row, providers.RequestMetadata{Provider: "openai", Model: "gpt-5.5"}, completeStatsUsage(), original, accepted, false)
	if row.RequestMeasurementStatus != "measured" || row.RequestTokenBasis != requestTokenBasis {
		t.Fatalf("measurement = %+v", row)
	}
	if row.RequestTokensBefore != countStatsJSON(t, original) || row.RequestTokensAfter != countStatsJSON(t, accepted) {
		t.Fatalf("counts do not cover whole request: before=%d after=%d", row.RequestTokensBefore, row.RequestTokensAfter)
	}
	if row.RequestTokensAfter <= countStatsJSON(t, compressed) {
		t.Fatal("markers and injected tools were not counted")
	}
	if row.RequestEstimatedInputDeltaUSD == nil || *row.RequestEstimatedInputDeltaUSD <= 0 {
		t.Fatalf("expected priced signed delta: %+v", row)
	}
	_, version := catalog.Price("openai", "gpt-5.5")
	if !row.PricingKnown || row.PricingCatalogVersion != version || row.PricingModel != "gpt-5.5" || row.PriceCacheWrite1hPerMillion < 0 {
		t.Fatalf("missing price snapshot: %+v", row)
	}
}

func TestRequestAccountingPreservesOverheadAndZero(t *testing.T) {
	original := []byte(`{"model":"gpt-5.5","messages":[{"role":"user","content":"x"}]}`)
	longer := []byte(`{"model":"gpt-5.5","messages":[{"role":"user","content":"x\n<<ccr:ccr_fixture>>"}]}`)
	for _, test := range []struct {
		name     string
		accepted []byte
		negative bool
	}{{"overhead", longer, true}, {"passthrough", original, false}} {
		t.Run(test.name, func(t *testing.T) {
			row := RequestRecord{StatusCode: 200, AuthMode: "payg"}
			requestAccounting(&row, providers.RequestMetadata{Provider: "openai", Model: "gpt-5.5"}, completeStatsUsage(), original, test.accepted, false)
			if row.RequestEstimatedInputDeltaUSD == nil {
				t.Fatal("known zero or negative delta was discarded")
			}
			if test.negative {
				if row.RequestTokensBefore >= row.RequestTokensAfter || *row.RequestEstimatedInputDeltaUSD >= 0 {
					t.Fatalf("overhead was clipped: %+v", row)
				}
			} else if row.RequestTokensBefore != row.RequestTokensAfter || *row.RequestEstimatedInputDeltaUSD != 0 {
				t.Fatalf("passthrough claimed delta: %+v", row)
			}
		})
	}
}

func TestRequestAccountingNormalizesEquivalentJSONRepresentations(t *testing.T) {
	original := []byte(`{ "model":"gpt-5.5", "input":[{"content":"\u0061\u0062 \u003chello\u003e \u0026","role":"user"}] }`)
	accepted := []byte(`{"input":[{"role":"user","content":"ab <hello> &"}],"model":"gpt-5.5"}`)
	row := RequestRecord{StatusCode: 200, AuthMode: "payg"}
	requestAccounting(&row, providers.RequestMetadata{Provider: "openai", Model: "gpt-5.5"}, completeStatsUsage(), original, accepted, false)
	if row.RequestMeasurementStatus != "measured" || row.RequestTokensBefore != row.RequestTokensAfter || row.RequestEstimatedInputDeltaUSD == nil || *row.RequestEstimatedInputDeltaUSD != 0 {
		t.Fatalf("JSON serialization differences minted a content delta: %+v", row)
	}
}

func TestRequestAccountingAllowsMediaNamesInTextToolSchemas(t *testing.T) {
	body := []byte(`{"model":"gpt-5.5","messages":[{"role":"user","content":"Read metadata"}],"tools":[{"type":"function","function":{"name":"read_metadata","parameters":{"type":"object","properties":{"image":{"type":"string"},"document":{"type":"object","properties":{"source":{"type":"string"}}},"audio":{"type":"string"},"video":{"type":"string"},"inlineData":{"type":"string"},"fileData":{"type":"string"},"input_audio":{"type":"string"},"image_url":{"type":"string"}}}}}]}`)
	row := RequestRecord{StatusCode: 200, AuthMode: "payg"}
	requestAccounting(&row, providers.RequestMetadata{Provider: "openai", Model: "gpt-5.5"}, completeStatsUsage(), body, body, false)
	if row.RequestMeasurementStatus != "measured" || row.RequestTokensBefore != row.RequestTokensAfter || row.RequestEstimatedInputDeltaUSD == nil || *row.RequestEstimatedInputDeltaUSD != 0 {
		t.Fatalf("ordinary tool property names blocked request accounting: %+v", row)
	}
}

func TestRequestAccountingKeepsSubscriptionEquivalentSeparate(t *testing.T) {
	original := []byte(chatReqBody)
	accepted := []byte(`{"model":"gpt-5.5","messages":[{"role":"user","content":"X"}]}`)
	for _, auth := range []string{"oauth", "subscription"} {
		row := RequestRecord{StatusCode: 200, AuthMode: auth}
		requestAccounting(&row, providers.RequestMetadata{Provider: "chatgpt-subscription", Model: "gpt-5.5"}, completeStatsUsage(), original, accepted, false)
		if row.RequestEstimatedInputDeltaUSD == nil || !row.PricingKnown || row.PricingProvider != "openai" {
			t.Fatalf("missing explicitly separate API equivalent: %+v", row)
		}
		if row.TotalCostUSD != 0 || row.SavingsUSD != 0 {
			t.Fatal("subscription was assigned actual dollars")
		}
	}
	row := RequestRecord{StatusCode: 200, AuthMode: "subscription"}
	requestAccounting(&row, providers.RequestMetadata{Provider: "chatgpt-subscription", Model: "gpt-unknown-special"}, completeStatsUsage(), original, accepted, false)
	if row.PricingKnown || row.RequestEstimatedInputDeltaUSD != nil || row.RequestMeasurementStatus != "measured" {
		t.Fatal("unknown model borrowed a sibling rate or lost available token counts")
	}
}

func TestRequestAccountingExcludesUnprovenPayloads(t *testing.T) {
	for _, test := range []struct {
		name, body, status string
		responseStatus     int
		failed, retrieved  bool
	}{
		{"http_failure", chatReqBody, "failed", 500, false, false},
		{"stream_failure", chatReqBody, "failed", 200, true, false},
		{"recovery_loop", chatReqBody, "recovery_multiple_calls", 200, false, true},
		{"image", `{"input":[{"type":"input_image","image_url":"data:image/png;base64,AA"}]}`, "unsupported_payload", 200, false, false},
		{"gemini_image", `{"contents":[{"parts":[{"inlineData":{"mimeType":"image/png","data":"AA"}}]}]}`, "unsupported_payload", 200, false, false},
		{"bedrock_image", `{"messages":[{"content":[{"image":{"format":"png","source":{"bytes":"AA"}}}]}]}`, "unsupported_payload", 200, false, false},
		{"bedrock_document", `{"messages":[{"content":[{"document":{"format":"pdf","source":{"bytes":"AA"}}}]}]}`, "unsupported_payload", 200, false, false},
		{"invalid", `not JSON`, "unsupported_payload", 200, false, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			row := RequestRecord{StatusCode: test.responseStatus, AuthMode: "payg"}
			usage := completeStatsUsage()
			usage.ProviderError = test.failed
			requestAccounting(&row, providers.RequestMetadata{Provider: "openai", Model: "gpt-5.5"}, usage, []byte(test.body), []byte(test.body), test.retrieved)
			if row.RequestMeasurementStatus != test.status || row.RequestTokenBasis != "" || row.RequestEstimatedInputDeltaUSD != nil {
				t.Fatalf("unproven payload was measured: %+v", row)
			}
		})
	}
}

func TestRequestAccountingBoundsWorkAndPreservesProviderPricing(t *testing.T) {
	row := RequestRecord{StatusCode: 200, AuthMode: "payg"}
	body := bytes.Repeat([]byte("x"), requestAccountingMaxBytes+1)
	requestAccounting(&row, providers.RequestMetadata{Provider: "openai", Model: "gpt-5.5"}, completeStatsUsage(), body, body, false)
	if row.RequestMeasurementStatus != "request_payload_too_large" || row.RequestTokensBefore != 0 || !row.PricingKnown {
		t.Fatalf("large request accounting should retain price but skip tokenization: %+v", row)
	}
}

func TestRequestAccountingKeepsCompleteFailedUsagePricedWithoutSavings(t *testing.T) {
	for _, incomplete := range []bool{false, true} {
		usage := completeStatsUsage()
		usage.ProviderError = true
		usage.OutputTokensReported = !incomplete
		row := RequestRecord{StatusCode: http.StatusOK, ErrorCode: "provider_stream_error", AuthMode: "payg"}
		requestAccounting(&row, providers.RequestMetadata{Provider: "openai", Model: "gpt-5.5"}, usage, []byte(chatReqBody), []byte(chatReqBody), false)
		if row.PricingKnown != !incomplete {
			t.Fatalf("incomplete=%v: observed spend pricing known=%v", incomplete, row.PricingKnown)
		}
		if row.RequestMeasurementStatus != "failed" || row.RequestEstimatedInputDeltaUSD != nil || row.RequestTokenBasis != "" {
			t.Fatalf("failed request earned a comparison or savings: %+v", row)
		}
		price, _ := catalog.Price("openai", "gpt-5.5")
		if _, ok := weightedInputDeltaUSD(price, 100, usage); ok {
			t.Fatal("provider error was eligible for counterfactual savings")
		}
	}
}

func TestWeightedInputDeltaUsesDisjointCacheBuckets(t *testing.T) {
	price := cost.Price{InputPerMillion: 3, CacheReadPerMillion: .3, CacheWritePerMillion: 3.75, CacheWrite1hPerMillion: 6}
	usage := completeStatsUsage()
	usage.CacheCreationInputTokens = 1000
	usage.CacheCreation1hTokens = 200
	value, ok := weightedInputDeltaUSD(price, 400, usage)
	// 3000 fresh, 6000 read, 800 five-minute write, 200 one-hour write.
	want := cost.RoundUSD(400 * (3000*3.0 + 6000*.3 + 800*3.75 + 200*6.0) / 10000 / 1e6)
	if !ok || value != want {
		t.Fatalf("weighted delta = %.10f, %v; want %.10f", value, ok, want)
	}
	negative, ok := weightedInputDeltaUSD(price, -400, usage)
	if !ok || negative != -want {
		t.Fatalf("negative overhead = %v, %v", negative, ok)
	}
	usage.CacheCreation1hTokens = usage.CacheCreationInputTokens + 1
	if _, ok := weightedInputDeltaUSD(price, 400, usage); ok {
		t.Fatal("invalid cache buckets were priced")
	}
}

func TestRequestAccountingDoesNotPriceMissingCacheClassAsFree(t *testing.T) {
	usage := completeStatsUsage()
	usage.CacheCreationInputTokens = 100
	row := RequestRecord{StatusCode: 200, AuthMode: "payg"}
	body := []byte(chatReqBody)
	// GPT-5.5 has no published separate cache-write rate in the catalog.
	requestAccounting(&row, providers.RequestMetadata{Provider: "openai", Model: "gpt-5.5"}, usage, body, body, false)
	if row.PricingKnown || row.RequestEstimatedInputDeltaUSD != nil || row.RequestMeasurementStatus != "measured" {
		t.Fatalf("unpriced cache-write bucket treated as free: %+v", row)
	}
}

func TestWeightedInputDeltaUsesObservedTierWithoutGuessingCrossing(t *testing.T) {
	price := cost.Price{InputPerMillion: 3, CacheReadPerMillion: .3, LongContextThresholdTokens: 1000, LongContextInputMultiplier: 2, LongContextOutputMultiplier: 1.5}
	usage := providers.UsageObservation{InputTokens: 1500, OutputTokens: 10, InputTokensReported: true, OutputTokensReported: true}
	value, ok := weightedInputDeltaUSD(price, 100, usage)
	if !ok || value != .0006 {
		t.Fatalf("long context = %.10f, %v; want 0.0006", value, ok)
	}
	usage.InputTokens = 950
	if _, ok := weightedInputDeltaUSD(price, 100, usage); ok {
		t.Fatal("local delta was used to claim provider threshold crossing")
	}
}

func TestStatsMeteringFollowsFinalAcceptedRequest(t *testing.T) {
	for _, test := range []struct {
		name       string
		statuses   []int
		out        string
		wantStatus string
		wantSign   int
	}{
		{"compressed", []int{200}, "X", "measured", 1},
		{"original_fallback", []int{429, 200}, "X", "measured", 0},
		{"failed", []int{500}, "X", "failed", 0},
		{"overhead", []int{200}, strings.Repeat("extra content ", 1000), "measured", -1},
	} {
		t.Run(test.name, func(t *testing.T) {
			sink := &captureSink{}
			transport := &captureTransport{statuses: test.statuses, responses: []string{chatRespBody, chatRespBody}}
			srv := New(Config{
				Adapters: []providers.Adapter{openai.New("https://api.openai.com")},
				Auth:     stubAuth{rc: RequestContext{RuntimeMode: "compress"}}, Creds: stubCreds{key: "sk-byok"}, Sink: sink,
				Compressor:     &stubCompressor{out: []byte(test.out), before: 100, after: 1, handle: "ccr_fixture"},
				RecoveryViaMCP: true, HTTPClient: &http.Client{Transport: transport},
			})
			req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(chatReqBody))
			srv.Handler().ServeHTTP(httptest.NewRecorder(), req)
			row := sink.last(t)
			if row.RequestMeasurementStatus != test.wantStatus {
				t.Fatalf("measurement status=%s", row.RequestMeasurementStatus)
			}
			if test.wantStatus == "failed" {
				if row.RequestTokensBefore != 0 || row.RequestTokensAfter != 0 || row.CompressionTokensBefore != 0 || row.RequestEstimatedInputDeltaUSD != nil {
					t.Fatalf("failed transform minted counters: %+v", row)
				}
				return
			}
			accepted := transport.bodies[len(transport.bodies)-1]
			if row.RequestTokensBefore != countStatsJSON(t, []byte(chatReqBody)) || row.RequestTokensAfter != countStatsJSON(t, accepted) {
				t.Fatalf("counter differs from final sent body: %+v", row)
			}
			delta := row.RequestTokensBefore - row.RequestTokensAfter
			if (test.wantSign == 1 && delta <= 0) || (test.wantSign == -1 && delta >= 0) || (test.wantSign == 0 && delta != 0) {
				t.Fatalf("net delta=%d, expected sign=%d", delta, test.wantSign)
			}
		})
	}
}

func TestStatsMeteringCountsCachedReplacementsOnEachProviderRequest(t *testing.T) {
	transport := payghTransport(2, chatRespBody)
	srv, sink := newPAYGPrefixServer(openai.New("https://api.openai.com"), "sk-byok", &stableCompressor{}, newTestPrefixCache(), transport)
	body := openaiTurnConversation(turnText(1))
	serveBody(t, srv, "/v1/chat/completions", body, nil)
	first := sink.last(t)
	serveBody(t, srv, "/v1/chat/completions", body, nil)
	replayed := sink.last(t)
	if replayed.CompressionTokensBefore != 0 || replayed.CompressionTokensAfter != 0 {
		t.Fatal("legacy unique-compression counters changed on replacement replay")
	}
	if replayed.RequestTokensBefore != first.RequestTokensBefore || replayed.RequestTokensAfter != first.RequestTokensAfter || replayed.RequestTokensBefore <= replayed.RequestTokensAfter {
		t.Fatalf("replayed provider request lost its original-vs-accepted delta: first=%+v replay=%+v", first, replayed)
	}
	if !bytes.Equal(transport.bodies[0], transport.bodies[1]) || replayed.RequestTokensAfter != countStatsJSON(t, transport.bodies[1]) {
		t.Fatal("replayed accounting diverged from accepted bytes")
	}
}

func TestStatsPricingOriginRequiresExactProvider(t *testing.T) {
	for _, test := range []struct {
		provider, origin string
		known            bool
	}{
		{"openai", "https://api.openai.com/v1/responses", true},
		{"openai", "https://eu.api.openai.com/v1/responses", true},
		{"anthropic", "https://api.anthropic.com/v1/messages", true},
		{"gemini", "https://generativelanguage.googleapis.com/v1beta/models/gemini", true},
		{"vertex", "https://us-central1-aiplatform.googleapis.com/", true},
		{"bedrock", "https://bedrock-runtime.us-east-1.amazonaws.com/", true},
		{"bedrock", "https://bedrock-mantle.us-east-1.api.aws/", true},
		{"openai", "https://gateway.example/v1/responses", false},
		{"openai", "https://api.openai.com.evil.example/v1/responses", false},
		{"openai", "https://api.anthropic.com/v1/messages", false},
		{"openai", "http://api.openai.com/v1/responses", false},
		{"bedrock", "https://bedrock-runtime.us-east-1.amazonaws.com.evil.example/", false},
		{"vertex", "https://foo.example-aiplatform.googleapis.com/", false},
	} {
		parsed, err := url.Parse(test.origin)
		if err != nil {
			t.Fatal(err)
		}
		if got := statsPricingOriginKnown(test.provider, parsed); got != test.known {
			t.Errorf("%s %s: known=%v want=%v", test.provider, test.origin, got, test.known)
		}
	}
}

func TestStatsCustomOriginKeepsTokenDeltaUnpriced(t *testing.T) {
	sink := &captureSink{}
	transport := &captureTransport{responses: []string{chatRespBody}}
	srv := New(Config{
		Adapters: []providers.Adapter{openai.New("https://gateway.example")},
		Auth:     stubAuth{rc: RequestContext{RuntimeMode: "compress"}}, Creds: stubCreds{key: "sk-byok"}, Sink: sink,
		Compressor:     &stubCompressor{out: []byte("X"), before: 100, after: 1, handle: "ccr_fixture"},
		RecoveryViaMCP: true, HTTPClient: &http.Client{Transport: transport},
	})
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(chatReqBody))
	srv.Handler().ServeHTTP(httptest.NewRecorder(), request)
	row := sink.last(t)
	if row.RequestMeasurementStatus != "measured" || row.RequestTokensBefore <= row.RequestTokensAfter || row.PricingKnown || row.RequestEstimatedInputDeltaUSD != nil {
		t.Fatalf("custom origin borrowed official published rates: %+v", row)
	}
}

func TestStatsChatGPTRecordsFinalBytesAndSeparateEquivalent(t *testing.T) {
	compressed := []byte(`{"model":"gpt-5.5","input":[{"role":"user","content":"X\n<<ccr:ccr_fixture>>"}]}`)
	original := []byte(`{"model":"gpt-5.5","input":[{"role":"user","content":"` + strings.Repeat("original file content ", 100) + `"}]}`)
	for _, test := range []struct {
		name     string
		accepted []byte
		complete bool
	}{{"compressed", compressed, true}, {"fallback", original, true}, {"incomplete_upload", compressed, false}} {
		t.Run(test.name, func(t *testing.T) {
			sink := &captureSink{}
			srv := &Server{sink: sink, chatGPTUpstream: DefaultChatGPTUpstream}
			requestCapture, responseCapture := &cappedBuffer{limit: chatGPTCaptureLimit}, &cappedBuffer{limit: chatGPTCaptureLimit}
			_, _ = requestCapture.Write(original)
			_, _ = responseCapture.Write([]byte(chatRespBody))
			rawHash, acceptedHash := sha256.Sum256(original), sha256.Sum256(test.accepted)
			request := httptest.NewRequest(http.MethodPost, "/chatgpt/responses", nil)
			srv.recordChatGPT(RequestContext{}, request, "request", "trace", "/responses", time.Now(), 200, "", requestCapture, rawHash[:], acceptedHash[:], test.complete, responseCapture, int64(len(chatRespBody)), false, nil, nil, true, test.accepted)
			row := sink.last(t)
			if !test.complete {
				if row.RequestTokenBasis != "" || row.RequestEstimatedInputDeltaUSD != nil {
					t.Fatal("incomplete upload was treated as a full request")
				}
				return
			}
			if row.RequestTokensBefore != countStatsJSON(t, original) || row.RequestTokensAfter != countStatsJSON(t, test.accepted) || !row.PricingKnown || row.RequestEstimatedInputDeltaUSD == nil {
				t.Fatalf("ChatGPT request accounting incomplete: %+v", row)
			}
			if row.SavingsUSD != 0 || row.TotalCostUSD != 0 || row.PricingProvider != "openai" {
				t.Fatal("ChatGPT equivalent changed actual dollar fields")
			}
		})
	}
}
