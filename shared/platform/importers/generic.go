package importers

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Recognised generic target columns. FieldMap keys outside this set are
// ignored (they would not map to a Span column). The string/int/float kind
// drives the coercion applied to the resolved source value.
//
// Mapping example (Options.FieldMap):
//
//	{
//	  "trace_id":            "trace.id",
//	  "span_id":             "id",
//	  "provider":            "request.provider",
//	  "model":               "request.model",
//	  "input_tokens":        "usage.prompt_tokens",
//	  "output_tokens":       "usage.completion_tokens",
//	  "cached_input_tokens": "usage.cache_read",
//	  "total_cost_usd":      "cost",
//	  "timestamp":           "created_at",
//	  "agent_slug":          "meta.agent",
//	}
var genericTargets = map[string]string{
	"trace_id":            "string",
	"span_id":             "string",
	"parent_span_id":      "string",
	"agent_slug":          "string",
	"workflow_slug":       "string",
	"span_type":           "string",
	"span_name":           "string",
	"status":              "string",
	"provider":            "string",
	"model":               "string",
	"tool_name":           "string",
	"input_tokens":        "int",
	"output_tokens":       "int",
	"cached_input_tokens": "int",
	"input_bytes":         "int",
	"output_bytes":        "int",
	"total_cost_usd":      "float",
	"timestamp":           "time",
	"end_timestamp":       "time",
}

// parseGeneric applies Options.FieldMap (target column → dotted source path) to
// each record in a JSON array (or {"data":[...]} wrapper). A missing/empty
// FieldMap is an error (fail-closed: a no-op mapping would silently ingest
// blank rows). Stable trace/span identity and source time mappings are required,
// required paths must resolve in every record. Optional configured paths may be
// absent from individual records, but each must resolve at least once so a
// misspelled mapping cannot silently fabricate blank fields. A malformed payload
// or mapping returns no rows.
func parseGeneric(data []byte, opts Options) ([]Span, error) {
	if err := validateGenericFieldMap(opts.FieldMap); err != nil {
		return nil, err
	}

	records, err := decodeGenericRecords(data)
	if err != nil {
		return nil, err
	}
	if len(records) == 0 {
		return []Span{}, nil
	}

	rows := make([]Span, 0, len(records))
	resolvedTargets := make(map[string]bool, len(opts.FieldMap))
	for i, rec := range records {
		sp, resolved, err := mapGeneric(rec, opts)
		if err != nil {
			return nil, fmt.Errorf("generic record %d: %w", i+1, err)
		}
		for target := range resolved {
			resolvedTargets[target] = true
		}
		rows = append(rows, sp)
	}
	for target, path := range opts.FieldMap {
		if !resolvedTargets[target] {
			return nil, fmt.Errorf("mapped source path %q for target %q was not found in any record", path, target)
		}
	}
	return rows, nil
}

func validateGenericFieldMap(fieldMap map[string]string) error {
	if len(fieldMap) == 0 {
		return fmt.Errorf("generic import requires a non-empty field map")
	}
	for target, path := range fieldMap {
		if _, ok := genericTargets[target]; !ok {
			return fmt.Errorf("unknown target column %q (not a caveman.spans column)", target)
		}
		if strings.TrimSpace(path) == "" {
			return fmt.Errorf("generic target %q requires a non-empty source path", target)
		}
	}
	for _, target := range []string{"trace_id", "span_id", "timestamp"} {
		if _, ok := fieldMap[target]; !ok {
			return fmt.Errorf("generic import requires a %q mapping", target)
		}
	}
	return nil
}

func decodeGenericRecords(data []byte) ([]map[string]any, error) {
	// Try a bare array first.
	var arr []map[string]any
	if err := json.Unmarshal(data, &arr); err == nil {
		return arr, nil
	}
	// Then a {"data":[...]} wrapper.
	var env struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(data, &env); err == nil && env.Data != nil {
		return env.Data, nil
	}
	return nil, fmt.Errorf("generic decode: expected a JSON array or {\"data\":[...]} of objects")
}

func mapGeneric(rec map[string]any, opts Options) (Span, map[string]bool, error) {
	var sp Span
	startNs, endNs := int64(0), int64(0)
	resolved := make(map[string]bool, len(opts.FieldMap))
	for target, path := range opts.FieldMap {
		kind := genericTargets[target]
		val, found := lookupPath(rec, path)
		if !found {
			if target == "trace_id" || target == "span_id" || target == "timestamp" {
				return Span{}, nil, fmt.Errorf("mapped source path %q for required target %q was not found", path, target)
			}
			continue
		}
		resolved[target] = true
		switch kind {
		case "string":
			assignString(&sp, target, asString(val))
		case "int":
			assignInt(&sp, target, parseFlexInt(val))
		case "float":
			sp.TotalCostUSD = roundUSD(parseFlexFloat(val))
		case "time":
			s, ns := parseTimeFlexible(val)
			if target == "timestamp" {
				sp.Timestamp = s
				startNs = ns
			} else {
				sp.EndTimestamp = s
				endNs = ns
			}
		}
	}
	if strings.TrimSpace(sp.TraceID) == "" {
		return Span{}, nil, fmt.Errorf("mapped trace_id is empty")
	}
	if strings.TrimSpace(sp.SpanID) == "" {
		return Span{}, nil, fmt.Errorf("mapped span_id is empty")
	}
	if sp.Timestamp == "" {
		return Span{}, nil, fmt.Errorf("mapped timestamp is missing or invalid")
	}
	if d := durationMS(startNs, endNs); d > 0 {
		sp.DurationMS = d
	}
	sp.Attributes = map[string]string{"import.source": "generic"}
	applyScope(&sp, opts)
	return sp, resolved, nil
}

func assignString(sp *Span, target, v string) {
	switch target {
	case "trace_id":
		sp.TraceID = v
	case "span_id":
		sp.SpanID = v
	case "parent_span_id":
		sp.ParentSpanID = v
	case "agent_slug":
		sp.AgentSlug = v
	case "workflow_slug":
		sp.WorkflowSlug = v
	case "span_type":
		sp.SpanType = v
	case "span_name":
		sp.SpanName = v
	case "status":
		sp.Status = v
	case "provider":
		sp.Provider = v
	case "model":
		sp.Model = v
	case "tool_name":
		sp.ToolName = v
	}
}

func assignInt(sp *Span, target string, v uint64) {
	switch target {
	case "input_tokens":
		sp.InputTokens = v
	case "output_tokens":
		sp.OutputTokens = v
	case "cached_input_tokens":
		sp.CachedInputTokens = v
	case "input_bytes":
		sp.InputBytes = v
	case "output_bytes":
		sp.OutputBytes = v
	}
}

// lookupPath resolves a dotted path (e.g. "request.usage.prompt_tokens") into a
// nested map. Returns (value, true) when present.
func lookupPath(rec map[string]any, path string) (any, bool) {
	parts := strings.Split(path, ".")
	var cur any = rec
	for _, p := range parts {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		v, ok := m[p]
		if !ok {
			return nil, false
		}
		cur = v
	}
	return cur, true
}
