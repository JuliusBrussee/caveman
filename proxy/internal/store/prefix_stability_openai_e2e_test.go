package store

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/JuliusBrussee/caveman/proxy/internal/gateway"
	"github.com/JuliusBrussee/caveman/proxy/providers"
	"github.com/JuliusBrussee/caveman/proxy/providers/openai"
)

// The OpenAI half of the cross-turn stability e2e, wired to the REAL file-backed
// SQLite replacement cache the binary uses. OpenAI's prompt cache is automatic, so
// a flipped prefix produces no error and no header — only a silently colder cache
// on a row that claimed a reduction. These tests answer the two production
// questions the in-memory gateway double cannot: does the stored replacement
// survive a process restart on disk, and does it hold when the proxy serves many
// turns at once against the store that is also its telemetry sink?

const e2eChatResp = `{"id":"chatcmpl_1","object":"chat.completion","model":"gpt-5.5","choices":[{"message":{"role":"assistant","content":"ok"}}],"usage":{"prompt_tokens":1000,"completion_tokens":20}}`

type e2eChatTransport struct {
	mu     sync.Mutex
	bodies [][]byte
}

func (t *e2eChatTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	body, _ := io.ReadAll(r.Body)
	t.mu.Lock()
	t.bodies = append(t.bodies, append([]byte(nil), body...))
	t.mu.Unlock()
	return &http.Response{
		StatusCode: 200,
		Header:     http.Header{"Content-Type": {"application/json"}},
		Body:       io.NopCloser(strings.NewReader(e2eChatResp)),
		Request:    r,
	}, nil
}

type e2eChatCreds struct{}

func (e2eChatCreds) Resolve(string, *http.Request) providers.Credential {
	return providers.Credential{Key: "sk-byok-e2e"}
}

// e2eNonceCompressor stands in for two different engine builds: the SAME input
// compresses to different bytes under a different nonce, so any byte equality
// across a restart can only have come from the durable store.
type e2eNonceCompressor struct{ nonce string }

func (c e2eNonceCompressor) CompressSegment(seg []byte) ([]byte, int, int) {
	return []byte("CMP" + c.nonce + ":" + e2eHandle(seg)), 100, 40
}

func (c e2eNonceCompressor) StoreOriginal(b []byte) (string, error) { return e2eHandle(b), nil }

func e2eChatConversation(userTexts ...string) string {
	msgs := []string{`{"role":"system","content":"You are a helpful assistant."}`}
	for i, text := range userTexts {
		if i > 0 {
			msgs = append(msgs, fmt.Sprintf(`{"role":"assistant","content":"assistant reply %d"}`, i))
		}
		msgs = append(msgs, `{"role":"user","content":"`+text+`"}`)
	}
	return `{"model":"gpt-5.5","messages":[` + strings.Join(msgs, ",") + `]}`
}

func newChatServer(st gateway.PrefixCache, comp gateway.Compressor, rt *e2eChatTransport) *gateway.Server {
	return gateway.New(gateway.Config{
		Adapters:       []providers.Adapter{openai.New("https://upstream.test")},
		Auth:           e2eAuth{},
		Creds:          e2eChatCreds{},
		Sink:           e2eSink{},
		Compressor:     comp,
		PrefixCache:    st,
		HTTPClient:     &http.Client{Transport: rt},
		RecoveryViaMCP: true,
	})
}

func serveChat(srv *gateway.Server, body string) {
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	srv.Handler().ServeHTTP(httptest.NewRecorder(), req)
}

// TestE2EOpenAIPrefixStabilityAcrossRestart closes the store and reopens the same
// database file with a compressor that now emits different bytes. The second
// process must still forward the FIRST process's replacement for the frozen turn.
func TestE2EOpenAIPrefixStabilityAcrossRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "caveman.db")
	t1 := strings.Repeat("turn one client bytes ", 40)
	t2 := strings.Repeat("turn two client bytes ", 40)
	want := "CMPv1:" + e2eHandle([]byte(t1))

	first, err := Open(path, nil)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	rt1 := &e2eChatTransport{}
	serveChat(newChatServer(first, e2eNonceCompressor{nonce: "v1"}, rt1), e2eChatConversation(t1))
	if len(rt1.bodies) != 1 || !strings.Contains(string(rt1.bodies[0]), want) {
		t.Fatalf("turn 1 did not compress the live zone:\n%s", rt1.bodies[0])
	}
	if err := first.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	second, err := Open(path, nil)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer second.Close()
	rt2 := &e2eChatTransport{}
	serveChat(newChatServer(second, e2eNonceCompressor{nonce: "v2"}, rt2), e2eChatConversation(t1, t2))

	body := string(rt2.bodies[0])
	if !strings.Contains(body, want) {
		t.Fatalf("post-restart request lost the stored replacement for the frozen turn:\n%s", body)
	}
	if strings.Contains(body, "CMPv2:"+e2eHandle([]byte(t1))) {
		t.Fatalf("post-restart request recompressed a frozen turn and diverged the prefix:\n%s", body)
	}
	if strings.Contains(body, t1) {
		t.Fatalf("post-restart request flipped the prefix back to the client's originals:\n%s", body)
	}
}

// TestE2EOpenAIPrefixStabilityUnderConcurrency fires turn 1 serially, then N
// concurrent turn-2 requests that all carry turn 1 as a FROZEN block. Every one of
// them must send turn 1's stored replacement: the store is also the telemetry sink,
// so each lookup races a write, and a lookup that degrades into a miss would put
// the client's original bytes upstream non-deterministically.
func TestE2EOpenAIPrefixStabilityUnderConcurrency(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "caveman.db"), nil)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	rt := &e2eChatTransport{}
	srv := newChatServer(st, e2eNonceCompressor{nonce: "v1"}, rt)

	t1 := strings.Repeat("turn one client bytes ", 40)
	want := "CMPv1:" + e2eHandle([]byte(t1))
	serveChat(srv, e2eChatConversation(t1))
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
			serveChat(srv, e2eChatConversation(t1, strings.Repeat(fmt.Sprintf("turn two variant %d ", i), 40)))
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
