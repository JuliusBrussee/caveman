package importers

import (
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/JuliusBrussee/caveman/shared/platform/telemetry"
)

// --- OTLP/JSON trace payload types (subset we decode) ---
// Mirrors services/gateway/internal/spans/otlp.go; copied (not imported)
// because gateway internal is not importable from internal/platform.

type otlpPayload struct {
	ResourceSpans []otlpResourceSpans `json:"resourceSpans"`
}

type otlpResourceSpans struct {
	Resource   otlpResource     `json:"resource"`
	ScopeSpans []otlpScopeSpans `json:"scopeSpans"`
}

type otlpResource struct {
	Attributes []otlpKV `json:"attributes"`
}

type otlpScopeSpans struct {
	Spans []otlpSpan `json:"spans"`
}

type otlpSpan struct {
	TraceID           string      `json:"traceId"`
	SpanID            string      `json:"spanId"`
	ParentSpanID      string      `json:"parentSpanId"`
	Name              string      `json:"name"`
	StartTimeUnixNano string      `json:"startTimeUnixNano"`
	EndTimeUnixNano   string      `json:"endTimeUnixNano"`
	Attributes        []otlpKV    `json:"attributes"`
	Events            []otlpEvent `json:"events"`
	Status            otlpStatus  `json:"status"`
}

type otlpKV struct {
	Key   string     `json:"key"`
	Value otlpAnyVal `json:"value"`
}

type otlpAnyVal struct {
	StringValue *string  `json:"stringValue"`
	IntValue    *string  `json:"intValue"` // proto3 int64 → JSON string
	DoubleValue *float64 `json:"doubleValue"`
	BoolValue   *bool    `json:"boolValue"`
}

func (a otlpAnyVal) string() string {
	switch {
	case a.StringValue != nil:
		return *a.StringValue
	case a.IntValue != nil:
		return *a.IntValue
	case a.DoubleValue != nil:
		return strconv.FormatFloat(*a.DoubleValue, 'f', -1, 64)
	case a.BoolValue != nil:
		if *a.BoolValue {
			return "true"
		}
		return "false"
	}
	return ""
}

type otlpEvent struct {
	Name         string   `json:"name"`
	TimeUnixNano string   `json:"timeUnixNano"`
	Attributes   []otlpKV `json:"attributes"`
}

type otlpStatus struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// parseOTLP decodes an OTLP/JSON trace payload and maps GenAI semantic
// conventions to Span columns. On a JSON error it returns no rows.
func parseOTLP(data []byte, opts Options) ([]Span, error) {
	var p otlpPayload
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, fmt.Errorf("otlp decode: %w", err)
	}
	var rows []Span
	for _, rs := range p.ResourceSpans {
		resAttrs := otlpKVMap(rs.Resource.Attributes)
		for _, ss := range rs.ScopeSpans {
			for _, sp := range ss.Spans {
				rows = append(rows, mapOTLPSpan(sp, resAttrs, opts))
			}
		}
	}
	return rows, nil
}

func mapOTLPSpan(sp otlpSpan, resAttrs map[string]string, opts Options) Span {
	attrs := otlpKVMap(sp.Attributes)

	genAI := telemetry.ExtractGenAIFields(attrs, resAttrs, sp.Name)

	// OTLP status code: 2=ERROR, 1=OK, 0=UNSET.
	status := "ok"
	switch sp.Status.Code {
	case 2:
		status = "error"
	case 0:
		status = "unset"
	}

	startNs := otlpNano(sp.StartTimeUnixNano)
	endNs := otlpNano(sp.EndTimeUnixNano)
	startStr, _ := epochToCH(startNs)
	endStr, _ := epochToCH(endNs)

	eventsJSON := "{}"
	if len(sp.Events) > 0 {
		events := make([]otlpEvent, len(sp.Events))
		for i, event := range sp.Events {
			kept := make([]otlpKV, 0, len(event.Attributes))
			for _, attribute := range event.Attributes {
				if telemetry.AttributeAllowed(attribute.Key) {
					kept = append(kept, attribute)
				}
			}
			event.Attributes = kept
			events[i] = event
		}
		if raw, err := json.Marshal(events); err == nil {
			eventsJSON = string(raw)
		}
	}

	agentSlug := attrs["cave.agent"]
	if agentSlug == "" {
		agentSlug = resAttrs["service.name"]
	}
	workflowSlug := attrs["cave.workflow"]

	allAttrs := make(map[string]string, len(attrs)+len(resAttrs))
	for k, v := range resAttrs {
		allAttrs["resource."+k] = v
	}
	for k, v := range attrs {
		allAttrs[k] = v
	}

	span := Span{
		Timestamp:         startStr,
		EndTimestamp:      endStr,
		TraceID:           sp.TraceID,
		SpanID:            sp.SpanID,
		ParentSpanID:      sp.ParentSpanID,
		AgentSlug:         agentSlug,
		WorkflowSlug:      workflowSlug,
		SpanType:          genAI.SpanType,
		SpanName:          sp.Name,
		Status:            status,
		DurationMS:        durationMS(startNs, endNs),
		Provider:          genAI.Provider,
		Model:             genAI.Model,
		ToolName:          genAI.ToolName,
		InputTokens:       genAI.InputTokens,
		OutputTokens:      genAI.OutputTokens,
		CachedInputTokens: genAI.CachedInputTokens,
		TotalCostUSD:      roundUSD(genAI.TotalCostUSD),
		Attributes:        allAttrs,
		EventsJSON:        eventsJSON,
	}
	applyScope(&span, opts)
	return span
}

func otlpKVMap(kvs []otlpKV) map[string]string {
	m := make(map[string]string, len(kvs))
	for _, kv := range kvs {
		m[kv.Key] = kv.Value.string()
	}
	return m
}

func otlpNano(s string) int64 {
	if s == "" {
		return 0
	}
	n, _ := strconv.ParseInt(s, 10, 64)
	return n
}
