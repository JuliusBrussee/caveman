package cloudlink

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/JuliusBrussee/caveman/proxy/internal/gateway"
	"github.com/JuliusBrussee/caveman/proxy/providers"
	"github.com/JuliusBrussee/caveman/proxy/providers/anthropic"
)

// The live cache check: real OpenRouter traffic through the whole proxy, on a
// throwaway home, only when CAVEMAN_LIVE_OPENROUTER_KEY names a key file.
// Spend is read from OpenRouter's own usage.cost and capped at $0.50.
//
//	CAVEMAN_LIVE_OPENROUTER_KEY=~/.config/caveman-research/openrouter.key \
//	  go test ./proxy/internal/cloudlink -run TestLiveOpenRouterCache -v
//
// It shows (1) a session's turns 2+ read the cache, with x-session-id and,
// once warm, the provider pin; (2) four sibling children on one new prefix
// write it once through the fan-out gate, against four writes when the same
// shape goes ungated.

const liveCap = 0.50

// liveCall is one request the proxy sent OpenRouter and what it answered.
type liveCall struct {
	session, pin                 bool
	provider                     string
	input, cacheRead, cacheWrite int
	cost                         float64
}

type liveRecorder struct {
	mu    sync.Mutex
	calls []liveCall
	spent float64
}

func (l *liveRecorder) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Host != "openrouter.ai" {
		return nil, errors.New("live check: only OpenRouter may be called, got " + r.URL.Host)
	}
	l.mu.Lock()
	over := l.spent >= liveCap
	l.mu.Unlock()
	if over {
		return nil, errors.New("live check: spend cap reached")
	}
	sent, _ := io.ReadAll(r.Body)
	r.Body = io.NopCloser(bytes.NewReader(sent))
	resp, err := http.DefaultTransport.RoundTrip(r)
	if err != nil {
		return nil, err
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body = io.NopCloser(bytes.NewReader(raw))
	var answer struct {
		Provider string `json:"provider"`
		Usage    struct {
			InputTokens         int     `json:"input_tokens"`
			CacheRead           int     `json:"cache_read_input_tokens"`
			CacheWrite          int     `json:"cache_creation_input_tokens"`
			PromptTokens        int     `json:"prompt_tokens"`
			Cost                float64 `json:"cost"`
			PromptTokensDetails struct {
				Cached int `json:"cached_tokens"`
				Write  int `json:"cache_write_tokens"`
			} `json:"prompt_tokens_details"`
		} `json:"usage"`
	}
	if strings.Contains(resp.Header.Get("content-type"), "event-stream") {
		// A stream: the largest of each count its events carry, and its provider.
		largest := func(field string) (n float64) {
			for _, m := range regexp.MustCompile(`"`+field+`"\s*:\s*([0-9.eE+-]+)`).FindAllSubmatch(raw, -1) {
				v, _ := strconv.ParseFloat(string(m[1]), 64)
				n = max(n, v)
			}
			return n
		}
		answer.Usage.InputTokens, answer.Usage.CacheRead = int(largest("input_tokens")), int(largest("cache_read_input_tokens"))
		answer.Usage.CacheWrite, answer.Usage.Cost = int(largest("cache_creation_input_tokens")), largest("cost")
		if m := regexp.MustCompile(`"provider"\s*:\s*"([^"]+)"`).FindSubmatch(raw); m != nil {
			answer.Provider = string(m[1])
		}
	} else {
		_ = json.Unmarshal(raw, &answer)
	}
	u := answer.Usage
	call := liveCall{
		session: r.Header.Get("x-session-id") != "", pin: bytes.Contains(sent, []byte(`"allow_fallbacks":false`)),
		provider: answer.Provider, cost: u.Cost,
		input: u.InputTokens + u.CacheRead + u.CacheWrite + u.PromptTokens, cacheRead: u.CacheRead + u.PromptTokensDetails.Cached, cacheWrite: u.CacheWrite + u.PromptTokensDetails.Write,
	}
	if resp.StatusCode >= 300 {
		call.provider = fmt.Sprintf("HTTP %d: %.200s", resp.StatusCode, raw)
	}
	l.mu.Lock()
	l.calls, l.spent = append(l.calls, call), l.spent+u.Cost
	l.mu.Unlock()
	return resp, nil
}

func (l *liveRecorder) take() []liveCall {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := l.calls
	l.calls = nil
	return out
}

// livePrefix is a system prompt of about 3k tokens no cache has seen.
func livePrefix() string {
	var b strings.Builder
	fmt.Fprintf(&b, "Run %d. You are a careful assistant. ", time.Now().UnixNano())
	for i := range 220 {
		fmt.Fprintf(&b, "Rule %d: answer in one word, and never mention rule %d or the colour of item %d. ", i, i*7%13, i*3+1)
	}
	return b.String()
}

func TestLiveOpenRouterCache(t *testing.T) {
	keyFile := os.Getenv("CAVEMAN_LIVE_OPENROUTER_KEY")
	if keyFile == "" {
		t.Skip("CAVEMAN_LIVE_OPENROUTER_KEY not set")
	}
	if strings.HasPrefix(keyFile, "~/") {
		home, _ := os.UserHomeDir()
		keyFile = home + keyFile[1:]
	}
	key, err := os.ReadFile(keyFile)
	if err != nil || len(bytes.TrimSpace(key)) == 0 {
		t.Skip("no OpenRouter key at CAVEMAN_LIVE_OPENROUTER_KEY")
	}
	t.Setenv("CAVE_NO_KEYCHAIN", "1")
	var mu sync.Mutex
	poolID := "openrouter/kimi-k3"
	cloud := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		mu.Lock()
		defer mu.Unlock()
		if r.URL.Path == "/v1/route" {
			_, _ = fmt.Fprintf(w, `{"pool_id":%q,"via":"local","model":%q}`, poolID, strings.TrimPrefix(poolID, "openrouter/"))
		}
	}))
	defer cloud.Close()
	home := signedIn(t, cloud.URL)
	addLogin(t, home, "openrouter", string(bytes.TrimSpace(key)))
	link := newLink(home)
	link.events.every = time.Hour
	rec := &liveRecorder{}
	srv := gateway.New(gateway.Config{
		Adapters: []providers.Adapter{anthropic.New("https://api.anthropic.com")},
		Auth:     localAuth{}, Creds: byok{}, Sink: nullSink{},
		HTTPClient: &http.Client{Transport: rec, Timeout: 2 * time.Minute},
		Cloud:      link,
	})
	send := func(stream bool, session, agent, system string, messages ...string) {
		body := fmt.Sprintf(`{"model":"claude-opus-5-5","max_tokens":8,"stream":%v,"system":[{"type":"text","text":%q,"cache_control":{"type":"ephemeral"}}],"messages":[%s]}`, stream, system, strings.Join(messages, ","))
		req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
		req.Header.Set("x-api-key", "sk-ant-api03-live")
		req.Header.Set("x-cave-agent", "claude")
		req.Header.Set("x-claude-code-session-id", session)
		if agent != "" {
			req.Header.Set("x-claude-code-agent-id", agent)
		}
		w := httptest.NewRecorder()
		srv.Handler().ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Errorf("status %d: %.300s", w.Code, w.Body.String())
		}
	}
	user := func(text string) string { return fmt.Sprintf(`{"role":"user","content":%q}`, text) }
	assistant := `{"role":"assistant","content":"Ok."}`
	logCalls := func(label string, calls []liveCall) {
		for i, c := range calls {
			t.Logf("%s #%d provider=%s input=%d cache_read=%d cache_write=%d cost=$%.5f x-session-id=%v pinned=%v",
				label, i+1, c.provider, c.input, c.cacheRead, c.cacheWrite, c.cost, c.session, c.pin)
		}
	}

	// (1) One session, four turns on kimi-k3 (chat wire, several providers).
	system := livePrefix()
	history := []string{user("Say hi.")}
	session := fmt.Sprintf("live-%d", time.Now().UnixNano())
	for turn := range 4 {
		send(false, session, "", system, history...)
		history = append(history, assistant, user(fmt.Sprintf("Turn %d: say ok.", turn+2)))
	}
	calls := rec.take()
	logCalls("session", calls)
	reads := 0
	for _, c := range calls[1:] {
		if c.cacheRead > 0 {
			reads++
		}
	}
	if len(calls) != 4 || reads == 0 {
		t.Errorf("session: %d calls, %d of turns 2-4 read the cache", len(calls), reads)
	}

	// (2) Four streamed siblings on one new prefix, Claude Sonnet 5.5
	// (messages wire, cache_control kept): gated (one parent), then ungated
	// (four parents). The gate opens at the leader's first content event.
	mu.Lock()
	poolID = "openrouter/claude-sonnet-5-5"
	mu.Unlock()
	fan := func(label string, parent func(i int) string) (writes int) {
		system := livePrefix()
		var wg sync.WaitGroup
		for i := range 4 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				send(true, parent(i), fmt.Sprintf("child-%d", i), system, user(fmt.Sprintf("Child %d: say ok.", i)))
			}()
		}
		wg.Wait()
		calls := rec.take()
		logCalls(label, calls)
		for _, c := range calls {
			if c.cacheWrite > 0 {
				writes++
			}
		}
		return writes
	}
	one := fmt.Sprintf("parent-%d", time.Now().UnixNano())
	gated := fan("fan-out gated", func(int) string { return one })
	ungated := fan("fan-out ungated", func(i int) string { return fmt.Sprintf("parent-%d-%d", time.Now().UnixNano(), i) })
	t.Logf("fan-out: %d cache writes gated, %d ungated; spent $%.4f of $%.2f", gated, ungated, rec.spent, liveCap)
	if gated != 1 {
		t.Errorf("gated fan-out wrote the prefix %d times, want once", gated)
	}
}
