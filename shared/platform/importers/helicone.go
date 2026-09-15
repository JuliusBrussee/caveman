package importers

import (
	"encoding/json"
	"fmt"
)

// heliconeRecord is one Helicone request/response log entry.
type heliconeRecord struct {
	ID        string          `json:"id"`
	RequestID string          `json:"request_id"`
	TraceID   string          `json:"trace_id"`
	CreatedAt any             `json:"created_at"`
	Request   heliconeRequest `json:"request"`
	Response  heliconeResp    `json:"response"`
	CostUSD   any             `json:"cost_usd"`
	Provider  string          `json:"provider"`
	Agent     string          `json:"agent"`
	Workflow  string          `json:"workflow"`
}

type heliconeRequest struct {
	Model    string          `json:"model"`
	Provider string          `json:"provider"`
	Messages json.RawMessage `json:"messages"`
}

type heliconeResp struct {
	Model      string         `json:"model"`
	StatusCode int            `json:"status_code"`
	Usage      heliconeUsage  `json:"usage"`
	Body       map[string]any `json:"body"`
}

type heliconeUsage struct {
	PromptTokens     any `json:"prompt_tokens"`
	CompletionTokens any `json:"completion_tokens"`
	CacheReadTokens  any `json:"cache_read_input_tokens"`
}

// heliconeEnvelope is the {"data":[...]} wrapper.
type heliconeEnvelope struct {
	Data []heliconeRecord `json:"data"`
}

// parseHelicone accepts either {"data":[...]} or a bare array of records.
// A malformed payload returns no rows.
func parseHelicone(data []byte, opts Options) ([]Span, error) {
	recs, err := decodeHelicone(data)
	if err != nil {
		return nil, err
	}
	rows := make([]Span, 0, len(recs))
	for _, rec := range recs {
		rows = append(rows, mapHelicone(rec, opts))
	}
	return rows, nil
}

func decodeHelicone(data []byte) ([]heliconeRecord, error) {
	var env heliconeEnvelope
	if err := json.Unmarshal(data, &env); err == nil && env.Data != nil {
		return env.Data, nil
	}
	var arr []heliconeRecord
	if err := json.Unmarshal(data, &arr); err != nil {
		return nil, fmt.Errorf("helicone decode: %w", err)
	}
	return arr, nil
}

func mapHelicone(rec heliconeRecord, opts Options) Span {
	ts, _ := parseTimeFlexible(rec.CreatedAt)

	model := rec.Response.Model
	if model == "" {
		model = rec.Request.Model
	}
	provider := rec.Provider
	if provider == "" {
		provider = rec.Request.Provider
	}

	// 2xx is ok; anything else (including 0/unknown) is an error.
	status := "ok"
	if sc := rec.Response.StatusCode; sc < 200 || sc >= 300 {
		status = "error"
	}

	traceID := rec.TraceID
	spanID := rec.ID
	if spanID == "" {
		spanID = rec.RequestID
	}
	if traceID == "" {
		traceID = spanID
	}

	attrs := map[string]string{"import.source": "helicone"}

	span := Span{
		Timestamp:         ts,
		EndTimestamp:      ts,
		TraceID:           traceID,
		SpanID:            spanID,
		AgentSlug:         rec.Agent,
		WorkflowSlug:      rec.Workflow,
		SpanType:          "chat",
		SpanName:          model,
		Status:            status,
		Provider:          provider,
		Model:             model,
		InputTokens:       parseFlexInt(rec.Response.Usage.PromptTokens),
		OutputTokens:      parseFlexInt(rec.Response.Usage.CompletionTokens),
		CachedInputTokens: parseFlexInt(rec.Response.Usage.CacheReadTokens),
		TotalCostUSD:      roundUSD(parseFlexFloat(rec.CostUSD)),
		Attributes:        attrs,
	}
	applyScope(&span, opts)
	return span
}
