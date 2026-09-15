package middleware

import (
	"bytes"
	"encoding/json"
	"strings"
)

// encoding/json assigns zero values to missing or null scalar fields. The
// protocol distinguishes those from explicit zero/false, so check presence
// before decoding into native Go values. DisallowUnknownFields still owns
// unknown-key rejection; validate owns bounds and cross-field invariants.
func requiredObject(raw json.RawMessage, fields, nullable string) (map[string]json.RawMessage, error) {
	var object map[string]json.RawMessage
	if json.Unmarshal(raw, &object) != nil || object == nil {
		return nil, Failure{"invalid_request"}
	}
	for _, field := range strings.Fields(fields) {
		value, ok := object[field]
		if !ok || (bytes.Equal(bytes.TrimSpace(value), []byte("null")) && !strings.Contains(" "+nullable+" ", " "+field+" ")) {
			return nil, Failure{"invalid_request"}
		}
	}
	return object, nil
}

func requestPresence(raw []byte, path string) error {
	fields, nullable := "schema_version scope", ""
	switch path {
	case RoutePrefix + "optimize":
		fields += " request_id logical_call_id attempt_id idempotency_key sequence adapter model mode policy segments context_manifest recovery_binding"
		nullable = "model recovery_binding"
	case RoutePrefix + "retrieve":
		fields += " handle offset limit query"
	case RoutePrefix + "receipts":
		fields += " logical_call_id attempt_id event_kind plan_id usage provider_request_sha256"
		nullable = "plan_id usage provider_request_sha256"
	case RoutePrefix + "sessions/delete":
	default:
		return nil
	}
	object, err := requiredObject(raw, fields, nullable)
	if err != nil {
		return err
	}
	if _, err := requiredObject(object["scope"], "namespace session_id branch_id cache_epoch", ""); err != nil {
		return err
	}
	if path == RoutePrefix+"optimize" {
		for _, child := range []struct{ name, fields string }{
			{"adapter", "id version framework_version serialization_revision"}, {"policy", "revision transforms"},
			{"model", "provider id protocol"}, {"recovery_binding", "id kind tool_name overhead_text"},
		} {
			if bytes.Equal(bytes.TrimSpace(object[child.name]), []byte("null")) {
				continue
			}
			if _, err := requiredObject(object[child.name], child.fields, ""); err != nil {
				return err
			}
		}
		for _, list := range []struct{ name, fields string }{
			{"segments", "id kind cache_region content sha256 source_id protected opaque"}, {"context_manifest", "id sha256"},
		} {
			var values []json.RawMessage
			if json.Unmarshal(object[list.name], &values) != nil || values == nil {
				return Failure{"invalid_request"}
			}
			for _, value := range values {
				if _, err := requiredObject(value, list.fields, ""); err != nil {
					return err
				}
			}
		}
	}
	if path == RoutePrefix+"receipts" && !bytes.Equal(bytes.TrimSpace(object["usage"]), []byte("null")) {
		_, err := requiredObject(object["usage"], "provenance complete input_tokens output_tokens cache_read_tokens cache_write_tokens reasoning_tokens",
			"input_tokens output_tokens cache_read_tokens cache_write_tokens reasoning_tokens")
		return err
	}
	return nil
}
