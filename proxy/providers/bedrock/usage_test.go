package bedrock

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"hash/crc32"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/JuliusBrussee/caveman/proxy/providers"
	"github.com/JuliusBrussee/caveman/shared/platform/catalog"
	"github.com/JuliusBrussee/caveman/shared/platform/cost"
)

// Converse responses report usage in camelCase, which the shared ParseUsageBytes
// does not understand — the Bedrock adapter must parse it itself.
func TestParseUsage_ConverseCamelCase(t *testing.T) {
	a := newAdapter(t)
	body := `{"output":{"message":{"role":"assistant","content":[{"text":"hi"}]}},` +
		`"usage":{"inputTokens":1200,"outputTokens":340,"totalTokens":1540}}`
	usage, _, err := a.ParseUsage(context.Background(), http.Header{}, strings.NewReader(body))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if usage.InputTokens != 1200 || usage.OutputTokens != 340 {
		t.Errorf("tokens = %d/%d, want 1200/340", usage.InputTokens, usage.OutputTokens)
	}
	// No cache telemetry reported -> cache_status stays honest "unknown".
	if usage.CacheStatus != "unknown" {
		t.Errorf("cache_status = %q, want unknown (no cache telemetry)", usage.CacheStatus)
	}
}

// InvokeModel on an Anthropic model returns the native snake_case usage shape,
// including cache telemetry.
func TestParseUsage_AnthropicInvokeSnakeCaseWithCacheHit(t *testing.T) {
	a := newAdapter(t)
	body := `{"id":"msg_x","type":"message","role":"assistant","content":[{"type":"text","text":"hi"}],` +
		`"usage":{"input_tokens":200,"output_tokens":120,"cache_read_input_tokens":900,"cache_creation_input_tokens":0}}`
	usage, _, err := a.ParseUsage(context.Background(), http.Header{}, strings.NewReader(body))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if usage.InputTokens != 1100 || usage.OutputTokens != 120 {
		t.Errorf("tokens = %d/%d, want normalized total 1100/120", usage.InputTokens, usage.OutputTokens)
	}
	if usage.CachedInputTokens != 900 {
		t.Errorf("cached = %d, want 900", usage.CachedInputTokens)
	}
	if usage.CacheStatus != "hit" {
		t.Errorf("cache_status = %q, want hit", usage.CacheStatus)
	}
}

func TestParseUsage_ConverseStreamBinaryEvent(t *testing.T) {
	a := newAdapter(t)
	message := awsEventFrame([]byte(`{"messageStart":{"role":"assistant"}}`))
	// Bedrock totalTokens includes cache buckets: 200 raw input + 1100 cache
	// read + 0 cache write + 50 output.
	metadata := awsEventFrame([]byte(`{"metadata":{"usage":{"inputTokens":200,"outputTokens":50,"totalTokens":1350,"cacheReadInputTokens":1100,"cacheWriteInputTokens":0}}}`))
	body := append(message, metadata...)

	usage, _, err := a.ParseUsage(context.Background(), http.Header{}, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if usage.Malformed {
		t.Fatalf("usage unexpectedly malformed: %+v", usage)
	}
	if !usage.Complete() {
		t.Fatalf("binary event usage incomplete: %+v", usage)
	}
	if usage.InputTokens != 1300 || usage.OutputTokens != 50 || usage.CachedInputTokens != 1100 {
		t.Fatalf("binary event usage = %+v, want input=1300 output=50 cached=1100", usage)
	}
	assertCatalogPricedBedrockUsage(t, usage)
}

func TestParseUsage_InvalidEventCRCIsUnavailable(t *testing.T) {
	a := newAdapter(t)
	body := awsEventFrame([]byte(`{"metadata":{"usage":{"inputTokens":200,"outputTokens":50}}}`))
	body[len(body)-1] ^= 0xff
	usage, _, err := a.ParseUsage(context.Background(), http.Header{}, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if usage.InputTokensReported || usage.OutputTokensReported || usage.InputTokens != 0 || usage.OutputTokens != 0 {
		t.Fatalf("invalid event CRC usage = %+v, want unavailable zero", usage)
	}
}

func TestParseUsage_AnthropicBinaryStreamWithoutFinalUsageIsPartial(t *testing.T) {
	a := newAdapter(t)
	providerEvent := []byte(`{"type":"message_start","message":{"usage":{"input_tokens":20,"output_tokens":1}}}`)
	wrapper := []byte(`{"bytes":"` + base64.StdEncoding.EncodeToString(providerEvent) + `"}`)
	body := awsEventFrame(wrapper)
	usage, _, err := a.ParseUsage(context.Background(), http.Header{}, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if usage.OutputTokensReported || usage.Complete() {
		t.Fatalf("truncated stream usage = %+v, want partial", usage)
	}
}

func TestParseUsage_AnthropicBinaryStreamNullTerminalCounterIsMalformed(t *testing.T) {
	a := newAdapter(t)
	start := []byte(`{"type":"message_start","message":{"usage":{"input_tokens":20,"output_tokens":1}}}`)
	delta := []byte(`{"type":"message_delta","usage":{"output_tokens":null}}`)
	body := append(awsEventFrame([]byte(`{"bytes":"`+base64.StdEncoding.EncodeToString(start)+`"}`)),
		awsEventFrame([]byte(`{"bytes":"`+base64.StdEncoding.EncodeToString(delta)+`"}`))...)
	usage, _, err := a.ParseUsage(context.Background(), http.Header{}, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if !usage.Malformed || usage.Complete() {
		t.Fatalf("null terminal counter must fail closed: %+v", usage)
	}
}

func TestParseUsage_MantleSSERequiresTerminalUsage(t *testing.T) {
	a := newAdapter(t)
	complete := strings.Join([]string{
		`event: message_start`,
		`data: {"type":"message_start","message":{"usage":{"input_tokens":20,"output_tokens":1,"cache_read_input_tokens":5}}}`,
		``,
		`event: message_delta`,
		`data: {"type":"message_delta","usage":{"output_tokens":7}}`,
		``,
		`event: message_stop`,
		`data: {"type":"message_stop"}`,
		``,
	}, "\n")
	usage, _, err := a.ParseUsage(context.Background(), http.Header{}, strings.NewReader(complete))
	if err != nil {
		t.Fatal(err)
	}
	if !usage.Complete() || usage.InputTokens != 25 || usage.OutputTokens != 7 {
		t.Fatalf("complete Mantle SSE usage = %+v, want complete input=25 output=7", usage)
	}

	truncated := strings.Join([]string{
		`event: message_start`,
		`data: {"type":"message_start","message":{"usage":{"input_tokens":20,"output_tokens":1}}}`,
		``,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","delta":{"type":"text_delta","text":"partial"}}`,
		``,
	}, "\n")
	usage, _, err = a.ParseUsage(context.Background(), http.Header{}, strings.NewReader(truncated))
	if err != nil {
		t.Fatal(err)
	}
	if usage.OutputTokensReported || usage.Complete() {
		t.Fatalf("truncated Mantle SSE usage = %+v, want partial", usage)
	}
}

func awsEventFrame(payload []byte) []byte {
	total := 16 + len(payload)
	frame := make([]byte, total)
	binary.BigEndian.PutUint32(frame[0:4], uint32(total))
	binary.BigEndian.PutUint32(frame[4:8], 0)
	binary.BigEndian.PutUint32(frame[8:12], crc32.ChecksumIEEE(frame[:8]))
	copy(frame[12:total-4], payload)
	binary.BigEndian.PutUint32(frame[total-4:], crc32.ChecksumIEEE(frame[:total-4]))
	return frame
}

func TestParseUsage_ConverseCacheWrite(t *testing.T) {
	a := newAdapter(t)
	body := `{"usage":{"inputTokens":200,"outputTokens":50,"totalTokens":1350,"cacheReadInputTokens":0,"cacheWriteInputTokens":1100}}`
	usage, _, err := a.ParseUsage(context.Background(), http.Header{}, strings.NewReader(body))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if usage.CacheCreationInputTokens != 1100 {
		t.Errorf("cache creation = %d, want 1100", usage.CacheCreationInputTokens)
	}
	if usage.CacheStatus != "write" {
		t.Errorf("cache_status = %q, want write", usage.CacheStatus)
	}
	if !usage.Complete() || usage.InputTokens != 1300 || usage.OutputTokens != 50 {
		t.Fatalf("cache-write usage = %+v, want complete input=1300 output=50", usage)
	}
}

func TestParseUsage_ConverseCacheDetailsPreserveTTLBreakdown(t *testing.T) {
	a := newAdapter(t)
	body := `{"usage":{"inputTokens":200,"outputTokens":50,"totalTokens":1350,"cacheWriteInputTokens":1100,"cacheDetails":[{"ttl":"5m","inputTokens":700},{"ttl":"1h","inputTokens":400}]}}`
	usage, _, err := a.ParseUsage(context.Background(), http.Header{}, strings.NewReader(body))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if usage.Malformed {
		t.Fatalf("valid TTL cache details marked malformed: %+v", usage)
	}
	if usage.CacheCreationInputTokens != 1100 || usage.CacheCreation5mTokens != 700 || usage.CacheCreation1hTokens != 400 {
		t.Fatalf("cache TTL usage = total:%d 5m:%d 1h:%d, want 1100/700/400",
			usage.CacheCreationInputTokens, usage.CacheCreation5mTokens, usage.CacheCreation1hTokens)
	}
	if usage.InputTokens != 1300 {
		t.Fatalf("normalized input tokens = %d, want 1300", usage.InputTokens)
	}
}

func TestParseUsage_ConverseCacheDetailsFailClosed(t *testing.T) {
	a := newAdapter(t)
	tests := []string{
		`{"usage":{"inputTokens":20,"outputTokens":5,"cacheWriteInputTokens":10,"cacheDetails":[{"ttl":"1h","inputTokens":9}]}}`,
		`{"usage":{"inputTokens":20,"outputTokens":5,"cacheWriteInputTokens":10,"cacheDetails":[{"ttl":"24h","inputTokens":10}]}}`,
		`{"usage":{"inputTokens":20,"outputTokens":5,"cacheWriteInputTokens":10,"cacheDetails":{"ttl":"1h","inputTokens":10}}}`,
		`{"usage":{"inputTokens":20,"outputTokens":5,"cacheWriteInputTokens":10,"cacheDetails":[{"ttl":"1h","inputTokens":-1}]}}`,
	}
	for _, body := range tests {
		usage, _, err := a.ParseUsage(context.Background(), http.Header{}, strings.NewReader(body))
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		if !usage.Malformed || usage.Complete() {
			t.Fatalf("invalid cacheDetails accepted: body=%s usage=%+v", body, usage)
		}
	}
}

// Catalog cost for a Bedrock model in the catalog must be non-zero (the catalog
// entry was added with this adapter), proving usage+cost are parsed correctly.
func TestUsageAndCost_NonZeroForCatalogModel(t *testing.T) {
	price, version := catalog.PriceForRegion("bedrock", claudeModel, "us-east-1")
	if strings.HasPrefix(version, "unpriced") {
		t.Fatalf("catalog missing bedrock model %q (version=%q)", claudeModel, version)
	}
	if price.InputPerMillion <= 0 || price.OutputPerMillion <= 0 {
		t.Fatalf("catalog bedrock price not set: %+v", price)
	}
	a := newAdapter(t)
	body := `{"usage":{"inputTokens":1000000,"outputTokens":500000,"totalTokens":1500000}}`
	usage, _, err := a.ParseUsage(context.Background(), http.Header{}, strings.NewReader(body))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	total := cost.EstimateUSD(price, cost.Usage{InputTokens: usage.InputTokens, OutputTokens: usage.OutputTokens})
	// Current extended-access price: 1M input @ $6 + 0.5M output @ $30 = $21.
	if total <= 0 {
		t.Fatalf("computed cost = %v, want > 0", total)
	}
	if total != 21 {
		t.Errorf("computed cost = %v, want 21", total)
	}
}

func TestParseUsage_BedrockTotalIncludesCacheBuckets(t *testing.T) {
	a := newAdapter(t)
	body := `{"usage":{"inputTokens":200,"outputTokens":50,"totalTokens":1350,"cacheReadInputTokens":1100,"cacheWriteInputTokens":0}}`
	usage, _, err := a.ParseUsage(context.Background(), http.Header{}, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if usage.Malformed {
		t.Fatalf("cached Bedrock usage incorrectly rejected: %+v", usage)
	}
	if usage.InputTokens != 1300 || usage.OutputTokens != 50 || !usage.Complete() {
		t.Fatalf("normalized usage = %+v, want complete 1300/50", usage)
	}
}

func TestParseUsage_ConverseAWSBlogTokenCountAliases(t *testing.T) {
	a := newAdapter(t)
	// AWS's published Converse prompt-caching example uses the older
	// cacheReadInputTokenCount/cacheWriteInputTokenCount spellings. Its total is
	// still cache-inclusive: 4 + 34 + 29,841 + 0 = 29,879.
	body := `{"usage":{"inputTokens":4,"outputTokens":34,"totalTokens":29879,"cacheReadInputTokenCount":0,"cacheWriteInputTokenCount":29841}}`
	usage, _, err := a.ParseUsage(context.Background(), http.Header{}, strings.NewReader(body))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if usage.Malformed || !usage.Complete() {
		t.Fatalf("AWS blog-shaped Converse usage = %+v, want complete", usage)
	}
	if usage.InputTokens != 29845 || usage.OutputTokens != 34 || usage.CacheCreationInputTokens != 29841 {
		t.Fatalf("AWS blog-shaped usage = %+v, want effective input=29845 output=34 cache_write=29841", usage)
	}
	if usage.CacheStatus != "write" {
		t.Fatalf("cache_status = %q, want write", usage.CacheStatus)
	}
	assertCatalogPricedBedrockUsage(t, usage)
}

func TestParseUsage_BedrockRawTotalsFailClosedAndKeepMantleSemantics(t *testing.T) {
	tests := []struct {
		name         string
		body         string
		wantComplete bool
		wantInput    int
	}{
		{
			name: "converse total excludes cache",
			body: `{"usage":{"inputTokens":200,"outputTokens":50,"totalTokens":250,"cacheReadInputTokens":1100,"cacheWriteInputTokens":0}}`,
		},
		{
			name: "conflicting cache read aliases",
			body: `{"usage":{"inputTokens":200,"outputTokens":50,"totalTokens":1350,"cacheReadInputTokens":1100,"cache_read_input_tokens":1099}}`,
		},
		{
			name: "conflicting cache read legacy alias",
			body: `{"usage":{"inputTokens":4,"outputTokens":34,"totalTokens":29879,"cacheReadInputTokens":29841,"cacheReadInputTokenCount":29840}}`,
		},
		{
			name: "conflicting input aliases",
			body: `{"usage":{"inputTokens":200,"input_tokens":201,"outputTokens":50,"totalTokens":1350,"cacheReadInputTokens":1100}}`,
		},
		{
			name: "conflicting cache write aliases",
			body: `{"usage":{"inputTokens":200,"outputTokens":50,"totalTokens":1350,"cacheWriteInputTokens":1100,"cache_creation_input_tokens":1099}}`,
		},
		{
			name: "conflicting total aliases",
			body: `{"usage":{"inputTokens":200,"outputTokens":50,"totalTokens":1350,"total_tokens":250,"cacheReadInputTokens":1100}}`,
		},
		{
			name: "cache-inclusive sum overflows int",
			body: `{"usage":{"inputTokens":9223372036854775807,"outputTokens":0,"totalTokens":9223372036854775807,"cacheReadInputTokens":1}}`,
		},
		{
			name: "negative cache counter",
			body: `{"usage":{"inputTokens":200,"outputTokens":50,"totalTokens":1350,"cacheReadInputTokens":-1}}`,
		},
		{
			name:         "mantle snake total excludes cache",
			body:         `{"usage":{"input_tokens":200,"output_tokens":50,"total_tokens":250,"cache_read_input_tokens":1100,"cache_creation_input_tokens":0}}`,
			wantComplete: true,
			wantInput:    1300,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			a := newAdapter(t)
			usage, _, err := a.ParseUsage(context.Background(), http.Header{}, strings.NewReader(tc.body))
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if usage.Complete() != tc.wantComplete {
				t.Fatalf("usage complete=%v, want %v: %+v", usage.Complete(), tc.wantComplete, usage)
			}
			if tc.wantComplete {
				if usage.InputTokens != tc.wantInput || usage.CacheStatus != "hit" {
					t.Fatalf("Mantle usage = %+v, want input=%d/cache_status=hit", usage, tc.wantInput)
				}
			} else if !usage.Malformed || usage.CacheStatus != "unknown" {
				t.Fatalf("invalid Bedrock total accepted: %+v", usage)
			}
		})
	}
}

func TestParseUsage_BedrockMixedUsageFamiliesFailClosed(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{
			name: "camel total with snake counters",
			body: `{"usage":{"input_tokens":200,"output_tokens":50,"totalTokens":1350,"cache_read_input_tokens":1100}}`,
		},
		{
			name: "snake total with camel counters",
			body: `{"usage":{"inputTokens":200,"outputTokens":50,"total_tokens":250,"cacheReadInputTokens":1100}}`,
		},
		{
			name: "partial duplicate aliases",
			body: `{"usage":{"inputTokens":200,"input_tokens":200,"outputTokens":50,"totalTokens":250,"cacheReadInputTokens":1100}}`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			a := newAdapter(t)
			usage, _, err := a.ParseUsage(context.Background(), http.Header{}, strings.NewReader(tc.body))
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if !usage.Malformed || usage.Complete() || usage.CacheStatus != "unknown" {
				t.Fatalf("mixed usage family accepted: %+v", usage)
			}
		})
	}
}

func TestParseUsage_BedrockExactDuplicateAliasesRemainUnambiguous(t *testing.T) {
	a := newAdapter(t)
	body := `{"usage":{"inputTokens":200,"input_tokens":200,"outputTokens":50,"output_tokens":50,"totalTokens":1350,"total_tokens":1350,"cacheReadInputTokens":1100,"cacheReadInputTokenCount":1100,"cache_read_input_tokens":1100,"cacheWriteInputTokens":0,"cacheWriteInputTokenCount":0,"cache_creation_input_tokens":0}}`
	usage, _, err := a.ParseUsage(context.Background(), http.Header{}, strings.NewReader(body))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if usage.Malformed || !usage.Complete() || usage.InputTokens != 1300 || usage.CachedInputTokens != 1100 {
		t.Fatalf("exact duplicate aliases = %+v, want complete effective input=1300/cache_read=1100", usage)
	}
}

// End-to-end (success criterion 3): a Bedrock invoke routed through the adapter
// against a Bedrock-shaped httptest stub records a truthful usage row — tokens
// and cost non-zero, cache_status honest. This exercises ResolveUpstreamURL +
// SanitizeAndMapHeaders (SigV4) + the real upstream call + ParseUsage together.
func TestBedrockInvoke_EndToEndTruthfulUsage(t *testing.T) {
	// Bedrock-shaped stub: asserts it received a SigV4 Authorization header and
	// no leaked secret, then returns a Converse usage body.
	var sawAuth string
	var leaked bool
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawAuth = r.Header.Get("Authorization")
		for _, vs := range r.Header {
			for _, v := range vs {
				if strings.Contains(v, testSecret) {
					leaked = true
				}
			}
		}
		w.Header().Set("content-type", "application/json")
		w.Header().Set("x-amzn-requestid", "req-stub-123")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"output":{"message":{"role":"assistant","content":[{"text":"hi"}]}},` +
			`"usage":{"inputTokens":1500,"outputTokens":420,"totalTokens":2520,"cacheReadInputTokens":600,"cacheWriteInputTokens":0}}`))
	}))
	defer stub.Close()

	a := New(stub.URL).(Adapter)

	inbound, _ := http.NewRequest(http.MethodPost, invokePath(claudeModel, "converse"),
		strings.NewReader(`{"messages":[{"role":"user","content":[{"text":"hi"}]}]}`))
	inbound.Header.Set("content-type", "application/json")
	inbound.Header.Set("x-cave-aws-region", "us-east-1")
	inbound.Header.Set("x-cave-route-path", invokePath(claudeModel, "converse"))

	// Resolve upstream + sign, exactly as the proxy does.
	upstreamURL, err := a.ResolveUpstreamURL(context.Background(), inbound, providers.RouteContext{})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	cred := providers.Credential{Key: "AKIAIOSFODNN7EXAMPLE:" + testSecret}
	headers, err := a.SanitizeAndMapHeaders(context.Background(), inbound, cred, upstreamURL)
	if err != nil {
		t.Fatalf("sanitize: %v", err)
	}

	upReq, _ := http.NewRequest(http.MethodPost, upstreamURL.String(), strings.NewReader(`{"messages":[]}`))
	upReq.Header = headers
	resp, err := http.DefaultClient.Do(upReq)
	if err != nil {
		t.Fatalf("upstream call: %v", err)
	}
	defer resp.Body.Close()

	if !strings.HasPrefix(sawAuth, "AWS4-HMAC-SHA256 ") {
		t.Errorf("stub did not receive a SigV4 Authorization header: %q", sawAuth)
	}
	if leaked {
		t.Fatal("AWS secret reached the upstream stub")
	}

	usage, _, err := a.ParseUsage(context.Background(), resp.Header, resp.Body)
	if err != nil {
		t.Fatalf("parse usage: %v", err)
	}
	if !usage.Complete() || usage.InputTokens != 2100 || usage.OutputTokens != 420 {
		t.Fatalf("usage tokens are not complete/effective: %+v", usage)
	}
	if usage.CachedInputTokens != 600 {
		t.Errorf("cached = %d, want 600", usage.CachedInputTokens)
	}
	if usage.CacheStatus != "hit" {
		t.Errorf("cache_status = %q, want hit", usage.CacheStatus)
	}
	if usage.ProviderRequestID != "req-stub-123" {
		t.Errorf("provider request id = %q, want req-stub-123", usage.ProviderRequestID)
	}

	// Cost must be non-zero for the catalog Bedrock model.
	price, _ := catalog.PriceForRegion("bedrock", claudeModel, "us-east-1")
	total := cost.EstimateUSD(price, cost.Usage{
		InputTokens:       usage.InputTokens - usage.CachedInputTokens,
		OutputTokens:      usage.OutputTokens,
		CachedInputTokens: usage.CachedInputTokens,
	})
	if total <= 0 {
		t.Fatalf("cost = %v, want > 0 (truthful spend)", total)
	}
	if !CachePointEligibleModel(claudeModel) {
		t.Fatal("catalog-priced Claude model must remain in the Bedrock cache-point eligible population")
	}
}

func assertCatalogPricedBedrockUsage(t *testing.T, usage providers.UsageObservation) {
	t.Helper()
	price, version := catalog.PriceForRegion("bedrock", claudeModel, "us-east-1")
	if strings.HasPrefix(version, "unpriced") || price.InputPerMillion <= 0 || price.OutputPerMillion <= 0 {
		t.Fatalf("catalog price missing for %q: version=%q price=%+v", claudeModel, version, price)
	}
	rawInput := usage.InputTokens - usage.CachedInputTokens - usage.CacheCreationInputTokens
	if rawInput < 0 {
		t.Fatalf("effective input smaller than cache subsets: %+v", usage)
	}
	if total := cost.EstimateUSD(price, cost.Usage{
		InputTokens: rawInput, OutputTokens: usage.OutputTokens,
		CachedInputTokens: usage.CachedInputTokens, CacheCreationTokens: usage.CacheCreationInputTokens,
	}); total <= 0 {
		t.Fatalf("catalog cost = %v, want > 0 for complete Bedrock usage", total)
	}
}

// Both truncation sites in this file zero OutputTokensReported themselves, and
// neither can rely on providers.ParseUsageBytes to stamp the raw blob:
// mergeBedrockUsage hands it one synthesized {"usage":{...}} document carrying
// none of the message_start/message_delta markers that function's own
// truncation detection keys on. Until they called
// providers.MarkRawUsageIncomplete directly, RawUsage kept the provisional
// {"output_tokens":1} with nothing recording that the parser had refused it —
// so the contract stated on UsageObservation.RawUsage and in
// 0025_raw_provider_usage.sql ("absence of the key means complete") was false
// for Bedrock, and a re-pricer obeying it would resurrect that count.
func TestParseUsage_TruncatedBedrockStreamsStampRawUsageIncomplete(t *testing.T) {
	providerEvent := []byte(`{"type":"message_start","message":{"usage":{"input_tokens":20,"output_tokens":1}}}`)
	eventStream := awsEventFrame([]byte(`{"bytes":"` + base64.StdEncoding.EncodeToString(providerEvent) + `"}`))
	mantleSSE := strings.Join([]string{
		`event: message_start`,
		`data: {"type":"message_start","message":{"usage":{"input_tokens":20,"output_tokens":1}}}`,
		``,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","delta":{"type":"text_delta","text":"partial"}}`,
		``,
	}, "\n")

	for name, body := range map[string][]byte{
		"aws event stream": eventStream,
		"mantle sse":       []byte(mantleSSE),
	} {
		t.Run(name, func(t *testing.T) {
			a := newAdapter(t)
			usage, _, err := a.ParseUsage(context.Background(), http.Header{}, bytes.NewReader(body))
			if err != nil {
				t.Fatal(err)
			}
			if usage.OutputTokensReported || usage.Complete() {
				t.Fatalf("truncated stream reported complete usage: %+v", usage)
			}
			if len(usage.RawUsage) == 0 {
				t.Fatal("RawUsage was dropped entirely; the provider's reported values should survive beside the label")
			}
			var raw map[string]any
			if err := json.Unmarshal(usage.RawUsage, &raw); err != nil {
				t.Fatalf("RawUsage is not valid JSON: %v (%s)", err, usage.RawUsage)
			}
			if flag, ok := raw[providers.RawUsageIncompleteKey].(bool); !ok || !flag {
				t.Fatalf("RawUsage = %s, want %s:true", usage.RawUsage, providers.RawUsageIncompleteKey)
			}
		})
	}
}

// The label has to mean something: a complete Bedrock stream must not carry it.
func TestParseUsage_CompleteBedrockStreamIsNotStampedIncomplete(t *testing.T) {
	a := newAdapter(t)
	complete := strings.Join([]string{
		`event: message_start`,
		`data: {"type":"message_start","message":{"usage":{"input_tokens":20,"output_tokens":1}}}`,
		``,
		`event: message_delta`,
		`data: {"type":"message_delta","usage":{"output_tokens":7}}`,
		``,
	}, "\n")
	usage, _, err := a.ParseUsage(context.Background(), http.Header{}, strings.NewReader(complete))
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(usage.RawUsage, &raw); err != nil {
		t.Fatalf("RawUsage is not valid JSON: %v (%s)", err, usage.RawUsage)
	}
	if _, stamped := raw[providers.RawUsageIncompleteKey]; stamped {
		t.Errorf("complete Bedrock stream was stamped incomplete: %s", usage.RawUsage)
	}
}
