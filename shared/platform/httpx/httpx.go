package httpx

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/JuliusBrussee/caveman/shared/platform/id"
)

type ErrorEnvelope struct {
	Error ErrorBody `json:"error"`
}

type ErrorBody struct {
	Type    string `json:"type"`
	Code    string `json:"code"`
	Message string `json:"message"`
	// Details carries the machine-readable half of an error whose prose would
	// otherwise force clients to parse English (e.g. the policy-publish 409's
	// list of deactivations). Omitted entirely when there is nothing to say.
	Details   map[string]any `json:"details,omitempty"`
	RequestID string         `json:"request_id"`
	TraceID   string         `json:"trace_id,omitempty"`
}

func RequestID(r *http.Request) string {
	if v := r.Header.Get("x-cave-request-id"); v != "" {
		return v
	}
	return id.NewUUIDv7()
}

func JSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("content-type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func Error(w http.ResponseWriter, r *http.Request, status int, code, message string) {
	ErrorWithDetails(w, r, status, code, message, nil)
}

// ErrorWithDetails writes the same envelope as Error plus a "details" object the
// caller can act on programmatically. Server errors are redacted exactly as in
// Error — the message is replaced AND the details are dropped, so internals
// never leak through the structured half.
func ErrorWithDetails(w http.ResponseWriter, r *http.Request, status int, code, message string, details map[string]any) {
	if status >= http.StatusInternalServerError {
		message = publicServerErrorMessage(status)
		details = nil
	}
	requestID := RequestID(r)
	w.Header().Set("x-cave-request-id", requestID)
	JSON(w, status, ErrorEnvelope{Error: ErrorBody{
		Type:      "cave_gateway_error",
		Code:      code,
		Message:   message,
		Details:   details,
		RequestID: requestID,
		TraceID:   r.Header.Get("x-cave-trace-id"),
	}})
}

func publicServerErrorMessage(status int) string {
	switch status {
	case http.StatusBadGateway:
		return "Upstream service error."
	case http.StatusServiceUnavailable:
		return "Service temporarily unavailable."
	case http.StatusGatewayTimeout:
		return "Upstream service timed out."
	default:
		return "Internal server error."
	}
}

func DecodeJSON(r *http.Request, dst any, maxBytes int64) error {
	defer r.Body.Close()
	reader := http.MaxBytesReader(nil, r.Body, maxBytes)
	dec := json.NewDecoder(reader)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return err
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("request body must contain one JSON value")
		}
		return err
	}
	return nil
}
