package translate

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sync"
	"time"

	"github.com/JuliusBrussee/caveman/proxy/providers/jsonsplice"
)

// The three wire grammars.
const (
	Messages  = "messages"  // Anthropic Messages
	Chat      = "chat"      // OpenAI chat completions
	Responses = "responses" // OpenAI Responses
)

// Supported: a caller body in grammar from can run on an upstream wire to:
// every pair of the three grammars.
func Supported(from, to string) bool {
	return isGrammar(from) && isGrammar(to)
}

func isGrammar(grammar string) bool {
	return grammar == Messages || grammar == Chat || grammar == Responses
}

// Options fit a request to one upstream.
type Options struct {
	Model           string   // the upstream's model id, written into the body
	Shown           string   // the model name the caller reads in the answer (the model it asked for); "" = Model
	Effort          string   // "", none, minimal, low, medium, high, xhigh, max: applied in the upstream's dialect ("" leaves the request's own, translated)
	Dialect         string   // chat upstream effort dialect: "openai_chat" (default), "openrouter", "deepseek", "toggle", "qwen", "none"
	Route           string   // upstream provider id ("anthropic" = Anthropic's own API): namespaces thinking signatures
	Replay          bool     // the upstream wants its own reasoning replayed as reasoning_content (chat wire)
	MaxTokensField  string   // chat: rename max_tokens to this (e.g. "max_completion_tokens"); "" keeps max_tokens
	DropParams      []string // top-level fields the upstream refuses
	MaxOutputTokens int      // Responses->Messages: max_tokens when the caller set none (0 = 32000)
	ChatGPTLogin    bool     // Responses upstream is the Sign-in-with-ChatGPT preview: chatgptLoginBody fitting (always streamed upstream)

	estimateFrom []byte // a streamed Messages caller's request: message_start's input_tokens is estimated from it (set by Request)
}

func (o Options) shown() string {
	if o.Shown != "" {
		return o.Shown
	}
	return o.Model
}

func (o Options) dialect() string {
	if o.Dialect == "" {
		return dialectOpenAIChat
	}
	return o.Dialect
}

// route is the Messages host the body goes to; "" is Anthropic's own API.
func (o Options) route() string {
	if o.Route == "" {
		return anthropicRoute
	}
	return o.Route
}

// chatSignature signs the thinking a chat route's reasoning becomes for a
// Messages caller: "caveman:" (so Anthropic never sees it), naming the route
// and model so only that pair gets it back as reasoning_content.
func (o Options) chatSignature() string { return signaturePrefix + "v1:" + o.Route + ":" + o.Model }

// replay is the signature whose thinking a chat route takes back as
// reasoning_content, "" for a route that replays none.
func (o Options) replay() string {
	if o.Replay {
		return o.chatSignature()
	}
	return ""
}

// Usage is what one answer reported. InputTokens is the total input,
// including cache reads and writes.
type Usage struct {
	InputTokens, OutputTokens, CacheReadTokens, CacheWriteTokens int
}

// Reply turns one upstream answer back into the caller's grammar.
type Reply struct {
	from, to       string
	opts           Options
	stream         bool // the caller asked for a stream
	upstreamStream bool // the upstream was asked for one
	tools          toolBridge
	upstreamID     string // the host's own id for the answer (UpstreamID)
	includeUsage   bool   // a streamed chat caller asked for the usage chunk
}

// UpstreamID is the host's own id for the answer it gave (OpenRouter's gen-…,
// a resp_… or msg_…), read from the start of its body once Serve returns; ""
// when it named none. The translated answer carries ids of its own, so this
// is what looks the call up on the host (its cost, say).
func (r *Reply) UpstreamID() string { return r.upstreamID }

// headBuffer keeps the first bytes of an upstream body (the reader goroutine
// writes while Serve may already be reading: hence the lock).
type headBuffer struct {
	mu  sync.Mutex
	buf []byte
}

func (h *headBuffer) Write(p []byte) (int, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if room := 8<<10 - len(h.buf); room > 0 {
		h.buf = append(h.buf, p[:min(room, len(p))]...)
	}
	return len(p), nil
}

var answerIDRE = regexp.MustCompile(`"id"\s*:\s*"([!-~]{1,200}?)"`)

// firstID is the first "id" in the head: a chat chunk's or answer's own, a
// Responses response.created's response, a Messages message_start's message.
func (h *headBuffer) firstID() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	if match := answerIDRE.FindSubmatch(h.buf); match != nil {
		return string(match[1])
	}
	return ""
}

// Stream reports whether the caller asked for a streamed answer.
func (r *Reply) Stream() bool { return r.stream }

// namespaces: the answer comes from a Messages host other than Anthropic's
// own API, so its thinking signatures are rewritten to "caveman:<route>:…".
func (r *Reply) namespaces() bool { return r.to == Messages && r.opts.route() != anthropicRoute }

// Request renders the caller's body (grammar from) for upstream wire to.
// Same-grammar requests are fitted (model, effort, params; Messages bodies get
// foreign thinking stripped/restored per Route, Anthropic tool ids fixed when
// Route is "anthropic"; Responses bodies to OpenAI get reasoning items OpenAI
// cannot verify removed). It returns the upstream body and a Reply that turns
// the upstream's answer back into the caller's grammar.
func Request(from, to string, body []byte, opts Options) ([]byte, *Reply, error) {
	if !Supported(from, to) {
		return nil, nil, fmt.Errorf("translate: a %s caller cannot run on a %s upstream", from, to)
	}
	fields, err := topFields(body)
	if err != nil {
		return nil, nil, fmt.Errorf("translate: %s body: %w", from, err)
	}
	stream := string(fields["stream"]) == "true"
	reply := &Reply{from: from, to: to, opts: opts, stream: stream, upstreamStream: stream}
	if from == Messages && to != Messages && stream {
		reply.opts.estimateFrom = body // counted when message_start goes, not before the send
	}
	if options, ok := parseObj(fields["stream_options"]); ok && from == Chat {
		reply.includeUsage = string(options.get("include_usage")) == "true"
	}
	switch {
	case from == Messages && to == Messages:
		messagesBody(fields, opts)
	case from == Messages && to == Chat:
		fields, err = messagesChatBody(fields, opts)
	case from == Messages && to == Responses:
		fields, err = messagesResponsesBody(fields, opts)
		reply.upstreamStream = true
	case from == Chat && to == Chat:
		stripChatEnvelopes(fields)
		fitChat(fields, opts, stream, chatCallerEffort(fields, opts))
	case from == Chat && to == Messages:
		fields, err = chatMessagesBody(fields, opts)
		reply.upstreamStream = true
	case from == Chat:
		fields, err = chatResponsesBody(fields, opts)
		reply.upstreamStream = true
	case to == Messages:
		fields, reply.tools, err = responsesMessagesBody(fields, opts)
		reply.upstreamStream = true
	case to == Chat:
		fields, reply.tools, err = responsesChatBody(fields, opts)
		reply.upstreamStream = true
	default:
		if err = responsesNativeBody(fields, opts.Model, opts.Effort, opts.Route); err == nil && opts.ChatGPTLogin {
			chatgptLoginBody(fields)
			reply.upstreamStream = true
		}
		dropParams(fields, opts)
	}
	if err != nil {
		return nil, nil, err
	}
	return rawObject(fields), reply, nil
}

// messagesBody fits the caller's Messages body to a Messages host: the
// upstream's model id, the thinking this host may see, the effort, the
// route's parameter rules, and (on Anthropic's own API) tool ids Anthropic
// accepts (OpenRouter's Kimi "functions.Bash:0" would fail Claude).
func messagesBody(fields map[string]json.RawMessage, opts Options) {
	fields["model"] = mustJSON(opts.Model)
	stripForeignThinking(fields, opts.route())
	if opts.Effort != "" && opts.Dialect != dialectNone {
		applyNativeEffort(fields, opts.Model, opts.Effort)
	} else if raw, set := fields["thinking"]; set {
		// No effort to apply: the caller's own thinking still has to be one
		// this host's model takes (at the caller's own effort).
		output, _ := parseObj(fields["output_config"])
		if thinking, keep := nativeThinking(raw, fields["max_tokens"], opts.Model, output.str("effort")); keep {
			fields["thinking"] = thinking
		} else {
			delete(fields, "thinking")
		}
	}
	fitRoute(fields, opts, false)
	if opts.route() == anthropicRoute {
		fixToolIDs(fields)
	}
}

// chatCallerEffort is the effort a chat caller's body goes with: Options'
// own, else the caller's when the upstream spells effort otherwise than it
// came ("" leaves the body's fields as they are).
func chatCallerEffort(fields map[string]json.RawMessage, opts Options) string {
	if opts.Effort != "" {
		return opts.Effort
	}
	var effort string
	_ = json.Unmarshal(fields["reasoning_effort"], &effort)
	var reasoning struct {
		Effort string `json:"effort"`
	}
	_, openRouterField := fields["reasoning"]
	if json.Unmarshal(fields["reasoning"], &reasoning) == nil && reasoning.Effort != "" {
		effort = reasoning.Effort
	}
	dialect := opts.dialect()
	if effort == "" || dialect == dialectOpenAIChat && !openRouterField || dialect == dialectOpenRouter && openRouterField {
		return ""
	}
	return effort
}

// fitChat fits a chat body to its upstream: model id, effort in the dialect,
// usage in the stream, and the route's parameter rules.
func fitChat(fields map[string]json.RawMessage, opts Options, stream bool, effort string) {
	if effort != "" {
		applyChatEffort(fields, effort, opts.dialect())
	}
	fields["model"] = mustJSON(opts.Model)
	if opts.dialect() != dialectOpenRouter {
		delete(fields, "reasoning") // OpenRouter's own field; OpenAI refuses it
	}
	if _, set := fields["stream_options"]; stream && !set {
		fields["stream_options"] = json.RawMessage(`{"include_usage":true}`)
	}
	fitRoute(fields, opts, true)
}

// fitRoute applies a route's parameter rules to a chat or Messages body.
func fitRoute(fields map[string]json.RawMessage, opts Options, chat bool) {
	if chat || opts.route() != anthropicRoute {
		// Claude Code's metadata.user_id (its account and device ids; `user`
		// once translated to chat) is for Anthropic's own API only.
		delete(fields, "metadata")
		delete(fields, "user")
	}
	if chat && opts.MaxTokensField != "" {
		// OpenAI's reasoning models refuse max_tokens.
		if value, set := fields["max_tokens"]; set {
			fields[opts.MaxTokensField] = value
			delete(fields, "max_tokens")
		}
	}
	if chat && opts.dialect() == dialectOpenRouter {
		if _, set := fields["usage"]; !set {
			fields["usage"] = json.RawMessage(`{"include":true}`)
		}
		// The chat wire carries no block cache_control, and a conversation is
		// resent whole every turn: one top-level breakpoint is OpenRouter's
		// automatic caching for Claude
		// (https://openrouter.ai/docs/features/prompt-caching).
		if _, set := fields["cache_control"]; !set && claudeID(opts.Model) != "" {
			fields["cache_control"] = json.RawMessage(`{"type":"ephemeral"}`)
		}
	}
	dropParams(fields, opts)
}

func dropParams(fields map[string]json.RawMessage, opts Options) {
	for _, name := range opts.DropParams {
		delete(fields, name)
	}
}

func cmpOr(value, fallback int) int {
	if value > 0 {
		return value
	}
	return fallback
}

// chatgptLoginUnsupported are the Responses fields the Sign in with ChatGPT
// preview refuses (developers.openai.com/siwc/preview-limitations).
var chatgptLoginUnsupported = []string{"background", "conversation", "max_output_tokens", "max_tool_calls", "metadata", "moderation",
	"multi_agent", "prompt", "prompt_cache_retention", "safety_identifier", "temperature", "top_logprobs", "top_p", "truncation", "user",
	"previous_response_id"}

// chatgptLoginBody fits a Responses body to the Sign in with ChatGPT preview:
// store false, stream true (a non-streaming caller gets the answer assembled
// from the stream), the unsupported fields dropped, and plain function and
// custom tools moved to additional_tools (the preview takes them only there
// or inside namespaces). Not verified against the live preview.
func chatgptLoginBody(body map[string]json.RawMessage) {
	for _, field := range chatgptLoginUnsupported {
		delete(body, field)
	}
	body["store"], body["stream"] = json.RawMessage(`false`), json.RawMessage(`true`)
	var tools []map[string]json.RawMessage
	if json.Unmarshal(body["tools"], &tools) != nil || len(tools) == 0 {
		return
	}
	var kept, additional []map[string]json.RawMessage
	for _, tool := range tools {
		var kind string
		_ = json.Unmarshal(tool["type"], &kind)
		if kind == "function" || kind == "custom" {
			additional = append(additional, tool)
		} else {
			kept = append(kept, tool)
		}
	}
	if len(kept) > 0 {
		body["tools"] = mustJSON(kept)
	} else {
		delete(body, "tools")
	}
	if len(additional) > 0 {
		body["additional_tools"] = mustJSON(additional)
	}
}

// --- answers ----------------------------------------------------------------

// Serve writes a 2xx upstream answer to w in the caller's grammar (streamed
// when the upstream streams: flushed per event, keepalive/ping where the
// source does; JSON otherwise), naming Shown as the model, and returns the
// usage it read. A non-nil error means the upstream stream was cut after
// bytes reached w (the caller already got an error event in its grammar).
// Same-grammar answers are relayed (Messages from a non-Anthropic host:
// signatures namespaced), with every "model" value rewritten to Shown.
//
// Nothing reaches w until the answer carries content (a block, a delta, an
// output item, a finished answer): an upstream that answers 2xx and then
// fails, or ends, before that returns ErrNotServed with w untouched, so the
// caller can still run the request elsewhere.
func (r *Reply) Serve(w http.ResponseWriter, upstream *http.Response) (Usage, error) {
	g := &gate{w: w, header: http.Header{}, started: time.Now()}
	head := &headBuffer{}
	tapped := *upstream
	tapped.Body = struct {
		io.Reader
		io.Closer
	}{io.TeeReader(upstream.Body, head), upstream.Body}
	usage, err := r.serve(g, &tapped)
	r.upstreamID = head.firstID()
	if !g.open {
		return usage, ErrNotServed
	}
	return usage, err
}

// ErrNotServed: the upstream failed or ended before any content; nothing was
// written to the caller.
var ErrNotServed = errors.New("translate: upstream failed before any content")

// gate holds an answer back until it carries content (commit).
type gate struct {
	w       http.ResponseWriter
	header  http.Header
	status  int
	buf     bytes.Buffer
	open    bool
	started time.Time
}

// gateHold is how long an upstream without content keeps the answer behind
// the gate, silent or sending only comments and pings: sseLines ticks at it,
// and that tick commits the headers and pings start (between events): the
// agent's client must not time out waiting for headers (Node's undici gives
// up at 300 s), and that liveness costs the fallback.
var gateHold = 10 * time.Second

// heartbeat is called on each silence tick, before a ping or keepalive.
func heartbeat(w io.Writer) {
	if g, ok := w.(*gate); ok && !g.open && time.Since(g.started) >= gateHold {
		g.commit()
	}
}

func (g *gate) Header() http.Header {
	if g.open {
		return g.w.Header()
	}
	return g.header
}

func (g *gate) WriteHeader(status int) {
	if g.open {
		g.w.WriteHeader(status)
		return
	}
	g.status = status
}

func (g *gate) Write(p []byte) (int, error) {
	if g.open {
		return g.w.Write(p)
	}
	return g.buf.Write(p)
}

func (g *gate) Flush() {
	if g.open {
		_ = http.NewResponseController(g.w).Flush()
	}
}

// commit sends what was held and passes everything after it straight on.
func (g *gate) commit() {
	if g.open {
		return
	}
	g.open = true
	for name, values := range g.header {
		g.w.Header()[name] = values
	}
	if g.status == 0 {
		g.status = http.StatusOK
	}
	g.w.WriteHeader(g.status)
	_, _ = g.w.Write(g.buf.Bytes())
	g.Flush()
}

// commitOn commits w when it is a gate.
func commitOn(w io.Writer) {
	if g, ok := w.(*gate); ok {
		g.commit()
	}
}

// heldEvents are the events that carry no content: they wait behind the gate.
var heldEvents = map[string]bool{"message_start": true, "ping": true, "error": true,
	"response.created": true, "response.in_progress": true, "response.failed": true}

func (r *Reply) serve(w http.ResponseWriter, upstream *http.Response) (Usage, error) {
	body := io.Reader(upstream.Body)
	if r.namespaces() && r.upstreamStream {
		pipe := namespaceStream(body, r.opts.route())
		defer pipe.Close()
		body = pipe
	}
	shown := r.opts.shown()
	switch {
	case r.from == Messages && r.to == Responses:
		return streamResponsesToAnthropic(w, body, r.stream, r.opts)
	case r.from == Messages && r.to == Chat:
		if r.upstreamStream {
			startSSE(w)
			markEstimate(w, r.opts.estimateFrom)
			usage, err := streamChatToAnthropic(w, body, shown, r.opts.chatSignature(), r.opts.estimateFrom)
			return usage.usage(), err
		}
		raw, err := io.ReadAll(body)
		if err != nil {
			return Usage{}, r.readFailed(w, err)
		}
		if chatAnswerFailed(raw) {
			// A 200 carrying an error, no choice or no JSON is no answer.
			writeAnthropicError(w, http.StatusBadGateway, "api_error", "upstream answered without a completion")
			return Usage{}, nil
		}
		answer, usage := chatToAnthropic(raw, shown, r.opts.chatSignature())
		writeJSON(w, answer)
		return usage.usage(), nil
	case r.from == Chat && r.to != Chat:
		out := newChatEmitter(w, r.stream, r.includeUsage, shown)
		var err error
		if r.to == Messages {
			err = streamAnthropicToChat(out, body)
		} else {
			err = streamResponsesToChat(out, body, r.opts.route())
		}
		return out.usage.usage(), err
	case r.from == Responses && r.to != Responses:
		out := finishTranslated(w, r.stream, shown, func(out *responsesStream) {
			if r.to == Messages {
				streamAnthropicToResponses(out, body, r.tools)
			} else {
				streamChatToResponses(out, body, r.tools, r.opts.replay())
			}
		})
		if out.failure != nil && *out.failure == streamCut {
			return out.usage.usage(), errStreamTruncated
		}
		return out.usage.usage(), nil
	case !r.upstreamStream:
		raw, err := io.ReadAll(body)
		if err != nil {
			return Usage{}, r.readFailed(w, err)
		}
		if r.namespaces() {
			raw = namespaceAnswer(raw, r.opts.route())
		}
		raw = r.tagReasoning(withModel(raw, shown))
		writeJSON(w, raw)
		return answerUsage(r.from, raw), nil
	case r.from == Responses && !r.stream:
		return r.assembleResponses(w, body, shown)
	}
	return r.relay(w, body, shown)
}

// relay copies a same-grammar stream line by line, flushing each, with
// every model renamed, and reads the usage on the way past.
func (r *Reply) relay(w http.ResponseWriter, body io.Reader, shown string) (Usage, error) {
	startSSE(w)
	out := newSSEWriter(w)
	var messages anthropicUsage
	var usage Usage
	terminal := false       // the upstream ended the answer itself
	upstreamFailed := false // with a failure of its own
	lines, cut, stop := sseLines(body)
	defer stop()
	boundary := true  // the upstream's last line ended an event
	openData := false // a data line came since the last blank line
	var tail []byte   // the last line, when it ended without a newline
	for line := range lines {
		if line == nil {
			heartbeat(w) // committing mid-event is safe: the rest follows
			if !boundary {
				continue // a ping never goes inside an event the upstream is still sending
			}
			if r.from == Messages {
				out.event("ping", map[string]any{"type": "ping"})
			} else { // an SSE comment: Codex's parser takes it for a keepalive, chat clients skip it
				_, _ = w.Write([]byte(": keepalive\n\n"))
				out.flush()
			}
			continue
		}
		if !bytes.HasSuffix(line, []byte("\n")) {
			tail = line // only the last line can end without one: held until the end is known
			continue
		}
		boundary = len(bytes.TrimRight(line, "\r\n")) == 0
		if boundary {
			openData = false
		}
		commit := false
		if data, ok := sseData(line); ok {
			openData = true
			failed := bytes.Contains(data, []byte(`"type":"error"`)) || bytes.Contains(data, []byte(`"error":{`)) || bytes.Contains(data, []byte(`"response.failed"`))
			if failed && !gated(w) {
				return usage, ErrNotServed // an error before any content: nothing reached the caller
			}
			// The upstream's own failure after content ends the stream: it is
			// relayed, and no second failure is added.
			upstreamFailed = upstreamFailed || failed
			terminal = terminal || failed || relayTerminal(r.from, data)
			commit = !failed && relayContent(r.from, data)
			if edited := r.tagReasoning(withModel(data, shown)); !bytes.Equal(edited, data) {
				line = append(append([]byte("data: "), edited...), '\n')
			}
			switch r.from {
			case Messages:
				anthropicStreamUsage(data, &messages)
				usage = messages.usage()
			case Chat:
				if bytes.Contains(data, []byte(`"usage"`)) {
					var chunk chatStreamChunk
					if json.Unmarshal(data, &chunk) == nil && chunk.Usage != nil {
						usage = chunk.Usage.usage()
					}
				}
			case Responses:
				if completed := completedResponse(data); completed != nil {
					usage = responsesUsageOf(completed).usage()
				}
			}
		}
		_, _ = w.Write(line)
		if commit {
			commitOn(w)
		}
		out.flush()
	}
	err := cut()
	if err == nil && tail != nil {
		// A clean end without a final newline: the line is whole.
		if data, ok := sseData(tail); ok {
			terminal = terminal || relayTerminal(r.from, data)
		}
		_, _ = w.Write(tail)
		out.flush()
	}
	if err == nil && !terminal {
		if !gated(w) {
			return usage, ErrNotServed
		}
		err = errors.New("no terminal event")
	}
	if err == nil && upstreamFailed {
		return usage, ErrUpstreamFailed
	}
	if err == nil {
		return usage, nil
	}
	message := "upstream stream ended early: " + err.Error()
	if openData {
		// The cut came after a data line of an unfinished event: end the event
		// (its data is whole) so the error is an event of its own. A line cut
		// short was held back and is not sent: a decoder would dispatch the
		// fragment. After an event line alone, the error's own event line
		// replaces the name.
		_, _ = w.Write([]byte("\n"))
	}
	switch r.from {
	case Messages:
		out.event("error", map[string]any{"type": "error", "error": map[string]any{"type": "api_error", "message": message}})
	case Chat:
		_, _ = w.Write(append(append([]byte("data: "), mustJSON(map[string]any{"error": map[string]any{"type": "api_error", "message": message}})...), '\n', '\n'))
		out.flush()
	case Responses:
		newResponsesStream(w, shown, true).fail(streamCut)
	}
	return usage, fmt.Errorf("%w: %w", errStreamTruncated, err)
}

// gated reports content already sent through w's gate (true for a plain writer).
func gated(w http.ResponseWriter) bool {
	g, ok := w.(*gate)
	return !ok || g.open
}

// ErrUpstreamFailed: the upstream sent a failure of its own after content;
// the caller got it as sent, as the stream's last event.
var ErrUpstreamFailed = errors.New("upstream failed mid-answer")

// relayContent reports a relayed event that carries content: not a start
// event or a ping, and for chat not a chunk that only opens the message (a
// role with empty content).
func relayContent(grammar string, data []byte) bool {
	for _, held := range []string{`"message_start"`, `"response.created"`, `"response.in_progress"`, `"type":"ping"`} {
		if bytes.Contains(data, []byte(held)) {
			return false
		}
	}
	if grammar != Chat || bytes.Equal(data, []byte("[DONE]")) {
		return true
	}
	var chunk chatStreamChunk
	if json.Unmarshal(data, &chunk) != nil {
		return false
	}
	for _, choice := range chunk.Choices {
		delta := choice.Delta
		if delta.Content != "" || delta.Reasoning != "" || delta.ReasoningContent != "" || len(delta.ToolCalls) > 0 || choice.FinishReason != "" {
			return true
		}
	}
	return false
}

// Relay is the Reply for an upstream that speaks the caller's grammar and was
// sent the caller's own bytes (the Caveman Cloud gateway): its answer goes
// through the same gate and naming as any other, shown as the model.
func Relay(grammar string, body []byte, shown string) *Reply {
	var probe struct {
		Stream bool `json:"stream"`
	}
	_ = json.Unmarshal(body, &probe)
	return &Reply{from: grammar, to: grammar, opts: Options{Shown: shown}, stream: probe.Stream, upstreamStream: probe.Stream}
}

// relayTerminal reports the event that ends an answer in grammar.
func relayTerminal(grammar string, data []byte) bool {
	switch grammar {
	case Messages:
		return bytes.Contains(data, []byte(`"message_stop"`))
	case Chat:
		return bytes.Equal(data, []byte("[DONE]")) || bytes.Contains(data, []byte(`"finish_reason":"`))
	}
	return bytes.Contains(data, []byte(`"response.completed"`)) || bytes.Contains(data, []byte(`"response.incomplete"`))
}

// assembleResponses answers a non-streaming Responses caller from a streamed
// upstream (the ChatGPT preview streams only): the response.completed
// object, its output rebuilt from the output_item.done events when the
// upstream left it empty.
func (r *Reply) assembleResponses(w http.ResponseWriter, body io.Reader, shown string) (Usage, error) {
	var items []json.RawMessage
	reader := bufio.NewReader(body)
	for {
		line, err := reader.ReadBytes('\n')
		if data, ok := sseData(line); ok {
			var event struct {
				Type     string          `json:"type"`
				Item     json.RawMessage `json:"item"`
				Response json.RawMessage `json:"response"`
			}
			_ = json.Unmarshal(data, &event)
			switch event.Type {
			case "response.output_item.done":
				items = append(items, r.tagReasoning(event.Item))
			case "response.completed":
				return writeAssembled(w, r.tagReasoning(event.Response), items, shown), nil
			case "response.failed":
				var failed struct {
					Error *responsesFailure `json:"error"`
				}
				_ = json.Unmarshal(event.Response, &failed)
				if failed.Error == nil {
					failed.Error = &responsesFailure{Code: "server_error", Message: "upstream response failed"}
				}
				writeResponsesFailure(w, false, shown, *failed.Error)
				return Usage{}, nil
			}
		}
		if err != nil {
			writeResponsesFailure(w, false, shown, streamCut)
			if errors.Is(err, io.EOF) {
				return Usage{}, fmt.Errorf("%w: no response.completed", errStreamTruncated)
			}
			return Usage{}, fmt.Errorf("%w: %w", errStreamTruncated, err)
		}
	}
}

func writeAssembled(w http.ResponseWriter, response json.RawMessage, items []json.RawMessage, shown string) Usage {
	var fields map[string]json.RawMessage
	if json.Unmarshal(response, &fields) == nil && len(items) > 0 {
		var output []json.RawMessage
		if json.Unmarshal(fields["output"], &output) != nil || len(output) == 0 {
			fields["output"] = mustJSON(items)
			response = mustJSON(fields)
		}
	}
	response = withModel(response, shown)
	writeJSON(w, response)
	return responsesUsageOf(response).usage()
}

func completedResponse(data []byte) json.RawMessage {
	if !bytes.Contains(data, []byte(`"response.completed"`)) {
		return nil
	}
	var event struct {
		Type     string          `json:"type"`
		Response json.RawMessage `json:"response"`
	}
	if json.Unmarshal(data, &event) != nil || event.Type != "response.completed" {
		return nil
	}
	return event.Response
}

func responsesUsageOf(response []byte) *responsesUsage {
	var parsed struct {
		Usage *responsesUsage `json:"usage"`
	}
	_ = json.Unmarshal(response, &parsed)
	return parsed.Usage
}

// answerUsage reads the usage of a non-streamed answer in grammar.
func answerUsage(grammar string, answer []byte) Usage {
	switch grammar {
	case Messages:
		var parsed struct {
			Usage anthropicUsage `json:"usage"`
		}
		_ = json.Unmarshal(answer, &parsed)
		return parsed.Usage.usage()
	case Chat:
		var parsed struct {
			Usage chatUsage `json:"usage"`
		}
		_ = json.Unmarshal(answer, &parsed)
		return parsed.Usage.usage()
	}
	return responsesUsageOf(answer).usage()
}

// readFailed answers a non-streamed call whose upstream body broke before a
// byte was written: a 502 in the caller's grammar.
func (r *Reply) readFailed(w http.ResponseWriter, err error) error {
	message := "upstream answer could not be read: " + err.Error()
	switch r.from {
	case Messages:
		writeAnthropicError(w, http.StatusBadGateway, "api_error", message)
	case Chat:
		w.Header().Set("content-type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write(mustJSON(map[string]any{"error": map[string]any{"type": "api_error", "message": message}}))
	default:
		writeResponsesFailure(w, false, r.opts.shown(), responsesFailure{Code: "server_error", Message: message})
	}
	return fmt.Errorf("%w: %w", errStreamTruncated, err)
}

func startSSE(w http.ResponseWriter) {
	w.Header().Set("content-type", "text/event-stream")
	w.Header().Set("cache-control", "no-cache")
	w.WriteHeader(http.StatusOK)
}

func writeJSON(w http.ResponseWriter, body []byte) {
	w.Header().Set("content-type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
	commitOn(w)
}

// withModel renames the model a payload reports to shown: the top-level
// "model" (a Messages, chat or Responses answer, a chat chunk), message.model
// (message_start) and response.model (Responses events). Every other byte is
// kept, including any "model" key inside tool input.
func withModel(data []byte, shown string) []byte {
	if shown == "" || !bytes.Contains(data, []byte(`"model"`)) {
		return data
	}
	root, ok := jsonsplice.Root(data)
	if !ok {
		return data
	}
	object := root
	for _, parent := range []string{"", "message", "response"} {
		if parent != "" {
			span, found := jsonsplice.Field(data, root, parent)
			if !found || data[span.Start] != '{' {
				continue
			}
			object = span
		}
		span, found := jsonsplice.Field(data, object, "model")
		if !found || data[span.Start] != '"' {
			continue
		}
		edited, err := jsonsplice.ReplaceRaw(data, span, mustJSON(shown))
		if err == nil {
			return withModel(edited, "") // one model per payload
		}
	}
	return data
}

// --- thinking signatures from Messages hosts other than Anthropic -----------

// namespaceStream relays a Messages stream with every thinking signature
// (redacted_thinking: its data) rewritten to "caveman:<route>:<signature>".
// A thinking block the host left unsigned gets "caveman:<route>:" so it still
// never reaches another host. Events are rewritten whole; every other event
// passes byte for byte. A read error reaches the reader as it came.
func namespaceStream(upstream io.Reader, route string) io.ReadCloser {
	out, in := io.Pipe()
	go func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				in.CloseWithError(fmt.Errorf("namespace stream: %v", recovered))
			}
		}()
		lines := bufio.NewReader(upstream)
		thinking := map[int]bool{} // index -> a thinking block still waiting for its signature
		var event []byte
		for {
			line, err := lines.ReadBytes('\n')
			event = append(event, line...)
			if len(event) > 0 && (len(bytes.TrimSpace(line)) == 0 || err != nil) {
				if _, werr := in.Write(namespaceEvent(event, route, thinking)); werr != nil {
					return
				}
				event = nil
			}
			if err != nil {
				if err == io.EOF {
					err = nil
				}
				in.CloseWithError(err)
				return
			}
		}
	}()
	return out
}

// namespaceEvent rewrites one SSE event (its lines, blank line included).
func namespaceEvent(event []byte, route string, thinking map[int]bool) []byte {
	var data []byte
	for _, line := range bytes.Split(event, []byte("\n")) {
		if rest, ok := bytes.CutPrefix(line, []byte("data:")); ok {
			data = bytes.TrimSpace(rest)
		}
	}
	if !bytes.Contains(data, []byte("thinking")) && !bytes.Contains(data, []byte("signature")) && !bytes.Contains(data, []byte("content_block_stop")) {
		return event
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(data, &fields) != nil {
		return event
	}
	var kind string
	var index int
	_ = json.Unmarshal(fields["type"], &kind)
	_ = json.Unmarshal(fields["index"], &index)
	prefix := signaturePrefix + route + ":"
	switch kind {
	case "content_block_start":
		var block map[string]json.RawMessage
		if json.Unmarshal(fields["content_block"], &block) != nil {
			return event
		}
		var blockType, signature, redacted string
		_ = json.Unmarshal(block["type"], &blockType)
		_ = json.Unmarshal(block["signature"], &signature)
		_ = json.Unmarshal(block["data"], &redacted)
		switch {
		case blockType == "thinking":
			thinking[index] = signature == ""
			if signature != "" {
				block["signature"] = mustJSON(prefix + signature)
			}
		case blockType == "redacted_thinking" && redacted != "":
			block["data"] = mustJSON(prefix + redacted)
		default:
			return event
		}
		fields["content_block"] = mustJSON(block)
	case "content_block_delta":
		var delta map[string]json.RawMessage
		var deltaType, signature string
		if json.Unmarshal(fields["delta"], &delta) != nil {
			return event
		}
		_ = json.Unmarshal(delta["type"], &deltaType)
		_ = json.Unmarshal(delta["signature"], &signature)
		if deltaType != "signature_delta" {
			return event
		}
		delete(thinking, index)
		delta["signature"] = mustJSON(prefix + signature)
		fields["delta"] = mustJSON(delta)
	case "content_block_stop":
		if !thinking[index] {
			return event
		}
		delete(thinking, index)
		signed := sseEvent("content_block_delta", map[string]any{"type": "content_block_delta", "index": index,
			"delta": map[string]any{"type": "signature_delta", "signature": prefix}})
		return append(signed, event...)
	default:
		return event
	}
	return sseEvent(kind, fields)
}

func sseEvent(name string, data any) []byte {
	return []byte("event: " + name + "\ndata: " + string(mustJSON(data)) + "\n\n")
}

// namespaceAnswer is namespaceStream for a non-streamed Messages answer.
func namespaceAnswer(answer []byte, route string) []byte {
	var body map[string]json.RawMessage
	var blocks []map[string]json.RawMessage
	if json.Unmarshal(answer, &body) != nil || json.Unmarshal(body["content"], &blocks) != nil {
		return answer
	}
	prefix, changed := signaturePrefix+route+":", false
	for _, block := range blocks {
		var kind, value string
		_ = json.Unmarshal(block["type"], &kind)
		field := "signature"
		if kind == "redacted_thinking" {
			field = "data"
		} else if kind != "thinking" {
			continue
		}
		_ = json.Unmarshal(block[field], &value)
		block[field], changed = mustJSON(prefix+value), true
	}
	if !changed {
		return answer
	}
	body["content"] = mustJSON(blocks)
	out, err := json.Marshal(body)
	if err != nil {
		return answer
	}
	return out
}

// --- native bodies ----------------------------------------------------------

// unsignedThinking reports a thinking signature or redacted data left empty
// ("signature":"" or "data":"", any spacing): a scan for "" and a look back,
// as fast as bytes.Index.
func unsignedThinking(body []byte) bool {
	for i := 0; ; {
		at := bytes.Index(body[i:], []byte(`""`))
		if at < 0 {
			return false
		}
		at += i
		j := at - 1
		for j >= 0 && isSpace(body[j]) {
			j--
		}
		if j >= 0 && body[j] == ':' {
			for j--; j >= 0 && isSpace(body[j]); j-- {
			}
			if key := body[:j+1]; bytes.HasSuffix(key, []byte(`"signature"`)) || bytes.HasSuffix(key, []byte(`"data"`)) {
				return true
			}
		}
		i = at + 2
	}
}

// AnthropicNative returns a Messages body bound for Anthropic's own API with
// every thinking block Anthropic did not mint removed (signed "caveman:…", or
// unsigned), exactly as stripForeignThinking(body, "anthropic") does. It
// returns the input slice unchanged (no re-marshal, byte-identical) when the
// body carries no "caveman:" signature, no unsigned thinking and no manual
// thinking (the one setting a foreign tool turn changes), and whenever the
// strip changed nothing.
// ponytail: a thinking block with no signature field at all skips the fast
// path's notice; Anthropic's grammar always sends the field.
func AnthropicNative(body []byte) []byte {
	if !bytes.Contains(body, []byte(signaturePrefix)) && !unsignedThinking(body) && !bytes.Contains(body, []byte(`"enabled"`)) {
		return body
	}
	fields, err := topFields(body)
	if err != nil || !stripForeignThinking(fields, anthropicRoute) {
		return body
	}
	return rawObject(fields)
}

// OpenAINative returns a Responses body bound for OpenAI with the reasoning
// items carrying this package's thinking envelope (Anthropic thinking stashed
// in encrypted_content) removed; the input slice unchanged (byte-identical)
// when it carries none.
func OpenAINative(body []byte) []byte {
	if !bytes.Contains(body, envelopeMarker) {
		return body
	}
	fields, err := topFields(body)
	if err != nil || !dropReasoning(fields) {
		return body
	}
	return rawObject(fields)
}
