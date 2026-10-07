package gateway

import (
	"bytes"
	"container/list"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"io"
	"maps"
	"math"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/JuliusBrussee/caveman/proxy/providers"
	"github.com/JuliusBrussee/caveman/proxy/providers/jsonsplice"
)

// The route stage (ADR 0083 §4: parse, ask, compress, route). The ask starts
// before compression so the Cloud round trip overlaps it; the answer is applied
// to the bytes compression produced. An answer names another model of the
// provider the agent already talks to, swapped into the body's top-level
// "model", and the effort to run at; or a pool entry on another login or the
// Cloud gateway (RouteAnswer.Target, route_pool.go). Every failure keeps
// the asked model and runs at the request's own top-level effort: a session
// that has per-message state keeps its marks and fixed top-level field (so its
// history does not change) and gets the agent's effort (or, when it sets
// none, the model's default) back with a mark, and one without marks goes as
// the agent sent it (see applyEffort); the request is never held past the
// link's budget. Cloud decides; this side reports facts and applies the answer.

// RouteAsk is what the route stage knows about one request. Body is read-only
// and never sent whole: the link derives counts, a local cache key, the ask's
// text (the latest human turn, the one before it and the end of the agent's
// last reply) and what the request declares (tool names, effort) from it.
type RouteAsk struct {
	Provider string
	Endpoint string
	Model    string
	Agent    string
	// SessionID keys the session; a Claude Code child is its session id plus
	// its agent id, and ParentSessionID is then its parent's key.
	SessionID       string
	ParentSessionID string
	ToolsCount      int
	InputBytes      int
	Body            []byte
	// Labels are the raw values of the routeLabelNames headers the request carries.
	Labels map[string]string
	// PerRequest is a request the agent labels compaction or auxiliary: it is
	// answered on its own, with no ask text and no decision shared with a turn.
	PerRequest bool
	// Last is what this session's previous upstream request ran; nil on its first.
	Last *RouteLast
	// PerMessageOff: this session's per-message effort is latched off.
	PerMessageOff bool
}

// RouteLast is what one session's previous upstream request ran (contracts
// route-ask-v1 last): the model the provider's answer names, the effort in
// force in the bytes sent, and the provider's usage. InputTokens is the total
// input, cache reads and writes included.
type RouteLast struct {
	Model            string `json:"model"`
	Effort           string `json:"effort"`
	AgeS             int    `json:"age_s"`
	InputTokens      int    `json:"input_tokens"`
	CacheReadTokens  int    `json:"cache_read_tokens"`
	CacheWriteTokens int    `json:"cache_write_tokens"`
	Compacted        bool   `json:"compacted"`
}

// RouteAnswer is the decision for one request. Model is set only when the
// request moves to it. Effort ("" leaves the request's) is applied per
// EffortMode on Anthropic: "message" (a per-message mark) or "top".
// DefaultEffort is the asked model's catalog default effort ("" unknown): the
// session keeps it for requests that set no effort of their own. Outcome is
// the runtime/v1 route outcome: routed, kept, degraded, paused or off.
type RouteAnswer struct {
	Model         string
	Effort        string
	EffortMode    string
	DefaultEffort string
	Outcome       string
	Reason        string
	DecisionID    string
	// Reject, when set, tells the link the provider refused the routed model
	// or effort, so the rest of this ask stays as the agent asked.
	Reject func()
	// Target, when set, sends the request to a pool entry off the harness's
	// own provider and credential (route_pool.go); Model is then unset.
	Target *RouteTarget
}

// CloudLink is the optional signed-in Cloud connection: the route stage asks
// it for a model, and every recorded request is offered to its runtime/v1
// sender. Both fail open and never block a request. Nil means a local-only
// proxy, exactly as before.
type CloudLink interface {
	// Ask starts the decision and returns the wait for it.
	Ask(ctx context.Context, ask RouteAsk) func() RouteAnswer
	Observe(rec RequestRecord)
}

// routable: Anthropic Messages or OpenAI chat/responses. Only API-key traffic
// to the provider's own API is asked about: subscription (OAuth Pro/Max) turns
// have no per-request dollar cost, so routing does nothing there (ADR 0083 §7).
func routable(provider, endpoint string) bool {
	switch provider {
	case "anthropic":
		return strings.HasSuffix(endpoint, "/messages")
	case "openai":
		return strings.HasSuffix(endpoint, "/chat/completions") || strings.HasSuffix(endpoint, "/responses")
	}
	return false
}

// setModel replaces the value of the top-level "model" string and leaves every
// other byte as it was. ok is false when there is no such string field.
func setModel(body []byte, model string) ([]byte, bool) {
	root, ok := jsonsplice.Root(body)
	if !ok {
		return body, false
	}
	span, ok := jsonsplice.Field(body, root, "model")
	if _, isString := jsonsplice.String(body, span); !ok || !isString {
		return body, false
	}
	encoded, _ := json.Marshal(model)
	out, err := jsonsplice.ReplaceRaw(body, span, encoded)
	if err != nil {
		return body, false
	}
	return out, true
}

// routeLabelNames are the request headers an ask carries raw (contracts
// route-ask-v1 request.labels). Cloud reads them; nothing here acts on their
// values except the session keys and the agent's own compaction/auxiliary label.
var routeLabelNames = []string{
	"x-claude-code-request-class", "x-claude-code-compaction", "x-claude-code-agent-id",
	"x-claude-code-parent-agent-id", "x-claude-code-context-compacted", "x-openai-subagent",
	"x-codex-turn-metadata", "x-caveman-agent", "x-parent-session-id",
	"x-codex-parent-thread-id", "thread-id",
}

// RouteLabelMax is the most bytes a label value may have; a longer one is left
// out, never cut (Cloud refuses a cut value). Codex's turn metadata is JSON and
// may be longer.
func RouteLabelMax(name string) int {
	if name == "x-codex-turn-metadata" {
		return 16 << 10
	}
	return 256
}

// routeRun carries one request's route-stage facts from the ask to the response.
type routeRun struct {
	key, parent string
	asked       string // the model the agent asked for
	labels      map[string]string
	perRequest  bool // the agent labels it compaction or auxiliary
	auxiliary   bool // the agent labels it auxiliary (a side request, not a compaction)
	compacted   bool // a compaction, or a request after one
	off         bool // the route stage was off for it
	replay      bool // count_tokens: the session's marks and heal, nothing new
	applied     bool // the route stage set the effort or sent marks
	marked      bool // the body sent carries the session's per-message marks
	unmarked    []byte
	marks       []effortMark // the marks the body sent carries, in order
	dropBlocks  bool         // the body sent asks the provider to drop unbound thinking
	// heal: a thinking-binding 400 may be the route stage's doing (it was on
	// for the request, or the session carries its marks or heal) and the
	// agent's body sets no thinking.block_binding of its own; noDropBlock: the
	// session's drop_block retry was refused, so the heal strips.
	heal, noDropBlock bool
	stripped          bool // the body sent lost thinking blocks to a remembered strip
	// effort is the effort in force in the body sent, "" when the route stage
	// did not set it (then it is read back from the bytes).
	effort string
}

// newRouteRun reads the labels and the session keys. The session is the
// caller's session id when it is exact, else the agent's own session header
// (a Codex thread is its own session); a Claude Code child
// (x-claude-code-agent-id) is that plus its agent id. Codex's turn metadata
// comes from the body's client_metadata when no header carried it.
func newRouteRun(h http.Header, sessionID, endpoint string, body []byte) *routeRun {
	run := &routeRun{labels: map[string]string{}}
	label := func(name, value string) {
		if value != "" && len(value) <= RouteLabelMax(name) && utf8.ValidString(value) {
			run.labels[name] = value
		}
	}
	for _, name := range routeLabelNames {
		label(name, h.Get(name))
	}
	responses := strings.HasSuffix(endpoint, "/responses")
	if _, ok := run.labels["x-codex-turn-metadata"]; !ok && responses {
		if root, ok := objectRoot(body); ok {
			metadata, _ := jsonsplice.Field(body, root, "client_metadata")
			turn, _ := jsonsplice.StringField(body, metadata, "x-codex-turn-metadata")
			label("x-codex-turn-metadata", turn)
		}
	}
	var turn struct {
		RequestKind  string `json:"request_kind"`
		ParentThread string `json:"parent_thread_id"`
		SubagentKind string `json:"subagent_kind"`
	}
	_ = json.Unmarshal([]byte(run.labels["x-codex-turn-metadata"]), &turn)
	for _, name := range []string{"x-claude-code-session-id", "thread-id", "session-id", "session_id"} {
		if sessionID == "" {
			sessionID = cutBytes(h.Get(name), 256)
		}
	}
	spawner, thread := run.labels["x-codex-parent-thread-id"], run.labels["thread-id"]
	if spawner == "" {
		spawner = turn.ParentThread
	}
	if run.key = sessionID; sessionID != "" {
		switch agent := run.labels["x-claude-code-agent-id"]; {
		case agent != "":
			run.key, run.parent = sessionID+"#"+agent, sessionID
			if parent := run.labels["x-claude-code-parent-agent-id"]; parent != "" {
				run.parent += "#" + parent
			}
		case thread != "" && spawner != "" && spawner != thread:
			run.parent = spawner
		case run.labels["x-parent-session-id"] != "":
			run.parent = run.labels["x-parent-session-id"]
		}
	}
	// A request the agent labels a compaction or a side request is answered on
	// its own: the same label rules Cloud's request kinds read (Claude Code's
	// request class and compaction flag, OpenCode's agent name, Codex's
	// subagent kind and turn metadata request kind).
	side := ""
	switch {
	case responses:
		kind := run.labels["x-openai-subagent"]
		if kind == "" {
			kind = turn.SubagentKind
		}
		switch {
		case turn.RequestKind == "compaction" || kind == "compact" && turn.RequestKind != "memory":
			side = "compaction"
		case turn.RequestKind == "memory" || kind != "" && kind != "collab_spawn" && kind != "thread_spawn" && kind != "review":
			side = "auxiliary"
		}
	case strings.HasSuffix(endpoint, "/messages"):
		side = strings.ToLower(strings.TrimSpace(run.labels["x-claude-code-request-class"]))
		if run.labels["x-claude-code-agent-id"] == "" {
			switch strings.ToLower(strings.TrimSpace(run.labels["x-caveman-agent"])) {
			case "title", "summary":
				side = "auxiliary"
			case "compaction":
				side = "compaction"
			}
		}
		if run.labels["x-claude-code-compaction"] != "" {
			side = "compaction"
		}
	}
	run.auxiliary = side == "auxiliary"
	run.perRequest = run.auxiliary || side == "compaction"
	run.compacted = side == "compaction" || run.labels["x-claude-code-context-compacted"] != ""
	return run
}

// cutBytes keeps at most n bytes of text, cut on a rune boundary.
func cutBytes(text string, n int) string {
	if len(text) <= n {
		return text
	}
	for n > 0 && !utf8.RuneStart(text[n]) {
		n--
	}
	return text[:n]
}

// routeSessionsMax bounds the sessions the route stage remembers, like the
// session ledger; least recently used goes first. Memory only, never on disk.
const routeSessionsMax = 1024

// routeSession is what the route stage remembers about one session: the
// previous upstream request's facts and the per-message effort marks.
type routeSession struct {
	key    string
	last   *RouteLast
	lastAt time.Time
	// refused are the models that refused per-message effort: they get no
	// marks, and the session reports per_message_off once there is one.
	refused map[string]bool
	// moved: a request of the session was served by another model than the
	// asked one, so its history carries thinking that model wrote.
	moved bool
	// version counts the writes to effortState: a request writes its copy
	// back only when nothing landed in between.
	version int
	effortState
}

// effortState is a session's per-message effort: top is the top-level effort,
// fixed at its first per-message request (nil until then); salt keys the
// anchors. pending are the marks of conversations that matched none of marks
// (a side request, or history compaction rewrote), newest first: a later
// request continuing one takes the session over. A served binding heal leaves
// either dropBlocks (later requests ask the provider to drop unbound thinking)
// or a strip: the agent's messages stripFrom up to strip lose their thinking
// blocks (stripFirst hashes the first message, stripAnchor the last of them),
// even should the agent later set a block_binding of its own (the history
// stays as served). A refused drop_block retry sets noDropBlock, for good (it
// depends on the model and thinking type, not the history): the strip path
// from then on. A refused strip sets noHeal, which goes with the marks: kept
// in a pending set when another conversation takes the session over, cleared
// when the marks are forgotten. In a session without marks neither ever
// clears (noHeal lasts the session's life, and a leftover strip keeps
// started() true). Its slices are replaced, never written in place, so a copy
// is safe to read.
type effortState struct {
	salt  [16]byte
	top   *string
	marks []effortMark
	// marked is the request shape the marks last went into (marks unset).
	marked  pendingMarks
	pending []pendingMarks
	// defaultEffort is defaultModel's catalog default effort, from Cloud's
	// answers: what a request that sets none runs at.
	defaultEffort, defaultModel string
	dropBlocks                  bool
	stripFrom                   int
	strip                       int
	stripFirst, stripAnchor     [32]byte
	noDropBlock, noHeal         bool
}

// pendingMarks are the marks of a conversation that matched none of the
// session's: first hashes its first message, last the last message of the
// request that made them and n that request's message count. A later request
// takes them over only when it starts the same, is longer and carries that
// last message where it was (a continuation, not a side request sent again).
type pendingMarks struct {
	first, last [32]byte
	n           int
	marks       []effortMark
	top         *string // the fixed top-level effort that went with them
	noHeal      bool    // and the refused strip
}

const pendingMax = 4

func (state effortState) started() bool {
	return state.top != nil || len(state.marks) > 0 || state.dropBlocks || state.strip > 0
}

// effortMark is one per-message effort mark: it goes before message at of the
// agent's own messages, where anchor hashes the message before it.
type effortMark struct {
	at     int
	anchor [32]byte
	effort string
}

type routeSessions struct {
	mu      sync.Mutex
	entries map[string]*list.Element
	order   *list.List
}

// get returns key's session, making it when create is set; the caller holds mu.
func (rs *routeSessions) get(key string, create bool) *routeSession {
	if key == "" {
		return nil
	}
	if element, ok := rs.entries[key]; ok {
		rs.order.MoveToFront(element)
		return element.Value.(*routeSession)
	}
	if !create {
		return nil
	}
	if rs.entries == nil {
		rs.entries, rs.order = map[string]*list.Element{}, list.New()
	}
	if rs.order.Len() >= routeSessionsMax {
		oldest := rs.order.Back()
		rs.order.Remove(oldest)
		delete(rs.entries, oldest.Value.(*routeSession).key)
	}
	session := &routeSession{key: key}
	_, _ = rand.Read(session.salt[:])
	rs.entries[key] = rs.order.PushFront(session)
	return session
}

// facts is what an ask reports about key's session: its previous request
// (age_s counted now) and the per-message latch.
func (rs *routeSessions) facts(key string, now time.Time) (*RouteLast, bool) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	session := rs.get(key, false)
	if session == nil || session.last == nil {
		return nil, session != nil && len(session.refused) > 0
	}
	last := *session.last
	last.AgeS = int(min(max(now.Sub(session.lastAt), 0)/time.Second, 1_000_000_000))
	return &last, len(session.refused) > 0
}

func (rs *routeSessions) served(key string, last RouteLast, at time.Time, moved bool) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if session := rs.get(key, true); session != nil {
		session.last, session.lastAt = &last, at
		session.moved = session.moved || moved
	}
}

// latch records that model refused per-message effort in key's session, for
// good: it gets no marks from here on.
func (rs *routeSessions) latch(key, model string) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if session := rs.get(key, true); session != nil {
		if session.refused == nil {
			session.refused = map[string]bool{}
		}
		session.refused[model] = true
		session.version++
	}
}

// healed records a served binding heal so later requests keep the history
// that was served: dropBlocks on, or the thinking blocks of the agent's
// messages from on stripped (body is the agent's own messages, unmarked). A
// second strip on the same history widens the first.
func (rs *routeSessions) healed(key string, dropBlocks bool, body []byte, from int) {
	_, _, items, ok := messageSpans(body)
	rs.mu.Lock()
	defer rs.mu.Unlock()
	session := rs.get(key, true)
	switch {
	case session == nil:
		return
	case dropBlocks:
		session.dropBlocks = true
	case ok:
		first := anchorAt(body, items, session.salt, 0)
		if session.strip > 0 && session.strip <= len(items) && first == session.stripFirst &&
			anchorAt(body, items, session.salt, session.strip) == session.stripAnchor {
			from = min(from, session.stripFrom)
		}
		session.stripFrom, session.strip = from, len(items)
		session.stripFirst, session.stripAnchor = first, anchorAt(body, items, session.salt, len(items))
	}
	session.version++
}

// healFailed records a binding heal retry the provider refused too: a refused
// drop_block moves the session to the strip path, a refused strip ends its
// binding heals.
func (rs *routeSessions) healFailed(key string, dropBlock bool) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if session := rs.get(key, true); session != nil {
		if dropBlock {
			session.noDropBlock = true
		} else {
			session.noHeal = true
		}
		session.version++
	}
}

// applyEffort applies the answer's effort to body (the bytes compression
// produced, model already set) and returns what to send. Responses and chat
// take their effort field. Anthropic takes it top-level, or as a per-message
// mark that keeps the cached prefix; a mark is never introduced into a session
// without marks unless Cloud answered effort_mode "message". A session's marks
// come back on every request it sends to a model that has not refused them, at
// the same places and byte-identical, so its history does not change under
// the agent; a "top" answer then goes in as one more mark. Without an effort
// from Cloud (a failure, routing off, effort "") a session without marks gets
// the agent's bytes as sent (one cache restart where Cloud's top-level effort
// was); one with marks runs at the request's own top-level effort, or at the
// model's default effort (kept from Cloud's answers) when it sets none, put
// back with a mark when it differs from the one in force, and with no default
// known goes without the marks, which the session forgets unless the request
// went to another model (see perMessage). Compaction and side requests read
// the session's state but never change it; count_tokens (replay) gets the
// marks, strip and drop_block as they are, of a session already known or its
// parent. A forked child takes its parent's refusals, and its whole state
// (marks, heal) when it resends the parent's history.
func (s *Server) applyEffort(run *routeRun, provider, endpoint, model string, body []byte, answer RouteAnswer) []byte {
	run.off = answer.Outcome == "off"
	if answer.Effort == "" || run.off {
		answer.Effort, answer.EffortMode = "", ""
	}
	switch {
	case strings.HasSuffix(endpoint, "/responses"):
		if answer.Effort != "" {
			body, run.applied = setString(body, answer.Effort, "reasoning", "effort")
			run.effort = answer.Effort
		}
		return body
	case strings.HasSuffix(endpoint, "/chat/completions"):
		if answer.Effort != "" {
			body, run.applied = setString(body, answer.Effort, "reasoning_effort")
			run.effort = answer.Effort
		}
		return body
	case provider != "anthropic":
		return body
	}
	mode := answer.EffortMode
	if mode == "" && answer.Effort != "" {
		mode = "top"
	}
	// Work on a copy so a body is never spliced under the lock; only a turn's
	// own requests write it back.
	s.routes.mu.Lock()
	session := s.routes.get(run.key, !run.replay)
	if session == nil && run.replay && run.key != "" {
		session = &routeSession{key: run.key} // a child's first count_tokens: its parent's state, nothing kept
	}
	if session == nil {
		s.routes.mu.Unlock()
		run.heal = !run.off && !blockBinding(body)
		if mode == "top" {
			body, run.applied = setString(body, answer.Effort, "output_config", "effort")
			run.effort = answer.Effort
		}
		return body
	}
	work, version, moved := session.effortState, session.version, session.moved
	refusals := maps.Clone(session.refused)
	var parent *routeSession
	if !work.started() {
		if p := s.routes.get(run.parent, false); p != nil && (p.started() || len(p.refused) > 0 || p.moved) {
			copied := *p
			copied.refused = maps.Clone(p.refused)
			parent = &copied
		}
	}
	s.routes.mu.Unlock()
	if parent != nil {
		for refused := range parent.refused {
			if refusals == nil {
				refusals = map[string]bool{}
			}
			refusals[refused] = true
		}
		if !freshConversation(body) {
			work, moved = parent.effortState, moved || parent.moved
		} else if work.defaultModel == "" {
			work.defaultEffort, work.defaultModel = parent.defaultEffort, parent.defaultModel
		}
	}
	if answer.DefaultEffort != "" {
		work.defaultEffort, work.defaultModel = answer.DefaultEffort, run.asked
	}
	run.heal = (!run.off || work.started() || moved) && !blockBinding(body) && !work.noHeal
	run.noDropBlock = work.noDropBlock

	body, run.stripped = work.applyStrip(body)
	switch {
	case refusals[model]:
		// This model refused marks: Cloud's effort goes top-level, as the heal
		// that found out sent it.
		if answer.Effort != "" {
			body, run.applied = setString(body, answer.Effort, "output_config", "effort")
			run.effort = answer.Effort
		}
	case mode == "top" && len(work.marks) == 0:
		root, _ := objectRoot(body)
		own := topEffort(body, root)
		body, run.applied = setString(body, answer.Effort, "output_config", "effort")
		run.effort = answer.Effort
		if !run.perRequest {
			// The cache restarts here: later marks start from this level, or
			// from none when the agent sets none (Cloud's never becomes it).
			work.top = nil
			if own != "" {
				top := answer.Effort
				work.top = &top
			}
		}
	case work.top == nil && (mode != "message" || run.perRequest):
	case run.replay && len(work.marks) == 0: // count_tokens adds nothing to a history without marks
	default:
		var out []byte
		restore := answer.Effort == "" && !run.replay
		fallback := ""
		if work.defaultModel == model {
			fallback = work.defaultEffort
		}
		out, run.unmarked, run.marks, run.effort = perMessage(body, &work, markAsk{
			effort: answer.Effort, top: mode == "top", fallback: fallback,
			allowNew: !run.perRequest || mode == "top", restore: restore, moved: model != run.asked,
		})
		run.marked = run.unmarked != nil
		run.applied = run.marked || !bytes.Equal(out, body)
		body = out
	}
	if work.dropBlocks {
		if out, ok := withDropBlock(body); ok {
			body, run.dropBlocks = out, true
		}
	}
	if !run.perRequest {
		s.routes.mu.Lock()
		if live := s.routes.get(run.key, true); live != nil && live.version == version {
			live.effortState, live.refused = work, refusals
			live.version++
		}
		s.routes.mu.Unlock()
	}
	return body
}

// freshConversation reports a Messages body without an assistant turn yet.
func freshConversation(body []byte) bool {
	_, _, items, _ := messageSpans(body)
	for _, item := range items {
		if role, _ := jsonsplice.StringField(body, item, "role"); role == "assistant" {
			return false
		}
	}
	return true
}

// applyStrip strips the thinking blocks a served binding heal stripped, while
// the history still starts the same way; changed reports a body it changed.
// Another history (a side request, compacted history) is left alone and
// changes nothing: the strip stays for the conversation it was made for.
func (state *effortState) applyStrip(body []byte) (out []byte, changed bool) {
	if state.strip == 0 {
		return body, false
	}
	_, _, items, ok := messageSpans(body)
	if !ok || state.strip > len(items) || anchorAt(body, items, state.salt, 0) != state.stripFirst ||
		anchorAt(body, items, state.salt, state.strip) != state.stripAnchor {
		return body, false
	}
	return dropThinking(body, state.stripFrom, state.strip)
}

// markAsk is what perMessage applies to one request.
type markAsk struct {
	effort   string // Cloud's effort, "" none
	top      bool   // Cloud answered effort_mode "top"
	fallback string // the model's default effort, "" unknown
	allowNew bool   // a new mark may go in (not a compaction or side request)
	restore  bool   // no effort from Cloud: the request runs at its own
	moved    bool   // the request goes to another model than the asked one
}

// perMessage re-inserts the session's marks into an Anthropic Messages body
// and, when allowed, adds one for an effort. It returns the body to send,
// that body without marks (nil when it carries none), the marks it carries
// and the effort in force. A mark is never introduced into a history that
// has none unless Cloud answered effort_mode "message".
//
// The session's top-level effort is fixed at its first per-message request:
// the routed effort on a fresh conversation (no assistant turn yet) whose
// request sets one, else the request's own (none stays none: a routed effort
// then goes in as a mark, first in messages on a fresh conversation, which
// the effort docs allow). A "top" answer on history carrying the session's
// marks goes in as one more mark (never wiping them); on any other history it
// sets the top-level field of that body only.
//
// restore (no effort from Cloud): a history without the session's marks goes
// as the agent sent it (top-level field included; the session's fixed one is
// forgotten, one cache restart). With marks it runs at the effort the request
// set itself, else at fallback (the model's default effort), cache-safely
// with a mark; with neither it goes as the agent sent it, without the marks,
// which the session forgets (the thinking blocks after them lose their
// binding), unless the request went to another model (moved).
//
// A new mark goes in only when the effort differs from the one in force (the
// newest per-message effort, the agent's own or a mark, else top-level), just
// before the last user turn, or at the end when that turn carries a tool
// result (never between a tool_use and its tool_result), and never before an
// earlier mark or per-message effort. A mark whose anchor no longer matches is
// dropped with every later one. A body matching none of the session's marks (a
// side request, or history compaction rewrote) keeps its own top-level field,
// and a mark it gets is its own, kept as pending (see pendingMarks) until a
// request continuing that conversation takes the session over; the session's
// marks then become pending in turn.
func perMessage(body []byte, state *effortState, ask markAsk) (out, unmarked []byte, sent []effortMark, inForce string) {
	root, array, items, ok := messageSpans(body)
	if !ok {
		return body, nil, nil, ""
	}
	matching := func(marks []effortMark) []effortMark {
		kept := marks[:0:0]
		for _, mark := range marks {
			if mark.at > len(items) || anchorAt(body, items, state.salt, mark.at) != mark.anchor {
				break
			}
			kept = append(kept, mark)
		}
		return kept
	}
	own, effort, allowNew := topEffort(body, root), ask.effort, ask.allowNew
	marks := matching(state.marks)
	main := len(marks) > 0 || len(state.marks) == 0
	var first [32]byte
	takeover := -1
	if !main {
		first = anchorAt(body, items, state.salt, 0)
		for i, set := range state.pending {
			if set.first == first && len(items) > set.n && anchorAt(body, items, state.salt, set.n) == set.last {
				if marks = matching(set.marks); len(marks) > 0 {
					main, takeover = true, i
					break
				}
			}
		}
	}
	if ask.top && len(marks) == 0 { // no marks in this history: the top-level field, this body only
		out, _ := setString(body, effort, "output_config", "effort")
		return out, nil, nil, effort
	}
	if ask.restore {
		if len(marks) == 0 {
			if main && !ask.moved {
				state.top = nil
			}
			return body, nil, nil, ""
		}
		effort, allowNew = own, true
		if own == "" {
			if ask.fallback == "" {
				if !ask.moved {
					state.top, state.marks, state.pending, state.noHeal = nil, nil, nil, false
				}
				return body, nil, nil, ""
			}
			effort = ask.fallback
		}
	}
	if takeover >= 0 { // that conversation continues: it takes the session over
		pending := slices.Delete(slices.Clone(state.pending), takeover, takeover+1)
		if len(state.marks) > 0 {
			previous := state.marked
			previous.marks, previous.top, previous.noHeal = state.marks, state.top, state.noHeal
			pending = addPending(pending, previous)
		}
		set := state.pending[takeover]
		state.top, state.pending, state.noHeal = set.top, pending, set.noHeal
	}
	if main {
		if state.top == nil {
			top := own
			if own != "" && effort != "" && freshConversation(body) {
				top = effort
			}
			state.top = &top
		}
		if *state.top != "" && own != *state.top {
			set, _ := setString(body, *state.top, "output_config", "effort")
			if _, array, items, ok = messageSpans(set); !ok {
				return body, nil, nil, ""
			}
			body, own = set, *state.top
		}
	}
	// The effort in force: the newest of the agent's own per-message efforts
	// and the marks, else the top-level field.
	inForce, agentAt := own, -1
	for i := len(items) - 1; i >= 0; i-- {
		if role, _ := jsonsplice.StringField(body, items[i], "role"); role == "system" {
			config, _ := jsonsplice.Field(body, items[i], "output_config")
			if agentEffort, found := jsonsplice.StringField(body, config, "effort"); found {
				inForce, agentAt = agentEffort, i
				break
			}
		}
	}
	if len(marks) > 0 && marks[len(marks)-1].at > agentAt {
		inForce = marks[len(marks)-1].effort
	}
	if allowNew && effort != "" && effort != inForce {
		at := len(items)
		for i := len(items) - 1; i >= 0; i-- {
			if role, _ := jsonsplice.StringField(body, items[i], "role"); role == "user" {
				if !carriesToolResult(body, items[i]) {
					at = i
				}
				break
			}
		}
		at = max(at, agentAt+1)
		if len(marks) > 0 {
			at = max(at, marks[len(marks)-1].at)
		}
		marks = append(marks, effortMark{at: at, anchor: anchorAt(body, items, state.salt, at), effort: effort})
		inForce = effort
	}
	switch {
	case main && len(marks) > 0:
		state.marks = marks
		state.marked = pendingMarks{first: anchorAt(body, items, state.salt, 0), last: anchorAt(body, items, state.salt, len(items)), n: len(items)}
	case main:
		state.marks = nil
	case len(marks) > 0:
		state.pending = addPending(state.pending, pendingMarks{first: first, last: anchorAt(body, items, state.salt, len(items)), n: len(items), marks: marks})
	}
	if len(marks) == 0 {
		return body, nil, nil, inForce
	}
	return insertMarks(body, array, items, marks), body, marks, inForce
}

// addPending puts set first among pending, in place of one for the same
// request shape (first message, length, last message), keeping pendingMax.
func addPending(pending []pendingMarks, set pendingMarks) []pendingMarks {
	out := []pendingMarks{set}
	for _, other := range pending {
		if (other.first != set.first || other.n != set.n || other.last != set.last) && len(out) < pendingMax {
			out = append(out, other)
		}
	}
	return out
}

// markBytes is one per-message effort mark, byte-identical every time.
// Efforts reaching here are lower-case letters only (the link checks).
func markBytes(effort string) []byte {
	return []byte(`{"role":"system","content":[],"output_config":{"effort":"` + effort + `"}}`)
}

// insertMarks splices the marks into the messages array; every byte of the
// agent's own messages stays as it was.
func insertMarks(body []byte, array jsonsplice.Span, items []jsonsplice.Span, marks []effortMark) []byte {
	var out bytes.Buffer
	out.Grow(len(body) + len(marks)*80)
	prev := 0
	for _, mark := range marks {
		switch {
		case mark.at < len(items):
			out.Write(body[prev:items[mark.at].Start])
			out.Write(markBytes(mark.effort))
			out.WriteByte(',')
			prev = items[mark.at].Start
		case len(items) > 0:
			out.Write(body[prev:items[len(items)-1].End])
			out.WriteByte(',')
			out.Write(markBytes(mark.effort))
			prev = items[len(items)-1].End
		default: // an empty array: the first mark opens it
			out.Write(body[prev : array.Start+1])
			if prev > array.Start {
				out.WriteByte(',')
			}
			out.Write(markBytes(mark.effort))
			prev = array.Start + 1
		}
	}
	out.Write(body[prev:])
	return out.Bytes()
}

// cacheControlRE matches a cache_control member with its comma: agents and
// the breakpoint planner move cache breakpoints every request, and the cached
// prefix (and so an anchor) does not depend on them.
var cacheControlRE = regexp.MustCompile(`\s*,\s*"cache_control"\s*:\s*\{[^{}]*\}|"cache_control"\s*:\s*\{[^{}]*\}\s*,?`)

// anchorAt hashes the message before position at (the first message for 0),
// salted per session, without its cache_control members and thinking blocks:
// a strip or drop_block heal removes those from history that keeps its marks.
func anchorAt(body []byte, items []jsonsplice.Span, salt [16]byte, at int) [32]byte {
	hash := sha256.New()
	hash.Write(salt[:])
	if at > len(items) || len(items) == 0 {
		return [32]byte(hash.Sum(nil))
	}
	message := items[max(at-1, 0)]
	if at == 0 {
		hash.Write([]byte{0})
	}
	content, _ := jsonsplice.Field(body, message, "content")
	blocks, ok := jsonsplice.Elements(body, content)
	if !ok {
		hash.Write(cacheControlRE.ReplaceAll(body[message.Start:message.End], nil))
		return [32]byte(hash.Sum(nil))
	}
	hash.Write(cacheControlRE.ReplaceAll(body[message.Start:content.Start], nil))
	for _, block := range blocks {
		if kind, _ := jsonsplice.StringField(body, block, "type"); kind != "thinking" && kind != "redacted_thinking" {
			hash.Write(cacheControlRE.ReplaceAll(body[block.Start:block.End], nil))
			hash.Write([]byte{','})
		}
	}
	hash.Write(cacheControlRE.ReplaceAll(body[content.End:message.End], nil))
	return [32]byte(hash.Sum(nil))
}

// objectRoot spans a body that is one JSON object. Unlike jsonsplice.Root it
// does not validate every byte again: a routed body already parsed as JSON
// (the adapter read its model), and only spliced, valid values go into it.
func objectRoot(body []byte) (jsonsplice.Span, bool) {
	start, end := len(body)-len(bytes.TrimLeft(body, " \t\r\n")), len(bytes.TrimRight(body, " \t\r\n"))
	return jsonsplice.Span{Start: start, End: end}, end-start >= 2 && body[start] == '{' && body[end-1] == '}'
}

func messageSpans(body []byte) (root, array jsonsplice.Span, items []jsonsplice.Span, ok bool) {
	if root, ok = objectRoot(body); !ok {
		return
	}
	array, _ = jsonsplice.Field(body, root, "messages")
	items, ok = jsonsplice.Elements(body, array)
	return
}

func topEffort(body []byte, root jsonsplice.Span) string {
	config, _ := jsonsplice.Field(body, root, "output_config")
	effort, _ := jsonsplice.StringField(body, config, "effort")
	return effort
}

func carriesToolResult(body []byte, message jsonsplice.Span) bool {
	content, _ := jsonsplice.Field(body, message, "content")
	blocks, _ := jsonsplice.Elements(body, content)
	for _, block := range blocks {
		if kind, _ := jsonsplice.StringField(body, block, "type"); kind == "tool_result" {
			return true
		}
	}
	return false
}

// setString sets the string at an object path, making the objects on the way
// (a null or non-object on the way is replaced). changed is false when the
// body is not a JSON object or already holds the value.
func setString(body []byte, value string, path ...string) ([]byte, bool) {
	object, ok := objectRoot(body)
	if !ok {
		return body, false
	}
	encoded, _ := json.Marshal(value)
	nested := func(from int) []byte { // the value wrapped in path[from:]
		out := encoded
		for i := len(path) - 1; i >= from; i-- {
			name, _ := json.Marshal(path[i])
			out = []byte("{" + string(name) + ":" + string(out) + "}")
		}
		return out
	}
	for i, name := range path {
		span, found := jsonsplice.Field(body, object, name)
		var out []byte
		var err error
		switch {
		case !found:
			out, err = jsonsplice.AppendObjectFields(body, object, jsonsplice.FieldInsertion{Name: name, Value: nested(i + 1)})
		case i == len(path)-1:
			if current, isString := jsonsplice.String(body, span); isString && current == value {
				return body, false
			}
			out, err = jsonsplice.ReplaceRaw(body, span, encoded)
		case body[span.Start] != '{':
			out, err = jsonsplice.ReplaceRaw(body, span, nested(i+1))
		default:
			object = span
			continue
		}
		if err != nil {
			return body, false
		}
		return out, true
	}
	return body, false
}

// effortInForce is the effort the bytes sent run at: on Anthropic the newest
// per-message mark, else output_config.effort; reasoning.effort on Responses;
// reasoning_effort on chat. "" when none.
func effortInForce(endpoint string, body []byte) string {
	root, ok := objectRoot(body)
	if !ok {
		return ""
	}
	switch {
	case strings.HasSuffix(endpoint, "/responses"):
		reasoning, _ := jsonsplice.Field(body, root, "reasoning")
		effort, _ := jsonsplice.StringField(body, reasoning, "effort")
		return effort
	case strings.HasSuffix(endpoint, "/chat/completions"):
		effort, _ := jsonsplice.StringField(body, root, "reasoning_effort")
		return effort
	}
	list, _ := jsonsplice.Field(body, root, "messages")
	items, _ := jsonsplice.Elements(body, list)
	for i := len(items) - 1; i >= 0; i-- {
		if role, _ := jsonsplice.StringField(body, items[i], "role"); role == "system" {
			config, _ := jsonsplice.Field(body, items[i], "output_config")
			if effort, ok := jsonsplice.StringField(body, config, "effort"); ok {
				return effort
			}
		}
	}
	return topEffort(body, root)
}

// withoutBrotli drops br from a routed request's accept-encoding: the heal
// and the served model read the provider's answer decoded, and there is no
// brotli decoder here. The agent offered the others too, or gets identity.
func withoutBrotli(header http.Header) {
	offered := strings.Split(header.Get("accept-encoding"), ",")
	kept := offered[:0]
	for _, coding := range offered {
		name, _, _ := strings.Cut(coding, ";")
		if name = strings.TrimSpace(name); name != "" && !strings.EqualFold(name, "br") {
			kept = append(kept, strings.TrimSpace(coding))
		}
	}
	if len(kept) == 0 {
		header.Del("accept-encoding")
		return
	}
	header.Set("accept-encoding", strings.Join(kept, ", "))
}

// The per-message beta and the betas that already carry it, and the beta
// that lets a request ask for unbound thinking to be dropped.
const (
	perMessageBeta = "mid-conversation-output-config-2026-07-01"
	bindingBeta    = "thinking-binding-controls-2026-08-01"
)

var perMessageBetas = []string{perMessageBeta, "per-turn-control-2026-07-01", "mid-conversation-effort-2026-08-01"}

// withBeta returns header with beta appended to anthropic-beta, unless it or
// a beta that carries it is already there.
func withBeta(header http.Header, beta string, carriers ...string) http.Header {
	betas := strings.Join(header.Values("anthropic-beta"), ",")
	for _, sent := range strings.Split(betas, ",") {
		if sent = strings.TrimSpace(sent); sent == beta || slices.Contains(carriers, sent) {
			return header
		}
	}
	out := header.Clone()
	if strings.TrimSpace(betas) == "" {
		out.Set("anthropic-beta", beta)
	} else {
		out.Set("anthropic-beta", betas+","+beta)
	}
	return out
}

func withPerMessageBeta(header http.Header) http.Header {
	return withBeta(header, perMessageBeta, perMessageBetas...)
}

// withDropBlock asks the provider to drop thinking blocks whose prefix changed
// instead of refusing the request: thinking.block_binding.
// prefix_mismatch_behavior "drop_block" (with the bindingBeta header), which
// Anthropic documents for adaptive and enabled thinking and refuses with
// Sonnet 5.5's between_tools; once used, a session sends it on every later
// request (platform.claude.com build-with-claude/preserved-thinking and
// thinking-troubleshooting, read 2026-10-06). ok is false where the option
// does not apply: no thinking field, another type, or a block_binding already
// there (the agent's own choice, or one already asked for).
func withDropBlock(body []byte) ([]byte, bool) {
	root, ok := objectRoot(body)
	if !ok {
		return body, false
	}
	thinking, _ := jsonsplice.Field(body, root, "thinking")
	if kind, _ := jsonsplice.StringField(body, thinking, "type"); kind != "adaptive" && kind != "enabled" || blockBinding(body) {
		return body, false
	}
	return setString(body, "drop_block", "thinking", "block_binding", "prefix_mismatch_behavior")
}

// blockBinding reports a body that sets thinking.block_binding.
func blockBinding(body []byte) bool {
	root, ok := objectRoot(body)
	if !ok {
		return false
	}
	thinking, _ := jsonsplice.Field(body, root, "thinking")
	_, found := jsonsplice.Field(body, thinking, "block_binding")
	return found
}

// The provider 400s the route stage answers, matched on Anthropic's documented
// wording (platform.claude.com build-with-claude/thinking-troubleshooting and
// /effort, read 2026-10-06):
//   - a replayed thinking block whose prefix changed: "messages.{i}.content.{j}:
//     Invalid `signature` in `thinking` block. The block is bound to a different
//     conversation. …" (drop_block applies), and a tampered or undecryptable
//     signature: the same first sentence alone (drop_block does not apply;
//     preserved-thinking, read 2026-10-06)
//   - per-message effort refused: "output_config.effort requires a model that
//     supports per-turn effort; this model does not", and with between_tools
//     thinking "messages.N: output_config.effort 'low' differs from the 'high'
//     in effect before it; effort cannot change when thinking is disabled …"
//
// On a marked request a 400 naming output_config.effort, a message's
// output_config, per-turn effort or the beta counts as the marks' fault (a
// provider that rejects the field outright answers "messages.N.output_config:
// Extra inputs are not permitted").
var (
	bindingRE     = regexp.MustCompile("(?i)bound to a different conversation|invalid `signature` in `thinking` block")
	prefixRE      = regexp.MustCompile(`(?i)bound to a different conversation`)
	bindingPathRE = regexp.MustCompile(`messages\.(\d+)\.content\.\d+:`)
	refusalRE     = regexp.MustCompile(`(?i)supports per-turn effort|effort cannot change`)
	markErrRE     = regexp.MustCompile(`(?i)output_config\.effort|messages\.\d+\.output_config|per-turn|mid-conversation`)
)

// healKind is the retry a provider 400 earned.
type healKind int

const (
	healNone      healKind = iota
	healMarks              // refused marks: top-level effort only
	healDropBlock          // broken binding: ask the provider to drop unbound thinking
	healStrip              // broken binding where that is refused: strip thinking from the failing message on
)

// routeHeal reads a provider 400 (decoded when compressed) and returns the one
// retry it earns. A broken thinking binding (run.heal: the route stage was on
// or the session carries its changes, and the agent set no block_binding),
// sent to the asked model, retries with drop_block when the prefix changed and
// the request's thinking takes it, else without the thinking blocks from the
// failing message on, with its marks either way; from is that message among
// the agent's own. A request the route stage sent to another model is left to
// the original-bytes retry on the asked one.
// Refused marks retry with top-level effort only (once served the model is
// latched as refused). Refusal wording on a request without marks latches the
// model at once. The 400's bytes are put back.
func (s *Server) routeHeal(run *routeRun, resp *http.Response, sent []byte, model string, sameModel bool) (retry []byte, kind healKind, from int) {
	head, ok := peekError(resp)
	if !ok {
		return nil, healNone, 0
	}
	switch {
	case bindingRE.Match(head):
		if !sameModel || !run.heal {
			return nil, healNone, 0
		}
		if out, ok := withDropBlock(sent); ok && prefixRE.Match(head) && !run.noDropBlock {
			return out, healDropBlock, 0
		}
		at := 0
		if match := bindingPathRE.FindSubmatch(head); match != nil {
			at, _ = strconv.Atoi(string(match[1]))
		}
		if out, dropped := dropThinking(sent, at, math.MaxInt); dropped {
			from = at // the sent index less the marks before it is the agent's own
			for j, mark := range run.marks {
				if mark.at+j < at {
					from--
				}
			}
			return out, healStrip, max(from, 0)
		}
	case run.marked && (refusalRE.Match(head) || markErrRE.Match(head)):
		top, _ := setString(run.unmarked, run.effort, "output_config", "effort")
		if run.dropBlocks {
			top, _ = withDropBlock(top)
		}
		return top, healMarks, 0
	case refusalRE.Match(head):
		s.routes.latch(run.key, model)
	}
	return nil, healNone, 0
}

// peekError reads the head of a provider error, decoded when compressed, and
// puts its bytes back.
func peekError(resp *http.Response) ([]byte, bool) {
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	resp.Body = struct {
		io.Reader
		io.Closer
	}{io.MultiReader(bytes.NewReader(raw), resp.Body), resp.Body}
	return providers.DecodeBody(raw, resp.Header.Get("Content-Encoding"), 1<<20)
}

// healRefused reports a heal retry the provider refused for the same reason:
// a 400 that again names the thinking binding, block_binding or its beta. A
// rate limit, an overload or any other 400 says nothing about the heal.
func healRefused(resp *http.Response) bool {
	if resp.StatusCode != http.StatusBadRequest {
		return false
	}
	head, ok := peekError(resp)
	return ok && (bindingRE.Match(head) || blockBindingRE.Match(head))
}

var blockBindingRE = regexp.MustCompile(`(?i)block_binding|thinking-binding-controls`)

// dropThinking removes every thinking and redacted_thinking block from the
// assistant turns among messages from up to (not including) to, and leaves
// each turn's other blocks in place.
func dropThinking(body []byte, from, to int) ([]byte, bool) {
	_, _, items, ok := messageSpans(body)
	if !ok {
		return body, false
	}
	dropped := false
	for i := min(to, len(items)) - 1; i >= max(from, 0); i-- { // last first: earlier spans stay valid
		if role, _ := jsonsplice.StringField(body, items[i], "role"); role != "assistant" {
			continue
		}
		content, _ := jsonsplice.Field(body, items[i], "content")
		blocks, _ := jsonsplice.Elements(body, content)
		kept := make([][]byte, 0, len(blocks))
		for _, block := range blocks {
			if kind, _ := jsonsplice.StringField(body, block, "type"); kind != "thinking" && kind != "redacted_thinking" {
				kept = append(kept, body[block.Start:block.End])
			}
		}
		if len(kept) == len(blocks) {
			continue
		}
		out, err := jsonsplice.ReplaceRaw(body, content, append(append([]byte("["), bytes.Join(kept, []byte(","))...), ']'))
		if err != nil {
			return body, false
		}
		body, dropped = out, true
	}
	return body, dropped
}

// servedModel reads the model the provider's answer names: the first "model"
// string in its first 256 KiB (a JSON body names it before the output, a
// stream in its first event), decoded when compressed. An answer cut there
// mid-compression names none.
type servedModel struct{ head []byte }

var servedModelRE = regexp.MustCompile(`"model"\s*:\s*"([^"\\]{1,128})"`)

func (m *servedModel) Write(p []byte) (int, error) {
	if room := 256<<10 - len(m.head); room > 0 {
		m.head = append(m.head, p[:min(len(p), room)]...)
	}
	return len(p), nil
}

func (m *servedModel) name(contentEncoding string) string {
	head, _ := providers.DecodeBody(m.head, contentEncoding, 1<<20)
	if match := servedModelRE.FindSubmatch(head); match != nil {
		return string(match[1])
	}
	return ""
}

// shownModel puts the model the agent asked for back into what it reads when
// the route stage moved the request: Claude Code drops its thinking when the
// answer names another model. Only the client's copy changes; usage, stats and
// last read the provider's bytes. A JSON answer changes its top-level "model"
// (setModel); a stream changes every "model":"<sent>" pair, which is where
// Anthropic's message_start, Responses' response.* events and every chat chunk
// name it (tool arguments stream as escaped strings and never match). A
// dated id of the model sent ("<sent>-2026-09-01") counts as it.
type shownModel struct {
	src      io.Reader
	sent     string
	asked    []byte
	ready    []byte // rewritten, not yet handed out
	tail     []byte // may still become a pair: held for the next read
	scratch  []byte
	err      error
	finished bool
}

// shownModelRE matches one "model":"…" pair; whitespace is bounded so a pair
// split across reads is never held for long.
var shownModelRE = regexp.MustCompile(`"model"[ \t\r\n]{0,8}:[ \t\r\n]{0,8}"([^"\\]{1,128})"`)

const shownModelPairMax = 7 + 8 + 1 + 8 + 1 + 128 + 1

func newShownModel(src io.Reader, sent, asked string) *shownModel {
	return &shownModel{src: src, sent: sent, asked: []byte(asked), scratch: make([]byte, 32<<10)}
}

func (m *shownModel) Read(p []byte) (int, error) {
	for len(m.ready) == 0 && !m.finished {
		n, err := m.src.Read(m.scratch)
		if n > 0 {
			chunk := m.rewrite(append(m.tail, m.scratch[:n]...))
			held := partialPair(chunk)
			m.ready = append(m.ready, chunk[:len(chunk)-held]...)
			m.tail = append([]byte(nil), chunk[len(chunk)-held:]...)
		}
		if err != nil {
			m.ready, m.tail, m.err, m.finished = append(m.ready, m.tail...), nil, err, true
		}
	}
	n := copy(p, m.ready)
	m.ready = m.ready[n:]
	if len(m.ready) == 0 && m.finished {
		return n, m.err
	}
	return n, nil
}

// rewrite swaps the value of every complete pair naming the model sent.
func (m *shownModel) rewrite(data []byte) []byte {
	matches := shownModelRE.FindAllSubmatchIndex(data, -1)
	if matches == nil {
		return data
	}
	var out []byte
	prev := 0
	for _, match := range matches {
		value := string(data[match[2]:match[3]])
		if value != m.sent && !strings.HasPrefix(value, m.sent+"-") {
			continue
		}
		out = append(append(out, data[prev:match[2]]...), m.asked...)
		prev = match[3]
	}
	if out == nil {
		return data
	}
	return append(out, data[prev:]...)
}

// partialPair is how many bytes at the end of data could still become a pair:
// from the earliest quote in the last shownModelPairMax bytes that starts an
// unfinished one. A finished event never ends in one, so streams stay live.
func partialPair(data []byte) int {
	for i := max(len(data)-shownModelPairMax, 0); i < len(data); i++ {
		if data[i] == '"' && pairPrefix(data[i:]) {
			return len(data) - i
		}
	}
	return 0
}

// pairPrefix reports whether tail is the unfinished start of a "model":"…" pair.
func pairPrefix(tail []byte) bool {
	const key = `"model"`
	i := 0
	for ; i < len(tail) && i < len(key); i++ {
		if tail[i] != key[i] {
			return false
		}
	}
	if i == len(tail) {
		return true
	}
	space := func() bool {
		start := i
		for i < len(tail) && strings.IndexByte(" \t\r\n", tail[i]) >= 0 {
			i++
		}
		return i-start <= 8
	}
	if !space() || i < len(tail) && tail[i] != ':' {
		return false
	}
	if i == len(tail) {
		return true
	}
	i++
	if !space() || i < len(tail) && tail[i] != '"' {
		return false
	}
	if i == len(tail) {
		return true
	}
	value := tail[i+1:]
	return len(value) <= 128 && bytes.IndexAny(value, `"\`) < 0
}
