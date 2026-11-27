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
	"github.com/klauspost/compress/zstd"
)

func TestChatGPTRecordsNativeSessionUsage(t *testing.T) {
	t.Parallel()
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
		for _, encoding := range []string{"identity", "gzip", "zstd", "zstd-8m"} {
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
					if encoding == "zstd-8m" {
						var encoded bytes.Buffer
						writer, err := zstd.NewWriter(&encoded, zstd.WithWindowSize(8<<20), zstd.WithSingleSegment(false))
						if err != nil {
							t.Fatal(err)
						}
						_, _ = writer.Write(body)
						if err := writer.Flush(); err != nil {
							t.Fatal(err)
						}
						if err := writer.Close(); err != nil {
							t.Fatal(err)
						}
						body = encoded.Bytes()
						var frame zstd.Header
						if err := frame.Decode(body); err != nil || frame.WindowSize != 8<<20 {
							t.Fatalf("fixture window=%d, want 8 MiB: %v", frame.WindowSize, err)
						}
						headers["Content-Encoding"] = "zstd"
					} else if encoding != "identity" {
						body = encodedBody(t, encoding, body)
						headers["Content-Encoding"] = encoding
					}
					// A valid marker is stripped before provider delivery. Bodies
					// without one retain their original encoded bytes.
					stripsMarker := tc.name == "signed" || tc.name == "signed-over-header" || tc.name == "conflicting-markers"
					handler := upstream.upstream.Config.Handler
					upstream.upstream.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						wire, err := io.ReadAll(r.Body)
						if err != nil {
							t.Error(err)
							w.WriteHeader(http.StatusBadRequest)
							return
						}
						if model != AutoModel && !stripsMarker && !bytes.Equal(wire, body) {
							t.Error("unmarked or invalid-marker request bytes changed")
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
						if stripsMarker && bytes.Contains(decoded, []byte("caveman-session-v1")) {
							t.Error("signed native marker reached provider")
						}
						var payload map[string]any
						if err := json.Unmarshal(decoded, &payload); err != nil || payload["input"] != "Explain E_TIMEOUT." {
							t.Errorf("technical payload changed: %v", err)
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

func TestChatGPTMetadataRejectsOversizedZstdWindow(t *testing.T) {
	t.Parallel()
	var encoded bytes.Buffer
	writer, err := zstd.NewWriter(&encoded, zstd.WithWindowSize(16<<20), zstd.WithSingleSegment(false))
	if err != nil {
		t.Fatal(err)
	}
	_, _ = writer.Write([]byte(`{"model":"gpt-6.1-sol","input":"Explain E_TIMEOUT."}`))
	if err := writer.Flush(); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	var frame zstd.Header
	if err := frame.Decode(encoded.Bytes()); err != nil || frame.WindowSize != 16<<20 {
		t.Fatalf("fixture window=%d, want 16 MiB: %v", frame.WindowSize, err)
	}
	if _, ok := decodeChatGPTMetadataBody(encoded.Bytes(), "zstd", 32<<20); ok {
		t.Fatal("metadata decoder accepted zstd window above the existing 8 MiB bound")
	}
}
