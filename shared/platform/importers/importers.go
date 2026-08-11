// Package importers parses third-party telemetry exports into caveman.spans
// rows. It is a pure-parser package: it has no I/O, no ClickHouse, and no
// Postgres dependency, so it is unit-testable with fixtures.
//
// Supported formats (an unknown/misspelled format FAILS CLOSED — see Parse):
//
//   - otlp          OTLP/JSON trace payload (resourceSpans→scopeSpans→spans),
//     GenAI semantic-convention attribute mapping (mirrors the gateway's
//     services/gateway/internal/spans/otlp.go mapping).
//   - caveman-jsonl one JSON span per line, fields already close to the Span shape.
//   - langfuse      {"observations":[...]} (or a bare array) of Langfuse observations.
//   - helicone      {"data":[...]} of Helicone request/response records.
//   - generic       field-mapping mode: Options.FieldMap maps a target Span
//     column to a source JSON path applied to each record in an array.
//
// HONESTY / TENANT-SCOPING — every produced Span carries Options.OrganizationID
// and Options.ProjectID. The caller (control-api) sets these from the
// authenticated JWT, never from the uploaded file, and the parsers here always
// overwrite any org/project the file might contain. See root CLAUDE.md.
package importers

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/JuliusBrussee/caveman/shared/platform/telemetry"
)

type Span = telemetry.Span

// Options carries the authenticated tenant scope and the generic field map.
// OrganizationID / ProjectID are ALWAYS applied to every produced Span; they
// come from the JWT, never the uploaded body (tenant-scoped rule).
type Options struct {
	OrganizationID string
	ProjectID      string
	IngestionID    string
	SourceKind     string
	SourceSystem   string
	ContentMode    string
	// FieldMap is used only by the "generic" format. It maps a target Span
	// column name (e.g. "provider", "model", "input_tokens", "trace_id") to a
	// dotted source JSON path into each record (e.g. "request.model",
	// "usage.prompt_tokens"). See genericMapping for the recognised target keys.
	FieldMap map[string]string
}

// Summary is the per-import result returned alongside the parsed rows.
type Summary struct {
	Format   string `json:"format"`
	RowCount int    `json:"row_count"`
}

// Format identifiers. Anything not in this set fails closed in Parse.
const (
	FormatOTLP         = "otlp"
	FormatCavemanJSONL = "caveman-jsonl"
	FormatLangfuse     = "langfuse"
	FormatHelicone     = "helicone"
	FormatGeneric      = "generic"
)

// Parse dispatches to the per-format parser. An unknown/misspelled format
// returns an error (fail-closed) — it never silently passes through.
//
// On any parse error the parser returns no rows (no partial silent ingest);
// the caller fails the whole import.
func Parse(format string, data []byte, opts Options) ([]Span, Summary, error) {
	if opts.SourceSystem == "" {
		opts.SourceSystem = sourceSystemForFormat(format)
	}
	var (
		rows []Span
		err  error
	)
	switch format {
	case FormatOTLP:
		rows, err = parseOTLP(data, opts)
	case FormatCavemanJSONL:
		rows, err = parseCavemanJSONL(data, opts)
	case FormatLangfuse:
		rows, err = parseLangfuse(data, opts)
	case FormatHelicone:
		rows, err = parseHelicone(data, opts)
	case FormatGeneric:
		rows, err = parseGeneric(data, opts)
	default:
		// fail-closed: unknown formats are an error, never a silent pass-through.
		return nil, Summary{}, fmt.Errorf("unknown import format %q: supported formats are otlp, caveman-jsonl, langfuse, helicone, generic", format)
	}
	if err != nil {
		return nil, Summary{}, err
	}
	for i := range rows {
		if len(rows[i].EventsJSON) > telemetry.MaxEventsBytes {
			return nil, Summary{}, fmt.Errorf("sanitize row %d: events exceed %d bytes", i, telemetry.MaxEventsBytes)
		}
		sanitized, sanitizeErr := telemetry.SanitizeAttributes(rows[i].Attributes)
		if sanitizeErr != nil {
			return nil, Summary{}, fmt.Errorf("sanitize row %d: %w", i, sanitizeErr)
		}
		rows[i].Attributes = sanitized
	}
	return rows, Summary{Format: format, RowCount: len(rows)}, nil
}

// ---------------------------------------------------------------------------
// Shared helpers
// ---------------------------------------------------------------------------

// chTimeLayout is the ClickHouse DateTime64 textual layout the gateway writer
// uses; importers must produce the same shape ("2006-01-02 15:04:05.000").
const chTimeLayout = "2006-01-02 15:04:05.000"

// applyScope stamps the authenticated org/project onto a span and fills the
// honest defaults that the spans schema expects for non-null operation. It is
// called for EVERY produced span so the uploaded file can never set org/project.
func applyScope(sp *Span, opts Options) {
	sp.OrganizationID = opts.OrganizationID
	sp.ProjectID = opts.ProjectID
	if sp.AgentSlug == "" {
		sp.AgentSlug = "imported-agent"
	}
	if sp.WorkflowSlug == "" {
		sp.WorkflowSlug = "imported-workflow"
	}
	if sp.Status == "" {
		sp.Status = "ok"
	}
	if sp.Attributes == nil {
		sp.Attributes = map[string]string{}
	}
	if sp.EventsJSON == "" {
		sp.EventsJSON = "{}"
	}
	if sp.Timestamp == "" {
		sp.Timestamp = time.Now().UTC().Format(chTimeLayout)
	}
	if sp.EndTimestamp == "" {
		sp.EndTimestamp = sp.Timestamp
	}
	// Imported cost is user-controlled evidence. ClickHouse accepts Float64 NaN
	// and infinities, but those values poison SUMs and every downstream savings
	// comparison. Negative spend is not a valid provider-usage observation either.
	// Fail closed to the honest zero instead of persisting an unusable amount.
	if sp.TotalCostUSD < 0 || math.IsNaN(sp.TotalCostUSD) || math.IsInf(sp.TotalCostUSD, 0) {
		sp.TotalCostUSD = 0
	}
	sourceKind := opts.SourceKind
	if sourceKind == "" {
		sourceKind = telemetry.SourceKindFileImport
	}
	sourceSystem := opts.SourceSystem
	if sourceSystem == "" {
		sourceSystem = telemetry.SourceSystemUnknown
	}
	contentMode := opts.ContentMode
	if contentMode == "" {
		contentMode = telemetry.ContentModeMetadataOnly
	}
	telemetry.ApplyProvenance(sp, telemetry.Provenance{
		SourceKind:           sourceKind,
		SourceSystem:         sourceSystem,
		IngestionID:          opts.IngestionID,
		NormalizationVersion: telemetry.NormalizationVersion,
		ContentMode:          contentMode,
	})
}

func sourceSystemForFormat(format string) string {
	switch format {
	case FormatOTLP:
		return telemetry.SourceSystemOTel
	case FormatCavemanJSONL:
		return telemetry.SourceSystemCaveman
	case FormatLangfuse:
		return telemetry.SourceSystemLangfuse
	case FormatHelicone:
		return telemetry.SourceSystemHelicone
	case FormatGeneric:
		return telemetry.SourceSystemGeneric
	default:
		return telemetry.SourceSystemUnknown
	}
}

// parseFlexInt coerces a JSON value (number or numeric string) to a non-negative
// uint64. Negative or unparseable values become 0.
func parseFlexInt(v any) uint64 {
	validFloat := func(n float64) uint64 {
		// JSON numbers decode through float64 in generic imports. Token counts are
		// whole, finite, non-negative integers; rejecting fractions avoids silently
		// turning malformed provider evidence into a plausible count.
		if n < 0 || math.IsNaN(n) || math.IsInf(n, 0) || math.Trunc(n) != n || n >= 1.8446744073709552e19 {
			return 0
		}
		return uint64(n)
	}
	switch x := v.(type) {
	case float64:
		return validFloat(x)
	case int:
		if x < 0 {
			return 0
		}
		return uint64(x)
	case int64:
		if x < 0 {
			return 0
		}
		return uint64(x)
	case string:
		n, err := strconv.ParseUint(strings.TrimSpace(x), 10, 64)
		if err != nil {
			return 0
		}
		return n
	}
	return 0
}

// parseFlexFloat coerces a JSON value (number or numeric string) to a finite,
// non-negative float64. It is used for provider-reported USD cost only.
func parseFlexFloat(v any) float64 {
	valid := func(n float64) float64 {
		if n < 0 || math.IsNaN(n) || math.IsInf(n, 0) {
			return 0
		}
		return n
	}
	switch x := v.(type) {
	case float64:
		return valid(x)
	case int:
		return valid(float64(x))
	case int64:
		return valid(float64(x))
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(x), 64)
		if err != nil {
			return 0
		}
		return valid(f)
	}
	return 0
}

// asString coerces a JSON scalar to a string (numbers/bools rendered too).
func asString(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case bool:
		if x {
			return "true"
		}
		return "false"
	case nil:
		return ""
	default:
		return fmt.Sprintf("%v", x)
	}
}

// roundUSD preserves the ClickHouse money columns' Decimal128(10) precision.
// Rounding each request to micro-dollars loses real spend for low-cost models
// (for example a single $0.20/M input token is only $0.0000002).
func roundUSD(v float64) float64 {
	if v < 0 || math.IsNaN(v) || math.IsInf(v, 0) {
		return 0
	}
	rounded := math.Round(v*10_000_000_000) / 10_000_000_000
	if math.IsNaN(rounded) || math.IsInf(rounded, 0) {
		return 0
	}
	return rounded
}

// parseTimeFlexible parses a timestamp that may arrive as RFC3339(/nano),
// epoch milliseconds, or epoch seconds, and returns it in chTimeLayout. The
// second return is the unix-nanos, used to compute duration when both ends are
// present. Returns ("", 0) when the value is empty/unparseable.
func parseTimeFlexible(v any) (string, int64) {
	switch x := v.(type) {
	case string:
		s := strings.TrimSpace(x)
		if s == "" {
			return "", 0
		}
		for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05.000Z", chTimeLayout} {
			if t, err := time.Parse(layout, s); err == nil {
				return t.UTC().Format(chTimeLayout), t.UTC().UnixNano()
			}
		}
		// Numeric-as-string epoch.
		if n, err := strconv.ParseInt(s, 10, 64); err == nil {
			return epochToCH(n)
		}
		return "", 0
	case float64:
		return epochToCH(int64(x))
	case int64:
		return epochToCH(x)
	case int:
		return epochToCH(int64(x))
	}
	return "", 0
}

// epochToCH converts an epoch value (heuristically seconds, millis, micros, or
// nanos by magnitude) to chTimeLayout plus unix-nanos.
func epochToCH(n int64) (string, int64) {
	if n <= 0 {
		return "", 0
	}
	var ns int64
	switch {
	case n > 1e18: // nanoseconds
		ns = n
	case n > 1e15: // microseconds
		ns = n * 1_000
	case n > 1e12: // milliseconds
		ns = n * 1_000_000
	default: // seconds
		ns = n * 1_000_000_000
	}
	t := time.Unix(0, ns).UTC()
	return t.Format(chTimeLayout), ns
}

// durationMS computes a non-negative millisecond duration from two unix-nanos.
func durationMS(startNs, endNs int64) uint64 {
	if endNs > startNs && startNs > 0 {
		return uint64((endNs - startNs) / 1_000_000)
	}
	return 0
}
