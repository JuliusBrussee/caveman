// Package cloudlink is the signed-in local runtime's connection to Caveman
// Cloud: the route stage's ask (POST /v1/route) and the runtime/v1 event sender
// (POST /api/v1/runtime/events). It reads the CLI's own state on every use —
// $CAVEMAN_HOME/cloud.json and the credential store the CLI wrote — so signing
// in or out, or switching the routing module, takes effect without a restart.
//
// It never touches compression or any other local stage, and every failure
// fails open: a Cloud error, timeout, 401 or allowance answer keeps the model
// the agent asked for. No prompt text leaves the machine: the ask's text is one
// line of counted features, and events carry counts and labels only.
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
	"strings"
	"sync"
	"time"

	"github.com/JuliusBrussee/caveman/proxy/internal/gateway"
)

const (
	routeBudget  = 800 * time.Millisecond
	failurePause = time.Minute      // after a timeout, network error or 5xx
	refusedPause = 10 * time.Minute // after a 401/403: a stale or revoked login
	decisionsMax = 1024
	answerMax    = 1 << 20
)

// pools are the models of one provider routing may move a request between, as
// wire model ids. A request is routed only when the agent asked for one of them,
// so a cheap background call never moves up a tier. The asked model goes first:
// Cloud's fallback is models[0].
// ponytail: static pools; take Cloud's default pool once the decision service
// publishes one (tiers spec §3 step 3).
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
	cfg        settings
	decisions  map[string]*decision
	pauseUntil time.Time
	paused     gateway.RouteAnswer // what an ask answers while paused

	events eventQueue
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
	defer l.mu.Unlock()
	if stamp != l.stamp || l.stamp == "" {
		l.cfg = l.load(current, legacy, credentials)
		l.stamp = stamp
	}
	return l.cfg
}

func (l *Link) load(current, legacy, credentials string) settings {
	out := settings{offline: os.Getenv("CAVEMAN_OFFLINE") == "1"}
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
	out.cloud = safeBase(stringOf(doc["baseURL"]))
	out.gateway = safeBase(stringOf(doc["gatewayUrl"]))
	if out.gateway == "" {
		out.gateway = out.cloud
	}
	secret := ""
	switch stringOf(doc["tokenStore"]) {
	case "file":
		if raw, err := os.ReadFile(credentials); err == nil {
			secret = string(raw)
		}
	case "keychain":
		secret = l.keychain()
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

// routeBearer prefers the durable project key; Cloud calls under /api prefer
// the session token while it is fresh.
func (s settings) routeBearer(now time.Time) string {
	if s.key != "" {
		return s.key
	}
	if fresh(s.access, now) {
		return s.access
	}
	return ""
}

func (s settings) apiBearer(now time.Time) string {
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
	bearer := cfg.routeBearer(l.now())
	if bearer == "" {
		return gateway.RouteAnswer{Outcome: "degraded", Reason: "login_expired"}
	}
	query := gateway.LatestHumanText(ask.Provider, ask.Endpoint, ask.Body)
	if query == "" {
		return gateway.RouteAnswer{Outcome: "kept", Reason: "no_human_text"}
	}
	key := sha256.Sum256([]byte(ask.SessionID + "\x00" + ask.Provider + "\x00" + ask.Model + "\x00" + query))
	// Every tool-loop turn of one ask reuses its answer, failures included, so
	// a turn never switches model halfway; a pause only stops new asks.
	l.mu.Lock()
	d, seen := l.decisions[string(key[:])]
	if !seen && l.now().Before(l.pauseUntil) {
		paused := l.paused
		l.mu.Unlock()
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
		d.answer = l.ask(cfg, bearer, ask, models, deadline)
		close(d.done)
	}
	<-d.done
	return d.answer
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

// features is the only text an ask carries: the router daemon's zero-retention
// features line, computed from counts. No prompt text.
// ponytail: tool errors are counted over the whole request, not the recent
// turns; narrow it if the classifier ever weighs old failures wrongly.
func features(ask gateway.RouteAsk) string {
	harness := ask.Agent
	if harness == "" || strings.ContainsAny(harness, " \t\r\n=") {
		harness = "unlabeled-agent"
	}
	return fmt.Sprintf("routerd features: harness=%s context_tokens=%d tools_declared=%d recent_tool_errors=%d images=%t",
		harness, ask.InputBytes/4, ask.ToolsCount, len(toolErrorRE.FindAllIndex(ask.Body, -1)), imageRE.Match(ask.Body))
}

// routeBody is POST /v1/route's body: the pool and the features line.
type routeBody struct {
	Models []string `json:"models"`
	Text   string   `json:"text"`
}

func (l *Link) ask(cfg settings, bearer string, ask gateway.RouteAsk, models []string, deadline time.Time) gateway.RouteAnswer {
	raw, _ := json.Marshal(routeBody{Models: models, Text: features(ask)})
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
		Error      struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(body, &answer)
	if answer.Reason == "allowance" || strings.Contains(answer.Error.Code, "allowance") {
		// Free routing is used up for the period: back to local until the 1st.
		now := l.now().UTC()
		first := time.Date(now.Year(), now.Month()+1, 1, 0, 0, 0, 0, time.UTC)
		return l.pause(first.Sub(now), gateway.RouteAnswer{Outcome: "paused", Reason: "allowance"})
	}
	switch {
	case response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden:
		return l.pause(refusedPause, gateway.RouteAnswer{Outcome: "degraded", Reason: fmt.Sprintf("cloud_%d", response.StatusCode)})
	case response.StatusCode != http.StatusOK:
		return l.pause(failurePause, gateway.RouteAnswer{Outcome: "degraded", Reason: fmt.Sprintf("cloud_%d", response.StatusCode)})
	case !slices.Contains(models, answer.Model):
		return gateway.RouteAnswer{Outcome: "degraded", Reason: "answer_outside_pool"}
	case answer.Model == ask.Model:
		return gateway.RouteAnswer{Outcome: "kept", Reason: bounded(answer.Reason), DecisionID: answer.DecisionID}
	}
	return gateway.RouteAnswer{Model: answer.Model, Outcome: "routed", Reason: bounded(answer.Reason), DecisionID: answer.DecisionID}
}

// pause stops asking for d; asks meanwhile answer with what.
func (l *Link) pause(d time.Duration, what gateway.RouteAnswer) gateway.RouteAnswer {
	l.mu.Lock()
	l.pauseUntil, l.paused = l.now().Add(d), what
	l.mu.Unlock()
	if l.logger != nil {
		l.logger.Warn("route stage paused; requests keep the model the agent asked for", "reason", what.Reason, "for", d.Round(time.Second).String())
	}
	return what
}

func bounded(text string) string {
	if len(text) > 64 {
		return text[:64]
	}
	return text
}
