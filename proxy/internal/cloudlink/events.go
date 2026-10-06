package cloudlink

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/JuliusBrussee/caveman/proxy/internal/gateway"
)

// The runtime/v1 sender (packages/shared/contracts/schemas/runtime-event-v1):
// one event per recorded model request, buffered in memory, sent in batches of
// at most eventBatchMax, and dropped on any failure. Observe never blocks: a
// full buffer drops the event. Events go only while signed in with routing on
// (signing in alone uploads nothing new), at the data level Cloud's /me names
// for the organization (counts when /me names none); level off, or a level
// that cannot be read, sends nothing.
const (
	eventBatchMax   = 500
	eventQueueMax   = 2000
	eventFlushEvery = 10 * time.Second
	levelTTL        = 10 * time.Minute
)

type eventQueue struct {
	flushing sync.Mutex // one flush at a time; it also guards level and levelAt
	once     sync.Once
	ch       chan runtimeEvent
	full     chan struct{}
	level    string
	levelAt  time.Time
	every    time.Duration // tests shorten it
}

// runtimeEvent carries every field; atLevel trims it to what the level allows.
type runtimeEvent struct {
	EventID          string          `json:"event_id"`
	OccurredAt       string          `json:"occurred_at"`
	InstallID        string          `json:"install_id"`
	Level            string          `json:"level"`
	Agent            string          `json:"agent"`
	Provider         string          `json:"provider,omitempty"`
	BillingBasis     string          `json:"billing_basis,omitempty"`
	Stages           map[string]any  `json:"stages"`
	ModelRequested   string          `json:"model_requested,omitempty"`
	ModelUsed        string          `json:"model_used,omitempty"`
	Tokens           *eventTokens    `json:"tokens,omitempty"`
	KeptOutOfContext *eventKeptCount `json:"kept_out_of_context,omitempty"`
	Route            *eventRoute     `json:"route,omitempty"`
}

type eventTokens struct {
	Label      string `json:"label"`
	Input      int    `json:"input"`
	Output     int    `json:"output"`
	CacheRead  int    `json:"cache_read"`
	CacheWrite int    `json:"cache_write"`
}

type eventKeptCount struct {
	Label  string `json:"label"`
	Tokens int    `json:"tokens"`
}

type eventRoute struct {
	Outcome    string `json:"outcome"`
	Reason     string `json:"reason,omitempty"`
	DecisionID string `json:"decision_id,omitempty"`
}

var (
	slugRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,39}$`)
	uuidRE = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
)

// Observe queues one event for a recorded request. It never blocks.
func (l *Link) Observe(rec gateway.RequestRecord) {
	cfg := l.settings()
	if !cfg.routing || cfg.offline || !cfg.signedIn() {
		return
	}
	q := &l.events
	q.once.Do(func() {
		q.ch = make(chan runtimeEvent, eventQueueMax)
		q.full = make(chan struct{}, 1)
		if q.every == 0 {
			q.every = eventFlushEvery
		}
		go l.sendLoop()
	})
	select {
	case q.ch <- eventFor(rec, cfg.install, l.now()):
	default: // buffer full: drop
	}
	if len(q.ch) >= eventBatchMax {
		select {
		case q.full <- struct{}{}:
		default:
		}
	}
}

func eventFor(rec gateway.RequestRecord, install string, now time.Time) runtimeEvent {
	agent := rec.AgentSlug
	if !slugRE.MatchString(agent) {
		agent = "unlabeled-agent"
	}
	provider := strings.ReplaceAll(rec.Provider, "_", "-")
	if !slugRE.MatchString(provider) {
		provider = ""
	}
	basis := "unknown"
	switch rec.AuthMode {
	case string(gateway.AuthModePAYG):
		basis = "api_key"
	case string(gateway.AuthModeSubscription):
		basis = "subscription"
	}
	compress := "off"
	if rec.RuntimeMode == "compress" || rec.RuntimeMode == "pixel" {
		compress = "ok"
	}
	route := "off"
	switch rec.RouteOutcome {
	case "routed", "kept":
		route = "ok"
	case "degraded", "paused":
		route = "degraded"
	}
	occurred := now.UTC()
	if at, err := time.Parse("2006-01-02 15:04:05.000", rec.Timestamp); err == nil { // the gateway's row stamp, UTC
		occurred = at
	}
	event := runtimeEvent{
		EventID:      newUUID(),
		OccurredAt:   occurred.Format(time.RFC3339Nano),
		InstallID:    install,
		Agent:        agent,
		Provider:     provider,
		BillingBasis: basis,
		Stages: map[string]any{
			"compress": map[string]string{"status": compress},
			"route":    map[string]string{"status": route},
			"meter":    map[string]string{"status": "ok"},
		},
		ModelRequested: bounded128(rec.RouteFrom),
		ModelUsed:      bounded128(rec.Model),
	}
	if rec.TokenUsageBasis == "provider_complete" || rec.TokenUsageBasis == "provider_partial" {
		event.Tokens = &eventTokens{Label: "measured", Input: count(rec.InputTokens), Output: count(rec.OutputTokens),
			CacheRead: count(rec.CachedInputTokens), CacheWrite: count(rec.CacheCreationInputTokens)}
	}
	if kept := rec.CompressionTokensBefore - rec.CompressionTokensAfter; kept > 0 {
		event.KeptOutOfContext = &eventKeptCount{Label: "inferred", Tokens: count(kept)}
	}
	if rec.RouteOutcome != "" {
		event.Route = &eventRoute{Outcome: rec.RouteOutcome, Reason: bounded(rec.RouteReason)}
		if uuidRE.MatchString(rec.RouteDecisionID) {
			event.Route.DecisionID = rec.RouteDecisionID
		}
	}
	return event
}

// atLevel keeps only the fields the data level allows.
func (e runtimeEvent) atLevel(level string) runtimeEvent {
	e.Level = level
	if level == "counts" {
		e.ModelRequested, e.ModelUsed, e.Tokens, e.KeptOutOfContext = "", "", nil, nil
	}
	if level != "decisions" {
		e.Route = nil
	}
	return e
}

func (l *Link) sendLoop() {
	q := &l.events
	ticker := time.NewTicker(q.every)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
		case <-q.full:
		}
		l.flush()
	}
}

// flush sends what is queued, up to eventBatchMax per request, and drops a
// batch that fails.
func (l *Link) flush() {
	q := &l.events
	q.flushing.Lock()
	defer q.flushing.Unlock()
	for len(q.ch) > 0 {
		batch := make([]runtimeEvent, 0, eventBatchMax)
		for len(batch) < eventBatchMax && len(q.ch) > 0 {
			batch = append(batch, <-q.ch)
		}
		cfg := l.settings()
		if !cfg.routing || cfg.offline || !cfg.signedIn() {
			continue
		}
		level := l.dataLevel(cfg)
		if level != "counts" && level != "usage" && level != "decisions" {
			continue
		}
		for i := range batch {
			batch[i] = batch[i].atLevel(level)
		}
		raw, _ := json.Marshal(map[string]any{"schema_version": 1, "events": batch})
		if status, err := l.call(cfg, http.MethodPost, "/api/v1/runtime/events", raw, nil); (err != nil || status >= 300) && l.logger != nil {
			l.logger.Debug("runtime/v1 batch dropped", "events", len(batch), "status", status, "error", err)
		}
	}
}

// dataLevel is /me's data.level, re-read every levelTTL. A /me without one
// means counts; a /me that cannot be read keeps the last answer.
func (l *Link) dataLevel(cfg settings) string {
	q := &l.events
	if q.levelAt.IsZero() || l.now().Sub(q.levelAt) >= levelTTL {
		q.levelAt = l.now()
		var me struct {
			Data struct {
				Level string `json:"level"`
			} `json:"data"`
		}
		if status, err := l.call(cfg, http.MethodGet, "/api/v1/auth/me", nil, &me); err == nil && status == http.StatusOK {
			q.level = me.Data.Level
			if q.level == "" {
				q.level = "counts"
			}
		}
	}
	return q.level
}

func (l *Link) call(cfg settings, method, path string, body []byte, out any) (int, error) {
	bearer := cfg.apiBearer(l.now())
	if bearer == "" {
		return 0, fmt.Errorf("no usable credential")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, method, cfg.cloud+path, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	request.Header.Set("authorization", "Bearer "+bearer)
	request.Header.Set("x-cave-client", "caveman-proxy")
	if body != nil {
		request.Header.Set("content-type", "application/json")
	}
	response, err := l.client.Do(request)
	if err != nil {
		return 0, err
	}
	defer response.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(response.Body, answerMax))
	if out != nil {
		_ = json.Unmarshal(raw, out)
	}
	return response.StatusCode, nil
}

func count(n int) int {
	return min(max(n, 0), 100_000_000)
}

func bounded128(text string) string {
	if len(text) > 128 {
		return text[:128]
	}
	return text
}

func newUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
