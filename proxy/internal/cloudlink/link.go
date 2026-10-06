// Package cloudlink is the signed-in local runtime's connection to Caveman
// Cloud: the route stage's ask (POST /v1/route) and the runtime/v1 event sender
// (POST /api/v1/runtime/events). It reads the CLI's own state on every use —
// $CAVEMAN_HOME/cloud.json and the credential store the CLI wrote — so signing
// in or out, or switching the routing module, takes effect without a restart.
//
// It never touches compression or any other local stage, and every failure
// fails open: a Cloud error, timeout, 401 or allowance answer keeps the model
// the agent asked for. The ask carries the caller's models, counts and the
// conversation's text Cloud picks the model from: the latest human turn, the
// one before it and the end of the agent's last reply (contracts route-ask-v1).
// Events carry counts and labels only, never prompt text.
package cloudlink

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/JuliusBrussee/caveman/proxy/internal/gateway"
	"github.com/JuliusBrussee/caveman/proxy/providers/jsonsplice"
)

const (
	routeBudget  = 800 * time.Millisecond
	failurePause = time.Minute      // after a timeout, network error or 5xx
	pauseRecheck = 15 * time.Minute // how often a limit pause asks /me whether it still holds
	refusedPause = 10 * time.Minute // after a 401/403: a stale or revoked login
	decisionsMax = 1024
	answerMax    = 1 << 20
)

// pools are the models of one provider a request may move between, as wire
// model ids; they are the `models` an ask sends. A request is asked about only
// when the agent asked for one of them, and the asked model goes first (Cloud's
// fallback is models[0]).
// ponytail: static pools; take them from Cloud once it publishes a default.
var pools = map[string][]string{
	"anthropic": {"claude-opus-5-5", "claude-sonnet-5-5"},
	"openai":    {"gpt-6.1-sol", "gpt-6-sol", "gpt-6-astra", "gpt-6-luna"},
}

// Link implements gateway.CloudLink.
type Link struct {
	home   string
	client *http.Client
	logger *slog.Logger
	now    func() time.Time
	// keychain reads the CLI's macOS keychain entry; tests replace it.
	keychain func() string

	mu         sync.Mutex
	stamp      string
	loaded     bool
	stale      bool // a background keychain read found a new secret
	cfg        settings
	decisions  map[string]*decision
	pauseUntil time.Time
	paused     gateway.RouteAnswer // what an ask answers while paused
	recheckAt  time.Time           // the last /me check of a limit pause
	rechecking bool
	pausePlan  string // the plan /me named while the limit held

	events eventQueue

	kmu    sync.Mutex
	kcache string
	kbusy  bool
	kgen   int // bumped by every refresh request
}

type decision struct {
	done   chan struct{}
	answer gateway.RouteAnswer
}

// New returns a link reading the CLI state under home ($CAVEMAN_HOME).
func New(home string, logger *slog.Logger) *Link {
	return &Link{
		home:     home,
		client:   &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
		logger:   logger,
		now:      time.Now,
		keychain: macKeychain,
	}
}

// settings is what the link needs from the CLI's state.
type settings struct {
	routing bool   // modules.routing is explicitly on
	cloud   string // control API base URL
	gateway string // where /v1/route lives
	key     string // the project gateway key from the device login, if any
	access  string // the session access token
	install string // opaque per-machine id
	offline bool   // CAVEMAN_OFFLINE=1: benchmark and air-gapped runs send nothing
	// The CLI telemetry opt-out (telemetry.enabled false, DO_NOT_TRACK,
	// CAVEMAN_TELEMETRY=0): no runtime events.
	noTelemetry bool
}

func (s settings) signedIn() bool { return s.cloud != "" && (s.key != "" || s.access != "") }

func (l *Link) cloudPaths() (current, legacy, credentials string) {
	userHome := os.Getenv("HOME")
	if userHome == "" {
		userHome = filepath.Dir(l.home)
	}
	return filepath.Join(l.home, "cloud.json"), filepath.Join(userHome, ".caveman-cloud", "config.json"), filepath.Join(l.home, "credentials")
}

// settings re-reads the CLI state when cloud.json or the credentials file
// changed (login, logout, token refresh and `caveman on|off` all rewrite
// cloud.json); otherwise it is two stat calls.
func (l *Link) settings() settings {
	current, legacy, credentials := l.cloudPaths()
	stamp := ""
	for _, path := range []string{current, legacy, credentials} {
		if info, err := os.Stat(path); err == nil {
			stamp += fmt.Sprintf("%s:%d:%d;", path, info.Size(), info.ModTime().UnixNano())
		}
	}
	l.mu.Lock()
	if l.loaded && stamp == l.stamp && !l.stale {
		defer l.mu.Unlock()
		return l.cfg
	}
	filesChanged := !l.loaded || stamp != l.stamp
	l.stale = false
	l.mu.Unlock()
	cfg := l.load(current, legacy, credentials, filesChanged)
	l.mu.Lock()
	defer l.mu.Unlock()
	if filesChanged || cfg != l.cfg {
		previous := l.cfg
		l.cfg, l.stamp, l.loaded = cfg, stamp, true
		if (cfg.access != "" || cfg.key != "") && (cfg.access != previous.access || cfg.key != previous.key) {
			// A new login starts fresh: no pause and no decision from the old one.
			// Signing out needs no reset: nothing is asked while signed out.
			l.pauseUntil, l.decisions = time.Time{}, nil
			l.forgetLocked()
		}
	}
	return l.cfg
}

// keychainSecret is the last keychain read; a read never runs on a request
// goroutine. refresh (cloud.json changed: a login, logout or token refresh)
// starts a background read and keeps serving the cached secret until it lands.
// A change during an in-flight read bumps the generation, which schedules
// exactly one more read, so no pre-login secret lingers. Only a non-empty,
// different secret replaces the cache and marks the settings stale; an empty
// read (a locked keychain) changes nothing, and logout is cloud.json's job.
func (l *Link) keychainSecret(refresh bool) string {
	l.kmu.Lock()
	defer l.kmu.Unlock()
	if refresh {
		l.kgen++
		if !l.kbusy {
			l.kbusy = true
			go l.readKeychain()
		}
	}
	return l.kcache
}

func (l *Link) readKeychain() {
	for {
		l.kmu.Lock()
		gen := l.kgen
		l.kmu.Unlock()
		secret := l.keychain()
		l.kmu.Lock()
		changed := secret != "" && secret != l.kcache
		if changed {
			l.kcache = secret
		}
		again := l.kgen != gen
		l.kbusy = again
		l.kmu.Unlock()
		if changed {
			l.mu.Lock()
			l.stale = true
			l.mu.Unlock()
		}
		if !again {
			return
		}
	}
}

func (l *Link) load(current, legacy, credentials string, refreshKeychain bool) settings {
	out := settings{offline: os.Getenv("CAVEMAN_OFFLINE") == "1", noTelemetry: telemetryEnvOff()}
	raw, err := os.ReadFile(current)
	if os.IsNotExist(err) {
		raw, err = os.ReadFile(legacy)
	}
	var doc map[string]any
	if err != nil || json.Unmarshal(raw, &doc) != nil {
		return out
	}
	if modules, ok := doc["modules"].(map[string]any); ok {
		out.routing = modules["routing"] == true
	}
	if telemetry, ok := doc["telemetry"].(map[string]any); ok && telemetry["enabled"] == false {
		out.noTelemetry = true
	}
	out.cloud = safeBase(stringOf(doc["baseURL"]))
	out.gateway = safeBase(stringOf(doc["gatewayUrl"]))
	if out.gateway == "" {
		out.gateway = out.cloud
	}
	secret := ""
	switch store := stringOf(doc["tokenStore"]); {
	case doc["logoutPendingLocalCleanup"] == true:
		// A logout revoked the session, then stopped before its final save: the
		// stored token is dead, so this is signed out (no asks, no events).
	case store == "file":
		if raw, err := os.ReadFile(credentials); err == nil {
			secret = string(raw)
		}
	case store == "keychain":
		secret = l.keychainSecret(refreshKeychain)
	default:
		secret = stringOf(doc["token"]) // legacy inline token
	}
	out.access, out.key = decodeCredentials(secret)
	id := stringOf(doc["deviceId"])
	if id == "" {
		id = l.home
	}
	sum := sha256.Sum256([]byte(id))
	out.install = hex.EncodeToString(sum[:])
	return out
}

// telemetryEnvOff mirrors the CLI: DO_NOT_TRACK set (and not 0), or
// CAVEMAN_TELEMETRY set to anything but 1/true/on.
func telemetryEnvOff() bool {
	if dnt := os.Getenv("DO_NOT_TRACK"); dnt != "" && dnt != "0" {
		return true
	}
	value := strings.ToLower(strings.TrimSpace(os.Getenv("CAVEMAN_TELEMETRY")))
	return os.Getenv("CAVEMAN_TELEMETRY") != "" && value != "1" && value != "true" && value != "on"
}

func stringOf(value any) string {
	text, _ := value.(string)
	return strings.TrimSpace(text)
}

// decodeCredentials reads the CLI's credential envelope ({access_token,
// gateway_api_key, …}) or a legacy bare access token.
func decodeCredentials(secret string) (access, key string) {
	secret = strings.TrimSpace(secret)
	var envelope struct {
		AccessToken   string `json:"access_token"`
		GatewayAPIKey string `json:"gateway_api_key"`
	}
	if json.Unmarshal([]byte(secret), &envelope) == nil && envelope.AccessToken != "" {
		return envelope.AccessToken, envelope.GatewayAPIKey
	}
	if strings.HasPrefix(secret, "{") {
		return "", ""
	}
	return secret, ""
}

// safeBase accepts https, or plain http only on loopback: a credential never
// crosses the network in the clear.
func safeBase(raw string) string {
	parsed, err := url.Parse(strings.TrimSuffix(raw, "/"))
	if err != nil || parsed.Host == "" {
		return ""
	}
	host := parsed.Hostname()
	ip := net.ParseIP(host)
	if parsed.Scheme == "https" || parsed.Scheme == "http" && (host == "localhost" || ip != nil && ip.IsLoopback()) {
		return strings.TrimSuffix(parsed.String(), "/")
	}
	return ""
}

func macKeychain() string {
	if runtime.GOOS != "darwin" || os.Getenv("CAVE_NO_KEYCHAIN") != "" {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "security", "find-generic-password", "-s", "caveman", "-a", "token", "-w").Output()
	if err != nil {
		return ""
	}
	return string(out)
}

// fresh reports an access token that is not about to expire. Cloud's tokens
// carry exp in their base64url JSON payload (the first dot-separated part).
func fresh(token string, now time.Time) bool {
	if token == "" {
		return false
	}
	payload, _, _ := strings.Cut(token, ".")
	raw, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		return true // not a token this check understands; let Cloud decide
	}
	var claims struct {
		Exp *float64 `json:"exp"`
	}
	if json.Unmarshal(raw, &claims) != nil || claims.Exp == nil {
		return true
	}
	return int64(*claims.Exp) > now.Add(30*time.Second).Unix()
}

// bearer is the signed-in session's token while it is fresh (Cloud takes
// device-login tokens on /v1/route and /api), else the durable project key the
// device login issued. The CLI owns refreshing the session; the proxy never
// spends the refresh token.
func (s settings) bearer(now time.Time) string {
	if fresh(s.access, now) {
		return s.access
	}
	return s.key
}

// Ask starts the decision for one request and returns the wait for it. The
// wait never outlasts routeBudget from the ask.
func (l *Link) Ask(_ context.Context, ask gateway.RouteAsk) func() gateway.RouteAnswer {
	started := time.Now() // the budget is wall time; l.now drives pauses
	result := make(chan gateway.RouteAnswer, 1)
	go func() {
		defer func() {
			if recover() != nil {
				result <- gateway.RouteAnswer{Outcome: "degraded", Reason: "route_stage_panic"}
			}
		}()
		result <- l.decide(ask, started.Add(routeBudget))
	}()
	return func() gateway.RouteAnswer {
		select { // an answer that is already here wins over a spent budget
		case answer := <-result:
			return answer
		default:
		}
		wait := routeBudget - time.Since(started)
		if wait < 0 {
			wait = 0
		}
		timer := time.NewTimer(wait)
		defer timer.Stop()
		select {
		case answer := <-result:
			return answer
		case <-timer.C:
			// Pause now: the ask's own deadline lands at the same moment, and
			// the next request must not wait on a slow Cloud again.
			return l.pause(failurePause, gateway.RouteAnswer{Outcome: "degraded", Reason: "timeout"})
		}
	}
}

func (l *Link) decide(ask gateway.RouteAsk, deadline time.Time) gateway.RouteAnswer {
	cfg := l.settings()
	if !cfg.routing || cfg.offline || !cfg.signedIn() {
		return gateway.RouteAnswer{Outcome: "off"}
	}
	models := poolFor(ask.Provider, ask.Model)
	if models == nil {
		return gateway.RouteAnswer{Outcome: "off", Reason: "model_outside_pool"}
	}
	bearer := cfg.bearer(l.now())
	if bearer == "" {
		return gateway.RouteAnswer{Outcome: "degraded", Reason: "login_expired"}
	}
	root, ok := jsonsplice.Root(ask.Body)
	if ok && statefulChain(ask.Body, root) {
		// A Responses chain's follow-ups carry no human text to key a decision
		// on, so routing it would switch model halfway through a turn.
		return gateway.RouteAnswer{Outcome: "off", Reason: "stateful_chain"}
	}
	text := askTextFor(ask.Endpoint, ask.Body, root)
	if text.Text == "" {
		return gateway.RouteAnswer{Outcome: "kept", Reason: "no_human_text"}
	}
	key := sha256.Sum256([]byte(ask.SessionID + "\x00" + ask.Provider + "\x00" + ask.Model + "\x00" + text.Text))
	// Every tool-loop turn of one ask reuses its answer, failures included, so
	// a turn never switches model halfway; a pause only stops new asks.
	l.mu.Lock()
	d, seen := l.decisions[string(key[:])]
	if !seen && l.now().Before(l.pauseUntil) {
		paused := l.paused
		l.mu.Unlock()
		l.recheckPause(cfg)
		return paused
	}
	if !seen {
		if l.decisions == nil || len(l.decisions) >= decisionsMax {
			l.decisions = map[string]*decision{} // ponytail: drop all when full; an LRU if hit rates ever matter
		}
		d = &decision{done: make(chan struct{})}
		l.decisions[string(key[:])] = d
	}
	l.mu.Unlock()
	if !seen {
		func() {
			defer close(d.done) // waiters on this ask never hang, even on a panic
			d.answer = l.ask(cfg, bearer, ask, text, models, deadline)
		}()
	}
	<-d.done
	answer := d.answer
	if answer.Model != "" {
		k := string(key[:])
		answer.Reject = func() {
			// The provider refused the routed model: the rest of this ask keeps
			// the asked one instead of failing over on every turn.
			done := make(chan struct{})
			close(done)
			l.mu.Lock()
			if l.decisions != nil {
				l.decisions[k] = &decision{done: done, answer: gateway.RouteAnswer{Outcome: "degraded", Reason: "provider_rejected_routed_model"}}
			}
			l.mu.Unlock()
		}
	}
	return answer
}

func statefulChain(body []byte, root jsonsplice.Span) bool {
	span, ok := jsonsplice.Field(body, root, "previous_response_id")
	return ok && string(body[span.Start:span.End]) != "null"
}

func poolFor(provider, model string) []string {
	pool := pools[provider]
	if !slices.Contains(pool, model) {
		return nil
	}
	return append([]string{model}, slices.DeleteFunc(slices.Clone(pool), func(id string) bool { return id == model })...)
}

var (
	toolErrorRE = regexp.MustCompile(`"is_error"\s*:\s*true`)
	imageRE     = regexp.MustCompile(`"type"\s*:\s*"(?:image|image_url|input_image)"`)
)

// routeAsk is POST /v1/route's body (contracts route-ask-v1): the caller's
// models, counts computed on this machine, and the conversation's text.
type routeAsk struct {
	Models  []string `json:"models"`
	Signals signals  `json:"signals"`
	Ask     askText  `json:"ask"`
}

// The contract's bounds, in bytes.
const (
	askBodyMax = 256 << 10
	askTextMax = 128 << 10
	askSideMax = 16 << 10
	askTurnMax = 1_000_000
)

// askText is the conversation the ask carries, raw: Cloud reads it as is.
// Text is the latest human turn, PrevText the human turn before it, ReplyTail
// the end of the newest assistant text before the latest turn, and Turn the
// number of human turns before the latest one.
type askText struct {
	Text      string `json:"text"`
	PrevText  string `json:"prev_text,omitempty"`
	ReplyTail string `json:"reply_tail,omitempty"`
	Turn      int    `json:"turn,omitempty"`
}

// askTextFor reads askText out of an Anthropic Messages, OpenAI chat or OpenAI
// Responses body in one walk over its message spans; only text blocks are
// decoded. A human turn is a user message with text: one carrying only tool
// results is not. Each field is cut to its bound on a rune boundary.
func askTextFor(endpoint string, body []byte, root jsonsplice.Span) askText {
	field := "messages"
	if strings.HasSuffix(endpoint, "/responses") {
		field = "input"
	}
	list, _ := jsonsplice.Field(body, root, field)
	if text, ok := jsonsplice.String(body, list); ok { // a Responses input string is one human turn
		if strings.TrimSpace(text) == "" {
			return askText{}
		}
		return askText{Text: truncate(text, askTextMax)}
	}
	items, _ := jsonsplice.Elements(body, list)
	var out askText
	humans, replied := 0, false
	for i := len(items) - 1; i >= 0; i-- {
		switch role, _ := jsonsplice.StringField(body, items[i], "role"); {
		case role == "user":
			text, ok := messageText(body, items[i], "text", "input_text")
			if !ok {
				continue
			}
			switch humans {
			case 0:
				out.Text = truncate(text, askTextMax)
			case 1:
				out.PrevText = truncate(text, askSideMax)
			}
			humans++
		case role == "assistant" && humans > 0 && !replied:
			if text, ok := messageText(body, items[i], "text", "output_text"); ok {
				out.ReplyTail, replied = tail(text, askSideMax), true
			}
		}
	}
	out.Turn = min(max(humans-1, 0), askTurnMax)
	return out
}

// messageText joins a message's text blocks with "\n"; a string content is one
// block. ok is false when no block has any non-space text.
func messageText(body []byte, message jsonsplice.Span, types ...string) (string, bool) {
	content, _ := jsonsplice.Field(body, message, "content")
	if text, ok := jsonsplice.String(body, content); ok {
		return text, strings.TrimSpace(text) != ""
	}
	blocks, _ := jsonsplice.Elements(body, content)
	var texts []string
	found := false
	for _, block := range blocks {
		if kind, _ := jsonsplice.StringField(body, block, "type"); !slices.Contains(types, kind) {
			continue
		}
		if text, ok := jsonsplice.StringField(body, block, "text"); ok {
			texts = append(texts, text)
			found = found || strings.TrimSpace(text) != ""
		}
	}
	return strings.Join(texts, "\n"), found
}

// askBody is the ask's JSON within askBodyMax. Escaping can grow text past it
// (a control byte encodes as six), so then text keeps its longest head that
// fits. The other fields always fit: even escaped they stay under 200 KiB.
func askBody(ask routeAsk) []byte {
	raw, _ := json.Marshal(ask)
	if len(raw) <= askBodyMax {
		return raw
	}
	text := ask.Ask.Text
	n := sort.Search(len(text)+1, func(n int) bool {
		ask.Ask.Text = truncate(text, n)
		raw, _ = json.Marshal(ask)
		return len(raw) > askBodyMax
	}) - 1
	ask.Ask.Text = truncate(text, n)
	raw, _ = json.Marshal(ask)
	return raw
}

type signals struct {
	Agent         string `json:"agent"`
	ContextTokens int    `json:"context_tokens"`
	ToolsDeclared int    `json:"tools_declared"`
	ToolErrors    int    `json:"tool_errors"`
	Images        bool   `json:"images"`
}

// signalsFor counts what the ask carries. Tool errors are counted over the
// whole request; context tokens are estimated from its size.
func signalsFor(ask gateway.RouteAsk) signals {
	agent := ask.Agent
	if !slugRE.MatchString(agent) {
		agent = "unlabeled-agent"
	}
	return signals{
		Agent:         agent,
		ContextTokens: min(ask.InputBytes/4, 1_000_000_000),
		ToolsDeclared: min(ask.ToolsCount, 1_000_000_000),
		ToolErrors:    len(toolErrorRE.FindAllIndex(ask.Body, -1)),
		Images:        imageRE.Match(ask.Body),
	}
}

func (l *Link) ask(cfg settings, bearer string, ask gateway.RouteAsk, text askText, models []string, deadline time.Time) gateway.RouteAnswer {
	raw := askBody(routeAsk{Models: models, Signals: signalsFor(ask), Ask: text})
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.gateway+"/v1/route", bytes.NewReader(raw))
	if err != nil {
		return l.pause(failurePause, gateway.RouteAnswer{Outcome: "degraded", Reason: "bad_cloud_url"})
	}
	request.Header.Set("content-type", "application/json")
	request.Header.Set("authorization", "Bearer "+bearer)
	response, err := l.client.Do(request)
	if err != nil {
		reason := "cloud_unreachable"
		if errors.Is(err, context.DeadlineExceeded) {
			reason = "timeout"
		}
		return l.pause(failurePause, gateway.RouteAnswer{Outcome: "degraded", Reason: reason})
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(response.Body, answerMax))
	var answer struct {
		Model      string `json:"model"`
		Reason     string `json:"reason"`
		DecisionID string `json:"decision_id"`
		Notice     string `json:"notice"`
		Error      struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	unreadable := json.Unmarshal(body, &answer) != nil
	limit := answer.Reason
	for _, known := range []string{"allowance", "billing_limit"} {
		if strings.Contains(answer.Error.Code, known) {
			limit = known
		}
	}
	if limit == "allowance" || limit == "billing_limit" {
		// Free routing used up: back to local until the 1st. A billing limit
		// can be raised at any time, so it is asked again sooner.
		wait := refusedPause
		if limit == "allowance" {
			now := l.now().UTC()
			wait = time.Date(now.Year(), now.Month()+1, 1, 0, 0, 0, 0, time.UTC).Sub(now)
		}
		return l.remember(l.pause(wait, gateway.RouteAnswer{Outcome: "paused", Reason: limit}), answer.Notice)
	}
	switch {
	case response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden:
		// A key minted before it could route (no router:write) or a revoked
		// login: `caveman login` mints a new one, which also lifts this pause.
		return l.remember(l.pause(refusedPause, gateway.RouteAnswer{Outcome: "degraded", Reason: fmt.Sprintf("cloud_%d", response.StatusCode)}), "")
	case response.StatusCode != http.StatusOK:
		return l.pause(failurePause, gateway.RouteAnswer{Outcome: "degraded", Reason: fmt.Sprintf("cloud_%d", response.StatusCode)})
	case unreadable:
		return l.pause(failurePause, gateway.RouteAnswer{Outcome: "degraded", Reason: "answer_unreadable"})
	case !slices.Contains(models, answer.Model):
		return gateway.RouteAnswer{Outcome: "degraded", Reason: "answer_outside_pool"}
	}
	l.forget()
	if answer.Model == ask.Model {
		return gateway.RouteAnswer{Outcome: "kept", Reason: truncate(answer.Reason, 64), DecisionID: answer.DecisionID}
	}
	return gateway.RouteAnswer{Model: answer.Model, Outcome: "routed", Reason: truncate(answer.Reason, 64), DecisionID: answer.DecisionID}
}

// remember writes a pause the person can act on (a refused key, a used-up
// allowance, a billing limit) to $CAVEMAN_HOME/route-state.json, where
// `caveman status` reads it; Cloud's notice rides along.
func (l *Link) remember(what gateway.RouteAnswer, notice string) gateway.RouteAnswer {
	l.mu.Lock()
	defer l.mu.Unlock()
	notice = truncate(notice, 240)
	raw, _ := json.Marshal(map[string]string{"outcome": what.Outcome, "reason": what.Reason, "notice": notice, "until": l.pauseUntil.UTC().Format(time.RFC3339)})
	path := filepath.Join(l.home, "route-state.json")
	if os.WriteFile(path+".tmp", raw, 0o600) == nil {
		_ = os.Rename(path+".tmp", path)
	}
	return what
}

func (l *Link) forget() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.forgetLocked()
}

// forgetLocked removes the pause record, also one an earlier proxy left.
func (l *Link) forgetLocked() {
	_ = os.Remove(filepath.Join(l.home, "route-state.json"))
}

// pause stops asking for d; asks meanwhile answer with what.
func (l *Link) pause(d time.Duration, what gateway.RouteAnswer) gateway.RouteAnswer {
	l.mu.Lock()
	l.pauseUntil, l.paused = l.now().Add(d), what
	l.recheckAt, l.pausePlan = l.now(), ""
	l.mu.Unlock()
	if l.logger != nil {
		l.logger.Warn("route stage paused; requests keep the model the agent asked for", "reason", what.Reason, "for", d.Round(time.Second).String())
	}
	return what
}

type meProduct struct {
	ID    string `json:"id"`
	State string `json:"state"`
}

// recheckPause lifts an allowance or billing-limit pause early once /me says
// routing is no longer limited or the plan changed (a card added, a limit
// raised). At most one /me per pauseRecheck, in the background; a /me without
// products changes nothing.
func (l *Link) recheckPause(cfg settings) {
	l.mu.Lock()
	if l.paused.Outcome != "paused" || l.rechecking || l.now().Sub(l.recheckAt) < pauseRecheck {
		l.mu.Unlock()
		return
	}
	l.rechecking, l.recheckAt = true, l.now()
	l.mu.Unlock()
	go func() {
		defer func() {
			l.mu.Lock()
			l.rechecking = false
			l.mu.Unlock()
		}()
		var me struct {
			Plan     string      `json:"plan"`
			Products []meProduct `json:"products"`
		}
		if status, err := l.call(cfg, http.MethodGet, "/api/v1/auth/me", nil, &me); err != nil || status != http.StatusOK || me.Products == nil {
			return
		}
		limited := slices.ContainsFunc(me.Products, func(p meProduct) bool { return p.ID == "routing" && p.State == "limited" })
		l.mu.Lock()
		defer l.mu.Unlock()
		if l.paused.Outcome != "paused" {
			return
		}
		if !limited || l.pausePlan != "" && me.Plan != l.pausePlan {
			l.pauseUntil, l.paused, l.pausePlan = time.Time{}, gateway.RouteAnswer{}, ""
			l.forgetLocked()
			return
		}
		l.pausePlan = me.Plan
	}()
}

// truncate cuts text to at most n bytes on a rune boundary.
func truncate(text string, n int) string {
	if len(text) <= n {
		return text
	}
	for n > 0 && !utf8.RuneStart(text[n]) {
		n--
	}
	return text[:n]
}

// tail keeps the last n bytes of text, cut on a rune boundary.
func tail(text string, n int) string {
	if len(text) <= n {
		return text
	}
	start := len(text) - n
	for start < len(text) && !utf8.RuneStart(text[start]) {
		start++
	}
	return text[start:]
}
