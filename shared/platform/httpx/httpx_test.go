package httpx

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestErrorNeverExposesCallerMessageFor5xx(t *testing.T) {
	for _, status := range []int{http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout} {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		w := httptest.NewRecorder()
		Error(w, r, status, "cave_test", `pq: password authentication failed for user "secret"`)
		var body ErrorEnvelope
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if body.Error.Message == `pq: password authentication failed for user "secret"` {
			t.Fatalf("status %d exposed caller-controlled internal error", status)
		}
	}
}

func TestDecodeJSONRejectsTrailingAndOversizedBodies(t *testing.T) {
	t.Run("trailing value", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{} {}`))
		var dst map[string]any
		if err := DecodeJSON(r, &dst, 1024); err == nil {
			t.Fatal("trailing JSON accepted")
		}
	})
	t.Run("oversized", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"x":"`+strings.Repeat("a", 100)+`"}`))
		var dst map[string]any
		err := DecodeJSON(r, &dst, 32)
		var maxErr *http.MaxBytesError
		if !errors.As(err, &maxErr) {
			t.Fatalf("error = %v, want *http.MaxBytesError", err)
		}
	})
}
