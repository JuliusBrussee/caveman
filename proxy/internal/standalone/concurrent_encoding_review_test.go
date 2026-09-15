package standalone

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/JuliusBrussee/caveman/engine/ccr"
	"github.com/JuliusBrussee/caveman/proxy/internal/config"
	"github.com/JuliusBrussee/caveman/proxy/internal/store"
	"github.com/klauspost/compress/zstd"
)

// #897 hypothesized a shared response re-encoder. This test instead checks the
// actual contract: overlapping, distinct compressed responses keep their exact
// bytes and remain decodable by the client. Both sides are real HTTP servers;
// the proxy uses its production transport and engine/recovery wiring.
func TestStandaloneConcurrentEncodedResponsesRemainDecodable(t *testing.T) {
	const concurrent = 12
	t.Setenv("CAVE_SSRF_ALLOWLIST", "127.0.0.1")
	for _, mode := range []string{"record", "compress"} {
		for _, encoding := range []string{"gzip", "zstd"} {
			for _, framing := range []string{"content_length", "chunked"} {
				t.Run(mode+"/"+encoding+"/"+framing, func(t *testing.T) {
					plain := make([][]byte, concurrent)
					wire := make([][]byte, concurrent)
					for i := range plain {
						// Distinct incompressible content crosses the proxy copy buffer
						// boundary, exposing accidental buffer reuse across responses.
						random := make([]byte, 48*1024+i*997)
						_, _ = rand.New(rand.NewSource(int64(i))).Read(random)
						plain[i], _ = json.Marshal(map[string]any{
							"id": fmt.Sprintf("msg_concurrent_%d", i), "type": "message", "role": "assistant", "model": "claude-sonnet-4-6",
							"content": []map[string]any{{"type": "text", "text": base64.StdEncoding.EncodeToString(random)}},
							"usage":   map[string]int{"input_tokens": 10 + i, "output_tokens": 50 + i},
						})
						wire[i] = encodeConcurrentFixture(t, encoding, plain[i])
					}
					var arrived atomic.Int32
					release := make(chan struct{})
					upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						i, err := strconv.Atoi(r.URL.Query().Get("fixture"))
						if err != nil || i < 0 || i >= concurrent {
							http.Error(w, "bad fixture", http.StatusBadRequest)
							return
						}
						if r.URL.Path != "/v1/messages" || r.URL.Query().Get("beta") != "true" || r.Header.Get("Accept-Encoding") != encoding {
							t.Error("upstream request route or encoding negotiation changed")
						}
						if arrived.Add(1) == concurrent {
							close(release)
						}
						select {
						case <-release:
						case <-r.Context().Done():
							return
						}
						w.Header().Set("Content-Type", "application/json")
						w.Header().Set("Content-Encoding", encoding)
						w.Header().Set("X-Request-Id", fmt.Sprintf("upstream-concurrent-%d", i))
						if framing == "content_length" {
							w.Header().Set("Content-Length", strconv.Itoa(len(wire[i])))
						}
						w.WriteHeader(http.StatusOK)
						for offset := 0; offset < len(wire[i]); {
							end := min(offset+4093+i, len(wire[i]))
							if _, err := w.Write(wire[i][offset:end]); err != nil {
								return
							}
							w.(http.Flusher).Flush()
							offset = end
						}
					}))
					defer upstream.Close()
					spend, err := store.Open(filepath.Join(t.TempDir(), "caveman.db"), nil)
					if err != nil {
						t.Fatal(err)
					}
					defer spend.Close()
					recovery, err := ccr.OpenMemory()
					if err != nil {
						t.Fatal(err)
					}
					defer recovery.Close()
					sink := make(protocolSink, concurrent)
					cfg := config.Config{Mode: mode, UpstreamProxy: "off", Providers: map[string]config.ProviderConfig{"anthropic": {BaseURL: upstream.URL}}}
					proxy := httptest.NewServer(New(cfg, sink, Options{
						Compressor: NewEngineCompressor(recovery), PrefixCache: spend, RecoveryViaMCP: true,
					}).Handler())
					defer proxy.Close()
					client := &http.Client{Transport: &http.Transport{DisableCompression: true}, Timeout: 15 * time.Second}
					defer client.CloseIdleConnections()
					var workers sync.WaitGroup
					for i := range plain {
						workers.Add(1)
						go func(i int) {
							defer workers.Done()
							req, err := http.NewRequest(http.MethodPost, proxy.URL+fmt.Sprintf("/w/claude/v1/messages?beta=true&fixture=%d", i),
								strings.NewReader(`{"model":"claude-sonnet-4-6","max_tokens":256,"stream":false,"messages":[{"role":"user","content":"hello"}]}`))
							if err != nil {
								t.Error(err)
								return
							}
							req.Header.Set("x-api-key", "sk-ant-api-local-fixture")
							req.Header.Set("Content-Type", "application/json")
							req.Header.Set("Accept-Encoding", encoding)
							resp, err := client.Do(req)
							if err != nil {
								t.Error(err)
								return
							}
							defer resp.Body.Close()
							got, err := io.ReadAll(resp.Body)
							if err != nil {
								t.Errorf("response %d read failed: %v", i, err)
								return
							}
							if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Encoding") != encoding ||
								resp.Header.Get("X-Request-Id") != fmt.Sprintf("upstream-concurrent-%d", i) {
								t.Errorf("response %d lost status, encoding or request identity", i)
							}
							if !bytes.Equal(got, wire[i]) {
								t.Errorf("response %d compressed bytes changed (%d received, %d expected)", i, len(got), len(wire[i]))
							}
							decoded, err := decodeConcurrentFixture(encoding, got)
							if err != nil || !bytes.Equal(decoded, plain[i]) {
								t.Errorf("response %d client decompression failed or decoded another response: %v", i, err)
							}
						}(i)
					}
					workers.Wait()
					if got := arrived.Load(); got != concurrent {
						t.Fatalf("provider calls = %d, want %d without retries", got, concurrent)
					}
					seen := make(map[int]bool)
					for range plain {
						select {
						case record := <-sink:
							i := record.InputTokens - 10
							if i < 0 || i >= concurrent || seen[i] || record.OutputTokens != 50+i || record.ErrorCode != "" || record.TokenUsageBasis != "provider_complete" {
								t.Errorf("concurrent accounting mixed, lost or corrupted a response: input=%d output=%d basis=%s error=%s", record.InputTokens, record.OutputTokens, record.TokenUsageBasis, record.ErrorCode)
							}
							seen[i] = true
						case <-time.After(5 * time.Second):
							t.Fatal("missing response accounting")
						}
					}
				})
			}
		}
	}
}

func encodeConcurrentFixture(t *testing.T, encoding string, plain []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	var encoder io.WriteCloser
	if encoding == "gzip" {
		encoder = gzip.NewWriter(&buf)
	} else {
		var err error
		encoder, err = zstd.NewWriter(&buf, zstd.WithEncoderConcurrency(1))
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := encoder.Write(plain); err != nil {
		t.Fatal(err)
	}
	if err := encoder.Close(); err != nil {
		t.Fatal(err)
	}
	return append([]byte(nil), buf.Bytes()...)
}

func decodeConcurrentFixture(encoding string, wire []byte) ([]byte, error) {
	var decoder io.ReadCloser
	if encoding == "gzip" {
		var err error
		decoder, err = gzip.NewReader(bytes.NewReader(wire))
		if err != nil {
			return nil, err
		}
	} else {
		reader, err := zstd.NewReader(bytes.NewReader(wire), zstd.WithDecoderConcurrency(1))
		if err != nil {
			return nil, err
		}
		decoder = reader.IOReadCloser()
	}
	defer decoder.Close()
	return io.ReadAll(decoder)
}
