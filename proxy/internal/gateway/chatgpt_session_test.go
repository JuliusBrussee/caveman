package gateway

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/JuliusBrussee/caveman/proxy/internal/nativeruntime"
)

func TestChatGPTRecordsNativeSessionUsage(t *testing.T) {
	key := bytes.Repeat([]byte{17}, 32)
	marker, err := nativeruntime.SessionMarker(key, "codex:session-a")
	if err != nil {
		t.Fatal(err)
	}
	other, err := nativeruntime.SessionMarker(key, "codex:session-b")
	if err != nil {
		t.Fatal(err)
	}
	for _, model := range []string{"gpt-6.1-sol", AutoModel} {
		for _, encoding := range []string{"identity", "gzip", "zstd"} {
			for _, tc := range []struct {
				name, context, header, session, basis string
			}{
				{name: "signed", context: marker, session: "codex:session-a", basis: "signed_marker"},
				{name: "explicit", header: "codex:header-session", session: "codex:header-session", basis: "explicit_header"},
				{name: "signed-over-header", context: marker, header: "codex:header-session", session: "codex:session-a", basis: "signed_marker"},
				{name: "invalid-signature", context: strings.Replace(marker, "sig=\"", "sig=\"0", 1)},
				{name: "conflicting-markers", context: marker + " " + other},
				{name: "unmarked"},
			} {
				t.Run(model+"/"+encoding+"/"+tc.name, func(t *testing.T) {
					upstream := newChatGPTAutoUpstream(t, "")
					srv, sink, _ := chatgptTestServer(t, upstream.upstream.URL)
					srv.sessionMarkerKey = key
					body, err := json.Marshal(map[string]any{
						"model": model, "stream": true,
						"instructions": "Preserve exact technical content.\n" + tc.context,
						"input":        "Explain E_TIMEOUT.",
					})
					if err != nil {
						t.Fatal(err)
					}
					headers := map[string]string{"x-cave-session": tc.header}
					if encoding != "identity" {
						body = encodedBody(t, encoding, body)
						headers["Content-Encoding"] = encoding
					}
					// The fixed-model route streams the caller's encoded bytes
					// unchanged. The stub must decode them like the real provider.
					handler := upstream.upstream.Config.Handler
					upstream.upstream.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						wire, err := io.ReadAll(r.Body)
						if err != nil {
							t.Error(err)
							w.WriteHeader(http.StatusBadRequest)
							return
						}
						if model != AutoModel && !bytes.Equal(wire, body) {
							t.Error("session accounting changed the forwarded request bytes")
						}
						decoded, _, err := decodeChatGPTRequestBody(wire, r.Header.Get("Content-Encoding"), chatGPTCaptureLimit)
						if r.Header.Get("Content-Encoding") == "gzip" {
							var reader *gzip.Reader
							reader, err = gzip.NewReader(bytes.NewReader(wire))
							if err == nil {
								decoded, err = io.ReadAll(reader)
								_ = reader.Close()
							}
						}
						if err != nil || !json.Valid(decoded) {
							t.Errorf("invalid provider body: %v", err)
							w.WriteHeader(http.StatusBadRequest)
							return
						}
						r.Body = io.NopCloser(bytes.NewReader(decoded))
						handler.ServeHTTP(w, r)
					})
					response := sendChatGPTAuto(t, srv, "/chatgpt/responses", body, headers)
					if response.Code != 200 || len(sink.rows) != 1 {
						t.Fatalf("response=%d records=%d", response.Code, len(sink.rows))
					}
					row := sink.rows[0]
					if row.SessionID != tc.session || row.SessionCorrelationBasis != tc.basis {
						t.Errorf("session=%q basis=%q; want session=%q basis=%q", row.SessionID, row.SessionCorrelationBasis, tc.session, tc.basis)
					}
					if row.InputTokens != 10 || row.OutputTokens != 2 {
						t.Errorf("usage=%d/%d; want provider usage=10/2", row.InputTokens, row.OutputTokens)
					}
					if row.TotalCostUSD != 0 || row.SavingsUSD != 0 {
						t.Fatal("subscription session correlation must not invent API-priced cost or savings")
					}
				})
			}
		}
	}
}
