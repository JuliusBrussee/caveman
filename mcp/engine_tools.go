package mcp

import (
	"encoding/json"
	"log/slog"
	"strings"
	"sync"

	"github.com/JuliusBrussee/caveman/engine"
)

// Engine is the slice of the Caveman Engine the Caveman tools need.
// *engine.Engine satisfies it; tests inject a mock so the framing can be proven
// without the real compressors.
type Engine interface {
	Compress(input []byte, opts engine.Options) (engine.Result, error)
	Retrieve(handle string) ([]byte, error)
	RetrieveQuery(handle, query string) ([]byte, error)
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
	accounting := &compressionSession{}
	return []Tool{
		{
			Name:        ToolCompress,
			Description: "Compress a large text or tool-output payload before it enters the model context. Lossy (S4) but reversible: returns the compressed text, an inferred token ratio, and a recovery_handle for caveman_retrieve. Fails closed: unusable input or a result that is not smaller returns unchanged with ratio 0.",
			InputSchema: ObjectSchema(map[string]any{
				"input":        StringProp("The payload to compress."),
				"content_type": StringProp("Optional engine content type, e.g. json or toon."),
				"type":         StringProp("Alias for content_type."),
			}, "input"),
			Handler: func(args json.RawMessage) ToolResult { return compressTool(eng, accounting, log, args) },
		},
		{
			Name:        ToolRetrieve,
			Description: "Recover content that Caveman dropped. Omit query for the byte-exact stored original; use a broad query for ranked complete records covering the details you need. Repeated requests remain available after host compaction. Pass the exact recovery_handle beginning ccr_; full <<ccr:...>> markers, ccr:ccr_ prefixes, and native ccr:// references are normalized. Unknown handles fail explicitly. A query-narrowed view returns COMPLETE records and prints \"… [caveman: non-adjacent] …\" wherever content was skipped, including at the start and end: records on either side are NOT consecutive, and nothing may be inferred from their order or from what is missing between them.",
			InputSchema: ObjectSchema(map[string]any{
				"recovery_handle": StringProp("Exact ccr_ handle returned by Caveman or copied from a <<ccr:HANDLE>> marker."),
				"query":           StringProp("Optional broad query covering related details. Omit for the byte-exact full original."),
			}, "recovery_handle"),
			// Recovery returns the exact original bytes: exempt from the
			// result-size cap so a >cap original (the shared gateway store has no
			// matching ceiling) stays recoverable rather than failing closed on
			// size (#139).
			ExemptResultCap: true,
			Handler:         func(args json.RawMessage) ToolResult { return retrieveTool(eng, args) },
		},
		{
			Name:        ToolStats,
			Description: "Report compression calls handled by this MCP server process: tokens before/after, request count including repeated inputs and pass-throughs, and an inferred ratio. Excludes other processes and earlier sessions. Local-only; never a verified figure.",
			InputSchema: ObjectSchema(map[string]any{}),
			Handler:     func(json.RawMessage) ToolResult { return statsTool(accounting) },
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
func compressTool(eng Engine, accounting *compressionSession, log *slog.Logger, args json.RawMessage) ToolResult {
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
	accounting.record(res)
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
// fail-closed tool error carrying a cave_snake_code (PRD §11.4). Every request
// remains recoverable: hosts can compact away prior results without restarting
// their MCP server, so process-local call history cannot prove transcript presence.
func retrieveTool(eng Engine, args json.RawMessage) ToolResult {
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
	// A query narrows recovery to the relevant sections (BM25); empty query is
	// byte-exact full recovery. RetrieveQuery never drops detail it cannot rank.
	original, err := eng.RetrieveQuery(a.RecoveryHandle, a.Query)
	if err != nil {
		return ToolError("cave_unknown_handle", "no original found for handle")
	}
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
func statsTool(accounting *compressionSession) ToolResult {
	return ToolText(accounting.snapshot())
}

// The shared CCR database counts distinct retained payloads across processes.
// Session accounting instead records every compression call handled here,
// including repeated inputs and pass-throughs that create no recovery row.
type compressionSession struct {
	mu           sync.Mutex
	tokensBefore int
	tokensAfter  int
	requests     int
}

func (s *compressionSession) record(res engine.Result) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tokensBefore += res.TokensBefore
	s.tokensAfter += res.TokensAfter
	s.requests++
}

func (s *compressionSession) snapshot() statsPayload {
	s.mu.Lock()
	defer s.mu.Unlock()
	ratio := 0.0
	if s.tokensBefore > 0 {
		ratio = float64(s.tokensBefore-s.tokensAfter) / float64(s.tokensBefore)
	}
	return statsPayload{
		TokensBefore: s.tokensBefore,
		TokensAfter:  s.tokensAfter,
		Requests:     s.requests,
		Ratio:        ratio,
		Basis:        engine.BasisInferred,
		Scope:        "session",
	}
}

// discard is an io.Writer that drops everything (for a nil logger default).
type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }
