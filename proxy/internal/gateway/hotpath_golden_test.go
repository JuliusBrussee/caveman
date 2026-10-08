package gateway

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"reflect"
	"testing"

	"github.com/JuliusBrussee/caveman/proxy/providers"
	"github.com/JuliusBrussee/caveman/proxy/providers/anthropic"
)

// hotPathFacts is what the request path and the row's accounting derive from
// one large agent request: the body hash, the frozen-prefix evidence, the
// inspected metadata and the request token counts.
func hotPathFacts(t *testing.T, size int) string {
	t.Helper()
	body := benchAnthropicBody(size, false)
	adapter := anthropic.New("https://api.anthropic.com")
	meta, err := adapter.InspectRequest(context.Background(), bytes.NewReader(body), http.Header{"X-Cave-Route-Path": []string{"/v1/messages"}})
	if err != nil {
		t.Fatal(err)
	}
	prefix, components, known := providerPrefixEvidence(adapter, body, meta)
	componentDigest := sha256.Sum256([]byte(components))
	bodyDigest := sha256.Sum256(body)
	row := RequestRecord{StatusCode: http.StatusOK, AuthMode: string(AuthModePAYG)}
	requestAccounting(&row, meta, benchPricedUsage(), body, body, false)
	delta := "nil"
	if row.RequestEstimatedInputDeltaUSD != nil {
		delta = fmt.Sprint(*row.RequestEstimatedInputDeltaUSD)
	}
	return fmt.Sprintf("bytes=%d sha256=%s meta=%+v prefix=%s components=%d:%s known=%v tokens=%d/%d status=%s basis=%s delta=%s",
		len(body), hex.EncodeToString(bodyDigest[:]), meta, prefix, len(components), hex.EncodeToString(componentDigest[:]), known,
		row.RequestTokensBefore, row.RequestTokensAfter, row.RequestMeasurementStatus, row.RequestSavingsBasis, delta)
}

// TestHotPathFactsGolden pins those facts for a 2k, 50k and 200k request as
// the build before this hot-path work derived them.
func TestHotPathFactsGolden(t *testing.T) {
	for _, size := range benchSizes {
		got := hotPathFacts(t, size.bytes)
		if os.Getenv("CAVEMAN_GOLDEN_ROWS_PRINT") != "" {
			t.Logf("%q: %q,", size.name, got)
			continue
		}
		if want := hotPathGolden[size.name]; got != want {
			t.Errorf("%s:\n got %s\nwant %s", size.name, got, want)
		}
	}
}

var hotPathGolden = map[string]string{
	"2k":   "bytes=10920 sha256=07ee432402c147929b0ec7ee3296ff1816ffc25aa8c7610b94dffb81f49b87e8 meta={Provider:anthropic Model:claude-sonnet-4-5 Region: ServiceTier: InferenceGeo: BillingTier: PricingUnsupportedReason: Endpoint:/v1/messages Stream:false InputBytes:10920 MessageCount:3 ToolsCount:8 SessionID:} prefix=e6780198564b61ff47fa921e10826089e4859988aecfa7ca0171bb4d9eda3041 components=324:3102a0384c7fe352b76a3d9183ab9cee293aa96e5be9e488416b0a7f30a71cab known=true tokens=2111/2111 status=measured basis=estimated_tokens_x_observed_cache_mix delta=0",
	"50k":  "bytes=182404 sha256=4e33de457d8eac14f1c86efd79fcb7651f0c71817dfd3d49acf8cb8ff616a155 meta={Provider:anthropic Model:claude-sonnet-4-5 Region: ServiceTier: InferenceGeo: BillingTier: PricingUnsupportedReason: Endpoint:/v1/messages Stream:false InputBytes:182404 MessageCount:113 ToolsCount:8 SessionID:} prefix=0583222f5d6074d421dd23f8c09ec420c20c413fbb7169d0cc1f5db2c9ce81c4 components=7474:62923a34b1d9e1a6572ac5682e09574b9ffdb96b2d8db86287996020c2b369e3 known=true tokens=34475/34475 status=measured basis=estimated_tokens_x_observed_cache_mix delta=0",
	"200k": "bytes=731123 sha256=c5ad09f1394ae68e88cd4ba786a51c9f2faf6879020cdcd797a122c2f9eaa2ee meta={Provider:anthropic Model:claude-sonnet-4-5 Region: ServiceTier: InferenceGeo: BillingTier: PricingUnsupportedReason: Endpoint:/v1/messages Stream:false InputBytes:731123 MessageCount:465 ToolsCount:8 SessionID:} prefix=e23d871a4ff5d03d8b6914fc3907919f7f405980d57e475458922c45db8dc102 components=30354:4707a21bb47048e52f95f108844e22c603a0f4e694646c1d4a73534a82cfa10e known=true tokens=138027/138027 status=measured basis=estimated_tokens_x_observed_cache_mix delta=0",
}

// TestRequestAccountingDecodesUnchangedBytesOnce: an untransformed request
// (same bytes both sides) counts exactly as a request whose sides differ only
// in serialization, which still takes the two-decode path.
func TestRequestAccountingDecodesUnchangedBytesOnce(t *testing.T) {
	meta := providers.RequestMetadata{Provider: "anthropic", Model: "claude-sonnet-4-5"}
	for _, body := range [][]byte{benchAnthropicBody(9_000, false), benchAnthropicBody(180_000, true), []byte(chatReqBody)} {
		var spaced bytes.Buffer
		if err := json.Indent(&spaced, body, "", "  "); err != nil {
			t.Fatal(err)
		}
		once := RequestRecord{StatusCode: http.StatusOK, AuthMode: string(AuthModePAYG)}
		requestAccounting(&once, meta, benchPricedUsage(), body, body, false)
		twice := RequestRecord{StatusCode: http.StatusOK, AuthMode: string(AuthModePAYG)}
		requestAccounting(&twice, meta, benchPricedUsage(), body, spaced.Bytes(), false)
		if once.RequestMeasurementStatus != "measured" || once.RequestTokensBefore == 0 || !reflect.DeepEqual(once, twice) {
			t.Fatalf("single decode differs:\nonce  %+v\ntwice %+v", once, twice)
		}
	}
}
