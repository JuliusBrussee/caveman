package mcp

import (
	"encoding/json"
	"log/slog"
	"strings"
	"sync"

	"github.com/JuliusBrussee/caveman/engine"
	"github.com/JuliusBrussee/caveman/engine/ccr"
)

// Engine is the slice of the Caveman Engine the Caveman tools need.
// *engine.Engine satisfies it; tests inject a mock so the framing can be proven
// without the real compressors.
type Engine interface {
	Compress(input []byte, opts engine.Options) (engine.Result, error)
	Retrieve(handle string) ([]byte, error)
	RetrieveQuery(handle, query string) ([]byte, error)
	Stats() (ccr.Stats, error)
	EncodeTOON(input []byte) ([]byte, error)
	DecodeTOON(input []byte) ([]byte, error)
}

// Tool names — exactly these five, case-sensitive, are exposed (PRD §11.1;
// the TOON pair is the wrap-simplification spec's agent-facing encoder).
const (
	ToolCompress   = "caveman_compress"
	ToolRetrieve   = "caveman_retrieve"
	ToolStats      = "caveman_stats"
	ToolToonEncode = "caveman_toon_encode"
	ToolToonDecode = "caveman_toon_decode"
)

// EngineTools returns the five Caveman compression tools bound to eng. log may
// be nil. This is the tool set the caveman-mcp binary serves.
func EngineTools(eng Engine, log *slog.Logger) []Tool {
	if log == nil {
		log = slog.New(slog.NewTextHandler(discard{}, nil))
	}
	// One server process is one session, so the recovery ledger lives for exactly
	// as long as the conversation whose retrieves it is counting.
	recoveries := newRetrieveSession()
	return []Tool{
		{
			Name:        ToolCompress,
			Description: "Compress a large text or tool-output payload before it enters the model context. Lossy (S4) but reversible: returns the compressed text, an inferred token ratio, and a recovery_handle for caveman_retrieve. Fails closed: unusable input or a result that is not smaller returns unchanged with ratio 0.",
			InputSchema: ObjectSchema(map[string]any{
				"input":        StringProp("The payload to compress."),
				"content_type": StringProp("Optional engine content type, e.g. json or toon."),
				"type":         StringProp("Alias for content_type."),
			}, "input"),
			Handler: func(args json.RawMessage) ToolResult { return compressTool(eng, log, args) },
		},
		{
			Name:        ToolRetrieve,
			Description: "Recover content that a previous caveman_compress (or the Caveman Engine) dropped. LAST RESORT, not a paging API. An elision marker states facts computed from the exact units it replaced (\"… 340 rows elided (caveman): all state=charged; range amount=5.00..199.99 …\"), and every dropped unit resembles one still shown — so counts, totals, and per-field questions are usually answerable from the visible view plus those invariants, with no call at all. Retrieve only for exact bytes you cannot derive that way, and then make ONE call per handle with a broad query covering everything you need; a sequence of narrow per-row retrievals re-reads the whole conversation prefix each time and costs far more than the compression saved. Pass the exact recovery_handle beginning ccr_; full <<ccr:...>> markers and ccr:ccr_ prefixes are normalized. Unknown handles fail explicitly. A query-narrowed view returns COMPLETE records only — never a plucked line — and prints \"… [caveman: non-adjacent] …\" wherever content was skipped, including at the start and end: records on either side of that marker are NOT consecutive and nothing may be inferred from their order or from what is missing between them.",
			InputSchema: ObjectSchema(map[string]any{
				"recovery_handle": StringProp("Exact ccr_ handle returned by Caveman or copied from a <<ccr:HANDLE>> marker."),
				"query":           StringProp("Optional but strongly preferred: one broad description covering every detail you need from this handle, so a single call answers the whole question instead of many narrow ones."),
			}, "recovery_handle"),
			// Recovery returns the exact original bytes: exempt from the
			// result-size cap so a >cap original (the shared gateway store has no
			// matching ceiling) stays recoverable rather than failing closed on
			// size (#139).
			ExemptResultCap: true,
			Handler:         func(args json.RawMessage) ToolResult { return retrieveTool(eng, recoveries, args) },
		},
		{
			Name:        ToolStats,
			Description: "Report this session's aggregate compression: tokens before/after, request count, and an inferred ratio. Local-only; never a verified figure.",
			InputSchema: ObjectSchema(map[string]any{}),
			Handler:     func(json.RawMessage) ToolResult { return statsTool(eng) },
		},
		{
			Name:        ToolToonEncode,
			Description: "Re-encode uniform/tabular JSON as TOON before quoting it into context — smaller for arrays of same-shaped objects. Lossless data re-encoding with JSON round-trip; input that cannot round-trip is returned unchanged with a note saying why. Returns both sizes so you can decide.",
			InputSchema: ObjectSchema(map[string]any{
				"input": StringProp("The JSON to re-encode as TOON."),
			}, "input"),
			Handler: func(args json.RawMessage) ToolResult { return toonEncodeTool(eng, log, args) },
		},
		{
			Name:        ToolToonDecode,
			Description: "Decode TOON back into JSON. Fails loudly on input that is not valid TOON — it never emits the raw input as if it were JSON.",
			InputSchema: ObjectSchema(map[string]any{
				"input": StringProp("The TOON to decode back into JSON."),
			}, "input"),
			Handler: func(args json.RawMessage) ToolResult { return toonDecodeTool(eng, args) },
		},
	}
}

type compressPayload struct {
	Compressed      string  `json:"compressed"`
	Ratio           float64 `json:"ratio"`
	TokensBefore    int     `json:"tokens_before"`
	TokensAfter     int     `json:"tokens_after"`
	Basis           string  `json:"basis"`
	ContentType     string  `json:"content_type"`
	RecoveryHandle  *string `json:"recovery_handle"`
	Method          string  `json:"method,omitempty"`
	LosslessToModel *bool   `json:"lossless_to_model,omitempty"`
}

type statsPayload struct {
	TokensBefore int     `json:"tokens_before"`
	TokensAfter  int     `json:"tokens_after"`
	Requests     int     `json:"requests"`
	Ratio        float64 `json:"ratio"`
	Basis        string  `json:"basis"`
	Scope        string  `json:"scope"`
}

// compressTool compresses the input string. Fail-closed: malformed,
// incompressible, or not-smaller input returns the original with ratio 0 and a
// null handle — a pass-through, never an error (PRD §11.3).
func compressTool(eng Engine, log *slog.Logger, args json.RawMessage) ToolResult {
	var a struct {
		Input       string `json:"input"`
		ContentType string `json:"content_type"`
		Type        string `json:"type"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return ToolError("cave_invalid_arguments", "compress: invalid arguments")
	}
	contentType := a.ContentType
	if contentType == "" {
		contentType = a.Type
	}
	res, err := eng.Compress([]byte(a.Input), engine.Options{Mode: engine.ModeCompress, Type: contentType})
	if err != nil {
		log.Warn("compress fell back to pass-through", "err", err)
		// Engine returns a fully-accounted byte-exact pass-through when recovery
		// persistence fails. Preserve it. A third-party Engine implementation that
		// returns an unsafe/empty result with an error fails loudly instead of
		// making non-empty content appear to cost zero tokens.
		if string(res.Output) != a.Input || res.TokensAfter != res.TokensBefore || (a.Input != "" && res.TokensBefore <= 0) || res.RecoveryHandle != "" {
			return ToolError("cave_compress_failed", "compress: recovery persistence failed")
		}
	}
	return ToolText(compressResultPayload(res))
}

func compressResultPayload(res engine.Result) compressPayload {
	var handle *string
	if res.RecoveryHandle != "" {
		h := res.RecoveryHandle
		handle = &h
	}
	return compressPayload{
		Compressed:      string(res.Output),
		Ratio:           res.Ratio,
		TokensBefore:    res.TokensBefore,
		TokensAfter:     res.TokensAfter,
		Basis:           res.Basis,
		ContentType:     res.ContentType,
		RecoveryHandle:  handle,
		Method:          res.Method,
		LosslessToModel: res.LosslessToModel,
	}
}

// retrieveTool returns the exact original for a handle; an unknown handle is a
// fail-closed tool error carrying a cave_snake_code (PRD §11.4). session may be
// nil, in which case every retrieve behaves exactly as it did before the
// anti-storm ledger existed.
func retrieveTool(eng Engine, session *retrieveSession, args json.RawMessage) ToolResult {
	var a struct {
		RecoveryHandle string `json:"recovery_handle"`
		Query          string `json:"query"`
	}
	if err := json.Unmarshal(args, &a); err != nil || a.RecoveryHandle == "" {
		return ToolError("cave_invalid_arguments", "retrieve: missing recovery_handle")
	}
	a.RecoveryHandle = normalizeRecoveryHandle(a.RecoveryHandle)
	if a.RecoveryHandle == "" {
		return ToolError("cave_invalid_arguments", "retrieve: missing recovery_handle")
	}
	// Already answered verbatim in this session: the bytes are above in the
	// transcript, so re-sending them buys nothing and costs a turn. Only ever said
	// of a request already served — never of content this session has not seen.
	if session.alreadyServed(a.RecoveryHandle, a.Query) {
		return ToolRawText(repeatNote)
	}
	// Past the historical storm threshold, stop paging. This policy intends to
	// avoid later narrow-query turns; no paired counterfactual has validated the
	// threshold or a token saving.
	if session.shouldPayOutInFull() {
		original, err := eng.RetrieveQuery(a.RecoveryHandle, "")
		if err != nil {
			return ToolError("cave_unknown_handle", "no original found for handle")
		}
		session.record(a.RecoveryHandle, a.Query)
		session.record(a.RecoveryHandle, "")
		return ToolRawText(fullPayoutNote + "\n\n" + string(original))
	}
	// A query narrows recovery to the relevant sections (BM25); empty query is
	// byte-exact full recovery. RetrieveQuery never drops detail it cannot rank.
	original, err := eng.RetrieveQuery(a.RecoveryHandle, a.Query)
	if err != nil {
		return ToolError("cave_unknown_handle", "no original found for handle")
	}
	session.record(a.RecoveryHandle, a.Query)
	return ToolRawText(string(original))
}

// normalizeRecoveryHandle accepts every form of a recovery reference this stack
// has ever shown an agent, because an agent copies what it sees and a form that
// does not resolve turns one recovery into a storm.
//
//   - `ccr_…` — the bare handle Compress returns.
//   - `<<ccr:ccr_…>>` — the marker embedded in compressed content.
//   - `ccr:ccr_…` — the same, half-stripped.
//   - `ccr://…` — the native runtime's tool-output mask (`full: ccr://<id>`),
//     whose id is a typed OBJECT id rather than a blob handle. Engine.Retrieve
//     resolves both id spaces; this only has to hand it the id. Missing this form
//     is what made inventory-mismatch and webhook-delivery-gaps unanswerable on
//     2026-08-08 — the agent was shown a reference the recovery tool rejected.
func normalizeRecoveryHandle(handle string) string {
	handle = strings.TrimSpace(handle)
	if strings.HasPrefix(handle, "<<") && strings.HasSuffix(handle, ">>") {
		handle = strings.TrimSuffix(strings.TrimPrefix(handle, "<<"), ">>")
		handle = strings.TrimSpace(handle)
	}
	if rest, ok := strings.CutPrefix(handle, "ccr://"); ok {
		return strings.Trim(strings.TrimSpace(rest), "/")
	}
	if rest, ok := strings.CutPrefix(handle, "ccr:"); ok {
		return strings.TrimSpace(rest)
	}
	return handle
}

type toonEncodePayload struct {
	Output      string `json:"output"`
	Encoded     bool   `json:"encoded"`
	InputBytes  int    `json:"input_bytes"`
	OutputBytes int    `json:"output_bytes"`
	Note        string `json:"note,omitempty"`
}

// toonEncodeTool re-encodes JSON as TOON on explicit agent request. This is
// not the proxy's best-of gate: a valid encoding is returned even when it is
// not smaller (both sizes included), because the agent asked for it and can
// decide. Lossless round-trip: un-encodable input comes back unchanged with a
// note — never a silent no-op, never an invented encoding.
func toonEncodeTool(eng Engine, log *slog.Logger, args json.RawMessage) ToolResult {
	var a struct {
		Input string `json:"input"`
	}
	if err := json.Unmarshal(args, &a); err != nil || a.Input == "" {
		return ToolError("cave_invalid_arguments", "toon_encode: missing input")
	}
	out, err := eng.EncodeTOON([]byte(a.Input))
	if err != nil {
		log.Warn("toon encode fell back to pass-through", "err", err)
		return ToolText(toonEncodePayload{
			Output:      a.Input,
			Encoded:     false,
			InputBytes:  len(a.Input),
			OutputBytes: len(a.Input),
			Note:        "not encoded: " + err.Error(),
		})
	}
	return ToolText(toonEncodePayload{
		Output:      string(out),
		Encoded:     true,
		InputBytes:  len(a.Input),
		OutputBytes: len(out),
	})
}

// toonDecodeTool decodes TOON back to JSON. It fails loudly: invalid TOON is
// a tool error, never the raw input passed off as JSON (the same invariant as
// the CLI decode verb).
func toonDecodeTool(eng Engine, args json.RawMessage) ToolResult {
	var a struct {
		Input string `json:"input"`
	}
	if err := json.Unmarshal(args, &a); err != nil || a.Input == "" {
		return ToolError("cave_invalid_arguments", "toon_decode: missing input")
	}
	out, err := eng.DecodeTOON([]byte(a.Input))
	if err != nil {
		return ToolError("cave_invalid_toon", "toon_decode: input is not valid TOON; no JSON emitted")
	}
	return ToolRawText(string(out))
}

// statsTool returns session-scoped aggregate accounting, labeled inferred. The
// string "verified" never appears (PRD §11.5).
func statsTool(eng Engine) ToolResult {
	st, err := eng.Stats()
	if err != nil {
		return ToolError("cave_stats_unavailable", "stats unavailable")
	}
	return ToolText(statsPayload{
		TokensBefore: st.Totals.TokensBefore,
		TokensAfter:  st.Totals.TokensAfter,
		Requests:     st.Totals.Count,
		Ratio:        st.Totals.Ratio,
		Basis:        engine.BasisInferred,
		Scope:        "session",
	})
}

// discard is an io.Writer that drops everything (for a nil logger default).
type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }

// --- retrieve anti-storm ---
//
// Recovery is meant to be a last resort, and each call costs a whole agent turn:
// the model re-reads the entire conversation prefix, and everything a previous
// retrieve returned is part of that prefix from then on. So N retrieves do not
// cost N payloads, they cost N turns over a transcript that each one grew.
//
// A 2026-08-10 read-only sweep of 229 local CaveBench stdout files found 34
// sessions with 534 recovery tool calls: 3 sessions had one, 16 had 2-5, and
// 15 had more than 5 (p95 118, max 143). Eight of those 15 high-call sessions
// still passed their exact task grader. Only 3 of 534 normalized (handle,
// trimmed-query) pairs were exact repeats, so repeat suppression had little
// historical applicability; many calls used new handles or pointer chains.
// These are confounded arms, tasks, and repeated benchmark batches, not a paired
// intervention or the managed gateway's tool-result population. They describe
// recovery shape but do not validate a universal threshold or token saving.
//
// Two rules, neither of which may ever withhold something this session has not
// already been given:
//
//  1. An identical (handle, query) is answered with a pointer to the answer
//     already in the transcript. The bytes are verbatim above; sending them twice
//     is pure cost.
//  2. Past retrieveStormThreshold distinct retrieves, the next one returns the
//     handle's FULL stored original instead of a query-narrowed view, and says so.
//     This intends to avoid a longer sequence of narrow views by leaving nothing
//     to page for; the confounded corpus above does not prove a token or outcome
//     improvement from the rule.

// retrieveStormThreshold is how many distinct retrieves a session may make before
// the next one is paid out in full. Five is preserved as historical runtime
// behavior. The larger sweep above supersedes the incomplete 11/25/46 prior:
// more-than-five-call sessions can still pass, and no same-task paired run has
// isolated this intervention. Changing the threshold requires that experiment;
// these descriptive counts authorize neither tightening nor removal.
const retrieveStormThreshold = 5

const repeatNote = "… caveman: this exact recovery_handle and query were already answered earlier in this session — the content is verbatim above in this conversation. Re-read it there; issuing the same query again returns nothing new."

const fullPayoutNote = "… caveman: this session has now made several recoveries, so here is the COMPLETE stored original for this handle rather than a query-narrowed view. Everything recoverable for this handle is below — read it once and answer from it; further queries against it cannot return anything this does not already contain."

// retrieveSession is a session's recovery ledger. A nil *retrieveSession is safe
// to use and disables both behaviours, so any caller that has no session concept
// keeps the old semantics.
type retrieveSession struct {
	mu     sync.Mutex
	served map[string]bool
}

func newRetrieveSession() *retrieveSession {
	return &retrieveSession{served: map[string]bool{}}
}

// retrieveKey pairs a handle with a query. The query is compared after the same
// trimming RetrieveQuery applies, so "  " and "" are one request, not two.
func retrieveKey(handle, query string) string {
	return handle + "\x00" + strings.TrimSpace(query)
}

func (s *retrieveSession) alreadyServed(handle, query string) bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.served[retrieveKey(handle, query)]
}

func (s *retrieveSession) shouldPayOutInFull() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.served) >= retrieveStormThreshold
}

func (s *retrieveSession) record(handle, query string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.served[retrieveKey(handle, query)] = true
}
