// Package cloudlink is the signed-in local runtime's connection to Caveman
// Cloud: the route stage's ask (POST /v1/route) and the runtime/v1 event sender
// (POST /api/v1/runtime/events). It reads the CLI's own state on every use —
// $CAVEMAN_HOME/cloud.json and the credential store the CLI wrote — so signing
// in or out, or switching the routing module, takes effect without a restart.
//
// It never touches compression or any other local stage, and every failure
// fails open: a Cloud error, timeout, 401 or allowance answer keeps the asked
// model and runs at the request's own effort (the gateway keeps a session's
// per-message marks and puts the agent's effort back with one). The ask carries the caller's models, counts, what the
// request declares (labels, tool names, effort), what the session's previous
// request ran, and the conversation's text Cloud picks the model and effort
// from: the latest human turn, the one before it and the end of the agent's
// last reply (contracts route-ask-v1). Events carry counts and labels only,
// never prompt text.
package cloudlink

import (
	"bytes"
	"container/list"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
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
	"github.com/JuliusBrussee/caveman/proxy/internal/pool"
	"github.com/JuliusBrussee/caveman/proxy/internal/translate"
	"github.com/JuliusBrussee/caveman/proxy/providers/jsonsplice"
)

const (
	routeBudget  = 800 * time.Millisecond
	failurePause = time.Minute      // after a timeout, network error or 5xx
	pauseRecheck = 15 * time.Minute // how often a limit pause asks /me whether it still holds
	refusedPause = 10 * time.Minute // after a 401/403: a stale or revoked login
	decisionsMax = 1024
	statesMax    = 1024
	stateMax     = 4096 // bytes of Cloud's opaque state kept and sent back
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
	// logins are the provider logins the person added (`caveman providers
	// add|login`): the pool an ask sends beyond the harness's own models.
	logins *pool.Store

	mu        sync.Mutex
	stamp     string
	loaded    bool
	stale     bool // a background keychain read found a new secret
	cfg       settings
	decisions map[string]*decision
	// states is Cloud's opaque state per session key, least recently used
	// first out, memory only, for as long as one login.
	states     map[string]*list.Element
	stateOrder *list.List
	pauseUntil time.Time
	paused     gateway.RouteAnswer // what an ask answers while paused
	recheckAt  time.Time           // the last /me check of a limit pause
	rechecking bool
	pausePlan  string // the plan /me named while the limit held
	// noPool, noCacheTTL: this login's Cloud refused an ask carrying pool or
	// request.cache_ttl with a 400 and answered it without: an older Cloud,
	// asked without that field from then on.
	noPool, noCacheTTL bool

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
		logins:   pool.NewStore(home),
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
			l.pauseUntil, l.decisions, l.states, l.noPool, l.noCacheTTL = time.Time{}, nil, nil, false, false
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

// bearer is the signed-in session's token while it is fresh (the control API
// takes device-login tokens on /api), else the durable project key the device
// login issued. The CLI owns refreshing the session; the proxy never
// spends the refresh token.
func (s settings) bearer(now time.Time) string {
	if fresh(s.access, now) {
		return s.access
	}
	return s.key
}

// gatewayBearer is the credential for the gateway (POST /v1/route): the
// project key, which is all the gateway authenticates (a login token is
// refused there); the session token only when no key was issued.
func (s settings) gatewayBearer(now time.Time) string {
	if s.key != "" {
		return s.key
	}
	return s.bearer(now)
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
	bearer := cfg.gatewayBearer(l.now())
	if bearer == "" {
		return gateway.RouteAnswer{Outcome: "degraded", Reason: "login_expired"}
	}
	root, ok := jsonsplice.Root(ask.Body)
	if ok && statefulChain(ask.Body, root) {
		// A Responses chain's follow-ups carry no human text to key a decision
		// on, so routing it would switch model halfway through a turn.
		return gateway.RouteAnswer{Outcome: "off", Reason: "stateful_chain"}
	}
	request := requestFor(ask, root)
	entries := l.poolEntries(ask, request.Endpoint, models)
	if ask.PerRequest {
		// A compaction or side request is answered on its own, without the ask.
		l.mu.Lock()
		paused, answer := l.now().Before(l.pauseUntil), l.paused
		l.mu.Unlock()
		if paused {
			l.recheckPause(cfg)
			return answer
		}
		return l.ask(cfg, bearer, ask, request, nil, models, entries, deadline)
	}
	text := askTextFor(ask.Endpoint, ask.Body, root)
	if text.Text == "" {
		return gateway.RouteAnswer{Outcome: "kept", Reason: "no_human_text"}
	}
	// The turn tells a repeated short ask ("yes") apart; it holds within a tool loop.
	key := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%s\x00%s\x00%d\x00%s", ask.SessionID, ask.Provider, ask.Model, text.Turn, text.Text)))
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
			d.answer = l.ask(cfg, bearer, ask, request, &text, models, entries, deadline)
		}()
	}
	<-d.done
	answer := d.answer
	if answer.Model != "" || answer.Effort != "" || answer.Target != nil {
		k := string(key[:])
		rejected := gateway.RouteAnswer{Outcome: "degraded", Reason: "provider_rejected_routed_model"}
		if answer.Target != nil {
			// A pool target that failed: the rest of this ask runs the asked
			// model, still at the answered effort fitted to that model's levels
			// (the same fallback every turn).
			rejected.Effort = translate.FitEffort(request.Endpoint, ask.Model, answer.Effort, ask.Body)
			rejected.EffortMode, rejected.DefaultEffort = answer.EffortMode, answer.DefaultEffort
		}
		answer.Reject = func() {
			// The provider refused the routed model or effort: the rest of this
			// ask keeps what the agent asked for instead of failing over every turn.
			done := make(chan struct{})
			close(done)
			l.mu.Lock()
			if l.decisions != nil {
				l.decisions[k] = &decision{done: done, answer: rejected}
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

// harnessPrefix marks the pool entries that are the harness's own models on
// its own credential: an answer naming one applies like a models answer.
const harnessPrefix = "harness/"

// poolEntries is the pool an ask sends (contracts route-ask-v1 pool): the
// harness's own models first, then every login's models this runtime can
// reach in the caller's grammar, at most 64. Nil when no login adds an entry
// (models says it all) or this login's Cloud is an older one that refuses pool.
func (l *Link) poolEntries(ask gateway.RouteAsk, grammar string, models []string) []pool.Entry {
	l.mu.Lock()
	off := l.noPool
	l.mu.Unlock()
	if off || l.logins == nil {
		return nil
	}
	out := make([]pool.Entry, 0, len(models))
	for _, model := range models {
		out = append(out, pool.Entry{ID: harnessPrefix + model, Model: model, Host: ask.Provider, Via: "local"})
	}
	logins := l.logins.Entries(grammar, ask.Agent, poolMax-len(out), func(host, model string) bool {
		return host == ask.Provider && slices.Contains(models, model) // the harness's own credential serves those
	})
	if len(logins) == 0 {
		return nil
	}
	return append(out, logins...)
}

const poolMax = 64

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
// models, counts computed on this machine, what the request declares, what the
// session's previous request ran, Cloud's state, and the conversation's text
// (left out for a compaction or side request).
type routeAsk struct {
	Models      []string           `json:"models"`
	Pool        []pool.Entry       `json:"pool,omitempty"`
	Signals     signals            `json:"signals"`
	Ask         *askText           `json:"ask,omitempty"`
	Request     routeRequest       `json:"request"`
	Last        *gateway.RouteLast `json:"last,omitempty"`
	State       string             `json:"state,omitempty"`
	ParentState string             `json:"parent_state,omitempty"`
}

// routeRequest is what the request itself declares, as sent.
type routeRequest struct {
	Endpoint      string            `json:"endpoint"`
	Labels        map[string]string `json:"labels,omitempty"`
	ToolNames     []string          `json:"tool_names,omitempty"`
	Effort        string            `json:"effort"`
	Thinking      string            `json:"thinking"`
	PerMessageOff bool              `json:"per_message_off"`
	CacheTTL      string            `json:"cache_ttl,omitempty"`
}

// requestFor reads routeRequest from the body: the declared tool names (at
// most 128; a name over 64 bytes is left out), the top-level
// effort and Anthropic's thinking.type when they are values the contract knows
// (else ""), and on a Claude Code child the agent type of the spawn call it was
// forked from (x-caveman-agent). A label over its bound is left out, never cut.
func requestFor(ask gateway.RouteAsk, root jsonsplice.Span) routeRequest {
	out := routeRequest{Endpoint: "messages", PerMessageOff: ask.PerMessageOff}
	labels := maps.Clone(ask.Labels)
	label := func(name, value string) {
		if _, set := labels[name]; !set && value != "" && len(value) <= gateway.RouteLabelMax(name) {
			if labels == nil {
				labels = map[string]string{}
			}
			labels[name] = value
		}
	}
	body := ask.Body
	var effort string
	switch {
	case strings.HasSuffix(ask.Endpoint, "/responses"):
		out.Endpoint = "responses"
		reasoning, _ := jsonsplice.Field(body, root, "reasoning")
		effort, _ = jsonsplice.StringField(body, reasoning, "effort")
	case strings.HasSuffix(ask.Endpoint, "/chat/completions"):
		out.Endpoint = "chat"
		effort, _ = jsonsplice.StringField(body, root, "reasoning_effort")
	default:
		config, _ := jsonsplice.Field(body, root, "output_config")
		effort, _ = jsonsplice.StringField(body, config, "effort")
		thinking, _ := jsonsplice.Field(body, root, "thinking")
		if kind, _ := jsonsplice.StringField(body, thinking, "type"); slices.Contains([]string{"adaptive", "enabled", "disabled", "between_tools"}, kind) {
			out.Thinking = kind
		}
		if labels["x-claude-code-agent-id"] != "" {
			label("x-caveman-agent", spawnedAgentType(body, root))
		}
	}
	if slices.Contains(contractEfforts, effort) {
		out.Effort = effort
	}
	out.CacheTTL = cacheTTL(out.Endpoint, body, root)
	out.Labels = labels
	list, _ := jsonsplice.Field(body, root, "tools")
	tools, _ := jsonsplice.Elements(body, list)
	for _, tool := range tools {
		if len(out.ToolNames) == 128 {
			break
		}
		name, _ := jsonsplice.StringField(body, tool, "name")
		if name == "" {
			function, _ := jsonsplice.Field(body, tool, "function")
			name, _ = jsonsplice.StringField(body, function, "name")
		}
		if name != "" && len(name) <= 64 { // decoded JSON: valid UTF-8 already
			out.ToolNames = append(out.ToolNames, name)
		}
	}
	return out
}

// The effort values route-ask-v1 knows; anything else goes as "".
var contractEfforts = []string{"none", "minimal", "low", "medium", "high", "xhigh", "max"}

// cacheTTLs are the cache TTLs route-ask-v1 knows, shortest first.
var cacheTTLs = []string{"5m", "30m", "1h", "24h"}

var (
	cacheControlRE = regexp.MustCompile(`"cache_control"\s*:\s*\{[^{}]*\}`)
	ttlRE          = regexp.MustCompile(`"ttl"\s*:\s*"([^"\\]*)"`)
)

// cacheTTL is the TTL the request writes its cache entries at, "" when it
// says none: on Messages the longest cache_control ttl (none is 5m, a value
// the contract does not know is left out); on OpenAI prompt_cache_options.ttl
// (GPT-6's 30m), else prompt_cache_retention (24h; in_memory is 5m).
func cacheTTL(endpoint string, body []byte, root jsonsplice.Span) string {
	if endpoint != "messages" {
		options, _ := jsonsplice.Field(body, root, "prompt_cache_options")
		if ttl, _ := jsonsplice.StringField(body, options, "ttl"); slices.Contains(cacheTTLs, ttl) {
			return ttl
		}
		switch retention, _ := jsonsplice.StringField(body, root, "prompt_cache_retention"); retention {
		case "24h":
			return "24h"
		case "in_memory", "in-memory":
			return "5m"
		}
		return ""
	}
	longest := -1
	for _, marker := range cacheControlRE.FindAll(body, -1) {
		ttl := "5m"
		if match := ttlRE.FindSubmatch(marker); match != nil {
			ttl = string(match[1])
		}
		longest = max(longest, slices.Index(cacheTTLs, ttl))
	}
	if longest < 0 {
		return ""
	}
	return cacheTTLs[longest]
}

// spawnedAgentType is the subagent_type of the spawn call a forked child came
// from: a forked child resends its parent's history up to the newest assistant
// message, then its own prompt. The one tool_use there whose input.prompt the
// child's prompt carries names it; none or several leave it "". Only that
// message's first 32 tool_use inputs and the text after it (at most
// askTextMax bytes) are read.
func spawnedAgentType(body []byte, root jsonsplice.Span) string {
	list, _ := jsonsplice.Field(body, root, "messages")
	items, _ := jsonsplice.Elements(body, list)
	var prompt []string
	for i := len(items) - 1; i >= 0; i-- {
		if role, _ := jsonsplice.StringField(body, items[i], "role"); role != "assistant" {
			if text, ok, _ := messageText(body, items[i], "text"); ok {
				prompt = append(prompt, text)
			}
			continue
		}
		child := strings.Join(prompt, "\n")
		if len(child) > askTextMax {
			return ""
		}
		content, _ := jsonsplice.Field(body, items[i], "content")
		blocks, _ := jsonsplice.Elements(body, content)
		found, uses := "", 0
		for _, block := range blocks {
			if kind, _ := jsonsplice.StringField(body, block, "type"); kind != "tool_use" {
				continue
			}
			if uses++; uses > 32 {
				return ""
			}
			input, _ := jsonsplice.Field(body, block, "input")
			agentType, _ := jsonsplice.StringField(body, input, "subagent_type")
			spawned, _ := jsonsplice.StringField(body, input, "prompt")
			if agentType == "" || strings.TrimSpace(spawned) == "" || !strings.Contains(child, spawned) {
				continue
			}
			if found != "" {
				return "" // two spawns match: no unique one
			}
			found = agentType
		}
		return found
	}
	return ""
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
// decoded. Turns are input groups, the items between two model outputs. A
// group with a tool result belongs to the tool loop, text riding along
// (injected reminders, an interrupt note) included, so a loop keeps its
// decision. Any other group with a user message is one human turn, read from
// its last user message only: an agent's opening context messages come before
// the ask, and a last message without text (an image) leaves the turn empty.
// Each field keeps its end within its bound, cut on a rune boundary: agents
// put their context first and the person's words last.
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
		return askText{Text: tail(text, askTextMax)}
	}
	items, _ := jsonsplice.Elements(body, list)
	var out askText
	humans, replied := 0, false
	text, read, tooled := "", false, false // the current group's last user message: its text, whether read; a tool result
	flush := func() {
		if read && !tooled {
			switch humans {
			case 0:
				out.Text = tail(text, askTextMax)
			case 1:
				out.PrevText = tail(text, askSideMax)
			}
			humans++
		}
		text, read, tooled = "", false, false
	}
	for i := len(items) - 1; i >= 0; i-- {
		role, _ := jsonsplice.StringField(body, items[i], "role")
		kind, _ := jsonsplice.StringField(body, items[i], "type")
		switch {
		case role == "user":
			if read { // an earlier user message of the group: only a tool result matters
				_, _, tool := messageText(body, items[i])
				tooled = tooled || tool
				continue
			}
			got, ok, tool := messageText(body, items[i], "text", "input_text")
			read, tooled = true, tooled || tool
			if ok {
				text = got
			}
		case role == "tool" || role == "function" || strings.HasSuffix(kind, "_output") || kind == "mcp_approval_response":
			tooled = true
		case role == "system" || role == "developer":
		default: // a model output: an assistant message, a tool call, reasoning
			flush()
			if role == "assistant" && humans > 0 && !replied {
				if got, ok, _ := messageText(body, items[i], "text", "output_text"); ok {
					out.ReplyTail, replied = tail(got, askSideMax), true
				}
			}
		}
	}
	flush()
	out.Turn = min(max(humans-1, 0), askTurnMax)
	return out
}

// messageText joins a message's text blocks with "\n"; a string content is one
// block. ok is false when no block has any non-space text; tool reports a
// tool-result block.
func messageText(body []byte, message jsonsplice.Span, types ...string) (text string, ok, tool bool) {
	content, _ := jsonsplice.Field(body, message, "content")
	if text, isString := jsonsplice.String(body, content); isString {
		return text, strings.TrimSpace(text) != "", false
	}
	blocks, _ := jsonsplice.Elements(body, content)
	var texts []string
	for _, block := range blocks {
		kind, _ := jsonsplice.StringField(body, block, "type")
		tool = tool || strings.HasSuffix(kind, "tool_result")
		if !slices.Contains(types, kind) {
			continue
		}
		if text, isString := jsonsplice.StringField(body, block, "text"); isString {
			texts = append(texts, text)
			ok = ok || strings.TrimSpace(text) != ""
		}
	}
	return strings.Join(texts, "\n"), ok, tool
}

// askBody is the ask's JSON within askBodyMax. Escaping can grow text past it
// (a control byte encodes as six), so then text keeps its longest end that
// fits. Nil when the text left is blank (Cloud refuses a blank text) or the
// rest alone does not fit, which only escaping-heavy labels can do.
func askBody(ask routeAsk) []byte {
	raw := encodeAsk(ask)
	if len(raw) > askBodyMax && ask.Ask != nil {
		text, trimmed := ask.Ask.Text, *ask.Ask
		ask.Ask = &trimmed
		n := sort.Search(len(text)+1, func(n int) bool {
			trimmed.Text = tail(text, n)
			return len(encodeAsk(ask)) > askBodyMax
		}) - 1
		trimmed.Text = tail(text, max(n, 0))
		raw = encodeAsk(ask)
	}
	if ask.Ask != nil && strings.TrimSpace(ask.Ask.Text) == "" || len(raw) > askBodyMax {
		return nil
	}
	return raw
}

// encodeAsk is JSON without HTML escaping: an agent's <tags> and && stay one
// byte each instead of six.
func encodeAsk(ask routeAsk) []byte {
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	_ = encoder.Encode(ask)
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n"))
}

type signals struct {
	Agent         string `json:"agent"`
	ContextTokens int    `json:"context_tokens"`
	ToolsDeclared int    `json:"tools_declared"`
	ToolErrors    int    `json:"tool_errors"`
	Images        bool   `json:"images"`
}

// signalsFor counts what the ask carries. Tool errors are counted over the
// whole request; context tokens are the gateway's count in the session's (a
// child's parent's) provider tokens, else estimated from its size.
func signalsFor(ask gateway.RouteAsk) signals {
	agent := ask.Agent
	if !slugRE.MatchString(agent) {
		agent = "unlabeled-agent"
	}
	tokens := ask.ContextTokens
	if tokens <= 0 {
		tokens = ask.InputBytes / 4
	}
	return signals{
		Agent:         agent,
		ContextTokens: min(tokens, 1_000_000_000),
		ToolsDeclared: min(ask.ToolsCount, 1_000_000_000),
		ToolErrors:    len(toolErrorRE.FindAllIndex(ask.Body, -1)),
		Images:        imageRE.Match(ask.Body),
	}
}

func (l *Link) ask(cfg settings, bearer string, ask gateway.RouteAsk, declared routeRequest, text *askText, models []string, entries []pool.Entry, deadline time.Time) gateway.RouteAnswer {
	var last *gateway.RouteLast
	if ask.Last != nil {
		bounded := *ask.Last
		bounded.Model = truncate(bounded.Model, 128)
		if !slices.Contains(contractEfforts, bounded.Effort) {
			bounded.Effort = ""
		}
		for _, count := range []*int{&bounded.AgeS, &bounded.InputTokens, &bounded.CacheReadTokens, &bounded.CacheWriteTokens} {
			*count = min(max(*count, 0), 1_000_000_000)
		}
		last = &bounded
	}
	l.mu.Lock()
	if l.noCacheTTL {
		declared.CacheTTL = ""
	}
	l.mu.Unlock()
	body := routeAsk{
		Models: models, Pool: entries, Signals: signalsFor(ask), Ask: text, Request: declared, Last: last,
		State: l.state(ask.SessionID), ParentState: l.state(ask.ParentSessionID),
	}
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()
	post := func(body routeAsk) (*http.Response, gateway.RouteAnswer, bool) {
		raw := askBody(body)
		if raw == nil {
			return nil, gateway.RouteAnswer{Outcome: "kept", Reason: "no_human_text"}, false
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.gateway+"/v1/route", bytes.NewReader(raw))
		if err != nil {
			return nil, l.pause(failurePause, gateway.RouteAnswer{Outcome: "degraded", Reason: "bad_cloud_url"}), false
		}
		request.Header.Set("content-type", "application/json")
		request.Header.Set("authorization", "Bearer "+bearer)
		response, err := l.client.Do(request)
		if err != nil {
			reason := "cloud_unreachable"
			if errors.Is(err, context.DeadlineExceeded) {
				reason = "timeout"
			}
			return nil, l.pause(failurePause, gateway.RouteAnswer{Outcome: "degraded", Reason: reason}), false
		}
		return response, gateway.RouteAnswer{}, true
	}
	response, failed, ok := post(body)
	if !ok {
		return failed
	}
	raw, _ := io.ReadAll(io.LimitReader(response.Body, answerMax))
	response.Body.Close()
	// An older Cloud refuses an additive field it does not know with a 400:
	// by name, or with its generic refusal of any body it cannot decode. The
	// ask goes again without the field the 400 names, else the newest one sent
	// (cache_ttl came after pool), at most once per field; a field whose
	// removal Cloud then accepts stays out for the rest of this login. Any
	// other 400 is answered as before, never retried.
	var dropped []string
	for response.StatusCode == http.StatusBadRequest {
		field := refusedField(strings.ToLower(string(raw)), body)
		if field == "" {
			break
		}
		dropped = append(dropped, field)
		switch field {
		case "cache_ttl":
			body.Request.CacheTTL = ""
		case "pool":
			body.Pool, entries = nil, nil
		}
		if response, failed, ok = post(body); !ok {
			return failed
		}
		raw, _ = io.ReadAll(io.LimitReader(response.Body, answerMax))
		response.Body.Close()
	}
	if len(dropped) > 0 && response.StatusCode == http.StatusOK {
		l.mu.Lock()
		l.noPool = l.noPool || slices.Contains(dropped, "pool")
		l.noCacheTTL = l.noCacheTTL || slices.Contains(dropped, "cache_ttl")
		l.mu.Unlock()
		if l.logger != nil {
			l.logger.Warn("Cloud refused newer route-ask fields; asking without them until the next login", "fields", strings.Join(dropped, ","))
		}
	}
	var answer struct {
		PoolID     string `json:"pool_id"`
		Via        string `json:"via"`
		Model      string `json:"model"`
		Effort     string `json:"effort"`
		EffortMode string `json:"effort_mode"`
		// DefaultEffort is the asked model's catalog default effort, "" unknown.
		DefaultEffort string `json:"default_effort"`
		State         string `json:"state"`
		Reason        string `json:"reason"`
		DecisionID    string `json:"decision_id"`
		Notice        string `json:"notice"`
		Error         struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	unreadable := json.Unmarshal(raw, &answer) != nil
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
	}
	target, model, reason := l.poolAnswer(cfg, declared.Endpoint, models, entries, answer.PoolID, answer.Via, answer.Model)
	switch reason {
	case "":
	case "cloud_off":
		// `caveman providers cloud off`: the asked model, on the harness's credential.
		return gateway.RouteAnswer{Outcome: "kept", Reason: reason}
	default:
		return gateway.RouteAnswer{Outcome: "degraded", Reason: reason}
	}
	l.forget()
	l.keepState(ask.SessionID, answer.State)
	out := gateway.RouteAnswer{Outcome: "kept", Reason: truncate(answer.Reason, 64), DecisionID: answer.DecisionID}
	// An effort the runtime cannot splice in safely is left out, never guessed at.
	if slices.Contains(contractEfforts, answer.Effort) && slices.Contains([]string{"", "message", "top"}, answer.EffortMode) {
		out.Effort, out.EffortMode = answer.Effort, answer.EffortMode
	}
	if slices.Contains(contractEfforts, answer.DefaultEffort) {
		out.DefaultEffort = answer.DefaultEffort
	}
	switch {
	case target != nil:
		out.Target, out.Outcome = target, "routed"
	case model != ask.Model:
		out.Model, out.Outcome = model, "routed"
	}
	return out
}

// refusedField is the additive field a 400 (lower-cased) is about: one it
// names that the ask still carries, else, for Cloud's generic refusal, the
// newest one it carries; "" when the 400 is about none.
func refusedField(refusal string, body routeAsk) string {
	carried := []string{}
	if body.Request.CacheTTL != "" {
		carried = append(carried, "cache_ttl")
	}
	if len(body.Pool) > 0 {
		carried = append(carried, "pool")
	}
	for _, field := range carried {
		if strings.Contains(refusal, field) {
			return field
		}
	}
	if len(carried) > 0 && strings.Contains(refusal, "cave_router_request_invalid") {
		return carried[0]
	}
	return ""
}

// cloudRouteRE is a via "cloud" pool id, sent back as x-caveman-route:
// cloud:<provider>:<model>, the provider lower case, at most 256 bytes.
var cloudRouteRE = regexp.MustCompile(`^cloud:[a-z0-9_]+:[!-~]+$`)

// poolAnswer reads where an answer sends the request. Without pool_id it is
// one of models (route-ask-v1 as before). A pool_id names one of the entries
// sent (via "local", the model it carries), a harness entry applying like a
// models answer and any other a login's Target; or, via "cloud", an entry
// Cloud added, sent to the Cloud gateway in the caller's grammar. Anything
// else, or a login whose secret cannot be read, is outside the pool: the
// request keeps the asked model (no pause, the next ask may differ).
func (l *Link) poolAnswer(cfg settings, grammar string, models []string, entries []pool.Entry, id, via, model string) (*gateway.RouteTarget, string, string) {
	if id == "" {
		if !slices.Contains(models, model) {
			return nil, "", "answer_outside_pool"
		}
		return nil, model, ""
	}
	switch via {
	case "cloud":
		switch {
		case len(id) > 256 || !cloudRouteRE.MatchString(id) || model == "" || len(model) > 128 || cfg.gateway == "":
			return nil, "", "answer_outside_pool"
		case l.logins != nil && l.logins.CloudOff():
			return nil, "", "cloud_off"
		case cfg.key == "":
			// The gateway takes only the project key, never the login token.
			return nil, "", "cloud_unavailable"
		}
		header := http.Header{}
		header.Set("authorization", "Bearer "+cfg.key)
		header.Set("x-caveman-route", id)
		return &gateway.RouteTarget{PoolID: id, Via: "cloud", Host: "cloud", Model: model, Wire: grammar,
			URL: cfg.gateway + cloudPaths[grammar], Header: header}, "", ""
	case "", "local":
		for _, entry := range entries {
			if entry.ID != id {
				continue
			}
			if model != "" && model != entry.Model {
				return nil, "", "answer_outside_pool"
			}
			if strings.HasPrefix(id, harnessPrefix) {
				return nil, entry.Model, ""
			}
			target, err := l.logins.Target(entry)
			if err != nil {
				return nil, "", "login_unusable"
			}
			return &target, "", ""
		}
	}
	return nil, "", "answer_outside_pool"
}

// cloudPaths are the Cloud gateway's routes for each caller grammar.
var cloudPaths = map[string]string{"messages": "/v1/messages", "chat": "/v1/chat/completions", "responses": "/v1/responses"}

// state is Cloud's opaque state for a session key, "" when none.
func (l *Link) state(key string) string {
	l.mu.Lock()
	defer l.mu.Unlock()
	if element, ok := l.states[key]; ok {
		l.stateOrder.MoveToFront(element)
		return element.Value.([2]string)[1]
	}
	return ""
}

// keepState stores the state Cloud answered for a session key. An empty one
// keeps the one there (a compaction or side answer need not carry it); one over
// stateMax bytes drops it.
func (l *Link) keepState(key, state string) {
	if key == "" || state == "" {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if element, ok := l.states[key]; ok {
		l.stateOrder.Remove(element)
		delete(l.states, key)
	}
	if len(state) > stateMax {
		return
	}
	if l.states == nil {
		l.states, l.stateOrder = map[string]*list.Element{}, list.New()
	}
	if l.stateOrder.Len() >= statesMax {
		oldest := l.stateOrder.Back()
		l.stateOrder.Remove(oldest)
		delete(l.states, oldest.Value.([2]string)[0])
	}
	l.states[key] = l.stateOrder.PushFront([2]string{key, state})
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
	start := len(text) - max(n, 0)
	for start < len(text) && !utf8.RuneStart(text[start]) {
		start++
	}
	return text[start:]
}
