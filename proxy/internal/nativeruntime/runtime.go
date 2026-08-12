// Package nativeruntime owns deterministic local-agent session state. Host
// packs translate native callbacks into this protocol; policy and persistence
// stay here so integrations cannot drift into separate implementations.
package nativeruntime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/JuliusBrussee/caveman/engine"
	"github.com/JuliusBrussee/caveman/engine/ccr"
	"github.com/JuliusBrussee/caveman/proxy/internal/nativepack"
	"github.com/JuliusBrussee/caveman/proxy/internal/repointel"
	"github.com/JuliusBrussee/caveman/proxy/internal/sessionusage"
	"github.com/JuliusBrussee/caveman/shared/platform/redact"
)

const ProtocolVersion = 1

type Agent struct {
	ID      string `json:"id"`
	Version string `json:"version,omitempty"`
	Surface string `json:"surface,omitempty"`
}

type Session struct {
	ID              string `json:"caveman_session_id"`
	HostSessionID   string `json:"host_session_id,omitempty"`
	ParentSessionID string `json:"parent_session_id,omitempty"`
	CWD             string `json:"cwd,omitempty"`
	RepositoryState string `json:"repository_state,omitempty"`
}

type Event struct {
	Type        string `json:"type"`
	TimestampMS int64  `json:"timestamp_ms,omitempty"`
}

type Model struct {
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`
}

type PayloadDigest struct {
	Bytes  int    `json:"bytes"`
	SHA256 string `json:"sha256"`
}

// TaskProfile is a bounded, deterministic classification produced by the thin
// host adapter. Raw prompt text never crosses the native runtime protocol.
type TaskProfile struct {
	Type         string   `json:"type"`
	Terms        []string `json:"terms,omitempty"`
	Continuation bool     `json:"continuation,omitempty"`
}

type Tool struct {
	Name       string          `json:"name"`
	Input      json.RawMessage `json:"input,omitempty"`
	InputState string          `json:"input_state,omitempty"`
	Output     []byte          `json:"output,omitempty"`
}

type Request struct {
	ProtocolVersion int            `json:"protocol_version"`
	PolicyMode      string         `json:"policy_mode,omitempty"`
	Profile         string         `json:"profile,omitempty"`
	Agent           Agent          `json:"agent"`
	Session         Session        `json:"session"`
	Event           Event          `json:"event"`
	Model           *Model         `json:"model,omitempty"`
	Prompt          *PayloadDigest `json:"prompt,omitempty"`
	TaskProfile     *TaskProfile   `json:"task_profile,omitempty"`
	Tool            *Tool          `json:"tool,omitempty"`
}

type Response struct {
	ProtocolVersion   int    `json:"protocol_version"`
	PolicyMode        string `json:"policy_mode"`
	Profile           string `json:"profile"`
	Action            string `json:"action"`
	Context           string `json:"context,omitempty"`
	Message           string `json:"message,omitempty"`
	OutputReplacement string `json:"output_replacement,omitempty"`
	RecoveryRef       string `json:"recovery_ref,omitempty"`
	DecisionID        string `json:"decision_id,omitempty"`
	Visibility        string `json:"visibility"`
	FailOpen          bool   `json:"fail_open"`
	RuntimeUS         int64  `json:"runtime_us"`
	decisionReason    string
}

type Runtime struct {
	store                 *ccr.Store
	receiptDir            string
	usage                 sessionusage.Reader
	mu                    sync.Mutex
	repositoryMu          sync.RWMutex
	repositoryMaps        map[string]*repositoryMapEntry
	repositorySessionRefs map[string]string
	repositoryWarming     map[string]bool
	repositoryEnded       map[string]bool
	activeSessions        map[string]sessionActivity
	lastActivity          time.Time
	// detect classifies a tool output so afterTool can tell what the elision
	// engine could do with it. Deterministic, local, no store and no network. A
	// nil detect means "cannot classify", which masks — see maskWouldDiscardFacts.
	detect func([]byte) string
}

type sessionActivity struct {
	At       time.Time
	CWD      string
	Provider string
	Model    string
}

type repositoryMapEntry struct {
	done    chan struct{}
	repoMap repointel.Map
	err     error
}

func newRuntime(store *ccr.Store) *Runtime {
	return &Runtime{
		store: store, repositoryMaps: map[string]*repositoryMapEntry{},
		repositorySessionRefs: map[string]string{}, repositoryWarming: map[string]bool{}, repositoryEnded: map[string]bool{},
		activeSessions: map[string]sessionActivity{}, lastActivity: time.Now(),
		// A store-less engine: Detect reads only the bytes handed to it.
		detect: engine.New(nil, nil).Detect,
	}
}

func New(store *ccr.Store) *Runtime { return newRuntime(store) }

func NewWithReceipts(store *ccr.Store, receiptDir string) *Runtime {
	runtime := newRuntime(store)
	runtime.receiptDir = receiptDir
	return runtime
}

func NewWithReceiptsAndUsage(store *ccr.Store, receiptDir string, usage sessionusage.Reader) *Runtime {
	runtime := newRuntime(store)
	runtime.receiptDir = receiptDir
	runtime.usage = usage
	return runtime
}

var eventTypes = map[string]struct{}{
	"session.start": {}, "prompt.submit": {}, "model.before": {}, "model.after": {},
	"tool.before": {}, "tool.after": {}, "tool.failure": {},
	"context.compact.before": {}, "context.compact.after": {}, "turn.stop": {},
	"session.end": {}, "subagent.start": {}, "subagent.stop": {},
}

type profileFeatures struct {
	taskContract bool
	compactState bool
	reuse        bool
	capture      bool
	mask         bool
	repository   bool
}

// resolveProfile keeps benchmark ablations mechanism-real. Production safe/max
// defaults select full product behavior. Record always overrides every active
// transform, regardless of requested profile, preserving byte-pass-through.
func resolveProfile(raw, policyMode string) (string, profileFeatures, error) {
	if policyMode == "record" {
		return "record-only", profileFeatures{}, nil
	}
	profile := strings.ToLower(strings.TrimSpace(raw))
	if profile == "" {
		if policyMode == "max" {
			profile = "full-max"
		} else {
			profile = "full-safe"
		}
	}
	switch profile {
	case "core", "core-lean-build":
		return profile, profileFeatures{}, nil
	case "ledger":
		return profile, profileFeatures{taskContract: true, compactState: true, reuse: true, capture: true}, nil
	case "ccr-masking":
		return profile, profileFeatures{capture: true, mask: true}, nil
	case "cache-aware":
		return profile, profileFeatures{taskContract: true, compactState: true}, nil
	case "full-safe", "full-max":
		return profile, profileFeatures{taskContract: true, compactState: true, reuse: true, capture: true, mask: true, repository: true}, nil
	default:
		return "", profileFeatures{}, fmt.Errorf("native runtime: unknown profile %q", raw)
	}
}

func (r *Runtime) Handle(_ context.Context, request Request) (Response, error) {
	started := time.Now()
	r.mu.Lock()
	defer r.mu.Unlock()

	if request.ProtocolVersion != ProtocolVersion {
		return Response{}, fmt.Errorf("native runtime: unsupported protocol version %d", request.ProtocolVersion)
	}
	if _, ok := eventTypes[request.Event.Type]; !ok {
		return Response{}, fmt.Errorf("native runtime: unknown event %q", request.Event.Type)
	}
	if strings.TrimSpace(request.Session.ID) == "" {
		return Response{}, errors.New("native runtime: session id is required")
	}
	policyMode := request.PolicyMode
	if policyMode == "" {
		policyMode = "safe"
	}
	if policyMode != "record" && policyMode != "safe" && policyMode != "max" {
		return Response{}, fmt.Errorf("native runtime: unknown policy mode %q", policyMode)
	}
	request.PolicyMode = policyMode
	profile, features, err := resolveProfile(request.Profile, policyMode)
	if err != nil {
		return Response{}, err
	}
	request.Profile = profile
	r.lastActivity = time.Now()
	if request.Event.Type == "session.end" {
		delete(r.activeSessions, request.Session.ID)
	} else {
		activity := r.activeSessions[request.Session.ID]
		activity.At = r.lastActivity
		if cwd := strings.TrimSpace(request.Session.CWD); cwd != "" {
			activity.CWD = filepath.Clean(cwd)
		}
		if request.Model != nil {
			if provider := strings.TrimSpace(request.Model.Provider); provider != "" {
				activity.Provider = strings.ToLower(provider)
			}
			if model := strings.TrimSpace(request.Model.Model); model != "" {
				activity.Model = strings.ToLower(model)
			}
		}
		r.activeSessions[request.Session.ID] = activity
	}
	response := Response{
		ProtocolVersion: ProtocolVersion,
		PolicyMode:      policyMode,
		Profile:         profile,
		Action:          "allow",
		Visibility:      "silent",
		FailOpen:        true,
		DecisionID:      decisionID(request, request.Event.Type),
	}
	err = nil
	switch request.Event.Type {
	case "session.start":
		if features.repository {
			r.startRepositoryEvidence(request)
		}
	case "prompt.submit":
		if features.taskContract {
			response.Context, err = r.ensureTaskContract(request)
		}
		if err == nil && features.repository {
			var evidenceContext, evidenceRef string
			evidenceContext, evidenceRef, err = r.repositoryEvidenceContext(request)
			response.Context = joinContext(response.Context, evidenceContext)
			response.RecoveryRef = evidenceRef
		}
		if err == nil && features.mask && !directOutputRewriteAgent(request.Agent.ID) {
			var deferred string
			deferred, err = r.deferredMaskContext(request.Session.ID)
			response.Context = joinContext(response.Context, deferred)
		}
	case "context.compact.after":
		if features.compactState {
			response.Context, err = r.compactStateContext(request.Session.ID)
		}
	case "context.compact.before":
		err = r.transitionAfterCompaction(request.Session.ID)
	case "tool.before":
		if features.reuse {
			response, err = r.beforeTool(request, response)
		}
	case "tool.after", "tool.failure":
		if features.capture {
			response, err = r.afterTool(request, response, features.mask)
		}
	case "subagent.start":
		err = r.recordSubagentState(request, "active")
	case "subagent.stop":
		err = r.mergeSubagentEvidence(request)
	}
	if err != nil {
		return Response{}, err
	}
	response.RuntimeUS = time.Since(started).Microseconds()
	if err := r.recordDecision(request, response); err != nil {
		return Response{}, err
	}
	if request.Event.Type == "session.end" && r.receiptDir != "" {
		receipt, writeErr := r.writeReceipt(request)
		if writeErr != nil {
			return Response{}, writeErr
		}
		response.Message = compactReceiptLine(receipt)
	}
	if request.Event.Type == "session.end" {
		r.markRepositorySessionEnded(request.Session.ID)
		if err := r.archiveSession(request.Session.ID); err != nil {
			return Response{}, err
		}
	}
	return response, nil
}

func directOutputRewriteAgent(agentID string) bool {
	switch strings.ToLower(strings.TrimSpace(agentID)) {
	case "claude", "opencode":
		return true
	default:
		return false
	}
}

func (r *Runtime) deferredMaskContext(sessionID string) (string, error) {
	objects, err := r.store.ListSessionObjects(sessionID, 1000)
	if err != nil {
		return "", err
	}
	lines := make([]string, 0, 3)
	ids := make([]string, 0, 3)
	for _, object := range objects {
		if len(lines) == 3 {
			break
		}
		if object.Lifecycle != ccr.Warm || object.Currentness != ccr.Current || object.OriginalByteLength <= 0 {
			continue
		}
		switch object.Type {
		case ccr.ObjectFileObservation, ccr.ObjectSearchResult, ccr.ObjectCommandResult,
			ccr.ObjectTestResult, ccr.ObjectBuildResult, ccr.ObjectDiffSnapshot,
			ccr.ObjectDocumentationExcerpt, ccr.ObjectBrowserSnapshot:
			lines = append(lines, fmt.Sprintf("[%s] source: %s; full: ccr://%s", object.Type, object.Source, object.ID))
			ids = append(ids, object.ID)
		}
	}
	for _, id := range ids {
		if err := r.store.SetObjectLifecycle(id, ccr.Cold); err != nil {
			return "", err
		}
	}
	if len(lines) == 0 {
		return "", nil
	}
	return "Caveman deferred exact-output masks (dynamic tail; host lacks direct rewrite):\n" + strings.Join(lines, "\n"), nil
}

func (r *Runtime) recordSubagentState(request Request, status string) error {
	if strings.TrimSpace(request.Session.ParentSessionID) == "" {
		return nil
	}
	data, err := json.Marshal(map[string]any{
		"schema":            "caveman.native.subagent-state.v1",
		"status":            status,
		"child_session_id":  request.Session.ID,
		"parent_session_id": request.Session.ParentSessionID,
	})
	if err != nil {
		return err
	}
	_, err = r.store.PutObject(ccr.Object{
		Type: ccr.ObjectExecutionState, Source: "native:subagent-" + status,
		SessionID: request.Session.ID, RepositoryState: request.Session.RepositoryState,
		TransformVersion: "native-subagent-v1", Currentness: ccr.Current, Lifecycle: ccr.Warm, Data: data,
	})
	return err
}

func (r *Runtime) mergeSubagentEvidence(request Request) error {
	parent := strings.TrimSpace(request.Session.ParentSessionID)
	if parent == "" {
		return nil
	}
	objects, err := r.store.ListSessionObjects(request.Session.ID, 1000)
	if err != nil {
		return err
	}
	type evidenceRef struct {
		Type        ccr.ObjectType  `json:"type"`
		Source      string          `json:"source"`
		Currentness ccr.Currentness `json:"currentness"`
		Reference   string          `json:"reference"`
	}
	references := make([]evidenceRef, 0, 64)
	for _, object := range objects {
		if len(references) == cap(references) || object.Currentness != ccr.Current {
			continue
		}
		switch object.Type {
		case ccr.ObjectFileObservation, ccr.ObjectSearchResult, ccr.ObjectCommandResult,
			ccr.ObjectTestResult, ccr.ObjectBuildResult, ccr.ObjectDiffSnapshot,
			ccr.ObjectDocumentationExcerpt, ccr.ObjectBrowserSnapshot, ccr.ObjectEvidenceBundle:
			references = append(references, evidenceRef{
				Type: object.Type, Source: object.Source, Currentness: object.Currentness, Reference: "ccr://" + object.ID,
			})
		}
	}
	data, err := json.Marshal(map[string]any{
		"schema":            "caveman.native.subagent-merge.v1",
		"status":            "completed",
		"child_session_id":  request.Session.ID,
		"parent_session_id": parent,
		"evidence":          references,
	})
	if err != nil {
		return err
	}
	_, err = r.store.PutObject(ccr.Object{
		Type: ccr.ObjectExecutionState, Source: "native:subagent-merge",
		SessionID: parent, RepositoryState: request.Session.RepositoryState,
		TransformVersion: "native-subagent-v1", Currentness: ccr.Current, Lifecycle: ccr.Warm, Data: data,
	})
	if err != nil {
		return err
	}
	return r.recordSubagentState(request, "completed")
}

// IdleSnapshot exposes lifecycle only; no session identifiers leave runtime.
func (r *Runtime) IdleSnapshot() (active int, lastActivity time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.activeSessions), r.lastActivity
}

// UniqueRecentSession is removed-marker fallback authority. It returns a
// candidate only when exactly one recent native session remains plausible.
// Known provider/model mismatches eliminate candidates; missing dimensions do
// not. Concurrent, ambiguous, future, or stale sessions return empty.
func (r *Runtime) UniqueRecentSession(now time.Time, window time.Duration, provider, model string) (string, string) {
	if window <= 0 {
		return "", ""
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	cutoff := now.Add(-window)
	provider = strings.ToLower(strings.TrimSpace(provider))
	model = strings.ToLower(strings.TrimSpace(model))
	candidate := ""
	matchedModel := false
	for sessionID, activity := range r.activeSessions {
		if activity.At.Before(cutoff) || activity.At.After(now.Add(time.Second)) {
			continue
		}
		if provider != "" && activity.Provider != "" && provider != activity.Provider {
			continue
		}
		if model != "" && activity.Model != "" && model != activity.Model {
			continue
		}
		if candidate != "" && candidate != sessionID {
			return "", ""
		}
		candidate = sessionID
		matchedModel = model != "" && activity.Model == model && (provider == "" || activity.Provider == "" || activity.Provider == provider)
	}
	if candidate == "" {
		return "", ""
	}
	if matchedModel {
		return candidate, "unique_recent_time_model"
	}
	return candidate, "unique_recent_session"
}

// WaitForIdle returns true only after every observed session ended and timeout
// elapsed. Explicit long-running gateway owners do not call it.
func (r *Runtime) WaitForIdle(ctx context.Context, idleTimeout time.Duration) bool {
	if idleTimeout <= 0 {
		return false
	}
	poll := idleTimeout / 4
	if poll < 10*time.Millisecond {
		poll = 10 * time.Millisecond
	}
	if poll > time.Second {
		poll = time.Second
	}
	ticker := time.NewTicker(poll)
	defer ticker.Stop()
	for {
		now := time.Now()
		r.mu.Lock()
		for sessionID, activity := range r.activeSessions {
			if now.Sub(activity.At) >= idleTimeout {
				delete(r.activeSessions, sessionID)
			}
		}
		active, last := len(r.activeSessions), r.lastActivity
		r.mu.Unlock()
		if active == 0 && time.Since(last) >= idleTimeout {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-ticker.C:
		}
	}
}

// startRepositoryEvidence warms deterministic repository metadata outside hook
// latency. No model/network call occurs; prompt hooks consume only completed maps.
func (r *Runtime) startRepositoryEvidence(request Request) {
	if strings.TrimSpace(request.Session.CWD) == "" {
		return
	}
	key := repositoryKey(request.Session)
	r.repositoryMu.Lock()
	entry, exists := r.repositoryMaps[key]
	if !exists {
		entry = &repositoryMapEntry{done: make(chan struct{})}
		r.repositoryMaps[key] = entry
	}
	if r.repositorySessionRefs[request.Session.ID] != "" || r.repositoryWarming[request.Session.ID] {
		r.repositoryMu.Unlock()
		return
	}
	r.repositoryWarming[request.Session.ID] = true
	r.repositoryMu.Unlock()

	if !exists {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			repoMap, _, err := repointel.Build(ctx, request.Session.CWD, request.Session.RepositoryState, nil)
			r.repositoryMu.Lock()
			entry.repoMap = repoMap
			entry.err = err
			close(entry.done)
			r.repositoryMu.Unlock()
		}()
	}

	go func() {
		<-entry.done
		r.repositoryMu.RLock()
		repoMap, mapErr := entry.repoMap, entry.err
		r.repositoryMu.RUnlock()
		if mapErr != nil {
			r.repositoryMu.Lock()
			delete(r.repositoryWarming, request.Session.ID)
			r.repositoryMu.Unlock()
			return
		}
		data, err := json.Marshal(repoMap)
		if err == nil {
			var id string
			r.repositoryMu.Lock()
			if r.repositoryEnded[request.Session.ID] {
				delete(r.repositoryWarming, request.Session.ID)
				r.repositoryMu.Unlock()
				return
			}
			id, err = r.store.PutObject(ccr.Object{
				Type: ccr.ObjectRepositoryMap, Source: "native:repository-map", SessionID: request.Session.ID,
				RepositoryState: request.Session.RepositoryState, TransformVersion: "repository-map-v1",
				Currentness: ccr.Current, Lifecycle: ccr.Warm, Data: data,
			})
			if err == nil {
				r.repositorySessionRefs[request.Session.ID] = id
			}
			r.repositoryMu.Unlock()
		}
		r.repositoryMu.Lock()
		delete(r.repositoryWarming, request.Session.ID)
		r.repositoryMu.Unlock()
	}()
}

func (r *Runtime) markRepositorySessionEnded(sessionID string) {
	r.repositoryMu.Lock()
	r.repositoryEnded[sessionID] = true
	delete(r.repositoryWarming, sessionID)
	r.repositoryMu.Unlock()
}

func (r *Runtime) transitionAfterCompaction(sessionID string) error {
	objects, err := r.store.ListSessionObjects(sessionID, 10000)
	if err != nil {
		return err
	}
	for _, object := range objects {
		target := ccr.Cold
		switch object.Type {
		case ccr.ObjectTaskContract:
			target = ccr.Hot
		case ccr.ObjectTaskDecision, ccr.ObjectRepositoryMap, ccr.ObjectEvidenceBundle, ccr.ObjectExecutionState:
			target = ccr.Warm
		}
		if object.Lifecycle != target {
			if err := r.store.SetObjectLifecycle(object.ID, target); err != nil {
				return err
			}
		}
	}
	return nil
}

func (r *Runtime) archiveSession(sessionID string) error {
	objects, err := r.store.ListSessionObjects(sessionID, 10000)
	if err != nil {
		return err
	}
	for _, object := range objects {
		if object.Lifecycle != ccr.LifecycleArchived {
			if err := r.store.SetObjectLifecycle(object.ID, ccr.LifecycleArchived); err != nil {
				return err
			}
		}
	}
	return nil
}

func (r *Runtime) repositoryEvidenceContext(request Request) (string, string, error) {
	if strings.TrimSpace(request.Session.CWD) == "" {
		return "Caveman repository evidence: unavailable (cwd not provided); no path claims injected.", "", nil
	}
	r.startRepositoryEvidence(request)
	key := repositoryKey(request.Session)
	r.repositoryMu.RLock()
	entry := r.repositoryMaps[key]
	mapRef := r.repositorySessionRefs[request.Session.ID]
	r.repositoryMu.RUnlock()
	if entry == nil {
		return "Caveman repository evidence: unavailable; no path claims injected.", "", nil
	}
	select {
	case <-entry.done:
		r.repositoryMu.RLock()
		repoMap, mapErr := entry.repoMap, entry.err
		r.repositoryMu.RUnlock()
		if mapErr != nil {
			return "Caveman repository evidence: local map failed closed; no path claims injected.", "", nil
		}
		if mapRef == "" {
			return "Caveman repository evidence: map persistence warming asynchronously; no path claims injected.", "", nil
		}
		terms := []string(nil)
		if request.TaskProfile != nil {
			terms = request.TaskProfile.Terms
		}
		bundle := repointel.Evidence(repoMap, terms)
		data, err := json.Marshal(bundle)
		if err != nil {
			return "", "", err
		}
		bundleID, err := r.store.PutObject(ccr.Object{
			Type: ccr.ObjectEvidenceBundle, Source: "native:repository-evidence", SessionID: request.Session.ID,
			RepositoryState: request.Session.RepositoryState, TransformVersion: "repository-evidence-v1",
			Currentness: ccr.Current, Lifecycle: ccr.Hot, Dependencies: []string{mapRef}, Data: data,
		})
		if err != nil {
			return "", "", err
		}
		ref := "ccr://" + bundleID
		return renderRepositoryEvidence(bundle, ref), ref, nil
	default:
		return "Caveman repository evidence: warming asynchronously; no path claims injected.", "", nil
	}
}

func renderRepositoryEvidence(bundle repointel.Bundle, ref string) string {
	if len(bundle.Items) == 0 {
		return "Likely implementation path (observed local metadata): no task-specific match; inspect directly before editing. Evidence: " + ref
	}
	var out strings.Builder
	out.WriteString("Likely implementation path (observed local metadata):")
	for index, item := range bundle.Items[:min(5, len(bundle.Items))] {
		fmt.Fprintf(&out, "\n%d. %s", index+1, item.Path)
		if item.LineStart > 0 {
			fmt.Fprintf(&out, ":%d", item.LineStart)
			if item.LineEnd > item.LineStart {
				fmt.Fprintf(&out, "-%d", item.LineEnd)
			}
		}
		if len(item.Reasons) > 0 {
			fmt.Fprintf(&out, " — %s", item.Reasons[0])
		}
	}
	fmt.Fprintf(&out, "\nEvidence: %s. Scout: %s.", ref, bundle.Scout.Status)
	return out.String()
}

func repositoryKey(session Session) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(session.CWD) + "\x00" + session.RepositoryState))
	return hex.EncodeToString(sum[:])
}

func joinContext(parts ...string) string {
	var nonempty []string
	for _, part := range parts {
		if strings.TrimSpace(part) != "" {
			nonempty = append(nonempty, strings.TrimSpace(part))
		}
	}
	return strings.Join(nonempty, "\n")
}

func (r *Runtime) recordDecision(request Request, response Response) error {
	stamp := request.Event.TimestampMS
	if stamp == 0 {
		stamp = time.Now().UnixMilli()
	}
	reason := firstNonEmpty(response.decisionReason, "event_recorded")
	switch {
	case response.decisionReason != "":
	case response.Action == "observe":
		reason = "current_observation_reused"
	case response.OutputReplacement != "":
		reason = "exact_output_mask_available"
	case request.Event.Type == "prompt.submit" && response.RecoveryRef != "":
		reason = "repository_evidence_recorded"
	case response.RecoveryRef != "":
		reason = "exact_output_recorded"
	}
	inputBasis := map[string]any{
		"agent":       request.Agent.ID,
		"event":       request.Event.Type,
		"policy_mode": request.PolicyMode,
		"profile":     request.Profile,
	}
	if request.Tool != nil {
		inputBasis["tool_name"] = request.Tool.Name
		toolType, _, _ := classifyTool(request.Tool)
		inputBasis["tool_type"] = string(toolType)
		if request.Event.Type == "tool.after" {
			inputBasis["tool_result"] = "succeeded"
		} else if request.Event.Type == "tool.failure" {
			inputBasis["tool_result"] = "failed"
		}
	}
	if request.Prompt != nil {
		inputBasis["prompt"] = request.Prompt
	}
	if request.TaskProfile != nil {
		inputBasis["task_type"] = normalizedTaskType(request.TaskProfile.Type)
		inputBasis["task_term_count"] = len(repointel.NormalizeTerms(request.TaskProfile.Terms))
	}
	stateAfter := "active"
	if request.Event.Type == "session.end" {
		stateAfter = "ended"
	}
	data, err := json.Marshal(map[string]any{
		"schema":                "caveman.native.decision.v1",
		"decision_id":           response.DecisionID,
		"timestamp_ms":          stamp,
		"action":                response.Action,
		"reason":                reason,
		"input_basis":           inputBasis,
		"alternatives_rejected": []string{},
		"task_state_before":     "active",
		"task_state_after":      stateAfter,
		"currentness":           string(ccr.Current),
		"recovery_ref":          response.RecoveryRef,
		"runtime_us":            response.RuntimeUS,
	})
	if err != nil {
		return err
	}
	_, err = r.store.PutObject(ccr.Object{
		Type:             ccr.ObjectTaskDecision,
		Source:           "native:" + request.Event.Type,
		SessionID:        request.Session.ID,
		RepositoryState:  request.Session.RepositoryState,
		TransformVersion: "native-runtime-v1",
		Currentness:      ccr.Current,
		Lifecycle:        ccr.Hot,
		Data:             data,
	})
	return err
}

type storedTaskContract struct {
	TaskType            string         `json:"task_type"`
	SelectedPolicy      string         `json:"selected_policy"`
	TaskTerms           []string       `json:"task_terms,omitempty"`
	GoalRef             *PayloadDigest `json:"goal_ref,omitempty"`
	PolicyDeliveryEpoch int            `json:"policy_delivery_epoch"`
}

func (r *Runtime) ensureTaskContract(request Request) (string, error) {
	objects, err := r.store.ListSessionObjects(request.Session.ID, 100)
	if err != nil {
		return "", err
	}
	requestedTaskType := "general"
	requestedTerms := []string(nil)
	continuation := false
	if request.TaskProfile != nil {
		requestedTaskType = normalizedTaskType(request.TaskProfile.Type)
		requestedTerms = repointel.NormalizeTerms(request.TaskProfile.Terms)
		continuation = request.TaskProfile.Continuation
	}
	currentIDs := make([]string, 0, 1)
	var current *storedTaskContract
	for _, object := range objects {
		if object.Type == ccr.ObjectTaskContract && object.Currentness == ccr.Current {
			currentIDs = append(currentIDs, object.ID)
			var candidate storedTaskContract
			if current == nil && json.Unmarshal(object.Data, &candidate) == nil {
				candidate.TaskType = normalizedTaskType(candidate.TaskType)
				if candidate.SelectedPolicy == "" {
					candidate.SelectedPolicy = taskPolicy(candidate.TaskType)
				}
				current = &candidate
			}
		}
	}
	// Host adapters classify bounded conversational continuations from raw prompt
	// text. Everything else starts a fresh task contract, even when task type is
	// unchanged, so goal identity never leaks across unrelated requests.
	if continuation && current != nil {
		return "", nil
	}
	taskType := requestedTaskType
	selectedPolicy := taskPolicy(taskType)
	emitPolicy := current == nil || current.SelectedPolicy != selectedPolicy
	deliveryEpoch := 0
	if current != nil {
		deliveryEpoch = current.PolicyDeliveryEpoch
	}
	if emitPolicy && selectedPolicy != "core" {
		deliveryEpoch++
	}
	for _, id := range currentIDs {
		if err := r.store.SetObjectCurrentness(id, ccr.Stale); err != nil {
			return "", err
		}
	}
	contract := map[string]any{
		"task_type":              taskType,
		"selected_policy":        selectedPolicy,
		"task_terms":             requestedTerms,
		"policy_delivery_epoch":  deliveryEpoch,
		"goal":                   "satisfy current user request",
		"acceptance_conditions":  []string{"requested behavior works", "relevant verification passes"},
		"repository_constraints": []string{"preserve repository conventions", "preserve unrelated user changes"},
		"allowed_scope":          "task-relevant files",
		"material_assumptions":   []string{},
		"change_budget": map[string]any{
			"files_modified":          nil,
			"production_lines_added":  nil,
			"new_dependencies":        "no speculative dependency; optimize total lifecycle cost",
			"new_public_interfaces":   nil,
			"new_configuration":       nil,
			"new_database_migrations": nil,
			"new_services":            "add only when correct ownership or lifecycle design requires one",
			"tool_calls":              nil,
			"basis":                   "unspecified dimensions require repository evidence; additions need task or lifecycle justification",
		},
		"verification_plan": []string{"run focused verification", "report unresolved risks"},
		"stop_condition":    "acceptance conditions satisfied",
	}
	if skill, ok := nativepack.Select(taskType); ok {
		contract["active_skill"] = skill.ID
		contract["skill_evidence_status"] = skill.EvidenceStatus
		contract["stop_condition"] = skill.StopCondition
	}
	if request.Prompt != nil {
		contract["goal_ref"] = request.Prompt
	}
	data, err := json.Marshal(contract)
	if err != nil {
		return "", err
	}
	_, err = r.store.PutObject(ccr.Object{
		Type:             ccr.ObjectTaskContract,
		Source:           "native:prompt.submit",
		SessionID:        request.Session.ID,
		RepositoryState:  request.Session.RepositoryState,
		TransformVersion: "native-runtime-v1",
		Currentness:      ccr.Current,
		Lifecycle:        ccr.Hot,
		Data:             data,
	})
	if err != nil {
		return "", err
	}
	if !emitPolicy {
		return "", nil
	}
	if selectedPolicy == "core" && current == nil {
		return "", nil
	}
	return taskContractContext(taskType), nil
}

func normalizedTaskType(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "feature", "bugfix", "investigation", "refactor", "migration", "verification", "review":
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return "general"
	}
}

func taskPolicy(taskType string) string {
	if skill, ok := nativepack.Select(normalizedTaskType(taskType)); ok {
		return skill.ID
	}
	return "core"
}

func taskContractContext(taskType string) string {
	context := fmt.Sprintf(
		"Caveman task contract (dynamic tail): type=%s; policy=%s; scope=task-relevant files; avoid speculative dependencies and optimize total lifecycle cost; verify focused acceptance conditions; stop when satisfied.",
		normalizedTaskType(taskType), taskPolicy(taskType),
	)
	if skill, ok := nativepack.Select(normalizedTaskType(taskType)); ok {
		context += "\nActive skill " + skill.ID + ": " + strings.Join(strings.Fields(skill.Instructions), " ")
	} else {
		context += "\nCore only: prior task overlay ended; follow stable Core and current user request."
	}
	return context
}

func (r *Runtime) compactStateContext(sessionID string) (string, error) {
	objects, err := r.store.ListSessionObjects(sessionID, 1000)
	if err != nil {
		return "", err
	}
	taskType := "general"
	deliveryEpoch := 0
	currentObservations := 0
	decisions := 0
	for _, object := range objects {
		switch object.Type {
		case ccr.ObjectTaskContract:
			if object.Currentness != ccr.Current {
				continue
			}
			var contract storedTaskContract
			if json.Unmarshal(object.Data, &contract) == nil {
				taskType = normalizedTaskType(contract.TaskType)
				deliveryEpoch = contract.PolicyDeliveryEpoch
			}
		case ccr.ObjectTaskDecision:
			decisions++
		default:
			if object.Currentness == ccr.Current {
				currentObservations++
			}
		}
	}
	state := fmt.Sprintf(
		"Caveman compact state (dynamic tail): type=%s; policy=%s; policy delivery epoch=%d; current observations=%d; decisions=%d; exact recovery remains available through caveman_retrieve.",
		taskType, taskPolicy(taskType), deliveryEpoch, currentObservations, decisions,
	)
	if _, ok := nativepack.Select(taskType); !ok {
		return state, nil
	}
	return taskContractContext(taskType) + "\n" + state, nil
}

func (r *Runtime) beforeTool(request Request, response Response) (Response, error) {
	if request.Tool == nil {
		return response, nil
	}
	typeName, source, dependency := classifyTool(request.Tool)
	inputState := firstNonEmptyValue(request.Tool.InputState, request.Session.RepositoryState)
	if !reuseHintEligible(typeName, source, dependency, inputState) {
		return response, nil
	}
	objects, err := r.store.ListSessionObjects(request.Session.ID, 1000)
	if err != nil {
		return Response{}, err
	}
	for _, object := range objects {
		if object.Type == typeName && object.Source == source && object.Currentness == ccr.Current &&
			repositoryStateMatches(object.RepositoryState, inputState) {
			response.RecoveryRef = "ccr://" + object.ID
			repeat := priorRepeatCount(objects, response.RecoveryRef) + 1
			switch repeat {
			case 1:
				response.Action = "observe"
				response.decisionReason = "current_observation_reused"
				response.Context = fmt.Sprintf("Current observation already exists: %s; repository state unchanged; %s", source, response.RecoveryRef)
			case 3:
				response.Action = "observe"
				response.decisionReason = "verified_repeat_loop_intervention"
				response.Context = fmt.Sprintf("Verified repeat loop: unchanged observation %s requested %d times; use %s or make progress before reopening.", source, repeat, response.RecoveryRef)
			default:
				response.decisionReason = "repeat_hint_suppressed"
			}
			return response, nil
		}
	}
	return response, nil
}

func priorRepeatCount(objects []ccr.Object, recoveryRef string) int {
	count := 0
	for _, object := range objects {
		if object.Type != ccr.ObjectTaskDecision {
			continue
		}
		var decision struct {
			Reason      string `json:"reason"`
			RecoveryRef string `json:"recovery_ref"`
		}
		if json.Unmarshal(object.Data, &decision) != nil || decision.RecoveryRef != recoveryRef {
			continue
		}
		switch decision.Reason {
		case "current_observation_reused", "repeat_hint_suppressed", "verified_repeat_loop_intervention":
			count++
		}
	}
	return count
}

// reuseHintEligible governs advisory reuse only. Runtime never blocks tool
// execution or substitutes stored output. File reads can use session-local
// invalidation. Repo-wide searches, tests, and builds need matching non-empty
// repository state. File reads require matching per-file input state from the
// adapter; repo-wide observations require matching worktree/index state.
// Browser, arbitrary command, network, time, random, process,
// deployment, and destructive results are recorded for recovery but never
// suggested as current observations.
func reuseHintEligible(typeName ccr.ObjectType, source, dependency, repositoryState string) bool {
	switch typeName {
	case ccr.ObjectFileObservation:
		return source != "unknown" && dependency != "" && repositoryState != ""
	case ccr.ObjectSearchResult, ccr.ObjectTestResult, ccr.ObjectBuildResult:
		return source != "unknown" && repositoryState != ""
	default:
		return false
	}
}

func repositoryStateMatches(observed, current string) bool {
	return current != "" && observed == current
}

func (r *Runtime) afterTool(request Request, response Response, allowMask bool) (Response, error) {
	if request.Tool == nil {
		return response, nil
	}
	typeName, source, dependency := classifyTool(request.Tool)
	if typeName == ccr.ObjectDiffSnapshot && dependency != "" {
		if err := r.invalidateDependency(request.Session.ID, dependency); err != nil {
			return Response{}, err
		}
		r.captureTestImpact(request, dependency)
	}
	if len(request.Tool.Output) == 0 {
		return response, nil
	}
	_, secretRules := redact.String(string(request.Tool.Output))
	if len(secretRules) > 0 {
		return response, nil
	}
	dependencies := []string{}
	if dependency != "" {
		dependencies = append(dependencies, dependency)
	}
	id, err := r.store.PutObject(ccr.Object{
		Type:               typeName,
		Source:             source,
		SessionID:          request.Session.ID,
		RepositoryState:    firstNonEmptyValue(request.Tool.InputState, request.Session.RepositoryState),
		TransformVersion:   "native-runtime-v1",
		Currentness:        ccr.Current,
		Lifecycle:          ccr.Hot,
		Dependencies:       dependencies,
		OriginalByteLength: len(request.Tool.Output),
		StoredByteLength:   len(request.Tool.Output),
		Data:               request.Tool.Output,
	})
	if err != nil {
		return Response{}, err
	}
	response.RecoveryRef = "ccr://" + id
	// The replacement stands in for the WHOLE tool output, so it has to say how to
	// get the bytes back. Without the last line an agent sees an opaque reference
	// and no verb: measured 2026-08-08, inventory-mismatch and
	// webhook-delivery-gaps read "every taskdata_fetch call returns only an opaque
	// ccr:// pointer", made 27-97 flailing recovery calls, and timed out without
	// answering. Naming the tool and the exact argument turns a dead end into one
	// call.
	replacement := fmt.Sprintf(
		"[%s]\nstatus: current\nsource: %s\nfull: %s\nrecover: call caveman_retrieve with recovery_handle=%q to get these bytes back in full — one call returns the entire original.",
		typeName, source, response.RecoveryRef, response.RecoveryRef)
	// Mask only when replacement plus one retrieval call and safety margin is
	// materially smaller. Bytes are conservative proxy for tokenizer variance.
	if allowMask && len(request.Tool.Output) > len(replacement)+256+128 &&
		!r.maskWouldDiscardFacts(request.Tool.Output) {
		response.OutputReplacement = replacement
		if err := r.store.SetObjectLifecycle(id, ccr.Warm); err != nil {
			return Response{}, err
		}
	}
	return response, nil
}

// maskWouldDiscardFacts reports whether replacing this tool output with a pointer
// would throw away a summary the elision engine could have put in its place.
//
// Mask what cannot be summarized in place; elide what can.
//
// The two mechanisms overlap and only one of them should touch a given payload.
// Masking is the right answer for a blob nothing can describe — a minified
// bundle, a screenshot dump, forty thousand lines of prose — where the only
// honest compression is "it is over there". But on structured tool output the
// elision engine keeps the head and tail rows, keeps one representative of every
// resemblance class, and states the class invariants of everything it dropped
// (`state=charged`, `status: delivered×18 attempted×17`,
// `wh-5000..wh-5059 all 60 present`). Masking that payload first destroys all of
// it: the agent never sees a row, and after it recovers, the FULL original enters
// context through the recovery-exempt path — the whole page, plus the stub, plus
// a turn. Strictly worse than no wrap at all.
//
// Measured 2026-08-08 on the shipped default (compress -> native policy "safe" ->
// profile "full-safe" -> masking on): inventory-mismatch and
// webhook-delivery-gaps served JSON and CSV pages over the threshold, were masked
// into 4-line stubs, and scored 0/6 with 27-97 recovery calls.
// rate-limit-forensics scored 3/3 at ~35% cheaper for one reason — its pages sat
// under the threshold and reached the elision path intact.
//
// Only classes with a field grammar qualify, because only those produce
// invariants: JSON records, CSV/TSV/markdown rows, and line-oriented logs
// (logfmt and NDJSON both, via the log compressor's per-line reader). Everything
// else — text, code, diff, terminal, HTML, config, search results, and anything
// undetected — keeps today's masking behaviour exactly.
//
// It fails toward masking: an unavailable classifier returns false, so the
// fallback is bounded context rather than an unbounded pass-through.
func (r *Runtime) maskWouldDiscardFacts(output []byte) bool {
	if r == nil || r.detect == nil {
		return false // cannot classify -> behave exactly as before
	}
	switch r.detect(output) {
	case engine.TypeJSON, engine.TypeTabular, engine.TypeLog:
		return true
	default:
		return false
	}
}

func firstNonEmptyValue(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func (r *Runtime) captureTestImpact(request Request, changedPath string) {
	if strings.TrimSpace(request.Session.CWD) == "" {
		return
	}
	key := repositoryKey(request.Session)
	r.repositoryMu.RLock()
	entry := r.repositoryMaps[key]
	r.repositoryMu.RUnlock()
	if entry == nil {
		return
	}
	select {
	case <-entry.done:
		r.repositoryMu.RLock()
		repoMap, mapErr := entry.repoMap, entry.err
		r.repositoryMu.RUnlock()
		if mapErr != nil {
			return
		}
		impact := repointel.ImpactTests(repoMap, []string{changedPath})
		if len(impact.AffectedTests) == 0 {
			return
		}
		data, err := json.Marshal(impact)
		if err != nil {
			return
		}
		_, _ = r.store.PutObject(ccr.Object{
			Type: ccr.ObjectExecutionState, Source: "native:test-impact:" + filepath.ToSlash(filepath.Clean(changedPath)),
			SessionID: request.Session.ID, RepositoryState: request.Session.RepositoryState,
			TransformVersion: "repository-test-impact-v1", Currentness: ccr.Current, Lifecycle: ccr.Hot,
			Dependencies: []string{changedPath}, Data: data,
		})
	default:
	}
}

func (r *Runtime) invalidateDependency(sessionID, path string) error {
	objects, err := r.store.ListSessionObjects(sessionID, 1000)
	if err != nil {
		return err
	}
	clean := filepath.Clean(path)
	for _, object := range objects {
		if object.Currentness != ccr.Current {
			continue
		}
		for _, dependency := range object.Dependencies {
			if filepath.Clean(dependency) == clean {
				if err := r.store.SetObjectCurrentness(object.ID, ccr.Stale); err != nil {
					return err
				}
				break
			}
		}
	}
	return nil
}

func classifyTool(tool *Tool) (ccr.ObjectType, string, string) {
	name := strings.ToLower(strings.TrimSpace(tool.Name))
	values := map[string]any{}
	_ = json.Unmarshal(tool.Input, &values)
	value := func(keys ...string) string {
		for _, key := range keys {
			if raw, ok := values[key].(string); ok && strings.TrimSpace(raw) != "" {
				clean, fired := redact.String(strings.TrimSpace(raw))
				if len(fired) > 0 {
					sum := sha256.Sum256([]byte(raw))
					return "redacted:sha256:" + hex.EncodeToString(sum[:8])
				}
				return clean
			}
		}
		return ""
	}
	path := value("path", "file_path", "file", "filename")
	command := value("command", "cmd")
	query := value("query", "pattern", "search")
	switch {
	case strings.Contains(name, "apply_patch") || strings.Contains(name, "write") || strings.Contains(name, "edit"):
		return ccr.ObjectDiffSnapshot, firstNonEmpty(path, name), path
	case strings.Contains(name, "read") || strings.Contains(name, "view_file"):
		return ccr.ObjectFileObservation, firstNonEmpty(path, name), path
	case strings.Contains(name, "search") || strings.Contains(name, "grep") || name == "rg" || strings.Contains(name, "find"):
		return ccr.ObjectSearchResult, firstNonEmpty(query, command, name), path
	case strings.Contains(name, "browser") || strings.Contains(name, "screenshot"):
		return ccr.ObjectBrowserSnapshot, name, ""
	case strings.Contains(command, " test") || strings.HasPrefix(command, "test ") || strings.Contains(command, "pytest") || strings.Contains(command, "go test"):
		return ccr.ObjectTestResult, firstNonEmpty(command, name), path
	case strings.Contains(command, " build") || strings.HasPrefix(command, "build ") || strings.Contains(command, "go build"):
		return ccr.ObjectBuildResult, firstNonEmpty(command, name), path
	default:
		return ccr.ObjectCommandResult, firstNonEmpty(command, name), path
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return "unknown"
}

func decisionID(request Request, reason string) string {
	stamp := request.Event.TimestampMS
	if stamp == 0 {
		stamp = time.Now().UnixMilli()
	}
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%s\x00%s\x00%d", request.Session.ID, request.Event.Type, reason, stamp)))
	return "dec_" + hex.EncodeToString(sum[:12])
}
