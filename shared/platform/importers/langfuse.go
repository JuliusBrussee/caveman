package importers

import (
	"encoding/json"
	"fmt"
)

// langfuseObservation is a Langfuse observation. Only type=="generation"
// observations carry model/usage; others are mapped as generic spans.
type langfuseObservation struct {
	ID                  string          `json:"id"`
	TraceID             string          `json:"traceId"`
	ParentObservationID string          `json:"parentObservationId"`
	Type                string          `json:"type"`
	Name                string          `json:"name"`
	Model               string          `json:"model"`
	StartTime           any             `json:"startTime"`
	EndTime             any             `json:"endTime"`
	Level               string          `json:"level"` // DEFAULT | WARNING | ERROR
	StatusMessage       string          `json:"statusMessage"`
	Usage               langfuseUsage   `json:"usage"`
	Metadata            map[string]any  `json:"metadata"`
	Input               json.RawMessage `json:"input"`
	Output              json.RawMessage `json:"output"`
}

// langfuseUsage covers both the newer {input,output} and the legacy
// {promptTokens,completionTokens} naming, plus cache-read tokens.
type langfuseUsage struct {
	Input            any `json:"input"`
	Output           any `json:"output"`
	CacheReadInput   any `json:"cacheReadInputTokens"`
	PromptTokens     any `json:"promptTokens"`
	CompletionTokens any `json:"completionTokens"`
}

// langfuseEnvelope is the top-level {"observations":[...]} wrapper.
type langfuseEnvelope struct {
	Observations []langfuseObservation `json:"observations"`
}

// parseLangfuse accepts either {"observations":[...]} or a bare array of
// observations. A malformed payload returns no rows.
func parseLangfuse(data []byte, opts Options) ([]Span, error) {
	obs, err := decodeLangfuse(data)
	if err != nil {
		return nil, err
	}
	rows := make([]Span, 0, len(obs))
	for _, o := range obs {
		rows = append(rows, mapLangfuse(o, opts))
	}
	return rows, nil
}

func decodeLangfuse(data []byte) ([]langfuseObservation, error) {
	var env langfuseEnvelope
	if err := json.Unmarshal(data, &env); err == nil && env.Observations != nil {
		return env.Observations, nil
	}
	var arr []langfuseObservation
	if err := json.Unmarshal(data, &arr); err != nil {
		return nil, fmt.Errorf("langfuse decode: %w", err)
	}
	return arr, nil
}

func mapLangfuse(o langfuseObservation, opts Options) Span {
	start, startNs := parseTimeFlexible(o.StartTime)
	end, endNs := parseTimeFlexible(o.EndTime)

	status := "ok"
	switch o.Level {
	case "ERROR":
		status = "error"
	case "WARNING":
		status = "unset"
	}

	spanType := o.Type
	if spanType == "" {
		spanType = "generation"
	}

	inputTokens := firstNonZero(o.Usage.Input, o.Usage.PromptTokens)
	outputTokens := firstNonZero(o.Usage.Output, o.Usage.CompletionTokens)
	cachedTokens := parseFlexInt(o.Usage.CacheReadInput)

	attrs := map[string]string{"import.source": "langfuse"}
	provider := ""
	agentSlug := ""
	workflowSlug := ""
	for k, v := range o.Metadata {
		attrs["metadata."+k] = asString(v)
	}
	if p, ok := o.Metadata["provider"]; ok {
		provider = asString(p)
	}
	if a, ok := o.Metadata["agent"]; ok {
		agentSlug = asString(a)
	}
	if wf, ok := o.Metadata["workflow"]; ok {
		workflowSlug = asString(wf)
	}
	span := Span{
		Timestamp:         start,
		EndTimestamp:      end,
		TraceID:           o.TraceID,
		SpanID:            o.ID,
		ParentSpanID:      o.ParentObservationID,
		AgentSlug:         agentSlug,
		WorkflowSlug:      workflowSlug,
		SpanType:          spanType,
		SpanName:          o.Name,
		Status:            status,
		DurationMS:        durationMS(startNs, endNs),
		Provider:          provider,
		Model:             o.Model,
		InputTokens:       inputTokens,
		OutputTokens:      outputTokens,
		CachedInputTokens: cachedTokens,
		Attributes:        attrs,
	}
	applyScope(&span, opts)
	return span
}

// firstNonZero returns the first of the candidates that parses to a non-zero
// uint, else 0 (handles Langfuse's dual usage-field naming).
func firstNonZero(candidates ...any) uint64 {
	for _, c := range candidates {
		if n := parseFlexInt(c); n != 0 {
			return n
		}
	}
	return 0
}
