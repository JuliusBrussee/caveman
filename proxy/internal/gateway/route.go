package gateway

import (
	"bytes"
	"container/list"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/JuliusBrussee/caveman/proxy/providers/jsonsplice"
)

// The route stage (ADR 0083 §4: parse, ask, compress, route). The ask starts
// before compression so the Cloud round trip overlaps it; the answer is applied
// to the bytes compression produced. Same provider only in this cut: an answer
// names another model of the provider the agent already talks to, swapped into
// the body's top-level "model", and the effort to run at. Every failure keeps
// the request as the agent sent it, and the request is never held past the
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
// EffortMode on Anthropic: "message" (a per-message mark) or "top". Outcome is
// the runtime/v1 route outcome: routed, kept, degraded, paused or off.
type RouteAnswer struct {
	Model      string
	Effort     string
	EffortMode string
	Outcome    string
	Reason     string
	DecisionID string
	// Reject, when set, tells the link the provider refused the routed model,
	// so the rest of this ask stays on the asked one.
	Reject func()
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
}

// routeRun carries one request's route-stage facts from the ask to the response.
type routeRun struct {
	key, parent string
	labels      map[string]string
	perRequest  bool   // the agent labels it compaction or auxiliary
	compacted   bool   // a compaction, or a request after one
	off         bool   // the route stage was off for it: nothing applied, nothing healed
	marked      bool   // the body sent carries the session's per-message marks
	unmarked    []byte // that body without them, for the heal
	// effort is the effort in force in the body sent, "" when the route stage
	// did not set it (then it is read back from the bytes).
	effort string
}

// newRouteRun reads the labels and the session keys. The session is the
// caller's x-cave-session, else the agent's own session header; a Claude Code
// child (x-claude-code-agent-id) is that plus its agent id.
func newRouteRun(h http.Header, sessionID string) *routeRun {
	run := &routeRun{labels: map[string]string{}}
	for _, name := range routeLabelNames {
		if value := h.Get(name); value != "" {
			run.labels[name] = cutBytes(value, 256)
		}
	}
	for _, name := range []string{"x-claude-code-session-id", "session-id", "session_id"} {
		if sessionID == "" {
			sessionID = cutBytes(h.Get(name), 256)
		}
	}
	if run.key = sessionID; sessionID != "" {
		if agent := run.labels["x-claude-code-agent-id"]; agent != "" {
			run.key, run.parent = sessionID+"#"+agent, sessionID
			if parent := run.labels["x-claude-code-parent-agent-id"]; parent != "" {
				run.parent += "#" + parent
			}
		} else if parent := run.labels["x-parent-session-id"]; parent != "" {
			run.parent = parent
		}
	}
	class := strings.ToLower(run.labels["x-claude-code-request-class"])
	compaction := run.labels["x-claude-code-compaction"] != "" || class == "compaction"
	run.perRequest = compaction || class == "auxiliary"
	run.compacted = compaction || run.labels["x-claude-code-context-compacted"] != ""
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
// previous upstream request's facts and the per-message effort marks. top is
// the top-level effort, fixed at the session's first per-message request (nil
// until then); salt keys the marks' anchors.
type routeSession struct {
	key           string
	last          *RouteLast
	lastAt        time.Time
	perMessageOff bool
	salt          [16]byte
	top           *string
	marks         []effortMark
}

// effortMark is one per-message effort mark: it goes before message at of the
// agent's own messages, where anchor hashes the message before it.
type effortMark struct {
	at     int
	anchor [32]byte
	effort string
}

// ponytail: one lock for every session, held while marks are spliced into a
// body; per-session locks if concurrent sessions ever contend on it.
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
		return nil, session != nil && session.perMessageOff
	}
	last := *session.last
	last.AgeS = int(min(max(now.Sub(session.lastAt), 0)/time.Second, 1_000_000_000))
	return &last, session.perMessageOff
}

func (rs *routeSessions) served(key string, last RouteLast, at time.Time) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if session := rs.get(key, true); session != nil {
		session.last, session.lastAt = &last, at
	}
}

// latch turns per-message effort off for key's session, for good.
func (rs *routeSessions) latch(key string) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if session := rs.get(key, true); session != nil {
		session.perMessageOff, session.marks = true, nil
	}
}

// applyEffort applies the answer's effort to body (the bytes compression
// produced, model already set) and returns what to send. Responses and chat
// take their effort field. Anthropic takes it top-level, or as a per-message
// mark that keeps the cached prefix: a session's marks come back on every
// request it asks about, at the same places and byte-identical, so its history
// does not change under the agent, even when this answer names no effort.
// Only a top-level answer or a route stage that is off leaves them out.
func (s *Server) applyEffort(run *routeRun, provider, endpoint string, body []byte, answer RouteAnswer) []byte {
	if answer.Outcome == "off" {
		run.off = true
		return body
	}
	switch {
	case strings.HasSuffix(endpoint, "/responses"):
		if answer.Effort != "" {
			body, _ = setString(body, answer.Effort, "reasoning", "effort")
			run.effort = answer.Effort
		}
		return body
	case strings.HasSuffix(endpoint, "/chat/completions"):
		if answer.Effort != "" {
			body, _ = setString(body, answer.Effort, "reasoning_effort")
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
	s.routes.mu.Lock()
	defer s.routes.mu.Unlock()
	session := s.routes.get(run.key, true)
	if mode == "top" {
		body, _ = setString(body, answer.Effort, "output_config", "effort")
		run.effort = answer.Effort
		if session != nil && !run.perRequest { // the cache restarts here: later marks start from this level
			top := answer.Effort
			session.top, session.marks = &top, nil
		}
		return body
	}
	// Per-message effort needs a session to replay its marks in. A session
	// starts it on a turn's message answer, or a child on its parent's marks.
	if session == nil || session.perMessageOff {
		return body
	}
	var parent *routeSession
	if session.top == nil {
		if parent = s.routes.get(run.parent, false); parent != nil && parent.top == nil {
			parent = nil
		}
		if parent == nil && (mode != "message" || run.perRequest) {
			return body
		}
	}
	effort := ""
	if mode == "message" {
		effort = answer.Effort
	}
	out, unmarked, inForce := perMessage(body, session, parent, effort, !run.perRequest)
	run.marked, run.unmarked, run.effort = unmarked != nil, unmarked, inForce
	return out
}

// perMessage re-inserts the session's marks into an Anthropic Messages body
// and, when allowed, adds one for effort. It returns the body to send and that
// body without marks (nil when it carries none).
//
// The session's top-level effort is fixed at its first per-message request:
// the routed effort on a fresh conversation (no assistant turn yet), else the
// request's own. A forked child resending its parent's history inherits the
// parent's marks instead. A new mark goes in only when the effort differs from
// the one in force (the newest per-message effort, the agent's own or a mark,
// else top-level), just before the last user turn, or at the end when that turn
// carries a tool result (never between a tool_use and its tool_result), and
// never before an earlier mark or per-message effort. A mark whose anchor no
// longer matches (compaction rewrote history) is dropped with every later one.
func perMessage(body []byte, session, parent *routeSession, effort string, allowNew bool) (out, unmarked []byte, inForce string) {
	root, list, items, ok := messageSpans(body)
	if !ok {
		return body, nil, ""
	}
	own := topEffort(body, root)
	if session.top == nil {
		fresh := true
		for _, item := range items {
			if role, _ := jsonsplice.StringField(body, item, "role"); role == "assistant" {
				fresh = false
				break
			}
		}
		if parent != nil && parent.top != nil && !fresh {
			session.salt, session.top, session.marks = parent.salt, parent.top, append([]effortMark(nil), parent.marks...)
		} else {
			top := own
			if fresh && effort != "" {
				top = effort
			}
			session.top = &top
		}
	}
	if *session.top != "" && own != *session.top {
		set, _ := setString(body, *session.top, "output_config", "effort")
		if _, list, items, ok = messageSpans(set); !ok {
			return body, nil, ""
		}
		body, own = set, *session.top
	}
	marks := session.marks[:0:0]
	for _, mark := range session.marks {
		if mark.at > len(items) || anchorAt(body, items, session.salt, mark.at) != mark.anchor {
			break
		}
		marks = append(marks, mark)
	}
	// The effort in force: the newest of the agent's own per-message efforts
	// and the session's marks, else the top-level field.
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
		marks = append(marks, effortMark{at: at, anchor: anchorAt(body, items, session.salt, at), effort: effort})
		inForce = effort
	}
	session.marks = marks
	if len(marks) == 0 {
		return body, nil, inForce
	}
	return insertMarks(body, list, items, marks), body, inForce
}

// markBytes is one per-message effort mark, byte-identical every time.
// Efforts reaching here are lower-case letters only (the link checks).
func markBytes(effort string) []byte {
	return []byte(`{"role":"system","content":[],"output_config":{"effort":"` + effort + `"}}`)
}

// insertMarks splices the marks into the messages array; every byte of the
// agent's own messages stays as it was.
func insertMarks(body []byte, list jsonsplice.Span, items []jsonsplice.Span, marks []effortMark) []byte {
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
			out.Write(body[prev : list.Start+1])
			if prev > list.Start {
				out.WriteByte(',')
			}
			out.Write(markBytes(mark.effort))
			prev = list.Start + 1
		}
	}
	out.Write(body[prev:])
	return out.Bytes()
}

// anchorAt hashes the message before position at, salted per session.
func anchorAt(body []byte, items []jsonsplice.Span, salt [16]byte, at int) [32]byte {
	hash := sha256.New()
	hash.Write(salt[:])
	if at > 0 {
		hash.Write(body[items[at-1].Start:items[at-1].End])
	}
	return [32]byte(hash.Sum(nil))
}

// objectRoot spans a body that is one JSON object. Unlike jsonsplice.Root it
// does not validate every byte again: a routed body already parsed as JSON
// (the adapter read its model), and only spliced, valid values go into it.
func objectRoot(body []byte) (jsonsplice.Span, bool) {
	start, end := len(body)-len(bytes.TrimLeft(body, " \t\r\n")), len(bytes.TrimRight(body, " \t\r\n"))
	return jsonsplice.Span{Start: start, End: end}, end-start >= 2 && body[start] == '{' && body[end-1] == '}'
}

func messageSpans(body []byte) (root, list jsonsplice.Span, items []jsonsplice.Span, ok bool) {
	if root, ok = objectRoot(body); !ok {
		return
	}
	list, _ = jsonsplice.Field(body, root, "messages")
	items, ok = jsonsplice.Elements(body, list)
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

// The per-message beta and the betas that already carry it.
const perMessageBeta = "mid-conversation-output-config-2026-07-01"

var perMessageBetas = []string{perMessageBeta, "per-turn-control-2026-07-01", "mid-conversation-effort-2026-08-01"}

// withPerMessageBeta returns header with the per-message beta appended to
// anthropic-beta, unless a beta that carries it is already there.
func withPerMessageBeta(header http.Header) http.Header {
	betas := strings.Join(header.Values("anthropic-beta"), ",")
	for _, beta := range strings.Split(betas, ",") {
		for _, known := range perMessageBetas {
			if strings.TrimSpace(beta) == known {
				return header
			}
		}
	}
	out := header.Clone()
	if strings.TrimSpace(betas) == "" {
		out.Set("anthropic-beta", perMessageBeta)
	} else {
		out.Set("anthropic-beta", betas+","+perMessageBeta)
	}
	return out
}

// The provider 400s the route stage answers, matched on Anthropic's documented
// wording (platform.claude.com build-with-claude/thinking-troubleshooting and
// /effort, read 2026-10-06):
//   - a replayed thinking block whose prefix changed: "messages.{i}.content.{j}:
//     Invalid `signature` in `thinking` block. The block is bound to a different
//     conversation. …"
//   - per-message effort refused: "output_config.effort requires a model that
//     supports per-turn effort; this model does not", and with between_tools
//     thinking "messages.N: output_config.effort 'low' differs from the 'high'
//     in effect before it; effort cannot change when thinking is disabled …"
//
// On a marked request any 400 naming output_config, per-turn effort or the
// beta counts as the marks' fault (a provider that rejects the field outright
// answers "messages.N.output_config: Extra inputs are not permitted").
var (
	bindingRE = regexp.MustCompile("(?i)bound to a different conversation|invalid `signature` in `thinking` block")
	refusalRE = regexp.MustCompile(`(?i)supports per-turn effort|effort cannot change`)
	markErrRE = regexp.MustCompile(`(?i)output_config|per-turn|mid-conversation`)
)

// routeHeal reads a provider 400 and returns the one retry it earns, or nil:
// without thinking blocks and marks when the thinking binding broke, or with
// top-level effort only when the marks were refused (marks reports that one;
// once it is served the session's latch goes on). Refusal wording on a request
// without marks latches the session at once. The 400's bytes are put back.
func (s *Server) routeHeal(run *routeRun, resp *http.Response, sent []byte) (retry []byte, marks bool) {
	head, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	resp.Body = struct {
		io.Reader
		io.Closer
	}{io.MultiReader(bytes.NewReader(head), resp.Body), resp.Body}
	switch {
	case bindingRE.Match(head):
		base := sent
		if run.marked {
			base = run.unmarked
		}
		if stripped, dropped := dropThinking(base); dropped || run.marked {
			return stripped, false
		}
	case run.marked && (refusalRE.Match(head) || markErrRE.Match(head)):
		top, _ := setString(run.unmarked, run.effort, "output_config", "effort")
		return top, true
	case refusalRE.Match(head):
		s.routes.latch(run.key)
	}
	return nil, false
}

// dropThinking removes every thinking and redacted_thinking block from the
// assistant turns and leaves each turn's other blocks in place.
func dropThinking(body []byte) ([]byte, bool) {
	_, _, items, ok := messageSpans(body)
	if !ok {
		return body, false
	}
	dropped := false
	for i := len(items) - 1; i >= 0; i-- { // last first: earlier spans stay valid
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
// stream in its first event). An encoded answer names none.
type servedModel struct{ head []byte }

var servedModelRE = regexp.MustCompile(`"model"\s*:\s*"([^"\\]{1,128})"`)

func (m *servedModel) Write(p []byte) (int, error) {
	if room := 256<<10 - len(m.head); room > 0 {
		m.head = append(m.head, p[:min(len(p), room)]...)
	}
	return len(p), nil
}

func (m *servedModel) name() string {
	if match := servedModelRE.FindSubmatch(m.head); match != nil {
		return string(match[1])
	}
	return ""
}
