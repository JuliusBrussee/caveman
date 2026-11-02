package providers

import (
	"encoding/json"
	"sort"
	"strings"
)

// PreservationAudit is content-blind diagnostics for one request. It never
// includes result payloads, arguments, call IDs, or inferred server identities.
type PreservationAudit struct {
	Protocol          string
	Supported         bool
	Tools             []string
	ProtectedResults  int
	UnresolvedResults int
	AmbiguousIDs      int
}

// AuditPreservation reports actual identities visible in supported protocol
// envelopes. The adapters remain authoritative for exclusion and byte splicing.
func AuditPreservation(body []byte, meta RequestMetadata) PreservationAudit {
	var root map[string]any
	report := PreservationAudit{}
	if !meta.PreserveTools.Enabled() || json.Unmarshal(body, &root) != nil {
		return report
	}
	if meta.Provider == "bedrock" || meta.Provider == "vertex" {
		report.Protocol = "unsupported"
		return report
	}
	text := func(m map[string]any, k string) string { s, _ := m[k].(string); return s }
	objects := func(v any) []map[string]any {
		values, _ := v.([]any)
		out := make([]map[string]any, 0, len(values))
		for _, v := range values {
			if m, ok := v.(map[string]any); ok {
				out = append(out, m)
			}
		}
		return out
	}
	calls := map[string]bool{}
	names := map[string]bool{}
	seen := map[string]string{}
	add := func(id, name string) {
		if id == "" {
			return
		}
		if previous, ok := seen[id]; ok && previous != name {
			report.AmbiguousIDs++
		}
		seen[id] = name
		matched := meta.PreserveTools.Matches(name)
		calls[id] = calls[id] || matched || name == ""
		if matched {
			names[name] = true
		}
	}
	result := func(id string) {
		_, known := calls[id]
		if !known || id == "" || seen[id] == "" {
			report.UnresolvedResults++
		}
		if ProtectedResult(calls, id, true) {
			report.ProtectedResults++
		}
	}
	switch {
	case strings.Contains(meta.Endpoint, "count_tokens") || strings.Contains(strings.ToLower(meta.Endpoint), "counttokens") || strings.HasSuffix(meta.Endpoint, "/input_tokens"):
		report.Protocol = "count-only-pass-through"
		report.Supported = true
	case strings.Contains(meta.Endpoint, "messages"):
		report.Protocol = "anthropic-messages"
		report.Supported = true
		messages := objects(root["messages"])
		for _, m := range messages {
			if text(m, "role") == "assistant" {
				for _, b := range objects(m["content"]) {
					if text(b, "type") == "tool_use" {
						add(text(b, "id"), text(b, "name"))
					}
				}
			}
		}
		for _, m := range messages {
			if text(m, "role") == "user" {
				for _, b := range objects(m["content"]) {
					if text(b, "type") == "tool_result" {
						result(text(b, "tool_use_id"))
					}
				}
			}
		}
	case strings.Contains(meta.Endpoint, "responses"):
		report.Protocol = "openai-responses"
		report.Supported = true
		items := objects(root["input"])
		for _, v := range items {
			if typ := text(v, "type"); typ == "function_call" || typ == "custom_tool_call" {
				add(text(v, "call_id"), text(v, "name"))
			}
		}
		for _, v := range items {
			if typ := text(v, "type"); typ == "function_call_output" || typ == "custom_tool_call_output" {
				result(text(v, "call_id"))
			}
		}
	case strings.Contains(meta.Endpoint, "chat/completions"):
		report.Protocol = "openai-chat"
		report.Supported = true
		messages := objects(root["messages"])
		for _, m := range messages {
			if text(m, "role") == "assistant" {
				for _, c := range objects(m["tool_calls"]) {
					f, _ := c["function"].(map[string]any)
					add(text(c, "id"), text(f, "name"))
				}
			}
		}
		for _, m := range messages {
			if text(m, "role") == "tool" {
				result(text(m, "tool_call_id"))
			}
		}
	case strings.Contains(strings.ToLower(meta.Endpoint), "generatecontent"):
		report.Protocol = "gemini-generatecontent"
		report.Supported = true
		for _, c := range objects(root["contents"]) {
			if text(c, "role") != "user" {
				continue
			}
			for _, part := range objects(c["parts"]) {
				if response, ok := part["functionResponse"].(map[string]any); ok {
					name := text(response, "name")
					if name == "" {
						report.UnresolvedResults++
						report.ProtectedResults++
					} else if meta.PreserveTools.Matches(name) {
						names[name] = true
						report.ProtectedResults++
					}
				}
			}
		}
	default:
		report.Protocol = "unsupported"
	}
	for name := range names {
		report.Tools = append(report.Tools, name)
	}
	sort.Strings(report.Tools)
	return report
}
