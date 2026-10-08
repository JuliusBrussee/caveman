package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/JuliusBrussee/caveman/proxy/internal/gateway"
	"github.com/JuliusBrussee/caveman/proxy/providers"
	"github.com/JuliusBrussee/caveman/proxy/providers/anthropic"
)

// End-to-end cross-turn stability wired to the REAL file-backed SQLite replacement
// cache the binary uses (cmd/caveman-proxy passes the spend store as
// gateway.PrefixCache), not the gateway package's in-memory test double. It answers
// the production question: does a message this proxy already compressed keep going
// upstream as the SAME bytes when the proxy is serving more than one request at a
// time? The store is also the telemetry sink, so a lookup always races a write.

type e2eTransport struct {
	mu     sync.Mutex
	bodies [][]byte
}

func (t *e2eTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	body, _ := io.ReadAll(r.Body)
	t.mu.Lock()
	t.bodies = append(t.bodies, append([]byte(nil), body...))
	t.mu.Unlock()
	return &http.Response{
		StatusCode: 200,
		Header:     http.Header{"Content-Type": {"application/json"}},
		Body: io.NopCloser(strings.NewReader(
			`{"id":"msg","type":"message","model":"claude-sonnet-4-6","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":10,"output_tokens":2}}`)),
		Request: r,
	}, nil
}

type e2eAuth struct{}

func (e2eAuth) Authenticate(context.Context, *http.Request) (gateway.RequestContext, error) {
	return gateway.RequestContext{Label: "local", RuntimeMode: "compress"}, nil
}

type e2eCreds struct{}

func (e2eCreds) Resolve(string, *http.Request) providers.Credential {
	return providers.Credential{Key: "sk-ant-oat-test", Scheme: "bearer"}
}

type e2eSink struct{}

func (e2eSink) Record(gateway.RequestRecord) {}

// e2eCompressor is deterministic per content, so any byte difference upstream comes
// from the cache, never from the compressor.
type e2eCompressor struct{}

func e2eHandle(b []byte) string {
	sum := sha256.Sum256(b)
	return "ccr_" + hex.EncodeToString(sum[:16])
}

func (e2eCompressor) CompressSegment(seg []byte) ([]byte, int, int) {
	return []byte("CMP:" + e2eHandle(seg)), 100, 40
}

func (e2eCompressor) StoreOriginal(b []byte) (string, error) { return e2eHandle(b), nil }

func e2eConversation(userTexts ...string) string {
	msgs := make([]string, 0, len(userTexts)*2)
	for i, text := range userTexts {
		if i > 0 {
			msgs = append(msgs, `{"role":"assistant","content":[{"type":"text","text":"assistant reply"}]}`)
		}
		msgs = append(msgs, `{"role":"user","content":[{"type":"text","text":"`+text+`","cache_control":{"type":"ephemeral"}}]}`)
	}
	return `{"model":"claude-sonnet-4-6","max_tokens":1024,"messages":[` + strings.Join(msgs, ",") + `]}`
}

var e2eHeaders = map[string]string{
	"user-agent":        "claude-cli/1.0.0",
	"authorization":     "Bearer sk-ant-oat-test",
	"anthropic-beta":    "oauth-2025-04-20",
	"anthropic-version": "2023-06-01",
}

func e2eServe(srv *gateway.Server, body string) {
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
	for k, v := range e2eHeaders {
		req.Header.Set(k, v)
	}
	srv.Handler().ServeHTTP(httptest.NewRecorder(), req)
}

// TestE2EPrefixStabilityWithRealStore fires turn 1 serially (establishing a stored
// replacement for t1), then fires N concurrent turn-2 requests that all carry t1 as
// a FROZEN block. Every one of them must send t1's stored replacement. Any request
// that sends t1's original bytes is a cross-turn prefix divergence. Before the
// store's WAL + busy_timeout DSN pragmas this failed ~7/16.
func TestE2EPrefixStabilityWithRealStore(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "caveman.db"), nil)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	rt := &e2eTransport{}
	srv := gateway.New(gateway.Config{
		Adapters:       []providers.Adapter{anthropic.New("https://upstream.test")},
		Auth:           e2eAuth{},
		Creds:          e2eCreds{},
		Sink:           e2eSink{},
		Compressor:     e2eCompressor{},
		PrefixCache:    st,
		HTTPClient:     &http.Client{Transport: rt},
		RecoveryViaMCP: true,
	})

	t1 := strings.Repeat("turn one client bytes ", 40)
	e2eServe(srv, e2eConversation(t1))
	want := "CMP:" + e2eHandle([]byte(t1))
	if len(rt.bodies) != 1 || !strings.Contains(string(rt.bodies[0]), want) {
		t.Fatalf("turn 1 did not compress the live zone:\n%s", rt.bodies[0])
	}
	rt.mu.Lock()
	rt.bodies = nil
	rt.mu.Unlock()

	const n = 16
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			e2eServe(srv, e2eConversation(t1, strings.Repeat(fmt.Sprintf("turn two variant %d ", i), 40)))
		}(i)
	}
	wg.Wait()

	rt.mu.Lock()
	defer rt.mu.Unlock()
	diverged := 0
	for _, b := range rt.bodies {
		if !strings.Contains(string(b), want) || strings.Contains(string(b), t1) {
			diverged++
		}
	}
	if diverged > 0 {
		t.Fatalf("%d/%d concurrent turn-2 requests flipped the frozen prefix back to the client's original bytes", diverged, len(rt.bodies))
	}
}

// e2eAlternatingCompressor turns every other block down, so requests in flight
// together reach different first decisions for the same bytes.
type e2eAlternatingCompressor struct{ calls atomic.Int64 }

func (c *e2eAlternatingCompressor) CompressSegment(seg []byte) ([]byte, int, int) {
	if c.calls.Add(1)%2 == 0 {
		return nil, 0, 0
	}
	return []byte("CMP:" + e2eHandle(seg)), 100, 40
}

func (*e2eAlternatingCompressor) StoreOriginal(b []byte) (string, error) { return e2eHandle(b), nil }

// TestE2EConcurrentDecisionsAgree: requests in flight together that reach
// different first decisions for one live block (one compresses it, another
// turns it down) must all forward the decision the real store took first.
// Each records its decision before it forwards and follows the row the store
// answers with, so the provider never sees the block in two forms.
func TestE2EConcurrentDecisionsAgree(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "caveman.db"), nil)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	rt := &e2eTransport{}
	srv := gateway.New(gateway.Config{
		Adapters:       []providers.Adapter{anthropic.New("https://upstream.test")},
		Auth:           e2eAuth{},
		Creds:          e2eCreds{},
		Sink:           e2eSink{},
		Compressor:     &e2eAlternatingCompressor{},
		PrefixCache:    st,
		HTTPClient:     &http.Client{Transport: rt},
		RecoveryViaMCP: true,
	})

	for round := 0; round < 16; round++ {
		block := strings.Repeat(fmt.Sprintf("shared live block %d ", round), 40)
		rt.mu.Lock()
		rt.bodies = nil
		rt.mu.Unlock()
		var wg sync.WaitGroup
		for i := 0; i < 8; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				e2eServe(srv, e2eConversation(strings.Repeat(fmt.Sprintf("context %d of round %d ", i, round), 40), block))
			}(i)
		}
		wg.Wait()
		rt.mu.Lock()
		compressed := 0
		for _, b := range rt.bodies {
			if strings.Contains(string(b), "CMP:"+e2eHandle([]byte(block))) {
				compressed++
			}
		}
		if compressed != 0 && compressed != len(rt.bodies) {
			t.Errorf("round %d: %d of %d concurrent requests forwarded the block compressed and the rest raw", round, compressed, len(rt.bodies))
		}
		rt.mu.Unlock()
	}
}
